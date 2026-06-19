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
	// 2. Iterative Block-STM Worker Pool (Group-Level)
	// -------------------------------------------------------------------------

	finalGroupResults := make([]groupResultExt, len(groupedGroups))
	groupsToExecute := make([]int, len(groupedGroups))
	for i := range groupedGroups {
		groupsToExecute[i] = i
	}

	validationCache := account_state_db.NewValidationStateCache(chainState.GetAccountStateDB(), chainState.GetSmartContractDB())

	for len(groupsToExecute) > 0 {
		var nextIdx uint32
		var wg sync.WaitGroup

		speculativeGroupResults := make(map[int]groupResultExt)
		var resultsMutex sync.Mutex

		numWorkers := runtime.NumCPU()
		if numWorkers > 16 {
			numWorkers = 16
		}

		// Phase 2.1: Execute all groups in groupsToExecute concurrently
		for w := 0; w < numWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					idx := int(atomic.AddUint32(&nextIdx, 1) - 1)
					if idx >= len(groupsToExecute) {
						break
					}
					realGroupIdx := groupsToExecute[idx]
					group := &groupedGroups[realGroupIdx]

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
					validationCache.ApplyAcceptedWritesTo(localAccountDB, localSmartContractDB)

					var ethAddressBytes [20]byte
					ethAddressBytes[0] = 0xFE
					copy(ethAddressBytes[1:16], lastBlockHeader.LastBlockHash().Bytes()[:15])
					binary.BigEndian.PutUint32(ethAddressBytes[16:], uint32(realGroupIdx))
					mvmId := common.Address(ethAddressBytes)

					res := processSingleGroup(
						ctx, chainState, localAccountDB, localSmartContractDB,
						group.Items, mvmId, lastBlockHeader, enableTrace, isCache, blockTime, leaderAddr,
					)
					res.readAccounts = localAccountDB.GetLoadedAccounts()
					res.readStorage = localSmartContractDB.GetReadStorageKeys()

					resultsMutex.Lock()
					speculativeGroupResults[realGroupIdx] = res
					resultsMutex.Unlock()

					localSmartContractDB.Discard()
					localAccountDB.Discard()
				}
			}()
		}
		wg.Wait()

		// Phase 2.2: Validate sequentially in the original order of groupsToExecute
		var nextRoundGroups []int
		for _, realGroupIdx := range groupsToExecute {
			res := speculativeGroupResults[realGroupIdx]

			hasConflict := validationCache.CheckConflict(res.readAccounts, res.readStorage)
			if hasConflict {
				logger.Info("🔄 [BLOCK-STM] Conflict detected for Group %d, re-queueing", realGroupIdx)
				nextRoundGroups = append(nextRoundGroups, realGroupIdx)
			} else {
				// Apply speculative writes to the sequential overlay
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
				finalGroupResults[realGroupIdx] = res
			}
		}

		// Prepare for the next round with only conflicting groups
		groupsToExecute = nextRoundGroups
	}

	validationCache.FlushToGlobal()

	var totalBlockGasFee *big.Int = big.NewInt(0)
	for _, gRs := range finalGroupResults {
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
	for _, gRs := range finalGroupResults {
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
