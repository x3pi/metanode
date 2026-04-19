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
