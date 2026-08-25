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

	// aggregateAmount is now provably bound to commitRoot via a real AggregateValueLeaf Merkle
	// proof (Section 2.3.1/11.2, risk #20) — a single-leaf tree here (proof = no siblings) is a
	// faithful minimal case: commitRoot IS the leaf hash.
	signFor := func(amount *big.Int) (common.Hash, QuorumCert) {
		leaf := AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: amount}
		root := HashAggregateValueLeaf(leaf)
		commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), root.Bytes()...)
		sig := bls.Sign(kp.PrivateKey(), commitMsg)
		return root, QuorumCert{Epoch: 5, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x0F}}
	}

	// Attack Case (Scenario 10.7): aggregateAmount = 6000 > available allocation = 5000 -> REJECT
	rootAttack, certAttack := signFor(big.NewInt(6000))
	_, errAttack := engine.AttestCommit(101, rootAttack, big.NewInt(6000), big.NewInt(0), MerkleProof{}, certAttack)
	assert.ErrorIs(t, errAttack, ErrAllocationExceeded)

	// Valid Case: aggregateAmount = 2000 <= 5000 -> Succeeded & Deducts allocation to 3000
	rootValid, certValid := signFor(big.NewInt(2000))
	attested, errValid := engine.AttestCommit(101, rootValid, big.NewInt(2000), big.NewInt(0), MerkleProof{}, certValid)
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

	commitRoot, layers, aggAmounts, aggIndex, errTree := BuildCommitTree([]CrossChainMessage{msg})
	require.NoError(t, errTree)
	proof := GetMerkleProof(layers, 0)
	aggregateProof := GetMerkleProof(layers, aggIndex["0"])

	// Attest commit first with real BLS signature
	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)
	cert := QuorumCert{
		Epoch:              5,
		AggregateSignature: sig.Bytes(),
		SignerBitmap:       []byte{0x0F},
	}
	_, errAttest := engine.AttestCommit(101, commitRoot, aggAmounts["0"], big.NewInt(0), aggregateProof, cert)
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

	// Deliberately fund the commit with a genuinely-proven aggregate leaf of 500, less than this
	// message's own value of 600 — this isolates ClaimMessage's hard-cap check (Section 2.3.1/11.6)
	// from AttestCommit's own binding check: a 2-leaf tree (message leaf + aggregate leaf) so both
	// get real, independent Merkle proofs into the same commitRoot.
	messageLeafHash := ComputeMessageLeafHash(msg)
	aggregateLeafHash := HashAggregateValueLeaf(AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: big.NewInt(500)})
	commitRoot := hashPair(messageLeafHash, aggregateLeafHash)
	proof := MerkleProof{LeafIndex: 0, Siblings: []common.Hash{aggregateLeafHash}}
	aggregateProof := MerkleProof{LeafIndex: 1, Siblings: []common.Hash{messageLeafHash}}

	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)
	cert := QuorumCert{
		Epoch:              5,
		AggregateSignature: sig.Bytes(),
		SignerBitmap:       []byte{0x0F},
	}
	_, errAttest := engine.AttestCommit(101, commitRoot, big.NewInt(500), big.NewInt(0), aggregateProof, cert)
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
	accountLeafHash := HashAccountLeaf(AccountLeaf{Account: account, Balance: big.NewInt(1000)})
	reg := engine.ChainRegistry[deadChainID]
	reg.AccountTreeRoot = accountLeafHash
	engine.ChainRegistry[deadChainID] = reg

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

func TestGateway_P2_2_MultiValidatorQuorumBitmap(t *testing.T) {
	// Setup 4 validators with 25 stake each (Total 100 stake, 2/3 threshold = 67)
	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()
	kp3 := bls.GenerateKeyPair()
	kp4 := bls.GenerateKeyPair()

	committee := []ValidatorEntry{
		{PubkeyBLS: kp1.PublicKey().Bytes(), Stake: 25},
		{PubkeyBLS: kp2.PublicKey().Bytes(), Stake: 25},
		{PubkeyBLS: kp3.PublicKey().Bytes(), Stake: 25},
		{PubkeyBLS: kp4.PublicKey().Bytes(), Stake: 25},
	}

	registry := map[uint64]ChainRegistry{
		201: {
			ChainID:         201,
			Epoch:           1,
			QuorumThreshold: 6667, // 66.67%
			Committee:       committee,
		},
	}

	ledger, err := NewGlobalSupplyLedger(big.NewInt(500_000), map[uint64]*big.Int{201: big.NewInt(500_000)})
	require.NoError(t, err)

	gateway := NewGatewayEngine(1000, registry, ledger)
	// aggregateAmount is now bound to commitRoot via a real Merkle proof (Section 2.3.1) — each
	// case's commitRoot is its declared amount's own single-leaf AggregateValueLeaf hash (proof =
	// no siblings), a faithful minimal case.
	commitRoot := HashAggregateValueLeaf(AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: big.NewInt(1000)})
	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)

	sig1 := bls.Sign(kp1.PrivateKey(), commitMsg)
	sig2 := bls.Sign(kp2.PrivateKey(), commitMsg)
	sig3 := bls.Sign(kp3.PrivateKey(), commitMsg)

	// Case 1: 3 out of 4 validators sign (Val 0, 1, 2) -> Stake = 75 >= 67 -> SUCCESS
	aggSig3 := bls.CreateAggregateSign([][]byte{sig1.Bytes(), sig2.Bytes(), sig3.Bytes()})
	cert3 := QuorumCert{
		Epoch:              1,
		AggregateSignature: aggSig3,
		SignerBitmap:       []byte{0x07}, // bits 0, 1, 2 set: 1 + 2 + 4 = 7
	}
	attested, err := gateway.AttestCommit(201, commitRoot, big.NewInt(1000), big.NewInt(0), MerkleProof{}, cert3)
	require.NoError(t, err)
	assert.Equal(t, commitRoot, attested.CommitRoot)

	// Case 2: Only 2 validators sign (Val 0, 1) -> Stake = 50 < 67 -> Quorum NOT reached
	commitRoot2 := HashAggregateValueLeaf(AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: big.NewInt(1001)})
	commitMsg2 := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot2.Bytes()...)
	sig1_2 := bls.Sign(kp1.PrivateKey(), commitMsg2)
	sig2_2 := bls.Sign(kp2.PrivateKey(), commitMsg2)
	aggSig2 := bls.CreateAggregateSign([][]byte{sig1_2.Bytes(), sig2_2.Bytes()})
	cert2 := QuorumCert{
		Epoch:              1,
		AggregateSignature: aggSig2,
		SignerBitmap:       []byte{0x03}, // bits 0, 1 set: 1 + 2 = 3
	}
	_, errQuorum := gateway.AttestCommit(201, commitRoot2, big.NewInt(1001), big.NewInt(0), MerkleProof{}, cert2)
	assert.ErrorIs(t, errQuorum, ErrQuorumNotReached)

	// Case 3: Bitmap claims 3 signers (0, 1, 2) but aggregate signature only contains 2 signers -> BLS Verify Fails
	certForged := QuorumCert{
		Epoch:              1,
		AggregateSignature: aggSig2,      // only 2 signatures aggregated
		SignerBitmap:       []byte{0x07}, // claims 3 signers
	}
	_, errBLS := gateway.AttestCommit(201, commitRoot2, big.NewInt(1001), big.NewInt(0), MerkleProof{}, certForged)
	assert.ErrorIs(t, errBLS, ErrInvalidBLSSignature)

	// Case 4: All 4 validators sign -> Stake = 100 >= 67 -> SUCCESS
	commitRoot4 := HashAggregateValueLeaf(AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: big.NewInt(2000)})
	commitMsg4 := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot4.Bytes()...)
	sig1_4 := bls.Sign(kp1.PrivateKey(), commitMsg4)
	sig2_4 := bls.Sign(kp2.PrivateKey(), commitMsg4)
	sig3_4 := bls.Sign(kp3.PrivateKey(), commitMsg4)
	sig4_4 := bls.Sign(kp4.PrivateKey(), commitMsg4)
	aggSig4 := bls.CreateAggregateSign([][]byte{sig1_4.Bytes(), sig2_4.Bytes(), sig3_4.Bytes(), sig4_4.Bytes()})
	cert4 := QuorumCert{
		Epoch:              1,
		AggregateSignature: aggSig4,
		SignerBitmap:       []byte{0x0F}, // bits 0, 1, 2, 3 set = 15
	}
	attested4, err4 := gateway.AttestCommit(201, commitRoot4, big.NewInt(2000), big.NewInt(0), MerkleProof{}, cert4)
	require.NoError(t, err4)
	assert.Equal(t, commitRoot4, attested4.CommitRoot)
}
