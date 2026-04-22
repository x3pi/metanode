package argument_encode

import (
	"math"

	"github.com/holiman/uint256"
)

func EncodeSingleString(input string) []byte {
	start := uint256.NewInt(32).Bytes32()
	length := uint256.NewInt(uint64(len(input))).Bytes32()
	allocateLen := math.Ceil(float64(len(input))/32.0) * 32
	rs := make([]byte, uint64(64+allocateLen))
	copy(rs[0:], start[:])
	copy(rs[32:], length[:])
	copy(rs[64:], input[:])
	return rs
}
