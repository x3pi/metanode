package transaction

import (
	"errors"
	"fmt"
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
	// Verify the map covers codes 1-87 continuously
	for code := int64(1); code <= 87; code++ {
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

// ──────────────────────────────────────────────
// TransactionError — Error(), Is(), errors.As()
// ──────────────────────────────────────────────

func TestTransactionError_ErrorAndIs(t *testing.T) {
	var nilErr *TransactionError
	assert.Equal(t, "", nilErr.Error())
	assert.True(t, nilErr.Is(nil))
	assert.False(t, nilErr.Is(ErrNonceTooLow))

	assert.Equal(t, "nonce too low", ErrNonceTooLow.Error())
	assert.True(t, errors.Is(ErrNonceTooLow, ErrNonceTooLow))

	// Semantic aliases
	assert.True(t, errors.Is(InvalidNonce, ErrNonceTooLow))
	assert.True(t, errors.Is(ErrNonceTooLow, InvalidNonce))

	assert.True(t, errors.Is(ErrInsufficientBalance, ErrInsufficientFunds))
	assert.True(t, errors.Is(InvalidMaxFee, ErrInsufficientFunds))
	assert.True(t, errors.Is(InvalidAmount, ErrInsufficientFunds))
	assert.True(t, errors.Is(ErrInsufficientFunds, ErrInsufficientBalance))

	assert.True(t, errors.Is(InvalidSign, ErrInvalidSender))
	assert.True(t, errors.Is(InvalidSignSecp, ErrInvalidSender))
	assert.True(t, errors.Is(ErrSenderRecovery, ErrInvalidSender))
	assert.True(t, errors.Is(ErrInvalidSender, InvalidSign))

	assert.True(t, errors.Is(InvalidMaxGas, ErrIntrinsicGasTooLow))
	assert.True(t, errors.Is(ErrIntrinsicGasTooLow, InvalidMaxGas))

	assert.True(t, errors.Is(ErrMaxCodeSizeExceeded, ErrMaxInitCodeSizeExceeded))
	assert.True(t, errors.Is(ErrMaxInitCodeSizeExceeded, ErrMaxCodeSizeExceeded))

	// Non-matching
	assert.False(t, errors.Is(ErrNonceTooLow, ErrInsufficientFunds))
	assert.False(t, errors.Is(ErrNonceTooLow, errors.New("some other error")))

	// Wrapped error matching with fmt.Errorf("%w")
	wrapped := fmt.Errorf("admission failed: %w", ErrNonceTooLow)
	assert.True(t, errors.Is(wrapped, ErrNonceTooLow))
	assert.True(t, errors.Is(wrapped, InvalidNonce))

	// errors.As extraction
	var extracted *TransactionError
	require.True(t, errors.As(wrapped, &extracted))
	assert.Equal(t, ErrNonceTooLow.Code, extracted.Code)
}

// ──────────────────────────────────────────────
// GethStandardRPCError table test
// ──────────────────────────────────────────────

func TestGethStandardRPCError_Table(t *testing.T) {
	tests := []struct {
		name        string
		inputErr    error
		expectedCode int
		expectedMsg  string
	}{
		// 1. Nonce too low
		{"typed ErrNonceTooLow", ErrNonceTooLow, -32000, "nonce too low"},
		{"typed InvalidNonce alias", InvalidNonce, -32000, "nonce too low"},
		{"wrapped ErrNonceTooLow", fmt.Errorf("tx failed: %w", ErrNonceTooLow), -32000, "nonce too low"},
		{"string nonce too low", errors.New("transaction nonce too low"), -32000, "nonce too low"},
		{"string invalid nonce", errors.New("invalid nonce value"), -32000, "nonce too low"},

		// 2. Nonce too high
		{"typed ErrNonceTooHigh", ErrNonceTooHigh, -32000, "nonce too high"},
		{"string nonce too high", errors.New("nonce too high for account"), -32000, "nonce too high"},

		// 3. Insufficient funds
		{"typed ErrInsufficientFunds", ErrInsufficientFunds, -32000, "insufficient funds for gas * price + value"},
		{"typed ErrInsufficientBalance alias", ErrInsufficientBalance, -32000, "insufficient funds for gas * price + value"},
		{"typed InvalidMaxFee alias", InvalidMaxFee, -32000, "insufficient funds for gas * price + value"},
		{"string insufficient funds", errors.New("insufficient funds for transfer"), -32000, "insufficient funds for gas * price + value"},
		{"string insufficient balance", errors.New("account insufficient balance"), -32000, "insufficient funds for gas * price + value"},

		// 4. Already known
		{"typed ErrAlreadyKnown", ErrAlreadyKnown, -32000, "already known"},
		{"string already known", errors.New("transaction already known"), -32000, "already known"},
		{"string already exists", errors.New("tx already exists in pool"), -32000, "already known"},

		// 5. Replacement underpriced
		{"typed ErrReplacementUnderpriced", ErrReplacementUnderpriced, -32000, "replacement transaction underpriced"},
		{"string replacement underpriced", errors.New("replacement transaction underpriced"), -32000, "replacement transaction underpriced"},

		// 6. Intrinsic gas too low
		{"typed ErrIntrinsicGasTooLow", ErrIntrinsicGasTooLow, -32000, "intrinsic gas too low"},
		{"typed InvalidMaxGas alias", InvalidMaxGas, -32000, "intrinsic gas too low"},
		{"string intrinsic gas", errors.New("intrinsic gas too low: 21000 < 53000"), -32000, "intrinsic gas too low"},

		// 7. Exceeds block gas limit
		{"typed ErrExceedsBlockGasLimit", ErrExceedsBlockGasLimit, -32000, "exceeds block gas limit"},
		{"string exceeds block gas limit", errors.New("tx exceeds block gas limit"), -32000, "exceeds block gas limit"},

		// 8. Invalid sender
		{"typed ErrInvalidSender", ErrInvalidSender, -32000, "invalid sender"},
		{"typed InvalidSign alias", InvalidSign, -32000, "invalid sender"},
		{"typed InvalidSignSecp alias", InvalidSignSecp, -32000, "invalid sender"},
		{"typed ErrSenderRecovery alias", ErrSenderRecovery, -32000, "invalid sender"},
		{"string invalid sender", errors.New("invalid sender recovered"), -32000, "invalid sender"},
		{"string recover sender", errors.New("failed to recover sender from signature"), -32000, "invalid sender"},

		// 9. Transaction type not supported
		{"typed ErrTxTypeNotSupported", ErrTxTypeNotSupported, -32000, "transaction type not supported"},
		{"string tx type not supported", errors.New("transaction type not supported"), -32000, "transaction type not supported"},

		// 10. Max initcode size exceeded
		{"typed ErrMaxInitCodeSizeExceeded", ErrMaxInitCodeSizeExceeded, -32000, "max initcode size exceeded"},
		{"string max initcode size exceeded", errors.New("max initcode size exceeded"), -32000, "max initcode size exceeded"},

		// 11. Gas limit reached
		{"typed ErrGasLimitReached", ErrGasLimitReached, -32000, "gas limit reached"},
		{"string gas limit reached", errors.New("gas limit reached"), -32000, "gas limit reached"},

		// 12. Chain ID
		{"typed InvalidChainId", InvalidChainId, -32000, "invalid chain id"},
		{"string invalid chain id", errors.New("invalid chain id: expected 991, got 1"), -32000, "invalid chain id"},

		// 13. Pre-EIP-155
		{"typed ErrPreEIP155", ErrPreEIP155, -32000, "pre-EIP-155 unprotected transactions are not allowed"},
		{"string pre-EIP-155", errors.New("pre-eip-155 unprotected transactions are not allowed"), -32000, "pre-EIP-155 unprotected transactions are not allowed"},

		// 14. Malleable signature
		{"typed ErrMalleableSignature", ErrMalleableSignature, -32000, "malleable signature: s exceeds curve order / 2 (EIP-2)"},
		{"string malleable signature", errors.New("malleable signature: s exceeds curve order / 2"), -32000, "malleable signature: s exceeds curve order / 2 (EIP-2)"},

		// 15. Max envelope size
		{"typed ErrExceedsMaxEnvelopeSize", ErrExceedsMaxEnvelopeSize, -32000, "transaction envelope exceeds maximum allowed size"},
		{"string envelope size", errors.New("transaction envelope size 200000 exceeds max allowed 131072"), -32000, "transaction envelope exceeds maximum allowed size"},

		// 16. Decode error
		{"typed ErrDecodeRawEth", ErrDecodeRawEth, -32000, "failed to decode raw Ethereum transaction envelope"},
		{"string decode error", errors.New("failed to decode RLP format"), -32000, "failed to decode raw Ethereum transaction envelope"},

		// 17. Nil error
		{"nil error", nil, 0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, msg := GethStandardRPCError(tt.inputErr)
			assert.Equal(t, tt.expectedCode, code)
			assert.Equal(t, tt.expectedMsg, msg)
		})
	}
}
