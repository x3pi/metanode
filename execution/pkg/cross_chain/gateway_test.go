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

	engine := NewGatewayEngine(102, registry, ledger)
	// C8 fix (2026-08-27): engine (chain 102) plays Reserve in this shared test setup, attesting
	// other chains' commits directly -- only a chain configured as its own Reserve may do that
	// for a nonzero-value commit.
	engine.ReserveChainID = 102
	return engine, kp
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

// TestGateway_ProposalAllocateSupply_UnblocksAttestCommit proves the fix for a real dead end
// found via live 2-node RPC testing: neither BootstrapFoundingChains nor
// ExecuteGovernanceProposal's ProposalRegisterChain case ever touches SupplyLedger, and
// production always constructs it with genesis_total_supply=0 and an empty allocation map
// (gateway_handler.go's loadGatewayEngine) — so a freshly onboarded chain's attestCommit
// rejects with "available 0" forever, with no prior governance-reachable way to fund it.
// ProposalAllocateSupply (GrantAllocation) closes that gap through the same propose/vote/
// timelock/execute path as every other governance action.
func TestGateway_ProposalAllocateSupply_UnblocksAttestCommit(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	engine.EnsureGovernance()
	// setupTestGatewayEngine sets engine.ReserveChainID = 102 (engine's own LocalChainID) —
	// engine plays Reserve for this test.

	// Chain 103: registered but never allocated — the exact state a real ProposalRegisterChain
	// leaves a brand-new chain in today.
	popSig := PopSign(kp.PrivateKey(), kp.PublicKey())
	engine.ChainRegistry[103] = ChainRegistry{
		ChainID:         103,
		Committee:       []ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 10000, PopSignature: popSig.Bytes()}},
		Epoch:           1,
		QuorumThreshold: 6667,
	}
	require.Equal(t, big.NewInt(0), engine.SupplyLedger.GetAllocation(103))

	signFor := func(amount *big.Int) (common.Hash, QuorumCert) {
		leaf := AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: amount}
		root := HashAggregateValueLeaf(leaf)
		commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), root.Bytes()...)
		sig := bls.Sign(kp.PrivateKey(), commitMsg)
		return root, QuorumCert{Epoch: 1, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x0F}}
	}

	// Before anything: even a modest amount is rejected, ceiling is 0.
	root, cert := signFor(big.NewInt(100))
	_, errBefore := engine.AttestCommit(103, root, big.NewInt(100), big.NewInt(0), MerkleProof{}, cert)
	assert.ErrorIs(t, errBefore, ErrAllocationExceeded)

	// C7 fix (2026-08-27): ProposalAllocateSupply attempting to grant a REGULAR chain (103, not
	// Reserve) is now rejected outright — this used to be exactly how a Sybil-controlled
	// governance majority could mint itself free money (note/cross_chain_attack_scenario_catalog.md
	// item C7). It only ever mints the one-time genesis supply, and only to this chain's own
	// configured Reserve (102).
	badGrant := AllocationGrantPayload{ChainID: 103, Amount: big.NewInt(1000)}
	badPayload, err := json.Marshal(badGrant)
	require.NoError(t, err)
	badProposalID, err := engine.Governance.Propose(ProposalAllocateSupply, badPayload, 1000)
	require.NoError(t, err)
	_, err = engine.Governance.Vote(badProposalID, 101, 1010)
	require.NoError(t, err)
	_, errBadGrant := engine.ExecuteGovernanceProposal(badProposalID, 1010+DefaultGovernanceTimelockSeconds+1)
	assert.ErrorIs(t, errBadGrant, ErrOnlyReserveMayMint)
	assert.Equal(t, big.NewInt(0), engine.SupplyLedger.GetAllocation(103), "rejected grant must not move any allocation")

	// setupTestGatewayEngine's fixture already constructs the ledger with a nonzero
	// GenesisTotalSupply (102 already holds 5000, as if its one-time genesis mint already
	// happened) — so attempting ProposalAllocateSupply again, even correctly targeting Reserve
	// (102) itself, must now fail: genesis mint is one-time only, never a repeatable grant.
	timelockProposalID, err := engine.Governance.Propose(ProposalAllocateSupply, json.RawMessage(`{"chain_id":102,"amount":1000}`), 1000)
	require.NoError(t, err)
	_, err = engine.Governance.Vote(timelockProposalID, 101, 1010)
	require.NoError(t, err)
	_, errEarly := engine.ExecuteGovernanceProposal(timelockProposalID, 1010+100)
	assert.ErrorIs(t, errEarly, ErrTimelockNotExpired)
	_, errSecondMint := engine.ExecuteGovernanceProposal(timelockProposalID, 1010+DefaultGovernanceTimelockSeconds+1)
	assert.ErrorIs(t, errSecondMint, ErrGenesisAlreadyMinted)
	assert.Equal(t, big.NewInt(5000), engine.SupplyLedger.GetAllocation(102), "rejected re-mint must not change Reserve's existing allocation")

	// Correct flow: Reserve (102) transfers part of its own already-minted supply (from the
	// fixture's initial 5000) to chain 103 via ProposalTransferAllocation — safe and
	// repeatable, moves existing supply only, never mints.
	transfer := AllocationTransferPayload{FromChainID: 102, ToChainID: 103, Amount: big.NewInt(1000)}
	transferPayload, err := json.Marshal(transfer)
	require.NoError(t, err)
	transferProposalID, err := engine.Governance.Propose(ProposalTransferAllocation, transferPayload, 3000)
	require.NoError(t, err)
	_, err = engine.Governance.Vote(transferProposalID, 101, 3010)
	require.NoError(t, err)
	_, err = engine.ExecuteGovernanceProposal(transferProposalID, 3010+DefaultGovernanceTimelockSeconds+1)
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(4000), engine.SupplyLedger.GetAllocation(102), "5000 fixture balance minus the 1000 transferred out")
	assert.Equal(t, big.NewInt(1000), engine.SupplyLedger.GetAllocation(103))

	// The exact same commit now succeeds, and debits normally afterward.
	attested, errAfter := engine.AttestCommit(103, root, big.NewInt(100), big.NewInt(0), MerkleProof{}, cert)
	require.NoError(t, errAfter)
	assert.Equal(t, big.NewInt(100), attested.FundedAmount)
	assert.Equal(t, big.NewInt(900), engine.SupplyLedger.GetAllocation(103))
}

// TestGlobalSupplyLedger_GrantAllocation is the focused unit test for the ledger primitive
// itself: increases both the target's allocation and genesis_total_supply together (unlike
// TransferAllocation, which redistributes existing allocation and would need a pre-funded
// reserve chain that nothing in production ever seeds), and keeps VerifyInvariant() true.
func TestGlobalSupplyLedger_GrantAllocation(t *testing.T) {
	ledger, err := NewGlobalSupplyLedger(big.NewInt(1000), map[uint64]*big.Int{1: big.NewInt(1000)})
	require.NoError(t, err)

	require.NoError(t, ledger.GrantAllocation(2, big.NewInt(500)))
	assert.Equal(t, big.NewInt(500), ledger.GetAllocation(2))
	assert.Equal(t, big.NewInt(1500), ledger.GenesisTotalSupply)
	assert.True(t, ledger.VerifyInvariant())

	// Granting again to the same chain accumulates rather than overwriting.
	require.NoError(t, ledger.GrantAllocation(2, big.NewInt(250)))
	assert.Equal(t, big.NewInt(750), ledger.GetAllocation(2))
	assert.Equal(t, big.NewInt(1750), ledger.GenesisTotalSupply)
	assert.True(t, ledger.VerifyInvariant())

	// Nil/zero/negative amounts are rejected.
	assert.ErrorIs(t, ledger.GrantAllocation(3, nil), ErrNilAmount)
	assert.ErrorIs(t, ledger.GrantAllocation(3, big.NewInt(0)), ErrNilAmount)
	assert.ErrorIs(t, ledger.GrantAllocation(3, big.NewInt(-1)), ErrNilAmount)
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

// TestGateway_CreditReserveAllocation_2HopDestCredit reproduces, at the unit level, the live-
// verified 2026-09-04 finding: in a private-to-private A->Reserve->B relay, ClaimMessage's own
// PerChainAllocation credit lands on B's own separate, non-authoritative ledger copy, never on
// Reserve's -- leaving Reserve's authoritative Σ PerChainAllocation permanently short by every
// such transfer's value. CreditReserveAllocation is the fix: called directly against Reserve's own
// engine (this test's `engine` plays Reserve, per setupTestGatewayEngine), it must credit the
// message's real DestChainID (103 here -- deliberately NOT engine.LocalChainID/102, to prove this
// does not depend on the claim itself having happened locally), be idempotent against a repeat
// call, and refuse to run at all on a non-Reserve node.
func TestGateway_CreditReserveAllocation_2HopDestCredit(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")

	msg := CrossChainMessage{
		MessageID:     common.HexToHash("0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"),
		SourceChainID: 101,
		DestChainID:   103, // a THIRD chain, distinct from both the source (101) and Reserve (102)
		Sender:        sender,
		Target:        target,
		Payload:       []byte{},
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(500),
		Sequence:      1,
		Tip:           big.NewInt(0),
		HopCount:      1,
	}

	commitRoot, layers, aggAmounts, aggIndex, errTree := BuildCommitTree([]CrossChainMessage{msg})
	require.NoError(t, errTree)
	proof := GetMerkleProof(layers, 0)
	aggregateProof := GetMerkleProof(layers, aggIndex["0"])

	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)
	cert := QuorumCert{
		Epoch:              5,
		AggregateSignature: sig.Bytes(),
		SignerBitmap:       []byte{0x0F},
	}
	// Step 1 (source debit, on Reserve): matches the real relayer's first leg.
	_, errAttest := engine.AttestCommit(101, commitRoot, aggAmounts["0"], big.NewInt(0), aggregateProof, cert)
	require.NoError(t, errAttest)
	preCreditSourceAlloc := engine.SupplyLedger.GetAllocation(101)
	assert.Equal(t, big.NewInt(4500), preCreditSourceAlloc) // 5000 - 500

	assert.Equal(t, big.NewInt(0), engine.SupplyLedger.GetAllocation(103))

	// Step 3 (this test's actual subject): destination credit, on Reserve -- deliberately called
	// BEFORE any local claim on chain 103 would happen in the real 2-hop flow, since Reserve does
	// not depend on or wait for that separate node's own state.
	errCredit := engine.CreditReserveAllocation(msg, proof, commitRoot)
	require.NoError(t, errCredit)
	assert.Equal(t, big.NewInt(500), engine.SupplyLedger.GetAllocation(103))
	// Source side must be untouched by the credit step (only the debit above touches it).
	assert.Equal(t, preCreditSourceAlloc, engine.SupplyLedger.GetAllocation(101))

	// Idempotent: a second call (retry / second relayer) must NOT double-credit.
	errCreditAgain := engine.CreditReserveAllocation(msg, proof, commitRoot)
	require.NoError(t, errCreditAgain)
	assert.Equal(t, big.NewInt(500), engine.SupplyLedger.GetAllocation(103))

	// Fails closed off Reserve's own node.
	nonReserveEngine, _ := setupTestGatewayEngine()
	nonReserveEngine.ReserveChainID = 999 // this engine is chain 102, but Reserve is configured as 999
	errWrongNode := nonReserveEngine.CreditReserveAllocation(msg, proof, commitRoot)
	assert.ErrorIs(t, errWrongNode, ErrNonReserveCeilingAttestation)

	// Invalid Merkle proof must be rejected, not silently credited.
	badEngine, kpBad := setupTestGatewayEngine()
	badMsg := msg
	badMsg.MessageID = common.HexToHash("0xDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD")
	badCommitRoot, badLayers, badAggAmounts, badAggIndex, errBadTree := BuildCommitTree([]CrossChainMessage{badMsg})
	require.NoError(t, errBadTree)
	badAggregateProof := GetMerkleProof(badLayers, badAggIndex["0"])
	badSig := bls.Sign(kpBad.PrivateKey(), append([]byte("COMMIT_ROOT_ATTEST_V1:"), badCommitRoot.Bytes()...))
	badCert := QuorumCert{Epoch: 5, AggregateSignature: badSig.Bytes(), SignerBitmap: []byte{0x0F}}
	_, errBadAttest := badEngine.AttestCommit(101, badCommitRoot, badAggAmounts["0"], big.NewInt(0), badAggregateProof, badCert)
	require.NoError(t, errBadAttest)
	wrongProof := MerkleProof{LeafIndex: 0, Siblings: []common.Hash{common.HexToHash("0xFF")}}
	errBadProof := badEngine.CreditReserveAllocation(badMsg, wrongProof, badCommitRoot)
	assert.ErrorIs(t, errBadProof, ErrInvalidMerkleProof)
	assert.Equal(t, big.NewInt(0), badEngine.SupplyLedger.GetAllocation(103))
}

func TestGateway_P2_4_RefundPathwayAndSupplyRestoration(t *testing.T) {
	kp101 := bls.GenerateKeyPair()
	kp102 := bls.GenerateKeyPair()
	pop101 := PopSign(kp101.PrivateKey(), kp101.PublicKey())
	pop102 := PopSign(kp102.PrivateKey(), kp102.PublicKey())

	registry := map[uint64]ChainRegistry{
		101: {
			ChainID: 101,
			Committee: []ValidatorEntry{
				{PubkeyBLS: kp101.BytesPublicKey(), Stake: 10000, PopSignature: pop101.Bytes()},
			},
			Epoch:           5,
			QuorumThreshold: 6667,
		},
		102: {
			ChainID: 102,
			Committee: []ValidatorEntry{
				{PubkeyBLS: kp102.BytesPublicKey(), Stake: 10000, PopSignature: pop102.Bytes()},
			},
			Epoch:           5,
			QuorumThreshold: 6667,
		},
	}

	allocs := map[uint64]*big.Int{
		101: big.NewInt(5000),
		102: big.NewInt(5000),
	}
	ledger, err := NewGlobalSupplyLedger(big.NewInt(10000), allocs)
	require.NoError(t, err)

	engine := NewGatewayEngine(101, registry, ledger)
	engine.ReserveChainID = 101 // C8 fix: engine plays Reserve, attesting its own outbound commit

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	txHash := common.HexToHash("0x7777777777777777777777777777777777777777777777777777777777777777")

	msg, err := engine.Outbound(sender, OutboundParams{
		DestChainID: 102,
		Target:      target,
		Payload:     []byte{1, 2, 3},
		AssetID:     big.NewInt(0),
		Value:       big.NewInt(100),
		Tip:         big.NewInt(5),
		HopCount:    1,
		Ordered:     false,
	}, txHash)
	require.NoError(t, err)

	// Build commit tree with the message
	messages := []CrossChainMessage{*msg}
	commitRoot, layers, aggAmounts, aggIndex, err := BuildCommitTree(messages)
	require.NoError(t, err)
	proof := GetMerkleProof(layers, 0)
	aggregateProof := GetMerkleProof(layers, aggIndex["0"])

	// Attest commit on chain 101
	commitMsg := ComputeCommitRootAttestMessage(commitRoot)
	sig101 := bls.Sign(kp101.PrivateKey(), commitMsg)
	cert101 := QuorumCert{
		Epoch:              5,
		AggregateSignature: sig101.Bytes(),
		SignerBitmap:       []byte{0x01},
	}
	_, err = engine.AttestCommit(101, commitRoot, aggAmounts["0"], big.NewInt(0), aggregateProof, cert101)
	require.NoError(t, err)

	// Destination (102) fails and signs failure cert
	failMsg := ComputeMessageFailureAttestMessage(msg.MessageID, 102)
	failSig := bls.Sign(kp102.PrivateKey(), failMsg)
	destFailureCert := QuorumCert{
		Epoch:              5,
		AggregateSignature: failSig.Bytes(),
		SignerBitmap:       []byte{0x01},
	}

	allocBefore := new(big.Int).Set(engine.SupplyLedger.PerChainAllocation[101])

	// Attack 1: Forged/unused messageID -> MUST REJECT
	forgedMsg := *msg
	forgedMsg.MessageID = common.HexToHash("0xFAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	errForged := engine.Refund(forgedMsg, proof, commitRoot, destFailureCert)
	assert.Error(t, errForged, "Refund with forged messageID must be rejected")

	// Attack 2: Forged amount in message -> Merkle proof MUST REJECT
	forgedAmountMsg := *msg
	forgedAmountMsg.Value = big.NewInt(999999)
	errForgedAmount := engine.Refund(forgedAmountMsg, proof, commitRoot, destFailureCert)
	assert.ErrorIs(t, errForgedAmount, ErrInvalidMerkleProof, "Refund with forged amount must fail Merkle check")

	// Attack 3: Forged destination failure cert -> BLS verification MUST REJECT
	forgedCert := destFailureCert
	forgedCert.AggregateSignature = bls.Sign(bls.GenerateKeyPair().PrivateKey(), failMsg).Bytes()
	errForgedCert := engine.Refund(*msg, proof, commitRoot, forgedCert)
	assert.ErrorIs(t, errForgedCert, ErrInvalidRefundProof, "Refund with forged failure cert must be rejected")

	// Valid Refund -> Success and Restores Allocation (+100)
	err = engine.Refund(*msg, proof, commitRoot, destFailureCert)
	require.NoError(t, err)
	assert.Equal(t, MessageStatusRefunded, engine.GetMessageStatus(msg.MessageID))
	assert.Equal(t, new(big.Int).Add(allocBefore, big.NewInt(100)), engine.SupplyLedger.PerChainAllocation[101])

	// Second Refund -> MUST REJECT (Already refunded)
	errDup := engine.Refund(*msg, proof, commitRoot, destFailureCert)
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
	gateway.ReserveChainID = 1000 // C8 fix: gateway plays Reserve, attesting chain 201's commit
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

func TestGateway_ProposalUpdateCommittee_Lifecycle(t *testing.T) {
	engine, kp1 := setupTestGatewayEngine()
	engine.EnsureGovernance()

	kp2 := bls.GenerateKeyPair()
	popSig2 := PopSign(kp2.PrivateKey(), kp2.PublicKey())

	newCommittee := []ValidatorEntry{
		{PubkeyBLS: kp1.BytesPublicKey(), Stake: 6000, PopSignature: PopSign(kp1.PrivateKey(), kp1.PublicKey()).Bytes()},
		{PubkeyBLS: kp2.BytesPublicKey(), Stake: 4000, PopSignature: popSig2.Bytes()},
	}

	payloadObj := UpdateCommitteePayload{
		ChainID:         101,
		NewEpoch:        2,
		NewCommittee:    newCommittee,
		QuorumThreshold: 6700,
		StateRoot:       common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		AccountTreeRoot: common.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
	}
	payloadBytes, err := json.Marshal(payloadObj)
	require.NoError(t, err)

	proposalID, err := engine.Governance.Propose(ProposalUpdateCommittee, payloadBytes, 1000)
	require.NoError(t, err)

	_, err = engine.Governance.Vote(proposalID, 101, 1010)
	require.NoError(t, err)

	// Timelock guard check
	_, errEarly := engine.ExecuteGovernanceProposal(proposalID, 1010+100)
	assert.ErrorIs(t, errEarly, ErrTimelockNotExpired)

	// Execute after timelock
	_, err = engine.ExecuteGovernanceProposal(proposalID, 1010+DefaultGovernanceTimelockSeconds+1)
	require.NoError(t, err)

	// Verify updated ChainRegistry state
	reg := engine.ChainRegistry[101]
	assert.Equal(t, uint64(2), reg.Epoch)
	assert.Equal(t, uint64(6700), reg.QuorumThreshold)
	assert.Equal(t, payloadObj.StateRoot, reg.StateRoot)
	assert.Equal(t, payloadObj.AccountTreeRoot, reg.AccountTreeRoot)
	assert.Equal(t, 2, len(reg.Committee))
	assert.Equal(t, kp1.BytesPublicKey(), reg.Committee[0].PubkeyBLS)
	assert.Equal(t, kp2.BytesPublicKey(), reg.Committee[1].PubkeyBLS)
}

func TestGateway_ProposalUpdateCommittee_RejectsInvalidPoP(t *testing.T) {
	engine, kp1 := setupTestGatewayEngine()
	engine.EnsureGovernance()

	kp2 := bls.GenerateKeyPair()
	badPopSig := make([]byte, 96) // Zeroed / invalid PoP signature

	newCommittee := []ValidatorEntry{
		{PubkeyBLS: kp1.BytesPublicKey(), Stake: 6000, PopSignature: PopSign(kp1.PrivateKey(), kp1.PublicKey()).Bytes()},
		{PubkeyBLS: kp2.BytesPublicKey(), Stake: 4000, PopSignature: badPopSig},
	}

	payloadObj := UpdateCommitteePayload{
		ChainID:      101,
		NewEpoch:     2,
		NewCommittee: newCommittee,
	}
	payloadBytes, err := json.Marshal(payloadObj)
	require.NoError(t, err)

	proposalID, err := engine.Governance.Propose(ProposalUpdateCommittee, payloadBytes, 1000)
	require.NoError(t, err)

	_, err = engine.Governance.Vote(proposalID, 101, 1010)
	require.NoError(t, err)

	// Execution must fail due to invalid PoP
	_, err = engine.ExecuteGovernanceProposal(proposalID, 1010+DefaultGovernanceTimelockSeconds+1)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ProposalUpdateCommittee")

	// Ensure registry was NOT modified
	reg := engine.ChainRegistry[101]
	assert.Equal(t, uint64(5), reg.Epoch)
	assert.Equal(t, 1, len(reg.Committee))
}

func TestGateway_ProposalUpdateCommittee_RejectsUnknownChain(t *testing.T) {
	engine, kp1 := setupTestGatewayEngine()
	engine.EnsureGovernance()

	newCommittee := []ValidatorEntry{
		{PubkeyBLS: kp1.BytesPublicKey(), Stake: 10000, PopSignature: PopSign(kp1.PrivateKey(), kp1.PublicKey()).Bytes()},
	}

	payloadObj := UpdateCommitteePayload{
		ChainID:      999, // Unknown chain
		NewEpoch:     2,
		NewCommittee: newCommittee,
	}
	payloadBytes, err := json.Marshal(payloadObj)
	require.NoError(t, err)

	proposalID, err := engine.Governance.Propose(ProposalUpdateCommittee, payloadBytes, 1000)
	require.NoError(t, err)

	_, err = engine.Governance.Vote(proposalID, 101, 1010)
	require.NoError(t, err)

	// Execution must fail due to unknown chain
	_, err = engine.ExecuteGovernanceProposal(proposalID, 1010+DefaultGovernanceTimelockSeconds+1)
	assert.ErrorIs(t, err, ErrUnknownChain)
}

// TestGateway_ProposalUpdateCommittee_RejectsSubBftQuorumThreshold is the regression test for a
// real gap found reviewing this same feature: QuorumThreshold was applied with no bounds check
// at all. VerifyQuorumCertAgainstRegistry treats it as the fraction of a committee's TOTAL STAKE
// required to sign before a QuorumCert verifies — a nonzero value below 2/3 lets a cert verify
// without Byzantine fault tolerance, i.e. a minority (even one low-stake signer) could forge a
// "valid" quorum for that chain's attestCommit()/vote() from then on.
func TestGateway_ProposalUpdateCommittee_RejectsSubBftQuorumThreshold(t *testing.T) {
	engine, kp1 := setupTestGatewayEngine()
	engine.EnsureGovernance()

	newCommittee := []ValidatorEntry{
		{PubkeyBLS: kp1.BytesPublicKey(), Stake: 10000, PopSignature: PopSign(kp1.PrivateKey(), kp1.PublicKey()).Bytes()},
	}

	// 3334 basis points = 33.34% -- well under the 2/3 BFT floor (6667).
	payloadObj := UpdateCommitteePayload{
		ChainID:         101,
		NewEpoch:        2,
		NewCommittee:    newCommittee,
		QuorumThreshold: 3334,
	}
	payloadBytes, err := json.Marshal(payloadObj)
	require.NoError(t, err)

	proposalID, err := engine.Governance.Propose(ProposalUpdateCommittee, payloadBytes, 1000)
	require.NoError(t, err)
	_, err = engine.Governance.Vote(proposalID, 101, 1010)
	require.NoError(t, err)

	_, err = engine.ExecuteGovernanceProposal(proposalID, 1010+DefaultGovernanceTimelockSeconds+1)
	assert.ErrorIs(t, err, ErrInvalidQuorumThreshold)

	// Registry must be untouched -- still the original committee/threshold from setupTestGatewayEngine.
	reg := engine.ChainRegistry[101]
	assert.Equal(t, uint64(5), reg.Epoch)
	assert.Equal(t, uint64(6667), reg.QuorumThreshold)
}

// TestGateway_ProposalRegisterChain_RejectsUnverifiedNonEmptyCommittee is the regression test
// for a real gap found reviewing ProposalUpdateCommittee: unlike BootstrapFoundingChains and
// ProposalUpdateCommittee (both of which verify PoP for every committee member), the older
// ProposalRegisterChain case accepted a NON-EMPTY committee wholesale with no PoP check at all
// -- a rogue-key attack surface identical to the one PoP exists to close everywhere else in this
// codebase. An EMPTY committee (registering routing metadata only, deferred committee via a
// later ProposalUpdateCommittee) is a real, pre-existing usage pattern and must still work.
func TestGateway_ProposalRegisterChain_RejectsUnverifiedNonEmptyCommittee(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	engine.EnsureGovernance()

	kpRogue := bls.GenerateKeyPair()
	forgedCommittee := []ValidatorEntry{
		{PubkeyBLS: kpRogue.BytesPublicKey(), Stake: 10000, PopSignature: make([]byte, 96)}, // zeroed/forged PoP
	}

	newChainReg := ChainRegistry{
		ChainID:   999,
		Epoch:     1,
		Committee: forgedCommittee,
	}
	payload, err := json.Marshal(newChainReg)
	require.NoError(t, err)

	proposalID, err := engine.Governance.Propose(ProposalRegisterChain, payload, 1000)
	require.NoError(t, err)
	_, err = engine.Governance.Vote(proposalID, 101, 1010)
	require.NoError(t, err)

	_, err = engine.ExecuteGovernanceProposal(proposalID, 1010+DefaultGovernanceTimelockSeconds+1)
	assert.ErrorIs(t, err, ErrPopVerifyFailed)

	_, exists := engine.ChainRegistry[999]
	assert.False(t, exists, "chain with an unverified committee must not be registered")
}

// TestGateway_ProposalRegisterChain_AllowsEmptyCommitteeDeferredToUpdate proves the legitimate
// pattern the fix above must not break: registering routing metadata with no committee yet,
// verified safe because VerifyQuorumCertAgainstRegistry fails closed (ErrEmptyCommittee) for
// that chain until a real committee is set via ProposalUpdateCommittee.
func TestGateway_ProposalRegisterChain_AllowsEmptyCommitteeDeferredToUpdate(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	engine.EnsureGovernance()

	newChainReg := ChainRegistry{
		ChainID:          104,
		Epoch:            1,
		QuorumThreshold:  6667,
		ArchivalEndpoint: "https://rpc.chain104.test",
	}
	payload, err := json.Marshal(newChainReg)
	require.NoError(t, err)

	proposalID, err := engine.Governance.Propose(ProposalRegisterChain, payload, 1000)
	require.NoError(t, err)
	_, err = engine.Governance.Vote(proposalID, 101, 1010)
	require.NoError(t, err)

	_, err = engine.ExecuteGovernanceProposal(proposalID, 1010+DefaultGovernanceTimelockSeconds+1)
	require.NoError(t, err)

	reg, exists := engine.ChainRegistry[104]
	require.True(t, exists)
	assert.Empty(t, reg.Committee)
}

// TestGateway_ProposalRegisterChain_RejectsSubBftQuorumThreshold mirrors the UpdateCommittee
// version above for the registration path.
func TestGateway_ProposalRegisterChain_RejectsSubBftQuorumThreshold(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	engine.EnsureGovernance()

	newChainReg := ChainRegistry{
		ChainID:         104,
		Epoch:           1,
		QuorumThreshold: 100, // 1% -- far under the 2/3 BFT floor
	}
	payload, err := json.Marshal(newChainReg)
	require.NoError(t, err)

	proposalID, err := engine.Governance.Propose(ProposalRegisterChain, payload, 1000)
	require.NoError(t, err)
	_, err = engine.Governance.Vote(proposalID, 101, 1010)
	require.NoError(t, err)

	_, err = engine.ExecuteGovernanceProposal(proposalID, 1010+DefaultGovernanceTimelockSeconds+1)
	assert.ErrorIs(t, err, ErrInvalidQuorumThreshold)

	_, exists := engine.ChainRegistry[104]
	assert.False(t, exists)
}

// TestGateway_ProposalRegisterChain_MinRegistrationStake is the regression test for the C6
// mitigation (2026-08-27, see note/cross_chain_attack_scenario_catalog.md item C6): opt-in via
// GatewayEngine.MinRegistrationStake, a candidate chain ID must already hold at least that much
// allocation before ProposalRegisterChain can execute for it, closing the previous gap where
// registration was fully decoupled from SupplyLedger.
func TestGateway_ProposalRegisterChain_MinRegistrationStake(t *testing.T) {
	newChainReg := func(chainID uint64) []byte {
		reg := ChainRegistry{ChainID: chainID, Epoch: 1, QuorumThreshold: 6667}
		payload, err := json.Marshal(reg)
		require.NoError(t, err)
		return payload
	}

	t.Run("unconfigured (zero) preserves old permissionless behavior", func(t *testing.T) {
		engine, _ := setupTestGatewayEngine()
		engine.EnsureGovernance()
		// engine.MinRegistrationStake left nil -- the default on every pre-2026-08-27 config.

		proposalID, err := engine.Governance.Propose(ProposalRegisterChain, newChainReg(104), 1000)
		require.NoError(t, err)
		_, err = engine.Governance.Vote(proposalID, 101, 1010)
		require.NoError(t, err)

		_, err = engine.ExecuteGovernanceProposal(proposalID, 1010+DefaultGovernanceTimelockSeconds+1)
		require.NoError(t, err)
		_, exists := engine.ChainRegistry[104]
		assert.True(t, exists, "unconfigured MinRegistrationStake must not block registration")
	})

	t.Run("configured, unfunded candidate fails closed", func(t *testing.T) {
		engine, _ := setupTestGatewayEngine()
		engine.EnsureGovernance()
		engine.MinRegistrationStake = big.NewInt(1000)

		proposalID, err := engine.Governance.Propose(ProposalRegisterChain, newChainReg(104), 1000)
		require.NoError(t, err)
		_, err = engine.Governance.Vote(proposalID, 101, 1010)
		require.NoError(t, err)

		_, err = engine.ExecuteGovernanceProposal(proposalID, 1010+DefaultGovernanceTimelockSeconds+1)
		assert.ErrorIs(t, err, ErrInsufficientRegistrationStake)
		_, exists := engine.ChainRegistry[104]
		assert.False(t, exists, "unfunded candidate must not be registered once a stake floor is configured")
	})

	t.Run("configured, pre-funded candidate succeeds", func(t *testing.T) {
		engine, _ := setupTestGatewayEngine()
		engine.EnsureGovernance()
		engine.MinRegistrationStake = big.NewInt(1000)

		// Pre-fund chain 104 (not yet in ChainRegistry) from Reserve (102, holding 5000) via the
		// same TransferAllocation primitive ProposalTransferAllocation uses -- proving no
		// ChainRegistry membership is required to pre-fund a genuinely new chain ID.
		require.NoError(t, engine.SupplyLedger.TransferAllocation(102, 104, big.NewInt(1000)))

		proposalID, err := engine.Governance.Propose(ProposalRegisterChain, newChainReg(104), 1000)
		require.NoError(t, err)
		_, err = engine.Governance.Vote(proposalID, 101, 1010)
		require.NoError(t, err)

		_, err = engine.ExecuteGovernanceProposal(proposalID, 1010+DefaultGovernanceTimelockSeconds+1)
		require.NoError(t, err)
		_, exists := engine.ChainRegistry[104]
		assert.True(t, exists, "candidate holding exactly MinRegistrationStake must be admitted")
	})
}

// TestGateway_RegisterChainViaStake is the regression test for the vote-free registration path.
// As of 2026-08-28, GatewayEngine.RegisterChainViaStake itself performs NO stake CHECK at all --
// it has no AccountStateDB access, so it cannot verify a real wallet balance. The real gate (a
// REAL native-coin deposit from the caller's own wallet, checked+burned against
// GatewayEngine.MinNativeStakeToRegister) lives one layer up, in gateway_handler.go's
// "registerChainViaStake" case -- see TestGatewayHandler_RegisterChainViaStake_RequiresRealNative
// StakeDeposit (gateway_handler_test.go) for that coverage. This file only covers what
// RegisterChainViaStake itself is still responsible for: duplicate-registration and PoP/quorum
// validation, vote-free.
//
// 2026-09-04: RegisterChainViaStake now DOES touch PerChainAllocation, as an outcome rather than
// a check -- it credits MinNativeStakeToRegister into the new chain's allocation on the Reserve's
// own ledger (see the function's own doc comment). None of the sub-tests below set
// MinNativeStakeToRegister on their engine, so that new branch stays a no-op here by construction
// -- see TestGateway_RegisterChainViaStake_CreditsStakeIntoAllocation for that coverage instead.
func TestGateway_RegisterChainViaStake(t *testing.T) {
	newChainReg := func(chainID uint64) []byte {
		reg := ChainRegistry{ChainID: chainID, Epoch: 1, QuorumThreshold: 6667}
		payload, err := json.Marshal(reg)
		require.NoError(t, err)
		return payload
	}

	t.Run("succeeds with ZERO votes cast and no stake precondition at this layer", func(t *testing.T) {
		engine, _ := setupTestGatewayEngine()
		engine.EnsureGovernance()

		// No Governance.Propose/Vote/ExecuteGovernanceProposal call anywhere in this sub-test --
		// that absence IS the point being tested. No SupplyLedger pre-funding either -- this
		// function no longer looks at PerChainAllocation at all.
		err := engine.RegisterChainViaStake(newChainReg(104), nil)
		require.NoError(t, err)
		reg, exists := engine.ChainRegistry[104]
		assert.True(t, exists, "RegisterChainViaStake must admit a valid candidate with no vote")
		assert.Equal(t, uint64(104), reg.ChainID)
		assert.Contains(t, engine.Governance.ActiveChains, uint64(104), "must also become a voting member for FUTURE proposals")
	})

	t.Run("already-registered chain ID is rejected", func(t *testing.T) {
		engine, _ := setupTestGatewayEngine()
		engine.EnsureGovernance()

		// Chain 101 is already in ChainRegistry via setupTestGatewayEngine's fixture.
		err := engine.RegisterChainViaStake(newChainReg(101), nil)
		assert.ErrorIs(t, err, ErrChainAlreadyRegistered)
	})

	t.Run("rejects an unverified non-empty committee (same PoP bar as everywhere else)", func(t *testing.T) {
		engine, _ := setupTestGatewayEngine()
		engine.EnsureGovernance()

		kpRogue := bls.GenerateKeyPair()
		forgedCommittee := []ValidatorEntry{
			{PubkeyBLS: kpRogue.BytesPublicKey(), Stake: 10000, PopSignature: make([]byte, 96)},
		}
		reg := ChainRegistry{ChainID: 104, Epoch: 1, QuorumThreshold: 6667, Committee: forgedCommittee}
		payload, err := json.Marshal(reg)
		require.NoError(t, err)

		err = engine.RegisterChainViaStake(payload, nil)
		assert.ErrorIs(t, err, ErrPopVerifyFailed)
		_, exists := engine.ChainRegistry[104]
		assert.False(t, exists)
	})
}

// TestGateway_RegisterChainViaStake_CreditsStakeIntoAllocation is the regression test for the
// 2026-09-04 unification of "stake to register" and "circulating cross-chain allocation" (see
// RegisterChainViaStake's own doc comment, and note/eurozone_unified_native_coin_plan.md mục 2.4)
// -- a freshly-registered chain must walk away with real cross-chain-outbound capacity equal to
// its own stake deposit, with zero separate ProposalTransferAllocation/ClaimMessage step needed.
//
// Same-day security fix covered here (user explicitly asked to re-check "không được in từ hư
// không" -- must never print from nothing): the credit MUST be a TRANSFER out of Reserve's own
// existing PerChainAllocation pool, never a GrantAllocation-style mint -- see the sub-test that
// asserts GenesisTotalSupply stays byte-for-byte constant, and the one proving an exhausted
// Reserve pool fails the whole registration closed rather than minting the gap.
func TestGateway_RegisterChainViaStake_CreditsStakeIntoAllocation(t *testing.T) {
	// genesisWallet stands in for "the caller that actually paid the stake" (forced by
	// gateway_handler.go in production, see RegisterChainViaStake's own doc comment) -- required
	// non-zero here whenever a sub-test configures MinNativeStakeToRegister>0, per this same
	// function's own genesis_wallet validation.
	genesisWallet := common.HexToAddress("0xfeedfeedfeedfeedfeedfeedfeedfeedfeedfeed")
	newChainReg := func(chainID uint64) []byte {
		reg := ChainRegistry{ChainID: chainID, Epoch: 1, QuorumThreshold: 6667, GenesisWallet: genesisWallet}
		payload, err := json.Marshal(reg)
		require.NoError(t, err)
		return payload
	}

	t.Run("Reserve's own copy: TRANSFERS from Reserve's own pool -- GenesisTotalSupply never changes", func(t *testing.T) {
		engine, _ := setupTestGatewayEngine() // LocalChainID == ReserveChainID == 102, holding 5000
		engine.EnsureGovernance()
		engine.MinNativeStakeToRegister = big.NewInt(777)

		supplyBefore := new(big.Int).Set(engine.SupplyLedger.GenesisTotalSupply)
		reserveBefore := engine.SupplyLedger.GetAllocation(102)

		require.NoError(t, engine.RegisterChainViaStake(newChainReg(104), engine.MinNativeStakeToRegister))

		assert.Equal(t, big.NewInt(777), engine.SupplyLedger.GetAllocation(104), "new chain must walk away with allocation == its real stake deposit")
		assert.Equal(t, new(big.Int).Sub(reserveBefore, big.NewInt(777)), engine.SupplyLedger.GetAllocation(102), "the credited amount must come OUT of Reserve's own pool, not out of thin air")
		assert.Equal(t, supplyBefore, engine.SupplyLedger.GenesisTotalSupply, "GenesisTotalSupply must NEVER change here -- this is a transfer, not a mint")
		assert.True(t, engine.SupplyLedger.VerifyInvariant())
	})

	t.Run("Reserve's pool exhausted: registration still succeeds (bootstrap-safe), just unfunded -- never mints the shortfall", func(t *testing.T) {
		// Regression test for a real deploy-pipeline break found 2026-09-04 (run_full_pipeline.sh):
		// at genesis of a brand-new system Reserve's pool starts at 0, and minting it needs quorum
		// from already-active chains -- for the very first chains, that means registering them
		// FIRST. An earlier version of this fix made insufficient allocation block the WHOLE
		// registration, which made that impossible (circular: register needs mint, mint needs
		// registered voters). Fixed: registration always succeeds; funding is best-effort.
		engine, _ := setupTestGatewayEngine() // Reserve (102) holds only 5000
		engine.EnsureGovernance()
		engine.MinNativeStakeToRegister = big.NewInt(999999) // far more than Reserve actually holds

		supplyBefore := new(big.Int).Set(engine.SupplyLedger.GenesisTotalSupply)
		reserveBefore := engine.SupplyLedger.GetAllocation(102)

		require.NoError(t, engine.RegisterChainViaStake(newChainReg(104), engine.MinNativeStakeToRegister))
		_, exists := engine.ChainRegistry[104]
		assert.True(t, exists, "registration must succeed even when Reserve's pool can't cover the stake yet -- this is the normal bootstrap case, not an error")
		assert.Equal(t, big.NewInt(0), engine.SupplyLedger.GetAllocation(104), "unfunded for now -- must recover later via ProposalTransferAllocation (e.g. fundGenesis), not via a silent mint")
		assert.Equal(t, reserveBefore, engine.SupplyLedger.GetAllocation(102), "Reserve's own pool must be completely untouched by a skipped credit")
		assert.Equal(t, supplyBefore, engine.SupplyLedger.GenesisTotalSupply, "GenesisTotalSupply must never change here")
	})

	t.Run("GenesisWallet unset: registration still succeeds, just unfunded (same best-effort treatment)", func(t *testing.T) {
		engine, _ := setupTestGatewayEngine() // Reserve (102) holds 5000, plenty
		engine.EnsureGovernance()
		engine.MinNativeStakeToRegister = big.NewInt(777)

		reg := ChainRegistry{ChainID: 104, Epoch: 1, QuorumThreshold: 6667} // GenesisWallet left zero
		payload, err := json.Marshal(reg)
		require.NoError(t, err)

		require.NoError(t, engine.RegisterChainViaStake(payload, engine.MinNativeStakeToRegister))
		_, exists := engine.ChainRegistry[104]
		assert.True(t, exists, "registration must still succeed")
		assert.Equal(t, big.NewInt(0), engine.SupplyLedger.GetAllocation(104), "must not credit a chain with no wallet to credit")
	})

	t.Run("non-Reserve chain's local copy: SupplyLedger untouched (no enforcement power there anyway)", func(t *testing.T) {
		engine, _ := setupTestGatewayEngine()
		engine.EnsureGovernance()
		engine.ReserveChainID = 999 // now LocalChainID(102) != ReserveChainID
		engine.MinNativeStakeToRegister = big.NewInt(777)

		supplyBefore := new(big.Int).Set(engine.SupplyLedger.GenesisTotalSupply)

		require.NoError(t, engine.RegisterChainViaStake(newChainReg(104), engine.MinNativeStakeToRegister))

		assert.Equal(t, big.NewInt(0), engine.SupplyLedger.GetAllocation(104), "non-Reserve chain's own local ledger has no enforcement power -- must not be credited")
		assert.Equal(t, supplyBefore, engine.SupplyLedger.GenesisTotalSupply)
	})

	t.Run("MinNativeStakeToRegister unset: no crediting, registration still succeeds (vote-free path unaffected)", func(t *testing.T) {
		engine, _ := setupTestGatewayEngine()
		engine.EnsureGovernance()
		// MinNativeStakeToRegister left nil -- matches every pre-2026-09-04 config.

		require.NoError(t, engine.RegisterChainViaStake(newChainReg(104), nil))
		assert.Equal(t, big.NewInt(0), engine.SupplyLedger.GetAllocation(104))
	})
}

// TestGateway_SetGenesisDigest is the regression test for the 2026-09-04 deterministic-genesis
// design's second phase -- publishing the canonical genesis.json digest AFTER a chain has already
// been registered via RegisterChainViaStake. See SetGenesisDigest's own doc comment for why this
// is a separate call (the digest can't be known at registration time) and why it's restricted to
// exactly the chain's own GenesisWallet (closes a front-running race a permissionless "first
// digest wins" design would otherwise have).
func TestGateway_SetGenesisDigest(t *testing.T) {
	genesisWallet := common.HexToAddress("0xfeedfeedfeedfeedfeedfeedfeedfeedfeedfeed")
	attacker := common.HexToAddress("0xbadbadbadbadbadbadbadbadbadbadbadbadbad")
	someDigest := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	otherDigest := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	newRegisteredEngine := func(t *testing.T) *GatewayEngine {
		engine, _ := setupTestGatewayEngine()
		engine.EnsureGovernance()
		reg := ChainRegistry{ChainID: 104, Epoch: 1, QuorumThreshold: 6667, GenesisWallet: genesisWallet}
		payload, err := json.Marshal(reg)
		require.NoError(t, err)
		require.NoError(t, engine.RegisterChainViaStake(payload, nil))
		return engine
	}

	t.Run("genesis wallet publishes successfully", func(t *testing.T) {
		engine := newRegisteredEngine(t)
		require.NoError(t, engine.SetGenesisDigest(104, someDigest, genesisWallet))
		assert.Equal(t, someDigest, engine.ChainRegistry[104].GenesisDigest)
	})

	t.Run("wrong caller (not genesis wallet) is rejected", func(t *testing.T) {
		engine := newRegisteredEngine(t)
		err := engine.SetGenesisDigest(104, someDigest, attacker)
		assert.ErrorIs(t, err, ErrNotGenesisWallet)
		assert.Equal(t, common.Hash{}, engine.ChainRegistry[104].GenesisDigest, "a rejected caller must never move the digest, even to the same value a later honest call would use")
	})

	t.Run("settable exactly once -- second attempt, even by the same wallet, is rejected", func(t *testing.T) {
		engine := newRegisteredEngine(t)
		require.NoError(t, engine.SetGenesisDigest(104, someDigest, genesisWallet))
		err := engine.SetGenesisDigest(104, otherDigest, genesisWallet)
		assert.ErrorIs(t, err, ErrGenesisDigestAlreadySet)
		assert.Equal(t, someDigest, engine.ChainRegistry[104].GenesisDigest, "must keep the FIRST published digest, never silently overwrite")
	})

	t.Run("unregistered chain is rejected", func(t *testing.T) {
		engine, _ := setupTestGatewayEngine()
		engine.EnsureGovernance()
		err := engine.SetGenesisDigest(999999, someDigest, genesisWallet)
		assert.ErrorIs(t, err, ErrUnknownSourceChain)
	})

	t.Run("zero digest is rejected", func(t *testing.T) {
		engine := newRegisteredEngine(t)
		err := engine.SetGenesisDigest(104, common.Hash{}, genesisWallet)
		assert.Error(t, err)
		assert.Equal(t, common.Hash{}, engine.ChainRegistry[104].GenesisDigest)
	})

	t.Run("a chain registered without a GenesisWallet (e.g. via governance ProposalRegisterChain) can never have its digest published by anyone", func(t *testing.T) {
		engine, _ := setupTestGatewayEngine()
		engine.EnsureGovernance()
		// Chain 101 comes from setupTestGatewayEngine's fixture, registered with no GenesisWallet.
		err := engine.SetGenesisDigest(101, someDigest, common.Address{})
		assert.ErrorIs(t, err, ErrNotGenesisWallet, "caller==zero-address must not accidentally match an unset GenesisWallet")
	})
}

// TestGateway_RegisterChainViaStake_RejectsSubBftQuorumThreshold covers the same gap at genesis
// time, now via RegisterChainViaStake -- BootstrapFoundingChains (and its batch-of->=
// MinFoundingChains shape) was retired 2026-08-28 in favor of RegisterChainViaStake being usable,
// per-chain, from chain #1 onward (see note/cross_chain_stake_and_value_flow.md).
func TestGateway_RegisterChainViaStake_RejectsSubBftQuorumThreshold(t *testing.T) {
	kp := bls.GenerateKeyPair()
	popSig := PopSign(kp.PrivateKey(), kp.PublicKey())

	makeEntry := func(chainID uint64, threshold uint64) []byte {
		reg := ChainRegistry{
			ChainID:         chainID,
			Committee:       []ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000, PopSignature: popSig.Bytes()}},
			Epoch:           1,
			QuorumThreshold: threshold,
		}
		b, _ := json.Marshal(reg)
		return b
	}

	ledger, err := NewGlobalSupplyLedger(big.NewInt(0), map[uint64]*big.Int{})
	require.NoError(t, err)
	engine := NewGatewayEngine(9099, map[uint64]ChainRegistry{}, ledger)
	engine.EnsureGovernance()

	// RegisterChainViaStake registers ONE chain per call (not a batch like the retired
	// BootstrapFoundingChains), so each entry now succeeds or fails independently -- a bad
	// sub-BFT entry no longer poisons any other, already-valid, entry's registration.
	require.NoError(t, engine.RegisterChainViaStake(makeEntry(101, 6667), nil))
	require.NoError(t, engine.RegisterChainViaStake(makeEntry(102, 6667), nil))
	require.NoError(t, engine.RegisterChainViaStake(makeEntry(103, 6667), nil))

	err = engine.RegisterChainViaStake(makeEntry(104, 1000), nil) // 10% -- far under the 2/3 BFT floor
	assert.ErrorIs(t, err, ErrInvalidQuorumThreshold)
	_, exists := engine.ChainRegistry[104]
	assert.False(t, exists, "the sub-BFT entry itself must still be rejected")
	assert.Len(t, engine.ChainRegistry, 3, "the three valid entries registered before it must be unaffected")
}

// TestGateway_ProposalUpdateCommittee_RecoversChainStuckManyEpochsBehind is the decision test
// for all_remaining_fixes_plan.md's Mục 1 ("epoch catch-up: chain mất kết nối nhiều epoch
// không có đường bắt kịp"). ApplyCommitteeUpdate (epoch_sync.go) requires strict sequential
// epoch progression AND a valid quorum cert from the chain's CURRENT committee -- if a chain
// loses connectivity for many epochs and its old committee's signing keys are gone (validators
// rotated in the meantime), self-attested continuity is permanently impossible; no amount of
// clever cryptography can prove continuity from keys that no longer exist. This test proves
// ProposalUpdateCommittee is a real, working answer: recovery via Root Anchor governance
// quorum (>=2/3 of OTHER active chains vouching for the stuck chain's claimed new committee,
// e.g. based on real-world proof the stuck chain's operators published out of band) rather
// than cryptographic self-continuity -- the same trust model BootstrapFoundingChains and
// ProposalRegisterChain already use elsewhere in this codebase, not a new risk class.
func TestGateway_ProposalUpdateCommittee_RecoversChainStuckManyEpochsBehind(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	engine.EnsureGovernance()

	// Chain 101 (from setupTestGatewayEngine) is stuck at Epoch 5. Simulate the real-world
	// failure this mechanism exists for: its old committee's keys are gone -- nobody in this
	// test ever produces a QuorumCert signed by the Epoch-5 committee, proving the recovery
	// path genuinely does not need one.
	require.Equal(t, uint64(5), engine.ChainRegistry[101].Epoch)

	kpNew := bls.GenerateKeyPair()
	popSigNew := PopSign(kpNew.PrivateKey(), kpNew.PublicKey())
	recoveredCommittee := []ValidatorEntry{
		{PubkeyBLS: kpNew.BytesPublicKey(), Stake: 10000, PopSignature: popSigNew.Bytes()},
	}

	// A big, non-sequential jump (5 -> 500) -- ApplyCommitteeUpdate would reject this outright
	// (ErrNonSequentialEpoch expects exactly 6). ProposalUpdateCommittee has no such
	// restriction: real Root Anchor governance quorum is the safety property here, not epoch
	// sequencing.
	payloadObj := UpdateCommitteePayload{
		ChainID:      101,
		NewEpoch:     500,
		NewCommittee: recoveredCommittee,
	}
	payload, err := json.Marshal(payloadObj)
	require.NoError(t, err)

	proposalID, err := engine.Governance.Propose(ProposalUpdateCommittee, payload, 1000)
	require.NoError(t, err)
	// Active chains here is just {101} (setupTestGatewayEngine's registry), so quorum = 1 --
	// in a real multi-chain Root Anchor this would be >=2/3 of OTHER active chains vouching
	// for chain 101, since chain 101 itself is the one that's stuck/unreachable.
	_, err = engine.Governance.Vote(proposalID, 101, 1010)
	require.NoError(t, err)

	_, err = engine.ExecuteGovernanceProposal(proposalID, 1010+DefaultGovernanceTimelockSeconds+1)
	require.NoError(t, err)

	reg := engine.ChainRegistry[101]
	assert.Equal(t, uint64(500), reg.Epoch, "chain recovered to the current epoch despite the 495-epoch gap")
	assert.Equal(t, 1, len(reg.Committee))
	assert.Equal(t, kpNew.BytesPublicKey(), reg.Committee[0].PubkeyBLS)
}
