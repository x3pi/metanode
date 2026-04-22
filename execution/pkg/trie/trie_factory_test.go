package trie

import (
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// TrieFactory Tests — SetStateBackend / GetStateBackend / NewStateTrie
// ══════════════════════════════════════════════════════════════════════════════

// resetBackend restores the global backend in test teardown.
func resetBackend(t *testing.T, previous string) {
	t.Helper()
	t.Cleanup(func() {
		globalStateBackend = previous
	})
}

func TestSetStateBackend_Flat(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)

	SetStateBackend(BackendFlat)
	assert.Equal(t, BackendFlat, GetStateBackend())
}

func TestSetStateBackend_MPT(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)

	SetStateBackend(BackendMPT)
	assert.Equal(t, BackendMPT, GetStateBackend())
}

func TestSetStateBackend_Verkle(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)

	SetStateBackend(BackendVerkle)
	assert.Equal(t, BackendVerkle, GetStateBackend())
}

func TestSetStateBackend_Empty_KeepsDefault(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)

	SetStateBackend(BackendVerkle) // set explicitly first
	SetStateBackend("")            // empty should keep current
	assert.Equal(t, BackendVerkle, GetStateBackend())
}

func TestSetStateBackend_Unknown_FallsBackToNOMT(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)

	SetStateBackend("unknownbackend")
	assert.Equal(t, BackendNOMT, GetStateBackend(), "unknown backend should fall back to nomt")
}

// ══════════════════════════════════════════════════════════════════════════════
// NewStateTrie factory tests
// ══════════════════════════════════════════════════════════════════════════════

func TestNewStateTrie_FlatBackend_EmptyRoot(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)
	SetStateBackend(BackendFlat)

	db := newMemFlatDB()
	st, err := NewStateTrie(e_common.Hash{}, db, true)
	require.NoError(t, err)
	require.NotNil(t, st)

	// Should be a FlatStateTrie
	_, ok := st.(*FlatStateTrie)
	assert.True(t, ok, "flat backend should return *FlatStateTrie")
}

func TestNewStateTrie_FlatBackend_EmptyRootHash(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)
	SetStateBackend(BackendFlat)

	db := newMemFlatDB()
	st, err := NewStateTrie(EmptyRootHash, db, true)
	require.NoError(t, err)
	require.NotNil(t, st)

	_, ok := st.(*FlatStateTrie)
	assert.True(t, ok, "flat backend with EmptyRootHash should return *FlatStateTrie")
}

func TestNewStateTrie_FlatBackend_NonEmptyRoot(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)
	SetStateBackend(BackendFlat)

	db := newMemFlatDB()
	root := e_common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	st, err := NewStateTrie(root, db, true)
	require.NoError(t, err)
	require.NotNil(t, st)

	_, ok := st.(*FlatStateTrie)
	assert.True(t, ok, "flat backend with non-empty root should return *FlatStateTrie")
}

func TestNewStateTrie_VerkleBackend_EmptyRoot(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)
	SetStateBackend(BackendVerkle)

	db := newMemFlatDB()
	st, err := NewStateTrie(e_common.Hash{}, db, true)
	require.NoError(t, err)
	require.NotNil(t, st)

	_, ok := st.(*VerkleStateTrie)
	assert.True(t, ok, "verkle backend should return *VerkleStateTrie")
}

func TestNewStateTrie_VerkleBackend_EmptyRootHash(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)
	SetStateBackend(BackendVerkle)

	db := newMemFlatDB()
	st, err := NewStateTrie(EmptyRootHash, db, false)
	require.NoError(t, err)
	require.NotNil(t, st)

	_, ok := st.(*VerkleStateTrie)
	assert.True(t, ok)
}

func TestNewStateTrie_VerkleBackend_NonEmptyRoot(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)
	SetStateBackend(BackendVerkle)

	db := newMemFlatDB()
	root := e_common.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	st, err := NewStateTrie(root, db, true)
	require.NoError(t, err)
	require.NotNil(t, st)

	_, ok := st.(*VerkleStateTrie)
	assert.True(t, ok, "verkle backend with non-empty root should return *VerkleStateTrie")
}

func TestNewStateTrie_BackendNotFlatStateDB_FallsBack(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)
	SetStateBackend(BackendFlat)

	// Use a minimal DB that only satisfies trie_db.DB but NOT FlatStateDB
	db := &minimalDB{}
	st, err := NewStateTrie(e_common.Hash{}, db, true)
	// Should fall back to MPT without error
	require.NoError(t, err)
	require.NotNil(t, st)
}

// ══════════════════════════════════════════════════════════════════════════════
// TrieFactory functional: round-trip via SetStateBackend → NewStateTrie → Use
// ══════════════════════════════════════════════════════════════════════════════

func TestNewStateTrie_FlatBackend_RoundTrip(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)
	SetStateBackend(BackendFlat)

	db := newMemFlatDB()
	st, err := NewStateTrie(e_common.Hash{}, db, true)
	require.NoError(t, err)

	key := testKey(0x42)
	value := testValue(0x77)

	require.NoError(t, st.Update(key, value))
	got, err := st.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value, got)
}

func TestNewStateTrie_VerkleBackend_RoundTrip(t *testing.T) {
	prev := GetStateBackend()
	resetBackend(t, prev)
	SetStateBackend(BackendVerkle)

	db := newMemFlatDB()
	st, err := NewStateTrie(e_common.Hash{}, db, true)
	require.NoError(t, err)

	key := testKey(0x99)
	value := testValue(0x88)

	require.NoError(t, st.Update(key, value))
	got, err := st.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value, got)
}

// ══════════════════════════════════════════════════════════════════════════════
// minimalDB — implements trie_db.DB but NOT FlatStateDB
// Used to test fallback to MPT when DB doesn't support FlatStateDB
// ══════════════════════════════════════════════════════════════════════════════

type minimalDB struct{}

func (m *minimalDB) Get(key []byte) ([]byte, error) {
	return nil, nil
}
