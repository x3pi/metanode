package executor

import (
	"fmt"
	"sort"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

type RequestHandler struct {
	storageManager          *storage.StorageManager
	chainState              *blockchain.ChainState
	genesisPath             string
	snapshotManager         *SnapshotManager // Automatic snapshot management
	connectionsManager      network.ConnectionsManager
	messageSender           network.MessageSender
	forceCommitCallback     func()                                                                  // Callback to trigger ForceCommit in BlockProcessor
	updateLastBlockCallback func(blk types.Block)                                                   // Callback to let Rust explicitly update Go memory state
	broadcastCallback       func(blk *block.Block, backupData []byte, blockNum uint64, txCount int) // Callback to broadcast synced blocks to network
}

func NewRequestHandler(storageManager *storage.StorageManager, chainState *blockchain.ChainState, genesisPath string) *RequestHandler {
	return &RequestHandler{
		storageManager: storageManager,
		chainState:     chainState,
		genesisPath:    genesisPath,
	}
}

// SetSnapshotManager configures the snapshot manager for the request handler
func (rh *RequestHandler) SetSnapshotManager(sm *SnapshotManager) {
	rh.snapshotManager = sm
}

// SetNetworkComponents sets ConnectionsManager and MessageSender for broadcasting to Sub nodes
func (rh *RequestHandler) SetNetworkComponents(cm network.ConnectionsManager, ms network.MessageSender) {
	rh.connectionsManager = cm
	rh.messageSender = ms
}

// SetForceCommitCallback sets the callback used to force an immediate block commit
func (rh *RequestHandler) SetForceCommitCallback(cb func()) {
	rh.forceCommitCallback = cb
}

// SetUpdateLastBlockCallback allows Rust commands to explicitly set Go's in-memory block state
func (rh *RequestHandler) SetUpdateLastBlockCallback(cb func(blk types.Block)) {
	rh.updateLastBlockCallback = cb
}

// SetBroadcastCallback allows RequestHandler to trigger block broadcast during execute mode sync
func (rh *RequestHandler) SetBroadcastCallback(cb func(blk *block.Block, backupData []byte, blockNum uint64, txCount int)) {
	rh.broadcastCallback = cb
}

// NOTE (Sync Architecture Redesign, Apr 2026):
// broadcastBackupToSub was REMOVED. Sub nodes now exclusively receive blocks
// through the normal block_processor_broadcast.go pipeline AFTER Master executes
// them. This prevents pre-execution delivery that could cause state divergence.
// See: restart_sync_requirements.md §6 — Go Sub Node Isolation.

// HandleBlockRequest processes a BlockRequest and returns a ValidatorList.
func (rh *RequestHandler) HandleBlockRequest(request *pb.BlockRequest) (*pb.ValidatorList, error) {
	blockNumber := request.GetBlockNumber()
	logger.Error("Handling block request for block number:", blockNumber)
	blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
	if !ok {
		return nil, fmt.Errorf("cannot find block hash for block number %d", blockNumber)
	}

	blockData, err := rh.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
	if err != nil {
		return nil, fmt.Errorf("could not get block data by hash %s: %w", blockHash, err)
	}

	blockDatabase := block.NewBlockDatabase(rh.storageManager.GetStorageBlock())
	chainStateNew, err := blockchain.NewChainState(rh.storageManager, blockDatabase, blockData.Header(), rh.chainState.GetConfig(), rh.chainState.GetFreeFeeAddress(), "") // Empty backupPath for temporary chain state
	if err != nil {
		return nil, fmt.Errorf("could not create new chain state: %w", err)
	}

	validators, err := chainStateNew.GetStakeStateDB().GetAllValidators()
	if err != nil {
		return nil, fmt.Errorf("could not get all validators from stake DB: %w", err)
	}

	// CRITICAL FORK-SAFETY FIX (G-C1): Sort by AuthorityKey to match ALL other validator handlers.
	// Previously sorted by Address().Hex() which produces a DIFFERENT order than
	// HandleGetActiveValidatorsRequest, HandleGetValidatorsAtBlockRequest, etc.
	// which all sort by AuthorityKey(). Committee mismatch → fork.
	sort.Slice(validators, func(i, j int) bool {
		return validators[i].AuthorityKey() < validators[j].AuthorityKey()
	})

	// Map the database validators to protobuf validators.
	validatorList := &pb.ValidatorList{}
	for _, dbValidator := range validators {
		val := &pb.Validator{
			Address:                    dbValidator.Address().Hex(),
			PrimaryAddress:             dbValidator.PrimaryAddress(),
			WorkerAddress:              dbValidator.WorkerAddress(),
			P2PAddress:                 dbValidator.P2PAddress(),
			Name:                       dbValidator.Name(),
			Description:                dbValidator.Description(),
			Website:                    dbValidator.Website(),
			Image:                      dbValidator.Image(),
			CommissionRate:             dbValidator.CommissionRate(),
			MinSelfDelegation:          dbValidator.MinSelfDelegation().String(),
			TotalStakedAmount:          dbValidator.TotalStakedAmount().String(),
			AccumulatedRewardsPerShare: dbValidator.AccumulatedRewardsPerShare().String(),
			PubkeyBls:                  dbValidator.PubKeyBls(),
			PubkeySecp:                 dbValidator.PubKeySecp(),
		}
		validatorList.Validators = append(validatorList.Validators, val)
	}

	return validatorList, nil
}

// HandleForceCommitRequest processes a ForceCommitRequest from Rust and triggers an immediate block generation
func (rh *RequestHandler) HandleForceCommitRequest(request *pb.ForceCommitRequest) (*pb.ForceCommitResponse, error) {
	reason := request.GetReason()
	if reason == "" {
		reason = "unspecified"
	}
	// logger.Info("🔄 [FORCE COMMIT] Received ForceCommitRequest from Rust (reason: %s)", reason)

	// Since RequestHandler relies on the global BlockChain instance, we can fetch
	// the BlockProcessor if it's accessible. For this architecture, block processing
	// is managed higher up, but we can access it via the App singleton or state.
	// Wait! We don't have direct access to bp here.
	// Let's rely on an injected callback or package-level function if needed.
	// We will inject the ForceCommit callback.
	if rh.forceCommitCallback != nil {
		rh.forceCommitCallback()
		return &pb.ForceCommitResponse{
			Success: true,
			Message: "ForceCommit signal sent successfully",
		}, nil
	}

	return &pb.ForceCommitResponse{
		Success: false,
		Message: "ForceCommit callback is not initialized",
	}, nil
}
