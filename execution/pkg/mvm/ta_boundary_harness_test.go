package mvm_test

// B6 of note/tee_core_packaging_plan.md: a harness that drives the real
// mvm+Xapian core (via mvm.ExecutionEngine, B5) using ONLY JSON-serialized
// request/result values — never the original Go objects the caller built.
//
// Why JSON round-trip and not just "call the interface directly": a plain
// Go value copy (e.g. passing a []byte or []common.Address argument as-is)
// can still share the same backing array as the caller's original slice —
// invisible today, but exactly the kind of hidden address-space sharing
// that cannot survive a real TA session boundary later (Normal World and
// Secure World do not share memory; a session command is a byte buffer in,
// a byte buffer out). Marshaling the request to JSON and unmarshaling it
// into a fresh struct forces a genuine deep copy before the engine ever
// sees it, and doing the same to the result on the way out proves the
// engine's OWN output is fully self-contained too — together this is the
// closest thing to "already speaks the TA command shape" this repo can
// verify without real hardware.
//
// SCOPE, HONESTLY STATED: this harness only proves the boundary for
// operations that don't depend on the 6 remaining C++->Go mid-execution
// callbacks documented in note/tee_core_packaging_plan.md section 2B
// (GetBlockHash, GetChainId, GetBlobHash, GetBlobBaseFee,
// GetCrossChainSender, GetCrossChainSourceId) — i.e. it avoids the
// BLOCKHASH/CHAINID/BLOBHASH/BLOBBASEFEE opcodes and cross-chain context.
// Those callbacks reach into ambient global state that this harness
// deliberately does NOT set up (blockchain.GetBlockChainInstance(),
// config.ConfigApp — see GetBlockHash/GetChainId in mvm_api.go) precisely
// because doing so would hide, not prove, the thing B1 needs to fix. Until
// B1 lands, bytecode/txs exercising those opcodes remain unverified by this
// harness on purpose.

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	mt_state "github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// serializeRoundTrip forces v through a real JSON marshal/unmarshal cycle
// and returns the result of unmarshaling into a fresh zero value — never
// the original v. Any field the caller forgot to export, or any slice the
// caller expected to still be shared with the original, breaks loudly here
// instead of silently passing.
func serializeRoundTrip[T any](t *testing.T, v T) T {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("serializeRoundTrip: marshal failed: %v", err)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("serializeRoundTrip: unmarshal failed: %v", err)
	}
	return out
}

// harnessChainState builds a minimal, in-memory ChainState — same
// construction tx_processor's newTestChainState uses — so this harness runs
// as a plain `go test`, no live cluster or NOMT/FFI backend required.
func harnessChainState(t *testing.T) *blockchain.ChainState {
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
		t.Fatalf("harnessChainState: %v", err)
	}
	return cs
}

func harnessSeedAccount(t *testing.T, cs *blockchain.ChainState, addr common.Address, balance *big.Int, nonce uint64) {
	t.Helper()
	as := mt_state.NewAccountState(addr)
	as.AddBalance(balance)
	as.SetNonce(nonce)
	cs.GetAccountStateDB().SetState(as)
}

// ─── Request/result shapes that cross the boundary in this harness ───
//
// Field types are all natively JSON-safe (fixed-size arrays, strings,
// uint64, bool, []byte as base64) — deliberately mirroring what a real TA
// command buffer would carry: no pointers, no interfaces, no maps of
// non-string keys.

type harnessNativeTransferRequest struct {
	From, To           common.Address
	AmountWei          []byte // big-endian
	GasPrice, GasLimit uint64
	BlockNumber        uint64
	BlockCoinbase      common.Address
	MvmId              common.Address
	IsCache            bool
}

type harnessDeployRequest struct {
	Sender      common.Address
	InitCode    []byte
	GasPrice    uint64
	GasLimit    uint64
	BlockNumber uint64
	MvmId       common.Address
	TxHash      common.Hash
}

// TestTABoundary_NativeTransfer_SurvivesSerialization drives a plain value
// transfer (the native fast-path's shape) through the engine using only a
// JSON-round-tripped request, then JSON-round-trips the result before
// asserting on it.
func TestTABoundary_NativeTransfer_SurvivesSerialization(t *testing.T) {
	cs := harnessChainState(t)

	from := common.HexToAddress("0x1111111111111111111111111111111111111a")
	to := common.HexToAddress("0x2222222222222222222222222222222222222b")
	mvmId := common.HexToAddress("0x3333333333333333333333333333333333333c")

	harnessSeedAccount(t, cs, from, big.NewInt(1_000_000), 0)

	amount := big.NewInt(4_242)
	amountBytes := make([]byte, 32)
	amount.FillBytes(amountBytes)

	req := serializeRoundTrip(t, harnessNativeTransferRequest{
		From:        from,
		To:          to,
		AmountWei:   amountBytes,
		GasPrice:    1,
		GasLimit:    21000,
		BlockNumber: 1,
		MvmId:       mvmId,
		IsCache:     false,
	})

	var engine mvm.ExecutionEngine = mvm.GetOrCreateMVMApi(req.MvmId, cs.GetSmartContractDB(), cs.GetAccountStateDB(), false)
	t.Cleanup(func() { mvm.ClearMVMApi(req.MvmId) })

	engine.SetRelatedAddresses([]common.Address{req.From, req.To})

	rs := engine.SendNative(
		req.From.Bytes(), req.To.Bytes(), new(big.Int).SetBytes(req.AmountWei),
		req.GasPrice, req.GasLimit,
		0, 30_000_000, 0, 0, req.BlockNumber, req.BlockCoinbase,
		req.MvmId, req.IsCache,
	)
	if rs == nil {
		t.Fatalf("SendNative returned nil result")
	}

	// Round-trip the RESULT too — proves the output is equally self-
	// contained, not just the input.
	rsWire := serializeRoundTrip(t, *rs)

	if rsWire.Status != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("status = %v, want RECEIPT_STATUS_RETURNED (exmsg=%q)", rsWire.Status, rsWire.Exmsg)
	}

	toKey := hexKey(req.To)
	fromKey := hexKey(req.From)

	gotAdd, ok := rsWire.MapAddBalance[toKey]
	if !ok {
		t.Fatalf("MapAddBalance missing entry for recipient %s; got keys %v", toKey, keysOf(rsWire.MapAddBalance))
	}
	if new(big.Int).SetBytes(gotAdd).Cmp(amount) != 0 {
		t.Errorf("MapAddBalance[recipient] = %s, want %s", new(big.Int).SetBytes(gotAdd), amount)
	}

	gotSub, ok := rsWire.MapSubBalance[fromKey]
	if !ok {
		t.Fatalf("MapSubBalance missing entry for sender %s; got keys %v", fromKey, keysOf(rsWire.MapSubBalance))
	}
	if new(big.Int).SetBytes(gotSub).Cmp(amount) != 0 {
		t.Errorf("MapSubBalance[sender] = %s, want %s", new(big.Int).SetBytes(gotSub), amount)
	}
}

// TestTABoundary_Deploy_SurvivesSerialization deploys a trivial contract
// (constructor copies+returns a runtime body that just returns the constant
// 42, no opcodes touching the still-callback-dependent context) through the
// engine using only a JSON-round-tripped request, then confirms the
// deployed code shows up under the CREATE-derived address in a
// JSON-round-tripped result.
func TestTABoundary_Deploy_SurvivesSerialization(t *testing.T) {
	cs := harnessChainState(t)

	sender := common.HexToAddress("0x4444444444444444444444444444444444444d")
	mvmId := common.HexToAddress("0x5555555555555555555555555555555555555e")
	harnessSeedAccount(t, cs, sender, big.NewInt(1_000_000_000), 0)

	// Runtime body: PUSH1 0x2a PUSH1 0x00 MSTORE PUSH1 0x20 PUSH1 0x00 RETURN
	// (returns the constant 42). Init code: CODECOPY this body out of its
	// own bytecode, then RETURN it — the standard minimal CREATE pattern.
	runtime := []byte{0x60, 0x2a, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}
	initCode := append([]byte{
		0x60, byte(len(runtime)), // PUSH1 <runtime len>
		0x60, 0x0c, // PUSH1 <offset of runtime within initCode = 12>
		0x60, 0x00, // PUSH1 0x00 (destOffset)
		0x39,                     // CODECOPY
		0x60, byte(len(runtime)), // PUSH1 <runtime len>
		0x60, 0x00, // PUSH1 0x00
		0xf3, // RETURN
	}, runtime...)

	req := serializeRoundTrip(t, harnessDeployRequest{
		Sender:      sender,
		InitCode:    initCode,
		GasPrice:    1,
		GasLimit:    200_000,
		BlockNumber: 1,
		MvmId:       mvmId,
		TxHash:      common.HexToHash("0xdeadbeef"),
	})

	var engine mvm.ExecutionEngine = mvm.GetOrCreateMVMApi(req.MvmId, cs.GetSmartContractDB(), cs.GetAccountStateDB(), false)
	t.Cleanup(func() { mvm.ClearMVMApi(req.MvmId) })

	engine.SetRelatedAddresses([]common.Address{req.Sender})

	rs := engine.Deploy(
		req.Sender.Bytes(), req.InitCode, big.NewInt(0),
		req.GasPrice, req.GasLimit,
		0, 30_000_000, 0, 0, req.BlockNumber, common.Address{},
		req.MvmId, req.TxHash.Bytes(),
		false, false, false,
	)
	if rs == nil {
		t.Fatalf("Deploy returned nil result")
	}

	rsWire := serializeRoundTrip(t, *rs)

	if rsWire.Status != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("status = %v, want RECEIPT_STATUS_RETURNED (exmsg=%q)", rsWire.Status, rsWire.Exmsg)
	}

	wantAddr := crypto.CreateAddress(sender, 0)
	gotCode, ok := rsWire.MapCodeChange[hexKey(wantAddr)]
	if !ok {
		t.Fatalf("MapCodeChange missing entry for deployed address %s (CreateAddress(sender, nonce=0)); got keys %v",
			wantAddr.Hex(), keysOf(rsWire.MapCodeChange))
	}
	if string(gotCode) != string(runtime) {
		t.Errorf("deployed code = %x, want %x", gotCode, runtime)
	}
}

func hexKey(addr common.Address) string {
	// Matches helpers.go's extractAddBalance/extractCodeChange etc: plain
	// lowercase hex, no 0x prefix.
	const hextable = "0123456789abcdef"
	b := addr.Bytes()
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hextable[c>>4]
		out[i*2+1] = hextable[c&0x0f]
	}
	return string(out)
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
