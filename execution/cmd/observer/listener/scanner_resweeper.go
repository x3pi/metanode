package listener

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/contracts/cross_chain_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

func (s *CrossChainScanner) runResweeper() {
	logger.Info("🧹 [Scanner] Resweeper started")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Topic signatures từ ABI
	var messageReceivedTopic, outboundResultTopic common.Hash
	if ev, ok := s.cfg.CrossChainAbi.Events["MessageReceived"]; ok {
		messageReceivedTopic = ev.ID
	}
	if ev, ok := s.cfg.CrossChainAbi.Events["OutboundResult"]; ok {
		outboundResultTopic = ev.ID
	}

	contractAddr := common.HexToAddress(s.cfg.CrossChainContract_)
	for range ticker.C {
		s.pendingBatches.Range(func(key, value interface{}) bool {
			batchId := key.([32]byte)
			data := value.(*PendingBatchData)

			// Nếu batch chưa quá 10s, bỏ qua
			if time.Since(data.Timestamp) < 20*time.Second {
				return true
			}

			// Dùng client bất kỳ đang khả dụng (client 0 mặc định)
			localConnClient, _ := s.GetActiveClient(0)
			if localConnClient == nil {
				logger.Warn("🧹 [Resweeper] Cannot get active client to check log")
				return true
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
			// do retry chậm hơn các Observer khác
			fromBlk := data.SubmitBlock
			if fromBlk > 50 {
				fromBlk -= 50
			} else {
				fromBlk = 1
			}
			latestBlk, errBlk := localConnClient.ChainGetBlockNumber()
			if errBlk != nil {
				logger.Warn("🧹 [Resweeper] Cannot get latest block (node=%s): %v", localConnClient.GetNodeAddr(), errBlk)
				return true
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
			logger.Info("[Resweeper] rescan node %s", localConnClient.GetNodeAddr())
			if err == nil && respRecv != nil && len(respRecv.Logs) > 0 {
				if len(respRecv.Logs) >= 2 {
					logger.Error("🚨 [Resweeper] CẢNH BÁO: Batch %x đã bị gửi và thực thi TRÙNG LẶP trên chain (tìm thấy %d logs)!", batchId[:4], len(respRecv.Logs))
				}
				logger.Info("🧹 [Resweeper] Batch %x HOÀN THÀNH - Mạng đã chốt. Xoá pending.", batchId[:4])
				s.pendingBatches.Delete(key)
			} else {
				// Không thấy hoặc lỗi => Mạng chưa đạt 2/3 hoặc Node bị chết
				logger.Warn("🧹 [Resweeper] Batch %x bị kẹt quá 30s (checked via Node %s). Chuyển súng sang Node kế tiếp!",
					batchId[:4], localConnClient.GetNodeAddr())

				// Thử gửi lại batch thông qua client kế tiếp
				// nextIndex := data.TargetIndex + 1
				// newTxHash, newActualIndex, errRetry := s.submitBatch(data.RemoteChain, data.Events, nextIndex)
				// if errRetry == nil {
				// 	// Cập nhật lại thời gian và target index
				// 	data.Timestamp = time.Now()
				// 	data.TargetIndex = newActualIndex
				// 	data.TxHash = newTxHash
				// 	logger.Info("🔄 [Resweeper] Gửi lại Batch %x (newTxHash=%s) cho Node %d thành công!", batchId[:4], newTxHash.Hex(), newActualIndex)

				// 	// Lấy block mới nhất để Resweeper quét cho lần sau
				// 	if defCli, _ := s.GetActiveClient(0); defCli != nil {
				// 		if blk, err := defCli.ChainGetBlockNumber(); err == nil && blk > 0 {
				// 			data.SubmitBlock = blk
				// 		}
				// 	}
				// 	s.pendingBatches.Store(key, data)
				// } else {
				// 	logger.Error("🧹 [Resweeper] Gửi lại Batch %x LỖI: %v", batchId[:4], errRetry)
				// }
			}

			return true
		})
	}
}
