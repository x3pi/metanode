package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mining" // Import mining package
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	mt_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/types"
)

type MtnAPI struct {
	App    *App
	ethApi *MetaAPI
	// LRU Cache for GetExecuteSCResultsHash results
	scResultsCache    *lru.Cache[uint64, common.Hash]
	accountStateCache *lru.Cache[string, map[string]interface{}]
}

// AppConfig defines the structure of the app_config.json file for storage addresses

func NewMtnAPI(app *App, ethApi *MetaAPI) *MtnAPI {
	// Define the maximum size of the LRU cache (e.g., 1000 entries)
	// You might want to make this configurable
	cacheSize := 1000
	cache, err := lru.New[uint64, common.Hash](cacheSize)
	if err != nil {
		logger.Error("[FATAL] Failed to create LRU cache: %v", err)
		logger.SyncFileLog()
		os.Exit(1)
	}

	accountCacheSize := 5000
	accountCache, err := lru.New[string, map[string]interface{}](accountCacheSize)
	if err != nil {
		logger.Error("[FATAL] Failed to create account state cache: %v", err)
		logger.SyncFileLog()
		os.Exit(1)
	}

	return &MtnAPI{
		App:               app,
		ethApi:            ethApi,
		scResultsCache:    cache,
		accountStateCache: accountCache,
	}
}

// connectAndCreateRemoteStorage establishes a connection to a remote storage service.

// GetExecuteSCResultsHash calculates and returns the hash of smart contract execution results for a given block number.
// Tạm thời public có thể gọi lấy getExecuteSCResultsHash qua rpc
func (api *MtnAPI) GetExecuteSCResultsHash(ctx context.Context, blockNumber hexutil.Uint64) (common.Hash, error) {
	logger.Error("blockNumber:", blockNumber)
	if blockNumber == 0 {
		return common.Hash{}, fmt.Errorf("TraceBlock: cannot trace genesis block (block number 0)")
	}

	// --- Cache Check (LRU's Get is thread-safe) ---
	if cachedHash, ok := api.scResultsCache.Get(uint64(blockNumber)); ok {
		logger.Info("GetExecuteSCResultsHash: Returning cached hash for block %d", blockNumber)
		return cachedHash, nil
	}

	// Lấy hash của block mục tiêu và dữ liệu
	hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(uint64(blockNumber))
	if !ok {
		return common.Hash{}, fmt.Errorf("TraceBlock: không tìm thấy block hash cho block number %d", blockNumber)
	}

	blockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(hash)
	if err != nil {
		logger.Warn("TraceBlock: error loading block %d from file: %v", blockNumber, err) // Use Warnf for formatted warning
		return common.Hash{}, fmt.Errorf("TraceBlock: failed to load block with hash %s: %w", hash.Hex(), err)
	}

	// Get previous block hash and data
	oldBlockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(uint64(blockNumber) - 1)
	if !ok {
		return common.Hash{}, fmt.Errorf("TraceBlock: cannot find previous block hash for block number %d", blockNumber-1)
	}

	oldBlockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(oldBlockHash)
	if err != nil {
		logger.Warn("TraceBlock: error loading previous block %d from file: %v", blockNumber-1, err) // Use Warnf
		return common.Hash{}, fmt.Errorf("TraceBlock: failed to load previous block with hash %s: %w", oldBlockHash.Hex(), err)
	}

	// Create TransactionStateDB for the target block to fetch transactions
	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), api.App.storageManager.GetStorageTransaction())
	if err != nil {
		return common.Hash{}, fmt.Errorf("TraceBlock: failed to create TransactionStateDB for block %d: %w", blockNumber, err)
	}

	// --- Fetch all transactions into a slice first ---
	transactionHashes := blockData.Transactions() // Assuming this returns []common.Hash
	txs := make([]types.Transaction, 0, len(transactionHashes))
	logger.Info("Fetching %d transactions for block %d...", len(transactionHashes), blockNumber)

	for _, txHash := range transactionHashes {
		tx, err := txDB.GetTransaction(txHash)
		if err != nil {
			logger.Error("TraceBlock: failed to get transaction %s from state DB for block %d: %v", txHash.Hex(), blockNumber, err)
			// Return immediately if a transaction cannot be fetched
			return common.Hash{}, fmt.Errorf("TraceBlock: cannot get transaction %s from state DB: %w", txHash.Hex(), err)
		}
		txs = append(txs, tx)
	}

	// 4. Connect to remote storages
	accountStorage := api.App.storageManager.GetStorageAccount()
	codeStorage := api.App.storageManager.GetStorageCode()

	dbSmartContract := api.App.storageManager.GetStorageSmartContract()

	chainState, err := blockchain.NewChainStateRemote(oldBlockData.Header(), accountStorage, codeStorage, dbSmartContract, api.App.chainState.GetFreeFeeAddress())
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to create chainState for block %d: %w", blockNumber, err)
	}

	// 6. Group transactions
	items := make([]grouptxns.Item, 0, len(txs))
	for i, tx := range txs {
		items = append(items, grouptxns.Item{
			ID:        i,
			Array:     tx.RelatedAddresses(),
			GroupID:   0,
			Tx:        tx,
			TimeStart: time.Now(),
		})
	}
	groupedGroups, _, err := grouptxns.GroupAndLimitTransactionsOptimized(items, mt_common.MAX_GROUP_GAS, mt_common.MAX_TOTAL_GAS, mt_common.MAX_GROUP_TIME, mt_common.MAX_TOTAL_TIME)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to create grouptxns for block %d: %w", blockNumber, err)
	}

	// 7. Process transactions using ProcessTransactionsRemote
	processResult, err := tx_processor.ProcessTransactionsRemote(ctx, chainState, groupedGroups, true, false, uint64(time.Now().Unix()))
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to process transactions for block %d: %w", blockNumber, err)
	}

	// 8. Calculate and return the final hash
	resultsHash := smart_contract.HashExecuteSCResultsKeccak256(processResult.ExecuteSCResults)

	// --- Store in Cache (LRU's Add is thread-safe) ---
	api.scResultsCache.Add(uint64(blockNumber), resultsHash)

	return resultsHash, nil
}

func (api *MtnAPI) SendRawTransactionWithDeviceKey(ctx context.Context, input hexutil.Bytes) (common.Hash, error) {
	txD := &mt_proto.TransactionWithDeviceKey{}
	err := proto.Unmarshal(input, txD)
	if err != nil {
		logger.Error("Error Unmarshal input:", err)
		return common.Hash{}, err
	}
	txM := &transaction.Transaction{}
	txM.FromProto(txD.Transaction)

	output, err := api.App.transactionProcessor.ProcessTransactionFromRpcWithDeviceKey(txD)
	if err != nil {
		return common.Hash{}, newError(err, output)

	}
	return txM.Hash(), nil

}

func (api *MtnAPI) GetDeviceKey(ctx context.Context, hash common.Hash) (common.Hash, error) {
	data, err := api.App.stateProcessor.GetDeviceKey(hash)
	return data, err
}

func (api *MtnAPI) GetAccountState(ctx context.Context, address common.Address, blockNrOrHash rpc.BlockNumberOrHash) (result map[string]interface{}, retErr error) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			logger.Error("🔴 [PANIC] GetAccountState panic: %v\nStack:\n%s", r, string(buf[:n]))
			retErr = fmt.Errorf("internal panic: %v", r)
		}
	}()

	var isLatest bool
	if blockNr, ok := blockNrOrHash.Number(); ok {
		if blockNr == rpc.LatestBlockNumber || blockNr == rpc.PendingBlockNumber {
			isLatest = true
		}
	}
	var as types.AccountState

	if isLatest {
		// CRITICAL FIX: Bypass LevelDB trie lookup for LatestBlock because the newest
		// hashing runs in the background. Get from live memory singleton instead.
		var err error
		as, err = api.App.chainState.GetAccountStateDB().AccountStateReadOnly(address)
		if err != nil {
			return nil, err
		}
	} else {
		stateRoot, err := api.resolveStateRoot(ctx, blockNrOrHash)
		if err != nil {
			return nil, err
		}

		cacheKey := stateRoot.Hex() + ":" + strings.ToLower(address.Hex())
		if cached, ok := api.accountStateCache.Get(cacheKey); ok {
			return cloneAccountStateMap(cached), nil
		}

		var accountStateTrie mt_trie.StateTrie
		trieCacheKey := stateRoot.Hex()
		if cachedTrie, ok := api.App.blockProcessor.GetTrieCache(trieCacheKey); ok {
			accountStateTrie = cachedTrie
		} else {
			accountStateTrie, err = mt_trie.NewStateTrie(
				stateRoot,
				api.App.storageManager.GetStorageAccount(),
				true,
			)
			if err != nil {
				return nil, err
			}
			api.App.blockProcessor.SetTrieCache(trieCacheKey, accountStateTrie)
		}

		accountStateDB := account_state_db.NewAccountStateDB(
			accountStateTrie,
			api.App.storageManager.GetStorageAccount(),
		)

		as, err = accountStateDB.AccountState(address)
		if err != nil {
			return nil, err
		}
	}

	if as == nil {
		// Account not found in trie — return empty state
		account := map[string]interface{}{
			"address":            address.Hex(),
			"balance":            "0",
			"pendingBalance":     "0",
			"deviceKey":          common.Hash{},
			"lastHash":           common.Hash{},
			"publicKeyBls":       "",
			"nonce":              uint64(0),
			"accountType":        uint64(0),
			"smartContractState": common.Hash{}.String(),
		}
		return account, nil
	}

	smartContractState := ""
	if scs := as.SmartContractState(); scs != nil {
		smartContractState = scs.String()
	}
	account := map[string]interface{}{
		"address":            as.Address(),
		"balance":            as.Balance().String(),
		"pendingBalance":     as.PendingBalance().String(),
		"deviceKey":          as.DeviceKey(),
		"lastHash":           as.LastHash(),
		"publicKeyBls":       hex.EncodeToString(as.PublicKeyBls()),
		"nonce":              as.Nonce(),
		"accountType":        as.AccountType(),
		"smartContractState": smartContractState,
	}

	if !isLatest {
		stateRoot, _ := api.resolveStateRoot(ctx, blockNrOrHash)
		cacheKey := stateRoot.Hex() + ":" + strings.ToLower(address.Hex())
		api.accountStateCache.Add(cacheKey, account)
	}
	return cloneAccountStateMap(account), nil
}

func (api *MtnAPI) resolveStateRoot(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (common.Hash, error) {
	getLastBlock := func() types.Block {
		if api.App.blockProcessor == nil {
			return nil
		}
		return api.App.blockProcessor.GetLastBlock()
	}

	if blockNr, ok := blockNrOrHash.Number(); ok {
		switch blockNr {
		case rpc.LatestBlockNumber, rpc.PendingBlockNumber:
			lb := getLastBlock()
			if lb == nil {
				return common.Hash{}, fmt.Errorf("last block not available")
			}
			return lb.Header().AccountStatesRoot(), nil
		default:
			targetNumber := blockNr.Int64()
			if targetNumber < 0 {
				lb := getLastBlock()
				if lb == nil {
					return common.Hash{}, fmt.Errorf("last block not available")
				}
				targetNumber = int64(lb.Header().BlockNumber())
			}
			hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(uint64(targetNumber))
			if !ok {
				return common.Hash{}, fmt.Errorf("block number %d not found", targetNumber)
			}
			blockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(hash)
			if err != nil {
				return common.Hash{}, fmt.Errorf("failed to load block %s: %w", hash.Hex(), err)
			}
			return blockData.Header().AccountStatesRoot(), nil
		}
	}

	if hash, ok := blockNrOrHash.Hash(); ok {
		blockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(hash)
		if err != nil {
			return common.Hash{}, fmt.Errorf("failed to load block %s: %w", hash.Hex(), err)
		}
		return blockData.Header().AccountStatesRoot(), nil
	}

	// Fallback to latest block state root
	return api.App.blockProcessor.GetLastBlock().Header().AccountStatesRoot(), nil
}

func cloneAccountStateMap(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// SearchTransactions trả về mảng JSON các giao dịch theo query, có phân trang
func (api *MtnAPI) SearchTransactions(ctx context.Context, query string, offset, limit int) (map[string]interface{}, error) {
	// Lấy service explorer từ app (giả sử app có trường ExplorerService)
	if !api.App.storageManager.IsExplorer() {
		return nil, fmt.Errorf("ExplorerService chưa được khởi tạo")
	}
	results, total, err := api.App.storageManager.GetExplorerSearchService().SearchTransactions(query, offset, limit)
	if err != nil {
		return nil, err
	}
	logger.Error(total)
	// Chuyển từng kết quả JSON string thành map[string]interface{}
	var txs []map[string]interface{}
	for _, jsonStr := range results {
		var tx map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &tx); err == nil {
			txs = append(txs, tx)
		}
	}
	// Trả về object cha chứa total và transactions
	return map[string]interface{}{
		"total":        total,
		"transactions": txs,
	}, nil
}

// SearchTransactions trả về mảng JSON các giao dịch theo query, có phân trang
func (api *MtnAPI) SearchTransactionsReadOnly(ctx context.Context, query string, offset, limit int) (map[string]interface{}, error) {
	// Lấy service explorer từ app (giả sử app có trường ExplorerService)
	if !api.App.storageManager.IsExplorer() {
		return nil, fmt.Errorf("ExplorerService chưa được khởi tạo")
	}
	results, total, err := api.App.storageManager.GetExplorerSearchServiceReadOnly().SearchTransactions(query, offset, limit)
	if err != nil {
		return nil, err
	}
	logger.Error(total)
	// Chuyển từng kết quả JSON string thành map[string]interface{}
	var txs []map[string]interface{}
	for _, jsonStr := range results {
		var tx map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &tx); err == nil {
			txs = append(txs, tx)
		}
	}
	// Trả về object cha chứa total và transactions
	return map[string]interface{}{
		"total":        total,
		"transactions": txs,
	}, nil
}

// GetJob trả về thông tin job theo địa chỉ
func (api *MtnAPI) GetJob(ctx context.Context, address common.Address) (*mining.Job, error) {
	// Lấy thông tin job từ cơ sở dữ liệu hoặc nguồn dữ liệu khác
	// (cần triển khai logic này dựa trên cấu trúc ứng dụng của bạn)
	if !api.App.storageManager.IsMining() {
		return nil, fmt.Errorf("MiningService chưa được khởi tạo")
	}
	jobInfo, err := api.App.storageManager.GetMiningService().GetOrAssignJob(address, api.App.blockProcessor.GetLastBlock().Header().BlockNumber())
	if err != nil {
		return nil, err
	}
	return jobInfo, nil
}

// CompleteJob gọi mining service để hoàn thành job
func (api *MtnAPI) CompleteJob(ctx context.Context, address common.Address, jobID string, hashExecute common.Hash) (bool, error) {
	logger.Info("Received CompleteJob request for JobID '%s' from address %s", jobID, address.String())
	if !api.App.storageManager.IsMining() {
		return false, fmt.Errorf("MiningService chưa được khởi tạo")
	}
	miniService := api.App.storageManager.GetMiningService()

	job, err := miniService.GetJobByID(jobID)
	if err != nil {
		logger.Error("Could not find job with ID %s: %v", jobID, err)
		return false, err
	}

	// 1. So sánh địa chỉ bằng cách chuyển đổi chuỗi assignee về lại kiểu common.Address.
	assigneeAddress := common.HexToAddress(job.Assignee)
	if assigneeAddress != address {
		err := fmt.Errorf("job %s is not assigned to address %s (assigned to %s)", jobID, address.String(), assigneeAddress.String())
		logger.Error(err.Error())
		return false, err
	}

	// 2. Kiểm tra xem trạng thái của job có phải là 'new' không
	if job.Status != mining.JobStatusNew {
		err := fmt.Errorf("job %s is not in '%s' status (current: %s)", jobID, mining.JobStatusNew, job.Status)
		logger.Error(err.Error())
		return false, err
	}

	// 3. Xử lý logic hoàn thành job dựa trên loại job
	if job.JobType == mining.JobTypeVideoAds {
		logger.Info("Completing a 'video_ads' job: %s", jobID)
		// Với job video, chỉ cần gọi hoàn thành là được
		_, err := miniService.CompleteJob(jobID, address.String())
		if err != nil {
			logger.Error("Error from service on completing video_ads job %s: %v", jobID, err)
			return false, err
		}
	} else {
		// Đây là logic cho các job khác, ví dụ: 'validate_block'
		logger.Info("Validating a 'validate_block' job: %s", jobID)

		// BƯỚC 1: Lấy block number từ job.Data
		var uint64Value hexutil.Uint64
		err := uint64Value.UnmarshalText([]byte(job.Data))
		if err != nil {
			err := fmt.Errorf("invalid block number format for job %s: '%s': %w", jobID, job.Data, err)
			logger.Error(err.Error())
			return false, err
		}

		// BƯỚC 2: Gọi GetExecuteSCResultsHash để tính hash chính xác của block đó
		logger.Info("Calculating expected hash for block %d...", job.Data)
		expectedHash, err := api.GetExecuteSCResultsHash(ctx, uint64Value)
		if err != nil {
			err := fmt.Errorf("could not calculate execution hash for block %s: %w", job.Data, err)
			logger.Error(err.Error())
			return false, err
		}
		logger.Info("Expected hash: %s", expectedHash.Hex())
		logger.Info("Received hash: %s", hashExecute.Hex())

		// BƯỚC 3: So sánh hash tính được với hash người dùng gửi lên
		if expectedHash != hashExecute {
			err := fmt.Errorf("hash mismatch for block %s: expected %s, but got %s", job.Data, expectedHash.Hex(), hashExecute.Hex())
			logger.Error(err.Error())
			return false, err
		}

		// BƯỚC 4: Nếu hash khớp, gọi service để hoàn thành job
		logger.Info("Hash validation successful for job %s. Completing job...", jobID)
		_, err = miniService.CompleteJob(jobID, address.String())
		if err != nil {
			logger.Error("Error from service on completing validate_block job %s: %v", jobID, err)
			return false, err
		}
	}

	logger.Info("Job %s completed successfully by %s.", jobID, address.String())
	return true, nil
}

// GetTransactionHistoryByAddress trả về lịch sử giao dịch liên quan đến một địa chỉ, có phân trang.
func (api *MtnAPI) GetTransactionHistoryByAddress(ctx context.Context, address common.Address, offset, limit int) (map[string]interface{}, error) {
	if !api.App.storageManager.IsMining() {
		return nil, fmt.Errorf("MiningService chưa được khởi tạo")
	}
	if !api.App.storageManager.IsMining() { // Kiểm tra xem mining service có được kích hoạt không
		return nil, fmt.Errorf("MiningService chưa được khởi tạo hoặc không được kích hoạt")
	}

	// Tạo query string để tìm giao dịch mà địa chỉ là người gửi hoặc người nhận
	queryStr := fmt.Sprintf("sender:%s OR recipient:%s", address.Hex(), address.Hex())

	// Gọi hàm tìm kiếm từ MiningService
	// Lưu ý: MiningService cần được bổ sung phương thức SearchTransactionRecords
	// Hiện tại MiningService chỉ có _searchAndParseTransactionRecords, nó là internal.
	// Bạn sẽ cần một public method trong MiningService để expose chức năng này.
	// Giả sử có `SearchTransactionHistory(query string, offset, limit int) ([]*mining.TransactionRecord, uint, error)`
	results, total, err := api.App.storageManager.GetMiningService().SearchTransactionHistory(queryStr, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search transaction history: %w", err)
	}

	// Chuyển đổi slice []*mining.TransactionRecord sang slice map[string]interface{} cho RPC response
	var records []map[string]interface{}
	for _, record := range results {
		// Marshal và Unmarshal lại để đảm bảo cấu trúc JSON đồng nhất, hoặc tạo map thủ công
		var recMap map[string]interface{}
		jsonBytes, _ := json.Marshal(record)
		json.Unmarshal(jsonBytes, &recMap)
		records = append(records, recMap)
	}

	return map[string]interface{}{
		"total":   total,
		"records": records,
	}, nil
}

// GetTransactionHistoryByJobID trả về lịch sử giao dịch liên quan đến một Job ID.
func (api *MtnAPI) GetTransactionHistoryByJobID(ctx context.Context, jobID string) (map[string]interface{}, error) {
	if !api.App.storageManager.IsMining() { // Kiểm tra xem mining service có được kích hoạt không
		return nil, fmt.Errorf("MiningService chưa được khởi tạo hoặc không được kích hoạt")
	}

	// Gọi hàm tìm kiếm từ MiningService
	// Lưu ý: MiningService cần được bổ sung phương thức GetTransactionRecordByJobID
	// Hiện tại MiningService chỉ có GetTransactionRecordByTxID.
	// Job ID có thể ánh xạ 1-1 với một giao dịch (nếu mỗi job chỉ có 1 reward tx)
	// hoặc bạn cần tìm tất cả các giao dịch liên quan đến job đó.
	// Giả sử mỗi job chỉ có 1 reward tx và Job.TxHash đã lưu nó.

	miniService := api.App.storageManager.GetMiningService()

	job, err := miniService.GetJobByID(jobID) // Lấy job để có TxHash
	if err != nil {
		return nil, fmt.Errorf("failed to find job with ID %s: %w", jobID, err)
	}

	if job.TxHash == "" {
		return nil, fmt.Errorf("no transaction history found for job %s", jobID)
	}

	// Dùng TxHash để lấy TransactionRecord
	record, err := miniService.GetTransactionRecordByTxID(job.TxHash) // Gọi hàm đã có
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve transaction record for job %s (TxHash %s): %w", jobID, job.TxHash, err)
	}

	// Chuyển đổi sang map[string]interface{}
	var recMap map[string]interface{}
	jsonBytes, _ := json.Marshal(record)
	json.Unmarshal(jsonBytes, &recMap)

	return map[string]interface{}{
		"record": recMap,
	}, nil
}

// EchoNumber là một API đơn giản nhận vào một số nguyên, ghi log và trả về chính số đó.
func (api *MtnAPI) EchoNumber(ctx context.Context, number int) (int, error) {
	// Ghi log số nhận được ra console
	// Trả về số đã nhận và lỗi nil để báo hiệu thành công
	return number, nil
}

// GetTransactionsAndTPSInRange trả về số lượng giao dịch và TPS trong một khoảng block.
func (api *MtnAPI) GetTransactionsAndTPSInRange(ctx context.Context, startBlock hexutil.Uint64, endBlock hexutil.Uint64) (map[string]interface{}, error) {
	if !api.App.storageManager.IsExplorer() {
		return nil, fmt.Errorf("ExplorerService is not initialized")
	}

	explorerService := api.App.storageManager.GetExplorerSearchService()

	totalTx, tps, err := explorerService.GetTransactionsAndTPSInRange(uint64(startBlock), uint64(endBlock))
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"totalTransactions": totalTx,
		"tps":               tps,
	}, nil
}

// RegisterBlsKeyParams holds the parameters for BLS key registration.
type RegisterBlsKeyParams struct {
	Address       string `json:"address"`
	BlsPrivateKey string `json:"blsPrivateKey"`
	Timestamp     string `json:"timestamp"`
	Signature     string `json:"signature"`
}

// RegisterBlsKeyWithSignature verifies an ECDSA signature proving address
// ownership, then stores the BLS private key in the Master's encrypted key
// store. This merges the RPC client's rpc_registerBlsKeyWithSignature handler.
func (api *MtnAPI) RegisterBlsKeyWithSignature(ctx context.Context, params RegisterBlsKeyParams) (string, error) {
	if api.App.blsKeyStore == nil {
		return "", fmt.Errorf("BLS key store is not configured (set master_password and app_pepper in config)")
	}

	// Validate address
	if !common.IsHexAddress(params.Address) {
		return "", fmt.Errorf("invalid Ethereum address format")
	}
	signerAddress := common.HexToAddress(params.Address)

	// Validate BLS key format
	if !strings.HasPrefix(params.BlsPrivateKey, "0x") || len(params.BlsPrivateKey) != 66 {
		return "", fmt.Errorf("invalid BLS private key format: expected 0x-prefixed 32-byte hex")
	}
	blsKeyBytes := common.FromHex(params.BlsPrivateKey)
	if len(blsKeyBytes) != 32 {
		return "", fmt.Errorf("invalid BLS private key: expected 32 bytes")
	}

	// Validate timestamp
	clientTimestamp, err := time.Parse(time.RFC3339Nano, params.Timestamp)
	if err != nil {
		clientTimestamp, err = time.Parse(time.RFC3339, params.Timestamp)
		if err != nil {
			return "", fmt.Errorf("invalid timestamp format: expected ISO 8601")
		}
	}
	if time.Since(clientTimestamp).Abs() > 2*time.Minute {
		return "", fmt.Errorf("timestamp is too old or in the future")
	}

	// Verify ECDSA signature
	messageToVerify := fmt.Sprintf("BLS Data: %s\nTimestamp: %s", params.BlsPrivateKey, params.Timestamp)
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(messageToVerify), messageToVerify)
	messageHash := crypto.Keccak256Hash([]byte(prefixedMessage))

	sigBytes := common.FromHex(params.Signature)
	if len(sigBytes) == 65 && (sigBytes[64] == 27 || sigBytes[64] == 28) {
		sigBytes[64] -= 27
	}

	recoveredPubKeyBytes, err := crypto.Ecrecover(messageHash.Bytes(), sigBytes)
	if err != nil {
		return "", fmt.Errorf("signature verification failed: could not recover public key")
	}
	unmarshaledPubKey, err := crypto.UnmarshalPubkey(recoveredPubKeyBytes)
	if err != nil {
		return "", fmt.Errorf("signature verification failed: could not unmarshal public key")
	}
	recoveredAddress := crypto.PubkeyToAddress(*unmarshaledPubKey)

	if recoveredAddress != signerAddress {
		return "", fmt.Errorf("signature verification failed: address mismatch (recovered=%s, expected=%s)",
			recoveredAddress.Hex(), signerAddress.Hex())
	}

	// Store the BLS private key
	if err := api.App.blsKeyStore.SetPrivateKey(signerAddress, params.BlsPrivateKey); err != nil {
		return "", fmt.Errorf("failed to store BLS private key: %w", err)
	}

	logger.Info("[RegisterBlsKey] BLS key registered for %s", signerAddress.Hex())
	return "BLS private key successfully registered.", nil
}
