package storage

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/syndtr/goleveldb/leveldb"
	"google.golang.org/protobuf/proto"
)

const (
	// Key prefixes for indexing
	PREFIX_ARTIFACT_DATA  = "artifact:" // artifact:<artifact_id> -> ArtifactData
	PREFIX_ADDRESS_TO_ID  = "addr:"     // addr:<contract_address> -> artifact_id
	PREFIX_BYTECODE_TO_ID = "bytecode:" // bytecode:<bytecode_hash> -> artifact_id
)

// artifactWriteRequest implement BatchWriteItem - dùng để batch write artifact data
type artifactWriteRequest struct {
	artifactData *pb.ArtifactData
}

// GetID trả về artifact_id để dùng làm key trong batch writer
// Batch writer dùng ID này để tránh duplicate: nếu cùng ID được gửi nhiều lần,
// chỉ giữ lại item cuối cùng (overwrite) trong pending map
func (a *artifactWriteRequest) GetID() string {
	return a.artifactData.ArtifactId
}

// CachedArtifactData lưu artifact data trong cache
type CachedArtifactData struct {
	Data     *pb.ArtifactData
	CachedAt time.Time
}

// GetCachedAt implement CachedItem interface
func (c *CachedArtifactData) GetCachedAt() time.Time {
	return c.CachedAt
}

// ArtifactStorage quản lý lưu trữ artifact data với cache
type ArtifactStorage struct {
	db           *leveldb.DB
	cachedWriter *CachedBatchWriter // Gộp cache + batch write
}

// NewArtifactStorage tạo mới ArtifactStorage
func NewArtifactStorage(db *leveldb.DB) *ArtifactStorage {
	// Serialize function cho artifact data (cần write nhiều keys: artifact data + indexes)
	serializeFunc := func(item BatchWriteItem) ([][2][]byte, error) {
		req := item.(*artifactWriteRequest)
		artifactData := req.artifactData
		var kvPairs [][2][]byte

		// 1. Artifact data chính
		artifactKey := []byte(fmt.Sprintf("%s%s", PREFIX_ARTIFACT_DATA, artifactData.ArtifactId))
		artifactBytes, err := proto.Marshal(artifactData)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal artifact data: %w", err)
		}
		kvPairs = append(kvPairs, [2][]byte{artifactKey, artifactBytes})

		// 2. Index: ContractAddress -> artifact_id
		if artifactData.ContractAddress != "" {
			addrKey := []byte(fmt.Sprintf("%s%s", PREFIX_ADDRESS_TO_ID, artifactData.ContractAddress))
			kvPairs = append(kvPairs, [2][]byte{addrKey, []byte(artifactData.ArtifactId)})
		}

		// 3. Index: BytecodeHash -> artifact_id
		if artifactData.BytecodeHash != "" {
			bytecodeKey := []byte(fmt.Sprintf("%s%s", PREFIX_BYTECODE_TO_ID, artifactData.BytecodeHash))
			kvPairs = append(kvPairs, [2][]byte{bytecodeKey, []byte(artifactData.ArtifactId)})
		}

		return kvPairs, nil
	}

	return &ArtifactStorage{
		db: db,
		cachedWriter: NewCachedBatchWriter(
			db,
			500,                  // Max cache size: 500 items
			50,                   // Batch 50 artifacts
			500*time.Millisecond, // Flush sau 500ms
			1000,                 // Buffer 1000 requests
			serializeFunc,
		),
	}
}

// CalculateArtifactID tính toán artifact_id từ bytecode hash và các compiler settings
func CalculateArtifactID(
	bytecodeHash string,
	solcVersion string,
	optimizerSettings string,
	evmVersion string,
	linkedLibraries string,
) string {
	// Concatenate all components
	data := bytecodeHash + solcVersion + optimizerSettings + evmVersion + linkedLibraries
	hash := crypto.Keccak256([]byte(data))
	return "0x" + hex.EncodeToString(hash)
}

// SaveArtifact lưu artifact data và các index mappings (async via batch writer)
func (as *ArtifactStorage) SaveArtifact(artifactData *pb.ArtifactData) error {
	// Update cache ngay lập tức
	as.updateCache(artifactData.ArtifactId, artifactData)

	// Gửi vào batch writer
	req := &artifactWriteRequest{
		artifactData: artifactData,
	}
	as.cachedWriter.Write(req)

	logger.Info("✅ Artifact queued: artifact_id=%s, contract=%s", artifactData.ArtifactId, artifactData.ContractAddress)
	return nil
}

// GetArtifactByID lấy artifact data theo artifact_id
func (as *ArtifactStorage) GetArtifactByID(artifactID string) (*pb.ArtifactData, error) {
	// 1. Check cache trước
	if cachedItem, ok := as.cachedWriter.LoadCache(artifactID); ok {
		cachedData := cachedItem.(*CachedArtifactData)
		return cachedData.Data, nil
	}

	// 2. Load từ DB
	key := []byte(fmt.Sprintf("%s%s", PREFIX_ARTIFACT_DATA, artifactID))
	dataBytes, err := as.db.Get(key, nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return nil, fmt.Errorf("artifact not found: %s", artifactID)
		}
		return nil, fmt.Errorf("failed to get artifact: %w", err)
	}

	var artifactData pb.ArtifactData
	if err := proto.Unmarshal(dataBytes, &artifactData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal artifact data: %w", err)
	}

	// 3. Update cache
	as.updateCache(artifactID, &artifactData)

	return &artifactData, nil
}

// GetArtifactIDByAddress lấy artifact_id từ contract address
func (as *ArtifactStorage) GetArtifactIDByAddress(contractAddress string) (string, error) {
	key := []byte(fmt.Sprintf("%s%s", PREFIX_ADDRESS_TO_ID, contractAddress))
	data, err := as.db.Get(key, nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return "", fmt.Errorf("artifact_id not found for address: %s", contractAddress)
		}
		return "", fmt.Errorf("failed to get artifact_id: %w", err)
	}
	return string(data), nil
}

// GetArtifactIDByBytecodeHash lấy artifact_id từ bytecode hash
func (as *ArtifactStorage) GetArtifactIDByBytecodeHash(bytecodeHash string) (string, error) {
	key := []byte(fmt.Sprintf("%s%s", PREFIX_BYTECODE_TO_ID, bytecodeHash))
	data, err := as.db.Get(key, nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return "", fmt.Errorf("artifact_id not found for bytecode_hash: %s", bytecodeHash)
		}
		return "", fmt.Errorf("failed to get artifact_id: %w", err)
	}
	return string(data), nil
}

// GetArtifactByAddress lấy artifact data từ contract address
func (as *ArtifactStorage) GetArtifactByAddress(contractAddress string) (*pb.ArtifactData, error) {
	artifactID, err := as.GetArtifactIDByAddress(contractAddress)
	if err != nil {
		return nil, err
	}
	return as.GetArtifactByID(artifactID)
}

// GetArtifactByBytecodeHash lấy artifact data từ bytecode hash
func (as *ArtifactStorage) GetArtifactByBytecodeHash(bytecodeHash string) (*pb.ArtifactData, error) {
	artifactID, err := as.GetArtifactIDByBytecodeHash(bytecodeHash)
	if err != nil {
		return nil, err
	}
	return as.GetArtifactByID(artifactID)
}

// updateCache cập nhật cache
func (as *ArtifactStorage) updateCache(artifactID string, data *pb.ArtifactData) {
	cachedData := &CachedArtifactData{
		Data:     data,
		CachedAt: time.Now(),
	}
	as.cachedWriter.StoreCache(artifactID, cachedData)
}

// Close đóng storage và flush tất cả pending writes
func (as *ArtifactStorage) Close() error {
	if as.cachedWriter != nil {
		if err := as.cachedWriter.Close(); err != nil {
			return err
		}
	}
	return as.db.Close()
}

// GetCacheStats trả về thống kê cache
func (as *ArtifactStorage) GetCacheStats() (size int, maxSize int) {
	return as.cachedWriter.GetCacheSize(), as.cachedWriter.GetCacheMaxSize()
}

// CalculateBytecodeHash tính hash của bytecode
func CalculateBytecodeHash(bytecodeHex string) (string, error) {
	// Remove 0x prefix if present
	if len(bytecodeHex) >= 2 && bytecodeHex[:2] == "0x" {
		bytecodeHex = bytecodeHex[2:]
	}
	bytecode, err := hex.DecodeString(bytecodeHex)
	if err != nil {
		return "", fmt.Errorf("invalid bytecode hex: %w", err)
	}

	hash := crypto.Keccak256Hash(bytecode)
	return hash.Hex(), nil
}
