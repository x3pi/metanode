package tx_processor

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// G1: Gate Matrix Test
// {registered, unregistered, node BLS identity, genesis account, rollup system tx} x
// {secp+gate ON, secp+gate OFF, bls_legacy} x
// {admission VerifyTransaction, execution filter checkTxSignature / verifySignatures}
func TestAccountGate_Matrix(t *testing.T) {
	chainID := big.NewInt(991)
	to := common.HexToAddress("0x789")

	// 1. Registered user
	regKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	regFrom := crypto.PubkeyToAddress(regKey.PublicKey)
	regTx := &transaction.Transaction{}
	regTx.FromProto(&pb.Transaction{
		FromAddress: regFrom.Bytes(), ToAddress: to.Bytes(), Amount: []byte{1},
		Nonce: []byte{0, 0, 0, 0, 0, 0, 0, 1}, MaxGas: p_common.TRANSFER_GAS_COST, MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID: chainID.Uint64(), Type: 0xFF,
	})
	require.NoError(t, regTx.SignSecpProto(regKey))

	asReg := state.NewAccountState(regFrom)
	asReg.AddBalance(big.NewInt(1_000_000_000_000_000))
	asReg.SetNonce(1)
	asReg.SetParentRegistered(true)

	// 2. Unregistered user
	unregKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	unregFrom := crypto.PubkeyToAddress(unregKey.PublicKey)
	unregTx := &transaction.Transaction{}
	unregTx.FromProto(&pb.Transaction{
		FromAddress: unregFrom.Bytes(), ToAddress: to.Bytes(), Amount: []byte{1},
		Nonce: []byte{0, 0, 0, 0, 0, 0, 0, 1}, MaxGas: p_common.TRANSFER_GAS_COST, MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID: chainID.Uint64(), Type: 0xFF,
	})
	require.NoError(t, unregTx.SignSecpProto(unregKey))

	asUnreg := state.NewAccountState(unregFrom)
	asUnreg.AddBalance(big.NewInt(1_000_000_000_000_000))
	asUnreg.SetNonce(1)
	asUnreg.SetParentRegistered(false)

	// 3. Node BLS identity
	nodeKPGen := bls.GenerateKeyPair()
	nodeTx := transaction.NewTransaction(
		nodeKPGen.Address(), to, big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, p_common.TRANSFER_GAS_COST,
		[]byte{}, nil, common.Hash{}, common.Hash{}, 1, chainID.Uint64(),
	)
	nodeTx.(*transaction.Transaction).SetSignBytes(bls.Sign(nodeKPGen.PrivateKey(), nodeTx.Hash().Bytes()).Bytes())
	nodePub := nodeKPGen.PublicKey().Bytes()

	asNode := state.NewAccountState(nodeTx.FromAddress())
	asNode.AddBalance(big.NewInt(1_000_000_000_000_000))
	asNode.SetPublicKeyBls(nodePub)
	asNode.SetNonce(1)

	// 4. Genesis account
	genKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	genFrom := crypto.PubkeyToAddress(genKey.PublicKey)
	genTx := &transaction.Transaction{}
	genTx.FromProto(&pb.Transaction{
		FromAddress: genFrom.Bytes(), ToAddress: to.Bytes(), Amount: []byte{1},
		Nonce: []byte{0, 0, 0, 0, 0, 0, 0, 1}, MaxGas: p_common.TRANSFER_GAS_COST, MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID: chainID.Uint64(), Type: 0xFF,
	})
	require.NoError(t, genTx.SignSecpProto(genKey))

	asGen := state.NewAccountState(genFrom)
	asGen.AddBalance(big.NewInt(1_000_000_000_000_000))
	asGen.SetNonce(1)
	asGen.SetParentRegistered(true)

	// 5. Rollup system tx (node identity sending to rollup.RollupSystemAddress)
	nodeKP := bls.GenerateKeyPair()
	systemTx := transaction.NewTransaction(
		nodeKP.Address(),
		rollup.RollupSystemAddress,
		big.NewInt(0),
		21000,
		p_common.MINIMUM_BASE_FEE,
		0,
		[]byte(`{"event":{"type":1}}`),
		nil,
		common.Hash{},
		common.Hash{},
		1,
		chainID.Uint64(),
	)
	systemTx.(*transaction.Transaction).SetSignBytes(bls.Sign(nodeKP.PrivateKey(), systemTx.Hash().Bytes()).Bytes())
	asSystemSender := state.NewAccountState(nodeKP.Address())
	asSystemSender.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSystemSender.SetPublicKeyBls(nodeKP.PublicKey().Bytes())
	asSystemSender.SetNonce(1)
	asSystemSender.SetParentRegistered(true)

	type testCase struct {
		name      string
		mode      string
		gate      string
		tx        types.Transaction
		as        types.AccountState
		wantAdmit *transaction.TransactionError
		wantExec  bool
	}

	testCases := []testCase{
		// --- Mode 1: secp + gate ON ---
		{
			name:      "secp+gate_ON / registered user",
			mode:      config.TxSignatureModeSecp,
			gate:      config.AccountGateParentRegistered,
			tx:        regTx,
			as:        asReg,
			wantAdmit: nil,
			wantExec:  true,
		},
		{
			name:      "secp+gate_ON / unregistered user",
			mode:      config.TxSignatureModeSecp,
			gate:      config.AccountGateParentRegistered,
			tx:        unregTx,
			as:        asUnreg,
			wantAdmit: transaction.AccountNotRegistered,
			wantExec:  false,
		},
		{
			name:      "secp+gate_ON / node BLS identity",
			mode:      config.TxSignatureModeSecp,
			gate:      config.AccountGateParentRegistered,
			tx:        nodeTx,
			as:        asNode,
			wantAdmit: nil,
			wantExec:  true,
		},
		{
			name:      "secp+gate_ON / genesis account",
			mode:      config.TxSignatureModeSecp,
			gate:      config.AccountGateParentRegistered,
			tx:        genTx,
			as:        asGen,
			wantAdmit: nil,
			wantExec:  true,
		},
		{
			name:      "secp+gate_ON / rollup system tx",
			mode:      config.TxSignatureModeSecp,
			gate:      config.AccountGateParentRegistered,
			tx:        systemTx,
			as:        asSystemSender,
			wantAdmit: nil,
			wantExec:  true,
		},

		// --- Mode 2: secp + gate OFF ---
		{
			name:      "secp+gate_OFF / registered user",
			mode:      config.TxSignatureModeSecp,
			gate:      "",
			tx:        regTx,
			as:        asReg,
			wantAdmit: nil,
			wantExec:  true,
		},
		{
			name:      "secp+gate_OFF / unregistered user",
			mode:      config.TxSignatureModeSecp,
			gate:      "",
			tx:        unregTx,
			as:        asUnreg,
			wantAdmit: nil, // gate is off, valid secp signature passes
			wantExec:  true,
		},
		{
			name:      "secp+gate_OFF / node BLS identity",
			mode:      config.TxSignatureModeSecp,
			gate:      "",
			tx:        nodeTx,
			as:        asNode,
			wantAdmit: nil,
			wantExec:  true,
		},

		// --- Mode 3: bls_legacy ---
		{
			name:      "bls_legacy / node BLS identity",
			mode:      config.TxSignatureModeBLSLegacy,
			gate:      "",
			tx:        nodeTx,
			as:        asNode,
			wantAdmit: nil,
			wantExec:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cs := setupTestChainState(t)
			cs.SetConfig(&config.SimpleChainConfig{
				ChainId:         chainID,
				TxSignatureMode: tc.mode,
				AccountGate:     tc.gate,
			})
			cs.GetAccountStateDB().SetState(tc.as)

			rotateVerifiedSignatures()
			rotateVerifiedSignatures()

			// 1. Admission: VerifyTransaction
			admitErr := VerifyTransaction(tc.tx, cs, tc.as)
			if tc.wantAdmit == nil {
				assert.Nil(t, admitErr, "VerifyTransaction should pass")
			} else {
				require.NotNil(t, admitErr, "VerifyTransaction should reject")
				assert.Equal(t, tc.wantAdmit.Code, admitErr.Code, "VerifyTransaction error code must match exactly")
			}

			// 2. Execution filter: checkTxSignature
			pol := sigPolicyOf(cs)
			execResult := checkTxSignature(tc.tx, tc.as, pol)
			assert.Equal(t, tc.wantExec, execResult, "checkTxSignature execution filter must match expectation")

			// 3. Execution filter: verifySignatures batch
			batchResults, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tc.tx}, func(i int) types.AccountState {
				return tc.as
			}, pol)
			require.Len(t, batchResults, 1)
			assert.Equal(t, tc.wantExec, batchResults[0], "verifySignatures must match expectation")

			// Invariant: Admission and execution filter MUST agree
			admitPassed := (admitErr == nil)
			assert.Equal(t, admitPassed, execResult, "admission and execution filter MUST reach identical verdict")
		})
	}
}

// G2: Cache behavior test
// Unregistered tx rejected -> valid signature cached -> account registered -> same tx re-submitted -> accepted immediately!
func TestAccountGate_SignatureCacheBehavior(t *testing.T) {
	chainID := big.NewInt(991)
	to := common.HexToAddress("0x789")

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(key.PublicKey)

	tx := &transaction.Transaction{}
	tx.FromProto(&pb.Transaction{
		FromAddress: from.Bytes(), ToAddress: to.Bytes(), Amount: []byte{1},
		Nonce: []byte{0, 0, 0, 0, 0, 0, 0, 1}, MaxGas: p_common.TRANSFER_GAS_COST, MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID: chainID.Uint64(), Type: 0xFF,
	})
	require.NoError(t, tx.SignSecpProto(key))

	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})

	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1_000_000_000_000_000))
	as.SetNonce(1)
	as.SetParentRegistered(false) // Initially UNREGISTERED
	cs.GetAccountStateDB().SetState(as)

	rotateVerifiedSignatures()
	rotateVerifiedSignatures()

	// Step 1: Submit while unregistered
	admitErr1 := VerifyTransaction(tx, cs, as)
	require.NotNil(t, admitErr1, "must be rejected when unregistered")
	assert.Equal(t, transaction.AccountNotRegistered.Code, admitErr1.Code)

	// Step 2: Verify that the cryptographic signature WAS cached despite gate rejection
	cacheKey := sigCacheKey(tx, nil)
	assert.True(t, LoadVerifiedSignature(cacheKey), "cryptographic validity must be cached in verifiedSignatures")

	// Step 3: Register the account (e.g. on-chain system registration event executed)
	as.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(as)

	// Step 4: Re-submit the EXACT SAME tx
	admitErr2 := VerifyTransaction(tx, cs, as)
	assert.Nil(t, admitErr2, "same tx must be accepted immediately after registration, without cache poisoning")

	// Step 5: Check execution filter
	pol := sigPolicyOf(cs)
	assert.True(t, checkTxSignature(tx, as, pol), "execution filter must accept registered account with cached signature")
}

// G3: Same-block behavior test
// Alice's tx in same block as registration system tx:
// Evaluated against pre-block state -> rejected/dropped
// In next block (post-registration state) -> accepted!
func TestAccountGate_SameBlockPreBlockState(t *testing.T) {
	chainID := big.NewInt(991)
	to := common.HexToAddress("0x789")

	aliceKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	aliceFrom := crypto.PubkeyToAddress(aliceKey.PublicKey)

	aliceTx := &transaction.Transaction{}
	aliceTx.FromProto(&pb.Transaction{
		FromAddress: aliceFrom.Bytes(), ToAddress: to.Bytes(), Amount: []byte{1},
		Nonce: []byte{0, 0, 0, 0, 0, 0, 0, 1}, MaxGas: p_common.TRANSFER_GAS_COST, MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID: chainID.Uint64(), Type: 0xFF,
	})
	require.NoError(t, aliceTx.SignSecpProto(aliceKey))

	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})
	pol := sigPolicyOf(cs)

	// Block N Pre-state: Alice is not yet registered
	preBlockAlice := state.NewAccountState(aliceFrom)
	preBlockAlice.AddBalance(big.NewInt(1_000_000_000_000_000))
	preBlockAlice.SetNonce(1)
	preBlockAlice.SetParentRegistered(false)
	cs.GetAccountStateDB().SetState(preBlockAlice)

	// In Block N, evaluating aliceTx against pre-block state must drop it
	assert.False(t, checkTxSignature(aliceTx, preBlockAlice, pol), "must be dropped in same block when evaluated against pre-block state")

	// System registration tx executes during Block N, state updates:
	postBlockAlice := preBlockAlice.Copy().(types.AccountState)
	postBlockAlice.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(postBlockAlice)

	// In Block N+1, evaluating aliceTx against post-registration state must pass
	assert.True(t, checkTxSignature(aliceTx, postBlockAlice, pol), "must be accepted in next block once registration is committed in state")
	assert.Nil(t, VerifyTransaction(aliceTx, cs, postBlockAlice), "admission must also pass for block N+1")
}

// X1: Cross-cluster replay test
// Two execution clusters with SAME chainId (991).
// User registers on Cluster A. Submits valid tx on Cluster A -> accepted.
// Attacker replays exact same signed tx on Cluster B -> rejected with AccountNotRegistered!
// Control: If Cluster B disables gate, replayed tx would be accepted.
func TestAccountGate_CrossClusterReplay(t *testing.T) {
	chainID := big.NewInt(991)
	to := common.HexToAddress("0x999")

	userKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	userFrom := crypto.PubkeyToAddress(userKey.PublicKey)

	// Signed tx for chainID 991
	userTx := &transaction.Transaction{}
	userTx.FromProto(&pb.Transaction{
		FromAddress: userFrom.Bytes(), ToAddress: to.Bytes(), Amount: []byte{100},
		Nonce: []byte{0, 0, 0, 0, 0, 0, 0, 1}, MaxGas: p_common.TRANSFER_GAS_COST, MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID: chainID.Uint64(), Type: 0xFF,
	})
	require.NoError(t, userTx.SignSecpProto(userKey))

	// Setup Cluster A (gate ON)
	csA := setupTestChainState(t)
	csA.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})
	asA := state.NewAccountState(userFrom)
	asA.AddBalance(big.NewInt(1_000_000_000_000_000))
	asA.SetNonce(1)
	asA.SetParentRegistered(true) // Registered on Cluster A
	csA.GetAccountStateDB().SetState(asA)

	// Setup Cluster B (gate ON)
	csB := setupTestChainState(t)
	csB.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})
	asB := state.NewAccountState(userFrom)
	asB.AddBalance(big.NewInt(1_000_000_000_000_000))
	asB.SetNonce(1)
	asB.SetParentRegistered(false) // NOT registered on Cluster B
	csB.GetAccountStateDB().SetState(asB)

	// Cluster A: Admission and execution both accept
	assert.Nil(t, VerifyTransaction(userTx, csA, asA), "Cluster A must accept registered user tx")
	assert.True(t, checkTxSignature(userTx, asA, sigPolicyOf(csA)), "Cluster A exec filter must pass")

	// Cluster B: Replayed tx MUST BE REJECTED at admission and dropped in execution filter
	replayErr := VerifyTransaction(userTx, csB, asB)
	require.NotNil(t, replayErr, "Cluster B must reject replayed tx from unregistered user")
	assert.Equal(t, transaction.AccountNotRegistered.Code, replayErr.Code, "Cluster B must return AccountNotRegistered")
	assert.False(t, checkTxSignature(userTx, asB, sigPolicyOf(csB)), "Cluster B exec filter must drop replayed tx")

	// CONTROL EXPERIMENT: If Cluster B had gate DISABLED, the replayed tx WOULD be accepted
	csBControl := setupTestChainState(t)
	csBControl.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateOff, // Gate turned OFF
	})
	csBControl.GetAccountStateDB().SetState(asB)
	assert.Nil(t, VerifyTransaction(userTx, csBControl, asB), "CONTROL: without gate, replayed tx would dangerously succeed!")
	assert.True(t, checkTxSignature(userTx, asB, sigPolicyOf(csBControl)), "CONTROL: without gate, replayed tx would execute on Cluster B!")
}
