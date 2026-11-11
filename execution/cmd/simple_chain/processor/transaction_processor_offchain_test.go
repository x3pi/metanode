package processor

import (
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/explorer"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
)

func TestEVMTimestampSeconds(t *testing.T) {
	const headerTimestampMs uint64 = 1_790_678_281_000
	const wantTimestampSeconds uint64 = 1_790_678_281

	if got := evmTimestampSeconds(headerTimestampMs); got != wantTimestampSeconds {
		t.Fatalf("EVM timestamp = %d, want %d", got, wantTimestampSeconds)
	}
}

func TestExecuteTransactionOffChain_NeverIndexesIntoExplorerOrMutatesTx(t *testing.T) {
	prevBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	t.Cleanup(func() { trie.SetStateBackend(prevBackend) })

	v := newReadTxExecutor(t)

	tmpDir := t.TempDir()
	primaryExp, err := explorer.NewExplorerSearchService(filepath.Join(tmpDir, "explorer"), 256, 2)
	require.NoError(t, err)
	roExp, err := explorer.NewExplorerSearchService(filepath.Join(tmpDir, "explorer-ro"), 256, 2)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = primaryExp.Close()
		_ = roExp.Close()
	})

	v.storageManager.SetExplorerSearchService(primaryExp)
	v.storageManager.SetExplorerSearchServiceReadOnly(roExp)

	// PUSH4 0xdeadbeef PUSH1 0x00 MSTORE PUSH1 0x04 PUSH1 0x1c RETURN
	evmCode := []byte{0x63, 0xde, 0xad, 0xbe, 0xef, 0x60, 0x00, 0x52, 0x60, 0x04, 0x60, 0x1c, 0xf3}
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	tx := deployTx(sender, evmCode, big.NewInt(0), 200_000)
	origNonce := tx.GetNonce()
	origHash := tx.Hash()

	res, err := v.ExecuteTransactionOffChain(tx, false)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Len(t, res.Return(), 20, "off-chain deploy returns the 20-byte created contract address")

	// Verify transaction fields were NOT mutated (no SetNonce / ClearCacheHash side effects)
	assert.Equal(t, origNonce, tx.GetNonce(), "off-chain execution must not mutate transaction nonce")
	assert.Equal(t, origHash, tx.Hash(), "off-chain execution must not mutate transaction hash")

	// Verify ZERO transactions were indexed into either primary or read-only explorer
	require.NoError(t, primaryExp.Commit())
	require.NoError(t, roExp.Commit())

	_, totalPrimary, err := primaryExp.SearchTransactions("address:"+sender.Hex(), 0, 10)
	require.NoError(t, err)
	assert.Equal(t, uint(0), totalPrimary, "off-chain call must never pollute primary explorer")

	_, totalRO, err := roExp.SearchTransactions("address:"+sender.Hex(), 0, 10)
	require.NoError(t, err)
	assert.Equal(t, uint(0), totalRO, "off-chain call must never index into read-only explorer")
}
