package tx_processor

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Barrier differential test with VALID calldata for the validator contract (0x1001), the gateway (0x1002) and the rollup
// system address (0x72). The block also carries native transfers. Candidate = ProcessTransactionsOptimistic (native groups
// through the fast path, barrier/EVM groups through TrueBlockSTM); reference = the same grouped block, in group order,
// through TrueBlockSTM alone. The fingerprint is the account state root and the stake state root (after binding contract
// storage roots, as block processing does) plus every watched balance/nonce and every receipt, so any state a handler
// writes, in any trie, has to match.

// sorts before every barrier sender (groups order same-group txs by sender address), so its credits run first
var barrierFunder = common.BigToAddress(new(big.Int).Add(new(big.Int).Lsh(big.NewInt(0x1111), 128), big.NewInt(0x01)))

const diffBarrierSeedHex = "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b"

var diffBarrierCluster = func() cm.PublicKey {
	var k cm.PublicKey
	copy(k[:], []byte("cluster_pub_key_32_bytes_long!!"))
	return k
}()

type barrierEnv struct {
	nodeKP *bls.KeyPair
	kp103  *bls.KeyPair     // sole committee member of gateway chain 103 (unregister / bond claims)
	kpR    *bls.KeyPair     // sole committee member of the reserve chain 104 (supply mint / allocation transfer)
	users  []common.Address // pool of users the rollup system registers
	evm    []diffAccount    // validator / gateway senders
	native []diffAccount
}

func newBarrierEnv() barrierEnv {
	seed := common.FromHex(diffBarrierSeedHex)
	env := barrierEnv{nodeKP: bls.NewKeyPair(seed[:32])}
	seed103 := common.FromHex("0x1030103010301030103010301030103010301030103010301030103010301030")
	seedR := common.FromHex("0x1040104010401040104010401040104010401040104010401040104010401040")
	env.kp103, env.kpR = bls.NewKeyPair(seed103[:32]), bls.NewKeyPair(seedR[:32])
	for i := 0; i < 4; i++ {
		env.users = append(env.users, common.BigToAddress(big.NewInt(int64(0x7000+i))))
	}
	for i := 0; i < 5; i++ {
		// not in the low, precompile-like address range: the gateway's native burn goes through the MVM, which throws for those
		env.evm = append(env.evm, diffAccount{addr: common.BigToAddress(new(big.Int).Add(new(big.Int).Lsh(big.NewInt(0x1111), 128), big.NewInt(int64(0xE1+i)))), balance: 1_000_000_000})
	}
	env.native = diffAccounts()
	return env
}

func barrierBlock(t *testing.T, r *rand.Rand, env barrierEnv) []mt_types.Transaction {
	vh, err := GetValidatorHandler()
	if err != nil {
		t.Fatal(err)
	}
	gh, err := GetGatewayHandler()
	if err != nil {
		t.Fatal(err)
	}
	wrap := func(in []byte) []byte {
		d, err := transaction.NewCallData(in).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	pack := func(abiPack func() ([]byte, error)) []byte {
		in, err := abiPack()
		if err != nil {
			t.Fatalf("pack: %v", err)
		}
		return wrap(in)
	}
	prices := []uint64{1, mt_common.MINIMUM_BASE_FEE}
	nonces := map[common.Address]uint64{}
	next := func(a common.Address) uint64 { n := nonces[a]; nonces[a]++; return n }

	// The sequential oracle runs one tx per block, and the block-end leader reward turns the leader into an existing account
	// after the first tx, which removes the new-account gas surcharge for later transfers TO the leader (45000 -> 20000
	// gas). That is a property of block granularity, not a pipeline difference, so such transfers are left out here.
	var txs []mt_types.Transaction
	for _, tx := range diffBlock(r, env.native) {
		if tx.ToAddress() != diffLeader {
			txs = append(txs, tx)
		}
	}
	// native credits into the barrier senders: whether a later validator/gateway tx can pay its fee now depends on an
	// earlier native tx having been applied first
	for i, k := 0, r.Intn(4); i < k; i++ {
		src := barrierFunder // dedicated funder, so its nonces never collide with the random native transfers
		txs = append(txs, transaction.NewTransaction(src, env.evm[r.Intn(len(env.evm))].addr, big.NewInt([]int64{2_000_000_000, 8_000_000_000}[r.Intn(2)]),
			21000, mt_common.MINIMUM_BASE_FEE, uint64(7000+i), nil, nil, common.Hash{}, common.Hash{}, next(src), 991))
	}
	n := 3 + r.Intn(8)
	for i := 0; i < n; i++ {
		from := env.evm[r.Intn(len(env.evm))].addr
		price := prices[r.Intn(len(prices))]
		var to common.Address
		var data []byte
		amount := big.NewInt(0)
		switch r.Intn(20) {
		case 6, 7, 8, 9: // the remaining validator methods; targets are the committee node or a sender
			to = mt_common.VALIDATOR_CONTRACT_ADDRESS
			target := env.nodeKP.Address()
			if r.Intn(2) == 0 {
				target = env.evm[r.Intn(len(env.evm))].addr
			}
			data = pack(func() ([]byte, error) {
				switch r.Intn(4) {
				case 0:
					return vh.abi.Pack("updateValidatorInfo", "n"+from.Hex()[38:], "d", "w", "i")
				case 1:
					return vh.abi.Pack("undelegate", target, big.NewInt(int64(1+r.Intn(1_000_000_000))))
				case 2:
					return vh.abi.Pack("withdrawReward", target)
				}
				return vh.abi.Pack("distributeRewards", target, big.NewInt(int64(r.Intn(1_000_000))))
			})
		case 17, 18, 19: // asset registration (cert from the home chain's own committee) and stake-funded chain registration
			to = mt_common.GATEWAY_CONTRACT_ADDRESS
			data = pack(func() ([]byte, error) {
				if r.Intn(3) == 0 {
					payload, err := json.Marshal(cross_chain.ChainRegistry{ChainID: uint64(110 + r.Intn(3)), Epoch: 1})
					if err != nil {
						return nil, err
					}
					return gh.abi.Pack("registerChainViaStake", payload, big.NewInt([]int64{1000, 5000}[r.Intn(2)]))
				}
				homes := []struct {
					id uint64
					kp *bls.KeyPair
				}{{102, env.nodeKP}, {103, env.kp103}, {104, env.kpR}}
				h := homes[r.Intn(len(homes))]
				assetID := big.NewInt(int64(1 + r.Intn(4)))
				canonical := common.BigToAddress(big.NewInt(int64(0xA550 + r.Intn(2))))
				payload, err := json.Marshal(cross_chain.AssetEntry{AssetID: assetID, HomeChainID: h.id, CanonicalContract: canonical})
				if err != nil {
					return nil, err
				}
				sig := bls.Sign(h.kp.PrivateKey(), cross_chain.ComputeRegisterAssetMessage(assetID, h.id, canonical)).Bytes()
				return gh.abi.Pack("registerAssetWithCert", payload, big.NewInt(1000), uint64(1), sig, []byte{0x01})
			})
		case 13, 14, 15, 16: // cert-authorized gateway calls, signed by the real single-member committees
			to = mt_common.GATEWAY_CONTRACT_ADDRESS
			sign := func(kp *bls.KeyPair, digest []byte) ([]byte, []byte) {
				return bls.Sign(kp.PrivateKey(), digest).Bytes(), []byte{0x01}
			}
			data = pack(func() ([]byte, error) {
				switch r.Intn(5) {
				case 0: // one-time genesis mint to the reserve chain (the second one in a block is rejected)
					amt := big.NewInt(1_000_000)
					sig, bm := sign(env.kpR, cross_chain.ComputeAllocateSupplyMessage(104, amt))
					return gh.abi.Pack("allocateSupplyWithCert", big.NewInt(104), amt, uint64(1), sig, bm)
				case 1: // move allocation out of the reserve; nonce 0 or 1, so ordering decides which one lands
					amt := big.NewInt([]int64{100, 300}[r.Intn(2)])
					nonce := uint64(r.Intn(2))
					sig, bm := sign(env.kpR, cross_chain.ComputeTransferAllocationMessage(104, 102, amt, nonce))
					return gh.abi.Pack("transferAllocationWithCert", big.NewInt(104), big.NewInt(102), amt, nonce, uint64(1), sig, bm)
				case 2: // transfer signed by the wrong committee
					amt := big.NewInt(100)
					sig, bm := sign(env.kp103, cross_chain.ComputeTransferAllocationMessage(104, 102, amt, 0))
					return gh.abi.Pack("transferAllocationWithCert", big.NewInt(104), big.NewInt(102), amt, uint64(0), uint64(1), sig, bm)
				case 3: // chain 103 leaves; repeating it (or a stale nonce) is rejected
					nonce := uint64(r.Intn(2))
					sig, bm := sign(env.kp103, cross_chain.ComputeUnregisterChainMessage(103, 1, nonce))
					return gh.abi.Pack("unregisterChainWithCert", big.NewInt(103), nonce, uint64(1), sig, bm)
				}
				return gh.abi.Pack("claimUnbondedBond", big.NewInt(103))
			})
		case 10, 11, 12: // gateway calls that move real balance through the native mint/burn path
			to = mt_common.GATEWAY_CONTRACT_ADDRESS
			data = pack(func() ([]byte, error) {
				switch r.Intn(3) {
				case 0:
					return gh.abi.Pack("postSecurityBond", big.NewInt(102), big.NewInt(int64(1+r.Intn(1_000_000))))
				case 1:
					return gh.abi.Pack("withdrawRelayerTip")
				}
				return gh.abi.Pack("claimUnbondedBond", big.NewInt(102))
			})
		case 0:
			to = mt_common.VALIDATOR_CONTRACT_ADDRESS
			data = pack(func() ([]byte, error) {
				return vh.abi.Pack("registerValidator", "p-"+from.Hex()[38:], "w", "n", "name", "d", "web", "img", uint64(100+r.Intn(50)),
					big.NewInt(1), "netkey", "host", "authkey-"+from.Hex()[38:], "protokey")
			})
		case 1:
			to = mt_common.VALIDATOR_CONTRACT_ADDRESS
			data = pack(func() ([]byte, error) { return vh.abi.Pack("deregisterValidator") })
		case 2:
			to = mt_common.VALIDATOR_CONTRACT_ADDRESS
			data = pack(func() ([]byte, error) { return vh.abi.Pack("setCommissionRate", uint64(r.Intn(12000))) })
		case 3: // delegate to an existing validator (the committee node, or one of the senders)
			to = mt_common.VALIDATOR_CONTRACT_ADDRESS
			target := env.nodeKP.Address()
			if r.Intn(2) == 0 {
				target = env.evm[r.Intn(len(env.evm))].addr
			}
			amount = big.NewInt([]int64{500_000_000, 1_500_000_000, 3_000_000_000}[r.Intn(3)]) // above a sender's own balance unless a native credit lands first
			data = pack(func() ([]byte, error) { return vh.abi.Pack("delegate", target) })
		case 4: // valid gateway outbound to the registered destination chain 102
			to = mt_common.GATEWAY_CONTRACT_ADDRESS
			data = pack(func() ([]byte, error) {
				return gh.abi.Pack("outbound", big.NewInt(102), env.users[r.Intn(len(env.users))], []byte{0xAA, byte(i)}, big.NewInt(0),
					big.NewInt(int64(1+r.Intn(500))), big.NewInt(int64(r.Intn(10))), big.NewInt(0), uint8(1), false, uint64(0))
			})
		default: // malformed calldata for the same addresses keeps the error paths in the mix
			to = []common.Address{mt_common.VALIDATOR_CONTRACT_ADDRESS, mt_common.GATEWAY_CONTRACT_ADDRESS}[r.Intn(2)]
			data = wrap([]byte{0xde, 0xad, 0xbe, 0xef, byte(i)})
		}
		txs = append(txs, transaction.NewTransaction(from, to, amount, 400_000, price, uint64(8000+i), data, nil,
			common.Hash{}, common.Hash{}, next(from), 991))
	}
	// rollup system events from the node identity: valid attested registrations, some replayed (same seq) or with a nonce gap
	for i, k := 0, r.Intn(4); i < k; i++ {
		user := env.users[r.Intn(len(env.users))]
		payload := attestedRegistrationPayload(t, env.nodeKP, user, diffBarrierCluster, uint64(1+r.Intn(2)))
		nonce := next(env.nodeKP.Address())
		if r.Intn(6) == 0 {
			nonce += 2
		}
		txs = append(txs, transaction.NewTransaction(env.nodeKP.Address(), rollup.RollupSystemAddress, big.NewInt(0), 21000, 1000,
			uint64(9000+i), payload, nil, common.Hash{}, common.Hash{}, nonce, 991))
	}
	r.Shuffle(len(txs), func(a, b int) { txs[a], txs[b] = txs[b], txs[a] })
	return txs
}

type barrierOutcome struct {
	accounts, receipts []string
	roots              string
}

// barrierSetup builds the chain state every barrier test shares: funded native and barrier-sender accounts, the node identity as
// the committee validator, three pre-registered validators, gateway destination chain 102 and the rollup system handler.
func barrierSetup(t *testing.T) (barrierEnv, *blockchain.ChainState) {
	t.Helper()
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "true")
	t.Setenv("METANODE_DEVNET", "true")
	env := newBarrierEnv()
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(parityChainID)})
	seedAccount(t, cs, diffLeader, big.NewInt(0), 0)
	seedAccount(t, cs, barrierFunder, big.NewInt(1_000_000_000_000_000), 0)
	for _, a := range append(append([]diffAccount{}, env.native...), env.evm...) {
		seedAccount(t, cs, a.addr, big.NewInt(a.balance), 0)
	}
	// node identity: BLS-native account, committee validator, funded for gas
	seedAccount(t, cs, env.nodeKP.Address(), big.NewInt(1_000_000_000_000_000), 0)
	addTestCommitteeValidator(t, cs, env.nodeKP)
	// the first three barrier senders are already validators, so deregister / setCommissionRate / delegate can succeed;
	// the other two can still register inside the block (registering again fails with "already exists")
	for _, a := range env.evm[:3] {
		if err := cs.GetStakeStateDB().CreateRegisterWithKeys(a.addr, "pre", "", "", "", 5, big.NewInt(0), "p", "w", "n", "", []byte{1}, []byte{2}, "h", []byte{3}); err != nil {
			t.Fatalf("pre-register validator: %v", err)
		}
	}
	// existing delegations to the committee node, with the matching funds held by the validator contract, so undelegate and
	// withdrawReward have a successful path
	seedAccount(t, cs, mt_common.VALIDATOR_CONTRACT_ADDRESS, big.NewInt(6_000_000_000), 0)
	for _, a := range env.evm[:3] {
		if err := cs.GetStakeStateDB().Delegate(env.nodeKP.Address(), a.addr, big.NewInt(2_000_000_000)); err != nil {
			t.Fatalf("pre-delegate: %v", err)
		}
	}
	// make the committee visible to the rollup attestation (committed trie), then unlock the stake DB again the way block
	// commit does, otherwise validator-contract writes fail with "db is locked"
	if _, err := cs.GetStakeStateDB().IntermediateRoot(); err != nil {
		t.Fatalf("stake intermediate root: %v", err)
	}
	if _, err := cs.GetStakeStateDB().Commit(); err != nil {
		t.Fatalf("stake commit: %v", err)
	}
	// destination chain 102 known to the gateway
	engine, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatal(err)
	}
	engine.ChainRegistry[102] = cross_chain.ChainRegistry{ChainID: 102, Epoch: 1,
		Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: env.nodeKP.BytesPublicKey(), Stake: 1000}}}
	// chains 103 and 104 (reserve), each with a single known committee key so that tests can sign real certs; chain 103
	// has a posted bond, and unbonding is immediate, so unregister followed by claimUnbondedBond pays out in one block
	for id, kp := range map[uint64]*bls.KeyPair{103: env.kp103, 104: env.kpR} {
		engine.ChainRegistry[id] = cross_chain.ChainRegistry{ChainID: id, Epoch: 1, GenesisWallet: env.users[1],
			Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000}}}
	}
	engine.ReserveChainID = 104
	engine.UnbondingPeriodSeconds = 0
	engine.MinNativeStakeToRegister = big.NewInt(1000)
	// a relayer tip for every barrier sender and an unbonding request for chain 102 that is already releasable
	engine.RelayerBalances = map[common.Address]*big.Int{}
	for _, a := range env.evm {
		engine.RelayerBalances[a.addr] = big.NewInt(12_345)
	}
	engine.SecurityBond = &cross_chain.SecurityBondLedger{Bond: map[uint64]*big.Int{103: big.NewInt(500)}, UnbondingRequests: map[uint64]*cross_chain.UnbondingRequest{}}
	engine.SecurityBond.UnbondingRequests[102] = &cross_chain.UnbondingRequest{Amount: big.NewInt(777), GenesisWallet: env.users[0], ReleaseAt: 0}
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatal(err)
	}
	prev := rollupSystemHandlerInstance
	rollupSystemHandlerInstance = &RollupSystemHandler{dispatcher: &MockRollupSystemDispatcher{}, registryHandler: rollup.NewAccountRegistryHandler(diffBarrierCluster)}
	t.Cleanup(func() { rollupSystemHandlerInstance = prev })

	return env, cs
}

func barrierRun(t *testing.T, seed int64, mode string) (barrierOutcome, []mt_types.Transaction) {
	t.Helper()
	env, cs := barrierSetup(t)

	txs := barrierBlock(t, rand.New(rand.NewSource(seed)), env)
	idx := map[common.Hash]int{}
	items := make([]grouptxns.Item, len(txs))
	for i, tx := range txs {
		idx[tx.Hash()] = i
		items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}
	}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
	var rcps []mt_types.Receipt
	flat := make([]mt_types.Transaction, 0, len(txs))
	for _, g := range groups {
		for _, it := range g.Items {
			flat = append(flat, it.Tx)
		}
	}
	switch mode {
	case "seq":
		// the oracle: every tx is its own block, so nothing can run speculatively or out of order
		for _, tx := range flat {
			_, one, _, _ := NewTrueBlockSTM([]mt_types.Transaction{tx}).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
			rcps = append(rcps, one...)
		}
	case "stm":
		_, rcps, _, _ = NewTrueBlockSTM(flat).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
	default:
		_, rcps, _, _ = ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, diffLeader, true)
	}

	var out barrierOutcome
	watch := []common.Address{diffLeader, barrierFunder, env.nodeKP.Address(), mt_common.VALIDATOR_CONTRACT_ADDRESS, mt_common.GATEWAY_CONTRACT_ADDRESS}
	watch = append(watch, env.users...)
	for _, a := range append(append([]diffAccount{}, env.native...), env.evm...) {
		watch = append(watch, a.addr)
	}
	for i := 0; i < 4; i++ {
		watch = append(watch, common.BigToAddress(big.NewInt(int64(0xF000+i))))
	}
	for _, a := range watch {
		st, err := cs.GetAccountStateDB().AccountState(a)
		if err != nil {
			t.Fatalf("account state: %v", err)
		}
		out.accounts = append(out.accounts, fmt.Sprintf("%s bal=%s nonce=%d parentReg=%v", a.Hex(), st.TotalBalance(), st.Nonce(), st.ParentRegistered()))
	}
	sort.Strings(out.accounts)
	for _, rc := range rcps {
		if rc == nil || fmt.Sprintf("%v", rc) == "<nil>" {
			continue
		}
		out.receipts = append(out.receipts, fmt.Sprintf("tx#%02d status=%v gas=%d", idx[rc.TransactionHash()], rc.Status(), rc.GasUsed()))
	}
	sort.Strings(out.receipts)
	if err := cs.GetSmartContractDB().LateBindRoots(); err != nil {
		t.Fatalf("LateBindRoots: %v", err)
	}
	accRoot, err := cs.GetAccountStateDB().IntermediateRoot(true)
	if err != nil {
		t.Fatalf("account root: %v", err)
	}
	stakeRoot, err := cs.GetStakeStateDB().IntermediateRoot()
	if err != nil {
		t.Fatalf("stake root: %v", err)
	}
	count, _ := cs.GetStakeStateDB().GetValidatorCount()
	out.roots = fmt.Sprintf("accountRoot=%s stakeRoot=%s validators=%d", accRoot.Hex(), stakeRoot.Hex(), count)
	return out, txs
}

func TestExecDifferential_ValidBarrierTxs_OptimisticVsTrueBlockSTMOnly(t *testing.T) {
	const iterations = 200
	vh0, _ := GetValidatorHandler()
	gh0, _ := GetGatewayHandler()
	diverged := 0
	okRcp := map[string]int{}
	for seed := int64(1); seed <= iterations; seed++ {
		oracle, txs := barrierRun(t, seed, "seq")
		opt, _ := barrierRun(t, seed, "opt")
		for _, line := range oracle.receipts {
			var idx int
			var status string
			if _, err := fmt.Sscanf(strings.Replace(line, "status=", "", 1), "tx#%d %s", &idx, &status); err == nil && idx < len(txs) {
				kind := map[common.Address]string{mt_common.VALIDATOR_CONTRACT_ADDRESS: "validator", mt_common.GATEWAY_CONTRACT_ADDRESS: "gateway", rollup.RollupSystemAddress: "rollup"}[txs[idx].ToAddress()]
				if kind == "gateway" {
					if in := txs[idx].CallData().Input(); len(in) >= 4 {
						if m, err := gh0.abi.MethodById(in[:4]); err == nil && m.Name != "outbound" {
							kind = "gateway." + m.Name
						}
					}
				}
				if kind == "validator" {
					if in := txs[idx].CallData().Input(); len(in) >= 4 {
						if m, err := vh0.abi.MethodById(in[:4]); err == nil {
							kind = "validator." + m.Name
						}
					}
				}
				if kind != "" {
					okRcp[kind+"/"+status]++
				}
			}
		}
		want := strings.Join(oracle.accounts, "\n") + "|" + strings.Join(oracle.receipts, "\n") + "|" + oracle.roots
		key := func(o barrierOutcome) string {
			return strings.Join(o.accounts, "\n") + "|" + strings.Join(o.receipts, "\n") + "|" + o.roots
		}
		bad := false
		if key(opt) != want {
			bad = true
			if diverged < 2 {
				t.Errorf("seed %d: ProcessTransactionsOptimistic differs from the sequential oracle\n%s oracle:     %s\n optimistic:  %s\n oracle receipts: %v\n opt receipts:    %v",
					seed, describeDiffBlock(txs), oracle.roots, opt.roots, oracle.receipts, opt.receipts)
			}
		}
		for rep := 0; rep < 3; rep++ {
			stm, _ := barrierRun(t, seed, "stm")
			if key(stm) != want {
				bad = true
				if diverged < 2 {
					t.Errorf("seed %d run %d: TrueBlockSTM differs from the sequential oracle\n%s oracle: %s\n stm:    %s", seed, rep, describeDiffBlock(txs), oracle.roots, stm.roots)
				}
				break
			}
		}
		if bad {
			diverged++
		}
	}
	if diverged > 0 {
		t.Errorf("%d of %d barrier blocks differed from the sequential oracle", diverged, iterations)
	}
	t.Logf("barrier receipts across all blocks: %v", okRcp)
	// the test is only meaningful if each barrier address actually executed successfully somewhere
	for _, kind := range []string{"validator.registerValidator", "validator.deregisterValidator", "validator.setCommissionRate", "validator.delegate", "validator.undelegate", "validator.updateValidatorInfo", "validator.withdrawReward", "gateway", "gateway.postSecurityBond", "gateway.withdrawRelayerTip", "gateway.claimUnbondedBond", "gateway.allocateSupplyWithCert", "gateway.transferAllocationWithCert", "gateway.unregisterChainWithCert", "gateway.registerAssetWithCert", "gateway.registerChainViaStake", "rollup"} {
		if okRcp[kind+"/RETURNED"] == 0 {
			t.Errorf("no successful %s barrier tx in %d blocks: the fixtures no longer exercise the valid path", kind, iterations)
		}
	}
}
