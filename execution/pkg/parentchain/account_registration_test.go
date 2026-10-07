package parentchain

import (
	"crypto/ecdsa"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestUser(t *testing.T) (*ecdsa.PrivateKey, common.Address) {
	priv, err := crypto.GenerateKey()
	require.NoError(t, err)
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	return priv, addr
}

func signUserRegistration(t *testing.T, userPriv *ecdsa.PrivateKey, userAddr common.Address, clusterPubKey cm.PublicKey) []byte {
	digest := ComputeRegisterAccountMessage(userAddr, clusterPubKey)
	userHash := crypto.Keccak256Hash(digest)
	sig, err := crypto.Sign(userHash.Bytes(), userPriv)
	require.NoError(t, err)
	return sig
}

func TestParentStores_AccountRegistrations(t *testing.T) {
	// Create factories for the stores to test
	stores := map[string]func() Store{
		"MemoryStore": func() Store {
			return NewMemoryStore()
		},
		"TreeStore": func() Store {
			return NewTreeStore(NewMemTreeKV())
		},
	}

	for storeName, createStore := range stores {
		t.Run(storeName, func(t *testing.T) {
			store := createStore()

			cluster1 := bls.GenerateKeyPair()
			cluster2 := bls.GenerateKeyPair()

			cluster1Hash := crypto.Keccak256Hash(cluster1.PublicKey().Bytes())
			cluster2Hash := crypto.Keccak256Hash(cluster2.PublicKey().Bytes())

			user1Priv, user1Addr := createTestUser(t)
			user2Priv, user2Addr := createTestUser(t)
			user3Priv, user3Addr := createTestUser(t)

			// 1. Initial query on empty store: returns empty, cursor 0
			events, nextCur, err := store.GetAccountRegistrations(cluster1Hash, 0)
			require.NoError(t, err)
			assert.Empty(t, events)
			assert.Equal(t, uint64(0), nextCur)

			// 2. Register user1 to cluster 1
			u1Sig := signUserRegistration(t, user1Priv, user1Addr, cluster1.PublicKey())
			c1Sig1 := bls.Sign(cluster1.PrivateKey(), ComputeRegisterAccountMessage(user1Addr, cluster1.PublicKey()))
			err = RegisterAccount(store, user1Addr, cluster1.PublicKey(), u1Sig, c1Sig1)
			require.NoError(t, err)

			// Verify 1 event in cluster 1
			events, nextCur, err = store.GetAccountRegistrations(cluster1Hash, 0)
			require.NoError(t, err)
			require.Len(t, events, 1)
			assert.Equal(t, uint64(0), events[0].Seq)
			assert.Equal(t, user1Addr, events[0].UserAddress)
			assert.Equal(t, cluster1.PublicKey(), events[0].ClusterKey)
			assert.Equal(t, uint64(1), nextCur)

			// Verify cluster 2 sees NO events
			eventsC2, nextCurC2, err := store.GetAccountRegistrations(cluster2Hash, 0)
			require.NoError(t, err)
			assert.Empty(t, eventsC2)
			assert.Equal(t, uint64(0), nextCurC2)

			// 3. Duplicate registration of user1 must be rejected with ErrAccountAlreadyRegistered and produce NO new event
			err = RegisterAccount(store, user1Addr, cluster1.PublicKey(), u1Sig, c1Sig1)
			require.ErrorIs(t, err, ErrAccountAlreadyRegistered)

			events, nextCur, err = store.GetAccountRegistrations(cluster1Hash, 0)
			require.NoError(t, err)
			require.Len(t, events, 1, "Duplicate registration must not emit a second event")
			assert.Equal(t, uint64(1), nextCur)

			// 4. Duplicate registration of user1 to cluster 2 must also be rejected
			u1SigC2 := signUserRegistration(t, user1Priv, user1Addr, cluster2.PublicKey())
			c2Sig1 := bls.Sign(cluster2.PrivateKey(), ComputeRegisterAccountMessage(user1Addr, cluster2.PublicKey()))
			err = RegisterAccount(store, user1Addr, cluster2.PublicKey(), u1SigC2, c2Sig1)
			require.ErrorIs(t, err, ErrAccountAlreadyRegistered)

			// 5. Register user2 to cluster 2
			u2Sig := signUserRegistration(t, user2Priv, user2Addr, cluster2.PublicKey())
			c2Sig2 := bls.Sign(cluster2.PrivateKey(), ComputeRegisterAccountMessage(user2Addr, cluster2.PublicKey()))
			err = RegisterAccount(store, user2Addr, cluster2.PublicKey(), u2Sig, c2Sig2)
			require.NoError(t, err)

			// Cluster 2 has Seq 0 for user2
			eventsC2, nextCurC2, err = store.GetAccountRegistrations(cluster2Hash, 0)
			require.NoError(t, err)
			require.Len(t, eventsC2, 1)
			assert.Equal(t, uint64(0), eventsC2[0].Seq)
			assert.Equal(t, user2Addr, eventsC2[0].UserAddress)
			assert.Equal(t, uint64(1), nextCurC2)

			// 6. Register user3 to cluster 1 -> Seq must be 1 (strictly monotonic)
			u3Sig := signUserRegistration(t, user3Priv, user3Addr, cluster1.PublicKey())
			c1Sig3 := bls.Sign(cluster1.PrivateKey(), ComputeRegisterAccountMessage(user3Addr, cluster1.PublicKey()))
			err = RegisterAccount(store, user3Addr, cluster1.PublicKey(), u3Sig, c1Sig3)
			require.NoError(t, err)

			events, nextCur, err = store.GetAccountRegistrations(cluster1Hash, 0)
			require.NoError(t, err)
			require.Len(t, events, 2)
			assert.Equal(t, uint64(0), events[0].Seq)
			assert.Equal(t, uint64(1), events[1].Seq)
			assert.Equal(t, user1Addr, events[0].UserAddress)
			assert.Equal(t, user3Addr, events[1].UserAddress)
			assert.Equal(t, uint64(2), nextCur)

			// Query with cursor = 1 -> returns only user3, next cursor = 2
			eventsFrom1, nextCurFrom1, err := store.GetAccountRegistrations(cluster1Hash, 1)
			require.NoError(t, err)
			require.Len(t, eventsFrom1, 1)
			assert.Equal(t, user3Addr, eventsFrom1[0].UserAddress)
			assert.Equal(t, uint64(2), nextCurFrom1)

			// 7. Pagination test: insert 55 more accounts to cluster 1
			for i := 0; i < 55; i++ {
				uPriv, uAddr := createTestUser(t)
				uSig := signUserRegistration(t, uPriv, uAddr, cluster1.PublicKey())
				cSig := bls.Sign(cluster1.PrivateKey(), ComputeRegisterAccountMessage(uAddr, cluster1.PublicKey()))
				require.NoError(t, RegisterAccount(store, uAddr, cluster1.PublicKey(), uSig, cSig))
			}

			// Total in cluster 1 is 2 + 55 = 57 accounts
			// Page 1: from 0, should return 50 items, nextCursor 50
			page1, p1Next, err := store.GetAccountRegistrations(cluster1Hash, 0)
			require.NoError(t, err)
			assert.Len(t, page1, 50)
			assert.Equal(t, uint64(50), p1Next)
			assert.Equal(t, uint64(0), page1[0].Seq)
			assert.Equal(t, uint64(49), page1[49].Seq)

			// Page 2: from 50, should return 7 items, nextCursor 57
			page2, p2Next, err := store.GetAccountRegistrations(cluster1Hash, 50)
			require.NoError(t, err)
			assert.Len(t, page2, 7)
			assert.Equal(t, uint64(57), p2Next)
			assert.Equal(t, uint64(50), page2[0].Seq)
			assert.Equal(t, uint64(56), page2[6].Seq)

			// Page 3: from 57, should return 0 items, nextCursor 57
			page3, p3Next, err := store.GetAccountRegistrations(cluster1Hash, 57)
			require.NoError(t, err)
			assert.Empty(t, page3)
			assert.Equal(t, uint64(57), p3Next)
		})
	}
}

func TestParentProof_AccountRegistrations(t *testing.T) {
	memTree := NewMemTreeKV()
	store := NewTreeStore(memTree)

	cluster := bls.GenerateKeyPair()
	clusterHash := crypto.Keccak256Hash(cluster.PublicKey().Bytes())
	userPriv, userAddr := createTestUser(t)

	uSig := signUserRegistration(t, userPriv, userAddr, cluster.PublicKey())
	cSig := bls.Sign(cluster.PrivateKey(), ComputeRegisterAccountMessage(userAddr, cluster.PublicKey()))
	require.NoError(t, RegisterAccount(store, userAddr, cluster.PublicKey(), uSig, cSig))

	// Verify that the event is stored in TreeKV with NamespaceAccountRegistrationLog and NamespaceAccountRegistrationSeq
	seqKey := TreeKey(NamespaceAccountRegistrationSeq, clusterHash.Bytes())
	seqBytes, found, err := memTree.Get(seqKey)
	require.NoError(t, err)
	assert.True(t, found)
	seq, err := DecodeUint64(seqBytes)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), seq)

	evKey := TreeKey(NamespaceAccountRegistrationLog, append(clusterHash.Bytes(), EncodeUint64(0)...))
	evBytes, found, err := memTree.Get(evKey)
	require.NoError(t, err)
	assert.True(t, found)
	ev, err := DecodeAccountRegisteredEvent(evBytes)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), ev.Seq)
	assert.Equal(t, userAddr, ev.UserAddress)
	assert.Equal(t, cluster.PublicKey(), ev.ClusterKey)
}
