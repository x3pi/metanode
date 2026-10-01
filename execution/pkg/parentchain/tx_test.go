package parentchain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	bls.Init()
}

// helper to register a cluster in store and return keys
func setupCluster(t *testing.T, store Store, clusterID uint64) (cm.PrivateKey, cm.PublicKey, common.Address) {
	kp := bls.GenerateKeyPair()
	priv := kp.PrivateKey()
	pub := kp.PublicKey()
	addr := kp.Address()
	clusterHash := crypto.Keccak256Hash(pub[:])
	err := store.SetChainRegistry(clusterHash, ChainRegistryEntry{
		FloatIdentityKey:     pub,
		ClusterIDDescriptive: clusterID,
		ChainIDDescriptive:   clusterID,
	})
	require.NoError(t, err)
	err = store.SetAccountRegistry(addr, pub)
	require.NoError(t, err)
	return priv, pub, addr
}

// T-U13: chữ ký sai/thiếu => tx bị loại đồng nhất, không đổi state/nonce.
func TestTx_SignatureVerification(t *testing.T) {
	store := NewMemoryStore()
	priv, pub, addr := setupCluster(t, store, 101)

	callData := EncodeRegisterClusterCallData(pub, 101)
	tx, err := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 0, callData)
	require.NoError(t, err)

	// Valid tx succeeds
	rcpt, err := ExecuteTx(store, tx, 1000)
	require.NoError(t, err)
	assert.Equal(t, uint8(1), rcpt.Status)

	nonce, err := store.GetNonce(addr)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), nonce)

	// Tampered signature
	txBadSig, err := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 1, callData)
	require.NoError(t, err)
	txBadSig.Sign[0] ^= 0xFF // corrupt signature byte

	rcptBad, err := ExecuteTx(store, txBadSig, 1001)
	assert.Error(t, err)
	assert.Equal(t, uint8(0), rcptBad.Status)

	// Nonce must not advance on signature failure
	nonceAfter, _ := store.GetNonce(addr)
	assert.Equal(t, uint64(1), nonceAfter)

	// Missing signature
	txNoSig := &pb.Transaction{
		FromAddress: addr.Bytes(),
		ToAddress:   ParentChainGatewayAddress.Bytes(),
		Nonce:       EncodeUint64(1),
		ChainID:     ParentChainID,
		Data:        callData,
	}
	_, errNoSig := ExecuteTx(store, txNoSig, 1002)
	assert.Error(t, errNoSig)
}

// T-U14: nonce cũ/nhảy => loại; nonce chỉ tăng khi tx hợp lệ.
func TestTx_NonceChecks(t *testing.T) {
	store := NewMemoryStore()
	priv, pub, _ := setupCluster(t, store, 101)

	callData := EncodeRegisterClusterCallData(pub, 101)

	// Jumped nonce: expected 0, but provide 5
	txJump, err := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 5, callData)
	require.NoError(t, err)
	rcptJump, err := ExecuteTx(store, txJump, 1000)
	assert.Error(t, err)
	assert.Equal(t, uint8(0), rcptJump.Status)
	assert.ErrorIs(t, err, ErrWrongNonce)

	// Now send valid nonce 0
	tx0, err := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 0, callData)
	require.NoError(t, err)
	rcpt0, err := ExecuteTx(store, tx0, 1001)
	require.NoError(t, err)
	assert.Equal(t, uint8(1), rcpt0.Status)

	// Old nonce: try sending nonce 0 again (replay)
	rcptOld, err := ExecuteTx(store, tx0, 1002)
	assert.Error(t, err)
	assert.Equal(t, uint8(0), rcptOld.Status)
	assert.ErrorIs(t, err, ErrWrongNonce)
}

// T-U15: ChainID sai => loại.
func TestTx_ChainIDVerification(t *testing.T) {
	store := NewMemoryStore()
	priv, pub, _ := setupCluster(t, store, 101)

	callData := EncodeRegisterClusterCallData(pub, 101)
	tx, err := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 0, callData)
	require.NoError(t, err)

	// Set wrong chain ID
	tx.ChainID = 991 // Root Anchor instead of Parent Chain (990)
	// re-sign for new hash
	txHash := ComputeTxHash(tx)
	tx.Sign = bls.Sign(priv, txHash[:]).Bytes()

	rcpt, err := ExecuteTx(store, tx, 1000)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidChainID)
	assert.Equal(t, uint8(0), rcpt.Status)
}

// T-U16: depositToFloat credits account and records messageID.
func TestTx_DepositToFloat(t *testing.T) {
	store := NewMemoryStore()
	priv, pub, _ := setupCluster(t, store, 101)

	msgID := common.HexToHash("0x11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff")
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	amount := big.NewInt(1000000)

	dig := ComputeDepositFloatMessage(pub, 101, sender, target, amount, msgID)
	cert := bls.Sign(priv, dig)
	callData := EncodeDepositToFloatCallData(pub, pub, 101, sender, target, amount, msgID, cert)
	tx, err := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 0, callData)
	require.NoError(t, err)

	rcpt, err := ExecuteTx(store, tx, 1000)
	require.NoError(t, err)
	assert.Equal(t, uint8(1), rcpt.Status)
	assert.Len(t, rcpt.Events, 1)
	assert.Equal(t, msgID.Bytes(), rcpt.Events[0])

	// Balance credited
	destHash := crypto.Keccak256Hash(pub[:])
	bal, err := store.GetFloat(destHash)
	require.NoError(t, err)
	assert.Equal(t, amount, bal)

	// Total supply updated
	total, err := store.GetFloat(common.Hash{})
	require.NoError(t, err)
	assert.Equal(t, amount, total)
}

// T-U17: trùng msgID => áp dụng đúng một lần, lần thứ hai lỗi.
func TestTx_DuplicateMsgID(t *testing.T) {
	store := NewMemoryStore()
	priv, pub, _ := setupCluster(t, store, 101)

	msgID := common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	dig := ComputeDepositFloatMessage(pub, 101, common.Address{}, common.Address{}, big.NewInt(500), msgID)
	cert := bls.Sign(priv, dig)
	callData := EncodeDepositToFloatCallData(pub, pub, 101, common.Address{}, common.Address{}, big.NewInt(500), msgID, cert)

	// Tx 1: nonce 0
	tx1, err := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 0, callData)
	require.NoError(t, err)
	rcpt1, err := ExecuteTx(store, tx1, 1000)
	require.NoError(t, err)
	assert.Equal(t, uint8(1), rcpt1.Status)

	// Tx 2: nonce 1, but same msgID
	tx2, err := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 1, callData)
	require.NoError(t, err)
	rcpt2, err := ExecuteTx(store, tx2, 1001)
	assert.Error(t, err)
	assert.Equal(t, uint8(0), rcpt2.Status)
	assert.Contains(t, err.Error(), ErrFloatAlreadyResolved.Error())
}

// T-U18: mọi handler kiểm ủy quyền bên trong (unauthorized sender rejected).
func TestTx_HandlerAuthorizationChecks(t *testing.T) {
	store := NewMemoryStore()
	priv1, pub1, _ := setupCluster(t, store, 101)
	priv2, pub2, _ := setupCluster(t, store, 102)

	// Deposit initial funds to cluster 1
	msgID := common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	dig := ComputeDepositFloatMessage(pub1, 101, common.Address{}, common.Address{}, big.NewInt(1000), msgID)
	cert := bls.Sign(priv1, dig)
	require.NoError(t, DepositToFloat(store, pub1, pub1, 101, common.Address{}, common.Address{}, big.NewInt(1000), msgID, cert, 1000))

	// TransferFloat initiated by cluster 1
	val := big.NewInt(100)
	fee := big.NewInt(0)
	payloadHash := crypto.Keccak256Hash(nil)
	transDigest := ComputeTransferFloatMessage(pub1, pub2, common.Address{}, common.Address{}, val, fee, payloadHash, 0)
	certTrans := bls.Sign(priv1, transDigest)

	callDataTransfer := EncodeTransferFloatCallData(pub2, 102, common.Address{}, common.Address{}, val, fee, 0, certTrans, false, nil)

	// Cluster 1 executes it (authorized)
	txTrans, err := BuildAndSignBLSTx(priv1, pub1, ParentChainGatewayAddress, 0, callDataTransfer)
	require.NoError(t, err)
	rcptTrans, err := ExecuteTx(store, txTrans, 1001)
	require.NoError(t, err)
	assert.Equal(t, uint8(1), rcptTrans.Status)

	// Cluster 1 tries to call MarkClaimed for the transfer (unauthorized: only DestKey cluster 2 can claim)
	transMsgID := common.BytesToHash(rcptTrans.Events[0])
	claimDigest := ComputeMarkClaimedMessage(transMsgID, FloatOutcomeCredited)
	certClaim1 := bls.Sign(priv1, claimDigest)
	callDataClaim1 := EncodeMarkClaimedCallData(transMsgID, FloatOutcomeCredited, certClaim1)

	txClaimUnauth, err := BuildAndSignBLSTx(priv1, pub1, ParentChainGatewayAddress, 1, callDataClaim1)
	require.NoError(t, err)

	rcptClaimUnauth, err := ExecuteTx(store, txClaimUnauth, 1002)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrUnauthorizedSender)
	assert.Equal(t, uint8(0), rcptClaimUnauth.Status)

	// Cluster 2 calls MarkClaimed (authorized)
	certClaim2 := bls.Sign(priv2, claimDigest)
	callDataClaim2 := EncodeMarkClaimedCallData(transMsgID, FloatOutcomeCredited, certClaim2)
	txClaimAuth, err := BuildAndSignBLSTx(priv2, pub2, ParentChainGatewayAddress, 0, callDataClaim2)
	require.NoError(t, err)
	rcptClaimAuth, err := ExecuteTx(store, txClaimAuth, 1003)
	require.NoError(t, err)
	assert.Equal(t, uint8(1), rcptClaimAuth.Status)

	// SubmitStateRoot: Cluster 2 tries to submit state root for Cluster 1 (unauthorized)
	root := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	rootDigest := ComputeSubmitStateRootMessage(pub1, 1, root)
	certRoot := bls.Sign(priv2, rootDigest)
	callDataRoot := EncodeSubmitStateRootCallData(pub1, 1, root, certRoot)

	txRootUnauth, err := BuildAndSignBLSTx(priv2, pub2, ParentChainGatewayAddress, 1, callDataRoot)
	require.NoError(t, err)

	rcptRootUnauth, err := ExecuteTx(store, txRootUnauth, 1004)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrUnauthorizedSender)
	assert.Equal(t, uint8(0), rcptRootUnauth.Status)
}
