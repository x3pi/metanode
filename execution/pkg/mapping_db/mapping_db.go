package mapping_db

import (
	"fmt"
	"log"
	"sync"

	"github.com/ethereum/go-ethereum/common"

	// Import types
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

type MappingDb struct {
	db storage.Storage
}

var (
	mappingDbInstance *MappingDb
	once              sync.Once
)

func NewMappingDb(db storage.Storage) *MappingDb {
	once.Do(func() {
		mappingDbInstance = &MappingDb{
			db: db,
		}
	})
	return mappingDbInstance
}

func GetMappingDb() *MappingDb {
	once.Do(func() {
		if mappingDbInstance == nil {
			log.Fatal("FATAL: MappingDB chưa được khởi tạo. Gọi NewMappingDb() trước.")
		}
	})
	return mappingDbInstance
}

func (db *MappingDb) ReturnDB() storage.Storage {
	return db.db
}

// Lưu blockNumber -> blockHash trực tiếp dưới dạng bytes
func (db *MappingDb) SaveBlockNumberToHash(blockNumber uint64, blockHash common.Hash) error {
	key := []byte(fmt.Sprintf("%d", blockNumber)) // Tạo key từ blockNumber
	err := db.db.Put(key, blockHash.Bytes())      // Lưu blockHash dưới dạng bytes
	if err != nil {
		return fmt.Errorf("failed to put blockHash to db: %w", err)
	}
	return nil
}

// Lấy block hash từ block number
func (db *MappingDb) GetBlockHashByNumber(blockNumber uint64) (common.Hash, bool) {
	key := []byte(fmt.Sprintf("%d", blockNumber)) // Tạo khóa từ blockNumber
	data, err := db.db.Get(key)                   // Lấy dữ liệu từ db theo khóa
	if err != nil || data == nil || len(data) != common.HashLength {
		return common.Hash{}, false // Trả về giá trị rỗng nếu không tìm thấy hoặc lỗi
	}

	return common.BytesToHash(data), true // Chuyển đổi []byte thành common.Hash
}
