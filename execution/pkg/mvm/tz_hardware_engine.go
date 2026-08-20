package mvm

/*
#include "tzproto/mvm_tz_protocol.h"
*/
import "C"
import (
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// tzHardwareEngine implements ExecutionEngine for real TrustZone
// hardware (note/tee_dual_mode_execution_plan.md's "Giai đoạn 3b", steps
// 5-6) — same wire codec and forward-command shape as tzLoopbackEngine
// (tz_loopback_engine.go), but the "TA side" is a REAL separate mvm_ta
// process on the board, reached over tzHardwareChannel
// (tz_hardware_channel.go) instead of a synchronous same-process call.
//
// Wraps a real *MVMApi (via GetOrCreateMVMApi) for the SAME reason
// tzLoopbackEngine does: not because computation happens here (it
// doesn't — the actual EVM interpreter runs inside mvm_ta, secure
// world), but because dispatchReverseCall's cores (globalStateGetCore,
// getStorageValueCore, the 4 extension cores) all resolve their
// SmartContractDB/AccountStateDB by looking up this SAME mvmId in the
// GetMVMApi registry — so a real *MVMApi must already be registered for
// mvmId before the TA can ask it any reverse-call question. The local-
// bookkeeping methods below (GetKey/SetRelatedAddresses/etc.) delegate
// to it for the identical reason tzLoopbackEngine's own do (see that
// type's doc comment): none of these cross the wire in protocol v1.
type tzHardwareEngine struct {
	real *MVMApi
}

func newTZHardwareEngine(
	key common.Address,
	smartContractDb SmartContractDB,
	accountStateDb AccountStateDB,
	extendedMode bool,
) *tzHardwareEngine {
	return &tzHardwareEngine{
		real: GetOrCreateMVMApi(key, smartContractDb, accountStateDb, extendedMode),
	}
}

var _ ExecutionEngine = (*tzHardwareEngine)(nil)

// ─────────────────── local bookkeeping: no wire protocol ───────────────────
// Identical rationale to tzLoopbackEngine's own (tz_loopback_engine.go).

func (e *tzHardwareEngine) GetKey() common.Address { return e.real.GetKey() }
func (e *tzHardwareEngine) SetSmartContractDb(smartContractDb SmartContractDB) {
	e.real.SetSmartContractDb(smartContractDb)
}
func (e *tzHardwareEngine) SmartContractDatas() SmartContractDB { return e.real.SmartContractDatas() }
func (e *tzHardwareEngine) SetAccountStateDb(accountStateDb AccountStateDB) {
	e.real.SetAccountStateDb(accountStateDb)
}
func (e *tzHardwareEngine) AccountStateDb() AccountStateDB { return e.real.AccountStateDb() }
func (e *tzHardwareEngine) SetRelatedAddresses(addresses []common.Address) {
	e.real.SetRelatedAddresses(addresses)
}
func (e *tzHardwareEngine) GetCurrentRelatedAddresses() []common.Address {
	return e.real.GetCurrentRelatedAddresses()
}
func (e *tzHardwareEngine) InRelatedAddress(address common.Address) bool {
	return e.real.InRelatedAddress(address)
}
func (e *tzHardwareEngine) AddRelatedAddress(address common.Address) {
	e.real.AddRelatedAddress(address)
}
func (e *tzHardwareEngine) SetBlobContext(blobVersionedHashes [][]byte, blobBaseFee *uint256.Int) {
	e.real.SetBlobContext(blobVersionedHashes, blobBaseFee)
}
func (e *tzHardwareEngine) SetCrossChainContext(sender common.Address, sourceChainId uint64) {
	e.real.SetCrossChainContext(sender, sourceChainId)
}
func (e *tzHardwareEngine) ClearCrossChainContext() { e.real.ClearCrossChainContext() }
func (e *tzHardwareEngine) GetExecuteResult() *MVMExecuteResult {
	return e.real.GetExecuteResult()
}

// ─────────────────────── generic round-trip plumbing ───────────────────────

// tzHardwareRoundTripTimeout matches ta/ca_test/mvm_ca_test.cpp's own
// TIMEOUT_S (60s) — long enough for a real world-switch + EVM execution,
// short enough that a genuinely stuck TA (see CLAUDE.md: a GGML_ASSERT-
// style internal abort spins forever instead of crashing cleanly) is
// eventually reported instead of hanging the caller silently forever.
const tzHardwareRoundTripTimeout = 60 * time.Second

// tzHardwareRoundTrip drives one full request/response cycle against
// REAL hardware: write the request as HOST_TO_TA, flip request_ready,
// then poll -- servicing any number of reverse calls the TA issues in
// the meantime via dispatchReverseCall (tz_hardware_reverse_dispatch.go)
// -- until the final HOST_TO_TA-tagged response_ready arrives. This is
// the one piece with NO loopback analogue: tzLoopbackRoundTrip's
// "process" callback runs synchronously in the same goroutine because
// there is no real second party; here there genuinely is one, so the
// wait must handle it arriving asynchronously and possibly multiple
// times before the real answer. Mirrors the proven wait loop at
// ta/ca_test/mvm_ca_test.cpp:882-931 (including its direction-guarded
// flag consumption, tzHardwareChannel's *ForDirection methods) — not a
// re-derivation of it.
func tzHardwareRoundTrip(cmd C.mvm_tz_cmd_t, reqHeader, reqBlob []byte) (respHeader, respBlob []byte, err error) {
	ch, chErr := getTZHardwareChannel()
	if chErr != nil {
		return nil, nil, fmt.Errorf("mvm: tzHardware: channel: %w", chErr)
	}

	if werr := ch.writeMessage(cmd, C.MVM_TZ_DIR_HOST_TO_TA, reqHeader, reqBlob); werr != nil {
		return nil, nil, fmt.Errorf("mvm: tzHardware: write request (cmd=%d): %w", cmd, werr)
	}
	ch.postRequestReady()

	deadline := time.Now().Add(tzHardwareRoundTripTimeout)
	for {
		if ch.consumeResponseReadyForDirection(C.MVM_TZ_DIR_HOST_TO_TA) {
			_, _, respHeader, respBlob = ch.readMessage()
			return respHeader, respBlob, nil
		}
		if ch.consumeRequestReadyForDirection(C.MVM_TZ_DIR_TA_TO_HOST) {
			rcmd, rdir, rHeader, rBlob := ch.readMessage()
			outHeader, outBlob := dispatchReverseCall(rcmd, rHeader, rBlob)
			// Reuse the SAME cmd/direction the TA's own request already
			// carried (matches mvm_ca_test.cpp's handle_reverse_call,
			// which never touches g_channel->cmd/direction when
			// answering -- only the blob content and response_ready).
			if werr := ch.writeMessage(rcmd, rdir, outHeader, outBlob); werr != nil {
				return nil, nil, fmt.Errorf("mvm: tzHardware: write reverse-call response (cmd=%d): %w", rcmd, werr)
			}
			ch.postResponseReady()
			continue
		}
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf(
				"mvm: tzHardware: TIMEOUT after %s waiting for cmd=%d response -- "+
					"genuinely stuck, not just slow (CLAUDE.md: a stuck TA spins forever "+
					"rather than crashing cleanly, indistinguishable from outside without "+
					"this kind of explicit timeout)", tzHardwareRoundTripTimeout, cmd)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ────────────────────── the 2 TA-supported forward commands ────────────────
//
// Only CALL and EXECUTE are wired on the real mvm_ta side today (see
// ta/mvm_ta_main.cpp's dispatch switch, and this file's own doc comment
// on the 5 NOT-yet-supported ones below). Guarded by tzSessionMu
// (declared in tz_channel.go, shared with tzLoopbackEngine) for the
// whole call, per the plan's serialization decision.

func (e *tzHardwareEngine) Call(
	bSender []byte, bContractAddress []byte, bInput []byte, amount *big.Int,
	gasPrice uint64, gasLimit uint64, blockPrevrandao uint64, blockGasLimit uint64,
	blockTime uint64, blockBaseFee uint64, blockNumber uint64, blockCoinbase common.Address,
	mvmId common.Address, readOnly bool, bTxHash []byte, relatedAddresses []common.Address,
	isDebug bool, isOffChain bool,
) *MVMExecuteResult {
	tzSessionMu.Lock()
	defer tzSessionMu.Unlock()

	reqHeader, reqBlob := encodeCallReq(
		bSender, bContractAddress, bInput, amount,
		gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber,
		blockCoinbase, mvmId, readOnly, bTxHash, relatedAddresses, isDebug, isOffChain,
	)

	respHeader, respBlob, err := tzHardwareRoundTrip(C.MVM_TZ_CMD_CALL, reqHeader, reqBlob)
	if err != nil {
		// Matches tzLoopbackEngine's own panic-on-protocol-error posture
		// (that type's doc comment) -- ExecutionEngine.Call has no error
		// return, so there is no clean way to surface a hardware-level
		// failure (timeout, decode mismatch) to the caller today. A real
		// production error-handling design (retry policy? node-level
		// alarm?) is deliberately NOT invented here without more
		// operational experience of how this actually fails on hardware.
		panic(fmt.Errorf("mvm: tzHardware: Call round trip: %w", err))
	}
	result, derr := decodeExecuteResult(respHeader, respBlob)
	if derr != nil {
		panic(fmt.Errorf("mvm: tzHardware: decodeExecuteResult (Call): %w", derr))
	}
	return result
}

func (e *tzHardwareEngine) Execute(
	bSender []byte, bContractAddress []byte, bInput []byte, amount *big.Int,
	gasPrice uint64, gasLimit uint64, blockPrevrandao uint64, blockGasLimit uint64,
	blockTime uint64, blockBaseFee uint64, blockNumber uint64, blockCoinbase common.Address,
	mvmId common.Address, bTxHash []byte, relatedAddresses []common.Address,
	isDebug bool, isCache bool,
) *MVMExecuteResult {
	tzSessionMu.Lock()
	defer tzSessionMu.Unlock()

	reqHeader, reqBlob := encodeExecuteReq(
		bSender, bContractAddress, bInput, amount,
		gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber,
		blockCoinbase, mvmId, bTxHash, relatedAddresses, isDebug, isCache,
	)

	respHeader, respBlob, err := tzHardwareRoundTrip(C.MVM_TZ_CMD_EXECUTE, reqHeader, reqBlob)
	if err != nil {
		panic(fmt.Errorf("mvm: tzHardware: Execute round trip: %w", err))
	}
	result, derr := decodeExecuteResult(respHeader, respBlob)
	if derr != nil {
		panic(fmt.Errorf("mvm: tzHardware: decodeExecuteResult (Execute): %w", derr))
	}
	return result
}

// ────────────── forward commands the real TA does not implement yet ─────────
//
// ta/mvm_ta_main.cpp's dispatch switch only has cases for CALL/EXECUTE —
// DEPLOY/SEND_NATIVE/PROCESS_NATIVE_MINT_BURN/NONCE_PLUS_ONE all fall
// through to its own "not yet implemented" default branch, and
// EXECUTE_BATCH has no wire codec at all yet (tz_codec.go). Panicking
// here, loudly and immediately, rather than silently returning a
// zero-value/fabricated *MVMExecuteResult -- a caller that reaches one
// of these on a real hardware-mode node needs to know THAT, not receive
// a result that looks like a legitimate (if odd) on-chain outcome.

func (e *tzHardwareEngine) Deploy(
	bSender []byte, bContractConstructor []byte, amount *big.Int,
	gasPrice uint64, gasLimit uint64, blockPrevrandao uint64, blockGasLimit uint64,
	blockTime uint64, blockBaseFee uint64, blockNumber uint64, blockCoinbase common.Address,
	mvmId common.Address, bTxHash []byte, isDebug bool, isCache bool, isOffChain bool,
) *MVMExecuteResult {
	panic("mvm: tzHardwareEngine.Deploy: MVM_TZ_CMD_DEPLOY is not implemented on the real TA yet (ta/mvm_ta_main.cpp)")
}

func (e *tzHardwareEngine) SendNative(
	bSender []byte, bContractAddress []byte, amount *big.Int,
	gasPrice uint64, gasLimit uint64, blockPrevrandao uint64, blockGasLimit uint64,
	blockTime uint64, blockBaseFee uint64, blockNumber uint64, blockCoinbase common.Address,
	mvmId common.Address, isCache bool,
) *MVMExecuteResult {
	panic("mvm: tzHardwareEngine.SendNative: MVM_TZ_CMD_SEND_NATIVE is not implemented on the real TA yet (ta/mvm_ta_main.cpp)")
}

func (e *tzHardwareEngine) ProcessNativeMintBurn(
	bFrom []byte, bTo []byte, amount *big.Int, operationType uint64,
	gasPrice uint64, gasLimit uint64, blockPrevrandao uint64, blockGasLimit uint64,
	blockTime uint64, blockBaseFee uint64, blockNumber uint64, blockCoinbase common.Address,
	mvmId common.Address, isCache bool,
) *MVMExecuteResult {
	panic("mvm: tzHardwareEngine.ProcessNativeMintBurn: MVM_TZ_CMD_PROCESS_NATIVE_MINT_BURN is not implemented on the real TA yet (ta/mvm_ta_main.cpp)")
}

func (e *tzHardwareEngine) NoncePlusOne(
	bSender []byte,
	gasPrice uint64, gasLimit uint64, blockPrevrandao uint64, blockGasLimit uint64,
	blockTime uint64, blockBaseFee uint64, blockNumber uint64, blockCoinbase common.Address,
	mvmId common.Address, isCache bool,
) *MVMExecuteResult {
	panic("mvm: tzHardwareEngine.NoncePlusOne: MVM_TZ_CMD_NONCE_PLUS_ONE is not implemented on the real TA yet (ta/mvm_ta_main.cpp)")
}

func (e *tzHardwareEngine) ExecuteBatch(
	inputs []ExecuteBatchInput,
	blockPrevrandao uint64, blockGasLimit uint64, blockTime uint64, blockBaseFee uint64,
	blockNumber uint64, blockCoinbase common.Address, mvmId common.Address,
) []*MVMExecuteResult {
	panic("mvm: tzHardwareEngine.ExecuteBatch: no wire codec exists for this command yet (tz_codec.go)")
}
