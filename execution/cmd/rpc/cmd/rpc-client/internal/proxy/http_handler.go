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
	"strconv"
	"strings"
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
		
		var nonceVal uint64
		var gotNonce bool

		if p.AppCtx != nil && p.AppCtx.ChainPool != nil {
			chainClient, err := p.AppCtx.ChainPool.Get()
			if err == nil {
				asBytes, err := chainClient.GetAccountState(addrBytes, 10*time.Second)
				if err == nil {
					var as pb.AccountState
					if err := proto.Unmarshal(asBytes, &as); err == nil {
						nonceVal = bytesToUint64(as.Nonce)
						gotNonce = true
						logger.Info("🔵 [http_handler] Sent TCP-based eth_getTransactionCount response: id=%v, address=%s, nonce=%d", id, addressStr, nonceVal)
					} else {
						logger.Warn("⚠️ [http_handler] eth_getTransactionCount unmarshal error: %v", err)
					}
				} else {
					logger.Warn("⚠️ [http_handler] eth_getTransactionCount TCP error: %v", err)
				}
			}
		}

		if gotNonce {
			resp := rpc_client.JSONRPCResponse{
				Jsonrpc: "2.0",
				Result:  fmt.Sprintf("0x%x", nonceVal),
				Id:      id,
			}
			utils.WriteJSON(w, resp)
			return
		}

		// Fallback to upstream RPC and intercept the response to ensure proper Ethereum hex encoding
		logger.Warn("⚠️ [http_handler] eth_getTransactionCount falling back to upstream C++ EVM for address=%s with intercepted capture", addressStr)
		
		rec := &responseCaptureWriter{
			ResponseWriter: w,
			body:           new(bytes.Buffer),
			headers:        make(http.Header),
			statusCode:     http.StatusOK,
		}
		
		r.Body = io.NopCloser(bytes.NewReader(body))
		p.ReverseProxy.ServeHTTP(rec, r)
		
		respBytes := rec.body.Bytes()
		
		// Copy captured headers to actual ResponseWriter, excluding Content-Length (which will be re-calculated)
		for k, vv := range rec.headers {
			if strings.ToLower(k) != "content-length" {
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
		}
		
		resultVal := gjson.GetBytes(respBytes, "result")
		if resultVal.Exists() && resultVal.Type == gjson.String {
			rawHex := resultVal.String()
			sanitizedHex := sanitizeHex(rawHex)
			
			idVal := gjson.GetBytes(respBytes, "id")
			var id interface{} = nil
			if idVal.Exists() {
				id = idVal.Value()
			}
			
			sanitizedResp := rpc_client.JSONRPCResponse{
				Jsonrpc: "2.0",
				Result:  sanitizedHex,
				Id:      id,
			}
			
			finalBytes, err := json.Marshal(sanitizedResp)
			if err == nil {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", strconv.Itoa(len(finalBytes)))
				w.WriteHeader(rec.statusCode)
				w.Write(finalBytes)
				return
			}
		}
		
		// Fallback if parsing or marshal fails, write raw response with recalculated Content-Length
		w.Header().Set("Content-Length", strconv.Itoa(len(respBytes)))
		w.WriteHeader(rec.statusCode)
		w.Write(respBytes)
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

type responseCaptureWriter struct {
	http.ResponseWriter
	body       *bytes.Buffer
	headers    http.Header
	statusCode int
}

func (w *responseCaptureWriter) Header() http.Header {
	return w.headers
}

func (w *responseCaptureWriter) Write(b []byte) (int, error) {
	return w.body.Write(b)
}

func (w *responseCaptureWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func sanitizeHex(rawHex string) string {
	rawHex = strings.ToLower(strings.TrimSpace(rawHex))
	rawHex = strings.TrimPrefix(rawHex, "0x")
	rawHex = strings.TrimPrefix(rawHex, "0x")
	sanitized := strings.TrimLeft(rawHex, "0")
	if sanitized == "" {
		return "0x0"
	}
	return "0x" + sanitized
}

func bytesToUint64(b []byte) uint64 {
	var val uint64
	for _, x := range b {
		val = (val << 8) | uint64(x)
	}
	return val
}

func (p *RpcReverseProxy) forwardAndSanitizeGetTransactionCount(rpcURL string, reqBody []byte) ([]byte, error) {
	resp, err := http.Post(rpcURL, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	resultVal := gjson.GetBytes(bodyBytes, "result")
	if resultVal.Exists() && resultVal.Type == gjson.String {
		rawHex := resultVal.String()
		sanitizedHex := sanitizeHex(rawHex)
		idVal := gjson.GetBytes(bodyBytes, "id")
		var id interface{} = nil
		if idVal.Exists() {
			id = idVal.Value()
		}
		sanitizedResp := rpc_client.JSONRPCResponse{
			Jsonrpc: "2.0",
			Result:  sanitizedHex,
			Id:      id,
		}
		return json.Marshal(sanitizedResp)
	}

	return bodyBytes, nil
}
