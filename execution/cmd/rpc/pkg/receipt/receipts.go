package receipt

import (
	"errors"
	"fmt"
	"log"
	"runtime"
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
	trie            *trie.MerklePatriciaTrie
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

func NewReceipts(db storage.Storage) types.Receipts {
	trie, err := trie.New(trie.EmptyRootHash, db, true)
	if err != nil {
		panic(err) // Có thể thay bằng việc trả lỗi nếu cần
	}
	return &Receipts{
		trie:           trie,
		db:             db,
		originRootHash: trie.Hash(),
		dirtyReceipts:  make(map[common.Hash]types.Receipt),
	}
}

// NewReceiptsFromRoot khởi tạo một đối tượng Receipts từ một root hash đã có.
// Hàm này cho phép bạn "load" lại trạng thái của tất cả các biên lai tại một thời điểm cụ thể.
func NewReceiptsFromRoot(root common.Hash, db storage.Storage) (types.Receipts, error) {
	trie, err := trie.New(root, db, true)
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
		return nil, ErrorReceiptNotFound
	}
	var receipt = &Receipt{}

	err = receipt.Unmarshal(data)
	if err != nil {
		return nil, err
	}
	return receipt, nil
}

func (r *Receipts) Discard() error {
	r.dirtyReceipts = make(map[common.Hash]types.Receipt)
	trie, err := trie.New(r.originRootHash, r.db, true)
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
		return common.Hash{}, fmt.Errorf(
			"hash gốc không khớp sau khi tính toán commit (intermediate: %s, commit: %s)",
			intermediateHash, committedHash,
		)
	}
	finalHash := committedHash

	// Giai đoạn 3: Song song hóa việc tạo batch từ nodeSet và ghi xuống DB.
	if nodeSet != nil && len(nodeSet.Nodes) > 0 {
		dbPhaseStart := time.Now()
		log.Printf("Bắt đầu xử lý %d node của trie receipts...", len(nodeSet.Nodes))

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
		log.Printf("✅ Tạo batch cho %d node hoàn tất trong %v", len(batch), time.Since(dbPhaseStart))

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
			log.Printf("✅ Ghi DB và Tuần tự hóa Batch hoàn tất trong %v", time.Since(dbPhaseStart))
		}
	} else {
		log.Printf("Không có node trie receipt mới nào để commit.")
	}

	// Giai đoạn 4: Tạo một instance trie mới với root hash đã được commit.
	newTrie, err := trie.New(finalHash, r.db, true)
	if err != nil {
		return common.Hash{}, fmt.Errorf("không thể tải trie mới cho root %s sau khi commit: %w", finalHash, err)
	}

	// Giai đoạn 5: Cập nhật trie và dọn dẹp.
	r.trie = newTrie
	r.originRootHash = finalHash

	log.Printf("🚀 Toàn bộ thời gian thực thi Commit Receipt: %v", time.Since(totalTimeStart))

	return finalHash, nil
}

func (r *Receipts) IntermediateRoot() (common.Hash, error) {
	if len(r.dirtyReceipts) == 0 {
		return r.trie.Hash(), nil
	}

	for _, receipt := range r.dirtyReceipts {
		logger.Error(receipt.TransactionHash())
		b, err := receipt.Marshal()
		if err != nil {
			return common.Hash{}, err
		}
		if err = r.trie.Update(receipt.TransactionHash().Bytes(), b); err != nil {
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
	pbReceipts := &pb.Receipts{
		Receipts: make([]*pb.Receipt, len(receipts)),
	}

	for i, r := range receipts {
		if receipt, ok := r.(*Receipt); ok {
			pbReceipt, ok := receipt.Proto().(*pb.Receipt)
			if !ok {
				return nil, fmt.Errorf("không thể khẳng định kiểu cho receipt tại chỉ số %d", i)
			}
			pbReceipts.Receipts[i] = pbReceipt
		} else {
			return nil, fmt.Errorf("phần tử tại chỉ số %d không phải là *Receipt", i)
		}
	}

	return proto.Marshal(pbReceipts)
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
