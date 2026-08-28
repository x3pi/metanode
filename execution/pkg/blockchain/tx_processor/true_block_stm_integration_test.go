package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	mt_state "github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ─── Lightweight ChainState harness for integration tests ───
//
// These tests exercise the real TrueBlockSTM.Process / ProcessTransactionsOptimistic
// entry points end-to-end (not just their helper functions), using an
// in-memory MPT trie backend instead of the production NOMT (Rust/FFI)
// backend so they run as plain Go unit tests.

func newTestChainState(t *testing.T) *blockchain.ChainState {
	t.Helper()

	prevBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	t.Cleanup(func() { trie.SetStateBackend(prevBackend) })

	accountStorage := storage.NewDummyStorage("")
	codeStorage := storage.NewDummyStorage("")
	scStorage := storage.NewDummyStorage("")

	header := block.NewBlockHeader(
		common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{},
		common.Address{}, 0, common.Hash{}, 0,
	)

	cs, err := blockchain.NewChainStateRemote(header, accountStorage, codeStorage, scStorage, map[common.Address]struct{}{})
	if err != nil {
		t.Fatalf("failed to create test chain state: %v", err)
	}
	return cs
}

func seedAccount(t *testing.T, cs *blockchain.ChainState, addr common.Address, balance *big.Int, nonce uint64) {
	t.Helper()
	as := mt_state.NewAccountState(addr)
	as.AddBalance(balance)
	as.SetNonce(nonce)
	cs.GetAccountStateDB().SetState(as)
}

func newTx(from, to common.Address, nonce uint64, amount *big.Int, data []byte) *transaction.Transaction {
	tx := transaction.NewTransaction(
		from, to, amount,
		21000, // maxGas
		1,     // maxGasPrice
		1000,  // maxTimeUse
		data,
		nil,           // relatedAddresses
		common.Hash{}, // lastDeviceKey
		common.Hash{}, // newDeviceKey
		nonce,
		1, // chainId
	)
	return tx.(*transaction.Transaction)
}

func blankHeader() types.BlockHeader {
	return block.NewBlockHeader(common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{}, common.Address{}, 0, common.Hash{}, 0)
}

// ─── A1: barrier TX segmentation (validator/cross-chain) ───

// TestTrueBlockSTM_BarrierTxObservesRealCommitNotSharedMVCCMap verifies the
// core plumbing added for A1: commitToBase() must run against the REAL
// chainState before each barrier TX, because runBarrierTx reads
// chainState.GetAccountStateDB() DIRECTLY — it does NOT go through the MVCC
// engine (that's the whole point: barrier TXs are excluded from MVCC
// tracking). A naive test that only checks a later MVCC segment can see an
// earlier segment's writes would pass even without any commit, because
// TrueBlockSTM's accountMap is one object shared across the whole block —
// segment boundaries are invisible to it. Only the barrier TX's direct
// baseDB read actually depends on commitToBase having run first, so that's
// what this test pins down:
//   - segment 1 bumps addrC's nonce (0 -> 1) via a normal MVCC-tracked TX.
//   - the barrier TX (from addrC) is built with the STALE nonce (0).
//   - if commitToBase ran first, the real chainState already shows nonce=1,
//     so the barrier TX's own nonce check rejects it (nil receipt).
//   - if commitToBase were skipped/misordered, chainState would still show
//     the stale nonce=0, the check would incorrectly pass, and execution
//     would proceed into ValidatorHandler (producing a non-nil,
//     TRANSACTION_ERROR receipt from the too-short calldata) instead of
//     being rejected up front.
func TestTrueBlockSTM_BarrierTxObservesRealCommitNotSharedMVCCMap(t *testing.T) {
	cs := newTestChainState(t)

	addrA := common.HexToAddress("0xA1A1A1A1A1A1A1A1A1A1A1A1A1A1A1A1A1A1A1A1")
	addrB := common.HexToAddress("0xB2B2B2B2B2B2B2B2B2B2B2B2B2B2B2B2B2B2B2B2")
	addrC := common.HexToAddress("0xC3C3C3C3C3C3C3C3C3C3C3C3C3C3C3C3C3C3C3C3") // barrier TX sender
	addrD := common.HexToAddress("0xD4D4D4D4D4D4D4D4D4D4D4D4D4D4D4D4D4D4D4D4")
	addrX := common.HexToAddress("0x9999999999999999999999999999999999999a")

	seedAccount(t, cs, addrA, big.NewInt(1_000_000), 0)
	seedAccount(t, cs, addrC, big.NewInt(1_000_000), 0)

	tx0 := newTx(addrA, addrB, 0, big.NewInt(500_000), nil)                          // A -> B, segment 1
	txBump := newTx(addrC, addrX, 0, big.NewInt(1000), nil)                          // C -> X, segment 1, bumps C's nonce 0 -> 1 (MVCC only)
	tx1 := newTx(addrC, mt_common.VALIDATOR_CONTRACT_ADDRESS, 0, big.NewInt(0), nil) // barrier: STALE nonce
	tx2 := newTx(addrB, addrD, 0, big.NewInt(100_000), nil)                          // B -> D, segment 2

	txs := []types.Transaction{tx0, txBump, tx1, tx2}
	leaderAddr := common.HexToAddress("0xE5E5E5E5E5E5E5E5E5E5E5E5E5E5E5E5E5E5E5E5")

	stm := NewTrueBlockSTM(txs)
	gotTxs, gotRcps, _, _ := stm.Process(context.Background(), cs, leaderAddr, blankHeader(), 12345)

	if len(gotTxs) != 4 || len(gotRcps) != 4 {
		t.Fatalf("expected 4 txs/receipts, got %d/%d", len(gotTxs), len(gotRcps))
	}

	if gotRcps[0] == nil || gotRcps[0].Status() != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("tx0 (A->B) should have succeeded, got receipt=%v", gotRcps[0])
	}
	if gotRcps[1] == nil || gotRcps[1].Status() != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("txBump (C->X) should have succeeded, got receipt=%v", gotRcps[1])
	}
	if gotRcps[2] != nil {
		t.Fatalf("tx1 (barrier, stale nonce) should have been rejected with a nil receipt — "+
			"a non-nil receipt means it read a stale (pre-commit) nonce from chainState, got %v", gotRcps[2])
	}
	if gotRcps[3] == nil || gotRcps[3].Status() != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("tx2 (B->D) should have succeeded — segment 2 must see segment 1's writes, got receipt=%v", gotRcps[3])
	}

	dState, err := cs.GetAccountStateDB().AccountState(addrD)
	if err != nil || dState == nil {
		t.Fatalf("expected addrD account to exist after commit, err=%v", err)
	}
	if dState.TotalBalance().Cmp(big.NewInt(100_000)) != 0 {
		t.Errorf("addrD balance = %s, want 100000", dState.TotalBalance())
	}

	cState, err := cs.GetAccountStateDB().AccountState(addrC)
	if err != nil || cState == nil {
		t.Fatalf("expected addrC account to exist, err=%v", err)
	}
	if cState.Nonce() != 1 {
		t.Errorf("addrC nonce = %d, want 1 (from txBump; the rejected barrier TX must not itself consume/advance it)", cState.Nonce())
	}
}

// ─── A2: mixed native + EVM-group block runs sequentially, no lost update ───

// TestProcessTransactionsOptimistic_MixedBlock_LeaderRewardNotLost exercises
// the mixed-block branch of ProcessTransactionsOptimistic with one native
// transfer group and one account-setting (EVM-classified) group, both of
// which independently credit the leader's account (native fast-path via
// AddPendingBalance, TrueBlockSTM via AddBalance — see commitToBase/Process).
// Before the A2 fix these two pipelines ran as concurrent goroutines writing
// the same global chainState; a torn SetState from one could clobber the
// other's leader-balance write. Running them sequentially (this test's
// subject) must always yield the exact sum of both fees.
func TestProcessTransactionsOptimistic_MixedBlock_LeaderRewardNotLost(t *testing.T) {
	cs := newTestChainState(t)

	nativeSender := common.HexToAddress("0x1111111111111111111111111111111111111a")
	nativeReceiver := common.HexToAddress("0x2222222222222222222222222222222222222b")
	blsSender := common.HexToAddress("0x3333333333333333333333333333333333333c")

	seedAccount(t, cs, nativeSender, big.NewInt(1_000_000), 0)
	seedAccount(t, cs, blsSender, big.NewInt(1_000_000), 0)

	nativeTx := newTx(nativeSender, nativeReceiver, 0, big.NewInt(1000), nil)

	blsKey := make([]byte, 48)
	for i := range blsKey {
		blsKey[i] = byte(i + 1)
	}
	packedInput, err := PackSetBlsPublicKey(blsKey)
	if err != nil {
		t.Fatalf("PackSetBlsPublicKey: %v", err)
	}
	callData := transaction.NewCallData(packedInput)
	dataBytes, err := callData.Marshal()
	if err != nil {
		t.Fatalf("marshal calldata: %v", err)
	}
	accountSettingAddr := utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	blsTx := newTx(blsSender, accountSettingAddr, 0, big.NewInt(0), dataBytes)

	items := []grouptxns.Item{
		{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(nativeTx), Tx: nativeTx},
		{ID: 1, Array: grouptxns.BuildDeterministicGroupAddrs(blsTx), Tx: blsTx},
	}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)

	// Sanity-check the test actually exercises the mixed-block branch: one
	// native-only group and one contract-classified group, both present.
	hasNative, hasContract := false, false
	for _, g := range groups {
		if g.Kind == grouptxns.GroupKindNativeOnly {
			hasNative = true
		} else {
			hasContract = true
		}
	}
	if !hasNative || !hasContract {
		t.Fatalf("test setup invariant broken: expected both a native-only and a contract group, got groups=%+v", groups)
	}

	leaderAddr := common.HexToAddress("0x4444444444444444444444444444444444444d")

	allTxs, allRcps, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, groups, blankHeader(),
		false, false, 999, leaderAddr, true, // skipSignatureVerify=true
	)

	if len(allTxs) != 2 || len(allRcps) != 2 {
		t.Fatalf("expected 2 txs processed, got %d txs / %d receipts", len(allTxs), len(allRcps))
	}
	for i, rcp := range allRcps {
		if rcp == nil || rcp.Status() != pb.RECEIPT_STATUS_RETURNED {
			t.Fatalf("tx %d failed: %v", i, rcp)
		}
	}

	leaderState, err := cs.GetAccountStateDB().AccountState(leaderAddr)
	if err != nil || leaderState == nil {
		t.Fatalf("expected leader account to exist, err=%v", err)
	}

	// nativeReceiver is never seeded (a genuinely brand-new account), so the native leg
	// also pays the anti-dust-account surcharge (EXE-03 fix, 2026-08-27, see
	// note/threat_matrix_verified_fixes_execution_plan.md Task 4): TRANSFER_GAS_COST +
	// CallNewAccountGas, on top of the unrelated STM leg's own TRANSFER_GAS_COST.
	wantFee := new(big.Int).SetUint64(2*mt_common.TRANSFER_GAS_COST + params.CallNewAccountGas)
	if leaderState.TotalBalance().Cmp(wantFee) != 0 {
		t.Errorf("leader reward = %s, want %s (native fee [+ new-account surcharge] + STM fee, no lost update)", leaderState.TotalBalance(), wantFee)
	}
}

// ─── A3: Smart Contract Gas Deduction & Insufficient Balance Revert ───

func TestTrueBlockSTM_SmartContractGasDeduction(t *testing.T) {
	cs := newTestChainState(t)

	senderGood := common.HexToAddress("0x5555555555555555555555555555555555555555")
	senderPoor := common.HexToAddress("0x6666666666666666666666666666666666666666")

	// senderGood has 1,000,000 wei. Gas cost is ~20000.
	seedAccount(t, cs, senderGood, big.NewInt(1_000_000), 0)

	// senderPoor has only 100 wei. Gas cost is ~20000, so it will fail to pay gas.
	seedAccount(t, cs, senderPoor, big.NewInt(100), 0)

	blsKey := make([]byte, 48)
	for i := range blsKey {
		blsKey[i] = byte(i + 1)
	}
	packedInput, _ := PackSetBlsPublicKey(blsKey)
	callData := transaction.NewCallData(packedInput)
	dataBytes, _ := callData.Marshal()
	dummyContractAddr := common.HexToAddress("0x9999999999999999999999999999999999999999")

	// MaxGasPrice is 1
	txGood := newTx(senderGood, dummyContractAddr, 0, big.NewInt(0), dataBytes)
	txPoor := newTx(senderPoor, dummyContractAddr, 0, big.NewInt(0), dataBytes)

	items := []grouptxns.Item{
		{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(txGood), Tx: txGood},
		{ID: 1, Array: grouptxns.BuildDeterministicGroupAddrs(txPoor), Tx: txPoor},
	}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)

	leaderAddr := common.HexToAddress("0x7777777777777777777777777777777777777777")

	allTxs, allRcps, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, groups, blankHeader(),
		false, false, 999, leaderAddr, true,
	)

	if len(allTxs) != 2 || len(allRcps) != 2 {
		t.Fatalf("expected 2 txs processed, got %d txs / %d receipts", len(allTxs), len(allRcps))
	}

	var rcpGood, rcpPoor types.Receipt
	if allTxs[0].Hash() == txGood.Hash() {
		rcpGood = allRcps[0]
		rcpPoor = allRcps[1]
	} else {
		rcpGood = allRcps[1]
		rcpPoor = allRcps[0]
	}

	if rcpGood.Status() != pb.RECEIPT_STATUS_TRANSACTION_ERROR {
		t.Fatalf("txGood should have failed (dummy contract), got status %v", rcpGood.Status())
	}
	if rcpPoor.Status() != pb.RECEIPT_STATUS_TRANSACTION_ERROR {
		t.Fatalf("txPoor should have failed with TRANSACTION_ERROR due to insufficient gas balance, got status %v", rcpPoor.Status())
	}

	goodState, _ := cs.GetAccountStateDB().AccountState(senderGood)
	expectedGoodBalance := big.NewInt(1_000_000 - int64(rcpGood.GasUsed()))
	if goodState.TotalBalance().Cmp(expectedGoodBalance) != 0 {
		t.Errorf("senderGood balance = %s, want %s (gas deducted)", goodState.TotalBalance(), expectedGoodBalance)
	}

	poorState, _ := cs.GetAccountStateDB().AccountState(senderPoor)
	if poorState.TotalBalance().Cmp(big.NewInt(100)) != 0 {
		t.Errorf("senderPoor balance = %s, want 100 (state reverted)", poorState.TotalBalance())
	}
}
