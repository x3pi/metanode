package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/file_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"google.golang.org/protobuf/proto"
)

// GetTransactionByHash returns the transaction for the given hash
func (api *MetaAPI) GetTransactionByHash(ctx context.Context, hashEth common.Hash) (*RPCTransaction, error) {

	blockNumber, ok := blockchain.GetBlockChainInstance().GetBlockNumberByTxHash(hashEth)
	hashTx := hashEth

	if !ok {

		hash, ok := blockchain.GetBlockChainInstance().GetEthHashMapblsHash(hashEth)
		if !ok {

			return nil, fmt.Errorf("transaction not found by hash: %v", hashEth)
		}

		blockNumber, ok = blockchain.GetBlockChainInstance().GetBlockNumberByTxHash(hash)
		if !ok {
			if rawTx, success := blockchain.GetBlockChainInstance().GetTxFromCache(hashEth); success {
				txE := new(types.Transaction)
				if err := txE.UnmarshalBinary(rawTx); err != nil {
					logger.Warn("failed to unmarshal transaction from cache", "hash", hashEth.Hex(), "error", err)
				} else {
					v, r, s := txE.RawSignatureValues()
					signer := types.NewCancunSigner(api.App.config.ChainId)

					from, err := types.Sender(signer, txE)
					if err != nil {
						return nil, fmt.Errorf("transaction not found Sender by hash: %v", hashEth)
					}
					return &RPCTransaction{
						Gas:                 hexutil.Uint64(0), //Ch
						GasPrice:            (*hexutil.Big)(new(big.Int).SetUint64(0)),
						GasFeeCap:           (*hexutil.Big)(new(big.Int).SetUint64(0)),
						GasTipCap:           (*hexutil.Big)(new(big.Int).SetUint64(0)),
						MaxFeePerBlobGas:    (*hexutil.Big)(new(big.Int).SetUint64(0)),
						Hash:                txE.Hash(),
						Input:               txE.Data(),
						Nonce:               hexutil.Uint64(txE.Nonce()),
						To:                  txE.To(),
						TransactionIndex:    (*hexutil.Uint64)(new(uint64)),
						Value:               (*hexutil.Big)(txE.Value()),
						Type:                hexutil.Uint64(0),
						V:                   (*hexutil.Big)(v),
						R:                   (*hexutil.Big)(r),
						S:                   (*hexutil.Big)(s),
						YParity:             nil,
						BlockHash:           (*common.Hash)(common.Hash{}.Bytes()),
						BlockNumber:         (*hexutil.Big)(new(big.Int).SetUint64(0)),
						Accesses:            nil,
						ChainID:             (*hexutil.Big)(txE.ChainId()),
						BlobVersionedHashes: nil,
						From:                from,
					}, nil
				}
			}

			return nil, fmt.Errorf("blockNumber not found by transaction hash: %v", hashEth)
		}
		hashTx = hash
	}

	hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)

	if !ok {
		return nil, fmt.Errorf("could not find block hash for block number %d", blockNumber)
	}

	// Load block from file
	var err error
	blockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(hash)
	if err != nil {
		logger.Warn("Error loading block from file:", err)
		return nil, err
	}

	txDB, _ := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), api.App.storageManager.GetStorageTransaction())
	tx, err := txDB.GetTransaction(hashTx)
	if err != nil {
		return nil, err
	}
	v, r, s := tx.RawSignatureValues()
	address := tx.ToAddress()
	if (tx.ToAddress() == common.Address{}) {
		address = crypto.CreateAddress(tx.FromAddress(), tx.GetNonce())

	}
	// Nếu tìm thấy giao dịch có hash khớp, trả về nó
	return &RPCTransaction{
		BlockHash:           (*common.Hash)(blockData.Header().Hash().Bytes()),
		BlockNumber:         (*hexutil.Big)(new(big.Int).SetUint64(blockData.Header().BlockNumber())),
		From:                tx.FromAddress(),
		Gas:                 hexutil.Uint64(0), //Ch
		GasPrice:            nil,
		GasFeeCap:           nil,
		GasTipCap:           nil,
		MaxFeePerBlobGas:    nil,
		Hash:                tx.Hash(),
		Input:               tx.CallData().Input(),
		Nonce:               hexutil.Uint64(tx.GetNonce()),
		To:                  (*common.Address)(address.Bytes()),
		TransactionIndex:    (*hexutil.Uint64)(new(uint64)),
		Value:               (*hexutil.Big)(tx.Amount()),
		Type:                hexutil.Uint64(0),
		Accesses:            nil,
		ChainID:             (*hexutil.Big)(new(big.Int).SetUint64(tx.GetChainID())),
		BlobVersionedHashes: nil,
		V:                   (*hexutil.Big)(v),
		R:                   (*hexutil.Big)(r),
		S:                   (*hexutil.Big)(s),
		YParity:             nil,
	}, nil

}

func (api *MetaAPI) SendTransaction(ctx context.Context, args TransactionArgs) (common.Hash, error) {
	txM := &transaction.Transaction{}

	err := txM.Unmarshal(*args.Data)
	if err != nil {
		return common.Hash{}, err
	}
	// api.App.transactionPool.AddTransaction(txM)

	return txM.Hash(), nil
}

// SubmitTransaction is a helper function that submits tx to txPool and logs a message.
func SubmitTransaction(ctx context.Context, tx *types.Transaction) (common.Hash, error) {
	sg := types.NewCancunSigner(tx.ChainId())
	from, _ := sg.Sender(tx)
	if tx.To() == nil {
		addr := crypto.CreateAddress(from, tx.Nonce())
		logger.Info("submitted contract creation", "hash", tx.Hash().Hex(), "from", from, "nonce", tx.Nonce(), "contract", addr.Hex(), "value", tx.Value())
	} else {
		logger.Info("submitted transaction", "hash", tx.Hash().Hex(), "from", from, "nonce", tx.Nonce(), "recipient", tx.To(), "value", tx.Value())
	}
	return tx.Hash(), nil
}

func (api *MetaAPI) SendRawTransaction(ctx context.Context, input []byte, inputEth []byte, pubKeyBlsL []byte) (common.Hash, error) {

	txM := &transaction.Transaction{}
	err := txM.Unmarshal(input)
	if err != nil {
		// BỔ SUNG LOG
		logger.Error("Lỗi Unmarshal txM: %v", err)
		return common.Hash{}, err
	}
	if len(inputEth) > 0 {

		txEth := new(types.Transaction)

		if err := txEth.UnmarshalBinary(inputEth); err != nil {
			// BỔ SUNG LOG
			logger.Error("Lỗi UnmarshalBinary txEth: %v", err)
			return common.Hash{}, err
		}

		signer := types.NewCancunSigner(api.App.config.ChainId)

		from, err := types.Sender(signer, txEth)
		if err != nil {
			// BỔ SUNG LOG
			logger.Error("Lỗi types.Sender: %v", err)
			return common.Hash{}, err
		}
		if from != txM.FromAddress() {
			// SỬA LỖI LOGIC: Tạo lỗi mới thay vì trả về 'err' (đang là nil)
			err = fmt.Errorf("địa chỉ 'from' không khớp: txEth from %s, txM from %s", from.Hex(), txM.FromAddress().Hex())
			// BỔ SUNG LOG
			logger.Error("Lỗi không khớp địa chỉ: %v", err)
			return common.Hash{}, err
		}

		output, err := api.App.transactionProcessor.ProcessTransactionFromRpc(txM)
		if err != nil {
			// BỔ SUNG LOG
			logger.Error("Lỗi ProcessTransactionFromRpc (nhánh eth): %v", err)
			return common.Hash{}, newError(err, output)

		}
		err = blockchain.GetBlockChainInstance().SetEthHashMapblsHash(txEth.Hash(), txM.Hash())

		if err != nil {
			// BỔ SUNG LOG
			logger.Error("Lỗi SetEthHashMapblsHash: %v", err)
			return common.Hash{}, newError(err, output)

		}
		return txEth.Hash(), nil
	} else {
		output, err := api.App.transactionProcessor.ProcessTransactionFromRpc(txM)
		if err != nil {
			// BỔ SUNG LOG
			logger.Error("Lỗi ProcessTransactionFromRpc (nhánh không-eth): %v", err)
			return common.Hash{}, newError(err, output)

		}
		return txM.Hash(), nil
	}
}

func TransactionToMap(tx *types.Transaction) (map[string]interface{}, error) {
	data, err := json.Marshal(tx)
	if err != nil {
		return nil, fmt.Errorf("json encoding failed: %v", err)
	}
	var dataMap map[string]interface{}
	if err := json.Unmarshal(data, &dataMap); err != nil {
		return nil, fmt.Errorf("json unmarshalling failed: %v", err)
	}
	return dataMap, nil
}
func (api *MetaAPI) startProcessingLogger() {
	// Biến này lưu số lượng của 10 giây trước
	var lastCount int64 = 0
	fileLogger, _ := loggerfile.NewFileLogger(fmt.Sprintf("DebugThread" + ".log"))
	// Tạo một ticker 10 giây
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	logger.Info("[ProcessingMonitor] Đã khởi động logger giám sát transaction...")

	// Vòng lặp vô hạn, chạy mỗi 10 giây
	for range ticker.C {
		// Lấy số lượng hiện tại (an toàn với atomic)
		currentCount := api.processingChunkCount.Load()

		// Log thông tin cơ bản
		fileLogger.Info("[ProcessingMonitor] Giao dịch đang xử lý (đồng thời): %d", currentCount)

		// Phân tích "bị kẹt":
		// Nếu số lượng > 0 VÀ số lượng hiện tại bằng 10s trước,
		// đây là một dấu hiệu có thể có transaction bị kẹt.
		if currentCount > 0 && currentCount == lastCount {
			fileLogger.Info(
				"[ProcessingMonitor] CẢNH BÁO: Số lượng xử lý không đổi (%d). Có thể có transaction bị kẹt.",
				currentCount,
			)
		} else if currentCount > lastCount {
			fileLogger.Info(
				"[ProcessingMonitor] Tải đang tăng (từ %d -> %d)",
				lastCount, currentCount,
			)
		} else if currentCount < lastCount {
			fileLogger.Info(
				"[ProcessingMonitor] Tải đang giảm (từ %d -> %d)",
				lastCount, currentCount,
			)
		}

		// Cập nhật lastCount cho lần lặp sau
		lastCount = currentCount
	}
}
func (api *MetaAPI) SendRawTransactionWithDeviceKey(ctx context.Context, input []byte, inputEth []byte, pubKeyBlsL []byte) (common.Hash, error) {
	txD := &mt_proto.TransactionWithDeviceKey{}
	err := proto.Unmarshal(input, txD)
	if err != nil {
		return common.Hash{}, err
	}
	// api.processingChunkCount.Add(1)
	// defer api.processingChunkCount.Add(-1)
	txM := &transaction.Transaction{}
	txM.FromProto(txD.Transaction)
	if len(inputEth) > 0 {
		txEth := new(types.Transaction)
		if err := txEth.UnmarshalBinary(inputEth); err != nil {
			return common.Hash{}, err
		}
		logger.Info("ETH SendRawTransactionWithDeviceKey", txEth.Hash())

		fileHandler, _ := file_handler.GetFileAbi()
		name, _ := fileHandler.ParseMethodName(txM)
		if !(txM.ToAddress() == file_handler.PredictContractAddress(common.HexToAddress(api.App.chainState.GetConfig().OwnerFileStorageAddress)) && name == "uploadChunk") {
			blockchain.GetBlockChainInstance().AddTxToCache(txEth.Hash(), append([]byte(nil), inputEth...))
		}
		signer := types.NewCancunSigner(api.App.config.ChainId)

		from, err := types.Sender(signer, txEth)
		if err != nil {
			return common.Hash{}, err
		}
		logger.Info("2. ETH")
		if from != txM.FromAddress() {

			return common.Hash{}, fmt.Errorf("address does not match signature")
		}
		logger.Info("3. ProcessTransactionFromRpcWithDeviceKey")
		output, err := api.App.transactionProcessor.ProcessTransactionFromRpcWithDeviceKey(txD)
		if err != nil {
			return common.Hash{}, newError(err, output)

		}
		err = blockchain.GetBlockChainInstance().SetEthHashMapblsHash(txEth.Hash(), txM.Hash())
		if err != nil {
			return common.Hash{}, newError(err, output)
		}
		return txEth.Hash(), nil
	} else {
		output, err := api.App.transactionProcessor.ProcessTransactionFromRpcWithDeviceKey(txD)
		if err != nil {
			return common.Hash{}, newError(err, output)

		}
		return txM.Hash(), nil
	}
}

func (api *MetaAPI) GetSendRawTransaction(ctx context.Context, input hexutil.Bytes) (map[string]interface{}, error) {
	ethTx := new(types.Transaction)
	if err := ethTx.UnmarshalBinary(input); err != nil {
		return nil, err
	}

	sg := types.NewCancunSigner(ethTx.ChainId())
	fromAddress, err := sg.Sender(ethTx)
	if err != nil {
		return nil, fmt.Errorf("failed to get fromAddress: %w", err) // Cập nhật thông báo lỗi
	}
	lastDeviceKey := common.HexToHash(
		"0000000000000000000000000000000000000000000000000000000000000000",
	)
	newDeviceKey := common.HexToHash(
		"0000000000000000000000000000000000000000000000000000000000000000",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get account: %w", err) // Cập nhật thông báo lỗi
	}
	bRelatedAddresses := make([][]byte, 0)

	var toAddress common.Address
	var bData []byte
	if len(ethTx.Data()) > 0 && ethTx.To() == nil {
		// toAddress = common.BytesToAddress(
		// 	crypto.Keccak256(
		// 		append(
		// 			as.Address().Bytes(),
		// 			as.LastHash().Bytes()...),
		// 	)[12:],
		// )
		toAddress = common.Address{}

		deployData := transaction.NewDeployData(
			ethTx.Data(),
			common.HexToAddress("0xda7284fac5e804f8b9d71aa39310f0f86776b51d"),
		)
		bData, err = deployData.Marshal()
		if err != nil {
			return nil, fmt.Errorf("failed to create deployData : %w", err) // Cập nhật thông báo lỗi
		}
	}

	if len(ethTx.Data()) > 0 && ethTx.To() != nil {
		toAddress = common.BytesToAddress(ethTx.To().Bytes())
		callData := transaction.NewCallData(ethTx.Data())

		bData, err = callData.Marshal()
		if err != nil {
			logger.Error("GetSendRawTransaction: ", err)
		}
	}

	if len(ethTx.Data()) == 0 && ethTx.To() != nil {
		toAddress = common.BytesToAddress(ethTx.To().Bytes())
	}
	transaction := transaction.NewTransaction(
		fromAddress,
		toAddress,
		ethTx.Value(),
		ethTx.Gas(),
		ethTx.GasPrice().Uint64(),
		0,
		bData,
		bRelatedAddresses,
		lastDeviceKey,
		newDeviceKey,
		ethTx.Nonce(),
		api.App.config.ChainId.Uint64(),
	)
	account := map[string]interface{}{
		"txHash":    transaction.Hash(),
		"toAddress": transaction.ToAddress(),
	}

	return account, nil
}

// SendRawEthTransaction accepts a raw Ethereum-format transaction (the same
// payload as eth_sendRawTransaction in MetaMask), converts it locally to a
// MetaNode transaction with BLS signing, and submits it.
//
// With the async queue enabled, this method enqueues the transaction for
// background processing and returns the ETH tx hash immediately (separated
// send stream). Clients poll eth_getTransactionReceipt for the result
// (receive stream).
//
// Falls back to synchronous processing if the async queue is not available.
func (api *MetaAPI) SendRawEthTransaction(ctx context.Context, input hexutil.Bytes) (common.Hash, error) {
	// Async path: enqueue and return immediately
	if api.App.txAsyncQueue != nil {
		return api.App.txAsyncQueue.EnqueueEthTransaction(ctx, input)
	}

	// Fallback: synchronous processing (legacy path)
	return api.sendRawEthTransactionSync(ctx, input)
}

// sendRawEthTransactionSync is the original synchronous implementation, kept
// as a fallback when the async queue is not available.
func (api *MetaAPI) sendRawEthTransactionSync(ctx context.Context, input hexutil.Bytes) (common.Hash, error) {
	// 1. Decode the Ethereum TX
	ethTx := new(types.Transaction)
	if err := ethTx.UnmarshalBinary(input); err != nil {
		return common.Hash{}, fmt.Errorf("failed to decode Ethereum transaction: %w", err)
	}

	// 2. Derive sender
	signer := types.LatestSignerForChainID(api.App.config.ChainId)
	fromAddress, err := types.Sender(signer, ethTx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to derive sender: %w", err)
	}

	// 3. Determine BLS private key: per-address from key store, or node default
	var blsPrivateKey mt_common.PrivateKey
	if api.App.blsKeyStore != nil {
		exists, _ := api.App.blsKeyStore.HasPrivateKey(fromAddress)
		if exists {
			pkStr, err := api.App.blsKeyStore.GetPrivateKey(fromAddress)
			if err != nil {
				return common.Hash{}, fmt.Errorf("failed to retrieve BLS key for %s: %w", fromAddress.Hex(), err)
			}
			kp := bls.NewKeyPair(common.FromHex(pkStr))
			blsPrivateKey = kp.PrivateKey()
		} else {
			blsPrivateKey = api.App.keyPair.PrivateKey()
		}
	} else {
		blsPrivateKey = api.App.keyPair.PrivateKey()
	}

	// 4. Get latest state root for account lookup
	stateRoot := api.App.blockProcessor.GetLastBlock().Header().AccountStatesRoot()

	// 5. Build MetaTx from EthTx (in-process, no HTTP)
	metaTxData, metaTx, err := buildMetaTxFromEthTx(
		ethTx,
		api.App.config.ChainId,
		blsPrivateKey,
		stateRoot,
		api.App,
	)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to build MetaTx: %w", err)
	}

	// 6. Process the transaction
	txD := &mt_proto.TransactionWithDeviceKey{}
	if err := proto.Unmarshal(metaTxData, txD); err != nil {
		return common.Hash{}, fmt.Errorf("failed to unmarshal TransactionWithDeviceKey: %w", err)
	}

	output, err := api.App.transactionProcessor.ProcessTransactionFromRpcWithDeviceKey(txD)
	if err != nil {
		return common.Hash{}, newError(err, output)
	}

	// 7. Map ETH hash → BLS hash for receipt lookup
	if err := blockchain.GetBlockChainInstance().SetEthHashMapblsHash(ethTx.Hash(), metaTx.Hash()); err != nil {
		logger.Warn("[SendRawEthTransaction] SetEthHashMapblsHash failed: %v", err)
	}

	logger.Info("[SendRawEthTransaction] TX submitted (sync): ethHash=%s metaHash=%s from=%s",
		ethTx.Hash().Hex(), metaTx.Hash().Hex(), fromAddress.Hex())

	return ethTx.Hash(), nil
}

func swapStatusNumber(bit int32) string {
	if bit == 0 {
		return hexutil.EncodeUint64(1)
	}
	return hexutil.EncodeUint64(0)
}

// GetTransactionReceipt returns the transaction receipt for the given transaction hash.
func (api *MetaAPI) GetTransactionReceipt(ctx context.Context, hashEth common.Hash) (map[string]interface{}, error) {
	// Bước 1 & 2: Tìm block number từ hash giao dịch (bao gồm cả việc xử lý hash mapping)
	blsHash, isEthHash := blockchain.GetBlockChainInstance().GetEthHashMapblsHash(hashEth)
	searchHash := hashEth
	if isEthHash {
		searchHash = blsHash
	}

	blockNumber, ok := blockchain.GetBlockChainInstance().GetBlockNumberByTxHash(searchHash)
	if !ok {
		return nil, nil // Trả về nil nếu không tìm thấy giao dịch
	}

	// Bước 3: Lấy block hash từ block number
	blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
	if !ok {
		return nil, nil
	}

	// Bước 4: Lấy toàn bộ dữ liệu của block
	blockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
	if err != nil {
		return nil, nil
	}

	rcpDb, err := receipt.NewReceiptsFromRoot(blockData.Header().ReceiptRoot(), api.App.storageManager.GetStorageReceipt())
	if err != nil {
		return nil, nil
	}
	// typeHash := "mtnHash"
	receipt, err := rcpDb.GetReceipt(searchHash)
	if err != nil {
		return nil, nil // Trả về lỗi nếu không tìm thấy
	}
	tx, err := api.GetTransactionByHash(ctx, receipt.TransactionHash())
	if err != nil {
		return nil, nil // Trả về lỗi nếu không tìm thấy
	}
	blockNumberBigInt := tx.BlockNumber.ToInt()
	blockNumberInt64 := blockNumberBigInt.Int64()

	bl := api.GetBlockByNumber(ctx, api.convertBlockNumber(blockNumberInt64), true)

	events := receipt.EventLogs()
	logs := make([]interface{}, len(events))
	for i, logData := range events {
		topics := make([]string, len(logData.Topics)) // Tạo mảng string để lưu trữ topics đã chuyển đổi
		for j, topicBytes := range logData.Topics {
			topics[j] = fmt.Sprintf("0x%s", common.Bytes2Hex(topicBytes)) // Chuyển đổi topicBytes thành chuỗi hex
		}

		logs[i] = LogData{
			BlockNumber:      hexutil.EncodeUint64(uint64(blockNumberInt64)),
			Address:          common.BytesToAddress(logData.Address).Hex(),
			Data:             fmt.Sprintf("0x%s", common.Bytes2Hex(logData.Data)),
			TransactionHash:  hashEth.Hex(),
			BlockHash:        fmt.Sprintf("%s", bl["hash"]), // Sử dụng fmt.Sprintf để định dạng chuỗi
			Topics:           topics,
			LogIndex:         hexutil.EncodeUint64(uint64(i)),
			TransactionIndex: hexutil.EncodeUint64(0),
		}
	}

	receiptMap := map[string]interface{}{
		// "typeHash":          typeHash,
		"type":              hexutil.EncodeUint64(2),
		"status":            swapStatusNumber(int32(receipt.Status().Number())),
		"transactionHash":   hashEth,
		"gasUsed":           hexutil.EncodeUint64(receipt.GasUsed()),
		"logs":              logs,
		"logsBloom":         "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
		"transactionIndex":  hexutil.EncodeUint64(receipt.TransactionIndex()),
		"blockHash":         bl["hash"],
		"blockNumber":       hexutil.EncodeUint64(uint64(blockNumberInt64)),
		"effectiveGasPrice": hexutil.EncodeUint64(receipt.GasFee()),
		"from":              receipt.FromAddress(),
		"cumulativeGasUsed": hexutil.EncodeUint64(mt_common.BLOCK_GAS_LIMIT),
	}
	// Thêm revertReason nếu tx bị lỗi (status != RETURNED)
	if receipt.Return() != nil && len(receipt.Return()) > 0 && receipt.Status().Number() != 0 {
		receiptMap["return"] = fmt.Sprintf("0x%s", common.Bytes2Hex(receipt.Return()))
	}
	if (receipt.ToAddress() == common.Address{}) {
		toAddressDeploy := crypto.CreateAddress(receipt.FromAddress(), uint64(tx.Nonce))
		receiptMap["contractAddress"] = toAddressDeploy
	} else {
		receiptMap["to"] = receipt.ToAddress()
	}

	return receiptMap, nil
}

// GetLogs returns logs matching the given argument that are stored within the state.
func (api *MetaAPI) GetLogs(ctx context.Context, crit filters.FilterCriteria) ([]*types.Log, error) {
	if len(crit.Topics) > maxTopics {
		return nil, errExceedMaxTopics
	}

	var eventLogs []*types.Log
	var beginBlock, endBlock *big.Int

	// Xác định khoảng block
	if crit.BlockHash != nil {
		blockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(*crit.BlockHash)
		if err != nil {
			return nil, err
		}
		blockNumber := new(big.Int).SetUint64(blockData.Header().BlockNumber())
		beginBlock = blockNumber
		endBlock = blockNumber
	} else {
		lastBlockNum := api.App.blockProcessor.GetLastBlock().Header().BlockNumber()

		begin := rpc.LatestBlockNumber.Int64()
		if crit.FromBlock != nil {
			begin = crit.FromBlock.Int64()
		}
		if begin == rpc.LatestBlockNumber.Int64() {
			beginBlock = new(big.Int).SetUint64(lastBlockNum)
		} else {
			beginBlock = new(big.Int).SetInt64(begin)
		}

		end := rpc.LatestBlockNumber.Int64()
		if crit.ToBlock != nil {
			end = crit.ToBlock.Int64()
		}
		if end == rpc.LatestBlockNumber.Int64() {
			endBlock = new(big.Int).SetUint64(lastBlockNum)
		} else {
			endBlock = new(big.Int).SetInt64(end)
		}
	}

	// Kiểm tra khoảng block hợp lệ
	if beginBlock.Cmp(big.NewInt(0)) > 0 && endBlock.Cmp(big.NewInt(0)) > 0 && beginBlock.Cmp(endBlock) > 0 {
		return nil, errInvalidBlockRange
	}

	// Kiểm tra khoảng cách tối đa 10,000 block
	blockDiff := new(big.Int).Sub(endBlock, beginBlock)
	if blockDiff.Cmp(big.NewInt(limitBlockRange)) > 0 {
		return nil, fmt.Errorf("block range too large: max %d blocks allowed", limitBlockRange)
	}

	currentBlockNum := new(big.Int).Set(beginBlock)
	for currentBlockNum.Cmp(endBlock) <= 0 {

		hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(currentBlockNum.Uint64())
		if !ok {
			currentBlockNum.Add(currentBlockNum, big.NewInt(1))
			continue
		}

		blockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(hash)
		if err != nil {
			currentBlockNum.Add(currentBlockNum, big.NewInt(1))
			continue
		}

		rcpDb, err := receipt.NewReceiptsFromRoot(blockData.Header().ReceiptRoot(), api.App.storageManager.GetStorageReceipt())
		if err != nil {
			return nil, err
		}

		for _, txsHash := range blockData.Transactions() {
			receipt, err := rcpDb.GetReceipt(txsHash)
			if err != nil {
				return nil, err
			}

			events := receipt.EventLogs()

			for _, eventLog := range events {
				tx, err := api.GetTransactionByHash(ctx, common.BytesToHash(eventLog.TransactionHash))
				if err != nil {
					logger.Warn("error GetTransactionByHash ", err)
					continue
				}

				blockNumberUint64, err := utils.HexutilBigToUint64(tx.BlockNumber)
				if err != nil {
					logger.Warn("error converting block number ", err)
					continue
				}

				if blockNumberUint64 != currentBlockNum.Uint64() {
					logger.Warn("log block number mismatch", "logBlock", blockNumberUint64, "currentBlock", currentBlockNum.Uint64())
					continue
				}

				topics := make([]common.Hash, len(eventLog.Topics))
				for j, topicStr := range eventLog.Topics {
					topics[j] = common.BytesToHash(topicStr)
				}

				evL := &types.Log{
					Address:     common.BytesToAddress(eventLog.Address),
					BlockNumber: blockNumberUint64,
					Topics:      topics,
					Data:        eventLog.Data,
					TxHash:      common.BytesToHash(eventLog.TransactionHash),
					BlockHash:   hash,
				}
				eventLogs = append(eventLogs, evL)
				if len(eventLogs) > maxLogsPerRequest {
					return nil, fmt.Errorf("log result exceeds maximum of %d entries", maxLogsPerRequest)
				}
			}
		}

		currentBlockNum.Add(currentBlockNum, big.NewInt(1))
	}

	// Lọc log theo điều kiện
	matchedLogs := filters.FilterLogs(eventLogs, beginBlock, endBlock, crit.Addresses, crit.Topics)
	return matchedLogs, nil
}
