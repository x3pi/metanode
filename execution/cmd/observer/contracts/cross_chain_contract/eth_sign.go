package cross_chain_contract

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// EthSignMessageId creates an ETH ECDSA signature for a messageId.
// messageId is already keccak256(abi.encode(packet fields...)) so it's sufficient.
//
// Contract will verify:
//
//	prefixedHash = keccak256("\x19Ethereum Signed Message:\n32", messageId)
//	ecrecover(prefixedHash, v, r, s) == msg.sender
func EthSignMessageId(
	ethKey *ecdsa.PrivateKey,
	messageId [32]byte,
) ([]byte, error) {
	if ethKey == nil {
		return nil, fmt.Errorf("ETH private key is nil")
	}

	// EIP-191 prefix: "\x19Ethereum Signed Message:\n32" + messageId
	prefixedHash := crypto.Keccak256Hash(
		[]byte("\x19Ethereum Signed Message:\n32"),
		messageId[:],
	)

	sig, err := crypto.Sign(prefixedHash.Bytes(), ethKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign: %w", err)
	}

	// Ethereum convention: v = sig[64] + 27
	if sig[64] < 27 {
		sig[64] += 27
	}

	logger.Info("🔏 ETH signed messageId=%x, signer=%s",
		messageId, crypto.PubkeyToAddress(ethKey.PublicKey).Hex())

	return sig, nil
}

// EthSignBatchNonceRange creates ONE ETH ECDSA signature for a batch
// using keccak256(abi.encode(firstNonce, lastNonce)).
//
// This matches the Solidity batchReceiveMessage / batchProcessConfirmation
// which verify: ecrecover(keccak256(abi.encode(first, last)), sig) == msg.sender
func EthSignBatchNonceRange(
	ethKey *ecdsa.PrivateKey,
	firstNonce *big.Int,
	lastNonce *big.Int,
) ([]byte, error) {
	if ethKey == nil {
		return nil, fmt.Errorf("ETH private key is nil")
	}
	if firstNonce == nil || lastNonce == nil {
		return nil, fmt.Errorf("firstNonce or lastNonce is nil")
	}

	// abi.encode(firstNonce, lastNonce) — each uint256 is 32 bytes, left-padded
	firstBytes := common.LeftPadBytes(firstNonce.Bytes(), 32)
	lastBytes := common.LeftPadBytes(lastNonce.Bytes(), 32)
	encoded := append(firstBytes, lastBytes...)

	// batchHash = keccak256(abi.encode(firstNonce, lastNonce))
	batchHash := crypto.Keccak256(encoded)

	// EIP-191 prefix: "\x19Ethereum Signed Message:\n32" + batchHash
	prefixedHash := crypto.Keccak256Hash(
		[]byte("\x19Ethereum Signed Message:\n32"),
		batchHash,
	)

	sig, err := crypto.Sign(prefixedHash.Bytes(), ethKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign batch nonce range: %w", err)
	}

	// Ethereum convention: v = sig[64] + 27
	if sig[64] < 27 {
		sig[64] += 27
	}

	logger.Info("🔏 ETH signed batch nonce range [%v..%v], signer=%s",
		firstNonce, lastNonce, crypto.PubkeyToAddress(ethKey.PublicKey).Hex())

	return sig, nil
}
