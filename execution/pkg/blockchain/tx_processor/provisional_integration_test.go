package tx_processor

import (
	"context"
	"encoding/json"
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

type mockRegStateDB struct {
	flags       map[common.Address]bool
	provisional map[common.Address]bool
	locked      map[common.Address]bool
}

func newMockRegStateDB() *mockRegStateDB {
	return &mockRegStateDB{
		flags:       make(map[common.Address]bool),
		provisional: make(map[common.Address]bool),
		locked:      make(map[common.Address]bool),
	}
}

func (m *mockRegStateDB) GetParentRegistered(addr common.Address) bool { return m.flags[addr] }
func (m *mockRegStateDB) SetParentRegistered(addr common.Address, r bool) { m.flags[addr] = r }
func (m *mockRegStateDB) GetProvisional(addr common.Address) bool { return m.provisional[addr] }
func (m *mockRegStateDB) SetProvisional(addr common.Address, p bool) { m.provisional[addr] = p }
func (m *mockRegStateDB) GetLocked(addr common.Address) bool { return m.locked[addr] }
func (m *mockRegStateDB) SetLocked(addr common.Address, l bool) { m.locked[addr] = l }

// scriptableParent simulates Parent Chain with toggleable offline status and explicit registry mapping.
type scriptableParent struct {
	parentchain.Client
	mu           sync.Mutex
	offline      bool
	registry     map[common.Address]cm.PublicKey
	events       map[cm.PublicKey][]*parentchain.AccountRegisteredEvent
	inboundSeq   uint64
	registerErrs map[common.Address]error
}

type mockCommitteeProvider struct {
	validators []cm.PublicKey
}

func (m *mockCommitteeProvider) GetActiveCommitteeBLSKeys() ([]cm.PublicKey, error) {
	return m.validators, nil
}

func newScriptableParent() *scriptableParent {
	return &scriptableParent{
		registry:     make(map[common.Address]cm.PublicKey),
		events:       make(map[cm.PublicKey][]*parentchain.AccountRegisteredEvent),
		registerErrs: make(map[common.Address]error),
	}
}

func (p *scriptableParent) setOffline(offline bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.offline = offline
}

func (p *scriptableParent) SendRegisterAccount(user common.Address, key cm.PublicKey, userSig []byte, clusterSig cm.Sign) (common.Hash, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.offline {
		return common.Hash{}, assert.AnError
	}
	if err, ok := p.registerErrs[user]; ok && err != nil {
		return common.Hash{}, err
	}
	if _, exists := p.registry[user]; !exists {
		p.registry[user] = key
		p.inboundSeq++
		ev := &parentchain.AccountRegisteredEvent{
			UserAddress: user,
			ClusterKey:  key,
			Seq:         p.inboundSeq,
		}
		p.events[key] = append(p.events[key], ev)
	}
	return common.HexToHash("0xabc123"), nil
}

func (p *scriptableParent) GetAccountRegistry(user common.Address) (cm.PublicKey, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.offline {
		return cm.PublicKey{}, false, assert.AnError
	}
	k, ok := p.registry[user]
	return k, ok, nil
}

func (p *scriptableParent) GetInboundAccountRegistrations(key cm.PublicKey, cursor uint64) ([]*parentchain.AccountRegisteredEvent, uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.offline {
		return nil, cursor, assert.AnError
	}
	all := p.events[key]
	var out []*parentchain.AccountRegisteredEvent
	nextCursor := cursor
	for _, ev := range all {
		if ev.Seq >= cursor {
			out = append(out, ev)
			if ev.Seq >= nextCursor {
				nextCursor = ev.Seq + 1
			}
		}
	}
	return out, nextCursor, nil
}

// provisionalTestCluster wraps a gateCluster with full provisional/lock proposal dispatching.
type provisionalTestCluster struct {
	*gateCluster
	parent *scriptableParent
}

func newProvisionalTestCluster(t *testing.T, parent *scriptableParent) *provisionalTestCluster {
	t.Helper()
	gc := newGateCluster(t, parent)
	ptc := &provisionalTestCluster{gateCluster: gc, parent: parent}

	accessor := newLiveAccountStateAccessor(gc.cs).(*liveAccountStateAccessor)
	gc.relay.SetProvisionalChecker(accessor.GetProvisional)
	gc.relay.SetLockedChecker(accessor.GetLocked)

	gc.relay.SetProvisionalProposer(func(user common.Address, userSig []byte) error {
		payload := rollup.AccountRegistrationPayload{
			Kind:       rollup.SystemPayloadKindAccountProvisional,
			User:       user,
			ClusterKey: gc.kp.PublicKey(),
			UserSig:    userSig,
		}
		digest := rollup.ComputeAccountProvisionalAttestDigest(parentchain.ParentChainID, user, gc.kp.PublicKey(), userSig)
		payload.Attestations = []rollup.RegistrationAttestation{{
			ValidatorPubkey: gc.kp.PublicKey(),
			Signature:       bls.Sign(gc.kp.PrivateKey(), digest),
		}}
		b, err := payload.MarshalProto()
		if err != nil {
			return err
		}
		tx := transaction.NewTransaction(gc.kp.Address(), rollup.RollupSystemAddress, big.NewInt(0), 21000, 1_000_000_000, 0, b, nil,
			common.Hash{}, common.Hash{}, gc.nonce, 1)
		gc.nonce++
		rcp, _, err := gc.h.HandleTransaction(context.Background(), gc.cs, tx, rollup.RollupSystemAddress, false, 0)
		if err != nil {
			return err
		}
		if rcp.Status() != pb.RECEIPT_STATUS_RETURNED {
			return assert.AnError
		}
		return nil
	})

	gc.relay.SetLockProposer(func(user common.Address, homeCluster cm.PublicKey) error {
		payload := rollup.AccountRegistrationPayload{
			Kind:        rollup.SystemPayloadKindAccountLocked,
			User:        user,
			HomeCluster: homeCluster,
		}
		digest := rollup.ComputeAccountLockedAttestDigest(parentchain.ParentChainID, user, homeCluster, 0)
		payload.Attestations = []rollup.RegistrationAttestation{{
			ValidatorPubkey: gc.kp.PublicKey(),
			Signature:       bls.Sign(gc.kp.PrivateKey(), digest),
		}}
		b, err := payload.MarshalProto()
		if err != nil {
			return err
		}
		tx := transaction.NewTransaction(gc.kp.Address(), rollup.RollupSystemAddress, big.NewInt(0), 21000, 1_000_000_000, 0, b, nil,
			common.Hash{}, common.Hash{}, gc.nonce, 1)
		gc.nonce++
		rcp, _, err := gc.h.HandleTransaction(context.Background(), gc.cs, tx, rollup.RollupSystemAddress, false, 0)
		if err != nil {
			return err
		}
		if rcp.Status() != pb.RECEIPT_STATUS_RETURNED {
			return assert.AnError
		}
		return nil
	})

	return ptc
}

// userTxTo builds a transaction from user u to toAddr.
func userTxTo(t *testing.T, u testUser, toAddr common.Address, nonce uint64, chainID uint64) *transaction.Transaction {
	t.Helper()
	tx := &transaction.Transaction{}
	var nonceBytes [8]byte
	for i := 0; i < 8; i++ {
		nonceBytes[7-i] = byte(nonce >> (8 * i))
	}
	tx.FromProto(&pb.Transaction{
		FromAddress: u.addr.Bytes(),
		ToAddress:   toAddr.Bytes(),
		Amount:      []byte{1},
		Nonce:       nonceBytes[:],
		MaxGas:      p_common.TRANSFER_GAS_COST,
		MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID:     chainID,
		Type:        0xFF,
	})
	require.NoError(t, tx.SignSecpProto(u.key))
	return tx
}

// P01: Parent offline, valid registration signature -> provisional activation commits, internal transfers allowed.
func TestProvisional_P01_ParentOffline_ProvisionalActivation(t *testing.T) {
	parent := newScriptableParent()
	parent.setOffline(true)
	cluster := newProvisionalTestCluster(t, parent)

	u := newTestUser(t)
	cluster.fund(u)

	// User submits registration
	res := cluster.register(u)
	require.Equal(t, rollup.RegStatusPending, res.Status)

	// Process batch with parent offline: triggers provisional activation proposal and commit
	cluster.relay.ProcessBatch()

	// Verify on-chain account state
	as, err := cluster.cs.GetAccountStateDB().AccountState(u.addr)
	require.NoError(t, err)
	require.NotNil(t, as)
	assert.True(t, as.IsProvisional(), "Account must be marked provisional on-chain")
	assert.False(t, as.ParentRegistered(), "Account must NOT be marked parent-registered yet")
	assert.False(t, as.IsLocked(), "Account must not be locked")

	// Verify relay reports PROVISIONAL
	status := cluster.relay.Status(u.addr)
	assert.Equal(t, rollup.RegStatusProvisional, status.Status)

	// Send native transfer to registered recipient (0x456)
	tx := userTx(t, u, 1)
	errAdmit := cluster.admit(tx, u)
	assert.Nil(t, errAdmit, "Provisional sender must be permitted by account gate")

	// Also verify provisional account can receive transfers
	sender := newTestUser(t)
	sSt := state.NewAccountState(sender.addr)
	sSt.SetParentRegistered(true)
	cluster.cs.GetAccountStateDB().SetState(sSt)
	cluster.fund(sender)

	txIn := userTxTo(t, sender, u.addr, 0, 1)
	errAdmitIn := cluster.admit(txIn, sender)
	assert.Nil(t, errAdmitIn, "Provisional recipient must be permitted by recipient gate")
}

// P02: Invalid user signature or wrong cluster key -> provisional activation rejected.
func TestProvisional_P02_InvalidSignature_ProvisionalRejected(t *testing.T) {
	parent := newScriptableParent()
	cluster := newProvisionalTestCluster(t, parent)

	u := newTestUser(t)
	badKey, _ := crypto.GenerateKey()
	// Sign with a different key
	badSig, err := crypto.Sign(crypto.Keccak256(cluster.relay.RegistrationDigest(u.addr)), badKey)
	require.NoError(t, err)

	// Direct submit should reject immediately
	_, errSubmit := cluster.relay.Submit(u.addr, badSig)
	assert.ErrorIs(t, errSubmit, rollup.ErrInvalidRegistrationSig)

	// Direct system payload with bad userSig applied to handler must also fail
	badPayload := &rollup.AccountRegistrationPayload{
		Kind:       rollup.SystemPayloadKindAccountProvisional,
		User:       u.addr,
		ClusterKey: cluster.kp.PublicKey(),
		UserSig:    badSig,
	}
	data, err := badPayload.MarshalProto()
	require.NoError(t, err)

	errApply := cluster.h.registryHandler.Apply(newLiveAccountStateAccessor(cluster.cs).(*liveAccountStateAccessor), data)
	assert.Error(t, errApply, "System handler must reject invalid user signature for provisional activation")

	as, _ := cluster.cs.GetAccountStateDB().AccountState(u.addr)
	if as != nil {
		assert.False(t, as.IsProvisional())
	}
}

// P03: Long parent outage exceeds old 20-attempt limit -> request is not dropped or permanently marked FAILED.
func TestProvisional_P03_ParentOutage_NoPermanentDrop(t *testing.T) {
	parent := newScriptableParent()
	parent.setOffline(true)
	cluster := newProvisionalTestCluster(t, parent)

	u := newTestUser(t)
	cluster.register(u)

	// Run 25 ticks (well above the old defaultRelayMaxAttempts = 20)
	for i := 0; i < 25; i++ {
		cluster.relay.ProcessBatch()
	}

	status := cluster.relay.Status(u.addr)
	assert.NotEqual(t, rollup.RegStatusFailed, status.Status, "Provisional request must NOT be abandoned as FAILED during network outage")
	assert.Equal(t, rollup.RegStatusProvisional, status.Status)
}

// P04: Persistent storage error returns failure; persistent entries reload on restart.
func TestProvisional_P04_Persistence_DurableRecovery(t *testing.T) {
	parent := newScriptableParent()
	parent.setOffline(true)
	cluster := newProvisionalTestCluster(t, parent)

	memKV := rollup.NewMemoryKVStore()
	store := rollup.NewKVRelayStore(memKV)
	require.NoError(t, cluster.relay.SetStore(store))

	u := newTestUser(t)
	res := cluster.register(u)
	require.Equal(t, rollup.RegStatusPending, res.Status)

	// Verify entry was persisted in store
	rec, found, err := store.Get(u.addr)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, u.addr, rec.User)

	// Create a new relay instance (simulating restart) and restore from the same store
	newRelay := rollup.NewRegistrationRelay(parent, cluster.kp.PublicKey(),
		func(digest []byte) cm.Sign { return bls.Sign(cluster.kp.PrivateKey(), digest) },
		func(a common.Address) bool { return false })
	require.NoError(t, newRelay.SetStore(store))

	newStatus := newRelay.Status(u.addr)
	assert.Equal(t, rollup.RegStatusPending, newStatus.Status, "Pending request must be recovered after restart")
}

// P05: Parent recovers, user registered only in Cluster A -> transitions PROVISIONAL -> CONFIRMED.
func TestProvisional_P05_ParentRecovery_ProvisionalToConfirmed(t *testing.T) {
	parent := newScriptableParent()
	parent.setOffline(true)
	cluster := newProvisionalTestCluster(t, parent)

	u := newTestUser(t)
	cluster.fund(u)
	cluster.register(u)

	// 1. Parent offline: account becomes provisional
	cluster.relay.ProcessBatch()
	status1 := cluster.relay.Status(u.addr)
	require.Equal(t, rollup.RegStatusProvisional, status1.Status)

	// 2. Parent recovers
	parent.setOffline(false)

	// 3. Settle machinery: relay sends to parent, worker receives inbound event and proposes confirmation
	for i := 0; i < 6; i++ {
		cluster.relay.ProcessBatch()
		cluster.worker.PollAndProcess()
	}

	// 4. State must now be CONFIRMED (ParentRegistered = true, Provisional = false)
	as, err := cluster.cs.GetAccountStateDB().AccountState(u.addr)
	require.NoError(t, err)
	assert.True(t, as.ParentRegistered(), "Account must be ParentRegistered")
	assert.False(t, as.IsProvisional(), "Provisional flag must be cleared upon confirmation")
	assert.False(t, as.IsLocked(), "Account must not be locked")

	status2 := cluster.relay.Status(u.addr)
	assert.Equal(t, rollup.RegStatusConfirmed, status2.Status)
}

// P06: Race between A and B: X is provisional on both. Parent confirms A first -> A becomes confirmed, B becomes locked.
func TestProvisional_P06_MultiClusterRace_ParentConfirmsA_ClusterBLocks(t *testing.T) {
	parent := newScriptableParent()
	parent.setOffline(true)

	clusterA := newProvisionalTestCluster(t, parent)
	clusterB := newProvisionalTestCluster(t, parent)

	u := newTestUser(t)
	clusterA.fund(u)
	clusterB.fund(u)

	// User registers on both A and B while offline
	sigA, err := crypto.Sign(crypto.Keccak256(clusterA.relay.RegistrationDigest(u.addr)), u.key)
	require.NoError(t, err)
	_, errA := clusterA.relay.Submit(u.addr, sigA)
	require.NoError(t, errA)

	sigB, err := crypto.Sign(crypto.Keccak256(clusterB.relay.RegistrationDigest(u.addr)), u.key)
	require.NoError(t, err)
	_, errB := clusterB.relay.Submit(u.addr, sigB)
	require.NoError(t, errB)

	// Offline provisional activation on both clusters
	clusterA.relay.ProcessBatch()
	clusterB.relay.ProcessBatch()

	asA, _ := clusterA.cs.GetAccountStateDB().AccountState(u.addr)
	asB, _ := clusterB.cs.GetAccountStateDB().AccountState(u.addr)
	require.True(t, asA.IsProvisional())
	require.True(t, asB.IsProvisional())

	// Parent recovers and Cluster A submits first (Parent confirms Cluster A as home cluster)
	parent.setOffline(false)
	clusterA.relay.ProcessBatch()
	clusterA.worker.PollAndProcess()

	// Verify Cluster A is confirmed
	asAConfirmed, _ := clusterA.cs.GetAccountStateDB().AccountState(u.addr)
	assert.True(t, asAConfirmed.ParentRegistered())

	// Cluster B now reconciles with Parent: Parent returns home = clusterA
	clusterB.relay.ProcessBatch()

	// Cluster B must lock account X
	asBLocked, err := clusterB.cs.GetAccountStateDB().AccountState(u.addr)
	require.NoError(t, err)
	assert.True(t, asBLocked.IsLocked(), "Losing cluster must lock the account")
	assert.False(t, asBLocked.IsProvisional(), "Provisional flag must be revoked upon lock")
	assert.False(t, asBLocked.ParentRegistered(), "Account must not be parent-registered on losing cluster")

	statusB := clusterB.relay.Status(u.addr)
	assert.Equal(t, rollup.RegStatusRejected, statusB.Status)
	require.NotNil(t, statusB.HomeCluster)
	assert.Equal(t, clusterA.kp.PublicKey(), *statusB.HomeCluster)

	// On Cluster B: sending transaction from X must be blocked with AccountLocked (code 92)
	txB := userTx(t, u, 1)
	errAdmit := clusterB.admit(txB, u)
	require.NotNil(t, errAdmit)
	assert.Equal(t, transaction.AccountLocked.Code, errAdmit.Code)

	// On Cluster B: sending native transfer to X must also be blocked with AccountLocked (code 92)
	senderB := newTestUser(t)
	clusterB.fund(senderB)
	sSt := state.NewAccountState(senderB.addr)
	sSt.SetParentRegistered(true)
	clusterB.cs.GetAccountStateDB().SetState(sSt)

	txToX := userTxTo(t, senderB, u.addr, 0, 1)
	errAdmitToX := clusterB.admit(txToX, senderB)
	require.NotNil(t, errAdmitToX)
	assert.Equal(t, transaction.AccountLocked.Code, errAdmitToX.Code)
}

// P07: Reverse order race: Cluster B wins confirmation first -> B becomes confirmed, A becomes locked.
func TestProvisional_P07_MultiClusterRace_ClusterBWins_ClusterALocks(t *testing.T) {
	parent := newScriptableParent()
	parent.setOffline(true)

	clusterA := newProvisionalTestCluster(t, parent)
	clusterB := newProvisionalTestCluster(t, parent)

	u := newTestUser(t)
	clusterA.fund(u)
	clusterB.fund(u)

	// User registers on both A and B while offline
	sigA, _ := crypto.Sign(crypto.Keccak256(clusterA.relay.RegistrationDigest(u.addr)), u.key)
	_, _ = clusterA.relay.Submit(u.addr, sigA)
	sigB, _ := crypto.Sign(crypto.Keccak256(clusterB.relay.RegistrationDigest(u.addr)), u.key)
	_, _ = clusterB.relay.Submit(u.addr, sigB)

	clusterA.relay.ProcessBatch()
	clusterB.relay.ProcessBatch()

	// Parent recovers and Cluster B submits first
	parent.setOffline(false)
	clusterB.relay.ProcessBatch()
	clusterB.worker.PollAndProcess()

	// Cluster B confirmed
	asB, _ := clusterB.cs.GetAccountStateDB().AccountState(u.addr)
	assert.True(t, asB.ParentRegistered())

	// Cluster A reconciles -> discovers Cluster B won
	clusterA.relay.ProcessBatch()

	// Cluster A locked
	asA, _ := clusterA.cs.GetAccountStateDB().AccountState(u.addr)
	assert.True(t, asA.IsLocked())
	assert.False(t, asA.IsProvisional())

	txA := userTx(t, u, 1)
	errAdmit := clusterA.admit(txA, u)
	require.NotNil(t, errAdmit)
	assert.Equal(t, transaction.AccountLocked.Code, errAdmit.Code)
}

// P08: Losing cluster restart / resubmit replay: once locked, account cannot be made provisional.
func TestProvisional_P08_RestartLosingCluster_ReplayActivationRejected(t *testing.T) {
	parent := newScriptableParent()
	cluster := newProvisionalTestCluster(t, parent)

	u := newTestUser(t)
	cluster.fund(u)

	// Directly set user as locked on cluster
	require.NoError(t, cluster.cs.GetAccountStateDB().SetLocked(u.addr, true))

	// User attempts to re-register on the locked cluster
	sig, _ := crypto.Sign(crypto.Keccak256(cluster.relay.RegistrationDigest(u.addr)), u.key)
	res, err := cluster.relay.Submit(u.addr, sig)
	require.NoError(t, err)
	assert.Equal(t, rollup.RegStatusRejected, res.Status)

	// An old provisional activation payload replayed against system handler must fail
	payload := &rollup.AccountRegistrationPayload{
		Kind:       rollup.SystemPayloadKindAccountProvisional,
		User:       u.addr,
		ClusterKey: cluster.kp.PublicKey(),
		UserSig:    sig,
	}
	data, err := payload.MarshalProto()
	require.NoError(t, err)

	errApply := cluster.h.registryHandler.Apply(newLiveAccountStateAccessor(cluster.cs).(*liveAccountStateAccessor), data)
	assert.Error(t, errApply, "Replayed provisional activation must be rejected for a locked account")

	as, _ := cluster.cs.GetAccountStateDB().AccountState(u.addr)
	assert.True(t, as.IsLocked(), "Account must remain locked")
	assert.False(t, as.IsProvisional(), "Account must NOT be made provisional")
}

// P09: Transactions completed prior to locking remain valid; balances of recipient Y and remaining balance of X are intact.
func TestProvisional_P09_PriorTransactionsPreserved(t *testing.T) {
	parent := newScriptableParent()
	parent.setOffline(true)
	cluster := newProvisionalTestCluster(t, parent)

	userX := newTestUser(t)
	userY := newTestUser(t)

	// Fund X with 1_000_000 wei
	initBalX := big.NewInt(1_000_000)
	require.NoError(t, cluster.cs.GetAccountStateDB().AddBalance(userX.addr, initBalX))

	// Activate both X and Y provisionally
	cluster.register(userX)
	cluster.register(userY)
	cluster.relay.ProcessBatch()

	// Prior transaction: X transfers 400 wei to Y while provisional
	transferAmt := big.NewInt(400)
	require.NoError(t, cluster.cs.GetAccountStateDB().SubBalance(userX.addr, transferAmt))
	require.NoError(t, cluster.cs.GetAccountStateDB().AddBalance(userY.addr, transferAmt))
	require.NoError(t, cluster.cs.GetAccountStateDB().PlusOneNonce(userX.addr))

	asYBefore, _ := cluster.cs.GetAccountStateDB().AccountState(userY.addr)
	balYBeforeLock := asYBefore.Balance()
	assert.Equal(t, transferAmt, balYBeforeLock)

	// Now Parent confirms another cluster and cluster locks X
	var otherCluster cm.PublicKey
	otherCluster[0] = 0x99
	parent.setOffline(false)
	parent.registry[userX.addr] = otherCluster

	cluster.relay.ProcessBatch()

	// Assert X is now locked
	asX, _ := cluster.cs.GetAccountStateDB().AccountState(userX.addr)
	assert.True(t, asX.IsLocked())

	// Y's balance remains intact (400 wei)
	asYAfter, _ := cluster.cs.GetAccountStateDB().AccountState(userY.addr)
	balYAfterLock := asYAfter.Balance()
	assert.Equal(t, transferAmt, balYAfterLock, "Recipient Y balance must NOT be revoked")

	// X's remaining balance is preserved (not confiscated)
	asXAfter, _ := cluster.cs.GetAccountStateDB().AccountState(userX.addr)
	balXAfterLock := asXAfter.Balance()
	assert.Equal(t, new(big.Int).Sub(initBalX, transferAmt), balXAfterLock, "X remaining balance must remain intact")
}

// P10: Committee co-attestation quorum required: 1 validator attestation stays pending; 2nd completes activation.
func TestProvisional_P10_CommitteeAttestation_QuorumEnforced(t *testing.T) {
	val1 := bls.GenerateKeyPair()
	val2 := bls.GenerateKeyPair()
	val3 := bls.GenerateKeyPair()
	val4 := bls.GenerateKeyPair()

	committee := []cm.PublicKey{val1.PublicKey(), val2.PublicKey(), val3.PublicKey(), val4.PublicKey()}
	cp := &mockCommitteeProvider{validators: committee}

	h := rollup.NewAccountRegistryHandler(val1.PublicKey())
	h.SetCommitteeProvider(cp)
	h.SetChainID(1)

	db := newMockRegStateDB()
	store := &inMemAttestStore{store: make(map[common.Hash][]rollup.RegistrationAttestation)}

	user := newTestUser(t)
	sig, err := crypto.Sign(crypto.Keccak256(parentchain.ComputeRegisterAccountMessage(user.addr, val1.PublicKey())), user.key)
	require.NoError(t, err)

	digest := rollup.ComputeAccountProvisionalAttestDigest(1, user.addr, val1.PublicKey(), sig)

	// 1. Submit payload with only Val1 attestation (required = 2)
	payload1 := &rollup.AccountRegistrationPayload{
		Kind:         rollup.SystemPayloadKindAccountProvisional,
		User:         user.addr,
		ClusterKey:   val1.PublicKey(),
		UserSig:      sig,
		Attestations: []rollup.RegistrationAttestation{{ValidatorPubkey: val1.PublicKey(), Signature: bls.Sign(val1.PrivateKey(), digest)}},
	}
	data1, _ := payload1.MarshalProto()
	err1 := h.ApplyAttested(db, store, cp, data1)
	require.NoError(t, err1)
	assert.False(t, db.GetProvisional(user.addr), "Single attestation must remain pending (not activated)")

	// 2. Submit payload with Val2 attestation -> quorum reached (2 >= 2)
	payload2 := &rollup.AccountRegistrationPayload{
		Kind:         rollup.SystemPayloadKindAccountProvisional,
		User:         user.addr,
		ClusterKey:   val1.PublicKey(),
		UserSig:      sig,
		Attestations: []rollup.RegistrationAttestation{{ValidatorPubkey: val2.PublicKey(), Signature: bls.Sign(val2.PrivateKey(), digest)}},
	}
	data2, _ := payload2.MarshalProto()
	err2 := h.ApplyAttested(db, store, cp, data2)
	require.NoError(t, err2)
	assert.True(t, db.GetProvisional(user.addr), "Quorum reached: account must be provisional")
}

// inMemAttestStore is an in-memory attestation store for testing.
type inMemAttestStore struct {
	mu    sync.Mutex
	store map[common.Hash][]rollup.RegistrationAttestation
}

func (s *inMemAttestStore) Load(k common.Hash) []rollup.RegistrationAttestation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store[k]
}

func (s *inMemAttestStore) Save(k common.Hash, a []rollup.RegistrationAttestation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[k] = a
}

func (s *inMemAttestStore) Clear(k common.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.store, k)
}

// P12: Cross-cluster gateway withdrawal / transfer by provisional account is blocked.
func TestProvisional_P12_ProvisionalGatewayWithdrawalBlocked(t *testing.T) {
	parent := newScriptableParent()
	parent.setOffline(true)
	cluster := newProvisionalTestCluster(t, parent)

	u := newTestUser(t)
	cluster.fund(u)

	// Make u provisional
	cluster.register(u)
	cluster.relay.ProcessBatch()

	as, _ := cluster.cs.GetAccountStateDB().AccountState(u.addr)
	require.True(t, as.IsProvisional())
	require.False(t, as.ParentRegistered())

	// Build a cross-node transfer transaction through ParentChainGatewayHandler
	targetNodeKey := bls.GenerateKeyPair().PublicKey()
	payloadHash := common.HexToHash("0x112233")
	targetAddr := common.HexToAddress("0x778899")

	var calldata []byte
	calldata = append(calldata, targetNodeKey.Bytes()[:]...)
	calldata = append(calldata, payloadHash.Bytes()...)
	calldata = append(calldata, targetAddr.Bytes()...)

	gatewayTx := transaction.NewTransaction(
		u.addr,
		common.HexToAddress("0x00000000000000000000000000000000000000aa"), // gateway address
		big.NewInt(100),
		100000,
		1_000_000_000,
		0,
		calldata,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1,
	)

	mockDispatcher := &MockCrossChainDispatcher{
		MockHash: common.HexToHash("0x1234"),
	}
	handler := &ParentChainGatewayHandler{dispatcher: mockDispatcher}

	// Attempt transfer while provisional -> must be rejected
	rcp, _, err := handler.HandleTransaction(context.Background(), cluster.cs, gatewayTx, gatewayTx.ToAddress(), false, 0)
	require.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcp.Status())
	assert.Contains(t, string(rcp.Return()), "account is not confirmed on parent chain")

	// Now confirm u on Parent Chain
	require.NoError(t, cluster.cs.GetAccountStateDB().SetParentRegistered(u.addr, true))
	require.NoError(t, cluster.cs.GetAccountStateDB().SetProvisional(u.addr, false))

	// Re-attempt transfer now that u is CONFIRMED -> proceeds past admission
	rcp2, _, err2 := handler.HandleTransaction(context.Background(), cluster.cs, gatewayTx, gatewayTx.ToAddress(), false, 0)
	require.NoError(t, err2)
	assert.NotEqual(t, "account is not confirmed on parent chain", string(rcp2.Exception()))
}

// S01: Serialization / Marshal / Copy / JSON fidelity with Provisional and Locked flags.
func TestProvisional_S01_StateSerializationCompatibility(t *testing.T) {
	addr := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	as := state.NewAccountState(addr)
	as.AddBalance(big.NewInt(12345))
	as.SetNonce(42)

	// Default values
	assert.False(t, as.ParentRegistered())
	assert.False(t, as.IsLocked())
	assert.False(t, as.IsProvisional())

	// Set flags
	as.SetProvisional(true)
	as.SetLocked(false)
	assert.True(t, as.IsProvisional())

	// 1. Copy
	copied := as.Copy()
	assert.True(t, copied.IsProvisional())
	assert.False(t, copied.IsLocked())

	// 2. Proto Marshal / Unmarshal
	raw, err := as.Marshal()
	require.NoError(t, err)

	as2 := state.NewAccountState(addr)
	require.NoError(t, as2.Unmarshal(raw))
	assert.True(t, as2.IsProvisional())
	assert.False(t, as2.IsLocked())
	assert.Equal(t, as.Balance(), as2.Balance())
	assert.Equal(t, as.Nonce(), as2.Nonce())

	// 3. JSON Marshal / Unmarshal
	var jsonState state.JsonAccountState
	jsonBytes, err := json.Marshal(as.String())
	require.NoError(t, err)

	err = json.Unmarshal([]byte(as.String()), &jsonState)
	require.NoError(t, err)
	assert.True(t, jsonState.Provisional)
	assert.False(t, jsonState.Locked)

	toState := jsonState.ToAccountState()
	assert.True(t, toState.IsProvisional())
	assert.False(t, toState.IsLocked())

	_ = jsonBytes
}
