package client

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/client_context"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/command"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/controllers"
	c_network "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/network"
	client_types "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/types"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
)

type Client struct {
	clientContext *client_context.ClientContext

	// mu               sync.Mutex
	accountStateChan chan types.AccountState
	receiptChan      chan types.Receipt
	receiptRequests  chan receiptRequest
	deviceKeyChan    chan types.LastDeviceKey
	nonce            chan uint64

	transactionErrorChan  chan *mt_transaction.TransactionHashWithError
	transactionController client_types.TransactionController
	subscribeSCAddresses  []common.Address

	keepAliveStop        chan struct{}
	pendingChainRequests sync.Map // map[string]chan []byte — chain-direct (header ID matching)
}

type receiptRequestType int

const (
	receiptRequestRegister receiptRequestType = iota
	receiptRequestCancel
)

type receiptRequest struct {
	action     receiptRequestType
	txHash     common.Hash
	responseCh chan types.Receipt
}

const (
	pendingReceiptTTL        = 60 * time.Second
	defaultKeepAliveInterval = 30 * time.Second
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
		nonce:                make(chan uint64, 1),
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
	logger.Error("Connecting to parent node at %s", config.ParentConnectionAddress)
	parentConn.SetRealConnAddr(config.ParentConnectionAddress)
	clientContext.Handler = c_network.NewHandler(
		client.accountStateChan,
		client.receiptChan,
		client.deviceKeyChan,
		client.transactionErrorChan,
		client.nonce,
	)
	clientContext.Handler.SetPendingChainRequests(&client.pendingChainRequests)
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
		clientContext.ConnectionsManager.AddParentConnection(parentConn)
		clientContext.SocketServer.OnConnect(parentConn)
		go clientContext.SocketServer.HandleConnection(parentConn)
		go clientContext.SocketServer.Listen("0.0.0.0:8080")
		client.startKeepAliveLoop()
	}
	client.transactionController = controllers.NewTransactionController(
		clientContext,
	)
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

func (client *Client) runReceiptRouter() {
	waiters := make(map[common.Hash][]chan types.Receipt)
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

			if chans, ok := waiters[txHash]; ok {
				delete(waiters, txHash)
				for _, ch := range chans {
					select {
					case ch <- receipt:
					default:
						logger.Warn("Receipt waiter channel full, dropping receipt for txHash %s", txHash.Hex())
					}
				}
				continue
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
				waiters[req.txHash] = append(waiters[req.txHash], req.responseCh)

			case receiptRequestCancel:
				if chans, ok := waiters[req.txHash]; ok {
					for i, ch := range chans {
						if ch == req.responseCh {
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

func (client *Client) waitReceipt(txHash common.Hash, timeout time.Duration) (types.Receipt, error) {
	responseCh := make(chan types.Receipt, 1)

	client.receiptRequests <- receiptRequest{
		action:     receiptRequestRegister,
		txHash:     txHash,
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
	return client.waitReceipt(txHash, timeout)
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

	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	if parentConn == nil || !parentConn.IsConnect() {
		if err := client.ReconnectToParent(); err != nil {
			return nil, err
		}
	}

	client.clientContext.MessageSender.SendBytes(parentConn, command.GetAccountState, fromAddress.Bytes())
	client.clientContext.MessageSender.SendBytes(parentConn, command.GetNonce, fromAddress.Bytes())

	lastDeviceKey := common.HexToHash("0000000000000000000000000000000000000000000000000000000000000000")
	newDeviceKey := common.HexToHash("0000000000000000000000000000000000000000000000000000000000000000")

	// Thay thế select bằng nhận trực tiếp và xử lý timeout bằng select bên ngoài
	var nonce uint64
	select {
	case nonce = <-client.nonce:
		logger.Info("Nonce : ", nonce)
	case <-time.After(10 * time.Second):
		logger.DebugP("Timeout waiting for nonce")
		return nil, fmt.Errorf("timeout waiting for nonce")
	}

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

	receipt, err := client.waitReceipt(tx.Hash(), pendingReceiptTTL)
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

	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	if !parentConn.IsConnect() {
		logger.Error("Parent connection is not connected, reconnecting...")
		if err := client.ReconnectToParent(); err != nil {
			return nil, err
		}
	}

	// Gửi yêu cầu lấy account state và nonce
	client.clientContext.MessageSender.SendBytes(parentConn, command.GetAccountState, fromAddress.Bytes())
	client.clientContext.MessageSender.SendBytes(parentConn, command.GetNonce, fromAddress.Bytes())

	lastDeviceKey := common.HexToHash("0000000000000000000000000000000000000000000000000000000000000000")
	newDeviceKey := common.HexToHash("0000000000000000000000000000000000000000000000000000000000000000")

	// Nhận nonce trực tiếp thay vì dùng select
	as := <-client.accountStateChan
	pendingBalance := as.PendingBalance()
	logger.Info("[Client] PendingBalance : %s", pendingBalance.String())
	bRelatedAddresses := make([][]byte, len(relatedAddress))
	for i, v := range relatedAddress {
		bRelatedAddresses[i] = v.Bytes()
	}
	logger.Info("[Client] bRelatedAddresses length : %d", len(bRelatedAddresses))
	tx, err := client.transactionController.ReadTransaction(
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
	logger.Info("[Client] Tx Hash : %s", tx.Hash().Hex())
	receipt, err := client.FindReceiptByHash(tx.Hash())
	if err != nil {
		return nil, err
	}
	logger.Info("[Client] Receipt found with Tx Hash : %s", receipt.TransactionHash().Hex())
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

	receipt, err := client.waitReceipt(tx.Hash(), pendingReceiptTTL)
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

		// Chờ biên lai giao dịch (receipt)
		receipt, err := client.waitReceipt(tx.Hash(), pendingReceiptTTL)
		if err != nil {
			return nil, err
		}
		return receipt, nil
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
		client.nonce,
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
	client.clientContext.SocketServer.AddOnDisconnectedCallBack(
		client.clientContext.SocketServer.RetryConnectToParent,
	)

	return evenLogsChan, nil
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

		receipt, err := client.waitReceipt(tx.Hash(), pendingReceiptTTL)
		if err != nil {
			return nil, err
		}
		return receipt, nil
	}
}

func (s *Client) GetMtnAddress() common.Address {
	return s.clientContext.KeyPair.Address()
}
