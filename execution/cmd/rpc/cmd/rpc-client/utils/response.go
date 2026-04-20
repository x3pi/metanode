package utils

import "github.com/meta-node-blockchain/meta-node/pkg/rpc_client"

func MakeInvalidParamError(id interface{}, message string) rpc_client.JSONRPCResponse {
	return rpc_client.JSONRPCResponse{
		Jsonrpc: "2.0",
		Error:   &rpc_client.JSONRPCError{Code: -32602, Message: message},
		Id:      id,
	}
}

func MakeInternalError(id interface{}, message string) rpc_client.JSONRPCResponse {
	return rpc_client.JSONRPCResponse{
		Jsonrpc: "2.0",
		Error:   &rpc_client.JSONRPCError{Code: -32000, Message: message},
		Id:      id,
	}
}

func MakeSuccessResponse(id interface{}, result interface{}) rpc_client.JSONRPCResponse {
	return rpc_client.JSONRPCResponse{
		Jsonrpc: "2.0",
		Result:  result,
		Id:      id,
		Error:   nil,
	}
}

func MakeAuthError(id interface{}, message string) rpc_client.JSONRPCResponse {
	return rpc_client.JSONRPCResponse{
		Jsonrpc: "2.0",
		Error:   &rpc_client.JSONRPCError{Code: -32002, Message: message},
		Id:      id,
	}
}
