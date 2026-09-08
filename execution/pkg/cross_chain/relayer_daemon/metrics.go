package relayer_daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// PRODUCTION-READINESS FIX (2026-09-05): before this file existed, daemon.go and
// cross_chain_relayer/main.go had ZERO metrics/health/HTTP surface (confirmed by grep for
// prometheus/http.ListenAndServe/health/metrics -- no hits) -- a hung or repeatedly-erroring
// relayer process looked externally identical to a healthy one (a live tmux/systemd process),
// found during the Relayer architecture production-readiness review. This mirrors the exact
// pattern pkg/metrics + cmd/simple_chain/backend.go already use for the main node (promauto
// global vars + a "/metrics" promhttp.Handler() + a hand-rolled JSON "/health"), with a
// "relayer_" metric name prefix instead of "master_" so a shared Grafana/Prometheus setup
// scraping both the node and this daemon never collides on names.

var (
	relayMessagesRelayedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "relayer_messages_relayed_total",
		Help: "Total cross-chain messages successfully relayed, labeled by source/destination chain",
	}, []string{"source_chain", "dest_chain"})

	relayWatchErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "relayer_watch_errors_total",
		Help: "Total errors encountered by WatchChainPair's batch/relay loop, labeled by source/destination chain",
	}, []string{"source_chain", "dest_chain"})

	relayLastSuccessTimestamp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "relayer_last_successful_poll_timestamp_seconds",
		Help: "Unix timestamp of the last error-free BatchAndRelay tick for a watched chain pair",
	}, []string{"source_chain", "dest_chain"})

	relayConsecutiveErrors = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "relayer_consecutive_errors",
		Help: "Current consecutive error count for a watched chain pair's poll loop (drives backoff; 0 means healthy)",
	}, []string{"source_chain", "dest_chain"})

	relayGasPriceWei = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "relayer_gas_price_wei",
		Help: "Last-resolved gas price (wei) used for transactions on a given chain",
	}, []string{"chain_id"})

	relayerBalanceWei = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "relayer_wallet_balance_wei",
		Help: "Relayer wallet's native balance on a given chain, refreshed periodically -- alert on this for gas-exhaustion",
	}, []string{"chain_id"})
)

func chainIDLabel(chainID uint64) string {
	return fmt.Sprintf("%d", chainID)
}

// pairHealth tracks one watched (sourceChainID, destChainID) pair's recent poll history, read by
// both the Prometheus gauges above and the /health JSON endpoint.
type pairHealth struct {
	lastSuccessAt     time.Time
	lastErrorAt       time.Time
	lastError         string
	consecutiveErrors int
}

func (d *RelayerDaemon) getOrCreatePairHealthLocked(pairKey string) *pairHealth {
	if d.pairHealth == nil {
		d.pairHealth = make(map[string]*pairHealth)
	}
	ph, ok := d.pairHealth[pairKey]
	if !ok {
		ph = &pairHealth{}
		d.pairHealth[pairKey] = ph
	}
	return ph
}

// recordPairFailure records a failed poll tick for pairKey and returns the new consecutive-error
// count (used by WatchChainPair to drive backoffDuration).
func (d *RelayerDaemon) recordPairFailure(pairKey string, err error) int {
	d.healthMu.Lock()
	defer d.healthMu.Unlock()
	ph := d.getOrCreatePairHealthLocked(pairKey)
	ph.consecutiveErrors++
	ph.lastErrorAt = time.Now()
	ph.lastError = err.Error()
	return ph.consecutiveErrors
}

// recordPairSuccess records an error-free poll tick for pairKey, resetting its error streak.
func (d *RelayerDaemon) recordPairSuccess(pairKey string) {
	d.healthMu.Lock()
	defer d.healthMu.Unlock()
	ph := d.getOrCreatePairHealthLocked(pairKey)
	ph.consecutiveErrors = 0
	ph.lastSuccessAt = time.Now()
}

// backoffDuration computes how long WatchChainPair should sleep after consecutiveErrors in a row,
// using exponential backoff (base * 2^(n-1)) capped at max.
//
// PRODUCTION-READINESS FIX (2026-09-05): WatchChainPair previously always slept exactly
// config.PollInterval regardless of an unbroken error streak -- a persistently-failing RPC
// endpoint (e.g. a chain node that is down) got hammered at full frequency forever. errors here
// are still logged via logger.Warn, never fatal -- a transient hiccup must never kill the loop --
// but a sustained failure now backs off instead of spinning.
func backoffDuration(base, max time.Duration, consecutiveErrors int) time.Duration {
	if consecutiveErrors <= 1 {
		return base
	}
	shift := consecutiveErrors - 1
	if shift > 20 { // guard against a 1<<shift overflow/wraparound after a very long error streak
		shift = 20
	}
	backoff := base * time.Duration(int64(1)<<uint(shift))
	if backoff <= 0 || backoff > max { // backoff<=0 also catches the overflow-wraparound case
		return max
	}
	return backoff
}

// StartMetricsServer starts a minimal HTTP server exposing Prometheus metrics ("/metrics",
// standard text format) and a liveness/readiness JSON endpoint ("/health") for this daemon.
// Returns the *http.Server so callers can Shutdown() it on their own exit path. Logs (never
// panics) if the listener fails to bind -- metrics/health being unavailable must never take down
// message relaying itself, which runs entirely independently of this server.
func (d *RelayerDaemon) StartMetricsServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		d.writeHealth(w)
	})
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		logger.Info("📈 [RELAYER DAEMON] metrics/health server listening on %s (/metrics, /health)", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Warn("⚠️ [RELAYER DAEMON] metrics server stopped: %v", err)
		}
	}()
	return srv
}

// writeHealth renders this daemon's current liveness/readiness state as JSON. Reports HTTP 503
// (status "degraded") when any watched pair has 5+ consecutive errors -- a threshold chosen so a
// single transient blip never flips health, but a genuinely stuck/erroring pair does.
func (d *RelayerDaemon) writeHealth(w http.ResponseWriter) {
	const degradedThreshold = 5

	d.healthMu.Lock()
	pairs := make(map[string]interface{}, len(d.pairHealth))
	healthy := true
	for key, ph := range d.pairHealth {
		if ph.consecutiveErrors >= degradedThreshold {
			healthy = false
		}
		entry := map[string]interface{}{
			"consecutive_errors": ph.consecutiveErrors,
		}
		if !ph.lastSuccessAt.IsZero() {
			entry["last_success_at"] = ph.lastSuccessAt.UTC().Format(time.RFC3339)
		}
		if !ph.lastErrorAt.IsZero() {
			entry["last_error_at"] = ph.lastErrorAt.UTC().Format(time.RFC3339)
			entry["last_error"] = ph.lastError
		}
		pairs[key] = entry
	}
	d.healthMu.Unlock()

	d.balancesMu.RLock()
	balances := make(map[string]string, len(d.balances))
	for chainID, bal := range d.balances {
		balances[chainIDLabel(chainID)] = bal.String()
	}
	d.balancesMu.RUnlock()

	status := "ok"
	if !healthy {
		status = "degraded"
	}

	resp := map[string]interface{}{
		"status":            status,
		"relayer_address":   d.relayerAddr.Hex(),
		"started_at":        d.startedAt.UTC().Format(time.RFC3339),
		"uptime_seconds":    time.Since(d.startedAt).Seconds(),
		"configured_chains": d.ConfiguredChains(),
		"watched_pairs":     pairs,
		"balances_wei":      balances,
	}

	w.Header().Set("Content-Type", "application/json")
	if !healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// StartBalanceMonitor periodically refreshes the relayer's own native-coin balance on every
// currently-configured chain, updating the relayer_wallet_balance_wei gauge and /health's
// balances_wei -- the metric a production operator should alert on for gas exhaustion, since a
// relayer whose balance can no longer cover gas fails silently (its sends start erroring exactly
// like any other transient RPC hiccup). interval<=0 defaults to 30s. Runs until ctx is cancelled
// or Stop() is called, tracked by the same WaitGroup Stop() already waits on, so callers don't
// need a special-case shutdown path for it.
func (d *RelayerDaemon) StartBalanceMonitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		refresh := func() {
			for _, chainID := range d.ConfiguredChains() {
				client, exists := d.GetChainClient(chainID)
				if !exists {
					continue
				}
				bal, err := client.GetBalance(ctx, d.relayerAddr)
				if err != nil {
					logger.Warn("⚠️ [RELAYER DAEMON] balance query failed for chain %d: %v", chainID, err)
					continue
				}
				d.balancesMu.Lock()
				if d.balances == nil {
					d.balances = make(map[uint64]*big.Int)
				}
				d.balances[chainID] = bal
				d.balancesMu.Unlock()
				balF, _ := new(big.Float).SetInt(bal).Float64()
				relayerBalanceWei.WithLabelValues(chainIDLabel(chainID)).Set(balF)
			}
		}
		refresh()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-d.stopCh:
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
}
