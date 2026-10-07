package tx_processor

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// signedEthProtoTx imports a genuine EIP-155 legacy tx signed for chainID, the way the RPC/TCP ingress does.
func signedEthProtoTx(t *testing.T, chainID int64, nonce uint64) (types.Transaction, common.Address) {
	t.Helper()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	to := common.HexToAddress("0x456")
	ethTx, err := e_types.SignNewTx(key, e_types.LatestSignerForChainID(big.NewInt(chainID)), &e_types.LegacyTx{
		Nonce: nonce, GasPrice: big.NewInt(int64(p_common.MINIMUM_BASE_FEE)), Gas: p_common.TRANSFER_GAS_COST, To: &to, Value: big.NewInt(1)})
	require.NoError(t, err)
	imp, err := transaction.NewTransactionFromEth(ethTx)
	require.NoError(t, err)
	return imp, crypto.PubkeyToAddress(key.PublicKey)
}

// In secp mode the consensus-level filter binds EVERY tx type to the chain, exactly like mempool admission. Before,
// the filter only checked type 0xFF, so an ETH tx signed for another chain (valid under ValidEthSign) passed it.
func TestChainBinding_ExecFilter_EthTxForeignChain(t *testing.T) {
	cases := []struct {
		mode      string
		foreignOK bool
	}{
		{config.TxSignatureModeBLSLegacy, true}, // legacy verdicts are intentionally unchanged (history)
		{config.TxSignatureModeSecp, false},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			cs := setupTestChainState(t) // node chain ID = 1
			cs.GetConfig().TxSignatureMode = c.mode

			own, ownFrom := signedEthProtoTx(t, 1, 1)
			foreign, foreignFrom := signedEthProtoTx(t, 99, 1)
			for _, a := range []common.Address{ownFrom, foreignFrom} {
				as := state.NewAccountState(a)
				as.AddBalance(big.NewInt(1_000_000_000_000_000))
				as.SetNonce(1)
				cs.GetAccountStateDB().SetState(as)
			}
			rotateVerifiedSignatures()
			rotateVerifiedSignatures()
			valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{own, foreign}, nil, sigPolicyOf(cs))
			assert.True(t, valid[0], "a tx for this chain is always valid")
			assert.Equal(t, c.foreignOK, valid[1], "tx signed for another chain, mode %q", c.mode)
		})
	}
}

// A secp-only account must be able to send its very first (nonce 0) ordinary tx; the legacy "first tx must bind a
// BLS key" rule (InvalidAddressMatchForTx0) is for BLS accounts only. The legacy chain keeps the rule.
func TestFirstTx_SecpOnlyAccount_Nonce0(t *testing.T) {
	for _, mode := range []string{config.TxSignatureModeSecp, config.TxSignatureModeBLSLegacy} {
		t.Run(mode, func(t *testing.T) {
			cs := setupTestChainState(t)
			cs.GetConfig().TxSignatureMode = mode
			key, _ := crypto.GenerateKey()
			from := crypto.PubkeyToAddress(key.PublicKey)
			as := state.NewAccountState(from)
			as.AddBalance(big.NewInt(1_000_000_000_000_000)) // nonce 0, no BLS key: a brand-new secp account
			tx := &transaction.Transaction{}
			tx.FromProto(&pb.Transaction{FromAddress: from.Bytes(), ToAddress: common.HexToAddress("0x456").Bytes(), Amount: []byte{1},
				Nonce: []byte{0, 0, 0, 0, 0, 0, 0, 0}, MaxGas: p_common.TRANSFER_GAS_COST, MaxGasPrice: p_common.MINIMUM_BASE_FEE, ChainID: 1, Type: 0xFF})
			require.NoError(t, tx.SignSecpProto(key))
			rotateVerifiedSignatures()
			rotateVerifiedSignatures()
			err := VerifyTransaction(tx, cs, as)
			if mode == config.TxSignatureModeSecp {
				assert.Nil(t, err, "fresh secp-only account must be able to send its first tx")
			} else {
				require.NotNil(t, err) // 0xFF is disabled on a legacy chain anyway
			}
		})
	}

	// Legacy rule still applies to a BLS-registered legacy account at nonce 0 sending an ordinary tx.
	cs := setupTestChainState(t)
	userFrom := common.BigToAddress(big.NewInt(83000))
	bls, pub := createTestTx(userFrom, common.HexToAddress("0x456"), big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 0)
	as := state.NewAccountState(userFrom)
	as.AddBalance(big.NewInt(1_000_000_000_000_000))
	as.SetPublicKeyBls(pub)
	rotateVerifiedSignatures()
	rotateVerifiedSignatures()
	verr := VerifyTransaction(bls, cs, as)
	require.NotNil(t, verr)
	assert.Equal(t, transaction.InvalidAddressMatchForTx0.Code, verr.Code, "legacy chain keeps the first-tx rule")
}

// Documents the operational invariant: replay protection is the chain ID alone. Two clusters configured with the SAME
// chain ID both accept the same signed tx (so an address funded with the same nonce on both can be replayed), while
// distinct chain IDs reject it. Chain IDs must therefore be unique per execution cluster.
func TestChainBinding_SameChainIDAcrossClusters_IsReplayable(t *testing.T) {
	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	tx := &transaction.Transaction{}
	tx.FromProto(&pb.Transaction{FromAddress: from.Bytes(), ToAddress: common.HexToAddress("0x456").Bytes(), Amount: []byte{1},
		Nonce: []byte{0, 0, 0, 0, 0, 0, 0, 1}, MaxGas: p_common.TRANSFER_GAS_COST, MaxGasPrice: p_common.MINIMUM_BASE_FEE, ChainID: 7, Type: 0xFF})
	require.NoError(t, tx.SignSecpProto(key))

	cluster := func(chainID int64) (verify func() *transaction.TransactionError) {
		cs := setupTestChainState(t)
		cs.GetConfig().TxSignatureMode = config.TxSignatureModeSecp
		cs.GetConfig().ChainId = big.NewInt(chainID)
		as := state.NewAccountState(from)
		as.AddBalance(big.NewInt(1_000_000_000_000_000))
		as.SetNonce(1) // same nonce and a balance on this cluster too
		return func() *transaction.TransactionError {
			rotateVerifiedSignatures()
			rotateVerifiedSignatures()
			return VerifyTransaction(tx, cs, as)
		}
	}
	clusterA, clusterBSameID, clusterCOtherID := cluster(7), cluster(7), cluster(8)

	assert.Nil(t, clusterA(), "cluster A accepts the tx")
	assert.Nil(t, clusterBSameID(), "RISK: a second cluster with the same chain ID accepts the very same signed tx (replay)")
	verr := clusterCOtherID()
	require.NotNil(t, verr)
	assert.Equal(t, transaction.InvalidChainId.Code, verr.Code, "a cluster with a different chain ID rejects it")
}
