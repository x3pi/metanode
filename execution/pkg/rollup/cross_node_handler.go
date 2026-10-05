package rollup

import (
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

type AccountStateDB interface {
	GetBalance(addr common.Address) *big.Int
	AddBalance(addr common.Address, amount *big.Int) error
	SubBalance(addr common.Address, amount *big.Int)
	GetNonce(addr common.Address) uint64
	SetNonce(addr common.Address, nonce uint64)
}

type CrossNodeHandler struct {
	fromKey cm.PublicKey

	// Using a simple lock for serialization. In true BlockSTM it would be handled differently.
	mu sync.Mutex
}

func NewCrossNodeHandler(fromKey cm.PublicKey) *CrossNodeHandler {
	return &CrossNodeHandler{
		fromKey: fromKey,
	}
}

// ComputeMessageID calculates the deterministic ID of a cross-node message according to Parent Chain rules.
// CrossNodeTransferFee is the flat fee a sender pays on top of the transfer value for a cross-node transfer. It is
// consensus state (part of the message ID and of the float accounting); callers that pre-validate a balance
// (ParentChainGatewayHandler) must use this same constant.
const CrossNodeTransferFee int64 = 100

func ComputeMessageID(fromKey, toKey cm.PublicKey, sender, target common.Address, value, fee *big.Int, payloadHash common.Hash, nonce uint64) common.Hash {
	digest := parentchain.ComputeTransferFloatMessage(fromKey, toKey, sender, target, value, fee, payloadHash, nonce)
	return crypto.Keccak256Hash(digest)
}

// HandleTransfer processes a cross-node transfer request from a user.
//
// stateDB MUST be freshly derived from the chainState instance the CALLING barrier-tx execution
// is actually operating against (see ParentChainGatewayHandler.HandleTransaction), never a
// reference captured once at App-construction time. The account balance/nonce this method
// mutates is also what TxValidatorPool's own nonce-gap ("future tx") check reads when deciding
// whether a later tx from the same sender is ready to execute; if this write lands in a
// different chainState instance than that check reads (e.g. app.chainState, the global/base
// instance, while the running block is executing against its own speculative clone), the two
// views permanently diverge — found live: SetNonce below appeared to succeed every time, but
// the account nonce the tx-pool observed never advanced, so every later system tx from
// app.go's eventProposer piled up as a permanent "future" tx and the credited balance never
// reached a real, committed block no matter how many times ReceiveWorker retried.
// store has the same per-call freshness requirement as stateDB above, and for the same reason:
// found live, empirically, that Put(record) below going through app.chainState-scoped storage
// (rather than the calling barrier-tx's own chainState) meant the record was NEVER visible to
// ScanNonTerminal() afterward — confirmed via direct instrumentation (0 records found on every
// tick, forever) even though HandleTransfer itself reported success every time.
func (h *CrossNodeHandler) HandleTransfer(
	store Store,
	stateDB AccountStateDB,
	toKey cm.PublicKey,
	sender common.Address,
	target common.Address,
	value *big.Int,
	payloadHash common.Hash,
) (common.Hash, error) {
	if store == nil {
		return common.Hash{}, fmt.Errorf("store is nil")
	}
	if stateDB == nil {
		return common.Hash{}, fmt.Errorf("stateDB is nil")
	}
	if value == nil || value.Sign() <= 0 {
		return common.Hash{}, fmt.Errorf("invalid value")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// 1. Check balance: the sender pays the value AND the transfer fee (both leave the cluster's float on the Parent
	// Chain, so both must leave the cluster's accounts here).
	fee := big.NewInt(CrossNodeTransferFee)
	need := new(big.Int).Add(value, fee)
	balance := stateDB.GetBalance(sender)
	if balance == nil {
		balance = big.NewInt(0)
	}
	if balance.Cmp(need) < 0 {
		return common.Hash{}, fmt.Errorf("insufficient balance: have %v, need %v (value %v + fee %v)", balance, need, value, fee)
	}

	// 2. Generate source sequence
	seq, err := store.GetNextFloatSeq()
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to get next float seq: %w", err)
	}

	// 3. Compute deterministic MessageID
	msgID := ComputeMessageID(h.fromKey, toKey, sender, target, value, fee, payloadHash, seq)

	// 4. Run State Machine Transition
	event := Event{
		Type:   EventTxSubmitted,
		Role:   RoleSender,
		Sender: sender,
		Target: target,
		Value:  value,
		GasFee: fee,
	}

	newState, actions, err := Next(StateNone, RoleSender, event)
	if err != nil {
		return common.Hash{}, fmt.Errorf("state transition failed: %w", err)
	}

	// 5. Apply Actions Atomically (In Memory / DB)
	for _, action := range actions {
		if action.Type == ActionDeductBalance {
			stateDB.SubBalance(action.Target, action.Amount)
		}
	}

	if err := store.IncrementFloatSeq(); err != nil {
		return common.Hash{}, fmt.Errorf("failed to increment float seq: %w", err)
	}
	stateDB.SetNonce(sender, stateDB.GetNonce(sender)+1)

	// 6. Create and Save Record
	record := &MessageRecord{
		MessageID:   msgID,
		Role:        RoleSender,
		State:       newState,
		Sender:      sender,
		Target:      target,
		Value:       value,
		GasFee:      fee,
		SourceSeq:   seq,
		SourcePubKey: h.fromKey,
		DestPubKey:   toKey,
		PayloadHash: payloadHash,
	}

	if err := store.Put(record); err != nil {
		return common.Hash{}, fmt.Errorf("failed to save rollup record: %w", err)
	}

	return msgID, nil
}

// HandleSystemEvent applies a rollup state machine event deterministically.
// This MUST be called by the transaction executor during Raft block processing
// to ensure zero state drift across all replicas.
//
// stateDB and writeStore have the same per-call freshness requirement documented on
// HandleTransfer above — both must come from the calling barrier-tx execution's own chainState
// (a snapshot), not a reference captured once at App-construction time, so this call's writes
// get included in whatever eventually gets promoted to the live chainState.
//
// readStore is DIFFERENT on purpose: it must be scoped to the live, always-current chainState
// (e.g. app.chainState in cmd/simple_chain), NOT the same barrier-tx snapshot as writeStore.
// The barrier-tx snapshot is taken at some earlier point in the speculative-execution pipeline
// and never refreshes — a worker goroutine's own direct Put() (e.g. ReceiveWorker.
// processMarkClaimedPendingCredit advancing a record to StateMarkClaimedSubmitted) that lands
// AFTER that snapshot was taken but BEFORE this barrier tx dispatches is invisible to a read
// through writeStore/the snapshot. Found live: HandleSystemEvent's own Get(msgID) kept seeing
// the record stuck at StateMarkedClaimedPendingCredit — one step behind what the worker had
// already durably advanced it to — so EventClaimedConfirmed was rejected every time with
// "cannot confirm claimed from state MARKED_CLAIMED_PENDING_CREDIT" even though the real,
// current record was already at StateMarkClaimedSubmitted. Reading through readStore instead
// fixes this; the final Put still goes through writeStore so THIS call's own result is durable.
func (h *CrossNodeHandler) HandleSystemEvent(readStore Store, writeStore Store, stateDB AccountStateDB, event Event, msgID common.Hash, sourceSeq uint64, sourcePubKey cm.PublicKey, destPubKey cm.PublicKey, payloadHash common.Hash) error {
	if readStore == nil {
		return fmt.Errorf("readStore is nil")
	}
	if writeStore == nil {
		return fmt.Errorf("writeStore is nil")
	}
	if stateDB == nil {
		return fmt.Errorf("stateDB is nil")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	rec, found, err := readStore.Get(msgID)
	if err != nil {
		return fmt.Errorf("failed to get record: %w", err)
	}

	var currentState State = StateNone
	if found {
		currentState = rec.State
	} else if event.Type == EventCreditObserved {
		// Initialize new record
		rec = &MessageRecord{
			MessageID:   msgID,
			Role:        event.Role,
			State:       StateNone,
			Sender:      event.Sender,
			Target:      event.Target,
			Value:       event.Value,
			SourceSeq:   sourceSeq,
			SourcePubKey: sourcePubKey,
			DestPubKey:   destPubKey,
			PayloadHash: payloadHash,
		}
	} else {
		return fmt.Errorf("record not found for event %v", event.Type)
	}

	// For EventCreditObserved, check if duplicate
	if event.Type == EventCreditObserved {
		event.IsDuplicate = found
	}

	newState, actions, err := Next(currentState, event.Role, event)
	if err != nil {
		return fmt.Errorf("state transition failed: %w", err)
	}

	// Apply actions atomically
	for _, action := range actions {
		if action.Type == ActionDeductBalance {
			stateDB.SubBalance(action.Target, action.Amount)
		} else if action.Type == ActionCreditLocal {
			// Must use AddBalance, not SubBalance with a negated amount: AccountStateDB.SubBalance
			// (pkg/account_state_db/account_state_db_mutations.go) explicitly guards
			// `amount.Sign() <= 0` as a no-op ("subtracting zero or negative"), so a negated
			// (negative) amount silently credited nothing at all. Found live: EventClaimedConfirmed
			// processed with no error every time, MarkClaimed on the Parent Chain genuinely
			// succeeded, yet the destination's on-chain balance stayed 0 forever.
			if err := stateDB.AddBalance(action.Target, action.Amount); err != nil {
				return fmt.Errorf("failed to credit local balance: %w", err)
			}
		}
		// Note: ActionSendRefund is not applied here. It requires sending an HTTP request,
		// which is non-deterministic and must be done by a background worker polling the state,
		// NOT during Raft block execution!
	}

	rec.State = newState
	if err := writeStore.Put(rec); err != nil {
		return fmt.Errorf("failed to save record: %w", err)
	}

	return nil
}
