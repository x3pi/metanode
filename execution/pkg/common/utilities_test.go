package common

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestSplitConnectionAddress(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		wantIP   string
		wantPort int
		wantErr  bool
	}{
		{
			name:     "valid address",
			address:  "127.0.0.1:4201",
			wantIP:   "127.0.0.1",
			wantPort: 4201,
			wantErr:  false,
		},
		{
			name:     "valid address with 0.0.0.0",
			address:  "0.0.0.0:8080",
			wantIP:   "0.0.0.0",
			wantPort: 8080,
			wantErr:  false,
		},
		{
			name:    "missing port",
			address: "127.0.0.1",
			wantErr: true,
		},
		{
			name:    "invalid port",
			address: "127.0.0.1:abc",
			wantErr: true,
		},
		{
			name:    "too many colons",
			address: "127.0.0.1:8080:extra",
			wantErr: true,
		},
		{
			name:    "empty string",
			address: "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIP, gotPort, err := SplitConnectionAddress(tt.address)
			if (err != nil) != tt.wantErr {
				t.Errorf("SplitConnectionAddress() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if gotIP != tt.wantIP {
					t.Errorf("SplitConnectionAddress() IP = %v, want %v", gotIP, tt.wantIP)
				}
				if gotPort != tt.wantPort {
					t.Errorf("SplitConnectionAddress() Port = %v, want %v", gotPort, tt.wantPort)
				}
			}
		})
	}
}

func TestAddressesToBytes(t *testing.T) {
	tests := []struct {
		name      string
		addresses []common.Address
		wantLen   int
	}{
		{
			name:      "empty list",
			addresses: []common.Address{},
			wantLen:   0,
		},
		{
			name: "single address",
			addresses: []common.Address{
				common.HexToAddress("0xda7284fac5e804f8b9d71aa39310f0f86776b51d"),
			},
			wantLen: 1,
		},
		{
			name: "multiple addresses",
			addresses: []common.Address{
				common.HexToAddress("0xda7284fac5e804f8b9d71aa39310f0f86776b51d"),
				common.HexToAddress("0x51bdebc98ad4e158b7bc02220ab8ab4cf18af6bd"),
			},
			wantLen: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AddressesToBytes(tt.addresses)
			if len(got) != tt.wantLen {
				t.Errorf("AddressesToBytes() length = %v, want %v", len(got), tt.wantLen)
			}
			for i, addr := range tt.addresses {
				if len(got[i]) != 20 {
					t.Errorf("AddressesToBytes()[%d] length = %v, want 20", i, len(got[i]))
				}
				if common.BytesToAddress(got[i]) != addr {
					t.Errorf("AddressesToBytes()[%d] = %v, want %v", i, common.BytesToAddress(got[i]), addr)
				}
			}
		})
	}
}

func TestStringToUint256(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantSuccess bool
		wantZero    bool
	}{
		{
			name:        "valid number",
			input:       "1000000000000000000",
			wantSuccess: true,
			wantZero:    false,
		},
		{
			name:        "zero",
			input:       "0",
			wantSuccess: true,
			wantZero:    true,
		},
		{
			name:        "invalid string",
			input:       "not_a_number",
			wantSuccess: false,
		},
		{
			name:        "empty string",
			input:       "",
			wantSuccess: false,
		},
		{
			name:        "large number",
			input:       "115792089237316195423570985008687907853269984665640564039457584007913129639935",
			wantSuccess: true,
			wantZero:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, success := StringToUint256(tt.input)
			if success != tt.wantSuccess {
				t.Errorf("StringToUint256() success = %v, want %v", success, tt.wantSuccess)
			}
			if tt.wantSuccess && tt.wantZero && !got.IsZero() {
				t.Errorf("StringToUint256() should be zero for input '0'")
			}
			if tt.wantSuccess && !tt.wantZero && got.IsZero() {
				t.Errorf("StringToUint256() should not be zero for input '%s'", tt.input)
			}
		})
	}
}

func TestReadLastLine(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()

	tests := []struct {
		name     string
		content  string
		wantLine string
		wantErr  bool
	}{
		{
			name:     "single line",
			content:  "hello world",
			wantLine: "hello world",
			wantErr:  false,
		},
		{
			name:     "multiple lines",
			content:  "line1\nline2\nline3",
			wantLine: "line3",
			wantErr:  false,
		},
		{
			name:     "empty file",
			content:  "",
			wantLine: "",
			wantErr:  false,
		},
		{
			name:     "trailing newline",
			content:  "line1\nline2\n",
			wantLine: "line2",
			wantErr:  false,
		},
		{
			name:     "csv format",
			content:  "1,0xabc\n2,0xdef\n11,0x6f641b71850f0555d074cab2ee73b53f26964107fdfd63ce75fdb37b39105a67",
			wantLine: "11,0x6f641b71850f0555d074cab2ee73b53f26964107fdfd63ce75fdb37b39105a67",
			wantErr:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write test file
			filePath := filepath.Join(tmpDir, tt.name+".txt")
			err := os.WriteFile(filePath, []byte(tt.content), 0644)
			if err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			got, err := ReadLastLine(filePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadLastLine() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.wantLine {
				t.Errorf("ReadLastLine() = %q, want %q", got, tt.wantLine)
			}
		})
	}

	// Test non-existent file
	t.Run("non-existent file", func(t *testing.T) {
		_, err := ReadLastLine(filepath.Join(tmpDir, "non_existent.txt"))
		if err == nil {
			t.Error("ReadLastLine() should error for non-existent file")
		}
	})
}

func TestErrorInvalidConnectionAddress(t *testing.T) {
	if ErrorInvalidConnectionAddress == nil {
		t.Error("ErrorInvalidConnectionAddress should not be nil")
	}
	if ErrorInvalidConnectionAddress.Error() != "invalid connection address" {
		t.Errorf("ErrorInvalidConnectionAddress = %q, want 'invalid connection address'", ErrorInvalidConnectionAddress.Error())
	}
}
