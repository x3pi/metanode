package rollup

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

const (
	defaultRegistrationPollInterval = 1 * time.Second
	maxRegistrationBatchSize        = 256
	// registrationShrinkConfirmPolls is how many CONSECUTIVE polls must report fewer parent events than the cursor
	// before the worker treats it as a parent reset. A parent node that is restarting/replaying commits briefly serves
	// a shorter log; it catches up long before this many polls, so it never causes a reset or a false alert. A history
	// mismatch (different user at the fingerprint seq) is proof, not lag, and still resets immediately.
	registrationShrinkConfirmPolls = 30
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
	// attestKey signs attestations. It must be the validator's COMMITTEE key (the account PublicKeyBls registered for
	// the validator address, i.e. Databases.BLSPrivateKey), which differs per validator; the cluster identity key
	// (blsKeyPair) is shared by the whole cluster and cannot tell validators apart. Defaults to blsKeyPair.
	attestKey *bls.KeyPair
	// AlreadyAttested (optional) reports whether this validator's attestation for the event is already recorded on
	// chain; the worker then waits for the other validators instead of re-submitting.
	AlreadyAttested func(user common.Address, parentSeq uint64) bool

	// Fingerprint tracking for event at cursor-1
	lastKnownMu   sync.Mutex
	lastKnownUser common.Address
	lastKnownSeq  uint64
	hasLastKnown  bool

	alertSink func(msg string)

	// shrinkStreak counts consecutive polls whose parent log was shorter than the cursor (only touched by the polling
	// goroutine); shrinkConfirmPolls is the threshold (registrationShrinkConfirmPolls, lowered in tests).
	shrinkStreak       int
	shrinkConfirmPolls int

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
	w := &RegistrationWorker{
		stateDB:       stateDB,
		client:        client,
		blsKeyPair:    blsKeyPair,
		clusterPubKey: clusterPubKey,
		interval:      defaultRegistrationPollInterval,
		wakeCh:        make(chan struct{}, 1),
		quitCh:        make(chan struct{}),
	}
	w.alertSink = func(msg string) { logger.Telegram(msg) }
	w.shrinkConfirmPolls = registrationShrinkConfirmPolls
	return w
}

// SetAlertSink configures the callback sink for operator alerts (Telegram).
func (w *RegistrationWorker) SetAlertSink(sink func(msg string)) {
	w.alertSink = sink
}

// LastKnownFingerprint returns the recorded user and sequence for cursor-1.
func (w *RegistrationWorker) LastKnownFingerprint() (common.Address, uint64, bool) {
	w.lastKnownMu.Lock()
	defer w.lastKnownMu.Unlock()
	return w.lastKnownUser, w.lastKnownSeq, w.hasLastKnown
}

// SetAttestationKey sets the validator committee key used to sign registration attestations.
func (w *RegistrationWorker) SetAttestationKey(kp *bls.KeyPair) { w.attestKey = kp }

func (w *RegistrationWorker) attestationKey() *bls.KeyPair {
	if w.attestKey != nil {
		return w.attestKey
	}
	return w.blsKeyPair
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

func (w *RegistrationWorker) triggerReset(workerCursor, parentNextCursor uint64, reason string) {
	log.Printf("RegistrationWorker: %s (workerCursor=%d, parentCursor=%d); resetting cursor to 0", reason, workerCursor, parentNextCursor)
	w.setCursor(0)
	w.shrinkStreak = 0
	w.lastKnownMu.Lock()
	w.hasLastKnown = false
	w.lastKnownUser = common.Address{}
	w.lastKnownSeq = 0
	w.lastKnownMu.Unlock()

	if w.alertSink != nil {
		msg := fmt.Sprintf("🚨 [Registration Sync] Parent chain sequence anomaly: %s (workerCursor=%d, parentCursor=%d). Local cursor reset to 0 to resync all registrations.", reason, workerCursor, parentNextCursor)
		w.alertSink(msg)
	}
}

func (w *RegistrationWorker) pollAndProcess() bool {
	if w.client == nil {
		return false
	}

	cursor := w.getCursor()
	queryCursor := cursor
	checkFingerprint := false

	if cursor > 0 {
		w.lastKnownMu.Lock()
		if w.hasLastKnown && w.lastKnownSeq == cursor-1 {
			queryCursor = cursor - 1
			checkFingerprint = true
		}
		w.lastKnownMu.Unlock()
	}

	events, nextCursor, err := w.client.GetInboundAccountRegistrations(w.clusterPubKey, queryCursor)
	if err != nil {
		log.Printf("RegistrationWorker: failed to fetch account registrations: %v", err)
		return false
	}

	if len(events) == 0 {
		if nextCursor < cursor {
			// Shorter parent log: a lagging (restarting) parent quorum or a real reset. Only a persistent shortfall
			// over shrinkConfirmPolls consecutive polls is treated as a reset.
			w.shrinkStreak++
			if w.shrinkStreak < w.shrinkConfirmPolls {
				if w.shrinkStreak == 1 {
					log.Printf("RegistrationWorker: parent reports %d registrations, below cursor %d; waiting for %d consecutive polls before treating it as a reset",
						nextCursor, cursor, w.shrinkConfirmPolls)
				}
				return false
			}
			w.triggerReset(cursor, nextCursor, fmt.Sprintf("parent chain sequence rolled back or wiped (seen on %d consecutive polls)", w.shrinkConfirmPolls))
			return true
		}
		w.shrinkStreak = 0
		return false
	}
	w.shrinkStreak = 0

	if checkFingerprint {
		w.lastKnownMu.Lock()
		expectedSeq := w.lastKnownSeq
		expectedUser := w.lastKnownUser
		w.lastKnownMu.Unlock()

		if events[0].Seq != expectedSeq || events[0].UserAddress != expectedUser {
			w.triggerReset(cursor, nextCursor, fmt.Sprintf(
				"parent chain history mismatch at seq %d (expected user %s, got user %s at seq %d)",
				expectedSeq, expectedUser.Hex(), events[0].UserAddress.Hex(), events[0].Seq,
			))
			return true
		}
		// Fingerprint verified: drop the cursor-1 overlap event
		events = events[1:]
		if len(events) == 0 {
			// No new events beyond cursor
			return false
		}
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
			if w.AlreadyAttested != nil && w.AlreadyAttested(ev.UserAddress, ev.Seq) {
				continue
			}
			payload := AccountRegistrationPayload{
				Kind:       SystemPayloadKindAccountRegistered,
				User:       ev.UserAddress,
				ClusterKey: w.clusterPubKey,
				ParentSeq:  ev.Seq,
			}
			if ak := w.attestationKey(); ak != nil {
				digest := ComputeAccountRegistrationAttestDigest(parentchain.ParentChainID, ev.UserAddress, w.clusterPubKey, ev.Seq)
				payload.Attestations = []RegistrationAttestation{{
					ValidatorPubkey: ak.PublicKey(),
					Signature:       bls.Sign(ak.PrivateKey(), digest),
				}}
			}
			data, err := payload.MarshalProto()
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
	var lastConfirmedUser common.Address
	var lastConfirmedSeq uint64
	hasNewConfirmed := false

	for _, ev := range events {
		if w.stateDB != nil && !w.stateDB.GetParentRegistered(ev.UserAddress) {
			// Not yet confirmed in stateDB, halt cursor advance here
			break
		}
		if ev.Seq >= advancedCursor {
			advancedCursor = ev.Seq + 1
			lastConfirmedUser = ev.UserAddress
			lastConfirmedSeq = ev.Seq
			hasNewConfirmed = true
			hasProgress = true
		}
	}

	if advancedCursor > cursor {
		w.setCursor(advancedCursor)
		if hasNewConfirmed {
			w.lastKnownMu.Lock()
			w.lastKnownUser = lastConfirmedUser
			w.lastKnownSeq = lastConfirmedSeq
			w.hasLastKnown = true
			w.lastKnownMu.Unlock()
		}
	}

	return hasProgress
}
