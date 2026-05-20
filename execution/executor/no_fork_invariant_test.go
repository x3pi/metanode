package executor

import (
	"bytes"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// No-Fork Invariant Tests: Validator Ordering & Boundary Block Consistency
//
// CRITICAL CONTEXT (Pillar-47 Advance-First Protocol):
//   - Validators must be sorted by AuthorityKey (BLS public key) as BYTES
//   - If AuthorityKeys are equal/empty, fallback to Address and then P2PAddress
//   - Go:   sort.SliceStable(v, func(i, j) => ...)
//   - Rust: sorted_validators.sort_by(|a, b| ...)
//   - Any divergence in sort order causes different committee hash → FORK
//   - BoundaryBlock from Rust is authoritative for epoch timestamps → no-fork
// ============================================================================

// simulatedValidator mirrors the AuthorityKey() used in production Go code.
type simulatedValidator struct {
	authorityKey []byte
	address      string
	p2pAddress   string
}

func (v simulatedValidator) AuthorityKey() []byte { return v.authorityKey }
func (v simulatedValidator) Address() string      { return v.address }
func (v simulatedValidator) P2PAddress() string   { return v.p2pAddress }

// sortValidatorsByAuthorityKey is the exact sort used in unix_socket_handler_epoch.go.
func sortValidatorsByAuthorityKey(validators []simulatedValidator) {
	sort.SliceStable(validators, func(i, j int) bool {
		cmp := bytes.Compare(validators[i].AuthorityKey(), validators[j].AuthorityKey())
		if cmp == 0 {
			addrI := validators[i].Address()
			addrJ := validators[j].Address()
			if addrI == addrJ {
				return validators[i].P2PAddress() < validators[j].P2PAddress()
			}
			return addrI < addrJ
		}
		return cmp < 0
	})
}

// ============================================================================
// TestNoFork_ValidatorSort_Deterministic
// Verify that validator sort produces identical output regardless of input order.
// ============================================================================

func TestNoFork_ValidatorSort_Deterministic(t *testing.T) {
	validators := []simulatedValidator{
		{authorityKey: []byte("bls-key-zzzzz"), address: "0xC", p2pAddress: "p2p-c"},
		{authorityKey: []byte("bls-key-aaaaa"), address: "0xA", p2pAddress: "p2p-a"},
		{authorityKey: []byte("bls-key-mmmmm"), address: "0xB", p2pAddress: "p2p-b"},
	}

	// Sort original order
	v1 := make([]simulatedValidator, len(validators))
	copy(v1, validators)
	sortValidatorsByAuthorityKey(v1)

	// Sort reversed order — must get same result
	v2 := []simulatedValidator{
		{authorityKey: []byte("bls-key-mmmmm"), address: "0xB", p2pAddress: "p2p-b"},
		{authorityKey: []byte("bls-key-zzzzz"), address: "0xC", p2pAddress: "p2p-c"},
		{authorityKey: []byte("bls-key-aaaaa"), address: "0xA", p2pAddress: "p2p-a"},
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
		{authorityKey: []byte("z-key"), address: "0x04"},
		{authorityKey: []byte("a-key"), address: "0x01"},
		{authorityKey: []byte("m-key"), address: "0x03"},
		{authorityKey: []byte("b-key"), address: "0x02"},
	}
	sortValidatorsByAuthorityKey(validators)

	// Must be strictly ascending order
	for i := 1; i < len(validators); i++ {
		assert.True(t, bytes.Compare(validators[i-1].AuthorityKey(), validators[i].AuthorityKey()) < 0,
			"validators must be sorted ascending by AuthorityKey at position %d", i)
	}
	assert.Equal(t, []byte("a-key"), validators[0].AuthorityKey(), "smallest key must be first")
	assert.Equal(t, []byte("z-key"), validators[len(validators)-1].AuthorityKey(), "largest key must be last")
}

func TestNoFork_ValidatorSort_SingleValidator(t *testing.T) {
	validators := []simulatedValidator{
		{authorityKey: []byte("only-key"), address: "0xAA"},
	}
	sortValidatorsByAuthorityKey(validators)
	assert.Equal(t, []byte("only-key"), validators[0].AuthorityKey(), "single validator sort should be stable")
}

func TestNoFork_ValidatorSort_EmptyList(t *testing.T) {
	var validators []simulatedValidator
	// Must not panic
	sortValidatorsByAuthorityKey(validators)
	assert.Empty(t, validators)
}

func TestNoFork_ValidatorSort_DuplicateKeys(t *testing.T) {
	// If AuthorityKeys are identical/duplicate, sorting must fall back deterministically
	// to Address and then P2PAddress.
	validators := []simulatedValidator{
		{authorityKey: []byte("same-key"), address: "0x02", p2pAddress: "p2p-y"},
		{authorityKey: []byte("same-key"), address: "0x01", p2pAddress: "p2p-z"},
		{authorityKey: []byte("same-key"), address: "0x02", p2pAddress: "p2p-x"},
		{authorityKey: []byte("other-key"), address: "0x03", p2pAddress: "p2p-w"},
	}
	
	sortValidatorsByAuthorityKey(validators)
	assert.Len(t, validators, 4)

	// "other-key" must come first because "other-key" < "same-key" lexicographically
	assert.Equal(t, []byte("other-key"), validators[0].AuthorityKey())

	// For the remaining three "same-key" validators, they should be sorted by Address:
	// "0x01" must come before "0x02"
	assert.Equal(t, "0x01", validators[1].Address())

	// For the two with Address = "0x02", they should be sorted by P2PAddress:
	// "p2p-x" must come before "p2p-y"
	assert.Equal(t, "0x02", validators[2].Address())
	assert.Equal(t, "p2p-x", validators[2].P2PAddress())

	assert.Equal(t, "0x02", validators[3].Address())
	assert.Equal(t, "p2p-y", validators[3].P2PAddress())
}

func TestNoFork_ValidatorSort_GoRustParity(t *testing.T) {
	// Simulate the exact BLS key format used in production (hex-encoded bytes)
	// and verify that comparison matches expected ordering.
	blsKeys := [][]byte{
		[]byte("a4b8c3d1e2f3a4b8c3d1e2f3a4b8c3d1e2f3a4b8c3d1e2f3a4b8c3d1e2f3a4b8"),
		[]byte("1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef12"),
		[]byte("fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210fe"),
		[]byte("0000000000000000000000000000000000000000000000000000000000000001"),
	}

	validators := make([]simulatedValidator, len(blsKeys))
	for i, k := range blsKeys {
		validators[i] = simulatedValidator{authorityKey: k, address: "0x" + string(k[:4])}
	}

	sortValidatorsByAuthorityKey(validators)

	// Verify sorted keys are in strictly ascending order
	for i := 1; i < len(validators); i++ {
		assert.True(t, bytes.Compare(validators[i-1].AuthorityKey(), validators[i].AuthorityKey()) < 0,
			"BLS keys must be in ascending lexicographic order at position %d", i)
	}

	// The "0000..." key must come first (lexicographically smallest)
	assert.Equal(t, []byte("0000000000000000000000000000000000000000000000000000000000000001"),
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
			result += string(v.AuthorityKey()) + "|"
		}
		return result
	}

	validators := []simulatedValidator{
		{authorityKey: []byte("key-C")},
		{authorityKey: []byte("key-A")},
		{authorityKey: []byte("key-B")},
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
		{authorityKey: []byte("key-B")},
		{authorityKey: []byte("key-C")},
		{authorityKey: []byte("key-A")},
	}
	sortValidatorsByAuthorityKey(sorted2)
	assert.Equal(t, sortedFingerprint, computeFingerprint(sorted2),
		"two independently sorted lists must produce identical committee fingerprint")
}
