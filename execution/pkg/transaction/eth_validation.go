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
		return fmt.Errorf("%w: transaction is nil", InvalidTransaction)
	}

	// 1. Reject pre-EIP-155 unprotected transactions
	if !ethTx.Protected() {
		return ErrPreEIP155
	}

	chainID := ethTx.ChainId()
	if chainID == nil || chainID.Sign() <= 0 {
		return fmt.Errorf("%w: transaction missing chain ID", InvalidChainId)
	}

	// 2. Verify chain ID matches expected
	if expectedChainId != nil && expectedChainId.Sign() > 0 {
		if chainID.Cmp(expectedChainId) != 0 {
			return fmt.Errorf("%w: invalid chain ID: expected %d, got %d", InvalidChainId, expectedChainId, chainID)
		}
	}

	// 3. Verify signature values
	v, r, s := ethTx.RawSignatureValues()
	if v == nil || r == nil || s == nil {
		return fmt.Errorf("%w: transaction missing signature values (v, r, s)", InvalidSignSecp)
	}

	if r.Sign() <= 0 || s.Sign() <= 0 {
		return fmt.Errorf("%w: signature r and s must be positive", InvalidSignSecp)
	}

	if r.Cmp(secp256k1N) >= 0 {
		return fmt.Errorf("%w: signature r exceeds curve order N", InvalidSignSecp)
	}

	// EIP-2: Malleable signature check: s must be in lower half of curve order (s <= N/2)
	if s.Cmp(secp256k1halfN) > 0 {
		return ErrMalleableSignature
	}

	// 4. Recover sender address
	signer := e_types.LatestSignerForChainID(chainID)
	from, err := e_types.Sender(signer, ethTx)
	if err != nil {
		return fmt.Errorf("%w: failed to recover sender: %v", ErrSenderRecovery, err)
	}
	if from == (common.Address{}) {
		return fmt.Errorf("%w: recovered sender address cannot be zero", ErrInvalidSender)
	}

	return nil
}

// ClassifyEthTxError maps an envelope validation, mempool, or execution error into an explicit typed TransactionError.
// It checks errors.As first for typed TransactionError instances, and falls back to string pattern matching.
func ClassifyEthTxError(err error) *TransactionError {
	if err == nil {
		return nil
	}
	var te *TransactionError
	if errors.As(err, &te) {
		return te
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "pre-eip-155"):
		return ErrPreEIP155
	case strings.Contains(msg, "chain id"):
		return InvalidChainId
	case strings.Contains(msg, "malleable signature") || strings.Contains(msg, "curve order / 2"):
		return ErrMalleableSignature
	case strings.Contains(msg, "recover sender") || strings.Contains(msg, "recovered sender"):
		return ErrSenderRecovery
	case strings.Contains(msg, "exceeds max") || strings.Contains(msg, "envelope size"):
		return ErrExceedsMaxEnvelopeSize
	case strings.Contains(msg, "too many transactions") || strings.Contains(msg, "max batch"):
		return ErrExceedsMaxBatchSize
	case strings.Contains(msg, "decode") || strings.Contains(msg, "rlp format"):
		return ErrDecodeRawEth
	case strings.Contains(msg, "already known") || strings.Contains(msg, "already exists"):
		return ErrAlreadyKnown
	case strings.Contains(msg, "nonce too high"):
		return ErrNonceTooHigh
	case strings.Contains(msg, "nonce too low") || strings.Contains(msg, "nonce"):
		return ErrNonceTooLow
	case strings.Contains(msg, "replacement transaction underpriced") || strings.Contains(msg, "underpriced"):
		return ErrReplacementUnderpriced
	case strings.Contains(msg, "intrinsic gas"):
		return ErrIntrinsicGasTooLow
	case strings.Contains(msg, "exceeds block gas limit"):
		return ErrExceedsBlockGasLimit
	case strings.Contains(msg, "max initcode size exceeded"):
		return ErrMaxInitCodeSizeExceeded
	case strings.Contains(msg, "gas limit reached"):
		return ErrGasLimitReached
	case strings.Contains(msg, "transaction type not supported"):
		return ErrTxTypeNotSupported
	case strings.Contains(msg, "invalid sender"):
		return ErrInvalidSender
	case strings.Contains(msg, "balance") || strings.Contains(msg, "funds") || strings.Contains(msg, "max fee"):
		return ErrInsufficientFunds
	case strings.Contains(msg, "gas price") || strings.Contains(msg, "base fee"):
		return InvalidMaxGasPrice
	default:
		return InvalidTransaction
	}
}

// GethStandardRPCError maps an error (typed TransactionError, wrapped error, or standard error)
// to the geth-standard JSON-RPC error code and message.
// Standard error code for tx rejection in geth is -32000.
func GethStandardRPCError(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var te *TransactionError
	if !errors.As(err, &te) {
		te = ClassifyEthTxError(err)
	}

	switch te.Code {
	case ErrNonceTooLow.Code, InvalidNonce.Code:
		return -32000, "nonce too low"
	case ErrNonceTooHigh.Code:
		return -32000, "nonce too high"
	case ErrInsufficientFunds.Code, ErrInsufficientBalance.Code, InvalidMaxFee.Code, InvalidAmount.Code:
		return -32000, "insufficient funds for gas * price + value"
	case ErrAlreadyKnown.Code:
		return -32000, "already known"
	case ErrReplacementUnderpriced.Code:
		return -32000, "replacement transaction underpriced"
	case ErrIntrinsicGasTooLow.Code, InvalidMaxGas.Code:
		return -32000, "intrinsic gas too low"
	case ErrExceedsBlockGasLimit.Code:
		return -32000, "exceeds block gas limit"
	case ErrInvalidSender.Code, InvalidSign.Code, InvalidSignSecp.Code, ErrSenderRecovery.Code:
		return -32000, "invalid sender"
	case ErrTxTypeNotSupported.Code:
		return -32000, "transaction type not supported"
	case ErrMaxInitCodeSizeExceeded.Code:
		return -32000, "max initcode size exceeded"
	case ErrGasLimitReached.Code:
		return -32000, "gas limit reached"
	case InvalidMaxGasPrice.Code:
		return -32000, "transaction underpriced"
	case InvalidChainId.Code:
		return -32000, "invalid chain id"
	case ErrPreEIP155.Code:
		return -32000, "pre-EIP-155 unprotected transactions are not allowed"
	case ErrMalleableSignature.Code:
		return -32000, "malleable signature: s exceeds curve order / 2 (EIP-2)"
	case ErrExceedsMaxEnvelopeSize.Code:
		return -32000, "transaction envelope exceeds maximum allowed size"
	case ErrDecodeRawEth.Code:
		return -32000, "failed to decode raw Ethereum transaction envelope"
	default:
		return -32000, te.Description
	}
}
