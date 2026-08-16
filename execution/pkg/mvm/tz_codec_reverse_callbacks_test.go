package mvm

// Round-trip tests for the 6 live reverse-callback codecs added in
// tz_codec.go (note/tee_dual_mode_execution_plan.md §2.1 / GĐ3,
// 2026-08-16). Same status as tz_codec_full_db_logs_test.go: these
// commands have no real TA-side dispatch handler yet (GĐ2's loopback
// engine calls the real *MVMApi directly, bypassing the wire for reverse
// calls — see tz_loopback_engine.go's doc comment) — plain encode/decode
// round-trip tests, not an engine-level comparison. Package mvm (not
// mvm_test) so the unexported codec functions are reachable directly.

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestGlobalStateGetReq_RoundTrip(t *testing.T) {
	wantMvmId := common.HexToAddress("0x1111111111111111111111111111111111111111")
	wantAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")

	header := encodeGlobalStateGetReq(wantMvmId, wantAddr)
	gotMvmId, gotAddr, err := decodeGlobalStateGetReq(header)
	if err != nil {
		t.Fatalf("decodeGlobalStateGetReq: %v", err)
	}
	if gotMvmId != wantMvmId || gotAddr != wantAddr {
		t.Errorf("got mvmId=%s address=%s, want mvmId=%s address=%s",
			gotMvmId.Hex(), gotAddr.Hex(), wantMvmId.Hex(), wantAddr.Hex())
	}
}

func TestGlobalStateGetReq_RejectsWrongHeaderLength(t *testing.T) {
	if _, _, err := decodeGlobalStateGetReq([]byte{0x01}); err == nil {
		t.Fatalf("decodeGlobalStateGetReq: expected error for truncated header, got nil")
	}
}

func TestGlobalStateGetResp_RoundTrip_Found(t *testing.T) {
	balance := bytes.Repeat([]byte{0xAA}, 32)
	nonce := bytes.Repeat([]byte{0xBB}, 32)
	code := []byte{0x60, 0x00, 0x60, 0x00, 0xF3}

	header, blob := encodeGlobalStateGetResp(1, balance, nonce, code)
	status, gotBalance, gotNonce, gotCode, err := decodeGlobalStateGetResp(header, blob)
	if err != nil {
		t.Fatalf("decodeGlobalStateGetResp: %v", err)
	}
	if status != 1 {
		t.Errorf("status = %d, want 1", status)
	}
	if !bytes.Equal(gotBalance, balance) {
		t.Errorf("balance = %x, want %x", gotBalance, balance)
	}
	if !bytes.Equal(gotNonce, nonce) {
		t.Errorf("nonce = %x, want %x", gotNonce, nonce)
	}
	if !bytes.Equal(gotCode, code) {
		t.Errorf("code = %x, want %x", gotCode, code)
	}
}

func TestGlobalStateGetResp_RoundTrip_NotFoundAndSuspend(t *testing.T) {
	for _, status := range []int32{0, 3} {
		header, blob := encodeGlobalStateGetResp(status, nil, nil, nil)
		gotStatus, balance, nonce, code, err := decodeGlobalStateGetResp(header, blob)
		if err != nil {
			t.Fatalf("status=%d: decodeGlobalStateGetResp: %v", status, err)
		}
		if gotStatus != status {
			t.Errorf("status = %d, want %d", gotStatus, status)
		}
		if balance != nil || nonce != nil || code != nil {
			t.Errorf("status=%d: expected nil balance/nonce/code, got %x/%x/%x", status, balance, nonce, code)
		}
	}
}

func TestGetStorageValueReq_RoundTrip(t *testing.T) {
	wantMvmId := common.HexToAddress("0x3333333333333333333333333333333333333333")
	wantAddr := common.HexToAddress("0x4444444444444444444444444444444444444444")
	wantKey := bytes.Repeat([]byte{0xCC}, 32)

	header := encodeGetStorageValueReq(wantMvmId, wantAddr, wantKey)
	gotMvmId, gotAddr, gotKey, err := decodeGetStorageValueReq(header)
	if err != nil {
		t.Fatalf("decodeGetStorageValueReq: %v", err)
	}
	if gotMvmId != wantMvmId || gotAddr != wantAddr {
		t.Errorf("got mvmId=%s address=%s, want mvmId=%s address=%s",
			gotMvmId.Hex(), gotAddr.Hex(), wantMvmId.Hex(), wantAddr.Hex())
	}
	if !bytes.Equal(gotKey, wantKey) {
		t.Errorf("key = %x, want %x", gotKey, wantKey)
	}
}

func TestGetStorageValueResp_RoundTrip_Success(t *testing.T) {
	value := bytes.Repeat([]byte{0xDD}, 32)

	header, blob := encodeGetStorageValueResp(0, value)
	status, gotValue, err := decodeGetStorageValueResp(header, blob)
	if err != nil {
		t.Fatalf("decodeGetStorageValueResp: %v", err)
	}
	if status != 0 {
		t.Errorf("status = %d, want 0", status)
	}
	if !bytes.Equal(gotValue, value) {
		t.Errorf("value = %x, want %x", gotValue, value)
	}
}

func TestGetStorageValueResp_RoundTrip_NotFoundAndSuspend(t *testing.T) {
	for _, status := range []int32{1, 2} {
		header, blob := encodeGetStorageValueResp(status, nil)
		gotStatus, value, err := decodeGetStorageValueResp(header, blob)
		if err != nil {
			t.Fatalf("status=%d: decodeGetStorageValueResp: %v", status, err)
		}
		if gotStatus != status {
			t.Errorf("status = %d, want %d", gotStatus, status)
		}
		if value != nil {
			t.Errorf("status=%d: expected nil value, got %x", status, value)
		}
	}
}

func TestExtensionBytes_RoundTrip(t *testing.T) {
	// Shared shape for ExtensionCallGetApi/ExtensionExtractJsonField/
	// ExtensionBlst — exercised once, generically.
	input := []byte("some ABI-encoded call data")
	reqBlob := encodeExtensionBytesReq(input)
	if !bytes.Equal(decodeExtensionBytesReq(reqBlob), input) {
		t.Errorf("request round-trip: got %x, want %x", decodeExtensionBytesReq(reqBlob), input)
	}

	output := []byte("some ABI-encoded result")
	respBlob := encodeExtensionBytesResp(output)
	if !bytes.Equal(decodeExtensionBytesResp(respBlob), output) {
		t.Errorf("response round-trip: got %x, want %x", decodeExtensionBytesResp(respBlob), output)
	}
}

func TestExtensionBytes_EmptyIsFailureCase(t *testing.T) {
	respBlob := encodeExtensionBytesResp(nil)
	if len(decodeExtensionBytesResp(respBlob)) != 0 {
		t.Errorf("expected empty response blob to decode as empty (Extension_return{nullptr,0} failure case)")
	}
}

func TestGetOrCreateSimpleDbReq_RoundTrip(t *testing.T) {
	wantAddr := common.HexToAddress("0x5555555555555555555555555555555555555555")
	wantMvmId := common.HexToAddress("0x6666666666666666666666666666666666666666")
	wantInput := []byte("ABI-encoded getOrCreateSimpleDb dispatch + args")

	header, blob := encodeGetOrCreateSimpleDbReq(wantAddr, wantMvmId, wantInput)
	gotAddr, gotMvmId, gotInput, err := decodeGetOrCreateSimpleDbReq(header, blob)
	if err != nil {
		t.Fatalf("decodeGetOrCreateSimpleDbReq: %v", err)
	}
	if gotAddr != wantAddr || gotMvmId != wantMvmId {
		t.Errorf("got address=%s mvmId=%s, want address=%s mvmId=%s",
			gotAddr.Hex(), gotMvmId.Hex(), wantAddr.Hex(), wantMvmId.Hex())
	}
	if !bytes.Equal(gotInput, wantInput) {
		t.Errorf("input = %x, want %x", gotInput, wantInput)
	}
}

func TestGetOrCreateSimpleDbReq_RejectsWrongHeaderLength(t *testing.T) {
	if _, _, _, err := decodeGetOrCreateSimpleDbReq([]byte{0x01, 0x02}, nil); err == nil {
		t.Fatalf("decodeGetOrCreateSimpleDbReq: expected error for truncated header, got nil")
	}
}

func TestGetOrCreateSimpleDbResp_RoundTrip(t *testing.T) {
	output := []byte("ABI-encoded getOrCreateSimpleDb result")
	blob := encodeGetOrCreateSimpleDbResp(output)
	if !bytes.Equal(decodeGetOrCreateSimpleDbResp(blob), output) {
		t.Errorf("round-trip: got %x, want %x", decodeGetOrCreateSimpleDbResp(blob), output)
	}
}
