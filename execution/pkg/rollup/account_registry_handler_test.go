package rollup

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRegistryStateDB struct {
	flags map[common.Address]bool
}

func newMockRegistryStateDB() *mockRegistryStateDB {
	return &mockRegistryStateDB{
		flags: make(map[common.Address]bool),
	}
}

func (m *mockRegistryStateDB) GetParentRegistered(addr common.Address) bool {
	return m.flags[addr]
}

func (m *mockRegistryStateDB) SetParentRegistered(addr common.Address, registered bool) {
	m.flags[addr] = registered
}

func makeRegistrationPayload(user common.Address, clusterKey cm.PublicKey, seq uint64) []byte {
	p := AccountRegistrationPayload{
		Kind:       SystemPayloadKindAccountRegistered,
		User:       user,
		ClusterKey: clusterKey,
		ParentSeq:  seq,
	}
	b, _ := json.Marshal(p)
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

		// Corrupt JSON
		assert.False(t, IsAccountRegistrationPayload([]byte("not json")))
		err = handler.Apply(db, []byte("not json"))
		require.Error(t, err)

		// Empty user address
		emptyUserPayload, _ := json.Marshal(AccountRegistrationPayload{
			Kind:       SystemPayloadKindAccountRegistered,
			User:       common.Address{},
			ClusterKey: clusterKey1,
			ParentSeq:  1,
		})
		err = handler.Apply(db, emptyUserPayload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty user address")

		// Wrong kind
		wrongKindPayload, _ := json.Marshal(map[string]interface{}{
			"kind": "wrong_kind",
			"user": user2.Hex(),
		})
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
