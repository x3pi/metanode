package listener

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/utils/tx_helper"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// ─────────────────────────────────────────────────────────────────────────────
// Progress updater — cập nhật lastBlock lên config SMC sau khi batch gửi xong
// ─────────────────────────────────────────────────────────────────────────────

func (s *CrossChainScanner) enqueueProgressUpdate(chainId uint64, lastBlock uint64) {
	select {
	case s.progressCh <- scanProgressUpdate{chainId: chainId, lastBlock: lastBlock}:
	default:
		logger.Warn("⚠️  [Scanner] progressCh full, dropping update chainId=%d block=%d", chainId, lastBlock)
	}
}

// cần xem lại update khi pending =0 là k update cái nào hết
func (s *CrossChainScanner) runProgressUpdater() {
	logger.Info("🔄 [Scanner] Progress updater started")
	pending := make(map[uint64]uint64) // chainId → lastBlock cao nhất
	var maxLocalBlock uint64           // local block cao nhất (nhận từ receipt watcher)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	flush := func() {
		if len(pending) == 0 && maxLocalBlock == 0 {
			return
		}

		// Nếu chuẩn bị update mà maxLocalBlock == 0 (vd: do update qua 1 phút rỗng, không có TX receipt nào sinh ra)
		// Chủ động fetch block hiện tại của local chain để gửi chốt lên, thay vì gửi số 0 (nguy hiểm)
		if maxLocalBlock == 0 {
			if defCli, _ := s.GetActiveClient(0); defCli != nil {
				if blk, err := defCli.ChainGetBlockNumber(); err == nil {
					maxLocalBlock = blk
				} else {
					logger.Warn("⚠️ [Scanner] Cannot fetch local block for snapshot (node=%s): %v", defCli.GetNodeAddr(), err)
				}
			}
		}

		if len(pending) == 0 {
			logger.Info("[Scanner] Only localBlock=%d to update (no remote scan progress)", maxLocalBlock)
		}
		// Gom toàn bộ map → 2 slice [chainIds, lastBlocks]
		chainIds := make([]uint64, 0, len(pending))
		lastBlocks := make([]uint64, 0, len(pending))
		for cid, blk := range pending {
			chainIds = append(chainIds, cid)
			lastBlocks = append(lastBlocks, blk)
		}

		if len(chainIds) > 0 || maxLocalBlock > 0 {
			s.updateScanProgressBatch(chainIds, lastBlocks, maxLocalBlock)
		}
		pending = make(map[uint64]uint64)
		maxLocalBlock = 0
	}
	for {
		select {
		case upd := <-s.progressCh:
			if upd.lastBlock > pending[upd.chainId] {
				pending[upd.chainId] = upd.lastBlock
			}
		case localBlock := <-s.localBlockCh:
			// Nhận block number từ receipt watcher (batchSubmit TX đã confirm)
			if localBlock > maxLocalBlock {
				maxLocalBlock = localBlock
				logger.Info("[Scanner] 📌 Local block updated from receipt: %d", localBlock)
			}
		case <-ticker.C:
			flush()
		}
	}
}

// updateScanProgressBatch gửi 1 TX batch lên config SMC với toàn bộ chainIds + lastBlocks.
// Dùng SendTransactionFromWallet với from=embassyAddr (cfg.Address()) → embassy ký TX.
// on-chain msg.sender = embassy address đã đăng ký → getScanProgress query sẽ trả đúng block.
func (s *CrossChainScanner) updateScanProgressBatch(chainIds []uint64, lastBlocks []uint64, localBlockNumber uint64) {
	configContract := common.HexToAddress(s.cfg.ConfigContract_)
	if configContract == (common.Address{}) {
		logger.Warn("[Scanner] config_contract not set, skipping scan progress update")
		return
	}
	logger.Info("[Scanner] batchUpdateScanProgress: chainIds=%v, lastBlocks=%v, localBlock=%d", chainIds, lastBlocks, localBlockNumber)
	// ABI uint256[] yêu cầu []*big.Int, không phải []uint64
	destIds := make([]*big.Int, len(chainIds))
	blksBig := make([]*big.Int, len(lastBlocks))
	for i, id := range chainIds {
		destIds[i] = new(big.Int).SetUint64(id)
	}
	for i, blk := range lastBlocks {
		blksBig[i] = new(big.Int).SetUint64(blk)
	}

	localBlockBig := new(big.Int).SetUint64(localBlockNumber)

	// 1 lần Pack với toàn bộ danh sách chains + blocks + localBlockNumber
	calldata, err := s.cfg.ConfigAbi.Pack(
		"batchUpdateScanProgress",
		destIds,
		blksBig,
		localBlockBig,
	)
	if err != nil {
		logger.Warn("[Scanner] pack batchUpdateScanProgress failed: %v", err)
		return
	}

	defCli, _ := s.GetActiveClient(0)
	if defCli == nil {
		logger.Warn("[Scanner] updateScanProgressBatch failed: no active client available")
		return
	}

	// Dùng embassy address (cfg.Address()) để msg.sender = embassy address lưu trong contract
	// → getScanProgress(embassyAddr, nationId) sẽ tra ra đúng block
	embassyAddr := common.HexToAddress(s.cfg.ParentAddress)
	_, err = tx_helper.SendTransaction(
		"batchUpdateScanProgress",
		defCli,
		s.cfg,
		configContract,
		embassyAddr,
		calldata,
		nil,
	)
	if err != nil {
		logger.Warn("[Scanner] updateScanProgressBatch TX failed (node=%s) chains=%v blocks=%v: %v", defCli.GetNodeAddr(), chainIds, lastBlocks, err)
		return
	}
	logger.Info("📋 [Scanner] ScanProgress batch updated: chainIds=%v, lastBlocks=%v, localBlock=%d (ambassador=%s)",
		chainIds, lastBlocks, localBlockNumber, embassyAddr.Hex())
}

// loadInitialScanProgress đọc block đã scan từ config contract cho embassy hiện tại.
// Trả về map[nationId]lastBlock để scanner dùng làm điểm bắt đầu khi resume.
// Nếu config contract chưa set hoặc lỗi → trả về map rỗng (scan từ block 0).
func (s *CrossChainScanner) loadInitialScanProgress() map[uint64]uint64 {
	result := make(map[uint64]uint64)

	configContract := common.HexToAddress(s.cfg.ConfigContract_)
	if configContract == (common.Address{}) {
		logger.Warn("⚠️  [Scanner] config_contract not set, starting scan from block 0")
		return result
	}

	embassyAddr := common.HexToAddress(s.cfg.ParentAddress)

	defCli, _ := s.GetActiveClient(0)
	if defCli == nil {
		logger.Warn("⚠️  [Scanner] getScanProgress failed: no active client available")
		return result
	}

	// Lấy cấu hình scanMode của embassy từ contract
	scanMode := uint8(0)
	if modeCalldata, err := s.cfg.ConfigAbi.Pack("getEmbassyScanMode", embassyAddr); err == nil {
		if receipt, err := tx_helper.SendReadTransaction("getEmbassyScanMode", defCli, s.cfg, configContract, embassyAddr, modeCalldata, nil); err == nil {
			if len(receipt.Return()) > 0 {
				if method, ok := s.cfg.ConfigAbi.Methods["getEmbassyScanMode"]; ok {
					if vals, err := method.Outputs.Unpack(receipt.Return()); err == nil && len(vals) > 0 {
						if modeVal, ok := vals[0].(uint8); ok {
							scanMode = modeVal
						}
					}
				}
			}
		}
	}
	logger.Info("📋 [Scanner] Embassy config ScanMode = %d", scanMode)

	for _, rc := range s.cfg.RemoteChains {
		logger.Info("CCCCCCCCCCCCCCCCCCCCCCCCCCC %v, rc %v", s.cfg.RemoteChains, rc)
		nationIdBig := new(big.Int).SetUint64(rc.NationId)

		calldata, err := s.cfg.ConfigAbi.Pack("getScanProgress", embassyAddr, nationIdBig)
		if err != nil {
			logger.Warn("⚠️  [Scanner] pack getScanProgress failed for nationId=%d: %v", rc.NationId, err)
			continue
		}

		receipt, err := tx_helper.SendReadTransaction(
			"getScanProgress",
			defCli,
			s.cfg,
			configContract,
			embassyAddr,
			calldata,
			nil,
		)
		if err != nil {
			logger.Warn("⚠️  [Scanner] getScanProgress read failed for nationId=%d: %v", rc.NationId, err)
			continue
		}

		returnData := receipt.Return()
		if len(returnData) == 0 {
			logger.Info("📋 [Scanner] getScanProgress nationId=%d → 0 (not set)", rc.NationId)
			continue
		}

		method, ok := s.cfg.ConfigAbi.Methods["getScanProgress"]
		if !ok {
			continue
		}
		vals, err := method.Outputs.Unpack(returnData)
		if err != nil || len(vals) == 0 {
			continue
		}

		lastBlock, ok := vals[0].(*big.Int)
		if !ok || lastBlock == nil {
			continue
		}

		blk := lastBlock.Uint64()
		if blk == 0 {
			if scanMode == 2 {
				remoteCli, _ := s.GetActiveRemoteClient(rc.NationId, 0)
				if remoteCli != nil {
					latestBlock, err := remoteCli.ChainGetBlockNumber()
					if err == nil && latestBlock > 0 {
						result[rc.NationId] = latestBlock
						logger.Info("📋 [Scanner] Resume nationId=%d from latest block %d (scanMode=2)", rc.NationId, latestBlock)
						continue
					} else {
						logger.Warn("⚠️  [Scanner] Cannot fetch latest block for nationId=%d: %v", rc.NationId, err)
					}
				}
			} else if scanMode == 1 {
				// Fetch max block from all embassies
				if maxCalldata, err := s.cfg.ConfigAbi.Pack("getScanBlockRange", nationIdBig); err == nil {
					if maxReceipt, err := tx_helper.SendReadTransaction("getScanBlockRange", defCli, s.cfg, configContract, embassyAddr, maxCalldata, nil); err == nil {
						if len(maxReceipt.Return()) > 0 {
							if method, ok := s.cfg.ConfigAbi.Methods["getScanBlockRange"]; ok {
								if vals, err := method.Outputs.Unpack(maxReceipt.Return()); err == nil && len(vals) > 1 {
									if maxBlk, ok := vals[1].(*big.Int); ok && maxBlk != nil && maxBlk.Uint64() > 0 {
										result[rc.NationId] = maxBlk.Uint64()
										logger.Info("📋 [Scanner] Resume nationId=%d from MAX block %d (scanMode=1)", rc.NationId, maxBlk.Uint64())
										continue
									}
								}
							}
						}
					}
				}
			}
			continue
		}
		// +1 vì blk là block đã scan xong → tiếp theo là blk+1
		resumeFrom := blk + 1
		result[rc.NationId] = resumeFrom
		logger.Info("📋 [Scanner] Resume nationId=%d from block %d (lastScanned=%d, from config contract)",
			rc.NationId, resumeFrom, blk)
	}

	return result
}
