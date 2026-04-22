package transaction

import (
	"math/big"
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

func makeTestTransaction() types.Transaction {
	return NewTransaction(
		common.HexToAddress("0xaaaa"), // fromAddress
		common.HexToAddress("0xbbbb"), // toAddress
		big.NewInt(5000),              // amount
		21000,                         // maxGas
		10,                            // maxGasPrice
		0,                             // maxTimeUse
		nil,                           // data
		nil,                           // relatedAddresses
		common.Hash{},                 // lastDeviceKey
		common.Hash{},                 // newDeviceKey
		1,                             // nonce
		1000,                          // chainId
	)
}

func makeTestContractDeployTx() types.Transaction {
	return NewTransaction(
		common.HexToAddress("0xaaaa"),  // fromAddress
		common.Address{},               // toAddress = zero (deploy)
		big.NewInt(0),                  // amount
		100000,                         // maxGas
		10,                             // maxGasPrice
		0,                              // maxTimeUse
		[]byte{0x60, 0x80, 0x60, 0x40}, // bytecode data
		nil,
		common.Hash{},
		common.Hash{},
		1, // nonce must be non-zero for deploy
		1000,
	)
}

func makeTestContractCallTx() types.Transaction {
	return NewTransaction(
		common.HexToAddress("0xaaaa"),  // fromAddress
		common.HexToAddress("0xcccc"),  // toAddress (contract)
		big.NewInt(0),                  // amount
		80000,                          // maxGas
		10,                             // maxGasPrice
		0,                              // maxTimeUse
		[]byte{0xa9, 0x05, 0x9c, 0xbb}, // function selector data
		nil,
		common.Hash{},
		common.Hash{},
		1,
		1000,
	)
}

// ──────────────────────────────────────────────
// NewTransaction
// ──────────────────────────────────────────────

func TestNewTransaction(t *testing.T) {
	tx := makeTestTransaction()
	require.NotNil(t, tx)

	assert.Equal(t, common.HexToAddress("0xaaaa"), tx.FromAddress())
	assert.Equal(t, common.HexToAddress("0xbbbb"), tx.ToAddress())
	assert.Equal(t, big.NewInt(5000), tx.Amount())
	assert.Equal(t, uint64(21000), tx.MaxGas())
	assert.Equal(t, uint64(10), tx.MaxGasPrice())
	assert.Equal(t, uint64(0), tx.MaxTimeUse())
	assert.Equal(t, uint64(1), tx.GetNonce())
	assert.Equal(t, uint64(1000), tx.GetChainID())
}

func TestNewTransaction_ZeroNonce(t *testing.T) {
	tx := NewTransaction(
		common.Address{}, common.Address{}, big.NewInt(0),
		0, 0, 0, nil, nil, common.Hash{}, common.Hash{}, 0, 0,
	)
	assert.Equal(t, uint64(0), tx.GetNonce())
	assert.Equal(t, uint64(0), tx.GetChainID())
}

// ──────────────────────────────────────────────
// Hash
// ──────────────────────────────────────────────

func TestTransaction_Hash_Deterministic(t *testing.T) {
	tx := makeTestTransaction()
	hash1 := tx.Hash()
	hash2 := tx.Hash()
	assert.Equal(t, hash1, hash2, "same transaction should produce same hash")
	assert.NotEqual(t, common.Hash{}, hash1, "hash should not be zero")
}

func TestTransaction_Hash_DifferentInputs(t *testing.T) {
	tx1 := makeTestTransaction()
	tx2 := NewTransaction(
		common.HexToAddress("0xdddd"), common.HexToAddress("0xeeee"),
		big.NewInt(9999), 30000, 20, 0, nil, nil,
		common.Hash{}, common.Hash{}, 2, 2000,
	)
	assert.NotEqual(t, tx1.Hash(), tx2.Hash(), "different transactions should produce different hashes")
}

func TestTransaction_RHash(t *testing.T) {
	tx := makeTestTransaction()
	rHash := tx.RHash()
	assert.NotEqual(t, common.Hash{}, rHash, "RHash should not be zero")

	// RHash should be cached
	rHash2 := tx.RHash()
	assert.Equal(t, rHash, rHash2)
}

// ──────────────────────────────────────────────
// ClearCacheHash
// ──────────────────────────────────────────────

func TestTransaction_ClearCacheHash(t *testing.T) {
	tx := makeTestTransaction().(*Transaction)

	hash1 := tx.Hash()
	assert.NotEqual(t, common.Hash{}, hash1)

	tx.ClearCacheHash()

	// After clear, re-computing should give same hash
	hash2 := tx.Hash()
	assert.Equal(t, hash1, hash2)
}

// ──────────────────────────────────────────────
// Marshal / Unmarshal
// ──────────────────────────────────────────────

func TestTransaction_MarshalUnmarshal(t *testing.T) {
	original := makeTestTransaction()

	data, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &Transaction{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, original.FromAddress(), restored.FromAddress())
	assert.Equal(t, original.ToAddress(), restored.ToAddress())
	assert.Equal(t, original.Amount(), restored.Amount())
	assert.Equal(t, original.GetNonce(), restored.GetNonce())
	assert.Equal(t, original.GetChainID(), restored.GetChainID())
	assert.Equal(t, original.MaxGas(), restored.MaxGas())
	assert.Equal(t, original.MaxGasPrice(), restored.MaxGasPrice())
}

func TestTransaction_Unmarshal_InvalidData(t *testing.T) {
	tx := &Transaction{}
	err := tx.Unmarshal([]byte("garbage"))
	assert.Error(t, err)
}

// ──────────────────────────────────────────────
// Proto roundtrip
// ──────────────────────────────────────────────

func TestTransaction_ProtoRoundtrip(t *testing.T) {
	original := makeTestTransaction()
	p := original.Proto().(*pb.Transaction)
	require.NotNil(t, p)

	restored := TransactionFromProto(p)
	assert.Equal(t, original.FromAddress(), restored.FromAddress())
	assert.Equal(t, original.ToAddress(), restored.ToAddress())
	assert.Equal(t, original.Amount(), restored.Amount())
	assert.Equal(t, original.GetNonce(), restored.GetNonce())
}

func TestTransactionsToProto_RoundTrip(t *testing.T) {
	tx1 := makeTestTransaction()
	tx2 := NewTransaction(
		common.HexToAddress("0x1111"), common.HexToAddress("0x2222"),
		big.NewInt(100), 10000, 5, 0, nil, nil,
		common.Hash{}, common.Hash{}, 0, 500,
	)

	txs := []types.Transaction{tx1, tx2}
	protos := TransactionsToProto(txs)
	require.Len(t, protos, 2)

	restored := TransactionsFromProto(protos)
	require.Len(t, restored, 2)
	assert.Equal(t, tx1.FromAddress(), restored[0].FromAddress())
	assert.Equal(t, tx2.FromAddress(), restored[1].FromAddress())
}

// ──────────────────────────────────────────────
// CopyTransaction
// ──────────────────────────────────────────────

func TestTransaction_CopyTransaction(t *testing.T) {
	original := makeTestTransaction()
	copied := original.CopyTransaction()

	assert.Equal(t, original.FromAddress(), copied.FromAddress())
	assert.Equal(t, original.ToAddress(), copied.ToAddress())
	assert.Equal(t, original.Amount(), copied.Amount())
	assert.Equal(t, original.GetNonce(), copied.GetNonce())
	assert.Equal(t, original.Hash(), copied.Hash())

	// Verify deep copy: modifying copy doesn't affect original
	copiedTx := copied.(*Transaction)
	copiedTx.proto.MaxGas = 99999
	assert.NotEqual(t, uint64(99999), original.MaxGas(), "modifying copy should not affect original")
}

// ──────────────────────────────────────────────
// Transaction type checks
// ──────────────────────────────────────────────

func TestTransaction_IsRegularTransaction(t *testing.T) {
	tx := makeTestTransaction()
	assert.True(t, tx.IsRegularTransaction(), "tx without data to non-zero address is regular")
	assert.False(t, tx.IsDeployContract())
	assert.False(t, tx.IsCallContract())
}

func TestTransaction_IsDeployContract(t *testing.T) {
	tx := makeTestContractDeployTx()
	assert.True(t, tx.IsDeployContract(), "tx with data to zero address and nonce>0 is deploy")
	assert.False(t, tx.IsRegularTransaction())
}

func TestTransaction_IsCallContract(t *testing.T) {
	tx := makeTestContractCallTx()
	assert.True(t, tx.IsCallContract(), "tx with data to non-zero address is call")
	assert.False(t, tx.IsDeployContract())
	assert.False(t, tx.IsRegularTransaction())
}

// ──────────────────────────────────────────────
// Nonce encoding
// ──────────────────────────────────────────────

func TestTransaction_GetNonce32Bytes(t *testing.T) {
	tx := makeTestTransaction()
	nonce := tx.GetNonce32Bytes()
	require.Len(t, nonce, 32)
	// Last byte should encode nonce=1
	assert.Equal(t, byte(1), nonce[31])
	// Leading bytes should be zero
	assert.Equal(t, byte(0), nonce[0])
}

// ──────────────────────────────────────────────
// Setters
// ──────────────────────────────────────────────

func TestTransaction_SetIsDebug(t *testing.T) {
	tx := makeTestTransaction().(*Transaction)
	assert.False(t, tx.GetIsDebug())
	tx.SetIsDebug(true)
	assert.True(t, tx.GetIsDebug())
}

func TestTransaction_SetReadOnly(t *testing.T) {
	tx := makeTestTransaction().(*Transaction)
	assert.False(t, tx.GetReadOnly())
	tx.SetReadOnly(true)
	assert.True(t, tx.GetReadOnly())
}

func TestTransaction_SetSignatureValues(t *testing.T) {
	tx := makeTestTransaction().(*Transaction)
	chainID := big.NewInt(1000)
	v := big.NewInt(27)
	r := big.NewInt(12345)
	s := big.NewInt(67890)

	tx.SetSignatureValues(chainID, v, r, s)

	gotV, gotR, gotS := tx.RawSignatureValues()
	assert.Equal(t, v, gotV)
	assert.Equal(t, r, gotR)
	assert.Equal(t, s, gotS)
}

func TestTransaction_AddRelatedAddress(t *testing.T) {
	tx := makeTestTransaction().(*Transaction)
	addr1 := common.HexToAddress("0x1111")
	addr2 := common.HexToAddress("0x2222")

	tx.AddRelatedAddress(addr1)
	tx.AddRelatedAddress(addr2)
	tx.AddRelatedAddress(addr1) // duplicate, should not add

	assert.Len(t, tx.BRelatedAddresses(), 2, "should not add duplicate address")
}

func TestTransaction_UpdateRelatedAddresses(t *testing.T) {
	tx := makeTestTransaction().(*Transaction)
	addrs := [][]byte{
		common.HexToAddress("0x1111").Bytes(),
		common.HexToAddress("0x2222").Bytes(),
	}
	tx.UpdateRelatedAddresses(addrs)
	assert.Len(t, tx.BRelatedAddresses(), 2)
}

// ──────────────────────────────────────────────
// Fee calculation
// ──────────────────────────────────────────────

func TestTransaction_Fee(t *testing.T) {
	tx := makeTestTransaction()
	fee := tx.Fee(10)
	// fee = maxGas * currentGasPrice * (maxTimeUse/1000 + 1)
	// = 21000 * 10 * (0/1000 + 1) = 21000 * 10 * 1 = 210000
	assert.Equal(t, big.NewInt(210000), fee)
}

func TestTransaction_MaxFee(t *testing.T) {
	tx := makeTestTransaction()
	maxFee := tx.MaxFee()
	// Legacy type: maxGas * maxGasPrice = 21000 * 10 = 210000
	assert.Equal(t, big.NewInt(210000), maxFee)
}

// ──────────────────────────────────────────────
// String
// ──────────────────────────────────────────────

func TestTransaction_String(t *testing.T) {
	tx := makeTestTransaction()
	s := tx.String()
	assert.Contains(t, s, "Transaction Details")
	assert.Contains(t, s, "From:")
	assert.Contains(t, s, "To:")
}

func TestTransaction_String_Nil(t *testing.T) {
	var tx *Transaction
	s := tx.String()
	assert.Contains(t, s, "nil")
}

// ──────────────────────────────────────────────
// DeviceKey
// ──────────────────────────────────────────────

func TestTransaction_DeviceKeys(t *testing.T) {
	lastKey := common.HexToHash("0xaabb")
	newKey := common.HexToHash("0xccdd")

	tx := NewTransaction(
		common.Address{}, common.Address{}, big.NewInt(0),
		0, 0, 0, nil, nil, lastKey, newKey, 0, 0,
	)

	assert.Equal(t, lastKey, tx.LastDeviceKey())
	assert.Equal(t, newKey, tx.NewDeviceKey())
}

func TestTransaction_UpdateDeriver(t *testing.T) {
	tx := makeTestTransaction().(*Transaction)
	newLast := common.HexToHash("0x1111")
	newNew := common.HexToHash("0x2222")
	tx.UpdateDeriver(newLast, newNew)

	assert.Equal(t, newLast, tx.LastDeviceKey())
	assert.Equal(t, newNew, tx.NewDeviceKey())
}

// ──────────────────────────────────────────────
// NewTransactionFromEth
// ──────────────────────────────────────────────

func TestNewTransactionFromEth_NilInput(t *testing.T) {
	_, err := NewTransactionFromEth(nil)
	assert.Error(t, err)
}

func TestFromEthTransaction_NilInputs(t *testing.T) {
	err := FromEthTransaction(nil, nil)
	assert.Error(t, err)
}

// ──────────────────────────────────────────────
// Security regression tests (GO-C2, GO-H1)
// ──────────────────────────────────────────────

// GO-C2: Verify ToAddress is NOT truncated in NewTransactionOffChain
func TestNewTransactionOffChain_ToAddressFullLength(t *testing.T) {
	// Use an address with distinct bytes so truncation is detectable.
	toAddr := common.HexToAddress("0xABCDEF1234567890ABCDEF1234567890ABCDEF12")
	fromAddr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	tx := NewTransactionOffChain(
		common.Hash{}, fromAddr, toAddr,
		big.NewInt(0), big.NewInt(100), 1000,
		1, 0, nil, nil,
		common.Hash{}, common.Hash{}, 1,
	)
	require.NotNil(t, tx, "transaction must not be nil")
	// Address MUST survive the round-trip completely (20 bytes, not 15)
	assert.Equal(t, toAddr, tx.ToAddress(),
		"ToAddress must be exactly 20 bytes — must not be truncated to 15")
	rawTx := tx.(*Transaction)
	assert.Equal(t, 20, len(rawTx.proto.ToAddress),
		"proto.ToAddress must be stored as 20 bytes")
}

// GO-H1: Verify Fee() handles uint64 values that would overflow int64
func TestTransaction_Fee_Overflow(t *testing.T) {
	// MaxGas set to a value > math.MaxInt64 to trigger the old overflow.
	// With the old code: int64(^uint64(0)) = -1 → fee would be negative.
	tx := NewTransaction(
		common.Address{}, common.Address{}, big.NewInt(0),
		^uint64(0), // MaxGas = math.MaxUint64
		1, 0, nil, nil,
		common.Hash{}, common.Hash{}, 1, 1,
	)
	fee := tx.Fee(1)
	require.NotNil(t, fee)
	// Fee must be positive (not negative due to overflow)
	assert.Positive(t, fee.Sign(),
		"Fee() must not be negative — uint64 overflow must not occur with large MaxGas")
}
