package tee_revm_ffi

import (
	"testing"
)

func TestExecuteTx(t *testing.T) {
	tee := NewTeeRevm()

	caller := make([]byte, 20)
	target := make([]byte, 20)
	calldata := []byte{0x01, 0x02, 0x03}
	gasLimit := uint64(21000)

	caller[19] = 1
	target[19] = 2

	diff, err := tee.ExecuteTx(caller, target, calldata, gasLimit)
	if err != nil {
		t.Fatalf("Failed to execute tx: %v", err)
	}

	if diff == nil {
		t.Fatalf("Expected non-nil State Diff")
	}

	if !diff.Success {
		t.Fatalf("Expected Success = true")
	}

	if len(diff.ReadKeys) != 1 {
		t.Fatalf("Expected 1 ReadKey, got %d", len(diff.ReadKeys))
	}

	if diff.ReadKeys[0][31] != 1 {
		t.Fatalf("Expected ReadKey[31] to be 1, got %d", diff.ReadKeys[0][31])
	}

	if len(diff.WriteKeys) != 1 {
		t.Fatalf("Expected 1 WriteKey, got %d", len(diff.WriteKeys))
	}

	if diff.WriteKeys[0][31] != 2 {
		t.Fatalf("Expected WriteKey[31] to be 2, got %d", diff.WriteKeys[0][31])
	}

	t.Logf("Successfully parsed State Diff!")
	t.Logf("Gas Used: %d", diff.GasUsed)
	t.Logf("Read Keys: %x", diff.ReadKeys[0])
	t.Logf("Write Keys: %x", diff.WriteKeys[0])
}
