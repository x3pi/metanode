package rollup

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
)

type SendWorker struct {
	store         Store
	client        parentchain.Client
	blsKeyPair    *bls.KeyPair
	destPubKey    cm.PublicKey
	destClusterID uint64
	interval      time.Duration

	// EventProposer is used to submit state machine events to the Raft consensus.
	EventProposer func(event Event, msgID common.Hash, sourceSeq uint64, sourcePubKey cm.PublicKey, destPubKey cm.PublicKey, payloadHash common.Hash) error

	wakeCh chan struct{}
	quitCh chan struct{}
	wg     sync.WaitGroup
}

func NewSendWorker(store Store, client parentchain.Client, blsKeyPair *bls.KeyPair, destPubKey cm.PublicKey, destClusterID uint64) *SendWorker {
	return &SendWorker{
		store:         store,
		client:        client,
		blsKeyPair:    blsKeyPair,
		destPubKey:    destPubKey,
		destClusterID: destClusterID,
		interval:      1 * time.Second,
		wakeCh:        make(chan struct{}, 1),
		quitCh:        make(chan struct{}),
	}
}

func (w *SendWorker) SetInterval(d time.Duration) {
	if d > 0 {
		w.interval = d
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
	interval := w.interval
	if interval <= 0 {
		interval = 1 * time.Second
	}
	ticker := time.NewTicker(interval)
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

	// Sort records strictly by SourceSeq so sequential nonce transfers are dispatched in order
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].SourceSeq < records[j].SourceSeq
	})

	for _, rec := range records {
		if !w.isReleasable(rec) {
			continue
		}

		if rec.State == StateLocalAppliedPendingSend {
			// [ROUTING] Query Account Registry for target address
			destPubKey, found, err := w.client.GetAccountRegistry(rec.Target)
			if err != nil {
				log.Printf("SendWorker: failed to lookup account %s: %v", rec.Target.Hex(), err)
				continue
			}
			if !found {
				log.Printf("SendWorker: account %s not found in registry, cannot route", rec.Target.Hex())
				continue
			}

			// Prepare message digest to sign
			digest := parentchain.ComputeTransferFloatMessage(
				w.blsKeyPair.PublicKey(),
				destPubKey,
				rec.Sender,
				rec.Target,
				rec.Value,
				rec.GasFee,
				rec.PayloadHash,
				rec.SourceSeq, // nonce
			)

			cert := bls.Sign(w.blsKeyPair.PrivateKey(), digest)

			// Send via client
			isRefund := false
			_, err = w.client.SendTransferFloat(
				w.blsKeyPair.PublicKey(),
				destPubKey,
				w.destClusterID, // cluster routing field, not used in digest
				rec.Sender,
				rec.Target,
				rec.Value,
				rec.GasFee, // gasFee
				rec.SourceSeq,
				cert[:],
				isRefund,
			)

			if err != nil {
				if strings.Contains(err.Error(), "wrong nonce") {
					// Verify whether the record actually landed on the parent chain
					if _, recFound, checkErr := w.client.GetTransferRecord(rec.MessageID); checkErr == nil && recFound {
						log.Printf("SendWorker: msgID %x returned 'wrong nonce' but record exists on parent chain, advancing to check confirmation", rec.MessageID)
					} else {
						log.Printf("SendWorker: msgID %x returned 'wrong nonce' (seq %d) and not found on parent chain, pausing to let nonce settle", rec.MessageID, rec.SourceSeq)
						break
					}
				} else {
					log.Printf("SendWorker: failed to send msgID %x: %v", rec.MessageID, err)
					break
				}
			}

			// Advance state to submitted
			event := Event{
				Type: EventRPCSubmitted,
				Role: RoleSender,
			}
			
			if w.EventProposer != nil {
				if err := w.EventProposer(event, rec.MessageID, rec.SourceSeq, rec.SourcePubKey, rec.DestPubKey, rec.PayloadHash); err != nil {
					log.Printf("SendWorker: failed to propose EventRPCSubmitted for %x: %v", rec.MessageID, err)
				}
				continue
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
				
				if w.EventProposer != nil {
					if err := w.EventProposer(event, rec.MessageID, rec.SourceSeq, rec.SourcePubKey, rec.DestPubKey, rec.PayloadHash); err != nil {
						log.Printf("SendWorker: failed to propose EventParentConfirmed for %x: %v", rec.MessageID, err)
					}
					continue
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
