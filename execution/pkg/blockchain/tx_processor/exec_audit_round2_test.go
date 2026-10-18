package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const auditLeader = "0xAAAA000000000000000000000000000000000001"

func auditEnv(t *testing.T) {
	t.Helper()
	// Unsigned fixtures: explicit devnet bypass of the execution signature filter (never active on chain 991).
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "true")
	t.Setenv("METANODE_DEVNET", "true")
}

// The nonce-0 BLS-key registration is free by protocol (the account has no balance). It debits nothing, so it must also
// report no gas: the leader reward is re-derived from receipt gas, and reporting 20000 gas minted GasUsed x price for
// every new account.
func TestAccountSetting_BlsRegistrationIsFreeAndRewardsNobody(t *testing.T) {
	auditEnv(t)
	acct := utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	leader := common.HexToAddress(auditLeader)
	for _, seeded := range []bool{true, false} {
		cs := newTestChainState(t)
		cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
		from := common.HexToAddress("0x1111111111111111111111111111111111111a")
		if seeded {
			seedAccount(t, cs, from, big.NewInt(1_000_000_000), 0)
		}
		seedAccount(t, cs, leader, big.NewInt(0), 0)
		key := make([]byte, 48)
		for i := range key {
			key[i] = byte(i + 1)
		}
		data, err := PackSetBlsPublicKey(key)
		require.NoError(t, err)
		tx := transaction.NewTransaction(from, acct, big.NewInt(0), 100000, mt_common.MINIMUM_BASE_FEE, 1000, data, nil, common.Hash{}, common.Hash{}, 0, 991)
		_, rcps, _, _ := NewTrueBlockSTM([]mt_types.Transaction{tx}).Process(context.Background(), cs, leader, blankHeader(), 999)
		require.Len(t, rcps, 1)
		assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())
		assert.Equal(t, uint64(0), rcps[0].GasUsed(), "a free registration reports no gas")
		fa, err := cs.GetAccountStateDB().AccountState(from)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), fa.Nonce())
		assert.Len(t, fa.PublicKeyBls(), 48)
		ld, err := cs.GetAccountStateDB().AccountState(leader)
		require.NoError(t, err)
		assert.Equal(t, 0, ld.TotalBalance().Sign(), "seeded=%v: nothing was charged, so the leader must receive nothing (got %s)", seeded, ld.TotalBalance())
	}
}

// setAccountType is NOT free: it must debit TRANSFER_GAS_COST x price, the leader earns exactly that, and when the sender
// cannot pay it the tx fails (nonce consumed, type unchanged, no reward).
func TestAccountSetting_SetAccountTypePaysItsFee(t *testing.T) {
	auditEnv(t)
	acct := utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	leader := common.HexToAddress(auditLeader)
	price := uint64(mt_common.MINIMUM_BASE_FEE)
	fee := new(big.Int).Mul(big.NewInt(int64(mt_common.TRANSFER_GAS_COST)), new(big.Int).SetUint64(price))

	for _, tc := range []struct {
		name    string
		balance *big.Int
		wantOK  bool
	}{
		{"can pay", new(big.Int).Add(fee, big.NewInt(12345)), true},
		{"cannot pay", new(big.Int).Sub(fee, big.NewInt(1)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestChainState(t)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
			from := common.HexToAddress("0x1111111111111111111111111111111111111a")
			seedAccount(t, cs, from, tc.balance, 1)
			seedAccount(t, cs, leader, big.NewInt(0), 0)
			data, err := PackSetAccountType(pb.ACCOUNT_TYPE(1))
			require.NoError(t, err)
			tx := transaction.NewTransaction(from, acct, big.NewInt(0), 100000, price, 1000, data, nil, common.Hash{}, common.Hash{}, 1, 991)
			_, rcps, _, _ := NewTrueBlockSTM([]mt_types.Transaction{tx}).Process(context.Background(), cs, leader, blankHeader(), 999)
			require.Len(t, rcps, 1)
			fa, err := cs.GetAccountStateDB().AccountState(from)
			require.NoError(t, err)
			ld, err := cs.GetAccountStateDB().AccountState(leader)
			require.NoError(t, err)
			assert.Equal(t, uint64(2), fa.Nonce(), "included in the block either way: the nonce is consumed")
			if tc.wantOK {
				assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())
				assert.Equal(t, 0, fa.TotalBalance().Cmp(big.NewInt(12345)), "fee debited, got balance %s", fa.TotalBalance())
				assert.Equal(t, 0, ld.TotalBalance().Cmp(fee), "leader earns exactly what was paid, got %s want %s", ld.TotalBalance(), fee)
				assert.Equal(t, pb.ACCOUNT_TYPE(1), fa.AccountType())
			} else {
				assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcps[0].Status())
				assert.Equal(t, uint64(0), rcps[0].GasUsed())
				assert.Equal(t, 0, fa.TotalBalance().Cmp(tc.balance), "an unpayable tx debits nothing")
				assert.Equal(t, 0, ld.TotalBalance().Sign(), "no reward for an unpaid fee")
				assert.Equal(t, pb.ACCOUNT_TYPE(0), fa.AccountType(), "the type must not change when the fee was not paid")
			}
		})
	}
}

// A plain value transfer (no calldata) to an address that has code must go through the EVM, not the balance-only native
// branch. Before the fix the native branch "delivered" it: status RETURNED, 45000 gas, the value trapped in the contract and
// its receive()/fallback never run. Now the EVM path handles it (the fixture token has no receive(), so the call fails
// and the value stays with the sender).
//
// Limitation: contract CALLS cannot succeed in this unit-test harness (every call comes back HALTED, with or without the
// fix), so this pins the ROUTING, not a successful receive() execution.
func TestPlainTransferToContract_RunsContractCode(t *testing.T) {
	auditEnv(t)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	sender := common.HexToAddress("0x1111111111111111111111111111111111111a")
	leader := common.HexToAddress(auditLeader)
	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 0)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	contract := deployTestWrappedAsset(t, cs, sender, big.NewInt(1000))
	require.True(t, cs.HasCode(contract))

	fa, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	tx := newHighGasTx(sender, contract, fa.Nonce(), big.NewInt(7), nil)
	items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
	_, rcps, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, true)
	require.Len(t, rcps, 1)

	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcps[0].Status(), "the contract has no receive(): the call must revert")
	ca, err := cs.GetAccountStateDB().AccountState(contract)
	require.NoError(t, err)
	assert.Equal(t, 0, ca.TotalBalance().Sign(), "the value must not be left trapped in the contract (got %s)", ca.TotalBalance())
}

// If the sender cannot pay the gas AFTER the EVM ran, the receipt says "failed / nothing charged"; the stored result must
// say the same. The deployed bytecode / code hash / storage root of a rejected deployment used to be committed anyway
// (the end-of-block merge applies whatever sits in the stored result without looking at the receipt).
func TestDeploy_UnpayableGasDeploysNothing(t *testing.T) {
	auditEnv(t)
	leader := common.HexToAddress(auditLeader)
	sender := common.HexToAddress("0x1111111111111111111111111111111111111a")
	run := func(balance *big.Int) (pb.RECEIPT_STATUS, []string, func(common.Address) bool) {
		cs := newTestChainState(t)
		cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
		seedAccount(t, cs, sender, balance, 0)
		seedAccount(t, cs, leader, big.NewInt(0), 0)
		args, err := testWrappedAssetABI(t).Pack("", big.NewInt(1000))
		require.NoError(t, err)
		code, err := hexDecode(testWrappedAssetBytecode)
		require.NoError(t, err)
		tx := newHighGasTx(sender, common.Address{}, 0, big.NewInt(0), append(append([]byte{}, code...), args...))
		require.True(t, tx.IsDeployContract())
		items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
		groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
		_, rcps, scrs, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, true)
		require.Len(t, rcps, 1)
		require.Len(t, scrs, 1)
		var deployed []string
		for addr := range scrs[0].MapCodeHash() {
			deployed = append(deployed, addr)
		}
		return rcps[0].Status(), deployed, cs.HasCode
	}

	st, deployed, hasCode := run(big.NewInt(1_000_000_000_000_000))
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, st, "control: a payable deployment succeeds")
	require.Len(t, deployed, 1, "control: it reports the new contract")
	require.True(t, hasCode(common.HexToAddress(deployed[0])), "control: the contract exists afterwards")
	contract := common.HexToAddress(deployed[0])

	st, deployed, hasCode = run(big.NewInt(100)) // far below gasUsed x price
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, st)
	assert.Empty(t, deployed, "a rejected deployment must not report (or commit) a code hash")
	assert.False(t, hasCode(contract), "a deployment whose gas could not be paid must not leave a contract behind")
}

// A native transfer to/from a free-fee address charges nothing and rewards nobody, in BOTH pipelines. The fast path used to
// bill MAX_GASS_FEE (5,000,000 gas) for it, and Block-STM billed the normal fee.
func TestFreeFeeAddress_NativeTransferIsFreeInBothPipelines(t *testing.T) {
	auditEnv(t)
	sender := common.HexToAddress("0x1111111111111111111111111111111111111a")
	free := common.HexToAddress("0x2222222222222222222222222222222222222b")
	leader := common.HexToAddress(auditLeader)
	price := uint64(mt_common.MINIMUM_BASE_FEE)
	for _, useSTM := range []bool{false, true} {
		name := map[bool]string{false: "fast-path", true: "block-stm"}[useSTM]
		t.Run(name, func(t *testing.T) {
			cs := newTestChainState(t)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
			cs.GetFreeFeeAddress()[free] = struct{}{}
			seedAccount(t, cs, sender, big.NewInt(10_000_000_000), 0)
			seedAccount(t, cs, free, big.NewInt(1), 0)
			seedAccount(t, cs, leader, big.NewInt(0), 0)
			tx := transaction.NewTransaction(sender, free, big.NewInt(100), 21000, price, 1000, nil, nil, common.Hash{}, common.Hash{}, 0, 991)
			var rcps []mt_types.Receipt
			if useSTM {
				_, rcps, _, _ = NewTrueBlockSTM([]mt_types.Transaction{tx}).Process(context.Background(), cs, leader, blankHeader(), 999)
			} else {
				items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
				groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
				_, rcps, _, _ = ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, true)
			}
			require.Len(t, rcps, 1)
			assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())
			sa, _ := cs.GetAccountStateDB().AccountState(sender)
			ra, _ := cs.GetAccountStateDB().AccountState(free)
			la, _ := cs.GetAccountStateDB().AccountState(leader)
			assert.Equal(t, 0, sa.TotalBalance().Cmp(big.NewInt(10_000_000_000-100)), "the sender pays the amount only, got %s", sa.TotalBalance())
			assert.Equal(t, 0, ra.TotalBalance().Cmp(big.NewInt(101)))
			assert.Equal(t, 0, la.TotalBalance().Sign(), "no fee was charged, so the leader earns nothing (got %s)", la.TotalBalance())
		})
	}
}

// The validator contract's manual distributeRewards took any caller's amount out of the contract's whole balance (which
// also holds every delegator's principal) and paid the "commission" part to the validator owner. Rewards are distributed
// per block by the protocol, so the entry point is disabled.
func TestValidatorHandler_DistributeRewardsIsDisabled(t *testing.T) {
	h, err := GetValidatorHandler()
	require.NoError(t, err)
	_, err = h.handleDistributeRewards(nil, nil, nil, nil, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")
}
