package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/vm_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The leader reward is derived from receipt gas. Barrier txs (validator / gateway) get their gas from noncePlusOne, which
// ALWAYS reports 50000 gas -- also for an empty account and for a free sender. When the fee is not really debited, the
// reported gas must be 0, or the leader is credited a fee nobody paid.
func TestUpdateStateDB_ReportsGasOnlyWhenTheFeeWasDebited(t *testing.T) {
	const reported = uint64(50000)
	price := uint64(mt_common.MINIMUM_BASE_FEE)
	fee := new(big.Int).Mul(new(big.Int).SetUint64(reported), new(big.Int).SetUint64(price))
	sender := common.HexToAddress("0x1111111111111111111111111111111111111a")
	leader := common.HexToAddress(auditLeader)
	mvmId := common.HexToAddress("0xFE00000000000000000000000000000000000001")

	for _, tc := range []struct {
		name     string
		balance  *big.Int
		free     bool
		wantGas  uint64
		wantLeft *big.Int
	}{
		{"fee paid", new(big.Int).Add(fee, big.NewInt(7)), false, reported, big.NewInt(7)},
		{"balance too low to pay", new(big.Int).Sub(fee, big.NewInt(1)), false, 0, new(big.Int).Sub(fee, big.NewInt(1))},
		{"free sender or recipient", big.NewInt(1_000_000), true, 0, big.NewInt(1_000_000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestChainState(t)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
			seedAccount(t, cs, sender, tc.balance, 0)
			txp := transaction.NewTransaction(sender, mt_common.VALIDATOR_CONTRACT_ADDRESS, big.NewInt(0), 100000, price, 1000, nil, nil, common.Hash{}, common.Hash{}, 0, 991)
			vmP := vm_processor.NewVmProcessor(cs, mvmId, false, 0, leader)
			rs := &mvm.MVMExecuteResult{Status: pb.RECEIPT_STATUS_RETURNED, GasUsed: reported}
			_, err := vmP.UpdateStateDB(context.Background(), txp, rs, mvmId, tc.free, false)
			require.NoError(t, err)
			assert.Equal(t, tc.wantGas, rs.GasUsed, "reported gas must equal the gas actually charged")
			st, err := cs.GetAccountStateDB().AccountState(sender)
			require.NoError(t, err)
			assert.Equal(t, 0, st.TotalBalance().Cmp(tc.wantLeft), "balance %s want %s", st.TotalBalance(), tc.wantLeft)
		})
	}
}

// A tx that is included in the block with an ERROR receipt must still consume the sender's nonce. A bare error receipt on
// malformed calldata left the account nonce unchanged, so every later tx of the sender was stranded behind it.
func TestMalformedBarrierAndAccountSettingTxConsumeTheNonce(t *testing.T) {
	auditEnv(t)
	leader := common.HexToAddress(auditLeader)
	acct := utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	for name, to := range map[string]common.Address{
		"validator contract": mt_common.VALIDATOR_CONTRACT_ADDRESS,
		"gateway":            mt_common.GATEWAY_CONTRACT_ADDRESS,
		"account setting":    acct,
	} {
		t.Run(name, func(t *testing.T) {
			cs := newTestChainState(t)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
			from := common.HexToAddress("0x1111111111111111111111111111111111111a")
			seedAccount(t, cs, from, big.NewInt(1_000_000_000_000_000), 0)
			seedAccount(t, cs, leader, big.NewInt(0), 0)
			// calldata shorter than a selector: the handler rejects it before looking at the method
			tx := transaction.NewTransaction(from, to, big.NewInt(0), 100000, 1, 1000, nil, nil, common.Hash{}, common.Hash{}, 0, 991)
			_, rcps, _, _ := NewTrueBlockSTM([]mt_types.Transaction{tx}).Process(context.Background(), cs, leader, blankHeader(), 999)
			require.Len(t, rcps, 1)
			require.NotNil(t, rcps[0])
			assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcps[0].Status())
			st, err := cs.GetAccountStateDB().AccountState(from)
			require.NoError(t, err)
			assert.Equal(t, uint64(1), st.Nonce(), "the failed tx was included, so its nonce must be consumed")
		})
	}
}

// Garbage calldata sent to the account setting address from a brand-new key must not create state: AccountState() hands out
// an empty entry for an unknown address, so "exists" has to mean "has a balance / nonce / BLS key / code", not "non-nil".
func TestMalformedAccountSettingTxFromFreshKeyCreatesNoState(t *testing.T) {
	auditEnv(t)
	leader := common.HexToAddress(auditLeader)
	acct := utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	fresh := common.HexToAddress("0x9999999999999999999999999999999999999a1")
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	tx := transaction.NewTransaction(fresh, acct, big.NewInt(0), 100000, 0, 1000, nil, nil, common.Hash{}, common.Hash{}, 0, 991)
	_, rcps, _, _ := NewTrueBlockSTM([]mt_types.Transaction{tx}).Process(context.Background(), cs, leader, blankHeader(), 999)
	require.Len(t, rcps, 1)
	require.NotNil(t, rcps[0])
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcps[0].Status())
	st, err := cs.GetAccountStateDB().AccountState(fresh)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), st.Nonce(), "a fresh key's garbage tx must not turn into a nonce-1 account")
	assert.False(t, accountHasState(st))
}
