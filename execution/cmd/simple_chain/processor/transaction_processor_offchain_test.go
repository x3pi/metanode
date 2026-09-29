package processor

import "testing"

func TestEVMTimestampSeconds(t *testing.T) {
	const headerTimestampMs uint64 = 1_790_678_281_000
	const wantTimestampSeconds uint64 = 1_790_678_281

	if got := evmTimestampSeconds(headerTimestampMs); got != wantTimestampSeconds {
		t.Fatalf("EVM timestamp = %d, want %d", got, wantTimestampSeconds)
	}
}
