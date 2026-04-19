package listener

import (
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/utils/tx_helper"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/contracts/cross_chain_contract"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/processor"
	cc "github.com/meta-node-blockchain/meta-node/pkg/connection_manager/connection_client"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// scanProgressUpdate là yêu cầu cập nhật lastBlock lên config SMC sau khi gửi batch thành công
type scanProgressUpdate struct {
	chainId   uint64
	lastBlock uint64
}

// CrossChainScanner quét GetLogs từ các remote chains.
// - Dùng 1 client (localClient) đại diện embassy gửi TX lên local chain.
// - WalletPool: N ví nhỏ gửi TX song song, không đợi receipt.
// - Sau mỗi batch gửi thành công → enqueue cập nhật lastBlock lên config SMC.
type CrossChainScanner struct {
	localClient  *client_tcp.Client
	cfg          *tcp_config.ClientConfig
	processor    *processor.SupervisorProcessor
	walletPool   *client_tcp.WalletPool
	scanInterval time.Duration
	// Chan nhận yêu cầu cập nhật lastBlock — goroutine riêng xử lý tuần tự để tránh nonce conflict
	progressCh chan scanProgressUpdate
	// localBlockCh: nhận block number từ receipt watcher khi batchSubmit TX được confirm trên local chain
	localBlockCh chan uint64
	// txHashToWallet: map[txHash]walletAddress — dùng bởi receiptWatcher để MarkReady
	txHashToWallet sync.Map
}

// NewCrossChainScanner tạo scanner với 1 localClient và WalletPool.
// walletPool nil → scanner sẽ khởi tạo pool đơn giản với fromAddress của client.
func NewCrossChainScanner(
	localClient *client_tcp.Client,
	cfg *tcp_config.ClientConfig,
	proc *processor.SupervisorProcessor,
) *CrossChainScanner {
	s := &CrossChainScanner{
		localClient:  localClient,
		cfg:          cfg,
		processor:    proc,
		scanInterval: 1 * time.Second,
		progressCh:   make(chan scanProgressUpdate, 256),
	}
	return s
}

// SetWalletPool cài đặt wallet pool từ ngoài (gọi trước Start nếu muốn dùng nhiều ví).
func (s *CrossChainScanner) SetWalletPool(pool *client_tcp.WalletPool) {
	s.walletPool = pool
}

// Start khởi động:
//  1. Goroutine watcher nhận receipt → MarkReady wallet
//  2. Goroutine xử lý progressCh → gửi updateScanProgress lên config SMC
//  3. 1 goroutine scan cho mỗi remote_chain trong config
func (s *CrossChainScanner) Start() {
	// Nếu chưa có pool từ ngoài, tự tạo pool ví từ các remote_chain.
	// Seed = embassyAddr (address của embassy này) + nationId + index
	// → mỗi embassy instance derive ra virtual wallet pool RIÊNG BIỆT
	// → không bao giờ trùng FromAddress → không nonce conflict khi 3 embassy cùng submit batchSubmit.
	if s.walletPool == nil {
		const totalPoolSize = 16
		embassyAddr := common.HexToAddress(s.cfg.ParentAddress) // unique per embassy instance (từ BLS private key riêng)

		allAddrs := client_tcp.DeriveEmbassyWalletAddresses(embassyAddr, totalPoolSize)
		for j, addr := range allAddrs {
			logger.Info("🔑 [Scanner] Derived wallet embassy=%s idx=%d → %s",
				embassyAddr.Hex()[:10], j, addr.Hex())
		}

		s.walletPool = client_tcp.NewWalletPool(allAddrs)
		logger.Info("🏦 [Scanner] WalletPool initialized: %d derived wallets (embassy=%s)",
			len(allAddrs), embassyAddr.Hex())
	}

	// Receipt watcher: khi TX confirm → đánh dấu ví sẵn sàng + ghi nhận local block number
	s.localBlockCh = make(chan uint64, 100)
	s.localClient.StartWalletPoolReceiptWatcher(s.walletPool, &s.txHashToWallet, s.localBlockCh)
	// Progress updater: cập nhật lastBlock lên config SMC (tuần tự, tránh nonce conflict)
	go s.runProgressUpdater()
	if len(s.cfg.RemoteChains) == 0 {
		logger.Warn("⚠️  [Scanner] No remote_chains configured, scanner idle")
		return
	}
	// Đọc block đã scan từ config contract để resume đúng vị trí
	initialProgress := s.loadInitialScanProgress()

	for _, rc := range s.cfg.RemoteChains {
		rc := rc // capture loop variable
		connAddr := rc.ConnectionAddress
		logger.Info("🚀 [Scanner] Starting scan goroutine: %s (nationId=%d, addr=%s, resumeBlock=%d)",
			rc.Name, rc.NationId, connAddr, initialProgress[rc.NationId])
		go s.runChainScanner(rc, connAddr, initialProgress[rc.NationId])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Scan loop cho 1 remote chain
// ─────────────────────────────────────────────────────────────────────────────

func (s *CrossChainScanner) runChainScanner(rc tcp_config.RemoteChain, connAddr string, resumeBlock uint64) {
	nationIdStr := fmt.Sprintf("%d", rc.NationId)

	lastBlock := resumeBlock // Resume từ điểm đã scan hoặc 0 nếu chưa có
	if lastBlock > 0 {
		logger.Info("📌 [Scanner][%s] Resuming from block %d", rc.Name, lastBlock)
	} else {
		logger.Info("🔄 [Scanner][%s] Scan loop started from block 0", rc.Name)
	}

	var client *cc.ConnectionClient
	var err error
	var lastUpdateBlock uint64 = lastBlock
	lastUpdateTime := time.Now()

	for {
		if client == nil {
			client, err = s.getOrConnectClient(nationIdStr, connAddr)
			if err != nil {
				logger.Error("❌ [Scanner][%s] Cannot connect: %v", rc.Name, err)
				client = nil
				time.Sleep(s.scanInterval + 5*time.Second)
				continue
			}
		}

		latestBlock, errBlock := client.GetBlockNumber()
		if errBlock != nil {
			logger.Error("❌ [Scanner][%s] GetBlockNumber failed: %v", rc.Name, errBlock)
			client = nil // Đánh dấu mất kết nối để vòng lặp sau reconnect
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
				logger.Warn("⚠️  [Scanner][%s] Scan failed at block %d: %v, will retry", rc.Name, blockNum, errScan)
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
	client *cc.ConnectionClient,
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

	// 1. Lọc MessageSent từ remote (destNationId = local)
	respSent, err := client.GetLogs(
		nil,
		blockStr,
		blockStr,
		[]common.Address{contractAddr},
		[][]common.Hash{
			{messageSentTopic},
			{},                 // Topic 1: sourceNationId (any)
			{localNationTopic}, // Topic 2: destNationId = local
			{},                 // Topic 3: msgId (any)
		},
	)
	if err != nil {
		return false, fmt.Errorf("GetLogs MessageSent block=%d: %w", blockNum, err)
	}

	// 2. Lọc MessageReceived từ remote (sourceNationId = local)
	respRecv, err2 := client.GetLogs(
		nil,
		blockStr,
		blockStr,
		[]common.Address{contractAddr},
		[][]common.Hash{
			{messageReceivedTopic},
			{localNationTopic}, // Topic 1: sourceNationId = local
			{},                 // Topic 2: destNationId (any)
			{},                 // Topic 3: msgId (any)
		},
	)
	if err2 != nil {
		return false, fmt.Errorf("GetLogs MessageReceived block=%d: %w", blockNum, err2)
	}

	var allLogs []*pb.LogEntry
	if respSent != nil && respSent.Logs != nil {
		allLogs = append(allLogs, respSent.Logs...)
	}
	if respRecv != nil && respRecv.Logs != nil {
		allLogs = append(allLogs, respRecv.Logs...)
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

				for attempt := 1; attempt <= maxRetries; attempt++ {
					txHash, err = s.submitBatch(rc, chunk)
					if err == nil {
						break
					}
					logger.Warn("⚠️  [Scanner][%s] submitBatch attempt %d/%d failed txHash=%s, at block %d (chunk %d-%d/%d): %v",
						rc.Name, attempt, maxRetries, txHash.Hex(), blockNum, i, end, len(events), err)
					if attempt < maxRetries {
						time.Sleep(2 * time.Second)
					}
				}

				if err != nil {
					logger.Error("❌ [Scanner][%s] submitBatch ALL %d retries failed at block %d: %v", rc.Name, maxRetries, blockNum, err)
					// Trả về error để vòng lặp ngoài không tăng lastBlock, ép scan lại block này
					return false, fmt.Errorf("submitBatch failed after %d retries: %w", maxRetries, err)
				}
			}
			
			// Đã submit thành công tất cả các chunk cho block này
			s.enqueueProgressUpdate(rc.NationId, blockNum)
		}
	}
	return hasEvents, nil
}

// buildEmbassyEvents parse logs → []EmbassyEventInput (dùng type từ cross_chain_contract).
// MessageSent → INBOUND, MessageReceived → CONFIRMATION.
func (s *CrossChainScanner) buildEmbassyEvents(
	rc tcp_config.RemoteChain,
	logs []*pb.LogEntry,
	messageSentTopic common.Hash,
	messageReceivedTopic common.Hash,
) []cross_chain_contract.EmbassyEventInput {
	events := make([]cross_chain_contract.EmbassyEventInput, 0, len(logs))

	for _, log := range logs {
		if len(log.Topics) == 0 {
			continue
		}
		topic0 := common.BytesToHash(log.Topics[0])

		switch topic0 {
		case messageSentTopic:
			ev, err := s.buildInboundEvent(rc, log)
			if err != nil {
				logger.Warn("[Scanner][%s] buildInboundEvent failed (block=%d): %v", rc.Name, log.BlockNumber, err)
				continue
			}
			events = append(events, ev)

		case messageReceivedTopic:
			ev, err := s.buildConfirmationEvent(rc, log)
			if err != nil {
				logger.Warn("[Scanner][%s] buildConfirmationEvent failed (block=%d): %v", rc.Name, log.BlockNumber, err)
				continue
			}
			events = append(events, ev)
		}
	}
	return events
}

// buildInboundEvent chuyển MessageSent log → EmbassyEventInput{INBOUND}.
func (s *CrossChainScanner) buildInboundEvent(rc tcp_config.RemoteChain, log *pb.LogEntry) (cross_chain_contract.EmbassyEventInput, error) {
	msgSent, err := parseSentLogRaw(s.cfg, log.Data, log.Topics)
	if err != nil {
		return cross_chain_contract.EmbassyEventInput{}, fmt.Errorf("parseSentLog: %w", err)
	}

	// msgSent.MsgId đã được parseSentLogRaw parse từ Topics[3] (txHash gốc của user trên chain nguồn)
	logger.Info("[MSGID-TRACE] 📵 [2/4] SCANNER[%s] READ MessageSent: msgId=0x%x src=%v→dest=%v block=%d sender=%s\n" +
		"        ⇨ sẽ gửi INBOUND vào chain %s",
		rc.Name,
		msgSent.MsgId[:], // full 32 bytes
		msgSent.SourceNationId, msgSent.DestNationId,
		log.BlockNumber, msgSent.Sender.Hex(),
		msgSent.DestNationId,
	)

	// Đảm bảo Payload không nil để ABI pack không panic
	payload := msgSent.Payload
	if payload == nil {
		payload = []byte{}
	}

	packet := cross_chain_contract.CrossChainPacket{
		SourceNationId: msgSent.SourceNationId,
		DestNationId:   msgSent.DestNationId,
		Timestamp:      msgSent.Timestamp,
		Sender:         msgSent.Sender,
		Target:         msgSent.Target,
		Value:          msgSent.Value,
		Payload:        payload,
	}

	return cross_chain_contract.EmbassyEventInput{
		EventKind:   cross_chain_contract.EventKindInbound,
		Packet:      packet,
		BlockNumber: log.BlockNumber,
		// Dùng Confirmation.MessageId để carry msgId gốc → handler emit vào MessageReceived
		Confirmation: cross_chain_contract.ConfirmationParam{
			MessageId:  msgSent.MsgId, // ← từ struct, đã được parse từ Topics[3]
			ReturnData: []byte{},
		},
	}, nil
}

// buildConfirmationEvent chuyển MessageReceived log → EmbassyEventInput{CONFIRMATION}.
func (s *CrossChainScanner) buildConfirmationEvent(rc tcp_config.RemoteChain, log *pb.LogEntry) (cross_chain_contract.EmbassyEventInput, error) {
	msgReceived, err := parseReceivedLogRaw(s.cfg, log.Data, log.Topics)
	if err != nil {
		return cross_chain_contract.EmbassyEventInput{}, fmt.Errorf("parseReceivedLog: %w", err)
	}

	statusStr := "SUCCESS"
	if msgReceived.Status != cross_chain_contract.MessageStatusSuccess {
		statusStr = "FAILED"
	}
	msgTypeStr := "ASSET_TRANSFER"
	if msgReceived.MsgType == cross_chain_contract.MessageTypeContractCall {
		msgTypeStr = "CONTRACT_CALL"
	}

	// msgReceived.MsgId đã được parseReceivedLogRaw parse từ Topics[3]
	logger.Info("[MSGID-TRACE] 📵 [3b/4] SCANNER[%s] READ MessageReceived: msgId=0x%x src=%v→dest=%v block=%d status=%s type=%s\n" +
		"        ⇨ sẽ gửi CONFIRMATION về chain %s",
		rc.Name,
		msgReceived.MsgId[:], // full 32 bytes
		msgReceived.SourceNationId, msgReceived.DestNationId,
		log.BlockNumber, statusStr, msgTypeStr,
		msgReceived.SourceNationId,
	)

	// Đảm bảo ReturnData không nil để ABI pack không panic
	returnData := msgReceived.ReturnData
	if returnData == nil {
		returnData = []byte{}
	}

	confirmation := cross_chain_contract.ConfirmationParam{
		MessageId:         msgReceived.MsgId,  // ← từ struct, đã parse từ Topics[3]
		SourceBlockNumber: new(big.Int).SetUint64(log.BlockNumber),
		IsSuccess:         msgReceived.Status == cross_chain_contract.MessageStatusSuccess,
		ReturnData:        returnData,
		Sender:            msgReceived.Sender,
		Value:             msgReceived.Amount,
	}

	if !confirmation.IsSuccess && msgReceived.Amount != nil && msgReceived.Amount.Sign() > 0 {
		logger.Info("💰 [Scanner][%s] Confirmation FAILED — refund amount: %s",
			rc.Name, msgReceived.Amount.String())
	}

	return cross_chain_contract.EmbassyEventInput{
		EventKind:    cross_chain_contract.EventKindConfirmation,
		Confirmation: confirmation,
		BlockNumber:  log.BlockNumber,
		Packet: cross_chain_contract.CrossChainPacket{
			Payload: []byte{},
		},
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Submit lên chain qua WalletPool
// ─────────────────────────────────────────────────────────────────────────────

// submitBatch lấy ví từ WalletPool và gọi cross_chain_contract.BatchSubmit (fire-and-forget).
// Caller lưu txHash → walletAddr vào txHashToWallet map để receiptWatcher gọi MarkReady.
func (s *CrossChainScanner) submitBatch(rc tcp_config.RemoteChain, events []cross_chain_contract.EmbassyEventInput) (common.Hash, error) {
	contractAddr := common.HexToAddress(s.cfg.CrossChainContract_)
	wallet := s.walletPool.Acquire()

	txHash, err := cross_chain_contract.BatchSubmit(
		s.localClient,
		s.cfg,
		contractAddr,
		wallet.Address(),
		events,
		s.cfg.BlsPublicKey(), // BLS public key của embassy → chain verify O(1)
		nil,
	)
	if err != nil {
		s.walletPool.MarkReady(wallet.Address())
		return common.Hash{}, fmt.Errorf("submitBatch: %w", err)
	}
	s.txHashToWallet.Store(txHash, wallet.Address())
	return txHash, nil
}

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
			if blk, err := s.localClient.ChainGetBlockNumber(); err == nil {
				maxLocalBlock = blk
			} else {
				logger.Warn("⚠️ [Scanner] Cannot fetch local block for snapshot: %v", err)
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

	// Dùng embassy address (cfg.Address()) để msg.sender = embassy address lưu trong contract
	// → getScanProgress(embassyAddr, nationId) sẽ tra ra đúng block
	embassyAddr := common.HexToAddress(s.cfg.ParentAddress)
	_, err = tx_helper.SendTransaction(
		"batchUpdateScanProgress",
		s.localClient,
		s.cfg,
		configContract,
		embassyAddr,
		calldata,
		nil,
	)
	if err != nil {
		logger.Warn("[Scanner] updateScanProgressBatch TX failed chains=%v blocks=%v: %v", chainIds, lastBlocks, err)
		return
	}
	logger.Info("📋 [Scanner] ScanProgress batch updated: chainIds=%v, lastBlocks=%v, localBlock=%d (ambassador=%s)",
		chainIds, lastBlocks, localBlockNumber, embassyAddr.Hex())
}

func (s *CrossChainScanner) getOrConnectClient(nationIdStr string, connAddr string) (*cc.ConnectionClient, error) {
	return s.processor.GetConnectionManager().GetOrCreateConnectionClient(nationIdStr, connAddr)
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

	for _, rc := range s.cfg.RemoteChains {
		nationIdBig := new(big.Int).SetUint64(rc.NationId)

		calldata, err := s.cfg.ConfigAbi.Pack("getScanProgress", embassyAddr, nationIdBig)
		if err != nil {
			logger.Warn("⚠️  [Scanner] pack getScanProgress failed for nationId=%d: %v", rc.NationId, err)
			continue
		}

		receipt, err := tx_helper.SendReadTransaction(
			"getScanProgress",
			s.localClient,
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
