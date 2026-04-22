package receipt

import (
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/types"
)

var ErrorReceiptNotFound = errors.New("receipt not found")

type Receipts struct {
	trie            trie.StateTrie
	db              storage.Storage
	originRootHash  common.Hash
	dirtyReceipts   map[common.Hash]types.Receipt
	receiptBatchPut []byte
}

func (r *Receipts) SetReceiptBatchPut(batch []byte) {
	r.receiptBatchPut = batch
}

func (r *Receipts) GetReceiptBatchPut() []byte {
	batch := r.receiptBatchPut
	r.receiptBatchPut = nil
	return batch
}

func NewReceipts(db storage.Storage) (types.Receipts, error) {
	trie, err := trie.NewStateTrie(trie.EmptyRootHash, db, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create empty receipt trie: %w", err)
	}
	return &Receipts{
		trie:           trie,
		db:             db,
		originRootHash: trie.Hash(),
		dirtyReceipts:  make(map[common.Hash]types.Receipt),
	}, nil
}

// NewReceiptsFromRoot khởi tạo một đối tượng Receipts từ một root hash đã có.
// Hàm này cho phép bạn "load" lại trạng thái của tất cả các biên lai tại một thời điểm cụ thể.
func NewReceiptsFromRoot(root common.Hash, db storage.Storage) (types.Receipts, error) {
	trie, err := trie.NewStateTrie(root, db, true)
	if err != nil {
		return nil, fmt.Errorf("không thể tạo trie từ root %s: %w", root.Hex(), err)
	}
	return &Receipts{
		trie:           trie,
		db:             db,
		originRootHash: root,
		dirtyReceipts:  make(map[common.Hash]types.Receipt),
	}, nil
}

func (r *Receipts) ReceiptsRoot() (common.Hash, error) {
	return r.trie.Hash(), nil
}

func (r *Receipts) AddReceipt(receipt types.Receipt) error {
	r.setDirtyReceipt(receipt) // Chỉ cập nhật dirtyReceipts, không ghi vào db ngay
	return nil
}

// AddReceipts is optimized for bulk insertions (block processing) to avoid Map Growth GC pressure.
func (r *Receipts) AddReceipts(receipts []types.Receipt) error {
	if len(r.dirtyReceipts) == 0 && len(receipts) > 0 {
		r.dirtyReceipts = make(map[common.Hash]types.Receipt, len(receipts))
	}
	for _, receipt := range receipts {
		r.setDirtyReceipt(receipt)
	}
	return nil
}

func (r *Receipts) ReceiptsMap() map[common.Hash]types.Receipt {
	return r.dirtyReceipts
}

func (r *Receipts) UpdateExecuteResultToReceipt(
	hash common.Hash,
	status pb.RECEIPT_STATUS,
	returnValue []byte,
	exception pb.EXCEPTION,
	gasUsed uint64,
	eventLogs []types.EventLog,
) error {
	receipt, exists := r.dirtyReceipts[hash]
	if !exists {
		return ErrorReceiptNotFound
	}
	receipt.UpdateExecuteResult(
		status,
		returnValue,
		exception,
		gasUsed,
		eventLogs,
	)
	r.setDirtyReceipt(receipt) // Cập nhật lại receipt vào dirtyReceipts
	return nil
}

func (r *Receipts) GasUsed() uint64 {
	var totalGas uint64
	for _, receipt := range r.dirtyReceipts {
		totalGas += receipt.GasUsed()
	}
	return totalGas
}

func (r *Receipts) GetReceipt(hash common.Hash) (types.Receipt, error) {
	if receipt, exists := r.dirtyReceipts[hash]; exists {
		return receipt, nil
	}

	data, err := r.trie.Get(hash.Bytes())
	if err != nil {
		logger.Debug("[RECEIPT GET] Receipt not found in trie: hash=%s, err=%v", hash.Hex(), err)
		return nil, ErrorReceiptNotFound
	}
	var receipt = &Receipt{}

	err = receipt.Unmarshal(data)
	if err != nil {
		logger.Error("❌ [RECEIPT GET] Failed to unmarshal receipt: hash=%s, err=%v", hash.Hex(), err)
		return nil, err
	}
	return receipt, nil
}

func (r *Receipts) Discard() error {
	r.dirtyReceipts = make(map[common.Hash]types.Receipt)
	trie, err := trie.NewStateTrie(r.originRootHash, r.db, true)
	if err != nil {
		return err
	}
	r.trie = trie
	return nil
}

// Commit tối ưu hóa việc ghi dữ liệu, tuân theo quy trình IntermediateRoot -> trie.Commit -> DB
func (r *Receipts) Commit() (common.Hash, error) {
	if r.trie == nil {
		return common.Hash{}, errors.New("commit được gọi với trie là nil")
	}

	totalTimeStart := time.Now()

	// Giai đoạn 1: Áp dụng các thay đổi từ dirtyReceipts vào trie trong bộ nhớ.
	intermediateHash, err := r.IntermediateRoot()
	if err != nil {
		return common.Hash{}, fmt.Errorf("commit thất bại trong quá trình IntermediateRoot: %w", err)
	}

	// Giai đoạn 2: Commit trie trong bộ nhớ để tạo ra nodeSet.
	committedHash, nodeSet, _, err := r.trie.Commit(true)
	if err != nil {
		return common.Hash{}, fmt.Errorf("tính toán trie.Commit thất bại: %w", err)
	}

	if intermediateHash != committedHash {
		// NOMT skips this check because its root hash is only computed during Commit()
		// intermediateHash = old root, committedHash = new root.
		if _, isNomt := r.trie.(*trie.NomtStateTrie); !isNomt {
			return common.Hash{}, fmt.Errorf(
				"hash gốc không khớp sau khi tính toán commit (intermediate: %s, commit: %s)",
				intermediateHash, committedHash,
			)
		}
	}
	finalHash := committedHash

	// Commit NOMT payload sequentially
	if nomtTrie, isNomt := r.trie.(*trie.NomtStateTrie); isNomt {
		if err := nomtTrie.CommitPayload(); err != nil {
			return common.Hash{}, fmt.Errorf("NOMT CommitPayload failed: %w", err)
		}
	}

	// Giai đoạn 3: Song song hóa việc tạo batch từ nodeSet và ghi xuống DB.
	if nodeSet != nil && len(nodeSet.Nodes) > 0 {
		dbPhaseStart := time.Now()
		logger.Debug("Processing %d receipt trie nodes...", len(nodeSet.Nodes))

		// SỬA LỖI: Định nghĩa một struct cục bộ để tránh tham chiếu đến kiểu không được xuất
		type nodeData struct {
			Hash common.Hash
			Blob []byte
		}
		type batchItem struct {
			Key   []byte
			Value []byte
		}

		numJobs := len(nodeSet.Nodes)
		jobs := make(chan nodeData, numJobs) // Sửa kiểu của channel
		results := make(chan batchItem, numJobs)
		numWorkers := runtime.NumCPU()
		var wg sync.WaitGroup

		for w := 0; w < numWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for node := range jobs { // Worker nhận dữ liệu từ `nodeData`
					results <- batchItem{Key: node.Hash.Bytes(), Value: node.Blob}
				}
			}()
		}

		// SỬA LỖI: Truyền dữ liệu vào channel `jobs` bằng struct cục bộ
		for _, node := range nodeSet.Nodes {
			if node.Hash != (common.Hash{}) {
				jobs <- nodeData{Hash: node.Hash, Blob: node.Blob}
			}
		}
		close(jobs)
		wg.Wait()
		close(results)

		batch := make([][2][]byte, 0, numJobs)
		for item := range results {
			batch = append(batch, [2][]byte{item.Key, item.Value})
		}
		logger.Debug("✅ Batch creation for %d nodes completed in %v", len(batch), time.Since(dbPhaseStart))

		if len(batch) > 0 {
			if err := r.db.BatchPut(batch); err != nil {
				return common.Hash{}, fmt.Errorf("lỗi khi ghi batch node receipt vào db: %w", err)
			}
			serializedBatchData, err := storage.SerializeBatch(batch)
			if err != nil {
				logger.Error(fmt.Sprintf("Lỗi khi tuần tự hóa receipt node batch: %v", err))
			} else {
				r.SetReceiptBatchPut(serializedBatchData)
			}
			logger.Debug("✅ DB Write and Batch Serialization completed in %v", time.Since(dbPhaseStart))
		}
	} else {
		// FlatStateTrie/VerkleStateTrie FIX: nodeSet is nil for flat backend, but we still need
		// the flat entries in ReceiptBatchPut for replication to Sub nodes.
		var flatBatch [][2][]byte
		if flatTrie, ok := r.trie.(*trie.FlatStateTrie); ok {
			flatBatch = flatTrie.GetCommitBatch()
		} else if verkleTrie, ok := r.trie.(*trie.VerkleStateTrie); ok {
			flatBatch = verkleTrie.GetCommitBatch()
		} else if nomtTrie, ok := r.trie.(*trie.NomtStateTrie); ok {
			flatBatch = nomtTrie.GetCommitBatch()
		}

		if len(flatBatch) > 0 {
			serializedBatchData, err := storage.SerializeBatch(flatBatch)
			if err != nil {
				logger.Error(fmt.Sprintf("Lỗi khi tuần tự hóa flat receipt batch: %v", err))
			} else {
				r.SetReceiptBatchPut(serializedBatchData)
			}
			logger.Debug("[FlatStateTrie] Included %d flat receipt entries in ReceiptBatchPut for replication", len(flatBatch))
		} else {
			logger.Debug("No new receipt trie nodes to commit.")
		}
	}

	// Giai đoạn 4: Tạo một instance trie mới với root hash đã được commit.
	newTrie, err := trie.NewStateTrie(finalHash, r.db, true)
	if err != nil {
		return common.Hash{}, fmt.Errorf("không thể tải trie mới cho root %s sau khi commit: %w", finalHash, err)
	}

	// Giai đoạn 5: Cập nhật trie và dọn dẹp.
	r.trie = newTrie
	r.originRootHash = finalHash

	logger.Debug("🚀 Total Receipt Commit Execution Time: %v", time.Since(totalTimeStart))

	return finalHash, nil
}

// CommitPipeline performs the fast, synchronous phase of commit.
// It generates the nodeSet and serialization batch but skips LevelDB BatchPut.
func (r *Receipts) CommitPipeline() (*types.ReceiptPipelineResult, error) {
	if r.trie == nil {
		return nil, errors.New("commit được gọi với trie là nil")
	}

	totalTimeStart := time.Now()

	// Giai đoạn 1: Áp dụng các thay đổi từ dirtyReceipts vào trie trong bộ nhớ.
	intermediateHash, err := r.IntermediateRoot()
	if err != nil {
		return nil, fmt.Errorf("CommitPipeline thất bại trong quá trình IntermediateRoot: %w", err)
	}

	// Giai đoạn 2: Commit trie trong bộ nhớ để tạo ra nodeSet.
	committedHash, nodeSet, oldKeys, err := r.trie.Commit(true)
	if err != nil {
		return nil, fmt.Errorf("tính toán trie.Commit thất bại: %w", err)
	}

	if intermediateHash != committedHash {
		if _, isNomt := r.trie.(*trie.NomtStateTrie); !isNomt {
			return nil, fmt.Errorf(
				"hash gốc không khớp sau khi tính toán commit (intermediate: %s, commit: %s)",
				intermediateHash, committedHash,
			)
		}
	}

	var batch [][2][]byte
	var receiptBatchData []byte

	// Giai đoạn 3: Tạo batch từ nodeSet
	if nodeSet != nil && len(nodeSet.Nodes) > 0 {
		batch = make([][2][]byte, 0, len(nodeSet.Nodes))
		for _, node := range nodeSet.Nodes {
			if node.Hash != (common.Hash{}) {
				batch = append(batch, [2][]byte{node.Hash.Bytes(), node.Blob})
			}
		}

		if len(batch) > 0 {
			serializedBatchData, err := storage.SerializeBatch(batch)
			if err != nil {
				logger.Error(fmt.Sprintf("Lỗi khi tuần tự hóa receipt node batch: %v", err))
			} else {
				receiptBatchData = serializedBatchData
			}
		}
	} else {
		// Flat Backend handling
		var flatBatch [][2][]byte
		if flatTrie, ok := r.trie.(*trie.FlatStateTrie); ok {
			flatBatch = flatTrie.GetCommitBatch()
		} else if verkleTrie, ok := r.trie.(*trie.VerkleStateTrie); ok {
			flatBatch = verkleTrie.GetCommitBatch()
		} else if nomtTrie, ok := r.trie.(*trie.NomtStateTrie); ok {
			flatBatch = nomtTrie.GetCommitBatch()
		}

		if len(flatBatch) > 0 {
			batch = flatBatch
			serializedBatchData, err := storage.SerializeBatch(flatBatch)
			if err != nil {
				logger.Error(fmt.Sprintf("Lỗi khi tuần tự hóa flat receipt batch: %v", err))
			} else {
				receiptBatchData = serializedBatchData
			}
		}
	}

	if receiptBatchData != nil {
		r.SetReceiptBatchPut(receiptBatchData)
	}

	logger.Debug("🚀 Receipt CommitPipeline Execution Time: %v", time.Since(totalTimeStart))

	return &types.ReceiptPipelineResult{
		FinalHash:    committedHash,
		Batch:        batch,
		ReceiptBatch: receiptBatchData,
		OldKeys:      oldKeys,
		Trie:         r.trie,
	}, nil
}

// PersistAsync updates LevelDB in background
func (r *Receipts) PersistAsync(result *types.ReceiptPipelineResult) error {
	if result == nil {
		return nil
	}

	if len(result.Batch) > 0 {
		if err := r.db.BatchPut(result.Batch); err != nil {
			return fmt.Errorf("Receipts PersistAsync BatchPut failed: %w", err)
		}
	}

	if nomtTrie, isNomt := result.Trie.(*trie.NomtStateTrie); isNomt {
		if err := nomtTrie.CommitPayload(); err != nil {
			return fmt.Errorf("Receipts PersistAsync NOMT CommitPayload failed: %w", err)
		}
	}

	var newTrie trie.StateTrie
	if result.Trie != nil {
		newTrie = result.Trie.(trie.StateTrie)
	} else {
		t, err := trie.NewStateTrie(result.FinalHash, r.db, true)
		if err != nil {
			return fmt.Errorf("không thể tải trie mới cho root %s sau khi commit: %w", result.FinalHash, err)
		}
		newTrie = t
	}

	r.trie = newTrie
	r.originRootHash = result.FinalHash
	return nil
}

func (r *Receipts) IntermediateRoot() (common.Hash, error) {
	if len(r.dirtyReceipts) == 0 {
		return r.trie.Hash(), nil
	}

	// FORK-SAFETY: Sort receipts by txHash for deterministic trie insertion order
	type receiptEntry struct {
		txHash common.Hash
		data   []byte
	}
	entries := make([]receiptEntry, len(r.dirtyReceipts))
	keys := make([]common.Hash, 0, len(r.dirtyReceipts))
	for hash := range r.dirtyReceipts {
		keys = append(keys, hash)
	}

	// Phase 1: Parallel Marshalling
	// Marshalling 65K receipts is CPU bound.
	var wg sync.WaitGroup
	numWorkers := runtime.NumCPU()
	if numWorkers > 32 {
		numWorkers = 32
	}
	chunkSize := (len(keys) + numWorkers - 1) / numWorkers

	errChan := make(chan error, numWorkers)

	for i := 0; i < numWorkers; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if start >= len(keys) {
			break
		}
		if end > len(keys) {
			end = len(keys)
		}

		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for j := s; j < e; j++ {
				hash := keys[j]
				rcp := r.dirtyReceipts[hash]
				b, err := rcp.Marshal()
				if err != nil {
					errChan <- err
					return
				}
				entries[j] = receiptEntry{txHash: hash, data: b}
			}
		}(start, end)
	}
	wg.Wait()
	close(errChan)
	if err := <-errChan; err != nil {
		return common.Hash{}, err
	}

	// Phase 2: Sort by txHash for deterministic order
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].txHash.Cmp(entries[j].txHash) < 0
	})

	// Phase 3: Update trie
	// BatchUpdate partitioning by first nibble is already optimized for parallelism.
	batchKeys := make([][]byte, len(entries))
	batchValues := make([][]byte, len(entries))
	for i, entry := range entries {
		batchKeys[i] = entry.txHash.Bytes()
		batchValues[i] = entry.data
	}

	if nomtTrie, ok := r.trie.(*trie.NomtStateTrie); ok {
		oldValues := make([][]byte, len(batchKeys))
		if err := nomtTrie.BatchUpdateWithCachedOldValues(batchKeys, batchValues, oldValues); err != nil {
			return common.Hash{}, err
		}
	} else if flatTrie, ok := r.trie.(*trie.FlatStateTrie); ok {
		oldValues := make([][]byte, len(batchKeys))
		if err := flatTrie.BatchUpdateWithCachedOldValues(batchKeys, batchValues, oldValues); err != nil {
			return common.Hash{}, err
		}
	} else {
		if err := r.trie.BatchUpdate(batchKeys, batchValues); err != nil {
			return common.Hash{}, err
		}
	}

	r.dirtyReceipts = make(map[common.Hash]types.Receipt)
	return r.trie.Hash(), nil
}

func (r *Receipts) setDirtyReceipt(receipt types.Receipt) {
	r.dirtyReceipts[receipt.TransactionHash()] = receipt
}

func MarshalReceipts(receipts []types.Receipt) ([]byte, error) {
	if len(receipts) == 0 {
		return proto.MarshalOptions{Deterministic: true}.Marshal(&pb.Receipts{Receipts: []*pb.Receipt{}})
	}

	pbList := make([]*pb.Receipt, len(receipts))
	// Phase 1: Convert all to proto (cheap but good to do before marshalling)
	for i, r := range receipts {
		if receipt, ok := r.(*Receipt); ok {
			pbReceipt, ok := receipt.Proto().(*pb.Receipt)
			if !ok {
				return nil, fmt.Errorf("không thể khẳng định kiểu cho receipt tại chỉ số %d", i)
			}
			pbList[i] = pbReceipt
		} else {
			return nil, fmt.Errorf("phần tử tại chỉ số %d không phải là *Receipt", i)
		}
	}

	// Marshaling the entire pb.Receipts object is one proto.Marshal call.
	// However, the individual receipts are already in proto form.
	pbReceipts := &pb.Receipts{
		Receipts: pbList,
	}

	return proto.MarshalOptions{Deterministic: true}.Marshal(pbReceipts)
}

func UnmarshalReceipts(data []byte) ([]types.Receipt, error) {
	pbReceipts := &pb.Receipts{}
	if err := proto.Unmarshal(data, pbReceipts); err != nil {
		return nil, err
	}

	receipts := make([]types.Receipt, len(pbReceipts.Receipts))
	for i, r := range pbReceipts.Receipts {
		receipts[i] = ReceiptFromProto(r)
	}
	return receipts, nil
}
