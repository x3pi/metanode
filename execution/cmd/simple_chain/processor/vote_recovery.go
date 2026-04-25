package processor

// vote_recovery.go
//
// Khi chain restart/sập, bộ nhớ voteMap (CCBatchVoteAccumulator) bị mất.
// File này quét lại các blocks trên local chain để phục hồi votes chưa đủ quorum.
//
// Flow:
// 1. Đọc getScanBlockRange(0) từ config contract → (minBlock, maxBlock)
// 2. Scan từng block từ minBlock → maxBlock
// 3. Dùng TransactionStateDB để load full TX object (giống ProcessGetTransactionsByBlockNumber)
// 4. Với mỗi TX có toAddress == CROSS_CHAIN_CONTRACT_ADDRESS + isBatchSubmit:
//    - Type 101 (EXECUTE): ghi nhận key đã executed
//    - Type 100 (SIG_ACK): import vote vào accumulator
// 5. Chỉ giữ votes cho key chưa có EXECUTE tương ứng
//
// Gọi 1 lần duy nhất khi chain khởi động lại.

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/types"
)

var (
	voteRecoveryOnce sync.Once
	voteRecoveryDone atomic.Bool // atomic để tránh data race từ nhiều goroutine virtual processor
)

// voteRecord lưu thông tin 1 vote type=100 tìm được trong block scan
type voteRecord struct {
	embassyAddr string
	key         [32]byte
	blockNum    uint64
}

// TryRecoverVotesOnce chạy vote recovery 1 lần duy nhất.
// Gọi từ processBatchSubmitVirtual khi nhận batchSubmit TX đầu tiên sau restart.
// Nếu đã chạy rồi thì skip ngay.
func TryRecoverVotesOnce(
	chainState *blockchain.ChainState,
	storageManager *storage.StorageManager,
	dummyTx types.Transaction,
) {
	if voteRecoveryDone.Load() {
		return
	}
	voteRecoveryOnce.Do(func() {
		logger.Info("[VoteRecovery] 🔄 Starting one-time vote recovery on chain startup...")

		ccHandler, err := cross_chain_handler.GetCrossChainHandler()
		if err != nil || ccHandler == nil {
			logger.Warn("[VoteRecovery] CrossChainHandler not ready, skip recovery: %v", err)
			voteRecoveryDone.Store(true)
			return
		}

		// Đọc getScanBlockRange(0) từ config contract (0 = local chain)
		minBlock, maxBlock, err := fetchScanBlockRange(ccHandler, chainState, dummyTx)
		if err != nil {
			logger.Warn("[VoteRecovery] fetchLocalScanBlockRange failed: %v, skip recovery", err)
			voteRecoveryDone.Store(true)
			return
		}

		if minBlock == 0 || maxBlock == 0 {
			logger.Info("[VoteRecovery] No local scan progress recorded (min=%d, max=%d), skip recovery", minBlock, maxBlock)
			voteRecoveryDone.Store(true)
			return
		}

		logger.Info("[VoteRecovery] 🔍 Scanning local blocks %d → %d for missing votes...", minBlock, maxBlock)

		recovered, err := scanAndRecoverVotes(chainState, storageManager, ccHandler, minBlock, maxBlock)
		if err != nil {
			logger.Error("[VoteRecovery] ❌ Error during recovery: %v", err)
		} else {
			logger.Info("[VoteRecovery] ✅ Recovery complete: %d votes recovered", recovered)
		}

		voteRecoveryDone.Store(true)
	})
}

// scanAndRecoverVotes quét blocks từ minBlock → maxBlock trên local chain,
// load full TX objects giống ProcessGetTransactionsByBlockNumber,
// phân loại SIG_ACK/EXECUTE và import lại votes.
func scanAndRecoverVotes(
	chainState *blockchain.ChainState,
	storageManager *storage.StorageManager,
	ccHandler *cross_chain_handler.CrossChainHandler,
	minBlock uint64,
	maxBlock uint64,
) (recoveredVotes int, err error) {
	bc := blockchain.GetBlockChainInstance()
	acc := GetCCBatchVoteAccumulator()
	crossChainAddr := mt_common.CROSS_CHAIN_CONTRACT_ADDRESS
	executedKeys := make(map[[32]byte]bool)
	var pendingVotes []voteRecord
	for blockNum := minBlock; blockNum <= maxBlock; blockNum++ {
		// Load block → transaction hashes
		blockHash, ok := bc.GetBlockHashByNumber(blockNum)
		if !ok {
			continue
		}
		blockData, err := chainState.GetBlockDatabase().GetBlockByHash(blockHash)
		if err != nil {
			logger.Warn("[VoteRecovery] Cannot load block %d: %v", blockNum, err)
			continue
		}

		txHashes := blockData.Transactions()
		if len(txHashes) == 0 {
			continue
		}

		// Load full TX objects từ TransactionStateDB (giống ProcessGetTransactionsByBlockNumber)
		txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(
			blockData.Header().TransactionsRoot(),
			storageManager.GetStorageTransaction(),
		)
		if err != nil {
			logger.Warn("[VoteRecovery] Cannot create txDB for block %d: %v", blockNum, err)
			continue
		}
		for _, txHash := range txHashes {
			tx, err := txDB.GetTransaction(txHash)
			if err != nil {
				continue
			}
			// Chỉ xét TX gửi đến CROSS_CHAIN_CONTRACT_ADDRESS
			if tx.ToAddress() != crossChainAddr {
				continue
			}
			inputData := tx.CallData().Input()
			if len(inputData) < 4 || !ccHandler.IsBatchSubmitTx(inputData) {
				continue
			}

			// QUAN TRỌNG: Tính vote key bằng cách re-pack chỉ EVENTS (loại bỏ pubkey),
			// giống hệt logic trong processBatchSubmitVirtual.
			// Nếu dùng sha256(inputData[4:]) sẽ include pubkey → key khác → không match.
			batchMethod, methodOk := ccHandler.GetABI().Methods["batchSubmit"]
			if !methodOk {
				continue
			}
			batchArgs, unpackErr := batchMethod.Inputs.Unpack(inputData[4:])
			if unpackErr != nil || len(batchArgs) < 1 {
				logger.Warn("[VoteRecovery] ABI unpack failed at block %d tx %s: %v", blockNum, txHash.Hex(), unpackErr)
				continue
			}
			eventsOnlyPacked, packErr := batchMethod.Inputs[:1].Pack(batchArgs[0])
			if packErr != nil {
				logger.Warn("[VoteRecovery] re-pack events failed at block %d tx %s: %v", blockNum, txHash.Hex(), packErr)
				continue
			}
			key := sha256.Sum256(eventsOnlyPacked)

			sender := tx.FromAddress()

			// ReadOnly=false → TX đã được execute (EXECUTE path) → key này đã xong.
			// ReadOnly=true → TX chỉ tăng nonce (SIG_ACK path) → cần recovery vote.
			if !tx.GetReadOnly() {
				// EXECUTE: key này đã xong
				executedKeys[key] = true
				logger.Info("[VoteRecovery] ✅ EXECUTE (readOnly=false) at block %d: key=%x...", blockNum, key[:8])
			} else {
				// SIG_ACK: vote chưa đủ quorum → lưu lại
				pendingVotes = append(pendingVotes, voteRecord{
					embassyAddr: sender.Hex(),
					key:         key,
					blockNum:    blockNum,
				})
				logger.Info("[VoteRecovery] 📝 SIG_ACK (readOnly=true) at block %d: embassy=%s, key=%x...",
					blockNum, sender.Hex(), key[:8])
			}
		}
	}

	// Phase 2: Import chỉ votes mà key chưa có EXECUTE tương ứng
	for _, vote := range pendingVotes {
		if executedKeys[vote.key] {
			// Key đã có type 101 → không cần recovery
			continue
		}

		voteCount, isFirstQuorum, voteErr := acc.AddVoteByKey(vote.embassyAddr, vote.key)
		if voteErr != nil {
			// Duplicate hoặc đã execute → skip
			continue
		}

		recoveredVotes++
		if isFirstQuorum {
			logger.Info("[VoteRecovery] 🚀 Quorum reached after recovery! key=%x... votes=%d",
				vote.key[:8], voteCount)
		} else {
			logger.Info("[VoteRecovery] 📥 Vote recovered: embassy=%s key=%x... votes=%d",
				vote.embassyAddr, vote.key[:8], voteCount)
		}
	}

	logger.Info("[VoteRecovery] Scanned %d→%d: %d SIG_ACK found, %d EXECUTE found, %d votes recovered",
		minBlock, maxBlock, len(pendingVotes), len(executedKeys), recoveredVotes)

	return recoveredVotes, nil
}

// fetchScanBlockRange gọi getScanBlockRange(0) trên config contract
// thông qua off-chain call (giống FetchChainId pattern trong cross_chain_config_helper.go).
func fetchScanBlockRange(
	ccHandler *cross_chain_handler.CrossChainHandler,
	chainState *blockchain.ChainState,
	dummyTx types.Transaction,
) (minBlock uint64, maxBlock uint64, err error) {
	cfg := chainState.GetConfig()
	if cfg == nil {
		return 0, 0, fmt.Errorf("chainState config is nil")
	}

	configContractHex := cfg.CrossChain.ConfigContract
	if configContractHex == "" {
		return 0, 0, fmt.Errorf("config_contract not set")
	}

	// Gọi off-chain: getScanBlockRange(0) → (uint256 minBlock, uint256 maxBlock)
	result, err := cross_chain_handler.CallConfigView(
		ccHandler, chainState, dummyTx,
		"getScanBlockRange",
		big.NewInt(0),
	)
	if err != nil {
		return 0, 0, fmt.Errorf("getScanBlockRange(0) call failed: %v", err)
	}

	// Unpack 2 uint256
	if len(result) < 2 {
		return 0, 0, fmt.Errorf("unexpected result length: %d", len(result))
	}

	minBig, ok1 := result[0].(*big.Int)
	maxBig, ok2 := result[1].(*big.Int)
	if !ok1 || !ok2 {
		return 0, 0, fmt.Errorf("unexpected result types")
	}

	return minBig.Uint64(), maxBig.Uint64(), nil
}
