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
