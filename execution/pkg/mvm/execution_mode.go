package mvm

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// Execution mode constants. See note/tee_dual_mode_execution_plan.md.
const (
	ModeCgo       = "cgo"       // in-process, today's behavior (default)
	ModeTrustzone = "trustzone" // mvm+Xapian core runs inside a TrustZone TA
)

// Global execution mode setting, mirroring pkg/trie/trie_factory.go's
// globalStateBackend pattern. Set once at startup from config.
var globalExecutionMode = ModeCgo

// SetExecutionMode sets the global execution mode. Must be called before any
// NewExecutionEngine call, i.e. before transaction processing starts.
func SetExecutionMode(mode string) {
	switch mode {
	case ModeCgo:
		globalExecutionMode = ModeCgo
		logger.Info("🔧 [MVM] Execution mode set to: cgo (in-process)")
	case ModeTrustzone:
		globalExecutionMode = ModeTrustzone
		logger.Info("🔧 [MVM] Execution mode set to: trustzone (TA)")
	case "":
		// Empty config: keep the global default (cgo)
		logger.Info("🔧 [MVM] Execution mode using default: %s", globalExecutionMode)
	default:
		logger.Warn("⚠️ [MVM] Unknown execution mode '%s', defaulting to cgo", mode)
		globalExecutionMode = ModeCgo
	}
}

// GetExecutionMode returns the current global execution mode.
func GetExecutionMode() string {
	return globalExecutionMode
}

// NewExecutionEngine constructs an ExecutionEngine for mvmId according to the
// global execution mode.
//
// This does NOT replace GetOrCreateMVMApi/GetMVMApi: the 6 live C++->Go
// callbacks (GlobalStateGet, GetStorageValue, ExtensionCallGetApi,
// ExtensionExtractJsonField, ExtensionBlst, ExtensionGetOrCreateSimpleDb —
// see note/tee_dual_mode_execution_plan.md section 2.1) resolve their
// instance via GetMVMApi and then read unexported *MVMApi fields directly
// (accountStateDb, smartContractDb, extendedMode, currentRelatedAddresses).
// Changing GetOrCreateMVMApi to return the interface would break those call
// sites. This factory is a separate, caller-facing entry point for mode
// selection only — existing call sites keep using GetOrCreateMVMApi
// directly and are unaffected by this function's existence.
func NewExecutionEngine(
	key common.Address,
	smartContractDb SmartContractDB,
	accountStateDb AccountStateDB,
	extendedMode bool,
) ExecutionEngine {
	switch globalExecutionMode {
	case ModeTrustzone:
		// GĐ2 (note/tee_dual_mode_execution_plan.md): loopback transport —
		// real wire encoding (tz_codec.go) + real spinlock CAS channel
		// (tz_channel.go), looped back to a real *MVMApi in-process. GĐ3
		// replaces the "TA side" of tzLoopbackRoundTrip with an actual TA
		// running on hardware; the ExecutionEngine surface Go callers use
		// does not change.
		return newTZLoopbackEngine(key, smartContractDb, accountStateDb, extendedMode)
	default: // ModeCgo
		return GetOrCreateMVMApi(key, smartContractDb, accountStateDb, extendedMode)
	}
}
