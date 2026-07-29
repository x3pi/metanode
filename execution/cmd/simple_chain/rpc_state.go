package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	mt_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"

	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

func (api *MetaAPI) Call(ctx context.Context, rawInput json.RawMessage, blockNrOrHash *rpc.BlockNumberOrHash) (hexutil.Bytes, error) {
	// Chuyển yêu cầu đến hàm xử lý song song
	resultChan := make(chan CallResult, 1) // Channel để nhận kết quả

	// Default to latest if nil
	if blockNrOrHash == nil {
		defaultBlock := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)
		blockNrOrHash = &defaultBlock
	}

	go api.processCallRequest(ctx, rawInput, *blockNrOrHash, resultChan)

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
func (api *MetaAPI) processCallRequest(ctx context.Context, rawInput json.RawMessage, blockNrOrHash rpc.BlockNumberOrHash, resultChan chan CallResult) {
	defer close(resultChan) // Đóng channel khi hoàn thành

	var inputStr string
	var txM *transaction.Transaction

	// 1. Cố gắng parse theo chuẩn cũ (truyền raw hexutil.Bytes của MetaNode transaction)
	if err := json.Unmarshal(rawInput, &inputStr); err == nil {
		inputBytes, errHex := hexutil.Decode(inputStr)
		if errHex == nil {
			txM = &transaction.Transaction{}
			if errUnmarshal := txM.Unmarshal(inputBytes); errUnmarshal != nil {
				txM = nil // Fallback
			}
		}
	}

	// 2. Nếu parse chuẩn cũ thất bại, cố gắng parse theo chuẩn Ethereum (TransactionArgs JSON object)
	if txM == nil {
		var args TransactionArgs
		if err := json.Unmarshal(rawInput, &args); err != nil {
			logger.Warn("Error Unmarshal Call input: %v", err)
			resultChan <- CallResult{Result: common.FromHex("0x00"), Error: fmt.Errorf("invalid Call input: %v", err)}
			return
		}

		var toAddress common.Address
		if args.To != nil {
			toAddress = *args.To
		}

		amount := big.NewInt(0)
		if args.Value != nil {
			amount = (*big.Int)(args.Value)
		}

		var inputData []byte
		if args.Data != nil {
			inputData = *args.Data
		} else if args.Input != nil {
			inputData = *args.Input
		}

		gasLimit := uint64(50000000)
		if args.Gas != nil {
			gasLimit = uint64(*args.Gas)
		}

		gasPrice := uint64(0)
		if args.GasPrice != nil {
			gasPrice = (*big.Int)(args.GasPrice).Uint64()
		}

		var fromAddress common.Address
		if args.From != nil {
			fromAddress = *args.From
		}

		var bData []byte
		if inputData != nil {
			callData := transaction.NewCallData(inputData)
			bData, _ = callData.Marshal()
		}

		txM = transaction.NewTransaction(
			fromAddress,
			toAddress,
			amount,
			gasLimit,
			gasPrice,
			0, // maxTimeUse
			bData,
			nil,           // relatedAddresses
			common.Hash{}, // lastDeviceKey
			common.Hash{}, // newDeviceKey
			1,             // nonce
			api.App.config.ChainId.Uint64(),
		).(*transaction.Transaction)

		txM.SetReadOnly(true)
	}

	if txM.GetNonce() == 0 {
		txM.SetNonce(1)
	}

	var rs mt_types.ExecuteSCResult
	var err error

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
		var header mt_types.BlockHeader
		lastBlockNum := storage.GetLastBlockNumber()
		if lastBlockNum > 0 {
			hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(lastBlockNum)
			if ok {
				if loadedBlock, errLoad := api.App.chainState.GetBlockDatabase().GetBlockByHash(hash); errLoad == nil {
					header = loadedBlock.Header()
				}
			}
		}
		if header == nil {
			if currentBlock := api.App.blockProcessor.GetLastBlock(); currentBlock != nil {
				header = currentBlock.Header()
			}
		}

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
	if err != nil {
		return nil, err
	}
	zero := new(big.Int)
	vl := hexutil.Big(*zero)
	if as == nil {
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
	if err != nil {
		return nil, err
	}
	if as == nil {
		return hexutil.Bytes{}, nil
	}
	sc := as.SmartContractState()
	if sc == nil {
		return hexutil.Bytes{}, nil
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
	if err != nil {
		return nil, err
	}
	emptyStorage := common.FromHex("0x0000000000000000000000000000000000000000000000000000000000000000")
	if as == nil {
		return emptyStorage, nil
	}

	asSc := as.SmartContractState()
	if asSc == nil {
		return emptyStorage, nil
	}

	rootSc := asSc.StorageRoot()
	key, _, err := decodeHash(hexKey)

	if err != nil {
		return nil, fmt.Errorf("unable to decode storage key: %s", err)
	}
	sValue, ok := api.App.chainState.GetSmartContractDB().StorageValue(address, key.Bytes(), &rootSc)

	if !ok {
		return emptyStorage, nil
	}
	return sValue, nil
}

func (api *MetaAPI) GetTransactionCount(ctx context.Context, address common.Address, blockNrOrHash rpc.BlockNumberOrHash) (*hexutil.Uint64, error) {
	as, err := api.resolveAccountState(ctx, address, blockNrOrHash)
	if err != nil {
		return nil, err
	}
	if as == nil {
		zero := hexutil.Uint64(0)
		return &zero, nil
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
		if blockNr == rpc.PendingBlockNumber || blockNr == rpc.LatestBlockNumber {
			as, err := api.App.chainState.GetAccountStateDB().AccountStateReadOnly(address)
			if err != nil {
				return nil, err
			}
			return as, nil
		}
	}

	var blockMap map[string]interface{}
	var targetBlockNumber uint64
	var foundBlockNumber bool
	var errGetBlock error
	if blockNr, ok := blockNrOrHash.Number(); ok {
		targetBlockNumber = uint64(blockNr.Int64())
		foundBlockNumber = true
		blockMap, errGetBlock = api.GetBlockByNumber(ctx, api.convertBlockNumber(blockNr.Int64()), false)
		if errGetBlock != nil {
			return nil, fmt.Errorf("failed to get block by number: %w", errGetBlock)
		}
	} else if hash, ok := blockNrOrHash.Hash(); ok {
		blockMap, errGetBlock = api.GetBlockByHash(ctx, hash, false)
		if errGetBlock != nil {
			return nil, fmt.Errorf("failed to get block by hash: %w", errGetBlock)
		}
	}

	if blockMap == nil {
		return nil, fmt.Errorf("block not found")
	}

	if numStr, okStr := blockMap["number"].(string); okStr {
		if num, err := hexutil.DecodeUint64(numStr); err == nil {
			targetBlockNumber = num
			foundBlockNumber = true
		}
	}

	// Phase 2.4: Try to get historical state from StateChangelogDB
	// CRITICAL FIX (Jun 2026): Use `<=` (not `<`) so that when the queried block equals the
	// last committed block, we still use StateChangelogDB instead of the current in-memory
	// NOMT state which may already be partially applying the next block.
	// Off-by-one example: if lastBlock=38 and targetBlock=38, `38 < 38 = false` skips the
	// changelog → falls through to live NOMT trie → returns state from block 39+ instead of 38.
	changelogDB := api.App.chainState.GetChangelogDB()
	isHistorical := foundBlockNumber && targetBlockNumber <= storage.GetLastBlockNumber()
	if changelogDB != nil && isHistorical {
		logger.Info("🔍 [DEBUG-RPC] Resolving historical state for %x at block %d using StateChangelogDB", address.Bytes(), targetBlockNumber)
		stateBytes, err := changelogDB.GetStateAt(address.Bytes(), targetBlockNumber)
		if err == nil {
			logger.Info("🔍 [DEBUG-RPC] StateChangelogDB found entry for %x at block %d, len(bytes)=%d", address.Bytes(), targetBlockNumber, len(stateBytes))
			if len(stateBytes) == 0 {
				// Address explicitly deleted or does not exist
				return nil, nil
			}
			// Unmarshal the protobuf bytes
			as := &state.AccountState{}
			if errUnmarshal := as.Unmarshal(stateBytes); errUnmarshal == nil {
				logger.Info("🔍 [DEBUG-RPC] Successfully unmarshaled AccountState for %x at block %d: Balance=%s, Nonce=%d", address.Bytes(), targetBlockNumber, as.Balance().String(), as.Nonce())
				return as, nil
			} else {
				logger.Warn("⚠️ [RPC] Failed to unmarshal historical state for %x at block %d: %v", address, targetBlockNumber, errUnmarshal)
			}
		} else {
			logger.Info("🔍 [DEBUG-RPC] StateChangelogDB GetStateAt returned error for %x at block %d: %v", address.Bytes(), targetBlockNumber, err)
			if mt_trie.GetStateBackend() == mt_trie.BackendNOMT {
				startBlock := changelogDB.GetStartBlock()
				hasAny := changelogDB.HasAnyEntry(address.Bytes())
				logger.Info("🔍 [DEBUG-RPC] StateChangelogDB startBlock=%d, HasAnyEntry=%v for %x", startBlock, hasAny, address.Bytes())

				// Case 1: targetBlock predates changelog tracking
				if startBlock > 0 && targetBlockNumber < startBlock {
					logger.Warn("⚠️ [RPC] Historical state unavailable: block %d is before changelog tracking started at block %d for address %x.", targetBlockNumber, startBlock, address)
					return nil, fmt.Errorf("historical state unavailable: block %d predates changelog tracking (started at block %d)", targetBlockNumber, startBlock)
				}

				if hasAny {
					// Case 3: Entries exist but only for blocks AFTER targetBlock
					logger.Warn("⚠️ [RPC] StateChangelogDB has entries for %x but not at/before block %d. Account was modified after target block — cannot determine pre-modification state.", address, targetBlockNumber)
					return nil, fmt.Errorf("historical state unavailable: account was modified after block %d but pre-modification state not recorded", targetBlockNumber)
				}

				// Case 2: No entries at all AND targetBlock >= startBlock
				// → Account NEVER modified in [startBlock, now] → current NOMT state is EXACT.
				logger.Debug("📜 [RPC] Address %x has no changelog entries since tracking started (block %d). Account unmodified — current state IS exact historical state at block %d.", address, startBlock, targetBlockNumber)
				// Fall through to NOMT trie read — this is guaranteed correct.
			} else {
				// Non-NOMT backends: fallback to stateRoot trie traversal (MPT supports historical queries)
				logger.Warn("⚠️ [RPC] StateChangelogDB missing entry for %x at block %d (err: %v). Falling back to trie with historical stateRoot.", address, targetBlockNumber, err)
			}
		}
	}

	if changelogDB == nil && mt_trie.GetStateBackend() == mt_trie.BackendNOMT && isHistorical {
		return nil, fmt.Errorf("historical state query not supported: StateChangelogDB is disabled (missing --is-rpc flag)")
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

// GetProof returns the Merkle proof for a given account address at a specific block.
func (api *MetaAPI) GetProof(ctx context.Context, address common.Address, blockNrOrHash rpc.BlockNumberOrHash) (hexutil.Bytes, error) {
	if mt_trie.GetStateBackend() == mt_trie.BackendNOMT {
		if nomtTrie, ok := api.App.chainState.GetAccountStateDB().Trie().(*mt_trie.NomtStateTrie); ok {
			if err := nomtTrie.WaitCommitPayload(); err != nil {
				logger.Error("WaitCommitPayload failed in GetProof (NOMT): %v", err)
			}
		}
	}

	// Resolve block map to get stateRoot
	var blockMap map[string]interface{}
	var errGetBlock error

	if blockNr, ok := blockNrOrHash.Number(); ok {
		if blockNr == rpc.PendingBlockNumber {
			blockMap, errGetBlock = api.GetBlockByNumber(ctx, rpc.LatestBlockNumber, false)
		} else {
			blockMap, errGetBlock = api.GetBlockByNumber(ctx, api.convertBlockNumber(blockNr.Int64()), false)
		}
	} else if hash, ok := blockNrOrHash.Hash(); ok {
		blockMap, errGetBlock = api.GetBlockByHash(ctx, hash, false)
	}

	if errGetBlock != nil {
		return nil, fmt.Errorf("failed to get block: %w", errGetBlock)
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
		stateRoot = common.BytesToHash(v)
	default:
		return nil, fmt.Errorf("unexpected type for stateRoot: %T", stateRootInterface)
	}

	// Try to get trie directly (works for latest block or MPT backend)
	accountStateTrie, err := api.App.GetAccountStateTrie(stateRoot)
	if err == nil {
		// Only use the direct trie if its root actually matches the requested block's stateRoot!
		// For NOMT, GetAccountStateTrie always returns the latest global trie, so we must check.
		if accountStateTrie.Hash() == stateRoot {
			if nomtTrie, ok := accountStateTrie.(*mt_trie.NomtStateTrie); ok {
				proof, errGen := nomtTrie.GenerateProof(address.Bytes())
				if errGen == nil {
					return proof, nil
				}
			}
		}
	}

	// If direct access fails (e.g. historical block in NOMT), fallback to reconstruction
	if mt_trie.GetStateBackend() == mt_trie.BackendNOMT {
		logger.Info("🔍 [GetProof] Direct trie access failed, falling back to reconstructHistoricalTrie for block %v", blockNrOrHash)
		verifyCacheMu.Lock()
		defer verifyCacheMu.Unlock()

		trie, _, _, _, _, errReconstruct := api.reconstructHistoricalTrieLocked(ctx, blockNrOrHash)
		if errReconstruct != nil {
			return nil, fmt.Errorf("failed to reconstruct historical trie: %w", errReconstruct)
		}

		proof, errProof := trie.GenerateProof(address.Bytes())
		if errProof != nil {
			return nil, fmt.Errorf("failed to generate proof from reconstructed trie: %w", errProof)
		}
		return proof, nil
	}

	return nil, fmt.Errorf("failed to get proof: direct access error=%v", err)
}

var (
	verifyCacheMu      sync.Mutex
	verifyCachedTrie   *mt_trie.NomtStateTrie
	verifyCachedBlock  uint64
	verifyCachedDir    string
	verifyCachedHandle *nomt_ffi.Handle
)

func (api *MetaAPI) reconstructHistoricalTrieLocked(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (
	*mt_trie.NomtStateTrie, uint64, common.Hash, bool, int, error) {

	// 1. Get block and expected state root
	var blockMap map[string]interface{}
	var targetBlockNumber uint64
	var errGetBlock error

	if blockNr, ok := blockNrOrHash.Number(); ok {
		targetBlockNumber = uint64(blockNr.Int64())
		blockMap, errGetBlock = api.GetBlockByNumber(ctx, api.convertBlockNumber(blockNr.Int64()), false)
	} else if hash, ok := blockNrOrHash.Hash(); ok {
		blockMap, errGetBlock = api.GetBlockByHash(ctx, hash, false)
	}

	if errGetBlock != nil || blockMap == nil {
		return nil, 0, common.Hash{}, false, 0, fmt.Errorf("failed to get block: %w", errGetBlock)
	}

	if numStr, okStr := blockMap["number"].(string); okStr {
		if num, err := hexutil.DecodeUint64(numStr); err == nil {
			targetBlockNumber = num
		}
	}

	stateRootInterface := blockMap["stateRoot"]
	var expectedRoot common.Hash
	switch v := stateRootInterface.(type) {
	case common.Hash:
		expectedRoot = v
	case string:
		expectedRoot = common.HexToHash(v)
	case []byte:
		expectedRoot = common.BytesToHash(v)
	default:
		return nil, 0, common.Hash{}, false, 0, fmt.Errorf("unexpected type for stateRoot: %T", stateRootInterface)
	}

	// 2. Check if ChangelogDB is available and state backend is NOMT
	if mt_trie.GetStateBackend() != mt_trie.BackendNOMT {
		return nil, 0, common.Hash{}, false, 0, fmt.Errorf("VerifyHistoricalRoot is only supported for NOMT state backend")
	}

	changelogDB := api.App.chainState.GetChangelogDB()
	if changelogDB == nil {
		return nil, 0, common.Hash{}, false, 0, fmt.Errorf("ChangelogDB is not available")
	}

	// 3. Get all known addresses from current trie
	lastBlock := api.App.blockProcessor.GetLastBlock()
	if lastBlock == nil {
		return nil, 0, common.Hash{}, false, 0, fmt.Errorf("failed to get last block")
	}
	currentTrie, err := api.App.GetAccountStateTrie(lastBlock.Header().AccountStatesRoot())
	if err != nil {
		return nil, 0, common.Hash{}, false, 0, fmt.Errorf("failed to get account state trie: %w", err)
	}
	nomtTrie, ok := currentTrie.(*mt_trie.NomtStateTrie)
	if !ok {
		return nil, 0, common.Hash{}, false, 0, fmt.Errorf("current trie is not NomtStateTrie")
	}

	allEntries, err := nomtTrie.GetAll()
	if err != nil {
		return nil, 0, common.Hash{}, false, 0, fmt.Errorf("failed to get all keys: %w", err)
	}
	uniqueAddrs, err := changelogDB.GetAllUniqueAddresses()
	if err == nil {
		for _, addr := range uniqueAddrs {
			hexKey := hex.EncodeToString(addr)
			if _, exists := allEntries[hexKey]; !exists {
				currentVal, _ := nomtTrie.Get(addr)
				allEntries[hexKey] = currentVal
			}
		}
	}

	// FAST PATH
	if verifyCachedTrie != nil && targetBlockNumber == verifyCachedBlock+1 {
		changes, errChange := changelogDB.GetBlockChanges(targetBlockNumber)
		if errChange == nil {
			for _, change := range changes {
				val := change.NewValue
				if bytes.Equal(val, []byte("DEL")) {
					verifyCachedTrie.Update(change.Key, []byte{})
				} else {
					verifyCachedTrie.Update(change.Key, val)
				}
			}

			// Commit with persistToDisk=true
			_, _, _, errCommit := verifyCachedTrie.Commit(true)
			if errCommit != nil {
				return nil, 0, common.Hash{}, false, 0, fmt.Errorf("failed to commit cached trie: %w", errCommit)
			}
			if err := verifyCachedTrie.CommitPayload(); err != nil {
				return nil, 0, common.Hash{}, false, 0, fmt.Errorf("failed to commit payload to cached trie: %w", err)
			}

			verifyCachedBlock = targetBlockNumber
			return verifyCachedTrie, targetBlockNumber, expectedRoot, true, -1, nil
		}
	}

	// FULL REBUILD PATH
	if verifyCachedTrie != nil {
		verifyCachedHandle.Close()
		os.RemoveAll(verifyCachedDir)
		verifyCachedTrie = nil
	}

	tempDir, err := os.MkdirTemp("", "nomt_verify_*")
	if err != nil {
		return nil, 0, common.Hash{}, false, 0, fmt.Errorf("failed to create temp dir: %w", err)
	}

	tempHandle, err := nomt_ffi.Open(tempDir, 1, 64, 64, 0, true)
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, 0, common.Hash{}, false, 0, fmt.Errorf("failed to init temp nomt handle: %w", err)
	}

	tempTrie := mt_trie.NewNomtStateTrie(tempHandle, false, "account_state")

	allAddressesToVerify := make(map[string]bool)
	for hexKey := range allEntries {
		allAddressesToVerify[hexKey] = true
	}

	genesisAddrs := make(map[string][]byte)
	if api.App.genesis != nil {
		for _, account := range api.App.genesis.Alloc {
			a := account.ToAccountState()
			a.PlusOneNonce()
			b, err := a.Marshal()
			if err == nil {
				genesisAddrs[hex.EncodeToString(a.Address().Bytes())] = b
			}
		}
	}
	for hexKey := range genesisAddrs {
		allAddressesToVerify[hexKey] = true
	}

	addressesToQuery := make([][]byte, 0, len(allAddressesToVerify))
	for hexKey := range allAddressesToVerify {
		address, errDecode := hex.DecodeString(hexKey)
		if errDecode != nil {
			continue
		}
		addressesToQuery = append(addressesToQuery, address)
	}

	historicalStates := changelogDB.GetHistoricalStates(addressesToQuery, targetBlockNumber)

	for _, address := range addressesToQuery {
		hexKey := hex.EncodeToString(address)
		hState := historicalStates[string(address)]

		if hState.Found {
			if len(hState.Value) > 0 {
				tempTrie.Update(address, hState.Value)
			}
		} else {
			if hState.HasAny {
				if genBytes, ok := genesisAddrs[hexKey]; ok {
					tempTrie.Update(address, genBytes)
				}
			} else {
				if currentVal, exists := allEntries[hexKey]; exists && len(currentVal) > 0 {
					tempTrie.Update(address, currentVal)
				} else if genBytes, ok := genesisAddrs[hexKey]; ok {
					tempTrie.Update(address, genBytes)
				}
			}
		}
	}

	_, _, _, errCommit := tempTrie.Commit(true)
	if errCommit != nil {
		tempHandle.Close()
		os.RemoveAll(tempDir)
		return nil, 0, common.Hash{}, false, 0, fmt.Errorf("failed to commit temp trie: %w", errCommit)
	}
	if err := tempTrie.CommitPayload(); err != nil {
		return nil, 0, common.Hash{}, false, 0, fmt.Errorf("failed to commit payload to temp trie: %w", err)
	}

	verifyCachedTrie = tempTrie
	verifyCachedHandle = tempHandle
	verifyCachedDir = tempDir
	verifyCachedBlock = targetBlockNumber

	return verifyCachedTrie, targetBlockNumber, expectedRoot, false, len(allEntries), nil
}

// VerifyHistoricalRoot recreates the NOMT trie at a historical block using ChangelogDB
// to verify if the computed root matches the stateRoot in the block header.
func (api *MetaAPI) VerifyHistoricalRoot(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (map[string]interface{}, error) {
	verifyCacheMu.Lock()
	defer verifyCacheMu.Unlock()

	trie, targetBlockNumber, expectedRoot, fastPath, numEntries, err := api.reconstructHistoricalTrieLocked(ctx, blockNrOrHash)
	if err != nil {
		return nil, err
	}

	recoveredRoot := trie.Hash()
	match := recoveredRoot == expectedRoot

	return map[string]interface{}{
		"targetBlock":   targetBlockNumber,
		"expectedRoot":  expectedRoot.Hex(),
		"recoveredRoot": recoveredRoot.Hex(),
		"match":         match,
		"num_entries":   numEntries,
		"fast_path":     fastPath,
	}, nil
}
