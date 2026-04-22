package utils

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// Uint64ToBytes converts a uint64 to a byte array.
func Uint64ToBytes(value uint64) []byte {
	bytes := make([]byte, 8)
	binary.BigEndian.PutUint64(bytes, value)
	return bytes
}

// BytesToUint64 converts a byte array to a uint64.
func BytesToUint64(bytes []byte) (uint64, error) {
	if len(bytes) != 8 {
		return 0, fmt.Errorf("byte array must be 8 bytes long")
	}
	return binary.BigEndian.Uint64(bytes), nil
}

// Uint64ToBigInt converts a uint64 to a big.Int.
func Uint64ToBigInt(value uint64) *big.Int {
	return new(big.Int).SetUint64(value)
}

// BigIntToUint64 converts a big.Int to a uint64.
func BigIntToUint64(value *big.Int) (uint64, error) {
	if value == nil {
		return 0, fmt.Errorf("big.Int không được phép là nil")
	}
	if value.IsUint64() {
		return value.Uint64(), nil
	}
	return 0, fmt.Errorf("big.Int quá lớn để chuyển đổi thành uint64")
}

func HexutilBigToUint64(n *hexutil.Big) (uint64, error) {
	if n == nil {
		return 0, fmt.Errorf("input hexutil.Big is nil")
	}
	bigInt := n.ToInt()
	uint64Value := bigInt.Uint64()
	return uint64Value, nil
}

func ParseBlockNumber(blockNumberStr string) (uint64, error) {
	// Kiểm tra xem blockNumberStr đã có tiền tố "0x" chưa
	blockNumberStr = strings.TrimPrefix(blockNumberStr, "0x")

	blockNumber, err := strconv.ParseUint(blockNumberStr, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("lỗi chuyển đổi block number: %w", err)
	}

	return blockNumber, nil
}

// GetFunctionSelector tính function selector (4 byte đầu) từ tên hàm Solidity
func GetFunctionSelector(methodSignature string) []byte {
	hash := crypto.Keccak256([]byte(methodSignature))
	return hash[:4] // Lấy 4 byte đầu
}

// GetFunctionSelector tính function selector (4 byte đầu) từ tên hàm Solidity
func GetAddressSelector(methodSignature string) common.Address {
	hash := crypto.Keccak256([]byte(methodSignature))
	return common.BytesToAddress(hash[:4])
}

// Hàm hỗ trợ so sánh uint64
func CompareUint64(a, b uint64) int {
	if a < b {
		return -1
	} else if a > b {
		return 1
	}
	return 0
}

func EncodeRevertReason(reason string) []byte {
	selector := GetFunctionSelector("Error(string)")
	t, err := abi.NewType("string", "", nil)
	if err != nil {
		// if type creation fails, return the selector only
		return selector
	}
	args := abi.Arguments{{Type: t}}
	encoded, _ := args.Pack(reason)
	return append(selector, encoded...)
}

// EncodeReturnData encodes the given value into Ethereum ABI return data format.
func EncodeReturnData(typ string, value interface{}) ([]byte, error) {
	t, err := abi.NewType(typ, "", nil)
	if err != nil {
		return nil, fmt.Errorf("invalid abi type: %w", err)
	}

	switch typ {
	case "uint256", "int256":
		switch v := value.(type) {
		case int:
			value = big.NewInt(int64(v))
		case uint:
			value = new(big.Int).SetUint64(uint64(v))
		case int64:
			value = big.NewInt(v)
		case uint64:
			value = new(big.Int).SetUint64(v)
		case *big.Int:
			// ok
		default:
			return nil, fmt.Errorf("invalid value type for %s: %T", typ, value)
		}

	case "address":
		switch v := value.(type) {
		case string:
			value = common.HexToAddress(v)
		case common.Address:
			// ok
		default:
			return nil, fmt.Errorf("invalid address type: %T", value)
		}

	case "bool":
		switch value.(type) {
		case bool:
			// ok
		default:
			return nil, fmt.Errorf("invalid bool type: %T", value)
		}

	case "string":
		switch value.(type) {
		case string:
			// ok
		default:
			return nil, fmt.Errorf("invalid string type: %T", value)
		}

	case "bytes":
		switch value.(type) {
		case []byte:
			// ok
		default:
			return nil, fmt.Errorf("invalid bytes type: %T", value)
		}
	}

	args := abi.Arguments{{Type: t}}
	return args.Pack(value)
}

// GetAddressFromSignature tạo ra một địa chỉ Ethereum (20 bytes) từ một chữ ký.
// Địa chỉ này được tạo bằng cách đặt 16 byte đầu là 0 và 4 byte cuối
// là 4 byte đầu của hash Keccak256 của chữ ký.
func GetAddressFromIdentifier(moduleSignature string) common.Address {
	// Băm chữ ký bằng Keccak256 để tạo ra một chuỗi 32 byte.
	hash := crypto.Keccak256([]byte(moduleSignature))

	// Tạo một mảng byte 20-byte cho địa chỉ.
	// Mảng này sẽ được khởi tạo với tất cả các giá trị là 0.
	var addressBytes [20]byte

	// Sao chép 4 byte ĐẦU TIÊN từ hash vào 4 vị trí CUỐI CÙNG của mảng address.
	// - addressBytes[16:]: Chỉ định đích là 4 byte cuối của mảng 20 byte (bắt đầu từ vị trí 16).
	// - hash[:4]: Chỉ định nguồn là 4 byte đầu của mảng hash 32 byte.
	copy(addressBytes[16:], hash[:4])

	// Chuyển đổi mảng 20 byte thành một địa chỉ Ethereum.
	return common.BytesToAddress(addressBytes[:])
}

// MaxDuration returns the larger of two time.Duration values
func MaxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
