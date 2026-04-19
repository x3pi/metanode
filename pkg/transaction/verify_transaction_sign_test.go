package transaction

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// ──────────────────────────────────────────────
// VerifyTransactionSignRequest
// ──────────────────────────────────────────────

func TestNewVerifyTransactionRequest(t *testing.T) {
	txHash := common.HexToHash("0xabcdef")
	pubkey := p_common.PubkeyFromBytes(make([]byte, 48))
	sign := p_common.SignFromBytes(make([]byte, 96))

	req := NewVerifyTransactionRequest(txHash, pubkey, sign)
	require.NotNil(t, req)

	assert.Equal(t, txHash, req.TransactionHash())
	assert.NotNil(t, req.SenderPublicKey())
	assert.NotNil(t, req.SenderSign())
	assert.NotNil(t, req.Proto())
}

func TestVerifyTransactionRequest_MarshalUnmarshal(t *testing.T) {
	txHash := common.HexToHash("0x1234")
	pubkey := p_common.PubkeyFromBytes(make([]byte, 48))
	sign := p_common.SignFromBytes(make([]byte, 96))

	original := NewVerifyTransactionRequest(txHash, pubkey, sign)

	data, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &VerifyTransactionSignRequest{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, original.TransactionHash(), restored.TransactionHash())
}

func TestVerifyTransactionRequest_Unmarshal_InvalidData(t *testing.T) {
	req := &VerifyTransactionSignRequest{}
	err := req.Unmarshal([]byte("not valid protobuf"))
	assert.Error(t, err)
}

// ──────────────────────────────────────────────
// VerifyTransactionSignResult
// ──────────────────────────────────────────────

func TestNewVerifyTransactionResult_Valid(t *testing.T) {
	txHash := common.HexToHash("0xdeadbeef")
	result := NewVerifyTransactionResult(txHash, true)
	require.NotNil(t, result)

	assert.Equal(t, txHash, result.TransactionHash())
	assert.True(t, result.Valid())
	assert.NotNil(t, result.Proto())
}

func TestNewVerifyTransactionResult_Invalid(t *testing.T) {
	txHash := common.HexToHash("0xcafebabe")
	result := NewVerifyTransactionResult(txHash, false)
	require.NotNil(t, result)

	assert.Equal(t, txHash, result.TransactionHash())
	assert.False(t, result.Valid())
}

func TestVerifyTransactionResult_MarshalUnmarshal(t *testing.T) {
	original := NewVerifyTransactionResult(common.HexToHash("0x9999"), true)

	data, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &VerifyTransactionSignResult{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, original.TransactionHash(), restored.TransactionHash())
	assert.Equal(t, original.Valid(), restored.Valid())
}

func TestVerifyTransactionResult_Unmarshal_InvalidData(t *testing.T) {
	result := &VerifyTransactionSignResult{}
	err := result.Unmarshal([]byte("garbage data"))
	assert.Error(t, err)
}
