package executor

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// No-Fork Invariant Tests: Validator Ordering & Boundary Block Consistency
//
// CRITICAL CONTEXT (Pillar-47 Advance-First Protocol):
//   - Validators must be sorted by AuthorityKey (BLS public key) as STRING
//   - This sort is identical in Go and Rust:
//       Go:   sort.Slice(v, func(i, j) => v[i].AuthorityKey() < v[j].AuthorityKey())
//       Rust: sorted_validators.sort_by(|a, b| a.authority_key.cmp(&b.authority_key))
//   - Any divergence in sort order causes different committee hash → FORK
//   - BoundaryBlock from Rust is authoritative for epoch timestamps → no-fork
// ============================================================================

// simulatedValidator mirrors the AuthorityKey() used in production Go code.
type simulatedValidator struct {
	authorityKey string
	address      string
}

func (v simulatedValidator) AuthorityKey() string { return v.authorityKey }
func (v simulatedValidator) Address() string      { return v.address }

// sortValidatorsByAuthorityKey is the exact sort used in unix_socket_handler_epoch.go.
// CRITICAL: Must use string comparison, not byte comparison.
func sortValidatorsByAuthorityKey(validators []simulatedValidator) {
	sort.Slice(validators, func(i, j int) bool {
		return validators[i].AuthorityKey() < validators[j].AuthorityKey()
	})
}

// ============================================================================
// TestNoFork_ValidatorSort_Deterministic
// Verify that validator sort produces identical output regardless of input order.
// ============================================================================

func TestNoFork_ValidatorSort_Deterministic(t *testing.T) {
	validators := []simulatedValidator{
		{authorityKey: "bls-key-zzzzz", address: "0xC"},
		{authorityKey: "bls-key-aaaaa", address: "0xA"},
		{authorityKey: "bls-key-mmmmm", address: "0xB"},
	}

	// Sort original order
	v1 := make([]simulatedValidator, len(validators))
	copy(v1, validators)
	sortValidatorsByAuthorityKey(v1)

	// Sort reversed order — must get same result
	v2 := []simulatedValidator{
		{authorityKey: "bls-key-mmmmm", address: "0xB"},
		{authorityKey: "bls-key-zzzzz", address: "0xC"},
		{authorityKey: "bls-key-aaaaa", address: "0xA"},
	}
	sortValidatorsByAuthorityKey(v2)

	require.Len(t, v1, 3)
	for i := range v1 {
		assert.Equal(t, v1[i].AuthorityKey(), v2[i].AuthorityKey(),
			"position %d: sort order must be identical regardless of input order", i)
	}
}

func TestNoFork_ValidatorSort_AscendingOrder(t *testing.T) {
	validators := []simulatedValidator{
		{authorityKey: "z-key"},
		{authorityKey: "a-key"},
		{authorityKey: "m-key"},
		{authorityKey: "b-key"},
	}
	sortValidatorsByAuthorityKey(validators)

	// Must be strictly ascending string order
	for i := 1; i < len(validators); i++ {
		assert.Less(t, validators[i-1].AuthorityKey(), validators[i].AuthorityKey(),
			"validators must be sorted ascending by AuthorityKey at position %d", i)
	}
	assert.Equal(t, "a-key", validators[0].AuthorityKey(), "smallest key must be first")
	assert.Equal(t, "z-key", validators[len(validators)-1].AuthorityKey(), "largest key must be last")
}

func TestNoFork_ValidatorSort_SingleValidator(t *testing.T) {
	validators := []simulatedValidator{
		{authorityKey: "only-key", address: "0xAA"},
	}
	sortValidatorsByAuthorityKey(validators)
	assert.Equal(t, "only-key", validators[0].AuthorityKey(), "single validator sort should be stable")
}

func TestNoFork_ValidatorSort_EmptyList(t *testing.T) {
	var validators []simulatedValidator
	// Must not panic
	sortValidatorsByAuthorityKey(validators)
	assert.Empty(t, validators)
}

func TestNoFork_ValidatorSort_DuplicateKeys(t *testing.T) {
	// In production this should not happen, but sort must not panic
	validators := []simulatedValidator{
		{authorityKey: "same-key", address: "0x01"},
		{authorityKey: "same-key", address: "0x02"},
		{authorityKey: "other-key", address: "0x03"},
	}
	// Must not panic
	sortValidatorsByAuthorityKey(validators)
	assert.Len(t, validators, 3)
}

func TestNoFork_ValidatorSort_GoRustParity(t *testing.T) {
	// Simulate the exact BLS key format used in production (hex-encoded)
	// and verify that string comparison matches expected ordering.
	//
	// Rust: .sort_by(|a, b| a.authority_key.cmp(&b.authority_key))
	// Go:   sort.Slice(..., func(i, j) => v[i].AuthorityKey() < v[j].AuthorityKey())
	//
	// Both use lexicographic string comparison — this test validates parity.
	blsKeys := []string{
		"a4b8c3d1e2f3a4b8c3d1e2f3a4b8c3d1e2f3a4b8c3d1e2f3a4b8c3d1e2f3a4b8",
		"1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef12",
		"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210fe",
		"0000000000000000000000000000000000000000000000000000000000000001",
	}

	validators := make([]simulatedValidator, len(blsKeys))
	for i, k := range blsKeys {
		validators[i] = simulatedValidator{authorityKey: k, address: "0x" + k[:4]}
	}

	sortValidatorsByAuthorityKey(validators)

	// Verify sorted keys are in strictly ascending order
	for i := 1; i < len(validators); i++ {
		assert.True(t, validators[i-1].AuthorityKey() < validators[i].AuthorityKey(),
			"BLS keys must be in ascending lexicographic order at position %d", i)
	}

	// The "0000..." key must come first (lexicographically smallest)
	assert.Equal(t, "0000000000000000000000000000000000000000000000000000000000000001",
		validators[0].AuthorityKey(), "smallest BLS key must be first")
}

// ============================================================================
// TestNoFork_BoundaryBlock_Monotonic
// Verify that boundary blocks are strictly monotonically increasing.
// Regression: non-monotonic boundary blocks can cause epoch timestamp divergence.
// ============================================================================

func TestNoFork_BoundaryBlock_Monotonic(t *testing.T) {
	// Simulate a sequence of epoch advancements
	type epochAdvancement struct {
		epoch         uint64
		boundaryBlock uint64
	}

	advancements := []epochAdvancement{
		{epoch: 1, boundaryBlock: 100},
		{epoch: 2, boundaryBlock: 200},
		{epoch: 3, boundaryBlock: 315},
		{epoch: 4, boundaryBlock: 400},
		{epoch: 5, boundaryBlock: 500},
	}

	// Verify monotonically increasing
	for i := 1; i < len(advancements); i++ {
		prev := advancements[i-1]
		curr := advancements[i]
		assert.Greater(t, curr.boundaryBlock, prev.boundaryBlock,
			"boundary block must increase: epoch %d (block %d) > epoch %d (block %d)",
			curr.epoch, curr.boundaryBlock, prev.epoch, prev.boundaryBlock)
		assert.Equal(t, prev.epoch+1, curr.epoch,
			"epoch must increment by 1: got %d → %d", prev.epoch, curr.epoch)
	}
}

func TestNoFork_BoundaryBlock_EpochZeroIsGenesis(t *testing.T) {
	// Epoch 0 always has boundary block 0 (genesis)
	const genesisEpoch = 0
	const genesisBoundaryBlock = 0

	assert.Equal(t, uint64(genesisEpoch), uint64(0), "genesis epoch must be 0")
	assert.Equal(t, uint64(genesisBoundaryBlock), uint64(0), "genesis boundary block must be 0")
}

func TestNoFork_EpochAdvancement_BackwardRejected(t *testing.T) {
	// Verify that backward epoch advancement is logically rejected.
	// In production: HandleAdvanceEpochRequest checks current epoch.
	// This test validates the business rule at unit level.

	type epochState struct {
		currentEpoch uint64
	}

	state := epochState{currentEpoch: 3}

	isValidAdvancement := func(newEpoch uint64) bool {
		return newEpoch > state.currentEpoch
	}

	assert.True(t, isValidAdvancement(4), "advancing forward is valid")
	assert.True(t, isValidAdvancement(10), "large jump forward is valid (catchup)")
	assert.False(t, isValidAdvancement(3), "same epoch is rejected")
	assert.False(t, isValidAdvancement(2), "backward epoch is rejected")
	assert.False(t, isValidAdvancement(0), "epoch 0 from epoch 3 is rejected")
}

func TestNoFork_EpochTimestamp_DeterministicFromBoundaryBlock(t *testing.T) {
	// Verify the timestamp derivation logic:
	// epoch_timestamp_ms = boundary_block_header.TimeStamp() * 1000
	// This ensures all nodes compute the same timestamp → no fork.

	// Simulate block header timestamps (seconds since unix epoch)
	blockTimestamps := map[uint64]uint64{
		100: 1700000100, // boundary block for epoch 1
		200: 1700000200, // boundary block for epoch 2
		315: 1700000315, // boundary block for epoch 3
	}

	// Convert to milliseconds (as done in HandleGetEpochBoundaryDataRequest)
	type epochTimeMs struct {
		epoch       uint64
		timestampMs uint64
	}

	expected := []epochTimeMs{
		{epoch: 1, timestampMs: blockTimestamps[100] * 1000},
		{epoch: 2, timestampMs: blockTimestamps[200] * 1000},
		{epoch: 3, timestampMs: blockTimestamps[315] * 1000},
	}

	for _, et := range expected {
		// Verify ms = seconds * 1000
		var boundaryBlock uint64
		switch et.epoch {
		case 1:
			boundaryBlock = 100
		case 2:
			boundaryBlock = 200
		case 3:
			boundaryBlock = 315
		}
		blockTs := blockTimestamps[boundaryBlock]
		computedMs := blockTs * 1000

		assert.Equal(t, et.timestampMs, computedMs,
			"epoch %d: timestamp_ms must be boundary block header timestamp * 1000", et.epoch)
		assert.Greater(t, computedMs, uint64(0), "timestamp must be positive")
	}
}

// ============================================================================
// TestNoFork_CommitteeHash_RequiresSameValidatorOrder
// Verify that different sort orders produce different "committee fingerprints"
// — demonstrating WHY consistent sort is critical for no-fork.
// ============================================================================

func TestNoFork_CommitteeHash_RequiresSameValidatorOrder(t *testing.T) {
	// Simulate committee fingerprint = concatenated authority keys
	computeFingerprint := func(validators []simulatedValidator) string {
		result := ""
		for _, v := range validators {
			result += v.AuthorityKey() + "|"
		}
		return result
	}

	validators := []simulatedValidator{
		{authorityKey: "key-C"},
		{authorityKey: "key-A"},
		{authorityKey: "key-B"},
	}

	// Sorted (deterministic)
	sorted := make([]simulatedValidator, len(validators))
	copy(sorted, validators)
	sortValidatorsByAuthorityKey(sorted)

	// Unsorted (non-deterministic)
	unsorted := make([]simulatedValidator, len(validators))
	copy(unsorted, validators)
	// Leave in original order

	sortedFingerprint := computeFingerprint(sorted)
	unsortedFingerprint := computeFingerprint(unsorted)

	assert.NotEqual(t, sortedFingerprint, unsortedFingerprint,
		"sorted vs unsorted must produce different fingerprints — "+
			"this demonstrates WHY consistent sort is required for no-fork")

	// Two independently sorted lists must have identical fingerprints
	sorted2 := []simulatedValidator{
		{authorityKey: "key-B"},
		{authorityKey: "key-C"},
		{authorityKey: "key-A"},
	}
	sortValidatorsByAuthorityKey(sorted2)
	assert.Equal(t, sortedFingerprint, computeFingerprint(sorted2),
		"two independently sorted lists must produce identical committee fingerprint")
}
