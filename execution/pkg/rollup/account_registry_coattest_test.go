package rollup

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

type mockCommitteeProvider struct {
	validators []cm.PublicKey
}

func (m *mockCommitteeProvider) GetActiveCommitteeBLSKeys() ([]cm.PublicKey, error) {
	return m.validators, nil
}

type coattestMockStateDB struct {
	mu          sync.Mutex
	registered  map[common.Address]bool
	provisional map[common.Address]bool
	locked      map[common.Address]bool
}

func newCoattestMockStateDB() *coattestMockStateDB {
	return &coattestMockStateDB{
		registered:  make(map[common.Address]bool),
		provisional: make(map[common.Address]bool),
		locked:      make(map[common.Address]bool),
	}
}

func (m *coattestMockStateDB) GetParentRegistered(addr common.Address) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.registered[addr]
}

func (m *coattestMockStateDB) SetParentRegistered(addr common.Address, reg bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registered[addr] = reg
}

func (m *coattestMockStateDB) GetProvisional(addr common.Address) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.provisional[addr]
}

func (m *coattestMockStateDB) SetProvisional(addr common.Address, prov bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.provisional[addr] = prov
}

func (m *coattestMockStateDB) GetLocked(addr common.Address) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.locked[addr]
}

func (m *coattestMockStateDB) SetLocked(addr common.Address, locked bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.locked[addr] = locked
}

func TestAccountRegistryHandler_CoAttestation(t *testing.T) {
	bls.Init()

	v1 := bls.GenerateKeyPair()
	v2 := bls.GenerateKeyPair()
	v3 := bls.GenerateKeyPair()
	v4 := bls.GenerateKeyPair()

	// 4 validators => f = (4-1)/3 = 1, required = f+1 = 2
	committee := []cm.PublicKey{v1.PublicKey(), v2.PublicKey(), v3.PublicKey(), v4.PublicKey()}
	clusterKey := v1.PublicKey()
	user := common.HexToAddress("0x1111111111111111111111111111111111111111")
	chainID := uint64(991)
	parentSeq := uint64(42)

	handler := NewAccountRegistryHandler(clusterKey)
	handler.SetChainID(chainID)
	handler.SetCommitteeProvider(&mockCommitteeProvider{validators: committee})

	digest := ComputeAccountRegistrationAttestDigest(chainID, user, clusterKey, parentSeq)
	sig1 := bls.Sign(v1.PrivateKey(), digest)
	sig2 := bls.Sign(v2.PrivateKey(), digest)

	t.Run("sufficient valid committee signatures succeeds", func(t *testing.T) {
		db := newCoattestMockStateDB()
		payload := AccountRegistrationPayload{
			Kind:       SystemPayloadKindAccountRegistered,
			User:       user,
			ClusterKey: clusterKey,
			ParentSeq:  parentSeq,
			Attestations: []RegistrationAttestation{
				{ValidatorPubkey: v1.PublicKey(), Signature: sig1},
				{ValidatorPubkey: v2.PublicKey(), Signature: sig2},
			},
		}
		data, err := payload.MarshalProto()
		require.NoError(t, err)

		err = handler.Apply(db, data)
		assert.NoError(t, err)
		assert.True(t, db.GetParentRegistered(user))
	})

	t.Run("insufficient attestations is rejected", func(t *testing.T) {
		db := newCoattestMockStateDB()
		payload := AccountRegistrationPayload{
			Kind:       SystemPayloadKindAccountRegistered,
			User:       user,
			ClusterKey: clusterKey,
			ParentSeq:  parentSeq,
			Attestations: []RegistrationAttestation{
				{ValidatorPubkey: v1.PublicKey(), Signature: sig1}, // only 1, required 2
			},
		}
		data, err := payload.MarshalProto()
		require.NoError(t, err)

		err = handler.Apply(db, data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "insufficient attestations: got 1, required 2")
		assert.False(t, db.GetParentRegistered(user))
	})

	t.Run("duplicate attestation from same validator is rejected", func(t *testing.T) {
		db := newCoattestMockStateDB()
		payload := AccountRegistrationPayload{
			Kind:       SystemPayloadKindAccountRegistered,
			User:       user,
			ClusterKey: clusterKey,
			ParentSeq:  parentSeq,
			Attestations: []RegistrationAttestation{
				{ValidatorPubkey: v1.PublicKey(), Signature: sig1},
				{ValidatorPubkey: v1.PublicKey(), Signature: sig1}, // duplicate v1
			},
		}
		data, err := payload.MarshalProto()
		require.NoError(t, err)

		err = handler.Apply(db, data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate attestation from validator")
		assert.False(t, db.GetParentRegistered(user))
	})

	t.Run("attestation from non-committee validator is rejected", func(t *testing.T) {
		db := newCoattestMockStateDB()
		outsider := bls.GenerateKeyPair()
		sigOutsider := bls.Sign(outsider.PrivateKey(), digest)

		payload := AccountRegistrationPayload{
			Kind:       SystemPayloadKindAccountRegistered,
			User:       user,
			ClusterKey: clusterKey,
			ParentSeq:  parentSeq,
			Attestations: []RegistrationAttestation{
				{ValidatorPubkey: v1.PublicKey(), Signature: sig1},
				{ValidatorPubkey: outsider.PublicKey(), Signature: sigOutsider},
			},
		}
		data, err := payload.MarshalProto()
		require.NoError(t, err)

		err = handler.Apply(db, data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "attestation from non-committee validator")
		assert.False(t, db.GetParentRegistered(user))
	})

	t.Run("tampered payload digest is rejected", func(t *testing.T) {
		db := newCoattestMockStateDB()
		// Signatures were computed for user, but payload modifies user to another address
		tamperedUser := common.HexToAddress("0x2222222222222222222222222222222222222222")
		payload := AccountRegistrationPayload{
			Kind:       SystemPayloadKindAccountRegistered,
			User:       tamperedUser,
			ClusterKey: clusterKey,
			ParentSeq:  parentSeq,
			Attestations: []RegistrationAttestation{
				{ValidatorPubkey: v1.PublicKey(), Signature: sig1},
				{ValidatorPubkey: v2.PublicKey(), Signature: sig2},
			},
		}
		data, err := payload.MarshalProto()
		require.NoError(t, err)

		err = handler.Apply(db, data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid BLS signature")
		assert.False(t, db.GetParentRegistered(tamperedUser))
	})
}

// A non-default chain ID must not break registration: the handler must derive the same digest the worker signs.
func TestAccountRegistryAttestationFollowsConfiguredChainID(t *testing.T) {
	old := parentchain.ParentChainID
	defer parentchain.SetParentChainID(old)
	parentchain.SetParentChainID(4242)

	kp := bls.GenerateKeyPair()
	user := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	cluster := kp.PublicKey()
	digest := ComputeAccountRegistrationAttestDigest(parentchain.ParentChainID, user, cluster, 7)
	payload := AccountRegistrationPayload{Kind: SystemPayloadKindAccountRegistered, User: user, ClusterKey: cluster, ParentSeq: 7,
		Attestations: []RegistrationAttestation{{ValidatorPubkey: kp.PublicKey(), Signature: bls.Sign(kp.PrivateKey(), digest)}}}
	data, _ := payload.MarshalProto()
	db := newCoattestMockStateDB()
	if err := NewAccountRegistryHandler(cluster).Apply(db, data); err != nil {
		t.Fatalf("registration must apply under chain ID 4242: %v", err)
	}
}

func TestAccountRegistryHandler_CoAttestation_LockWithCommittee(t *testing.T) {
	bls.Init()

	v1 := bls.GenerateKeyPair()
	v2 := bls.GenerateKeyPair()
	v3 := bls.GenerateKeyPair()
	v4 := bls.GenerateKeyPair()

	// 4 validators => f = (4-1)/3 = 1, required = f+1 = 2
	committee := []cm.PublicKey{v1.PublicKey(), v2.PublicKey(), v3.PublicKey(), v4.PublicKey()}
	clusterKey := v1.PublicKey()
	winningCluster := bls.GenerateKeyPair().PublicKey()
	user := common.HexToAddress("0x1111111111111111111111111111111111111111")
	chainID := uint64(991)
	parentSeq := uint64(42)

	handler := NewAccountRegistryHandler(clusterKey)
	handler.SetChainID(chainID)
	handler.SetCommitteeProvider(&mockCommitteeProvider{validators: committee})

	digest := ComputeAccountLockedAttestDigest(chainID, user, winningCluster, parentSeq)
	sig1 := bls.Sign(v1.PrivateKey(), digest)
	sig2 := bls.Sign(v2.PrivateKey(), digest)

	t.Run("sufficient valid committee signatures locks provisional account", func(t *testing.T) {
		db := newCoattestMockStateDB()
		db.SetProvisional(user, true)

		payload := AccountRegistrationPayload{
			Kind:        SystemPayloadKindAccountLocked,
			User:        user,
			HomeCluster: winningCluster,
			ParentSeq:   parentSeq,
			Attestations: []RegistrationAttestation{
				{ValidatorPubkey: v1.PublicKey(), Signature: sig1},
				{ValidatorPubkey: v2.PublicKey(), Signature: sig2},
			},
		}
		data, err := payload.MarshalProto()
		require.NoError(t, err)

		err = handler.ApplyAttested(db, nil, &mockCommitteeProvider{validators: committee}, data)
		assert.NoError(t, err)
		assert.True(t, db.GetLocked(user))
		assert.False(t, db.GetProvisional(user))
	})

	t.Run("non-committee validator attestation is rejected", func(t *testing.T) {
		db := newCoattestMockStateDB()
		db.SetProvisional(user, true)

		outsider := bls.GenerateKeyPair()
		sigOutsider := bls.Sign(outsider.PrivateKey(), digest)

		payload := AccountRegistrationPayload{
			Kind:        SystemPayloadKindAccountLocked,
			User:        user,
			HomeCluster: winningCluster,
			ParentSeq:   parentSeq,
			Attestations: []RegistrationAttestation{
				{ValidatorPubkey: v1.PublicKey(), Signature: sig1},
				{ValidatorPubkey: outsider.PublicKey(), Signature: sigOutsider},
			},
		}
		data, err := payload.MarshalProto()
		require.NoError(t, err)

		err = handler.ApplyAttested(db, nil, &mockCommitteeProvider{validators: committee}, data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "attestation from non-committee validator")
		assert.False(t, db.GetLocked(user))
	})

	t.Run("insufficient committee attestations is rejected", func(t *testing.T) {
		db := newCoattestMockStateDB()
		db.SetProvisional(user, true)

		payload := AccountRegistrationPayload{
			Kind:        SystemPayloadKindAccountLocked,
			User:        user,
			HomeCluster: winningCluster,
			ParentSeq:   parentSeq,
			Attestations: []RegistrationAttestation{
				{ValidatorPubkey: v1.PublicKey(), Signature: sig1}, // only 1, required 2
			},
		}
		data, err := payload.MarshalProto()
		require.NoError(t, err)

		err = handler.ApplyAttested(db, nil, &mockCommitteeProvider{validators: committee}, data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "insufficient attestations: got 1, required 2")
		assert.False(t, db.GetLocked(user))
	})

	t.Run("officially confirmed account CANNOT be locked even with full committee signatures", func(t *testing.T) {
		db := newCoattestMockStateDB()
		db.SetParentRegistered(user, true)

		sig3 := bls.Sign(v3.PrivateKey(), digest)
		sig4 := bls.Sign(v4.PrivateKey(), digest)

		// All 4 committee members sign the lock payload
		payload := AccountRegistrationPayload{
			Kind:        SystemPayloadKindAccountLocked,
			User:        user,
			HomeCluster: winningCluster,
			ParentSeq:   parentSeq,
			Attestations: []RegistrationAttestation{
				{ValidatorPubkey: v1.PublicKey(), Signature: sig1},
				{ValidatorPubkey: v2.PublicKey(), Signature: sig2},
				{ValidatorPubkey: v3.PublicKey(), Signature: sig3},
				{ValidatorPubkey: v4.PublicKey(), Signature: sig4},
			},
		}
		data, err := payload.MarshalProto()
		require.NoError(t, err)

		err = handler.ApplyAttested(db, nil, &mockCommitteeProvider{validators: committee}, data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot lock officially confirmed account")
		assert.True(t, db.GetParentRegistered(user), "ParentRegistered must remain true")
		assert.False(t, db.GetLocked(user), "Account must NOT be locked")
	})
}

func TestAccountRegistryHandler_CoAttestation_LockedAccountCannotBeRegistered(t *testing.T) {
	bls.Init()

	v1 := bls.GenerateKeyPair()
	v2 := bls.GenerateKeyPair()
	committee := []cm.PublicKey{v1.PublicKey(), v2.PublicKey()}
	clusterKey := v1.PublicKey()
	user := common.HexToAddress("0x2222222222222222222222222222222222222222")
	chainID := uint64(991)
	parentSeq := uint64(50)

	handler := NewAccountRegistryHandler(clusterKey)
	handler.SetChainID(chainID)

	db := newCoattestMockStateDB()
	db.SetLocked(user, true)

	digest := ComputeAccountRegistrationAttestDigest(chainID, user, clusterKey, parentSeq)
	payload := AccountRegistrationPayload{
		Kind:       SystemPayloadKindAccountRegistered,
		User:       user,
		ClusterKey: clusterKey,
		ParentSeq:  parentSeq,
		Attestations: []RegistrationAttestation{
			{ValidatorPubkey: v1.PublicKey(), Signature: bls.Sign(v1.PrivateKey(), digest)},
			{ValidatorPubkey: v2.PublicKey(), Signature: bls.Sign(v2.PrivateKey(), digest)},
		},
	}
	data, err := payload.MarshalProto()
	require.NoError(t, err)

	err = handler.ApplyAttested(db, nil, &mockCommitteeProvider{validators: committee}, data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot register locked account")
	assert.False(t, db.GetParentRegistered(user))
	assert.True(t, db.GetLocked(user))
}

func TestAccountRegistryHandler_LockCoAttestationAccumulatesAcross4Keys(t *testing.T) {
	bls.Init()

	v1 := bls.GenerateKeyPair()
	v2 := bls.GenerateKeyPair()
	v3 := bls.GenerateKeyPair()
	v4 := bls.GenerateKeyPair()
	committee := []cm.PublicKey{v1.PublicKey(), v2.PublicKey(), v3.PublicKey(), v4.PublicKey()}
	winningCluster := bls.GenerateKeyPair().PublicKey()
	user := common.HexToAddress("0x3333333333333333333333333333333333333333")
	chainID := uint64(991)

	handler := NewAccountRegistryHandler(v1.PublicKey())
	handler.SetChainID(chainID)

	db := newCoattestMockStateDB()
	db.SetProvisional(user, true)
	scdb := newMemSCDB()
	attStore := NewDBAttestationStore(scdb)
	cp := &mockCommitteeProvider{validators: committee}

	digest := ComputeAccountLockedAttestDigest(chainID, user, winningCluster, 0)
	makeLockPayload := func(v *bls.KeyPair) []byte {
		p := AccountRegistrationPayload{
			Kind:        SystemPayloadKindAccountLocked,
			User:        user,
			HomeCluster: winningCluster,
			Attestations: []RegistrationAttestation{{
				ValidatorPubkey: v.PublicKey(),
				Signature:       bls.Sign(v.PrivateKey(), digest),
			}},
		}
		b, err := p.MarshalProto()
		require.NoError(t, err)
		return b
	}

	// 1st validator attestation: stored in pending, account stays provisional and unlocked.
	require.NoError(t, handler.ApplyAttested(db, attStore, cp, makeLockPayload(v1)))
	assert.False(t, db.GetLocked(user))
	assert.True(t, db.GetProvisional(user))
	assert.True(t, HasPendingLockAttestation(scdb, chainID, user, winningCluster, v1.PublicKey()))
	assert.False(t, HasPendingLockAttestation(scdb, chainID, user, winningCluster, v2.PublicKey()))

	// 2nd distinct validator attestation reaches f+1=2: locks account, clears provisional and pending store.
	require.NoError(t, handler.ApplyAttested(db, attStore, cp, makeLockPayload(v2)))
	assert.True(t, db.GetLocked(user))
	assert.False(t, db.GetProvisional(user))
	assert.False(t, HasPendingLockAttestation(scdb, chainID, user, winningCluster, v1.PublicKey()))
}

func TestAccountRegistryHandler_OnProvisionalAppliedCallback(t *testing.T) {
	bls.Init()
	clusterKP := bls.GenerateKeyPair()
	handler := NewAccountRegistryHandler(clusterKP.PublicKey())
	handler.SetChainID(991)

	var triggered []common.Address
	var triggeredTx []common.Hash
	handler.OnProvisionalApplied = func(u common.Address, txHash common.Hash) {
		triggered = append(triggered, u)
		triggeredTx = append(triggeredTx, txHash)
	}

	h := newRelayHarness()
	h.clusterKP = clusterKP
	h.relay = NewRegistrationRelay(h.parent, clusterKP.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(clusterKP.PrivateKey(), d) },
		func(common.Address) bool { return false })
	user, sig := h.userSig(t)

	digest := ComputeAccountProvisionalAttestDigest(991, user, clusterKP.PublicKey(), sig)
	p := AccountRegistrationPayload{
		Kind:       SystemPayloadKindAccountProvisional,
		User:       user,
		ClusterKey: clusterKP.PublicKey(),
		UserSig:    sig,
		Attestations: []RegistrationAttestation{{
			ValidatorPubkey: clusterKP.PublicKey(),
			Signature:       bls.Sign(clusterKP.PrivateKey(), digest),
		}},
	}
	data, err := p.MarshalProto()
	require.NoError(t, err)

	db := newCoattestMockStateDB()
	cp := &mockCommitteeProvider{validators: []cm.PublicKey{clusterKP.PublicKey()}}
	attStore := NewDBAttestationStore(newMemSCDB())

	// First application triggers callback once, carrying the system tx hash.
	txHash := common.HexToHash("0xabc1")
	require.NoError(t, handler.ApplyAttestedTx(db, attStore, cp, data, txHash))
	assert.Equal(t, []common.Address{user}, triggered)
	assert.Equal(t, []common.Hash{txHash}, triggeredTx)

	// Idempotent repeat does not re-trigger (neither through ApplyAttested nor ApplyAttestedTx).
	require.NoError(t, handler.ApplyAttested(db, attStore, cp, data))
	require.NoError(t, handler.ApplyAttestedTx(db, attStore, cp, data, common.HexToHash("0xabc2")))
	assert.Len(t, triggered, 1)

	// ApplyAttested (no tx context) reports a zero hash.
	other, otherSig := h.userSig(t)
	d2 := ComputeAccountProvisionalAttestDigest(991, other, clusterKP.PublicKey(), otherSig)
	p2 := AccountRegistrationPayload{
		Kind:       SystemPayloadKindAccountProvisional,
		User:       other,
		ClusterKey: clusterKP.PublicKey(),
		UserSig:    otherSig,
		Attestations: []RegistrationAttestation{{
			ValidatorPubkey: clusterKP.PublicKey(),
			Signature:       bls.Sign(clusterKP.PrivateKey(), d2),
		}},
	}
	data2, err := p2.MarshalProto()
	require.NoError(t, err)
	require.NoError(t, handler.ApplyAttested(db, attStore, cp, data2))
	require.Len(t, triggeredTx, 2)
	assert.Equal(t, other, triggered[1])
	assert.Equal(t, common.Hash{}, triggeredTx[1])

	// A locked account is rejected and never reported.
	locked, lockedSig := h.userSig(t)
	db.SetLocked(locked, true)
	d3 := ComputeAccountProvisionalAttestDigest(991, locked, clusterKP.PublicKey(), lockedSig)
	p3 := AccountRegistrationPayload{
		Kind:       SystemPayloadKindAccountProvisional,
		User:       locked,
		ClusterKey: clusterKP.PublicKey(),
		UserSig:    lockedSig,
		Attestations: []RegistrationAttestation{{
			ValidatorPubkey: clusterKP.PublicKey(),
			Signature:       bls.Sign(clusterKP.PrivateKey(), d3),
		}},
	}
	data3, err := p3.MarshalProto()
	require.NoError(t, err)
	assert.Error(t, handler.ApplyAttestedTx(db, attStore, cp, data3, common.HexToHash("0xabc3")))
	assert.Len(t, triggered, 2)
}
