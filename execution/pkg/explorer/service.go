// explorer/service.go
package explorer

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/goxapian"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types"
)

const (
	defaultNumShards  = 32
	defaultNumWorkers = 32
	defaultQueueSize  = 8192
	maxQueueSize      = 1 << 22 // ~4M jobs
	minQueueSize      = 256
)

// BlockRange không đổi
type BlockRange struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
}

// indexJob đại diện cho một công việc đánh chỉ mục
type indexJob struct {
	Tx          types.Transaction
	Rcpt        types.Receipt
	BlockHeader types.BlockHeader // TỐI ƯU HÓA: Chỉ lưu BlockHeader
}

// ExplorerSearchService quản lý việc đánh chỉ mục và tìm kiếm dữ liệu trên nhiều shard.
type ExplorerSearchService struct {
	dbs    []*goxapian.Database
	qps    []*goxapian.QueryParser
	locks  []sync.Mutex // Mutex cho từng shard
	dbPath string

	numShards     int
	numWorkers    int
	queueCapacity int
	// Hàng đợi cho các công việc đánh chỉ mục
	indexQueue chan indexJob
	// Dùng để quản lý vòng đời của các worker
	wg sync.WaitGroup

	// Mutex để bảo vệ file block_ranges.json và biến indexedBlockRanges
	rangesMu           sync.RWMutex
	rangesFilePath     string
	indexedBlockRanges []BlockRange
}

func normalizeQueueSize(size int) int {
	if size <= 0 {
		return defaultQueueSize
	}
	if size < minQueueSize {
		return minQueueSize
	}
	if size > maxQueueSize {
		return maxQueueSize
	}
	return size
}

func normalizeWorkerCount(count int) int {
	if count <= 0 {
		return defaultNumWorkers
	}
	if count < 1 {
		return 1
	}
	if count > 256 {
		return 256
	}
	return count
}

func NewExplorerSearchService(dbPath string, queueSize int, workerCount int) (*ExplorerSearchService, error) {
	numShards := defaultNumShards
	queueSize = normalizeQueueSize(queueSize)
	workerCount = normalizeWorkerCount(workerCount)

	service := &ExplorerSearchService{
		dbs:                make([]*goxapian.Database, numShards),
		qps:                make([]*goxapian.QueryParser, numShards),
		locks:              make([]sync.Mutex, numShards),
		dbPath:             dbPath,
		numShards:          numShards,
		numWorkers:         workerCount,
		queueCapacity:      queueSize,
		indexQueue:         make(chan indexJob, queueSize),
		rangesFilePath:     filepath.Join(dbPath, "block_ranges.json"),
		indexedBlockRanges: make([]BlockRange, 0),
	}

	for i := 0; i < numShards; i++ {
		shardPath := filepath.Join(dbPath, fmt.Sprintf("shard_%d", i))
		if err := os.MkdirAll(shardPath, 0755); err != nil {
			return nil, fmt.Errorf("could not create directory for shard %d: %v", i, err)
		}

		db, err := goxapian.NewWritableDatabase(shardPath)
		if err != nil {
			for j := 0; j < i; j++ {
				service.dbs[j].Close()
			}
			return nil, fmt.Errorf("could not open database for shard %d: %v", i, err)
		}
		service.dbs[i] = db

		qp := goxapian.NewQueryParser()
		if qp == nil {
			return nil, fmt.Errorf("could not create query parser for shard %d", i)
		}
		qp.SetDatabase(db)
		qp.SetDefaultOp(goxapian.QueryOpOr)
		qp.AddPrefix("hash", "H")
		qp.AddPrefix("from", "F")
		qp.AddPrefix("to", "T")
		qp.AddPrefix("block", "B")
		qp.AddPrefix("token", "K")
		qp.AddPrefix("t_from", "TF")
		qp.AddPrefix("t_to", "TT")
		qp.AddPrefix("r_hash", "RH")
		service.qps[i] = qp
	}

	if err := service.loadBlockRanges(); err != nil {
		fmt.Printf("Warning: could not load block ranges file: %v. Starting with an empty list.\n", err)
	}

	// Khởi động các worker và goroutine giám sát
	service.startWorkers(workerCount)
	go service.monitorQueue() // BỔ SUNG: Chạy goroutine giám sát

	return service, nil
}

// BỔ SUNG: Goroutine để giám sát độ sâu của hàng đợi index.
func (s *ExplorerSearchService) monitorQueue() {
	ticker := time.NewTicker(5 * time.Second) // Báo cáo mỗi 5 giây
	defer ticker.Stop()
	for range ticker.C {
		length := len(s.indexQueue)
		if length > 0 {
			log.Printf("📊 **[DEBUG] Index Queue Depth: %d / %d (%.2f%% full)**", length, s.queueCapacity, float64(length)*100.0/float64(s.queueCapacity))
		}
	}
}

func (s *ExplorerSearchService) Close() error {
	log.Println("🔌 Shutting down ExplorerSearchService...")

	// Bước 1: Ngừng nhận thêm công việc mới
	close(s.indexQueue)
	log.Println("   - Index queue closed. No new jobs will be accepted.")

	// Bước 2: Chờ tất cả các worker xử lý hết công việc còn lại trong hàng đợi
	log.Println("   - Waiting for all indexing workers to finish...")
	s.wg.Wait()
	log.Println("   - All indexing workers have finished.")

	// Bước 3: Đóng các kết nối CSDL
	var firstErr error
	for i := 0; i < s.numShards; i++ {
		s.locks[i].Lock()
		if s.qps[i] != nil {
			s.qps[i].Close()
		}
		if s.dbs[i] != nil {
			s.dbs[i].Close()
		}
		s.locks[i].Unlock()
	}
	log.Println("✅ ExplorerSearchService shut down gracefully.")
	return firstErr
}

// SỬA ĐỔI: Hàm này giờ sẽ bị chặn (block) nếu hàng đợi đầy, thay vì báo lỗi.
// Điều này tạo ra cơ chế điều tiết tự nhiên (backpressure).
func (s *ExplorerSearchService) IndexTransaction(tx types.Transaction, rcpt types.Receipt, blockHeader types.BlockHeader) error {
	job := indexJob{
		Tx:          tx,
		Rcpt:        rcpt,
		BlockHeader: blockHeader, // TỐI ƯU HÓA: Truyền vào block.Header()
	}

	// Gửi job vào hàng đợi. Nếu hàng đợi đầy, goroutine gọi hàm này
	// sẽ tạm dừng tại đây cho đến khi có chỗ trống. Điều này là an toàn và mong muốn.
	s.indexQueue <- job

	return nil
}

// getShardIndex chọn shard cho một key (ví dụ: transaction hash).
func (s *ExplorerSearchService) getShardIndex(key string) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	return int(h.Sum32()) % s.numShards
}

// startWorkers khởi động các goroutine để xử lý công việc từ indexQueue.
func (s *ExplorerSearchService) startWorkers(numWorkers int) {
	s.wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go func() {
			defer s.wg.Done()
			for job := range s.indexQueue {
				if err := s.doIndexTransaction(job); err != nil {
					logger.Error("Failed to index transaction", "hash", job.Tx.Hash().Hex(), "error", err)
				}
			}
		}()
	}
}

// Commit lưu các thay đổi vào tất cả các shard.
func (s *ExplorerSearchService) Commit() error {
	var commitWg sync.WaitGroup
	commitWg.Add(s.numShards)

	for i := 0; i < s.numShards; i++ {
		go func(shardIndex int) {
			defer commitWg.Done()
			s.locks[shardIndex].Lock()
			defer s.locks[shardIndex].Unlock()
			if s.dbs[shardIndex] != nil {
				s.dbs[shardIndex].Commit()
			}
		}(i)
	}

	commitWg.Wait()
	return nil
}

// doIndexTransaction là hàm thực thi việc đánh chỉ mục, được gọi bởi các worker.
func (s *ExplorerSearchService) doIndexTransaction(job indexJob) error {
	hash := strings.ToLower(job.Tx.Hash().Hex())
	shardIndex := s.getShardIndex(hash)

	s.locks[shardIndex].Lock()
	defer s.locks[shardIndex].Unlock()

	db := s.dbs[shardIndex]
	if db == nil {
		return fmt.Errorf("database for shard %d is not initialized or already closed", shardIndex)
	}

	doc := goxapian.NewDocument()
	if doc == nil {
		return errors.New("failed to create new Xapian document")
	}
	defer doc.Close()

	fromAddr := strings.ToLower(job.Tx.FromAddress().Hex())
	toAddr := strings.ToLower(job.Tx.ToAddress().Hex())
	rHash := strings.ToLower(job.Rcpt.RHash().Hex())

	doc.AddTerm("H" + hash)
	doc.AddTerm("F" + fromAddr)
	if toAddr != "0x0000000000000000000000000000000000000000" {
		doc.AddTerm("T" + toAddr)
	}
	doc.AddTerm("B" + strconv.FormatUint(job.BlockHeader.BlockNumber(), 10)) // TỐI ƯU HÓA
	doc.AddTerm("RH" + rHash)

	if job.Tx.IsCallContract() {
		tokenContractAddr := toAddr
		if tokenData, ok := ParseERC20Transfer(job.Tx.FromAddress(), job.Tx.CallData().Input()); ok {
			tokenFrom := strings.ToLower(tokenData.From.Hex())
			tokenTo := strings.ToLower(tokenData.To.Hex())
			doc.AddTerm("K" + tokenContractAddr)
			doc.AddTerm("TF" + tokenFrom)
			doc.AddTerm("TT" + tokenTo)
		}
	}

	explorerTx := NewExplorerTransaction(job.Tx, job.Rcpt, job.BlockHeader) // TỐI ƯU HÓA
	jsonData, err := explorerTx.ToJSONString()
	if err != nil {
		return fmt.Errorf("could not marshal explorer transaction to JSON: %w", err)
	}
	doc.SetData(jsonData)

	uniqueTerm := "H" + hash // Sử dụng term định danh duy nhất dựa trên hash
	db.ReplaceDocumentByTerm(uniqueTerm, doc)
	return nil
}

// SearchTransactions thực hiện tìm kiếm trên tất cả các shard và tổng hợp kết quả.
func (s *ExplorerSearchService) SearchTransactions(queryStr string, offset, limit int) ([]string, uint, error) {
	var totalResults uint
	var allDocs []string
	var mu sync.Mutex

	var searchWg sync.WaitGroup
	searchWg.Add(s.numShards)

	features := []goxapian.QueryParserFeature{
		goxapian.FeatureBoolean,
		goxapian.FeatureBooleanAnyCase,
		goxapian.FeaturePhrase,
		goxapian.FeatureWildcard,
	}

	for i := 0; i < s.numShards; i++ {
		go func(shardIndex int) {
			defer searchWg.Done()

			s.locks[shardIndex].Lock()
			defer s.locks[shardIndex].Unlock()

			qp := s.qps[shardIndex]
			db := s.dbs[shardIndex]
			if db == nil || qp == nil {
				logger.Error("Search service is not initialized for shard", "shardIndex", shardIndex)
				return
			}

			query := qp.ParseQuery(queryStr, features...)
			if query == nil {
				return
			}
			defer query.Close()

			enquire := db.Enquire()
			if enquire == nil {
				return
			}
			defer enquire.Close()

			enquire.SetQuery(query)
			mset := enquire.GetMSet(0, uint(offset+limit))
			if mset == nil {
				return
			}
			defer mset.Close()

			shardTotal := mset.GetMatchesEstimated()
			var shardDocs []string
			for i := 0; i < mset.GetSize(); i++ {
				doc := mset.GetDocument(uint(i))
				if doc != nil {
					shardDocs = append(shardDocs, doc.GetData())
					doc.Close()
				}
			}

			mu.Lock()
			totalResults += shardTotal
			allDocs = append(allDocs, shardDocs...)
			mu.Unlock()
		}(i)
	}

	searchWg.Wait()

	end := offset + limit
	if end > len(allDocs) {
		end = len(allDocs)
	}
	if offset > len(allDocs) {
		offset = len(allDocs)
	}

	return allDocs[offset:end], totalResults, nil
}

// Các hàm quản lý block range
func (s *ExplorerSearchService) AddBlockToIndexRanges(blockNumber uint64) error {
	s.rangesMu.Lock()
	defer s.rangesMu.Unlock()
	s.indexedBlockRanges = append(s.indexedBlockRanges, BlockRange{Start: blockNumber, End: blockNumber})
	return s.mergeAndSaveBlockRanges()
}

func (s *ExplorerSearchService) mergeAndSaveBlockRanges() error {
	if len(s.indexedBlockRanges) <= 1 {
		return s.saveBlockRanges()
	}
	sort.Slice(s.indexedBlockRanges, func(i, j int) bool {
		return s.indexedBlockRanges[i].Start < s.indexedBlockRanges[j].Start
	})
	merged := []BlockRange{s.indexedBlockRanges[0]}
	last := 0
	for i := 1; i < len(s.indexedBlockRanges); i++ {
		current := s.indexedBlockRanges[i]
		if current.Start <= merged[last].End+1 {
			if current.End > merged[last].End {
				merged[last].End = current.End
			}
		} else {
			merged = append(merged, current)
			last++
		}
	}
	s.indexedBlockRanges = merged
	return s.saveBlockRanges()
}

func (s *ExplorerSearchService) saveBlockRanges() error {
	data, err := json.MarshalIndent(s.indexedBlockRanges, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling block ranges: %w", err)
	}
	return os.WriteFile(s.rangesFilePath, data, 0644)
}

func (s *ExplorerSearchService) loadBlockRanges() error {
	s.rangesMu.RLock()
	defer s.rangesMu.RUnlock()
	if _, err := os.Stat(s.rangesFilePath); os.IsNotExist(err) {
		return nil
	}
	data, err := os.ReadFile(s.rangesFilePath)
	if err != nil {
		return fmt.Errorf("error reading block ranges file: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, &s.indexedBlockRanges)
}

func (s *ExplorerSearchService) GetIndexedBlockRanges() []BlockRange {
	s.rangesMu.RLock()
	defer s.rangesMu.RUnlock()
	rangesCopy := make([]BlockRange, len(s.indexedBlockRanges))
	copy(rangesCopy, s.indexedBlockRanges)
	return rangesCopy
}

func (s *ExplorerSearchService) GetMissingBlockRanges() []BlockRange {
	s.rangesMu.RLock()
	defer s.rangesMu.RUnlock()

	if len(s.indexedBlockRanges) < 1 {
		return nil
	}

	sortedRanges := make([]BlockRange, len(s.indexedBlockRanges))
	copy(sortedRanges, s.indexedBlockRanges)
	sort.Slice(sortedRanges, func(i, j int) bool {
		return sortedRanges[i].Start < sortedRanges[j].Start
	})

	var missingRanges []BlockRange
	lastIndexedBlock := uint64(0)
	if sortedRanges[0].Start > 1 {
		missingRanges = append(missingRanges, BlockRange{Start: 1, End: sortedRanges[0].Start - 1})
	}
	lastIndexedBlock = sortedRanges[0].End

	for i := 1; i < len(sortedRanges); i++ {
		nextRange := sortedRanges[i]
		if nextRange.Start > lastIndexedBlock+1 {
			missingRanges = append(missingRanges, BlockRange{Start: lastIndexedBlock + 1, End: nextRange.Start - 1})
		}
		if nextRange.End > lastIndexedBlock {
			lastIndexedBlock = nextRange.End
		}
	}
	logger.Info("missingRanges %v", missingRanges)
	return missingRanges
}

func (s *ExplorerSearchService) GetTransactionsAndTPSInRange(startBlock, endBlock uint64) (int, float64, error) {
	if startBlock > endBlock {
		return 0, 0, fmt.Errorf("block bắt đầu %d không thể lớn hơn block kết thúc %d", startBlock, endBlock)
	}

	var queryParts []string
	for i := startBlock; i <= endBlock; i++ {
		queryParts = append(queryParts, "block:"+strconv.FormatUint(i, 10))
	}
	if len(queryParts) == 0 {
		return 0, 0, nil
	}
	queryString := strings.Join(queryParts, " OR ")

	results, total, err := s.SearchTransactions(queryString, 0, 1000000)
	if err != nil {
		return 0, 0, err
	}

	if total == 0 {
		return 0, 0, nil
	}

	var minTimestamp, maxTimestamp uint64
	minTimestamp = ^uint64(0)

	for _, res := range results {
		var data struct {
			Timestamp uint64 `json:"timestamp"`
		}
		if err := json.Unmarshal([]byte(res), &data); err == nil {
			if data.Timestamp < minTimestamp {
				minTimestamp = data.Timestamp
			}
			if data.Timestamp > maxTimestamp {
				maxTimestamp = data.Timestamp
			}
		}
	}

	if minTimestamp > maxTimestamp {
		return int(total), 0, nil
	}

	duration := maxTimestamp - minTimestamp
	if duration <= 0 {
		duration = 1
	}

	tps := float64(total) / float64(duration)
	return int(total), tps, nil
}
