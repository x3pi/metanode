package processor

import (
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	e_common "github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
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

// ============================================================================
// MockHelpPayManager for executeAndAddTx ingress hot path tests
// ============================================================================
type MockHelpPayManagerForTest struct {
	enabled        bool
	needsTopUp     bool
	preCheckErr    error
	topUpErr       error
	preCheckCalled atomic.Int32
	topUpCalled    atomic.Int32
	releaseCalled  atomic.Int32
}

func (m *MockHelpPayManagerForTest) IsEnabled() bool                            { return m.enabled }
func (m *MockHelpPayManagerForTest) NeedsTopUp(recipient e_common.Address) bool { return m.needsTopUp }
func (m *MockHelpPayManagerForTest) CheckAndTopUp(recipient e_common.Address) error {
	return m.topUpErr
}
func (m *MockHelpPayManagerForTest) CheckAndTopUpWithClient(recipient e_common.Address, clientIP string) error {
	m.topUpCalled.Add(1)
	return m.topUpErr
}
func (m *MockHelpPayManagerForTest) ActiveWalletCount() int { return 1 }
func (m *MockHelpPayManagerForTest) PreCheckAndAcquire(
	fromAddr e_common.Address,
	txNonce uint64,
	txGas uint64,
	txAmount *big.Int,
	clientIP string,
	ingressType string,
) (func(), bool, error) {
	m.preCheckCalled.Add(1)
	if m.preCheckErr != nil {
		return nil, false, m.preCheckErr
	}
	release := func() {
		m.releaseCalled.Add(1)
	}
	return release, true, nil
}

// ============================================================================
// TestExecuteAndAddTx_Funded_BypassesHelpPay
// Tests that a funded account (NeedsTopUp == false) bypasses PreCheck and async goroutines.
// ============================================================================
func TestExecuteAndAddTx_Funded_BypassesHelpPay(t *testing.T) {
	chainID := big.NewInt(991)
	cs := createTestChainState(true, 991)
	sender := NewMockMessageSender()
	tp := newTestTransactionProcessor(cs, sender)

	mockHp := &MockHelpPayManagerForTest{
		enabled:    true,
		needsTopUp: false, // Funded
	}
	tp.helpPayManager = mockHp

	mockConn := NewMockConnection(e_common.HexToAddress("0x1234"))
	rawTx := signTestTx(t, e_types.NewTx(&e_types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(100000),
		Gas:      21000,
		To:       &e_common.Address{0x01},
		Value:    big.NewInt(100),
	}), chainID)

	tp.executeAndAddTx(injectionRequest{
		conn:    mockConn,
		rawBody: rawTx,
		rawEth:  true,
		msgID:   "funded-msg",
	})

	assert.Equal(t, int32(0), mockHp.preCheckCalled.Load(), "Funded account must NOT call PreCheckAndAcquire")
	assert.Equal(t, int32(0), mockHp.topUpCalled.Load(), "Funded account must NOT call CheckAndTopUpWithClient")
}

// ============================================================================
// TestExecuteAndAddTx_Unfunded_PreCheckRejected
// Tests that when PreCheckAndAcquire rejects an unfunded tx, error is sent and no topup runs.
// ============================================================================
func TestExecuteAndAddTx_Unfunded_PreCheckRejected(t *testing.T) {
	chainID := big.NewInt(991)
	cs := createTestChainState(true, 991)
	sender := NewMockMessageSender()
	tp := newTestTransactionProcessor(cs, sender)

	mockHp := &MockHelpPayManagerForTest{
		enabled:     true,
		needsTopUp:  true, // Unfunded
		preCheckErr: errors.New("helppay concurrency limit reached"),
	}
	tp.helpPayManager = mockHp

	mockConn := NewMockConnection(e_common.HexToAddress("0x1234"))
	rawTx := signTestTx(t, e_types.NewTx(&e_types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(100000),
		Gas:      21000,
		To:       &e_common.Address{0x01},
		Value:    big.NewInt(100),
	}), chainID)

	tp.executeAndAddTx(injectionRequest{
		conn:    mockConn,
		rawBody: rawTx,
		rawEth:  true,
		msgID:   "precheck-reject-msg",
	})

	assert.Equal(t, int32(1), mockHp.preCheckCalled.Load(), "Unfunded account must call PreCheckAndAcquire")
	assert.Equal(t, int32(0), mockHp.topUpCalled.Load(), "Rejected precheck must NOT invoke CheckAndTopUpWithClient")
	assert.Equal(t, 1, mockConn.SentCount(), "Error message must be returned to client connection")
}

// ============================================================================
// TestExecuteAndAddTx_Unfunded_PreCheckAndTopUpSuccess
// Tests that when PreCheck succeeds, topup is triggered asynchronously and slot is released.
// ============================================================================
func TestExecuteAndAddTx_Unfunded_PreCheckAndTopUpSuccess(t *testing.T) {
	chainID := big.NewInt(991)
	cs := createTestChainState(true, 991)
	sender := NewMockMessageSender()
	tp := newTestTransactionProcessor(cs, sender)

	mockHp := &MockHelpPayManagerForTest{
		enabled:    true,
		needsTopUp: true, // Unfunded
	}
	tp.helpPayManager = mockHp

	mockConn := NewMockConnection(e_common.HexToAddress("0x1234"))
	rawTx := signTestTx(t, e_types.NewTx(&e_types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(100000),
		Gas:      21000,
		To:       &e_common.Address{0x01},
		Value:    big.NewInt(100),
	}), chainID)

	tp.executeAndAddTx(injectionRequest{
		conn:    mockConn,
		rawBody: rawTx,
		rawEth:  true,
		msgID:   "topup-success-msg",
	})

	assert.Equal(t, int32(1), mockHp.preCheckCalled.Load(), "Unfunded account must call PreCheckAndAcquire")

	// Wait briefly for asynchronous goroutine
	require.Eventually(t, func() bool {
		return mockHp.topUpCalled.Load() == 1 && mockHp.releaseCalled.Load() == 1
	}, 2*time.Second, 10*time.Millisecond, "Async goroutine must call top-up and release slot")
}
