package rollup

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
)

type ReceiveWorker struct {
	store       Store
	stateDB     AccountStateDB
	client      parentchain.Client
	blsKeyPair  *bls.KeyPair

	// cursor is kept IN-MEMORY ONLY, not persisted via store/SmartContractDB. It used to be
	// written directly with dbStore.scDB.SetStorageValue() from this worker's own goroutine,
	// outside any barrier-tx execution -- but chainState's SmartContractDB pointer gets
	// wholesale REPLACED (cmd/simple_chain/processor/speculative_executor.go's committer,
	// bp.chainState.SetSmartContractDB(...)) every time a block containing ANY real tx commits,
	// swapping in a speculative clone that was snapshotted BEFORE this worker's direct write
	// happened -- silently discarding it. Confirmed live: setCursor(1)'s own immediate readback
	// correctly showed 1, but the very next 5s poll tick read back 0 again, every time, because
	// at least one such swap happened in between (this devnet's system-tx retries alone produced
	// dozens of them). The cursor is a pure "don't re-fetch already-seen events" optimization,
	// not a correctness requirement -- handleIncomingTransfer's real duplicate-safety comes from
	// IsDuplicate (store.Get(msgID), which DOES persist reliably because it's only ever written
	// from inside a real barrier-tx execution against that tx's own chainState, per this
	// session's earlier CrossNodeHandler fix). Keeping the cursor in memory sidesteps the whole
	// swap-discards-writes class of bug for this piece of state entirely; the worst case on a
	// process restart is a burst of re-fetched "duplicate" events, which the state machine now
	// (see statemachine.go's EventCreditObserved idempotent cases) rejects cleanly instead of
	// erroring.
	cursor atomic.Uint64

	Validator func(common.Address) bool

	// EventProposer is used to submit state machine events to the Raft consensus.
	// In a real execution node, this MUST submit a SystemTransaction to the tx_pool
	// so the event can be processed atomically by the Block Processor.
	// If nil, the worker executes events locally (unsafe for Raft, only for tests).
	EventProposer func(event Event, msgID common.Hash, sourceSeq uint64, sourcePubKey cm.PublicKey, destPubKey cm.PublicKey, payloadHash common.Hash) error

	wakeCh chan struct{}
	quitCh chan struct{}
	wg     sync.WaitGroup
}

func NewReceiveWorker(store Store, stateDB AccountStateDB, client parentchain.Client, blsKeyPair *bls.KeyPair) *ReceiveWorker {
	return &ReceiveWorker{
		store:      store,
		stateDB:    stateDB,
		client:     client,
		blsKeyPair: blsKeyPair,
		wakeCh:     make(chan struct{}, 1),
		quitCh:     make(chan struct{}),
	}
}

func (w *ReceiveWorker) Start() {
	w.wg.Add(1)
	go w.loop()
}

func (w *ReceiveWorker) Stop() {
	close(w.quitCh)
	w.wg.Wait()
}

func (w *ReceiveWorker) WakeUp() {
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
}

func (w *ReceiveWorker) getCursor() uint64 {
	return w.cursor.Load()
}

func (w *ReceiveWorker) setCursor(cursor uint64) {
	w.cursor.Store(cursor)
}

func (w *ReceiveWorker) isReleasable(record *MessageRecord) bool {
	if raftfeed.Enabled() {
		if node := raftfeed.GetNode(); node != nil {
			return node.IsLeader()
		}
		return false
	}
	return true
}

func (w *ReceiveWorker) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.quitCh:
			return
		case <-ticker.C:
			w.pollAndProcess()
		case <-w.wakeCh:
			w.pollAndProcess()
		}
	}
}

func (w *ReceiveWorker) pollAndProcess() {
	// 1. Poll new incoming transfers
	cursor := w.getCursor()
	transfers, newCursor, err := w.client.GetInboundTransfers(w.blsKeyPair.PublicKey(), cursor)
	if err != nil {
		log.Printf("ReceiveWorker: failed to fetch transfers: %v", err)
	} else {
		for _, tx := range transfers {
			w.handleIncomingTransfer(tx)
		}
		if newCursor > cursor {
			w.setCursor(newCursor)
		}
	}

	// 2. Process non-terminal records for the Receiver role
	records, err := w.store.ScanNonTerminal()
	if err != nil {
		log.Printf("ReceiveWorker: ScanNonTerminal error: %v", err)
		return
	}

	for _, rec := range records {
		if rec.Role != RoleReceiver {
			continue
		}
		if !w.isReleasable(rec) {
			continue
		}

		switch rec.State {
		case StateMarkedClaimedPendingCredit:
			w.processMarkClaimedPendingCredit(rec)
		case StateMarkedClaimedPendingRefund:
			w.processMarkClaimedPendingRefund(rec)
		case StateMarkClaimedSubmitted:
			w.processMarkClaimedSubmitted(rec)
		case StateRefundSent:
			w.processRefundSent(rec)
		}
	}
}

func (w *ReceiveWorker) handleIncomingTransfer(tx *parentchain.TransferEvent) {
	_, found, _ := w.store.Get(tx.MsgID)

	event := Event{
		Type:               EventCreditObserved,
		Role:               RoleReceiver,
		Sender:             tx.Sender,
		Target:             tx.Target,
		Value:              tx.Amount,
		IsDuplicate:        found,
		IsDestinationValid: w.isValidDestination(tx.Target),
	}

	if w.EventProposer != nil {
		if err := w.EventProposer(event, tx.MsgID, tx.SourceSeq, tx.SourcePubKey, tx.DestPubKey, tx.PayloadHash); err != nil {
			log.Printf("ReceiveWorker: failed to propose EventCreditObserved for %x: %v", tx.MsgID, err)
		}
		return
	}

	// Fallback: local direct execution (only for testing without Raft)
	newState, _, err := Next(StateNone, RoleReceiver, event)
	if err != nil {
		log.Printf("ReceiveWorker: next failed for incoming msg %x: %v", tx.MsgID, err)
		return
	}

	if !found {
		record := &MessageRecord{
			MessageID:   tx.MsgID,
			Role:        RoleReceiver,
			State:       newState, // SKIPPED_DUP, MARKED_CLAIMED_PENDING_CREDIT, or MARKED_CLAIMED_PENDING_REFUND
			Sender:      tx.Sender,
			Target:      tx.Target,
			Value:       tx.Amount,
			SourceSeq:   tx.SourceSeq,
			SourcePubKey: tx.SourcePubKey,
			DestPubKey:   tx.DestPubKey,
			PayloadHash: tx.PayloadHash,
		}
		_ = w.store.Put(record)
	}
}

func (w *ReceiveWorker) isValidDestination(addr common.Address) bool {
	if w.Validator != nil {
		return w.Validator(addr)
	}
	// Simple validation, for phase B just assume true
	return true
}

func (w *ReceiveWorker) processMarkClaimedPendingCredit(rec *MessageRecord) {
	cert := w.signMarkClaimed(rec.MessageID, parentchain.FloatOutcomeCredited)
	_, err := w.client.SendMarkClaimed(rec.MessageID, parentchain.FloatOutcomeCredited, cert)
	if err != nil {
		if !strings.Contains(err.Error(), "already resolved") {
			return // wait for retry
		}
	}

	// Advance to submitted
	event := Event{
		Type: EventRPCSubmitted,
		Role: RoleReceiver,
	}

	if w.EventProposer != nil {
		if err := w.EventProposer(event, rec.MessageID, rec.SourceSeq, rec.SourcePubKey, rec.DestPubKey, rec.PayloadHash); err != nil {
			log.Printf("ReceiveWorker: failed to propose EventRPCSubmitted for %x: %v", rec.MessageID, err)
		}
		return
	}

	newState, _, err := Next(rec.State, RoleReceiver, event)
	if err == nil {
		rec.State = newState
		w.store.Put(rec)
	}
}

func (w *ReceiveWorker) processMarkClaimedPendingRefund(rec *MessageRecord) {
	cert := w.signMarkClaimed(rec.MessageID, parentchain.FloatOutcomeRefund)
	_, err := w.client.SendMarkClaimed(rec.MessageID, parentchain.FloatOutcomeRefund, cert)
	if err != nil {
		if !strings.Contains(err.Error(), "already resolved") {
			return 
		}
	}

	// Advance to submitted
	event := Event{
		Type: EventRPCSubmitted,
		Role: RoleReceiver,
	}

	if w.EventProposer != nil {
		if err := w.EventProposer(event, rec.MessageID, rec.SourceSeq, rec.SourcePubKey, rec.DestPubKey, rec.PayloadHash); err != nil {
			log.Printf("ReceiveWorker: failed to propose EventRPCSubmitted for %x: %v", rec.MessageID, err)
		}
		return
	}

	newState, _, err := Next(rec.State, RoleReceiver, event)
	if err == nil {
		rec.State = newState
		w.store.Put(rec)
	}
}

func (w *ReceiveWorker) processMarkClaimedSubmitted(rec *MessageRecord) {
	// Poll ParentChain to check if it has actually been confirmed
	outcome, err := w.client.GetClaimed(rec.MessageID)
	if err != nil {
		log.Printf("ReceiveWorker: failed to poll GetClaimed for msgID %x: %v", rec.MessageID, err)
		return
	}
	
	if outcome == parentchain.FloatOutcomeCredited || outcome == parentchain.FloatOutcomeRefund {
		// Advance based on confirmed outcome
		var smOutcome Outcome
		if outcome == parentchain.FloatOutcomeCredited {
			smOutcome = OutcomeCredited
		} else {
			smOutcome = OutcomeRefund
		}
		
		event := Event{
			Type:    EventClaimedConfirmed,
			Role:    RoleReceiver,
			Target:  rec.Target,
			Sender:  rec.Sender,
			Value:   rec.Value,
			Outcome: smOutcome,
		}

		if w.EventProposer != nil {
			if err := w.EventProposer(event, rec.MessageID, rec.SourceSeq, rec.SourcePubKey, rec.DestPubKey, rec.PayloadHash); err != nil {
				log.Printf("ReceiveWorker: failed to propose EventClaimedConfirmed for %x: %v", rec.MessageID, err)
			}
			return
		}

		// Fallback: local direct execution (only for testing without Raft)
		newState, actions, err := Next(rec.State, RoleReceiver, event)
		if err != nil {
			log.Printf("ReceiveWorker: Next(EventClaimedConfirmed) error: %v", err)
			return
		}
		
		// Apply Actions (CreditLocal or SendRefund)
		for _, action := range actions {
			if action.Type == ActionCreditLocal {
				// See cross_node_handler.go's HandleSystemEvent for why this must be
				// AddBalance, not SubBalance with a negated amount (AccountStateDB.SubBalance
				// silently no-ops on a non-positive amount).
				if err := w.stateDB.AddBalance(action.Target, action.Amount); err != nil {
					log.Printf("ReceiveWorker: failed to credit local balance for %x: %v", rec.MessageID, err)
				}
			} else if action.Type == ActionSendRefund {
				// Handle SendRefund (similar to what was in processMarkClaimedPendingRefund)
				seq, err := w.store.GetNextFloatSeq()
				if err != nil {
					log.Printf("ReceiveWorker: failed to get next float seq: %v", err)
					return
				}

				payloadHash := crypto.Keccak256Hash(nil)
				digest := parentchain.ComputeTransferFloatMessage(
					w.blsKeyPair.PublicKey(), rec.SourcePubKey, rec.Target, rec.Sender, rec.Value, nil, payloadHash, seq,
				)
				refundCert := bls.Sign(w.blsKeyPair.PrivateKey(), digest)
				refundMsgID := crypto.Keccak256Hash(digest)

				isRefund := true
				_, sendErr := w.client.SendTransferFloat(
					w.blsKeyPair.PublicKey(),
					rec.SourcePubKey, // destination is the source of the original transfer
					0, // destChainID
					rec.Target,
					rec.Sender,
					action.Amount,
					nil,
					seq,
					refundCert[:],
					isRefund,
				)
				if sendErr != nil {
					log.Printf("ReceiveWorker: failed to send refund transfer: %v", sendErr)
					return // Will retry
				}

				_ = w.store.IncrementFloatSeq()
				rec.RefundMsgID = refundMsgID
			}
		}

		rec.State = newState
		w.store.Put(rec)
	}
}

// processRefundSent polls Parent Chain to confirm the compensating refund Transfer this
// record already submitted (StateRefundSent, set by processMarkClaimedSubmitted's
// ActionSendRefund branch) actually landed, before closing this record out. Mirrors
// SendWorker.processPending's own submit-then-poll-confirm discipline: "accepted into the
// RPC queue" is not "applied" — see the state machine's EventRefundConfirmed (already
// existed, unused before this) for the terminal transition.
func (w *ReceiveWorker) processRefundSent(rec *MessageRecord) {
	if rec.RefundMsgID == (common.Hash{}) {
		log.Printf("ReceiveWorker: record %x in StateRefundSent has no RefundMsgID to poll (pre-existing record from before this field existed?)", rec.MessageID)
		return
	}
	_, found, err := w.client.GetTransferRecord(rec.RefundMsgID)
	if err != nil {
		log.Printf("ReceiveWorker: failed to poll refund msgID %x: %v", rec.RefundMsgID, err)
		return
	}
	if !found {
		return // not yet applied, wait for retry
	}

	event := Event{Type: EventRefundConfirmed, Role: RoleReceiver}

	if w.EventProposer != nil {
		if err := w.EventProposer(event, rec.MessageID, rec.SourceSeq, rec.SourcePubKey, rec.DestPubKey, rec.PayloadHash); err != nil {
			log.Printf("ReceiveWorker: failed to propose EventRefundConfirmed for %x: %v", rec.MessageID, err)
		}
		return
	}

	newState, _, err := Next(rec.State, RoleReceiver, event)
	if err != nil {
		log.Printf("ReceiveWorker: Next(EventRefundConfirmed) error: %v", err)
		return
	}
	rec.State = newState
	w.store.Put(rec)
}

func (w *ReceiveWorker) signMarkClaimed(msgID common.Hash, outcome parentchain.FloatOutcome) []byte {
	digest := parentchain.ComputeMarkClaimedMessage(msgID, outcome)
	cert := bls.Sign(w.blsKeyPair.PrivateKey(), digest)
	return cert[:]
}
