package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/constants"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/handlers"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/models"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/utils"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/rpc_client"
	"github.com/tidwall/gjson"

	"fmt"
	"time"
	ethCommon "github.com/ethereum/go-ethereum/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

func (p *RpcReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, releaseBody, err := ReadBodyWithLimit(r)
	if err != nil {
		releaseBody()
		logger.Error("Failed to read request body: %v", err)
		if errors.Is(err, constants.ErrRequestBodyTooLarge) {
			http.Error(w, "Request entity too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		}
		return
	}
	defer releaseBody()

	methodResult := gjson.GetBytes(body, "method")
	if !methodResult.Exists() {
		r.Body = io.NopCloser(bytes.NewReader(body))
		p.ReverseProxy.ServeHTTP(w, r)
		return
	}

	method := methodResult.String()
	id := utils.ExtractRequestID(body)

	switch method {
	case "eth_sendRawTransaction":
		rawTx := gjson.GetBytes(body, "params.0")
		if !rawTx.Exists() {
			resp := utils.MakeInvalidParamError(id, "Invalid params for sendRawTransaction")
			utils.WriteJSON(w, resp)
			return
		}
		resp := handlers.ProcessSendRawTransaction(
			p.AppCtx,
			rawTx.String(),
			id,
		)
		utils.WriteJSON(w, resp)
		return
	case "net_version":
		resp := rpc_client.JSONRPCResponse{
			Jsonrpc: "2.0",
			Result:  p.AppCtx.ClientRpc.ChainId.String(),
			Id:      id,
		}
		utils.WriteJSON(w, resp)
		return

	case "eth_estimateGas":
		callParam := gjson.GetBytes(body, "params.0")
		if !callParam.Exists() {
			logger.Info("Cannot unmarshal params for eth_estimateGas")
			resp := utils.MakeInvalidParamError(id, "Cannot unmarshal params for eth_estimateGas")
			utils.WriteJSON(w, resp)
			return
		}
		resp := handlers.HandleEstimateGasRaw(p.AppCtx, json.RawMessage(callParam.Raw), id)
		utils.WriteJSON(w, resp)
		return
	case "eth_call":
		callParam := gjson.GetBytes(body, "params.0")
		if !callParam.Exists() {
			resp := utils.MakeInvalidParamError(id, "Cannot unmarshal params for eth_call")
			utils.WriteJSON(w, resp)
			return
		}
		resp := handlers.HandleEthCallRaw(p.AppCtx, json.RawMessage(callParam.Raw), id)
		utils.WriteJSON(w, resp)
		return

	case "rpc_registerBlsKeyWithSignature":
		registerParam := gjson.GetBytes(body, "params.0")
		if !registerParam.Exists() {
			resp := utils.MakeInvalidParamError(id, "Cannot unmarshal params for rpc_registerBlsKeyWithSignature")
			utils.WriteJSON(w, resp)
			return
		}
		resp := handlers.HandleRpcRegisterBlsKeyWithSignatureRaw(
			p.AppCtx,
			json.RawMessage(registerParam.Raw),
			id,
		)
		utils.WriteJSON(w, resp)
		return

	case "eth_deployContract":
		var req models.JSONRPCRequestRaw
		if err := json.Unmarshal(body, &req); err != nil {
			resp := utils.MakeInvalidParamError(id, "Invalid JSON-RPC request")
			utils.WriteJSON(w, resp)
			return
		}
		resp := handlers.HandleDeployContract(p.AppCtx, req)
		utils.WriteJSON(w, resp)
		return

	case "rpc_pushArtifact":
		var req models.JSONRPCRequestRaw
		if err := json.Unmarshal(body, &req); err != nil {
			resp := utils.MakeInvalidParamError(id, "Invalid JSON-RPC request")
			utils.WriteJSON(w, resp)
			return
		}
		resp := handlers.HandlePushArtifact(p.AppCtx, req)
		utils.WriteJSON(w, resp)
		return

	// case "eth_getTransactionByHash":
	// 	txHash := gjson.GetBytes(body, "params.0")
	// 	if !txHash.Exists() {
	// 		logger.Warn("⚠️ [http_handler] eth_getTransactionByHash missing params: id=%v", id)
	// 		resp := utils.MakeInvalidParamError(id, "Invalid params for eth_getTransactionByHash")
	// 		utils.WriteJSON(w, resp)
	// 		return
	// 	}
	// 	// Forward request to upstream RPC server
	// 	r.Body = io.NopCloser(bytes.NewReader(body))
	// 	p.ReverseProxy.ServeHTTP(w, r)
	// 	return

	// case "eth_getTransactionReceipt":
	// 	txHash := gjson.GetBytes(body, "params.0")
	// 	if !txHash.Exists() {
	// 		logger.Warn("⚠️ [http_handler] eth_getTransactionReceipt missing params: id=%v", id)
	// 		resp := utils.MakeInvalidParamError(id, "Invalid params for eth_getTransactionReceipt")
	// 		utils.WriteJSON(w, resp)
	// 		return
	// 	}
	// 	txHashStr := txHash.String()
	// 	logger.Info("🔵 [http_handler] Received eth_getTransactionReceipt request: id=%v, txHash=%s", id, txHashStr)
	// 	// Forward request to upstream RPC server
	// 	r.Body = io.NopCloser(bytes.NewReader(body))
	// 	logger.Info("🔵 [http_handler] Forwarding eth_getTransactionReceipt to upstream: id=%v, txHash=%s", id, txHashStr)
	// 	p.ReverseProxy.ServeHTTP(w, r)
	// 	logger.Info("🔵 [http_handler] Forwarded eth_getTransactionReceipt response: id=%v, txHash=%s", id, txHashStr)
	// 	return

	case "eth_getTransactionCount":
		address := gjson.GetBytes(body, "params.0")
		if !address.Exists() {
			logger.Warn("⚠️ [http_handler] eth_getTransactionCount missing params: id=%v", id)
			resp := utils.MakeInvalidParamError(id, "Invalid params for eth_getTransactionCount")
			utils.WriteJSON(w, resp)
			return
		}
		addressStr := address.String()
		
		// Decode address to bytes
		addrBytes := ethCommon.FromHex(addressStr)
		
		if p.AppCtx != nil && p.AppCtx.ChainPool != nil {
			chainClient, err := p.AppCtx.ChainPool.Get()
			if err == nil {
				asBytes, err := chainClient.GetAccountState(addrBytes, 10*time.Second)
				if err == nil {
					var as pb.AccountState
					if err := proto.Unmarshal(asBytes, &as); err == nil {
						nonce := as.Nonce
						resp := rpc_client.JSONRPCResponse{
							Jsonrpc: "2.0",
							Result:  fmt.Sprintf("0x%x", nonce),
							Id:      id,
						}
						utils.WriteJSON(w, resp)
						logger.Info("🔵 [http_handler] Sent TCP-based eth_getTransactionCount response: id=%v, address=%s, nonce=%d", id, addressStr, nonce)
						return
					} else {
						logger.Warn("⚠️ [http_handler] eth_getTransactionCount unmarshal error: %v", err)
					}
				} else {
					logger.Warn("⚠️ [http_handler] eth_getTransactionCount TCP error: %v", err)
				}
			}
		}

		// Fallback to upstream RPC if TCP fails or AppCtx isn't ready
		logger.Warn("⚠️ [http_handler] eth_getTransactionCount falling back to upstream C++ EVM for address=%s", addressStr)
		r.Body = io.NopCloser(bytes.NewReader(body))
		p.ReverseProxy.ServeHTTP(w, r)
		return

	default:
		r.Body = io.NopCloser(bytes.NewReader(body))
		p.ReverseProxy.ServeHTTP(w, r)
		return
	}
}

func (p *RpcReverseProxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	logger.Error("ReverseProxy error for %s %s: %v", r.Method, r.URL, err)
	http.Error(w, "Upstream server error", http.StatusBadGateway)
}

func (p *RpcReverseProxy) readonlyErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	logger.Error("Readonly ReverseProxy error for %s %s: %v", r.Method, r.URL, err)
	http.Error(w, "Readonly upstream server error", http.StatusBadGateway)
}
