package tx_processor

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ProcessTransactionsOptimistic is the entry point for Block-STM style execution.
//
// OPTIMIZATION: Groups are pre-classified (GroupKind) by GroupTransactionsDeterministic.
//   - GroupKindNativeOnly  → processNativeTransfersFastPath (lock-free, zero conflict overhead)
//   - GroupKindContractOnly / GroupKindMixed → TrueBlockSTM (MVCC parallel execution)
//
// When a block contains BOTH native-only and EVM groups the two pipelines run
// CONCURRENTLY in separate goroutines, then results are merged in original group
// order for determinism.
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
	for _, group := range groupedGroups {
		totalTxs += len(group.Items)
	}
	if totalTxs == 0 {
		return nil, nil, nil, nil
	}

	// ─── Partition by Kind (O(n) over groups, no item re-scan) ───────────────
	nativeGroups := make([]grouptxns.RelativeGroup, 0, len(groupedGroups))
	nativeOrigIdx := make([]int, 0, len(groupedGroups))
	evmGroups := make([]grouptxns.RelativeGroup, 0, len(groupedGroups))
	evmOrigIdx := make([]int, 0, len(groupedGroups))

	nativeTxCount := 0
	evmTxCount := 0

	for i, g := range groupedGroups {
		if g.Kind == grouptxns.GroupKindNativeOnly {
			nativeGroups = append(nativeGroups, g)
			nativeOrigIdx = append(nativeOrigIdx, i)
			nativeTxCount += len(g.Items)
		} else {
			evmGroups = append(evmGroups, g)
			evmOrigIdx = append(evmOrigIdx, i)
			evmTxCount += len(g.Items)
		}
	}
	hasEvmTx := len(evmGroups) > 0

	logger.Info("🔍 [BLOCK-STM-DEBUG] Block contains %d txs, hasEvmTx=%v (native=%d evm=%d)",
		totalTxs, hasEvmTx, nativeTxCount, evmTxCount)

	// ─── Pre-load accounts (shared across both pipelines) ─────────────────────
	startPreload := time.Now()
	uniqueAddrMap := make(map[common.Address]struct{}, totalTxs*2)
	addrSlice := make([]common.Address, 0, totalTxs*2)
	for _, group := range groupedGroups {
		for _, item := range group.Items {
			tx := item.Tx
			if _, seen := uniqueAddrMap[tx.FromAddress()]; !seen {
				uniqueAddrMap[tx.FromAddress()] = struct{}{}
				addrSlice = append(addrSlice, tx.FromAddress())
			}
			if _, seen := uniqueAddrMap[tx.ToAddress()]; !seen {
				uniqueAddrMap[tx.ToAddress()] = struct{}{}
				addrSlice = append(addrSlice, tx.ToAddress())
			}
		}
	}
	chainState.GetAccountStateDB().PreloadAccounts(addrSlice)
	logger.Debug("⚡ [PERF] Pre-fetched %d unique addresses in %v", len(addrSlice), time.Since(startPreload))

	// ─── Pre-verify BLS signatures once (covers both pipelines) ───────────────
	if !skipSignatureVerify {
		startPreVerify := time.Now()
		flatAll := make([]types.Transaction, 0, totalTxs)
		for _, group := range groupedGroups {
			for _, item := range group.Items {
				flatAll = append(flatAll, item.Tx)
			}
		}
		PreVerifySignatures(flatAll, chainState)
		logger.Debug("⚡ [PERF] Pre-verified %d signatures in %v", len(flatAll), time.Since(startPreVerify))
	} else {
		logger.Debug("⚡ [PERF] Skipped BLS pre-verify (consensus block)")
	}

	// ─── Fast-path only: no EVM at all ────────────────────────────────────────
	if !hasEvmTx {
		return processNativeTransfersFastPath(
			ctx, chainState, groupedGroups, totalTxs,
			enableTrace, leaderAddr, skipSignatureVerify,
		)
	}

	// ─── No native groups: pure EVM block ─────────────────────────────────────
	if len(nativeGroups) == 0 {
		return runEvmPipeline(ctx, chainState, evmGroups, lastBlockHeader, leaderAddr, blockTime)
	}

	// ─── Mixed block: run native fast-path, THEN TrueBlockSTM (sequential) ────
	// FORK-SAFETY: these two pipelines both mutate chainState's global DBs.
	// Static Union-Find grouping only guarantees disjoint addresses for what's
	// known ahead of execution (tx.RelatedAddresses()/AccessList) — an EVM call
	// can still touch an address discovered only at runtime (e.g. a transfer
	// target read from contract storage), which could coincide with an address
	// a concurrently-running native-transfer group is mutating (this includes
	// the leader's own reward account, credited by both pipelines). Running
	// them concurrently made that a real data race with node-dependent
	// scheduling outcomes. Running them sequentially removes the hazard;
	// native transfers are lock-sharded and fast, so the wall-clock cost of
	// serializing is small relative to the correctness gained.
	type groupResult struct {
		txs      []types.Transaction
		rcps     []types.Receipt
		scRs     []types.ExecuteSCResult
		mvmIdMap map[common.Hash]common.Address
	}
	results := make([]groupResult, len(groupedGroups))

	// Pipeline A – Native fast-path
	{
		txs, rcps, scRs, mvmMap := processNativeTransfersFastPath(
			ctx, chainState, nativeGroups, nativeTxCount,
			enableTrace, leaderAddr, skipSignatureVerify,
		)
		// processNativeTransfersFastPath returns flat results in group order;
		// slice them back per group using each group's item count.
		// NOTE FOR MAINTAINERS & AI: We explicitly calculate and bound-check individual
		// end indices (txsEnd, rcpsEnd, scRsEnd) against slice lengths.
		// If a transaction fails or omits a receipt/result, slice lengths may differ.
		// Independent bounds checking prevents runtime panic ("slice bounds out of range")
		// and ensures node stability during group slicing.
		off := 0
		for k, g := range nativeGroups {
			n := len(g.Items)
			end := off + n

			// Clamp slicing boundaries to actual slice lengths to prevent out-of-bounds panics
			txsEnd := end
			if txsEnd > len(txs) {
				txsEnd = len(txs)
			}
			rcpsEnd := end
			if rcpsEnd > len(rcps) {
				rcpsEnd = len(rcps)
			}
			scRsEnd := end
			if scRsEnd > len(scRs) {
				scRsEnd = len(scRs)
			}

			var tSlice []types.Transaction
			var rSlice []types.Receipt
			var sSlice []types.ExecuteSCResult

			// Safely extract slices only if starting offset is within bounds
			if off < txsEnd {
				tSlice = txs[off:txsEnd]
			}
			if off < rcpsEnd {
				rSlice = rcps[off:rcpsEnd]
			}
			if off < scRsEnd {
				sSlice = scRs[off:scRsEnd]
			}

			results[nativeOrigIdx[k]] = groupResult{
				txs:      tSlice,
				rcps:     rSlice,
				scRs:     sSlice,
				mvmIdMap: mvmMap,
			}
			off = end
		}
	}

	// Pipeline B – TrueBlockSTM for EVM / Mixed groups
	{
		flatEvmTxs := make([]types.Transaction, 0, evmTxCount)
		groupStarts := make([]int, len(evmGroups))
		for k, g := range evmGroups {
			groupStarts[k] = len(flatEvmTxs)
			for _, item := range g.Items {
				flatEvmTxs = append(flatEvmTxs, item.Tx)
			}
		}

		rawTxs, rawRcps, rawScRs, rawMvmMap := NewTrueBlockSTM(flatEvmTxs).
			Process(ctx, chainState, leaderAddr, lastBlockHeader, blockTime)

		for k, g := range evmGroups {
			start := groupStarts[k]
			end := start + len(g.Items)
			if end > len(rawRcps) {
				end = len(rawRcps)
			}
			var txSlice []types.Transaction
			var rcpSlice []types.Receipt
			var scRSlice []types.ExecuteSCResult
			if end > start {
				txSlice = rawTxs[start:end]
				rcpSlice = rawRcps[start:end]
				scRSlice = rawScRs[start:end]
			}
			results[evmOrigIdx[k]] = groupResult{
				txs:      txSlice,
				rcps:     rcpSlice,
				scRs:     scRSlice,
				mvmIdMap: rawMvmMap,
			}
		}

		// Apply code/storageRoot for deployed contracts (EVM pipeline only).
		var allScRs []types.ExecuteSCResult
		for i := range rawRcps {
			if rawRcps[i] != nil && i < len(rawScRs) && rawScRs[i] != nil {
				allScRs = append(allScRs, rawScRs[i])
			}
		}
		if mergedRs := mergeMvmResults(allScRs, leaderAddr); mergedRs != nil {
			applyCodeAndStorageRootOnly(chainState, mergedRs)
		}
	}

	// ─── Merge in original group order (deterministic) ─────────────────────────
	allTransactions := make([]types.Transaction, 0, totalTxs)
	allReceipts := make([]types.Receipt, 0, totalTxs)
	allExecuteSCResults := make([]types.ExecuteSCResult, 0, totalTxs)
	allMvmIdMap := make(map[common.Hash]common.Address)

	blockTxIndex := uint64(0)
	for _, res := range results {
		for i, rcp := range res.rcps {
			if rcp != nil {
				rcp.SetTransactionIndex(blockTxIndex)
				rcp.SetBlockTransactionIndex(blockTxIndex)
				blockTxIndex++
				allTransactions = append(allTransactions, res.txs[i])
				allReceipts = append(allReceipts, rcp)
				if i < len(res.scRs) && res.scRs[i] != nil {
					allExecuteSCResults = append(allExecuteSCResults, res.scRs[i])
				}
			}
		}
		for h, id := range res.mvmIdMap {
			allMvmIdMap[h] = id
		}
	}

	return allTransactions, allReceipts, allExecuteSCResults, allMvmIdMap
}

// runEvmPipeline handles a block that contains only EVM/Mixed groups (no native-only).
// Kept as a helper to keep ProcessTransactionsOptimistic readable.
func runEvmPipeline(
	ctx context.Context,
	chainState *blockchain.ChainState,
	evmGroups []grouptxns.RelativeGroup,
	lastBlockHeader types.BlockHeader,
	leaderAddr common.Address,
	blockTime uint64,
) (
	[]types.Transaction,
	[]types.Receipt,
	[]types.ExecuteSCResult,
	map[common.Hash]common.Address,
) {
	totalEvmTxs := 0
	flatTxs := make([]types.Transaction, 0)
	for _, g := range evmGroups {
		for _, item := range g.Items {
			flatTxs = append(flatTxs, item.Tx)
		}
		totalEvmTxs += len(g.Items)
	}

	rawTxs, rawRcps, rawScRs, rawMvmMap := NewTrueBlockSTM(flatTxs).
		Process(ctx, chainState, leaderAddr, lastBlockHeader, blockTime)

	allTransactions := make([]types.Transaction, 0, totalEvmTxs)
	allReceipts := make([]types.Receipt, 0, totalEvmTxs)
	allExecuteSCResults := make([]types.ExecuteSCResult, 0, totalEvmTxs)
	allMvmIdMap := make(map[common.Hash]common.Address, len(rawMvmMap))

	blockTxIndex := uint64(0)
	for i, rcp := range rawRcps {
		if rcp != nil {
			rcp.SetTransactionIndex(blockTxIndex)
			rcp.SetBlockTransactionIndex(blockTxIndex)
			blockTxIndex++
			allTransactions = append(allTransactions, rawTxs[i])
			allReceipts = append(allReceipts, rcp)
			if i < len(rawScRs) && rawScRs[i] != nil {
				allExecuteSCResults = append(allExecuteSCResults, rawScRs[i])
			}
			if mvmId, ok := rawMvmMap[rawTxs[i].Hash()]; ok {
				allMvmIdMap[rawTxs[i].Hash()] = mvmId
			}
		}
	}

	// Apply code/storageRoot for deployed contracts.
	if mergedRs := mergeMvmResults(allExecuteSCResults, leaderAddr); mergedRs != nil {
		applyCodeAndStorageRootOnly(chainState, mergedRs)
	}

	return allTransactions, allReceipts, allExecuteSCResults, allMvmIdMap
}
