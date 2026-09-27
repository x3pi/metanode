package rollup

import (
	"log"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
)

type ReclaimWorker struct {
	store      Store
	stateDB    AccountStateDB
	client     parentchain.Client
	blsKeyPair *bls.KeyPair

	wakeCh chan struct{}
	quitCh chan struct{}
	wg     sync.WaitGroup
}

func NewReclaimWorker(store Store, stateDB AccountStateDB, client parentchain.Client, blsKeyPair *bls.KeyPair) *ReclaimWorker {
	return &ReclaimWorker{
		store:      store,
		stateDB:    stateDB,
		client:     client,
		blsKeyPair: blsKeyPair,
		wakeCh:     make(chan struct{}, 1),
		quitCh:     make(chan struct{}),
	}
}

func (w *ReclaimWorker) Start() {
	w.wg.Add(1)
	go w.loop()
}

func (w *ReclaimWorker) Stop() {
	close(w.quitCh)
	w.wg.Wait()
}

func (w *ReclaimWorker) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.quitCh:
			return
		case <-ticker.C:
			w.processReclaims()
		case <-w.wakeCh:
			w.processReclaims()
		}
	}
}

func (w *ReclaimWorker) isReleasable(record *MessageRecord) bool {
	if raftfeed.Enabled() {
		if node := raftfeed.GetNode(); node != nil {
			return node.IsLeader()
		}
		return false
	}
	return true
}

func (w *ReclaimWorker) processReclaims() {
	records, err := w.store.ScanNonTerminal()
	if err != nil {
		log.Printf("ReclaimWorker: scan failed: %v", err)
		return
	}

	for _, rec := range records {
		if rec.Role != RoleSender {
			continue
		}
		if !w.isReleasable(rec) {
			continue
		}

		if rec.State == StateSentConfirmed {
			w.checkAndReclaim(rec)
		} else if rec.State == StateReclaimSubmitted {
			w.checkReclaimOutcome(rec)
		} else if rec.State == StateRefundInTransit {
			w.processRefund(rec)
		}
	}
}

func (w *ReclaimWorker) checkAndReclaim(rec *MessageRecord) {
	// Query parentchain to check if time > timeout
	transferRec, found, err := w.client.GetTransferRecord(rec.MessageID)
	if err != nil || !found {
		return
	}
	
	currentTime := uint64(time.Now().Unix())
	isEligible := currentTime > transferRec.ConfirmedAtBlockTime + 60
	
	if isEligible {
		event := Event{
			Type:              EventReclaimEligible,
			Role:              RoleSender,
			Value:             rec.Value,
			ParentBlockTime:   currentTime,
			ParentConfirmTime: transferRec.ConfirmedAtBlockTime,
			Timeout:           60,
		}
		newState, _, err := Next(rec.State, RoleSender, event)
		if err == nil {
			rec.State = newState
			w.store.Put(rec)
			w.submitReclaim(rec)
		}
	}
}

func (w *ReclaimWorker) submitReclaim(rec *MessageRecord) {
	cert := w.signReclaim(rec.MessageID)
	_, err := w.client.SendReclaimFloat(rec.MessageID, cert)
	if err != nil {
		log.Printf("Failed to send reclaim: %v", err)
		return
	}
	// Once submitted successfully, we just wait for outcome (checkReclaimOutcome)
}

func (w *ReclaimWorker) checkReclaimOutcome(rec *MessageRecord) {
	// Poll parent chain for outcome (Won or Lost).
	_, found, err := w.client.GetTransferRecord(rec.MessageID)
	if err != nil || !found {
		return
	}

	outcome, err := w.client.GetClaimed(rec.MessageID)
	if err != nil {
		return
	}

	var event Event
	if outcome == parentchain.FloatOutcomeReclaimed {
		event = Event{
			Type:   EventReclaimWon,
			Role:   RoleSender,
			Sender: rec.Sender,
			Value:  rec.Value,
		}
	} else if outcome == parentchain.FloatOutcomeCredited || outcome == parentchain.FloatOutcomeRefund {
		var mappedOutcome Outcome
		if outcome == parentchain.FloatOutcomeCredited {
			mappedOutcome = OutcomeCredited
		} else {
			mappedOutcome = OutcomeRefund
		}
		event = Event{
			Type:    EventClaimedObserved,
			Role:    RoleSender,
			Outcome: mappedOutcome,
			Value:   rec.Value,
		}
	} else {
		// Still pending or FloatOutcomeNone
		return
	}

	newState, actions, err := Next(rec.State, RoleSender, event)
	if err == nil {
		w.applyActions(actions)
		rec.State = newState
		w.store.Put(rec)
	}
}

func (w *ReclaimWorker) processRefund(rec *MessageRecord) {
	// Refund has already been processed by ReceiveWorker via a separate TransferFloat.
	// We just need to close this original record to StateConfirmedRefunded.
	event := Event{
		Type:   EventRefundObserved,
		Role:   RoleSender,
		Sender: rec.Sender,
		Value:  rec.Value,
	}
	newState, actions, err := Next(rec.State, RoleSender, event)
	if err == nil {
		w.applyActions(actions)
		rec.State = newState
		w.store.Put(rec)
	}
}

func (w *ReclaimWorker) signReclaim(msgID common.Hash) []byte {
	digest := parentchain.ComputeReclaimFloatMessage(msgID, w.blsKeyPair.PublicKey())
	cert := bls.Sign(w.blsKeyPair.PrivateKey(), digest)
	return cert[:]
}

func (w *ReclaimWorker) applyActions(actions []Action) {
	for _, action := range actions {
		if action.Type == ActionCreditLocal {
			// SubBalance negative amount = add balance
			// (Assuming SubBalance handles big ints properly)
			w.stateDB.SubBalance(action.Target, new(big.Int).Neg(action.Amount))
		}
	}
}
