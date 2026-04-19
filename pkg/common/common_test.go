package common

import (
	"encoding/hex"
	"testing"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ──────────────────────────────────────────────
// PublicKey
// ──────────────────────────────────────────────

func TestPubkeyFromBytes_FullLength(t *testing.T) {
	input := make([]byte, 48)
	for i := range input {
		input[i] = byte(i)
	}
	pk := PubkeyFromBytes(input)
	assert.Equal(t, input, pk.Bytes())
}

func TestPubkeyFromBytes_ShortInput(t *testing.T) {
	// Short input should be zero-padded
	input := []byte{0xaa, 0xbb}
	pk := PubkeyFromBytes(input)

	bytes := pk.Bytes()
	assert.Equal(t, byte(0xaa), bytes[0])
	assert.Equal(t, byte(0xbb), bytes[1])
	assert.Equal(t, byte(0x00), bytes[2], "remaining bytes should be zero")
	assert.Len(t, bytes, 48)
}

func TestPublicKey_String(t *testing.T) {
	input := make([]byte, 48)
	input[0] = 0xab
	input[47] = 0xcd
	pk := PubkeyFromBytes(input)

	s := pk.String()
	assert.Equal(t, hex.EncodeToString(input), s)
	assert.Len(t, s, 96) // 48 bytes = 96 hex chars
}

func TestPublicKey_Bytes_Roundtrip(t *testing.T) {
	input := make([]byte, 48)
	input[10] = 0xff
	pk := PubkeyFromBytes(input)
	roundtripped := PubkeyFromBytes(pk.Bytes())
	assert.Equal(t, pk, roundtripped)
}

// ──────────────────────────────────────────────
// PrivateKey
// ──────────────────────────────────────────────

func TestPrivateKeyFromBytes_FullLength(t *testing.T) {
	input := make([]byte, 32)
	for i := range input {
		input[i] = byte(i + 100)
	}
	sk := PrivateKeyFromBytes(input)
	assert.Equal(t, input, sk.Bytes())
}

func TestPrivateKeyFromBytes_ShortInput(t *testing.T) {
	input := []byte{0x01}
	sk := PrivateKeyFromBytes(input)
	bytes := sk.Bytes()
	assert.Equal(t, byte(0x01), bytes[0])
	assert.Equal(t, byte(0x00), bytes[1])
	assert.Len(t, bytes, 32)
}

func TestPrivateKey_String(t *testing.T) {
	input := make([]byte, 32)
	input[0] = 0xde
	sk := PrivateKeyFromBytes(input)
	s := sk.String()
	assert.Equal(t, hex.EncodeToString(input), s)
	assert.Len(t, s, 64) // 32 bytes = 64 hex chars
}

// ──────────────────────────────────────────────
// Sign
// ──────────────────────────────────────────────

func TestSignFromBytes_FullLength(t *testing.T) {
	input := make([]byte, 96)
	for i := range input {
		input[i] = byte(i % 256)
	}
	sig := SignFromBytes(input)
	assert.Equal(t, input, sig.Bytes())
}

func TestSignFromBytes_ShortInput(t *testing.T) {
	input := []byte{0xaa}
	sig := SignFromBytes(input)
	bytes := sig.Bytes()
	assert.Equal(t, byte(0xaa), bytes[0])
	assert.Equal(t, byte(0x00), bytes[1])
	assert.Len(t, bytes, 96)
}

func TestSign_String(t *testing.T) {
	input := make([]byte, 96)
	input[0] = 0xfe
	sig := SignFromBytes(input)
	s := sig.String()
	assert.Equal(t, hex.EncodeToString(input), s)
	assert.Len(t, s, 192) // 96 bytes = 192 hex chars
}

// ──────────────────────────────────────────────
// AddressFromPubkey
// ──────────────────────────────────────────────

func TestAddressFromPubkey_Deterministic(t *testing.T) {
	input := make([]byte, 48)
	for i := range input {
		input[i] = byte(i)
	}
	pk := PubkeyFromBytes(input)

	addr1 := AddressFromPubkey(pk)
	addr2 := AddressFromPubkey(pk)
	assert.Equal(t, addr1, addr2, "same pubkey should produce same address")
	assert.NotEqual(t, ethcommon.Address{}, addr1, "address should not be zero")
}

func TestAddressFromPubkey_DifferentKeys(t *testing.T) {
	pk1 := PubkeyFromBytes([]byte{0x01})
	pk2 := PubkeyFromBytes([]byte{0x02})

	addr1 := AddressFromPubkey(pk1)
	addr2 := AddressFromPubkey(pk2)
	assert.NotEqual(t, addr1, addr2, "different pubkeys should produce different addresses")
}

func TestAddressFromPubkey_ReturnType(t *testing.T) {
	pk := PubkeyFromBytes(make([]byte, 48))
	addr := AddressFromPubkey(pk)
	// Address should be 20 bytes
	require.Len(t, addr.Bytes(), 20)
}
