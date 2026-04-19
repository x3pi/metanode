package executor

import (
	"bufio"
	"encoding/binary"
	"net"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// ============================================================================
// Helper: sendExecutableBlock writes a ExecutableBlock message to the
// Listener socket using the same uvarint-prefixed protocol that Rust uses.
// ============================================================================

func sendExecutableBlock(conn net.Conn, data *pb.ExecutableBlock) error {
	buf, err := proto.Marshal(data)
	if err != nil {
		return err
	}
	// Write uvarint-encoded length prefix (same as Rust executor sends)
	lenBuf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(lenBuf, uint64(len(buf)))
	if _, err := conn.Write(lenBuf[:n]); err != nil {
		return err
	}
	// Write proto body
	_, err = conn.Write(buf)
	return err
}

// connectToListener dials the Listener's socket and returns the connection.
func connectToListener(t *testing.T, socketPath string) net.Conn {
	t.Helper()
	sc := NewSocketConfig(socketPath)
	conn, err := sc.Dial()
	require.NoError(t, err, "should connect to Listener socket")
	t.Cleanup(func() { conn.Close() })
	return conn
}

// makeExecutableBlock creates a ExecutableBlock with given parameters.
func makeExecutableBlock(gei uint64, epoch uint64, txDigests [][]byte, opts ...func(*pb.ExecutableBlock)) *pb.ExecutableBlock {
	var txs []*pb.TransactionExe
	for _, d := range txDigests {
		txs = append(txs, &pb.TransactionExe{Digest: d})
	}

	data := &pb.ExecutableBlock{
		GlobalExecIndex:   gei,
		CommitIndex:       uint32(gei),
		Epoch:             epoch,
		CommitTimestampMs: uint64(time.Now().UnixMilli()),
		LeaderAuthorIndex: 0,
		LeaderAddress:     []byte{0xAA, 0xBB, 0xCC, 0xDD, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01},
	}

	if len(txs) > 0 {
		data.Transactions = txs
	}

	for _, fn := range opts {
		fn(data)
	}
	return data
}

// ============================================================================
// Test Group 1: ExecutableBlock Processing via Listener
// These tests simulate Rust sending ExecutableBlock over UDS and verify
// that the Go Listener correctly receives and decodes the data.
// ============================================================================

func TestExecutableBlock_SingleBlockReceived(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "listener.sock")

	// Start Listener
	listener := NewListener(socketPath)
	require.NoError(t, listener.Start())
	t.Cleanup(func() { listener.Stop() })
	time.Sleep(50 * time.Millisecond)

	// Connect as Rust and send one ExecutableBlock
	conn := connectToListener(t, socketPath)

	txDigest := []byte("fake-tx-data-0001")
	sent := makeExecutableBlock(1, 0, [][]byte{txDigest})
	require.NoError(t, sendExecutableBlock(conn, sent))

	// Read from DataChannel
	select {
	case received := <-listener.DataChannel():
		require.NotNil(t, received)
		assert.Equal(t, uint64(1), received.GetGlobalExecIndex())
		assert.Equal(t, uint64(0), received.GetEpoch())
		require.Len(t, received.Transactions, 1)
		assert.Equal(t, txDigest, received.Transactions[0].Digest)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for ExecutableBlock on DataChannel")
	}
}

func TestExecutableBlock_EmptyCommitReceived(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "listener.sock")

	listener := NewListener(socketPath)
	require.NoError(t, listener.Start())
	t.Cleanup(func() { listener.Stop() })
	time.Sleep(50 * time.Millisecond)

	conn := connectToListener(t, socketPath)

	// Send empty commit (no blocks, no transactions)
	sent := makeExecutableBlock(5, 1, nil)
	require.NoError(t, sendExecutableBlock(conn, sent))

	select {
	case received := <-listener.DataChannel():
		require.NotNil(t, received)
		assert.Equal(t, uint64(5), received.GetGlobalExecIndex())
		assert.Len(t, received.Transactions, 0, "empty commit should have no txs")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for empty ExecutableBlock")
	}
}

func TestExecutableBlock_MultiTxMerge(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "listener.sock")

	listener := NewListener(socketPath)
	require.NoError(t, listener.Start())
	t.Cleanup(func() { listener.Stop() })
	time.Sleep(50 * time.Millisecond)

	conn := connectToListener(t, socketPath)

	// Send ExecutableBlock with multiple txs (simulating Rust flattening)
	data := &pb.ExecutableBlock{
		GlobalExecIndex:   10,
		CommitIndex:       10,
		Epoch:             2,
		CommitTimestampMs: uint64(time.Now().UnixMilli()),
		LeaderAuthorIndex: 1,
		LeaderAddress:     []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, 0x11, 0x12, 0x13, 0x14},
		Transactions: []*pb.TransactionExe{
			{Digest: []byte("tx-from-validator-0")},
			{Digest: []byte("tx-from-validator-0-b")},
			{Digest: []byte("tx-from-validator-1")},
			{Digest: []byte("tx-from-validator-2")},
			{Digest: []byte("tx-from-validator-2-b")},
		},
	}

	require.NoError(t, sendExecutableBlock(conn, data))

	select {
	case received := <-listener.DataChannel():
		require.NotNil(t, received)
		assert.Equal(t, uint64(10), received.GetGlobalExecIndex())
		assert.Equal(t, uint64(2), received.GetEpoch())
		require.Len(t, received.Transactions, 5, "should receive all 5 txs")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for multi-tx ExecutableBlock")
	}
}

func TestExecutableBlock_SequentialDelivery(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "listener.sock")

	listener := NewListener(socketPath)
	require.NoError(t, listener.Start())
	t.Cleanup(func() { listener.Stop() })
	time.Sleep(50 * time.Millisecond)

	conn := connectToListener(t, socketPath)

	// Send 10 sequential ExecutableBlock messages
	const numMessages = 10
	for i := uint64(1); i <= numMessages; i++ {
		data := makeExecutableBlock(i, 0, [][]byte{[]byte("tx-data")})
		require.NoError(t, sendExecutableBlock(conn, data), "send gei=%d", i)
	}

	// Verify all received in order
	for i := uint64(1); i <= numMessages; i++ {
		select {
		case received := <-listener.DataChannel():
			require.NotNil(t, received)
			assert.Equal(t, i, received.GetGlobalExecIndex(),
				"expected sequential delivery: gei=%d", i)
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for gei=%d", i)
		}
	}
}

func TestExecutableBlock_ConsensusTimestampPreserved(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "listener.sock")

	listener := NewListener(socketPath)
	require.NoError(t, listener.Start())
	t.Cleanup(func() { listener.Stop() })
	time.Sleep(50 * time.Millisecond)

	conn := connectToListener(t, socketPath)

	// Send with specific consensus timestamp
	expectedTs := uint64(1700000000123)
	data := makeExecutableBlock(1, 0, [][]byte{[]byte("tx")}, func(d *pb.ExecutableBlock) {
		d.CommitTimestampMs = expectedTs
	})
	require.NoError(t, sendExecutableBlock(conn, data))

	select {
	case received := <-listener.DataChannel():
		assert.Equal(t, expectedTs, received.GetCommitTimestampMs(),
			"consensus timestamp must be preserved exactly")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestExecutableBlock_LeaderAddressPreserved(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "listener.sock")

	listener := NewListener(socketPath)
	require.NoError(t, listener.Start())
	t.Cleanup(func() { listener.Stop() })
	time.Sleep(50 * time.Millisecond)

	conn := connectToListener(t, socketPath)

	leaderAddr := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	data := makeExecutableBlock(1, 0, [][]byte{[]byte("tx")}, func(d *pb.ExecutableBlock) {
		d.LeaderAddress = leaderAddr
		d.LeaderAuthorIndex = 3
	})
	require.NoError(t, sendExecutableBlock(conn, data))

	select {
	case received := <-listener.DataChannel():
		assert.Equal(t, leaderAddr, received.GetLeaderAddress(),
			"leader address must be preserved byte-for-byte")
		assert.Equal(t, uint32(3), received.GetLeaderAuthorIndex(),
			"leader author index must be preserved")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

// ============================================================================
// Test Group 2: Fork-Safety Ordering Verification
// These tests verify the deterministic ordering properties that prevent forks.
// ============================================================================

func TestForkSafety_TransactionSortDeterminism(t *testing.T) {
	// Verify that Go's transaction sort-by-hash produces deterministic ordering
	// regardless of the input order. This is the key fork-safety property.

	// Simulate transaction digests with known hashes
	txDigests := [][]byte{
		{0xFF, 0x00, 0x01}, // "high" hash
		{0x00, 0x00, 0x01}, // "low" hash
		{0xAA, 0x00, 0x01}, // "mid" hash
		{0x55, 0x00, 0x01}, // "mid-low" hash
	}

	// Sort by digest bytes (simulating Go's sort by tx hash)
	sorted := make([][]byte, len(txDigests))
	copy(sorted, txDigests)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})

	// Verify deterministic ordering
	assert.Equal(t, []byte{0x00, 0x00, 0x01}, sorted[0], "lowest hash first")
	assert.Equal(t, []byte{0x55, 0x00, 0x01}, sorted[1], "second lowest")
	assert.Equal(t, []byte{0xAA, 0x00, 0x01}, sorted[2], "third")
	assert.Equal(t, []byte{0xFF, 0x00, 0x01}, sorted[3], "highest hash last")

	// Verify reverse input produces same output
	reversed := make([][]byte, len(txDigests))
	for i, d := range txDigests {
		reversed[len(txDigests)-1-i] = d
	}
	sort.Slice(reversed, func(i, j int) bool {
		a, b := reversed[i], reversed[j]
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
	assert.Equal(t, sorted, reversed, "sorting must be deterministic regardless of input order")
}

func TestForkSafety_TransactionDedup(t *testing.T) {
	// Verify that duplicate transactions (same digest) are correctly deduplicated.
	// In the real flow: same TX appears in multiple validator blocks within a sub-DAG.

	allDigests := [][]byte{
		{0x01, 0x02, 0x03}, // unique
		{0x04, 0x05, 0x06}, // duplicate
		{0x04, 0x05, 0x06}, // duplicate
		{0x07, 0x08, 0x09}, // unique
		{0x01, 0x02, 0x03}, // duplicate of first
	}

	// Dedup by content (simulating Go's seen map)
	type digestKey = string
	seen := make(map[digestKey]bool)
	var unique [][]byte
	for _, d := range allDigests {
		key := string(d)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, d)
		}
	}

	assert.Len(t, unique, 3, "should have 3 unique transactions after dedup")
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, unique[0])
	assert.Equal(t, []byte{0x04, 0x05, 0x06}, unique[1])
	assert.Equal(t, []byte{0x07, 0x08, 0x09}, unique[2])
}

func TestForkSafety_GlobalExecIndexOrdering(t *testing.T) {
	// Verify the ordering logic: sequential blocks are accepted,
	// future blocks should be buffered, old blocks should be skipped.

	type orderTestCase struct {
		name           string
		gei            uint64
		nextExpected   uint64
		expectedAction string // "process", "buffer", "skip"
	}

	cases := []orderTestCase{
		{"sequential block", 5, 5, "process"},
		{"future block (gap=1)", 7, 6, "buffer"},
		{"future block (gap=10)", 15, 5, "buffer"},
		{"old/duplicate block", 3, 5, "skip"},
		{"duplicate of expected-1", 4, 5, "skip"},
		{"first block", 1, 1, "process"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var action string
			if tc.gei == tc.nextExpected {
				action = "process"
			} else if tc.gei > tc.nextExpected {
				action = "buffer"
			} else {
				action = "skip"
			}
			assert.Equal(t, tc.expectedAction, action,
				"gei=%d, nextExpected=%d should %s", tc.gei, tc.nextExpected, tc.expectedAction)
		})
	}
}

func TestForkSafety_PendingBlockDrain(t *testing.T) {
	// Verify that buffered (pending) blocks are processed in order
	// when the gap block arrives.

	pending := map[uint64]*pb.ExecutableBlock{
		3: makeExecutableBlock(3, 0, [][]byte{[]byte("tx3")}),
		4: makeExecutableBlock(4, 0, [][]byte{[]byte("tx4")}),
		5: makeExecutableBlock(5, 0, [][]byte{[]byte("tx5")}),
	}

	// Simulate: nextExpected=3, process sequentially
	nextExpected := uint64(3)
	var processedOrder []uint64

	for {
		data, exists := pending[nextExpected]
		if !exists {
			break
		}
		processedOrder = append(processedOrder, data.GetGlobalExecIndex())
		delete(pending, nextExpected)
		nextExpected++
	}

	assert.Equal(t, []uint64{3, 4, 5}, processedOrder, "pending blocks must drain in order")
	assert.Empty(t, pending, "all pending blocks should be consumed")
	assert.Equal(t, uint64(6), nextExpected, "nextExpected should advance past all drained blocks")
}

// ============================================================================
// Test Group 3: Listener Connection Resilience
// These tests verify the Listener handles connection lifecycle correctly.
// ============================================================================

func TestListener_MultipleConnections(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "listener.sock")

	listener := NewListener(socketPath)
	require.NoError(t, listener.Start())
	t.Cleanup(func() { listener.Stop() })
	time.Sleep(50 * time.Millisecond)

	// Two simultaneous Rust connections (simulating Rust restart/reconnect)
	conn1 := connectToListener(t, socketPath)
	conn2 := connectToListener(t, socketPath)

	// Send from conn1
	d1 := makeExecutableBlock(1, 0, [][]byte{[]byte("from-conn1")})
	require.NoError(t, sendExecutableBlock(conn1, d1))

	// Send from conn2
	d2 := makeExecutableBlock(2, 0, [][]byte{[]byte("from-conn2")})
	require.NoError(t, sendExecutableBlock(conn2, d2))

	// Both should arrive on the data channel
	received := map[uint64]bool{}
	for i := 0; i < 2; i++ {
		select {
		case data := <-listener.DataChannel():
			received[data.GetGlobalExecIndex()] = true
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for data from multiple connections")
		}
	}
	assert.True(t, received[1], "should receive gei=1 from conn1")
	assert.True(t, received[2], "should receive gei=2 from conn2")
}

func TestListener_ReconnectAfterDisconnect(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "listener.sock")

	listener := NewListener(socketPath)
	require.NoError(t, listener.Start())
	t.Cleanup(func() { listener.Stop() })
	time.Sleep(50 * time.Millisecond)

	// First connection — send and disconnect
	conn1 := connectToListener(t, socketPath)
	d1 := makeExecutableBlock(1, 0, [][]byte{[]byte("tx1")})
	require.NoError(t, sendExecutableBlock(conn1, d1))
	conn1.Close()

	// Receive from first connection
	select {
	case data := <-listener.DataChannel():
		assert.Equal(t, uint64(1), data.GetGlobalExecIndex())
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}

	// Brief pause for server to detect disconnection
	time.Sleep(100 * time.Millisecond)

	// Reconnect — second connection should work
	sc := NewSocketConfig(socketPath)
	conn2, err := sc.Dial()
	require.NoError(t, err)
	defer conn2.Close()

	d2 := makeExecutableBlock(2, 0, [][]byte{[]byte("tx2")})
	require.NoError(t, sendExecutableBlock(conn2, d2))

	select {
	case data := <-listener.DataChannel():
		assert.Equal(t, uint64(2), data.GetGlobalExecIndex())
	case <-time.After(3 * time.Second):
		t.Fatal("timeout after reconnect")
	}
}

// ============================================================================
// Test Group 4: UDS Handler Extended Coverage (via SocketExecutor)
// ============================================================================

func TestHandler_EpochTimestampAfterMultipleAdvances(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	// Advance through 3 epochs with known timestamps
	timestamps := map[uint64]uint64{
		1: 1700000000000,
		2: 1700000060000,
		3: 1700000120000,
	}

	for epoch := uint64(1); epoch <= 3; epoch++ {
		_, err := client.advanceEpoch(epoch, timestamps[epoch], epoch*100)
		require.NoError(t, err)
	}

	// Query each timestamp — verify all persisted correctly
	for epoch := uint64(1); epoch <= 3; epoch++ {
		resp, err := client.getEpochStartTimestamp(epoch)
		require.NoError(t, err)
		tsResp := resp.GetGetEpochStartTimestampResponse()
		require.NotNil(t, tsResp, "epoch %d should have timestamp response", epoch)
		assert.Equal(t, timestamps[epoch], tsResp.GetTimestampMs(),
			"epoch %d timestamp mismatch", epoch)
	}
}

func TestHandler_GetLastBlockNumber_FreshState(t *testing.T) {
	env := setupTestEnv(t)
	client := env.newClient()

	// With nil storage, getLastBlockNumber should return 0 or error gracefully
	resp, err := client.getLastBlockNumber()
	require.NoError(t, err, "transport should succeed")

	blockResp := resp.GetLastBlockNumberResponse()
	if blockResp == nil {
		// Expected with nil storage
		errPayload := resp.GetError()
		assert.NotEmpty(t, errPayload, "should get error with nil storage")
		t.Logf("Got expected error: %s", errPayload)
	} else {
		assert.Equal(t, uint64(0), blockResp.GetLastBlockNumber(),
			"fresh state should have block 0")
	}
}

func TestHandler_ConcurrentQueriesWhileAdvancingEpoch(t *testing.T) {
	env := setupTestEnv(t)

	// Writer goroutine: advance epochs
	done := make(chan struct{})
	go func() {
		defer close(done)
		writer := env.newClient()
		for epoch := uint64(1); epoch <= 5; epoch++ {
			_, err := writer.advanceEpoch(epoch, 1700000000000+epoch*1000, epoch*100)
			if err != nil {
				t.Logf("writer error at epoch %d: %v", epoch, err)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	// Reader goroutine: query epoch concurrently
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		reader := env.newClient()
		for i := 0; i < 20; i++ {
			resp, err := reader.getCurrentEpoch()
			if err != nil {
				t.Logf("reader error: %v", err)
				return
			}
			epoch := resp.GetGetCurrentEpochResponse().GetEpoch()
			_ = epoch // Just verify no crash/deadlock
			time.Sleep(5 * time.Millisecond)
		}
	}()

	<-done
	<-readerDone
}

// ============================================================================
// Test: Full Listener + SocketExecutor Integration
// Simulates the complete flow: Rust sends ExecutableBlock via Listener
// AND queries Go state via SocketExecutor simultaneously.
// ============================================================================

func TestFullIntegration_ListenerAndSocketExecutorConcurrent(t *testing.T) {
	tmpDir := t.TempDir()
	listenerPath := filepath.Join(tmpDir, "listener.sock")
	executorPath := filepath.Join(tmpDir, "executor.sock")

	// Start Listener (for data from Rust)
	listener := NewListener(listenerPath)
	require.NoError(t, listener.Start())
	t.Cleanup(func() { listener.Stop() })

	// Start SocketExecutor (for queries from Rust)
	cs := blockchain.NewTestChainState()
	se := NewSocketExecutor(executorPath, nil, cs, "")
	require.NoError(t, se.Start())
	t.Cleanup(func() { _ = se.Stop() })

	time.Sleep(50 * time.Millisecond)

	// Simulate Rust: send data via Listener
	conn := connectToListener(t, listenerPath)
	data := makeExecutableBlock(1, 0, [][]byte{[]byte("integration-tx")})
	require.NoError(t, sendExecutableBlock(conn, data))

	// Simulate Rust: query epoch via SocketExecutor
	queryConn, err := NewSocketConfig(executorPath).Dial()
	require.NoError(t, err)
	defer queryConn.Close()

	queryClient := &testRustClient{
		conn:   queryConn,
		reader: bufio.NewReader(queryConn),
		writer: bufio.NewWriter(queryConn),
	}

	// Advance epoch
	resp, err := queryClient.advanceEpoch(1, 1700000000000, 0)
	require.NoError(t, err)
	require.NotNil(t, resp.GetAdvanceEpochResponse())

	// Verify data received
	select {
	case received := <-listener.DataChannel():
		assert.Equal(t, uint64(1), received.GetGlobalExecIndex())
		require.Len(t, received.Transactions, 1)
		assert.Equal(t, []byte("integration-tx"), received.Transactions[0].Digest)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for listener data")
	}

	// Verify epoch state
	resp, err = queryClient.getCurrentEpoch()
	require.NoError(t, err)
	assert.Equal(t, uint64(1), resp.GetGetCurrentEpochResponse().GetEpoch())
}
