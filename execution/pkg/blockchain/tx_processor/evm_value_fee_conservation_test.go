package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	eth_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

func contractHasCode(st mt_types.AccountState) bool {
	if st == nil || st.SmartContractState() == nil {
		return false
	}
	return st.SmartContractState().CodeHash() != common.Hash{}
}

// TestReviewEVMValueAndFeeConservation verifies that when a transaction has enough balance
// for its value before gas deduction but not after gas fee is deducted, the EVM execution does
// not credit the contract while failing to debit the sender (preventing unbacked token creation).
func TestReviewEVMValueAndFeeConservation(t *testing.T) { runReviewValue(t, 0) }

func TestReviewEVMValueAndFeeControl(t *testing.T) { runReviewValue(t, 100_000_000_000) }

func runReviewValue(t *testing.T, extra int64) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	initial := big.NewInt(1_000_000_000_000)
	seedAccount(t, cs, sender, new(big.Int).Add(initial, big.NewInt(extra)), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	// Payable initcode returning a one-byte STOP runtime (0x00).
	code := common.FromHex("0x6001600c60003960016000f300")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	seedAccount(t, cs, recipient, big.NewInt(1), 1)
	price := uint64(mt_common.MINIMUM_BASE_FEE)
	makeTx := func(nonce uint64, to *common.Address, value *big.Int, gas uint64, data []byte) mt_types.Transaction {
		signed, err := eth_types.SignNewTx(key, eth_types.NewEIP155Signer(big.NewInt(991)), &eth_types.LegacyTx{
			Nonce:    nonce,
			To:       to,
			Value:    value,
			Gas:      gas,
			GasPrice: new(big.Int).SetUint64(price),
			Data:     data,
		})
		require.NoError(t, err)
		result, err := transaction.NewTransactionFromEth(signed)
		require.NoError(t, err)
		return result
	}

	first := makeTx(1, &recipient, big.NewInt(100_000_000_000), 21000, nil)
	amount := new(big.Int).Sub(initial, big.NewInt(100_000_000_000))
	amount.Sub(amount, new(big.Int).Mul(new(big.Int).SetUint64(mt_common.TRANSFER_GAS_COST), new(big.Int).SetUint64(price)))
	tx := makeTx(2, nil, amount, 100000, code)

	before, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.True(t, first.ValidMaxFee(before))
	require.True(t, tx.ValidMaxFee(before))
	require.True(t, first.ValidEthSign())
	require.True(t, tx.ValidEthSign())
	require.Nil(t, VerifyTransaction(first, cs, before))
	require.Nil(t, VerifyTransaction(tx, cs, before))
	t.Logf("both signed transactions pass VerifyTransaction against initial balance; deploy value=%s", amount)

	txs := []mt_types.Transaction{first, tx}
	items := make([]grouptxns.Item, len(txs))
	for i, item := range txs {
		items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(item), Tx: item}
	}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
	_, rcps, scrs, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
	require.Len(t, rcps, 2)

	// Assertions on transaction 1 (Native transfer)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())
	require.Equal(t, uint64(mt_common.TRANSFER_GAS_COST), rcps[0].GasUsed())

	contractAddr := crypto.CreateAddress(sender, 2)

	if extra == 0 {
		// Insufficient balance scenario
		require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcps[1].Status())
		require.Equal(t, "insufficient balance", string(rcps[1].Return()))
		require.Greater(t, rcps[1].GasUsed(), uint64(0), "gas must be charged for EVM computation")
		require.Empty(t, rcps[1].EventLogs(), "failed execution must not leave event logs")

		// Nonce must advance to prevent replay
		stSender, err := cs.GetAccountStateDB().AccountState(sender)
		require.NoError(t, err)
		require.Equal(t, uint64(3), stSender.Nonce(), "sender nonce must increment")

		// Contract code must not be committed
		stContract, err := cs.GetAccountStateDB().AccountState(contractAddr)
		require.NoError(t, err)
		require.False(t, contractHasCode(stContract), "contract bytecode must not exist on failure")

		// Contract balance must not exist or be 0
		if stContract != nil && stContract.TotalBalance() != nil {
			require.Equal(t, 0, stContract.TotalBalance().Sign(), "contract must not have balance credited")
		}
	} else {
		// Sufficient balance scenario
		require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[1].Status())
		require.Greater(t, rcps[1].GasUsed(), uint64(0))

		// Contract code must be committed
		stContract, err := cs.GetAccountStateDB().AccountState(contractAddr)
		require.NoError(t, err)
		require.True(t, contractHasCode(stContract), "contract codehash must be set")

		// Contract balance must equal deploy amount
		require.Equal(t, amount, stContract.TotalBalance())
	}

	// Leader must receive exactly the execution gas fee billed in receipts
	fee1 := new(big.Int).Mul(new(big.Int).SetUint64(rcps[0].GasUsed()), new(big.Int).SetUint64(price))
	fee2 := new(big.Int).Mul(new(big.Int).SetUint64(rcps[1].GasUsed()), new(big.Int).SetUint64(price))
	expectedLeaderReward := new(big.Int).Add(fee1, fee2)
	stLeader, err := cs.GetAccountStateDB().AccountState(leader)
	require.NoError(t, err)
	require.Equal(t, expectedLeaderReward, stLeader.TotalBalance(), "leader must receive exactly total gas fee")

	// Global balance conservation check
	sum := new(big.Int)
	for _, addr := range []common.Address{sender, leader, recipient} {
		st, err := cs.GetAccountStateDB().AccountState(addr)
		require.NoError(t, err)
		t.Logf("account=%s balance=%s", addr, st.TotalBalance())
		sum.Add(sum, st.TotalBalance())
	}
	for _, rs := range scrs {
		if rs != nil {
			for addr := range rs.MapCodeHash() {
				st, err := cs.GetAccountStateDB().AccountState(common.HexToAddress(addr))
				require.NoError(t, err)
				t.Logf("contract=%s balance=%s", addr, st.TotalBalance())
				sum.Add(sum, st.TotalBalance())
			}
		}
	}
	t.Logf("status=%s gas=%d initial=%s final=%s return=%s", rcps[1].Status(), rcps[1].GasUsed(), new(big.Int).Add(initial, big.NewInt(1+extra)), sum, rcps[1].Return())
	require.Equal(t, 0, sum.Cmp(new(big.Int).Add(initial, big.NewInt(1+extra))), "balance conservation")
}

// TestReviewEVMValueAndFee_SubsequentTxExecutes asserts that when an EVM transaction fails due to
// insufficient balance in the middle of a block, subsequent valid transactions in the same block
// still execute and commit successfully without poisoning block state.
func TestReviewEVMValueAndFee_SubsequentTxExecutes(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})

	senderKey, err := crypto.HexToECDSA("4f3edf983ac636a65a342ce7c70d482f693b4f6e6371c6f499dfd5262c4a0342")
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)

	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	thirdAddr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	leader := common.HexToAddress(auditLeader)

	initial := big.NewInt(1_000_000_000_000)
	seedAccount(t, cs, sender, initial, 1)
	seedAccount(t, cs, recipient, big.NewInt(1), 1)
	seedAccount(t, cs, thirdAddr, big.NewInt(0), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	price := uint64(mt_common.MINIMUM_BASE_FEE)
	makeSenderTx := func(nonce uint64, to *common.Address, value *big.Int, gas uint64, data []byte) mt_types.Transaction {
		signed, err := eth_types.SignNewTx(senderKey, eth_types.NewEIP155Signer(big.NewInt(991)), &eth_types.LegacyTx{
			Nonce:    nonce,
			To:       to,
			Value:    value,
			Gas:      gas,
			GasPrice: new(big.Int).SetUint64(price),
			Data:     data,
		})
		require.NoError(t, err)
		res, err := transaction.NewTransactionFromEth(signed)
		require.NoError(t, err)
		return res
	}

	// Tx 1 (nonce 1): Sender -> Recipient (100,000,000,000) (succeeds)
	tx1 := makeSenderTx(1, &recipient, big.NewInt(100_000_000_000), 21000, nil)

	// Tx 2 (nonce 2): Sender payable deploy with value exhausting remaining funds (fails)
	// Remaining funds after tx1: initial - 100B - (21000 * price) = 897,900,000,000.
	// Payable value 895,000,000,000 + deploy gas fee (53201 * price = 5,320,100,000) > 897,900,000,000.
	code := common.FromHex("0x6001600c60003960016000f300")
	tx2 := makeSenderTx(2, nil, big.NewInt(895_000_000_000), 100000, code)

	// Tx 3 (nonce 3): Sender -> ThirdAddr (50,000,000,000) (succeeds after tx2 error)
	tx3 := makeSenderTx(3, &thirdAddr, big.NewInt(50_000_000_000), 21000, nil)

	txs := []mt_types.Transaction{tx1, tx2, tx3}
	items := make([]grouptxns.Item, len(txs))
	for i, item := range txs {
		items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(item), Tx: item}
	}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
	_, rcps, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
	require.Len(t, rcps, 3)

	rcpMap := make(map[common.Hash]mt_types.Receipt, len(rcps))
	for _, r := range rcps {
		rcpMap[r.TransactionHash()] = r
	}

	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcpMap[tx1.Hash()].Status(), "tx1 must succeed")
	require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcpMap[tx2.Hash()].Status(), "tx2 must fail with insufficient balance")
	require.Equal(t, "insufficient balance", string(rcpMap[tx2.Hash()].Return()))
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcpMap[tx3.Hash()].Status(), "tx3 must succeed after tx2 failure in the same block")

	// Third address received its transfer
	stThird, err := cs.GetAccountStateDB().AccountState(thirdAddr)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(50_000_000_000), stThird.TotalBalance())

	// Recipient received its transfer
	stRecipient, err := cs.GetAccountStateDB().AccountState(recipient)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(100_000_000_001), stRecipient.TotalBalance())

	// Sender nonce reached 4
	stSender, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Equal(t, uint64(4), stSender.Nonce())

	// Conservation across all 4 accounts
	sum := new(big.Int)
	for _, addr := range []common.Address{sender, recipient, thirdAddr, leader} {
		st, err := cs.GetAccountStateDB().AccountState(addr)
		require.NoError(t, err)
		sum.Add(sum, st.TotalBalance())
	}
	require.Equal(t, 0, sum.Cmp(new(big.Int).Add(initial, big.NewInt(1))), "balance conservation with subsequent tx")
}

// TestReviewEVMValueAndFee_CallWithValueConservation tests contract CALL with value where sender cannot cover value after gas.
func TestReviewEVMValueAndFee_CallWithValueConservation(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})

	deployerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	deployer := crypto.PubkeyToAddress(deployerKey.PublicKey)

	callerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	caller := crypto.PubkeyToAddress(callerKey.PublicKey)

	leader := common.HexToAddress(auditLeader)
	price := uint64(mt_common.MINIMUM_BASE_FEE)

	seedAccount(t, cs, deployer, big.NewInt(1_000_000_000_000), 1)
	callerInitial := big.NewInt(500_000_000_000)
	seedAccount(t, cs, caller, callerInitial, 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	// Deploy a payable contract: initcode copies runtime (0x60016000f3) which accepts any call
	// Bytecode: 6005600c60003960056000f3 60016000f300
	initCode := common.FromHex("0x6006600c60003960066000f360016000f300")
	deployTxSigned, err := eth_types.SignNewTx(deployerKey, eth_types.NewEIP155Signer(big.NewInt(991)), &eth_types.LegacyTx{
		Nonce:    1,
		Gas:      100000,
		GasPrice: new(big.Int).SetUint64(price),
		Data:     initCode,
	})
	require.NoError(t, err)
	deployTx, err := transaction.NewTransactionFromEth(deployTxSigned)
	require.NoError(t, err)

	items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(deployTx), Tx: deployTx}}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
	_, rcps, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())
	contractAddr := crypto.CreateAddress(deployer, 1)
	stContractDeploy, err := cs.GetAccountStateDB().AccountState(contractAddr)
	require.NoError(t, err)
	require.True(t, contractHasCode(stContractDeploy))

	// In Block 2: Caller has 500,000,000,000.
	// Tx 1: Transfer 100,000,000,000 to deployer
	// Tx 2: Call payable contract with value = 400,000,000,000 - transfer_gas_cost*price.
	// After gas fee deduction, remaining balance is insufficient for the call's value.
	makeCallerTx := func(nonce uint64, to *common.Address, value *big.Int, gas uint64, data []byte) mt_types.Transaction {
		signed, err := eth_types.SignNewTx(callerKey, eth_types.NewEIP155Signer(big.NewInt(991)), &eth_types.LegacyTx{
			Nonce:    nonce,
			To:       to,
			Value:    value,
			Gas:      gas,
			GasPrice: new(big.Int).SetUint64(price),
			Data:     data,
		})
		require.NoError(t, err)
		res, err := transaction.NewTransactionFromEth(signed)
		require.NoError(t, err)
		return res
	}

	callAmt := new(big.Int).Sub(callerInitial, big.NewInt(100_000_000_000))
	callAmt.Sub(callAmt, new(big.Int).Mul(new(big.Int).SetUint64(mt_common.TRANSFER_GAS_COST), new(big.Int).SetUint64(price)))

	cTx1 := makeCallerTx(1, &deployer, big.NewInt(100_000_000_000), 21000, nil)
	cTx2 := makeCallerTx(2, &contractAddr, callAmt, 100000, nil)

	txs2 := []mt_types.Transaction{cTx1, cTx2}
	items2 := make([]grouptxns.Item, len(txs2))
	for i, item := range txs2 {
		items2[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(item), Tx: item}
	}
	groups2 := grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode)
	_, rcps2, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups2, blankHeader(), false, false, 999, leader, false)
	require.Len(t, rcps2, 2)

	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps2[0].Status())
	require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcps2[1].Status())
	require.Equal(t, "insufficient balance", string(rcps2[1].Return()))

	// Contract balance must not have been credited with callAmt
	stContract, err := cs.GetAccountStateDB().AccountState(contractAddr)
	require.NoError(t, err)
	if stContract != nil && stContract.TotalBalance() != nil {
		require.Equal(t, 0, stContract.TotalBalance().Sign(), "contract balance must remain 0")
	}
}

// TestReviewEVMValueAndFee_PendingBalanceFolding verifies that an account with pendingBalance > 0
// correctly has its pending balance folded when participating in a 0-value EVM transaction.
func TestReviewEVMValueAndFee_PendingBalanceFolding(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	addr := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)

	seedAccount(t, cs, addr, big.NewInt(200_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	// Add pending balance of 100_000_000_000
	acc, err := cs.GetAccountStateDB().AccountState(addr)
	require.NoError(t, err)
	acc.AddPendingBalance(big.NewInt(100_000_000_000))
	require.Equal(t, big.NewInt(100_000_000_000), acc.PendingBalance())
	require.Equal(t, big.NewInt(300_000_000_000), acc.TotalBalance())

	price := uint64(mt_common.MINIMUM_BASE_FEE)
	code := common.FromHex("0x6001600c60003960016000f300")
	signed, err := eth_types.SignNewTx(key, eth_types.NewEIP155Signer(big.NewInt(991)), &eth_types.LegacyTx{
		Nonce:    1,
		Gas:      100000,
		GasPrice: new(big.Int).SetUint64(price),
		Data:     code,
		Value:    big.NewInt(0),
	})
	require.NoError(t, err)
	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
	_, rcps, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	accAfter, err := cs.GetAccountStateDB().AccountState(addr)
	require.NoError(t, err)
	// Pending balance must have been folded into regular balance (pending balance = 0)
	require.Equal(t, 0, accAfter.PendingBalance().Sign(), "pendingBalance must be folded to 0")
	gasFee := new(big.Int).Mul(new(big.Int).SetUint64(rcps[0].GasUsed()), new(big.Int).SetUint64(price))
	expectedBalance := new(big.Int).Sub(big.NewInt(300_000_000_000), gasFee)
	require.Equal(t, expectedBalance, accAfter.Balance(), "balance must equal initial total minus gas fee")
}
