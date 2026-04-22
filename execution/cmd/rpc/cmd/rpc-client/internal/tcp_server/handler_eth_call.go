package tcp_server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/account_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/connection_manager/connection_client"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	robothandler "github.com/meta-node-blockchain/meta-node/pkg/robot_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/rpc_client"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

// handleEthCall - gửi ReadTransaction qua TCP trực tiếp
// Chỉ nhận proto TcpEthCallRequest (TCP-only handler)
func (srv *RpcTcpServer) handleEthCall(request t_network.Request) error {
	conn := request.Connection()
	msgID := request.Message().ID()
	body := request.Message().Body()

	// Parse proto TcpEthCallRequest
	tcpReq := &pb.TcpEthCallRequest{}
	if err := proto.Unmarshal(body, tcpReq); err != nil || len(tcpReq.To) == 0 {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32602,
			Message: "Invalid params: failed to parse TcpEthCallRequest",
		})
	}

	// Chuyển proto → JSON callParam cho handleEthCallTCP
	callObj := map[string]string{
		"to":   common.BytesToAddress(tcpReq.To).Hex(),
		"data": "0x" + hex.EncodeToString(tcpReq.Data),
	}
	callParam, _ := json.Marshal(callObj)

	fromHex := request.Message().ToAddress()
	if fromHex == (common.Address{}) {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32602,
			Message: "Missing 'from' address in eth_call",
		})
	}

	chainClient, err := srv.AppCtx.ChainPool.Get()
	if err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "Failed to get chain connection: " + err.Error(),
		})
	}

	return srv.handleEthCallTCP(conn, msgID, callParam, chainClient, fromHex)
}

// handleEthCallTCP gửi ReadTransaction qua chain TCP connection, nhận receipt về
func (srv *RpcTcpServer) handleEthCallTCP(conn t_network.Connection, msgID string, callParam json.RawMessage, chainClient *connection_client.ConnectionClient, fromAddr common.Address) error {
	// Build transaction từ callParam
	var callObj map[string]interface{}
	if err := json.Unmarshal(callParam, &callObj); err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32602,
			Message: "Invalid call object: " + err.Error(),
		})
	}

	toHex, _ := callObj["to"].(string)
	dataHex, _ := callObj["data"].(string)

	var toAddr common.Address
	if toHex != "" {
		toAddr = common.HexToAddress(toHex)
	}

	var data []byte
	if dataHex != "" {
		rawInput, _ := hex.DecodeString(dataHex[2:]) // skip 0x
		// Chain expects Data = marshalled CallData proto, not raw ABI input
		callData := transaction.NewCallData(rawInput)
		data, _ = callData.Marshal()

		// Interceptor check for account & robot on TCP eth_call
		if toAddr == common.HexToAddress(srv.AppCtx.Cfg.ContractsInterceptor[0]) {
			accountHandler, err := account_handler.GetAccountHandler(srv.AppCtx)
			if err != nil {
				return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{Code: -32603, Message: "Failed to get account handler: " + err.Error()})
			}
			result, err := accountHandler.HandleEthCall(context.Background(), rawInput, fromAddr)
			if err != nil {
				return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{Code: -32603, Message: "Account handler error: " + err.Error()})
			}
			if result != nil {
				jsonBytes, _ := json.Marshal(result)
				// Trả raw bytes trực tiếp (client nhận []byte)
				return srv.sendRpcResponse(conn, msgID, jsonBytes, nil)
			}
		}

		if toAddr == common.HexToAddress(srv.AppCtx.Cfg.ContractsInterceptor[1]) {
			robotHandler, err := robothandler.GetRobotHandler(srv.AppCtx)
			if err != nil {
				return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{Code: -32603, Message: "Failed to get robot handler: " + err.Error()})
			}
			result, err := robotHandler.HandleEthCall(context.Background(), rawInput)
			if err != nil {
				return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{Code: -32603, Message: "Robot handler error: " + err.Error()})
			}
			if result != nil {
				jsonBytes, _ := json.Marshal(result)
				// Trả raw bytes trực tiếp (client nhận []byte)
				return srv.sendRpcResponse(conn, msgID, jsonBytes, nil)
			}
		}
	}

	// Lấy nonce từ chain
	nonce, err := chainClient.GetNonce(fromAddr.Bytes(), 30*time.Second)
	if err != nil {
		logger.Warn("GetNonce failed, using 0: %v", err)
		nonce = 0
	}
	lastDeviceKey := common.HexToHash("0000000000000000000000000000000000000000000000000000000000000000")
	newDeviceKey := common.HexToHash("0000000000000000000000000000000000000000000000000000000000000000")
	
	chainId := srv.AppCtx.ClientTcp.GetClientContext().Config.ChainId
	tx := transaction.NewTransaction(
		fromAddr,
		toAddr,
		big.NewInt(0), // amount
		10_000_000,    // maxGas
		1_000_000,     // maxGasFee
		0,             // maxTimeUse
		data,          // data
		[][]byte{},    // relatedAddresses
		lastDeviceKey, // lastDeviceKey
		newDeviceKey,  // newDeviceKey
		nonce,         // nonce từ chain
		chainId,       // chainId
	)
	exists, err := srv.AppCtx.PKS.HasPrivateKey(fromAddr)
	if err == nil && exists {
		senderPkString, _ := srv.AppCtx.PKS.GetPrivateKey(fromAddr)
		keyPair := bls.NewKeyPair(common.FromHex(senderPkString))
		tx.SetSign(keyPair.PrivateKey())
	} else {
		tx.SetSign(srv.AppCtx.ClientTcp.GetClientContext().KeyPair.PrivateKey())
	}
	
	txBytes, err := tx.Marshal()
	if err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "Failed to marshal ReadTransaction: " + err.Error(),
		})
	}

	receiptBytes, err := chainClient.ReadTransaction(txBytes, 30*time.Second)
	if err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "ReadTransaction TCP error: " + err.Error(),
		})
	}

	// Parse receipt
	rcp := &receipt.Receipt{}
	if err := rcp.Unmarshal(receiptBytes); err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "Failed to unmarshal receipt: " + err.Error(),
		})
	}

	logger.Info("📋 Receipt: status=%v, from=%s, to=%s, returnLen=%d, gasUsed=%d, exception=%v",
		rcp.Status(), rcp.FromAddress().Hex(), rcp.ToAddress().Hex(),
		len(rcp.Return()), rcp.GasUsed(), rcp.Exception())

	if rcp.Status() != pb.RECEIPT_STATUS_RETURNED {
		errMsg := fmt.Sprintf("EVM error: status=%v, exception=%v", rcp.Status(), rcp.Exception())
		rpcErr := &pb.RpcError{
			Code:    3,
			Message: errMsg,
			Data:    "0x" + hex.EncodeToString(rcp.Return()),
		}
		// Decode error if supported
		rpcErr = srv.applyErrorDecoder(msgID, rpcErr, toAddr)
		return srv.sendRpcResponse(conn, msgID, nil, rpcErr)
	}

	// Trả raw bytes trực tiếp — client nhận []byte không cần hex decode
	returnData := rcp.Return()
	logger.Info("✅ TCP eth_call (TCP-direct) result: %d bytes", len(returnData))
	return srv.sendRpcResponse(conn, msgID, returnData, nil)
}

// applyErrorDecoder applies the error decoder to a standard pb.RpcError
func (srv *RpcTcpServer) applyErrorDecoder(id string, rpcErr *pb.RpcError, contractAddr common.Address) *pb.RpcError {
	if srv.AppCtx.ErrorDecoder == nil || rpcErr == nil || contractAddr == (common.Address{}) {
		return rpcErr
	}
	// Chuyển đổi pb.RpcError sang rpc_client.JSONRPCError để decode
	// Vì DecodeError hiện tại nhận rpc_client.JSONRPCResponse
	// Chúng ta có thể tối ưu bằng cách gọi trực tiếp logic decode trong ErrorDecoder sau này
	tempResp := &rpc_client.JSONRPCResponse{
		Error: &rpc_client.JSONRPCError{
			Code:    int(rpcErr.Code),
			Message: rpcErr.Message,
			Data:    rpcErr.Data,
		},
	}
	srv.AppCtx.ErrorDecoder.DecodeError(context.Background(), tempResp, contractAddr.Hex(), 0)
	// Cập nhật lại proto error
	rpcErr.Message = tempResp.Error.Message
	rpcErr.Data = tempResp.Error.Data
	rpcErr.Code = int32(tempResp.Error.Code)

	return rpcErr
}
