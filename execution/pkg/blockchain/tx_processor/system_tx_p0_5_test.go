package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

func buildSystemTx(from common.Address, nonce uint64, chainID uint64, ev rollup.Event) *transaction.Transaction {
	p := rollup.RollupSystemPayload{Event: ev, MsgID: common.HexToHash("0xabc123"), SourceSeq: 1}
	raw, _ := rollup.MarshalRollupSystemPayload(&p)
	return transaction.NewTransaction(from, rollup.RollupSystemAddress, big.NewInt(0), 21000, 1_000_000_000, 0, raw, nil,
		common.Hash{}, common.Hash{}, nonce, chainID).(*transaction.Transaction)
}

// P0-5: Verify that BLS node identity transactions to RollupSystemAddress are admitted and executed.
func TestP0_5_RollupSystemTx_NodeIdentityAccepted(t *testing.T) {
	bls.Init()
	cs, _, _, _ := newPersistentTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         big.NewInt(991),
		TxSignatureMode: config.TxSignatureModeSecp,
	})
	db := cs.GetAccountStateDB()
	h := newSystemHandler()

	// 1. Setup Node BLS Identity
	nodeKP := bls.GenerateKeyPair()
	nodeAddr := nodeKP.Address()
	asNode := state.NewAccountState(nodeAddr)
	asNode.AddBalance(big.NewInt(1_000_000_000_000_000))
	asNode.SetPublicKeyBls(nodeKP.PublicKey().Bytes())
	asNode.SetNonce(1)
	db.SetState(asNode)

	// 2. Build system transaction to RollupSystemAddress signed by node BLS key
	target := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	events := forgedCreditEvents(target)
	tx := buildSystemTx(nodeAddr, 1, 991, events[0])
	tx.SetSignBytes(bls.Sign(nodeKP.PrivateKey(), tx.Hash().Bytes()).Bytes())

	// 3. Admission Verification: VerifyTransaction must admit node identity
	rotateVerifiedSignatures()
	rotateVerifiedSignatures()
	verr := VerifyTransaction(tx, cs, asNode)
	assert.Nil(t, verr, "node BLS identity sending to RollupSystemAddress must pass admission: %v", verr)

	// 4. Execution: HandleTransaction must succeed and return status RECEIPT_STATUS_RETURNED
	rcp, _, err := h.HandleTransaction(context.Background(), cs, tx, rollup.RollupSystemAddress, false, 0)
	require.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcp.Status(), "system transaction must execute successfully: %s", string(rcp.Return()))
}

// P0-5: Verify that transactions from ordinary users to RollupSystemAddress are rejected
// with UnauthorizedSystemSender at both admission and execution stages.
func TestP0_5_UserTxToRollupSystemAddress_Rejected(t *testing.T) {
	bls.Init()
	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         big.NewInt(991),
		TxSignatureMode: config.TxSignatureModeSecp,
	})
	db := cs.GetAccountStateDB()
	h := newSystemHandler()

	// 1. Setup ordinary user account (secp256k1 key, no BLS node identity)
	userKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	userAddr := crypto.PubkeyToAddress(userKey.PublicKey)

	asUser := state.NewAccountState(userAddr)
	asUser.AddBalance(big.NewInt(1_000_000_000_000_000))
	asUser.SetNonce(1)
	db.SetState(asUser)

	// 2. Build transaction to RollupSystemAddress signed with user's secp key
	target := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	events := forgedCreditEvents(target)
	tx := buildSystemTx(userAddr, 1, 991, events[0])
	require.NoError(t, tx.SignSecpProto(userKey))

	// 3. Admission check: must fail fast with UnauthorizedSystemSender
	rotateVerifiedSignatures()
	rotateVerifiedSignatures()
	verr := VerifyTransaction(tx, cs, asUser)
	require.NotNil(t, verr, "user transaction to RollupSystemAddress must be rejected at admission")
	assert.Equal(t, transaction.UnauthorizedSystemSender.Code, verr.Code)
	assert.Equal(t, transaction.UnauthorizedSystemSender.Description, verr.Description)

	// 4. Execution defense-in-depth: HandleTransaction must also reject with UnauthorizedSystemSender
	rcp, _, err := h.HandleTransaction(context.Background(), cs, tx, rollup.RollupSystemAddress, false, 0)
	require.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcp.Status())
	assert.Contains(t, string(rcp.Return()), transaction.UnauthorizedSystemSender.Description)
}

// P0-5: Verify that root-anchor submitter generates valid EIP-155 secp256k1 envelopes
// addressed to GATEWAY_CONTRACT_ADDRESS that can be parsed and admitted on Root Anchor (ChainID 991).
func TestP0_5_RootAnchorSubmitter_EnvelopeEIP155Valid(t *testing.T) {
	chainID := big.NewInt(991)

	// 1. Generate submitter ECDSA key
	submitterKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	submitterAddr := crypto.PubkeyToAddress(submitterKey.PublicKey)

	// 2. Build EIP-155 envelope as signAndSubmit does in CommitteeAttestationWorker
	calldata := []byte("dummyCommitteeUpdateCalldata")
	const gasLimit = uint64(2_000_000)
	gasPrice := big.NewInt(20_000_000_000)
	nonce := uint64(0)

	ethTx := ethtypes.NewTransaction(nonce, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), gasLimit, gasPrice, calldata)
	signedTx, err := ethtypes.SignTx(ethTx, ethtypes.NewEIP155Signer(chainID), submitterKey)
	require.NoError(t, err)

	rawTxBytes, err := signedTx.MarshalBinary()
	require.NoError(t, err)

	// 3. Ingress verification: Envelope unmarshal and validation must succeed
	parsedEthTx := new(ethtypes.Transaction)
	require.NoError(t, parsedEthTx.UnmarshalBinary(rawTxBytes))

	err = transaction.ValidateEthTxEnvelope(parsedEthTx, chainID)
	require.NoError(t, err)

	metaTx, err := transaction.NewTransactionFromEth(parsedEthTx)
	require.NoError(t, err, "raw envelope from root-anchor submitter must parse cleanly")
	assert.Equal(t, submitterAddr, metaTx.FromAddress(), "recovered sender must match submitter address")
	assert.Equal(t, mt_common.GATEWAY_CONTRACT_ADDRESS, metaTx.ToAddress(), "recipient must be GATEWAY_CONTRACT_ADDRESS")
	assert.Equal(t, chainID.Uint64(), metaTx.GetChainID(), "chain ID must match 991")
	assert.Equal(t, nonce, metaTx.GetNonce())
}
