package parentchain

import (
	"errors"
	"math/big"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/crypto"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// ErrClusterNotAuthorized is returned when a registerCluster transaction names a cluster key that the chain's
// genesis does not authorize.
var ErrClusterNotAuthorized = errors.New("parentchain: cluster registration not authorized by genesis")

// ErrDepositDisabled is returned for depositToFloat when the genesis keeps the chain at a fixed float supply.
var ErrDepositDisabled = errors.New("parentchain: depositToFloat is disabled by genesis (fixed float supply)")

// ClusterPolicy says which cluster keys may be added to the ChainRegistry and whether float can be minted at all.
// A registered cluster is a trust anchor (its certificate authorizes deposits and state roots), so registration must
// not be open to everyone on a real network.
//
// The policy comes from the genesis file, which is identical on every validator, so applying it inside
// transaction execution is deterministic. The zero value (nil policy) is the secure default: nothing may register
// and nothing may be minted.
type ClusterPolicy struct {
	// Open allows any key to register (devnet / tests only; never for a network holding real value).
	Open bool
	// Allowed lists the cluster keys that may register when Open is false (the founding set).
	Allowed map[cm.PublicKey]struct{}
	// MinFloat, when > 0, additionally lets any key register whose float balance is at least this many base units.
	// With a fixed supply that balance is scarce, so it is a real entry barrier.
	MinFloat *big.Int
	// AllowDeposit enables depositToFloat, the only path that increases the total float supply (devnet / tests).
	// Production keeps it false: the supply is exactly what the genesis allocated.
	AllowDeposit bool
}

var clusterPolicy atomic.Pointer[ClusterPolicy]

// SetClusterPolicy installs the process-wide cluster policy (called once at startup from the genesis).
func SetClusterPolicy(p ClusterPolicy) {
	clusterPolicy.Store(&p)
}

// clusterRegistrationAllowed reports whether key may register as a cluster, reading its float balance from store
// when the balance rule applies (deterministic: it only reads chain state).
func clusterRegistrationAllowed(store Store, key cm.PublicKey) (bool, error) {
	p := clusterPolicy.Load()
	if p == nil {
		return false, nil
	}
	if p.Open {
		return true, nil
	}
	if _, ok := p.Allowed[key]; ok {
		return true, nil
	}
	if p.MinFloat != nil && p.MinFloat.Sign() > 0 {
		bal, err := store.GetFloat(crypto.Keccak256Hash(key[:]))
		if err != nil {
			return false, err
		}
		return bal.Cmp(p.MinFloat) >= 0, nil
	}
	return false, nil
}

func depositToFloatAllowed() bool {
	p := clusterPolicy.Load()
	return p != nil && p.AllowDeposit
}
