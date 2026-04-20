package client

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/client_context"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/command"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/controllers"
	c_network "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/network"
	client_types "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/types"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

type Client struct {
	clientContext *client_context.ClientContext

	// mu               sync.Mutex
	accountStateChan chan types.AccountState
	receiptChan      chan types.Receipt
	receiptRequests  chan receiptRequest
	deviceKeyChan    chan types.LastDeviceKey

	transactionErrorChan  chan *mt_transaction.TransactionHashWithError
	transactionController client_types.TransactionController
	subscribeSCAddresses  []common.Address

	keepAliveStop        chan struct{}
	pendingRpcRequests   sync.Map // map[string]chan *pb.RpcResponse  — cho RPC proxy
	pendingChainRequests sync.Map // map[string]chan []byte           — cho chain-direct (header ID matching)
}

type receiptRequestType int

const (
	receiptRequestRegister receiptRequestType = iota
	receiptRequestCancel
)

type receiptMatchType int

const (
	matchByTxHash receiptMatchType = iota
	matchByReceiptHash
)

type receiptRequest struct {
	action     receiptRequestType
	txHash     common.Hash
	matchType  receiptMatchType
	responseCh chan types.Receipt
}

const (
	pendingReceiptTTL        = 60 * time.Second
	defaultKeepAliveInterval = 30 * time.Second
	defaultRpcTimeout        = 60 * time.Second
)

type pendingReceipt struct {
	value     types.Receipt
	expiresAt time.Time
}

// var client = Client{}

func NewClient(
	config *c_config.ClientConfig,
) (*Client, error) {
	clientContext := &client_context.ClientContext{
		Config: config,
	}
	client := Client{
		clientContext:    clientContext,
		accountStateChan: make(chan types.AccountState, 2000),
		receiptChan:      make(chan types.Receipt, 1),
		receiptRequests:  make(chan receiptRequest),
		deviceKeyChan:    make(chan types.LastDeviceKey, 1),

		transactionErrorChan: make(chan *mt_transaction.TransactionHashWithError, 1),
	}

	go client.runReceiptRouter()

	clientContext.KeyPair = bls.NewKeyPair(config.PrivateKey())
	clientContext.MessageSender = p_network.NewMessageSender(
		config.Version(),
	)
	clientContext.ConnectionsManager = p_network.NewConnectionsManager()
	parentConn := p_network.NewConnection(
		common.HexToAddress(config.ParentAddress),
		config.ParentConnectionType,
		nil,
	)
	logger.Info("Connecting to parent node at %s", config.ParentConnectionAddress)
	parentConn.SetRealConnAddr(config.ParentConnectionAddress)
	clientContext.Handler = c_network.NewHandler(
		client.accountStateChan,
		client.receiptChan,
		client.deviceKeyChan,
		client.transactionErrorChan,
	)
	// Set pending RPC requests vào handler (sync.Map)
	clientContext.Handler.SetPendingRpcRequests(&client.pendingRpcRequests)
	clientContext.Handler.SetPendingChainRequests(&client.pendingChainRequests)
	clientContext.SocketServer, _ = p_network.NewSocketServer(
		nil,
		clientContext.KeyPair,
		clientContext.ConnectionsManager,
		clientContext.Handler,
		config.NodeType(),
		config.Version(),
	)

	retryCount := 0
	for {
		err := parentConn.Connect()
		if err != nil {
			retryCount++
			if retryCount%10 == 1 {
				logger.Error("Failed to connect to parent node: %v, retrying every 1s...", err)
			}
			time.Sleep(1 * time.Second)
			// Recreate connection for retry
			parentConn = p_network.NewConnection(
				common.HexToAddress(config.ParentAddress),
				config.ParentConnectionType,
				nil,
			)
			parentConn.SetRealConnAddr(config.ParentConnectionAddress)
			continue
		}
		break
	}
	{
		clientContext.ConnectionsManager.AddParentConnection(parentConn)
		clientContext.SocketServer.OnConnect(parentConn)
		go clientContext.SocketServer.HandleConnection(parentConn)
		clientContext.SocketServer.StartWorkerPool() // Khởi động Worker Pool để duyệt message trong requestChan
		client.startKeepAliveLoop()
		client.clientContext.SocketServer.AddOnDisconnectedCallBack(
			client.handleParentDisconnectWithResubscribe,
		)
		// Register auto-reconnect callback

	}
	client.transactionController = controllers.NewTransactionController(
		clientContext,
	)
	return &client, nil
}

// NewClientNonBlocking tạo Client mà KHÔNG block nếu kết nối thất bại.
// Thử kết nối 1 lần: thành công → client sẵn sàng; thất bại → chạy reconnect background.
// IsParentConnected() sẽ trả false cho đến khi kết nối thành công.
func NewClientNonBlocking(
	config *c_config.ClientConfig,
) (*Client, error) {
	clientContext := &client_context.ClientContext{
		Config: config,
	}
	client := Client{
		clientContext:        clientContext,
		accountStateChan:     make(chan types.AccountState, 2000),
		receiptChan:          make(chan types.Receipt, 1),
		receiptRequests:      make(chan receiptRequest),
		deviceKeyChan:        make(chan types.LastDeviceKey, 1),
		transactionErrorChan: make(chan *mt_transaction.TransactionHashWithError, 1),
	}

	go client.runReceiptRouter()

	clientContext.KeyPair = bls.NewKeyPair(config.PrivateKey())
	clientContext.MessageSender = p_network.NewMessageSender(config.Version())
	clientContext.ConnectionsManager = p_network.NewConnectionsManager()
	clientContext.Handler = c_network.NewHandler(
		client.accountStateChan,
		client.receiptChan,
		client.deviceKeyChan,
		client.transactionErrorChan,
	)
	clientContext.Handler.SetPendingRpcRequests(&client.pendingRpcRequests)
	clientContext.Handler.SetPendingChainRequests(&client.pendingChainRequests)
	clientContext.SocketServer, _ = p_network.NewSocketServer(
		nil,
		clientContext.KeyPair,
		clientContext.ConnectionsManager,
		clientContext.Handler,
		config.NodeType(),
		config.Version(),
	)
	client.transactionController = controllers.NewTransactionController(clientContext)

	// Thử kết nối 1 lần duy nhất — không block
	parentConn := p_network.NewConnection(
		common.HexToAddress(config.ParentAddress),
		config.ParentConnectionType,
		nil,
	)
	parentConn.SetRealConnAddr(config.ParentConnectionAddress)

	if err := parentConn.Connect(); err != nil {
		logger.Warn("⚠️ [NonBlocking] Cannot connect to %s: %v → retry in background",
			config.ParentConnectionAddress, err)
		go func() {
			for {
				time.Sleep(3 * time.Second)
				if reconnErr := client.ReconnectToParent(); reconnErr == nil {
					logger.Info("✅ [NonBlocking] Connected to %s (background)", config.ParentConnectionAddress)
					go clientContext.SocketServer.HandleConnection(parentConn)
					clientContext.SocketServer.StartWorkerPool()
					client.startKeepAliveLoop()
					client.clientContext.SocketServer.AddOnDisconnectedCallBack(
						client.handleParentDisconnectWithResubscribe,
					)
					break
				}
			}
		}()
	} else {
		clientContext.ConnectionsManager.AddParentConnection(parentConn)
		clientContext.SocketServer.OnConnect(parentConn)
		go clientContext.SocketServer.HandleConnection(parentConn)
		clientContext.SocketServer.StartWorkerPool()
		client.startKeepAliveLoop()
		client.clientContext.SocketServer.AddOnDisconnectedCallBack(
			client.handleParentDisconnectWithResubscribe,
		)
	}

	return &client, nil
}

func (client *Client) GetClientContext() *client_context.ClientContext {
	return client.clientContext
}

func (client *Client) GetTransactionController() client_types.TransactionController {
	return client.transactionController
}
func (client *Client) GetAccountStateChan() chan types.AccountState {
	return client.accountStateChan
}
func (client *Client) GetDeviceKeyChan() chan types.LastDeviceKey {
	return client.deviceKeyChan
}
func (client *Client) GetRecepitChan() chan types.Receipt {
	return client.receiptChan
}

// ===================== RPC TCP Methods =====================
// Sử dụng parentConn + pendingRpcRequests map (thread-safe, hỗ trợ concurrent requests)
// sendRpcRequest gửi command qua TCP và đợi response theo ID
// Thread-safe: dùng sync.Map, mỗi request có channel riêng
func (client *Client) sendRpcRequest(cmd string, body []byte, timeout time.Duration) (*pb.RpcResponse, error) {
	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	if parentConn == nil || !parentConn.IsConnect() {
		return nil, fmt.Errorf("parent connection not available")
	}
	// logger.Info("📤 Sending %s to RPC TCP server... (wallet=%s)", cmd, client.clientContext.Config.ParentAddress)
	// Tạo UUID riêng cho request
	reqID := uuid.New().String()
	respCh := make(chan *pb.RpcResponse, 1)

	// Register pending request vào sync.Map TRƯỚC khi gửi
	client.pendingRpcRequests.Store(reqID, respCh)

	// Tạo message proto với ID tự chọn
	walletAddr := common.HexToAddress(client.clientContext.Config.ParentAddress)
	msg := p_network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command:   cmd,
			ToAddress: walletAddr.Bytes(),
			ID:        reqID,
		},
		Body: body,
	})

	// logger.Info("📤 Sending %s (id=%s) to RPC TCP server...", cmd, reqID)

	// Gửi trực tiếp qua connection
	err := parentConn.SendMessage(msg)
	if err != nil {
		client.pendingRpcRequests.Delete(reqID)
		return nil, fmt.Errorf("failed to send %s: %w", cmd, err)
	}

	// Đợi response theo ID
	select {
	case resp := <-respCh:
		return resp, nil
	case <-time.After(timeout):
		client.pendingRpcRequests.Delete(reqID)
		return nil, fmt.Errorf("timeout waiting for %s response (id=%s)", cmd, reqID)
	}
}

// RpcGetChainId gửi command eth_getChainId qua TCP
func (client *Client) RpcGetChainId() (string, error) {
	resp, err := client.sendRpcRequest("eth_getChainId", nil, defaultRpcTimeout)
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("RPC error: code=%d, message=%s", resp.Error.Code, resp.Error.Message)
	}
	var chainIdHex string
	if err := json.Unmarshal(resp.Result, &chainIdHex); err != nil {
		return string(resp.Result), nil
	}
	logger.Info("✅ RpcGetChainId result: %s", chainIdHex)
	return chainIdHex, nil
}

// RpcSendRawTransaction gửi raw transaction hex qua TCP
// Dùng proto TcpSendTxRequest (binary) thay vì JSON → nhanh hơn ~50%
func (client *Client) RpcSendRawTransaction(rawTxHex string) (string, error) {
	// Decode hex → raw bytes
	hexStr := strings.TrimPrefix(rawTxHex, "0x")
	rawBytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return "", fmt.Errorf("invalid raw tx hex: %w", err)
	}
	// Encode proto: gửi binary trực tiếp
	tcpReq := &pb.TcpSendTxRequest{RawTx: rawBytes}
	body, err := proto.Marshal(tcpReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal TcpSendTxRequest: %w", err)
	}
	resp, err := client.sendRpcRequest("eth_sendRawTransaction", body, defaultRpcTimeout)
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("RPC error: code=%d, message=%s, data=%s",
			resp.Error.Code, resp.Error.Message, resp.Error.Data)
	}
	// Parse proto TcpHashParam response
	hashResp := &pb.TcpHashParam{}
	if err := proto.Unmarshal(resp.Result, hashResp); err != nil {
		return "", fmt.Errorf("failed to parse TcpHashParam response: %w", err)
	}
	txHash := common.BytesToHash(hashResp.Hash).Hex()
	logger.Info("✅ RpcSendRawTransaction result: %s", txHash)
	return txHash, nil
}

// RpcHttpSendRawTransaction gửi raw transaction qua HTTP forward (http_sendRawTransaction command)
// Khác với RpcSendRawTransaction (TCP-direct to chain), method này forward qua HTTP proxy
func (client *Client) RpcHttpSendRawTransaction(rawTxHex string) (string, error) {
	params, _ := json.Marshal([]string{rawTxHex})
	resp, err := client.sendRpcRequest("http_sendRawTransaction", params, defaultRpcTimeout)
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("RPC error: code=%d, message=%s, data=%s",
			resp.Error.Code, resp.Error.Message, resp.Error.Data)
	}
	var txHash string
	if err := json.Unmarshal(resp.Result, &txHash); err != nil {
		return string(resp.Result), nil
	}
	logger.Info("✅ RpcHttpSendRawTransaction result: %s", txHash)
	return txHash, nil
}

// RpcEthCall gọi eth_call qua TCP (đọc contract, không tạo giao dịch)
// Dùng proto TcpEthCallRequest (binary) thay vì JSON → nhanh hơn
func (client *Client) RpcEthCall(to common.Address, data []byte) ([]byte, error) {
	tcpReq := &pb.TcpEthCallRequest{
		To:   to.Bytes(),
		Data: data, // raw ABI-encoded bytes, không cần hex encode
	}
	body, err := proto.Marshal(tcpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal TcpEthCallRequest: %w", err)
	}
	resp, err := client.sendRpcRequest("eth_call", body, defaultRpcTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error: code=%d, message=%s, data=%s",
			resp.Error.Code, resp.Error.Message, resp.Error.Data)
	}
	// Server trả raw bytes trực tiếp — không cần hex decode
	logger.Info("✅ RpcEthCall result: %d bytes", len(resp.Result))
	return resp.Result, nil
}

// RpcGetPendingNonce lấy pending nonce cho address qua TCP
// Dùng proto TcpGetNonceRequest/TcpGetNonceResponse (binary, không JSON)
func (client *Client) RpcGetPendingNonce(address common.Address) (uint64, error) {
	tcpReq := &pb.TcpAddressParam{Address: address.Bytes()}
	body, err := proto.Marshal(tcpReq)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal TcpGetNonceRequest: %w", err)
	}
	resp, err := client.sendRpcRequest("eth_getTransactionCount", body, defaultRpcTimeout)
	if err != nil {
		return 0, err
	}
	if resp.Error != nil {
		return 0, fmt.Errorf("RPC error: code=%d, message=%s", resp.Error.Code, resp.Error.Message)
	}
	// Parse proto TcpGetNonceResponse
	nonceResp := &pb.TcpGetNonceResponse{}
	if err := proto.Unmarshal(resp.Result, nonceResp); err != nil {
		return 0, fmt.Errorf("failed to parse TcpGetNonceResponse: %w", err)
	}
	return nonceResp.Nonce, nil
}

// RpcGetTransactionReceipt lấy receipt của transaction qua TCP
// Gửi TcpHashParam (binary hash), nhận RpcReceipt proto
func (client *Client) RpcGetTransactionReceipt(txHash string) (*pb.RpcReceipt, error) {
	hashBytes := common.HexToHash(txHash)
	tcpReq := &pb.TcpHashParam{Hash: hashBytes.Bytes()}
	body, err := proto.Marshal(tcpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal TcpHashParam: %w", err)
	}
	resp, err := client.sendRpcRequest("eth_getTransactionReceipt", body, defaultRpcTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error: code=%d, message=%s", resp.Error.Code, resp.Error.Message)
	}
	if len(resp.Result) == 0 {
		return nil, nil // Receipt chưa có (pending)
	}

	// Parse protobuf RpcReceipt
	receipt := &pb.RpcReceipt{}
	if err := proto.Unmarshal(resp.Result, receipt); err != nil {
		logger.Warn("RpcGetTransactionReceipt: proto unmarshal failed, raw=%d bytes", len(resp.Result))
		return nil, fmt.Errorf("failed to parse receipt proto: %w", err)
	}
	logger.Info("✅ RpcGetTransactionReceipt: %s (status=%s, gasUsed=%s)", txHash, receipt.Status, receipt.GasUsed)
	return receipt, nil
}

// RpcSubscribe gửi eth_subscribe qua TCP
// contractAddrs: danh sách contract address cần subscribe
// topics: danh sách topic hashes (ví dụ: event signature hash)
// callback: hàm xử lý event khi nhận được (nhận raw protobuf RpcEvent bytes)
// Trả về subscription ID
func (client *Client) RpcSubscribe(contractAddrs []string, topics []string, callback func([]byte)) (string, error) {
	// Convert hex strings → bytes
	tcpReq := &pb.TcpSubscribeRequest{}
	for _, addr := range contractAddrs {
		tcpReq.Addresses = append(tcpReq.Addresses, common.HexToAddress(addr).Bytes())
	}
	for _, topic := range topics {
		tcpReq.Topics = append(tcpReq.Topics, common.HexToHash(topic).Bytes())
	}

	body, err := proto.Marshal(tcpReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal TcpSubscribeRequest: %w", err)
	}
	resp, err := client.sendRpcRequest("eth_subscribe", body, defaultRpcTimeout)
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("RPC error: code=%d, message=%s", resp.Error.Code, resp.Error.Message)
	}
	// Server trả subscription ID as raw string bytes
	subID := string(resp.Result)

	// Đăng ký callback cho subscription này
	client.clientContext.Handler.RegisterEventCallback(subID, callback)
	logger.Info("✅ RpcSubscribe: id=%s, contracts=%v, topics=%v", subID, contractAddrs, topics)
	return subID, nil
}

// RpcUnsubscribe gửi eth_unsubscribe qua TCP + xoá callback
// Dùng proto TcpUnsubscribeRequest
func (client *Client) RpcUnsubscribe(subID string) (bool, error) {
	tcpReq := &pb.TcpUnsubscribeRequest{SubscriptionId: subID}
	body, err := proto.Marshal(tcpReq)
	if err != nil {
		return false, fmt.Errorf("failed to marshal TcpUnsubscribeRequest: %w", err)
	}
	resp, err := client.sendRpcRequest("eth_unsubscribe", body, defaultRpcTimeout)
	if err != nil {
		return false, err
	}
	if resp.Error != nil {
		return false, fmt.Errorf("RPC error: code=%d, message=%s", resp.Error.Code, resp.Error.Message)
	}
	// Server trả 1 byte: 1=true, 0=false
	result := len(resp.Result) > 0 && resp.Result[0] == 1

	// Xoá callback
	client.clientContext.Handler.RemoveEventCallback(subID)
	logger.Info("✅ RpcUnsubscribe: id=%s, result=%v", subID, result)
	return result, nil
}

// ================================== END RPC======================================

func (client *Client) startKeepAliveLoop() {
	client.keepAliveStop = make(chan struct{})
	go func() {
		ticker := time.NewTicker(defaultKeepAliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-client.keepAliveStop:
				return
			case <-ticker.C:
				parentConn := client.clientContext.ConnectionsManager.ParentConnection()
				if parentConn == nil || !parentConn.IsConnect() {
					continue
				}
				if err := client.clientContext.MessageSender.SendBytes(parentConn, command.Ping, nil); err != nil {
					logger.Warn("KeepAlive: failed to send ping to parent: %v", err)
				}
			}
		}
	}()
}

type receiptWaiter struct {
	ch        chan types.Receipt
	matchType receiptMatchType
	txHash    common.Hash
}

func (client *Client) runReceiptRouter() {
	waiters := make(map[common.Hash][]receiptWaiter)
	pendingReceipts := make(map[common.Hash]pendingReceipt)
	cleanupTicker := time.NewTicker(pendingReceiptTTL)
	defer cleanupTicker.Stop()

	for {
		select {
		case receipt := <-client.receiptChan:
			if receipt == nil {
				continue
			}

			txHash := receipt.TransactionHash()
			receiptHash := receipt.RHash()
			// Kiểm tra waiters dựa trên TransactionHash
			if chans, ok := waiters[txHash]; ok {
				remainingWaiters := []receiptWaiter{}
				for _, waiter := range chans {
					matched := false
					if waiter.matchType == matchByTxHash && waiter.txHash == txHash {
						matched = true
					}

					if matched {
						select {
						case waiter.ch <- receipt:
						default:
							logger.Warn("Receipt waiter channel full, dropping receipt for txHash %s", txHash.Hex())
						}
					} else {
						remainingWaiters = append(remainingWaiters, waiter)
					}
				}

				if len(remainingWaiters) == 0 {
					delete(waiters, txHash)
				} else {
					waiters[txHash] = remainingWaiters
				}
			}

			// Kiểm tra waiters dựa trên RHash (cho matchByReceiptHash)
			if chans, ok := waiters[receiptHash]; ok {
				remainingWaiters := []receiptWaiter{}
				for _, waiter := range chans {
					matched := false
					if waiter.matchType == matchByReceiptHash && waiter.txHash == receiptHash {
						matched = true
					}

					if matched {
						select {
						case waiter.ch <- receipt:
						default:
							logger.Warn("Receipt waiter channel full, dropping receipt for RHash %s", receiptHash.Hex())
						}
					} else {
						remainingWaiters = append(remainingWaiters, waiter)
					}
				}

				if len(remainingWaiters) == 0 {
					delete(waiters, receiptHash)
				} else {
					waiters[receiptHash] = remainingWaiters
				}
			}

			pendingReceipts[txHash] = pendingReceipt{
				value:     receipt,
				expiresAt: time.Now().Add(pendingReceiptTTL),
			}

		case req := <-client.receiptRequests:
			switch req.action {
			case receiptRequestRegister:
				if pending, ok := pendingReceipts[req.txHash]; ok {
					if time.Now().After(pending.expiresAt) {
						delete(pendingReceipts, req.txHash)
						break
					}
					delete(pendingReceipts, req.txHash)
					select {
					case req.responseCh <- pending.value:
					default:
						logger.Warn("Receipt response channel full, dropping receipt for txHash %s", req.txHash.Hex())
					}
					continue
				}
				waiters[req.txHash] = append(waiters[req.txHash], receiptWaiter{
					ch:        req.responseCh,
					matchType: req.matchType,
					txHash:    req.txHash,
				})

			case receiptRequestCancel:
				if chans, ok := waiters[req.txHash]; ok {
					for i, waiter := range chans {
						if waiter.ch == req.responseCh {
							chans = append(chans[:i], chans[i+1:]...)
							break
						}
					}
					if len(chans) == 0 {
						delete(waiters, req.txHash)
					} else {
						waiters[req.txHash] = chans
					}
				}
			}

		case <-cleanupTicker.C:
			now := time.Now()
			for hash, pending := range pendingReceipts {
				if now.After(pending.expiresAt) {
					delete(pendingReceipts, hash)
				}
			}
		}
	}
}

func (client *Client) waitReceipt(txHash common.Hash, matchType receiptMatchType, timeout time.Duration) (types.Receipt, error) {
	responseCh := make(chan types.Receipt, 1)

	client.receiptRequests <- receiptRequest{
		action:     receiptRequestRegister,
		txHash:     txHash,
		matchType:  matchType,
		responseCh: responseCh,
	}

	if timeout <= 0 {
		receipt := <-responseCh
		return receipt, nil
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case receipt := <-responseCh:
		return receipt, nil
	case <-timer.C:
		client.receiptRequests <- receiptRequest{
			action:     receiptRequestCancel,
			txHash:     txHash,
			matchType:  matchType,
			responseCh: responseCh,
		}
		select {
		case receipt := <-responseCh:
			return receipt, nil
		default:
		}
		return nil, fmt.Errorf("timeout (%s) waiting for receipt with txHash %s", timeout, txHash.Hex())
	}
}

func (client *Client) FindReceiptByHash(txHash common.Hash) (types.Receipt, error) {
	timeout := 20 * time.Second
	return client.waitReceipt(txHash, matchByTxHash, timeout)
}

func (client *Client) FindReceiptByHashWithType(txHash common.Hash, matchType receiptMatchType) (types.Receipt, error) {
	timeout := 20 * time.Second
	return client.waitReceipt(txHash, matchType, timeout)
}

// IsParentConnected checks if the parent connection is established and active.
func (client *Client) IsParentConnected() bool {
	if client.clientContext == nil || client.clientContext.ConnectionsManager == nil {
		return false
	}
	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	return parentConn != nil && parentConn.IsConnect()
}

func (client *Client) ReconnectToParent() error {
	parentConn := p_network.NewConnection(
		common.HexToAddress(client.clientContext.Config.ParentAddress),
		client.clientContext.Config.ParentConnectionType,
		nil,
	)
	parentConn.SetRealConnAddr(client.clientContext.Config.ParentConnectionAddress)
	err := parentConn.Connect()
	if err != nil {
		return err
	} else {
		client.clientContext.ConnectionsManager.AddParentConnection(parentConn)
		client.clientContext.SocketServer.OnConnect(parentConn)
		go client.clientContext.SocketServer.HandleConnection(parentConn)
	}
	return nil
}

func (client *Client) SendTransaction(
	fromAddress common.Address,
	toAddress common.Address,
	amount *big.Int,
	data []byte,
	relatedAddress []common.Address,
	maxGas uint64,
	maxGasPrice uint64,
	maxTimeUse uint64,
) (types.Receipt, error) {

	if client.clientContext == nil || client.clientContext.ConnectionsManager == nil {
		return nil, fmt.Errorf("client not ready: clientContext or ConnectionsManager is nil")
	}

	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	if parentConn == nil || !parentConn.IsConnect() {
		if err := client.ReconnectToParent(); err != nil {
			return nil, err
		}
	}

	client.clientContext.MessageSender.SendBytes(parentConn, command.GetAccountState, fromAddress.Bytes())

	lastDeviceKey := common.HexToHash("0000000000000000000000000000000000000000000000000000000000000000")
	newDeviceKey := common.HexToHash("0000000000000000000000000000000000000000000000000000000000000000")

	// Lấy nonce qua sendChainRequest (ID matching, thread-safe)
	nonceResp, err := client.sendChainRequest(command.GetNonce, fromAddress.Bytes(), 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("get nonce failed: %w", err)
	}
	var nonce uint64
	if len(nonceResp) >= 8 {
		nonce = binary.BigEndian.Uint64(nonceResp)
	}
	logger.Info("Nonce : %d", nonce)

	var as types.AccountState
	select {
	case as = <-client.accountStateChan:
	case <-time.After(10 * time.Second):
		logger.DebugP("Timeout waiting for account state")
		return nil, fmt.Errorf("timeout waiting for account state")
	}

	pendingBalance := as.PendingBalance()

	bRelatedAddresses := make([][]byte, len(relatedAddress))
	for i, v := range relatedAddress {
		bRelatedAddresses[i] = v.Bytes()
	}

	tx, err := client.transactionController.SendTransaction(
		fromAddress,
		toAddress,
		pendingBalance,
		amount,
		maxGas,
		maxGasPrice,
		maxTimeUse,
		data,
		bRelatedAddresses,
		lastDeviceKey,
		newDeviceKey,
		as.Nonce(),
		client.clientContext.Config.ChainId,
	)
	if err != nil {
		return nil, err
	}

	receipt, err := client.FindReceiptByHash(tx.Hash())
	if err != nil {
		logger.DebugP(err.Error())
		return nil, err
	}
	return receipt, nil
}

func (client *Client) ReadTransaction(
	fromAddress common.Address,
	toAddress common.Address,
	amount *big.Int,
	data []byte,
	relatedAddress []common.Address,
	maxGas uint64,
	maxGasPrice uint64,
	maxTimeUse uint64,
) (types.Receipt, error) {
	if client.clientContext == nil || client.clientContext.ConnectionsManager == nil {
		return nil, fmt.Errorf("client not ready: clientContext or ConnectionsManager is nil")
	}
	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	if parentConn == nil || !parentConn.IsConnect() {
		logger.Error("Parent connection is not connected, reconnecting...")
		if err := client.ReconnectToParent(); err != nil {
			return nil, err
		}
	}
	// Gửi yêu cầu lấy account state
	lastDeviceKey := common.HexToHash("0000000000000000000000000000000000000000000000000000000000000000")
	newDeviceKey := common.HexToHash("0000000000000000000000000000000000000000000000000000000000000000")
	bRelatedAddresses := make([][]byte, len(relatedAddress))
	for i, v := range relatedAddress {
		bRelatedAddresses[i] = v.Bytes()
	}
	tx, err := client.transactionController.ReadTransaction(
		fromAddress,
		toAddress,
		big.NewInt(0),
		amount,
		maxGas,
		maxGasPrice,
		maxTimeUse,
		data,
		bRelatedAddresses,
		lastDeviceKey,
		newDeviceKey,
		client.clientContext.Config.ChainId,
	)
	if err != nil {
		return nil, err
	}
	logger.Info("[Client] Tx Hash : %v", tx.Hash().Hex())
	receipt, err := client.FindReceiptByHashWithType(tx.Hash(), matchByReceiptHash)
	if err != nil {
		return nil, err
	}
	return receipt, nil
}

func (client *Client) AddAccountForClient(privateKey string, chainId string) (types.Receipt, error) {
	bigIntChainId, success := new(big.Int).SetString(chainId, 10)
	if !success {
		logger.Info("Chuyển đổi thất bại cho chuỗi: %s\n", chainId)
		return nil, fmt.Errorf("chuyển đổi thất bại cho chuỗi: %s", chainId)
	}
	publickey := client.clientContext.KeyPair.PublicKey().String()
	ethTx, err := CreateSignedSetBLSPublicKeyTx(privateKey, publickey, bigIntChainId)
	if err != nil {
		return nil, err
	}

	// Lấy kết nối tới Parent Node
	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	if parentConn == nil || !parentConn.IsConnect() {
		err := client.ReconnectToParent()
		if err != nil {
			return nil, err
		}
	}
	signer := e_types.NewEIP155Signer(bigIntChainId)

	from, err := e_types.Sender(signer, ethTx)

	if err != nil {
		return nil, fmt.Errorf("transaction not found Sender")
	}
	// Gửi yêu cầu lấy trạng thái tài khoản
	client.clientContext.MessageSender.SendBytes(
		parentConn,
		command.GetAccountState,
		from.Bytes(),
	)
	as := <-client.accountStateChan
	logger.Info("as", as)

	newDeviceKey := common.HexToHash(
		"0000000000000000000000000000000000000000000000000000000000000000",
	)
	rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d", hex.EncodeToString(as.LastHash().Bytes()), time.Now().Unix()))

	rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)

	deviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

	bRelatedAddresses := make([][]byte, 0)

	transaction, err := mt_transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		return nil, fmt.Errorf("error buidl  NewTransactionFromEth: %w", err)
	}
	txByte, _ := ethTx.MarshalJSON()
	fmt.Println(string(txByte))
	logger.Info(transaction)

	transaction.UpdateRelatedAddresses(bRelatedAddresses)
	transaction.UpdateDeriver(deviceKey, newDeviceKey)
	transaction.SetSign(client.clientContext.KeyPair.PrivateKey())

	tx, err := client.transactionController.SendNewTransactionWithDeviceKey(transaction, newDeviceKey.Bytes())
	if err != nil {
		return nil, err
	}

	receipt, err := client.FindReceiptByHash(tx.Hash())
	if err != nil {
		return nil, err
	}
	return receipt, nil
}

func (client *Client) BuildTransactionTx0(
	ethTx *e_types.Transaction,
	as types.AccountState,
) (types.Transaction, error) {
	newDeviceKey := common.HexToHash(
		"0000000000000000000000000000000000000000000000000000000000000000",
	)

	rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d", hex.EncodeToString(as.LastHash().Bytes()), time.Now().Unix()))

	rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)

	deviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

	bRelatedAddresses := make([][]byte, 0)

	transaction, err := mt_transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		return nil, fmt.Errorf("error buidl  NewTransactionFromEth: %w", err)
	}

	transaction.UpdateRelatedAddresses(bRelatedAddresses)
	transaction.UpdateDeriver(deviceKey, newDeviceKey)
	transaction.SetSign(client.clientContext.KeyPair.PrivateKey())

	return transaction, err
}

func CreateSignedSetBLSPublicKeyTx(
	privateKeyHex string,
	blsPubKeyHex string,
	chainID *big.Int,
) (*e_types.Transaction, error) {

	// Parse private key
	privateKey, err := crypto.HexToECDSA(strings.TrimPrefix(privateKeyHex, "0x"))
	if err != nil {
		return nil, fmt.Errorf("lỗi khi parse private key: %v", err)
	}

	// Decode BLS public key
	blsPubKeyBytes, err := hex.DecodeString(strings.TrimPrefix(blsPubKeyHex, "0x"))
	if err != nil {
		return nil, fmt.Errorf("lỗi decode BLS public key: %v", err)
	}

	// Parse contract address
	contractAddr := common.HexToAddress("0x00000000000000000000000000000000D844bb55")

	// ABI JSON
	abiJSON := `[{"inputs":[{"internalType":"bytes","name":"publicKey","type":"bytes"}],"name":"setBlsPublicKey","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"}]`
	parsedABI, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		return nil, fmt.Errorf("lỗi khi parse ABI: %v", err)
	}

	// Tạo data gọi hàm
	data, err := parsedABI.Pack("setBlsPublicKey", blsPubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("lỗi đóng gói dữ liệu ABI: %v", err)
	}

	// Tạo transaction
	tx := e_types.NewTransaction(0, contractAddr, big.NewInt(0), 1000000000, big.NewInt(100000), data)
	// Ký transaction
	// publicKeyECDSA := privateKey.Public().(*ecdsa.PublicKey)
	// fromAddr := crypto.PubkeyToAddress(*publicKeyECDSA)

	signer := e_types.LatestSignerForChainID(chainID)
	signedTx, err := e_types.SignTx(tx, signer, privateKey)
	if err != nil {
		return nil, fmt.Errorf("lỗi khi ký giao dịch: %v", err)
	}

	return signedTx, nil
}

func (client *Client) SendTransactionWithDeviceKey(
	fromAddress common.Address,
	toAddress common.Address,
	amount *big.Int,
	data []byte,
	relatedAddress []common.Address,
	maxGas uint64,
	maxGasPrice uint64,
	maxTimeUse uint64,
) (types.Receipt, error) {
	// Lấy kết nối tới Parent Node
	parentConn := client.clientContext.ConnectionsManager.ParentConnection()

	if parentConn == nil || !parentConn.IsConnect() {
		err := client.ReconnectToParent()
		if err != nil {
			return nil, err
		}
		parentConn = client.clientContext.ConnectionsManager.ParentConnection()
	}
	// Gửi yêu cầu lấy trạng thái tài khoản
	client.clientContext.MessageSender.SendBytes(
		parentConn,
		command.GetAccountState,
		fromAddress.Bytes(),
	)
	logger.Info("TcpRemoteAddr: %v", parentConn.TcpRemoteAddr())
	logger.Info("TcpLocalAddr: %v", parentConn.TcpLocalAddr())
	// Lắng nghe tài khoản trong kênh accountStateChan bằng for range
	for as := range client.accountStateChan {
		// Nếu không phải tài khoản mong muốn, tiếp tục lắng nghe mà không bỏ dữ liệu
		if as.Address() != fromAddress {
			// Gửi lại dữ liệu cho luồng khác đọc (không bỏ dữ liệu)
			client.accountStateChan <- as
			time.Sleep(50 * time.Millisecond) // Delay trước khi tiếp tục lặp
			continue
		}
		// Nếu tìm thấy tài khoản phù hợp, xử lý giao dịch
		lastHash := as.LastHash()
		pendingBalance := as.PendingBalance()

		err := client.clientContext.MessageSender.SendBytes(
			parentConn,
			"GetDeviceKey",
			lastHash.Bytes(),
		)

		if err != nil {
			return nil, err
		}

		// Lắng nghe deviceKey từ server
		receiveDeviceKey := <-client.deviceKeyChan
		TransactionHash := receiveDeviceKey.TransactionHash
		lastDeviceKey := common.HexToHash(
			hex.EncodeToString(receiveDeviceKey.LastDeviceKeyFromServer),
		)

		// Tạo khóa thiết bị mới
		rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d", hex.EncodeToString(TransactionHash), time.Now().Unix()))
		rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)
		newDeviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

		// Chuyển đổi danh sách địa chỉ liên quan sang mảng byte
		bRelatedAddresses := make([][]byte, len(relatedAddress))
		for i, v := range relatedAddress {
			bRelatedAddresses[i] = v.Bytes()
		}
		// Gửi giao dịch với device key
		tx, err := client.transactionController.SendTransactionWithDeviceKey(
			fromAddress,
			toAddress,
			pendingBalance,
			amount,
			maxGas,
			maxGasPrice,
			maxTimeUse,
			data,
			bRelatedAddresses,
			lastDeviceKey,
			newDeviceKey,
			as.Nonce(),
			rawNewDeviceKey,
			client.clientContext.Config.ChainId,
		)
		if err != nil {
			return nil, err
		}

		// Chờ biên lai giao dịch (receipt) hoặc lỗi từ transactionErrorChan
		responseCh := make(chan types.Receipt, 1)
		client.receiptRequests <- receiptRequest{
			action:     receiptRequestRegister,
			txHash:     tx.Hash(),
			matchType:  matchByTxHash,
			responseCh: responseCh,
		}

		timeout := time.NewTimer(20 * time.Second)
		defer timeout.Stop()

		select {
		case receipt := <-responseCh:
			// logger.Info("Receipt Data: %v", receipt)
			return receipt, nil
		case txErr := <-client.transactionErrorChan:
			client.receiptRequests <- receiptRequest{
				action:     receiptRequestCancel,
				txHash:     tx.Hash(),
				matchType:  matchByTxHash,
				responseCh: responseCh,
			}
			logger.Error("transaction error:txHash %v \n desc %s \n output %s", common.BytesToHash(txErr.Proto().Hash), txErr.Proto().Description, common.Bytes2Hex(txErr.Proto().Output))
			return nil, fmt.Errorf("transaction error: output %s", common.BytesToHash(txErr.Proto().Output))
		case <-timeout.C:
			client.receiptRequests <- receiptRequest{
				action:     receiptRequestCancel,
				txHash:     tx.Hash(),
				matchType:  matchByTxHash,
				responseCh: responseCh,
			}
			// Kiểm tra lần cuối nếu có receipt
			select {
			case receipt := <-responseCh:
				return receipt, nil
			default:
			}
			return nil, fmt.Errorf("timeout (20s) waiting for receipt with txHash %s", tx.Hash().Hex())
		}
	}

	// Nếu kênh accountStateChan bị đóng, trả lỗi
	return nil, fmt.Errorf("account state channel closed unexpectedly")
}

func (client *Client) SendAllTransactionsInDirectory(
	directoryPath string, // Đường dẫn đến thư mục chứa các tệp giao dịch

) error {

	// Gửi giao dịch với device key
	err := client.transactionController.SendAllTransactionsInDirectory(
		directoryPath,
	)
	if err != nil {
		return err
	}

	return nil

	// Nếu kênh accountStateChan bị đóng, trả lỗi
}

func (client *Client) SaveTransactionWithDeviceKeyToFile(
	fromAddress common.Address,
	toAddress common.Address,
	amount *big.Int,
	data []byte,
	relatedAddress []common.Address,
	maxGas uint64,
	maxGasPrice uint64,
	maxTimeUse uint64,
) error {
	// Lấy kết nối tới Parent Node
	logger.Info("SaveTransactionWithDeviceKeyToFile 1")
	parentConn := client.clientContext.ConnectionsManager.ParentConnection()

	if parentConn == nil || !parentConn.IsConnect() {
		err := client.ReconnectToParent()
		if err != nil {
			return err
		}
		parentConn = client.clientContext.ConnectionsManager.ParentConnection()
	}
	// Gửi yêu cầu lấy trạng thái tài khoản
	client.clientContext.MessageSender.SendBytes(
		parentConn,
		command.GetAccountState,
		fromAddress.Bytes(),
	)
	logger.Info("TcpRemoteAddr: %v", parentConn.TcpRemoteAddr())

	logger.Info("TcpLocalAddr: %v", parentConn.TcpLocalAddr())
	// Lắng nghe tài khoản trong kênh accountStateChan bằng for range
	for as := range client.accountStateChan {
		// Nếu không phải tài khoản mong muốn, tiếp tục lắng nghe mà không bỏ dữ liệu
		if as.Address() != fromAddress {
			// Gửi lại dữ liệu cho luồng khác đọc (không bỏ dữ liệu)
			client.accountStateChan <- as
			time.Sleep(50 * time.Millisecond) // Delay trước khi tiếp tục lặp
			continue
		}

		// Nếu tìm thấy tài khoản phù hợp, xử lý giao dịch
		lastHash := as.LastHash()
		pendingBalance := as.PendingBalance()

		err := client.clientContext.MessageSender.SendBytes(
			parentConn,
			"GetDeviceKey",
			lastHash.Bytes(),
		)

		if err != nil {
			return err
		}

		// Lắng nghe deviceKey từ server
		receiveDeviceKey := <-client.deviceKeyChan
		TransactionHash := receiveDeviceKey.TransactionHash
		lastDeviceKey := common.HexToHash(
			hex.EncodeToString(receiveDeviceKey.LastDeviceKeyFromServer),
		)

		// Tạo khóa thiết bị mới
		rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d", hex.EncodeToString(TransactionHash), time.Now().Unix()))
		rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)
		newDeviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

		// Chuyển đổi danh sách địa chỉ liên quan sang mảng byte
		bRelatedAddresses := make([][]byte, len(relatedAddress)+2)
		for i, v := range relatedAddress {
			bRelatedAddresses[i] = v.Bytes()
		}
		bRelatedAddresses[len(relatedAddress)] = fromAddress.Bytes()
		bRelatedAddresses[len(relatedAddress)+1] = toAddress.Bytes()

		// Gửi giao dịch với device key
		err = client.transactionController.SaveTransactionWithDeviceKeyToFile(
			fromAddress,
			toAddress,
			pendingBalance,
			amount,
			maxGas,
			maxGasPrice,
			maxTimeUse,
			data,
			bRelatedAddresses,
			lastDeviceKey,
			newDeviceKey,
			as.Nonce(),
			rawNewDeviceKey,
			client.clientContext.Config.ChainId,
		)
		if err != nil {
			return err
		}

		// Chờ biên lai giao dịch (receipt)
		return nil
	}

	// Nếu kênh accountStateChan bị đóng, trả lỗi
	return fmt.Errorf("account state channel closed unexpectedly")
}

func (client *Client) AccountState(address common.Address) (types.AccountState, error) {
	// client.mu.Lock()
	// defer client.mu.Unlock()
	// get account state
	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	client.clientContext.MessageSender.SendBytes(
		parentConn,
		command.GetAccountState,
		address.Bytes(),
	)
	as := <-client.accountStateChan
	return as, nil
}

func (client *Client) Get(address common.Address) (types.AccountState, error) {
	// client.mu.Lock()
	// defer client.mu.Unlock()
	// get account state
	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	client.clientContext.MessageSender.SendBytes(
		parentConn,
		command.GetAccountState,
		address.Bytes(),
	)
	as := <-client.accountStateChan
	return as, nil
}

func NewStorageClient(
	config *c_config.ClientConfig,
	listSCAddress []common.Address,
) (*Client, error) {
	clientContext := &client_context.ClientContext{
		Config: config,
	}

	client := Client{
		clientContext:        clientContext,
		accountStateChan:     make(chan types.AccountState, 1),
		receiptChan:          make(chan types.Receipt, 1),
		receiptRequests:      make(chan receiptRequest),
		transactionErrorChan: make(chan *mt_transaction.TransactionHashWithError, 1),
		subscribeSCAddresses: listSCAddress,
	}

	go client.runReceiptRouter()

	clientContext.KeyPair = bls.NewKeyPair(config.PrivateKey())
	clientContext.MessageSender = p_network.NewMessageSender(
		config.Version(),
	)
	clientContext.ConnectionsManager = p_network.NewConnectionsManager()
	parentConn := p_network.NewConnection(
		common.HexToAddress(config.ParentAddress),
		config.ParentConnectionType,
		nil,
	)
	clientContext.Handler = c_network.NewHandler(
		client.accountStateChan,
		client.receiptChan,
		client.deviceKeyChan,
		client.transactionErrorChan,
	)
	clientContext.SocketServer, _ = p_network.NewSocketServer(
		nil,
		clientContext.KeyPair,
		clientContext.ConnectionsManager,
		clientContext.Handler,
		config.NodeType(),
		config.Version(),
	)
	err := parentConn.Connect()
	if err != nil {
		return nil, err
	} else {
		// init connection
		clientContext.ConnectionsManager.AddParentConnection(parentConn)
		clientContext.SocketServer.OnConnect(parentConn)
		go clientContext.SocketServer.HandleConnection(parentConn)
	}

	for _, address := range listSCAddress {
		err = client.clientContext.MessageSender.SendBytes(parentConn, command.SubscribeToAddress, address.Bytes())
		if err != nil {
			return nil, fmt.Errorf("unable to send subscribe")
		}
	}

	client.transactionController = controllers.NewTransactionController(
		clientContext,
	)

	evenLogsChan := make(chan types.EventLogs)
	client.clientContext.Handler.(*c_network.Handler).SetEventLogsChan(evenLogsChan)
	client.clientContext.SocketServer.AddOnDisconnectedCallBack(client.RetryConnectToStorage)

	return &client, nil
}

func (client *Client) Subcribe(
	storageAddress common.Address,
	smartContractAddress common.Address,
) (chan types.EventLogs, error) {
	storageConnection := p_network.NewConnection(
		storageAddress,
		p_common.STORAGE_CONNECTION_TYPE,
		nil,
	)
	err := storageConnection.Connect()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to storage")
	}
	go client.clientContext.SocketServer.HandleConnection(storageConnection)

	err = client.clientContext.MessageSender.SendBytes(
		storageConnection,
		command.SubscribeToAddress,
		smartContractAddress.Bytes(),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to send subscribe")
	}
	evenLogsChan := make(chan types.EventLogs)
	client.clientContext.Handler.(*c_network.Handler).SetEventLogsChan(evenLogsChan)
	return evenLogsChan, nil
}

func (client *Client) Subcribes(
	storageAddress common.Address,
	listSCAddress []common.Address,
) (chan types.EventLogs, error) {
	storageConnection := p_network.NewConnection(
		storageAddress,
		p_common.STORAGE_CONNECTION_TYPE,
		nil,
	)
	err := storageConnection.Connect()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to storage")
	}
	go client.clientContext.SocketServer.HandleConnection(storageConnection)

	for _, address := range listSCAddress {
		err = client.clientContext.MessageSender.SendBytes(
			storageConnection,
			command.SubscribeToAddress,
			address.Bytes(),
		)
		if err != nil {
			return nil, fmt.Errorf("unable to send subscribe")
		}
	}

	evenLogsChan := make(chan types.EventLogs)
	client.clientContext.Handler.(*c_network.Handler).SetEventLogsChan(evenLogsChan)
	return evenLogsChan, nil
}

func (client *Client) ParentSubcribes(
	listSCAddress []common.Address,
) (chan types.EventLogs, error) {
	// Append subscribed addresses for re-subscription after reconnect
	// (dùng append để không overwrite khi gọi ParentSubscribes nhiều lần)
	client.subscribeSCAddresses = append(client.subscribeSCAddresses, listSCAddress...)

	for _, address := range listSCAddress {
		err := client.clientContext.MessageSender.SendBytes(
			client.clientContext.ConnectionsManager.ParentConnection(),
			command.SubscribeToAddress,
			address.Bytes(),
		)
		if err != nil {
			return nil, fmt.Errorf("unable to send subscribe")
		}
	}

	evenLogsChan := make(chan types.EventLogs)
	client.clientContext.Handler.(*c_network.Handler).SetEventLogsChan(evenLogsChan)
	// Use custom callback that includes re-subscription

	return evenLogsChan, nil
}

// handleParentDisconnectWithResubscribe handles parent disconnection and re-subscribes after reconnect
func (client *Client) handleParentDisconnectWithResubscribe(conn t_network.Connection) {

	logger.Warn("[Client] Parent connection lost, will reconnect and re-subscribe...")

	// Use ReconnectToParent which creates a fresh connection from config
	// DO NOT use SocketServer.RetryConnectToParent(conn) because Clone() copies
	// the Type/Address from the disconnected connection, which may be wrong
	// (e.g., Type="child_node" instead of "client")
	go func() {
		retryCount := 0
		for {
			retryCount++
			select {
			case <-client.clientContext.SocketServer.Context().Done():
				return
			default:
			}
			err := client.ReconnectToParent()
			if err == nil {
				logger.Info("[Client] ✅ Successfully reconnected to parent after %d attempts", retryCount)
				// Re-subscribe to all contract addresses
				if len(client.subscribeSCAddresses) > 0 {
					logger.Info("[Client] Re-subscribing to %d addresses...", len(client.subscribeSCAddresses))
					err := client.reSubscribeToAddresses()
					if err != nil {
						logger.Error("[Client] Failed to re-subscribe: %v", err)
					} else {
						logger.Info("[Client] ✅ Successfully re-subscribed to all %d addresses",
							len(client.subscribeSCAddresses))
					}
				}
				return
			}
			if retryCount%10 == 0 {
				logger.Info("[Client] Waiting for parent to reconnect (attempt %d)... Error: %v", retryCount, err)
			}
			time.Sleep(1 * time.Second)
		}
	}()
}

// reSubscribeToAddresses re-subscribes to all previously subscribed contract addresses
func (client *Client) reSubscribeToAddresses() error {
	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	if parentConn == nil || !parentConn.IsConnect() {
		return fmt.Errorf("parent connection not available")
	}

	for _, address := range client.subscribeSCAddresses {
		logger.Info("[Client] Re-subscribing to contract: %s", address.Hex())
		err := client.clientContext.MessageSender.SendBytes(
			parentConn,
			command.SubscribeToAddress,
			address.Bytes(),
		)
		if err != nil {
			return fmt.Errorf("failed to subscribe to %s: %w", address.Hex(), err)
		}
	}

	return nil
}

func (client *Client) RetryConnectToStorage(conn t_network.Connection) {
	for {
		<-time.After(5 * time.Second)
		parentConn := client.clientContext.ConnectionsManager.ParentConnection()
		if parentConn == nil || !parentConn.IsConnect() {
			err := client.ReconnectToParent()
			if err != nil {
				logger.Warn(fmt.Sprintf("error when retry connect to parent %v", err))
				continue
			}
		}
		panic("panic when retry connect")
	}
}

func (client *Client) GetEventLogsChan() chan types.EventLogs {
	return client.clientContext.Handler.(*c_network.Handler).GetEventLogsChan()
}

func (client *Client) Close() {
	// remove parent connection to avoid reconnect
	if client.keepAliveStop != nil {
		close(client.keepAliveStop)
		client.keepAliveStop = nil
	}
	client.clientContext.ConnectionsManager.AddParentConnection(nil)
	client.clientContext.SocketServer.Stop()
}

func (client *Client) SendQueryLogs(bQuery []byte) {
	// client.mu.Lock()
	// defer client.mu.Unlock()
	// get account state

	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	client.clientContext.MessageSender.SendBytes(
		parentConn,
		command.QueryLogs,
		bQuery,
	)
}

func (client *Client) NewEventLogsChan() chan types.EventLogs {
	evenLogsChan := make(chan types.EventLogs)
	client.clientContext.Handler.(*c_network.Handler).SetEventLogsChan(evenLogsChan)
	return evenLogsChan
}

func (client *Client) SendTransactionWithFullInfo(
	fromAddress common.Address,

	toAddress common.Address,
	amount *big.Int,
	maxGas uint64,
	maxGasFee uint64,
	maxTimeUse uint64,
	data []byte,
	relatedAddress []common.Address,
	lastDeviceKey common.Hash,
	newDeviceKey common.Hash,
) (types.Receipt, error) {
	// client.mu.Lock()
	// defer client.mu.Unlock()
	// get account state
	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	if parentConn == nil || !parentConn.IsConnect() {
		err := client.ReconnectToParent()
		if err != nil {
			return nil, err
		}
	}

	client.clientContext.MessageSender.SendBytes(
		parentConn,
		command.GetAccountState,
		fromAddress.Bytes(),
	)

	select {
	case as := <-client.accountStateChan:
		pendingBalance := as.PendingBalance()

		bRelatedAddresses := make([][]byte, len(relatedAddress))
		for i, v := range relatedAddress {
			bRelatedAddresses[i] = v.Bytes()
		}
		tx, err := client.transactionController.SendTransaction(
			fromAddress,
			toAddress,
			pendingBalance,
			amount,
			maxGas,
			maxGasFee,
			maxTimeUse,
			data,
			bRelatedAddresses,
			lastDeviceKey,
			newDeviceKey,
			as.Nonce(),
			client.clientContext.Config.ChainId,
		)
		if err != nil {
			return nil, err
		}

		receipt, err := client.waitReceipt(tx.Hash(), matchByTxHash, 0)
		if err != nil {
			return nil, err
		}
		return receipt, nil
	}
}

func (s *Client) GetMtnAddress() common.Address {
	return s.clientContext.KeyPair.Address()
}

// GetNodeAddr trả về địa chỉ IP:port của node mà client đang kết nối (dùng cho debug log).
func (s *Client) GetNodeAddr() string {
	if s.clientContext != nil && s.clientContext.Config != nil {
		return s.clientContext.Config.ParentConnectionAddress
	}
	return "unknown"
}
