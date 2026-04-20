package tcp_server

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pkgCommon "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/types"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

// handleGetTransactionReceipt - lấy receipt qua TCP trực tiếp từ chain
// Nhận proto TcpHashParam (binary hash bytes)
func (srv *RpcTcpServer) handleGetTransactionReceipt(request t_network.Request) error {
	conn := request.Connection()
	msgID := request.Message().ID()
	body := request.Message().Body()

	// Parse proto TcpHashParam
	tcpReq := &pb.TcpHashParam{}
	if err := proto.Unmarshal(body, tcpReq); err != nil || len(tcpReq.Hash) == 0 {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32602,
			Message: "Invalid params: failed to parse TcpHashParam",
		})
	}

	txHashBytes := common.BytesToHash(tcpReq.Hash)
	chainClient, err := srv.AppCtx.ChainPool.Get()
	if err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "Failed to get chain connection: " + err.Error(),
		})
	}

	reqProto := &pb.GetTransactionReceiptRequest{
		TransactionHash: txHashBytes.Bytes(),
	}
	reqBytes, err := proto.Marshal(reqProto)
	if err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "Failed to marshal request: " + err.Error(),
		})
	}

	respBytes, err := chainClient.GetTransactionReceipt(reqBytes, 30*time.Second)
	if err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "GetTransactionReceipt TCP error: " + err.Error(),
		})
	}

	respProto := &pb.GetTransactionReceiptResponse{}
	if err := proto.Unmarshal(respBytes, respProto); err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "Failed to unmarshal receipt response: " + err.Error(),
		})
	}

	if respProto.Error != "" {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "Chain error: " + respProto.Error,
		})
	}

	if respProto.Receipt == nil {
		return srv.sendRpcResponse(conn, msgID, nil, nil)
	}

	receiptProtoBytes, err := proto.Marshal(respProto.Receipt)
	if err != nil {
		logger.Error("❌ handleGetTransactionReceipt: proto marshal error: %v", err)
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "Failed to marshal receipt: " + err.Error(),
		})
	}

	logger.Info("✅ TCP eth_getTransactionReceipt (TCP-direct): %s (%d bytes proto)", txHashBytes.Hex(), len(receiptProtoBytes))

	resp := &pb.RpcResponse{
		Jsonrpc: "2.0",
		Id:      msgID,
		Result:  receiptProtoBytes,
	}
	return srv.sendTcpResponse(conn, resp)
}

// handleGetNonce — lấy nonce (pending) từ chain qua TCP
func (srv *RpcTcpServer) handleGetNonce(request t_network.Request) error {
	conn := request.Connection()
	msg := request.Message()
	if msg == nil {
		return nil
	}

	body := msg.Body()
	if len(body) != common.AddressLength {
		return srv.sendRpcResponse(conn, msg.ID(), nil, &pb.RpcError{
			Code:    -32602,
			Message: "Invalid params: GetNonce body must be 20-byte address",
		})
	}

	address := common.BytesToAddress(body)
	logger.Info("🔄 TCP GetNonce from %s (addr=%s)", conn.RemoteAddrSafe(), address.Hex())

	if srv.AppCtx == nil || srv.AppCtx.ChainPool == nil {
		return nil
	}

	chainClient, err := srv.AppCtx.ChainPool.Get()
	if err != nil {
		return err
	}

	nonce, err := chainClient.GetNonce(address.Bytes(), 30*time.Second)
	if err != nil {
		return err
	}

	// Trả về byte luôn k cần bọc sendRpcResponse
	bodyResp := make([]byte, 8)
	binary.BigEndian.PutUint64(bodyResp, nonce)
	
	respMsg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command:   pkgCommon.Nonce, // Or pkgCommon.Nonce if imported
			Version:   msg.Version(),
			ToAddress: conn.Address().Bytes(),
			ID:        msg.ID(),
		},
		Body: bodyResp,
	})

	logger.Info("✅ TCP GetNonce: %s nonce=%d", address.Hex(), nonce)
	return conn.SendMessage(respMsg)
}

// receiptToRpcReceipt converts internal receipt to pb.RpcReceipt
func receiptToRpcReceipt(rcp *receipt.Receipt, tx types.Transaction) *pb.RpcReceipt {
	rpcReceipt := &pb.RpcReceipt{
		Status: fmt.Sprintf("0x%x", rcp.Status()),
	}
	if tx != nil {
		rpcReceipt.TransactionHash = tx.Hash().Hex()
	}
	// GasUsed
	rpcReceipt.GasUsed = fmt.Sprintf("0x%x", rcp.GasUsed())

	return rpcReceipt
}

// jsonToRpcReceipt convert JSON receipt map sang pb.RpcReceipt
func jsonToRpcReceipt(m map[string]interface{}) *pb.RpcReceipt {
	receipt := &pb.RpcReceipt{
		TransactionHash:   getStr(m, "transactionHash"),
		From:              getStr(m, "from"),
		To:                getStr(m, "to"),
		ContractAddress:   getStr(m, "contractAddress"),
		Status:            getStr(m, "status"),
		GasUsed:           getStr(m, "gasUsed"),
		CumulativeGasUsed: getStr(m, "cumulativeGasUsed"),
		EffectiveGasPrice: getStr(m, "effectiveGasPrice"),
		BlockHash:         getStr(m, "blockHash"),
		BlockNumber:       getStr(m, "blockNumber"),
		TransactionIndex:  getStr(m, "transactionIndex"),
		Type:              getStr(m, "type"),
		LogsBloom:         getStr(m, "logsBloom"),
	}

	// Parse logs
	if logsRaw, ok := m["logs"].([]interface{}); ok {
		for _, logRaw := range logsRaw {
			if logMap, ok := logRaw.(map[string]interface{}); ok {
				log := &pb.RpcLogEntry{
					Address:          getStr(logMap, "address"),
					Data:             getStr(logMap, "data"),
					BlockNumber:      getStr(logMap, "blockNumber"),
					TransactionHash:  getStr(logMap, "transactionHash"),
					BlockHash:        getStr(logMap, "blockHash"),
					TransactionIndex: getStr(logMap, "transactionIndex"),
					LogIndex:         getStr(logMap, "logIndex"),
					Removed:          getBoolVal(logMap, "removed"),
				}
				// Topics
				if topicsRaw, ok := logMap["topics"].([]interface{}); ok {
					for _, t := range topicsRaw {
						if ts, ok := t.(string); ok {
							log.Topics = append(log.Topics, ts)
						}
					}
				}
				receipt.Logs = append(receipt.Logs, log)
			}
		}
	}
	return receipt
}

func getStr(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getBoolVal(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}
