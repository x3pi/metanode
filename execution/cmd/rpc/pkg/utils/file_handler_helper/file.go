package file_handler_helper

import (
	"fmt"
	"math/big"
	"reflect"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/file_handler/abi_file"
	file_model "github.com/meta-node-blockchain/meta-node/pkg/models/file_model"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// FileInfo struct để lưu thông tin file từ smart contract

// GetFileInfoTransaction tạo transaction để gọi getFileInfo từ smart contract
func GetFileInfoTransaction(fileKey [32]byte, originalTx types.Transaction) (types.Transaction, error) {
	// Parse ABI để encode function call
	parsedABI, err := abi.JSON(strings.NewReader(abi_file.FileABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %v", err)
	}

	// Encode function call cho getFileInfo
	inputData, err := parsedABI.Pack("getFileInfo", fileKey)
	if err != nil {
		return nil, fmt.Errorf("failed to pack getFileInfo data: %v", err)
	}

	callData := transaction.NewCallData(inputData)
	bData, _ := callData.Marshal()
	// Tạo transaction mới (sao chép thông tin từ original tx)
	newTx := transaction.NewTransaction(
		originalTx.FromAddress(), // from address
		originalTx.ToAddress(),   // to address (contract address)
		big.NewInt(0),            // amount = 0 (view function)
		20000000,                 // max gas
		10000000,                 // max gas price
		60,                       // max time use
		bData,                    // encoded function call
		[][]byte{},               // related addresses
		common.Hash{},            // last device key
		common.Hash{},            // new device key
		originalTx.GetNonce(),    // nonce
		originalTx.GetChainID(),  // chain ID
	)

	// Set ReadOnly = true vì là view function
	newTx.SetReadOnly(true)

	// Copy signature từ original transaction nếu cần
	sign := originalTx.Sign()
	if len(sign) > 0 {
		newTx.SetSignBytes(sign[:])
	}

	return newTx, nil
}

// ParseFileInfoFromResult parse kết quả trả về từ getFileInfo
func ParseFileInfoFromResult(returnData []byte) (*file_model.FileInfo, error) {
	// logger.Error("Phân tích kết quả từ getFileInfo (hex): %s", hex.EncodeToString(returnData))
	if len(returnData) == 0 {
		return nil, fmt.Errorf("return data is empty")
	}

	// Parse ABI để decode kết quả
	parsedABI, err := abi.JSON(strings.NewReader(abi_file.FileABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %v", err)
	}
	method, exists := parsedABI.Methods["getFileInfo"]
	if !exists {
		return nil, fmt.Errorf("getFileInfo method not found in ABI")
	}

	// Unpack kết quả
	results, err := method.Outputs.Unpack(returnData)

	if err != nil {
		return nil, fmt.Errorf("failed to unpack results: %v", err)
	}
	// Smart contract trả về struct được wrap trong tuple, nên chỉ có 1 phần tử
	if len(results) == 0 {
		return nil, fmt.Errorf("no return values")
	}

	// Phần tử đầu tiên chứa toàn bộ struct Info
	// Cần cast sang struct type từ go-ethereum binding
	// Sử dụng reflection để map các field từ struct được unpack
	resultValue := reflect.ValueOf(results[0])
	if resultValue.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected struct type, got: %T", results[0])
	}

	fileInfo := &file_model.FileInfo{}

	// Map các field từ struct được unpack sang FileInfo
	resultType := resultValue.Type()
	for i := 0; i < resultType.NumField(); i++ {
		field := resultType.Field(i)
		fieldValue := resultValue.Field(i)

		// Map các field dựa trên json tag hoặc tên field
		switch field.Tag.Get("json") {
		case "owner":
			if addr, ok := fieldValue.Interface().(common.Address); ok {
				fileInfo.OwnerAddress = addr
			}
		case "merkleRoot":
			// Xử lý cả [32]uint8 và [32]byte (trong Go, chúng là các type khác nhau mặc dù uint8 == byte)
			if merkleRootUint8, ok := fieldValue.Interface().([32]uint8); ok {
				// Copy từ [32]uint8 sang [32]byte
				for j := 0; j < 32; j++ {
					fileInfo.MerkleRoot[j] = byte(merkleRootUint8[j])
				}
			} else if merkleRootBytes, ok := fieldValue.Interface().([32]byte); ok {
				fileInfo.MerkleRoot = merkleRootBytes
			} else {
				logger.Error("Không thể convert merkleRoot, type: %T", fieldValue.Interface())
			}
		case "contentLen":
			if contentLen, ok := fieldValue.Interface().(uint64); ok {
				fileInfo.FileSize = big.NewInt(int64(contentLen))
			}
		case "totalChunks":
			if totalChunks, ok := fieldValue.Interface().(uint64); ok {
				fileInfo.TotalChunks = big.NewInt(int64(totalChunks))
			}
		case "expireTime":
			if expireTime, ok := fieldValue.Interface().(uint64); ok {
				fileInfo.UploadTime = big.NewInt(int64(expireTime))
			}
		case "name":
			if name, ok := fieldValue.Interface().(string); ok {
				fileInfo.FileName = name
			}
		case "ext":
			if ext, ok := fieldValue.Interface().(string); ok {
				fileInfo.FileType = ext
			}
		case "contentDisposition":
			if contentDisposition, ok := fieldValue.Interface().(string); ok {
				fileInfo.Category = contentDisposition
			}
		case "contentID":
			if contentID, ok := fieldValue.Interface().(string); ok {
				fileInfo.ContentID = contentID
			}
		case "status":
			if status, ok := fieldValue.Interface().(uint8); ok {
				fileInfo.Status = status
			}
		}
	}

	return fileInfo, nil
}
