package grouptxns

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
)



type mockTx struct {
	types.Transaction // embed interface
	hash              common.Hash
	from              common.Address
	nonce             uint64
}

func (m *mockTx) Hash() common.Hash {
	return m.hash
}
func (m *mockTx) FromAddress() common.Address {
	return m.from
}
func (m *mockTx) GetNonce() uint64 {
	return m.nonce
}
func (m *mockTx) MaxGas() uint64 {
	return 0
}

// ToAddress and IsRegularTransaction are called by classifyGroup() (via
// GroupTransactionsDeterministic) even though this test only cares about
// grouping/ordering by FromAddress+Nonce. types.Transaction is embedded as a
// nil interface, so without these overrides classifyGroup panics with a nil
// pointer dereference instead of exercising the code path under test.
func (m *mockTx) ToAddress() common.Address {
	return common.Address{}
}
func (m *mockTx) IsRegularTransaction() bool {
	return true
}
func (m *mockTx) GetType() uint64 {
	return 0
}

// ============================================================================
// TestGroupTransactionsDeterministic
// Tests the FORK-SAFE grouping logic
// ============================================================================
func TestGroupTransactionsDeterministic(t *testing.T) {
	from1 := common.HexToAddress("0x11")
	from2 := common.HexToAddress("0x22")

	txA := &mockTx{hash: common.HexToHash("0xa"), from: from1, nonce: 2}
	txB := &mockTx{hash: common.HexToHash("0xb"), from: from1, nonce: 1}
	txC := &mockTx{hash: common.HexToHash("0xc"), from: from2, nonce: 1}

	items := []Item{
		// Group 1: shares address 0x55
		{ID: 1, Array: []common.Address{common.HexToAddress("0x55")}, Tx: txA},
		{ID: 2, Array: []common.Address{common.HexToAddress("0x55")}, Tx: txB},
		// Group 2: isolated address 0x99
		{ID: 3, Array: []common.Address{common.HexToAddress("0x99")}, Tx: txC},
	}

	groups := GroupTransactionsDeterministic(items, nil)

	// We expect 2 groups.
	assert.Equal(t, 2, len(groups))

	// Group order is deterministic: sorted by smallest TX hash in the group.
	// Group 1 min hash = 0xa
	// Group 2 min hash = 0xc
	// So Group 1 should be groups[0]
	assert.Equal(t, 2, len(groups[0].Items))
	assert.Equal(t, 1, len(groups[1].Items))

	// Within Group 1, items are sorted by FromAddress then Nonce ascending.
	// txB has nonce 1, txA has nonce 2. So txB should be first!
	assert.Equal(t, txB.hash, groups[0].Items[0].Tx.Hash())
	assert.Equal(t, txA.hash, groups[0].Items[1].Tx.Hash())

	// Verify Group IDs
	assert.Equal(t, 0, groups[0].GroupID)
	assert.Equal(t, 1, groups[1].GroupID)
}

// mockValueTransferTx is a plain value-transfer tx (Amount > 0, no calldata),
// the shape that would otherwise take the native fast-path.
type mockValueTransferTx struct {
	mockTx
	to     common.Address
	amount *big.Int
}

func (m *mockValueTransferTx) ToAddress() common.Address {
	return m.to
}

func (m *mockValueTransferTx) Amount() *big.Int {
	return m.amount
}

// ============================================================================
// TestClassifyGroup_HasCodeFunc
// A plain value-transfer to an address hasCode reports as code-bearing must
// be classified as EVM (ContractOnly), not routed to the native fast-path —
// see HasCodeFunc's doc comment for the mainnet-parity rationale.
// ============================================================================
func TestClassifyGroup_HasCodeFunc(t *testing.T) {
	from := common.HexToAddress("0x11")
	toWithCode := common.HexToAddress("0xc0de")
	toPlain := common.HexToAddress("0x22")

	txToContract := &mockValueTransferTx{
		mockTx: mockTx{hash: common.HexToHash("0xa"), from: from, nonce: 1},
		to:     toWithCode,
		amount: big.NewInt(1),
	}
	txToPlainEOA := &mockValueTransferTx{
		mockTx: mockTx{hash: common.HexToHash("0xb"), from: from, nonce: 2},
		to:     toPlain,
		amount: big.NewInt(1),
	}

	hasCode := func(addr common.Address) bool {
		return addr == toWithCode
	}

	t.Run("routes to EVM when recipient has code", func(t *testing.T) {
		kind := classifyGroup([]Item{{ID: 0, Tx: txToContract}}, hasCode)
		assert.Equal(t, GroupKindContractOnly, kind)
	})

	t.Run("stays native when recipient has no code", func(t *testing.T) {
		kind := classifyGroup([]Item{{ID: 0, Tx: txToPlainEOA}}, hasCode)
		assert.Equal(t, GroupKindNativeOnly, kind)
	})

	t.Run("nil hasCode preserves old behavior (never routes on code presence)", func(t *testing.T) {
		kind := classifyGroup([]Item{{ID: 0, Tx: txToContract}}, nil)
		assert.Equal(t, GroupKindNativeOnly, kind)
	})

	t.Run("mixed group with one code-bearing recipient is Mixed", func(t *testing.T) {
		kind := classifyGroup([]Item{
			{ID: 0, Tx: txToContract},
			{ID: 1, Tx: txToPlainEOA},
		}, hasCode)
		assert.Equal(t, GroupKindMixed, kind)
	})
}
