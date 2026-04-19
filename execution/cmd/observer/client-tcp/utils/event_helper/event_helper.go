package event_helper

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

func ExtractAddress(val interface{}) (common.Address, bool) {
	switch v := val.(type) {
	case common.Address:
		return v, true
	case [20]byte:
		return common.BytesToAddress(v[:]), true
	case []byte:
		return common.BytesToAddress(v), true
	case string:
		if common.IsHexAddress(v) {
			return common.HexToAddress(v), true
		}
		if strings.HasPrefix(v, "0x") && len(v) >= 42 {
			return common.HexToAddress(v[:42]), true
		}
	}
	return common.Address{}, false
}

func ExtractBytes32Hex(val interface{}) string {
	switch v := val.(type) {
	case [32]byte:
		return "0x" + hex.EncodeToString(v[:])
	case []byte:
		return "0x" + hex.EncodeToString(v)
	case string:
		if strings.HasPrefix(v, "0x") {
			return v
		}
		return "0x" + v
	default:
		return ""
	}
}

func ExtractBigInt(val interface{}) *big.Int {
	switch v := val.(type) {
	case *big.Int:
		return v
	case uint64:
		return new(big.Int).SetUint64(v)
	case int64:
		return big.NewInt(v)
	default:
		return big.NewInt(0)
	}
}

func ExtractStringValue(val interface{}) string {
	switch v := val.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return string(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func ExtractBytesValue(val interface{}) []byte {
	switch v := val.(type) {
	case []byte:
		return append([]byte(nil), v...)
	case string:
		if strings.HasPrefix(v, "0x") {
			return common.FromHex(v)
		}
		return []byte(v)
	default:
		return nil
	}
}

func CleanHex(input string) string {
	s := strings.TrimSpace(input)
	if idx := strings.Index(s, "("); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	s = strings.ReplaceAll(s, " ", "")
	if len(s)%2 == 1 {
		s = "0" + s
	}
	return s
}

// GetBigIntFromEventData extracts a *big.Int from event data map with error handling
// Supports: *big.Int, common.Hash, uint64, int64
func GetBigIntFromEventData(eventData map[string]interface{}, key string) (*big.Int, error) {
	val, ok := eventData[key]
	if !ok {
		return nil, fmt.Errorf("key %s missing", key)
	}

	switch v := val.(type) {
	case *big.Int:
		return v, nil
	case common.Hash:
		return new(big.Int).SetBytes(v.Bytes()), nil
	case uint64:
		return new(big.Int).SetUint64(v), nil
	case int64:
		return big.NewInt(v), nil
	default:
		return nil, fmt.Errorf("key %s has invalid type %T, expected *big.Int, common.Hash, uint64, or int64", key, val)
	}
}

// GetAddressFromEventData extracts a common.Address from event data map with error handling
// Supports: common.Address, common.Hash, [20]byte, []byte, string
func GetAddressFromEventData(eventData map[string]interface{}, key string) (common.Address, error) {
	val, ok := eventData[key]
	if !ok {
		return common.Address{}, fmt.Errorf("key %s missing", key)
	}

	switch v := val.(type) {
	case common.Address:
		return v, nil
	case common.Hash:
		return common.BytesToAddress(v.Bytes()), nil
	case [20]byte:
		return common.BytesToAddress(v[:]), nil
	case []byte:
		return common.BytesToAddress(v), nil
	case string:
		if common.IsHexAddress(v) {
			return common.HexToAddress(v), nil
		}
		return common.Address{}, fmt.Errorf("key %s has invalid address string: %s", key, v)
	default:
		return common.Address{}, fmt.Errorf("key %s has invalid type %T, expected common.Address, common.Hash, []byte, or string", key, val)
	}
}
