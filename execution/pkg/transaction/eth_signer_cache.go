package transaction

import (
	"math/big"
	"sync/atomic"

	e_types "github.com/ethereum/go-ethereum/core/types"
)

type cachedSignerEntry struct {
	chainID uint64
	signer  e_types.Signer
}

var (
	cachedLondon atomic.Pointer[cachedSignerEntry]
	cachedCancun atomic.Pointer[cachedSignerEntry]
	cachedPrague atomic.Pointer[cachedSignerEntry]
	cachedEIP155 atomic.Pointer[cachedSignerEntry]
)

// CachedLondonSigner returns a cached immutable London signer for uint64 chain IDs,
// avoiding repeated heap allocations and big.Int cloning in transaction hot paths.
func CachedLondonSigner(chainID *big.Int) e_types.Signer {
	if chainID != nil && chainID.IsUint64() && chainID.Sign() > 0 {
		u := chainID.Uint64()
		if cur := cachedLondon.Load(); cur != nil && cur.chainID == u {
			return cur.signer
		}
		s := e_types.NewLondonSigner(chainID)
		cachedLondon.Store(&cachedSignerEntry{chainID: u, signer: s})
		return s
	}
	return e_types.NewLondonSigner(chainID)
}

// CachedCancunSigner returns a cached immutable Cancun signer for uint64 chain IDs.
func CachedCancunSigner(chainID *big.Int) e_types.Signer {
	if chainID != nil && chainID.IsUint64() && chainID.Sign() > 0 {
		u := chainID.Uint64()
		if cur := cachedCancun.Load(); cur != nil && cur.chainID == u {
			return cur.signer
		}
		s := e_types.NewCancunSigner(chainID)
		cachedCancun.Store(&cachedSignerEntry{chainID: u, signer: s})
		return s
	}
	return e_types.NewCancunSigner(chainID)
}

// CachedPragueSigner returns a cached immutable Prague signer for uint64 chain IDs.
func CachedPragueSigner(chainID *big.Int) e_types.Signer {
	if chainID != nil && chainID.IsUint64() && chainID.Sign() > 0 {
		u := chainID.Uint64()
		if cur := cachedPrague.Load(); cur != nil && cur.chainID == u {
			return cur.signer
		}
		s := e_types.NewPragueSigner(chainID)
		cachedPrague.Store(&cachedSignerEntry{chainID: u, signer: s})
		return s
	}
	return e_types.NewPragueSigner(chainID)
}

// CachedEIP155Signer returns a cached immutable EIP-155 signer for uint64 chain IDs.
func CachedEIP155Signer(chainID *big.Int) e_types.Signer {
	if chainID != nil && chainID.IsUint64() && chainID.Sign() > 0 {
		u := chainID.Uint64()
		if cur := cachedEIP155.Load(); cur != nil && cur.chainID == u {
			return cur.signer
		}
		s := e_types.NewEIP155Signer(chainID)
		cachedEIP155.Store(&cachedSignerEntry{chainID: u, signer: s})
		return s
	}
	return e_types.NewEIP155Signer(chainID)
}
