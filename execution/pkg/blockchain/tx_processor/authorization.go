package tx_processor

import (
	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// processAuthorizationList applies every EIP-7702 authorization tuple carried
// by tx to state: verify (chain ID, nonce, signature, existing-code shape)
// then write the delegation designator (0xef0100 || address) to the
// authority's code, or clear it back to "no code" when the tuple delegates to
// the zero address (spec-defined revocation). Mirrors go-ethereum's
// stateTransition.applyAuthorization (core/state_transition.go) tuple for
// tuple, including its most load-bearing rule: an invalid tuple is skipped,
// never fails the whole transaction (see TransitionDb's "errors are ignored,
// we simply skip invalid authorizations here").
//
// accountStateDB must be the tx-scoped view (MVCC-wrapped in TrueBlockSTM) so
// nonce/codeHash writes participate in conflict detection and roll back with
// the rest of the tx on abort. smartContractDB must be the real, global
// SmartContractDB (not MVCC-wrapped): code storage is content-addressed by
// codeHash (see SmartContractDB.SetCode), so writing the same fixed 23-byte
// designator early is always safe/idempotent and needs no conflict tracking —
// this also makes it visible to MVCCSmartContractDB.Code's same-tx delegated
// self-call path (see its comment).
//
// Returns the total intrinsic gas the caller must charge (at the tx's own
// EffectiveGasPrice, alongside its regular execution gas fee) for the
// authorizations actually applied. Deliberately charges the flat
// params.CallNewAccountGas per applied tuple rather than replicating
// go-ethereum's refund-if-authority-already-exists optimization
// (params.TxAuthTupleGas) — this chain has no unified gas-refund counter, and
// a flat charge only ever over-collects, never under-collects, so it cannot
// under-price network resource usage.
func processAuthorizationList(
	tx types.Transaction,
	currentChainID uint64,
	accountStateDB types.AccountStateDB,
	smartContractDB types.SmartContractDB,
) uint64 {
	authList := tx.AuthorizationList()
	if len(authList) == 0 {
		return 0
	}
	ethAuthList := transaction.ToEthAuthorizationList(authList)

	var gasUsed uint64
	for i := range ethAuthList {
		auth := &ethAuthList[i]

		// Chain ID must be unset (wildcard) or match this chain exactly.
		if !auth.ChainID.IsZero() && auth.ChainID.Uint64() != currentChainID {
			continue
		}
		// EIP-2681 nonce overflow guard.
		if auth.Nonce+1 < auth.Nonce {
			continue
		}
		authority, err := auth.Authority()
		if err != nil {
			continue
		}
		authorityState, err := accountStateDB.AccountState(authority)
		if err != nil || authorityState == nil {
			continue
		}
		// Authority must have no code, or already be delegating — re-pointing
		// an existing delegation is allowed, overwriting real contract code
		// is not.
		if scState := authorityState.SmartContractState(); scState != nil {
			if existingCode := smartContractDB.Code(authority); len(existingCode) != 0 {
				if _, isDelegation := e_types.ParseDelegation(existingCode); !isDelegation {
					continue
				}
			}
		}
		if authorityState.Nonce() != auth.Nonce {
			continue
		}

		gasUsed += params.CallNewAccountGas

		if err := accountStateDB.SetNonce(authority, auth.Nonce+1); err != nil {
			continue
		}

		if auth.Address == (common.Address{}) {
			// Delegation to the zero address means "clear" (EIP-7702 revocation).
			accountStateDB.SetCodeHash(authority, common.Hash{})
			continue
		}

		designator := e_types.AddressToDelegation(auth.Address)
		codeHash := crypto.Keccak256Hash(designator)
		accountStateDB.SetCodeHash(authority, codeHash)
		smartContractDB.SetCode(authority, codeHash, designator)
	}
	return gasUsed
}
