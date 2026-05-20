package grouptxns

import (
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common" // Assuming this is where types.Transaction and types.Receipt are defined
	"github.com/stretchr/testify/assert"
)

func TestGroupItems(t *testing.T) {
	// Tạo các giao dịch mẫu
	items := []Item{
		{ID: 1, Array: []common.Address{common.HexToAddress("0x123"), common.HexToAddress("0x456")}, Tx: nil},
		{ID: 2, Array: []common.Address{common.HexToAddress("0x456"), common.HexToAddress("0x789")}, Tx: nil},
		{ID: 3, Array: []common.Address{common.HexToAddress("0xabc")}, Tx: nil},
		{ID: 4, Array: []common.Address{common.HexToAddress("0xdef"), common.HexToAddress("0x789")}, Tx: nil},
	}

	// Nhóm các giao dịch
	groupedItems, err := GroupItems(items)
	assert.NoError(t, err)

	// Kiểm tra kết quả
	assert.Equal(t, 2, len(GroupByGroupID(groupedItems))) // Expecting 2 groups
	// Thêm các assertion để kiểm tra GroupID của từng item nếu cần thiết
}

func TestGroupItems2(t *testing.T) {
	// Tạo các giao dịch mẫu
	items := []Item{
		{ID: 1, Array: []common.Address{}, Tx: nil},
		{ID: 2, Array: []common.Address{}, Tx: nil},
		{ID: 3, Array: []common.Address{common.HexToAddress("0xabc")}, Tx: nil},
		{ID: 4, Array: []common.Address{common.HexToAddress("0xdef"), common.HexToAddress("0x789")}, Tx: nil},
	}

	// Nhóm các giao dịch
	groupedItems, err := GroupItems(items)
	assert.NoError(t, err)
	// Kiểm tra kết quả
	assert.Equal(t, 4, len(GroupByGroupID(groupedItems))) // Expecting 2 groups
	// Thêm các assertion để kiểm tra GroupID của từng item nếu cần thiết
}

func TestGroupByGroupID(t *testing.T) {
	// Tạo các giao dịch mẫu đã được nhóm
	items := []Item{
		{ID: 1, Array: []common.Address{common.HexToAddress("0x123"), common.HexToAddress("0x456")}, GroupID: 1, Tx: nil},
		{ID: 2, Array: []common.Address{common.HexToAddress("0x456"), common.HexToAddress("0x789")}, GroupID: 1, Tx: nil},
		{ID: 3, Array: []common.Address{common.HexToAddress("0xabc")}, GroupID: 2, Tx: nil},
	}

	// Nhóm các giao dịch theo GroupID
	groupedGroups := GroupByGroupID(items)

	// Sort by group length (descending) to ensure deterministic assertion
	// Go map iteration order is non-deterministic, so we must sort before asserting by index
	sort.Slice(groupedGroups, func(i, j int) bool {
		return len(groupedGroups[i]) > len(groupedGroups[j])
	})

	// Kiểm tra kết quả
	assert.Equal(t, 2, len(groupedGroups))    // Expecting 2 groups
	assert.Equal(t, 2, len(groupedGroups[0])) // First group (largest) should have 2 items
	assert.Equal(t, 1, len(groupedGroups[1])) // Second group should have 1 item

}

func TestEmptyInput(t *testing.T) {
	items := []Item{}
	_, err := GroupItems(items)
	assert.Error(t, err)
	assert.EqualError(t, err, "input slice is empty")
}

type mockTx struct {
	types.Transaction // embed interface
	hash  common.Hash
	from  common.Address
	nonce uint64
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

	groups := GroupTransactionsDeterministic(items)

	// We expect 2 groups.
	assert.Equal(t, 2, len(groups))

	// Group order is deterministic: sorted by smallest TX hash in the group.
	// Group 1 min hash = 0xa
	// Group 2 min hash = 0xc
	// So Group 1 should be groups[0]
	assert.Equal(t, 2, len(groups[0].Items))
	assert.Equal(t, 1, len(groups[1].Items))

	// Within Group 1, items are sorted by (FromAddress, Nonce).
	// Both have from1. txB has nonce 1, txA has nonce 2. So txB should be first!
	assert.Equal(t, txB.hash, groups[0].Items[0].Tx.Hash())
	assert.Equal(t, txA.hash, groups[0].Items[1].Tx.Hash())

	// Verify Group IDs
	assert.Equal(t, 0, groups[0].GroupID)
	assert.Equal(t, 1, groups[1].GroupID)
}
