package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// BatchStore — serialize/deserialize pure functions
// ══════════════════════════════════════════════════════════════════════════════

// ---------- SerializeBatch / DeserializeBatch ----------

func TestSerializeBatch_RoundTrip(t *testing.T) {
	original := [][2][]byte{
		{[]byte("key1"), []byte("val1")},
		{[]byte("key2"), []byte("val2")},
		{[]byte("key3"), []byte("val3")},
	}
	encoded, err := SerializeBatch(original)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	decoded, err := DeserializeBatch(encoded)
	require.NoError(t, err)
	assert.Equal(t, len(original), len(decoded))
	for i := range original {
		assert.Equal(t, original[i][0], decoded[i][0])
		assert.Equal(t, original[i][1], decoded[i][1])
	}
}

func TestSerializeBatch_Empty(t *testing.T) {
	encoded, err := SerializeBatch([][2][]byte{})
	require.NoError(t, err)

	decoded, err := DeserializeBatch(encoded)
	require.NoError(t, err)
	assert.Equal(t, 0, len(decoded))
}

func TestSerializeBatch_LargeValues(t *testing.T) {
	bigVal := make([]byte, 4096)
	for i := range bigVal {
		bigVal[i] = byte(i % 256)
	}
	original := [][2][]byte{
		{[]byte("bigkey"), bigVal},
	}
	encoded, err := SerializeBatch(original)
	require.NoError(t, err)

	decoded, err := DeserializeBatch(encoded)
	require.NoError(t, err)
	assert.Equal(t, bigVal, decoded[0][1])
}

// ---------- SerializeByteArrays / DeserializeByteArrays ----------

func TestSerializeByteArrays_RoundTrip(t *testing.T) {
	original := [][]byte{
		[]byte("first"),
		[]byte("second"),
		[]byte("third"),
	}
	encoded, err := SerializeByteArrays(original)
	require.NoError(t, err)

	decoded, err := DeserializeByteArrays(encoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

func TestSerializeByteArrays_Empty(t *testing.T) {
	encoded, err := SerializeByteArrays([][]byte{})
	require.NoError(t, err)

	decoded, err := DeserializeByteArrays(encoded)
	require.NoError(t, err)
	assert.Equal(t, 0, len(decoded))
}

func TestSerializeByteArrays_SingleElement(t *testing.T) {
	original := [][]byte{[]byte("only")}
	encoded, err := SerializeByteArrays(original)
	require.NoError(t, err)

	decoded, err := DeserializeByteArrays(encoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

// ---------- PutToBytes / BytesToPut ----------

func TestPutToBytes_RoundTrip(t *testing.T) {
	key := []byte("mykey")
	value := []byte("myvalue")
	encoded, err := PutToBytes(key, value)
	require.NoError(t, err)

	decoded, err := BytesToPut(encoded)
	require.NoError(t, err)
	assert.Equal(t, key, decoded[0])
	assert.Equal(t, value, decoded[1])
}

func TestPutToBytes_EmptyValue(t *testing.T) {
	key := []byte("k")
	value := []byte{}
	encoded, err := PutToBytes(key, value)
	require.NoError(t, err)

	decoded, err := BytesToPut(encoded)
	require.NoError(t, err)
	assert.Equal(t, key, decoded[0])
	assert.Equal(t, value, decoded[1])
}

// ---------- BackUpDb AddToFullDbLogs ----------

func TestBackUpDb_AddToFullDbLogs(t *testing.T) {
	backup := BackUpDb{}
	assert.Nil(t, backup.FullDbLogs)

	entry := map[string][]byte{"key1": []byte("val1")}
	backup.AddToFullDbLogs(entry)
	assert.Equal(t, 1, len(backup.FullDbLogs))

	backup.AddToFullDbLogs(map[string][]byte{"key2": []byte("val2")})
	assert.Equal(t, 2, len(backup.FullDbLogs))
}

// ---------- SerializeBackupDb / DeserializeBackupDb ----------

func TestSerializeBackupDb_RoundTrip(t *testing.T) {
	original := BackUpDb{
		BockNumber: 42,
		NodeId:     "node1",
		TxBatchPut: []byte("txdata"),
	}
	encoded, err := SerializeBackupDb(original)
	require.NoError(t, err)

	decoded, err := DeserializeBackupDb(encoded)
	require.NoError(t, err)
	assert.Equal(t, original.BockNumber, decoded.BockNumber)
	assert.Equal(t, original.NodeId, decoded.NodeId)
	assert.Equal(t, original.TxBatchPut, decoded.TxBatchPut)
}

func TestSerializeBackupDb_Empty(t *testing.T) {
	original := BackUpDb{}
	encoded, err := SerializeBackupDb(original)
	require.NoError(t, err)

	decoded, err := DeserializeBackupDb(encoded)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), decoded.BockNumber)
	assert.Equal(t, "", decoded.NodeId)
}

// ---------- Buffer Pool ----------

func TestBufferPool_GetPut(t *testing.T) {
	buf := getBuffer()
	require.NotNil(t, buf)
	assert.Equal(t, 0, buf.Len())

	buf.WriteString("test")
	assert.Equal(t, 4, buf.Len())

	putBuffer(buf) // should not panic
}
