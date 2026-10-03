package rollup

import (
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

	// Gate, when set, is consulted before any new transfer leaves the cluster (see ConservationGuard.Allow).
	Gate func() error

	lastGateLog atomic.Int64 // unix seconds of the last "blocked" log line, to keep the log readable

	wakeCh          chan struct{}
	quitCh          chan struct{}
	wg              sync.WaitGroup
	inFlightSubmits sync.Map
}

func (w *SendWorker) logGate(err error) {
	now := time.Now().Unix()
	if last := w.lastGateLog.Load(); now-last >= 30 && w.lastGateLog.CompareAndSwap(last, now) {
		log.Printf("🛑 SendWorker: not sending new transfers: %v", err)
	}
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
			if w.processPending() {
				ticker.Reset(100 * time.Millisecond)
			} else {
				ticker.Reset(interval)
			}
		case <-w.wakeCh:
			if w.processPending() {
				ticker.Reset(100 * time.Millisecond)
			} else {
				ticker.Reset(interval)
			}
		}
	}
}

func (w *SendWorker) processPending() bool {
	hasProgress := false
	// Gate: while the cluster's BLS float is not verified against its accounts (or a violation is confirmed), nothing
	// new may leave the cluster. Settling inbound transfers (ReceiveWorker/ReclaimWorker) is deliberately not gated.
	if w.Gate != nil {
		if err := w.Gate(); err != nil {
			w.logGate(err)
			return false
		}
	}
	records, err := w.store.ScanNonTerminal()
	if err != nil {
		log.Printf("SendWorker: failed to scan records: %v", err)
		return false
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
			if _, inFlight := w.inFlightSubmits.Load(rec.MessageID); inFlight {
				// Already submitted to parent chain, waiting for local commit of EventRPCSubmitted
				continue
			}

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
				if strings.Contains(err.Error(), "wrong nonce") || strings.Contains(err.Error(), "already resolved") {
					// Verify whether the record actually landed on the parent chain
					if _, recFound, checkErr := w.client.GetTransferRecord(rec.MessageID); checkErr == nil && recFound {
						log.Printf("SendWorker: msgID %x returned '%v' but record exists on parent chain, advancing to check confirmation", rec.MessageID, err)
					} else {
						log.Printf("SendWorker: msgID %x returned 'wrong nonce' (seq %d) and not found on parent chain, pausing to let nonce settle", rec.MessageID, rec.SourceSeq)
						break
					}
				} else {
					log.Printf("SendWorker: failed to send msgID %x: %v", rec.MessageID, err)
					break
				}
			}

			w.inFlightSubmits.Store(rec.MessageID, true)
			hasProgress = true

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
			w.inFlightSubmits.Delete(rec.MessageID)
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
				hasProgress = true
				
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
	return hasProgress
}
