package tx_processor

import (
	"context"
	"encoding/binary"
	"math/big"
	"runtime"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
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
	var totalTxs int
	for _, group := range groupedGroups {
		totalTxs += len(group.Items)
	}
	if totalTxs == 0 {
		return nil, nil, nil, nil
	}

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

	// -------------------------------------------------------------------------
	// 2. Iterative Parallel Group Block-STM (Union-Find Conflict Resolution)
	// -------------------------------------------------------------------------

	finalResults := make([]groupResultExt, len(groupedGroups))
	validationCache := account_state_db.NewValidationStateCache(chainState.GetAccountStateDB(), chainState.GetSmartContractDB())

	// groupsToExecute contains the indices of all groups that need to be executed/re-executed
	groupsToExecute := make([]int, len(groupedGroups))
	for i := range groupedGroups {
		groupsToExecute[i] = i
	}

	speculativeResults := make(map[int]groupResultExt)
	isFirstRound := true
	numRounds := 0
	conflictsInBlock := 0

	for len(groupsToExecute) > 0 {
		numRounds++
		var wg sync.WaitGroup
		var resultsMutex sync.Mutex
		var executionGroups [][]int

		numWorkers := runtime.NumCPU()

		if isFirstRound {
			// Round 1: Flat parallel execution of all static groups
			for _, realIdx := range groupsToExecute {
				executionGroups = append(executionGroups, []int{realIdx})
			}
			
			jobs := make(chan int, len(groupsToExecute))
			for _, realIdx := range groupsToExecute {
				jobs <- realIdx
			}
			close(jobs)

			for w := 0; w < numWorkers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					
					for realIdx := range jobs {
						select {
						case <-ctx.Done():
							return // Global abort
						default:
						}

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

						group := groupedGroups[realIdx]

						// In Round 1, execute from the base state without previous writes
						var ethAddressBytes [20]byte
						ethAddressBytes[0] = 0xFE
						copy(ethAddressBytes[1:16], lastBlockHeader.LastBlockHash().Bytes()[:15])
						binary.BigEndian.PutUint32(ethAddressBytes[16:], uint32(realIdx))
						mvmId := common.Address(ethAddressBytes)

						res := processSingleGroup(
							ctx, chainState, localAccountDB, localSmartContractDB,
							group.Items, mvmId, lastBlockHeader, enableTrace, isCache, blockTime, leaderAddr,
						)
						res.readAccounts = localAccountDB.GetLoadedAccounts()
						res.readStorage = localSmartContractDB.GetReadStorageKeys()

						resultsMutex.Lock()
						speculativeResults[realIdx] = res
						resultsMutex.Unlock()

						localSmartContractDB.Discard()
					}
				}()
			}
			wg.Wait()
			isFirstRound = false

		} else {
			// Subsequent Rounds: Group-level parallel execution of conflicting groups using Union-Find
			numConflicting := len(groupsToExecute)
			uf := grouptxns.NewUnionFind(numConflicting)

			cd := NewConflictDetector(groupsToExecute)

			for _, realIdx := range groupsToExecute {
				res := speculativeResults[realIdx]
				// Read accounts
				for addr := range res.readAccounts {
					cd.RegisterAccess(addr, false, realIdx)
				}
				// Written accounts
				if res.exPtr != nil {
					for _, ex := range *res.exPtr {
						if ex != nil {
							if ex.MapAddBalance() != nil {
								for addrStr := range ex.MapAddBalance() { cd.RegisterAccess(common.HexToAddress(addrStr), true, realIdx) }
							}
							if ex.MapSubBalance() != nil {
								for addrStr := range ex.MapSubBalance() { cd.RegisterAccess(common.HexToAddress(addrStr), true, realIdx) }
							}
							if ex.MapNonce() != nil {
								for addrStr := range ex.MapNonce() { cd.RegisterAccess(common.HexToAddress(addrStr), true, realIdx) }
							}
							if ex.MapPublicKeyBls() != nil {
								for addrStr := range ex.MapPublicKeyBls() { cd.RegisterAccess(common.HexToAddress(addrStr), true, realIdx) }
							}
							if ex.MapAccountType() != nil {
								for addrStr := range ex.MapAccountType() { cd.RegisterAccess(common.HexToAddress(addrStr), true, realIdx) }
							}
						}
					}
				}
				// Read storage
				for addr, keys := range res.readStorage {
					for _, kStr := range keys {
						cd.RegisterStorageAccess(addr, kStr, false, realIdx)
					}
				}
				// Written storage
				if res.exPtr != nil {
					for _, ex := range *res.exPtr {
						if ex != nil && ex.MapStorageChange() != nil {
							for addrStr, kvs := range ex.MapStorageChange() {
								addr := common.HexToAddress(addrStr)
								for kStr := range kvs {
									cd.RegisterStorageAccess(addr, kStr, true, realIdx)
								}
							}
						}
					}
				}
			}

			executionGroups = cd.BuildExecutionGroups(groupsToExecute, uf)

			// Execute groups in parallel, sequentially within each group
			speculativeGroupResults := make(map[int]map[int]groupResultExt) // groupIdx -> realIdx -> result

			groupJobs := make(chan int, len(executionGroups))
			for gIdx := range executionGroups {
				groupJobs <- gIdx
			}
			close(groupJobs)

			for w := 0; w < numWorkers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()

					for gIdx := range groupJobs {
						select {
						case <-ctx.Done():
							return // Global abort
						default:
						}

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

						groupRealIndices := executionGroups[gIdx]

						// Inject only the targeted accounts and storage slots that were read by these groups in the previous round
						for _, realIdx := range groupRealIndices {
							prevRes := speculativeResults[realIdx]
							validationCache.InjectTargetedAcceptedWrites(localAccountDB, localSmartContractDB, prevRes.readAccounts, prevRes.readStorage)
						}

						groupResults := make(map[int]groupResultExt)

						// Execute sequentially within this meta-group
						for _, realIdx := range groupRealIndices {
							group := groupedGroups[realIdx]

							var ethAddressBytes [20]byte
							ethAddressBytes[0] = 0xFE
							copy(ethAddressBytes[1:16], lastBlockHeader.LastBlockHash().Bytes()[:15])
							binary.BigEndian.PutUint32(ethAddressBytes[16:], uint32(realIdx))
							mvmId := common.Address(ethAddressBytes)

							// 🔒 FIX STALE C++ CACHE: Clear the old MVMApi instance from Phase 1.
							// Without this, the re-execution uses stale cached state, leading to non-determinism,
							// forks, and infinite EVM loops that cause 27GB+ memory OOM crashes.
							mvm.ClearMVMApi(mvmId)

							res := processSingleGroup(
								ctx, chainState, localAccountDB, localSmartContractDB,
								group.Items, mvmId, lastBlockHeader, enableTrace, isCache, blockTime, leaderAddr,
							)
							res.readAccounts = localAccountDB.GetLoadedAccounts()
							res.readStorage = localSmartContractDB.GetReadStorageKeys()

							groupResults[realIdx] = res
						}

						resultsMutex.Lock()
						speculativeGroupResults[gIdx] = groupResults
						resultsMutex.Unlock()

						localSmartContractDB.Discard()
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
		var nextRoundGroups []int
		currentPassCache := account_state_db.NewValidationStateCache(nil, nil)
		
		// Validate meta-groups as atomic units
		for _, groupRealIndices := range executionGroups {
			hasConflict := false
			for _, realIdx := range groupRealIndices {
				res := speculativeResults[realIdx]
				if currentPassCache.CheckConflict(res.readAccounts, res.readStorage) {
					hasConflict = true
					break
				}
			}
			
			if hasConflict {
				for _, realIdx := range groupRealIndices {
					logger.Info("🔄 [BLOCK-STM] Conflict detected for GROUP %d, re-queueing", realIdx)
					conflictsInBlock++
					nextRoundGroups = append(nextRoundGroups, realIdx)
				}
				// -------------------------------------------------------------------------
				// PIPELINED EARLY ABORT SIGNAL
				// -------------------------------------------------------------------------
				// If a conflict is detected and the block size is large, we could signal 
				// dependent groups to abort early. For now, since validation is fast, 
				// we just enqueue them for the next round.
			} else {
				for _, realIdx := range groupRealIndices {
					res := speculativeResults[realIdx]
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
					currentPassCache.ApplyWrites(dirtyAccountsMap, storageWrites)
					finalResults[realIdx] = res
				}
			}
		}

		// Set up for next round
		groupsToExecute = nextRoundGroups
	}

	metrics.BlockStmRounds.Observe(float64(numRounds))
	if conflictsInBlock > 0 {
		metrics.BlockStmConflictsTotal.Add(float64(conflictsInBlock))
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

	allTransactions := make([]types.Transaction, 0, totalTxs)
	allReceipts := make([]types.Receipt, 0, totalTxs)
	allExecuteSCResults := make([]types.ExecuteSCResult, 0, totalTxs)
	allMvmIdMap := make(map[common.Hash]common.Address, totalTxs)

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

	// ═══════════════════════════════════════════════════════════════
	// MERGE & APPLY GLOBALLY
	// ═══════════════════════════════════════════════════════════════
	mergedRs := mergeMvmResults(allExecuteSCResults, leaderAddr)
	err := applyMergedExecuteResult(chainState, mergedRs)
	if err != nil {
		logger.Error("Failed to apply merged ExecuteSCResult to global DB: %v", err)
	}

	return allTransactions, allReceipts, allExecuteSCResults, allMvmIdMap
}
