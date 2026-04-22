package processor

// import (
// 	"fmt"
// 	"math/big"
// 	"sync"
// 	"time"

// 	pkg_com "github.com/meta-node-blockchain/meta-node/pkg/common"

// 	"github.com/meta-node-blockchain/meta-node/pkg/logger"
// 	pb_cross "github.com/meta-node-blockchain/meta-node/pkg/proto/cross_chain_proto"
// 	t_network "github.com/meta-node-blockchain/meta-node/types/network"
// 	"google.golang.org/protobuf/proto"
// )

// // SendContractCrossChain handles incoming cross-chain contract requests with caching
// func (p *SupervisorProcessor) SendContractCrossChain(req t_network.Request) error {
// 	conn := req.Connection()
// 	if conn == nil {
// 		return fmt.Errorf("connection is nil")
// 	}
// 	// Parse the request from message body (proto)
// 	protoReq := &pb_cross.ContractCrossChainRequest{}
// 	if err := proto.Unmarshal(req.Message().Body(), protoReq); err != nil {
// 		logger.Error("Failed to parse ContractCrossChainRequest", "error", err)
// 		return p.sendCrossChainError(conn, protoReq.RequestId, "failed to parse request")
// 	}
// 	requestId := protoReq.RequestId
// 	logger.Info("Received cross-chain request: requestId=%s, contract=%s", requestId, protoReq.ContractAddress)
// 	// ✅ ATOMIC: LoadOrStore đảm bảo không có race condition
// 	val, loaded := p.crossChainCache.LoadOrStore(requestId, &CrossChainCache{
// 		Connections: sync.Map{},
// 		Result:      nil,
// 		Timestamp:   time.Now(),
// 	})
// 	cache := val.(*CrossChainCache)
// 	if loaded {
// 		// Đã có cache rồi
// 		cache.mu.Lock()
// 		// Kiểm tra xem connection này đã được xử lý chưa
// 		if _, processed := cache.Connections.Load(conn); processed {
// 			cache.mu.Unlock()
// 			logger.Warn("Connection already processed for requestId=%s, ignoring duplicate", requestId)
// 			return nil
// 		}
// 		if cache.Result != nil {
// 			logger.Info("Cache hit for requestId=%s, returning cached result", requestId)
// 			cache.Connections.Store(conn, true) // Mark as processed
// 			result := cache.Result
// 			cache.mu.Unlock()
// 			if contractResp, ok := result.(*pb_cross.ContractCrossChainResponse); ok {
// 				return p.sendCrossChainResponse(conn, contractResp)
// 			}
// 			return fmt.Errorf("invalid result type in cache")
// 		}
// 		// Chưa có result, thêm connection vào danh sách chờ
// 		cache.Connections.Store(conn, false) // false = waiting for result
// 		cache.mu.Unlock()
// 		return nil
// 	}
// 	cache.Connections.Store(conn, false)
// 	go func() {
// 		// Get cluster config
// 		clusterCfg, ok := p.config.KnownClusters[protoReq.GetTargetNationId()]
// 		if !ok {
// 			logger.Error("Cluster config not found for ID: %d", protoReq.GetTargetNationId())
// 			p.sendCrossChainErrorToAll(cache, requestId, "cluster config not found")
// 			return
// 		}
// 		// Create connection client
// 		connectionIndex := uint64(time.Now().UnixNano()) % 5 // Simple load balancing
// 		client, err := p.GetOrCreateConnectionClient(
// 			fmt.Sprintf("crosschain_%d", connectionIndex),
// 			clusterCfg.ConnectionAddress,
// 		)
// 		if err != nil {
// 			logger.Error("Failed to create connection client: %v", err)
// 			p.sendCrossChainErrorToAll(cache, requestId, fmt.Sprintf("connection error: %v", err))
// 			return
// 		}
// 		// Send cross-chain request
// 		amount := new(big.Int).SetBytes(protoReq.Amount)
// 		response, err := client.SendContractCrossChain(
// 			protoReq.GetContractAddress(),
// 			protoReq.GetData(),
// 			amount,
// 			protoReq.GetRequestId(),
// 			protoReq.GetOriginalNationId(),
// 			protoReq.GetTargetNationId(),
// 		)
// 		if err != nil {
// 			logger.Error("SendContractCrossChain failed: %v", err)
// 			p.sendCrossChainErrorToAll(cache, requestId, fmt.Sprintf("send error: %v", err))
// 			return
// 		}
// 		// Verify transaction in block (không cần sign)
// 		verifyReq := &pb_cross.VerifyTransactionRequest{
// 			BlockNumber: response.BlockNumber,
// 			TxHash:      response.TxHash,
// 			FromAddress: response.FromAddress,
// 			ToAddress:   response.ToAddress,
// 			Amount:      response.Amount,
// 		}
// 		if err := p.verifyTransactionInBlock(client, response.BlockNumber, verifyReq); err != nil {
// 			logger.Error("Transaction verification failed: %v", err)
// 			p.sendCrossChainErrorToAll(cache, requestId, fmt.Sprintf("verification failed: %v", err))
// 			return
// 		}

// 		// Lưu result vào cache
// 		cache.mu.Lock()
// 		cache.Result = response // interface{} - flexible for different types
// 		cache.mu.Unlock()
// 		// Collect all connections and mark as processed
// 		var connections []t_network.Connection
// 		cache.Connections.Range(func(key, value interface{}) bool {
// 			conn := key.(t_network.Connection)
// 			connections = append(connections, conn)
// 			cache.Connections.Store(conn, true) // Mark as processed
// 			return true
// 		})
// 		// Trả về cho TẤT CẢ connections
// 		logger.Info("Broadcasting result to %d connections for requestId=%s", len(connections), requestId)
// 		for _, c := range connections {
// 			if err := p.sendCrossChainResponse(c, response); err != nil {
// 				logger.Error("Failed to send response to connection: %v", err)
// 			}
// 		}
// 	}()
// 	return nil
// }

// // sendCrossChainResponse sends a ContractCrossChainResponse to the connection
// func (p *SupervisorProcessor) sendCrossChainResponse(conn t_network.Connection, response *pb_cross.ContractCrossChainResponse) error {
// 	responseBytes, err := proto.Marshal(response)
// 	if err != nil {
// 		return fmt.Errorf("failed to marshal response: %w", err)
// 	}
// 	return p.messageSender.SendBytes(conn, pkg_com.ContractCrossChainResponse, responseBytes)
// }

// // sendCrossChainError sends an error response for a single connection
// func (p *SupervisorProcessor) sendCrossChainError(conn t_network.Connection, requestId string, errorMsg string) error {
// 	response := &pb_cross.ContractCrossChainResponse{
// 		RequestId: requestId,
// 		Status:    "error",
// 		Error:     errorMsg,
// 	}
// 	return p.sendCrossChainResponse(conn, response)
// }

// // sendCrossChainErrorToAll sends error to all connections in cache
// func (p *SupervisorProcessor) sendCrossChainErrorToAll(cache *CrossChainCache, requestId string, errorMsg string) {
// 	// Collect all connections from sync.Map
// 	var connections []t_network.Connection
// 	cache.Connections.Range(func(key, value interface{}) bool {
// 		conn := key.(t_network.Connection)
// 		connections = append(connections, conn)
// 		cache.Connections.Store(conn, true) // Mark as processed
// 		return true
// 	})

// 	errorResponse := &pb_cross.ContractCrossChainResponse{
// 		RequestId: requestId,
// 		Status:    "error",
// 		Error:     errorMsg,
// 	}

// 	for _, c := range connections {
// 		if err := p.sendCrossChainResponse(c, errorResponse); err != nil {
// 			logger.Error("Failed to send error response: %v", err)
// 		}
// 	}
// }
