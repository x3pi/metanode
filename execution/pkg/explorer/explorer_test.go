package explorer_test

import (
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/explorer"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

func TestExplorerTransactionMarshalAndIndexFast(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	chainID := big.NewInt(991)
	signer := e_types.LatestSignerForChainID(chainID)
	toAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	ethTx, err := e_types.SignNewTx(key, signer, &e_types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     7,
		GasTipCap: big.NewInt(1_000_000_000),
		GasFeeCap: big.NewInt(2_000_000_000),
		Gas:       21000,
		To:        &toAddr,
		Value:     big.NewInt(123456),
		Data:      []byte{0xde, 0xad, 0xbe, 0xef},
	})
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}
	tx, err := transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		t.Fatalf("NewTransactionFromEth: %v", err)
	}

	header := block.NewBlockHeader(
		common.HexToHash("0x1111"),
		42,
		common.HexToHash("0x2222"),
		common.HexToHash("0x3333"),
		common.HexToHash("0x4444"),
		common.HexToAddress("0xaaaa"),
		1700000000,
		common.HexToHash("0x5555"),
		3,
		100,
	)
	rcpt := receipt.NewReceipt(
		tx.Hash(),
		tx.FromAddress(),
		toAddr,
		big.NewInt(123456),
		pb.RECEIPT_STATUS_RETURNED,
		[]byte{0x01, 0x02},
		pb.EXCEPTION_NONE,
		42000,
		21000,
		nil,
		0,
		header.Hash(),
		header.BlockNumber(),
	)

	et := explorer.NewExplorerTransaction(tx, rcpt, header)
	jsonStr, err := et.ToJSONString()
	if err != nil {
		t.Fatalf("ToJSONString: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if parsed["hash"] != tx.Hash().Hex() {
		t.Fatalf("hash mismatch: got %v want %v", parsed["hash"], tx.Hash().Hex())
	}
	if parsed["blockHash"] != header.Hash().Hex() {
		t.Fatalf("blockHash mismatch: got %v want %v", parsed["blockHash"], header.Hash().Hex())
	}

	dir, err := os.MkdirTemp("", "explorer-svc-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	svc, err := explorer.NewExplorerSearchService(dir, 1024, 4)
	if err != nil {
		t.Fatalf("NewExplorerSearchService: %v", err)
	}
	if err := svc.IndexTransactionFast(tx, rcpt, header, header.Hash().Hex(), false); err != nil {
		t.Fatalf("IndexTransactionFast: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	svc2, err := explorer.NewExplorerSearchService(dir, 1024, 4)
	if err != nil {
		t.Fatalf("Reopen NewExplorerSearchService: %v", err)
	}
	defer svc2.Close()

	docs, total, err := svc2.SearchTransactions("hash:"+strings.ToLower(tx.Hash().Hex()), 0, 10)
	if err != nil {
		t.Fatalf("SearchTransactions: %v", err)
	}
	if total != 1 || len(docs) != 1 {
		t.Fatalf("expected 1 search result, got total=%d len=%d", total, len(docs))
	}
	if docs[0] != jsonStr {
		t.Fatalf("indexed JSON mismatch:\ngot:  %s\nwant: %s", docs[0], jsonStr)
	}
}
