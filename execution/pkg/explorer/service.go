// explorer/service.go
package explorer

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/goxapian"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types"
)

const (
	defaultNumShards  = 32
	defaultNumWorkers = 32
	defaultQueueSize  = 65536
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
	BlockHash   string
	Replace     bool
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

	// OOM PREVENTION: Track dropped jobs when queue is full.
	// Explorer indexing is non-critical — dropping is better than blocking pipeline.
	droppedJobs atomic.Int64

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
		droppedTotal := s.droppedJobs.Load()
		if length > 0 || droppedTotal > 0 {
			log.Printf("📊 **[DEBUG] Index Queue Depth: %d / %d (%.2f%% full) | Dropped: %d**", length, s.queueCapacity, float64(length)*100.0/float64(s.queueCapacity), droppedTotal)
		}
	}
}

// QueueDepth returns the current number of pending jobs in indexQueue.
func (s *ExplorerSearchService) QueueDepth() int {
	return len(s.indexQueue)
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
			if err := s.dbs[i].Commit(); err != nil && firstErr == nil {
				firstErr = err
			}
			s.dbs[i].Close()
		}
		s.locks[i].Unlock()
	}
	log.Println("✅ ExplorerSearchService shut down gracefully.")
	return firstErr
}

// OOM PREVENTION: Non-blocking IndexTransaction with drop policy.
// When queue is full, jobs are DROPPED instead of blocking the block processing pipeline.
// Explorer indexing is non-critical (doesn't affect consensus/execution).
// Dropped transactions can be re-indexed offline via the missing block ranges mechanism.
func (s *ExplorerSearchService) IndexTransaction(tx types.Transaction, rcpt types.Receipt, blockHeader types.BlockHeader) error {
	return s.IndexTransactionFast(tx, rcpt, blockHeader, "", true)
}

// IndexTransactionFast enqueues an indexing job with an optional precomputed
// blockHashHex and replace flag. When replace is false (newly committed blocks),
// the worker calls Xapian add_document directly instead of performing a random
// B-tree lookup via replace_document.
func (s *ExplorerSearchService) IndexTransactionFast(
	tx types.Transaction,
	rcpt types.Receipt,
	blockHeader types.BlockHeader,
	blockHashHex string,
	replace bool,
) error {
	job := indexJob{
		Tx:          tx,
		Rcpt:        rcpt,
		BlockHeader: blockHeader,
		BlockHash:   blockHashHex,
		Replace:     replace,
	}

	// Fast non-blocking send when queue has room (zero timer allocation on hot path).
	select {
	case s.indexQueue <- job:
		return nil
	default:
	}

	// Queue is momentarily full: pace the background indexing producer (up to 50ms)
	// so bursts from in-memory pendingIndexBlocks match the 32 Xapian workers' drain rate
	// without dropping jobs, while still failing open if Xapian shards are stalled.
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case s.indexQueue <- job:
		return nil
	case <-timer.C:
		dropped := s.droppedJobs.Add(1)
		// Log every 100th drop to avoid log spam
		if dropped%100 == 1 {
			logger.Warn("⚠️ [EXPLORER] Index queue full (%d/%d). Dropped %d index jobs total. TX %s will not be searchable until re-indexed.",
				len(s.indexQueue), s.queueCapacity, dropped, tx.Hash().Hex())
		}
		return fmt.Errorf("explorer index queue full, job dropped (total dropped: %d)", dropped)
	}
}

// getShardIndex chọn shard cho một key (ví dụ: transaction hash) sử dụng FNV-1a 32-bit zero-alloc.
func (s *ExplorerSearchService) getShardIndex(key string) int {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return int(h) % s.numShards
}

const maxWorkerBatchDrain = 64

// startWorkers khởi động các goroutine để xử lý công việc từ indexQueue.
func (s *ExplorerSearchService) startWorkers(numWorkers int) {
	s.wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go func() {
			defer s.wg.Done()
			var batchJobs []indexJob
			for firstJob := range s.indexQueue {
				batchJobs = append(batchJobs[:0], firstJob)
			drainLoop:
				for len(batchJobs) < maxWorkerBatchDrain {
					select {
					case nextJob, ok := <-s.indexQueue:
						if !ok {
							break drainLoop
						}
						batchJobs = append(batchJobs, nextJob)
					default:
						break drainLoop
					}
				}

				if len(batchJobs) == 1 {
					if err := s.doIndexTransaction(batchJobs[0]); err != nil {
						txHash := "<nil>"
						if batchJobs[0].Tx != nil {
							txHash = batchJobs[0].Tx.Hash().Hex()
						}
						logger.Error("Failed to index transaction", "hash", txHash, "error", err)
					}
					clear(batchJobs)
					continue
				}

				s.doIndexTransactionsBatch(batchJobs)
				clear(batchJobs)
			}
		}()
	}
}

// Commit lưu các thay đổi vào tất cả các shard.
func (s *ExplorerSearchService) Commit() error {
	var commitWg sync.WaitGroup
	commitWg.Add(s.numShards)

	// 2026-09 mục 22: this used to always `return nil` no matter what
	// happened in the per-shard goroutines below -- goxapian.Database.Commit()
	// itself used to be unable to report failure at all, so this was doubly
	// silent. Both are fixed now; collect per-shard errors under a mutex
	// (concurrent goroutines can't safely append to a shared slice/error
	// without one) and return them joined so a caller actually finds out a
	// shard's commit failed instead of believing every shard was flushed.
	var mu sync.Mutex
	var errs []error

	for i := 0; i < s.numShards; i++ {
		go func(shardIndex int) {
			defer commitWg.Done()
			s.locks[shardIndex].Lock()
			defer s.locks[shardIndex].Unlock()
			if s.dbs[shardIndex] != nil {
				if err := s.dbs[shardIndex].Commit(); err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("shard %d: %w", shardIndex, err))
					mu.Unlock()
				}
			}
		}(i)
	}

	commitWg.Wait()
	return errors.Join(errs...)
}

// prepareIndexDoc tách toàn bộ việc chuẩn bị dữ liệu (hex, terms, JSON, shard hash) ra ngoài shard mutex.
func (s *ExplorerSearchService) prepareIndexDoc(job indexJob) (int, goxapian.BatchDocumentInput, error) {
	if job.Tx == nil {
		return 0, goxapian.BatchDocumentInput{}, errors.New("transaction is nil")
	}

	txHashHex := job.Tx.Hash().Hex()
	hash := strings.ToLower(txHashHex)
	shardIndex := s.getShardIndex(hash)

	fromHex := job.Tx.FromAddress().Hex()
	fromAddr := strings.ToLower(fromHex)
	toAddress := job.Tx.ToAddress()
	var toHex, toAddr string
	if toAddress != (common.Address{}) {
		toHex = toAddress.Hex()
		toAddr = strings.ToLower(toHex)
	}
	var rHashHex, rHash string
	if job.Rcpt != nil {
		rHashHex = job.Rcpt.RHash().Hex()
		rHash = strings.ToLower(rHashHex)
	}

	uniqueTerm := "H" + hash

	var termsBuf strings.Builder
	termsBuf.Grow(256)
	termsBuf.WriteString(uniqueTerm)
	termsBuf.WriteByte('\n')
	termsBuf.WriteString("F")
	termsBuf.WriteString(fromAddr)
	if toAddr != "" {
		termsBuf.WriteByte('\n')
		termsBuf.WriteString("T")
		termsBuf.WriteString(toAddr)
	}
	if job.BlockHeader != nil {
		termsBuf.WriteByte('\n')
		termsBuf.WriteString("B")
		termsBuf.WriteString(strconv.FormatUint(job.BlockHeader.BlockNumber(), 10))
	}
	if rHash != "" {
		termsBuf.WriteByte('\n')
		termsBuf.WriteString("RH")
		termsBuf.WriteString(rHash)
	}

	var tokenData *ERC20TransferData
	if job.Tx.IsCallContract() && job.Tx.CallData() != nil {
		if td, ok := ParseERC20Transfer(job.Tx.FromAddress(), job.Tx.CallData().Input()); ok {
			tokenData = td
			tokenFrom := strings.ToLower(td.From.Hex())
			tokenTo := strings.ToLower(td.To.Hex())
			termsBuf.WriteByte('\n')
			termsBuf.WriteString("K")
			termsBuf.WriteString(toAddr)
			termsBuf.WriteByte('\n')
			termsBuf.WriteString("TF")
			termsBuf.WriteString(tokenFrom)
			termsBuf.WriteByte('\n')
			termsBuf.WriteString("TT")
			termsBuf.WriteString(tokenTo)
		}
	}

	var jsonData string
	if job.BlockHeader != nil && job.Rcpt != nil {
		jsonBytes, err := marshalExplorerTxFull(
			job.Tx,
			job.Rcpt,
			job.BlockHeader,
			job.BlockHash,
			txHashHex,
			fromHex,
			toAddress,
			toHex,
			rHashHex,
			tokenData,
			true,
		)
		if err != nil {
			return 0, goxapian.BatchDocumentInput{}, fmt.Errorf("could not marshal explorer transaction to JSON: %w", err)
		}
		jsonData = string(jsonBytes)
	} else {
		explorerTx := NewExplorerTransaction(job.Tx, job.Rcpt, job.BlockHeader)
		var err error
		jsonData, err = explorerTx.ToJSONString()
		if err != nil {
			return 0, goxapian.BatchDocumentInput{}, fmt.Errorf("could not marshal explorer transaction to JSON: %w", err)
		}
	}

	return shardIndex, goxapian.BatchDocumentInput{
		UniqueTerm:      uniqueTerm,
		Data:            jsonData,
		TermsPacked:     termsBuf.String(),
		ReplaceExisting: job.Replace,
	}, nil
}

// doIndexTransaction là hàm thực thi việc đánh chỉ mục đơn lẻ.
func (s *ExplorerSearchService) doIndexTransaction(job indexJob) error {
	shardIndex, doc, err := s.prepareIndexDoc(job)
	if err != nil {
		return err
	}

	s.locks[shardIndex].Lock()
	db := s.dbs[shardIndex]
	if db == nil {
		s.locks[shardIndex].Unlock()
		return fmt.Errorf("database for shard %d is not initialized or already closed", shardIndex)
	}
	err = db.IndexDocument(doc.UniqueTerm, doc.Data, doc.TermsPacked, doc.ReplaceExisting)
	s.locks[shardIndex].Unlock()
	if err != nil {
		return fmt.Errorf("could not index transaction %s into shard %d: %w", doc.UniqueTerm, shardIndex, err)
	}
	return nil
}

// doIndexTransactionsBatch gom lô các tài liệu theo shardIndex và chỉ lock mỗi shard 1 lần duy nhất.
func (s *ExplorerSearchService) doIndexTransactionsBatch(jobs []indexJob) {
	shardDocs := make([][]goxapian.BatchDocumentInput, s.numShards)

	for _, job := range jobs {
		shardIndex, doc, err := s.prepareIndexDoc(job)
		if err != nil {
			txHash := "<nil>"
			if job.Tx != nil {
				txHash = job.Tx.Hash().Hex()
			}
			logger.Error("Failed to prepare index doc", "hash", txHash, "error", err)
			continue
		}
		shardDocs[shardIndex] = append(shardDocs[shardIndex], doc)
	}

	for shardIdx, docs := range shardDocs {
		if len(docs) == 0 {
			continue
		}
		s.locks[shardIdx].Lock()
		db := s.dbs[shardIdx]
		if db == nil {
			s.locks[shardIdx].Unlock()
			logger.Error("Database for shard is not initialized or closed", "shard", shardIdx)
			continue
		}
		err := db.IndexDocumentsBatch(docs)
		s.locks[shardIdx].Unlock()
		if err != nil {
			logger.Error("Failed to batch index into shard", "shard", shardIdx, "count", len(docs), "error", err)
		}
	}
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
