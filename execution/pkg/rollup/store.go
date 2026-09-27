package rollup

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

var (
	// System address for Rollup DB
	RollupSystemAddress = common.HexToAddress("0x0000000000000000000000000000000000000072")
	// Index key to keep track of non-terminal messages
	RollupIndexKey = crypto.Keccak256Hash([]byte("RollupIndexKey"))
	// Key to keep track of this cluster's float sequence (nonce)
	RollupFloatSeqKey = crypto.Keccak256Hash([]byte("RollupFloatSeqKey"))
)

type Store interface {
	Get(messageID common.Hash) (*MessageRecord, bool, error)
	Put(record *MessageRecord) error
	ScanNonTerminal() ([]*MessageRecord, error)
	GetNextFloatSeq() (uint64, error)
	IncrementFloatSeq() error
}

type MessageRecord struct {
	MessageID   common.Hash    `json:"messageId"`
	Role        Role           `json:"role"`
	State       State          `json:"state"`
	Sender      common.Address `json:"sender"`
	Target      common.Address `json:"target"`
	Value       *big.Int       `json:"value"`
	SourceSeq   uint64         `json:"sourceSeq"`
	SourcePubKey cm.PublicKey  `json:"sourcePubKey"`
	DestPubKey   cm.PublicKey  `json:"destPubKey"`
	PayloadHash common.Hash    `json:"payloadHash"`
}

type SmartContractDB interface {
	StorageValue(address common.Address, key common.Hash) ([]byte, bool)
	SetStorageValue(address common.Address, key common.Hash, value []byte)
}

type DBStore struct {
	scDB SmartContractDB
	mu   sync.Mutex
}

func NewDBStore(scDB SmartContractDB) *DBStore {
	return &DBStore{scDB: scDB}
}

func computeStorageKey(messageID common.Hash) common.Hash {
	// keccak256("rollup_msg_v1" || messageID)
	var data []byte
	data = append(data, []byte("rollup_msg_v1")...)
	data = append(data, messageID.Bytes()...)
	return crypto.Keccak256Hash(data)
}

func (s *DBStore) Get(messageID common.Hash) (*MessageRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := computeStorageKey(messageID)
	data, ok := s.scDB.StorageValue(RollupSystemAddress, key)
	if !ok || len(data) == 0 {
		return nil, false, nil
	}

	var record MessageRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, false, fmt.Errorf("failed to unmarshal MessageRecord: %w", err)
	}
	return &record, true, nil
}

func (s *DBStore) Put(record *MessageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := computeStorageKey(record.MessageID)
	
	// Marshal record
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal MessageRecord: %w", err)
	}

	// Read index
	indexData, _ := s.scDB.StorageValue(RollupSystemAddress, RollupIndexKey)
	var activeIDs []common.Hash
	if len(indexData) > 0 {
		if err := json.Unmarshal(indexData, &activeIDs); err != nil {
			return fmt.Errorf("failed to unmarshal index: %w", err)
		}
	}

	// Update index if needed
	found := false
	for i, id := range activeIDs {
		if id == record.MessageID {
			found = true
			if record.State.IsTerminal() {
				// Remove from active list
				activeIDs = append(activeIDs[:i], activeIDs[i+1:]...)
			}
			break
		}
	}

	if !found && !record.State.IsTerminal() {
		activeIDs = append(activeIDs, record.MessageID)
		// trần kích thước tường minh (backpressure limit)
		if len(activeIDs) > 10000 {
			return fmt.Errorf("active rollup messages limit exceeded")
		}
	}

	// Write index back
	newIndexData, err := json.Marshal(activeIDs)
	if err != nil {
		return fmt.Errorf("failed to marshal index: %w", err)
	}

	s.scDB.SetStorageValue(RollupSystemAddress, RollupIndexKey, newIndexData)
	s.scDB.SetStorageValue(RollupSystemAddress, key, data)
	return nil
}

func (s *DBStore) ScanNonTerminal() ([]*MessageRecord, error) {
	s.mu.Lock()

	indexData, _ := s.scDB.StorageValue(RollupSystemAddress, RollupIndexKey)
	var activeIDs []common.Hash
	if len(indexData) > 0 {
		if err := json.Unmarshal(indexData, &activeIDs); err != nil {
			s.mu.Unlock()
			return nil, fmt.Errorf("failed to unmarshal index: %w", err)
		}
	}
	s.mu.Unlock()

	var records []*MessageRecord
	for _, id := range activeIDs {
		record, found, err := s.Get(id)
		if err != nil {
			return nil, err
		}
		if found {
			records = append(records, record)
		}
	}
	return records, nil
}

func (s *DBStore) GetNextFloatSeq() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, found := s.scDB.StorageValue(RollupSystemAddress, RollupFloatSeqKey)
	if !found || len(data) == 0 {
		return 0, nil
	}
	return new(big.Int).SetBytes(data).Uint64(), nil
}

func (s *DBStore) IncrementFloatSeq() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, found := s.scDB.StorageValue(RollupSystemAddress, RollupFloatSeqKey)
	var seq uint64
	if found && len(data) > 0 {
		seq = new(big.Int).SetBytes(data).Uint64()
	}
	seq++
	val := new(big.Int).SetUint64(seq).Bytes()
	s.scDB.SetStorageValue(RollupSystemAddress, RollupFloatSeqKey, val)
	return nil
}
