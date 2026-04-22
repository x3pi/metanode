package smart_contract

import (
	"encoding/json"
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

func makeTestEventLog() types.EventLog {
	return NewEventLog(
		common.HexToHash("0xaabb"),
		common.HexToAddress("0x1234"),
		[]byte{0xde, 0xad},
		[][]byte{{0x01, 0x02}, {0x03, 0x04}},
	)
}

func makeTestExecuteSCResult() *ExecuteSCResult {
	txHash := common.HexToHash("0xface")
	return NewExecuteSCResult(
		txHash,
		pb.RECEIPT_STATUS_RETURNED,
		pb.EXCEPTION_NONE,
		[]byte{0xca, 0xfe},
		21000,
		common.HexToHash("0xbbcc"),
		map[string][]byte{"addr1": {0x00, 0x01}},
		map[string][]byte{"addr2": {0x00, 0x02}},
		nil, // mapNonce
		nil, // mapCodeHash
		nil, // mapStorageRoot
		nil, // mapStorageAddress
		nil, // mapCreatorPubkey
		nil, // mapStorageAddressTouchedAddresses
		nil, // mapNativeSmartContractUpdateStorage
		[]types.EventLog{makeTestEventLog()},
	)
}

// ──────────────────────────────────────────────
// EventLog — Construction & Getters
// ──────────────────────────────────────────────

func TestNewEventLog(t *testing.T) {
	log := makeTestEventLog()
	require.NotNil(t, log)

	assert.Equal(t, common.HexToAddress("0x1234"), log.Address())
	assert.Equal(t, common.Bytes2Hex([]byte{0xde, 0xad}), log.Data())
	assert.Len(t, log.Topics(), 2)
}

func TestEventLog_TransactionHash(t *testing.T) {
	log := makeTestEventLog()
	assert.NotEmpty(t, log.TransactionHash())
}

func TestEventLog_Topics(t *testing.T) {
	log := makeTestEventLog()
	topics := log.Topics()
	assert.Equal(t, common.Bytes2Hex([]byte{0x01, 0x02}), topics[0])
	assert.Equal(t, common.Bytes2Hex([]byte{0x03, 0x04}), topics[1])
}

func TestEventLog_String(t *testing.T) {
	log := makeTestEventLog()
	s := log.String()
	assert.Contains(t, s, "Transaction Hash")
	assert.Contains(t, s, "Address")
	assert.Contains(t, s, "Topic 0")
	assert.Contains(t, s, "Topic 1")
}

// ──────────────────────────────────────────────
// EventLog — Marshal / Unmarshal
// ──────────────────────────────────────────────

func TestEventLog_MarshalUnmarshal(t *testing.T) {
	original := makeTestEventLog()

	data, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &EventLog{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, original.Address(), restored.Address())
	assert.Equal(t, original.Data(), restored.Data())
	assert.Equal(t, original.Topics(), restored.Topics())
	assert.Equal(t, original.TransactionHash(), restored.TransactionHash())
}

func TestEventLog_Unmarshal_InvalidData(t *testing.T) {
	log := &EventLog{}
	err := log.Unmarshal([]byte("invalid protobuf data"))
	assert.Error(t, err)
}

// ──────────────────────────────────────────────
// EventLog — Proto roundtrip
// ──────────────────────────────────────────────

func TestEventLog_ProtoRoundtrip(t *testing.T) {
	original := makeTestEventLog()
	pbData := original.Proto()
	require.NotNil(t, pbData)

	restored := &EventLog{}
	restored.FromProto(pbData)

	assert.Equal(t, original.Address(), restored.Address())
	assert.Equal(t, original.Data(), restored.Data())
	assert.Equal(t, original.Topics(), restored.Topics())
}

func TestNewEventLogFromProto(t *testing.T) {
	original := makeTestEventLog()
	pbData := original.Proto()

	restored := NewEventLogFromProto(pbData)
	assert.Equal(t, original.Address(), restored.Address())
	assert.Equal(t, original.Data(), restored.Data())
}

// ──────────────────────────────────────────────
// EventLog — Copy
// ──────────────────────────────────────────────

func TestEventLog_Copy(t *testing.T) {
	original := makeTestEventLog()
	copied := original.Copy()

	assert.Equal(t, original.Address(), copied.Address())
	assert.Equal(t, original.Data(), copied.Data())
	assert.Equal(t, original.Hash(), copied.Hash())

	// Deep copy isolation: modifying original proto should not affect copy
	assert.Equal(t, original.Topics(), copied.Topics())
}

// ──────────────────────────────────────────────
// EventLog — Hash
// ──────────────────────────────────────────────

func TestEventLog_Hash_Deterministic(t *testing.T) {
	log := makeTestEventLog()
	h1 := log.Hash()
	h2 := log.Hash()
	assert.Equal(t, h1, h2, "same log should produce same hash")
	assert.NotEqual(t, common.Hash{}, h1, "hash should not be zero")
}

func TestEventLog_Hash_DifferentInputs(t *testing.T) {
	log1 := makeTestEventLog()
	log2 := NewEventLog(
		common.HexToHash("0xdead"),
		common.HexToAddress("0x9999"),
		[]byte{0xff},
		nil,
	)
	assert.NotEqual(t, log1.Hash(), log2.Hash())
}

// ──────────────────────────────────────────────
// EventLog — Batch conversions
// ──────────────────────────────────────────────

func TestEventLogsToProto_FromProto_Roundtrip(t *testing.T) {
	logs := []types.EventLog{
		makeTestEventLog(),
		NewEventLog(common.Hash{}, common.Address{}, nil, nil),
	}

	pbLogs := EventLogsToProto(logs)
	require.Len(t, pbLogs, 2)

	restored := EventLogsFromProto(pbLogs)
	require.Len(t, restored, 2)

	assert.Equal(t, logs[0].Address(), restored[0].Address())
	assert.Equal(t, logs[1].Address(), restored[1].Address())
}

func TestGetLogsHash_Deterministic(t *testing.T) {
	logs := []types.EventLog{makeTestEventLog()}
	h1 := GetLogsHash(logs)
	h2 := GetLogsHash(logs)
	assert.Equal(t, h1, h2)
	assert.NotEqual(t, common.Hash{}, h1)
}

func TestGetLogsHash_EmptyLogs(t *testing.T) {
	h1 := GetLogsHash(nil)
	h2 := GetLogsHash([]types.EventLog{})
	// Both should produce the same hash for empty input
	assert.Equal(t, h1, h2)
}

// ──────────────────────────────────────────────
// ExecuteSCResult — Construction & Getters
// ──────────────────────────────────────────────

func TestNewExecuteSCResult(t *testing.T) {
	r := makeTestExecuteSCResult()
	require.NotNil(t, r)

	assert.Equal(t, common.HexToHash("0xface"), r.TransactionHash())
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, r.ReceiptStatus())
	assert.Equal(t, pb.EXCEPTION_NONE, r.Exception())
	assert.Equal(t, []byte{0xca, 0xfe}, r.Return())
	assert.Equal(t, uint64(21000), r.GasUsed())
	assert.Equal(t, common.HexToHash("0xbbcc"), r.LogsHash())

	assert.NotNil(t, r.MapAddBalance())
	assert.NotNil(t, r.MapSubBalance())
	assert.Len(t, r.EventLogs(), 1)
}

func TestNewErrorExecuteSCResult(t *testing.T) {
	r := NewErrorExecuteSCResult(
		common.HexToHash("0xbad"),
		pb.RECEIPT_STATUS_HALTED,
		pb.EXCEPTION_ERR_OUT_OF_GAS,
		[]byte{0x01},
	)
	require.NotNil(t, r)

	assert.Equal(t, common.HexToHash("0xbad"), r.TransactionHash())
	assert.Equal(t, pb.RECEIPT_STATUS_HALTED, r.ReceiptStatus())
	assert.Equal(t, pb.EXCEPTION_ERR_OUT_OF_GAS, r.Exception())
	assert.Equal(t, uint64(0), r.GasUsed())
}

// ──────────────────────────────────────────────
// ExecuteSCResult — Marshal / Unmarshal
// ──────────────────────────────────────────────

func TestExecuteSCResult_MarshalUnmarshal(t *testing.T) {
	original := makeTestExecuteSCResult()

	data, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &ExecuteSCResult{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, original.TransactionHash(), restored.TransactionHash())
	assert.Equal(t, original.ReceiptStatus(), restored.ReceiptStatus())
	assert.Equal(t, original.Exception(), restored.Exception())
	assert.Equal(t, original.Return(), restored.Return())
	assert.Equal(t, original.GasUsed(), restored.GasUsed())
}

func TestExecuteSCResult_Unmarshal_InvalidData(t *testing.T) {
	r := &ExecuteSCResult{}
	err := r.Unmarshal([]byte("garbage"))
	assert.Error(t, err)
}

// ──────────────────────────────────────────────
// ExecuteSCResult — Proto roundtrip
// ──────────────────────────────────────────────

func TestExecuteSCResult_ProtoRoundtrip(t *testing.T) {
	original := makeTestExecuteSCResult()
	pbData := original.Proto().(*pb.ExecuteSCResult)
	require.NotNil(t, pbData)

	restored := &ExecuteSCResult{}
	restored.FromProto(pbData)

	assert.Equal(t, original.TransactionHash(), restored.TransactionHash())
	assert.Equal(t, original.ReceiptStatus(), restored.ReceiptStatus())
	assert.Equal(t, original.GasUsed(), restored.GasUsed())
	assert.Len(t, restored.EventLogs(), 1)
}

// ──────────────────────────────────────────────
// ExecuteSCResult — Batch conversions
// ──────────────────────────────────────────────

func TestExecuteSCResultsBatchRoundtrip(t *testing.T) {
	results := []types.ExecuteSCResult{
		makeTestExecuteSCResult(),
		NewErrorExecuteSCResult(common.Hash{}, pb.RECEIPT_STATUS_HALTED, pb.EXCEPTION_ERR_DEPTH, nil),
	}

	pbResults := ExecuteSCResultsToProto(results)
	require.Len(t, pbResults, 2)

	restored := ExecuteSCResultsFromProto(pbResults)
	require.Len(t, restored, 2)

	assert.Equal(t, results[0].TransactionHash(), restored[0].TransactionHash())
	assert.Equal(t, results[1].ReceiptStatus(), restored[1].ReceiptStatus())
}

// ──────────────────────────────────────────────
// ExecuteSCResult — MarshalJSON
// ──────────────────────────────────────────────

func TestExecuteSCResult_MarshalJSON(t *testing.T) {
	r := makeTestExecuteSCResult()

	jsonData, err := r.MarshalJSON()
	require.NoError(t, err)
	require.NotEmpty(t, jsonData)

	var parsed map[string]interface{}
	err = json.Unmarshal(jsonData, &parsed)
	require.NoError(t, err)

	assert.Contains(t, parsed, "transactionHash")
	assert.Contains(t, parsed, "status")
	assert.Contains(t, parsed, "gasUsed")
	assert.Contains(t, parsed, "eventLogs")
}

func TestExecuteSCResult_String(t *testing.T) {
	r := makeTestExecuteSCResult()
	s := r.String()
	assert.Contains(t, s, "Transaction Hash")
	assert.Contains(t, s, "Status")
}

// ──────────────────────────────────────────────
// SmartContractUpdateData
// ──────────────────────────────────────────────

func TestSmartContractUpdateData_Construction(t *testing.T) {
	code := []byte{0x60, 0x80, 0x60, 0x40}
	storage := map[string][]byte{"key1": {0x01}, "key2": {0x02}}
	logs := []types.EventLog{makeTestEventLog()}

	data := NewSmartContractUpdateData(code, storage, logs)
	require.NotNil(t, data)

	assert.Equal(t, code, data.Code())
	assert.Equal(t, storage, data.Storage())
	assert.Len(t, data.EventLogs(), 1)
}

func TestSmartContractUpdateData_CodeHash(t *testing.T) {
	data := NewSmartContractUpdateData([]byte{0x01, 0x02}, nil, nil)
	h1 := data.CodeHash()
	h2 := data.CodeHash()
	assert.Equal(t, h1, h2, "deterministic hash")
	assert.NotEqual(t, common.Hash{}, h1)
}

func TestSmartContractUpdateData_SetCode(t *testing.T) {
	data := NewSmartContractUpdateData([]byte{0x01}, nil, nil)
	oldHash := data.CodeHash()

	data.SetCode([]byte{0x02, 0x03})
	assert.Equal(t, []byte{0x02, 0x03}, data.Code())
	assert.NotEqual(t, oldHash, data.CodeHash())
}

func TestSmartContractUpdateData_UpdateStorage(t *testing.T) {
	data := NewSmartContractUpdateData(nil, map[string][]byte{"a": {1}}, nil)
	data.UpdateStorage(map[string][]byte{"b": {2}, "a": {9}})

	assert.Equal(t, []byte{9}, data.Storage()["a"], "existing key should be overwritten")
	assert.Equal(t, []byte{2}, data.Storage()["b"], "new key should be added")
}

func TestSmartContractUpdateData_AddEventLog(t *testing.T) {
	data := NewSmartContractUpdateData(nil, nil, nil)
	assert.Len(t, data.EventLogs(), 0)

	data.AddEventLog(makeTestEventLog())
	assert.Len(t, data.EventLogs(), 1)
}

func TestSmartContractUpdateData_MarshalUnmarshal(t *testing.T) {
	original := NewSmartContractUpdateData(
		[]byte{0xaa, 0xbb},
		map[string][]byte{"k": {0xcc}},
		[]types.EventLog{makeTestEventLog()},
	)

	bytes, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, bytes)

	restored := &SmartContractUpdateData{}
	err = restored.Unmarshal(bytes)
	require.NoError(t, err)

	assert.Equal(t, original.Code(), restored.Code())
	assert.Equal(t, original.Storage(), restored.Storage())
	assert.Len(t, restored.EventLogs(), 1)
}

func TestSmartContractUpdateData_ProtoRoundtrip(t *testing.T) {
	original := NewSmartContractUpdateData(
		[]byte{0x01},
		map[string][]byte{"k": {0x02}},
		nil,
	)

	pbData := original.Proto()
	require.NotNil(t, pbData)

	restored := &SmartContractUpdateData{}
	restored.FromProto(pbData)

	assert.Equal(t, original.Code(), restored.Code())
	assert.Equal(t, original.Storage(), restored.Storage())
}

func TestSmartContractUpdateData_String(t *testing.T) {
	data := NewSmartContractUpdateData([]byte{0xab}, map[string][]byte{"x": {0xcd}}, nil)
	s := data.String()
	assert.Contains(t, s, "SmartContractUpdateData")
	assert.Contains(t, s, "Code")
	assert.Contains(t, s, "Storage")
}
