package transaction

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// ──────────────────────────────────────────────
// TransactionError — Proto roundtrip
// ──────────────────────────────────────────────

func TestTransactionError_ProtoRoundtrip(t *testing.T) {
	original := &TransactionError{Code: 42, Description: "test error"}
	pbData := original.Proto()
	require.NotNil(t, pbData)
	assert.Equal(t, int64(42), pbData.Code)
	assert.Equal(t, "test error", pbData.Description)

	restored := &TransactionError{}
	restored.FromProto(pbData)
	assert.Equal(t, original.Code, restored.Code)
	assert.Equal(t, original.Description, restored.Description)
}

// ──────────────────────────────────────────────
// TransactionError — Marshal / Unmarshal
// ──────────────────────────────────────────────

func TestTransactionError_MarshalUnmarshal(t *testing.T) {
	original := &TransactionError{Code: 10, Description: "invalid stake address"}

	data, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &TransactionError{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, original.Code, restored.Code)
	assert.Equal(t, original.Description, restored.Description)
}

func TestTransactionError_Unmarshal_InvalidData(t *testing.T) {
	te := &TransactionError{}
	err := te.Unmarshal([]byte("invalid protobuf data"))
	assert.Error(t, err)
}

// ──────────────────────────────────────────────
// TransactionHashWithErrorCode
// ──────────────────────────────────────────────

func TestTransactionHashWithErrorCode_Construction(t *testing.T) {
	txHash := common.HexToHash("0xabcdef")
	thec := NewTransactionHashWithErrorCode(txHash, 18)
	require.NotNil(t, thec)
}

func TestTransactionHashWithErrorCode_ProtoRoundtrip(t *testing.T) {
	txHash := common.HexToHash("0xface")
	original := NewTransactionHashWithErrorCode(txHash, 26)

	pbData := original.Proto()
	require.NotNil(t, pbData)
	assert.Equal(t, int64(26), pbData.Code)
	assert.Equal(t, txHash.Bytes(), pbData.TransactionHash)

	restored := &TransactionHashWithErrorCode{}
	restored.FromProto(pbData)
	assert.Equal(t, txHash, restored.transactionHash)
	assert.Equal(t, int64(26), restored.errorCode)
}

func TestTransactionHashWithErrorCode_MarshalUnmarshal(t *testing.T) {
	txHash := common.HexToHash("0xdead")
	original := NewTransactionHashWithErrorCode(txHash, 5)

	data, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &TransactionHashWithErrorCode{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, txHash, restored.transactionHash)
	assert.Equal(t, int64(5), restored.errorCode)
}

func TestTransactionHashWithErrorCode_Unmarshal_InvalidData(t *testing.T) {
	thec := &TransactionHashWithErrorCode{}
	err := thec.Unmarshal([]byte("garbage"))
	assert.Error(t, err)
}

// ──────────────────────────────────────────────
// TransactionHashWithError
// ──────────────────────────────────────────────

func TestTransactionHashWithError_Construction(t *testing.T) {
	txHash := common.HexToHash("0xbeef")
	the := NewTransactionHashWithError(txHash, 50, "execution reverted", []byte{0x08, 0xc3})
	require.NotNil(t, the)
}

func TestTransactionHashWithError_ProtoRoundtrip(t *testing.T) {
	txHash := common.HexToHash("0xcafe")
	original := NewTransactionHashWithError(txHash, 48, "insufficient balance", []byte{0x01})

	pbData := original.Proto()
	require.NotNil(t, pbData)
	assert.Equal(t, int64(48), pbData.Code)
	assert.Equal(t, "insufficient balance", pbData.Description)
	assert.Equal(t, []byte{0x01}, pbData.Output)

	restored := &TransactionHashWithError{}
	restored.FromProto(pbData)
	assert.Equal(t, txHash, restored.hash)
	assert.Equal(t, int64(48), restored.errorCode)
	assert.Equal(t, "insufficient balance", restored.description)
	assert.Equal(t, []byte{0x01}, restored.output)
}

func TestTransactionHashWithError_MarshalUnmarshal(t *testing.T) {
	txHash := common.HexToHash("0x1234")
	original := NewTransactionHashWithError(txHash, 45, "out of gas", nil)

	data, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &TransactionHashWithError{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, txHash, restored.hash)
	assert.Equal(t, int64(45), restored.errorCode)
	assert.Equal(t, "out of gas", restored.description)
}

func TestTransactionHashWithError_Unmarshal_InvalidData(t *testing.T) {
	the := &TransactionHashWithError{}
	err := the.Unmarshal([]byte("not valid"))
	assert.Error(t, err)
}

// ──────────────────────────────────────────────
// MapProtoExceptionToTransactionError
// ──────────────────────────────────────────────

func TestMapProtoExceptionToTransactionError_AllKnown(t *testing.T) {
	tests := []struct {
		exception pb.EXCEPTION
		expected  *TransactionError
	}{
		{pb.EXCEPTION_ERR_OUT_OF_GAS, ErrOutOfGas},
		{pb.EXCEPTION_ERR_CODE_STORE_OUT_OF_GAS, ErrCodeStoreOutOfGas},
		{pb.EXCEPTION_ERR_DEPTH, ErrDepth},
		{pb.EXCEPTION_ERR_INSUFFICIENT_BALANCE, ErrInsufficientBalance},
		{pb.EXCEPTION_ERR_CONTRACT_ADDRESS_COLLISION, ErrContractAddressCollision},
		{pb.EXCEPTION_ERR_EXECUTION_REVERTED, ErrExecutionReverted},
		{pb.EXCEPTION_ERR_MAX_CODE_SIZE_EXCEEDED, ErrMaxCodeSizeExceeded},
		{pb.EXCEPTION_ERR_INVALID_JUMP, ErrInvalidJump},
		{pb.EXCEPTION_ERR_WRITE_PROTECTION, ErrWriteProtection},
		{pb.EXCEPTION_ERR_RETURN_DATA_OUT_OF_BOUNDS, ErrReturnDataOutOfBounds},
		{pb.EXCEPTION_ERR_GAS_UINT_OVERFLOW, ErrGasUintOverflow},
		{pb.EXCEPTION_ERR_INVALID_CODE, ErrInvalidCode},
		{pb.EXCEPTION_ERR_NONCE_UINT_OVERFLOW, ErrNonceUintOverflow},
		{pb.EXCEPTION_ERR_OUT_OF_BOUNDS, ErrOutOfBounds},
		{pb.EXCEPTION_ERR_OVERFLOW, ErrOverflow},
		{pb.EXCEPTION_ERR_ADDRESS_NOT_IN_RELATED, ErrAddressNotInRelated},
		{pb.EXCEPTION_NONE, ErrNone},
	}

	for _, tt := range tests {
		t.Run(tt.exception.String(), func(t *testing.T) {
			result := MapProtoExceptionToTransactionError(tt.exception)
			require.NotNil(t, result, "mapping for %v should not be nil", tt.exception)
			assert.Equal(t, tt.expected.Code, result.Code)
			assert.Equal(t, tt.expected.Description, result.Description)
		})
	}
}

func TestMapProtoExceptionToTransactionError_Unknown(t *testing.T) {
	result := MapProtoExceptionToTransactionError(pb.EXCEPTION(9999))
	assert.Nil(t, result, "unknown exception should return nil")
}

// ──────────────────────────────────────────────
// CodeToError map
// ──────────────────────────────────────────────

func TestCodeToError_AllCodesPresent(t *testing.T) {
	// Verify the map covers codes 1-68 continuously
	for code := int64(1); code <= 68; code++ {
		err, ok := CodeToError[code]
		assert.True(t, ok, "CodeToError should have code %d", code)
		assert.Equal(t, code, err.Code, "error code mismatch for code %d", code)
		assert.NotEmpty(t, err.Description, "description should not be empty for code %d", code)
	}
}

func TestCodeToError_UnknownCode(t *testing.T) {
	_, ok := CodeToError[999]
	assert.False(t, ok, "unknown code should not be in the map")
}
