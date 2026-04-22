package storage

import (
	"fmt"
	"time"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/syndtr/goleveldb/leveldb"
	"google.golang.org/protobuf/proto"
)

const (
	ERROR_STORAGE_PREFIX = "error:" // error:<txHash> → StoredErrorData
)

// CachedErrorData lưu error data trong cache
type CachedErrorData struct {
	Data     *pb.StoredErrorData
	CachedAt time.Time
}

// GetCachedAt implement CachedItem interface
func (c *CachedErrorData) GetCachedAt() time.Time {
	return c.CachedAt
}

// RobotTransaction quản lý lưu trữ error data
type RobotTransaction struct {
	db           *leveldb.DB
	cachedWriter *CachedBatchWriter // Gộp cache + batch write
}

// ErrorWriteRequest implement BatchWriteItem
type errorWriteRequest struct {
	txHash string
	data   *pb.StoredErrorData
}

func (e *errorWriteRequest) GetID() string {
	return e.txHash
}

// NewTransactionStorage tạo mới RobotTransaction storage
func NewTransactionStorage(db *leveldb.DB) *RobotTransaction {
	// Serialize function cho error data
	serializeFunc := func(item BatchWriteItem) ([][2][]byte, error) {
		req := item.(*errorWriteRequest)
		key := []byte(fmt.Sprintf("%s%s", ERROR_STORAGE_PREFIX, req.txHash))
		dataBytes, err := proto.Marshal(req.data)
		if err != nil {
			return nil, err
		}
		return [][2][]byte{{key, dataBytes}}, nil
	}

	ts := &RobotTransaction{
		db: db,
		cachedWriter: NewCachedBatchWriter(
			db,
			500,                  // Max cache size: 500 items
			50,                   // Batch 50 errors
			500*time.Millisecond, // Flush sau 500ms
			1000,                 // Buffer 1000 requests
			serializeFunc,
		),
	}

	return ts
}

// SaveError lưu error data (async via channel)
func (ts *RobotTransaction) SaveError(
	txHash string,
	inputData string, // Input data serialized as JSON string
	errorMessage string,
) error {
	// Tạo stored error data
	storedData := &pb.StoredErrorData{
		TxHash:       txHash,
		InputData:    inputData,
		ErrorMessage: errorMessage,
		CreatedAt:    time.Now().Unix(),
	}
	// Update cache ngay lập tức
	ts.updateCache(txHash, storedData)

	// Gửi vào batch writer
	req := &errorWriteRequest{
		txHash: txHash,
		data:   storedData,
	}
	ts.cachedWriter.Write(req)

	return nil
}

// GetErrorByHash lấy error data theo txHash (check cache trước)
func (ts *RobotTransaction) GetErrorByHash(txHashHex string) (*pb.StoredErrorData, error) {
	// 1. Check cache trước
	if cachedItem, ok := ts.cachedWriter.LoadCache(txHashHex); ok {
		cachedData := cachedItem.(*CachedErrorData)
		return cachedData.Data, nil
	}

	// 2. Load từ DB
	key := []byte(fmt.Sprintf("%s%s", ERROR_STORAGE_PREFIX, txHashHex))
	dataBytes, err := ts.db.Get(key, nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return nil, fmt.Errorf("error not found: %s", txHashHex)
		}
		return nil, fmt.Errorf("failed to get error: %w", err)
	}

	var storedData pb.StoredErrorData
	if err := proto.Unmarshal(dataBytes, &storedData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal error data: %w", err)
	}

	// 3. Update cache
	ts.updateCache(txHashHex, &storedData)

	return &storedData, nil
}

// updateCache cập nhật cache
func (ts *RobotTransaction) updateCache(txHash string, data *pb.StoredErrorData) {
	cachedData := &CachedErrorData{
		Data:     data,
		CachedAt: time.Now(),
	}
	ts.cachedWriter.StoreCache(txHash, cachedData)
}

// Close đóng storage và flush tất cả pending writes
func (ts *RobotTransaction) Close() error {
	if ts.cachedWriter != nil {
		return ts.cachedWriter.Close()
	}
	return nil
}

// GetCacheStats trả về thống kê cache
func (ts *RobotTransaction) GetCacheStats() (size int, maxSize int) {
	return ts.cachedWriter.GetCacheSize(), ts.cachedWriter.GetCacheMaxSize()
}
