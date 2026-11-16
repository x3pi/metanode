package rollup

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

// RegistrationStatus is what a user sees while the execution node and the Parent Chain turn a registration request into
// a real (parent-registered) account. The user only ever talks to the execution node.
type RegistrationStatus string

const (
	RegStatusNone        RegistrationStatus = "NONE"        // no request known for this address
	RegStatusPending     RegistrationStatus = "PENDING"     // accepted; being relayed to / confirmed by the Parent Chain
	RegStatusProvisional RegistrationStatus = "PROVISIONAL" // provisionally active on this cluster, pending Parent Chain confirmation
	RegStatusConfirmed   RegistrationStatus = "CONFIRMED"   // Parent Chain registered this address to THIS cluster (flag set on-chain here)
	RegStatusRejected    RegistrationStatus = "REJECTED"    // Parent Chain registered this address to ANOTHER cluster first
	RegStatusFailed      RegistrationStatus = "FAILED"      // could not be relayed / confirmed; the user may resubmit
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
	// Operator alerts are aggregated into at most one Telegram message per interval (never per tick/account).
	defaultRelayAlertInterval = 1 * time.Minute
	relayAlertSamplesPerKind  = 3
	// relayProvisionalRetryTicks is how many relay attempts a request waits after proposing provisional activation
	// before proposing it again (only happens if the first proposal did not apply).
	relayProvisionalRetryTicks = 30
	relayReportedTxCap         = 1024
)

// RegistrationParent is the part of the Parent Chain client the relay needs.
type RegistrationParent interface {
	SendRegisterAccount(userAddress common.Address, floatIdentityKey cm.PublicKey, userSig []byte, clusterSig cm.Sign) (common.Hash, error)
	GetAccountRegistry(userAddress common.Address) (cm.PublicKey, bool, error)
	GetReceipt(txHash common.Hash) (*parentchain.Receipt, bool, error)
	ReportTxFailure(txHash common.Hash)
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
	createdAt time.Time
	txHash    common.Hash
	alerted   bool // a sync-error alert was recorded for the current trouble episode (in-memory only)
	queued    bool // the address is currently in r.queue (prevents double enqueue by the stall sweep)
	provWait  int  // relay attempts to skip before proposing provisional activation again
	batched   bool // txHash is a batch registration tx
	noBatch   bool // the parent rejected a batch carrying this request: send it alone from now on
}

// alertBucket aggregates operator alerts of one kind between two Telegram flushes.
type alertBucket struct {
	count   int
	samples []string
}

// KVStore is a minimal key-value storage interface compatible with storage.Storage and leveldb/pebbledb/memory stores.
type KVStore interface {
	Get(key []byte) ([]byte, error)
	Put(key []byte, value []byte) error
	Delete(key []byte) error
	PrefixScan(prefix []byte) ([][2][]byte, error)
}

// RelayRecord is the persisted state of an account registration request.
type RelayRecord struct {
	User      common.Address     `json:"user"`
	UserSig   []byte             `json:"userSig"`
	Status    RegistrationStatus `json:"status"`
	Submitted bool               `json:"submitted"`
	Attempts  int                `json:"attempts"`
	Verifies  int                `json:"verifies"`
	Home      *cm.PublicKey      `json:"home,omitempty"`
	Reason    string             `json:"reason,omitempty"`
	CreatedAt int64              `json:"createdAt,omitempty"`
	TxHash    common.Hash        `json:"txHash,omitempty"`
}

// RegistrationRelayStore defines persistent operations for registration entries.
type RegistrationRelayStore interface {
	Get(user common.Address) (*RelayRecord, bool, error)
	Put(record *RelayRecord) error
	Delete(user common.Address) error
	ScanAll() ([]*RelayRecord, error)
}

const relayStoreKeyPrefix = "reg_relay:"

type kvRelayStore struct {
	kv KVStore
}

// NewKVRelayStore wraps a KVStore (such as storage.Storage) to persist registration relay records.
func NewKVRelayStore(kv KVStore) RegistrationRelayStore {
	return &kvRelayStore{kv: kv}
}

func (s *kvRelayStore) makeKey(user common.Address) []byte {
	return append([]byte(relayStoreKeyPrefix), user.Bytes()...)
}

func (s *kvRelayStore) Get(user common.Address) (*RelayRecord, bool, error) {
	if s.kv == nil {
		return nil, false, nil
	}
	data, err := s.kv.Get(s.makeKey(user))
	if err != nil || len(data) == 0 {
		return nil, false, err
	}
	var rec RelayRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, false, err
	}
	return &rec, true, nil
}

func (s *kvRelayStore) Put(record *RelayRecord) error {
	if s.kv == nil || record == nil {
		return nil
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.kv.Put(s.makeKey(record.User), data)
}

func (s *kvRelayStore) Delete(user common.Address) error {
	if s.kv == nil {
		return nil
	}
	return s.kv.Delete(s.makeKey(user))
}

func (s *kvRelayStore) ScanAll() ([]*RelayRecord, error) {
	if s.kv == nil {
		return nil, nil
	}
	pairs, err := s.kv.PrefixScan([]byte(relayStoreKeyPrefix))
	if err != nil {
		return nil, err
	}
	var records []*RelayRecord
	for _, pair := range pairs {
		var rec RelayRecord
		if err := json.Unmarshal(pair[1], &rec); err == nil {
			records = append(records, &rec)
		}
	}
	return records, nil
}

// MemoryKVStore is an in-memory implementation of KVStore for testing and isolated usage.
type MemoryKVStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func NewMemoryKVStore() *MemoryKVStore {
	return &MemoryKVStore{data: make(map[string][]byte)}
}

func (m *MemoryKVStore) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[string(key)]
	if !ok {
		return nil, nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (m *MemoryKVStore) Put(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := make([]byte, len(value))
	copy(v, value)
	m.data[string(key)] = v
	return nil
}

func (m *MemoryKVStore) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, string(key))
	return nil
}

func (m *MemoryKVStore) PrefixScan(prefix []byte) ([][2][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	prefStr := string(prefix)
	var out [][2][]byte
	for k, v := range m.data {
		if strings.HasPrefix(k, prefStr) {
			valCopy := make([]byte, len(v))
			copy(valCopy, v)
			out = append(out, [2][]byte{[]byte(k), valCopy})
		}
	}
	return out, nil
}

// RegistrationRelay accepts account registration requests from users, relays them to the Parent Chain automatically and
// tracks the outcome. It is deliberately NOT part of consensus: it only produces the cluster-signed Parent Chain
// transaction and a user-facing status. Whether an account may send transactions is decided solely by the on-chain
// ParentRegistered flag, which the RegistrationWorker applies from the parent's ordered registration events. All limits
// are counts (queue length, tracked requests, attempts) — no wall-clock decides anything.
type RegistrationRelay struct {
	parent        RegistrationParent
	clusterKey    cm.PublicKey
	sign          func(digest []byte) cm.Sign
	registered    func(common.Address) bool // reads the on-chain ParentRegistered flag
	isProvisional func(common.Address) bool // reads the on-chain Provisional flag
	isLocked      func(common.Address) bool // reads the on-chain Locked flag
	lockedHome    func(common.Address) cm.PublicKey

	provisionalProposer func(user common.Address, userSig []byte) error
	lockProposer        func(user common.Address, homeCluster cm.PublicKey) error

	maxTracked  int
	maxAttempts int
	maxVerifies int
	interval    time.Duration

	store RegistrationRelayStore

	mu      sync.Mutex
	entries map[common.Address]*relayEntry
	queue   chan common.Address

	wakeCh chan struct{}
	quitCh chan struct{}
	wg     sync.WaitGroup
	once   sync.Once

	onStatusChanged func(user common.Address, res RegistrationResult)

	// storeHasMore is set when persisted requests did not fit in memory (maxTracked); the stall sweep loads them
	// once room frees up, so no persisted request is forgotten after a crash/restart.
	storeHasMore bool

	batchSize  int                      // > 1: registrations are sent in batch transactions (SetBatchSize)
	reportedTx map[common.Hash]struct{} // failed tx hashes already reported to the parent client

	alertSink      func(msg string) // operator alerts (Telegram); replaceable in tests
	alertInterval  time.Duration
	alertMu        sync.Mutex
	alertBuckets   map[string]*alertBucket
	alertOrder     []string
	lastAlertFlush time.Time
}

// NewRegistrationRelay builds a relay. sign must sign the registration digest with the cluster BLS key; registered reads
// the on-chain ParentRegistered flag of an address.
func NewRegistrationRelay(parent RegistrationParent, clusterKey cm.PublicKey, sign func(digest []byte) cm.Sign, registered func(common.Address) bool) *RegistrationRelay {
	r := &RegistrationRelay{
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
	r.alertSink = func(msg string) { logger.Telegram(msg) }
	r.alertInterval = defaultRelayAlertInterval
	r.alertBuckets = make(map[string]*alertBucket)
	return r
}

// SetProvisionalChecker configures the predicate to check if an address has provisional status on-chain.
func (r *RegistrationRelay) SetProvisionalChecker(fn func(common.Address) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.isProvisional = fn
}

// SetLockedChecker configures the predicate to check if an address is locked on-chain.
func (r *RegistrationRelay) SetLockedChecker(fn func(common.Address) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.isLocked = fn
}

// SetLockedHomeClusterChecker configures a durable lookup of the home cluster
// recorded when an account was locked after Parent Chain reconciliation.
func (r *RegistrationRelay) SetLockedHomeClusterChecker(fn func(common.Address) cm.PublicKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lockedHome = fn
}

// SetProvisionalProposer configures the proposer callback triggered when Parent Chain is offline.
func (r *RegistrationRelay) SetProvisionalProposer(fn func(user common.Address, userSig []byte) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.provisionalProposer = fn
}

// SetLockProposer configures the proposer callback triggered when an address is discovered to belong to another cluster.
func (r *RegistrationRelay) SetLockProposer(fn func(user common.Address, homeCluster cm.PublicKey) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lockProposer = fn
}

// SetStore attaches a durable store to the relay and reloads any unconfirmed requests.
func (r *RegistrationRelay) SetStore(store RegistrationRelayStore) error {
	if store == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = store

	records, err := store.ScanAll()
	if err != nil {
		return fmt.Errorf("failed to scan registration store: %w", err)
	}

	for _, rec := range records {
		if rec == nil || rec.User == (common.Address{}) {
			continue
		}
		// Registered or locked on-chain: Status() answers CONFIRMED/REJECTED from chain state, so the persisted record is cleaned up.
		if (r.registered != nil && r.registered(rec.User)) || (r.isLocked != nil && r.isLocked(rec.User)) {
			_ = store.Delete(rec.User)
			continue
		}
		// Bounded memory: records beyond maxTracked stay in the store; the stall sweep loads them once room frees up.
		if len(r.entries) >= r.maxTracked {
			r.storeHasMore = true
			continue
		}
		entry := entryFromRecord(rec)
		r.entries[rec.User] = entry

		// Re-enqueue pending or provisional requests so they resume reconciliation with parent. If the queue is
		// full the stall sweep in ProcessBatch enqueues them later.
		if entry.status == RegStatusPending || entry.status == RegStatusProvisional {
			r.enqueueLocked(entry)
		}
	}
	log.Printf("RegistrationRelay: restored %d persisted registration request(s) (%d tracked in memory)", len(records), len(r.entries))
	r.wake()
	return nil
}

// SetOnStatusChanged registers a callback to receive push notifications when a registration reaches a terminal state.
func (r *RegistrationRelay) SetOnStatusChanged(fn func(user common.Address, res RegistrationResult)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onStatusChanged = fn
}

func (r *RegistrationRelay) notifyStatusChanged(user common.Address, res RegistrationResult) {
	r.mu.Lock()
	fn := r.onStatusChanged
	r.mu.Unlock()
	if fn != nil {
		go fn(user, res)
	}
}

// Wake triggers an immediate batch check on the registration relay loop.
func (r *RegistrationRelay) Wake() {
	r.wake()
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

// Submit accepts a registration request. It is idempotent: an address that is already confirmed, pending, provisional or rejected
// returns that state without enqueueing again; a FAILED request is reset and re-queued.
func (r *RegistrationRelay) Submit(user common.Address, userSig []byte) (RegistrationResult, error) {
	if user == (common.Address{}) {
		return RegistrationResult{}, errors.New("empty user address")
	}
	if r.isLocked != nil && r.isLocked(user) {
		return RegistrationResult{Status: RegStatusRejected, Reason: "account is locked on-chain"}, nil
	}
	if r.registered != nil && r.registered(user) {
		return RegistrationResult{Status: RegStatusConfirmed}, nil
	}
	if r.isProvisional != nil && r.isProvisional(user) {
		return RegistrationResult{Status: RegStatusProvisional}, nil
	}
	if !r.verifyUserSig(user, userSig) {
		return RegistrationResult{}, ErrInvalidRegistrationSig
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.entries[user]; ok {
		switch e.status {
		case RegStatusPending, RegStatusProvisional, RegStatusRejected, RegStatusConfirmed:
			return r.resultLocked(e), nil
		}
		// FAILED: allow a fresh attempt below
	} else if len(r.entries) >= r.maxTracked {
		r.evictTerminalLocked()
		if len(r.entries) >= r.maxTracked {
			return RegistrationResult{}, ErrRegistrationBusy
		}
	}

	// Persist (fsync) BEFORE acknowledging: a crash after this point cannot lose the request, it is reloaded and
	// relayed by SetStore on restart.
	e := &relayEntry{user: user, userSig: append([]byte(nil), userSig...), status: RegStatusPending, createdAt: time.Now()}
	if err := r.saveLocked(e); err != nil {
		return RegistrationResult{}, fmt.Errorf("failed to persist registration request: %w", err)
	}

	if !r.enqueueLocked(e) {
		// Not accepted: drop the record again so a busy answer leaves no orphan behind.
		if r.store != nil {
			if err := r.store.Delete(user); err != nil {
				log.Printf("RegistrationRelay: failed to drop busy request %s: %v", user.Hex(), err)
			}
		}
		return RegistrationResult{}, ErrRegistrationBusy
	}
	r.entries[user] = e
	r.wake()
	return r.resultLocked(e), nil
}

// Status reports the current state of an address. The on-chain flag always wins.
func (r *RegistrationRelay) Status(user common.Address) RegistrationResult {
	if r.isLocked != nil && r.isLocked(user) {
		var home cm.PublicKey
		if r.lockedHome != nil {
			home = r.lockedHome(user)
		}
		if home != (cm.PublicKey{}) {
			h := home
			return RegistrationResult{Status: RegStatusRejected, HomeCluster: &h, Reason: "account is locked on-chain (registered to another cluster)"}
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if e, ok := r.entries[user]; ok && e.home != nil {
			return r.resultLocked(e)
		}
		return RegistrationResult{Status: RegStatusRejected, Reason: "account is locked on-chain (registered to another cluster)"}
	}
	if r.registered != nil && r.registered(user) {
		return RegistrationResult{Status: RegStatusConfirmed}
	}
	if r.isProvisional != nil && r.isProvisional(user) {
		return RegistrationResult{Status: RegStatusProvisional}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[user]; ok {
		return r.resultLocked(e)
	}
	if r.store != nil {
		if rec, found, err := r.store.Get(user); err == nil && found && rec != nil {
			entry := entryFromRecord(rec)
			r.entries[user] = entry
			return r.resultLocked(entry)
		}
	}
	return RegistrationResult{Status: RegStatusNone}
}

// PendingStats returns the current number of PENDING registration requests and the age
// in seconds of the oldest PENDING request.
func (r *RegistrationRelay) PendingStats() (int, float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int
	var maxAge float64
	now := time.Now()
	for _, e := range r.entries {
		if e.status == RegStatusPending {
			count++
			if !e.createdAt.IsZero() {
				age := now.Sub(e.createdAt).Seconds()
				if age > maxAge {
					maxAge = age
				}
			}
		}
	}
	return count, maxAge
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
	r.flushAlerts(true)
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
	var toSend []common.Address
	for i := 0; i < n; i++ {
		select {
		case user := <-r.queue:
			r.mu.Lock()
			if e, ok := r.entries[user]; ok {
				e.queued = false
			}
			r.mu.Unlock()
			if r.prepareRelay(user) {
				toSend = append(toSend, user)
			}
		default:
			i = n // queue drained by someone else
		}
	}
	// Requests the parent does not know yet go out together: one batch tx (one parent nonce) when enabled.
	r.sendRegistrations(toSend)
	// 2. submitted requests still waiting for the parent's answer
	for _, user := range r.submittedUsers(relayBatchPerTick) {
		r.verifyOne(user)
	}
	// 3. requests that are persisted/tracked but not in the queue (queue was full at restart or on retry, or they did
	// not fit in memory) are enqueued again, so nothing waits for a user action to be retried.
	r.requeueStalled()
	r.flushAlerts(false)
}

// requeueStalled enqueues unsubmitted PENDING/PROVISIONAL requests that are not in the queue, and loads persisted
// requests that did not fit in memory once there is room.
func (r *RegistrationRelay) requeueStalled() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.storeHasMore && r.store != nil && len(r.entries) < r.maxTracked {
		r.loadMoreFromStoreLocked()
	}
	budget := relayBatchPerTick
	for _, e := range r.entries {
		if budget == 0 {
			return
		}
		if (e.status == RegStatusPending || e.status == RegStatusProvisional) && !e.submitted && !e.queued {
			if !r.enqueueLocked(e) {
				return // queue full: try again next tick
			}
			budget--
		}
	}
}

func (r *RegistrationRelay) loadMoreFromStoreLocked() {
	records, err := r.store.ScanAll()
	if err != nil {
		log.Printf("RegistrationRelay: failed to scan registration store: %v", err)
		return
	}
	more := false
	for _, rec := range records {
		if rec == nil || rec.User == (common.Address{}) {
			continue
		}
		if _, ok := r.entries[rec.User]; ok {
			continue
		}
		if len(r.entries) >= r.maxTracked {
			more = true
			break
		}
		r.entries[rec.User] = entryFromRecord(rec)
	}
	r.storeHasMore = more
}

// enqueueLocked puts an unsubmitted request on the relay queue unless it is already there. Caller holds r.mu.
func (r *RegistrationRelay) enqueueLocked(e *relayEntry) bool {
	if e.queued {
		return true
	}
	if e.submitted {
		return false
	}
	select {
	case r.queue <- e.user:
		e.queued = true
		return true
	default:
		return false
	}
}

func (r *RegistrationRelay) enqueue(user common.Address) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[user]
	return ok && r.enqueueLocked(e)
}

// evictTerminalLocked frees memory held by FAILED/REJECTED requests. Their records stay in the store, so Status()
// still answers them (lazy load) and a FAILED user can resubmit. Caller holds r.mu.
func (r *RegistrationRelay) evictTerminalLocked() {
	for u, e := range r.entries {
		if e.status == RegStatusFailed || e.status == RegStatusRejected {
			delete(r.entries, u)
		}
	}
}

func entryFromRecord(rec *RelayRecord) *relayEntry {
	created := time.Now()
	if rec.CreatedAt > 0 {
		created = time.Unix(rec.CreatedAt, 0)
	}
	return &relayEntry{
		user:      rec.User,
		userSig:   append([]byte(nil), rec.UserSig...),
		status:    rec.Status,
		submitted: rec.Submitted,
		attempts:  rec.Attempts,
		verifies:  rec.Verifies,
		home:      rec.Home,
		reason:    rec.Reason,
		createdAt: created,
		txHash:    rec.TxHash,
	}
}

func (r *RegistrationRelay) submittedUsers(limit int) []common.Address {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []common.Address
	for u, e := range r.entries {
		if (e.status == RegStatusPending || e.status == RegStatusProvisional) && e.submitted {
			out = append(out, u)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

func (r *RegistrationRelay) markProvisionalIfApplied(user common.Address) {
	r.mu.Lock()
	checker := r.isProvisional
	r.mu.Unlock()
	if checker == nil || !checker(user) {
		return
	}

	changed := false
	r.update(user, func(e *relayEntry) {
		if e.status == RegStatusPending {
			e.status = RegStatusProvisional
			e.reason = ""
			changed = true
		}
	})
	if changed {
		r.notifyStatusChanged(user, RegistrationResult{Status: RegStatusProvisional})
	}
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
		r.saveLocked(e)
	}
}

func (r *RegistrationRelay) saveLocked(e *relayEntry) error {
	if r.store == nil || e == nil {
		return nil
	}
	var created int64
	if !e.createdAt.IsZero() {
		created = e.createdAt.Unix()
	}
	rec := &RelayRecord{
		User:      e.user,
		UserSig:   append([]byte(nil), e.userSig...),
		Status:    e.status,
		Submitted: e.submitted,
		Attempts:  e.attempts,
		Verifies:  e.verifies,
		Home:      e.home,
		Reason:    e.reason,
		CreatedAt: created,
		TxHash:    e.txHash,
	}
	if err := r.store.Put(rec); err != nil {
		log.Printf("RegistrationRelay: failed to persist entry %s: %v", e.user.Hex(), err)
		return err
	}
	return nil
}

// retryOrFail puts a request back for a later tick, or marks it FAILED once its attempts are exhausted.
// If the account is provisional, network outages do not abandon reconciliation.
func (r *RegistrationRelay) retryOrFail(user common.Address, reason string) {
	failed := false
	r.mu.Lock()
	checker := r.isProvisional
	r.mu.Unlock()

	isProvOnChain := checker != nil && checker(user)

	var isProv bool
	r.update(user, func(e *relayEntry) {
		isProv = isProvOnChain || e.status == RegStatusProvisional
		e.attempts++
		e.reason = reason
		e.submitted = false
		e.txHash = common.Hash{}
		if !isProv && e.attempts >= r.maxAttempts {
			e.status = RegStatusFailed
			failed = true
		}
	})
	if failed {
		log.Printf("RegistrationRelay: giving up on %s: %s", user.Hex(), reason)
		r.alert(user, "❌ registration FAILED (attempts exhausted)", reason)
		r.notifyStatusChanged(user, RegistrationResult{Status: RegStatusFailed, Reason: reason})
		return
	}
	r.alertOnce(user, reason)
	r.notifyStatusChanged(user, RegistrationResult{Status: RegStatusPending, Reason: reason})
	if !r.enqueue(user) {
		r.update(user, func(e *relayEntry) {
			if !isProv {
				e.status = RegStatusFailed
			} else {
				e.submitted = true
				e.verifies = r.maxVerifies
			}
			e.reason = "queue full while retrying"
		})
		r.alert(user, "⚠️ relay retry queue full", "queue full while retrying")
		if !isProv {
			r.notifyStatusChanged(user, RegistrationResult{Status: RegStatusFailed, Reason: "queue full while retrying"})
		}
	}
}

// relayOne asks the parent who owns the address, and if nobody does, sends the cluster-signed registration.
func (r *RegistrationRelay) relayOne(user common.Address) {
	if r.prepareRelay(user) {
		r.sendRegistrations([]common.Address{user})
	}
}

// prepareRelay asks the parent who owns the address. It returns true only when nobody does yet, i.e. the cluster-signed
// registration must be sent; every other outcome (confirmed, owned, lookup error) is handled here.
func (r *RegistrationRelay) prepareRelay(user common.Address) bool {
	e, ok := r.snapshot(user)
	if !ok || (e.status != RegStatusPending && e.status != RegStatusProvisional) {
		return false
	}
	if r.registered != nil && r.registered(user) {
		r.markConfirmed(user)
		return false
	}
	r.markProvisionalIfApplied(user)

	home, found, err := r.parent.GetAccountRegistry(user)
	if err != nil {
		r.retryOrFail(user, fmt.Sprintf("parent registry lookup failed: %v", err))
		r.maybeProposeProvisional(user, e.userSig, err)
		return false
	}
	if found {
		r.resolveFound(user, home)
		return false
	}
	return true
}

// SetBatchSize enables sending up to n registrations in ONE Parent Chain transaction (one nonce) when the parent
// client supports it. n <= 1 keeps one transaction per registration. Only enable it once every Parent Chain node
// runs a version that knows METHOD_REGISTER_ACCOUNTS_BATCH.
func (r *RegistrationRelay) SetBatchSize(n int) {
	if n > parentchain.MaxRegisterAccountsBatch {
		n = parentchain.MaxRegisterAccountsBatch
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batchSize = n
}

// batchRegistrationParent is implemented by parent clients that can send a registration batch (parentchain.QuorumClient).
type batchRegistrationParent interface {
	SendRegisterAccountsBatch(entries []parentchain.RegisterAccountEntry) (common.Hash, error)
}

// sendRegistrations sends the cluster-signed registrations of users: in batches when enabled, otherwise (or for a
// request whose batch was rejected by the parent before) one transaction each.
func (r *RegistrationRelay) sendRegistrations(users []common.Address) {
	if len(users) == 0 {
		return
	}
	r.mu.Lock()
	size := r.batchSize
	var batchable, single []common.Address
	for _, u := range users {
		if e, ok := r.entries[u]; ok && e.noBatch {
			single = append(single, u)
		} else {
			batchable = append(batchable, u)
		}
	}
	r.mu.Unlock()
	bp, canBatch := r.parent.(batchRegistrationParent)
	if !canBatch || size <= 1 || len(batchable) < 2 {
		single = append(single, batchable...)
		batchable = nil
	}
	for len(batchable) > 0 {
		n := size
		if n > len(batchable) {
			n = len(batchable)
		}
		r.sendBatch(bp, batchable[:n])
		batchable = batchable[n:]
	}
	for _, u := range single {
		r.sendSingle(u)
	}
}

func (r *RegistrationRelay) sendSingle(user common.Address) {
	e, ok := r.snapshot(user)
	if !ok {
		return
	}
	digest := r.RegistrationDigest(user)
	txHash, err := r.parent.SendRegisterAccount(user, r.clusterKey, e.userSig, r.sign(digest))
	r.afterSend(user, e.userSig, txHash, false, err)
}

func (r *RegistrationRelay) sendBatch(bp batchRegistrationParent, users []common.Address) {
	entries := make([]parentchain.RegisterAccountEntry, 0, len(users))
	sigs := make([][]byte, 0, len(users))
	sent := make([]common.Address, 0, len(users))
	for _, u := range users {
		e, ok := r.snapshot(u)
		if !ok {
			continue
		}
		entries = append(entries, parentchain.RegisterAccountEntry{
			UserAddress:      u,
			FloatIdentityKey: r.clusterKey,
			UserSig:          e.userSig,
			ClusterSig:       r.sign(r.RegistrationDigest(u)),
		})
		sigs = append(sigs, e.userSig)
		sent = append(sent, u)
	}
	if len(entries) == 0 {
		return
	}
	txHash, err := bp.SendRegisterAccountsBatch(entries)
	if err != nil {
		// The parent refused the batch itself (e.g. a node without batch support): send these one by one, now.
		log.Printf("RegistrationRelay: batch of %d registrations refused, falling back to single transactions: %v", len(sent), err)
		for _, u := range sent {
			r.update(u, func(e *relayEntry) { e.noBatch = true })
			r.sendSingle(u)
		}
		return
	}
	for i, u := range sent {
		r.afterSend(u, sigs[i], txHash, true, nil)
	}
}

// afterSend records the outcome of sending a registration and proposes provisional activation when appropriate.
func (r *RegistrationRelay) afterSend(user common.Address, userSig []byte, txHash common.Hash, batched bool, err error) {
	if err != nil {
		r.retryOrFail(user, fmt.Sprintf("parent registration submit failed: %v", err))
		r.maybeProposeProvisional(user, userSig, err)
		return
	}
	r.update(user, func(e *relayEntry) {
		e.submitted = true
		e.verifies = 0
		e.reason = ""
		e.txHash = txHash
		e.batched = batched
	})
	// Nobody owns the address and the parent accepted our registration: activate it provisionally on this cluster
	// right away instead of making the user wait for the Parent Chain to execute and confirm it.
	r.maybeProposeProvisional(user, userSig, nil)
}

// reportTxFailureOnce resets the parent nonce pipeline once per failed tx hash: the members of a failed batch share one
// hash, and repeated reports of a hash already purged would force needless full nonce resets.
func (r *RegistrationRelay) reportTxFailureOnce(txHash common.Hash) {
	if txHash == (common.Hash{}) || r.parent == nil {
		return
	}
	r.mu.Lock()
	if r.reportedTx == nil || len(r.reportedTx) >= relayReportedTxCap {
		r.reportedTx = make(map[common.Hash]struct{})
	}
	_, done := r.reportedTx[txHash]
	r.reportedTx[txHash] = struct{}{}
	r.mu.Unlock()
	if !done {
		r.parent.ReportTxFailure(txHash)
	}
}

func isLocalConfigError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, parentchain.ErrNoNodesConfigured) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no parent chain nodes configured") ||
		strings.Contains(msg, "private key not configured") ||
		strings.Contains(msg, "private key is nil")
}

func (r *RegistrationRelay) maybeProposeProvisional(user common.Address, userSig []byte, parentErr error) {
	if isLocalConfigError(parentErr) {
		return
	}
	r.mu.Lock()
	e, ok := r.entries[user]
	if !ok || e.status != RegStatusPending {
		r.mu.Unlock()
		return
	}
	// One proposal per request, then wait relayProvisionalRetryTicks attempts before proposing again: the proposal
	// needs one committee attestation, so it applies at its first commit and the on-chain flag stops further ones.
	// Re-proposing on every tick would put one system tx per waiting user into every block during a parent outage.
	if e.provWait > 0 {
		e.provWait--
		r.mu.Unlock()
		return
	}
	isProv := r.isProvisional
	prop := r.provisionalProposer
	r.mu.Unlock()

	if isProv != nil && isProv(user) {
		return
	}
	if prop == nil {
		return
	}
	if err := prop(user, userSig); err != nil {
		log.Printf("RegistrationRelay: failed to propose provisional activation for %s (will retry): %v", user.Hex(), err)
		return // retry on the next attempt
	}
	r.mu.Lock()
	if e, ok := r.entries[user]; ok {
		e.provWait = relayProvisionalRetryTicks
	}
	r.mu.Unlock()
}

// markConfirmed handles the on-chain CONFIRMED state: the request is finished, so it is dropped from memory and from
// the store (Status() answers CONFIRMED from the on-chain flag). A recovery is reported if a sync error was alerted.
func (r *RegistrationRelay) markConfirmed(user common.Address) {
	wasAlerted := false
	r.mu.Lock()
	if e, ok := r.entries[user]; ok {
		wasAlerted = e.alerted
		delete(r.entries, user)
	}
	if r.store != nil {
		if err := r.store.Delete(user); err != nil {
			log.Printf("RegistrationRelay: failed to drop confirmed request %s: %v", user.Hex(), err)
		}
	}
	r.mu.Unlock()
	if wasAlerted {
		r.alert(user, "✅ CONFIRMED after sync error", "")
	}
	r.notifyStatusChanged(user, RegistrationResult{Status: RegStatusConfirmed})
}

// alertOnce records at most one sync-error alert per trouble episode of a request (re-armed on confirmation), so the
// alert counts affected accounts rather than retry ticks.
func (r *RegistrationRelay) alertOnce(user common.Address, reason string) {
	first := false
	r.mu.Lock()
	if e, ok := r.entries[user]; ok && !e.alerted {
		e.alerted = true
		first = true
	}
	r.mu.Unlock()
	if first {
		r.alert(user, "⚠️ Parent Chain sync error", reason)
	}
}

// alert records an operator alert of kind `title`. Alerts are not sent one by one: flushAlerts aggregates them into a
// single Telegram message per alertInterval (count per kind + a few sample accounts), so a Parent Chain outage
// affecting thousands of registrations produces one message per interval instead of a flood.
func (r *RegistrationRelay) alert(user common.Address, title, reason string) {
	sample := user.Hex()
	if reason != "" {
		sample += ": " + reason
	}
	r.alertMu.Lock()
	defer r.alertMu.Unlock()
	if r.alertBuckets == nil {
		r.alertBuckets = make(map[string]*alertBucket)
	}
	b, ok := r.alertBuckets[title]
	if !ok {
		b = &alertBucket{}
		r.alertBuckets[title] = b
		r.alertOrder = append(r.alertOrder, title)
	}
	b.count++
	if len(b.samples) < relayAlertSamplesPerKind {
		b.samples = append(b.samples, sample)
	}
}

// flushAlerts sends the aggregated alerts as one Telegram message once alertInterval has elapsed since the previous
// message (force sends immediately). The first problem after a quiet period is therefore reported at once, later ones
// at most once per interval. Wall-clock here only paces operator notifications; it decides nothing on-chain.
func (r *RegistrationRelay) flushAlerts(force bool) {
	r.alertMu.Lock()
	if len(r.alertOrder) == 0 || (!force && time.Since(r.lastAlertFlush) < r.alertInterval) {
		r.alertMu.Unlock()
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "[Registration Sync] cluster=%s", r.clusterTag())
	for _, title := range r.alertOrder {
		b := r.alertBuckets[title]
		fmt.Fprintf(&sb, "\n%s ×%d", title, b.count)
		for _, s := range b.samples {
			sb.WriteString("\n  - " + s)
		}
		if b.count > len(b.samples) {
			fmt.Fprintf(&sb, "\n  - ... and %d more", b.count-len(b.samples))
		}
	}
	r.alertBuckets = make(map[string]*alertBucket)
	r.alertOrder = nil
	r.lastAlertFlush = time.Now()
	r.alertMu.Unlock()

	pending, provisional, failed := r.trackedCounts()
	fmt.Fprintf(&sb, "\nTracked now: pending=%d provisional=%d failed=%d", pending, provisional, failed)
	r.alertSink(sb.String())
}

func (r *RegistrationRelay) trackedCounts() (pending, provisional, failed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		switch e.status {
		case RegStatusPending:
			pending++
		case RegStatusProvisional:
			provisional++
		case RegStatusFailed:
			failed++
		}
	}
	return
}

func (r *RegistrationRelay) clusterTag() string {
	s := r.clusterKey.String()
	if len(s) > 16 {
		s = s[:16]
	}
	return s
}

// verifyOne polls the parent registry for a submitted request: the parent executes the registration after accepting the
// transaction, and a duplicate (the address already belongs to another cluster) only shows up in its registry.
func (r *RegistrationRelay) verifyOne(user common.Address) {
	e, ok := r.snapshot(user)
	if !ok || (e.status != RegStatusPending && e.status != RegStatusProvisional) || !e.submitted {
		return
	}
	if r.registered != nil && r.registered(user) {
		r.markConfirmed(user)
		return
	}
	r.markProvisionalIfApplied(user)

	// Check if parent chain rejected the submitted transaction (e.g. sender nonce mismatch or other revert)
	if e.txHash != (common.Hash{}) && r.parent != nil {
		if rcpt, found, rerr := r.parent.GetReceipt(e.txHash); rerr == nil && found {
			if rcpt.Status != 1 {
				reason := fmt.Sprintf("parent chain rejected registration tx (errCode=%d)", rcpt.ErrorCode)
				if rcpt.ErrorCode == 103 {
					reason = "parent chain rejected registration tx: sender nonce mismatch (errCode=103)"
				}
				// Always report transaction failure to reset the nonce pipeline on any non-success status,
				// avoiding cascade nonce gaps on subsequent attempts (once per hash: batch members share it).
				r.reportTxFailureOnce(e.txHash)
				if e.batched {
					// A failed batch tx (e.g. a parent without batch support) is not retried as a batch.
					r.update(user, func(e *relayEntry) { e.noBatch = true })
				}
				r.retryOrFail(user, reason)
				return
			}
		}
	}

	home, found, err := r.parent.GetAccountRegistry(user)
	if err != nil {
		// Transient network or quorum error: log warning and retry on next poll without burning an attempt.
		log.Printf("RegistrationRelay: transient lookup error for %s (will retry): %v", user.Hex(), err)
		r.alertOnce(user, fmt.Sprintf("parent registry lookup failed while confirming: %v", err))
		return
	}
	if found {
		r.resolveFound(user, home)
		return
	}
	failed := false
	resubmit := false
	r.mu.Lock()
	isProvOnChain := r.isProvisional != nil && r.isProvisional(user)
	r.mu.Unlock()

	var staleTx common.Hash
	r.update(user, func(e *relayEntry) {
		e.verifies++
		if e.verifies >= r.maxVerifies {
			staleTx = e.txHash
			e.txHash = common.Hash{}
			if e.status == RegStatusProvisional || isProvOnChain {
				e.status = RegStatusProvisional
				e.submitted = false
				e.verifies = 0
				e.reason = "parent did not confirm registration within limit; retrying submission"
				resubmit = true
			} else {
				e.status = RegStatusFailed
				e.reason = "the parent chain did not register the account"
				failed = true
			}
		}
	})
	r.reportTxFailureOnce(staleTx)
	if failed {
		r.alert(user, "❌ registration FAILED (parent confirmation timeout)", "the parent chain did not register the account")
		r.notifyStatusChanged(user, RegistrationResult{Status: RegStatusFailed, Reason: "the parent chain did not register the account"})
	}
	if resubmit {
		r.alert(user, "⏱️ Parent Chain confirmation timeout, resubmitting", "parent did not confirm registration within limit")
		if r.enqueue(user) {
			r.wake()
		} else {
			r.update(user, func(e *relayEntry) {
				e.submitted = true
				e.verifies = r.maxVerifies
				e.reason = "retry queue full; waiting to resubmit"
			})
		}
	}
}

// resolveFound applies the parent's answer: ours ⇒ stay PENDING until the on-chain flag is applied by the
// RegistrationWorker (Status then reports CONFIRMED); another cluster's ⇒ propose lock event and wait for
// the on-chain lock before marking REJECTED when a lock checker is configured.
func (r *RegistrationRelay) resolveFound(user common.Address, home cm.PublicKey) {
	if home != r.clusterKey {
		r.mu.Lock()
		lockProp := r.lockProposer
		isLocked := r.isLocked
		r.mu.Unlock()
		// Re-proposing on every poll is bounded by the proposer: it dedups in-flight payloads by hash (same lock
		// payload => no new tx until it commits or the 60s stall reset), so this cannot spam the pool.
		if lockProp != nil && (isLocked == nil || !isLocked(user)) {
			if err := lockProp(user, home); err != nil {
				log.Printf("RegistrationRelay: failed to propose lock for %s (will retry): %v", user.Hex(), err)
				r.retryOrFail(user, fmt.Sprintf("failed to propose account lock: %v", err))
				return
			}
		}
		if isLocked != nil && !isLocked(user) {
			// Pool admission and partial co-attestations are not proof of execution.
			// Keep polling even when the parent already had an owner at the first lookup.
			r.update(user, func(e *relayEntry) {
				e.submitted = true
				e.reason = "waiting for account lock on-chain"
			})
			return
		}
	}
	rejected := false
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
		rejected = true
	})
	if rejected {
		r.alert(user, "🚨 registration CONFLICT (address owned by another cluster)", "home="+home.String())
		h := home
		r.notifyStatusChanged(user, RegistrationResult{Status: RegStatusRejected, HomeCluster: &h, Reason: "address is registered to another cluster on the parent chain"})
	}
}
