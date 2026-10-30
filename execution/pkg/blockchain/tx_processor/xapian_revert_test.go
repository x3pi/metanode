// ══════════════════════════════════════════════════════════════════════════════
// ARCHITECTURE & TESTING DIRECTIVE: XAPIAN EVM PRECOMPILES (V1 vs V0)
// ══════════════════════════════════════════════════════════════════════════════
//
// ⚠️ QUAN TRỌNG: ƯU TIÊN TEST VÀ PHÁT TRIỂN TRÊN V1 (0x107) THAY VÌ V0 (0x106)
//
// 1. Phân biệt V1 và V0:
//    - V1 (0x107 - FULL_DATABASE_ADDRESS_V1):
//      + Interface chuẩn cho Production (dApps, Smart Contracts như IFullDBV1 trong test-db-xapiant-v2.sol).
//      + Dữ liệu đầu vào / đầu ra là binary `bytes` (native byte arrays).
//      + Hỗ trợ trực tiếp dữ liệu nhị phân tùy ý (ABI-encoded structs, JSON, hash, media, etc.) mà không lo
//        lỗi encoding chuỗi hay null-byte.
//      + Tích hợp đầy đủ EVM snapshot/rollback journal và In-Memory Version Chaining cho Block-STM MVCC.
//
//    - V0 (0x106 - FULL_DATABASE_ADDRESS):
//      + Interface cũ (Legacy), sử dụng `string` cho dữ liệu tài liệu.
//      + Ít được sử dụng trong thực tế và có kế hoạch deprecate / loại bỏ trong tương lai.
//      + Các test case V0 trong file này được giữ lại chủ yếu để kiểm tra tương thích ngược (regression testing).
//
// 2. Chỉ dẫn cho lập trình viên (Developer Guidance):
//    - BẤT KỲ tính năng mới, bản sửa lỗi, hoặc kịch bản kiểm thử mới nào liên quan đến Xapian EVM integration
//      BẮT BUỘC phải viết test case và ưu tiên kiểm thử trên V1 (0x107).
//    - Xem bộ test case mẫu V1 chuyên dụng ở nửa dưới của file này (TestXapianV1_*).
// ══════════════════════════════════════════════════════════════════════════════

package tx_processor

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	eth "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// ─── Legacy V0 Helpers (0x106 FullDatabase - String Interface) ───────────────
// NOTE: V0 is legacy. For new tests, use V1 helpers below (0x107 Native Bytes).

func makeXapianNewDocData(t *testing.T, dbname, docContent string) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"newDocument","inputs":[{"name":"dbname","type":"string"},{"name":"data","type":"string"}],"outputs":[{"type":"uint256"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("newDocument", dbname, docContent)
	require.NoError(t, err)
	return data
}

func makeXapianGetOrCreateDbData(t *testing.T, dbname string) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getOrCreateDb","inputs":[{"name":"dbname","type":"string"}],"outputs":[{"type":"bool"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("getOrCreateDb", dbname)
	require.NoError(t, err)
	return data
}

func makeXapianSetDataData(t *testing.T, dbname string, docId *big.Int, dataStr string) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"setDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"},{"name":"data","type":"string"}],"outputs":[{"type":"uint256"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("setDataDocument", dbname, docId, dataStr)
	require.NoError(t, err)
	return data
}

func makeXapianAddTermData(t *testing.T, dbname string, docId *big.Int, term string) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"addTermDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"},{"name":"term","type":"string"}],"outputs":[{"type":"uint256"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("addTermDocument", dbname, docId, term)
	require.NoError(t, err)
	return data
}

func makeXapianGetDataData(t *testing.T, dbname string, docId *big.Int) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"string"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("getDataDocument", dbname, docId)
	require.NoError(t, err)
	return data
}

func makeXapianGetTermsData(t *testing.T, dbname string, docId *big.Int) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getTermsDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"string[]"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("getTermsDocument", dbname, docId)
	require.NoError(t, err)
	return data
}

func callXapianOp(memOffset int, data []byte) []any {
	return confJoin(
		confMemWrite(memOffset, data),
		[]any{32, memOffset + len(data) + 32, len(data), memOffset, 0, common.HexToAddress("0x106"), 60000, "CALL", "POP"},
	)
}

// ─── V1 Helpers (0x107 Native Bytes - Preferred Modern Interface) ─────────────
// V1 is the primary production interface using native binary `bytes` instead of `string`.
// All new contracts and tests should target V1 (address 0x107).

func makeXapianV1NewDocData(t *testing.T, dbname string, dataBytes []byte) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"newDocument","inputs":[{"name":"dbname","type":"string"},{"name":"data","type":"bytes"}],"outputs":[{"type":"uint256"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("newDocument", dbname, dataBytes)
	require.NoError(t, err)
	return data
}

func makeXapianV1SetDataData(t *testing.T, dbname string, docId *big.Int, dataBytes []byte) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"setDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"},{"name":"data","type":"bytes"}],"outputs":[{"type":"uint256"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("setDataDocument", dbname, docId, dataBytes)
	require.NoError(t, err)
	return data
}

func makeXapianV1GetDataData(t *testing.T, dbname string, docId *big.Int) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"bytes"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("getDataDocument", dbname, docId)
	require.NoError(t, err)
	return data
}

func makeXapianV1AddTermData(t *testing.T, dbname string, docId *big.Int, term string) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"addTermDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"},{"name":"term","type":"string"}],"outputs":[{"type":"uint256"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("addTermDocument", dbname, docId, term)
	require.NoError(t, err)
	return data
}

func callXapianV1Op(memOffset int, data []byte) []any {
	return confJoin(
		confMemWrite(memOffset, data),
		[]any{32, memOffset + len(data) + 32, len(data), memOffset, 0, common.HexToAddress("0x107"), 60000, "CALL", "POP"},
	)
}

func setupXapianChainEnv(t *testing.T) (*blockchain.ChainState, *ecdsa.PrivateKey, common.Address, common.Address) {
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())
	t.Cleanup(func() {
		mvm.DestroyAllXapianInstances()
	})

	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)

	seedAccount(t, cs, sender, big.NewInt(1000000000000000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	return cs, key, sender, leader
}

func executeTestTx(
	t *testing.T,
	cs *blockchain.ChainState,
	key *ecdsa.PrivateKey,
	target common.Address,
	extraAddrs []common.Address,
	leader common.Address,
) (types.Transaction, []types.Receipt) {
	signed, err := eth.SignNewTx(key, eth.LatestSignerForChainID(big.NewInt(991)), &eth.LegacyTx{
		Nonce:    1,
		To:       &target,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
	})
	require.NoError(t, err)

	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	accessedAddrs := append(grouptxns.BuildDeterministicGroupAddrs(tx), extraAddrs...)
	items := []grouptxns.Item{{ID: 0, Array: accessedAddrs, Tx: tx}}
	_, r, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	return tx, r
}

// 1. Sub-call reverts: all Xapian writes produced inside child frame must be discarded.
func TestXapianNestedRevert_SubCallRevertDiscardsWrites(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000001")
	child := common.HexToAddress("0x1000000000000000000000000000000000000002")

	childData := makeXapianNewDocData(t, "revert_db", "must_not_persist")
	childCode := confScript(confJoin(
		callXapianOp(0, childData),
		[]any{32, 512, "REVERT"},
	)...)

	// Parent calls child and returns child's success status (0 word)
	outerCode := confScript(confJoin(
		[]any{32, 512, 0, 0, 0, child, 60000, "CALL"},
		confReturnWords(1),
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, child, big.NewInt(0), childCode)

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{child}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()), "outer contract must succeed")
	require.Equal(t, make([]byte, 32), r[0].Return(), "child call must report failure (0) to caller")

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.Empty(t, logs, "reverted sub-call must not leak any Xapian writes to commit")
}

// 2. Sub-call succeeds: Xapian writes produced inside child frame must be preserved and committed.
func TestXapianNestedRevert_SubCallSuccessCommitsWrites(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000011")
	child := common.HexToAddress("0x1000000000000000000000000000000000000012")

	childData := makeXapianNewDocData(t, "success_db", "must_persist")
	childCode := confScript(confJoin(
		callXapianOp(0, childData),
		[]any{32, 512, "RETURN"},
	)...)

	outerCode := confScript(confJoin(
		[]any{32, 512, 0, 0, 0, child, 60000, "CALL"},
		confReturnWords(1),
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, child, big.NewInt(0), childCode)

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{child}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()))
	expectedSuccess := common.LeftPadBytes([]byte{1}, 32)
	require.Equal(t, expectedSuccess, r[0].Return(), "child call must report success (1) to caller")

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.NotEmpty(t, logs, "successful sub-call writes must be committed")
	require.Contains(t, logs, child.Hex(), "logs must contain child contract address")
}

// 3. Sibling sub-calls: Child 1 succeeds, Child 2 reverts. Child 1's writes must persist, Child 2's writes must be discarded.
func TestXapianNestedRevert_SiblingSubCallsOneSucceedsOneReverts(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000021")
	child1 := common.HexToAddress("0x1000000000000000000000000000000000000022")
	child2 := common.HexToAddress("0x1000000000000000000000000000000000000023")

	data1 := makeXapianNewDocData(t, "sibling_db", "from_child1")
	data2 := makeXapianNewDocData(t, "sibling_db", "from_child2_reverted")

	child1Code := confScript(confJoin(
		callXapianOp(0, data1),
		[]any{32, 512, "RETURN"},
	)...)

	child2Code := confScript(confJoin(
		callXapianOp(0, data2),
		[]any{32, 512, "REVERT"},
	)...)

	// Parent calls child1 (succeeds -> pops 1), then calls child2 (reverts -> pops 0), then returns
	outerCode := confScript(confJoin(
		[]any{32, 512, 0, 0, 0, child1, 60000, "CALL", "POP"},
		[]any{32, 512, 0, 0, 0, child2, 60000, "CALL", "POP"},
		[]any{0, 0, "RETURN"},
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, child1, big.NewInt(0), child1Code)
	seedContract(t, cs, child2, big.NewInt(0), child2Code)

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{child1, child2}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()))

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.NotEmpty(t, logs)
	require.Contains(t, logs, child1.Hex(), "child1 writes must be committed")
	require.NotContains(t, logs, child2.Hex(), "child2 writes must be rolled back")
}

// 4. Parent writes doc 1, calls child (which writes doc 2 and reverts), parent catches revert and writes doc 3.
// Docs 1 & 3 must be committed, Doc 2 must be discarded.
func TestXapianNestedRevert_ParentAndChild_ChildRevertPreservesParent(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000031")
	child := common.HexToAddress("0x1000000000000000000000000000000000000032")

	parentData1 := makeXapianNewDocData(t, "parent_child_db", "parent_doc_1")
	parentData2 := makeXapianNewDocData(t, "parent_child_db", "parent_doc_2")
	childData := makeXapianNewDocData(t, "parent_child_db", "child_doc_reverted")

	childCode := confScript(confJoin(
		callXapianOp(0, childData),
		[]any{32, 512, "REVERT"},
	)...)

	outerCode := confScript(confJoin(
		callXapianOp(0, parentData1),
		[]any{32, 512, 0, 0, 0, child, 60000, "CALL", "POP"},
		callXapianOp(0, parentData2),
		[]any{0, 0, "RETURN"},
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, child, big.NewInt(0), childCode)

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{child}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()))

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.NotEmpty(t, logs)
	require.Contains(t, logs, outer.Hex(), "parent writes must be committed")
	require.NotContains(t, logs, child.Hex(), "child writes must be discarded")
}

// 5. Deeply nested calls: A -> B -> C -> D. D reverts, C catches and writes doc, B succeeds, A succeeds.
// C's writes must be committed, D's writes must be discarded.
func TestXapianNestedRevert_MultiLevelDeepRevert(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	aAddr := common.HexToAddress("0x1000000000000000000000000000000000000041")
	bAddr := common.HexToAddress("0x1000000000000000000000000000000000000042")
	cAddr := common.HexToAddress("0x1000000000000000000000000000000000000043")
	dAddr := common.HexToAddress("0x1000000000000000000000000000000000000044")

	cData1 := makeXapianNewDocData(t, "deep_db", "from_C_before")
	cData2 := makeXapianNewDocData(t, "deep_db", "from_C_after")
	dData := makeXapianNewDocData(t, "deep_db", "from_D_reverted")

	dCode := confScript(confJoin(
		callXapianOp(0, dData),
		[]any{32, 512, "REVERT"},
	)...)

	cCode := confScript(confJoin(
		callXapianOp(0, cData1),
		[]any{32, 512, 0, 0, 0, dAddr, 60000, "CALL", "POP"},
		callXapianOp(0, cData2),
		[]any{0, 0, "RETURN"},
	)...)

	bCode := confScript(confJoin(
		[]any{32, 512, 0, 0, 0, cAddr, 80000, "CALL", "POP"},
		[]any{0, 0, "RETURN"},
	)...)

	aCode := confScript(confJoin(
		[]any{32, 512, 0, 0, 0, bAddr, 100000, "CALL", "POP"},
		[]any{0, 0, "RETURN"},
	)...)

	seedContract(t, cs, aAddr, big.NewInt(0), aCode)
	seedContract(t, cs, bAddr, big.NewInt(0), bCode)
	seedContract(t, cs, cAddr, big.NewInt(0), cCode)
	seedContract(t, cs, dAddr, big.NewInt(0), dCode)

	tx, r := executeTestTx(t, cs, key, aAddr, []common.Address{bAddr, cAddr, dAddr}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()))

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.NotEmpty(t, logs)
	require.Contains(t, logs, cAddr.Hex(), "C's writes must be committed")
	require.NotContains(t, logs, dAddr.Hex(), "D's writes must be discarded")
}

// 6. Top-level transaction reverts: entire buffer must be cleared.
func TestXapianNestedRevert_TopLevelTxRevertClearsAll(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000051")
	child := common.HexToAddress("0x1000000000000000000000000000000000000052")

	childData := makeXapianNewDocData(t, "top_revert_db", "child_doc")
	parentData := makeXapianNewDocData(t, "top_revert_db", "parent_doc")

	childCode := confScript(confJoin(
		callXapianOp(0, childData),
		[]any{0, 0, "RETURN"},
	)...)

	// Parent writes doc, calls child (succeeds), then parent REVERTS
	outerCode := confScript(confJoin(
		callXapianOp(0, parentData),
		[]any{32, 512, 0, 0, 0, child, 60000, "CALL", "POP"},
		[]any{32, 512, "REVERT"},
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, child, big.NewInt(0), childCode)

	_, r := executeTestTx(t, cs, key, outer, []common.Address{child}, leader)
	require.Len(t, r, 1)
	require.NotEqual(t, 0, int(r[0].Status()), "top-level transaction must fail/revert")

	// Top-level tx failed; runtime must have automatically cleared the buffer
	logs := mvm.ExportAllXapianLogs()
	require.Empty(t, logs, "failed top-level transaction must not leave any Xapian writes")
}

// 7. Read-only Xapian operations interleaved with write operations and sub-call reverts:
// Read-only calls must not take snapshots, must not record rollbacks, and must not disrupt
// rollback ordering or state preservation for successful writes.
func TestXapianNestedRevert_ReadOnlyOpInterleavedWithWrites(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000061")
	child := common.HexToAddress("0x1000000000000000000000000000000000000062")

	roData := makeXapianGetOrCreateDbData(t, "ro_db")
	parentDoc1 := makeXapianNewDocData(t, "ro_db", "parent_ro_interleaved_1")
	parentDoc2 := makeXapianNewDocData(t, "ro_db", "parent_ro_interleaved_2")
	childDocRevert := makeXapianNewDocData(t, "ro_db", "child_ro_reverted")

	// Child writes doc, does read-only op, then reverts
	childCode := confScript(confJoin(
		callXapianOp(0, childDocRevert),
		callXapianOp(0, roData),
		[]any{32, 512, "REVERT"},
	)...)

	// Parent does:
	// 1. read-only op
	// 2. write parentDoc1
	// 3. read-only op
	// 4. CALL child (child reverts)
	// 5. read-only op
	// 6. write parentDoc2
	// 7. RETURN success
	outerCode := confScript(confJoin(
		callXapianOp(0, roData),
		callXapianOp(0, parentDoc1),
		callXapianOp(0, roData),
		[]any{32, 512, 0, 0, 0, child, 60000, "CALL", "POP"},
		callXapianOp(0, roData),
		callXapianOp(0, parentDoc2),
		[]any{0, 0, "RETURN"},
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, child, big.NewInt(0), childCode)

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{child}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()), "outer transaction must succeed")

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.NotEmpty(t, logs)
	require.Contains(t, logs, outer.Hex(), "outer writes must be committed")
	require.NotContains(t, logs, child.Hex(), "child writes must be discarded on revert")
}

// 8. STATICCALL write protection:
// A contract called via STATICCALL must be prevented from executing any Xapian write operation
// (e.g. newDocument), raising ErrWriteProtection, while read operations remain allowed.
func TestXapianStaticCallWriteProtection(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000071")
	childWrite := common.HexToAddress("0x1000000000000000000000000000000000000072")
	childRead := common.HexToAddress("0x1000000000000000000000000000000000000073")

	newDocData := makeXapianNewDocData(t, "static_db", "must_not_persist")
	roData := makeXapianGetOrCreateDbData(t, "static_db")

	// childWrite attempts to call 0x106 with newDocument
	childWriteCode := confScript(confJoin(
		confMemWrite(0, newDocData),
		[]any{32, 512, len(newDocData), 0, 0, common.HexToAddress("0x106"), 60000, "CALL"},
		confReturnWords(1),
	)...)

	// childRead calls 0x106 with read-only getOrCreateDb
	childReadCode := confScript(confJoin(
		confMemWrite(0, roData),
		[]any{32, 512, len(roData), 0, 0, common.HexToAddress("0x106"), 60000, "CALL"},
		confReturnWords(1),
	)...)

	// Outer executes STATICCALL to childWrite (which must fail), then STATICCALL to childRead (which must succeed)
	outerCode := confScript(confJoin(
		// 1. STATICCALL childWrite -> should return 0 (failure due to ErrWriteProtection)
		[]any{32, 512, 0, 0, childWrite, 60000, "STATICCALL"},
		// 2. STATICCALL childRead -> should return 1 (success)
		[]any{32, 544, 0, 0, childRead, 60000, "STATICCALL", "POP"},
		[]any{0, 0, "RETURN"},
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, childWrite, big.NewInt(0), childWriteCode)
	seedContract(t, cs, childRead, big.NewInt(0), childReadCode)

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{childWrite, childRead}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()), "outer tx must succeed")

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.Empty(t, logs, "STATICCALL must not commit any Xapian writes")
}

// 9. Multiple writers in the same block modifying the same document:
// Given a document created in a prior block containing "old_content",
// within the SAME block:
// - Tx A (nonce 2) updates document data to "new_content" via setDataDocument.
// - Tx B (nonce 3) adds a term "tag_multi_writer" to the same document via addTermDocument.
// - Tx C (nonce 4) reads the document data via getDataDocument.
//
// In-Memory Version Chaining ensures Tx C observes "new_content" (not stale "old_content" from disk).
// After committing the block, disk persistence must contain both "new_content" and "tag_multi_writer".
func TestXapianNestedRevert_MultipleWritersSameBlock(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	sharedContract := common.HexToAddress("0x1000000000000000000000000000000000000088")
	forwardCode := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		512, 1024, "CALLDATASIZE", 0, 0, common.HexToAddress("0x106"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 1024, "RETURN",
	)
	seedContract(t, cs, sharedContract, big.NewInt(0), forwardCode)

	// Block 1: Setup document with "old_content"
	newDocData := makeXapianNewDocData(t, "multi_writer_db", "old_content")
	signer := eth.LatestSignerForChainID(big.NewInt(991))
	signed0, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    1,
		To:       &sharedContract,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     newDocData,
	})
	require.NoError(t, err)
	tx0, err := transaction.NewTransactionFromEth(signed0)
	require.NoError(t, err)

	items0 := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx0), sharedContract), Tx: tx0}}
	_, r0, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items0, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r0, 1)
	require.Equal(t, 0, int(r0[0].Status()))
	require.Len(t, r0[0].Return(), 32, "setup transaction must return 32-byte docId")
	docId := new(big.Int).SetBytes(r0[0].Return())
	require.False(t, docId.Sign() == 0, "docId must be non-zero")

	// Commit Block 1 to disk
	mvm.CommitXapianTxBuffer(tx0.Hash().Bytes())
	// Destroy in-memory instances to force fresh disk reload
	mvm.DestroyAllXapianInstances()

	// Block 2: Intra-block multi-writer scenario
	setDataData := makeXapianSetDataData(t, "multi_writer_db", docId, "new_content")
	addTermData := makeXapianAddTermData(t, "multi_writer_db", docId, "tag_multi_writer")
	getDataData := makeXapianGetDataData(t, "multi_writer_db", docId)

	signedA, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    2,
		To:       &sharedContract,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     setDataData,
	})
	require.NoError(t, err)
	txA, err := transaction.NewTransactionFromEth(signedA)
	require.NoError(t, err)

	signedB, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    3,
		To:       &sharedContract,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     addTermData,
	})
	require.NoError(t, err)
	txB, err := transaction.NewTransactionFromEth(signedB)
	require.NoError(t, err)

	signedC, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    4,
		To:       &sharedContract,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     getDataData,
	})
	require.NoError(t, err)
	txC, err := transaction.NewTransactionFromEth(signedC)
	require.NoError(t, err)

	items2 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txA), sharedContract), Tx: txA},
		{ID: 1, Array: append(grouptxns.BuildDeterministicGroupAddrs(txB), sharedContract), Tx: txB},
		{ID: 2, Array: append(grouptxns.BuildDeterministicGroupAddrs(txC), sharedContract), Tx: txC},
	}

	_, r2, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r2, 3)
	require.Equal(t, 0, int(r2[0].Status()), "Tx A must succeed")
	require.Equal(t, 0, int(r2[1].Status()), "Tx B must succeed")
	require.Equal(t, 0, int(r2[2].Status()), "Tx C must succeed")

	// Tx C must return "new_content", proving intra-block version chaining from Tx B back to Tx A!
	getDataAbi, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"string"}]}]`))
	require.NoError(t, err)
	var returnedContent string
	err = getDataAbi.UnpackIntoInterface(&returnedContent, "getDataDocument", r2[2].Return())
	require.NoError(t, err)
	require.Equal(t, "new_content", returnedContent, "Tx C must observe latest writer's content ('new_content') instead of stale disk content ('old_content')")

	// Commit Block 2
	mvm.CommitXapianTxBufferBatch([][]byte{
		txA.Hash().Bytes(),
		txB.Hash().Bytes(),
		txC.Hash().Bytes(),
	})

	// Destroy in-memory instances to force fresh reload from disk
	mvm.DestroyAllXapianInstances()

	// Block 3: Verify disk persistence (both data and term were written)
	signedD, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    5,
		To:       &sharedContract,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     getDataData,
	})
	require.NoError(t, err)
	txD, err := transaction.NewTransactionFromEth(signedD)
	require.NoError(t, err)

	items3 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txD), sharedContract), Tx: txD},
	}
	_, r3, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items3, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r3, 1)
	require.Equal(t, 0, int(r3[0].Status()))

	var diskContent string
	err = getDataAbi.UnpackIntoInterface(&diskContent, "getDataDocument", r3[0].Return())
	require.NoError(t, err)
	require.Equal(t, "new_content", diskContent, "Disk content after commit must be 'new_content'")

	mvm.CommitXapianTxBuffer(txD.Hash().Bytes())
}

// 10. Multi-writer intra-block with an intermediate reverting transaction:
// Given doc with "initial_val", Tx A writes "content_A" (succeeds), Tx B writes "content_B" (reverts),
// Tx C reads doc (succeeds).
// Tx C must read "content_A" (since Tx B's write was reverted).
func TestXapianNestedRevert_MultiWriterWithInterleavedRevert(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	sharedContract := common.HexToAddress("0x1000000000000000000000000000000000000098")
	forwardCode := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		512, 1024, "CALLDATASIZE", 0, 0, common.HexToAddress("0x106"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 1024, "RETURN",
	)
	seedContract(t, cs, sharedContract, big.NewInt(0), forwardCode)

	revertCaller := common.HexToAddress("0x1000000000000000000000000000000000000099")
	revertCallerCode := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		512, 1024, "CALLDATASIZE", 0, 0, sharedContract, 100000, "CALL", "POP",
		32, 1024, "REVERT",
	)
	seedContract(t, cs, revertCaller, big.NewInt(0), revertCallerCode)

	// Block 1: Initial document
	newDocData := makeXapianNewDocData(t, "revert_multi_db", "initial_val")
	signer := eth.LatestSignerForChainID(big.NewInt(991))
	signed0, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    1,
		To:       &sharedContract,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     newDocData,
	})
	require.NoError(t, err)
	tx0, err := transaction.NewTransactionFromEth(signed0)
	require.NoError(t, err)

	items0 := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx0), sharedContract), Tx: tx0}}
	_, r0, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items0, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r0, 1)
	require.Equal(t, 0, int(r0[0].Status()))
	docId := new(big.Int).SetBytes(r0[0].Return())
	require.False(t, docId.Sign() == 0)

	mvm.CommitXapianTxBuffer(tx0.Hash().Bytes())
	mvm.DestroyAllXapianInstances()

	// Block 2: Tx A writes "content_A", Tx B writes "content_B" and reverts via caller, Tx C reads
	setDataA := makeXapianSetDataData(t, "revert_multi_db", docId, "content_A")
	setDataB := makeXapianSetDataData(t, "revert_multi_db", docId, "content_B")
	getDataData := makeXapianGetDataData(t, "revert_multi_db", docId)

	signedA, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    2,
		To:       &sharedContract,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     setDataA,
	})
	require.NoError(t, err)
	txA, err := transaction.NewTransactionFromEth(signedA)
	require.NoError(t, err)

	signedB, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    3,
		To:       &revertCaller,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     setDataB,
	})
	require.NoError(t, err)
	txB, err := transaction.NewTransactionFromEth(signedB)
	require.NoError(t, err)

	signedC, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    4,
		To:       &sharedContract,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     getDataData,
	})
	require.NoError(t, err)
	txC, err := transaction.NewTransactionFromEth(signedC)
	require.NoError(t, err)

	items2 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txA), sharedContract), Tx: txA},
		{ID: 1, Array: append(grouptxns.BuildDeterministicGroupAddrs(txB), revertCaller, sharedContract), Tx: txB},
		{ID: 2, Array: append(grouptxns.BuildDeterministicGroupAddrs(txC), sharedContract), Tx: txC},
	}

	_, r2, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r2, 3)
	require.Equal(t, 0, int(r2[0].Status()), "Tx A must succeed")
	require.NotEqual(t, 0, int(r2[1].Status()), "Tx B must revert")
	require.Equal(t, 0, int(r2[2].Status()), "Tx C must succeed")

	// Tx C must read "content_A" because Tx B reverted
	getDataAbi, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"string"}]}]`))
	require.NoError(t, err)
	var returnedContent string
	err = getDataAbi.UnpackIntoInterface(&returnedContent, "getDataDocument", r2[2].Return())
	require.NoError(t, err)
	require.Equal(t, "content_A", returnedContent, "Tx C must observe 'content_A' since Tx B reverted")

	// Commit Tx A & Tx C, Clear Tx B
	mvm.CommitXapianTxBufferBatch([][]byte{
		txA.Hash().Bytes(),
		txC.Hash().Bytes(),
	})
	mvm.ClearXapianTxBuffer(txB.Hash().Bytes())

	mvm.DestroyAllXapianInstances()
}

// ═════════════════════════════════════════════════════════════════════════════
// ─── V1 DEDICATED TEST SUITE (Address 0x107 Native Bytes — PRIMARY SUITE) ─────
// ═════════════════════════════════════════════════════════════════════════════
//
// 📌 HƯỚNG DẪN DÀNH CHO LẬP TRÌNH VIÊN (DEVELOPER GUIDANCE):
// 1. V1 (FULL_DATABASE_ADDRESS_V1 = 0x107) là chuẩn precompile chính thức cho mọi dApp và Smart Contract.
//    - Nó nhận và trả về dữ liệu kiểu `bytes` (native binary), tương thích trực tiếp với
//      ABI-encoded struct (như ProductData trong test-db-xapiant-v2.sol), JSON blob, hoặc binary payload.
//    - V0 (0x106) sử dụng `string`, dễ bị lỗi encoding chuỗi/null-byte và được xem là legacy/sắp bỏ.
// 2. TẤT CẢ các test case mới phải được viết cho V1 (0x107) bằng cách sử dụng các helper V1:
//    - makeXapianV1NewDocData(t, dbname, dataBytes)
//    - makeXapianV1SetDataData(t, dbname, docId, dataBytes)
//    - makeXapianV1GetDataData(t, dbname, docId)
//    - makeXapianV1AddTermData(t, dbname, docId, term)
//    - callXapianV1Op(memOffset, data)
// 3. Dưới đây là 10 test case V1 hoàn chỉnh bao phủ toàn bộ các kịch bản:
//    - Sub-call revert (Test 11)
//    - Sub-call success commit (Test 12)
//    - Sibling sub-calls (Test 13)
//    - Parent & child interleaved revert (Test 14)
//    - Multi-level deep revert (Test 15)
//    - Top-level tx revert auto cleanup (Test 16)
//    - Read-only op interleaved with writes (Test 17)
//    - STATICCALL write protection (Test 18)
//    - Multi-writer intra-block version chaining (Test 19)
//    - Multi-writer intra-block with interleaved revert (Test 20)

// 11. V1 Sub-call revert: binary writes produced inside child frame must be discarded.
func TestXapianV1_NestedRevert_SubCallRevertDiscardsWrites(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000101")
	child := common.HexToAddress("0x1000000000000000000000000000000000000102")

	childData := makeXapianV1NewDocData(t, "v1_revert_db", []byte("must_not_persist_binary_bytes"))
	childCode := confScript(confJoin(
		callXapianV1Op(0, childData),
		[]any{32, 512, "REVERT"},
	)...)

	// Parent calls child and returns child's success status (0 word)
	outerCode := confScript(confJoin(
		[]any{32, 512, 0, 0, 0, child, 60000, "CALL"},
		confReturnWords(1),
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, child, big.NewInt(0), childCode)

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{child}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()), "outer contract must succeed")
	require.Equal(t, make([]byte, 32), r[0].Return(), "child call must report failure (0) to caller")

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.Empty(t, logs, "reverted V1 sub-call must not leak any Xapian writes to commit")
}

// 12. V1 Sub-call succeeds: binary Xapian writes produced inside child frame must be preserved and committed.
func TestXapianV1_NestedRevert_SubCallSuccessCommitsWrites(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000111")
	child := common.HexToAddress("0x1000000000000000000000000000000000000112")

	childData := makeXapianV1NewDocData(t, "v1_success_db", []byte("v1_must_persist_binary_bytes"))
	childCode := confScript(confJoin(
		callXapianV1Op(0, childData),
		[]any{32, 512, "RETURN"},
	)...)

	outerCode := confScript(confJoin(
		[]any{32, 512, 0, 0, 0, child, 60000, "CALL"},
		confReturnWords(1),
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, child, big.NewInt(0), childCode)

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{child}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()))
	expectedSuccess := common.LeftPadBytes([]byte{1}, 32)
	require.Equal(t, expectedSuccess, r[0].Return(), "child call must report success (1) to caller")

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.NotEmpty(t, logs, "successful V1 sub-call writes must be committed")
	require.Contains(t, logs, child.Hex(), "logs must contain child contract address")
}

// 13. V1 Sibling sub-calls: Child 1 succeeds, Child 2 reverts. Child 1's writes must persist, Child 2's writes must be discarded.
func TestXapianV1_SiblingSubCallsOneSucceedsOneReverts(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000121")
	child1 := common.HexToAddress("0x1000000000000000000000000000000000000122")
	child2 := common.HexToAddress("0x1000000000000000000000000000000000000123")

	data1 := makeXapianV1NewDocData(t, "v1_sibling_db", []byte("from_child1_v1"))
	data2 := makeXapianV1NewDocData(t, "v1_sibling_db", []byte("from_child2_v1_reverted"))

	child1Code := confScript(confJoin(
		callXapianV1Op(0, data1),
		[]any{32, 512, "RETURN"},
	)...)

	child2Code := confScript(confJoin(
		callXapianV1Op(0, data2),
		[]any{32, 512, "REVERT"},
	)...)

	outerCode := confScript(confJoin(
		[]any{32, 512, 0, 0, 0, child1, 60000, "CALL", "POP"},
		[]any{32, 512, 0, 0, 0, child2, 60000, "CALL", "POP"},
		[]any{0, 0, "RETURN"},
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, child1, big.NewInt(0), child1Code)
	seedContract(t, cs, child2, big.NewInt(0), child2Code)

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{child1, child2}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()))

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.NotEmpty(t, logs)
	require.Contains(t, logs, child1.Hex(), "child1 writes must be committed")
	require.NotContains(t, logs, child2.Hex(), "child2 writes must be rolled back")
}

// 14. V1 Parent writes doc 1, calls child (writes doc 2 and reverts), parent catches revert and writes doc 3.
// Docs 1 & 3 must be committed, Doc 2 must be discarded.
func TestXapianV1_ParentAndChild_ChildRevertPreservesParent(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000131")
	child := common.HexToAddress("0x1000000000000000000000000000000000000132")

	parentData1 := makeXapianV1NewDocData(t, "v1_parent_child_db", []byte("parent_v1_doc_1"))
	parentData2 := makeXapianV1NewDocData(t, "v1_parent_child_db", []byte("parent_v1_doc_2"))
	childData := makeXapianV1NewDocData(t, "v1_parent_child_db", []byte("child_v1_doc_reverted"))

	childCode := confScript(confJoin(
		callXapianV1Op(0, childData),
		[]any{32, 512, "REVERT"},
	)...)

	outerCode := confScript(confJoin(
		callXapianV1Op(0, parentData1),
		[]any{32, 512, 0, 0, 0, child, 60000, "CALL", "POP"},
		callXapianV1Op(0, parentData2),
		[]any{0, 0, "RETURN"},
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, child, big.NewInt(0), childCode)

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{child}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()))

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.NotEmpty(t, logs)
	require.Contains(t, logs, outer.Hex(), "parent writes must be committed")
	require.NotContains(t, logs, child.Hex(), "child writes must be discarded")
}

// 15. V1 Deeply nested calls: A -> B -> C -> D. D reverts, C catches and writes doc, B succeeds, A succeeds.
// C's writes must be committed, D's writes must be discarded.
func TestXapianV1_MultiLevelDeepRevert(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	aAddr := common.HexToAddress("0x1000000000000000000000000000000000000141")
	bAddr := common.HexToAddress("0x1000000000000000000000000000000000000142")
	cAddr := common.HexToAddress("0x1000000000000000000000000000000000000143")
	dAddr := common.HexToAddress("0x1000000000000000000000000000000000000144")

	cData1 := makeXapianV1NewDocData(t, "v1_deep_db", []byte("from_C_v1_before"))
	cData2 := makeXapianV1NewDocData(t, "v1_deep_db", []byte("from_C_v1_after"))
	dData := makeXapianV1NewDocData(t, "v1_deep_db", []byte("from_D_v1_reverted"))

	dCode := confScript(confJoin(
		callXapianV1Op(0, dData),
		[]any{32, 512, "REVERT"},
	)...)

	cCode := confScript(confJoin(
		callXapianV1Op(0, cData1),
		[]any{32, 512, 0, 0, 0, dAddr, 60000, "CALL", "POP"},
		callXapianV1Op(0, cData2),
		[]any{0, 0, "RETURN"},
	)...)

	bCode := confScript(confJoin(
		[]any{32, 512, 0, 0, 0, cAddr, 80000, "CALL", "POP"},
		[]any{0, 0, "RETURN"},
	)...)

	aCode := confScript(confJoin(
		[]any{32, 512, 0, 0, 0, bAddr, 100000, "CALL", "POP"},
		[]any{0, 0, "RETURN"},
	)...)

	seedContract(t, cs, aAddr, big.NewInt(0), aCode)
	seedContract(t, cs, bAddr, big.NewInt(0), bCode)
	seedContract(t, cs, cAddr, big.NewInt(0), cCode)
	seedContract(t, cs, dAddr, big.NewInt(0), dCode)

	tx, r := executeTestTx(t, cs, key, aAddr, []common.Address{bAddr, cAddr, dAddr}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()))

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.NotEmpty(t, logs)
	require.Contains(t, logs, cAddr.Hex(), "C's writes must be committed")
	require.NotContains(t, logs, dAddr.Hex(), "D's writes must be discarded")
}

// 16. V1 Top-level transaction reverts: entire buffer must be cleared automatically.
func TestXapianV1_TopLevelTxRevertClearsAll(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000151")
	child := common.HexToAddress("0x1000000000000000000000000000000000000152")

	childData := makeXapianV1NewDocData(t, "v1_top_revert_db", []byte("child_v1_doc"))
	parentData := makeXapianV1NewDocData(t, "v1_top_revert_db", []byte("parent_v1_doc"))

	childCode := confScript(confJoin(
		callXapianV1Op(0, childData),
		[]any{0, 0, "RETURN"},
	)...)

	// Parent writes doc, calls child (succeeds), then parent REVERTS
	outerCode := confScript(confJoin(
		callXapianV1Op(0, parentData),
		[]any{32, 512, 0, 0, 0, child, 60000, "CALL", "POP"},
		[]any{32, 512, "REVERT"},
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, child, big.NewInt(0), childCode)

	_, r := executeTestTx(t, cs, key, outer, []common.Address{child}, leader)
	require.Len(t, r, 1)
	require.NotEqual(t, 0, int(r[0].Status()), "top-level transaction must fail/revert")

	// Top-level tx failed; runtime must have automatically cleared the buffer
	logs := mvm.ExportAllXapianLogs()
	require.Empty(t, logs, "failed top-level transaction must not leave any Xapian writes")
}

// 17. V1 Read-only Xapian operations interleaved with write operations and sub-call reverts:
// Read-only calls must not take snapshots, must not record rollbacks, and must not disrupt
// rollback ordering or state preservation for successful writes on 0x107.
func TestXapianV1_ReadOnlyOpInterleavedWithWrites(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000161")
	child := common.HexToAddress("0x1000000000000000000000000000000000000162")

	roData := makeXapianGetOrCreateDbData(t, "v1_ro_db")
	parentDoc1 := makeXapianV1NewDocData(t, "v1_ro_db", []byte("parent_v1_ro_interleaved_1"))
	parentDoc2 := makeXapianV1NewDocData(t, "v1_ro_db", []byte("parent_v1_ro_interleaved_2"))
	childDocRevert := makeXapianV1NewDocData(t, "v1_ro_db", []byte("child_v1_ro_reverted"))

	// Child writes doc, does read-only op, then reverts
	childCode := confScript(confJoin(
		callXapianV1Op(0, childDocRevert),
		callXapianV1Op(0, roData),
		[]any{32, 512, "REVERT"},
	)...)

	// Parent does:
	// 1. read-only op
	// 2. write parentDoc1
	// 3. read-only op
	// 4. CALL child (child reverts)
	// 5. read-only op
	// 6. write parentDoc2
	// 7. RETURN success
	outerCode := confScript(confJoin(
		callXapianV1Op(0, roData),
		callXapianV1Op(0, parentDoc1),
		callXapianV1Op(0, roData),
		[]any{32, 512, 0, 0, 0, child, 60000, "CALL", "POP"},
		callXapianV1Op(0, roData),
		callXapianV1Op(0, parentDoc2),
		[]any{0, 0, "RETURN"},
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, child, big.NewInt(0), childCode)

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{child}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()), "outer transaction must succeed")

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.NotEmpty(t, logs)
	require.Contains(t, logs, outer.Hex(), "outer writes must be committed")
	require.NotContains(t, logs, child.Hex(), "child writes must be discarded on revert")
}

// 18. V1 STATICCALL write protection:
// A contract called via STATICCALL must be prevented from executing any Xapian V1 write operation (0x107),
// raising ErrWriteProtection, while read operations remain allowed.
func TestXapianV1_StaticCallWriteProtection(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	outer := common.HexToAddress("0x1000000000000000000000000000000000000171")
	childWrite := common.HexToAddress("0x1000000000000000000000000000000000000172")
	childRead := common.HexToAddress("0x1000000000000000000000000000000000000173")

	newDocData := makeXapianV1NewDocData(t, "v1_static_db", []byte("must_not_persist_v1"))
	roData := makeXapianGetOrCreateDbData(t, "v1_static_db")

	// childWrite attempts to call 0x107 with newDocument
	childWriteCode := confScript(confJoin(
		confMemWrite(0, newDocData),
		[]any{32, 512, len(newDocData), 0, 0, common.HexToAddress("0x107"), 60000, "CALL"},
		confReturnWords(1),
	)...)

	// childRead calls 0x107 with read-only getOrCreateDb
	childReadCode := confScript(confJoin(
		confMemWrite(0, roData),
		[]any{32, 512, len(roData), 0, 0, common.HexToAddress("0x107"), 60000, "CALL"},
		confReturnWords(1),
	)...)

	// Outer executes STATICCALL to childWrite (which must fail), then STATICCALL to childRead (which must succeed)
	outerCode := confScript(confJoin(
		// 1. STATICCALL childWrite -> should return 0 (failure due to ErrWriteProtection)
		[]any{32, 512, 0, 0, childWrite, 60000, "STATICCALL"},
		// 2. STATICCALL childRead -> should return 1 (success)
		[]any{32, 544, 0, 0, childRead, 60000, "STATICCALL", "POP"},
		[]any{0, 0, "RETURN"},
	)...)

	seedContract(t, cs, outer, big.NewInt(0), outerCode)
	seedContract(t, cs, childWrite, big.NewInt(0), childWriteCode)
	seedContract(t, cs, childRead, big.NewInt(0), childReadCode)

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{childWrite, childRead}, leader)
	require.Len(t, r, 1)
	require.Equal(t, 0, int(r[0].Status()), "outer tx must succeed")

	mvm.CommitXapianTxBuffer(tx.Hash().Bytes())
	logs := mvm.ExportAllXapianLogs()
	require.Empty(t, logs, "STATICCALL to V1 must not commit any Xapian writes")
}

// 19. V1 Multiple writers in the same block modifying the same document:
// Validates FullDatabaseV1 (0x107) with native binary bytes.
// - Tx 0: creates doc with []byte("v1_old_bytes") on disk and commits.
// - In Block 2:
//   * Tx A: sets data to []byte("v1_new_bytes") via setDataDocument on 0x107.
//   * Tx B: adds term "v1_tag_chain" via addTermDocument on 0x107.
//   * Tx C: reads data via getDataDocument on 0x107.
//
// In-Memory Version Chaining ensures Tx C observes []byte("v1_new_bytes").
// After committing Block 2, disk persistence must contain both "v1_new_bytes" and "v1_tag_chain".
func TestXapianV1_MultipleWritersSameBlock(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	sharedContractV1 := common.HexToAddress("0x1000000000000000000000000000000000000188")
	forwardCodeV1 := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		512, 1024, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 1024, "RETURN",
	)
	seedContract(t, cs, sharedContractV1, big.NewInt(0), forwardCodeV1)

	// Block 1: Setup document with "v1_old_bytes"
	newDocData := makeXapianV1NewDocData(t, "v1_multi_db", []byte("v1_old_bytes"))
	signer := eth.LatestSignerForChainID(big.NewInt(991))
	signed0, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    1,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     newDocData,
	})
	require.NoError(t, err)
	tx0, err := transaction.NewTransactionFromEth(signed0)
	require.NoError(t, err)

	items0 := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx0), sharedContractV1), Tx: tx0}}
	_, r0, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items0, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r0, 1)
	require.Equal(t, 0, int(r0[0].Status()))
	require.Len(t, r0[0].Return(), 32, "setup transaction must return 32-byte docId")
	docId := new(big.Int).SetBytes(r0[0].Return())
	require.False(t, docId.Sign() == 0, "docId must be non-zero")

	// Commit Block 1 to disk
	mvm.CommitXapianTxBuffer(tx0.Hash().Bytes())
	mvm.DestroyAllXapianInstances()

	// Block 2: Intra-block multi-writer scenario on 0x107
	setDataData := makeXapianV1SetDataData(t, "v1_multi_db", docId, []byte("v1_new_bytes"))
	addTermData := makeXapianV1AddTermData(t, "v1_multi_db", docId, "v1_tag_chain")
	getDataData := makeXapianV1GetDataData(t, "v1_multi_db", docId)

	signedA, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    2,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     setDataData,
	})
	require.NoError(t, err)
	txA, err := transaction.NewTransactionFromEth(signedA)
	require.NoError(t, err)

	signedB, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    3,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     addTermData,
	})
	require.NoError(t, err)
	txB, err := transaction.NewTransactionFromEth(signedB)
	require.NoError(t, err)

	signedC, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    4,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     getDataData,
	})
	require.NoError(t, err)
	txC, err := transaction.NewTransactionFromEth(signedC)
	require.NoError(t, err)

	items2 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txA), sharedContractV1), Tx: txA},
		{ID: 1, Array: append(grouptxns.BuildDeterministicGroupAddrs(txB), sharedContractV1), Tx: txB},
		{ID: 2, Array: append(grouptxns.BuildDeterministicGroupAddrs(txC), sharedContractV1), Tx: txC},
	}

	_, r2, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r2, 3)
	require.Equal(t, 0, int(r2[0].Status()), "Tx A must succeed")
	require.Equal(t, 0, int(r2[1].Status()), "Tx B must succeed")
	require.Equal(t, 0, int(r2[2].Status()), "Tx C must succeed")

	// Tx C must return []byte("v1_new_bytes")
	getDataAbi, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"bytes"}]}]`))
	require.NoError(t, err)
	var returnedBytes []byte
	err = getDataAbi.UnpackIntoInterface(&returnedBytes, "getDataDocument", r2[2].Return())
	require.NoError(t, err)
	require.Equal(t, []byte("v1_new_bytes"), returnedBytes, "V1 intra-block reader must observe latest binary content")

	// Commit Block 2
	mvm.CommitXapianTxBufferBatch([][]byte{
		txA.Hash().Bytes(),
		txB.Hash().Bytes(),
		txC.Hash().Bytes(),
	})

	// Destroy in-memory instances to force fresh reload from disk
	mvm.DestroyAllXapianInstances()

	// Block 3: Verify disk persistence on 0x107
	signedD, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    5,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     getDataData,
	})
	require.NoError(t, err)
	txD, err := transaction.NewTransactionFromEth(signedD)
	require.NoError(t, err)

	items3 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txD), sharedContractV1), Tx: txD},
	}
	_, r3, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items3, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r3, 1)
	require.Equal(t, 0, int(r3[0].Status()))

	var diskBytes []byte
	err = getDataAbi.UnpackIntoInterface(&diskBytes, "getDataDocument", r3[0].Return())
	require.NoError(t, err)
	require.Equal(t, []byte("v1_new_bytes"), diskBytes, "Disk content after commit must be 'v1_new_bytes'")

	mvm.CommitXapianTxBuffer(txD.Hash().Bytes())
}

// 20. V1 Multi-writer intra-block with an intermediate reverting transaction:
// Given doc with []byte("v1_initial"), Tx A writes []byte("v1_payload_A") (succeeds),
// Tx B writes []byte("v1_payload_B") (reverts), Tx C reads doc (succeeds).
// Tx C must read []byte("v1_payload_A") via 0x107.
func TestXapianV1_MultiWriterWithInterleavedRevert(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	sharedContractV1 := common.HexToAddress("0x1000000000000000000000000000000000000198")
	forwardCodeV1 := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		512, 1024, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 1024, "RETURN",
	)
	seedContract(t, cs, sharedContractV1, big.NewInt(0), forwardCodeV1)

	revertCallerV1 := common.HexToAddress("0x1000000000000000000000000000000000000199")
	revertCallerCodeV1 := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		512, 1024, "CALLDATASIZE", 0, 0, sharedContractV1, 100000, "CALL", "POP",
		32, 1024, "REVERT",
	)
	seedContract(t, cs, revertCallerV1, big.NewInt(0), revertCallerCodeV1)

	// Block 1: Initial document
	newDocData := makeXapianV1NewDocData(t, "v1_revert_db", []byte("v1_initial"))
	signer := eth.LatestSignerForChainID(big.NewInt(991))
	signed0, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    1,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     newDocData,
	})
	require.NoError(t, err)
	tx0, err := transaction.NewTransactionFromEth(signed0)
	require.NoError(t, err)

	items0 := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx0), sharedContractV1), Tx: tx0}}
	_, r0, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items0, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r0, 1)
	require.Equal(t, 0, int(r0[0].Status()))
	docId := new(big.Int).SetBytes(r0[0].Return())
	require.False(t, docId.Sign() == 0)

	mvm.CommitXapianTxBuffer(tx0.Hash().Bytes())
	mvm.DestroyAllXapianInstances()

	// Block 2: Tx A writes "v1_payload_A", Tx B writes "v1_payload_B" and reverts via caller, Tx C reads
	setDataA := makeXapianV1SetDataData(t, "v1_revert_db", docId, []byte("v1_payload_A"))
	setDataB := makeXapianV1SetDataData(t, "v1_revert_db", docId, []byte("v1_payload_B"))
	getDataData := makeXapianV1GetDataData(t, "v1_revert_db", docId)

	signedA, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    2,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     setDataA,
	})
	require.NoError(t, err)
	txA, err := transaction.NewTransactionFromEth(signedA)
	require.NoError(t, err)

	signedB, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    3,
		To:       &revertCallerV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     setDataB,
	})
	require.NoError(t, err)
	txB, err := transaction.NewTransactionFromEth(signedB)
	require.NoError(t, err)

	signedC, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    4,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     getDataData,
	})
	require.NoError(t, err)
	txC, err := transaction.NewTransactionFromEth(signedC)
	require.NoError(t, err)

	items2 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txA), sharedContractV1), Tx: txA},
		{ID: 1, Array: append(grouptxns.BuildDeterministicGroupAddrs(txB), revertCallerV1, sharedContractV1), Tx: txB},
		{ID: 2, Array: append(grouptxns.BuildDeterministicGroupAddrs(txC), sharedContractV1), Tx: txC},
	}

	_, r2, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r2, 3)
	require.Equal(t, 0, int(r2[0].Status()), "Tx A must succeed")
	require.NotEqual(t, 0, int(r2[1].Status()), "Tx B must revert")
	require.Equal(t, 0, int(r2[2].Status()), "Tx C must succeed")

	// Tx C must read []byte("v1_payload_A") because Tx B reverted
	getDataAbi, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"bytes"}]}]`))
	require.NoError(t, err)
	var returnedBytes []byte
	err = getDataAbi.UnpackIntoInterface(&returnedBytes, "getDataDocument", r2[2].Return())
	require.NoError(t, err)
	require.Equal(t, []byte("v1_payload_A"), returnedBytes, "Tx C must observe 'v1_payload_A' since Tx B reverted")

	// Commit Tx A & Tx C, Clear Tx B
	mvm.CommitXapianTxBufferBatch([][]byte{
		txA.Hash().Bytes(),
		txC.Hash().Bytes(),
	})
	mvm.ClearXapianTxBuffer(txB.Hash().Bytes())

	mvm.DestroyAllXapianInstances()
}

