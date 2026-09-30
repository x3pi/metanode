package parentchain

import (
	"bytes"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to create a new isolated DBStore for testing
func newTestDBStore(t *testing.T) *DBStore {
	dir := t.TempDir()
	store, err := NewDBStore(dir)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

// Simple test executor that supports two mock tx formats:
// "set:<key>:<val>" -> sets float balance
// "err:<msg>" -> returns error
func mockExecutor(store Store, txIndex int, rawTx []byte) (*Receipt, error) {
	s := string(rawTx)
	txHash := crypto.Keccak256Hash(rawTx)

	if len(s) >= 4 && s[:4] == "err:" {
		return &Receipt{
			TxHash:    txHash,
			Status:    0,
			ErrorCode: 99,
		}, errors.New(s[4:])
	}

	if len(s) >= 4 && s[:4] == "set:" {
		parts := bytes.Split(rawTx[4:], []byte(":"))
		if len(parts) == 2 {
			k := common.BytesToHash(crypto.Keccak256(parts[0]))
			val := new(big.Int)
			val.SetString(string(parts[1]), 10)
			if err := store.SetFloat(k, val); err != nil {
				return nil, err
			}
			return &Receipt{
				TxHash: txHash,
				Status: 1,
			}, nil
		}
	}

	return &Receipt{
		TxHash: txHash,
		Status: 1,
	}, nil
}

// T-U1: Hai node độc lập cùng chạy chuỗi block 1..N phải ra cùng
// state_root, txs_root, receipts_root, block_hash ở MỌI block.
func TestBlockExec_DeterministicTwoNodes(t *testing.T) {
	nodeA := newTestDBStore(t)
	nodeB := newTestDBStore(t)

	blocks := []BlockInput{
		{
			Number:        1,
			GEI:           1,
			CommitIndex:   1,
			Epoch:         0,
			TimestampMs:   10000,
			LeaderAddress: common.HexToAddress("0xaaaa"),
			CommitDigest:  common.HexToHash("0x1111"),
			Txs: [][]byte{
				[]byte("set:alice:100"),
				[]byte("set:bob:200"),
			},
		},
		{
			Number:        2,
			GEI:           2,
			CommitIndex:   2,
			Epoch:         0,
			TimestampMs:   11000,
			LeaderAddress: common.HexToAddress("0xbbbb"),
			CommitDigest:  common.HexToHash("0x2222"),
			Txs: [][]byte{
				[]byte("set:alice:300"),
				[]byte("set:charlie:500"),
			},
		},
		{
			Number:        3,
			GEI:           3,
			CommitIndex:   3,
			Epoch:         0,
			TimestampMs:   12000,
			LeaderAddress: common.HexToAddress("0xcccc"),
			CommitDigest:  common.HexToHash("0x3333"),
			Txs:           nil, // empty block
		},
	}

	for _, in := range blocks {
		resA, errA := nodeA.ApplyBlock(in, mockExecutor)
		require.NoError(t, errA)

		resB, errB := nodeB.ApplyBlock(in, mockExecutor)
		require.NoError(t, errB)

		assert.Equal(t, resA.Record.Header.StateRoot, resB.Record.Header.StateRoot, "StateRoot mismatch at block %d", in.Number)
		assert.Equal(t, resA.Record.Header.TxsRoot, resB.Record.Header.TxsRoot, "TxsRoot mismatch at block %d", in.Number)
		assert.Equal(t, resA.Record.Header.ReceiptsRoot, resB.Record.Header.ReceiptsRoot, "ReceiptsRoot mismatch at block %d", in.Number)
		assert.Equal(t, resA.Record.BlockHash, resB.Record.BlockHash, "BlockHash mismatch at block %d", in.Number)
	}
}

// T-U3: Replay block đã áp dụng => không đổi gì, Replayed=true.
func TestBlockExec_ReplayIdempotency(t *testing.T) {
	node := newTestDBStore(t)

	blk := BlockInput{
		Number:        1,
		GEI:           1,
		CommitIndex:   1,
		Epoch:         0,
		TimestampMs:   10000,
		LeaderAddress: common.HexToAddress("0xaaaa"),
		CommitDigest:  common.HexToHash("0x1111"),
		Txs: [][]byte{
			[]byte("set:alice:100"),
		},
	}

	res1, err := node.ApplyBlock(blk, mockExecutor)
	require.NoError(t, err)
	assert.False(t, res1.Replayed)

	// Replay exact same block
	res2, err := node.ApplyBlock(blk, mockExecutor)
	require.NoError(t, err)
	assert.True(t, res2.Replayed)
	assert.Equal(t, res1.Record.BlockHash, res2.Record.BlockHash)
	assert.Equal(t, res1.Record.Header.StateRoot, res2.Record.Header.StateRoot)
}

// T-U4: Cùng số block khác nội dung => ErrBlockConflict, state không đổi.
func TestBlockExec_ConflictDetection(t *testing.T) {
	node := newTestDBStore(t)

	blk1 := BlockInput{
		Number:        1,
		GEI:           1,
		CommitIndex:   1,
		Epoch:         0,
		TimestampMs:   10000,
		LeaderAddress: common.HexToAddress("0xaaaa"),
		CommitDigest:  common.HexToHash("0x1111"),
		Txs: [][]byte{
			[]byte("set:alice:100"),
		},
	}
	_, err := node.ApplyBlock(blk1, mockExecutor)
	require.NoError(t, err)

	// Same block number (1), but different txs
	blkConflict := BlockInput{
		Number:        1,
		GEI:           1,
		CommitIndex:   1,
		Epoch:         0,
		TimestampMs:   10000,
		LeaderAddress: common.HexToAddress("0xaaaa"),
		CommitDigest:  common.HexToHash("0x1111"),
		Txs: [][]byte{
			[]byte("set:alice:9999"), // different tx
		},
	}
	_, err = node.ApplyBlock(blkConflict, mockExecutor)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBlockConflict))

	// Verify state wasn't modified
	aliceKey := common.BytesToHash(crypto.Keccak256([]byte("alice")))
	bal, err := node.GetFloat(aliceKey)
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(100), bal)
}

// T-U5: Nhảy cóc => ErrBlockGap; chain mới nhận block != 1 => ErrBlockGap.
func TestBlockExec_GapDetection(t *testing.T) {
	node := newTestDBStore(t)

	// 1. Fresh chain receiving block 2 directly -> ErrBlockGap
	blk2 := BlockInput{
		Number:      2,
		GEI:         2,
		CommitIndex: 2,
		TimestampMs: 10000,
		Txs:         [][]byte{[]byte("set:alice:100")},
	}
	_, err := node.ApplyBlock(blk2, mockExecutor)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBlockGap))

	// 2. Apply block 1 successfully
	blk1 := BlockInput{
		Number:      1,
		GEI:         1,
		CommitIndex: 1,
		TimestampMs: 10000,
		Txs:         [][]byte{[]byte("set:alice:100")},
	}
	_, err = node.ApplyBlock(blk1, mockExecutor)
	require.NoError(t, err)

	// 3. Skip to block 3 -> ErrBlockGap
	blk3 := BlockInput{
		Number:      3,
		GEI:         3,
		CommitIndex: 3,
		TimestampMs: 12000,
		Txs:         [][]byte{[]byte("set:alice:300")},
	}
	_, err = node.ApplyBlock(blk3, mockExecutor)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBlockGap))
}

// T-U6: Tx lỗi giữa chừng (đã ghi rồi lỗi) => không để lại ghi dở, tx sau vẫn chạy.
func TestBlockExec_TxFailureRollback(t *testing.T) {
	node := newTestDBStore(t)

	// Block containing 3 transactions:
	// tx 0: set alice:100 (success)
	// tx 1: set bob:200, then error! (failure -> must drop writes)
	// tx 2: set charlie:300 (success)
	execWithPartialFail := func(store Store, txIndex int, rawTx []byte) (*Receipt, error) {
		txHash := crypto.Keccak256Hash(rawTx)
		if txIndex == 1 {
			// Write to bob, then return error
			bobKey := common.BytesToHash(crypto.Keccak256([]byte("bob")))
			_ = store.SetFloat(bobKey, big.NewInt(200))
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 50}, errors.New("tx1 partial failure")
		}
		return mockExecutor(store, txIndex, rawTx)
	}

	blk := BlockInput{
		Number:      1,
		GEI:         1,
		CommitIndex: 1,
		TimestampMs: 10000,
		Txs: [][]byte{
			[]byte("set:alice:100"),
			[]byte("fail_tx"),
			[]byte("set:charlie:300"),
		},
	}

	res, err := node.ApplyBlock(blk, execWithPartialFail)
	require.NoError(t, err)
	require.Len(t, res.TxErrors, 3)
	assert.NoError(t, res.TxErrors[0])
	assert.Error(t, res.TxErrors[1])
	assert.NoError(t, res.TxErrors[2])

	// Receipts status check
	assert.Equal(t, uint8(1), res.Record.Receipts[0].Status)
	assert.Equal(t, uint8(0), res.Record.Receipts[1].Status)
	assert.Equal(t, uint8(1), res.Record.Receipts[2].Status)

	// State check:
	// Alice has 100
	aliceKey := common.BytesToHash(crypto.Keccak256([]byte("alice")))
	balAlice, err := node.GetFloat(aliceKey)
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(100), balAlice)

	// Bob has 0 (writes were dropped due to overlay rollback!)
	bobKey := common.BytesToHash(crypto.Keccak256([]byte("bob")))
	balBob, err := node.GetFloat(bobKey)
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(0), balBob)

	// Charlie has 300
	charlieKey := common.BytesToHash(crypto.Keccak256([]byte("charlie")))
	balCharlie, err := node.GetFloat(charlieKey)
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(300), balCharlie)
}

// T-U8: Không dùng đồng hồ: chạy hai thời điểm khác nhau hoặc commit_timestamp_ms == 0
// cho ra kết quả giống nhau.
func TestBlockExec_ZeroTimestampDeterminism(t *testing.T) {
	nodeA := newTestDBStore(t)
	nodeB := newTestDBStore(t)

	blk1 := BlockInput{
		Number:      1,
		GEI:         1,
		CommitIndex: 1,
		TimestampMs: 50000,
		Txs:         [][]byte{[]byte("set:alice:100")},
	}
	res1A, err := nodeA.ApplyBlock(blk1, mockExecutor)
	require.NoError(t, err)
	res1B, err := nodeB.ApplyBlock(blk1, mockExecutor)
	require.NoError(t, err)
	assert.Equal(t, res1A.Record.BlockHash, res1B.Record.BlockHash)

	// Block 2 has TimestampMs == 0 (must use previous block's timestamp 50000, never time.Now())
	blk2 := BlockInput{
		Number:      2,
		GEI:         2,
		CommitIndex: 2,
		TimestampMs: 0, // 0 timestamp!
		Txs:         [][]byte{[]byte("set:bob:200")},
	}

	res2A, err := nodeA.ApplyBlock(blk2, mockExecutor)
	require.NoError(t, err)
	res2B, err := nodeB.ApplyBlock(blk2, mockExecutor)
	require.NoError(t, err)

	assert.Equal(t, uint64(50000), res2A.Record.Header.TimestampMs)
	assert.Equal(t, res2A.Record.Header.TimestampMs, res2B.Record.Header.TimestampMs)
	assert.Equal(t, res2A.Record.BlockHash, res2B.Record.BlockHash)
}

// T-U11: Proof state của một khóa kiểm được; proof sai bị từ chối.
func TestBlockExec_StateProofVerification(t *testing.T) {
	node := newTestDBStore(t)

	aliceKeyHash := common.HexToHash("0x1234")
	blk1 := BlockInput{
		Number:      1,
		GEI:         1,
		CommitIndex: 1,
		TimestampMs: 10000,
		Txs: [][]byte{
			[]byte("set:alice:999999"),
		},
	}
	res, err := node.ApplyBlock(blk1, func(store Store, txIndex int, rawTx []byte) (*Receipt, error) {
		_ = store.SetFloat(aliceKeyHash, big.NewInt(999999))
		return &Receipt{TxHash: crypto.Keccak256Hash(rawTx), Status: 1}, nil
	})
	require.NoError(t, err)

	stateRoot := res.Record.Header.StateRoot

	// 1. Generate proof for alice's float key
	rawKey := TreeKey(NamespaceFloat, aliceKeyHash.Bytes())
	proof, err := node.GenerateProof(rawKey)
	require.NoError(t, err)
	require.NotEmpty(t, proof)

	// 2. Verify proof with correct value
	expectedVal := EncodeBigInt(big.NewInt(999999))
	valid, err := nomt_ffi.VerifyProof(stateRoot, rawKey, expectedVal, proof)
	require.NoError(t, err)
	assert.True(t, valid, "State proof should verify against StateRoot")

	// 3. Verify proof with wrong value
	wrongVal := EncodeBigInt(big.NewInt(111111))
	validWrong, err := nomt_ffi.VerifyProof(stateRoot, rawKey, wrongVal, proof)
	require.NoError(t, err)
	assert.False(t, validWrong, "State proof with wrong value must be rejected")

	// 4. Verify proof for non-existent key
	nonExistentKey := TreeKey(NamespaceFloat, common.HexToHash("0xdeadbeef").Bytes())
	proofNonExist, err := node.GenerateProof(nonExistentKey)
	require.NoError(t, err)
	validNonExist, err := nomt_ffi.VerifyProof(stateRoot, nonExistentKey, nil, proofNonExist)
	require.NoError(t, err)
	assert.True(t, validNonExist, "Non-existence proof should verify with nil value")
}

// T-U7: Giả lập crash và kiểm tra tính nhất quán rào chắn độ bền.
func TestBlockExec_DurabilityBarrier(t *testing.T) {
	dir := t.TempDir()

	// Phase 1: Create store, commit block 1 and 2
	{
		store, err := NewDBStore(dir)
		require.NoError(t, err)

		blk1 := BlockInput{
			Number:      1,
			GEI:         1,
			CommitIndex: 1,
			TimestampMs: 10000,
			Txs:         [][]byte{[]byte("set:alice:100")},
		}
		_, err = store.ApplyBlock(blk1, mockExecutor)
		require.NoError(t, err)

		blk2 := BlockInput{
			Number:      2,
			GEI:         2,
			CommitIndex: 2,
			TimestampMs: 11000,
			Txs:         [][]byte{[]byte("set:bob:200")},
		}
		_, err = store.ApplyBlock(blk2, mockExecutor)
		require.NoError(t, err)

		_ = store.Close()
	}

	// Phase 2: Reopen store -> progress, records, and state must be intact
	{
		store, err := NewDBStore(dir)
		require.NoError(t, err)
		defer store.Close()

		prog, err := store.LastApplied()
		require.NoError(t, err)
		assert.Equal(t, uint64(2), prog.LastBlock)

		rec1, found1, err := store.GetBlockRecord(1)
		require.NoError(t, err)
		assert.True(t, found1)
		assert.Equal(t, uint64(1), rec1.Header.Number)

		rec2, found2, err := store.GetBlockRecord(2)
		require.NoError(t, err)
		assert.True(t, found2)
		assert.Equal(t, uint64(2), rec2.Header.Number)

		// State intact
		aliceKey := common.BytesToHash(crypto.Keccak256([]byte("alice")))
		balAlice, err := store.GetFloat(aliceKey)
		require.NoError(t, err)
		assert.Equal(t, big.NewInt(100), balAlice)

		bobKey := common.BytesToHash(crypto.Keccak256([]byte("bob")))
		balBob, err := store.GetFloat(bobKey)
		require.NoError(t, err)
		assert.Equal(t, big.NewInt(200), balBob)

		// Next block must be block 3
		blk3 := BlockInput{
			Number:      3,
			GEI:         3,
			CommitIndex: 3,
			TimestampMs: 12000,
			Txs:         [][]byte{[]byte("set:alice:300")},
		}
		res3, err := store.ApplyBlock(blk3, mockExecutor)
		require.NoError(t, err)
		assert.Equal(t, uint64(3), res3.Record.Header.Number)
	}
}
