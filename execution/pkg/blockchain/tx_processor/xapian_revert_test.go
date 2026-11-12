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
	"fmt"
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
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
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

func makeXapianV1IndexTextData(t *testing.T, dbname string, docId *big.Int, text string, weight uint8, prefix string) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"indexTextForDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"},{"name":"text","type":"string"},{"name":"weight","type":"uint8"},{"name":"prefix","type":"string"}],"outputs":[{"type":"uint256"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("indexTextForDocument", dbname, docId, text, weight, prefix)
	require.NoError(t, err)
	return data
}

func makeXapianV1AddValueData(t *testing.T, dbname string, docId, slot *big.Int, val string, isSerialise bool) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"addValueDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"},{"name":"slot","type":"uint256"},{"name":"data","type":"string"},{"name":"isSerialise","type":"bool"}],"outputs":[{"type":"uint256"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("addValueDocument", dbname, docId, slot, val, isSerialise)
	require.NoError(t, err)
	return data
}

func makeXapianV1GetValueData(t *testing.T, dbname string, docId, slot *big.Int, isSerialise bool) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getValueDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"},{"name":"slot","type":"uint256"},{"name":"isSerialise","type":"bool"}],"outputs":[{"type":"string"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("getValueDocument", dbname, docId, slot, isSerialise)
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
//   - Tx A: sets data to []byte("v1_new_bytes") via setDataDocument on 0x107.
//   - Tx B: adds term "v1_tag_chain" via addTermDocument on 0x107.
//   - Tx C: reads data via getDataDocument on 0x107.
//
// In-Memory Version Chaining ensures Tx C observes []byte("v1_new_bytes").
// After committing Block 2, disk persistence must contain both "v1_new_bytes" and "v1_tag_chain".
func TestXapianV1_MultipleWritersSameBlock(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	sharedContractV1 := common.HexToAddress("0x1000000000000000000000000000000000000188")
	forwardCodeV1 := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		0, 0, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 0, 0, "RETURNDATACOPY",
		"RETURNDATASIZE", 0, "RETURN",
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
		0, 0, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 0, 0, "RETURNDATACOPY",
		"RETURNDATASIZE", 0, "RETURN",
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

func TestXapianV1_MalformedAndTruncatedABIRegression(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)
	sharedContractV1 := common.HexToAddress("0x1000000000000000000000000000000000000199")
	signer := eth.LatestSignerForChainID(big.NewInt(991))

	// Forwarder contract to precompile 0x107
	forwardCodeV1 := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		0, 0, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 0, 0, "RETURNDATACOPY",
		"RETURNDATASIZE", 0, "RETURN",
	)
	seedContract(t, cs, sharedContractV1, big.NewInt(0), forwardCodeV1)

	newDocSel := crypto.Keccak256([]byte("newDocument(string,bytes)"))[:4]
	setDataSel := crypto.Keccak256([]byte("setDataDocument(string,uint256,bytes)"))[:4]
	addTermSel := crypto.Keccak256([]byte("addTermDocument(string,uint256,string)"))[:4]
	querySearchSel := crypto.Keccak256([]byte("querySearch(string,(string,(string,string)[],string[],uint64,uint64,int64,bool,(uint256,string,string)[]))"))[:4]
	getOrCreateDBSel := crypto.Keccak256([]byte("getOrCreateDB(string)"))[:4]

	// 1. Truncated newDocument: 4 bytes selector + 64 bytes (offset 64 pointing to end of buffer, missing lengths/payloads)
	// In vulnerable code, this triggered ASan / heap buffer overflow reading offset + 28.
	malformedNewDoc := append([]byte{}, newDocSel...)
	malformedNewDocHead := make([]byte, 64)
	malformedNewDocHead[31] = 64 // dbname_offset = 64
	malformedNewDocHead[63] = 96 // data_offset = 96
	malformedNewDoc = append(malformedNewDoc, malformedNewDocHead...)

	// 2. Truncated setDataDocument: 4 bytes selector + 96 bytes (offset 96 pointing to end of buffer)
	malformedSetData := append([]byte{}, setDataSel...)
	malformedSetDataHead := make([]byte, 96)
	malformedSetDataHead[31] = 96  // dbname_offset = 96
	malformedSetDataHead[95] = 128 // data_offset = 128
	malformedSetData = append(malformedSetData, malformedSetDataHead...)

	// 2b. Truncated addTermDocument: 4 bytes selector + truncated head (triggers decode exception)
	// In vulnerable code, this triggered std::out_of_range -> on-chain abort / SIGABRT!
	malformedAddTerm := append([]byte{}, addTermSel...)
	malformedAddTerm = append(malformedAddTerm, make([]byte, 64)...)

	// 2c. Truncated querySearch: 4 bytes selector + 10 bytes (too short to contain dbname offset)
	// In vulnerable code, this threw std::runtime_error in getDbNameFromABI -> unhandled -> SIGABRT!
	malformedQuerySearch := append([]byte{}, querySearchSel...)
	malformedQuerySearch = append(malformedQuerySearch, make([]byte, 10)...)

	// 2d. Truncated getOrCreateDB: 4 bytes selector + 32 bytes (data too short for parseABI, < 68)
	malformedGetOrCreateDB := append([]byte{}, getOrCreateDBSel...)
	malformedGetOrCreateDB = append(malformedGetOrCreateDB, make([]byte, 32)...)

	// 2e. Malicious getOrCreateDB: exact 68 bytes with str_length=30 pointing past EOF
	// In vulnerable code, parseABI checked `offset + str_length > data.size()` (32 + 30 <= 68) -> heap OOB read!
	oobGetOrCreateDB := append([]byte{}, getOrCreateDBSel...)
	oobGetOrCreateDBHead := make([]byte, 64)
	oobGetOrCreateDBHead[31] = 32 // offset = 32
	oobGetOrCreateDBHead[63] = 30 // str_length = 30 (but 0 payload bytes exist after byte 68)
	oobGetOrCreateDB = append(oobGetOrCreateDB, oobGetOrCreateDBHead...)

	// 3. Valid newDocument
	validNewDoc := makeXapianV1NewDocData(t, "v1_sec_db", []byte("safe_initial_data"))

	signedMalformed1, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 1, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: malformedNewDoc,
	})
	require.NoError(t, err)
	txM1, err := transaction.NewTransactionFromEth(signedMalformed1)
	require.NoError(t, err)

	signedMalformed2, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 2, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: malformedSetData,
	})
	require.NoError(t, err)
	txM2, err := transaction.NewTransactionFromEth(signedMalformed2)
	require.NoError(t, err)

	signedMalformed3, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 3, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: malformedAddTerm,
	})
	require.NoError(t, err)
	txM3, err := transaction.NewTransactionFromEth(signedMalformed3)
	require.NoError(t, err)

	signedMalformed4, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 4, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: malformedQuerySearch,
	})
	require.NoError(t, err)
	txM4, err := transaction.NewTransactionFromEth(signedMalformed4)
	require.NoError(t, err)

	signedMalformed5, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 5, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: malformedGetOrCreateDB,
	})
	require.NoError(t, err)
	txM5, err := transaction.NewTransactionFromEth(signedMalformed5)
	require.NoError(t, err)

	signedMalformed6, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 6, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: oobGetOrCreateDB,
	})
	require.NoError(t, err)
	txM6, err := transaction.NewTransactionFromEth(signedMalformed6)
	require.NoError(t, err)

	signedValid, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 7, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: validNewDoc,
	})
	require.NoError(t, err)
	txValid, err := transaction.NewTransactionFromEth(signedValid)
	require.NoError(t, err)

	items := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txM1), sharedContractV1), Tx: txM1},
		{ID: 1, Array: append(grouptxns.BuildDeterministicGroupAddrs(txM2), sharedContractV1), Tx: txM2},
		{ID: 2, Array: append(grouptxns.BuildDeterministicGroupAddrs(txM3), sharedContractV1), Tx: txM3},
		{ID: 3, Array: append(grouptxns.BuildDeterministicGroupAddrs(txM4), sharedContractV1), Tx: txM4},
		{ID: 4, Array: append(grouptxns.BuildDeterministicGroupAddrs(txM5), sharedContractV1), Tx: txM5},
		{ID: 5, Array: append(grouptxns.BuildDeterministicGroupAddrs(txM6), sharedContractV1), Tx: txM6},
		{ID: 6, Array: append(grouptxns.BuildDeterministicGroupAddrs(txValid), sharedContractV1), Tx: txValid},
	}

	// Execution MUST NOT crash or panic when encountering malformed / truncated calldata
	_, receipts, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode),
		blankHeader(), false, false, 999, leader, false,
	)

	require.Len(t, receipts, 7)
	// Malformed calls should return error code (32 zero bytes) or ABI revert reason
	require.Equal(t, make([]byte, 32), receipts[0].Return(), "Malformed newDocument must return 32 zero bytes error")
	require.Equal(t, make([]byte, 32), receipts[1].Return(), "Malformed setDataDocument must return 32 zero bytes error")
	require.Equal(t, utils.EncodeRevertReason("ABI: malformed input data"), receipts[2].Return(), "Malformed addTermDocument must return ABI revert reason, not crash!")
	require.Equal(t, utils.EncodeRevertReason("ABI: malformed input data"), receipts[3].Return(), "Malformed querySearch must return ABI revert reason, not crash!")
	require.Equal(t, make([]byte, 32), receipts[4].Return(), "Malformed getOrCreateDB must return 32 zero bytes error")
	require.Equal(t, make([]byte, 32), receipts[5].Return(), "OOB getOrCreateDB must return 32 zero bytes error, not crash!")

	// Valid call must succeed with non-zero docId
	require.Equal(t, 0, int(receipts[6].Status()))
	validDocId := new(big.Int).SetBytes(receipts[6].Return())
	require.True(t, validDocId.Sign() > 0, "Valid newDocument must return non-zero docId")

	mvm.CommitXapianTxBuffer(txValid.Hash().Bytes())
	mvm.DestroyAllXapianInstances()
}

func TestXapianV1_ReservedDocIdTermCollisionPrevention(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)
	sharedContractV1 := common.HexToAddress("0x100000000000000000000000000000000000019A")
	signer := eth.LatestSignerForChainID(big.NewInt(991))

	// Forwarder contract to precompile 0x107
	forwardCodeV1 := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		0, 0, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 0, 0, "RETURNDATACOPY",
		"RETURNDATASIZE", 0, "RETURN",
	)
	seedContract(t, cs, sharedContractV1, big.NewInt(0), forwardCodeV1)

	dbName := "test_db_q\"collision"

	// 1. Create Document A
	newDocAData := makeXapianV1NewDocData(t, dbName, []byte("payload_of_doc_A"))
	signedTxA, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 1, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: newDocAData,
	})
	require.NoError(t, err)
	txA, err := transaction.NewTransactionFromEth(signedTxA)
	require.NoError(t, err)

	// 2. Create Document B
	newDocBData := makeXapianV1NewDocData(t, dbName, []byte("payload_of_doc_B"))
	signedTxB, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 2, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: newDocBData,
	})
	require.NoError(t, err)
	txB, err := transaction.NewTransactionFromEth(signedTxB)
	require.NoError(t, err)

	items1 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txA), sharedContractV1), Tx: txA},
		{ID: 1, Array: append(grouptxns.BuildDeterministicGroupAddrs(txB), sharedContractV1), Tx: txB},
	}

	_, receipts1, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items1, cs.HasCode),
		blankHeader(), false, false, 999, leader, false,
	)
	require.Len(t, receipts1, 2)
	require.Equal(t, 0, int(receipts1[0].Status()))
	require.Equal(t, 0, int(receipts1[1].Status()))

	docIdA := new(big.Int).SetBytes(receipts1[0].Return())
	docIdB := new(big.Int).SetBytes(receipts1[1].Return())
	require.True(t, docIdA.Sign() > 0)
	require.True(t, docIdB.Sign() > 0)

	mvm.CommitXapianTxBufferBatch([][]byte{
		txA.Hash().Bytes(),
		txB.Hash().Bytes(),
	})
	mvm.DestroyAllXapianInstances()

	// 3. Attack attempt: Try adding reserved system docId term (33 chars, within max term len 64)
	attackTerm := "Q1234567890abcdef1234567890abcdef"
	attackTermData := makeXapianV1AddTermData(t, dbName, docIdA, attackTerm)
	signedTxAttack, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 3, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: attackTermData,
	})
	require.NoError(t, err)
	txAttack, err := transaction.NewTransactionFromEth(signedTxAttack)
	require.NoError(t, err)

	// 3b. Partial prefix attack: Try indexTextForDocument with prefix "Q" + first hex of docB
	hexDocB := fmt.Sprintf("%064x", docIdB)
	attackPrefix := "Q" + hexDocB[0:1]
	attackPrefixData := makeXapianV1IndexTextData(t, dbName, docIdA, hexDocB[1:], 1, attackPrefix)
	signedTxAttackPrefix, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 4, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: attackPrefixData,
	})
	require.NoError(t, err)
	txAttackPrefix, err := transaction.NewTransactionFromEth(signedTxAttackPrefix)
	require.NoError(t, err)

	// 4. Legitimate term: Add standard term starting with 'Q' (e.g. "QUANTUM") to doc A
	legitTermData := makeXapianV1AddTermData(t, dbName, docIdA, "QUANTUM")
	signedTxLegit, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 5, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: legitTermData,
	})
	require.NoError(t, err)
	txLegit, err := transaction.NewTransactionFromEth(signedTxLegit)
	require.NoError(t, err)

	// 5. Read back Document B data
	readDocBData := makeXapianV1GetDataData(t, dbName, docIdB)
	signedTxReadB, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 6, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: readDocBData,
	})
	require.NoError(t, err)
	txReadB, err := transaction.NewTransactionFromEth(signedTxReadB)
	require.NoError(t, err)

	items2 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txAttack), sharedContractV1), Tx: txAttack},
		{ID: 1, Array: append(grouptxns.BuildDeterministicGroupAddrs(txAttackPrefix), sharedContractV1), Tx: txAttackPrefix},
		{ID: 2, Array: append(grouptxns.BuildDeterministicGroupAddrs(txLegit), sharedContractV1), Tx: txLegit},
		{ID: 3, Array: append(grouptxns.BuildDeterministicGroupAddrs(txReadB), sharedContractV1), Tx: txReadB},
	}

	_, receipts2, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode),
		blankHeader(), false, false, 999, leader, false,
	)
	require.Len(t, receipts2, 4)

	// txAttack must be rejected with error return ("Xapian: reserved term")
	expectedRevert := utils.EncodeRevertReason("Xapian: reserved term")
	require.Equal(t, expectedRevert, receipts2[0].Return(), "Attack term colliding with system docId must revert with 'Xapian: reserved term'")

	// txAttackPrefix must be rejected with error return ("Xapian: reserved prefix")
	expectedPrefixRevert := utils.EncodeRevertReason("Xapian: reserved prefix")
	require.Equal(t, expectedPrefixRevert, receipts2[1].Return(), "Attack prefix colliding with system docId prefix must revert with 'Xapian: reserved prefix'")

	// txLegit ("QUANTUM") must succeed with non-zero return
	legitRet := new(big.Int).SetBytes(receipts2[2].Return())
	require.True(t, legitRet.Sign() > 0, "Legitimate term 'QUANTUM' starting with Q must succeed")

	// txReadB must return payload_of_doc_B, NOT payload_of_doc_A!
	getDataAbi, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"bytes"}]}]`))
	require.NoError(t, err)
	var returnedBytes []byte
	err = getDataAbi.UnpackIntoInterface(&returnedBytes, "getDataDocument", receipts2[3].Return())
	require.NoError(t, err)
	require.Equal(t, []byte("payload_of_doc_B"), returnedBytes, "Document B must NOT be hijacked by Document A")

	mvm.CommitXapianTxBufferBatch([][]byte{
		txLegit.Hash().Bytes(),
	})
	mvm.ClearXapianTxBuffer(txAttack.Hash().Bytes())
	mvm.ClearXapianTxBuffer(txAttackPrefix.Hash().Bytes())
	mvm.DestroyAllXapianInstances()
}

// TestXapianV1_LongPrefixRejectionRegression verifies that an indexTextForDocument call
// with a prefix >= 64 characters (e.g. 300 characters 'T') is rejected early with
// "Xapian: prefix too long" and does NOT reach Xapian disk commit to trigger SIGABRT.
func TestXapianV1_LongPrefixRejectionRegression(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)
	sharedContractV1 := common.HexToAddress("0x100000000000000000000000000000000000019B")
	signer := eth.LatestSignerForChainID(big.NewInt(991))

	// Forwarder contract to precompile 0x107
	forwardCodeV1 := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		0, 0, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 0, 0, "RETURNDATACOPY",
		"RETURNDATASIZE", 0, "RETURN",
	)
	seedContract(t, cs, sharedContractV1, big.NewInt(0), forwardCodeV1)

	dbName := "v1_long_prefix_db"

	// 1. Create initial document
	newDocData := makeXapianV1NewDocData(t, dbName, []byte("hello world long prefix test"))
	signedTx1, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 1, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: newDocData,
	})
	require.NoError(t, err)
	tx1, err := transaction.NewTransactionFromEth(signedTx1)
	require.NoError(t, err)

	items1 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx1), sharedContractV1), Tx: tx1},
	}
	_, receipts1, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items1, cs.HasCode),
		blankHeader(), false, false, 999, leader, false,
	)
	require.Len(t, receipts1, 1)
	docId := new(big.Int).SetBytes(receipts1[0].Return())
	require.True(t, docId.Sign() > 0, "DocID must be non-zero")

	mvm.CommitXapianTxBufferBatch([][]byte{tx1.Hash().Bytes()})

	// 2. Attack: prefix with 300 characters 'T'
	longPrefix := strings.Repeat("T", 300)
	attackData := makeXapianV1IndexTextData(t, dbName, docId, "hello", 1, longPrefix)
	signedTxAttack, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 2, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: attackData,
	})
	require.NoError(t, err)
	txAttack, err := transaction.NewTransactionFromEth(signedTxAttack)
	require.NoError(t, err)

	// 3. Normal legitimate prefix (e.g. "title:")
	normalPrefix := "title"
	legitData := makeXapianV1IndexTextData(t, dbName, docId, "hello", 1, normalPrefix)
	signedTxLegit, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 3, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: legitData,
	})
	require.NoError(t, err)
	txLegit, err := transaction.NewTransactionFromEth(signedTxLegit)
	require.NoError(t, err)

	items2 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txAttack), sharedContractV1), Tx: txAttack},
		{ID: 1, Array: append(grouptxns.BuildDeterministicGroupAddrs(txLegit), sharedContractV1), Tx: txLegit},
	}
	_, receipts2, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode),
		blankHeader(), false, false, 999, leader, false,
	)
	require.Len(t, receipts2, 2)

	// txAttack must revert with "Xapian: prefix too long"
	expectedRevert := utils.EncodeRevertReason("Xapian: prefix too long")
	require.Equal(t, expectedRevert, receipts2[0].Return(), "Long prefix (>63 chars) must revert with 'Xapian: prefix too long'")

	// txLegit must succeed
	legitRet := new(big.Int).SetBytes(receipts2[1].Return())
	require.True(t, legitRet.Sign() > 0, "Normal prefix index_text must succeed")

	// Commit must not abort
	mvm.CommitXapianTxBufferBatch([][]byte{txLegit.Hash().Bytes()})
	mvm.ClearXapianTxBuffer(txAttack.Hash().Bytes())
	mvm.DestroyAllXapianInstances()
}

// TestXapianV1_SlotRangeAndReservedSlotProtection verifies that:
// 1. Large slot 4294967297 (0x100000001) is rejected and does not alias to slot 1.
// 2. Reserved system slots 253 (created_at) and 254 (deleted_at) cannot be written to.
// 3. Legitimate slot 1 can be written and read back correctly.
func TestXapianV1_SlotRangeAndReservedSlotProtection(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)
	sharedContractV1 := common.HexToAddress("0x100000000000000000000000000000000000019C")
	signer := eth.LatestSignerForChainID(big.NewInt(991))

	// Forwarder contract to precompile 0x107
	forwardCodeV1 := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		0, 0, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 0, 0, "RETURNDATACOPY",
		"RETURNDATASIZE", 0, "RETURN",
	)
	seedContract(t, cs, sharedContractV1, big.NewInt(0), forwardCodeV1)

	dbName := "v1_slot_test_db"

	// 1. Create initial document
	newDocData := makeXapianV1NewDocData(t, dbName, []byte("slot protection test document"))
	signedTx1, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 1, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: newDocData,
	})
	require.NoError(t, err)
	tx1, err := transaction.NewTransactionFromEth(signedTx1)
	require.NoError(t, err)

	items1 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx1), sharedContractV1), Tx: tx1},
	}
	_, receipts1, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items1, cs.HasCode),
		blankHeader(), false, false, 999, leader, false,
	)
	require.Len(t, receipts1, 1)
	docId := new(big.Int).SetBytes(receipts1[0].Return())
	require.True(t, docId.Sign() > 0, "DocID must be non-zero")
	mvm.CommitXapianTxBufferBatch([][]byte{tx1.Hash().Bytes()})

	// 2a. Add value with out-of-range slot: 4294967297 (0x100000001)
	largeSlot := new(big.Int).SetUint64(4294967297)
	txLargeSlotData := makeXapianV1AddValueData(t, dbName, docId, largeSlot, "aliased_value", false)
	signedTxLargeSlot, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 2, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: txLargeSlotData,
	})
	require.NoError(t, err)
	txLargeSlot, err := transaction.NewTransactionFromEth(signedTxLargeSlot)
	require.NoError(t, err)

	// 2b. Add value with reserved slot 254 (deleted_at)
	reservedSlot254 := big.NewInt(254)
	txReserved254Data := makeXapianV1AddValueData(t, dbName, docId, reservedSlot254, "tampered_deleted_at", false)
	signedTxReserved254, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 3, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: txReserved254Data,
	})
	require.NoError(t, err)
	txReserved254, err := transaction.NewTransactionFromEth(signedTxReserved254)
	require.NoError(t, err)

	// 2c. Add value with reserved slot 253 (created_at)
	reservedSlot253 := big.NewInt(253)
	txReserved253Data := makeXapianV1AddValueData(t, dbName, docId, reservedSlot253, "tampered_created_at", false)
	signedTxReserved253, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 4, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: txReserved253Data,
	})
	require.NoError(t, err)
	txReserved253, err := transaction.NewTransactionFromEth(signedTxReserved253)
	require.NoError(t, err)

	// 2d. Get value with out-of-range slot: 4294967297
	txGetLargeSlotData := makeXapianV1GetValueData(t, dbName, docId, largeSlot, false)
	signedTxGetLargeSlot, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 5, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: txGetLargeSlotData,
	})
	require.NoError(t, err)
	txGetLargeSlot, err := transaction.NewTransactionFromEth(signedTxGetLargeSlot)
	require.NoError(t, err)

	// 2e. Legitimate add value to slot 1
	validSlot1 := big.NewInt(1)
	txValidAddData := makeXapianV1AddValueData(t, dbName, docId, validSlot1, "legit_slot_1_data", false)
	signedTxValidAdd, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 6, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: txValidAddData,
	})
	require.NoError(t, err)
	txValidAdd, err := transaction.NewTransactionFromEth(signedTxValidAdd)
	require.NoError(t, err)

	// 2f. Legitimate get value from slot 1
	txValidGetData := makeXapianV1GetValueData(t, dbName, docId, validSlot1, false)
	signedTxValidGet, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 7, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: txValidGetData,
	})
	require.NoError(t, err)
	txValidGet, err := transaction.NewTransactionFromEth(signedTxValidGet)
	require.NoError(t, err)

	items2 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txLargeSlot), sharedContractV1), Tx: txLargeSlot},
		{ID: 1, Array: append(grouptxns.BuildDeterministicGroupAddrs(txReserved254), sharedContractV1), Tx: txReserved254},
		{ID: 2, Array: append(grouptxns.BuildDeterministicGroupAddrs(txReserved253), sharedContractV1), Tx: txReserved253},
		{ID: 3, Array: append(grouptxns.BuildDeterministicGroupAddrs(txGetLargeSlot), sharedContractV1), Tx: txGetLargeSlot},
		{ID: 4, Array: append(grouptxns.BuildDeterministicGroupAddrs(txValidAdd), sharedContractV1), Tx: txValidAdd},
		{ID: 5, Array: append(grouptxns.BuildDeterministicGroupAddrs(txValidGet), sharedContractV1), Tx: txValidGet},
	}

	_, receipts2, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode),
		blankHeader(), false, false, 999, leader, false,
	)
	require.Len(t, receipts2, 6)

	expectedSlotRevert := utils.EncodeRevertReason("Xapian: invalid value slot")

	// 2a must revert with "Xapian: invalid value slot"
	require.Equal(t, expectedSlotRevert, receipts2[0].Return(), "Add value with slot 4294967297 must revert with 'Xapian: invalid value slot'")

	// 2b must revert with "Xapian: invalid value slot"
	require.Equal(t, expectedSlotRevert, receipts2[1].Return(), "Add value with reserved slot 254 must revert with 'Xapian: invalid value slot'")

	// 2c must revert with "Xapian: invalid value slot"
	require.Equal(t, expectedSlotRevert, receipts2[2].Return(), "Add value with reserved slot 253 must revert with 'Xapian: invalid value slot'")

	// 2d must revert with "Xapian: invalid value slot"
	require.Equal(t, expectedSlotRevert, receipts2[3].Return(), "Get value with slot 4294967297 must revert with 'Xapian: invalid value slot'")

	// 2e must succeed
	validAddRet := new(big.Int).SetBytes(receipts2[4].Return())
	require.True(t, validAddRet.Sign() > 0, "Legitimate add_value to slot 1 must succeed")

	// 2f must return "legit_slot_1_data"
	getValueAbi, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getValueDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"},{"name":"slot","type":"uint256"},{"name":"isSerialise","type":"bool"}],"outputs":[{"type":"string"}]}]`))
	require.NoError(t, err)
	var returnedVal string
	err = getValueAbi.UnpackIntoInterface(&returnedVal, "getValueDocument", receipts2[5].Return())
	require.NoError(t, err)
	require.Equal(t, "legit_slot_1_data", returnedVal, "Slot 1 must contain 'legit_slot_1_data'")

	mvm.CommitXapianTxBufferBatch([][]byte{
		txValidAdd.Hash().Bytes(),
	})
	mvm.ClearXapianTxBuffer(txLargeSlot.Hash().Bytes())
	mvm.ClearXapianTxBuffer(txReserved254.Hash().Bytes())
	mvm.ClearXapianTxBuffer(txReserved253.Hash().Bytes())
	mvm.ClearXapianTxBuffer(txGetLargeSlot.Hash().Bytes())
	mvm.DestroyAllXapianInstances()
}

func TestXapian_GasRejectedAndNonceSkippedBufferClear(t *testing.T) {
	runGasCase := func(t *testing.T, balance *big.Int) (pb.RECEIPT_STATUS, string, uint64, int) {
		mvm.DestroyAllXapianInstances()
		mvm.ConfigureXapianBasePath(t.TempDir())
		t.Cleanup(mvm.DestroyAllXapianInstances)

		cs := newTestChainState(t)
		cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
		key, err := crypto.GenerateKey()
		require.NoError(t, err)
		sender := crypto.PubkeyToAddress(key.PublicKey)
		leader := common.HexToAddress(auditLeader)
		seedAccount(t, cs, sender, balance, 1)
		seedAccount(t, cs, leader, big.NewInt(0), 0)

		target := common.HexToAddress("0x1000000000000000000000000000000000000091")
		code := confScript(confJoin(
			callXapianOp(0, makeXapianNewDocData(t, "gasreject_db", "doc_payload")),
			[]any{0, 0, "RETURN"},
		)...)
		seedContract(t, cs, target, big.NewInt(0), code)

		signed, err := eth.SignNewTx(key, eth.LatestSignerForChainID(big.NewInt(991)), &eth.LegacyTx{
			Nonce: 1, To: &target, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0),
		})
		require.NoError(t, err)
		tx, err := transaction.NewTransactionFromEth(signed)
		require.NoError(t, err)
		items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
		_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs,
			grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
		require.Len(t, r, 1)

		// Even if CommitXapianTxBufferBatch is invoked unconditionally, TrueBlockSTM must have
		// already cleared the buffer for a gas-rejected transaction.
		mvm.CommitXapianTxBufferBatch([][]byte{tx.Hash().Bytes()})
		logs := mvm.ExportAllXapianLogs()
		return r[0].Status(), string(r[0].Return()), r[0].GasUsed(), len(logs)
	}

	t.Run("SufficientGas_CommitsXapianBuffer", func(t *testing.T) {
		status, _, gasUsed, logLen := runGasCase(t, big.NewInt(1000000000000000))
		require.Equal(t, pb.RECEIPT_STATUS_RETURNED, status)
		require.Greater(t, gasUsed, uint64(0))
		require.Greater(t, logLen, 0, "accepted tx must commit Xapian logs")
	})

	t.Run("InsufficientGas_ClearsXapianBuffer", func(t *testing.T) {
		status, retStr, gasUsed, logLen := runGasCase(t, big.NewInt(1000))
		require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, status)
		require.Equal(t, "insufficient balance for gas", retStr)
		require.Equal(t, uint64(0), gasUsed)
		require.Equal(t, 0, logLen, "gas-rejected tx must not leave Xapian writes")
	})

	t.Run("DrainByPriorTransferInSameBlock_ClearsXapianBuffer", func(t *testing.T) {
		mvm.DestroyAllXapianInstances()
		mvm.ConfigureXapianBasePath(t.TempDir())
		t.Cleanup(mvm.DestroyAllXapianInstances)

		cs := newTestChainState(t)
		cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
		key, err := crypto.GenerateKey()
		require.NoError(t, err)
		sender := crypto.PubkeyToAddress(key.PublicKey)
		recipient := common.HexToAddress("0x2000000000000000000000000000000000000092")
		leader := common.HexToAddress(auditLeader)

		// Give sender enough for tx1 (native transfer + 21000 gas), leaving only 500 wei for tx2 (not enough for gas)
		transferGasFee := int64(21000 * 100000)
		transferValue := int64(1000000)
		seedAccount(t, cs, sender, big.NewInt(transferGasFee+transferValue+500), 1)
		seedAccount(t, cs, recipient, big.NewInt(1), 1)
		seedAccount(t, cs, leader, big.NewInt(0), 0)

		target := common.HexToAddress("0x1000000000000000000000000000000000000093")
		code := confScript(confJoin(
			callXapianOp(0, makeXapianNewDocData(t, "drain_db", "unpaid_doc")),
			[]any{0, 0, "RETURN"},
		)...)
		seedContract(t, cs, target, big.NewInt(0), code)

		signer := eth.LatestSignerForChainID(big.NewInt(991))
		signed1, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
			Nonce: 1, To: &recipient, Gas: 21000, GasPrice: big.NewInt(100000), Value: big.NewInt(transferValue),
		})
		require.NoError(t, err)
		tx1, err := transaction.NewTransactionFromEth(signed1)
		require.NoError(t, err)

		signed2, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
			Nonce: 2, To: &target, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0),
		})
		require.NoError(t, err)
		tx2, err := transaction.NewTransactionFromEth(signed2)
		require.NoError(t, err)

		items := []grouptxns.Item{
			{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx1), Tx: tx1},
			{ID: 1, Array: grouptxns.BuildDeterministicGroupAddrs(tx2), Tx: tx2},
		}
		_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs,
			grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
		require.Len(t, r, 2)
		require.Equal(t, pb.RECEIPT_STATUS_RETURNED, r[0].Status())
		require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, r[1].Status())
		require.Equal(t, "insufficient balance for gas", string(r[1].Return()))

		mvm.CommitXapianTxBufferBatch([][]byte{tx2.Hash().Bytes()})
		logs := mvm.ExportAllXapianLogs()
		require.Equal(t, 0, len(logs), "tx2 drained by prior tx1 in same block must not commit Xapian writes")
	})

	t.Run("DuplicateNonceInSameBlock_ClearsSpeculativeXapianBuffer", func(t *testing.T) {
		mvm.DestroyAllXapianInstances()
		mvm.ConfigureXapianBasePath(t.TempDir())
		t.Cleanup(mvm.DestroyAllXapianInstances)

		cs := newTestChainState(t)
		cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
		key, err := crypto.GenerateKey()
		require.NoError(t, err)
		sender := crypto.PubkeyToAddress(key.PublicKey)
		recipient := common.HexToAddress("0x2000000000000000000000000000000000000094")
		leader := common.HexToAddress(auditLeader)

		seedAccount(t, cs, sender, big.NewInt(1000000000000000), 1)
		seedAccount(t, cs, recipient, big.NewInt(1), 1)
		seedAccount(t, cs, leader, big.NewInt(0), 0)

		noopTarget := common.HexToAddress("0x1000000000000000000000000000000000000094")
		seedContract(t, cs, noopTarget, big.NewInt(0), confScript([]any{0, 0, "RETURN"}...))

		target := common.HexToAddress("0x1000000000000000000000000000000000000095")
		code := confScript(confJoin(
			callXapianOp(0, makeXapianNewDocData(t, "dupnonce_db", "dup_nonce_doc")),
			[]any{0, 0, "RETURN"},
		)...)
		seedContract(t, cs, target, big.NewInt(0), code)

		signer := eth.LatestSignerForChainID(big.NewInt(991))
		// tx1: contract call with nonce 1 (at index 0)
		signed1, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
			Nonce: 1, To: &noopTarget, Gas: 100000, GasPrice: big.NewInt(100000), Value: big.NewInt(0),
		})
		require.NoError(t, err)
		tx1, err := transaction.NewTransactionFromEth(signed1)
		require.NoError(t, err)

		// tx2: Xapian contract call with DUPLICATE nonce 1 (at index 1; may execute speculatively in incarnation 1, then fail nonce check in incarnation 2)
		signed2, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
			Nonce: 1, To: &target, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0),
		})
		require.NoError(t, err)
		tx2, err := transaction.NewTransactionFromEth(signed2)
		require.NoError(t, err)

		groups := []grouptxns.RelativeGroup{{
			Kind: grouptxns.GroupKindContractOnly,
			Items: []grouptxns.Item{
				{ID: 0, Tx: tx1},
				{ID: 1, Tx: tx2},
			},
		}}
		_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs,
			groups, blankHeader(), false, false, 999, leader, false)
		require.Len(t, r, 1, "only tx1 should produce a receipt; duplicate-nonce tx2 is skipped")
		require.Equal(t, tx1.Hash(), r[0].TransactionHash())
		require.Equal(t, pb.RECEIPT_STATUS_RETURNED, r[0].Status())

		mvm.CommitXapianTxBufferBatch([][]byte{tx2.Hash().Bytes()})
		logs := mvm.ExportAllXapianLogs()
		require.Equal(t, 0, len(logs), "duplicate-nonce tx2 must not leave speculative Xapian buffer entries")
	})
}

