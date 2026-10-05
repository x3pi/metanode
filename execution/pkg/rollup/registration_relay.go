package rollup

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

// RegistrationStatus is what a user sees while the execution node and the Parent Chain turn a registration request into
// a real (parent-registered) account. The user only ever talks to the execution node.
type RegistrationStatus string

const (
	RegStatusNone      RegistrationStatus = "NONE"      // no request known for this address
	RegStatusPending   RegistrationStatus = "PENDING"   // accepted; being relayed to / confirmed by the Parent Chain
	RegStatusConfirmed RegistrationStatus = "CONFIRMED" // Parent Chain registered this address to THIS cluster (flag set on-chain here)
	RegStatusRejected  RegistrationStatus = "REJECTED"  // Parent Chain registered this address to ANOTHER cluster first
	RegStatusFailed    RegistrationStatus = "FAILED"    // could not be relayed / confirmed; the user may resubmit
)

var (
	// ErrRegistrationBusy is returned when the bounded queue or the tracked-request table is full.
	ErrRegistrationBusy = errors.New("registration relay is busy, retry later")
	// ErrInvalidRegistrationSig is returned when userSig does not recover to the requested address.
	ErrInvalidRegistrationSig = errors.New("registration signature does not match the address")
)

const (
	defaultRelayQueueSize   = 1024
	defaultRelayMaxTracked  = 4096
	defaultRelayMaxAttempts = 20 // relay attempts (send / registry lookup) before a request is marked FAILED
	defaultRelayMaxVerifies = 600
	defaultRelayInterval    = 1 * time.Second
	relayBatchPerTick       = 64
	registrationSigLength   = 65
	registrationSigVIndex   = 64
	ethRecoveryIDOffset     = 27 // Ethereum signers add 27 to the recovery id
)

// RegistrationParent is the part of the Parent Chain client the relay needs.
type RegistrationParent interface {
	SendRegisterAccount(userAddress common.Address, floatIdentityKey cm.PublicKey, userSig []byte, clusterSig cm.Sign) (common.Hash, error)
	GetAccountRegistry(userAddress common.Address) (cm.PublicKey, bool, error)
}

// RegistrationResult is a status plus optional detail (the winning cluster for REJECTED, the reason for FAILED).
type RegistrationResult struct {
	Status      RegistrationStatus
	HomeCluster *cm.PublicKey
	Reason      string
}

type relayEntry struct {
	user      common.Address
	userSig   []byte
	status    RegistrationStatus
	submitted bool // SendRegisterAccount accepted by the parent (the parent executes it later)
	attempts  int  // failed relay steps so far
	verifies  int  // registry lookups since submitted without an answer
	home      *cm.PublicKey
	reason    string
}

// RegistrationRelay accepts account registration requests from users, relays them to the Parent Chain automatically and
// tracks the outcome. It is deliberately NOT part of consensus: it only produces the cluster-signed Parent Chain
// transaction and a user-facing status. Whether an account may send transactions is decided solely by the on-chain
// ParentRegistered flag, which the RegistrationWorker applies from the parent's ordered registration events. All limits
// are counts (queue length, tracked requests, attempts) — no wall-clock decides anything.
type RegistrationRelay struct {
	parent     RegistrationParent
	clusterKey cm.PublicKey
	sign       func(digest []byte) cm.Sign
	registered func(common.Address) bool // reads the on-chain flag

	maxTracked  int
	maxAttempts int
	maxVerifies int
	interval    time.Duration

	mu      sync.Mutex
	entries map[common.Address]*relayEntry
	queue   chan common.Address

	wakeCh chan struct{}
	quitCh chan struct{}
	wg     sync.WaitGroup
	once   sync.Once
}

// NewRegistrationRelay builds a relay. sign must sign the registration digest with the cluster BLS key; registered reads
// the on-chain ParentRegistered flag of an address.
func NewRegistrationRelay(parent RegistrationParent, clusterKey cm.PublicKey, sign func(digest []byte) cm.Sign, registered func(common.Address) bool) *RegistrationRelay {
	return &RegistrationRelay{
		parent:      parent,
		clusterKey:  clusterKey,
		sign:        sign,
		registered:  registered,
		maxTracked:  defaultRelayMaxTracked,
		maxAttempts: defaultRelayMaxAttempts,
		maxVerifies: defaultRelayMaxVerifies,
		interval:    defaultRelayInterval,
		entries:     make(map[common.Address]*relayEntry),
		queue:       make(chan common.Address, defaultRelayQueueSize),
		wakeCh:      make(chan struct{}, 1),
		quitCh:      make(chan struct{}),
	}
}

// ClusterKey returns the cluster identity key users must sign their registration for.
func (r *RegistrationRelay) ClusterKey() cm.PublicKey { return r.clusterKey }

// RegistrationDigest is the message the Parent Chain verifies (and the cluster signs): the user signs
// keccak256(RegistrationDigest(user)) with their secp256k1 key.
func (r *RegistrationRelay) RegistrationDigest(user common.Address) []byte {
	return parentchain.ComputeRegisterAccountMessage(user, r.clusterKey)
}

// verifyUserSig mirrors the Parent Chain's own check (parentchain.RegisterAccount): ecrecover over
// keccak256(digest) must give the address. Rejecting bad signatures here keeps garbage out of the queue and off the
// Parent Chain.
func (r *RegistrationRelay) verifyUserSig(user common.Address, sig []byte) bool {
	if len(sig) != registrationSigLength {
		return false
	}
	s := make([]byte, len(sig))
	copy(s, sig)
	if s[registrationSigVIndex] >= ethRecoveryIDOffset {
		s[registrationSigVIndex] -= ethRecoveryIDOffset // accept the Ethereum 27/28 form too
	}
	digest := r.RegistrationDigest(user)
	hash := crypto.Keccak256Hash(digest)
	if pub, err := crypto.SigToPub(hash.Bytes(), s); err == nil && crypto.PubkeyToAddress(*pub) == user {
		return true
	}
	// Also accept Ethereum personal_sign text prefix
	ethHash := accounts.TextHash(digest)
	if pub2, err2 := crypto.SigToPub(ethHash, s); err2 == nil && crypto.PubkeyToAddress(*pub2) == user {
		return true
	}
	return false
}

// Submit accepts a registration request. It is idempotent: an address that is already confirmed, pending or rejected
// returns that state without enqueueing again; a FAILED request is reset and re-queued.
func (r *RegistrationRelay) Submit(user common.Address, userSig []byte) (RegistrationResult, error) {
	if user == (common.Address{}) {
		return RegistrationResult{}, errors.New("empty user address")
	}
	if r.registered != nil && r.registered(user) {
		return RegistrationResult{Status: RegStatusConfirmed}, nil
	}
	if !r.verifyUserSig(user, userSig) {
		return RegistrationResult{}, ErrInvalidRegistrationSig
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.entries[user]; ok {
		switch e.status {
		case RegStatusPending, RegStatusRejected, RegStatusConfirmed:
			return r.resultLocked(e), nil
		}
		// FAILED: allow a fresh attempt below
	} else if len(r.entries) >= r.maxTracked {
		return RegistrationResult{}, ErrRegistrationBusy
	}

	e := &relayEntry{user: user, userSig: append([]byte(nil), userSig...), status: RegStatusPending}
	select {
	case r.queue <- user:
	default:
		return RegistrationResult{}, ErrRegistrationBusy
	}
	r.entries[user] = e
	r.wake()
	return r.resultLocked(e), nil
}

// Status reports the current state of an address. The on-chain flag always wins.
func (r *RegistrationRelay) Status(user common.Address) RegistrationResult {
	if r.registered != nil && r.registered(user) {
		return RegistrationResult{Status: RegStatusConfirmed}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[user]; ok {
		return r.resultLocked(e)
	}
	return RegistrationResult{Status: RegStatusNone}
}

func (r *RegistrationRelay) resultLocked(e *relayEntry) RegistrationResult {
	res := RegistrationResult{Status: e.status, Reason: e.reason}
	if e.home != nil {
		h := *e.home
		res.HomeCluster = &h
	}
	return res
}

func (r *RegistrationRelay) wake() {
	select {
	case r.wakeCh <- struct{}{}:
	default:
	}
}

// Start runs the relay loop in the background.
func (r *RegistrationRelay) Start() {
	r.wg.Add(1)
	go r.loop()
}

// Stop shuts the relay down; safe to call more than once.
func (r *RegistrationRelay) Stop() {
	r.once.Do(func() { close(r.quitCh) })
	r.wg.Wait()
}

func (r *RegistrationRelay) loop() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.quitCh:
			return
		case <-ticker.C:
			r.ProcessBatch()
		case <-r.wakeCh:
			r.ProcessBatch()
		}
	}
}

// ProcessBatch handles up to relayBatchPerTick queued requests and re-checks submitted ones. Exported for tests.
func (r *RegistrationRelay) ProcessBatch() {
	if r.parent == nil {
		return
	}
	// 1. queued requests: only the ones present at the START of this tick, at most relayBatchPerTick. A request that
	// fails is re-queued for the NEXT tick, so one tick is exactly one attempt (no hot retry loop).
	n := len(r.queue)
	if n > relayBatchPerTick {
		n = relayBatchPerTick
	}
	for i := 0; i < n; i++ {
		select {
		case user := <-r.queue:
			r.relayOne(user)
		default:
			i = n // queue drained by someone else
		}
	}
	// 2. submitted requests still waiting for the parent's answer
	for _, user := range r.submittedUsers(relayBatchPerTick) {
		r.verifyOne(user)
	}
}

func (r *RegistrationRelay) submittedUsers(limit int) []common.Address {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []common.Address
	for u, e := range r.entries {
		if e.status == RegStatusPending && e.submitted {
			out = append(out, u)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

func (r *RegistrationRelay) snapshot(user common.Address) (relayEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[user]
	if !ok {
		return relayEntry{}, false
	}
	return *e, true
}

func (r *RegistrationRelay) update(user common.Address, fn func(e *relayEntry)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[user]; ok {
		fn(e)
	}
}

// retryOrFail puts a request back for a later tick, or marks it FAILED once its attempts are exhausted.
func (r *RegistrationRelay) retryOrFail(user common.Address, reason string) {
	failed := false
	r.update(user, func(e *relayEntry) {
		e.attempts++
		e.reason = reason
		if e.attempts >= r.maxAttempts {
			e.status = RegStatusFailed
			failed = true
		}
	})
	if failed {
		log.Printf("RegistrationRelay: giving up on %s: %s", user.Hex(), reason)
		return
	}
	select {
	case r.queue <- user:
	default:
		r.update(user, func(e *relayEntry) { e.status = RegStatusFailed; e.reason = "queue full while retrying" })
	}
}

// relayOne asks the parent who owns the address, and if nobody does, sends the cluster-signed registration.
func (r *RegistrationRelay) relayOne(user common.Address) {
	e, ok := r.snapshot(user)
	if !ok || e.status != RegStatusPending {
		return
	}
	if r.registered != nil && r.registered(user) {
		r.update(user, func(e *relayEntry) { e.status = RegStatusConfirmed })
		return
	}

	home, found, err := r.parent.GetAccountRegistry(user)
	if err != nil {
		r.retryOrFail(user, fmt.Sprintf("parent registry lookup failed: %v", err))
		return
	}
	if found {
		r.resolveFound(user, home)
		return
	}

	digest := r.RegistrationDigest(user)
	if _, err := r.parent.SendRegisterAccount(user, r.clusterKey, e.userSig, r.sign(digest)); err != nil {
		r.retryOrFail(user, fmt.Sprintf("parent registration submit failed: %v", err))
		return
	}
	r.update(user, func(e *relayEntry) { e.submitted = true; e.verifies = 0; e.reason = "" })
}

// verifyOne polls the parent registry for a submitted request: the parent executes the registration after accepting the
// transaction, and a duplicate (the address already belongs to another cluster) only shows up in its registry.
func (r *RegistrationRelay) verifyOne(user common.Address) {
	e, ok := r.snapshot(user)
	if !ok || e.status != RegStatusPending || !e.submitted {
		return
	}
	if r.registered != nil && r.registered(user) {
		r.update(user, func(e *relayEntry) { e.status = RegStatusConfirmed })
		return
	}
	home, found, err := r.parent.GetAccountRegistry(user)
	if err == nil && found {
		r.resolveFound(user, home)
		return
	}
	r.update(user, func(e *relayEntry) {
		e.verifies++
		if e.verifies >= r.maxVerifies {
			e.status = RegStatusFailed
			e.reason = "the parent chain did not register the account"
		}
	})
}

// resolveFound applies the parent's answer: ours ⇒ stay PENDING until the on-chain flag is applied by the
// RegistrationWorker (Status then reports CONFIRMED); another cluster's ⇒ REJECTED.
func (r *RegistrationRelay) resolveFound(user common.Address, home cm.PublicKey) {
	r.update(user, func(e *relayEntry) {
		if home == r.clusterKey {
			// Parent chose THIS cluster: wait for the ordered registration event to set the on-chain flag (Status
			// reports CONFIRMED the moment it is set). Stop polling the parent after a bounded number of lookups.
			e.submitted = e.verifies < r.maxVerifies
			e.verifies++
			e.reason = ""
			return
		}
		h := home
		e.status = RegStatusRejected
		e.home = &h
		e.reason = "address is registered to another cluster on the parent chain"
	})
}
