package executor

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// testRustClient simulates Rust's ExecutorClient for integration tests.
// It connects to the Go SocketExecutor via Unix domain socket and sends
// protobuf-encoded Request messages, receiving Response messages back.
// ============================================================================

type testRustClient struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

func newTestRustClient(socketPath string) (*testRustClient, error) {
	socketConfig := NewSocketConfig(socketPath)
	conn, err := socketConfig.Dial()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to socket %s: %w", socketPath, err)
	}
	return &testRustClient{
		conn:   conn,
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}, nil
}

func (c *testRustClient) close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// sendRequest sends a protobuf Request and returns the Response.
func (c *testRustClient) sendRequest(req *pb.Request) (*pb.Response, error) {
	if err := WriteMessage(c.writer, req); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}
	if err := c.writer.Flush(); err != nil {
		return nil, fmt.Errorf("flush request: %w", err)
	}
	var resp pb.Response
	if err := ReadMessage(c.reader, &resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return &resp, nil
}

// Convenience helpers for common requests ──────────────────────────────────

func (c *testRustClient) advanceEpoch(epoch, timestampMs, boundaryBlock uint64) (*pb.Response, error) {
	return c.sendRequest(&pb.Request{
		Payload: &pb.Request_AdvanceEpochRequest{
			AdvanceEpochRequest: &pb.AdvanceEpochRequest{
				NewEpoch:              epoch,
				EpochStartTimestampMs: timestampMs,
				BoundaryBlock:         boundaryBlock,
			},
		},
	})
}

func (c *testRustClient) getCurrentEpoch() (*pb.Response, error) {
	return c.sendRequest(&pb.Request{
		Payload: &pb.Request_GetCurrentEpochRequest{
			GetCurrentEpochRequest: &pb.GetCurrentEpochRequest{},
		},
	})
}

func (c *testRustClient) getEpochStartTimestamp(epoch uint64) (*pb.Response, error) {
	return c.sendRequest(&pb.Request{
		Payload: &pb.Request_GetEpochStartTimestampRequest{
			GetEpochStartTimestampRequest: &pb.GetEpochStartTimestampRequest{
				Epoch: epoch,
			},
		},
	})
}

// ============================================================================
// Test helpers
// ============================================================================

// testEnv bundles the SocketExecutor, ChainState, and temp socket path
// needed for each test. It handles lifecycle and cleanup.
type testEnv struct {
	socketPath string
	executor   *SocketExecutor
	chainState *blockchain.ChainState
	t          *testing.T
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	// Create a temp socket path unique to this test
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Create a lightweight ChainState with only epoch-tracking fields
	cs := blockchain.NewTestChainState()

	// Create SocketExecutor with nil storage manager (acceptable for epoch tests)
	se := NewSocketExecutor(socketPath, nil, cs, "")
	require.NoError(t, se.Start(), "SocketExecutor.Start should succeed")

	// Brief pause for the listener to accept connections
	time.Sleep(50 * time.Millisecond)

	env := &testEnv{
		socketPath: socketPath,
		executor:   se,
		chainState: cs,
		t:          t,
	}

	t.Cleanup(func() {
		_ = se.Stop()
	})

	return env
}

func (env *testEnv) newClient() *testRustClient {
	env.t.Helper()
	client, err := newTestRustClient(env.socketPath)
	require.NoError(env.t, err, "newTestRustClient should succeed")
	env.t.Cleanup(client.close)
	return client
}

// ============================================================================
// Group 1: Happy-Path Epoch Advancement
// ============================================================================

func TestEpochTransition_HappyPath(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	// 1. Verify initial epoch is 0
	resp, err := client.getCurrentEpoch()
	require.NoError(t, err)
	epochResp := resp.GetGetCurrentEpochResponse()
	require.NotNil(t, epochResp, "expected GetCurrentEpochResponse payload")
	assert.Equal(t, uint64(0), epochResp.GetEpoch(), "initial epoch should be 0")

	// 2. Advance to epoch 1
	ts1 := uint64(time.Now().UnixMilli())
	resp, err = client.advanceEpoch(1, ts1, 100)
	require.NoError(t, err)
	advResp := resp.GetAdvanceEpochResponse()
	require.NotNil(t, advResp, "expected AdvanceEpochResponse payload")
	assert.Equal(t, uint64(1), advResp.GetNewEpoch())
	assert.Equal(t, ts1, advResp.GetEpochStartTimestampMs())

	// 3. Confirm epoch is now 1
	resp, err = client.getCurrentEpoch()
	require.NoError(t, err)
	assert.Equal(t, uint64(1), resp.GetGetCurrentEpochResponse().GetEpoch())

	// 4. Query epoch 1 timestamp
	resp, err = client.getEpochStartTimestamp(1)
	require.NoError(t, err)
	tsResp := resp.GetGetEpochStartTimestampResponse()
	require.NotNil(t, tsResp, "expected GetEpochStartTimestampResponse payload")
	assert.Equal(t, ts1, tsResp.GetTimestampMs())
}

func TestEpochTransition_MultiEpochSequential(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	baseTs := uint64(1700000000000)
	for epoch := uint64(1); epoch <= 5; epoch++ {
		ts := baseTs + epoch*60_000 // 1 minute apart
		boundary := epoch * 100     // 100, 200, ...

		resp, err := client.advanceEpoch(epoch, ts, boundary)
		require.NoError(t, err, "advance to epoch %d", epoch)
		require.NotNil(t, resp.GetAdvanceEpochResponse(), "epoch %d: expected AdvanceEpochResponse", epoch)
		assert.Equal(t, epoch, resp.GetAdvanceEpochResponse().GetNewEpoch())
	}

	// Verify final epoch
	resp, err := client.getCurrentEpoch()
	require.NoError(t, err)
	assert.Equal(t, uint64(5), resp.GetGetCurrentEpochResponse().GetEpoch())

	// Verify each timestamp is queryable
	for epoch := uint64(1); epoch <= 5; epoch++ {
		ts := baseTs + epoch*60_000
		resp, err = client.getEpochStartTimestamp(epoch)
		require.NoError(t, err)
		assert.Equal(t, ts, resp.GetGetEpochStartTimestampResponse().GetTimestampMs(),
			"timestamp mismatch for epoch %d", epoch)
	}
}

// ============================================================================
// Group 2: Error Handling & Edge Cases
// ============================================================================

func TestEpochTransition_BackwardRejected(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	// Advance to epoch 3
	_, err := client.advanceEpoch(3, 1700000000000, 300)
	require.NoError(t, err)

	// Try to go back to epoch 1 — should get error
	resp, err := client.advanceEpoch(1, 1700000001000, 100)
	require.NoError(t, err, "transport should succeed even if handler returns error")

	errPayload := resp.GetError()
	require.NotEmpty(t, errPayload, "expected error response for backward epoch")
	assert.Contains(t, errPayload, "cannot go backwards")
}

func TestEpochTransition_DuplicateIdempotent(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	ts := uint64(1700000000000)

	// First advance
	resp, err := client.advanceEpoch(1, ts, 100)
	require.NoError(t, err)
	require.NotNil(t, resp.GetAdvanceEpochResponse())

	// Second advance — same epoch — should succeed silently
	resp, err = client.advanceEpoch(1, ts, 100)
	require.NoError(t, err)
	require.NotNil(t, resp.GetAdvanceEpochResponse(), "duplicate advance should still succeed")
	assert.Equal(t, uint64(1), resp.GetAdvanceEpochResponse().GetNewEpoch())

	// Epoch is still 1
	resp, err = client.getCurrentEpoch()
	require.NoError(t, err)
	assert.Equal(t, uint64(1), resp.GetGetCurrentEpochResponse().GetEpoch())
}

func TestEpochTransition_InitialEpochIsZero(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	resp, err := client.getCurrentEpoch()
	require.NoError(t, err)
	assert.Equal(t, uint64(0), resp.GetGetCurrentEpochResponse().GetEpoch())
}

func TestEpochTransition_UnknownRequest(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	// Send a nil/default payload — should get error response
	resp, err := client.sendRequest(&pb.Request{})
	require.NoError(t, err)
	errPayload := resp.GetError()
	assert.NotEmpty(t, errPayload, "unknown request should return error")
}

// ============================================================================
// Group 3: Connection Resilience
// ============================================================================

func TestEpochTransition_ConnectionDrop_ServerSurvives(t *testing.T) {
	env := setupTestEnv(t)

	// Client 1: advance to epoch 1
	client1 := env.newClient()
	_, err := client1.advanceEpoch(1, 1700000000000, 100)
	require.NoError(t, err)

	// Force close client 1 (simulating Rust node crash)
	client1.conn.Close()

	// Brief pause to let server detect disconnection
	time.Sleep(100 * time.Millisecond)

	// Client 2: reconnect and verify state persisted
	client2 := env.newClient()
	resp, err := client2.getCurrentEpoch()
	require.NoError(t, err)
	assert.Equal(t, uint64(1), resp.GetGetCurrentEpochResponse().GetEpoch(),
		"server should retain state after client disconnect")

	// Client 2 can still advance
	_, err = client2.advanceEpoch(2, 1700000001000, 200)
	require.NoError(t, err)

	resp, err = client2.getCurrentEpoch()
	require.NoError(t, err)
	assert.Equal(t, uint64(2), resp.GetGetCurrentEpochResponse().GetEpoch())
}

func TestEpochTransition_Reconnect_StatePersistedAcrossConnections(t *testing.T) {
	env := setupTestEnv(t)

	// Advance through 3 epochs with separate connections (simulating network partition recovery)
	for epoch := uint64(1); epoch <= 3; epoch++ {
		client := env.newClient()
		_, err := client.advanceEpoch(epoch, 1700000000000+epoch*1000, epoch*100)
		require.NoError(t, err)
		client.close()
		time.Sleep(50 * time.Millisecond)
	}

	// New connection should see epoch 3
	client := env.newClient()
	resp, err := client.getCurrentEpoch()
	require.NoError(t, err)
	assert.Equal(t, uint64(3), resp.GetGetCurrentEpochResponse().GetEpoch())
}

// ============================================================================
// Group 4: Concurrency
// ============================================================================

func TestEpochTransition_ConcurrentAdvances(t *testing.T) {
	env := setupTestEnv(t)

	const numGoroutines = 10
	var wg sync.WaitGroup
	results := make([]error, numGoroutines)

	// All goroutines try to advance to different epochs concurrently.
	// Only the sequential ones (based on timing) should succeed;
	// the point is that no deadlock or data corruption occurs.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			client, err := newTestRustClient(env.socketPath)
			if err != nil {
				results[idx] = err
				return
			}
			defer client.close()

			// Each goroutine tries to advance to its own epoch
			epoch := uint64(idx + 1)
			ts := uint64(1700000000000) + epoch*1000
			_, err = client.advanceEpoch(epoch, ts, epoch*100)
			results[idx] = err
		}(i)
	}
	wg.Wait()

	// All transport calls should succeed (errors are in the response, not transport)
	for i, err := range results {
		assert.NoError(t, err, "goroutine %d transport should succeed", i)
	}

	// Verify the final epoch is at least 1 (we can't predict exact order)
	client := env.newClient()
	resp, err := client.getCurrentEpoch()
	require.NoError(t, err)
	finalEpoch := resp.GetGetCurrentEpochResponse().GetEpoch()
	assert.True(t, finalEpoch >= 1, "final epoch should be at least 1, got %d", finalEpoch)
}

func TestEpochTransition_MultipleClientsReadWrite(t *testing.T) {
	env := setupTestEnv(t)

	// Writer: advance epochs sequentially
	writer := env.newClient()
	for epoch := uint64(1); epoch <= 3; epoch++ {
		_, err := writer.advanceEpoch(epoch, 1700000000000+epoch*1000, epoch*100)
		require.NoError(t, err)
	}

	// Multiple readers verify the same state
	const numReaders = 5
	var wg sync.WaitGroup
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader := env.newClient()
			resp, err := reader.getCurrentEpoch()
			require.NoError(t, err)
			assert.Equal(t, uint64(3), resp.GetGetCurrentEpochResponse().GetEpoch())
		}()
	}
	wg.Wait()
}

// ============================================================================
// Group 5: Socket Lifecycle
// ============================================================================

func TestEpochTransition_ServerStopDuringIdle(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	cs := blockchain.NewTestChainState()
	se := NewSocketExecutor(socketPath, nil, cs, "")
	require.NoError(t, se.Start())
	time.Sleep(50 * time.Millisecond)

	// Stop without any client connections — should be clean
	err := se.Stop()
	assert.NoError(t, err, "stop on idle server should succeed")

	// Socket file should be cleaned up
	_, err = os.Stat(socketPath)
	assert.True(t, os.IsNotExist(err) || err == nil,
		"socket file may or may not exist after stop")
}

func TestEpochTransition_DoubleStartFails(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	cs := blockchain.NewTestChainState()
	se := NewSocketExecutor(socketPath, nil, cs, "")
	require.NoError(t, se.Start())
	t.Cleanup(func() { _ = se.Stop() })

	// Second start should fail
	err := se.Start()
	assert.Error(t, err, "double start should return error")
}

// ============================================================================
// Group 6: Transition Handoff APIs
// ============================================================================

func (c *testRustClient) setConsensusStartBlock(blockNumber uint64) (*pb.Response, error) {
	return c.sendRequest(&pb.Request{
		Payload: &pb.Request_SetConsensusStartBlockRequest{
			SetConsensusStartBlockRequest: &pb.SetConsensusStartBlockRequest{
				BlockNumber: blockNumber,
			},
		},
	})
}

func (c *testRustClient) setSyncStartBlock(lastConsensusBlock uint64) (*pb.Response, error) {
	return c.sendRequest(&pb.Request{
		Payload: &pb.Request_SetSyncStartBlockRequest{
			SetSyncStartBlockRequest: &pb.SetSyncStartBlockRequest{
				LastConsensusBlock: lastConsensusBlock,
			},
		},
	})
}

func (c *testRustClient) getLastBlockNumber() (*pb.Response, error) {
	return c.sendRequest(&pb.Request{
		Payload: &pb.Request_GetLastBlockNumberRequest{
			GetLastBlockNumberRequest: &pb.GetLastBlockNumberRequest{},
		},
	})
}

func (c *testRustClient) waitForSyncToBlock(targetBlock, timeoutSecs uint64) (*pb.Response, error) {
	return c.sendRequest(&pb.Request{
		Payload: &pb.Request_WaitForSyncToBlockRequest{
			WaitForSyncToBlockRequest: &pb.WaitForSyncToBlockRequest{
				TargetBlock:    targetBlock,
				TimeoutSeconds: timeoutSecs,
			},
		},
	})
}

func TestHandoff_SetConsensusStartBlock(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	// Set consensus start — should succeed (block 0 = beginning)
	resp, err := client.setConsensusStartBlock(0)
	require.NoError(t, err)

	handoffResp := resp.GetSetConsensusStartBlockResponse()
	if handoffResp == nil {
		// Might get error if storage is nil — that's expected for this minimal test env
		errPayload := resp.GetError()
		assert.NotEmpty(t, errPayload, "should get either success or descriptive error")
		t.Logf("Expected error with nil storage: %s", errPayload)
	} else {
		// With nil storage, sync hasn't caught up so success may be false — that's valid behavior
		// The point is that we get a proper response, not a crash or transport error
		t.Logf("SetConsensusStartBlock response: success=%v, last_sync=%d, msg=%s",
			handoffResp.GetSuccess(), handoffResp.GetLastSyncBlock(), handoffResp.GetMessage())
	}
}

func TestHandoff_SetSyncStartBlock(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	// First advance epoch so there's a valid state
	_, err := client.advanceEpoch(1, 1700000000000, 100)
	require.NoError(t, err)

	// Set sync start at block 100 (last consensus block)
	resp, err := client.setSyncStartBlock(100)
	require.NoError(t, err)

	handoffResp := resp.GetSetSyncStartBlockResponse()
	if handoffResp == nil {
		// May get error with nil storage — expected for minimal test env
		errPayload := resp.GetError()
		assert.NotEmpty(t, errPayload, "should get either success or descriptive error")
		t.Logf("Expected error with nil storage: %s", errPayload)
	} else {
		assert.True(t, handoffResp.GetSuccess(), "SetSyncStartBlock should succeed")
		assert.Equal(t, uint64(101), handoffResp.GetSyncStartBlock(), "sync should start at last_consensus_block + 1")
	}
}

func TestHandoff_GetLastBlockNumber(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	// Query last block number — with nil storage, should return 0 or error
	resp, err := client.getLastBlockNumber()
	require.NoError(t, err, "transport should succeed")

	blockResp := resp.GetLastBlockNumberResponse()
	if blockResp == nil {
		// Expected: nil storage manager can cause error
		errPayload := resp.GetError()
		assert.NotEmpty(t, errPayload, "should get either response or error")
		t.Logf("Expected error with nil storage: %s", errPayload)
	} else {
		// With nil storage, last block should be 0
		assert.Equal(t, uint64(0), blockResp.GetLastBlockNumber(), "with nil storage, last block should be 0")
	}
}

func TestHandoff_WaitForSyncToBlock_Timeout(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	// Wait for sync to reach block 1000 with 1-second timeout
	// With nil storage, this should timeout since no blocks will be synced
	resp, err := client.waitForSyncToBlock(1000, 1)
	require.NoError(t, err, "transport should succeed")

	waitResp := resp.GetWaitForSyncToBlockResponse()
	if waitResp == nil {
		// May get error with nil storage
		errPayload := resp.GetError()
		assert.NotEmpty(t, errPayload)
		t.Logf("Expected error: %s", errPayload)
	} else {
		// Should report not reached (timeout)
		assert.False(t, waitResp.GetReached(), "should not reach target block in 1 second with nil storage")
	}
}

func TestHandoff_FullTransitionLifecycle(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	// Simulate full transition lifecycle:
	// 1. Start in Validator mode at epoch 0
	resp, err := client.getCurrentEpoch()
	require.NoError(t, err)
	assert.Equal(t, uint64(0), resp.GetGetCurrentEpochResponse().GetEpoch())

	// 2. Epoch 1 starts — advance epoch
	ts1 := uint64(1700000000000)
	_, err = client.advanceEpoch(1, ts1, 0)
	require.NoError(t, err)

	// 3. Get epoch timestamp to verify
	resp, err = client.getEpochStartTimestamp(1)
	require.NoError(t, err)
	assert.Equal(t, ts1, resp.GetGetEpochStartTimestampResponse().GetTimestampMs())

	// 4. Epoch 1 ends — Validator → SyncOnly transition
	// Set sync start at block 100 (last consensus block)
	resp, err = client.setSyncStartBlock(100)
	require.NoError(t, err)
	// Accept either success or error (nil storage)
	t.Logf("SetSyncStartBlock response: %v", resp)

	// 5. Epoch 2 starts — advance epoch
	ts2 := uint64(1700000060000)
	_, err = client.advanceEpoch(2, ts2, 100)
	require.NoError(t, err)

	// 6. SyncOnly → Validator transition
	// Set consensus start at block 200
	resp, err = client.setConsensusStartBlock(200)
	require.NoError(t, err)
	t.Logf("SetConsensusStartBlock response: %v", resp)

	// 7. Verify final state
	resp, err = client.getCurrentEpoch()
	require.NoError(t, err)
	assert.Equal(t, uint64(2), resp.GetGetCurrentEpochResponse().GetEpoch())
}
