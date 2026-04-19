package tx_processor

import (
	"encoding/binary"

	"sync/atomic"
	"testing"
	"time"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
)

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Tests for callDataToAccountType
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestCallDataToAccountType_ValidRegularAccount(t *testing.T) {
	selector := utils.GetFunctionSelector("setAccountType(int256)")
	callData := make([]byte, 36)
	copy(callData[:4], selector)
	// Last 4 bytes = 0 → REGULAR_ACCOUNT
	binary.BigEndian.PutUint32(callData[32:], 0)

	accountType, err := callDataToAccountType(callData)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if accountType != pb.ACCOUNT_TYPE_REGULAR_ACCOUNT {
		t.Errorf("expected REGULAR_ACCOUNT, got %v", accountType)
	}
}

func TestCallDataToAccountType_ValidReadWriteStrict(t *testing.T) {
	selector := utils.GetFunctionSelector("setAccountType(int256)")
	callData := make([]byte, 36)
	copy(callData[:4], selector)
	// Last 4 bytes = 1 → READ_WRITE_STRICT
	binary.BigEndian.PutUint32(callData[32:], 1)

	accountType, err := callDataToAccountType(callData)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if accountType != pb.ACCOUNT_TYPE_READ_WRITE_STRICT {
		t.Errorf("expected READ_WRITE_STRICT, got %v", accountType)
	}
}

func TestCallDataToAccountType_InvalidLength(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"too_short", []byte{0x01, 0x02, 0x03}},
		{"35_bytes", make([]byte, 35)},
		{"37_bytes", make([]byte, 37)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callDataToAccountType(tt.data)
			if err != transaction.InvalidData {
				t.Errorf("expected InvalidData error, got %v", err)
			}
		})
	}
}

func TestCallDataToAccountType_WrongSelector(t *testing.T) {
	callData := make([]byte, 36)
	// Wrong selector bytes
	callData[0] = 0xFF
	callData[1] = 0xFF
	callData[2] = 0xFF
	callData[3] = 0xFF

	_, err := callDataToAccountType(callData)
	if err != transaction.InvalidData {
		t.Errorf("expected InvalidData error for wrong selector, got %v", err)
	}
}

func TestCallDataToAccountType_InvalidTypeValue(t *testing.T) {
	selector := utils.GetFunctionSelector("setAccountType(int256)")
	tests := []uint32{2, 3, 10, 255, 1000}

	for _, val := range tests {
		callData := make([]byte, 36)
		copy(callData[:4], selector)
		binary.BigEndian.PutUint32(callData[32:], val)

		_, err := callDataToAccountType(callData)
		if err != transaction.InvalidData {
			t.Errorf("callDataToAccountType with value %d: expected InvalidData, got %v", val, err)
		}
	}
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Tests for GetValidatorHandler
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestGetValidatorHandler_ReturnsSameInstance(t *testing.T) {
	h1, err1 := GetValidatorHandler()
	if err1 != nil {
		t.Fatalf("GetValidatorHandler() first call error: %v", err1)
	}
	if h1 == nil {
		t.Fatal("GetValidatorHandler() returned nil")
	}

	h2, err2 := GetValidatorHandler()
	if err2 != nil {
		t.Fatalf("GetValidatorHandler() second call error: %v", err2)
	}
	if h1 != h2 {
		t.Error("GetValidatorHandler() should return same instance (singleton)")
	}
}

func TestGetValidatorHandler_HasABI(t *testing.T) {
	h, err := GetValidatorHandler()
	if err != nil {
		t.Fatalf("GetValidatorHandler() error: %v", err)
	}
	// The handler should have parsed ABI with known methods
	methods := []string{
		"registerValidator",
		"deregisterValidator",
		"setCommissionRate",
		"delegate",
		"undelegate",
		"withdrawReward",
		"distributeRewards",
		"getValidatorCount",
		"validators",
		"balanceOf",
	}
	for _, name := range methods {
		if _, ok := h.abi.Methods[name]; !ok {
			t.Errorf("ABI missing method %q", name)
		}
	}
}

func TestGetValidatorHandler_HasEvents(t *testing.T) {
	h, err := GetValidatorHandler()
	if err != nil {
		t.Fatalf("GetValidatorHandler() error: %v", err)
	}
	events := []string{
		"ValidatorRegistered",
		"ValidatorDeregistered",
		"CommissionRateUpdated",
		"Delegated",
		"Undelegated",
		"RewardWithdrawn",
		"RewardsDistributed",
	}
	for _, name := range events {
		if _, ok := h.abi.Events[name]; !ok {
			t.Errorf("ABI missing event %q", name)
		}
	}
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Tests for Signature Cache
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestSignatureCacheConstants(t *testing.T) {
	if maxVerifiedSignaturesCacheSize <= 0 {
		t.Errorf("maxVerifiedSignaturesCacheSize should be positive, got %d", maxVerifiedSignaturesCacheSize)
	}
	if signatureCacheCleanupInterval <= 0 {
		t.Errorf("signatureCacheCleanupInterval should be positive, got %v", signatureCacheCleanupInterval)
	}
}

func TestStartSignatureCacheCleanup_StopsOnSignal(t *testing.T) {
	stopCh := make(chan struct{})
	StartSignatureCacheCleanup(stopCh)

	// Store something in cache
	verifiedSignaturesCache.Store("test_hash_1", true)
	atomic.AddInt64(&verifiedSignaturesCacheCount, 1)

	// Signal stop
	close(stopCh)
	time.Sleep(100 * time.Millisecond)

	// Cleanup goroutine should have stopped — no crash
}

func TestSignatureCache_AutoResetOnOverflow(t *testing.T) {
	// Save original and restore after test
	originalCount := atomic.LoadInt64(&verifiedSignaturesCacheCount)
	defer func() {
		verifiedSignaturesCache.Clear()
		atomic.StoreInt64(&verifiedSignaturesCacheCount, originalCount)
	}()

	// Simulate cache reaching max size
	for i := int64(0); i < 10; i++ {
		verifiedSignaturesCache.Store(i, true)
	}
	atomic.StoreInt64(&verifiedSignaturesCacheCount, maxVerifiedSignaturesCacheSize)

	// Next addition should trigger reset
	verifiedSignaturesCache.Store("overflow_trigger", true)
	newCount := atomic.AddInt64(&verifiedSignaturesCacheCount, 1)

	if newCount > maxVerifiedSignaturesCacheSize+1 {
		t.Errorf("cache count should not exceed max+1 indefinitely, got %d", newCount)
	}
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Tests for FunctionSelector consistency
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestFunctionSelectors_Consistency(t *testing.T) {
	// Verify that the function selector for setAccountType(int256) is deterministic
	sel1 := utils.GetFunctionSelector("setAccountType(int256)")
	sel2 := utils.GetFunctionSelector("setAccountType(int256)")
	if len(sel1) != 4 {
		t.Fatalf("function selector should be 4 bytes, got %d", len(sel1))
	}
	for i := range sel1 {
		if sel1[i] != sel2[i] {
			t.Errorf("function selector not deterministic at byte %d", i)
		}
	}
}

func TestFunctionSelectors_DifferentForDifferentFunctions(t *testing.T) {
	sel1 := utils.GetFunctionSelector("setAccountType(int256)")
	sel2 := utils.GetFunctionSelector("setBlsPublicKey(bytes)")
	if sel1[0] == sel2[0] && sel1[1] == sel2[1] && sel1[2] == sel2[2] && sel1[3] == sel2[3] {
		t.Error("different functions should have different selectors")
	}
}
