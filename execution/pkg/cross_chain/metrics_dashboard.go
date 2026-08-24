package cross_chain

import (
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// ══════════════════════════════════════════════════════════════════════════════
// P7 — CROSS-CHAIN MONITORING DASHBOARD, METRICS & REAL-TIME ALERTS (P7 DoD)
// ══════════════════════════════════════════════════════════════════════════════

// SecurityAlertSeverity defines the critical levels of monitoring alerts.
type SecurityAlertSeverity string

const (
	SeverityInfo     SecurityAlertSeverity = "INFO"
	SeverityWarning  SecurityAlertSeverity = "WARNING"
	SeverityCritical SecurityAlertSeverity = "CRITICAL"
)

// SecurityAlert represents an instant alert triggered by security invariants or anomalies.
type SecurityAlert struct {
	AlertID     string                `json:"alert_id"`
	Kind        string                `json:"kind"`
	Severity    SecurityAlertSeverity `json:"severity"`
	SourceChain uint64                `json:"source_chain,omitempty"`
	DestChain   uint64                `json:"dest_chain,omitempty"`
	MessageID   common.Hash           `json:"message_id,omitempty"`
	Details     string                `json:"details"`
	Timestamp   uint64                `json:"timestamp"`
}

// LatencyStats contains calculated statistical latency percentiles (P7.1).
type LatencyStats struct {
	Count uint64  `json:"count"`
	Min   float64 `json:"min_seconds"`
	Max   float64 `json:"max_seconds"`
	Avg   float64 `json:"avg_seconds"`
	P50   float64 `json:"p50_seconds"`
	P90   float64 `json:"p90_seconds"`
	P99   float64 `json:"p99_seconds"`
}

// PendingMessageInspection contains details about a message nearing pruning limits (P7.3).
type PendingMessageInspection struct {
	MessageID          common.Hash `json:"message_id"`
	SourceChainID      uint64      `json:"source_chain_id"`
	DestChainID        uint64      `json:"dest_chain_id"`
	CreatedAt          uint64      `json:"created_at"`
	AgeSeconds         uint64      `json:"age_seconds"`
	PruningGracePeriod uint64      `json:"pruning_grace_period"`
	RemainingSeconds   uint64      `json:"remaining_seconds"`
	IsNearDeadline     bool        `json:"is_near_deadline"`
}

// DashboardMetricsSummary aggregates all runtime health and metric data.
type DashboardMetricsSummary struct {
	LatencyStats            LatencyStats               `json:"latency_stats"`
	TotalAllocationRejects  uint64                     `json:"total_allocation_rejects"`
	ActiveSupplyDrift       *big.Int                   `json:"active_supply_drift"`
	PendingMessagesCount    int                        `json:"pending_messages_count"`
	PendingNearPruneCount   int                        `json:"pending_near_prune_count"`
	RecentAlerts            []SecurityAlert            `json:"recent_alerts"`
	PendingInspectedDetails []PendingMessageInspection `json:"pending_inspected_details"`
}

// CrossChainDashboardEngine manages metrics collection, latency histograms, DA window warnings, and alert broadcasting.
type CrossChainDashboardEngine struct {
	mu sync.RWMutex

	// P7.1 Latency metric observations (seconds)
	latencies []float64

	// P7.2 Real-time Security Alerts
	alertsChannel chan SecurityAlert
	alertHistory  []SecurityAlert
	maxHistory    int

	// P7.3 Data-Availability / Pruning config
	pruningGracePeriodSeconds uint64 // e.g. 86400s (24h)
	warningThresholdPercent   uint64 // e.g. 75% -> alert at 18h

	// In-flight message tracking: messageId -> createdAt timestamp
	inFlightTimestamps map[common.Hash]uint64
	inFlightMetadata   map[common.Hash]CrossChainMessage

	// References to underlying state engines
	supplyLedger *GlobalSupplyLedger
	gateway      *GatewayEngine
	assetReg     *AssetRegistryEngine
}

// NewCrossChainDashboardEngine creates an instance of the monitoring dashboard.
func NewCrossChainDashboardEngine(
	pruningGracePeriodSeconds uint64,
	warningThresholdPercent uint64,
	supplyLedger *GlobalSupplyLedger,
	gateway *GatewayEngine,
	assetReg *AssetRegistryEngine,
) *CrossChainDashboardEngine {
	if pruningGracePeriodSeconds == 0 {
		pruningGracePeriodSeconds = 86400 // Default 24h
	}
	if warningThresholdPercent == 0 {
		warningThresholdPercent = 75 // Default 75% of grace period
	}

	dashboard := &CrossChainDashboardEngine{
		latencies:                 make([]float64, 0, 1024),
		alertsChannel:             make(chan SecurityAlert, 256),
		alertHistory:              make([]SecurityAlert, 0, 500),
		maxHistory:                500,
		pruningGracePeriodSeconds: pruningGracePeriodSeconds,
		warningThresholdPercent:   warningThresholdPercent,
		inFlightTimestamps:        make(map[common.Hash]uint64),
		inFlightMetadata:          make(map[common.Hash]CrossChainMessage),
		supplyLedger:              supplyLedger,
		gateway:                   gateway,
		assetReg:                  assetReg,
	}

	// Wire AllocationRejected hook if gateway is provided
	if gateway != nil {
		gateway.SetAllocationRejectedListener(func(chainID uint64, requested, available *big.Int) {
			dashboard.RecordAllocationRejected(chainID, requested, available)
		})
	}

	return dashboard
}

// ──────────────────────────────────────────────────────────────────────────────
// P7.1 — METRIC: cross_chain_relay_latency_seconds
// ──────────────────────────────────────────────────────────────────────────────

// RecordOutboundMessage registers the creation time of an in-flight cross-chain message.
func (d *CrossChainDashboardEngine) RecordOutboundMessage(msg CrossChainMessage, timestamp uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.inFlightTimestamps[msg.MessageID] = timestamp
	d.inFlightMetadata[msg.MessageID] = msg
}

// RecordSettlementMessage records the completed relay latency (P7.1).
func (d *CrossChainDashboardEngine) RecordSettlementMessage(messageID common.Hash, completedTimestamp uint64) float64 {
	d.mu.Lock()
	defer d.mu.Unlock()

	startTs, exists := d.inFlightTimestamps[messageID]
	if !exists {
		return 0
	}

	var latencySec float64
	if completedTimestamp >= startTs {
		latencySec = float64(completedTimestamp - startTs)
	}
	d.latencies = append(d.latencies, latencySec)

	// Remove from in-flight tracking
	delete(d.inFlightTimestamps, messageID)
	delete(d.inFlightMetadata, messageID)

	return latencySec
}

// RecordDirectLatency records a directly observed latency sample in seconds.
func (d *CrossChainDashboardEngine) RecordDirectLatency(latencySeconds float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.latencies = append(d.latencies, latencySeconds)
}

// GetLatencyStats calculates statistical percentiles (Min, Max, Avg, P50, P90, P99).
func (d *CrossChainDashboardEngine) GetLatencyStats() LatencyStats {
	d.mu.RLock()
	defer d.mu.RUnlock()

	n := len(d.latencies)
	if n == 0 {
		return LatencyStats{}
	}

	sorted := make([]float64, n)
	copy(sorted, d.latencies)
	sort.Float64s(sorted)

	var sum float64
	for _, v := range sorted {
		sum += v
	}

	p50Idx := int(float64(n-1) * 0.50)
	p90Idx := int(float64(n-1) * 0.90)
	p99Idx := int(float64(n-1) * 0.99)

	return LatencyStats{
		Count: uint64(n),
		Min:   sorted[0],
		Max:   sorted[n-1],
		Avg:   sum / float64(n),
		P50:   sorted[p50Idx],
		P90:   sorted[p90Idx],
		P99:   sorted[p99Idx],
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// P7.2 — INSTANT SECURITY ALERT ON AllocationRejected (Scenario 10.7)
// ──────────────────────────────────────────────────────────────────────────────

// RecordAllocationRejected emits an instant CRITICAL security alert when ceiling overdraw occurs.
func (d *CrossChainDashboardEngine) RecordAllocationRejected(chainID uint64, requested, available *big.Int) SecurityAlert {
	alert := SecurityAlert{
		AlertID:     fmt.Sprintf("ALERT_ALLOC_REJECT_%d_%d", chainID, time.Now().UnixNano()),
		Kind:        "AllocationRejected",
		Severity:    SeverityCritical,
		SourceChain: chainID,
		Details: fmt.Sprintf(
			"SECURITY OVERDRAW BLOCKED: Chain %d requested %s MTN exceeding ceiling %s MTN",
			chainID, requested.String(), available.String(),
		),
		Timestamp: uint64(time.Now().Unix()),
	}

	d.emitAlert(alert)
	return alert
}

// ──────────────────────────────────────────────────────────────────────────────
// P7.3 — SUPPLY DRIFT & PENDING DATA-AVAILABILITY (DA) WINDOW MONITORING
// ──────────────────────────────────────────────────────────────────────────────

// CheckSupplyDrift verifies the global supply conservation invariant and alerts if drift != 0.
func (d *CrossChainDashboardEngine) CheckSupplyDrift(expectedTotalSupply *big.Int) (*big.Int, *SecurityAlert) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.supplyLedger == nil {
		return big.NewInt(0), nil
	}

	currentTotal := d.supplyLedger.SumAllocations()
	drift := new(big.Int).Sub(currentTotal, expectedTotalSupply)

	if drift.Sign() != 0 {
		alert := SecurityAlert{
			AlertID:   fmt.Sprintf("ALERT_SUPPLY_DRIFT_%d", time.Now().UnixNano()),
			Kind:      "GlobalSupplyDriftDetected",
			Severity:  SeverityCritical,
			Details:   fmt.Sprintf("CRITICAL INVARIANT VIOLATION: Current supply %s deviates from expected %s (Drift: %s)", currentTotal.String(), expectedTotalSupply.String(), drift.String()),
			Timestamp: uint64(time.Now().Unix()),
		}
		d.emitAlertLocked(alert)
		return drift, &alert
	}

	return big.NewInt(0), nil
}

// InspectPendingMessages scans in-flight pending messages and detects DA window exhaustion risk.
func (d *CrossChainDashboardEngine) InspectPendingMessages(currentTimestamp uint64) []PendingMessageInspection {
	d.mu.Lock()
	defer d.mu.Unlock()

	inspections := make([]PendingMessageInspection, 0, len(d.inFlightTimestamps))
	warnThresholdSeconds := (d.pruningGracePeriodSeconds * d.warningThresholdPercent) / 100

	for msgID, createdAt := range d.inFlightTimestamps {
		var age uint64
		if currentTimestamp >= createdAt {
			age = currentTimestamp - createdAt
		}

		var remaining uint64
		if d.pruningGracePeriodSeconds >= age {
			remaining = d.pruningGracePeriodSeconds - age
		} else {
			remaining = 0
		}

		isNear := age >= warnThresholdSeconds
		meta := d.inFlightMetadata[msgID]

		inspection := PendingMessageInspection{
			MessageID:          msgID,
			SourceChainID:      meta.SourceChainID,
			DestChainID:        meta.DestChainID,
			CreatedAt:          createdAt,
			AgeSeconds:         age,
			PruningGracePeriod: d.pruningGracePeriodSeconds,
			RemainingSeconds:   remaining,
			IsNearDeadline:     isNear,
		}
		inspections = append(inspections, inspection)

		// If near pruning deadline, emit warning alert
		if isNear {
			alert := SecurityAlert{
				AlertID:     fmt.Sprintf("ALERT_PENDING_PRUNE_%s", msgID.Hex()[:10]),
				Kind:        "PendingMessageNearPruneDeadline",
				Severity:    SeverityWarning,
				SourceChain: meta.SourceChainID,
				DestChain:   meta.DestChainID,
				MessageID:   msgID,
				Details: fmt.Sprintf(
					"Message %s pending for %ds (Threshold: %ds, Grace: %ds, Remaining: %ds). Needs immediate relayer retry!",
					msgID.Hex(), age, warnThresholdSeconds, d.pruningGracePeriodSeconds, remaining,
				),
				Timestamp: currentTimestamp,
			}
			d.emitAlertLocked(alert)
		}
	}

	return inspections
}

// GetSummary produces a consolidated monitoring snapshot.
func (d *CrossChainDashboardEngine) GetSummary(currentTimestamp uint64, expectedSupply *big.Int) DashboardMetricsSummary {
	latencyStats := d.GetLatencyStats()
	inspections := d.InspectPendingMessages(currentTimestamp)

	d.mu.RLock()
	defer d.mu.RUnlock()

	nearCount := 0
	for _, insp := range inspections {
		if insp.IsNearDeadline {
			nearCount++
		}
	}

	var drift *big.Int = big.NewInt(0)
	if d.supplyLedger != nil && expectedSupply != nil {
		currentTotal := d.supplyLedger.SumAllocations()
		drift = new(big.Int).Sub(currentTotal, expectedSupply)
	}

	// Count allocation rejected alerts
	var allocRejects uint64 = 0
	for _, a := range d.alertHistory {
		if a.Kind == "AllocationRejected" {
			allocRejects++
		}
	}

	recentAlerts := make([]SecurityAlert, len(d.alertHistory))
	copy(recentAlerts, d.alertHistory)

	return DashboardMetricsSummary{
		LatencyStats:            latencyStats,
		TotalAllocationRejects:  allocRejects,
		ActiveSupplyDrift:       drift,
		PendingMessagesCount:    len(d.inFlightTimestamps),
		PendingNearPruneCount:   nearCount,
		RecentAlerts:            recentAlerts,
		PendingInspectedDetails: inspections,
	}
}

// ExportPrometheusMetrics renders Prometheus-compliant text format metrics.
func (d *CrossChainDashboardEngine) ExportPrometheusMetrics() string {
	stats := d.GetLatencyStats()

	d.mu.RLock()
	defer d.mu.RUnlock()

	var allocRejects uint64 = 0
	for _, a := range d.alertHistory {
		if a.Kind == "AllocationRejected" {
			allocRejects++
		}
	}

	return fmt.Sprintf(
		"# HELP cross_chain_relay_latency_seconds Cross-chain message relay latency in seconds\n"+
			"# TYPE cross_chain_relay_latency_seconds summary\n"+
			"cross_chain_relay_latency_seconds{quantile=\"0.5\"} %.4f\n"+
			"cross_chain_relay_latency_seconds{quantile=\"0.9\"} %.4f\n"+
			"cross_chain_relay_latency_seconds{quantile=\"0.99\"} %.4f\n"+
			"cross_chain_relay_latency_seconds_count %d\n"+
			"cross_chain_relay_latency_seconds_sum %.4f\n"+
			"\n"+
			"# HELP cross_chain_allocation_rejected_total Total count of blocked overdraw attacks\n"+
			"# TYPE cross_chain_allocation_rejected_total counter\n"+
			"cross_chain_allocation_rejected_total %d\n"+
			"\n"+
			"# HELP cross_chain_in_flight_pending_messages Total active pending messages\n"+
			"# TYPE cross_chain_in_flight_pending_messages gauge\n"+
			"cross_chain_in_flight_pending_messages %d\n",
		stats.P50, stats.P90, stats.P99, stats.Count, stats.Avg*float64(stats.Count),
		allocRejects,
		len(d.inFlightTimestamps),
	)
}

// AlertsChannel returns the stream of real-time security alerts.
func (d *CrossChainDashboardEngine) AlertsChannel() <-chan SecurityAlert {
	return d.alertsChannel
}

// Internal alert dispatchers
func (d *CrossChainDashboardEngine) emitAlert(alert SecurityAlert) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.emitAlertLocked(alert)
}

func (d *CrossChainDashboardEngine) emitAlertLocked(alert SecurityAlert) {
	d.alertHistory = append(d.alertHistory, alert)
	if len(d.alertHistory) > d.maxHistory {
		d.alertHistory = d.alertHistory[1:]
	}

	// Non-blocking broadcast to subscriber channel
	select {
	case d.alertsChannel <- alert:
	default:
		// Queue full, drop from live channel but preserved in history
	}
}
