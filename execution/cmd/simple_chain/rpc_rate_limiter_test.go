package main

import (
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

