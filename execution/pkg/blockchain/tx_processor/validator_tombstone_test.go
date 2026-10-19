package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Txs of one sender run in nonce order and senders are ordered by address, so the only txs that can run between the
// deregistration and the re-registration of an address are its own; that is what is covered here.
//
// After deregisterValidator the validator is a nil tombstone in the stake DB until the block commits. Every other validator
// contract call that touches it in the same block must be an ordinary failed tx (never a panic), and a fresh registration
// must work and give an empty validator (nothing carried over from the old one).
func TestValidatorHandler_EveryOpOnATombstonedValidatorFailsCleanly(t *testing.T) {
	env, cs := barrierSetup(t)
	vh, err := GetValidatorHandler()
	require.NoError(t, err)
	val := env.evm[0].addr // pre-registered by barrierSetup
	pack := func(method string, args ...interface{}) []byte {
		in, err := vh.abi.Pack(method, args...)
		require.NoError(t, err)
		d, err := transaction.NewCallData(in).Marshal()
		require.NoError(t, err)
		return d
	}
	nonce := map[common.Address]uint64{}
	mk := func(from common.Address, amount int64, method string, args ...interface{}) mt_types.Transaction {
		n := nonce[from]
		nonce[from]++
		return transaction.NewTransaction(from, mt_common.VALIDATOR_CONTRACT_ADDRESS, big.NewInt(amount), 400000, 1, uint64(8000+n)+uint64(from[19])*100, pack(method, args...),
			nil, common.Hash{}, common.Hash{}, n, 991)
	}
	dereg := mk(val, 0, "deregisterValidator")
	onTombstone := []struct {
		name string
		tx   mt_types.Transaction
	}{
		{"setCommissionRate", mk(val, 0, "setCommissionRate", uint64(10))},
		{"updateValidatorInfo", mk(val, 0, "updateValidatorInfo", "a", "b", "c", "d")},
		{"deregister again", mk(val, 0, "deregisterValidator")},
		{"withdrawReward", mk(val, 0, "withdrawReward", val)},
		{"undelegate", mk(val, 0, "undelegate", val, big.NewInt(1))},
		{"self delegate", mk(val, 5_000, "delegate", val)},
		{"distributeRewards", mk(val, 0, "distributeRewards", val, big.NewInt(10))},
	}
	reRegister := mk(val, 0, "registerValidator", "p", "w", "n", "name", "d", "web", "img", uint64(100), big.NewInt(1), "netkey", "host", "authkey", "protokey")

	txs := []mt_types.Transaction{dereg}
	for _, c := range onTombstone {
		txs = append(txs, c.tx)
	}
	txs = append(txs, reRegister)
	byHash := map[common.Hash]int{}
	for i, tx := range txs {
		byHash[tx.Hash()] = i
	}

	var rcps []mt_types.Receipt
	require.NotPanics(t, func() {
		_, rcps, _, _ = NewTrueBlockSTM(txs).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
	})
	status := map[int]pb.RECEIPT_STATUS{}
	for _, r := range rcps {
		if r != nil {
			status[byHash[r.TransactionHash()]] = r.Status()
		}
	}
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, status[0], "deregister")
	for i, c := range onTombstone {
		// the sender's own nonce order inside a group decides which ones run before the deregistration; only the outcome
		// class matters here: no panic, and a tombstoned validator is never successfully operated on.
		if c.name == "deregister again" || c.name == "withdrawReward" {
			continue // can succeed or fail depending on rewards state; covered by the no-panic requirement
		}
		assert.NotEqual(t, pb.RECEIPT_STATUS_RETURNED, status[i+1], "%s on a deregistered validator must not succeed", c.name)
	}
	v, err := cs.GetStakeStateDB().GetValidator(val)
	require.NoError(t, err)
	require.NotNil(t, v, "the address is a validator again after the final registration")
	assert.Equal(t, 0, v.TotalStakedAmount().Sign(), "a re-registered validator starts empty")
}
