package cross_chain

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGateway_Timeout_RejectsClaim(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x9999999999999999999999999999999999999999")

	msg := CrossChainMessage{
		MessageID:        common.HexToHash("0x11"),
		SourceChainID:    101,
		DestChainID:      engine.LocalChainID,
		Sender:           sender,
		Target:           target,
		Value:            big.NewInt(100),
		AssetID:          big.NewInt(0),
		Payload:          []byte{},
		TimeoutTimestamp: 5000,
	}

	proof := MerkleProof{
		LeafIndex: 0,
		Siblings:  []common.Hash{},
	}

	leafHash := ComputeMessageLeafHash(msg)
	commitRoot := leafHash
	engine.AttestedCommits[fmt.Sprintf("101:%s:0", commitRoot.Hex())] = AttestedCommit{
		CommitRoot:    commitRoot,
		FundedAmount:  big.NewInt(100),
		ClaimedAmount: big.NewInt(0),
	}

	status, err := engine.ClaimMessage(msg, proof, commitRoot, relayer, 4000)
	require.NoError(t, err)
	assert.Equal(t, MessageStatusSuccess, status)

	msg2 := msg
	msg2.MessageID = common.HexToHash("0x33")
	leafHash2 := ComputeMessageLeafHash(msg2)
	commitRoot2 := leafHash2
	engine.AttestedCommits[fmt.Sprintf("101:%s:0", commitRoot2.Hex())] = AttestedCommit{
		CommitRoot:    commitRoot2,
		FundedAmount:  big.NewInt(100),
		ClaimedAmount: big.NewInt(0),
	}

	status2, err2 := engine.ClaimMessage(msg2, proof, commitRoot2, relayer, 6000)
	require.NoError(t, err2)
	assert.Equal(t, MessageStatusFailedTimeout, status2)
}

// TestGateway_VelocityLimit_CapsOutflowPer24hWindow proves the 20%-per-24h velocity limit
// (note/cross_chain/production_security_hardening_research.md §3.2 item 5) is enforced against
// the CURRENT authoritative allocation (SupplyLedger.PerChainAllocation[sourceChainID], read
// inside attestCommitInternal's own enforceCeiling branch) rather than a non-authoritative copy --
// see gateway.go's OutflowInWindow doc comment for why an earlier attempt at this feature (checked
// at outbound() time, on the sending chain's own local ledger copy) was a dead end.
func TestGateway_VelocityLimit_CapsOutflowPer24hWindow(t *testing.T) {
	engine, kp := setupTestGatewayEngine()

	signFor := func(amount *big.Int) (common.Hash, QuorumCert) {
		leaf := AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: amount}
		root := HashAggregateValueLeaf(leaf)
		commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), root.Bytes()...)
		sig := bls.Sign(kp.PrivateKey(), commitMsg)
		return root, QuorumCert{Epoch: 5, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x0F}}
	}

	// setupTestGatewayEngine funds chain 101 with 5000 -> 20% velocity limit = 1000.
	root1, cert1 := signFor(big.NewInt(600))
	_, err1 := engine.AttestCommit(101, root1, big.NewInt(600), big.NewInt(0), MerkleProof{}, cert1, 10_000)
	require.NoError(t, err1)

	// A second attest in the SAME window pushing cumulative outflow to 1100 (> 1000 limit) must be
	// rejected, even though 500 alone is well within the 4400 remaining hard cap.
	root2, cert2 := signFor(big.NewInt(500))
	_, err2 := engine.AttestCommit(101, root2, big.NewInt(500), big.NewInt(0), MerkleProof{}, cert2, 10_000+1)
	assert.ErrorIs(t, err2, ErrVelocityLimitExceeded)
	assert.Equal(t, big.NewInt(4400), engine.SupplyLedger.PerChainAllocation[101], "a rejected velocity-limited attest must not touch the ledger")

	// The same 400 (within the remaining 1000-600=400 budget) succeeds in the SAME window.
	root3, cert3 := signFor(big.NewInt(400))
	attested3, err3 := engine.AttestCommit(101, root3, big.NewInt(400), big.NewInt(0), MerkleProof{}, cert3, 10_000+2)
	require.NoError(t, err3)
	assert.Equal(t, big.NewInt(400), attested3.FundedAmount)
	assert.Equal(t, big.NewInt(4000), engine.SupplyLedger.PerChainAllocation[101])

	// Advancing past the 24h window resets the budget -- a fresh 20% of the NEW 4000 allocation
	// (= 800) now succeeds even though the window's cumulative total (600+400+800=1800) would have
	// exceeded the original 1000 limit had the window never reset.
	root4, cert4 := signFor(big.NewInt(800))
	attested4, err4 := engine.AttestCommit(101, root4, big.NewInt(800), big.NewInt(0), MerkleProof{}, cert4, 10_000+24*60*60+1)
	require.NoError(t, err4)
	assert.Equal(t, big.NewInt(800), attested4.FundedAmount)
	assert.Equal(t, big.NewInt(3200), engine.SupplyLedger.PerChainAllocation[101])
}

// With VelocityLimitDisabled a chain can move its whole allocation at once and again in the same window: it is bounded only
// by its own allocation (its balance), like a wallet. The default (flag unset) keeps the 20%/24h cap.
func TestGateway_VelocityLimitDisabled_BoundedOnlyByAllocation(t *testing.T) {
	attest := func(engine *GatewayEngine, kp *bls.KeyPair, seq uint64, amount int64) error {
		msg := CrossChainMessage{MessageID: common.BigToHash(big.NewInt(int64(0xE000) + int64(seq))), SourceChainID: 101, DestChainID: 102, Sequence: seq, HopCount: 1,
			Sender: common.HexToAddress("0x01"), Target: common.HexToAddress("0x02"), AssetID: big.NewInt(0), Value: big.NewInt(amount), Tip: big.NewInt(0), GasFee: big.NewInt(0)}
		root, layers, agg, aggIdx, err := BuildCommitTree([]CrossChainMessage{msg})
		require.NoError(t, err)
		sig := bls.Sign(kp.PrivateKey(), ComputeCommitRootAttestMessage(root))
		_, err = engine.AttestCommit(101, root, agg["0"], big.NewInt(0), GetMerkleProof(layers, aggIdx["0"]),
			QuorumCert{Epoch: 5, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x0F}}, 0)
		return err
	}

	// default: 20% of a 5000 allocation is 1000, so 4000 in one go is refused
	engine, kp := setupTestGatewayEngine()
	require.ErrorIs(t, attest(engine, kp, 1, 4000), ErrVelocityLimitExceeded)

	// disabled: the same 4000 passes, then the remaining 1000 passes too, and a further 1 is refused because the allocation is spent
	engine, kp = setupTestGatewayEngine()
	engine.VelocityLimitDisabled = true
	require.NoError(t, attest(engine, kp, 1, 4000))
	require.NoError(t, attest(engine, kp, 2, 1000))
	require.ErrorIs(t, attest(engine, kp, 3, 1), ErrAllocationExceeded, "out of allocation: nothing more can move")
	assert.Equal(t, 0, engine.SupplyLedger.GetAllocation(101).Sign())
}
