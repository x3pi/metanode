package mvcc

import (
	"math"
	"sort"
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
	mu       sync.RWMutex
	versions map[Version]types.AccountState
	writeIDs map[Version]WriteID
	// sortedVersions is kept ascending and unique so Read can binary-search
	// for the floor version instead of scanning every version on every read
	// (hot accounts can accumulate one entry per TX in the block).
	sortedVersions []Version
	// highest version written so far (for fast path lookups)
	highestVersion Version
	estimates      []Version
}

func NewVersionedAccountState() *VersionedAccountState {
	return &VersionedAccountState{
		versions:       make(map[Version]types.AccountState),
		writeIDs:       make(map[Version]WriteID),
		highestVersion: BaseVersion,
		estimates:      make([]Version, 0, 2),
	}
}

func (v *VersionedAccountState) Write(version Version, state types.AccountState) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, exists := v.versions[version]; !exists {
		v.insertVersionLocked(version)
	}
	v.versions[version] = state
	v.writeIDs[version] = nextWriteID()
	if version > v.highestVersion {
		v.highestVersion = version
	}
}

func (v *VersionedAccountState) insertVersionLocked(version Version) {
	idx := sort.Search(len(v.sortedVersions), func(i int) bool { return v.sortedVersions[i] >= version })
	v.sortedVersions = append(v.sortedVersions, 0)
	copy(v.sortedVersions[idx+1:], v.sortedVersions[idx:])
	v.sortedVersions[idx] = version
}

func (v *VersionedAccountState) Delete(version Version) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, exists := v.versions[version]; exists {
		idx := sort.Search(len(v.sortedVersions), func(i int) bool { return v.sortedVersions[i] >= version })
		if idx < len(v.sortedVersions) && v.sortedVersions[idx] == version {
			v.sortedVersions = append(v.sortedVersions[:idx], v.sortedVersions[idx+1:]...)
		}
	}
	delete(v.versions, version)
	delete(v.writeIDs, version)
}

func (v *VersionedAccountState) AddEstimate(version Version) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, e := range v.estimates {
		if e == version {
			return
		}
	}
	v.estimates = append(v.estimates, version)
}

func (v *VersionedAccountState) RemoveEstimate(version Version) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i, e := range v.estimates {
		if e == version {
			v.estimates = append(v.estimates[:i], v.estimates[i+1:]...)
			return
		}
	}
}

// Read finds the highest version of the account state that is less than or equal to the requested version.
// If the highest version is found, it returns the state and its version.
// If no version is found (e.g., all versions are > requested version), it returns (nil, BaseVersion).
func (v *VersionedAccountState) Read(requestVersion Version) (types.AccountState, Version, WriteID, Version) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	// 1. Check for estimates
	for _, estVer := range v.estimates {
		if estVer <= requestVersion {
			return nil, BaseVersion, BaseWriteID, estVer
		}
	}

	// Floor search: index of the first version strictly greater than
	// requestVersion, then step back one to get the floor entry.
	idx := sort.Search(len(v.sortedVersions), func(i int) bool { return v.sortedVersions[i] > requestVersion })
	if idx == 0 {
		return nil, BaseVersion, BaseWriteID, BaseVersion
	}
	ver := v.sortedVersions[idx-1]
	return v.versions[ver], ver, v.writeIDs[ver], BaseVersion
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
		state, _, _, _ := vState.Read(MaxVersion)
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

func (m *MVCCAccountMap) Delete(addr common.Address, version Version) {
	m.mu.RLock()
	v, exists := m.accounts[addr]
	m.mu.RUnlock()
	if exists {
		v.Delete(version)
	}
}

func (m *MVCCAccountMap) AddEstimate(addr common.Address, version Version) {
	v := m.getOrCreate(addr)
	v.AddEstimate(version)
}

func (m *MVCCAccountMap) RemoveEstimate(addr common.Address, version Version) {
	v := m.getOrCreate(addr)
	v.RemoveEstimate(version)
}

// Read gets the highest version of the state strictly less than requestVersion.
// (Because a transaction with requestVersion = i should read the output of transaction i-1 or earlier).
func (m *MVCCAccountMap) Read(addr common.Address, requestVersion Version) (types.AccountState, Version, WriteID, Version) {
	v := m.getOrCreate(addr)
	// We read the version STRICTLY LESS THAN requestVersion
	if requestVersion == 0 {
		return nil, BaseVersion, BaseWriteID, BaseVersion
	}
	return v.Read(requestVersion - 1)
}

// VersionedStorage holds multiple versions of a smart contract storage value.
type VersionedStorage struct {
	mu       sync.RWMutex
	versions map[Version][]byte
	writeIDs map[Version]WriteID
	// sortedVersions ascending/unique — see VersionedAccountState for why.
	sortedVersions []Version
	estimates      []Version
}

func NewVersionedStorage() *VersionedStorage {
	return &VersionedStorage{
		versions: make(map[Version][]byte),
		writeIDs: make(map[Version]WriteID),
		estimates: make([]Version, 0, 2),
	}
}

func (v *VersionedStorage) Write(version Version, value []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, exists := v.versions[version]; !exists {
		idx := sort.Search(len(v.sortedVersions), func(i int) bool { return v.sortedVersions[i] >= version })
		v.sortedVersions = append(v.sortedVersions, 0)
		copy(v.sortedVersions[idx+1:], v.sortedVersions[idx:])
		v.sortedVersions[idx] = version
	}
	v.versions[version] = value
	v.writeIDs[version] = nextWriteID()
}

func (v *VersionedStorage) Delete(version Version) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, exists := v.versions[version]; exists {
		idx := sort.Search(len(v.sortedVersions), func(i int) bool { return v.sortedVersions[i] >= version })
		if idx < len(v.sortedVersions) && v.sortedVersions[idx] == version {
			v.sortedVersions = append(v.sortedVersions[:idx], v.sortedVersions[idx+1:]...)
		}
	}
	delete(v.versions, version)
	delete(v.writeIDs, version)
}

func (v *VersionedStorage) AddEstimate(version Version) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, e := range v.estimates {
		if e == version {
			return
		}
	}
	v.estimates = append(v.estimates, version)
}

func (v *VersionedStorage) RemoveEstimate(version Version) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i, e := range v.estimates {
		if e == version {
			v.estimates = append(v.estimates[:i], v.estimates[i+1:]...)
			return
		}
	}
}

func (v *VersionedStorage) Read(requestVersion Version) ([]byte, Version, WriteID, Version) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	// 1. Check for estimates
	for _, estVer := range v.estimates {
		if estVer <= requestVersion {
			return nil, BaseVersion, BaseWriteID, estVer
		}
	}

	idx := sort.Search(len(v.sortedVersions), func(i int) bool { return v.sortedVersions[i] > requestVersion })
	if idx == 0 {
		return nil, BaseVersion, BaseWriteID, BaseVersion
	}
	ver := v.sortedVersions[idx-1]
	return v.versions[ver], ver, v.writeIDs[ver], BaseVersion
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
		val, _, _, _ := vStorage.Read(MaxVersion)
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

func (m *MVCCStorageMap) Delete(addr common.Address, key string, version Version) {
	sKey := storageKey(addr, key)
	m.mu.RLock()
	v, exists := m.storage[sKey]
	m.mu.RUnlock()
	if exists {
		v.Delete(version)
	}
}


func (m *MVCCStorageMap) AddEstimate(addr common.Address, key string, version Version) {
	v := m.getOrCreate(addr, key)
	v.AddEstimate(version)
}

func (m *MVCCStorageMap) RemoveEstimate(addr common.Address, key string, version Version) {
	v := m.getOrCreate(addr, key)
	v.RemoveEstimate(version)
}

func (m *MVCCStorageMap) Read(addr common.Address, key string, requestVersion Version) ([]byte, Version, WriteID, Version) {
	v := m.getOrCreate(addr, key)
	if requestVersion == 0 {
		return nil, BaseVersion, BaseWriteID, BaseVersion
	}
	return v.Read(requestVersion - 1)
}

// ReadVersion is used by read sets to track exactly which version of data was read
type ReadVersion struct {
	Version Version
	WriteID WriteID
}
