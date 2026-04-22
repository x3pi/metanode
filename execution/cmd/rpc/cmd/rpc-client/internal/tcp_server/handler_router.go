package tcp_server

import (
	"encoding/json"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rpc_client"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

// HandleRequest xử lý tất cả request từ TCP client
func (srv *RpcTcpServer) HandleRequest(request t_network.Request) error {
	if request == nil || request.Message() == nil {
		return nil
	}

	cmd := request.Message().Command()

	switch cmd {
	// === Bỏ qua ===
	case "InitConnection":
		return srv.handleInitConnection(request)
	case "Ping":
		return nil
	// === Method cần xử lý đặc biệt ===
	case "eth_sendRawTransaction":
		return srv.handleSendRawTransaction(request)
	case "http_sendRawTransaction":
		return srv.handleHttpSendRawTransaction(request)
	case "eth_call":
		return srv.handleEthCall(request)
	case "rpc_registerBlsKeyWithSignature":
		return srv.handleRegisterBlsKey(request)
	// === Subscription ===
	case "eth_subscribe":
		return srv.handleEthSubscribe(request)
	case "eth_unsubscribe":
		return srv.handleEthUnsubscribe(request)
	// === Chain-direct via TCP ===
	case "eth_getTransactionReceipt":
		return srv.handleGetTransactionReceipt(request)
	case "GetNonce":
		return srv.handleGetNonce(request)
	case "GetChainId":
		return srv.handleChainIdDirect(request)
	case "GetAccountState":
		return srv.handleGetAccountState(request)
	// === Default ===
	default:
		return srv.sendRpcResponse(request.Connection(), request.Message().ID(), nil, &pb.RpcError{
			Code:    -32601,
			Message: "Method not supported",
		})
	}
}

// ===================== Helper functions =====================

// httpRespToTcpResp chuyển đổi rpc_client.JSONRPCResponse → pb.RpcResponse
func httpRespToTcpResp(httpResp rpc_client.JSONRPCResponse, msgID string) *pb.RpcResponse {
	resp := &pb.RpcResponse{
		Jsonrpc: "2.0",
		Id:      msgID,
	}
	if httpResp.Error != nil {
		resp.Error = &pb.RpcError{
			Code:    int32(httpResp.Error.Code),
			Message: httpResp.Error.Message,
			Data:    httpResp.Error.Data,
		}
	} else if httpResp.Result != nil {
		resultBytes, err := json.Marshal(httpResp.Result)
		if err != nil {
			resp.Error = &pb.RpcError{
				Code:    -32603,
				Message: "Internal error: failed to marshal result",
			}
		} else {
			resp.Result = resultBytes
		}
	}
	return resp
}

// sendTcpResponse serialize và gửi pb.RpcResponse về client
func (srv *RpcTcpServer) sendTcpResponse(conn t_network.Connection, resp *pb.RpcResponse) error {
	if resp.Error != nil {
		logger.Error("❌ TCP %s error: %s", "response", resp.Error.Message)
	}
	respBytes, err := proto.Marshal(resp)
	if err != nil {
		logger.Error("TCP sendTcpResponse: marshal error: %v", err)
		return err
	}
	return srv.messageSender.SendBytes(conn, CmdRpcResponse, respBytes)
}

// sendRpcResponse helper gửi RpcResponse về client
func (srv *RpcTcpServer) sendRpcResponse(conn t_network.Connection, id string, result []byte, rpcErr *pb.RpcError) error {
	resp := &pb.RpcResponse{
		Jsonrpc: "2.0",
		Result:  result,
		Error:   rpcErr,
		Id:      id,
	}
	respBytes, err := proto.Marshal(resp)
	if err != nil {
		logger.Error("TCP sendRpcResponse: marshal error: %v", err)
		return err
	}
	return srv.messageSender.SendBytes(conn, CmdRpcResponse, respBytes)
}

// parseParamsRaw parse body thành []json.RawMessage
func parseParamsRaw(body []byte) []json.RawMessage {
	var params []json.RawMessage
	if len(body) > 0 {
		_ = json.Unmarshal(body, &params)
	}
	return params
}

// marshalParams convert []json.RawMessage thành json.RawMessage (JSON array)
func marshalParams(params []json.RawMessage) json.RawMessage {
	b, _ := json.Marshal(params)
	return json.RawMessage(b)
}
