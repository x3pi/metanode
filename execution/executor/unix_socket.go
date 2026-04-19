package executor

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/tracing"
	"context"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// SocketExecutor manages Unix domain socket / TCP connections from Rust to Go
type SocketExecutor struct {
	socketPath     string
	listener       net.Listener
	requestHandler *RequestHandler
	mu             sync.Mutex
	wg             sync.WaitGroup
	quit           chan struct{}
	isRunning      bool
}

// NewSocketExecutor creates a new SocketExecutor instance
func NewSocketExecutor(socketPath string, storageManager *storage.StorageManager, chainState *blockchain.ChainState, genesisPath string) *SocketExecutor {
	if socketPath == "" {
		socketPath = "/tmp/rust-go.sock"
	}
	return &SocketExecutor{
		socketPath:     socketPath,
		requestHandler: NewRequestHandler(storageManager, chainState, genesisPath),
		quit:           make(chan struct{}),
		isRunning:      false,
	}
}

// GetRequestHandler returns the request handler for further configuration (e.g., setting snapshot manager)
func (se *SocketExecutor) GetRequestHandler() *RequestHandler {
	return se.requestHandler
}

// listenAndServe starts listening and accepting connections
func (se *SocketExecutor) listenAndServe() error {
	// Use new socket abstraction layer
	socketConfig := NewSocketConfig(se.socketPath)

	listener, err := socketConfig.Listen()
	if err != nil {
		return fmt.Errorf("cannot start listener on socket %s: %w", socketConfig, err)
	}
	se.listener = listener
	logger.Info("[Go Server] Listening on %s (type: %s)", socketConfig, socketConfig.Network())
	// Connection accept loop
	se.wg.Add(1)
	go func() {
		defer se.wg.Done()
		for {
			// Accept new connection
			conn, err := se.listener.Accept()
			if err != nil {
				// Check if error is due to listener being closed (in Stop)
				select {
				case <-se.quit: // Stop() was called — expected error
					logger.Info("[Go Server] Stopped accepting connections.")
					return
				default:
					logger.Error("[Go Server] Error accepting connection: %v", err)
				}
				continue
			}
			logger.Debug("[Go Server] Rust client connected!")
			// Handle each connection in a separate goroutine
			se.wg.Add(1)
			go se.handleConnection(conn)
		}
	}()

	return nil
}

// handleConnection listens for requests from Rust and sends back responses
func (se *SocketExecutor) handleConnection(conn net.Conn) {
	defer se.wg.Done()
	defer conn.Close()

	// Optimize connection buffers
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
		tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetReadBuffer(32 * 1024 * 1024)
		_ = tcpConn.SetWriteBuffer(32 * 1024 * 1024)
	}
	if unixConn, ok := conn.(*net.UnixConn); ok {
		_ = unixConn.SetReadBuffer(32 * 1024 * 1024)
		_ = unixConn.SetWriteBuffer(32 * 1024 * 1024)
	}

	// Use bufio for Reader and Writer to ensure Protocol Uvarint/Fixed works and Flush
	reader := bufio.NewReaderSize(conn, 4*1024*1024)
	writer := bufio.NewWriterSize(conn, 4*1024*1024)

	for {
		var wrappedRequest pb.Request
		err := ReadMessage(reader, &wrappedRequest)
		if err != nil {
			if err == io.EOF {
				logger.Debug("[Go Server] Rust client disconnected.")
			} else {
				logger.Error("[Go Server] Error reading message: %v", err)
			}
			return
		}

		var wrappedResponse *pb.Response = se.requestHandler.ProcessProtobufRequest(&wrappedRequest)
		reqType := fmt.Sprintf("%T", wrappedRequest.GetPayload())

		tracer := tracing.GetTracer()
		_, span := tracer.Start(context.Background(), "SocketExecutor.handleRequest",
			trace.WithAttributes(attribute.String("request_type", reqType)))

		span.End()

		// Always send response (even on error)
		if wrappedResponse == nil {
			logger.Error("[Go Server] wrappedResponse is nil - this is a bug!")
			wrappedResponse = &pb.Response{
				Payload: &pb.Response_Error{
					Error: "Internal server error: response is nil",
				},
			}
		}

		if err := WriteMessage(writer, wrappedResponse); err != nil {
			logger.Error("[Go Server] Error sending response: %v", err)
			return
		}
		if err := writer.Flush(); err != nil {
			logger.Error("[Go Server] Error flushing response: %v", err)
			return
		}

	}
}

// Start initializes and starts the Socket Executor
func (se *SocketExecutor) Start() error {
	se.mu.Lock()
	if se.isRunning {
		se.mu.Unlock()
		return fmt.Errorf("SocketExecutor is already running")
	}
	se.isRunning = true
	se.mu.Unlock()

	// Start the server (no Connect() — we listen for incoming connections)
	if err := se.listenAndServe(); err != nil {
		se.mu.Lock()
		se.isRunning = false
		se.mu.Unlock()
		return err
	}

	logger.Info("[Go Server] SocketExecutor started successfully")
	return nil
}

// Stop shuts down the Socket Executor and closes all connections
func (se *SocketExecutor) Stop() error {
	se.mu.Lock()
	defer se.mu.Unlock()
	if !se.isRunning {
		return fmt.Errorf("SocketExecutor is not running")
	}
	close(se.quit)
	// Close listener to stop accepting new connections
	if se.listener != nil {
		se.listener.Close()
	}
	// Wait for all goroutines to finish
	se.wg.Wait()
	se.isRunning = false
	logger.Info("[Go Server] SocketExecutor stopped.")
	return nil
}

// RunSocketExecutor is a convenience function to create and start a SocketExecutor
func RunSocketExecutor(socketPath string, storageManager *storage.StorageManager, chainState *blockchain.ChainState, genesisPath string) (*SocketExecutor, error) {
	executor := NewSocketExecutor(socketPath, storageManager, chainState, genesisPath)
	if err := executor.Start(); err != nil {
		return nil, fmt.Errorf("failed to start SocketExecutor: %v", err)
	}

	return executor, nil
}
