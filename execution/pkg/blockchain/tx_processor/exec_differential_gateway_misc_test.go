package tx_processor

import (
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

// Differential test for the gateway methods that only record attestations, checkpoints, digests and PoPs, or flip a chain to
// dead: registerCommitteePop, submit{Committee,Commit,MessageFailure,MessageSuccess}Attestation, submitCheckpoint,
// setGenesisDigest, slashOnEquivocation and batchOutboundCommit. All signatures are real BLS signatures of the single-member
// committees of chains 102 (node key), 103 and the reserve; repeats, stale epochs and wrong signers keep the error paths in the mix.

// deadChainAccounts are the accounts in chain 103's account tree (claimDeadChainBalance proves a leaf against its root).
func deadChainAccounts(env barrierEnv) []cross_chain.AccountLeaf {
	var out []cross_chain.AccountLeaf
	for i, u := range env.users {
		out = append(out, cross_chain.AccountLeaf{Account: u, Balance: big.NewInt(int64(100 * (i + 1)))})
	}
	return out
}

// refundMessage k is an outbound message of the local chain to chain 102 whose batch is already committed, so refund() can
// be proven against it.
func refundMessage(env barrierEnv, k int) cross_chain.CrossChainMessage {
	return cross_chain.CrossChainMessage{
		MessageID:     common.BigToHash(big.NewInt(int64(0xF100 + k))),
		SourceChainID: reserveChainID, DestChainID: 102, Sequence: uint64(1 + k), HopCount: 1, Sender: env.users[k%len(env.users)],
		Target: env.users[(k+1)%len(env.users)], AssetID: big.NewInt(0), Value: big.NewInt(int64(50 + k)), Tip: big.NewInt(0), GasFee: big.NewInt(0),
	}
}

// miscTweak makes env.evm[0] the genesis wallet of chain 103 (so setGenesisDigest can succeed for that sender), publishes
// chain 103's account tree root and commits the batches that refund() proves against.
func miscTweak(t *testing.T, env barrierEnv, cs *blockchain.ChainState) {
	t.Helper()
	engine, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatal(err)
	}
	reg := engine.ChainRegistry[103]
	reg.GenesisWallet = env.evm[0].addr
	var leaves []common.Hash
	for _, a := range deadChainAccounts(env) {
		leaves = append(leaves, cross_chain.HashAccountLeaf(a))
	}
	reg.AccountTreeRoot, _ = cross_chain.BuildMerkleTree(leaves)
	if engine.CommittedBatches == nil {
		engine.CommittedBatches = map[common.Hash]cross_chain.CommittedOutboundBatch{}
	}
	for k := 0; k < 4; k++ {
		msgs := []cross_chain.CrossChainMessage{refundMessage(env, k)}
		root, _, _, _, err := cross_chain.BuildCommitTree(msgs)
		if err != nil {
			t.Fatal(err)
		}
		engine.CommittedBatches[root] = cross_chain.CommittedOutboundBatch{Messages: msgs, Epoch: 1}
	}
	engine.ChainRegistry[103] = reg
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatal(err)
	}
}

func miscBlock(t *testing.T, r *rand.Rand, env barrierEnv) []mt_types.Transaction {
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
	sign := func(kp *bls.KeyPair, digest []byte) []byte { return bls.Sign(kp.PrivateKey(), digest).Bytes() }
	type member struct {
		chain uint64
		kp    *bls.KeyPair
	}
	members := []member{{102, env.nodeKP}, {103, env.kp103}, {reserveChainID, env.kpR}}
	for i, n := 0, 3+r.Intn(10); i < n; i++ {
		from := env.evm[r.Intn(len(env.evm))].addr
		m := members[r.Intn(len(members))]
		signer := m.kp
		if r.Intn(8) == 0 { // wrong signer for the chain
			signer = members[r.Intn(len(members))].kp
		}
		epoch := uint64(1)
		if r.Intn(8) == 0 {
			epoch = 2
		}
		hash := common.BigToHash(big.NewInt(int64(0xE000 + r.Intn(4))))
		var data []byte
		switch r.Intn(12) {
		case 0:
			pop := cross_chain.PopSign(signer.PrivateKey(), signer.PublicKey()).Bytes()
			data = pack(gh.abi.Pack("registerCommitteePop", signer.BytesPublicKey(), pop))
		case 1:
			data = pack(gh.abi.Pack("submitCommitteeAttestation", big.NewInt(int64(m.chain)), epoch, hash, signer.BytesPublicKey(), sign(signer, hash.Bytes())))
		case 2:
			data = pack(gh.abi.Pack("submitCommitAttestation", big.NewInt(int64(m.chain)), epoch, hash, signer.BytesPublicKey(),
				sign(signer, cross_chain.ComputeCommitRootAttestMessage(hash))))
		case 3:
			data = pack(gh.abi.Pack("submitMessageFailureAttestation", big.NewInt(int64(m.chain)), hash, epoch, signer.BytesPublicKey(),
				sign(signer, cross_chain.ComputeMessageFailureAttestMessage(hash, m.chain))))
		case 4:
			data = pack(gh.abi.Pack("submitMessageSuccessAttestation", big.NewInt(int64(m.chain)), hash, epoch, signer.BytesPublicKey(),
				sign(signer, cross_chain.ComputeMessageSuccessAttestMessage(hash, m.chain))))
		case 5: // checkpoints must be monotonic in block height, so racing ones decide by order
			height := uint64(1 + r.Intn(5))
			state := common.BigToHash(big.NewInt(int64(0xC000 + r.Intn(3))))
			vset := common.BigToHash(big.NewInt(int64(0xF000 + r.Intn(3))))
			data = pack(gh.abi.Pack("submitCheckpoint", big.NewInt(int64(m.chain)), uint64(1), height, state, vset, uint64(1),
				sign(signer, cross_chain.ComputeCheckpointMessage(m.chain, 1, height, state, vset)), []byte{0x01}))
		case 6: // only the genesis wallet (env.evm[0], chain 103) may publish the digest, and only once
			data = pack(gh.abi.Pack("setGenesisDigest", big.NewInt(103), common.BigToHash(big.NewInt(int64(0xD000+r.Intn(3))))))
		case 7: // two certs over different roots at the same epoch: the committee equivocated, the chain is declared dead
			a, b := common.BigToHash(big.NewInt(0xA1)), common.BigToHash(big.NewInt(0xB2+int64(r.Intn(2))))
			data = pack(gh.abi.Pack("slashOnEquivocation", big.NewInt(int64(m.chain)), uint64(1), a, b,
				uint64(1), sign(signer, cross_chain.ComputeCommitRootAttestMessage(a)), []byte{0x01},
				uint64(1), sign(signer, cross_chain.ComputeCommitRootAttestMessage(b)), []byte{0x01}))
		case 8: // rotate chain 103's committee to a fresh key: its PoP is registered first (same sender), the old committee signs
			seedNew := common.FromHex("0x2020202020202020202020202020202020202020202020202020202020202020")
			kpNew := bls.NewKeyPair(seedNew[:32])
			pop := cross_chain.PopSign(kpNew.PrivateKey(), kpNew.PublicKey()).Bytes()
			txs = append(txs, transaction.NewTransaction(from, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), 400_000, 1, uint64(9100+i),
				pack(gh.abi.Pack("registerCommitteePop", kpNew.BytesPublicKey(), pop)), nil, common.Hash{}, common.Hash{}, next(from), 991))
			newEpoch := uint64(2 + r.Intn(2)/1*r.Intn(2)) // mostly 2 (the valid next epoch), sometimes 3
			state, acct := common.BigToHash(big.NewInt(0x51)), common.BigToHash(big.NewInt(0x52))
			committee := []cross_chain.ValidatorEntry{{PubkeyBLS: kpNew.BytesPublicKey(), Stake: 1000, PopSignature: pop}}
			digest := cross_chain.ComputeCommitteeUpdateDigest(103, newEpoch, committee, state, acct)
			oldSigner := env.kp103
			if r.Intn(6) == 0 {
				oldSigner = env.nodeKP // not a member of chain 103's committee
			}
			data = pack(gh.abi.Pack("committeeUpdate", big.NewInt(103), newEpoch, [][]byte{kpNew.BytesPublicKey()}, []uint64{1000}, [][]byte{pop},
				uint64(0), state, acct, digest, [][]byte{oldSigner.BytesPublicKey()}, sign(oldSigner, digest.Bytes())))
		case 9: // claim a balance of the dead chain 103 against its account tree (only after slashOnEquivocation killed it)
			if r.Intn(2) == 0 { // kill chain 103 first (two certs over different roots at one epoch), same sender so it sorts first
				a, b := common.BigToHash(big.NewInt(0xA1)), common.BigToHash(big.NewInt(0xB2))
				slash := pack(gh.abi.Pack("slashOnEquivocation", big.NewInt(103), uint64(1), a, b,
					uint64(1), sign(env.kp103, cross_chain.ComputeCommitRootAttestMessage(a)), []byte{0x01},
					uint64(1), sign(env.kp103, cross_chain.ComputeCommitRootAttestMessage(b)), []byte{0x01}))
				txs = append(txs, transaction.NewTransaction(from, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), 400_000, 1, uint64(9200+i), slash, nil,
					common.Hash{}, common.Hash{}, next(from), 991))
			}
			accts := deadChainAccounts(env)
			var leaves []common.Hash
			for _, a := range accts {
				leaves = append(leaves, cross_chain.HashAccountLeaf(a))
			}
			_, layers := cross_chain.BuildMerkleTree(leaves)
			k := r.Intn(len(accts))
			amt := new(big.Int).Set(accts[k].Balance)
			if r.Intn(8) == 0 {
				amt.Add(amt, big.NewInt(1)) // wrong amount: leaf hash does not match
			}
			p := cross_chain.GetMerkleProof(layers, k)
			data = pack(gh.abi.Pack("claimDeadChainBalance", big.NewInt(103), accts[k].Account, amt, new(big.Int).SetUint64(p.LeafIndex),
				hashesToBytes32(p.Siblings), cross_chain.HashAccountLeaf(cross_chain.AccountLeaf{Account: accts[k].Account, Balance: amt})))
		case 10: // refund an outbound message of the local chain, with a failure cert from the destination (102) committee
			msg := refundMessage(env, r.Intn(4))
			root, layers, _, _, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
			if err != nil {
				t.Fatal(err)
			}
			mp := cross_chain.GetMerkleProof(layers, 0)
			data = pack(gh.abi.Pack("refund", msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
				big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target, msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
				new(big.Int).SetUint64(mp.LeafIndex), hashesToBytes32(mp.Siblings), root, uint64(1),
				sign(env.nodeKP, cross_chain.ComputeMessageFailureAttestMessage(msg.MessageID, 102)), []byte{0x01}))
		default: // queue an outbound message to chain 102 first (same sender, so it sorts first), then batch the queue
			if r.Intn(3) != 0 {
				out := pack(gh.abi.Pack("outbound", big.NewInt(102), env.users[r.Intn(len(env.users))], []byte{0xAA, byte(i)}, big.NewInt(0),
					big.NewInt(int64(1+r.Intn(500))), big.NewInt(int64(r.Intn(10))), big.NewInt(0), uint8(1), false, uint64(0)))
				txs = append(txs, transaction.NewTransaction(from, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), 400_000, 1, uint64(8900+i), out, nil,
					common.Hash{}, common.Hash{}, next(from), 991))
			}
			data = pack(gh.abi.Pack("batchOutboundCommit", big.NewInt(int64([]uint64{102, 102, 103}[r.Intn(3)]))))
		}
		txs = append(txs, transaction.NewTransaction(from, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), 400_000, 1, uint64(8800+i), data, nil,
			common.Hash{}, common.Hash{}, next(from), 991))
	}
	return txs
}

func TestExecDifferential_GatewayMiscFlows_OptimisticVsTrueBlockSTMOnly(t *testing.T) {
	runGatewayDifferential(t, 200, miscBlock, func(t *testing.T, env barrierEnv, cs *blockchain.ChainState) {
		seedGatewayLedger(t, env, cs)
		miscTweak(t, env, cs)
	}, []string{"registerCommitteePop", "submitCommitteeAttestation", "submitCommitAttestation", "submitMessageFailureAttestation",
		"submitMessageSuccessAttestation", "submitCheckpoint", "setGenesisDigest", "slashOnEquivocation", "batchOutboundCommit", "committeeUpdate", "claimDeadChainBalance", "refund"})
}
