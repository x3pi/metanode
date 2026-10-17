package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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
