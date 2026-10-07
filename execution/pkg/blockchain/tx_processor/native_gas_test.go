package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeTransferGas(t *testing.T) {
	const base, sur = uint64(mt_common.TRANSFER_GAS_COST), uint64(params.CallNewAccountGas)
	cases := []struct {
		name                            string
		maxGas, intrinsic, newAcct, ref uint64
		strict                          bool
		wantGas                         uint64
		wantOK                          bool
	}{
		{"plain, existing recipient", 21000, base, 0, 0, false, base, true},
		{"plain, new recipient, low limit still pays surcharge (EXE-03)", 21000, base, sur, 0, false, base + sur, true},
		{"strict, limit below intrinsic fails and bills the limit", 21000, base + sur, 0, 0, true, 21000, false},
		{"strict, limit cannot pay surcharge fails", base + sur, base + sur, sur, 0, true, base + sur, false},
		{"strict, limit exactly covers everything", base + 2*sur, base + sur, sur, 0, true, base + 2*sur, true},
		{"refund is capped at gas/5", 100000, base + sur, 0, 12500, true, (base + sur) - (base+sur)/5, true},
		{"refund below the cap is applied in full", 200000, base + 4*sur, 0, 12500, true, base + 4*sur - 12500, true},
	}
	for _, c := range cases {
		gas, ok := nativeTransferGas(c.maxGas, c.intrinsic, c.newAcct, c.ref, c.strict)
		assert.Equal(t, c.wantGas, gas, c.name)
		assert.Equal(t, c.wantOK, ok, c.name)
	}
}

// The same plain transfer must cost the same in the native fast path and in TrueBlockSTM: which pipeline runs it
// depends on how the block is grouped, so any difference would make a user's fee depend on unrelated txs.
func TestNativeTransfer_GasParity_FastPathVsSTM(t *testing.T) {
	// The txs below are unsigned test fixtures: use the explicit devnet bypass of the execution signature filter.
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "true")
	t.Setenv("METANODE_DEVNET", "true")
	const chainID = 991
	sender := common.HexToAddress("0x1111111111111111111111111111111111111a")
	newRecipient := common.HexToAddress("0x2222222222222222222222222222222222222b")
	leader := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	initial := big.NewInt(1_000_000_000)

	run := func(useSTM bool) (gasUsed uint64, spent *big.Int) {
		cs := newTestChainState(t)
		cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
		seedAccount(t, cs, sender, initial, 0)
		tx := newTx(sender, newRecipient, 0, big.NewInt(1000), nil) // MaxGas 21000, price 1, brand-new recipient
		var rcps []mt_types.Receipt
		if useSTM {
			_, rcps, _, _ = NewTrueBlockSTM([]mt_types.Transaction{tx}).Process(context.Background(), cs, leader, blankHeader(), 999)
		} else {
			items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
			groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
			_, rcps, _, _ = ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, true)
		}
		require.Len(t, rcps, 1)
		require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())
		st, err := cs.GetAccountStateDB().AccountState(sender)
		require.NoError(t, err)
		return rcps[0].GasUsed(), new(big.Int).Sub(initial, st.TotalBalance())
	}

	fastGas, fastSpent := run(false)
	stmGas, stmSpent := run(true)
	assert.Equal(t, fastGas, stmGas, "receipt gas must not depend on the pipeline")
	assert.Equal(t, 0, fastSpent.Cmp(stmSpent), "sender balance delta must not depend on the pipeline (fast %s, stm %s)", fastSpent, stmSpent)
	assert.Equal(t, uint64(mt_common.TRANSFER_GAS_COST+params.CallNewAccountGas), stmGas, "EXE-03: the anti-dust surcharge is billed even with a 21000 limit")
}

// SetCode txs are strict: the signed limit must pay for intrinsic gas (25000 per tuple) AND the mandatory
// new-account surcharge, otherwise the tx fails and only the signed limit is billed.
func TestEIP7702_StrictGas_NewAccountSurcharge(t *testing.T) {
	const chainID = 991
	build := func(gas uint64) (status pb.RECEIPT_STATUS, gasUsed uint64) {
		senderKey, senderAddr := genTestKey(t)
		authKey, _ := genTestKey(t)
		cs := newTestChainState(t)
		cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
		seedAccount(t, cs, senderAddr, big.NewInt(1_000_000_000), 0)
		auth := signAuthorization(t, authKey, chainID, common.HexToAddress("0x1234"), 0)
		tx := buildSetCodeTx(t, senderKey, chainID, 0, gas, common.HexToAddress("0x9999"), 0, []types.SetCodeAuthorization{auth}) // 0x9999 is a new account
		_, rcps, _, _ := NewTrueBlockSTM([]mt_types.Transaction{tx}).Process(context.Background(), cs, common.HexToAddress("0xAAAA000000000000000000000000000000000001"), blankHeader(), 12345)
		require.Len(t, rcps, 1)
		return rcps[0].Status(), rcps[0].GasUsed()
	}
	need := uint64(mt_common.TRANSFER_GAS_COST) + params.CallNewAccountGas /*tuple*/ + params.CallNewAccountGas /*new recipient*/

	st, gu := build(need - 1)
	assert.NotEqual(t, pb.RECEIPT_STATUS_RETURNED, st, "one gas short of intrinsic + surcharge must fail")
	assert.Equal(t, need-1, gu, "a failed strict tx bills exactly the signed limit, never more")

	st, gu = build(need)
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, st)
	assert.Equal(t, need, gu)
}
