package rollup

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

var (
	ErrInvalidTransition = errors.New("invalid state transition")
	ErrInvalidRole       = errors.New("invalid role for event")
	ErrInvalidOutcome    = errors.New("invalid outcome")
	ErrInvalidAmount     = errors.New("amount must be non-nil and strictly positive")
	ErrReclaimTooEarly   = errors.New("reclaim attempted before timeout threshold")
)

// Role defines whether this state machine handles the sender or receiver side.
type Role uint32

const (
	RoleUnknown  Role = 0
	RoleSender   Role = 1
	RoleReceiver Role = 2
)

func (r Role) String() string {
	switch r {
	case RoleSender:
		return "SENDER"
	case RoleReceiver:
		return "RECEIVER"
	default:
		return "UNKNOWN"
	}
}

// State represents the lifecycle state of a cross-node rollup message.
type State uint32

const (
	StateNone State = 0

	// Sender states (Phía gửi: 10..19)
	StateLocalAppliedPendingSend State = 10
	StateSentConfirmed           State = 11
	StateReclaimSubmitted        State = 12
	StateRefundInTransit         State = 13
	StateConfirmedRefunded       State = 18 // terminal
	StateConfirmedSuccess        State = 19 // terminal

	// Receiver states (Phía nhận: 21..29)
	StateMarkedClaimedPendingCredit State = 21
	StateMarkedClaimedPendingRefund State = 22
	StateRefundSent                 State = 23
	StateSkippedDup                 State = 27 // terminal
	StateRefunded                   State = 28 // terminal
	StateCredited                   State = 29 // terminal
)

func (s State) String() string {
	switch s {
	case StateNone:
		return "NONE"
	case StateLocalAppliedPendingSend:
		return "LOCAL_APPLIED_PENDING_SEND"
	case StateSentConfirmed:
		return "SENT_CONFIRMED"
	case StateReclaimSubmitted:
		return "RECLAIM_SUBMITTED"
	case StateRefundInTransit:
		return "REFUND_IN_TRANSIT"
	case StateConfirmedRefunded:
		return "CONFIRMED_REFUNDED"
	case StateConfirmedSuccess:
		return "CONFIRMED_SUCCESS"
	case StateMarkedClaimedPendingCredit:
		return "MARKED_CLAIMED_PENDING_CREDIT"
	case StateMarkedClaimedPendingRefund:
		return "MARKED_CLAIMED_PENDING_REFUND"
	case StateRefundSent:
		return "REFUND_SENT"
	case StateSkippedDup:
		return "SKIPPED_DUP"
	case StateRefunded:
		return "REFUNDED"
	case StateCredited:
		return "CREDITED"
	default:
		return fmt.Sprintf("UNKNOWN_STATE(%d)", uint32(s))
	}
}

// Role returns the expected role associated with this state.
func (s State) Role() Role {
	switch {
	case s >= 10 && s <= 19:
		return RoleSender
	case s >= 21 && s <= 29:
		return RoleReceiver
	default:
		return RoleUnknown
	}
}

// IsTerminal returns true if the state cannot transition to any new state.
func (s State) IsTerminal() bool {
	switch s {
	case StateConfirmedRefunded, StateConfirmedSuccess, StateSkippedDup, StateRefunded, StateCredited:
		return true
	default:
		return false
	}
}

// Outcome indicates the final disposition of a cross-chain transfer.
type Outcome uint32

const (
	OutcomeNone     Outcome = 0
	OutcomeCredited Outcome = 1
	OutcomeRefund   Outcome = 2
)

func (o Outcome) String() string {
	switch o {
	case OutcomeNone:
		return "NONE"
	case OutcomeCredited:
		return "CREDITED"
	case OutcomeRefund:
		return "REFUND"
	default:
		return fmt.Sprintf("UNKNOWN_OUTCOME(%d)", uint32(o))
	}
}

// ActionType defines the side-effect or outbound operation to perform.
type ActionType string

const (
	ActionDeductBalance ActionType = "DeductBalance"
	ActionCreateRecord  ActionType = "CreateRecord"
	ActionSendTransfer  ActionType = "SendTransfer"
	ActionMarkClaimed   ActionType = "MarkClaimed"
	ActionCreditLocal   ActionType = "CreditLocal"
	ActionSendRefund    ActionType = "SendRefund"
	ActionSendReclaim   ActionType = "SendReclaim"
)

// Action encapsulates a command emitted during state transition.
type Action struct {
	Type    ActionType
	Target  common.Address
	Amount  *big.Int
	Outcome Outcome
}

// EventType defines the type of event driving state transitions.
type EventType string

const (
	EventTxSubmitted         EventType = "TxSubmitted"
	EventParentConfirmed     EventType = "ParentConfirmed"
	EventSendFailedTransient EventType = "SendFailedTransient"
	EventClaimedObserved     EventType = "ClaimedObserved"
	EventRefundObserved      EventType = "RefundObserved"
	EventReclaimEligible     EventType = "ReclaimEligible"
	EventReclaimWon          EventType = "ReclaimWon"
	EventReclaimLost         EventType = "ReclaimLost"

	EventCreditObserved   EventType = "CreditObserved"
	EventClaimedConfirmed EventType = "ClaimedConfirmed"
	EventRefundConfirmed  EventType = "RefundConfirmed"
)

// Role returns the role that is allowed to process this event type.
func (e EventType) Role() Role {
	switch e {
	case EventTxSubmitted, EventParentConfirmed, EventSendFailedTransient,
		EventClaimedObserved, EventRefundObserved, EventReclaimEligible,
		EventReclaimWon, EventReclaimLost:
		return RoleSender
	case EventCreditObserved, EventClaimedConfirmed, EventRefundConfirmed:
		return RoleReceiver
	default:
		return RoleUnknown
	}
}

// Event contains inputs to the pure state machine.
type Event struct {
	Type EventType
	Role Role // Optional, if set must match EventType.Role()

	// Common fields
	Sender common.Address
	Target common.Address
	Value  *big.Int
	GasFee *big.Int

	// Parent Chain timing / metadata
	ParentBlockTime   uint64
	ParentConfirmTime uint64
	Timeout           uint64
	ParentTxHash      common.Hash

	// Outcome for ClaimedObserved
	Outcome Outcome

	// Receiver fields
	IsDuplicate        bool
	IsDestinationValid bool
}

// CloneBigInt safely deep-copies a *big.Int to prevent aliasing mutations.
func CloneBigInt(v *big.Int) *big.Int {
	if v == nil {
		return nil
	}
	return new(big.Int).Set(v)
}
