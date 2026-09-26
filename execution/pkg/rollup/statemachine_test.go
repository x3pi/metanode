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

// TransitionFixture defines a single valid transition edge as the single source of truth
type TransitionFixture struct {
	FromState       State
	Role            Role
	EventType       EventType
	Event           Event
	ToState         State
	ActionCount     int
	ActionType      ActionType
	ExpectedActions []Action
	IsReplay        bool
}

// allValidTransitions is the unified truth table matching Section 1.7 of SEQUENCER_SCHEMAS_AND_TEST_PLAN.md
var allValidTransitions = []TransitionFixture{
	// ─── SENDER FORWARD EDGES ─────────────────────────────────────────────
	{
		FromState:   StateNone,
		Role:        RoleSender,
		EventType:   EventTxSubmitted,
		Event:       Event{Type: EventTxSubmitted, Sender: testSender, Target: testTarget, Value: testValue},
		ToState:     StateLocalAppliedPendingSend,
		ActionCount: 3,
		ActionType:  ActionDeductBalance,
		ExpectedActions: []Action{
			{Type: ActionDeductBalance, Target: testSender, Amount: testValue},
			{Type: ActionCreateRecord, Target: testTarget, Amount: testValue},
			{Type: ActionSendTransfer, Target: testTarget, Amount: testValue},
		},
	},
	{
		FromState:   StateLocalAppliedPendingSend,
		Role:        RoleSender,
		EventType:   EventParentConfirmed,
		Event:       Event{Type: EventParentConfirmed},
		ToState:     StateSentConfirmed,
		ActionCount: 0,
	},
	{
		FromState:   StateLocalAppliedPendingSend,
		Role:        RoleSender,
		EventType:   EventSendFailedTransient,
		Event:       Event{Type: EventSendFailedTransient},
		ToState:     StateLocalAppliedPendingSend,
		ActionCount: 0,
	},
	{
		FromState:   StateSentConfirmed,
		Role:        RoleSender,
		EventType:   EventClaimedObserved,
		Event:       Event{Type: EventClaimedObserved, Outcome: OutcomeCredited},
		ToState:     StateConfirmedSuccess,
		ActionCount: 0,
	},
	{
		FromState:   StateSentConfirmed,
		Role:        RoleSender,
		EventType:   EventClaimedObserved,
		Event:       Event{Type: EventClaimedObserved, Outcome: OutcomeRefund},
		ToState:     StateRefundInTransit,
		ActionCount: 0,
	},
	{
		FromState:   StateRefundInTransit,
		Role:        RoleSender,
		EventType:   EventRefundObserved,
		Event:       Event{Type: EventRefundObserved, Sender: testSender, Value: testValue},
		ToState:     StateConfirmedRefunded,
		ActionCount: 1,
		ActionType:  ActionCreditLocal,
		ExpectedActions: []Action{
			{Type: ActionCreditLocal, Target: testSender, Amount: testValue},
		},
	},
	{
		FromState: StateSentConfirmed,
		Role:      RoleSender,
		EventType: EventReclaimEligible,
		Event: Event{
			Type:              EventReclaimEligible,
			ParentBlockTime:   1500,
			ParentConfirmTime: 1000,
			Timeout:           300,
			Value:             testValue,
		},
		ToState:     StateReclaimSubmitted,
		ActionCount: 1,
		ActionType:  ActionSendReclaim,
		ExpectedActions: []Action{
			{Type: ActionSendReclaim, Amount: testValue},
		},
	},
	{
		FromState:   StateReclaimSubmitted,
		Role:        RoleSender,
		EventType:   EventReclaimWon,
		Event:       Event{Type: EventReclaimWon, Sender: testSender, Value: testValue},
		ToState:     StateConfirmedRefunded,
		ActionCount: 1,
		ActionType:  ActionCreditLocal,
		ExpectedActions: []Action{
			{Type: ActionCreditLocal, Target: testSender, Amount: testValue},
		},
	},
	{
		FromState:   StateReclaimSubmitted,
		Role:        RoleSender,
		EventType:   EventReclaimLost,
		Event:       Event{Type: EventReclaimLost},
		ToState:     StateSentConfirmed,
		ActionCount: 0,
	},

	// ─── SENDER IDEMPOTENT REPLAYS ────────────────────────────────────────
	{
		FromState:   StateLocalAppliedPendingSend,
		Role:        RoleSender,
		EventType:   EventTxSubmitted,
		Event:       Event{Type: EventTxSubmitted, Sender: testSender, Value: testValue},
		ToState:     StateLocalAppliedPendingSend,
		ActionCount: 0,
		IsReplay:    true,
	},
	{
		FromState:   StateSentConfirmed,
		Role:        RoleSender,
		EventType:   EventParentConfirmed,
		Event:       Event{Type: EventParentConfirmed},
		ToState:     StateSentConfirmed,
		ActionCount: 0,
		IsReplay:    true,
	},
	{
		FromState:   StateConfirmedSuccess,
		Role:        RoleSender,
		EventType:   EventClaimedObserved,
		Event:       Event{Type: EventClaimedObserved, Outcome: OutcomeCredited},
		ToState:     StateConfirmedSuccess,
		ActionCount: 0,
		IsReplay:    true,
	},
	{
		FromState:   StateRefundInTransit,
		Role:        RoleSender,
		EventType:   EventClaimedObserved,
		Event:       Event{Type: EventClaimedObserved, Outcome: OutcomeRefund},
		ToState:     StateRefundInTransit,
		ActionCount: 0,
		IsReplay:    true,
	},
	{
		FromState:   StateConfirmedRefunded,
		Role:        RoleSender,
		EventType:   EventRefundObserved,
		Event:       Event{Type: EventRefundObserved, Value: testValue},
		ToState:     StateConfirmedRefunded,
		ActionCount: 0,
		IsReplay:    true,
	},
	{
		FromState:   StateConfirmedRefunded,
		Role:        RoleSender,
		EventType:   EventReclaimWon,
		Event:       Event{Type: EventReclaimWon, Value: testValue},
		ToState:     StateConfirmedRefunded,
		ActionCount: 0,
		IsReplay:    true,
	},
	{
		FromState: StateReclaimSubmitted,
		Role:      RoleSender,
		EventType: EventReclaimEligible,
		Event: Event{
			Type:              EventReclaimEligible,
			ParentBlockTime:   200,
			ParentConfirmTime: 100,
			Timeout:           50,
			Value:             testValue,
		},
		ToState:     StateReclaimSubmitted,
		ActionCount: 0,
		IsReplay:    true,
	},
	{
		FromState:   StateSentConfirmed,
		Role:        RoleSender,
		EventType:   EventReclaimLost,
		Event:       Event{Type: EventReclaimLost},
		ToState:     StateSentConfirmed,
		ActionCount: 0,
		IsReplay:    true,
	},

	// ─── RECEIVER FORWARD EDGES ───────────────────────────────────────────
	{
		FromState: StateNone,
		Role:      RoleReceiver,
		EventType: EventCreditObserved,
		Event: Event{
			Type:               EventCreditObserved,
			IsDestinationValid: true,
			Value:              testValue,
		},
		ToState:     StateMarkedClaimedPendingCredit,
		ActionCount: 1,
		ActionType:  ActionMarkClaimed,
		ExpectedActions: []Action{
			{Type: ActionMarkClaimed, Outcome: OutcomeCredited, Amount: testValue},
		},
	},
	{
		FromState: StateNone,
		Role:      RoleReceiver,
		EventType: EventCreditObserved,
		Event: Event{
			Type:               EventCreditObserved,
			IsDestinationValid: false,
			Value:              testValue,
		},
		ToState:     StateMarkedClaimedPendingRefund,
		ActionCount: 1,
		ActionType:  ActionMarkClaimed,
		ExpectedActions: []Action{
			{Type: ActionMarkClaimed, Outcome: OutcomeRefund, Amount: testValue},
		},
	},
	{
		FromState:   StateNone,
		Role:        RoleReceiver,
		EventType:   EventCreditObserved,
		Event:       Event{Type: EventCreditObserved, IsDuplicate: true},
		ToState:     StateSkippedDup,
		ActionCount: 0,
	},
	{
		FromState:   StateMarkedClaimedPendingCredit,
		Role:        RoleReceiver,
		EventType:   EventClaimedConfirmed,
		Event:       Event{Type: EventClaimedConfirmed, Target: testTarget, Value: testValue},
		ToState:     StateCredited,
		ActionCount: 1,
		ActionType:  ActionCreditLocal,
		ExpectedActions: []Action{
			{Type: ActionCreditLocal, Target: testTarget, Amount: testValue},
		},
	},
	{
		FromState:   StateMarkedClaimedPendingRefund,
		Role:        RoleReceiver,
		EventType:   EventClaimedConfirmed,
		Event:       Event{Type: EventClaimedConfirmed, Sender: testSender, Value: testValue},
		ToState:     StateRefundSent,
		ActionCount: 1,
		ActionType:  ActionSendRefund,
		ExpectedActions: []Action{
			{Type: ActionSendRefund, Target: testSender, Amount: testValue},
		},
	},
	{
		FromState:   StateRefundSent,
		Role:        RoleReceiver,
		EventType:   EventRefundConfirmed,
		Event:       Event{Type: EventRefundConfirmed},
		ToState:     StateRefunded,
		ActionCount: 0,
	},

	// ─── RECEIVER IDEMPOTENT REPLAYS ──────────────────────────────────────
	{
		FromState:   StateSkippedDup,
		Role:        RoleReceiver,
		EventType:   EventCreditObserved,
		Event:       Event{Type: EventCreditObserved, IsDuplicate: true},
		ToState:     StateSkippedDup,
		ActionCount: 0,
		IsReplay:    true,
	},
	{
		FromState: StateMarkedClaimedPendingCredit,
		Role:      RoleReceiver,
		EventType: EventCreditObserved,
		Event: Event{
			Type:               EventCreditObserved,
			IsDestinationValid: true,
			Value:              testValue,
		},
		ToState:     StateMarkedClaimedPendingCredit,
		ActionCount: 0,
		IsReplay:    true,
	},
	{
		FromState: StateMarkedClaimedPendingRefund,
		Role:      RoleReceiver,
		EventType: EventCreditObserved,
		Event: Event{
			Type:               EventCreditObserved,
			IsDestinationValid: false,
			Value:              testValue,
		},
		ToState:     StateMarkedClaimedPendingRefund,
		ActionCount: 0,
		IsReplay:    true,
	},
	{
		FromState:   StateCredited,
		Role:        RoleReceiver,
		EventType:   EventClaimedConfirmed,
		Event:       Event{Type: EventClaimedConfirmed, Value: testValue},
		ToState:     StateCredited,
		ActionCount: 0,
		IsReplay:    true,
	},
	{
		FromState:   StateRefundSent,
		Role:        RoleReceiver,
		EventType:   EventClaimedConfirmed,
		Event:       Event{Type: EventClaimedConfirmed, Value: testValue},
		ToState:     StateRefundSent,
		ActionCount: 0,
		IsReplay:    true,
	},
	{
		FromState:   StateRefunded,
		Role:        RoleReceiver,
		EventType:   EventRefundConfirmed,
		Event:       Event{Type: EventRefundConfirmed},
		ToState:     StateRefunded,
		ActionCount: 0,
		IsReplay:    true,
	},
}

// 1. Table-driven test covering all valid forward transitions and their emitted actions
func TestValidTransitionsTable(t *testing.T) {
	t.Run("Sender Happy Path", func(t *testing.T) {
		s0 := StateNone
		s1, act1, err := Next(s0, RoleSender, Event{Type: EventTxSubmitted, Sender: testSender, Target: testTarget, Value: testValue})
		if err != nil || s1 != StateLocalAppliedPendingSend || len(act1) != 3 {
			t.Fatalf("TxSubmitted failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}
		if act1[0].Type != ActionDeductBalance || act1[0].Target != testSender || act1[0].Amount == nil || act1[0].Amount.Cmp(testValue) != 0 {
			t.Fatalf("TxSubmitted act[0] mismatch: expected DeductBalance target=%s val=%s, got %+v", testSender, testValue, act1[0])
		}
		if act1[1].Type != ActionCreateRecord || act1[1].Target != testTarget || act1[1].Amount == nil || act1[1].Amount.Cmp(testValue) != 0 {
			t.Fatalf("TxSubmitted act[1] mismatch: expected CreateRecord target=%s val=%s, got %+v", testTarget, testValue, act1[1])
		}
		if act1[2].Type != ActionSendTransfer || act1[2].Target != testTarget || act1[2].Amount == nil || act1[2].Amount.Cmp(testValue) != 0 {
			t.Fatalf("TxSubmitted act[2] mismatch: expected SendTransfer target=%s val=%s, got %+v", testTarget, testValue, act1[2])
		}

		s2, act2, err := Next(s1, RoleSender, Event{Type: EventParentConfirmed})
		if err != nil || s2 != StateSentConfirmed || len(act2) != 0 {
			t.Fatalf("ParentConfirmed failed: s=%s, act=%d, err=%v", s2, len(act2), err)
		}

		s3, act3, err := Next(s2, RoleSender, Event{Type: EventClaimedObserved, Outcome: OutcomeCredited})
		if err != nil || s3 != StateConfirmedSuccess || len(act3) != 0 {
			t.Fatalf("ClaimedObserved(Credited) failed: s=%s, act=%d, err=%v", s3, len(act3), err)
		}
	})

	t.Run("Sender Refund Path", func(t *testing.T) {
		s0 := StateSentConfirmed
		s1, act1, err := Next(s0, RoleSender, Event{Type: EventClaimedObserved, Outcome: OutcomeRefund})
		if err != nil || s1 != StateRefundInTransit || len(act1) != 0 {
			t.Fatalf("ClaimedObserved(Refund) failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}

		s2, act2, err := Next(s1, RoleSender, Event{Type: EventRefundObserved, Sender: testSender, Value: testValue})
		if err != nil || s2 != StateConfirmedRefunded || len(act2) != 1 || act2[0].Type != ActionCreditLocal {
			t.Fatalf("RefundObserved failed: s=%s, act=%d, err=%v", s2, len(act2), err)
		}
	})

	t.Run("Sender Reclaim Won", func(t *testing.T) {
		s0 := StateSentConfirmed
		s1, act1, err := Next(s0, RoleSender, Event{
			Type:              EventReclaimEligible,
			ParentBlockTime:   1500,
			ParentConfirmTime: 1000,
			Timeout:           300,
			Value:             testValue,
		})
		if err != nil || s1 != StateReclaimSubmitted || len(act1) != 1 || act1[0].Type != ActionSendReclaim {
			t.Fatalf("ReclaimEligible failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}

		s2, act2, err := Next(s1, RoleSender, Event{Type: EventReclaimWon, Sender: testSender, Value: testValue})
		if err != nil || s2 != StateConfirmedRefunded || len(act2) != 1 || act2[0].Type != ActionCreditLocal {
			t.Fatalf("ReclaimWon failed: s=%s, act=%d, err=%v", s2, len(act2), err)
		}
	})

	t.Run("Sender Reclaim Lost", func(t *testing.T) {
		s0 := StateReclaimSubmitted
		s1, act1, err := Next(s0, RoleSender, Event{Type: EventReclaimLost})
		if err != nil || s1 != StateSentConfirmed || len(act1) != 0 {
			t.Fatalf("ReclaimLost failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}
	})

	t.Run("Receiver Happy Path", func(t *testing.T) {
		s0 := StateNone
		s1, act1, err := Next(s0, RoleReceiver, Event{
			Type:               EventCreditObserved,
			IsDestinationValid: true,
			Value:              testValue,
		})
		if err != nil || s1 != StateMarkedClaimedPendingCredit || len(act1) != 1 || act1[0].Type != ActionMarkClaimed {
			t.Fatalf("CreditObserved(Valid) failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}

		s2, act2, err := Next(s1, RoleReceiver, Event{Type: EventClaimedConfirmed, Target: testTarget, Value: testValue})
		if err != nil || s2 != StateCredited || len(act2) != 1 || act2[0].Type != ActionCreditLocal {
			t.Fatalf("ClaimedConfirmed failed: s=%s, act=%d, err=%v", s2, len(act2), err)
		}
	})

	t.Run("Receiver Refund Path", func(t *testing.T) {
		s0 := StateNone
		s1, act1, err := Next(s0, RoleReceiver, Event{
			Type:               EventCreditObserved,
			IsDestinationValid: false,
			Value:              testValue,
		})
		if err != nil || s1 != StateMarkedClaimedPendingRefund || len(act1) != 1 || act1[0].Outcome != OutcomeRefund {
			t.Fatalf("CreditObserved(Invalid) failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}

		s2, act2, err := Next(s1, RoleReceiver, Event{Type: EventClaimedConfirmed, Sender: testSender, Value: testValue})
		if err != nil || s2 != StateRefundSent || len(act2) != 1 || act2[0].Type != ActionSendRefund {
			t.Fatalf("ClaimedConfirmed(Refund) failed: s=%s, act=%d, err=%v", s2, len(act2), err)
		}

		s3, act3, err := Next(s2, RoleReceiver, Event{Type: EventRefundConfirmed})
		if err != nil || s3 != StateRefunded || len(act3) != 0 {
			t.Fatalf("RefundConfirmed failed: s=%s, act=%d, err=%v", s3, len(act3), err)
		}
	})

	t.Run("Receiver Duplicate", func(t *testing.T) {
		s0 := StateNone
		s1, act1, err := Next(s0, RoleReceiver, Event{Type: EventCreditObserved, IsDuplicate: true})
		if err != nil || s1 != StateSkippedDup || len(act1) != 0 {
			t.Fatalf("CreditObserved(Dup) failed: s=%s, act=%d, err=%v", s1, len(act1), err)
		}
	})
}

// 2. Cartesian product test: evaluates all (State, Role, EventType) combinations against the truth table
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

	allRoles := []Role{
		RoleSender,
		RoleReceiver,
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

	// Map of valid fixtures keyed by (State, Role, EventType)
	validMap := make(map[[3]uint32][]TransitionFixture)
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

	for _, fixture := range allValidTransitions {
		key := [3]uint32{uint32(fixture.FromState), uint32(fixture.Role), eventIndex[fixture.EventType]}
		validMap[key] = append(validMap[key], fixture)
	}

	totalChecked := 0
	rejectedCount := 0
	passedCount := 0

	for _, s := range allStates {
		for _, r := range allRoles {
			for _, eType := range allEventTypes {
				totalChecked++
				key := [3]uint32{uint32(s), uint32(r), eventIndex[eType]}
				fixtures, isAllowed := validMap[key]

				if isAllowed {
					for _, fix := range fixtures {
						newState, actions, err := Next(fix.FromState, fix.Role, fix.Event)
						if err != nil {
							t.Errorf("Allowed transition failed: (State=%s, Role=%s, Event=%s): %v", s, r, eType, err)
						} else {
							passedCount++
							if newState != fix.ToState {
								t.Errorf("State mismatch for %s: expected %s, got %s", fix.EventType, fix.ToState, newState)
							}
							if len(actions) != fix.ActionCount {
								t.Errorf("Action count mismatch for %s: expected %d, got %d", fix.EventType, fix.ActionCount, len(actions))
							}
							if fix.ActionCount > 0 && fix.ActionType != "" && actions[0].Type != fix.ActionType {
								t.Errorf("Action type mismatch for %s: expected %s, got %s", fix.EventType, fix.ActionType, actions[0].Type)
							}
							if len(fix.ExpectedActions) > 0 {
								if len(actions) != len(fix.ExpectedActions) {
									t.Errorf("ExpectedActions count mismatch for %s: expected %d, got %d", fix.EventType, len(fix.ExpectedActions), len(actions))
								}
								for idx, exp := range fix.ExpectedActions {
									if idx >= len(actions) {
										break
									}
									got := actions[idx]
									if got.Type != exp.Type {
										t.Errorf("Action[%d] type mismatch for %s: expected %s, got %s", idx, fix.EventType, exp.Type, got.Type)
									}
									if exp.Target != (common.Address{}) && got.Target != exp.Target {
										t.Errorf("Action[%d] target mismatch for %s: expected %s, got %s", idx, fix.EventType, exp.Target, got.Target)
									}
									if exp.Outcome != OutcomeNone && got.Outcome != exp.Outcome {
										t.Errorf("Action[%d] outcome mismatch for %s: expected %s, got %s", idx, fix.EventType, exp.Outcome, got.Outcome)
									}
									if exp.Amount != nil && (got.Amount == nil || got.Amount.Cmp(exp.Amount) != 0) {
										t.Errorf("Action[%d] amount mismatch for %s: expected %v, got %v", idx, fix.EventType, exp.Amount, got.Amount)
									}
								}
							}
						}
					}
				} else {
					// Illegal combination: must be rejected cleanly
					genericEvent := Event{
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
					nextState, actions, err := Next(s, r, genericEvent)
					if err == nil {
						t.Errorf("Cartesian check failed: triple (State=%s, Role=%s, Event=%s) should be rejected, but got nil error", s, r, eType)
					} else {
						rejectedCount++
						// Invariant: errors must leave state unchanged and emit 0 actions
						if nextState != s {
							t.Errorf("State mutated on rejected transition from %s to %s", s, nextState)
						}
						if len(actions) != 0 {
							t.Errorf("Actions emitted on rejected transition: %d actions", len(actions))
						}
					}
				}
			}
		}
	}

	expectedTotal := len(allStates) * len(allRoles) * len(allEventTypes) // 13 * 2 * 11 = 286
	if totalChecked != expectedTotal {
		t.Fatalf("expected %d Cartesian triples, got %d", expectedTotal, totalChecked)
	}
	t.Logf("Cartesian validation: checked %d triples (%d valid edge variants passed, %d illegal combinations cleanly rejected)", totalChecked, passedCount, rejectedCount)
}

// 3. Replay idempotency: ensures replay never emits actions
func TestReplayNeverEmitsActions(t *testing.T) {
	for _, fix := range allValidTransitions {
		if !fix.IsReplay {
			continue
		}
		t.Run(string(fix.EventType)+"_replay", func(t *testing.T) {
			sNew, actions, err := Next(fix.FromState, fix.Role, fix.Event)
			if err != nil {
				t.Fatalf("unexpected error on replay: %v", err)
			}
			if sNew != fix.FromState {
				t.Fatalf("state changed on replay: expected %s, got %s", fix.FromState, sNew)
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

	s, actions, err := Next(StateNone, RoleSender, ev)
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
	_, recvActs, err := Next(StateNone, RoleReceiver, evRecv)
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
	evOverflow := Event{
		Type:              EventReclaimEligible,
		ParentBlockTime:   1000,
		ParentConfirmTime: math.MaxUint64 - 50,
		Timeout:           100,
		Value:             testValue,
	}
	_, _, err := Next(s, RoleSender, evOverflow)
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
	sNew, acts, err := Next(s, RoleSender, evExact)
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
	_, _, errEarly := Next(s, RoleSender, evEarly)
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
		_, _, err1 := Next(StateNone, RoleSender, Event{Type: EventTxSubmitted, Sender: testSender, Target: testTarget, Value: v})
		if !errors.Is(err1, ErrInvalidAmount) {
			t.Errorf("TxSubmitted: expected ErrInvalidAmount for value %v, got %v", v, err1)
		}

		// ReclaimEligible
		_, _, err2 := Next(StateSentConfirmed, RoleSender, Event{Type: EventReclaimEligible, ParentBlockTime: 200, ParentConfirmTime: 100, Timeout: 50, Value: v})
		if !errors.Is(err2, ErrInvalidAmount) {
			t.Errorf("ReclaimEligible: expected ErrInvalidAmount for value %v, got %v", v, err2)
		}

		// CreditObserved
		_, _, err3 := Next(StateNone, RoleReceiver, Event{Type: EventCreditObserved, IsDestinationValid: true, Value: v})
		if !errors.Is(err3, ErrInvalidAmount) {
			t.Errorf("CreditObserved: expected ErrInvalidAmount for value %v, got %v", v, err3)
		}

		// ClaimedConfirmed
		_, _, err4 := Next(StateMarkedClaimedPendingCredit, RoleReceiver, Event{Type: EventClaimedConfirmed, Target: testTarget, Value: v})
		if !errors.Is(err4, ErrInvalidAmount) {
			t.Errorf("ClaimedConfirmed: expected ErrInvalidAmount for value %v, got %v", v, err4)
		}
	}
}

// 7. Role invariant validation: unknown role or role mismatch must be rejected
func TestRoleInvariantValidation(t *testing.T) {
	// Unknown role rejected
	_, _, err := Next(StateNone, RoleUnknown, Event{Type: EventTxSubmitted, Sender: testSender, Value: testValue})
	if !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("expected ErrInvalidRole for RoleUnknown, got %v", err)
	}

	// Invalid role value rejected
	_, _, err = Next(StateNone, Role(99), Event{Type: EventTxSubmitted, Sender: testSender, Value: testValue})
	if !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("expected ErrInvalidRole for Role(99), got %v", err)
	}

	// State role mismatch with record role rejected
	_, _, err = Next(StateLocalAppliedPendingSend, RoleReceiver, Event{Type: EventCreditObserved, Value: testValue})
	if !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("expected ErrInvalidRole when state is Sender but role is Receiver, got %v", err)
	}

	// Event declared role mismatch with record role rejected
	_, _, err = Next(StateNone, RoleSender, Event{Type: EventTxSubmitted, Role: RoleReceiver, Sender: testSender, Value: testValue})
	if !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("expected ErrInvalidRole when event.Role != recordRole, got %v", err)
	}
}

// 8. Property / Fuzz invariant: No execution path can reach both CONFIRMED_SUCCESS and CONFIRMED_REFUNDED
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
			newState, _, err := Next(state, RoleSender, ev)
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

// 9. Go Fuzz Target: Proves mathematical mutual exclusion between terminal states
func FuzzMutualExclusion(f *testing.F) {
	// Seed corpus with sample event sequences
	f.Add([]byte{0, 1, 3})
	f.Add([]byte{0, 1, 4, 5})
	f.Add([]byte{0, 1, 6, 7})
	f.Add([]byte{0, 1, 6, 8, 4})
	f.Add([]byte{2, 0, 1, 8, 7})

	allSenderEvents := []Event{
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

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}

		state := StateNone
		reachedSuccess := false
		reachedRefunded := false

		for _, b := range data {
			idx := int(b) % len(allSenderEvents)
			ev := allSenderEvents[idx]

			newState, _, err := Next(state, RoleSender, ev)
			if err == nil {
				state = newState
				if state == StateConfirmedSuccess {
					reachedSuccess = true
				}
				if state == StateConfirmedRefunded {
					reachedRefunded = true
				}
			}

			// Invariant check at every transition:
			if reachedSuccess && reachedRefunded {
				t.Fatalf("Fuzz violation: state sequence reached both ConfirmedSuccess and ConfirmedRefunded!")
			}
		}
	})
}
