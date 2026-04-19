package network

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/command"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/stats"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

var ErrorCommandNotFound = errors.New("command not found")

type Handler struct {
	accountStateChan     chan types.AccountState
	receiptChan          chan types.Receipt
	eventLogChan         chan types.EventLogs
	transactionErrorChan chan types.TransactionError
	deviceKeyChan        chan types.LastDeviceKey
	nonceChan            chan uint64
	txErrorChan          chan error
}

func NewHandler(
	accountStateChan chan types.AccountState,
	receiptChan chan types.Receipt,
	deviceKeyChan chan types.LastDeviceKey,
	transactionErrorChan chan types.TransactionError,
	nonceChan chan uint64,
) *Handler {
	return &Handler{
		accountStateChan:     accountStateChan,
		receiptChan:          receiptChan,
		deviceKeyChan:        deviceKeyChan,
		transactionErrorChan: transactionErrorChan,
		nonceChan:            nonceChan,
		txErrorChan:          make(chan error, 1),
	}
}

func (h *Handler) TxErrorChan() chan error {
	return h.txErrorChan
}

func (h *Handler) HandleRequest(request network.Request) (err error) {

	cmd := request.Message().Command()
	// logger.Debug("handler.gohandling command: " + cmd)
	switch cmd {
	case command.InitConnection:
		return h.handleInitConnection(request)
	case command.AccountState:
		return h.handleAccountState(request)
	case command.Nonce:
		return h.handleNonce(request)
	case command.TransactionError:
		transactionError := &transaction.TransactionHashWithError{}
		_ = transactionError.Unmarshal(request.Message().Body())
		protoErr := transactionError.Proto()
		logger.Error("TransactionError Code : %v", protoErr.Code)
		logger.Error("TransactionError Output : %v", common.Bytes2Hex(protoErr.Output))
		logger.Error("TransactionError Description: %v", protoErr.Description)
		logger.Error("TransactionError Hash: %v", common.BytesToHash(protoErr.Hash))
		if h.txErrorChan != nil {
			select {
			case h.txErrorChan <- fmt.Errorf("transaction rejected (code %d): %s", protoErr.Code, protoErr.Description):
			case <-time.After(100 * time.Millisecond):
				logger.Warn("txErrorChan full, dropping transaction error")
			}
		}
		return nil
	case command.Receipt:
		return h.handleReceipt(request)
	case command.TransactionSuccess:
		// Giao dịch đã vào mempool thành công, server trả về txHash (bỏ qua hoặc log debug)
		// Không có action đặc biệt nào cần làm vì tx_sender sẽ poll receipt
		return nil
	case command.DeviceKey:
		return h.handleDeviceKey(request)
	case command.EventLogs:
		return h.handleEventLogs(request)
	case command.QueryLogs:
		return h.handleEventLogs(request)
	case command.Stats:
		return h.handleStats(request)

	case command.ServerBusy:
		logger.Error("ServerBusy")
		return nil
	}
	return ErrorCommandNotFound
}

func (h *Handler) SetEventLogsChan(ch chan types.EventLogs) {
	h.eventLogChan = ch
}

func (h *Handler) GetEventLogsChan() chan types.EventLogs {
	return h.eventLogChan
}

/*
handleInitConnection will receive request from connection
then init that connection with data in request then
add it to connection manager
*/
func (h *Handler) handleInitConnection(request network.Request) (err error) {
	fileLogger, _ := loggerfile.NewFileLogger("connect/handleInitConnection%s_%s.log")
	conn := request.Connection()
	fileLogger.Info("local/remote addr %v  : %v", conn.TcpLocalAddr(), conn.TcpRemoteAddr())
	initData := &pb.InitConnection{}
	err = request.Message().Unmarshal(initData)
	if err != nil {
		fileLogger.Info("error %v", err)
		return err
	}
	address := common.BytesToAddress(initData.Address)
	logger.Debug(fmt.Sprintf(
		"init connection from %v type %v", address, initData.Type,
	))
	fileLogger.Info("init connection from %v type %v", address, initData.Type)

	conn.Init(address, initData.Type)
	return nil
}

/*
handleAccountState will receive account state from connection
then push it to account state chan
*/
func (h *Handler) handleAccountState(request network.Request) (err error) {
	accountState := &state.AccountState{}
	err = accountState.Unmarshal(request.Message().Body())
	if err != nil {
		return err
	}
	// logger.Debug(fmt.Sprintf("Receive Account state: \n%v", accountState))
	select {
	case h.accountStateChan <- accountState:
		// Success
	case <-time.After(100 * time.Millisecond):
		logger.Warn("accountStateChan is full, dropping account state update")
	}
	return nil
}

func (h *Handler) handleNonce(request network.Request) (err error) {
	numBytes := request.Message().Body()
	num := uint64(0)
	if len(numBytes) == 8 { // Check for valid length
		num = binary.BigEndian.Uint64(numBytes)
	} else {
		return fmt.Errorf("invalid nonce data length: %d", len(numBytes))
	}
	select {
	case h.nonceChan <- num:
		// Success
	case <-time.After(100 * time.Millisecond):
		logger.Warn("nonceChan is full, dropping nonce update for %d", num)
	}
	return nil
}

func (h *Handler) handleDeviceKey(request network.Request) (err error) {

	data := request.Message().Body()
	if len(data) != 64 && len(data) != 32 {
		err = fmt.Errorf("unable to parse wrong len: %d", len(data))
		return err
	}

	transactionHash := data[:32]

	var lastDeviceKeyFromServer []byte

	if len(data) == 32 {
		lastDeviceKeyFromServer = common.Hash{}.Bytes()
	} else {
		lastDeviceKeyFromServer = data[32:]
	}

	lastDeviceKey := types.LastDeviceKey{
		TransactionHash:         transactionHash,
		LastDeviceKeyFromServer: lastDeviceKeyFromServer,
	}
	if h.deviceKeyChan != nil {
		select {
		case h.deviceKeyChan <- lastDeviceKey:
			// Success
		case <-time.After(100 * time.Millisecond):
			logger.Warn("deviceKeyChan is full, dropping device key update")
		}
	} else {
		err = fmt.Errorf("deviceKeyChan is nil")
		return err
	}

	return nil
}

/*
handleAccountState will receive receipt from connection
then print it out
*/
func (h *Handler) handleReceipt(request network.Request) (err error) {
	receipt := &receipt.Receipt{}
	err = receipt.Unmarshal(request.Message().Body())
	if err != nil {
		return err
	}
	if h.receiptChan != nil {
		h.receiptChan <- receipt
	} else {
		logger.Debug(fmt.Sprintf("Receive receipt: %v", receipt))
		logger.Debug(fmt.Sprintf("Receive To address: %v", request.Message().ToAddress()))
		if receipt.Status() == pb.RECEIPT_STATUS_TRANSACTION_ERROR {
			transactionErr := &transaction.TransactionError{}
			transactionErr.Unmarshal(receipt.Return())
			logger.Debug("Receive Transaction error 1: ", transactionErr)
		}
	}
	return nil
}

/*
handleTransactionError will receive transaction error from parent node connection
then print it out
*/
func (h *Handler) handleEventLogs(request network.Request) error {
	eventLogs := &smart_contract.EventLogs{}
	err := eventLogs.Unmarshal(request.Message().Body())
	if err != nil {
		return err
	}
	eventLogList := eventLogs.EventLogList()
	for _, eventLog := range eventLogList {
		logger.Debug("EventLogs: ", eventLog.String())
	}
	if h.eventLogChan != nil {
		h.eventLogChan <- eventLogs
	}
	return nil
}

/*
handleStats will receive stats from connection
then print it our
*/
func (h *Handler) handleStats(request network.Request) (err error) {
	stats := &stats.Stats{}
	err = stats.Unmarshal(request.Message().Body())
	if err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("Receive Stats: \n%v", stats))
	return nil
}
