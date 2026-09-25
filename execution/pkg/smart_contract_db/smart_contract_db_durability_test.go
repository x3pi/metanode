package smart_contract_db

import (
	"errors"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

// durableRecordingDB records the order of BatchPut and SyncDurable calls so a test can prove that
// contract bytecode is made durable right after it is written (a buffered store would otherwise lose
// it on a crash).
type durableRecordingDB struct {
	*testDB
	mu      sync.Mutex
	events  []string
	syncErr error
}

func newDurableRecordingDB() *durableRecordingDB {
	return &durableRecordingDB{testDB: newTestDB()}
}

func (d *durableRecordingDB) BatchPut(pairs [][2][]byte) error {
	d.mu.Lock()
	d.events = append(d.events, "batchput")
	d.mu.Unlock()
	return d.testDB.BatchPut(pairs)
}

func (d *durableRecordingDB) SyncDurable() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, "sync")
	return d.syncErr
}

func (d *durableRecordingDB) recorded() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.events...)
}

func TestCommit_SyncsCodeStorageAfterWritingBytecode(t *testing.T) {
	code := newDurableRecordingDB()
	db := NewSmartContractDB(code, newTestDB(), nil)

	addr := common.HexToAddress("0x1000000000000000000000000000000000000001")
	codeHash := common.HexToHash("0xc0de")
	db.SetCode(addr, codeHash, []byte{0x60, 0x80, 0x60, 0x40})

	require.NoError(t, db.Commit())
	require.Equal(t, []string{"batchput", "sync"}, code.recorded(),
		"bytecode must be written and then made durable, in that order, exactly once")

	stored, err := code.Get(codeHash.Bytes())
	require.NoError(t, err)
	require.Equal(t, []byte{0x60, 0x80, 0x60, 0x40}, stored)
}

func TestCommit_PropagatesCodeStorageSyncError(t *testing.T) {
	code := newDurableRecordingDB()
	code.syncErr = errors.New("fsync failed")
	db := NewSmartContractDB(code, newTestDB(), nil)
	db.SetCode(common.HexToAddress("0x1000000000000000000000000000000000000002"), common.HexToHash("0xc0de02"), []byte{0x01})

	err := db.Commit()
	require.Error(t, err, "a failed sync must fail the commit rather than report a durable block")
	require.Contains(t, err.Error(), "fsync failed")
}

func TestCommit_DoesNotSyncCodeStorageWhenNoNewCode(t *testing.T) {
	code := newDurableRecordingDB()
	db := NewSmartContractDB(code, newTestDB(), nil)

	require.NoError(t, db.Commit())
	require.Empty(t, code.recorded(), "blocks without new bytecode must not pay for an fsync")
}
