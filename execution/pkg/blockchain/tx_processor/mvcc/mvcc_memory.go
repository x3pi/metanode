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

// EstimateMarker tracks which transaction plans to write to a location, allowing subsequent transactions to suspend and wait.
type EstimateMarker struct {
	Version Version
	Wakeup  chan struct{}
}

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
	estimates      []EstimateMarker
}

func NewVersionedAccountState() *VersionedAccountState {
	return &VersionedAccountState{
		versions:       make(map[Version]types.AccountState),
		writeIDs:       make(map[Version]WriteID),
		highestVersion: BaseVersion,
		readers:        make([]Version, 0, 4),
		estimates:      make([]EstimateMarker, 0, 2),
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

func (v *VersionedAccountState) AddEstimate(version Version) chan struct{} {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, e := range v.estimates {
		if e.Version == version {
			return e.Wakeup
		}
	}
	ch := make(chan struct{})
	v.estimates = append(v.estimates, EstimateMarker{Version: version, Wakeup: ch})
	return ch
}

func (v *VersionedAccountState) RemoveEstimate(version Version) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i, e := range v.estimates {
		if e.Version == version {
			v.estimates = append(v.estimates[:i], v.estimates[i+1:]...)
			close(e.Wakeup)
			return
		}
	}
}

// Read finds the highest version of the account state that is less than or equal to the requested version.
// It also checks if there are any EstimateMarkers strictly less than the requester. If so, it returns a Wakeup channel.
// If the highest version is found, it returns the state and its version.
// If no version is found (e.g., all versions are > requested version), it returns (nil, BaseVersion).
func (v *VersionedAccountState) Read(requestVersion Version, requester Version) (types.AccountState, Version, WriteID, chan struct{}) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	// 1. Check for estimates
	// We must suspend if there is ANY estimate i such that i < requester
	for _, e := range v.estimates {
		if e.Version < requester {
			return nil, BaseVersion, BaseWriteID, e.Wakeup
		}
	}

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

	return bestState, bestVersion, bestWriteID, nil
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
		state, _, _, _ := vState.Read(MaxVersion, MaxVersion)
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

func (m *MVCCAccountMap) AddEstimate(addr common.Address, version Version) chan struct{} {
	v := m.getOrCreate(addr)
	return v.AddEstimate(version)
}

func (m *MVCCAccountMap) RemoveEstimate(addr common.Address, version Version) {
	m.mu.RLock()
	v, exists := m.accounts[addr]
	m.mu.RUnlock()
	if exists {
		v.RemoveEstimate(version)
	}
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
func (m *MVCCAccountMap) Read(addr common.Address, requestVersion Version) (types.AccountState, Version, WriteID, chan struct{}) {
	v := m.getOrCreate(addr)
	// We read the version STRICTLY LESS THAN requestVersion
	if requestVersion == 0 {
		return nil, BaseVersion, BaseWriteID, nil
	}
	// Pass requestVersion as the requester identity to check estimates
	return v.Read(requestVersion-1, requestVersion)
}

// VersionedStorage holds multiple versions of a smart contract storage value.
type VersionedStorage struct {
	mu       sync.RWMutex
	versions map[Version][]byte
	writeIDs map[Version]WriteID
	readers  []Version
	estimates []EstimateMarker
}

func NewVersionedStorage() *VersionedStorage {
	return &VersionedStorage{
		versions: make(map[Version][]byte),
		writeIDs: make(map[Version]WriteID),
		readers:  make([]Version, 0, 4),
		estimates: make([]EstimateMarker, 0, 2),
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

func (v *VersionedStorage) AddEstimate(version Version) chan struct{} {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, e := range v.estimates {
		if e.Version == version {
			return e.Wakeup
		}
	}
	ch := make(chan struct{})
	v.estimates = append(v.estimates, EstimateMarker{Version: version, Wakeup: ch})
	return ch
}

func (v *VersionedStorage) RemoveEstimate(version Version) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i, e := range v.estimates {
		if e.Version == version {
			v.estimates = append(v.estimates[:i], v.estimates[i+1:]...)
			close(e.Wakeup)
			return
		}
	}
}

func (v *VersionedStorage) Read(requestVersion Version, requester Version) ([]byte, Version, WriteID, chan struct{}) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	// 1. Check for estimates
	for _, e := range v.estimates {
		if e.Version < requester {
			return nil, BaseVersion, BaseWriteID, e.Wakeup
		}
	}

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

	return bestVal, bestVersion, bestWriteID, nil
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
		val, _, _, _ := vStorage.Read(MaxVersion, MaxVersion)
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

func (m *MVCCStorageMap) AddEstimate(addr common.Address, key string, version Version) chan struct{} {
	v := m.getOrCreate(addr, key)
	return v.AddEstimate(version)
}

func (m *MVCCStorageMap) RemoveEstimate(addr common.Address, key string, version Version) {
	sKey := storageKey(addr, key)
	m.mu.RLock()
	v, exists := m.storage[sKey]
	m.mu.RUnlock()
	if exists {
		v.RemoveEstimate(version)
	}
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

func (m *MVCCStorageMap) Read(addr common.Address, key string, requestVersion Version) ([]byte, Version, WriteID, chan struct{}) {
	v := m.getOrCreate(addr, key)
	if requestVersion == 0 {
		return nil, BaseVersion, BaseWriteID, nil
	}
	return v.Read(requestVersion-1, requestVersion)
}

// ReadVersion is used by read sets to track exactly which version of data was read
type ReadVersion struct {
	Version Version
	WriteID WriteID
}
