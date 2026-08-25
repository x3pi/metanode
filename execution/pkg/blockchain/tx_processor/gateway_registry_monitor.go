package tx_processor

import (
	"context"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain/rootanchor"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// GatewayRegistryMonitor periodically compares this chain's locally-committed (consensus-agreed)
// ChainRegistry entries against Root Anchor's copy and reports drift.
//
// FORK-SAFETY: this is READ-ONLY with respect to consensus state. It calls loadGatewayEngine()
// (a read) but never saveGatewayEngine() (a write) — deliberately. ChainRegistry is folded into
// this chain's state root every block (see gateway_handler.go's storage model doc comment), so
// any write to it must be replayed identically by every validator. A background goroutine polling
// an external HTTP endpoint on its own schedule and writing the result directly into state would
// make validators disagree the moment two of them poll at different times or get different
// answers — an immediate, guaranteed fork. The correct way to actually UPDATE ChainRegistry is a
// transaction ordered through consensus and cert-verified by every validator identically
// (cross_chain.ApplyCommitteeUpdate already implements the verification; wiring it to a real
// CommitteeUpdate transaction triggered from epoch transition is Milestone C's job, not this
// monitor's).
//
// What this monitor DOES provide is real operational value on its own: the "drift ChainRegistry"
// dashboard signal note/cross_chain_root_anchor_architecture.md mục 3 already calls for
// (deploy/ansible/monitors row: "Thêm dashboard theo dõi độ trễ relay + drift ChainRegistry") —
// visibility into how far a chain's on-chain-agreed registry has fallen behind Root Anchor's,
// which is exactly the signal that says "Milestone C's CommitteeUpdate flow needs to run."
type GatewayRegistryMonitor struct {
	client       *rootanchor.Client
	chainState   *blockchain.ChainState
	pollInterval time.Duration

	mu       sync.RWMutex
	lastSeen map[uint64]cross_chain.ChainRegistry // last successfully fetched Root Anchor snapshot, by chainID
	drift    map[uint64]bool                      // true if Root Anchor's epoch is ahead of our locally-committed epoch
}

// NewGatewayRegistryMonitor builds a monitor. pollInterval <= 0 defaults to 60s, matching
// config.CrossChainConfig.RootAnchorPollIntervalSeconds's documented default.
func NewGatewayRegistryMonitor(client *rootanchor.Client, chainState *blockchain.ChainState, pollInterval time.Duration) *GatewayRegistryMonitor {
	if pollInterval <= 0 {
		pollInterval = 60 * time.Second
	}
	return &GatewayRegistryMonitor{
		client:       client,
		chainState:   chainState,
		pollInterval: pollInterval,
		lastSeen:     make(map[uint64]cross_chain.ChainRegistry),
		drift:        make(map[uint64]bool),
	}
}

// Run polls once immediately, then every pollInterval, until ctx is cancelled. Meant to be
// launched as `go monitor.Run(ctx)` — mirrors the launch pattern of this codebase's other
// background workers (e.g. stateCommitter in processor/block_processor_state.go).
func (m *GatewayRegistryMonitor) Run(ctx context.Context) {
	logger.Info("✅ Gateway ChainRegistry Drift Monitor started (poll interval: %s)", m.pollInterval)
	m.poll(ctx)

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("Gateway ChainRegistry Drift Monitor stopped")
			return
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

func (m *GatewayRegistryMonitor) poll(ctx context.Context) {
	// READ ONLY: see the fork-safety doc comment above — this function must never call
	// saveGatewayEngine.
	engine, err := loadGatewayEngine(m.chainState)
	if err != nil {
		logger.Warn("⚠️ [ROOT ANCHOR MONITOR] failed to read local ChainRegistry, skipping this poll: %v", err)
		return
	}

	for chainID, localEntry := range engine.ChainRegistry {
		if chainID == engine.LocalChainID {
			continue
		}
		remote, exists, err := m.client.GetChainRegistry(ctx, chainID)
		if err != nil {
			// Network failure or open circuit breaker — Zero-Fork: this chain keeps operating
			// normally, only cross-chain-to-this-remote-chain visibility is stale until the next
			// successful poll. Never panics, never blocks anything else.
			logger.Warn("⚠️ [ROOT ANCHOR MONITOR] chain %d: %v", chainID, err)
			continue
		}
		if !exists {
			logger.Warn("⚠️ [ROOT ANCHOR MONITOR] chain %d is registered locally but Root Anchor reports it does not exist", chainID)
			continue
		}

		isDrifting := remote.Epoch > localEntry.Epoch

		m.mu.Lock()
		m.lastSeen[chainID] = *remote
		m.drift[chainID] = isDrifting
		m.mu.Unlock()

		if isDrifting {
			lag := remote.Epoch - localEntry.Epoch
			logger.Warn("⚠️ [ROOT ANCHOR MONITOR] chain %d: local ChainRegistry epoch=%d is %d epochs behind Root Anchor epoch=%d — needs CommitteeUpdate catch-up transactions (Milestone C)",
				chainID, localEntry.Epoch, lag, remote.Epoch)
		}
	}
}

// Snapshot returns the last successfully fetched Root Anchor copy of chainID's ChainRegistry, if
// any poll has succeeded for it yet.
func (m *GatewayRegistryMonitor) Snapshot(chainID uint64) (cross_chain.ChainRegistry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.lastSeen[chainID]
	return r, ok
}

// IsDrifting reports whether the last successful poll found chainID's locally-committed
// ChainRegistry epoch behind Root Anchor's. Returns false (not true) if no poll has succeeded yet
// — absence of information is not evidence of drift.
func (m *GatewayRegistryMonitor) IsDrifting(chainID uint64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.drift[chainID]
}

// DriftEpochs returns how many epochs chainID's locally-committed ChainRegistry is behind
// Root Anchor's copy. Returns 0 if not drifting or if no snapshot has been fetched yet.
func (m *GatewayRegistryMonitor) DriftEpochs(chainID uint64) uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	remote, ok := m.lastSeen[chainID]
	if !ok {
		return 0
	}
	engine, err := loadGatewayEngine(m.chainState)
	if err != nil {
		return 0
	}
	local, ok := engine.ChainRegistry[chainID]
	if !ok || remote.Epoch <= local.Epoch {
		return 0
	}
	return remote.Epoch - local.Epoch
}
