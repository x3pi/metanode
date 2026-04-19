package processor

import (
	"fmt"

	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// runPeerDiscoverySocket starts the TCP socket listener for peer discovery queries
// This allows remote Rust nodes to query this Go Master for epoch/validator info
func (bp *BlockProcessor) runPeerDiscoverySocket(peerRPCPort int) {
	if peerRPCPort <= 0 {
		logger.Warn("⚠️ [PEER DISCOVERY] peer_rpc_port not configured or invalid, skipping TCP listener")
		return
	}

	// Construct TCP socket address from peer_rpc_port
	// Bind to 0.0.0.0 to accept connections from all network interfaces
	tcpAddress := fmt.Sprintf("tcp://0.0.0.0:%d", peerRPCPort)

	logger.Info("🌐 [PEER DISCOVERY] Starting TCP socket listener for peer queries on %s", tcpAddress)

	// Start TCP socket executor for peer discovery
	// This is SEPARATE from the local Unix socket used for local Rust-Go communication
	se, err := executor.RunSocketExecutor(tcpAddress, bp.storageManager, bp.chainState, bp.genesisPath)
	if err != nil {
		logger.Error("❌ [PEER DISCOVERY] Error starting TCP listener: %v", err)
		// Don't return - this is non-critical, local Unix socket can continue
		return
	}

	// Inject ForceCommit callback for Event-Driven Block Generation
	se.GetRequestHandler().SetForceCommitCallback(func() {
		logger.Info("⚡ [RUST TRIGGER TCP] Rust triggered ForceCommit via TCP! Generating block immediately.")
		bp.ForceCommit()
	})

	logger.Info("✅ [PEER DISCOVERY] TCP listener started successfully on %s (peer nodes can now query this Go Master)", tcpAddress)

	// Keep this goroutine alive to maintain TCP listener
	// Block forever to keep listener running
	select {}
}
