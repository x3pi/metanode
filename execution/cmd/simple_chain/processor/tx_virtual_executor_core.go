package processor

import (
	"sync"

	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

const maxConcurrentReadTx = 1000
const maxConcurrentOffChainExecution = 100

// TxVirtualExecutor encapsulates logic for processing read-only and off-chain transactions.
// It leverages Struct Embedding to be seamlessly integrated into TransactionProcessor.
type TxVirtualExecutor struct {
	env            ITransactionProcessorEnvironment
	messageSender  network.MessageSender
	chainState     *blockchain.ChainState
	storageManager *storage.StorageManager

	readTxRequestChan        chan readTxRequest
	readTxHashes             sync.Map
	readOnlyResultChan       chan tx_processor.ProcessResult
	readTxLimiter            chan struct{}
	offChainExecutionLimiter chan struct{}
}

func NewTxVirtualExecutor(
	env ITransactionProcessorEnvironment,
	messageSender network.MessageSender,
	chainState *blockchain.ChainState,
	storageManager *storage.StorageManager,
) *TxVirtualExecutor {
	v := &TxVirtualExecutor{
		env:                      env,
		messageSender:            messageSender,
		chainState:               chainState,
		storageManager:           storageManager,
		readTxRequestChan:        make(chan readTxRequest, 10000),
		readOnlyResultChan:       make(chan tx_processor.ProcessResult, 1000),
		readTxLimiter:            make(chan struct{}, maxConcurrentReadTx),
		offChainExecutionLimiter: make(chan struct{}, maxConcurrentOffChainExecution),
	}
	return v
}

// SetEnvironment updates the environment for the executor
func (v *TxVirtualExecutor) SetEnvironment(env ITransactionProcessorEnvironment) {
	v.env = env
}

// ProcessReadTransaction is the fast-path handler. It unmarshals the TX
// and enqueues it for async execution by a worker pool (Component A).
// This decouples the network receive goroutine from the slow EVM execution.
func (v *TxVirtualExecutor) ProcessReadTransaction(
	request network.Request,
) error {
	logger.Info("ProcessReadTransaction: enqueuing for async execution")

	tx := &transaction.Transaction{}
	err := tx.Unmarshal(request.Message().Body())
	if err != nil {
		v.sendTransactionError(request.Connection(), common.Hash{}, -1, err.Error(), nil, request.Message().ID())
		logger.Error("Error unmarshalling read transaction: %v", err)
		return err
	}

	if !tx.IsCallContract() {
		err = fmt.Errorf("not a smart contract call")
		logger.Error("Transaction is not a smart contract call: %v", err)
		v.sendTransactionError(request.Connection(), tx.RHash(), -1, err.Error(), nil, request.Message().ID())
		return err
	}

	logger.Info("ProcessReadTransaction tx: %v", tx)
	// Enqueue to async worker pool — non-blocking
	select {
	case v.readTxRequestChan <- readTxRequest{
		conn:          request.Connection(),
		tx:            tx,
		msgID:         request.Message().ID(),
		isEstimateGas: false, // Đây là eth_call
	}:
		// Successfully enqueued
	default:
		err := fmt.Errorf("read TX queue full, system overloaded")
		logger.Warn("readTxRequestChan is full, dropping read transaction")
		v.sendTransactionError(request.Connection(), tx.RHash(), -1, err.Error(), nil, request.Message().ID())
		return err
	}
	return nil
}

func (v *TxVirtualExecutor) ProcessEstimateGas(
	request network.Request,
) error {
	logger.Info("ProcessEstimateGas: enqueuing for async execution")

	tx := &transaction.Transaction{}
	err := tx.Unmarshal(request.Message().Body())
	if err != nil {
		v.sendTransactionError(request.Connection(), common.Hash{}, -1, err.Error(), nil, request.Message().ID())
		logger.Error("Error unmarshalling EstimateGas transaction: %v", err)
		return err
	}

	// Enqueue to async worker pool — non-blocking
	select {
	case v.readTxRequestChan <- readTxRequest{
		conn:          request.Connection(),
		tx:            tx,
		msgID:         request.Message().ID(),
		isEstimateGas: true, // Phân biệt đây là estimateGas
	}:
		// Successfully enqueued
	default:
		err := fmt.Errorf("read TX queue full, system overloaded")
		logger.Warn("readTxRequestChan is full, dropping estimate gas transaction")
		v.sendTransactionError(request.Connection(), tx.RHash(), -1, err.Error(), nil, request.Message().ID())
		return err
	}
	return nil
}

// startReadTxWorkers spawns n goroutines that consume from readTxRequestChan
// and execute each read TX asynchronously (Component A).
func (v *TxVirtualExecutor) startReadTxWorkers(n int) {
	for i := 0; i < n; i++ {
		go func(workerID int) {
			logger.Info("Read TX worker %d started", workerID)
			for req := range v.readTxRequestChan {
				v.executeAndRespondReadTx(req)
			}
			logger.Info("Read TX worker %d stopped", workerID)
		}(i)
	}
}

// executeAndRespondReadTx handles the full lifecycle of a read TX:
// concurrency limiting, EVM execution, receipt creation, and response.
func (v *TxVirtualExecutor) executeAndRespondReadTx(req readTxRequest) {
	// Concurrency limiter (semaphore)
	startWait := time.Now()
	select {
	case v.readTxLimiter <- struct{}{}:
		waitDuration := time.Since(startWait)
		logger.Info("[PERF] Read TX semaphore wait: %v", waitDuration)
		defer func() { <-v.readTxLimiter }()
	case <-time.After(100 * time.Millisecond):
		logger.Warn("Read TX limiter timeout, system overloaded")
		v.sendTransactionError(req.conn, common.Hash{}, -1, "system overloaded, please retry", nil, req.msgID)
		return
	}

	tx := req.tx
	tx.SetReadOnly(true)
	tx.SetNonce(storage.GetIncrementingCounter())
	tx.ClearCacheHash()
	txHash := tx.Hash()

	startExec := time.Now()
	exRs, err := v.executeTransactionOffChain(tx)
	execDuration := time.Since(startExec)
	logger.Info("[PERF] Read/Estimate TX executeTransactionOffChain: %v, hash: %v", execDuration, txHash.Hex())
	if err != nil {
		logger.Error("Error executing read/estimate transaction off-chain: %v", err)
		v.sendTransactionError(req.conn, tx.RHash(), -1, err.Error(), nil, req.msgID)
		return
	}
	if exRs == nil {
		logger.Error("ExecuteSCResult is nil for read/estimate TX")
		v.sendTransactionError(req.conn, tx.RHash(), -1, "evm return nil", nil, req.msgID)
		return
	}

	var gasUsed uint64
	if req.isEstimateGas {
		gasUsed = exRs.GasUsed() + mt_common.MINIMUM_BASE_FEE
	} else {
		gasUsed = exRs.GasUsed()
	}

	rcp := receipt.NewReceipt(
		txHash,
		tx.FromAddress(),
		tx.ToAddress(),
		tx.Amount(),
		exRs.ReceiptStatus(),
		exRs.Return(),
		exRs.Exception(),
		mt_common.MINIMUM_BASE_FEE,
		gasUsed,
		exRs.EventLogs(),
		uint64(0),
		common.Hash{},
		0,
	)
	rcp.SetRHash(tx.RHash())

	b, err := rcp.Marshal()
	if err != nil {
		logger.Error("Error marshalling receipt: %v", err)
		v.sendTransactionError(req.conn, tx.RHash(), -1, err.Error(), nil, req.msgID)
		return
	}

	logger.Info("Read/Estimate TX executed, sending receipt: %v (msgID=%s), isEstimateGas=%v", rcp, req.msgID, req.isEstimateGas)
	// Gửi receipt với header ID để client có thể match response
	respMsg := p_network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: command.Receipt,
			ID:      req.msgID,
		},
		Body: b,
	})
	req.conn.SendMessage(respMsg)
}
