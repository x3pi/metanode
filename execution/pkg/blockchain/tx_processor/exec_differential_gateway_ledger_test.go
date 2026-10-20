package tx_processor

import (
	"fmt"
	"math/big"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Differential test for the gateway paths that need a funded supply ledger on the reserve chain (the local chain here):
// attestCommit with the ceiling check, claimMessage into the reserve, creditReserveAllocation and refundReserveAllocation
// for a 2-hop message 103 -> 102, and transferAllocationWithCert. The ledger is pre-seeded (genesis supply minted), which the
// main barrier test cannot do because its allocateSupplyWithCert needs an empty ledger. Same oracle as the main test.

func seedGatewayLedger(t *testing.T, _ barrierEnv, cs *blockchain.ChainState) {
	t.Helper()
	engine, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10_000_000), map[uint64]*big.Int{
		reserveChainID: big.NewInt(6_000_000), 103: big.NewInt(3_000_000), 102: big.NewInt(1_000_000)})
	if err != nil {
		t.Fatal(err)
	}
	engine.SupplyLedger = ledger
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatal(err)
	}
}

func ledgerBlock(t *testing.T, r *rand.Rand, env barrierEnv) []mt_types.Transaction {
	gh, err := GetGatewayHandler()
	if err != nil {
		t.Fatal(err)
	}
	pack := func(in []byte, err error) []byte {
		if err != nil {
			t.Fatalf("pack: %v", err)
		}
		d, err := transaction.NewCallData(in).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	nonces := map[common.Address]uint64{}
	next := func(a common.Address) uint64 { n := nonces[a]; nonces[a]++; return n }
	var txs []mt_types.Transaction
	add := func(from common.Address, data []byte, i int) {
		txs = append(txs, transaction.NewTransaction(from, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), 400_000, 1, uint64(8700+len(txs)+i), data, nil,
			common.Hash{}, common.Hash{}, next(from), 991))
	}
	sign := func(kp *bls.KeyPair, digest []byte) []byte { return bls.Sign(kp.PrivateKey(), digest).Bytes() }
	for i, n := 0, 3+r.Intn(8); i < n; i++ {
		from := env.evm[r.Intn(len(env.evm))].addr
		if r.Intn(6) == 0 { // allocation move out of the reserve, signed by the reserve committee
			amt := big.NewInt(int64(1 + r.Intn(1000)))
			nonce := uint64(r.Intn(2))
			add(from, pack(gh.abi.Pack("transferAllocationWithCert", big.NewInt(int64(reserveChainID)), big.NewInt(103), amt, nonce, uint64(1),
				sign(env.kpR, cross_chain.ComputeTransferAllocationMessage(reserveChainID, 103, amt, nonce)), []byte{0x01})), i)
			continue
		}
		// a message from chain 103; 2-hop (dest 102) or into the reserve itself
		dest := []uint64{102, reserveChainID}[r.Intn(2)]
		msg := cross_chain.CrossChainMessage{
			MessageID:     common.BigToHash(big.NewInt(int64(0xD100 + r.Intn(6)))),
			SourceChainID: 103, DestChainID: dest, Sequence: uint64(1 + r.Intn(3)), HopCount: 1, Sender: from,
			Target: env.users[r.Intn(len(env.users))], AssetID: big.NewInt(0), Value: big.NewInt(int64(1 + r.Intn(300))),
			Tip: big.NewInt(0), GasFee: big.NewInt(0),
		}
		if dest == reserveChainID && r.Intn(3) == 0 {
			// 2-hop A -> reserve -> B: the relay marker asks the reserve to forward the value onward instead of settling it here;
			// an unregistered or self-referential final destination must be refused
			msg.Payload = cross_chain.EncodeRelayPayload([]uint64{102, 102, 102, 103, 999, reserveChainID}[r.Intn(6)], []byte{0xBE, byte(i)})
		}
		root, layers, agg, aggIdx, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
		if err != nil {
			t.Fatal(err)
		}
		mp, ap := cross_chain.GetMerkleProof(layers, 0), cross_chain.GetMerkleProof(layers, aggIdx["0"])
		if dest == reserveChainID && r.Intn(5) == 0 { // attest + claim in one call, signed by chain 103's committee
			add(from, pack(gh.abi.Pack("verifyAndExecute", msg.MessageID, big.NewInt(103), big.NewInt(int64(dest)), big.NewInt(int64(msg.Sequence)), msg.HopCount,
				msg.Sender, msg.Target, msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
				new(big.Int).SetUint64(ap.LeafIndex), hashesToBytes32(ap.Siblings), new(big.Int).SetUint64(mp.LeafIndex), hashesToBytes32(mp.Siblings), root,
				uint64(1), sign(env.kp103, cross_chain.ComputeCommitRootAttestMessage(root)), []byte{0x01})), i)
			continue
		}
		if r.Intn(6) != 0 { // the attestation (ceiling-checked, debits chain 103), signed by chain 103's committee
			add(from, pack(gh.abi.Pack("attestCommit", big.NewInt(103), root, agg["0"], big.NewInt(0), new(big.Int).SetUint64(ap.LeafIndex),
				hashesToBytes32(ap.Siblings), uint64(1), sign(env.kp103, cross_chain.ComputeCommitRootAttestMessage(root)), []byte{0x01})), i)
		}
		claimFrom := from
		if r.Intn(4) == 0 {
			claimFrom = env.evm[r.Intn(len(env.evm))].addr
		}
		fields := func() []interface{} {
			return []interface{}{msg.MessageID, big.NewInt(103), big.NewInt(int64(dest)), big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender,
				msg.Target, msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered}
		}
		proof := []interface{}{new(big.Int).SetUint64(mp.LeafIndex), hashesToBytes32(mp.Siblings), root}
		switch {
		case dest == reserveChainID:
			args := append(append(fields(), uint64(0)), proof...)
			add(claimFrom, pack(gh.abi.Pack("claimMessage", args...)), i)
		case r.Intn(2) == 0: // success certificate from the destination committee
			cert := []interface{}{uint64(1), sign(env.nodeKP, cross_chain.ComputeMessageSuccessAttestMessage(msg.MessageID, 102)), []byte{0x01}}
			add(claimFrom, pack(gh.abi.Pack("creditReserveAllocation", append(append(fields(), proof...), cert...)...)), i)
		default: // failure certificate from the destination committee
			cert := []interface{}{uint64(1), sign(env.nodeKP, cross_chain.ComputeMessageFailureAttestMessage(msg.MessageID, 102)), []byte{0x01}}
			add(claimFrom, pack(gh.abi.Pack("refundReserveAllocation", append(append(fields(), proof...), cert...)...)), i)
		}
	}
	return txs
}

// runGatewayDifferential compares ProcessTransactionsOptimistic and TrueBlockSTM against the one-tx-per-block oracle on
// blocks from gen, and insists that every method in required returned successfully at least once across the run.
func runGatewayDifferential(t *testing.T, iterations int64, gen func(*testing.T, *rand.Rand, barrierEnv) []mt_types.Transaction,
	tweak func(*testing.T, barrierEnv, *blockchain.ChainState), required []string) {
	t.Helper()
	gh0, _ := GetGatewayHandler()
	diverged := 0
	okRcp := map[string]int{}
	key := func(o barrierOutcome) string {
		return strings.Join(o.accounts, "\n") + "|" + strings.Join(o.receipts, "\n") + "|" + o.roots
	}
	for seed := int64(1); seed <= iterations; seed++ {
		oracle, txs := barrierRunWith(t, seed, "seq", gen, tweak)
		for _, line := range oracle.receipts {
			var idx int
			var status string
			if _, err := fmt.Sscanf(strings.Replace(line, "status=", "", 1), "tx#%d %s", &idx, &status); err == nil && idx < len(txs) {
				name := "?"
				if in := txs[idx].CallData().Input(); len(in) >= 4 {
					if m, err := gh0.abi.MethodById(in[:4]); err == nil {
						name = m.Name
					}
				}
				okRcp[name+"/"+status]++
			}
		}
		want := key(oracle)
		opt, _ := barrierRunWith(t, seed, "opt", gen, tweak)
		bad := key(opt) != want
		if bad && diverged < 2 {
			t.Errorf("seed %d: ProcessTransactionsOptimistic differs from the sequential oracle\n%s oracle: %s\n opt:    %s", seed, describeDiffBlock(txs), oracle.roots, opt.roots)
		}
		for rep := 0; rep < 3 && !bad; rep++ {
			stm, _ := barrierRunWith(t, seed, "stm", gen, tweak)
			if key(stm) != want {
				bad = true
				if diverged < 2 {
					t.Errorf("seed %d run %d: TrueBlockSTM differs from the sequential oracle\n%s oracle: %s\n stm:    %s", seed, rep, describeDiffBlock(txs), oracle.roots, stm.roots)
				}
			}
		}
		if bad {
			diverged++
		}
	}
	if diverged > 0 {
		t.Errorf("%d of %d blocks differed from the sequential oracle", diverged, iterations)
	}
	var keys []string
	for k := range okRcp {
		keys = append(keys, fmt.Sprintf("%s:%d", k, okRcp[k]))
	}
	sort.Strings(keys)
	t.Logf("receipts across all blocks: %v", keys)
	for _, kind := range required {
		if okRcp[kind+"/RETURNED"] == 0 {
			t.Errorf("no successful %s tx in %d blocks: the fixtures no longer exercise the valid path", kind, iterations)
		}
	}
}

func TestExecDifferential_GatewayLedgerFlows_OptimisticVsTrueBlockSTMOnly(t *testing.T) {
	runGatewayDifferential(t, 200, ledgerBlock, seedGatewayLedger,
		[]string{"attestCommit", "claimMessage", "creditReserveAllocation", "refundReserveAllocation", "transferAllocationWithCert", "verifyAndExecute"})
}
