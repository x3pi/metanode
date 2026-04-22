package proxy

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/internal/ws_writer"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/models"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  65536, // 64KB
	WriteBufferSize: 65536, // 64KB
}

// ServeWebSocketWithoutInterceptor - WebSocket không có interceptor, forward trực tiếp lên chain
func (p *RpcReverseProxy) ServeWebSocketWithoutInterceptor(w http.ResponseWriter, r *http.Request, targetURL string) {
	// Upgrade HTTP connection to WebSocket
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("WebSocket upgrade failed for %s: %v", r.RemoteAddr, err)
		return
	}
	defer clientConn.Close()

	clientWriter := ws_writer.NewWebSocketWriter(clientConn)
	// Validate target URL
	if targetURL == "" {
		logger.Error("Target WebSocket URL not configured for client %s", clientConn.RemoteAddr())
		_ = clientWriter.WriteCloseMessage(websocket.CloseInternalServerErr, "Target WebSocket URL not configured")
		return
	}
	// Connect to upstream WebSocket server
	targetConn, err := p.dialUpstreamWebSocket(targetURL, r, clientConn.RemoteAddr().String())
	if err != nil {
		logger.Error("Failed to connect to upstream WebSocket %s: %v", targetURL, err)
		_ = clientWriter.WriteCloseMessage(websocket.CloseGoingAway, fmt.Sprintf("Failed to connect to upstream: %v", err))
		return
	}
	defer targetConn.Close()

	targetWriter := ws_writer.NewWebSocketWriter(targetConn)
	// Proxy bidirectional traffic - không có interceptor
	p.proxyWebSocketTrafficWithoutInterceptor(clientConn, targetConn, clientWriter, targetWriter, r)
}

// ServeWebSocketWithInterceptor - WebSocket có interceptor, chặn lại và trả về RPC
func (p *RpcReverseProxy) ServeWebSocketWithInterceptor(w http.ResponseWriter, r *http.Request, targetURL string) {
	// Upgrade HTTP connection to WebSocket
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("WebSocket upgrade failed for %s: %v", r.RemoteAddr, err)
		return
	}
	defer func() {
		p.AppCtx.SubInterceptor.RemoveByConnection(clientConn)
		clientConn.Close()
	}()

	clientWriter := ws_writer.NewWebSocketWriter(clientConn)
	// Validate target URL
	if targetURL == "" {
		logger.Error("Target WebSocket URL not configured for client %s", clientConn.RemoteAddr())
		_ = clientWriter.WriteCloseMessage(websocket.CloseInternalServerErr, "Target WebSocket URL not configured")
		return
	}
	// Connect to upstream WebSocket server
	targetConn, err := p.dialUpstreamWebSocket(targetURL, r, clientConn.RemoteAddr().String())
	if err != nil {
		logger.Error("Failed to connect to upstream WebSocket %s: %v", targetURL, err)
		_ = clientWriter.WriteCloseMessage(websocket.CloseGoingAway, fmt.Sprintf("Failed to connect to upstream: %v", err))
		return
	}
	defer targetConn.Close()

	targetWriter := ws_writer.NewWebSocketWriter(targetConn)
	// Proxy bidirectional traffic - có interceptor
	p.proxyWebSocketTraffic(clientConn, targetConn, clientWriter, targetWriter, r)
}

// ServeWebSocket - Alias cho backward compatibility, mặc định dùng interceptor
func (p *RpcReverseProxy) ServeWebSocket(w http.ResponseWriter, r *http.Request, targetURL string) {
	p.ServeWebSocketWithInterceptor(w, r, targetURL)
}

// dialUpstreamWebSocket establishes connection to upstream WebSocket server
func (p *RpcReverseProxy) dialUpstreamWebSocket(targetURL string, r *http.Request, clientAddr string) (*websocket.Conn, error) {
	targetHeaders := make(http.Header)
	if origin := r.Header.Get("Origin"); origin != "" {
		targetHeaders.Set("Origin", origin)
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout:  15 * time.Second,
		ReadBufferSize:    65536,
		WriteBufferSize:   65536,
		EnableCompression: false,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // Skip TLS certificate verification
		},
		NetDialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	targetConn, resp, err := dialer.Dial(targetURL, targetHeaders)
	if err != nil {
		return p.handleDialError(err, resp, targetURL, clientAddr)
	}

	return targetConn, nil
}

// handleDialError xử lý chi tiết lỗi khi kết nối upstream WebSocket
func (p *RpcReverseProxy) handleDialError(err error, resp *http.Response, targetURL, clientAddr string) (*websocket.Conn, error) {
	// Default error message
	errMsg := fmt.Sprintf("Could not connect to target WebSocket %s", targetURL)
	fullErrMsgLog := fmt.Sprintf("%s for client %s: %v", errMsg, clientAddr, err)
	fullErrMsgClient := fmt.Sprintf("%s: %v", errMsg, err)

	if resp != nil {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)

		errMsg = fmt.Sprintf("Target WebSocket handshake failed with HTTP status %s", resp.Status)
		fullErrMsgLog = fmt.Sprintf("%s for client %s. Response Body: %s", errMsg, clientAddr, string(bodyBytes))
		fullErrMsgClient = fmt.Sprintf("%s. Server responded with: %s", errMsg, string(bodyBytes))
		// ✅ Xử lý case đặc biệt: 200 OK thay vì 101 Switching Protocols
		if resp.StatusCode == http.StatusOK {
			logger.Error("Handshake Error: Target server responded with 200 OK instead of 101 Switching Protocols. Ensure WSS URL path is correct (e.g., includes /ws if required).")
			fullErrMsgClient = "WebSocket handshake error: Target server returned HTTP 200 OK, not a WebSocket upgrade. Check WSS URL path in config."
		} else if resp.StatusCode >= http.StatusInternalServerError {
			fullErrMsgClient = fmt.Sprintf("Upstream server error (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
		} else if resp.StatusCode >= http.StatusBadRequest && resp.StatusCode < http.StatusInternalServerError {
			fullErrMsgClient = fmt.Sprintf("Client/config error (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
		}
	}
	logger.Error(fullErrMsgLog)

	return nil, fmt.Errorf("%s", fullErrMsgClient)
}

// proxyWebSocketTraffic handles bidirectional message forwarding
func (p *RpcReverseProxy) proxyWebSocketTraffic(
	clientConn, targetConn *websocket.Conn,
	clientWriter, targetWriter *ws_writer.WebSocketWriter,
	r *http.Request,
) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("PANIC in WebSocket proxy traffic handler: %v", r)
			// Close connections safely
			if clientConn != nil {
				_ = clientWriter.WriteCloseMessage(websocket.CloseInternalServerErr, "Internal server error")
				clientConn.Close()
			}
			if targetConn != nil {
				targetConn.Close()
			}
		}
	}()

	ctx := r.Context()
	errChan := make(chan error, 2)
	quit := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(2)
	// Goroutine 1: Client → Upstream (with RPC method handling)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("PANIC in client-to-upstream goroutine: %v", r)
				select {
				case errChan <- fmt.Errorf("client goroutine panic: %v", r):
				default:
				}
			}
		}()
		defer wg.Done()
		p.proxyClientToUpstream(clientConn, targetConn, clientWriter, targetWriter, errChan, quit)
	}()

	// Goroutine 2: Upstream → Client (passthrough)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("PANIC in upstream-to-client goroutine: %v", r)
				select {
				case errChan <- fmt.Errorf("upstream goroutine panic: %v", r):
				default:
				}
			}
		}()
		defer wg.Done()
		p.proxyUpstreamToClient(targetConn, clientWriter, errChan, quit)
	}()

	// Wait for error or context cancellation
	var finalError error
	select {
	case err := <-errChan:
		finalError = err
	case err := <-errChan:
		if finalError == nil {
			finalError = err
		}
	case <-ctx.Done():
		finalError = ctx.Err()
	}

	close(quit)

	// Send close message to client
	if finalError != nil {
		if !isExpectedCloseError(finalError) {
			logger.Error("WebSocket proxy error for %s: %v", clientConn.RemoteAddr(), finalError)
			_ = clientWriter.WriteCloseMessage(websocket.CloseInternalServerErr, "Proxy error")
		}
	} else {
		_ = clientWriter.WriteCloseMessage(websocket.CloseNormalClosure, "Connection closing normally")
	}

	// Wait for goroutines to finish
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Debug("WebSocket goroutines finished for %s", clientConn.RemoteAddr())
	case <-time.After(5 * time.Second):
		logger.Warn("Timeout waiting for WebSocket goroutines for %s", clientConn.RemoteAddr())
	}
}

// proxyClientToUpstreamWithoutInterceptor handles Client → Upstream traffic WITHOUT interceptor (forward trực tiếp)
func (p *RpcReverseProxy) proxyClientToUpstreamWithoutInterceptor(
	clientConn, targetConn *websocket.Conn,
	clientWriter, targetWriter *ws_writer.WebSocketWriter,
	errChan chan<- error,
	quit <-chan struct{},
) {
	for {
		select {
		case <-quit:
			return
		default:
		}
		// Read JSON-RPC request from client
		var req models.JSONRPCRequestRaw
		if err := clientConn.SetReadDeadline(time.Now().Add(180 * time.Second)); err != nil {
			logger.Warn("Error setting read deadline: %v", err)
		}
		readErr := clientConn.ReadJSON(&req)
		_ = clientConn.SetReadDeadline(time.Time{})

		if readErr != nil {
			if !isExpectedCloseError(readErr) {
				logger.Error("Error reading from client %s: %v", clientConn.RemoteAddr(), readErr)
				select {
				case errChan <- fmt.Errorf("client read error: %w", readErr):
				case <-quit:
				}
			}
			return
		}
		rpcResp, handled := p.RouteWebSocketMessage(req)
		if handled && rpcResp != nil {
			if err := clientWriter.WriteJSON(rpcResp); err != nil {
				logger.Error("Error writing RPC response to client %s: %v", clientConn.RemoteAddr(), err)
				select {
				case errChan <- fmt.Errorf("client write error: %w", err):
				case <-quit:
				}
				return
			}
		} else {
			if err := targetWriter.WriteJSON(req); err != nil {
				logger.Error("Error writing to upstream for client %s: %v", clientConn.RemoteAddr(), err)
				select {
				case errChan <- fmt.Errorf("upstream write error: %w", err):
				case <-quit:
				}
				return
			}
		}
	}
}

// proxyWebSocketTrafficWithoutInterceptor handles bidirectional message forwarding WITHOUT interceptor
func (p *RpcReverseProxy) proxyWebSocketTrafficWithoutInterceptor(
	clientConn, targetConn *websocket.Conn,
	clientWriter, targetWriter *ws_writer.WebSocketWriter,
	r *http.Request,
) {
	ctx := r.Context()
	errChan := make(chan error, 2)
	quit := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(2)
	// Goroutine 1: Client → Upstream (forward trực tiếp, không có interceptor)
	go func() {
		defer wg.Done()
		p.proxyClientToUpstreamWithoutInterceptor(clientConn, targetConn, clientWriter, targetWriter, errChan, quit)
	}()

	// Goroutine 2: Upstream → Client (passthrough)
	go func() {
		defer wg.Done()
		p.proxyUpstreamToClient(targetConn, clientWriter, errChan, quit)
	}()

	// Wait for error or context cancellation
	var finalError error
	select {
	case err := <-errChan:
		finalError = err
	case err := <-errChan:
		if finalError == nil {
			finalError = err
		}
	case <-ctx.Done():
		finalError = ctx.Err()
	}

	close(quit)
	// Send close message to client
	if finalError != nil {
		if !isExpectedCloseError(finalError) {
			logger.Error("WebSocket proxy error for %s: %v", clientConn.RemoteAddr(), finalError)
			_ = clientWriter.WriteCloseMessage(websocket.CloseInternalServerErr, "Proxy error")
		}
	} else {
		_ = clientWriter.WriteCloseMessage(websocket.CloseNormalClosure, "Connection closing normally")
	}

	// Wait for goroutines to finish
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Debug("WebSocket goroutines finished for %s", clientConn.RemoteAddr())
	case <-time.After(5 * time.Second):
		logger.Warn("Timeout waiting for WebSocket goroutines for %s", clientConn.RemoteAddr())
	}
}

// proxyClientToUpstream handles Client → Upstream traffic with RPC routing (WITH interceptor)
func (p *RpcReverseProxy) proxyClientToUpstream(
	clientConn, targetConn *websocket.Conn,
	clientWriter, targetWriter *ws_writer.WebSocketWriter,
	errChan chan<- error,
	quit <-chan struct{},
) {
	for {
		select {
		case <-quit:
			return
		default:
		}
		// Read JSON-RPC request from client
		var req models.JSONRPCRequestRaw
		if err := clientConn.SetReadDeadline(time.Now().Add(180 * time.Second)); err != nil {
			logger.Warn("Error setting read deadline: %v", err)
		}
		readErr := clientConn.ReadJSON(&req)
		_ = clientConn.SetReadDeadline(time.Time{})

		if readErr != nil {
			if !isExpectedCloseError(readErr) {
				logger.Error("Error reading from client %s: %v", clientConn.RemoteAddr(), readErr)
				select {
				case errChan <- fmt.Errorf("client read error: %w", readErr):
				case <-quit:
				}
			}
			return
		}

		// Handle panic recovery for RPC processing
		func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("Panic in proxyClientToUpstream for method %s: %v", req.Method, r)
					errorResp := map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      req.Id,
						"error": map[string]interface{}{
							"code":    -32603,
							"message": "Internal server error",
						},
					}
					clientWriter.WriteJSON(errorResp)
				}
			}()

			if req.Method == "eth_subscribe" {
				if err := p.HandleSubscribeRequest(req, clientConn, targetConn, clientWriter, targetWriter, errChan, quit); err != nil {
					errorResp := map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      req.Id,
						"error": map[string]interface{}{
							"code":    -32602,
							"message": err.Error(),
						},
					}
					clientWriter.WriteJSON(errorResp)
				}
				return
			} else if req.Method == "eth_unsubscribe" {
				p.AppCtx.SubInterceptor.RemoveByConnection(clientConn)
			}

			// Try to handle RPC method locally
			rpcResp, handled := p.RouteWebSocketMessage(req)
			if handled && rpcResp != nil {
				if err := clientWriter.WriteJSON(rpcResp); err != nil {
					logger.Error("Error writing RPC response to client %s: %v", clientConn.RemoteAddr(), err)
					select {
					case errChan <- fmt.Errorf("client write error: %w", err):
					case <-quit:
					}
					return
				}
			} else {
				if err := targetWriter.WriteJSON(req); err != nil {
					logger.Error("Error writing to upstream for client %s: %v", clientConn.RemoteAddr(), err)
					select {
					case errChan <- fmt.Errorf("upstream write error: %w", err):
					case <-quit:
					}
					return
				}
			}
		}()
	}
}

// proxyUpstreamToClient handles Upstream → Client traffic (passthrough)
func (p *RpcReverseProxy) proxyUpstreamToClient(
	targetConn *websocket.Conn,
	clientWriter *ws_writer.WebSocketWriter,
	errChan chan<- error,
	quit <-chan struct{},
) {
	for {
		select {
		case <-quit:
			return
		default:
		}
		// Read message from upstream
		if err := targetConn.SetReadDeadline(time.Now().Add(180 * time.Second)); err != nil {
			logger.Warn("Error setting read deadline: %v", err)
		}
		messageType, message, readErr := targetConn.ReadMessage()
		_ = targetConn.SetReadDeadline(time.Time{})
		if readErr != nil {
			if !isExpectedCloseError(readErr) {
				logger.Error("Error reading from upstream: %v", readErr)
				select {
				case errChan <- fmt.Errorf("upstream read error: %w", readErr):
				case <-quit:
				}
			}
			return
		}

		// Forward message to client
		if err := clientWriter.WriteMessage(messageType, message); err != nil {
			logger.Error("Error writing to client: %v", err)
			select {
			case errChan <- fmt.Errorf("client write error: %w", err):
			case <-quit:
			}
			return
		}
	}
}

// isExpectedCloseError checks if error is an expected close error
func isExpectedCloseError(err error) bool {
	if err == io.EOF {
		return true
	}
	if websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseAbnormalClosure,
		websocket.CloseNoStatusReceived) {
		return true
	}
	if strings.Contains(err.Error(), "client read error") ||
		strings.Contains(err.Error(), "client write error") {
		return true
	}
	return false
}
