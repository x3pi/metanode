package utils

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Helper function để encode indexed topic
func EncodeIndexedTopic(arg interface{}, argType abi.Type) (string, error) {
	switch argType.T {
	case abi.AddressTy:
		addr, ok := arg.(common.Address)
		if !ok {
			return "", fmt.Errorf("expected address, got %T", arg)
		}
		// Address indexed: pad to 32 bytes
		return common.BytesToHash(addr.Bytes()).Hex(), nil

	case abi.UintTy, abi.IntTy:
		var num *big.Int
		switch v := arg.(type) {
		case *big.Int:
			num = v
		case int64:
			num = big.NewInt(v)
		case uint64:
			num = new(big.Int).SetUint64(v)
		default:
			return "", fmt.Errorf("expected number, got %T", arg)
		}
		// Uint/Int indexed: encode as 32-byte hash
		hash := common.BigToHash(num)
		return hash.Hex(), nil

	case abi.BytesTy, abi.StringTy:
		// Bytes/String indexed: use Keccak256 hash
		var data []byte
		switch v := arg.(type) {
		case []byte:
			data = v
		case string:
			data = []byte(v)
		default:
			return "", fmt.Errorf("expected bytes or string, got %T", arg)
		}
		hash := crypto.Keccak256Hash(data)
		return hash.Hex(), nil

	default:
		return "", fmt.Errorf("unsupported indexed type: %v", argType.T)
	}
}
