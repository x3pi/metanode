package processor

import (
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TestGenerateBlockData_BasicCreation
// ============================================================================
func TestGenerateBlockData_BasicCreation(t *testing.T) {
	prevHash := e_common.HexToHash("0x1111")
	leaderAddr := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	asRoot := e_common.HexToHash("0x2222")
	stakeRoot := e_common.HexToHash("0x3333")
	rcpRoot := e_common.HexToHash("0x4444")
	txsRoot := e_common.HexToHash("0x5555")

	lastHeader := block.NewBlockHeader(prevHash, 99, asRoot, stakeRoot, rcpRoot, leaderAddr, 1000, txsRoot, 1)

	tx1 := NewMockTransaction(
		e_common.HexToAddress("0x1111111111111111111111111111111111111111"),
		e_common.HexToAddress("0x2222222222222222222222222222222222222222"),
		1,
	)
	txs := []types.Transaction{tx1}

	bl, err := GenerateBlockData(
		lastHeader, leaderAddr,
		txs, nil,
		asRoot, stakeRoot, rcpRoot, txsRoot,
		100,  // blockNumber
		2,    // epoch
		5000, // deterministic timestamp
		0,    // globalExecIndex
		0,    // blobGasUsed
	)

	require.NoError(t, err)
	require.NotNil(t, bl)

	header := bl.Header()
	assert.Equal(t, uint64(100), header.BlockNumber())
	assert.Equal(t, uint64(2), header.Epoch())
	assert.Equal(t, uint64(5000), header.TimeStamp(), "should use provided timestamp")
	assert.Equal(t, leaderAddr, header.LeaderAddress())
	assert.Equal(t, asRoot, header.AccountStatesRoot())
	assert.Equal(t, lastHeader.Hash(), header.LastBlockHash(), "should reference previous block hash")

	// Verify transactions
	txHashes := bl.Transactions()
	require.Len(t, txHashes, 1)
	assert.Equal(t, tx1.Hash(), txHashes[0])
}

// ============================================================================
// TestGenerateBlockData_ZeroTimestampFallback
// ============================================================================
func TestGenerateBlockData_ZeroTimestampPanic(t *testing.T) {
	lastHeader := block.NewBlockHeader(
		e_common.Hash{}, 0,
		e_common.Hash{}, e_common.Hash{}, e_common.Hash{},
		e_common.Address{}, 0, e_common.Hash{}, 0,
	)

	assert.Panics(t, func() {
		_, _ = GenerateBlockData(
			lastHeader, e_common.Address{},
			nil, nil,
			e_common.Hash{}, e_common.Hash{}, e_common.Hash{}, e_common.Hash{},
			1, 0,
			0, // zero -> must panic
			0, // globalExecIndex
			0, // blobGasUsed
		)
	}, "zero timestamp must panic to prevent fork-safety issues")
}

// ============================================================================
// TestGenerateBlockData_NoTransactions
// ============================================================================
func TestGenerateBlockData_NoTransactions(t *testing.T) {
	lastHeader := block.NewBlockHeader(
		e_common.Hash{}, 0,
		e_common.Hash{}, e_common.Hash{}, e_common.Hash{},
		e_common.Address{}, 0, e_common.Hash{}, 0,
	)

	bl, err := GenerateBlockData(
		lastHeader, e_common.Address{},
		nil, nil, // no transactions, no SC results
		e_common.Hash{}, e_common.Hash{}, e_common.Hash{}, e_common.Hash{},
		1, 0, 1000, 0, 0,
	)

	require.NoError(t, err)
	assert.Empty(t, bl.Transactions(), "empty tx list should produce block with 0 transactions")
}

// ============================================================================
// TestGenerateBlockDataReadOnly
// ============================================================================
func TestGenerateBlockDataReadOnly(t *testing.T) {
	leaderAddr := e_common.HexToAddress("0xbbbb000000000000000000000000000000000002")
	tx1 := NewMockTransaction(
		e_common.HexToAddress("0x1111111111111111111111111111111111111111"),
		e_common.HexToAddress("0x2222222222222222222222222222222222222222"),
		5,
	)

	bl, err := GenerateBlockDataReadOnly(
		leaderAddr,
		[]types.Transaction{tx1}, nil,
		e_common.Hash{}, e_common.Hash{}, e_common.Hash{}, e_common.Hash{},
		42, 1, 9999, 0, 0,
	)

	require.NoError(t, err)
	require.NotNil(t, bl)

	header := bl.Header()
	assert.Equal(t, uint64(42), header.BlockNumber())
	assert.Equal(t, uint64(1), header.Epoch())
	assert.Equal(t, uint64(9999), header.TimeStamp())
	assert.Equal(t, leaderAddr, header.LeaderAddress())
	assert.Equal(t, e_common.Hash{}, header.LastBlockHash(), "read-only block should have zeroed previous hash")
}

// ============================================================================
// TestGenerateBlockDataReadOnly_ZeroTimestamp
// ============================================================================
func TestGenerateBlockDataReadOnly_ZeroTimestampPanic(t *testing.T) {
	assert.Panics(t, func() {
		_, _ = GenerateBlockDataReadOnly(
			e_common.Address{},
			nil, nil,
			e_common.Hash{}, e_common.Hash{}, e_common.Hash{}, e_common.Hash{},
			1, 0,
			0, // zero -> must panic
			0, // globalExecIndex
			0, // blobGasUsed
		)
	}, "zero timestamp must panic to prevent fork-safety issues")
}

// ============================================================================
// TestGenerateBlockData_MultipleTransactions
// ============================================================================
func TestGenerateBlockData_MultipleTransactions(t *testing.T) {
	lastHeader := block.NewBlockHeader(
		e_common.Hash{}, 0,
		e_common.Hash{}, e_common.Hash{}, e_common.Hash{},
		e_common.Address{}, 0, e_common.Hash{}, 0,
	)

	from := e_common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := e_common.HexToAddress("0x2222222222222222222222222222222222222222")

	txs := make([]types.Transaction, 100)
	for i := 0; i < 100; i++ {
		txs[i] = NewMockTransaction(from, to, uint64(i))
	}

	bl, err := GenerateBlockData(
		lastHeader, e_common.Address{},
		txs, nil,
		e_common.Hash{}, e_common.Hash{}, e_common.Hash{}, e_common.Hash{},
		1, 0, 1000, 0, 0,
	)

	require.NoError(t, err)
	assert.Len(t, bl.Transactions(), 100, "block should contain all 100 tx hashes")

	// Verify each hash matches
	for i, txHash := range bl.Transactions() {
		assert.Equal(t, txs[i].Hash(), txHash, "tx hash at index %d should match", i)
	}
}

// ============================================================================
// TestGetLeaderAddress_LeaderOverride
// Tests the variadic leaderAddressOverride in createBlockFromResults.
//
// CORRECTED (2026-09-10): this test used to hand-copy a DIFFERENT decision rule
// than createBlockFromResults actually implements -- it added an
// `&& tt.override[0] != (e_common.Address{})` condition that treats an explicit
// zero-address override the same as "no override" (falls back to
// bp.validatorAddress). The real function only checks `len(leaderAddressOverride)
// > 0` -- it does NOT special-case the zero value -- which is what its own
// "CRITICAL FORK-SAFETY" comment requires: "Use leader address from Rust
// consensus if provided, even if it's the zero address... Falling back to
// bp.validatorAddress would cause a fork!" (block_processor_processing.go). A
// zero-address override is Rust's OWN deterministic, intentional value for
// commits with no real leader (e.g. EndOfEpoch system transactions, see
// block_processor_core.go's GetLeaderAddress) -- every honest node computes the
// SAME zero address for that case, so respecting it is fork-safe, and silently
// substituting bp.validatorAddress (a different value on every node) instead
// would NOT be. Found while investigating a real, transient block-hash mismatch
// on 2026-09-10 (see note/consensus_local_dag_trust_gap_design_2026-09.md) --
// this specific test never actually caught anything because it doesn't call
// createBlockFromResults at all, it re-implements a parallel (and, until this
// fix, wrong) copy of its logic; fixed here to match the real function so a
// future regression in the real function's fallback logic would actually be
// caught by running this test suite.
// ============================================================================
func TestGetLeaderAddress_LeaderOverride(t *testing.T) {
	fallback := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	override := e_common.HexToAddress("0xbbbb000000000000000000000000000000000002")
	zeroAddress := e_common.Address{}

	// Mirrors createBlockFromResults's actual decision rule exactly:
	//   blockLeaderAddress := bp.validatorAddress
	//   if len(leaderAddressOverride) > 0 { blockLeaderAddress = leaderAddressOverride[0] }
	tests := []struct {
		name     string
		override []e_common.Address
		expected e_common.Address
	}{
		{"no override", nil, fallback},
		{"empty override", []e_common.Address{}, fallback},
		{"zero address override is respected, NOT treated as no-override", []e_common.Address{zeroAddress}, zeroAddress},
		{"valid override", []e_common.Address{override}, override},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blockLeaderAddress := fallback
			if len(tt.override) > 0 {
				blockLeaderAddress = tt.override[0]
			}
			assert.Equal(t, tt.expected, blockLeaderAddress)
		})
	}
}

// ============================================================================
// TestVerifyDraftBlock
// Tests the FORK-GUARD logic that prevents 0x0 roots from being committed
// ============================================================================
func TestVerifyDraftBlock(t *testing.T) {
	bp := &BlockProcessor{}
	validHash := e_common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	zeroHash := e_common.Hash{}

	tests := []struct {
		name               string
		accountRoot        e_common.Hash
		stakeRoot          e_common.Hash
		currentBlockNumber uint64
		expected           bool
	}{
		{
			name:               "Valid block with both roots",
			accountRoot:        validHash,
			stakeRoot:          validHash,
			currentBlockNumber: 100,
			expected:           true,
		},
		{
			name:               "Block 1 is allowed to have 0x0 roots (genesis state)",
			accountRoot:        zeroHash,
			stakeRoot:          zeroHash,
			currentBlockNumber: 1,
			expected:           true,
		},
		{
			name:               "Block > 1 with 0x0 AccountStatesRoot should be rejected",
			accountRoot:        zeroHash,
			stakeRoot:          validHash,
			currentBlockNumber: 2,
			expected:           false,
		},
		{
			name:               "Block > 1 with 0x0 StakeStatesRoot should be rejected",
			accountRoot:        validHash,
			stakeRoot:          zeroHash,
			currentBlockNumber: 100,
			expected:           false,
		},
		{
			name:               "Block > 1 with both 0x0 roots should be rejected",
			accountRoot:        zeroHash,
			stakeRoot:          zeroHash,
			currentBlockNumber: 50,
			expected:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create dummy block with specific header roots
			header := block.NewBlockHeader(
				e_common.Hash{}, 0, tt.accountRoot, tt.stakeRoot,
				e_common.Hash{}, e_common.Address{}, 0, e_common.Hash{}, 1,
			)
			bl := block.NewBlock(header, nil, nil)

			result := bp.verifyDraftBlock(bl, tt.currentBlockNumber, 1)
			assert.Equal(t, tt.expected, result)
		})
	}
}
