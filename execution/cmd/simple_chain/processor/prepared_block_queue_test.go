package processor

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreparedBlockQueue_BasicLifecycle(t *testing.T) {
	q := NewPreparedBlockQueue(2)

	block := &pb.ExecutableBlock{
		GlobalExecIndex:   100,
		BlockNumber:       50,
		CommitIndex:       40,
		Epoch:             1,
		CommitTimestampMs: 1700000000000,
	}

	ctx := context.Background()
	prep, err := q.PrepareBlock(ctx, block, 50)
	require.NoError(t, err)
	require.NotNil(t, prep)

	assert.Equal(t, uint64(100), prep.GEI)
	assert.Equal(t, uint64(50), prep.BlockNum)
	assert.Equal(t, uint32(40), prep.CommitIndex)
	assert.Equal(t, uint64(1), prep.Epoch)

	// Fetch from queue
	retrieved, ok := q.GetPreparedBlock(100)
	assert.True(t, ok)
	assert.Equal(t, prep, retrieved)

	// Deduplication on second prepare
	prep2, err := q.PrepareBlock(ctx, block, 50)
	require.NoError(t, err)
	assert.Same(t, prep, prep2, "Should return same instance on deduplication")

	// Delete
	q.DeletePreparedBlock(100)
	_, ok = q.GetPreparedBlock(100)
	assert.False(t, ok)
}

func TestPreparedBlockQueue_ConcurrentPreparation(t *testing.T) {
	q := NewPreparedBlockQueue(4)

	var wg sync.WaitGroup
	numBlocks := 20
	errChan := make(chan error, numBlocks)

	for i := 0; i < numBlocks; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			b := &pb.ExecutableBlock{
				GlobalExecIndex:   uint64(idx + 1),
				BlockNumber:       uint64(idx + 10),
				CommitIndex:       uint32(idx),
				Epoch:             1,
				CommitTimestampMs: 1700000000000,
			}
			ctx := context.Background()
			_, err := q.PrepareBlock(ctx, b, uint64(idx+10))
			if err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		t.Fatalf("Unexpected error during concurrent preparation: %v", err)
	}

	// Verify all blocks are stored
	for i := 0; i < numBlocks; i++ {
		_, ok := q.GetPreparedBlock(uint64(i + 1))
		assert.True(t, ok, "GEI=%d should be in queue", i+1)
	}

	// Clean through half
	q.CleanThrough(10)
	for i := 0; i < 10; i++ {
		_, ok := q.GetPreparedBlock(uint64(i + 1))
		assert.False(t, ok, "GEI=%d should be cleaned", i+1)
	}
	for i := 10; i < numBlocks; i++ {
		_, ok := q.GetPreparedBlock(uint64(i + 1))
		assert.True(t, ok, "GEI=%d should still exist", i+1)
	}

	// Clear all
	q.Clear()
	for i := 0; i < numBlocks; i++ {
		_, ok := q.GetPreparedBlock(uint64(i + 1))
		assert.False(t, ok, "All should be cleared")
	}
}

func TestPreparedBlockQueue_ContextCancellation(t *testing.T) {
	q := NewPreparedBlockQueue(1) // Semaphore capacity 1

	// Block the single semaphore slot
	q.prepSem <- struct{}{} // Artificially saturate semaphore

	cancelCtx, cancelFunc := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelFunc()

	b := &pb.ExecutableBlock{
		GlobalExecIndex: 999,
		BlockNumber:     1,
	}

	_, err := q.PrepareBlock(cancelCtx, b, 1)
	assert.Error(t, err, "Should fail when context expires while waiting for semaphore")
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// Release semaphore
	<-q.prepSem
}
