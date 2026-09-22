package cross_chain

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// relayFixture drives a real A(101) -> Reserve(102) -> B(103) relayed transfer at the engine level,
// exactly as gateway_handler.go's claimMessage does it: AttestCommit + ClaimMessage of leg 1 on
// Reserve, then Outbound (leg 2, preserving leg 1's MessageID) + ReleaseRelayedValue, then
// BatchOutboundCommit so leg 2 sits in Reserve's own CommittedBatches.
type relayFixture struct {
	engine     *GatewayEngine
	kp103      *bls.KeyPair
	leg2       CrossChainMessage
	leg2Proof  MerkleProof
	commitRoot common.Hash
	value      *big.Int
}

const (
	relayChainA       = uint64(101)
	relayChainReserve = uint64(102)
	relayChainB       = uint64(103)
)

func newRelayFixture(t *testing.T) *relayFixture {
	t.Helper()
	engine, kpA := setupTestGatewayEngine() // engine == Reserve (102); ledger: 101=5000, 102=5000
	kp103 := bls.GenerateKeyPair()
	pop103 := PopSign(kp103.PrivateKey(), kp103.PublicKey())
	engine.ChainRegistry[relayChainB] = ChainRegistry{
		ChainID:         relayChainB,
		Committee:       []ValidatorEntry{{PubkeyBLS: kp103.BytesPublicKey(), Stake: 10000, PopSignature: pop103.Bytes()}},
		Epoch:           7,
		QuorumThreshold: 6667,
	}

	value := big.NewInt(500)
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

	leg1 := CrossChainMessage{
		MessageID:     common.HexToHash("0xAA000000000000000000000000000000000000000000000000000000000000AA"),
		SourceChainID: relayChainA,
		DestChainID:   relayChainReserve,
		Sender:        sender,
		Target:        recipient,
		Payload:       EncodeRelayPayload(relayChainB, nil),
		AssetID:       big.NewInt(0),
		Value:         value,
		Sequence:      1,
		Tip:           big.NewInt(0),
		GasFee:        big.NewInt(0),
		HopCount:      1,
	}
	commitRoot1, layers1, aggAmounts1, aggIndex1, err := BuildCommitTree([]CrossChainMessage{leg1})
	require.NoError(t, err)
	proof1 := GetMerkleProof(layers1, 0)
	aggProof1 := GetMerkleProof(layers1, aggIndex1["0"])
	sig1 := bls.Sign(kpA.PrivateKey(), append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot1.Bytes()...))
	cert1 := QuorumCert{Epoch: 5, AggregateSignature: sig1.Bytes(), SignerBitmap: []byte{0x0F}}
	_, err = engine.AttestCommit(relayChainA, commitRoot1, aggAmounts1["0"], big.NewInt(0), aggProof1, cert1)
	require.NoError(t, err)
	_, err = engine.ClaimMessage(leg1, proof1, commitRoot1, relayer, 0)
	require.NoError(t, err)

	// What claimMessage's relay branch does right after ClaimMessage:
	orig := leg1.MessageID
	_, err = engine.Outbound(sender, OutboundParams{
		DestChainID: relayChainB, Target: recipient, Payload: nil, Value: new(big.Int).Set(value),
		AssetID: big.NewInt(0), Tip: big.NewInt(0), GasFee: big.NewInt(0), HopCount: 2, OriginalID: &orig,
	}, common.HexToHash("0xBEEF"))
	require.NoError(t, err)
	require.NoError(t, engine.ReleaseRelayedValue(leg1.MessageID, value))

	commitRoot2, msgs2, err := func() (common.Hash, []CrossChainMessage, error) {
		root, msgs, e := engine.BatchOutboundCommit(relayChainB, 0)
		return root, msgs, e
	}()
	require.NoError(t, err)
	require.Len(t, msgs2, 1)
	_, layers2, _, _, err := BuildCommitTree(msgs2)
	require.NoError(t, err)

	return &relayFixture{
		engine: engine, kp103: kp103, leg2: msgs2[0], leg2Proof: GetMerkleProof(layers2, 0),
		commitRoot: commitRoot2, value: value,
	}
}

func (f *relayFixture) alloc(chain uint64) int64 {
	return f.engine.SupplyLedger.GetAllocation(chain).Int64()
}

func (f *relayFixture) sum() int64 {
	return f.alloc(relayChainA) + f.alloc(relayChainReserve) + f.alloc(relayChainB)
}

// The regression this file exists for: after the relay hop, Reserve must NOT keep the credit
// ClaimMessage gave it, because the value is in flight, not held.
func TestRelayedFlow_ReleaseMakesReserveNetZeroInFlight(t *testing.T) {
	f := newRelayFixture(t)
	assert.Equal(t, int64(4500), f.alloc(relayChainA), "A debited by AttestCommit")
	assert.Equal(t, int64(5000), f.alloc(relayChainReserve), "Reserve: +V from ClaimMessage, -V released for the in-flight leg")
	assert.Equal(t, int64(0), f.alloc(relayChainB))
	assert.Equal(t, int64(9500), f.sum(), "500 is in flight, nowhere in the ledger yet")
}

func TestRelayedFlow_Success_CreditsBAndConservesLedger(t *testing.T) {
	f := newRelayFixture(t)

	digest := ComputeMessageSuccessAttestMessage(f.leg2.MessageID, relayChainB)
	sig := bls.Sign(f.kp103.PrivateKey(), digest)
	successCert := QuorumCert{Epoch: 7, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}

	require.NoError(t, f.engine.CreditReserveAllocation(f.leg2, f.leg2Proof, f.commitRoot, successCert),
		"leg 2 is sourced by Reserve itself; its own CommittedBatches must be accepted as proof")

	assert.Equal(t, int64(4500), f.alloc(relayChainA))
	assert.Equal(t, int64(5000), f.alloc(relayChainReserve))
	assert.Equal(t, int64(500), f.alloc(relayChainB), "B must be credited on Reserve's authoritative ledger")
	assert.Equal(t, int64(10000), f.sum(), "sum of allocations conserved end to end")

	// Idempotent: a retry / second relayer must not double-credit.
	require.NoError(t, f.engine.CreditReserveAllocation(f.leg2, f.leg2Proof, f.commitRoot, successCert))
	assert.Equal(t, int64(500), f.alloc(relayChainB))
	assert.Empty(t, f.engine.RelayedInFlight, "in-flight record cleared once resolved")
}

func TestRelayedFlow_Failure_RefundDoesNotDoubleCountOnReserve(t *testing.T) {
	f := newRelayFixture(t)

	digest := ComputeMessageFailureAttestMessage(f.leg2.MessageID, relayChainB)
	sig := bls.Sign(f.kp103.PrivateKey(), digest)
	failCert := QuorumCert{Epoch: 7, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}

	// refund() on Reserve (leg 2's source): the handler mints V to the sender there and the engine
	// credits Reserve's ledger for it.
	require.NoError(t, f.engine.Refund(f.leg2, f.leg2Proof, f.commitRoot, failCert))

	assert.Equal(t, int64(4500), f.alloc(relayChainA))
	assert.Equal(t, int64(5500), f.alloc(relayChainReserve), "Reserve holds exactly the V minted back to the sender there, not 2V")
	assert.Equal(t, int64(0), f.alloc(relayChainB))
	assert.Equal(t, int64(10000), f.sum(), "sum conserved (before the fix this ended at 10500)")
	assert.Empty(t, f.engine.RelayedInFlight)
}

func TestRelayedFlow_ReleaseIsIdempotent(t *testing.T) {
	f := newRelayFixture(t)
	require.NoError(t, f.engine.ReleaseRelayedValue(f.leg2.MessageID, f.value))
	require.NoError(t, f.engine.ReleaseRelayedValue(f.leg2.MessageID, f.value))
	assert.Equal(t, int64(5000), f.alloc(relayChainReserve), "a repeated release must not debit Reserve twice")
}

func TestRelayedFlow_ReleaseIsNoOpOffReserve(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	engine.LocalChainID = relayChainA // not Reserve
	before := engine.SupplyLedger.GetAllocation(relayChainA).Int64()
	require.NoError(t, engine.ReleaseRelayedValue(common.HexToHash("0x01"), big.NewInt(100)))
	assert.Equal(t, before, engine.SupplyLedger.GetAllocation(relayChainA).Int64())
	assert.Empty(t, engine.RelayedInFlight)
}

// Crediting B for a Reserve-sourced message that was NOT relayed (an ordinary Reserve-issued
// transfer, whose value was never released from Reserve's ledger) would inflate the sum of
// allocations -- the narrow leg-2 exception must not open that door.
func TestRelayedFlow_CreditRejectedForNonRelayedReserveSourcedMessage(t *testing.T) {
	f := newRelayFixture(t)

	// Forget the relay record, as if this were an ordinary Reserve-issued message.
	delete(f.engine.RelayedInFlight, f.leg2.MessageID)

	digest := ComputeMessageSuccessAttestMessage(f.leg2.MessageID, relayChainB)
	sig := bls.Sign(f.kp103.PrivateKey(), digest)
	successCert := QuorumCert{Epoch: 7, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}

	err := f.engine.CreditReserveAllocation(f.leg2, f.leg2Proof, f.commitRoot, successCert)
	require.ErrorIs(t, err, ErrCommitNotAttested)
	assert.Equal(t, int64(0), f.alloc(relayChainB))
}

func TestRelayedFlow_CreditRejectedWhenValueDoesNotMatchRecordedRelay(t *testing.T) {
	f := newRelayFixture(t)
	f.engine.RelayedInFlight[f.leg2.MessageID] = big.NewInt(1) // recorded relay was for a different value

	digest := ComputeMessageSuccessAttestMessage(f.leg2.MessageID, relayChainB)
	sig := bls.Sign(f.kp103.PrivateKey(), digest)
	successCert := QuorumCert{Epoch: 7, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}

	err := f.engine.CreditReserveAllocation(f.leg2, f.leg2Proof, f.commitRoot, successCert)
	require.ErrorIs(t, err, ErrCommitNotAttested)
	assert.Equal(t, int64(0), f.alloc(relayChainB))
}

// Upgrade safety: an engine that never relayed must serialize exactly as before (no new key), so
// the new field cannot change any existing chain's state root.
func TestRelayedFlow_NewFieldDoesNotChangeSerializationWhenUnused(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	raw, err := json.Marshal(engine)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "relayed_in_flight")
}

// An ordinary (non-relayed) Reserve-issued native transfer never debits Reserve's PerChainAllocation
// pool on issue (attestReserveIssuedCommit skips the ceiling), so a failure refund must NOT credit
// the pool either -- otherwise every failed Reserve->B transfer inflates the pool by V, and that
// pool is what RegisterChainViaStake funds new chains from.
func TestReserveIssuedRefund_NonRelayedDoesNotInflateReservePool(t *testing.T) {
	engine, _ := setupTestGatewayEngine() // Reserve == 102
	kp103 := bls.GenerateKeyPair()
	pop103 := PopSign(kp103.PrivateKey(), kp103.PublicKey())
	engine.ChainRegistry[relayChainB] = ChainRegistry{
		ChainID:         relayChainB,
		Committee:       []ValidatorEntry{{PubkeyBLS: kp103.BytesPublicKey(), Stake: 10000, PopSignature: pop103.Bytes()}},
		Epoch:           7,
		QuorumThreshold: 6667,
	}
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")

	_, err := engine.Outbound(sender, OutboundParams{
		DestChainID: relayChainB, Target: target, Value: big.NewInt(500), AssetID: big.NewInt(0),
		Tip: big.NewInt(0), GasFee: big.NewInt(0), HopCount: 1,
	}, common.HexToHash("0xCAFE"))
	require.NoError(t, err)
	root, msgs, err := engine.BatchOutboundCommit(relayChainB, 0)
	require.NoError(t, err)
	_, layers, _, _, err := BuildCommitTree(msgs)
	require.NoError(t, err)

	sig := bls.Sign(kp103.PrivateKey(), ComputeMessageFailureAttestMessage(msgs[0].MessageID, relayChainB))
	failCert := QuorumCert{Epoch: 7, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}
	require.NoError(t, engine.Refund(msgs[0], GetMerkleProof(layers, 0), root, failCert))

	assert.Equal(t, int64(5000), engine.SupplyLedger.GetAllocation(relayChainReserve).Int64(), "Reserve pool must be unchanged: it was never debited on issue")
	assert.Equal(t, int64(10000), engine.SupplyLedger.GetAllocation(relayChainA).Int64()+engine.SupplyLedger.GetAllocation(relayChainReserve).Int64())
}

// Someone submitting Reserve's own (publicly available) commit cert through
// AttestReserveIssuedCommit ON Reserve records an AttestedCommit but never debits the pool. That
// record must not be mistaken for a debit when the message later fails and is refunded.
func TestReserveIssuedRefund_ReserveIssuedAttestationOnReserveIsNotADebit(t *testing.T) {
	engine, _ := setupTestGatewayEngine() // Reserve == 102
	kpReserve := bls.GenerateKeyPair()
	engine.ChainRegistry[relayChainReserve] = ChainRegistry{
		ChainID:         relayChainReserve,
		Committee:       []ValidatorEntry{{PubkeyBLS: kpReserve.BytesPublicKey(), Stake: 10000, PopSignature: PopSign(kpReserve.PrivateKey(), kpReserve.PublicKey()).Bytes()}},
		Epoch:           3,
		QuorumThreshold: 6667,
	}
	kp103 := bls.GenerateKeyPair()
	engine.ChainRegistry[relayChainB] = ChainRegistry{
		ChainID:         relayChainB,
		Committee:       []ValidatorEntry{{PubkeyBLS: kp103.BytesPublicKey(), Stake: 10000, PopSignature: PopSign(kp103.PrivateKey(), kp103.PublicKey()).Bytes()}},
		Epoch:           7,
		QuorumThreshold: 6667,
	}

	_, err := engine.Outbound(common.HexToAddress("0x1111111111111111111111111111111111111111"), OutboundParams{
		DestChainID: relayChainB, Target: common.HexToAddress("0x2222222222222222222222222222222222222222"),
		Value: big.NewInt(500), AssetID: big.NewInt(0), Tip: big.NewInt(0), GasFee: big.NewInt(0), HopCount: 1,
	}, common.HexToHash("0xF00D"))
	require.NoError(t, err)
	root, msgs, err := engine.BatchOutboundCommit(relayChainB, 3)
	require.NoError(t, err)
	_, layers, aggAmounts, aggIdx, err := BuildCommitTree(msgs)
	require.NoError(t, err)

	attSig := bls.Sign(kpReserve.PrivateKey(), ComputeCommitRootAttestMessage(root))
	_, err = engine.AttestReserveIssuedCommit(relayChainReserve, root, aggAmounts["0"], big.NewInt(0), GetMerkleProof(layers, aggIdx["0"]),
		QuorumCert{Epoch: 3, AggregateSignature: attSig.Bytes(), SignerBitmap: []byte{0x01}})
	require.NoError(t, err)
	require.Equal(t, int64(5000), engine.SupplyLedger.GetAllocation(relayChainReserve).Int64(), "attestReserveIssuedCommit must not debit")

	failSig := bls.Sign(kp103.PrivateKey(), ComputeMessageFailureAttestMessage(msgs[0].MessageID, relayChainB))
	require.NoError(t, engine.Refund(msgs[0], GetMerkleProof(layers, 0), root,
		QuorumCert{Epoch: 7, AggregateSignature: failSig.Bytes(), SignerBitmap: []byte{0x01}}))
	assert.Equal(t, int64(5000), engine.SupplyLedger.GetAllocation(relayChainReserve).Int64(), "no debit happened, so no credit may be owed")
}
