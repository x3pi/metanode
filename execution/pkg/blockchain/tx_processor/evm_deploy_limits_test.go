package tx_processor

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	eth "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	mt "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// confDeployStatus runs one contract-creation transaction and returns its receipt status.
func confDeployStatus(t *testing.T, key *ecdsa.PrivateKey, init []byte, gas uint64) (pb.RECEIPT_STATUS, []byte) {
	t.Helper()
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	s, err := eth.SignNewTx(key, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{Nonce: 1, Value: big.NewInt(0), Gas: gas, GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE), Data: init})
	require.NoError(t, err)
	tx, err := transaction.NewTransactionFromEth(s)
	require.NoError(t, err)
	items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
	var _ types.Transaction = tx
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
	require.Len(t, r, 1)
	return r[0].Status(), r[0].Return()
}

// TestTopLevelDeployCodeSizeLimits pins the code-size limits of a contract-creation transaction: runtime code
// of at most 24576 bytes (EIP-170) and init code of at most 49152 bytes (EIP-3860).
func TestTopLevelDeployCodeSizeLimits(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	for _, tc := range []struct {
		name    string
		runtime int
		wantOK  bool
	}{
		{"runtime_24576_ok", 24576, true},
		{"runtime_24577_rejected", 24577, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := append([]byte{0x5b}, bytes.Repeat([]byte{0x00}, tc.runtime-1)...)
			st, ret := confDeployStatus(t, key, confDeployInit(rt), 30_000_000)
			require.Equal(t, tc.wantOK, st == pb.RECEIPT_STATUS_RETURNED, "status %v return %q", st, ret)
		})
	}
	t.Run("initcode_49153_rejected", func(t *testing.T) {
		init := append(confDeployInit([]byte{0x5b, 0x00}), bytes.Repeat([]byte{0x00}, 49153-len(confDeployInit([]byte{0x5b, 0x00})))...)
		require.Len(t, init, 49153)
		st, ret := confDeployStatus(t, key, init, 30_000_000)
		require.NotEqual(t, pb.RECEIPT_STATUS_RETURNED, st, "init code over 49152 bytes must be rejected, got return %q", ret)
	})
	t.Run("initcode_49152_ok", func(t *testing.T) {
		init := append(confDeployInit([]byte{0x5b, 0x00}), bytes.Repeat([]byte{0x00}, 49152-len(confDeployInit([]byte{0x5b, 0x00})))...)
		require.Len(t, init, 49152)
		st, ret := confDeployStatus(t, key, init, 30_000_000)
		require.Equal(t, pb.RECEIPT_STATUS_RETURNED, st, "return %q", ret)
	})
}
