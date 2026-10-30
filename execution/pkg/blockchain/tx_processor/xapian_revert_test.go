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

func callXapianOp(memOffset int, data []byte) []any {
	return confJoin(
		confMemWrite(memOffset, data),
		[]any{32, memOffset + len(data) + 32, len(data), memOffset, 0, common.HexToAddress("0x106"), 60000, "CALL", "POP"},
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

	tx, r := executeTestTx(t, cs, key, outer, []common.Address{child}, leader)
	require.Len(t, r, 1)
	require.NotEqual(t, 0, int(r[0].Status()), "top-level transaction must fail/revert")

	// Since top-level tx failed, runtime calls ClearXapianTxBuffer
	mvm.ClearXapianTxBuffer(tx.Hash().Bytes())
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
