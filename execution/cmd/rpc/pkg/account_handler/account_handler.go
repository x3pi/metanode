package account_handler

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	ethCommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/app"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/utils"
	"github.com/meta-node-blockchain/meta-node/pkg/account_handler/abi_account"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	utilsPkg "github.com/meta-node-blockchain/meta-node/pkg/utils"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/syndtr/goleveldb/leveldb"
)

// Nó không còn chứa chainState nữa.
type AccountHandlerNoReceipt struct {
	abi     abi.ABI
	storage *storage.BlsAccountStorage
	appCtx  *app.Context
	// Owner TX Queue — xử lý tuần tự các giao dịch từ ví owner (reward, transfer)
	ownerTxQueue chan *OwnerTxRequest
}

// OwnerTxRequest là request gửi transaction từ ví owner
type OwnerTxRequest struct {
	FromAddress ethCommon.Address
	ToAddress   ethCommon.Address
	Amount      *big.Int
	ResultCh    chan *OwnerTxResult // channel trả kết quả về caller
}

// OwnerTxResult là kết quả xử lý transaction owner
type OwnerTxResult struct {
	TxHash string
	Err    error
}

var (
	accountHandlerInstance *AccountHandlerNoReceipt
	accountOnce            sync.Once
)

func GetAccountHandler(appCtx *app.Context) (*AccountHandlerNoReceipt, error) {
	var err error
	accountOnce.Do(func() {
		var parsedABI abi.ABI
		parsedABI, err = abi.JSON(strings.NewReader(abi_account.AccountABI))
		if err != nil {
			return
		}

		accountHandlerInstance = &AccountHandlerNoReceipt{
			abi:          parsedABI,
			storage:      storage.NewBlsAccountStorage(appCtx.LdbBlsWallet),
			appCtx:       appCtx,
			ownerTxQueue: make(chan *OwnerTxRequest, 1000),
		}
		// Start owner TX queue worker
		go accountHandlerInstance.processOwnerTxQueue()
	})

	return accountHandlerInstance, err
}

// HandleAccountTransaction xử lý các giao dịch liên quan đến account
func (h *AccountHandlerNoReceipt) HandleAccountTransaction(
	ctx context.Context,
	tx mt_types.Transaction,
	rawTransactionHex string,
) (handled bool, result interface{}, err error) {
	// Kiểm tra địa chỉ đích

	inputData := tx.CallData().Input()
	if len(inputData) < 4 {
		return false, nil, fmt.Errorf("dữ liệu input không hợp lệ")
	}

	method, err := h.abi.MethodById(inputData[:4])
	if err != nil {
		return false, nil, fmt.Errorf("lỗi khi lấy method từ input data: %v", err)
	}

	switch method.Name {
	case "setBlsPublicKey":
		err = h.handleSetBlsPublicKey(tx, method, inputData[4:], rawTransactionHex)
		return true, nil, err
	case "confirmAccountWithoutSign":
		result, err = h.handleConfirmAccountWithoutSign(tx, method, inputData[4:])
		return true, result, err
	case "confirmAccount":
		result, err = h.handleConfirmAccount(tx, method, inputData[4:])
		return true, result, err
	case "transferFrom":
		result, err = h.handleTransferFrom(tx, method, inputData[4:])
		return true, result, err
	case "addContractFreeGas":
		result, err = h.handleAddContractFreeGas(tx, method, inputData[4:])
		return true, result, err
	case "removeContractFreeGas":
		result, err = h.handleRemoveContractFreeGas(tx, method, inputData[4:])
		return true, result, err
	case "addAuthorizedWallet":
		result, err = h.handleAddAuthorizedWallet(tx, method, inputData[4:])
		return true, result, err
	case "removeAuthorizedWallet":
		result, err = h.handleRemoveAuthorizedWallet(tx, method, inputData[4:])
		return true, result, err
	case "addAdmin":
		result, err = h.handleAddAdmin(tx, method, inputData[4:])
		return true, result, err
	case "removeAdmin":
		result, err = h.handleRemoveAdmin(tx, method, inputData[4:])
		return true, result, err
	case "setAccountType":
		return false, nil, nil
	default:
		logger.Info("___default for tx %s", method.Name)
		return false, nil, nil
	}
}
func (h *AccountHandlerNoReceipt) HandleEthCall(ctx context.Context, data []byte, fromAddress ethCommon.Address) (interface{}, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("invalid call data: too short")
	}
	// Lấy method signature từ 4 bytes đầu
	method, err := h.abi.MethodById(data[:4])
	if err != nil {
		return nil, fmt.Errorf("method not found: %w", err)
	}
	// Chỉ handle getAllAccount cho eth_call
	switch method.Name {
	case "getAllAccount":
		return h.handleGetAllAccount(method, data[4:])
	case "getNotifications": // ✅ THÊM
		return h.handleGetNotifications(method, data[4:])
	case "getAllContractFreeGas":
		return h.handleGetAllContractFreeGas(method, data[4:])
	case "getMyContracts":
		return h.handleGetMyContracts(method, data[4:], fromAddress)
	case "getAllAuthorizedWallets":
		return h.handleGetAllAuthorizedWallets(method, data[4:], fromAddress)
	case "getAllAdmins":
		return h.handleGetAllAdmins(method, data[4:], fromAddress)
	case "getPublickeyBls":
		return h.handleGetPublickeyBls(method, data[4:])

	default:
		return nil, nil
	}
}
func (h *AccountHandlerNoReceipt) handleSetBlsPublicKey(
	tx mt_types.Transaction,
	method *abi.Method,
	inputData []byte,
	rawTransactionHex string,
) error {
	logger.Info("Handling setBlsPublicKey for tx %s", tx.Hash().Hex())
	// Unpack input data
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return fmt.Errorf("lỗi khi unpack input data: %v", err)
	}

	blsPublicKeyBytes, ok := args[0].([]byte)
	if !ok {
		return fmt.Errorf("invalid BLS public key format")
	}
	fromAddress := tx.FromAddress()
	currentTime := time.Now().Unix()
	adminAddress := ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress)
	accountData := &pb.BlsAccountData{
		Address:        fromAddress.Bytes(),
		BlsPublicKey:   blsPublicKeyBytes,
		RegisteredAt:   time.Now().Unix(),
		RegisterTxHash: tx.Hash().Bytes(),
		IsConfirmed:    false,
	}

	// Lưu account data vào unconfirmed storage
	if err := h.storage.AddAccountToBlsPublicKey(accountData, false); err != nil {
		return fmt.Errorf("failed to save account data: %w", err)
	}

	// Lưu pending transaction
	pendingTx := &pb.PendingTransaction{
		Address:           fromAddress.Bytes(),
		BlsPublicKey:      blsPublicKeyBytes,
		RawTransactionHex: rawTransactionHex,
		CreatedAt:         time.Now().Unix(),
		Nonce:             tx.GetNonce(),
		OriginalGasPrice:  0,
	}

	if err := h.storage.SavePendingTransaction(pendingTx); err != nil {
		return fmt.Errorf("failed to save pending transaction: %w", err)
	}
	msgNoti := fmt.Sprintf("BLS registered for account %s", fromAddress.Hex())
	notification := &pb.Notification{
		AccountAddress: adminAddress.Bytes(),
		Message:        msgNoti,
		CreatedAt:      currentTime,
	}
	if err := h.appCtx.LdbNotification.SaveNotification(notification); err != nil {
		logger.Error("Failed to save notification: %v", err)
		return fmt.Errorf("Failed to save notification: %v", err)
	}
	h.broadcastEvent(
		"RegisterBls",
		adminAddress,
		big.NewInt(currentTime),
		blsPublicKeyBytes,
		msgNoti,
	)
	return nil
}

func (h *AccountHandlerNoReceipt) handleConfirmAccountWithoutSign(
	tx mt_types.Transaction,
	method *abi.Method,
	inputData []byte,
) (string, error) {
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return "", fmt.Errorf("lỗi khi unpack input data: %v", err)
	}
	accountAddress, _ := args[0].(ethCommon.Address)
	currentTime := time.Now().Unix()
	pendingTx, err := h.storage.GetPendingTransaction(accountAddress)
	if err != nil {
		return "", fmt.Errorf("pending transaction not found: %w", err)
	}
	rawTransactionHex := pendingTx.RawTransactionHex
	decodedTxBytes, releaseDecoded, err := utils.DecodeHexPooled(rawTransactionHex)
	if err != nil {
		return "", fmt.Errorf("invalid raw transaction hex: %w", err)
	}
	decodedReleased := false
	releaseDecodedOnce := func() {
		if decodedReleased {
			return
		}
		decodedReleased = true
		if releaseDecoded != nil {
			releaseDecoded()
		}
	}
	ethTx := new(types.Transaction)
	if err := ethTx.UnmarshalBinary(decodedTxBytes); err != nil {
		return "", fmt.Errorf("failed to unmarshal ethereum transaction: %w", err)
	}
	signer := types.LatestSignerForChainID(h.appCtx.ClientRpc.ChainId)
	fromAddress, err := types.Sender(signer, ethTx)
	if err != nil {
		return "", fmt.Errorf("failed to derive sender: %w", err)
	}
	if fromAddress != accountAddress {
		return "", fmt.Errorf("sender mismatch: expected %s, got %s", accountAddress.Hex(), fromAddress.Hex())
	}
	var (
		bTx       []byte
		mtTx      mt_types.Transaction
		releaseTx func()
		buildErr  error
	)
	exists, err := h.appCtx.PKS.HasPrivateKey(fromAddress)
	if err != nil {
		return "", fmt.Errorf("error checking private key store: %w", err)
	}
	if !exists {
		bTx, mtTx, releaseTx, buildErr = h.appCtx.ClientRpc.BuildTransactionWithDeviceKeyFromEthTx(ethTx, h.appCtx.TcpCfg, h.appCtx.Cfg, h.appCtx.LdbContractFreeGas, false, nil)
	} else {
		senderPkString, _ := h.appCtx.PKS.GetPrivateKey(fromAddress)
		keyPair := bls.NewKeyPair(ethCommon.FromHex(senderPkString))
		bTx, mtTx, releaseTx, buildErr = h.appCtx.ClientRpc.BuildTransactionWithDeviceKeyFromEthTxAndBlsPrivateKey(
			ethTx,
			h.appCtx.TcpCfg, h.appCtx.Cfg, h.appCtx.LdbContractFreeGas,
			keyPair.PrivateKey(), nil,
		)
	}
	if buildErr != nil {
		return "", fmt.Errorf("failed to build transaction: %w", buildErr)
	}
	rs := h.appCtx.ClientRpc.SendRawTransactionBinary(
		bTx,
		releaseTx,
		decodedTxBytes,
		releaseDecodedOnce,
		nil,
	)
	if rs.Error != nil {
		return "", fmt.Errorf("failed to send transaction: %v", rs.Error)
	}
	newTxHash := rs.Result.(string)

	// Gửi reward qua owner queue (tuần tự)
	if h.appCtx.Cfg.RewardAmount != nil && h.appCtx.Cfg.RewardAmount.Cmp(big.NewInt(0)) > 0 {
		h.SendOwnerTransfer(ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress), ethCommon.Address(pendingTx.Address), h.appCtx.Cfg.RewardAmount)
	}

	if err := h.storage.MarkAccountConfirmed(accountAddress, mtTx.Hash().Bytes(), pendingTx.BlsPublicKey); err != nil {
		logger.Error("Failed to mark account as confirmed: %v", err)
	}
	if err := h.storage.DeletePendingTransaction(accountAddress); err != nil {
		logger.Error("Failed to delete pending transaction: %v", err)
	}
	msgNoti := fmt.Sprintf("Your account %s has been successfully confirmed!", accountAddress.Hex())
	notification := &pb.Notification{
		AccountAddress: accountAddress.Bytes(),
		Message:        msgNoti,
		CreatedAt:      currentTime,
	}
	if err := h.appCtx.LdbNotification.SaveNotification(notification); err != nil {
		logger.Error("Failed to save notification: %v", err)
		return "", fmt.Errorf("Failed to save notification: %v", err)
	}
	h.broadcastEvent("AccountConfirmed", accountAddress, big.NewInt(currentTime), msgNoti)
	logger.Info("✅ Đã confirm account %s, tx hash: %v", accountAddress.Hex(), newTxHash)
	return newTxHash, nil
}

func (h *AccountHandlerNoReceipt) handleConfirmAccount(
	tx mt_types.Transaction,
	method *abi.Method,
	inputData []byte,
) (string, error) {
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return "", fmt.Errorf("lỗi khi unpack input data: %v", err)
	}
	accountAddress, _ := args[0].(ethCommon.Address)
	timestamp, _ := args[1].(*big.Int)
	signatureBytes, _ := args[2].([]byte)

	// Verify timestamp
	if err := h.verifyTimestamp(timestamp); err != nil {
		return "", err
	}
	// Build and verify signature
	message := buildMessageWithTimestamp(accountAddress.Bytes(), timestamp)
	if err := h.verifyOwnerSignature(message, signatureBytes); err != nil {
		return "", err
	}
	currentTime := time.Now().Unix()
	pendingTx, err := h.storage.GetPendingTransaction(accountAddress)
	if err != nil {
		return "", fmt.Errorf("pending transaction not found: %w", err)
	}
	// ========== REBUILD TRANSACTION TỪ rawTransactionHex ==========
	rawTransactionHex := pendingTx.RawTransactionHex
	// Decode hex
	decodedTxBytes, releaseDecoded, err := utils.DecodeHexPooled(rawTransactionHex)
	if err != nil {
		return "", fmt.Errorf("invalid raw transaction hex: %w", err)
	}
	decodedReleased := false
	releaseDecodedOnce := func() {
		if decodedReleased {
			return
		}
		decodedReleased = true
		if releaseDecoded != nil {
			releaseDecoded()
		}
	}
	// Unmarshal Ethereum transaction
	ethTx := new(types.Transaction)
	if err := ethTx.UnmarshalBinary(decodedTxBytes); err != nil {
		return "", fmt.Errorf("failed to unmarshal ethereum transaction: %w", err)
	}
	// Verify sender
	signer := types.LatestSignerForChainID(h.appCtx.ClientRpc.ChainId)
	fromAddress, err := types.Sender(signer, ethTx)
	if err != nil {
		return "", fmt.Errorf("failed to derive sender: %w", err)
	}
	if fromAddress != accountAddress {
		return "", fmt.Errorf("sender mismatch: expected %s, got %s", accountAddress.Hex(), fromAddress.Hex())
	}
	var (
		bTx       []byte
		mtTx      mt_types.Transaction
		releaseTx func()
		buildErr  error
	)
	exists, err := h.appCtx.PKS.HasPrivateKey(fromAddress)
	if err != nil {
		return "", fmt.Errorf("error checking private key store: %w", err)
	}
	if !exists {
		bTx, mtTx, releaseTx, buildErr = h.appCtx.ClientRpc.BuildTransactionWithDeviceKeyFromEthTx(ethTx, h.appCtx.TcpCfg, h.appCtx.Cfg, h.appCtx.LdbContractFreeGas, false, nil)
	} else {
		senderPkString, _ := h.appCtx.PKS.GetPrivateKey(fromAddress)
		keyPair := bls.NewKeyPair(ethCommon.FromHex(senderPkString))
		bTx, mtTx, releaseTx, buildErr = h.appCtx.ClientRpc.BuildTransactionWithDeviceKeyFromEthTxAndBlsPrivateKey(
			ethTx,
			h.appCtx.TcpCfg, h.appCtx.Cfg, h.appCtx.LdbContractFreeGas,
			keyPair.PrivateKey(), nil,
		)
	}
	if buildErr != nil {
		return "", fmt.Errorf("failed to build transaction: %w", buildErr)
	}
	rs := h.appCtx.ClientRpc.SendRawTransactionBinary(
		bTx,
		releaseTx,
		decodedTxBytes,
		releaseDecodedOnce,
		nil,
	)
	if rs.Error != nil {
		return "", fmt.Errorf("failed to send transaction: %v", rs.Error)
	}
	newTxHash := rs.Result.(string)

	// Gửi reward qua owner queue (tuần tự)
	if h.appCtx.Cfg.RewardAmount != nil && h.appCtx.Cfg.RewardAmount.Cmp(big.NewInt(0)) > 0 {
		h.SendOwnerTransfer(ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress), ethCommon.Address(pendingTx.Address), h.appCtx.Cfg.RewardAmount)
	}

	if err := h.storage.MarkAccountConfirmed(accountAddress, mtTx.Hash().Bytes(), pendingTx.BlsPublicKey); err != nil {
		logger.Error("Failed to mark account as confirmed: %v", err)
	}
	if err := h.storage.DeletePendingTransaction(accountAddress); err != nil {
		logger.Error("Failed to delete pending transaction: %v", err)
	}
	msgNoti := fmt.Sprintf("Your account %s has been successfully confirmed!", accountAddress.Hex())
	notification := &pb.Notification{
		AccountAddress: accountAddress.Bytes(),
		Message:        msgNoti,
		CreatedAt:      currentTime,
	}
	if err := h.appCtx.LdbNotification.SaveNotification(notification); err != nil {
		logger.Error("Failed to save notification: %v", err)
		return "", fmt.Errorf("Failed to save notification: %v", err)
	}
	h.broadcastEvent("AccountConfirmed", accountAddress, big.NewInt(currentTime), msgNoti)
	logger.Info("✅ Đã confirm account %s, tx hash: %v", accountAddress.Hex(), newTxHash)
	return newTxHash, nil
}

func (h *AccountHandlerNoReceipt) handleTransferFrom(
	tx mt_types.Transaction,
	method *abi.Method,
	inputData []byte,
) (string, error) {
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return "", fmt.Errorf("lỗi khi unpack input data: %v", err)
	}
	toAddress, _ := args[0].(ethCommon.Address)
	transferAmount, _ := args[1].(*big.Int)
	timestamp, _ := args[2].(*big.Int)
	signatureBytes, _ := args[3].([]byte)

	fromAddress := tx.FromAddress()
	currentTime := time.Now().Unix()

	if err := h.verifyTimestamp(timestamp); err != nil {
		return "", err
	}
	if transferAmount.Cmp(big.NewInt(0)) <= 0 {
		return "", fmt.Errorf("transfer amount must be greater than 0")
	}
	amountBytes := make([]byte, 32)
	transferAmount.FillBytes(amountBytes)
	timestampBytes := make([]byte, 32)
	timestamp.FillBytes(timestampBytes)
	message := make([]byte, 0, 84)
	message = append(message, toAddress.Bytes()...)
	message = append(message, amountBytes...)
	message = append(message, timestampBytes...)
	signerAddress, err := h.recoverSignerAddress(message, signatureBytes)
	if err != nil {
		return "", err
	}
	if signerAddress != fromAddress {
		return "", fmt.Errorf("invalid signature: signer %s does not match sender %s", signerAddress.Hex(), fromAddress.Hex())
	}

	// Gửi transfer qua owner queue (tuần tự, tránh nonce conflict)
	result := h.SendOwnerTransfer(fromAddress, toAddress, transferAmount)

	msgNotiSender := fmt.Sprintf("You transferred %s to %s", transferAmount.String(), toAddress.Hex())
	notificationSender := &pb.Notification{
		AccountAddress: fromAddress.Bytes(),
		Message:        msgNotiSender,
		CreatedAt:      currentTime,
	}
	if err := h.appCtx.LdbNotification.SaveNotification(notificationSender); err != nil {
		logger.Error("Failed to save sender notification: %v", err)
	}
	msgNotiReceiver := fmt.Sprintf("You received %s from %s", transferAmount.String(), fromAddress.Hex())
	notificationReceiver := &pb.Notification{
		AccountAddress: toAddress.Bytes(),
		Message:        msgNotiReceiver,
		CreatedAt:      currentTime,
	}
	if err := h.appCtx.LdbNotification.SaveNotification(notificationReceiver); err != nil {
		logger.Error("Failed to save receiver notification: %v", err)
	}
	msgEvent := fmt.Sprintf("Transfer %s from %s to %s", transferAmount.String(), fromAddress.Hex(), toAddress.Hex())
	h.broadcastEvent("TransferFrom", fromAddress, toAddress, transferAmount, big.NewInt(currentTime), msgEvent)

	logger.Info("✅ Transfer completed from %s to %s, amount: %s, tx hash: %v",
		fromAddress.Hex(), toAddress.Hex(), transferAmount.String(), result.TxHash)

	if result.Err != nil {
		return "", result.Err
	}
	return result.TxHash, nil
}

func (h *AccountHandlerNoReceipt) broadcastEvent(
	eventName string,
	eventArgs ...interface{},
) error {
	addressContract := ethCommon.HexToAddress(h.appCtx.Cfg.ContractsInterceptor[0])
	event, ok := h.abi.Events[eventName]
	if !ok {
		return fmt.Errorf("event %s not found in ABI", eventName)
	}
	eventHash := event.ID
	argIndex := 0
	eventTopics := []string{eventHash.Hex()}
	nonIndexedArgs := make([]interface{}, 0)
	for _, input := range event.Inputs {
		if argIndex >= len(eventArgs) {
			break
		}
		logger.Info("Processing event arg %d: input %v \n indexed=%v, type=%v", argIndex, input, input.Indexed, input.Type.String())
		if input.Indexed {
			topicValue, err := utilsPkg.EncodeIndexedTopic(eventArgs[argIndex], input.Type)
			if err != nil {
				logger.Error("Failed to encode indexed topic: %v", err)
				return err
			}
			eventTopics = append(eventTopics, topicValue)
		} else {
			nonIndexedArgs = append(nonIndexedArgs, eventArgs[argIndex])
		}
		argIndex++
	}
	// Pack event data
	eventData, err := event.Inputs.NonIndexed().Pack(eventArgs...)
	if err != nil {
		logger.Error("Failed to pack %s event data: %v", eventName, err)
		return fmt.Errorf("failed to pack %s event data: %w", eventName, err)
	}
	eventLogData := map[string]interface{}{
		"address": addressContract,
		"topics": []string{
			eventHash.Hex(), // topics[0]: Event signature
		},
		"data":             fmt.Sprintf("0x%x", eventData),
		"blockNumber":      fmt.Sprintf("0x%x", 1),
		"transactionHash":  fmt.Sprintf("0x%064x", time.Now().UnixNano()),
		"blockHash":        "0xa08082c7663f884e3c4d325ad1de149f6e167a84556be205103c16b1595d22cc",
		"logIndex":         "0x0",
		"transactionIndex": "0x0",
		"removed":          false,
	}

	h.appCtx.SubInterceptor.BroadcastEventToContract(
		addressContract.Hex(),
		[]string{eventHash.Hex()},
		eventLogData,
	)
	logger.Info("✅ Broadcasted %s event", eventName)
	return nil
}
func (h *AccountHandlerNoReceipt) handleGetAllAccount(
	method *abi.Method,
	inputData []byte,
) (interface{}, error) {
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("lỗi khi unpack input data: %v", err)
	}

	signBytes, _ := args[0].([]byte)
	blsPublicKeyBytes, _ := args[1].([]byte)
	timestamp, _ := args[2].(*big.Int)
	page, _ := args[3].(*big.Int)
	pageSize, _ := args[4].(*big.Int)
	isConfirmed, _ := args[5].(bool)

	ok, err := h.verifySignature(signBytes, blsPublicKeyBytes, timestamp)
	if err != nil {
		return nil, fmt.Errorf("failed to verify signature: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("invalid signature")
	}
	// Parse page và pageSize từ big.Int
	pageNum := int(page.Int64())
	pageSizeNum := int(pageSize.Int64())

	// Validate pagination parameters
	if pageNum < 0 {
		pageNum = 0
	}
	if pageSizeNum <= 0 || pageSizeNum > 100 {
		pageSizeNum = 20 // Default size, max 100
	}

	// Lấy accounts từ LevelDB (filter by confirmation status)
	accounts, total, err := h.storage.GetAccountsByBlsPublicKey(
		blsPublicKeyBytes,
		pageNum,
		pageSizeNum,
		isConfirmed,
	)
	if err != nil {
		if errors.Is(err, leveldb.ErrNotFound) {
			return map[string]interface{}{
				"accounts":  []map[string]interface{}{},
				"total":     total,
				"page":      pageNum,
				"pageSize":  pageSizeNum,
				"totalPage": (total + pageSizeNum - 1) / pageSizeNum,
				"confirmed": isConfirmed,
			}, nil
		}
		return nil, fmt.Errorf("failed to get accounts: %w", err)
	}
	accountsJSON := make([]map[string]interface{}, 0, len(accounts))
	for _, acc := range accounts {
		accountsJSON = append(accountsJSON, map[string]interface{}{
			"address":        ethCommon.BytesToAddress(acc.Address).Hex(),
			"blsPublicKey":   "0x" + ethCommon.Bytes2Hex(acc.BlsPublicKey),
			"registeredAt":   acc.RegisteredAt,
			"registerTxHash": "0x" + ethCommon.Bytes2Hex(acc.RegisterTxHash),
			"isConfirmed":    acc.IsConfirmed,
			"confirmedAt":    acc.ConfirmedAt,
			"confirmTxHash":  "0x" + ethCommon.Bytes2Hex(acc.ConfirmTxHash),
		})
	}
	// Trả về kết quả
	result := map[string]interface{}{
		"accounts":  accountsJSON,
		"total":     total,
		"page":      pageNum,
		"pageSize":  pageSizeNum,
		"totalPage": (total + pageSizeNum - 1) / pageSizeNum,
		"confirmed": isConfirmed,
	}

	logger.Info("✅ Trả về %d accounts (tổng: %d)", len(accounts), total)
	return result, nil

}
func (h *AccountHandlerNoReceipt) handleGetNotifications(
	method *abi.Method,
	inputData []byte,
) (interface{}, error) {
	logger.Info("🔍 Handling getNotifications...")
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack input: %w", err)
	}

	accountAddress := args[0].(ethCommon.Address)
	page := int(args[1].(*big.Int).Int64())
	pageSize := int(args[2].(*big.Int).Int64())

	notifications, total, err := h.appCtx.LdbNotification.GetNotifications(
		accountAddress,
		page,
		pageSize,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get notifications: %w", err)
	}
	// ✅ Convert notifications to JSON-serializable format
	notifList := make([]map[string]interface{}, len(notifications))
	for i, notif := range notifications {
		notifList[i] = map[string]interface{}{
			"id":        notif.Id,
			"message":   notif.Message,
			"createdAt": notif.CreatedAt,
		}
	}
	totalPage := (total + pageSize - 1) / pageSize
	if totalPage == 0 {
		totalPage = 1
	}
	result := map[string]interface{}{
		"notifications": notifList,
		"total":         total,
		"page":          page,
		"pageSize":      pageSize,
		"totalPages":    totalPage, // ✅ Đổi từ totalPage thành totalPages để match với TypeScript
	}
	return result, nil
}

// ========== SIGNATURE VERIFICATION HELPERS ==========

// verifyTimestamp kiểm tra timestamp có hợp lệ không (trong vòng 5 phút)
func (h *AccountHandlerNoReceipt) verifyTimestamp(timestamp *big.Int) error {
	currentTime := time.Now().Unix()
	if utilsPkg.Abs(currentTime-timestamp.Int64()) > 300 {
		return fmt.Errorf("timestamp expired (current: %d, provided: %d)", currentTime, timestamp.Int64())
	}
	return nil
}

// recoverSignerAddress phục hồi địa chỉ người ký từ message và signature
func (h *AccountHandlerNoReceipt) recoverSignerAddress(message []byte, signatureBytes []byte) (ethCommon.Address, error) {
	if len(signatureBytes) < 65 {
		return ethCommon.Address{}, fmt.Errorf("invalid signature length: expected at least 65, got %d", len(signatureBytes))
	}

	// Create Ethereum signed message
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)
	messageHash := crypto.Keccak256Hash([]byte(prefixedMessage))

	// Adjust V value (Ethereum uses 27/28, crypto.Ecrecover expects 0/1)
	signature := make([]byte, 65)
	copy(signature, signatureBytes)
	if signature[64] >= 27 {
		signature[64] -= 27
	}

	// Recover public key
	pubKey, err := crypto.SigToPub(messageHash.Bytes(), signature)
	if err != nil {
		return ethCommon.Address{}, fmt.Errorf("failed to recover public key: %w", err)
	}

	return crypto.PubkeyToAddress(*pubKey), nil
}

// verifyOwnerSignature kiểm tra chữ ký có phải của owner không
func (h *AccountHandlerNoReceipt) verifyOwnerSignature(message []byte, signatureBytes []byte) error {
	signerAddress, err := h.recoverSignerAddress(message, signatureBytes)
	if err != nil {
		return err
	}

	ownerAddress := ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress)
	if signerAddress != ownerAddress {
		return fmt.Errorf("invalid signature: signer %s is not owner %s", signerAddress.Hex(), ownerAddress.Hex())
	}

	return nil
}

// buildMessageWithTimestamp tạo message từ data và timestamp
func buildMessageWithTimestamp(data []byte, timestamp *big.Int) []byte {
	timestampBytes := make([]byte, 32)
	timestamp.FillBytes(timestampBytes)

	message := make([]byte, 0, len(data)+32)
	message = append(message, data...)
	message = append(message, timestampBytes...)

	return message
}

// verifySignature - hàm cũ giữ lại để tương thích với getAllAccount
func (h *AccountHandlerNoReceipt) verifySignature(
	signBytes []byte,
	blsPublicKeyBytes []byte,
	timestamp *big.Int,
) (bool, error) {
	// Verify timestamp
	if err := h.verifyTimestamp(timestamp); err != nil {
		return false, err
	}

	// Build message
	message := buildMessageWithTimestamp(blsPublicKeyBytes, timestamp)

	// Verify signature
	if err := h.verifyOwnerSignature(message, signBytes); err != nil {
		return false, err
	}

	return true, nil
}

// ========== CONTRACT FREE GAS HANDLERS ==========

func (h *AccountHandlerNoReceipt) handleAddContractFreeGas(
	tx mt_types.Transaction,
	method *abi.Method,
	inputData []byte,
) (string, error) {
	logger.Info("Handling addContractFreeGas for tx %s", tx.Hash().Hex())

	// Unpack input data
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return "", fmt.Errorf("failed to unpack input: %w", err)
	}
	contractAddress, _ := args[0].(ethCommon.Address)

	// Verify sender
	fromAddress := tx.FromAddress()
	ownerAddress := ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress)

	isOwner := fromAddress == ownerAddress
	isAuthorized := false
	if !isOwner {
		var errAuth error
		isAuthorized, errAuth = h.appCtx.LdbContractFreeGas.IsAuthorized(fromAddress)
		if errAuth != nil {
			return "", fmt.Errorf("failed to check authorization: %w", errAuth)
		}
	}

	if !isOwner && !isAuthorized {
		// Check admin list
		isAdminInList, errAdmin := h.appCtx.LdbContractFreeGas.IsAdmin(fromAddress)
		if errAdmin == nil && isAdminInList {
			isAuthorized = true
		}
	}

	if !isOwner && !isAuthorized {
		return "", fmt.Errorf("only owner or authorized wallet can add contract free gas")
	}

	// Add contract to storage
	if err := h.appCtx.LdbContractFreeGas.AddContract(contractAddress, fromAddress); err != nil {
		return "", fmt.Errorf("failed to add contract: %w", err)
	}

	logger.Info("✅ Added contract %s to free gas list by %s", contractAddress.Hex(), fromAddress.Hex())
	return tx.Hash().Hex(), nil
}

func (h *AccountHandlerNoReceipt) handleRemoveContractFreeGas(
	tx mt_types.Transaction,
	method *abi.Method,
	inputData []byte,
) (string, error) {
	logger.Info("Handling removeContractFreeGas for tx %s", tx.Hash().Hex())

	// Unpack input data
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return "", fmt.Errorf("failed to unpack input: %w", err)
	}

	contractAddress, _ := args[0].(ethCommon.Address)

	// Verify sender
	fromAddress := tx.FromAddress()
	ownerAddress := ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress)

	contractData, err := h.appCtx.LdbContractFreeGas.GetContract(contractAddress)
	if err != nil {
		return "", fmt.Errorf("failed to get contract data: %w", err)
	}

	if fromAddress != ownerAddress && fromAddress != ethCommon.BytesToAddress(contractData.AddedBy) {
		return "", fmt.Errorf("only owner or the original creator can remove this contract")
	}

	// Remove contract from storage
	if err := h.appCtx.LdbContractFreeGas.RemoveContract(contractAddress); err != nil {
		return "", fmt.Errorf("failed to remove contract: %w", err)
	}

	logger.Info("✅ Removed contract %s from free gas list by %s", contractAddress.Hex(), fromAddress.Hex())
	return tx.Hash().Hex(), nil
}

func (h *AccountHandlerNoReceipt) handleGetAllContractFreeGas(
	method *abi.Method,
	inputData []byte,
) (interface{}, error) {
	logger.Info("Handling getAllContractFreeGas")

	// Unpack input data
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack input: %w", err)
	}

	page, _ := args[0].(*big.Int)
	pageSize, _ := args[1].(*big.Int)
	timestamp, _ := args[2].(*big.Int)
	signatureBytes, _ := args[3].([]byte)

	// Verify timestamp
	if err := h.verifyTimestamp(timestamp); err != nil {
		return nil, err
	}

	// Build message: page (32 bytes) + pageSize (32 bytes) + timestamp (32 bytes)
	pageBytes := make([]byte, 32)
	page.FillBytes(pageBytes)

	pageSizeBytes := make([]byte, 32)
	pageSize.FillBytes(pageSizeBytes)

	timestampBytes := make([]byte, 32)
	timestamp.FillBytes(timestampBytes)

	message := make([]byte, 0, 96)
	message = append(message, pageBytes...)
	message = append(message, pageSizeBytes...)
	message = append(message, timestampBytes...)

	// Verify signature
	if err := h.verifyOwnerSignature(message, signatureBytes); err != nil {
		return nil, err
	}

	// Parse page và pageSize từ big.Int
	pageInt := int(page.Int64())
	pageSizeInt := int(pageSize.Int64())

	// Validate pagination parameters
	if pageInt < 0 {
		return nil, fmt.Errorf("page must be >= 0")
	}
	if pageSizeInt <= 0 || pageSizeInt > 100 {
		return nil, fmt.Errorf("pageSize must be between 1 and 100")
	}

	// Lấy contracts từ LevelDB
	contracts, total, err := h.appCtx.LdbContractFreeGas.GetContracts(pageInt, pageSizeInt)
	if err != nil {
		return nil, fmt.Errorf("failed to get contracts: %w", err)
	}

	// Calculate total pages
	totalPages := (total + pageSizeInt - 1) / pageSizeInt

	// Convert contracts to JSON-serializable format
	contractList := make([]map[string]interface{}, 0, len(contracts))
	for _, contract := range contracts {
		contractList = append(contractList, map[string]interface{}{
			"contract_address": ethCommon.BytesToAddress(contract.ContractAddress).Hex(),
			"added_at":         contract.AddedAt,
			"added_by":         ethCommon.BytesToAddress(contract.AddedBy).Hex(),
		})
	}

	// Trả về kết quả
	return map[string]interface{}{
		"contracts":   contractList,
		"total":       total,
		"page":        pageInt,
		"page_size":   pageSizeInt,
		"total_pages": totalPages,
	}, nil
}

func (h *AccountHandlerNoReceipt) handleGetMyContracts(
	method *abi.Method,
	inputData []byte,
	fromAddress ethCommon.Address,
) (interface{}, error) {
	logger.Info("Handling getMyContracts for %s", fromAddress.Hex())

	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack input: %w", err)
	}

	// args: [adder address, page uint256, pageSize uint256]
	adder, _ := args[0].(ethCommon.Address)
	page, _ := args[1].(*big.Int)
	pageSize, _ := args[2].(*big.Int)

	// Nếu adder == zero address, dùng fromAddress của caller
	var targetAdder ethCommon.Address
	if adder == (ethCommon.Address{}) {
		targetAdder = fromAddress
	} else {
		// Có adder chỉ định: chỉ owner hoặc admin mới được query của người khác
		ownerAddress := ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress)
		if fromAddress != ownerAddress {
			isAdmin, _ := h.appCtx.LdbContractFreeGas.IsAdmin(fromAddress)
			if !isAdmin {
				return nil, fmt.Errorf("only owner or admin can query another address's contracts")
			}
		}
		targetAdder = adder
	}

	pageInt := int(page.Int64())
	pageSizeInt := int(pageSize.Int64())

	if pageInt < 0 {
		return nil, fmt.Errorf("page must be >= 0")
	}
	if pageSizeInt <= 0 || pageSizeInt > 100 {
		return nil, fmt.Errorf("pageSize must be between 1 and 100")
	}

	contracts, total, err := h.appCtx.LdbContractFreeGas.GetContractsByAdder(targetAdder, pageInt, pageSizeInt)
	if err != nil {
		return nil, fmt.Errorf("failed to get my contracts: %w", err)
	}

	totalPages := 0
	if pageSizeInt > 0 {
		totalPages = (total + pageSizeInt - 1) / pageSizeInt
	}

	contractList := make([]map[string]interface{}, 0, len(contracts))
	for _, contract := range contracts {
		contractList = append(contractList, map[string]interface{}{
			"contract_address": ethCommon.BytesToAddress(contract.ContractAddress).Hex(),
			"added_at":         contract.AddedAt,
			"added_by":         ethCommon.BytesToAddress(contract.AddedBy).Hex(),
		})
	}

	return map[string]interface{}{
		"adder":       targetAdder.Hex(),
		"contracts":   contractList,
		"total":       total,
		"page":        pageInt,
		"page_size":   pageSizeInt,
		"total_pages": totalPages,
	}, nil
}

func (h *AccountHandlerNoReceipt) handleAddAuthorizedWallet(
	tx mt_types.Transaction,
	method *abi.Method,
	inputData []byte,
) (string, error) {
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return "", fmt.Errorf("failed to unpack input: %w", err)
	}
	walletAddress, _ := args[0].(ethCommon.Address)

	fromAddress := tx.FromAddress()
	ownerAddress := ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress)
	if fromAddress != ownerAddress {
		// Admin list cũng được thêm authorized wallet
		isAdminInList, _ := h.appCtx.LdbContractFreeGas.IsAdmin(fromAddress)
		if !isAdminInList {
			return "", fmt.Errorf("only root owner or admin can add authorized wallet, sender: %s", fromAddress.Hex())
		}
	}

	if err := h.appCtx.LdbContractFreeGas.AddWallet(walletAddress, fromAddress); err != nil {
		return "", fmt.Errorf("failed to add authorized wallet: %w", err)
	}

	logger.Info("✅ Added authorized wallet %s", walletAddress.Hex())
	return tx.Hash().Hex(), nil
}

func (h *AccountHandlerNoReceipt) handleRemoveAuthorizedWallet(
	tx mt_types.Transaction,
	method *abi.Method,
	inputData []byte,
) (string, error) {
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return "", fmt.Errorf("failed to unpack input: %w", err)
	}
	walletAddress, _ := args[0].(ethCommon.Address)

	fromAddress := tx.FromAddress()
	ownerAddress := ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress)
	if fromAddress != ownerAddress {
		isAdminInList, _ := h.appCtx.LdbContractFreeGas.IsAdmin(fromAddress)
		if !isAdminInList {
			return "", fmt.Errorf("only root owner or admin can remove authorized wallet, sender: %s", fromAddress.Hex())
		}
	}

	if err := h.appCtx.LdbContractFreeGas.RemoveWallet(walletAddress); err != nil {
		return "", fmt.Errorf("failed to remove authorized wallet: %w", err)
	}

	logger.Info("✅ Removed authorized wallet %s", walletAddress.Hex())
	return tx.Hash().Hex(), nil
}

func (h *AccountHandlerNoReceipt) handleGetAllAuthorizedWallets(
	method *abi.Method,
	inputData []byte,
	fromAddress ethCommon.Address,
) (interface{}, error) {
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack input: %w", err)
	}

	page, _ := args[0].(*big.Int)
	pageSize, _ := args[1].(*big.Int)

	ownerAddress := ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress)
	if fromAddress != ownerAddress {
		return nil, fmt.Errorf("only owner can get all authorized wallets, caller: %s", fromAddress.Hex())
	}

	pageInt := int(page.Int64())
	pageSizeInt := int(pageSize.Int64())

	if pageInt < 0 {
		return nil, fmt.Errorf("page must be >= 0")
	}
	if pageSizeInt <= 0 || pageSizeInt > 100 {
		return nil, fmt.Errorf("pageSize must be between 1 and 100")
	}

	wallets, total, err := h.appCtx.LdbContractFreeGas.GetWallets(pageInt, pageSizeInt)
	if err != nil {
		return nil, fmt.Errorf("failed to get wallets: %w", err)
	}

	totalPages := (total + pageSizeInt - 1) / pageSizeInt

	walletList := make([]map[string]interface{}, 0, len(wallets))
	for _, wallet := range wallets {
		walletList = append(walletList, map[string]interface{}{
			"wallet_address": ethCommon.BytesToAddress(wallet.WalletAddress).Hex(),
			"added_at":       wallet.AddedAt,
			"added_by":       ethCommon.BytesToAddress(wallet.AddedBy).Hex(),
		})
	}

	return map[string]interface{}{
		"wallets":     walletList,
		"total":       total,
		"page":        pageInt,
		"page_size":   pageSizeInt,
		"total_pages": totalPages,
	}, nil
}

func (h *AccountHandlerNoReceipt) handleAddAdmin(
	tx mt_types.Transaction,
	method *abi.Method,
	inputData []byte,
) (string, error) {
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return "", fmt.Errorf("failed to unpack input: %w", err)
	}
	adminAddress, _ := args[0].(ethCommon.Address)

	fromAddress := tx.FromAddress()
	ownerAddress := ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress)
	if fromAddress != ownerAddress {
		return "", fmt.Errorf("only root owner can add admin, sender: %s", fromAddress.Hex())
	}

	if err := h.appCtx.LdbContractFreeGas.AddAdmin(adminAddress, fromAddress); err != nil {
		return "", fmt.Errorf("failed to add admin: %w", err)
	}

	logger.Info("✅ Added admin %s", adminAddress.Hex())
	return tx.Hash().Hex(), nil
}

func (h *AccountHandlerNoReceipt) handleRemoveAdmin(
	tx mt_types.Transaction,
	method *abi.Method,
	inputData []byte,
) (string, error) {
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return "", fmt.Errorf("failed to unpack input: %w", err)
	}
	adminAddress, _ := args[0].(ethCommon.Address)

	fromAddress := tx.FromAddress()
	ownerAddress := ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress)
	if fromAddress != ownerAddress {
		return "", fmt.Errorf("only root owner can remove admin, sender: %s", fromAddress.Hex())
	}

	if err := h.appCtx.LdbContractFreeGas.RemoveAdmin(adminAddress); err != nil {
		return "", fmt.Errorf("failed to remove admin: %w", err)
	}

	logger.Info("✅ Removed admin %s", adminAddress.Hex())
	return tx.Hash().Hex(), nil
}

func (h *AccountHandlerNoReceipt) handleGetAllAdmins(
	method *abi.Method,
	inputData []byte,
	fromAddress ethCommon.Address,
) (interface{}, error) {
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack input: %w", err)
	}

	page, _ := args[0].(*big.Int)
	pageSize, _ := args[1].(*big.Int)

	ownerAddress := ethCommon.HexToAddress(h.appCtx.Cfg.OwnerRpcAddress)
	if fromAddress != ownerAddress {
		return nil, fmt.Errorf("only root owner can get all admins, caller: %s", fromAddress.Hex())
	}

	pageInt := int(page.Int64())
	pageSizeInt := int(pageSize.Int64())

	if pageInt < 0 {
		return nil, fmt.Errorf("page must be >= 0")
	}
	if pageSizeInt <= 0 || pageSizeInt > 100 {
		return nil, fmt.Errorf("pageSize must be between 1 and 100")
	}

	admins, total, err := h.appCtx.LdbContractFreeGas.GetAdmins(pageInt, pageSizeInt)
	if err != nil {
		return nil, fmt.Errorf("failed to get admins: %w", err)
	}

	totalPages := (total + pageSizeInt - 1) / pageSizeInt

	adminList := make([]map[string]interface{}, 0, len(admins))
	for _, admin := range admins {
		adminList = append(adminList, map[string]interface{}{
			"admin_address": ethCommon.BytesToAddress(admin.AdminAddress).Hex(),
			"added_at":      admin.AddedAt,
			"added_by":      ethCommon.BytesToAddress(admin.AddedBy).Hex(),
		})
	}

	return map[string]interface{}{
		"admins":      adminList,
		"total":       total,
		"page":        pageInt,
		"page_size":   pageSizeInt,
		"total_pages": totalPages,
	}, nil
}

func (h *AccountHandlerNoReceipt) handleGetPublickeyBls(
	method *abi.Method,
	inputData []byte,
) (interface{}, error) {
	// Lấy public key từ KeyPair
	publicKeyString := h.appCtx.ClientRpc.KeyPair.PublicKey().String()
	// Đảm bảo có prefix "0x" nếu chưa có
	if !strings.HasPrefix(publicKeyString, "0x") {
		publicKeyString = "0x" + publicKeyString
	}

	logger.Info("✅ getPublickeyBls returning public key: %s", publicKeyString)
	// Trả về string trực tiếp (sẽ được JSON marshal thành "0x...")
	return publicKeyString, nil
}
