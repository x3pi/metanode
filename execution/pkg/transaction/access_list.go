package transaction

import (
	"math/big"

	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
	"github.com/meta-node-blockchain/meta-node/types"
)

// EffectiveAccessList returns tx.EthAccessList() with any EIP-7702 authorities added to warm them,
// matching go-ethereum's core/state_transition.go:578-592 (validateAuthorization):
// st.state.AddAddressToAccessList(authority) is only executed when:
// 1. auth.ChainID is zero (spec wildcard) or matches the current chainID
// 2. auth.Nonce+1 >= auth.Nonce (EIP-2681 nonce overflow guard)
// 3. auth.Authority() succeeds without error
func EffectiveAccessList(tx types.Transaction, chainID *big.Int) e_types.AccessList {
	if tx == nil {
		return nil
	}
	al := tx.EthAccessList()
	authList := tx.EthAuthorizationList()
	if len(authList) == 0 {
		return al
	}
	var uChainID *uint256.Int
	if chainID != nil {
		var overflow bool
		uChainID, overflow = uint256.FromBig(chainID)
		if overflow {
			uChainID = nil
		}
	}
	for i := range authList {
		auth := &authList[i]
		// Chain ID must be zero (wildcard) or match this chain exactly.
		if !auth.ChainID.IsZero() && (uChainID == nil || auth.ChainID.Cmp(uChainID) != 0) {
			continue
		}
		// EIP-2681 nonce overflow guard.
		if auth.Nonce+1 < auth.Nonce {
			continue
		}
		if authority, err := auth.Authority(); err == nil {
			al = append(al, e_types.AccessTuple{Address: authority})
		}
	}
	return al
}

// EffectiveGasPriceUint64 returns the execution gas price paid by the sender as uint64,
// dispatching per tx type (flat MaxGasPrice for Legacy/EIP-2930/Type 0xFF,
// min(GasFeeCap, MINIMUM_BASE_FEE + GasTipCap) for EIP-1559/EIP-4844/EIP-7702).
func EffectiveGasPriceUint64(tx types.Transaction) uint64 {
	if tx == nil {
		return 0
	}
	if eff := tx.EffectiveGasPrice(); eff != nil {
		// Saturate instead of wrapping: the MVM takes a uint64, so an out-of-range price must not truncate.
		if !eff.IsUint64() {
			return ^uint64(0)
		}
		return eff.Uint64()
	}
	return tx.MaxGasPrice()
}
