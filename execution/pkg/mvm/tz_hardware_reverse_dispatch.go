package mvm

/*
#include "tzproto/mvm_tz_protocol.h"
*/
import "C"

import "github.com/meta-node-blockchain/meta-node/pkg/logger"

// Unexported package-level Go values of the reverse cmd IDs (plus one
// forward cmd, for a "not a reverse call" negative test) — exist ONLY so
// tz_hardware_reverse_dispatch_test.go can call dispatchReverseCall
// without its own `import "C"`: Go's cgo does not support `import "C"`
// inside a _test.go file at all ("use of cgo in test ... not
// supported"), so any C.* value a test needs must be re-exposed as a
// plain Go identifier from a non-test file like this one. Unexported
// (not *ForTest-exported) since the test file lives in this same
// package (mvm, not mvm_test) and can reach them directly.
const (
	cmdGlobalStateGetTest               = C.MVM_TZ_RCMD_GLOBAL_STATE_GET
	cmdGetStorageValueTest              = C.MVM_TZ_RCMD_GET_STORAGE_VALUE
	cmdExtensionCallGetApiTest          = C.MVM_TZ_RCMD_EXTENSION_CALL_GET_API
	cmdExtensionExtractJsonFieldTest    = C.MVM_TZ_RCMD_EXTENSION_EXTRACT_JSON_FIELD
	cmdExtensionBlstTest                = C.MVM_TZ_RCMD_EXTENSION_BLST
	cmdExtensionGetOrCreateSimpleDbTest = C.MVM_TZ_RCMD_EXTENSION_GET_OR_CREATE_SIMPLE_DB
	cmdGetLatestFullDbLogsTest          = C.MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS
	cmdCallTest                         = C.MVM_TZ_CMD_CALL // a real forward cmd, used as "not a reverse call" in tests
)

// dispatchReverseCall answers ONE reverse-call request a real TA has
// issued (TA -> Host direction) with REAL Go state — the missing piece
// GĐ2's loopback mode never needed (there, "the TA" just calls *MVMApi
// directly in the same process; see tz_loopback_engine.go's own doc
// comment). This is pure decode -> core -> encode, no channel I/O of its
// own — the hardware round-trip loop (plan §9's "Giai đoạn 3b" step 5,
// not written yet) owns reading the request off the shared page and
// writing this function's result back.
//
// cmd/header/blob are exactly what a real mvm_ta issues via
// mvm_reverse_round_trip (ta/mvm_ta_main.cpp) -- decoded with the SAME
// tz_codec.go functions GĐ1/GĐ2 already use, so this function is only
// ever as correct as those decoders/encoders are (see tz_codec.go's own
// 2026-08-20 framing-bug fix for forward requests -- that fix does NOT
// apply to the reverse-call shapes here, which were already
// length-prefixed/fixed-width correctly per mvm_tz_protocol.h).
//
// A decode error or unrecognized cmd is logged and answered with a safe
// empty/not-found response rather than panicking — the channel is
// trusted (both sides are this project's own code) but a malformed
// message here would be a real protocol bug, and crashing the whole node
// process over one bad reverse-call is worse than answering "not found"
// for that one call. (Not "fail loud" via panic/abort — reserved for
// dev-only tools like ca_test/mvm_ca_test.cpp; this runs inside a real
// node.)
func dispatchReverseCall(cmd C.mvm_tz_cmd_t, header, blob []byte) (respHeader, respBlob []byte) {
	switch cmd {
	case C.MVM_TZ_RCMD_GLOBAL_STATE_GET:
		mvmId, address, err := decodeGlobalStateGetReq(header)
		if err != nil {
			logger.Error("[TZ_HW] dispatchReverseCall: decodeGlobalStateGetReq: %v", err)
			return encodeGlobalStateGetResp(0, nil, nil, nil)
		}
		status, balance, nonce, code := globalStateGetCore(mvmId, address)
		return encodeGlobalStateGetResp(status, balance, nonce, code)

	case C.MVM_TZ_RCMD_GET_STORAGE_VALUE:
		mvmId, address, key, err := decodeGetStorageValueReq(header)
		if err != nil {
			logger.Error("[TZ_HW] dispatchReverseCall: decodeGetStorageValueReq: %v", err)
			return encodeGetStorageValueResp(int32(StorageStatusNotFound), nil)
		}
		value, status, _ := getStorageValueCore(mvmId, address, key)
		return encodeGetStorageValueResp(status, value)

	case C.MVM_TZ_RCMD_EXTENSION_CALL_GET_API:
		// Deliberately NOT calling extensionCallGetApiCore (real outbound
		// HTTP) here -- CALL_GET_API is a scope decision, not a "not yet":
		// this bridge does not and will not support live HTTP calls issued
		// from inside a TA round trip (an EVM call blocking on real network
		// I/O, under tzSessionMu, with a 60s hardware round-trip timeout
		// racing a 5s HTTP timeout, is a footgun this project chooses not
		// to carry). A contract that depends on this precompile will
		// reliably see "no data" on the hardware path -- same shape as any
		// other not-found/failure response on this channel, not a crash.
		logger.Warn("[TZ_HW] dispatchReverseCall: EXTENSION_CALL_GET_API is not supported over the hardware bridge (by design) -- returning empty")
		return nil, encodeExtensionBytesResp(nil)

	case C.MVM_TZ_RCMD_EXTENSION_EXTRACT_JSON_FIELD:
		input := decodeExtensionBytesReq(blob)
		out := extensionExtractJsonFieldCore(input)
		return nil, encodeExtensionBytesResp(out)

	case C.MVM_TZ_RCMD_EXTENSION_BLST:
		input := decodeExtensionBytesReq(blob)
		out := extensionBlstCore(input)
		return nil, encodeExtensionBytesResp(out)

	case C.MVM_TZ_RCMD_EXTENSION_GET_OR_CREATE_SIMPLE_DB:
		address, mvmId, input, err := decodeGetOrCreateSimpleDbReq(header, blob)
		if err != nil {
			logger.Error("[TZ_HW] dispatchReverseCall: decodeGetOrCreateSimpleDbReq: %v", err)
			return nil, encodeGetOrCreateSimpleDbResp(nil)
		}
		out := extensionGetOrCreateSimpleDbCore(input, address, mvmId)
		return nil, encodeGetOrCreateSimpleDbResp(out)

	case C.MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS:
		// Real address-indexed lookup (storage.GetLatestFullDbLogsForAddress,
		// see note/tee_dual_mode_execution_plan.md §5b) is NOT wired here
		// yet -- deliberately deferred, blocked on the same Xapian/GCC-ABI
		// toolchain issue documented in tz-llm-trustzone's DEPLOYED_STATE.md
		// (letting Xapian throw on the TA isn't safe yet, and this command
		// exists specifically to help the TA distinguish "found" from "not
		// found" via that mechanism). entry_count=0 is a valid, honest
		// "nothing to replay" answer per this cmd's own protocol doc
		// comment (mvm_tz_protocol.h) -- not a stub lie, just "we have
		// nothing for you right now."
		if _, err := decodeGetLatestFullDbLogsReq(header); err != nil {
			logger.Error("[TZ_HW] dispatchReverseCall: decodeGetLatestFullDbLogsReq: %v", err)
		}
		return encodeReplayFullDbLogsResp(nil)

	default:
		logger.Error("[TZ_HW] dispatchReverseCall: unrecognized cmd=%d", int(cmd))
		return nil, nil
	}
}
