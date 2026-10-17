package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRPCRateLimiter_ConcurrentAccessRace ensures concurrent requests from the same IP
// do not cause data races when updating lastSeen and blockedUntil timestamps.
func TestRPCRateLimiter_ConcurrentAccessRace(t *testing.T) {
	cfg := &config.RpcRateLimitConfig{
		Enabled:           true,
		GlobalRate:        1000,
		PerIpRate:         500,
		BlockDurationSecs: 2,
	}
	rl := NewRPCRateLimiter(cfg)
	defer rl.Close()

	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := rl.Middleware(dummyHandler)

	const goroutines = 50
	const requestsPerGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < requestsPerGoroutine; j++ {
				req := httptest.NewRequest("POST", "/", nil)
				req.RemoteAddr = "192.168.1.100:12345"
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
			}
		}()
	}

	wg.Wait()
	stats := rl.GetStats()
	require.NotNil(t, stats)
	assert.Equal(t, 1, stats["tracked_ips"])
}

// TestRPCRateLimiter_IPBanning verifies that exceeding per-IP rate triggers a temporary ban with HTTP 429
func TestRPCRateLimiter_IPBanning(t *testing.T) {
	cfg := &config.RpcRateLimitConfig{
		Enabled:           true,
		GlobalRate:        1000,
		PerIpRate:         5, // Low limit: burst is max(10, 20) = 20
		BlockDurationSecs: 1,
	}
	rl := NewRPCRateLimiter(cfg)
	defer rl.Close()

	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := rl.Middleware(dummyHandler)

	// Send requests until burst is depleted and IP is banned
	banned := false
	for i := 0; i < 50; i++ {
		req := httptest.NewRequest("POST", "/", nil)
		req.RemoteAddr = "10.0.0.1:54321"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusTooManyRequests {
			banned = true
			assert.Contains(t, rec.Body.String(), "Rate limit exceeded. IP Blocked")
			break
		}
	}

	assert.True(t, banned, "Spamming IP should be banned with HTTP 429")

	// Immediately following request from the same IP must still be blocked
	req := httptest.NewRequest("POST", "/", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// Another IP should not be affected
	otherReq := httptest.NewRequest("POST", "/", nil)
	otherReq.RemoteAddr = "10.0.0.2:54321"
	otherRec := httptest.NewRecorder()
	handler.ServeHTTP(otherRec, otherReq)
	assert.Equal(t, http.StatusOK, otherRec.Code)
}

// TestRPCRateLimiter_ExemptEndpoints verifies /metrics and /health bypass rate limiting
func TestRPCRateLimiter_ExemptEndpoints(t *testing.T) {
	cfg := &config.RpcRateLimitConfig{
		Enabled:           true,
		GlobalRate:        1,
		PerIpRate:         1,
		BlockDurationSecs: 10,
	}
	rl := NewRPCRateLimiter(cfg)
	defer rl.Close()

	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := rl.Middleware(dummyHandler)

	for _, path := range []string{"/metrics", "/metrics/json", "/health"} {
		for i := 0; i < 10; i++ {
			req := httptest.NewRequest("GET", path, nil)
			req.RemoteAddr = "1.2.3.4:9999"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code, "Path %s should bypass rate limiter", path)
		}
	}
}

// TestRPCRateLimiter_WebSocketPerMessageLimiting verifies that messages sent
// inside an established WebSocket connection are individually rate limited,
// preventing evasion where sending 50 RPC calls over 1 connection only counts as 1 request.
func TestRPCRateLimiter_WebSocketPerMessageLimiting(t *testing.T) {
	cfg := &config.RpcRateLimitConfig{
		Enabled:           true,
		GlobalRate:        1000,
		PerIpRate:         10, // burst is 20
		BlockDurationSecs: 2,
	}
	rl := NewRPCRateLimiter(cfg)
	defer rl.Close()

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	wsEchoHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				break
			}
			if err := conn.WriteMessage(mt, message); err != nil {
				break
			}
		}
	})

	ts := httptest.NewServer(rl.WebsocketMiddleware(wsEchoHandler))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer clientConn.Close()

	// Send 50 messages over this SINGLE WebSocket connection
	const msgCount = 50
	var successfulSends int
	for i := 0; i < msgCount; i++ {
		payload := []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)
		if err := clientConn.WriteMessage(websocket.TextMessage, payload); err != nil {
			break
		}
		_ = clientConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, _, err := clientConn.ReadMessage()
		if err != nil {
			break
		}
		successfulSends++
	}

	stats := rl.GetStats()
	totalAllowed := stats["total_allowed"].(int64)
	totalRejected := stats["total_rejected"].(int64)

	// In the old bug, totalAllowed was 0 (or 1 from handshake) and all 50 messages bypassed limiting.
	// With the fix, totalAllowed + totalRejected must count multiple messages (more than 1)!
	assert.Greater(t, totalAllowed+totalRejected, int64(1), "Rate limiter must count messages inside WebSocket, not just handshake")
	assert.Greater(t, totalRejected, int64(0), "Spamming 50 messages when burst is 20 must trigger rate limiting rejection")
	assert.Less(t, successfulSends, msgCount, "Connection should be blocked/terminated before all 50 messages succeed")
}

// TestRPCRateLimiter_WebSocketRejectedFrameNotDeliveredToDecoder verifies that when
// rate limiting rejects a WebSocket frame, Read() returns 0 bytes and error, preventing
// bufio.Reader from buffering and delivering rejected bytes to the JSON-RPC decoder.
func TestRPCRateLimiter_WebSocketRejectedFrameNotDeliveredToDecoder(t *testing.T) {
	cfg := &config.RpcRateLimitConfig{
		Enabled:           true,
		GlobalRate:        1000,
		PerIpRate:         1,
		BlockDurationSecs: 5,
	}
	rl := NewRPCRateLimiter(cfg)
	defer rl.Close()

	// Ban IP immediately
	entry := rl.getIPEntry("192.168.1.50")
	entry.blockedUntilUnixNano.Store(time.Now().Add(5 * time.Second).UnixNano())

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	wrapped := newWSRateLimitConn(serverConn, nil, "192.168.1.50", rl)
	br := bufio.NewReader(wrapped)

	// Send an unmasked text frame containing an RPC call from clientConn
	payload := []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)
	frameHeader := []byte{0x81, byte(len(payload))} // FIN=1, Opcode=1 (Text), unmasked
	frameData := append(frameHeader, payload...)

	go func() {
		_, _ = clientConn.Write(frameData)
	}()

	buf := make([]byte, 256)
	n, err := br.Read(buf)

	// CRITICAL CHECK: Must return 0 bytes and an error
	assert.Error(t, err, "Decoder read must return an error when limiter rejects the frame")
	assert.Equal(t, 0, n, "Bufio reader must not receive any bytes from a rejected frame")
}

// TestRPCRateLimiter_WebSocketBatchRPCAccounting verifies that batch RPCs over WebSocket
// are accounted by the number of requests in the batch, rather than counting 1 per message.
func TestRPCRateLimiter_WebSocketBatchRPCAccounting(t *testing.T) {
	cfg := &config.RpcRateLimitConfig{
		Enabled:           true,
		GlobalRate:        1000,
		PerIpRate:         10, // burst is max(20, 20) = 20
		BlockDurationSecs: 2,
	}
	rl := NewRPCRateLimiter(cfg)
	defer rl.Close()

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	wsEchoHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				break
			}
			if err := conn.WriteMessage(mt, message); err != nil {
				break
			}
		}
	})

	ts := httptest.NewServer(rl.WebsocketMiddleware(wsEchoHandler))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// 1. Test batch within quota (5 calls, burst is 20)
	clientConn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	var batch5 []string
	for i := 1; i <= 5; i++ {
		batch5 = append(batch5, fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":%d}`, i))
	}
	batchPayload5 := []byte("[" + strings.Join(batch5, ",") + "]")

	err = clientConn1.WriteMessage(websocket.TextMessage, batchPayload5)
	require.NoError(t, err)

	_ = clientConn1.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, echoResp, err := clientConn1.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, batchPayload5, echoResp)
	clientConn1.Close()

	stats1 := rl.GetStats()
	// Handshake = 1, Batch = 5 -> total_allowed = 6
	assert.Equal(t, int64(6), stats1["total_allowed"].(int64), "Quota must accurately account for 5 batch RPCs + 1 handshake")

	// 2. Test batch exceeding burst quota (100 calls in single message, burst is 20)
	// Use another IP by creating a new limiter
	rl2 := NewRPCRateLimiter(cfg)
	defer rl2.Close()

	ts2 := httptest.NewServer(rl2.WebsocketMiddleware(wsEchoHandler))
	defer ts2.Close()
	wsURL2 := "ws" + strings.TrimPrefix(ts2.URL, "http")

	clientConn2, _, err := websocket.DefaultDialer.Dial(wsURL2, nil)
	require.NoError(t, err)
	defer clientConn2.Close()

	var batch100 []string
	for i := 1; i <= 100; i++ {
		batch100 = append(batch100, fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":%d}`, i))
	}
	batchPayload100 := []byte("[" + strings.Join(batch100, ",") + "]")

	err = clientConn2.WriteMessage(websocket.TextMessage, batchPayload100)
	require.NoError(t, err)

	_ = clientConn2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _, err = clientConn2.ReadMessage()
	assert.Error(t, err, "Batch of 100 requests must be rejected when burst limit is 20")

	stats2 := rl2.GetStats()
	assert.GreaterOrEqual(t, stats2["total_rejected"].(int64), int64(100), "Rejection counter must reflect 100 RPC calls in batch, not 1")
}

// TestRPCRateLimiter_WebSocketHandshakeLimiting verifies that rapid opening of WebSocket
// connections consumes rate limit tokens and triggers temporary IP banning to prevent socket exhaustion.
func TestRPCRateLimiter_WebSocketHandshakeLimiting(t *testing.T) {
	cfg := &config.RpcRateLimitConfig{
		Enabled:           true,
		GlobalRate:        1000,
		PerIpRate:         5, // burst is max(10, 20) = 20
		BlockDurationSecs: 1,
	}
	rl := NewRPCRateLimiter(cfg)
	defer rl.Close()

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	wsDummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	})

	ts := httptest.NewServer(rl.WebsocketMiddleware(wsDummyHandler))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Rapidly open 40 connections without sending messages
	var rejectedCount int
	for i := 0; i < 40; i++ {
		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			rejectedCount++
			if resp != nil {
				assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
			}
		} else {
			conn.Close()
		}
	}

	assert.Greater(t, rejectedCount, 0, "Rapid WebSocket connection opening must hit rate limiter and be rejected")
}

