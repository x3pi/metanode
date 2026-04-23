package processor

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

// GetTransactionReceipt handles GetTransactionReceipt requests from network
// Đọc request ID từ Header.ID thay vì từ proto body
func (bp *BlockProcessor) GetTransactionReceipt(request network.Request) error {
	id := request.Message().ID()

	// Parse request body chỉ để lấy TransactionHash
	req := &mt_proto.GetTransactionReceiptRequest{}
	if err := proto.Unmarshal(request.Message().Body(), req); err != nil {
		logger.Error("GetTransactionReceipt: Failed to unmarshal request: %v", err)
		return bp.sendReceiptError(request, id, fmt.Sprintf("failed to unmarshal request: %v", err))
	}
	// logger.Info("GetTransactionReceipt: Received request, header ID: %v", id)

	receiptEntry, err := bp.getTransactionReceipt(common.BytesToHash(req.TransactionHash))
	if err != nil {
		logger.Error("GetTransactionReceipt: Failed to get receipt: %v", err)
		return bp.sendReceiptError(request, id, err.Error())
	}

	response := &mt_proto.GetTransactionReceiptResponse{
		Receipt: receiptEntry,
		Error:   "",
	}

	responseBytes, err := proto.Marshal(response)
	if err != nil {
		logger.Error("GetTransactionReceipt: Failed to marshal response: %v", err)
		return bp.sendReceiptError(request, id, fmt.Sprintf("failed to marshal response: %v", err))
	}

	respMsg := p_network.NewMessage(&mt_proto.Message{
		Header: &mt_proto.Header{
			Command: command.TransactionReceipt,
			ID:      id,
		},
		Body: responseBytes,
	})
	return request.Connection().SendMessage(respMsg)
}

// getTransactionReceipt retrieves receipt for a given transaction hash
func (bp *BlockProcessor) getTransactionReceipt(hashEth common.Hash) (*mt_proto.RpcReceipt, error) {
	// Bước 1 & 2: Tìm block number từ hash giao dịch (bao gồm cả việc xử lý hash mapping)
	blsHash, isEthHash := blockchain.GetBlockChainInstance().GetEthHashMapblsHash(hashEth)
	searchHash := hashEth
	if isEthHash {
		searchHash = blsHash
	}

	blockNumber, ok := blockchain.GetBlockChainInstance().GetBlockNumberByTxHash(searchHash)
	if !ok {
		return nil, nil // Trả về nil nếu không tìm thấy giao dịch (not an error, just not found)
	}

	// Bước 3: Lấy block hash từ block number
	blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
	if !ok {
		return nil, fmt.Errorf("block not found for number: %d", blockNumber)
	}

	// Bước 4: Lấy toàn bộ dữ liệu của block
	blockData, err := bp.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get block data: %w", err)
	}

	// Lấy receipts database từ block
	rcpDb, err := receipt.NewReceiptsFromRoot(blockData.Header().ReceiptRoot(), bp.storageManager.GetStorageReceipt())
	if err != nil {
		return nil, fmt.Errorf("failed to load receipts: %w", err)
	}

	// Lấy receipt cho transaction
	receipt, err := rcpDb.GetReceipt(searchHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipt: %w", err)
	}
	// Convert receipt to RpcReceipt
	return bp.convertReceiptToRpcReceipt(receipt, hashEth, blockHash, blockNumber)
}

// convertReceiptToRpcReceipt converts internal receipt to RpcReceipt (string hex format)
func (bp *BlockProcessor) convertReceiptToRpcReceipt(
	rcp types.Receipt,
	txHash common.Hash,
	blockHash common.Hash,
	blockNumber uint64,
) (*mt_proto.RpcReceipt, error) {

	// Convert event logs to RpcLogEntry format (string hex)
	events := rcp.EventLogs()
	rpcLogs := make([]*mt_proto.RpcLogEntry, len(events))

	for i, logData := range events {
		topics := make([]string, len(logData.Topics))
		for j, topicBytes := range logData.Topics {
			topics[j] = common.BytesToHash(topicBytes).Hex()
		}

		rpcLogs[i] = &mt_proto.RpcLogEntry{
			Address:          common.BytesToAddress(logData.Address).Hex(),
			Topics:           topics,
			Data:             fmt.Sprintf("0x%x", logData.Data),
			BlockNumber:      fmt.Sprintf("0x%x", blockNumber),
			TransactionHash:  txHash.Hex(),
			BlockHash:        blockHash.Hex(),
			TransactionIndex: fmt.Sprintf("0x%x", rcp.TransactionIndex()),
			LogIndex:         fmt.Sprintf("0x%x", i),
			Removed:          false,
		}
	}

	rpcReceipt := &mt_proto.RpcReceipt{
		TransactionHash:   txHash.Hex(),
		From:              rcp.FromAddress().Hex(),
		To:                rcp.ToAddress().Hex(),
		Status:            rcp.Status(),
		GasUsed:           fmt.Sprintf("0x%x", rcp.GasUsed()),
		CumulativeGasUsed: fmt.Sprintf("0x%x", rcp.GasUsed()),
		EffectiveGasPrice: fmt.Sprintf("0x%x", rcp.GasFee()),
		BlockHash:         blockHash.Hex(),
		BlockNumber:       fmt.Sprintf("0x%x", blockNumber),
		TransactionIndex:  fmt.Sprintf("0x%x", rcp.TransactionIndex()),
		Type:              "0x2",
		Logs:              rpcLogs,
		ReturnData:        rcp.Return(),
		Exception:         fmt.Sprintf("%d", rcp.Exception()),
	}

	// Get transaction to fill contract address
	blockData, err := bp.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
	if err == nil {
		txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), bp.storageManager.GetStorageTransaction())
		if err == nil {
			tx, err := txDB.GetTransaction(rcp.TransactionHash())
			if err == nil {
				if rcp.ToAddress() == (common.Address{}) {
					contractAddr := crypto.CreateAddress(rcp.FromAddress(), tx.GetNonce())
					rpcReceipt.ContractAddress = contractAddr.Hex()
				}
			}
		}
	}

	return rpcReceipt, nil
}

// sendReceiptError sends error response with header ID
func (bp *BlockProcessor) sendReceiptError(request network.Request, id string, errorMsg string) error {
	response := &mt_proto.GetTransactionReceiptResponse{
		Receipt: nil,
		Error:   errorMsg,
	}

	responseBytes, err := proto.Marshal(response)
	if err != nil {
		logger.Error("GetTransactionReceipt: Failed to marshal error response: %v", err)
		return err
	}

	respMsg := p_network.NewMessage(&mt_proto.Message{
		Header: &mt_proto.Header{
			Command: command.TransactionReceipt,
			ID:      id,
		},
		Body: responseBytes,
	})
	return request.Connection().SendMessage(respMsg)
}

// GetTransactionByHash handles GetTransactionByHash requests from network
// Đọc request ID từ Header.ID thay vì từ proto body
func (bp *BlockProcessor) GetTransactionByHash(request network.Request) error {
	id := request.Message().ID()

	req := &mt_proto.GetTransactionByHashRequest{}
	if err := proto.Unmarshal(request.Message().Body(), req); err != nil {
		logger.Error("GetTransactionByHash: Failed to unmarshal request: %v", err)
		return bp.sendTransactionByHashError(request, id, fmt.Sprintf("failed to unmarshal request: %v", err))
	}

	logger.Info("GetTransactionByHash: header ID: %s, TxHash: 0x%x", id, req.TransactionHash)

	txEntry, err := bp.getTransactionByHash(common.BytesToHash(req.TransactionHash))
	if err != nil {
		logger.Error("GetTransactionByHash: Failed to get transaction: %v", err)
		return bp.sendTransactionByHashError(request, id, err.Error())
	}

	response := &mt_proto.GetTransactionByHashResponse{
		Transaction: txEntry,
		Error:       "",
	}

	responseBytes, err := proto.Marshal(response)
	if err != nil {
		logger.Error("GetTransactionByHash: Failed to marshal response: %v", err)
		return bp.sendTransactionByHashError(request, id, fmt.Sprintf("failed to marshal response: %v", err))
	}

	respMsg := p_network.NewMessage(&mt_proto.Message{
		Header: &mt_proto.Header{
			Command: command.TransactionByHash,
			ID:      id,
		},
		Body: responseBytes,
	})
	return request.Connection().SendMessage(respMsg)
}

// getTransactionByHash retrieves transaction for a given transaction hash
func (bp *BlockProcessor) getTransactionByHash(hashEth common.Hash) (*mt_proto.TransactionEntry, error) {
	// Bước 1 & 2: Tìm block number từ hash giao dịch (bao gồm cả việc xử lý hash mapping)
	blsHash, isEthHash := blockchain.GetBlockChainInstance().GetEthHashMapblsHash(hashEth)
	searchHash := hashEth
	if isEthHash {
		searchHash = blsHash
	}

	blockNumber, ok := blockchain.GetBlockChainInstance().GetBlockNumberByTxHash(searchHash)
	if !ok {
		return nil, nil // Trả về nil nếu không tìm thấy giao dịch (not an error, just not found)
	}

	// Bước 3: Lấy block hash từ block number
	blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
	if !ok {
		return nil, fmt.Errorf("block not found for number: %d", blockNumber)
	}

	// Bước 4: Lấy block data để lấy transaction
	blockData, err := bp.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get block data: %w", err)
	}

	// Bước 5: Lấy transaction từ transaction state DB
	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), bp.storageManager.GetStorageTransaction())
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction state DB: %w", err)
	}

	tx, err := txDB.GetTransaction(searchHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction: %w", err)
	}

	// Convert transaction to proto format
	return bp.convertTransactionToProto(tx, hashEth, blockHash, blockNumber)
}

// convertTransactionToProto converts internal transaction to proto TransactionEntry
func (bp *BlockProcessor) convertTransactionToProto(
	tx types.Transaction,
	txHash common.Hash,
	blockHash common.Hash,
	blockNumber uint64,
) (*mt_proto.TransactionEntry, error) {
	// Get signature values
	v, r, s := tx.RawSignatureValues()

	// Determine transaction type
	var txType uint64 = 0 // Regular transaction
	if tx.IsDeployContract() {
		txType = 1
	} else if tx.IsCallContract() {
		txType = 2
	}

	// Create TransactionEntry
	txEntry := &mt_proto.TransactionEntry{
		TransactionHash:  txHash.Bytes(),
		FromAddress:      tx.FromAddress().Bytes(),
		ToAddress:        tx.ToAddress().Bytes(),
		Amount:           tx.Amount().Bytes(),
		Nonce:            tx.GetNonce(),
		MaxGas:           tx.MaxGas(),
		MaxGasPrice:      tx.MaxGasPrice(),
		Data:             tx.Data(),
		Type:             txType,
		ChainID:          tx.GetChainID(),
		V:                v.Bytes(),
		R:                r.Bytes(),
		S:                s.Bytes(),
		BlockHash:        blockHash.Bytes(),
		BlockNumber:      blockNumber,
		TransactionIndex: 0, // Default, will update if we have receipt
		GasTipCap:        tx.GasTipCap().Bytes(),
		GasFeeCap:        tx.GasFeeCap().Bytes(),
	}

	// Try to get receipt to find transaction index
	blockData, err := bp.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
	if err == nil {
		rcpDb, err := receipt.NewReceiptsFromRoot(blockData.Header().ReceiptRoot(), bp.storageManager.GetStorageReceipt())
		if err == nil {
			rcp, err := rcpDb.GetReceipt(tx.Hash())
			if err == nil {
				txEntry.TransactionIndex = rcp.TransactionIndex()
			}
		}
	}

	return txEntry, nil
}

// sendTransactionByHashError sends error response with header ID
func (bp *BlockProcessor) sendTransactionByHashError(request network.Request, id string, errorMsg string) error {
	response := &mt_proto.GetTransactionByHashResponse{
		Transaction: nil,
		Error:       errorMsg,
	}

	responseBytes, err := proto.Marshal(response)
	if err != nil {
		logger.Error("GetTransactionByHash: Failed to marshal error response: %v", err)
		return err
	}

	respMsg := p_network.NewMessage(&mt_proto.Message{
		Header: &mt_proto.Header{
			Command: command.TransactionByHash,
			ID:      id,
		},
		Body: responseBytes,
	})
	return request.Connection().SendMessage(respMsg)
}
