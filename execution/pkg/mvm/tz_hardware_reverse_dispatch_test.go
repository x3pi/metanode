package mvm

// Tests for dispatchReverseCall (tz_hardware_reverse_dispatch.go),
// 2026-08-20, plan §9's "Giai đoạn 3b" step 3. Package mvm (not mvm_test)
// so the unexported dispatcher and codec functions are reachable
// directly, same convention as tz_codec_reverse_callbacks_test.go.
//
// TestDispatchReverseCall_ExtensionCallGetApi_UnsupportedByDesign below
// pins that MVM_TZ_RCMD_EXTENSION_CALL_GET_API is a permanent no-op on
// this bridge (2026-08-20 decision) — it does NOT make a real network
// call, by design, not merely "not implemented yet". See
// tz_hardware_reverse_dispatch.go's own comment on that case, and
// extensionCallGetApiCore's doc comment in extension.go.
//
// Uses the cmd*Test Go constants from tz_hardware_reverse_dispatch.go
// instead of C.MVM_TZ_RCMD_* directly: Go's cgo does not support
// `import "C"` inside a _test.go file at all.

import (
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_state "github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// dispatchTestAddrSeq/dispatchTestAddr and dispatchTestChainState/
// dispatchTestSeedAccount mirror ta_boundary_harness_test.go's
// nextTestAddr/harnessChainState/harnessSeedAccount exactly (same repo,
// same purpose) but can't be reused directly: that file lives in the
// EXTERNAL mvm_test package (so it can import pkg/mvm itself, per its
// own doc comment), while this file needs to stay in the INTERNAL mvm
// package to reach dispatchReverseCall and the codec functions — Go
// gives no way to share unexported helpers across that package boundary,
// so this is a small, deliberate duplication rather than a missed reuse.
var dispatchTestAddrSeq int64

func dispatchTestAddr() common.Address {
	n := atomic.AddInt64(&dispatchTestAddrSeq, 1)
	var addr common.Address
	addr[0] = 0xfd // stays clear of the 1..409 EVM precompile address range
	addr[19] = byte(n)
	addr[18] = byte(n >> 8)
	return addr
}

func dispatchTestChainState(t *testing.T) *blockchain.ChainState {
	t.Helper()
	prevBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	t.Cleanup(func() { trie.SetStateBackend(prevBackend) })

	accountStorage := storage.NewDummyStorage("")
	codeStorage := storage.NewDummyStorage("")
	scStorage := storage.NewDummyStorage("")
	header := block.NewBlockHeader(
		common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{},
		common.Address{}, 0, common.Hash{}, 0,
	)
	cs, err := blockchain.NewChainStateRemote(header, accountStorage, codeStorage, scStorage, map[common.Address]struct{}{})
	if err != nil {
		t.Fatalf("dispatchTestChainState: %v", err)
	}
	return cs
}

func dispatchTestSeedAccount(t *testing.T, cs *blockchain.ChainState, addr common.Address, balance *big.Int, nonce uint64) {
	t.Helper()
	as := mt_state.NewAccountState(addr)
	as.AddBalance(balance)
	as.SetNonce(nonce)
	cs.GetAccountStateDB().SetState(as)
}

// dispatchTestSeedContractAccount is dispatchTestSeedAccount plus
// GetOrCreateSmartContractState() -- getOrCreateSimpleDb/setSimpledb/
// getSimpledb (extension.go) all nil-pointer-dereference on
// AccountState.SmartContractState() for a plain EOA (only a real
// deployed contract has one); the SIMPLE_DATABASE_ADDRESS precompile is
// only ever meant to be called from inside a contract's own execution,
// so tests exercising it need an account state that looks like one.
func dispatchTestSeedContractAccount(t *testing.T, cs *blockchain.ChainState, addr common.Address, balance *big.Int, nonce uint64) {
	t.Helper()
	as := mt_state.NewAccountState(addr)
	as.AddBalance(balance)
	as.SetNonce(nonce)
	as.(*mt_state.AccountState).GetOrCreateSmartContractState()
	cs.GetAccountStateDB().SetState(as)
}

// packStringArgs ABI-encodes calldata for a function taking N "string"
// args, prefixed with a 4-byte placeholder selector — none of the
// dispatcher branches under test here verify the selector, matching
// extension.go's own real handlers (they only ever skip bCallData[:4]).
func packStringArgs(t *testing.T, args ...string) []byte {
	t.Helper()
	strType, err := abi.NewType("string", "", nil)
	if err != nil {
		t.Fatalf("packStringArgs: abi.NewType: %v", err)
	}
	var arguments abi.Arguments
	vals := make([]interface{}, len(args))
	for i, a := range args {
		arguments = append(arguments, abi.Argument{Type: strType})
		vals[i] = a
	}
	packed, err := arguments.Pack(vals...)
	if err != nil {
		t.Fatalf("packStringArgs: Pack: %v", err)
	}
	return append([]byte{0xde, 0xad, 0xbe, 0xef}, packed...)
}

// packSimpleDbCall is packStringArgs with a REAL selector (via `cast
// sig`, matching extension.go's real ABI -- see plan §9.31's own
// selector table) instead of a placeholder, since
// extensionGetOrCreateSimpleDbCore DOES dispatch on selector (unlike the
// 3 bytes-only extension commands).
func packSimpleDbCall(t *testing.T, selector uint32, args ...string) []byte {
	t.Helper()
	full := packStringArgs(t, args...)
	full[0] = byte(selector >> 24)
	full[1] = byte(selector >> 16)
	full[2] = byte(selector >> 8)
	full[3] = byte(selector)
	return full
}

const (
	simpleDbGetOrCreateSelector uint32 = 0x84243b93 // getOrCreateSimpleDb(string)
	simpleDbSetSelector         uint32 = 0xda465d74 // set(string,string,string)
	simpleDbGetSelector         uint32 = 0x3e10510b // get(string,string)
)

// packBytesArgs is packStringArgs's "bytes" sibling, needed for BLST's
// verifySign(bytes,bytes,bytes) — a 48-byte pubkey and a 96-byte
// signature are both multi-word dynamic bytes, which packStringArgs's Go
// arg type (string) can't represent directly.
func packBytesArgs(t *testing.T, args ...[]byte) []byte {
	t.Helper()
	bytesType, err := abi.NewType("bytes", "", nil)
	if err != nil {
		t.Fatalf("packBytesArgs: abi.NewType: %v", err)
	}
	var arguments abi.Arguments
	vals := make([]interface{}, len(args))
	for i, a := range args {
		arguments = append(arguments, abi.Argument{Type: bytesType})
		vals[i] = a
	}
	packed, err := arguments.Pack(vals...)
	if err != nil {
		t.Fatalf("packBytesArgs: Pack: %v", err)
	}
	return append([]byte{0xee, 0x57, 0xfa, 0x59}, packed...) // real verifySign(bytes,bytes,bytes) selector
}

// unpackSingleString decodes the standard ABI single-"string"-return
// shape (offset+length+data) that argument_encode.EncodeSingleString
// (and therefore extensionExtractJsonFieldCore/getSimpledb's real ABI
// pack) produce.
func unpackSingleString(t *testing.T, out []byte) string {
	t.Helper()
	strType, err := abi.NewType("string", "", nil)
	if err != nil {
		t.Fatalf("unpackSingleString: abi.NewType: %v", err)
	}
	vals, err := (abi.Arguments{{Type: strType}}).Unpack(out)
	if err != nil {
		t.Fatalf("unpackSingleString: Unpack: %v (out=%x)", err, out)
	}
	s, _ := vals[0].(string)
	return s
}

func TestDispatchReverseCall_GlobalStateGet_RealAccount(t *testing.T) {
	cs := dispatchTestChainState(t)
	mvmId := dispatchTestAddr()
	addr := dispatchTestAddr()
	dispatchTestSeedAccount(t, cs, addr, big.NewInt(1_000_000), 7)

	GetOrCreateMVMApi(mvmId, cs.GetSmartContractDB(), cs.GetAccountStateDB(), false)
	t.Cleanup(func() { ClearMVMApi(mvmId) })

	header := encodeGlobalStateGetReq(mvmId, addr)
	respHeader, respBlob := dispatchReverseCall(cmdGlobalStateGetTest, header, nil)

	status, balance, nonce, _, err := decodeGlobalStateGetResp(respHeader, respBlob)
	if err != nil {
		t.Fatalf("decodeGlobalStateGetResp: %v", err)
	}
	if status != 1 {
		t.Fatalf("status = %d, want 1 (found)", status)
	}
	wantBalance := new(big.Int).SetBytes(balance)
	if wantBalance.Cmp(big.NewInt(1_000_000)) != 0 {
		t.Errorf("balance = %s, want 1000000", wantBalance.String())
	}
	wantNonce := new(big.Int).SetBytes(nonce)
	if wantNonce.Cmp(big.NewInt(7)) != 0 {
		t.Errorf("nonce = %s, want 7", wantNonce.String())
	}
}

func TestDispatchReverseCall_GlobalStateGet_UnknownMvmId(t *testing.T) {
	// mvmId never passed to GetOrCreateMVMApi -> GetMVMApi returns nil ->
	// globalStateGetCore's own documented status=0 ("not found, create
	// fresh") early return.
	header := encodeGlobalStateGetReq(dispatchTestAddr(), dispatchTestAddr())
	respHeader, respBlob := dispatchReverseCall(cmdGlobalStateGetTest, header, nil)

	status, _, _, _, err := decodeGlobalStateGetResp(respHeader, respBlob)
	if err != nil {
		t.Fatalf("decodeGlobalStateGetResp: %v", err)
	}
	if status != 0 {
		t.Fatalf("status = %d, want 0 (not found)", status)
	}
}

func TestDispatchReverseCall_GetStorageValue_UnknownMvmId(t *testing.T) {
	// Same "never registered" case as above -- documented quirk (see
	// getStorageValueCore's own doc comment): status comes back 0
	// (StorageStatusSuccess), not a distinguishable not-found/error
	// status. Asserting it here pins the quirk so a future change to it
	// is a deliberate diff, not a silent regression.
	header := encodeGetStorageValueReq(dispatchTestAddr(), dispatchTestAddr(), make([]byte, 32))
	respHeader, respBlob := dispatchReverseCall(cmdGetStorageValueTest, header, nil)

	status, _, err := decodeGetStorageValueResp(respHeader, respBlob)
	if err != nil {
		t.Fatalf("decodeGetStorageValueResp: %v", err)
	}
	if status != 0 {
		t.Fatalf("status = %d, want 0 (documented nil-mvmApi quirk)", status)
	}
}

func TestDispatchReverseCall_ExtensionCallGetApi_UnsupportedByDesign(t *testing.T) {
	// Pins the 2026-08-20 decision: EXTENSION_CALL_GET_API is a permanent
	// no-op on the hardware bridge, not "not implemented yet". A URL calldata
	// that WOULD make a real HTTP GET if extensionCallGetApiCore were ever
	// invoked here (it deliberately is not -- if this test ever needed
	// network access to pass, that would itself be the regression) --
	// asserting an empty response is enough to prove the dispatcher took
	// the unsupported branch instead of the real-HTTP one.
	calldata := []byte("http://169.254.169.254/should-never-be-fetched")
	respHeader, respBlob := dispatchReverseCall(cmdExtensionCallGetApiTest, nil, calldata)

	if respHeader != nil {
		t.Fatalf("respHeader = %v, want nil", respHeader)
	}
	out := decodeExtensionBytesResp(respBlob)
	if out != nil {
		t.Fatalf("output = %v, want nil (unsupported-by-design empty response)", out)
	}
}

func TestDispatchReverseCall_ExtractJsonField(t *testing.T) {
	calldata := packStringArgs(t, `{"status":"ok","value":123,"flag":true}`, "value")
	_, respBlob := dispatchReverseCall(cmdExtensionExtractJsonFieldTest, nil, calldata)

	out := decodeExtensionBytesResp(respBlob)
	if got := unpackSingleString(t, out); got != "123" {
		t.Fatalf("extracted field = %q, want %q", got, "123")
	}
}

func TestDispatchReverseCall_Blst_VerifySign(t *testing.T) {
	kp := bls.GenerateKeyPair()
	msg := []byte("dispatchReverseCall BLST test message")
	sig := bls.Sign(kp.PrivateKey(), msg)

	calldata := packBytesArgs(t, kp.BytesPublicKey(), sig.Bytes(), msg)
	_, respBlob := dispatchReverseCall(cmdExtensionBlstTest, nil, calldata)

	out := decodeExtensionBytesResp(respBlob)
	boolType, _ := abi.NewType("bool", "", nil)
	vals, err := (abi.Arguments{{Type: boolType}}).Unpack(out)
	if err != nil {
		t.Fatalf("unpack bool result: %v (out=%x)", err, out)
	}
	if ok, _ := vals[0].(bool); !ok {
		t.Fatalf("verifySign result = false, want true (real BLS keypair/signature)")
	}
}

// TestDispatchReverseCall_GetOrCreateSimpleDb_UnrecognizedSelector covers
// the dispatcher's decode -> core -> encode plumbing for this cmd without
// needing trie_database.GetTrieDatabaseManager() -- a real, separate,
// sync.Once-guarded process-global singleton that only production init
// code (cmd/simple_chain/app_blockchain.go etc.) ever constructs. A real
// set(...)/get(...) round trip needs that singleton AND an account whose
// AccountState.SmartContractState() is non-nil (setSimpledb/getSimpledb
// both dereference it unconditionally -- a real precondition of this
// precompile: it's only ever meant to be called from inside a contract's
// own execution, never a plain EOA) -- both are real integration-level
// setup out of scope for a dispatcher-plumbing unit test; exercising
// them properly belongs in a future test that boots that singleton
// deliberately, not as a side effect of testing the wire codec here.
func TestDispatchReverseCall_GetOrCreateSimpleDb_UnrecognizedSelector(t *testing.T) {
	addr, mvmId := dispatchTestAddr(), dispatchTestAddr()
	calldata := packStringArgs(t, "db1", "key1", "hello_dispatch") // placeholder 0xdeadbeef selector, matches no real method
	header, blob := encodeGetOrCreateSimpleDbReq(addr, mvmId, calldata)
	_, respBlob := dispatchReverseCall(cmdExtensionGetOrCreateSimpleDbTest, header, blob)
	if decodeGetOrCreateSimpleDbResp(respBlob) != nil {
		t.Fatalf("expected nil output (Extension_return{nullptr,0}) for an unrecognized selector")
	}
}

func TestDispatchReverseCall_GetLatestFullDbLogs_EmptyIsValid(t *testing.T) {
	header := encodeGetLatestFullDbLogsReq(dispatchTestAddr())
	respHeader, respBlob := dispatchReverseCall(cmdGetLatestFullDbLogsTest, header, nil)

	logs, err := decodeReplayFullDbLogsResp(respHeader, respBlob)
	if err != nil {
		t.Fatalf("decodeReplayFullDbLogsResp: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("logs = %v, want empty (auto-trigger deliberately not wired yet)", logs)
	}
}

func TestDispatchReverseCall_UnrecognizedCmd_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("dispatchReverseCall panicked on an unrecognized cmd: %v", r)
		}
	}()
	// MVM_TZ_CMD_CALL (a real forward-command id, never a valid reverse
	// cmd) stands in for "something dispatchReverseCall's switch doesn't
	// recognize" without needing an out-of-range literal.
	respHeader, respBlob := dispatchReverseCall(cmdCallTest, nil, nil)
	if respHeader != nil || respBlob != nil {
		t.Fatalf("expected nil, nil for an unrecognized cmd, got header=%x blob=%x", respHeader, respBlob)
	}
}
