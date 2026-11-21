package rollup

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSource implements CheckpointSource for tests.
type fakeSource struct {
	mu          sync.Mutex
	isLeader    bool
	attested    uint64
	lastPruned  uint64
	blocks      map[uint64]common.Hash
	stateRoots  map[uint64]common.Hash
	blockErrors map[uint64]bool
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		isLeader:    true,
		blocks:      make(map[uint64]common.Hash),
		stateRoots:  make(map[uint64]common.Hash),
		blockErrors: make(map[uint64]bool),
	}
}

func (s *fakeSource) IsLeader() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isLeader
}

func (s *fakeSource) AttestedBlock() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attested
}

func (s *fakeSource) BlockAt(n uint64) (common.Hash, common.Hash, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blockErrors[n] {
		return common.Hash{}, common.Hash{}, false
	}
	h, okH := s.blocks[n]
	r, okR := s.stateRoots[n]
	if !okH || !okR {
		// generate deterministic hashes if not explicitly set
		h = common.HexToHash(fmt.Sprintf("0xhash%08d", n))
		r = common.HexToHash(fmt.Sprintf("0xroot%08d", n))
	}
	return h, r, true
}

func (s *fakeSource) LastPrunedBlock() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPruned
}

// fakeParentClient implements parentchain.Client for worker testing.
type fakeParentClient struct {
	parentchain.Client
	mu sync.Mutex

	checkpoints  map[uint64]*pb.BlockCheckpoint
	latestNumber uint64
	latestHash   common.Hash
	latestRoot   common.Hash
	hasLatest    bool

	submittedBatches [][]*pb.BlockCheckpoint
	submittedCerts   []cm.Sign
	receipts         map[common.Hash]*parentchain.Receipt

	queryCount    int
	sendCallCount int
	parentDown    bool
	submitDown    bool
	txDropCounter int
}

func newFakeParentClient() *fakeParentClient {
	return &fakeParentClient{
		checkpoints: make(map[uint64]*pb.BlockCheckpoint),
		receipts:    make(map[common.Hash]*parentchain.Receipt),
	}
}

func (p *fakeParentClient) GetLatestBlockCheckpoint(clusterPubKey cm.PublicKey) (uint64, common.Hash, common.Hash, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queryCount++
	if p.parentDown {
		return 0, common.Hash{}, common.Hash{}, false, errors.New("parent chain unavailable")
	}
	hash := p.latestHash
	if hash == (common.Hash{}) && p.latestNumber > 0 {
		hash = common.HexToHash(fmt.Sprintf("0xhash%08d", p.latestNumber))
	}
	root := p.latestRoot
	if root == (common.Hash{}) && p.latestNumber > 0 {
		root = common.HexToHash(fmt.Sprintf("0xroot%08d", p.latestNumber))
	}
	return p.latestNumber, hash, root, p.hasLatest, nil
}

func (p *fakeParentClient) GetReceipt(txHash common.Hash) (*parentchain.Receipt, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.receipts != nil {
		rcpt, ok := p.receipts[txHash]
		if ok {
			return rcpt, true, nil
		}
	}
	return nil, false, nil
}

func (p *fakeParentClient) SendSubmitBlockCheckpoints(clusterPubKey cm.PublicKey, checkpoints []*pb.BlockCheckpoint, cert cm.Sign) (common.Hash, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sendCallCount++
	if p.parentDown || p.submitDown {
		return common.Hash{}, errors.New("parent chain send failure")
	}
	if p.txDropCounter > 0 {
		p.txDropCounter--
		// Tx accepted by RPC but "dropped" in mempool -> return tx hash but don't apply state
		return common.HexToHash("0xdropped"), nil
	}

	// Apply checkpoints idempotently
	p.submittedBatches = append(p.submittedBatches, checkpoints)
	p.submittedCerts = append(p.submittedCerts, cert)

	for _, cp := range checkpoints {
		p.checkpoints[cp.BlockNumber] = cp
		if !p.hasLatest || cp.BlockNumber > p.latestNumber {
			p.latestNumber = cp.BlockNumber
			p.latestHash = common.BytesToHash(cp.BlockHash)
			p.latestRoot = common.BytesToHash(cp.StateRoot)
			p.hasLatest = true
		}
	}
	return common.HexToHash(fmt.Sprintf("0xtx%d", len(p.submittedBatches))), nil
}

func TestCheckpointWorker_NotLeader(t *testing.T) {
	bls.Init()
	kp := bls.GenerateKeyPair()
	src := newFakeSource()
	src.isLeader = false
	src.attested = 500
	client := newFakeParentClient()

	worker := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker.runOnce()

	assert.Equal(t, 0, len(client.submittedBatches), "follower must not submit checkpoints")
	assert.Equal(t, 0, client.queryCount, "follower must not query parent")
}

func TestCheckpointWorker_AttestedBelowInterval(t *testing.T) {
	kp := bls.GenerateKeyPair()
	src := newFakeSource()
	src.attested = 99 // < interval (100)
	client := newFakeParentClient()

	worker := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker.runOnce()

	assert.Equal(t, 0, len(client.submittedBatches), "attested below interval must not submit")
	assert.Equal(t, 0, client.queryCount)
}

func TestCheckpointWorker_Bootstrap(t *testing.T) {
	kp := bls.GenerateKeyPair()
	src := newFakeSource()
	src.attested = 350
	client := newFakeParentClient() // hasLatest = false

	worker := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker.runOnce()

	// target = floor(350/100)*100 = 300.
	// Since !found, bootstrap start = target (300).
	// batch = [300].
	require.Equal(t, 1, len(client.submittedBatches))
	require.Equal(t, 1, len(client.submittedBatches[0]))
	assert.Equal(t, uint64(300), client.submittedBatches[0][0].BlockNumber)
	assert.Equal(t, uint64(300), client.latestNumber)
}

func TestCheckpointWorker_FastLeaderAttestedCheck(t *testing.T) {
	// Leader has block 1000 locally, but attested is only 250 -> only anchors up to 200
	kp := bls.GenerateKeyPair()
	src := newFakeSource()
	src.attested = 250
	client := newFakeParentClient()

	worker := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker.runOnce()

	require.Equal(t, 1, len(client.submittedBatches))
	assert.Equal(t, uint64(200), client.submittedBatches[0][0].BlockNumber)
}

func TestCheckpointWorker_BatchCatchupWhenParentRevives(t *testing.T) {
	kp := bls.GenerateKeyPair()
	src := newFakeSource()
	src.attested = 800
	client := newFakeParentClient()
	// Parent already has checkpoint at 200
	client.hasLatest = true
	client.latestNumber = 200
	client.latestHash = common.HexToHash(fmt.Sprintf("0xhash%08d", 200))
	client.latestRoot = common.HexToHash(fmt.Sprintf("0xroot%08d", 200))

	worker := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker.runOnce()

	// Should send batch from 300 to 800: [300, 400, 500, 600, 700, 800] in ONE transaction!
	require.Equal(t, 1, len(client.submittedBatches))
	batch := client.submittedBatches[0]
	require.Equal(t, 6, len(batch))
	assert.Equal(t, uint64(300), batch[0].BlockNumber)
	assert.Equal(t, uint64(800), batch[5].BlockNumber)
	assert.Equal(t, uint64(800), client.latestNumber)
}

func TestCheckpointWorker_MaxBatchBounds(t *testing.T) {
	// 100 checkpoints behind (> 64) -> first batch must be capped at 64
	kp := bls.GenerateKeyPair()
	src := newFakeSource()
	src.attested = 10000 // 100 intervals of 100
	client := newFakeParentClient()
	client.hasLatest = true
	client.latestNumber = 0 // start at 100

	worker := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker.runOnce()

	require.Equal(t, 1, len(client.submittedBatches))
	assert.Equal(t, 64, len(client.submittedBatches[0]))
	assert.Equal(t, uint64(100), client.submittedBatches[0][0].BlockNumber)
	assert.Equal(t, uint64(6400), client.submittedBatches[0][63].BlockNumber)
}

func TestCheckpointWorker_InflightAndStallRetry(t *testing.T) {
	kp := bls.GenerateKeyPair()
	src := newFakeSource()
	src.attested = 300
	client := newFakeParentClient()
	client.hasLatest = true
	client.latestNumber = 100

	worker := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker.stallRounds = 4 // short stall for test

	// Round 1: tx is dropped in mempool (RPC accepted, but not applied)
	client.txDropCounter = 1
	worker.runOnce()

	assert.Equal(t, 0, len(client.submittedBatches), "tx was dropped, not applied")
	assert.NotNil(t, worker.inflight, "inflight should be set")
	assert.Equal(t, uint64(300), worker.inflight.last)

	// Rounds 2, 3, 4: waiting, inflight rounds increment, no new tx sent
	for r := 2; r <= 4; r++ {
		worker.runOnce()
		assert.Equal(t, 0, len(client.submittedBatches))
		assert.NotNil(t, worker.inflight)
	}

	// Round 5: stallRounds exceeded (rounds reached 4) -> inflight reset and retried!
	worker.runOnce()
	require.Equal(t, 1, len(client.submittedBatches), "stalled tx must be retried")
	assert.Equal(t, uint64(300), client.latestNumber)
}

func TestCheckpointWorker_IdleCacheSkipsParentRead(t *testing.T) {
	kp := bls.GenerateKeyPair()
	src := newFakeSource()
	src.attested = 200
	client := newFakeParentClient()
	client.hasLatest = true
	client.latestNumber = 200

	worker := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker.refreshRounds = 10

	// Round 1: queries parent, discovers confirmed = 200, nothing to send
	worker.runOnce()
	assert.Equal(t, 1, client.queryCount)
	assert.True(t, worker.hasConfirmed)
	assert.Equal(t, uint64(200), worker.confirmed)

	// Rounds 2 through 9: target is still 200 <= confirmed, no inflight -> NO query to parent!
	for i := 2; i <= 9; i++ {
		worker.runOnce()
	}
	assert.Equal(t, 1, client.queryCount, "idle worker must not query parent while target <= confirmed")

	// Round 10: refresh round triggers re-query
	worker.runOnce()
	assert.Equal(t, 2, client.queryCount, "refresh round must query parent")
}

func TestCheckpointWorker_PruningSkipSafe(t *testing.T) {
	kp := bls.GenerateKeyPair()
	src := newFakeSource()
	src.attested = 1000
	src.lastPruned = 550 // blocks 1..550 are pruned!
	client := newFakeParentClient()
	client.hasLatest = true
	client.latestNumber = 100 // parent last was 100

	worker := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker.runOnce()

	// Desired start was 200, but lastPruned is 550 -> skips to floor(550/100 + 1)*100 = 600
	require.Equal(t, 1, len(client.submittedBatches))
	batch := client.submittedBatches[0]
	assert.Equal(t, uint64(600), batch[0].BlockNumber)
	assert.Equal(t, uint64(1000), batch[len(batch)-1].BlockNumber)
}

func TestCheckpointWorker_LeaderChange(t *testing.T) {
	kp := bls.GenerateKeyPair()
	src := newFakeSource()
	src.attested = 400
	client := newFakeParentClient()
	client.hasLatest = true
	client.latestNumber = 200

	// Node 1 is leader: sends [300, 400]
	worker1 := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker1.runOnce()
	require.Equal(t, 1, len(client.submittedBatches))
	assert.Equal(t, uint64(400), client.latestNumber)

	// Node 1 steps down
	src.isLeader = false
	worker1.runOnce()
	assert.Nil(t, worker1.inflight)
	assert.False(t, worker1.hasConfirmed)

	// Node 2 becomes leader, new height 500
	src.isLeader = true
	src.attested = 500
	worker2 := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker2.runOnce()

	// Node 2 queries parent -> latest is 400 -> smoothly sends [500]!
	require.Equal(t, 2, len(client.submittedBatches))
	assert.Equal(t, uint64(500), client.latestNumber)
}

func TestCheckpointWorker_TelegramAntiSpamAndRecovery(t *testing.T) {
	kp := bls.GenerateKeyPair()

	src := newFakeSource()
	src.attested = 1000
	client := newFakeParentClient()

	// Simulate Parent Chain is DEAD / UNRESPONSIVE
	client.parentDown = true

	worker := NewCheckpointWorker(client, src, kp, 100, time.Second)

	var alerts []string
	var alertMu sync.Mutex
	worker.SetAlertSink(func(msg string) {
		alertMu.Lock()
		defer alertMu.Unlock()
		alerts = append(alerts, msg)
	})

	// Configure low thresholds for testing: warn at 3, dead at 6
	worker.SetThresholds(3, 6)

	// Rounds 1-2: failures occur, but below warnThreshold -> NO Telegram alerts
	worker.runOnce()
	worker.runOnce()
	alertMu.Lock()
	assert.Empty(t, alerts, "rounds 1-2 below threshold should not send Telegram alerts")
	alertMu.Unlock()

	// Round 3: reaches warnThreshold (3) -> Exactly 1 WARNING alert sent
	worker.runOnce()
	alertMu.Lock()
	require.Len(t, alerts, 1, "round 3 must trigger first warning alert")
	assert.Contains(t, alerts[0], "Mất kết nối")
	assert.Contains(t, alerts[0], "3 lần")
	alertMu.Unlock()

	// Rounds 4-5: between warn and dead threshold -> NO duplicate warning spam
	worker.runOnce()
	worker.runOnce()
	alertMu.Lock()
	assert.Len(t, alerts, 1, "rounds 4-5 must not spam warnings")
	alertMu.Unlock()

	// Round 6: reaches deadThreshold (6) -> Exactly 1 DEAD CHAIN final notice alert sent
	worker.runOnce()
	alertMu.Lock()
	require.Len(t, alerts, 2, "round 6 must trigger dead chain alert")
	assert.Contains(t, alerts[1], "Chain ngừng hoạt động")
	assert.Contains(t, alerts[1], "Dừng thông báo tránh spam")
	alertMu.Unlock()

	// Rounds 7-25: chain stays dead for 19 more rounds -> ZERO alerts sent (ANTI-SPAM)
	for i := 0; i < 19; i++ {
		worker.runOnce()
	}
	alertMu.Lock()
	assert.Len(t, alerts, 2, "rounds 7-25 must not spam any alerts during chain outage")
	alertMu.Unlock()

	// Now Parent Chain REVIVES / comes back up!
	client.parentDown = false

	// Next round: worker reconnects, submits checkpoints, and sends RECOVERY alert
	worker.runOnce()

	alertMu.Lock()
	require.Len(t, alerts, 3, "must emit exactly 1 recovery alert when chain revives")
	assert.Contains(t, alerts[2], "Chain đã phục hồi")
	assert.Contains(t, alerts[2], "Tiếp tục gửi checkpoint")
	alertMu.Unlock()

	// Subsequent normal operations do NOT send more recovery alerts
	src.attested = 1100
	worker.runOnce()
	alertMu.Lock()
	assert.Len(t, alerts, 3, "normal rounds after recovery must not send spam")
	alertMu.Unlock()
}

func TestCheckpointWorker_ForkDetected_HaltsSubmission(t *testing.T) {
	kp := bls.GenerateKeyPair()
	src := newFakeSource()
	src.attested = 500
	client := newFakeParentClient()

	// Parent has checkpoint 200 with an UNKNOWN / FORKED hash
	client.hasLatest = true
	client.latestNumber = 200
	client.latestHash = common.HexToHash("0xFORKED_BLOCK_HASH")

	worker := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker.SetRejectBackoffRounds(10)

	var recordedAlerts []string
	var alertMu sync.Mutex
	worker.SetAlertSink(func(msg string) {
		alertMu.Lock()
		defer alertMu.Unlock()
		recordedAlerts = append(recordedAlerts, msg)
	})

	// Round 1: fork detected, emits dedicated fork alert and sets backoff
	worker.runOnce()

	// Worker MUST halt and NOT submit any checkpoints!
	assert.Empty(t, client.submittedBatches, "must not submit checkpoints when fork is detected")
	assert.Nil(t, worker.inflight)
	assert.True(t, worker.isForkDetected)
	assert.Greater(t, worker.backoffUntil, worker.rounds)

	alertMu.Lock()
	require.Len(t, recordedAlerts, 1, "exactly 1 fork alert must be sent")
	assert.Contains(t, recordedAlerts[0], "PHÁT HIỆN LỆCH HASH / FORK")
	alertMu.Unlock()

	// Rounds 2 & 3: during backoff, no duplicate queries or alerts
	worker.runOnce()
	worker.runOnce()

	alertMu.Lock()
	assert.Len(t, recordedAlerts, 1, "must not spam alerts during backoff")
	alertMu.Unlock()
}

func TestCheckpointWorker_InflightRejected_HaltsRetry(t *testing.T) {
	kp := bls.GenerateKeyPair()
	src := newFakeSource()
	src.attested = 300
	client := newFakeParentClient()
	client.hasLatest = true
	client.latestNumber = 100

	var alerts []string
	var alertMu sync.Mutex

	// Use txDropCounter so SendSubmitBlockCheckpoints returns 0xdropped and does NOT apply state
	client.txDropCounter = 1

	worker := NewCheckpointWorker(client, src, kp, 100, time.Second)
	worker.SetRejectBackoffRounds(10) // 10 rounds backoff for test
	worker.SetAlertSink(func(msg string) {
		alertMu.Lock()
		defer alertMu.Unlock()
		alerts = append(alerts, msg)
	})

	// Round 1: tx is sent, received as 0xdropped
	worker.runOnce()
	require.Equal(t, 1, client.sendCallCount)
	require.NotNil(t, worker.inflight)
	require.Equal(t, common.HexToHash("0xdropped"), worker.inflight.txHash)

	// Simulate Parent Chain rejecting the tx (Status 0, ErrorCode 230 - ErrCheckpointConflict)
	client.receipts[common.HexToHash("0xdropped")] = &parentchain.Receipt{
		TxHash:    common.HexToHash("0xdropped"),
		Status:    0,
		ErrorCode: 230,
	}

	// Round 2: worker checks receipt, sees Status 0 -> triggers backoff, sets isTxRejected, sends 1st alert
	worker.runOnce()
	assert.Nil(t, worker.inflight, "inflight must be cleared when receipt status is 0")
	assert.Greater(t, worker.backoffUntil, worker.rounds, "backoffUntil must be set")
	assert.True(t, worker.isTxRejected, "isTxRejected must be set on status 0")

	alertMu.Lock()
	require.Len(t, alerts, 1, "exactly 1 alert must be sent on initial rejection")
	assert.Contains(t, alerts[0], "bị từ chối")
	alertMu.Unlock()

	// Rounds 3 & 4: during backoff, worker MUST NOT resubmit!
	worker.runOnce()
	worker.runOnce()

	// Exactly 1 send call occurred across all 4 rounds!
	assert.Equal(t, 1, client.sendCallCount, "must not resubmit rejected batch during backoff")
	assert.Nil(t, worker.inflight)

	// Fast-forward past backoff, drop next tx again to simulate ongoing rejection
	worker.rounds = worker.backoffUntil
	client.txDropCounter = 1
	worker.runOnce() // submits again
	assert.Equal(t, 2, client.sendCallCount)

	// Check receipt status 0 again
	worker.runOnce()
	alertMu.Lock()
	assert.Len(t, alerts, 1, "ongoing rejection MUST NOT spam Telegram alerts")
	alertMu.Unlock()

	// Now simulate recovery: parent accepts checkpoint up to 300
	worker.rounds = worker.backoffUntil
	client.latestNumber = 300
	worker.runOnce()

	assert.False(t, worker.isTxRejected, "isTxRejected must clear after recovery")
	alertMu.Lock()
	require.Len(t, alerts, 2, "must emit exactly 1 recovery alert when tx succeeds after rejection")
	assert.Contains(t, alerts[1], "thành công trở lại")
	alertMu.Unlock()
}
