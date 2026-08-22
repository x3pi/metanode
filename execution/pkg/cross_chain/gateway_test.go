package cross_chain

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestGatewayEngine() *GatewayEngine {
	registry := make(map[uint64]ChainRegistry)
	registry[101] = ChainRegistry{
		ChainID:          101,
		Committee:        []ValidatorEntry{},
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

	return NewGatewayEngine(102, registry, ledger)
}

func TestGateway_P2_1_and_P2_5_OutboundAndHopCountGuard(t *testing.T) {
	engine := setupTestGatewayEngine()
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
	engine := setupTestGatewayEngine()
	commitRoot := common.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555")
	cert := QuorumCert{
		Epoch:              5,
		AggregateSignature: make([]byte, 48),
		SignerBitmap:       []byte{0x0F},
	}

	// Attack Case (Scenario 10.7): aggregateAmount = 6000 > available allocation = 5000 -> REJECT
	_, errAttack := engine.AttestCommit(101, commitRoot, big.NewInt(6000), cert, true)
	assert.ErrorIs(t, errAttack, ErrAllocationExceeded)

	// Valid Case: aggregateAmount = 2000 <= 5000 -> Succeeded & Deducts allocation to 3000
	attested, errValid := engine.AttestCommit(101, commitRoot, big.NewInt(2000), cert, true)
	require.NoError(t, errValid)
	assert.Equal(t, big.NewInt(2000), attested.FundedAmount)
	assert.Equal(t, big.NewInt(3000), engine.SupplyLedger.PerChainAllocation[101])
}

func TestGateway_P2_3_ClaimMessageAndDoubleClaimPrevention(t *testing.T) {
	engine := setupTestGatewayEngine()
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


	leafBytes, _ := json.Marshal(msg)
	leafHash := Keccak256(leafBytes)
	proof := MerkleProof{
		LeafIndex: 0,
		Siblings:  []common.Hash{},
	}
	commitRoot := leafHash // Root equals leaf with 0 siblings

	// Attest commit first
	cert := QuorumCert{
		Epoch:              5,
		AggregateSignature: make([]byte, 48),
		SignerBitmap:       []byte{0x0F},
	}
	_, errAttest := engine.AttestCommit(101, commitRoot, big.NewInt(500), cert, true)
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

func TestGateway_P2_4_RefundPathwayAndDoubleRefundGuard(t *testing.T) {
	engine := setupTestGatewayEngine()
	msgID := common.HexToHash("0x7777777777777777777777777777777777777777777777777777777777777777")
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")

	// First Refund -> Success
	err := engine.Refund(msgID, sender, big.NewInt(100), true)
	require.NoError(t, err)
	assert.Equal(t, MessageStatusRefunded, engine.GetMessageStatus(msgID))

	// Second Refund -> MUST REJECT (Already refunded)
	errDup := engine.Refund(msgID, sender, big.NewInt(100), true)
	assert.ErrorIs(t, errDup, ErrInvalidRefundState)
}

func TestGateway_P2_8_ClaimDeadChainBalanceAndDuplicateGuard(t *testing.T) {
	engine := setupTestGatewayEngine()
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
