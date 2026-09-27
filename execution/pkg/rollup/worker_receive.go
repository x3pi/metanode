package rollup

import (
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
)

var RollupCursorKey = crypto.Keccak256Hash([]byte("RollupCursorKey"))

type ReceiveWorker struct {
	store       Store
	stateDB     AccountStateDB
	client      parentchain.Client
	blsKeyPair  *bls.KeyPair

	Validator func(common.Address) bool

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
	dbStore, ok := w.store.(*DBStore)
	if !ok {
		return 0
	}
	data, found := dbStore.scDB.StorageValue(RollupSystemAddress, RollupCursorKey)
	if !found || len(data) < 8 {
		return 0
	}
	// deserialize uint64 (big endian)
	return common.BytesToHash(data).Big().Uint64()
}

func (w *ReceiveWorker) setCursor(cursor uint64) {
	dbStore, ok := w.store.(*DBStore)
	if !ok {
		return
	}
	val := common.BigToHash(new(big.Int).SetUint64(cursor)).Bytes()
	dbStore.scDB.SetStorageValue(RollupSystemAddress, RollupCursorKey, val)
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
		newState, actions, err := Next(rec.State, RoleReceiver, event)
		if err != nil {
			log.Printf("ReceiveWorker: Next(EventClaimedConfirmed) error: %v", err)
			return
		}
		
		// Apply Actions (CreditLocal or SendRefund)
		for _, action := range actions {
			if action.Type == ActionCreditLocal {
				w.stateDB.SubBalance(action.Target, new(big.Int).Neg(action.Amount))
			} else if action.Type == ActionSendRefund {
				// Handle SendRefund (similar to what was in processMarkClaimedPendingRefund)
				seq, err := w.store.GetNextFloatSeq()
				if err != nil {
					log.Printf("ReceiveWorker: failed to get next float seq: %v", err)
					return
				}
				
				isRefund := true
				refundCert := w.signTransfer(rec.SourcePubKey, rec.Target, rec.Sender, rec.Value, seq)
				
				_, sendErr := w.client.SendTransferFloat(
					w.blsKeyPair.PublicKey(),
					rec.SourcePubKey, // destination is the source of the original transfer
					0, // destChainID
					rec.Target,
					rec.Sender,
					action.Amount,
					nil,
					seq,
					refundCert,
					isRefund,
				)
				if sendErr != nil {
					log.Printf("ReceiveWorker: failed to send refund transfer: %v", sendErr)
					return // Will retry
				}
				
				_ = w.store.IncrementFloatSeq()
			}
		}
		
		rec.State = newState
		w.store.Put(rec)
	}
}

func (w *ReceiveWorker) signMarkClaimed(msgID common.Hash, outcome parentchain.FloatOutcome) []byte {
	digest := parentchain.ComputeMarkClaimedMessage(msgID, outcome)
	cert := bls.Sign(w.blsKeyPair.PrivateKey(), digest)
	return cert[:]
}

func (w *ReceiveWorker) signTransfer(destPubKey cm.PublicKey, sender, target common.Address, value *big.Int, nonce uint64) []byte {
	digest := parentchain.ComputeTransferFloatMessage(
		w.blsKeyPair.PublicKey(), destPubKey, sender, target, value, crypto.Keccak256Hash(nil), nonce,
	)
	cert := bls.Sign(w.blsKeyPair.PrivateKey(), digest)
	return cert[:]
}
