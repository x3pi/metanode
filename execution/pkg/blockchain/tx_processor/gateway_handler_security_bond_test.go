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

// ══════════════════════════════════════════════════════════════════════════════
// Phase A — SecurityBond ABI wiring
// (note/cross_chain/root_anchor_production_security_hardening_plan.md)
//
// The core ledger mechanics (checkBondCap, forfeitBond, unbonding, SlashOnEquivocation
// verification) are already covered exhaustively at the engine level in
// pkg/cross_chain/security_bond_test.go. These tests prove the ONE thing that level can't:
// the real AccountStateDB burn/mint wiring through gateway_handler.go's ABI dispatch.
// ══════════════════════════════════════════════════════════════════════════════

func mustPackGetSecurityBond(t *testing.T, h *GatewayHandler, chainID uint64) []byte {
	t.Helper()
	data, err := h.abi.Pack("getSecurityBond", new(big.Int).SetUint64(chainID))
	if err != nil {
		t.Fatalf("pack getSecurityBond: %v", err)
	}
	return marshalCallData(t, data)
}

func mustPackGetUnbondingRequest(t *testing.T, h *GatewayHandler, chainID uint64) []byte {
	t.Helper()
	data, err := h.abi.Pack("getUnbondingRequest", new(big.Int).SetUint64(chainID))
	if err != nil {
		t.Fatalf("pack getUnbondingRequest: %v", err)
	}
	return marshalCallData(t, data)
}

func TestGatewayHandler_PostSecurityBond_BurnsRealBalance(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	engine := cross_chain.NewGatewayEngine(991, map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Epoch: 0},
	}, nil)
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine: %v", err)
	}

	caller := common.HexToAddress("0xB0000000B0000000B0000000B0000000B0000000")
	if err := cs.GetAccountStateDB().AddBalance(caller, big.NewInt(10_000)); err != nil {
		t.Fatalf("AddBalance: %v", err)
	}

	calldata, err := h.abi.Pack("postSecurityBond", new(big.Int).SetUint64(101), big.NewInt(4_000))
	if err != nil {
		t.Fatalf("pack postSecurityBond: %v", err)
	}
	tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	if rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); failed {
		reason := ""
		if rcp != nil {
			reason = string(rcp.Return())
		}
		t.Fatalf("postSecurityBond failed: %q", reason)
	}

	as, err := cs.GetAccountStateDB().AccountState(caller)
	if err != nil || as == nil || as.Balance().Cmp(big.NewInt(6_000)) != 0 {
		t.Fatalf("expected caller balance to be debited to 6000, got %v (err=%v)", as.Balance(), err)
	}

	viewTx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), mustPackGetSecurityBond(t, h, 101))
	bondData, err := h.HandleOffChainQuery(cs, viewTx)
	if err != nil {
		t.Fatalf("getSecurityBond failed: %v", err)
	}
	outValues, err := h.abi.Unpack("getSecurityBond", bondData)
	if err != nil {
		t.Fatalf("unpack getSecurityBond: %v", err)
	}
	gotBond, _ := outValues[0].(*big.Int)
	if gotBond == nil || gotBond.Cmp(big.NewInt(4_000)) != 0 {
		t.Fatalf("getSecurityBond = %v, want 4000", gotBond)
	}
}

func TestGatewayHandler_PostSecurityBond_InsufficientBalanceFailsClosed(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	engine := cross_chain.NewGatewayEngine(991, map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Epoch: 0},
	}, nil)
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine: %v", err)
	}

	caller := common.HexToAddress("0xB1111111B1111111B1111111B1111111B1111111")
	if err := cs.GetAccountStateDB().AddBalance(caller, big.NewInt(100)); err != nil {
		t.Fatalf("AddBalance: %v", err)
	}

	calldata, err := h.abi.Pack("postSecurityBond", new(big.Int).SetUint64(101), big.NewInt(4_000))
	if err != nil {
		t.Fatalf("pack postSecurityBond: %v", err)
	}
	tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); !failed {
		t.Fatalf("expected postSecurityBond to fail closed with insufficient real balance")
	}

	as, err := cs.GetAccountStateDB().AccountState(caller)
	if err != nil || as == nil || as.Balance().Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("expected caller balance to remain untouched at 100, got %v", as.Balance())
	}
	reloaded, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatalf("loadGatewayEngine: %v", err)
	}
	if reloaded.SecurityBond != nil && reloaded.SecurityBond.Bond[101] != nil && reloaded.SecurityBond.Bond[101].Sign() > 0 {
		t.Fatalf("expected no bond to have been recorded on a failed deposit, got %v", reloaded.SecurityBond.Bond[101])
	}
}

// TestGatewayHandler_ClaimUnbondedBond_CreditsRealBalanceAfterUnbondingPeriod is the end-to-end
// regression test for the "hit and run" fix: unregistering a chain does NOT release its bond
// immediately, and claiming too early fails closed; only after the unbonding period elapses does
// a real native-coin credit land in the chain's recorded GenesisWallet.
func TestGatewayHandler_ClaimUnbondedBond_CreditsRealBalanceAfterUnbondingPeriod(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	chainKP := bls.GenerateKeyPair()
	chainPop := cross_chain.PopSign(chainKP.PrivateKey(), chainKP.PublicKey())
	genesisWallet := common.HexToAddress("0xC2222222C2222222C2222222C2222222C2222222")

	engine := cross_chain.NewGatewayEngine(991, map[uint64]cross_chain.ChainRegistry{
		101: {
			ChainID: 101, Epoch: 0, GenesisWallet: genesisWallet,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: chainKP.BytesPublicKey(), Stake: 10000, PopSignature: chainPop.Bytes()},
			},
		},
	}, nil)
	engine.UnbondingPeriodSeconds = 1000
	if err := engine.PostSecurityBond(101, big.NewInt(2_000)); err != nil {
		t.Fatalf("seed PostSecurityBond: %v", err)
	}
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine: %v", err)
	}

	caller := common.HexToAddress("0xC3333333C3333333C3333333C3333333C3333333")

	// Unregister at blockTime=10_000 -- starts the unbonding clock.
	// Self-authorized by chain 101's own committee key (nonce 0, registry epoch 0).
	digest := cross_chain.ComputeUnregisterChainMessage(101, 0, 0)
	sig := bls.Sign(chainKP.PrivateKey(), digest)
	unregCalldata, err := h.abi.Pack("unregisterChainWithCert", new(big.Int).SetUint64(101), uint64(0), uint64(0), sig.Bytes(), []byte{0x01})
	if err != nil {
		t.Fatalf("pack unregisterChainWithCert: %v", err)
	}
	unregTx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, unregCalldata))
	if rcp, _, failed := h.HandleTransaction(context.Background(), cs, unregTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 10_000); failed {
		reason := ""
		if rcp != nil {
			reason = string(rcp.Return())
		}
		t.Fatalf("unregisterChainWithCert failed: %q", reason)
	}

	// getUnbondingRequest must now report the pending withdrawal.
	viewTx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), mustPackGetUnbondingRequest(t, h, 101))
	reqData, err := h.HandleOffChainQuery(cs, viewTx)
	if err != nil {
		t.Fatalf("getUnbondingRequest failed: %v", err)
	}
	outValues, err := h.abi.Unpack("getUnbondingRequest", reqData)
	if err != nil {
		t.Fatalf("unpack getUnbondingRequest: %v", err)
	}
	exists, _ := outValues[0].(bool)
	amount, _ := outValues[1].(*big.Int)
	if !exists || amount == nil || amount.Cmp(big.NewInt(2_000)) != 0 {
		t.Fatalf("expected a pending unbonding request of 2000, got exists=%v amount=%v", exists, amount)
	}

	// Claiming too early (blockTime=10_500, < ReleaseAt=11_000) must fail closed.
	claimCalldata, err := h.abi.Pack("claimUnbondedBond", new(big.Int).SetUint64(101))
	if err != nil {
		t.Fatalf("pack claimUnbondedBond: %v", err)
	}
	tooEarlyTx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 1, big.NewInt(0), marshalCallData(t, claimCalldata))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, tooEarlyTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 10_500); !failed {
		t.Fatalf("expected claimUnbondedBond to fail closed before the unbonding period elapses")
	}

	// After the period (blockTime=11_000) -- must succeed and credit the REAL native balance of
	// the chain's recorded GenesisWallet (not the caller who happened to submit this tx).
	onTimeTx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 2, big.NewInt(0), marshalCallData(t, claimCalldata))
	if rcp, _, failed := h.HandleTransaction(context.Background(), cs, onTimeTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 11_000); failed {
		reason := ""
		if rcp != nil {
			reason = string(rcp.Return())
		}
		t.Fatalf("claimUnbondedBond failed: %q", reason)
	}

	as, err := cs.GetAccountStateDB().AccountState(genesisWallet)
	if err != nil || as == nil || as.Balance().Cmp(big.NewInt(2_000)) != 0 {
		t.Fatalf("expected genesisWallet balance to be credited to 2000, got %v (err=%v)", as.Balance(), err)
	}
}

func TestGatewayHandler_UnregisterChainWithCert_FailsOnBadCert(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	chainKP := bls.GenerateKeyPair()
	chainPop := cross_chain.PopSign(chainKP.PrivateKey(), chainKP.PublicKey())
	genesisWallet := common.HexToAddress("0xC2222222C2222222C2222222C2222222C2222222")

	engine := cross_chain.NewGatewayEngine(991, map[uint64]cross_chain.ChainRegistry{
		102: {
			ChainID: 102, Epoch: 0, GenesisWallet: genesisWallet,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: chainKP.BytesPublicKey(), Stake: 10000, PopSignature: chainPop.Bytes()},
			},
		},
	}, nil)
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine: %v", err)
	}

	caller := common.HexToAddress("0xC3333333C3333333C3333333C3333333C3333333")

	// Create a BAD cert (wrong private key)
	badKP := bls.GenerateKeyPair()
	digest := cross_chain.ComputeUnregisterChainMessage(102, 0, 0)
	badSig := bls.Sign(badKP.PrivateKey(), digest)
	unregCalldata, err := h.abi.Pack("unregisterChainWithCert", new(big.Int).SetUint64(102), uint64(0), uint64(0), badSig.Bytes(), []byte{0x01})
	if err != nil {
		t.Fatalf("pack unregisterChainWithCert: %v", err)
	}

	unregTx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, unregCalldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, unregTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 10_000)
	if !failed {
		t.Fatalf("expected unregisterChainWithCert to fail closed due to invalid signature")
	}
	if rcp == nil || len(rcp.Return()) == 0 {
		t.Fatalf("a rejected unregister must carry a reason in the receipt")
	}

	// Fail closed means NO state change: the chain stays registered and its unregister nonce is not consumed.
	reloaded, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatalf("loadGatewayEngine: %v", err)
	}
	if _, stillRegistered := reloaded.ChainRegistry[102]; !stillRegistered {
		t.Fatalf("chain 102 must remain registered after a rejected unregister")
	}
	if n := reloaded.UnregisterNonce[102]; n != 0 {
		t.Fatalf("a rejected unregister must not consume the nonce, got %d", n)
	}
}
