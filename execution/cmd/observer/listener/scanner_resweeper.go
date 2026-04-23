package listener

import (
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/contracts/cross_chain_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

func (s *CrossChainScanner) runResweeper() {
	logger.Info("🧹 [Scanner] Resweeper started")

	// Topic signatures từ ABI
	var messageReceivedTopic, outboundResultTopic common.Hash
	if ev, ok := s.cfg.CrossChainAbi.Events["MessageReceived"]; ok {
		messageReceivedTopic = ev.ID
	}
	if ev, ok := s.cfg.CrossChainAbi.Events["OutboundResult"]; ok {
		outboundResultTopic = ev.ID
	}

	contractAddr := common.HexToAddress(s.cfg.CrossChainContract_)

	// Giới hạn 8 goroutine quét cùng lúc (giống pattern bên Watcher)
	sem := make(chan struct{}, 16)
	var tracking sync.Map

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.pendingBatches.Range(func(key, value interface{}) bool {
			batchId := key.([32]byte)
			data := value.(*PendingBatchData)

			// Nếu batch chưa quá 20s, bỏ qua
			if time.Since(data.Timestamp) < 20*time.Second {
				return true
			}

			// Kiểm tra xem batch này có đang được quét chưa
			if _, already := tracking.LoadOrStore(batchId, struct{}{}); already {
				return true
			}

			// Gửi vào goroutine quét
			select {
			case sem <- struct{}{}:
				go func(k, v interface{}) {
					defer func() {
						<-sem
						tracking.Delete(batchId)
					}()
					s.processSingleResweep(k, v, messageReceivedTopic, outboundResultTopic, contractAddr)
				}(key, value)
			default:
				// Nếu đã đạt giới hạn worker, giải phóng tracking để lượt sau quét lại
				tracking.Delete(batchId)
				// logger.Warn("🧹 [Resweeper] Max workers (8) reached, skipping batch %x", batchId[:4])
			}

			return true
		})
	}
}

func (s *CrossChainScanner) processSingleResweep(
	key, value interface{},
	messageReceivedTopic, outboundResultTopic common.Hash,
	contractAddr common.Address,
) {
	batchId := key.([32]byte)
	data := value.(*PendingBatchData)

	// Dùng client đã dùng để submit batch này (hoặc node sống gần nó nhất)
	localConnClient, _ := s.GetActiveClient(data.TargetIndex)
	if localConnClient == nil {
		logger.Warn("🧹 [Resweeper] Cannot get active client to check log (Batch %x)", batchId[:4])
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

	logger.Info("🧹 [Resweeper] Checking Batch %x (txHash: %s): eventKind=%s (fromBlock=%d), msgId=%s",
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

	latestBlk, errBlk := localConnClient.ChainGetBlockNumber()
	if errBlk != nil {
		logger.Warn("🧹 [Resweeper] Cannot get latest block (node=%s): %v", localConnClient.GetNodeAddr(), errBlk)
		return
	}

	fromBlockStr := hexutil.EncodeUint64(fromBlk)
	toBlockStr := hexutil.EncodeUint64(latestBlk)

	respRecv, err := localConnClient.ChainGetLogs(
		nil,
		fromBlockStr, // From: block lúc submit
		toBlockStr,   // To: block mới nhất
		[]common.Address{contractAddr},
		topics,
	)

	logger.Info("[Resweeper] rescan node %s for batch %x", localConnClient.GetNodeAddr(), batchId[:4])

	if err == nil && respRecv != nil && len(respRecv.Logs) > 0 {
		if len(respRecv.Logs) >= 2 {
			logger.Error("🚨 [Resweeper] CẢNH BÁO: Batch %x đã bị gửi và thực thi TRÙNG LẶP trên chain (tìm thấy %d logs)!", batchId[:4], len(respRecv.Logs))
		}
		logger.Info("🧹 [Resweeper] Batch %x HOÀN THÀNH - Mạng đã chốt. Xoá pending.", batchId[:4])
		s.pendingBatches.Delete(key)
	} else {
		// Không thấy hoặc lỗi => Mạng chưa đạt 2/3 hoặc Node bị chết
		logger.Warn("🧹 [Resweeper] Batch %x bị kẹt quá 20s (checked via Node %s).",
			batchId[:4], localConnClient.GetNodeAddr())
	}
}
