package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	mt_state "github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the rule that a plain native transfer must have ONE outcome no matter which pipeline executes it:
// the native fast path (blocks without EVM txs) or TrueBlockSTM. Which pipeline runs depends only on the other txs in
// the block, so any difference would make a user's result (or the leader's reward) depend on unrelated txs.

const parityChainID = 991

type parityResult struct {
	receipts   int
	statuses   []pb.RECEIPT_STATUS
	gasUsed    []uint64
	senderNon  uint64
	senderBal  *big.Int
	toBal      *big.Int
	leaderBal  *big.Int
	totalAfter *big.Int
}

func runParity(t *testing.T, useSTM bool, skipSig bool, senderBalance *big.Int, mk func(from, to common.Address) []mt_types.Transaction) (parityResult, *big.Int) {
	t.Helper()
	// Unsigned fixtures: use the explicit devnet bypass of the execution signature filter (never active on chain 991).
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "true")
	t.Setenv("METANODE_DEVNET", "true")
	sender := common.HexToAddress("0x1111111111111111111111111111111111111a")
	to := common.HexToAddress("0x2222222222222222222222222222222222222b")
	leader := common.HexToAddress("0xAAAA000000000000000000000000000000000001")

	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(parityChainID)})
	seedAccount(t, cs, sender, senderBalance, 0)
	seedAccount(t, cs, to, big.NewInt(1), 0)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	totalBefore := new(big.Int).Add(senderBalance, big.NewInt(1))

	txs := mk(sender, to)
	var rcps []mt_types.Receipt
	if useSTM {
		_, rcps, _, _ = NewTrueBlockSTM(txs).Process(context.Background(), cs, leader, blankHeader(), 999)
	} else {
		items := make([]grouptxns.Item, len(txs))
		for i, tx := range txs {
			items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}
		}
		groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
		_, rcps, _, _ = ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, skipSig)
	}
	var res parityResult
	res.receipts = len(rcps)
	for _, r := range rcps {
		res.statuses = append(res.statuses, r.Status())
		res.gasUsed = append(res.gasUsed, r.GasUsed())
	}
	bal := func(a common.Address) (*big.Int, uint64) {
		st, err := cs.GetAccountStateDB().AccountState(a)
		require.NoError(t, err)
		return st.TotalBalance(), st.Nonce()
	}
	res.senderBal, res.senderNon = bal(sender)
	res.toBal, _ = bal(to)
	res.leaderBal, _ = bal(leader)
	res.totalAfter = new(big.Int).Add(new(big.Int).Add(res.senderBal, res.toBal), res.leaderBal)
	return res, totalBefore
}

func parityTx(from, to common.Address, nonce uint64, amount int64, price uint64) mt_types.Transaction {
	return transaction.NewTransaction(from, to, big.NewInt(amount), 21000, price, 1000, nil, nil, common.Hash{}, common.Hash{}, nonce, parityChainID)
}

// A transfer that fails for lack of funds debits nobody, so it must reward nobody: the leader reward is derived from
// receipt gas, and a receipt that reports gas for a fee that was never charged would mint balance out of thin air.
func TestExecParity_FailedTransferDoesNotMintLeaderReward(t *testing.T) {
	const price = uint64(mt_common.MINIMUM_BASE_FEE)
	for _, useSTM := range []bool{false, true} {
		name := map[bool]string{false: "fast-path", true: "block-stm"}[useSTM]
		t.Run(name, func(t *testing.T) {
			res, before := runParity(t, useSTM, true, big.NewInt(100_000), func(a, b common.Address) []mt_types.Transaction {
				return []mt_types.Transaction{parityTx(a, b, 0, 10_000_000, price)} // amount far above the balance
			})
			require.Equal(t, 1, res.receipts)
			assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, res.statuses[0])
			assert.Equal(t, uint64(0), res.gasUsed[0], "a fee that was not charged must not be reported as gas used")
			assert.Equal(t, uint64(1), res.senderNon, "the failed tx still consumes the nonce")
			assert.Equal(t, 0, res.senderBal.Cmp(big.NewInt(100_000)), "sender must not be debited")
			assert.Equal(t, 0, res.leaderBal.Sign(), "leader must not be rewarded for an unpaid fee (got %s)", res.leaderBal)
			assert.Equal(t, 0, res.totalAfter.Cmp(before), "supply must be conserved: before %s after %s", before, res.totalAfter)
		})
	}
}

// After a failed transfer the sender's nonce has advanced, so the sender's NEXT tx is valid and must execute in both
// pipelines. The fast path used to drop it silently (no receipt) while TrueBlockSTM executed it.
func TestExecParity_FailedThenValidSameSender(t *testing.T) {
	// Price 1 keeps the fees small enough that tx #2 (amount 5 + fee 20000) fits the 100000 balance that tx #1 (amount
	// 10_000_000) cannot. The fee floor is bypassed by the devnet switches runParity sets.
	const price = uint64(1)
	mk := func(a, b common.Address) []mt_types.Transaction {
		return []mt_types.Transaction{parityTx(a, b, 0, 10_000_000, price), parityTx(a, b, 1, 5, price)}
	}
	fast, beforeF := runParity(t, false, true, big.NewInt(100_000), mk)
	stm, beforeS := runParity(t, true, true, big.NewInt(100_000), mk)

	for name, r := range map[string]parityResult{"fast-path": fast, "block-stm": stm} {
		require.Equal(t, 2, r.receipts, name)
		assert.Equal(t, []pb.RECEIPT_STATUS{pb.RECEIPT_STATUS_TRANSACTION_ERROR, pb.RECEIPT_STATUS_RETURNED}, r.statuses, name)
		assert.Equal(t, uint64(2), r.senderNon, name)
	}
	assert.Equal(t, 0, fast.senderBal.Cmp(stm.senderBal), "sender balance fast %s stm %s", fast.senderBal, stm.senderBal)
	assert.Equal(t, 0, fast.toBal.Cmp(stm.toBal), "recipient balance fast %s stm %s", fast.toBal, stm.toBal)
	assert.Equal(t, fast.gasUsed, stm.gasUsed, "receipt gas must not depend on the pipeline")
	assert.Equal(t, 0, fast.totalAfter.Cmp(beforeF), "fast-path supply must be conserved")
	assert.Equal(t, 0, stm.totalAfter.Cmp(beforeS), "block-stm supply must be conserved")
}

// The fast path used to re-run the mempool validator (ValidMaxFee etc.) whenever the node-local signature cache
// missed and skipSignatureVerify was false, but not when it was true. Call sites differ on that flag (speculative and
// sync pass true, replay and the fallback pass false), so the same block could be executed differently. The outcome
// must not depend on the flag. Here the balance covers the real fee + amount but not MaxGas*price + amount (the
// admission-only ValidMaxFee), with the filter bypassed so the cache stays cold.
func TestExecParity_FastPathIndependentOfSkipSignatureVerify(t *testing.T) {
	const price = uint64(mt_common.MINIMUM_BASE_FEE)
	// actual fee per tx for an existing recipient = TRANSFER_GAS_COST*price (20000*price); MaxFee would be 21000*price.
	// Fund enough for 2 txs' actual cost (2 * (20000*price + 5)) plus 500_000, so after tx #0 (nonce 0, fee-exempt at
	// admission), tx #1 (nonce 1, NOT fee-exempt at admission) has enough for the real execution cost (20000*price + 5)
	// but less than ValidMaxFee (21000*price + 5).
	oneTxActual := new(big.Int).Add(big.NewInt(5), new(big.Int).Mul(big.NewInt(int64(mt_common.TRANSFER_GAS_COST)), new(big.Int).SetUint64(price)))
	balance := new(big.Int).Mul(oneTxActual, big.NewInt(2))
	balance.Add(balance, big.NewInt(500_000))
	mk := func(a, b common.Address) []mt_types.Transaction {
		return []mt_types.Transaction{
			parityTx(a, b, 0, 5, price),
			parityTx(a, b, 1, 5, price),
		}
	}
	skipTrue, _ := runParity(t, false, true, balance, mk)
	skipFalse, _ := runParity(t, false, false, balance, mk)
	assert.Equal(t, skipTrue, skipFalse, "fast-path outcome must not depend on skipSignatureVerify")
	require.Equal(t, 2, skipFalse.receipts)
	assert.Equal(t, []pb.RECEIPT_STATUS{pb.RECEIPT_STATUS_RETURNED, pb.RECEIPT_STATUS_RETURNED}, skipFalse.statuses)
	assert.Equal(t, uint64(2), skipFalse.senderNon)
}

// A native blob transfer rejected by blobFeeOrReject (e.g. MaxFeePerBlobGas below blockBlobBaseFee) still produces an
// error receipt in the block and therefore must consume the sender's nonce in both pipelines so a subsequent tx from
// the same sender in the same block executes identically.
func TestExecParity_RejectedBlobTransferStillConsumesNonce(t *testing.T) {
	const price = uint64(1)
	mk := func(a, b common.Address) []mt_types.Transaction {
		underpricedBlobTx := transaction.TransactionFromProto(&pb.Transaction{
			Type:                3,
			FromAddress:         a.Bytes(),
			ToAddress:           b.Bytes(),
			Amount:              big.NewInt(10).Bytes(),
			MaxGas:              21000,
			MaxGasPrice:         price,
			GasFeeCap:           big.NewInt(int64(price)).Bytes(),
			GasTipCap:           big.NewInt(0).Bytes(),
			Nonce:               make([]byte, 8), // nonce 0
			ChainID:             parityChainID,
			BlobVersionedHashes: [][]byte{{0x01}},
			MaxFeePerBlobGas:    big.NewInt(0).Bytes(), // 0 < min blob base fee (1 wei)
		})
		return []mt_types.Transaction{
			underpricedBlobTx,
			parityTx(a, b, 1, 5, price),
		}
	}
	fast, beforeF := runParity(t, false, true, big.NewInt(100_000), mk)
	stm, beforeS := runParity(t, true, true, big.NewInt(100_000), mk)

	for name, r := range map[string]parityResult{"fast-path": fast, "block-stm": stm} {
		require.Equal(t, 2, r.receipts, name)
		assert.Equal(t, []pb.RECEIPT_STATUS{pb.RECEIPT_STATUS_TRANSACTION_ERROR, pb.RECEIPT_STATUS_RETURNED}, r.statuses, name)
		assert.Equal(t, uint64(0), r.gasUsed[0], "%s: rejected blob tx must report 0 gas used", name)
		assert.Equal(t, uint64(2), r.senderNon, "%s: rejected blob tx must still advance nonce so tx #2 executes", name)
	}
	assert.Equal(t, 0, fast.senderBal.Cmp(stm.senderBal), "sender balance fast %s stm %s", fast.senderBal, stm.senderBal)
	assert.Equal(t, 0, fast.toBal.Cmp(stm.toBal), "recipient balance fast %s stm %s", fast.toBal, stm.toBal)
	assert.Equal(t, fast.gasUsed, stm.gasUsed, "receipt gas must not depend on the pipeline")
	assert.Equal(t, 0, fast.totalAfter.Cmp(beforeF), "fast-path supply must be conserved")
	assert.Equal(t, 0, stm.totalAfter.Cmp(beforeS), "block-stm supply must be conserved")
}

func TestFeeFloorOK(t *testing.T) {
	a := common.HexToAddress("0x01")
	b := common.HexToAddress("0x02")
	freeAddr := common.HexToAddress("0x03")
	free := map[common.Address]struct{}{freeAddr: {}}
	min := uint64(mt_common.MINIMUM_BASE_FEE)

	assert.False(t, feeFloorOK(parityTx(a, b, 1, 1, 0), free), "zero price, nonce>0, not free: below the floor")
	assert.False(t, feeFloorOK(parityTx(a, b, 7, 1, min-1), free), "one below the floor")
	assert.True(t, feeFloorOK(parityTx(a, b, 7, 1, min), free), "exactly the floor")
	assert.True(t, feeFloorOK(parityTx(a, b, 7, 1, min+1), free))
	assert.True(t, feeFloorOK(parityTx(a, b, 0, 1, 0), free), "nonce 0 is exempt, like at admission")
	assert.True(t, feeFloorOK(parityTx(a, freeAddr, 7, 1, 0), free), "free recipient is exempt")
	assert.True(t, feeFloorOK(parityTx(freeAddr, b, 7, 1, 0), free), "free sender is exempt")
}

// FilterBelowFeeFloor must keep group order and item order, drop only the offending txs, and remove emptied groups.
func TestFilterBelowFeeFloor_DropsOnlyUnderpaid(t *testing.T) {
	cs := newTestChainState(t)
	a := common.HexToAddress("0x01")
	b := common.HexToAddress("0x02")
	c := common.HexToAddress("0x03")
	min := uint64(mt_common.MINIMUM_BASE_FEE)
	ok1 := parityTx(a, b, 3, 1, min)
	bad := parityTx(a, b, 4, 1, 0)
	ok2 := parityTx(c, b, 9, 1, min+5)
	bad2 := parityTx(c, a, 9, 1, 1)

	g1 := grouptxns.RelativeGroup{Items: []grouptxns.Item{{ID: 0, Tx: ok1}, {ID: 1, Tx: bad}}}
	g2 := grouptxns.RelativeGroup{Items: []grouptxns.Item{{ID: 2, Tx: bad2}}}
	g3 := grouptxns.RelativeGroup{Items: []grouptxns.Item{{ID: 3, Tx: ok2}}}
	out := FilterBelowFeeFloor(cs, []grouptxns.RelativeGroup{g1, g2, g3})

	require.Len(t, out, 2, "the group that only held an underpaid tx is removed")
	require.Len(t, out[0].Items, 1)
	assert.Equal(t, ok1.Hash(), out[0].Items[0].Tx.Hash())
	require.Len(t, out[1].Items, 1)
	assert.Equal(t, ok2.Hash(), out[1].Items[0].Tx.Hash())
}

// End to end on real signed secp txs (filters active, no devnet bypass): an underpaid tx is dropped without a receipt,
// without touching the sender, and a correctly priced tx still executes.
func TestFeeFloor_EndToEnd_SignedSecpTx(t *testing.T) {
	to := common.HexToAddress("0x2222222222222222222222222222222222222b")
	leader := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	min := int64(mt_common.MINIMUM_BASE_FEE)
	for _, tc := range []struct {
		name       string
		price      int64
		wantExec   bool
		wantNonce  uint64
		wantReceip int
	}{
		{"below floor is dropped", min - 1, false, 1, 0},
		{"at floor executes", min, true, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, addr := genTestKey(t)
			inner := &types.LegacyTx{Nonce: 1, GasPrice: big.NewInt(tc.price), Gas: 21000, To: &to, Value: big.NewInt(1000)}
			ethTx, err := types.SignNewTx(key, types.NewEIP155Signer(big.NewInt(parityChainID)), inner)
			require.NoError(t, err)
			tx, err := transaction.NewTransactionFromEth(ethTx)
			require.NoError(t, err)

			cs := newTestChainState(t)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(parityChainID), TxSignatureMode: config.TxSignatureModeSecp})
			as := mt_state.NewAccountState(addr)
			as.AddBalance(big.NewInt(10_000_000_000))
			as.SetNonce(1)
			cs.GetAccountStateDB().SetState(as)
			seedAccount(t, cs, to, big.NewInt(1), 0)

			items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
			groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
			_, rcps, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)

			st, err := cs.GetAccountStateDB().AccountState(addr)
			require.NoError(t, err)
			assert.Equal(t, tc.wantReceip, len(rcps))
			assert.Equal(t, tc.wantNonce, st.Nonce())
			if !tc.wantExec {
				assert.Equal(t, 0, st.TotalBalance().Cmp(big.NewInt(10_000_000_000)), "an underpaid tx must not touch the sender")
			} else {
				assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())
			}
		})
	}
}
