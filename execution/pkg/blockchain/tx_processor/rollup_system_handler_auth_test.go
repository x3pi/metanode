package tx_processor

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

// systemPayload mirrors cmd/simple_chain's rollupSystemPayload (the wire format of a rollup system tx).
type systemPayload struct {
	Event        rollup.Event        `json:"event"`
	MsgID        common.Hash         `json:"msg_id"`
	SourceSeq    uint64              `json:"source_seq"`
	SourcePubKey mt_common.PublicKey `json:"source_pub_key"`
	DestPubKey   mt_common.PublicKey `json:"dest_pub_key"`
	PayloadHash  common.Hash         `json:"payload_hash"`
}

type systemDispatcher func(store rollup.Store, stateDB AccountStateAccessor, data []byte) error

func (f systemDispatcher) HandleSystemEvent(store rollup.Store, stateDB AccountStateAccessor, data []byte) error {
	return f(store, stateDB, data)
}

// newSystemHandler wires RollupSystemHandler to the REAL CrossNodeHandler with the same call shape as
// cmd/simple_chain handleRollupSystemEvent (payload JSON -> HandleSystemEvent).
func newSystemHandler() *RollupSystemHandler {
	var key mt_common.PublicKey
	key[0] = 9
	cn := rollup.NewCrossNodeHandler(key)
	return &RollupSystemHandler{dispatcher: systemDispatcher(func(store rollup.Store, stateDB AccountStateAccessor, data []byte) error {
		var p systemPayload
		if err := json.Unmarshal(data, &p); err != nil {
			return err
		}
		return cn.HandleSystemEvent(store, store, stateDB, p.Event, p.MsgID, p.SourceSeq, p.SourcePubKey, p.DestPubKey, p.PayloadHash)
	})}
}

var forgedMint = new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(1_000_000_000_000_000_000))

// The three events that, applied in order, credit Target/Value straight from the payload (no parent data consulted).
func forgedCreditEvents(target common.Address) []rollup.Event {
	return []rollup.Event{
		{Type: rollup.EventCreditObserved, Role: rollup.RoleReceiver, Target: target, Value: forgedMint, IsDestinationValid: true},
		{Type: rollup.EventRPCSubmitted, Role: rollup.RoleReceiver},
		{Type: rollup.EventClaimedConfirmed, Role: rollup.RoleReceiver, Outcome: rollup.OutcomeCredited, Target: target, Value: forgedMint},
	}
}

func systemTx(from common.Address, nonce uint64, ev rollup.Event) *transaction.Transaction {
	raw, _ := json.Marshal(systemPayload{Event: ev, MsgID: common.HexToHash("0xabc123"), SourceSeq: 1})
	return transaction.NewTransaction(from, rollup.RollupSystemAddress, big.NewInt(0), 21000, 1_000_000_000, 0, raw, nil,
		common.Hash{}, common.Hash{}, nonce, 1).(*transaction.Transaction)
}

// Before the fix an ordinary account with gas money could mint arbitrary balance by submitting these three events.
func TestRollupSystemHandler_UnauthorizedSenderCannotForgeEvents(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	db := cs.GetAccountStateDB()
	h := newSystemHandler()

	key, _ := crypto.GenerateKey()
	attacker := crypto.PubkeyToAddress(key.PublicKey) // ordinary user: not a node identity
	db.AddBalance(attacker, big.NewInt(1_000_000_000_000_000))
	loot := common.HexToAddress("0x000000000000000000000000000000000000dEaD")

	for i, ev := range forgedCreditEvents(loot) {
		rcp, _, err := h.HandleTransaction(context.Background(), cs, systemTx(attacker, uint64(i), ev), rollup.RollupSystemAddress, false, 0)
		require.NoError(t, err)
		assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcp.Status(), "event %d from an ordinary account must be rejected", i)
		assert.Contains(t, string(rcp.Return()), transaction.UnauthorizedSystemSender.Description)
	}
	as, err := db.AccountState(loot)
	require.NoError(t, err)
	assert.Equal(t, int64(0), as.Balance().Int64(), "no balance may be minted")
}

// Positive control: a BLS-native node identity (what eventProposer uses) is still accepted, and the same three events do
// credit — proving the test above rejects because of the sender, not because every system tx fails.
func TestRollupSystemHandler_NodeIdentityStillAccepted(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	db := cs.GetAccountStateDB()
	h := newSystemHandler()

	kp := bls.GenerateKeyPair()
	node := kp.Address()
	as := state.NewAccountState(node)
	as.AddBalance(big.NewInt(1_000_000_000_000_000))
	as.SetPublicKeyBls(kp.PublicKey().Bytes())
	db.SetState(as)
	target := common.HexToAddress("0x0000000000000000000000000000000000001234")

	for i, ev := range forgedCreditEvents(target) {
		rcp, _, err := h.HandleTransaction(context.Background(), cs, systemTx(node, uint64(i), ev), rollup.RollupSystemAddress, false, 0)
		require.NoError(t, err)
		assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcp.Status(), "event %d from the node identity: %s", i, string(rcp.Return()))
	}
	got, err := db.AccountState(target)
	require.NoError(t, err)
	assert.Equal(t, forgedMint.String(), got.Balance().String(), "authorized events still apply")
}

// Admission rejects the same forgery before it reaches a block, with the dedicated error code; a node identity passes.
func TestVerifyTransaction_RollupSystemEventSender(t *testing.T) {
	cs := setupTestChainState(t)
	cs.GetConfig().TxSignatureMode = "secp"

	key, _ := crypto.GenerateKey()
	user := crypto.PubkeyToAddress(key.PublicKey)
	asUser := state.NewAccountState(user)
	asUser.AddBalance(big.NewInt(1_000_000_000_000_000))
	asUser.SetNonce(1)
	userTx := systemTx(user, 1, forgedCreditEvents(common.Address{1})[0])
	require.NoError(t, userTx.SignSecpProto(key))
	rotateVerifiedSignatures()
	rotateVerifiedSignatures()
	verr := VerifyTransaction(userTx, cs, asUser)
	require.NotNil(t, verr)
	assert.Equal(t, transaction.UnauthorizedSystemSender.Code, verr.Code)

	kp := bls.GenerateKeyPair()
	node := kp.Address()
	asNode := state.NewAccountState(node)
	asNode.AddBalance(big.NewInt(1_000_000_000_000_000))
	asNode.SetPublicKeyBls(kp.PublicKey().Bytes())
	asNode.SetNonce(1)
	nodeTx := systemTx(node, 1, forgedCreditEvents(common.Address{1})[0])
	nodeTx.SetSignBytes(bls.Sign(kp.PrivateKey(), nodeTx.Hash().Bytes()).Bytes())
	rotateVerifiedSignatures()
	rotateVerifiedSignatures()
	if nerr := VerifyTransaction(nodeTx, cs, asNode); nerr != nil {
		assert.NotEqual(t, transaction.UnauthorizedSystemSender.Code, nerr.Code, "a node identity must not be rejected as unauthorized: %v", nerr)
	}
}

// The account-registration branch needs only the (public) cluster key in its payload, so without the sender check any
// account could mark itself parent-registered and bypass the account gate.
func TestRollupSystemHandler_ForgedRegistrationCannotSetFlag(t *testing.T) {
	var clusterKey mt_common.PublicKey
	clusterKey[0] = 0x42
	h := &RollupSystemHandler{
		dispatcher:      systemDispatcher(func(rollup.Store, AccountStateAccessor, []byte) error { return nil }),
		registryHandler: rollup.NewAccountRegistryHandler(clusterKey),
	}

	cs, _, _, _ := newPersistentTestChainState(t)
	db := cs.GetAccountStateDB()

	key, _ := crypto.GenerateKey()
	attacker := crypto.PubkeyToAddress(key.PublicKey)
	db.AddBalance(attacker, big.NewInt(1_000_000_000_000_000))

	forge := func(from common.Address, user common.Address, nonce uint64) (*transaction.Transaction, []byte) {
		raw, _ := json.Marshal(rollup.AccountRegistrationPayload{Kind: rollup.SystemPayloadKindAccountRegistered, User: user, ClusterKey: clusterKey, ParentSeq: 1})
		return transaction.NewTransaction(from, rollup.RollupSystemAddress, big.NewInt(0), 21000, 1_000_000_000, 0, raw, nil,
			common.Hash{}, common.Hash{}, nonce, 1).(*transaction.Transaction), raw
	}

	// Attacker registers ITSELF with a forged event.
	tx, _ := forge(attacker, attacker, 0)
	rcp, _, err := h.HandleTransaction(context.Background(), cs, tx, rollup.RollupSystemAddress, false, 0)
	require.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcp.Status())
	assert.Contains(t, string(rcp.Return()), transaction.UnauthorizedSystemSender.Description)
	as, err := db.AccountState(attacker)
	require.NoError(t, err)
	assert.False(t, as.ParentRegistered(), "a forged registration event must not set the flag")

	// Positive control: the same payload from a node identity registers the user.
	nodeKP := bls.GenerateKeyPair()
	node := nodeKP.Address()
	ns := state.NewAccountState(node)
	ns.AddBalance(big.NewInt(1_000_000_000_000_000))
	ns.SetPublicKeyBls(nodeKP.PublicKey().Bytes())
	db.SetState(ns)
	victim := common.HexToAddress("0x00000000000000000000000000000000000a11ce")
	addTestCommitteeValidator(t, cs, nodeKP)
	flushTestStake(t, cs)
	tx, _ = forge(node, victim, 0)
	tx = transaction.NewTransaction(node, rollup.RollupSystemAddress, big.NewInt(0), 21000, 1_000_000_000, 0,
		attestedRegistrationPayload(t, nodeKP, victim, clusterKey, 1), nil, common.Hash{}, common.Hash{}, 0, 1).(*transaction.Transaction)
	rcp, _, err = h.HandleTransaction(context.Background(), cs, tx, rollup.RollupSystemAddress, false, 0)
	require.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcp.Status(), string(rcp.Return()))
	vs, err := db.AccountState(victim)
	require.NoError(t, err)
	assert.True(t, vs.ParentRegistered(), "an authorized registration event must set the flag")
}
