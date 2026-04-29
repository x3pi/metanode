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

func (s *CrossChainScanner) enqueueProgressUpdate(chainId uint64, lastBlock uint64, localBlock uint64, isQuorum bool) {
	select {
	case s.progressCh <- scanProgressUpdate{chainId: chainId, lastBlock: lastBlock, localBlock: localBlock, isQuorum: isQuorum}:
	default:
		logger.Warn("⚠️  [Scanner] progressCh full, dropping update chainId=%d block=%d", chainId, lastBlock)
	}
}

// cần xem lại update khi pending =0 là k update cái nào hết
func (s *CrossChainScanner) runProgressUpdater() {
	logger.Info("🔄 [Scanner] Progress updater started")
	pending := make(map[uint64]uint64)      // chainId → lastBlock cao nhất
	pendingQuorums := make(map[uint64]bool) // chainId → có phải quorum không
	var maxLocalBlock uint64                // local block cao nhất (nhận từ receipt watcher)
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
		// Gom toàn bộ map → 3 slice [chainIds, lastBlocks, isQuorums]
		chainIds := make([]uint64, 0, len(pending))
		lastBlocks := make([]uint64, 0, len(pending))
		isQuorums := make([]bool, 0, len(pending))
		for cid, blk := range pending {
			chainIds = append(chainIds, cid)
			lastBlocks = append(lastBlocks, blk)
			isQuorums = append(isQuorums, pendingQuorums[cid])
		}

		if len(chainIds) > 0 || maxLocalBlock > 0 {
			s.updateScanProgressBatch(chainIds, lastBlocks, isQuorums, maxLocalBlock)
		}
		pending = make(map[uint64]uint64)
		pendingQuorums = make(map[uint64]bool)
		maxLocalBlock = 0
	}
	for {
		select {
		case upd := <-s.progressCh:
			if upd.lastBlock > pending[upd.chainId] {
				pending[upd.chainId] = upd.lastBlock
				pendingQuorums[upd.chainId] = upd.isQuorum
			} else if upd.lastBlock == pending[upd.chainId] && upd.isQuorum {
				pendingQuorums[upd.chainId] = true
			}

			if upd.localBlock > maxLocalBlock {
				maxLocalBlock = upd.localBlock
				logger.Info("[Scanner] 📌 Local block updated from progress: %d", upd.localBlock)
			}
		case <-ticker.C:
			flush()
		}
	}
}

// updateScanProgressBatch gửi 1 TX batch lên config SMC với toàn bộ chainIds + lastBlocks + isQuorums.
// Dùng SendTransactionFromWallet với from=embassyAddr (cfg.Address()) → embassy ký TX.
// on-chain msg.sender = embassy address đã đăng ký → getScanProgress query sẽ trả đúng block.
func (s *CrossChainScanner) updateScanProgressBatch(chainIds []uint64, lastBlocks []uint64, isQuorums []bool, localBlockNumber uint64) {
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

	// 1 lần Pack với toàn bộ danh sách chains + blocks + localBlockNumber + isQuorums
	calldata, err := s.cfg.ConfigAbi.Pack(
		"batchUpdateScanProgress",
		destIds,
		blksBig,
		localBlockBig,
		isQuorums,
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
func (s *CrossChainScanner) loadInitialScanProgress() map[uint64]ScanResumeState {
	result := make(map[uint64]ScanResumeState)

	configContract := common.HexToAddress(s.cfg.ConfigContract_)
	if configContract == (common.Address{}) {
		logger.Warn("⚠️  [Scanner] config_contract not set, starting scan from block 0")
		return result
	}

	embassyAddr := common.HexToAddress(s.cfg.ParentAddress)

	// Lấy cấu hình scanMode của embassy từ contract
	scanMode := uint8(0)
	for {
		defCli, _ := s.GetActiveClient(0)
		if defCli == nil {
			logger.Warn("⚠️  [Scanner] getEmbassyScanMode: no active client available, retrying...")
			time.Sleep(2 * time.Second)
			continue
		}

		modeCalldata, err := s.cfg.ConfigAbi.Pack("getEmbassyScanMode", embassyAddr)
		if err != nil {
			logger.Warn("⚠️  [Scanner] Pack getEmbassyScanMode failed: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		receipt, err := tx_helper.SendReadTransaction("getEmbassyScanMode", defCli, s.cfg, configContract, embassyAddr, modeCalldata, nil)
		if err != nil {
			logger.Warn("⚠️  [Scanner] SendReadTransaction getEmbassyScanMode failed: %v, retrying...", err)
			time.Sleep(2 * time.Second)
			continue
		}

		if len(receipt.Return()) > 0 {
			if method, ok := s.cfg.ConfigAbi.Methods["getEmbassyScanMode"]; ok {
				if vals, err := method.Outputs.Unpack(receipt.Return()); err == nil && len(vals) > 0 {
					if modeVal, ok := vals[0].(uint8); ok {
						scanMode = modeVal
					}
				} else {
					logger.Warn("⚠️  [Scanner] Unpack getEmbassyScanMode failed: %v", err)
				}
			}
		}
		break
	}
	logger.Info("📋 [Scanner] Embassy config ScanMode = %d", scanMode)

	for _, rc := range s.cfg.RemoteChains {
		nationIdBig := new(big.Int).SetUint64(rc.NationId)

		if scanMode == 1 {
			for {
				defCli, _ := s.GetActiveClient(0)
				if defCli == nil {
					logger.Warn("⚠️  [Scanner] fetch latest block: no active client available, retrying...")
					time.Sleep(2 * time.Second)
					continue
				}

				latestBlock, err := defCli.ChainGetBlockNumber()
				if err == nil && latestBlock > 0 {
					result[rc.NationId] = ScanResumeState{RemoteBlock: latestBlock, LocalBlock: 0, NeedCheckExecuted: false}
					logger.Info("📋 [Scanner] Resume nationId=%d from latest block %d (scanMode=1)", rc.NationId, latestBlock)
					break
				} else {
					logger.Warn("⚠️  [Scanner] Cannot fetch latest block for nationId=%d: %v, retrying...", rc.NationId, err)
					time.Sleep(2 * time.Second)
					continue
				}
			}
			continue
		} else if scanMode == 0 {
			// Fetch Quorum block (Block Chính) computed dynamically from contract
			for {
				defCli, _ := s.GetActiveClient(0)
				if defCli == nil {
					logger.Warn("⚠️  [Scanner] getNetworkQuorumBlock: no active client available, retrying...")
					time.Sleep(2 * time.Second)
					continue
				}

				maxCalldata, err := s.cfg.ConfigAbi.Pack("getNetworkQuorumBlock", nationIdBig)
				if err != nil {
					logger.Warn("⚠️  [Scanner] Pack getNetworkQuorumBlock failed: %v", err)
					time.Sleep(2 * time.Second)
					continue
				}

				maxReceipt, err := tx_helper.SendReadTransaction("getNetworkQuorumBlock", defCli, s.cfg, configContract, embassyAddr, maxCalldata, nil)
				if err != nil {
					logger.Warn("⚠️  [Scanner] SendReadTransaction getNetworkQuorumBlock failed: %v, retrying...", err)
					time.Sleep(2 * time.Second)
					continue
				}

				if len(maxReceipt.Return()) > 0 {
					if method, ok := s.cfg.ConfigAbi.Methods["getNetworkQuorumBlock"]; ok {
						if vals, err := method.Outputs.Unpack(maxReceipt.Return()); err == nil && len(vals) > 0 {
							var progData struct {
								RemoteBlock *big.Int `abi:"remoteBlock"`
								LocalBlock  *big.Int `abi:"localBlock"`
							}
							if err := s.cfg.ConfigAbi.UnpackIntoInterface(&progData, "getNetworkQuorumBlock", maxReceipt.Return()); err == nil {
								rBlk := uint64(0)
								if progData.RemoteBlock != nil {
									rBlk = progData.RemoteBlock.Uint64()
								}
								lBlk := uint64(0)
								if progData.LocalBlock != nil {
									lBlk = progData.LocalBlock.Uint64()
								}
								if rBlk > 0 {
									result[rc.NationId] = ScanResumeState{RemoteBlock: rBlk, LocalBlock: lBlk, NeedCheckExecuted: true}
									logger.Info("📋 [Scanner] Resume nationId=%d from Single Network Quorum Block %d (localBlock %d) (scanMode=0)", rc.NationId, rBlk, lBlk)
								}
							} else {
								logger.Warn("⚠️  [Scanner] UnpackIntoInterface getNetworkQuorumBlock failed: %v", err)
							}
						} else {
							if err != nil {
								logger.Warn("⚠️  [Scanner] Outputs.Unpack getNetworkQuorumBlock failed: %v", err)
							}
						}
					}
				}
				break
			}
		}
	}

	if scanMode != 0 {
		logger.Info("🔄 [Scanner] Resetting scanMode from %d to 0 for embassy %s", scanMode, embassyAddr.Hex())
		for {
			defCli, _ := s.GetActiveClient(0)
			if defCli == nil {
				logger.Warn("⚠️  [Scanner] setEmbassyScanMode: no active client available, retrying...")
				time.Sleep(2 * time.Second)
				continue
			}

			resetCalldata, err := s.cfg.ConfigAbi.Pack("setEmbassyScanMode", embassyAddr, uint8(0))
			if err != nil {
				logger.Warn("⚠️  [Scanner] Failed to pack setEmbassyScanMode: %v", err)
				time.Sleep(2 * time.Second)
				continue
			}

			_, errTx := tx_helper.SendTransaction(
				"setEmbassyScanMode",
				defCli,
				s.cfg,
				configContract,
				embassyAddr,
				resetCalldata,
				nil,
			)
			if errTx != nil {
				logger.Warn("⚠️  [Scanner] Failed to reset scanMode to 0: %v, retrying...", errTx)
				time.Sleep(2 * time.Second)
				continue
			}
			logger.Info("✅ [Scanner] Successfully reset scanMode to 0")
			break
		}
	}

	return result
}
