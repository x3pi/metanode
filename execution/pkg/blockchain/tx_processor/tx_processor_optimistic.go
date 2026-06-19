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
	// 2. Iterative Block-STM Worker Pool
	// -------------------------------------------------------------------------

	finalResults := make([]groupResultExt, len(allTxs))
	failedSendersMap := make(map[common.Address]bool)

	// txsToExecute contains the indices of the transactions that need to be executed in this round.
	txsToExecute := make([]int, len(allTxs))
	for i := range allTxs {
		txsToExecute[i] = i
	}

	validationCache := account_state_db.NewValidationStateCache(chainState.GetAccountStateDB(), chainState.GetSmartContractDB())

	for len(txsToExecute) > 0 {
		var nextIdx uint32
		var wg sync.WaitGroup

		speculativeResults := make(map[int]groupResultExt)
		var resultsMutex sync.Mutex

		numWorkers := runtime.NumCPU()
		if numWorkers > 16 {
			numWorkers = 16
		}

		// Phase 2.1: Execute all txs in txsToExecute concurrently
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

					localTrie := validationCache.Trie().Copy()
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

					// Not sharing failure map deterministically during concurrent phase, 
					// will process sequentially in Phase 2.2
					
					localSmartContractDB.Discard()
					localAccountDB.Discard()
				}
			}()
		}
		wg.Wait()

		// Phase 2.2: Validate sequentially in the original order of txsToExecute
		var nextRoundTxs []int
		for _, realIdx := range txsToExecute {
			tx := allTxs[realIdx]
			res := speculativeResults[realIdx]

			senderFailed := failedSendersMap[tx.FromAddress()]
			if senderFailed {
				logger.Warn("❌ [BLOCK-STM] TX %s skipped due to previous sender error", tx.Hash().Hex())
				finalResults[realIdx] = groupResultExt{
					txPtr:   &[]types.Transaction{tx},
					mvmPtr:  &map[common.Hash]common.Address{},
					rcpPtr:  &[]types.Receipt{},
					exPtr:   &[]types.ExecuteSCResult{},
				}
				continue
			}

			hasConflict := validationCache.CheckConflict(res.readAccounts, res.readStorage)
			if hasConflict {
				logger.Info("🔄 [BLOCK-STM] Conflict detected for TX %s, re-queueing", tx.Hash().Hex())
				nextRoundTxs = append(nextRoundTxs, realIdx)
			} else {
				// Record potential sender failure
				if res.rcpPtr != nil && len(*res.rcpPtr) > 0 {
					for _, rcp := range *res.rcpPtr {
						if rcp.Status() == 0 { // TX failed
							failedSendersMap[tx.FromAddress()] = true
						}
					}
				}

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
				finalResults[realIdx] = res
			}
		}

		// Prepare for the next round with only conflicting transactions
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
		for i, rcp := range *gRs.rcpPtr {
			rcp.SetTransactionIndex(0) // TODO: actual index
			rcp.SetBlockTransactionIndex(blockTxIndex)
			blockTxIndex++
			(*gRs.rcpPtr)[i] = rcp
		}
		allTransactions = append(allTransactions, *gRs.txPtr...)
		allReceipts = append(allReceipts, *gRs.rcpPtr...)
		allExecuteSCResults = append(allExecuteSCResults, *gRs.exPtr...)
		for k, v := range *gRs.mvmPtr {
			allMvmIdMap[k] = v
		}
	}

	return allTransactions, allReceipts, allExecuteSCResults, allMvmIdMap
}
