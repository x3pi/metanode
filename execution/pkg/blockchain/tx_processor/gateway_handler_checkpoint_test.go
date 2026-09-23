package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

// TestGatewayHandler_SubmitCheckpoint_EndToEnd is the ABI-dispatch regression test for Phase B
// tầng 1 (note/cross_chain/root_anchor_production_security_hardening_plan.md) -- the engine-level
// mechanics are already covered exhaustively in pkg/cross_chain/checkpoint_test.go, this proves
// the ABI wiring (submitCheckpoint write dispatch + getCheckpoint view dispatch) works end-to-end.
func TestGatewayHandler_SubmitCheckpoint_EndToEnd(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	kp := bls.GenerateKeyPair()
	pop := cross_chain.PopSign(kp.PrivateKey(), kp.PublicKey())
	engine := cross_chain.NewGatewayEngine(991, map[uint64]cross_chain.ChainRegistry{
		101: {
			ChainID:   101,
			Epoch:     5,
			Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 10000, PopSignature: pop.Bytes()}},
		},
	}, nil)
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine: %v", err)
	}

	stateRoot := common.HexToHash("0x1234123412341234123412341234123412341234123412341234123412341234")
	valSetHash := common.HexToHash("0x5678567856785678567856785678567856785678567856785678567856785678")
	digest := cross_chain.ComputeCheckpointMessage(101, 5, 42, stateRoot, valSetHash)
	sig := bls.Sign(kp.PrivateKey(), digest)

	sender := common.HexToAddress("0xD0000000D0000000D0000000D0000000D0000000")
	calldata, err := h.abi.Pack("submitCheckpoint",
		new(big.Int).SetUint64(101), uint64(5), uint64(42), stateRoot, valSetHash,
		uint64(5), sig.Bytes(), []byte{0x01},
	)
	if err != nil {
		t.Fatalf("pack submitCheckpoint: %v", err)
	}
	tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	if rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 77_000); failed {
		reason := ""
		if rcp != nil {
			reason = string(rcp.Return())
		}
		t.Fatalf("submitCheckpoint failed: %q", reason)
	}

	getCalldata, err := h.abi.Pack("getCheckpoint", new(big.Int).SetUint64(101))
	if err != nil {
		t.Fatalf("pack getCheckpoint: %v", err)
	}
	viewTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, getCalldata))
	data, err := h.HandleOffChainQuery(cs, viewTx)
	if err != nil {
		t.Fatalf("getCheckpoint failed: %v", err)
	}
	outValues, err := h.abi.Unpack("getCheckpoint", data)
	if err != nil {
		t.Fatalf("unpack getCheckpoint: %v", err)
	}
	exists, _ := outValues[0].(bool)
	epoch, _ := outValues[1].(uint64)
	blockHeight, _ := outValues[2].(uint64)
	gotStateRoot, _ := outValues[3].([32]byte)
	gotValSetHash, _ := outValues[4].([32]byte)
	submittedAt, _ := outValues[5].(uint64)

	if !exists {
		t.Fatal("expected getCheckpoint to report exists=true after a successful submitCheckpoint")
	}
	if epoch != 5 || blockHeight != 42 || submittedAt != 77_000 {
		t.Fatalf("getCheckpoint = epoch=%d blockHeight=%d submittedAt=%d, want 5/42/77000", epoch, blockHeight, submittedAt)
	}
	if common.Hash(gotStateRoot) != stateRoot || common.Hash(gotValSetHash) != valSetHash {
		t.Fatalf("getCheckpoint stateRoot/validatorSetHash mismatch")
	}
}

// TestGatewayHandler_SubmitCheckpoint_RejectsNonMonotonicReplay proves the ABI path rejects an
// attempted replay of a stale checkpoint exactly like the engine-level test does, end to end.
func TestGatewayHandler_SubmitCheckpoint_RejectsNonMonotonicReplay(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	kp := bls.GenerateKeyPair()
	pop := cross_chain.PopSign(kp.PrivateKey(), kp.PublicKey())
	engine := cross_chain.NewGatewayEngine(991, map[uint64]cross_chain.ChainRegistry{
		101: {
			ChainID:   101,
			Epoch:     0,
			Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 10000, PopSignature: pop.Bytes()}},
		},
	}, nil)
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine: %v", err)
	}

	sender := common.HexToAddress("0xD1111111D1111111D1111111D1111111D1111111")
	sign := func(blockHeight uint64) []byte {
		digest := cross_chain.ComputeCheckpointMessage(101, 0, blockHeight, common.Hash{}, common.Hash{})
		return bls.Sign(kp.PrivateKey(), digest).Bytes()
	}
	pack := func(blockHeight uint64) []byte {
		calldata, err := h.abi.Pack("submitCheckpoint",
			new(big.Int).SetUint64(101), uint64(0), blockHeight, common.Hash{}, common.Hash{},
			uint64(0), sign(blockHeight), []byte{0x01},
		)
		if err != nil {
			t.Fatalf("pack submitCheckpoint: %v", err)
		}
		return marshalCallData(t, calldata)
	}

	tx1 := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), pack(100))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, tx1, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 1); failed {
		t.Fatalf("first submitCheckpoint at height 100 unexpectedly failed")
	}

	tx2 := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 1, big.NewInt(0), pack(100))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, tx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 2); !failed {
		t.Fatalf("expected a replayed checkpoint at the same height to be rejected")
	}
}
