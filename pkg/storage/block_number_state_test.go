package storage

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ══════════════════════════════════════════════════════════════════════════════
// BlockNumberState — atomic state tests
// ══════════════════════════════════════════════════════════════════════════════

// Reset global state between tests (these are package-level globals)
func resetBlockState() {
	atomic.StoreUint64(&lastBlockNumber, 0)
	atomic.StoreUint64(&lastBlockNumberFromMaster, 0)
	atomic.StoreUint64(&lastGlobalExecIndex, 0)
	atomic.StoreUint64(&firstUpdateInRam, 0)
	atomic.StoreUint64(&firstUpdateInDb, 0)
	atomic.StoreUint64(&incrementingCounter, 100) // safe starting value
}

// ---------- LastBlockNumber ----------

func TestUpdateLastBlockNumber_Monotonic(t *testing.T) {
	resetBlockState()

	UpdateLastBlockNumber(10)
	assert.Equal(t, uint64(10), GetLastBlockNumber())

	UpdateLastBlockNumber(20)
	assert.Equal(t, uint64(20), GetLastBlockNumber())

	// Should NOT go backwards
	UpdateLastBlockNumber(15)
	assert.Equal(t, uint64(20), GetLastBlockNumber())
}

func TestUpdateLastBlockNumber_ConcurrentMonotonic(t *testing.T) {
	resetBlockState()
	var wg sync.WaitGroup

	// 100 goroutines each try to set their own block number
	for i := uint64(1); i <= 100; i++ {
		wg.Add(1)
		go func(n uint64) {
			defer wg.Done()
			UpdateLastBlockNumber(n)
		}(i)
	}
	wg.Wait()

	// Final value should be 100 (highest)
	assert.Equal(t, uint64(100), GetLastBlockNumber())
}

func TestUpdateLastBlockNumber_Callback(t *testing.T) {
	resetBlockState()
	var called uint64

	SetBlockCommitCallback(func(blockNumber uint64) {
		atomic.StoreUint64(&called, blockNumber)
	})
	defer func() { blockCommitCallback = nil }()

	UpdateLastBlockNumber(42)
	assert.Equal(t, uint64(42), atomic.LoadUint64(&called))
}

// ---------- CommitLock (no-op) ----------

func TestCommitLock_AlwaysFalse(t *testing.T) {
	SetCommitLock(true)
	assert.False(t, GetCommitLock())

	SetCommitLock(false)
	assert.False(t, GetCommitLock())
}

// ---------- IncrementingCounter ----------

func TestGetIncrementingCounter_Increasing(t *testing.T) {
	resetBlockState()
	v1 := GetIncrementingCounter()
	v2 := GetIncrementingCounter()
	v3 := GetIncrementingCounter()
	assert.True(t, v2 > v1)
	assert.True(t, v3 > v2)
}

// ---------- GlobalExecIndex ----------

func TestUpdateLastGlobalExecIndex_Monotonic(t *testing.T) {
	resetBlockState()

	UpdateLastGlobalExecIndex(5)
	assert.Equal(t, uint64(5), GetLastGlobalExecIndex())

	UpdateLastGlobalExecIndex(10)
	assert.Equal(t, uint64(10), GetLastGlobalExecIndex())

	// Should NOT go backwards
	UpdateLastGlobalExecIndex(7)
	assert.Equal(t, uint64(10), GetLastGlobalExecIndex())
}

// ---------- FirstUpdateInRam / FirstUpdateInDb ----------

func TestFirstUpdateInRam(t *testing.T) {
	resetBlockState()

	UpdateFirstUpdateInRam(100)
	assert.Equal(t, uint64(100), GetFirstUpdateInRam())

	UpdateFirstUpdateInRam(200)
	assert.Equal(t, uint64(200), GetFirstUpdateInRam())
}

func TestFirstUpdateInDb(t *testing.T) {
	resetBlockState()

	UpdateFirstUpdateInDb(50)
	assert.Equal(t, uint64(50), GetFirstUpdateInDb())

	UpdateFirstUpdateInDb(150)
	assert.Equal(t, uint64(150), GetFirstUpdateInDb())
}

// ---------- BlockNumberFromMaster ----------

func TestBlockNumberFromMaster(t *testing.T) {
	resetBlockState()

	UpdateLastBlockNumberFromMaster(999)
	assert.Equal(t, uint64(999), GetLastBlockNumberFromMaster())
}

// ---------- ConsensusStartBlock / SyncStartBlock ----------

func TestConsensusStartBlock(t *testing.T) {
	SetConsensusStartBlock(500)
	assert.Equal(t, uint64(500), GetConsensusStartBlock())
}

func TestSyncStartBlock(t *testing.T) {
	SetSyncStartBlock(600)
	assert.Equal(t, uint64(600), GetSyncStartBlock())
}

// ---------- ConnectState / UpdateState ----------

func TestGetConnectState_Default(t *testing.T) {
	// Just verify the getter doesn't panic
	_ = GetConnectState()
}

func TestGetUpdateState_Default(t *testing.T) {
	_ = GetUpdateState()
}
