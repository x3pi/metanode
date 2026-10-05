package rollup

import (
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

const (
	defaultRegistrationPollInterval = 1 * time.Second
	maxRegistrationBatchSize        = 256
)

// RegistrationEventProposer proposes an account registration system transaction to consensus.
type RegistrationEventProposer func(payload []byte) error

// RegistrationWorker polls inbound account registrations from the Parent Chain and
// proposes system transactions to the execution cluster.
type RegistrationWorker struct {
	stateDB       AccountStateRegistryDB
	client        parentchain.Client
	blsKeyPair    *bls.KeyPair
	clusterPubKey cm.PublicKey

	cursor        atomic.Uint64
	EventProposer RegistrationEventProposer

	interval time.Duration
	wakeCh   chan struct{}
	quitCh   chan struct{}
	quitOnce sync.Once
	wg       sync.WaitGroup
}

// NewRegistrationWorker creates a new worker for polling parent-chain account registrations.
func NewRegistrationWorker(
	stateDB AccountStateRegistryDB,
	client parentchain.Client,
	blsKeyPair *bls.KeyPair,
	clusterPubKey cm.PublicKey,
) *RegistrationWorker {
	return &RegistrationWorker{
		stateDB:       stateDB,
		client:        client,
		blsKeyPair:    blsKeyPair,
		clusterPubKey: clusterPubKey,
		interval:      defaultRegistrationPollInterval,
		wakeCh:        make(chan struct{}, 1),
		quitCh:        make(chan struct{}),
	}
}

// SetInterval updates the polling interval.
func (w *RegistrationWorker) SetInterval(d time.Duration) {
	if d > 0 {
		w.interval = d
	}
}

// Start runs the polling loop in a background goroutine.
func (w *RegistrationWorker) Start() {
	w.wg.Add(1)
	go w.loop()
}

// Stop gracefully shuts down the worker and waits for the goroutine to terminate.
func (w *RegistrationWorker) Stop() {
	w.quitOnce.Do(func() { close(w.quitCh) })
	w.wg.Wait()
}

// WakeUp signals the worker to perform an immediate poll without waiting for ticker.
func (w *RegistrationWorker) WakeUp() {
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
}

// Cursor returns the current inbound registration cursor.
func (w *RegistrationWorker) Cursor() uint64 {
	return w.cursor.Load()
}

func (w *RegistrationWorker) getCursor() uint64 {
	return w.cursor.Load()
}

func (w *RegistrationWorker) setCursor(cursor uint64) {
	w.cursor.Store(cursor)
}

func (w *RegistrationWorker) loop() {
	defer w.wg.Done()
	interval := w.interval
	if interval <= 0 {
		interval = defaultRegistrationPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.quitCh:
			return
		case <-ticker.C:
			if w.pollAndProcess() {
				ticker.Reset(100 * time.Millisecond)
			} else {
				ticker.Reset(interval)
			}
		case <-w.wakeCh:
			if w.pollAndProcess() {
				ticker.Reset(100 * time.Millisecond)
			} else {
				ticker.Reset(interval)
			}
		}
	}
}

// PollAndProcess performs a single poll-and-process cycle. Exported for tests.
func (w *RegistrationWorker) PollAndProcess() bool {
	return w.pollAndProcess()
}

func (w *RegistrationWorker) pollAndProcess() bool {
	if w.client == nil {
		return false
	}

	cursor := w.getCursor()
	events, _, err := w.client.GetInboundAccountRegistrations(w.clusterPubKey, cursor)
	if err != nil {
		log.Printf("RegistrationWorker: failed to fetch account registrations: %v", err)
		return false
	}

	if len(events) == 0 {
		return false
	}

	// Bounded processing to avoid memory spikes under high load
	if len(events) > maxRegistrationBatchSize {
		events = events[:maxRegistrationBatchSize]
	}

	hasProgress := false

	// Propose un-applied events
	for _, ev := range events {
		if w.stateDB != nil && w.stateDB.GetParentRegistered(ev.UserAddress) {
			// Already registered on-chain
			continue
		}

		if w.EventProposer != nil {
			payload := AccountRegistrationPayload{
				Kind:       SystemPayloadKindAccountRegistered,
				User:       ev.UserAddress,
				ClusterKey: w.clusterPubKey,
				ParentSeq:  ev.Seq,
			}
			data, err := json.Marshal(payload)
			if err != nil {
				log.Printf("RegistrationWorker: failed to marshal payload for user %s: %v", ev.UserAddress.Hex(), err)
				continue
			}

			if err := w.EventProposer(data); err != nil {
				log.Printf("RegistrationWorker: failed to propose account registration for user %s: %v", ev.UserAddress.Hex(), err)
			} else {
				hasProgress = true
			}
		}
	}

	// Advance cursor strictly up to the consecutive sequence of events verified in stateDB
	advancedCursor := cursor
	for _, ev := range events {
		if w.stateDB != nil && !w.stateDB.GetParentRegistered(ev.UserAddress) {
			// Not yet confirmed in stateDB, halt cursor advance here
			break
		}
		if ev.Seq >= advancedCursor {
			advancedCursor = ev.Seq + 1
			hasProgress = true
		}
	}

	if advancedCursor > cursor {
		w.setCursor(advancedCursor)
	}

	return hasProgress
}
