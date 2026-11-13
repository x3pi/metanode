package tx_processor

import (
	"bytes"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	"github.com/meta-node-blockchain/meta-node/types"
)

// Adversarial tests: cố tình tìm lỗ hổng của Xapian undo journal (XUJ1).
// Mỗi test in ra kết quả thực tế (t.Logf) để làm bằng chứng.

func advSetup(t *testing.T, contractHex, dbName string) (*blockchain.ChainState, func(data []byte) types.Transaction, common.Address, common.Address) {
	t.Helper()
	mvm.DestroyAllXapianInstances()
	mvm.ClearAllMVMApi()
	mvm.CallClearAllStateInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	key, err := crypto.HexToECDSA("4f3edf983ac636a65a842ce7c78d9aa706d3b113bce9c46f30d7d21715b23b1d")
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	contractAddr := common.HexToAddress(contractHex)

	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	seedAccount(t, cs, sender, new(big.Int).Lsh(big.NewInt(1), 90), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedContract(t, cs, contractAddr, big.NewInt(0), buildStatefulXapianV1Forwarder())

	nonce := uint64(1)
	mk := func(data []byte) types.Transaction {
		n := nonce
		nonce++
		return signAndWrapCall(t, key, n, contractAddr, data)
	}
	return cs, mk, contractAddr, leader
}

var advGetDataABI = func() abi.ABI {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"bytes"}]}]`))
	if err != nil {
		panic(err)
	}
	return a
}()

func advReadData(t *testing.T, cs *blockchain.ChainState, mk func([]byte) types.Transaction, contract, leader common.Address, db string, doc *big.Int) []byte {
	t.Helper()
	tx := mk(makeXapianV1GetDataData(t, db, doc))
	rcp := runTxBatchOnChainState(t, cs, contract, leader, []types.Transaction{tx})
	var got []byte
	_ = advGetDataABI.UnpackIntoInterface(&got, "getDataDocument", rcp[0].Return())
	return got
}

// ADV-1: commit KHÔNG kèm số block (CommitAllXapian(), blockNumber=0) không ghi undo journal.
// Đây là đường mà snapshot_init.go:133 và raft_state_source.go:101 đang gọi.
// Kỳ vọng an toàn: sau Rollback(1), dữ liệu phải quay về trạng thái block 1.
func TestAdv1_CommitWithoutBlockNumberIsNotRolledBack(t *testing.T) {
	const db = "adv1"
	cs, mk, contract, leader := advSetup(t, "0x1000000000000000000000000000000000000801", db)

	txInit := mk(makeXapianV1GetOrCreateDbData(t, db))
	txNew := mk(makeXapianV1NewDocData(t, db, []byte("original_b1")))
	rc := runTxBatchOnChainState(t, cs, contract, leader, []types.Transaction{txInit, txNew})
	doc := new(big.Int).SetBytes(rc[1].Return())
	commitTxsToXapianAtBlock([]types.Transaction{txInit, txNew}, 1)

	txSet := mk(makeXapianV1SetDataData(t, db, doc, []byte("WRITTEN_WITHOUT_JOURNAL")))
	runTxBatchOnChainState(t, cs, contract, leader, []types.Transaction{txSet})
	mvm.CommitXapianTxBufferBatch([][]byte{txSet.Hash().Bytes()})
	mvm.CommitAllXapian() // blockNumber = 0 -> không có undo record

	rolled, err := mvm.RollbackXapianAboveBlock(1)
	require.NoError(t, err)
	got := advReadData(t, cs, mk, contract, leader, db, doc)
	t.Logf("EVIDENCE ADV-1: rolled=%d, data sau Rollback(1) = %q (an toàn nếu = %q)", rolled, got, "original_b1")
	require.Equal(t, "original_b1", string(got),
		"BUG: ghi qua CommitAllXapian() (không số block) KHÔNG được rollback -> Xapian lệch so với chain")
}

// ADV-2: cùng 1 block commit 2 lần (merge undo record). Sau Rollback, docid cấp lại phải y hệt ban đầu.
func TestAdv2_SameBlockTwoCommitsRollbackDeterministicDocid(t *testing.T) {
	const db = "adv2"
	cs, mk, contract, leader := advSetup(t, "0x1000000000000000000000000000000000000802", db)

	txInit := mk(makeXapianV1GetOrCreateDbData(t, db))
	txNew1 := mk(makeXapianV1NewDocData(t, db, []byte("b1_doc")))
	runTxBatchOnChainState(t, cs, contract, leader, []types.Transaction{txInit, txNew1})
	commitTxsToXapianAtBlock([]types.Transaction{txInit, txNew1}, 1)

	// block 2, commit lần 1: tạo docX
	txX := mk(makeXapianV1NewDocData(t, db, []byte("docX_b2_part1")))
	rcX := runTxBatchOnChainState(t, cs, contract, leader, []types.Transaction{txX})
	docX := new(big.Int).SetBytes(rcX[0].Return())
	commitTxsToXapianAtBlock([]types.Transaction{txX}, 2)

	// block 2, commit lần 2 (cùng block 2): sửa docX + tạo docY
	txSetX := mk(makeXapianV1SetDataData(t, db, docX, []byte("docX_b2_part2")))
	txY := mk(makeXapianV1NewDocData(t, db, []byte("docY_b2_part2")))
	rcY := runTxBatchOnChainState(t, cs, contract, leader, []types.Transaction{txSetX, txY})
	docY := new(big.Int).SetBytes(rcY[1].Return())
	commitTxsToXapianAtBlock([]types.Transaction{txSetX, txY}, 2)

	rolled, err := mvm.RollbackXapianAboveBlock(1)
	require.NoError(t, err)
	gotX := advReadData(t, cs, mk, contract, leader, db, docX)
	gotY := advReadData(t, cs, mk, contract, leader, db, docY)
	t.Logf("EVIDENCE ADV-2: rolled=%d docX=%q docY=%q (kỳ vọng cả hai rỗng)", rolled, gotX, gotY)
	require.Empty(t, gotX, "docX phải biến mất sau rollback về block 1")
	require.Empty(t, gotY, "docY phải biến mất sau rollback về block 1")

	_ = docY
}

// ADV-3: rollback lặp 2 lần và rollback khi không có gì để lùi phải idempotent.
func TestAdv3_RollbackIdempotent(t *testing.T) {
	const db = "adv3"
	cs, mk, contract, leader := advSetup(t, "0x1000000000000000000000000000000000000803", db)

	txInit := mk(makeXapianV1GetOrCreateDbData(t, db))
	txNew := mk(makeXapianV1NewDocData(t, db, []byte("b1")))
	runTxBatchOnChainState(t, cs, contract, leader, []types.Transaction{txInit, txNew})
	commitTxsToXapianAtBlock([]types.Transaction{txInit, txNew}, 1)

	r1, e1 := mvm.RollbackXapianAboveBlock(5) // target cao hơn head
	r2, e2 := mvm.RollbackXapianAboveBlock(1)
	r3, e3 := mvm.RollbackXapianAboveBlock(1)
	t.Logf("EVIDENCE ADV-3: rollback(5)=%d,%v rollback(1)=%d,%v rollback(1)lần2=%d,%v", r1, e1, r2, e2, r3, e3)
	require.NoError(t, e1)
	require.NoError(t, e2)
	require.NoError(t, e3)
	require.Equal(t, 0, r1)
	require.Equal(t, 0, r2)
	require.Equal(t, 0, r3)
}

// ADV-4: crash sau khi đã commit tới block N nhưng node khởi động lại với DB "lạnh"
// (instances bị hủy) và target thấp hơn nhiều block (gap 3 block). Phải về đúng block 1.
func TestAdv4_ColdRollbackMultiBlockGap(t *testing.T) {
	const db = "adv4"
	cs, mk, contract, leader := advSetup(t, "0x1000000000000000000000000000000000000804", db)

	txInit := mk(makeXapianV1GetOrCreateDbData(t, db))
	txNew := mk(makeXapianV1NewDocData(t, db, []byte("v1")))
	rc := runTxBatchOnChainState(t, cs, contract, leader, []types.Transaction{txInit, txNew})
	doc := new(big.Int).SetBytes(rc[1].Return())
	commitTxsToXapianAtBlock([]types.Transaction{txInit, txNew}, 1)

	for bn := uint64(2); bn <= 4; bn++ {
		tx := mk(makeXapianV1SetDataData(t, db, doc, []byte("v"+big.NewInt(int64(bn)).String())))
		runTxBatchOnChainState(t, cs, contract, leader, []types.Transaction{tx})
		commitTxsToXapianAtBlock([]types.Transaction{tx}, bn)
	}
	mvm.DestroyAllXapianInstances() // giả lập process chết rồi mở lại

	rolled, err := mvm.RollbackXapianAboveBlock(1)
	require.NoError(t, err)
	got := advReadData(t, cs, mk, contract, leader, db, doc)
	t.Logf("EVIDENCE ADV-4: rolled=%d, data=%q (kỳ vọng v1)", rolled, got)
	require.Equal(t, "v1", string(got))
}

func advSetupOnDir(t *testing.T, xapianDir string, contractHexes ...string) (*blockchain.ChainState, func(contract common.Address, data []byte) types.Transaction, common.Address) {
	t.Helper()
	mvm.DestroyAllXapianInstances()
	mvm.ClearAllMVMApi()
	mvm.CallClearAllStateInstances()
	mvm.ConfigureXapianBasePath(xapianDir)

	key, err := crypto.HexToECDSA("4f3edf983ac636a65a842ce7c78d9aa706d3b113bce9c46f30d7d21715b23b1d")
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)

	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	seedAccount(t, cs, sender, new(big.Int).Lsh(big.NewInt(1), 90), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	for _, ch := range contractHexes {
		seedContract(t, cs, common.HexToAddress(ch), big.NewInt(0), buildStatefulXapianV1Forwarder())
	}

	nonce := uint64(1)
	mkFor := func(contract common.Address, data []byte) types.Transaction {
		n := nonce
		nonce++
		return signAndWrapCall(t, key, n, contract, data)
	}
	return cs, mkFor, leader
}

// ADV-5: 1 contract ghi vào >= 2 dbName khác nhau trong cùng block.
// ExportAllXapianLogs() trả về map[addressHex][]byte nên chỉ giữ 1 DB, làm mất trắng DB còn lại khi SyncNode replay -> FORK.
func TestAdv5_SingleContractMultiDb_ExportOverwritesLogsAndForksSyncNode(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	valDir := t.TempDir()
	syncDir := t.TempDir()
	const contractHex = "0x1000000000000000000000000000000000000805"
	contractAddr := common.HexToAddress(contractHex)

	// Phase 1: Validator thực thi Block 1 ghi vào 2 Xapian DB (db_alpha, db_beta) dưới cùng 1 contract
	csVal, mkB1, leader := advSetupOnDir(t, valDir, contractHex)
	txInitA := mkB1(contractAddr, makeXapianV1GetOrCreateDbData(t, "db_alpha"))
	txNewA := mkB1(contractAddr, makeXapianV1NewDocData(t, "db_alpha", []byte("alpha_payload")))
	txInitB := mkB1(contractAddr, makeXapianV1GetOrCreateDbData(t, "db_beta"))
	txNewB := mkB1(contractAddr, makeXapianV1NewDocData(t, "db_beta", []byte("beta_payload")))
	b1Txs := []types.Transaction{txInitA, txNewA, txInitB, txNewB}

	rc1 := runTxBatchOnChainState(t, csVal, contractAddr, leader, b1Txs)
	docA := new(big.Int).SetBytes(rc1[1].Return())
	docB := new(big.Int).SetBytes(rc1[3].Return())
	require.True(t, docA.Sign() > 0 && docB.Sign() > 0)

	logsB1 := commitTxsToXapianAtBlock(b1Txs, 1)

	// Phase 2: SyncNode nhận BackupDb.FullDbLogs của Block 1 và replay đúng luồng unix_socket_handler_sync.go
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(syncDir)
	require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{logsB1}, 1))
	mvm.CommitAllXapianAtBlock(1)
	mvm.DestroyAllXapianInstances()

	// Phase 3: Cả Validator và SyncNode chạy cùng Block 2 đọc lại cả db_alpha và db_beta
	unpackBytes := func(ret []byte) []byte {
		var out []byte
		_ = advGetDataABI.UnpackIntoInterface(&out, "getDataDocument", ret)
		return out
	}

	csValB2, mkValB2, leaderVal := advSetupOnDir(t, valDir, contractHex)
	txValReadA := mkValB2(contractAddr, makeXapianV1GetDataData(t, "db_alpha", docA))
	txValReadB := mkValB2(contractAddr, makeXapianV1GetDataData(t, "db_beta", docB))
	rcValB2 := runTxBatchOnChainState(t, csValB2, contractAddr, leaderVal, []types.Transaction{txValReadA, txValReadB})
	valGotA := unpackBytes(rcValB2[0].Return())
	valGotB := unpackBytes(rcValB2[1].Return())

	csSyncB2, mkSyncB2, leaderSync := advSetupOnDir(t, syncDir, contractHex)
	txSyncReadA := mkSyncB2(contractAddr, makeXapianV1GetDataData(t, "db_alpha", docA))
	txSyncReadB := mkSyncB2(contractAddr, makeXapianV1GetDataData(t, "db_beta", docB))
	rcSyncB2 := runTxBatchOnChainState(t, csSyncB2, contractAddr, leaderSync, []types.Transaction{txSyncReadA, txSyncReadB})
	syncGotA := unpackBytes(rcSyncB2[0].Return())
	syncGotB := unpackBytes(rcSyncB2[1].Return())

	slotsEqual := true
	for slot := 1; slot <= 2; slot++ {
		var slotKey common.Hash
		big.NewInt(int64(slot)).FillBytes(slotKey[:])
		vVal, _ := csValB2.GetSmartContractDB().StorageValue(contractAddr, slotKey.Bytes())
		vSync, _ := csSyncB2.GetSmartContractDB().StorageValue(contractAddr, slotKey.Bytes())
		if !bytes.Equal(vVal, vSync) {
			slotsEqual = false
		}
	}

	t.Logf("EVIDENCE ADV-5: exportedMapKeys=%d, Validator=(%q, %q), SyncNode=(%q, %q), EVM_StorageEqual=%v",
		len(logsB1), valGotA, valGotB, syncGotA, syncGotB, slotsEqual)

	require.Equal(t, "alpha_payload", string(valGotA))
	require.Equal(t, "beta_payload", string(valGotB))
	require.Equal(t, "alpha_payload", string(syncGotA), "BUG ADV-5: SyncNode mất dữ liệu db_alpha do ExportAllXapianLogs ghi đè theo addressHex")
	require.Equal(t, "beta_payload", string(syncGotB), "BUG ADV-5: SyncNode mất dữ liệu db_beta do ExportAllXapianLogs ghi đè theo addressHex")
	require.True(t, slotsEqual, "BUG ADV-5: EVM contract storage sau Block 2 lệch nhau giữa Validator và SyncNode -> FORK")
}

// ADV-6: Local node chạy nhầm nhánh fork ở Block 2 (ghi contractStale), sau đó sync đè Block 2 chuẩn (chỉ có contractCanon).
// CallReplayFullDbLogsBatchAtBlock chỉ rollback các DB có mặt trong FullDbLogs mới, bỏ sót contractStale.
func TestAdv6_SyncForkOverwrite_DoesNotRollbackUntouchedStaleXapianDb(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	leaderDir := t.TempDir()
	localDir := t.TempDir()
	const staleHex = "0x1000000000000000000000000000000000000806"
	const canonHex = "0x1000000000000000000000000000000000000807"
	contractStale := common.HexToAddress(staleHex)
	contractCanon := common.HexToAddress(canonHex)

	// 1. Leader tạo Block 1 (cả contractStale và contractCanon) và Block 2 chuẩn (chỉ sửa contractCanon)
	csLeader, mkLeader, leader := advSetupOnDir(t, leaderDir, staleHex, canonHex)
	txSInit := mkLeader(contractStale, makeXapianV1GetOrCreateDbData(t, "db_stale"))
	txSNew := mkLeader(contractStale, makeXapianV1NewDocData(t, "db_stale", []byte("b1_clean")))
	rcS := runTxBatchOnChainState(t, csLeader, contractStale, leader, []types.Transaction{txSInit, txSNew})
	docS := new(big.Int).SetBytes(rcS[1].Return())

	txCInit := mkLeader(contractCanon, makeXapianV1GetOrCreateDbData(t, "db_canon"))
	txCNew := mkLeader(contractCanon, makeXapianV1NewDocData(t, "db_canon", []byte("b1_canon")))
	rcC := runTxBatchOnChainState(t, csLeader, contractCanon, leader, []types.Transaction{txCInit, txCNew})
	docC := new(big.Int).SetBytes(rcC[1].Return())

	logsB1 := commitTxsToXapianAtBlock([]types.Transaction{txSInit, txSNew, txCInit, txCNew}, 1)

	txCanonB2 := mkLeader(contractCanon, makeXapianV1SetDataData(t, "db_canon", docC, []byte("CANONICAL_B2")))
	runTxBatchOnChainState(t, csLeader, contractCanon, leader, []types.Transaction{txCanonB2})
	canonicalLogsB2 := commitTxsToXapianAtBlock([]types.Transaction{txCanonB2}, 2)
	require.Len(t, canonicalLogsB2, 1)

	// 2. Local node sync Block 1, rồi ghi Block 2 nhánh fork vào contractStale
	csLocal, mkLocal, localLeader := advSetupOnDir(t, localDir, staleHex, canonHex)
	require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{logsB1}, 1))
	mvm.CommitAllXapianAtBlock(1)

	txStaleForkB2 := mkLocal(contractStale, makeXapianV1SetDataData(t, "db_stale", docS, []byte("PHANTOM_STALE_FORK_B2")))
	runTxBatchOnChainState(t, csLocal, contractStale, localLeader, []types.Transaction{txStaleForkB2})
	_ = commitTxsToXapianAtBlock([]types.Transaction{txStaleForkB2}, 2)

	// 3. Sync đè Block 2 chuẩn từ Leader lên Local node (đúng luồng unix_socket_handler_sync.go:1093-1108)
	require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{canonicalLogsB2}, 2))
	mvm.CommitAllXapianAtBlock(2)
	mvm.DestroyAllXapianInstances()

	// 4. Đọc lại docS trên contractStale tại Local node
	csCheck, mkCheck, checkLeader := advSetupOnDir(t, localDir, staleHex)
	txReadStale := mkCheck(contractStale, makeXapianV1GetDataData(t, "db_stale", docS))
	rcCheck := runTxBatchOnChainState(t, csCheck, contractStale, checkLeader, []types.Transaction{txReadStale})
	var gotStale []byte
	_ = advGetDataABI.UnpackIntoInterface(&gotStale, "getDataDocument", rcCheck[0].Return())

	t.Logf("EVIDENCE ADV-6: docS trên contractStale sau khi sync ghi đè Block 2 = %q (an toàn nếu = %q)", gotStale, "b1_clean")
	require.Equal(t, "b1_clean", string(gotStale), "BUG ADV-6: Sync ghi đè nhánh fork không rollback Xapian DB nằm ngoài FullDbLogs của block mới")
}

// ADV-9: destroyInstance() đã bỏ revertUncommittedChanges() nhưng Xapian::WritableDatabase::close()
// tự động flush/commit mọi thay đổi chưa commit xuống đĩa mà KHÔNG ghi __mtn_undo_* hay __mtn_meta_head_block!
// Sau khi restart và RollbackXapianAboveBlock(1), dữ liệu bẩn ở Block 2 chưa commit vẫn tồn tại vĩnh viễn.
func TestAdv9_DestroyInstance_SilentlyCommitsUncommittedWritesWithoutUndoJournal(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	dir := t.TempDir()
	const contractHex = "0x1000000000000000000000000000000000000809"
	contractAddr := common.HexToAddress(contractHex)

	// Block 1: tạo document và commit đầy đủ tại Block 1 (__mtn_meta_head_block = 1)
	cs1, mkB1, leader1 := advSetupOnDir(t, dir, contractHex)
	txInit := mkB1(contractAddr, makeXapianV1GetOrCreateDbData(t, "db_adv9"))
	txNew := mkB1(contractAddr, makeXapianV1NewDocData(t, "db_adv9", []byte("clean_b1")))
	b1Txs := []types.Transaction{txInit, txNew}
	rc1 := runTxBatchOnChainState(t, cs1, contractAddr, leader1, b1Txs)
	docA := new(big.Int).SetBytes(rc1[1].Return())
	require.True(t, docA.Sign() > 0)
	_ = commitTxsToXapianAtBlock(b1Txs, 1)

	// Block 2 (chưa commit block): sửa docA và replay tx buffer vào WritableDatabase nhưng CHƯA gọi CommitAllXapianAtBlock(2)
	txB2 := mkB1(contractAddr, makeXapianV1SetDataData(t, "db_adv9", docA, []byte("UNCOMMITTED_DIRTY_B2")))
	runTxBatchOnChainState(t, cs1, contractAddr, leader1, []types.Transaction{txB2})
	mvm.CommitXapianTxBufferBatch([][]byte{txB2.Hash().Bytes()})

	// Giả lập shutdown / reset instance trước khi Block 2 commit:
	// destroyInstance() gọi db.close() khiến Xapian::WritableDatabase tự flush xuống đĩa mà không tạo Undo Journal!
	mvm.DestroyAllXapianInstances()

	// Node khởi động lại theo BlockDB (head = 1) và chạy RollbackXapianAboveBlock(1)
	rolled, rbErr := mvm.RollbackXapianAboveBlock(1)
	require.NoError(t, rbErr)

	// Đọc lại docA sau recovery
	csCheck, mkCheck, leaderCheck := advSetupOnDir(t, dir, contractHex)
	txRead := mkCheck(contractAddr, makeXapianV1GetDataData(t, "db_adv9", docA))
	rcRead := runTxBatchOnChainState(t, csCheck, contractAddr, leaderCheck, []types.Transaction{txRead})
	var got []byte
	_ = advGetDataABI.UnpackIntoInterface(&got, "getDataDocument", rcRead[0].Return())

	t.Logf("EVIDENCE ADV-9: rolled=%d, docA=%q (kỳ vọng %q)", rolled, string(got), "clean_b1")
	require.Equal(t, "clean_b1", string(got),
		"BUG ADV-9: XapianManager::destroyInstance() gọi db.close() làm auto-commit dữ liệu chưa commit xuống đĩa mà không có Undo Journal, khiến RollbackXapianAboveBlock(1) bỏ sót!")
}

// ADV-13: Nếu một batch FullDbLogs có nhiều frame (nhiều DB trên 1 contract) hoặc nhiều contract,
// và frame/entry thứ 2 bị hỏng giữa chừng, ReplayFullDbLogsAtBlock phải hoàn tác sạch (atomic)
// toàn bộ thay đổi đã áp vào frame/entry thứ 1, tuyệt đối không để rò rỉ dữ liệu bẩn sang lần đọc sau.
func TestAdv13_ReplayFullDbLogsBatch_MidBatchCorruptFrameRollsBackAtomically(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	const hexA = "0x1000000000000000000000000000000000000813"
	const hexB = "0x1000000000000000000000000000000000000814"
	addrA := common.HexToAddress(hexA)
	addrB := common.HexToAddress(hexB)

	// 1. Khởi tạo dữ liệu sạch ở Block 1 trên srcDir rồi replay sang dstDir
	csSrc, mkSrc, leaderSrc := advSetupOnDir(t, srcDir, hexA, hexB)
	txAInit := mkSrc(addrA, makeXapianV1GetOrCreateDbData(t, "db_atomic_a"))
	txANew := mkSrc(addrA, makeXapianV1NewDocData(t, "db_atomic_a", []byte("clean_b1_A")))
	rcA := runTxBatchOnChainState(t, csSrc, addrA, leaderSrc, []types.Transaction{txAInit, txANew})
	docA := new(big.Int).SetBytes(rcA[1].Return())

	txBInit := mkSrc(addrB, makeXapianV1GetOrCreateDbData(t, "db_atomic_b"))
	txBNew := mkSrc(addrB, makeXapianV1NewDocData(t, "db_atomic_b", []byte("clean_b1_B")))
	runTxBatchOnChainState(t, csSrc, addrB, leaderSrc, []types.Transaction{txBInit, txBNew})

	logsB1 := commitTxsToXapianAtBlock([]types.Transaction{txAInit, txANew, txBInit, txBNew}, 1)

	// Tạo frame hợp lệ ở Block 2 trên srcDir cho addrA ("DIRTY_PARTIAL_B2")
	txADirty := mkSrc(addrA, makeXapianV1SetDataData(t, "db_atomic_a", docA, []byte("DIRTY_PARTIAL_B2")))
	runTxBatchOnChainState(t, csSrc, addrA, leaderSrc, []types.Transaction{txADirty})
	logsB2ValidA := commitTxsToXapianAtBlock([]types.Transaction{txADirty}, 2)
	validFrameA := logsB2ValidA[addrA.Hex()]
	require.NotEmpty(t, validFrameA)

	// 2. Sync Block 1 sang dstDir
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(dstDir)
	require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{logsB1}, 1))
	mvm.CommitAllXapianAtBlock(1)

	// Case 1: Cùng 1 contract có Frame 1 hợp lệ nối tiếp Frame 2 bị cắt cụt (corrupt)
	multiFrameCorrupt := append(append([]byte{}, validFrameA...), 0x00, 0x00, 0x00, 0x7f, 'b', 'a', 'd')
	rcMultiFrame := mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{{addrA.Hex(): multiFrameCorrupt}}, 2)
	require.Equal(t, 0, rcMultiFrame, "Frame thứ 2 bị hỏng phải làm batch fail-closed")

	// Case 2: Batch 2 contract: Entry 0 (addrA) hợp lệ, Entry 1 (addrB) bị hỏng
	rcMultiEntry := mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{{
		addrA.Hex(): validFrameA,
		addrB.Hex(): []byte{0x00, 0x00, 0x00, 0x55, 0xff},
	}}, 2)
	require.Equal(t, 0, rcMultiEntry, "Entry thứ 2 bị hỏng phải làm toàn bộ batch fail-closed")

	// Đọc lại docA trên dstDir (cả trước và sau khi đóng/mở lại instance) -> tuyệt đối phải giữ nguyên "clean_b1_A"
	csCheckMem, mkCheckMem, leaderCheck := advSetupOnDir(t, dstDir, hexA)
	txRead1 := mkCheckMem(addrA, makeXapianV1GetDataData(t, "db_atomic_a", docA))
	rcRead1 := runTxBatchOnChainState(t, csCheckMem, addrA, leaderCheck, []types.Transaction{txRead1})
	var gotAfterAbort []byte
	_ = advGetDataABI.UnpackIntoInterface(&gotAfterAbort, "getDataDocument", rcRead1[0].Return())
	require.Equal(t, "clean_b1_A", string(gotAfterAbort),
		"ReplayFullDbLogsAtBlock làm rò rỉ dữ liệu chưa commit của Frame 1 / Entry 0 khi Frame 2 / Entry 1 thất bại!")
}

// ADV-14: CallReplayFullDbLogsBatchAtBlock phải fail-closed (trả về 0) khi gặp địa chỉ hex sai định dạng
// thay vì continue và trả về 1; đồng thời nếu batch rỗng tại blockNumber > 0 vẫn phải rollback nhánh fork cũ.
func TestAdv14_CallReplayFullDbLogsBatch_RejectsMalformedAddressHexAndRollsBackEmptyBatch(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	dir := t.TempDir()
	const contractHex = "0x1000000000000000000000000000000000000815"
	contractAddr := common.HexToAddress(contractHex)

	cs, mk, leader := advSetupOnDir(t, dir, contractHex)
	txInit := mk(contractAddr, makeXapianV1GetOrCreateDbData(t, "db_adv14"))
	txNew := mk(contractAddr, makeXapianV1NewDocData(t, "db_adv14", []byte("base_b1")))
	rc1 := runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txInit, txNew})
	doc := new(big.Int).SetBytes(rc1[1].Return())
	logsB1 := commitTxsToXapianAtBlock([]types.Transaction{txInit, txNew}, 1)
	validPayload := logsB1[contractAddr.Hex()]
	require.NotEmpty(t, validPayload)

	// 1. Địa chỉ hex sai độ dài hoặc ký tự không phải hex -> bắt buộc trả về 0 (fail-closed)
	require.Equal(t, 0, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{{"0x1234": validPayload}}, 2))
	require.Equal(t, 0, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{{"0xZZZZ000000000000000000000000000000000000": validPayload}}, 2))

	// 2. Tạo nhánh fork cục bộ tại Block 2 rồi gọi replay với batch rỗng tại Block 2 -> phải rollback về Block 1
	txForkB2 := mk(contractAddr, makeXapianV1SetDataData(t, "db_adv14", doc, []byte("STALE_FORK_B2")))
	runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txForkB2})
	_ = commitTxsToXapianAtBlock([]types.Transaction{txForkB2}, 2)

	require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{{contractAddr.Hex(): nil}}, 2))
	mvm.DestroyAllXapianInstances()

	csCheck, mkCheck, leaderCheck := advSetupOnDir(t, dir, contractHex)
	txRead := mkCheck(contractAddr, makeXapianV1GetDataData(t, "db_adv14", doc))
	rcRead := runTxBatchOnChainState(t, csCheck, contractAddr, leaderCheck, []types.Transaction{txRead})
	var got []byte
	_ = advGetDataABI.UnpackIntoInterface(&got, "getDataDocument", rcRead[0].Return())
	require.Equal(t, "base_b1", string(got), "Batch rỗng sau lọc tại blockNumber=2 phải rollback nhánh fork cũ về Block 1")
}

// ADV-15: Kiểm tra tương đương toàn diện (Validator vs SyncNode) qua 3 block liên tiếp trên 3 DB Xapian
// khác nhau của cùng 1 contract với đầy đủ thao tác tạo mới, cập nhật, xóa và cấp phát docId tiếp theo.
func TestAdv15_ThreeDbInterleavedMultiBlockValidatorToSyncNodeEquivalence(t *testing.T) {
	t.Cleanup(func() { mvm.DestroyAllXapianInstances() })

	valDir := t.TempDir()
	syncDir := t.TempDir()
	const contractHex = "0x1000000000000000000000000000000000000816"
	contractAddr := common.HexToAddress(contractHex)

	// Chạy chuỗi 3 block trên Validator và thu thập FullDbLogs từng block
	csVal, mkVal, leaderVal := advSetupOnDir(t, valDir, contractHex)

	// Block 1: Khởi tạo 3 DB + thêm doc đầu tiên vào mỗi DB
	b1Txs := []types.Transaction{
		mkVal(contractAddr, makeXapianV1GetOrCreateDbData(t, "db_orders")),
		mkVal(contractAddr, makeXapianV1NewDocData(t, "db_orders", []byte("order_1_v1"))),
		mkVal(contractAddr, makeXapianV1GetOrCreateDbData(t, "db_users")),
		mkVal(contractAddr, makeXapianV1NewDocData(t, "db_users", []byte("user_1_v1"))),
		mkVal(contractAddr, makeXapianV1GetOrCreateDbData(t, "db_audit")),
		mkVal(contractAddr, makeXapianV1NewDocData(t, "db_audit", []byte("audit_1_v1"))),
	}
	rcB1 := runTxBatchOnChainState(t, csVal, contractAddr, leaderVal, b1Txs)
	docOrder1 := new(big.Int).SetBytes(rcB1[1].Return())
	docUser1 := new(big.Int).SetBytes(rcB1[3].Return())
	docAudit1 := new(big.Int).SetBytes(rcB1[5].Return())
	logsB1 := commitTxsToXapianAtBlock(b1Txs, 1)

	// Block 2: Sửa order_1, xóa user_1, thêm order_2 và user_2
	b2Txs := []types.Transaction{
		mkVal(contractAddr, makeXapianV1SetDataData(t, "db_orders", docOrder1, []byte("order_1_v2"))),
		mkVal(contractAddr, makeXapianV1DeleteDocData(t, "db_users", docUser1)),
		mkVal(contractAddr, makeXapianV1NewDocData(t, "db_orders", []byte("order_2_v1"))),
		mkVal(contractAddr, makeXapianV1NewDocData(t, "db_users", []byte("user_2_v1"))),
	}
	rcB2 := runTxBatchOnChainState(t, csVal, contractAddr, leaderVal, b2Txs)
	docOrder2 := new(big.Int).SetBytes(rcB2[2].Return())
	docUser2 := new(big.Int).SetBytes(rcB2[3].Return())
	logsB2 := commitTxsToXapianAtBlock(b2Txs, 2)

	// Block 3: Sửa audit_1 và user_2
	b3Txs := []types.Transaction{
		mkVal(contractAddr, makeXapianV1SetDataData(t, "db_audit", docAudit1, []byte("audit_1_final"))),
		mkVal(contractAddr, makeXapianV1SetDataData(t, "db_users", docUser2, []byte("user_2_final"))),
	}
	runTxBatchOnChainState(t, csVal, contractAddr, leaderVal, b3Txs)
	logsB3 := commitTxsToXapianAtBlock(b3Txs, 3)

	// Replay tuần tự Block 1..3 sang SyncNode
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(syncDir)
	for blkNum, logs := range []map[string][]byte{logsB1, logsB2, logsB3} {
		bn := uint64(blkNum + 1)
		require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{logs}, bn))
		mvm.CommitAllXapianAtBlock(bn)
	}
	mvm.DestroyAllXapianInstances()

	// So sánh kết quả đọc của cả 5 docIds trên cả Validator và SyncNode
	readAll := func(dir string) []string {
		csNode, mkNode, ldr := advSetupOnDir(t, dir, contractHex)
		queries := []types.Transaction{
			mkNode(contractAddr, makeXapianV1GetDataData(t, "db_orders", docOrder1)),
			mkNode(contractAddr, makeXapianV1GetDataData(t, "db_orders", docOrder2)),
			mkNode(contractAddr, makeXapianV1GetDataData(t, "db_users", docUser1)),
			mkNode(contractAddr, makeXapianV1GetDataData(t, "db_users", docUser2)),
			mkNode(contractAddr, makeXapianV1GetDataData(t, "db_audit", docAudit1)),
		}
		rcs := runTxBatchOnChainState(t, csNode, contractAddr, ldr, queries)
		out := make([]string, len(rcs))
		for i, r := range rcs {
			var b []byte
			_ = advGetDataABI.UnpackIntoInterface(&b, "getDataDocument", r.Return())
			out[i] = string(b)
		}
		return out
	}

	valState := readAll(valDir)
	syncState := readAll(syncDir)
	expected := []string{"order_1_v2", "order_2_v1", "", "user_2_final", "audit_1_final"}
	require.Equal(t, expected, valState)
	require.Equal(t, valState, syncState, "Trạng thái 3 DB Xapian sau 3 block phải trùng khớp 100%% giữa Validator và SyncNode")
}
