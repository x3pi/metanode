package tx_processor

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

type xapianV1PrefixEntry struct {
	Key   string
	Value string
}

type xapianV1RangeFilter struct {
	Slot  *big.Int
	Start string
	End   string
}

type xapianV1QueryOptions struct {
	Queries         string
	PrefixMap       []xapianV1PrefixEntry
	StopWords       []string
	Offset          uint64
	Limit           uint64
	SortByValueSlot int64
	SortAscending   bool
	RangeFilters    []xapianV1RangeFilter
}

type xapianV1SearchResultItem struct {
	Docid   *big.Int
	Rank    *big.Int
	Percent *big.Int
	Data    []byte
}

type xapianV1SearchResultsPage struct {
	TotalCount *big.Int
	Results    []xapianV1SearchResultItem
}

const xapianV1QuerySearchABIJSON = `[
  {
    "type": "function",
    "name": "querySearch",
    "inputs": [
      {"name": "dbname", "type": "string"},
      {
        "name": "options",
        "type": "tuple",
        "components": [
          {"name": "queries", "type": "string"},
          {
            "name": "prefixMap",
            "type": "tuple[]",
            "components": [
              {"name": "key", "type": "string"},
              {"name": "value", "type": "string"}
            ]
          },
          {"name": "stopWords", "type": "string[]"},
          {"name": "offset", "type": "uint64"},
          {"name": "limit", "type": "uint64"},
          {"name": "sortByValueSlot", "type": "int64"},
          {"name": "sortAscending", "type": "bool"},
          {
            "name": "rangeFilters",
            "type": "tuple[]",
            "components": [
              {"name": "slot", "type": "uint256"},
              {"name": "start", "type": "string"},
              {"name": "end", "type": "string"}
            ]
          }
        ]
      }
    ],
    "outputs": [
      {
        "name": "page",
        "type": "tuple",
        "components": [
          {"name": "totalCount", "type": "uint256"},
          {
            "name": "results",
            "type": "tuple[]",
            "components": [
              {"name": "docid", "type": "uint256"},
              {"name": "rank", "type": "uint256"},
              {"name": "percent", "type": "int256"},
              {"name": "data", "type": "bytes"}
            ]
          }
        ]
      }
    ]
  }
]`

func makeXapianV1GetOrCreateDbData(t *testing.T, dbname string) []byte {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getOrCreateDb","inputs":[{"name":"dbname","type":"string"}],"outputs":[{"type":"bool"}]}]`))
	require.NoError(t, err)
	data, err := a.Pack("getOrCreateDb", dbname)
	require.NoError(t, err)
	return data
}

func makeXapianV1QuerySearchData(t *testing.T, dbname, query, defaultPrefix string) []byte {
	a, err := abi.JSON(strings.NewReader(xapianV1QuerySearchABIJSON))
	require.NoError(t, err)
	opts := xapianV1QueryOptions{
		Queries:         query,
		PrefixMap:       []xapianV1PrefixEntry{{Key: "", Value: defaultPrefix}},
		StopWords:       []string{},
		Offset:          0,
		Limit:           50,
		SortByValueSlot: -1,
		SortAscending:   false,
		RangeFilters:    []xapianV1RangeFilter{},
	}
	data, err := a.Pack("querySearch", dbname, opts)
	require.NoError(t, err)
	return data
}

func unpackXapianV1QuerySearchReturn(t *testing.T, raw []byte) (*big.Int, []xapianV1SearchResultItem) {
	a, err := abi.JSON(strings.NewReader(xapianV1QuerySearchABIJSON))
	require.NoError(t, err)
	var out struct {
		Page xapianV1SearchResultsPage
	}
	err = a.UnpackIntoInterface(&out, "querySearch", raw)
	require.NoError(t, err)
	return out.Page.TotalCount, out.Page.Results
}

// buildStatefulXapianV1Forwarder deploys bytecode that forwards arbitrary calldata to 0x107,
// stores keccak256(returndata) into storage[storage[0]], increments storage[0], and returns returndata.
// Because every Xapian read/search/write response is hashed into EVM contract storage, ANY divergence
// in Xapian data, values, wdf/BM25 percent, rank, or internal docid directly alters EVM state!
func buildStatefulXapianV1Forwarder() []byte {
	return confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		0, 0, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 500000, "CALL", "POP",
		"RETURNDATASIZE", 0, 0, "RETURNDATACOPY",
		"RETURNDATASIZE", 0, []byte{0x20}, // KECCAK256(offset=0, size=RETURNDATASIZE) -> [retHash]
		0, "SLOAD", 1, "ADD", // [retHash, nextIdx]
		[]byte{0x80}, 0, "SSTORE", // DUP1, PUSH 0, SSTORE -> storage[0] = nextIdx; stack: [retHash, nextIdx]
		"SSTORE", // storage[nextIdx] = retHash
		"RETURNDATASIZE", 0, "RETURN",
	)
}

func signAndWrapCall(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to common.Address, calldata []byte) types.Transaction {
	signer := eth.LatestSignerForChainID(big.NewInt(991))
	signed, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    nonce,
		To:       &to,
		Gas:      4000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     calldata,
	})
	require.NoError(t, err)
	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)
	return tx
}

func runTxBatchOnChainState(
	t *testing.T,
	cs *blockchain.ChainState,
	contractAddr common.Address,
	leader common.Address,
	txs []types.Transaction,
) []types.Receipt {
	items := make([]grouptxns.Item, len(txs))
	for i, tx := range txs {
		items[i] = grouptxns.Item{
			ID:    i,
			Array: append(grouptxns.BuildDeterministicGroupAddrs(tx), contractAddr),
			Tx:    tx,
		}
	}
	_, receipts, _, _ := ProcessTransactionsOptimistic(
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
	require.Len(t, receipts, len(txs))
	for i, rcp := range receipts {
		require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcp.Status(), "tx %d must succeed", i)
	}
	return receipts
}

func commitTxsToXapianAtBlock(txs []types.Transaction, blockNum uint64) map[string][]byte {
	hashes := make([][]byte, len(txs))
	for i, tx := range txs {
		hashes[i] = tx.Hash().Bytes()
	}
	mvm.CommitXapianTxBufferBatch(hashes)
	logs := mvm.ExportAllXapianLogs()
	mvm.CommitAllXapianAtBlock(blockNum)
	return logs
}

// 1. Validator Crash & Re-Execution Fork-Prevention Test:
// Simulates a crash right AFTER CommitAllXapianAtBlock(2) and BEFORE Block 2's EVM/BlockDB state
// is persisted. Verifies that startup RollbackXapianAboveBlock(1) + EVM re-execution of Block 2
// yields 100% identical EVM storage slots, receipts, exported FullDbLogs, BM25 rankings, and internal docids.
func TestXapianV1_CrashBetweenXapianCommitAndBlockPersist_PreventsForkOnReExecution(t *testing.T) {
	keyHex := "4f3edf983ac636a65a842ce7c78d9aa706d3b113bce9c46f30d7d21715b23b1d"
	key, err := crypto.HexToECDSA(keyHex)
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	contractAddr := common.HexToAddress("0x1000000000000000000000000000000000000701")
	const dbName = "crash_fork_guard_v1"

	newEnvAtBlock0 := func(t *testing.T, xapianDir string) *blockchain.ChainState {
		mvm.DestroyAllXapianInstances()
		mvm.ClearAllMVMApi()
		mvm.CallClearAllStateInstances()
		mvm.ConfigureXapianBasePath(xapianDir)

		cs := newTestChainState(t)
		cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
		seedAccount(t, cs, sender, big.NewInt(1000000000000000000), 1)
		seedAccount(t, cs, leader, big.NewInt(0), 0)
		seedContract(t, cs, contractAddr, big.NewInt(0), buildStatefulXapianV1Forwarder())
		return cs
	}

	executeBlock1 := func(t *testing.T, cs *blockchain.ChainState) (*big.Int, *big.Int, *big.Int) {
		txInit := signAndWrapCall(t, key, 1, contractAddr, makeXapianV1GetOrCreateDbData(t, dbName))
		txNewA := signAndWrapCall(t, key, 2, contractAddr, makeXapianV1NewDocData(t, dbName, []byte("block1_docA_payload")))
		txNewB := signAndWrapCall(t, key, 3, contractAddr, makeXapianV1NewDocData(t, dbName, []byte("block1_docB_to_delete")))
		txNewC := signAndWrapCall(t, key, 4, contractAddr, makeXapianV1NewDocData(t, dbName, []byte("block1_docC_anchor")))
		rcps1a := runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txInit, txNewA, txNewB, txNewC})

		docA := new(big.Int).SetBytes(rcps1a[1].Return())
		docB := new(big.Int).SetBytes(rcps1a[2].Return())
		docC := new(big.Int).SetBytes(rcps1a[3].Return())
		require.True(t, docA.Sign() > 0 && docB.Sign() > 0 && docC.Sign() > 0)

		txIdxA := signAndWrapCall(t, key, 5, contractAddr, makeXapianV1IndexTextData(t, dbName, docA, "consensus recovery alpha", 1, "S"))
		txValA := signAndWrapCall(t, key, 6, contractAddr, makeXapianV1AddValueData(t, dbName, docA, big.NewInt(512), "val_b1", false))
		txTermA := signAndWrapCall(t, key, 7, contractAddr, makeXapianV1AddTermData(t, dbName, docA, "Sbase"))
		txIdxB := signAndWrapCall(t, key, 8, contractAddr, makeXapianV1IndexTextData(t, dbName, docB, "consensus ephemeral", 1, "S"))
		txIdxC := signAndWrapCall(t, key, 9, contractAddr, makeXapianV1IndexTextData(t, dbName, docC, "consensus anchor", 1, "S"))
		runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txIdxA, txValA, txTermA, txIdxB, txIdxC})

		commitTxsToXapianAtBlock([]types.Transaction{txInit, txNewA, txNewB, txNewC, txIdxA, txValA, txTermA, txIdxB, txIdxC}, 1)
		return docA, docB, docC
	}

	// Block 2 FIRST reads Xapian (getData, getValue, querySearch -> all hashed into EVM SSTORE!)
	// and THEN mutates existing docA, deletes docB, creates docD, and runs a post-write querySearch!
	buildBlock2Txs := func(t *testing.T, docA, docB *big.Int) []types.Transaction {
		return []types.Transaction{
			signAndWrapCall(t, key, 10, contractAddr, makeXapianV1GetDataData(t, dbName, docA)),
			signAndWrapCall(t, key, 11, contractAddr, makeXapianV1GetValueData(t, dbName, docA, big.NewInt(512), false)),
			signAndWrapCall(t, key, 12, contractAddr, makeXapianV1QuerySearchData(t, dbName, "consensus", "S")),
			signAndWrapCall(t, key, 13, contractAddr, makeXapianV1SetDataData(t, dbName, docA, []byte("block2_docA_updated"))),
			signAndWrapCall(t, key, 14, contractAddr, makeXapianV1AddValueData(t, dbName, docA, big.NewInt(512), "val_b2", false)),
			signAndWrapCall(t, key, 15, contractAddr, makeXapianV1AddTermData(t, dbName, docA, "Sboost")),
			signAndWrapCall(t, key, 16, contractAddr, makeXapianV1IndexTextData(t, dbName, docA, "consensus consensus delta", 1, "S")),
			signAndWrapCall(t, key, 17, contractAddr, makeXapianV1DeleteDocData(t, dbName, docB)),
			signAndWrapCall(t, key, 18, contractAddr, makeXapianV1NewDocData(t, dbName, []byte("block2_docD_fresh"))),
		}
	}

	t.Cleanup(func() {
		mvm.DestroyAllXapianInstances()
	})

	// ── Node A: Healthy Reference Validator (No Crash) ──
	refDir := t.TempDir()
	csRef := newEnvAtBlock0(t, refDir)
	refDocA, refDocB, _ := executeBlock1(t, csRef)
	refB2Txs := buildBlock2Txs(t, refDocA, refDocB)
	refB2Rcps := runTxBatchOnChainState(t, csRef, contractAddr, leader, refB2Txs)
	refDocD := new(big.Int).SetBytes(refB2Rcps[8].Return())

	// Index docD and run final Block 2 search
	refTxIdxD := signAndWrapCall(t, key, 19, contractAddr, makeXapianV1IndexTextData(t, dbName, refDocD, "consensus fresh", 1, "S"))
	refTxFinalSearch := signAndWrapCall(t, key, 20, contractAddr, makeXapianV1QuerySearchData(t, dbName, "consensus", "S"))
	refB2TailRcps := runTxBatchOnChainState(t, csRef, contractAddr, leader, []types.Transaction{refTxIdxD, refTxFinalSearch})
	refLogsB2 := commitTxsToXapianAtBlock(append(refB2Txs, refTxIdxD, refTxFinalSearch), 2)

	// Snapshot reference EVM contract storage slots 0..20
	refStorage := make([][]byte, 21)
	for slot := 0; slot <= 20; slot++ {
		var slotKey common.Hash
		big.NewInt(int64(slot)).FillBytes(slotKey[:])
		val, _ := csRef.GetSmartContractDB().StorageValue(contractAddr, slotKey.Bytes())
		refStorage[slot] = val
	}
	refTotal, refResults := unpackXapianV1QuerySearchReturn(t, refB2TailRcps[1].Return())
	require.Equal(t, int64(3), refTotal.Int64(), "docA, docC, docD must match 'consensus' (docB deleted)")

	// ── Node B: Validator Crashes Right After CommitAllXapianAtBlock(2) Before Block 2 EVM Persist ──
	crashDir := t.TempDir()
	csCrashPre := newEnvAtBlock0(t, crashDir)
	cDocA, cDocB, _ := executeBlock1(t, csCrashPre)
	require.Equal(t, refDocA, cDocA)
	require.Equal(t, refDocB, cDocB)

	// Execute Block 2 before crash and flush Xapian to disk at Block 2!
	cB2Txs := buildBlock2Txs(t, cDocA, cDocB)
	cB2RcpsPre := runTxBatchOnChainState(t, csCrashPre, contractAddr, leader, cB2Txs)
	cDocDPre := new(big.Int).SetBytes(cB2RcpsPre[8].Return())
	cTxIdxDPre := signAndWrapCall(t, key, 19, contractAddr, makeXapianV1IndexTextData(t, dbName, cDocDPre, "consensus fresh", 1, "S"))
	cTxFinalSearchPre := signAndWrapCall(t, key, 20, contractAddr, makeXapianV1QuerySearchData(t, dbName, "consensus", "S"))
	runTxBatchOnChainState(t, csCrashPre, contractAddr, leader, []types.Transaction{cTxIdxDPre, cTxFinalSearchPre})
	commitTxsToXapianAtBlock(append(cB2Txs, cTxIdxDPre, cTxFinalSearchPre), 2)

	// 💥 SIMULATE ABRUPT NODE CRASH BEFORE BlockDB / NOMT PERSISTED BLOCK 2:
	// Process restarts with cold Xapian directory (crashDir) and EVM state restored only up to Block 1!
	mvm.DestroyAllXapianInstances()
	mvm.ClearAllMVMApi()
	mvm.CallClearAllStateInstances()

	// Build post-restart EVM state at Block 1 (using a scratch Xapian dir just to reconstruct Block 1 EVM state cleanly,
	// then switching back to crashDir to simulate startup where BlockDB=1 and crashDir Xapian=2!)
	csRestartedAtB1 := newEnvAtBlock0(t, t.TempDir())
	executeBlock1(t, csRestartedAtB1)
	mvm.DestroyAllXapianInstances()
	mvm.ClearAllMVMApi()
	mvm.CallClearAllStateInstances()
	mvm.ConfigureXapianBasePath(crashDir)

	// 🛡️ STARTUP CRASH RECOVERY: Roll back Xapian on cold disk to Block 1!
	rolledBackCount, rbErr := mvm.RollbackXapianAboveBlock(1)
	require.NoError(t, rbErr)
	require.Equal(t, 1, rolledBackCount, "exactly 1 Xapian DB must be rolled back from Block 2 to Block 1")

	// Re-execute Block 2 on recovered Node B
	reB2Rcps := runTxBatchOnChainState(t, csRestartedAtB1, contractAddr, leader, cB2Txs)
	reDocD := new(big.Int).SetBytes(reB2Rcps[8].Return())
	require.Equal(t, refDocD, reDocD)

	reTxIdxD := signAndWrapCall(t, key, 19, contractAddr, makeXapianV1IndexTextData(t, dbName, reDocD, "consensus fresh", 1, "S"))
	reTxFinalSearch := signAndWrapCall(t, key, 20, contractAddr, makeXapianV1QuerySearchData(t, dbName, "consensus", "S"))
	reB2TailRcps := runTxBatchOnChainState(t, csRestartedAtB1, contractAddr, leader, []types.Transaction{reTxIdxD, reTxFinalSearch})
	reLogsB2 := commitTxsToXapianAtBlock(append(cB2Txs, reTxIdxD, reTxFinalSearch), 2)

	// Verify pre-write read return values during re-execution saw clean Block 1 state (NOT dirty Block 2!)
	var readPreWriteData []byte
	getDataAbi, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"bytes"}]}]`))
	require.NoError(t, err)
	require.NoError(t, getDataAbi.UnpackIntoInterface(&readPreWriteData, "getDataDocument", reB2Rcps[0].Return()))
	require.Equal(t, []byte("block1_docA_payload"), readPreWriteData, "re-execution must read Block 1 data before Block 2 setData")

	// Verify ALL receipts, EVM storage slots (proving identical AccountStatesRoot!), FullDbLogs, and BM25 search output match Node A 100%!
	for i := range refB2Rcps {
		require.Equal(t, refB2Rcps[i].Return(), reB2Rcps[i].Return(), "Block 2 receipt[%d] return mismatch after crash recovery", i)
	}
	for i := range refB2TailRcps {
		require.Equal(t, refB2TailRcps[i].Return(), reB2TailRcps[i].Return(), "Block 2 tail receipt[%d] return mismatch after crash recovery", i)
	}
	for slot := 0; slot <= 20; slot++ {
		var slotKey common.Hash
		big.NewInt(int64(slot)).FillBytes(slotKey[:])
		gotSlot, _ := csRestartedAtB1.GetSmartContractDB().StorageValue(contractAddr, slotKey.Bytes())
		require.Equal(t, refStorage[slot], gotSlot, "EVM storage slot %d diverged (would cause AccountStatesRoot consensus fork!)", slot)
	}
	require.Equal(t, refLogsB2, reLogsB2, "exported FullDbLogs must be byte-for-byte identical")

	reTotal, reResults := unpackXapianV1QuerySearchReturn(t, reB2TailRcps[1].Return())
	require.Equal(t, refTotal, reTotal)
	require.Equal(t, refResults, reResults, "BM25 docids, ranks, percents, and payloads must match healthy node")
}

// 2. Multi-Block Pipeline Crash Test + SyncOnly ReplayFullDbLogsBatchAtBlock Idempotency Test:
// Verifies rolling back multiple pipelined blocks (Blocks 2, 3, 4 -> Block 1) from cold disk AND
// verifies that replaying exported FullDbLogs twice at the same block height on a SyncOnly node is 100% idempotent.
func TestXapianV1_MultiBlockPipelineRollback_And_SyncReplayIdempotency(t *testing.T) {
	keyHex := "6cbed15c793ce57650b9877cf6fa156fbef513c4e6134f022a85b1ffdd59b2a1"
	key, err := crypto.HexToECDSA(keyHex)
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	contractAddr := common.HexToAddress("0x1000000000000000000000000000000000000702")
	const dbName = "pipeline_sync_v1"

	t.Cleanup(func() {
		mvm.DestroyAllXapianInstances()
	})

	valDir := t.TempDir()
	mvm.DestroyAllXapianInstances()
	mvm.ClearAllMVMApi()
	mvm.CallClearAllStateInstances()
	mvm.ConfigureXapianBasePath(valDir)

	csVal := newTestChainState(t)
	csVal.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	seedAccount(t, csVal, sender, big.NewInt(1000000000000000000), 1)
	seedAccount(t, csVal, leader, big.NewInt(0), 0)
	seedContract(t, csVal, contractAddr, big.NewInt(0), buildStatefulXapianV1Forwarder())

	// Block 1: create doc1 + index text
	tx1 := signAndWrapCall(t, key, 1, contractAddr, makeXapianV1GetOrCreateDbData(t, dbName))
	tx2 := signAndWrapCall(t, key, 2, contractAddr, makeXapianV1NewDocData(t, dbName, []byte("v1_b1")))
	rB1a := runTxBatchOnChainState(t, csVal, contractAddr, leader, []types.Transaction{tx1, tx2})
	doc1 := new(big.Int).SetBytes(rB1a[1].Return())
	tx3 := signAndWrapCall(t, key, 3, contractAddr, makeXapianV1IndexTextData(t, dbName, doc1, "metanode search", 1, "S"))
	runTxBatchOnChainState(t, csVal, contractAddr, leader, []types.Transaction{tx3})
	logsB1 := commitTxsToXapianAtBlock([]types.Transaction{tx1, tx2, tx3}, 1)

	// Block 2: update doc1 with ADD_TERM + INDEX_TEXT + high value slot 1024, plus create doc2
	tx4 := signAndWrapCall(t, key, 4, contractAddr, makeXapianV1SetDataData(t, dbName, doc1, []byte("v1_b2")))
	tx5 := signAndWrapCall(t, key, 5, contractAddr, makeXapianV1AddTermData(t, dbName, doc1, "Stag2"))
	tx6 := signAndWrapCall(t, key, 6, contractAddr, makeXapianV1IndexTextData(t, dbName, doc1, "metanode metanode boost", 1, "S"))
	tx7 := signAndWrapCall(t, key, 7, contractAddr, makeXapianV1AddValueData(t, dbName, doc1, big.NewInt(1024), "high_b2", false))
	tx8 := signAndWrapCall(t, key, 8, contractAddr, makeXapianV1NewDocData(t, dbName, []byte("doc2_b2")))
	rB2a := runTxBatchOnChainState(t, csVal, contractAddr, leader, []types.Transaction{tx4, tx5, tx6, tx7, tx8})
	doc2 := new(big.Int).SetBytes(rB2a[4].Return())
	tx9 := signAndWrapCall(t, key, 9, contractAddr, makeXapianV1IndexTextData(t, dbName, doc2, "metanode peer", 1, "S"))
	runTxBatchOnChainState(t, csVal, contractAddr, leader, []types.Transaction{tx9})
	logsB2 := commitTxsToXapianAtBlock([]types.Transaction{tx4, tx5, tx6, tx7, tx8, tx9}, 2)

	// Block 3 & Block 4 pipelined ahead in Xapian on Validator before simulated crash back to Block 2!
	tx10 := signAndWrapCall(t, key, 10, contractAddr, makeXapianV1SetDataData(t, dbName, doc1, []byte("v1_b3_unpersisted")))
	tx11 := signAndWrapCall(t, key, 11, contractAddr, makeXapianV1DeleteDocData(t, dbName, doc2))
	runTxBatchOnChainState(t, csVal, contractAddr, leader, []types.Transaction{tx10, tx11})
	commitTxsToXapianAtBlock([]types.Transaction{tx10, tx11}, 3)

	tx12 := signAndWrapCall(t, key, 12, contractAddr, makeXapianV1NewDocData(t, dbName, []byte("doc3_b4_unpersisted")))
	runTxBatchOnChainState(t, csVal, contractAddr, leader, []types.Transaction{tx12})
	commitTxsToXapianAtBlock([]types.Transaction{tx12}, 4)

	// Simulate Validator crash when BlockDB only persisted up to Block 2 -> roll back Blocks 4 & 3!
	mvm.DestroyAllXapianInstances()
	rbCount, rbErr := mvm.RollbackXapianAboveBlock(2)
	require.NoError(t, rbErr)
	require.Equal(t, 1, rbCount)

	// Query Validator's Block 2 Xapian state after rolling back Blocks 3 & 4
	txValSearch := signAndWrapCall(t, key, 13, contractAddr, makeXapianV1QuerySearchData(t, dbName, "metanode", "S"))
	txValGetVal := signAndWrapCall(t, key, 14, contractAddr, makeXapianV1GetValueData(t, dbName, doc1, big.NewInt(1024), false))
	valRcps := runTxBatchOnChainState(t, csVal, contractAddr, leader, []types.Transaction{txValSearch, txValGetVal})
	valTotal, valResults := unpackXapianV1QuerySearchReturn(t, valRcps[0].Return())
	require.Equal(t, int64(2), valTotal.Int64(), "both doc1 and doc2 must exist at Block 2 after rolling back Blocks 3 & 4")

	// ── SyncOnly Node: Replay Block 1 once, then Replay Block 2 TWICE (simulating interrupted epoch sync retry) ──
	syncDir := t.TempDir()
	mvm.DestroyAllXapianInstances()
	mvm.ClearAllMVMApi()
	mvm.CallClearAllStateInstances()
	mvm.ConfigureXapianBasePath(syncDir)

	require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{logsB1}, 1))
	mvm.CommitAllXapianAtBlock(1)

	require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{logsB2}, 2))
	mvm.CommitAllXapianAtBlock(2)

	// Simulate sync interruption and duplicate replay of Block 2 FullDbLogs!
	mvm.DestroyAllXapianInstances()
	require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{logsB2}, 2))
	mvm.CommitAllXapianAtBlock(2)

	// Query SyncOnly node's Xapian state and verify 100% equivalence with Validator at Block 2!
	csSync := newTestChainState(t)
	csSync.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	seedAccount(t, csSync, sender, big.NewInt(1000000000000000000), 1)
	seedAccount(t, csSync, leader, big.NewInt(0), 0)
	seedContract(t, csSync, contractAddr, big.NewInt(0), buildStatefulXapianV1Forwarder())

	txSyncSearch := signAndWrapCall(t, key, 1, contractAddr, makeXapianV1QuerySearchData(t, dbName, "metanode", "S"))
	txSyncGetVal := signAndWrapCall(t, key, 2, contractAddr, makeXapianV1GetValueData(t, dbName, doc1, big.NewInt(1024), false))
	syncRcps := runTxBatchOnChainState(t, csSync, contractAddr, leader, []types.Transaction{txSyncSearch, txSyncGetVal})
	syncTotal, syncResults := unpackXapianV1QuerySearchReturn(t, syncRcps[0].Return())

	require.Equal(t, valTotal, syncTotal)
	require.Equal(t, valResults, syncResults, "SyncOnly node after duplicate Block 2 replay must match Validator BM25 results & docids 100%")
	require.Equal(t, valRcps[1].Return(), syncRcps[1].Return(), "high value slot 1024 must match Validator 100%")
}

// 3. Draft Block Revert Test:
// Verifies ClearAllXapianTxBuffers() + RollbackXapianAboveBlock(parentBlock) discards all buffered
// and committed Xapian mutations from an aborted draft block.
func TestXapianV1_DraftBlockRevert_ClearsBuffersAndRollsBackAboveParent(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)
	contractAddr := common.HexToAddress("0x1000000000000000000000000000000000000703")
	seedContract(t, cs, contractAddr, big.NewInt(0), buildStatefulXapianV1Forwarder())
	const dbName = "draft_revert_v1"

	// Block 1 committed
	tx1 := signAndWrapCall(t, key, 1, contractAddr, makeXapianV1GetOrCreateDbData(t, dbName))
	tx2 := signAndWrapCall(t, key, 2, contractAddr, makeXapianV1NewDocData(t, dbName, []byte("parent_b1")))
	rcps1 := runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{tx1, tx2})
	doc1 := new(big.Int).SetBytes(rcps1[1].Return())
	commitTxsToXapianAtBlock([]types.Transaction{tx1, tx2}, 1)

	// Draft Block 2 buffers writes in tx_buffers (not yet committed)
	txDraft := signAndWrapCall(t, key, 3, contractAddr, makeXapianV1SetDataData(t, dbName, doc1, []byte("aborted_draft_b2")))
	runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txDraft})

	// Simulate revertDraftBlock cleanup
	mvm.ClearAllXapianTxBuffers()
	_, rbErr := mvm.RollbackXapianAboveBlock(1)
	require.NoError(t, rbErr)

	// Even if CommitXapianTxBuffer is called later for txDraft, nothing was left in tx_buffers
	mvm.CommitXapianTxBuffer(txDraft.Hash().Bytes())
	require.Empty(t, mvm.ExportAllXapianLogs())

	txRead := signAndWrapCall(t, key, 4, contractAddr, makeXapianV1GetDataData(t, dbName, doc1))
	rcpsRead := runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txRead})
	getDataAbi, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"bytes"}]}]`))
	require.NoError(t, err)
	var got []byte
	require.NoError(t, getDataAbi.UnpackIntoInterface(&got, "getDataDocument", rcpsRead[0].Return()))
	require.Equal(t, []byte("parent_b1"), got)
}

// 4. Continuous High-Load Multi-Sender Block-STM Spam Stress Test:
// Spams 30 consecutive blocks (8 concurrent senders x 2 txs/block = 480 EVM + Xapian V1 txs)
// mixing newDocument, indexText, addTerm, addValue, setData, deleteDocument (including bogus
// short-hex docIds like 999), and BM25 querySearch under heavy Block-STM contention, plus a
// mid-stream crash rollback + re-execution and SyncOnly full-history batch replay check.
func TestXapianV1_ContinuousHighLoadSpam_MultiSenderBlockSTM_NoCrashAndFast(t *testing.T) {
	cs, adminKey, _, leader := setupXapianChainEnv(t)
	contractAddr := common.HexToAddress("0x1000000000000000000000000000000000000704")
	seedContract(t, cs, contractAddr, big.NewInt(0), buildStatefulXapianV1Forwarder())
	const dbName = "spam_stress_v1"

	const numSenders = 8
	senderKeys := make([]*ecdsa.PrivateKey, numSenders)
	senderNonces := make([]uint64, numSenders)
	fundingBalance := new(big.Int).Lsh(big.NewInt(1), 90)
	for s := 0; s < numSenders; s++ {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		senderKeys[s] = k
		senderNonces[s] = 1
		seedAccount(t, cs, crypto.PubkeyToAddress(k.PublicKey), new(big.Int).Set(fundingBalance), 1)
	}

	// Block 1: initialize DB + seed 4 baseline documents
	initTxs := []types.Transaction{
		signAndWrapCall(t, adminKey, 1, contractAddr, makeXapianV1GetOrCreateDbData(t, dbName)),
	}
	for i := 0; i < 4; i++ {
		initTxs = append(initTxs, signAndWrapCall(t, adminKey, uint64(2+i), contractAddr,
			makeXapianV1NewDocData(t, dbName, []byte(fmt.Sprintf("seed_%d", i)))))
	}
	rcpsInit := runTxBatchOnChainState(t, cs, contractAddr, leader, initTxs)
	var liveDocIds []*big.Int
	for i := 1; i <= 4; i++ {
		liveDocIds = append(liveDocIds, new(big.Int).SetBytes(rcpsInit[i].Return()))
	}
	allBlockLogs := make(map[uint64]map[string][]byte)
	allBlockLogs[1] = commitTxsToXapianAtBlock(initTxs, 1)

	const totalBlocks = 30
	startWall := time.Now()
	totalTxs := len(initTxs)

	for bn := uint64(2); bn <= totalBlocks; bn++ {
		var blockTxs []types.Transaction
		var newDocSlotIndices []int

		for s := 0; s < numSenders; s++ {
			// Tx A per sender
			n1 := senderNonces[s]
			senderNonces[s]++
			switch (int(bn) + s) % 4 {
			case 0:
				newDocSlotIndices = append(newDocSlotIndices, len(blockTxs))
				blockTxs = append(blockTxs, signAndWrapCall(t, senderKeys[s], n1, contractAddr,
					makeXapianV1NewDocData(t, dbName, []byte(fmt.Sprintf("spam_b%d_s%d", bn, s)))))
			case 1:
				target := liveDocIds[(int(bn)+s)%len(liveDocIds)]
				blockTxs = append(blockTxs, signAndWrapCall(t, senderKeys[s], n1, contractAddr,
					makeXapianV1IndexTextData(t, dbName, target, fmt.Sprintf("spam keyword consensus block %d sender %d", bn, s), 1, "S")))
			case 2:
				target := liveDocIds[(int(bn)+s)%len(liveDocIds)]
				blockTxs = append(blockTxs, signAndWrapCall(t, senderKeys[s], n1, contractAddr,
					makeXapianV1SetDataData(t, dbName, target, []byte(fmt.Sprintf("updated_b%d_s%d", bn, s)))))
			default:
				// Hostile delete of non-existent short-hex docId (999) — must NEVER crash!
				blockTxs = append(blockTxs, signAndWrapCall(t, senderKeys[s], n1, contractAddr,
					makeXapianV1DeleteDocData(t, dbName, big.NewInt(999+int64(s)))))
			}

			// Tx B per sender: concurrent hot-term indexing + BM25 search inside Block-STM
			n2 := senderNonces[s]
			senderNonces[s]++
			if s%2 == 0 {
				target := liveDocIds[s%len(liveDocIds)]
				blockTxs = append(blockTxs, signAndWrapCall(t, senderKeys[s], n2, contractAddr,
					makeXapianV1AddTermData(t, dbName, target, "Sspam")))
			} else {
				blockTxs = append(blockTxs, signAndWrapCall(t, senderKeys[s], n2, contractAddr,
					makeXapianV1QuerySearchData(t, dbName, "spam OR consensus", "S")))
			}
		}

		rcps := runTxBatchOnChainState(t, cs, contractAddr, leader, blockTxs)
		for _, idx := range newDocSlotIndices {
			docId := new(big.Int).SetBytes(rcps[idx].Return())
			if docId.Sign() > 0 {
				liveDocIds = append(liveDocIds, docId)
			}
		}
		allBlockLogs[bn] = commitTxsToXapianAtBlock(blockTxs, bn)
		totalTxs += len(blockTxs)

		// At block 20, simulate an unexpected crash & rollback to block 19 followed by idempotent replay!
		if bn == 20 {
			mvm.DestroyAllXapianInstances()
			rolled, rbErr := mvm.RollbackXapianAboveBlock(19)
			require.NoError(t, rbErr)
			require.Equal(t, 1, rolled)
			require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{allBlockLogs[20]}, 20))
			mvm.CommitAllXapianAtBlock(20)
		}
	}

	elapsed := time.Since(startWall)
	t.Logf("[GO BLOCK-STM XAPIAN SPAM] %d blocks, %d EVM+Xapian txs completed in %v (%.2f ms/block, %.0f tx/s)",
		totalBlocks, totalTxs, elapsed, float64(elapsed.Microseconds())/1000.0/float64(totalBlocks),
		float64(totalTxs)/elapsed.Seconds())

	// Query final BM25 search results on Validator
	valSearchTx := signAndWrapCall(t, adminKey, 6, contractAddr, makeXapianV1QuerySearchData(t, dbName, "spam", "S"))
	valRcps := runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{valSearchTx})
	valTotal, valItems := unpackXapianV1QuerySearchReturn(t, valRcps[0].Return())
	require.Greater(t, valTotal.Int64(), int64(0))

	// Spin up a fresh SyncOnly Xapian dir, replay all 30 blocks (replaying block 30 twice to test retry idempotency),
	// and verify 100% identical BM25 search results!
	syncDir := t.TempDir()
	mvm.DestroyAllXapianInstances()
	mvm.ClearAllMVMApi()
	mvm.CallClearAllStateInstances()
	mvm.ConfigureXapianBasePath(syncDir)
	for bn := uint64(1); bn <= totalBlocks; bn++ {
		if len(allBlockLogs[bn]) > 0 {
			require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{allBlockLogs[bn]}, bn))
			mvm.CommitAllXapianAtBlock(bn)
		}
	}
	// Duplicate delivery of block 30 on SyncOnly
	require.Equal(t, 1, mvm.CallReplayFullDbLogsBatchAtBlock([]map[string][]byte{allBlockLogs[totalBlocks]}, totalBlocks))
	mvm.CommitAllXapianAtBlock(totalBlocks)

	csSync := newTestChainState(t)
	csSync.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	adminAddr := crypto.PubkeyToAddress(adminKey.PublicKey)
	seedAccount(t, csSync, adminAddr, big.NewInt(1000000000000000000), 1)
	seedAccount(t, csSync, leader, big.NewInt(0), 0)
	seedContract(t, csSync, contractAddr, big.NewInt(0), buildStatefulXapianV1Forwarder())

	syncSearchTx := signAndWrapCall(t, adminKey, 1, contractAddr, makeXapianV1QuerySearchData(t, dbName, "spam", "S"))
	syncRcps := runTxBatchOnChainState(t, csSync, contractAddr, leader, []types.Transaction{syncSearchTx})
	syncTotal, syncItems := unpackXapianV1QuerySearchReturn(t, syncRcps[0].Return())
	require.Equal(t, valTotal.String(), syncTotal.String())
	require.Equal(t, valItems, syncItems)
}

// 5. Genesis Rollback Fail-Close (Pruned Floor > 0) + Physical DocId Non-Overwrite + Sorted NID Tie-Break Test:
// Verifies at the Go EVM/CGO boundary that:
//   - Deleting docId=2 (0x0000...0002) never deletes physical docid 2 (docB).
//   - RollbackXapianAboveBlock(0) succeeds when unpruned (<= 64 blocks) via Undo Journal,
//     and fails closed (returns error without wiping on-disk DB) once > 64 blocks are committed.
func TestXapianV1_GenesisRollbackFailCloseWhenPruned_And_PhysicalDoc2Protected(t *testing.T) {
	t.Cleanup(func() {
		mvm.DestroyAllXapianInstances()
	})

	xapianDir := t.TempDir()
	mvm.DestroyAllXapianInstances()
	mvm.ClearAllMVMApi()
	mvm.CallClearAllStateInstances()
	mvm.ConfigureXapianBasePath(xapianDir)

	keyHex := "4f3edf983ac636a65a842ce7c78d9aa706d3b113bce9c46f30d7d21715b23b1d"
	key, err := crypto.HexToECDSA(keyHex)
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	contractAddr := common.HexToAddress("0x1000000000000000000000000000000000000705")
	const dbName = "genesis_guard_v1"

	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	funding := new(big.Int).Lsh(big.NewInt(1), 90)
	seedAccount(t, cs, sender, funding, 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedContract(t, cs, contractAddr, big.NewInt(0), buildStatefulXapianV1Forwarder())

	// Block 1: create 3 docs -> while unpruned, RollbackXapianAboveBlock(0) must succeed cleanly!
	nonce := uint64(1)
	nextNonce := func() uint64 {
		n := nonce
		nonce++
		return n
	}
	txInit := signAndWrapCall(t, key, nextNonce(), contractAddr, makeXapianV1GetOrCreateDbData(t, dbName))
	txPre := signAndWrapCall(t, key, nextNonce(), contractAddr, makeXapianV1NewDocData(t, dbName, []byte("unpruned_b1")))
	runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txInit, txPre})
	commitTxsToXapianAtBlock([]types.Transaction{txInit, txPre}, 1)

	rolledZero, errZero := mvm.RollbackXapianAboveBlock(0)
	require.NoError(t, errZero)
	require.Equal(t, 1, rolledZero)

	// Re-seed 3 docs at Block 1 (occupying physical docids 1, 2, 3 in sorted nid order)
	t1 := signAndWrapCall(t, key, nextNonce(), contractAddr, makeXapianV1NewDocData(t, dbName, []byte("doc_one")))
	t2 := signAndWrapCall(t, key, nextNonce(), contractAddr, makeXapianV1NewDocData(t, dbName, []byte("doc_two")))
	t3 := signAndWrapCall(t, key, nextNonce(), contractAddr, makeXapianV1NewDocData(t, dbName, []byte("doc_three")))
	rcpsSeed := runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{t1, t2, t3})
	d1 := new(big.Int).SetBytes(rcpsSeed[0].Return())
	d2 := new(big.Int).SetBytes(rcpsSeed[1].Return())
	d3 := new(big.Int).SetBytes(rcpsSeed[2].Return())
	commitTxsToXapianAtBlock([]types.Transaction{t1, t2, t3}, 1)

	// Delete numeric docId = 2 (0x0000...0002) at Block 2 -> must NOT delete any of d1, d2, d3!
	txDel2 := signAndWrapCall(t, key, nextNonce(), contractAddr, makeXapianV1DeleteDocData(t, dbName, big.NewInt(2)))
	runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txDel2})
	commitTxsToXapianAtBlock([]types.Transaction{txDel2}, 2)

	getDataAbi, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"bytes"}]}]`))
	require.NoError(t, err)
	for idx, want := range []struct {
		id   *big.Int
		data string
	}{{d1, "doc_one"}, {d2, "doc_two"}, {d3, "doc_three"}} {
		txGet := signAndWrapCall(t, key, nextNonce(), contractAddr, makeXapianV1GetDataData(t, dbName, want.id))
		rcp := runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txGet})
		var got []byte
		require.NoError(t, getDataAbi.UnpackIntoInterface(&got, "getDataDocument", rcp[0].Return()))
		require.Equal(t, []byte(want.data), got, "doc %d must never be deleted by deleteDocument(2)", idx)
	}

	// Advance past MAX_UNDO_BLOCKS (64) up to Block 70 -> pruned_floor > 0
	for bn := uint64(3); bn <= 70; bn++ {
		txUpd := signAndWrapCall(t, key, nextNonce(), contractAddr,
			makeXapianV1SetDataData(t, dbName, d1, []byte(fmt.Sprintf("d1_b%d", bn))))
		runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txUpd})
		commitTxsToXapianAtBlock([]types.Transaction{txUpd}, bn)
	}

	// RollbackXapianAboveBlock(0) MUST fail closed (error != nil) and MUST NOT wipe the DB!
	_, errPrunedZero := mvm.RollbackXapianAboveBlock(0)
	require.Error(t, errPrunedZero, "rollback to block 0 when undo floor is pruned must fail closed")

	mvm.DestroyAllXapianInstances()
	_, errColdZero := mvm.RollbackXapianAboveBlock(0)
	require.Error(t, errColdZero, "cold rollback to block 0 when undo floor is pruned must also fail closed")

	// Verify d1 at Block 70 is STILL intact on disk (never wiped by remove_all!)
	txVerify := signAndWrapCall(t, key, nextNonce(), contractAddr, makeXapianV1GetDataData(t, dbName, d1))
	rcpVerify := runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txVerify})
	var gotFinal []byte
	require.NoError(t, getDataAbi.UnpackIntoInterface(&gotFinal, "getDataDocument", rcpVerify[0].Return()))
	require.Equal(t, []byte("d1_b70"), gotFinal)
}

func runSigKillScenarioBlocks1And2(
	t *testing.T,
	cs *blockchain.ChainState,
	key *ecdsa.PrivateKey,
	contractAddr, leader common.Address,
	dbName string,
) (*big.Int, *big.Int, *big.Int, *big.Int, uint64) {
	nonce := uint64(1)
	next := func() uint64 {
		n := nonce
		nonce++
		return n
	}

	// Block 1: create docA, docB, docC + index + values
	txInit := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1GetOrCreateDbData(t, dbName))
	txNewA := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1NewDocData(t, dbName, []byte("sigkill_docA_b1")))
	txNewB := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1NewDocData(t, dbName, []byte("sigkill_docB_b1")))
	txNewC := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1NewDocData(t, dbName, []byte("sigkill_docC_b1")))
	rcp1a := runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txInit, txNewA, txNewB, txNewC})
	docA := new(big.Int).SetBytes(rcp1a[1].Return())
	docB := new(big.Int).SetBytes(rcp1a[2].Return())
	docC := new(big.Int).SetBytes(rcp1a[3].Return())

	txIdxA := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1IndexTextData(t, dbName, docA, "consensus sigkill alpha", 1, "S"))
	txValA := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1AddValueData(t, dbName, docA, big.NewInt(7), "v_b1", false))
	txIdxB := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1IndexTextData(t, dbName, docB, "consensus sigkill bravo", 1, "S"))
	txIdxC := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1IndexTextData(t, dbName, docC, "consensus sigkill charlie", 1, "S"))
	runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txIdxA, txValA, txIdxB, txIdxC})
	commitTxsToXapianAtBlock([]types.Transaction{txInit, txNewA, txNewB, txNewC, txIdxA, txValA, txIdxB, txIdxC}, 1)

	// Block 2: update docA, delete docB, create docD + index
	txUpdA2 := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1SetDataData(t, dbName, docA, []byte("sigkill_docA_b2")))
	txValA2 := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1AddValueData(t, dbName, docA, big.NewInt(7), "v_b2", false))
	txDelB2 := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1DeleteDocData(t, dbName, docB))
	txNewD2 := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1NewDocData(t, dbName, []byte("sigkill_docD_b2")))
	rcp2a := runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txUpdA2, txValA2, txDelB2, txNewD2})
	docD := new(big.Int).SetBytes(rcp2a[3].Return())

	txIdxD2 := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1IndexTextData(t, dbName, docD, "consensus sigkill delta", 1, "S"))
	runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txIdxD2})
	commitTxsToXapianAtBlock([]types.Transaction{txUpdA2, txValA2, txDelB2, txNewD2, txIdxD2}, 2)

	return docA, docB, docC, docD, nonce
}

func runSigKillScenarioBlocks3And4(
	t *testing.T,
	cs *blockchain.ChainState,
	key *ecdsa.PrivateKey,
	contractAddr, leader common.Address,
	dbName string,
	docA, docC, docD *big.Int,
	startNonce uint64,
) ([]types.Receipt, map[string][]byte) {
	nonce := startNonce
	next := func() uint64 {
		n := nonce
		nonce++
		return n
	}

	// Block 3: read pre-state into EVM storage, mutate docA, delete docC, create docE + index
	b3Pre := []types.Transaction{
		signAndWrapCall(t, key, next(), contractAddr, makeXapianV1GetDataData(t, dbName, docA)),
		signAndWrapCall(t, key, next(), contractAddr, makeXapianV1GetValueData(t, dbName, docA, big.NewInt(7), false)),
		signAndWrapCall(t, key, next(), contractAddr, makeXapianV1QuerySearchData(t, dbName, "consensus", "S")),
		signAndWrapCall(t, key, next(), contractAddr, makeXapianV1SetDataData(t, dbName, docA, []byte("sigkill_docA_b3"))),
		signAndWrapCall(t, key, next(), contractAddr, makeXapianV1DeleteDocData(t, dbName, docC)),
		signAndWrapCall(t, key, next(), contractAddr, makeXapianV1NewDocData(t, dbName, []byte("sigkill_docE_b3"))),
	}
	rcp3a := runTxBatchOnChainState(t, cs, contractAddr, leader, b3Pre)
	docE := new(big.Int).SetBytes(rcp3a[5].Return())
	txIdxE := signAndWrapCall(t, key, next(), contractAddr, makeXapianV1IndexTextData(t, dbName, docE, "consensus sigkill echo", 1, "S"))
	rcp3b := runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txIdxE})
	commitTxsToXapianAtBlock(append(b3Pre, txIdxE), 3)

	// Block 4: update docD, boost docA BM25 term frequency, run final consensus search
	b4 := []types.Transaction{
		signAndWrapCall(t, key, next(), contractAddr, makeXapianV1SetDataData(t, dbName, docD, []byte("sigkill_docD_b4"))),
		signAndWrapCall(t, key, next(), contractAddr, makeXapianV1IndexTextData(t, dbName, docA, "consensus consensus boost", 1, "S")),
		signAndWrapCall(t, key, next(), contractAddr, makeXapianV1QuerySearchData(t, dbName, "consensus", "S")),
	}
	rcp4 := runTxBatchOnChainState(t, cs, contractAddr, leader, b4)
	logs4 := commitTxsToXapianAtBlock(b4, 4)

	allRcps := make([]types.Receipt, 0, len(rcp3a)+len(rcp3b)+len(rcp4))
	allRcps = append(allRcps, rcp3a...)
	allRcps = append(allRcps, rcp3b...)
	allRcps = append(allRcps, rcp4...)
	return allRcps, logs4
}

// Child subprocess helper for real OS SIGKILL (kill -9) crash testing.
func TestHelperProcess_XapianSigKillChild(t *testing.T) {
	if os.Getenv("MVM_TEST_XAPIAN_SIGKILL_CHILD") != "1" {
		return
	}
	crashDir := os.Getenv("MVM_TEST_XAPIAN_SIGKILL_DIR")
	require.NotEmpty(t, crashDir)

	keyHex := "4f3edf983ac636a65a842ce7c78d9aa706d3b113bce9c46f30d7d21715b23b1d"
	key, err := crypto.HexToECDSA(keyHex)
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	contractAddr := common.HexToAddress("0x1000000000000000000000000000000000000706")
	const dbName = "sigkill_guard_v1"

	mvm.ConfigureXapianBasePath(crashDir)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	funding := new(big.Int).Lsh(big.NewInt(1), 90)
	seedAccount(t, cs, sender, funding, 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedContract(t, cs, contractAddr, big.NewInt(0), buildStatefulXapianV1Forwarder())

	docA, _, docC, docD, nonce := runSigKillScenarioBlocks1And2(t, cs, key, contractAddr, leader, dbName)

	// Commit Block 3 to Xapian on disk (ahead of BlockDB which only persisted Block 2!)
	b3Pre := []types.Transaction{
		signAndWrapCall(t, key, nonce, contractAddr, makeXapianV1GetDataData(t, dbName, docA)),
		signAndWrapCall(t, key, nonce+1, contractAddr, makeXapianV1GetValueData(t, dbName, docA, big.NewInt(7), false)),
		signAndWrapCall(t, key, nonce+2, contractAddr, makeXapianV1QuerySearchData(t, dbName, "consensus", "S")),
		signAndWrapCall(t, key, nonce+3, contractAddr, makeXapianV1SetDataData(t, dbName, docA, []byte("sigkill_docA_b3_aborted"))),
		signAndWrapCall(t, key, nonce+4, contractAddr, makeXapianV1DeleteDocData(t, dbName, docC)),
		signAndWrapCall(t, key, nonce+5, contractAddr, makeXapianV1NewDocData(t, dbName, []byte("sigkill_docE_b3_aborted"))),
	}
	rcp3 := runTxBatchOnChainState(t, cs, contractAddr, leader, b3Pre)
	docE := new(big.Int).SetBytes(rcp3[5].Return())
	txIdxE := signAndWrapCall(t, key, nonce+6, contractAddr, makeXapianV1IndexTextData(t, dbName, docE, "consensus sigkill echo", 1, "S"))
	runTxBatchOnChainState(t, cs, contractAddr, leader, []types.Transaction{txIdxE})
	commitTxsToXapianAtBlock(append(b3Pre, txIdxE), 3)

	// Now start Block 4 MID-EXECUTION: stage tx buffers + comprehensive_log WITHOUT committing Xapian,
	// while keeping WritableDatabase (.flintlock) open in C++ memory with zero cleanup!
	midBlockTxs := []types.Transaction{
		signAndWrapCall(t, key, nonce+7, contractAddr, makeXapianV1SetDataData(t, dbName, docD, []byte("half_baked_mid_block4_dirty"))),
		signAndWrapCall(t, key, nonce+8, contractAddr, makeXapianV1NewDocData(t, dbName, []byte("orphan_mid_block4_doc"))),
	}
	runTxBatchOnChainState(t, cs, contractAddr, leader, midBlockTxs)
	midHashes := [][]byte{midBlockTxs[0].Hash().Bytes(), midBlockTxs[1].Hash().Bytes()}
	mvm.CommitXapianTxBufferBatch(midHashes)

	// Signal parent to deliver real kernel SIGKILL (kill -9) immediately!
	_, _ = fmt.Fprintln(os.Stdout, "XAPIAN_CHILD_READY_FOR_SIGKILL")
	_ = os.Stdout.Sync()

	// Block forever so C++ destructors / DestroyAllXapianInstances NEVER run!
	select {}
}

// 5. Real OS SIGKILL (kill -9) Mid-Block Subprocess Crash & Restart Convergence Test:
// Launches a real child OS process that commits Blocks 1..3 to Xapian, stages mid-flight Block 4
// operations while holding open Glass .flintlock, and gets killed abruptly via kernel SIGKILL (kill -9).
// Verifies that the parent process reclaims .flintlock on cold startup, rolls back Block 3 -> Block 2
// via XUJ1, and re-executes Blocks 3..4 to 100% byte-for-byte convergence with a clean node.
func TestXapianV1_RealSigKillMidBlockSubprocess_RecoverAndConverge(t *testing.T) {
	t.Cleanup(func() {
		mvm.DestroyAllXapianInstances()
	})

	keyHex := "4f3edf983ac636a65a842ce7c78d9aa706d3b113bce9c46f30d7d21715b23b1d"
	key, err := crypto.HexToECDSA(keyHex)
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	contractAddr := common.HexToAddress("0x1000000000000000000000000000000000000706")
	const dbName = "sigkill_guard_v1"

	// 1. Spawn child subprocess and wait until it holds open .flintlock mid-block
	crashDir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess_XapianSigKillChild$")
	cmd.Env = append(os.Environ(),
		"MVM_TEST_XAPIAN_SIGKILL_CHILD=1",
		"MVM_TEST_XAPIAN_SIGKILL_DIR="+crashDir,
	)
	stdoutPipe, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())

	readyCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "XAPIAN_CHILD_READY_FOR_SIGKILL" {
				readyCh <- nil
				return
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			readyCh <- scanErr
		} else {
			readyCh <- fmt.Errorf("child exited before signaling XAPIAN_CHILD_READY_FOR_SIGKILL")
		}
	}()

	select {
	case readyErr := <-readyCh:
		require.NoError(t, readyErr)
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timed out waiting for child process to reach mid-block SIGKILL point")
	}

	// 2. Deliver real kernel SIGKILL (kill -9) to child PID!
	require.NoError(t, syscall.Kill(cmd.Process.Pid, syscall.SIGKILL))
	waitErr := cmd.Wait()
	require.Error(t, waitErr, "child must exit abnormally from SIGKILL")
	exitErr, ok := waitErr.(*exec.ExitError)
	require.True(t, ok, "expected *exec.ExitError, got %T", waitErr)
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	require.True(t, ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL,
		"expected child termination by SIGKILL, got status=%v", ws)

	// Confirm Glass flintlock file was left behind on disk by the SIGKILL'd process
	var flintlockMatches []string
	require.NoError(t, filepath.WalkDir(crashDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && d.Name() == "flintlock" {
			flintlockMatches = append(flintlockMatches, path)
		}
		return nil
	}))
	require.Len(t, flintlockMatches, 1, "flintlock file must exist on disk after SIGKILL")

	// 3. Build healthy Reference Node (Blocks 1..4 cleanly without crash)
	refDir := t.TempDir()
	mvm.DestroyAllXapianInstances()
	mvm.ClearAllMVMApi()
	mvm.CallClearAllStateInstances()
	mvm.ConfigureXapianBasePath(refDir)

	csRef := newTestChainState(t)
	csRef.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	funding := new(big.Int).Lsh(big.NewInt(1), 90)
	seedAccount(t, csRef, sender, funding, 1)
	seedAccount(t, csRef, leader, big.NewInt(0), 0)
	seedContract(t, csRef, contractAddr, big.NewInt(0), buildStatefulXapianV1Forwarder())

	refDocA, _, refDocC, refDocD, refNonce := runSigKillScenarioBlocks1And2(t, csRef, key, contractAddr, leader, dbName)
	refRcps34, refLogs4 := runSigKillScenarioBlocks3And4(t, csRef, key, contractAddr, leader, dbName, refDocA, refDocC, refDocD, refNonce)

	// 4. Simulate Crashed Node Restart at Block 2 (BlockDB persisted up to Block 2; crashDir has committed Block 3 + aborted Block 4 + stale flintlock)
	mvm.DestroyAllXapianInstances()
	mvm.ClearAllMVMApi()
	mvm.CallClearAllStateInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())

	csRecovered := newTestChainState(t)
	csRecovered.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	seedAccount(t, csRecovered, sender, funding, 1)
	seedAccount(t, csRecovered, leader, big.NewInt(0), 0)
	seedContract(t, csRecovered, contractAddr, big.NewInt(0), buildStatefulXapianV1Forwarder())
	recDocA, _, recDocC, recDocD, recNonce := runSigKillScenarioBlocks1And2(t, csRecovered, key, contractAddr, leader, dbName)

	// Point MVM to the SIGKILL'd crashDir and execute startup RollbackXapianAboveBlock(2)!
	mvm.DestroyAllXapianInstances()
	mvm.ClearAllMVMApi()
	mvm.CallClearAllStateInstances()
	mvm.ConfigureXapianBasePath(crashDir)

	rolledCount, rbErr := mvm.RollbackXapianAboveBlock(2)
	require.NoError(t, rbErr, "cold startup after kill -9 must reclaim flintlock and roll back cleanly")
	require.Equal(t, 1, rolledCount, "committed Block 3 in crashDir must be rolled back to Block 2")

	// 5. Re-execute Blocks 3 and 4 on recovered crashDir and assert 100% convergence with Reference Node!
	recRcps34, recLogs4 := runSigKillScenarioBlocks3And4(t, csRecovered, key, contractAddr, leader, dbName, recDocA, recDocC, recDocD, recNonce)
	require.Len(t, recRcps34, len(refRcps34))
	for i := range refRcps34 {
		require.Equal(t, refRcps34[i].Return(), recRcps34[i].Return(), "receipt[%d] diverged after kill -9 recovery", i)
	}
	require.Equal(t, refLogs4, recLogs4, "Block 4 exported FullDbLogs must match byte-for-byte after kill -9 recovery")

	for slot := 0; slot <= 25; slot++ {
		var slotKey common.Hash
		big.NewInt(int64(slot)).FillBytes(slotKey[:])
		wantVal, _ := csRef.GetSmartContractDB().StorageValue(contractAddr, slotKey.Bytes())
		gotVal, _ := csRecovered.GetSmartContractDB().StorageValue(contractAddr, slotKey.Bytes())
		require.Equal(t, wantVal, gotVal, "EVM storage slot %d diverged after kill -9 recovery", slot)
	}

	refTotal, refItems := unpackXapianV1QuerySearchReturn(t, refRcps34[len(refRcps34)-1].Return())
	recTotal, recItems := unpackXapianV1QuerySearchReturn(t, recRcps34[len(recRcps34)-1].Return())
	require.Equal(t, int64(3), refTotal.Int64(), "docA, docD, docE must remain live after Block 4")
	require.Equal(t, refTotal, recTotal)
	require.Equal(t, refItems, recItems, "BM25 rankings, physical docids, and payloads must match 100%% after kill -9 recovery")
}
