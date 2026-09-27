package rollup

import (
	"log"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
)

type SendWorker struct {
	store       Store
	client      parentchain.Client
	blsKeyPair  *bls.KeyPair
	destPubKey  cm.PublicKey
	destChainID uint64

	wakeCh chan struct{}
	quitCh chan struct{}
	wg     sync.WaitGroup
}

func NewSendWorker(store Store, client parentchain.Client, blsKeyPair *bls.KeyPair, destPubKey cm.PublicKey, destChainID uint64) *SendWorker {
	return &SendWorker{
		store:       store,
		client:      client,
		blsKeyPair:  blsKeyPair,
		destPubKey:  destPubKey,
		destChainID: destChainID,
		wakeCh:      make(chan struct{}, 1),
		quitCh:      make(chan struct{}),
	}
}

func (w *SendWorker) Start() {
	// Sync float sequence on startup to recover after crash
	remoteSeq, err := w.client.GetFloatSeq(w.blsKeyPair.PublicKey())
	if err == nil {
		localSeq, _ := w.store.GetNextFloatSeq()
		if localSeq < remoteSeq {
			log.Printf("SendWorker: syncing FloatSeq forward. Local=%d, Remote=%d", localSeq, remoteSeq)
			_ = w.store.SetFloatSeq(remoteSeq)
		} else if localSeq > remoteSeq {
			log.Printf("ERROR: SendWorker: local FloatSeq (%d) is greater than remote (%d). This is abnormal! Refusing to auto-fix to prevent forks.", localSeq, remoteSeq)
		}
	} else {
		log.Printf("SendWorker: failed to GetFloatSeq on start: %v", err)
	}

	w.wg.Add(1)
	go w.loop()
}

func (w *SendWorker) Stop() {
	close(w.quitCh)
	w.wg.Wait()
}

func (w *SendWorker) WakeUp() {
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
}

func (w *SendWorker) isReleasable(record *MessageRecord) bool {
	if raftfeed.Enabled() {
		if node := raftfeed.GetNode(); node != nil {
			return node.IsLeader()
		}
		return false
	}
	return true
}

func (w *SendWorker) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.quitCh:
			return
		case <-ticker.C:
			w.processPending()
		case <-w.wakeCh:
			w.processPending()
		}
	}
}

func (w *SendWorker) processPending() {
	records, err := w.store.ScanNonTerminal()
	if err != nil {
		log.Printf("SendWorker: failed to scan records: %v", err)
		return
	}

	for _, rec := range records {
		if !w.isReleasable(rec) {
			continue
		}

		if rec.State == StateLocalAppliedPendingSend {
			// Prepare message digest to sign
			digest := parentchain.ComputeTransferFloatMessage(
				w.blsKeyPair.PublicKey(),
				w.destPubKey,
				rec.Sender,
				rec.Target,
				rec.Value,
				rec.PayloadHash,
				rec.SourceSeq, // nonce
			)

			cert := bls.Sign(w.blsKeyPair.PrivateKey(), digest)

			// Send via client
			isRefund := false
			_, err := w.client.SendTransferFloat(
				w.blsKeyPair.PublicKey(),
				w.destPubKey,
				w.destChainID,
				rec.Sender,
				rec.Target,
				rec.Value,
				nil, // gasFee
				rec.SourceSeq,
				cert[:],
				isRefund,
			)

			if err != nil {
				log.Printf("SendWorker: failed to send msgID %x: %v", rec.MessageID, err)
				continue
			}

			// Advance state to submitted
			event := Event{
				Type: EventRPCSubmitted,
				Role: RoleSender,
			}
			newState, _, err := Next(rec.State, RoleSender, event)
			if err == nil {
				rec.State = newState
				w.store.Put(rec)
			}
		} else if rec.State == StateSendSubmitted {
			// Poll ParentChain to check if it has actually been confirmed
			_, found, err := w.client.GetTransferRecord(rec.MessageID)
			if err != nil {
				log.Printf("SendWorker: failed to poll msgID %x: %v", rec.MessageID, err)
				continue
			}
			if found {
				// Actually confirmed by consensus
				event := Event{
					Type: EventParentConfirmed,
					Role: RoleSender,
				}
				newState, _, err := Next(rec.State, RoleSender, event)
				if err == nil {
					rec.State = newState
					w.store.Put(rec)
				}
			}
		}
	}
}
