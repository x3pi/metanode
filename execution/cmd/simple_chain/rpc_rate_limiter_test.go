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

// A batch larger than the bucket can never be admitted; it must be refused WITHOUT banning the IP (the caller did not
// spam, it sent one oversized request), and the IP must still be able to make normal calls right after.
func TestRPCRateLimiter_CheckRequestN_BatchTooLargeDoesNotBan(t *testing.T) {
	rl := NewRPCRateLimiter(&config.RpcRateLimitConfig{Enabled: true, GlobalRate: 1000, PerIpRate: 10, BlockDurationSecs: 60}) // burst 20
	defer rl.Close()

	err := rl.CheckRequestN("10.0.0.1", 21)
	require.ErrorIs(t, err, errRateBatchTooLarge)
	assert.NoError(t, rl.CheckRequestN("10.0.0.1", 1), "an oversized batch must not ban the IP")
}

func TestRPCRateLimiter_HTTPBatchCountedPerCallAndBodyPreserved(t *testing.T) {
	rl := NewRPCRateLimiter(&config.RpcRateLimitConfig{Enabled: true, GlobalRate: 1000, PerIpRate: 10, BlockDurationSecs: 60}) // burst 20
	defer rl.Close()

	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(strings.Builder)
		buf := make([]byte, 1024)
		for {
			n, err := r.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		got = b.String()
		w.WriteHeader(http.StatusOK)
	})
	h := rl.Middleware(next)

	batch := func(n int) string {
		items := make([]string, n)
		for i := range items {
			items[i] = fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":%d}`, i)
		}
		return "[" + strings.Join(items, ",") + "]"
	}
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.RemoteAddr = "10.0.0.7:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// A batch of 15 is admitted, consumes 15 tokens and reaches the handler byte for byte.
	b15 := batch(15)
	rec := post(b15)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, b15, got, "the batch body must be restored for the next handler")

	// 15 more calls do not fit in what is left of the bucket: refused (and the IP is banned, it really spammed).
	rec = post(batch(15))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// A single (non-batch) body passes through unchanged for a fresh IP.
	single := `{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(single))
	req.RemoteAddr = "10.0.0.8:1234"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, single, got)

	// An oversized batch (21 > burst 20) is refused with a clear message and does not ban.
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(batch(21)))
	req.RemoteAddr = "10.0.0.9:1234"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "exceeds the rate-limit burst")
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(single))
	req.RemoteAddr = "10.0.0.9:1234"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "the IP that sent an oversized batch must not be banned")
}

// wsFrame builds an unmasked client->server frame header for a payload of n bytes.
func wsFrameHeader(b0 byte, n int) []byte {
	switch {
	case n <= 125:
		return []byte{b0, byte(n)}
	case n <= 0xFFFF:
		return []byte{b0, 126, byte(n >> 8), byte(n)}
	default:
		h := []byte{b0, 127, 0, 0, 0, 0, 0, 0, 0, 0}
		for i := 0; i < 8; i++ {
			h[9-i] = byte(uint64(n) >> (8 * i))
		}
		return h
	}
}

// The wrapper holds fragments back until the message is complete, so the reassembled size must be capped: endless
// FIN=0 frames used to grow memory without bound because no downstream read limit ever saw a byte.
func TestRPCRateLimiter_WebSocketFragmentedMessageIsCapped(t *testing.T) {
	rl := NewRPCRateLimiter(&config.RpcRateLimitConfig{Enabled: true, GlobalRate: 1000, PerIpRate: 1000, BlockDurationSecs: 5})
	defer rl.Close()
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	wrapped := newWSRateLimitConn(serverConn, nil, "192.168.1.60", rl)

	chunk := make([]byte, 5<<20)
	for i := range chunk {
		chunk[i] = ' '
	}
	go func() {
		_, _ = clientConn.Write(append(wsFrameHeader(0x01, len(chunk)), chunk...)) // text, FIN=0
		_, _ = clientConn.Write(append(wsFrameHeader(0x00, len(chunk)), chunk...)) // continuation, FIN=0: 10 MB > cap
	}()
	// drain the 1009 close frame the wrapper writes so the pipe write does not block
	go func() { buf := make([]byte, 16); _, _ = clientConn.Read(buf) }()

	buf := make([]byte, 64)
	n, err := wrapped.Read(buf)
	assert.Error(t, err, "a fragmented message above the cap must fail the read")
	assert.Equal(t, 0, n, "no byte of an over-cap message may reach the decoder")
}

// No extension is negotiated, so a frame with a reserved bit (e.g. RSV1 = permessage-deflate) cannot be counted:
// it must be refused, not mis-counted.
func TestRPCRateLimiter_WebSocketReservedBitRejected(t *testing.T) {
	rl := NewRPCRateLimiter(&config.RpcRateLimitConfig{Enabled: true, GlobalRate: 1000, PerIpRate: 1000, BlockDurationSecs: 5})
	defer rl.Close()
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	wrapped := newWSRateLimitConn(serverConn, nil, "192.168.1.61", rl)

	payload := []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)
	go func() { _, _ = clientConn.Write(append(wsFrameHeader(0xC1, len(payload)), payload...)) }() // FIN + RSV1 + text
	go func() { buf := make([]byte, 16); _, _ = clientConn.Read(buf) }()

	buf := make([]byte, 128)
	n, err := wrapped.Read(buf)
	assert.Error(t, err)
	assert.Equal(t, 0, n)
}

// A batch padded with more than the old 512-byte look-ahead of whitespace must still be counted per call, and padding
// beyond the hard cap is refused instead of slipping through as "1 call".
func TestRPCRateLimiter_HTTPBatchPaddedWithWhitespaceStillCounted(t *testing.T) {
	rl := NewRPCRateLimiter(&config.RpcRateLimitConfig{Enabled: true, GlobalRate: 1000, PerIpRate: 10, BlockDurationSecs: 60}) // burst 20
	defer rl.Close()
	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(strings.Builder)
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		got = b.String()
		w.WriteHeader(http.StatusOK)
	})
	h := rl.Middleware(next)
	items := make([]string, 30)
	for i := range items {
		items[i] = `{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`
	}
	batch := "[" + strings.Join(items, ",") + "]"

	for name, pad := range map[string]int{"padded 1000 (> old 512 window)": 1000} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat(" ", pad)+batch))
		req.RemoteAddr = "10.1.0.1:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusTooManyRequests, rec.Code, name+": a 30-call batch exceeds burst 20 whatever the padding")
	}

	// Padding past the cap is refused outright.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat(" ", maxHTTPLeadingWhitespace+10)+batch))
	req.RemoteAddr = "10.1.0.2:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// A small padded batch passes and the exact bytes (padding included) reach the handler.
	small := "[" + items[0] + "]"
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("   \n\t "+small))
	req.RemoteAddr = "10.1.0.3:1"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "   \n\t "+small, got)
}

// The per-IP table must stay bounded: past maxIPEntries distinct addresses, new ones share one overflow bucket.
func TestRPCRateLimiter_IPTableIsBounded(t *testing.T) {
	rl := NewRPCRateLimiter(&config.RpcRateLimitConfig{Enabled: true, GlobalRate: 100000, PerIpRate: 10, BlockDurationSecs: 60})
	defer rl.Close()
	for i := 0; i < maxIPEntries+500; i++ {
		_ = rl.getIPEntry(fmt.Sprintf("10.%d.%d.%d", (i>>16)&255, (i>>8)&255, i&255))
	}
	count := 0
	rl.perIP.Range(func(_, _ any) bool { count++; return true })
	assert.LessOrEqual(t, count, maxIPEntries, "tracked addresses must not exceed the cap")
	assert.Equal(t, int64(count), rl.ipCount.Load(), "the counter must match the table")
	assert.Same(t, rl.overflow, rl.getIPEntry("203.0.113.250"), "an address beyond the cap uses the shared overflow entry")
}
