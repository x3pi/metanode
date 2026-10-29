package tx_processor

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	eth "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
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
	st, ret, _ := confDeployReceipt(t, key, init, gas)
	return st, ret
}

// confDeployReceipt runs one contract-creation transaction and returns its receipt status, return data and gas used.
func confDeployReceipt(t *testing.T, key *ecdsa.PrivateKey, init []byte, gas uint64) (pb.RECEIPT_STATUS, []byte, uint64) {
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
	return r[0].Status(), r[0].Return(), r[0].GasUsed()
}

// TestTopLevelDeployCodeSizeLimits pins the code-size limits of a contract-creation transaction: runtime code
// of at most 512KB and init code of at most 1MB.
func TestTopLevelDeployCodeSizeLimits(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	const maxCodeSize = 512 * 1024
	const maxInitCodeSize = 1024 * 1024

	for _, tc := range []struct {
		name    string
		runtime int
		wantOK  bool
	}{
		{"runtime_512KB_ok", maxCodeSize, true},
		{"runtime_512KB_rejected", maxCodeSize + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := append([]byte{0x5b}, bytes.Repeat([]byte{0x00}, tc.runtime-1)...)
			st, ret := confDeployStatus(t, key, confDeployInit(rt), 200_000_000)
			require.Equal(t, tc.wantOK, st == pb.RECEIPT_STATUS_RETURNED, "status %v return %q", st, ret)
		})
	}
	t.Run("initcode_1MB_rejected", func(t *testing.T) {
		init := append(confDeployInit([]byte{0x5b, 0x00}), bytes.Repeat([]byte{0x00}, (maxInitCodeSize+1)-len(confDeployInit([]byte{0x5b, 0x00})))...)
		require.Len(t, init, maxInitCodeSize+1)
		st, ret := confDeployStatus(t, key, init, 100_000_000)
		require.NotEqual(t, pb.RECEIPT_STATUS_RETURNED, st, "init code over 1MB must be rejected, got return %q", ret)
	})
	t.Run("initcode_1MB_ok", func(t *testing.T) {
		init := append(confDeployInit([]byte{0x5b, 0x00}), bytes.Repeat([]byte{0x00}, maxInitCodeSize-len(confDeployInit([]byte{0x5b, 0x00})))...)
		require.Len(t, init, maxInitCodeSize)
		st, ret := confDeployStatus(t, key, init, 100_000_000)
		require.Equal(t, pb.RECEIPT_STATUS_RETURNED, st, "return %q", ret)
	})
}

// TestDeployGasAgainstGeth compares the gas of contract-creation transactions with go-ethereum: intrinsic gas
// (53000 + calldata + 2 per init code word, EIP-3860), the init code run, the 200-gas-per-byte code deposit and the
// failures (revert keeps the unused gas, any other failure consumes everything).
func TestDeployGasAgainstGeth(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	const txGas = 3_000_000
	ret := func(n int) []byte { // init code that returns n zero bytes of runtime code (PUSH3 n PUSH1 0 RETURN)
		return []byte{0x62, byte(n >> 16), byte(n >> 8), byte(n), 0x60, 0x00, 0xf3}
	}
	cases := map[string][]byte{
		"returns_0_bytes":     ret(0),
		"returns_1_byte":      append(confScript(0x5b, "STOP")[:0:0], ret(1)...),
		"returns_300_bytes":   ret(300),
		"returns_5000_bytes":  ret(5000),
		"stores_then_returns": append(confScript(7, 0, "SSTORE"), ret(10)...),
		"clears_after_set":    append(confScript(7, 0, "SSTORE", 0, 0, "SSTORE"), ret(10)...),
		"reverts":             confScript(0, 0, "REVERT"),
		"invalid":             {0xfe},
		"returns_0xef_code":   {0x60, 0xef, 0x60, 0x00, 0x53, 0x60, 0x01, 0x60, 0x00, 0xf3},
		"runtime_24577_bytes": ret(24577),
		"stop_only":           {0x00},
	}
	for name, init := range cases {
		t.Run(name, func(t *testing.T) {
			// MVM
			st, _, mvmGas := confDeployReceipt(t, key, init, txGas)

			// go-ethereum: intrinsic gas of a creation, then the init code run by evm.Create
			intrinsic, ierr := core.IntrinsicGas(init, nil, nil, true, true, true, true)
			require.NoError(t, ierr)
			sdb, _ := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
			sender := crypto.PubkeyToAddress(key.PublicKey)
			sdb.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
			sdb.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
			cfg := &runtime.Config{Origin: sender, State: sdb, GasLimit: txGas - intrinsic, Value: big.NewInt(0), BlockNumber: big.NewInt(1)}
			_, _, left, cerr := runtime.Create(init, cfg)
			total := txGas - left
			if cerr == nil || errors.Is(cerr, vm.ErrExecutionReverted) {
				total = intrinsic + (txGas - intrinsic - left)
				refund := sdb.GetRefund()
				if cap := total / 5; refund > cap {
					refund = cap
				}
				total -= refund
			} else {
				total = txGas // an exceptional halt consumes everything
			}
			gethOK := cerr == nil
			mvmOK := st == pb.RECEIPT_STATUS_RETURNED
			require.Equal(t, gethOK, mvmOK, "success (geth error: %v)", cerr)
			require.Equal(t, total, mvmGas, "gas used (geth error: %v)", cerr)
		})
	}
}
