package parentchain

import (
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func init() {
	bls.Init()
	// Most tests register clusters freely; policy-specific tests install their own policy and restore this one.
	SetClusterPolicy(ClusterPolicy{Open: true, AllowDeposit: true})
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
		Authorized:           true,
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
	tx.ChainID = ParentChainID + 1 // any other chain ID (e.g. Root Anchor)
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

// There is exactly one way to authenticate a transaction: a signature over its hash plus a sequential nonce.
// The old alternate modes (no signature at all for deposits, a signature over a method-specific digest, the
// cluster certificate reused as the transaction signature) must all be rejected without touching state.
func TestTx_NoAlternateAuthenticationModes(t *testing.T) {
	store := NewMemoryStore()
	priv, pub, addr := setupCluster(t, store, 101)

	amount := big.NewInt(500)
	msgID := common.HexToHash("0x01")
	dig := ComputeDepositFloatMessage(pub, 101, common.Address{}, common.Address{}, amount, msgID)
	cert := bls.Sign(priv, dig)
	callData := EncodeDepositToFloatCallData(pub, pub, 101, common.Address{}, common.Address{}, amount, msgID, cert)

	// (1) A deposit with a valid certificate but NO transaction signature (the old "API boundary" mode).
	unsigned := &pb.Transaction{
		FromAddress: addr.Bytes(),
		ToAddress:   ParentChainGatewayAddress.Bytes(),
		Nonce:       make([]byte, 8),
		Data:        callData,
		ChainID:     ParentChainID,
	}
	rcpt, err := ExecuteTx(store, unsigned, 1)
	assert.Error(t, err)
	assert.Equal(t, uint8(0), rcpt.Status)
	assert.Equal(t, uint32(104), uint32(rcpt.ErrorCode))

	// (2) The certificate reused as the transaction signature (signature over the method digest, not the tx hash).
	certAsSig := proto.Clone(unsigned).(*pb.Transaction)
	certAsSig.Sign = cert.Bytes()
	rcpt, err = ExecuteTx(store, certAsSig, 1)
	assert.Error(t, err)
	assert.Equal(t, uint8(0), rcpt.Status)

	// (3) Neither attempt may have changed state or advanced the nonce.
	nonce, _ := store.GetNonce(addr)
	assert.Equal(t, uint64(0), nonce)
	bal, _ := store.GetFloat(crypto.Keccak256Hash(pub[:]))
	assert.True(t, bal == nil || bal.Sign() == 0, "no float may be credited")

	// (4) The properly signed transaction is accepted.
	good, err := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 0, callData)
	require.NoError(t, err)
	rcpt, err = ExecuteTx(store, good, 1)
	require.NoError(t, err)
	assert.Equal(t, uint8(1), rcpt.Status)

	// (5) Replaying the very same signed transaction fails on the nonce.
	rcpt, err = ExecuteTx(store, good, 2)
	assert.Error(t, err)
	assert.Equal(t, uint32(103), uint32(rcpt.ErrorCode))
}

// Two identical deposits from the same client must get distinct message ids (they are derived from the nonce).
func TestQuorumClient_ConsecutiveDepositsUniqueMsgID(t *testing.T) {
	kp := bls.GenerateKeyPair()
	var mu sync.Mutex
	var msgIDs []common.Hash
	next := uint64(0)

	mux := http.NewServeMux()
	mux.HandleFunc("/nonce", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{"nonce": next})
	})
	mux.HandleFunc("/receipt", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"found": false})
	})
	mux.HandleFunc("/send_raw_transaction", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var tx pb.Transaction
		if err := proto.Unmarshal(raw, &tx); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _, _, _, _, _, msgID, _, err := DecodeDepositToFloatCallData(tx.Data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		msgIDs = append(msgIDs, msgID)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{"tx_hash": ComputeTxHash(&tx), "status": "queued"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	qc := NewQuorumClient([]string{ts.URL}, kp.PrivateKey(), kp.PublicKey())
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	for i := 0; i < 3; i++ {
		_, err := qc.SendDepositToFloat(kp.PublicKey(), 101, sender, sender, big.NewInt(1000))
		require.NoError(t, err)
	}
	require.Len(t, msgIDs, 3)
	assert.NotEqual(t, msgIDs[0], msgIDs[1])
	assert.NotEqual(t, msgIDs[1], msgIDs[2])
}

// Cluster registration is a trust anchor: with the default (closed) policy nobody may register, with a
// genesis allow-list only the listed keys may, and only an explicit open policy lets anyone in.
func TestTx_ClusterRegistrationPolicy(t *testing.T) {
	defer SetClusterPolicy(ClusterPolicy{Open: true, AllowDeposit: true})

	register := func(kp *bls.KeyPair, nonce uint64, store Store) (*Receipt, error) {
		tx, err := BuildAndSignBLSTx(kp.PrivateKey(), kp.PublicKey(), ParentChainGatewayAddress, nonce, EncodeRegisterClusterCallData(kp.PublicKey(), 7))
		require.NoError(t, err)
		return ExecuteTx(store, tx, 1)
	}
	allowed := bls.GenerateKeyPair()
	stranger := bls.GenerateKeyPair()

	// Closed (secure default): nothing registers.
	SetClusterPolicy(ClusterPolicy{})
	store := NewMemoryStore()
	rcpt, err := register(allowed, 0, store)
	assert.ErrorIs(t, err, ErrClusterNotAuthorized)
	assert.Equal(t, uint32(221), uint32(rcpt.ErrorCode))

	// Allow-list: only the listed key.
	SetClusterPolicy(ClusterPolicy{Allowed: map[cm.PublicKey]struct{}{allowed.PublicKey(): {}}})
	store = NewMemoryStore()
	_, err = register(stranger, 0, store)
	assert.ErrorIs(t, err, ErrClusterNotAuthorized, "a key outside the genesis list must not register")
	rcpt, err = register(allowed, 0, store)
	require.NoError(t, err)
	assert.Equal(t, uint8(1), rcpt.Status)
	_, found, _ := store.GetChainRegistry(crypto.Keccak256Hash(allowed.PublicKey().Bytes()))
	assert.True(t, found)

	// Open (devnet): anyone.
	SetClusterPolicy(ClusterPolicy{Open: true, AllowDeposit: true})
	store = NewMemoryStore()
	rcpt, err = register(stranger, 0, store)
	require.NoError(t, err)
	assert.Equal(t, uint8(1), rcpt.Status)
}

// A deposit mints float, so it must always be certified by a registered cluster: a zero source key, an
// unregistered source, or a certificate from a different key must all be rejected and change nothing.
func TestDeposit_AlwaysRequiresRegisteredCertifyingSource(t *testing.T) {
	store := NewMemoryStore()
	priv, pub, _ := setupCluster(t, store, 101)
	stranger := bls.GenerateKeyPair()
	amount := big.NewInt(1000)
	msgID := common.HexToHash("0xaa")
	var zero cm.PublicKey

	digest := ComputeDepositFloatMessage(pub, 101, common.Address{}, common.Address{}, amount, msgID)

	// (1) zero source key: the old "unauthenticated mint" bypass.
	err := DepositToFloat(store, zero, pub, 101, common.Address{}, common.Address{}, amount, msgID, bls.Sign(priv, digest), 1)
	assert.ErrorIs(t, err, ErrFloatUnknownSource)

	// (2) source key that is not a registered cluster, with its own valid certificate.
	err = DepositToFloat(store, stranger.PublicKey(), pub, 101, common.Address{}, common.Address{}, amount, msgID, bls.Sign(stranger.PrivateKey(), digest), 1)
	assert.ErrorIs(t, err, ErrFloatUnknownSource)

	// (3) registered source, certificate signed by someone else.
	err = DepositToFloat(store, pub, pub, 101, common.Address{}, common.Address{}, amount, msgID, bls.Sign(stranger.PrivateKey(), digest), 1)
	assert.ErrorIs(t, err, ErrInvalidSignature)

	bal, _ := store.GetFloat(crypto.Keccak256Hash(pub[:]))
	assert.True(t, bal == nil || bal.Sign() == 0, "nothing may be minted by any rejected deposit")

	// (4) the legitimate deposit works.
	err = DepositToFloat(store, pub, pub, 101, common.Address{}, common.Address{}, amount, msgID, bls.Sign(priv, digest), 1)
	assert.NoError(t, err)
}
