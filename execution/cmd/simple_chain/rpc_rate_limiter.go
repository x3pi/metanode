package main

// rpc_rate_limiter.go — HTTP-level rate limiting for the Master's RPC server.
// Uses golang.org/x/time/rate (already a project dependency via routes.go).

import (
	"encoding/json"
	"fmt"
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

// ipEntry holds a rate limiter and the last time it was used.
type ipEntry struct {
	limiter      *rate.Limiter
	lastSeen     time.Time
	blockedUntil time.Time // If set > Now(), all requests are instantly rejected
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
			cutoff := time.Now().Add(-ipCleanupEvery)
			rl.perIP.Range(func(key, value any) bool {
				entry := value.(*ipEntry)
				// Clean up only if it hasn't been seen recently AND it's not currently blocked
				if entry.lastSeen.Before(cutoff) && time.Now().After(entry.blockedUntil) {
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
	if val, ok := rl.perIP.Load(ip); ok {
		entry := val.(*ipEntry)
		entry.lastSeen = time.Now()
		return entry
	}

	burst := rl.config.PerIpRate * 2
	if burst < 20 {
		burst = 20
	}

	// Create new limiter — Store-or-Load pattern avoids duplicates
	newEntry := &ipEntry{
		limiter:  rate.NewLimiter(rate.Limit(rl.config.PerIpRate), burst),
		lastSeen: time.Now(),
	}
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

		// Check if IP is currently blocked
		if time.Now().Before(entry.blockedUntil) {
			rl.totalRejected.Add(1)
			writeIPBlockedResponse(w, entry.blockedUntil)
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
			entry.blockedUntil = time.Now().Add(time.Duration(rl.config.BlockDurationSecs) * time.Second)
			logger.Warn("🚨 [RPC_RATE_LIMIT] IP %s spammed RPC. Banned for %d seconds.", ip, rl.config.BlockDurationSecs)

			writeIPBlockedResponse(w, entry.blockedUntil)
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
