package utils

import (
	"math/big"
	"testing"
	"time"
)

func TestUint64ToBytesAndBack(t *testing.T) {
	cases := []uint64{0, 1, 42, 255, 65535, 1<<63 - 1, 1<<64 - 1}
	for _, v := range cases {
		b := Uint64ToBytes(v)
		if len(b) != 8 {
			t.Fatalf("Uint64ToBytes(%d) length = %d, want 8", v, len(b))
		}
		got, err := BytesToUint64(b)
		if err != nil {
			t.Fatalf("BytesToUint64 for %d: unexpected error: %v", v, err)
		}
		if got != v {
			t.Errorf("roundtrip %d: got %d", v, got)
		}
	}
}

func TestBytesToUint64_InvalidLength(t *testing.T) {
	badInputs := [][]byte{nil, {}, {1}, {1, 2, 3}, {1, 2, 3, 4, 5, 6, 7, 8, 9}}
	for _, b := range badInputs {
		_, err := BytesToUint64(b)
		if err == nil {
			t.Errorf("BytesToUint64(%v) expected error, got nil", b)
		}
	}
}

func TestUint64ToBigInt(t *testing.T) {
	cases := []uint64{0, 1, 42, 1<<64 - 1}
	for _, v := range cases {
		big := Uint64ToBigInt(v)
		if big.Uint64() != v {
			t.Errorf("Uint64ToBigInt(%d) = %s", v, big.String())
		}
	}
}

func TestBigIntToUint64(t *testing.T) {
	tests := []struct {
		name    string
		input   *big.Int
		want    uint64
		wantErr bool
	}{
		{"zero", big.NewInt(0), 0, false},
		{"positive", big.NewInt(42), 42, false},
		{"nil", nil, 0, true},
		{"overflow", new(big.Int).Add(new(big.Int).SetUint64(^uint64(0)), big.NewInt(1)), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BigIntToUint64(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("BigIntToUint64() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("BigIntToUint64() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseBlockNumber(t *testing.T) {
	tests := []struct {
		input   string
		want    uint64
		wantErr bool
	}{
		{"0x0", 0, false},
		{"0x1", 1, false},
		{"0xff", 255, false},
		{"0xFF", 255, false},
		{"ff", 255, false},
		{"0x10", 16, false},
		{"invalid", 0, true},
		{"0xZZZZ", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseBlockNumber(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseBlockNumber(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseBlockNumber(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetFunctionSelector(t *testing.T) {
	// Standard ERC-20 transfer(address,uint256) selector is 0xa9059cbb
	sel := GetFunctionSelector("transfer(address,uint256)")
	if len(sel) != 4 {
		t.Fatalf("selector length = %d, want 4", len(sel))
	}
	// Verify known selector
	if sel[0] != 0xa9 || sel[1] != 0x05 || sel[2] != 0x9c || sel[3] != 0xbb {
		t.Errorf("transfer selector = %x, want a9059cbb", sel)
	}
}

func TestCompareUint64(t *testing.T) {
	tests := []struct {
		a, b uint64
		want int
	}{
		{0, 0, 0}, {1, 0, 1}, {0, 1, -1}, {42, 42, 0},
	}
	for _, tt := range tests {
		got := CompareUint64(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("CompareUint64(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestEncodeRevertReason(t *testing.T) {
	reason := "insufficient balance"
	data := EncodeRevertReason(reason)
	if len(data) < 4 {
		t.Fatal("EncodeRevertReason returned too short data")
	}
	// First 4 bytes should be the Error(string) selector
	expectedSel := GetFunctionSelector("Error(string)")
	for i := 0; i < 4; i++ {
		if data[i] != expectedSel[i] {
			t.Fatalf("selector byte %d: got %x, want %x", i, data[i], expectedSel[i])
		}
	}
}

func TestEncodeReturnData(t *testing.T) {
	// Test bool
	data, err := EncodeReturnData("bool", true)
	if err != nil {
		t.Fatalf("EncodeReturnData bool: %v", err)
	}
	if len(data) != 32 {
		t.Errorf("bool return data length = %d, want 32", len(data))
	}

	// Test uint256
	data, err = EncodeReturnData("uint256", big.NewInt(42))
	if err != nil {
		t.Fatalf("EncodeReturnData uint256: %v", err)
	}
	if len(data) != 32 {
		t.Errorf("uint256 return data length = %d, want 32", len(data))
	}

	// Test invalid type
	_, err = EncodeReturnData("invalid_type", 0)
	if err == nil {
		t.Error("EncodeReturnData with invalid type should return error")
	}

	// Test type mismatch
	_, err = EncodeReturnData("bool", "not_a_bool")
	if err == nil {
		t.Error("EncodeReturnData bool with string should return error")
	}
}

func TestGetAddressFromIdentifier(t *testing.T) {
	addr := GetAddressFromIdentifier("testModule")
	// Expect first 16 bytes to be zero
	for i := 0; i < 16; i++ {
		if addr[i] != 0 {
			t.Fatalf("address byte %d = %x, want 0", i, addr[i])
		}
	}
	// Last 4 bytes should be non-zero (hash output)
	allZero := true
	for i := 16; i < 20; i++ {
		if addr[i] != 0 {
			allZero = false
		}
	}
	if allZero {
		t.Error("GetAddressFromIdentifier last 4 bytes should not all be zero")
	}

	// Same input should produce same output (deterministic)
	addr2 := GetAddressFromIdentifier("testModule")
	if addr != addr2 {
		t.Errorf("GetAddressFromIdentifier not deterministic: %s != %s", addr.Hex(), addr2.Hex())
	}
}

func TestMaxDuration(t *testing.T) {
	a := 5 * time.Second
	b := 10 * time.Second
	if MaxDuration(a, b) != b {
		t.Error("MaxDuration(5s, 10s) should return 10s")
	}
	if MaxDuration(b, a) != b {
		t.Error("MaxDuration(10s, 5s) should return 10s")
	}
	if MaxDuration(a, a) != a {
		t.Error("MaxDuration(5s, 5s) should return 5s")
	}
}
