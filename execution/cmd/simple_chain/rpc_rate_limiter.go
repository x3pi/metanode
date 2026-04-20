package main

// rpc_rate_limiter.go — HTTP-level rate limiting for the Master's RPC server.
// Uses golang.org/x/time/rate (already a project dependency via routes.go).

import (
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	"golang.org/x/time/rate"
)

const (
	// Global RPC rate limit
	rpcGlobalRate  = 300000 // requests per second
	rpcGlobalBurst = 50000  // burst size

	// Per-IP rate limit (must be < global to provide meaningful protection)
	rpcPerIPRate  = 100000 // requests per second per IP
	rpcPerIPBurst = 20000  // burst per IP

	// Per-IP limiter cache
	maxIPEntries   = 10000
	ipCleanupEvery = 1 * time.Minute
)

// ipEntry holds a rate limiter and the last time it was used.
type ipEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RPCRateLimiter provides global and per-IP rate limiting for the RPC server.
type RPCRateLimiter struct {
	global *rate.Limiter

	// PERFORMANCE: use sync.Map for lock-free reads on the hot path
	// Only cleanup needs a full scan via Range()
	perIP   sync.Map // map[string]*ipEntry
	closeCh chan struct{}

	// PERFORMANCE: atomic counters — zero mutex contention
	totalAllowed  atomic.Int64
	totalRejected atomic.Int64
}

// NewRPCRateLimiter creates a new rate limiter with default settings.
func NewRPCRateLimiter() *RPCRateLimiter {
	rl := &RPCRateLimiter{
		global:  rate.NewLimiter(rate.Limit(rpcGlobalRate), rpcGlobalBurst),
		closeCh: make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

// cleanupLoop periodically removes stale per-IP entries.
func (rl *RPCRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(ipCleanupEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().Add(-ipCleanupEvery)
			rl.perIP.Range(func(key, value any) bool {
				entry := value.(*ipEntry)
				if entry.lastSeen.Before(cutoff) {
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

// getIPLimiter returns the per-IP limiter, creating one if needed.
// PERFORMANCE: Uses sync.Map for lock-free reads (hot path).
func (rl *RPCRateLimiter) getIPLimiter(ip string) *rate.Limiter {
	if val, ok := rl.perIP.Load(ip); ok {
		entry := val.(*ipEntry)
		entry.lastSeen = time.Now()
		return entry.limiter
	}

	// Create new limiter — Store-or-Load pattern avoids duplicates
	newEntry := &ipEntry{
		limiter:  rate.NewLimiter(rate.Limit(rpcPerIPRate), rpcPerIPBurst),
		lastSeen: time.Now(),
	}
	actual, _ := rl.perIP.LoadOrStore(ip, newEntry)
	return actual.(*ipEntry).limiter
}

// extractIP returns just the IP portion of a remote address.
func extractIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// Middleware wraps an http.Handler with rate limiting checks.
func (rl *RPCRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip rate limiting for metrics/health endpoints
		if r.URL.Path == "/metrics" || r.URL.Path == "/metrics/json" || r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		// Global rate check
		if !rl.global.Allow() {
			rl.totalRejected.Add(1)
			writeRateLimitResponse(w)
			return
		}

		// Per-IP rate check
		ip := extractIP(r.RemoteAddr)
		if !rl.getIPLimiter(ip).Allow() {
			rl.totalRejected.Add(1)
			logger.Warn("[RPC_RATE_LIMIT] Per-IP limit exceeded for %s", ip)
			writeRateLimitResponse(w)
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
		"global_rate":    rpcGlobalRate,
		"per_ip_rate":    rpcPerIPRate,
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
			"message": "Rate limit exceeded. Please retry after 1 second.",
		},
		"id": nil,
	}
	json.NewEncoder(w).Encode(resp)
}
