package listener

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/contracts/cross_chain_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// runResweeper quét lại các batch đang ở trạng thái Pending.
// Nếu một batch đã gửi nhưng chưa thấy log (MessageReceived hoặc OutboundResult)
// sau 20s, nó sẽ thực hiện quét lại (rescan) trên node mục tiêu.
func (s *CrossChainScanner) runResweeper() {
	var messageReceivedTopic, outboundResultTopic common.Hash
	if ev, ok := s.cfg.CrossChainAbi.Events["MessageReceived"]; ok {
		messageReceivedTopic = ev.ID
	}
	if ev, ok := s.cfg.CrossChainAbi.Events["OutboundResult"]; ok {
		outboundResultTopic = ev.ID
	}
	contractAddr := common.HexToAddress(s.cfg.CrossChainContract_)

	sem := make(chan struct{}, 16)
	var activeWorkers int32

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	logger.Info("🧹 [Resweeper] Started (Interval: 30s, Workers: 16)")

	for range ticker.C {
		// Đếm số lượng pending batches để in ra log
		count := 0
		s.pendingBatches.Range(func(key, value interface{}) bool {
			count++
			return true
		})
		logger.Info("🧹 [Resweeper] Periodic scan triggered. Pending batches: %d", count)

		s.pendingBatches.Range(func(key, value interface{}) bool {
			// Chặn ở đây nếu đã đủ 16 worker — đợi cho đến khi có slot trống
			sem <- struct{}{}
			atomic.AddInt32(&activeWorkers, 1)
			go func(k, v interface{}) {
				defer func() {
					<-sem
					atomic.AddInt32(&activeWorkers, -1)
				}()

				// Tạo context timeout để tránh worker bị treo vĩnh viễn
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()

				s.ProcessSingleResweep(ctx, k, v, messageReceivedTopic, outboundResultTopic, contractAddr)
			}(key, value)

			return true
		})
	}
}

func (s *CrossChainScanner) ProcessSingleResweep(
	ctx context.Context,
	key, value interface{},
	messageReceivedTopic, outboundResultTopic common.Hash,
	contractAddr common.Address,
) {
	batchId := key.([32]byte)
	data := value.(*PendingBatchData)

	// Dùng client đã dùng để submit batch này (hoặc node sống gần nó nhất)
	localConnClient, _ := s.GetActiveClient(data.TargetIndex)
	if localConnClient == nil {
		logger.Warn("🧹 [Resweeper] Cannot get active client to check log (Batch %x, NodeIndex %d)", batchId[:4], data.TargetIndex)
		return
	}

	// Lấy msgId + topics đúng theo loại event:
	// - INBOUND:      msgId từ Packet.MessageId      → check MessageReceived (msgId ở Topic[3])
	// - CONFIRMATION: msgId từ Confirmation.MessageId → check OutboundResult  (msgId ở Topic[1])
	var topics [][]common.Hash
	eventKind := data.Events[0].EventKind
	var msgIdHex string
	if eventKind == cross_chain_contract.EventKindInbound {
		msgId := data.Events[0].Packet.MessageId
		msgIdHex = fmt.Sprintf("%x", msgId[:])
		topics = [][]common.Hash{
			{messageReceivedTopic},
			{},      // Topic 1: sourceNationId
			{},      // Topic 2: destNationId
			{msgId}, // Topic 3: msgId
		}
	} else {
		msgId := data.Events[0].Confirmation.MessageId
		msgIdHex = fmt.Sprintf("%x", msgId[:])
		topics = [][]common.Hash{
			{outboundResultTopic},
			{msgId}, // Topic 1: msgId
		}
	}

	logger.Info("🧹 [Resweeper] Checking Batch %x (txHash: %s): eventKind=%s (submitBlock=%d), msgId=%s",
		batchId[:4],
		data.TxHash.Hex(),
		func() string {
			if eventKind == cross_chain_contract.EventKindInbound {
				return "INBOUND"
			}
			return "CONFIRM"
		}(),
		data.SubmitBlock,
		msgIdHex)

	// Quét từ block lúc submit (SubmitBlock) trừ đi 50 block để tránh miss
	fromBlk := data.SubmitBlock
	if fromBlk > 50 {
		fromBlk -= 50
	} else {
		fromBlk = 1
	}

	// Chờ lấy block number mới nhất
	latestBlk, errBlk := localConnClient.ChainGetBlockNumber()
	if errBlk != nil {
		logger.Warn("🧹 [Resweeper] Cannot get latest block from node %s: %v", localConnClient.GetNodeAddr(), errBlk)
		return
	}

	if latestBlk < fromBlk {
		logger.Warn("🧹 [Resweeper] Node %s is behind? (latest=%d < searchFrom=%d). Skipping batch %x",
			localConnClient.GetNodeAddr(), latestBlk, fromBlk, batchId[:4])
		return
	}

	fromBlockStr := hexutil.EncodeUint64(fromBlk)
	toBlockStr := hexutil.EncodeUint64(latestBlk)

	// Gọi API lấy logs
	// Note: ChainGetLogs không nhận context nên ta không thể cancel nó giữa chừng,
	// nhưng context timeout ở ngoài sẽ giúp worker pool không bị đầy nếu nó treo.
	respRecv, err := localConnClient.ChainGetLogs(
		nil,
		fromBlockStr, // From: block lúc submit - 50
		toBlockStr,   // To: block mới nhất
		[]common.Address{contractAddr},
		topics,
	)

	if err != nil {
		logger.Warn("🧹 [Resweeper] ChainGetLogs error (node=%s, batch=%x): %v", localConnClient.GetNodeAddr(), batchId[:4], err)
		return
	}

	if respRecv != nil && len(respRecv.Logs) > 0 {
		if len(respRecv.Logs) >= 2 {
			logger.Error("🚨 [Resweeper] DUPLICATE DETECTED: Batch %x found %d times on chain!", batchId[:4], len(respRecv.Logs))
		}
		logger.Info("🧹 [Resweeper] Batch %x HOÀN THÀNH - Log found at node %s. Removing from pending.", batchId[:4], localConnClient.GetNodeAddr())
		s.pendingBatches.Delete(key)
	} else {
		// Không thấy log => Có thể TX bị rớt hoặc Node chưa sync kịp
		logger.Warn("🧹 [Resweeper] Batch %x NOT FOUND on node %s (checked blocks %d to %d). Will retry later.",
			batchId[:4], localConnClient.GetNodeAddr(), fromBlk, latestBlk)
	}
}
