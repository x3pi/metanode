package rollup

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

type mockSysAccountDB struct {
	balances   map[common.Address]*big.Int
	nonces     map[common.Address]uint64
	registered map[common.Address]bool
}

func newMockSysAccountDB() *mockSysAccountDB {
	return &mockSysAccountDB{
		balances:   map[common.Address]*big.Int{},
		nonces:     map[common.Address]uint64{},
		registered: map[common.Address]bool{},
	}
}

func (m *mockSysAccountDB) GetBalance(addr common.Address) *big.Int {
	if b, ok := m.balances[addr]; ok {
		return b
	}
	return big.NewInt(0)
}

func (m *mockSysAccountDB) AddBalance(addr common.Address, amount *big.Int) error {
	m.balances[addr] = new(big.Int).Add(m.GetBalance(addr), amount)
	return nil
}

func (m *mockSysAccountDB) SubBalance(addr common.Address, amount *big.Int) {
	m.balances[addr] = new(big.Int).Sub(m.GetBalance(addr), amount)
}

func (m *mockSysAccountDB) GetNonce(addr common.Address) uint64 {
	return m.nonces[addr]
}

func (m *mockSysAccountDB) SetNonce(addr common.Address, nonce uint64) {
	m.nonces[addr] = nonce
}

func (m *mockSysAccountDB) GetParentRegistered(addr common.Address) bool {
	return m.registered[addr]
}

func (m *mockSysAccountDB) SetParentRegistered(addr common.Address, registered bool) {
	m.registered[addr] = registered
}

type attestedSysEnv struct {
	vals     []*bls.KeyPair
	sc       *memSCDB
	stateDB  *mockSysAccountDB
	chainID  uint64
	innerRaw []byte
}

func newAttestedSysEnv(n int) *attestedSysEnv {
	bls.Init()
	samplePayload := &RollupSystemPayload{
		Event: Event{Type: EventCreditObserved},
		MsgID: common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
	}
	inner, _ := MarshalRollupSystemPayload(samplePayload)
	e := &attestedSysEnv{
		sc:       newMemSCDB(),
		stateDB:  newMockSysAccountDB(),
		chainID:  parentchain.ParentChainID,
		innerRaw: inner,
	}
	for i := 0; i < n; i++ {
		e.vals = append(e.vals, bls.GenerateKeyPair())
	}
	return e
}

func (e *attestedSysEnv) committee(n int) *mockCommitteeProvider {
	m := &mockCommitteeProvider{}
	for i := 0; i < n; i++ {
		m.validators = append(m.validators, e.vals[i].PublicKey())
	}
	return m
}

func (e *attestedSysEnv) buildPayload(signer *bls.KeyPair, inner []byte) []byte {
	digest := ComputeRollupSystemEventDigest(e.chainID, inner)
	sig := bls.Sign(signer.PrivateKey(), digest)
	p := RollupSystemAttestedPayload{
		Kind:  PayloadKindRollupSystemAttested,
		Inner: inner,
		Attestations: []RegistrationAttestation{
			{
				ValidatorPubkey: signer.PublicKey(),
				Signature:       sig,
			},
		},
	}
	b, _ := MarshalRollupSystemAttestedPayload(&p)
	return b
}

func TestRollupSystemAttestation_4ValidatorThresholdAndTombstone(t *testing.T) {
	e := newAttestedSysEnv(4) // N=4, f=1 => required = 2
	comm := e.committee(4)

	dispatchedCount := 0
	dispatcher := func(store Store, stateDB AccountStateDB, inner []byte) error {
		dispatchedCount++
		require.Equal(t, e.innerRaw, inner)
		return nil
	}

	// 1. Validator 0 submits: only 1 signature < 2 required => PENDING (dispatchedCount == 0)
	p0 := e.buildPayload(e.vals[0], e.innerRaw)
	err := ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, p0, dispatcher)
	require.NoError(t, err)
	require.Equal(t, 0, dispatchedCount, "1 signature should not dispatch event")
	require.True(t, HasPendingRollupSystemAttestation(e.sc, e.chainID, e.innerRaw, e.vals[0].PublicKey()), "v0 should be pending")
	require.False(t, HasPendingRollupSystemAttestation(e.sc, e.chainID, e.innerRaw, e.vals[1].PublicKey()), "v1 should not be pending yet")

	// Validator 0 submits again => duplicate in consecutive blocks: still 1 signature, stays PENDING
	err = ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, p0, dispatcher)
	require.NoError(t, err)
	require.Equal(t, 0, dispatchedCount, "duplicate submission must not advance threshold")

	// 2. Validator 1 submits: 2 distinct signatures >= 2 => DISPATCHED!
	p1 := e.buildPayload(e.vals[1], e.innerRaw)
	err = ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, p1, dispatcher)
	require.NoError(t, err)
	require.Equal(t, 1, dispatchedCount, "2 distinct signatures must dispatch event")

	// Tombstone is now active
	digest := ComputeRollupSystemEventDigest(e.chainID, e.innerRaw)
	tombstoneKey := RollupSystemTombstoneKey(digest)
	val, ok := e.sc.StorageValue(RollupSystemAddress, tombstoneKey)
	require.True(t, ok)
	require.NotEmpty(t, val)

	// Pending attestations cleared after dispatch
	pendingKey := RollupSystemPendingKey(digest)
	atts := NewDBAttestationStore(e.sc).Load(pendingKey)
	require.Empty(t, atts, "pending attestations must be cleared upon dispatch")

	// HasPendingRollupSystemAttestation reports true for any validator now because tombstone is set
	require.True(t, HasPendingRollupSystemAttestation(e.sc, e.chainID, e.innerRaw, e.vals[2].PublicKey()), "tombstoned event should report true")

	// 3. Validator 2 and 3 submit late => tombstone hit, no-op success, NO second dispatch
	p2 := e.buildPayload(e.vals[2], e.innerRaw)
	err = ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, p2, dispatcher)
	require.NoError(t, err)
	require.Equal(t, 1, dispatchedCount, "late validator must not trigger duplicate dispatch")

	p3 := e.buildPayload(e.vals[3], e.innerRaw)
	err = ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, p3, dispatcher)
	require.NoError(t, err)
	require.Equal(t, 1, dispatchedCount, "late validator must not trigger duplicate dispatch")
}

func TestRollupSystemAttestation_NonCommitteeAndForgedSignaturesRejected(t *testing.T) {
	e := newAttestedSysEnv(4)
	comm := e.committee(4)

	dispatched := false
	dispatcher := func(store Store, stateDB AccountStateDB, inner []byte) error {
		dispatched = true
		return nil
	}

	// 1. Signature from outsider (not in committee) => rejected
	outsider := bls.GenerateKeyPair()
	pOutsider := e.buildPayload(outsider, e.innerRaw)
	err := ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, pOutsider, dispatcher)
	require.Error(t, err)
	require.Contains(t, err.Error(), "non-committee validator")
	require.False(t, dispatched)

	// 2. Forged signature (signed different digest) => rejected
	wrongDigest := ComputeRollupSystemEventDigest(e.chainID, []byte(`{"different":"payload"}`))
	badSig := bls.Sign(e.vals[0].PrivateKey(), wrongDigest)
	pForged := RollupSystemAttestedPayload{
		Kind:  PayloadKindRollupSystemAttested,
		Inner: e.innerRaw,
		Attestations: []RegistrationAttestation{
			{
				ValidatorPubkey: e.vals[0].PublicKey(),
				Signature:       badSig,
			},
		},
	}
	bForged, _ := MarshalRollupSystemAttestedPayload(&pForged)
	err = ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, bForged, dispatcher)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid BLS signature")
	require.False(t, dispatched)
}

func TestRollupSystemAttestation_DispatcherErrorPropagates(t *testing.T) {
	e := newAttestedSysEnv(1) // N=1 => required = 1
	comm := e.committee(1)

	expectedErr := errors.New("state transition rejected")
	dispatcher := func(store Store, stateDB AccountStateDB, inner []byte) error {
		return expectedErr
	}

	p := e.buildPayload(e.vals[0], e.innerRaw)
	err := ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, p, dispatcher)
	require.ErrorIs(t, err, expectedErr)
}

// A rejected inner event must not be lost: the event is only tombstoned after a SUCCESSFUL dispatch, so a retry
// (the worker re-proposes after a stale-state rejection) still applies it exactly once.
func TestRollupSystemAttestation_RejectedDispatchIsRetriedNotLost(t *testing.T) {
	for _, n := range []int{1, 4} {
		e := newAttestedSysEnv(n)
		comm := e.committee(n)
		fail := true
		applied := 0
		dispatcher := func(Store, AccountStateDB, []byte) error {
			if fail {
				return errors.New("stale state, retry later")
			}
			applied++
			return nil
		}
		need := (n-1)/3 + 1
		var last error
		for i := 0; i < need; i++ {
			last = ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, e.buildPayload(e.vals[i], e.innerRaw), dispatcher)
		}
		require.Error(t, last, "n=%d: quorum reached but the dispatch was rejected", n)
		require.Equal(t, 0, applied)

		fail = false
		// retry by the validator whose share was not recorded
		require.NoError(t, ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, e.buildPayload(e.vals[need-1], e.innerRaw), dispatcher))
		require.Equal(t, 1, applied, "n=%d: the event must be applied after the retry", n)
		// and exactly once
		require.NoError(t, ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, e.buildPayload(e.vals[0], e.innerRaw), dispatcher))
		require.Equal(t, 1, applied, "n=%d: tombstone prevents a second application", n)
	}
}

func TestRollupSystemAttestation_Metrics(t *testing.T) {
	e := newAttestedSysEnv(4)
	comm := e.committee(4)

	// 1. Committee read error metric
	errComm := &failingCommitteeMock{err: errors.New("committee lookup failed")}
	initErrCount := testutil.ToFloat64(metrics.RollupCommitteeReadErrorsTotal)
	_ = ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, errComm, e.chainID, e.buildPayload(e.vals[0], e.innerRaw), func(Store, AccountStateDB, []byte) error { return nil })
	require.Equal(t, initErrCount+1, testutil.ToFloat64(metrics.RollupCommitteeReadErrorsTotal))

	// 2. Non-committee rejection metric
	nonVal := bls.GenerateKeyPair()
	initNonComm := testutil.ToFloat64(metrics.RollupSignaturesRejectedTotal.WithLabelValues("non_committee"))
	_ = ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, e.buildPayload(nonVal, e.innerRaw), func(Store, AccountStateDB, []byte) error { return nil })
	require.Equal(t, initNonComm+1, testutil.ToFloat64(metrics.RollupSignaturesRejectedTotal.WithLabelValues("non_committee")))

	// 3. Duplicate rejection metric
	pDup := RollupSystemAttestedPayload{
		Kind:  PayloadKindRollupSystemAttested,
		Inner: e.innerRaw,
		Attestations: []RegistrationAttestation{
			{ValidatorPubkey: e.vals[0].PublicKey(), Signature: bls.Sign(e.vals[0].PrivateKey(), ComputeRollupSystemEventDigest(e.chainID, e.innerRaw))},
			{ValidatorPubkey: e.vals[0].PublicKey(), Signature: bls.Sign(e.vals[0].PrivateKey(), ComputeRollupSystemEventDigest(e.chainID, e.innerRaw))},
		},
	}
	bDup, _ := MarshalRollupSystemAttestedPayload(&pDup)
	initDup := testutil.ToFloat64(metrics.RollupSignaturesRejectedTotal.WithLabelValues("duplicate"))
	_ = ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, bDup, func(Store, AccountStateDB, []byte) error { return nil })
	require.Equal(t, initDup+1, testutil.ToFloat64(metrics.RollupSignaturesRejectedTotal.WithLabelValues("duplicate")))

	// 4. Invalid signature metric
	wrongDigest := ComputeRollupSystemEventDigest(e.chainID, []byte("wrong"))
	badSig := bls.Sign(e.vals[0].PrivateKey(), wrongDigest)
	pBadSig := RollupSystemAttestedPayload{
		Kind:  PayloadKindRollupSystemAttested,
		Inner: e.innerRaw,
		Attestations: []RegistrationAttestation{
			{ValidatorPubkey: e.vals[0].PublicKey(), Signature: badSig},
		},
	}
	bBadSig, _ := MarshalRollupSystemAttestedPayload(&pBadSig)
	initBadSig := testutil.ToFloat64(metrics.RollupSignaturesRejectedTotal.WithLabelValues("invalid_signature"))
	_ = ApplyAttestedSystemEvent(nil, e.stateDB, e.sc, comm, e.chainID, bBadSig, func(Store, AccountStateDB, []byte) error { return nil })
	require.Equal(t, initBadSig+1, testutil.ToFloat64(metrics.RollupSignaturesRejectedTotal.WithLabelValues("invalid_signature")))

	// 5. Pending-store counters in dbAttestationStore
	initStored := testutil.ToFloat64(metrics.RollupAttestationStoredTotal)
	initDone := testutil.ToFloat64(metrics.RollupAttestationCompletedTotal)
	key := RollupSystemPendingKey([]byte("metric_test_key"))
	attStore := NewDBAttestationStore(e.sc)
	attStore.Save(key, []RegistrationAttestation{{ValidatorPubkey: e.vals[0].PublicKey(), Signature: cm.Sign{}}})
	require.Equal(t, initStored+1, testutil.ToFloat64(metrics.RollupAttestationStoredTotal))
	attStore.Clear(key)
	require.Equal(t, initDone+1, testutil.ToFloat64(metrics.RollupAttestationCompletedTotal))
}

type failingCommitteeMock struct {
	err error
}

func (m *failingCommitteeMock) GetActiveCommitteeBLSKeys() ([]cm.PublicKey, error) {
	return nil, m.err
}
