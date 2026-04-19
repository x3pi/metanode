package rpcquery

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

func TestConvertLogsToProto_Empty(t *testing.T) {
	result := ConvertLogsToProto(nil)
	if len(result) != 0 {
		t.Errorf("expected empty, got %d entries", len(result))
	}
}

func TestConvertLogsToProto_SingleLog(t *testing.T) {
	logs := []*ethtypes.Log{
		{
			Address:     common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678"),
			Topics:      []common.Hash{common.HexToHash("0xabc")},
			Data:        []byte{0x01, 0x02},
			BlockNumber: 42,
			TxIndex:     3,
			Index:       7,
			Removed:     false,
		},
	}
	result := ConvertLogsToProto(logs)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	if result[0].BlockNumber != 42 {
		t.Errorf("expected block 42, got %d", result[0].BlockNumber)
	}
	if result[0].TransactionIndex != 3 {
		t.Errorf("expected txIndex 3, got %d", result[0].TransactionIndex)
	}
	if result[0].LogIndex != 7 {
		t.Errorf("expected logIndex 7, got %d", result[0].LogIndex)
	}
	if len(result[0].Topics) != 1 {
		t.Errorf("expected 1 topic, got %d", len(result[0].Topics))
	}
}

func TestFormatBigIntHex(t *testing.T) {
	tests := []struct {
		input *big.Int
		want  string
	}{
		{nil, "0x0"},
		{big.NewInt(0), "0x0"},
		{big.NewInt(255), "0xff"},
		{big.NewInt(4096), "0x1000"},
	}
	for _, tt := range tests {
		got := FormatBigIntHex(tt.input)
		if got != tt.want {
			t.Errorf("FormatBigIntHex(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatUint64Hex(t *testing.T) {
	tests := []struct {
		input uint64
		want  string
	}{
		{0, "0x0"},
		{1, "0x1"},
		{256, "0x100"},
		{65535, "0xffff"},
	}
	for _, tt := range tests {
		got := FormatUint64Hex(tt.input)
		if got != tt.want {
			t.Errorf("FormatUint64Hex(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
