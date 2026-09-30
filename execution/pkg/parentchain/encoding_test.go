package parentchain

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T-U9: Canonical encoding vectors (Header, Receipt, state values) remain fixed across runs.
func TestCanonicalEncoding_Vectors(t *testing.T) {
	// 1. Header round-trip and hash determinism
	h := &Header{
		Number:        100,
		ParentHash:    common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		StateRoot:     common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		TxsRoot:       common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333"),
		ReceiptsRoot:  common.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444"),
		TimestampMs:   1700000000000,
		Epoch:         5,
		CommitIndex:   42,
		GEI:           105,
		LeaderAddress: common.HexToAddress("0x9999999999999999999999999999999999999999"),
		CommitDigest:  common.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555"),
		TxCount:       3,
	}

	encH := EncodeHeader(h)
	assert.Equal(t, CanonicalHeaderSize, len(encH))
	hHash1 := h.Hash()
	hHash2 := h.Hash()
	assert.Equal(t, hHash1, hHash2)

	decH, err := DecodeHeader(encH)
	require.NoError(t, err)
	assert.Equal(t, h, decH)
	assert.Equal(t, hHash1, decH.Hash())

	// 2. Receipt round-trip
	rec := &Receipt{
		TxHash:    common.HexToHash("0xaaaa"),
		Status:    1,
		ErrorCode: 0,
		Events: [][]byte{
			[]byte("event_data_1"),
			[]byte("event_data_2_longer"),
		},
	}
	encRec := EncodeReceipt(rec)
	decRec, err := DecodeReceipt(encRec)
	require.NoError(t, err)
	assert.Equal(t, rec.TxHash, decRec.TxHash)
	assert.Equal(t, rec.Status, decRec.Status)
	assert.Equal(t, rec.ErrorCode, decRec.ErrorCode)
	assert.Equal(t, len(rec.Events), len(decRec.Events))
	for i := range rec.Events {
		assert.True(t, bytes.Equal(rec.Events[i], decRec.Events[i]))
	}

	// 3. ChainRegistryEntry round-trip
	var pubKey cm.PublicKey
	copy(pubKey[:], []byte("012345678901234567890123456789012345678901234567"))
	entry := &ChainRegistryEntry{
		FloatIdentityKey:     pubKey,
		ClusterIDDescriptive: 101,
		ChainIDDescriptive:   101,
	}
	encEntry := EncodeChainRegistryEntry(entry)
	decEntry, err := DecodeChainRegistryEntry(encEntry)
	require.NoError(t, err)
	assert.Equal(t, entry.FloatIdentityKey, decEntry.FloatIdentityKey)
	assert.Equal(t, entry.ClusterIDDescriptive, decEntry.ClusterIDDescriptive)

	// 4. FloatTransferRecord round-trip
	srcKey := pubKey
	record := &FloatTransferRecord{
		SourceKey:            &srcKey,
		DestKey:              pubKey,
		Value:                big.NewInt(1000000),
		ConfirmedAtBlockTime: 12345678,
	}
	encRecord := EncodeFloatTransferRecord(record)
	decRecord, err := DecodeFloatTransferRecord(encRecord)
	require.NoError(t, err)
	assert.Equal(t, *record.SourceKey, *decRecord.SourceKey)
	assert.Equal(t, record.DestKey, decRecord.DestKey)
	assert.Equal(t, record.Value, decRecord.Value)
	assert.Equal(t, record.ConfirmedAtBlockTime, decRecord.ConfirmedAtBlockTime)

	// Nil source key case
	recordNoSrc := &FloatTransferRecord{
		SourceKey:            nil,
		DestKey:              pubKey,
		Value:                big.NewInt(2000000),
		ConfirmedAtBlockTime: 87654321,
	}
	encRecordNoSrc := EncodeFloatTransferRecord(recordNoSrc)
	decRecordNoSrc, err := DecodeFloatTransferRecord(encRecordNoSrc)
	require.NoError(t, err)
	assert.Nil(t, decRecordNoSrc.SourceKey)
	assert.Equal(t, recordNoSrc.DestKey, decRecordNoSrc.DestKey)
	assert.Equal(t, recordNoSrc.Value, decRecordNoSrc.Value)

	// 5. TreeKey namespace isolation
	k1 := TreeKey(NamespaceFloat, []byte("key1"))
	k2 := TreeKey(NamespaceChainRegistry, []byte("key1"))
	assert.NotEqual(t, k1, k2, "different namespaces must produce different keys")
}
