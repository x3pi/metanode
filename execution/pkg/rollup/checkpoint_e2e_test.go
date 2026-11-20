package rollup

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eCheckpointSource simulates the shard chain block source for E2E tests.
type e2eCheckpointSource struct {
	mu         sync.Mutex
	isLeader   bool
	attested   uint64
	lastPruned uint64
	blocks     map[uint64]common.Hash
	roots      map[uint64]common.Hash
}

func newE2ECheckpointSource() *e2eCheckpointSource {
	return &e2eCheckpointSource{
		isLeader: true,
		blocks:   make(map[uint64]common.Hash),
		roots:    make(map[uint64]common.Hash),
	}
}

func (s *e2eCheckpointSource) IsLeader() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isLeader
}

func (s *e2eCheckpointSource) SetLeader(leader bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isLeader = leader
}

func (s *e2eCheckpointSource) AttestedBlock() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attested
}

func (s *e2eCheckpointSource) ProduceBlock(n uint64) (common.Hash, common.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := common.HexToHash(fmt.Sprintf("0xblock_hash_%08d", n))
	r := common.HexToHash(fmt.Sprintf("0xstate_root_%08d", n))
	s.blocks[n] = h
	s.roots[n] = r
	if n > s.attested {
		s.attested = n
	}
	return h, r
}

func (s *e2eCheckpointSource) BlockAt(n uint64) (common.Hash, common.Hash, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, okH := s.blocks[n]
	r, okR := s.roots[n]
	if !okH || !okR {
		return common.Hash{}, common.Hash{}, false
	}
	return h, r, true
}

func (s *e2eCheckpointSource) LastPrunedBlock() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPruned
}

// e2eParentClient wraps ParentChainClientAdapter with simulation controls (outages, delays).
type e2eParentClient struct {
	*ParentChainClientAdapter
	outage           atomic.Bool
	rejectNextSubmit atomic.Bool
	sendCount        atomic.Int64
}

func newE2EParentClient(chain *InMemoryParentChain, chainID uint64) *e2eParentClient {
	return &e2eParentClient{
		ParentChainClientAdapter: &ParentChainClientAdapter{
			chain:   chain,
			chainID: chainID,
		},
	}
}

func (c *e2eParentClient) GetLatestBlockCheckpoint(clusterPubKey cm.PublicKey) (uint64, common.Hash, common.Hash, bool, error) {
	if c.outage.Load() {
		return 0, common.Hash{}, common.Hash{}, false, errors.New("parent chain network outage")
	}
	return c.ParentChainClientAdapter.GetLatestBlockCheckpoint(clusterPubKey)
}

func (c *e2eParentClient) GetBlockCheckpoint(clusterPubKey cm.PublicKey, blockNumber uint64) (common.Hash, common.Hash, bool, error) {
	if c.outage.Load() {
		return common.Hash{}, common.Hash{}, false, errors.New("parent chain network outage")
	}
	return c.ParentChainClientAdapter.GetBlockCheckpoint(clusterPubKey, blockNumber)
}

func (c *e2eParentClient) SendSubmitBlockCheckpoints(clusterPubKey cm.PublicKey, checkpoints []*pb.BlockCheckpoint, cert cm.Sign) (common.Hash, error) {
	if c.outage.Load() {
		return common.Hash{}, errors.New("parent chain network outage")
	}
	c.sendCount.Add(1)
	if c.rejectNextSubmit.Load() {
		c.rejectNextSubmit.Store(false)
		txHash := crypto.Keccak256Hash([]byte("rejected_e2e_tx"))
		c.chain.mu.Lock()
		c.chain.receipts[txHash] = &parentchain.Receipt{
			TxHash:    txHash,
			Status:    0,
			ErrorCode: 230,
		}
		c.chain.mu.Unlock()
		return txHash, nil
	}
	return c.ParentChainClientAdapter.SendSubmitBlockCheckpoints(clusterPubKey, checkpoints, cert)
}

func registerClusterOnParent(chain *InMemoryParentChain, pub cm.PublicKey, clusterID uint64) {
	clusterHash := crypto.Keccak256Hash(pub[:])
	_ = chain.store.SetChainRegistry(clusterHash, parentchain.ChainRegistryEntry{
		FloatIdentityKey:     pub,
		ClusterIDDescriptive: clusterID,
		ChainIDDescriptive:   clusterID,
		Authorized:           true,
	})
}

// -----------------------------------------------------------------------------------------
// Test 1: Full Normal Lifecycle with background goroutine and event-driven Wakeup
// -----------------------------------------------------------------------------------------
func TestE2E_Checkpoint_NormalLifecycle(t *testing.T) {
	kp := bls.GenerateKeyPair()
	pub := kp.PublicKey()
	clusterHash := crypto.Keccak256Hash(pub[:])
	parentChain := NewInMemoryParentChain()
	registerClusterOnParent(parentChain, pub, 101)
	client := newE2EParentClient(parentChain, 101)

	src := newE2ECheckpointSource()

	// Create CheckpointWorker with short tick for fast E2E
	worker := NewCheckpointWorker(client, src, kp, 100, 20*time.Millisecond)
	worker.Start()
	defer worker.Stop()

	// Produce block 100 on shard
	h100, r100 := src.ProduceBlock(100)
	worker.Wakeup()

	require.Eventually(t, func() bool {
		num, h, r, found, err := parentChain.store.GetLatestBlockCheckpoint(clusterHash)
		return err == nil && found && num == 100 && h == h100 && r == r100
	}, 2*time.Second, 10*time.Millisecond, "Parent chain must commit checkpoint 100")

	// Produce block 200 and 300
	h200, r200 := src.ProduceBlock(200)
	h300, r300 := src.ProduceBlock(300)
	worker.Wakeup()

	require.Eventually(t, func() bool {
		num, h, r, found, err := parentChain.store.GetLatestBlockCheckpoint(clusterHash)
		return err == nil && found && num == 300 && h == h300 && r == r300
	}, 2*time.Second, 10*time.Millisecond, "Parent chain must advance to checkpoint 300")

	// Verify all checkpoints exist in parent store individually
	hCheck100, rCheck100, f100, err := parentChain.store.GetBlockCheckpoint(clusterHash, 100)
	require.NoError(t, err)
	assert.True(t, f100)
	assert.Equal(t, h100, hCheck100)
	assert.Equal(t, r100, rCheck100)

	hCheck200, rCheck200, f200, err := parentChain.store.GetBlockCheckpoint(clusterHash, 200)
	require.NoError(t, err)
	assert.True(t, f200)
	assert.Equal(t, h200, hCheck200)
	assert.Equal(t, r200, rCheck200)

	hCheck300, rCheck300, f300, err := parentChain.store.GetBlockCheckpoint(clusterHash, 300)
	require.NoError(t, err)
	assert.True(t, f300)
	assert.Equal(t, h300, hCheck300)
	assert.Equal(t, r300, rCheck300)
}

// -----------------------------------------------------------------------------------------
// Test 2: Rapid Block Production & Batch Catch-Up E2E
// -----------------------------------------------------------------------------------------
func TestE2E_Checkpoint_RapidCatchupBatching(t *testing.T) {
	kp := bls.GenerateKeyPair()
	pub := kp.PublicKey()
	clusterHash := crypto.Keccak256Hash(pub[:])
	parentChain := NewInMemoryParentChain()
	registerClusterOnParent(parentChain, pub, 101)
	client := newE2EParentClient(parentChain, 101)

	src := newE2ECheckpointSource()

	// 1. Shard starts at block 50 (initial bootstrap)
	src.ProduceBlock(50)

	worker := NewCheckpointWorker(client, src, kp, 50, 20*time.Millisecond)
	worker.Start()
	defer worker.Stop()

	// Wait for bootstrap checkpoint 50 to land on parent
	require.Eventually(t, func() bool {
		num, _, _, found, err := parentChain.store.GetLatestBlockCheckpoint(clusterHash)
		return err == nil && found && num == 50
	}, 2*time.Second, 10*time.Millisecond)

	// 2. Shard rapidly produces 19 more checkpoints up to 1000 while parent was idle
	for i := uint64(100); i <= 1000; i += 50 {
		src.ProduceBlock(i)
	}
	worker.Wakeup()

	// Worker should catch up and submit all 19 checkpoints [100..1000] in ONE batch
	require.Eventually(t, func() bool {
		num, _, _, found, err := parentChain.store.GetLatestBlockCheckpoint(clusterHash)
		return err == nil && found && num == 1000
	}, 2*time.Second, 10*time.Millisecond, "Parent chain must catch up to block 1000 in batch")

	// Ensure each individual checkpoint 50..1000 is readable on Parent Chain
	for i := uint64(50); i <= 1000; i += 50 {
		expH, expR, _ := src.BlockAt(i)
		actH, actR, found, err := parentChain.store.GetBlockCheckpoint(clusterHash, i)
		require.NoError(t, err)
		assert.True(t, found, "checkpoint %d must exist on parent", i)
		assert.Equal(t, expH, actH)
		assert.Equal(t, expR, actR)
	}
}

// -----------------------------------------------------------------------------------------
// Test 3: Leader Failover E2E (Node 1 -> Node 2 handover)
// -----------------------------------------------------------------------------------------
func TestE2E_Checkpoint_LeaderFailover(t *testing.T) {
	kp := bls.GenerateKeyPair()
	pub := kp.PublicKey()
	clusterHash := crypto.Keccak256Hash(pub[:])
	parentChain := NewInMemoryParentChain()
	registerClusterOnParent(parentChain, pub, 101)
	client := newE2EParentClient(parentChain, 101)

	// Shared source or replicated block state
	srcNode1 := newE2ECheckpointSource()
	srcNode2 := newE2ECheckpointSource()

	// Node 1 is initially leader
	srcNode1.SetLeader(true)
	srcNode2.SetLeader(false)

	worker1 := NewCheckpointWorker(client, srcNode1, kp, 100, 20*time.Millisecond)
	worker2 := NewCheckpointWorker(client, srcNode2, kp, 100, 20*time.Millisecond)

	worker1.Start()
	defer worker1.Stop()
	worker2.Start()
	defer worker2.Stop()

	// Step 1: Node 1 bootstraps block 100
	h100, r100 := srcNode1.ProduceBlock(100)
	srcNode2.blocks[100] = h100
	srcNode2.roots[100] = r100
	srcNode2.attested = 100
	worker1.Wakeup()

	require.Eventually(t, func() bool {
		num, _, _, found, _ := parentChain.store.GetLatestBlockCheckpoint(clusterHash)
		return found && num == 100
	}, 2*time.Second, 10*time.Millisecond)

	// Step 2: Node 1 advances to 300
	for i := uint64(200); i <= 300; i += 100 {
		h, r := srcNode1.ProduceBlock(i)
		srcNode2.blocks[i] = h
		srcNode2.roots[i] = r
		srcNode2.attested = i
	}
	worker1.Wakeup()

	// Wait for Node 1 to anchor up to 300
	require.Eventually(t, func() bool {
		num, _, _, found, _ := parentChain.store.GetLatestBlockCheckpoint(clusterHash)
		return found && num == 300
	}, 2*time.Second, 10*time.Millisecond)

	// FAILOVER: Node 1 crashes / steps down, Node 2 elected leader!
	srcNode1.SetLeader(false)
	srcNode2.SetLeader(true)

	// Node 2 continues producing blocks 400..600
	for i := uint64(400); i <= 600; i += 100 {
		srcNode2.ProduceBlock(i)
	}
	worker2.Wakeup()

	// Worker 2 must smoothly pick up from 300 and anchor 400..600!
	require.Eventually(t, func() bool {
		num, _, _, found, _ := parentChain.store.GetLatestBlockCheckpoint(clusterHash)
		return found && num == 600
	}, 2*time.Second, 10*time.Millisecond, "Node 2 must seamlessly continue anchoring to 600")

	// Verify all 6 checkpoints 100..600 are contiguous on parent
	for i := uint64(100); i <= 600; i += 100 {
		_, _, found, err := parentChain.store.GetBlockCheckpoint(clusterHash, i)
		require.NoError(t, err)
		assert.True(t, found, "checkpoint %d must exist without gap after failover", i)
	}
}

// -----------------------------------------------------------------------------------------
// Test 4: Fork / Devnet Reset Detection E2E (Zero-Fork Protection)
// -----------------------------------------------------------------------------------------
func TestE2E_Checkpoint_ForkDetection_ZeroForkRule(t *testing.T) {
	kp := bls.GenerateKeyPair()
	pub := kp.PublicKey()
	clusterHash := crypto.Keccak256Hash(pub[:])
	parentChain := NewInMemoryParentChain()
	registerClusterOnParent(parentChain, pub, 101)
	client := newE2EParentClient(parentChain, 101)

	// Pre-populate Parent Chain with checkpoint 200 from an old chain / devnet
	oldHash := common.HexToHash("0xOLD_CHAIN_BLOCK_HASH")
	oldRoot := common.HexToHash("0xOLD_CHAIN_STATE_ROOT")
	require.NoError(t, parentChain.store.SetBlockCheckpoint(clusterHash, 200, oldHash, oldRoot))
	require.NoError(t, parentChain.store.SetLatestBlockCheckpoint(clusterHash, 200, oldHash, oldRoot))

	// Local shard has a freshly generated chain where block 200 has a DIFFERENT hash
	src := newE2ECheckpointSource()
	src.ProduceBlock(100)
	src.ProduceBlock(200) // hash will be 0xblock_hash_00000200 != oldHash
	src.ProduceBlock(300)
	src.ProduceBlock(400)

	var recordedAlerts []string
	var alertMu sync.Mutex

	worker := NewCheckpointWorker(client, src, kp, 100, 20*time.Millisecond)
	worker.SetAlertSink(func(msg string) {
		alertMu.Lock()
		defer alertMu.Unlock()
		recordedAlerts = append(recordedAlerts, msg)
	})
	worker.SetThresholds(1, 2)

	worker.Start()
	defer worker.Stop()

	// Wait 150ms for worker loop
	time.Sleep(150 * time.Millisecond)

	// Parent Chain MUST NOT be corrupted: latest must remain 200 with oldHash,
	// and blocks 300, 400 MUST NOT be written!
	num, h, _, found, err := parentChain.store.GetLatestBlockCheckpoint(clusterHash)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, uint64(200), num)
	assert.Equal(t, oldHash, h)

	_, _, f300, _ := parentChain.store.GetBlockCheckpoint(clusterHash, 300)
	assert.False(t, f300, "Block 300 must not be written when fork is detected")

	alertMu.Lock()
	assert.NotEmpty(t, recordedAlerts)
	assert.Contains(t, recordedAlerts[0], "PHÁT HIỆN LỆCH HASH / FORK")
	alertMu.Unlock()
}

// -----------------------------------------------------------------------------------------
// Test 5: Parent Outage, Anti-Spam Telegram Alerts, and Automatic Recovery E2E
// -----------------------------------------------------------------------------------------
func TestE2E_Checkpoint_ParentOutage_AntiSpam_And_Recovery(t *testing.T) {
	kp := bls.GenerateKeyPair()
	pub := kp.PublicKey()
	clusterHash := crypto.Keccak256Hash(pub[:])
	parentChain := NewInMemoryParentChain()
	registerClusterOnParent(parentChain, pub, 101)
	client := newE2EParentClient(parentChain, 101)

	src := newE2ECheckpointSource()
	src.ProduceBlock(100)
	src.ProduceBlock(200)

	var alerts []string
	var alertMu sync.Mutex

	worker := NewCheckpointWorker(client, src, kp, 100, 15*time.Millisecond)
	worker.SetAlertSink(func(msg string) {
		alertMu.Lock()
		defer alertMu.Unlock()
		alerts = append(alerts, msg)
	})
	// Low thresholds for test speed: warn at 2, dead at 4
	worker.SetThresholds(2, 4)

	// 1. Simulate Parent Chain OUTAGE (network down / stopped)
	client.outage.Store(true)

	worker.Start()
	defer worker.Stop()

	// Wait for worker to experience outage and trigger:
	// Alert 1 (Warn at streak 2)
	// Alert 2 (Dead at streak 4: "final notice & silence spam")
	require.Eventually(t, func() bool {
		alertMu.Lock()
		defer alertMu.Unlock()
		return len(alerts) >= 2
	}, 2*time.Second, 10*time.Millisecond)

	alertMu.Lock()
	assert.Contains(t, alerts[0], "Mất kết nối")
	assert.Contains(t, alerts[1], "Chain ngừng hoạt động")
	assert.Contains(t, alerts[1], "Dừng thông báo tránh spam")
	alertCountDuringOutage := len(alerts)
	alertMu.Unlock()

	// Let the worker run for another 150ms (~10 more ticks) while parent is dead.
	// Verifies ANTI-SPAM: NO NEW ALERTS MUST BE SENT!
	time.Sleep(150 * time.Millisecond)

	alertMu.Lock()
	assert.Equal(t, alertCountDuringOutage, len(alerts), "Must NOT spam any Telegram alerts during ongoing outage")
	alertMu.Unlock()

	// 2. Simulate Parent Chain RECOVERY (revived!)
	client.outage.Store(false)

	// Shard produces block 300
	src.ProduceBlock(300)
	worker.Wakeup()

	// Worker must:
	// a. Send exactly 1 RECOVERY alert
	// b. Anchor all checkpoints up to 300 to Parent Chain
	require.Eventually(t, func() bool {
		num, _, _, found, _ := parentChain.store.GetLatestBlockCheckpoint(clusterHash)
		return found && num == 300
	}, 2*time.Second, 10*time.Millisecond, "Must anchor up to 300 after parent recovery")

	alertMu.Lock()
	require.Len(t, alerts, 3, "Must have exactly 3 alerts: warn -> dead -> recovered")
	assert.Contains(t, alerts[2], "Chain đã phục hồi")
	assert.Contains(t, alerts[2], "Tiếp tục gửi checkpoint")
	alertMu.Unlock()
}

// -----------------------------------------------------------------------------------------
// Test 6: Receipt Status=0 (Rejected) E2E: Worker halts retry to prevent pipeline nonce resets
// -----------------------------------------------------------------------------------------
func TestE2E_Checkpoint_ConflictRejectedReceipt_NoNonceThrashing(t *testing.T) {
	kp := bls.GenerateKeyPair()
	pub := kp.PublicKey()
	parentChain := NewInMemoryParentChain()
	registerClusterOnParent(parentChain, pub, 101)
	client := newE2EParentClient(parentChain, 101)

	src := newE2ECheckpointSource()
	src.ProduceBlock(100)

	worker := NewCheckpointWorker(client, src, kp, 100, 20*time.Millisecond)
	worker.SetRejectBackoffRounds(10) // 10 rounds backoff

	client.rejectNextSubmit.Store(true)

	// Round 1: worker submits block 100 -> gets rejected_e2e_tx
	worker.runOnce()
	require.NotNil(t, worker.inflight, "worker must have in-flight tx after submit")
	assert.Equal(t, crypto.Keccak256Hash([]byte("rejected_e2e_tx")), worker.inflight.txHash)

	// Round 2: worker checks receipt, sees Status: 0 -> backoff engaged, inflight cleared!
	worker.runOnce()
	assert.Nil(t, worker.inflight, "in-flight state must be cleared on status 0 to avoid thrashing nonce pipeline")
	assert.Greater(t, worker.backoffUntil, worker.rounds)

	// Rounds 3 & 4: worker must be in backoff, NOT submitting again
	worker.runOnce()
	worker.runOnce()

	assert.Equal(t, int64(1), client.sendCount.Load(), "worker must NOT re-submit while in rejection backoff")
}
