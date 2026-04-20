package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	_ "net/http/pprof"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ethCommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gorilla/websocket"
	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/file_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	"github.com/meta-node-blockchain/meta-node/pkg/rpc_client"

	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/semaphore"
)

// --- Globals ---

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  65536, // 64KB cho high throughput
	WriteBufferSize: 65536, // 64KB cho high throughput
}

const (
	maxRequestBodyBytes  = 8 << 20 // 8MB
	maxInFlightBodyBytes = int64(5 << 30)
)

var requestBufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 16*1024)
		return &buf
	},
}

var hexDecodePool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 64*1024)
		return &buf
	},
}

var bodyMemLimiter = semaphore.NewWeighted(maxInFlightBodyBytes)
var currentBodyBytes atomic.Int64
var currentBodyRequests atomic.Int64
var bodyUsageBucket atomic.Int64
var peakBodyBytes atomic.Int64
var cumulativeBodyBytes atomic.Int64
var cumulativeBodyCount atomic.Int64
var defaultLogsDir string

func acquireBodyBytes(n int) {
	if n <= 0 {
		return
	}
	if err := bodyMemLimiter.Acquire(context.Background(), int64(n)); err != nil {
		log.Fatalf("failed to acquire body memory: %v", err)
	}
	total := currentBodyBytes.Add(int64(n))
	updatePeakBodyBytes(total)
	maybeLogBodyUsage(total)
}

func releaseBodyBytes(n int) {
	if n <= 0 {
		return
	}
	bodyMemLimiter.Release(int64(n))
	total := currentBodyBytes.Add(-int64(n))
	if total < 0 {
		total = 0
		currentBodyBytes.Store(0)
	}
	maybeLogBodyUsage(total)
}

func updatePeakBodyBytes(total int64) {
	for {
		peak := peakBodyBytes.Load()
		if total <= peak {
			return
		}
		if peakBodyBytes.CompareAndSwap(peak, total) {
			logger.Info("Đỉnh mới bộ nhớ request đang giữ: %.2f MB", float64(total)/1024/1024)
			return
		}
	}
}

func maybeLogBodyUsage(total int64) {
	if maxInFlightBodyBytes <= 0 {
		return
	}
	percent := int64(0)
	if total > 0 {
		percent = total * 100 / maxInFlightBodyBytes
	}
	bucket := percent / 10
	for {
		prev := bodyUsageBucket.Load()
		if bucket == prev {
			break
		}
		if bodyUsageBucket.CompareAndSwap(prev, bucket) {
			avg := int64(0)
			if cnt := cumulativeBodyCount.Load(); cnt > 0 {
				avg = cumulativeBodyBytes.Load() / cnt
			}
			logger.Info("Bộ nhớ request đang giữ: %.2f MB (%d%%), số request: %d, trung bình: %.2f MB", float64(total)/1024/1024, percent, currentBodyRequests.Load(), float64(avg)/1024/1024)
			break
		}
	}
}

// --- Structs ---

type RpcReverseProxy struct {
	ReverseProxy         *httputil.ReverseProxy
	ReadonlyReverseProxy *httputil.ReverseProxy
	ClientRpc            *rpc_client.ClientRPC
	PKS                  *PrivateKeyStore
	NodeBlsPrivateKey    common.PrivateKey
	NodeBlsPublicKey     common.PublicKey
	ReadonlyWSSServerURL string
	Cfg                  *Config
	TcpCfg               *tcp_config.ClientConfig
	CLientTcp            *client_tcp.Client
}

type JSONRPCRequestRaw struct {
	Jsonrpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Id      interface{}     `json:"id"`
}

type Config struct {
	RPCServerURL         string   `json:"rpc_server_url"`
	WSSServerURL         string   `json:"wss_server_url"`
	ReadonlyRPCServerURL string   `json:"readonly_rpc_server_url"`
	ReadonlyWSSServerURL string   `json:"readonly_wss_server_url"`
	PrivateKey           string   `json:"private_key"`
	ServerPort           string   `json:"server_port"`
	HTTPSPort            string   `json:"https_port"`
	CertFile             string   `json:"cert_file"`
	KeyFile              string   `json:"key_file"`
	ChainId              *big.Int `json:"chain_id"`
	MasterPassword       string   `json:"master_password"`
	AppPepper            string   `json:"app_pepper"`
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	body       *bytes.Buffer
}

type RegisterBlsKeyParams struct {
	Address       string `json:"address"`
	BlsPrivateKey string `json:"blsPrivateKey"`
	Timestamp     string `json:"timestamp"`
	Signature     string `json:"signature"`
}

type callObjectSchema struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Data  string `json:"data"`
	Input string `json:"input"`
}

// --- RpcReverseProxy Methods ---

func (p *RpcReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Giảm logging để tăng hiệu suất - chỉ log khi có lỗi
	body, releaseBody, err := readBodyWithLimit(r)
	if err != nil {
		releaseBody()
		logger.Error("Failed to read request body: %v", err)
		if errors.Is(err, errRequestBodyTooLarge) {
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
	id := extractRequestID(body)

	switch method {
	case "eth_sendRawTransaction":
		rawTx := gjson.GetBytes(body, "params.0")
		if !rawTx.Exists() {
			resp := makeInvalidParamError(id, "Invalid params for sendRawTransaction")
			writeJSON(w, resp)
			return
		}

		resp := processSendRawTransaction(p, rawTx.String(), id)
		writeJSON(w, resp)
		return
	case "net_version":
		resp := rpc_client.JSONRPCResponse{
			Jsonrpc: "2.0",
			Result:  p.ClientRpc.ChainId.String(),
			Id:      id,
		}
		writeJSON(w, resp)
		return
	case "eth_estimateGas":
		callParam := gjson.GetBytes(body, "params.0")
		if !callParam.Exists() {
			resp := makeInvalidParamError(id, "Cannot unmarshal params for eth_estimateGas")
			writeJSON(w, resp)
			return
		}
		resp := handleEstimateGasRaw(p, json.RawMessage(callParam.Raw), id)
		writeJSON(w, resp)
		return
	case "eth_call":
		callParam := gjson.GetBytes(body, "params.0")
		if !callParam.Exists() {
			resp := makeInvalidParamError(id, "Cannot unmarshal params for eth_call")
			writeJSON(w, resp)
			return
		}
		resp := handleEthCallRaw(p, json.RawMessage(callParam.Raw), id)
		writeJSON(w, resp)
		return
	case "rpc_registerBlsKeyWithSignature":
		registerParam := gjson.GetBytes(body, "params.0")
		if !registerParam.Exists() {
			resp := makeInvalidParamError(id, "Cannot unmarshal params or params array is empty for rpc_registerBlsKeyWithSignature")
			writeJSON(w, resp)
			return
		}
		resp := handleRpcRegisterBlsKeyWithSignatureRaw(p, json.RawMessage(registerParam.Raw), id)
		writeJSON(w, resp)
		return
	default:
		r.Body = io.NopCloser(bytes.NewReader(body))
		p.ReverseProxy.ServeHTTP(w, r)
		return
	}
}

var errRequestBodyTooLarge = errors.New("request body exceeded limit")

func readBodyWithLimit(r *http.Request) ([]byte, func(), error) {
	bufPtr := requestBufferPool.Get().(*[]byte)
	buf := *bufPtr

	held := 0
	if cap(buf) > 0 {
		acquireBodyBytes(cap(buf))
		held += cap(buf)
	}

	requestCounted := false
	var once sync.Once
	release := func() {
		once.Do(func() {
			releaseBodyBytes(held)
			if requestCounted {
				currentBodyRequests.Add(-1)
			}
			if cap(buf) > 256*1024 {
				buf = make([]byte, 0, 16*1024)
			} else {
				buf = buf[:0]
			}
			*bufPtr = buf
			requestBufferPool.Put(bufPtr)
		})
	}

	for {
		if len(buf) == cap(buf) {
			if cap(buf) >= maxRequestBodyBytes {
				release()
				return nil, func() {}, errRequestBodyTooLarge
			}
			oldCap := cap(buf)
			newCap := oldCap * 2
			if newCap == 0 {
				newCap = 4096
			}
			if newCap > maxRequestBodyBytes {
				newCap = maxRequestBodyBytes
			}
			acquireBodyBytes(newCap)
			held += newCap
			newBuf := make([]byte, len(buf), newCap)
			copy(newBuf, buf)
			releaseBodyBytes(oldCap)
			held -= oldCap
			buf = newBuf
		}
		readSize := cap(buf) - len(buf)
		tmp := buf[len(buf):cap(buf)]
		n, err := r.Body.Read(tmp)
		buf = buf[:len(buf)+n]
		if err != nil {
			if err == io.EOF {
				break
			}
			release()
			return nil, func() {}, err
		}
		if n == 0 && readSize == 0 {
			break
		}
	}

	currentBodyRequests.Add(1)
	requestCounted = true
	size := len(buf)
	cumulativeBodyBytes.Add(int64(size))
	cumulativeBodyCount.Add(1)
	maybeLogBodyUsage(currentBodyBytes.Load())
	return buf[:len(buf)], release, nil
}

func (p *RpcReverseProxy) serveWebSocket(w http.ResponseWriter, r *http.Request, targetURLStr string) {
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("WebSocket upgrade failed for %s: %v", r.RemoteAddr, err)
		return
	}
	// Giảm logging để tăng hiệu suất
	defer func() {
		clientConn.Close()
	}()

	var clientWriteMutex sync.Mutex

	safeWriteJSON := func(v interface{}) error {
		clientWriteMutex.Lock()
		defer clientWriteMutex.Unlock()
		if err := clientConn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			logger.Warn("Error setting write deadline for clientConn: %v", err)
		}
		err := clientConn.WriteJSON(v)
		_ = clientConn.SetWriteDeadline(time.Time{})
		return err
	}

	safeWriteMessage := func(messageType int, data []byte) error {
		clientWriteMutex.Lock()
		defer clientWriteMutex.Unlock()
		if err := clientConn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			logger.Warn("Error setting write deadline for clientConn: %v", err)
		}
		err := clientConn.WriteMessage(messageType, data)
		_ = clientConn.SetWriteDeadline(time.Time{})
		return err
	}

	safeWriteCloseMessage := func(closeCode int, text string) error {
		clientWriteMutex.Lock()
		defer clientWriteMutex.Unlock()
		if err := clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			logger.Warn("Error setting write deadline for clientConn (close): %v", err)
		}
		msg := websocket.FormatCloseMessage(closeCode, text)
		err := clientConn.WriteMessage(websocket.CloseMessage, msg)
		_ = clientConn.SetWriteDeadline(time.Time{})
		return err
	}

	if targetURLStr == "" {
		errMsg := "Target WebSocket URL is not configured"
		logger.Error("%s for client %s", errMsg, clientConn.RemoteAddr())
		_ = safeWriteCloseMessage(websocket.CloseInternalServerErr, errMsg)
		return
	}
	// Giảm logging để tăng hiệu suất

	targetHeaders := make(http.Header)
	if origin := r.Header.Get("Origin"); origin != "" {
		targetHeaders.Set("Origin", origin)
	}

	// Tạo custom dialer cho WebSocket với connection pooling
	customDialer := &websocket.Dialer{
		HandshakeTimeout:  15 * time.Second,
		ReadBufferSize:    65536, // 64KB
		WriteBufferSize:   65536, // 64KB
		EnableCompression: false, // Tắt compression để tăng tốc
		NetDialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	targetConn, resp, err := customDialer.Dial(targetURLStr, targetHeaders)
	if err != nil {
		errMsg := fmt.Sprintf("Could not connect to target WebSocket %s", targetURLStr)
		statusCode := websocket.CloseGoingAway
		fullErrMsgLog := fmt.Sprintf("%s for client %s: %v", errMsg, clientConn.RemoteAddr(), err)
		fullErrMsgClient := fmt.Sprintf("%s: %v", errMsg, err)

		if resp != nil {
			defer resp.Body.Close()
			bodyBytes, _ := io.ReadAll(resp.Body)
			errMsg = fmt.Sprintf("Target WebSocket handshake failed with HTTP status %s", resp.Status)
			fullErrMsgLog = fmt.Sprintf("%s for client %s. Response Body: %s", errMsg, clientConn.RemoteAddr(), string(bodyBytes))
			fullErrMsgClient = fmt.Sprintf("%s. Server responded with: %s", errMsg, string(bodyBytes))

			if resp.StatusCode == http.StatusOK {
				logger.Error("Handshake Error: Target server responded with 200 OK instead of 101 Switching Protocols. Ensure WSS URL path is correct (e.g., includes /ws if required).")
				fullErrMsgClient = "WebSocket handshake error: Target server returned HTTP 200 OK, not a WebSocket upgrade. Check WSS URL path in config."
				statusCode = websocket.ClosePolicyViolation
			} else if resp.StatusCode >= http.StatusInternalServerError {
				statusCode = websocket.CloseInternalServerErr
			} else if resp.StatusCode >= http.StatusBadRequest && resp.StatusCode < http.StatusInternalServerError {
				statusCode = websocket.ClosePolicyViolation
			}
		}
		logger.Error(fullErrMsgLog)
		_ = safeWriteCloseMessage(statusCode, fullErrMsgClient)
		return
	}
	// Giảm logging để tăng hiệu suất - chỉ log khi có lỗi
	defer func() {
		targetConn.Close()
	}()

	errChan := make(chan error, 2)
	quit := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		// Giảm logging để tăng hiệu suất
		for {
			var req JSONRPCRequestRaw
			if err := clientConn.SetReadDeadline(time.Now().Add(180 * time.Second)); err != nil {
				logger.Warn("[WS GR1] Error setting read deadline for clientConn: %v", err)
			}
			readErr := clientConn.ReadJSON(&req)
			_ = clientConn.SetReadDeadline(time.Time{})

			if readErr != nil {
				if !websocket.IsCloseError(readErr, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNoStatusReceived) && readErr != io.EOF {
					logger.Error("[WS GR1] Error reading JSON from client %s: %v", clientConn.RemoteAddr(), readErr)
					select {
					case errChan <- fmt.Errorf("client read error: %w", readErr):
					case <-quit:
					}
				}
				return
			}
			// Giảm logging - không log mỗi message để tăng hiệu suất
			var rpcResp *rpc_client.JSONRPCResponse
			switch req.Method {
			case "eth_sendRawTransaction":
				r := handleSendRawTransaction(p, req)
				rpcResp = &r
			case "net_version":
				r := rpc_client.JSONRPCResponse{
					Jsonrpc: "2.0",
					Result:  p.ClientRpc.ChainId.String(),
					Id:      req.Id,
				}
				rpcResp = &r
			case "eth_estimateGas":
				r := handleEstimateGas(p, req)
				rpcResp = &r
			case "eth_call":
				r := handleEthCall(p, req)
				rpcResp = &r
			case "rpc_registerBlsKeyWithSignature":
				r := handleRpcRegisterBlsKeyWithSignature(p, req)
				rpcResp = &r
			}

			if rpcResp != nil {
				if err := safeWriteJSON(rpcResp); err != nil {
					logger.Error("[WS GR1] Error writing RPC response to client %s: %v", clientConn.RemoteAddr(), err)
					select {
					case errChan <- fmt.Errorf("client write error (rpcResp): %w", err):
					case <-quit:
					}
					return
				}
			} else {
				// Giảm logging để tăng hiệu suất
				if err := targetConn.SetWriteDeadline(time.Now().Add(180 * time.Second)); err != nil {
					logger.Warn("[WS GR1] Error setting write deadline for targetConn: %v", err)
				}
				writeErr := targetConn.WriteJSON(req)
				_ = targetConn.SetWriteDeadline(time.Time{})

				if writeErr != nil {
					logger.Error("[WS GR1] Error writing JSON to target for client %s: %v", clientConn.RemoteAddr(), writeErr)
					select {
					case errChan <- fmt.Errorf("target write error: %w", writeErr):
					case <-quit:
					}
					return
				}
			}

			select {
			case <-quit:
				logger.Debug("[WS GR1] Quit by signal for %s", clientConn.RemoteAddr())
				return
			default:
			}
		}
	}()

	go func() {
		defer wg.Done()
		// Giảm logging để tăng hiệu suất
		for {
			if err := targetConn.SetReadDeadline(time.Now().Add(180 * time.Second)); err != nil {
				logger.Warn("[WS GR2] Error setting read deadline for targetConn: %v", err)
			}
			messageType, message, readErr := targetConn.ReadMessage()
			_ = targetConn.SetReadDeadline(time.Time{})

			if readErr != nil {
				if !websocket.IsCloseError(readErr, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNoStatusReceived) && readErr != io.EOF {
					logger.Error("[WS GR2] Error reading message from target for %s: %v", clientConn.RemoteAddr(), readErr)
					select {
					case errChan <- fmt.Errorf("target read error: %w", readErr):
					case <-quit:
					}
				}
				return
			}
			// Giảm logging để tăng hiệu suất
			if err := safeWriteMessage(messageType, message); err != nil {
				logger.Error("[WS GR2] Error writing message to client %s: %v", clientConn.RemoteAddr(), err)
				select {
				case errChan <- fmt.Errorf("client write error: %w", err):
				case <-quit:
				}
				return
			}

			select {
			case <-quit:
				logger.Debug("[WS GR2] Quit by signal for %s", clientConn.RemoteAddr())
				return
			default:
			}
		}
	}()

	var finalError error
	select {
	case err := <-errChan:
		finalError = err
	case err := <-errChan:
		if finalError == nil {
			finalError = err
		}
	case <-r.Context().Done():
		finalError = r.Context().Err()
	}

	close(quit)

	if finalError != nil {
		logger.Error("WebSocket proxying ending due to error/context done for %s: %v", clientConn.RemoteAddr(), finalError)
		if !websocket.IsCloseError(finalError,
			websocket.CloseNormalClosure, websocket.CloseGoingAway,
			websocket.CloseNoStatusReceived, websocket.CloseAbnormalClosure) &&
			finalError != io.EOF &&
			!strings.Contains(finalError.Error(), "client read error") &&
			!strings.Contains(finalError.Error(), "client write error") {
			_ = safeWriteCloseMessage(websocket.CloseInternalServerErr, "Proxy error or context done")
		}
	} else {
		_ = safeWriteCloseMessage(websocket.CloseNormalClosure, "Connection closing normally")
	}

	waitTimeout := 5 * time.Second
	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		logger.Info("All WebSocket goroutines finished gracefully for %s.", clientConn.RemoteAddr())
	case <-time.After(waitTimeout):
		logger.Warn("Timeout waiting for WebSocket goroutines to finish after quit signal for %s.", clientConn.RemoteAddr())
	}
	logger.Info("serveWebSocket finished for %s", clientConn.RemoteAddr())
}

// --- RPC Method Handlers ---

func makeInvalidParamError(id interface{}, message string) rpc_client.JSONRPCResponse {
	return rpc_client.JSONRPCResponse{
		Jsonrpc: "2.0",
		Error:   &rpc_client.JSONRPCError{Code: -32602, Message: message},
		Id:      id,
	}
}

func makeInternalError(id interface{}, message string) rpc_client.JSONRPCResponse {
	return rpc_client.JSONRPCResponse{
		Jsonrpc: "2.0",
		Error:   &rpc_client.JSONRPCError{Code: -32000, Message: message},
		Id:      id,
	}
}

func makeSuccessResponse(id interface{}, result interface{}) rpc_client.JSONRPCResponse {
	return rpc_client.JSONRPCResponse{
		Jsonrpc: "2.0",
		Result:  result,
		Id:      id,
		Error:   nil,
	}
}

func makeAuthError(id interface{}, message string) rpc_client.JSONRPCResponse {
	return rpc_client.JSONRPCResponse{
		Jsonrpc: "2.0",
		Error:   &rpc_client.JSONRPCError{Code: -32002, Message: message},
		Id:      id,
	}
}

func handleRpcRegisterBlsKeyWithSignature(p *RpcReverseProxy, req JSONRPCRequestRaw) rpc_client.JSONRPCResponse {
	var rawParams []json.RawMessage
	if err := json.Unmarshal(req.Params, &rawParams); err != nil || len(rawParams) == 0 {
		return makeInvalidParamError(req.Id, "Cannot unmarshal params or params array is empty for rpc_registerBlsKeyWithSignature")
	}

	var params RegisterBlsKeyParams
	if err := json.Unmarshal(rawParams[0], &params); err != nil {
		return makeInvalidParamError(req.Id, "Invalid parameters for rpc_registerBlsKeyWithSignature: "+err.Error())
	}

	return processRegisterBlsKeyParams(p, params, req.Id)
}

func handleRpcRegisterBlsKeyWithSignatureRaw(p *RpcReverseProxy, param json.RawMessage, id interface{}) rpc_client.JSONRPCResponse {
	var params RegisterBlsKeyParams
	if err := json.Unmarshal(param, &params); err != nil {
		return makeInvalidParamError(id, "Invalid parameters for rpc_registerBlsKeyWithSignature: "+err.Error())
	}

	return processRegisterBlsKeyParams(p, params, id)
}

func processRegisterBlsKeyParams(p *RpcReverseProxy, params RegisterBlsKeyParams, id interface{}) rpc_client.JSONRPCResponse {
	if !ethCommon.IsHexAddress(params.Address) {
		return makeInvalidParamError(id, "Invalid Ethereum address format.")
	}
	signerAddress := ethCommon.HexToAddress(params.Address)

	if !strings.HasPrefix(params.BlsPrivateKey, "0x") || len(params.BlsPrivateKey) != 66 {
		return makeInvalidParamError(id, "Invalid BLS private key format. Expected 0x prefixed 32-byte hex string.")
	}
	blsPrivKeyBytes, releaseBls, err := decodeHexPooled(params.BlsPrivateKey)
	if err != nil || len(blsPrivKeyBytes) != 32 {
		releaseBls()
		return makeInvalidParamError(id, "Invalid BLS private key hex data.")
	}
	releaseBls()

	clientTimestamp, err := time.Parse(time.RFC3339Nano, params.Timestamp)
	if err != nil {
		clientTimestamp, err = time.Parse(time.RFC3339, params.Timestamp)
		if err != nil {
			return makeInvalidParamError(id, "Invalid timestamp format. Expected ISO 8601.")
		}
	}

	if time.Since(clientTimestamp).Abs() > 2*time.Minute {
		return makeAuthError(id, "Timestamp is too old or in the future.")
	}

	messageToVerify := fmt.Sprintf("BLS Data: %s\nTimestamp: %s", params.BlsPrivateKey, params.Timestamp)
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(messageToVerify), messageToVerify)
	messageHash := crypto.Keccak256Hash([]byte(prefixedMessage))

	signatureBytes, releaseSig, err := decodeHexPooled(params.Signature)
	if err != nil {
		releaseSig()
		return makeInvalidParamError(id, "Invalid signature hex data: "+err.Error())
	}
	defer releaseSig()
	if len(signatureBytes) == 65 && (signatureBytes[64] == 27 || signatureBytes[64] == 28) {
		signatureBytes[64] -= 27
	}

	recoveredSigPubKeyBytes, err := crypto.Ecrecover(messageHash.Bytes(), signatureBytes)
	if err != nil {
		return makeAuthError(id, "Signature verification failed: could not recover public key.")
	}
	unmarshaledRecPubKey, err := crypto.UnmarshalPubkey(recoveredSigPubKeyBytes)
	if err != nil {
		return makeAuthError(id, "Signature verification failed: could not unmarshal recovered public key.")
	}
	recoveredAddress := crypto.PubkeyToAddress(*unmarshaledRecPubKey)

	if !bytes.Equal(recoveredAddress.Bytes(), signerAddress.Bytes()) {
		return makeAuthError(id, "Signature verification failed: address mismatch.")
	}

	if p.PKS == nil {
		return makeInternalError(id, "Internal server error: Private key store not available.")
	}

	err = p.PKS.SetPrivateKey(signerAddress, params.BlsPrivateKey)
	if err != nil {
		return makeInternalError(id, fmt.Sprintf("Failed to store BLS private key: %v", err))
	}

	return makeSuccessResponse(id, "BLS private key successfully registered.")
}

func handleEthCall(p *RpcReverseProxy, req JSONRPCRequestRaw) rpc_client.JSONRPCResponse {
	var callParamsList []json.RawMessage
	if err := json.Unmarshal(req.Params, &callParamsList); err != nil || len(callParamsList) == 0 {
		return makeInvalidParamError(req.Id, "Cannot unmarshal params for eth_call")
	}
	return processEthCallParams(p, req.Id, callParamsList[0])
}

func handleEthCallRaw(p *RpcReverseProxy, callParam json.RawMessage, id interface{}) rpc_client.JSONRPCResponse {
	return processEthCallParams(p, id, callParam)
}

func processEthCallParams(p *RpcReverseProxy, id interface{}, callObjectRaw json.RawMessage) rpc_client.JSONRPCResponse {
	fromAddress, toAddress, hasTo, payload, err := decodeCallObject(callObjectRaw)
	if err != nil {
		return makeInvalidParamError(id, "Invalid eth_call parameter")
	}

	var bTx []byte
	var buildErr error

	if !hasTo {
		bTx, buildErr = p.ClientRpc.BuildDeployTransaction(payload, fromAddress)
	} else {
		bTx, buildErr = p.ClientRpc.BuildCallTransaction(payload, toAddress, fromAddress)
	}

	if buildErr != nil {
		return makeInternalError(id, "Failed to build transaction for eth_call")
	}

	rs := p.ClientRpc.SendCallTransaction(bTx)
	rs.Id = id
	return rs
}

func handleEstimateGas(p *RpcReverseProxy, req JSONRPCRequestRaw) rpc_client.JSONRPCResponse {
	var callParamsList []json.RawMessage
	if err := json.Unmarshal(req.Params, &callParamsList); err != nil || len(callParamsList) == 0 {
		return makeInvalidParamError(req.Id, "Cannot unmarshal params for eth_estimateGas")
	}
	return handleEstimateGasRaw(p, callParamsList[0], req.Id)
}

func handleEstimateGasRaw(p *RpcReverseProxy, callParam json.RawMessage, id interface{}) rpc_client.JSONRPCResponse {
	fromAddress, toAddress, hasTo, payload, err := decodeCallObject(callParam)
	if err != nil {
		return makeInvalidParamError(id, "Invalid eth_estimateGas parameter")
	}

	var bTx []byte
	var buildErr error

	if !hasTo {
		bTx, buildErr = p.ClientRpc.BuildDeployTransaction(payload, fromAddress)
	} else {
		bTx, buildErr = p.ClientRpc.BuildCallTransaction(payload, toAddress, fromAddress)
	}

	if buildErr != nil {
		return makeInternalError(id, "Failed to build transaction for estimateGas")
	}

	rs := p.ClientRpc.SendEstimateGas(bTx)
	rs.Id = id
	return rs
}

func decodeCallObject(raw json.RawMessage) (ethCommon.Address, ethCommon.Address, bool, []byte, error) {
	var schema callObjectSchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return ethCommon.Address{}, ethCommon.Address{}, false, nil, err
	}

	var fromAddress ethCommon.Address
	if schema.From != "" {
		fromAddress = ethCommon.HexToAddress(schema.From)
	}

	payloadHex := schema.Data
	if payloadHex == "" {
		payloadHex = schema.Input
	}

	var payload []byte
	if payloadHex != "" {
		if !strings.HasPrefix(payloadHex, "0x") && !strings.HasPrefix(payloadHex, "0X") {
			payloadHex = "0x" + payloadHex
		}
		data, err := decodeHexString(payloadHex)
		if err != nil {
			return ethCommon.Address{}, ethCommon.Address{}, false, nil, err
		}
		payload = data
	}

	if schema.To == "" || schema.To == "0x" {
		return fromAddress, ethCommon.Address{}, false, payload, nil
	}

	toAddress := ethCommon.HexToAddress(schema.To)
	return fromAddress, toAddress, true, payload, nil
}

func handleSendRawTransaction(p *RpcReverseProxy, req JSONRPCRequestRaw) rpc_client.JSONRPCResponse {
	rawTxResult := gjson.GetBytes(req.Params, "0")
	if !rawTxResult.Exists() {
		return makeInvalidParamError(req.Id, "Invalid params for sendRawTransaction")
	}
	data := processSendRawTransaction(p, rawTxResult.String(), req.Id)
	return data
}

func processSendRawTransaction(p *RpcReverseProxy, rawTransactionHex string, id interface{}) rpc_client.JSONRPCResponse {
	decodedTxBytes, releaseDecoded, err := decodeHexPooled(rawTransactionHex)
	if err != nil {
		return makeInvalidParamError(id, "Invalid raw transaction hex data")
	}
	decodedReleased := false
	releaseDecodedOnce := func() {
		if decodedReleased {
			return
		}
		decodedReleased = true
		if releaseDecoded != nil {
			releaseDecoded()
		}
	}
	ethTx := new(types.Transaction)
	if err := ethTx.UnmarshalBinary(decodedTxBytes); err != nil {
		releaseDecodedOnce()
		return makeInternalError(id, "Failed to unmarshal Ethereum transaction")
	}
	signer := types.LatestSignerForChainID(p.ClientRpc.ChainId)
	fromAddress, err := types.Sender(signer, ethTx)
	if err != nil {
		releaseDecodedOnce()
		return makeInternalError(id, "Failed to derive sender from transaction")
	}
	if p.PKS == nil {
		releaseDecodedOnce()
		return makeInternalError(id, "Private key store not available.")
	}
	exists, err := p.PKS.HasPrivateKey(fromAddress)
	if err != nil {
		releaseDecodedOnce()
		return makeInternalError(id, "Error checking private key store")
	}
	var (
		bTx       []byte
		releaseTx func()
		buildErr  error

		tx mt_types.Transaction
	)
	if !exists {
		bTx, tx, releaseTx, buildErr = p.ClientRpc.BuildTransactionWithDeviceKeyFromEthTx(ethTx)
	} else {
		senderPkString, _ := p.PKS.GetPrivateKey(fromAddress)
		keyPair := bls.NewKeyPair(ethCommon.FromHex(senderPkString))
		bTx, tx, releaseTx, buildErr = p.ClientRpc.BuildTransactionWithDeviceKeyFromEthTxAndBlsPrivateKey(ethTx, keyPair.PrivateKey())
	}
	if buildErr != nil {
		releaseDecodedOnce()
		if releaseTx != nil {
			releaseTx()
		}
		return makeInternalError(id, "Failed to build transaction: "+buildErr.Error())
	}

	if tx != nil {
		fileAbi, _ := file_handler.GetFileAbi()
		name, _ := fileAbi.ParseMethodName(tx)
		if !(tx.ToAddress() == file_handler.PredictContractAddress(ethCommon.HexToAddress(p.CLientTcp.GetClientContext().Config.OwnerFileStorageAddress)) && name == "uploadChunk") {
			rs := p.ClientRpc.SendRawTransactionBinary(bTx, releaseTx, decodedTxBytes, releaseDecodedOnce, nil)
			releaseDecodedOnce()
			rs.Id = id
			return rs
		} else {
			fileHandler, err := file_handler.GetFileHandlerTCP(p.CLientTcp, p.TcpCfg)
			if err != nil {
				return makeInternalError(id, "Failed to build transaction: "+err.Error())
			}
			isPrevent, err := fileHandler.HandleFileTransactionNoReceipt(context.Background(), tx)
			if err != nil {
				return makeInternalError(id, "Failed to build transaction: "+err.Error())
			}
			if isPrevent {
				releaseDecodedOnce()
				releaseTx()
				// fileTimeLogger, _ := loggerfile.NewFileLogger("fileTimeLogger_TX.log")
				// fileTimeLogger.Info("Prevent uploadChunk transaction: %s", tx.Hash().Hex())
				return rpc_client.JSONRPCResponse{
					Jsonrpc: "2.0",
					Result:  tx.Hash().Hex(),
					Id:      id,
				}
			}
			return makeInternalError(id, "Failed to build transaction: "+err.Error())
		}

	} else {
		return makeInternalError(id, "null transaction: "+err.Error())
	}
}

func extractRequestID(body []byte) interface{} {
	idResult := gjson.GetBytes(body, "id")
	if !idResult.Exists() {
		return nil
	}
	var id interface{}
	if err := json.Unmarshal([]byte(idResult.Raw), &id); err != nil {
		return nil
	}
	return id
}

func writeJSON(w http.ResponseWriter, resp rpc_client.JSONRPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	// loggerfile, _ := loggerfile.NewFileLogger("rpc_requests.log")
	// loggerfile.Info("Response: %+v", resp)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// loggerfile.Info("__Failed to encode JSON response: %v errorr %v", resp, err)
		logger.Error("Failed to encode JSON response: %v", err)
	}
}

// --- Configuration Loading ---

func LoadConfig(path string, tcpCfgPath string) (*Config, *tcp_config.ClientConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open config file %s: %w", path, err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}
	Tcpcfg, err := tcp_config.LoadConfig(tcpCfgPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load tcp config from %s: %w", tcpCfgPath, err)
	}
	TcpCfg, ok := Tcpcfg.(*tcp_config.ClientConfig)
	if !ok {
		return nil, nil, fmt.Errorf("invalid config type loaded from %s", tcpCfgPath)
	}
	var rawConfig map[string]interface{}
	if err := json.Unmarshal(content, &rawConfig); err != nil {
		return nil, nil, fmt.Errorf("failed to parse config file %s as JSON: %w", path, err)
	}

	config := &Config{}
	if err := json.Unmarshal(content, config); err != nil {
		return nil, nil, fmt.Errorf("failed to decode config file %s into struct: %w", path, err)
	}

	if chainIdVal, ok := rawConfig["chain_id"]; ok {
		switch v := chainIdVal.(type) {
		case float64:
			config.ChainId = big.NewInt(int64(v))
		case string:
			if chainIdInt, success := new(big.Int).SetString(v, 0); success {
				config.ChainId = chainIdInt
			} else {
				return nil, nil, fmt.Errorf("invalid chain_id string format '%s'", v)
			}
		default:
			return nil, nil, fmt.Errorf("invalid type for chain_id (%T)", chainIdVal)
		}
	}

	if config.RPCServerURL == "" {
		return nil, nil, fmt.Errorf("missing 'rpc_server_url' in config")
	}
	if config.ReadonlyRPCServerURL == "" {
		log.Printf("Warning: 'readonly_rpc_server_url' not found. /readonly HTTP path will be disabled.")
	}
	if config.ReadonlyWSSServerURL == "" {
		log.Printf("Warning: 'readonly_wss_server_url' not found. /readonly WebSocket path will be disabled.")
	}
	if config.ChainId == nil {
		return nil, nil, fmt.Errorf("'chain_id' is missing or invalid in config")
	}

	return config, TcpCfg, nil
}

func main() {
	log.SetOutput(os.Stdout)
	logger.Info("Starting RPC Reverse Proxy...")

	var configPath string
	var tcpConfigPath string

	flag.StringVar(&defaultLogsDir, "logs-root", "./logs", "Root directory to store rpc-client logs (epoch_N)")
	flag.StringVar(&configPath, "config", "config-rpc.json", "Path to the RPC configuration file")
	flag.StringVar(&tcpConfigPath, "tcp-config", "config-client-tcp.json", "Path to the TCP client configuration file")
	flag.Parse()

	if err := os.MkdirAll(defaultLogsDir, 0o755); err != nil {
		log.Fatalf("FATAL: Failed to create logs directory %s: %v", defaultLogsDir, err)
	}
	loggerfile.SetGlobalLogDir(defaultLogsDir)
	log.Printf("Log files will be stored under %s", defaultLogsDir)
	if _, err := logger.EnableDailyFileLog("rpc-client.log"); err != nil {
		log.Fatalf("FATAL: Failed to enable file logging: %v", err)
	}
	logger.SetConsoleOutputEnabled(false)

	cfg, tcpCfg, err := LoadConfig(configPath, tcpConfigPath)
	if err != nil {
		log.Fatalf("FATAL: Failed to load configuration: %v", err)
	}

	pkStore, err := NewPrivateKeyStore(cfg.MasterPassword, cfg.AppPepper)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize PrivateKeyStore: %v", err)
	}
	defer pkStore.Close()
	logger.Info("PrivateKeyStore initialized successfully.")

	if cfg.PrivateKey == "" {
		log.Fatalf("FATAL: Node's BLS private key ('private_key') is missing in config.")
	}
	keyPair := bls.NewKeyPair(ethCommon.FromHex(cfg.PrivateKey))
	logger.Info("Node BLS Private Key loaded.")

	targetHTTPURL, err := url.Parse(cfg.RPCServerURL)
	if err != nil {
		log.Fatalf("FATAL: Invalid RPC server URL '%s': %v", cfg.RPCServerURL, err)
	}

	go func() {
		logger.Info("Starting pprof server on localhost:6069")
		logger.Error(http.ListenAndServe("localhost:6069", nil))
	}()

	// Tạo custom transport với connection pooling và resilient settings
	customTransport := &http.Transport{
		MaxIdleConns:          1000,             // Tổng số idle connections
		MaxIdleConnsPerHost:   500,              // Idle connections per host (tăng từ default 2)
		MaxConnsPerHost:       0,                // Unlimited active connections
		IdleConnTimeout:       90 * time.Second, // Giảm xuống để tránh stale connections
		DisableCompression:    true,             // Tắt compression để tăng tốc
		DisableKeepAlives:     false,            // Bật keep-alive
		ForceAttemptHTTP2:     false,            // Tắt HTTP/2 để ổn định hơn với EOF errors
		TLSHandshakeTimeout:   60 * time.Second, // Tăng thời gian handshake TLS
		ExpectContinueTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 240 * time.Second, // Timeout cho response headers
		// DialContext để kiểm soát connection timeout
		DialContext: (&net.Dialer{
			Timeout:   120 * time.Second,
			KeepAlive: 120 * time.Second,
		}).DialContext,
	}

	defaultProxy := httputil.NewSingleHostReverseProxy(targetHTTPURL)
	defaultProxy.Transport = customTransport

	var readonlyProxy *httputil.ReverseProxy
	if cfg.ReadonlyRPCServerURL != "" {
		readonlyTargetURL, err := url.Parse(cfg.ReadonlyRPCServerURL)
		if err != nil {
			log.Fatalf("FATAL: Invalid readonly RPC server URL '%s': %v", cfg.ReadonlyRPCServerURL, err)
		}

		// Tạo custom transport cho readonly proxy với resilient settings
		readonlyTransport := &http.Transport{
			MaxIdleConns:          1000,
			MaxIdleConnsPerHost:   500,
			MaxConnsPerHost:       0,
			IdleConnTimeout:       90 * time.Second,
			DisableCompression:    true,
			DisableKeepAlives:     false,
			ForceAttemptHTTP2:     false,
			TLSHandshakeTimeout:   60 * time.Second,
			ExpectContinueTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 240 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   120 * time.Second,
				KeepAlive: 120 * time.Second,
			}).DialContext,
		}

		readonlyProxy = httputil.NewSingleHostReverseProxy(readonlyTargetURL)
		readonlyProxy.Transport = readonlyTransport
		logger.Info("Readonly HTTP proxy configured for target: %s", cfg.ReadonlyRPCServerURL)
		readonlyProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("Readonly ReverseProxy error for %s %s: %v", r.Method, r.URL, err)
			http.Error(w, "Readonly upstream server error", http.StatusBadGateway)
		}
	}

	clientRpc, err := rpc_client.NewClientRPC(cfg.RPCServerURL, cfg.WSSServerURL, cfg.PrivateKey, cfg.ChainId)
	if err != nil {
		log.Fatalf("FATAL: Failed to create NewClientRPC: %v", err)
	}
	clientRpc.ChainId = cfg.ChainId
	clientTcp, err := client_tcp.NewClient(tcpCfg)

	if err != nil {
		logger.Warn("Warning: Failed to create TCP Client (port 4200 may be offline): %v", err)
	}
	proxy := &RpcReverseProxy{
		ReverseProxy:         defaultProxy,
		ReadonlyReverseProxy: readonlyProxy,
		ClientRpc:            clientRpc,
		CLientTcp:            clientTcp,
		PKS:                  pkStore,
		NodeBlsPrivateKey:    keyPair.PrivateKey(),
		NodeBlsPublicKey:     keyPair.PublicKey(),
		ReadonlyWSSServerURL: cfg.ReadonlyWSSServerURL,
		Cfg:                  cfg,
		TcpCfg:               tcpCfg,
	}

	proxy.ReverseProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Error("ReverseProxy error for %s %s: %v", r.Method, r.URL, err)
		http.Error(w, "Upstream server error", http.StatusBadGateway)
	}

	mux := http.NewServeMux()
	webPathPrefix := "/register-bls-key/"
	fs := http.FileServer(http.Dir("./dist"))
	mux.Handle(webPathPrefix, http.StripPrefix(webPathPrefix, fs))
	mux.HandleFunc("/debug/logs/list", handleRPCLogList)
	mux.HandleFunc("/debug/logs/content", handleRPCLogContent)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			var finalTargetURL string
			if strings.HasPrefix(r.URL.Path, "/readonly") {
				if proxy.ReadonlyWSSServerURL != "" {
					targetBaseURL, _ := url.Parse(proxy.ReadonlyWSSServerURL)
					targetHost := targetBaseURL.Scheme + "://" + targetBaseURL.Host
					pathForTarget := strings.TrimPrefix(r.URL.Path, "/readonly")
					finalTargetURL = targetHost + pathForTarget
					// logger.Info("Routing WebSocket upgrade for %s to READONLY target %s", r.URL.Path, finalTargetURL)
				} else {
					logger.Error("Readonly WebSocket upgrade requested for %s, but no 'readonly_wss_server_url' is configured.", r.URL.Path)
					http.Error(w, "Readonly WebSocket endpoint is not configured on the proxy", http.StatusServiceUnavailable)
					return
				}
			} else {
				finalTargetURL = proxy.ClientRpc.UrlWS
				// logger.Info("Routing WebSocket upgrade for %s to DEFAULT target %s", r.URL.Path, finalTargetURL)
			}

			if finalTargetURL == "" {
				logger.Error("WebSocket upgrade requested for %s, but target URL is empty.", r.URL.Path)
				http.Error(w, "Target WebSocket endpoint is not configured", http.StatusServiceUnavailable)
				return
			}
			proxy.serveWebSocket(w, r, finalTargetURL)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/readonly") {
			if proxy.ReadonlyReverseProxy != nil {
				logger.Info("Forwarding HTTP request to READONLY target: %s", r.URL.Path)
				r.URL.Path = strings.TrimPrefix(r.URL.Path, "/readonly")
				if !strings.HasPrefix(r.URL.Path, "/") {
					r.URL.Path = "/" + r.URL.Path
				}
				proxy.ReadonlyReverseProxy.ServeHTTP(w, r)
			} else {
				logger.Error("Received HTTP request for /readonly but readonly proxy is not configured.")
				http.Error(w, "Readonly endpoint is not configured on the proxy", http.StatusNotImplemented)
			}
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/" {
			http.Redirect(w, r, webPathPrefix, http.StatusMovedPermanently)
			return
		}

		proxy.ServeHTTP(w, r)
	})

	var tlsConfig *tls.Config
	useTLS := cfg.CertFile != "" && cfg.KeyFile != ""
	if useTLS {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			log.Fatalf("FATAL: Failed to load TLS certificate/key: %v", err)
		}
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		logger.Info("TLS enabled using cert: %s, key: %s", cfg.CertFile, cfg.KeyFile)
	}

	handler := logRequestResponseMiddleware(corsMiddleware(mux))

	var wg sync.WaitGroup
	serverRunning := false

	if cfg.ServerPort != "" {
		serverRunning = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			httpServer := &http.Server{
				Addr:              cfg.ServerPort,
				Handler:           handler,
				ReadTimeout:       120 * time.Second,
				WriteTimeout:      120 * time.Second,
				IdleTimeout:       360 * time.Second,
				MaxHeaderBytes:    1 << 20, // 1MB
				ReadHeaderTimeout: 10 * time.Second,
			}
			logger.Info("Starting HTTP server on port %s", cfg.ServerPort)
			if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("FATAL: HTTP server failed: %v", err)
			}
		}()
	}

	if useTLS && cfg.HTTPSPort != "" {
		serverRunning = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			httpsServer := &http.Server{
				Addr:              cfg.HTTPSPort,
				Handler:           handler,
				TLSConfig:         tlsConfig,
				ReadTimeout:       120 * time.Second,
				WriteTimeout:      120 * time.Second,
				IdleTimeout:       360 * time.Second,
				MaxHeaderBytes:    1 << 20, // 1MB
				ReadHeaderTimeout: 10 * time.Second,
			}
			logger.Info("Starting HTTPS server on port %s", cfg.HTTPSPort)
			if err := httpsServer.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("FATAL: HTTPS server failed: %v", err)
			}
		}()
	}

	if !serverRunning {
		log.Fatalf("FATAL: No HTTP or HTTPS server configured to run. Exiting.")
	}

	logger.Info("RPC Reverse Proxy started successfully.")
	wg.Wait()
	logger.Info("All servers have shut down. Exiting.")
}

func handleRPCLogList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeLogJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	root := strings.TrimSpace(r.URL.Query().Get("root"))
	if root == "" {
		root = defaultLogsDir
	}
	epoch := strings.TrimSpace(r.URL.Query().Get("epoch"))
	files, err := loggerfile.ListLogFiles(root, epoch)
	if err != nil {
		writeLogJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := map[string]interface{}{
		"root":  root,
		"epoch": epoch,
		"files": files,
	}
	writeLogJSON(w, http.StatusOK, resp)
}

func handleRPCLogContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeLogJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query := r.URL.Query()
	fileName := strings.TrimSpace(query.Get("file"))
	if fileName == "" {
		writeLogJSONError(w, http.StatusBadRequest, "query parameter 'file' is required")
		return
	}
	root := strings.TrimSpace(query.Get("root"))
	if root == "" {
		root = defaultLogsDir
	}
	epoch := strings.TrimSpace(query.Get("epoch"))
	maxBytes := int64(0)
	if maxStr := strings.TrimSpace(query.Get("maxBytes")); maxStr != "" {
		if parsed, err := strconv.ParseInt(maxStr, 10, 64); err == nil {
			maxBytes = parsed
		}
	}
	content, err := loggerfile.ReadLogFile(root, epoch, fileName, maxBytes)
	if err != nil {
		writeLogJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch strings.ToLower(strings.TrimSpace(query.Get("format"))) {
	case "text", "plain":
		writeLogPlain(w, content)
	case "html":
		writeLogHTML(w, content)
	default:
		resp := map[string]interface{}{
			"root":     root,
			"epoch":    epoch,
			"fileName": fileName,
			"content":  content,
		}
		writeLogJSON(w, http.StatusOK, resp)
	}
}

func writeLogJSONError(w http.ResponseWriter, status int, msg string) {
	writeLogJSON(w, status, map[string]string{"error": msg})
}

func writeLogJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Failed to encode JSON response: %v", err)
	}
}

func writeLogPlain(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, content); err != nil {
		log.Printf("Failed to write plain log response: %v", err)
	}
}

func writeLogHTML(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, renderColorLogHTML(content, "rpc-client log"))
}

func renderColorLogHTML(content, title string) string {
	var buf bytes.Buffer
	if title == "" {
		title = "Log viewer"
	}
	buf.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>")
	template.HTMLEscape(&buf, []byte(title))
	buf.WriteString("</title>")
	buf.WriteString("<style>")
	buf.WriteString("body{background:#0b0c10;color:#c5c6c7;font-family:Consolas,monospace;margin:0;padding:16px;}")
	buf.WriteString("pre{white-space:pre-wrap;word-break:break-word;line-height:1.4;}")
	buf.WriteString(".level-error{color:#ff6b6b;}")
	buf.WriteString(".level-warn{color:#ffd166;}")
	buf.WriteString(".level-info{color:#4ecdc4;}")
	buf.WriteString(".level-debug{color:#add8e6;}")
	buf.WriteString(".level-trace{color:#b084f9;}")
	buf.WriteString("</style></head><body><pre>")
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		class := classifyLogLine(line)
		if class != "" {
			buf.WriteString(`<span class="` + class + `">`)
		}
		template.HTMLEscape(&buf, []byte(line))
		if class != "" {
			buf.WriteString("</span>")
		}
		buf.WriteString("\n")
	}
	buf.WriteString("</pre></body></html>")
	return buf.String()
}

func classifyLogLine(line string) string {
	upper := strings.ToUpper(line)
	switch {
	case strings.Contains(upper, "[ERROR"):
		return "level-error"
	case strings.Contains(upper, "[WARN"):
		return "level-warn"
	case strings.Contains(upper, "[INFO"):
		return "level-info"
	case strings.Contains(upper, "[DEBUG"):
		return "level-debug"
	case strings.Contains(upper, "[TRACE"):
		return "level-trace"
	default:
		return ""
	}
}

// --- Middleware Implementations ---

func newLoggingResponseWriter(w http.ResponseWriter) *loggingResponseWriter {
	return &loggingResponseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		body:           new(bytes.Buffer),
	}
}

func (w *loggingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *loggingResponseWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func logRequestResponseMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Tối ưu hóa: Chỉ log WebSocket upgrades và errors (không log body để tăng hiệu suất)
		isWebSocket := websocket.IsWebSocketUpgrade(r)
		// logger.Info("Received request from %s for %s %s", r.RemoteAddr, r.Method, r.URL.RequestURI())
		if isWebSocket {
			// Chỉ log WebSocket upgrades
			logger.Debug("[WS] %s -> Attempting upgrade for %s", r.RemoteAddr, r.URL.RequestURI())
		}
		// Không log request/response body nữa - tăng throughput đáng kể
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Origin")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func decodeHexString(hexStr string) ([]byte, error) {
	if strings.HasPrefix(hexStr, "0x") || strings.HasPrefix(hexStr, "0X") {
		hexStr = hexStr[2:]
	}
	if len(hexStr)%2 == 1 {
		hexStr = "0" + hexStr
	}

	bufPtr := hexDecodePool.Get().(*[]byte)
	buf := *bufPtr
	if cap(buf) < len(hexStr)/2 {
		buf = make([]byte, len(hexStr)/2)
	} else {
		buf = buf[:len(hexStr)/2]
	}

	n, err := hex.Decode(buf, []byte(hexStr))
	if err != nil {
		hexDecodePool.Put(bufPtr)
		return nil, err
	}
	result := make([]byte, n)
	copy(result, buf[:n])
	*bufPtr = buf
	hexDecodePool.Put(bufPtr)
	return result, nil
}

func decodeHexPooled(hexStr string) ([]byte, func(), error) {
	if strings.HasPrefix(hexStr, "0x") || strings.HasPrefix(hexStr, "0X") {
		hexStr = hexStr[2:]
	}
	if len(hexStr)%2 != 0 {
		hexStr = "0" + hexStr
	}

	bufPtr := hexDecodePool.Get().(*[]byte)
	buf := *bufPtr
	needed := len(hexStr) / 2
	if cap(buf) < needed {
		buf = make([]byte, needed)
	} else {
		buf = buf[:needed]
	}

	decoder := hex.NewDecoder(strings.NewReader(hexStr))
	if _, err := io.ReadFull(decoder, buf); err != nil {
		if cap(buf) > 256*1024 {
			buf = make([]byte, 0, 64*1024)
		} else {
			buf = buf[:0]
		}
		*bufPtr = buf
		hexDecodePool.Put(bufPtr)
		return nil, func() {}, err
	}

	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if cap(buf) > 256*1024 {
			buf = make([]byte, 0, 64*1024)
		} else {
			buf = buf[:0]
		}
		*bufPtr = buf
		hexDecodePool.Put(bufPtr)
	}

	return buf, release, nil
}
