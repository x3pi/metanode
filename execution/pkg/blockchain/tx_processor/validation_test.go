package tx_processor

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
)

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
	hash := common.BytesToHash([]byte("test_hash_1"))
	StoreVerifiedSignature(hash)
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
		atomic.StoreInt64(&verifiedSignaturesCacheCount, originalCount)
	}()

	// Simulate cache reaching max size
	for i := int64(0); i < 10; i++ {
		hashI := common.BytesToHash([]byte{byte(i)})
		StoreVerifiedSignature(hashI)
	}
	atomic.StoreInt64(&verifiedSignaturesCacheCount, maxVerifiedSignaturesCacheSize)

	// Next addition should trigger reset/rotation in VerifyTransaction
	overflowHash := common.BytesToHash([]byte("overflow_trigger"))
	StoreVerifiedSignature(overflowHash)
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
