package network

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp/command"
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
	"google.golang.org/protobuf/proto"
)

var ErrorCommandNotFound = errors.New("command not found")

type Handler struct {
	accountStateChan     chan types.AccountState
	receiptChan          chan types.Receipt
	eventLogChan         chan types.EventLogs
	transactionErrorChan chan *transaction.TransactionHashWithError
	deviceKeyChan        chan types.LastDeviceKey
	nonceChan            chan uint64
	pendingRpcRequests   *sync.Map    // map[string]chan *pb.RpcResponse
	pendingChainRequests *sync.Map    // map[string]chan []byte — chain-direct responses
	eventCallbacks       sync.Map     // map[subscriptionID]func([]byte)
	receiptCallback      func([]byte) // callback khi nhận receipt forwarded từ RPC server
}

func NewHandler(
	accountStateChan chan types.AccountState,
	receiptChan chan types.Receipt,
	deviceKeyChan chan types.LastDeviceKey,
	transactionErrorChan chan *transaction.TransactionHashWithError,
	nonceChan chan uint64,
) *Handler {
	return &Handler{
		accountStateChan:     accountStateChan,
		receiptChan:          receiptChan,
		deviceKeyChan:        deviceKeyChan,
		transactionErrorChan: transactionErrorChan,
		nonceChan:            nonceChan,
	}
}

func (h *Handler) HandleRequest(request network.Request) (err error) {

	cmd := request.Message().Command()
	// logger.Debug("handler.gohandling command: " + cmd)
	switch cmd {
	case command.InitConnection:
		return h.handleInitConnection(request)
	case command.AccountState:
		return h.handleAccountState(request)
	case command.TransactionError:
		return h.handleTransactionError(request)
	case command.Receipt:
		return h.handleReceipt(request)
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
	case command.RpcResponse:
		return h.handleRpcResponse(request)
	case command.RpcEvent:
		return h.handleRpcEvent(request)

	// Chain-direct responses — dispatch bằng header ID
	case command.ChainId, command.TransactionReceipt, command.BlockNumber,
		command.Logs, command.TransactionByHash, command.Nonce, command.TransactionSuccess:
		return h.handleChainResponse(request)
	}
	return ErrorCommandNotFound
}

func (h *Handler) SetEventLogsChan(ch chan types.EventLogs) {
	h.eventLogChan = ch
}

func (h *Handler) GetEventLogsChan() chan types.EventLogs {
	return h.eventLogChan
}

func (h *Handler) SetPendingRpcRequests(pending *sync.Map) {
	h.pendingRpcRequests = pending
}

func (h *Handler) SetPendingChainRequests(pending *sync.Map) {
	h.pendingChainRequests = pending
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
	h.accountStateChan <- accountState
	return nil
}
func (h *Handler) handleTransactionError(request network.Request) (err error) {
	transactionError := &transaction.TransactionHashWithError{}
	err = transactionError.Unmarshal(request.Message().Body())
	if err != nil {
		return err
	}
	// logger.Debug(fmt.Sprintf("Receive Account state: \n%v", accountState))
	h.transactionErrorChan <- transactionError
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
	h.nonceChan <- num
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
		h.deviceKeyChan <- lastDeviceKey
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
	body := request.Message().Body()

	// Gọi receiptCallback nếu có (để nhận receipt forwarded từ RPC server)
	if h.receiptCallback != nil {
		h.receiptCallback(body)
	}

	receipt := &receipt.Receipt{}
	err = receipt.Unmarshal(body)
	if err != nil {
		return err
	}
	// Receipt sẽ được xử lý qua receiptChan, không cần log ở đây
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

// handleRpcResponse xử lý response từ RPC TCP server
// Route response đến đúng caller dựa trên request ID
func (h *Handler) handleRpcResponse(request network.Request) error {
	resp := &pb.RpcResponse{}
	if err := proto.Unmarshal(request.Message().Body(), resp); err != nil {
		logger.Error("handleRpcResponse: unmarshal error: %v", err)
		return err
	}
	// logger.Info("📥 Received RpcResponse: id=%s", resp.Id)

	if h.pendingRpcRequests == nil {
		logger.Warn("handleRpcResponse: pendingRpcRequests not set, dropping response")
		return nil
	}

	// sync.Map: LoadAndDelete - lock-free, thread-safe
	val, ok := h.pendingRpcRequests.LoadAndDelete(resp.Id)
	if ok {
		ch := val.(chan *pb.RpcResponse)
		ch <- resp
	} else {
		logger.Warn("handleRpcResponse: no pending request for id=%s, dropping", resp.Id)
	}
	return nil
}

// handleRpcEvent xử lý subscription event push từ RPC server
// Dispatch event đến callback tương ứng theo subscription_id
func (h *Handler) handleRpcEvent(request network.Request) error {
	event := &pb.RpcEvent{}
	body := request.Message().Body()
	if err := proto.Unmarshal(body, event); err != nil {
		logger.Error("handleRpcEvent: unmarshal error: %v", err)
		return err
	}
	addr := ""
	if event.Log != nil {
		addr = event.Log.Address
	}
	logger.Info("📡 Received RpcEvent: subId=%s, contract=%s", event.SubscriptionId, addr)

	// Tìm callback theo subscription_id
	if cb, ok := h.eventCallbacks.Load(event.SubscriptionId); ok {
		cb.(func([]byte))(body)
	} else {
		logger.Warn("handleRpcEvent: no callback for subId=%s, dropping event", event.SubscriptionId)
	}
	return nil
}

// RegisterEventCallback đăng ký callback cho 1 subscription ID
// Gọi sau khi RpcSubscribe trả về subID
func (h *Handler) RegisterEventCallback(subID string, cb func([]byte)) {
	h.eventCallbacks.Store(subID, cb)
}

// RemoveEventCallback xoá callback khi unsubscribe
func (h *Handler) RemoveEventCallback(subID string) {
	h.eventCallbacks.Delete(subID)
}

// RegisterReceiptCallback đăng ký callback khi nhận receipt forwarded (command "Receipt")
func (h *Handler) RegisterReceiptCallback(cb func([]byte)) {
	h.receiptCallback = cb
}

// SetEventCallback backward compat — dùng key "_default"
func (h *Handler) SetEventCallback(cb func([]byte)) {
	h.eventCallbacks.Store("_default", cb)
}

// AddEventCallback backward compat — dùng key tự tăng
func (h *Handler) AddEventCallback(cb func([]byte)) {
	h.eventCallbacks.Store("_default", cb)
}

// handleChainResponse xử lý response từ chain trực tiếp (ChainId, TransactionReceipt, BlockNumber)
// Dispatch bằng header ID — gửi raw body bytes vào channel
func (h *Handler) handleChainResponse(request network.Request) error {
	msg := request.Message()
	id := msg.ID()
	body := msg.Body()

	if h.pendingChainRequests == nil {
		logger.Warn("handleChainResponse: pendingChainRequests not set, dropping")
		return nil
	}

	val, ok := h.pendingChainRequests.LoadAndDelete(id)
	if ok {
		ch := val.(chan []byte)
		ch <- body
	} else {
		logger.Warn("handleChainResponse: no pending request for id=%s cmd=%s", id, msg.Command())
	}
	return nil
}
