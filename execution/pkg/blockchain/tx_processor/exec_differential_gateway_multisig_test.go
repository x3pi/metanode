package tx_processor

import (
	"bytes"
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Differential test with committees of several members: aggregated BLS signatures, signer bitmaps, stake thresholds (the
// default 2f+1 for chain 105, a custom 75% for chain 106 with uneven stakes). Subsets that fall short of the threshold, and
// bitmaps that claim a signer who did not sign, keep the rejection paths in the mix.

type msMember struct {
	kp    *bls.KeyPair
	stake uint64
}

// tenMembers is a committee of ten (so the signer bitmap needs two bytes), 100 stake each.
func tenMembers(mk func(uint64, int) *bls.KeyPair) []msMember {
	var out []msMember
	for i := 0; i < 10; i++ {
		out = append(out, msMember{mk(107, i), 100})
	}
	return out
}

// multisigCommittees returns the committees of chain 105 (4 members, 1000 each) and 106 (3 members, 1000/2000/3000).
func multisigCommittees() map[uint64][]msMember {
	mk := func(chain uint64, i int) *bls.KeyPair {
		return bls.NewKeyPair(bytes.Repeat([]byte{byte(chain), byte(0x60 + i)}, 16))
	}
	return map[uint64][]msMember{
		105: {{mk(105, 0), 1000}, {mk(105, 1), 1000}, {mk(105, 2), 1000}, {mk(105, 3), 1000}},
		106: {{mk(106, 0), 1000}, {mk(106, 1), 2000}, {mk(106, 2), 3000}},
		107: tenMembers(mk),
	}
}

func multisigTweak(t *testing.T, env barrierEnv, cs *blockchain.ChainState) {
	t.Helper()
	seedGatewayLedger(t, env, cs)
	engine, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatal(err)
	}
	for id, members := range multisigCommittees() {
		reg := cross_chain.ChainRegistry{ChainID: id, Epoch: 1, GenesisWallet: env.users[2]}
		for _, m := range members {
			reg.Committee = append(reg.Committee, cross_chain.ValidatorEntry{PubkeyBLS: m.kp.BytesPublicKey(), Stake: m.stake,
				PopSignature: cross_chain.PopSign(m.kp.PrivateKey(), m.kp.PublicKey()).Bytes()})
		}
		if id == 106 {
			reg.QuorumThreshold = 7500
		}
		engine.ChainRegistry[id] = reg
		engine.SecurityBond.Bond[id] = big.NewInt(700)
		// the chain's allocation comes out of the reserve's, so the ledger invariant keeps holding
		if err := engine.SupplyLedger.TransferAllocation(reserveChainID, id, big.NewInt(500_000)); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatal(err)
	}
}

func multisigBlock(t *testing.T, r *rand.Rand, env barrierEnv) []mt_types.Transaction {
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
	committees := multisigCommittees()
	nonces := map[common.Address]uint64{}
	next := func(a common.Address) uint64 { n := nonces[a]; nonces[a]++; return n }
	var txs []mt_types.Transaction
	add := func(from common.Address, data []byte, i int) {
		txs = append(txs, transaction.NewTransaction(from, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), 400_000, 1, uint64(9300+len(txs)+i), data, nil,
			common.Hash{}, common.Hash{}, next(from), 991))
	}
	// cert signs digest with a random non-empty subset of the chain's committee; the bitmap usually matches the signers
	cert := func(chain uint64, digest []byte) (signers [][]byte, aggSig, bitmap []byte) {
		members := committees[chain]
		var sigs [][]byte
		bm := make([]byte, (len(members)+7)/8)
		for i, m := range members {
			if r.Intn(4) != 0 || (i == len(members)-1 && len(sigs) == 0) {
				sigs = append(sigs, bls.Sign(m.kp.PrivateKey(), digest).Bytes())
				signers = append(signers, m.kp.BytesPublicKey())
				bm[i/8] |= 1 << uint(i%8)
			}
		}
		if r.Intn(10) == 0 { // the bitmap claims a member who did not sign
			k := r.Intn(len(members))
			bm[k/8] |= 1 << uint(k%8)
		}
		return signers, bls.CreateAggregateSign(sigs), bm
	}
	chains := []uint64{105, 106, 107}
	for i, n := 0, 3+r.Intn(8); i < n; i++ {
		from := env.evm[r.Intn(len(env.evm))].addr
		chain := chains[r.Intn(len(chains))]
		switch r.Intn(6) {
		case 0, 1: // commit attested by the chain's committee (ceiling-checked), then claimed into the reserve
			msg := cross_chain.CrossChainMessage{
				MessageID:     common.BigToHash(big.NewInt(int64(0xA500 + r.Intn(6)))),
				SourceChainID: chain, DestChainID: reserveChainID, Sequence: uint64(1 + r.Intn(3)), HopCount: 1, Sender: from,
				Target: env.users[r.Intn(len(env.users))], AssetID: big.NewInt(0), Value: big.NewInt(int64(1 + r.Intn(300))), Tip: big.NewInt(0), GasFee: big.NewInt(0),
			}
			root, layers, agg, aggIdx, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
			if err != nil {
				t.Fatal(err)
			}
			mp, ap := cross_chain.GetMerkleProof(layers, 0), cross_chain.GetMerkleProof(layers, aggIdx["0"])
			_, sig, bm := cert(chain, cross_chain.ComputeCommitRootAttestMessage(root))
			add(from, pack(gh.abi.Pack("attestCommit", big.NewInt(int64(chain)), root, agg["0"], big.NewInt(0), new(big.Int).SetUint64(ap.LeafIndex),
				hashesToBytes32(ap.Siblings), uint64(1), sig, bm)), i)
			add(from, pack(gh.abi.Pack("claimMessage", msg.MessageID, big.NewInt(int64(chain)), big.NewInt(int64(reserveChainID)), big.NewInt(int64(msg.Sequence)),
				msg.HopCount, msg.Sender, msg.Target, msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered, uint64(0),
				new(big.Int).SetUint64(mp.LeafIndex), hashesToBytes32(mp.Siblings), root)), i)
		case 2: // the chain leaves; its bond goes into unbonding
			nonce := uint64(r.Intn(2))
			_, sig, bm := cert(chain, cross_chain.ComputeUnregisterChainMessage(chain, 1, nonce))
			add(from, pack(gh.abi.Pack("unregisterChainWithCert", big.NewInt(int64(chain)), nonce, uint64(1), sig, bm)), i)
		case 3: // equivocation: two certs over different roots, each from its own subset of the committee
			a, b := common.BigToHash(big.NewInt(0xA1)), common.BigToHash(big.NewInt(0xB2))
			_, sigA, bmA := cert(chain, cross_chain.ComputeCommitRootAttestMessage(a))
			_, sigB, bmB := cert(chain, cross_chain.ComputeCommitRootAttestMessage(b))
			add(from, pack(gh.abi.Pack("slashOnEquivocation", big.NewInt(int64(chain)), uint64(1), a, b, uint64(1), sigA, bmA, uint64(1), sigB, bmB)), i)
		case 4: // allocation move signed by the chain's committee
			amt := big.NewInt(int64(1 + r.Intn(1000)))
			nonce := uint64(r.Intn(2))
			_, sig, bm := cert(chain, cross_chain.ComputeTransferAllocationMessage(chain, 102, amt, nonce))
			add(from, pack(gh.abi.Pack("transferAllocationWithCert", big.NewInt(int64(chain)), big.NewInt(102), amt, nonce, uint64(1), sig, bm)), i)
		default: // committee rotation: PoP of the new key first, then the update signed by a subset of the old committee
			seedNew := bytes.Repeat([]byte{0x07, byte(chain)}, 16)
			kpNew := bls.NewKeyPair(seedNew)
			pop := cross_chain.PopSign(kpNew.PrivateKey(), kpNew.PublicKey()).Bytes()
			add(from, pack(gh.abi.Pack("registerCommitteePop", kpNew.BytesPublicKey(), pop)), i)
			state, acct := common.BigToHash(big.NewInt(0x61)), common.BigToHash(big.NewInt(0x62))
			committee := []cross_chain.ValidatorEntry{{PubkeyBLS: kpNew.BytesPublicKey(), Stake: 1000, PopSignature: pop}}
			digest := cross_chain.ComputeCommitteeUpdateDigest(chain, 2, committee, state, acct)
			signers, sig, _ := cert(chain, digest.Bytes())
			add(from, pack(gh.abi.Pack("committeeUpdate", big.NewInt(int64(chain)), uint64(2), [][]byte{kpNew.BytesPublicKey()}, []uint64{1000}, [][]byte{pop},
				uint64(0), state, acct, digest, signers, sig)), i)
		}
	}
	return txs
}

func TestExecDifferential_GatewayMultisigFlows_OptimisticVsTrueBlockSTMOnly(t *testing.T) {
	runGatewayDifferential(t, 200, multisigBlock, multisigTweak,
		[]string{"attestCommit", "claimMessage", "unregisterChainWithCert", "slashOnEquivocation", "transferAllocationWithCert", "committeeUpdate"})
}
