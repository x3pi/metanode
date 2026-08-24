package mvm_test

// Real-hardware mirrors of ta_boundary_harness_test.go's
// TestTABoundary_TrustzoneLoopback_MatchesCgo_{SendNative,
// ProcessNativeMintBurn,NoncePlusOne} — same cgo-vs-X comparison shape,
// same runXViaMode helpers (mode is already a plain string parameter
// there, so mvm.ModeTrustzoneHardware needs no new helper), but X here is
// ModeTrustzoneHardware instead of ModeTrustzone: a REAL separate mvm_ta
// process on the board over /dev/tc_ns_client, not an in-process loopback.
//
// WHY these 3 specifically: Call/Execute/Deploy (the other 3 forward
// commands) were already validated against real hardware via
// cmd/tool/tz_replay_check replaying real committed block data
// (DEPLOYED_STATE.md's 2026-08-21/22 "Go CA THẬT" entry). SendNative/
// ProcessNativeMintBurn/NoncePlusOne were wired on tzHardwareEngine the
// same session (same commit) but never actually exercised against real
// hardware -- only against the TA side directly via the C++ test harness
// (ta/ca_test/mvm_ca_test.cpp). This file closes that gap: it exercises
// the Go engine's own encode/round-trip/decode path for these 3, not just
// the TA's dispatch handlers in isolation.
//
// Requires REAL hardware -- a genuine mvm_ta process already running on
// this exact board (chanmgr launches it once per boot; see CLAUDE.md-style
// guidance in tz-llm-trustzone's own CLAUDE.md, and this repo's
// tz_hardware_channel.go's own doc comments). Not runnable on a dev
// machine; cross-compile for aarch64 and run via `go test -c` on the
// board, same pattern already used for cmd/tool/tz_replay_check this
// session -- see DEPLOYED_STATE.md for the exact toolchain/library-swap
// recipe.
//
// Per CLAUDE.md's "TA launches once per boot" rule: each `go test` binary
// invocation opens the hardware channel once (sync.Once, tz_hardware_
// channel.go) and keeps it for the whole process -- running these 3
// tests in ONE `go test` invocation (the default) is correct and safe;
// do NOT invoke this test binary a second time in the same boot session,
// same as any other real-hardware test/tool in this repo.

import (
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

func skipIfNoTZHardware(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/tc_ns_client"); os.IsNotExist(err) {
		t.Skip("skipping real-hardware TrustZone test: /dev/tc_ns_client not found (only runnable on physical board)")
	}
}

func TestTABoundary_TrustzoneHardware_MatchesCgo_SendNative(t *testing.T) {
	skipIfNoTZHardware(t)
	from := nextTestAddr()
	to := nextTestAddr()
	amount := big.NewInt(1_234)

	cgoRs := runNativeTransferViaMode(t, mvm.ModeCgo,
		nextTestAddr(), from, to, amount)
	hwRs := runNativeTransferViaMode(t, mvm.ModeTrustzoneHardware,
		nextTestAddr(), from, to, amount)

	if cgoRs.Status != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("cgo status = %v, want RECEIPT_STATUS_RETURNED (exmsg=%q)", cgoRs.Status, cgoRs.Exmsg)
	}
	if hwRs.Status != cgoRs.Status {
		t.Fatalf("trustzone-hardware status = %v, want %v (cgo) (exmsg=%q)", hwRs.Status, cgoRs.Status, hwRs.Exmsg)
	}

	toKey := hexKey(to)
	cgoAdd, ok := cgoRs.MapAddBalance[toKey]
	if !ok {
		t.Fatalf("cgo: MapAddBalance missing entry for recipient %s", toKey)
	}
	hwAdd, ok := hwRs.MapAddBalance[toKey]
	if !ok {
		t.Fatalf("trustzone-hardware: MapAddBalance missing entry for recipient %s; got keys %v", toKey, keysOf(hwRs.MapAddBalance))
	}
	if string(hwAdd) != string(cgoAdd) {
		t.Errorf("MapAddBalance[recipient]: trustzone-hardware=%x, cgo=%x", hwAdd, cgoAdd)
	}
	if new(big.Int).SetBytes(cgoAdd).Cmp(amount) != 0 {
		t.Errorf("cgo MapAddBalance[recipient] = %s, want %s", new(big.Int).SetBytes(cgoAdd), amount)
	}
}

func TestTABoundary_TrustzoneHardware_MatchesCgo_ProcessNativeMintBurn(t *testing.T) {
	skipIfNoTZHardware(t)
	systemAddr := common.HexToAddress("0x000000000000000000000000000000000000MINT")
	to := nextTestAddr()
	amount := big.NewInt(777_777)

	cgoRs := runMintBurnViaMode(t, mvm.ModeCgo,
		nextTestAddr(), systemAddr, to, amount, 0)
	hwRs := runMintBurnViaMode(t, mvm.ModeTrustzoneHardware,
		nextTestAddr(), systemAddr, to, amount, 0)

	if cgoRs.Status != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("cgo ProcessNativeMintBurn status = %v, want RECEIPT_STATUS_RETURNED (exmsg=%q)", cgoRs.Status, cgoRs.Exmsg)
	}
	if hwRs.Status != cgoRs.Status {
		t.Fatalf("trustzone-hardware ProcessNativeMintBurn status = %v, want %v (cgo) (exmsg=%q)", hwRs.Status, cgoRs.Status, hwRs.Exmsg)
	}

	toKey := hexKey(to)
	cgoAdd, ok := cgoRs.MapAddBalance[toKey]
	if !ok {
		t.Fatalf("cgo: MapAddBalance missing entry for mint recipient %s", toKey)
	}
	hwAdd, ok := hwRs.MapAddBalance[toKey]
	if !ok {
		t.Fatalf("trustzone-hardware: MapAddBalance missing entry for mint recipient %s; got keys %v", toKey, keysOf(hwRs.MapAddBalance))
	}
	if string(hwAdd) != string(cgoAdd) {
		t.Errorf("MapAddBalance[recipient]: trustzone-hardware=%x, cgo=%x", hwAdd, cgoAdd)
	}
	if new(big.Int).SetBytes(cgoAdd).Cmp(amount) != 0 {
		t.Errorf("cgo MapAddBalance[recipient] = %s, want %s", new(big.Int).SetBytes(cgoAdd), amount)
	}
}

func TestTABoundary_TrustzoneHardware_MatchesCgo_NoncePlusOne(t *testing.T) {
	skipIfNoTZHardware(t)
	sender := nextTestAddr()
	const startNonce = 41

	cgoRs := runNoncePlusOneViaMode(t, mvm.ModeCgo,
		nextTestAddr(), sender, startNonce)
	hwRs := runNoncePlusOneViaMode(t, mvm.ModeTrustzoneHardware,
		nextTestAddr(), sender, startNonce)

	if cgoRs.Status != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("cgo NoncePlusOne status = %v, want RECEIPT_STATUS_RETURNED (exmsg=%q)", cgoRs.Status, cgoRs.Exmsg)
	}
	if hwRs.Status != cgoRs.Status {
		t.Fatalf("trustzone-hardware NoncePlusOne status = %v, want %v (cgo) (exmsg=%q)", hwRs.Status, cgoRs.Status, hwRs.Exmsg)
	}

	senderKey := hexKey(sender)
	cgoNonce, ok := cgoRs.MapNonce[senderKey]
	if !ok {
		t.Fatalf("cgo: MapNonce missing entry for sender %s", senderKey)
	}
	hwNonce, ok := hwRs.MapNonce[senderKey]
	if !ok {
		t.Fatalf("trustzone-hardware: MapNonce missing entry for sender %s; got keys %v", senderKey, keysOf(hwRs.MapNonce))
	}
	if string(hwNonce) != string(cgoNonce) {
		t.Errorf("MapNonce[sender]: trustzone-hardware=%x, cgo=%x", hwNonce, cgoNonce)
	}
	if new(big.Int).SetBytes(cgoNonce).Cmp(big.NewInt(startNonce+1)) != 0 {
		t.Errorf("cgo MapNonce[sender] = %s, want %d", new(big.Int).SetBytes(cgoNonce), startNonce+1)
	}
}
