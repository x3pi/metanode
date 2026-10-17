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

func TestExplorerBatchDrainAndMultiShardSearch(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	chainID := big.NewInt(991)
	signer := e_types.LatestSignerForChainID(chainID)
	toAddr := common.HexToAddress("0x7777777777777777777777777777777777777777")

	dir, err := os.MkdirTemp("", "explorer-batch-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	svc, err := explorer.NewExplorerSearchService(dir, 1024, 8)
	if err != nil {
		t.Fatalf("NewExplorerSearchService: %v", err)
	}

	header := block.NewBlockHeader(
		common.HexToHash("0x1111"),
		100,
		common.HexToHash("0x2222"),
		common.HexToHash("0x3333"),
		common.HexToHash("0x4444"),
		common.HexToAddress("0xaaaa"),
		1700000000,
		common.HexToHash("0x5555"),
		3,
		100,
	)

	const txCount = 50
	var hashes []string

	for i := 0; i < txCount; i++ {
		ethTx, err := e_types.SignNewTx(key, signer, &e_types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     uint64(i),
			GasTipCap: big.NewInt(1_000_000_000),
			GasFeeCap: big.NewInt(2_000_000_000),
			Gas:       21000,
			To:        &toAddr,
			Value:     big.NewInt(int64(i + 1)),
		})
		if err != nil {
			t.Fatalf("SignNewTx[%d]: %v", i, err)
		}
		tx, err := transaction.NewTransactionFromEth(ethTx)
		if err != nil {
			t.Fatalf("NewTransactionFromEth[%d]: %v", i, err)
		}
		rcpt := receipt.NewReceipt(
			tx.Hash(),
			tx.FromAddress(),
			toAddr,
			big.NewInt(int64(i+1)),
			pb.RECEIPT_STATUS_RETURNED,
			nil,
			pb.EXCEPTION_NONE,
			21000,
			21000,
			nil,
			uint64(i),
			header.Hash(),
			header.BlockNumber(),
		)
		hashes = append(hashes, strings.ToLower(tx.Hash().Hex()))
		if err := svc.IndexTransactionFast(tx, rcpt, header, header.Hash().Hex(), false); err != nil {
			t.Fatalf("IndexTransactionFast[%d]: %v", i, err)
		}
	}

	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and verify search queries
	svc2, err := explorer.NewExplorerSearchService(dir, 1024, 8)
	if err != nil {
		t.Fatalf("Reopen svc: %v", err)
	}
	defer svc2.Close()

	// 1. Search by block number: must return all 50 txs
	docs, total, err := svc2.SearchTransactions("block:100", 0, 100)
	if err != nil {
		t.Fatalf("SearchTransactions block:100: %v", err)
	}
	if total != txCount || len(docs) != txCount {
		t.Fatalf("expected %d docs for block:100, got total=%d len=%d", txCount, total, len(docs))
	}

	// 2. Search by individual hash for first 5 txs
	for i := 0; i < 5; i++ {
		docs, total, err := svc2.SearchTransactions("hash:"+hashes[i], 0, 10)
		if err != nil {
			t.Fatalf("SearchTransactions hash:%s: %v", hashes[i], err)
		}
		if total != 1 || len(docs) != 1 {
			t.Fatalf("expected 1 doc for hash %s, got total=%d len=%d", hashes[i], total, len(docs))
		}
	}
}

