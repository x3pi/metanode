package parentchain

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func batchStores() map[string]func() Store {
	return map[string]func() Store{
		"MemoryStore": func() Store { return NewMemoryStore() },
		"TreeStore":   func() Store { return NewTreeStore(NewMemTreeKV()) },
	}
}

func batchEntry(t *testing.T, cluster *bls.KeyPair) (RegisterAccountEntry, common.Address) {
	t.Helper()
	priv, addr := createTestUser(t)
	return RegisterAccountEntry{
		UserAddress:      addr,
		FloatIdentityKey: cluster.PublicKey(),
		UserSig:          signUserRegistration(t, priv, addr, cluster.PublicKey()),
		ClusterSig:       bls.Sign(cluster.PrivateKey(), ComputeRegisterAccountMessage(addr, cluster.PublicKey())),
	}, addr
}

func TestRegisterAccountsBatch_CallDataRoundTripAndBounds(t *testing.T) {
	cluster := bls.GenerateKeyPair()
	e1, _ := batchEntry(t, cluster)
	e2, _ := batchEntry(t, cluster)

	got, err := DecodeRegisterAccountsBatchCallData(EncodeRegisterAccountsBatchCallData([]RegisterAccountEntry{e1, e2}))
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, e1.UserAddress, got[0].UserAddress)
	assert.Equal(t, e1.UserSig, got[0].UserSig)
	assert.Equal(t, e1.ClusterSig, got[0].ClusterSig)
	assert.Equal(t, e2.UserAddress, got[1].UserAddress)

	_, err = DecodeRegisterAccountsBatchCallData(EncodeRegisterAccountsBatchCallData(nil))
	assert.ErrorIs(t, err, ErrMalformedCallData, "empty batch")
	big := make([]RegisterAccountEntry, MaxRegisterAccountsBatch+1)
	for i := range big {
		big[i] = e1
	}
	_, err = DecodeRegisterAccountsBatchCallData(EncodeRegisterAccountsBatchCallData(big))
	assert.ErrorIs(t, err, ErrMalformedCallData, "oversized batch")
}

// One transaction registers every valid entry; an entry that fails (already owned elsewhere, bad user signature) is
// skipped without reverting the others, and the sender's nonce advances once.
func TestRegisterAccountsBatch_ExecutesEntriesIndependently(t *testing.T) {
	for name, newStore := range batchStores() {
		t.Run(name, func(t *testing.T) {
			store := newStore()
			cluster := bls.GenerateKeyPair()
			other := bls.GenerateKeyPair()
			clusterHash := crypto.Keccak256Hash(cluster.PublicKey().Bytes())

			ok1, a1 := batchEntry(t, cluster)
			ok2, a2 := batchEntry(t, cluster)
			taken, takenAddr := batchEntry(t, cluster)
			require.NoError(t, store.SetAccountRegistry(takenAddr, other.PublicKey())) // won by another cluster first
			bad, badAddr := batchEntry(t, cluster)
			bad.UserSig = append([]byte(nil), bad.UserSig...)
			bad.UserSig[10] ^= 0xff
			ok3, a3 := batchEntry(t, cluster)

			tx, err := BuildAndSignBLSTx(cluster.PrivateKey(), cluster.PublicKey(), ParentChainGatewayAddress, 0,
				EncodeRegisterAccountsBatchCallData([]RegisterAccountEntry{ok1, ok2, taken, bad, ok3}))
			require.NoError(t, err)

			// Admission (HTTP pre-filter) accepts a batch from a cluster not yet in the AccountRegistry.
			senderKey, err := VerifyTxSignature(tx, store)
			require.NoError(t, err)
			require.Equal(t, cluster.PublicKey(), senderKey)

			rcpt, err := ExecuteTx(store, tx, 1)
			require.NoError(t, err)
			require.Equal(t, uint8(1), rcpt.Status)

			for _, a := range []common.Address{a1, a2, a3} {
				k, found, err := store.GetAccountRegistry(a)
				require.NoError(t, err)
				require.True(t, found)
				assert.Equal(t, cluster.PublicKey(), k)
			}
			k, _, _ := store.GetAccountRegistry(takenAddr)
			assert.Equal(t, other.PublicKey(), k, "an address owned elsewhere is untouched")
			_, found, _ := store.GetAccountRegistry(badAddr)
			assert.False(t, found, "an entry with a bad user signature is skipped")

			events, _, err := store.GetAccountRegistrations(clusterHash, 0)
			require.NoError(t, err)
			require.Len(t, events, 3)
			for i, ev := range events {
				assert.Equal(t, uint64(i), ev.Seq, "inbound sequence stays dense")
			}
			assert.Equal(t, []common.Address{a1, a2, a3}, []common.Address{events[0].UserAddress, events[1].UserAddress, events[2].UserAddress})

			nonce, err := store.GetNonce(common.BytesToAddress(tx.FromAddress))
			require.NoError(t, err)
			assert.Equal(t, uint64(1), nonce, "one batch = one nonce")
		})
	}
}

// A cluster may only batch registrations to itself.
func TestRegisterAccountsBatch_RejectsEntriesForAnotherCluster(t *testing.T) {
	for name, newStore := range batchStores() {
		t.Run(name, func(t *testing.T) {
			store := newStore()
			cluster := bls.GenerateKeyPair()
			other := bls.GenerateKeyPair()
			own, ownAddr := batchEntry(t, cluster)
			foreign, foreignAddr := batchEntry(t, other)

			tx, err := BuildAndSignBLSTx(cluster.PrivateKey(), cluster.PublicKey(), ParentChainGatewayAddress, 0,
				EncodeRegisterAccountsBatchCallData([]RegisterAccountEntry{own, foreign}))
			require.NoError(t, err)
			rcpt, err := ExecuteTx(store, tx, 1)
			require.ErrorIs(t, err, ErrUnauthorizedSender)
			assert.Equal(t, uint8(0), rcpt.Status)
			assert.Equal(t, uint32(226), rcpt.ErrorCode)
			for _, a := range []common.Address{ownAddr, foreignAddr} {
				_, found, _ := store.GetAccountRegistry(a)
				assert.False(t, found, "nothing of a rejected batch is applied")
			}
		})
	}
}

func TestRegisterAccountsBatch_MalformedBatchFailsWithCode225(t *testing.T) {
	store := NewMemoryStore()
	cluster := bls.GenerateKeyPair()
	e, _ := batchEntry(t, cluster)
	big := make([]RegisterAccountEntry, MaxRegisterAccountsBatch+1)
	for i := range big {
		big[i] = e
	}
	tx, err := BuildAndSignBLSTx(cluster.PrivateKey(), cluster.PublicKey(), ParentChainGatewayAddress, 0,
		EncodeRegisterAccountsBatchCallData(big))
	require.NoError(t, err)
	rcpt, err := ExecuteTx(store, tx, 1)
	require.Error(t, err)
	assert.Equal(t, uint32(225), rcpt.ErrorCode)
}
