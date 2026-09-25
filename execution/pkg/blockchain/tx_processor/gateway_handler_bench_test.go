package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

// BenchmarkGatewayHandler_Outbound measures a VALID outbound() call through
// GatewayHandler.HandleTransaction (ABI decode, engine load, business logic, engine save, receipt).
// Every iteration must succeed (the loop fails otherwise), so this cannot silently measure an
// early-reject path. Each call adds one message to the engine, so the per-call cost includes the
// growth of the persisted engine blob: run with several -benchtime=Nx values to see how the cost
// depends on the amount of accumulated Gateway state (Gateway calls are barrier transactions and
// therefore execute strictly sequentially).
func BenchmarkGatewayHandler_Outbound(b *testing.B) {
	cs, _, _, _ := newPersistentTestChainState(b)
	h, err := GetGatewayHandler()
	if err != nil {
		b.Fatalf("GetGatewayHandler: %v", err)
	}

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	if err := cs.GetAccountStateDB().AddBalance(sender, new(big.Int).Lsh(big.NewInt(1), 100)); err != nil {
		b.Fatalf("AddBalance: %v", err)
	}
	engine, err := loadGatewayEngine(cs)
	if err != nil {
		b.Fatalf("loadGatewayEngine: %v", err)
	}
	engine.ChainRegistry[102] = cross_chain.ChainRegistry{
		ChainID: 102,
		Epoch:   1,
		Committee: []cross_chain.ValidatorEntry{
			{PubkeyBLS: bls.GenerateKeyPair().BytesPublicKey(), Stake: 1000},
		},
	}
	if err := saveGatewayEngine(cs, engine); err != nil {
		b.Fatalf("saveGatewayEngine: %v", err)
	}

	calldata, err := h.abi.Pack("outbound",
		big.NewInt(102), target, []byte{0xAA, 0xBB}, big.NewInt(0), big.NewInt(100), big.NewInt(5), big.NewInt(0),
		uint8(1), false, uint64(0),
	)
	if err != nil {
		b.Fatalf("pack outbound: %v", err)
	}
	dataBytes, err := transaction.NewCallData(calldata).Marshal()
	if err != nil {
		b.Fatalf("marshal CallData: %v", err)
	}

	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, uint64(i), big.NewInt(0), dataBytes)
		rcp, _, failed := h.HandleTransaction(ctx, cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		if failed {
			reason := ""
			if rcp != nil {
				reason = string(rcp.Return())
			}
			b.Fatalf("outbound failed at iteration %d: %q", i, reason)
		}
	}
}
