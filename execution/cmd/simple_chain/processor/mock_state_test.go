package processor

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
)

// ============================================================================
// Helper: newTestValidator creates a ValidatorState for testing with basic fields.
// Uses the real state.NewValidatorState and configures via setters.
// ============================================================================

func newTestValidator(addr common.Address, authorityKey string, stake *big.Int, jailed bool) state.ValidatorState {
	vs := state.NewValidatorState(addr)
	vs.SetAuthorityKey(authorityKey)
	vs.SetName("validator-" + addr.Hex()[:8])

	if jailed {
		vs.SetJailed(true, time.Now().Add(1*time.Hour))
	}

	if stake != nil && stake.Sign() > 0 {
		// SetDelegate adds stake for a delegator address
		vs.SetDelegate(addr, stake)
	}

	return vs
}

// newTestValidators creates a set of test validators with distinct authority keys.
// The authority keys are designed so that sorting alphabetically produces a
// deterministic ordering: "authkey_A" < "authkey_B" < "authkey_C".
func newTestValidators() []state.ValidatorState {
	addrA := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	addrB := common.HexToAddress("0xBBBB000000000000000000000000000000000002")
	addrC := common.HexToAddress("0xCCCC000000000000000000000000000000000003")

	stake := big.NewInt(1000000)

	return []state.ValidatorState{
		newTestValidator(addrA, "authkey_A", stake, false),
		newTestValidator(addrB, "authkey_B", stake, false),
		newTestValidator(addrC, "authkey_C", stake, false),
	}
}
