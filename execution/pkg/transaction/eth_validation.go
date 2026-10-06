package transaction

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
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
