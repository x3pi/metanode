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
// SCOPE, UPDATED after B1 landed (note/tee_core_packaging_plan.md section
// 2B): 5 of the 6 C++->Go mid-execution callbacks (GetChainId, GetBlobHash,
// GetBlobBaseFee, GetCrossChainSender, GetCrossChainSourceId) are now
// covered directly by TestTABoundary_ChainId_/_Blob_/_CrossChain_ below,
// each driven through the same serializeRoundTrip as everything else in
// this file. Only GetBlockHash (BLOCKHASH opcode) remains — deliberately
// deferred in B1's own scope (needs an array of up to 256 hashes, not a
// single value) and still reads from the ambient
// blockchain.GetBlockChainInstance() singleton this harness does NOT set
// up. BLOCKHASH-dependent bytecode remains unverified by this harness until
// that deferred piece of B1 lands.

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
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

type harnessCallRequest struct {
	Sender      common.Address
	Contract    common.Address
	Calldata    []byte
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

// TestTABoundary_ChainId_SurvivesSerialization verifies B1's chain_id path
// end-to-end: a constructor that runs CHAINID and returns it must see the
// real chain id delivered via BlockContext (block_context.h), not the
// pre-B1 mid-execution callback — proven by setting a distinctive
// config.ConfigApp.ChainId, driving Deploy through a serialized request,
// and checking the constructor's own RETURN data on the way back out.
func TestTABoundary_ChainId_SurvivesSerialization(t *testing.T) {
	prevConfig := config.ConfigApp
	t.Cleanup(func() { config.ConfigApp = prevConfig })
	config.ConfigApp = &config.SimpleChainConfig{ChainId: big.NewInt(778899)}

	cs := harnessChainState(t)
	sender := common.HexToAddress("0x6666666666666666666666666666666666666f")
	mvmId := common.HexToAddress("0x7777777777777777777777777777777777777a")
	harnessSeedAccount(t, cs, sender, big.NewInt(1_000_000_000), 0)

	// Constructor: CHAINID PUSH1 0x00 MSTORE PUSH1 0x20 PUSH1 0x00 RETURN —
	// returns the 32-byte value CHAINID pushed, nothing else.
	initCode := []byte{0x46, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}

	req := serializeRoundTrip(t, harnessDeployRequest{
		Sender:      sender,
		InitCode:    initCode,
		GasPrice:    1,
		GasLimit:    200_000,
		BlockNumber: 1,
		MvmId:       mvmId,
		TxHash:      common.HexToHash("0xc4a1d"),
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

	// Return on a successful Deploy is the CREATE-derived contract address
	// (confirmed empirically — not the constructor's raw RETURN bytes), so
	// assert on MapCodeChange instead, same as the Deploy test above.
	want := make([]byte, 32)
	big.NewInt(778899).FillBytes(want)
	wantAddr := crypto.CreateAddress(sender, 0)
	gotCode, ok := rsWire.MapCodeChange[hexKey(wantAddr)]
	if !ok {
		t.Fatalf("MapCodeChange missing entry for deployed address %s; got keys %v",
			wantAddr.Hex(), keysOf(rsWire.MapCodeChange))
	}
	if string(gotCode) != string(want) {
		t.Errorf("CHAINID result = %x, want %x", gotCode, want)
	}
}

// TestTABoundary_Blob_SurvivesSerialization verifies B1's blob path
// end-to-end: BLOBHASH(0) inside a constructor must see the versioned hash
// delivered via BlockContext, not the pre-B1 callback — proven the same
// way as the chain-id test above. Also covers BLOBBASEFEE in the same
// constructor, since it's a single-value passthrough structurally identical
// to chain_id's (see buildB1Context in mvm_api.go) rather than needing its
// own separate test.
func TestTABoundary_Blob_SurvivesSerialization(t *testing.T) {
	cs := harnessChainState(t)
	sender := common.HexToAddress("0x8888888888888888888888888888888888888b")
	mvmId := common.HexToAddress("0x9999999999999999999999999999999999999c")
	harnessSeedAccount(t, cs, sender, big.NewInt(1_000_000_000), 0)

	versionedHash := make([]byte, 32)
	versionedHash[0] = 0x01 // EIP-4844 KZG-commitment version byte
	for i := 1; i < 32; i++ {
		versionedHash[i] = byte(i)
	}
	const blobBaseFee = 7

	// Constructor:
	//   PUSH1 0x00 BLOBHASH PUSH1 0x00 MSTORE   -> memory[0:32]  = BLOBHASH(0)
	//   BLOBBASEFEE PUSH1 0x20 MSTORE           -> memory[32:64] = BLOBBASEFEE
	//   PUSH1 0x40 PUSH1 0x00 RETURN            -> return memory[0:64]
	initCode := []byte{
		0x60, 0x00, 0x49, 0x60, 0x00, 0x52,
		0x4a, 0x60, 0x20, 0x52,
		0x60, 0x40, 0x60, 0x00, 0xf3,
	}
	wantOutput := append(append([]byte{}, versionedHash...), make([]byte, 32)...)
	wantOutput[63] = blobBaseFee

	req := serializeRoundTrip(t, harnessDeployRequest{
		Sender:      sender,
		InitCode:    initCode,
		GasPrice:    1,
		GasLimit:    200_000,
		BlockNumber: 1,
		MvmId:       mvmId,
		TxHash:      common.HexToHash("0xb10b"),
	})

	var engine mvm.ExecutionEngine = mvm.GetOrCreateMVMApi(req.MvmId, cs.GetSmartContractDB(), cs.GetAccountStateDB(), false)
	t.Cleanup(func() { mvm.ClearMVMApi(req.MvmId) })
	engine.SetRelatedAddresses([]common.Address{req.Sender})
	engine.SetBlobContext([][]byte{versionedHash}, uint256.NewInt(blobBaseFee))

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

	// Return on a successful Deploy is the CREATE-derived contract address
	// (confirmed empirically — not the constructor's raw RETURN bytes), so
	// assert on MapCodeChange instead, same as the Deploy test above.
	wantAddr := crypto.CreateAddress(sender, 0)
	gotCode, ok := rsWire.MapCodeChange[hexKey(wantAddr)]
	if !ok {
		t.Fatalf("MapCodeChange missing entry for deployed address %s; got keys %v",
			wantAddr.Hex(), keysOf(rsWire.MapCodeChange))
	}
	if string(gotCode) != string(wantOutput) {
		t.Errorf("BLOBHASH(0)+BLOBBASEFEE result = %x, want %x", gotCode, wantOutput)
	}
}

// buildCallWrapperInitCode hand-assembles a constructor that performs a
// low-level CALL(selector-only calldata) against target, then RETURNs
// whatever the sub-call returned — used to reach the cross-chain precompile,
// since it only dispatches from is_precompile()'s check inside a CALL
// opcode's handling (processor.cpp), not from deploy/call/execute's own
// entry point. Because a successful Deploy's Return is the CREATE-derived
// address rather than the constructor's raw RETURN bytes (see the ChainId/
// Blob tests above), returning the sub-call's response INSTALLS it as this
// wrapper's "code" — read back via MapCodeChange, not Return.
func buildCallWrapperInitCode(target common.Address, calldata []byte) []byte {
	if len(calldata) > 32 {
		panic("buildCallWrapperInitCode: calldata must fit in one 32-byte word")
	}
	var code []byte
	push := func(b ...byte) { code = append(code, b...) }

	// PUSH32 <calldata, right-aligned in a 32-byte word> ; PUSH1 0 ; MSTORE
	//   -> memory[0:32] holds it, with the calldata bytes themselves
	//   landing at memory[32-len(calldata) : 32] (PUSH always zero-extends
	//   on the LEFT/high-order side, so a short value's real bytes end up
	//   at the END of the 32-byte word once MSTORE'd at offset 0 — argsOffset
	//   below must account for that, not assume left-alignment).
	padded := make([]byte, 32)
	copy(padded[32-len(calldata):], calldata)
	push(0x7f) // PUSH32
	code = append(code, padded...)
	push(0x60, 0x00) // PUSH1 0 (mstore offset)
	push(0x52)       // MSTORE

	argsOffset := byte(32 - len(calldata))
	// CALL(gas, target, value=0, argsOffset, argsSize=len(calldata), retOffset=0, retSize=0)
	push(0x60, 0x00)               // PUSH1 0   (retSize)
	push(0x60, 0x00)               // PUSH1 0   (retOffset)
	push(0x60, byte(len(calldata))) // PUSH1 argsSize
	push(0x60, argsOffset)          // PUSH1 argsOffset
	push(0x60, 0x00)               // PUSH1 0   (value)
	push(0x73)                     // PUSH20 <target>
	code = append(code, target.Bytes()...)
	push(0x5a) // GAS
	push(0xf1) // CALL
	push(0x50) // POP (discard success flag)
	// RETURNDATACOPY(destOffset=0, offset=0, size=RETURNDATASIZE); RETURN(0, RETURNDATASIZE)
	push(0x3d)       // RETURNDATASIZE
	push(0x60, 0x00) // PUSH1 0 (offset)
	push(0x60, 0x00) // PUSH1 0 (destOffset)
	push(0x3e)       // RETURNDATACOPY
	push(0x3d)       // RETURNDATASIZE
	push(0x60, 0x00) // PUSH1 0 (offset)
	push(0xf3)       // RETURN
	return code
}

// TestTABoundary_CrossChain_SurvivesSerialization verifies B1's
// cross-chain-precompile path end-to-end — the trickiest of the 3, since
// the precompile ABI-encodes both getters as 32-byte values
// (cross_chain_precompile.cpp explicitly checks source_bin.size() == 32),
// which my_global_state.cpp's replacement getters must reproduce exactly
// even though BlockContext itself stores the plain unpadded values.
func TestTABoundary_CrossChain_SurvivesSerialization(t *testing.T) {
	cs := harnessChainState(t)
	mvmId := common.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbe")

	originalSender := common.HexToAddress("0xcccccccccccccccccccccccccccccccccccccf")
	const sourceChainID = uint64(555)

	var engine mvm.ExecutionEngine = mvm.GetOrCreateMVMApi(mvmId, cs.GetSmartContractDB(), cs.GetAccountStateDB(), false)
	t.Cleanup(func() { mvm.ClearMVMApi(mvmId) })
	engine.SetCrossChainContext(originalSender, sourceChainID)
	t.Cleanup(engine.ClearCrossChainContext)

	// Each Deploy call here is fully independent — nonce changes from one
	// call are never committed back into cs before the next, so reusing one
	// sender for 2 deploys would collide on the same CREATE address
	// (confirmed empirically: both would resolve to nonce=0's address).
	// Using 2 distinct senders sidesteps that entirely rather than trying
	// to predict/track a nonce this harness never actually commits.
	callViaWrapper := func(sender common.Address, selectorSig string) []byte {
		t.Helper()
		harnessSeedAccount(t, cs, sender, big.NewInt(1_000_000_000), 0)
		selector := crypto.Keccak256([]byte(selectorSig))[:4]
		initCode := buildCallWrapperInitCode(mt_common.CROSS_CHAIN_CONTRACT_ADDRESS, selector)

		req := serializeRoundTrip(t, harnessDeployRequest{
			Sender:      sender,
			InitCode:    initCode,
			GasPrice:    1,
			GasLimit:    300_000,
			BlockNumber: 1,
			MvmId:       mvmId,
			TxHash:      common.HexToHash("0xc7"),
		})
		engine.SetRelatedAddresses([]common.Address{req.Sender, mt_common.CROSS_CHAIN_CONTRACT_ADDRESS})

		rs := engine.Deploy(
			req.Sender.Bytes(), req.InitCode, big.NewInt(0),
			req.GasPrice, req.GasLimit,
			0, 30_000_000, 0, 0, req.BlockNumber, common.Address{},
			req.MvmId, req.TxHash.Bytes(),
			false, false, false,
		)
		if rs == nil {
			t.Fatalf("Deploy(wrapper for %s) returned nil result", selectorSig)
		}
		rsWire := serializeRoundTrip(t, *rs)
		if rsWire.Status != pb.RECEIPT_STATUS_RETURNED {
			t.Fatalf("Deploy(wrapper for %s) status = %v, want RECEIPT_STATUS_RETURNED (exmsg=%q)",
				selectorSig, rsWire.Status, rsWire.Exmsg)
		}
		wrapperAddr := crypto.CreateAddress(sender, 0)
		got, ok := rsWire.MapCodeChange[hexKey(wrapperAddr)]
		if !ok {
			t.Fatalf("MapCodeChange missing entry for wrapper address %s; got keys %v",
				wrapperAddr.Hex(), keysOf(rsWire.MapCodeChange))
		}
		return got
	}

	gotSender := callViaWrapper(
		common.HexToAddress("0xd1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1"), "getOriginalSender()")
	wantSender := make([]byte, 32)
	copy(wantSender[12:], originalSender.Bytes())
	if string(gotSender) != string(wantSender) {
		t.Errorf("getOriginalSender() = %x, want %x", gotSender, wantSender)
	}

	gotSourceID := callViaWrapper(
		common.HexToAddress("0xd2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2"), "getSourceChainId()")
	wantSourceID := make([]byte, 32)
	big.NewInt(0).SetUint64(sourceChainID).FillBytes(wantSourceID)
	if string(gotSourceID) != string(wantSourceID) {
		t.Errorf("getSourceChainId() = %x, want %x", gotSourceID, wantSourceID)
	}
}

// TestTABoundary_BlockHash_SurvivesSerialization verifies B1's last
// remaining callback, GetBlockHash/BLOCKHASH: a constructor querying
// BLOCKHASH(blockNumber-1) must see the real hash delivered via
// BlockContext.block_hashes, populated only because the constructor
// bytecode contains opcode 0x40 (HasBlockhashOpcode, mvm_api.go) — not the
// pre-B1 callback into blockchain.GetBlockChainInstance(). Seeds the
// package-level BlockChain singleton via cache only (SetBlockNumberToHash
// writes straight to blockNumberToHashCache — no real StorageManager I/O
// needed for GetBlockHashByNumber's cache-hit path).
func TestTABoundary_BlockHash_SurvivesSerialization(t *testing.T) {
	blockchain.InitBlockChain(10, block.NewBlockDatabase(storage.NewDummyStorage("")), storage.NewStorageManager())
	wantHash := common.HexToHash("0xfeed5eed00000000000000000000000000000000000000000000000000005eed")
	if err := blockchain.GetBlockChainInstance().SetBlockNumberToHash(0, wantHash); err != nil {
		t.Fatalf("SetBlockNumberToHash: %v", err)
	}

	cs := harnessChainState(t)
	sender := common.HexToAddress("0xa5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5")
	mvmId := common.HexToAddress("0xa6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6")
	harnessSeedAccount(t, cs, sender, big.NewInt(1_000_000_000), 0)

	// Constructor: PUSH1 0x00 BLOCKHASH PUSH1 0x00 MSTORE PUSH1 0x20
	// PUSH1 0x00 RETURN — queries block 0 (blockNumber-1 below). Using
	// blockNumber=1 keeps fetchRecentBlockHashes's lookback to exactly 1
	// (only block 0), so it never probes an unseeded block number — real
	// chains have no gaps in this range, only this synthetic harness would
	// (a bare storage.NewStorageManager() has no real backing for
	// GetBlockHashByNumber's cache-miss DB fallback to read from).
	initCode := []byte{0x60, 0x00, 0x40, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}

	req := serializeRoundTrip(t, harnessDeployRequest{
		Sender:      sender,
		InitCode:    initCode,
		GasPrice:    1,
		GasLimit:    200_000,
		BlockNumber: 1,
		MvmId:       mvmId,
		TxHash:      common.HexToHash("0xb10c40"),
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
		t.Fatalf("MapCodeChange missing entry for deployed address %s; got keys %v",
			wantAddr.Hex(), keysOf(rsWire.MapCodeChange))
	}
	if string(gotCode) != string(wantHash.Bytes()) {
		t.Errorf("BLOCKHASH(0) result = %x, want %x", gotCode, wantHash.Bytes())
	}
}

// TestHasBlockhashOpcode_DetectsCallFamily is the regression test for the
// code-review finding on TestTABoundary_BlockHash above: HasBlockhashOpcode
// originally only checked for byte 0x40 itself, so a caller with no 0x40 of
// its OWN that reaches BLOCKHASH through a nested CALL into another
// contract would get block_hashes=empty and silently see 0 — a real
// regression from the pre-B1 callback, which worked at any call depth. It
// now also treats any CALL-family opcode as "might reach BLOCKHASH
// downstream" (see its doc comment for why CREATE/CREATE2 are deliberately
// excluded).
//
// A full end-to-end version of this (2 real deployed contracts, a real
// nested CALL) was attempted first but hits a wall unrelated to the fix
// under test: harnessChainState's storage.NewDummyStorage backs SmartContractDB
// with storage whose Get always returns (nil, nil) regardless of what was
// Put/BatchPut/Committed — code manually installed via SetCode+Commit for
// a second contract is therefore never visible to a later Deploy/Call's
// GlobalStateGet callback in this harness, independent of whether
// HasBlockhashOpcode's own logic is correct. A unit test of the scan
// itself is what's actually load-bearing here.
func TestHasBlockhashOpcode_DetectsCallFamily(t *testing.T) {
	cases := []struct {
		name string
		code []byte
		want bool
	}{
		{"empty", nil, false},
		{"plain arithmetic, no BLOCKHASH or CALL", []byte{0x60, 0x01, 0x60, 0x02, 0x01, 0x00}, false},
		{"direct BLOCKHASH", []byte{0x60, 0x00, 0x40}, true},
		{"CALL (0xf1)", []byte{0x60, 0x00, 0xf1}, true},
		{"CALLCODE (0xf2)", []byte{0x60, 0x00, 0xf2}, true},
		{"DELEGATECALL (0xf4)", []byte{0x60, 0x00, 0xf4}, true},
		{"STATICCALL (0xfa)", []byte{0x60, 0x00, 0xfa}, true},
		{"CREATE (0xf0) alone does not count", []byte{0x60, 0x00, 0xf0}, false},
		{"CREATE2 (0xf5) alone does not count", []byte{0x60, 0x00, 0xf5}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mvm.HasBlockhashOpcode(tc.code); got != tc.want {
				t.Errorf("HasBlockhashOpcode(%x) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

// TestTABoundary_NativeLogs_SurvivesSerialization verifies B2 end-to-end: a
// constructor that runs BALANCE (the interpreter's only current
// NativeLogger.LogString call site, processor.cpp's balance()/
// opSelfBalance()) must have its log line show up in the JSON-round-tripped
// MVMExecuteResult.NativeLogs, proving MyLogger's buffer (my_logger.h/.cpp)
// -> ExecuteResult.b_native_logs (mvm_linker.cpp) -> extractNativeLogs
// (helpers.go) pipeline carries it correctly, instead of the pre-B2
// mid-execution GoLogString callback.
func TestTABoundary_NativeLogs_SurvivesSerialization(t *testing.T) {
	cs := harnessChainState(t)
	sender := common.HexToAddress("0xe1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1")
	queried := common.HexToAddress("0xf2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2")
	mvmId := common.HexToAddress("0xe3e3e3e3e3e3e3e3e3e3e3e3e3e3e3e3e3e3e3e3")
	harnessSeedAccount(t, cs, sender, big.NewInt(1_000_000_000), 0)
	harnessSeedAccount(t, cs, queried, big.NewInt(4_321), 0)

	// Constructor: PUSH20 <queried> BALANCE POP PUSH1 0 PUSH1 0 RETURN —
	// triggers processor.cpp's balance()'s NativeLogger.LogString call,
	// then returns empty (the balance value itself isn't what's under test
	// here, only that the log line survived the round trip).
	initCode := append([]byte{0x73}, queried.Bytes()...)
	initCode = append(initCode, 0x31, 0x50, 0x60, 0x00, 0x60, 0x00, 0xf3)

	req := serializeRoundTrip(t, harnessDeployRequest{
		Sender:      sender,
		InitCode:    initCode,
		GasPrice:    1,
		GasLimit:    200_000,
		BlockNumber: 1,
		MvmId:       mvmId,
		TxHash:      common.HexToHash("0x109b0e"),
	})

	var engine mvm.ExecutionEngine = mvm.GetOrCreateMVMApi(req.MvmId, cs.GetSmartContractDB(), cs.GetAccountStateDB(), false)
	t.Cleanup(func() { mvm.ClearMVMApi(req.MvmId) })
	engine.SetRelatedAddresses([]common.Address{req.Sender, queried})

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

	found := false
	for _, entry := range rsWire.NativeLogs {
		if entry.Flag == 0 && strings.Contains(entry.Message, "Balance of") &&
			strings.Contains(strings.ToLower(entry.Message), strings.ToLower(queried.Hex())) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("NativeLogs missing expected BALANCE log line for %s; got %+v", queried.Hex(), rsWire.NativeLogs)
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
