package mvm

import (
	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// ExecutionMode selects which ExecutionEngine implementation
// NewExecutionEngine hands back — the same runtime-switch-from-config
// pattern pkg/trie/trie_factory.go already uses for StateBackend (global
// var set once at startup, read by a factory function; see that file for
// the template this mirrors). See
// note/tee_dual_mode_execution_plan.md's "Giai đoạn 0" for the full design
// and why this is a pure where-it-runs switch, not a trust-model change.
const (
	// ModeCgo is today's default: mvm+Xapian in-process via cgo (*MVMApi).
	ModeCgo = "cgo"
	// ModeTrustzone routes execution through tzLoopbackEngine
	// (tz_loopback_engine.go) — GĐ2's x86 loopback transport: encodes the
	// call through the real wire codec (tz_codec.go) and the real
	// spinlock-CAS shared channel (tz_channel.go), then calls straight
	// into the SAME *MVMApi in-process (no real TA yet, see GĐ3). This
	// exercises the protocol end to end without hardware — a real TA
	// client (GĐ3) will need its own ModeTZHardware-style addition here
	// later, not a change to this one.
	ModeTrustzone = "trustzone"
)

// Global execution mode setting. Set once at startup from config
// (SimpleChainConfig.ExecutionMode, pkg/config/config.go) — mirrors
// pkg/trie/trie_factory.go's globalStateBackend exactly.
var globalExecutionMode = ModeCgo

// SetExecutionMode sets the global execution mode. Must be called before
// any call to NewExecutionEngine (typically once at node startup, right
// alongside trie.SetStateBackend — see cmd/simple_chain/app.go).
func SetExecutionMode(mode string) {
	switch mode {
	case ModeTrustzone:
		globalExecutionMode = ModeTrustzone
		logger.Warn("⚠️ [MVM] Execution mode set to: TRUSTZONE (GĐ2 x86 loopback -- see note/tee_dual_mode_execution_plan.md; NOT a real TA yet, that's GĐ3)")
	case ModeCgo, "":
		// Empty config: keep the global default (cgo) -- matches
		// trie_factory.go's own empty-string handling.
		globalExecutionMode = ModeCgo
		logger.Info("🔧 [MVM] Execution mode set to: CGO (in-process mvm+Xapian, default)")
	default:
		logger.Warn("⚠️ [MVM] Unknown execution mode '%s', defaulting to cgo", mode)
		globalExecutionMode = ModeCgo
	}
}

// GetExecutionMode returns the current global execution mode.
func GetExecutionMode() string {
	return globalExecutionMode
}

// NewExecutionEngine returns an ExecutionEngine for key, chosen by the
// global execution mode (see SetExecutionMode). This is the intended
// entry point for new call sites that want to be mode-agnostic — it does
// NOT replace GetOrCreateMVMApi (still the only way to get a *MVMApi
// specifically, and every existing cgo call site keeps calling it
// directly and unchanged; see this package's own engine.go doc comment
// for why *MVMApi's own exported fields can't just be swapped for the
// interface type).
func NewExecutionEngine(
	key common.Address,
	smartContractDb SmartContractDB,
	accountStateDb AccountStateDB,
	extendedMode bool,
) ExecutionEngine {
	switch globalExecutionMode {
	case ModeTrustzone:
		return newTZLoopbackEngine(key, smartContractDb, accountStateDb, extendedMode)
	default: // ModeCgo
		return GetOrCreateMVMApi(key, smartContractDb, accountStateDb, extendedMode)
	}
}
