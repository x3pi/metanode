package tx_processor

import (
	"context"
	"encoding/binary"
	"math/big"
	"sync"
	"sync/atomic"
	"time"
	"runtime"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract_db"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
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
) (
	[]types.Transaction,
	[]types.Receipt,
	[]types.ExecuteSCResult,
	map[common.Hash]common.Address,
) {
	var allTxs []types.Transaction
	for _, group := range groupedGroups {
		for _, item := range group.Items {
			allTxs = append(allTxs, item.Tx)
		}
	}
	if len(allTxs) == 0 {
		return nil, nil, nil, nil
	}

	startPreload := time.Now()
	uniqueAddrMap := make(map[common.Address]struct{}, len(allTxs)*2)
	addrSlice := make([]common.Address, 0, len(allTxs)*2)
	for _, tx := range allTxs {
		fromAddr := tx.FromAddress()
		if _, seen := uniqueAddrMap[fromAddr]; !seen {
			uniqueAddrMap[fromAddr] = struct{}{}
			addrSlice = append(addrSlice, fromAddr)
		}
		if tx.IsCallContract() {
			toAddr := tx.ToAddress()
			if _, seen := uniqueAddrMap[toAddr]; !seen {
				uniqueAddrMap[toAddr] = struct{}{}
				addrSlice = append(addrSlice, toAddr)
			}
		}
	}
	chainState.GetAccountStateDB().PreloadAccounts(addrSlice)
	logger.Debug("⚡ [PERF] Pre-fetched %d unique addresses in %v", len(addrSlice), time.Since(startPreload))

	// -------------------------------------------------------------------------
	// 2. Iterative Parallel Group Block-STM (Union-Find Conflict Resolution)
	// -------------------------------------------------------------------------

	finalResults := make([]groupResultExt, len(allTxs))
	validationCache := account_state_db.NewValidationStateCache(chainState.GetAccountStateDB(), chainState.GetSmartContractDB())

	// txsToExecute contains the indices of all transactions that need to be executed/re-executed
	txsToExecute := make([]int, len(allTxs))
	for i := range allTxs {
		txsToExecute[i] = i
	}

	speculativeResults := make(map[int]groupResultExt)
	isFirstRound := true

	for len(txsToExecute) > 0 {
		var wg sync.WaitGroup
		var resultsMutex sync.Mutex

		numWorkers := runtime.NumCPU()
		if numWorkers > 16 {
			numWorkers = 16
		}

		if isFirstRound {
			// Round 1: Flat parallel execution of all transactions
			var nextIdx uint32
			for w := 0; w < numWorkers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						idx := int(atomic.AddUint32(&nextIdx, 1) - 1)
						if idx >= len(txsToExecute) {
							break
						}
						realIdx := txsToExecute[idx]
						tx := allTxs[realIdx]

						var localTrie p_trie.StateTrie
						baseTrie := validationCache.Trie()
						if _, ok := baseTrie.(*p_trie.FlatStateTrie); ok {
							localTrie = baseTrie
						} else if _, ok := baseTrie.(*p_trie.NomtStateTrie); ok {
							localTrie = baseTrie
						} else {
							localTrie = baseTrie.Copy()
						}
						localAccountDB := account_state_db.NewAccountStateDB(localTrie, chainState.GetStorageManager().GetStorageAccount())
						localSmartContractDB := smart_contract_db.NewSmartContractDB(
							chainState.GetStorageManager().GetStorageCode(),
							chainState.GetStorageManager().GetStorageSmartContract(),
							localAccountDB,
						)

						// In Round 1, execute from the base state without previous writes
						var ethAddressBytes [20]byte
						ethAddressBytes[0] = 0xFE
						copy(ethAddressBytes[1:16], lastBlockHeader.LastBlockHash().Bytes()[:15])
						binary.BigEndian.PutUint32(ethAddressBytes[16:], uint32(realIdx))
						mvmId := common.Address(ethAddressBytes)

						res := processSingleGroup(
							ctx, chainState, localAccountDB, localSmartContractDB,
							[]grouptxns.Item{{Tx: tx}}, mvmId, lastBlockHeader, enableTrace, isCache, blockTime, leaderAddr,
						)
						res.readAccounts = localAccountDB.GetLoadedAccounts()
						res.readStorage = localSmartContractDB.GetReadStorageKeys()

						resultsMutex.Lock()
						speculativeResults[realIdx] = res
						resultsMutex.Unlock()

						localSmartContractDB.Discard()
						localAccountDB.Discard()
					}
				}()
			}
			wg.Wait()
			isFirstRound = false

		} else {
			// Subsequent Rounds: Group-level parallel execution of conflicting transactions using Union-Find
			numConflicting := len(txsToExecute)
			uf := grouptxns.NewUnionFind(numConflicting)

			txIdxMap := make(map[int]int, numConflicting) // realIdx -> index in txsToExecute
			for i, realIdx := range txsToExecute {
				txIdxMap[realIdx] = i
			}

			addressToIndices := make(map[common.Address][]int)
			registerAccess := func(addr common.Address, realIdx int) {
				if idxInConflicting, ok := txIdxMap[realIdx]; ok {
					addressToIndices[addr] = append(addressToIndices[addr], idxInConflicting)
				}
			}

			for _, realIdx := range txsToExecute {
				res := speculativeResults[realIdx]
				// Read accounts
				for addr := range res.readAccounts {
					registerAccess(addr, realIdx)
				}
				// Written accounts
				for _, acc := range res.DirtyAccounts {
					registerAccess(acc.Address(), realIdx)
				}
				// Read storage
				for addr := range res.readStorage {
					registerAccess(addr, realIdx)
				}
				// Written storage
				if res.exPtr != nil {
					for _, ex := range *res.exPtr {
						if ex != nil && ex.MapStorageChange() != nil {
							for addrStr := range ex.MapStorageChange() {
								registerAccess(common.HexToAddress(addrStr), realIdx)
							}
						}
					}
				}
			}

			// Union transactions accessing same addresses
			for _, indices := range addressToIndices {
				for i := 1; i < len(indices); i++ {
					uf.Union(indices[0], indices[i])
				}
			}

			// Collect transaction indices into groups
			rootToItems := make(map[int][]int)
			for i, realIdx := range txsToExecute {
				root := uf.Find(i)
				rootToItems[root] = append(rootToItems[root], realIdx)
			}

			var executionGroups [][]int
			for _, groupRealIndices := range rootToItems {
				executionGroups = append(executionGroups, groupRealIndices)
			}

			// Execute groups in parallel, sequentially within each group
			var nextGroupIdx uint32
			speculativeGroupResults := make(map[int]map[int]groupResultExt) // groupIdx -> realIdx -> result

			for w := 0; w < numWorkers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						gIdx := int(atomic.AddUint32(&nextGroupIdx, 1) - 1)
						if gIdx >= len(executionGroups) {
							break
						}
						groupRealIndices := executionGroups[gIdx]

						var localTrie p_trie.StateTrie
						baseTrie := validationCache.Trie()
						if _, ok := baseTrie.(*p_trie.FlatStateTrie); ok {
							localTrie = baseTrie
						} else if _, ok := baseTrie.(*p_trie.NomtStateTrie); ok {
							localTrie = baseTrie
						} else {
							localTrie = baseTrie.Copy()
						}
						localAccountDB := account_state_db.NewAccountStateDB(localTrie, chainState.GetStorageManager().GetStorageAccount())
						localSmartContractDB := smart_contract_db.NewSmartContractDB(
							chainState.GetStorageManager().GetStorageCode(),
							chainState.GetStorageManager().GetStorageSmartContract(),
							localAccountDB,
						)

						// Apply all accepted writes so far to the local execution DB
						validationCache.ApplyAcceptedWritesTo(localAccountDB, localSmartContractDB)

						groupResults := make(map[int]groupResultExt)

						// Execute sequentially within this group
						for _, realIdx := range groupRealIndices {
							tx := allTxs[realIdx]

							var ethAddressBytes [20]byte
							ethAddressBytes[0] = 0xFE
							copy(ethAddressBytes[1:16], lastBlockHeader.LastBlockHash().Bytes()[:15])
							binary.BigEndian.PutUint32(ethAddressBytes[16:], uint32(realIdx))
							mvmId := common.Address(ethAddressBytes)

							res := processSingleGroup(
								ctx, chainState, localAccountDB, localSmartContractDB,
								[]grouptxns.Item{{Tx: tx}}, mvmId, lastBlockHeader, enableTrace, isCache, blockTime, leaderAddr,
							)
							res.readAccounts = localAccountDB.GetLoadedAccounts()
							res.readStorage = localSmartContractDB.GetReadStorageKeys()

							groupResults[realIdx] = res
						}

						resultsMutex.Lock()
						speculativeGroupResults[gIdx] = groupResults
						resultsMutex.Unlock()

						localSmartContractDB.Discard()
						localAccountDB.Discard()
					}
				}()
			}
			wg.Wait()

			// Merge speculativeGroupResults back to speculativeResults
			for _, groupResults := range speculativeGroupResults {
				for realIdx, res := range groupResults {
					speculativeResults[realIdx] = res
				}
			}
		}

		// --- Sequential Validation & Conflict Check ---
		var nextRoundTxs []int
		for _, realIdx := range txsToExecute {
			res := speculativeResults[realIdx]

			hasConflict := validationCache.CheckConflict(res.readAccounts, res.readStorage)
			if hasConflict {
				logger.Info("🔄 [BLOCK-STM] Conflict detected for TX %d, re-queueing", realIdx)
				nextRoundTxs = append(nextRoundTxs, realIdx)
			} else {
				// Apply writes to the validation cache overlay
				storageWrites := make(map[string]map[string][]byte)
				if res.exPtr != nil {
					for _, ex := range *res.exPtr {
						if ex != nil && ex.MapStorageChange() != nil {
							for k, v := range ex.MapStorageChange() {
								storageWrites[k] = v
							}
						}
					}
				}

				dirtyAccountsMap := make(map[common.Address]types.AccountState)
				for _, acc := range res.DirtyAccounts {
					dirtyAccountsMap[acc.Address()] = acc
				}

				validationCache.ApplyWrites(dirtyAccountsMap, storageWrites)
				finalResults[realIdx] = res
			}
		}

		// Set up for next round
		txsToExecute = nextRoundTxs
	}

	validationCache.FlushToGlobal()

	var totalBlockGasFee *big.Int = big.NewInt(0)
	for _, gRs := range finalResults {
		if gRs.TotalGasFee != nil && gRs.TotalGasFee.Sign() > 0 {
			totalBlockGasFee.Add(totalBlockGasFee, gRs.TotalGasFee)
		}
	}
	if totalBlockGasFee.Sign() > 0 && leaderAddr != (common.Address{}) {
		chainState.GetAccountStateDB().AddPendingBalance(leaderAddr, totalBlockGasFee)
	}

	allTransactions := make([]types.Transaction, 0, len(allTxs))
	allReceipts := make([]types.Receipt, 0, len(allTxs))
	allExecuteSCResults := make([]types.ExecuteSCResult, 0, len(allTxs))
	allMvmIdMap := make(map[common.Hash]common.Address, len(allTxs))

	blockTxIndex := uint64(0)
	for _, gRs := range finalResults {
		if gRs.rcpPtr != nil {
			for i, rcp := range *gRs.rcpPtr {
				rcp.SetTransactionIndex(0) // TODO: actual index
				rcp.SetBlockTransactionIndex(blockTxIndex)
				blockTxIndex++
				(*gRs.rcpPtr)[i] = rcp
			}
		}
		if gRs.txPtr != nil {
			allTransactions = append(allTransactions, *gRs.txPtr...)
		}
		if gRs.rcpPtr != nil {
			allReceipts = append(allReceipts, *gRs.rcpPtr...)
		}
		if gRs.exPtr != nil {
			allExecuteSCResults = append(allExecuteSCResults, *gRs.exPtr...)
		}
		if gRs.mvmPtr != nil {
			for k, v := range *gRs.mvmPtr {
				allMvmIdMap[k] = v
			}
		}
	}

	return allTransactions, allReceipts, allExecuteSCResults, allMvmIdMap
}
