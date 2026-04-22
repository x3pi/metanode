package connection_client

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	pkg_com "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
)

// TransactionErrorResponse wraps a TransactionError body so callers can distinguish
// it from a successful receipt ([]byte) in the pendingRequests dispatch.
type TransactionErrorResponse struct {
	Body []byte
}

// TransactionSuccessResponse wraps a TransactionSuccess body (txHash bytes from chain).
type TransactionSuccessResponse struct {
	Body []byte
}

// ConnectionClient manages connection to a specific blockchain cluster
type ConnectionClient struct {
	key               string
	connectionAddress string
	connection        t_network.Connection
	messageSender     t_network.MessageSender
	// Key là RequestID hoặc TxHash, Value là channel để trả data về cho hàm gọi
	dispatchCh      chan t_network.Request
	pendingRequests sync.Map
	connected       int32 // 0 = false, 1 = true, atomic
	ctx             context.Context
	cancel          context.CancelFunc
	errorNotifyChan chan error // Channel để nhận error từ connection
}

func NewConnectionClient(ctx context.Context, key string, connectionAddress string, messageSender t_network.MessageSender) *ConnectionClient {
	clientCtx, cancel := context.WithCancel(ctx)
	return &ConnectionClient{
		key:               key,
		connectionAddress: connectionAddress,
		messageSender:     messageSender,
		ctx:               clientCtx,
		cancel:            cancel,
		errorNotifyChan:   make(chan error, 1),
	}
}

// Connection trả về underlying TCP connection (dùng cho keep-alive ping)
func (c *ConnectionClient) Connection() t_network.Connection {
	return c.connection
}

func (c *ConnectionClient) Connect() error {
	if atomic.LoadInt32(&c.connected) == 1 {
		return nil
	}
	// Create new connection — dùng address unique từ key để server không replace
	clientAddr := crypto.Keccak256Hash([]byte(c.key))
	conn := network.NewConnection(common.BytesToAddress(clientAddr.Bytes()), "m_client", nil)
	conn.SetRealConnAddr(c.connectionAddress)
	// Connect
	if err := conn.Connect(); err != nil {
		return fmt.Errorf("failed to connect to cluster %s at %s: %w", c.key, c.connectionAddress, err)
	}
	c.connection = conn
	atomic.StoreInt32(&c.connected, 1) // true

	// QUAN TRỌNG: Gửi InitConnection message tới server NGAY SAU khi TCP connect.
	// Server's HandleConnection có initReady gate chặn TẤT CẢ commands khác
	// cho đến khi nhận được InitConnection. Nếu không gửi, gate chỉ mở sau 30s timeout.
	initMsg := &pb.InitConnection{
		Address: clientAddr.Bytes()[:20], // Unique address per connection
		Type:    "m_client",
		Replace: true, // Không replace connection cùng type
	}
	if err := c.messageSender.SendMessage(conn, pkg_com.InitConnection, initMsg); err != nil {
		logger.Warn("Connect: Failed to send InitConnection to %s: %v (server gate will timeout after 30s)", c.connectionAddress, err)
	} else {
		logger.Info("Connect: InitConnection sent to %s, server gate unblocked", c.connectionAddress)
	}

	// Start monitoring errorChan in a separate goroutine
	c.initWorkerPool()
	go c.readLoop()
	go c.startKeepAlive()

	logger.Info("Connected to cluster %s at %s", c.key, c.connectionAddress)
	return nil
}

// startKeepAlive gửi Ping mỗi 30s để giữ connection sống.
// Không cần nhận Pong vì TCP tự ACK.
func (c *ConnectionClient) startKeepAlive() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if atomic.LoadInt32(&c.connected) == 0 {
				return
			}
			conn := c.connection
			if conn == nil {
				return
			}
			if err := c.messageSender.SendBytes(conn, "Ping", nil); err != nil {
				logger.Warn("KeepAlive: ping failed for %s: %v", c.key, err)
			}
		}
	}
}
func (c *ConnectionClient) readLoop() {
	conn := c.connection
	msgChan, errChan := conn.RequestChan()
	if msgChan == nil || errChan == nil {
		logger.Error("Channels are nil for cluster %s", c.key)
		c.handleConnectionError(fmt.Errorf("channels are nil"))
		return
	}
	for {
		select {
		case <-c.ctx.Done():
			return
		case err := <-errChan:
			if err != nil {
				if err != nil {
					logger.Error("Connection error received from cluster %s: %v", c.key, err)
					c.handleConnectionError(err)
					return
				}
			}
		case req := <-msgChan:
			if req == nil {
				c.handleConnectionError(fmt.Errorf("msgChan closed or received nil"))
				return
			}
			select {
			case c.dispatchCh <- req:
			default:
				logger.Warn("dispatch queue full, dropping message")
			}
		}
	}
}
func (c *ConnectionClient) initWorkerPool() {
	const dispatchWorkerCount = 8 // tuỳ CPU
	c.dispatchCh = make(chan t_network.Request, 200)
	for i := 0; i < dispatchWorkerCount; i++ {
		go func() {
			for {
				select {
				case <-c.ctx.Done():
					return
				case req := <-c.dispatchCh:
					c.dispatchMessage(req)
				}
			}
		}()
	}
}

func (c *ConnectionClient) dispatchMessage(req t_network.Request) {
	msg := req.Message()
	if msg == nil {
		logger.Error("Received nil message in connection error handler for cluster %s", c.key)
		return
	}
	cmd := msg.Command()
	body := msg.Body()

	if cmd == pkg_com.InitConnection {
		return
	}
	var id string
	var responseData interface{}

	switch cmd {
	case cmdBlockNumber:
		id = msg.ID()
		if len(body) >= 8 {
			responseData = binary.BigEndian.Uint64(body)
		} else {
			logger.Error("BlockNumber: body too short: %d bytes", len(body))
			return
		}
	case cmdReceipt:
		id = msg.ID()
		bodyCopy := make([]byte, len(body))
		copy(bodyCopy, body)
		responseData = bodyCopy
	case cmdTransactionError:
		id = msg.ID()
		bodyCopy := make([]byte, len(body))
		copy(bodyCopy, body)
		responseData = &TransactionErrorResponse{Body: bodyCopy}
	case cmdTransactionSuccess:
		id = msg.ID()
		bodyCopy := make([]byte, len(body))
		copy(bodyCopy, body)
		responseData = &TransactionSuccessResponse{Body: bodyCopy}
	case cmdAccountState:
		id = msg.ID()
		bodyCopy := make([]byte, len(body))
		copy(bodyCopy, body)
		responseData = bodyCopy
	case cmdDeviceKey:
		id = msg.ID()
		bodyCopy := make([]byte, len(body))
		copy(bodyCopy, body)
		responseData = bodyCopy
	case cmdTransactionReceipt:
		id = msg.ID()
		bodyCopy := make([]byte, len(body))
		copy(bodyCopy, body)
		responseData = bodyCopy
	case cmdNonce:
		id = msg.ID()
		if len(body) >= 8 {
			responseData = binary.BigEndian.Uint64(body)
		} else {
			logger.Error("Nonce: body too short: %d bytes", len(body))
			return
		}
	default:
		logger.Debug("Received unhandled command: %s", cmd)
		return
	}
	if id == "" {
		logger.Error("Empty id for command: %s", cmd)
		return
	}
	if val, ok := c.pendingRequests.Load(id); ok {
		respChan := val.(chan interface{})
		select {
		case respChan <- responseData:
		default:
			logger.Warn("Response channel full or blocked for request ID: %s", id)
		}
	} else {
		logger.Warn("Received response for unknown request ID: %s , key %s", id, c.key)
	}
}

func (c *ConnectionClient) handleConnectionError(connErr error) {
	select {
	case c.errorNotifyChan <- connErr:
	default:
		logger.Warn("Error notify channel full, dropping error for cluster %s", c.key)
	}
	c.Disconnect()

}

// GetErrorNotifyChan returns the error notification channel
func (c *ConnectionClient) GetErrorNotifyChan() <-chan error {
	return c.errorNotifyChan
}

// Disconnect closes the connection
func (c *ConnectionClient) Disconnect() {

	if atomic.LoadInt32(&c.connected) == 0 {
		return
	}
	if c.connection != nil {
		c.connection.Disconnect()
	}
	atomic.StoreInt32(&c.connected, 0) // false
	// Cancel context to stop monitoring
	if c.cancel != nil {
		c.cancel()
	}
	c.pendingRequests.Range(func(key, value interface{}) bool {
		c.pendingRequests.Delete(key)
		return true
	})
	logger.Info("Disconnected from cluster %s", c.key)
}

func (c *ConnectionClient) IsConnected() bool {
	return atomic.LoadInt32(&c.connected) == 1
}

// GetKey returns the key of the connection client
func (c *ConnectionClient) GetKey() string {
	return c.key
}

// GetBlockNumber sends a GetBlockNumber request and waits for the response
// Trả về uint64 trực tiếp, không cần proto wrapper
func (c *ConnectionClient) GetBlockNumber() (uint64, error) {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return 0, fmt.Errorf("not connected to cluster %s", c.key)
	}

	id := uuid.New().String()

	responseChan := make(chan interface{}, 1)
	c.pendingRequests.Store(id, responseChan)
	defer c.pendingRequests.Delete(id)

	msg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: cmdGetBlockNumber,
			ID:      id,
		},
	})

	if err := c.connection.SendMessage(msg); err != nil {
		return 0, fmt.Errorf("failed to send GetBlockNumber request: %w", err)
	}

	for {
		select {
		case res := <-responseChan:
			if bn, ok := res.(uint64); ok {
				return bn, nil
			}
			return 0, fmt.Errorf("invalid response type")
		case err := <-c.errorNotifyChan:
			return 0, fmt.Errorf("connection error: %w", err)
		case <-time.After(30 * time.Second):
			return 0, fmt.Errorf("timeout waiting for GetBlockNumber")
		case <-c.ctx.Done():
			return 0, fmt.Errorf("context cancelled")
		}
	}
}
