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
	serviceType common.ServiceType,
	messageSender network.MessageSender,
) {
	// --- KHỞI TẠO RATE LIMITERS ---
	// Giới hạn 1,000,000 req/s, burst 100,000 (cho 100ms)
	readTxLimiter := rate.NewLimiter(rate.Limit(500000), 50000)
	// Giới hạn 200,000 req/s, burst 20,000 (cho 100ms)
	deviceKeyTxLimiter := rate.NewLimiter(rate.Limit(200000), 20000)

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
	routes[command.BlockNumber] = blockProcessor.ProcessBlockNumber
	routes[command.GetLastBlockHeader] = blockProcessor.GetLastBlockHeader
	routes[command.SendProcessedVirtualTransaction] = blockProcessor.ProcessedVirtualTransaction
	routes[command.GetLogs] = blockProcessor.GetLogs
	routes[command.GetTransactionReceipt] = blockProcessor.GetTransactionReceipt
	routes[command.GetTransactionByHash] = blockProcessor.GetTransactionByHash
	routes[command.GetChainId] = blockProcessor.GetChainId

	// TransactionsFromSubTopic:
	// - MODE_SINGLE: MASTER cũng cần route này để nhận TX từ SUB-WRITE qua TCP
	//   (SUB-WRITE gọi TxsProcessor2 → gửi đến MASTER_CONNECTION_TYPE via TCP)
	// - MODE_MULTI:  MASTER nhận TX từ Rust consensus qua UDS, không qua TCP
	//   (chỉ Sub nodes cần route này để nhận từ các node khác)
	// if serviceType != common.ServiceTypeMaster || mode == common.MODE_SINGLE {
	// 	routes[common.TransactionsFromSubTopic] = blockProcessor.TransactionsFromSubTopic
	// }



	// State attestation: all nodes receive attestations from peers for fork detection
	routes[common.StateAttestationTopic] = blockProcessor.ProcessStateAttestation

	// transaction routes
	routes[command.RemoteDeviceKeyDB] = transactionProcessor.HandleDeviceKeyRequest
	routes[command.SendTransaction] = transactionProcessor.ProcessTransactionFromClient
	routes[command.SendTransactions] = transactionProcessor.ProcessTransactionsFromClient
	routes[common.TransactionsFromSubTopic] = transactionProcessor.ProcessTransactionsFromClient

	// subscribe routes
	routes[command.SubscribeToAddress] = subscribeProcessor.ProcessSubscribeToAddress

	// --- CÁC ROUTE ĐƯỢC ÁP DỤNG RATE LIMITING ---
	routes[command.SendTransactionWithDeviceKey] = withRateLimit(deviceKeyTxLimiter, transactionProcessor.ProcessTransactionFromClientWithDeviceKey)

	// Master Node now handles API read requests directly
	routes[command.ReadTransaction] = withRateLimit(readTxLimiter, transactionProcessor.ProcessReadTransaction)
	routes[command.EstimateGas] = withRateLimit(readTxLimiter, transactionProcessor.ProcessEstimateGas)
}
