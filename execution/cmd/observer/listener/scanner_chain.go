package listener

import (
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/contracts/cross_chain_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// ─────────────────────────────────────────────────────────────────────────────
// Scan loop cho 1 remote chain
// ─────────────────────────────────────────────────────────────────────────────

func (s *CrossChainScanner) runChainScanner(rc tcp_config.RemoteChain, connAddr string, resumeState ScanResumeState) {
	// nationIdStr := fmt.Sprintf("%d", rc.NationId)

	lastBlock := resumeState.RemoteBlock // Resume từ điểm đã scan hoặc 0 nếu chưa có
	needCheckExecuted := resumeState.NeedCheckExecuted
	localBlock := resumeState.LocalBlock

	if lastBlock > 0 {
		logger.Info("📌 [Scanner][%s] Resuming from block %d (needCheckExecuted=%v, localBlock=%d)", rc.Name, lastBlock, needCheckExecuted, localBlock)
	} else {
		logger.Info("🔄 [Scanner][%s] Scan loop started from block 0", rc.Name)
	}

	var client *client_tcp.Client
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
			isEmpty := true
			s.pendingBatches.Range(func(key, value interface{}) bool {
				isEmpty = false
				return false // Dừng vòng lặp ngay khi tìm thấy 1 phần tử
			})

			if isEmpty && time.Since(lastUpdateTime) >= time.Minute {
				// Định kỳ 1 phút cắm chốt lên chain nếu toàn block rỗng tránh khi restart phải fetch lại từ đầu
				// Lấy local block hiện tại (Retry liên tục nếu lỗi)
				localBlockNum := uint64(0)
				for {
					localCli, _ := s.GetActiveClient(0)
					if localCli != nil {
						bn, err := localCli.ChainGetBlockNumber()
						if err == nil {
							localBlockNum = bn
							break
						}
					}
					time.Sleep(1 * time.Second)
				}
				s.enqueueProgressUpdate(rc.NationId, lastBlock, localBlockNum, true)
				logger.Info("⏱️ [Scanner][%s] Khởi chạy cập nhật snapshot trống sau 1 phút quét rỗng (block %d)", rc.Name, lastBlock)
			}
			lastUpdateTime = time.Now()
			time.Sleep(s.scanInterval)
			continue
		}

		// Scan từng block một từ lastBlock+1 đến latestBlock
		for blockNum := lastBlock + 1; blockNum <= latestBlock; blockNum++ {

			hasEvents, isExecuted, errScan := s.scanAndSubmit(rc, client, blockNum, needCheckExecuted, localBlock)
			if errScan != nil {
				// GetLogs thất bại — dừng, thử lại block này ở vòng ngoài
				logger.Warn("⚠️  [Scanner][%s] Scan failed at block %d (node=%s): %v, will retry", rc.Name, blockNum, client.GetNodeAddr(), errScan)
				break
			}
			lastBlock = blockNum
			if isExecuted {
				continue
			}
			if hasEvents {
				needCheckExecuted = false // Chỉ cần check lô đầu tiên, nếu có batch mới thì ngắt cờ
				lastUpdateTime = time.Now()
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
// Trả về (hasEvents, isExecuted, error)
func (s *CrossChainScanner) scanAndSubmit(
	rc tcp_config.RemoteChain,
	client *client_tcp.Client,
	blockNum uint64,
	needCheckExecuted bool,
	localBlock uint64,
) (bool, bool, error) {
	contractAddr := common.HexToAddress(s.cfg.CrossChainContract_)
	if contractAddr == (common.Address{}) {
		logger.Warn("[Scanner][%s] contract_cross_chain not configured", rc.Name)
		return false, false, fmt.Errorf("contract_cross_chain not configured")
	}

	// Topic signatures từ ABI
	var messageSentTopic, messageReceivedTopic, outboundResultTopic common.Hash
	if ev, ok := s.cfg.CrossChainAbi.Events["MessageSent"]; ok {
		messageSentTopic = ev.ID
	}
	if ev, ok := s.cfg.CrossChainAbi.Events["MessageReceived"]; ok {
		messageReceivedTopic = ev.ID
	}
	if ev, ok := s.cfg.CrossChainAbi.Events["OutboundResult"]; ok {
		outboundResultTopic = ev.ID
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
		return false, false, fmt.Errorf("GetLogs combined block=%d (node=%s): %w", blockNum, client.GetNodeAddr(), err)
	}

	var allLogs []*pb.LogEntry
	var sentTopicCount, recvTopicCount int
	var sentNationMismatch, recvNationMismatch int
	if resp != nil && resp.Logs != nil {
		// Lọc bộ nhớ trong Go (In-memory filter) tiết kiệm tải cho Full Node
		for _, log := range resp.Logs {
			if len(log.Topics) == 0 {
				continue
			}
			t0 := common.BytesToHash(log.Topics[0])

			// 1. Lọc MessageSent (destNationId = localNation)
			if t0 == messageSentTopic && len(log.Topics) > 2 {
				sentTopicCount++
				if common.BytesToHash(log.Topics[2]) == localNationTopic {
					allLogs = append(allLogs, log)
				} else {
					sentNationMismatch++
				}
			} else
			// 2. Lọc MessageReceived (sourceNationId = localNation)
			if t0 == messageReceivedTopic && len(log.Topics) > 1 {
				recvTopicCount++
				if common.BytesToHash(log.Topics[1]) == localNationTopic {
					allLogs = append(allLogs, log)
				} else {
					recvNationMismatch++
				}
			}
		}
	}

	logger.Info("📦 [Scanner][%s] Block %d: %d total raw logs from node %s → %d filtered logs found",
		rc.Name, blockNum, func() int {
			if resp != nil && resp.Logs != nil {
				return len(resp.Logs)
			}
			return 0
		}(), client.GetNodeAddr(), len(allLogs))

	hasEvents := false
	if len(allLogs) > 0 {
		events := s.buildEmbassyEvents(rc, allLogs, messageSentTopic, messageReceivedTopic)
		if len(events) > 0 {
			// -----------------------------------------------------
			// CHECK NẾU ĐÃ ĐƯỢC XỬ LÝ (RESUME STATE)
			// -----------------------------------------------------
			if needCheckExecuted && localBlock > 0 {
				eventKind := events[0].EventKind
				var checkTopic common.Hash
				if eventKind == cross_chain_contract.EventKindInbound {
					checkTopic = messageReceivedTopic
				} else {
					checkTopic = outboundResultTopic
				}

				msgId := events[0].Packet.MessageId
				if eventKind == cross_chain_contract.EventKindConfirmation {
					msgId = events[0].Confirmation.MessageId
				}

				// Quét log từ localBlock - 2000 đến localBlock + 1000 để tìm msgId này
				fromBlk := uint64(0)
				if localBlock > 2000 {
					fromBlk = localBlock - 2000
				}
				toBlk := localBlock + 1000
				// Xây dựng topic array tuỳ theo eventKind
				var topics [][]common.Hash
				if eventKind == cross_chain_contract.EventKindInbound {
					topics = [][]common.Hash{
						{checkTopic},
						{},      // Topic 1: sourceNationId
						{},      // Topic 2: destNationId
						{msgId}, // Topic 3: msgId
					}
				} else {
					topics = [][]common.Hash{
						{checkTopic},
						{msgId}, // Topic 1: msgId
					}
				}

				totalLocalClients := len(s.localClients)
				if totalLocalClients == 0 {
					totalLocalClients = 1
				}

				var logResp *pb.GetLogsResponse
				var errLog error
				success := false

				for attempt := 0; attempt < totalLocalClients; attempt++ {
					localCli, _ := s.GetActiveClient(attempt)
					if localCli != nil {
						logResp, errLog = localCli.ChainGetLogs(nil, hexutil.EncodeUint64(fromBlk), hexutil.EncodeUint64(toBlk), []common.Address{contractAddr}, topics)
						if errLog == nil {
							success = true
							break
						}
						logger.Warn("⚠️  [Scanner][%s] ChainGetLogs execution check failed on attempt %d: %v", rc.Name, attempt+1, errLog)
					} else {
						errLog = fmt.Errorf("no active local client available")
					}
					time.Sleep(500 * time.Millisecond)
				}

				if !success {
					logger.Error("❌ [Scanner][%s] ChainGetLogs for execution check COMPLETELY FAILED after %d attempts: %v", rc.Name, totalLocalClients, errLog)
					return false, false, fmt.Errorf("ChainGetLogs for execution check failed after %d attempts: %w", totalLocalClients, errLog)
				}

				if logResp != nil && len(logResp.Logs) > 0 {
					logger.Info("⏩ [Scanner][%s] Batch from block %d ALREADY EXECUTED on local chain (eventKind=%d, msgId=%x...). Skipping submit.",
						rc.Name, blockNum, eventKind, msgId[:8])
					return false, true, nil
				}
			}

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

				var batchId [32]byte
				for attempt := 1; ; attempt++ {
					var tIndex int
					txHash, tIndex, _, err = s.submitBatch(rc, chunk, -1, blockNum)
					if err == nil {
						batchId, _ = s.calculateBatchId(chunk)
						// Lấy block hiện tại để Resweeper biết quét từ đâu (retry đến khi lấy được)
						var submitBlk uint64
						for att := 1; ; att++ {
							if defCli, _ := s.GetActiveClient(tIndex); defCli != nil {
								if blk, err := defCli.ChainGetBlockNumber(); err == nil && blk > 0 {
									submitBlk = blk
									break
								}
							}
							logger.Warn("⚠️  [Scanner][%s] Attemp %d to get the node's submit block", rc.Name, att)
							time.Sleep(200 * time.Millisecond)
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
					logger.Warn("⚠️  [Scanner][%s] submitBatch attempt %d failed txHash=%s, at block %d (chunk %d-%d/%d): %v",
						rc.Name, attempt, txHash.Hex(), blockNum, i, end, len(events), err)

					time.Sleep(1 * time.Second)
				}
			}
			// Đã submit thành công tất cả các chunk cho block này
		}
	}
	return hasEvents, false, nil
}
