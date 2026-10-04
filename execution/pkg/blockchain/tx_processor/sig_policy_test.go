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
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// createNodeIdentityTx builds a BLS-signed tx from a BLS-native identity (address derived from its own BLS key),
// i.e. what the execution node itself submits (rollup system txs). Returns the tx and the BLS public key.
func createNodeIdentityTx(to common.Address, amount *big.Int, nonce uint64) (types.Transaction, []byte) {
	kp := bls.GenerateKeyPair()
	tx := transaction.NewTransaction(
		kp.Address(), to, amount, p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, p_common.TRANSFER_GAS_COST,
		[]byte{}, nil, common.Hash{}, common.Hash{}, nonce, 1,
	)
	tx.(*transaction.Transaction).SetSignBytes(bls.Sign(kp.PrivateKey(), tx.Hash().Bytes()).Bytes())
	return tx, kp.PublicKey().Bytes()
}

func setMode(cs interface {
	GetConfig() *config.SimpleChainConfig
}, mode string) {
	cs.GetConfig().TxSignatureMode = mode
}

// Policy matrix, exec-time filter: legacy chain keeps the dapp BLS flow and rejects 0xFF; secp chain accepts
// secp-signed txs and BLS only from node identities.
func TestSigPolicy_ExecutionFilterMatrix(t *testing.T) {
	cs := setupTestChainState(t)
	to := common.HexToAddress("0x456")

	// user with a registered BLS key, address NOT derived from it (legacy dual-key dapp account)
	userFrom := common.BigToAddress(big.NewInt(81000))
	userBLS, userPub := createTestTx(userFrom, to, big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)
	asUser := state.NewAccountState(userFrom)
	asUser.AddBalance(big.NewInt(1_000_000_000_000_000))
	asUser.SetPublicKeyBls(userPub)
	asUser.SetNonce(1)
	cs.GetAccountStateDB().SetState(asUser)

	// node identity
	nodeTx, nodePub := createNodeIdentityTx(to, big.NewInt(1), 1)
	asNode := state.NewAccountState(nodeTx.FromAddress())
	asNode.AddBalance(big.NewInt(1_000_000_000_000_000))
	asNode.SetPublicKeyBls(nodePub)
	asNode.SetNonce(1)
	cs.GetAccountStateDB().SetState(asNode)

	// secp proto user
	key, _ := crypto.GenerateKey()
	secpFrom := crypto.PubkeyToAddress(key.PublicKey)
	asSecp := state.NewAccountState(secpFrom)
	asSecp.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSecp.SetNonce(1)
	cs.GetAccountStateDB().SetState(asSecp)
	proto0xFF := &transaction.Transaction{}
	proto0xFF.FromProto(&pb.Transaction{
		FromAddress: secpFrom.Bytes(), ToAddress: to.Bytes(), Amount: []byte{1},
		Nonce: []byte{0, 0, 0, 0, 0, 0, 0, 1}, MaxGas: p_common.TRANSFER_GAS_COST, MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID: 1, Type: 0xFF,
	})
	require.NoError(t, proto0xFF.SignSecpProto(key))

	txs := []types.Transaction{userBLS, nodeTx, proto0xFF}
	run := func() []bool {
		rotateVerifiedSignatures()
		rotateVerifiedSignatures()
		v, _ := verifySignatures(cs.GetAccountStateDB(), txs, nil, sigPolicyOf(cs))
		return v
	}

	setMode(cs, config.TxSignatureModeBLSLegacy)
	assert.Equal(t, []bool{true, true, false}, run(), "legacy: BLS dapp txs valid (user + node), 0xFF rejected")

	setMode(cs, "") // default must equal legacy
	assert.Equal(t, []bool{true, true, false}, run(), "empty mode == legacy")

	setMode(cs, config.TxSignatureModeSecp)
	assert.Equal(t, []bool{false, true, true}, run(), "secp: user BLS rejected, node-identity BLS and secp 0xFF valid")
}

// secp mode admission: a forged ETH-style tx from a secp-only account must be rejected even with nonce > 0
// (the legacy "sub-node lagging" bypass would otherwise skip verification for every account without a BLS key).
func TestVerifyTransaction_SecpMode_NoLaggingBypass(t *testing.T) {
	cs := setupTestChainState(t)
	setMode(cs, config.TxSignatureModeSecp)

	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1_000_000_000_000_000))
	as.SetNonce(1)

	forged := &transaction.Transaction{}
	forged.FromProto(&pb.Transaction{
		FromAddress: from.Bytes(), ToAddress: common.HexToAddress("0x456").Bytes(), Amount: []byte{1},
		Nonce: []byte{0, 0, 0, 0, 0, 0, 0, 1}, MaxGas: p_common.TRANSFER_GAS_COST, MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID: 1, Type: 2, R: []byte{1}, S: []byte{1}, V: []byte{0},
	})
	rotateVerifiedSignatures()
	rotateVerifiedSignatures()
	txErr := VerifyTransaction(forged, cs, as)
	require.NotNil(t, txErr, "unsigned/forged tx from a secp-only account must not pass admission")
	assert.Equal(t, transaction.InvalidSign.Code, txErr.Code)

	// A user BLS-signed tx is rejected in secp mode even though the BLS signature itself is valid.
	userFrom := common.BigToAddress(big.NewInt(82000))
	userTx, userPub := createTestTx(userFrom, common.HexToAddress("0x456"), big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)
	asU := state.NewAccountState(userFrom)
	asU.AddBalance(big.NewInt(1_000_000_000_000_000))
	asU.SetPublicKeyBls(userPub)
	asU.SetNonce(1)
	rotateVerifiedSignatures()
	rotateVerifiedSignatures()
	txErr = VerifyTransaction(userTx, cs, asU)
	require.NotNil(t, txErr)
	assert.Equal(t, transaction.InvalidSign.Code, txErr.Code)

	// ...while the same tx is fine on a legacy chain.
	setMode(cs, config.TxSignatureModeBLSLegacy)
	rotateVerifiedSignatures()
	rotateVerifiedSignatures()
	assert.Nil(t, VerifyTransaction(userTx, cs, asU), "legacy chain keeps the BLS dapp flow")
}

// 0xFF is disabled on a legacy chain at admission too.
func TestVerifyTransaction_Legacy_Rejects0xFF(t *testing.T) {
	cs := setupTestChainState(t) // legacy by default
	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1_000_000_000_000_000))
	as.SetNonce(1)
	tx := &transaction.Transaction{}
	tx.FromProto(&pb.Transaction{
		FromAddress: from.Bytes(), ToAddress: common.HexToAddress("0x456").Bytes(), Amount: []byte{1},
		Nonce: []byte{0, 0, 0, 0, 0, 0, 0, 1}, MaxGas: p_common.TRANSFER_GAS_COST, MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID: 1, Type: 0xFF,
	})
	require.NoError(t, tx.SignSecpProto(key))
	txErr := VerifyTransaction(tx, cs, as)
	require.NotNil(t, txErr)
	assert.Equal(t, transaction.InvalidSign.Code, txErr.Code)
}
