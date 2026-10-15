package rollup

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRegistryStateDB struct {
	flags       map[common.Address]bool
	provisional map[common.Address]bool
	locked      map[common.Address]bool
}

func newMockRegistryStateDB() *mockRegistryStateDB {
	return &mockRegistryStateDB{
		flags:       make(map[common.Address]bool),
		provisional: make(map[common.Address]bool),
		locked:      make(map[common.Address]bool),
	}
}

func (m *mockRegistryStateDB) GetParentRegistered(addr common.Address) bool {
	return m.flags[addr]
}

func (m *mockRegistryStateDB) SetParentRegistered(addr common.Address, registered bool) {
	m.flags[addr] = registered
}

func (m *mockRegistryStateDB) GetProvisional(addr common.Address) bool {
	return m.provisional[addr]
}

func (m *mockRegistryStateDB) SetProvisional(addr common.Address, provisional bool) {
	m.provisional[addr] = provisional
}

func (m *mockRegistryStateDB) GetLocked(addr common.Address) bool {
	return m.locked[addr]
}

func (m *mockRegistryStateDB) SetLocked(addr common.Address, locked bool) {
	m.locked[addr] = locked
}

func makeRegistrationPayload(user common.Address, clusterKey cm.PublicKey, seq uint64) []byte {
	p := AccountRegistrationPayload{
		Kind:       SystemPayloadKindAccountRegistered,
		User:       user,
		ClusterKey: clusterKey,
		ParentSeq:  seq,
	}
	b, _ := p.MarshalProto()
	return b
}

func TestAccountRegistryHandler_H1_Unit(t *testing.T) {
	var clusterKey1 cm.PublicKey
	copy(clusterKey1[:], []byte("cluster_one_public_key_32_bytes!"))
	var clusterKey2 cm.PublicKey
	copy(clusterKey2[:], []byte("cluster_two_public_key_32_bytes!"))

	handler := NewAccountRegistryHandler(clusterKey1)
	require.Equal(t, clusterKey1, handler.ClusterPublicKey())

	user1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	user2 := common.HexToAddress("0x2222222222222222222222222222222222222222")

	t.Run("H1_Success", func(t *testing.T) {
		db := newMockRegistryStateDB()
		assert.False(t, db.GetParentRegistered(user1))

		payload := makeRegistrationPayload(user1, clusterKey1, 1)
		assert.True(t, IsAccountRegistrationPayload(payload))

		err := handler.Apply(db, payload)
		require.NoError(t, err)
		assert.True(t, db.GetParentRegistered(user1))
	})

	t.Run("H1_Idempotent", func(t *testing.T) {
		db := newMockRegistryStateDB()
		payload := makeRegistrationPayload(user1, clusterKey1, 1)

		// First apply
		err := handler.Apply(db, payload)
		require.NoError(t, err)
		assert.True(t, db.GetParentRegistered(user1))

		// Second apply (duplicate)
		err = handler.Apply(db, payload)
		require.NoError(t, err)
		assert.True(t, db.GetParentRegistered(user1))
	})

	t.Run("H1_ClusterKeyMismatch", func(t *testing.T) {
		db := newMockRegistryStateDB()
		payload := makeRegistrationPayload(user1, clusterKey2, 1) // clusterKey2 != clusterKey1

		err := handler.Apply(db, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cluster key mismatch")
		assert.False(t, db.GetParentRegistered(user1), "flag must not be set on mismatch")
	})

	t.Run("H1_InvalidPayloads", func(t *testing.T) {
		db := newMockRegistryStateDB()

		// Nil DB
		err := handler.Apply(nil, makeRegistrationPayload(user1, clusterKey1, 1))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stateDB is nil")

		// Empty data
		assert.False(t, IsAccountRegistrationPayload(nil))
		assert.False(t, IsAccountRegistrationPayload([]byte{}))
		err = handler.Apply(db, []byte{})
		require.Error(t, err)

		// Corrupt protobuf
		assert.False(t, IsAccountRegistrationPayload([]byte("not protobuf")))
		err = handler.Apply(db, []byte("not protobuf"))
		require.Error(t, err)

		// Empty user address
		pEmpty := &AccountRegistrationPayload{
			Kind:       SystemPayloadKindAccountRegistered,
			User:       common.Address{},
			ClusterKey: clusterKey1,
			ParentSeq:  1,
		}
		emptyUserPayload, _ := pEmpty.MarshalProto()
		err = handler.Apply(db, emptyUserPayload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty user address")

		// Wrong kind
		pWrong := &AccountRegistrationPayload{
			Kind: "wrong_kind",
			User: user2,
		}
		wrongKindPayload, _ := pWrong.MarshalProto()
		assert.False(t, IsAccountRegistrationPayload(wrongKindPayload))
		err = handler.Apply(db, wrongKindPayload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected payload kind")
	})
}

func TestAccountRegistryHandler_H2_Determinism(t *testing.T) {
	var clusterKey cm.PublicKey
	copy(clusterKey[:], []byte("cluster_consensus_key_32_bytes!"))
	handler := NewAccountRegistryHandler(clusterKey)

	user1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	user2 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	user3 := common.HexToAddress("0x3333333333333333333333333333333333333333")
	userUnregistered := common.HexToAddress("0x4444444444444444444444444444444444444444")

	p1 := makeRegistrationPayload(user1, clusterKey, 1)
	p2 := makeRegistrationPayload(user2, clusterKey, 2)
	p3 := makeRegistrationPayload(user3, clusterKey, 3)

	// Replica 1 applies sequence: [p1, p2, p1 (dup), p3]
	replica1 := newMockRegistryStateDB()
	for _, p := range [][]byte{p1, p2, p1, p3} {
		require.NoError(t, handler.Apply(replica1, p))
	}

	// Replica 2 applies sequence: [p2, p1, p3, p2 (dup)]
	replica2 := newMockRegistryStateDB()
	for _, p := range [][]byte{p2, p1, p3, p2} {
		require.NoError(t, handler.Apply(replica2, p))
	}

	// Replica 3 applies sequence: [p3, p1, p2]
	replica3 := newMockRegistryStateDB()
	for _, p := range [][]byte{p3, p1, p2} {
		require.NoError(t, handler.Apply(replica3, p))
	}

	// Verify all replicas arrive at identical deterministic state
	allUsers := []common.Address{user1, user2, user3, userUnregistered}
	for _, u := range allUsers {
		f1 := replica1.GetParentRegistered(u)
		f2 := replica2.GetParentRegistered(u)
		f3 := replica3.GetParentRegistered(u)

		assert.Equal(t, f1, f2, "replica1 and replica2 must match for %s", u.Hex())
		assert.Equal(t, f1, f3, "replica1 and replica3 must match for %s", u.Hex())
	}

	assert.True(t, replica1.GetParentRegistered(user1))
	assert.True(t, replica1.GetParentRegistered(user2))
	assert.True(t, replica1.GetParentRegistered(user3))
	assert.False(t, replica1.GetParentRegistered(userUnregistered))

	// Hash state snapshot of each replica to verify 100% hash equivalence
	computeStateFingerprint := func(db *mockRegistryStateDB) common.Hash {
		var combined []byte
		for _, u := range allUsers {
			b := byte(0)
			if db.GetParentRegistered(u) {
				b = 1
			}
			combined = append(combined, u.Bytes()...)
			combined = append(combined, b)
		}
		return crypto.Keccak256Hash(combined)
	}

	hash1 := computeStateFingerprint(replica1)
	hash2 := computeStateFingerprint(replica2)
	hash3 := computeStateFingerprint(replica3)

	assert.Equal(t, hash1, hash2, "Replicas must produce identical state fingerprint")
	assert.Equal(t, hash1, hash3, "Replicas must produce identical state fingerprint")
}

func TestAccountRegistryHandler_S1_ConfirmedAccountCannotBeLocked(t *testing.T) {
	clusterKey := bls.GenerateKeyPair()
	handler := NewAccountRegistryHandler(clusterKey.PublicKey())
	handler.SetChainID(parentchain.ParentChainID)

	db := newMockRegistryStateDB()
	user := common.HexToAddress("0x1111111111111111111111111111111111111111")
	// Account is already confirmed on this cluster
	db.SetParentRegistered(user, true)

	// An attacker or rogue validator tries to send a lock payload
	winningCluster := bls.GenerateKeyPair()
	digest := ComputeAccountLockedAttestDigest(parentchain.ParentChainID, user, winningCluster.PublicKey(), 0)
	lockPayload := AccountRegistrationPayload{
		Kind:        SystemPayloadKindAccountLocked,
		User:        user,
		HomeCluster: winningCluster.PublicKey(),
		Attestations: []RegistrationAttestation{{
			ValidatorPubkey: clusterKey.PublicKey(),
			Signature:       bls.Sign(clusterKey.PrivateKey(), digest),
		}},
	}
	data, err := lockPayload.MarshalProto()
	require.NoError(t, err)

	err = handler.Apply(db, data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot lock officially confirmed account")

	// Invariants hold: account remains ParentRegistered and is NOT locked
	assert.True(t, db.GetParentRegistered(user))
	assert.False(t, db.GetLocked(user))
}

func TestAccountRegistryHandler_S1_UntrustedBLSKeyCannotLock(t *testing.T) {
	clusterKey := bls.GenerateKeyPair()
	handler := NewAccountRegistryHandler(clusterKey.PublicKey())
	handler.SetChainID(parentchain.ParentChainID)

	db := newMockRegistryStateDB()
	user := common.HexToAddress("0x2222222222222222222222222222222222222222")
	db.SetProvisional(user, true)

	// Attacker generates an arbitrary BLS keypair
	attackerKey := bls.GenerateKeyPair()
	winningCluster := bls.GenerateKeyPair()
	digest := ComputeAccountLockedAttestDigest(parentchain.ParentChainID, user, winningCluster.PublicKey(), 0)
	lockPayload := AccountRegistrationPayload{
		Kind:        SystemPayloadKindAccountLocked,
		User:        user,
		HomeCluster: winningCluster.PublicKey(),
		Attestations: []RegistrationAttestation{{
			ValidatorPubkey: attackerKey.PublicKey(),
			Signature:       bls.Sign(attackerKey.PrivateKey(), digest),
		}},
	}
	data, err := lockPayload.MarshalProto()
	require.NoError(t, err)

	err = handler.Apply(db, data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock attestation from untrusted validator")

	// Account remains provisional and is NOT locked
	assert.True(t, db.GetProvisional(user))
	assert.False(t, db.GetLocked(user))
}

func TestAccountRegistryHandler_S2_LockedAccountCannotBeRegistered(t *testing.T) {
	clusterKey := bls.GenerateKeyPair()
	handler := NewAccountRegistryHandler(clusterKey.PublicKey())
	handler.SetChainID(parentchain.ParentChainID)

	db := newMockRegistryStateDB()
	user := common.HexToAddress("0x3333333333333333333333333333333333333333")
	// Account is already locked
	db.SetLocked(user, true)

	regPayload := makeRegistrationPayload(user, clusterKey.PublicKey(), 1)
	err := handler.Apply(db, regPayload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot register locked account")

	// Invariants hold: account remains locked and is NOT ParentRegistered
	assert.True(t, db.GetLocked(user))
	assert.False(t, db.GetParentRegistered(user))
}

func TestAccountRegistryHandler_S1b_ClusterKeyLockAllowed_AttackerRejected(t *testing.T) {
	clusterKP := bls.GenerateKeyPair()
	attacker := bls.GenerateKeyPair()
	winningCluster := bls.GenerateKeyPair()
	h := NewAccountRegistryHandler(clusterKP.PublicKey())
	h.SetChainID(parentchain.ParentChainID)

	db := newMockRegistryStateDB()
	provUser := common.HexToAddress("0x4444444444444444444444444444444444444444")
	db.SetProvisional(provUser, true)

	// 1. Attacker tries to lock: must fail
	attackerDigest := ComputeAccountLockedAttestDigest(parentchain.ParentChainID, provUser, winningCluster.PublicKey(), 0)
	attackerPayload := AccountRegistrationPayload{
		Kind:        SystemPayloadKindAccountLocked,
		User:        provUser,
		HomeCluster: winningCluster.PublicKey(),
		Attestations: []RegistrationAttestation{{
			ValidatorPubkey: attacker.PublicKey(),
			Signature:       bls.Sign(attacker.PrivateKey(), attackerDigest),
		}},
	}
	bAttacker, err := attackerPayload.MarshalProto()
	require.NoError(t, err)
	err = h.ApplyAttested(db, nil, nil, bAttacker)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock attestation from untrusted validator")
	assert.False(t, db.GetLocked(provUser), "Account must NOT be locked by attacker")

	// 2. Cluster key itself signs lock: must succeed
	clusterDigest := ComputeAccountLockedAttestDigest(parentchain.ParentChainID, provUser, winningCluster.PublicKey(), 0)
	clusterPayload := AccountRegistrationPayload{
		Kind:        SystemPayloadKindAccountLocked,
		User:        provUser,
		HomeCluster: winningCluster.PublicKey(),
		Attestations: []RegistrationAttestation{{
			ValidatorPubkey: clusterKP.PublicKey(),
			Signature:       bls.Sign(clusterKP.PrivateKey(), clusterDigest),
		}},
	}
	bCluster, err := clusterPayload.MarshalProto()
	require.NoError(t, err)
	err = h.ApplyAttested(db, nil, nil, bCluster)
	require.NoError(t, err)
	assert.True(t, db.GetLocked(provUser), "Account must be locked by cluster key")
}
