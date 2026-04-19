package tcp_trans

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	ethCom "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	client "github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp"
	"github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp/command"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/file_handler/abi_file"
	"github.com/meta-node-blockchain/meta-node/pkg/models/file_model"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils/file_handler_helper"
	"github.com/meta-node-blockchain/meta-node/types"
)

func GetFileInfoTransaction(c *client.Client, config *tcp_config.ClientConfig, fileKey [32]byte, originalTx types.Transaction) (*file_model.FileInfo, error) {
	maxGas := uint64(20000000) // Consider making these configurable
	maxGasPrice := uint64(10000000)
	parsedABI, err := abi.JSON(strings.NewReader(abi_file.FileABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %v", err)
	}
	inputData, err := parsedABI.Pack("getFileInfo", fileKey)
	callData := transaction.NewCallData(inputData)
	bData, _ := callData.Marshal()
	receipt, err := c.ReadTransaction(originalTx.FromAddress(), originalTx.ToAddress(), big.NewInt(0), bData, []ethCom.Address{}, maxGas, maxGasPrice, 60)
	if err != nil {
		return nil, fmt.Errorf("failed to get fileInfo: %v", err)
	}
	var returnData []byte = receipt.Return()
	if len(returnData) == 0 {
		return nil, fmt.Errorf("return data is empty")
	}
	fileInfo, err := file_handler_helper.ParseFileInfoFromResult(returnData)
	if err != nil {
		return nil, fmt.Errorf("lỗi parse fileInfo: %v", err)
	}
	// method, exists := parsedABI.Methods["getFileInfo"]
	// if !exists {
	// 	return nil, fmt.Errorf("getFileInfo method not found in ABI")
	// }
	// results, err := method.Outputs.Unpack(returnData)
	// if len(results) == 0 {
	// 	return nil, fmt.Errorf("no return values")
	// }
	// resultValue := reflect.ValueOf(results[0])
	// if resultValue.Kind() != reflect.Struct {
	// 	return nil, fmt.Errorf("expected struct type, got: %T", results[0])
	// }
	// fileInfo := &file_model.FileInfo{}

	// // Map các field từ struct được unpack sang FileInfo
	// resultType := resultValue.Type()
	// for i := 0; i < resultType.NumField(); i++ {
	// 	field := resultType.Field(i)
	// 	fieldValue := resultValue.Field(i)

	// 	// Map các field dựa trên json tag hoặc tên field
	// 	switch field.Tag.Get("json") {
	// 	case "owner":
	// 		if addr, ok := fieldValue.Interface().(common.Address); ok {
	// 			fileInfo.OwnerAddress = addr
	// 		}
	// 	case "merkleRoot":
	// 		// Xử lý cả [32]uint8 và [32]byte (trong Go, chúng là các type khác nhau mặc dù uint8 == byte)
	// 		if merkleRootUint8, ok := fieldValue.Interface().([32]uint8); ok {
	// 			// Copy từ [32]uint8 sang [32]byte
	// 			for j := 0; j < 32; j++ {
	// 				fileInfo.MerkleRoot[j] = byte(merkleRootUint8[j])
	// 			}
	// 		} else if merkleRootBytes, ok := fieldValue.Interface().([32]byte); ok {
	// 			fileInfo.MerkleRoot = merkleRootBytes
	// 		} else {
	// 			logger.Error("Không thể convert merkleRoot, type: %T", fieldValue.Interface())
	// 		}
	// 	case "contentLen":
	// 		if contentLen, ok := fieldValue.Interface().(uint64); ok {
	// 			fileInfo.FileSize = big.NewInt(int64(contentLen))
	// 		}
	// 	case "totalChunks":
	// 		if totalChunks, ok := fieldValue.Interface().(uint64); ok {
	// 			fileInfo.TotalChunks = big.NewInt(int64(totalChunks))
	// 		}
	// 	case "expireTime":
	// 		if expireTime, ok := fieldValue.Interface().(uint64); ok {
	// 			fileInfo.UploadTime = big.NewInt(int64(expireTime))
	// 		}
	// 	case "name":
	// 		if name, ok := fieldValue.Interface().(string); ok {
	// 			fileInfo.FileName = name
	// 		}
	// 	case "ext":
	// 		if ext, ok := fieldValue.Interface().(string); ok {
	// 			fileInfo.FileType = ext
	// 		}
	// 	case "contentDisposition":
	// 		if contentDisposition, ok := fieldValue.Interface().(string); ok {
	// 			fileInfo.Category = contentDisposition
	// 		}
	// 	case "contentID":
	// 		if contentID, ok := fieldValue.Interface().(string); ok {
	// 			fileInfo.ContentID = contentID
	// 		}
	// 	case "status":
	// 		if status, ok := fieldValue.Interface().(uint8); ok {
	// 			fileInfo.Status = status
	// 		}
	// 	}
	// }

	return fileInfo, nil
}
func GetRustServerAddressesListTransaction(c *client.Client, config *tcp_config.ClientConfig, originalTx types.Transaction) ([]string, error) {
	maxGas := uint64(20000000) // Consider making these configurable
	maxGasPrice := uint64(10000000)
	parsedABI, err := abi.JSON(strings.NewReader(abi_file.FileABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %v", err)
	}
	inputData, err := parsedABI.Pack("getRustServerAddresses")
	if err != nil {
		return nil, fmt.Errorf("failed to pack getRustServerAddresses data: %v", err)
	}
	callData := transaction.NewCallData(inputData)
	bData, err := callData.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal call data: %v", err)
	}
	receipt, err := c.ReadTransaction(originalTx.FromAddress(), originalTx.ToAddress(), big.NewInt(0), bData, []ethCom.Address{}, maxGas, maxGasPrice, 60)
	if err != nil {
		return nil, fmt.Errorf("failed to get Rust server addresses: %v", err)
	}
	var returnData []byte = receipt.Return()
	if len(returnData) == 0 {
		return nil, fmt.Errorf("return data is empty")
	}
	method, exists := parsedABI.Methods["getRustServerAddresses"]
	if !exists {
		return nil, fmt.Errorf("failed to unpack results: %v", err)
	}
	results, err := method.Outputs.Unpack(returnData)
	if len(results) == 0 {
		return nil, fmt.Errorf("no return values")
	}
	serverAddresses, ok := results[0].([]string)
	if !ok {
		return nil, fmt.Errorf("cannot cast result to []string, got: %T", results[0])
	}
	return serverAddresses, nil
}

func SendTransactionWithDeviceKey(
	client *client.Client,
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
	clientContext := client.GetClientContext()
	parentConn := clientContext.ConnectionsManager.ParentConnection()

	if parentConn == nil || !parentConn.IsConnect() {
		err := client.ReconnectToParent()
		if err != nil {
			return nil, err
		}
		parentConn = clientContext.ConnectionsManager.ParentConnection()
	}
	// Gửi yêu cầu lấy trạng thái tài khoản
	clientContext.MessageSender.SendBytes(
		parentConn,
		command.GetAccountState,
		fromAddress.Bytes(),
	)
	// Lắng nghe tài khoản trong kênh accountStateChan bằng for range
	for as := range client.GetAccountStateChan() {
		// Nếu không phải tài khoản mong muốn, tiếp tục lắng nghe mà không bỏ dữ liệu
		if as.Address() != fromAddress {
			// Gửi lại dữ liệu cho luồng khác đọc (không bỏ dữ liệu)
			client.GetAccountStateChan() <- as
			time.Sleep(50 * time.Millisecond) // Delay trước khi tiếp tục lặp
			continue
		}

		// Nếu tìm thấy tài khoản phù hợp, xử lý giao dịch
		lastHash := as.LastHash()
		pendingBalance := as.PendingBalance()

		err := clientContext.MessageSender.SendBytes(
			parentConn,
			"GetDeviceKey",
			lastHash.Bytes(),
		)

		if err != nil {
			return nil, err
		}

		// Lắng nghe deviceKey từ server
		receiveDeviceKey := <-client.GetDeviceKeyChan()
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
		tx, err := client.GetTransactionController().SendTransactionWithDeviceKey(
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
			clientContext.Config.ChainId,
		)
		if err != nil {
			return nil, err
		}
		// Chờ biên lai giao dịch (receipt)
		receipt, err := client.FindReceiptByHash(tx.Hash())
		if err != nil {
			return nil, err
		}
		return receipt, nil
	}

	// Nếu kênh accountStateChan bị đóng, trả lỗi
	return nil, fmt.Errorf("account state channel closed unexpectedly")
}
