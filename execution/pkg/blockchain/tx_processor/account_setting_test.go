package tx_processor

import (
	"testing"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

func TestIsValidGoAccountType(t *testing.T) {
	tests := []struct {
		name  string
		input pb.ACCOUNT_TYPE
		want  bool
	}{
		{"regular", pb.ACCOUNT_TYPE_REGULAR_ACCOUNT, true},
		{"read_write_strict", pb.ACCOUNT_TYPE_READ_WRITE_STRICT, true},
		{"unknown_high", pb.ACCOUNT_TYPE(99), false},
		{"unknown_negative", pb.ACCOUNT_TYPE(255), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidGoAccountType(tt.input); got != tt.want {
				t.Errorf("isValidGoAccountType(%d) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestPackSetBlsPublicKey(t *testing.T) {
	// Valid public key (48 bytes)
	validKey := make([]byte, 48)
	for i := range validKey {
		validKey[i] = byte(i + 1)
	}

	data, err := PackSetBlsPublicKey(validKey)
	if err != nil {
		t.Fatalf("PackSetBlsPublicKey with valid key: %v", err)
	}
	if len(data) < 4 {
		t.Fatal("PackSetBlsPublicKey: output too short")
	}

	// Empty key should error
	_, err = PackSetBlsPublicKey([]byte{})
	if err == nil {
		t.Error("PackSetBlsPublicKey with empty key should return error")
	}

	// Nil key should error
	_, err = PackSetBlsPublicKey(nil)
	if err == nil {
		t.Error("PackSetBlsPublicKey with nil key should return error")
	}
}

func TestPackSetAccountType(t *testing.T) {
	// Valid type
	data, err := PackSetAccountType(pb.ACCOUNT_TYPE_REGULAR_ACCOUNT)
	if err != nil {
		t.Fatalf("PackSetAccountType: %v", err)
	}
	if len(data) < 4 {
		t.Fatal("PackSetAccountType: output too short")
	}

	// Invalid type
	_, err = PackSetAccountType(pb.ACCOUNT_TYPE(99))
	if err == nil {
		t.Error("PackSetAccountType with invalid type should return error")
	}
}

func TestPackAndUnpackSetBlsPublicKey(t *testing.T) {
	validKey := make([]byte, 48)
	for i := range validKey {
		validKey[i] = byte(i + 1)
	}

	packed, err := PackSetBlsPublicKey(validKey)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}

	unpacked, err := UnpackSetBlsPublicKeyInput(packed)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}

	if len(unpacked) != len(validKey) {
		t.Fatalf("roundtrip length mismatch: got %d, want %d", len(unpacked), len(validKey))
	}
	for i := range validKey {
		if unpacked[i] != validKey[i] {
			t.Fatalf("roundtrip byte %d mismatch: got %d, want %d", i, unpacked[i], validKey[i])
		}
	}
}

func TestPackAndUnpackSetAccountType(t *testing.T) {
	accountType := pb.ACCOUNT_TYPE_READ_WRITE_STRICT

	packed, err := PackSetAccountType(accountType)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}

	unpacked, err := UnpackSetAccountTypeInput(packed)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}

	if unpacked != accountType {
		t.Errorf("roundtrip: got %v, want %v", unpacked, accountType)
	}
}

func TestUnpackSetBlsPublicKeyInput_InvalidCalldata(t *testing.T) {
	// Too short
	_, err := UnpackSetBlsPublicKeyInput([]byte{0x01, 0x02})
	if err == nil {
		t.Error("expected error for short calldata")
	}

	// Wrong function selector
	wrongSelector := make([]byte, 68)
	_, err = UnpackSetBlsPublicKeyInput(wrongSelector)
	if err == nil {
		t.Error("expected error for wrong function selector")
	}
}

func TestPackBlsPublicKey(t *testing.T) {
	data, err := PackBlsPublicKey()
	if err != nil {
		t.Fatalf("PackBlsPublicKey: %v", err)
	}
	if len(data) != 4 {
		t.Errorf("PackBlsPublicKey output length = %d, want 4 (just selector)", len(data))
	}
}
