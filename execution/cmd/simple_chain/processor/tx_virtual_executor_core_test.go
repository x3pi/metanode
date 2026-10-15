package processor

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

func TestTxVirtualExecutor_ParseReadTx_Protobuf(t *testing.T) {
	v := &TxVirtualExecutor{}

	from := common.HexToAddress("0x1234567890123456789012345678901234567890")
	to := common.HexToAddress("0x0987654321098765432109876543210987654321")
	amount := big.NewInt(1000)

	tx := transaction.NewTransaction(
		from,
		to,
		amount,
		50000,
		1,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		1,
		0,
	).(*transaction.Transaction)

	txBytes, err := tx.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal tx: %v", err)
	}

	parsedTx, err := v.parseReadTx(txBytes)
	if err != nil {
		t.Fatalf("parseReadTx failed for protobuf: %v", err)
	}
	if parsedTx.FromAddress() != from || parsedTx.ToAddress() != to {
		t.Fatalf("parsed protobuf tx address mismatch")
	}
}

func TestTxVirtualExecutor_ParseReadTx_EmptyBody(t *testing.T) {
	v := &TxVirtualExecutor{}
	_, err := v.parseReadTx([]byte{})
	if err == nil {
		t.Fatalf("expected error for empty body, got nil")
	}
}

func TestTxVirtualExecutor_ParseReadTx_ChainIDMatchAndMismatch(t *testing.T) {
	chainState := &blockchain.ChainState{}
	chainState.SetConfig(&config.SimpleChainConfig{
		ChainId: big.NewInt(1337),
	})
	v := &TxVirtualExecutor{
		chainState: chainState,
	}

	from := common.HexToAddress("0x1234567890123456789012345678901234567890")
	to := common.HexToAddress("0x0987654321098765432109876543210987654321")

	// Case 1: Matching chain ID 1337 -> MUST PASS
	txMatch := transaction.NewTransaction(
		from,
		to,
		big.NewInt(0),
		50000,
		1,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		1,
		1337,
	).(*transaction.Transaction)
	bytesMatch, _ := txMatch.Marshal()
	parsedMatch, err := v.parseReadTx(bytesMatch)
	if err != nil {
		t.Fatalf("expected matching chainId to succeed, got error: %v", err)
	}
	if parsedMatch.GetChainID() != 1337 {
		t.Fatalf("expected chainId 1337, got: %d", parsedMatch.GetChainID())
	}

	// Case 2: Chain ID = 0 when node requires 1337 -> MUST FAIL
	txZero := transaction.NewTransaction(
		from,
		to,
		big.NewInt(0),
		50000,
		1,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		1,
		0,
	).(*transaction.Transaction)
	bytesZero, _ := txZero.Marshal()
	_, err = v.parseReadTx(bytesZero)
	if err == nil {
		t.Fatalf("expected error for chainId=0 when node has chainId=1337, got nil")
	}
	if !strings.Contains(err.Error(), "chainId does not match node's") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Case 3: Tx with mismatched chain ID 9999 -> MUST FAIL
	txMismatch := transaction.NewTransaction(
		from,
		to,
		big.NewInt(0),
		50000,
		1,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		1,
		9999,
	).(*transaction.Transaction)

	bytesMismatch, _ := txMismatch.Marshal()
	_, err = v.parseReadTx(bytesMismatch)
	if err == nil {
		t.Fatalf("expected chainId mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "chainId does not match node's") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestPlainTransferEstimateGas(t *testing.T) {
	from := common.HexToAddress("0x1234567890123456789012345678901234567890")
	to := common.HexToAddress("0x0987654321098765432109876543210987654321")
	tx := transaction.NewTransaction(from, to, big.NewInt(1), 50000, 1, 0, nil, nil, common.Hash{}, common.Hash{}, 1, 0).(*transaction.Transaction)

	// A plain transfer owes only the minimum base fee.
	if got := plainTransferEstimateGas(tx); got != mt_common.MINIMUM_BASE_FEE {
		t.Fatalf("plain transfer: got %d, want %d", got, mt_common.MINIMUM_BASE_FEE)
	}

	// A SetCode tx also owes the intrinsic gas of every authorization tuple (same rule as HTTP eth_estimateGas).
	tx.SetAuthorizationList(transaction.FromEthAuthorizationList([]e_types.SetCodeAuthorization{{}, {}}))
	want := params.TxGas + 2*params.CallNewAccountGas + mt_common.MINIMUM_BASE_FEE
	if got := plainTransferEstimateGas(tx); got != want {
		t.Fatalf("SetCode tx with 2 tuples: got %d, want %d", got, want)
	}
}

// Clients matched on this wording before the read path accepted plain transfers; keep it stable.
func TestErrNotSmartContractCallWording(t *testing.T) {
	if errNotSmartContractCall != "not a smart contract call" {
		t.Fatalf("wording changed: %q", errNotSmartContractCall)
	}
}
