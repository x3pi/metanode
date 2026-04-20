// network/server.go

package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

const (
	assumedParentConnectionType = "PARENT"
)

type SocketServer struct {
	connectionsManager     network.ConnectionsManager
	listener               net.Listener
	handler                network.Handler
	config                 *Config
	nodeType               string
	version                string
	keyPair                *bls.KeyPair
	ctx                    context.Context
	cancelFunc             context.CancelFunc
	onConnectedCallBack    []func(connection network.Connection)
	onDisconnectedCallBack []func(connection network.Connection)
	requestChan            chan network.Request
	wg                     sync.WaitGroup
}

func NewSocketServer(
	cfg *Config,
	keyPair *bls.KeyPair,
	connectionsManager network.ConnectionsManager,
	handler network.Handler,
	nodeType string,
	version string,
) (network.SocketServer, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if keyPair == nil {
		return nil, errors.New("NewSocketServer: keyPair cannot be nil")
	}
	if connectionsManager == nil {
		return nil, errors.New("NewSocketServer: connectionsManager cannot be nil")
	}
	if handler == nil {
		return nil, errors.New("NewSocketServer: handler cannot be nil")
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	s := &SocketServer{
		config:             cfg,
		keyPair:            keyPair,
		connectionsManager: connectionsManager,
		handler:            handler,
		nodeType:           nodeType,
		version:            version,
		ctx:                ctx,
		cancelFunc:         cancelFunc,
		requestChan:        make(chan network.Request, cfg.RequestChanSize),
	}
	return s, nil
}

func (s *SocketServer) StartWorkerPool() {
	workerCount := s.config.HandlerWorkerPoolSize
	logger.Info("Starting Worker Pool with %d workers...", workerCount)
	logger.Info("Worker Pool Config - RequestChanSize: %d, NumCPU: %d",
		s.config.RequestChanSize, runtime.NumCPU())

	s.wg.Add(workerCount)
	activeWorkers := int32(0)
	processedRequests := int64(0)

	for i := 0; i < workerCount; i++ {
		go func(workerID int) {
			defer s.wg.Done()
			for {
				select {
				case <-s.ctx.Done():
					return
				case request, ok := <-s.requestChan:
					if !ok {
						return
					}
					if request == nil {
						continue
					}

					func() {
						atomic.AddInt32(&activeWorkers, 1)
						defer atomic.AddInt32(&activeWorkers, -1)
						defer atomic.AddInt64(&processedRequests, 1)

						// Ensure request is put back into the pool after handling
						defer func() {
							if req, ok := request.(*Request); ok {
								requestPool.Put(req)
							}
						}()

						if err := s.handler.HandleRequest(request); err != nil {
							logger.Warn(
								"Worker %d: Error from handler while processing request from %s (Command: %s): %v",
								workerID,
								request.Connection().RemoteAddrSafe(),
								request.Message().Command(),
								err,
							)
						}
					}()
				}
			}
		}(i)
	}

	// Log worker pool stats định kỳ
	go func() {
		ticker := time.NewTicker(60 * time.Second) // Log mỗi 60 giây
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				active := atomic.LoadInt32(&activeWorkers)
				processed := atomic.LoadInt64(&processedRequests)
				queueLen := len(s.requestChan)

				// Lấy thống kê connections
				stats := s.connectionsManager.Stats()
				totalConnections := 0
				connectionsByType := make(map[string]int)
				for typeName, count := range stats.TotalConnectionByType {
					totalConnections += int(count)
					connectionsByType[typeName] = int(count)
				}

				// Log chi tiết
				logger.Info(
					"Worker Pool Stats - Active: %d/%d, Queue: %d/%d, Processed: %d, TotalConnections: %d",
					active, workerCount, queueLen, s.config.RequestChanSize, processed, totalConnections,
				)

				// Log connections by type
				if len(connectionsByType) > 0 {
					logger.Info("Connections by type: %v", connectionsByType)
				}

				// Tính số goroutines ước tính (mỗi connection = 2-3 goroutines: readLoop, writeLoop, HandleConnection)
				estimatedGoroutinesFromConnections := totalConnections * 3
				logger.Info(
					"Estimated goroutines from connections: ~%d (connections: %d × 3)",
					estimatedGoroutinesFromConnections, totalConnections,
				)

				// Cảnh báo nếu có quá nhiều connections
				if totalConnections > 500 {
					logger.Warn("⚠️ Có quá nhiều connections (%d), có thể gây vấn đề về memory và goroutines", totalConnections)
				}

				// Cảnh báo nếu queue quá dài
				if queueLen > s.config.RequestChanSize/2 {
					logger.Warn("⚠️ Request queue đang dài (%d/%d), có thể cần tăng worker pool", queueLen, s.config.RequestChanSize)
				}

				// Cảnh báo nếu hơn 50% workers đang active
				if active > int32(workerCount/2) {
					logger.Warn("⚠️ Hơn 50%% workers đang active (%d/%d), có thể cần tăng worker pool", active, workerCount)
				}
			}
		}
	}()
}

// THIS FUNCTION IS NOW MORE ROBUST
func (s *SocketServer) HandleConnection(conn network.Connection) error {
	requestChan, errorChan := conn.RequestChan()
	if requestChan == nil || errorChan == nil {
		return errors.New("request or error channel is nil")
	}

	// NOTE: ReadRequest() là empty function, không cần tạo goroutine
	// Logic đọc thực sự nằm trong readLoop() được tạo trong connection.run()
	// go conn.ReadRequest() // REMOVED: Không cần thiết, gây goroutine leak

	// Ensure the connection is cleaned up when this handler exits for any reason
	defer func() {
		logger.Info("HandleConnection: Exiting for %s. Cleaning up.", conn.RemoteAddrSafe())
		s.OnDisconnect(conn)  // This calls RemoveConnection
		_ = conn.Disconnect() // This closes the TCP conn and channels
	}()

	for {
		select {
		case <-s.ctx.Done():
			logger.Info("HandleConnection: Server context done, closing connection %s", conn.RemoteAddrSafe())
			return s.ctx.Err()

		case request, ok := <-requestChan:
			if !ok {
				logger.Info("HandleConnection: Request channel closed for %s.", conn.RemoteAddrSafe())
				return errors.New("connection request channel closed")
			}
			if request == nil {
				continue
			}
			// Funnel the request to the central server channel
			cmd := request.Message().Command()
			select {
			case s.requestChan <- request:
			// Success
			default:
				// Server's main channel is full, connection is too fast
				logger.Warn(
					"HandleConnection: Server's central request channel is full. Dropping request from %s (Command: %s)",
					conn.RemoteAddrSafe(),
					cmd,
				)
				// QUAN TRỌNG: Đặc biệt log error nếu đây là InitConnection request
				// Vì connection sẽ không được add vào manager
				if cmd == "InitConnection" {
					logger.Error(
						"HandleConnection: CRITICAL - Dropping InitConnection request! Connection sẽ không được add vào manager!",
						"remoteAddr", conn.RemoteAddrSafe(),
					)
				}
				// Put request back into the pool as it's not being handled
				if req, ok := request.(*Request); ok {
					requestPool.Put(req)
				}
				// Inform the client that the server is busy
				busyMsg := generateMessage(conn.Address(), p_common.ServerBusy, nil, s.version)
				_ = conn.SendMessage(busyMsg)
			}

		case err, ok := <-errorChan:
			if !ok {
				logger.Info("HandleConnection: Error channel closed for %s.", conn.RemoteAddrSafe())
				return errors.New("connection error channel closed")
			}
			// Log the specific error that caused the reader to exit
			if !(errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)) {
				logger.Error("HandleConnection: Unrecoverable error on connection %s: %v. Closing connection.", conn.RemoteAddrSafe(), err)
			} else {
				logger.Info("HandleConnection: Connection %s closed by remote peer (EOF).", conn.RemoteAddrSafe())
			}
			return err // Exit the handler, triggering the defer
		}
	}
}

func (s *SocketServer) Listen(listenAddress string) error {
	s.StartWorkerPool()

	var err error
	s.listener, err = net.Listen("tcp", listenAddress)
	if err != nil {
		return fmt.Errorf("error listening on %s: %w", listenAddress, err)
	}
	defer s.listener.Close()
	logger.Info("Server listening on %s", listenAddress)

	for {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		default:
			if tcpListener, ok := s.listener.(*net.TCPListener); ok {
				_ = tcpListener.SetDeadline(time.Now().Add(1 * time.Second))
			}

			tcpConn, err := s.listener.Accept()
			if err != nil {
				if opError, ok := err.(net.Error); ok && opError.Timeout() {
					continue
				}
				if s.ctx.Err() != nil {
					return s.ctx.Err()
				}
				logger.Warn("Listen: Error accepting connection: %v", err)
				continue
			}

			logger.Info("Listen: Accepted new connection from %s", tcpConn.RemoteAddr().String())

			conn, errCreation := ConnectionFromTcpConnection(tcpConn, s.config)
			if errCreation != nil {
				logger.Error("Listen: Error creating Connection from TCP conn: %v", errCreation)
				_ = tcpConn.Close()
				continue
			}

			// OnConnect and HandleConnection are now both handled here
			go func() {
				s.OnConnect(conn)
				_ = s.HandleConnection(conn)
			}()
		}
	}
}

func (s *SocketServer) Stop() {
	logger.Info("Stopping server...")
	s.cancelFunc()
	if listener := s.listener; listener != nil {
		_ = listener.Close()
	}
	// Close the central request channel to signal workers to stop
	if s.requestChan != nil {
		close(s.requestChan)
	}
	// Wait for worker pool to finish processing remaining requests
	s.wg.Wait()
	logger.Info("All workers have shut down.")
	if h, ok := s.handler.(interface{ Shutdown() }); ok {
		h.Shutdown()
	}
	logger.Info("Server stopped.")
}

func (s *SocketServer) OnConnect(conn network.Connection) {
	fmt.Printf("[ONCONNECT] ===== OnConnect được gọi =====\n")
	fmt.Printf("[ONCONNECT] Remote address: %s\n", conn.RemoteAddrSafe())
	fmt.Printf("[ONCONNECT] Node type: %s\n", s.nodeType)

	var addressForInitMsgBytes []byte
	parentConn := s.connectionsManager.ParentConnection()
	if parentConn != nil {
		addressForInitMsgBytes = parentConn.Address().Bytes()
		fmt.Printf("[ONCONNECT] Sử dụng parent address: %s\n", parentConn.Address().Hex())
	} else {
		addressForInitMsgBytes = s.keyPair.Address().Bytes()
		fmt.Printf("[ONCONNECT] Không có parent, sử dụng keyPair address: %s\n", s.keyPair.Address().Hex())
	}
	initMsg := &pb.InitConnection{
		Address: addressForInitMsgBytes,
		Type:    s.nodeType,
	}
	fmt.Printf("[ONCONNECT] Đang gửi InitConnection message (type: %s, address: %x)...\n", s.nodeType, addressForInitMsgBytes)
	err := SendMessage(conn, p_common.InitConnection, initMsg, s.version)
	if err != nil {
		fmt.Printf("[ONCONNECT] ❌ Lỗi khi gửi InitConnection: %v\n", err)
		logger.Warn("OnConnect: Error sending INIT message to %s: %v", conn.RemoteAddrSafe(), err)
	} else {
		fmt.Printf("[ONCONNECT] ✅ Đã gửi InitConnection message thành công\n")
	}
	for _, callBack := range s.onConnectedCallBack {
		callBack(conn)
	}
	fmt.Printf("[ONCONNECT] ===== OnConnect hoàn tất =====\n")
}

func (s *SocketServer) OnDisconnect(conn network.Connection) {
	if conn == nil {
		return
	}
	logger.Info("OnDisconnect: Removing connection %s from connection manager.", conn.RemoteAddrSafe())
	s.connectionsManager.RemoveConnection(conn)
	for _, callBack := range s.onDisconnectedCallBack {
		callBack(conn)
	}
}

func (s *SocketServer) AddOnConnectedCallBack(callBack func(network.Connection)) {
	s.onConnectedCallBack = append(s.onConnectedCallBack, callBack)
}

func (s *SocketServer) AddOnDisconnectedCallBack(callBack func(network.Connection)) {
	s.onDisconnectedCallBack = append(s.onDisconnectedCallBack, callBack)
}

func (s *SocketServer) SetContext(ctx context.Context, cancelFunc context.CancelFunc) {
	if ctx == nil || cancelFunc == nil {
		return
	}
	if s.cancelFunc != nil {
		s.cancelFunc()
	}
	s.cancelFunc = cancelFunc
	s.ctx = ctx
}

func (s *SocketServer) RetryConnectToParent(disconnectedParentConn network.Connection) {
	clonedParentConn := disconnectedParentConn.Clone()
	go func(connToRetry network.Connection) {
		logger.Info("Starting to retry connection to parent: %s", connToRetry.RemoteAddrSafe())
		for {
			select {
			case <-s.ctx.Done():
				logger.Info("Stopping parent retry due to server context shutdown.")
				return
			default:
			}
			err := connToRetry.Connect()
			if err == nil {
				logger.Info("Successfully reconnected to parent: %s", connToRetry.RemoteAddrSafe())
				s.connectionsManager.AddParentConnection(connToRetry)
				s.OnConnect(connToRetry)
				go s.HandleConnection(connToRetry)
				return
			}
			logger.Warn("Failed to reconnect to parent, will retry in %s. Error: %v", s.config.RetryParentInterval, err)
			select {
			case <-time.After(s.config.RetryParentInterval):
			case <-s.ctx.Done():
				logger.Info("Stopping parent retry due to server context shutdown.")
				return
			}
		}
	}(clonedParentConn)
}

func (s *SocketServer) SetKeyPair(newKeyPair *bls.KeyPair) {
	if newKeyPair == nil {
		return
	}
	s.keyPair = newKeyPair
}

func (s *SocketServer) Context() context.Context {
	return s.ctx
}

func (s *SocketServer) DebugStatus() {}
