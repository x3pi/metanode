package block

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ──────────────────────────────────────────────
// Helper
// ──────────────────────────────────────────────

func makeTestHeader() *BlockHeader {
	return NewBlockHeader(
		common.HexToHash("0xaabbccdd"),  // lastBlockHash
		42,                              // blockNumber
		common.HexToHash("0x1111"),      // accountStatesRoot
		common.HexToHash("0x2222"),      // stakeStatesRoot
		common.HexToHash("0x3333"),      // receiptRoot
		common.HexToAddress("0xabcdef"), // leaderAddress
		1700000000,                      // timeStamp
		common.HexToHash("0x4444"),      // transactionsRoot
		5,                               // epoch
		100,                             // globalExecIndex
	)
}

// ──────────────────────────────────────────────
// BlockHeader tests
// ──────────────────────────────────────────────

func TestNewBlockHeader(t *testing.T) {
	h := makeTestHeader()

	assert.Equal(t, common.HexToHash("0xaabbccdd"), h.LastBlockHash())
	assert.Equal(t, uint64(42), h.BlockNumber())
	assert.Equal(t, common.HexToHash("0x1111"), h.AccountStatesRoot())
	assert.Equal(t, common.HexToHash("0x2222"), h.StakeStatesRoot())
	assert.Equal(t, common.HexToHash("0x3333"), h.ReceiptRoot())
	assert.Equal(t, common.HexToAddress("0xabcdef"), h.LeaderAddress())
	assert.Equal(t, uint64(1700000000), h.TimeStamp())
	assert.Equal(t, common.HexToHash("0x4444"), h.TransactionsRoot())
	assert.Equal(t, uint64(5), h.Epoch())
	assert.Equal(t, uint64(100), h.GlobalExecIndex())
}

func TestNewBlockHeader_NoGlobalExecIndex(t *testing.T) {
	h := NewBlockHeader(
		common.Hash{}, 1, common.Hash{}, common.Hash{},
		common.Hash{}, common.Address{}, 0, common.Hash{}, 0,
	)
	assert.Equal(t, uint64(0), h.GlobalExecIndex(), "default globalExecIndex should be 0")
}

func TestBlockHeader_SetGlobalExecIndex(t *testing.T) {
	h := makeTestHeader()
	h.SetGlobalExecIndex(999)
	assert.Equal(t, uint64(999), h.GlobalExecIndex())
}

func TestBlockHeader_SetAccountStatesRoot(t *testing.T) {
	h := makeTestHeader()
	newRoot := common.HexToHash("0xdeadbeef")
	h.SetAccountStatesRoot(newRoot)
	assert.Equal(t, newRoot, h.AccountStatesRoot())
}

func TestBlockHeader_SetStakeStatesRoot(t *testing.T) {
	h := makeTestHeader()
	newRoot := common.HexToHash("0xcafebabe")
	h.SetStakeStatesRoot(newRoot)
	assert.Equal(t, newRoot, h.StakeStatesRoot())
}

func TestBlockHeader_MarshalUnmarshal(t *testing.T) {
	original := makeTestHeader()

	data, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &BlockHeader{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, original.LastBlockHash(), restored.LastBlockHash())
	assert.Equal(t, original.BlockNumber(), restored.BlockNumber())
	assert.Equal(t, original.AccountStatesRoot(), restored.AccountStatesRoot())
	assert.Equal(t, original.StakeStatesRoot(), restored.StakeStatesRoot())
	assert.Equal(t, original.ReceiptRoot(), restored.ReceiptRoot())
	assert.Equal(t, original.LeaderAddress(), restored.LeaderAddress())
	assert.Equal(t, original.TimeStamp(), restored.TimeStamp())
	assert.Equal(t, original.TransactionsRoot(), restored.TransactionsRoot())
	assert.Equal(t, original.Epoch(), restored.Epoch())
}

func TestBlockHeader_Unmarshal_InvalidData(t *testing.T) {
	h := &BlockHeader{}
	err := h.Unmarshal([]byte("invalid protobuf data"))
	assert.Error(t, err, "should fail on invalid data")
}

func TestBlockHeader_Hash_Deterministic(t *testing.T) {
	h := makeTestHeader()
	hash1 := h.Hash()
	hash2 := h.Hash()
	assert.Equal(t, hash1, hash2, "same header should produce same hash")
	assert.NotEqual(t, common.Hash{}, hash1, "hash should not be zero")
}

func TestBlockHeader_Hash_DifferentInputs(t *testing.T) {
	h1 := makeTestHeader()
	h2 := NewBlockHeader(
		common.HexToHash("0xdifferent"), 99, common.Hash{}, common.Hash{},
		common.Hash{}, common.Address{}, 0, common.Hash{}, 0,
	)
	assert.NotEqual(t, h1.Hash(), h2.Hash(), "different headers should produce different hashes")
}

func TestBlockHeader_ProtoRoundtrip(t *testing.T) {
	original := makeTestHeader()
	pb := original.Proto()
	require.NotNil(t, pb)

	restored := &BlockHeader{}
	restored.FromProto(pb)

	assert.Equal(t, original.LastBlockHash(), restored.LastBlockHash())
	assert.Equal(t, original.BlockNumber(), restored.BlockNumber())
	assert.Equal(t, original.AccountStatesRoot(), restored.AccountStatesRoot())
	assert.Equal(t, original.StakeStatesRoot(), restored.StakeStatesRoot())
	assert.Equal(t, original.ReceiptRoot(), restored.ReceiptRoot())
	assert.Equal(t, original.LeaderAddress(), restored.LeaderAddress())
	assert.Equal(t, original.TimeStamp(), restored.TimeStamp())
	assert.Equal(t, original.TransactionsRoot(), restored.TransactionsRoot())
	assert.Equal(t, original.Epoch(), restored.Epoch())
	assert.Equal(t, original.GlobalExecIndex(), restored.GlobalExecIndex())
}

func TestBlockHeader_String(t *testing.T) {
	h := makeTestHeader()
	s := h.String()
	assert.Contains(t, s, "BlockHeader")
	assert.Contains(t, s, "42") // block number
}

// ──────────────────────────────────────────────
// Block tests
// ──────────────────────────────────────────────

func TestNewBlock(t *testing.T) {
	header := makeTestHeader()
	txHashes := []common.Hash{
		common.HexToHash("0xaa"),
		common.HexToHash("0xbb"),
	}

	b := NewBlock(header, txHashes, nil)
	require.NotNil(t, b)

	assert.Equal(t, header, b.Header())
	assert.Equal(t, txHashes, b.Transactions())
	assert.Nil(t, b.ExecuteSCResults())
}

func TestNewBlock_EmptyTransactions(t *testing.T) {
	header := makeTestHeader()
	b := NewBlock(header, nil, nil)

	assert.Nil(t, b.Transactions())
	assert.Nil(t, b.ExecuteSCResults())
}

func TestBlock_MarshalUnmarshal(t *testing.T) {
	header := makeTestHeader()
	txHashes := []common.Hash{
		common.HexToHash("0xaa"),
		common.HexToHash("0xbb"),
		common.HexToHash("0xcc"),
	}
	original := NewBlock(header, txHashes, nil)

	data, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &Block{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, original.Header().BlockNumber(), restored.Header().BlockNumber())
	assert.Equal(t, original.Header().LastBlockHash(), restored.Header().LastBlockHash())
	assert.Equal(t, original.Header().Epoch(), restored.Header().Epoch())
	assert.Equal(t, len(original.Transactions()), len(restored.Transactions()))
	for i, txHash := range original.Transactions() {
		assert.Equal(t, txHash, restored.Transactions()[i])
	}
}

func TestBlock_Unmarshal_InvalidData(t *testing.T) {
	b := &Block{}
	err := b.Unmarshal([]byte("garbage"))
	assert.Error(t, err)
}

func TestBlock_ProtoRoundtrip(t *testing.T) {
	header := makeTestHeader()
	txHashes := []common.Hash{common.HexToHash("0x01"), common.HexToHash("0x02")}
	original := NewBlock(header, txHashes, nil)

	pb := original.Proto()
	require.NotNil(t, pb)

	restored := &Block{}
	restored.FromProto(pb)

	assert.Equal(t, original.Header().BlockNumber(), restored.Header().BlockNumber())
	assert.Equal(t, original.Header().LastBlockHash(), restored.Header().LastBlockHash())
	assert.Equal(t, len(original.Transactions()), len(restored.Transactions()))
}

// ──────────────────────────────────────────────
// HashWithoutSignature tests
// ──────────────────────────────────────────────

func TestBlockHeader_HashWithoutSignature_Deterministic(t *testing.T) {
	h := makeTestHeader()
	hash1 := h.HashWithoutSignature()
	hash2 := h.HashWithoutSignature()
	assert.Equal(t, hash1, hash2, "same header should produce same hash without signature")
	assert.NotEqual(t, common.Hash{}, hash1, "hash should not be zero")
}

func TestBlockHeader_HashWithoutSignature_NotAffectedBySignature(t *testing.T) {
	h := makeTestHeader()
	hashBefore := h.HashWithoutSignature()
	h.SetAggregateSignature([]byte("some-signature-data"))
	hashAfter := h.HashWithoutSignature()
	assert.Equal(t, hashBefore, hashAfter, "HashWithoutSignature should be same regardless of signature")
}

func TestBlockHeader_Hash_NotAffectedBySignature(t *testing.T) {
	h := makeTestHeader()
	hashBefore := h.Hash()
	h.SetAggregateSignature([]byte("some-signature-data"))
	hashAfter := h.Hash()
	// Proto() does NOT include AggregateSignature, so Hash() is unchanged
	assert.Equal(t, hashBefore, hashAfter, "Hash() should NOT change when signature is set (Proto excludes sig)")
}

func TestBlockHeader_HashEqualsHashWithoutSignature(t *testing.T) {
	h := makeTestHeader()
	h.SetAggregateSignature([]byte("test-sig"))
	hashFull := h.Hash()
	hashNoSig := h.HashWithoutSignature()
	// Both Proto() and HashWithoutSignature exclude AggregateSignature
	assert.Equal(t, hashFull, hashNoSig, "Hash and HashWithoutSignature should be equal (both exclude sig from Proto)")
}

func TestBlockHeader_SetAggregateSignature(t *testing.T) {
	h := makeTestHeader()
	assert.Nil(t, h.AggregateSignature())

	sig := []byte{0x01, 0x02, 0x03, 0x04}
	h.SetAggregateSignature(sig)
	assert.Equal(t, sig, h.AggregateSignature())
}

// ──────────────────────────────────────────────
// GlobalExecIndex roundtrip
// ──────────────────────────────────────────────

func TestBlockHeader_GlobalExecIndex_SetAndGet(t *testing.T) {
	h := makeTestHeader()
	assert.Equal(t, uint64(100), h.GlobalExecIndex(), "initial value from makeTestHeader")

	h.SetGlobalExecIndex(12345)
	assert.Equal(t, uint64(12345), h.GlobalExecIndex())
}

func TestBlockHeader_GlobalExecIndex_ProtoRoundtrip(t *testing.T) {
	h := makeTestHeader()
	h.SetGlobalExecIndex(54321)

	pbHeader := h.Proto()
	restored := &BlockHeader{}
	restored.FromProto(pbHeader)

	assert.Equal(t, uint64(54321), restored.GlobalExecIndex())
}

// ──────────────────────────────────────────────
// Constants tests
// ──────────────────────────────────────────────

func TestBlockConstants(t *testing.T) {
	assert.Equal(t, 1000, maxBlocksPerShard, "maxBlocksPerShard should be 1000")
	assert.Equal(t, 66, lineByte, "lineByte should be 66")
}

// ──────────────────────────────────────────────
// Block with many transactions
// ──────────────────────────────────────────────

func TestBlock_ManyTransactions_MarshalRoundtrip(t *testing.T) {
	header := makeTestHeader()
	txHashes := make([]common.Hash, 100)
	for i := 0; i < 100; i++ {
		txHashes[i] = common.BigToHash(common.Big1)
		txHashes[i][0] = byte(i)
	}
	original := NewBlock(header, txHashes, nil)

	data, err := original.Marshal()
	require.NoError(t, err)

	restored := &Block{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, 100, len(restored.Transactions()))
	for i, txHash := range original.Transactions() {
		assert.Equal(t, txHash, restored.Transactions()[i], "tx hash mismatch at index %d", i)
	}
}

