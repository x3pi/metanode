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
	eth_types "github.com/ethereum/go-ethereum/core/types"
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
	sharedmemory "github.com/meta-node-blockchain/meta-node/pkg/shared_memory"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"google.golang.org/protobuf/proto"
)

// GetTransactionByHash returns the transaction for the given hash
func (api *MetaAPI) GetTransactionByHash(ctx context.Context, hashEth common.Hash) (*RPCTransaction, error) {
	hashTx := hashEth
	if blsHash, okBls := blockchain.GetBlockChainInstance().GetEthHashMapblsHash(hashEth); okBls {
		hashTx = blsHash
	}

	blockNumber, ok := blockchain.GetBlockChainInstance().GetBlockNumberByTxHash(hashTx)

	if !ok || blockNumber > storage.GetLastBlockNumber() {
		// Fallback to cache as a pending transaction
		if rawTx, success := blockchain.GetBlockChainInstance().GetTxFromCache(hashEth); success {
			txE := new(types.Transaction)
			if err := txE.UnmarshalBinary(rawTx); err != nil {
				logger.Warn("failed to unmarshal transaction from cache", "hash", hashEth.Hex(), "error", err)
				return nil, fmt.Errorf("failed to unmarshal transaction from cache: %w", err)
			}
			v, r, s := txE.RawSignatureValues()
			signer := types.NewCancunSigner(api.App.config.ChainId)

			from, err := types.Sender(signer, txE)
			if err != nil {
				// According to Ethereum JSON-RPC specs, if a transaction is not found, it should return null, not an error.
				return nil, nil
			}
			return &RPCTransaction{
				Gas:                 hexutil.Uint64(txE.Gas()),
				GasPrice:            (*hexutil.Big)(txE.GasPrice()),
				GasFeeCap:           (*hexutil.Big)(txE.GasFeeCap()),
				GasTipCap:           (*hexutil.Big)(txE.GasTipCap()),
				Hash:                txE.Hash(),
				Input:               txE.Data(),
				Nonce:               hexutil.Uint64(txE.Nonce()),
				To:                  txE.To(),
				Value:               (*hexutil.Big)(txE.Value()),
				Type:                hexutil.Uint64(uint64(txE.Type())),
				V:                   (*hexutil.Big)(v),
				R:                   (*hexutil.Big)(r),
				S:                   (*hexutil.Big)(s),
				YParity:             nil,
				BlockHash:           nil,
				BlockNumber:         nil,
				Accesses:            nil,
				ChainID:             (*hexutil.Big)(txE.ChainId()),
				BlobVersionedHashes: nil,
				From:                from,
			}, nil
		}
		// According to Ethereum JSON-RPC specs, if a transaction is not found, it should return null, not an error.
		return nil, nil
	}

	hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)

	if !ok {
		return nil, fmt.Errorf("could not find block hash for block number %d", blockNumber)
	}

	// Load block from file
	var err error
	blockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(hash)
	if err != nil {
		logger.Error("❌ [RPC-TX] Error loading block from file: %v", err)
		return nil, nil
	}

	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), api.App.storageManager.GetStorageTransaction())
	if err != nil {
		logger.Error("❌ [RPC-TX] failed to open transaction state DB: %v", err)
		return nil, nil
	}
	tx, err := txDB.GetTransaction(hashTx)
	if err != nil {
		logger.Error("❌ [RPC-TX] failed to get transaction: %v", err)
		return nil, nil
	}
	if tx == nil {
		return nil, nil
	}
	v, r, s := tx.RawSignatureValues()
	address := tx.ToAddress()
	if (tx.ToAddress() == common.Address{}) {
		address = crypto.CreateAddress(tx.FromAddress(), tx.GetNonce())

	}

	var txIndexVal uint64
	var found bool
	for idx, tHash := range blockData.Transactions() {
		if tHash == hashTx || tHash == hashEth {
			txIndexVal = uint64(idx)
			found = true
			break
		}
	}
	if !found {
		logger.Error("❌ [RPC-TX] transaction %s not found in block %d transaction list", hashEth.Hex(), blockNumber)
		return nil, nil
	}

	ethHash := tx.Hash()
	if r != nil && s != nil && (r.Sign() != 0 || s.Sign() != 0) {
		if ethTx := tx.ToEthTransaction(); ethTx != nil {
			if h := ethTx.Hash(); h != (common.Hash{}) {
				ethHash = h
			}
		}
	} else if hashEth != (common.Hash{}) && hashEth != tx.Hash() {
		ethHash = hashEth
	}

	// Nếu tìm thấy giao dịch có hash khớp, trả về nó
	return &RPCTransaction{
		BlockHash:           (*common.Hash)(blockData.Header().Hash().Bytes()),
		BlockNumber:         (*hexutil.Big)(new(big.Int).SetUint64(blockData.Header().BlockNumber())),
		From:                tx.FromAddress(),
		Gas:                 hexutil.Uint64(tx.MaxGas()),
		GasPrice:            (*hexutil.Big)(new(big.Int).SetUint64(tx.MaxGasPrice())),
		GasFeeCap:           nil,
		GasTipCap:           nil,
		MaxFeePerBlobGas:    nil,
		Hash:                ethHash,
		Input:               tx.CallData().Input(),
		Nonce:               hexutil.Uint64(tx.GetNonce()),
		To:                  (*common.Address)(address.Bytes()),
		TransactionIndex:    (*hexutil.Uint64)(&txIndexVal),
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
	var isExistOverloaded bool
	value, exists := sharedmemory.GlobalSharedMemory.Read("pendingOverloaded")
	if exists {
		var ok bool
		isExistOverloaded, ok = value.(bool)
		if ok && isExistOverloaded {
			return common.Hash{}, fmt.Errorf("system overloaded. waiting")
		}
	}

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

// SendRawEthTransaction accepts a raw Ethereum-format transaction (the same
// payload as eth_sendRawTransaction in MetaMask), converts it locally to a
// Metanode transaction (MetaTx) by signing it with a BLS key, and submits it.
//
// If EnablePrivateGateway is true (Private Chain), it executes the transaction
// speculatively and caches a mock receipt for instant finality.
//
// Falls back to synchronous processing if the async queue is not available.
func (api *MetaAPI) SendRawEthTransaction(ctx context.Context, input hexutil.Bytes) (common.Hash, error) {
	var isExistOverloaded bool
	value, exists := sharedmemory.GlobalSharedMemory.Read("pendingOverloaded")
	if exists {
		var ok bool
		isExistOverloaded, ok = value.(bool)
		if ok && isExistOverloaded {
			return common.Hash{}, fmt.Errorf("system overloaded. waiting")
		}
	}
	
	if api.App.config.EnablePrivateGateway {
		return api.sendRawEthTransactionSpeculative(ctx, input)
	}

	// Use synchronous processing to return errors directly to client
	return api.sendRawEthTransactionSync(ctx, input)
}

// sendRawEthTransactionSpeculative performs speculative execution for the Private Gateway
func (api *MetaAPI) sendRawEthTransactionSpeculative(ctx context.Context, input hexutil.Bytes) (common.Hash, error) {
	// 1. Decode the Ethereum TX
	ethTx := new(types.Transaction)
	if err := ethTx.UnmarshalBinary(input); err != nil {
		return common.Hash{}, fmt.Errorf("failed to decode Ethereum transaction: %w", err)
	}

	// 2. Derive sender
	signer := types.LatestSignerForChainID(api.App.config.ChainId)
	_, err := types.Sender(signer, ethTx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to derive sender: %w", err)
	}

	// 3. Determine BLS private key: Use GatewayBLSKey from config
	var blsPrivateKey mt_common.PrivateKey
	if api.App.config.GatewayBLSKey != "" {
		kp := bls.NewKeyPair(common.FromHex(api.App.config.GatewayBLSKey))
		blsPrivateKey = kp.PrivateKey()
	} else {
		blsPrivateKey = api.App.keyPair.PrivateKey()
	}

	// 4. Get latest state root for account lookup
	stateRoot := api.App.blockProcessor.GetLastBlock().Header().AccountStatesRoot()

	// 5. Build MetaTx from EthTx
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


	// 10. Execute real transaction synchronously to return errors to client
	output, errRun := api.App.transactionProcessor.ProcessTransactionFromRpcWithDeviceKey(txD)
	if errRun != nil {
		return common.Hash{}, newError(errRun, output)
	}

	// Map ETH hash → BLS hash for receipt lookup
	if err := blockchain.GetBlockChainInstance().SetEthHashMapblsHash(ethTx.Hash(), metaTx.Hash()); err != nil {
		logger.Warn("[SpeculativeGateway] SetEthHashMapblsHash failed: %v", err)
	}

	logger.Info("[SpeculativeGateway] TX executed speculatively without mock receipt: ethHash=%s", ethTx.Hash().Hex())

	return ethTx.Hash(), nil
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
	if !ok || blockNumber > storage.GetLastBlockNumber() {
		return nil, nil // Trả về nil nếu không tìm thấy giao dịch hoặc chưa committed
	}

	// Bước 3: Lấy block hash từ block number
	blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
	if !ok {
		return nil, nil
	}

	// Bước 4: Lấy toàn bộ dữ liệu của block
	blockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
	if err != nil {
		logger.Error("❌ [RPC-RECEIPT] failed to get block by hash %s: %v", blockHash.Hex(), err)
		return nil, nil
	}
	if blockData == nil {
		return nil, nil
	}

	rcpDb, err := receipt.NewReceiptsFromRoot(blockData.Header().ReceiptRoot(), api.App.storageManager.GetStorageReceipt())
	if err != nil {
		logger.Error("❌ [RPC-RECEIPT] failed to open receipts DB from root: %v", err)
		return nil, nil
	}
	// typeHash := "mtnHash"
	rcp, err := rcpDb.GetReceipt(searchHash)
	if err != nil {
		if err == receipt.ErrorReceiptNotFound {
			return nil, nil
		}
		logger.Error("❌ [RPC-RECEIPT] failed to get receipt: %v", err)
		return nil, nil
	}
	tx, err := api.GetTransactionByHash(ctx, rcp.TransactionHash())
	if err != nil {
		logger.Error("❌ [RPC-RECEIPT] failed to get transaction for receipt: %v", err)
		return nil, nil
	}
	if tx == nil {
		logger.Error("❌ [RPC-RECEIPT] transaction not found for receipt: %s", rcp.TransactionHash().Hex())
		return nil, nil
	}
	blockNumberBigInt := tx.BlockNumber.ToInt()
	blockNumberInt64 := blockNumberBigInt.Int64()

	events := rcp.EventLogs()
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
			BlockHash:        blockHash.Hex(),
			Topics:           topics,
			LogIndex:         hexutil.EncodeUint64(uint64(i)),
			TransactionIndex: hexutil.EncodeUint64(0),
		}
	}

	receiptMap := map[string]interface{}{
		// "typeHash":          typeHash,
		"type":              hexutil.EncodeUint64(2),
		"status":            swapStatusNumber(int32(rcp.Status().Number())),
		"transactionHash":   hashEth,
		"gasUsed":           hexutil.EncodeUint64(rcp.GasUsed()),
		"logs":              logs,
		"logsBloom":         types.Bloom{},
		"transactionIndex":  hexutil.EncodeUint64(rcp.TransactionIndex()),
		"groupIndex":        hexutil.EncodeUint64(rcp.GroupIndex()), // Debug: deterministic group order
		"blockHash":         blockHash,
		"blockNumber":       hexutil.EncodeUint64(uint64(blockNumberInt64)),
		"effectiveGasPrice": hexutil.EncodeUint64(rcp.GasFee()),
		"from":              rcp.FromAddress(),
		"cumulativeGasUsed": hexutil.EncodeUint64(mt_common.BLOCK_GAS_LIMIT),
	}
	// Thêm revertReason nếu tx bị lỗi (status != RETURNED)
	if rcp.Return() != nil && len(rcp.Return()) > 0 && rcp.Status().Number() != 0 {
		receiptMap["return"] = fmt.Sprintf("0x%s", common.Bytes2Hex(rcp.Return()))
	}
	if (rcp.ToAddress() == common.Address{}) {
		toAddressDeploy := crypto.CreateAddress(rcp.FromAddress(), uint64(tx.Nonce))
		receiptMap["contractAddress"] = toAddressDeploy
	} else {
		receiptMap["to"] = rcp.ToAddress()
	}

	return receiptMap, nil
}

// checkBloomFilter checks if a bloom filter might contain the given addresses and topics.
// If it returns false, we are 100% sure the block does not contain matching logs.
func checkBloomFilter(bloom eth_types.Bloom, addresses []common.Address, topics [][]common.Hash) bool {
	if len(addresses) > 0 {
		var match bool
		for _, addr := range addresses {
			if eth_types.BloomLookup(bloom, addr) {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}

	for _, sub := range topics {
		if len(sub) == 0 {
			continue // empty rule set == wildcard
		}
		var match bool
		for _, topic := range sub {
			if eth_types.BloomLookup(bloom, topic) {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	return true
}

// GetLogs returns logs matching the given argument that are stored within the state.
func (api *MetaAPI) GetLogs(ctx context.Context, crit filters.FilterCriteria) ([]*types.Log, error) {
	if len(crit.Topics) > maxTopics {
		return nil, errExceedMaxTopics
	}

	eventLogs := make([]*types.Log, 0)
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
		lastBlockNum := storage.GetLastBlockNumber()

		begin := rpc.LatestBlockNumber.Int64()
		if crit.FromBlock != nil {
			begin = crit.FromBlock.Int64()
		}
		if begin == rpc.LatestBlockNumber.Int64() || begin == rpc.PendingBlockNumber.Int64() || begin == rpc.FinalizedBlockNumber.Int64() || begin == rpc.SafeBlockNumber.Int64() {
			beginBlock = new(big.Int).SetUint64(lastBlockNum)
		} else {
			beginBlock = new(big.Int).SetInt64(begin)
		}

		end := rpc.LatestBlockNumber.Int64()
		if crit.ToBlock != nil {
			end = crit.ToBlock.Int64()
		}
		if end == rpc.LatestBlockNumber.Int64() || end == rpc.PendingBlockNumber.Int64() || end == rpc.FinalizedBlockNumber.Int64() || end == rpc.SafeBlockNumber.Int64() {
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

		if len(blockData.Transactions()) == 0 {
			currentBlockNum.Add(currentBlockNum, big.NewInt(1))
			continue
		}

		// 🚀 OPTIMIZATION: Check Bloom Filter before opening ReceiptTrie
		// If the block header has a valid LogsBloom, we can skip decoding the receipts
		// if the Bloom filter definitively says there's no match.
		if len(blockData.Header().LogsBloom()) > 0 {
			bloom := eth_types.BytesToBloom(blockData.Header().LogsBloom())
			if !checkBloomFilter(bloom, crit.Addresses, crit.Topics) {
				currentBlockNum.Add(currentBlockNum, big.NewInt(1))
				continue
			}
		}

		rcpDb, err := receipt.NewReceiptsFromRoot(blockData.Header().ReceiptRoot(), api.App.storageManager.GetStorageReceipt())
		if err != nil {
			return nil, err
		}

		logIndex := uint(0)
		for txIndex, txsHash := range blockData.Transactions() {
			receipt, err := rcpDb.GetReceipt(txsHash)
			if err != nil {
				return nil, err
			}

			events := receipt.EventLogs()

			for _, eventLog := range events {
				topics := make([]common.Hash, len(eventLog.Topics))
				for j, topicStr := range eventLog.Topics {
					topics[j] = common.BytesToHash(topicStr)
				}

				evL := &types.Log{
					Address:     common.BytesToAddress(eventLog.Address),
					BlockNumber: currentBlockNum.Uint64(),
					Topics:      topics,
					Data:        eventLog.Data,
					TxHash:      common.BytesToHash(eventLog.TransactionHash),
					BlockHash:   hash,
					TxIndex:     uint(txIndex),
					Index:       logIndex,
				}
				
				// 🚀 OPTIMIZATION: Lọc (Early Filtering) ngay tại đây thay vì dồn hết vào mảng rồi mới lọc
				if len(filters.FilterLogs([]*types.Log{evL}, beginBlock, endBlock, crit.Addresses, crit.Topics)) > 0 {
					eventLogs = append(eventLogs, evL)
					if len(eventLogs) > maxLogsPerRequest {
						return nil, fmt.Errorf("log result exceeds maximum of %d entries", maxLogsPerRequest)
					}
				}
				logIndex++
			}
		}

		currentBlockNum.Add(currentBlockNum, big.NewInt(1))
	}

	// Lọc log theo điều kiện
	matchedLogs := filters.FilterLogs(eventLogs, beginBlock, endBlock, crit.Addresses, crit.Topics)
	if matchedLogs == nil {
		matchedLogs = make([]*types.Log, 0)
	}
	return matchedLogs, nil
}
