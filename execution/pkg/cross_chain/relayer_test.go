package cross_chain

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestRelayerNetwork() (*RelayerEngine, map[uint64]*GatewayEngine) {
	// Chain 1000 = Reserve / Root Anchor
	// Chain 101  = Private Chain A
	// Chain 102  = Private Chain B
	// Chain 103  = Private Chain C (Adversarial)

	kp1000 := bls.GenerateKeyPair()
	kp101 := bls.GenerateKeyPair()
	kp102 := bls.GenerateKeyPair()
	kp103 := bls.GenerateKeyPair()

	registry := make(map[uint64]ChainRegistry)
	registry[1000] = ChainRegistry{
		ChainID:         1000,
		Epoch:           1,
		QuorumThreshold: 6667,
		Committee:       []ValidatorEntry{{PubkeyBLS: kp1000.PublicKey().Bytes(), Stake: 100}},
		StateRoot:       common.HexToHash("0x1000100010001000100010001000100010001000100010001000100010001000"),
	}
	registry[101] = ChainRegistry{
		ChainID:         101,
		Epoch:           1,
		QuorumThreshold: 6667,
		Committee:       []ValidatorEntry{{PubkeyBLS: kp101.PublicKey().Bytes(), Stake: 100}},
		StateRoot:       common.HexToHash("0x1010101010101010101010101010101010101010101010101010101010101010"),
	}
	registry[102] = ChainRegistry{
		ChainID:         102,
		Epoch:           1,
		QuorumThreshold: 6667,
		Committee:       []ValidatorEntry{{PubkeyBLS: kp102.PublicKey().Bytes(), Stake: 100}},
		StateRoot:       common.HexToHash("0x1020102010201020102010201020102010201020102010201020102010201020"),
	}
	registry[103] = ChainRegistry{
		ChainID:         103,
		Epoch:           1,
		QuorumThreshold: 6667,
		Committee:       []ValidatorEntry{{PubkeyBLS: kp103.PublicKey().Bytes(), Stake: 100}},
		StateRoot:       common.HexToHash("0x1030103010301030103010301030103010301030103010301030103010301030"),
	}

	allocs := map[uint64]*big.Int{
		1000: big.NewInt(100_000),
		101:  big.NewInt(5_000),
		102:  big.NewInt(5_000),
		103:  big.NewInt(500), // Chain C only has 500 allocation
	}
	ledger, err := NewGlobalSupplyLedger(big.NewInt(110_500), allocs)
	if err != nil {
		panic(err)
	}

	chains := make(map[uint64]*GatewayEngine)
	chains[1000] = NewGatewayEngine(1000, registry, ledger)
	chains[101] = NewGatewayEngine(101, registry, ledger)
	chains[102] = NewGatewayEngine(102, registry, ledger)
	chains[103] = NewGatewayEngine(103, registry, ledger)

	cfg := RelayerConfig{
		RelayerAddress: common.HexToAddress("0x7777777777777777777777777777777777777777"),
		ReserveChainID: 1000,
		BatchSize:      2000,
		PollInterval:   10 * time.Millisecond,
		MaxRetries:     3,
	}

	engine := NewRelayerEngine(cfg, chains)
	engine.SetSigners(1000, []*bls.KeyPair{kp1000})
	engine.SetSigners(101, []*bls.KeyPair{kp101})
	engine.SetSigners(102, []*bls.KeyPair{kp102})
	engine.SetSigners(103, []*bls.KeyPair{kp103})
	return engine, chains
}

// ─────────────────────────────────────────────────────────────────────────────
// P4.2: Relay Tip Claiming & Competition Tests ("First Come, First Served")
// ─────────────────────────────────────────────────────────────────────────────

func TestRelayer_P4_2_TipClaimingAndConcurrencyCompetition(t *testing.T) {
	relayerEngine, chains := setupTestRelayerNetwork()

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	txHash := common.HexToHash("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	tipAmount := big.NewInt(50)
	params := OutboundParams{
		DestChainID: 102,
		Target:      target,
		Payload:     []byte{1, 2, 3},
		AssetID:     big.NewInt(0),
		Value:       big.NewInt(0), // Direct 1-hop
		Tip:         tipAmount,
		HopCount:    1,
		Ordered:     false,
	}

	msg, err := relayerEngine.SubmitOutbound(101, sender, params, txHash)
	require.NoError(t, err)

	commitData, err := relayerEngine.CertifyCommit(101, 1, []CrossChainMessage{*msg}, nil)
	require.NoError(t, err)
	proof := GetMerkleProof(commitData.MerkleLayers, 0)

	relayer1 := common.HexToAddress("0xAAAA1111AAAA1111AAAA1111AAAA1111AAAA1111")
	relayer2 := common.HexToAddress("0xBBBB2222BBBB2222BBBB2222BBBB2222BBBB2222")

	// Simulate both relayers racing to claim the same message
	winner, receipt, losers, dupErrors := relayerEngine.CompeteRelayers(
		*msg, proof, commitData.CommitRoot, commitData.Cert,
		[]common.Address{relayer1, relayer2},
	)

	// Winner verification
	assert.Equal(t, relayer1, winner)
	require.NotNil(t, receipt)
	assert.Equal(t, MessageStatusSuccess, receipt.Status)
	assert.Equal(t, tipAmount, receipt.TipCollected)

	// Verify tip disbursed on destination chain
	destEngine := chains[102]
	assert.Equal(t, tipAmount, destEngine.RelayerBalances[relayer1])

	// Loser verification (Relayer 2 must be rejected cleanly with ErrAlreadyClaimed)
	assert.Len(t, losers, 1)
	assert.Equal(t, relayer2, losers[0])
	require.Len(t, dupErrors, 1)
	assert.ErrorIs(t, dupErrors[0], ErrAlreadyClaimed)

	// Verify relayer2 received NO tip (zero double-spending)
	assert.Nil(t, destEngine.RelayerBalances[relayer2])
}

// ─────────────────────────────────────────────────────────────────────────────
// P4.1: T1 Devnet 8-Scenario Test Suite (Definition of Done)
// ─────────────────────────────────────────────────────────────────────────────

func TestRelayer_Scenario10_1_NativeTransferViaReserve(t *testing.T) {
	relayerEngine, chains := setupTestRelayerNetwork()

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	txHash := common.HexToHash("0x1111111100000000000000000000000000000000000000000000000000000001")

	transferAmount := big.NewInt(100)
	tipAmount := big.NewInt(5)

	params := OutboundParams{
		DestChainID: 102,
		Target:      recipient,
		Payload:     nil,
		AssetID:     big.NewInt(0),
		Value:       transferAmount,
		Tip:         tipAmount,
		HopCount:    1,
		Ordered:     false,
	}

	// Step 1: User calls outbound on Chain A (101)
	msg, err := relayerEngine.SubmitOutbound(101, sender, params, txHash)
	require.NoError(t, err)
	assert.Equal(t, MessageStatusPending, chains[101].GetMessageStatus(txHash))

	// Step 2: Certified commit generated on Chain A
	commitData, err := relayerEngine.CertifyCommit(101, 1, []CrossChainMessage{*msg}, nil)
	require.NoError(t, err)

	// Step 3 & 4 & 5: Relayer carries cert to Reserve (1000), checks ceiling, forwards to Chain B (102)
	receipts, err := relayerEngine.RelayCommit(101, commitData.CommitRoot, relayerEngine.Config.RelayerAddress)
	require.NoError(t, err)
	require.Len(t, receipts, 1)

	// Verification
	receipt := receipts[0]
	assert.Equal(t, MessageStatusSuccess, receipt.Status)
	assert.Equal(t, 2, len(receipt.Routes)) // 101 -> 1000 -> 102

	// Reserve allocation for Chain 101 reduced by 100 (5000 - 100 = 4900)
	reserveEngine := chains[1000]
	assert.Equal(t, big.NewInt(4900), reserveEngine.SupplyLedger.PerChainAllocation[101])

	// Relayer collected tip
	assert.Equal(t, tipAmount, chains[102].RelayerBalances[relayerEngine.Config.RelayerAddress])
}

func TestRelayer_Scenario10_2_ContractCallWithValueAndOriginalSender(t *testing.T) {
	relayerEngine, chains := setupTestRelayerNetwork()

	sender := common.HexToAddress("0xUSERUSERUSERUSERUSERUSERUSERUSERUSERUSER")
	targetContract := common.HexToAddress("0xGAMEGAMEGAMEGAMEGAMEGAMEGAMEGAMEGAMEGAME")
	txHash := common.HexToHash("0x2222222200000000000000000000000000000000000000000000000000000002")

	params := OutboundParams{
		DestChainID: 102,
		Target:      targetContract,
		Payload:     []byte("buyItem(sword_id=42)"),
		AssetID:     big.NewInt(0),
		Value:       big.NewInt(250),
		Tip:         big.NewInt(2),
		HopCount:    1,
		Ordered:     false,
	}

	msg, err := relayerEngine.SubmitOutbound(101, sender, params, txHash)
	require.NoError(t, err)

	commitData, err := relayerEngine.CertifyCommit(101, 1, []CrossChainMessage{*msg}, nil)
	require.NoError(t, err)

	receipts, err := relayerEngine.RelayCommit(101, commitData.CommitRoot, relayerEngine.Config.RelayerAddress)
	require.NoError(t, err)
	require.Len(t, receipts, 1)

	assert.Equal(t, MessageStatusSuccess, receipts[0].Status)
	assert.Equal(t, MessageStatusSuccess, chains[102].GetMessageStatus(txHash))
}

func TestRelayer_Scenario10_3_ContractCallFailedAndAutomatedRefund(t *testing.T) {
	relayerEngine, chains := setupTestRelayerNetwork()

	sender := common.HexToAddress("0xUSERUSERUSERUSERUSERUSERUSERUSERUSERUSER")
	targetContract := common.HexToAddress("0xOUTOFSTOCKOUTOFSTOCKOUTOFSTOCKOUTOFSTOC")
	txHash := common.HexToHash("0x3333333300000000000000000000000000000000000000000000000000000003")

	refundAmount := big.NewInt(300)
	params := OutboundParams{
		DestChainID: 102,
		Target:      targetContract,
		Payload:     []byte("buyItem(sold_out)"),
		AssetID:     big.NewInt(0),
		Value:       refundAmount,
		Tip:         big.NewInt(1),
		HopCount:    1,
		Ordered:     false,
	}

	msg, err := relayerEngine.SubmitOutbound(101, sender, params, txHash)
	require.NoError(t, err)

	// Simulate contract failure at destination: Relayer executes automated refund pipeline
	err = relayerEngine.ProcessRefund(101, 102, msg.MessageID, sender, refundAmount, true)
	require.NoError(t, err)

	// Chain A reflects refunded status
	assert.Equal(t, MessageStatusRefunded, chains[101].GetMessageStatus(msg.MessageID))

	// Double refund protection: attempting a 2nd refund MUST fail
	errDup := relayerEngine.ProcessRefund(101, 102, msg.MessageID, sender, refundAmount, true)
	assert.ErrorIs(t, errDup, ErrInvalidRefundState)
}

func TestRelayer_Scenario10_4_DestinationOfflinePendingZeroFork(t *testing.T) {
	relayerEngine, chains := setupTestRelayerNetwork()

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	txHash := common.HexToHash("0x4444444400000000000000000000000000000000000000000000000000000004")

	params := OutboundParams{
		DestChainID: 102,
		Target:      recipient,
		Payload:     nil,
		AssetID:     big.NewInt(0),
		Value:       big.NewInt(50),
		Tip:         big.NewInt(1),
		HopCount:    1,
		Ordered:     false,
	}

	msg, err := relayerEngine.SubmitOutbound(101, sender, params, txHash)
	require.NoError(t, err)

	commitData, err := relayerEngine.CertifyCommit(101, 1, []CrossChainMessage{*msg}, nil)
	require.NoError(t, err)

	// Simulate Chain B is temporarily OFFLINE (maintenance / network partition)
	relayerEngine.SetChainOffline(102, true)

	// Relayer attempt fails with ErrDestinationOffline, message stays strictly PENDING (Zero-Fork invariant)
	_, errOffline := relayerEngine.RelayCommit(101, commitData.CommitRoot, relayerEngine.Config.RelayerAddress)
	assert.ErrorIs(t, errOffline, ErrDestinationOffline)
	assert.Equal(t, MessageStatusPending, chains[101].GetMessageStatus(txHash))

	// Chain B comes back ONLINE
	relayerEngine.SetChainOffline(102, false)

	// Relayer retries and successfully processes the pending message
	receipts, errOnline := relayerEngine.RelayCommit(101, commitData.CommitRoot, relayerEngine.Config.RelayerAddress)
	require.NoError(t, errOnline)
	require.Len(t, receipts, 1)
	assert.Equal(t, MessageStatusSuccess, receipts[0].Status)
}

func TestRelayer_Scenario10_5_TwoWayHopCountLoopGuard(t *testing.T) {
	relayerEngine, _ := setupTestRelayerNetwork()

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")

	// Step 1: Hop count = 6 -> MUST SUCCEED
	paramsHop6 := OutboundParams{
		DestChainID: 102,
		Target:      target,
		Payload:     []byte("ping"),
		AssetID:     big.NewInt(0),
		Value:       big.NewInt(0),
		Tip:         big.NewInt(1),
		HopCount:    6,
		Ordered:     false,
	}
	_, err6 := relayerEngine.SubmitOutbound(101, sender, paramsHop6, common.HexToHash("0x5555555500000000000000000000000000000000000000000000000000000006"))
	require.NoError(t, err6)

	// Step 2: Hop count = 7 -> MUST BE HARD REJECTED (Section 2.6.2 & 10.5)
	paramsHop7 := OutboundParams{
		DestChainID: 102,
		Target:      target,
		Payload:     []byte("infinite_loop"),
		AssetID:     big.NewInt(0),
		Value:       big.NewInt(0),
		Tip:         big.NewInt(1),
		HopCount:    7,
		Ordered:     false,
	}
	_, err7 := relayerEngine.SubmitOutbound(101, sender, paramsHop7, common.HexToHash("0x5555555500000000000000000000000000000000000000000000000000000007"))
	assert.ErrorIs(t, err7, ErrHopCountExceeded)
}

func TestRelayer_Scenario10_6_OnboardNewChainViaGovernance(t *testing.T) {
	_, chains := setupTestRelayerNetwork()

	// 4 active chains: 1000, 101, 102, 103 -> Quorum >= 2/3 of 4 chains = 3 chains
	activeChains := []uint64{1000, 101, 102, 103}
	timelock := uint64(72 * 3600)
	gov := NewGovernanceEngineWithTimelock(activeChains, timelock)

	newChainPayload := []byte(`{"chain_id": 104, "name": "Chain D"}`)
	propID, err := gov.Propose(ProposalRegisterChain, newChainPayload, 1000)
	require.NoError(t, err)

	// Vote by Chain 1000, 101, 102 (3 votes >= 3)
	_, err = gov.Vote(propID, 1000, 1050)
	require.NoError(t, err)
	_, err = gov.Vote(propID, 101, 1060)
	require.NoError(t, err)
	status, err := gov.Vote(propID, 102, 1070)
	require.NoError(t, err)
	assert.Equal(t, ProposalStatusTimelocked, status)

	// Cannot execute before 72h delay
	_, errEarly := gov.Execute(propID, 1070+100)
	assert.ErrorIs(t, errEarly, ErrTimelockNotExpired)

	// Executes successfully after 72h delay
	_, errExec := gov.Execute(propID, 1070+timelock+1)
	require.NoError(t, errExec)

	// Register Chain 104 in Reserve registry with 0 initial allocation
	kp104 := bls.GenerateKeyPair()
	chains[1000].ChainRegistry[104] = ChainRegistry{
		ChainID:         104,
		Epoch:           1,
		QuorumThreshold: 6667,
		Committee:       []ValidatorEntry{{PubkeyBLS: kp104.PublicKey().Bytes(), Stake: 100}},
	}
	chains[1000].SupplyLedger.PerChainAllocation[104] = big.NewInt(0)

	assert.Equal(t, big.NewInt(0), chains[1000].SupplyLedger.PerChainAllocation[104])
}

func TestRelayer_Scenario10_7_AdversarialOverdrawAttackBlocked(t *testing.T) {
	relayerEngine, chains := setupTestRelayerNetwork()

	attacker := common.HexToAddress("0xATTACKERATTACKERATTACKERATTACKERATTACKER")
	recipient := common.HexToAddress("0xRECEIVERRECEIVERRECEIVERRECEIVERRECEIVER")
	txHash := common.HexToHash("0x7777777700000000000000000000000000000000000000000000000000000007")

	// Chain 103 only has 500 allocation. Attacker attempts to withdraw 1,000,000 native coin
	maliciousAmount := big.NewInt(1_000_000)
	params := OutboundParams{
		DestChainID: 102,
		Target:      recipient,
		Payload:     nil,
		AssetID:     big.NewInt(0),
		Value:       maliciousAmount,
		Tip:         big.NewInt(10),
		HopCount:    1,
		Ordered:     false,
	}

	msg, err := relayerEngine.SubmitOutbound(103, attacker, params, txHash)
	require.NoError(t, err)

	commitData, err := relayerEngine.CertifyCommit(103, 1, []CrossChainMessage{*msg}, nil)
	require.NoError(t, err)

	// Reserve MUST reject the overdrawn commit because requested (1,000,000) > available (500)
	_, errOverdraw := relayerEngine.RelayCommit(103, commitData.CommitRoot, relayerEngine.Config.RelayerAddress)
	assert.Error(t, errOverdraw)
	assert.ErrorIs(t, errOverdraw, ErrAllocationExceeded)

	// Allocation on Reserve for Chain 103 remains untouched at 500
	assert.Equal(t, big.NewInt(500), chains[1000].SupplyLedger.PerChainAllocation[103])
}

func TestRelayer_Scenario10_8_DeadChainRecovery(t *testing.T) {
	_, chains := setupTestRelayerNetwork()

	reserveEngine := chains[1000]
	deadChainID := uint64(103)

	// 1. Declare chain dead on Reserve
	reserveEngine.DeadChains[deadChainID] = true

	victimAccount := common.HexToAddress("0xVICTIMVICTIMVICTIMVICTIMVICTIMVICTIMVICT")
	victimBalance := big.NewInt(250)

	// Leaf encoding
	leaf := AccountLeaf{
		Account: victimAccount,
		Balance: victimBalance,
	}
	leafBytes, _ := json.Marshal(leaf)
	leafHash := Keccak256(leafBytes)

	// Set state root on Reserve registry to leafHash for test
	reg := reserveEngine.ChainRegistry[deadChainID]
	reg.StateRoot = leafHash
	reserveEngine.ChainRegistry[deadChainID] = reg

	proof := MerkleProof{
		LeafIndex: 0,
		Siblings:  []common.Hash{},
	}

	// 2. User claims balance on Reserve
	errClaim := reserveEngine.ClaimDeadChainBalance(deadChainID, victimAccount, victimBalance, proof, leafHash)
	require.NoError(t, errClaim)

	// 3. Double claim MUST fail
	errDouble := reserveEngine.ClaimDeadChainBalance(deadChainID, victimAccount, victimBalance, proof, leafHash)
	assert.ErrorIs(t, errDouble, ErrDeadChainAlreadyClaimed)
}
