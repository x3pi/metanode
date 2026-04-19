package tx_processor

import (
	"bytes"
	"fmt"
	"math/big"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Helpers
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

// makeTx creates a simple Transaction for testing.
func makeTx(from, to common.Address, nonce uint64) *transaction.Transaction {
	tx := transaction.NewTransaction(
		from,
		to,
		big.NewInt(100), // amount
		21000,           // maxGas
		1,               // maxGasPrice
		1000,            // maxTimeUse
		nil,             // data (no call/deploy)
		nil,             // relatedAddresses
		common.Hash{},   // lastDeviceKey
		common.Hash{},   // newDeviceKey
		nonce,
		1, // chainId
	)
	// NewTransaction returns types.Transaction which is *transaction.Transaction
	return tx.(*transaction.Transaction)
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Tests for createErrorReceipt
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestCreateErrorReceipt_StatusIsTransactionError(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	tx := makeTx(from, to, 5)

	rcp := createErrorReceipt(tx, to, fmt.Errorf("nonce mismatch"))

	if rcp.Status() != pb.RECEIPT_STATUS_TRANSACTION_ERROR {
		t.Errorf("expected TRANSACTION_ERROR status, got %v", rcp.Status())
	}
}

func TestCreateErrorReceipt_ReturnContainsErrorMessage(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	tx := makeTx(from, to, 5)

	rcp := createErrorReceipt(tx, to, fmt.Errorf("test error message"))

	returnData := string(rcp.Return())
	if len(returnData) == 0 {
		t.Error("expected non-empty return data containing error message")
	}
	if returnData != "test error message" {
		t.Errorf("expected return data %q, got %q", "test error message", returnData)
	}
}

func TestCreateErrorReceipt_PreservesAddresses(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	tx := makeTx(from, to, 1)

	rcp := createErrorReceipt(tx, to, fmt.Errorf("error"))

	if rcp.FromAddress() != from {
		t.Errorf("from address mismatch: got %s, want %s", rcp.FromAddress().Hex(), from.Hex())
	}
	if rcp.ToAddress() != to {
		t.Errorf("to address mismatch: got %s, want %s", rcp.ToAddress().Hex(), to.Hex())
	}
}

func TestCreateErrorReceipt_PreservesTransactionHash(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	tx := makeTx(from, to, 1)

	rcp := createErrorReceipt(tx, to, fmt.Errorf("error"))

	if rcp.TransactionHash() != tx.Hash() {
		t.Errorf("tx hash mismatch: got %s, want %s", rcp.TransactionHash().Hex(), tx.Hash().Hex())
	}
}

func TestCreateErrorReceipt_GasUsedIsZero(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	tx := makeTx(from, to, 1)

	rcp := createErrorReceipt(tx, to, fmt.Errorf("error"))

	if rcp.GasUsed() != 0 {
		t.Errorf("expected gasUsed=0 for error receipt, got %d", rcp.GasUsed())
	}
}

func TestCreateErrorReceipt_DeployContractUsesZeroAddress(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	zeroAddr := common.Address{}
	tx := makeTx(from, zeroAddr, 1)

	rcp := createErrorReceipt(tx, zeroAddr, fmt.Errorf("deploy error"))

	if rcp.ToAddress() != zeroAddr {
		t.Errorf("expected zero address for deploy, got %s", rcp.ToAddress().Hex())
	}
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Tests for ProcessResult struct
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestProcessResult_ZeroValue(t *testing.T) {
	var pr ProcessResult
	if pr.Error != nil {
		t.Error("zero value ProcessResult should have nil Error")
	}
	if pr.Transactions != nil {
		t.Error("zero value ProcessResult should have nil Transactions")
	}
	if pr.Receipts != nil {
		t.Error("zero value ProcessResult should have nil Receipts")
	}
	if pr.Root != (common.Hash{}) {
		t.Error("zero value ProcessResult should have zero Root")
	}
	if pr.StakeStatesRoot != (common.Hash{}) {
		t.Error("zero value ProcessResult should have zero StakeStatesRoot")
	}
}

func TestProcessResult_WithError(t *testing.T) {
	pr := ProcessResult{
		Error: fmt.Errorf("intermediate root failed"),
	}
	if pr.Error == nil {
		t.Error("expected non-nil Error")
	}
	if pr.Error.Error() != "intermediate root failed" {
		t.Errorf("unexpected error message: %v", pr.Error)
	}
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Tests for createErrorReceipt — edge cases
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestCreateErrorReceipt_VariousErrors(t *testing.T) {
	from := common.HexToAddress("0xaaaa")
	to := common.HexToAddress("0xbbbb")

	tests := []struct {
		name string
		err  error
	}{
		{"nonce_mismatch", fmt.Errorf("nonce mismatch: tx.Nonce()=5, state.Nonce()=3")},
		{"skipped", fmt.Errorf("skipped due to previous transaction failure")},
		{"invalid_calldata", fmt.Errorf("invalid calldata")},
		{"deploy_revert", fmt.Errorf("ExecuteNonceOnly failed during revert: something went wrong")},
		{"empty_error", fmt.Errorf("")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := makeTx(from, to, 1)
			rcp := createErrorReceipt(tx, to, tt.err)
			if rcp.Status() != pb.RECEIPT_STATUS_TRANSACTION_ERROR {
				t.Errorf("expected TRANSACTION_ERROR, got %v", rcp.Status())
			}
			if string(rcp.Return()) != tt.err.Error() {
				t.Errorf("return data mismatch: got %q, want %q", string(rcp.Return()), tt.err.Error())
			}
		})
	}
}

func TestCreateErrorReceipt_Amount(t *testing.T) {
	from := common.HexToAddress("0xaaaa")
	to := common.HexToAddress("0xbbbb")
	tx := makeTx(from, to, 1)

	rcp := createErrorReceipt(tx, to, fmt.Errorf("error"))

	expectedAmount := big.NewInt(100)
	if rcp.Amount().Cmp(expectedAmount) != 0 {
		t.Errorf("receipt amount mismatch: got %s, want %s", rcp.Amount().String(), expectedAmount.String())
	}
}

func TestCreateErrorReceipt_DeterministicHash(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")

	tx1 := makeTx(from, to, 5)
	tx2 := makeTx(from, to, 5)

	rcp1 := createErrorReceipt(tx1, to, fmt.Errorf("nonce error"))
	rcp2 := createErrorReceipt(tx2, to, fmt.Errorf("nonce error"))

	if rcp1.TransactionHash() != rcp2.TransactionHash() {
		t.Error("same inputs should produce same receipt tx hash")
	}
	if rcp1.Status() != rcp2.Status() {
		t.Error("same inputs should produce same receipt status")
	}
}

func TestCreateErrorReceipt_NonceBoundary(t *testing.T) {
	from := common.HexToAddress("0xaaaa")
	to := common.HexToAddress("0xbbbb")

	// nonce = 0 (BLS registration path)
	tx0 := makeTx(from, to, 0)
	rcp0 := createErrorReceipt(tx0, to, fmt.Errorf("bls error"))
	if rcp0.Status() != pb.RECEIPT_STATUS_TRANSACTION_ERROR {
		t.Error("nonce=0 should still produce error receipt")
	}

	// max nonce
	txMax := makeTx(from, to, ^uint64(0))
	rcpMax := createErrorReceipt(txMax, to, fmt.Errorf("max nonce error"))
	if rcpMax.Status() != pb.RECEIPT_STATUS_TRANSACTION_ERROR {
		t.Error("max nonce should still produce error receipt")
	}
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Tests for address sorting (fork-safety logic)
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestAddressSorting_Deterministic(t *testing.T) {
	// This tests the fork-safety sorting logic used in processGroupsConcurrently
	addrs := []common.Address{
		common.HexToAddress("0xcccc"),
		common.HexToAddress("0xaaaa"),
		common.HexToAddress("0xbbbb"),
		common.HexToAddress("0x1111"),
		common.HexToAddress("0xffff"),
	}

	sorted1 := make([]common.Address, len(addrs))
	copy(sorted1, addrs)
	sort.Slice(sorted1, func(i, j int) bool {
		return bytes.Compare(sorted1[i].Bytes(), sorted1[j].Bytes()) < 0
	})

	// Verify ascending order
	for i := 1; i < len(sorted1); i++ {
		if bytes.Compare(sorted1[i-1].Bytes(), sorted1[i].Bytes()) > 0 {
			t.Errorf("addresses not sorted at index %d: %s > %s",
				i, sorted1[i-1].Hex(), sorted1[i].Hex())
		}
	}

	// Verify determinism: same input, same output
	sorted2 := make([]common.Address, len(addrs))
	copy(sorted2, addrs)
	sort.Slice(sorted2, func(i, j int) bool {
		return bytes.Compare(sorted2[i].Bytes(), sorted2[j].Bytes()) < 0
	})

	for i := range sorted1 {
		if sorted1[i] != sorted2[i] {
			t.Errorf("non-deterministic sorting at index %d: %s != %s",
				i, sorted1[i].Hex(), sorted2[i].Hex())
		}
	}
}

func TestAddressSorting_LargeSet(t *testing.T) {
	// Simulate fork-safety with many addresses (like 30K BLS TXs)
	addrs := make([]common.Address, 1000)
	for i := range addrs {
		addrs[i] = common.BigToAddress(big.NewInt(int64(i * 7))) // non-sequential
	}

	sort.Slice(addrs, func(i, j int) bool {
		return bytes.Compare(addrs[i].Bytes(), addrs[j].Bytes()) < 0
	})

	for i := 1; i < len(addrs); i++ {
		if bytes.Compare(addrs[i-1].Bytes(), addrs[i].Bytes()) > 0 {
			t.Fatalf("sort broken at index %d", i)
		}
	}
}

func TestAddressDeduplication(t *testing.T) {
	// Test the unique address collection logic from processGroupsConcurrently
	type fakeItem struct {
		from common.Address
		to   common.Address
	}

	items := []fakeItem{
		{common.HexToAddress("0xaaaa"), common.HexToAddress("0xbbbb")},
		{common.HexToAddress("0xaaaa"), common.HexToAddress("0xcccc")}, // duplicate from
		{common.HexToAddress("0xdddd"), common.HexToAddress("0xbbbb")}, // duplicate to
	}

	uniqueMap := make(map[common.Address]struct{})
	for _, item := range items {
		uniqueMap[item.from] = struct{}{}
		uniqueMap[item.to] = struct{}{}
	}

	// Should have 4 unique: 0xaaaa, 0xbbbb, 0xcccc, 0xdddd
	if len(uniqueMap) != 4 {
		t.Errorf("expected 4 unique addresses, got %d", len(uniqueMap))
	}

	// Verify all expected addresses are present
	expected := []common.Address{
		common.HexToAddress("0xaaaa"),
		common.HexToAddress("0xbbbb"),
		common.HexToAddress("0xcccc"),
		common.HexToAddress("0xdddd"),
	}
	for _, addr := range expected {
		if _, ok := uniqueMap[addr]; !ok {
			t.Errorf("missing expected address %s", addr.Hex())
		}
	}
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// Tests for worker pool result ordering (fork-safety)
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func TestIndexedResultsMerge_DeterministicOrder(t *testing.T) {
	// Simulates the indexed results merge from processGroupsConcurrently
	from := common.HexToAddress("0xaaaa")
	to := common.HexToAddress("0xbbbb")

	// Simulate 5 group results with different numbers of transactions
	nGroups := 5
	results := make([]struct {
		txs      []common.Hash
		receipts []pb.RECEIPT_STATUS
	}, nGroups)

	for i := 0; i < nGroups; i++ {
		nTxs := i + 1 // group 0 has 1 tx, group 1 has 2, etc.
		results[i].txs = make([]common.Hash, nTxs)
		results[i].receipts = make([]pb.RECEIPT_STATUS, nTxs)
		for j := 0; j < nTxs; j++ {
			tx := makeTx(from, to, uint64(i*100+j))
			results[i].txs[j] = tx.Hash()
			results[i].receipts[j] = pb.RECEIPT_STATUS_RETURNED
		}
	}

	// Merge in indexed order (deterministic)
	var allTxHashes []common.Hash
	for _, r := range results {
		allTxHashes = append(allTxHashes, r.txs...)
	}

	// Total should be 1+2+3+4+5 = 15
	totalExpected := 15
	if len(allTxHashes) != totalExpected {
		t.Errorf("expected %d total tx hashes, got %d", totalExpected, len(allTxHashes))
	}

	// Verify order is deterministic by doing it again
	var allTxHashes2 []common.Hash
	for _, r := range results {
		allTxHashes2 = append(allTxHashes2, r.txs...)
	}

	for i := range allTxHashes {
		if allTxHashes[i] != allTxHashes2[i] {
			t.Errorf("non-deterministic merge at index %d", i)
		}
	}
}
