package tx_processor

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ProcessTransactionsOptimistic is the entry point for Block-STM style execution.
func ProcessTransactionsOptimistic(
	ctx context.Context,
	chainState *blockchain.ChainState,
	groupedGroups []grouptxns.RelativeGroup,
	lastBlockHeader types.BlockHeader,
	enableTrace bool,
	isCache bool,
	blockTime uint64,
	leaderAddr common.Address,
	skipSignatureVerify bool,
) (
	[]types.Transaction,
	[]types.Receipt,
	[]types.ExecuteSCResult,
	map[common.Hash]common.Address,
) {
	var totalTxs int
	hasEvmTx := false
	for _, group := range groupedGroups {
		totalTxs += len(group.Items)
		for _, item := range group.Items {
			tx := item.Tx
			if !tx.IsRegularTransaction() {
				hasEvmTx = true
			}
			to := tx.ToAddress()
			if to == mt_common.VALIDATOR_CONTRACT_ADDRESS ||
				to == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS ||
				to == utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
				hasEvmTx = true
			}
		}
	}
	if totalTxs == 0 {
		return nil, nil, nil, nil
	}
	logger.Info("🔍 [BLOCK-STM-DEBUG] Block contains %d txs, hasEvmTx=%v", totalTxs, hasEvmTx)

	startPreload := time.Now()
	uniqueAddrMap := make(map[common.Address]struct{}, totalTxs*2)
	addrSlice := make([]common.Address, 0, totalTxs*2)
	for _, group := range groupedGroups {
		for _, item := range group.Items {
			tx := item.Tx
			fromAddr := tx.FromAddress()
			if _, seen := uniqueAddrMap[fromAddr]; !seen {
				uniqueAddrMap[fromAddr] = struct{}{}
				addrSlice = append(addrSlice, fromAddr)
			}
			toAddr := tx.ToAddress()
			if _, seen := uniqueAddrMap[toAddr]; !seen {
				uniqueAddrMap[toAddr] = struct{}{}
				addrSlice = append(addrSlice, toAddr)
			}
		}
	}
	chainState.GetAccountStateDB().PreloadAccounts(addrSlice)
	logger.Debug("⚡ [PERF] Pre-fetched %d unique addresses in %v", len(addrSlice), time.Since(startPreload))

	// Pre-verify BLS signatures in parallel to avoid CPU bottlenecks in workers
	if !skipSignatureVerify {
		startPreVerify := time.Now()
		flatTxs := make([]types.Transaction, 0, totalTxs)
		for _, group := range groupedGroups {
			for _, item := range group.Items {
				flatTxs = append(flatTxs, item.Tx)
			}
		}
		PreVerifySignatures(flatTxs, chainState)
		logger.Debug("⚡ [PERF] Pre-verified %d signatures in parallel in %v", len(flatTxs), time.Since(startPreVerify))
	} else {
		logger.Debug("⚡ [PERF] Skipped BLS pre-verify (transactions from consensus block)")
	}

	// =========================================================================
	// FAST-PATH: All-Native-Transfer Blocks (Bypass Block-STM)
	// =========================================================================
	// When ALL TXs are simple value transfers (no EVM, no contract calls),
	// we bypass the full Block-STM machinery (Union-Find, ValidationStateCache,
	// conflict detection, speculative re-execution) and process directly on the
	// global AccountStateDB. This eliminates:
	//   - 25K+ localAccountDB/localSmartContractDB allocations
	//   - 25K+ goroutine job scheduling overhead
	//   - Conflict detection rounds (always 0 conflicts for native TXs)
	//   - ValidationStateCache merge overhead
	//
	// FORK-SAFETY: Native transfers use AccountStateDB's sharded locks
	// (accountLocks[address[0:2]]) for thread-safe concurrent mutations.
	// Each sender is unique per group (guaranteed by UnionFind grouping),
	// so no data race on sender state. Receiver AddBalance is also lock-protected.
	// =========================================================================
	if !hasEvmTx {
		return processNativeTransfersFastPath(
			ctx, chainState, groupedGroups, totalTxs,
			enableTrace, leaderAddr, skipSignatureVerify,
		)
	}

	// -------------------------------------------------------------------------
	// 2. True Block-STM (Aptos-style Deterministic Parallel Execution)
	// -------------------------------------------------------------------------

	flatTxs := make([]types.Transaction, 0, totalTxs)
	for _, group := range groupedGroups {
		for _, item := range group.Items {
			flatTxs = append(flatTxs, item.Tx)
		}
	}

	trueStm := NewTrueBlockSTM(flatTxs)
	rawTransactions, rawReceipts, rawExecuteSCResults, rawMvmIdMap := trueStm.Process(ctx, chainState, leaderAddr, lastBlockHeader)

	allTransactions := make([]types.Transaction, 0, len(rawReceipts))
	allReceipts := make([]types.Receipt, 0, len(rawReceipts))
	allExecuteSCResults := make([]types.ExecuteSCResult, 0, len(rawReceipts))
	allMvmIdMap := make(map[common.Hash]common.Address, len(rawMvmIdMap))

	blockTxIndex := uint64(0)
	for i, rcp := range rawReceipts {
		if rcp != nil {
			rcp.SetTransactionIndex(0) // TODO: actual index
			rcp.SetBlockTransactionIndex(blockTxIndex)
			blockTxIndex++
			
			tx := rawTransactions[i]
			allTransactions = append(allTransactions, tx)
			allReceipts = append(allReceipts, rcp)
			
			if rawExecuteSCResults != nil && i < len(rawExecuteSCResults) && rawExecuteSCResults[i] != nil {
				allExecuteSCResults = append(allExecuteSCResults, rawExecuteSCResults[i])
			}
			
			if mvmId, ok := rawMvmIdMap[tx.Hash()]; ok {
				allMvmIdMap[tx.Hash()] = mvmId
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════
	// BLOCK-STM ALREADY COMMITTED ALL STATE VIA MVCC
	// ═══════════════════════════════════════════════════════════════
	// DO NOT call mergeMvmResults + applyMergedExecuteResult here!
	// TrueBlockSTM.Process() already commits:
	//   1. Account states (balance, nonce, lastHash, deviceKey) via ExportLatest → SetState
	//   2. Storage values via storageMap.ExportLatest → SetStorageValue
	//   3. Leader gas reward via AddBalance(leaderAddr, totalGasFee)
	//
	// Calling applyMergedExecuteResult would DOUBLE-WRITE nonces from C++ EVM results,
	// overwriting Block-STM's correct sequential nonces with stale C++ values.
	// This was the ROOT CAUSE of non-deterministic hash mismatches between nodes:
	//   - Block-STM sets nonce=3 (correct, after 3 TXs from same sender)
	//   - C++ EVM MapNonce returns nonce=2 (stale, only knows about its own TX)
	//   - applyMergedExecuteResult overwrites nonce=3 → nonce=2 (WRONG!)
	//
	// However, we still need to apply CODE changes (deploy contracts) because
	// Block-STM MVCC does not track contract code deployment.
	mergedRs := mergeMvmResults(allExecuteSCResults, leaderAddr)
	if mergedRs != nil {
		applyCodeAndStorageRootOnly(chainState, mergedRs)
	}

	return allTransactions, allReceipts, allExecuteSCResults, allMvmIdMap
}
