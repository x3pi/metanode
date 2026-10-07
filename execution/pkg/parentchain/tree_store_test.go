package parentchain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T-U12: overlay merges writes correctly with base data and rolls back on tx failure.
func TestOverlayTree_LayersAndMerge(t *testing.T) {
	base := NewMemTreeKV()
	k1 := TreeKey(NamespaceFloat, []byte("key1"))
	k2 := TreeKey(NamespaceFloat, []byte("key2"))
	require.NoError(t, base.Put(k1, []byte("val1_base")))

	overlay := NewOverlayTree(base)

	// Read from base through overlay
	v, found, err := overlay.Get(k1)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []byte("val1_base"), v)

	// Tx 1: push, write k1 and k2, drop (failure)
	overlay.Push()
	require.NoError(t, overlay.Put(k1, []byte("val1_tx1")))
	require.NoError(t, overlay.Put(k2, []byte("val2_tx1")))
	v, _, _ = overlay.Get(k1)
	assert.Equal(t, []byte("val1_tx1"), v)
	overlay.Drop()

	// After drop, writes from tx 1 must be gone
	v, found, _ = overlay.Get(k1)
	assert.Equal(t, []byte("val1_base"), v)
	_, found2, _ := overlay.Get(k2)
	assert.False(t, found2)

	// Tx 2: push, write k2, merge (success)
	overlay.Push()
	require.NoError(t, overlay.Put(k2, []byte("val2_tx2")))
	overlay.Merge()

	// After merge, k2 exists in block layer
	v2, found2, _ := overlay.Get(k2)
	assert.True(t, found2)
	assert.Equal(t, []byte("val2_tx2"), v2)

	// BlockWrites returns sorted keys
	keys, writes := overlay.BlockWrites()
	assert.Equal(t, 1, len(keys))
	assert.Equal(t, k2, keys[0])
	assert.Equal(t, []byte("val2_tx2"), writes[k2])
}

func TestTreeStore_TypedOperations(t *testing.T) {
	mem := NewMemTreeKV()
	store := NewTreeStore(mem)

	// 1. Float balance
	keyHash := common.HexToHash("0x1234")
	bal, err := store.GetFloat(keyHash)
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(0), bal)

	require.NoError(t, store.SetFloat(keyHash, big.NewInt(500000)))
	bal2, err := store.GetFloat(keyHash)
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(500000), bal2)

	// 2. Chain registry & GetAllChainRegistryKeys
	var pub1 cm.PublicKey
	copy(pub1[:], []byte("cluster_one_key_48_bytes_padding_000000000000000"))
	require.NoError(t, store.SetChainRegistry(keyHash, ChainRegistryEntry{
		FloatIdentityKey:     pub1,
		ClusterIDDescriptive: 101,
	}))

	e, found, err := store.GetChainRegistry(keyHash)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, pub1, e.FloatIdentityKey)
	assert.Equal(t, uint64(101), e.ClusterIDDescriptive)

	allKeys, err := store.GetAllChainRegistryKeys()
	require.NoError(t, err)
	assert.Equal(t, []common.Hash{keyHash}, allKeys)

	// 3. Claimed
	msgID := common.HexToHash("0xabcd")
	outcome, err := store.GetClaimed(msgID)
	require.NoError(t, err)
	assert.Equal(t, FloatOutcomeNone, outcome)

	require.NoError(t, store.SetClaimed(msgID, FloatOutcomeCredited))
	outcome2, err := store.GetClaimed(msgID)
	require.NoError(t, err)
	assert.Equal(t, FloatOutcomeCredited, outcome2)

	// 4. TransferRecord
	rec := FloatTransferRecord{
		DestKey:              pub1,
		Value:                big.NewInt(999),
		ConfirmedAtBlockTime: 12345,
	}
	require.NoError(t, store.SetTransferRecord(msgID, rec))
	gotRec, found, err := store.GetTransferRecord(msgID)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, rec.Value, gotRec.Value)
	assert.Equal(t, rec.ConfirmedAtBlockTime, gotRec.ConfirmedAtBlockTime)

	// 5. Inbound transfers
	ev := &TransferEvent{
		MsgID:      msgID,
		DestPubKey: pub1,
		Sender:     common.HexToAddress("0x1111111111111111111111111111111111111111"),
		Target:     common.HexToAddress("0x2222222222222222222222222222222222222222"),
		Amount:     big.NewInt(777),
		BlockTime:  1000,
	}
	require.NoError(t, store.AppendInboundTransfer(keyHash, ev))
	events, total, err := store.GetInboundTransfers(keyHash, 0)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), total)
	require.Len(t, events, 1)
	assert.Equal(t, ev.Amount, events[0].Amount)

	// 6. Nonce
	addr := common.HexToAddress("0xaaaa")
	nonce, err := store.GetNonce(addr)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), nonce)

	require.NoError(t, store.SetNonce(addr, 1))
	nonce1, err := store.GetNonce(addr)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), nonce1)
}
