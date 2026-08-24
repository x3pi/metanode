package cross_chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestGatewayEngine() (*GatewayEngine, *bls.KeyPair) {
	kp := bls.GenerateKeyPair()
	popSig := PopSign(kp.PrivateKey(), kp.PublicKey())

	registry := make(map[uint64]ChainRegistry)
	registry[101] = ChainRegistry{
		ChainID: 101,
		Committee: []ValidatorEntry{
			{
				PubkeyBLS:    kp.BytesPublicKey(),
				Stake:        10000,
				PopSignature: popSig.Bytes(),
			},
		},
		Epoch:            5,
		QuorumThreshold:  6667,
		GatewayContract:  common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		StateRoot:        common.HexToHash("0xEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE"),
		ArchivalEndpoint: "http://archive.chain101.test",
		RegisteredAt:     1000,
	}

	allocs := map[uint64]*big.Int{
		101: big.NewInt(5000),
		102: big.NewInt(5000),
	}
	ledger, err := NewGlobalSupplyLedger(big.NewInt(10000), allocs)
	if err != nil {
		panic(err)
	}

	return NewGatewayEngine(102, registry, ledger), kp
}

func TestGateway_P2_1_and_P2_5_OutboundAndHopCountGuard(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	txHash := common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")

	// Test 1: Hop count = 6 -> MUST SUCCEED (P2.5)
	paramsValid := OutboundParams{
		DestChainID: 102,
		Target:      target,
		Payload:     []byte{1, 2, 3},
		AssetID:     big.NewInt(0),
		Value:       big.NewInt(100),
		Tip:         big.NewInt(5),
		HopCount:    6,
		Ordered:     false,
	}
	msg, err := engine.Outbound(sender, paramsValid, txHash)
	require.NoError(t, err)
	assert.Equal(t, txHash, msg.MessageID)
	assert.Equal(t, MessageStatusPending, engine.GetMessageStatus(txHash))

	// Test 2: Hop count = 7 -> MUST REJECT (P2.5)
	paramsInvalid := OutboundParams{
		DestChainID: 102,
		Target:      target,
		Payload:     []byte{1, 2, 3},
		AssetID:     big.NewInt(0),
		Value:       big.NewInt(100),
		Tip:         big.NewInt(5),
		HopCount:    7,
		Ordered:     false,
	}
	_, errInvalid := engine.Outbound(sender, paramsInvalid, common.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444"))
	assert.ErrorIs(t, errInvalid, ErrHopCountExceeded)
}

func TestGateway_P2_2_AttestCommitAndScenario10_7_AllocationGuard(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	commitRoot := common.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555")

	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)

	cert := QuorumCert{
		Epoch:              5,
		AggregateSignature: sig.Bytes(),
		SignerBitmap:       []byte{0x0F},
	}

	// Attack Case (Scenario 10.7): aggregateAmount = 6000 > available allocation = 5000 -> REJECT
	_, errAttack := engine.AttestCommit(101, commitRoot, big.NewInt(6000), cert)
	assert.ErrorIs(t, errAttack, ErrAllocationExceeded)

	// Valid Case: aggregateAmount = 2000 <= 5000 -> Succeeded & Deducts allocation to 3000
	attested, errValid := engine.AttestCommit(101, commitRoot, big.NewInt(2000), cert)
	require.NoError(t, errValid)
	assert.Equal(t, big.NewInt(2000), attested.FundedAmount)
	assert.Equal(t, big.NewInt(3000), engine.SupplyLedger.PerChainAllocation[101])
}

func TestGateway_P2_3_ClaimMessageAndDoubleClaimPrevention(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x9999999999999999999999999999999999999999")

	msg := CrossChainMessage{
		MessageID:     common.HexToHash("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		SourceChainID: 101,
		DestChainID:   102,
		Sender:        sender,
		Target:        target,
		Payload:       []byte{0x10, 0x20},
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(500),
		Sequence:      1,
		Tip:           big.NewInt(10),
		HopCount:      1,
		Ordered:       false,
	}

	leafHash := ComputeMessageLeafHash(msg)
	proof := MerkleProof{
		LeafIndex: 0,
		Siblings:  []common.Hash{},
	}
	commitRoot := leafHash // Root equals leaf with 0 siblings

	// Attest commit first with real BLS signature
	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)
	cert := QuorumCert{
		Epoch:              5,
		AggregateSignature: sig.Bytes(),
		SignerBitmap:       []byte{0x0F},
	}
	_, errAttest := engine.AttestCommit(101, commitRoot, big.NewInt(500), cert)
	require.NoError(t, errAttest)

	// First Claim -> SUCCESS (P2.3)
	status, errClaim := engine.ClaimMessage(msg, proof, commitRoot, relayer)
	require.NoError(t, errClaim)
	assert.Equal(t, MessageStatusSuccess, status)
	assert.Equal(t, MessageStatusSuccess, engine.GetMessageStatus(msg.MessageID))

	// Second Claim -> MUST REJECT (Double Claim / Idempotent Guard)
	_, errDup := engine.ClaimMessage(msg, proof, commitRoot, relayer)
	assert.ErrorIs(t, errDup, ErrAlreadyClaimed)
}

func TestGateway_P2_3_1_HardCapCommitCapacityDefense(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x9999999999999999999999999999999999999999")

	// Create message with value 600
	msg := CrossChainMessage{
		MessageID:     common.HexToHash("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"),
		SourceChainID: 101,
		DestChainID:   102,
		Sender:        sender,
		Target:        target,
		Payload:       []byte{},
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(600), // Exceeds 500 funded cap!
		Sequence:      1,
		Tip:           big.NewInt(0),
		HopCount:      1,
	}

	leafHash := ComputeMessageLeafHash(msg)
	proof := MerkleProof{
		LeafIndex: 0,
		Siblings:  []common.Hash{},
	}
	commitRoot := leafHash

	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)
	cert := QuorumCert{
		Epoch:              5,
		AggregateSignature: sig.Bytes(),
		SignerBitmap:       []byte{0x0F},
	}
	// Commit is only funded with 500
	_, errAttest := engine.AttestCommit(101, commitRoot, big.NewInt(500), cert)
	require.NoError(t, errAttest)

	// Attacker tries to claim 600 -> MUST REJECT (Hard-cap capacity exceeded)
	_, errOverClaim := engine.ClaimMessage(msg, proof, commitRoot, relayer)
	assert.ErrorIs(t, errOverClaim, ErrAllocationExceeded)
}

func TestGateway_P2_4_RefundPathwayAndSupplyRestoration(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	msgID := common.HexToHash("0x7777777777777777777777777777777777777777777777777777777777777777")
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")

	allocBefore := new(big.Int).Set(engine.SupplyLedger.PerChainAllocation[101])

	// First Refund -> Success and Restores Allocation (+100)
	err := engine.Refund(msgID, 101, sender, big.NewInt(100), true)
	require.NoError(t, err)
	assert.Equal(t, MessageStatusRefunded, engine.GetMessageStatus(msgID))
	assert.Equal(t, new(big.Int).Add(allocBefore, big.NewInt(100)), engine.SupplyLedger.PerChainAllocation[101])

	// Second Refund -> MUST REJECT (Already refunded)
	errDup := engine.Refund(msgID, 101, sender, big.NewInt(100), true)
	assert.ErrorIs(t, errDup, ErrInvalidRefundState)
}

func TestGateway_P2_8_ClaimDeadChainBalanceAndDuplicateGuard(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	deadChainID := uint64(101)
	account := common.HexToAddress("0x3333333333333333333333333333333333333333")
	accountLeafHash := common.HexToHash("0xEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE") // matches stateRoot
	proof := MerkleProof{
		LeafIndex: 0,
		Siblings:  []common.Hash{},
	}

	// Before declared dead -> Reject
	errNotDead := engine.ClaimDeadChainBalance(deadChainID, account, big.NewInt(1000), proof, accountLeafHash)
	assert.ErrorIs(t, errNotDead, ErrChainNotDead)

	// Declare chain dead
	engine.DeadChains[deadChainID] = true

	// First Claim -> Success
	errClaim := engine.ClaimDeadChainBalance(deadChainID, account, big.NewInt(1000), proof, accountLeafHash)
	require.NoError(t, errClaim)

	// Second Claim -> MUST REJECT (Already claimed)
	errDup := engine.ClaimDeadChainBalance(deadChainID, account, big.NewInt(1000), proof, accountLeafHash)
	assert.ErrorIs(t, errDup, ErrDeadChainAlreadyClaimed)
}
