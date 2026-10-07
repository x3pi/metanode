package smart_contract_db

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/stretchr/testify/require"
)

// A speculative copy must inherit the uncommitted in-memory state of its parent (tries, pending code)
// and must not share it: writes on the copy never leak back into the original.
func TestCopy_InheritsAndIsolatesInMemoryState(t *testing.T) {
	orig := NewSmartContractDB(newTestDB(), newTestDB(), nil, nil)

	addr := common.HexToAddress("0x01")
	tr, err := trie.New(common.Hash{}, newTestDB(), false)
	require.NoError(t, err)
	require.NoError(t, tr.Update([]byte("k"), []byte("v1")))
	orig.smartContractStorageTries.Store(addr, trie.StateTrie(tr))

	codeHash := common.HexToHash("0xc0de")
	orig.pendingCode.Store(codeHash, []byte{1, 2, 3})

	cp := orig.Copy(nil)

	v, ok := cp.smartContractStorageTries.Load(addr)
	require.True(t, ok, "copy must inherit the parent's dirty storage trie")
	got, err := v.(trie.StateTrie).Get([]byte("k"))
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), got)
	code, ok := cp.pendingCode.Load(codeHash)
	require.True(t, ok)
	require.Equal(t, []byte{1, 2, 3}, code)

	// isolation
	require.NoError(t, v.(trie.StateTrie).Update([]byte("k"), []byte("v2")))
	cp.pendingCode.Delete(codeHash)
	ov, _ := orig.smartContractStorageTries.Load(addr)
	got, err = ov.(trie.StateTrie).Get([]byte("k"))
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), got, "write on the copy leaked into the original")
	_, ok = orig.pendingCode.Load(codeHash)
	require.True(t, ok)
}
