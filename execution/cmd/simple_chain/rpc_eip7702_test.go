package main

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	eth_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRPC_F6_TransactionArgs_AuthorizationList_JSONUnmarshal verifies that TransactionArgs
// correctly accepts and parses the "authorizationList" field from JSON-RPC requests (eth_call / eth_estimateGas).
func TestRPC_F6_TransactionArgs_AuthorizationList_JSONUnmarshal(t *testing.T) {
	rawJSON := `{
		"from": "0x1111111111111111111111111111111111111111",
		"to": "0x2222222222222222222222222222222222222222",
		"gas": "0x186a0",
		"gasPrice": "0x1",
		"value": "0x0",
		"data": "0xabcdef",
		"chainId": "0x3df",
		"authorizationList": [
			{
				"chainId": "0x3df",
				"address": "0x3333333333333333333333333333333333333333",
				"nonce": "0x1",
				"yParity": "0x0",
				"r": "0x1111111111111111111111111111111111111111111111111111111111111111",
				"s": "0x2222222222222222222222222222222222222222222222222222222222222222"
			}
		]
	}`

	var args TransactionArgs
	err := json.Unmarshal([]byte(rawJSON), &args)
	require.NoError(t, err, "TransactionArgs must successfully unmarshal JSON containing authorizationList")

	require.Len(t, args.AuthList, 1, "args.AuthList must have exactly 1 item")
	auth := args.AuthList[0]
	assert.Equal(t, uint64(991), auth.ChainID.Uint64())
	assert.Equal(t, common.HexToAddress("0x3333333333333333333333333333333333333333"), auth.Address)
	assert.Equal(t, uint64(1), auth.Nonce)

	// Verify ToTransaction correctly creates SetCodeTx
	gas := hexutil.Uint64(100_000)
	args.Gas = &gas
	tx := args.ToTransaction(int(eth_types.SetCodeTxType))
	require.NotNil(t, tx, "ToTransaction must create a non-nil Transaction")
	assert.Equal(t, uint8(eth_types.SetCodeTxType), tx.Type(), "Transaction type must be SetCodeTxType (4)")
	assert.Equal(t, 1, len(tx.SetCodeAuthorizations()), "Transaction must contain 1 authorization tuple")
}

// TestRPC_F6_FromEthAuthorizationList verifies translation between go-ethereum types
// and MetaNode protobuf representations.
func TestRPC_F6_FromEthAuthorizationList(t *testing.T) {
	ethAuths := []eth_types.SetCodeAuthorization{
		{
			ChainID: *uint256.NewInt(991),
			Address: common.HexToAddress("0x4444444444444444444444444444444444444444"),
			Nonce:   2,
			V:       27,
			R:       *uint256.NewInt(123),
			S:       *uint256.NewInt(456),
		},
	}

	protoAuths := transaction.FromEthAuthorizationList(ethAuths)
	require.Len(t, protoAuths, 1)
	assert.Equal(t, uint64(991), protoAuths[0].ChainID)
	assert.Equal(t, common.HexToAddress("0x4444444444444444444444444444444444444444").Bytes(), protoAuths[0].Address)
	assert.Equal(t, uint64(2), protoAuths[0].Nonce)

	// Round-trip back to eth authorization list
	roundTrip := transaction.ToEthAuthorizationList(protoAuths)
	require.Len(t, roundTrip, 1)
	assert.Equal(t, ethAuths[0].ChainID.Uint64(), roundTrip[0].ChainID.Uint64())
	assert.Equal(t, ethAuths[0].Address, roundTrip[0].Address)
	assert.Equal(t, ethAuths[0].Nonce, roundTrip[0].Nonce)
}
