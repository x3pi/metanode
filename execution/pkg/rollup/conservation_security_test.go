package rollup

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

type nilFloatReader struct{}

func (nilFloatReader) GetFloat(cm.PublicKey) (*big.Int, uint64, error) {
	return nil, 0, nil
}

type errorFloatReader struct{ err error }

func (e errorFloatReader) GetFloat(cm.PublicKey) (*big.Int, uint64, error) {
	return nil, 0, e.err
}

// ---------------------------------------------------------------------------
// 1. Adversarial Inputs: Nil, Negative, and Zero Values
// ---------------------------------------------------------------------------

func TestConservationSecurity_AdversarialInputs_NilNegativeZero(t *testing.T) {
	// Case 1.1: Incomplete inputs to CheckConservation
	_, err := CheckConservation(ConservationInputs{})
	require.Error(t, err, "CheckConservation with empty inputs must fail")
	assert.Contains(t, err.Error(), "incomplete inputs")

	// Case 1.2: FloatReader returns nil *big.Int
	store := NewDBStore(&mockDB{data: make(map[common.Address]map[common.Hash][]byte)})
	inNilFloat := ConservationInputs{
		Store:       store,
		Client:      &idempotentClient{},
		Float:       nilFloatReader{},
		TotalSupply: func() (*big.Int, error) { return big.NewInt(1000), nil },
	}
	_, err = CheckConservation(inNilFloat)
	require.Error(t, err, "nil Float must return error, not panic")
	assert.Contains(t, err.Error(), "float balance is nil")

	// Case 1.3: TotalSupply returns nil *big.Int
	inNilSupply := ConservationInputs{
		Store:       store,
		Client:      &idempotentClient{},
		Float:       fixedFloat{big.NewInt(1000)},
		TotalSupply: func() (*big.Int, error) { return nil, nil },
	}
	_, err = CheckConservation(inNilSupply)
	require.Error(t, err, "nil TotalSupply must return error, not panic")
	assert.Contains(t, err.Error(), "total supply is nil")

	// Case 1.4: Non-terminal record with nil Value, nil GasFee
	recordNil := &MessageRecord{
		MessageID: crypto.Keccak256Hash([]byte("nil-record")),
		Role:      RoleSender,
		State:     StateLocalAppliedPendingSend,
		Value:     nil,
		GasFee:    nil,
	}
	require.NoError(t, store.Put(recordNil))

	inAdversarialRecords := ConservationInputs{
		Store:       store,
		Client:      &idempotentClient{},
		Float:       fixedFloat{big.NewInt(1000)},
		TotalSupply: func() (*big.Int, error) { return big.NewInt(1000), nil },
	}
	res, err := CheckConservation(inAdversarialRecords)
	require.NoError(t, err, "nil record fields must not panic")
	assert.True(t, res.Stable)
	assert.Equal(t, int64(0), res.Pending.Int64(), "nil value/fee should contribute 0 to pending")

	// Case 1.5: Non-terminal record with negative Value and negative GasFee
	recordNeg := &MessageRecord{
		MessageID: crypto.Keccak256Hash([]byte("neg-record")),
		Role:      RoleSender,
		State:     StateLocalAppliedPendingSend,
		Value:     big.NewInt(-500),
		GasFee:    big.NewInt(-100),
	}
	require.NoError(t, store.Put(recordNeg))

	resNeg, err := CheckConservation(inAdversarialRecords)
	require.NoError(t, err)
	assert.Equal(t, int64(0), resNeg.Pending.Int64(), "negative value/fee must NOT decrease pending")

	// Case 1.6: Inbound transfer with nil event and negative amount
	client := &idempotentClient{
		inbound: []*parentchain.TransferEvent{
			nil,
			{Amount: nil},
			{Amount: big.NewInt(-1000)},
			{Amount: big.NewInt(0)},
			{Amount: big.NewInt(250)},
		},
	}
	inAdversarialInbound := ConservationInputs{
		Store:       store,
		Client:      client,
		Float:       fixedFloat{big.NewInt(1250)},
		TotalSupply: func() (*big.Int, error) { return big.NewInt(1000), nil },
		RecvCursor:  func() uint64 { return 0 },
	}
	resInbound, err := CheckConservation(inAdversarialInbound)
	require.NoError(t, err)
	assert.True(t, resInbound.Stable)
	assert.Equal(t, int64(250), resInbound.Pending.Int64(), "only positive inbound amount must be added to pending")
	assert.True(t, resInbound.OK, "diff (250) matches positive pending (250)")
}

// ---------------------------------------------------------------------------
// 2. Deficit Underflow Attack: Cannot Mask Unbacked Currency
// ---------------------------------------------------------------------------

func TestConservationSecurity_DeficitUnderflowAttack_CannotMask(t *testing.T) {
	// Scenario: A malicious actor exploits a bug to mint 500 unbacked coins locally.
	// Cluster float = 1,000, but accounts sum = 1,500. Diff = -500 (DEFICIT).
	// The attacker attempts to bypass this by creating massive fake in-flight records (e.g. 10 billion).
	store := NewDBStore(&mockDB{data: make(map[common.Address]map[common.Hash][]byte)})
	hugePending := new(big.Int).Mul(big.NewInt(10_000_000_000), big.NewInt(1_000_000_000))
	recordHuge := &MessageRecord{
		MessageID: crypto.Keccak256Hash([]byte("huge-pending")),
		Role:      RoleSender,
		State:     StateLocalAppliedPendingSend,
		Value:     hugePending,
		GasFee:    big.NewInt(100),
	}
	require.NoError(t, store.Put(recordHuge))

	in := ConservationInputs{
		Store:       store,
		Client:      &idempotentClient{},
		Float:       fixedFloat{big.NewInt(1000)},
		TotalSupply: func() (*big.Int, error) { return big.NewInt(1500), nil },
	}

	res, err := CheckConservation(in)
	require.NoError(t, err)
	assert.True(t, res.Stable)
	assert.False(t, res.OK, "Underflow (Float < Accounts) must NEVER be OK, regardless of pending amount")
	assert.Contains(t, res.Reason, "BELOW", "Reason must state float is BELOW accounts")
	assert.True(t, res.Diff.Sign() < 0, "Diff must be negative")
}

// ---------------------------------------------------------------------------
// 3. Surplus Overflow Attack: Vanishing Backed Coins Detected
// ---------------------------------------------------------------------------

func TestConservationSecurity_SurplusOverflowAttack_StealingBackedCoins(t *testing.T) {
	// Scenario: Float = 2,000. Accounts = 1,000. Diff = +1,000.
	// If in-flight pending is 800, exactly 200 backed coins have vanished.
	store := NewDBStore(&mockDB{data: make(map[common.Address]map[common.Hash][]byte)})
	require.NoError(t, store.Put(senderRecord("flight", StateLocalAppliedPendingSend, 700, 100))) // pending = 800

	in := ConservationInputs{
		Store:       store,
		Client:      &idempotentClient{},
		Float:       fixedFloat{big.NewInt(2000)},
		TotalSupply: func() (*big.Int, error) { return big.NewInt(1000), nil },
	}

	res, err := CheckConservation(in)
	require.NoError(t, err)
	assert.True(t, res.Stable)
	assert.False(t, res.OK, "Surplus exceeding pending must be flagged as violation")
	assert.Contains(t, res.Reason, "ABOVE", "Reason must state float is ABOVE accounts + in-flight")

	// Boundary condition 1: Exactly equal (Diff == Pending == 800) -> Must be OK
	in.Float = fixedFloat{big.NewInt(1800)}
	resEq, err := CheckConservation(in)
	require.NoError(t, err)
	assert.True(t, resEq.OK, "Surplus exactly equal to pending must be OK")

	// Boundary condition 2: Exceeding by 1 wei (Diff = 801 > Pending = 800) -> Must violate
	in.Float = fixedFloat{big.NewInt(1801)}
	resOver1, err := CheckConservation(in)
	require.NoError(t, err)
	assert.False(t, resOver1.OK, "Surplus exceeding pending by even 1 wei must be flagged")
}

// ---------------------------------------------------------------------------
// 4. Double-Spend, Replay, and Cross-Role State Machine Attacks
// ---------------------------------------------------------------------------

func TestConservationSecurity_DoubleSpendAndReplayAttacks(t *testing.T) {
	sender := common.HexToAddress("0xaaaa")
	target := common.HexToAddress("0xbbbb")
	val := big.NewInt(500)
	fee := big.NewInt(100)

	// Attack 4.1: Replay EventTxSubmitted on already applied state
	eventTx := Event{
		Type:   EventTxSubmitted,
		Role:   RoleSender,
		Sender: sender,
		Target: target,
		Value:  val,
		GasFee: fee,
	}
	state1, actions1, err := Next(StateNone, RoleSender, eventTx)
	require.NoError(t, err)
	assert.Equal(t, StateLocalAppliedPendingSend, state1)
	assert.Len(t, actions1, 3, "initial submission must emit actions")

	// Replay must be idempotent no-op without emitting ActionDeductBalance!
	stateReplay, actionsReplay, err := Next(state1, RoleSender, eventTx)
	require.NoError(t, err)
	assert.Equal(t, StateLocalAppliedPendingSend, stateReplay)
	assert.Empty(t, actionsReplay, "replaying EventTxSubmitted must NOT emit any deduct action")

	// Attack 4.2: Replay EventCreditObserved with duplicate flag
	eventCreditDup := Event{
		Type:               EventCreditObserved,
		Role:               RoleReceiver,
		Sender:             sender,
		Target:             target,
		Value:              val,
		IsDuplicate:        true,
		IsDestinationValid: true,
	}
	stateDup, actionsDup, err := Next(StateNone, RoleReceiver, eventCreditDup)
	require.NoError(t, err)
	assert.Equal(t, StateSkippedDup, stateDup)
	assert.Empty(t, actionsDup, "duplicate credit observation must NOT credit balance")

	// Attack 4.3: Replay EventClaimedConfirmed after already credited
	eventClaimed := Event{
		Type:    EventClaimedConfirmed,
		Role:    RoleReceiver,
		Target:  target,
		Value:   val,
		Outcome: OutcomeCredited,
	}
	stateCredited, actionsCredited, err := Next(StateCredited, RoleReceiver, eventClaimed)
	require.NoError(t, err)
	assert.Equal(t, StateCredited, stateCredited)
	assert.Empty(t, actionsCredited, "replaying EventClaimedConfirmed must NOT emit ActionCreditLocal")

	// Attack 4.4: Replay EventReclaimWon after already refunded
	eventReclaim := Event{
		Type:   EventReclaimWon,
		Role:   RoleSender,
		Sender: sender,
		Value:  val,
	}
	stateRefunded, actionsRefunded, err := Next(StateConfirmedRefunded, RoleSender, eventReclaim)
	require.NoError(t, err)
	assert.Equal(t, StateConfirmedRefunded, stateRefunded)
	assert.Empty(t, actionsRefunded, "replaying EventReclaimWon must NOT emit ActionCreditLocal")

	// Attack 4.5: Cross-role injection: Receiver event on Sender role
	_, _, err = Next(StateLocalAppliedPendingSend, RoleSender, eventCreditDup)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidRole), "Cross-role event injection must be rejected with ErrInvalidRole")

	// Attack 4.6: Premature reclaim before timeout
	eventPremature := Event{
		Type:              EventReclaimEligible,
		Role:              RoleSender,
		Value:             val,
		ParentBlockTime:   100,
		ParentConfirmTime: 90,
		Timeout:           60, // 100 - 90 = 10 < 60
	}
	_, _, err = Next(StateSentConfirmed, RoleSender, eventPremature)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrReclaimTooEarly), "Premature reclaim must be rejected")
}

// ---------------------------------------------------------------------------
// 5. MaxUint256 and Extreme BigInt Amounts
// ---------------------------------------------------------------------------

func TestConservationSecurity_MaxUint256AndExtremeBigInt(t *testing.T) {
	maxUint256, _ := new(big.Int).SetString("115792089237316195423570985008687907853269984665640564039457584007913129639935", 10)
	fee := big.NewInt(100)
	sender := common.HexToAddress("0x1111")
	target := common.HexToAddress("0x2222")

	// Event submission with MaxUint256
	event := Event{
		Type:   EventTxSubmitted,
		Role:   RoleSender,
		Sender: sender,
		Target: target,
		Value:  maxUint256,
		GasFee: fee,
	}
	state, actions, err := Next(StateNone, RoleSender, event)
	require.NoError(t, err)
	assert.Equal(t, StateLocalAppliedPendingSend, state)

	// Ensure debit equals maxUint256 + 100 without 64-bit overflow wrap
	expectedDebit := new(big.Int).Add(maxUint256, fee)
	assert.Equal(t, expectedDebit, actions[0].Amount)

	// CrossNodeHandler rejecting insufficient balance for MaxUint256
	store := NewDBStore(&mockDB{data: make(map[common.Address]map[common.Hash][]byte)})
	stateDB := &mockAccountStateDB{balances: make(map[common.Address]*big.Int)}
	stateDB.balances[sender] = big.NewInt(1_000_000) // Much less than MaxUint256

	handler := NewCrossNodeHandler(cm.PublicKey{})
	_, err = handler.HandleTransfer(store, stateDB, cm.PublicKey{}, sender, target, maxUint256, common.Hash{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient balance")
}

// ---------------------------------------------------------------------------
// 6. ConservationGuard Fail-Closed Enforcement
// ---------------------------------------------------------------------------

func TestConservationSecurity_FailClosedGuardBehavior(t *testing.T) {
	g := NewConservationGuard(ConservationEnforce)

	// Security Check 1: Deny before first conclusive verification
	require.Error(t, g.Allow(), "Unverified guard must deny cross-chain (fail-closed)")
	assert.Contains(t, g.Allow().Error(), "has not been verified")

	// Security Check 2: Inconclusive measurements (unstable or RPC failure) must NOT unlock
	g.Observe(ConservationResult{Stable: false}, nil)
	require.Error(t, g.Allow(), "Unstable measurement must NOT unlock guard")

	g.Observe(ConservationResult{}, errors.New("parent chain RPC timeout"))
	require.Error(t, g.Allow(), "RPC error must NOT unlock guard")

	// Security Check 3: Conclusive OK unlocks
	okRes := ConservationResult{Stable: true, OK: true}
	g.Observe(okRes, nil)
	require.NoError(t, g.Allow(), "Conclusive OK must unlock guard")

	// Security Check 4: Grace period for trie lag (1 or 2 violations do not block yet)
	badRes := ConservationResult{Stable: true, OK: false, Reason: "deficit"}
	g.Observe(badRes, nil)
	require.NoError(t, g.Allow(), "1 violation allows grace period")

	g.Observe(badRes, nil)
	require.NoError(t, g.Allow(), "2 violations allows grace period")

	// Security Check 5: 3rd consecutive violation must HALT cross-chain
	g.Observe(badRes, nil)
	errHalted := g.Allow()
	require.Error(t, errHalted, "3 consecutive violations must halt cross-chain")
	assert.Contains(t, errHalted.Error(), "cross-chain halted")

	// Security Check 6: Recovery with conclusive OK lifts the halt
	g.Observe(okRes, nil)
	require.NoError(t, g.Allow(), "Conclusive OK lifts the halt")
}

// ---------------------------------------------------------------------------
// 7. Adversarial Destination & Auto-Compensating Refund Conservation
// ---------------------------------------------------------------------------

func TestConservationSecurity_AdversarialDest_AutoRefundConserved(t *testing.T) {
	parentChain := NewInMemoryParentChain()
	kp1, kp2 := bls.GenerateKeyPair(), bls.GenerateKeyPair()
	parentChain.nodeKeys[1], parentChain.nodeKeys[2] = kp1, kp2
	sender, badTarget := common.HexToAddress("0xaaa"), common.HexToAddress("0xbad_target")
	parentChain.accounts[sender], parentChain.accounts[badTarget] = kp1.PublicKey(), kp2.PublicKey()

	// Initial cluster balances: cluster 1 has 10,000 float & 10,000 sender balance; cluster 2 has 0
	setupClusterFloatBalance(t, parentChain, kp1, 1, big.NewInt(10000))
	pub2 := kp2.PublicKey()
	_ = parentChain.store.SetChainRegistry(crypto.Keccak256Hash(pub2[:]), parentchain.ChainRegistryEntry{
		FloatIdentityKey:     pub2,
		ClusterIDDescriptive: 2,
		Authorized:           true,
	})

	node1 := NewRollupNode(1, parentChain, kp1, kp2.PublicKey(), 2)
	node2 := NewRollupNode(2, parentChain, kp2, kp1.PublicKey(), 1)
	// Destination cluster explicitly rejects the adversarial recipient address
	node2.receiveWorker.Validator = func(addr common.Address) bool {
		return addr != badTarget
	}
	node1.StateDB.(*mockAccountStateDB).balances[sender] = big.NewInt(10000)

	sum := func(n *RollupNode) func() (*big.Int, error) {
		return func() (*big.Int, error) {
			db := n.StateDB.(*mockAccountStateDB)
			db.mu.Lock()
			defer db.mu.Unlock()
			s := new(big.Int)
			for _, b := range db.balances {
				s.Add(s, b)
			}
			return s, nil
		}
	}
	inputs := func(n *RollupNode, kp *bls.KeyPair, id uint64) ConservationInputs {
		return ConservationInputs{
			Store:       n.Store,
			Client:      &ParentChainClientAdapter{chain: parentChain, chainID: id},
			Float:       chainFloat{parentChain, kp.PublicKey()},
			BLS:         kp.PublicKey(),
			TotalSupply: sum(n),
			RecvCursor:  n.receiveWorker.getCursor,
		}
	}

	h := NewCrossNodeHandler(kp1.PublicKey())
	// Sender initiates transfer of 500 to the invalid destination
	_, err := h.HandleTransfer(node1.Store, node1.StateDB, kp2.PublicKey(), sender, badTarget, big.NewInt(500), crypto.Keccak256Hash(nil))
	require.NoError(t, err)

	// Step 1: Immediately after debit, in-flight invariant holds on Cluster 1
	res1, err := CheckConservation(inputs(node1, kp1, 1))
	require.NoError(t, err)
	assert.True(t, res1.OK, "Cluster 1 must satisfy conservation bound while transfer is in flight")
	assert.Equal(t, int64(600), res1.Diff.Int64(), "Diff must equal 500 value + 100 fee")

	// Step 2: Let workers process until compensating refund completes
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	refundSettled := func() bool {
		// 10000 - 600 debited + 500 refunded = 9900; 100 fee burned on parent chain
		return node1.StateDB.GetBalance(sender).Cmp(big.NewInt(9900)) == 0
	}
	for !refundSettled() {
		select {
		case <-ctx.Done():
			t.Fatal("Compensating refund did not settle within timeout")
		case <-time.After(50 * time.Millisecond):
		}
		node1.sendWorker.processPending()
		node2.receiveWorker.pollAndProcess()
		node1.receiveWorker.pollAndProcess()
	}

	// Settle bookkeeping ticks
	for i := 0; i < 10; i++ {
		node1.sendWorker.processPending()
		node2.receiveWorker.pollAndProcess()
		node1.receiveWorker.pollAndProcess()
		time.Sleep(20 * time.Millisecond)
	}

	// Step 3: Verify exact conservation invariant at rest on BOTH clusters
	finalRes1, err := CheckConservation(inputs(node1, kp1, 1))
	require.NoError(t, err)
	assert.True(t, finalRes1.Stable)
	assert.True(t, finalRes1.OK, "Cluster 1 must be OK at rest after refund")
	assert.Equal(t, 0, finalRes1.Diff.Sign(), "Cluster 1 Float must equal Accounts exactly (Diff == 0)")
	assert.Equal(t, int64(9900), finalRes1.Float.Int64(), "Cluster 1 Float must be 9900 (10000 - 100 fee burned)")

	finalRes2, err := CheckConservation(inputs(node2, kp2, 2))
	require.NoError(t, err)
	assert.True(t, finalRes2.Stable)
	assert.True(t, finalRes2.OK, "Cluster 2 must be OK at rest after refund")
	assert.Equal(t, 0, finalRes2.Diff.Sign(), "Cluster 2 Float must equal Accounts exactly (Diff == 0)")
	assert.Equal(t, int64(0), finalRes2.Float.Int64(), "Cluster 2 Float must be 0")
}
