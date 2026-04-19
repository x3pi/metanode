package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"

	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

func (api *MetaAPI) Call(ctx context.Context, input hexutil.Bytes, blockNrOrHash *rpc.BlockNumberOrHash) (hexutil.Bytes, error) {
	// Chuyển yêu cầu đến hàm xử lý song song
	resultChan := make(chan CallResult, 1) // Channel để nhận kết quả

	// Default to latest if nil
	if blockNrOrHash == nil {
		defaultBlock := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)
		blockNrOrHash = &defaultBlock
	}

	go api.processCallRequest(ctx, input, *blockNrOrHash, resultChan)

	// Chờ kết quả từ goroutine
	result := <-resultChan
	if result.Rs != nil && result.Rs.Exception() == mt_proto.EXCEPTION_ERR_EXECUTION_REVERTED {
		logger.Info(result)
		return nil, newRevertError(result.Result)
	}
	return result.Result, result.Error
}

// CallResult struct to hold the result and error
type CallResult struct {
	Result hexutil.Bytes
	Error  error
	Rs     mt_types.ExecuteSCResult
}

// processCallRequest handles the call request concurrently
func (api *MetaAPI) processCallRequest(ctx context.Context, input hexutil.Bytes, blockNrOrHash rpc.BlockNumberOrHash, resultChan chan CallResult) {
	defer close(resultChan) // Đóng channel khi hoàn thành

	txM := &transaction.Transaction{}
	err := txM.Unmarshal(input)
	if err != nil {
		logger.Warn("Error Unmarshal input:", err)
		resultChan <- CallResult{Result: common.FromHex("0x00"), Error: err}
		return
	}
	if txM.GetNonce() == 0 {
		txM.SetNonce(1)
	}

	var rs mt_types.ExecuteSCResult

	// Decide if we can use the fast in-memory current state
	isLatest := false
	if blockNrOrHash.BlockNumber != nil {
		bn := *blockNrOrHash.BlockNumber
		if bn == rpc.LatestBlockNumber || bn == rpc.PendingBlockNumber {
			isLatest = true
		}
	} else if blockNrOrHash.BlockHash == nil {
		isLatest = true // Default is latest
	}

	if isLatest {
		// Fast path: Use in-memory state (avoids missing trie nodes during PebbleDB async flush)
		rs, err = api.App.transactionProcessor.ProcessTransactionOffChain(txM)
	} else {
		// Slow path: Load specific historical block and state root
		currentBlock := api.App.blockProcessor.GetLastBlock()
		header := currentBlock.Header() // Default to latest

		if blockNrOrHash.BlockNumber != nil && *blockNrOrHash.BlockNumber >= 0 {
			bn := *blockNrOrHash.BlockNumber
			hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(uint64(bn))
			if ok {
				loadedBlock, errLoad := api.App.chainState.GetBlockDatabase().GetBlockByHash(hash)
				if errLoad == nil {
					header = loadedBlock.Header()
				}
			}
		} else if blockNrOrHash.BlockHash != nil {
			loadedBlock, errLoad := api.App.chainState.GetBlockDatabase().GetBlockByHash(*blockNrOrHash.BlockHash)
			if errLoad == nil {
				header = loadedBlock.Header()
			}
		}

		stateRoot := header.AccountStatesRoot()
		rs, err = api.App.transactionProcessor.ProcessTransactionOffChainWithState(txM, stateRoot, header)
	}

	if err != nil {
		logger.Warn("Error processing transaction:", err)
		resultChan <- CallResult{Result: common.FromHex("0x00"), Error: err}
		return
	}
	// Trả kết quả khi thành công
	// Kiểm tra xem rs có phải là nil không trước khi gọi Return()
	if rs == nil {
		logger.Warn("ExecuteSCResult is nil")
		resultChan <- CallResult{Result: common.FromHex("0x00"), Error: errors.New("ExecuteSCResult is nil, cannot process call")}
		return
	}

	// Trả kết quả khi thành công
	resultChan <- CallResult{Result: rs.Return(), Error: nil, Rs: rs}
}

func (api *MetaAPI) GetBalance(ctx context.Context, address common.Address, blockNrOrHash rpc.BlockNumberOrHash) (*hexutil.Big, error) {
	as, err := api.resolveAccountState(ctx, address, blockNrOrHash)
	zero := new(big.Int)
	vl := hexutil.Big(*zero)
	if err != nil || as == nil {
		return &vl, nil
	}

	balance := new(big.Int)
	balance.Add(as.Balance(), as.PendingBalance())
	hexBalance := hexutil.Big(*balance)
	return &hexBalance, nil
}

// GetCode returns the code stored at the given address in the state for the given block number.
func (api *MetaAPI) GetCode(ctx context.Context, address common.Address, blockNrOrHash rpc.BlockNumberOrHash) (hexutil.Bytes, error) {
	as, err := api.resolveAccountState(ctx, address, blockNrOrHash)
	if err != nil || as == nil {
		return nil, err
	}
	sc := as.SmartContractState()
	if sc == nil {
		return nil, fmt.Errorf("smartContractState is nil")
	}
	codeHash := as.SmartContractState().CodeHash()
	code := api.App.chainState.GetSmartContractDB().GetCodeByCodeHash(address, codeHash)
	return code, nil
}

// GetStorageAt returns the storage from the state at the given address, key and
// block number. The rpc.LatestBlockNumber and rpc.PendingBlockNumber meta block
// numbers are also allowed.
func (api *MetaAPI) GetStorageAt(ctx context.Context, address common.Address, hexKey string, blockNrOrHash rpc.BlockNumberOrHash) (hexutil.Bytes, error) {
	as, err := api.resolveAccountState(ctx, address, blockNrOrHash)
	if err != nil || as == nil {
		return nil, err
	}

	asSc := as.SmartContractState()
	if asSc == nil {
		return nil, fmt.Errorf("smartContractState is nil")
	}

	rootSc := asSc.StorageRoot()
	key, _, err := decodeHash(hexKey)

	if err != nil {
		return nil, fmt.Errorf("unable to decode storage key: %s", err)
	}
	sValue, ok := api.App.chainState.GetSmartContractDB().StorageValue(address, key.Bytes(), &rootSc)

	if !ok {
		return nil, fmt.Errorf("smartContractState is nil")
	}
	return sValue, nil
}

func (api *MetaAPI) GetTransactionCount(ctx context.Context, address common.Address, blockNrOrHash rpc.BlockNumberOrHash) (*hexutil.Uint64, error) {
	as, err := api.resolveAccountState(ctx, address, blockNrOrHash)
	if err != nil || as == nil {
		return nil, err
	}
	count := hexutil.Uint64(as.Nonce())
	return &count, nil
}

func (api *MetaAPI) GetAccountLastHash(ctx context.Context, address common.Address, blockNrOrHash rpc.BlockNumberOrHash) (common.Hash, error) {
	as, err := api.resolveAccountState(ctx, address, blockNrOrHash)
	if err != nil || as == nil {
		return common.Hash{}, err
	}
	lastHash := as.LastHash()
	return lastHash, nil
}

func (api *MetaAPI) resolveAccountState(ctx context.Context, address common.Address, blockNrOrHash rpc.BlockNumberOrHash) (mt_types.AccountState, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		if blockNr == rpc.LatestBlockNumber || blockNr == rpc.PendingBlockNumber {
			as, err := api.App.chainState.GetAccountStateDB().AccountStateReadOnly(address)
			if err != nil {
				return nil, err
			}
			return as, nil
		}
	}

	var blockMap map[string]interface{}
	if blockNr, ok := blockNrOrHash.Number(); ok {
		blockMap = api.GetBlockByNumber(ctx, api.convertBlockNumber(blockNr.Int64()), true)
	} else if hash, ok := blockNrOrHash.Hash(); ok {
		blockMap = api.GetBlockByHash(ctx, hash, true)
	}

	if blockMap == nil {
		return nil, fmt.Errorf("block not found")
	}

	stateRootInterface := blockMap["stateRoot"]
	var stateRoot common.Hash
	switch v := stateRootInterface.(type) {
	case common.Hash:
		stateRoot = v
	case string:
		stateRoot = common.HexToHash(v)
	case []byte:
		stateRoot = common.BytesToHash(v) // Sử dụng BytesToHash nếu là []byte
	default:
		return nil, fmt.Errorf("unexpected type for stateRoot: %T", stateRootInterface)
	}

	accountStateTrie, err := api.App.GetAccountStateTrie(stateRoot)
	if err != nil {
		return nil, err
	}

	accountStateDB := account_state_db.NewAccountStateDB(
		accountStateTrie,
		api.App.storageManager.GetStorageAccount(),
	)

	return accountStateDB.AccountState(address)
}
