package stake_state_db

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

func TestStakeStateDB_NoDebugLogFilesAndDeterministicRoot(t *testing.T) {
	origWD, err := os.Getwd()
	require.NoError(t, err)
	workDir := t.TempDir()
	require.NoError(t, os.Chdir(workDir))
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	origBackend := p_trie.GetStateBackend()
	p_trie.SetStateBackend(p_trie.BackendNOMT)
	t.Cleanup(func() {
		p_trie.SetStateBackend(origBackend)
		p_trie.CloseNomtDB()
	})

	nomtDir := filepath.Join(workDir, "nomt_db")
	require.NoError(t, p_trie.InitNomtDB(nomtDir, 2, 16, 16))

	dummyBackupPath := filepath.Join(workDir, "backup", "stake_db")
	stakeStorage := storage.NewDummyStorage(dummyBackupPath)
	stakeTrie, err := p_trie.NewStateTrie(common.Hash{}, stakeStorage, true)
	require.NoError(t, err)

	db := NewStakeStateDB(stakeTrie, stakeStorage)
	require.NotNil(t, db)

	valA := common.HexToAddress("0x00000000000000000000000000000000000000AA")
	valB := common.HexToAddress("0x00000000000000000000000000000000000000BB")

	require.NoError(t, db.CreateRegister(
		valB, "val-b", "desc-b", "", "", 5, big.NewInt(1e18),
		"127.0.0.1:4001", "127.0.0.1:4011", "127.0.0.1:9001", "bls-b", "secp-b",
	))
	require.NoError(t, db.CreateRegister(
		valA, "val-a", "desc-a", "", "", 5, big.NewInt(1e18),
		"127.0.0.1:4000", "127.0.0.1:4010", "127.0.0.1:9000", "bls-a", "secp-a",
	))

	irHash1, err := db.IntermediateRoot(true)
	require.NoError(t, err)
	assert.NotEqual(t, common.Hash{}, irHash1)

	committedHash1, err := db.Commit()
	require.NoError(t, err)
	assert.Equal(t, irHash1, committedHash1)

	// Empty block (no validator mutations) through CommitPipeline + PersistAsync
	irHash2, err := db.IntermediateRoot(true)
	require.NoError(t, err)
	assert.Equal(t, committedHash1, irHash2)

	pipeRes, err := db.CommitPipeline()
	require.NoError(t, err)
	require.NotNil(t, pipeRes)
	require.NoError(t, db.PersistAsync(pipeRes))

	// Verify NO debug log files (IntermediateRoot_.log, Commit.log) were created anywhere in workDir
	var leakedLogs []string
	require.NoError(t, filepath.Walk(workDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		base := filepath.Base(path)
		if base == "IntermediateRoot_.log" || base == "Commit.log" {
			leakedLogs = append(leakedLogs, path)
		}
		return nil
	}))
	assert.Empty(t, leakedLogs, "StakeStateDB must not create per-block IntermediateRoot_.log or Commit.log files")
}

func TestStakeStateDB_UpgradeFromPebbleToDummyStorage_ZeroDataLoss(t *testing.T) {
	origBackend := p_trie.GetStateBackend()
	p_trie.SetStateBackend(p_trie.BackendNOMT)
	t.Cleanup(func() {
		p_trie.SetStateBackend(origBackend)
		p_trie.CloseNomtDB()
	})

	baseDir := t.TempDir()
	nomtDir := filepath.Join(baseDir, "data", "consensus", "nomt_db")
	backupStakePath := filepath.Join(baseDir, "backup", "stake_db")
	pebbleStakePath := filepath.Join(baseDir, "data", "stake_db")

	// Phase 1: Simulate pre-upgrade node where stake storage was opened as a real ShardelDB (Pebble)
	require.NoError(t, p_trie.InitNomtDB(nomtDir, 2, 16, 16))
	legacyShardDB, err := storage.NewShardelDB(pebbleStakePath, 1, 2, storage.TypePebbleDB, backupStakePath)
	require.NoError(t, err)
	require.NoError(t, legacyShardDB.Open())

	triePre, err := p_trie.NewStateTrie(common.Hash{}, legacyShardDB, true)
	require.NoError(t, err)
	dbPre := NewStakeStateDB(triePre, legacyShardDB)

	valAddr := common.HexToAddress("0x5a075aC7e35CD05E331eaD878d52CbfBE5f8d732")
	minStake := new(big.Int)
	minStake.SetString("1000000000000000000000", 10)
	require.NoError(t, dbPre.CreateRegister(
		valAddr, "pri_cloud", "Production validator", "", "", 5, minStake,
		"127.0.0.1:4000", "127.0.0.1:4012", "127.0.0.1:9000", "0x8235afcb", "0x01",
	))

	irRoot, err := dbPre.IntermediateRoot(true)
	require.NoError(t, err)
	preUpgradeRoot, err := dbPre.Commit()
	require.NoError(t, err)
	assert.Equal(t, irRoot, preUpgradeRoot)

	// Cleanly close pre-upgrade node
	p_trie.CloseNomtDB()
	require.NoError(t, legacyShardDB.Close())

	// Phase 2: Reopen on the SAME data dir using DummyStorage (post-upgrade Part A behavior)
	require.NoError(t, p_trie.InitNomtDB(nomtDir, 2, 16, 16))
	dummyStakeDB := storage.NewDummyStorage(backupStakePath)
	triePost, err := p_trie.NewStateTrie(preUpgradeRoot, dummyStakeDB, true)
	require.NoError(t, err)

	nomtRoot, ok := p_trie.GetNomtHandleRoot("stake_db")
	require.True(t, ok, "stake_db NOMT handle must open existing namespace")
	assert.Equal(t, preUpgradeRoot, nomtRoot, "NOMT stake_db root must match pre-upgrade root bit-for-bit")

	dbPost := NewStakeStateDB(triePost, dummyStakeDB)
	allVals, err := dbPost.GetAllValidators()
	require.NoError(t, err)
	require.Len(t, allVals, 1)
	assert.Equal(t, valAddr, allVals[0].Address())
	assert.Equal(t, "pri_cloud", allVals[0].Name())
	assert.Equal(t, uint64(5), allVals[0].CommissionRate())
}
