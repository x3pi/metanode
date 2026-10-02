package rollup

import (
	"fmt"
)

// Next performs a pure, deterministic state transition for cross-node rollup transactions.
// It is completely free of I/O, storage access, system clocks, or network calls.
// Transition errors return ErrInvalidTransition (or specific error) without panicking.
// Repeated events leading to the same state are treated as idempotent no-ops (emitting no actions).
func Next(current State, recordRole Role, event Event) (State, []Action, error) {
	if recordRole != RoleSender && recordRole != RoleReceiver {
		return current, nil, fmt.Errorf("%w: record role must be SENDER or RECEIVER, got %s", ErrInvalidRole, recordRole)
	}

	// EventRPCSubmitted is shared by both roles (see EventType.Role's doc comment) — its
	// expected role comes from the caller-supplied Event.Role, not from the event type.
	if event.Type == EventRPCSubmitted {
		if event.Role != RoleSender && event.Role != RoleReceiver {
			return current, nil, fmt.Errorf("%w: EventRPCSubmitted requires an explicit Event.Role", ErrInvalidRole)
		}
		if event.Role != recordRole {
			return current, nil, fmt.Errorf("%w: event role %s does not match record role %s", ErrInvalidRole, event.Role, recordRole)
		}
	} else {
		expectedRole := event.Type.Role()
		if expectedRole == RoleUnknown {
			return current, nil, fmt.Errorf("%w: unknown event type '%s'", ErrInvalidTransition, event.Type)
		}

		// 1. Role invariant checks
		if expectedRole != recordRole {
			return current, nil, fmt.Errorf("%w: event %s role %s does not match record role %s", ErrInvalidRole, event.Type, expectedRole, recordRole)
		}
		if event.Role != RoleUnknown && event.Role != recordRole {
			return current, nil, fmt.Errorf("%w: event role %s does not match record role %s", ErrInvalidRole, event.Role, recordRole)
		}
	}
	if current != StateNone && current.Role() != recordRole {
		return current, nil, fmt.Errorf("%w: cannot process %s event in %s state %s", ErrInvalidRole, recordRole, current.Role(), current)
	}

	switch event.Type {

	// ==========================================
	// SENDER SIDE TRANSITIONS (10..19)
	// ==========================================

	case EventTxSubmitted:
		if current == StateLocalAppliedPendingSend {
			// Idempotent replay: no action emitted
			return StateLocalAppliedPendingSend, nil, nil
		}
		if current != StateNone {
			return current, nil, fmt.Errorf("%w: cannot submit tx from state %s", ErrInvalidTransition, current)
		}
		if event.Value == nil || event.Value.Sign() <= 0 {
			return current, nil, ErrInvalidAmount
		}

		// The Parent Chain charges the cluster's float Value + GasFee for the transfer (the fee is burned there), so the
		// sender must be debited the same total here: the cluster's BLS float always equals the sum of its accounts.
		debit := CloneBigInt(event.Value)
		if event.GasFee != nil && event.GasFee.Sign() > 0 {
			debit.Add(debit, event.GasFee)
		}
		actions := []Action{
			{
				Type:   ActionDeductBalance,
				Target: event.Sender,
				Amount: debit,
			},
			{
				Type:   ActionCreateRecord,
				Target: event.Target,
				Amount: CloneBigInt(event.Value),
			},
			{
				Type:   ActionSendTransfer,
				Target: event.Target,
				Amount: CloneBigInt(event.Value),
			},
		}
		return StateLocalAppliedPendingSend, actions, nil

	case EventRPCSubmitted:
		if current == StateSendSubmitted || current == StateMarkClaimedSubmitted {
			return current, nil, nil // Idempotent
		}
		if current == StateLocalAppliedPendingSend {
			return StateSendSubmitted, nil, nil
		}
		if current == StateMarkedClaimedPendingCredit || current == StateMarkedClaimedPendingRefund {
			return StateMarkClaimedSubmitted, nil, nil
		}
		return current, nil, fmt.Errorf("%w: cannot submit to RPC from state %s", ErrInvalidTransition, current)

	case EventParentConfirmed:
		if current == StateSentConfirmed {
			return StateSentConfirmed, nil, nil // Idempotent
		}
		if current != StateSendSubmitted {
			return current, nil, fmt.Errorf("%w: cannot confirm parent from state %s", ErrInvalidTransition, current)
		}
		return StateSentConfirmed, nil, nil

	case EventSendFailedTransient:
		if current == StateLocalAppliedPendingSend || current == StateSendSubmitted {
			return current, nil, nil
		}
		return current, nil, fmt.Errorf("%w: transient failure not applicable in state %s", ErrInvalidTransition, current)

	case EventClaimedObserved:
		if current == StateConfirmedSuccess && event.Outcome == OutcomeCredited {
			return StateConfirmedSuccess, nil, nil // Idempotent
		}
		if current == StateRefundInTransit && event.Outcome == OutcomeRefund {
			return StateRefundInTransit, nil, nil // Idempotent
		}
		if current != StateSentConfirmed && current != StateReclaimSubmitted {
			return current, nil, fmt.Errorf("%w: claimed observed not allowed from state %s", ErrInvalidTransition, current)
		}

		switch event.Outcome {
		case OutcomeCredited:
			return StateConfirmedSuccess, nil, nil
		case OutcomeRefund:
			return StateRefundInTransit, nil, nil
		default:
			return current, nil, fmt.Errorf("%w: unknown or missing outcome %s", ErrInvalidOutcome, event.Outcome)
		}

	case EventRefundObserved:
		if current == StateConfirmedRefunded {
			return StateConfirmedRefunded, nil, nil // Idempotent
		}
		if current != StateRefundInTransit {
			return current, nil, fmt.Errorf("%w: refund observed not allowed from state %s", ErrInvalidTransition, current)
		}
		return StateConfirmedRefunded, nil, nil

	case EventReclaimEligible:
		if current == StateReclaimSubmitted {
			return StateReclaimSubmitted, nil, nil // Idempotent
		}
		if current != StateSentConfirmed {
			return current, nil, fmt.Errorf("%w: reclaim eligible check not allowed from state %s", ErrInvalidTransition, current)
		}
		// Overflow-safe elapsed time comparison
		if event.ParentBlockTime < event.ParentConfirmTime || (event.ParentBlockTime-event.ParentConfirmTime) < event.Timeout {
			return current, nil, fmt.Errorf("%w: blockTime %d < confirmTime %d + timeout %d", ErrReclaimTooEarly, event.ParentBlockTime, event.ParentConfirmTime, event.Timeout)
		}
		if event.Value == nil || event.Value.Sign() <= 0 {
			return current, nil, ErrInvalidAmount
		}
		actions := []Action{
			{
				Type:   ActionSendReclaim,
				Amount: CloneBigInt(event.Value),
			},
		}
		return StateReclaimSubmitted, actions, nil

	case EventReclaimWon:
		if current == StateConfirmedRefunded {
			return StateConfirmedRefunded, nil, nil // Idempotent
		}
		if current != StateReclaimSubmitted {
			return current, nil, fmt.Errorf("%w: reclaim won not allowed from state %s", ErrInvalidTransition, current)
		}
		if event.Value == nil || event.Value.Sign() <= 0 {
			return current, nil, ErrInvalidAmount
		}
		actions := []Action{
			{
				Type:   ActionCreditLocal,
				Target: event.Sender,
				Amount: CloneBigInt(event.Value),
			},
		}
		return StateConfirmedRefunded, actions, nil

	case EventReclaimLost:
		if current == StateSentConfirmed {
			return StateSentConfirmed, nil, nil // Idempotent
		}
		if current != StateReclaimSubmitted {
			return current, nil, fmt.Errorf("%w: reclaim lost not allowed from state %s", ErrInvalidTransition, current)
		}
		return StateSentConfirmed, nil, nil

	// ==========================================
	// RECEIVER SIDE TRANSITIONS (21..29)
	// ==========================================

	case EventCreditObserved:
		if current == StateSkippedDup && event.IsDuplicate {
			return StateSkippedDup, nil, nil // Idempotent
		}
		if current == StateMarkedClaimedPendingCredit && !event.IsDuplicate && event.IsDestinationValid {
			return StateMarkedClaimedPendingCredit, nil, nil // Idempotent
		}
		if current == StateMarkedClaimedPendingRefund && !event.IsDuplicate && !event.IsDestinationValid {
			return StateMarkedClaimedPendingRefund, nil, nil // Idempotent
		}
		// General idempotent case: a re-observation (IsDuplicate=true, i.e. store.Get(msgID)
		// already found a record) of a message that's already progressed past StateNone into
		// ANY other state is always safe to no-op rather than error. The 3 specific cases above
		// only cover exact (state, IsDuplicate, IsDestinationValid) combinations; this catches
		// every other already-progressed state (e.g. StateMarkedClaimedPendingCredit with
		// IsDuplicate=true, which the second check above does NOT cover since it requires
		// !IsDuplicate). Found live: ReceiveWorker's inbound-transfer poll can re-discover and
		// re-fire EventCreditObserved for an already-processed message (its cursor is
		// intentionally in-memory-only now, see worker_receive.go's cursor field doc comment,
		// so this WILL happen at least once after every process restart) -- without this, that
		// re-fire errored instead of no-op'ing, and critically still consumed that poll tick's
		// one dispatchable nonce slot ahead of the record's real next step, starving it.
		if event.IsDuplicate && current != StateNone {
			return current, nil, nil // Idempotent
		}

		if current != StateNone {
			return current, nil, fmt.Errorf("%w: credit observed not allowed from state %s", ErrInvalidTransition, current)
		}

		if event.IsDuplicate {
			return StateSkippedDup, nil, nil
		}

		if event.Value == nil || event.Value.Sign() <= 0 {
			return current, nil, ErrInvalidAmount
		}

		if event.IsDestinationValid {
			actions := []Action{
				{
					Type:    ActionMarkClaimed,
					Outcome: OutcomeCredited,
					Amount:  CloneBigInt(event.Value),
				},
			}
			return StateMarkedClaimedPendingCredit, actions, nil
		}

		actions := []Action{
			{
				Type:    ActionMarkClaimed,
				Outcome: OutcomeRefund,
				Amount:  CloneBigInt(event.Value),
			},
		}
		return StateMarkedClaimedPendingRefund, actions, nil

	case EventClaimedConfirmed:
		if current == StateCredited {
			return StateCredited, nil, nil // Idempotent
		}
		if current == StateRefundSent {
			return StateRefundSent, nil, nil // Idempotent
		}
		if current != StateMarkClaimedSubmitted {
			return current, nil, fmt.Errorf("%w: cannot confirm claimed from state %s", ErrInvalidTransition, current)
		}

		switch event.Outcome {
		case OutcomeCredited:
			if event.Value == nil || event.Value.Sign() <= 0 {
				return current, nil, ErrInvalidAmount
			}
			actions := []Action{
				{
					Type:   ActionCreditLocal,
					Target: event.Target,
					Amount: CloneBigInt(event.Value),
				},
			}
			return StateCredited, actions, nil

		case OutcomeRefund:
			if event.Value == nil || event.Value.Sign() <= 0 {
				return current, nil, ErrInvalidAmount
			}
			// Only refund Value, GasFee is NOT refunded
			actions := []Action{
				{
					Type:   ActionSendRefund,
					Target: event.Sender,
					Amount: CloneBigInt(event.Value),
				},
			}
			return StateRefundSent, actions, nil

		default:
			return current, nil, fmt.Errorf("%w: claimed confirmed not allowed from state %s", ErrInvalidTransition, current)
		}

	case EventRefundConfirmed:
		if current == StateRefunded {
			return StateRefunded, nil, nil // Idempotent
		}
		if current != StateRefundSent {
			return current, nil, fmt.Errorf("%w: refund confirmed not allowed from state %s", ErrInvalidTransition, current)
		}
		return StateRefunded, nil, nil

	default:
		return current, nil, fmt.Errorf("%w: unhandled event type '%s'", ErrInvalidTransition, event.Type)
	}
}
