package tx_processor

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

func signAuthorization(t *testing.T, key *ecdsa.PrivateKey, chainID uint64, delegate common.Address, nonce uint64) types.SetCodeAuthorization {
	t.Helper()
	auth, err := types.SignSetCode(key, types.SetCodeAuthorization{
		ChainID: *uint256.NewInt(chainID),
		Address: delegate,
		Nonce:   nonce,
	})
	if err != nil {
		t.Fatalf("SignSetCode: %v", err)
	}
	return auth
}

// TestTrueBlockSTM_EIP7702_NativeTransfer_EstimateHit runs blocks in which several senders credit two "hot"
// accounts (which are also the EIP-7702 authorities) while one native transfer carries an authorization
// list signed by both hot accounts. The authority read inside processAuthorizationList can hit an ESTIMATE
// left by a lower tx that is being re-executed; if that were ignored the authorization would be dropped
// silently (the tx still succeeds), and which authority is lost would depend on scheduling, i.e. a
// non-deterministic state. Native credits commute, so the expected final state is exact and independent of
// scheduling: both authorities delegate, have nonce 1 and hold the sum of all credits.
func TestTrueBlockSTM_EIP7702_NativeTransfer_EstimateHit(t *testing.T) {
	iters := stressIterations(40)
	procsChoices := []int{1, 2, 4, 8, 16}
	if v := os.Getenv("STM_STRESS_PROCS"); v != "" { // e.g. "8" or "1,8"
		procsChoices = nil
		for _, f := range strings.Split(v, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && n > 0 {
				procsChoices = append(procsChoices, n)
			}
		}
	}
	maxWorkers, _ := strconv.Atoi(os.Getenv("STM_STRESS_MAXWORKERS")) // 0 = default (GOMAXPROCS-scaled)
	maxFail, _ := strconv.Atoi(os.Getenv("STM_STRESS_MAXFAIL"))       // 0 = stop after 5 failing iterations
	if maxFail <= 0 {
		maxFail = 5
	}
	prevProcs := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(prevProcs) })

	newKey := func() (*ecdsa.PrivateKey, common.Address) {
		k, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		return k, crypto.PubkeyToAddress(k.PublicKey)
	}
	const chainID = 991
	authKeys := make([]*ecdsa.PrivateKey, 2)
	authAddrs := make([]common.Address, 2)
	for i := range authKeys {
		authKeys[i], authAddrs[i] = newKey()
	}
	senderKeys := make([]*ecdsa.PrivateKey, 3)
	senderAddrs := make([]common.Address, 3)
	for i := range senderKeys {
		senderKeys[i], senderAddrs[i] = newKey()
	}
	sponsorKey, sponsorAddr := newKey() // sends the EIP-7702 tx

	delegateAddr := common.HexToAddress("0x0000000000000000000000000000000000001234")
	expectedCodeHash := crypto.Keccak256Hash(types.AddressToDelegation(delegateAddr))
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")

	const creditsPerSender = 20 // 3 senders * 20 = 60 credits, each of 1 wei, spread over the two authorities
	failures := 0
	for it := 0; it < iters && failures < maxFail; it++ {
		procs := procsChoices[it%len(procsChoices)]
		runtime.GOMAXPROCS(procs)
		rnd := rand.New(rand.NewSource(int64(it) + 1))

		chainState := newTestChainState(t)
		chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
		big1e12 := big.NewInt(1_000_000_000_000)
		for _, a := range senderAddrs {
			seedAccount(t, chainState, a, big1e12, 0)
		}
		seedAccount(t, chainState, sponsorAddr, big1e12, 0)
		for _, a := range authAddrs {
			seedAccount(t, chainState, a, big.NewInt(0), 0)
		}

		// Credits: each sender's nonces ascend; the two authorities receive alternating credits.
		wantBal := []*big.Int{big.NewInt(0), big.NewInt(0)}
		var queue [][]mt_types.Transaction
		for s := range senderAddrs {
			var chain []mt_types.Transaction
			for n := 0; n < creditsPerSender; n++ {
				to := (s + n) % 2
				chain = append(chain, newTx(senderAddrs[s], authAddrs[to], uint64(n), big.NewInt(1), nil))
				wantBal[to].Add(wantBal[to], big.NewInt(1))
			}
			queue = append(queue, chain)
		}

		// EIP-7702 native transfer carrying authorizations from BOTH authorities (auth nonce 0 = their nonce).
		inner := &types.SetCodeTx{
			ChainID:   uint256.NewInt(chainID),
			Nonce:     0,
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(1),
			Gas:       100000,
			To:        common.HexToAddress("0x9999"),
			Value:     uint256.NewInt(0),
			AuthList: []types.SetCodeAuthorization{
				signAuthorization(t, authKeys[0], chainID, delegateAddr, 0),
				signAuthorization(t, authKeys[1], chainID, delegateAddr, 0),
			},
		}
		ethTx, err := types.SignNewTx(sponsorKey, types.NewPragueSigner(big.NewInt(chainID)), inner)
		if err != nil {
			t.Fatalf("SignNewTx: %v", err)
		}
		eip7702Tx, err := transaction.NewTransactionFromEth(ethTx)
		if err != nil {
			t.Fatalf("NewTransactionFromEth: %v", err)
		}
		queue = append(queue, []mt_types.Transaction{eip7702Tx})

		// Interleave the chains randomly while keeping each chain's own order.
		var txs []mt_types.Transaction
		for len(queue) > 0 {
			i := rnd.Intn(len(queue))
			txs = append(txs, queue[i][0])
			queue[i] = queue[i][1:]
			if len(queue[i]) == 0 {
				queue = append(queue[:i], queue[i+1:]...)
			}
		}

		stm := NewTrueBlockSTM(txs)
		if maxWorkers > 0 {
			stm = stm.WithMaxExecWorkers(maxWorkers)
		}
		_, rcps, _, _ := stm.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)

		var problems []string
		for i, rcp := range rcps {
			if rcp == nil || rcp.Status() != pb.RECEIPT_STATUS_RETURNED {
				problems = append(problems, "tx "+strconv.Itoa(i)+" did not succeed")
				break
			}
		}
		for i, a := range authAddrs {
			st, err := chainState.GetAccountStateDB().AccountState(a)
			if err != nil || st == nil {
				problems = append(problems, "authority "+strconv.Itoa(i)+" missing")
				continue
			}
			if got := st.TotalBalance(); got.Cmp(wantBal[i]) != 0 {
				problems = append(problems, "authority "+strconv.Itoa(i)+" balance "+got.String()+", want "+wantBal[i].String())
			}
			if st.Nonce() != 1 {
				problems = append(problems, "authority "+strconv.Itoa(i)+" nonce "+strconv.FormatUint(st.Nonce(), 10)+", want 1 (authorization dropped)")
			}
			sc := st.SmartContractState()
			if sc == nil {
				problems = append(problems, "authority "+strconv.Itoa(i)+" has no delegation (authorization dropped)")
			} else if sc.CodeHash() != expectedCodeHash {
				problems = append(problems, "authority "+strconv.Itoa(i)+" wrong code hash")
			}
		}
		for i, a := range senderAddrs {
			if st, err := chainState.GetAccountStateDB().AccountState(a); err != nil || st == nil || st.Nonce() != creditsPerSender {
				problems = append(problems, "sender "+strconv.Itoa(i)+" nonce wrong")
			}
		}
		if len(problems) > 0 {
			failures++
			t.Errorf("iter %d (GOMAXPROCS=%d): %s", it, procs, strings.Join(problems, "; "))
		}
	}
}

// TestTrueBlockSTM_EIP7702_PartialAuthWriteIsCleanedUp covers the write set of an incarnation that is
// suspended in the MIDDLE of an authorization list. MVCCAccountStateDB writes to the shared MVCC map
// immediately, so when the second authority hits an ESTIMATE the first authority's SetNonce/SetCodeHash are
// already visible at this tx's version. The block is arranged so that the first authority's authorization is
// VALID for an early incarnation that has not yet seen the (lower) tx that consumes its nonce, and INVALID in
// the final incarnation. If the suspended incarnation's write set is not recorded, nobody deletes that
// stale entry and the authority ends up delegating although the correct execution skips its authorization.
func TestTrueBlockSTM_EIP7702_PartialAuthWriteIsCleanedUp(t *testing.T) {
	// Where the ESTIMATE is hit AFTER the first authority was already written:
	//   second-authority: reading the second authority inside processAuthorizationList
	//   native-recipient: reading the recipient of the native transfer (recipient = hot account)
	//   contract-call:    the EVM call reads the hot account through the VM callback
	for _, mode := range []string{"second-authority", "native-recipient", "contract-call"} {
		mode := mode
		t.Run(mode, func(t *testing.T) { runPartialAuthScenario(t, mode) })
	}
}

func runPartialAuthScenario(t *testing.T, mode string) {
	iters := stressIterations(40)
	procsChoices := []int{2, 4, 8, 16}
	if v := os.Getenv("STM_STRESS_PROCS"); v != "" {
		procsChoices = nil
		for _, f := range strings.Split(v, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && n > 0 {
				procsChoices = append(procsChoices, n)
			}
		}
	}
	maxFail, _ := strconv.Atoi(os.Getenv("STM_STRESS_MAXFAIL"))
	if maxFail <= 0 {
		maxFail = 5
	}
	prevProcs := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(prevProcs) })

	newKey := func() (*ecdsa.PrivateKey, common.Address) {
		k, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		return k, crypto.PubkeyToAddress(k.PublicKey)
	}
	const chainID = 991
	trapKey, trapAddr := newKey() // authorization valid only if its own tx has not run yet
	hotKey, hotAddr := newKey()   // hot credit target: its read is what hits the ESTIMATE
	sponsorKey, sponsorAddr := newKey()
	senderKeys := make([]*ecdsa.PrivateKey, 3)
	senderAddrs := make([]common.Address, 3)
	for i := range senderKeys {
		senderKeys[i], senderAddrs[i] = newKey()
	}
	_ = senderKeys
	delegateAddr := common.HexToAddress("0x0000000000000000000000000000000000001234")
	expectedCodeHash := crypto.Keccak256Hash(types.AddressToDelegation(delegateAddr))
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")

	const creditsPerSender = 20
	failures := 0
	for it := 0; it < iters && failures < maxFail; it++ {
		procs := procsChoices[it%len(procsChoices)]
		runtime.GOMAXPROCS(procs)
		rnd := rand.New(rand.NewSource(int64(it) + 1))

		chainState := newTestChainState(t)
		chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
		big1e12 := big.NewInt(1_000_000_000_000)
		for _, a := range append(append([]common.Address{}, senderAddrs...), sponsorAddr, trapAddr) {
			seedAccount(t, chainState, a, big1e12, 0)
		}
		seedAccount(t, chainState, hotAddr, big.NewInt(0), 0)

		var queue [][]mt_types.Transaction
		for s := range senderAddrs {
			var chain []mt_types.Transaction
			for n := 0; n < creditsPerSender; n++ {
				chain = append(chain, newTx(senderAddrs[s], hotAddr, uint64(n), big.NewInt(1), nil))
			}
			queue = append(queue, chain)
		}
		wantHot := big.NewInt(int64(len(senderAddrs) * creditsPerSender))

		// Chain [A, B]: A (from trap, nonce 0) precedes B (7702 tx) in block order, so in the correct
		// execution trap's nonce is already 1 when B runs and B's authorization tuple (nonce 0) is skipped.
		txA := newTx(trapAddr, common.HexToAddress("0x7777"), 0, big.NewInt(1), nil)
		inner := &types.SetCodeTx{
			ChainID:   uint256.NewInt(chainID),
			Nonce:     0,
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(1),
			Gas:       100000,
			To:        common.HexToAddress("0x9999"),
			Value:     uint256.NewInt(0),
			AuthList: []types.SetCodeAuthorization{
				signAuthorization(t, trapKey, chainID, delegateAddr, 0), // written first, invalid in the end
			},
		}
		switch mode {
		case "second-authority":
			inner.AuthList = append(inner.AuthList, signAuthorization(t, hotKey, chainID, delegateAddr, 0)) // read second
		case "native-recipient":
			inner.To = hotAddr
		case "contract-call":
			inner.To = hotAddr
			inner.Data = []byte{0xde, 0xad, 0xbe, 0xef}
		}
		ethTx, err := types.SignNewTx(sponsorKey, types.NewPragueSigner(big.NewInt(chainID)), inner)
		if err != nil {
			t.Fatalf("SignNewTx: %v", err)
		}
		txB, err := transaction.NewTransactionFromEth(ethTx)
		if err != nil {
			t.Fatalf("NewTransactionFromEth: %v", err)
		}
		queue = append(queue, []mt_types.Transaction{txA, txB})

		var txs []mt_types.Transaction
		for len(queue) > 0 {
			i := rnd.Intn(len(queue))
			txs = append(txs, queue[i][0])
			queue[i] = queue[i][1:]
			if len(queue[i]) == 0 {
				queue = append(queue[:i], queue[i+1:]...)
			}
		}

		stm := NewTrueBlockSTM(txs)
		_, rcps, _, _ := stm.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)

		var problems []string
		for i, rcp := range rcps {
			// A call with calldata to an account without code always ends in TRANSACTION_ERROR here (same as
			// TestTrueBlockSTM_SmartContractGasDeduction), so for that one tx only require "a receipt exists".
			if mode == "contract-call" && txs[i].Hash() == txB.Hash() && rcp != nil {
				continue
			}
			if rcp == nil || rcp.Status() != pb.RECEIPT_STATUS_RETURNED {
				detail := "nil receipt"
				if rcp != nil {
					detail = "status " + rcp.Status().String() + " exception " + rcp.Exception().String() + " return " + string(rcp.Return())
				}
				problems = append(problems, "tx "+strconv.Itoa(i)+" did not succeed ("+detail+")")
				break
			}
		}
		if st, err := chainState.GetAccountStateDB().AccountState(trapAddr); err != nil || st == nil {
			problems = append(problems, "trap account missing")
		} else {
			if st.Nonce() != 1 {
				problems = append(problems, "trap nonce "+strconv.FormatUint(st.Nonce(), 10)+", want 1")
			}
			if sc := st.SmartContractState(); sc != nil && sc.CodeHash() == expectedCodeHash {
				problems = append(problems, "trap delegates although its authorization must be skipped (stale write of a suspended incarnation)")
			}
		}
		if st, err := chainState.GetAccountStateDB().AccountState(hotAddr); err != nil || st == nil {
			problems = append(problems, "hot account missing")
		} else {
			if st.TotalBalance().Cmp(wantHot) != 0 {
				problems = append(problems, "hot balance "+st.TotalBalance().String()+", want "+wantHot.String())
			}
			if mode == "second-authority" {
				if st.Nonce() != 1 {
					problems = append(problems, "hot nonce "+strconv.FormatUint(st.Nonce(), 10)+", want 1 (authorization dropped)")
				}
				if sc := st.SmartContractState(); sc == nil || sc.CodeHash() != expectedCodeHash {
					problems = append(problems, "hot has no delegation (authorization dropped)")
				}
			}
		}
		if len(problems) > 0 {
			failures++
			t.Errorf("iter %d (GOMAXPROCS=%d): %s", it, procs, strings.Join(problems, "; "))
		}
	}
}

func zzInit(runtime []byte) []byte {
	return append([]byte{0x60, byte(len(runtime)), 0x60, 0x0c, 0x60, 0x00, 0x39, 0x60, byte(len(runtime)), 0x60, 0x00, 0xf3}, runtime...)
}

// A 7702-delegated EOA is the SENDER of a tx; the tx calls a contract that calls back into the sender, whose delegated code
// executes CREATE. In the EVM the sender's nonce then ends at tx.nonce+1+1.
func TestSenderNonceKeepsCreateIncrementOfDelegatedSender(t *testing.T) {
	deployKey, _ := crypto.HexToECDSA("4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318")
	deployer := crypto.PubkeyToAddress(deployKey.PublicKey)
	relayKey, _ := crypto.HexToECDSA("5c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362319")
	relayer := crypto.PubkeyToAddress(relayKey.PublicKey)
	delKey, _ := crypto.HexToECDSA("6c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362310")
	del := crypto.PubkeyToAddress(delKey.PublicKey)
	factory := crypto.CreateAddress(deployer, 1)
	caller := crypto.CreateAddress(deployer, 2)
	chainID := big.NewInt(991)
	price := uint64(mt_common.MINIMUM_BASE_FEE)

	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	cs, _, _, _ := newPersistentTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})
	leader := common.HexToAddress(auditLeader)
	big1 := new(big.Int).Mul(big.NewInt(1_000_000_000_000), big.NewInt(1000))
	seedAccount(t, cs, deployer, big1, 1)
	seedAccount(t, cs, relayer, big1, 1)
	seedAccount(t, cs, del, big1, 0)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	run := func(txs ...mt_types.Transaction) string {
		items := make([]grouptxns.Item, len(txs))
		for i, tx := range txs {
			items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}
		}
		groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
		_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
		line := ""
		for _, x := range r {
			if x != nil {
				line += fmt.Sprintf(" [%v gas=%d]", x.Status(), x.GasUsed())
			}
		}
		return line
	}
	legacy := func(k *ecdsa.PrivateKey, nonce uint64, to *common.Address, data []byte) mt_types.Transaction {
		signed, err := types.SignNewTx(k, types.NewEIP155Signer(chainID), &types.LegacyTx{Nonce: nonce, To: to, Value: big.NewInt(0), Gas: 400000, GasPrice: new(big.Int).SetUint64(price), Data: data})
		if err != nil {
			t.Fatal(err)
		}
		tx, err := transaction.NewTransactionFromEth(signed)
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	callerRuntime := common.FromHex("0x6000600060006000" + "6000" + "73")
	callerRuntime = append(callerRuntime, del.Bytes()...)
	callerRuntime = append(callerRuntime, common.FromHex("0x61fffff15000")...)

	l1 := run(legacy(deployKey, 1, nil, zzInit(common.FromHex("0x600060006000f05000"))), legacy(deployKey, 2, nil, zzInit(callerRuntime)))
	auth, err := types.SignSetCode(delKey, types.SetCodeAuthorization{ChainID: *uint256.MustFromBig(chainID), Address: factory, Nonce: 0})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := types.SignNewTx(relayKey, types.NewPragueSigner(chainID), &types.SetCodeTx{ChainID: uint256.MustFromBig(chainID), Nonce: 1, GasTipCap: uint256.NewInt(price), GasFeeCap: uint256.NewInt(price), Gas: 300000, To: relayer, Value: uint256.NewInt(0), AuthList: []types.SetCodeAuthorization{auth}})
	if err != nil {
		t.Fatal(err)
	}
	atx, err := transaction.NewTransactionFromEth(signed)
	if err != nil {
		t.Fatal(err)
	}
	l2 := run(atx)
	nonce := func(a common.Address) uint64 { st, _ := cs.GetAccountStateDB().AccountState(a); return st.Nonce() }
	before := nonce(del)
	l3 := run(legacy(delKey, before, &caller, []byte{1}))
	root, _ := cs.GetAccountStateDB().IntermediateRoot(true)
	fmt.Printf("DELNONCE root=%x delegated_sender nonce before=%d after=%d (EVM expects before+2)%s%s%s\n", root, before, nonce(del), l1, l2, l3)
	require.Equal(t, before+2, nonce(del), "delegated sender executing CREATE must end at before+2")
}
