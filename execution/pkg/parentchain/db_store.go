package parentchain

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/syndtr/goleveldb/leveldb"
)

var (
	PrefixChainRegistry = []byte("cr:")
	PrefixFloat         = []byte("fl:")
	PrefixClaimed       = []byte("cl:")
	PrefixTransfer      = []byte("tx:")
	PrefixSeq           = []byte("sq:")
	PrefixVelocity      = []byte("vl:")
	PrefixAccount       = []byte("ac:")
	PrefixStateRoot     = []byte("sr:")
)

type DBStore struct {
	db *leveldb.DB
	mu sync.RWMutex
}

func NewDBStore(path string) (*DBStore, error) {
	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		return nil, err
	}
	return &DBStore{db: db}, nil
}

func (s *DBStore) Close() error {
	return s.db.Close()
}

func (s *DBStore) GetChainRegistry(key common.Hash) (ChainRegistryEntry, bool, error) {
	data, err := s.db.Get(append(PrefixChainRegistry, key.Bytes()...), nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return ChainRegistryEntry{}, false, nil
		}
		return ChainRegistryEntry{}, false, err
	}
	var entry ChainRegistryEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return ChainRegistryEntry{}, false, err
	}
	return entry, true, nil
}

func (s *DBStore) SetChainRegistry(key common.Hash, entry ChainRegistryEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return s.db.Put(append(PrefixChainRegistry, key.Bytes()...), data, nil)
}

func (s *DBStore) GetFloat(key common.Hash) (*big.Int, error) {
	data, err := s.db.Get(append(PrefixFloat, key.Bytes()...), nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return big.NewInt(0), nil
		}
		return nil, err
	}
	val := new(big.Int)
	val.SetBytes(data)
	return val, nil
}

func (s *DBStore) SetFloat(key common.Hash, balance *big.Int) error {
	if balance == nil || balance.Sign() < 0 {
		return errors.New("negative balance")
	}
	return s.db.Put(append(PrefixFloat, key.Bytes()...), balance.Bytes(), nil)
}

func (s *DBStore) GetClaimed(messageID common.Hash) (FloatOutcome, error) {
	data, err := s.db.Get(append(PrefixClaimed, messageID.Bytes()...), nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return FloatOutcomeNone, nil
		}
		return FloatOutcomeNone, err
	}
	if len(data) > 0 {
		return FloatOutcome(data[0]), nil
	}
	return FloatOutcomeNone, nil
}

func (s *DBStore) SetClaimed(messageID common.Hash, outcome FloatOutcome) error {
	return s.db.Put(append(PrefixClaimed, messageID.Bytes()...), []byte{byte(outcome)}, nil)
}

func (s *DBStore) GetTransferRecord(messageID common.Hash) (FloatTransferRecord, bool, error) {
	data, err := s.db.Get(append(PrefixTransfer, messageID.Bytes()...), nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return FloatTransferRecord{}, false, nil
		}
		return FloatTransferRecord{}, false, err
	}
	var rec FloatTransferRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return FloatTransferRecord{}, false, err
	}
	return rec, true, nil
}

func (s *DBStore) SetTransferRecord(messageID common.Hash, rec FloatTransferRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.db.Put(append(PrefixTransfer, messageID.Bytes()...), data, nil)
}

func (s *DBStore) GetFloatSeq(key common.Hash) (uint64, error) {
	data, err := s.db.Get(append(PrefixSeq, key.Bytes()...), nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return 0, nil
		}
		return 0, err
	}
	val := new(big.Int)
	val.SetBytes(data)
	return val.Uint64(), nil
}

func (s *DBStore) SetFloatSeq(key common.Hash, seq uint64) error {
	val := new(big.Int).SetUint64(seq)
	return s.db.Put(append(PrefixSeq, key.Bytes()...), val.Bytes(), nil)
}

func (s *DBStore) GetVelocity(key common.Hash) (FloatVelocityState, error) {
	data, err := s.db.Get(append(PrefixVelocity, key.Bytes()...), nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return FloatVelocityState{
				WindowBaseAlloc: big.NewInt(0),
				Spent:           big.NewInt(0),
			}, nil
		}
		return FloatVelocityState{}, err
	}
	var st FloatVelocityState
	if err := json.Unmarshal(data, &st); err != nil {
		return FloatVelocityState{}, err
	}
	if st.WindowBaseAlloc == nil {
		st.WindowBaseAlloc = big.NewInt(0)
	}
	if st.Spent == nil {
		st.Spent = big.NewInt(0)
	}
	return st, nil
}

func (s *DBStore) SetVelocity(key common.Hash, st FloatVelocityState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.db.Put(append(PrefixVelocity, key.Bytes()...), data, nil)
}

func (s *DBStore) GetAccountRegistry(userAddress common.Address) (cm.PublicKey, bool, error) {
	data, err := s.db.Get(append(PrefixAccount, userAddress.Bytes()...), nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return cm.PublicKey{}, false, nil
		}
		return cm.PublicKey{}, false, err
	}
	return cm.PubkeyFromBytes(data), true, nil
}

func (s *DBStore) SetAccountRegistry(userAddress common.Address, floatIdentityKey cm.PublicKey) error {
	return s.db.Put(append(PrefixAccount, userAddress.Bytes()...), floatIdentityKey.Bytes(), nil)
}

func (s *DBStore) GetAllChainRegistryKeys() ([]common.Hash, error) {
	iter := s.db.NewIterator(nil, nil)
	defer iter.Release()
	
	var keys []common.Hash
	for iter.Next() {
		k := iter.Key()
		if len(k) > len(PrefixChainRegistry) && string(k[:len(PrefixChainRegistry)]) == string(PrefixChainRegistry) {
			hashBytes := k[len(PrefixChainRegistry):]
			if len(hashBytes) == 32 {
				keys = append(keys, common.BytesToHash(hashBytes))
			}
		}
	}
	return keys, iter.Error()
}

var (
	PrefixInbound    = []byte("in:")
	PrefixInboundSeq = []byte("in_sq:")
)

func (s *DBStore) AppendInboundTransfer(destKeyHash common.Hash, event *TransferEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	
	seqKey := append(PrefixInboundSeq, destKeyHash.Bytes()...)
	var curSeq uint64
	seqData, err := s.db.Get(seqKey, nil)
	if err == nil && len(seqData) == 8 {
		curSeq = binary.BigEndian.Uint64(seqData)
	}

	// Create a unique monotonically increasing key using destination key hash + sequence
	// This guarantees new events are ALWAYS appended strictly after previous events in LevelDB.
	key := make([]byte, 0, len(PrefixInbound)+32+8)
	key = append(key, PrefixInbound...)
	key = append(key, destKeyHash.Bytes()...)
	
	var seqBytes [8]byte
	binary.BigEndian.PutUint64(seqBytes[:], curSeq)
	key = append(key, seqBytes[:]...)
	
	if err := s.db.Put(key, data, nil); err != nil {
		return err
	}

	var nextSeqBytes [8]byte
	binary.BigEndian.PutUint64(nextSeqBytes[:], curSeq+1)
	return s.db.Put(seqKey, nextSeqBytes[:], nil)
}

func (s *DBStore) GetInboundTransfers(destKeyHash common.Hash, cursor uint64) ([]*TransferEvent, uint64, error) {
	prefix := make([]byte, 0, len(PrefixInbound)+32)
	prefix = append(prefix, PrefixInbound...)
	prefix = append(prefix, destKeyHash.Bytes()...)
	
	iter := s.db.NewIterator(nil, nil)
	defer iter.Release()
	
	var allEvents []*TransferEvent
	for iter.Next() {
		k := iter.Key()
		if len(k) >= len(prefix) && string(k[:len(prefix)]) == string(prefix) {
			var event TransferEvent
			if err := json.Unmarshal(iter.Value(), &event); err == nil {
				allEvents = append(allEvents, &event)
			}
		}
	}
	
	if err := iter.Error(); err != nil {
		return nil, cursor, err
	}
	
	if cursor >= uint64(len(allEvents)) {
		return nil, cursor, nil
	}
	
	count := uint64(len(allEvents)) - cursor
	if count > 50 {
		count = 50
	}
	
	res := allEvents[cursor : cursor+count]
	return res, cursor + count, nil
}

func (s *DBStore) GetStateRoot(clusterKeyHash common.Hash, epoch uint64) (common.Hash, bool, error) {
	key := make([]byte, 0, len(PrefixStateRoot)+32+8)
	key = append(key, PrefixStateRoot...)
	key = append(key, clusterKeyHash.Bytes()...)
	var epochBytes [8]byte
	for i := 7; i >= 0; i-- {
		epochBytes[i] = byte(epoch >> (8 * (7 - i)))
	}
	key = append(key, epochBytes[:]...)

	data, err := s.db.Get(key, nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return common.Hash{}, false, nil
		}
		return common.Hash{}, false, err
	}
	if len(data) != 32 {
		return common.Hash{}, false, errors.New("invalid state root length")
	}
	return common.BytesToHash(data), true, nil
}

func (s *DBStore) SetStateRoot(clusterKeyHash common.Hash, epoch uint64, root common.Hash) error {
	key := make([]byte, 0, len(PrefixStateRoot)+32+8)
	key = append(key, PrefixStateRoot...)
	key = append(key, clusterKeyHash.Bytes()...)
	var epochBytes [8]byte
	for i := 7; i >= 0; i-- {
		epochBytes[i] = byte(epoch >> (8 * (7 - i)))
	}
	key = append(key, epochBytes[:]...)

	return s.db.Put(key, root.Bytes(), nil)
}
