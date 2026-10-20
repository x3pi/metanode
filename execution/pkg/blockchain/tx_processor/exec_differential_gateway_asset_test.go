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

// Differential test for custom assets (asset id != 0), where the gateway moves real ERC-20-shaped tokens through the EVM:
//   - asset 7, home chain 103, wrapped on the local chain: attestCommit + claimMessage mint the wrapped token;
//   - asset 8, home chain = the local chain: approve + outbound lock tokens in the gateway, refund releases them.
// Token balances live in contract storage, so a divergence shows up in the account root.

var assetTokens struct{ wrapped7, home8 common.Address }

const (
	wrappedAssetID = 7
	homeAssetID    = 8
)

func assetTweak(t *testing.T, env barrierEnv, cs *blockchain.ChainState) {
	t.Helper()
	seedGatewayLedger(t, env, cs)
	assetTokens.wrapped7 = deployTestWrappedAsset(t, cs, env.evm[1].addr, big.NewInt(0))
	assetTokens.home8 = deployTestWrappedAsset(t, cs, env.evm[0].addr, big.NewInt(1_000_000_000))
	engine, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatal(err)
	}
	engine.EnsureAssetRegistry()
	engine.AssetRegistry.Assets["7"] = &cross_chain.AssetEntry{AssetID: big.NewInt(wrappedAssetID), Active: true, HomeChainID: 103,
		CanonicalContract: common.BigToAddress(big.NewInt(0xCA70)), WrappedContracts: map[uint64]common.Address{reserveChainID: assetTokens.wrapped7}}
	engine.AssetRegistry.Assets["8"] = &cross_chain.AssetEntry{AssetID: big.NewInt(homeAssetID), Active: true, HomeChainID: reserveChainID,
		CanonicalContract: assetTokens.home8, WrappedContracts: map[uint64]common.Address{102: common.BigToAddress(big.NewInt(0xCA80))}}
	if engine.CommittedBatches == nil {
		engine.CommittedBatches = map[common.Hash]cross_chain.CommittedOutboundBatch{}
	}
	for k := 0; k < 3; k++ {
		msgs := []cross_chain.CrossChainMessage{assetRefundMessage(env, k)}
		root, _, _, _, err := cross_chain.BuildCommitTree(msgs)
		if err != nil {
			t.Fatal(err)
		}
		engine.CommittedBatches[root] = cross_chain.CommittedOutboundBatch{Messages: msgs, Epoch: 1}
	}
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatal(err)
	}
}

// assetRefundMessage k: tokens of asset 8 that evm[0] locked in an outbound message to chain 102 whose batch is committed.
func assetRefundMessage(env barrierEnv, k int) cross_chain.CrossChainMessage {
	return cross_chain.CrossChainMessage{
		MessageID:     common.BigToHash(big.NewInt(int64(0xF800 + k))),
		SourceChainID: reserveChainID, DestChainID: 102, Sequence: uint64(1 + k), HopCount: 1, Sender: env.evm[0].addr,
		Target: env.users[k%len(env.users)], AssetID: big.NewInt(homeAssetID), Value: big.NewInt(int64(100 + k)), Tip: big.NewInt(0), GasFee: big.NewInt(0),
	}
}

func assetBlock(t *testing.T, r *rand.Rand, env barrierEnv) []mt_types.Transaction {
	gh, err := GetGatewayHandler()
	if err != nil {
		t.Fatal(err)
	}
	tokenABI := testWrappedAssetABI(t)
	wrap := func(in []byte, err error) []byte {
		if err != nil {
			t.Fatalf("pack: %v", err)
		}
		d, err := transaction.NewCallData(in).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	// the deployers of the two tokens already spent nonce 0 on the deployment
	nonces := map[common.Address]uint64{env.evm[0].addr: 1, env.evm[1].addr: 1}
	next := func(a common.Address) uint64 { n := nonces[a]; nonces[a]++; return n }
	var txs []mt_types.Transaction
	add := func(from, to common.Address, data []byte, i int) {
		txs = append(txs, transaction.NewTransaction(from, to, big.NewInt(0), 600_000, 1, uint64(9600+len(txs)+i), data, nil,
			common.Hash{}, common.Hash{}, next(from), 991))
	}
	holder := env.evm[0].addr // holds the whole supply of token 8
	for i, n := 0, 3+r.Intn(7); i < n; i++ {
		switch r.Intn(5) {
		case 0: // approve + lock tokens of asset 8 in an outbound message
			approve, err := tokenABI.Pack("approve", mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(int64(100+r.Intn(5000))))
			if err != nil {
				t.Fatal(err)
			}
			add(holder, assetTokens.home8, wrap(approve, nil), i)
			add(holder, mt_common.GATEWAY_CONTRACT_ADDRESS, wrap(gh.abi.Pack("outbound", big.NewInt(102), env.users[r.Intn(len(env.users))], []byte{0xAB, byte(i)},
				big.NewInt(homeAssetID), big.NewInt(int64(1+r.Intn(3000))), big.NewInt(0), big.NewInt(0), uint8(1), false, uint64(0))), i)
		case 1: // outbound without (enough) allowance
			add(holder, mt_common.GATEWAY_CONTRACT_ADDRESS, wrap(gh.abi.Pack("outbound", big.NewInt(102), env.users[0], []byte{0xAC},
				big.NewInt(homeAssetID), big.NewInt(int64(1+r.Intn(10_000))), big.NewInt(0), big.NewInt(0), uint8(1), false, uint64(0))), i)
		case 2: // release the locked tokens of a committed message after a failure cert from chain 102
			msg := assetRefundMessage(env, r.Intn(3))
			root, layers, _, _, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
			if err != nil {
				t.Fatal(err)
			}
			mp := cross_chain.GetMerkleProof(layers, 0)
			sig := bls.Sign(env.nodeKP.PrivateKey(), cross_chain.ComputeMessageFailureAttestMessage(msg.MessageID, 102)).Bytes()
			from := env.evm[r.Intn(len(env.evm))].addr
			add(from, mt_common.GATEWAY_CONTRACT_ADDRESS, wrap(gh.abi.Pack("refund", msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
				big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target, msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
				new(big.Int).SetUint64(mp.LeafIndex), hashesToBytes32(mp.Siblings), root, uint64(1), sig, []byte{0x01})), i)
		default: // wrapped asset 7 coming from chain 103: attest the commit, then claim (mints the wrapped token to the recipient)
			from := env.evm[1+r.Intn(len(env.evm)-1)].addr
			recipient := env.users[r.Intn(len(env.users))]
			msg := cross_chain.CrossChainMessage{
				MessageID:     common.BigToHash(big.NewInt(int64(0xA700 + r.Intn(6)))),
				SourceChainID: 103, DestChainID: reserveChainID, Sequence: uint64(1 + r.Intn(3)), HopCount: 1, Sender: from,
				Target: assetTokens.wrapped7, AssetID: big.NewInt(wrappedAssetID), Value: big.NewInt(int64(1 + r.Intn(500))),
				Payload: recipient.Bytes(), Tip: big.NewInt(0), GasFee: big.NewInt(0),
			}
			root, layers, agg, aggIdx, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
			if err != nil {
				t.Fatal(err)
			}
			mp, ap := cross_chain.GetMerkleProof(layers, 0), cross_chain.GetMerkleProof(layers, aggIdx["7"])
			sig := bls.Sign(env.kp103.PrivateKey(), cross_chain.ComputeCommitRootAttestMessage(root)).Bytes()
			if r.Intn(5) != 0 {
				add(from, mt_common.GATEWAY_CONTRACT_ADDRESS, wrap(gh.abi.Pack("attestCommit", big.NewInt(103), root, agg["7"], big.NewInt(wrappedAssetID),
					new(big.Int).SetUint64(ap.LeafIndex), hashesToBytes32(ap.Siblings), uint64(1), sig, []byte{0x01})), i)
			}
			add(from, mt_common.GATEWAY_CONTRACT_ADDRESS, wrap(gh.abi.Pack("claimMessage", msg.MessageID, big.NewInt(103), big.NewInt(int64(reserveChainID)),
				big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target, msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered, uint64(0),
				new(big.Int).SetUint64(mp.LeafIndex), hashesToBytes32(mp.Siblings), root)), i)
		}
	}
	return txs
}

func TestExecDifferential_GatewayAssetFlows_OptimisticVsTrueBlockSTMOnly(t *testing.T) {
	barrierContractStorage = true
	defer func() { barrierContractStorage = false }()
	runGatewayDifferential(t, 150, assetBlock, assetTweak, []string{"outbound", "refund", "attestCommit", "claimMessage"})
}
