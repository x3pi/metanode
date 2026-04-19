package bls

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/stretchr/testify/assert"
)

func TestNewKeyPair(t *testing.T) {
	// Use a known valid BLS private key (same as testSecret1 in bls_test.go)
	keyPair := NewKeyPair(common.FromHex("372e9d6411071707a7e7ba76a51c7907a6c799f0cb972df1671e582d649caabf"))
	logger.Info(keyPair)
	assert.NotNil(t, keyPair)
	assert.NotEmpty(t, keyPair.PublicKey().Bytes(), "public key should not be empty")
	assert.NotEqual(t, common.Address{}, keyPair.Address(), "address should not be zero")
}
