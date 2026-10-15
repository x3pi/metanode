package processor

import (
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
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
	"github.com/meta-node-blockchain/meta-node/types"
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

	// FORK-SAFETY: Shared lock between virtual execution (RLock) and real block
	// processing (Lock). Prevents concurrent cgo calls to C++ MVM that cause
	// non-deterministic stateRoot divergence across nodes.
	blockProcessingLock *sync.RWMutex
	virtualEvmMutex     sync.Mutex
}

func NewTxVirtualExecutor(
	env ITransactionProcessorEnvironment,
	messageSender network.MessageSender,
	chainState *blockchain.ChainState,
	storageManager *storage.StorageManager,
	blockProcessingLock *sync.RWMutex,
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
		blockProcessingLock:      blockProcessingLock,
	}
	return v
}

// SetEnvironment updates the environment for the executor
func (v *TxVirtualExecutor) SetEnvironment(env ITransactionProcessorEnvironment) {
	v.env = env
}

// errNotSmartContractCall is the error returned to an eth_call whose target is not a smart contract (no VM run).
// It keeps the wording clients already matched on before the read path started accepting plain transfers.
const errNotSmartContractCall = "not a smart contract call"

// plainTransferEstimateGas is the gas EstimateGas reports for a native transfer (no VM run). It follows the HTTP
// eth_estimateGas rule (rpc_block.go): a SetCode (EIP-7702) tx still owes the intrinsic gas of every authorization tuple.
func plainTransferEstimateGas(tx types.Transaction) uint64 {
	if n := len(tx.AuthorizationList()); n > 0 {
		return params.TxGas + uint64(n)*params.CallNewAccountGas + mt_common.MINIMUM_BASE_FEE
	}
	return mt_common.MINIMUM_BASE_FEE
}

func (v *TxVirtualExecutor) parseReadTx(body []byte) (types.Transaction, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty transaction body")
	}

	chainID := uint64(0)
	if v.chainState != nil && v.chainState.GetConfig() != nil && v.chainState.GetConfig().ChainId != nil {
		chainID = v.chainState.GetConfig().ChainId.Uint64()
	}

	tx := &transaction.Transaction{}
	err := tx.Unmarshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction: %w", err)
	}
	if chainID != 0 && tx.GetChainID() != chainID {
		return nil, fmt.Errorf("chainId does not match node's (have=%v, want=%v)", tx.GetChainID(), chainID)
	}
	return tx, nil
}

// ProcessReadTransaction is the fast-path handler. It parses the protobuf transaction
// and enqueues it for async execution by a worker pool (Component A).
// This decouples the network receive goroutine from the slow EVM execution.
func (v *TxVirtualExecutor) ProcessReadTransaction(
	request network.Request,
) error {
	logger.Info("ProcessReadTransaction: enqueuing for async execution")

	tx, err := v.parseReadTx(request.Message().Body())
	if err != nil {
		v.sendTransactionError(request.Connection(), common.Hash{}, -1, err.Error(), nil, request.Message().ID())
		logger.Error("Error parsing read transaction: %v", err)
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

	tx, err := v.parseReadTx(request.Message().Body())
	if err != nil {
		v.sendTransactionError(request.Connection(), common.Hash{}, -1, err.Error(), nil, request.Message().ID())
		logger.Error("Error parsing EstimateGas transaction: %v", err)
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
			// logger.Info("Read TX worker %d started", workerID)
			for req := range v.readTxRequestChan {
				v.executeAndRespondReadTx(req)
			}
			// logger.Info("Read TX worker %d stopped", workerID)
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
	tx.SetReadOnly(!req.isEstimateGas)
	tx.ClearCacheHash()
	txHash := tx.Hash()

	startExec := time.Now()
	exRs, err := v.ProcessTransactionOffChain(tx)
	execDuration := time.Since(startExec)
	logger.Info("[PERF] Read/Estimate TX ProcessTransactionOffChain: %v, hash: %v", execDuration, txHash.Hex())
	if err != nil {
		logger.Error("Error executing read/estimate transaction off-chain: %v", err)
		v.sendTransactionError(req.conn, tx.RHash(), -1, err.Error(), nil, req.msgID)
		return
	}
	var (
		receiptStatus pb.RECEIPT_STATUS
		ret           []byte
		exception     pb.EXCEPTION
		gasUsed       uint64
		logs          []types.EventLog
	)

	if exRs == nil {
		if !req.isEstimateGas {
			logger.Error("ExecuteSCResult is nil for read TX: %s", errNotSmartContractCall)
			v.sendTransactionError(req.conn, tx.RHash(), -1, errNotSmartContractCall, nil, req.msgID)
			return
		}
		// Plain native transfer (no smart contract execution) -> tốn MINIMUM_BASE_FEE
		receiptStatus = pb.RECEIPT_STATUS_RETURNED
		exception = pb.EXCEPTION_NONE
		gasUsed = plainTransferEstimateGas(tx)
	} else {
		receiptStatus = exRs.ReceiptStatus()
		ret = exRs.Return()
		exception = exRs.Exception()
		logs = exRs.EventLogs()
		gasUsed = exRs.GasUsed()
		if req.isEstimateGas {
			gasUsed += mt_common.MINIMUM_BASE_FEE
		}
	}

	rcp := receipt.NewReceipt(
		txHash,
		tx.FromAddress(),
		tx.ToAddress(),
		tx.Amount(),
		receiptStatus,
		ret,
		exception,
		tx.EffectiveGasPrice().Uint64(),
		gasUsed,
		logs,
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
