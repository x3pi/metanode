package main

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/common/math"
	eth_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// MarshalBlockToMap converts a mt_types.Block to a map[string]interface{}.
func MarshalBlockToMap(block mt_types.Block, fullTx bool, fetchTx func(common.Hash) (mt_types.Transaction, error), storageReceipt storage.Storage) (map[string]interface{}, error) {
	// Create a map to hold the block data.
	blockMap := make(map[string]interface{})
	// note có thể metamask dùng hai trường blockHash blockNumber để ánh xạ vơi recipte
	blockMap["hash"] = block.Header().Hash()
	blockMap["number"] = hexutil.EncodeUint64(block.Header().BlockNumber())
	blockMap["sha3Uncles"] = common.Hash{}
	blockMap["miner"] = block.Header().LeaderAddress()
	blockMap["parentHash"] = block.Header().LastBlockHash()                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      // Hash của khối cha
	blockMap["stateRoot"] = block.Header().AccountStatesRoot()                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   // Root của Merkle Patricia Trie chứa trạng thái tài khoản
	blockMap["receiptsRoot"] = block.Header().ReceiptRoot()                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      // Root của Merkle Patricia Trie chứa receipts của các giao dịch
	blockMap["transactionsRoot"] = block.Header().TransactionsRoot()                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             // Root của Merkle Patricia Trie chứa các giao dịch
	blockMap["logsBloom"] = eth_types.Bloom{} // Bloom filter chứa thông tin về logs
	blockMap["difficulty"] = hexutil.EncodeUint64(0)
	blockMap["gasLimit"] = hexutil.EncodeUint64(0)                           // Giới hạn gas của khối
	blockMap["gasUsed"] = hexutil.EncodeUint64(0)                            // Gas đã sử dụng trong khối
	blockMap["timestamp"] = hexutil.EncodeUint64(block.Header().TimeStamp()) // Thời gian tạo khối
	blockMap["extraData"] = "0x"                                             // Dữ liệu bổ sung
	blockMap["mixHash"] = common.Hash{}                                      // Hash của proof-of-work
	blockMap["nonce"] = "0x0000000000000000"                                 // Nonce của khối
	blockMap["baseFeePerGas"] = hexutil.EncodeUint64(0)                      // Phí cơ bản trên mỗi gas (EIP-1559)
	blockMap["withdrawalsRoot"] = trie.EmptyRootHash                         // Root của Merkle Patricia Trie chứa các giao dịch rút tiền (EIP-3675)
	blockMap["blobGasUsed"] = hexutil.EncodeUint64(0)                        // Gas đã sử dụng cho blobs (EIP-4844)
	blockMap["excessBlobGas"] = hexutil.EncodeUint64(0)                      // Gas dư thừa cho blobs (EIP-4844)
	blockMap["parentBeaconBlockRoot"] = common.Hash{}                        // Root của khối beacon cha (trong trường hợp sharding)
	blockMap["totalDifficulty"] = hexutil.EncodeUint64(0)                    // Tổng độ khó của chuỗi cho đến khối này

	// ═══════════════════════════════════════════════════════════════════════
	// CUSTOM FIELDS: Not part of ETH standard, but included for debugging.
	// ETH clients ignore unknown fields, so this is backward-compatible.
	// These fields are part of block hash calculation and critical for
	// diagnosing fork issues where standard fields match but hash differs.
	// ═══════════════════════════════════════════════════════════════════════
	blockMap["globalExecIndex"] = hexutil.EncodeUint64(block.Header().GlobalExecIndex()) // Maps Go block → Rust consensus commit index
	blockMap["commitIndex"] = hexutil.EncodeUint64(block.Header().CommitIndex())         // Rust consensus commit index
	blockMap["stakeStatesRoot"] = block.Header().StakeStatesRoot()                       // Root của Merkle trie chứa trạng thái stake
	blockMap["epoch"] = hexutil.EncodeUint64(block.Header().Epoch())                     // Epoch của khối
	blockMap["leaderAddress"] = block.Header().LeaderAddress().Hex()                     // Địa chỉ validator tạo khối
	blockMap["aggregateSignature"] = hexutil.Encode(block.Header().AggregateSignature()) // Chữ ký gộp của block

	// Add transactions to the map.
	txHashes := block.Transactions()
	transactions := make([]interface{}, 0, len(txHashes))
	if !fullTx {
		for _, txHash := range txHashes {
			transactions = append(transactions, txHash.String())
		}
		blockMap["transactions"] = transactions
		return blockMap, nil
	}

	if fetchTx == nil {
		return nil, fmt.Errorf("fetchTx is nil while fullTx is requested")
	}

	// ═══════════════════════════════════════════════════════════════════════
	// GROUP INFO FROM STORED RECEIPTS: Read GroupIndex/TransactionIndex that were
	// stamped during block processing — no need to re-run GroupTransactionsDeterministic.
	// ═══════════════════════════════════════════════════════════════════════
	type txGroupInfo struct {
		groupIndex       uint64
		transactionIndex uint64
	}
	groupInfoMap := make(map[common.Hash]txGroupInfo, len(txHashes))

	// Open the receipt trie for this block (read-only, no new trie is created)
	rcpDb, rcpErr := receipt.NewReceiptsFromRoot(block.Header().ReceiptRoot(), storageReceipt)
	if rcpErr != nil {
		logger.Error("❌ [RPC-BLOCK] Cannot open receipt trie for block #%d: %v", block.Header().BlockNumber(), rcpErr)
		return nil, fmt.Errorf("cannot open receipt trie for block %d: %w", block.Header().BlockNumber(), rcpErr)
	}

	for _, txHash := range txHashes {
		rcp, err := rcpDb.GetReceipt(txHash)
		if err != nil {
			logger.Error("❌ [RPC-BLOCK] Receipt not found for tx %s in block #%d", txHash.Hex(), block.Header().BlockNumber())
			return nil, fmt.Errorf("receipt not found for tx %s: %w", txHash.Hex(), err)
		}
		groupInfoMap[txHash] = txGroupInfo{
			groupIndex:       rcp.GroupIndex(),
			transactionIndex: rcp.TransactionIndex(),
		}
		// 🔍 DIAGNOSTIC: Log GroupIndex/TransactionIndex from receipt trie (helps debug m4 mismatch)
		logger.Info("📋 [RPC-RECEIPT-IDX] Block #%d tx=%s...→ groupId=%d, txIndex=%d",
			block.Header().BlockNumber(), txHash.Hex()[:18], rcp.GroupIndex(), rcp.TransactionIndex())
	}

	for _, txHash := range txHashes {
		tx, err := fetchTx(txHash)
		if err != nil {
			return nil, err
		}
		txMap := make(map[string]interface{})
		v, r, s := tx.RawSignatureValues()
		txMap["hash"] = tx.Hash()
		txMap["from"] = tx.FromAddress()                           // Địa chỉ người gửi
		txMap["to"] = tx.ToAddress()                               // Địa chỉ người nhận
		txMap["value"] = (*hexutil.Big)(tx.Amount())               // Số lượng tiền được chuyển
		txMap["input"] = hexutil.Bytes(tx.CallData().Input())      // Dữ liệu đầu vào của giao dịch (data)
		txMap["nonce"] = hexutil.EncodeUint64(tx.GetNonce())       // Nonce của giao dịch
		txMap["gas"] = hexutil.EncodeUint64(tx.MaxGas())           // Giới hạn gas của giao dịch
		txMap["gasPrice"] = hexutil.EncodeUint64(tx.MaxGasPrice()) // Giá gas của giao dịch
		txMap["chainId"] = hexutil.EncodeUint64(tx.GetChainID())   // ID của chuỗi
		txMap["v"] = (*hexutil.Big)(v)                             // Giá trị V trong chữ ký
		txMap["r"] = (*hexutil.Big)(r)                             // Giá trị R trong chữ ký
		txMap["s"] = (*hexutil.Big)(s)                             // Giá trị S trong chữ ký

		// Add grouping info from stored receipts (stamped during block processing)
		if info, exists := groupInfoMap[tx.Hash()]; exists {
			txMap["groupId"] = hexutil.EncodeUint64(info.groupIndex)
			txMap["transactionIndex"] = hexutil.EncodeUint64(info.transactionIndex)
		}

		transactions = append(transactions, txMap)
	}
	blockMap["transactions"] = transactions // Mảng các giao dịch trong khối

	return blockMap, nil
}

// GetBlockByNumber returns the requested canonical block.
//   - When blockNr is -1 the chain pending block is returned.
//   - When blockNr is -2 the chain latest block is returned.
//   - When blockNr is -3 the chain finalized block is returned.
//   - When blockNr is -4 the chain safe block is returned.
//   - When fullTx is true all transactions in the block are returned, otherwise
//     only the transaction hash is returned.
func (api *MetaAPI) GetBlockByNumber(ctx context.Context, number rpc.BlockNumber, fullTx bool) (map[string]interface{}, error) {
	var blockData mt_types.Block // Corrected type
	if number == rpc.LatestBlockNumber {
		lastBlockNum := storage.GetLastBlockNumber()
		if lastBlockNum > 0 {
			hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(lastBlockNum)
			if ok {
				blockData = blockchain.GetBlockChainInstance().GetBlock(hash)
			}
		}
		if blockData == nil {
			blockData = api.App.blockProcessor.GetLastBlock() // Correctly assign lastBlock fallback
		}
	} else {
		if number.Int64() >= 0 && uint64(number.Int64()) > storage.GetLastBlockNumber() {
			return nil, nil
		}
		hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(uint64(number.Int64()))
		if !ok {
			return nil, nil
		}
		blockData = blockchain.GetBlockChainInstance().GetBlock(hash)
	}

	if blockData == nil {
		return nil, nil
	}
	if blockData.Header().BlockNumber() > storage.GetLastBlockNumber() {
		return nil, nil
	}
	var fetchTx func(common.Hash) (mt_types.Transaction, error)
	if fullTx {
		if len(blockData.Transactions()) == 0 {
			fetchTx = func(hash common.Hash) (mt_types.Transaction, error) {
				return nil, fmt.Errorf("no transactions in block")
			}
		} else {
			txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), api.App.storageManager.GetStorageTransaction())
			if err != nil {
				logger.Warn("⚠️ [RPC] Failed to open transaction state DB from root %s: %v", blockData.Header().TransactionsRoot().Hex(), err)
				return nil, fmt.Errorf("failed to open transaction state DB: %w", err)
			}
			fetchTx = func(hash common.Hash) (mt_types.Transaction, error) {
				return txDB.GetTransaction(hash)
			}
		}
	}

	blockMap, err := MarshalBlockToMap(blockData, fullTx, fetchTx, api.App.storageManager.GetStorageReceipt())
	if err != nil {
		logger.Error("❌ [RPC-BLOCK] Error generating block map for block %d: %v", blockData.Header().BlockNumber(), err)
		return nil, nil
	}

	return blockMap, nil
}

// decodeBCSSystemTransaction decodes a BCS-encoded SystemTransaction from Rust into a
// human-readable map. BCS layout for SystemTransaction:
//
//	struct SystemTransaction { kind: SystemTransactionKind }
//	enum SystemTransactionKind {
//	  EndOfEpoch { new_epoch: u64, boundary_block: u64 },       // variant 0
//	  EpochBoundary { epoch: u64, epoch_start_timestamp_ms: u64,
//	                  boundary_block: u64, validators: Vec<...> }, // variant 1
//	}
//
// BCS uses ULEB128 for enum variant index, little-endian for integers.
func decodeBCSSystemTransaction(data []byte) map[string]interface{} {
	if len(data) < 1 {
		return map[string]interface{}{"raw": hexutil.Encode(data), "error": "empty data"}
	}

	// Read variant index (ULEB128 — for small values it's just 1 byte)
	variant := data[0]
	rest := data[1:]

	readU64 := func(b []byte) (uint64, []byte) {
		if len(b) < 8 {
			return 0, b
		}
		v := uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
			uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
		return v, b[8:]
	}

	switch variant {
	case 0: // EndOfEpoch
		if len(rest) < 16 {
			return map[string]interface{}{"type": "EndOfEpoch", "raw": hexutil.Encode(data), "error": "truncated"}
		}
		newEpoch, rest2 := readU64(rest)
		boundaryBlock, _ := readU64(rest2)
		return map[string]interface{}{
			"type":           "EndOfEpoch",
			"new_epoch":      newEpoch,
			"boundary_block": boundaryBlock,
		}
	case 1: // EpochBoundary
		if len(rest) < 24 {
			return map[string]interface{}{"type": "EpochBoundary", "raw": hexutil.Encode(data), "error": "truncated"}
		}
		epoch, rest2 := readU64(rest)
		epochStartTs, rest3 := readU64(rest2)
		boundaryBlock, rest4 := readU64(rest3)
		// Read validator count (ULEB128)
		validatorCount := uint64(0)
		if len(rest4) > 0 {
			// Simple ULEB128 decode (handles up to 64-bit)
			shift := uint(0)
			for i := 0; i < len(rest4) && i < 10; i++ {
				b := rest4[i]
				validatorCount |= uint64(b&0x7f) << shift
				if b&0x80 == 0 {
					break
				}
				shift += 7
			}
		}
		return map[string]interface{}{
			"type":                     "EpochBoundary",
			"epoch":                    epoch,
			"epoch_start_timestamp_ms": epochStartTs,
			"boundary_block":           boundaryBlock,
			"validator_count":          validatorCount,
		}
	default:
		return map[string]interface{}{"type": fmt.Sprintf("Unknown(%d)", variant), "raw": hexutil.Encode(data)}
	}
}

// GetSystemTransactionsByBlockNumber returns the system transactions for a given block number.
// Returns decoded, human-readable fields instead of raw BCS byte code.
func (api *MetaAPI) GetSystemTransactionsByBlockNumber(ctx context.Context, number rpc.BlockNumber) []map[string]interface{} {
	var blockNum uint64
	if number == rpc.LatestBlockNumber {
		blockNum = storage.GetLastBlockNumber()
	} else {
		if number.Int64() >= 0 && uint64(number.Int64()) > storage.GetLastBlockNumber() {
			return []map[string]interface{}{}
		}
		blockNum = uint64(number.Int64())
	}

	sysTxs, err := api.App.chainState.GetBlockDatabase().GetSystemTransactions(blockNum)
	if err != nil || len(sysTxs) == 0 {
		return []map[string]interface{}{}
	}

	result := make([]map[string]interface{}, 0, len(sysTxs))
	for _, txBytes := range sysTxs {
		decoded := decodeBCSSystemTransaction(txBytes)
		decoded["raw_hex"] = hexutil.Encode(txBytes)
		decoded["block_number"] = blockNum
		result = append(result, decoded)
	}
	return result
}

// GetBlockByHash returns the requested block. When fullTx is true all transactions in the block are returned in full
// detail, otherwise only the transaction hash is returned.
func (api *MetaAPI) GetBlockByHash(ctx context.Context, hash common.Hash, fullTx bool) (map[string]interface{}, error) {
	blockData := blockchain.GetBlockChainInstance().GetBlock(hash)
	if blockData == nil {
		return nil, nil // Return nil, nil if block not found
	}
	if blockData.Header().BlockNumber() > storage.GetLastBlockNumber() {
		return nil, nil
	}

	var fetchTx func(common.Hash) (mt_types.Transaction, error)
	if fullTx {
		if len(blockData.Transactions()) == 0 {
			fetchTx = func(hash common.Hash) (mt_types.Transaction, error) {
				return nil, fmt.Errorf("no transactions in block")
			}
		} else {
			txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), api.App.storageManager.GetStorageTransaction())
			if err != nil {
				logger.Warn("⚠️ [RPC] Failed to open transaction state DB from root %s: %v", blockData.Header().TransactionsRoot().Hex(), err)
				return nil, fmt.Errorf("failed to open transaction state DB: %w", err)
			}
			fetchTx = func(hash common.Hash) (mt_types.Transaction, error) {
				return txDB.GetTransaction(hash)
			}
		}
	}

	blockMap, err := MarshalBlockToMap(blockData, fullTx, fetchTx, api.App.storageManager.GetStorageReceipt())
	if err != nil {
		logger.Error("❌ [RPC-BLOCK] Error generating block map for hash %s: %v", hash.Hex(), err)
		return nil, nil
	}
	return blockMap, nil
}

func (api *MetaAPI) BlockNumber() string {
	return hexutil.EncodeUint64(storage.GetLastBlockNumber())
}

// GetTransactionByBlockNumberAndIndex returns the transaction for the given block number and index.
func (api *MetaAPI) GetTransactionByBlockNumberAndIndex(ctx context.Context, blockNr rpc.BlockNumber, index hexutil.Uint) (*RPCTransaction, error) {
	var blockData mt_types.Block // Corrected type
	if blockNr == rpc.LatestBlockNumber {
		lastBlockNum := storage.GetLastBlockNumber()
		if lastBlockNum > 0 {
			hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(lastBlockNum)
			if ok {
				blockData = blockchain.GetBlockChainInstance().GetBlock(hash)
			}
		}
		if blockData == nil {
			blockData = api.App.blockProcessor.GetLastBlock()
		}
	} else {
		if blockNr.Int64() >= 0 && uint64(blockNr.Int64()) > storage.GetLastBlockNumber() {
			return nil, nil
		}
		hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(uint64(blockNr.Int64()))
		if !ok {
			return nil, nil
		}
		blockData = blockchain.GetBlockChainInstance().GetBlock(hash)
	}

	if blockData == nil {
		return nil, nil
	}
	if blockData.Header().BlockNumber() > storage.GetLastBlockNumber() {
		return nil, nil
	}

	indexInt := int(index)
	if indexInt < 0 || indexInt >= len(blockData.Transactions()) {
		return nil, nil
	}
	txHash := blockData.Transactions()[index]
	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), api.App.storageManager.GetStorageTransaction())
	if err != nil {
		logger.Error("❌ [RPC-TX] failed to open transaction state DB: %v", err)
		return nil, nil
	}

	tx, err := txDB.GetTransaction(txHash)
	if err != nil {
		logger.Error("❌ [RPC-TX] failed to get transaction: %v", err)
		return nil, nil
	}
	if tx == nil {
		return nil, nil
	}
	v, r, s := tx.RawSignatureValues()

	txIndexVal := uint64(index)
	return &RPCTransaction{
		BlockHash:           (*common.Hash)(blockData.Header().Hash().Bytes()),
		BlockNumber:         (*hexutil.Big)(new(big.Int).SetUint64(blockData.Header().BlockNumber())),
		From:                tx.FromAddress(),
		Gas:                 hexutil.Uint64(tx.MaxGas()),
		GasPrice:            (*hexutil.Big)(new(big.Int).SetUint64(tx.MaxGasPrice())),
		GasFeeCap:           nil,
		GasTipCap:           nil,
		MaxFeePerBlobGas:    nil,
		Hash:                tx.Hash(),
		Input:               tx.CallData().Input(),
		Nonce:               hexutil.Uint64(tx.GetNonce()),
		To:                  (*common.Address)(tx.ToAddress().Bytes()),
		TransactionIndex:    (*hexutil.Uint64)(&txIndexVal),
		Value:               (*hexutil.Big)(tx.Amount()),
		Type:                hexutil.Uint64(0),
		Accesses:            nil,
		ChainID:             (*hexutil.Big)(new(big.Int).SetUint64(tx.GetChainID())), // Chuyển đổi uint64 thành *hexutil.Big
		BlobVersionedHashes: nil,
		V:                   (*hexutil.Big)(v),
		R:                   (*hexutil.Big)(r),
		S:                   (*hexutil.Big)(s),
		YParity:             nil,
	}, nil
}

// GetTransactionByBlockHashAndIndex returns the transaction for the given block hash and index.
func (api *MetaAPI) GetTransactionByBlockHashAndIndex(ctx context.Context, blockHash common.Hash, index hexutil.Uint) (*RPCTransaction, error) {

	blockData := blockchain.GetBlockChainInstance().GetBlock(blockHash)
	if blockData == nil {
		logger.Warn("Error loading block from cache/file: not found for hash", blockHash)
		return nil, nil
	}
	if blockData.Header().BlockNumber() > storage.GetLastBlockNumber() {
		return nil, nil
	}

	indexInt := int(index)
	if indexInt < 0 || indexInt >= len(blockData.Transactions()) {
		return nil, nil
	}
	txHash := blockData.Transactions()[index]
	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), api.App.storageManager.GetStorageTransaction())
	if err != nil {
		logger.Error("❌ [RPC-TX] failed to open transaction state DB: %v", err)
		return nil, nil
	}

	tx, err := txDB.GetTransaction(txHash)
	if err != nil {
		logger.Error("❌ [RPC-TX] failed to get transaction: %v", err)
		return nil, nil
	}
	if tx == nil {
		return nil, nil
	}
	v, r, s := tx.RawSignatureValues()

	txIndexVal := uint64(index)
	return &RPCTransaction{
		BlockHash:           (*common.Hash)(blockData.Header().Hash().Bytes()),
		BlockNumber:         (*hexutil.Big)(new(big.Int).SetUint64(blockData.Header().BlockNumber())),
		From:                tx.FromAddress(),
		Gas:                 hexutil.Uint64(tx.MaxGas()),
		GasPrice:            (*hexutil.Big)(new(big.Int).SetUint64(tx.MaxGasPrice())),
		GasFeeCap:           nil,
		GasTipCap:           nil,
		MaxFeePerBlobGas:    nil,
		Hash:                tx.Hash(),
		Input:               tx.CallData().Input(),
		Nonce:               hexutil.Uint64(tx.GetNonce()),
		To:                  (*common.Address)(tx.ToAddress().Bytes()),
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

// GetBlockTransactionCountByNumber returns the number of transactions in the block with the given block number.
func (api *MetaAPI) GetBlockTransactionCountByNumber(ctx context.Context, blockNr rpc.BlockNumber) *hexutil.Uint {
	var blockData mt_types.Block // Corrected type
	if blockNr == rpc.LatestBlockNumber {
		lastBlockNum := storage.GetLastBlockNumber()
		if lastBlockNum > 0 {
			hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(lastBlockNum)
			if ok {
				blockData = blockchain.GetBlockChainInstance().GetBlock(hash)
			}
		}
		if blockData == nil {
			blockData = api.App.blockProcessor.GetLastBlock()
		}
	} else {
		if blockNr.Int64() >= 0 && uint64(blockNr.Int64()) > storage.GetLastBlockNumber() {
			return nil
		}
		hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(uint64(blockNr.Int64()))
		if !ok {
			return nil
		}
		blockData = blockchain.GetBlockChainInstance().GetBlock(hash)
	}

	if blockData == nil {
		return nil
	}
	if blockData.Header().BlockNumber() > storage.GetLastBlockNumber() {
		return nil
	}

	n := hexutil.Uint(len(blockData.Transactions()))

	return &n
}

// GetBlockTransactionCountByHash returns the number of transactions in the block with the given hash.
func (api *MetaAPI) GetBlockTransactionCountByHash(ctx context.Context, blockHash common.Hash) *hexutil.Uint {
	blockData := blockchain.GetBlockChainInstance().GetBlock(blockHash)
	if blockData == nil {
		logger.Warn("Error loading block from cache/file: not found for hash", blockHash)
		return nil // Return nil if there's an error
	}
	if blockData.Header().BlockNumber() > storage.GetLastBlockNumber() {
		return nil
	}
	n := hexutil.Uint(len(blockData.Transactions()))

	return &n
}

// GetRawTransactionByBlockNumberAndIndex returns the bytes of the transaction for the given block number and index.
func (api *MetaAPI) GetRawTransactionByBlockNumberAndIndex(ctx context.Context, blockNr rpc.BlockNumber, index hexutil.Uint) hexutil.Bytes {
	var blockData mt_types.Block // Corrected type
	if blockNr == rpc.LatestBlockNumber {
		lastBlockNum := storage.GetLastBlockNumber()
		if lastBlockNum > 0 {
			hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(lastBlockNum)
			if ok {
				blockData = blockchain.GetBlockChainInstance().GetBlock(hash)
			}
		}
		if blockData == nil {
			blockData = api.App.blockProcessor.GetLastBlock()
		}
	} else {
		if blockNr.Int64() >= 0 && uint64(blockNr.Int64()) > storage.GetLastBlockNumber() {
			return nil
		}
		hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(uint64(blockNr.Int64()))
		if !ok {
			return nil
		}
		blockData = blockchain.GetBlockChainInstance().GetBlock(hash)
	}

	if blockData == nil {
		return nil
	}
	if blockData.Header().BlockNumber() > storage.GetLastBlockNumber() {
		return nil
	}

	indexInt := int(index)
	if indexInt < 0 || indexInt >= len(blockData.Transactions()) {
		return nil
	}
	b, err := blockData.Marshal()
	if err != nil {
		logger.Warn("Error Marshal block:", err)
		return nil
	}
	return b
}

// GetRawTransactionByBlockHashAndIndex returns the bytes of the transaction for the given block hash and index.
func (api *MetaAPI) GetRawTransactionByBlockHashAndIndex(ctx context.Context, blockHash common.Hash, index hexutil.Uint) hexutil.Bytes {
	blockData := blockchain.GetBlockChainInstance().GetBlock(blockHash)
	if blockData == nil {
		logger.Warn("Error loading block from cache/file: not found for hash", blockHash)
		return nil // Return nil if there's an error
	}
	if blockData.Header().BlockNumber() > storage.GetLastBlockNumber() {
		return nil
	}

	indexInt := int(index)

	if indexInt < 0 || indexInt >= len(blockData.Transactions()) {
		return nil
	}
	b, err := blockData.Marshal()
	if err != nil {
		logger.Warn("Error Marshal block:", err)
		return nil
	}
	return b

}

// EstimateGas returns the lowest possible gas limit that allows the transaction to run
// successfully at block `blockNrOrHash`, or the latest block if `blockNrOrHash` is unspecified. It
// returns error if the transaction would revert or if there are unexpected failures. The returned
// value is capped by both `args.Gas` (if non-nil & non-zero) and the backend's RPCGasCap
// configuration (if non-zero).
// Note: Required blob gas is not computed in this method.
func (api *MetaAPI) EstimateGas(ctx context.Context, input hexutil.Bytes) (hexutil.Uint64, error) {
	txM := &transaction.Transaction{}
	err := txM.Unmarshal(input)
	if err != nil {
		logger.Warn("Error Unmarshal input:", err)
		return 0, err
	}
	rs, err := api.App.transactionProcessor.ProcessTransactionOffChain(txM)

	if err != nil {
		logger.Warn("Error Unmarshal input:", err)

		return 0, err
	}
	if rs == nil {
		return hexutil.Uint64(mt_common.MINIMUM_BASE_FEE), nil

	}
	return hexutil.Uint64(rs.GasUsed() + mt_common.MINIMUM_BASE_FEE), nil
}

func (api *MetaAPI) MaxPriorityFeePerGas(ctx context.Context) (*hexutil.Big, error) {
	return api.cachedMaxPriorityFee, nil
}

func (api *MetaAPI) convertBlockNumber(blockNr int64) rpc.BlockNumber {
	blockNumber := storage.GetLastBlockNumber()

	if blockNr < 0 {
		return rpc.BlockNumber(blockNumber)
	} else if blockNr == 0 {
		return rpc.BlockNumber(1)
	} else {
		return rpc.BlockNumber(blockNr)
	}
}

// GasPrice returns a suggestion for a gas price for legacy transactions.
func (api *MetaAPI) GasPrice(ctx context.Context) (*hexutil.Big, error) {
	return api.cachedGasPrice, nil
}

type feeHistoryResult struct {
	OldestBlock      *hexutil.Big     `json:"oldestBlock"`
	Reward           [][]*hexutil.Big `json:"reward,omitempty"`
	BaseFee          []*hexutil.Big   `json:"baseFeePerGas,omitempty"`
	GasUsedRatio     []float64        `json:"gasUsedRatio"`
	BlobBaseFee      []*hexutil.Big   `json:"baseFeePerBlobGas,omitempty"`
	BlobGasUsedRatio []float64        `json:"blobGasUsedRatio,omitempty"`
}

// Giả sử bạn có một hàm để lấy số khối từ lastBlock
func getOldestBlock(ctx context.Context, lastBlock rpc.BlockNumber) *big.Int {

	// Đây chỉ là một trình giữ chỗ. Bạn cần thay thế nó bằng logic thực tế
	// để lấy số khối từ lastBlock. Điều này có thể liên quan đến việc truy vấn
	// một cơ sở dữ liệu hoặc sử dụng một số API khác.
	// Ví dụ: nếu lastBlock là "latest", bạn có thể truy vấn số khối mới nhất.
	// Nếu lastBlock là một số cụ thể, bạn có thể sử dụng số đó.
	// Ở đây, chúng ta chỉ trả về một giá trị được mã hóa cứng.
	return big.NewInt(lastBlock.Int64())
}

// FeeHistory returns the fee market history.
func (api *MetaAPI) FeeHistory(ctx context.Context, blockCount math.HexOrDecimal64, lastBlock rpc.BlockNumber, rewardPercentiles []float64) (*feeHistoryResult, error) {
	oldestBlock := getOldestBlock(ctx, api.convertBlockNumber(lastBlock.Int64()))

	// Tạo một phiên bản của feeHistoryResult
	result := &feeHistoryResult{
		OldestBlock: (*hexutil.Big)(oldestBlock),
		Reward: [][]*hexutil.Big{
			{(*hexutil.Big)(new(big.Int).SetInt64(1)), (*hexutil.Big)(new(big.Int).SetInt64(2)), (*hexutil.Big)(new(big.Int).SetInt64(3))},
			{(*hexutil.Big)(new(big.Int).SetInt64(4)), (*hexutil.Big)(new(big.Int).SetInt64(5)), (*hexutil.Big)(new(big.Int).SetInt64(6))},
			{(*hexutil.Big)(new(big.Int).SetInt64(7)), (*hexutil.Big)(new(big.Int).SetInt64(8)), (*hexutil.Big)(new(big.Int).SetInt64(9))},
			{(*hexutil.Big)(new(big.Int).SetInt64(10)), (*hexutil.Big)(new(big.Int).SetInt64(11)), (*hexutil.Big)(new(big.Int).SetInt64(12))},
			{(*hexutil.Big)(new(big.Int).SetInt64(13)), (*hexutil.Big)(new(big.Int).SetInt64(14)), (*hexutil.Big)(new(big.Int).SetInt64(15))},
			{(*hexutil.Big)(new(big.Int).SetInt64(16)), (*hexutil.Big)(new(big.Int).SetInt64(17)), (*hexutil.Big)(new(big.Int).SetInt64(18))},
			{(*hexutil.Big)(new(big.Int).SetInt64(19)), (*hexutil.Big)(new(big.Int).SetInt64(20)), (*hexutil.Big)(new(big.Int).SetInt64(21))},
			{(*hexutil.Big)(new(big.Int).SetInt64(22)), (*hexutil.Big)(new(big.Int).SetInt64(23)), (*hexutil.Big)(new(big.Int).SetInt64(24))},
			{(*hexutil.Big)(new(big.Int).SetInt64(25)), (*hexutil.Big)(new(big.Int).SetInt64(26)), (*hexutil.Big)(new(big.Int).SetInt64(27))},
			{(*hexutil.Big)(new(big.Int).SetInt64(28)), (*hexutil.Big)(new(big.Int).SetInt64(29)), (*hexutil.Big)(new(big.Int).SetInt64(30))},
		},
		BaseFee: []*hexutil.Big{
			(*hexutil.Big)(new(big.Int).SetInt64(256)), (*hexutil.Big)(new(big.Int).SetInt64(257)), (*hexutil.Big)(new(big.Int).SetInt64(258)),
			(*hexutil.Big)(new(big.Int).SetInt64(259)), (*hexutil.Big)(new(big.Int).SetInt64(260)), (*hexutil.Big)(new(big.Int).SetInt64(261)),
			(*hexutil.Big)(new(big.Int).SetInt64(262)), (*hexutil.Big)(new(big.Int).SetInt64(263)), (*hexutil.Big)(new(big.Int).SetInt64(264)),
			(*hexutil.Big)(new(big.Int).SetInt64(265)),
		},
		GasUsedRatio: []float64{
			0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0,
		},
		BlobBaseFee: []*hexutil.Big{
			(*hexutil.Big)(new(big.Int).SetInt64(512)), (*hexutil.Big)(new(big.Int).SetInt64(513)), (*hexutil.Big)(new(big.Int).SetInt64(514)),
			(*hexutil.Big)(new(big.Int).SetInt64(515)), (*hexutil.Big)(new(big.Int).SetInt64(516)), (*hexutil.Big)(new(big.Int).SetInt64(517)),
			(*hexutil.Big)(new(big.Int).SetInt64(518)), (*hexutil.Big)(new(big.Int).SetInt64(519)), (*hexutil.Big)(new(big.Int).SetInt64(520)),
			(*hexutil.Big)(new(big.Int).SetInt64(521)),
		},
		BlobGasUsedRatio: []float64{
			0.01, 0.02, 0.03, 0.04, 0.05, 0.06, 0.07, 0.08, 0.09, 0.10,
		},
	}

	return result, nil
}
