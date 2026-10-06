package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/rpc"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
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
