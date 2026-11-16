package rollup

import (
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

// provHarness extends relayHarness with a simulated on-chain Provisional flag (set when the provisional proposal
// "commits") and captures every Telegram message instead of sending it. alertInterval is 0, so every ProcessBatch
// that recorded alerts emits exactly one aggregated message.
type provHarness struct {
	*relayHarness
	prov        sync.Map // addr -> bool: on-chain Provisional flag
	mu          sync.Mutex
	msgs        []string
	proposals   int
	proposerErr error
}

func newProvHarness() *provHarness {
	h := &provHarness{relayHarness: newRelayHarness()}
	h.relay.SetProvisionalChecker(func(a common.Address) bool { v, ok := h.prov.Load(a); return ok && v.(bool) })
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

// A failing provisional proposal (e.g. tx pool busy) is retried on the next tick.
func TestProvisionalAlert_ProposerErrorIsRetried(t *testing.T) {
	h := newProvHarness()
	h.proposerErr = errors.New("tx pool busy")
	user, sig := h.userSig(t)
	h.parent.lookupErr = errors.New("parent chain offline")
	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)

	h.ticks(1)
	assert.Equal(t, 1, h.proposalCount())
	assert.Equal(t, RegStatusPending, h.relay.Status(user).Status)

	h.mu.Lock()
	h.proposerErr = nil
	h.mu.Unlock()
	h.ticks(1)
	assert.Equal(t, 2, h.proposalCount())
	assert.Equal(t, RegStatusProvisional, h.relay.Status(user).Status)
}

// Address already owned by another cluster on the parent: never activated provisionally, conflict alerted.
func TestProvisionalAlert_OwnedByOtherCluster_NoProvisionalAndConflictAlert(t *testing.T) {
	h := newProvHarness()
	user, sig := h.userSig(t)
	h.parent.registry[user] = bls.GenerateKeyPair().PublicKey()

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	h.ticks(1)

	assert.Equal(t, 0, h.proposalCount(), "must not activate an address owned by another cluster")
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
		"NoNodes": func(p *fakeRegParent) { p.lookupErr = parentchain.ErrNoNodesConfigured },
		"PrivateKey": func(p *fakeRegParent) {
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
	h.parent.lookupErr = errors.New("parent chain offline")
	const n = 10
	for i := 0; i < n; i++ {
		u, sig := h.userSig(t)
		_, err := h.relay.Submit(u, sig)
		require.NoError(t, err)
	}
	h.ticks(50)
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
