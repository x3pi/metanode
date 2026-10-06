package transaction

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	// MaxStandardTxEnvelopeSize is the maximum allowed size for standard (non-blob) EIP-2718 envelopes (128 KB).
	MaxStandardTxEnvelopeSize = 128 * 1024
	// MaxRawEthTxEnvelopeSize is the maximum envelope size for EIP-4844 blob transactions with KZG sidecars (1 MB).
	MaxRawEthTxEnvelopeSize = 1024 * 1024
	// MaxBatchTxCount is the maximum number of transactions allowed in a single TCP batch.
	MaxBatchTxCount = 1000
)

var (
	secp256k1N     = crypto.S256().Params().N
	secp256k1halfN = new(big.Int).Rsh(secp256k1N, 1)
)

// ValidateEthTxEnvelope validates an Ethereum transaction envelope before admission:
// 1. Transaction must not be nil.
// 2. Reject pre-EIP-155 transactions (must be protected by EIP-155 with non-nil positive chain ID).
// 3. Chain ID must match expectedChainId if expectedChainId is provided (> 0).
// 4. Signature values (r, s, v) must be present, positive, r < N, and s <= N/2 (anti-malleability per EIP-2).
// 5. Sender address must be recoverable and non-zero.
func ValidateEthTxEnvelope(ethTx *e_types.Transaction, expectedChainId *big.Int) error {
	if ethTx == nil {
		return errors.New("transaction is nil")
	}

	// 1. Reject pre-EIP-155 unprotected transactions
	if !ethTx.Protected() {
		return errors.New("pre-EIP-155 unprotected transactions are not allowed")
	}

	chainID := ethTx.ChainId()
	if chainID == nil || chainID.Sign() <= 0 {
		return errors.New("transaction missing chain ID")
	}

	// 2. Verify chain ID matches expected
	if expectedChainId != nil && expectedChainId.Sign() > 0 {
		if chainID.Cmp(expectedChainId) != 0 {
			return fmt.Errorf("invalid chain ID: expected %d, got %d", expectedChainId, chainID)
		}
	}

	// 3. Verify signature values
	v, r, s := ethTx.RawSignatureValues()
	if v == nil || r == nil || s == nil {
		return errors.New("transaction missing signature values (v, r, s)")
	}

	if r.Sign() <= 0 || s.Sign() <= 0 {
		return errors.New("signature r and s must be positive")
	}

	if r.Cmp(secp256k1N) >= 0 {
		return errors.New("signature r exceeds curve order N")
	}

	// EIP-2: Malleable signature check: s must be in lower half of curve order (s <= N/2)
	if s.Cmp(secp256k1halfN) > 0 {
		return errors.New("malleable signature: s exceeds curve order / 2 (EIP-2)")
	}

	// 4. Recover sender address
	signer := e_types.LatestSignerForChainID(chainID)
	from, err := e_types.Sender(signer, ethTx)
	if err != nil {
		return fmt.Errorf("failed to recover sender: %w", err)
	}
	if from == (common.Address{}) {
		return errors.New("recovered sender address cannot be zero")
	}

	return nil
}

// ClassifyEthTxError maps an envelope validation or mempool error into an explicit typed TransactionError.
func ClassifyEthTxError(err error) *TransactionError {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "pre-EIP-155"):
		return ErrPreEIP155
	case strings.Contains(msg, "chain ID"):
		return InvalidChainId
	case strings.Contains(msg, "malleable signature") || strings.Contains(msg, "curve order / 2"):
		return ErrMalleableSignature
	case strings.Contains(msg, "recover sender") || strings.Contains(msg, "recovered sender"):
		return ErrSenderRecovery
	case strings.Contains(msg, "exceeds max") || strings.Contains(msg, "envelope size"):
		return ErrExceedsMaxEnvelopeSize
	case strings.Contains(msg, "too many transactions") || strings.Contains(msg, "max batch"):
		return ErrExceedsMaxBatchSize
	case strings.Contains(msg, "decode") || strings.Contains(msg, "RLP format"):
		return ErrDecodeRawEth
	case strings.Contains(msg, "already known") || strings.Contains(msg, "already exists"):
		return ErrAlreadyKnown
	case strings.Contains(msg, "nonce"):
		return InvalidNonce
	case strings.Contains(msg, "balance") || strings.Contains(msg, "funds"):
		return ErrInsufficientBalance
	case strings.Contains(msg, "gas price") || strings.Contains(msg, "base fee"):
		return InvalidMaxGasPrice
	default:
		return InvalidTransaction
	}
}
