package tx_processor

// In-process end-to-end tests of account registration: a REAL Parent Chain execution path (parentchain.ExecuteTx on a
// MemoryStore, served by the real parentchain.HTTPServer and reached through the real parentchain.QuorumClient over
// HTTP) and a REAL execution cluster state (chain state, stake-DB committee of 4 distinct BLS keys, RollupSystemHandler
// applying system transactions, account gate admission). Only block production is simulated: a proposed system tx is
// executed immediately, and parent transactions are executed by a goroutine in arrival order.

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
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
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

// e2eParent is a single Parent Chain node: real HTTP API, real transaction execution, switchable outage.
type e2eParent struct {
	store    *parentchain.MemoryStore
	srv      *httptest.Server
	down     atomic.Bool
	txs      chan *pb.Transaction
	executed atomic.Int64
	wg       sync.WaitGroup
}

func newE2EParent(t *testing.T) *e2eParent {
	t.Helper()
	p := &e2eParent{store: parentchain.NewMemoryStore(), txs: make(chan *pb.Transaction, 1024)}
	api := parentchain.NewHTTPServer(p.store)
	api.SetTxChan(p.txs)
	routes := api.Handler()
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.down.Load() {
			http.Error(w, "parent chain down", http.StatusServiceUnavailable)
			return
		}
		routes.ServeHTTP(w, r)
	}))
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for tx := range p.txs {
			_, _ = parentchain.ExecuteTx(p.store, tx, uint64(time.Now().Unix()))
			p.executed.Add(1)
		}
	}()
	t.Cleanup(func() {
		p.srv.Close()
		close(p.txs)
		p.wg.Wait()
	})
	return p
}

func (p *e2eParent) owner(t *testing.T, a common.Address) (cm.PublicKey, bool) {
	t.Helper()
	k, found, err := p.store.GetAccountRegistry(a)
	require.NoError(t, err)
	return k, found
}

// e2eCluster is one execution cluster whose committee has 4 distinct BLS keys (f+1 = 2).
type e2eCluster struct {
	*gateCluster
	validators []*bls.KeyPair // validators[0] is the node that receives the user's RPC (= g.kp)
	workers    []*rollup.RegistrationWorker
	client     *parentchain.QuorumClient
}

func newE2ECluster(t *testing.T, parentURL string) *e2eCluster {
	t.Helper()
	cs := setupTestChainState(t)
	cs.GetConfig().TxSignatureMode = config.TxSignatureModeSecp
	cs.GetConfig().AccountGate = config.AccountGateParentRegistered

	kp := bls.GenerateKeyPair()
	node := state.NewAccountState(kp.Address())
	node.AddBalance(big.NewInt(1_000_000_000_000_000_000))
	node.SetPublicKeyBls(kp.PublicKey().Bytes())
	cs.GetAccountStateDB().SetState(node)
	rec := state.NewAccountState(common.HexToAddress("0x456"))
	rec.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(rec)

	vals := []*bls.KeyPair{kp, bls.GenerateKeyPair(), bls.GenerateKeyPair(), bls.GenerateKeyPair()}
	for _, v := range vals {
		addTestCommitteeValidator(t, cs, v)
	}
	flushTestStake(t, cs)
	committee, err := NewLiveCommitteeProvider(cs).GetActiveCommitteeBLSKeys()
	require.NoError(t, err)
	require.Len(t, committee, 4, "committee of 4 distinct keys: f+1 = 2")

	c := &e2eCluster{
		gateCluster: &gateCluster{t: t, cs: cs, kp: kp},
		validators:  vals,
		client:      parentchain.NewQuorumClient([]string{parentURL}, kp.PrivateKey(), kp.PublicKey()),
	}
	c.h = &RollupSystemHandler{
		dispatcher:      systemDispatcher(func(rollup.Store, AccountStateAccessor, []byte) error { return nil }),
		registryHandler: rollup.NewAccountRegistryHandler(kp.PublicKey()),
	}
	c.relay = c.newRelay(nil)
	// Every validator runs its own RegistrationWorker and attests with its own committee key.
	accessor := newLiveAccountStateAccessor(cs).(*liveAccountStateAccessor)
	for _, v := range vals {
		w := rollup.NewRegistrationWorker(accessor, c.client, kp, kp.PublicKey())
		w.SetAttestationKey(v)
		w.EventProposer = c.execSystemTx
		c.workers = append(c.workers, w)
	}
	c.worker = c.workers[0]
	return c
}

// execSystemTx is "propose a system tx and have it committed in the next block".
func (c *e2eCluster) execSystemTx(payload []byte) error {
	tx := transaction.NewTransaction(c.kp.Address(), rollup.RollupSystemAddress, big.NewInt(0), 21000, 1_000_000_000, 0, payload, nil,
		common.Hash{}, common.Hash{}, c.nonce, 1)
	c.nonce++
	rcp, _, err := c.h.HandleTransaction(context.Background(), c.cs, tx, rollup.RollupSystemAddress, false, 0)
	if err != nil {
		return err
	}
	if rcp.Status() != pb.RECEIPT_STATUS_RETURNED {
		return assert.AnError
	}
	return nil
}

// newRelay wires a relay exactly like app.go: on-chain checkers, provisional proposer signed by the RPC node's key.
func (c *e2eCluster) newRelay(store rollup.RegistrationRelayStore) *rollup.RegistrationRelay {
	accessor := newLiveAccountStateAccessor(c.cs).(*liveAccountStateAccessor)
	r := rollup.NewRegistrationRelay(c.client, c.kp.PublicKey(),
		func(digest []byte) cm.Sign { return bls.Sign(c.kp.PrivateKey(), digest) },
		accessor.GetParentRegistered)
	r.SetProvisionalChecker(accessor.GetProvisional)
	r.SetLockedChecker(accessor.GetLocked)
	r.SetBatchSize(parentchain.MaxRegisterAccountsBatch)
	chainID := c.h.registryHandler.(*rollup.AccountRegistryHandler).AttestChainID()
	r.SetProvisionalProposer(func(user common.Address, userSig []byte) error {
		p := rollup.AccountRegistrationPayload{Kind: rollup.SystemPayloadKindAccountProvisional, User: user, ClusterKey: c.kp.PublicKey(), UserSig: userSig}
		digest := rollup.ComputeAccountProvisionalAttestDigest(chainID, user, c.kp.PublicKey(), userSig)
		p.Attestations = []rollup.RegistrationAttestation{{ValidatorPubkey: c.kp.PublicKey(), Signature: bls.Sign(c.kp.PrivateKey(), digest)}}
		b, err := p.MarshalProto()
		if err != nil {
			return err
		}
		return c.execSystemTx(b)
	})
	if store != nil {
		require.NoError(c.t, r.SetStore(store))
	}
	return r
}

func (c *e2eCluster) onChain(t *testing.T, a common.Address) *state.AccountState {
	t.Helper()
	as, err := c.cs.GetAccountStateDB().AccountState(a)
	require.NoError(t, err)
	require.NotNil(t, as)
	return as.(*state.AccountState)
}

func (c *e2eCluster) pollWorkers(n int) {
	for _, w := range c.workers[:n] {
		w.PollAndProcess()
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 10*time.Millisecond, what)
}

// E2E-1: parent up, 5 users at once, committee of 4 keys.
// One batch tx on the parent; every account usable at once (provisional, ONE attestation); confirmation still needs
// f+1 = 2 validator attestations.
func TestRegistrationE2E_BatchProvisionalAndConfirmation(t *testing.T) {
	parent := newE2EParent(t)
	c := newE2ECluster(t, parent.srv.URL)

	var users []testUser
	for i := 0; i < 5; i++ {
		u := newTestUser(t)
		c.fund(u)
		require.Equal(t, rollup.RegStatusPending, c.register(u).Status)
		users = append(users, u)
	}
	c.relay.ProcessBatch()

	waitFor(t, "parent executes the batch", func() bool { return parent.executed.Load() >= 1 })
	assert.Equal(t, int64(1), parent.executed.Load(), "5 registrations = ONE parent transaction")
	nonce, err := parent.store.GetNonce(c.kp.Address())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), nonce, "one parent nonce used")
	for _, u := range users {
		k, found := parent.owner(t, u.addr)
		require.True(t, found)
		assert.Equal(t, c.kp.PublicKey(), k)

		as := c.onChain(t, u.addr)
		assert.True(t, as.IsProvisional(), "provisional with a single committee attestation on a 4-key committee")
		assert.Equal(t, rollup.RegStatusProvisional, c.relay.Status(u.addr).Status)
		assert.Nil(t, c.admit(userTx(t, u, 1), u), "a provisional account may transact")
	}

	// One validator's attestation is not enough to CONFIRM (f+1 = 2) ...
	c.pollWorkers(1)
	assert.False(t, c.onChain(t, users[0].addr).ParentRegistered())
	// ... a second validator's attestation is.
	c.pollWorkers(2)
	c.relay.ProcessBatch()
	for _, u := range users {
		as := c.onChain(t, u.addr)
		assert.True(t, as.ParentRegistered())
		assert.False(t, as.IsProvisional(), "confirmation clears the provisional flag")
		assert.Equal(t, rollup.RegStatusConfirmed, c.relay.Status(u.addr).Status)
	}
}

// E2E-2: parent down at registration time. The account becomes usable immediately (provisional); when the parent comes
// back the registration is relayed and confirmed without any user action.
func TestRegistrationE2E_ParentOutageThenRecovery(t *testing.T) {
	parent := newE2EParent(t)
	c := newE2ECluster(t, parent.srv.URL)
	parent.down.Store(true)

	u := newTestUser(t)
	c.fund(u)
	c.register(u)
	c.relay.ProcessBatch()

	assert.True(t, c.onChain(t, u.addr).IsProvisional(), "usable during the outage")
	assert.Nil(t, c.admit(userTx(t, u, 1), u))
	for i := 0; i < 50; i++ {
		c.relay.ProcessBatch()
	}
	assert.Equal(t, rollup.RegStatusProvisional, c.relay.Status(u.addr).Status, "never FAILED during an outage")
	_, found := parent.owner(t, u.addr)
	assert.False(t, found)

	parent.down.Store(false)
	waitFor(t, "relayed after recovery", func() bool {
		c.relay.ProcessBatch()
		_, found := parent.owner(t, u.addr)
		return found
	})
	c.pollWorkers(2)
	c.relay.ProcessBatch()
	assert.Equal(t, rollup.RegStatusConfirmed, c.relay.Status(u.addr).Status)
}

// E2E-3: the node crashes after acknowledging registrations while the parent is down. A new relay over the same durable
// store relays every one of them once the parent is back.
func TestRegistrationE2E_CrashRestartDurability(t *testing.T) {
	parent := newE2EParent(t)
	c := newE2ECluster(t, parent.srv.URL)
	kv := rollup.NewMemoryKVStore()
	c.relay = c.newRelay(rollup.NewKVRelayStore(kv))
	parent.down.Store(true)

	var users []testUser
	for i := 0; i < 3; i++ {
		u := newTestUser(t)
		c.register(u)
		users = append(users, u)
	}
	c.relay.ProcessBatch()

	// Crash: the first relay is gone; a fresh one starts from the same store.
	parent.down.Store(false)
	c.relay = c.newRelay(rollup.NewKVRelayStore(kv))
	waitFor(t, "all persisted registrations relayed after restart", func() bool {
		c.relay.ProcessBatch()
		for _, u := range users {
			if _, found := parent.owner(t, u.addr); !found {
				return false
			}
		}
		return true
	})
	c.pollWorkers(2)
	c.relay.ProcessBatch()
	for _, u := range users {
		assert.Equal(t, rollup.RegStatusConfirmed, c.relay.Status(u.addr).Status)
	}
	left, err := rollup.NewKVRelayStore(kv).ScanAll()
	require.NoError(t, err)
	assert.Empty(t, left, "confirmed requests are removed from the store")
}

// E2E-4: two clusters, one address. The cluster whose registration the parent executes first wins; the other one
// locks the address and reports REJECTED with the winner.
func TestRegistrationE2E_TwoClustersRace(t *testing.T) {
	parent := newE2EParent(t)
	a := newE2ECluster(t, parent.srv.URL)
	b := newE2ECluster(t, parent.srv.URL)
	accessorB := newLiveAccountStateAccessor(b.cs).(*liveAccountStateAccessor)
	b.relay.SetLockProposer(func(user common.Address, home cm.PublicKey) error {
		chainID := b.h.registryHandler.(*rollup.AccountRegistryHandler).AttestChainID()
		digest := rollup.ComputeAccountLockedAttestDigest(chainID, user, home, 0)
		for _, v := range b.validators[:2] { // f+1 validators of B co-attest the lock
			p := rollup.AccountRegistrationPayload{Kind: rollup.SystemPayloadKindAccountLocked, User: user, HomeCluster: home,
				Attestations: []rollup.RegistrationAttestation{{ValidatorPubkey: v.PublicKey(), Signature: bls.Sign(v.PrivateKey(), digest)}}}
			bz, err := p.MarshalProto()
			if err != nil {
				return err
			}
			if err := b.execSystemTx(bz); err != nil {
				return err
			}
		}
		return nil
	})
	b.relay.SetLockedChecker(accessorB.GetLocked)

	u := newTestUser(t)
	sigA, err := crypto.Sign(crypto.Keccak256(a.relay.RegistrationDigest(u.addr)), u.key)
	require.NoError(t, err)
	sigB, err := crypto.Sign(crypto.Keccak256(b.relay.RegistrationDigest(u.addr)), u.key)
	require.NoError(t, err)
	_, err = a.relay.Submit(u.addr, sigA)
	require.NoError(t, err)
	_, err = b.relay.Submit(u.addr, sigB)
	require.NoError(t, err)

	a.relay.ProcessBatch()
	waitFor(t, "A's registration executed", func() bool { _, f := parent.owner(t, u.addr); return f })

	waitFor(t, "B learns the address belongs to A and locks it", func() bool {
		b.relay.ProcessBatch()
		return b.relay.Status(u.addr).Status == rollup.RegStatusRejected
	})
	res := b.relay.Status(u.addr)
	require.NotNil(t, res.HomeCluster)
	assert.Equal(t, a.kp.PublicKey(), *res.HomeCluster)
	assert.True(t, b.onChain(t, u.addr).IsLocked())
	assert.False(t, b.onChain(t, u.addr).IsProvisional(), "locking clears provisional activation once parent gives the address to A")

	a.pollWorkers(2)
	a.relay.ProcessBatch()
	assert.Equal(t, rollup.RegStatusConfirmed, a.relay.Status(u.addr).Status)
}

// E2E-5: Multi-validator lock co-attestation on a 4-key committee (f+1 = 2).
// Cluster B's RPC node (validator 0) proposes PROVISIONAL at Submit while the address already belongs to A on Parent.
// OnProvisionalApplied notifies validator 1 (watchOnly), and each validator signs ONLY its own 1 account_locked
// attestation with HasPendingLockAttestation deduplication until the 2-of-4 threshold locks the account.
func TestRegistrationE2E_TwoClustersRace_MultiValidatorLockCoAttestation(t *testing.T) {
	parent := newE2EParent(t)
	a := newE2ECluster(t, parent.srv.URL)
	b := newE2ECluster(t, parent.srv.URL)

	regHandlerB := b.h.registryHandler.(*rollup.AccountRegistryHandler)
	chainID := regHandlerB.AttestChainID()
	accessorB := newLiveAccountStateAccessor(b.cs).(*liveAccountStateAccessor)
	scdbB := &liveSmartContractDB{db: b.cs.GetSmartContractDB()}

	var lockTxCounts [2]atomic.Int32
	wireValidatorRelay := func(idx int, isPrimary bool) *rollup.RegistrationRelay {
		v := b.validators[idx]
		var r *rollup.RegistrationRelay
		if isPrimary {
			r = b.relay
		} else {
			r = rollup.NewRegistrationRelay(b.client, b.kp.PublicKey(),
				func(digest []byte) cm.Sign { return bls.Sign(b.kp.PrivateKey(), digest) },
				accessorB.GetParentRegistered)
			r.SetProvisionalChecker(accessorB.GetProvisional)
		}
		r.SetLockedChecker(accessorB.GetLocked)
		r.SetLockedHomeClusterChecker(func(addr common.Address) cm.PublicKey {
			as, err := b.cs.GetAccountStateDB().AccountState(addr)
			if err != nil || as == nil {
				return cm.PublicKey{}
			}
			return as.LockedHomeCluster()
		})
		r.SetLockAlreadyAttested(func(user common.Address, home cm.PublicKey) bool {
			return rollup.HasPendingLockAttestation(scdbB, chainID, user, home, v.PublicKey())
		})
		r.SetLockProposer(func(user common.Address, home cm.PublicKey) error {
			lockTxCounts[idx].Add(1)
			digest := rollup.ComputeAccountLockedAttestDigest(chainID, user, home, 0)
			p := rollup.AccountRegistrationPayload{
				Kind:        rollup.SystemPayloadKindAccountLocked,
				User:        user,
				HomeCluster: home,
				Attestations: []rollup.RegistrationAttestation{{
					ValidatorPubkey: v.PublicKey(),
					Signature:       bls.Sign(v.PrivateKey(), digest),
				}},
			}
			bz, err := p.MarshalProto()
			if err != nil {
				return err
			}
			return b.execSystemTx(bz)
		})
		return r
	}

	r0 := wireValidatorRelay(0, true)
	r1 := wireValidatorRelay(1, false)
	// Execution reports the account_provisional tx hash; in this harness a tx executes directly on the committed
	// state, so a reported tx counts as committed (production: tx->block mapping written at block commit).
	var provTxs sync.Map
	r1.SetTxCommittedChecker(func(h common.Hash) bool { _, ok := provTxs.Load(h); return ok })
	regHandlerB.OnProvisionalApplied = func(user common.Address, txHash common.Hash) {
		provTxs.Store(txHash, user)
		r1.EnqueueWatch(user, txHash)
	}
	defer func() { regHandlerB.OnProvisionalApplied = nil }()

	// Cluster A wins first on Parent.
	u := newTestUser(t)
	sigA, err := crypto.Sign(crypto.Keccak256(a.relay.RegistrationDigest(u.addr)), u.key)
	require.NoError(t, err)
	_, err = a.relay.Submit(u.addr, sigA)
	require.NoError(t, err)
	a.relay.ProcessBatch()
	waitFor(t, "A's registration executed on Parent", func() bool { _, f := parent.owner(t, u.addr); return f })

	// User submits to Cluster B's validator 0 -> immediately becomes PROVISIONAL, notifying validator 1.
	sigB, err := crypto.Sign(crypto.Keccak256(r0.RegistrationDigest(u.addr)), u.key)
	require.NoError(t, err)
	_, err = r0.Submit(u.addr, sigB)
	require.NoError(t, err)
	require.True(t, b.onChain(t, u.addr).IsProvisional())
	reported := 0
	provTxs.Range(func(k, v any) bool {
		reported++
		assert.NotEqual(t, common.Hash{}, k.(common.Hash), "RollupSystemHandler must report the real tx hash")
		assert.Equal(t, u.addr, v.(common.Address))
		return true
	})
	assert.Equal(t, 1, reported, "exactly one provisional activation reported")

	// Validator 0 alone submits 1st lock attestation (insufficient for 2-of-4).
	r0.ProcessBatch()
	assert.False(t, b.onChain(t, u.addr).IsLocked(), "1 lock attestation must not lock on 4-key committee")
	assert.True(t, rollup.HasPendingLockAttestation(scdbB, chainID, u.addr, a.kp.PublicKey(), b.validators[0].PublicKey()))

	// Extra ticks on validator 0 must NOT re-submit because HasPendingLockAttestation is true.
	r0.ProcessBatch()
	assert.Equal(t, int32(1), lockTxCounts[0].Load(), "validator 0 must submit lock only once")

	// Validator 1 (watchOnly) runs ProcessBatch -> adds 2nd lock attestation -> account locks!
	r1.ProcessBatch()
	assert.Equal(t, int32(1), lockTxCounts[1].Load(), "validator 1 must submit lock only once")
	assert.True(t, b.onChain(t, u.addr).IsLocked(), "2nd attestation locks account on 4-key committee")
	assert.False(t, b.onChain(t, u.addr).IsProvisional(), "locking clears provisional flag")

	r0.ProcessBatch()
	r1.ProcessBatch()
	res := r0.Status(u.addr)
	assert.Equal(t, rollup.RegStatusRejected, res.Status)
	require.NotNil(t, res.HomeCluster)
	assert.Equal(t, a.kp.PublicKey(), *res.HomeCluster)
	assert.Equal(t, rollup.RegStatusRejected, r1.Status(u.addr).Status)
	assert.Equal(t, int32(1), lockTxCounts[0].Load())
	assert.Equal(t, int32(1), lockTxCounts[1].Load())
}
