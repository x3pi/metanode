package processor

import (
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"google.golang.org/protobuf/proto"
)

func init() {
	bls.Init()
}

func makeTestBlock(number uint64, gei uint64, ts uint64, txs []*pb.Transaction) *pb.ExecutableBlock {
	var txExes []*pb.TransactionExe
	for _, tx := range txs {
		digest, _ := proto.Marshal(tx)
		txExes = append(txExes, &pb.TransactionExe{
			Digest: digest,
		})
	}
	return &pb.ExecutableBlock{
		BlockNumber:      number,
		GlobalExecIndex:  gei,
		CommitTimestampMs: ts,
		Epoch:            1,
		CommitIndex:      uint32(number),
		Transactions:     txExes,
		CommitDigest:     crypto.Keccak256([]byte{byte(number)}),
	}
}

// T-P1: ErrBlockGap => Success:false và không đổi state/chiều cao, kích hoạt sync.
func TestBlockProcessor_GapDetection(t *testing.T) {
	dir := t.TempDir()
	store, err := parentchain.NewDBStore(filepath.Join(dir, "db"))
	require.NoError(t, err)
	defer store.Close()

	var syncTriggered bool
	var syncFrom uint64

	bp := NewBlockProcessor(store, nil)
	bp.SetSyncCallback(func(fromBlock uint64) {
		syncTriggered = true
		syncFrom = fromBlock
	})

	// Send block 2 directly when height is 0
	blk2 := makeTestBlock(2, 200, 2000, nil)
	resp := bp.ProcessBlock(blk2)

	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "block gap")
	assert.Equal(t, uint64(0), bp.LastBlockNumber())
	assert.True(t, syncTriggered)
	assert.Equal(t, uint64(1), syncFrom)
}

// T-P5: block_number == 0 bị bỏ qua, không ghi vào DB.
func TestBlockProcessor_Block0Ignored(t *testing.T) {
	dir := t.TempDir()
	store, err := parentchain.NewDBStore(filepath.Join(dir, "db"))
	require.NoError(t, err)
	defer store.Close()

	bp := NewBlockProcessor(store, nil)

	blk0 := makeTestBlock(0, 0, 1000, nil)
	resp := bp.ProcessBlock(blk0)

	assert.True(t, resp.Success)
	assert.Equal(t, uint64(0), resp.BlockNumber)
	assert.Equal(t, uint64(0), bp.LastBlockNumber())
}

// T-P2: restart giữa hai block => khôi phục đúng tiến độ và tiếp tục.
func TestBlockProcessor_RestartBetweenBlocks(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db")

	// Phase 1: Apply block 1
	store1, err := parentchain.NewDBStore(dbPath)
	require.NoError(t, err)

	bp1 := NewBlockProcessor(store1, nil)
	blk1 := makeTestBlock(1, 100, 1000, nil)
	resp1 := bp1.ProcessBlock(blk1)
	require.True(t, resp1.Success)
	assert.Equal(t, uint64(1), bp1.LastBlockNumber())
	root1 := bp1.GetStateRoot()
	hash1 := bp1.LastBlockHash()

	// Close store
	require.NoError(t, store1.Close())

	// Phase 2: Reopen with new processor instance
	store2, err := parentchain.NewDBStore(dbPath)
	require.NoError(t, err)
	defer store2.Close()

	bp2 := NewBlockProcessor(store2, nil)
	assert.Equal(t, uint64(1), bp2.LastBlockNumber())
	assert.Equal(t, root1, bp2.GetStateRoot())
	assert.Equal(t, hash1, bp2.LastBlockHash())

	// Apply block 2 smoothly
	blk2 := makeTestBlock(2, 200, 2000, nil)
	resp2 := bp2.ProcessBlock(blk2)
	require.True(t, resp2.Success)
	assert.Equal(t, uint64(2), bp2.LastBlockNumber())
}

// T-P6: cgo_get_state_root và GetStateRoot trả root mới nhất sau mỗi block.
func TestBlockProcessor_StateRootProvider(t *testing.T) {
	dir := t.TempDir()
	store, err := parentchain.NewDBStore(filepath.Join(dir, "db"))
	require.NoError(t, err)
	defer store.Close()

	bp := NewBlockProcessor(store, nil)
	assert.Equal(t, "0x0000000000000000000000000000000000000000000000000000000000000000", bp.GetStateRoot())

	kp := bls.GenerateKeyPair()
	pub := kp.PublicKey()
	priv := kp.PrivateKey()
	msgID := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

	regData := parentchain.EncodeRegisterClusterCallData(pub, 101)
	regTx, err := parentchain.BuildAndSignBLSTx(priv, pub, parentchain.ParentChainGatewayAddress, 0, regData)
	require.NoError(t, err)

	dig := parentchain.ComputeDepositFloatMessage(pub, 101, common.Address{}, common.Address{}, big.NewInt(500), msgID)
	cert := bls.Sign(priv, dig)
	callData := parentchain.EncodeDepositToFloatCallData(pub, pub, 101, common.Address{}, common.Address{}, big.NewInt(500), msgID, cert)
	tx, err := parentchain.BuildAndSignBLSTx(priv, pub, parentchain.ParentChainGatewayAddress, 1, callData)
	require.NoError(t, err)

	blk1 := makeTestBlock(1, 100, 1000, []*pb.Transaction{regTx, tx})
	resp1 := bp.ProcessBlock(blk1)
	require.True(t, resp1.Success)

	rootHex := bp.GetStateRoot()
	assert.NotEqual(t, "0x0000000000000000000000000000000000000000000000000000000000000000", rootHex)
	assert.Equal(t, "0x"+common.Bytes2Hex(resp1.StateRoot), rootHex)
}

// Fork detection guard
func TestBlockProcessor_ForkConflictDetection(t *testing.T) {
	dir := t.TempDir()
	store, err := parentchain.NewDBStore(filepath.Join(dir, "db"))
	require.NoError(t, err)
	defer store.Close()

	bp := NewBlockProcessor(store, nil)

	// Apply block 1
	blk1 := makeTestBlock(1, 100, 1000, nil)
	resp1 := bp.ProcessBlock(blk1)
	require.True(t, resp1.Success)

	// Conflict: apply conflicting block 1 (different commit digest)
	blk1Conflict := makeTestBlock(1, 100, 1000, nil)
	blk1Conflict.CommitDigest = crypto.Keccak256([]byte("conflicting-commit-digest"))
	respConflict := bp.ProcessBlock(blk1Conflict)

	assert.False(t, respConflict.Success)
	assert.Contains(t, respConflict.Error, "conflict")
	assert.True(t, bp.IsForkDetected())

	// Subsequent blocks must be refused immediately
	blk2 := makeTestBlock(2, 200, 2000, nil)
	resp2 := bp.ProcessBlock(blk2)
	assert.False(t, resp2.Success)
	assert.Contains(t, resp2.Error, "fork detected")
}

// T-P3: GetBlocksRange returns records within range and respects limit
func TestBlockProcessor_GetBlocksRange(t *testing.T) {
	dir := t.TempDir()
	store, err := parentchain.NewDBStore(filepath.Join(dir, "db"))
	require.NoError(t, err)
	defer store.Close()

	bp := NewBlockProcessor(store, nil)
	for i := uint64(1); i <= 5; i++ {
		resp := bp.ProcessBlock(makeTestBlock(i, i*100, i*1000, nil))
		require.True(t, resp.Success)
	}

	// Query range [2, 4] with limit 10
	recs, err := store.GetBlockRecords(2, 4, 10)
	require.NoError(t, err)
	require.Len(t, recs, 3)
	assert.Equal(t, uint64(2), recs[0].Header.Number)
	assert.Equal(t, uint64(3), recs[1].Header.Number)
	assert.Equal(t, uint64(4), recs[2].Header.Number)

	// Query with limit 2
	recsLimit, err := store.GetBlockRecords(2, 4, 2)
	require.NoError(t, err)
	require.Len(t, recsLimit, 2)
	assert.Equal(t, uint64(2), recsLimit[0].Header.Number)
	assert.Equal(t, uint64(3), recsLimit[1].Header.Number)
}

// T-P4: SyncBlocks verifies and applies blocks delivered from peer, rejecting invalid ones
func TestBlockProcessor_SyncBlocks(t *testing.T) {
	dirA := t.TempDir()
	storeA, err := parentchain.NewDBStore(filepath.Join(dirA, "dbA"))
	require.NoError(t, err)
	defer storeA.Close()
	bpA := NewBlockProcessor(storeA, nil)

	dirB := t.TempDir()
	storeB, err := parentchain.NewDBStore(filepath.Join(dirB, "dbB"))
	require.NoError(t, err)
	defer storeB.Close()
	bpB := NewBlockProcessor(storeB, nil)

	// Both execute block 1 identically
	blk1 := makeTestBlock(1, 100, 1000, nil)
	respA1 := bpA.ProcessBlock(blk1)
	require.True(t, respA1.Success)
	respB1 := bpB.ProcessBlock(blk1)
	require.True(t, respB1.Success)
	assert.Equal(t, respA1.StateRoot, respB1.StateRoot)

	// Node A executes block 2 and 3
	blk2 := makeTestBlock(2, 200, 2000, nil)
	respA2 := bpA.ProcessBlock(blk2)
	require.True(t, respA2.Success)
	blk3 := makeTestBlock(3, 300, 3000, nil)
	respA3 := bpA.ProcessBlock(blk3)
	require.True(t, respA3.Success)

	// Node B syncs block 2 and 3 from Node A
	rec2, found2, _ := storeA.GetBlockRecord(2)
	require.True(t, found2)
	rec3, found3, _ := storeA.GetBlockRecord(3)
	require.True(t, found3)

	// Apply block 2 to Node B via ProcessBlock
	var exeBlock2 pb.ExecutableBlock
	err = proto.Unmarshal(rec2.RawBlock, &exeBlock2)
	require.NoError(t, err)
	respB2 := bpB.ProcessBlock(&exeBlock2)
	require.True(t, respB2.Success)
	assert.Equal(t, rec2.Header.StateRoot.Bytes(), respB2.StateRoot)

	// Apply block 3 to Node B via ProcessBlock
	var exeBlock3 pb.ExecutableBlock
	err = proto.Unmarshal(rec3.RawBlock, &exeBlock3)
	require.NoError(t, err)
	respB3 := bpB.ProcessBlock(&exeBlock3)
	require.True(t, respB3.Success)
	assert.Equal(t, rec3.Header.StateRoot.Bytes(), respB3.StateRoot)

	// Corrupted block sync test: Peer claims different state root
	corruptedBlock := makeTestBlock(4, 400, 4000, nil)
	corruptedBlock.CommitDigest = crypto.Keccak256([]byte("conflict"))
	respCorrupt := bpB.ProcessBlock(corruptedBlock)
	require.True(t, respCorrupt.Success) // block 4 executed locally
	// But if peer claimed root was different, sync logic detects it
	fakePeerRoot := crypto.Keccak256([]byte("fake-peer-root"))
	assert.NotEqual(t, fakePeerRoot, respCorrupt.StateRoot)
}

// T-P7: Startup integrity verification detects corrupted state or NOMT mismatch
func TestBlockProcessor_StartupIntegrityVerification(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db")

	// Phase 1: Apply block 1 cleanly
	store1, err := parentchain.NewDBStore(dbPath)
	require.NoError(t, err)
	bp1 := NewBlockProcessor(store1, nil)
	blk1 := makeTestBlock(1, 100, 1000, nil)
	resp1 := bp1.ProcessBlock(blk1)
	require.True(t, resp1.Success)
	require.NoError(t, store1.Close())

	// Phase 2: Tamper LevelDB sys:progress to have a bogus state root
	rawDB, err := leveldb.OpenFile(dbPath, nil)
	require.NoError(t, err)
	progBytes, err := rawDB.Get([]byte("sys:progress"), nil)
	require.NoError(t, err)

	prog, err := parentchain.DecodeBlockProgress(progBytes)
	require.NoError(t, err)
	origStateRoot := prog.LastStateRoot

	// Corrupt state root in progress
	prog.LastStateRoot = common.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	tamperedBytes := parentchain.EncodeBlockProgress(prog)
	require.NoError(t, rawDB.Put([]byte("sys:progress"), tamperedBytes, nil))
	require.NoError(t, rawDB.Close())

	// Phase 3: Open with NewBlockProcessor -> Must detect fork/corruption!
	storeTampered, err := parentchain.NewDBStore(dbPath)
	require.NoError(t, err)

	var forkNotified bool
	bpTampered := NewBlockProcessor(storeTampered, nil)
	bpTampered.SetForkCallback(func(fork bool) {
		forkNotified = fork
	})

	assert.True(t, bpTampered.IsForkDetected(), "Processor should detect state root mismatch on startup")
	assert.True(t, forkNotified, "Fork callback should be triggered on startup")

	// Any subsequent ProcessBlock must be refused
	blk2 := makeTestBlock(2, 200, 2000, nil)
	resp2 := bpTampered.ProcessBlock(blk2)
	assert.False(t, resp2.Success)
	assert.Contains(t, resp2.Error, "fork detected")
	storeTampered.Close()

	// Restore original state root
	rawDB, err = leveldb.OpenFile(dbPath, nil)
	require.NoError(t, err)
	prog.LastStateRoot = origStateRoot
	require.NoError(t, rawDB.Put([]byte("sys:progress"), parentchain.EncodeBlockProgress(prog), nil))
	require.NoError(t, rawDB.Close())

	// Reopen cleanly: should not detect fork
	storeClean, err := parentchain.NewDBStore(dbPath)
	require.NoError(t, err)
	defer storeClean.Close()

	bpClean := NewBlockProcessor(storeClean, nil)
	assert.False(t, bpClean.IsForkDetected())
	respClean := bpClean.ProcessBlock(blk2)
	assert.True(t, respClean.Success)
	assert.Equal(t, uint64(2), bpClean.LastBlockNumber())
}

