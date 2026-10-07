package transaction

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
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

// TestTransaction_Hash_IncludesBlobAndAuthorizationFields guards against silently
// dropping the EIP-4844/EIP-7702 fields from the hash preimage (Hash() and RHash()
// must match the Rust-side calculate_single_transaction_hash — see tx_hash.rs).
func TestTransaction_Hash_IncludesBlobAndAuthorizationFields(t *testing.T) {
	base := makeTestTransaction().(*Transaction)
	baseHash := base.Hash()
	baseRHash := base.RHash()

	withBlob := makeTestTransaction().(*Transaction)
	withBlob.proto.BlobVersionedHashes = [][]byte{{0x01, 0xAA}}
	assert.NotEqual(t, baseHash, withBlob.Hash(), "BlobVersionedHashes must affect Hash()")
	assert.NotEqual(t, baseRHash, withBlob.RHash(), "BlobVersionedHashes must affect RHash()")

	withFeeCap := makeTestTransaction().(*Transaction)
	withFeeCap.proto.MaxFeePerBlobGas = []byte{0x03, 0xE8}
	assert.NotEqual(t, baseHash, withFeeCap.Hash(), "MaxFeePerBlobGas must affect Hash()")

	withAuth := makeTestTransaction().(*Transaction)
	withAuth.proto.AuthorizationList = []*pb.SetCodeAuthorization{{ChainID: 1, Address: []byte{0x01}}}
	assert.NotEqual(t, baseHash, withAuth.Hash(), "AuthorizationList must affect Hash()")

	// Sidecar must NOT affect the hash — it's network representation, not committed data.
	withSidecar := makeTestTransaction().(*Transaction)
	withSidecar.proto.Sidecar = &pb.BlobSidecar{Blobs: [][]byte{{0xDE, 0xAD, 0xBE, 0xEF}}}
	assert.Equal(t, baseHash, withSidecar.Hash(), "Sidecar must NOT affect Hash()")
	assert.Equal(t, baseRHash, withSidecar.RHash(), "Sidecar must NOT affect RHash()")
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
	assert.True(t, tx.IsDeployContract(), "tx with data to zero address is deploy")
	assert.False(t, tx.IsRegularTransaction())

	txNonce0 := NewTransaction(
		common.HexToAddress("0xaaaa"),
		common.Address{},
		big.NewInt(0),
		100000,
		10,
		0,
		[]byte{0x60, 0x80, 0x60, 0x40},
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1000,
	)
	assert.True(t, txNonce0.IsDeployContract(), "tx with data to zero address and nonce=0 is deploy")
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

// AddRelatedAddress/UpdateRelatedAddresses/BRelatedAddresses are deprecated
// no-ops: RelatedAddresses() now always resolves dynamically to
// [FromAddress, ToAddress] (see GetRelatedAddresses), independent of these
// setters. These tests document that behavior instead of exercising the old
// accumulator, which no longer exists.
func TestTransaction_AddRelatedAddress(t *testing.T) {
	tx := makeTestTransaction().(*Transaction)
	addr1 := common.HexToAddress("0x1111")
	addr2 := common.HexToAddress("0x2222")

	tx.AddRelatedAddress(addr1)
	tx.AddRelatedAddress(addr2)
	tx.AddRelatedAddress(addr1) // no-op regardless: setter is deprecated

	assert.Empty(t, tx.BRelatedAddresses(), "BRelatedAddresses is a deprecated no-op")
	assert.ElementsMatch(t, []common.Address{tx.FromAddress(), tx.ToAddress()}, tx.RelatedAddresses(),
		"RelatedAddresses() always dynamically resolves to [From, To]")
}

func TestTransaction_UpdateRelatedAddresses(t *testing.T) {
	tx := makeTestTransaction().(*Transaction)
	addrs := [][]byte{
		common.HexToAddress("0x1111").Bytes(),
		common.HexToAddress("0x2222").Bytes(),
	}
	tx.UpdateRelatedAddresses(addrs) // no-op regardless: setter is deprecated

	assert.Empty(t, tx.BRelatedAddresses(), "BRelatedAddresses is a deprecated no-op")
	assert.ElementsMatch(t, []common.Address{tx.FromAddress(), tx.ToAddress()}, tx.RelatedAddresses(),
		"RelatedAddresses() always dynamically resolves to [From, To]")
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

// TestTransaction_EffectiveGasPrice_PerType verifies ADR D2 fee semantics:
// - Legacy/EIP-2930: flat MaxGasPrice
// - EIP-1559/EIP-4844/EIP-7702: min(GasFeeCap, F + GasTipCap) where F = MINIMUM_BASE_FEE (100,000)
func TestTransaction_EffectiveGasPrice_PerType(t *testing.T) {
	for _, c := range []struct {
		name        string
		txType      uint64
		maxGasPrice uint64
		gasFeeCap   []byte
		gasTipCap   []byte
		want        *big.Int
	}{
		{"legacy uses flat MaxGasPrice", 0, 42, nil, nil, big.NewInt(42)},
		{"eip2930 uses flat MaxGasPrice", 1, 42, nil, nil, big.NewInt(42)},
		{"eip1559 pays F + tip when below cap", 2, 0, big.NewInt(150000).Bytes(), big.NewInt(10000).Bytes(), big.NewInt(110000)},
		{"eip1559 capped at GasFeeCap when tip is high", 2, 0, big.NewInt(105000).Bytes(), big.NewInt(10000).Bytes(), big.NewInt(105000)},
		{"eip4844 with zero tip pays flat F", 3, 0, big.NewInt(200000).Bytes(), nil, big.NewInt(100000)},
		{"eip7702 pays F + tip", 4, 0, big.NewInt(120000).Bytes(), big.NewInt(5000).Bytes(), big.NewInt(105000)},
	} {
		t.Run(c.name, func(t *testing.T) {
			tx := &Transaction{proto: &pb.Transaction{
				Type: c.txType, MaxGasPrice: c.maxGasPrice, GasFeeCap: c.gasFeeCap, GasTipCap: c.gasTipCap,
			}}
			assert.Equal(t, c.want, tx.EffectiveGasPrice())
		})
	}
}

// TestTransaction_GasPriceCap_PerType verifies GasPriceCap returns the ceiling:
// MaxGasPrice for legacy/2930, GasFeeCap for 1559/4844/7702.
func TestTransaction_GasPriceCap_PerType(t *testing.T) {
	for _, c := range []struct {
		name        string
		txType      uint64
		maxGasPrice uint64
		gasFeeCap   []byte
		want        *big.Int
	}{
		{"legacy cap is MaxGasPrice", 0, 42, nil, big.NewInt(42)},
		{"eip2930 cap is MaxGasPrice", 1, 42, nil, big.NewInt(42)},
		{"eip1559 cap is GasFeeCap", 2, 0, big.NewInt(150000).Bytes(), big.NewInt(150000)},
		{"eip4844 cap is GasFeeCap", 3, 0, big.NewInt(200000).Bytes(), big.NewInt(200000)},
		{"eip7702 cap is GasFeeCap", 4, 0, big.NewInt(120000).Bytes(), big.NewInt(120000)},
	} {
		t.Run(c.name, func(t *testing.T) {
			tx := &Transaction{proto: &pb.Transaction{
				Type: c.txType, MaxGasPrice: c.maxGasPrice, GasFeeCap: c.gasFeeCap,
			}}
			assert.Equal(t, c.want, tx.GasPriceCap())
		})
	}
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

// P0-8: Cross-language Go <-> Rust hash equivalence test (Zero-Fork invariant).
func TestCrossLanguage_HashEquivalence_RawEnvelope(t *testing.T) {
	envelope := []byte("sample_raw_eip2718_envelope_bytes")
	expectedHash := crypto.Keccak256Hash(envelope)

	pTx := &pb.Transaction{
		FromAddress: bytes.Repeat([]byte{0x01}, 20),
		ToAddress:   bytes.Repeat([]byte{0x02}, 20),
		RawEnvelope: envelope,
	}
	tx := TransactionFromProto(pTx)

	assert.Equal(t, expectedHash, tx.Hash(), "Go Hash() must match Keccak256(RawEnvelope)")
	assert.Equal(t, expectedHash, tx.EthHash(), "Go EthHash() must match Keccak256(RawEnvelope)")
}

func TestCrossLanguage_HashEquivalence_SystemTxWithoutEnvelope(t *testing.T) {
	// Rust calculate_single_transaction_hash for this exact proto calculates:
	// SYSTEM_TX_HASH: 1925db928262bc9d4db0f4dba30bdd01a29ffa7d5d4ae5f418b0a63ba373c9eb
	expectedRustHash := common.HexToHash("0x1925db928262bc9d4db0f4dba30bdd01a29ffa7d5d4ae5f418b0a63ba373c9eb")

	pTx := &pb.Transaction{
		FromAddress: bytes.Repeat([]byte{0xaa}, 20),
		ToAddress:   bytes.Repeat([]byte{0xbb}, 20),
		Amount:      []byte{0x01},
		MaxGas:      21000,
		MaxGasPrice: 100000,
		RawEnvelope: nil, // empty envelope = system transaction
	}
	tx := TransactionFromProto(pTx)

	assert.Equal(t, expectedRustHash, tx.Hash(), "Go system tx Hash() must exactly match Rust consensus calculate_single_transaction_hash")
}

func TestSingleCanonicalHash_AllEthTxTypes(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	to := common.HexToAddress("0x0000000000000000000000000000000000abcdef")

	// 1. Legacy
	legacyInner := &e_types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(100000),
		Gas:      21000,
		To:       &to,
		Value:    big.NewInt(100),
	}
	ethLegacy, err := e_types.SignNewTx(key, e_types.NewEIP155Signer(big.NewInt(1337)), legacyInner)
	require.NoError(t, err)

	metaLegacy, err := NewTransactionFromEth(ethLegacy)
	require.NoError(t, err)
	assert.Equal(t, ethLegacy.Hash(), metaLegacy.Hash(), "Legacy: meta.Hash() must equal eth.Hash()")
	assert.Equal(t, ethLegacy.Hash(), metaLegacy.EthHash(), "Legacy: meta.EthHash() must equal eth.Hash()")

	// 2. EIP-2930
	eip2930Inner := &e_types.AccessListTx{
		ChainID:  big.NewInt(1337),
		Nonce:    2,
		GasPrice: big.NewInt(100000),
		Gas:      21000,
		To:       &to,
		Value:    big.NewInt(200),
	}
	eth2930, err := e_types.SignNewTx(key, e_types.NewLondonSigner(big.NewInt(1337)), eip2930Inner)
	require.NoError(t, err)

	meta2930, err := NewTransactionFromEth(eth2930)
	require.NoError(t, err)
	assert.Equal(t, eth2930.Hash(), meta2930.Hash(), "EIP-2930: meta.Hash() must equal eth.Hash()")
	assert.Equal(t, eth2930.Hash(), meta2930.EthHash(), "EIP-2930: meta.EthHash() must equal eth.Hash()")

	// 3. EIP-1559
	eip1559Inner := &e_types.DynamicFeeTx{
		ChainID:   big.NewInt(1337),
		Nonce:     3,
		GasTipCap: big.NewInt(1000),
		GasFeeCap: big.NewInt(100000),
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(300),
	}
	eth1559, err := e_types.SignNewTx(key, e_types.NewLondonSigner(big.NewInt(1337)), eip1559Inner)
	require.NoError(t, err)

	meta1559, err := NewTransactionFromEth(eth1559)
	require.NoError(t, err)
	assert.Equal(t, eth1559.Hash(), meta1559.Hash(), "EIP-1559: meta.Hash() must equal eth.Hash()")
	assert.Equal(t, eth1559.Hash(), meta1559.EthHash(), "EIP-1559: meta.EthHash() must equal eth.Hash()")
}
