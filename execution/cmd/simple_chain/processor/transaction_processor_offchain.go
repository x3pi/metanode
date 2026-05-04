package processor

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/vm_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

func (v *TxVirtualExecutor) ProcessTransactionOffChain(tx types.Transaction) (types.ExecuteSCResult, error) {
	if tx.IsCallContract() || tx.IsDeployContract() {
		exRs, err := v.executeTransactionOffChain(tx)
		if err != nil {
			logger.Error("Error executing transaction off-chain: %v", err)
			return nil, err
		}
		if exRs == nil {
			logger.Error("ExecuteSCResult is nil")
			return nil, fmt.Errorf("return null")
		}
		return exRs, nil
	}
	return nil, nil
}

// ProcessTransactionOffChainWithState supports executing off-chain transaction against a specific state root and block header
func (v *TxVirtualExecutor) ProcessTransactionOffChainWithState(tx types.Transaction, stateRoot common.Hash, header types.BlockHeader) (types.ExecuteSCResult, error) {
	if tx.IsCallContract() || tx.IsDeployContract() {
		exRs, err := v.executeTransactionOffChainWithState(tx, stateRoot, header)
		if err != nil {
			logger.Error("Error executing transaction off-chain with state: %v", err)
			return nil, err
		}
		if exRs == nil {
			logger.Error("ExecuteSCResult is nil")
			return nil, fmt.Errorf("return null")
		}
		return exRs, nil
	}
	return nil, nil
}

// executeTransactionOffChainWithState executes logic against specific state
func (v *TxVirtualExecutor) executeTransactionOffChainWithState(
	executeTransaction types.Transaction,
	stateRoot common.Hash,
	header types.BlockHeader,
) (types.ExecuteSCResult, error) {

	v.offChainExecutionLimiter <- struct{}{}
	defer func() {
		<-v.offChainExecutionLimiter
	}()

	if executeTransaction.ToAddress() == mt_common.VALIDATOR_CONTRACT_ADDRESS {
		blockDatabase := block.NewBlockDatabase(v.storageManager.GetStorageBlock())
		chainStateNew, err := blockchain.NewChainState(v.storageManager, blockDatabase, header, v.chainState.GetConfig(), v.chainState.GetFreeFeeAddress(), "") // Empty backupPath for temporary chain state
		if err != nil {
			return nil, err
		}

		if stakeDB := chainStateNew.GetStakeStateDB(); stakeDB != nil && v.chainState.GetConfig().EnableHistoricalState {
			changelogDB := v.chainState.GetStakeChangelogDB()
			if changelogDB != nil {
				stakeDB.SetHistoricalContext(changelogDB, header.BlockNumber())
			}
		}

		validatorHandler, err := tx_processor.GetValidatorHandler()
		if err != nil {
			return nil, err
		}
		return validatorHandler.HandleOffChainQuery(executeTransaction, chainStateNew)
	}

	if executeTransaction.ToAddress() == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS {
		blockDatabase := block.NewBlockDatabase(v.storageManager.GetStorageBlock())
		chainStateNew, err := blockchain.NewChainState(v.storageManager, blockDatabase, header, v.chainState.GetConfig(), v.chainState.GetFreeFeeAddress(), "")
		if err != nil {
			return nil, err
		}
		ccHandler, err := cross_chain_handler.GetCrossChainHandler()
		if err != nil {
			return nil, err
		}
		return ccHandler.HandleOffChainQuery(executeTransaction, chainStateNew)
	}

	ctx := context.Background()
	transactionHash := executeTransaction.Hash()
	currentTime := time.Now().Unix()
	combinedHash := sha256.Sum256([]byte(fmt.Sprintf("%x%d%d", transactionHash, currentTime, rand.Intn(1000))))

	ethAddressBytes := combinedHash[12:]
	ethAddressBytes[0] = 0xFD // Prevent overlap with real Xapian DB contracts
	mvmId := common.BytesToAddress(ethAddressBytes)

	accountStateTrie, err := trie.NewStateTrie(stateRoot, v.storageManager.GetStorageAccount(), true)
	if err != nil {
		return nil, fmt.Errorf("failed to create account state trie: %v", err)
	}
	accountStateDB := account_state_db.NewAccountStateDB(accountStateTrie, v.storageManager.GetStorageAccount())

	blockDatabase := block.NewBlockDatabase(v.storageManager.GetStorageBlock())
	chainStateNew, err := blockchain.NewChainState(v.storageManager, blockDatabase, header, v.chainState.GetConfig(), v.chainState.GetFreeFeeAddress(), "") // Empty backupPath for temporary chain state
	if err != nil {
		return nil, err
	}

	vmP := vm_processor.NewVmProcessor(chainStateNew, mvmId, false, header.TimeStamp())
	mvmOffChain := mvm.GetOrCreateMVMApi(mvmId, chainStateNew.GetSmartContractDB(), accountStateDB, true)
	logger.Info("Off-chain execution for transaction %s with MVM ID %s", executeTransaction.Hash().Hex(), mvmId.Hex())
	mvmOffChain.SetRelatedAddresses(executeTransaction.RelatedAddresses())
	var mvmResult *mvm.MVMExecuteResult

	if executeTransaction.IsCallContract() {
		mvmResult = mvmOffChain.Call(
			executeTransaction.FromAddress().Bytes(),
			executeTransaction.ToAddress().Bytes(),
			executeTransaction.CallData().Input(),
			executeTransaction.Amount(),
			executeTransaction.MaxGasPrice(),
			executeTransaction.MaxGas(),
			header.TimeStamp(),
			mt_common.OFF_CHAIN_GAS_LIMIT,
			header.TimeStamp(),
			mt_common.MINIMUM_BASE_FEE,
			header.BlockNumber(),
			header.LeaderAddress(),
			mvmId,
			false,
			executeTransaction.Hash().Bytes(),
			executeTransaction.RelatedAddresses(),
			false,
			true,
		)
	}

	if executeTransaction.IsDeployContract() {
		mvmResult = mvmOffChain.Deploy(
			executeTransaction.FromAddress().Bytes(),
			executeTransaction.DeployData().Code(),
			executeTransaction.Amount(),
			executeTransaction.MaxGasPrice(),
			executeTransaction.MaxGas(),
			header.TimeStamp(),
			mt_common.OFF_CHAIN_GAS_LIMIT,
			header.TimeStamp(),
			mt_common.MINIMUM_BASE_FEE,
			header.BlockNumber(),
			header.LeaderAddress(),
			mvmId,
			executeTransaction.Hash().Bytes(),
			false,
			false,
			true,
		)
	}
	logger.Info("MVM execution completed for transaction %v", mvmResult)
	if mvmResult == nil {
		return nil, fmt.Errorf("mvmResult is nil")
	}

	exRsE, err := vmP.MvmResultToExecuteResultOffChain(ctx, executeTransaction, mvmResult)
	mvm.ClearMVMApi(mvmId)
	if err != nil {
		return nil, err
	}
	return exRsE, nil
}

// Nhật
func (v *TxVirtualExecutor) executeTransactionOffChain(
	executeTransaction types.Transaction,
) (types.ExecuteSCResult, error) {

	v.offChainExecutionLimiter <- struct{}{}
	defer func() {
		// Giải phóng slot khi hoàn thành
		<-v.offChainExecutionLimiter
	}()
	// cần code thêm
	if executeTransaction.ToAddress() == mt_common.VALIDATOR_CONTRACT_ADDRESS {
		blockDatabase := block.NewBlockDatabase(v.storageManager.GetStorageBlock())
		headerPtr := v.chainState.GetcurrentBlockHeader()
		if headerPtr == nil {
			logger.Error("CRITICAL: v.chainState.GetcurrentBlockHeader() is nil in executeTransactionOffChain")
			return nil, fmt.Errorf("current block header is nil")
		}
		lastBlockHeader := *headerPtr

		chainStateNew, err := blockchain.NewChainState(v.storageManager, blockDatabase, lastBlockHeader, v.chainState.GetConfig(), v.chainState.GetFreeFeeAddress(), "") // Empty backupPath for temporary chain state

		if err != nil {
			return nil, err
		}
		validatorHandler, err := tx_processor.GetValidatorHandler()
		if err != nil {
			return nil, err
		}
		return validatorHandler.HandleOffChainQuery(executeTransaction, chainStateNew)
	}

	if executeTransaction.ToAddress() == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS {
		blockDatabase := block.NewBlockDatabase(v.storageManager.GetStorageBlock())
		headerPtr := v.chainState.GetcurrentBlockHeader()
		if headerPtr == nil {
			logger.Error("CRITICAL: v.chainState.GetcurrentBlockHeader() is nil in executeTransactionOffChain for cross-chain")
			return nil, fmt.Errorf("current block header is nil")
		}
		lastBlockHeader := *headerPtr

		chainStateNew, err := blockchain.NewChainState(v.storageManager, blockDatabase, lastBlockHeader, v.chainState.GetConfig(), v.chainState.GetFreeFeeAddress(), "")
		if err != nil {
			return nil, err
		}
		ccHandler, err := cross_chain_handler.GetCrossChainHandler()
		if err != nil {
			return nil, err
		}
		return ccHandler.HandleOffChainQuery(executeTransaction, chainStateNew)
	}

	// cần code thêm
	if executeTransaction.ToAddress() == utils.GetAddressSelector(mt_common.IDENTIFIER_STAKE) {
		blockDatabase := block.NewBlockDatabase(v.storageManager.GetStorageBlock())
		lastBlockHeader := *v.chainState.GetcurrentBlockHeader()
		chainStateNew, err := blockchain.NewChainState(v.storageManager, blockDatabase, lastBlockHeader, v.chainState.GetConfig(), v.chainState.GetFreeFeeAddress(), "") // Empty backupPath for temporary chain state
		if err != nil {
			return nil, err
		}
		validatorHandler, err := tx_processor.GetValidatorHandler()
		if err != nil {
			return nil, err
		}
		return validatorHandler.HandleOffChainQuery(executeTransaction, chainStateNew)

	}

	ctx := context.Background()

	transactionHash := executeTransaction.Hash()
	currentTime := time.Now().Unix()
	combinedHash := sha256.Sum256([]byte(fmt.Sprintf("%x%d%d", transactionHash, currentTime, rand.Intn(1000))))

	ethAddressBytes := combinedHash[12:]
	ethAddressBytes[0] = 0xFD // Prevent overlap with real Xapian DB contracts
	mvmId := common.BytesToAddress(ethAddressBytes)
	lastBlockHeader := *v.chainState.GetcurrentBlockHeader()

	vmP := vm_processor.NewVmProcessor(v.chainState, mvmId, false, lastBlockHeader.TimeStamp())
	mvmOffChain := mvm.GetOrCreateMVMApi(mvmId, v.chainState.GetSmartContractDB(), v.chainState.GetAccountStateDB(), true)
	logger.Info("Off-chain execution for transaction %s with MVM ID %s", executeTransaction.Hash().Hex(), mvmId.Hex())
	mvmOffChain.SetRelatedAddresses(executeTransaction.RelatedAddresses())
	var mvmResult *mvm.MVMExecuteResult

	if executeTransaction.IsCallContract() {
		mvmResult = mvmOffChain.Call(
			executeTransaction.FromAddress().Bytes(),
			executeTransaction.ToAddress().Bytes(),
			executeTransaction.CallData().Input(),
			executeTransaction.Amount(),
			executeTransaction.MaxGasPrice(),
			executeTransaction.MaxGas(),
			lastBlockHeader.TimeStamp(),
			mt_common.OFF_CHAIN_GAS_LIMIT,
			lastBlockHeader.TimeStamp(),
			mt_common.MINIMUM_BASE_FEE,
			lastBlockHeader.BlockNumber(),
			lastBlockHeader.LeaderAddress(),
			mvmId,
			false,
			executeTransaction.Hash().Bytes(),
			executeTransaction.RelatedAddresses(),
			false,
			true,
		)
	}
	if executeTransaction.IsDeployContract() {
		mvmResult = mvmOffChain.Deploy(
			executeTransaction.FromAddress().Bytes(),
			executeTransaction.DeployData().Code(),
			executeTransaction.Amount(),
			executeTransaction.MaxGasPrice(),
			executeTransaction.MaxGas(),
			lastBlockHeader.TimeStamp(),
			mt_common.OFF_CHAIN_GAS_LIMIT,
			lastBlockHeader.TimeStamp(),
			mt_common.MINIMUM_BASE_FEE,
			lastBlockHeader.BlockNumber(),
			lastBlockHeader.LeaderAddress(),
			mvmId,
			executeTransaction.Hash().Bytes(),
			false,
			false,
			true,
		)
	}
	logger.Info("MVM execution completed for transaction %v", mvmResult)
	if mvmResult == nil {
		logger.Fatal("mvmResult is null for transaction %s", executeTransaction.Hash().Hex())
	}

	exRsE, err := vmP.MvmResultToExecuteResultOffChain(ctx, executeTransaction, mvmResult)
	mvm.ClearMVMApi(mvmId)
	if err != nil {
		return nil, err
	}

	rcp := receipt.NewReceipt(
		executeTransaction.Hash(),
		executeTransaction.FromAddress(),
		executeTransaction.ToAddress(),
		executeTransaction.Amount(),
		exRsE.ReceiptStatus(),
		exRsE.Return(),
		exRsE.Exception(),
		mt_common.MINIMUM_BASE_FEE,
		exRsE.GasUsed(),
		exRsE.EventLogs(),
		uint64(0),
		common.Hash{},
		0,
	)
	// --- KẾT THÚC KIỂM TRA ---
	logger.Info("Off-chain transaction %v executed, preparing to index", executeTransaction)
	// Nếu không trùng lặp, tiếp tục xử lý giao dịch
	executeTransaction.SetReadOnly(true)
	executeTransaction.SetNonce(storage.GetIncrementingCounter())
	executeTransaction.ClearCacheHash()

	// txHash := executeTransaction.Hash()

	// // --- KIỂM TRA GIAO DỊCH TRÙNG LẶP ---
	// if _, loaded := v.readTxHashes.LoadOrStore(txHash, time.Now()); loaded {
	// 	errMsg := fmt.Sprintf("duplicate read transaction: %s : nonce : %v", txHash.Hex(), executeTransaction.GetNonce())
	// 	logger.Error("Duplicate read transaction detected: %s", errMsg)
	// 	panic("duplicate read transaction")
	// }
	rcp.SetRHash(executeTransaction.RHash())

	if explorerService := v.storageManager.GetExplorerSearchServiceReadOnly(); explorerService != nil {
		if err := explorerService.IndexTransaction(executeTransaction, rcp, lastBlockHeader); err != nil {
			logger.Fatal("Cannot index off-chain transaction %s in block #%d: %v", executeTransaction.Hash().Hex(), lastBlockHeader.BlockNumber(), err)
		}
	}
	logger.Info("Off-chain transaction %v  processed and indexed successfully", executeTransaction)
	return exRsE, nil
}

func (v *TxVirtualExecutor) ProcessTransactionDebug(tx types.Transaction, block types.Block) (types.ExecuteSCResult, error) {
	ctx := context.Background()

	if tx.IsCallContract() || tx.IsDeployContract() {
		vmP := vm_processor.NewVmProcessor(v.chainState, tx.ToAddress(), false, block.Header().TimeStamp())
		exRs, err := vmP.ExecuteTransactionWithMvmIdDebug(ctx, tx, false)
		if err != nil {
			logger.Error("Error executing transaction in debug mode: %v", err)
			return nil, err
		}
		if exRs == nil {
			logger.Error("ExecuteSCResult is nil in debug mode")
			return nil, fmt.Errorf("return null")
		}
		return exRs, nil
	}
	return nil, nil
}
