package rollup

import (
	"errors"
	"math"
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

var (
	testSender = common.HexToAddress("0x1111111111111111111111111111111111111111")
	testTarget = common.HexToAddress("0x2222222222222222222222222222222222222222")
	testValue  = big.NewInt(1000)
	testGasFee = big.NewInt(21)
)

// 1. Table-driven test covering all valid forward transitions and their emitted actions
func TestValidTransitionsTable(t *testing.T) {
	t.Run("Sender Happy Path", func(t *testing.T) {
		s0 := StateNone
		s1, act1, err := Next(s0, Event{Type: EventTxSubmitted, Sender: testSender, Target: testTarget, Value: testValue})
		if err != nil || s1 != StateLocalAppliedPendingSend || len(act1) != 3 {
			t.Fatalf("TxSubmitted failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}

		s2, act2, err := Next(s1, Event{Type: EventParentConfirmed})
		if err != nil || s2 != StateSentConfirmed || len(act2) != 0 {
			t.Fatalf("ParentConfirmed failed: s=%s, act=%d, err=%v", s2, len(act2), err)
		}

		s3, act3, err := Next(s2, Event{Type: EventClaimedObserved, Outcome: OutcomeCredited})
		if err != nil || s3 != StateConfirmedSuccess || len(act3) != 0 {
			t.Fatalf("ClaimedObserved(Credited) failed: s=%s, act=%d, err=%v", s3, len(act3), err)
		}
	})

	t.Run("Sender Refund Path", func(t *testing.T) {
		s0 := StateSentConfirmed
		s1, act1, err := Next(s0, Event{Type: EventClaimedObserved, Outcome: OutcomeRefund})
		if err != nil || s1 != StateRefundInTransit || len(act1) != 0 {
			t.Fatalf("ClaimedObserved(Refund) failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}

		s2, act2, err := Next(s1, Event{Type: EventRefundObserved, Sender: testSender, Value: testValue})
		if err != nil || s2 != StateConfirmedRefunded || len(act2) != 1 || act2[0].Type != ActionCreditLocal {
			t.Fatalf("RefundObserved failed: s=%s, act=%d, err=%v", s2, len(act2), err)
		}
	})

	t.Run("Sender Reclaim Won", func(t *testing.T) {
		s0 := StateSentConfirmed
		s1, act1, err := Next(s0, Event{
			Type:              EventReclaimEligible,
			ParentBlockTime:   1500,
			ParentConfirmTime: 1000,
			Timeout:           300,
			Value:             testValue,
		})
		if err != nil || s1 != StateReclaimSubmitted || len(act1) != 1 || act1[0].Type != ActionSendReclaim {
			t.Fatalf("ReclaimEligible failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}

		s2, act2, err := Next(s1, Event{Type: EventReclaimWon, Sender: testSender, Value: testValue})
		if err != nil || s2 != StateConfirmedRefunded || len(act2) != 1 || act2[0].Type != ActionCreditLocal {
			t.Fatalf("ReclaimWon failed: s=%s, act=%d, err=%v", s2, len(act2), err)
		}
	})

	t.Run("Sender Reclaim Lost", func(t *testing.T) {
		s0 := StateReclaimSubmitted
		s1, act1, err := Next(s0, Event{Type: EventReclaimLost})
		if err != nil || s1 != StateSentConfirmed || len(act1) != 0 {
			t.Fatalf("ReclaimLost failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}
	})

	t.Run("Receiver Happy Path", func(t *testing.T) {
		s0 := StateNone
		s1, act1, err := Next(s0, Event{
			Type:               EventCreditObserved,
			IsDestinationValid: true,
			Value:              testValue,
		})
		if err != nil || s1 != StateMarkedClaimedPendingCredit || len(act1) != 1 || act1[0].Type != ActionMarkClaimed {
			t.Fatalf("CreditObserved(Valid) failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}

		s2, act2, err := Next(s1, Event{Type: EventClaimedConfirmed, Target: testTarget, Value: testValue})
		if err != nil || s2 != StateCredited || len(act2) != 1 || act2[0].Type != ActionCreditLocal {
			t.Fatalf("ClaimedConfirmed failed: s=%s, act=%d, err=%v", s2, len(act2), err)
		}
	})

	t.Run("Receiver Refund Path", func(t *testing.T) {
		s0 := StateNone
		s1, act1, err := Next(s0, Event{
			Type:               EventCreditObserved,
			IsDestinationValid: false,
			Value:              testValue,
		})
		if err != nil || s1 != StateMarkedClaimedPendingRefund || len(act1) != 1 || act1[0].Outcome != OutcomeRefund {
			t.Fatalf("CreditObserved(Invalid) failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}

		s2, act2, err := Next(s1, Event{Type: EventClaimedConfirmed, Sender: testSender, Value: testValue})
		if err != nil || s2 != StateRefundSent || len(act2) != 1 || act2[0].Type != ActionSendRefund {
			t.Fatalf("ClaimedConfirmed(Refund) failed: s=%s, act=%d, err=%v", s2, len(act2), err)
		}

		s3, act3, err := Next(s2, Event{Type: EventRefundConfirmed})
		if err != nil || s3 != StateRefunded || len(act3) != 0 {
			t.Fatalf("RefundConfirmed failed: s=%s, act=%d, err=%v", s3, len(act3), err)
		}
	})

	t.Run("Receiver Duplicate", func(t *testing.T) {
		s0 := StateNone
		s1, act1, err := Next(s0, Event{Type: EventCreditObserved, IsDuplicate: true})
		if err != nil || s1 != StateSkippedDup || len(act1) != 0 {
			t.Fatalf("CreditObserved(Dup) failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}
	})
}

// 2. Cartesian product test: generates all (State, EventType) pairs and enforces rejection of all illegal pairs
func TestCartesianProductRejection(t *testing.T) {
	allStates := []State{
		StateNone,
		StateLocalAppliedPendingSend,
		StateSentConfirmed,
		StateReclaimSubmitted,
		StateRefundInTransit,
		StateConfirmedRefunded,
		StateConfirmedSuccess,
		StateMarkedClaimedPendingCredit,
		StateMarkedClaimedPendingRefund,
		StateRefundSent,
		StateSkippedDup,
		StateRefunded,
		StateCredited,
	}

	allEventTypes := []EventType{
		EventTxSubmitted,
		EventParentConfirmed,
		EventSendFailedTransient,
		EventClaimedObserved,
		EventRefundObserved,
		EventReclaimEligible,
		EventReclaimWon,
		EventReclaimLost,
		EventCreditObserved,
		EventClaimedConfirmed,
		EventRefundConfirmed,
	}

	// Set of legally allowed (state, eventType) pairs (including forward transitions and idempotent replays)
	allowedPairs := map[[2]uint32]bool{
		{uint32(StateNone), 1}:                        true, // TxSubmitted
		{uint32(StateNone), 9}:                        true, // CreditObserved
		{uint32(StateLocalAppliedPendingSend), 1}:     true, // TxSubmitted (idempotent)
		{uint32(StateLocalAppliedPendingSend), 2}:     true, // ParentConfirmed
		{uint32(StateLocalAppliedPendingSend), 3}:     true, // SendFailedTransient
		{uint32(StateSentConfirmed), 2}:               true, // ParentConfirmed (idempotent)
		{uint32(StateSentConfirmed), 4}:               true, // ClaimedObserved
		{uint32(StateSentConfirmed), 6}:               true, // ReclaimEligible
		{uint32(StateSentConfirmed), 8}:               true, // ReclaimLost (idempotent)
		{uint32(StateReclaimSubmitted), 6}:            true, // ReclaimEligible (idempotent)
		{uint32(StateReclaimSubmitted), 7}:            true, // ReclaimWon
		{uint32(StateReclaimSubmitted), 8}:            true, // ReclaimLost
		{uint32(StateRefundInTransit), 4}:             true, // ClaimedObserved (idempotent)
		{uint32(StateRefundInTransit), 5}:             true, // RefundObserved
		{uint32(StateConfirmedRefunded), 5}:           true, // RefundObserved (idempotent)
		{uint32(StateConfirmedRefunded), 7}:           true, // ReclaimWon (idempotent)
		{uint32(StateConfirmedSuccess), 4}:            true, // ClaimedObserved (idempotent)
		{uint32(StateMarkedClaimedPendingCredit), 9}:  true, // CreditObserved (idempotent)
		{uint32(StateMarkedClaimedPendingCredit), 10}: true, // ClaimedConfirmed
		{uint32(StateMarkedClaimedPendingRefund), 9}:  true, // CreditObserved (idempotent)
		{uint32(StateMarkedClaimedPendingRefund), 10}: true, // ClaimedConfirmed
		{uint32(StateRefundSent), 10}:                 true, // ClaimedConfirmed (idempotent)
		{uint32(StateRefundSent), 11}:                 true, // RefundConfirmed
		{uint32(StateSkippedDup), 9}:                  true, // CreditObserved (idempotent)
		{uint32(StateRefunded), 11}:                   true, // RefundConfirmed (idempotent)
		{uint32(StateCredited), 10}:                   true, // ClaimedConfirmed (idempotent)
	}

	eventIndex := map[EventType]uint32{
		EventTxSubmitted:         1,
		EventParentConfirmed:     2,
		EventSendFailedTransient: 3,
		EventClaimedObserved:     4,
		EventRefundObserved:      5,
		EventReclaimEligible:     6,
		EventReclaimWon:          7,
		EventReclaimLost:         8,
		EventCreditObserved:      9,
		EventClaimedConfirmed:    10,
		EventRefundConfirmed:     11,
	}

	totalChecked := 0
	rejectedCount := 0

	for _, s := range allStates {
		for _, eType := range allEventTypes {
			totalChecked++
			key := [2]uint32{uint32(s), eventIndex[eType]}
			isAllowed := allowedPairs[key]

			ev := Event{
				Type:               eType,
				Sender:             testSender,
				Target:             testTarget,
				Value:              testValue,
				ParentConfirmTime:  100,
				ParentBlockTime:    200,
				Timeout:            50,
				Outcome:            OutcomeCredited,
				IsDestinationValid: true,
			}

			_, _, err := Next(s, ev)
			if !isAllowed {
				if err == nil {
					t.Errorf("Cartesian check failed: pair (State=%s, Event=%s) should be rejected, but got nil error", s, eType)
				} else {
					rejectedCount++
				}
			}
		}
	}

	if totalChecked != 143 {
		t.Fatalf("expected 143 Cartesian pairs, got %d", totalChecked)
	}
	t.Logf("Cartesian validation: checked %d pairs, cleanly rejected %d illegal pairs", totalChecked, rejectedCount)
}

// 3. Replay idempotency: ensures replay never emits actions
func TestReplayNeverEmitsActions(t *testing.T) {
	replays := []struct {
		state State
		event Event
		name  string
	}{
		{StateLocalAppliedPendingSend, Event{Type: EventTxSubmitted, Sender: testSender, Value: testValue}, "TxSubmitted replay"},
		{StateSentConfirmed, Event{Type: EventParentConfirmed}, "ParentConfirmed replay"},
		{StateConfirmedSuccess, Event{Type: EventClaimedObserved, Outcome: OutcomeCredited}, "ClaimedObserved(Credited) replay"},
		{StateRefundInTransit, Event{Type: EventClaimedObserved, Outcome: OutcomeRefund}, "ClaimedObserved(Refund) replay"},
		{StateConfirmedRefunded, Event{Type: EventRefundObserved, Value: testValue}, "RefundObserved replay"},
		{StateConfirmedRefunded, Event{Type: EventReclaimWon, Value: testValue}, "ReclaimWon replay"},
		{StateReclaimSubmitted, Event{Type: EventReclaimEligible, ParentBlockTime: 200, ParentConfirmTime: 100, Timeout: 50, Value: testValue}, "ReclaimEligible replay"},
		{StateSentConfirmed, Event{Type: EventReclaimLost}, "ReclaimLost replay"},
		{StateSkippedDup, Event{Type: EventCreditObserved, IsDuplicate: true}, "CreditObserved(Dup) replay"},
		{StateMarkedClaimedPendingCredit, Event{Type: EventCreditObserved, IsDestinationValid: true, Value: testValue}, "CreditObserved(Valid) replay"},
		{StateMarkedClaimedPendingRefund, Event{Type: EventCreditObserved, IsDestinationValid: false, Value: testValue}, "CreditObserved(Invalid) replay"},
		{StateCredited, Event{Type: EventClaimedConfirmed, Value: testValue}, "ClaimedConfirmed(Credited) replay"},
		{StateRefundSent, Event{Type: EventClaimedConfirmed, Value: testValue}, "ClaimedConfirmed(Refund) replay"},
		{StateRefunded, Event{Type: EventRefundConfirmed}, "RefundConfirmed replay"},
	}

	for _, tc := range replays {
		t.Run(tc.name, func(t *testing.T) {
			sNew, actions, err := Next(tc.state, tc.event)
			if err != nil {
				t.Fatalf("unexpected error on replay: %v", err)
			}
			if sNew != tc.state {
				t.Fatalf("state changed on replay: expected %s, got %s", tc.state, sNew)
			}
			if len(actions) != 0 {
				t.Fatalf("actions emitted on replay: got %d actions, expected 0", len(actions))
			}
		})
	}
}

// 4. Mutation test: mutating Event.Value after Next must NOT affect Action.Amount
func TestBigIntAliasingMutation(t *testing.T) {
	origVal := big.NewInt(12345)
	ev := Event{
		Type:   EventTxSubmitted,
		Sender: testSender,
		Target: testTarget,
		Value:  origVal,
	}

	s, actions, err := Next(StateNone, ev)
	if err != nil || s != StateLocalAppliedPendingSend {
		t.Fatalf("Next failed: %v", err)
	}

	// Mutate the original big.Int
	origVal.SetInt64(9999999)

	// Action amount must remain 12345
	for i, act := range actions {
		if act.Amount != nil && act.Amount.Int64() != 12345 {
			t.Fatalf("Action[%d].Amount was mutated via aliasing! Expected 12345, got %d", i, act.Amount.Int64())
		}
	}

	// Test receiver side aliasing
	recvVal := big.NewInt(777)
	evRecv := Event{
		Type:               EventCreditObserved,
		IsDestinationValid: true,
		Value:              recvVal,
	}
	_, recvActs, err := Next(StateNone, evRecv)
	if err != nil {
		t.Fatalf("Next failed: %v", err)
	}
	recvVal.SetInt64(111)
	if recvActs[0].Amount.Int64() != 777 {
		t.Fatalf("ActionMarkClaimed Amount was mutated! Expected 777, got %d", recvActs[0].Amount.Int64())
	}
}

// 5. Overflow and boundary test for uint64 Reclaim timing
func TestReclaimUint64OverflowSafety(t *testing.T) {
	s := StateSentConfirmed

	// Case A: ParentConfirmTime + Timeout would overflow uint64 if added directly
	// e.g., ConfirmTime = MaxUint64 - 50, Timeout = 100
	// Direct add wraps around to 49!
	// ParentBlockTime = 1000.
	// If added directly: 1000 >= 49 -> true (BUG!).
	// Safe subtract: ParentBlockTime < ConfirmTime -> false (CORRECT!).
	evOverflow := Event{
		Type:              EventReclaimEligible,
		ParentBlockTime:   1000,
		ParentConfirmTime: math.MaxUint64 - 50,
		Timeout:           100,
		Value:             testValue,
	}
	_, _, err := Next(s, evOverflow)
	if !errors.Is(err, ErrReclaimTooEarly) {
		t.Fatalf("expected ErrReclaimTooEarly on overflow case, got %v", err)
	}

	// Case B: Boundary exact equality: ParentBlockTime - ParentConfirmTime == Timeout
	evExact := Event{
		Type:              EventReclaimEligible,
		ParentBlockTime:   1300,
		ParentConfirmTime: 1000,
		Timeout:           300,
		Value:             testValue,
	}
	sNew, acts, err := Next(s, evExact)
	if err != nil || sNew != StateReclaimSubmitted || len(acts) != 1 {
		t.Fatalf("exact timeout boundary failed: s=%s, err=%v", sNew, err)
	}

	// Case C: 1 millisecond too early
	evEarly := Event{
		Type:              EventReclaimEligible,
		ParentBlockTime:   1299,
		ParentConfirmTime: 1000,
		Timeout:           300,
		Value:             testValue,
	}
	_, _, errEarly := Next(s, evEarly)
	if !errors.Is(errEarly, ErrReclaimTooEarly) {
		t.Fatalf("expected ErrReclaimTooEarly for 1ms early, got %v", errEarly)
	}
}

// 6. Value validation: nil, zero, and negative values must be rejected
func TestValueValidation(t *testing.T) {
	invalidValues := []*big.Int{
		nil,
		big.NewInt(0),
		big.NewInt(-1),
		big.NewInt(-1000),
	}

	for _, v := range invalidValues {
		// TxSubmitted
		_, _, err1 := Next(StateNone, Event{Type: EventTxSubmitted, Sender: testSender, Target: testTarget, Value: v})
		if !errors.Is(err1, ErrInvalidAmount) {
			t.Errorf("TxSubmitted: expected ErrInvalidAmount for value %v, got %v", v, err1)
		}

		// ReclaimEligible
		_, _, err2 := Next(StateSentConfirmed, Event{Type: EventReclaimEligible, ParentBlockTime: 200, ParentConfirmTime: 100, Timeout: 50, Value: v})
		if !errors.Is(err2, ErrInvalidAmount) {
			t.Errorf("ReclaimEligible: expected ErrInvalidAmount for value %v, got %v", v, err2)
		}

		// CreditObserved
		_, _, err3 := Next(StateNone, Event{Type: EventCreditObserved, IsDestinationValid: true, Value: v})
		if !errors.Is(err3, ErrInvalidAmount) {
			t.Errorf("CreditObserved: expected ErrInvalidAmount for value %v, got %v", v, err3)
		}

		// ClaimedConfirmed
		_, _, err4 := Next(StateMarkedClaimedPendingCredit, Event{Type: EventClaimedConfirmed, Target: testTarget, Value: v})
		if !errors.Is(err4, ErrInvalidAmount) {
			t.Errorf("ClaimedConfirmed: expected ErrInvalidAmount for value %v, got %v", v, err4)
		}
	}
}

// 7. Property / Fuzz invariant: No execution path can reach both CONFIRMED_SUCCESS and CONFIRMED_REFUNDED
func TestProperty_MutualExclusion(t *testing.T) {
	allEvents := []Event{
		{Type: EventTxSubmitted, Sender: testSender, Target: testTarget, Value: testValue},
		{Type: EventParentConfirmed},
		{Type: EventSendFailedTransient},
		{Type: EventClaimedObserved, Outcome: OutcomeCredited},
		{Type: EventClaimedObserved, Outcome: OutcomeRefund},
		{Type: EventRefundObserved, Sender: testSender, Value: testValue},
		{Type: EventReclaimEligible, ParentBlockTime: 2000, ParentConfirmTime: 1000, Timeout: 300, Value: testValue},
		{Type: EventReclaimWon, Sender: testSender, Value: testValue},
		{Type: EventReclaimLost},
	}

	rng := rand.New(rand.NewSource(42))

	for iter := 0; iter < 1000; iter++ {
		state := StateNone
		reachedSuccess := false
		reachedRefunded := false

		// Simulate random sequence of 20 transitions
		for step := 0; step < 20; step++ {
			ev := allEvents[rng.Intn(len(allEvents))]
			newState, _, err := Next(state, ev)
			if err == nil {
				state = newState
				if state == StateConfirmedSuccess {
					reachedSuccess = true
				}
				if state == StateConfirmedRefunded {
					reachedRefunded = true
				}
			}
		}

		// Critical invariant: mutually exclusive!
		if reachedSuccess && reachedRefunded {
			t.Fatalf("CRITICAL INVARIANT VIOLATION: sequence reached BOTH ConfirmedSuccess and ConfirmedRefunded at iter %d!", iter)
		}
	}
}
