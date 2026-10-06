package routes

import (
	"fmt"
	"time" // Thêm import này

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor"
	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/types/network"
	"golang.org/x/time/rate"
)

func InitRoutes(
	routes map[string]func(network.Request) error,
	limits map[string]int,
	connectionProcessor *processor.ConnectionProcessor,
	blockProcessor *processor.BlockProcessor,
	stateProcessor *processor.StateProcessor,
	transactionProcessor *processor.TransactionProcessor,
	subscribeProcessor *processor.SubscribeProcessor,
	messageSender network.MessageSender,
) {
	// --- KHỞI TẠO RATE LIMITERS ---
	// Giới hạn 500,000 req/s, burst 50,000 (cho 100ms)
	readTxLimiter := rate.NewLimiter(rate.Limit(500000), 50000)

	// --- HÀM BỌC (WRAPPER) VỚI LOGIC BACKPRESSURE ---
	withRateLimit := func(limiter *rate.Limiter, next func(network.Request) error) func(network.Request) error {
		return func(r network.Request) error {
			if !limiter.Allow() {
				// 1. Gửi lại tin nhắn ServerBusy cho client
				conn := r.Connection()
				if conn != nil && conn.IsConnect() {
					_ = messageSender.SendMessage(conn, common.ServerBusy, nil)
				}

				// 2. TẠO ÁP LỰC NGƯỢC: Buộc worker xử lý request này phải dừng lại một chút.
				//    Điều này ngăn client gửi yêu cầu liên tục mà không bị ảnh hưởng.
				time.Sleep(100 * time.Millisecond)

				// 3. Trả về lỗi để network handler biết và ghi log
				return fmt.Errorf("rate limit exceeded for command: %s", r.Message().Command())
			}
			// Gọi handler gốc nếu không vượt giới hạn
			return next(r)
		}
	}

	// --- ĐỊNH NGHĨA CÁC ROUTE ---

	// connection routes
	routes[command.InitConnection] = connectionProcessor.ProcessInitConnection
	routes[command.Ping] = connectionProcessor.ProcessPing

	// state routes
	routes[command.GetAccountState] = stateProcessor.ProcessGetAccountState
	routes[command.GetNonce] = stateProcessor.ProcessGetNonce
	routes[command.GetTransactionsByBlockNumber] = stateProcessor.ProcessGetTransactionsByBlockNumber
	routes[command.GetBlockHeaderByBlockNumber] = stateProcessor.ProcessGetBlockHeaderByBlockNumber
	routes[command.GetJob] = stateProcessor.ProcessGetJob
	routes[command.SetCompleteJob] = stateProcessor.ProcessCompleteJob
	routes[command.GetTxRewardHistoryByAddress] = stateProcessor.ProcessGetTxHistoryByAddress
	routes[command.GetTxRewardHistoryByJobID] = stateProcessor.ProcessGetTxHistoryByJobID
	routes[command.GetDeviceKey] = stateProcessor.ProcessGetDeviceKey

	// block routes
	routes[command.GetBlockNumber] = blockProcessor.GetBlockNumber
	// Đã bỏ: API giao tiếp nội bộ giữa master/sub cũ
	// routes[command.BlockNumber] = blockProcessor.ProcessBlockNumber
	routes[command.GetLastBlockHeader] = blockProcessor.GetLastBlockHeader
	// Đã bỏ: API giao tiếp nội bộ giữa master/sub cũ
	// routes[command.SendProcessedVirtualTransaction] = blockProcessor.ProcessedVirtualTransaction
	routes[command.GetLogs] = blockProcessor.GetLogs
	routes[command.GetTransactionReceipt] = blockProcessor.GetTransactionReceipt
	routes[command.GetTransactionByHash] = blockProcessor.GetTransactionByHash
	routes[command.GetChainId] = blockProcessor.GetChainId



	// State attestation: all nodes receive attestations from peers for fork detection
	routes[common.StateAttestationTopic] = blockProcessor.ProcessStateAttestation

	// Legacy proto/BLS commands: unconditionally rejected
	routes[command.SendTransaction] = transactionProcessor.ProcessTransactionFromClient
	routes[command.SendTransactions] = transactionProcessor.ProcessTransactionsFromClient
	routes[command.SendTransactionWithDeviceKey] = transactionProcessor.ProcessTransactionFromClientWithDeviceKey

	// Eth-only TCP ingress
	routes[command.SendRawTransaction] = transactionProcessor.ProcessRawTransactionFromClient
	routes[command.SendRawTransactions] = transactionProcessor.ProcessRawTransactionsFromClient

	// subscribe routes
	routes[command.SubscribeToAddress] = subscribeProcessor.ProcessSubscribeToAddress

	// Master Node now handles API read requests directly

	// Master Node now handles API read requests directly
	routes[command.ReadTransaction] = withRateLimit(readTxLimiter, transactionProcessor.ProcessReadTransaction)
	routes[command.EstimateGas] = withRateLimit(readTxLimiter, transactionProcessor.ProcessEstimateGas)
}
