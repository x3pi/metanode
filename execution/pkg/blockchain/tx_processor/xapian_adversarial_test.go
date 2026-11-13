package tx_processor

import (
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
