package main

// rpc_rate_limiter.go — HTTP-level rate limiting for the Master's RPC server.
// Uses golang.org/x/time/rate (already a project dependency via routes.go).

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	"golang.org/x/time/rate"
)

const (
	// Per-IP limiter cache
	maxIPEntries   = 10000
	ipCleanupEvery = 5 * time.Minute
)

// Sentinel errors returned by CheckRequestN (the message strings are part of the log/test contract).
var (
	errRateIPBlocked      = errors.New("ip_blocked")
	errRateGlobalExceeded = errors.New("global_rate_exceeded")
	errRateIPBanned       = errors.New("ip_banned")
	errRateBatchTooLarge  = errors.New("batch_too_large")
)

// ipEntry holds a rate limiter and thread-safe timestamps for tracking.
type ipEntry struct {
	limiter              *rate.Limiter
	lastSeenUnixNano     atomic.Int64
	blockedUntilUnixNano atomic.Int64 // If set > Now(), all requests are instantly rejected
}

// RPCRateLimiter provides global and per-IP rate limiting for the RPC server.
type RPCRateLimiter struct {
	config *config.RpcRateLimitConfig
	global *rate.Limiter

	// PERFORMANCE: use sync.Map for lock-free reads on the hot path
	// Only cleanup needs a full scan via Range()
	perIP   sync.Map     // map[string]*ipEntry
	ipCount atomic.Int64 // entries in perIP (bounded by maxIPEntries)
	// overflow is the single shared entry used once perIP holds maxIPEntries addresses, so a flood of distinct source
	// addresses (spoofed or IPv6) cannot grow the table without bound; the excess shares one bucket.
	overflow *ipEntry
	closeCh  chan struct{}

	// PERFORMANCE: atomic counters — zero mutex contention
	totalAllowed  atomic.Int64
	totalRejected atomic.Int64
}

// NewRPCRateLimiter creates a new rate limiter with settings from config.
func NewRPCRateLimiter(cfg *config.RpcRateLimitConfig) *RPCRateLimiter {
	if cfg == nil {
		cfg = &config.RpcRateLimitConfig{Enabled: false}
	}

	// Allow burst to be slightly larger than rate
	globalBurst := cfg.GlobalRate * 2
	if globalBurst < 100 {
		globalBurst = 100
	}

	rl := &RPCRateLimiter{
		config:  cfg,
		global:  rate.NewLimiter(rate.Limit(cfg.GlobalRate), globalBurst),
		closeCh: make(chan struct{}),
	}
	rl.overflow = rl.newIPEntry(time.Now().UnixNano())

	if cfg.Enabled {
		go rl.cleanupLoop()
	}
	return rl
}

// cleanupLoop periodically removes stale per-IP entries.
func (rl *RPCRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(ipCleanupEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cutoffNano := time.Now().Add(-ipCleanupEvery).UnixNano()
			nowNano := time.Now().UnixNano()
			rl.perIP.Range(func(key, value any) bool {
				entry := value.(*ipEntry)
				// Clean up only if it hasn't been seen recently AND it's not currently blocked
				if entry.lastSeenUnixNano.Load() < cutoffNano && nowNano >= entry.blockedUntilUnixNano.Load() {
					if _, loaded := rl.perIP.LoadAndDelete(key); loaded {
						rl.ipCount.Add(-1)
					}
				}
				return true
			})
		case <-rl.closeCh:
			return
		}
	}
}

// Close stops the cleanup goroutine.
func (rl *RPCRateLimiter) Close() {
	close(rl.closeCh)
}

// perIPBurst is the token-bucket burst of a per-IP limiter; a single request carrying more calls than this can never
// be admitted, whatever the IP's history.
func (rl *RPCRateLimiter) perIPBurst() int {
	burst := rl.config.PerIpRate * 2
	if burst < 20 {
		burst = 20
	}
	return burst
}

func (rl *RPCRateLimiter) newIPEntry(nowNano int64) *ipEntry {
	e := &ipEntry{limiter: rate.NewLimiter(rate.Limit(rl.config.PerIpRate), rl.perIPBurst())}
	e.lastSeenUnixNano.Store(nowNano)
	return e
}

// getIPEntry returns the per-IP entry, creating one if needed.
// PERFORMANCE: Uses sync.Map for lock-free reads (hot path).
// Once maxIPEntries addresses are tracked, new addresses share the overflow entry instead of growing the table.
func (rl *RPCRateLimiter) getIPEntry(ip string) *ipEntry {
	nowNano := time.Now().UnixNano()
	if val, ok := rl.perIP.Load(ip); ok {
		entry := val.(*ipEntry)
		entry.lastSeenUnixNano.Store(nowNano)
		return entry
	}
	if rl.ipCount.Load() >= maxIPEntries {
		rl.overflow.lastSeenUnixNano.Store(nowNano)
		return rl.overflow
	}

	// Create new limiter — Store-or-Load pattern avoids duplicates
	actual, loaded := rl.perIP.LoadOrStore(ip, rl.newIPEntry(nowNano))
	if !loaded {
		rl.ipCount.Add(1)
	}
	return actual.(*ipEntry)
}

// extractIP returns just the IP portion of a remote address.
func extractIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// CheckRequest evaluates whether an individual RPC call from ip is permitted.
// If the IP is blocked or exceeds global or per-IP rate limits, it records the rejection,
// bans the IP if burst was exceeded, and returns an error.
// If allowed, it increments totalAllowed and returns nil.
func (rl *RPCRateLimiter) CheckRequest(ip string) error {
	return rl.CheckRequestN(ip, 1)
}

// CheckRequestN evaluates whether n RPC calls from ip are permitted.
// If the IP is blocked or exceeds global or per-IP rate limits, it records the rejection,
// bans the IP if burst was exceeded, and returns an error.
// If allowed, it increments totalAllowed by n and returns nil.
func (rl *RPCRateLimiter) CheckRequestN(ip string, n int) error {
	if rl == nil || !rl.config.Enabled {
		return nil
	}
	if n <= 0 {
		n = 1
	}

	entry := rl.getIPEntry(ip)
	now := time.Now()
	nowNano := now.UnixNano()
	blockedUntilNano := entry.blockedUntilUnixNano.Load()

	// Check if IP is currently blocked
	if nowNano < blockedUntilNano {
		rl.totalRejected.Add(int64(n))
		return errRateIPBlocked
	}

	// A batch larger than the bucket can never be admitted. Reject it without banning the IP: the caller did not
	// spam, it sent one oversized request, and banning would lock an honest wallet out for BlockDurationSecs.
	if n > rl.perIPBurst() || n > rl.global.Burst() {
		rl.totalRejected.Add(int64(n))
		return errRateBatchTooLarge
	}

	// Global rate check
	if !rl.global.AllowN(now, n) {
		rl.totalRejected.Add(int64(n))
		return errRateGlobalExceeded
	}

	// Check per-IP limiter
	if !entry.limiter.AllowN(now, n) {
		rl.totalRejected.Add(int64(n))

		// IP exceeds rate, BAN it
		blockedUntil := now.Add(time.Duration(rl.config.BlockDurationSecs) * time.Second)
		entry.blockedUntilUnixNano.Store(blockedUntil.UnixNano())
		logger.Warn("🚨 [RPC_RATE_LIMIT] IP %s spammed RPC (%d calls). Banned for %d seconds.", ip, n, rl.config.BlockDurationSecs)
		return errRateIPBanned
	}

	rl.totalAllowed.Add(int64(n))
	return nil
}

// Middleware wraps an http.Handler with rate limiting checks.
func (rl *RPCRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip entirely if disabled
		if !rl.config.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		// Skip rate limiting for metrics/health endpoints
		if r.URL.Path == "/metrics" || r.URL.Path == "/metrics/json" || r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		ip := extractIP(r.RemoteAddr)
		// A JSON-RPC batch (a top-level array) is N calls: count them all, exactly like the WebSocket path, so a
		// single HTTP POST cannot carry 1000 calls for the price of one token.
		n := httpRPCCallCount(r)
		if err := rl.CheckRequestN(ip, n); err != nil {
			entry := rl.getIPEntry(ip)
			switch {
			case errors.Is(err, errRateIPBlocked), errors.Is(err, errRateIPBanned):
				writeIPBlockedResponse(w, time.Unix(0, entry.blockedUntilUnixNano.Load()))
			case errors.Is(err, errRateBatchTooLarge):
				writeBatchTooLargeResponse(w, n, rl.perIPBurst())
			default:
				writeRateLimitResponse(w)
			}
			return
		}

		// ── Prometheus: track RPC request and duration ─────────────────
		method := r.URL.Path
		metrics.RPCRequestsTotal.WithLabelValues(method).Inc()
		start := time.Now()
		next.ServeHTTP(w, r)
		metrics.RPCDuration.WithLabelValues(method).Observe(time.Since(start).Seconds())
	})
}

const (
	maxWSFramePayload = 8 << 20 // 8 MB limit on one WebSocket frame payload
	// maxWSMessagePayload bounds the reassembled size of one fragmented message. The wrapper holds fragments back until
	// the message is complete, so without this a client could send endless FIN=0 frames and grow memory without limit
	// (the downstream read limit never sees a byte). Same 8 MB ceiling as the HTTP body limit of the sendRawTx path.
	maxWSMessagePayload = 8 << 20
	// maxHTTPBatchScan bounds how much of a POST body is read to count a JSON-RPC batch.
	maxHTTPBatchScan = 8 << 20
	// maxHTTPLeadingWhitespace bounds the padding accepted before the first JSON byte of an HTTP RPC body.
	maxHTTPLeadingWhitespace = 4096
)

// httpRPCCallCount returns how many JSON-RPC calls an HTTP request carries: the number of top-level elements for a
// batch (body starts with '['), 1 for everything else. The body is restored for the next handler.
func httpRPCCallCount(r *http.Request) int {
	if r.Method != http.MethodPost || r.Body == nil || r.Body == http.NoBody {
		return 1
	}
	orig := r.Body
	br := bufio.NewReaderSize(orig, 4096)
	// Skip leading JSON whitespace byte by byte (no fixed look-ahead window a client could pad past) and keep what was
	// consumed so the body can be restored exactly. A pathological amount of padding is refused as an oversized request.
	var lead bytes.Buffer
	first := byte(0)
	for {
		b, err := br.ReadByte()
		if err != nil {
			break
		}
		if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			if lead.Len() >= maxHTTPLeadingWhitespace {
				r.Body = struct {
					io.Reader
					io.Closer
				}{io.MultiReader(&lead, br), orig}
				return maxHTTPBatchScan // far above any burst: refused as batch_too_large
			}
			lead.WriteByte(b)
			continue
		}
		first = b
		_ = br.UnreadByte()
		break
	}
	if first != '[' {
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(&lead, br), orig}
		return 1
	}
	data, _ := io.ReadAll(io.LimitReader(br, maxHTTPBatchScan))
	full := append(append([]byte(nil), lead.Bytes()...), data...)
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(full), br), orig}
	return countBatchItems(full)
}

func writeBatchTooLargeResponse(w http.ResponseWriter, n, max int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"error": map[string]interface{}{
			"code":    -32005,
			"message": fmt.Sprintf("Batch of %d calls exceeds the rate-limit burst (%d). Split it into smaller batches.", n, max),
		},
		"id": nil,
	})
}

// countBatchItems scans a WebSocket JSON-RPC payload and returns the number of RPC requests.
// For a single request (JSON object), it returns 1.
// For a batch request (JSON array), it counts the top-level elements inside the array.
func countBatchItems(payload []byte) int {
	i := 0
	for i < len(payload) && (payload[i] == ' ' || payload[i] == '\t' || payload[i] == '\r' || payload[i] == '\n') {
		i++
	}
	if i >= len(payload) || payload[i] != '[' {
		return 1
	}
	i++ // skip '['

	count := 0
	inString := false
	escape := false
	depth := 0

	for ; i < len(payload); i++ {
		b := payload[i]
		if inString {
			if escape {
				escape = false
			} else if b == '\\' {
				escape = true
			} else if b == '"' {
				inString = false
			}
			continue
		}

		switch b {
		case '"':
			inString = true
		case '{', '[':
			if depth == 0 {
				count++
			}
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			} else if b == ']' {
				if count == 0 {
					return 1
				}
				return count
			}
		case ',':
			// Comma separates elements at depth 0
		default:
			// Non-whitespace primitive at depth 0
			if depth == 0 && b != ' ' && b != '\t' && b != '\r' && b != '\n' {
				count++
				for i+1 < len(payload) && payload[i+1] != ',' && payload[i+1] != ']' && payload[i+1] != ' ' && payload[i+1] != '\t' && payload[i+1] != '\r' && payload[i+1] != '\n' {
					i++
				}
			}
		}
	}
	if count == 0 {
		return 1
	}
	return count
}

type wsParsedFrame struct {
	fin      bool
	opcode   byte
	masked   bool
	maskKey  [4]byte
	rawBytes []byte
	payload  []byte
}

// wsRateLimitConn wraps the hijacked net.Conn of a WebSocket to inspect RFC 6455
// incoming frames and enforce rate limits on every individual JSON-RPC message, including batches.
type wsRateLimitConn struct {
	net.Conn
	initialReader io.Reader
	ip            string
	rl            *RPCRateLimiter

	readMu       sync.Mutex
	closed       bool
	rawIn        bytes.Buffer
	pendingRead  bytes.Buffer
	fragRawBytes bytes.Buffer
	fragPayload  bytes.Buffer
}

func newWSRateLimitConn(conn net.Conn, initialReader io.Reader, ip string, rl *RPCRateLimiter) *wsRateLimitConn {
	return &wsRateLimitConn{
		Conn:          conn,
		initialReader: initialReader,
		ip:            ip,
		rl:            rl,
	}
}

func (c *wsRateLimitConn) Close() error {
	c.readMu.Lock()
	c.closed = true
	c.rawIn.Reset()
	c.pendingRead.Reset()
	c.fragRawBytes.Reset()
	c.fragPayload.Reset()
	c.readMu.Unlock()
	return c.Conn.Close()
}

func (c *wsRateLimitConn) readRaw(buf []byte) (int, error) {
	if c.initialReader != nil {
		n, err := c.initialReader.Read(buf)
		if err == io.EOF {
			c.initialReader = nil
			if n > 0 {
				return n, nil
			}
			return c.Conn.Read(buf)
		}
		return n, err
	}
	return c.Conn.Read(buf)
}

func (c *wsRateLimitConn) tryReadFrame() (*wsParsedFrame, error) {
	data := c.rawIn.Bytes()
	if len(data) < 2 {
		return nil, nil
	}

	b0 := data[0]
	b1 := data[1]
	// No extension (e.g. permessage-deflate) is negotiated on this endpoint, so a reserved bit means the payload is not
	// plain JSON and cannot be counted. Fail closed with 1002 (protocol error) instead of mis-counting it.
	if b0&0x70 != 0 {
		_ = c.Conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		_, _ = c.Conn.Write([]byte{0x88, 0x02, 0x03, 0xea}) // 1002 Protocol Error
		_ = c.Conn.Close()
		return nil, errors.New("websocket frame uses reserved bits but no extension was negotiated")
	}
	fin := (b0 & 0x80) != 0
	opcode := b0 & 0x0f
	masked := (b1 & 0x80) != 0
	len7 := b1 & 0x7f

	extraLen := 0
	if len7 == 126 {
		extraLen = 2
	} else if len7 == 127 {
		extraLen = 8
	}

	maskLen := 0
	if masked {
		maskLen = 4
	}

	headerLen := 2 + extraLen + maskLen
	if len(data) < headerLen {
		return nil, nil
	}

	var payloadLen int64
	if len7 <= 125 {
		payloadLen = int64(len7)
	} else if len7 == 126 {
		payloadLen = int64(binary.BigEndian.Uint16(data[2:4]))
	} else {
		payloadLen = int64(binary.BigEndian.Uint64(data[2:10]))
	}

	if payloadLen < 0 || payloadLen > maxWSFramePayload {
		_ = c.Conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		_, _ = c.Conn.Write([]byte{0x88, 0x02, 0x03, 0xf1}) // 1009 Message Too Big
		_ = c.Conn.Close()
		return nil, fmt.Errorf("websocket frame payload too large: %d bytes (max: %d)", payloadLen, maxWSFramePayload)
	}

	totalLen := headerLen + int(payloadLen)
	if len(data) < totalLen {
		return nil, nil
	}

	rawBytes := c.rawIn.Next(totalLen)

	var maskKey [4]byte
	if masked {
		copy(maskKey[:], rawBytes[2+extraLen:2+extraLen+4])
	}

	payload := rawBytes[headerLen:]

	return &wsParsedFrame{
		fin:      fin,
		opcode:   opcode,
		masked:   masked,
		maskKey:  maskKey,
		rawBytes: rawBytes,
		payload:  payload,
	}, nil
}

func (c *wsRateLimitConn) handleFrame(frame *wsParsedFrame) error {
	// RFC 6455 Control frames (Close: 0x8, Ping: 0x9, Pong: 0xA) pass through directly
	if frame.opcode >= 0x8 {
		c.pendingRead.Write(frame.rawBytes)
		return nil
	}

	// Unmask payload
	unmasked := make([]byte, len(frame.payload))
	if frame.masked {
		for i := 0; i < len(frame.payload); i++ {
			unmasked[i] = frame.payload[i] ^ frame.maskKey[i%4]
		}
	} else {
		copy(unmasked, frame.payload)
	}

	// Check if this is part of a fragmented message
	isFragmented := !frame.fin || c.fragPayload.Len() > 0

	if isFragmented {
		if c.fragPayload.Len()+len(unmasked) > maxWSMessagePayload {
			_ = c.Conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			_, _ = c.Conn.Write([]byte{0x88, 0x02, 0x03, 0xf1}) // 1009 Message Too Big
			_ = c.Conn.Close()
			c.rawIn.Reset()
			c.pendingRead.Reset()
			c.fragRawBytes.Reset()
			c.fragPayload.Reset()
			return fmt.Errorf("websocket fragmented message exceeds %d bytes", maxWSMessagePayload)
		}
		c.fragRawBytes.Write(frame.rawBytes)
		c.fragPayload.Write(unmasked)

		if !frame.fin {
			// Await remaining fragments
			return nil
		}
	}

	var fullPayload []byte
	var allRawBytes []byte
	if isFragmented {
		fullPayload = c.fragPayload.Bytes()
		allRawBytes = c.fragRawBytes.Bytes()
	} else {
		fullPayload = unmasked
		allRawBytes = frame.rawBytes
	}

	// Count number of JSON-RPC requests (handles single calls and batch arrays)
	count := countBatchItems(fullPayload)

	if err := c.rl.CheckRequestN(c.ip, count); err != nil {
		// Policy violation: send close frame 1008 and terminate connection
		_ = c.Conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		_, _ = c.Conn.Write([]byte{0x88, 0x02, 0x03, 0xf0})
		_ = c.Conn.Close()

		c.rawIn.Reset()
		c.pendingRead.Reset()
		c.fragRawBytes.Reset()
		c.fragPayload.Reset()
		return fmt.Errorf("websocket rpc rate limit exceeded for ip %s (batch size %d): %w", c.ip, count, err)
	}

	metrics.RPCRequestsTotal.WithLabelValues("/ws").Add(float64(count))
	c.pendingRead.Write(allRawBytes)

	if isFragmented {
		c.fragRawBytes.Reset()
		c.fragPayload.Reset()
	}
	return nil
}

func (c *wsRateLimitConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	if c.closed {
		return 0, net.ErrClosed
	}

	for c.pendingRead.Len() == 0 {
		frame, err := c.tryReadFrame()
		if err != nil {
			return 0, err
		}
		if frame != nil {
			if err := c.handleFrame(frame); err != nil {
				return 0, err
			}
			continue
		}

		var buf [4096]byte
		n, err := c.readRaw(buf[:])
		if n > 0 {
			c.rawIn.Write(buf[:n])
		}
		if err != nil {
			if c.pendingRead.Len() > 0 {
				break
			}
			return 0, err
		}
	}

	return c.pendingRead.Read(p)
}

type wsHijackResponseWriter struct {
	http.ResponseWriter
	ip string
	rl *RPCRateLimiter
}

func (w *wsHijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not implement http.Hijacker")
	}
	conn, brw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	var initialReader io.Reader
	if brw != nil && brw.Reader != nil && brw.Reader.Buffered() > 0 {
		initialReader = brw.Reader
	}
	wrappedConn := newWSRateLimitConn(conn, initialReader, w.ip, w.rl)
	newBrw := bufio.NewReadWriter(bufio.NewReader(wrappedConn), bufio.NewWriter(wrappedConn))
	return wrappedConn, newBrw, nil
}

func (w *wsHijackResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// WebsocketMiddleware wraps a WebSocket http.Handler to enforce handshake and per-message rate limiting.
func (rl *RPCRateLimiter) WebsocketMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.config.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		ip := extractIP(r.RemoteAddr)
		entry := rl.getIPEntry(ip)

		now := time.Now()
		nowNano := now.UnixNano()
		blockedUntilNano := entry.blockedUntilUnixNano.Load()

		// Reject initial handshake if IP is currently banned
		if nowNano < blockedUntilNano {
			rl.totalRejected.Add(1)
			writeIPBlockedResponse(w, time.Unix(0, blockedUntilNano))
			return
		}

		// Enforce rate limit on the handshake request itself to prevent connection exhaustion
		if !rl.global.Allow() {
			rl.totalRejected.Add(1)
			writeRateLimitResponse(w)
			return
		}

		if !entry.limiter.Allow() {
			rl.totalRejected.Add(1)
			blockedUntil := now.Add(time.Duration(rl.config.BlockDurationSecs) * time.Second)
			entry.blockedUntilUnixNano.Store(blockedUntil.UnixNano())
			logger.Warn("🚨 [RPC_RATE_LIMIT] IP %s spammed WebSocket handshake. Banned for %d seconds.", ip, rl.config.BlockDurationSecs)
			writeIPBlockedResponse(w, blockedUntil)
			return
		}

		rl.totalAllowed.Add(1)

		wrapped := &wsHijackResponseWriter{
			ResponseWriter: w,
			ip:             ip,
			rl:             rl,
		}
		next.ServeHTTP(wrapped, r)
	})
}

// GetStats returns rate limiter statistics for the metrics endpoint.
func (rl *RPCRateLimiter) GetStats() map[string]interface{} {
	var trackedIPs int
	rl.perIP.Range(func(_, _ any) bool {
		trackedIPs++
		return true
	})
	return map[string]interface{}{
		"total_allowed":  rl.totalAllowed.Load(),
		"total_rejected": rl.totalRejected.Load(),
		"tracked_ips":    trackedIPs,
		"global_rate":    rl.config.GlobalRate,
		"per_ip_rate":    rl.config.PerIpRate,
	}
}

func writeRateLimitResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusTooManyRequests)
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"error": map[string]interface{}{
			"code":    -32005,
			"message": "Global rate limit exceeded. Please retry after 1 second.",
		},
		"id": nil,
	}
	json.NewEncoder(w).Encode(resp)
}

func writeIPBlockedResponse(w http.ResponseWriter, blockedUntil time.Time) {
	w.Header().Set("Content-Type", "application/json")
	retryAfter := int(time.Until(blockedUntil).Seconds())
	if retryAfter < 1 {
		retryAfter = 1
	}

	w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
	w.WriteHeader(http.StatusTooManyRequests)
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"error": map[string]interface{}{
			"code":    -32005,
			"message": fmt.Sprintf("Rate limit exceeded. IP Blocked. Please retry after %d seconds.", retryAfter),
		},
		"id": nil,
	}
	json.NewEncoder(w).Encode(resp)
}
