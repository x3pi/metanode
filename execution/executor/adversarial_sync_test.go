package executor

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/types"
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
	// Tầng 1 (Sender / select_ok multi-peer safety): Validator bị thiếu BackupDb của block CÓ tx (#1)
	// tuyệt đối KHÔNG ĐƯỢC bịa BackUpDb rỗng để trả về, mà phải dừng ở Count=0 để client.rs (select_ok)
	// loại bỏ peer lỗi và chọn peer lành lặn khác.
	require.Equal(t, uint64(0), rangeResp.GetCount(),
		"BUG ADV-10 (Sender): HandleGetBlocksRangeRequest không được giả mạo BackUpDb rỗng cho block có giao dịch (sẽ đầu độc select_ok race ở client.rs)")
	require.Empty(t, rangeResp.GetBlocks())

	// Tầng 2 (Receiver defense-in-depth): Nếu một peer độc hại/lỗi cố tình gửi BackUpDb rỗng cho block có tx,
	// SyncNode vẫn phải từ chối fail-closed thay vì commit header rỗng state.
	raw1, err := blk1.Marshal()
	require.NoError(t, err)
	forgedEmptyBackup1, err := storage.SerializeBackupDb(storage.BackUpDb{BockNumber: 1})
	require.NoError(t, err)

	rhSync, _, _, _, _ := setupAdvSyncHarness(t)
	syncResp, syncErr := rhSync.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks: []*pb.BlockData{
			{BlockNumber: 1, RawBlockBytes: raw1, BackupData: forgedEmptyBackup1},
		},
	})

	t.Logf("EVIDENCE ADV-10: blk1Txs=%d, senderCount=%d, receiverSyncResp=(Count=%d, Err=%q, callErr=%v), syncTip=%d",
		len(blk1.Transactions()), rangeResp.GetCount(),
		syncResp.GetSyncedCount(), syncResp.GetError(), syncErr, storage.GetLastBlockNumber())

	require.NoError(t, syncErr)
	require.Equal(t, uint64(0), syncResp.GetSyncedCount())
	require.NotEmpty(t, syncResp.GetError(),
		"BUG ADV-10 (Receiver): SyncNode phải từ chối BackUpDb rỗng đối với block có giao dịch")
	require.Equal(t, uint64(0), storage.GetLastBlockNumber())
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

// ADV-17: HandleSyncBlocksRequest nuốt lỗi CommitBlockState khi thiếu/mất AccountBatch
// (khiến MPT/trie không mở được AccountStatesRoot), đồng thời CommitBlockState đã ghi
// SaveLastBlock(blk) và SetBlockNumberToHash trước bước verify/rebuild state trie -> làm hỏng BlockDB & StorageMapping.
func TestAdv17_SyncBlocks_MissingAccountBatchSwallowsCommitErrorAndCorruptsBlockDB(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	rh, sm, cs, blockDB, genHash := setupAdvSyncHarness(t)
	bc := blockchain.GetBlockChainInstance()

	genBlk, err := blockDB.GetBlockByHash(genHash)
	require.NoError(t, err)
	require.NoError(t, blockDB.SaveLastBlockSync(genBlk))

	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())

	tx1 := common.HexToHash("0x1717000000000000000000000000000000000000000000000000000000000001")
	txsRoot := common.HexToHash("0x1718000000000000000000000000000000000000000000000000000000000001")
	stakeRoot := cs.GetStakeStateDB().Trie().Hash()
	missingAccRoot := common.HexToHash("0x9999000000000000000000000000000000000000000000000000000000000017")

	blk1Hdr := block.NewBlockHeader(genHash, 1, missingAccRoot, stakeRoot, txsRoot, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1 := block.NewBlock(blk1Hdr, []common.Hash{tx1}, nil)
	raw1, err := blk1.Marshal()
	require.NoError(t, err)

	logFrame := buildMinimalXapianCompLog("db_adv17", "0x1717171717171717171717171717171717171717171717171717171717171717", "doc_b1")
	backup1, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber:   1,
		AccountBatch: nil,
		FullDbLogs:   []map[string][]byte{{"1000000000000000000000000000000000000817": logFrame}},
	})
	require.NoError(t, err)

	resp, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks: []*pb.BlockData{
			{BlockNumber: 1, RawBlockBytes: raw1, BackupData: backup1},
		},
	})

	lastBlkAfter, _ := blockDB.GetLastBlock()
	var lastBlkNumAfter uint64
	if lastBlkAfter != nil {
		lastBlkNumAfter = lastBlkAfter.Header().BlockNumber()
	}
	gotHash1, hasHash1 := bc.GetBlockHashByNumber(1)
	_ = gotHash1
	persistedMapping1, _ := sm.GetStorageMapping().Get([]byte("blockNumber_1"))
	persistedBackup1, _ := sm.GetStorageBackupDb().Get([]byte("block_data_topic-1"))
	rolledBackDBs, _ := mvm.RollbackXapianAboveBlock(0)

	t.Logf("EVIDENCE ADV-17: callErr=%v, respErr=%q, SyncedCount=%d, StorageLastBlockNum=%d, BlockDBLastBlock=%d, hasHash1=%v, len(mapping_1)=%d, len(backup_1)=%d, xapianRolledBackDBs=%d",
		callErr, resp.GetError(), resp.GetSyncedCount(), storage.GetLastBlockNumber(), lastBlkNumAfter, hasHash1, len(persistedMapping1), len(persistedBackup1), rolledBackDBs)

	require.True(t, callErr != nil || resp.GetError() != "",
		"BUG ADV-17: HandleSyncBlocksRequest nuốt lỗi CommitBlockState (Error=%q, callErr=%v) dù thiếu AccountBatch khiến không mở được AccountStatesRoot!",
		resp.GetError(), callErr)
	require.Equal(t, uint64(0), lastBlkNumAfter,
		"BUG ADV-17: CommitBlockState ghi SaveLastBlock(blk1) trước khi verify/rebuild state trie và không rollback khi lỗi")
	require.False(t, hasHash1,
		"BUG ADV-17: Mapping blockNumber_1 -> blk1Hash vẫn bị ghi và flush xuống StorageMapping dù CommitBlockState thất bại")
	require.Equal(t, 0, rolledBackDBs,
		"BUG ADV-17: Xapian đã commit Block #1 nhưng không được rollback khi CommitBlockState thất bại")
}

// ADV-18: HandleSyncBlocksRequest early return giữa lô (khi block sau lỗi/thiếu BackupDb)
// bỏ qua toàn bộ đoạn finalization sau vòng lặp (bc.Commit(), GetStorageMapping().Flush(),
// updateLastBlockCallback, pushAsyncGEIUpdateCallback, clearNoncesCacheCallback) cho các block trước đó đã commit!
func TestAdv18_SyncBlocks_MidBatchAbortSkipsMappingFlushAndStateCallbacksForSyncedPrefix(t *testing.T) {
	rh, sm, cs, _, genHash := setupAdvSyncHarness(t)

	var updatedLastBlock types.Block
	rh.SetUpdateLastBlockCallback(func(blk types.Block) {
		updatedLastBlock = blk
	})
	var pushedGEI uint64
	rh.SetPushAsyncGEIUpdateCallback(func(gei uint64, hash []byte, commitIndex uint32, epoch uint64) {
		pushedGEI = gei
	})
	var clearedNonces bool
	rh.SetClearNoncesCacheCallback(func() {
		clearedNonces = true
	})

	accRoot := cs.GetAccountStateDB().Trie().Hash()
	stakeRoot := cs.GetStakeStateDB().Trie().Hash()

	tx1 := common.HexToHash("0x1818000000000000000000000000000000000000000000000000000000000001")
	txsRoot1 := common.HexToHash("0x1819000000000000000000000000000000000000000000000000000000000001")
	blk1Hdr := block.NewBlockHeader(genHash, 1, accRoot, stakeRoot, txsRoot1, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1 := block.NewBlock(blk1Hdr, []common.Hash{tx1}, nil)
	raw1, err := blk1.Marshal()
	require.NoError(t, err)

	txBatch1, err := storage.SerializeBatch([][2][]byte{{tx1.Bytes(), []byte("raw_tx_1")}})
	require.NoError(t, err)
	backup1, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber: 1,
		TxBatchPut: txBatch1,
	})
	require.NoError(t, err)

	tx2 := common.HexToHash("0x1818000000000000000000000000000000000000000000000000000000000002")
	txsRoot2 := common.HexToHash("0x1819000000000000000000000000000000000000000000000000000000000002")
	blk2Hdr := block.NewBlockHeader(blk1.Header().Hash(), 2, accRoot, stakeRoot, txsRoot2, common.Address{}, 2000, common.Hash{}, 0, 2)
	blk2 := block.NewBlock(blk2Hdr, []common.Hash{tx2}, nil)
	raw2, err := blk2.Marshal()
	require.NoError(t, err)

	resp, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks: []*pb.BlockData{
			{BlockNumber: 1, RawBlockBytes: raw1, BackupData: backup1},
			{BlockNumber: 2, RawBlockBytes: raw2, BackupData: nil},
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, uint64(1), resp.GetSyncedCount())
	require.Equal(t, uint64(1), resp.GetLastSyncedBlock())
	require.Equal(t, uint64(1), storage.GetLastBlockNumber())
	require.Equal(t, uint64(1), storage.GetLastGlobalExecIndex())

	rawBlockNumMap, _ := sm.GetStorageMapping().Get([]byte("blockNumber_1"))
	rawTxMap, _ := sm.GetStorageMapping().Get([]byte("txHashPrefix" + tx1.Hex()))

	t.Logf("EVIDENCE ADV-18: SyncedCount=%d, LastBlockNum=%d, LastGEI=%d, len(blockNumber_1_in_DB)=%d, len(txHashPrefix_in_DB)=%d, updatedLastBlock=%v, pushedGEI=%d, clearedNonces=%v",
		resp.GetSyncedCount(), storage.GetLastBlockNumber(), storage.GetLastGlobalExecIndex(),
		len(rawBlockNumMap), len(rawTxMap), updatedLastBlock != nil, pushedGEI, clearedNonces)

	require.Equal(t, blk1.Header().Hash().Bytes(), rawBlockNumMap,
		"BUG ADV-18: Early return giữa lô bỏ qua bc.Commit() khiến mapping blockNumber_1 mất trắng trên đĩa!")
	require.Len(t, rawTxMap, 8,
		"BUG ADV-18: Early return giữa lô bỏ qua bc.Commit() khiến mapping txHashPrefix mất trắng trên đĩa (eth_getTransactionByHash trả null sau restart)!")
	require.NotNil(t, updatedLastBlock,
		"BUG ADV-18: Early return giữa lô bỏ qua updateLastBlockCallback cho Block #1 đã commit!")
	require.Equal(t, uint64(1), pushedGEI,
		"BUG ADV-18: Early return giữa lô bỏ qua pushAsyncGEIUpdateCallback khiến GEIAuthority lệch với DB -> FORK khi resume consensus!")
	require.True(t, clearedNonces,
		"BUG ADV-18: Early return giữa lô bỏ qua clearNoncesCacheCallback!")
}

// ADV-19: HandleGetBlocksRangeRequest giới hạn maxProbe = blockNum + 64,
// nên nếu có > 64 block rỗng liên tiếp thiếu block_data_topic-N (ví dụ catch up qua reexecuteBlocksToCatchUp)
// trước một block có BackupDb ở #66, nó không dò tới lastBlockNumber và kẹt vĩnh viễn ở Count = 0!
func TestAdv19_GetBlocksRange_StallsForeverWhenMoreThan64EmptyBlocksLackBackupDb(t *testing.T) {
	rh, sm, cs, blockDB, genHash := setupAdvSyncHarness(t)
	bc := blockchain.GetBlockChainInstance()

	accRoot := cs.GetAccountStateDB().Trie().Hash()
	stakeRoot := cs.GetStakeStateDB().Trie().Hash()

	parentHash := genHash
	for bn := uint64(1); bn <= 66; bn++ {
		hdr := block.NewBlockHeader(parentHash, bn, accRoot, stakeRoot, common.Hash{}, common.Address{}, 1000+bn, common.Hash{}, 0, bn)
		blk := block.NewBlock(hdr, nil, nil)
		blkHash := blk.Header().Hash()
		if bn < 66 {
			require.NoError(t, blockDB.SaveBlockByHash(blk))
		} else {
			require.NoError(t, blockDB.SaveLastBlockSync(blk))
		}
		require.NoError(t, bc.SetBlockNumberToHash(bn, blkHash))
		parentHash = blkHash
	}
	require.NoError(t, bc.Commit())

	storage.ResetAllBlockCounters(66)
	storage.ForceSetLastGlobalExecIndex(66)
	storage.ForceSetLastHandledCommitIndex(66)
	storage.UpdateLastHandledCommitEpoch(0)

	backup66Bytes, err := storage.SerializeBackupDb(storage.BackUpDb{BockNumber: 66})
	require.NoError(t, err)
	require.NoError(t, sm.GetStorageBackupDb().Put([]byte("block_data_topic-66"), backup66Bytes))

	resp, err := rh.HandleGetBlocksRangeRequest(&pb.GetBlocksRangeRequest{
		FromBlock: 1,
		ToBlock:   66,
	})
	require.NoError(t, err)

	t.Logf("EVIDENCE ADV-19: FromBlock=1..66 (65 empty blocks missing BackupDb + Block #66 has BackupDb) -> Count=%d, len(Blocks)=%d",
		resp.GetCount(), len(resp.GetBlocks()))

	require.Equal(t, uint64(66), resp.GetCount(),
		"BUG ADV-19: maxProbe chỉ quét tối đa blockNum+64 và không kiểm tra lastBlockNumber, khiến GetBlocksRange kẹt vĩnh viễn ở Count=0 khi có >64 block rỗng liên tiếp thiếu BackupDb!")

	// Kịch bản bổ sung: Toàn bộ tip (#67..#140) cũng là block rỗng từ reexecuteBlocksToCatchUp, KHÔNG có bất kỳ block_data_topic-N nào ở tip.
	for bn := uint64(67); bn <= 140; bn++ {
		hdr := block.NewBlockHeader(parentHash, bn, accRoot, stakeRoot, common.Hash{}, common.Address{}, 1000+bn, common.Hash{}, 0, bn)
		blk := block.NewBlock(hdr, nil, nil)
		blkHash := blk.Header().Hash()
		if bn < 140 {
			require.NoError(t, blockDB.SaveBlockByHash(blk))
		} else {
			require.NoError(t, blockDB.SaveLastBlockSync(blk))
		}
		require.NoError(t, bc.SetBlockNumberToHash(bn, blkHash))
		parentHash = blkHash
	}
	require.NoError(t, bc.Commit())
	storage.ResetAllBlockCounters(140)
	storage.ForceSetLastGlobalExecIndex(140)

	resp2, err := rh.HandleGetBlocksRangeRequest(&pb.GetBlocksRangeRequest{
		FromBlock: 67,
		ToBlock:   140,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(74), resp2.GetCount(),
		"GetBlocksRange phải phục vụ O(1) cho cả dải block rỗng đã commit tới tận lastBlockNumber dù không có block_data_topic ở phía sau")
}

// ADV-20: Kết hợp ADV-17 + ADV-18 trong cùng một lô [Block #1 hợp lệ, Block #2 lỗi CommitBlockState]:
// Block #1 phải được finalize đầy đủ (mappings, callbacks, Xapian, BackupDb, LastHandledCommitIndex=11),
// còn Block #2 phải bị rollback sạch sẽ (không lọt vào BlockDB, không bẩn mapping/BackupDb/full_db_logs_latest,
// không nhảy LastHandledCommitIndex lên 22, và Xapian lùi về đúng trạng thái Block #1).
func TestAdv20_SyncBlocks_MidBatchCommitFailureFinalizesPrefixAndRollsBackFailedBlock(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	rh, sm, cs, blockDB, genHash := setupAdvSyncHarness(t)
	bc := blockchain.GetBlockChainInstance()

	genBlk, err := blockDB.GetBlockByHash(genHash)
	require.NoError(t, err)
	require.NoError(t, blockDB.SaveLastBlockSync(genBlk))

	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())

	var updatedLastBlock types.Block
	rh.SetUpdateLastBlockCallback(func(blk types.Block) {
		updatedLastBlock = blk
	})
	var pushedGEI uint64
	var pushedCommitIdx uint32
	rh.SetPushAsyncGEIUpdateCallback(func(gei uint64, hash []byte, commitIndex uint32, epoch uint64) {
		pushedGEI = gei
		pushedCommitIdx = commitIndex
	})
	var clearedNonces bool
	rh.SetClearNoncesCacheCallback(func() {
		clearedNonces = true
	})

	accRoot := cs.GetAccountStateDB().Trie().Hash()
	stakeRoot := cs.GetStakeStateDB().Trie().Hash()

	addr1 := "1111000000000000000000000000000000002001"
	addr2 := "2222000000000000000000000000000000002002"

	// Block #1: Hợp lệ, CommitIndex = 11, có tx1 + Xapian log trên addr1
	tx1 := common.HexToHash("0x1111202000000000000000000000000000000000000000000000000000000001")
	txsRoot1 := common.HexToHash("0x2021000000000000000000000000000000000000000000000000000000000001")
	blk1Hdr := block.NewBlockHeader(genHash, 1, accRoot, stakeRoot, txsRoot1, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1Hdr.SetCommitIndex(11)
	blk1 := block.NewBlock(blk1Hdr, []common.Hash{tx1}, nil)
	raw1, err := blk1.Marshal()
	require.NoError(t, err)

	txBatch1, err := storage.SerializeBatch([][2][]byte{{tx1.Bytes(), []byte("raw_tx_1")}})
	require.NoError(t, err)
	logFrame1 := buildMinimalXapianCompLog("db_adv20_1", "0x2020202020202020202020202020202020202020202020202020202020200001", "doc_b1_valid")
	backup1, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber: 1,
		TxBatchPut: txBatch1,
		FullDbLogs: []map[string][]byte{{addr1: logFrame1}},
	})
	require.NoError(t, err)

	// Block #2: Lỗi thiếu AccountBatch (missingAccRoot), CommitIndex = 22, kèm Xapian log ghi đè addr1 + tạo mới addr2
	tx2 := common.HexToHash("0x2222202000000000000000000000000000000000000000000000000000000002")
	txsRoot2 := common.HexToHash("0x2021000000000000000000000000000000000000000000000000000000000002")
	missingAccRoot2 := common.HexToHash("0x9999000000000000000000000000000000000000000000000000000000000020")
	blk2Hdr := block.NewBlockHeader(blk1.Header().Hash(), 2, missingAccRoot2, stakeRoot, txsRoot2, common.Address{}, 2000, common.Hash{}, 0, 2)
	blk2Hdr.SetCommitIndex(22)
	blk2 := block.NewBlock(blk2Hdr, []common.Hash{tx2}, nil)
	raw2, err := blk2.Marshal()
	require.NoError(t, err)

	logFrame1Corrupt := buildMinimalXapianCompLog("db_adv20_1", "0x2020202020202020202020202020202020202020202020202020202020200002", "doc_b2_uncommitted")
	logFrame2 := buildMinimalXapianCompLog("db_adv20_2", "0x2020202020202020202020202020202020202020202020202020202020200003", "doc_b2_new_contract")
	backup2, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber:   2,
		AccountBatch: nil,
		FullDbLogs:   []map[string][]byte{{addr1: logFrame1Corrupt, addr2: logFrame2}},
	})
	require.NoError(t, err)

	resp, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode:            true,
		PreserveOwnCommitIndex: false,
		Blocks: []*pb.BlockData{
			{BlockNumber: 1, RawBlockBytes: raw1, BackupData: backup1},
			{BlockNumber: 2, RawBlockBytes: raw2, BackupData: backup2},
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, uint64(1), resp.GetSyncedCount())
	require.Equal(t, uint64(1), resp.GetLastSyncedBlock())
	require.Equal(t, uint64(1), resp.GetLastExecutedGei())
	require.Contains(t, resp.GetError(), "CommitBlockState failed for block 2")

	// Kiểm tra prefix Block #1 đã được finalize đầy đủ
	require.Equal(t, uint64(1), storage.GetLastBlockNumber())
	require.Equal(t, uint64(1), storage.GetLastGlobalExecIndex())
	require.Equal(t, uint32(11), storage.GetLastHandledCommitIndex(), "LastHandledCommitIndex chỉ được dừng ở Block #1 (11), không được vọt lên 22 của Block #2 lỗi")

	lastBlkAfter, err := blockDB.GetLastBlock()
	require.NoError(t, err)
	require.Equal(t, uint64(1), lastBlkAfter.Header().BlockNumber())

	rawMap1, _ := sm.GetStorageMapping().Get([]byte("blockNumber_1"))
	require.Equal(t, blk1.Header().Hash().Bytes(), rawMap1)
	rawTx1, _ := sm.GetStorageMapping().Get([]byte("txHashPrefix" + tx1.Hex()))
	require.Len(t, rawTx1, 8)

	require.NotNil(t, updatedLastBlock)
	require.Equal(t, uint64(1), updatedLastBlock.Header().BlockNumber())
	require.Equal(t, uint64(1), pushedGEI)
	require.Equal(t, uint32(11), pushedCommitIdx)
	require.True(t, clearedNonces)

	latestAddr1, okAddr1 := storage.GetLatestFullDbLogsForAddress(sm.GetStorageBackupDb(), addr1)
	require.True(t, okAddr1)
	require.Equal(t, logFrame1, latestAddr1, "full_db_logs_latest của addr1 phải giữ nguyên bản Block #1, không bị ghi đè bởi Block #2 lỗi")

	// Kiểm tra Block #2 không để lại bất kỳ rác nào
	_, hasHash2 := bc.GetBlockHashByNumber(2)
	require.False(t, hasHash2)
	rawMap2, _ := sm.GetStorageMapping().Get([]byte("blockNumber_2"))
	require.Empty(t, rawMap2)
	rawTx2, _ := sm.GetStorageMapping().Get([]byte("txHashPrefix" + tx2.Hex()))
	require.Empty(t, rawTx2)
	backup2Saved, _ := sm.GetStorageBackupDb().Get([]byte("block_data_topic-2"))
	require.Empty(t, backup2Saved)
	latestAddr2, okAddr2 := storage.GetLatestFullDbLogsForAddress(sm.GetStorageBackupDb(), addr2)
	require.False(t, okAddr2)
	require.Empty(t, latestAddr2)

	// Gọi RollbackXapianAboveBlock(1) phải trả 0 vì Xapian của Block #2 đã được rollback ngay khi CommitBlockState thất bại
	rolledAbove1, rbErr := mvm.RollbackXapianAboveBlock(1)
	require.NoError(t, rbErr)
	require.Equal(t, 0, rolledAbove1, "Xapian của Block #2 phải đã được rollback về Block #1 ngay trong HandleSyncBlocksRequest")
}

// ADV-21: Nếu lô sync [Block #1 hợp lệ, Block #2 hợp lệ, Block #3 sai parentHash],
// nhánh parent-hash mismatch KHÔNG ĐƯỢC lùi counter/Xapian về prevBlockNum-1 (#1) làm hỏng Block #2 vừa commit,
// mà phải finalize sạch sẽ prefix #1..#2 và trả về LastSyncedBlock=2.
func TestAdv21_SyncBlocks_MidBatchParentHashMismatchPreservesCommittedPrefix(t *testing.T) {
	rh, sm, cs, _, genHash := setupAdvSyncHarness(t)

	var updatedLastBlock types.Block
	rh.SetUpdateLastBlockCallback(func(blk types.Block) {
		updatedLastBlock = blk
	})

	accRoot := cs.GetAccountStateDB().Trie().Hash()
	stakeRoot := cs.GetStakeStateDB().Trie().Hash()

	blk1Hdr := block.NewBlockHeader(genHash, 1, accRoot, stakeRoot, common.Hash{}, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1 := block.NewBlock(blk1Hdr, nil, nil)
	raw1, err := blk1.Marshal()
	require.NoError(t, err)
	backup1, err := storage.SerializeBackupDb(storage.BackUpDb{BockNumber: 1})
	require.NoError(t, err)

	blk2Hdr := block.NewBlockHeader(blk1.Header().Hash(), 2, accRoot, stakeRoot, common.Hash{}, common.Address{}, 2000, common.Hash{}, 0, 2)
	blk2 := block.NewBlock(blk2Hdr, nil, nil)
	raw2, err := blk2.Marshal()
	require.NoError(t, err)
	backup2, err := storage.SerializeBackupDb(storage.BackUpDb{BockNumber: 2})
	require.NoError(t, err)

	bogusParent := common.HexToHash("0xdead000000000000000000000000000000000000000000000000000000000021")
	blk3Hdr := block.NewBlockHeader(bogusParent, 3, accRoot, stakeRoot, common.Hash{}, common.Address{}, 3000, common.Hash{}, 0, 3)
	blk3 := block.NewBlock(blk3Hdr, nil, nil)
	raw3, err := blk3.Marshal()
	require.NoError(t, err)
	backup3, err := storage.SerializeBackupDb(storage.BackUpDb{BockNumber: 3})
	require.NoError(t, err)

	resp, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks: []*pb.BlockData{
			{BlockNumber: 1, RawBlockBytes: raw1, BackupData: backup1},
			{BlockNumber: 2, RawBlockBytes: raw2, BackupData: backup2},
			{BlockNumber: 3, RawBlockBytes: raw3, BackupData: backup3},
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, uint64(2), resp.GetSyncedCount())
	require.Equal(t, uint64(2), resp.GetLastSyncedBlock())
	require.Contains(t, resp.GetError(), "parent-hash mismatch at block 3")

	require.Equal(t, uint64(2), storage.GetLastBlockNumber(), "Block #2 hợp lệ vừa commit không được bị lùi counter về #1")
	require.Equal(t, uint64(2), storage.GetLastGlobalExecIndex())

	rawMap2, _ := sm.GetStorageMapping().Get([]byte("blockNumber_2"))
	require.Equal(t, blk2.Header().Hash().Bytes(), rawMap2, "Mapping blockNumber_2 phải được flush xuống đĩa")
	require.NotNil(t, updatedLastBlock)
	require.Equal(t, uint64(2), updatedLastBlock.Header().BlockNumber())
}

// ADV-22: Khi Validator gọi SyncBlocks với PreserveOwnCommitIndex=true và có đăng ký pushAsyncGEIUpdateCallback,
// bước finalizer cuối hàm KHÔNG ĐƯỢC ghi đè LastHandledCommitIndex của Validator bằng CommitIndex của peer.
func TestAdv22_SyncBlocks_PreserveOwnCommitIndexRespectedInFinalizer(t *testing.T) {
	rh, sm, cs, _, genHash := setupAdvSyncHarness(t)

	storage.ForceSetLastHandledCommitIndex(5)
	storage.UpdateLastHandledCommitEpoch(0)

	var pushedGEI uint64
	var pushedCommitIdx uint32
	rh.SetPushAsyncGEIUpdateCallback(func(gei uint64, hash []byte, commitIndex uint32, epoch uint64) {
		pushedGEI = gei
		pushedCommitIdx = commitIndex
	})

	accRoot := cs.GetAccountStateDB().Trie().Hash()
	stakeRoot := cs.GetStakeStateDB().Trie().Hash()

	blk1Hdr := block.NewBlockHeader(genHash, 1, accRoot, stakeRoot, common.Hash{}, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1Hdr.SetCommitIndex(77)
	blk1 := block.NewBlock(blk1Hdr, nil, nil)
	raw1, err := blk1.Marshal()
	require.NoError(t, err)
	backup1, err := storage.SerializeBackupDb(storage.BackUpDb{BockNumber: 1})
	require.NoError(t, err)

	resp, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode:            true,
		PreserveOwnCommitIndex: true,
		Blocks: []*pb.BlockData{
			{BlockNumber: 1, RawBlockBytes: raw1, BackupData: backup1},
		},
	})
	require.NoError(t, callErr)
	require.Empty(t, resp.GetError())
	require.Equal(t, uint64(1), resp.GetSyncedCount())
	require.Equal(t, uint64(1), pushedGEI)
	require.Equal(t, uint32(0), pushedCommitIdx, "PreserveOwnCommitIndex=true không được đẩy peer CommitIndex=77 vào GEI callback")
	require.Equal(t, uint32(5), storage.GetLastHandledCommitIndex(), "Validator LastHandledCommitIndex=5 không được bị ghi đè thành 77")
	persistedCI, _ := sm.GetStorageBackupDb().Get(storage.LastHandledCommitIndexHashKey.Bytes())
	require.Empty(t, persistedCI, "PreserveOwnCommitIndex=true không được ghi đè LastHandledCommitIndexHashKey xuống BackupDb")
}

type pebbleStorageAdapter struct {
	*storage.PebbleDB
	backupPath string
}

func (p *pebbleStorageAdapter) Open() error           { return p.PebbleDB.Open(1) }
func (p *pebbleStorageAdapter) GetBackupPath() string { return p.backupPath }

func openPebbleStorageForTest(t *testing.T, dir, backupPath string) storage.Storage {
	t.Helper()
	pdb := storage.NewPebbleDB(dir)
	require.NoError(t, pdb.Open(1))
	t.Cleanup(func() { _ = pdb.Close() })
	return &pebbleStorageAdapter{PebbleDB: pdb, backupPath: backupPath}
}

func setupPebbleAdvSyncHarness(t *testing.T, backend string) (*RequestHandler, *storage.StorageManager, *blockchain.ChainState, *block.BlockDatabase, common.Hash) {
	t.Helper()

	prevBackend := trie.GetStateBackend()
	prevBackupDb := storage.GetBackupDbInstance()
	prevBlockNum := storage.GetLastBlockNumber()
	prevGEI := storage.GetLastGlobalExecIndex()
	prevCommitIdx := storage.GetLastHandledCommitIndex()
	prevCommitEpoch := storage.GetLastHandledCommitEpoch()
	prevNomtBlock := storage.GetLastNomtCommittedBlock()

	t.Cleanup(func() {
		storage.SetBackupDbInstance(prevBackupDb)
		trie.SetStateBackend(prevBackend)
		storage.ResetAllBlockCounters(prevBlockNum)
		storage.ForceSetLastGlobalExecIndex(prevGEI)
		storage.ForceSetLastHandledCommitIndex(prevCommitIdx)
		storage.UpdateLastHandledCommitEpoch(prevCommitEpoch)
		storage.ForceSetLastNomtCommittedBlock(prevNomtBlock)
	})

	rootDir := t.TempDir()
	trie.SetStateBackend(backend)
	if backend == trie.BackendNOMT {
		trie.CloseNomtDB()
		require.NoError(t, trie.InitNomtDB(filepath.Join(rootDir, "nomt"), 1, 16, 16))
		t.Cleanup(func() {
			trie.CloseNomtDB()
		})
	}
	storage.ForceSetLastNomtCommittedBlock(0)

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
	openPebble("backup", "", sm.AddStorageBackupDb)
	openPebble("mapping", "", sm.AddStorageMapping)
	openPebble("stake", "stake_db", sm.AddStorageStake)

	blockDB := block.NewBlockDatabase(sm.GetStorageBlock())
	genHeader := block.NewBlockHeader(common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{}, common.Address{}, 0, common.Hash{}, 0)
	genBlock := block.NewBlock(genHeader, nil, nil)
	genHash := genBlock.Header().Hash()

	var cfg *config.SimpleChainConfig
	backupPath := "skip_epoch_data"
	if backend == trie.BackendNOMT {
		cfg = &config.SimpleChainConfig{
			ChainId:   big.NewInt(991),
			IsRPCNode: true,
			Databases: config.DatabasesConfig{RootPath: rootDir},
		}
		backupPath = filepath.Join(rootDir, "backup")
	} else {
		cfg = &config.SimpleChainConfig{ChainId: big.NewInt(991)}
	}

	cs, err := blockchain.NewChainStateWithGenesis(sm, blockDB, genHeader, cfg, map[common.Address]struct{}{}, &config.GenesisConfig{}, backupPath)
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })

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

	rh := NewRequestHandler(sm, cs, "")
	return rh, sm, cs, blockDB, genHash
}

func buildMptRootAndBatch(t *testing.T, producerDB storage.Storage, prevRoot common.Hash, kvs [][2][]byte) (common.Hash, []byte) {
	t.Helper()
	tr, err := trie.NewStateTrie(prevRoot, producerDB, true)
	require.NoError(t, err)
	for _, kv := range kvs {
		require.NoError(t, tr.Update(kv[0], kv[1]))
	}
	newRoot, nodeSet, _, err := tr.Commit(false)
	require.NoError(t, err)

	var batch [][2][]byte
	if nodeSet != nil && len(nodeSet.Nodes) > 0 {
		for _, n := range nodeSet.Nodes {
			if n.Hash != (common.Hash{}) {
				batch = append(batch, [2][]byte{n.Hash.Bytes(), n.Blob})
			}
		}
		if len(batch) > 0 {
			require.NoError(t, producerDB.BatchPut(batch))
		}
	}
	serialized, err := storage.SerializeBatch(batch)
	require.NoError(t, err)
	return newRoot, serialized
}

func makeNomtTestKey(raw []byte) []byte {
	out := make([]byte, 5+len(raw))
	copy(out[:5], "nomt:")
	copy(out[5:], raw)
	return out
}

func makeNomtTestSCKey(addr common.Address, slotByte byte) []byte {
	slot := make([]byte, 32)
	slot[31] = slotByte
	out := make([]byte, 20+5+32)
	copy(out[:20], addr[:])
	copy(out[20:25], "nomt:")
	copy(out[25:], slot)
	return out
}

// ADV-23: Khi applyBackupDbBatches đã ghi vào toàn bộ các Storage của chain (Block, Code,
// SmartContract, SC Storage, Receipt, Transaction, Mapping, TrieDB) và Xapian, nhưng sau đó block
// thất bại ở bước ReplayFullDbLogs (Subcase A) hoặc CommitBlockState (Subcase B), dữ liệu chain
// trong MỌI storage + BlockChain cache phải được rollback sạch sẽ về trạng thái Block #1 hệt như Xapian.
func TestAdv23_SyncBlocks_FailedBlockRollsBackAllChainStoragesAlongsideXapian(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	rh, sm, cs, blockDB, genHash := setupPebbleAdvSyncHarness(t, trie.BackendMPT)
	bc := blockchain.GetBlockChainInstance()
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())

	producerAccDB := openPebbleStorageForTest(t, filepath.Join(t.TempDir(), "prod_acc"), "account_state")
	producerStakeDB := openPebbleStorageForTest(t, filepath.Join(t.TempDir(), "prod_stake"), "stake_db")

	initAccRoot := cs.GetAccountStateDB().Trie().Hash()
	initStakeRoot := cs.GetStakeStateDB().Trie().Hash()

	accKey1 := common.HexToAddress("0x1000000000000000000000000000000000000001").Bytes()
	stakeKey1 := common.HexToAddress("0x2000000000000000000000000000000000000001").Bytes()
	accRoot1, accBatch1 := buildMptRootAndBatch(t, producerAccDB, initAccRoot, [][2][]byte{{accKey1, []byte("acc_state_v1")}})
	stakeRoot1, stakeBatch1 := buildMptRootAndBatch(t, producerStakeDB, initStakeRoot, [][2][]byte{{stakeKey1, []byte("stake_state_v1")}})

	// Sync Block #1 hợp lệ ghi đủ các Storage của chain + Xapian
	tx1 := common.HexToHash("0x1111000000000000000000000000000000000000000000000000000000000001")
	txsRoot1 := common.HexToHash("0xaaaa000000000000000000000000000000000000000000000000000000000001")
	blk1Hdr := block.NewBlockHeader(genHash, 1, accRoot1, stakeRoot1, txsRoot1, common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1 := block.NewBlock(blk1Hdr, []common.Hash{tx1}, nil)
	blk1Hash := blk1.Header().Hash()
	raw1, err := blk1.Marshal()
	require.NoError(t, err)

	codeK1 := []byte("code_k1")
	scK1 := []byte("sc_meta_k1")
	slotK1 := []byte("sc_slot_k1")
	customMapK1 := []byte("custom_map_k1")
	trieAddrHex := "1000000000000000000000000000000000002301"
	trieDbName := "inv_db"
	trieKeyPath := fmt.Sprintf("%x:%s:", crypto.Keccak256([]byte(trieDbName)), trieAddrHex)
	trieItem1 := []byte("item_1")

	b1BockBatch, err := storage.SerializeBatch([][2][]byte{{blk1Hash.Bytes(), raw1}})
	require.NoError(t, err)
	b1CodeBatch, err := storage.SerializeBatch([][2][]byte{{codeK1, []byte("code_v1")}})
	require.NoError(t, err)
	b1ScBatch, err := storage.SerializeBatch([][2][]byte{{scK1, []byte("sc_v1")}})
	require.NoError(t, err)
	b1ScStorageBatch, err := storage.SerializeBatch([][2][]byte{{slotK1, []byte("slot_v1")}})
	require.NoError(t, err)
	b1RcptBatch, err := storage.SerializeBatch([][2][]byte{{tx1.Bytes(), []byte("rcpt_v1")}})
	require.NoError(t, err)
	b1TxBatch, err := storage.SerializeBatch([][2][]byte{{tx1.Bytes(), []byte("tx_v1")}})
	require.NoError(t, err)
	b1MapBatch, err := storage.SerializeBatch([][2][]byte{
		{customMapK1, []byte("map_v1")},
		{[]byte("blockNumber_1"), blk1Hash.Bytes()},
	})
	require.NoError(t, err)
	b1TrieBatch, err := storage.SerializeBatch([][2][]byte{{trieItem1, []byte("qty_10")}})
	require.NoError(t, err)
	logB1 := buildMinimalXapianCompLog("db_adv23", "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "xap_b1")

	backup1, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber:                1,
		BockBatch:                 b1BockBatch,
		AccountBatch:              accBatch1,
		StakeState:                stakeBatch1,
		CodeBatchPut:              b1CodeBatch,
		SmartContractBatch:        b1ScBatch,
		SmartContractStorageBatch: b1ScStorageBatch,
		ReceiptBatchPut:           b1RcptBatch,
		TxBatchPut:                b1TxBatch,
		MapppingBatch:             b1MapBatch,
		TrieDatabaseBatchPut:      map[string][]byte{trieAddrHex + "/" + trieDbName: b1TrieBatch},
		FullDbLogs:                []map[string][]byte{{trieAddrHex: logB1}},
	})
	require.NoError(t, err)

	resp1, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks:      []*pb.BlockData{{BlockNumber: 1, RawBlockBytes: raw1, BackupData: backup1}},
	})
	require.NoError(t, callErr)
	require.Empty(t, resp1.GetError())
	require.Equal(t, uint64(1), resp1.GetSyncedCount())
	require.Equal(t, accRoot1, cs.GetAccountStateDB().Trie().Hash())

	// Chuẩn bị Block #2 với hàng loạt đột biến ghi đè dữ liệu Block #1 + thêm key mới của Block #2
	tx2 := common.HexToHash("0x2222000000000000000000000000000000000000000000000000000000000002")
	txsRoot2 := common.HexToHash("0xbbbb000000000000000000000000000000000000000000000000000000000002")
	accRoot2, accBatch2 := buildMptRootAndBatch(t, producerAccDB, accRoot1, [][2][]byte{{accKey1, []byte("acc_state_v2")}})

	blk2HdrValid := block.NewBlockHeader(blk1Hash, 2, accRoot2, stakeRoot1, txsRoot2, common.Address{}, 2000, common.Hash{}, 0, 2)
	blk2Valid := block.NewBlock(blk2HdrValid, []common.Hash{tx2}, nil)
	blk2ValidHash := blk2Valid.Header().Hash()
	raw2Valid, err := blk2Valid.Marshal()
	require.NoError(t, err)

	codeK2 := []byte("code_k2_new")
	trieItem2 := []byte("item_2_new")
	buildMutatingBackup2 := func(bHash common.Hash, rawB []byte, useAccBatch bool, xapLogs []map[string][]byte) []byte {
		bockB, _ := storage.SerializeBatch([][2][]byte{{bHash.Bytes(), rawB}})
		codeB, _ := storage.SerializeBatch([][2][]byte{{codeK1, []byte("code_OVERWRITTEN_b2")}, {codeK2, []byte("code_v2")}})
		scB, _ := storage.SerializeBatch([][2][]byte{{scK1, []byte("sc_OVERWRITTEN_b2")}})
		scSlotB, _ := storage.SerializeBatch([][2][]byte{{slotK1, []byte("slot_OVERWRITTEN_b2")}})
		rcptB, _ := storage.SerializeBatch([][2][]byte{{tx2.Bytes(), []byte("rcpt_v2")}})
		txB, _ := storage.SerializeBatch([][2][]byte{{tx2.Bytes(), []byte("tx_v2")}})
		mapB, _ := storage.SerializeBatch([][2][]byte{
			{customMapK1, []byte("map_OVERWRITTEN_b2")},
			{[]byte("blockNumber_2"), bHash.Bytes()},
			{[]byte("txHashPrefix" + tx2.Hex()), []byte{0, 0, 0, 0, 0, 0, 0, 2}},
		})
		trieB, _ := storage.SerializeBatch([][2][]byte{
			{trieItem1, []byte("qty_OVERWRITTEN_b2")},
			{trieItem2, []byte("qty_20")},
		})
		var ab []byte
		if useAccBatch {
			ab = accBatch2
		}
		out, serErr := storage.SerializeBackupDb(storage.BackUpDb{
			BockNumber:                2,
			BockBatch:                 bockB,
			AccountBatch:              ab,
			CodeBatchPut:              codeB,
			SmartContractBatch:        scB,
			SmartContractStorageBatch: scSlotB,
			ReceiptBatchPut:           rcptB,
			TxBatchPut:                txB,
			MapppingBatch:             mapB,
			TrieDatabaseBatchPut:      map[string][]byte{trieAddrHex + "/" + trieDbName: trieB},
			FullDbLogs:                xapLogs,
		})
		require.NoError(t, serErr)
		return out
	}

	assertAllStoragesIntactAtBlock1 := func(subcase string, rejectedBlockHash common.Hash) {
		t.Helper()
		require.Equal(t, uint64(1), storage.GetLastBlockNumber(), "%s: LastBlockNumber phải giữ nguyên 1", subcase)
		lastBlk, lErr := blockDB.GetLastBlock()
		require.NoError(t, lErr)
		require.Equal(t, blk1Hash, lastBlk.Header().Hash(), "%s: BlockDB last block phải giữ Block #1", subcase)
		require.Equal(t, accRoot1, cs.GetAccountStateDB().Trie().Hash(), "%s: ChainState AccountStatesRoot phải giữ Block #1", subcase)
		_, hasHash2 := bc.GetBlockHashByNumber(2)
		require.False(t, hasHash2, "%s: bc không được giữ mapping block 2", subcase)
		_, hasTx2Map := bc.GetBlockNumberByTxHash(tx2)
		require.False(t, hasTx2Map, "%s: bc không được giữ tx2 mapping", subcase)

		_, blk2Err := blockDB.GetBlockByHash(rejectedBlockHash)
		require.Error(t, blk2Err, "%s: Block #2 lỗi phải bị xóa khỏi StorageBlock", subcase)

		vCode1, err := sm.GetStorageCode().Get(codeK1)
		require.NoError(t, err)
		require.Equal(t, []byte("code_v1"), vCode1, "%s: Code k1 bị ghi đè phải rollback về v1", subcase)
		_, errCode2 := sm.GetStorageCode().Get(codeK2)
		require.Error(t, errCode2, "%s: Code k2 mới thêm bởi Block #2 lỗi phải bị xóa", subcase)

		vSc1, err := sm.GetStorageSmartContract().Get(scK1)
		require.NoError(t, err)
		require.Equal(t, []byte("sc_v1"), vSc1, "%s: SmartContract k1 phải rollback về v1", subcase)
		vSlot1, err := sm.GetStorageSmartContract().Get(slotK1)
		require.NoError(t, err)
		require.Equal(t, []byte("slot_v1"), vSlot1, "%s: SC Storage slot k1 phải rollback về v1", subcase)

		vRcpt1, err := sm.GetStorageReceipt().Get(tx1.Bytes())
		require.NoError(t, err)
		require.Equal(t, []byte("rcpt_v1"), vRcpt1)
		_, errRcpt2 := sm.GetStorageReceipt().Get(tx2.Bytes())
		require.Error(t, errRcpt2, "%s: Receipt tx2 phải bị xóa", subcase)

		vTx1, err := sm.GetStorageTransaction().Get(tx1.Bytes())
		require.NoError(t, err)
		require.Equal(t, []byte("tx_v1"), vTx1)
		_, errTx2 := sm.GetStorageTransaction().Get(tx2.Bytes())
		require.Error(t, errTx2, "%s: Transaction tx2 phải bị xóa", subcase)

		vMapCustom, err := sm.GetStorageMapping().Get(customMapK1)
		require.NoError(t, err)
		require.Equal(t, []byte("map_v1"), vMapCustom, "%s: Mapping custom_map_1 phải rollback về v1", subcase)
		_, errMap2 := sm.GetStorageMapping().Get([]byte("blockNumber_2"))
		require.Error(t, errMap2, "%s: Mapping blockNumber_2 phải bị xóa", subcase)
		_, errMapTx2 := sm.GetStorageMapping().Get([]byte("txHashPrefix" + tx2.Hex()))
		require.Error(t, errMapTx2, "%s: Mapping txHashPrefix tx2 phải bị xóa", subcase)

		prefTrie := storage.NewPrefixStorage(sm.GetStorageDatabaseTrie(), trieKeyPath)
		vTrie1, err := prefTrie.Get(trieItem1)
		require.NoError(t, err)
		require.Equal(t, []byte("qty_10"), vTrie1, "%s: TrieDB item1 phải rollback về qty_10", subcase)
		_, errTrie2 := prefTrie.Get(trieItem2)
		require.Error(t, errTrie2, "%s: TrieDB item2 phải bị xóa", subcase)

		rolledAbove1, rbErr := mvm.RollbackXapianAboveBlock(1)
		require.NoError(t, rbErr)
		require.Equal(t, 0, rolledAbove1, "%s: Xapian ở Block #2 phải đã rollback sạch về Block #1", subcase)
	}

	// Subcase A: ReplayFullDbLogs C++ thất bại SAU KHI toàn bộ KV & TrieDB batches đã ghi vào DB
	corruptLog := []byte{0xFF, 0x00, 0x01, 0x02}
	backup2A := buildMutatingBackup2(blk2ValidHash, raw2Valid, true, []map[string][]byte{{trieAddrHex: corruptLog}})
	resp2A, err := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks:      []*pb.BlockData{{BlockNumber: 2, RawBlockBytes: raw2Valid, BackupData: backup2A}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp2A.GetError())
	require.Equal(t, uint64(0), resp2A.GetSyncedCount())
	assertAllStoragesIntactAtBlock1("Subcase A (ReplayFullDbLogs failure)", blk2ValidHash)

	// Subcase B: CommitBlockState thất bại SAU KHI KV + TrieDB + Xapian hợp lệ đều đã áp dụng
	missingRoot2 := common.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000002")
	blk2BadHdr := block.NewBlockHeader(blk1Hash, 2, missingRoot2, stakeRoot1, txsRoot2, common.Address{}, 2500, common.Hash{}, 0, 2)
	blk2Bad := block.NewBlock(blk2BadHdr, []common.Hash{tx2}, nil)
	blk2BadHash := blk2Bad.Header().Hash()
	raw2Bad, err := blk2Bad.Marshal()
	require.NoError(t, err)

	validLogB2 := buildMinimalXapianCompLog("db_adv23", "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "xap_bad_b2")
	backup2B := buildMutatingBackup2(blk2BadHash, raw2Bad, false, []map[string][]byte{{trieAddrHex: validLogB2}})
	resp2B, err := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks:      []*pb.BlockData{{BlockNumber: 2, RawBlockBytes: raw2Bad, BackupData: backup2B}},
	})
	require.NoError(t, err)
	require.Contains(t, resp2B.GetError(), "CommitBlockState failed for block 2")
	require.Equal(t, uint64(0), resp2B.GetSyncedCount())
	assertAllStoragesIntactAtBlock1("Subcase B (CommitBlockState failure)", blk2BadHash)

	// Verify Block #1 Xapian commit is genuinely still alive at block 1
	rolledAbove0, rbErr := mvm.RollbackXapianAboveBlock(0)
	require.NoError(t, rbErr)
	require.Equal(t, 1, rolledAbove0, "Xapian Block #1 hợp lệ vẫn phải còn nguyên trước khi rollback về 0")
}

// ADV-24: Kiểm tra 2 bảo đảm fork rollback của dữ liệu chain:
//  1. Phần A: Khi peer gửi Block #2 fork nhưng corrupt (nil BackupData / raw bytes rác / AccountBatch hỏng),
//     forkOverwrite bị trì hoãn cho tới sau pre-validation => Block #2,#3 lành mạnh cục bộ KHÔNG bị phá hủy.
//  2. Phần B: Khi gặp parent-hash mismatch ở executedCount == 0, rollbackLocalChainToBlock phải dọn sạch
//     dữ liệu chain của cả nhánh fork (#2, #3) trong BlockDB, Mapping, Tx, Receipt, BackupDb, ChainState
//     và Block counters đồng bộ với RollbackXapianAboveBlock.
func TestAdv24_SyncBlocks_DeferredForkValidationAndSymmetricForkChainRollback(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	rh, sm, cs, blockDB, genHash := setupPebbleAdvSyncHarness(t, trie.BackendMPT)
	bc := blockchain.GetBlockChainInstance()
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())

	producerAccDB := openPebbleStorageForTest(t, filepath.Join(t.TempDir(), "prod_acc"), "account_state")

	initAccRoot := cs.GetAccountStateDB().Trie().Hash()
	stakeRoot := cs.GetStakeStateDB().Trie().Hash()
	contractHex := "1000000000000000000000000000000000002401"
	addr1 := common.HexToAddress("0x1000000000000000000000000000000000000001").Bytes()
	addr2 := common.HexToAddress("0x2000000000000000000000000000000000000002").Bytes()
	addr3 := common.HexToAddress("0x3000000000000000000000000000000000000003").Bytes()

	syncValidBlock := func(bn uint64, parentHash common.Hash, prevAccRoot common.Hash, accK, accV []byte, txHash common.Hash, ts uint64, xapPayload string) (types.Block, common.Hash) {
		accRoot, accBatch := buildMptRootAndBatch(t, producerAccDB, prevAccRoot, [][2][]byte{{accK, accV}})
		txsRoot := common.BytesToHash([]byte{byte(bn), 0x24})
		hdr := block.NewBlockHeader(parentHash, bn, accRoot, stakeRoot, txsRoot, common.Address{}, ts, common.Hash{}, 0, bn)
		blk := block.NewBlock(hdr, []common.Hash{txHash}, nil)
		rawB, err := blk.Marshal()
		require.NoError(t, err)

		rcptB, _ := storage.SerializeBatch([][2][]byte{{txHash.Bytes(), []byte(fmt.Sprintf("rcpt_%d", bn))}})
		txB, _ := storage.SerializeBatch([][2][]byte{{txHash.Bytes(), []byte(fmt.Sprintf("tx_%d", bn))}})
		logFrame := buildMinimalXapianCompLog("db_adv24", fmt.Sprintf("0x%064x", bn), xapPayload)
		backupBytes, err := storage.SerializeBackupDb(storage.BackUpDb{
			BockNumber:      bn,
			AccountBatch:    accBatch,
			ReceiptBatchPut: rcptB,
			TxBatchPut:      txB,
			FullDbLogs:      []map[string][]byte{{contractHex: logFrame}},
		})
		require.NoError(t, err)

		resp, err := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
			ExecuteMode: true,
			Blocks:      []*pb.BlockData{{BlockNumber: bn, RawBlockBytes: rawB, BackupData: backupBytes}},
		})
		require.NoError(t, err)
		require.Empty(t, resp.GetError())
		require.Equal(t, uint64(1), resp.GetSyncedCount())
		return blk, accRoot
	}

	tx1 := common.HexToHash("0x1111000000000000000000000000000000000000000000000000000000000011")
	tx2Stale := common.HexToHash("0x2222000000000000000000000000000000000000000000000000000000000022")
	tx3Stale := common.HexToHash("0x3333000000000000000000000000000000000000000000000000000000000033")

	blk1, accRoot1 := syncValidBlock(1, genHash, initAccRoot, addr1, []byte("b1"), tx1, 1000, "canon_b1")
	blk1Hash := blk1.Header().Hash()
	blk2Stale, _ := syncValidBlock(2, blk1Hash, accRoot1, addr2, []byte("b2_stale"), tx2Stale, 2000, "stale_b2")
	blk2StaleHash := blk2Stale.Header().Hash()
	blk3Stale, accRoot3Stale := syncValidBlock(3, blk2StaleHash, blk2Stale.Header().AccountStatesRoot(), addr3, []byte("b3_stale"), tx3Stale, 3000, "stale_b3")
	blk3StaleHash := blk3Stale.Header().Hash()

	require.Equal(t, uint64(3), storage.GetLastBlockNumber())
	require.Equal(t, accRoot3Stale, cs.GetAccountStateDB().Trie().Hash())

	// Phần A: Peer gửi Block #2 khác hash (kích hoạt forkOverwrite=true), nhưng kèm payload lỗi
	tx2PeerBad := common.HexToHash("0x9999000000000000000000000000000000000000000000000000000000000099")
	blk2PeerBadHdr := block.NewBlockHeader(blk1Hash, 2, accRoot1, stakeRoot, common.HexToHash("0xbeef"), common.Address{}, 2999, common.Hash{}, 0, 2)
	blk2PeerBad := block.NewBlock(blk2PeerBadHdr, []common.Hash{tx2PeerBad}, nil)
	raw2PeerBad, err := blk2PeerBad.Marshal()
	require.NoError(t, err)

	corruptSubBatchBackup, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber:   2,
		AccountBatch: []byte{0xFF, 0xFF, 0xFF}, // corrupt encoded batch
	})
	require.NoError(t, err)

	for _, badData := range [][]byte{nil, {0xDE, 0xAD}, corruptSubBatchBackup} {
		respBad, callErr := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
			ExecuteMode: true,
			Blocks:      []*pb.BlockData{{BlockNumber: 2, RawBlockBytes: raw2PeerBad, BackupData: badData}},
		})
		require.NoError(t, callErr)
		require.NotEmpty(t, respBad.GetError())
		require.Equal(t, uint64(0), respBad.GetSyncedCount())

		// State cục bộ ở Block #3 PHẢI còn nguyên vẹn tuyệt đối
		require.Equal(t, uint64(3), storage.GetLastBlockNumber())
		lastB, lErr := blockDB.GetLastBlock()
		require.NoError(t, lErr)
		require.Equal(t, blk3StaleHash, lastB.Header().Hash())
		h2, ok2 := bc.GetBlockHashByNumber(2)
		require.True(t, ok2)
		require.Equal(t, blk2StaleHash, h2)
		require.Equal(t, accRoot3Stale, cs.GetAccountStateDB().Trie().Hash())
	}

	// Phần B: Nhận Block #3 sai parentHash (so với blk2StaleHash) ở executedCount == 0
	// => rollbackLocalChainToBlock(1) phải dọn sạch cả Block #2 & Block #3 stale và kéo chain + Xapian về #1!
	bogusParent := common.HexToHash("0x7777000000000000000000000000000000000000000000000000000000000003")
	blk3MismatchHdr := block.NewBlockHeader(bogusParent, 3, accRoot1, stakeRoot, common.Hash{}, common.Address{}, 3500, common.Hash{}, 0, 3)
	blk3Mismatch := block.NewBlock(blk3MismatchHdr, nil, nil)
	raw3Mismatch, err := blk3Mismatch.Marshal()
	require.NoError(t, err)
	backup3Empty, err := storage.SerializeBackupDb(storage.BackUpDb{BockNumber: 3})
	require.NoError(t, err)

	respMismatch, err := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks:      []*pb.BlockData{{BlockNumber: 3, RawBlockBytes: raw3Mismatch, BackupData: backup3Empty}},
	})
	require.NoError(t, err)
	require.Contains(t, respMismatch.GetError(), "parent-hash mismatch at block 3")
	require.Equal(t, uint64(0), respMismatch.GetSyncedCount())

	// Kiểm tra toàn bộ dữ liệu chain và Xapian đều đã quay về đúng Block #1
	require.Equal(t, uint64(1), storage.GetLastBlockNumber())
	require.Equal(t, uint64(1), storage.GetLastGlobalExecIndex())
	require.Equal(t, uint64(1), (*cs.GetcurrentBlockHeader()).BlockNumber())
	require.Equal(t, accRoot1, cs.GetAccountStateDB().Trie().Hash())

	lastB, err := blockDB.GetLastBlock()
	require.NoError(t, err)
	require.Equal(t, blk1Hash, lastB.Header().Hash(), "BlockDB lastBlock phải được phục hồi về Block #1")

	_, errB2 := blockDB.GetBlockByHash(blk2StaleHash)
	require.Error(t, errB2, "blk2Stale phải bị xóa khỏi BlockDB")
	_, errB3 := blockDB.GetBlockByHash(blk3StaleHash)
	require.Error(t, errB3, "blk3Stale phải bị xóa khỏi BlockDB")

	_, ok2 := bc.GetBlockHashByNumber(2)
	require.False(t, ok2, "Mapping blockNumber_2 phải bị xóa")
	_, ok3 := bc.GetBlockHashByNumber(3)
	require.False(t, ok3, "Mapping blockNumber_3 phải bị xóa")
	gotBn1, okTx1 := bc.GetBlockNumberByTxHash(tx1)
	require.True(t, okTx1)
	require.Equal(t, uint64(1), gotBn1)
	_, okTx2 := bc.GetBlockNumberByTxHash(tx2Stale)
	require.False(t, okTx2, "Mapping tx2Stale phải bị xóa")
	_, okTx3 := bc.GetBlockNumberByTxHash(tx3Stale)
	require.False(t, okTx3, "Mapping tx3Stale phải bị xóa")

	// BackupDb topic của #2, #3 phải bị xóa, còn #1 giữ nguyên
	bkp1Saved, err := sm.GetStorageBackupDb().Get([]byte("block_data_topic-1"))
	require.NoError(t, err)
	require.NotEmpty(t, bkp1Saved)
	_, errBkp2 := sm.GetStorageBackupDb().Get([]byte("block_data_topic-2"))
	require.Error(t, errBkp2)
	_, errBkp3 := sm.GetStorageBackupDb().Get([]byte("block_data_topic-3"))
	require.Error(t, errBkp3)

	// Receipt/Transaction của #2, #3 phải bị xóa, còn #1 giữ nguyên
	_, errTx1Store := sm.GetStorageTransaction().Get(tx1.Bytes())
	require.NoError(t, errTx1Store)
	_, errTx2Store := sm.GetStorageTransaction().Get(tx2Stale.Bytes())
	require.Error(t, errTx2Store)
	_, errRcpt3Store := sm.GetStorageReceipt().Get(tx3Stale.Bytes())
	require.Error(t, errRcpt3Store)

	rolledAbove1, rbErr := mvm.RollbackXapianAboveBlock(1)
	require.NoError(t, rbErr)
	require.Equal(t, 0, rolledAbove1, "Xapian của nhánh fork #2,#3 phải đã rollback sạch về Block #1")
	rolledAbove0, rbErr := mvm.RollbackXapianAboveBlock(0)
	require.NoError(t, rbErr)
	require.Equal(t, 1, rolledAbove0, "Xapian của Block #1 phải vẫn còn nguyên")
}

// ADV-25: Kiểm tra rollback toàn diện trên backend NOMT (C++ NOMT handles + StateChangelogDB +
// LastNomtCommittedBlock + KV/TrieDB storages + BlockDB/Mapping + Xapian) trong cả hai tình huống:
//  1. Subcase A: Block #2 lỗi (NOMT accountStatesRoot mismatch sau khi StakeState, SC Storage, KV, TrieDB
//     và Xapian đều đã được áp dụng) -> C++ NOMT roots, StateChangelogDB, chain storages và Xapian cùng lùi về Block #1.
//  2. Subcase B: Block #2 đang ở nhánh fork bị ghi đè bởi Canonical Block #2 -> C++ NOMT roots
//     (kể cả smart_contract_storage), StateChangelogDB và dữ liệu chain khớp 100% node sạch.
func TestAdv25_SyncBlocks_NomtBackendRollsBackHandlesChangelogsChainKVAndXapianTogether(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	acct1 := common.HexToAddress("0x1000000000000000000000000000000000000001").Bytes()
	acct2 := common.HexToAddress("0x2000000000000000000000000000000000000002").Bytes()
	stake1 := common.HexToAddress("0x3000000000000000000000000000000000000001").Bytes()
	stake2 := common.HexToAddress("0x4000000000000000000000000000000000000002").Bytes()
	scAddrA := common.HexToAddress("0xA000000000000000000000000000000000002501")
	scAddrB := common.HexToAddress("0xB000000000000000000000000000000000002502")

	// Precompute exact expected NOMT Merkle roots on clean scratch NOMT DBs (closed before node harness starts)
	prevBackend := trie.GetStateBackend()
	trie.CloseNomtDB()
	trie.SetStateBackend(trie.BackendNOMT)

	computeNomtSequence := func(steps []struct {
		bn    uint64
		acc   [][2][]byte
		stake [][2][]byte
		sc    [][2][]byte
	}) (accRoots, stakeRoots, scRoots []common.Hash) {
		trie.CloseNomtDB()
		require.NoError(t, trie.InitNomtDB(filepath.Join(t.TempDir(), "scratch_nomt"), 1, 16, 16))
		storage.ForceSetLastNomtCommittedBlock(0)
		for _, st := range steps {
			agg := map[string][][2][]byte{
				"Account":    st.acc,
				"StakeState": st.stake,
				"SC Storage": st.sc,
			}
			_, err := trie.ApplyNomtReplicationBatches(agg, nil, nil, st.bn)
			require.NoError(t, err)
			aR, _ := trie.GetNomtHandleRoot("account_state")
			sR, _ := trie.GetNomtHandleRoot("stake_db")
			cR, _ := trie.GetNomtHandleRoot("smart_contract_storage")
			accRoots = append(accRoots, aR)
			stakeRoots = append(stakeRoots, sR)
			scRoots = append(scRoots, cR)
		}
		trie.CloseNomtDB()
		return accRoots, stakeRoots, scRoots
	}

	b1Acc := [][2][]byte{{makeNomtTestKey(acct1), []byte("nomt_acc_b1")}}
	b1Stake := [][2][]byte{{makeNomtTestKey(stake1), []byte("nomt_stake_b1")}}
	b1SC := [][2][]byte{{makeNomtTestSCKey(scAddrA, 1), []byte("nomt_sc_a1_b1")}}

	b2ForkAcc := [][2][]byte{
		{makeNomtTestKey(acct1), []byte("nomt_acc_fork_b2")},
		{makeNomtTestKey(acct2), []byte("nomt_acc2_fork_b2")},
	}
	b2ForkStake := [][2][]byte{{makeNomtTestKey(stake1), []byte("nomt_stake_fork_b2")}}
	b2ForkSC := [][2][]byte{
		{makeNomtTestSCKey(scAddrA, 1), []byte("nomt_sc_a1_fork_b2")},
		{makeNomtTestSCKey(scAddrB, 2), []byte("nomt_sc_b2_fork_b2")},
	}

	b2CanonAcc := [][2][]byte{{makeNomtTestKey(acct1), []byte("nomt_acc_canon_b2")}}
	b2CanonStake := [][2][]byte{{makeNomtTestKey(stake2), []byte("nomt_stake2_canon_b2")}}
	b2CanonSC := [][2][]byte{{makeNomtTestSCKey(scAddrA, 1), []byte("nomt_sc_a1_canon_b2")}}

	forkAccR, forkStakeR, forkScR := computeNomtSequence([]struct {
		bn    uint64
		acc   [][2][]byte
		stake [][2][]byte
		sc    [][2][]byte
	}{
		{1, b1Acc, b1Stake, b1SC},
		{2, b2ForkAcc, b2ForkStake, b2ForkSC},
	})
	canonAccR, canonStakeR, canonScR := computeNomtSequence([]struct {
		bn    uint64
		acc   [][2][]byte
		stake [][2][]byte
		sc    [][2][]byte
	}{
		{1, b1Acc, b1Stake, b1SC},
		{2, b2CanonAcc, b2CanonStake, b2CanonSC},
	})
	trie.SetStateBackend(prevBackend)

	accRoot1, stakeRoot1, scRoot1 := canonAccR[0], canonStakeR[0], canonScR[0]
	accRoot2Fork, stakeRoot2Fork, scRoot2Fork := forkAccR[1], forkStakeR[1], forkScR[1]
	accRoot2Canon, stakeRoot2Canon, scRoot2Canon := canonAccR[1], canonStakeR[1], canonScR[1]

	rh, sm, cs, blockDB, genHash := setupPebbleAdvSyncHarness(t, trie.BackendNOMT)
	bc := blockchain.GetBlockChainInstance()
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())

	cloneKVs := func(in [][2][]byte) [][2][]byte {
		out := make([][2][]byte, len(in))
		for i, kv := range in {
			out[i] = [2][]byte{bytes.Clone(kv[0]), bytes.Clone(kv[1])}
		}
		return out
	}

	// 1. Sync Block #1 hợp lệ trên NOMT
	tx1 := common.HexToHash("0x1111000000000000000000000000000000000000000000000000000000002501")
	blk1Hdr := block.NewBlockHeader(genHash, 1, accRoot1, stakeRoot1, common.HexToHash("0x101"), common.Address{}, 1000, common.Hash{}, 0, 1)
	blk1 := block.NewBlock(blk1Hdr, []common.Hash{tx1}, nil)
	blk1Hash := blk1.Header().Hash()
	raw1, err := blk1.Marshal()
	require.NoError(t, err)

	ab1, _ := storage.SerializeBatch(cloneKVs(b1Acc))
	sb1, _ := storage.SerializeBatch(cloneKVs(b1Stake))
	scb1, _ := storage.SerializeBatch(cloneKVs(b1SC))
	rcptB1, _ := storage.SerializeBatch([][2][]byte{{tx1.Bytes(), []byte("nomt_rcpt_1")}})
	txB1, _ := storage.SerializeBatch([][2][]byte{{tx1.Bytes(), []byte("nomt_tx_1")}})
	log1 := buildMinimalXapianCompLog("db_adv25", "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "nomt_b1")
	backup1, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber:                1,
		AccountBatch:              ab1,
		StakeState:                sb1,
		SmartContractStorageBatch: scb1,
		ReceiptBatchPut:           rcptB1,
		TxBatchPut:                txB1,
		FullDbLogs:                []map[string][]byte{{scAddrA.Hex()[2:]: log1}},
	})
	require.NoError(t, err)

	resp1, err := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks:      []*pb.BlockData{{BlockNumber: 1, RawBlockBytes: raw1, BackupData: backup1}},
	})
	require.NoError(t, err)
	require.Empty(t, resp1.GetError())
	require.Equal(t, uint64(1), resp1.GetSyncedCount())
	require.Equal(t, uint64(1), storage.GetLastNomtCommittedBlock())

	gotA1, _ := trie.GetNomtHandleRoot("account_state")
	gotS1, _ := trie.GetNomtHandleRoot("stake_db")
	gotC1, _ := trie.GetNomtHandleRoot("smart_contract_storage")
	require.Equal(t, accRoot1, gotA1)
	require.Equal(t, stakeRoot1, gotS1)
	require.Equal(t, scRoot1, gotC1)
	require.Equal(t, uint64(1), cs.GetChangelogDB().GetHighestBlock())
	require.Equal(t, uint64(1), cs.GetSmartContractDB().GetChangelogDB().GetHighestBlock())

	// 2. Subcase A: Block #2 có StakeState + SC Storage + KV + Xapian nhưng AccountBatch không khớp header Root
	// => Post-apply NOMT verify phải fail & rollback toàn bộ NOMT handles, Changelogs, LastNomtCommittedBlock, KV & Xapian về Block #1!
	tx2Bad := common.HexToHash("0x2222000000000000000000000000000000000000000000000000000000002599")
	blk2BadHdr := block.NewBlockHeader(blk1Hash, 2, accRoot2Canon, stakeRoot2Fork, common.HexToHash("0x209"), common.Address{}, 1900, common.Hash{}, 0, 2)
	blk2Bad := block.NewBlock(blk2BadHdr, []common.Hash{tx2Bad}, nil)
	raw2Bad, err := blk2Bad.Marshal()
	require.NoError(t, err)

	// Gửi b2ForkAcc trong khi header đòi accRoot2Canon -> post-apply mismatch!
	ab2Wrong, _ := storage.SerializeBatch(cloneKVs(b2ForkAcc))
	sb2Fork, _ := storage.SerializeBatch(cloneKVs(b2ForkStake))
	scb2Fork, _ := storage.SerializeBatch(cloneKVs(b2ForkSC))
	rcptB2Bad, _ := storage.SerializeBatch([][2][]byte{{tx2Bad.Bytes(), []byte("nomt_rcpt_2_bad")}})
	txB2Bad, _ := storage.SerializeBatch([][2][]byte{{tx2Bad.Bytes(), []byte("nomt_tx_2_bad")}})
	log2Bad := buildMinimalXapianCompLog("db_adv25", "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "nomt_bad_b2")
	backup2Bad, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber:                2,
		AccountBatch:              ab2Wrong,
		StakeState:                sb2Fork,
		SmartContractStorageBatch: scb2Fork,
		ReceiptBatchPut:           rcptB2Bad,
		TxBatchPut:                txB2Bad,
		FullDbLogs:                []map[string][]byte{{scAddrA.Hex()[2:]: log2Bad}},
	})
	require.NoError(t, err)

	resp2Bad, err := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks:      []*pb.BlockData{{BlockNumber: 2, RawBlockBytes: raw2Bad, BackupData: backup2Bad}},
	})
	require.NoError(t, err)
	require.Contains(t, resp2Bad.GetError(), "NOMT accountStatesRoot mismatch")
	require.Equal(t, uint64(0), resp2Bad.GetSyncedCount())
	require.Equal(t, uint64(1), storage.GetLastBlockNumber())
	require.Equal(t, uint64(1), storage.GetLastNomtCommittedBlock())

	// Kiểm tra cả 3 NOMT handles + Changelogs + KV + Xapian đều quay về đúng Block #1
	afterBadA, _ := trie.GetNomtHandleRoot("account_state")
	afterBadS, _ := trie.GetNomtHandleRoot("stake_db")
	afterBadC, _ := trie.GetNomtHandleRoot("smart_contract_storage")
	require.Equal(t, accRoot1, afterBadA, "NOMT account_state handle root phải rollback về Block #1")
	require.Equal(t, stakeRoot1, afterBadS, "NOMT stake_db handle root phải rollback về Block #1")
	require.Equal(t, scRoot1, afterBadC, "NOMT smart_contract_storage handle root phải rollback về Block #1")
	require.Equal(t, uint64(1), cs.GetChangelogDB().GetHighestBlock())
	require.Equal(t, uint64(1), cs.GetStakeChangelogDB().GetHighestBlock())
	require.Equal(t, uint64(1), cs.GetSmartContractDB().GetChangelogDB().GetHighestBlock())

	_, errBadTx := sm.GetStorageTransaction().Get(tx2Bad.Bytes())
	require.Error(t, errBadTx)
	_, errBadRcpt := sm.GetStorageReceipt().Get(tx2Bad.Bytes())
	require.Error(t, errBadRcpt)
	rolledAfterBad, rbErr := mvm.RollbackXapianAboveBlock(1)
	require.NoError(t, rbErr)
	require.Equal(t, 0, rolledAfterBad)

	// 3. Subcase B: Sync nhánh Fork Block #2 hợp lệ, sau đó bị Canonical Block #2 ghi đè qua forkOverwrite
	tx2Fork := common.HexToHash("0x2222000000000000000000000000000000000000000000000000000000002502")
	blk2ForkHdr := block.NewBlockHeader(blk1Hash, 2, accRoot2Fork, stakeRoot2Fork, common.HexToHash("0x202"), common.Address{}, 2000, common.Hash{}, 0, 2)
	blk2Fork := block.NewBlock(blk2ForkHdr, []common.Hash{tx2Fork}, nil)
	blk2ForkHash := blk2Fork.Header().Hash()
	raw2Fork, err := blk2Fork.Marshal()
	require.NoError(t, err)

	ab2Fork, _ := storage.SerializeBatch(cloneKVs(b2ForkAcc))
	rcptB2Fork, _ := storage.SerializeBatch([][2][]byte{{tx2Fork.Bytes(), []byte("nomt_rcpt_2_fork")}})
	txB2Fork, _ := storage.SerializeBatch([][2][]byte{{tx2Fork.Bytes(), []byte("nomt_tx_2_fork")}})
	log2Fork := buildMinimalXapianCompLog("db_adv25", "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "nomt_fork_b2")
	backup2Fork, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber:                2,
		AccountBatch:              ab2Fork,
		StakeState:                sb2Fork,
		SmartContractStorageBatch: scb2Fork,
		ReceiptBatchPut:           rcptB2Fork,
		TxBatchPut:                txB2Fork,
		FullDbLogs:                []map[string][]byte{{scAddrA.Hex()[2:]: log2Fork}},
	})
	require.NoError(t, err)

	resp2Fork, err := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks:      []*pb.BlockData{{BlockNumber: 2, RawBlockBytes: raw2Fork, BackupData: backup2Fork}},
	})
	require.NoError(t, err)
	require.Empty(t, resp2Fork.GetError())
	require.Equal(t, uint64(2), storage.GetLastBlockNumber())
	gotForkA, _ := trie.GetNomtHandleRoot("account_state")
	gotForkC, _ := trie.GetNomtHandleRoot("smart_contract_storage")
	require.Equal(t, accRoot2Fork, gotForkA)
	require.Equal(t, scRoot2Fork, gotForkC)

	// Ghi đè bằng Canonical Block #2
	tx2Canon := common.HexToHash("0x2222000000000000000000000000000000000000000000000000000000002503")
	blk2CanonHdr := block.NewBlockHeader(blk1Hash, 2, accRoot2Canon, stakeRoot2Canon, common.HexToHash("0x203"), common.Address{}, 2500, common.Hash{}, 0, 2)
	blk2Canon := block.NewBlock(blk2CanonHdr, []common.Hash{tx2Canon}, nil)
	blk2CanonHash := blk2Canon.Header().Hash()
	require.NotEqual(t, blk2ForkHash, blk2CanonHash)
	raw2Canon, err := blk2Canon.Marshal()
	require.NoError(t, err)

	ab2Canon, _ := storage.SerializeBatch(cloneKVs(b2CanonAcc))
	sb2Canon, _ := storage.SerializeBatch(cloneKVs(b2CanonStake))
	scb2Canon, _ := storage.SerializeBatch(cloneKVs(b2CanonSC))
	rcptB2Canon, _ := storage.SerializeBatch([][2][]byte{{tx2Canon.Bytes(), []byte("nomt_rcpt_2_canon")}})
	txB2Canon, _ := storage.SerializeBatch([][2][]byte{{tx2Canon.Bytes(), []byte("nomt_tx_2_canon")}})
	log2Canon := buildMinimalXapianCompLog("db_adv25", "0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", "nomt_canon_b2")
	backup2Canon, err := storage.SerializeBackupDb(storage.BackUpDb{
		BockNumber:                2,
		AccountBatch:              ab2Canon,
		StakeState:                sb2Canon,
		SmartContractStorageBatch: scb2Canon,
		ReceiptBatchPut:           rcptB2Canon,
		TxBatchPut:                txB2Canon,
		FullDbLogs:                []map[string][]byte{{scAddrA.Hex()[2:]: log2Canon}},
	})
	require.NoError(t, err)

	resp2Canon, err := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		ExecuteMode: true,
		Blocks:      []*pb.BlockData{{BlockNumber: 2, RawBlockBytes: raw2Canon, BackupData: backup2Canon}},
	})
	require.NoError(t, err)
	require.Empty(t, resp2Canon.GetError())
	require.Equal(t, uint64(1), resp2Canon.GetSyncedCount())
	require.Equal(t, uint64(2), storage.GetLastBlockNumber())
	require.Equal(t, uint64(2), storage.GetLastNomtCommittedBlock())

	// Kiểm tra 3 NOMT roots sau fork overwrite khớp 100% chuỗi sạch [B1, CanonB2]
	finalA, _ := trie.GetNomtHandleRoot("account_state")
	finalS, _ := trie.GetNomtHandleRoot("stake_db")
	finalC, _ := trie.GetNomtHandleRoot("smart_contract_storage")
	require.Equal(t, accRoot2Canon, finalA, "account_state NOMT root sau fork overwrite phải khớp node sạch")
	require.Equal(t, stakeRoot2Canon, finalS, "stake_db NOMT root sau fork overwrite phải khớp node sạch")
	require.Equal(t, scRoot2Canon, finalC, "smart_contract_storage NOMT root sau fork overwrite phải khớp node sạch")

	gotHash2, ok2 := bc.GetBlockHashByNumber(2)
	require.True(t, ok2)
	require.Equal(t, blk2CanonHash, gotHash2)
	_, hasForkTx := bc.GetBlockNumberByTxHash(tx2Fork)
	require.False(t, hasForkTx, "tx2Fork phải bị xóa khỏi BlockChain mapping")
	gotCanonBn, hasCanonTx := bc.GetBlockNumberByTxHash(tx2Canon)
	require.True(t, hasCanonTx)
	require.Equal(t, uint64(2), gotCanonBn)
	_, errForkTxStore := sm.GetStorageTransaction().Get(tx2Fork.Bytes())
	require.Error(t, errForkTxStore, "tx2Fork phải bị xóa khỏi StorageTransaction")
	_, errForkBlock := blockDB.GetBlockByHash(blk2ForkHash)
	require.Error(t, errForkBlock, "blk2Fork phải bị xóa khỏi BlockDB")
}

