package processor

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors" // Đã thêm import này
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	lru "github.com/hashicorp/golang-lru/v2"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mining"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

type StateProcessor struct {
	messageSender network.MessageSender

	blockProcessor *BlockProcessor
	storageManager *storage.StorageManager
	scResultsCache *lru.Cache[uint64, common.Hash]
}

func NewStateProcessor(
	messageSender network.MessageSender,
	accountStateDB *account_state_db.AccountStateDB,
	blockProcessor *BlockProcessor,
	storageManager *storage.StorageManager,
) *StateProcessor {
	cacheSize := 1000 // Configurable cache size
	cache, err := lru.New[uint64, common.Hash](cacheSize)
	if err != nil {
		logger.Fatal("Failed to create LRU cache for StateProcessor: %v", err)
	}
	return &StateProcessor{
		messageSender,
		blockProcessor,
		storageManager,
		cache,
	}
}

// sendFailureResponse marshals and sends a CompleteJobResponse with Success=false.
func (sp *StateProcessor) sendFailureResponse(conn network.Connection) {
	respProto := &pb.CompleteJobResponse{Success: false}
	respBytes, err := proto.Marshal(respProto)
	if err != nil {
		logger.Error("sendFailureResponse: Failed to marshal failure response: %v", err)
		return
	}
	sp.messageSender.SendBytes(conn, command.CompleteJob, respBytes)
}

// sendTxHistoryByAddressErrorResponse is a helper function to send an error response.
func (sp *StateProcessor) sendTxHistoryByAddressErrorResponse(conn network.Connection, errorMessage string) {
	respProto := &pb.GetTransactionHistoryByAddressResponse{
		Error: errorMessage,
	}
	respBytes, err := proto.Marshal(respProto)
	if err != nil {
		logger.Error("sendTxHistoryByAddressErrorResponse: Could not marshal error response: %v", err)
		return
	}
	sp.messageSender.SendBytes(conn, command.TxRewardHistoryByAddress, respBytes)
}

// sendTxHistoryByJobIDErrorResponse is a helper function to send an error response.
func (sp *StateProcessor) sendTxHistoryByJobIDErrorResponse(conn network.Connection, errorMessage string) {
	respProto := &pb.GetTransactionHistoryByJobIDResponse{
		Error: errorMessage,
	}
	respBytes, err := proto.Marshal(respProto)
	if err != nil {
		logger.Error("sendTxHistoryByJobIDErrorResponse: Could not marshal error response: %v", err)
		return
	}
	sp.messageSender.SendBytes(conn, command.TxRewardHistoryByJobID, respBytes)
}

func (sp *StateProcessor) ProcessGetAccountState(request network.Request) error {
	address := common.BytesToAddress(request.Message().Body())
	as, err := sp.blockProcessor.chainState.GetAccountStateDB().AccountState(address)
	if err != nil {
		return err
	}

	// msgID := request.Message().ID()
	// if msgID != "" {
	// Có msgID → gửi response với header ID để client match request-response
	b, err := proto.Marshal(as.Proto())
	if err != nil {
		return err
	}
	respMsg := p_network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: command.AccountState,
			ID:      request.Message().ID(),
		},
		Body: b,
	})
	return request.Connection().SendMessage(respMsg)
}

func (sp *StateProcessor) ProcessGetNonce(request network.Request) error {
	address := common.BytesToAddress(request.Message().Body())
	as, err := sp.blockProcessor.chainState.GetAccountStateDB().AccountState(address)
	if err != nil {
		return err
	}
	msgID := request.Message().ID()
	respMsg := p_network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: command.Nonce,
			ID:      msgID,
		},
		Body: as.Proto().Nonce,
	})
	return request.Connection().SendMessage(respMsg)
}

func (sp *StateProcessor) ProcessGetTransactionsByBlockNumber(request network.Request) error {
	blockNumber := binary.BigEndian.Uint64(request.Message().Body())
	blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
	if !ok {
		return fmt.Errorf("cannot find block hash for block number %d", blockNumber)
	}
	blockData, err := sp.blockProcessor.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
	if err != nil {
		return fmt.Errorf("failed to get block by hash: %w", err)
	}
	transactionHashes := blockData.Transactions()
	transactions := make([]types.Transaction, len(transactionHashes))
	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), sp.blockProcessor.chainState.GetStorageManager().GetStorageTransaction())
	if err != nil {
		return fmt.Errorf("failed to create TransactionStateDB: %w", err)
	}
	for i, txHash := range transactionHashes {
		tx, err := txDB.GetTransaction(txHash)
		if err != nil {
			return fmt.Errorf("failed to get transaction by hash %s: %w", txHash.Hex(), err)
		}
		transactions[i] = tx
	}
	bTransaction, err := transaction.MarshalTransactions(transactions)
	if err != nil {
		return err
	}

	msgID := request.Message().ID()
	respMsg := p_network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: command.TransactionsByBlockNumber,
			ID:      msgID,
		},
		Body: bTransaction,
	})
	return request.Connection().SendMessage(respMsg)
}

func (sp *StateProcessor) ProcessGetBlockHeaderByBlockNumber(request network.Request) error {
	blockNumber := binary.BigEndian.Uint64(request.Message().Body())
	blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
	if !ok {
		return fmt.Errorf("cannot find block hash for block number %d", blockNumber)
	}
	blockData, err := sp.blockProcessor.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
	if err != nil {
		return fmt.Errorf("failed to get block by hash %s: %w", blockHash.Hex(), err)
	}
	header := blockData.Header()
	bHeader, err := proto.Marshal(header.Proto())
	if err != nil {
		return err
	}

	msgID := request.Message().ID()
	respMsg := p_network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: command.BlockHeaderByBlockNumber,
			ID:      msgID,
		},
		Body: bHeader,
	})
	return request.Connection().SendMessage(respMsg)
}

func (sp *StateProcessor) ProcessGetDeviceKey(request network.Request) (err error) {
	deviceStorage := sp.storageManager.GetStorageBackupDeviceKey()
	data, _ := deviceStorage.Get(request.Message().Body())
	logger.Debug(
		"Get device key",
		hex.EncodeToString(request.Message().Body()),
		hex.EncodeToString(data),
	)
	rsData := append(request.Message().Body(), data...)

	respMsg := p_network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: command.DeviceKey,
			ID:      request.Message().ID(),
		},
		Body: rsData,
	})
	request.Connection().SendMessage(respMsg)
	return nil

}

func (sp *StateProcessor) GetDeviceKey(hash common.Hash) (common.Hash, error) {
	deviceStorage := sp.storageManager.GetStorageBackupDeviceKey()
	data, err := deviceStorage.Get(hash.Bytes())
	if err != nil {
		return common.Hash{}, err
	}
	return common.BytesToHash(data), nil
}

func (sp *StateProcessor) ProcessGetJob(request network.Request) error {
	address := common.BytesToAddress(request.Message().Body())
	logger.Info("ProcessGetJob: Received request for address %s", address.Hex())

	if !sp.storageManager.IsMining() {
		return errors.New("ProcessGetJob: MiningService is not initialized or enabled")
	}

	miningService := sp.storageManager.GetMiningService()

	if sp.blockProcessor == nil || sp.blockProcessor.GetLastBlock() == nil {
		return errors.New("block processor not initialized or no last block found")
	}
	lastBlockNumber := sp.blockProcessor.GetLastBlock().Header().BlockNumber()

	job, err := miningService.GetOrAssignJob(address, lastBlockNumber)
	if err != nil {
		logger.Error("ProcessGetJob: Failed to get or assign job for %s: %v", address.Hex(), err)
		return err
	}

	jobResponse := &pb.GetJobResponse{
		Job: &pb.Job{
			JobId:       job.JobID,
			Creator:     job.Creator,
			Assignee:    job.Assignee,
			JobType:     job.JobType,
			Status:      job.Status,
			Data:        job.Data,
			Reward:      job.Reward,
			CreatedAt:   job.CreatedAt,
			CompletedAt: job.CompletedAt,
			TxHash:      job.TxHash,
		},
	}

	jobBytes, err := proto.Marshal(jobResponse)
	if err != nil {
		logger.Error("ProcessGetJob: Failed to marshal job response to Protobuf: %v", err)
		return err
	}

	return sp.messageSender.SendBytes(
		request.Connection(),
		command.Job,
		jobBytes,
	)
}

func (sp *StateProcessor) GetExecuteSCResultsHashCore(ctx context.Context, blockNumber uint64) (common.Hash, error) {
	if blockNumber == 0 {
		return common.Hash{}, errors.New("GetExecuteSCResultsHashCore: cannot process genesis block")
	}
	if cachedHash, ok := sp.scResultsCache.Get(blockNumber); ok {
		return cachedHash, nil
	}
	hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
	if !ok {
		return common.Hash{}, fmt.Errorf("could not find block hash for block number %d", blockNumber)
	}
	blockData, err := sp.blockProcessor.chainState.GetBlockDatabase().GetBlockByHash(hash)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to load block with hash %s: %w", hash.Hex(), err)
	}
	oldBlockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber - 1)
	if !ok {
		return common.Hash{}, fmt.Errorf("could not find previous block hash for block number %d", blockNumber-1)
	}
	oldBlockData, err := sp.blockProcessor.chainState.GetBlockDatabase().GetBlockByHash(oldBlockHash)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to load previous block with hash %s: %w", oldBlockHash.Hex(), err)
	}
	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), sp.storageManager.GetStorageTransaction())
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to create TransactionStateDB for block %d: %w", blockNumber, err)
	}
	transactionHashes := blockData.Transactions()
	txs := make([]types.Transaction, 0, len(transactionHashes))
	for _, txHash := range transactionHashes {
		tx, err := txDB.GetTransaction(txHash)
		if err != nil {
			return common.Hash{}, fmt.Errorf("cannot get transaction %s from state DB: %w", txHash.Hex(), err)
		}
		txs = append(txs, tx)
	}

	freeFeeMap := sp.blockProcessor.chainState.GetFreeFeeAddress()
	if len(freeFeeMap) == 0 {
		return common.Hash{}, errors.New("free fee address not found in chainState")
	}

	chainState, err := blockchain.NewChainStateRemote(oldBlockData.Header(), sp.storageManager.GetStorageAccount(), sp.storageManager.GetStorageCode(), sp.storageManager.GetStorageSmartContract(), freeFeeMap)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to create chainState for block %d: %w", blockNumber, err)
	}

	items := make([]grouptxns.Item, 0, len(txs))
	for i, tx := range txs {
		items = append(items, grouptxns.Item{ID: i, Array: tx.RelatedAddresses(), Tx: tx})
	}
	groupedGroups, _, err := grouptxns.GroupAndLimitTransactionsOptimized(items, mt_common.MAX_GROUP_GAS, mt_common.MAX_TOTAL_GAS, mt_common.MAX_GROUP_TIME, mt_common.MAX_TOTAL_TIME)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to group transactions for block %d: %w", blockNumber, err)
	}

	// Use the block's stored timestamp for deterministic replay
	blockTimeSec := blockData.Header().TimeStamp() / 1000 // Convert ms→s
	processResult, err := tx_processor.ProcessTransactionsRemote(ctx, chainState, groupedGroups, true, false, blockTimeSec)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to process transactions for block %d: %w", blockNumber, err)
	}

	resultsHash := smart_contract.HashExecuteSCResultsKeccak256(processResult.ExecuteSCResults)
	sp.scResultsCache.Add(blockNumber, resultsHash)
	return resultsHash, nil
}

func (sp *StateProcessor) ProcessCompleteJob(request network.Request) error {
	var reqProto pb.CompleteJobRequest
	if err := proto.Unmarshal(request.Message().Body(), &reqProto); err != nil {
		logger.Error("ProcessCompleteJob: Failed to unmarshal request body: %v", err)
		sp.sendFailureResponse(request.Connection())
		return fmt.Errorf("invalid request format: %w", err)
	}

	address := common.BytesToAddress(reqProto.Address.Value)
	jobID := reqProto.JobId
	hashExecute := common.BytesToHash(reqProto.HashExecute.Value)

	logger.Info("Received CompleteJob request for JobID '%s' from address %s", jobID, address.Hex())

	if !sp.storageManager.IsMining() {
		logger.Error("ProcessCompleteJob: MiningService is not initialized or enabled")
		sp.sendFailureResponse(request.Connection())
		return errors.New("MiningService not initialized or enabled")
	}

	miningService := sp.storageManager.GetMiningService()

	job, err := miningService.GetJobByID(jobID)
	if err != nil {
		logger.Error("Could not find job with ID %s: %v", jobID, err)
		sp.sendFailureResponse(request.Connection())
		return err
	}

	assigneeAddress := common.HexToAddress(job.Assignee)
	if assigneeAddress != address {
		err := fmt.Errorf("job %s is not assigned to address %s (assigned to %s)", jobID, address.String(), assigneeAddress.String())
		logger.Error(err.Error())
		sp.sendFailureResponse(request.Connection())
		return err
	}

	if job.Status != mining.JobStatusNew {
		err := fmt.Errorf("job %s is not in '%s' status (current: %s)", jobID, mining.JobStatusNew, job.Status)
		logger.Error(err.Error())
		sp.sendFailureResponse(request.Connection())
		return err
	}

	if job.JobType == mining.JobTypeVideoAds {
		logger.Info("Completing a 'video_ads' job: %s", jobID)
		_, err := miningService.CompleteJob(jobID, address.String())
		if err != nil {
			logger.Error("Error from service on completing video_ads job %s: %v", jobID, err)
			sp.sendFailureResponse(request.Connection())
			return err
		}
	} else { // JobTypeValidateBlock
		logger.Info("Validating a 'validate_block' job: %s", jobID)

		var blockNumHex hexutil.Uint64
		if err := blockNumHex.UnmarshalText([]byte(job.Data)); err != nil {
			err := fmt.Errorf("invalid block number format for job %s: '%s': %w", jobID, job.Data, err)
			logger.Error(err.Error())
			sp.sendFailureResponse(request.Connection())
			return err
		}
		blockNumberUint64 := uint64(blockNumHex)

		logger.Info("Calculating expected hash for block %d...", blockNumberUint64)
		expectedHash, err := sp.GetExecuteSCResultsHashCore(context.Background(), blockNumberUint64)
		if err != nil {
			err := fmt.Errorf("could not calculate execution hash for block %d: %w", blockNumberUint64, err)
			logger.Error(err.Error())
			sp.sendFailureResponse(request.Connection())
			return err
		}
		logger.Info("Expected hash: %s", expectedHash.Hex())
		logger.Info("Received hash: %s", hashExecute.Hex())

		if expectedHash != hashExecute {
			err := fmt.Errorf("hash mismatch for block %d: expected %s, but got %s", blockNumberUint64, expectedHash.Hex(), hashExecute.Hex())
			logger.Error(err.Error())
			sp.sendFailureResponse(request.Connection())
			return err
		}

		logger.Info("Hash validation successful for job %s. Completing job...", jobID)
		_, err = miningService.CompleteJob(jobID, address.String())
		if err != nil {
			logger.Error("Error from service on completing validate_block job %s: %v", jobID, err)
			sp.sendFailureResponse(request.Connection())
			return err
		}
	}

	logger.Info("Job %s completed successfully by %s.", jobID, address.String())
	respProto := &pb.CompleteJobResponse{Success: true}
	respBytes, err := proto.Marshal(respProto)
	if err != nil {
		logger.Error("ProcessCompleteJob: Failed to marshal success response: %v", err)
		return err
	}
	return sp.messageSender.SendBytes(
		request.Connection(),
		command.CompleteJob,
		respBytes,
	)
}

func (sp *StateProcessor) ProcessGetTxHistoryByAddress(request network.Request) error {
	var reqProto pb.GetTransactionHistoryByAddressRequest
	if err := proto.Unmarshal(request.Message().Body(), &reqProto); err != nil {
		errMsg := fmt.Sprintf("invalid request format: %v", err)
		logger.Error("ProcessGetTxHistoryByAddress: Failed to unmarshal request body: %v", err)
		sp.sendTxHistoryByAddressErrorResponse(request.Connection(), errMsg)
		return errors.New(errMsg)
	}

	address := common.BytesToAddress(reqProto.Address.Value)
	offset := int(reqProto.Offset)
	limit := int(reqProto.Limit)

	logger.Info("ProcessGetTxHistoryByAddress: Received request for address %s, offset %d, limit %d", address.Hex(), offset, limit)

	if !sp.storageManager.IsMining() {
		errMsg := "MiningService is not initialized or enabled"
		logger.Error("ProcessGetTxHistoryByAddress: %s", errMsg)
		sp.sendTxHistoryByAddressErrorResponse(request.Connection(), errMsg)
		return errors.New(errMsg)
	}

	miningService := sp.storageManager.GetMiningService()

	records, total, err := miningService.SearchTransactionHistory(
		fmt.Sprintf("R%s OR P%s", strings.ToLower(address.Hex()), strings.ToLower(address.Hex())),
		offset,
		limit,
	)
	if err != nil {
		errMsg := fmt.Sprintf("failed to search transaction history: %v", err)
		logger.Error("ProcessGetTxHistoryByAddress: %s", errMsg)
		sp.sendTxHistoryByAddressErrorResponse(request.Connection(), errMsg)
		return err
	}

	protoRecords := make([]*pb.TransactionRecord, len(records))
	for i, r := range records {
		protoRecords[i] = &pb.TransactionRecord{
			TxId:      r.TxID,
			JobId:     r.JobID,
			Sender:    r.Sender,
			Recipient: r.Recipient,
			Amount:    r.Amount,
			Timestamp: r.Timestamp,
			Status:    r.Status,
		}
	}

	respProto := &pb.GetTransactionHistoryByAddressResponse{
		Total:   uint64(total),
		Records: protoRecords,
	}
	respBytes, err := proto.Marshal(respProto)
	if err != nil {
		logger.Error("ProcessGetTxHistoryByAddress: Failed to marshal success response: %v", err)
		return err
	}

	return sp.messageSender.SendBytes(
		request.Connection(),
		command.TxRewardHistoryByAddress,
		respBytes,
	)
}

func (sp *StateProcessor) ProcessGetTxHistoryByJobID(request network.Request) error {
	var reqProto pb.GetTransactionHistoryByJobIDRequest
	if err := proto.Unmarshal(request.Message().Body(), &reqProto); err != nil {
		errMsg := fmt.Sprintf("invalid request format: %v", err)
		logger.Error("ProcessGetTxHistoryByJobID: Failed to unmarshal request body: %v", err)
		sp.sendTxHistoryByJobIDErrorResponse(request.Connection(), errMsg)
		return errors.New(errMsg)
	}

	jobID := reqProto.JobId
	logger.Info("ProcessGetTxHistoryByJobID: Received request for JobID %s", jobID)

	if !sp.storageManager.IsMining() {
		errMsg := "MiningService is not initialized or enabled"
		logger.Error("ProcessGetTxHistoryByJobID: %s", errMsg)
		sp.sendTxHistoryByJobIDErrorResponse(request.Connection(), errMsg)
		return errors.New(errMsg)
	}

	miningService := sp.storageManager.GetMiningService()

	job, err := miningService.GetJobByID(jobID)
	if err != nil {
		errMsg := fmt.Sprintf("job with ID %s not found: %v", jobID, err)
		logger.Error("ProcessGetTxHistoryByJobID: Failed to find job with ID %s: %v", jobID, err)
		sp.sendTxHistoryByJobIDErrorResponse(request.Connection(), errMsg)
		return errors.New(errMsg)
	}

	if job.TxHash == "" {
		logger.Warn("ProcessGetTxHistoryByJobID: No transaction history found for job %s", jobID)
		respProto := &pb.GetTransactionHistoryByJobIDResponse{Record: nil}
		respBytes, _ := proto.Marshal(respProto)
		return sp.messageSender.SendBytes(
			request.Connection(),
			command.TxRewardHistoryByJobID,
			respBytes,
		)
	}

	record, err := miningService.GetTransactionRecordByTxID(job.TxHash)
	if err != nil {
		errMsg := fmt.Sprintf("failed to retrieve transaction record for job %s: %v", jobID, err)
		logger.Error("ProcessGetTxHistoryByJobID: Failed to retrieve transaction record for job %s (TxHash %s): %v", jobID, job.TxHash, err)
		sp.sendTxHistoryByJobIDErrorResponse(request.Connection(), errMsg)
		return errors.New(errMsg)
	}

	protoRecord := &pb.TransactionRecord{
		TxId:      record.TxID,
		JobId:     record.JobID,
		Sender:    record.Sender,
		Recipient: record.Recipient,
		Amount:    record.Amount,
		Timestamp: record.Timestamp,
		Status:    record.Status,
	}

	respProto := &pb.GetTransactionHistoryByJobIDResponse{
		Record: protoRecord,
	}
	respBytes, err := proto.Marshal(respProto)
	if err != nil {
		logger.Error("ProcessGetTxHistoryByJobID: Failed to marshal success response: %v", err)
		return err
	}

	return sp.messageSender.SendBytes(
		request.Connection(),
		command.TxRewardHistoryByJobID,
		respBytes,
	)
}
