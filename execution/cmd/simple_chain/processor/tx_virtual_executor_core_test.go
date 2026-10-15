package processor

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
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
