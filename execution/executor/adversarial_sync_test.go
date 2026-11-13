package executor

import (
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
)

func setupAdvSyncHarness(t *testing.T) (*RequestHandler, *storage.StorageManager, *blockchain.ChainState, *block.BlockDatabase, common.Hash) {
	t.Helper()

	prevBackend := trie.GetStateBackend()
	prevBlockNum := storage.GetLastBlockNumber()
	prevGEI := storage.GetLastGlobalExecIndex()
	prevCommitIdx := storage.GetLastHandledCommitIndex()
	prevCommitEpoch := storage.GetLastHandledCommitEpoch()

	t.Cleanup(func() {
		trie.SetStateBackend(prevBackend)
		storage.ResetAllBlockCounters(prevBlockNum)
		storage.ForceSetLastGlobalExecIndex(prevGEI)
		storage.ForceSetLastHandledCommitIndex(prevCommitIdx)
		storage.UpdateLastHandledCommitEpoch(prevCommitEpoch)
	})

	trie.SetStateBackend(trie.BackendMPT)

	sm := storage.NewStorageManager()
	for _, add := range []func(storage.Storage) error{
		sm.AddStorageAccount, sm.AddStorageTransaction, sm.AddStorageBackupDeviceKey, sm.AddStorageReceipt,
		sm.AddStorageSmartContract, sm.AddStorageCode, sm.AddStorageDatabaseTrie, sm.AddStorageBlock,
		sm.AddStorageBackupDb, sm.AddStorageMapping, sm.AddStorageStake,
	} {
		require.NoError(t, add(storage.NewMemoryDb()))
	}

	blockDB := block.NewBlockDatabase(sm.GetStorageBlock())
	genHeader := block.NewBlockHeader(common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{}, common.Address{}, 0, common.Hash{}, 0)
	genBlock := block.NewBlock(genHeader, nil, nil)
	genHash := genBlock.Header().Hash()

	cfg := &config.SimpleChainConfig{ChainId: big.NewInt(991)}
	cs, err := blockchain.NewChainStateWithGenesis(sm, blockDB, genHeader, cfg, map[common.Address]struct{}{}, &config.GenesisConfig{}, "skip_epoch_data")
	require.NoError(t, err)

	require.NoError(t, blockDB.SaveBlockByHash(genBlock))
	restoreBC := blockchain.SwapBlockChainForTest(blockDB, sm)
	t.Cleanup(restoreBC)
	bc := blockchain.GetBlockChainInstance()
	require.NoError(t, bc.SetBlockNumberToHash(0, genHash))
	require.NoError(t, bc.Commit())

	storage.ResetAllBlockCounters(0)
	storage.ForceSetLastGlobalExecIndex(0)
	storage.ForceSetLastHandledCommitIndex(0)
	storage.UpdateLastHandledCommitEpoch(0)

	rh := NewRequestHandler(sm, cs, "")
	return rh, sm, cs, blockDB, genHash
}

// ADV-7: Crash giữa CommitBlockState (đồng bộ) và backupDbWorker (bất đồng bộ)
// khiến block_data_topic-N bị thiếu, làm HandleGetBlocksRangeRequest kẹt ở 0 block vĩnh viễn.
func TestAdv7_GetBlocksRange_StallsForeverWhenAsyncBackupDbMissingAfterCrash(t *testing.T) {
	rh, sm, _, blockDB, genHash := setupAdvSyncHarness(t)
	bc := blockchain.GetBlockChainInstance()

	blk1Hdr := block.NewBlockHeader(genHash, 1, common.Hash{}, common.Hash{}, common.Hash{}, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1 := block.NewBlock(blk1Hdr, nil, nil)
	blk1Hash := blk1.Header().Hash()

	blk2Hdr := block.NewBlockHeader(blk1Hash, 2, common.Hash{}, common.Hash{}, common.Hash{}, common.Address{}, 2000, common.Hash{}, 0, 2)
	blk2 := block.NewBlock(blk2Hdr, nil, nil)
	blk2Hash := blk2.Header().Hash()

	require.NoError(t, blockDB.SaveBlockByHash(blk1))
	require.NoError(t, blockDB.SaveLastBlockSync(blk2))
	require.NoError(t, bc.SetBlockNumberToHash(1, blk1Hash))
	require.NoError(t, bc.SetBlockNumberToHash(2, blk2Hash))
	require.NoError(t, bc.Commit())

	storage.ResetAllBlockCounters(2)
	storage.ForceSetLastGlobalExecIndex(2)
	storage.ForceSetLastHandledCommitIndex(2)
	storage.UpdateLastHandledCommitEpoch(0)

	// Mô phỏng crash trước khi backupDbWorker ghi block_data_topic-1,
	// trong khi block_data_topic-2 sau khi node restart đã được ghi đầy đủ.
	backup2Bytes, err := storage.SerializeBackupDb(storage.BackUpDb{BockNumber: 2})
	require.NoError(t, err)
	require.NoError(t, sm.GetStorageBackupDb().Put([]byte("block_data_topic-2"), backup2Bytes))

	resp, err := rh.HandleGetBlocksRangeRequest(&pb.GetBlocksRangeRequest{
		FromBlock: 1,
		ToBlock:   2,
	})
	require.NoError(t, err)
	t.Logf("EVIDENCE ADV-7: GetBlocksRange(1..2) trả về Count=%d, len(Blocks)=%d (kỳ vọng phục vụ được block thay vì kẹt vĩnh viễn ở 0)",
		resp.GetCount(), len(resp.GetBlocks()))
	require.NotZero(t, resp.GetCount(),
		"BUG ADV-7: Thiếu block_data_topic-1 sau crash async BackupDb làm GetBlocksRange trả 0 block vĩnh viễn")
}

// ADV-8: HandleSyncBlocksRequest fail-open khi BackupData bị hỏng:
// DeserializeBackupDb lỗi chỉ bị log rồi bỏ qua, hàm vẫn commit block header và lưu rác vào BackupDb.
func TestAdv8_SyncBlocks_CorruptBackupDbFailsOpenAndCommitsBlockHeader(t *testing.T) {
	rh, sm, cs, _, genHash := setupAdvSyncHarness(t)

	currentAccRoot := cs.GetAccountStateDB().Trie().Hash()
	currentStakeRoot := cs.GetStakeStateDB().Trie().Hash()
	blk1Hdr := block.NewBlockHeader(genHash, 1, currentAccRoot, currentStakeRoot, common.Hash{}, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1 := block.NewBlock(blk1Hdr, nil, nil)
	raw1, err := blk1.Marshal()
	require.NoError(t, err)

	corruptPayload := []byte("CORRUPTED_BACKUP_PAYLOAD_WITH_XAPIAN_LOGS")
	resp, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks: []*pb.BlockData{
			{
				BlockNumber:   1,
				RawBlockBytes: raw1,
				BackupData:    corruptPayload,
			},
		},
	})

	savedBackup, _ := sm.GetStorageBackupDb().Get([]byte("block_data_topic-1"))
	t.Logf("EVIDENCE ADV-8: SyncedCount=%d, Error=%q, callErr=%v, LastBlockNum=%d, SavedCorruptBackup=%q",
		resp.GetSyncedCount(), resp.GetError(), callErr, storage.GetLastBlockNumber(), string(savedBackup))

	require.True(t, callErr != nil || resp.GetError() != "" || resp.GetSyncedCount() == 0,
		"BUG ADV-8: HandleSyncBlocksRequest fail-open khi DeserializeBackupDb lỗi, vẫn commit block và lưu payload hỏng vào BackupDb")
}

func buildMinimalXapianCompLog(dbName, docHex, payload string) []byte {
	appendLenStr := func(buf []byte, s string) []byte {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(len(s)))
		buf = append(buf, b[:]...)
		return append(buf, s...)
	}

	var entry []byte
	entry = append(entry, 1) // Operation::NEW_DOC
	entry = appendLenStr(entry, docHex)
	entry = appendLenStr(entry, payload)

	var out []byte
	out = appendLenStr(out, dbName)
	var b4 [4]byte
	binary.BigEndian.PutUint32(b4[:], 1) // xapian_logs_count = 1
	out = append(out, b4[:]...)
	binary.BigEndian.PutUint32(b4[:], uint32(len(entry)))
	out = append(out, b4[:]...)
	out = append(out, entry...)
	return out
}

// ADV-10: HandleGetBlocksRangeRequest (unix_socket_handler_block.go:252-258) tổng hợp BackUpDb rỗng
// khi block_data_topic-N bị thiếu mà block N+1 đã có backup — kể cả khi block N CÓ GIAO DỊCH (len(txs) > 0)!
// Peer sync nhận BackUpDb rỗng sẽ commit block header mà bỏ trắng toàn bộ AccountBatch / FullDbLogs -> FORK.
func TestAdv10_GetBlocksRange_SynthesizesEmptyBackupForTxBlockAndForksSyncNode(t *testing.T) {
	rhVal, smVal, csVal, blockDBVal, genHash := setupAdvSyncHarness(t)
	bcVal := blockchain.GetBlockChainInstance()

	accRoot := csVal.GetAccountStateDB().Trie().Hash()
	stakeRoot := csVal.GetStakeStateDB().Trie().Hash()
	tx1 := common.HexToHash("0x1111000000000000000000000000000000000000000000000000000000000001")
	txsRoot := common.HexToHash("0x2222000000000000000000000000000000000000000000000000000000000001")

	// Validator có Block #1 chứa 1 giao dịch thực tế (nhưng block_data_topic-1 bị mất do crash trước khi async backup kịp ghi)
	blk1Hdr := block.NewBlockHeader(genHash, 1, accRoot, stakeRoot, txsRoot, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1 := block.NewBlock(blk1Hdr, []common.Hash{tx1}, nil)
	blk1Hash := blk1.Header().Hash()

	blk2Hdr := block.NewBlockHeader(blk1Hash, 2, accRoot, stakeRoot, common.Hash{}, common.Address{}, 2000, common.Hash{}, 0, 2)
	blk2 := block.NewBlock(blk2Hdr, nil, nil)
	blk2Hash := blk2.Header().Hash()

	require.NoError(t, blockDBVal.SaveBlockByHash(blk1))
	require.NoError(t, blockDBVal.SaveLastBlockSync(blk2))
	require.NoError(t, bcVal.SetBlockNumberToHash(1, blk1Hash))
	require.NoError(t, bcVal.SetBlockNumberToHash(2, blk2Hash))
	require.NoError(t, bcVal.Commit())

	storage.ResetAllBlockCounters(2)
	storage.ForceSetLastGlobalExecIndex(2)
	storage.ForceSetLastHandledCommitIndex(2)
	storage.UpdateLastHandledCommitEpoch(0)

	// Block 2 có backup bình thường; Block 1 cố tình thiếu block_data_topic-1
	backup2Bytes, err := storage.SerializeBackupDb(storage.BackUpDb{BockNumber: 2})
	require.NoError(t, err)
	require.NoError(t, smVal.GetStorageBackupDb().Put([]byte("block_data_topic-2"), backup2Bytes))

	rangeResp, err := rhVal.HandleGetBlocksRangeRequest(&pb.GetBlocksRangeRequest{
		FromBlock: 1,
		ToBlock:   2,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(2), rangeResp.GetCount())

	deser1, deserErr := storage.DeserializeBackupDb(rangeResp.GetBlocks()[0].GetBackupData())
	require.NoError(t, deserErr)

	// Đưa thẳng Blocks từ GetBlocksRange vào SyncNode mới (trống)
	rhSync, _, _, _, _ := setupAdvSyncHarness(t)
	syncResp, syncErr := rhSync.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks:      rangeResp.GetBlocks(),
	})

	t.Logf("EVIDENCE ADV-10: blk1Txs=%d, synthBackup1(AccBytes=%d, FullDbLogs=%d), syncResp=(Count=%d, Err=%q, callErr=%v), syncTip=%d",
		len(blk1.Transactions()), len(deser1.AccountBatch), len(deser1.FullDbLogs),
		syncResp.GetSyncedCount(), syncResp.GetError(), syncErr, storage.GetLastBlockNumber())

	// An toàn nếu SyncNode phát hiện block có tx mà BackupDb hoàn toàn rỗng (hoặc GetBlocksRange không giả mạo BackUpDb rỗng cho block có tx)
	require.True(t, syncErr != nil || syncResp.GetError() != "" || syncResp.GetSyncedCount() == 0,
		"BUG ADV-10: HandleGetBlocksRangeRequest tổng hợp BackUpDb rỗng cho block có giao dịch và SyncNode nuốt trôi, commit block mà mất sạch state/Xapian -> FORK")
}

// ADV-11: HandleSyncBlocksRequest (unix_socket_handler_sync.go:533) chỉ kiểm tra BackupDb khi len(backupBytes) > 0.
// Nếu peer gửi BackupData = nil/rỗng cho một block CÓ giao dịch (len(txs) > 0), node đồng bộ bỏ qua applyBackupDbBatches hoàn toàn và commit block header -> FORK.
func TestAdv11_SyncBlocks_NilBackupDataOnTxBlockSilentlySkipsStateAndCommitsHeader(t *testing.T) {
	rh, _, cs, _, genHash := setupAdvSyncHarness(t)

	currentAccRoot := cs.GetAccountStateDB().Trie().Hash()
	currentStakeRoot := cs.GetStakeStateDB().Trie().Hash()
	txHash := common.HexToHash("0x4444000000000000000000000000000000000000000000000000000000000001")
	txsRoot := common.HexToHash("0x3333000000000000000000000000000000000000000000000000000000000001")

	blk1Hdr := block.NewBlockHeader(genHash, 1, currentAccRoot, currentStakeRoot, txsRoot, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1 := block.NewBlock(blk1Hdr, []common.Hash{txHash}, nil)
	raw1, err := blk1.Marshal()
	require.NoError(t, err)

	resp, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks: []*pb.BlockData{
			{
				BlockNumber:   1,
				RawBlockBytes: raw1,
				BackupData:    nil,
			},
		},
	})

	t.Logf("EVIDENCE ADV-11: blk1Txs=%d, BackupData=nil -> SyncedCount=%d, Error=%q, callErr=%v, LastBlockNum=%d",
		len(blk1.Transactions()), resp.GetSyncedCount(), resp.GetError(), callErr, storage.GetLastBlockNumber())

	require.True(t, callErr != nil || resp.GetError() != "" || resp.GetSyncedCount() == 0,
		"BUG ADV-11: HandleSyncBlocksRequest bỏ qua BackupData rỗng trên block có giao dịch, vẫn commit block header mà không áp dụng state/Xapian")
}

// ADV-12: Strict block number guard (unix_socket_handler_sync.go:346-350) kiểm tra `blockNum < lastBlockNum`
// NHƯNG quên kiểm tra `localHash == blockHash`! Khi node dính nhánh fork dài >= 2 block (tip ở block 3)
// và nhận được canonical block 2 (đã phát hiện hash mismatch ở dòng 323 và rollback Xapian về block 1!),
// dòng 348 bất ngờ đặt lại isFullyExecuted = true -> BỎ QUA ghi đè block 2 chuẩn, khiến đồng bộ kẹt ở block 3 và Xapian bị lệch so với BlockDB!
func TestAdv12_SyncBlocks_StrictBlockNumberGuardSkipsForkOverwriteWhenLocalTipAhead(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	rh, _, cs, blockDB, genHash := setupAdvSyncHarness(t)
	bc := blockchain.GetBlockChainInstance()
	xapianDir := t.TempDir()
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(xapianDir)

	accRoot := cs.GetAccountStateDB().Trie().Hash()
	stakeRoot := cs.GetStakeStateDB().Trie().Hash()

	// Block 1 chuẩn (cả leader và local giống nhau)
	blk1Hdr := block.NewBlockHeader(genHash, 1, accRoot, stakeRoot, common.Hash{}, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1 := block.NewBlock(blk1Hdr, nil, nil)
	blk1Hash := blk1.Header().Hash()
	require.NoError(t, blockDB.SaveBlockByHash(blk1))
	require.NoError(t, bc.SetBlockNumberToHash(1, blk1Hash))

	// Local dính fork 2 block: Block 2 stale và Block 3 stale (tip = 3)
	blk2StaleHdr := block.NewBlockHeader(blk1Hash, 2, accRoot, stakeRoot, common.Hash{}, common.Address{}, 2000, common.Hash{}, 0, 2)
	blk2Stale := block.NewBlock(blk2StaleHdr, nil, nil)
	blk2StaleHash := blk2Stale.Header().Hash()

	blk3StaleHdr := block.NewBlockHeader(blk2StaleHash, 3, accRoot, stakeRoot, common.Hash{}, common.Address{}, 3000, common.Hash{}, 0, 3)
	blk3Stale := block.NewBlock(blk3StaleHdr, nil, nil)
	blk3StaleHash := blk3Stale.Header().Hash()

	require.NoError(t, blockDB.SaveBlockByHash(blk2Stale))
	require.NoError(t, blockDB.SaveLastBlockSync(blk3Stale))
	require.NoError(t, bc.SetBlockNumberToHash(2, blk2StaleHash))
	require.NoError(t, bc.SetBlockNumberToHash(3, blk3StaleHash))
	require.NoError(t, bc.Commit())

	storage.ResetAllBlockCounters(3)
	storage.ForceSetLastGlobalExecIndex(3)
	storage.ForceSetLastHandledCommitIndex(3)

	// Ghi Xapian log cho stale fork ở block 2 để kiểm tra xem Xapian có bị rollback về 1 trong khi BlockDB giữ stale block 2 hay không
	contractHex := "1000000000000000000000000000000000000812"
	staleLog := buildMinimalXapianCompLog("db_adv12", "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "stale_b2")
	require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{{contractHex: staleLog}}, 2))

	// Leader cung cấp cặp Block 2 chuẩn & Block 3 chuẩn để ghi đè nhánh fork của Local
	blk2CanonHdr := block.NewBlockHeader(blk1Hash, 2, accRoot, stakeRoot, common.Hash{}, common.Address{}, 2500, common.Hash{}, 0, 2)
	blk2Canon := block.NewBlock(blk2CanonHdr, nil, nil)
	blk2CanonHash := blk2Canon.Header().Hash()
	require.NotEqual(t, blk2StaleHash, blk2CanonHash)
	raw2Canon, err := blk2Canon.Marshal()
	require.NoError(t, err)

	canonLog2 := buildMinimalXapianCompLog("db_adv12", "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "canon_b2")
	backup2Canon, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber: 2,
		FullDbLogs: []map[string][]byte{{contractHex: canonLog2}},
	})
	require.NoError(t, err)

	blk3CanonHdr := block.NewBlockHeader(blk2CanonHash, 3, accRoot, stakeRoot, common.Hash{}, common.Address{}, 3500, common.Hash{}, 0, 3)
	blk3Canon := block.NewBlock(blk3CanonHdr, nil, nil)
	raw3Canon, err := blk3Canon.Marshal()
	require.NoError(t, err)

	backup3Canon, err := storage.SerializeBackupDb(storage.BackUpDb{BockNumber: 3})
	require.NoError(t, err)

	resp, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks: []*pb.BlockData{
			{BlockNumber: 2, RawBlockBytes: raw2Canon, BackupData: backup2Canon},
			{BlockNumber: 3, RawBlockBytes: raw3Canon, BackupData: backup3Canon},
		},
	})
	require.NoError(t, callErr)

	gotHash2, _ := bc.GetBlockHashByNumber(2)
	t.Logf("EVIDENCE ADV-12: SyncedCount=%d, Err=%q, Block2HashInDB=%s (stale=%s, canon=%s), LastBlockAfter=%d",
		resp.GetSyncedCount(), resp.GetError(), gotHash2.Hex()[:18], blk2StaleHash.Hex()[:18], blk2CanonHash.Hex()[:18], storage.GetLastBlockNumber())

	require.Equal(t, blk2CanonHash, gotHash2,
		"BUG ADV-12: Strict block number guard (blockNum < lastBlockNum) ghi đè lại isFullyExecuted=true dù vừa phát hiện hash mismatch, bỏ qua overwrite block 2!")
	require.Empty(t, resp.GetError())
	require.Equal(t, uint64(2), resp.GetSyncedCount())
}

// ADV-13: Nếu node cục bộ dính fork sâu vượt quá cửa sổ undo đã bị cắt tỉa (MAX_UNDO_BLOCKS = 64),
// RollbackXapianAboveBlock trả lỗi; resetLocalForkBeforeBlock bắt buộc phải fail-closed ngay lập tức
// thay vì chỉ ghi log rồi tiếp tục ghi đè BlockDB (khiến Xapian bị kẹt ở nhánh cũ còn BlockDB nhảy sang nhánh mới).
func TestAdv13_SyncBlocks_ForkRollbackFailureFailsClosedWithoutOverwritingBlockDB(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	rh, _, cs, blockDB, genHash := setupAdvSyncHarness(t)
	bc := blockchain.GetBlockChainInstance()
	xapianDir := t.TempDir()
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(xapianDir)

	accRoot := cs.GetAccountStateDB().Trie().Hash()
	stakeRoot := cs.GetStakeStateDB().Trie().Hash()

	// Local lưu Block 1 stale (hash khác với canonical Block 1 của peer)
	blk1StaleHdr := block.NewBlockHeader(genHash, 1, accRoot, stakeRoot, common.Hash{}, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1Stale := block.NewBlock(blk1StaleHdr, nil, nil)
	blk1StaleHash := blk1Stale.Header().Hash()
	require.NoError(t, blockDB.SaveLastBlockSync(blk1Stale))
	require.NoError(t, bc.SetBlockNumberToHash(1, blk1StaleHash))
	require.NoError(t, bc.Commit())

	storage.ResetAllBlockCounters(1)
	storage.ForceSetLastGlobalExecIndex(1)
	storage.ForceSetLastHandledCommitIndex(1)

	// Đẩy Xapian tiến tới block 66 (> MAX_UNDO_BLOCKS = 64) để cắt tỉa undo floor tới block 2,
	// khiến RollbackXapianAboveBlock(0) chắc chắn trả về lỗi.
	contractHex := "1000000000000000000000000000000000000813"
	for bn := uint64(1); bn <= 66; bn++ {
		logBytes := buildMinimalXapianCompLog("db_pruned", "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "v")
		require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{{contractHex: logBytes}}, bn))
	}

	// Peer gửi canonical Block 1 (khác hash -> kích hoạt resetLocalForkBeforeBlock(1) -> RollbackXapianAboveBlock(0))
	blk1CanonHdr := block.NewBlockHeader(genHash, 1, accRoot, stakeRoot, common.Hash{}, common.Address{}, 1500, common.Hash{}, 0, 1)
	blk1Canon := block.NewBlock(blk1CanonHdr, nil, nil)
	raw1Canon, err := blk1Canon.Marshal()
	require.NoError(t, err)
	backup1Canon, err := storage.SerializeBackupDb(storage.BackUpDb{BockNumber: 1})
	require.NoError(t, err)

	resp, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks: []*pb.BlockData{
			{BlockNumber: 1, RawBlockBytes: raw1Canon, BackupData: backup1Canon},
		},
	})
	require.NoError(t, callErr)
	require.NotEmpty(t, resp.GetError(), "RollbackXapianAboveBlock thất bại bắt buộc phải trả về lỗi trong SyncBlocksResponse")
	require.Equal(t, uint64(0), resp.GetSyncedCount())

	gotHash1, _ := bc.GetBlockHashByNumber(1)
	require.Equal(t, blk1StaleHash, gotHash1, "Không được ghi đè BlockDB sang nhánh mới khi Xapian không thể rollback!")
	require.Equal(t, uint64(1), storage.GetLastBlockNumber(), "Không được reset block counter khi Xapian rollback thất bại!")
}

// ADV-14: Nếu một block có giao dịch (len(txs) > 0) đi kèm FullDbLogs chứa địa chỉ hex sai định dạng
// để lách qua hasAnyBackupDbPayload, HandleSyncBlocksRequest phải fail-closed thay vì nuốt lỗi và commit header!
func TestAdv14_SyncBlocks_MalformedFullDbLogsAddressFailsClosed(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	rh, _, cs, _, genHash := setupAdvSyncHarness(t)
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())

	accRoot := cs.GetAccountStateDB().Trie().Hash()
	stakeRoot := cs.GetStakeStateDB().Trie().Hash()
	txHash := common.HexToHash("0x5555000000000000000000000000000000000000000000000000000000000001")
	txsRoot := common.HexToHash("0x6666000000000000000000000000000000000000000000000000000000000001")

	blk1Hdr := block.NewBlockHeader(genHash, 1, accRoot, stakeRoot, txsRoot, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1 := block.NewBlock(blk1Hdr, []common.Hash{txHash}, nil)
	raw1, err := blk1.Marshal()
	require.NoError(t, err)

	validLogFrame := buildMinimalXapianCompLog("db_adv14_sync", "0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", "payload")
	badBackupBytes, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber: 1,
		FullDbLogs: []map[string][]byte{{"0xINVALID_ADDRESS": validLogFrame}},
	})
	require.NoError(t, err)

	resp, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks: []*pb.BlockData{
			{BlockNumber: 1, RawBlockBytes: raw1, BackupData: badBackupBytes},
		},
	})
	require.NoError(t, callErr)
	require.NotEmpty(t, resp.GetError(), "FullDbLogs có địa chỉ hex sai định dạng bắt buộc phải bị từ chối fail-closed")
	require.Equal(t, uint64(0), resp.GetSyncedCount())
	require.Equal(t, uint64(0), storage.GetLastBlockNumber())
}

// ADV-15: Happy-path end-to-end: Khi Block 1 thực sự là block rỗng (0 txs) bị thiếu block_data_topic-1 sau crash
// và Block 2 có giao dịch + FullDbLogs hợp lệ (key EIP-55 "0xAbCd..."), GetBlocksRange tổng hợp BackUpDb rỗng
// cho Block 1 và SyncNode đồng bộ thành công cả 2 block, đồng thời cập nhật chỉ mục full_db_logs_latest-<addr>.
func TestAdv15_GetBlocksRangeAndSyncBlocks_EmptyBlockSynthesizedBackupHappyPathAndLatestIndex(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	rhVal, smVal, csVal, blockDBVal, genHash := setupAdvSyncHarness(t)
	bcVal := blockchain.GetBlockChainInstance()

	accRoot := csVal.GetAccountStateDB().Trie().Hash()
	stakeRoot := csVal.GetStakeStateDB().Trie().Hash()
	tx2 := common.HexToHash("0x7777000000000000000000000000000000000000000000000000000000000002")
	txsRoot2 := common.HexToHash("0x8888000000000000000000000000000000000000000000000000000000000002")

	// Block 1 thực sự rỗng (0 giao dịch), thiếu block_data_topic-1
	blk1Hdr := block.NewBlockHeader(genHash, 1, accRoot, stakeRoot, common.Hash{}, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1 := block.NewBlock(blk1Hdr, nil, nil)
	blk1Hash := blk1.Header().Hash()

	// Block 2 có 1 giao dịch + FullDbLogs với địa chỉ EIP-55 chữ hoa/thường trộn lẫn
	blk2Hdr := block.NewBlockHeader(blk1Hash, 2, accRoot, stakeRoot, txsRoot2, common.Address{}, 2000, common.Hash{}, 0, 2)
	blk2 := block.NewBlock(blk2Hdr, []common.Hash{tx2}, nil)
	blk2Hash := blk2.Header().Hash()

	require.NoError(t, blockDBVal.SaveBlockByHash(blk1))
	require.NoError(t, blockDBVal.SaveLastBlockSync(blk2))
	require.NoError(t, bcVal.SetBlockNumberToHash(1, blk1Hash))
	require.NoError(t, bcVal.SetBlockNumberToHash(2, blk2Hash))
	require.NoError(t, bcVal.Commit())

	storage.ResetAllBlockCounters(2)
	storage.ForceSetLastGlobalExecIndex(2)
	storage.ForceSetLastHandledCommitIndex(2)

	eip55Addr := "0xAbCdEf0000000000000000000000000000000815"
	lowerAddrNoPrefix := "abcdef0000000000000000000000000000000815"
	logFrameB2 := buildMinimalXapianCompLog("db_happy", "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "synced_doc_b2")
	backup2Bytes, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber: 2,
		FullDbLogs: []map[string][]byte{{eip55Addr: logFrameB2}},
	})
	require.NoError(t, err)
	require.NoError(t, smVal.GetStorageBackupDb().Put([]byte("block_data_topic-2"), backup2Bytes))

	rangeResp, err := rhVal.HandleGetBlocksRangeRequest(&pb.GetBlocksRangeRequest{
		FromBlock: 1,
		ToBlock:   2,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(2), rangeResp.GetCount())

	// Kiểm tra block_data_topic-1 rỗng đã được persist tự chữa cho các lần đọc sau trên Validator
	persistedSynth1, getErr := smVal.GetStorageBackupDb().Get([]byte("block_data_topic-1"))
	require.NoError(t, getErr)
	require.NotEmpty(t, persistedSynth1)

	// Đồng bộ sang SyncNode mới
	rhSync, smSync, _, _, _ := setupAdvSyncHarness(t)
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())

	syncResp, syncErr := rhSync.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks:      rangeResp.GetBlocks(),
	})
	require.NoError(t, syncErr)
	require.Empty(t, syncResp.GetError())
	require.Equal(t, uint64(2), syncResp.GetSyncedCount())
	require.Equal(t, uint64(2), storage.GetLastBlockNumber())

	// Kiểm tra chỉ mục full_db_logs_latest-<addr> trên SyncNode tra cứu được bằng hex thường không "0x"
	indexedBytes, ok := storage.GetLatestFullDbLogsForAddress(smSync.GetStorageBackupDb(), lowerAddrNoPrefix)
	require.True(t, ok, "SyncNode phải lưu chỉ mục full_db_logs_latest chuẩn hóa chữ thường cho TA tra cứu")
	require.Equal(t, logFrameB2, indexedBytes)
}

// ADV-16: Khi đồng bộ một canonical block rỗng (0 txs, BackupData = nil) đè lên nhánh fork cục bộ,
// HandleSyncBlocksRequest phải vừa ghi đè BlockDB sang hash chuẩn vừa rollback Xapian về blockNum - 1.
func TestAdv16_SyncBlocks_EmptyCanonicalBlockRollsBackStaleXapianFork(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	rh, _, cs, blockDB, genHash := setupAdvSyncHarness(t)
	bc := blockchain.GetBlockChainInstance()
	xapianDir := t.TempDir()
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(xapianDir)

	accRoot := cs.GetAccountStateDB().Trie().Hash()
	stakeRoot := cs.GetStakeStateDB().Trie().Hash()

	// Local ở Block 1 nhánh fork kèm dữ liệu Xapian tại Block 1
	blk1StaleHdr := block.NewBlockHeader(genHash, 1, accRoot, stakeRoot, common.Hash{}, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1Stale := block.NewBlock(blk1StaleHdr, nil, nil)
	blk1StaleHash := blk1Stale.Header().Hash()
	require.NoError(t, blockDB.SaveLastBlockSync(blk1Stale))
	require.NoError(t, bc.SetBlockNumberToHash(1, blk1StaleHash))
	require.NoError(t, bc.Commit())
	storage.ResetAllBlockCounters(1)
	storage.ForceSetLastGlobalExecIndex(1)

	contractHex := "1000000000000000000000000000000000000816"
	staleLog := buildMinimalXapianCompLog("db_adv16", "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "stale_b1")
	require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{{contractHex: staleLog}}, 1))

	// Peer gửi canonical Block 1 rỗng (0 txs, BackupData = nil)
	blk1CanonHdr := block.NewBlockHeader(genHash, 1, accRoot, stakeRoot, common.Hash{}, common.Address{}, 1200, common.Hash{}, 0, 1)
	blk1Canon := block.NewBlock(blk1CanonHdr, nil, nil)
	blk1CanonHash := blk1Canon.Header().Hash()
	raw1Canon, err := blk1Canon.Marshal()
	require.NoError(t, err)

	resp, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks: []*pb.BlockData{
			{BlockNumber: 1, RawBlockBytes: raw1Canon, BackupData: nil},
		},
	})
	require.NoError(t, callErr)
	require.Empty(t, resp.GetError())
	require.Equal(t, uint64(1), resp.GetSyncedCount())

	gotHash1, _ := bc.GetBlockHashByNumber(1)
	require.Equal(t, blk1CanonHash, gotHash1)

	// Vì Xapian đã được rollback về 0 ngay trong HandleSyncBlocksRequest, gọi RollbackXapianAboveBlock(0) lần nữa phải trả 0 DB cần lùi!
	rolledAgain, rbErr := mvm.RollbackXapianAboveBlock(0)
	require.NoError(t, rbErr)
	require.Equal(t, 0, rolledAgain, "Xapian DB của nhánh fork tại Block 1 phải đã được rollback về 0 khi sync canonical block rỗng")
}
