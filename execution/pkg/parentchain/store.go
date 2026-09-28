package parentchain

import (
	"errors"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

type FloatOutcome uint8

const (
	FloatOutcomeNone      FloatOutcome = 0
	FloatOutcomeCredited  FloatOutcome = 1
	FloatOutcomeRefund    FloatOutcome = 2
	FloatOutcomeReclaimed FloatOutcome = 3
)

type ChainRegistryEntry struct {
	FloatIdentityKey   cm.PublicKey
	ChainIDDescriptive uint64
}

type FloatTransferRecord struct {
	SourceKey            *cm.PublicKey
	DestKey              cm.PublicKey
	Value                *big.Int
	ConfirmedAtBlockTime uint64
}

type FloatVelocityState struct {
	WindowStart     uint64
	WindowBaseAlloc *big.Int
	Spent           *big.Int
}

type Store interface {
	GetChainRegistry(key common.Hash) (ChainRegistryEntry, bool, error)
	SetChainRegistry(key common.Hash, entry ChainRegistryEntry) error

	GetFloat(key common.Hash) (*big.Int, error)
	SetFloat(key common.Hash, balance *big.Int) error

	GetClaimed(messageID common.Hash) (FloatOutcome, error)
	SetClaimed(messageID common.Hash, outcome FloatOutcome) error

	GetTransferRecord(messageID common.Hash) (FloatTransferRecord, bool, error)
	SetTransferRecord(messageID common.Hash, rec FloatTransferRecord) error

	GetFloatSeq(key common.Hash) (uint64, error)
	SetFloatSeq(key common.Hash, seq uint64) error

	GetVelocity(key common.Hash) (FloatVelocityState, error)
	SetVelocity(key common.Hash, st FloatVelocityState) error

	GetAccountRegistry(userAddress common.Address) (cm.PublicKey, bool, error)
	SetAccountRegistry(userAddress common.Address, floatIdentityKey cm.PublicKey) error

	GetStateRoot(clusterKeyHash common.Hash, epoch uint64) (common.Hash, bool, error)
	SetStateRoot(clusterKeyHash common.Hash, epoch uint64, root common.Hash) error

	GetAllChainRegistryKeys() ([]common.Hash, error)

	AppendInboundTransfer(destKeyHash common.Hash, event *TransferEvent) error
	GetInboundTransfers(destKeyHash common.Hash, cursor uint64) ([]*TransferEvent, uint64, error)
}

// MemoryStore is an in-memory implementation for testing
type MemoryStore struct {
	mu              sync.RWMutex
	chains          map[common.Hash]ChainRegistryEntry
	floats          map[common.Hash]*big.Int
	claimed         map[common.Hash]FloatOutcome
	transferRecords map[common.Hash]FloatTransferRecord
	seqs            map[common.Hash]uint64
	velocities      map[common.Hash]FloatVelocityState
	accounts        map[common.Address]cm.PublicKey
	inbound         map[common.Hash][]*TransferEvent
	stateRoots      map[common.Hash]map[uint64]common.Hash
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		chains:          make(map[common.Hash]ChainRegistryEntry),
		floats:          make(map[common.Hash]*big.Int),
		claimed:         make(map[common.Hash]FloatOutcome),
		transferRecords: make(map[common.Hash]FloatTransferRecord),
		seqs:            make(map[common.Hash]uint64),
		velocities:      make(map[common.Hash]FloatVelocityState),
		accounts:        make(map[common.Address]cm.PublicKey),
		inbound:         make(map[common.Hash][]*TransferEvent),
		stateRoots:      make(map[common.Hash]map[uint64]common.Hash),
	}
}

// Clone creates a deep copy of the MemoryStore, useful for testing persistence via reload
func (m *MemoryStore) Clone() *MemoryStore {
	m.mu.RLock()
	defer m.mu.RUnlock()
	clone := NewMemoryStore()
	for k, v := range m.chains {
		clone.chains[k] = v
	}
	for k, v := range m.floats {
		clone.floats[k] = new(big.Int).Set(v)
	}
	for k, v := range m.claimed {
		clone.claimed[k] = v
	}
	for k, v := range m.transferRecords {
		rec := v
		if v.SourceKey != nil {
			sk := *v.SourceKey
			rec.SourceKey = &sk
		}
		rec.Value = new(big.Int).Set(v.Value)
		clone.transferRecords[k] = rec
	}
	for k, v := range m.seqs {
		clone.seqs[k] = v
	}
	for k, v := range m.velocities {
		vel := v
		if v.WindowBaseAlloc != nil {
			vel.WindowBaseAlloc = new(big.Int).Set(v.WindowBaseAlloc)
		}
		if v.Spent != nil {
			vel.Spent = new(big.Int).Set(v.Spent)
		}
		clone.velocities[k] = vel
	}
	for k, v := range m.accounts {
		clone.accounts[k] = v
	}
	for k, v := range m.inbound {
		events := make([]*TransferEvent, len(v))
		for i, ev := range v {
			evCopy := *ev
			if ev.Amount != nil {
				evCopy.Amount = new(big.Int).Set(ev.Amount)
			}
			events[i] = &evCopy
		}
		clone.inbound[k] = events
	}
	return clone
}

func (m *MemoryStore) GetChainRegistry(key common.Hash) (ChainRegistryEntry, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.chains[key]
	return entry, ok, nil
}

func (m *MemoryStore) SetChainRegistry(key common.Hash, entry ChainRegistryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chains[key] = entry
	return nil
}

func (m *MemoryStore) GetFloat(key common.Hash) (*big.Int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.floats[key]
	if !ok || val == nil {
		return big.NewInt(0), nil
	}
	return new(big.Int).Set(val), nil
}

func (m *MemoryStore) SetFloat(key common.Hash, balance *big.Int) error {
	if balance == nil || balance.Sign() < 0 {
		return errors.New("negative balance")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.floats[key] = new(big.Int).Set(balance)
	return nil
}

func (m *MemoryStore) GetClaimed(messageID common.Hash) (FloatOutcome, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.claimed[messageID]
	if !ok {
		return FloatOutcomeNone, nil
	}
	return val, nil
}

func (m *MemoryStore) SetClaimed(messageID common.Hash, outcome FloatOutcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimed[messageID] = outcome
	return nil
}

func (m *MemoryStore) GetTransferRecord(messageID common.Hash) (FloatTransferRecord, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.transferRecords[messageID]
	if !ok {
		return FloatTransferRecord{}, false, nil
	}
	ret := val
	if val.SourceKey != nil {
		sk := *val.SourceKey
		ret.SourceKey = &sk
	}
	ret.Value = new(big.Int).Set(val.Value)
	return ret, true, nil
}

func (m *MemoryStore) SetTransferRecord(messageID common.Hash, rec FloatTransferRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := rec
	if rec.SourceKey != nil {
		sk := *rec.SourceKey
		clone.SourceKey = &sk
	}
	clone.Value = new(big.Int).Set(rec.Value)
	m.transferRecords[messageID] = clone
	return nil
}

func (m *MemoryStore) GetFloatSeq(key common.Hash) (uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.seqs[key]
	if !ok {
		return 0, nil
	}
	return val, nil
}

func (m *MemoryStore) SetFloatSeq(key common.Hash, seq uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seqs[key] = seq
	return nil
}

func (m *MemoryStore) GetVelocity(key common.Hash) (FloatVelocityState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.velocities[key]
	if !ok {
		return FloatVelocityState{
			WindowStart:     0,
			WindowBaseAlloc: nil,
			Spent:           nil,
		}, nil
	}
	ret := val
	if val.WindowBaseAlloc != nil {
		ret.WindowBaseAlloc = new(big.Int).Set(val.WindowBaseAlloc)
	} else {
		ret.WindowBaseAlloc = big.NewInt(0)
	}
	if val.Spent != nil {
		ret.Spent = new(big.Int).Set(val.Spent)
	} else {
		ret.Spent = big.NewInt(0)
	}
	return ret, nil
}

func (m *MemoryStore) SetVelocity(key common.Hash, st FloatVelocityState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := st
	if st.WindowBaseAlloc != nil {
		clone.WindowBaseAlloc = new(big.Int).Set(st.WindowBaseAlloc)
	}
	if st.Spent != nil {
		clone.Spent = new(big.Int).Set(st.Spent)
	}
	m.velocities[key] = clone
	return nil
}

func (m *MemoryStore) GetAccountRegistry(userAddress common.Address) (cm.PublicKey, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key, ok := m.accounts[userAddress]
	return key, ok, nil
}

func (m *MemoryStore) SetAccountRegistry(userAddress common.Address, floatIdentityKey cm.PublicKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accounts[userAddress] = floatIdentityKey
	return nil
}

func (m *MemoryStore) GetAllChainRegistryKeys() ([]common.Hash, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]common.Hash, 0, len(m.chains))
	for k := range m.chains {
		keys = append(keys, k)
	}
	return keys, nil
}

func (m *MemoryStore) AppendInboundTransfer(destKeyHash common.Hash, event *TransferEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// Create a safe copy to store
	evCopy := *event
	if event.Amount != nil {
		evCopy.Amount = new(big.Int).Set(event.Amount)
	}
	
	m.inbound[destKeyHash] = append(m.inbound[destKeyHash], &evCopy)
	return nil
}

func (m *MemoryStore) GetInboundTransfers(destKeyHash common.Hash, cursor uint64) ([]*TransferEvent, uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	events := m.inbound[destKeyHash]
	if cursor >= uint64(len(events)) {
		return nil, cursor, nil
	}
	
	count := uint64(len(events)) - cursor
	if count > 50 {
		count = 50 // paginate 50 at a time
	}
	
	res := make([]*TransferEvent, count)
	for i := uint64(0); i < count; i++ {
		ev := events[cursor+i]
		evCopy := *ev
		if ev.Amount != nil {
			evCopy.Amount = new(big.Int).Set(ev.Amount)
		}
		res[i] = &evCopy
	}
	
	return res, cursor + count, nil
}

func (m *MemoryStore) GetStateRoot(clusterKeyHash common.Hash, epoch uint64) (common.Hash, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	epochs, ok := m.stateRoots[clusterKeyHash]
	if !ok {
		return common.Hash{}, false, nil
	}
	root, found := epochs[epoch]
	return root, found, nil
}

func (m *MemoryStore) SetStateRoot(clusterKeyHash common.Hash, epoch uint64, root common.Hash) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateRoots[clusterKeyHash] == nil {
		m.stateRoots[clusterKeyHash] = make(map[uint64]common.Hash)
	}
	m.stateRoots[clusterKeyHash][epoch] = root
	return nil
}
