package receipt

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ──────────────────────────────────────────────
// Helper
// ──────────────────────────────────────────────

func makeTestReceipt() *Receipt {
	r := NewReceipt(
		common.HexToHash("0xabcd1234"),  // transactionHash
		common.HexToAddress("0xaa"),     // fromAddress
		common.HexToAddress("0xbb"),     // toAddress
		big.NewInt(1000),                // amount
		pb.RECEIPT_STATUS_RETURNED,      // status
		[]byte("return data"),           // returnValue
		pb.EXCEPTION_NONE,               // exception
		500,                             // gasFee
		300,                             // gasUsed
		nil,                             // eventLogs
		7,                               // transactionIndex
		common.HexToHash("0xblockhash"), // blockHash
		42,                              // blockNumber
	)
	return r.(*Receipt)
}

// ──────────────────────────────────────────────
// NewReceipt
// ──────────────────────────────────────────────

func TestNewReceipt(t *testing.T) {
	r := makeTestReceipt()
	require.NotNil(t, r)

	assert.Equal(t, common.HexToHash("0xabcd1234"), r.TransactionHash())
	assert.Equal(t, common.HexToAddress("0xaa"), r.FromAddress())
	assert.Equal(t, common.HexToAddress("0xbb"), r.ToAddress())
	assert.Equal(t, big.NewInt(1000), r.Amount())
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, r.Status())
	assert.Equal(t, []byte("return data"), r.Return())
	assert.Equal(t, pb.EXCEPTION_NONE, r.Exception())
	assert.Equal(t, uint64(500), r.GasFee())
	assert.Equal(t, uint64(300), r.GasUsed())
	assert.Equal(t, uint64(7), r.TransactionIndex())
}

func TestNewReceipt_ErrorStatus(t *testing.T) {
	r := NewReceipt(
		common.Hash{}, common.Address{}, common.Address{},
		big.NewInt(0), pb.RECEIPT_STATUS_HALTED,
		nil, pb.EXCEPTION_NONE, 0, 0, nil, 0, common.Hash{}, 0,
	).(*Receipt)

	// Halted status should be normalized to TRANSACTION_ERROR
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, r.Status())
}

func TestNewReceipt_InvalidCodeException(t *testing.T) {
	r := NewReceipt(
		common.Hash{}, common.Address{}, common.Address{},
		big.NewInt(0), pb.RECEIPT_STATUS_TRANSACTION_ERROR,
		nil, pb.EXCEPTION_ERR_INVALID_CODE, 0, 0, nil, 0, common.Hash{}, 0,
	).(*Receipt)

	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, r.Status())
	assert.NotEmpty(t, r.Return(), "should encode revert reason for invalid code")
}

// ──────────────────────────────────────────────
// Getters / Setters
// ──────────────────────────────────────────────

func TestReceipt_SetRHash(t *testing.T) {
	r := makeTestReceipt()
	rHash := common.HexToHash("0xdeadbeef")
	r.SetRHash(rHash)
	assert.Equal(t, rHash, r.RHash())
}

func TestReceipt_SetToAddress(t *testing.T) {
	r := makeTestReceipt()
	newAddr := common.HexToAddress("0xcc")
	r.SetToAddress(newAddr)
	assert.Equal(t, newAddr, r.ToAddress())
}

func TestReceipt_SetReturn(t *testing.T) {
	r := makeTestReceipt()
	r.SetReturn([]byte("new return"))
	assert.Equal(t, []byte("new return"), r.Return())
}

func TestReceipt_SetProcessingType(t *testing.T) {
	r := makeTestReceipt()
	r.SetProcessingType(pb.RECEIPT_PROCESSING_TYPE_PRE_COMMIT_NOTIFICATION)
	assert.Equal(t, pb.RECEIPT_PROCESSING_TYPE_PRE_COMMIT_NOTIFICATION, r.ProcessingType())
}

func TestReceipt_ToTypes(t *testing.T) {
	r := makeTestReceipt()
	t2 := r.ToTypes()
	require.NotNil(t, t2)
	assert.Equal(t, r.TransactionHash(), t2.TransactionHash())
}

// ──────────────────────────────────────────────
// Marshal / Unmarshal
// ──────────────────────────────────────────────

func TestReceipt_MarshalUnmarshal(t *testing.T) {
	original := makeTestReceipt()

	data, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &Receipt{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, original.TransactionHash(), restored.TransactionHash())
	assert.Equal(t, original.FromAddress(), restored.FromAddress())
	assert.Equal(t, original.ToAddress(), restored.ToAddress())
	assert.Equal(t, original.Amount(), restored.Amount())
	assert.Equal(t, original.Status(), restored.Status())
	assert.Equal(t, original.GasUsed(), restored.GasUsed())
	assert.Equal(t, original.GasFee(), restored.GasFee())
	assert.Equal(t, original.TransactionIndex(), restored.TransactionIndex())
}

func TestReceipt_Unmarshal_InvalidData(t *testing.T) {
	r := &Receipt{}
	err := r.Unmarshal([]byte("invalid"))
	assert.Error(t, err)
}

// ──────────────────────────────────────────────
// Proto
// ──────────────────────────────────────────────

func TestReceipt_ProtoRoundtrip(t *testing.T) {
	original := makeTestReceipt()
	p := original.Proto().(*pb.Receipt)
	require.NotNil(t, p)

	restored := ReceiptFromProto(p).(*Receipt)
	assert.Equal(t, original.TransactionHash(), restored.TransactionHash())
	assert.Equal(t, original.FromAddress(), restored.FromAddress())
	assert.Equal(t, original.ToAddress(), restored.ToAddress())
}

// ──────────────────────────────────────────────
// Batch conversions
// ──────────────────────────────────────────────

func TestReceiptsToProto_RoundTrip(t *testing.T) {
	r1 := makeTestReceipt()
	r2 := NewReceipt(
		common.HexToHash("0x9999"), common.HexToAddress("0xcc"),
		common.HexToAddress("0xdd"), big.NewInt(2000),
		pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
		100, 50, nil, 1, common.Hash{}, 10,
	)

	protos := ReceiptsToProto([]types.Receipt{r1, r2})
	require.Len(t, protos, 2)

	restored := ProtoToReceipts(protos)
	require.Len(t, restored, 2)
	assert.Equal(t, r1.TransactionHash(), restored[0].TransactionHash())
	assert.Equal(t, r2.TransactionHash(), restored[1].TransactionHash())
}

// ──────────────────────────────────────────────
// UpdateExecuteResult
// ──────────────────────────────────────────────

func TestReceipt_UpdateExecuteResult(t *testing.T) {
	r := makeTestReceipt()
	r.UpdateExecuteResult(
		pb.RECEIPT_STATUS_RETURNED,
		[]byte("new result"),
		pb.EXCEPTION_NONE,
		999,
		nil,
	)

	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, r.Status())
	assert.Equal(t, []byte("new result"), r.Return())
	assert.Equal(t, uint64(999), r.GasUsed())
}

func TestReceipt_UpdateExecuteResult_HaltedStatus(t *testing.T) {
	r := makeTestReceipt()
	r.UpdateExecuteResult(
		pb.RECEIPT_STATUS_HALTED,
		nil,
		pb.EXCEPTION_NONE,
		100,
		nil,
	)

	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, r.Status())
	assert.NotEmpty(t, r.Return(), "halted should encode revert reason")
}

// ──────────────────────────────────────────────
// JSON
// ──────────────────────────────────────────────

func TestReceipt_MarshalJSON(t *testing.T) {
	r := makeTestReceipt()
	data, err := r.MarshalJSON()
	require.NoError(t, err)
	require.NotEmpty(t, data)
	// Verify it's valid JSON
	assert.Contains(t, string(data), "transaction_hash")
	assert.Contains(t, string(data), "from_address")
}

func TestReceipt_Json(t *testing.T) {
	r := makeTestReceipt()
	data, err := r.Json()
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

func TestReceipt_MarshalReceiptToMap(t *testing.T) {
	r := makeTestReceipt()
	m, err := r.MarshalReceiptToMap()
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Contains(t, m, "transaction_hash")
	assert.Contains(t, m, "from_address")
	assert.Contains(t, m, "gas_fee")
	assert.Contains(t, m, "gas_used")
}

// ──────────────────────────────────────────────
// String
// ──────────────────────────────────────────────

func TestReceipt_String(t *testing.T) {
	r := makeTestReceipt()
	s := r.String()
	assert.Contains(t, s, "Transaction hash")
	assert.Contains(t, s, "From address")
}

// ──────────────────────────────────────────────
// File I/O
// ──────────────────────────────────────────────

func TestSaveAndLoadReceiptFromFile(t *testing.T) {
	r := makeTestReceipt()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test_receipt.dat")

	err := SaveReceiptToFile(r, filePath)
	require.NoError(t, err)

	loaded, err := LoadReceiptFromFile(filePath)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, r.TransactionHash(), loaded.TransactionHash())
	assert.Equal(t, r.FromAddress(), loaded.FromAddress())
	assert.Equal(t, r.Amount(), loaded.Amount())
}

func TestLoadReceiptFromFile_NotFound(t *testing.T) {
	_, err := LoadReceiptFromFile("/nonexistent/path/receipt.dat")
	assert.Error(t, err)
}

func TestLoadReceiptByHash_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := LoadReceiptByHash(tmpDir+"/", common.HexToHash("0xdeadbeef"))
	assert.Error(t, err)
	assert.Equal(t, ErrorReceiptNotFound, err)
}

func TestSaveReceiptToFile_CreatesDirs(t *testing.T) {
	r := makeTestReceipt()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "sub", "dir", "receipt.dat")

	err := SaveReceiptToFile(r, filePath)
	require.NoError(t, err)

	_, err = os.Stat(filePath)
	assert.NoError(t, err, "file should exist")
}
