package trie_database

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// TrieDatabaseStatus đại diện cho trạng thái của TrieDatabase.
type TrieDatabaseStatus int

const (
	Committed TrieDatabaseStatus = iota // 0: Đã commit (mặc định)
	Deleted                             // 1: Đã xóa
	Reverted                            // 2: Đã hoàn nguyên
)

type TrieDatabase struct {
	trieR          p_trie.StateTrie
	originRootHash common.Hash
	db             storage.Storage
	dirtyData      sync.Map
	mu             sync.Mutex
	address        common.Address
	mvmId          common.Address
	dbName         string
	accountStateDB *account_state_db.AccountStateDB
	status         TrieDatabaseStatus // Trạng thái của TrieDatabase
	backUpDb       []byte
	subPath        string
}

func NewTrieDatabase(
	hash common.Hash,
	db storage.Storage,
	mvmId common.Address,
	address common.Address,
	dbName string,
	accountStateDB *account_state_db.AccountStateDB,

) *TrieDatabase {

	var trieR p_trie.StateTrie
	var err error

	if (hash == common.Hash{}) {
		trieR, err = p_trie.NewStateTrie(common.Hash{}, db, false)
	} else {
		// Thử tối đa 3 lần với độ trễ giữa các lần thử
		maxRetries := 3
		for i := 0; i < maxRetries; i++ {
			trieR, err = p_trie.NewStateTrie(hash, db, false)
			if err == nil {
				break
			}
			// Nếu không phải lần thử cuối cùng, đợi trước khi thử lại
			if i < maxRetries-1 {
				time.Sleep(100 * time.Millisecond)
			}
		}

	}
	if err != nil {
		logger.Error("Error creating trie: %v", err)
		return nil
	}
	subPath := filepath.Join(address.String(), dbName)

	return &TrieDatabase{
		trieR:          trieR,
		db:             db,
		originRootHash: trieR.Hash(),
		dirtyData:      sync.Map{},
		address:        address,
		mvmId:          mvmId,
		dbName:         dbName,
		accountStateDB: accountStateDB,
		status:         Committed, // Mặc định là Committed
		subPath:        subPath,
	}
}

// GetStatus trả về trạng thái của TrieDatabase.
func (t *TrieDatabase) GetStatus() TrieDatabaseStatus {
	return t.status
}

// GetStatus trả về trạng thái của TrieDatabase.
func (t *TrieDatabase) GetSubPath() string {
	return t.subPath
}

// SetStatus đặt trạng thái của TrieDatabase.
func (t *TrieDatabase) SetStatus(status TrieDatabaseStatus) {
	t.status = status
}

func (trieDatabae *TrieDatabase) Commit() (common.Hash, error) {
	trieDatabae.IntermediateRoot()
	trieCopy := trieDatabae.trieR.Copy()
	
	root, nodeSet, _, err := trieCopy.Commit(true)
	if err != nil {
		return common.Hash{}, err
	}

	if nodeSet != nil && len(nodeSet.Nodes) > 0 {
		batch := make([][2][]byte, 0, len(nodeSet.Nodes))
		for _, node := range nodeSet.Nodes {
			if node.Hash == (common.Hash{}) {
				continue
			}
			batch = append(batch, [2][]byte{node.Hash.Bytes(), node.Blob})
		}
		if len(batch) > 0 {
			if err := trieDatabae.db.BatchPut(batch); err != nil {
				return common.Hash{}, fmt.Errorf("DB BatchPut failed: %w", err)
			}
		}
	}

	// Create a new trie based on the new root hash
	newTrie, err := p_trie.NewStateTrie(root, trieDatabae.db, false)
	if err != nil {
		logger.Error("Error creating new trie after commit: %v", err)
		return common.Hash{}, err
	}

	trieDatabae.trieR = newTrie
	trieDatabae.originRootHash = root

	return root, nil
}

func (trieDatabae *TrieDatabase) RestoreTrieFromRootHash(rootHash common.Hash) (p_trie.StateTrie, error) {
	// Thử tối đa 3 lần với độ trễ giữa các lần thử
	maxRetries := 3
	var err error
	var tr p_trie.StateTrie
	for i := 0; i < maxRetries; i++ {
		tr, err = p_trie.NewStateTrie(rootHash, trieDatabae.db, false)
		if err == nil {
			return tr, nil
		}

		// Nếu không phải lần thử cuối cùng, đợi trước khi thử lại
		if i < maxRetries-1 {
			time.Sleep(100 * time.Millisecond)
		}
		logger.Error("Error creating trie after restore, retrying: %v", err)
	}

	// Nếu đến đây, tất cả các lần thử đều thất bại
	logger.Error("Error creating trie after multiple retries")
	return nil, err
}

func (trieDatabae *TrieDatabase) IntermediateRoot() (common.Hash, error) {
	var sortedKeys []string // Thay đổi kiểu thành string
	trieDatabae.dirtyData.Range(func(key, value interface{}) bool {
		address := key.(string) // Thay đổi kiểu thành string
		sortedKeys = append(sortedKeys, address)
		return true
	})
	sort.Slice(sortedKeys, func(i, j int) bool {
		return sortedKeys[i] < sortedKeys[j] // So sánh chuỗi trực tiếp
	})
	var batch [][2][]byte

	for _, key := range sortedKeys {
		value, _ := trieDatabae.dirtyData.Load(key)
		valStr := value.(string)
		batch = append(batch, [2][]byte{[]byte(key), []byte(valStr)})
		if err := trieDatabae.trieR.Update([]byte(key), []byte(valStr)); err != nil { // Chuyển đổi cả key và value thành []byte
			return common.Hash{}, err
		}
	}

	if len(batch) > 0 { // Chỉ thực hiện BatchPut nếu có dữ liệu

		if config.ConfigApp.ServiceType == p_common.ServiceTypeMaster {
			data, err := storage.SerializeBatch(batch)
			if err != nil {
				logger.Error(fmt.Sprintf("Error marshaling receipt: %v", err))
			}
			trieDatabae.backUpDb = data
		}
	}
	rootHash := trieDatabae.trieR.Hash()
	trieDatabae.dirtyData.Clear()

	return rootHash, nil
}

func (trieDatabae *TrieDatabase) Storage() storage.Storage {
	return trieDatabae.db
}

func (trieDatabae *TrieDatabase) setDirty(key string, value string) {
	trieDatabae.dirtyData.Store(key, value)
}

func (trieDatabae *TrieDatabase) Get(
	key string,
) (string, error) {

	value, ok := trieDatabae.dirtyData.Load(key)
	if ok {
		return value.(string), nil
	}
	bData, err := trieDatabae.trieR.Get([]byte(key))
	if err != nil {
		logger.Error("TrieDatabase Get", err)
		return "", err
	}
	return string(bData), nil
}

func (trieDatabae *TrieDatabase) Put(
	key string,
	value string,
) error {
	trieDatabae.setDirty(key, value)
	return nil
}

// GetAllKeyValues retrieves all key-value pairs from both dirtyData and the trie.
// It returns a map[string]string containing all the data.  If a key exists in both
// dirtyData and the trie, the value from dirtyData takes precedence.
func (trieDatabae *TrieDatabase) GetAllKeyValues() (map[string]string, error) {
	allKeyValues := make(map[string]string)

	// Iterate over dirtyData and add/update key-value pairs in the map.
	trieDatabae.dirtyData.Range(func(key, value interface{}) bool {
		allKeyValues[key.(string)] = value.(string)
		return true
	})

	// Get key-value pairs from the trie
	allFromTrie, err := trieDatabae.trieR.GetAll()
	if err != nil {
		return nil, err
	}
	
	for hexKey, vBytes := range allFromTrie {
		keyBytes, err := hex.DecodeString(hexKey)
		if err != nil {
			logger.Warn("Failed to decode hex key from GetAll: %s", hexKey)
			continue
		}
		key := string(keyBytes)
		if _, ok := allKeyValues[key]; !ok {
			allKeyValues[key] = string(vBytes)
		}
	}

	return allKeyValues, nil
}

// Discard abandons all changes made since the last Commit.
func (trieDatabae *TrieDatabase) Discard() error {
	trieDatabae.mu.Lock()
	defer trieDatabae.mu.Unlock()

	newTrie, err := trieDatabae.RestoreTrieFromRootHash(trieDatabae.originRootHash)
	if err != nil {
		return err
	}

	trieDatabae.trieR = newTrie
	trieDatabae.dirtyData.Clear()
	return nil
}

// SearchKeyValuesByValue searches for key-value pairs with the given value.
// It returns a map[string]string containing all matching key-value pairs.
func (trieDatabae *TrieDatabase) SearchByValue(searchValue string) (map[string]string, error) {
	matchingKeyValues := make(map[string]string)
	// Search in dirtyData
	trieDatabae.dirtyData.Range(func(key, value interface{}) bool {
		if value.(string) == searchValue {
			matchingKeyValues[key.(string)] = value.(string)
		}
		return true
	})

	// Search in trieR
	allFromTrie, err := trieDatabae.trieR.GetAll()
	if err != nil {
		return nil, err
	}
	
	for hexKey, vBytes := range allFromTrie {
		if string(vBytes) == searchValue {
			keyBytes, err := hex.DecodeString(hexKey)
			if err != nil {
				continue
			}
			key := string(keyBytes)
			if _, ok := matchingKeyValues[key]; !ok {
				matchingKeyValues[key] = string(vBytes)
			}
		}
	}

	return matchingKeyValues, nil
}

// GetNextKeys returns a sorted list of keys that lexicographically follow the given startKey.
// It considers keys from both the dirty map and the committed trie.
// The number of keys returned is limited by the 'limit' parameter, with a maximum hard cap of 10.
func (trieDatabae *TrieDatabase) GetNextKeys(startKey string, limit int) ([]string, error) {
	// Determine the effective limit, capping at 10
	effectiveLimit := limit
	const maxLimit = 10
	if effectiveLimit <= 0 || effectiveLimit > maxLimit {
		effectiveLimit = maxLimit // Apply default/max limit
	}

	// Use a map to collect unique keys efficiently
	nextKeysSet := make(map[string]struct{}) // Using struct{} uses zero memory

	// 1. Iterate through dirty accounts
	trieDatabae.dirtyData.Range(func(key, value interface{}) bool {
		keyStr := key.(string)
		if keyStr > startKey {
			nextKeysSet[keyStr] = struct{}{}
		}
		return true
	})

	// 2. Iterate through the committed trie
	allFromTrie, err := trieDatabae.trieR.GetAll()
	if err != nil {
		logger.Error("Error getting all from trie in GetNextKeys: %v", err)
		return nil, fmt.Errorf("failed to get all from trie: %w", err)
	}

	for hexKey := range allFromTrie {
		keyBytes, err := hex.DecodeString(hexKey)
		if err != nil {
			continue
		}
		currentKeyStr := string(keyBytes)
		if currentKeyStr > startKey {
			nextKeysSet[currentKeyStr] = struct{}{}
		}
	}

	// Convert the set keys to a slice
	nextKeys := make([]string, 0, len(nextKeysSet))
	for k := range nextKeysSet {
		nextKeys = append(nextKeys, k)
	}

	// Sort the keys lexicographically
	sort.Strings(nextKeys)

	// Apply the limit *after* sorting
	if len(nextKeys) > effectiveLimit {
		nextKeys = nextKeys[:effectiveLimit] // Truncate the slice
	}

	return nextKeys, nil
}
