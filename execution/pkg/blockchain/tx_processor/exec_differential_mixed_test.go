package tx_processor

import (
	"context"
	"fmt"
	"math/big"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Mixed-block differential test. ProcessTransactionsOptimistic runs the native-only groups through the lock-free fast path
// and the EVM / barrier groups through TrueBlockSTM; the reference below puts the SAME grouped block, in group order, through
// TrueBlockSTM alone. Groups are address-disjoint, so moving native transfers between the two engines must not change
// any account, receipt or deployed contract. Barrier txs (validator 0x1001, gateway 0x1002, parent-chain gateway 0x1003,
// rollup system 0x72) carry malformed calldata here, which is the case where nonce and fee handling used to diverge.

var diffMixedLeader = common.HexToAddress("0xAAAA000000000000000000000000000000000001")

type mixedFixture struct {
	native   []diffAccount // plain transfers among these
	evm      []diffAccount // deploys, contract calls and barrier txs come from these
	deployer common.Address
}

func mixedFixtureAccounts() mixedFixture {
	nb := []int64{50_000_000, 3_000_000_000, 2_100_000_000_000, 7, 900_000_000_000}
	eb := []int64{0, 1_000, 40_000_000, 3_000_000_000_000_000, 90_000_000_000_000_000_000 / 1_000_000}
	f := mixedFixture{deployer: common.BigToAddress(big.NewInt(0xD0))}
	for i, b := range nb {
		f.native = append(f.native, diffAccount{addr: common.BigToAddress(big.NewInt(int64(0xA1 + i))), balance: b})
	}
	for i, b := range eb {
		f.evm = append(f.evm, diffAccount{addr: common.BigToAddress(big.NewInt(int64(0xE1 + i))), balance: b})
	}
	return f
}

func mixedBlock(t *testing.T, r *rand.Rand, f mixedFixture, contract common.Address) []mt_types.Transaction {
	barriers := []common.Address{mt_common.VALIDATOR_CONTRACT_ADDRESS, mt_common.GATEWAY_CONTRACT_ADDRESS,
		mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, mt_common.ROLLUP_SYSTEM_ADDRESS}
	prices := []uint64{1, mt_common.MINIMUM_BASE_FEE, 0}
	gasLimits := []uint64{21000, 100_000, 5_000_000, 20999}
	garbage := func() []byte {
		switch r.Intn(4) {
		case 0:
			return nil
		case 1:
			return []byte{0x01, 0x02}
		case 2:
			return []byte{0xde, 0xad, 0xbe, 0xef}
		default:
			b := make([]byte, 36)
			r.Read(b)
			return b
		}
	}
	code, _ := hexDecode(testWrappedAssetBytecode)
	nonces := map[common.Address]uint64{}
	next := func(a common.Address) uint64 { n := nonces[a]; nonces[a]++; return n }

	// the native part reuses the pure-native generator over the native accounts
	txs := diffBlock(r, f.native)
	n := 2 + r.Intn(8)
	for i := 0; i < n; i++ {
		from := f.evm[r.Intn(len(f.evm))].addr
		var tx mt_types.Transaction
		switch r.Intn(4) {
		case 0: // deploy
			args, _ := testWrappedAssetABI(t).Pack("", big.NewInt(int64(1000+r.Intn(1000))))
			tx = newHighGasTx(from, common.Address{}, next(from), big.NewInt(0), append(append([]byte{}, code...), args...))
		case 1: // barrier address, malformed calldata
			tx = transaction.NewTransaction(from, barriers[r.Intn(len(barriers))], big.NewInt(0), gasLimits[r.Intn(len(gasLimits))],
				prices[r.Intn(len(prices))], uint64(5000+i), garbage(), nil, common.Hash{}, common.Hash{}, next(from), 991)
		case 2: // plain value transfer to a contract: must run the contract code, not the native branch
			tx = transaction.NewTransaction(from, contract, big.NewInt(int64(r.Intn(3))), gasLimits[r.Intn(len(gasLimits))],
				prices[r.Intn(len(prices))], uint64(5000+i), nil, nil, common.Hash{}, common.Hash{}, next(from), 991)
		default: // contract call with garbage calldata
			tx = transaction.NewTransaction(from, contract, big.NewInt(0), gasLimits[r.Intn(len(gasLimits))],
				prices[r.Intn(len(prices))], uint64(5000+i), garbage(), nil, common.Hash{}, common.Hash{}, next(from), 991)
		}
		txs = append(txs, tx)
	}
	r.Shuffle(len(txs), func(a, b int) { txs[a], txs[b] = txs[b], txs[a] })
	return txs
}

type mixedOutcome struct {
	accounts, receipts, deployed []string
	supply                       *big.Int
}

// mixedRun builds the state, pre-deploys one contract, groups the block and executes it either through
// ProcessTransactionsOptimistic (useSTMOnly=false) or through TrueBlockSTM alone over the same grouped order.
func mixedRun(t *testing.T, seed int64, useSTMOnly bool) (mixedOutcome, []mt_types.Transaction, *big.Int) {
	t.Helper()
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "true")
	t.Setenv("METANODE_DEVNET", "true")
	f := mixedFixtureAccounts()
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(parityChainID)})
	seedAccount(t, cs, f.deployer, big.NewInt(1_000_000_000_000_000), 0)
	seedAccount(t, cs, diffMixedLeader, big.NewInt(0), 0)
	supplyBefore := big.NewInt(0)
	for _, a := range append(append([]diffAccount{}, f.native...), f.evm...) {
		seedAccount(t, cs, a.addr, big.NewInt(a.balance), 0)
		supplyBefore.Add(supplyBefore, big.NewInt(a.balance))
	}
	contract := deployTestWrappedAsset(t, cs, f.deployer, big.NewInt(1000))
	// the deployer pays gas for the pre-deploy, so the supply baseline is read after it
	watch := []common.Address{diffMixedLeader, f.deployer, contract}
	for _, a := range append(append([]diffAccount{}, f.native...), f.evm...) {
		watch = append(watch, a.addr)
	}
	for i := 0; i < 4; i++ {
		watch = append(watch, common.BigToAddress(big.NewInt(int64(0xF000+i))))
	}
	sum := func(extra []common.Address) *big.Int {
		s := new(big.Int)
		for _, a := range append(append([]common.Address{}, watch...), extra...) {
			st, err := cs.GetAccountStateDB().AccountState(a)
			if err != nil {
				t.Fatalf("account state %s: %v", a.Hex(), err)
			}
			s.Add(s, st.TotalBalance())
		}
		return s
	}
	before := sum(nil)

	txs := mixedBlock(t, rand.New(rand.NewSource(seed)), f, contract)
	items := make([]grouptxns.Item, len(txs))
	for i, tx := range txs {
		items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}
	}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)

	idx := map[common.Hash]int{}
	for i, tx := range txs {
		idx[tx.Hash()] = i
	}
	var rcps []mt_types.Receipt
	var scrs []mt_types.ExecuteSCResult
	if useSTMOnly {
		flat := make([]mt_types.Transaction, 0, len(txs))
		for _, g := range groups {
			for _, it := range g.Items {
				flat = append(flat, it.Tx)
			}
		}
		_, rcps, scrs, _ = NewTrueBlockSTM(flat).Process(context.Background(), cs, diffMixedLeader, blankHeader(), 999)
	} else {
		_, rcps, scrs, _ = ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, diffMixedLeader, true)
	}

	var out mixedOutcome
	var deployed []common.Address
	for _, s := range scrs {
		if s == nil {
			continue
		}
		for addr := range s.MapCodeHash() {
			out.deployed = append(out.deployed, addr)
			deployed = append(deployed, common.HexToAddress(addr))
		}
	}
	sort.Strings(out.deployed)
	for _, a := range append(append([]common.Address{}, watch...), deployed...) {
		st, err := cs.GetAccountStateDB().AccountState(a)
		if err != nil {
			t.Fatalf("account state %s: %v", a.Hex(), err)
		}
		out.accounts = append(out.accounts, fmt.Sprintf("%s bal=%s nonce=%d", a.Hex(), st.TotalBalance(), st.Nonce()))
	}
	sort.Strings(out.accounts)
	for _, r := range rcps {
		if r == nil || fmt.Sprintf("%v", r) == "<nil>" {
			continue
		}
		out.receipts = append(out.receipts, fmt.Sprintf("tx#%02d status=%v gas=%d", idx[r.TransactionHash()], r.Status(), r.GasUsed()))
	}
	sort.Strings(out.receipts)
	out.supply = sum(deployed)
	return out, txs, before
}

func describeMixed(txs []mt_types.Transaction) string {
	var b strings.Builder
	for i, tx := range txs {
		kind := "call"
		if tx.IsDeployContract() {
			kind = "deploy"
		}
		fmt.Fprintf(&b, "  #%02d %-6s from=%s to=%s nonce=%d amount=%s gas=%d price=%d data=%dB\n", i, kind, tx.FromAddress().Hex()[38:],
			tx.ToAddress().Hex()[38:], tx.GetNonce(), tx.Amount(), tx.MaxGas(), tx.MaxGasPrice(), len(tx.CallData().Input()))
	}
	return b.String()
}

func TestExecDifferential_MixedBlocks_OptimisticVsTrueBlockSTMOnly(t *testing.T) {
	const iterations = 120
	diverged := 0
	for seed := int64(1); seed <= iterations; seed++ {
		opt, txs, before := mixedRun(t, seed, false)
		ref, _, _ := mixedRun(t, seed, true)
		for name, o := range map[string]mixedOutcome{"optimistic": opt, "stm-only": ref} {
			if o.supply.Cmp(before) != 0 {
				t.Errorf("seed %d %s: supply not conserved, before %s after %s\n%s", seed, name, before, o.supply, describeMixed(txs))
			}
		}
		same := strings.Join(opt.accounts, "\n") == strings.Join(ref.accounts, "\n") &&
			strings.Join(opt.receipts, "\n") == strings.Join(ref.receipts, "\n") &&
			strings.Join(opt.deployed, "\n") == strings.Join(ref.deployed, "\n")
		if !same {
			diverged++
			if diverged <= 3 {
				t.Errorf("seed %d: pipelines diverge\n%s optimistic accounts:\n  %s\n stm-only accounts:\n  %s\n optimistic receipts:\n  %s\n stm-only receipts:\n  %s\n optimistic deployed: %v\n stm-only deployed: %v",
					seed, describeMixed(txs), strings.Join(opt.accounts, "\n  "), strings.Join(ref.accounts, "\n  "),
					strings.Join(opt.receipts, "\n  "), strings.Join(ref.receipts, "\n  "), opt.deployed, ref.deployed)
			}
		}
	}
	if diverged > 0 {
		t.Errorf("%d of %d random mixed blocks produced different outcomes", diverged, iterations)
	}
}
