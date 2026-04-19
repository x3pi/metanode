// network/server.go

package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
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
	closeOnce              sync.Once     // GO-H3: ensure requestChan closed exactly once
	connSem                chan struct{} // GO-M2: max concurrent connections semaphore
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
	maxConns := cfg.MaxConnections
	if maxConns <= 0 {
		maxConns = 1000
	}
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
		connSem:            make(chan struct{}, maxConns),
	}
	return s, nil
}

func (s *SocketServer) startWorkerPool() {
	workerCount := s.config.HandlerWorkerPoolSize

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
						atomic.AddInt64(&processedRequests, 1)

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

	// Log worker pool stats periodically
	go s.logStats(&activeWorkers, &processedRequests, workerCount)
}

// HandleConnection processes commands from a single connection.
// RACE CONDITION FIXES:
//  1. InitConnection uses BLOCKING send (never dropped even when requestChan is full)
//  2. initReady gate ensures all other commands WAIT until InitConnection is queued
//     This guarantees ProcessInitConnection runs before SendTransaction for the same client
func (s *SocketServer) HandleConnection(conn network.Connection) error {
	logger.Info("⚠️  [SERVER DEBUG] HandleConnection started for remote: %v", conn.RemoteAddrSafe())
	requestChan, errorChan := conn.RequestChan()
	if requestChan == nil || errorChan == nil {
		return errors.New("request or error channel is nil")
	}

	// Ensure the connection is cleaned up when this handler exits for any reason
	defer func() {
		s.OnDisconnect(conn)  // This calls RemoveConnection
		_ = conn.Disconnect() // This closes the TCP conn and channels
	}()

	// ─── InitReady Gate ─────────────────────────────────────────────────────
	// All non-InitConnection commands MUST wait until InitConnection has been
	// successfully queued to the worker pool. This prevents race conditions
	// where SendTransaction is processed before ProcessInitConnection.
	initReady := make(chan struct{})
	initOnce := &sync.Once{}

	// Timeout: if no InitConnection arrives within 30s, unblock to avoid deadlock
	// (e.g. internal connections that don't send InitConnection)
	go func() {
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-initReady:
			// Already signaled, nothing to do
		case <-timer.C:
			initOnce.Do(func() {
				logger.Warn("HandleConnection: InitConnection not received within 30s from %s, unblocking gate",
					conn.RemoteAddrSafe())
				close(initReady)
			})
		}
	}()

	for {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()

		case request, ok := <-requestChan:
			if !ok {
				return errors.New("connection request channel closed")
			}
			if request == nil {
				continue
			}

			cmd := request.Message().Command()

			if cmd == p_common.InitConnection {
				// ─── InitConnection: BLOCKING send (NEVER drop) ──────────────
				// Use blocking send with timeout to guarantee InitConnection
				// reaches the worker pool. This fixes Race 4.
				select {
				case s.requestChan <- request:
					logger.Info("HandleConnection: InitConnection queued successfully for %s", conn.RemoteAddrSafe())
				case <-time.After(5 * time.Second):
					logger.Error(
						"HandleConnection: CRITICAL - InitConnection timed out (5s) waiting for queue space! remote=%s",
						conn.RemoteAddrSafe(),
					)
					// Still try non-blocking as last resort
					select {
					case s.requestChan <- request:
						logger.Info("HandleConnection: InitConnection queued (retry) for %s", conn.RemoteAddrSafe())
					default:
						logger.Error("HandleConnection: FATAL - InitConnection DROPPED for %s", conn.RemoteAddrSafe())
						if req, ok := request.(*Request); ok {
							requestPool.Put(req)
						}
					}
				}
				// Signal that InitConnection has been queued — unblock other commands
				initOnce.Do(func() { close(initReady) })
				continue
			}

			// ─── All other commands: WAIT for InitConnection first ────────
			// This fixes Race 1: ensures ProcessInitConnection runs before
			// SendTransaction/GetAccountState for the same client connection.
			select {
			case <-initReady:
				// InitConnection already queued, proceed
			case <-s.ctx.Done():
				return s.ctx.Err()
			}

			// Normal dispatch: non-blocking send to worker pool
			select {
			case s.requestChan <- request:
				logger.Info("⚠️  [SERVER DEBUG] Command queued to requestChan: %s", cmd)
				// Success
			default:
				logger.Warn(
					"HandleConnection: Server's central request channel is full. Dropping request from %s (Command: %s)",
					conn.RemoteAddrSafe(),
					cmd,
				)
				if req, ok := request.(*Request); ok {
					requestPool.Put(req)
				}
				busyMsg := generateMessage(conn.Address(), p_common.ServerBusy, nil, s.version)
				_ = conn.SendMessage(busyMsg)
			}

		case err, ok := <-errorChan:
			if !ok {
				return errors.New("connection error channel closed")
			}
			if !(errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)) {
				logger.Error("HandleConnection: Unrecoverable error on connection %s: %v. Closing connection.", conn.RemoteAddrSafe(), err)
			}
			return err
		}
	}
}

func (s *SocketServer) Listen(listenAddress string) error {
	s.startWorkerPool()

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

			conn, errCreation := ConnectionFromTcpConnection(tcpConn, s.config)
			if errCreation != nil {
				logger.Error("Listen: Error creating Connection from TCP conn: %v", errCreation)
				_ = tcpConn.Close()
				continue
			}
			// GO-M2: acquire connection slot (non-blocking) — refuse if at max capacity
			select {
			case s.connSem <- struct{}{}:
				// Slot acquired — handle connection in goroutine
				go func() {
					defer func() { <-s.connSem }() // release slot on disconnect
					s.OnConnect(conn)
					_ = s.HandleConnection(conn)
				}()
			default:
				logger.Warn("Listen: Max connections reached (%d), rejecting %s",
					cap(s.connSem), tcpConn.RemoteAddr())
				_ = tcpConn.Close()
			}
		}
	}
}

func (s *SocketServer) Stop() {
	logger.Info("Stopping server...")
	s.cancelFunc()
	if listener := s.listener; listener != nil {
		_ = listener.Close()
	}
	// GO-H3: use sync.Once to guarantee requestChan is closed exactly once,
	// even if Stop() is called concurrently. Workers drain via ctx.Done(),
	// so closing the channel after cancellation is safe.
	s.closeOnce.Do(func() {
		if s.requestChan != nil {
			close(s.requestChan)
		}
	})
	// Wait for worker pool to finish processing remaining requests
	s.wg.Wait()
	logger.Info("All workers have shut down.")
	if h, ok := s.handler.(interface{ Shutdown() }); ok {
		h.Shutdown()
	}
	logger.Info("Server stopped.")
}

func (s *SocketServer) OnConnect(conn network.Connection) {
	var addressForInitMsgBytes []byte
	parentConn := s.connectionsManager.ParentConnection()
	if parentConn != nil {
		addressForInitMsgBytes = parentConn.Address().Bytes()
		logger.Info("OnConnect: Using parentConn address: %v", parentConn.Address().Hex())
	} else {
		addressForInitMsgBytes = s.keyPair.Address().Bytes()
		logger.Info("OnConnect: Using BLS keyPair address: %v", s.keyPair.Address())
	}
	// }
	initMsg := &pb.InitConnection{
		Address: addressForInitMsgBytes,
		Type:    s.nodeType,
		Replace: true,
	}
	err := SendMessage(conn, p_common.InitConnection, initMsg, s.version)
	if err != nil {
		logger.Warn("OnConnect: Error sending INIT message to %s: %v", conn.RemoteAddrSafe(), err)
	}
	for _, callBack := range s.onConnectedCallBack {
		callBack(conn)
	}
}

func (s *SocketServer) OnDisconnect(conn network.Connection) {
	if conn == nil {
		return
	}
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

// func (s *SocketServer) RetryConnectToParent(disconnectedParentConn network.Connection) {
// 	clonedParentConn := disconnectedParentConn.Clone()

// 	// Copy realConnAddr from disconnected connection
// 	if disconnectedParentConn.TcpRemoteAddr() != nil {
// 		clonedParentConn.SetRealConnAddr(disconnectedParentConn.TcpRemoteAddr().String())
// 	}

// 	go func(connToRetry network.Connection) {
// 		retryCount := 0
// 		for {
// 			select {
// 			case <-s.ctx.Done():
// 				return
// 			default:
// 			}

//				retryCount++
//				err := connToRetry.Connect()
//				if err == nil {
//					logger.Info("[SocketServer] Successfully reconnected to parent after %d attempts", retryCount)
//					s.connectionsManager.AddParentConnection(connToRetry)
//					s.OnConnect(connToRetry)
//					go s.HandleConnection(connToRetry)
//					return
//				}
//				logger.Warn("[SocketServer] Failed to reconnect to parent (attempt #%d), will retry in %s. Error: %v",
//					retryCount, s.config.RetryParentInterval, err)
//				select {
//				case <-time.After(s.config.RetryParentInterval):
//				case <-s.ctx.Done():
//					return
//				}
//			}
//		}(clonedParentConn)
//	}
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
