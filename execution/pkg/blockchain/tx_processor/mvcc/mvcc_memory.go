package mvcc

import (
	"math"
	"sync"
        "sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/types"
)

// Version represents the transaction index in a block.
// A version consists of the transaction index.
type Version int64

// WriteID uniquely identifies a specific write operation.
// Used to prevent ABA problems when a transaction re-executes and writes to the same Version.
type WriteID uint64

const (
	// BaseVersion is the version of data loaded from the global database (before any block transactions).
	BaseVersion Version = -1
	// MaxVersion represents the maximum possible version.
	MaxVersion Version = math.MaxInt64
	// BaseWriteID is the WriteID for data loaded from the global database.
	BaseWriteID WriteID = 0
)

var globalWriteIDCounter uint64

func nextWriteID() WriteID {
	// Import sync/atomic if not already imported
	return WriteID(atomic.AddUint64(&globalWriteIDCounter, 1))
}

// VersionedAccountState holds multiple versions of an AccountState.
type VersionedAccountState struct {
	mu             sync.RWMutex
	versions       map[Version]types.AccountState
	writeIDs       map[Version]WriteID
	// highest version written so far (for fast path lookups)
	highestVersion Version
	readers        []Version
}

func NewVersionedAccountState() *VersionedAccountState {
	return &VersionedAccountState{
		versions:       make(map[Version]types.AccountState),
		writeIDs:       make(map[Version]WriteID),
		highestVersion: BaseVersion,
		readers:        make([]Version, 0, 4),
	}
}

func (v *VersionedAccountState) Write(version Version, state types.AccountState) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.versions[version] = state
	v.writeIDs[version] = nextWriteID()
	if version > v.highestVersion {
		v.highestVersion = version
	}
}

func (v *VersionedAccountState) Delete(version Version) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.versions, version)
	delete(v.writeIDs, version)
}

func (v *VersionedAccountState) AddReader(version Version) {
	v.mu.Lock()
	defer v.mu.Unlock()
	// Avoid duplicate readers for the same version
	for _, r := range v.readers {
		if r == version {
			return
		}
	}
	v.readers = append(v.readers, version)
}

func (v *VersionedAccountState) GetReaders() []Version {
	v.mu.RLock()
	defer v.mu.RUnlock()
	readers := make([]Version, len(v.readers))
	copy(readers, v.readers)
	return readers
}

// Read finds the highest version of the account state that is less than or equal to the requested version.
// If the highest version is found, it returns the state and its version.
// If no version is found (e.g., all versions are > requested version), it returns (nil, BaseVersion).
func (v *VersionedAccountState) Read(requestVersion Version) (types.AccountState, Version, WriteID) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	var bestVersion Version = BaseVersion
	var bestState types.AccountState = nil
	var bestWriteID WriteID = BaseWriteID
	found := false

	for ver, state := range v.versions {
		if ver <= requestVersion {
			if !found || ver > bestVersion {
				bestVersion = ver
				bestState = state
				bestWriteID = v.writeIDs[ver]
				found = true
			}
		}
	}

	return bestState, bestVersion, bestWriteID
}

// MVCCAccountMap stores all versioned account states for the block.
type MVCCAccountMap struct {
	mu       sync.RWMutex
	accounts map[common.Address]*VersionedAccountState
}

func NewMVCCAccountMap() *MVCCAccountMap {
	return &MVCCAccountMap{
		accounts: make(map[common.Address]*VersionedAccountState),
	}
}

func (m *MVCCAccountMap) ExportLatest() map[common.Address]types.AccountState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make(map[common.Address]types.AccountState)
	for addr, vState := range m.accounts {
		state, _, _ := vState.Read(MaxVersion)
		if state != nil {
			res[addr] = state
		}
	}
	return res
}

func (m *MVCCAccountMap) getOrCreate(addr common.Address) *VersionedAccountState {
	m.mu.RLock()
	v, exists := m.accounts[addr]
	m.mu.RUnlock()

	if exists {
		return v
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Double check
	if v, exists := m.accounts[addr]; exists {
		return v
	}
	v = NewVersionedAccountState()
	m.accounts[addr] = v
	return v
}

// Write sets the state for an account at a specific version.
func (m *MVCCAccountMap) Write(addr common.Address, version Version, state types.AccountState) {
	v := m.getOrCreate(addr)
	v.Write(version, state)
}

func (m *MVCCAccountMap) AddReader(addr common.Address, version Version) {
	v := m.getOrCreate(addr)
	v.AddReader(version)
}

func (m *MVCCAccountMap) GetReaders(addr common.Address) []Version {
	m.mu.RLock()
	v, exists := m.accounts[addr]
	m.mu.RUnlock()
	if !exists {
		return nil
	}
	return v.GetReaders()
}

func (m *MVCCAccountMap) Delete(addr common.Address, version Version) {
	m.mu.RLock()
	v, exists := m.accounts[addr]
	m.mu.RUnlock()
	if exists {
		v.Delete(version)
	}
}

// Read gets the highest version of the state strictly less than requestVersion.
// (Because a transaction with requestVersion = i should read the output of transaction i-1 or earlier).
func (m *MVCCAccountMap) Read(addr common.Address, requestVersion Version) (types.AccountState, Version, WriteID) {
	v := m.getOrCreate(addr)
	// We read the version STRICTLY LESS THAN requestVersion
	if requestVersion == 0 {
		return nil, BaseVersion, BaseWriteID
	}
	return v.Read(requestVersion - 1)
}

// VersionedStorage holds multiple versions of a smart contract storage value.
type VersionedStorage struct {
	mu       sync.RWMutex
	versions map[Version][]byte
	writeIDs map[Version]WriteID
	readers  []Version
}

func NewVersionedStorage() *VersionedStorage {
	return &VersionedStorage{
		versions: make(map[Version][]byte),
		writeIDs: make(map[Version]WriteID),
		readers:  make([]Version, 0, 4),
	}
}

func (v *VersionedStorage) Write(version Version, value []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.versions[version] = value
	v.writeIDs[version] = nextWriteID()
}

func (v *VersionedStorage) Delete(version Version) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.versions, version)
	delete(v.writeIDs, version)
}

func (v *VersionedStorage) AddReader(version Version) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, r := range v.readers {
		if r == version {
			return
		}
	}
	v.readers = append(v.readers, version)
}

func (v *VersionedStorage) GetReaders() []Version {
	v.mu.RLock()
	defer v.mu.RUnlock()
	readers := make([]Version, len(v.readers))
	copy(readers, v.readers)
	return readers
}

func (v *VersionedStorage) Read(requestVersion Version) ([]byte, Version, WriteID) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	var bestVersion Version = BaseVersion
	var bestVal []byte = nil
	var bestWriteID WriteID = BaseWriteID
	found := false

	for ver, val := range v.versions {
		if ver <= requestVersion {
			if !found || ver > bestVersion {
				bestVersion = ver
				bestVal = val
				bestWriteID = v.writeIDs[ver]
				found = true
			}
		}
	}

	return bestVal, bestVersion, bestWriteID
}

// MVCCStorageMap stores all versioned storage values for the block.
type MVCCStorageMap struct {
	mu       sync.RWMutex
	storage  map[string]*VersionedStorage
}

func NewMVCCStorageMap() *MVCCStorageMap {
	return &MVCCStorageMap{
		storage: make(map[string]*VersionedStorage),
	}
}

func (m *MVCCStorageMap) ExportLatest() map[string][]byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make(map[string][]byte)
	for sKey, vStorage := range m.storage {
		val, _, _ := vStorage.Read(MaxVersion)
		if val != nil {
			res[sKey] = val
		}
	}
	return res
}

// storageKey combines address and slot key
func storageKey(addr common.Address, key string) string {
	return addr.Hex() + key
}

func (m *MVCCStorageMap) getOrCreate(addr common.Address, key string) *VersionedStorage {
	sKey := storageKey(addr, key)
	m.mu.RLock()
	v, exists := m.storage[sKey]
	m.mu.RUnlock()

	if exists {
		return v
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if v, exists := m.storage[sKey]; exists {
		return v
	}
	v = NewVersionedStorage()
	m.storage[sKey] = v
	return v
}

func (m *MVCCStorageMap) Write(addr common.Address, key string, version Version, value []byte) {
	v := m.getOrCreate(addr, key)
	v.Write(version, value)
}

func (m *MVCCStorageMap) AddReader(addr common.Address, key string, version Version) {
	v := m.getOrCreate(addr, key)
	v.AddReader(version)
}

func (m *MVCCStorageMap) GetReaders(addr common.Address, key string) []Version {
	sKey := storageKey(addr, key)
	m.mu.RLock()
	v, exists := m.storage[sKey]
	m.mu.RUnlock()
	if !exists {
		return nil
	}
	return v.GetReaders()
}

func (m *MVCCStorageMap) Delete(addr common.Address, key string, version Version) {
	sKey := storageKey(addr, key)
	m.mu.RLock()
	v, exists := m.storage[sKey]
	m.mu.RUnlock()
	if exists {
		v.Delete(version)
	}
}

func (m *MVCCStorageMap) Read(addr common.Address, key string, requestVersion Version) ([]byte, Version, WriteID) {
	v := m.getOrCreate(addr, key)
	if requestVersion == 0 {
		return nil, BaseVersion, BaseWriteID
	}
	return v.Read(requestVersion - 1)
}

// ReadVersion is used by read sets to track exactly which version of data was read
type ReadVersion struct {
	Version Version
	WriteID WriteID
}
