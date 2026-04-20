package debug

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rpc_client"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// Error selectors (4-byte function signatures)
var (
	ErrorSelectorString = common.HexToHash("0x08c379a0") // Error(string)
	PanicSelector       = common.HexToHash("0x4e487b71") // Panic(uint256)
)

// ErrorDecoder chịu trách nhiệm decode revert data
type ErrorDecoder struct {
	artifactStorage *storage.ArtifactStorage
}

// DecodedError represents decoded error information
type DecodedError struct {
	Decoded      bool                   `json:"decoded"`
	ErrorType    string                 `json:"error_type"` // "panic", "custom", "standard", "unknown"
	ErrorName    string                 `json:"error_name,omitempty"`
	ErrorSig     string                 `json:"error_signature,omitempty"`
	Arguments    map[string]interface{} `json:"arguments,omitempty"`
	Message      string                 `json:"message"`
	PanicCode    string                 `json:"panic_code,omitempty"`
	ContractAddr string                 `json:"contract_address,omitempty"`
	ContractName string                 `json:"contract_name,omitempty"`
	RawData      string                 `json:"raw_revert_data,omitempty"`
	PC           uint64                 `json:"pc,omitempty"`
	CallDepth    int                    `json:"call_depth,omitempty"`
	Function     string                 `json:"function,omitempty"`
}

func NewErrorDecoder(artifactStorage *storage.ArtifactStorage) *ErrorDecoder {
	return &ErrorDecoder{
		artifactStorage: artifactStorage,
	}
}
func (d *ErrorDecoder) DecodeError(
	ctx context.Context,
	response *rpc_client.JSONRPCResponse,
	contractAddr string,
	pc uint64,
) {
	if response == nil || response.Error == nil {
		return
	}
	// 1. Tự động trích xuất revert data từ response error
	revertData := d.ExtractRevertData(response.Error)
	if revertData == nil {
		return
	}
	// Nếu không có data tối thiểu 4 bytes (selector), gán raw data rồi thoát
	if len(revertData) < 4 {
		response.Error.Decoded = false
		response.Error.ErrorType = "unknown"
		return
	}
	// Luôn cập nhật Data field với định dạng chuẩn 0x
	response.Error.ContractAddr = contractAddr
	response.Error.PC = pc

	// 2. Định nghĩa selector
	selector := common.BytesToHash(revertData[:4])
	var decoded *DecodedError
	var err error

	// 3. Phân luồng giải mã
	if bytes.Equal(selector.Bytes(), PanicSelector.Bytes()) {
		decoded, err = d.decodePanic(revertData)
	} else if bytes.Equal(selector.Bytes(), ErrorSelectorString.Bytes()) {
		decoded, err = d.decodeStandardError(revertData)
	} else {
		// Thử Custom Error nếu có Artifact
		if artifact, aErr := d.artifactStorage.GetArtifactByAddress(contractAddr); aErr == nil {
			decoded, err = d.decodeCustomError(revertData, artifact)
		}
	}
	// 4. Gán kết quả TRỰC TIẾP vào response.Error
	if err == nil && decoded != nil {
		response.Error.Decoded = true
		response.Error.Message = decoded.Message
		response.Error.ErrorType = decoded.ErrorType
		response.Error.ErrorName = decoded.ErrorName
		response.Error.ErrorSig = decoded.ErrorSig
		response.Error.Arguments = decoded.Arguments
		response.Error.PanicCode = decoded.PanicCode

		logger.Info("✅ Decoded successfully: %s", decoded.Message)
	} else {
		// Nếu không giải mã được các loại trên
		response.Error.Decoded = false
		response.Error.ErrorType = "unknown"
	}
}
func (d *ErrorDecoder) decodePanic(revertData []byte) (*DecodedError, error) {
	if len(revertData) < 36 {
		return nil, fmt.Errorf("invalid panic data")
	}
	panicCodeBytes := revertData[4:36]
	panicCode := "0x" + hex.EncodeToString(panicCodeBytes[28:32])
	message := getPanicMessage(panicCode)
	return &DecodedError{
		Decoded:   true,
		ErrorType: "panic",
		PanicCode: panicCode,
		Message:   message,
	}, nil
}

// getPanicMessage returns human-readable message for panic code
func getPanicMessage(code string) string {
	panicMessages := map[string]string{
		"0x00000001": "Assertion failed",
		"0x00000011": "Arithmetic overflow or underflow",
		"0x00000012": "Division by zero",
		"0x00000021": "Invalid enum value",
		"0x00000022": "Storage byte array incorrectly encoded",
		"0x00000031": "Empty array pop",
		"0x00000032": "Array index out of bounds",
	}

	if msg, ok := panicMessages[code]; ok {
		return msg
	}
	return "Unknown panic: " + code
}

// decodeCustomError decode Custom Error từ ABI
func (d *ErrorDecoder) decodeCustomError(
	revertData []byte,
	artifact *pb.ArtifactData,
) (*DecodedError, error) {
	selector := revertData[:4]
	var contractABI abi.ABI
	if err := json.Unmarshal([]byte(artifact.Abi), &contractABI); err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %w", err)
	}
	// Log ABI parsing (debug only)
	// logger.Info("Contract ABI parsed: %d errors", len(contractABI.Errors))
	for name, errDef := range contractABI.Errors {
		if len(errDef.ID) >= 4 && bytes.Equal(errDef.ID[:4], selector) {
			// Đây là hàm của thư viện go-ethereum. Nó dựa vào định nghĩa trong ABI để "dịch" chuỗi hex thành các biến Go (như uint256 -> *big.Int).
			args, err := errDef.Inputs.Unpack(revertData[4:])
			if err != nil {
				return nil, fmt.Errorf("failed to unpack error args: %w", err)
			}
			argMap := make(map[string]interface{})
			// get value in custom error
			for i, input := range errDef.Inputs {
				if i < len(args) {
					argMap[input.Name] = args[i]
				}
			}
			message := formatErrorMessage(name, argMap)
			return &DecodedError{
				Decoded:   true,
				ErrorType: "custom",
				ErrorName: name,
				ErrorSig:  errDef.Sig,
				Arguments: argMap,
				Message:   message,
			}, nil
		}
	}
	return nil, fmt.Errorf("error not found in ABI")
}

// decodeStandardError decode Error(string)
func (d *ErrorDecoder) decodeStandardError(revertData []byte) (*DecodedError, error) {
	if len(revertData) < 36 {
		return nil, fmt.Errorf("invalid error data")
	}
	// Decode ABI-encoded string (skip 4-byte selector + 32-byte offset)
	// String encoding: offset (32 bytes) + length (32 bytes) + data
	if len(revertData) < 68 {
		return nil, fmt.Errorf("invalid string encoding")
	}
	// Get string length (bytes 36-67)
	length := int(common.BytesToHash(revertData[36:68]).Big().Uint64())
	if len(revertData) < 68+length {
		return nil, fmt.Errorf("string data truncated")
	}
	// Extract string data
	message := string(revertData[68 : 68+length])

	return &DecodedError{
		Decoded:   true,
		ErrorType: "standard",
		Message:   message,
	}, nil
}
func (d *ErrorDecoder) ExtractRevertData(err *rpc_client.JSONRPCError) []byte {
	if err == nil {
		return nil
	}
	// Thử lấy từ Data field
	if err.Data != "" {
		data := strings.TrimPrefix(strings.TrimSpace(err.Data), "0x")
		if decoded, decodeErr := hex.DecodeString(data); decodeErr == nil && len(decoded) >= 4 {
			return decoded
		}
	}

	// Thử extract từ Message (execution reverted: 0x...)
	if err.Message != "" {
		if idx := strings.Index(err.Message, "0x"); idx >= 0 {
			hexStr := strings.Split(err.Message[idx:], " ")[0]
			hexData := strings.TrimPrefix(hexStr, "0x")
			if decoded, decodeErr := hex.DecodeString(hexData); decodeErr == nil && len(decoded) >= 4 {
				return decoded
			}
		}
	}
	return nil
}

func formatErrorMessage(errorName string, args map[string]interface{}) string {
	if len(args) == 0 {
		return errorName + "()"
	}
	parts := make([]string, 0, len(args))
	for name, value := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", name, value))
	}

	return fmt.Sprintf("%s(%s)", errorName, fmt.Sprintf("%v", parts))
}
