package parentchain

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ParentChainLastBlock tracks the latest committed block number.
	ParentChainLastBlock = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "parent_chain_last_block",
		Help: "Current committed block number of the parent chain node",
	})

	// ParentChainForkDetected tracks whether a fork attempt or conflict was detected (1) or clean (0).
	ParentChainForkDetected = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "parent_chain_fork_detected",
		Help: "1 if fork conflict was detected on this node, 0 otherwise",
	})

	// ParentChainStateRoot records the current state root hash string as a label.
	ParentChainStateRoot = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "parent_chain_state_root",
		Help: "State root tracking gauge with current state root as label",
	}, []string{"state_root"})

	// ParentChainTxsTotal counts total parent chain transactions processed.
	ParentChainTxsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "parent_chain_txs_total",
		Help: "Total parent chain transactions executed",
	})

	// ParentChainBlocksTotal counts total parent chain blocks processed.
	ParentChainBlocksTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "parent_chain_blocks_total",
		Help: "Total blocks applied by parent chain",
	})
)

// UpdateMetrics updates the Prometheus gauges for the given block number, state root, and fork status.
func UpdateMetrics(lastBlock uint64, stateRootHex string, forkDetected bool) {
	ParentChainLastBlock.Set(float64(lastBlock))
	if forkDetected {
		ParentChainForkDetected.Set(1)
	} else {
		ParentChainForkDetected.Set(0)
	}
	if stateRootHex != "" {
		ParentChainStateRoot.Reset()
		ParentChainStateRoot.WithLabelValues(stateRootHex).Set(1)
	}
}
