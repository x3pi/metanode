package parse_helper

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

func ParseResponseData(response []byte) {
	if len(response) == 0 {
		return
	}
	logger.Info("[Parse Data] Raw Hex: %s", hexutil.Encode(response))

	// Case Uint256
	if len(response) == 32 {
		valInt := new(big.Int).SetBytes(response)
		logger.Info("[Parse Data] As Uint256: %s", valInt.String())
	}

	// Case String
	if len(response) >= 64 {
		stringABI, _ := abi.NewType("string", "", nil)
		args := abi.Arguments{{Type: stringABI}}
		unpacked, err := args.Unpack(response)
		if err == nil {
			strVal := unpacked[0].(string)
			logger.Info("[Parse Data] As String: %s", strVal)
		}
	}
}
