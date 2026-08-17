package mvm

// Round-trip tests for the MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS codec added
// in note/tee_dual_mode_execution_plan.md §5b (2026-08-16). Unlike the 6
// live forward commands (see ta_boundary_harness_test.go's cgo-vs-loopback
// comparisons), this reverse command has no real dispatch handler yet — its
// "TA side" only exists on real hardware (GĐ3) — so these are plain
// encode/decode round-trip tests, not an engine-level comparison. Package
// mvm (not mvm_test) so the unexported codec functions are reachable
// directly, same as tz_codec.go's own conventions.

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestGetLatestFullDbLogsReq_RoundTrip(t *testing.T) {
	want := common.HexToAddress("0xabababababababababababababababababababab")

	header := encodeGetLatestFullDbLogsReq(want)
	got, err := decodeGetLatestFullDbLogsReq(header)
	if err != nil {
		t.Fatalf("decodeGetLatestFullDbLogsReq: %v", err)
	}
	if got != want {
		t.Errorf("address round-trip mismatch: got %s, want %s", got.Hex(), want.Hex())
	}
}

func TestGetLatestFullDbLogsReq_RejectsWrongHeaderLength(t *testing.T) {
	if _, err := decodeGetLatestFullDbLogsReq([]byte{0x01, 0x02, 0x03}); err == nil {
		t.Fatalf("decodeGetLatestFullDbLogsReq: expected error for truncated header, got nil")
	}
}

func TestReplayFullDbLogsResp_RoundTrip_Empty(t *testing.T) {
	header, blob := encodeReplayFullDbLogsResp(nil)
	got, err := decodeReplayFullDbLogsResp(header, blob)
	if err != nil {
		t.Fatalf("decodeReplayFullDbLogsResp: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 entries for a miss (nothing saved for this address), got %d", len(got))
	}
}

func TestReplayFullDbLogsResp_RoundTrip_OneEntry(t *testing.T) {
	// The lazy single-address pull this command exists for (plan §5b) only
	// ever carries 0 or 1 entries in practice, but the wire shape is
	// generic (also doubles as MVM_TZ_CMD_REPLAY_FULL_DB_LOGS's N-entry
	// forward payload) — exercise 2 entries here to prove the shared shape
	// genuinely round-trips more than the single-address case needs.
	addr1 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	addr2 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	want := map[string][]byte{
		addr1: []byte("serialized XapianLog::ComprehensiveLog bytes for addr1"),
		addr2: []byte("serialized XapianLog::ComprehensiveLog bytes for addr2"),
	}

	header, blob := encodeReplayFullDbLogsResp(want)
	got, err := decodeReplayFullDbLogsResp(header, blob)
	if err != nil {
		t.Fatalf("decodeReplayFullDbLogsResp: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("entry count mismatch: got %d, want %d", len(got), len(want))
	}
	for addr, wantBytes := range want {
		gotBytes, ok := got[addr]
		if !ok {
			gotKeys := make([]string, 0, len(got))
			for k := range got {
				gotKeys = append(gotKeys, k)
			}
			t.Fatalf("missing entry for address %s; got keys %v", addr, gotKeys)
		}
		if !bytes.Equal(gotBytes, wantBytes) {
			t.Errorf("entry[%s] = %q, want %q", addr, gotBytes, wantBytes)
		}
	}
}

func TestReplayFullDbLogsResp_RejectsWrongHeaderLength(t *testing.T) {
	if _, err := decodeReplayFullDbLogsResp([]byte{0x01}, nil); err == nil {
		t.Fatalf("decodeReplayFullDbLogsResp: expected error for truncated header, got nil")
	}
}
