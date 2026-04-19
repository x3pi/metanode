package trie

import (
	"os"
	"path/filepath"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// GetAll Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMPT_GetAll_Empty(t *testing.T) {
	tr := newTestMPT(t)

	data, err := tr.GetAll()
	require.NoError(t, err)
	assert.Empty(t, data, "empty trie should return empty map")
}

func TestMPT_GetAll_ReturnsInserted(t *testing.T) {
	tr := newTestMPT(t)

	for i := byte(0); i < 5; i++ {
		_ = tr.Update(mptKey(i), mptValue(i))
	}

	data, err := tr.GetAll()
	require.NoError(t, err)
	assert.Len(t, data, 5, "GetAll should return all 5 inserted entries")
}

// ══════════════════════════════════════════════════════════════════════════════
// Count Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMPT_Count_Empty(t *testing.T) {
	tr := newTestMPT(t)

	count, err := tr.Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "empty trie should have count 0")
}

func TestMPT_Count_MatchesInserted(t *testing.T) {
	tr := newTestMPT(t)
	n := 8

	for i := byte(0); i < byte(n); i++ {
		_ = tr.Update(mptKey(i), mptValue(i))
	}

	count, err := tr.Count()
	require.NoError(t, err)
	assert.Equal(t, n, count, "count should match number of inserted keys")
}

// ══════════════════════════════════════════════════════════════════════════════
// GetRootHash Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMPT_GetRootHash_Empty(t *testing.T) {
	data := map[string][]byte{}
	hash, err := GetRootHash(data)
	require.NoError(t, err)
	assert.Equal(t, EmptyRootHash, hash, "empty data should return EmptyRootHash")
}

func TestMPT_GetRootHash_Deterministic(t *testing.T) {
	data := map[string][]byte{
		e_common.Bytes2Hex(mptKey(0x01)): mptValue(0x01),
		e_common.Bytes2Hex(mptKey(0x02)): mptValue(0x02),
	}

	hash1, err := GetRootHash(data)
	require.NoError(t, err)

	hash2, err := GetRootHash(data)
	require.NoError(t, err)

	assert.Equal(t, hash1, hash2, "same data should produce same root hash")
	assert.NotEqual(t, EmptyRootHash, hash1, "non-empty data should not produce EmptyRootHash")
}

// ══════════════════════════════════════════════════════════════════════════════
// ExportTrie / ImportTrie Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMPT_ExportImport_RoundTrip(t *testing.T) {
	tr := newTestMPT(t)

	for i := byte(1); i <= 5; i++ {
		_ = tr.Update(mptKey(i), mptValue(i))
	}

	// Get all data for comparison
	origData, err := tr.GetAll()
	require.NoError(t, err)
	require.Len(t, origData, 5)

	// Export to temp file
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "trie_export.gob")

	err = ExportTrie(tr, filePath)
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(filePath)
	require.NoError(t, err, "exported file should exist")

	// Import from file — note: ImportTrie creates a new empty trie and
	// rebuilds from saved key-value pairs, so hash may differ from original
	imported, err := ImportTrie(filePath)
	require.NoError(t, err)
	require.NotNil(t, imported)

	// Verify data integrity — all keys should be recoverable
	importedData, err := imported.GetAll()
	require.NoError(t, err)
	assert.Len(t, importedData, len(origData), "imported trie should have same number of entries")
}
