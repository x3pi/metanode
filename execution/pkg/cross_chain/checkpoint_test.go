package cross_chain

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// Phase B tầng 1 — Checkpoint liveness signal
// (note/cross_chain/root_anchor_production_security_hardening_plan.md)
// ══════════════════════════════════════════════════════════════════════════════

func TestSubmitCheckpoint_Success(t *testing.T) {
	engine, kp := setupTestGatewayEngine() // kp is chain 101's real committee key, epoch 5

	stateRoot := common.HexToHash("0xAAAA111100000000000000000000000000000000000000000000000000000000")
	valSetHash := common.HexToHash("0xBBBB222200000000000000000000000000000000000000000000000000000000")
	digest := ComputeCheckpointMessage(101, 5, 1000, stateRoot, valSetHash)
	sig := bls.Sign(kp.PrivateKey(), digest)
	cert := QuorumCert{Epoch: 5, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}

	require.NoError(t, engine.SubmitCheckpoint(101, 5, 1000, stateRoot, valSetHash, cert, 50_000))

	got, ok := engine.Checkpoints[101]
	require.True(t, ok)
	assert.Equal(t, uint64(5), got.Epoch)
	assert.Equal(t, uint64(1000), got.BlockHeight)
	assert.Equal(t, stateRoot, got.StateRoot)
	assert.Equal(t, valSetHash, got.ValidatorSetHash)
	assert.Equal(t, uint64(50_000), got.SubmittedAt)
}

func TestSubmitCheckpoint_RejectsUnknownChain(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	stateRoot := common.HexToHash("0xCCCC")
	valSetHash := common.HexToHash("0xDDDD")
	digest := ComputeCheckpointMessage(9999, 0, 1, stateRoot, valSetHash)
	sig := bls.Sign(kp.PrivateKey(), digest)
	cert := QuorumCert{Epoch: 0, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}

	err := engine.SubmitCheckpoint(9999, 0, 1, stateRoot, valSetHash, cert, 1)
	assert.ErrorIs(t, err, ErrUnknownSourceChain)
}

func TestSubmitCheckpoint_RejectsDeadChain(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	engine.DeadChains[101] = true
	stateRoot := common.HexToHash("0xEEEE")
	valSetHash := common.HexToHash("0xFFFF")
	digest := ComputeCheckpointMessage(101, 5, 1000, stateRoot, valSetHash)
	sig := bls.Sign(kp.PrivateKey(), digest)
	cert := QuorumCert{Epoch: 5, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}

	err := engine.SubmitCheckpoint(101, 5, 1000, stateRoot, valSetHash, cert, 1)
	assert.ErrorIs(t, err, ErrChainDeclaredDead)
}

func TestSubmitCheckpoint_RejectsWrongEpoch(t *testing.T) {
	engine, kp := setupTestGatewayEngine() // registry epoch is 5
	stateRoot := common.HexToHash("0x1010")
	valSetHash := common.HexToHash("0x2020")
	digest := ComputeCheckpointMessage(101, 6, 1000, stateRoot, valSetHash)
	sig := bls.Sign(kp.PrivateKey(), digest)
	cert := QuorumCert{Epoch: 6, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}} // wrong epoch

	err := engine.SubmitCheckpoint(101, 6, 1000, stateRoot, valSetHash, cert, 1)
	assert.ErrorIs(t, err, ErrEpochMismatch)
}

func TestSubmitCheckpoint_RejectsForgedSignature(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	attacker := bls.GenerateKeyPair() // NOT chain 101's real committee key
	stateRoot := common.HexToHash("0x3030")
	valSetHash := common.HexToHash("0x4040")
	digest := ComputeCheckpointMessage(101, 5, 1000, stateRoot, valSetHash)
	sig := bls.Sign(attacker.PrivateKey(), digest)
	cert := QuorumCert{Epoch: 5, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}

	err := engine.SubmitCheckpoint(101, 5, 1000, stateRoot, valSetHash, cert, 1)
	assert.Error(t, err)
	_, exists := engine.Checkpoints[101]
	assert.False(t, exists, "a forged checkpoint must never be recorded")
}

// TestSubmitCheckpoint_RejectsNonMonotonicBlockHeight proves a checkpoint at or below the
// currently recorded BlockHeight is rejected -- a captured committee (or accidental resubmit)
// replaying an old checkpoint must not be able to reset the staleness clock without the chain
// actually having progressed.
func TestSubmitCheckpoint_RejectsNonMonotonicBlockHeight(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	sign := func(blockHeight uint64, stateRoot, valSetHash common.Hash) QuorumCert {
		digest := ComputeCheckpointMessage(101, 5, blockHeight, stateRoot, valSetHash)
		sig := bls.Sign(kp.PrivateKey(), digest)
		return QuorumCert{Epoch: 5, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}
	}

	root1 := common.HexToHash("0x5050")
	val1 := common.HexToHash("0x6060")
	require.NoError(t, engine.SubmitCheckpoint(101, 5, 1000, root1, val1, sign(1000, root1, val1), 100))

	// Same height again -- rejected.
	err := engine.SubmitCheckpoint(101, 5, 1000, root1, val1, sign(1000, root1, val1), 200)
	assert.ErrorIs(t, err, ErrCheckpointNotMonotonic)

	// Lower height -- rejected (e.g. a rollback/replay attempt).
	err = engine.SubmitCheckpoint(101, 5, 999, root1, val1, sign(999, root1, val1), 200)
	assert.ErrorIs(t, err, ErrCheckpointNotMonotonic)

	// The recorded checkpoint must still be the original one -- neither rejected attempt mutated it.
	got := engine.Checkpoints[101]
	assert.Equal(t, uint64(1000), got.BlockHeight)
	assert.Equal(t, uint64(100), got.SubmittedAt)

	// A genuinely higher height succeeds and advances the staleness clock.
	root2 := common.HexToHash("0x7070")
	val2 := common.HexToHash("0x8080")
	require.NoError(t, engine.SubmitCheckpoint(101, 5, 1001, root2, val2, sign(1001, root2, val2), 300))
	got = engine.Checkpoints[101]
	assert.Equal(t, uint64(1001), got.BlockHeight)
	assert.Equal(t, uint64(300), got.SubmittedAt)
}
