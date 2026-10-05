package rollup

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

type memSCDB struct {
	mu sync.Mutex
	m  map[common.Hash][]byte
}

func newMemSCDB() *memSCDB { return &memSCDB{m: map[common.Hash][]byte{}} }
func (d *memSCDB) StorageValue(_ common.Address, k common.Hash) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.m[k]
	return v, ok
}
func (d *memSCDB) SetStorageValue(_ common.Address, k common.Hash, v []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.m[k] = v
}

type accEnv struct {
	vals    []*bls.KeyPair
	cluster cm.PublicKey
	user    common.Address
	h       *AccountRegistryHandler
	db      *coattestMockStateDB
	sc      *memSCDB
	store   AttestationStore
}

func newAccEnv(n int) *accEnv {
	bls.Init()
	e := &accEnv{user: common.HexToAddress("0x00000000000000000000000000000000000000bb"), db: newCoattestMockStateDB(), sc: newMemSCDB()}
	for i := 0; i < n; i++ {
		e.vals = append(e.vals, bls.GenerateKeyPair())
	}
	e.cluster = bls.GenerateKeyPair().PublicKey()
	e.h = NewAccountRegistryHandler(e.cluster)
	e.store = NewDBAttestationStore(e.sc)
	return e
}

func (e *accEnv) committee(n int) *mockCommitteeProvider {
	m := &mockCommitteeProvider{}
	for i := 0; i < n; i++ {
		m.validators = append(m.validators, e.vals[i].PublicKey())
	}
	return m
}

func (e *accEnv) payload(signer *bls.KeyPair, seq uint64) []byte {
	d := ComputeAccountRegistrationAttestDigest(parentchain.ParentChainID, e.user, e.cluster, seq)
	p := AccountRegistrationPayload{Kind: SystemPayloadKindAccountRegistered, User: e.user, ClusterKey: e.cluster, ParentSeq: seq,
		Attestations: []RegistrationAttestation{{ValidatorPubkey: signer.PublicKey(), Signature: bls.Sign(signer.PrivateKey(), d)}}}
	b, _ := json.Marshal(p)
	return b
}

func TestAttestationsAccumulateAcrossTransactions(t *testing.T) {
	e := newAccEnv(4) // f=1 => 2 distinct validators needed
	c := e.committee(4)

	require.NoError(t, e.h.ApplyAttested(e.db, e.store, c, e.payload(e.vals[0], 1)))
	require.False(t, e.db.GetParentRegistered(e.user), "one validator alone (a Byzantine one) must not register the account")
	require.True(t, HasPendingAttestation(e.sc, parentchain.ParentChainID, e.user, e.cluster, 1, e.vals[0].PublicKey()))
	require.False(t, HasPendingAttestation(e.sc, parentchain.ParentChainID, e.user, e.cluster, 1, e.vals[1].PublicKey()))

	// The same validator repeating itself never counts twice.
	require.NoError(t, e.h.ApplyAttested(e.db, e.store, c, e.payload(e.vals[0], 1)))
	require.False(t, e.db.GetParentRegistered(e.user))

	require.NoError(t, e.h.ApplyAttested(e.db, e.store, c, e.payload(e.vals[1], 1)))
	require.True(t, e.db.GetParentRegistered(e.user), "f+1 distinct validators => registered")
	require.False(t, HasPendingAttestation(e.sc, parentchain.ParentChainID, e.user, e.cluster, 1, e.vals[0].PublicKey()), "pending set cleared after apply")
}

func TestAttestationsForDifferentEventsDoNotMix(t *testing.T) {
	e := newAccEnv(4)
	c := e.committee(4)
	require.NoError(t, e.h.ApplyAttested(e.db, e.store, c, e.payload(e.vals[0], 1)))
	require.NoError(t, e.h.ApplyAttested(e.db, e.store, c, e.payload(e.vals[1], 2))) // other parentSeq
	require.False(t, e.db.GetParentRegistered(e.user))
}

func TestAttestationRejections(t *testing.T) {
	e := newAccEnv(4)
	c := e.committee(3) // vals[3] is NOT in the committee
	require.Error(t, e.h.ApplyAttested(e.db, e.store, c, e.payload(e.vals[3], 1)), "non-committee signer")

	bad := e.payload(e.vals[0], 1)
	var p AccountRegistrationPayload
	require.NoError(t, json.Unmarshal(bad, &p))
	p.ParentSeq = 99 // signature was for seq 1
	bad, _ = json.Marshal(p)
	require.Error(t, e.h.ApplyAttested(e.db, e.store, c, bad), "signature over another digest")

	noAtt, _ := json.Marshal(AccountRegistrationPayload{Kind: SystemPayloadKindAccountRegistered, User: e.user, ClusterKey: e.cluster, ParentSeq: 1})
	require.Error(t, e.h.ApplyAttested(e.db, e.store, c, noAtt), "payload without attestation must not register under a committee")

	require.False(t, e.db.GetParentRegistered(e.user))
	require.Empty(t, e.sc.m, "rejected payloads must not leave pending state")
}

func TestAttestationSignerLeavingCommitteeNoLongerCounts(t *testing.T) {
	e := newAccEnv(4)
	require.NoError(t, e.h.ApplyAttested(e.db, e.store, e.committee(4), e.payload(e.vals[0], 1)))
	// committee changes: vals[0] is gone (replaced by a new member); its stored share must not count
	c4 := &mockCommitteeProvider{validators: []cm.PublicKey{e.vals[1].PublicKey(), e.vals[2].PublicKey(), e.vals[3].PublicKey(), bls.GenerateKeyPair().PublicKey()}} // n=4 => need 2
	require.NoError(t, e.h.ApplyAttested(e.db, e.store, c4, e.payload(e.vals[1], 1)))
	require.False(t, e.db.GetParentRegistered(e.user), "stale share of a removed validator must not complete the quorum")
}

func TestSingleValidatorCommitteeNeedsOneAttestation(t *testing.T) {
	e := newAccEnv(1)
	require.NoError(t, e.h.ApplyAttested(e.db, e.store, e.committee(1), e.payload(e.vals[0], 1)))
	require.True(t, e.db.GetParentRegistered(e.user))
}

func TestAttestationOrderDoesNotMatter(t *testing.T) {
	for _, order := range [][]int{{0, 1, 2}, {2, 1, 0}, {1, 2, 0}} {
		e := newAccEnv(7) // f=2 => 3 needed
		c := e.committee(7)
		for i, v := range order {
			require.NoError(t, e.h.ApplyAttested(e.db, e.store, c, e.payload(e.vals[v], 5)))
			require.Equal(t, i == 2, e.db.GetParentRegistered(e.user), "order %v step %d", order, i)
		}
	}
}
