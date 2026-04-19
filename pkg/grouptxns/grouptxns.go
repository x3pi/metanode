package grouptxns

import (
	"fmt"
	"sort"
	"time"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// Item đại diện cho một phần tử cần được nhóm (transaction)
type Item struct {
	ID        int
	Array     []common.Address
	GroupID   int
	Tx        types.Transaction
	TimeStart time.Time
}

// UnionFind là cấu trúc dữ liệu Union-Find
type UnionFind struct {
	parent []int
	rank   []int
}

// GroupResult đại diện cho kết quả xử lý một nhóm giao dịch.
type GroupResult struct {
	Transactions     []types.Transaction
	Receipts         []types.Receipt
	ExecuteSCResults []types.ExecuteSCResult
	Error            error
	AsRoot           common.Hash
	DirtyAccounts    []types.AccountState               // Deferred dirty accounts — applied after parallel phase
	MvmIdMap         map[common.Hash]common.Address // Maps tx.Hash to its executing mvmId
}

// RelativeGroup đại diện cho một nhóm giao dịch liên quan
type RelativeGroup struct {
	GroupID   int
	Items     []Item
	Relatives []common.Address
}

// TotalGas tính tổng gas của tất cả các item trong nhóm
func (rg *RelativeGroup) TotalGas() uint64 {
	totalGas := uint64(0)
	for _, item := range rg.Items {
		totalGas += item.Tx.MaxGas()
	}
	return totalGas
}

// TotalTime tính tổng thời gian của tất cả các item trong nhóm
func (rg *RelativeGroup) TotalTime() uint64 {
	totalTime := uint64(0)
	for _, item := range rg.Items {
		totalTime += item.Tx.MaxTimeUse()
	}
	return totalTime
}

// NewUnionFind tạo một đối tượng UnionFind mới
func NewUnionFind(n int) *UnionFind {
	parent := make([]int, n)
	rank := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	return &UnionFind{parent, rank}
}

// Find tìm cha của một phần tử
func (uf *UnionFind) Find(i int) int {
	if uf.parent[i] == i {
		return i
	}
	uf.parent[i] = uf.Find(uf.parent[i])
	return uf.parent[i]
}

// Union hợp nhất hai phần tử
func (uf *UnionFind) Union(i, j int) {
	rootI := uf.Find(i)
	rootJ := uf.Find(j)
	if rootI != rootJ {
		if uf.rank[rootI] < uf.rank[rootJ] {
			uf.parent[rootI] = rootJ
		} else if uf.rank[rootI] > uf.rank[rootJ] {
			uf.parent[rootJ] = rootI
		} else {
			uf.parent[rootJ] = rootI
			uf.rank[rootI]++
		}
	}
}
func GroupItems(items []Item) ([]Item, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("input slice is empty")
	}

	// Sử dụng map để ánh xạ địa chỉ -> danh sách các chỉ số của các item chứa địa chỉ đó
	addressToIndices := make(map[string][]int)
	for i, item := range items {
		for _, addr := range item.Array {
			addressToIndices[addr.Hex()] = append(addressToIndices[addr.Hex()], i)
		}
	}

	// Khởi tạo Union-Find
	uf := NewUnionFind(len(items))

	// Liên kết các phần tử có địa chỉ chung
	for _, indices := range addressToIndices {
		for i := 1; i < len(indices); i++ {
			uf.Union(indices[0], indices[i])
		}
	}

	// Gán GroupID cho mỗi item dựa trên Union-Find
	groupIDMap := make(map[int]int)
	groupIDCounter := 1
	for i := range items {
		root := uf.Find(i)
		if _, ok := groupIDMap[root]; !ok {
			groupIDMap[root] = groupIDCounter
			groupIDCounter++
		}
		items[i].GroupID = groupIDMap[root]
	}

	return items, nil
}

// groupByGroupID nhóm các phần tử theo GroupID
func GroupByGroupID(items []Item) [][]Item {
	groups := make(map[int][]Item)
	for _, item := range items {
		groups[item.GroupID] = append(groups[item.GroupID], item)
	}

	result := make([][]Item, 0, len(groups))
	for _, group := range groups {
		result = append(result, group)
	}
	return result
}

func GroupAndLimitTransactionsOptimized(items []Item, maxGroupGas uint64, maxTotalGas uint64, maxGroupTimes uint64, maxTotalTime uint64) ([]RelativeGroup, []Item, error) {

	relativeGroups := []RelativeGroup{}
	excludedItems := []Item{}
	totalGas := uint64(0)
	totalTime := uint64(0)

	// Map ánh xạ địa chỉ tới groupID
	addressToGroup := make(map[string]int)

	for _, item := range items {
		// Nếu giao dịch chỉ đọc, tạo nhóm riêng
		if item.Tx.GetReadOnly() {
			newGroup := RelativeGroup{
				GroupID: len(relativeGroups),
				Items:   []Item{item},
			}
			relativeGroups = append(relativeGroups, newGroup)
			continue // Tiếp tục vòng lặp cho item tiếp theo
		}

		var selectedGroup *RelativeGroup

		// Kiểm tra các nhóm có thể thêm item
		for _, addr := range item.Array {
			if groupID, exists := addressToGroup[addr.Hex()]; exists {
				group := &relativeGroups[groupID]
				newGas := group.TotalGas() + item.Tx.MaxGas()
				newTime := group.TotalTime() + item.Tx.MaxTimeUse()

				// Nếu nhóm này đủ điều kiện thì chọn
				if newGas <= maxGroupGas && newTime <= maxGroupTimes {
					selectedGroup = group
					break
				}
			}
		}

		if selectedGroup != nil {
			// Nếu tìm thấy nhóm phù hợp, thêm item vào nhóm
			selectedGroup.Items = append(selectedGroup.Items, item)
			// Sắp xếp lại Items theo TimeStart

			for _, addr := range item.Array {
				addressToGroup[addr.Hex()] = selectedGroup.GroupID
			}
		} else {
			// Nếu không tìm thấy nhóm phù hợp, tạo nhóm mới
			newGroup := RelativeGroup{
				GroupID: len(relativeGroups),
				Items:   []Item{item},
			}
			newGas := item.Tx.MaxGas()
			newTime := item.Tx.MaxTimeUse()

			// Kiểm tra các điều kiện giới hạn
			if newGas <= maxGroupGas && newTime <= maxGroupTimes &&
				totalGas+newGas <= maxTotalGas && totalTime+newTime <= maxTotalTime && len(relativeGroups) < 500000 {
				relativeGroups = append(relativeGroups, newGroup)
				for _, addr := range item.Array {
					addressToGroup[addr.Hex()] = newGroup.GroupID
				}
				totalGas += newGas
				totalTime += newTime
			} else {
				// Thêm vào danh sách loại bỏ nếu không hợp lệ
				excludedItems = append(excludedItems, item)
			}
		}
	}

	// Sắp xếp từng nhóm con trong relativeGroups
	for i := range relativeGroups {
		sort.Slice(relativeGroups[i].Items, func(a, b int) bool {
			gasCmp := utils.CompareUint64(relativeGroups[i].Items[a].Tx.MaxGas(), relativeGroups[i].Items[b].Tx.MaxGas())
			if gasCmp != 0 {
				return gasCmp == -1 // Giảm dần theo MaxGas
			}
			nonceCmp := utils.CompareUint64(relativeGroups[i].Items[a].Tx.GetNonce(), relativeGroups[i].Items[b].Tx.GetNonce())
			if nonceCmp != 0 {
				return nonceCmp == 1 // Tăng dần theo Nonce
			}
			return relativeGroups[i].Items[a].Tx.Hash().Cmp(relativeGroups[i].Items[b].Tx.Hash()) == -1 // Tăng dần theo Hash
		})
	}

	return relativeGroups, excludedItems, nil
}

// GroupTransactionsDeterministic groups TXs by shared RelatedAddresses for
// FORK-SAFE parallel execution of Rust-committed blocks.
//
// Guarantees (all nodes produce identical output for identical input):
//  1. Union-Find groups TXs sharing ANY RelatedAddress into the same group
//  2. TXs with non-overlapping addresses run in separate groups → parallel CPUs
//  3. Within each group: sorted by (FromAddress, Nonce, Hash) → deterministic nonce ordering
//  4. Groups sorted by smallest TX hash → deterministic group order
//  5. NO TX is ever dropped (no gas/time limits)
//  6. NO time.Now() or any non-deterministic input
func GroupTransactionsDeterministic(items []Item) []RelativeGroup {
	if len(items) == 0 {
		return []RelativeGroup{}
	}

	// ═══════════════════════════════════════════════════════════════
	// STEP 1: Union-Find to merge TXs sharing any RelatedAddress
	// ═══════════════════════════════════════════════════════════════
	uf := NewUnionFind(len(items))
	addressToFirstIdx := make(map[common.Address]int, len(items)*2)

	for i, item := range items {
		for _, addr := range item.Array {
			if firstIdx, exists := addressToFirstIdx[addr]; exists {
				// This address was seen before — merge the two TXs into same group
				uf.Union(firstIdx, i)
			} else {
				addressToFirstIdx[addr] = i
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════
	// STEP 2: Collect items into groups by Union-Find root
	// ═══════════════════════════════════════════════════════════════
	rootToItems := make(map[int][]Item, len(items))
	for i := range items {
		root := uf.Find(i)
		rootToItems[root] = append(rootToItems[root], items[i])
	}

	// ═══════════════════════════════════════════════════════════════
	// STEP 3: Convert to RelativeGroup slice
	// ═══════════════════════════════════════════════════════════════
	groups := make([]RelativeGroup, 0, len(rootToItems))
	for _, groupItems := range rootToItems {
		groups = append(groups, RelativeGroup{
			Items: groupItems,
		})
	}

	// ═══════════════════════════════════════════════════════════════
	// STEP 4: DETERMINISTIC SORT within each group
	// Sort by (FromAddress, Nonce, Hash) — guarantees:
	//   - Same sender's TXs are consecutive and nonce-ordered
	//   - Different senders within same group have stable order
	// ═══════════════════════════════════════════════════════════════
	for i := range groups {
		sort.Slice(groups[i].Items, func(a, b int) bool {
			txA := groups[i].Items[a].Tx
			txB := groups[i].Items[b].Tx
			// Primary: sort by FromAddress (bytes comparison)
			fromCmp := txA.FromAddress().Cmp(txB.FromAddress())
			if fromCmp != 0 {
				return fromCmp < 0
			}
			// Secondary: sort by Nonce (ascending — critical for EVM execution order)
			if txA.GetNonce() != txB.GetNonce() {
				return txA.GetNonce() < txB.GetNonce()
			}
			// Tertiary: sort by Hash (tiebreaker — shouldn't happen with unique nonces)
			return txA.Hash().Cmp(txB.Hash()) < 0
		})
	}

	// ═══════════════════════════════════════════════════════════════
	// STEP 5: DETERMINISTIC SORT of groups themselves
	// Sort by the smallest TX hash in each group.
	// This ensures all nodes process groups in the same order,
	// which matters for deterministic mvmId assignment.
	// ═══════════════════════════════════════════════════════════════
	sort.Slice(groups, func(i, j int) bool {
		// Each group has at least 1 item (guaranteed by construction)
		minHashI := groups[i].Items[0].Tx.Hash()
		for _, item := range groups[i].Items[1:] {
			if item.Tx.Hash().Cmp(minHashI) < 0 {
				minHashI = item.Tx.Hash()
			}
		}
		minHashJ := groups[j].Items[0].Tx.Hash()
		for _, item := range groups[j].Items[1:] {
			if item.Tx.Hash().Cmp(minHashJ) < 0 {
				minHashJ = item.Tx.Hash()
			}
		}
		return minHashI.Cmp(minHashJ) < 0
	})

	// Assign sequential GroupIDs after final sort
	for i := range groups {
		groups[i].GroupID = i
	}

	return groups
}

func GroupTransactionsByRelativeAddress(items []Item) ([]RelativeGroup, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("input slice is empty")
	}

	relativeGroups := []RelativeGroup{}
	// Map ánh xạ địa chỉ tới groupID
	addressToGroup := make(map[string]int)

	for _, item := range items {
		var targetGroup *RelativeGroup
		foundGroup := false

		// Tìm kiếm nhóm hiện có cho item này
		for _, addr := range item.Array {
			if groupID, exists := addressToGroup[addr.Hex()]; exists {
				// Nếu đã tìm thấy một nhóm, chỉ cần hợp nhất vào nhóm đó
				if foundGroup && targetGroup.GroupID != groupID {
					// Logic để hợp nhất các nhóm nếu một item thuộc về nhiều nhóm
					// Ở đây, ta sẽ hợp nhất vào nhóm có chỉ số nhỏ hơn
					sourceGroup := &relativeGroups[groupID]
					if targetGroup.GroupID > sourceGroup.GroupID {
						targetGroup, sourceGroup = sourceGroup, targetGroup
					}
					// Chuyển tất cả item từ sourceGroup sang targetGroup
					targetGroup.Items = append(targetGroup.Items, sourceGroup.Items...)
					// Cập nhật lại addressToGroup cho các địa chỉ trong sourceGroup
					for _, mergedItem := range sourceGroup.Items {
						for _, mergedAddr := range mergedItem.Array {
							addressToGroup[mergedAddr.Hex()] = targetGroup.GroupID
						}
					}
					// Đánh dấu sourceGroup là rỗng để có thể xóa sau
					sourceGroup.Items = nil
				} else if !foundGroup {
					targetGroup = &relativeGroups[groupID]
					foundGroup = true
				}
			}
		}

		if !foundGroup {
			// Nếu không tìm thấy nhóm nào, tạo nhóm mới
			newGroup := RelativeGroup{
				GroupID: len(relativeGroups),
				Items:   []Item{},
			}
			relativeGroups = append(relativeGroups, newGroup)
			targetGroup = &relativeGroups[len(relativeGroups)-1]
		}

		// Thêm item vào nhóm đã chọn (hoặc nhóm mới)
		targetGroup.Items = append(targetGroup.Items, item)
		// Cập nhật addressToGroup cho tất cả địa chỉ trong item
		for _, addr := range item.Array {
			addressToGroup[addr.Hex()] = targetGroup.GroupID
		}
	}

	// Loại bỏ các nhóm rỗng đã được hợp nhất
	finalGroups := []RelativeGroup{}
	for _, group := range relativeGroups {
		if len(group.Items) > 0 {
			finalGroups = append(finalGroups, group)
		}
	}

	// Sắp xếp các item trong mỗi nhóm
	for i := range finalGroups {
		sort.Slice(finalGroups[i].Items, func(a, b int) bool {
			gasCmp := utils.CompareUint64(finalGroups[i].Items[a].Tx.MaxGas(), finalGroups[i].Items[b].Tx.MaxGas())
			if gasCmp != 0 {
				return gasCmp == -1 // Giảm dần theo MaxGas
			}
			nonceCmp := utils.CompareUint64(finalGroups[i].Items[a].Tx.GetNonce(), finalGroups[i].Items[b].Tx.GetNonce())
			if nonceCmp != 0 {
				return nonceCmp == 1 // Tăng dần theo Nonce
			}
			return finalGroups[i].Items[a].Tx.Hash().Cmp(finalGroups[i].Items[b].Tx.Hash()) == -1 // Tăng dần theo Hash
		})
	}

	return finalGroups, nil
}

// PartitionRelativeGroups chia một mảng []RelativeGroup thành n phần nhỏ hơn.
// Nếu số lượng phần tử nhỏ hơn n, nó sẽ được chia thành len(groups) phần.
func PartitionRelativeGroups(groups []RelativeGroup, n int) ([][]RelativeGroup, error) {
	if n <= 0 {
		return nil, fmt.Errorf("số lượng phần chia (n) phải lớn hơn 0")
	}

	if len(groups) == 0 {
		return [][]RelativeGroup{}, nil
	}

	// Nếu số lượng group ít hơn n, chia thành len(groups) phần
	if len(groups) < n {
		n = len(groups)
	}

	partitions := make([][]RelativeGroup, 0, n)
	baseSize := len(groups) / n
	remainder := len(groups) % n
	current := 0

	for i := 0; i < n; i++ {
		size := baseSize
		if remainder > 0 {
			size++
			remainder--
		}

		end := current + size
		if end > len(groups) {
			end = len(groups)
		}

		partitions = append(partitions, groups[current:end])
		current = end
	}

	return partitions, nil
}

// ToProtoRelativeGroup chuyển đổi một struct RelativeGroup gốc sang Protobuf message.
func ToProtoRelativeGroup(rg *RelativeGroup) *pb.RelativeGroup {
	if rg == nil {
		return nil
	}

	protoItems := make([]*pb.Item, len(rg.Items))
	for i, item := range rg.Items {
		protoItems[i] = toProtoItem(&item)
	}

	return &pb.RelativeGroup{
		GroupId:   int32(rg.GroupID),
		Items:     protoItems,
		Relatives: addressesToBytes(rg.Relatives),
	}
}

// FromProtoRelativeGroup chuyển đổi một Protobuf message RelativeGroup sang struct gốc.
func FromProtoRelativeGroup(protoRg *pb.RelativeGroup) *RelativeGroup {
	if protoRg == nil {
		return nil
	}

	goItems := make([]Item, len(protoRg.Items))
	for i, protoItem := range protoRg.Items {
		goItems[i] = *fromProtoItem(protoItem)
	}

	return &RelativeGroup{
		GroupID:   int(protoRg.GroupId),
		Items:     goItems,
		Relatives: bytesToAddresses(protoRg.Relatives),
	}
}

// toProtoItem chuyển đổi một struct Item gốc sang Protobuf message.
func toProtoItem(item *Item) *pb.Item {
	if item == nil {
		return nil
	}
	return &pb.Item{
		Id:        int32(item.ID),
		Array:     addressesToBytes(item.Array),
		GroupId:   int32(item.GroupID),
		Tx:        toProtoTransaction(item.Tx),
		TimeStart: item.TimeStart.Unix(),
	}
}

// fromProtoItem chuyển đổi một Protobuf message Item sang struct gốc.
func fromProtoItem(protoItem *pb.Item) *Item {
	if protoItem == nil {
		return nil
	}
	return &Item{
		ID:        int(protoItem.Id),
		Array:     bytesToAddresses(protoItem.Array),
		GroupID:   int(protoItem.GroupId),
		Tx:        fromProtoTransaction(protoItem.Tx),
		TimeStart: time.Unix(protoItem.TimeStart, 0),
	}
}

// toProtoTransaction sử dụng phương thức Proto() có sẵn từ transaction của bạn.
func toProtoTransaction(tx types.Transaction) *pb.Transaction {
	if tx == nil || tx.Proto() == nil {
		return nil
	}
	// tx.Proto() trả về protoreflect.ProtoMessage, cần ép kiểu về *pb.Transaction
	protoTx, ok := tx.Proto().(*pb.Transaction)
	if !ok {
		// Xử lý lỗi nếu ép kiểu thất bại (trường hợp này hiếm khi xảy ra nếu cấu trúc đúng)
		return nil
	}
	return protoTx
}

// fromProtoTransaction sử dụng hàm TransactionFromProto có sẵn từ package transaction.
func fromProtoTransaction(protoTx *pb.Transaction) types.Transaction {
	if protoTx == nil {
		return nil
	}
	// Gọi thẳng hàm đã có từ package transaction
	return transaction.TransactionFromProto(protoTx)
}

// addressesToBytes chuyển đổi một slice []common.Address thành [][]byte.
func addressesToBytes(addrs []common.Address) [][]byte {
	if addrs == nil {
		return nil
	}
	byteArrays := make([][]byte, len(addrs))
	for i, addr := range addrs {
		byteArrays[i] = addr.Bytes()
	}
	return byteArrays
}

// bytesToAddresses chuyển đổi một slice [][]byte thành []common.Address.
func bytesToAddresses(byteArrays [][]byte) []common.Address {
	if byteArrays == nil {
		return nil
	}
	addrs := make([]common.Address, len(byteArrays))
	for i, b := range byteArrays {
		addrs[i] = common.BytesToAddress(b)
	}
	return addrs
}
