package main

// metrics.go — Prometheus + backward-compatible JSON metrics for the Master node.
// Prometheus endpoint: /metrics  (scraped by Prometheus/Grafana)
// JSON endpoint:       /metrics/json (backward compat for existing tools)

import (
	"encoding/json"
	"net/http"
	"runtime"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
)

// MetricsCollector gathers and exposes metrics for the Master node.
// Prometheus metrics are handled by pkg/metrics; this struct provides
// the backward-compatible JSON snapshot and wires external stat providers.
type MetricsCollector struct {
	startTime time.Time

	// External stat providers (set externally)
	getCircuitBreakerStats func() map[string]interface{}
	getRateLimiterStats    func() map[string]interface{}

	// App reference for blockchain info
	app *App
}

// NewMetricsCollector creates a new collector and starts the system metrics goroutine.
func NewMetricsCollector(app *App) *MetricsCollector {
	// Start goroutine that periodically updates runtime gauges
	metrics.StartSystemMetricsCollector(5 * time.Second)

	return &MetricsCollector{
		startTime: time.Now(),
		app:       app,
	}
}

// RecordRPCRequest increments the Prometheus RPC request counter.
func (mc *MetricsCollector) RecordRPCRequest(method string) {
	metrics.RPCRequestsTotal.WithLabelValues(method).Inc()
}

// RecordRPCError increments the Prometheus RPC error counter.
func (mc *MetricsCollector) RecordRPCError(method string) {
	metrics.RPCErrorsTotal.WithLabelValues(method).Inc()
}

// SetCircuitBreakerStatsFunc sets the function to retrieve circuit breaker stats.
func (mc *MetricsCollector) SetCircuitBreakerStatsFunc(fn func() map[string]interface{}) {
	mc.getCircuitBreakerStats = fn
}

// SetRateLimiterStatsFunc sets the function to retrieve rate limiter stats.
func (mc *MetricsCollector) SetRateLimiterStatsFunc(fn func() map[string]interface{}) {
	mc.getRateLimiterStats = fn
}

// Snapshot returns all metrics as a structured map (backward compat JSON).
func (mc *MetricsCollector) Snapshot() map[string]interface{} {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	snapshot := map[string]interface{}{
		"system": map[string]interface{}{
			"uptime_seconds":    int64(time.Since(mc.startTime).Seconds()),
			"goroutines":        runtime.NumGoroutine(),
			"go_version":        runtime.Version(),
			"num_cpu":           runtime.NumCPU(),
			"heap_alloc_mb":     float64(memStats.HeapAlloc) / (1024 * 1024),
			"heap_sys_mb":       float64(memStats.HeapSys) / (1024 * 1024),
			"heap_inuse_mb":     float64(memStats.HeapInuse) / (1024 * 1024),
			"heap_idle_mb":      float64(memStats.HeapIdle) / (1024 * 1024),
			"stack_inuse_mb":    float64(memStats.StackInuse) / (1024 * 1024),
			"gc_cycles":         memStats.NumGC,
			"gc_pause_total_ms": float64(memStats.PauseTotalNs) / 1e6,
		},
	}

	// Blockchain info
	if mc.app != nil && mc.app.blockProcessor != nil {
		lastBlock := mc.app.blockProcessor.GetLastBlock()
		if lastBlock != nil && lastBlock.Header() != nil {
			snapshot["blockchain"] = map[string]interface{}{
				"block_number": lastBlock.Header().BlockNumber(),
				"block_hash":   lastBlock.Header().Hash().Hex(),
			}
		}
	}

	// Circuit breaker stats
	if mc.getCircuitBreakerStats != nil {
		snapshot["circuit_breaker"] = mc.getCircuitBreakerStats()
	}

	// Rate limiter stats
	if mc.getRateLimiterStats != nil {
		snapshot["rate_limiter"] = mc.getRateLimiterStats()
	}

	return snapshot
}

// ServeHTTP implements http.Handler — serves JSON metrics at /metrics/json.
func (mc *MetricsCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	snapshot := mc.Snapshot()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		logger.Error("[METRICS] Failed to marshal metrics: %v", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	w.Write(data)
}
