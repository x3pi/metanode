package grouptxns

import (
	"sort"
	"time"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
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

// IsNativeParallelAddress checks if the address is a special native contract
// that bypasses Union-Find grouping to allow concurrent execution.
func IsNativeParallelAddress(addr common.Address) bool {
	accountSettingAddr := utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	if addr == accountSettingAddr {
		return true
	}
	if addr == mt_common.VALIDATOR_CONTRACT_ADDRESS {
		return true
	}
	if addr == mt_common.GATEWAY_CONTRACT_ADDRESS {
		return true
	}
	if addr == common.HexToAddress("0x0000000000000000000000000000000000000106") {
		return true
	}
	return false
}

// BuildDeterministicGroupAddrs builds the Array for UnionFind grouping
func BuildDeterministicGroupAddrs(tx types.Transaction) []common.Address {
	return AppendDeterministicGroupAddrs(tx, make([]common.Address, 0))
}

// AppendDeterministicGroupAddrs extracts grouping addresses and appends them to a provided slice
// to allow the caller to use a single flat backing array for memory optimization.
func AppendDeterministicGroupAddrs(tx types.Transaction, out []common.Address) []common.Address {
	var al e_types.AccessList
	ethTx := tx.ToEthTransaction()
	if ethTx != nil {
		al = ethTx.AccessList()
	}
	hasAccessList := len(al) > 0

	// Value transfer modifies the SC balance, so it MUST NOT be separated from the ToAddress
	isValueTransfer := false
	if tx.Amount() != nil && tx.Amount().Sign() > 0 {
		isValueTransfer = true
	}

	startLen := len(out)

	if hasAccessList && !isValueTransfer {
		out = append(out, tx.FromAddress())
		for _, tuple := range al {
			// Skip contract address if it's a native parallel contract to allow concurrent execution
			if !IsNativeParallelAddress(tuple.Address) {
				out = append(out, tuple.Address)
			}
			// NOTE FOR MAINTAINERS & AI: Even if tuple.Address is a native parallel address,
			// any specific StorageKeys listed in the AccessList MUST still be appended for grouping.
			// Transactions accessing the exact same storage slots/keys have state conflicts
			// and MUST be serialized within the same Union-Find group to prevent race conditions & state forks.
			for _, key := range tuple.StorageKeys {
				pseudoAddr := common.BytesToAddress(key.Bytes()[12:])
				out = append(out, pseudoAddr)
			}
		}
	} else {
		for _, addr := range tx.RelatedAddresses() {
			if !IsNativeParallelAddress(addr) {
				out = append(out, addr)
			}
		}
	}

	if len(out) == startLen {
		out = append(out, tx.FromAddress())
	}
	return out
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
	DirtyAccounts    []types.AccountState           // Deferred dirty accounts — applied after parallel phase
	MvmIdMap         map[common.Hash]common.Address // Maps tx.Hash to its executing mvmId
}

// GroupKind classifies a group for dispatch to the optimal execution pipeline.
type GroupKind uint8

const (
	// GroupKindNativeOnly — all txs are simple value transfers; use fast-path.
	GroupKindNativeOnly GroupKind = iota
	// GroupKindContractOnly — all txs are EVM/contract calls; use TrueBlockSTM.
	GroupKindContractOnly
	// GroupKindMixed — native transfers mixed with contract calls sharing addresses;
	// must be serialized inside TrueBlockSTM to preserve correctness.
	GroupKindMixed
)

// HasCodeFunc reports whether addr currently has code — a real deployed
// contract, or an EIP-7702-delegated EOA. Used by classifyGroup to route a
// plain value-transfer (no calldata) through the EVM instead of the native
// fast-path when its recipient has code to run, matching mainnet Ethereum's
// "any value-CALL to an address with code invokes it" semantics — the native
// fast-path moves balance directly and never gives the recipient's
// receive()/fallback/delegate a chance to run at all.
//
// KNOWN LIMITATION: this is checked against chain state as of the START of
// the batch being classified (grouping happens before any of its own txs
// execute), so it does NOT see code an EARLIER tx in the SAME batch
// deploys/delegates — that narrower case would need conflict detection to
// treat "this address might gain code during this batch" as a dependency,
// which the current Union-Find grouping (based on declared
// RelatedAddresses()/AccessList) doesn't do. Every address that already had
// code BEFORE this batch (the common case — the vast majority of contracts
// and any EIP-7702 delegation from an earlier block) is still handled
// correctly.
//
// May be nil, in which case classifyGroup falls back to its previous
// behavior (never checks code presence) — every call site not yet updated to
// pass one keeps working exactly as before.
type HasCodeFunc func(common.Address) bool

// RelativeGroup đại diện cho một nhóm giao dịch liên quan
type RelativeGroup struct {
	GroupID   int
	Items     []Item
	Relatives []common.Address
	// Kind is computed once during grouping and tells the dispatcher which
	// execution pipeline to use.
	Kind GroupKind
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
func GroupTransactionsDeterministic(items []Item, hasCode HasCodeFunc) []RelativeGroup {
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
	// OPTIMIZATION: Use slice of slices instead of map to avoid hash overhead
	rootToItems := make([][]Item, len(items))
	for i := range items {
		root := uf.Find(i)
		rootToItems[root] = append(rootToItems[root], items[i])
	}

	// ═══════════════════════════════════════════════════════════════
	// STEP 3: Convert to RelativeGroup slice
	// ═══════════════════════════════════════════════════════════════
	groups := make([]RelativeGroup, 0, len(items))
	for _, groupItems := range rootToItems {
		if len(groupItems) > 0 {
			groups = append(groups, RelativeGroup{
				Items: groupItems,
			})
		}
	}

	// ═══════════════════════════════════════════════════════════════
	// STEP 4: DETERMINISTIC SORT within each group
	// Sort by FromAddress and Nonce ascending.
	// This guarantees replay matches proposer execution order.
	// ═══════════════════════════════════════════════════════════════
	type sortItem struct {
		item  Item
		from  common.Address
		nonce uint64
		gas   uint64
	}

	for i := range groups {
		if len(groups[i].Items) <= 1 {
			continue // OPTIMIZE: Skip expensive reflection-based sorting for single-item groups
		}
		
		items := make([]sortItem, len(groups[i].Items))
		for j, it := range groups[i].Items {
			items[j] = sortItem{
				item:  it,
				from:  it.Tx.FromAddress(),
				nonce: it.Tx.GetNonce(),
				gas:   it.Tx.MaxGas(),
			}
		}
		sort.Slice(items, func(a, b int) bool {
			cmp := items[a].from.Cmp(items[b].from)
			if cmp != 0 {
				return cmp < 0
			}
			if items[a].nonce != items[b].nonce {
				return items[a].nonce < items[b].nonce
			}
			if items[a].gas != items[b].gas {
				return items[a].gas > items[b].gas // Giảm dần theo MaxGas
			}
			return items[a].item.ID < items[b].item.ID
		})
		for j := range items {
			groups[i].Items[j] = items[j].item
		}
	}

	// ═══════════════════════════════════════════════════════════════
	// STEP 5: DETERMINISTIC SORT of groups themselves
	// Sort by the smallest TX hash in each group.
	// This ensures all nodes process groups in the same order,
	// which matters for deterministic mvmId assignment.
	// ═══════════════════════════════════════════════════════════════
	type groupWithHash struct {
		group   RelativeGroup
		minHash common.Hash
	}
	
	gwh := make([]groupWithHash, len(groups))
	for i, g := range groups {
		min := g.Items[0].Tx.Hash()
		for _, item := range g.Items[1:] {
			if item.Tx.Hash().Cmp(min) < 0 {
				min = item.Tx.Hash()
			}
		}
		gwh[i] = groupWithHash{group: g, minHash: min}
	}

	sort.Slice(gwh, func(i, j int) bool {
		return gwh[i].minHash.Cmp(gwh[j].minHash) < 0
	})

	for i := range gwh {
		groups[i] = gwh[i].group
	}

	// ═══════════════════════════════════════════════════════════════
	// STEP 6: CLASSIFY AND ASSIGN GROUP IDs
	// NOTE: Chunking logic (breaking large groups into smaller ones) 
	// was removed here because:
	// 1. TrueBlockSTM flattens all EVM/Mixed groups into a single array 
	//    anyway and uses MVCC + Index order to resolve conflicts.
	// 2. NativeOnly groups were never chunked to avoid race conditions 
	//    in the lock-free fast path.
	// Therefore, chunking was completely redundant and only added overhead.
	// ═══════════════════════════════════════════════════════════════
	for i := range groups {
		groups[i].Kind = classifyGroup(groups[i].Items, hasCode)
		groups[i].GroupID = i
	}

	return groups
}

// classifyGroup inspects items once and returns the appropriate GroupKind.
func classifyGroup(items []Item, hasCode HasCodeFunc) GroupKind {
	hasNative := false
	hasContract := false
	for _, item := range items {
		tx := item.Tx
		isEvm := false

		if !tx.IsRegularTransaction() {
			isEvm = true
		}

		// EIP-7702 SetCode txs must always run the authorization-list
		// pipeline (delegation designator write, nonce bump) even when
		// they carry no calldata and would otherwise look like a plain
		// value transfer — the native fast-path never runs it.
		if tx.GetType() == uint64(e_types.SetCodeTxType) {
			isEvm = true
		}

		to := tx.ToAddress()
		if to == mt_common.VALIDATOR_CONTRACT_ADDRESS ||
			to == mt_common.GATEWAY_CONTRACT_ADDRESS ||
			to == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS ||
			to == utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
			isEvm = true
		}

		// A plain value-transfer to an address that already has code (a real
		// contract, or an EIP-7702-delegated EOA) must run through the EVM so
		// its receive()/fallback/delegate actually executes — see
		// HasCodeFunc's doc for what this does and doesn't cover. Only
		// checked when not already routed to the EVM for another reason,
		// both because it's redundant then and to skip the state lookup on
		// the hot path when it can't change the outcome.
		if !isEvm && hasCode != nil && hasCode(to) {
			isEvm = true
		}

		if isEvm {
			hasContract = true
		} else {
			hasNative = true
		}

		if hasNative && hasContract {
			return GroupKindMixed
		}
	}
	if hasContract {
		return GroupKindContractOnly
	}
	return GroupKindNativeOnly
}



// PartitionRelativeGroups chia một mảng []RelativeGroup thành n phần nhỏ hơn.
// Nếu số lượng phần tử nhỏ hơn n, nó sẽ được chia thành len(groups) phần.


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
	}
}

// toProtoItem chuyển đổi một struct Item gốc sang Protobuf message.
func toProtoItem(item *Item) *pb.Item {
	if item == nil {
		return nil
	}
	return &pb.Item{
		Id:        int32(item.ID),
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
