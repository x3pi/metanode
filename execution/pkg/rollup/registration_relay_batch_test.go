package rollup

import (
	"errors"
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

// fakeBatchParent adds SendRegisterAccountsBatch to fakeRegParent.
type fakeBatchParent struct {
	*fakeRegParent
	bmu      sync.Mutex
	batches  [][]parentchain.RegisterAccountEntry
	batchErr error
	register bool // apply each batch entry to the registry (the parent executed it)
}

func (f *fakeBatchParent) SendRegisterAccountsBatch(entries []parentchain.RegisterAccountEntry) (common.Hash, error) {
	f.bmu.Lock()
	defer f.bmu.Unlock()
	if f.batchErr != nil {
		return common.Hash{}, f.batchErr
	}
	f.batches = append(f.batches, append([]parentchain.RegisterAccountEntry(nil), entries...))
	if f.register {
		f.mu.Lock()
		for _, e := range entries {
			f.registry[e.UserAddress] = e.FloatIdentityKey
		}
		f.mu.Unlock()
	}
	return common.BigToHash(big.NewInt(int64(1000 + len(f.batches)))), nil
}

func (f *fakeBatchParent) batchSizes() []int {
	f.bmu.Lock()
	defer f.bmu.Unlock()
	var out []int
	for _, b := range f.batches {
		out = append(out, len(b))
	}
	return out
}

type batchHarness struct {
	*relayHarness
	bp *fakeBatchParent
}

func newBatchHarness(batchSize int) *batchHarness {
	base := newRelayHarness()
	bp := &fakeBatchParent{fakeRegParent: base.parent}
	base.relay = NewRegistrationRelay(bp, base.clusterKP.PublicKey(),
		func(digest []byte) cm.Sign { return bls.Sign(base.clusterKP.PrivateKey(), digest) },
		func(a common.Address) bool { v, ok := base.flags.Load(a); return ok && v.(bool) })
	base.relay.alertSink = func(string) {}
	base.relay.SetBatchSize(batchSize)
	return &batchHarness{relayHarness: base, bp: bp}
}

func (h *batchHarness) submitN(t *testing.T, n int) []common.Address {
	t.Helper()
	var users []common.Address
	for i := 0; i < n; i++ {
		u, sig := h.userSig(t)
		_, err := h.relay.Submit(u, sig)
		require.NoError(t, err)
		users = append(users, u)
	}
	return users
}

func TestRegistrationBatch_ManyUsersOneParentTransaction(t *testing.T) {
	h := newBatchHarness(64)
	users := h.submitN(t, 5)
	h.relay.ProcessBatch()

	assert.Equal(t, []int{5}, h.bp.batchSizes(), "5 registrations must go out as ONE batch tx")
	assert.Equal(t, 0, h.parent.sendCount(), "no single registration txs")
	var hash common.Hash
	for i, u := range users {
		e, ok := h.relay.snapshot(u)
		require.True(t, ok)
		assert.True(t, e.submitted)
		assert.True(t, e.batched)
		if i == 0 {
			hash = e.txHash
		}
		assert.Equal(t, hash, e.txHash, "batch members share the tx hash")
	}
	// The cluster signature in each entry is valid for that user's registration digest.
	for _, e := range h.bp.batches[0] {
		assert.True(t, bls.VerifySign(h.clusterKP.PublicKey(), e.ClusterSig, h.relay.RegistrationDigest(e.UserAddress)))
		assert.Equal(t, h.clusterKP.PublicKey(), e.FloatIdentityKey)
	}
}

func TestRegistrationBatch_ChunkedByBatchSize(t *testing.T) {
	h := newBatchHarness(2)
	h.submitN(t, 5)
	h.relay.ProcessBatch()
	assert.Equal(t, []int{2, 2, 1}, h.bp.batchSizes())
	assert.Equal(t, 0, h.parent.sendCount())
}

func TestRegistrationBatch_DisabledByDefaultSendsSingles(t *testing.T) {
	h := newBatchHarness(0)
	h.submitN(t, 3)
	h.relay.ProcessBatch()
	assert.Empty(t, h.bp.batchSizes())
	assert.Equal(t, 3, h.parent.sendCount())
}

// A parent that refuses the batch at submission (e.g. not upgraded) gets the same registrations one by one, in the same
// tick, and those requests are never batched again.
func TestRegistrationBatch_RefusedBatchFallsBackToSingles(t *testing.T) {
	h := newBatchHarness(64)
	h.bp.batchErr = errors.New("rejected: unknown method")
	users := h.submitN(t, 3)
	h.relay.ProcessBatch()

	assert.Equal(t, 3, h.parent.sendCount(), "fallback sends each registration alone immediately")
	for _, u := range users {
		e, _ := h.relay.snapshot(u)
		assert.True(t, e.submitted)
		assert.False(t, e.batched)
		assert.True(t, e.noBatch)
	}
}

// A batch tx that the parent executes but rejects (receipt status 0) resets the nonce pipeline ONCE, and its members
// are resent as single transactions.
func TestRegistrationBatch_FailedReceiptReportedOnceThenSingles(t *testing.T) {
	h := newBatchHarness(64)
	users := h.submitN(t, 4)
	h.relay.ProcessBatch()
	require.Equal(t, []int{4}, h.bp.batchSizes())

	e0, _ := h.relay.snapshot(users[0])
	h.parent.mu.Lock()
	h.parent.receipts[e0.txHash] = &parentchain.Receipt{TxHash: e0.txHash, Status: 0, ErrorCode: 299}
	h.parent.mu.Unlock()

	h.relay.ProcessBatch() // verifyOne sees the failed receipt for every member
	h.parent.mu.Lock()
	reports := 0
	for _, r := range h.parent.failedReports {
		if r == e0.txHash {
			reports++
		}
	}
	h.parent.mu.Unlock()
	assert.Equal(t, 1, reports, "one failed batch tx = one nonce-pipeline report")

	h.relay.ProcessBatch() // members are resent alone
	assert.Equal(t, 4, h.parent.sendCount())
	assert.Equal(t, []int{4}, h.bp.batchSizes(), "no second batch for requests whose batch failed")
}

// Partial success: the parent applies some entries and rejects others (already owned elsewhere). Each member is
// resolved from the registry individually.
func TestRegistrationBatch_PartialSuccessResolvedPerUser(t *testing.T) {
	h := newBatchHarness(64)
	h.bp.register = true
	users := h.submitN(t, 3)
	other := bls.GenerateKeyPair().PublicKey()
	h.relay.ProcessBatch()

	// Another cluster won users[1] on the parent before our batch executed.
	h.parent.mu.Lock()
	h.parent.registry[users[1]] = other
	h.parent.mu.Unlock()
	for _, u := range []common.Address{users[0], users[2]} {
		h.flags.Store(u, true) // our registration events landed
	}
	h.relay.ProcessBatch()

	assert.Equal(t, RegStatusConfirmed, h.relay.Status(users[0]).Status)
	assert.Equal(t, RegStatusConfirmed, h.relay.Status(users[2]).Status)
	res := h.relay.Status(users[1])
	assert.Equal(t, RegStatusRejected, res.Status)
	require.NotNil(t, res.HomeCluster)
	assert.Equal(t, other, *res.HomeCluster)
}

// During a parent outage the provisional proposal is not repeated every tick: once proposed at Submit it waits
// relayProvisionalRetryTicks attempts before re-proposing (the proposal applies at its first commit with one committee attestation).
func TestRegistrationRelay_ProvisionalProposalIsThrottled(t *testing.T) {
	h := newRelayHarness()
	h.relay.alertSink = func(string) {}
	h.relay.maxAttempts = 1000 // keep the request PENDING for the whole run (the proposal never applies here)
	user, sig := h.userSig(t)
	h.parent.lookupErr = errors.New("parent chain offline")
	calls := 0
	h.relay.SetProvisionalProposer(func(common.Address, []byte) error { calls++; return nil }) // never applies
	_, err := h.relay.Submit(user, sig)
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "initial proposal happens at Submit")

	// Check boundary: ticks 1..relayProvisionalRetryTicks only count down cooldown; tick 31 proposes a 2nd time.
	for i := 0; i < relayProvisionalRetryTicks; i++ {
		h.relay.ProcessBatch()
	}
	assert.Equal(t, 1, calls, "cooldown must hold for %d ticks", relayProvisionalRetryTicks)

	h.relay.ProcessBatch()
	assert.Equal(t, 2, calls, "tick %d must re-propose", relayProvisionalRetryTicks+1)

	const totalTicks = 100
	for i := relayProvisionalRetryTicks + 1; i < totalTicks; i++ {
		h.relay.ProcessBatch()
	}
	want := 1 + totalTicks/(relayProvisionalRetryTicks+1)
	assert.Equal(t, want, calls, "one proposal per %d attempts, not one per tick", relayProvisionalRetryTicks+1)
}
