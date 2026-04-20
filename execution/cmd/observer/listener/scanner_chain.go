package listener

import (
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// ─────────────────────────────────────────────────────────────────────────────
// Scan loop cho 1 remote chain
// ─────────────────────────────────────────────────────────────────────────────

func (s *CrossChainScanner) runChainScanner(rc tcp_config.RemoteChain, connAddr string, resumeBlock uint64) {
	// nationIdStr := fmt.Sprintf("%d", rc.NationId)

	lastBlock := resumeBlock // Resume từ điểm đã scan hoặc 0 nếu chưa có
	if lastBlock > 0 {
		logger.Info("📌 [Scanner][%s] Resuming from block %d", rc.Name, lastBlock)
	} else {
		logger.Info("🔄 [Scanner][%s] Scan loop started from block 0", rc.Name)
	}

	var client *client_tcp.Client
	var lastUpdateBlock uint64 = lastBlock
	lastUpdateTime := time.Now()
	nodeIdx := 0

	for {
		if client == nil {
			client, nodeIdx = s.GetActiveRemoteClient(rc.NationId, nodeIdx)
			if client == nil {
				logger.Error("❌ [Scanner][%s] No active remote clients found", rc.Name)
				time.Sleep(s.scanInterval + 5*time.Second)
				continue
			}
		}

		latestBlock, errBlock := client.ChainGetBlockNumber()
		if errBlock != nil {
			logger.Error("❌ [Scanner][%s] GetBlockNumber failed: %v", rc.Name, errBlock)
			client = nil // Đánh dấu mất kết nối để vòng lặp sau reconnect
			nodeIdx++
			time.Sleep(s.scanInterval)
			continue
		}

		if latestBlock <= lastBlock {
			// Đã scan hết, chờ block mới
			if lastBlock > lastUpdateBlock && time.Since(lastUpdateTime) >= time.Minute {
				s.enqueueProgressUpdate(rc.NationId, lastBlock)
				lastUpdateBlock = lastBlock
				lastUpdateTime = time.Now()
				logger.Info("⏱️ [Scanner][%s] Khởi chạy cập nhật snapshot trống sau 1 phút không có event (block %d)", rc.Name, lastBlock)
			}
			time.Sleep(s.scanInterval)
			continue
		}

		// Scan từng block một từ lastBlock+1 đến latestBlock
		for blockNum := lastBlock + 1; blockNum <= latestBlock; blockNum++ {
			logger.Info("🔍 [Scanner][%s] Scanning block %d", rc.Name, blockNum)
			hasEvents, errScan := s.scanAndSubmit(rc, client, blockNum)
			if errScan != nil {
				// GetLogs thất bại — dừng, thử lại block này ở vòng ngoài
				logger.Warn("⚠️  [Scanner][%s] Scan failed at block %d (node=%s): %v, will retry", rc.Name, blockNum, client.GetNodeAddr(), errScan)
				break
			}
			lastBlock = blockNum
			if hasEvents {
				lastUpdateBlock = lastBlock
				lastUpdateTime = time.Now()
			} else if lastBlock > lastUpdateBlock && time.Since(lastUpdateTime) >= time.Minute {
				// Định kỳ 1 phút cắm chốt lên chain nếu toàn block rỗng tránh khi restart phải fetch lại từ đầu
				s.enqueueProgressUpdate(rc.NationId, lastBlock)
				lastUpdateBlock = lastBlock
				lastUpdateTime = time.Now()
				logger.Info("⏱️ [Scanner][%s] Khởi chạy cập nhật snapshot trống sau 1 phút quét rỗng (block %d)", rc.Name, lastBlock)
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Scan + gom logs + submit lên chain
// ─────────────────────────────────────────────────────────────────────────────

// scanAndSubmit quét đúng 1 block, gom tất cả logs (MessageSent + MessageReceived)
// từ cùng 1 block đó → gửi batchSubmit lên chain.
// Trả về nil nếu thành công (kể cả submitBatch lỗi — block đã scan xong, không retry).
// Trả về error nếu GetLogs thất bại — caller phải retry lại block này.
func (s *CrossChainScanner) scanAndSubmit(
	rc tcp_config.RemoteChain,
	client *client_tcp.Client,
	blockNum uint64,
) (bool, error) {
	contractAddr := common.HexToAddress(s.cfg.CrossChainContract_)
	if contractAddr == (common.Address{}) {
		logger.Warn("[Scanner][%s] contract_cross_chain not configured", rc.Name)
		return false, fmt.Errorf("contract_cross_chain not configured")
	}

	// Topic signatures từ ABI
	var messageSentTopic, messageReceivedTopic common.Hash
	if ev, ok := s.cfg.CrossChainAbi.Events["MessageSent"]; ok {
		messageSentTopic = ev.ID
	}
	if ev, ok := s.cfg.CrossChainAbi.Events["MessageReceived"]; ok {
		messageReceivedTopic = ev.ID
	}

	blockStr := hexutil.EncodeUint64(blockNum)
	var localNationTopic common.Hash
	new(big.Int).SetUint64(s.cfg.NationId).FillBytes(localNationTopic[:])

	// Gộp quét MessageSent và MessageReceived thành 1 lệnh RPC duy nhất giúp tăng tốc HTTP/TCP lên gấp đôi
	resp, err := client.ChainGetLogs(
		nil,
		blockStr,
		blockStr,
		[]common.Address{contractAddr},
		[][]common.Hash{
			{messageSentTopic, messageReceivedTopic}, // Lấy tất cả sự kiện Sent hoặc Received
		},
	)
	if err != nil {
		return false, fmt.Errorf("GetLogs combined block=%d (node=%s): %w", blockNum, client.GetNodeAddr(), err)
	}

	var allLogs []*pb.LogEntry
	if resp != nil && resp.Logs != nil {
		// Lọc bộ nhớ trong Go (In-memory filter) tiết kiệm tải cho Full Node
		for _, log := range resp.Logs {
			if len(log.Topics) == 0 {
				continue
			}
			t0 := common.BytesToHash(log.Topics[0])

			// 1. Lọc MessageSent (destNationId = localNation)
			if t0 == messageSentTopic && len(log.Topics) > 2 {
				if common.BytesToHash(log.Topics[2]) == localNationTopic {
					allLogs = append(allLogs, log)
				}
			} else
			// 2. Lọc MessageReceived (sourceNationId = localNation)
			if t0 == messageReceivedTopic && len(log.Topics) > 1 {
				if common.BytesToHash(log.Topics[1]) == localNationTopic {
					allLogs = append(allLogs, log)
				}
			}
		}
	}

	logger.Info("📦 [Scanner][%s] Block %d: %d logs found", rc.Name, blockNum, len(allLogs))

	hasEvents := false
	if len(allLogs) > 0 {
		events := s.buildEmbassyEvents(rc, allLogs, messageSentTopic, messageReceivedTopic)
		if len(events) > 0 {
			hasEvents = true
			// Chia events thành chunks nhỏ (maxBatchSize) để tránh TX quá lớn
			const maxBatchSize = 50
			for i := 0; i < len(events); i += maxBatchSize {
				end := i + maxBatchSize
				if end > len(events) {
					end = len(events)
				}
				chunk := events[i:end]

				var txHash common.Hash
				var err error
				maxRetries := 3

				forceIndex := -1
				var batchId [32]byte
				for attempt := 1; attempt <= maxRetries; attempt++ {
					var tIndex int
					txHash, tIndex, err = s.submitBatch(rc, chunk, forceIndex)
					if err == nil {
						batchId, _ = s.calculateBatchId(chunk)
						// Lấy block hiện tại để Resweeper biết quét từ đâu (retry đến khi lấy được)
						var submitBlk uint64
						for retry := 0; retry < 5; retry++ {
							if defCli, _ := s.GetActiveClient(0); defCli != nil {
								if blk, err := defCli.ChainGetBlockNumber(); err == nil && blk > 0 {
									submitBlk = blk
									break
								}
							}
							time.Sleep(500 * time.Millisecond)
						}
						s.pendingBatches.Store(batchId, &PendingBatchData{
							TargetIndex: tIndex,
							RemoteChain: rc,
							Events:      chunk,
							Timestamp:   time.Now(),
							SubmitBlock: submitBlk,
							TxHash:      txHash,
						})
						break
					}
					logger.Warn("⚠️  [Scanner][%s] submitBatch attempt %d/%d failed txHash=%s, at block %d (chunk %d-%d/%d): %v",
						rc.Name, attempt, maxRetries, txHash.Hex(), blockNum, i, end, len(events), err)

					// Nếu thất bại, tính target index cũ và tịnh tiến lên +1
					if forceIndex < 0 {
						batchId, _ = s.calculateBatchId(chunk)
						batchIdBig := new(big.Int).SetBytes(batchId[:])
						mod := big.NewInt(int64(len(s.localClients)))
						forceIndex = int(new(big.Int).Mod(batchIdBig, mod).Int64())
					}
					forceIndex++
					if attempt < maxRetries {
						time.Sleep(2 * time.Second)
					}
				}
				if err != nil {
					logger.Error("❌ [Scanner][%s] submitBatch ALL %d retries failed at block %d: %v", rc.Name, maxRetries, blockNum, err)
					return false, fmt.Errorf("submitBatch failed after %d retries: %w", maxRetries, err)
				}
			}

			// Đã submit thành công tất cả các chunk cho block này
			s.enqueueProgressUpdate(rc.NationId, blockNum)
		}
	}
	return hasEvents, nil
}
