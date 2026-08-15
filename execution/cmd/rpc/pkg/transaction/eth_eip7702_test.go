package transaction

import (
	"bytes"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

func signAuthorization(t *testing.T, key *ecdsa.PrivateKey, chainID uint64, delegate common.Address, nonce uint64) types.SetCodeAuthorization {
	t.Helper()
	auth, err := types.SignSetCode(key, types.SetCodeAuthorization{
		ChainID: *uint256.NewInt(chainID),
		Address: delegate,
		Nonce:   nonce,
	})
	if err != nil {
		t.Fatalf("SignSetCode: %v", err)
	}
	return auth
}

// TestFromEthSetCodeTx_RejectsContractCreation guards the EIP-7702 rule that
// SetCode txs can never deploy a contract (the To field must be a real
// recipient).
func TestFromEthSetCodeTx_RejectsContractCreation(t *testing.T) {
	key, _ := crypto.GenerateKey()
	authKey, _ := crypto.GenerateKey()
	authAddr := crypto.PubkeyToAddress(*authKey.Public().(*ecdsa.PublicKey))
	auth := signAuthorization(t, authKey, 1337, common.HexToAddress("0x00000000000000000000000000000000009999"), 0)
	_ = authAddr

	innerTx := &types.SetCodeTx{
		ChainID:   uint256.NewInt(1337),
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       100000,
		To:        common.Address{}, // zero address: not allowed for SetCode txs
		Value:     uint256.NewInt(0),
		AuthList:  []types.SetCodeAuthorization{auth},
	}
	ethTx, err := types.SignNewTx(key, types.NewPragueSigner(big.NewInt(1337)), innerTx)
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}

	pTx := &pb.Transaction{}
	if err := FromEthSetCodeTx(ethTx, pTx); err == nil {
		t.Fatalf("FromEthSetCodeTx: expected zero-address (contract creation) SetCode tx to be rejected")
	}
}

// TestFromEthSetCodeTx_RejectsEmptyAuthList guards the EIP-7702 rule that a
// SetCode tx must carry at least one authorization tuple.
func TestFromEthSetCodeTx_RejectsEmptyAuthList(t *testing.T) {
	key, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x00000000000000000000000000000000001234")

	innerTx := &types.SetCodeTx{
		ChainID:   uint256.NewInt(1337),
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       100000,
		To:        to,
		Value:     uint256.NewInt(0),
		AuthList:  nil,
	}
	ethTx, err := types.SignNewTx(key, types.NewPragueSigner(big.NewInt(1337)), innerTx)
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}

	pTx := &pb.Transaction{}
	if err := FromEthSetCodeTx(ethTx, pTx); err == nil {
		t.Fatalf("FromEthSetCodeTx: expected empty authorization list to be rejected")
	}
}

// TestFromEthSetCodeTx_RoundTrip exercises the full FromEthSetCodeTx ->
// NewTransactionFromEth -> ToEthSetCodeTx path with a real signed SetCode tx
// carrying one authorization tuple, and checks the authorization survives the
// protobuf round trip (chainID/address/nonce/signature all byte-for-byte).
func TestFromEthSetCodeTx_RoundTrip(t *testing.T) {
	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(*key.Public().(*ecdsa.PublicKey))
	to := common.HexToAddress("0x00000000000000000000000000000000001234")

	authKey, _ := crypto.GenerateKey()
	authAddr := crypto.PubkeyToAddress(*authKey.Public().(*ecdsa.PublicKey))
	delegate := common.HexToAddress("0x00000000000000000000000000000000005678")
	auth := signAuthorization(t, authKey, 1337, delegate, 7)

	innerTx := &types.SetCodeTx{
		ChainID:   uint256.NewInt(1337),
		Nonce:     0,
		GasTipCap: uint256.NewInt(100),
		GasFeeCap: uint256.NewInt(1000),
		Gas:       100000,
		To:        to,
		Value:     uint256.NewInt(5000),
		AuthList:  []types.SetCodeAuthorization{auth},
	}
	ethTx, err := types.SignNewTx(key, types.NewPragueSigner(big.NewInt(1337)), innerTx)
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}

	txIface, err := NewTransactionFromEth(ethTx)
	if err != nil {
		t.Fatalf("NewTransactionFromEth: %v", err)
	}
	tx := txIface.(*Transaction)

	if tx.FromAddress() != from {
		t.Fatalf("FromAddress mismatch: got %s, want %s", tx.FromAddress().Hex(), from.Hex())
	}
	if tx.ToAddress() != to {
		t.Fatalf("ToAddress mismatch: got %s, want %s", tx.ToAddress().Hex(), to.Hex())
	}

	authList := tx.AuthorizationList()
	if len(authList) != 1 {
		t.Fatalf("AuthorizationList length = %d, want 1", len(authList))
	}
	if got := common.BytesToAddress(authList[0].Address); got != delegate {
		t.Fatalf("authorization delegate mismatch: got %s, want %s", got.Hex(), delegate.Hex())
	}
	if authList[0].Nonce != 7 {
		t.Fatalf("authorization nonce mismatch: got %d, want 7", authList[0].Nonce)
	}

	// The recovered authority must match the key that actually signed the
	// authorization tuple, not the tx sender.
	recovered, err := ToEthAuthorizationList(authList)[0].Authority()
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	if recovered != authAddr {
		t.Fatalf("recovered authority = %s, want %s", recovered.Hex(), authAddr.Hex())
	}

	rebuilt := tx.ToEthTransaction()
	if rebuilt == nil {
		t.Fatalf("ToEthTransaction returned nil for a valid SetCode tx")
	}
	if rebuilt.Type() != types.SetCodeTxType {
		t.Fatalf("rebuilt tx type = %d, want SetCodeTxType", rebuilt.Type())
	}
	rebuiltAuths := rebuilt.SetCodeAuthorizations()
	if len(rebuiltAuths) != 1 || rebuiltAuths[0].Address != delegate || rebuiltAuths[0].Nonce != 7 {
		t.Fatalf("rebuilt authorization mismatch: got %+v", rebuiltAuths)
	}
	rebuiltAuthority, err := rebuiltAuths[0].Authority()
	if err != nil || rebuiltAuthority != authAddr {
		t.Fatalf("rebuilt authorization authority mismatch: got %s, err=%v, want %s", rebuiltAuthority.Hex(), err, authAddr.Hex())
	}
}

// TestFromEthSetCodeTx_CallDataRoundTrip mirrors the EIP-4844 calldata-unwrap
// regression test: internally, calldata is stored as a marshaled CallData
// proto message, not raw EVM calldata — ToEthSetCodeTx must unwrap it or the
// reconstructed tx's Data (and therefore its hash and EVM execution) is wrong
// for any SetCode tx that also calls a contract (e.g. the "set delegation and
// immediately batch-call through it" pattern EIP-7702 sponsored-execution
// relies on).
func TestFromEthSetCodeTx_CallDataRoundTrip(t *testing.T) {
	key, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x00000000000000000000000000000000005678")
	authKey, _ := crypto.GenerateKey()
	auth := signAuthorization(t, authKey, 1337, common.HexToAddress("0x00000000000000000000000000000000009999"), 0)
	calldata := []byte{0xAA, 0xBB, 0xCC, 0xDD}

	innerTx := &types.SetCodeTx{
		ChainID: uint256.NewInt(1337), Nonce: 0,
		GasTipCap: uint256.NewInt(100), GasFeeCap: uint256.NewInt(1000), Gas: 100000,
		To: to, Value: uint256.NewInt(0), Data: calldata,
		AuthList: []types.SetCodeAuthorization{auth},
	}
	ethTx, err := types.SignNewTx(key, types.NewPragueSigner(big.NewInt(1337)), innerTx)
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}
	originalHash := ethTx.Hash()

	txIface, err := NewTransactionFromEth(ethTx)
	if err != nil {
		t.Fatalf("NewTransactionFromEth: %v", err)
	}
	tx := txIface.(*Transaction)

	rebuilt := tx.ToEthTransaction()
	if rebuilt == nil {
		t.Fatalf("ToEthTransaction returned nil")
	}
	if !bytes.Equal(rebuilt.Data(), calldata) {
		t.Fatalf("calldata mismatch: got %x, want %x", rebuilt.Data(), calldata)
	}
	if got := rebuilt.Hash(); got != originalHash {
		t.Fatalf("rebuilt hash = %s, want %s (calldata unwrap bug)", got.Hex(), originalHash.Hex())
	}
}
