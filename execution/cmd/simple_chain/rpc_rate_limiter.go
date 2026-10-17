package main

// rpc_rate_limiter.go — HTTP-level rate limiting for the Master's RPC server.
// Uses golang.org/x/time/rate (already a project dependency via routes.go).

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
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
	perIP   sync.Map // map[string]*ipEntry
	closeCh chan struct{}

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
					rl.perIP.Delete(key)
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

// getIPEntry returns the per-IP entry, creating one if needed.
// PERFORMANCE: Uses sync.Map for lock-free reads (hot path).
func (rl *RPCRateLimiter) getIPEntry(ip string) *ipEntry {
	nowNano := time.Now().UnixNano()
	if val, ok := rl.perIP.Load(ip); ok {
		entry := val.(*ipEntry)
		entry.lastSeenUnixNano.Store(nowNano)
		return entry
	}

	burst := rl.config.PerIpRate * 2
	if burst < 20 {
		burst = 20
	}

	// Create new limiter — Store-or-Load pattern avoids duplicates
	newEntry := &ipEntry{
		limiter: rate.NewLimiter(rate.Limit(rl.config.PerIpRate), burst),
	}
	newEntry.lastSeenUnixNano.Store(nowNano)
	actual, _ := rl.perIP.LoadOrStore(ip, newEntry)
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
	if rl == nil || !rl.config.Enabled {
		return nil
	}

	entry := rl.getIPEntry(ip)
	now := time.Now()
	nowNano := now.UnixNano()
	blockedUntilNano := entry.blockedUntilUnixNano.Load()

	// Check if IP is currently blocked
	if nowNano < blockedUntilNano {
		rl.totalRejected.Add(1)
		return fmt.Errorf("ip_blocked")
	}

	// Global rate check
	if !rl.global.Allow() {
		rl.totalRejected.Add(1)
		return fmt.Errorf("global_rate_exceeded")
	}

	// Check per-IP limiter
	if !entry.limiter.Allow() {
		rl.totalRejected.Add(1)

		// IP exceeds rate, BAN it
		blockedUntil := now.Add(time.Duration(rl.config.BlockDurationSecs) * time.Second)
		entry.blockedUntilUnixNano.Store(blockedUntil.UnixNano())
		logger.Warn("🚨 [RPC_RATE_LIMIT] IP %s spammed RPC. Banned for %d seconds.", ip, rl.config.BlockDurationSecs)
		return fmt.Errorf("ip_banned")
	}

	rl.totalAllowed.Add(1)
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

		// Per-IP rate check
		ip := extractIP(r.RemoteAddr)
		entry := rl.getIPEntry(ip)

		now := time.Now()
		nowNano := now.UnixNano()
		blockedUntilNano := entry.blockedUntilUnixNano.Load()

		// Check if IP is currently blocked
		if nowNano < blockedUntilNano {
			rl.totalRejected.Add(1)
			writeIPBlockedResponse(w, time.Unix(0, blockedUntilNano))
			return
		}

		// Global rate check
		if !rl.global.Allow() {
			rl.totalRejected.Add(1)
			writeRateLimitResponse(w)
			return
		}

		// Check per-IP limiter
		if !entry.limiter.Allow() {
			rl.totalRejected.Add(1)

			// IP exceeds rate, BAN it
			blockedUntil := now.Add(time.Duration(rl.config.BlockDurationSecs) * time.Second)
			entry.blockedUntilUnixNano.Store(blockedUntil.UnixNano())
			logger.Warn("🚨 [RPC_RATE_LIMIT] IP %s spammed RPC. Banned for %d seconds.", ip, rl.config.BlockDurationSecs)

			writeIPBlockedResponse(w, blockedUntil)
			return
		}

		rl.totalAllowed.Add(1)

		// ── Prometheus: track RPC request and duration ─────────────────
		method := r.URL.Path
		metrics.RPCRequestsTotal.WithLabelValues(method).Inc()
		start := time.Now()
		next.ServeHTTP(w, r)
		metrics.RPCDuration.WithLabelValues(method).Observe(time.Since(start).Seconds())
	})
}

type wsFrameState int

const (
	wsStateHeader wsFrameState = iota
	wsStatePayload
)

// wsRateLimitConn wraps the hijacked net.Conn of a WebSocket to inspect RFC 6455
// incoming frames and enforce rate limits on every individual JSON-RPC message.
type wsRateLimitConn struct {
	net.Conn
	initialReader io.Reader
	ip            string
	rl            *RPCRateLimiter

	state            wsFrameState
	headerBuf        [14]byte
	headerLen        int
	expectedHeader   int
	payloadRemaining int64
}

func newWSRateLimitConn(conn net.Conn, initialReader io.Reader, ip string, rl *RPCRateLimiter) *wsRateLimitConn {
	return &wsRateLimitConn{
		Conn:          conn,
		initialReader: initialReader,
		ip:            ip,
		rl:            rl,
		state:         wsStateHeader,
	}
}

func (c *wsRateLimitConn) Read(p []byte) (int, error) {
	var n int
	var err error
	if c.initialReader != nil {
		n, err = c.initialReader.Read(p)
		if err == io.EOF {
			c.initialReader = nil
			if n == 0 {
				n, err = c.Conn.Read(p)
			} else {
				err = nil
			}
		}
	} else {
		n, err = c.Conn.Read(p)
	}

	if n > 0 {
		if parseErr := c.processIncomingBytes(p[:n]); parseErr != nil {
			return n, parseErr
		}
	}
	return n, err
}

func (c *wsRateLimitConn) processIncomingBytes(p []byte) error {
	n := len(p)
	for i := 0; i < n; i++ {
		b := p[i]
		if c.state == wsStateHeader {
			c.headerBuf[c.headerLen] = b
			c.headerLen++
			if c.headerLen == 2 {
				b1 := c.headerBuf[1]
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
				c.expectedHeader = 2 + extraLen + maskLen
			}
			if c.headerLen >= 2 && c.headerLen == c.expectedHeader {
				// Header complete!
				opcode := c.headerBuf[0] & 0x0f
				b1 := c.headerBuf[1]
				len7 := b1 & 0x7f
				var payloadLen int64
				if len7 <= 125 {
					payloadLen = int64(len7)
				} else if len7 == 126 {
					payloadLen = int64(binary.BigEndian.Uint16(c.headerBuf[2:4]))
				} else {
					payloadLen = int64(binary.BigEndian.Uint64(c.headerBuf[2:10]))
				}

				// RFC 6455: Opcode 0x1 (Text) and 0x2 (Binary) are incoming RPC request messages
				if opcode == 0x1 || opcode == 0x2 {
					if err := c.rl.CheckRequest(c.ip); err != nil {
						// Write close frame with code 1008 (Policy Violation) and terminate connection
						_ = c.Conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
						_, _ = c.Conn.Write([]byte{0x88, 0x02, 0x03, 0xf0})
						_ = c.Conn.Close()
						return fmt.Errorf("websocket rpc rate limit exceeded for ip %s: %w", c.ip, err)
					}
					metrics.RPCRequestsTotal.WithLabelValues("/ws").Inc()
				}

				c.headerLen = 0
				c.expectedHeader = 0
				if payloadLen > 0 {
					c.state = wsStatePayload
					c.payloadRemaining = payloadLen
				} else {
					c.state = wsStateHeader
				}
			}
		} else if c.state == wsStatePayload {
			avail := int64(n - i)
			if avail <= c.payloadRemaining {
				c.payloadRemaining -= avail
				if c.payloadRemaining == 0 {
					c.state = wsStateHeader
				}
				break
			} else {
				i += int(c.payloadRemaining) - 1
				c.payloadRemaining = 0
				c.state = wsStateHeader
				continue
			}
		}
	}
	return nil
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

// WebsocketMiddleware wraps a WebSocket http.Handler to enforce per-message rate limiting.
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
