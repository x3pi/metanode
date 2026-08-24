package cross_chain

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// P7 — DASHBOARD GIÁM SÁT, METRICS & CẢNH BÁO AN NINH TEST SUITE (P7 DoD)
// ══════════════════════════════════════════════════════════════════════════════

var testKP101 = bls.GenerateKeyPair()
var testKP102 = bls.GenerateKeyPair()

func setupDashboardTestEnv() (*CrossChainDashboardEngine, *GatewayEngine, *GlobalSupplyLedger) {
	genesisTotalSupply := big.NewInt(10_000_000)
	initialAllocations := map[uint64]*big.Int{
		101: big.NewInt(5_000_000),
		102: big.NewInt(5_000_000),
	}
	supplyLedger, err := NewGlobalSupplyLedger(genesisTotalSupply, initialAllocations)
	if err != nil {
		panic(err)
	}

	chainRegistry := make(map[uint64]ChainRegistry)
	chainRegistry[101] = ChainRegistry{
		ChainID:         101,
		Epoch:           1,
		QuorumThreshold: 6667,
		Committee:       []ValidatorEntry{{PubkeyBLS: testKP101.PublicKey().Bytes(), Stake: 100}},
	}
	chainRegistry[102] = ChainRegistry{
		ChainID:         102,
		Epoch:           1,
		QuorumThreshold: 6667,
		Committee:       []ValidatorEntry{{PubkeyBLS: testKP102.PublicKey().Bytes(), Stake: 100}},
	}

	gateway := NewGatewayEngine(101, chainRegistry, supplyLedger)

	dashboard := NewCrossChainDashboardEngine(
		86400, // 24h grace period
		75,    // 75% warning threshold (18h)
		supplyLedger,
		gateway,
		nil,
	)

	return dashboard, gateway, supplyLedger
}

// ──────────────────────────────────────────────────────────────────────────────
// TEST P7.1: Metric cross_chain_relay_latency_seconds & Percentiles (DoD)
// ──────────────────────────────────────────────────────────────────────────────
func TestP7_1_RelayLatencyMetricsAndPrometheusExport(t *testing.T) {
	dashboard, _, _ := setupDashboardTestEnv()

	// Simulate 6 relay deliveries with varying latencies
	latencies := []float64{2.5, 4.0, 5.5, 8.0, 10.0, 12.0}
	for _, lat := range latencies {
		dashboard.RecordDirectLatency(lat)
	}

	// 1. Verify percentiles calculations
	stats := dashboard.GetLatencyStats()
	assert.Equal(t, uint64(6), stats.Count)
	assert.InDelta(t, 7.0, stats.Avg, 0.1)
	assert.InDelta(t, 6.75, stats.P50, 1.5)
	assert.InDelta(t, 10.0, stats.P90, 2.0)

	// 2. Verify Prometheus text exporter output
	promText := dashboard.ExportPrometheusMetrics()
	assert.True(t, strings.Contains(promText, "cross_chain_relay_latency_seconds_count 6"))
	assert.True(t, strings.Contains(promText, "cross_chain_relay_latency_seconds_sum 42.0000"))
	assert.True(t, strings.Contains(promText, "cross_chain_allocation_rejected_total 0"))
}

// ──────────────────────────────────────────────────────────────────────────────
// TEST P7.2: Instant Security Alert on AllocationRejected (DoD Scenario 10.7)
// ──────────────────────────────────────────────────────────────────────────────
func TestP7_2_InstantSecurityAlertOnAllocationRejected(t *testing.T) {
	dashboard, gateway, _ := setupDashboardTestEnv()

	// Available ceiling for Chain 101 is 5,000,000 MTN
	// Attacker attempts to withdraw 15,000,000 MTN
	hackAmount := big.NewInt(15_000_000)
	commitRoot := common.HexToHash("0xDEADBEEF00000000000000000000000000000000000000000000000000000000")
	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(testKP101.PrivateKey(), commitMsg)

	cert := QuorumCert{
		Epoch:              1,
		AggregateSignature: sig.Bytes(),
		SignerBitmap:       []byte{0x01},
	}

	// Trigger overdraw attack via gateway
	_, err := gateway.AttestCommit(101, commitRoot, hackAmount, cert)
	assert.ErrorIs(t, err, ErrAllocationExceeded, "Overdraw attempt must be rejected")

	// Verify instant alert received in channel within milliseconds (< 1s)
	select {
	case alert := <-dashboard.AlertsChannel():
		assert.Equal(t, "AllocationRejected", alert.Kind)
		assert.Equal(t, SeverityCritical, alert.Severity)
		assert.Equal(t, uint64(101), alert.SourceChain)
		assert.Contains(t, alert.Details, "SECURITY OVERDRAW BLOCKED")
		assert.Contains(t, alert.Details, "15000000")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Timeout: Instant alert on AllocationRejected was not dispatched!")
	}

	// Check summary aggregates
	summary := dashboard.GetSummary(uint64(time.Now().Unix()), big.NewInt(10_000_000))
	assert.Equal(t, uint64(1), summary.TotalAllocationRejects)
	assert.Equal(t, 1, len(summary.RecentAlerts))
}

// ──────────────────────────────────────────────────────────────────────────────
// TEST P7.3: Supply Drift & Pending Data-Availability Window Monitoring (DoD)
// ──────────────────────────────────────────────────────────────────────────────
func TestP7_3_SupplyDriftAndPendingPruningDeadlineWarnings(t *testing.T) {
	dashboard, _, supplyLedger := setupDashboardTestEnv()

	now := uint64(1_700_000_000)

	// 1. Test In-Flight Pending Message Pruning Warning (DA Window)
	// Grace period = 86,400s (24h). 75% threshold = 64,800s (18h)
	freshMsg := CrossChainMessage{MessageID: common.HexToHash("0xAAAA"), SourceChainID: 101, DestChainID: 102}
	staleMsg := CrossChainMessage{MessageID: common.HexToHash("0xBBBB"), SourceChainID: 101, DestChainID: 102}

	// Fresh message: created 2h ago (7,200s) -> NO warning
	dashboard.RecordOutboundMessage(freshMsg, now-7200)

	// Stale message: created 20h ago (72,000s > 64,800s threshold) -> MUST WARN
	dashboard.RecordOutboundMessage(staleMsg, now-72000)

	inspections := dashboard.InspectPendingMessages(now)
	require.Equal(t, 2, len(inspections))

	var staleInspection *PendingMessageInspection
	for _, insp := range inspections {
		if insp.MessageID == staleMsg.MessageID {
			staleInspection = &insp
		}
	}
	require.NotNil(t, staleInspection)
	assert.True(t, staleInspection.IsNearDeadline, "Message pending 20h must be flagged as near deadline")
	assert.Equal(t, uint64(14400), staleInspection.RemainingSeconds, "Remaining time before 24h prune must be 4h (14400s)")

	// Verify alert was emitted
	select {
	case alert := <-dashboard.AlertsChannel():
		assert.Equal(t, "PendingMessageNearPruneDeadline", alert.Kind)
		assert.Equal(t, SeverityWarning, alert.Severity)
		assert.Equal(t, staleMsg.MessageID, alert.MessageID)
	default:
		t.Fatal("Expected warning alert for pending message near pruning deadline")
	}

	// 2. Test Global Supply Drift Detection
	// Expected supply is 10,000,000
	// Modify allocation artificially to 10,000,500 (Drift = +500)
	supplyLedger.PerChainAllocation[101] = big.NewInt(5_000_500)

	drift, driftAlert := dashboard.CheckSupplyDrift(big.NewInt(10_000_000))
	assert.Equal(t, big.NewInt(500), drift)
	require.NotNil(t, driftAlert)
	assert.Equal(t, "GlobalSupplyDriftDetected", driftAlert.Kind)
	assert.Equal(t, SeverityCritical, driftAlert.Severity)
	assert.Contains(t, driftAlert.Details, "CRITICAL INVARIANT VIOLATION")

	// Summary inspection
	summary := dashboard.GetSummary(now, big.NewInt(10_000_000))
	assert.Equal(t, big.NewInt(500), summary.ActiveSupplyDrift)
	assert.Equal(t, 2, summary.PendingMessagesCount)
	assert.Equal(t, 1, summary.PendingNearPruneCount)
}
