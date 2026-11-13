package tx_processor

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"testing"

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

// Edge-case tests for the Xapian x Block-STM interaction. Every test commits the Xapian buffer of EVERY
// transaction unconditionally (like the pre-fix block processor effectively did for -1 receipts), so a
// failed / rejected / skipped transaction leaves a trace in ExportAllXapianLogs only if TrueBlockSTM
// itself failed to discard its buffer.

func edgeKey(n byte) *ecdsa.PrivateKey {
	b := make([]byte, 32)
	b[31] = n
	b[0] = 0x11
	k, err := crypto.ToECDSA(b)
	if err != nil {
		panic(err)
	}
	return k
}

func edgeEnv(t *testing.T) (*blockchain.ChainState, common.Address) {
	mvm.DestroyAllXapianInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())
	t.Cleanup(mvm.DestroyAllXapianInstances)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	leader := common.HexToAddress(auditLeader)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	return cs, leader
}

// xapianWriterCode returns bytecode that writes one Xapian document into dbname and then runs `tail`.
func xapianWriterCode(t *testing.T, dbname string, tail ...any) []byte {
	return confScript(confJoin(
		callXapianOp(0, makeXapianNewDocData(t, dbname, "payload-"+dbname)),
		tail,
	)...)
}

func edgeTx(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to common.Address, gas uint64, gasPrice int64) types.Transaction {
	signed, err := eth.SignNewTx(key, eth.LatestSignerForChainID(big.NewInt(991)), &eth.LegacyTx{
		Nonce: nonce, To: &to, Gas: gas, GasPrice: big.NewInt(gasPrice), Value: big.NewInt(0),
	})
	require.NoError(t, err)
	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)
	return tx
}

func edgeRun(t *testing.T, cs *blockchain.ChainState, leader common.Address, txs ...types.Transaction) []types.Receipt {
	items := make([]grouptxns.Item, len(txs))
	for i, tx := range txs {
		items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}
	}
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs,
		grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	return r
}

func commitAll(txs ...types.Transaction) map[string][]byte {
	hashes := make([][]byte, len(txs))
	for i, tx := range txs {
		hashes[i] = tx.Hash().Bytes()
	}
	mvm.CommitXapianTxBufferBatch(hashes)
	return mvm.ExportAllXapianLogs()
}

// A transaction that writes to Xapian and then halts exceptionally (INVALID opcode) must leave nothing.
func TestXapianEdge_ExceptionalHaltLeavesNoWrites(t *testing.T) {
	cs, leader := edgeEnv(t)
	key := edgeKey(1)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	seedAccount(t, cs, sender, big.NewInt(1000000000000000), 1)
	target := common.HexToAddress("0x1000000000000000000000000000000000000a01")
	seedContract(t, cs, target, big.NewInt(0), xapianWriterCode(t, "halt_db", "INVALID"))

	tx := edgeTx(t, key, 1, target, 3000000, 100000)
	r := edgeRun(t, cs, leader, tx)
	require.Len(t, r, 1)
	require.NotEqual(t, pb.RECEIPT_STATUS_RETURNED, r[0].Status())
	require.Empty(t, commitAll(tx), "halted tx must not leave Xapian writes")
}

// Out of gas while calling the Xapian precompile (tiny gas limit) must leave nothing.
func TestXapianEdge_OutOfGasLeavesNoWrites(t *testing.T) {
	cs, leader := edgeEnv(t)
	key := edgeKey(2)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	seedAccount(t, cs, sender, big.NewInt(1000000000000000), 1)
	target := common.HexToAddress("0x1000000000000000000000000000000000000a02")
	seedContract(t, cs, target, big.NewInt(0), xapianWriterCode(t, "oog_db", 0, 0, "RETURN"))

	tx := edgeTx(t, key, 1, target, 22000, 100000)
	r := edgeRun(t, cs, leader, tx)
	require.Len(t, r, 1)
	t.Logf("OOG receipt status=%d return=%q", r[0].Status(), string(r[0].Return()))
	if r[0].Status() == pb.RECEIPT_STATUS_RETURNED {
		t.Skip("tx unexpectedly succeeded with 22000 gas; cannot assert OOG")
	}
	require.Empty(t, commitAll(tx), "out-of-gas tx must not leave Xapian writes")
}

// Block with one funded sender and one unfunded sender writing to two DIFFERENT contracts' databases:
// only the funded sender's contract may appear in the exported logs.
func TestXapianEdge_MixedFundedAndUnfundedSenders(t *testing.T) {
	cs, leader := edgeEnv(t)
	okKey, poorKey := edgeKey(3), edgeKey(4)
	okAddr, poorAddr := crypto.PubkeyToAddress(okKey.PublicKey), crypto.PubkeyToAddress(poorKey.PublicKey)
	seedAccount(t, cs, okAddr, big.NewInt(1000000000000000), 1)
	seedAccount(t, cs, poorAddr, big.NewInt(1000), 1)

	okTarget := common.HexToAddress("0x1000000000000000000000000000000000000a03")
	poorTarget := common.HexToAddress("0x1000000000000000000000000000000000000a04")
	seedContract(t, cs, okTarget, big.NewInt(0), xapianWriterCode(t, "ok_db", 0, 0, "RETURN"))
	seedContract(t, cs, poorTarget, big.NewInt(0), xapianWriterCode(t, "poor_db", 0, 0, "RETURN"))

	txOK := edgeTx(t, okKey, 1, okTarget, 3000000, 100000)
	txPoor := edgeTx(t, poorKey, 1, poorTarget, 3000000, 100000)
	r := edgeRun(t, cs, leader, txOK, txPoor)
	require.Len(t, r, 2)
	statusByHash := map[common.Hash]pb.RECEIPT_STATUS{}
	for _, x := range r {
		statusByHash[x.TransactionHash()] = x.Status()
	}
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, statusByHash[txOK.Hash()])
	require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, statusByHash[txPoor.Hash()])

	logs := commitAll(txOK, txPoor)
	require.Contains(t, logs, okTarget.Hex(), "accepted tx must be committed")
	require.NotContains(t, logs, poorTarget.Hex(), "rejected tx must not be committed")
}

// Same sender, 3 sequential txs; the 3rd cannot afford gas after the first two. Only the first two persist.
func TestXapianEdge_SameSenderBalanceExhaustedMidBlock(t *testing.T) {
	cs, leader := edgeEnv(t)
	key := edgeKey(5)
	sender := crypto.PubkeyToAddress(key.PublicKey)

	t1 := common.HexToAddress("0x1000000000000000000000000000000000000a05")
	t2 := common.HexToAddress("0x1000000000000000000000000000000000000a06")
	t3 := common.HexToAddress("0x1000000000000000000000000000000000000a07")
	seedContract(t, cs, t1, big.NewInt(0), xapianWriterCode(t, "m1_db", 0, 0, "RETURN"))
	seedContract(t, cs, t2, big.NewInt(0), xapianWriterCode(t, "m2_db", 0, 0, "RETURN"))
	seedContract(t, cs, t3, big.NewInt(0), xapianWriterCode(t, "m3_db", 0, 0, "RETURN"))

	// Measure the real gas cost of one run to size the balance precisely.
	probeCS, probeLeader := edgeEnv(t)
	seedAccount(t, probeCS, sender, big.NewInt(1000000000000000), 1)
	seedContract(t, probeCS, t1, big.NewInt(0), xapianWriterCode(t, "m1_db", 0, 0, "RETURN"))
	probeTx := edgeTx(t, key, 1, t1, 3000000, 100000)
	pr := edgeRun(t, probeCS, probeLeader, probeTx)
	require.Len(t, pr, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, pr[0].Status())
	oneRun := new(big.Int).Mul(new(big.Int).SetUint64(pr[0].GasUsed()), big.NewInt(100000))

	// fresh env for the real run
	cs, leader = edgeEnv(t)
	seedContract(t, cs, t1, big.NewInt(0), xapianWriterCode(t, "m1_db", 0, 0, "RETURN"))
	seedContract(t, cs, t2, big.NewInt(0), xapianWriterCode(t, "m2_db", 0, 0, "RETURN"))
	seedContract(t, cs, t3, big.NewInt(0), xapianWriterCode(t, "m3_db", 0, 0, "RETURN"))
	// enough for 2 full runs (plus slack smaller than a 3rd run)
	bal := new(big.Int).Mul(oneRun, big.NewInt(2))
	bal.Add(bal, new(big.Int).Div(oneRun, big.NewInt(2)))
	seedAccount(t, cs, sender, bal, 1)

	tx1 := edgeTx(t, key, 1, t1, 3000000, 100000)
	tx2 := edgeTx(t, key, 2, t2, 3000000, 100000)
	tx3 := edgeTx(t, key, 3, t3, 3000000, 100000)
	r := edgeRun(t, cs, leader, tx1, tx2, tx3)
	require.Len(t, r, 3)
	st := map[common.Hash]pb.RECEIPT_STATUS{}
	for _, x := range r {
		st[x.TransactionHash()] = x.Status()
	}
	t.Logf("statuses: tx1=%d tx2=%d tx3=%d", st[tx1.Hash()], st[tx2.Hash()], st[tx3.Hash()])
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, st[tx1.Hash()])
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, st[tx2.Hash()])
	require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, st[tx3.Hash()], "3rd tx must be gas-rejected")

	logs := commitAll(tx1, tx2, tx3)
	require.Contains(t, logs, t1.Hex())
	require.Contains(t, logs, t2.Hex())
	require.NotContains(t, logs, t3.Hex(), "gas-rejected 3rd tx must leave no Xapian write")
}

// Duplicate-nonce transactions: the one that loses must never leave a speculative Xapian write, regardless
// of how the parallel scheduler interleaves incarnations. Repeated to shake out scheduling races.
func TestXapianEdge_DuplicateNonceStress(t *testing.T) {
	const dups = 6
	const rounds = 25
	for round := 0; round < rounds; round++ {
		cs, leader := edgeEnv(t)
		key := edgeKey(6)
		sender := crypto.PubkeyToAddress(key.PublicKey)
		seedAccount(t, cs, sender, big.NewInt(1000000000000000), 1)

		noop := common.HexToAddress("0x1000000000000000000000000000000000000b00")
		seedContract(t, cs, noop, big.NewInt(0), confScript([]any{0, 0, "RETURN"}...))

		win := edgeTx(t, key, 1, noop, 100000, 100000)
		items := []grouptxns.Item{{ID: 0, Tx: win}}
		var losers []types.Transaction
		var loserAddrs []common.Address
		for i := 0; i < dups; i++ {
			tgt := common.HexToAddress(fmt.Sprintf("0x1000000000000000000000000000000000000b%02x", i+1))
			seedContract(t, cs, tgt, big.NewInt(0), xapianWriterCode(t, fmt.Sprintf("dup%d_db", i), 0, 0, "RETURN"))
			l := edgeTx(t, key, 1, tgt, 3000000, int64(100000+i+1))
			losers = append(losers, l)
			loserAddrs = append(loserAddrs, tgt)
			items = append(items, grouptxns.Item{ID: i + 1, Tx: l})
		}
		groups := []grouptxns.RelativeGroup{{Kind: grouptxns.GroupKindContractOnly, Items: items}}
		_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
		require.NotEmpty(t, r)
		successes := 0
		for _, x := range r {
			if x.Status() == pb.RECEIPT_STATUS_RETURNED {
				successes++
			}
		}
		require.Equal(t, 1, successes, "round %d: exactly one tx per nonce may succeed", round)

		all := append([]types.Transaction{win}, losers...)
		logs := commitAll(all...)
		for i, a := range loserAddrs {
			require.NotContainsf(t, logs, a.Hex(), "round %d: loser %d leaked a Xapian write", round, i)
		}
	}
}

// Determinism: the same block executed twice from identical fresh state must export byte-identical logs,
// even though Block-STM schedules the parallel execution differently each time.
func TestXapianEdge_DeterministicLogsAcrossRuns(t *testing.T) {
	run := func() map[string][]byte {
		cs, leader := edgeEnv(t)
		var txs []types.Transaction
		for i := 0; i < 8; i++ {
			key := edgeKey(byte(20 + i))
			addr := crypto.PubkeyToAddress(key.PublicKey)
			seedAccount(t, cs, addr, big.NewInt(1000000000000000), 1)
			tgt := common.HexToAddress(fmt.Sprintf("0x1000000000000000000000000000000000000c%02x", i%3))
			if !cs.HasCode(tgt) {
				seedContract(t, cs, tgt, big.NewInt(0), xapianWriterCode(t, fmt.Sprintf("det%d_db", i%3), 0, 0, "RETURN"))
			}
			txs = append(txs, edgeTx(t, key, 1, tgt, 3000000, 100000))
		}
		r := edgeRun(t, cs, leader, txs...)
		require.Len(t, r, len(txs))
		for _, x := range r {
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, x.Status())
		}
		return commitAll(txs...)
	}
	a := run()
	for i := 0; i < 5; i++ {
		b := run()
		require.Equal(t, len(a), len(b))
		for k, v := range a {
			require.Truef(t, bytes.Equal(v, b[k]), "run %d: exported Xapian log for %s differs between runs", i, k)
		}
	}
}
