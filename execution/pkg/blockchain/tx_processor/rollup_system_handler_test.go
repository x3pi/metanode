package tx_processor

import (
	"context"
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/stretchr/testify/assert"
)

type MockRollupSystemDispatcher struct {
	HandleSystemEventCalled bool
	LastData                []byte
	MockError               error
}

func (m *MockRollupSystemDispatcher) HandleSystemEvent(
	store rollup.Store,
	stateDB AccountStateAccessor,
	data []byte,
) error {
	m.HandleSystemEventCalled = true
	m.LastData = data
	return m.MockError
}

// newNodeIdentityAccount creates a funded BLS-native node identity (address derived from its own registered BLS key):
// the only kind of sender allowed to submit a rollup system event (see isAuthorizedRollupSystemSender).
func newNodeIdentityAccount(db *account_state_db.AccountStateDB, balance *big.Int) common.Address {
	kp := bls.GenerateKeyPair()
	as := state.NewAccountState(kp.Address())
	as.AddBalance(balance)
	as.SetPublicKeyBls(kp.PublicKey().Bytes())
	db.SetState(as)
	return kp.Address()
}

func TestRollupSystemHandler_HandleTransaction(t *testing.T) {
	mockDispatcher := &MockRollupSystemDispatcher{}
	InitRollupSystemHandler(mockDispatcher)
	handler := GetRollupSystemHandler()
	assert.NotNil(t, handler)

	chainState, _, _, _ := newPersistentTestChainState(t)
	stateDB := chainState.GetAccountStateDB()
	initialBalance := big.NewInt(1000000000000000000) // 1 ETH
	senderAddr := newNodeIdentityAccount(stateDB, initialBalance)

	payload := []byte(`{"type":"test"}`)
	tx := transaction.NewTransaction(
		senderAddr,
		rollup.RollupSystemAddress,
		big.NewInt(0),
		21000,
		1000, // gasPrice
		0,
		payload,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1,
	)

	receipt, _, err := handler.HandleTransaction(context.Background(), chainState, tx, rollup.RollupSystemAddress, false, 0)
	assert.NoError(t, err)
	assert.NotNil(t, receipt)
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
	assert.True(t, mockDispatcher.HandleSystemEventCalled)

	// Verify gas deduction
	accountState, _ := stateDB.AccountState(senderAddr)
	newBalance := accountState.Balance()
	gasFee := new(big.Int).Mul(big.NewInt(int64(mt_common.TRANSFER_GAS_COST)), big.NewInt(1000))
	expectedBalance := new(big.Int).Sub(initialBalance, gasFee)
	assert.Equal(t, expectedBalance.String(), newBalance.String())

	// Verify nonce advanced
	assert.Equal(t, uint64(1), accountState.Nonce())
}

func TestRollupSystemHandler_AccountRegistration_Success(t *testing.T) {
	mockDispatcher := &MockRollupSystemDispatcher{}
	var clusterKey mt_common.PublicKey
	copy(clusterKey[:], []byte("cluster_pub_key_32_bytes_long!!"))
	regHandler := rollup.NewAccountRegistryHandler(clusterKey)

	InitRollupSystemHandler(mockDispatcher, regHandler)
	handler := GetRollupSystemHandler()
	handler.SetRegistryHandler(regHandler)
	assert.NotNil(t, handler)

	chainState, _, _, _ := newPersistentTestChainState(t)
	userAddr := common.HexToAddress("0x5555555555555555555555555555555555555555")

	stateDB := chainState.GetAccountStateDB()
	initialBalance := big.NewInt(1000000000000000000)
	nodeKP := bls.GenerateKeyPair()
	ns := state.NewAccountState(nodeKP.Address())
	ns.AddBalance(initialBalance)
	ns.SetPublicKeyBls(nodeKP.PublicKey().Bytes())
	stateDB.SetState(ns)
	senderAddr := nodeKP.Address()
	// the node is the (only) validator of the committee, so its own attestation reaches f+1
	addTestCommitteeValidator(t, chainState, nodeKP)
	flushTestStake(t, chainState)

	payload := attestedRegistrationPayload(t, nodeKP, userAddr, clusterKey, 1)
	tx := transaction.NewTransaction(
		senderAddr,
		rollup.RollupSystemAddress,
		big.NewInt(0),
		21000,
		1000,
		0,
		payload,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1,
	)

	receipt, _, err := handler.HandleTransaction(context.Background(), chainState, tx, rollup.RollupSystemAddress, false, 0)
	assert.NoError(t, err)
	assert.NotNil(t, receipt)
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())

	// User state must be registered
	userState, err := stateDB.AccountState(userAddr)
	assert.NoError(t, err)
	assert.NotNil(t, userState)
	assert.True(t, userState.ParentRegistered())

	// Sender nonce must advance
	senderState, _ := stateDB.AccountState(senderAddr)
	assert.Equal(t, uint64(1), senderState.Nonce())
}

func TestRollupSystemHandler_AccountRegistration_ErrorConsumesNonce(t *testing.T) {
	mockDispatcher := &MockRollupSystemDispatcher{}
	var clusterKey mt_common.PublicKey
	copy(clusterKey[:], []byte("cluster_pub_key_32_bytes_long!!"))
	regHandler := rollup.NewAccountRegistryHandler(clusterKey)

	InitRollupSystemHandler(mockDispatcher, regHandler)
	handler := GetRollupSystemHandler()
	handler.SetRegistryHandler(regHandler)
	assert.NotNil(t, handler)

	chainState, _, _, _ := newPersistentTestChainState(t)
	userAddr := common.HexToAddress("0x7777777777777777777777777777777777777777")

	stateDB := chainState.GetAccountStateDB()
	initialBalance := big.NewInt(1000000000000000000)
	senderAddr := newNodeIdentityAccount(stateDB, initialBalance)

	// Payload with mismatch cluster key
	var wrongClusterKey mt_common.PublicKey
	copy(wrongClusterKey[:], []byte("wrong_cluster_key_32_bytes_long"))
	regPayload := rollup.AccountRegistrationPayload{
		Kind:       rollup.SystemPayloadKindAccountRegistered,
		User:       userAddr,
		ClusterKey: wrongClusterKey,
		ParentSeq:  1,
	}
	payload, _ := regPayload.MarshalProto()
	tx := transaction.NewTransaction(
		senderAddr,
		rollup.RollupSystemAddress,
		big.NewInt(0),
		21000,
		1000,
		0,
		payload,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1,
	)

	receipt, _, err := handler.HandleTransaction(context.Background(), chainState, tx, rollup.RollupSystemAddress, false, 0)
	assert.NoError(t, err)
	assert.NotNil(t, receipt)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, receipt.Status())
	assert.Contains(t, string(receipt.Return()), "cluster key mismatch")

	// User state must NOT be registered
	userState, _ := stateDB.AccountState(userAddr)
	if userState != nil {
		assert.False(t, userState.ParentRegistered())
	}

	// Sender nonce MUST still advance on error to avoid wedging the system tx queue!
	senderState, _ := stateDB.AccountState(senderAddr)
	assert.Equal(t, uint64(1), senderState.Nonce())
}

// F1: Fuzz test on RollupSystemHandler.HandleTransaction
// 2000 rounds of pseudo-random payloads with fixed seed:
// Never panics, never marks any account as ParentRegistered, and always handles safely.
func TestRollupSystemHandler_FuzzPayload(t *testing.T) {
	mockDispatcher := &MockRollupSystemDispatcher{}
	var clusterKey mt_common.PublicKey
	copy(clusterKey[:], []byte("cluster_pub_key_32_bytes_long!!"))
	regHandler := rollup.NewAccountRegistryHandler(clusterKey)

	InitRollupSystemHandler(mockDispatcher, regHandler)
	handler := GetRollupSystemHandler()
	handler.SetRegistryHandler(regHandler)
	assert.NotNil(t, handler)

	chainState, _, _, _ := newPersistentTestChainState(t)
	stateDB := chainState.GetAccountStateDB()
	senderAddr := newNodeIdentityAccount(stateDB, big.NewInt(1000000000000000000))

	targetUser := common.HexToAddress("0x7777777777777777777777777777777777777777")

	rng := rand.New(rand.NewSource(42))

	const rounds = 2000
	for i := 0; i < rounds; i++ {
		// Generate random payload
		payloadLen := rng.Intn(256)
		payload := make([]byte, payloadLen)
		rng.Read(payload)

		tx := transaction.NewTransaction(
			senderAddr,
			rollup.RollupSystemAddress,
			big.NewInt(0),
			21000,
			1000,
			0,
			payload,
			nil,
			common.Hash{},
			common.Hash{},
			uint64(i),
			1,
		)

		// Must never panic
		assert.NotPanics(t, func() {
			receipt, _, err := handler.HandleTransaction(context.Background(), chainState, tx, rollup.RollupSystemAddress, false, 0)
			// Must never crash or return fatal system error
			assert.NoError(t, err)
			assert.NotNil(t, receipt)
		}, "fuzz payload round %d panicked", i)

		// Target user must NEVER become registered from fuzz data
		userState, _ := stateDB.AccountState(targetUser)
		if userState != nil {
			assert.False(t, userState.ParentRegistered(), "fuzz data must never register user")
		}
	}

	// Sender nonce should have advanced consistently
	senderState, _ := stateDB.AccountState(senderAddr)
	assert.Equal(t, uint64(rounds), senderState.Nonce())
}
