// Package block_signer provides BLS-based block hash signing and verification
// for ensuring state consistency between Go Master and Go Sub nodes.
//
// Design:
//   - Master signs each block hash after commit using its BLS private key
//   - Sub nodes verify the signature before accepting blocks from P2P
//   - Uses BLS12-381 (same as transaction signing) for aggregate signature support
//   - Zero TPS impact: signing is async in commitWorker, verification only on Sub
package block_signer

import (
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// BlockSigner handles signing block hashes using BLS12-381.
// Used by Go Master to sign blocks before broadcasting to Sub nodes.
type BlockSigner struct {
	privateKey cm.PrivateKey
	publicKey  cm.PublicKey
	address    common.Address

	mu sync.RWMutex
}

// NewBlockSigner creates a new BlockSigner from a BLS private key (hex string).
// Returns nil if the private key is empty (signing disabled).
func NewBlockSigner(blsPrivateKeyHex string) (*BlockSigner, error) {
	if blsPrivateKeyHex == "" {
		return nil, fmt.Errorf("BLS private key is empty, block signing disabled")
	}

	privateKey, publicKey, address := bls.GenerateKeyPairFromSecretKey(blsPrivateKeyHex)
	if len(privateKey.Bytes()) == 0 {
		return nil, fmt.Errorf("failed to generate BLS key pair from private key")
	}

	logger.Info("🔏 [BLOCK SIGNER] Initialized with address=%s, pubkey=%s...%s",
		address.Hex(),
		hex.EncodeToString(publicKey.Bytes()[:8]),
		hex.EncodeToString(publicKey.Bytes()[len(publicKey.Bytes())-4:]),
	)

	return &BlockSigner{
		privateKey: privateKey,
		publicKey:  publicKey,
		address:    address,
	}, nil
}

// NewBlockSignerFromKeyPair creates a BlockSigner from an existing BLS KeyPair.
func NewBlockSignerFromKeyPair(kp *bls.KeyPair) *BlockSigner {
	return &BlockSigner{
		privateKey: kp.PrivateKey(),
		publicKey:  kp.PublicKey(),
		address:    kp.Address(),
	}
}

// SignBlockHash signs a block hash using the BLS private key.
// Returns the BLS signature bytes (96 bytes compressed).
func (bs *BlockSigner) SignBlockHash(blockHash common.Hash) []byte {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	sig := bls.Sign(bs.privateKey, blockHash.Bytes())
	return sig.Bytes()
}

// PublicKey returns the signer's BLS public key bytes.
func (bs *BlockSigner) PublicKey() []byte {
	return bs.publicKey.Bytes()
}

// Address returns the signer's address.
func (bs *BlockSigner) Address() common.Address {
	return bs.address
}

// VerifyBlockSignature verifies a BLS signature on a block hash.
// This is a static function — can be called without a BlockSigner instance.
// Returns true if the signature is valid for the given block hash and public key.
func VerifyBlockSignature(blockHash common.Hash, signature []byte, pubKeyBytes []byte) bool {
	if len(signature) == 0 || len(pubKeyBytes) == 0 {
		return false
	}

	pubKey := cm.PubkeyFromBytes(pubKeyBytes)
	sig := cm.SignFromBytes(signature)

	return bls.VerifySign(pubKey, sig, blockHash.Bytes())
}

// VerifyAggregateBlockSignature verifies an aggregate BLS signature on multiple block hashes.
// Used for checkpoint verification where multiple validators sign the same state hash.
func VerifyAggregateBlockSignature(messages [][]byte, aggregateSignature []byte, pubKeys [][]byte) bool {
	if len(aggregateSignature) == 0 || len(pubKeys) == 0 || len(messages) == 0 {
		return false
	}

	return bls.VerifyAggregateSign(pubKeys, aggregateSignature, messages)
}

// CreateAggregateSignature aggregates multiple BLS signatures into one.
// The aggregate signature is 96 bytes regardless of the number of input signatures.
func CreateAggregateSignature(signatures [][]byte) ([]byte, error) {
	if len(signatures) == 0 {
		return nil, fmt.Errorf("no signatures to aggregate")
	}

	return bls.CreateAggregateSign(signatures), nil
}
