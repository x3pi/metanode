package parentchain

import (
	"errors"
	"sync/atomic"

	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// ErrClusterNotAuthorized is returned when a registerCluster transaction names a cluster key that the chain's
// genesis does not authorize.
var ErrClusterNotAuthorized = errors.New("parentchain: cluster registration not authorized by genesis")

// ClusterPolicy says which cluster keys may be added to the ChainRegistry. A registered cluster is a trust
// anchor: its certificate is what authorizes deposits (minting float), so registration must not be open to
// everyone on a real network.
//
// The policy comes from the genesis file, which is identical on every validator, so applying it inside
// transaction execution is deterministic. The zero value (nil policy) is the secure default: nothing may register.
type ClusterPolicy struct {
	// Open allows any key to register (devnet / tests only; never for a network holding real value).
	Open bool
	// Allowed lists the cluster keys that may register when Open is false.
	Allowed map[cm.PublicKey]struct{}
}

var clusterPolicy atomic.Pointer[ClusterPolicy]

// SetClusterPolicy installs the process-wide cluster policy (called once at startup from the genesis).
func SetClusterPolicy(p ClusterPolicy) {
	clusterPolicy.Store(&p)
}

func clusterRegistrationAllowed(key cm.PublicKey) bool {
	p := clusterPolicy.Load()
	if p == nil {
		return false
	}
	if p.Open {
		return true
	}
	_, ok := p.Allowed[key]
	return ok
}
