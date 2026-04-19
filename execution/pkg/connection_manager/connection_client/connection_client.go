package connection_client

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
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
	pb_cross "github.com/meta-node-blockchain/meta-node/pkg/proto/cross_chain_proto"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

type ConnectionClient struct {
	key               string
	clientId          string // Unique ID per ConnectionClient instance để tạo address duy nhất
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
	// Tạo clientId từ key kết hợp UUID để đảm bảo mỗi observer có 1 ID riêng rẽ
	clientId := fmt.Sprintf("%s-%s", key, uuid.New().String())
	return &ConnectionClient{
		key:               key,
		clientId:          clientId,
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
	// Create new connection — dùng address unique từ clientId để các observer không đá (replace) nhau
	clientAddr := crypto.Keccak256Hash([]byte(c.clientId))
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
		Replace: true,
	}
	if err := c.messageSender.SendMessage(conn, pkg_com.InitConnection, initMsg); err != nil {
		logger.Warn("Connect: Failed to send InitConnection to %s: %v (server gate will timeout after 30s)", c.connectionAddress, err)
	} else {
		logger.Info("Connect: InitConnection sent to %s, server gate unblocked", c.connectionAddress)
	}

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
	var err error

	switch cmd {
	case pkg_com.TransactionsByBlockNumber:
		// Special case: uses block number as key, not header ID
		resp := &pb.TransactionsWithBlockNumber{}
		if err = proto.Unmarshal(body, resp); err == nil {
			id = fmt.Sprintf("%d", resp.BlockNumber)
			responseData = resp
		} else {
			logger.Error("Unmarshal TransactionsWithBlockNumber error: %v", err)
		}
	case cmdTransactionReceipt:
		resp := &pb.GetTransactionReceiptResponse{}
		if err = proto.Unmarshal(body, resp); err == nil {
			id = msg.ID() // Dùng header ID thay vì resp.RequestId
			responseData = resp
		} else {
			logger.Error("Unmarshal GetTransactionReceiptResponse error: %v", err)
		}
	case cmdLogs:
		resp := &pb.GetLogsResponse{}
		if err = proto.Unmarshal(body, resp); err == nil {
			id = msg.ID() // Dùng header ID thay vì resp.RequestId
			responseData = resp
		} else {
			logger.Error("Unmarshal GetLogsResponse error: %v", err)
		}
	case cmdBlockNumber:
		if len(body) >= 8 {
			id = msg.ID()
			responseData = binary.BigEndian.Uint64(body)
		} else {
			logger.Error("BlockNumber: body too short: %d bytes", len(body))
		}
	case cmdReceipt:
		// Receipt response từ ReadTransaction — trả raw bytes để caller unmarshal
		id = msg.ID()
		if id != "" {
			bodyCopy := make([]byte, len(body))
			copy(bodyCopy, body)
			responseData = bodyCopy
		} else {
			logger.Debug("Receipt: no header ID, skipping (broadcast receipt)")
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

// VerifyContractTransaction sends a contract transaction verification request to observer and waits for ACK
func (c *ConnectionClient) VerifyTransaction(
	clusterId uint64,
	blockNumber uint64,
	fromAddress common.Address,
	toAddress common.Address,
	txHash common.Hash,
	amount *big.Int,
) (*pb_cross.CrossClusterTransferAck, error) {
	if atomic.LoadInt32(&c.connected) != 1 {
		return nil, fmt.Errorf("not connected to observer")
	}

	reqKey := txHash.Hex()
	responseChan := make(chan interface{}, 1)
	c.pendingRequests.Store(reqKey, responseChan)
	defer c.pendingRequests.Delete(reqKey)
	// Convert amount to bytes
	var amountBytes []byte
	if amount != nil {
		amountBytes = amount.Bytes()
	}
	// Create VerifyTransactionRequest proto message (reuse existing message)
	request := &pb_cross.VerifyTransactionRequest{
		NationId:    clusterId,
		BlockNumber: blockNumber,
		FromAddress: fromAddress.Bytes(),
		ToAddress:   toAddress.Bytes(),
		TxHash:      txHash.Bytes(),
		Amount:      amountBytes,
	}
	// Marshal the request
	requestBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal VerifyTransactionRequest: %w", err)
	}

	// Send request using VerifyTransaction command
	if err := c.messageSender.SendBytes(c.connection, pkg_com.VerifyTransaction, requestBytes); err != nil {
		return nil, fmt.Errorf("failed to send VerifyTransactionRequest: %w", err)
	}
	// Wait for CrossClusterTransferAck response with timeout
	for {
		select {
		case res := <-responseChan:
			if ack, ok := res.(*pb_cross.CrossClusterTransferAck); ok {
				return ack, nil
			}
			return nil, fmt.Errorf("invalid response type")
		case err := <-c.errorNotifyChan:
			return nil, fmt.Errorf("connection error: %w", err)
		case <-time.After(30 * time.Second):
			return nil, fmt.Errorf("timeout waiting for CrossClusterTransferAck")
		case <-c.ctx.Done():
			return nil, fmt.Errorf("context cancelled")
		}
	}
}
func (c *ConnectionClient) GetTransactionsByBlockNumber(blockNumber uint64) (*pb.TransactionsWithBlockNumber, error) {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return nil, fmt.Errorf("not connected to cluster %s", c.key)
	}
	conn := c.connection
	requestBytes := make([]byte, 8)
	blNumberStr := fmt.Sprintf("%d", blockNumber)
	binary.BigEndian.PutUint64(requestBytes, blockNumber)
	responseChan := make(chan interface{}, 1)
	c.pendingRequests.Store(blNumberStr, responseChan)
	defer c.pendingRequests.Delete(blNumberStr)
	if err := c.messageSender.SendBytes(conn, cmdGetTransactionsByBlockNumber, requestBytes); err != nil {
		return nil, fmt.Errorf("failed to send GetTransactionsByBlockNumber request: %w", err)
	}
	for {
		select {
		case res := <-responseChan:
			if ack, ok := res.(*pb.TransactionsWithBlockNumber); ok {
				return ack, nil
			}
			return nil, fmt.Errorf("invalid response type")
		case err := <-c.errorNotifyChan:
			return nil, fmt.Errorf("connection error: %w", err)
		case <-time.After(30 * time.Second):
			return nil, fmt.Errorf("timeout waiting for CrossClusterTransferAck")
		case <-c.ctx.Done():
			return nil, fmt.Errorf("context cancelled")
		}
	}
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
