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
	mu         sync.Mutex
	registered map[common.Address]bool
}

func newCoattestMockStateDB() *coattestMockStateDB {
	return &coattestMockStateDB{registered: make(map[common.Address]bool)}
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
