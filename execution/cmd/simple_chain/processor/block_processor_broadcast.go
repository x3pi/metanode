// @title processor/block_processor_broadcast.go
// @markdown processor/block_processor_broadcast.go - Broadcasting receipts, events, and logs
package processor

import (
	"context"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	eth_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract_db"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"

	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler"
	mt_filters "github.com/meta-node-blockchain/meta-node/pkg/filters"
)

// safePrefix returns s[:n] if len(s) >= n, otherwise returns s as-is.
func safePrefix(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s
}

// PrepareChainEventData prepares chain event data from block
func PrepareChainEventData(bl types.Block) *mt_filters.ChainEvent {
	header := &mt_filters.EventHeader{
		ParentHash:  bl.Header().LastBlockHash(),
		Hash:        bl.Header().Hash(),
		UncleHash:   common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
		Coinbase:    common.HexToAddress("0x0000000000000000000000000000000000000000"),
		Root:        bl.Header().AccountStatesRoot(),
		ReceiptHash: bl.Header().ReceiptRoot(),
		Bloom:       eth_types.Bloom{},
		Difficulty:  big.NewInt(1),
		Number:      big.NewInt(int64(bl.Header().BlockNumber())),
		GasLimit:    uint64(0),
		GasUsed:     uint64(0),
		Time:        bl.Header().TimeStamp(),
		Extra:       []byte{},
		MixDigest:   common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
		Nonce:       eth_types.BlockNonce{},
	}
	return &mt_filters.ChainEvent{Header: header}
}

func (bp *BlockProcessor) checkForConfigUpdates(allEventLogs []types.EventLog) {
	// Config registry address
	configHex := bp.chainState.GetConfig().CrossChain.ConfigContract
	if configHex == "" {
		return
	}
	configAddr := common.HexToAddress(configHex)

	ccHandler, err := cross_chain_handler.GetCrossChainHandler()
	if err != nil || ccHandler == nil {
		return
	}
	configABI := ccHandler.GetConfigABI()

	// Lấy Topic ID trực tiếp từ ABI
	embassyAddedTopic := configABI.Events["EmbassyAdded"].ID.Hex()
	embassyRemovedTopic := configABI.Events["EmbassyRemoved"].ID.Hex()
	chainRegisteredTopic := configABI.Events["ChainRegistered"].ID.Hex()
	chainUnregisteredTopic := configABI.Events["ChainUnregistered"].ID.Hex()

	for _, eventLog := range allEventLogs {
		if common.Address(eventLog.Address()) == configAddr {
			if len(eventLog.Topics()) > 0 {
				topic0Str := eventLog.Topics()[0]
				logger.Info("🔄 [CrossChain] Broadcast detected ConfigRegistry event: %s", topic0Str)
				topic0 := common.HexToHash(topic0Str).Hex()
				if topic0 == embassyAddedTopic || topic0 == embassyRemovedTopic ||
					topic0 == chainRegisteredTopic || topic0 == chainUnregisteredTopic {
					logger.Info("🔄 [CrossChain] Broadcast detected ConfigRegistry event: %s", topic0)
					ccHandler.InvalidateConfigCache()
					return // Just trigger invalidate once per block is enough
				}
			}
		}
	}
}

// broadcastEventsOnly broadcasts only events, not receipts
// Used by master node (no client connections)
func (bp *BlockProcessor) broadcastEventsOnly(lastBlock types.Block, allEventLogs []types.EventLog) {
	if bp.eventSystem != nil {
		chainEventData := PrepareChainEventData(lastBlock)
		syncData := PrepareSyncData(lastBlock)
		bp.eventSystem.ChainFeed.Send(*chainEventData)
		bp.eventSystem.SendStatus(syncData)
	}
	// Master node only broadcasts event logs via event system, no receipt broadcast (no client connections)
	// Receipts will be broadcast by child node when receiving block from master
	if len(allEventLogs) > 0 {
		mapEventLogs := smart_contract_db.GroupEventLogsByAddress(allEventLogs)
		go bp.checkForConfigUpdates(allEventLogs)
		go bp.BroadCastEventLogs(mapEventLogs)
	} else {
		logger.Debug("broadcastEventsOnly: no event logs to broadcast",
			"blockNumber", lastBlock.Header().BlockNumber())
	}
	logger.Debug("broadcastEventsOnly: completed (receipts will be broadcast by child nodes)",
		"blockNumber", lastBlock.Header().BlockNumber())
}

// broadcastEventsAndReceipts broadcasts both events and receipts
func (bp *BlockProcessor) broadcastEventsAndReceipts(lastBlock types.Block, allReceipts []types.Receipt, allEventLogs []types.EventLog) {

	// Add context with timeout to prevent goroutine leak
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// logger.Info("broadcastEventsAndReceipts: preparing broadcast payload",
	// 	"blockNumber", lastBlock.Header().BlockNumber(),
	// 	"receiptCount", len(allReceipts),
	// 	"eventLogCount", len(allEventLogs))

	// Check if context is cancelled
	select {
	case <-ctx.Done():
		// logger.Warn("broadcastEventsAndReceipts: context cancelled, skipping broadcast", "blockNumber", lastBlock.Header().BlockNumber())
		return
	default:
	}
	if bp.eventSystem != nil {
		chainEventData := PrepareChainEventData(lastBlock)
		syncData := PrepareSyncData(lastBlock)
		bp.eventSystem.ChainFeed.Send(*chainEventData)
		bp.eventSystem.SendStatus(syncData)
	}
	var allEthEventLogs []*eth_types.Log
	for _, rpc := range allReceipts {
		for _, eventLog := range rpc.EventLogs() {
			topics := make([]common.Hash, len(eventLog.Topics))
			for j, topicStr := range eventLog.Topics {
				topics[j] = common.BytesToHash(topicStr)
			}
			evL := &eth_types.Log{Address: common.Address(eventLog.Address), BlockNumber: lastBlock.Header().BlockNumber(), Topics: topics, Data: eventLog.Data, TxHash: rpc.TransactionHash(), BlockHash: lastBlock.Header().Hash()}
			allEthEventLogs = append(allEthEventLogs, evL)
		}
	}
	// Check context before sending
	select {
	case <-ctx.Done():
		logger.Warn("broadcastEventsAndReceipts: context cancelled before sending logs", "blockNumber", lastBlock.Header().BlockNumber())
		return
	default:
		bp.eventSystem.LogsFeed.Send(allEthEventLogs)
	}

	if len(allReceipts) > 0 {
		// ✅ Log to track receipt broadcast process (especially for child node)
		// ALL receipts (including revert THREW/HALTED) will be broadcast to clients
		revertedCount := 0
		for _, rpc := range allReceipts {
			if rpc.Status() == pb.RECEIPT_STATUS_THREW || rpc.Status() == pb.RECEIPT_STATUS_HALTED {
				revertedCount++
			}
		}
		blockNum := lastBlock.Header().BlockNumber()
		logger.Info("📤 [RECEIPT BROADCAST] broadcastEventsAndReceipts: queuing receipts for broadcast to clients",
			"blockNumber", blockNum,
			"totalReceipts", len(allReceipts),
			"revertedReceipts", revertedCount,
			"successReceipts", len(allReceipts)-revertedCount)
		// ✅ Child node will broadcast ALL receipts (including revert) to client connections
		go bp.BroadCastReceipts(allReceipts)
	} else {
		// logger.Warn("⚠️  [RECEIPT BROADCAST] broadcastEventsAndReceipts: no receipts to broadcast for block #%d",
		// 	lastBlock.Header().BlockNumber())
	}
	if len(allEventLogs) > 0 {
		mapEventLogs := smart_contract_db.GroupEventLogsByAddress(allEventLogs)
		go bp.checkForConfigUpdates(allEventLogs)
		go bp.BroadCastEventLogs(mapEventLogs)
	} else {
		logger.Debug("broadcastEventsAndReceipts: no event logs to broadcast",
			"blockNumber", lastBlock.Header().BlockNumber())
	}
}

// BroadCastReceipts broadcasts ALL receipts (including revert THREW/HALTED) to clients
// Delivery strategy:
//   - PRIORITY 1: txHash-based delivery — receipt sent to the connection that submitted the TX
//   - PRIORITY 2: toAddress-based delivery — receipt sent to the "to" address connection
//   - Skip if ProcessingType == PRE_COMMIT_NOTIFICATION
//
// NO filter for status THREW/HALTED - all receipts are broadcast
func (bp *BlockProcessor) BroadCastReceipts(receipts []types.Receipt) {
	processedCount := 0
	skippedCount := 0
	toErrCount := 0
	txHashDeliveredCount := 0

	for _, v := range receipts {
		// Skip pre-commit notifications
		if v.ProcessingType() == pb.RECEIPT_PROCESSING_TYPE_PRE_COMMIT_NOTIFICATION {
			skippedCount++
			continue
		}
		// ═══════════════════════════════════════════════════════════════
		// PRIORITY 1: txHash-based delivery (for RPC pool wallets)
		// ═══════════════════════════════════════════════════════════════
		txHash := v.TransactionHash()
		var txHashConn network.Connection

		if connVal, ok := bp.txHashConnectionMap.LoadAndDelete(txHash); ok {
			if entry, castOk := connVal.(TxHashConnEntry); castOk && entry.Conn != nil && entry.Conn.IsConnect() {
				txHashConn = entry.Conn
				b, err := v.Marshal()
				if err == nil {
					txHashDeliveredCount++
					processedCount++
					_txHash := txHash.Hex()
					go func(_conn network.Connection, _marshaledReceipt []byte, _msgID string, _txHash string) {
						respMsg := p_network.NewMessage(&pb.Message{
							Header: &pb.Header{
								Command: command.Receipt,
								ID:      _msgID,
							},
							Body: _marshaledReceipt,
						})
						sendErr := _conn.SendMessage(respMsg)
						if sendErr != nil {
							logger.Error("❌ [RECEIPT BROADCAST] txHash delivery failed: txHash=%s, err=%v",
								_txHash, sendErr)
						} else {
							logger.Info("📬 [RECEIPT BROADCAST] Delivered via txHash: %s (msgID=%s)", safePrefix(_txHash, 16), safePrefix(_msgID, 8))
						}
					}(entry.Conn, b, entry.MsgID, _txHash)
				}
			}
		}

		// ═══════════════════════════════════════════════════════════════
		// PRIORITY 2: toAddress-based delivery
		// ═══════════════════════════════════════════════════════════════
		asTo, errTo := bp.chainState.GetAccountStateDB().AccountState(v.ToAddress())
		if errTo != nil || asTo == nil {
			toErrCount++
			continue
		}

		processedCount++

		b, err := v.Marshal()
		if err != nil {
			logger.Error(
				"BroadCastReceipts: failed to marshal receipt",
				"txHash", v.TransactionHash().Hex(),
				"err", err,
			)
			continue
		}

		// Hàm helper để gửi receipt cho một địa chỉ, tránh trùng lặp
		sendToAddress := func(addr common.Address) {
			conn := bp.connectionsManager.ConnectionByTypeAndAddress(p_common.CLIENT_CONNECTION_IDX, addr)
			if conn != nil && conn.IsConnect() && conn.RemoteAddrSafe() != "" {
				// Nếu kết nối này chính là kết nối đã gửi theo txHash ở trên, ta skip để không bị nhận đúp
				if txHashConn != nil && conn.RemoteAddrSafe() == txHashConn.RemoteAddrSafe() {
					return
				}

				_txHash := v.TransactionHash().Hex()
				go func(_conn network.Connection, _marshaledReceipt []byte, _txHash string, _targetAddr common.Address) {
					sendErr := bp.messageSender.SendBytes(_conn, command.Receipt, _marshaledReceipt)
					if sendErr != nil {
						logger.Error("❌ [RECEIPT BROADCAST] Failed to send receipt: txHash=%s, target=%s, error=%v",
							_txHash, _targetAddr.Hex(), sendErr)
					} else {
						logger.Info("📬 [RECEIPT BROADCAST] Delivered via toAddress: %s (target=%s)", safePrefix(_txHash, 16), _targetAddr.Hex())
					}
				}(conn, b, _txHash, addr)
			}
		}

		// Gửi cho địa chỉ account (thường là ETH address)
		addrTo := asTo.Address()
		sendToAddress(addrTo)

		// Gửi cho địa chỉ BLS (nếu có và khác biệt)
		pubKeyTo := asTo.PublicKeyBls()
		if pubKeyTo != nil {
			blsAddr := bls.GetAddressFromPublicKey(pubKeyTo)
			if blsAddr != addrTo {
				sendToAddress(blsAddr)
			}
		}
	}
	// NOTE: All sends are fire-and-forget goroutines — we do NOT wg.Wait() here.
	// This keeps BroadCastReceipts non-blocking so the block processor can continue immediately.
	logger.Info("✅ [RECEIPT BROADCAST] BroadCastReceipts: total=%d, processed=%d, skipped=%d, txHash_delivered=%d, to_err=%d",
		len(receipts), processedCount, skippedCount, txHashDeliveredCount, toErrCount)
}

// BroadCastEventLogs broadcasts event logs to subscribers
func (bp *BlockProcessor) BroadCastEventLogs(mapEvents map[common.Address][]types.EventLog) {
	for address, eventLogs := range mapEvents {
		bp.subscribeProcessor.BroadcastLogToSubscriber(address, eventLogs)
	}
}

// addPendingReceipt saves a marshaled receipt for a disconnected client address.
// Limited to 1000 receipts per address to prevent memory leaks.
func (bp *BlockProcessor) addPendingReceipt(addr common.Address, marshaledReceipt []byte) {
	const maxPendingPerClient = 1000

	// Load or create the slice
	val, _ := bp.pendingReceipts.LoadOrStore(addr, &pendingReceiptQueue{
		receipts: make([][]byte, 0, 16),
	})
	q := val.(*pendingReceiptQueue)
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.receipts) >= maxPendingPerClient {
		logger.Warn("addPendingReceipt: queue full, dropping oldest receipt",
			"target", addr.Hex()[:10],
			"queueSize", len(q.receipts),
		)
		q.receipts = q.receipts[1:] // Drop oldest
	}
	// Make a copy of the receipt data
	cpy := make([]byte, len(marshaledReceipt))
	copy(cpy, marshaledReceipt)
	q.receipts = append(q.receipts, cpy)
	q.lastUpdated = time.Now()
}

// FlushPendingReceipts sends all pending receipts to a newly connected client.
// Called from ProcessInitConnection when a client reconnects.
func (bp *BlockProcessor) FlushPendingReceipts(addr common.Address, conn network.Connection) {
	val, ok := bp.pendingReceipts.LoadAndDelete(addr)
	if !ok {
		return
	}
	q := val.(*pendingReceiptQueue)
	q.mu.Lock()
	receipts := q.receipts
	q.receipts = nil
	q.mu.Unlock()

	if len(receipts) == 0 {
		return
	}

	logger.Info("📬 [PENDING RECEIPTS] Flushing %d pending receipts to reconnected client %s",
		len(receipts), addr.Hex()[:10])

	for _, marshaledReceipt := range receipts {
		if err := bp.messageSender.SendBytes(conn, command.Receipt, marshaledReceipt); err != nil {
			logger.Error("❌ [PENDING RECEIPTS] Failed to flush receipt to %s: %v",
				addr.Hex()[:10], err)
			break // Connection may have died again
		}
	}
}

// pendingReceiptQueue holds buffered receipts for a disconnected client
type pendingReceiptQueue struct {
	mu          sync.Mutex
	receipts    [][]byte
	lastUpdated time.Time // Track when this queue was last written to
}

// cleanupPendingReceipts periodically removes stale pending receipt entries
// for clients that disconnected and never reconnected (e.g. tps_blast one-shot clients).
// Without this, each tps_blast loop (10K unique addresses) leaks ~230MB of receipt data.
func (bp *BlockProcessor) cleanupPendingReceipts() {
	const staleTTL = 60 * time.Second          // Evict entries not updated in 60s
	ticker := time.NewTicker(30 * time.Second) // Run cleanup every 30s
	defer ticker.Stop()

	for range ticker.C {
		cutoff := time.Now().Add(-staleTTL)
		removedCount := 0
		totalCount := 0

		bp.pendingReceipts.Range(func(key, value interface{}) bool {
			totalCount++
			q, ok := value.(*pendingReceiptQueue)
			if !ok {
				bp.pendingReceipts.Delete(key)
				removedCount++
				return true
			}
			q.mu.Lock()
			stale := q.lastUpdated.Before(cutoff)
			q.mu.Unlock()
			if stale {
				bp.pendingReceipts.Delete(key)
				removedCount++
			}
			return true
		})

		if removedCount > 0 {
			logger.Info("🧹 [PENDING RECEIPTS CLEANUP] Removed %d stale entries (remaining: %d, TTL: %v)",
				removedCount, totalCount-removedCount, staleTTL)
		}
	}
}

// cleanupTxHashConnectionMap periodically removes stale txHash→connection entries
// that were never consumed by BroadCastReceipts (e.g. TX was dropped/rejected).
func (bp *BlockProcessor) cleanupTxHashConnectionMap() {
	const staleTTL = 60 * time.Second
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		cutoff := time.Now().Add(-staleTTL)
		removedCount := 0

		bp.txHashConnectionMap.Range(func(key, value interface{}) bool {
			entry, ok := value.(TxHashConnEntry)
			if !ok {
				bp.txHashConnectionMap.Delete(key)
				removedCount++
				return true
			}
			if entry.CreatedAt.Before(cutoff) {
				bp.txHashConnectionMap.Delete(key)
				removedCount++
			}
			return true
		})

		if removedCount > 0 {
			logger.Info("🧹 [TX-HASH-MAP CLEANUP] Removed %d stale entries (TTL: %v)", removedCount, staleTTL)
		}
	}
}
