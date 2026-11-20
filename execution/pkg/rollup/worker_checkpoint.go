package rollup

import (
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

type CheckpointSource interface {
	IsLeader() bool
	AttestedBlock() uint64
	BlockAt(n uint64) (hash, stateRoot common.Hash, ok bool)
	LastPrunedBlock() uint64
}

type inFlightInfo struct {
	txHash common.Hash
	last   uint64
	rounds int
}

const (
	defaultStallRounds   = 30 // ~60s at 2s tick
	defaultRefreshRounds = 30 // ~60s at 2s tick

	defaultFailWarnThreshold   = 5   // ~10s: initial warning alert to Telegram
	defaultFailDeadThreshold   = 15  // ~30s: declare Parent Chain dead, send final alert, then mute
	defaultRejectBackoffRounds = 150 // ~5 min at 2s tick: backoff after tx rejection or detected fork
)

type CheckpointWorker struct {
	client         parentchain.Client
	src            CheckpointSource
	keyPair        *bls.KeyPair
	intervalBlocks uint64
	tick           time.Duration

	stallRounds   int
	refreshRounds int

	inflight     *inFlightInfo
	confirmed    uint64
	hasConfirmed bool
	rounds       uint64

	// Telegram Alert & Anti-spam state
	alertSink         func(msg string)
	failWarnThreshold int
	failDeadThreshold int
	failStreak        int
	isParentDead      bool

	// Backoff after rejected tx or detected fork
	rejectBackoffRounds int
	backoffUntil        uint64
	isForkDetected      bool
	isTxRejected        bool

	wakeCh chan struct{}
	quitCh chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
}

func NewCheckpointWorker(client parentchain.Client, src CheckpointSource,
	keyPair *bls.KeyPair, intervalBlocks uint64, tick time.Duration) *CheckpointWorker {
	if tick <= 0 {
		tick = 2 * time.Second
	}
	return &CheckpointWorker{
		client:              client,
		src:                 src,
		keyPair:             keyPair,
		intervalBlocks:      intervalBlocks,
		tick:                tick,
		stallRounds:         defaultStallRounds,
		refreshRounds:       defaultRefreshRounds,
		alertSink:           func(msg string) { logger.Telegram(msg) },
		failWarnThreshold:   defaultFailWarnThreshold,
		failDeadThreshold:   defaultFailDeadThreshold,
		rejectBackoffRounds: defaultRejectBackoffRounds,
		wakeCh:              make(chan struct{}, 1),
		quitCh:              make(chan struct{}),
	}
}

// SetAlertSink configures the callback sink for operator alerts (Telegram).
func (w *CheckpointWorker) SetAlertSink(sink func(msg string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.alertSink = sink
}

// SetThresholds configures warn and dead thresholds (useful in tests).
func (w *CheckpointWorker) SetThresholds(warnThresh, deadThresh int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failWarnThreshold = warnThresh
	w.failDeadThreshold = deadThresh
}

// SetRejectBackoffRounds sets the backoff duration in rounds after tx rejection or fork (useful in tests).
func (w *CheckpointWorker) SetRejectBackoffRounds(r int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rejectBackoffRounds = r
}

func (w *CheckpointWorker) recordFailure(detail string) {
	w.failStreak++
	warnThresh := w.failWarnThreshold
	if warnThresh <= 0 {
		warnThresh = defaultFailWarnThreshold
	}
	deadThresh := w.failDeadThreshold
	if deadThresh <= 0 {
		deadThresh = defaultFailDeadThreshold
	}

	if w.failStreak == warnThresh && !w.isParentDead {
		if w.alertSink != nil {
			w.alertSink(fmt.Sprintf("⚠️ [ParentChain] Mất kết nối %d lần liên tiếp (%s). Đang tự động thử lại...", w.failStreak, detail))
		}
	} else if w.failStreak >= deadThresh && !w.isParentDead {
		w.isParentDead = true
		if w.alertSink != nil {
			w.alertSink(fmt.Sprintf("🚨 [ParentChain] Chain ngừng hoạt động (%d lần lỗi). Dừng thông báo tránh spam, sẽ báo lại khi chain sống dậy!", w.failStreak))
		}
	}
	// When w.isParentDead == true, keep silent on Telegram to prevent spamming.
}

func (w *CheckpointWorker) recordSuccess() {
	if w.isParentDead {
		if w.alertSink != nil {
			w.alertSink(fmt.Sprintf("🟢 [ParentChain] Chain đã phục hồi sau %d lần lỗi! Tiếp tục gửi checkpoint.", w.failStreak))
		}
		w.isParentDead = false
	}
	w.failStreak = 0
}

func (w *CheckpointWorker) Start() {
	if w.intervalBlocks == 0 || w.keyPair == nil {
		logger.Info("[CHECKPOINT-WORKER] Checkpoint anchoring disabled (interval=%d, keyPair=%v)", w.intervalBlocks, w.keyPair != nil)
		return
	}
	w.wg.Add(1)
	go w.loop()
	logger.Info("🚀 [CHECKPOINT-WORKER] Started Raft checkpoint anchoring worker (interval=%d blocks, tick=%v)", w.intervalBlocks, w.tick)
}

func (w *CheckpointWorker) Stop() {
	if w.intervalBlocks == 0 || w.keyPair == nil {
		return
	}
	close(w.quitCh)
	w.wg.Wait()
	logger.Info("[CHECKPOINT-WORKER] Stopped")
}

func (w *CheckpointWorker) Wakeup() {
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
}

func (w *CheckpointWorker) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.tick)
	defer ticker.Stop()

	for {
		select {
		case <-w.quitCh:
			return
		case <-w.wakeCh:
			w.runOnce()
		case <-ticker.C:
			w.runOnce()
		}
	}
}

func (w *CheckpointWorker) runOnce() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.src.IsLeader() {
		w.inflight = nil
		w.hasConfirmed = false
		w.confirmed = 0
		w.failStreak = 0
		w.isParentDead = false
		w.isForkDetected = false
		w.isTxRejected = false
		w.backoffUntil = 0
		return
	}

	I := w.intervalBlocks
	if I == 0 {
		return
	}

	attested := w.src.AttestedBlock()
	target := (attested / I) * I
	if target == 0 {
		return
	}

	w.rounds++
	if w.rounds < w.backoffUntil {
		return
	}

	// Cache check: if no in-flight tx, and we know confirmed height >= target,
	// and it's not time for periodic refresh, skip parent query.
	refreshRounds := w.refreshRounds
	if refreshRounds <= 0 {
		refreshRounds = defaultRefreshRounds
	}
	if w.inflight == nil && w.hasConfirmed && target <= w.confirmed && (w.rounds%uint64(refreshRounds) != 0) {
		return
	}

	// Read latest checkpoint from Parent Chain using strong quorum
	latest, latestHash, _, found, err := w.client.GetLatestBlockCheckpoint(w.keyPair.PublicKey())
	if err != nil {
		logger.Debug("[CHECKPOINT-WORKER] GetLatestBlockCheckpoint quorum read failed: %v", err)
		w.recordFailure(fmt.Sprintf("GetLatestBlockCheckpoint failed: %v", err))
		return
	}

	w.hasConfirmed = true
	if found {
		w.confirmed = latest
		if w.isTxRejected && latest >= target {
			w.isTxRejected = false
			if w.alertSink != nil {
				w.alertSink("🟢 [ParentChain] Giao dịch checkpoint đã thành công trở lại! Tiếp tục neo checkpoint.")
			}
		}

		// Fork / Reset safeguard: ensure parent latest checkpoint hash matches local chain
		localHash, _, ok := w.src.BlockAt(latest)
		if ok {
			if localHash != latestHash {
				metrics.CheckpointForkDetectedTotal.Inc()
				backoff := w.rejectBackoffRounds
				if backoff <= 0 {
					backoff = defaultRejectBackoffRounds
				}
				w.backoffUntil = w.rounds + uint64(backoff)

				if !w.isForkDetected {
					w.isForkDetected = true
					logger.Error("🚨 [CHECKPOINT-WORKER] CRITICAL FORK/RESET DETECTED: Parent latest checkpoint %d hash (%s) does not match local block hash (%s)! Halting checkpoint submission for %d rounds.", latest, latestHash.Hex(), localHash.Hex(), backoff)
					if w.alertSink != nil {
						w.alertSink(fmt.Sprintf("🚨 [ParentChain] PHÁT HIỆN LỆCH HASH / FORK: Checkpoint %d trên Parent (%s) khác hash cục bộ (%s)! Đã dừng neo checkpoint để bảo vệ dữ liệu.", latest, latestHash.Hex(), localHash.Hex()))
					}
				}
				return
			}
			if w.isForkDetected {
				w.isForkDetected = false
				if w.alertSink != nil {
					w.alertSink("🟢 [ParentChain] Lệch hash / fork đã được giải quyết! Tiếp tục neo checkpoint.")
				}
			}
		} else {
			lastPruned := w.src.LastPrunedBlock()
			if lastPruned >= latest {
				logger.Warn("⚠️ [CHECKPOINT-WORKER] Latest checkpoint %d on parent already pruned locally (lastPruned=%d), skipping hash consistency check", latest, lastPruned)
			} else {
				logger.Warn("⚠️ [CHECKPOINT-WORKER] Local block %d unavailable for hash consistency check against parent", latest)
				metrics.CheckpointBlockUnavailableTotal.Inc()
			}
		}
	} else {
		w.confirmed = 0
	}

	stallRounds := w.stallRounds
	if stallRounds <= 0 {
		stallRounds = defaultStallRounds
	}

	if w.inflight != nil {
		if found && latest >= w.inflight.last {
			if w.isTxRejected {
				w.isTxRejected = false
				if w.alertSink != nil {
					w.alertSink("🟢 [ParentChain] Giao dịch checkpoint đã thành công trở lại! Tiếp tục neo checkpoint.")
				}
			}
			w.inflight = nil
		} else {
			// Check receipt of in-flight tx if available
			if w.inflight.txHash != (common.Hash{}) {
				rcpt, rFound, rErr := w.client.GetReceipt(w.inflight.txHash)
				if rErr == nil && rFound {
					if rcpt.Status == 0 {
						backoff := w.rejectBackoffRounds
						if backoff <= 0 {
							backoff = defaultRejectBackoffRounds
						}
						w.backoffUntil = w.rounds + uint64(backoff)

						logger.Error("🚨 [CHECKPOINT-WORKER] in-flight tx %s REJECTED by parent (status=0, errorCode=%d). Halting submission for %d rounds to prevent nonce pipeline reset.", w.inflight.txHash.Hex(), rcpt.ErrorCode, backoff)
						metrics.CheckpointTxRejectedTotal.Inc()
						if !w.isTxRejected {
							w.isTxRejected = true
							if w.alertSink != nil {
								w.alertSink(fmt.Sprintf("🚨 [ParentChain] Giao dịch checkpoint bị từ chối (ErrorCode=%d). Tạm dừng gửi %d vòng để bảo vệ pipeline nonce!", rcpt.ErrorCode, backoff))
							}
						}
						w.inflight = nil
						return
					}
				}
			}

			w.inflight.rounds++
			if w.inflight.rounds < stallRounds {
				w.recordSuccess()
				return // keep waiting
			}
			logger.Warn("⚠️ [CHECKPOINT-WORKER] in-flight tx stalled for %d rounds (target=%d, latest=%d), retrying", w.inflight.rounds, w.inflight.last, latest)
			w.inflight = nil
		}
	}

	var start uint64
	if found {
		start = (latest/I + 1) * I
	} else {
		start = target // !found => bootstrap from current target
	}

	// Pruning safeguard (Section 3.5)
	lastPruned := w.src.LastPrunedBlock()
	if lastPruned > 0 && start <= lastPruned {
		newStart := (lastPruned/I + 1) * I
		logger.Warn("🚨 [CHECKPOINT-WORKER] CRITICAL: requested checkpoint %d was pruned (lastPruned=%d), skipping to %d", start, lastPruned, newStart)
		metrics.CheckpointGapTotal.Inc()
		start = newStart
	}

	if start > target {
		w.recordSuccess()
		return
	}

	// Construct batch up to MaxBlockCheckpointsBatch (64)
	var batch []*pb.BlockCheckpoint
	for n := start; n <= target && len(batch) < parentchain.MaxBlockCheckpointsBatch; n += I {
		hash, stateRoot, ok := w.src.BlockAt(n)
		if !ok {
			logger.Warn("[CHECKPOINT-WORKER] BlockAt(%d) unavailable, terminating batch", n)
			metrics.CheckpointBlockUnavailableTotal.Inc()
			break
		}
		batch = append(batch, &pb.BlockCheckpoint{
			BlockNumber: n,
			BlockHash:   hash.Bytes(),
			StateRoot:   stateRoot.Bytes(),
		})
	}

	if len(batch) == 0 {
		w.recordSuccess()
		return
	}

	// Sign and submit batch
	msg := parentchain.ComputeBlockCheckpointsMessage(w.keyPair.PublicKey(), batch)
	cert := bls.Sign(w.keyPair.PrivateKey(), msg)

	txHash, err := w.client.SendSubmitBlockCheckpoints(w.keyPair.PublicKey(), batch, cert)
	if err != nil {
		logger.Warn("⚠️ [CHECKPOINT-WORKER] SendSubmitBlockCheckpoints failed: %v", err)
		w.recordFailure(fmt.Sprintf("SendSubmitBlockCheckpoints failed: %v", err))
		return
	}

	w.recordSuccess()

	lastInBatch := batch[len(batch)-1].BlockNumber
	logger.Info("✅ [CHECKPOINT-WORKER] Submitted %d checkpoints (range [%d..%d]) tx=%s", len(batch), batch[0].BlockNumber, lastInBatch, txHash.Hex())
	w.inflight = &inFlightInfo{
		txHash: txHash,
		last:   lastInBatch,
		rounds: 0,
	}
}
