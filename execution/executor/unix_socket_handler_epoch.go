package executor

import (
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// ============================================================================
// GO-AUTHORITATIVE GEI: Recovery RPC
// ============================================================================

// HandleGetLastHandledCommitIndexRequest returns Go's current execution state
// so Rust can resume from the correct point after restart.
// This replaces the fragile fragment_offset reconstruction logic.
func (rh *RequestHandler) HandleGetLastHandledCommitIndexRequest(request *pb.GetLastHandledCommitIndexRequest) (*pb.GetLastHandledCommitIndexResponse, error) {
	lastGEI := storage.GetLastGlobalExecIndex()
	lastBlockNumber := storage.GetLastBlockNumber()
	currentEpoch := rh.chainState.GetCurrentEpoch()

	// Go is the authoritative source for lastHandledCommitIndex
	isAuthoritative := true

	// FORK-SAFETY: Return the epoch of the commit_index, not just current epoch.
	// If lastHandledCommitEpoch != current_epoch, the commit_index is stale (from a previous epoch).
	commitIndex := storage.GetLastHandledCommitIndex()
	commitEpoch := storage.GetLastHandledCommitEpoch()

	// Epoch validation: If commit belongs to a different epoch, report 0 to prevent cross-epoch fork
	if commitEpoch > 0 && commitEpoch != currentEpoch {
		logger.Warn("🚨 [GO-AUTH GEI] EPOCH MISMATCH in recovery: lastHandledCommitIndex=%d belongs to epoch=%d but current epoch=%d. Reporting commit_index=0 to Rust.",
			commitIndex, commitEpoch, currentEpoch)
		commitIndex = 0
	}

	var lastBlockTimestampMs uint64 = 0
	var stateRoot []byte = nil
	if lastBlockNumber > 0 {
		blockchainInstance := blockchain.GetBlockChainInstance()
		if blockchainInstance != nil {
			lastBlock := blockchainInstance.GetLastBlock()
			if lastBlock != nil {
				lastBlockTimestampMs = lastBlock.Header().TimeStamp()
				stateRoot = lastBlock.Header().AccountStatesRoot().Bytes()
			}
		}
	}

	logger.Info("🔑 [GO-AUTH GEI] Recovery query: last_commit=%d (epoch=%d), last_gei=%d, last_block=%d, current_epoch=%d, authoritative=%v, ts=%d, state_root=%x",
		commitIndex, commitEpoch, lastGEI, lastBlockNumber, currentEpoch, isAuthoritative, lastBlockTimestampMs, stateRoot)

	response := &pb.GetLastHandledCommitIndexResponse{
		LastCommitIndex:      commitIndex,
		LastGei:              lastGEI,
		LastBlockNumber:      lastBlockNumber,
		Epoch:                currentEpoch,
		IsAuthoritative:      isAuthoritative,
		LastBlockTimestampMs: lastBlockTimestampMs,
		StateRoot:            stateRoot,
	}

	return response, nil
}
