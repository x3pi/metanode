package executor

import (
	"bytes"
	"errors"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/stretchr/testify/require"
)

// failingBackupStorage wraps an existing Storage and fails Put operations for specific key prefixes.
type failingBackupStorage struct {
	storage.Storage
	failPutPrefixes [][]byte
	err             error
}

func (f *failingBackupStorage) Put(key, value []byte) error {
	for _, p := range f.failPutPrefixes {
		if bytes.HasPrefix(key, p) {
			return f.err
		}
	}
	return f.Storage.Put(key, value)
}

// computeNomtRootsInScratch calculates exact NOMT state roots across a sequence of blocks using an
// isolated scratch NOMT DB so the test harness's live handles are never touched or corrupted.
func computeNomtRootsInScratch(
	t *testing.T,
	steps []struct {
		bn    uint64
		acc   [][2][]byte
		stake [][2][]byte
	},
) (accRoots, stakeRoots []common.Hash) {
	t.Helper()
	scratchDir := filepath.Join(t.TempDir(), "nomt_scratch_roots")
	trie.CloseNomtDB()
	trie.SetStateBackend(trie.BackendNOMT)
	require.NoError(t, trie.InitNomtDB(scratchDir, 1, 16, 16))

	cloneKVs := func(in [][2][]byte) [][2][]byte {
		out := make([][2][]byte, len(in))
		for i, kv := range in {
			out[i] = [2][]byte{bytes.Clone(kv[0]), bytes.Clone(kv[1])}
		}
		return out
	}

	for _, st := range steps {
		agg := map[string][][2][]byte{
			"Account":    cloneKVs(st.acc),
			"StakeState": cloneKVs(st.stake),
		}
		_, err := trie.ApplyNomtReplicationBatches(agg, nil, nil, st.bn)
		require.NoError(t, err)
		aR, _ := trie.GetNomtHandleRoot("account_state")
		sR, _ := trie.GetNomtHandleRoot("stake_db")
		accRoots = append(accRoots, aR)
		stakeRoots = append(stakeRoots, sR)
	}
	trie.CloseNomtDB()
	return accRoots, stakeRoots
}

// changelogDB==nil) with real NOMT DB handles and an initial genesis state at block #0.
func setupNomtExecuteSyncValidatorHarness(
	t *testing.T,
	genAccKVs [][2][]byte,
	genStakeKVs [][2][]byte,
	wrapBackup func(storage.Storage) storage.Storage,
) (*RequestHandler, *storage.StorageManager, *block.BlockDatabase, common.Hash, common.Hash, common.Hash) {
	t.Helper()
	t.Setenv("NODE_TYPE", "")

	prevBackend := trie.GetStateBackend()
	prevBackupDb := storage.GetBackupDbInstance()
	prevBlockNum := storage.GetLastBlockNumber()
	prevGEI := storage.GetLastGlobalExecIndex()
	prevCommitIdx := storage.GetLastHandledCommitIndex()
	prevCommitEpoch := storage.GetLastHandledCommitEpoch()
	prevNomtBlock := storage.GetLastNomtCommittedBlock()
	t.Cleanup(func() {
		mvm.DestroyAllXapianInstances()
		storage.SetBackupDbInstance(prevBackupDb)
		trie.SetStateBackend(prevBackend)
		storage.ResetAllBlockCounters(prevBlockNum)
		storage.ForceSetLastGlobalExecIndex(prevGEI)
		storage.ForceSetLastHandledCommitIndex(prevCommitIdx)
		storage.UpdateLastHandledCommitEpoch(prevCommitEpoch)
		storage.ForceSetLastNomtCommittedBlock(prevNomtBlock)
		trie.CloseNomtDB()
	})

	rootDir := t.TempDir()
	trie.CloseNomtDB()
	trie.SetStateBackend(trie.BackendNOMT)
	require.NoError(t, trie.InitNomtDB(filepath.Join(rootDir, "nomt"), 1, 16, 16))
	storage.ForceSetLastNomtCommittedBlock(0)

	// Pre-seed Genesis state into NOMT handle (block 0)
	cloneKVs := func(in [][2][]byte) [][2][]byte {
		out := make([][2][]byte, len(in))
		for i, kv := range in {
			out[i] = [2][]byte{bytes.Clone(kv[0]), bytes.Clone(kv[1])}
		}
		return out
	}
	_, err := trie.ApplyNomtReplicationBatches(map[string][][2][]byte{
		"Account":    cloneKVs(genAccKVs),
		"StakeState": cloneKVs(genStakeKVs),
	}, nil, nil, 0)
	require.NoError(t, err)

	genAccRoot, _ := trie.GetNomtHandleRoot("account_state")
	genStakeRoot, _ := trie.GetNomtHandleRoot("stake_db")

	sm := storage.NewStorageManager()
	openPebble := func(name, nsBackup string, add func(storage.Storage) error) {
		require.NoError(t, add(openPebbleStorageForTest(t, filepath.Join(rootDir, "pebble_"+name), nsBackup)))
	}
	openPebble("account", "account_state", sm.AddStorageAccount)
	openPebble("tx", "transaction_state", sm.AddStorageTransaction)
	openPebble("device", "", sm.AddStorageBackupDeviceKey)
	openPebble("receipt", "receipts", sm.AddStorageReceipt)
	openPebble("sc", "smart_contract_storage", sm.AddStorageSmartContract)
	openPebble("code", "", sm.AddStorageCode)
	openPebble("triedb", "", sm.AddStorageDatabaseTrie)
	openPebble("block", "", sm.AddStorageBlock)

	rawBackup := openPebbleStorageForTest(t, filepath.Join(rootDir, "pebble_backup"), "")
	if wrapBackup != nil {
		rawBackup = wrapBackup(rawBackup)
	}
	require.NoError(t, sm.AddStorageBackupDb(rawBackup))

	openPebble("mapping", "", sm.AddStorageMapping)
	openPebble("stake", "stake_db", sm.AddStorageStake)

	blockDB := block.NewBlockDatabase(sm.GetStorageBlock())
	genHeader := block.NewBlockHeader(common.Hash{}, 0, genAccRoot, genStakeRoot, common.Hash{}, common.Address{}, 0, common.Hash{}, 0)
	genBlock := block.NewBlock(genHeader, nil, nil)
	genHash := genBlock.Header().Hash()

	cfg := &config.SimpleChainConfig{
		ChainId:   big.NewInt(991),
		IsRPCNode: false,
		Databases: config.DatabasesConfig{RootPath: rootDir},
	}
	cs, err := blockchain.NewChainStateWithGenesis(sm, blockDB, genHeader, cfg, map[common.Address]struct{}{}, &config.GenesisConfig{}, filepath.Join(rootDir, "backup"))
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	require.Nil(t, cs.GetChangelogDB(), "Validator must leave account changelogDB nil")
	require.Nil(t, cs.GetStakeChangelogDB(), "Validator must leave stake changelogDB nil")

	require.NoError(t, blockDB.SaveLastBlockSync(genBlock))
	restoreBC := blockchain.SwapBlockChainForTest(blockDB, sm)
	t.Cleanup(restoreBC)
	bc := blockchain.GetBlockChainInstance()
	require.NoError(t, bc.SetBlockNumberToHash(0, genHash))
	require.NoError(t, bc.Commit())

	storage.ResetAllBlockCounters(0)
	storage.ForceSetLastGlobalExecIndex(0)
	storage.ForceSetLastHandledCommitIndex(0)
	storage.UpdateLastHandledCommitEpoch(0)
	storage.ForceSetLastNomtCommittedBlock(0)

	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())
	rh := NewRequestHandler(sm, cs, "")

	return rh, sm, blockDB, genHash, genAccRoot, genStakeRoot
}

// TestExecuteSync_NomtUndo_HappyPath verifies end-to-end that HandleSyncBlocksRequest on a Validator
// node with BackendNOMT persists compact pre-image undo records ("ua" and "us") in BackupDb during sync execution.
func TestExecuteSync_NomtUndo_HappyPath(t *testing.T) {
	acctA := common.HexToAddress("0x1111111111111111111111111111111111110001").Bytes()
	acctBNew := common.HexToAddress("0x1111111111111111111111111111111111110002").Bytes()
	stakeV1 := common.HexToAddress("0x2222222222222222222222222222222222220001").Bytes()

	genAccKVs := [][2][]byte{
		{makeNomtTestKey(acctA), []byte("genesis_A_balance_1000")},
	}
	genStakeKVs := [][2][]byte{
		{makeNomtTestKey(stakeV1), []byte("genesis_V1_stake_500")},
	}

	b1AccMutations := [][2][]byte{
		{makeNomtTestKey(acctA), []byte("block1_A_balance_900")},
		{makeNomtTestKey(acctBNew), []byte("block1_B_new_100")},
	}
	b1StakeMutations := [][2][]byte{
		{makeNomtTestKey(stakeV1), []byte("block1_V1_stake_700")},
	}

	// Compute expected roots in isolated scratch NOMT DB
	steps := []struct {
		bn    uint64
		acc   [][2][]byte
		stake [][2][]byte
	}{
		{bn: 0, acc: genAccKVs, stake: genStakeKVs},
		{bn: 1, acc: b1AccMutations, stake: b1StakeMutations},
	}
	accRoots, stakeRoots := computeNomtRootsInScratch(t, steps)
	expectedB1AccRoot := accRoots[1]
	expectedB1StakeRoot := stakeRoots[1]

	rh, sm, _, genHash, _, _ := setupNomtExecuteSyncValidatorHarness(t, genAccKVs, genStakeKVs, nil)

	// Construct block #1 and serialized backup data
	txHash := common.HexToHash("0x9901")
	b1 := block.NewBlock(
		block.NewBlockHeader(genHash, 1, expectedB1AccRoot, expectedB1StakeRoot, common.HexToHash("0x1111"), common.Address{}, 100, common.Hash{}, 0, 1),
		[]common.Hash{txHash},
		nil,
	)
	rawB1, err := b1.Marshal()
	require.NoError(t, err)

	ab1, _ := storage.SerializeBatch(b1AccMutations)
	sb1, _ := storage.SerializeBatch(b1StakeMutations)
	rcptB, _ := storage.SerializeBatch([][2][]byte{{txHash.Bytes(), []byte("receipt_data")}})
	txB, _ := storage.SerializeBatch([][2][]byte{{txHash.Bytes(), []byte("tx_data")}})

	backupBytes1, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber:      1,
		AccountBatch:    ab1,
		StakeState:      sb1,
		ReceiptBatchPut: rcptB,
		TxBatchPut:      txB,
	})
	require.NoError(t, err)

	// Execute sync block #1
	resp, err := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks:      []*pb.BlockData{{BlockNumber: 1, RawBlockBytes: rawB1, BackupData: backupBytes1}},
	})
	require.NoError(t, err)
	require.Empty(t, resp.GetError())
	require.Equal(t, uint64(1), resp.GetSyncedCount())

	// Verify state counters and block database
	require.Equal(t, uint64(1), storage.GetLastBlockNumber())
	require.Equal(t, uint64(1), storage.GetLastNomtCommittedBlock())
	require.NotNil(t, blockchain.GetBlockChainInstance().GetBlockByNumber(1))

	// Verify validator NOMT undo was persisted in BackupDb
	backupDB := sm.GetStorageBackupDb()
	require.NotNil(t, backupDB)

	accUndo, okAcc := storage.LoadNomtUndoBatch(backupDB, storage.NomtUndoKindAccount, 1)
	require.True(t, okAcc, "account undo batch for block 1 must exist")
	require.Len(t, accUndo, 2)

	accPreMap := make(map[string][]byte)
	for _, kv := range accUndo {
		accPreMap[string(kv[0])] = kv[1]
	}
	require.Equal(t, []byte("genesis_A_balance_1000"), accPreMap[string(acctA)],
		"Account A pre-image must equal genesis value")
	require.Nil(t, accPreMap[string(acctBNew)],
		"New Account B pre-image must be nil (inserted key)")

	stakeUndo, okStake := storage.LoadNomtUndoBatch(backupDB, storage.NomtUndoKindStake, 1)
	require.True(t, okStake, "stake undo batch for block 1 must exist")
	require.Len(t, stakeUndo, 1)
	require.Equal(t, string(stakeV1), string(stakeUndo[0][0]))
	require.Equal(t, []byte("genesis_V1_stake_500"), stakeUndo[0][1],
		"Stake V1 pre-image must equal genesis value")
}

// TestExecuteSync_NomtUndo_FailClosed_AbortsAndRollsBackMutations verifies that if writing NOMT undo fails
// during EXECUTE SYNC, the sync batch fails-closed: it aborts, rolls back in-flight NOMT mutations, and
// does NOT commit the block or advance state counters.
func TestExecuteSync_NomtUndo_FailClosed_AbortsAndRollsBackMutations(t *testing.T) {
	acctA := common.HexToAddress("0x3333333333333333333333333333333333330001").Bytes()
	stakeV1 := common.HexToAddress("0x4444444444444444444444444444444444440001").Bytes()

	genAccKVs := [][2][]byte{
		{makeNomtTestKey(acctA), []byte("genesis_A_bal")},
	}
	genStakeKVs := [][2][]byte{
		{makeNomtTestKey(stakeV1), []byte("genesis_V1_stake")},
	}

	b1AccMutations := [][2][]byte{
		{makeNomtTestKey(acctA), []byte("b1_mutated_val")},
	}
	b1StakeMutations := [][2][]byte{
		{makeNomtTestKey(stakeV1), []byte("b1_mutated_stake")},
	}

	// Compute expected roots in isolated scratch NOMT DB
	steps := []struct {
		bn    uint64
		acc   [][2][]byte
		stake [][2][]byte
	}{
		{bn: 0, acc: genAccKVs, stake: genStakeKVs},
		{bn: 1, acc: b1AccMutations, stake: b1StakeMutations},
	}
	accRoots, stakeRoots := computeNomtRootsInScratch(t, steps)
	expectedB1AccRoot := accRoots[1]
	expectedB1StakeRoot := stakeRoots[1]

	injectedErr := errors.New("simulated backup db put failure")
	wrapFailingBackup := func(underlying storage.Storage) storage.Storage {
		return &failingBackupStorage{
			Storage:         underlying,
			failPutPrefixes: [][]byte{[]byte(storage.NomtUndoKindAccount)},
			err:             injectedErr,
		}
	}

	rh, _, _, genHash, genAccRoot, _ := setupNomtExecuteSyncValidatorHarness(t, genAccKVs, genStakeKVs, wrapFailingBackup)

	txHash := common.HexToHash("0x9902")
	b1 := block.NewBlock(
		block.NewBlockHeader(genHash, 1, expectedB1AccRoot, expectedB1StakeRoot, common.HexToHash("0x2222"), common.Address{}, 100, common.Hash{}, 0, 1),
		[]common.Hash{txHash},
		nil,
	)
	rawB1, err := b1.Marshal()
	require.NoError(t, err)

	ab1, _ := storage.SerializeBatch(b1AccMutations)
	sb1, _ := storage.SerializeBatch(b1StakeMutations)
	rcptB, _ := storage.SerializeBatch([][2][]byte{{txHash.Bytes(), []byte("receipt_data")}})
	txB, _ := storage.SerializeBatch([][2][]byte{{txHash.Bytes(), []byte("tx_data")}})

	backupBytes1, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber:      1,
		AccountBatch:    ab1,
		StakeState:      sb1,
		ReceiptBatchPut: rcptB,
		TxBatchPut:      txB,
	})
	require.NoError(t, err)

	// Execute sync block #1 with failing backup storage
	resp, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks:      []*pb.BlockData{{BlockNumber: 1, RawBlockBytes: rawB1, BackupData: backupBytes1}},
	})

	// Must fail-closed
	require.NoError(t, callErr)
	require.Contains(t, resp.GetError(), "NOMT undo durability failed for block 1")
	require.Equal(t, uint64(0), resp.GetSyncedCount())

	// Block #1 must NOT be saved in block database
	require.Nil(t, blockchain.GetBlockChainInstance().GetBlockByNumber(1), "uncommitted block #1 must not be in block DB")

	// State counters must remain at block 0
	require.Equal(t, uint64(0), storage.GetLastBlockNumber())
	require.Equal(t, uint64(0), storage.GetLastNomtCommittedBlock())

	// NOMT account handle must have been rolled back to Genesis root (fail-closed, no mutation leak)
	currentAccRoot, ok := trie.GetNomtHandleRoot("account_state")
	require.True(t, ok)
	require.Equal(t, genAccRoot, currentAccRoot,
		"NOMT account handle root must remain at Genesis root after rollback")
}
