package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deregisterValidator marks the validator deleted in the stake DB's dirty map (a nil tombstone). A registerValidator from
// the same address in the same block then read the tombstone as "no such validator", passed the "already exists" check,
// and CreateRegisterWithKeys got a nil ValidatorState back (LoadOrStore returned the tombstone) and dereferenced it: a
// panic in block execution reachable by any validator.
func TestValidatorHandler_RegisterAfterDeregisterInTheSameBlock_DoesNotPanic(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "true")
	t.Setenv("METANODE_DEVNET", "true")
	env := newBarrierEnv()
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	val := env.evm[0].addr
	seedAccount(t, cs, val, big.NewInt(1_000_000_000_000), 0)
	seedAccount(t, cs, diffLeader, big.NewInt(0), 0)
	seedAccount(t, cs, env.nodeKP.Address(), big.NewInt(1_000_000_000_000_000), 0)
	addTestCommitteeValidator(t, cs, env.nodeKP)
	require.NoError(t, cs.GetStakeStateDB().CreateRegisterWithKeys(val, "pre", "", "", "", 5, big.NewInt(0), "p", "w", "n", "", []byte{1}, []byte{2}, "h", []byte{3}))

	vh, err := GetValidatorHandler()
	require.NoError(t, err)
	mk := func(nonce uint64, method string, args ...interface{}) mt_types.Transaction {
		in, err := vh.abi.Pack(method, args...)
		require.NoError(t, err)
		d, err := transaction.NewCallData(in).Marshal()
		require.NoError(t, err)
		return transaction.NewTransaction(val, mt_common.VALIDATOR_CONTRACT_ADDRESS, big.NewInt(0), 400000, 1, 8000+nonce, d, nil, common.Hash{}, common.Hash{}, nonce, 991)
	}
	txs := []mt_types.Transaction{
		mk(0, "deregisterValidator"),
		mk(1, "registerValidator", "p", "w", "n", "name", "d", "web", "img", uint64(100), big.NewInt(1), "netkey", "host", "authkey", "protokey"),
	}
	var rcps []mt_types.Receipt
	require.NotPanics(t, func() {
		_, rcps, _, _ = NewTrueBlockSTM(txs).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
	})
	require.Len(t, rcps, 2)
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status(), "deregister")
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[1].Status(), "the validator left, so it may register again")
	v, err := cs.GetStakeStateDB().GetValidator(val)
	require.NoError(t, err)
	assert.NotNil(t, v, "the address is a validator again after the second tx")
}
