package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// A barrier tx (gateway outbound) that moves tokens in a token contract must not be undone by the MVCC write set of an
// earlier normal EVM tx in the same block that touched the same storage slots. Before the fix, TrueBlockSTM kept its MVCC
// maps across barrier txs, so the final commitToBase wrote the pre-barrier values back: the holder's balance debit of the
// outbound disappeared while the gateway kept the locked tokens (tokens created from nothing), and the allowance the
// outbound consumed was restored.

func realTokenAllowanceOf(t *testing.T, cs *blockchain.ChainState, token, owner, spender common.Address) *big.Int {
	t.Helper()
	// allowance(owner, spender) is a public mapping getter on the fixture token
	return tokenViewCall(t, cs, token, "allowance", owner, spender)
}

func tokenViewCall(t *testing.T, cs *blockchain.ChainState, token common.Address, method string, args ...interface{}) *big.Int {
	t.Helper()
	parsedABI := testWrappedAssetABI(t)
	calldata, err := parsedABI.Pack(method, args...)
	if err != nil {
		t.Fatalf("pack %s: %v", method, err)
	}
	caller := common.HexToAddress("0xCA11CA11CA11CA11CA11CA11CA11CA11CA11CA1")
	callTx := newHighGasTx(caller, token, 0, big.NewInt(0), calldata)
	_, mvmE := createVmProcessorForGateway(context.Background(), cs, callTx, 0)
	h := *cs.GetcurrentBlockHeader()
	leader := h.LeaderAddress()
	if leader == (common.Address{}) {
		leader = caller
	}
	res := mvmE.Execute(caller.Bytes(), token.Bytes(), calldata, big.NewInt(0), callTx.MaxGasPrice(), callTx.MaxGas(), h.TimeStamp(), mt_common.BLOCK_GAS_LIMIT,
		uint64(0), mt_common.MINIMUM_BASE_FEE, h.BlockNumber()+1, leader, mvmE.GetKey(), callTx.Hash().Bytes(), []common.Address{}, false, false)
	out, err := parsedABI.Unpack(method, res.Return)
	if err != nil {
		t.Fatalf("unpack %s: %v (status %v)", method, err, res.Status)
	}
	return out[0].(*big.Int)
}

func TestTrueBlockSTM_BarrierTxChangesToTokenSlotsSurviveEarlierNormalEVMWrites(t *testing.T) {
	barrierContractStorage = true
	defer func() { barrierContractStorage = false }()
	type outcome struct{ holder, gateway, allowance string }
	run := func(mode string) outcome {
		env, cs := barrierSetup(t)
		assetTweak(t, env, cs)
		gh, err := GetGatewayHandler()
		if err != nil {
			t.Fatal(err)
		}
		tokenABI := testWrappedAssetABI(t)
		holder := env.evm[0].addr
		wrap := func(in []byte, err error) []byte {
			if err != nil {
				t.Fatal(err)
			}
			d, err := transaction.NewCallData(in).Marshal()
			if err != nil {
				t.Fatal(err)
			}
			return d
		}
		nonce := uint64(1) // the holder deployed the token with nonce 0
		var txs []mt_types.Transaction
		add := func(to common.Address, data []byte) {
			txs = append(txs, transaction.NewTransaction(holder, to, big.NewInt(0), 600_000, 1, uint64(9600+nonce), data, nil, common.Hash{}, common.Hash{}, nonce, 991))
			nonce++
		}
		// normal tx 1 writes the holder's allowance slot, normal tx 2 the holder's balance slot, then a barrier tx moves both
		add(assetTokens.home8, wrap(tokenABI.Pack("approve", mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(1000))))
		add(assetTokens.home8, wrap(tokenABI.Pack("transfer", env.users[1], big.NewInt(5))))
		add(mt_common.GATEWAY_CONTRACT_ADDRESS, wrap(gh.abi.Pack("outbound", big.NewInt(102), env.users[0], []byte{0xAB}, big.NewInt(homeAssetID), big.NewInt(600),
			big.NewInt(0), big.NewInt(0), uint8(1), false, uint64(0))))

		items := make([]grouptxns.Item, len(txs))
		for i, tx := range txs {
			items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}
		}
		groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
		var flat []mt_types.Transaction
		for _, g := range groups {
			for _, it := range g.Items {
				flat = append(flat, it.Tx)
			}
		}
		switch mode {
		case "seq":
			for _, tx := range flat {
				NewTrueBlockSTM([]mt_types.Transaction{tx}).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
				if err := cs.GetSmartContractDB().LateBindRoots(); err != nil {
					t.Fatal(err)
				}
				if _, err := cs.GetSmartContractDB().Commit(); err != nil {
					t.Fatal(err)
				}
			}
		case "stm":
			NewTrueBlockSTM(flat).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
		default:
			ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, diffLeader, true)
		}
		if err := cs.GetSmartContractDB().LateBindRoots(); err != nil {
			t.Fatal(err)
		}
		if _, err := cs.GetSmartContractDB().Commit(); err != nil {
			t.Fatal(err)
		}
		return outcome{
			holder:    tokenViewCall(t, cs, assetTokens.home8, "balanceOf", holder).String(),
			gateway:   tokenViewCall(t, cs, assetTokens.home8, "balanceOf", mt_common.GATEWAY_CONTRACT_ADDRESS).String(),
			allowance: realTokenAllowanceOf(t, cs, assetTokens.home8, holder, mt_common.GATEWAY_CONTRACT_ADDRESS).String(),
		}
	}
	want := run("seq")
	if want.holder != "999999395" || want.gateway != "600" || want.allowance != "400" {
		t.Fatalf("sequential oracle is not what the scenario expects: %+v", want)
	}
	for _, mode := range []string{"stm", "opt"} {
		if got := run(mode); got != want {
			t.Errorf("%s: %+v, want %+v (a barrier tx's token changes were overwritten by an earlier normal tx's write set)", mode, got, want)
		}
	}
}
