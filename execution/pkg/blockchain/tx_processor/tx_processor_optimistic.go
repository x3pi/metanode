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
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
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
		if numWorkers > 16 {
			numWorkers = 16
		}

		if isFirstRound {
			// Round 1: Flat parallel execution of all static groups
			var nextIdx uint32
			for _, realIdx := range groupsToExecute {
				executionGroups = append(executionGroups, []int{realIdx})
			}
			for w := 0; w < numWorkers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					
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

					for {
						idx := int(atomic.AddUint32(&nextIdx, 1) - 1)
						if idx >= len(groupsToExecute) {
							break
						}
						realIdx := groupsToExecute[idx]
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
						localAccountDB.Discard()
					}
				}()
			}
			wg.Wait()
			isFirstRound = false

		} else {
			// Subsequent Rounds: Group-level parallel execution of conflicting groups using Union-Find
			numConflicting := len(groupsToExecute)
			uf := grouptxns.NewUnionFind(numConflicting)

			groupIdxMap := make(map[int]int, numConflicting) // realIdx -> index in groupsToExecute
			for i, realIdx := range groupsToExecute {
				groupIdxMap[realIdx] = i
			}

			readToIndices := make(map[string][]int)
			writeToIndices := make(map[string][]int)

			conflictFreeAddr := common.HexToAddress("0x00000000000000000000000000000000D844bb55")
			
			registerAccess := func(addr common.Address, isWrite bool, realIdx int) {
				if addr == conflictFreeAddr {
					return
				}
				key := addr.String()
				idx := groupIdxMap[realIdx]
				if isWrite {
					writeToIndices[key] = append(writeToIndices[key], idx)
				} else {
					readToIndices[key] = append(readToIndices[key], idx)
				}
			}
			
			registerStorageAccess := func(addr common.Address, kStr string, isWrite bool, realIdx int) {
				if addr == conflictFreeAddr {
					return
				}
				key := addr.String() + kStr
				idx := groupIdxMap[realIdx]
				if isWrite {
					writeToIndices[key] = append(writeToIndices[key], idx)
				} else {
					readToIndices[key] = append(readToIndices[key], idx)
				}
			}

			for _, realIdx := range groupsToExecute {
				res := speculativeResults[realIdx]
				// Read accounts
				for addr := range res.readAccounts {
					registerAccess(addr, false, realIdx)
				}
				// Written accounts
				for _, acc := range res.DirtyAccounts {
					registerAccess(acc.Address(), true, realIdx)
				}
				// Read storage
				for addr, keys := range res.readStorage {
					for _, kStr := range keys {
						registerStorageAccess(addr, kStr, false, realIdx)
					}
				}
				// Written storage
				if res.exPtr != nil {
					for _, ex := range *res.exPtr {
						if ex != nil && ex.MapStorageChange() != nil {
							for addrStr, kvs := range ex.MapStorageChange() {
								addr := common.HexToAddress(addrStr)
								for kStr := range kvs {
									registerStorageAccess(addr, kStr, true, realIdx)
								}
							}
						}
					}
				}
			}

			// Union groups accessing same addresses (Write-Write and Read-Write ONLY)
			for key, wIndices := range writeToIndices {
				rIndices := readToIndices[key]
				
				// All writers of this key conflict with each other
				for i := 1; i < len(wIndices); i++ {
					uf.Union(wIndices[0], wIndices[i])
				}
				// All readers of this key conflict with the writers
				if len(wIndices) > 0 {
					for _, rIdx := range rIndices {
						uf.Union(wIndices[0], rIdx)
					}
				}
			}

			// Collect group indices into execution groups
			rootToItems := make(map[int][]int)
			for i, realIdx := range groupsToExecute {
				root := uf.Find(i)
				rootToItems[root] = append(rootToItems[root], realIdx)
			}

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

					for {
						gIdx := int(atomic.AddUint32(&nextGroupIdx, 1) - 1)
						if gIdx >= len(executionGroups) {
							break
						}
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

	return allTransactions, allReceipts, allExecuteSCResults, allMvmIdMap
}
