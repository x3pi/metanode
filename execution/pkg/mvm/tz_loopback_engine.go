package mvm

/*
#include "tzproto/mvm_tz_protocol.h"
*/
import "C"
import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// tzLoopbackEngine implements ExecutionEngine for GĐ2 of
// note/tee_dual_mode_execution_plan.md: it runs the real mvm_tz_protocol.h
// wire encoding (real C struct layouts, real spinlock CAS via tzChannel) but
// loops back to a genuine *MVMApi in the same process instead of crossing a
// real CA<->TA world switch. Its job is to catch protocol/layout bugs on
// x86, long before GĐ3 has real hardware to test on — NOT to model real
// cross-world latency or concurrency (see tz_channel.go's doc comment).
//
// Any error surfaced by the codec here (bad header length, unmarshal
// failure, etc.) is treated as a protocol/layout bug, not a runtime
// condition callers should handle — it panics, deliberately, same spirit as
// execution_mode.go's "not implemented" panic: a silently-wrong wire
// round-trip is worse than a loud crash while this is still dev-only.
type tzLoopbackEngine struct {
	real *MVMApi
}

func newTZLoopbackEngine(
	key common.Address,
	smartContractDb SmartContractDB,
	accountStateDb AccountStateDB,
	extendedMode bool,
) *tzLoopbackEngine {
	return &tzLoopbackEngine{
		real: GetOrCreateMVMApi(key, smartContractDb, accountStateDb, extendedMode),
	}
}

var _ ExecutionEngine = (*tzLoopbackEngine)(nil)

// ─────────────────── local bookkeeping: no wire protocol ───────────────────
//
// These are pure Go-side instance state (per note/tee_dual_mode_execution_
// plan.md's GĐ2 scope: only the 6 forward exec commands + reverse callbacks
// cross the wire; identity/context wiring is host-local bookkeeping today
// and stays host-local in TZ mode too — see mvm_tz_protocol.h's command
// inventory, which deliberately has no CMD for any of these).

func (e *tzLoopbackEngine) GetKey() common.Address { return e.real.GetKey() }
func (e *tzLoopbackEngine) SetSmartContractDb(smartContractDb SmartContractDB) {
	e.real.SetSmartContractDb(smartContractDb)
}
func (e *tzLoopbackEngine) SmartContractDatas() SmartContractDB { return e.real.SmartContractDatas() }
func (e *tzLoopbackEngine) SetAccountStateDb(accountStateDb AccountStateDB) {
	e.real.SetAccountStateDb(accountStateDb)
}
func (e *tzLoopbackEngine) AccountStateDb() AccountStateDB { return e.real.AccountStateDb() }
func (e *tzLoopbackEngine) SetRelatedAddresses(addresses []common.Address) {
	e.real.SetRelatedAddresses(addresses)
}
func (e *tzLoopbackEngine) GetCurrentRelatedAddresses() []common.Address {
	return e.real.GetCurrentRelatedAddresses()
}
func (e *tzLoopbackEngine) InRelatedAddress(address common.Address) bool {
	return e.real.InRelatedAddress(address)
}
func (e *tzLoopbackEngine) AddRelatedAddress(address common.Address) {
	e.real.AddRelatedAddress(address)
}
func (e *tzLoopbackEngine) SetBlobContext(blobVersionedHashes [][]byte, blobBaseFee *uint256.Int) {
	e.real.SetBlobContext(blobVersionedHashes, blobBaseFee)
}
func (e *tzLoopbackEngine) SetCrossChainContext(sender common.Address, sourceChainId uint64) {
	e.real.SetCrossChainContext(sender, sourceChainId)
}
func (e *tzLoopbackEngine) ClearCrossChainContext() { e.real.ClearCrossChainContext() }
func (e *tzLoopbackEngine) GetExecuteResult() *MVMExecuteResult {
	return e.real.GetExecuteResult()
}

// ─────────────────────── generic round-trip plumbing ───────────────────────

// tzLoopbackRoundTrip drives one full request/response cycle over the
// shared tzChannel: write the (already-encoded) request as HOST_TO_TA,
// flip request_ready, "TA side" consumes it and runs process (decode ->
// call real *MVMApi -> encode result) — same-process for GĐ2, a real TA
// event loop from GĐ3 on — writes the response back as TA_TO_HOST, flips
// response_ready, and the host consumes+reads it back out. Every step
// exercises the real primitives from tz_channel.go, not a shortcut.
func tzLoopbackRoundTrip(
	cmd C.mvm_tz_cmd_t,
	reqHeader, reqBlob []byte,
	process func(reqHeader, reqBlob []byte) (respHeader, respBlob []byte),
) (respHeader, respBlob []byte) {
	ch := getTZChannel()

	if err := ch.writeMessage(cmd, C.MVM_TZ_DIR_HOST_TO_TA, reqHeader, reqBlob); err != nil {
		panic(fmt.Errorf("mvm: tzLoopback: write request (cmd=%d): %w", cmd, err))
	}
	ch.postRequestReady()

	// "TA side": consume the request and run the real work. In GĐ2 this
	// happens synchronously in the same goroutine — GĐ3's real TA runs
	// this as its own persistent request-serving loop instead.
	if !ch.consumeRequestReady() {
		panic(fmt.Errorf("mvm: tzLoopback: TA side failed to consume request_ready (cmd=%d)", cmd))
	}
	_, _, gotHeader, gotBlob := ch.readMessage()
	outHeader, outBlob := process(gotHeader, gotBlob)

	if err := ch.writeMessage(cmd, C.MVM_TZ_DIR_TA_TO_HOST, outHeader, outBlob); err != nil {
		panic(fmt.Errorf("mvm: tzLoopback: write response (cmd=%d): %w", cmd, err))
	}
	ch.postResponseReady()

	if !ch.consumeResponseReady() {
		panic(fmt.Errorf("mvm: tzLoopback: host side failed to consume response_ready (cmd=%d)", cmd))
	}
	_, _, respHeader, respBlob = ch.readMessage()
	return respHeader, respBlob
}

// ────────────────────── the 6 live forward exec commands ───────────────────
//
// All 6 follow the identical pattern: encode request -> round-trip -> decode
// result. Guarded by tzSessionMu (declared in tz_channel.go) for the whole
// call, per the plan's serialization decision — multiple Block-STM worker
// goroutines calling in concurrently must queue safely through the one
// channel, not race on it.

func (e *tzLoopbackEngine) Call(
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

	respHeader, respBlob := tzLoopbackRoundTrip(C.MVM_TZ_CMD_CALL, reqHeader, reqBlob,
		func(h, b []byte) ([]byte, []byte) {
			dSender, dContractAddress, dInput, dAmount,
				dGasPrice, dGasLimit, dBlockPrevrandao, dBlockGasLimit, dBlockTime, dBlockBaseFee, dBlockNumber,
				dBlockCoinbase, dMvmId, dReadOnly, dTxHash, dRelatedAddresses,
				dIsDebug, dIsOffChain, err := decodeCallReq(h, b)
			if err != nil {
				panic(fmt.Errorf("mvm: tzLoopback: decodeCallReq: %w", err))
			}
			result := e.real.Call(
				dSender, dContractAddress, dInput, dAmount,
				dGasPrice, dGasLimit, dBlockPrevrandao, dBlockGasLimit, dBlockTime, dBlockBaseFee, dBlockNumber,
				dBlockCoinbase, dMvmId, dReadOnly, dTxHash, dRelatedAddresses, dIsDebug, dIsOffChain,
			)
			rh, rb, eerr := encodeExecuteResult(result)
			if eerr != nil {
				panic(fmt.Errorf("mvm: tzLoopback: encodeExecuteResult (Call): %w", eerr))
			}
			return rh, rb
		})

	result, err := decodeExecuteResult(respHeader, respBlob)
	if err != nil {
		panic(fmt.Errorf("mvm: tzLoopback: decodeExecuteResult (Call): %w", err))
	}
	return result
}

func (e *tzLoopbackEngine) Execute(
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

	respHeader, respBlob := tzLoopbackRoundTrip(C.MVM_TZ_CMD_EXECUTE, reqHeader, reqBlob,
		func(h, b []byte) ([]byte, []byte) {
			dSender, dContractAddress, dInput, dAmount,
				dGasPrice, dGasLimit, dBlockPrevrandao, dBlockGasLimit, dBlockTime, dBlockBaseFee, dBlockNumber,
				dBlockCoinbase, dMvmId, dTxHash, dRelatedAddresses,
				dIsDebug, dIsCache, err := decodeExecuteReq(h, b)
			if err != nil {
				panic(fmt.Errorf("mvm: tzLoopback: decodeExecuteReq: %w", err))
			}
			result := e.real.Execute(
				dSender, dContractAddress, dInput, dAmount,
				dGasPrice, dGasLimit, dBlockPrevrandao, dBlockGasLimit, dBlockTime, dBlockBaseFee, dBlockNumber,
				dBlockCoinbase, dMvmId, dTxHash, dRelatedAddresses, dIsDebug, dIsCache,
			)
			rh, rb, eerr := encodeExecuteResult(result)
			if eerr != nil {
				panic(fmt.Errorf("mvm: tzLoopback: encodeExecuteResult (Execute): %w", eerr))
			}
			return rh, rb
		})

	result, err := decodeExecuteResult(respHeader, respBlob)
	if err != nil {
		panic(fmt.Errorf("mvm: tzLoopback: decodeExecuteResult (Execute): %w", err))
	}
	return result
}

func (e *tzLoopbackEngine) Deploy(
	bSender []byte, bContractConstructor []byte, amount *big.Int,
	gasPrice uint64, gasLimit uint64, blockPrevrandao uint64, blockGasLimit uint64,
	blockTime uint64, blockBaseFee uint64, blockNumber uint64, blockCoinbase common.Address,
	mvmId common.Address, bTxHash []byte, isDebug bool, isCache bool, isOffChain bool,
) *MVMExecuteResult {
	tzSessionMu.Lock()
	defer tzSessionMu.Unlock()

	reqHeader, reqBlob := encodeDeployReq(
		bSender, bContractConstructor, amount,
		gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber,
		blockCoinbase, mvmId, bTxHash, isDebug, isCache, isOffChain,
	)

	respHeader, respBlob := tzLoopbackRoundTrip(C.MVM_TZ_CMD_DEPLOY, reqHeader, reqBlob,
		func(h, b []byte) ([]byte, []byte) {
			dSender, dContractConstructor, dAmount,
				dGasPrice, dGasLimit, dBlockPrevrandao, dBlockGasLimit, dBlockTime, dBlockBaseFee, dBlockNumber,
				dBlockCoinbase, dMvmId, dTxHash, dIsDebug, dIsCache, dIsOffChain, err := decodeDeployReq(h, b)
			if err != nil {
				panic(fmt.Errorf("mvm: tzLoopback: decodeDeployReq: %w", err))
			}
			result := e.real.Deploy(
				dSender, dContractConstructor, dAmount,
				dGasPrice, dGasLimit, dBlockPrevrandao, dBlockGasLimit, dBlockTime, dBlockBaseFee, dBlockNumber,
				dBlockCoinbase, dMvmId, dTxHash, dIsDebug, dIsCache, dIsOffChain,
			)
			rh, rb, eerr := encodeExecuteResult(result)
			if eerr != nil {
				panic(fmt.Errorf("mvm: tzLoopback: encodeExecuteResult (Deploy): %w", eerr))
			}
			return rh, rb
		})

	result, err := decodeExecuteResult(respHeader, respBlob)
	if err != nil {
		panic(fmt.Errorf("mvm: tzLoopback: decodeExecuteResult (Deploy): %w", err))
	}
	return result
}

func (e *tzLoopbackEngine) SendNative(
	bSender []byte, bContractAddress []byte, amount *big.Int,
	gasPrice uint64, gasLimit uint64, blockPrevrandao uint64, blockGasLimit uint64,
	blockTime uint64, blockBaseFee uint64, blockNumber uint64, blockCoinbase common.Address,
	mvmId common.Address, isCache bool,
) *MVMExecuteResult {
	tzSessionMu.Lock()
	defer tzSessionMu.Unlock()

	reqHeader, reqBlob := encodeSendNativeReq(
		bSender, bContractAddress, amount,
		gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber,
		blockCoinbase, mvmId, isCache,
	)

	respHeader, respBlob := tzLoopbackRoundTrip(C.MVM_TZ_CMD_SEND_NATIVE, reqHeader, reqBlob,
		func(h, b []byte) ([]byte, []byte) {
			dSender, dContractAddress, dAmount,
				dGasPrice, dGasLimit, dBlockPrevrandao, dBlockGasLimit, dBlockTime, dBlockBaseFee, dBlockNumber,
				dBlockCoinbase, dMvmId, dIsCache, err := decodeSendNativeReq(h, b)
			if err != nil {
				panic(fmt.Errorf("mvm: tzLoopback: decodeSendNativeReq: %w", err))
			}
			result := e.real.SendNative(
				dSender, dContractAddress, dAmount,
				dGasPrice, dGasLimit, dBlockPrevrandao, dBlockGasLimit, dBlockTime, dBlockBaseFee, dBlockNumber,
				dBlockCoinbase, dMvmId, dIsCache,
			)
			rh, rb, eerr := encodeExecuteResult(result)
			if eerr != nil {
				panic(fmt.Errorf("mvm: tzLoopback: encodeExecuteResult (SendNative): %w", eerr))
			}
			return rh, rb
		})

	result, err := decodeExecuteResult(respHeader, respBlob)
	if err != nil {
		panic(fmt.Errorf("mvm: tzLoopback: decodeExecuteResult (SendNative): %w", err))
	}
	return result
}

func (e *tzLoopbackEngine) ProcessNativeMintBurn(
	bFrom []byte, bTo []byte, amount *big.Int, operationType uint64,
	gasPrice uint64, gasLimit uint64, blockPrevrandao uint64, blockGasLimit uint64,
	blockTime uint64, blockBaseFee uint64, blockNumber uint64, blockCoinbase common.Address,
	mvmId common.Address, isCache bool,
) *MVMExecuteResult {
	tzSessionMu.Lock()
	defer tzSessionMu.Unlock()

	reqHeader, reqBlob := encodeProcessNativeMintBurnReq(
		bFrom, bTo, amount, operationType,
		gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber,
		blockCoinbase, mvmId, isCache,
	)

	respHeader, respBlob := tzLoopbackRoundTrip(C.MVM_TZ_CMD_PROCESS_NATIVE_MINT_BURN, reqHeader, reqBlob,
		func(h, b []byte) ([]byte, []byte) {
			dFrom, dTo, dAmount, dOperationType,
				dGasPrice, dGasLimit, dBlockPrevrandao, dBlockGasLimit, dBlockTime, dBlockBaseFee, dBlockNumber,
				dBlockCoinbase, dMvmId, dIsCache, err := decodeProcessNativeMintBurnReq(h, b)
			if err != nil {
				panic(fmt.Errorf("mvm: tzLoopback: decodeProcessNativeMintBurnReq: %w", err))
			}
			result := e.real.ProcessNativeMintBurn(
				dFrom, dTo, dAmount, dOperationType,
				dGasPrice, dGasLimit, dBlockPrevrandao, dBlockGasLimit, dBlockTime, dBlockBaseFee, dBlockNumber,
				dBlockCoinbase, dMvmId, dIsCache,
			)
			rh, rb, eerr := encodeExecuteResult(result)
			if eerr != nil {
				panic(fmt.Errorf("mvm: tzLoopback: encodeExecuteResult (ProcessNativeMintBurn): %w", eerr))
			}
			return rh, rb
		})

	result, err := decodeExecuteResult(respHeader, respBlob)
	if err != nil {
		panic(fmt.Errorf("mvm: tzLoopback: decodeExecuteResult (ProcessNativeMintBurn): %w", err))
	}
	return result
}

func (e *tzLoopbackEngine) NoncePlusOne(
	bSender []byte,
	gasPrice uint64, gasLimit uint64, blockPrevrandao uint64, blockGasLimit uint64,
	blockTime uint64, blockBaseFee uint64, blockNumber uint64, blockCoinbase common.Address,
	mvmId common.Address, isCache bool,
) *MVMExecuteResult {
	tzSessionMu.Lock()
	defer tzSessionMu.Unlock()

	reqHeader, reqBlob := encodeNoncePlusOneReq(
		bSender,
		gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber,
		blockCoinbase, mvmId, isCache,
	)

	respHeader, respBlob := tzLoopbackRoundTrip(C.MVM_TZ_CMD_NONCE_PLUS_ONE, reqHeader, reqBlob,
		func(h, b []byte) ([]byte, []byte) {
			dSender,
				dGasPrice, dGasLimit, dBlockPrevrandao, dBlockGasLimit, dBlockTime, dBlockBaseFee, dBlockNumber,
				dBlockCoinbase, dMvmId, dIsCache, err := decodeNoncePlusOneReq(h, b)
			if err != nil {
				panic(fmt.Errorf("mvm: tzLoopback: decodeNoncePlusOneReq: %w", err))
			}
			result := e.real.NoncePlusOne(
				dSender,
				dGasPrice, dGasLimit, dBlockPrevrandao, dBlockGasLimit, dBlockTime, dBlockBaseFee, dBlockNumber,
				dBlockCoinbase, dMvmId, dIsCache,
			)
			rh, rb, eerr := encodeExecuteResult(result)
			if eerr != nil {
				panic(fmt.Errorf("mvm: tzLoopback: encodeExecuteResult (NoncePlusOne): %w", eerr))
			}
			return rh, rb
		})

	result, err := decodeExecuteResult(respHeader, respBlob)
	if err != nil {
		panic(fmt.Errorf("mvm: tzLoopback: decodeExecuteResult (NoncePlusOne): %w", err))
	}
	return result
}

// ExecuteBatch is confirmed dead in Go today (nothing calls it — see
// mvm_tz_protocol.h's MVM_TZ_CMD_EXECUTE_BATCH comment and note/
// tee_dual_mode_execution_plan.md's phase notes). Per the plan's explicit
// lowest-priority guidance, it delegates straight to the real engine without
// wire-protocol wrapping — kept only for ExecutionEngine interface parity,
// not exercised by any current call site, so there is nothing to gain from
// building out its wire codec (MVM_TZ_CMD_EXECUTE_BATCH's req struct
// already exists in mvm_tz_protocol.h if/when a real caller shows up).
func (e *tzLoopbackEngine) ExecuteBatch(
	inputs []ExecuteBatchInput,
	blockPrevrandao uint64, blockGasLimit uint64, blockTime uint64, blockBaseFee uint64,
	blockNumber uint64, blockCoinbase common.Address, mvmId common.Address,
) []*MVMExecuteResult {
	return e.real.ExecuteBatch(inputs, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber, blockCoinbase, mvmId)
}
