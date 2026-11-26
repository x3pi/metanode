package rollup

import (
	"errors"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

// provHarness extends relayHarness with simulated on-chain Provisional and Locked flags (set when proposals
// "commit") and captures every Telegram message instead of sending it. alertInterval is 0, so every ProcessBatch
// that recorded alerts emits exactly one aggregated message.
type provHarness struct {
	*relayHarness
	prov        sync.Map // addr -> bool: on-chain Provisional flag
	locked      sync.Map // addr -> bool: on-chain Locked flag
	mu          sync.Mutex
	msgs        []string
	proposals   int
	lockCalls   int
	proposerErr error
	lockErr     error
	deferLock   bool // when true, lockProposer does not immediately set the on-chain Locked flag
}

func newProvHarness() *provHarness {
	h := &provHarness{relayHarness: newRelayHarness()}
	h.relay.SetProvisionalChecker(func(a common.Address) bool { v, ok := h.prov.Load(a); return ok && v.(bool) })
	h.relay.SetLockedChecker(func(a common.Address) bool { v, ok := h.locked.Load(a); return ok && v.(bool) })
	h.relay.SetProvisionalProposer(func(u common.Address, _ []byte) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.proposals++
		if h.proposerErr != nil {
			return h.proposerErr
		}
		h.prov.Store(u, true) // proposal committed by the cluster
		return nil
	})
	h.relay.SetLockProposer(func(u common.Address, _ cm.PublicKey) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.lockCalls++
		if h.lockErr != nil {
			return h.lockErr
		}
		if !h.deferLock {
			h.locked.Store(u, true)
			h.prov.Store(u, false) // locking clears provisional on-chain
		}
		return nil
	})
	h.relay.alertInterval = 0
	h.relay.alertSink = func(msg string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.msgs = append(h.msgs, msg)
	}
	return h
}

func (h *provHarness) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.msgs...)
}

func (h *provHarness) proposalCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.proposals
}

func (h *provHarness) lockProposalCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lockCalls
}

func (h *provHarness) ticks(n int) {
	for i := 0; i < n; i++ {
		h.relay.ProcessBatch()
	}
}

// ---------------------------------------------------------------------------------------------------------------
// Provisional activation
// ---------------------------------------------------------------------------------------------------------------

// Parent healthy: the account is provisionally active after the first tick and the registration is still relayed.
// A healthy sync produces no Telegram message.
func TestProvisionalAlert_ParentUp_ActivatesImmediatelyAndSyncsWithoutAlert(t *testing.T) {
	h := newProvHarness()
	user, sig := h.userSig(t)
	h.parent.onSend = func(u common.Address) { h.parent.registry[u] = h.clusterKP.PublicKey() }

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	h.ticks(1)

	assert.Equal(t, 1, h.proposalCount(), "provisional activation must be proposed on the first tick")
	assert.Equal(t, RegStatusProvisional, h.relay.Status(user).Status, "user can use the account right away")
	assert.Equal(t, 1, h.parent.sendCount(), "registration must be relayed to the parent")

	h.flags.Store(user, true) // the parent's ordered registration event lands on this cluster
	h.ticks(1)
	assert.Equal(t, RegStatusConfirmed, h.relay.Status(user).Status)
	assert.Empty(t, h.messages(), "a healthy sync must not alert")
}

// A failing provisional proposal (e.g. tx pool busy) at Submit is retried on the next tick.
func TestProvisionalAlert_ProposerErrorIsRetried(t *testing.T) {
	h := newProvHarness()
	h.proposerErr = errors.New("tx pool busy")
	user, sig := h.userSig(t)
	h.parent.lookupErr = errors.New("parent chain offline")
	res, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	assert.Equal(t, RegStatusPending, res.Status)
	assert.Equal(t, 1, h.proposalCount())

	h.ticks(1)
	assert.Equal(t, 2, h.proposalCount())
	assert.Equal(t, RegStatusPending, h.relay.Status(user).Status)

	h.mu.Lock()
	h.proposerErr = nil
	h.mu.Unlock()
	h.ticks(1)
	assert.Equal(t, 3, h.proposalCount())
	assert.Equal(t, RegStatusProvisional, h.relay.Status(user).Status)
}

// Address already owned by another cluster on the parent: provisionally activated at Submit, then locked on-chain
// once Parent responds, transitioning to REJECTED with a conflict alert.
func TestProvisionalAlert_OwnedByOtherCluster_NoProvisionalAndConflictAlert(t *testing.T) {
	h := newProvHarness()
	user, sig := h.userSig(t)
	h.parent.registry[user] = bls.GenerateKeyPair().PublicKey()

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	assert.Equal(t, 1, h.proposalCount(), "provisional activation is proposed right at Submit")
	assert.Equal(t, RegStatusProvisional, h.relay.Status(user).Status)

	h.ticks(1)

	assert.Equal(t, 1, h.lockProposalCount(), "lock proposer must run once parent reports another owner")
	assert.Equal(t, 0, h.parent.sendCount())
	assert.Equal(t, RegStatusRejected, h.relay.Status(user).Status)
	msgs := h.messages()
	require.Len(t, msgs, 1, "%v", msgs)
	assert.Contains(t, msgs[0], "CONFLICT")
	assert.Contains(t, msgs[0], user.Hex())
}

// Local misconfiguration (no parent nodes / no private key) is not a parent outage: no provisional activation, since
// this node could never reconcile it, but the operator is told.
func TestProvisionalAlert_LocalConfigError_NoProvisionalButAlert(t *testing.T) {
	for name, setup := range map[string]func(p *fakeRegParent){
		"NoNodes": func(p *fakeRegParent) {
			p.unconfigured = true
			p.lookupErr = parentchain.ErrNoNodesConfigured
		},
		"PrivateKey": func(p *fakeRegParent) {
			p.unconfigured = true
			p.sendErr = errors.New("cannot SendRegisterAccount: private key not configured")
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newProvHarness()
			setup(h.parent)
			user, sig := h.userSig(t)
			_, err := h.relay.Submit(user, sig)
			require.NoError(t, err)
			h.ticks(5)

			assert.Equal(t, 0, h.proposalCount())
			assert.Equal(t, RegStatusPending, h.relay.Status(user).Status)
			msgs := h.messages()
			require.Len(t, msgs, 1, "%v", msgs)
			assert.Contains(t, msgs[0], "Parent Chain sync error ×1")
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// Retry / timeout
// ---------------------------------------------------------------------------------------------------------------

// Parent executes the registration tx but rejects it: alerted with the error code, and the request is resent.
func TestProvisionalAlert_ParentRejectsTx_AlertWithErrorCodeAndResend(t *testing.T) {
	h := newProvHarness()
	user, sig := h.userSig(t)
	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	h.ticks(1)
	require.Equal(t, 1, h.parent.sendCount())

	e, _ := h.relay.snapshot(user)
	h.parent.mu.Lock()
	h.parent.receipts[e.txHash] = &parentchain.Receipt{TxHash: e.txHash, Status: 0, ErrorCode: 7}
	h.parent.mu.Unlock()
	h.relay.verifyOne(user)
	h.relay.flushAlerts(true)

	msgs := h.messages()
	require.Len(t, msgs, 1, "%v", msgs)
	assert.Contains(t, msgs[0], "errCode=7")
	assert.Equal(t, RegStatusProvisional, h.relay.Status(user).Status, "provisional account stays usable")

	h.parent.mu.Lock()
	delete(h.parent.receipts, e.txHash)
	h.parent.mu.Unlock()
	h.ticks(1)
	assert.Equal(t, 2, h.parent.sendCount(), "registration must be resent")
}

// Parent accepts the tx but never registers the address: confirmation timeout is alerted and the tx resent.
func TestProvisionalAlert_ConfirmationTimeout_AlertAndResend(t *testing.T) {
	h := newProvHarness()
	h.relay.maxVerifies = 3
	user, sig := h.userSig(t)
	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)

	h.ticks(5)
	assert.GreaterOrEqual(t, h.parent.sendCount(), 2, "registration must be resent after the confirmation limit")
	require.NotEmpty(t, h.messages())
	assert.Contains(t, strings.Join(h.messages(), "\n"), "confirmation timeout")
	assert.Equal(t, RegStatusProvisional, h.relay.Status(user).Status)
}

// Without provisional activation (no proposer wired), exhausted attempts end in FAILED with an alert.
func TestProvisionalAlert_NonProvisionalExhausted_FailedAlert(t *testing.T) {
	h := newRelayHarness()
	h.relay.maxAttempts = 3
	h.relay.alertInterval = 0
	var msgs []string
	h.relay.alertSink = func(m string) { msgs = append(msgs, m) }
	user, sig := h.userSig(t)
	h.parent.lookupErr = errors.New("connection refused")

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		h.relay.ProcessBatch()
	}

	assert.Equal(t, RegStatusFailed, h.relay.Status(user).Status)
	require.Len(t, msgs, 2, "%v", msgs)
	assert.Contains(t, msgs[0], "Parent Chain sync error ×1")
	assert.Contains(t, msgs[1], "FAILED")
}

// ---------------------------------------------------------------------------------------------------------------
// Telegram anti-spam
// ---------------------------------------------------------------------------------------------------------------

// Parent down for a long time: one message for the whole outage, the account stays PROVISIONAL (never FAILED), and a
// recovery line is sent once the parent confirms.
func TestProvisionalAlert_ParentDown_SingleMessageThenRecovery(t *testing.T) {
	h := newProvHarness()
	user, sig := h.userSig(t)
	h.parent.lookupErr = errors.New("dial tcp: i/o timeout")

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	h.ticks(1)
	assert.Equal(t, 1, h.proposalCount(), "provisional must be proposed on the first parent failure")

	h.ticks(3 * defaultRelayMaxAttempts)
	assert.Equal(t, RegStatusProvisional, h.relay.Status(user).Status, "provisional account must never be abandoned")
	assert.Equal(t, 1, h.proposalCount(), "no re-proposal once provisional is on-chain")

	msgs := h.messages()
	require.Len(t, msgs, 1, "exactly one message for the whole outage: %v", msgs)
	assert.Contains(t, msgs[0], "Parent Chain sync error ×1")
	assert.Contains(t, msgs[0], user.Hex())
	assert.Contains(t, msgs[0], "i/o timeout")
	assert.Contains(t, msgs[0], "Tracked now:")

	h.parent.mu.Lock()
	h.parent.lookupErr = nil
	h.parent.onSend = func(u common.Address) { h.parent.registry[u] = h.clusterKP.PublicKey() }
	h.parent.mu.Unlock()
	h.ticks(2)
	assert.Equal(t, 1, h.parent.sendCount(), "registration relayed once after recovery")

	h.flags.Store(user, true)
	h.ticks(1)
	assert.Equal(t, RegStatusConfirmed, h.relay.Status(user).Status)
	msgs = h.messages()
	require.Len(t, msgs, 2, "%v", msgs)
	assert.Contains(t, msgs[1], "CONFIRMED after sync error ×1")
}

// Many accounts failing in the same window: ONE message with a count and a few samples, not one per account.
func TestProvisionalAlert_ManyAccounts_OneAggregatedMessage(t *testing.T) {
	h := newProvHarness()
	h.relay.alertInterval = defaultRelayAlertInterval
	h.relay.lastAlertFlush = time.Now()
	h.parent.lookupErr = errors.New("parent chain offline")
	const n = 10
	for i := 0; i < n; i++ {
		u, sig := h.userSig(t)
		_, err := h.relay.Submit(u, sig)
		require.NoError(t, err)
	}
	h.ticks(50)
	h.relay.flushAlerts(true)
	assert.Equal(t, n, h.proposalCount())
	msgs := h.messages()
	require.Len(t, msgs, 1, "%v", msgs)
	assert.Contains(t, msgs[0], "Parent Chain sync error ×10")
	assert.Contains(t, msgs[0], "... and 7 more")
	assert.Equal(t, relayAlertSamplesPerKind, strings.Count(msgs[0], "\n  - 0x"))
}

// With the production interval, alerts recorded after a message are held until the interval elapses.
func TestProvisionalAlert_IntervalRateLimitsMessages(t *testing.T) {
	h := newProvHarness()
	h.relay.alertInterval = defaultRelayAlertInterval
	h.parent.lookupErr = errors.New("parent chain offline")

	u1, s1 := h.userSig(t)
	_, err := h.relay.Submit(u1, s1)
	require.NoError(t, err)
	h.ticks(1)
	require.Len(t, h.messages(), 1, "the first problem after a quiet period is reported at once")

	u2, s2 := h.userSig(t)
	_, err = h.relay.Submit(u2, s2)
	require.NoError(t, err)
	h.ticks(10)
	require.Len(t, h.messages(), 1, "later problems wait for the interval")

	h.relay.flushAlerts(true) // e.g. Stop() or the interval elapsed
	msgs := h.messages()
	require.Len(t, msgs, 2)
	assert.Contains(t, msgs[1], u2.Hex())
}

// ---------------------------------------------------------------------------------------------------------------
// Durability (crash / restart) and skip-if-exists
// ---------------------------------------------------------------------------------------------------------------

func newStoredRecords(t *testing.T, store RegistrationRelayStore, n int, status RegistrationStatus) []common.Address {
	t.Helper()
	var users []common.Address
	for i := 0; i < n; i++ {
		u := common.BigToAddress(big.NewInt(int64(1000 + i)))
		require.NoError(t, store.Put(&RelayRecord{User: u, UserSig: []byte{1}, Status: status, CreatedAt: 1}))
		users = append(users, u)
	}
	return users
}

// The request is on disk before Submit returns, so a crash right after the user's call cannot lose it.
func TestRegistrationDurability_SubmitPersistsBeforeAck(t *testing.T) {
	h := newRelayHarness()
	store := NewKVRelayStore(NewMemoryKVStore())
	require.NoError(t, h.relay.SetStore(store))
	user, sig := h.userSig(t)
	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)

	rec, found, err := store.Get(user)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, RegStatusPending, rec.Status)
	assert.Equal(t, sig, rec.UserSig, "the signature is persisted so the request can be resent after a crash")
}

// After a crash with more persisted requests than the queue holds, every one of them is still relayed.
func TestRegistrationDurability_RestartBacklogLargerThanQueueIsFullyRelayed(t *testing.T) {
	h := newRelayHarness()
	store := NewKVRelayStore(NewMemoryKVStore())
	newStoredRecords(t, store, 5, RegStatusPending)
	h.relay.queue = make(chan common.Address, 2)
	require.NoError(t, h.relay.SetStore(store))

	for i := 0; i < 5; i++ {
		h.relay.ProcessBatch()
	}
	assert.Equal(t, 5, h.parent.sendCount(), "requests that did not fit in the queue at restart must not be stranded")
}

// After a crash with more persisted requests than maxTracked, the rest are loaded and relayed as earlier ones finish.
func TestRegistrationDurability_RestartBacklogLargerThanMemoryIsFullyRelayed(t *testing.T) {
	h := newRelayHarness()
	h.relay.maxTracked = 2
	store := NewKVRelayStore(NewMemoryKVStore())
	newStoredRecords(t, store, 4, RegStatusProvisional)
	h.parent.onSend = func(u common.Address) {
		h.parent.registry[u] = h.clusterKP.PublicKey()
		h.flags.Store(u, true) // parent registered it and the event landed
	}
	require.NoError(t, h.relay.SetStore(store))
	require.Len(t, h.relay.entries, 2)

	for i := 0; i < 10; i++ {
		h.relay.ProcessBatch()
		h.relay.mu.Lock()
		require.LessOrEqual(t, len(h.relay.entries), 2, "memory stays bounded")
		h.relay.mu.Unlock()
	}
	assert.Equal(t, 4, h.parent.sendCount())
	left, err := store.ScanAll()
	require.NoError(t, err)
	assert.Empty(t, left, "confirmed requests are removed from the store")
}

// A persisted request whose address the parent already registered to this cluster is NOT sent again.
func TestRegistrationDurability_AlreadyRegisteredOnParentIsNotResent(t *testing.T) {
	h := newRelayHarness()
	store := NewKVRelayStore(NewMemoryKVStore())
	users := newStoredRecords(t, store, 1, RegStatusProvisional)
	h.parent.registry[users[0]] = h.clusterKP.PublicKey()
	require.NoError(t, h.relay.SetStore(store))

	h.relay.ProcessBatch()
	assert.Equal(t, 0, h.parent.sendCount(), "already registered on the parent: skip sending")

	h.flags.Store(users[0], true)
	h.relay.ProcessBatch()
	assert.Equal(t, RegStatusConfirmed, h.relay.Status(users[0]).Status)
	_, found, _ := store.Get(users[0])
	assert.False(t, found)
}

// A request already sent before the crash keeps its tx hash, so a parent-side rejection is still detected and resent.
func TestRegistrationDurability_TxHashSurvivesRestart(t *testing.T) {
	h := newRelayHarness()
	store := NewKVRelayStore(NewMemoryKVStore())
	user := common.BigToAddress(big.NewInt(42))
	tx := common.HexToHash("0xabc")
	require.NoError(t, store.Put(&RelayRecord{User: user, UserSig: []byte{1}, Status: RegStatusProvisional, Submitted: true, TxHash: tx}))
	h.parent.receipts[tx] = &parentchain.Receipt{TxHash: tx, Status: 0, ErrorCode: 103}
	require.NoError(t, h.relay.SetStore(store))

	h.relay.ProcessBatch()
	h.parent.mu.Lock()
	assert.Contains(t, h.parent.failedReports, tx)
	h.parent.mu.Unlock()
	h.relay.ProcessBatch()
	assert.Equal(t, 1, h.parent.sendCount(), "rejected tx is resent")
}

// Finished requests free their slot: the tracked table no longer fills up permanently.
func TestRegistrationDurability_FinishedRequestsFreeCapacity(t *testing.T) {
	h := newRelayHarness()
	h.relay.maxTracked = 1
	h.relay.maxAttempts = 1
	store := NewKVRelayStore(NewMemoryKVStore())
	require.NoError(t, h.relay.SetStore(store))

	// CONFIRMED frees its slot.
	u1, s1 := h.userSig(t)
	_, err := h.relay.Submit(u1, s1)
	require.NoError(t, err)
	h.flags.Store(u1, true)
	h.relay.ProcessBatch()
	u2, s2 := h.userSig(t)
	_, err = h.relay.Submit(u2, s2)
	require.NoError(t, err, "a confirmed request must not keep occupying the table")

	// FAILED frees its slot but is still answered from the store.
	h.parent.lookupErr = errors.New("parent down")
	h.relay.ProcessBatch()
	require.Equal(t, RegStatusFailed, h.relay.Status(u2).Status)
	h.parent.lookupErr = nil
	u3, s3 := h.userSig(t)
	_, err = h.relay.Submit(u3, s3)
	require.NoError(t, err, "a failed request must not keep occupying the table")
	assert.Equal(t, RegStatusFailed, h.relay.Status(u2).Status, "evicted FAILED request is still answered from the store")
}

// A request refused as busy leaves nothing in the store.
func TestRegistrationDurability_BusyLeavesNoOrphanRecord(t *testing.T) {
	h := newRelayHarness()
	h.relay.queue = make(chan common.Address, 1)
	store := NewKVRelayStore(NewMemoryKVStore())
	require.NoError(t, h.relay.SetStore(store))
	u1, s1 := h.userSig(t)
	_, err := h.relay.Submit(u1, s1)
	require.NoError(t, err)
	u2, s2 := h.userSig(t)
	_, err = h.relay.Submit(u2, s2)
	require.ErrorIs(t, err, ErrRegistrationBusy)
	_, found, _ := store.Get(u2)
	assert.False(t, found)
}

// ---------------------------------------------------------------------------------------------------------------
// Provisional-first (child chain first, parent chain reconciliation afterwards)
// ---------------------------------------------------------------------------------------------------------------

// 1. Submit proposes provisional activation immediately without consulting Parent Chain (lookupCount == 0).
// When parent == nil (non-rollup mode), Submit must NOT propose provisional activation.
func TestProvisionalFirst_SubmitProposesImmediatelyWithoutParentLookup(t *testing.T) {
	h := newProvHarness()
	user, sig := h.userSig(t)

	res, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	assert.Equal(t, RegStatusPending, res.Status, "Submit returns PENDING until the block commits")
	assert.Equal(t, 1, h.proposalCount(), "provisional activation must be proposed right at Submit")
	assert.Equal(t, 0, h.parent.getLookupCount(), "Submit must not query Parent Chain")
	assert.Equal(t, 0, h.parent.sendCount(), "Submit must not send to Parent Chain")
	assert.Equal(t, RegStatusProvisional, h.relay.Status(user).Status, "Status becomes PROVISIONAL once on-chain flag commits")

	// Non-rollup mode (r.parent == nil): Submit does not propose provisional.
	noParent := NewRegistrationRelay(nil, h.clusterKP.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(h.clusterKP.PrivateKey(), d) },
		func(common.Address) bool { return false })
	calls := 0
	noParent.SetProvisionalProposer(func(common.Address, []byte) error { calls++; return nil })
	u2, s2 := h.userSig(t)
	_, err = noParent.Submit(u2, s2)
	require.NoError(t, err)
	assert.Equal(t, 0, calls)
}

// 2. If provisional proposer fails at Submit (e.g. tx pool full), Submit still returns PENDING without error,
// resets provWait, and the very next tick retries proposing provisional activation.
func TestProvisionalFirst_SubmitProposerErrorReturnsPendingAndRetriesNextTick(t *testing.T) {
	h := newProvHarness()
	h.proposerErr = errors.New("tx pool full")
	user, sig := h.userSig(t)

	res, err := h.relay.Submit(user, sig)
	require.NoError(t, err, "proposer failure must not fail Submit")
	assert.Equal(t, RegStatusPending, res.Status)
	assert.Equal(t, 1, h.proposalCount())

	// provWait must have been reset to 0 so the next tick retries immediately.
	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.Equal(t, 0, entry.provWait)

	h.mu.Lock()
	h.proposerErr = nil
	h.mu.Unlock()
	h.ticks(1)
	assert.Equal(t, 2, h.proposalCount(), "next tick must immediately retry provisional proposal")
	assert.Equal(t, RegStatusProvisional, h.relay.Status(user).Status)
	assert.Equal(t, 1, h.parent.sendCount())
}

// 3. Account owned by another cluster: provisionally activated at Submit -> lock proposer called -> stays non-REJECTED
// until the on-chain Locked flag commits -> then transitions to REJECTED and does not re-propose lock.
func TestProvisionalFirst_OwnedByOtherCluster_ProvisionalThenLockThenRejected(t *testing.T) {
	h := newProvHarness()
	h.deferLock = true // simulate async committee lock tx that has not committed yet
	otherCluster := bls.GenerateKeyPair().PublicKey()
	user, sig := h.userSig(t)
	h.parent.registry[user] = otherCluster

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	assert.Equal(t, 1, h.proposalCount())
	assert.Equal(t, RegStatusProvisional, h.relay.Status(user).Status)

	// Tick 1: Parent reports otherCluster -> lock proposer called, but lock has not committed on-chain yet.
	h.ticks(1)
	assert.Positive(t, h.lockProposalCount())
	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.True(t, entry.submitted, "must keep submitted=true while waiting for lock execution")
	assert.NotEqual(t, RegStatusRejected, entry.status)
	assert.Equal(t, RegStatusProvisional, h.relay.Status(user).Status)

	// On-chain lock commits (sets Locked=true, clears Provisional=false, matching AccountRegistryHandler).
	h.locked.Store(user, true)
	h.prov.Store(user, false)

	beforeLockCommit := h.lockProposalCount()
	h.ticks(1)
	assert.Equal(t, beforeLockCommit, h.lockProposalCount(), "must not propose lock again after on-chain lock commits")
	res := h.relay.Status(user)
	assert.Equal(t, RegStatusRejected, res.Status)
	require.NotNil(t, res.HomeCluster)
	assert.Equal(t, otherCluster, *res.HomeCluster)
}

// 4. Parent down with 64 wallets in queue: each tick does at most 1 Parent lookup, the remaining 63 wallets are left
// in the queue without touching Parent or incrementing attempts, and all 64 remain PROVISIONAL (never FAILED).
func TestProvisionalFirst_ParentDown64Wallets_OneLookupPerTickAndNoAttemptBurn(t *testing.T) {
	h := newProvHarness()
	h.parent.lookupErr = errors.New("dial tcp: i/o timeout")

	users := make([]common.Address, 0, relayBatchPerTick)
	for i := 0; i < relayBatchPerTick; i++ {
		u, sig := h.userSig(t)
		_, err := h.relay.Submit(u, sig)
		require.NoError(t, err)
		users = append(users, u)
	}
	assert.Equal(t, relayBatchPerTick, h.proposalCount(), "all 64 wallets propose provisional at Submit")
	assert.Equal(t, 0, h.parent.getLookupCount())

	// Tick 1: only 1 lookup; remaining 63 stay in queue untouched.
	h.ticks(1)
	assert.Equal(t, 1, h.parent.getLookupCount(), "tick must stop after first parent network error")
	assert.Equal(t, relayBatchPerTick, len(h.relay.queue), "all 64 entries remain queued (63 untouched + 1 rotated to back)")

	e0, ok := h.relay.snapshot(users[0])
	require.True(t, ok)
	assert.Equal(t, 1, e0.attempts, "first wallet burned 1 attempt")
	for i := 1; i < relayBatchPerTick; i++ {
		ei, ok := h.relay.snapshot(users[i])
		require.True(t, ok)
		assert.Equal(t, 0, ei.attempts, "remaining wallet %d must not burn attempts", i)
		assert.True(t, ei.queued, "remaining wallet %d must stay queued", i)
	}

	// Tick 2: rotates to users[1], still only 1 lookup.
	h.ticks(1)
	assert.Equal(t, 2, h.parent.getLookupCount())
	e1, _ := h.relay.snapshot(users[1])
	assert.Equal(t, 1, e1.attempts)
	e2, _ := h.relay.snapshot(users[2])
	assert.Equal(t, 0, e2.attempts)

	// Verify verifyOne also breaks on first network error (both GetAccountRegistry error and GetReceipt error).
	t.Run("VerifyOneBreaksOnRegistryError", func(t *testing.T) {
		hv := newProvHarness()
		var subUsers []common.Address
		for i := 0; i < 4; i++ {
			u, sig := hv.userSig(t)
			_, err := hv.relay.Submit(u, sig)
			require.NoError(t, err)
			subUsers = append(subUsers, u)
		}
		hv.ticks(1) // submits all 4 (4 prepareRelay lookups + 4 verifyOne lookups in the same tick)
		require.Equal(t, 8, hv.parent.getLookupCount())

		hv.parent.mu.Lock()
		hv.parent.lookupCount = 0
		hv.parent.lookupErr = errors.New("parent timeout")
		hv.parent.mu.Unlock()

		hv.ticks(1) // verifyOne loop must break after the 1st lookup error
		assert.Equal(t, 1, hv.parent.getLookupCount(), "verifyOne loop must stop after first network error")
		for _, u := range subUsers {
			assert.Equal(t, RegStatusProvisional, hv.relay.Status(u).Status)
		}
	})

	t.Run("VerifyOneBreaksOnReceiptError", func(t *testing.T) {
		hv := newProvHarness()
		for i := 0; i < 4; i++ {
			u, sig := hv.userSig(t)
			_, err := hv.relay.Submit(u, sig)
			require.NoError(t, err)
		}
		hv.ticks(1)
		hv.parent.mu.Lock()
		hv.parent.lookupCount = 0
		hv.parent.receiptCount = 0
		hv.parent.receiptErr = errors.New("receipt RPC timeout")
		hv.parent.mu.Unlock()

		hv.ticks(1) // verifyOne must break on first GetReceipt network error without calling GetAccountRegistry
		assert.Equal(t, 1, hv.parent.getReceiptCount(), "verifyOne loop must stop after first receipt error")
		assert.Equal(t, 0, hv.parent.getLookupCount(), "must not fall through to GetAccountRegistry after receipt error")
	})
}

// 5. Reloaded request after restart (SetStore) proposes provisional BEFORE querying Parent Chain.
func TestProvisionalFirst_RestartReloadProposesBeforeParentLookup(t *testing.T) {
	store := NewKVRelayStore(NewMemoryKVStore())
	kp := bls.GenerateKeyPair()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	addr := crypto.PubkeyToAddress(key.PublicKey)

	parent := newFakeRegParent()
	parent.lookupErr = errors.New("parent offline")

	var prov sync.Map
	var orderMu sync.Mutex
	var order []string

	parent.onLookup = func(u common.Address) {
		if u == addr {
			orderMu.Lock()
			order = append(order, "lookup")
			orderMu.Unlock()
		}
	}

	relay := NewRegistrationRelay(parent, kp.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), d) },
		func(common.Address) bool { return false })
	relay.alertSink = func(string) {}
	sig, err := crypto.Sign(crypto.Keccak256(relay.RegistrationDigest(addr)), key)
	require.NoError(t, err)

	// Persist a PENDING request as if node crashed before proposing provisional.
	require.NoError(t, store.Put(&RelayRecord{User: addr, UserSig: sig, Status: RegStatusPending, CreatedAt: 1}))

	relay.SetProvisionalChecker(func(a common.Address) bool { v, ok := prov.Load(a); return ok && v.(bool) })
	relay.SetProvisionalProposer(func(u common.Address, _ []byte) error {
		orderMu.Lock()
		order = append(order, "propose")
		orderMu.Unlock()
		prov.Store(u, true)
		return nil
	})
	require.NoError(t, relay.SetStore(store))

	relay.ProcessBatch()

	orderMu.Lock()
	gotOrder := append([]string(nil), order...)
	orderMu.Unlock()
	assert.Equal(t, []string{"propose", "lookup"}, gotOrder, "provisional proposal must happen before Parent lookup on restart")
	assert.Equal(t, RegStatusProvisional, relay.Status(addr).Status)
}

// Reloaded request from SetStore must NOT propose provisional if Parent Chain is locally unconfigured.
func TestProvisionalFirst_RestartReloadWithUnconfiguredParent_NoProvisional(t *testing.T) {
	store := NewKVRelayStore(NewMemoryKVStore())
	kp := bls.GenerateKeyPair()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	addr := crypto.PubkeyToAddress(key.PublicKey)

	parent := newFakeRegParent()
	parent.unconfigured = true
	parent.lookupErr = parentchain.ErrNoNodesConfigured

	relay := NewRegistrationRelay(parent, kp.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), d) },
		func(common.Address) bool { return false })
	relay.alertSink = func(string) {}
	sig, err := crypto.Sign(crypto.Keccak256(relay.RegistrationDigest(addr)), key)
	require.NoError(t, err)
	require.NoError(t, store.Put(&RelayRecord{User: addr, UserSig: sig, Status: RegStatusPending, CreatedAt: 1}))

	proposals := 0
	relay.SetProvisionalProposer(func(common.Address, []byte) error { proposals++; return nil })
	require.NoError(t, relay.SetStore(store))

	relay.ProcessBatch()
	assert.Equal(t, 0, proposals, "unconfigured Parent must block provisional proposal even after SetStore reload")
}

// Concurrent Submit() and ProcessBatch() must never double-propose provisional activation for the same address.
func TestProvisionalFirst_ConcurrentSubmitAndProcessBatch_SingleProposal(t *testing.T) {
	for iter := 0; iter < 20; iter++ {
		h := newRelayHarness()
		h.relay.alertSink = func(string) {}
		user, sig := h.userSig(t)

		entered := make(chan struct{})
		release := make(chan struct{})
		var mu sync.Mutex
		calls := 0
		h.relay.SetProvisionalProposer(func(common.Address, []byte) error {
			mu.Lock()
			calls++
			c := calls
			mu.Unlock()
			if c == 1 {
				select {
				case <-entered:
				default:
					close(entered)
				}
				<-release
			}
			return nil
		})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := h.relay.Submit(user, sig)
			require.NoError(t, err)
		}()
		go func() {
			defer wg.Done()
			<-entered
			h.relay.ProcessBatch()
			close(release)
		}()
		wg.Wait()

		mu.Lock()
		assert.Equal(t, 1, calls, "concurrent Submit and ProcessBatch must propose at most once (iter=%d)", iter)
		mu.Unlock()
	}
}

// afterSend must not burn an extra provWait cooldown step when Parent succeeds.
func TestProvisionalFirst_AfterSendDoesNotDoubleDecrementProvWait(t *testing.T) {
	h := newRelayHarness()
	h.relay.alertSink = func(string) {}
	user, sig := h.userSig(t)
	calls := 0
	h.relay.SetProvisionalProposer(func(common.Address, []byte) error { calls++; return nil })

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	assert.Equal(t, 1, calls)

	// prepareRelay decrements provWait once (30 -> 29); afterSend must NOT decrement it again.
	h.relay.ProcessBatch()
	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.Equal(t, relayProvisionalRetryTicks-1, entry.provWait)
	assert.Equal(t, 1, h.parent.sendCount())
	assert.Equal(t, 1, calls)
}

// When rollup is disabled in app.go (no provisionalProposer/lockProposer wired), Submit and ProcessBatch
// must not propose anything on-chain even if parentClient has a fallback URL.
func TestProvisionalFirst_NoProposerWhenRollupDisabled(t *testing.T) {
	h := newRelayHarness() // neither provisionalProposer nor lockProposer is set
	h.relay.alertSink = func(string) {}
	h.parent.lookupErr = errors.New("connection refused")
	user, sig := h.userSig(t)

	res, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	assert.Equal(t, RegStatusPending, res.Status)

	h.relay.ProcessBatch()
	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.Equal(t, 0, entry.provWait, "provWait must stay 0 when no provisionalProposer is wired")
	assert.Equal(t, RegStatusPending, h.relay.Status(user).Status)
}

// ---------------------------------------------------------------------------------------------------------------
// Watch-only reconciliation (multi-validator lock co-attestation)
// ---------------------------------------------------------------------------------------------------------------

func TestWatchOnly_NeverSendsRegisterAccount(t *testing.T) {
	h := newProvHarness()
	h.relay.maxVerifies = 3
	user, _ := h.userSig(t)
	h.prov.Store(user, true)

	h.relay.EnqueueWatch(user, provTxOf(user))
	for i := 0; i < 10; i++ {
		h.ticks(1)
	}
	assert.Equal(t, 0, h.parent.sendCount(), "watchOnly must never send SendRegisterAccount")
	assert.GreaterOrEqual(t, h.parent.getLookupCount(), 10)
	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.True(t, entry.watchOnly)
	assert.True(t, entry.submitted)
	assert.Empty(t, h.relay.queue, "watchOnly must never enter r.queue")
}

func TestWatchOnly_OwnedByOtherCluster_CoAttestsOnceAndCleansUpOnLock(t *testing.T) {
	h := newProvHarness()
	h.deferLock = true
	otherCluster := bls.GenerateKeyPair().PublicKey()
	user, _ := h.userSig(t)
	h.prov.Store(user, true)
	h.parent.registry[user] = otherCluster

	var attested atomic.Bool
	h.relay.SetLockAlreadyAttested(func(u common.Address, home cm.PublicKey) bool {
		return u == user && home == otherCluster && attested.Load()
	})
	h.relay.SetLockProposer(func(u common.Address, home cm.PublicKey) error {
		h.mu.Lock()
		h.lockCalls++
		h.mu.Unlock()
		attested.Store(true)
		return nil
	})
	h.relay.SetLockedHomeClusterChecker(func(u common.Address) cm.PublicKey {
		if v, ok := h.locked.Load(u); ok && v.(bool) {
			return otherCluster
		}
		return cm.PublicKey{}
	})

	h.relay.EnqueueWatch(user, provTxOf(user))
	// Tick 1: calls lockProposer once and marks attested=true.
	h.ticks(1)
	assert.Equal(t, 1, h.lockProposalCount())
	assert.Equal(t, 0, h.parent.sendCount())

	// Ticks 2..4: HasPendingLockAttestation returns true -> must NOT call lockProposer again.
	h.ticks(3)
	assert.Equal(t, 1, h.lockProposalCount(), "already-attested validator must not spam lock txs")

	// Remaining validators co-attest and on-chain lock commits.
	h.locked.Store(user, true)
	h.prov.Store(user, false)
	h.ticks(1)

	_, tracked := h.relay.snapshot(user)
	assert.False(t, tracked, "watchOnly entry must be deleted once locked on-chain")
	res := h.relay.Status(user)
	assert.Equal(t, RegStatusRejected, res.Status)
	require.NotNil(t, res.HomeCluster)
	assert.Equal(t, otherCluster, *res.HomeCluster)
}

func TestWatchOnly_OwnedByThisCluster_CleanedUpOnConfirmed(t *testing.T) {
	h := newProvHarness()
	h.relay.maxVerifies = 2
	user, _ := h.userSig(t)
	h.prov.Store(user, true)
	h.parent.registry[user] = h.clusterKP.PublicKey()

	h.relay.EnqueueWatch(user, provTxOf(user))
	// First tick sees home == clusterKey; ticks 2..5 must NOT query Parent again while waiting for RegistrationWorker.
	h.ticks(5)
	assert.Equal(t, 0, h.parent.sendCount())
	assert.Equal(t, 1, h.parent.getLookupCount(), "watchOnly must stop polling Parent once home == clusterKey is confirmed")
	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.True(t, entry.submitted)

	// RegistrationWorker confirms on-chain.
	h.flags.Store(user, true)
	h.prov.Store(user, false)
	h.ticks(1)

	_, tracked := h.relay.snapshot(user)
	assert.False(t, tracked, "watchOnly entry must be deleted once confirmed on-chain")
	assert.Equal(t, RegStatusConfirmed, h.relay.Status(user).Status)
}

func TestWatchOnly_PersistedAndReloadedAcrossRestart(t *testing.T) {
	store := NewKVRelayStore(NewMemoryKVStore())
	kp := bls.GenerateKeyPair()
	otherCluster := bls.GenerateKeyPair().PublicKey()
	user := common.HexToAddress("0x4444444444444444444444444444444444444444")

	parent1 := newFakeRegParent()
	relay1 := NewRegistrationRelay(parent1, kp.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), d) },
		func(common.Address) bool { return false })
	relay1.SetProvisionalChecker(func(a common.Address) bool { return a == user })
	require.NoError(t, relay1.SetStore(store))
	relay1.Watch(user, provTxOf(user))

	rec, found, err := store.Get(user)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, rec.WatchOnly)
	assert.True(t, rec.Submitted)

	// Restart into relay2 over the same store.
	parent2 := newFakeRegParent()
	parent2.registry[user] = otherCluster
	relay2 := NewRegistrationRelay(parent2, kp.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), d) },
		func(common.Address) bool { return false })
	relay2.alertSink = func(string) {}
	var locked atomic.Bool
	relay2.SetProvisionalChecker(func(a common.Address) bool { return a == user && !locked.Load() })
	relay2.SetLockedChecker(func(a common.Address) bool { return a == user && locked.Load() })
	lockCalls := 0
	relay2.SetLockProposer(func(u common.Address, home cm.PublicKey) error {
		lockCalls++
		locked.Store(true)
		return nil
	})
	require.NoError(t, relay2.SetStore(store))

	assert.Empty(t, relay2.queue, "reloaded watchOnly entry must not enter r.queue")
	relay2.ProcessBatch()
	assert.Equal(t, 0, parent2.sendCount(), "reloaded watchOnly entry must never call SendRegisterAccount")
	assert.Equal(t, 1, lockCalls)
	_, tracked := relay2.snapshot(user)
	assert.False(t, tracked)
	_, foundAfter, _ := store.Get(user)
	assert.False(t, foundAfter)
}

// provTxOf is a deterministic stand-in for the account_provisional tx hash that activated user.
func provTxOf(u common.Address) common.Hash {
	return common.BytesToHash(append([]byte("prov-tx:"), u.Bytes()...))
}

// committedTxs is a test tx->block mapping: a hash is "committed" once stored.
type committedTxs struct{ m sync.Map }

func (c *committedTxs) commit(h common.Hash)         { c.m.Store(h, true) }
func (c *committedTxs) has(h common.Hash) bool       { _, ok := c.m.Load(h); return ok }
func (c *committedTxs) install(r *RegistrationRelay) { r.SetTxCommittedChecker(c.has) }

// An activation callback from an execution whose tx later commits WITHOUT the flag (rolled back / failed on
// re-execution) is dropped, but only once that tx is in a committed block: never by tick count.
func TestWatchOnly_OrphanDroppedOnlyAfterProvisionalTxCommitted(t *testing.T) {
	h := newProvHarness()
	store := NewKVRelayStore(NewMemoryKVStore())
	require.NoError(t, h.relay.SetStore(store))
	var committed committedTxs
	committed.install(h.relay)
	user, _ := h.userSig(t)
	// h.prov does NOT have user: the flag is not visible in committed state.

	h.relay.EnqueueWatch(user, provTxOf(user))
	h.ticks(50)
	_, tracked := h.relay.snapshot(user)
	assert.True(t, tracked, "must wait while the provisional tx is not committed, however many ticks run")
	_, found, _ := store.Get(user)
	assert.True(t, found, "the persisted watch must not be deleted before commit")
	assert.Equal(t, 0, h.parent.getLookupCount(), "must not query Parent while the flag is absent")

	committed.commit(provTxOf(user)) // the block is committed and the flag is still absent: activation did not apply
	h.ticks(1)
	_, tracked = h.relay.snapshot(user)
	assert.False(t, tracked, "orphan watch must be dropped once its tx is committed without the flag")
	_, found, _ = store.Get(user)
	assert.False(t, found)
	assert.Equal(t, 0, h.parent.getLookupCount())
}

// Without a commit signal (checker not wired, or a watch without a tx hash) an absent flag is never final.
func TestWatchOnly_NoCommitSignalNeverDrops(t *testing.T) {
	h := newProvHarness()
	u1, _ := h.userSig(t)
	u2, _ := h.userSig(t)
	h.relay.EnqueueWatch(u1, provTxOf(u1)) // no checker installed
	h.ticks(30)
	_, tracked := h.relay.snapshot(u1)
	assert.True(t, tracked)

	var committed committedTxs
	committed.install(h.relay)
	h.relay.EnqueueWatch(u2, common.Hash{}) // unknown tx
	committed.commit(provTxOf(u1))
	h.ticks(30)
	_, tracked = h.relay.snapshot(u2)
	assert.True(t, tracked, "a watch without tx hash cannot be proven orphan")
	_, tracked = h.relay.snapshot(u1)
	assert.False(t, tracked, "u1's tx is committed without flag: dropped")
}

// Reviewer repro (ProcessBatch form): several activations in one block are reported during speculative execution,
// many ProcessBatch runs happen before the block commits; no watch may be lost, and after commit the conflicting
// account is co-signed.
func TestWatchOnly_BurstBeforeCommitKeepsWatchesAndCoSignsLock(t *testing.T) {
	h := newProvHarness()
	h.deferLock = true
	store := NewKVRelayStore(NewMemoryKVStore())
	require.NoError(t, h.relay.SetStore(store))
	var committed committedTxs
	committed.install(h.relay)
	otherCluster := bls.GenerateKeyPair().PublicKey()

	users := make([]common.Address, 4)
	for i := range users {
		users[i], _ = h.userSig(t)
		h.relay.EnqueueWatch(users[i], provTxOf(users[i]))
		h.ticks(10) // many batches per event, flag still invisible (block not committed)
	}
	for _, u := range users {
		_, tracked := h.relay.snapshot(u)
		require.True(t, tracked, "watch for %s lost before commit", u.Hex())
	}
	assert.Equal(t, 0, h.parent.getLookupCount())
	assert.Equal(t, 0, h.lockProposalCount())

	// Block commits: flags visible, txs mapped; Parent says users[0] belongs to another cluster.
	for _, u := range users {
		h.prov.Store(u, true)
		committed.commit(provTxOf(u))
	}
	h.parent.mu.Lock()
	h.parent.registry[users[0]] = otherCluster
	h.parent.mu.Unlock()

	h.ticks(1)
	assert.Equal(t, 1, h.lockProposalCount(), "this validator must co-sign the lock of the conflicting account")
	assert.Equal(t, 0, h.parent.sendCount())
	for _, u := range users {
		_, tracked := h.relay.snapshot(u)
		assert.True(t, tracked, "flag now visible: %s must stay watched until confirmed/locked", u.Hex())
	}
}

// Reviewer repro (loop form): EnqueueWatch events 5ms apart with a very fast ticker (many ProcessBatch runs) must not
// drop any watch before the flag shows; once it shows, the lock is co-signed.
func TestWatchOnly_LoopFastTickerBurstDoesNotDropBeforeCommit(t *testing.T) {
	h := newProvHarness()
	h.deferLock = true
	h.relay.interval = time.Millisecond
	var committed committedTxs
	committed.install(h.relay)
	otherCluster := bls.GenerateKeyPair().PublicKey()
	users := make([]common.Address, 4)
	for i := range users {
		users[i], _ = h.userSig(t)
	}

	h.relay.Start()
	defer h.relay.Stop()
	for _, u := range users {
		h.relay.EnqueueWatch(u, provTxOf(u))
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // ~100 ticks with the flags still invisible
	for _, u := range users {
		_, tracked := h.relay.snapshot(u)
		require.True(t, tracked, "watch for %s dropped before commit", u.Hex())
	}

	h.parent.mu.Lock()
	h.parent.registry[users[0]] = otherCluster
	h.parent.mu.Unlock()
	for _, u := range users {
		h.prov.Store(u, true)
		committed.commit(provTxOf(u))
	}
	require.Eventually(t, func() bool { return h.lockProposalCount() >= 1 }, 2*time.Second, 5*time.Millisecond,
		"lock must be co-signed after the flag shows")
	assert.Equal(t, 0, h.parent.sendCount())
}

// A watch event only records the watch: it must not trigger a ProcessBatch (and Parent lookups) per event.
func TestWatchOnly_LoopEventDoesNotTriggerBatch(t *testing.T) {
	h := newProvHarness()
	h.relay.interval = time.Hour
	users := make([]common.Address, 4)
	for i := range users {
		users[i], _ = h.userSig(t)
		h.prov.Store(users[i], true)
	}

	h.relay.Start()
	defer h.relay.Stop()
	for _, u := range users {
		h.relay.EnqueueWatch(u, provTxOf(u))
	}
	require.Eventually(t, func() bool {
		for _, u := range users {
			if _, ok := h.relay.snapshot(u); !ok {
				return false
			}
		}
		return true
	}, 2*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, h.parent.getLookupCount(), "watch events must not run a batch")
}

// A later activation of an already-watched address (e.g. the first account_provisional tx failed) replaces its tx:
// only the latest tx decides whether the watch is an orphan. The tx survives a restart.
func TestWatchOnly_WatchTxReplacedAndPersisted(t *testing.T) {
	store := NewKVRelayStore(NewMemoryKVStore())
	kp := bls.GenerateKeyPair()
	user := common.HexToAddress("0x5555555555555555555555555555555555555555")
	tx1 := common.HexToHash("0x01")
	tx2 := common.HexToHash("0x02")

	relay1 := NewRegistrationRelay(newFakeRegParent(), kp.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), d) },
		func(common.Address) bool { return false })
	require.NoError(t, relay1.SetStore(store))
	relay1.Watch(user, tx1)
	relay1.Watch(user, tx2)
	e, ok := relay1.snapshot(user)
	require.True(t, ok)
	assert.Equal(t, tx2, e.watchTx)
	rec, found, err := store.Get(user)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, tx2, rec.WatchTx)

	// Restart: the watch tx is reloaded; tx1 committed does not drop it, tx2 committed (flag absent) does.
	relay2 := NewRegistrationRelay(newFakeRegParent(), kp.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), d) },
		func(common.Address) bool { return false })
	relay2.alertSink = func(string) {}
	relay2.SetProvisionalChecker(func(common.Address) bool { return false })
	var committed committedTxs
	committed.install(relay2)
	require.NoError(t, relay2.SetStore(store))
	e, ok = relay2.snapshot(user)
	require.True(t, ok)
	assert.Equal(t, tx2, e.watchTx)

	committed.commit(tx1)
	relay2.ProcessBatch()
	_, ok = relay2.snapshot(user)
	assert.True(t, ok, "a superseded tx must not decide the watch")

	committed.commit(tx2)
	relay2.ProcessBatch()
	_, ok = relay2.snapshot(user)
	assert.False(t, ok)
	_, found, _ = store.Get(user)
	assert.False(t, found)
}

func TestWatchOnly_DoesNotOverwriteRealEntry(t *testing.T) {
	h := newProvHarness()
	user, sig := h.userSig(t)
	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)

	h.relay.Watch(user, provTxOf(user))
	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.False(t, entry.watchOnly, "Watch must not overwrite an existing RPC-submitted entry")
}

func TestWatchOnly_LockProposerErrorRetriesWithoutEnteringQueue(t *testing.T) {
	h := newProvHarness()
	otherCluster := bls.GenerateKeyPair().PublicKey()
	user, _ := h.userSig(t)
	h.prov.Store(user, true)
	h.parent.registry[user] = otherCluster
	h.lockErr = errors.New("tx pool busy")

	h.relay.EnqueueWatch(user, provTxOf(user))
	h.ticks(1)
	assert.Equal(t, 1, h.lockProposalCount())
	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.True(t, entry.submitted)
	assert.Equal(t, 0, entry.attempts, "watchOnly lock error must not burn attempts")
	assert.Empty(t, h.relay.queue, "watchOnly must never enter r.queue")

	// Clear lock error -> next tick retries lock and succeeds.
	h.mu.Lock()
	h.lockErr = nil
	h.mu.Unlock()
	h.ticks(1)
	assert.Equal(t, 2, h.lockProposalCount())
	_, tracked := h.relay.snapshot(user)
	assert.False(t, tracked)
	assert.Equal(t, RegStatusRejected, h.relay.Status(user).Status)
}

func TestWatchOnly_ParentOfflineRetriesAndRecovers(t *testing.T) {
	h := newProvHarness()
	otherCluster := bls.GenerateKeyPair().PublicKey()
	user, _ := h.userSig(t)
	h.prov.Store(user, true)
	h.parent.lookupErr = errors.New("parent timeout")

	h.relay.EnqueueWatch(user, provTxOf(user))
	h.ticks(25)
	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.True(t, entry.watchOnly)
	assert.True(t, entry.submitted)
	assert.Equal(t, 0, entry.attempts)
	assert.Equal(t, 0, h.parent.sendCount())

	// Parent recovers and reveals another cluster owns user.
	h.parent.mu.Lock()
	h.parent.lookupErr = nil
	h.parent.registry[user] = otherCluster
	h.parent.mu.Unlock()

	h.ticks(1)
	assert.Equal(t, 1, h.lockProposalCount())
	assert.Equal(t, 0, h.parent.sendCount())
	assert.Equal(t, RegStatusRejected, h.relay.Status(user).Status)
}

func TestWatchOnly_StartLoopReactsToEnqueueWatch(t *testing.T) {
	h := newProvHarness()
	h.relay.interval = 50 * time.Millisecond
	otherCluster := bls.GenerateKeyPair().PublicKey()
	user, _ := h.userSig(t)
	h.prov.Store(user, true)
	h.parent.registry[user] = otherCluster

	h.relay.Start()
	defer h.relay.Stop()

	h.relay.EnqueueWatch(user, provTxOf(user))
	require.Eventually(t, func() bool {
		return h.relay.Status(user).Status == RegStatusRejected && h.lockProposalCount() >= 1
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 0, h.parent.sendCount())
}

func TestWatchOnly_SeparateCapacityDoesNotBlockSubmit(t *testing.T) {
	h := newProvHarness()
	h.relay.maxTracked = 2
	h.relay.maxWatched = 3
	store := NewKVRelayStore(NewMemoryKVStore())
	require.NoError(t, h.relay.SetStore(store))

	// Fill all 3 watch slots (plus a 4th overflow Watch that must be dropped without failing).
	var watchedUsers []common.Address
	for i := 0; i < 4; i++ {
		w, _ := h.userSig(t)
		h.prov.Store(w, true)
		h.relay.Watch(w, provTxOf(w))
		watchedUsers = append(watchedUsers, w)
	}
	_, watchedCount := func() (int, int) {
		h.relay.mu.Lock()
		defer h.relay.mu.Unlock()
		return h.relay.countTrackedLocked()
	}()
	assert.Equal(t, 3, watchedCount, "watch table must enforce maxWatched=3")
	_, overflowTracked := h.relay.snapshot(watchedUsers[3])
	assert.False(t, overflowTracked)

	// Real Submit() still has its own maxTracked=2 budget completely unaffected by watchOnly entries.
	u1, s1 := h.userSig(t)
	_, err := h.relay.Submit(u1, s1)
	require.NoError(t, err, "full watch table must not cause ErrRegistrationBusy for Submit")
	u2, s2 := h.userSig(t)
	_, err = h.relay.Submit(u2, s2)
	require.NoError(t, err)

	// 3rd real Submit exceeds maxTracked=2 -> ErrRegistrationBusy.
	u3, s3 := h.userSig(t)
	_, err = h.relay.Submit(u3, s3)
	assert.ErrorIs(t, err, ErrRegistrationBusy)

	// Verify SetStore reload also keeps maxTracked and maxWatched budgets separate.
	reloaded := NewRegistrationRelay(h.parent, h.clusterKP.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(h.clusterKP.PrivateKey(), d) },
		func(common.Address) bool { return false })
	reloaded.maxTracked = 2
	reloaded.maxWatched = 3
	require.NoError(t, reloaded.SetStore(store))
	reloaded.mu.Lock()
	ownedAfter, watchedAfter := reloaded.countTrackedLocked()
	reloaded.mu.Unlock()
	assert.Equal(t, 2, ownedAfter)
	assert.Equal(t, 3, watchedAfter)
}

func TestWatchOnly_FullChannelDoesNotBlock(t *testing.T) {
	h := newProvHarness()
	h.relay.watchCh = make(chan watchEvent, 2)
	for i := 0; i < 5; i++ {
		u, _ := h.userSig(t)
		h.relay.EnqueueWatch(u, provTxOf(u)) // must return immediately without blocking even when watchCh is full
	}
	assert.Equal(t, 2, len(h.relay.watchCh))
}

// A watch that arrives while the watch table is full is persisted, not lost: the provisional callback fires only once,
// so the stall sweep must load it once a slot frees up and this validator can still co-sign the lock.
func TestWatchOnly_OverflowIsPersistedAndLoadedWhenSlotFrees(t *testing.T) {
	h := newProvHarness()
	h.relay.maxWatched = 1
	store := NewKVRelayStore(NewMemoryKVStore())
	require.NoError(t, h.relay.SetStore(store))

	first, _ := h.userSig(t)
	second, _ := h.userSig(t)
	h.prov.Store(first, true)
	h.prov.Store(second, true)
	h.relay.Watch(first, provTxOf(first))
	h.relay.Watch(second, provTxOf(second)) // table full

	_, inMemory := h.relay.snapshot(second)
	assert.False(t, inMemory, "overflow watch must not exceed maxWatched in memory")
	rec, found, err := store.Get(second)
	require.NoError(t, err)
	require.True(t, found, "overflow watch must be persisted")
	assert.True(t, rec.WatchOnly)
	assert.Equal(t, provTxOf(second), rec.WatchTx, "the deferred watch keeps its provisional tx")

	// First watch finishes (e.g. locked or confirmed) and frees its slot; the next tick loads the deferred one.
	h.relay.deleteEntry(first)
	h.relay.ProcessBatch()
	e, ok := h.relay.snapshot(second)
	require.True(t, ok, "deferred watch must be loaded once a slot frees up")
	assert.True(t, e.watchOnly)
	assert.Equal(t, 0, h.parent.sendCount(), "loaded watch must never send a registration")
}
