package listener

import (
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

func (s *CrossChainScanner) runSmartWatcher() {
	logger.Info("👀 [Scanner] Smart Watcher started (Multi-node support)")
	const (
		scanInterval   = 400 * time.Millisecond
		pollInterval   = 100 * time.Millisecond
		maxPollWorkers = 16
	)

	sem := make(chan struct{}, maxPollWorkers)
	var tracking sync.Map

	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()

	for range ticker.C {
		s.txHashToWallet.Range(func(key, val any) bool {
			txHash := key.(common.Hash)
			trackedTx := val.(TrackedTx)

			if _, already := tracking.LoadOrStore(txHash, struct{}{}); already {
				return true
			}

			sem <- struct{}{}
			go func(txHash common.Hash, tracked TrackedTx) {
				walletAddr := tracked.Wallet
				defer func() {
					<-sem
					tracking.Delete(txHash)
				}()

				startTime := time.Now()
				lastNodeIdx := 0
				var cli *client_tcp.Client

				for {
					if time.Since(startTime) > 60*time.Second {
						nodeAddr := cli.GetNodeAddr()
						logger.Error("❌ [Watcher] Timeout receipt for txHash=%s after 60s (lastNode=%s)! Force releasing wallet %s", txHash.Hex(), nodeAddr, walletAddr.Hex())
						s.walletPool.MarkReady(walletAddr)
						s.txHashToWallet.Delete(txHash)
						return
					}

					// Thử lần lượt các client để lấy receipt
					var resp *pb.GetTransactionReceiptResponse
					var err error

					// Quản lý kết nối linh hoạt, ưu tiên dùng node hoạt động tốt gần nhất
					var cliIdx int
					cli, cliIdx = s.GetActiveClient(lastNodeIdx)
					if cli != nil {
						resp, err = cli.ChainGetTransactionReceipt(txHash)
						if err == nil {
							lastNodeIdx = cliIdx // Ghi nhớ node tốt để lần sau dùng tiếp
						}
					}

					if err == nil && resp != nil && resp.Receipt != nil {
						if resp.Receipt.Status != pb.RECEIPT_STATUS_RETURNED {
							logger.Error("❌ [Watcher] Receipt status FAILED for %v", resp.Receipt)
						}

						// Thành công: Giải phóng ví
						s.walletPool.MarkReady(walletAddr)
						s.txHashToWallet.Delete(txHash)

						// Lấy local block từ receipt
						blockNumHex := resp.Receipt.GetBlockNumber()
						if blockNumHex != "" {
							var bn uint64
							if _, scanErr := fmt.Sscanf(blockNumHex, "0x%x", &bn); scanErr == nil && bn > 0 {
								// LUÔN LUÔN cập nhật tiến độ và localBlock chung 1 channel
								s.enqueueProgressUpdate(tracked.NationId, tracked.RemoteBlock, bn, tracked.IsQuorum)
							}
						}

						logger.Info("✅ [Watcher] Receipt confirmed txHash=%s from Node[%d], status=%s → wallet %s ready",
							txHash.Hex(), lastNodeIdx, resp.Receipt.Status, walletAddr.Hex())
						return
					}
					// Nếu chưa có receipt hoặc lỗi kết nối cụm, chờ rồi thử lại
					time.Sleep(pollInterval)
				}
			}(txHash, trackedTx)
			return true
		})
	}
}
