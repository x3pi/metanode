package rollup

import (
	"errors"
	"math/big"
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

// fakeRegParent is a scripted Parent Chain: registry lookups and submissions are recorded and can fail or be redirected.
type fakeRegParent struct {
	mu        sync.Mutex
	registry  map[common.Address]cm.PublicKey
	sends     []fakeSend
	sendErr   error
	lookupErr error
	// onSend, if set, runs inside SendRegisterAccount (e.g. to make another cluster win the race).
	onSend func(user common.Address)
}

type fakeSend struct {
	user       common.Address
	clusterKey cm.PublicKey
	userSig    []byte
	clusterSig cm.Sign
}

func newFakeRegParent() *fakeRegParent {
	return &fakeRegParent{registry: map[common.Address]cm.PublicKey{}}
}

func (f *fakeRegParent) SendRegisterAccount(user common.Address, key cm.PublicKey, userSig []byte, clusterSig cm.Sign) (common.Hash, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return common.Hash{}, f.sendErr
	}
	f.sends = append(f.sends, fakeSend{user, key, append([]byte(nil), userSig...), clusterSig})
	if f.onSend != nil {
		f.onSend(user)
	}
	return common.HexToHash("0x01"), nil
}

func (f *fakeRegParent) GetAccountRegistry(user common.Address) (cm.PublicKey, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lookupErr != nil {
		return cm.PublicKey{}, false, f.lookupErr
	}
	k, ok := f.registry[user]
	return k, ok, nil
}

func (f *fakeRegParent) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

type relayHarness struct {
	relay     *RegistrationRelay
	parent    *fakeRegParent
	clusterKP *bls.KeyPair
	flags     sync.Map // addr -> bool: the on-chain ParentRegistered flag
}

func newRelayHarness() *relayHarness {
	h := &relayHarness{parent: newFakeRegParent(), clusterKP: bls.GenerateKeyPair()}
	h.relay = NewRegistrationRelay(h.parent, h.clusterKP.PublicKey(),
		func(digest []byte) cm.Sign { return bls.Sign(h.clusterKP.PrivateKey(), digest) },
		func(a common.Address) bool { v, ok := h.flags.Load(a); return ok && v.(bool) })
	return h
}

// userSig signs, like a real wallet/SDK, keccak256(RegistrationDigest(user)) with the user's secp256k1 key.
func (h *relayHarness) userSig(t *testing.T) (common.Address, []byte) {
	t.Helper()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	addr := crypto.PubkeyToAddress(key.PublicKey)
	sig, err := crypto.Sign(crypto.Keccak256(h.relay.RegistrationDigest(addr)), key)
	require.NoError(t, err)
	return addr, sig
}

func TestRegistrationRelay_RejectsBadSignatures(t *testing.T) {
	h := newRelayHarness()
	addr, sig := h.userSig(t)

	// wrong length
	_, err := h.relay.Submit(addr, sig[:64])
	assert.ErrorIs(t, err, ErrInvalidRegistrationSig)
	// signature of a DIFFERENT address
	other, otherSig := h.userSig(t)
	_, err = h.relay.Submit(addr, otherSig)
	assert.ErrorIs(t, err, ErrInvalidRegistrationSig)
	_ = other
	// garbage
	_, err = h.relay.Submit(addr, make([]byte, 65))
	assert.ErrorIs(t, err, ErrInvalidRegistrationSig)
	// signature over a different cluster's message (another cluster key) must not verify here
	key, _ := crypto.GenerateKey()
	a2 := crypto.PubkeyToAddress(key.PublicKey)
	foreign := NewRegistrationRelay(h.parent, bls.GenerateKeyPair().PublicKey(), nil, nil)
	wrongCluster, _ := crypto.Sign(crypto.Keccak256(foreign.RegistrationDigest(a2)), key)
	_, err = h.relay.Submit(a2, wrongCluster)
	assert.ErrorIs(t, err, ErrInvalidRegistrationSig, "a signature for another cluster must be rejected")
	// empty address
	_, err = h.relay.Submit(common.Address{}, sig)
	assert.Error(t, err)

	h.relay.ProcessBatch()
	assert.Equal(t, 0, h.parent.sendCount(), "nothing invalid may reach the parent")
	assert.Equal(t, RegStatusNone, h.relay.Status(addr).Status)
}

func TestRegistrationRelay_AcceptsEthereumRecoveryIDForm(t *testing.T) {
	h := newRelayHarness()
	addr, sig := h.userSig(t)
	sig27 := append([]byte(nil), sig...)
	sig27[64] += 27
	res, err := h.relay.Submit(addr, sig27)
	require.NoError(t, err)
	assert.Equal(t, RegStatusPending, res.Status)
}

// The full automatic path: submit -> relay signs and sends exactly once -> parent registers this cluster -> the
// on-chain flag arrives -> CONFIRMED, with the user doing nothing besides the first request.
func TestRegistrationRelay_AutomaticPathToConfirmed(t *testing.T) {
	h := newRelayHarness()
	addr, sig := h.userSig(t)

	res, err := h.relay.Submit(addr, sig)
	require.NoError(t, err)
	assert.Equal(t, RegStatusPending, res.Status)

	h.relay.ProcessBatch()
	require.Equal(t, 1, h.parent.sendCount())
	send := h.parent.sends[0]
	assert.Equal(t, addr, send.user)
	assert.Equal(t, h.clusterKP.PublicKey(), send.clusterKey)
	assert.Equal(t, sig, send.userSig, "the user's signature is forwarded untouched")
	assert.True(t, bls.VerifySign(h.clusterKP.PublicKey(), send.clusterSig, h.relay.RegistrationDigest(addr)),
		"the cluster signature must verify against the registration digest")
	assert.Equal(t, RegStatusPending, h.relay.Status(addr).Status, "still pending until the parent answers and the flag is set")

	// parent executes it: registry now names THIS cluster
	h.parent.mu.Lock()
	h.parent.registry[addr] = h.clusterKP.PublicKey()
	h.parent.mu.Unlock()
	h.relay.ProcessBatch()
	assert.Equal(t, RegStatusPending, h.relay.Status(addr).Status, "parent chose us, but the on-chain flag is not set yet")

	// the RegistrationWorker applies the ordered event
	h.flags.Store(addr, true)
	assert.Equal(t, RegStatusConfirmed, h.relay.Status(addr).Status)
	assert.Equal(t, 1, h.parent.sendCount(), "no duplicate submission")
}

func TestRegistrationRelay_SubmitIsIdempotent(t *testing.T) {
	h := newRelayHarness()
	addr, sig := h.userSig(t)
	for i := 0; i < 5; i++ {
		res, err := h.relay.Submit(addr, sig)
		require.NoError(t, err)
		assert.Equal(t, RegStatusPending, res.Status)
	}
	h.relay.ProcessBatch()
	h.relay.ProcessBatch()
	assert.Equal(t, 1, h.parent.sendCount(), "repeated submits must produce a single parent submission")

	// an already-confirmed address is answered from the on-chain flag without touching the parent
	h.flags.Store(addr, true)
	res, err := h.relay.Submit(addr, sig)
	require.NoError(t, err)
	assert.Equal(t, RegStatusConfirmed, res.Status)
	assert.Equal(t, 1, h.parent.sendCount())
}

// The address already belongs to another cluster on the parent: no submission at all, REJECTED with the winner's key.
func TestRegistrationRelay_AlreadyRegisteredElsewhere_IsRejectedWithoutSending(t *testing.T) {
	h := newRelayHarness()
	addr, sig := h.userSig(t)
	winner := bls.GenerateKeyPair().PublicKey()
	h.parent.registry[addr] = winner

	_, err := h.relay.Submit(addr, sig)
	require.NoError(t, err)
	h.relay.ProcessBatch()

	assert.Equal(t, 0, h.parent.sendCount())
	res := h.relay.Status(addr)
	assert.Equal(t, RegStatusRejected, res.Status)
	require.NotNil(t, res.HomeCluster)
	assert.Equal(t, winner, *res.HomeCluster)

	// REJECTED is sticky: resubmitting does not re-queue anything
	res2, err := h.relay.Submit(addr, sig)
	require.NoError(t, err)
	assert.Equal(t, RegStatusRejected, res2.Status)
	h.relay.ProcessBatch()
	assert.Equal(t, 0, h.parent.sendCount())
}

// Race: both clusters submit; the other one lands first. We only learn it from the registry after our own submission.
func TestRegistrationRelay_LosesTheRaceAfterSubmitting(t *testing.T) {
	h := newRelayHarness()
	addr, sig := h.userSig(t)
	winner := bls.GenerateKeyPair().PublicKey()
	h.parent.onSend = func(user common.Address) { h.parent.registry[user] = winner } // winner executes first

	_, err := h.relay.Submit(addr, sig)
	require.NoError(t, err)
	h.relay.ProcessBatch() // sends
	require.Equal(t, 1, h.parent.sendCount())
	h.relay.ProcessBatch() // verifies

	res := h.relay.Status(addr)
	assert.Equal(t, RegStatusRejected, res.Status)
	require.NotNil(t, res.HomeCluster)
	assert.Equal(t, winner, *res.HomeCluster)
}

func TestRegistrationRelay_LockedAccountReportsDurableHomeCluster(t *testing.T) {
	h := newRelayHarness()
	addr, _ := h.userSig(t)
	winner := bls.GenerateKeyPair().PublicKey()
	var locked atomic.Bool

	h.relay.SetLockedChecker(func(common.Address) bool { return locked.Load() })
	h.relay.SetLockedHomeClusterChecker(func(common.Address) cm.PublicKey { return winner })
	locked.Store(true)

	// This simulates a restart: there is no in-memory relay entry, so the
	// response must come exclusively from durable account state.
	res := h.relay.Status(addr)
	assert.Equal(t, RegStatusRejected, res.Status)
	require.NotNil(t, res.HomeCluster)
	assert.Equal(t, winner, *res.HomeCluster)
}

func TestRegistrationRelay_ParentFailureRetriesThenFailsThenAllowsResubmit(t *testing.T) {
	h := newRelayHarness()
	h.relay.maxAttempts = 3
	addr, sig := h.userSig(t)
	h.parent.sendErr = errors.New("parent unreachable")

	_, err := h.relay.Submit(addr, sig)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		h.relay.ProcessBatch()
		assert.Equal(t, RegStatusPending, h.relay.Status(addr).Status, "still retrying after attempt %d", i+1)
	}
	h.relay.ProcessBatch() // third failure exhausts the attempts
	res := h.relay.Status(addr)
	assert.Equal(t, RegStatusFailed, res.Status)
	assert.Contains(t, res.Reason, "parent unreachable")
	assert.Equal(t, 0, h.parent.sendCount())

	// parent recovers; the user resubmits and it goes through
	h.parent.mu.Lock()
	h.parent.sendErr = nil
	h.parent.mu.Unlock()
	res, err = h.relay.Submit(addr, sig)
	require.NoError(t, err)
	assert.Equal(t, RegStatusPending, res.Status)
	h.relay.ProcessBatch()
	assert.Equal(t, 1, h.parent.sendCount())
}

func TestRegistrationRelay_RegistryLookupErrorIsRetried(t *testing.T) {
	h := newRelayHarness()
	addr, sig := h.userSig(t)
	h.parent.lookupErr = errors.New("registry unavailable")
	_, err := h.relay.Submit(addr, sig)
	require.NoError(t, err)
	h.relay.ProcessBatch()
	assert.Equal(t, RegStatusPending, h.relay.Status(addr).Status)
	assert.Equal(t, 0, h.parent.sendCount(), "never submit blind while the registry cannot be consulted")

	h.parent.mu.Lock()
	h.parent.lookupErr = nil
	h.parent.mu.Unlock()
	h.relay.ProcessBatch()
	assert.Equal(t, 1, h.parent.sendCount())
}

func TestRegistrationRelay_BoundsAreEnforced(t *testing.T) {
	// tracked-request table
	h := newRelayHarness()
	h.relay.maxTracked = 3
	for i := 0; i < 3; i++ {
		a, s := h.userSig(t)
		_, err := h.relay.Submit(a, s)
		require.NoError(t, err)
	}
	a, s := h.userSig(t)
	_, err := h.relay.Submit(a, s)
	assert.ErrorIs(t, err, ErrRegistrationBusy)
	assert.Equal(t, RegStatusNone, h.relay.Status(a).Status, "a rejected-as-busy request leaves no state behind")

	// queue length
	h2 := newRelayHarness()
	h2.relay.queue = make(chan common.Address, 2)
	for i := 0; i < 2; i++ {
		a, s := h2.userSig(t)
		_, err := h2.relay.Submit(a, s)
		require.NoError(t, err)
	}
	a2, s2 := h2.userSig(t)
	_, err = h2.relay.Submit(a2, s2)
	assert.ErrorIs(t, err, ErrRegistrationBusy)
	assert.Equal(t, RegStatusNone, h2.relay.Status(a2).Status)
}

// A relay restart loses only the in-memory queue: the on-chain flag still answers, and the user can resubmit.
func TestRegistrationRelay_RestartKeepsConfirmedAndAllowsResubmit(t *testing.T) {
	h := newRelayHarness()
	addr, sig := h.userSig(t)
	h.flags.Store(addr, true)
	assert.Equal(t, RegStatusConfirmed, h.relay.Status(addr).Status)

	pending, psig := h.userSig(t)
	fresh := NewRegistrationRelay(h.parent, h.clusterKP.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(h.clusterKP.PrivateKey(), d) },
		func(a common.Address) bool { v, ok := h.flags.Load(a); return ok && v.(bool) })
	assert.Equal(t, RegStatusConfirmed, fresh.Status(addr).Status)
	assert.Equal(t, RegStatusNone, fresh.Status(pending).Status, "unconfirmed requests are not durable across a restart")
	res, err := fresh.Submit(pending, psig)
	require.NoError(t, err)
	assert.Equal(t, RegStatusPending, res.Status)
	_ = sig
}

func TestRegistrationRelay_StopIsIdempotentAndLoopProcesses(t *testing.T) {
	h := newRelayHarness()
	h.relay.interval = 5 * 1000 * 1000 // 5ms
	h.relay.Start()
	addr, sig := h.userSig(t)
	_, err := h.relay.Submit(addr, sig)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return h.parent.sendCount() == 1 }, 3*1000*1000*1000, 5*1000*1000)
	h.relay.Stop()
	h.relay.Stop() // must not panic
}

func TestRegistrationRelay_ConcurrentSubmitsAreSafe(t *testing.T) {
	h := newRelayHarness()
	h.relay.maxTracked = 10_000
	h.relay.queue = make(chan common.Address, 10_000)
	var wg sync.WaitGroup
	var ok atomic.Int64
	addr, sig := h.userSig(t)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.relay.Submit(addr, sig); err == nil { // same address from many goroutines
				ok.Add(1)
			}
			_ = h.relay.Status(addr)
		}()
	}
	wg.Wait()
	assert.Equal(t, int64(64), ok.Load())
	h.relay.ProcessBatch()
	assert.Equal(t, 1, h.parent.sendCount(), "64 concurrent submits for one address must send exactly once")
}

func TestRegistrationRelay_DurableQueueSurvivesRestartAndReplays(t *testing.T) {
	parent := newFakeRegParent()
	kp := bls.GenerateKeyPair()
	var flags sync.Map

	registeredFn := func(a common.Address) bool {
		v, ok := flags.Load(a)
		return ok && v.(bool)
	}

	kv := NewMemoryKVStore()
	store := NewKVRelayStore(kv)

	// Instance 1: submits request while relay is not processing batches yet
	relay1 := NewRegistrationRelay(parent, kp.PublicKey(),
		func(digest []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), digest) },
		registeredFn)
	require.NoError(t, relay1.SetStore(store))

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	addr := crypto.PubkeyToAddress(key.PublicKey)
	sig, err := crypto.Sign(crypto.Keccak256(relay1.RegistrationDigest(addr)), key)
	require.NoError(t, err)

	res1, err := relay1.Submit(addr, sig)
	require.NoError(t, err)
	assert.Equal(t, RegStatusPending, res1.Status)

	// Simulate "kill -9" / crash: relay1 is dropped without processing
	relay1 = nil

	// Instance 2: restarts using the same durable store
	relay2 := NewRegistrationRelay(parent, kp.PublicKey(),
		func(digest []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), digest) },
		registeredFn)
	require.NoError(t, relay2.SetStore(store))

	// Status before processing must be PENDING (recovered from durable store)
	res2 := relay2.Status(addr)
	assert.Equal(t, RegStatusPending, res2.Status)

	// Now relay2 processes the re-queued batch: relays to parent
	relay2.ProcessBatch()
	assert.Equal(t, 1, parent.sendCount(), "relay2 must submit reloaded request to parent")

	// Parent registers the account to this cluster
	parent.mu.Lock()
	parent.registry[addr] = kp.PublicKey()
	parent.mu.Unlock()

	// Verify phase: parent confirms
	relay2.ProcessBatch()

	// On-chain flag is set (e.g. by RegistrationWorker)
	flags.Store(addr, true)

	// Final status is CONFIRMED
	assert.Equal(t, RegStatusConfirmed, relay2.Status(addr).Status)

	// Idempotent: submitting again returns CONFIRMED
	res3, err := relay2.Submit(addr, sig)
	require.NoError(t, err)
	assert.Equal(t, RegStatusConfirmed, res3.Status)
}

func TestRegistrationRelay_ReloadDropsRegisteredAndBoundsMemory(t *testing.T) {
	kv := NewMemoryKVStore()
	store := NewKVRelayStore(kv)
	registered := map[common.Address]bool{}
	for i := 0; i < 6; i++ {
		u := common.BigToAddress(big.NewInt(int64(i + 1)))
		require.NoError(t, store.Put(&RelayRecord{User: u, UserSig: []byte{1}, Status: RegStatusPending}))
		if i < 2 {
			registered[u] = true
		}
	}
	r := NewRegistrationRelay(nil, cm.PublicKey{}, func([]byte) cm.Sign { return cm.Sign{} }, func(a common.Address) bool { return registered[a] })
	r.maxTracked = 3
	require.NoError(t, r.SetStore(store))
	require.Len(t, r.entries, 3, "memory stays bounded")
	left, _ := store.ScanAll()
	require.Len(t, left, 4, "records of already-registered users are deleted from the store")
	for _, rec := range left {
		require.False(t, registered[rec.User])
	}
	// a record that did not fit in memory is still answered (lazy load)
	for _, rec := range left {
		require.Equal(t, RegStatusPending, r.Status(rec.User).Status)
	}
}

func TestRegistrationRelay_PendingStats(t *testing.T) {
	h := newRelayHarness()

	// Initial empty stats
	cnt, maxAge := h.relay.PendingStats()
	require.Equal(t, 0, cnt)
	require.Equal(t, float64(0), maxAge)

	addr, sig := h.userSig(t)
	res, err := h.relay.Submit(addr, sig)
	require.NoError(t, err)
	require.Equal(t, RegStatusPending, res.Status)

	time.Sleep(10 * time.Millisecond)
	cnt, maxAge = h.relay.PendingStats()
	require.Equal(t, 1, cnt)
	require.Greater(t, maxAge, float64(0.005))
}

func TestRegistrationRelay_LockProposalFailureRetriesAndDoesNotMarkRejected(t *testing.T) {
	h := newRelayHarness()
	addr, sig := h.userSig(t)
	winner := bls.GenerateKeyPair().PublicKey()
	h.parent.registry[addr] = winner // parent confirmed another cluster

	var lockAttempts int
	h.relay.SetLockProposer(func(user common.Address, home cm.PublicKey) error {
		lockAttempts++
		if lockAttempts == 1 {
			return errors.New("temporary txpool full")
		}
		return nil
	})

	_, err := h.relay.Submit(addr, sig)
	require.NoError(t, err)

	// First verify: lockProposer returns error, must NOT be marked Rejected immediately
	h.relay.ProcessBatch()
	assert.Equal(t, 1, lockAttempts)
	assert.Equal(t, RegStatusPending, h.relay.Status(addr).Status, "status must remain pending/retrying")

	// Second verify: lockProposer succeeds, now marked Rejected
	h.relay.ProcessBatch()
	assert.Equal(t, 2, lockAttempts)
	res := h.relay.Status(addr)
	assert.Equal(t, RegStatusRejected, res.Status)
	require.NotNil(t, res.HomeCluster)
	assert.Equal(t, winner, *res.HomeCluster)
}

// Exercise asynchronous proposal acceptance separately from committee execution.
func TestRegistrationRelay_WaitsForCommitteeLockExecution(t *testing.T) {
	h := newRelayHarness()
	user, sig := h.userSig(t)
	winner := bls.GenerateKeyPair().PublicKey()
	h.parent.registry[user] = winner
	db := newCoattestMockStateDB()
	handler := NewAccountRegistryHandler(h.clusterKP.PublicKey())
	handler.SetChainID(991)
	vals := []*bls.KeyPair{bls.GenerateKeyPair(), bls.GenerateKeyPair(), bls.GenerateKeyPair(), bls.GenerateKeyPair()}
	committee := &mockCommitteeProvider{}
	for _, v := range vals {
		committee.validators = append(committee.validators, v.PublicKey())
	}
	store := NewDBAttestationStore(newMemSCDB())
	h.relay.SetLockedChecker(db.GetLocked)
	h.relay.SetProvisionalChecker(db.GetProvisional)
	makePayload := func(v *bls.KeyPair) []byte {
		digest := ComputeAccountLockedAttestDigest(991, user, winner, 0)
		p := AccountRegistrationPayload{Kind: SystemPayloadKindAccountLocked, User: user, HomeCluster: winner,
			Attestations: []RegistrationAttestation{{ValidatorPubkey: v.PublicKey(), Signature: bls.Sign(v.PrivateKey(), digest)}}}
		data, err := p.MarshalProto()
		require.NoError(t, err)
		return data
	}
	attempts := 0
	var queued []byte
	h.relay.SetLockProposer(func(u common.Address, home cm.PublicKey) error {
		require.Equal(t, user, u)
		require.Equal(t, winner, home)
		attempts++
		queued = makePayload(vals[0])
		return nil // Accepted by pool; execution has not happened yet.
	})
	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	db.SetProvisional(user, true)
	h.relay.ProcessBatch()
	require.Positive(t, attempts)
	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	require.NotEqual(t, RegStatusRejected, entry.status)
	require.True(t, entry.submitted)
	// An execution rejection must not stop reconciliation.
	require.Error(t, handler.ApplyAttested(db, store, &mockCommitteeProvider{}, queued))
	before := attempts
	h.relay.ProcessBatch()
	require.Greater(t, attempts, before)
	// A valid validator key distinct from the cluster key is accepted, but one share is insufficient.
	require.NoError(t, handler.ApplyAttested(db, store, committee, queued))
	require.False(t, db.GetLocked(user))
	h.relay.ProcessBatch()
	entry, _ = h.relay.snapshot(user)
	require.NotEqual(t, RegStatusRejected, entry.status)
	require.NoError(t, handler.ApplyAttested(db, store, committee, makePayload(vals[1])))
	require.True(t, db.GetLocked(user))
	before = attempts
	h.relay.ProcessBatch()
	require.Equal(t, before, attempts, "do not propose again after lock is committed")
	entry, _ = h.relay.snapshot(user)
	require.Equal(t, RegStatusRejected, entry.status)
	require.NotNil(t, entry.home)
	require.Equal(t, winner, *entry.home)
}

func TestReview3_BugA_DuplicateProvisionalProposalsEveryTick(t *testing.T) {
	h := newRelayHarness()
	user, sig := h.userSig(t)
	h.parent.lookupErr = errors.New("parent chain offline")

	provisionalCalls := 0
	h.relay.SetProvisionalProposer(func(u common.Address, us []byte) error {
		require.Equal(t, user, u)
		provisionalCalls++
		return nil
	})
	isProv := true
	h.relay.SetProvisionalChecker(func(a common.Address) bool {
		return a == user && isProv
	})

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)

	for i := 0; i < 10; i++ {
		h.relay.ProcessBatch()
	}

	assert.Equal(t, 0, provisionalCalls, "provisionalProposer must not be called when account is already provisional on-chain")
}

func TestReview3_BugB_ProvisionalSubmittedNeverResubmitsAfterMaxVerifies(t *testing.T) {
	h := newRelayHarness()
	h.relay.maxVerifies = 5
	user, sig := h.userSig(t)

	isProv := false
	h.relay.SetProvisionalChecker(func(a common.Address) bool {
		return a == user && isProv
	})

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)

	// Tick 1: ProcessBatch sends SendRegisterAccount (submitted=true)
	h.relay.ProcessBatch()
	assert.Equal(t, 1, h.parent.sendCount())

	// Account becomes provisional on-chain
	isProv = true

	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.True(t, entry.submitted)

	// Now simulate parent dropped tx from mempool (so found=false, err=nil).
	// Run past maxVerifies (e.g. 10 ticks).
	for i := 0; i < 10; i++ {
		h.relay.ProcessBatch()
	}

	// Because of R2 fix, it should have reset submitted=false and resubmitted SendRegisterAccount!
	assert.Greater(t, h.parent.sendCount(), 1, "SendRegisterAccount must be retried after maxVerifies when tx dropped on parent")
}

func TestReview3_S3_LocalConfigErrorsAndThreshold(t *testing.T) {
	// Subtest 1: ErrNoNodesConfigured does not trigger provisional proposal
	t.Run("NoNodesConfigured", func(t *testing.T) {
		h := newRelayHarness()
		user, sig := h.userSig(t)
		h.parent.lookupErr = parentchain.ErrNoNodesConfigured
		calls := 0
		h.relay.SetProvisionalProposer(func(u common.Address, us []byte) error {
			calls++
			return nil
		})
		_, err := h.relay.Submit(user, sig)
		require.NoError(t, err)
		for i := 0; i < 5; i++ {
			h.relay.ProcessBatch()
		}
		assert.Equal(t, 0, calls, "ErrNoNodesConfigured must never trigger provisionalProposer")
	})

	// Subtest 2: Missing private key error does not trigger provisional proposal
	t.Run("PrivateKeyNotConfigured", func(t *testing.T) {
		h := newRelayHarness()
		user, sig := h.userSig(t)
		h.parent.sendErr = errors.New("cannot SendRegisterAccount: private key not configured")
		calls := 0
		h.relay.SetProvisionalProposer(func(u common.Address, us []byte) error {
			calls++
			return nil
		})
		_, err := h.relay.Submit(user, sig)
		require.NoError(t, err)
		for i := 0; i < 5; i++ {
			h.relay.ProcessBatch()
		}
		assert.Equal(t, 0, calls, "private key not configured must never trigger provisionalProposer")
	})

	// Subtest 3: Network outage requires >= 2 consecutive failures
	t.Run("ConsecutiveFailuresThreshold", func(t *testing.T) {
		h := newRelayHarness()
		user, sig := h.userSig(t)
		h.parent.lookupErr = errors.New("network timeout")
		calls := 0
		h.relay.SetProvisionalProposer(func(u common.Address, us []byte) error {
			calls++
			return nil
		})
		_, err := h.relay.Submit(user, sig)
		require.NoError(t, err)

		// Tick 1: 1st failure (attempts becomes 1). Must not trigger yet.
		h.relay.ProcessBatch()
		assert.Equal(t, 0, calls, "Must not propose provisional after only 1 failure")

		// Tick 2: 2nd failure (attempts becomes 2). Must trigger!
		h.relay.ProcessBatch()
		assert.Equal(t, 1, calls, "Must propose provisional on 2nd consecutive failure")
	})
}

func TestRegistrationRelay_FullQueue_VerifyOneRetriesWhenQueueDrains(t *testing.T) {
	h := newRelayHarness()
	h.relay.maxVerifies = 2
	user, sig := h.userSig(t)

	isProv := false
	h.relay.SetProvisionalChecker(func(a common.Address) bool {
		return a == user && isProv
	})

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)

	// Step 1: Send tx, submitted=true
	h.relay.ProcessBatch()
	assert.Equal(t, 1, h.parent.sendCount())

	isProv = true
	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.True(t, entry.submitted)

	// Fill r.queue to its capacity
	for {
		select {
		case h.relay.queue <- common.HexToAddress("0xdead"):
		default:
			goto QueueFull
		}
	}
QueueFull:

	// Step 2: verifyOne reaches maxVerifies (2 times)
	// verifyOne call 1 (verifies becomes 1)
	h.relay.verifyOne(user)
	entry, _ = h.relay.snapshot(user)
	assert.True(t, entry.submitted)

	// verifyOne call 2 (verifies reaches maxVerifies 2):
	// Queue is full! It should NOT be dropped; instead submitted remains true and verifies = maxVerifies
	h.relay.verifyOne(user)
	entry, _ = h.relay.snapshot(user)
	assert.True(t, entry.submitted, "Request must remain submitted=true when queue is full")
	assert.Equal(t, h.relay.maxVerifies, entry.verifies)
	assert.Equal(t, "retry queue full; waiting to resubmit", entry.reason)

	// Step 3: Drain the queue
	drained := 0
	for len(h.relay.queue) > 0 {
		<-h.relay.queue
		drained++
	}
	require.Positive(t, drained)

	// Step 4: Now queue has room! verifyOne should successfully enqueue
	h.relay.verifyOne(user)
	entry, _ = h.relay.snapshot(user)
	assert.False(t, entry.submitted, "submitted must become false once enqueued")
	assert.Equal(t, 0, entry.verifies)

	// Step 5: ProcessBatch should pop from queue and resubmit SendRegisterAccount!
	h.relay.ProcessBatch()
	assert.Equal(t, 2, h.parent.sendCount(), "SendRegisterAccount must be retried after queue drains")
}

func TestRegistrationRelay_FullQueue_ParentConfirmsWhileWaiting(t *testing.T) {
	h := newRelayHarness()
	h.relay.maxVerifies = 2
	user, sig := h.userSig(t)

	isProv := false
	h.relay.SetProvisionalChecker(func(a common.Address) bool {
		return a == user && isProv
	})

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)

	h.relay.ProcessBatch()
	assert.Equal(t, 1, h.parent.sendCount())

	isProv = true

	// Put into full-queue waiting state
	h.relay.update(user, func(e *relayEntry) {
		e.status = RegStatusProvisional
		e.submitted = true
		e.verifies = h.relay.maxVerifies
		e.reason = "retry queue full; waiting to resubmit"
	})

	// Parent confirms while waiting
	h.parent.mu.Lock()
	h.parent.registry[user] = h.clusterKP.PublicKey()
	h.parent.mu.Unlock()
	h.flags.Store(user, true) // on-chain registered

	// Next verifyOne should confirm the account
	h.relay.verifyOne(user)
	res := h.relay.Status(user)
	assert.Equal(t, RegStatusConfirmed, res.Status, "Account must be confirmed when parent confirms during full queue wait")
}

func TestRegistrationRelay_FullQueue_RetryOrFailKeepsProvisionalAlive(t *testing.T) {
	h := newRelayHarness()
	user, sig := h.userSig(t)

	isProv := false
	h.relay.SetProvisionalChecker(func(a common.Address) bool {
		return a == user && isProv
	})

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)

	isProv = true

	// Fill queue to capacity
	for {
		select {
		case h.relay.queue <- common.HexToAddress("0xdead"):
		default:
			goto QueueFilled
		}
	}
QueueFilled:

	// retryOrFail is called when parent fails
	h.relay.retryOrFail(user, "parent offline")

	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.Equal(t, RegStatusPending, entry.status, "Provisional request must NOT be marked FAILED on full queue")
	assert.True(t, entry.submitted, "Provisional request must be marked submitted=true to be polled by verifyOne")
	assert.Equal(t, h.relay.maxVerifies, entry.verifies)
}

func TestReview5_RetryOrFail_NonProvisionalFailsEvenWithProposerConfigured(t *testing.T) {
	h := newRelayHarness()
	h.relay.maxAttempts = 3
	user, sig := h.userSig(t)

	// Even if provisionalProposer is configured (standard in production),
	// an account that is not provisional must still transition to FAILED when attempts exhaust.
	h.relay.SetProvisionalProposer(func(u common.Address, us []byte) error {
		return nil
	})
	h.relay.SetProvisionalChecker(func(a common.Address) bool {
		return false
	})

	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)

	for i := 0; i < h.relay.maxAttempts; i++ {
		h.relay.retryOrFail(user, "persistent error")
	}

	entry, ok := h.relay.snapshot(user)
	require.True(t, ok)
	assert.Equal(t, RegStatusFailed, entry.status, "Non-provisional request must reach FAILED once maxAttempts exhausted")
}



