package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_pool"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatGethError_RPCErrorPreserved(t *testing.T) {
	customRPC := &jsonrpcError{code: -32600, message: "custom invalid request"}
	err := formatGethError(customRPC)
	require.NotNil(t, err)
	rpcErr, ok := err.(rpc.Error)
	require.True(t, ok)
	assert.Equal(t, -32600, rpcErr.ErrorCode())
	assert.Equal(t, "custom invalid request", rpcErr.Error())
}

func TestFormatGethError_StandardErrors(t *testing.T) {
	tests := []struct {
		name        string
		inputErr    error
		expectedCode int
		expectedMsg  string
	}{
		{
			name:        "typed ErrNonceTooLow",
			inputErr:    transaction.ErrNonceTooLow,
			expectedCode: -32000,
			expectedMsg:  "nonce too low",
		},
		{
			name:        "wrapped InvalidNonce",
			inputErr:    fmt.Errorf("mempool add failed: %w", transaction.InvalidNonce),
			expectedCode: -32000,
			expectedMsg:  "nonce too low",
		},
		{
			name:        "typed ErrInsufficientFunds",
			inputErr:    transaction.ErrInsufficientFunds,
			expectedCode: -32000,
			expectedMsg:  "insufficient funds for gas * price + value",
		},
		{
			name:        "typed InvalidMaxFee",
			inputErr:    transaction.InvalidMaxFee,
			expectedCode: -32000,
			expectedMsg:  "insufficient funds for gas * price + value",
		},
		{
			name:        "typed ErrAlreadyKnown",
			inputErr:    transaction.ErrAlreadyKnown,
			expectedCode: -32000,
			expectedMsg:  "already known",
		},
		{
			name:        "typed ErrReplacementUnderpriced",
			inputErr:    transaction.ErrReplacementUnderpriced,
			expectedCode: -32000,
			expectedMsg:  "replacement transaction underpriced",
		},
		{
			name:        "typed ErrIntrinsicGasTooLow",
			inputErr:    transaction.ErrIntrinsicGasTooLow,
			expectedCode: -32000,
			expectedMsg:  "intrinsic gas too low",
		},
		{
			name:        "typed ErrExceedsBlockGasLimit",
			inputErr:    transaction.ErrExceedsBlockGasLimit,
			expectedCode: -32000,
			expectedMsg:  "exceeds block gas limit",
		},
		{
			name:        "typed ErrInvalidSender",
			inputErr:    transaction.ErrInvalidSender,
			expectedCode: -32000,
			expectedMsg:  "invalid sender",
		},
		{
			name:        "typed ErrTxTypeNotSupported",
			inputErr:    transaction.ErrTxTypeNotSupported,
			expectedCode: -32000,
			expectedMsg:  "transaction type not supported",
		},
		{
			name:        "typed ErrMaxInitCodeSizeExceeded",
			inputErr:    transaction.ErrMaxInitCodeSizeExceeded,
			expectedCode: -32000,
			expectedMsg:  "max initcode size exceeded",
		},
		{
			name:        "typed ErrGasLimitReached",
			inputErr:    transaction.ErrGasLimitReached,
			expectedCode: -32000,
			expectedMsg:  "gas limit reached",
		},
		{
			name:        "unwrapped string error fallback",
			inputErr:    errors.New("exceeds block gas limit"),
			expectedCode: -32000,
			expectedMsg:  "exceeds block gas limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatted := formatGethError(tt.inputErr)
			require.NotNil(t, formatted)
			rpcErr, ok := formatted.(rpc.Error)
			require.True(t, ok, "must implement rpc.Error")
			assert.Equal(t, tt.expectedCode, rpcErr.ErrorCode())
			assert.Equal(t, tt.expectedMsg, rpcErr.Error())
		})
	}
}

func TestFormatGethError_NilReturnsNil(t *testing.T) {
	assert.Nil(t, formatGethError(nil))
}

func TestGetTransactionCount_Pending(t *testing.T) {
	prevBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	defer trie.SetStateBackend(prevBackend)

	accountStorage := storage.NewDummyStorage("")
	codeStorage := storage.NewDummyStorage("")
	scStorage := storage.NewDummyStorage("")
	header := block.NewBlockHeader(
		common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{},
		common.Address{}, 0, common.Hash{}, 0,
	)
	cs, err := blockchain.NewChainStateRemote(header, accountStorage, codeStorage, scStorage, map[common.Address]struct{}{})
	require.NoError(t, err)

	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	as := state.NewAccountState(addr)
	as.SetNonce(5)
	cs.GetAccountStateDB().InjectLoadedAccount(as)

	pool := transaction_pool.NewTransactionPool()
	tp := processor.NewTransactionProcessor(nil, pool, nil, nil, t.TempDir(), "991", nil, cs)

	app := &App{
		config: &config.SimpleChainConfig{
			ChainId: big.NewInt(991),
		},
		chainState:           cs,
		transactionProcessor: tp,
	}

	api := &MetaAPI{App: app}
	ctx := context.Background()

	// 1. "latest" returns on-chain state nonce = 5
	latestRes, err := api.GetTransactionCount(ctx, addr, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))
	require.NoError(t, err)
	assert.Equal(t, uint64(5), uint64(*latestRes))

	// 2. "pending" with empty pool returns on-chain state nonce = 5
	pendingRes, err := api.GetTransactionCount(ctx, addr, rpc.BlockNumberOrHashWithNumber(rpc.PendingBlockNumber))
	require.NoError(t, err)
	assert.Equal(t, uint64(5), uint64(*pendingRes))

	// 3. Add 3 consecutive pending transactions with nonces 5, 6, 7
	for n := uint64(5); n <= 7; n++ {
		tx := transaction.NewTransaction(
			addr, common.HexToAddress("0xFF"), big.NewInt(10), 21000, 100, 0,
			[]byte{byte(n)}, nil, common.Hash{}, common.Hash{}, n, 991,
		)
		require.NoError(t, pool.AddTransaction(tx))
	}

	// "pending" now returns 8!
	pendingRes, err = api.GetTransactionCount(ctx, addr, rpc.BlockNumberOrHashWithNumber(rpc.PendingBlockNumber))
	require.NoError(t, err)
	assert.Equal(t, uint64(8), uint64(*pendingRes))

	// "latest" still returns 5
	latestRes, err = api.GetTransactionCount(ctx, addr, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))
	require.NoError(t, err)
	assert.Equal(t, uint64(5), uint64(*latestRes))

	// 4. Add transaction with nonce 9 (creating a gap at nonce 8)
	txGap := transaction.NewTransaction(
		addr, common.HexToAddress("0xFF"), big.NewInt(10), 21000, 100, 0,
		[]byte{9}, nil, common.Hash{}, common.Hash{}, 9, 991,
	)
	require.NoError(t, pool.AddTransaction(txGap))

	// "pending" must stop at the gap (nonce 8 is missing) -> returns 8
	pendingRes, err = api.GetTransactionCount(ctx, addr, rpc.BlockNumberOrHashWithNumber(rpc.PendingBlockNumber))
	require.NoError(t, err)
	assert.Equal(t, uint64(8), uint64(*pendingRes))

	// 5. Fill the gap with nonce 8
	txFill := transaction.NewTransaction(
		addr, common.HexToAddress("0xFF"), big.NewInt(10), 21000, 100, 0,
		[]byte{8}, nil, common.Hash{}, common.Hash{}, 8, 991,
	)
	require.NoError(t, pool.AddTransaction(txFill))

	// "pending" now advances through 8 and 9 -> returns 10!
	pendingRes, err = api.GetTransactionCount(ctx, addr, rpc.BlockNumberOrHashWithNumber(rpc.PendingBlockNumber))
	require.NoError(t, err)
	assert.Equal(t, uint64(10), uint64(*pendingRes))
}

