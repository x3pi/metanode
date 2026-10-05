package processor

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"github.com/ethereum/go-ethereum/crypto"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
	"os"
	"sync"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TestTransactionManagerSyncMap_LoadOrStore
// ============================================================================
func TestTransactionManagerSyncMap_LoadOrStore(t *testing.T) {
	tm := NewTransactionManagerSyncMap()

	key := e_common.HexToHash("0x1111")
	value := "test-value"

	// Store a new key
	_, loaded := tm.pending.LoadOrStore(key, value)
	assert.False(t, loaded, "first store should not find existing value")

	// Load existing key
	got, loaded := tm.pending.LoadOrStore(key, "other-value")
	assert.True(t, loaded, "second store should find existing value")
	assert.Equal(t, value, got.(string), "should return original value")
}

// ============================================================================
// TestTransactionManagerSyncMap_Delete
// ============================================================================
func TestTransactionManagerSyncMap_Delete(t *testing.T) {
	tm := NewTransactionManagerSyncMap()

	key := e_common.HexToHash("0x2222")
	tm.pending.Store(key, "data")

	// Verify stored
	_, exists := tm.pending.Load(key)
	require.True(t, exists)

	// Delete
	tm.pending.Delete(key)

	// Verify deleted
	_, exists = tm.pending.Load(key)
	assert.False(t, exists, "key should be gone after delete")
}

// ============================================================================
// TestTransactionManagerSyncMap_Concurrent
// ============================================================================
func TestTransactionManagerSyncMap_Concurrent(t *testing.T) {
	tm := NewTransactionManagerSyncMap()

	const goroutines = 50
	const opsPerRoutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gID int) {
			defer wg.Done()
			for i := 0; i < opsPerRoutine; i++ {
				key := e_common.BigToHash(e_common.Big1)
				tm.pending.Store(key, gID)
				tm.pending.Load(key)
				tm.pending.Delete(key)
			}
		}(g)
	}

	wg.Wait()
	// No race / no panic = pass
}

// ============================================================================
// TestAddTransactionToPool_NilTx
// Tests the nil-check guard at the top of AddTransactionToPool.
// ============================================================================
func TestAddTransactionToPool_NilTx(t *testing.T) {
	tp := &TransactionProcessor{}

	code, err := tp.AddTransactionToPool(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
	assert.Equal(t, transaction.InvalidTransaction.Code, code,
		"nil tx should return InvalidTransaction error code")
}

// ============================================================================
// TestSendTransactionError_WithMockSender
// Verifies that sendTransactionError sends an error message through MessageSender.
// ============================================================================
func TestSendTransactionError_WithMockSender(t *testing.T) {
	sender := &MockMessageSender{}
	tp := &TransactionProcessor{
		TxVirtualExecutor: &TxVirtualExecutor{
			messageSender: sender,
		},
	}

	txHash := e_common.HexToHash("0xdead")
	conn := NewMockConnection(e_common.HexToAddress("0x1111111111111111111111111111111111111111"))

	tp.sendTransactionError(conn, txHash, -1, "test error", nil, "")

	assert.Equal(t, 1, conn.SentCount(), "should have sent exactly 1 error message")
}

// ============================================================================
// TestSendTransactionError_NilSender
// Verifies no panic when messageSender is nil.
// ============================================================================
func TestSendTransactionError_NilSender(t *testing.T) {
	tp := &TransactionProcessor{
		TxVirtualExecutor: &TxVirtualExecutor{
			messageSender: nil,
		},
	}

	txHash := e_common.HexToHash("0xdead")
	conn := NewMockConnection(e_common.HexToAddress("0x1111111111111111111111111111111111111111"))

	// Should not panic
	assert.NotPanics(t, func() {
		tp.sendTransactionError(conn, txHash, -1, "test error", nil, "")
	})
}

// ============================================================================
// TestSendTransactionError_MultipleCalls
// Verifies sent count accumulates correctly.
// ============================================================================
func TestSendTransactionError_MultipleCalls(t *testing.T) {
	sender := &MockMessageSender{}
	tp := &TransactionProcessor{
		TxVirtualExecutor: &TxVirtualExecutor{
			messageSender: sender,
		},
	}

	conn := NewMockConnection(e_common.HexToAddress("0x1111111111111111111111111111111111111111"))

	for i := 0; i < 5; i++ {
		tp.sendTransactionError(conn, e_common.HexToHash("0xdead"), -1, "err", nil, "")
	}

	assert.Equal(t, 5, conn.SentCount(), "should have sent 5 error messages")
}

// TestDeviceKeyHandlerMissingTransaction rejects malformed wrappers before hashing.
func TestDeviceKeyHandlerMissingTransaction(t *testing.T) {
	tp := &TransactionProcessor{injectionQueue: make(chan injectionRequest, 1)}
	conn := NewMockConnection(e_common.Address{})
	for _, body := range [][]byte{nil, {0xff}, {0x12, 0x01, 0x01}} {
		err := tp.ProcessTransactionFromClientWithDeviceKey(NewMockRequest(conn, NewMockMessage("SendTransactionWithDeviceKey", body)))
		require.Error(t, err)
		require.Empty(t, tp.injectionQueue)
	}
}

// TestTCPSetCodeSuiteFixture checks the actual suite encoder against node types.
func TestTCPSetCodeSuiteFixture(t *testing.T) {
	path := os.Getenv("TCP_SETCODE_FIXTURE")
	if path == "" {
		t.Skip("set TCP_SETCODE_FIXTURE to the suite's offline fixture")
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var f map[string]string
	require.NoError(t, json.Unmarshal(data, &f))
	wire, err := hex.DecodeString(f["wire"])
	require.NoError(t, err)
	publicKey, err := hex.DecodeString(f["bls_public_key"])
	require.NoError(t, err)
	var wrapper pb.TransactionWithDeviceKey
	require.NoError(t, proto.Unmarshal(wire, &wrapper))
	require.NotNil(t, wrapper.Transaction)
	tp := &TransactionProcessor{injectionQueue: make(chan injectionRequest, 1)}
	conn := NewMockConnection(e_common.Address{})
	require.NoError(t, tp.ProcessTransactionFromClientWithDeviceKey(NewMockRequest(conn, NewMockMessage("SendTransactionWithDeviceKey", wire))))
	require.Len(t, tp.injectionQueue, 1)
	tx := (<-tp.injectionQueue).tx
	require.NotNil(t, tx)
	require.Equal(t, uint64(4), tx.GetType())
	require.Equal(t, e_common.HexToHash(f["hash"]), tx.Hash())
	require.True(t, tx.ValidSign(cm.PubkeyFromBytes(publicKey)))
	require.True(t, tx.ValidEthSign())
	require.Equal(t, e_common.HexToHash(f["eth_hash"]), tx.ToEthTransaction().Hash())
	require.Equal(t, crypto.Keccak256Hash(wrapper.DeviceKey), tx.NewDeviceKey())
	require.Len(t, tx.AuthorizationList(), 1)
	authority, err := transaction.ToEthAuthorizationList(tx.AuthorizationList())[0].Authority()
	require.NoError(t, err)
	require.Equal(t, e_common.HexToAddress(f["authority"]), authority)
	// Native signature must bind both the device chain and authorization tuple.
	for _, mutate := range []func(*pb.Transaction){
		func(p *pb.Transaction) { p.LastDeviceKey = bytes.Repeat([]byte{9}, 32) },
		func(p *pb.Transaction) { p.NewDeviceKey = bytes.Repeat([]byte{9}, 32) },
		func(p *pb.Transaction) { p.AuthorizationList[0].Nonce++ },
		func(p *pb.Transaction) { p.AuthorizationList[0].Address = bytes.Repeat([]byte{9}, 20) },
		func(p *pb.Transaction) { p.Sign[0] ^= 1 },
	} {
		changed := proto.Clone(wrapper.Transaction).(*pb.Transaction)
		mutate(changed)
		altered := &transaction.Transaction{}
		altered.FromProto(changed)
		require.False(t, altered.ValidSign(cm.PubkeyFromBytes(publicKey)))
	}
}

// TestProcessTransactionFromClient_SecpProtoType0xFF tests client TCP ingestion
// of a Type 0xFF (Secp256k1 Proto TCP) transaction via SendTransaction.
func TestProcessTransactionFromClient_SecpProtoType0xFF(t *testing.T) {
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(privKey.PublicKey)
	to := e_common.HexToAddress("0xCAFE00000000000000000000000000000000CAFE")

	tx := &transaction.Transaction{}
	tx.FromProto(&pb.Transaction{
		FromAddress: from.Bytes(),
		ToAddress:   to.Bytes(),
		Amount:      []byte{0x01},
		MaxGas:      21000,
		MaxGasPrice: 1000,
		MaxTimeUse:  1000,
		ChainID:     991,
		Type:        0xFF,
	})
	tx.SetNonce(0)
	require.NoError(t, tx.SignSecpProto(privKey))

	wireBytes, err := tx.Marshal()
	require.NoError(t, err)

	tp := &TransactionProcessor{
		injectionQueue: make(chan injectionRequest, 10),
	}
	conn := NewMockConnection(from)

	req := NewMockRequest(conn, NewMockMessage("SendTransaction", wireBytes))
	err = tp.ProcessTransactionFromClient(req)
	require.NoError(t, err)

	require.Len(t, tp.injectionQueue, 1)
	injected := <-tp.injectionQueue
	assert.Equal(t, wireBytes, injected.rawBody)

	// Execute deferred unmarshal and check transaction structure
	unmarshaledTx := &transaction.Transaction{}
	require.NoError(t, unmarshaledTx.Unmarshal(injected.rawBody))
	assert.Equal(t, uint64(0xFF), unmarshaledTx.Type())
	assert.Equal(t, tx.Hash(), unmarshaledTx.Hash())
	assert.Equal(t, tx.SigningHash(), unmarshaledTx.SigningHash())
	assert.True(t, unmarshaledTx.ValidSecpProtoSign())
	assert.True(t, unmarshaledTx.ValidSecpSign())
	assert.Equal(t, from, unmarshaledTx.FromAddress())
}

