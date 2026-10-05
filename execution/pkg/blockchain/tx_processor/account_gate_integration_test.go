package tx_processor

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// localParent is an in-process Parent Chain: it executes the REAL parentchain.RegisterAccount (verifying the cluster's
// BLS signature and the user's ECDSA signature, first registration wins) against a MemoryStore, and serves the ordered
// per-cluster registration events like the real node does. Other Client methods are never used here.
type localParent struct {
	parentchain.Client
	store *parentchain.MemoryStore
}

func (p *localParent) SendRegisterAccount(user common.Address, key cm.PublicKey, userSig []byte, clusterSig cm.Sign) (common.Hash, error) {
	// Parent admission accepts the tx; execution (and any duplicate error) happens inside the parent. A duplicate is
	// therefore NOT an error here: it only shows up in the registry, exactly like the real parent.
	_ = parentchain.RegisterAccount(p.store, user, key, userSig, clusterSig)
	return common.Hash{1}, nil
}

func (p *localParent) GetAccountRegistry(user common.Address) (cm.PublicKey, bool, error) {
	return p.store.GetAccountRegistry(user)
}

func (p *localParent) GetInboundAccountRegistrations(key cm.PublicKey, cursor uint64) ([]*parentchain.AccountRegisteredEvent, uint64, error) {
	return p.store.GetAccountRegistrations(crypto.Keccak256Hash(key.Bytes()), cursor)
}

// gateCluster is one execution cluster with the account gate on: chain state, BLS node identity (== cluster key),
// the real system-tx handler, registration relay and worker.
type gateCluster struct {
	t      *testing.T
	cs     *blockchain.ChainState
	kp     *bls.KeyPair
	relay  *rollup.RegistrationRelay
	worker *rollup.RegistrationWorker
	h      *RollupSystemHandler
	nonce  uint64
}

func newGateCluster(t *testing.T, parent parentchain.Client) *gateCluster {
	t.Helper()
	cs := setupTestChainState(t) // chain ID 1
	cs.GetConfig().TxSignatureMode = config.TxSignatureModeSecp
	cs.GetConfig().AccountGate = config.AccountGateParentRegistered

	kp := bls.GenerateKeyPair()
	node := state.NewAccountState(kp.Address()) // the node identity: BLS-native, funded for gas
	node.AddBalance(big.NewInt(1_000_000_000_000_000_000))
	node.SetPublicKeyBls(kp.PublicKey().Bytes())
	cs.GetAccountStateDB().SetState(node)

	g := &gateCluster{t: t, cs: cs, kp: kp}
	g.h = &RollupSystemHandler{
		dispatcher:      systemDispatcher(func(rollup.Store, AccountStateAccessor, []byte) error { return nil }),
		registryHandler: rollup.NewAccountRegistryHandler(kp.PublicKey()),
	}
	accessor := newLiveAccountStateAccessor(cs).(*liveAccountStateAccessor)
	g.relay = rollup.NewRegistrationRelay(parent, kp.PublicKey(),
		func(digest []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), digest) },
		accessor.GetParentRegistered)
	g.worker = rollup.NewRegistrationWorker(accessor, parent, kp, kp.PublicKey())
	// "Proposing" a system tx here means the node identity submits it and it is executed in a block.
	g.worker.EventProposer = func(payload []byte) error {
		tx := transaction.NewTransaction(kp.Address(), rollup.RollupSystemAddress, big.NewInt(0), 21000, 1_000_000_000, 0, payload, nil,
			common.Hash{}, common.Hash{}, g.nonce, 1)
		g.nonce++
		rcp, _, err := g.h.HandleTransaction(context.Background(), cs, tx, rollup.RollupSystemAddress, false, 0)
		require.NoError(t, err)
		if rcp.Status() != pb.RECEIPT_STATUS_RETURNED {
			return assert.AnError
		}
		return nil
	}
	return g
}

type testUser struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

func newTestUser(t *testing.T) testUser {
	t.Helper()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	return testUser{key: key, addr: crypto.PubkeyToAddress(key.PublicKey)}
}

// fund gives the user a balance on this cluster (a brand-new secp account: nonce 0, no BLS key).
func (g *gateCluster) fund(u testUser) {
	g.t.Helper()
	require.NoError(g.t, g.cs.GetAccountStateDB().AddBalance(u.addr, big.NewInt(1_000_000_000_000_000)))
}

// register performs what a user does: sign the message, call mtn_registerAccount — nothing else.
func (g *gateCluster) register(u testUser) rollup.RegistrationResult {
	g.t.Helper()
	sig, err := crypto.Sign(crypto.Keccak256(g.relay.RegistrationDigest(u.addr)), u.key)
	require.NoError(g.t, err)
	res, err := g.relay.Submit(u.addr, sig)
	require.NoError(g.t, err)
	return res
}

// settle runs the automatic machinery (relay → parent → ordered event → worker → on-chain flag) for a few ticks.
func (g *gateCluster) settle() {
	for i := 0; i < 6; i++ {
		g.relay.ProcessBatch()
		g.worker.PollAndProcess()
	}
}

// userTx builds a secp-signed (type 0xFF) transfer for this chain, i.e. what the user's wallet/SDK submits.
func userTx(t *testing.T, u testUser, chainID uint64) *transaction.Transaction {
	t.Helper()
	tx := &transaction.Transaction{}
	tx.FromProto(&pb.Transaction{FromAddress: u.addr.Bytes(), ToAddress: common.HexToAddress("0x456").Bytes(), Amount: []byte{1},
		Nonce: []byte{0, 0, 0, 0, 0, 0, 0, 0}, MaxGas: p_common.TRANSFER_GAS_COST, MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID: chainID, Type: 0xFF})
	require.NoError(t, tx.SignSecpProto(u.key))
	return tx
}

// admit runs mempool admission and the consensus-level filter on tx against the user's LIVE state on this cluster and
// requires them to agree.
func (g *gateCluster) admit(tx *transaction.Transaction, u testUser) *transaction.TransactionError {
	g.t.Helper()
	as, err := g.cs.GetAccountStateDB().AccountState(u.addr)
	require.NoError(g.t, err)
	rotateVerifiedSignatures()
	rotateVerifiedSignatures()
	verr := VerifyTransaction(tx, g.cs, as)
	valid, _ := verifySignatures(g.cs.GetAccountStateDB(), []types.Transaction{tx}, nil, sigPolicyOf(g.cs))
	assert.Equal(g.t, verr == nil, valid[0], "admission and the execution filter must agree (admission=%v, filter=%v)", verr, valid[0])
	return verr
}

func registryEvents(t *testing.T, store *parentchain.MemoryStore, key cm.PublicKey) []*parentchain.AccountRegisteredEvent {
	t.Helper()
	evs, _, err := store.GetAccountRegistrations(crypto.Keccak256Hash(key.Bytes()), 0)
	require.NoError(t, err)
	return evs
}

// The whole automatic path with every real component except the network: the user only signs and calls
// mtn_registerAccount on the execution node.
func TestAccountGate_UserRegistersWithTheNodeOnly_BecomesRealAccountAutomatically(t *testing.T) {
	parent := &localParent{store: parentchain.NewMemoryStore()}
	g := newGateCluster(t, parent)
	u := newTestUser(t)
	g.fund(u)

	require.NotNil(t, g.admit(userTx(t, u, 1), u), "gate on: an unregistered account cannot send")
	assert.Equal(t, transaction.AccountNotRegistered.Code, g.admit(userTx(t, u, 1), u).Code)
	assert.Equal(t, rollup.RegStatusNone, g.relay.Status(u.addr).Status)

	assert.Equal(t, rollup.RegStatusPending, g.register(u).Status)
	g.settle()

	// the parent registered the address to THIS cluster (it verified both signatures) and emitted exactly one event
	home, found, err := parent.store.GetAccountRegistry(u.addr)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, g.kp.PublicKey(), home)
	evs := registryEvents(t, parent.store, g.kp.PublicKey())
	require.Len(t, evs, 1)
	assert.Equal(t, u.addr, evs[0].UserAddress)

	// the ordered event set the on-chain flag; the relay reports CONFIRMED; the user can now send
	assert.Equal(t, rollup.RegStatusConfirmed, g.relay.Status(u.addr).Status)
	as, err := g.cs.GetAccountStateDB().AccountState(u.addr)
	require.NoError(t, err)
	assert.True(t, as.ParentRegistered())
	assert.Nil(t, g.admit(userTx(t, u, 1), u), "after registration the same kind of tx is accepted")

	// nothing is applied twice
	nonceBefore := g.nonce
	g.settle()
	g.settle()
	assert.Equal(t, nonceBefore, g.nonce, "no further system txs once the event is applied")
	assert.Len(t, registryEvents(t, parent.store, g.kp.PublicKey()), 1)
}

// The same address registers with two clusters at once: the parent picks the first, the other is told REJECTED and the
// account stays unusable there — whatever order the clusters process in.
func TestAccountGate_SameAddressOnTwoClusters_ParentPicksTheFirst(t *testing.T) {
	for _, aFirst := range []bool{true, false} {
		name := "A first"
		if !aFirst {
			name = "B first"
		}
		t.Run(name, func(t *testing.T) {
			parent := &localParent{store: parentchain.NewMemoryStore()}
			a := newGateCluster(t, parent)
			b := newGateCluster(t, parent)
			u := newTestUser(t)
			a.fund(u)
			b.fund(u) // same address funded on both: the replay precondition

			require.Equal(t, rollup.RegStatusPending, a.register(u).Status)
			require.Equal(t, rollup.RegStatusPending, b.register(u).Status)

			first, second := a, b
			if !aFirst {
				first, second = b, a
			}
			first.settle() // its registration reaches the parent first
			second.settle()

			assert.Equal(t, rollup.RegStatusConfirmed, first.relay.Status(u.addr).Status)
			loser := second.relay.Status(u.addr)
			assert.Equal(t, rollup.RegStatusRejected, loser.Status)
			require.NotNil(t, loser.HomeCluster)
			assert.Equal(t, first.kp.PublicKey(), *loser.HomeCluster, "the loser learns who won")

			home, found, _ := parent.store.GetAccountRegistry(u.addr)
			require.True(t, found)
			assert.Equal(t, first.kp.PublicKey(), home)
			assert.Len(t, registryEvents(t, parent.store, second.kp.PublicKey()), 0, "the loser cluster gets no registration event")

			// the cross-cluster replay: a tx signed once is valid on the home cluster and refused on the other one,
			// even though BOTH have chain ID 1 and the address has a balance and the same nonce on both
			tx := userTx(t, u, 1)
			assert.Nil(t, first.admit(tx, u))
			rej := second.admit(tx, u)
			require.NotNil(t, rej)
			assert.Equal(t, transaction.AccountNotRegistered.Code, rej.Code)
			secondState, _ := second.cs.GetAccountStateDB().AccountState(u.addr)
			assert.False(t, secondState.ParentRegistered())
		})
	}
}

// A forged registration event from an ordinary account cannot substitute for the real flow.
func TestAccountGate_ForgedRegistrationEventDoesNotOpenTheGate(t *testing.T) {
	parent := &localParent{store: parentchain.NewMemoryStore()}
	g := newGateCluster(t, parent)
	attacker := newTestUser(t)
	g.fund(attacker)
	require.NoError(t, g.cs.GetAccountStateDB().AddBalance(attacker.addr, big.NewInt(1_000_000_000_000_000)))

	raw, _ := json.Marshal(rollup.AccountRegistrationPayload{Kind: rollup.SystemPayloadKindAccountRegistered, User: attacker.addr, ClusterKey: g.kp.PublicKey()})
	tx := transaction.NewTransaction(attacker.addr, rollup.RollupSystemAddress, big.NewInt(0), 21000, 1_000_000_000, 0, raw, nil,
		common.Hash{}, common.Hash{}, 0, 1)
	rcp, _, err := g.h.HandleTransaction(context.Background(), g.cs, tx, rollup.RollupSystemAddress, false, 0)
	require.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcp.Status())

	require.NotNil(t, g.admit(userTx(t, attacker, 1), attacker))
}
