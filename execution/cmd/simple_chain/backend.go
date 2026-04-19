package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"

	"github.com/ethereum/go-ethereum/rpc"
)

var (
	errInvalidBlockRange  = errors.New("invalid block range params")
	errExceedMaxTopics    = errors.New("exceed max topics")
	ErrInvalidSig         = errors.New("invalid transaction v, r, s values")
	errInvalidCredentials = errors.New("invalid credentials")
	errInvalidTypeState   = errors.New("invalid type state")
	errStateNotReady      = errors.New("state not ready")
)

const maxTopics = 4

const maxLogsPerRequest = 5000

// The maximum number of allowed topics within a topic criteria
const limitBlockRange = 10000

const (
	rpcServerReadTimeout       = 120 * time.Second
	rpcServerWriteTimeout      = 120 * time.Second
	rpcServerIdleTimeout       = 180 * time.Second
	rpcServerReadHeaderTimeout = 30 * time.Second
)

// RPCTransaction represents a transaction that will serialize to the RPC representation of a transaction
type RPCTransaction struct {
	BlockHash           *common.Hash      `json:"blockHash"`
	BlockNumber         *hexutil.Big      `json:"blockNumber"`
	From                common.Address    `json:"from"`
	Gas                 hexutil.Uint64    `json:"gas"`
	GasPrice            *hexutil.Big      `json:"gasPrice"`
	GasFeeCap           *hexutil.Big      `json:"maxFeePerGas,omitempty"`
	GasTipCap           *hexutil.Big      `json:"maxPriorityFeePerGas,omitempty"`
	MaxFeePerBlobGas    *hexutil.Big      `json:"maxFeePerBlobGas,omitempty"`
	Hash                common.Hash       `json:"hash"`
	Input               hexutil.Bytes     `json:"input"`
	Nonce               hexutil.Uint64    `json:"nonce"`
	To                  *common.Address   `json:"to"`
	TransactionIndex    *hexutil.Uint64   `json:"transactionIndex"`
	Value               *hexutil.Big      `json:"value"`
	Type                hexutil.Uint64    `json:"type"`
	Accesses            *types.AccessList `json:"accessList,omitempty"`
	ChainID             *hexutil.Big      `json:"chainId,omitempty"`
	BlobVersionedHashes []common.Hash     `json:"blobVersionedHashes,omitempty"`
	V                   *hexutil.Big      `json:"v"`
	R                   *hexutil.Big      `json:"r"`
	S                   *hexutil.Big      `json:"s"`
	YParity             *hexutil.Uint64   `json:"yParity,omitempty"`
}

// OverrideAccount indicates the overriding fields of account during the execution
// of a message call.
// Note, state and stateDiff can't be specified at the same time. If state is
// set, message execution will only use the data in the given state. Otherwise
// if stateDiff is set, all diff will be applied first and then execute the call
// message.
type OverrideAccount struct {
	Nonce            *hexutil.Uint64             `json:"nonce"`
	Code             *hexutil.Bytes              `json:"code"`
	Balance          *hexutil.Big                `json:"balance"`
	State            map[common.Hash]common.Hash `json:"state"`
	StateDiff        map[common.Hash]common.Hash `json:"stateDiff"`
	MovePrecompileTo *common.Address             `json:"movePrecompileToAddress"`
}

// StateOverride is the collection of overridden accounts.
type StateOverride map[common.Address]OverrideAccount

// MetaAPI xử lý các RPC calls của Ethereum
type MetaAPI struct {
	App                  *App // Export field Client
	events               *filters.EventSystem
	processingChunkCount atomic.Int64

	// ── Cached results (avoid allocations on hot path) ──
	cachedChainId        hexutil.Big  // Never changes — set once at init
	cachedGasPrice       *hexutil.Big // Hardcoded value — set once at init
	cachedMaxPriorityFee *hexutil.Big // Hardcoded value — set once at init

}

// initCaches pre-computes values that never change or change rarely.
// Called once when the MetaAPI is created.
func (api *MetaAPI) initCaches() {
	// ChainId never changes
	api.cachedChainId = hexutil.Big(*api.App.config.ChainId)

	// GasPrice is currently hardcoded at 0x3e8
	gasPrice := big.NewInt(0x3e8)
	hexGasPrice := hexutil.Big(*gasPrice)
	api.cachedGasPrice = &hexGasPrice

	// MaxPriorityFeePerGas is currently hardcoded at 0x5f5e100
	priority := big.NewInt(0x5f5e100)
	hexPriority := hexutil.Big(*priority)
	api.cachedMaxPriorityFee = &hexPriority

}

// decodeHash parses a hex-encoded 32-byte hash. The input may optionally
// be prefixed by 0x and can have a byte length up to 32.
func decodeHash(s string) (h common.Hash, inputLength int, err error) {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		s = s[2:]
	}
	if (len(s) & 1) > 0 {
		s = "0" + s
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return common.Hash{}, 0, errors.New("hex string invalid")
	}
	if len(b) > 32 {
		return common.Hash{}, len(b), errors.New("hex string too long, want at most 32 bytes")
	}
	return common.BytesToHash(b), len(b), nil
}

func (api *MetaAPI) ChainId() (hexutil.Big, error) {
	return api.cachedChainId, nil
}

type revertError struct {
	error
	reason string
}

// ErrorData returns the hex encoded revert reason.
func (e *revertError) ErrorData() interface{} {
	return e.reason
}

// ErrorCode returns the JSON error code for a revert.
// See: https://github.com/ethereum/wiki/wiki/JSON-RPC-Error-Codes-Improvement-Proposal
func (e *revertError) ErrorCode() int {
	return 3
}

// newRevertError creates a revertError instance with the provided revert data.
func newRevertError(revert []byte) *revertError {
	err := vm.ErrExecutionReverted
	reason, errUnpack := abi.UnpackRevert(revert)
	if errUnpack == nil {
		err = fmt.Errorf("%w: %v", vm.ErrExecutionReverted, reason)
	}
	return &revertError{
		error:  err,
		reason: hexutil.Encode(revert),
	}
}

func newError(err error, revert []byte) *revertError {
	return &revertError{
		error:  err,
		reason: hexutil.Encode(revert),
	}
}

type LogData struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockNumber      string   `json:"blockNumber"`
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex string   `json:"transactionIndex"`
	BlockHash        string   `json:"blockHash"`
	LogIndex         string   `json:"logIndex"`
	Removed          bool     `json:"removed"`
}

type JsonData struct {
	Logs []LogData `json:"logs"`
}

type Header struct {
	Number uint64
	Hash   common.Hash
}

// BlockOverrides is a set of header fields to override.
type BlockOverrides struct {
	Number        *hexutil.Big
	Difficulty    *hexutil.Big // No-op if we're simulating post-merge calls.
	Time          *hexutil.Uint64
	GasLimit      *hexutil.Uint64
	FeeRecipient  *common.Address
	PrevRandao    *common.Hash
	BaseFeePerGas *hexutil.Big
	BlobBaseFee   *hexutil.Big
}

// Apply overrides the given header fields into the given block context.
func (o *BlockOverrides) Apply(blockCtx *vm.BlockContext) {
	if o == nil {
		return
	}
	if o.Number != nil {
		blockCtx.BlockNumber = o.Number.ToInt()
	}
	if o.Difficulty != nil {
		blockCtx.Difficulty = o.Difficulty.ToInt()
	}
	if o.Time != nil {
		blockCtx.Time = uint64(*o.Time)
	}
	if o.GasLimit != nil {
		blockCtx.GasLimit = uint64(*o.GasLimit)
	}
	if o.FeeRecipient != nil {
		blockCtx.Coinbase = *o.FeeRecipient
	}
	if o.PrevRandao != nil {
		blockCtx.Random = o.PrevRandao
	}
	if o.BaseFeePerGas != nil {
		blockCtx.BaseFee = o.BaseFeePerGas.ToInt()
	}
	if o.BlobBaseFee != nil {
		blockCtx.BlobBaseFee = o.BlobBaseFee.ToInt()
	}
}

// MakeHeader returns a new header object with the overridden
// fields.
// Note: MakeHeader ignores BlobBaseFee if set. That's because
// header has no such field.
func (o *BlockOverrides) MakeHeader(header *types.Header) *types.Header {
	if o == nil {
		return header
	}
	h := types.CopyHeader(header)
	if o.Number != nil {
		h.Number = o.Number.ToInt()
	}
	if o.Difficulty != nil {
		h.Difficulty = o.Difficulty.ToInt()
	}
	if o.Time != nil {
		h.Time = uint64(*o.Time)
	}
	if o.GasLimit != nil {
		h.GasLimit = uint64(*o.GasLimit)
	}
	if o.FeeRecipient != nil {
		h.Coinbase = *o.FeeRecipient
	}
	if o.PrevRandao != nil {
		h.MixDigest = *o.PrevRandao
	}
	if o.BaseFeePerGas != nil {
		h.BaseFee = o.BaseFeePerGas.ToInt()
	}
	return h
}

// Thêm hàm khởi tạo server
func NewServer(app *App) *http.ServeMux {

	server := rpc.NewServer()

	customAPI := &MetaAPI{App: app, events: app.eventSystem, processingChunkCount: atomic.Int64{}}
	customAPI.initCaches()
	// go customAPI.startProcessingLogger()
	if err := server.RegisterName("eth", customAPI); err != nil {
		logger.Error("[FATAL] Không thể đăng ký API custom: %v", err)
		logger.SyncFileLog()
		os.Exit(1)
	}

	adminApi := &AdminApi{App: app, events: app.eventSystem}

	if err := server.RegisterName("admin", adminApi); err != nil {
		logger.Error("[FATAL] Không thể đăng ký API adminApi: %v", err)
		logger.SyncFileLog()
		os.Exit(1)
	}

	if app.config.Debug {
		debugApi := &DebugApi{App: app}

		if err := server.RegisterName("debug", debugApi); err != nil {
			logger.Error("[FATAL] Không thể đăng ký API debugApi: %v", err)
			logger.SyncFileLog()
			os.Exit(1)
		}
	}

	// MtnAPI được di chuyển sang file riêng
	mtnAPI := NewMtnAPI(app, customAPI) // Sử dụng hàm NewMtnAPI để tạo instance
	if err := server.RegisterName("mtn", mtnAPI); err != nil {
		logger.Error("[FATAL] Không thể đăng ký API MtnAPI: %v", err)
		logger.SyncFileLog()
		os.Exit(1)
	}
	// CORS middleware — only allows wildcard origin on the public RPC root path.
	// Admin, debug, and pipeline endpoints do NOT get CORS headers to prevent
	// cross-site attacks (e.g. browser-based admin API abuse via CSRF).
	corsMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only allow CORS on public RPC paths (MetaMask / browser wallets)
			if r.URL.Path == "/" || r.URL.Path == "/ws" {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			}

			// Handle preflight
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	// Rate limiter middleware (global + per-IP)
	rateLimiter := NewRPCRateLimiter()

	// Metrics collector
	metricsCollector := NewMetricsCollector(app)
	metricsCollector.SetRateLimiterStatsFunc(rateLimiter.GetStats)
	// Wire circuit breaker stats from P2P handler if available
	if app.handler != nil {
		metricsCollector.SetCircuitBreakerStatsFunc(app.handler.GetCircuitBreakerStats)
	}
	app.metricsCollector = metricsCollector

	// ethSendRawTxMiddleware intercepts eth_sendRawTransaction calls from
	// MetaMask/standard Ethereum wallets (single hex param) and routes them
	// to SendRawEthTransaction() for in-process ETH→MetaTx conversion.
	//
	// PERFORMANCE: Only reads body when Content-Length > 100 bytes.
	// Read-only calls (eth_blockNumber ~60B, eth_chainId ~55B) skip
	// body reading entirely → zero allocation overhead.
	ethSendRawTxMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only intercept POST to /
			if r.Method != http.MethodPost || r.URL.Path != "/" {
				next.ServeHTTP(w, r)
				return
			}

			// FAST PATH: eth_sendRawTransaction payloads are always large
			// (typically >200 bytes due to the hex-encoded signed tx).
			// Simple read calls like eth_blockNumber/chainId are ~55-65 bytes.
			// Skip body inspection entirely for small requests.
			if r.ContentLength >= 0 && r.ContentLength < 100 {
				next.ServeHTTP(w, r)
				return
			}

			// Read body (limit to 8MB for safety)
			bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
			r.Body.Close()
			if err != nil || len(bodyBytes) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			// Quick check: does the body contain eth_sendRawTransaction?
			if !bytes.Contains(bodyBytes, []byte("eth_sendRawTransaction")) {
				// Not our target — restore body and pass through
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				next.ServeHTTP(w, r)
				return
			}

			// Parse JSON-RPC request
			var rpcReq struct {
				Jsonrpc string        `json:"jsonrpc"`
				Method  string        `json:"method"`
				Params  []interface{} `json:"params"`
				Id      interface{}   `json:"id"`
			}
			if err := json.Unmarshal(bodyBytes, &rpcReq); err != nil {
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				next.ServeHTTP(w, r)
				return
			}

			// Only intercept eth_sendRawTransaction with exactly 1 param (MetaMask format)
			if rpcReq.Method != "eth_sendRawTransaction" || len(rpcReq.Params) != 1 {
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				next.ServeHTTP(w, r)
				return
			}

			// Extract the hex-encoded raw tx from the single param
			rawTxHex, ok := rpcReq.Params[0].(string)
			if !ok || rawTxHex == "" {
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				next.ServeHTTP(w, r)
				return
			}

			// Decode hex to bytes
			rawTxHex = strings.TrimPrefix(rawTxHex, "0x")
			txBytes, err := hex.DecodeString(rawTxHex)
			if err != nil {
				writeJSONRPCError(w, rpcReq.Id, -32602, "Invalid raw transaction hex data")
				return
			}

			// Delegate to SendRawEthTransaction (in-process, no HTTP round-trip)
			txHash, err := customAPI.SendRawEthTransaction(r.Context(), txBytes)
			if err != nil {
				var revErr *revertError
				if errors.As(err, &revErr) {
					writeJSONRPCError(w, rpcReq.Id, revErr.ErrorCode(), revErr.Error())
				} else {
					writeJSONRPCError(w, rpcReq.Id, -32000, err.Error())
				}
				return
			}

			// Return standard JSON-RPC success response
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      rpcReq.Id,
				"result":  txHash.Hex(),
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		})
	}

	// Sử dụng mux để xử lý cả HTTP và WebSocket
	mux := http.NewServeMux()

	// Middleware chain: CORS → ETH SendRawTx Intercept → Rate Limiter → RPC Server
	httpHandler := corsMiddleware(ethSendRawTxMiddleware(rateLimiter.Middleware(server)))
	mux.Handle("/", httpHandler)

	// Prometheus metrics endpoint (standard Prometheus text format)
	mux.Handle("/metrics", promhttp.Handler())
	// Backward-compatible JSON metrics endpoint
	mux.Handle("/metrics/json", metricsCollector)
	// Enhanced /health endpoint (Liveness)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		
		status := map[string]interface{}{
			"status": "ok",
		}

		if app != nil && app.blockProcessor != nil {
			lastBlock := app.blockProcessor.GetLastBlock()
			if lastBlock != nil && lastBlock.Header() != nil {
				status["block"] = lastBlock.Header().BlockNumber()
				status["epoch"] = lastBlock.Header().Epoch()
				
				// Calculate block age
				blockTimeMs := lastBlock.Header().TimeStamp()
				if blockTimeMs > 0 {
					blockAgeMs := time.Now().UnixNano()/1e6 - int64(blockTimeMs)
					status["last_block_age_ms"] = blockAgeMs
				}
			}
		}

		json.NewEncoder(w).Encode(status)
	})

	// /readiness endpoint (Readiness Probe)
	mux.HandleFunc("/readiness", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		ready := true
		checks := map[string]string{
			"db": "ok",
		}

		if app == nil || app.blockProcessor == nil || app.blockProcessor.GetLastBlock() == nil {
			ready = false
			checks["db"] = "not_initialized"
		}

		status := map[string]interface{}{
			"ready": ready,
			"checks": checks,
		}

		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		json.NewEncoder(w).Encode(status)
	})

	// Pipeline monitoring endpoints
	mux.HandleFunc("/pipeline/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		data, err := app.blockProcessor.GetPipelineStatsJSON()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(data)
	})
	mux.HandleFunc("/pipeline/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		app.blockProcessor.ResetPipelineStats()
		w.Write([]byte(`{"status":"reset"}`))
	})

	mux.HandleFunc("/mtn/sendRawTransactionBin", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()

		rawPayload, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
			return
		}

		metaTx, ethTx, pubKey, err := decodeBinaryRawTxPayload(rawPayload)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid payload: %v", err), http.StatusBadRequest)
			return
		}

		txHash, err := customAPI.SendRawTransactionWithDeviceKey(r.Context(), metaTx, ethTx, pubKey)
		if err != nil {
			var revErr *revertError
			if errors.As(err, &revErr) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"code":    revErr.ErrorCode(),
					"message": revErr.Error(),
					"data":    revErr.ErrorData(),
				})
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code":    -32000,
				"message": err.Error(),
			})
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		if _, writeErr := w.Write(txHash.Bytes()); writeErr != nil {
			logger.Warn("failed to write binary transaction hash response: %v", writeErr)
		}
	})

	// Áp dụng middleware vào handler WebSocket
	wsHandler := server.WebsocketHandler([]string{"*"})
	mux.Handle("/ws", wsHandler)
	if app.config.Debug {
		debugApi := &DebugApi{App: app}
		mux.Handle("/debug/logs/ws", corsMiddleware(http.HandlerFunc(debugApi.HandleLogStreamWS)))
		mux.Handle("/debug/logs/content", corsMiddleware(http.HandlerFunc(debugApi.ServeLogPreview)))
	}

	return mux
}

func decodeBinaryRawTxPayload(payload []byte) ([]byte, []byte, []byte, error) {
	const headerSize = 4
	if len(payload) < headerSize*3 {
		return nil, nil, nil, fmt.Errorf("payload too short")
	}

	readSegment := func(buf []byte) ([]byte, []byte, error) {
		if len(buf) < headerSize {
			return nil, nil, fmt.Errorf("not enough data for length header")
		}
		segmentLen := binary.BigEndian.Uint32(buf[:headerSize])
		buf = buf[headerSize:]
		if segmentLen == 0 {
			return nil, buf, nil
		}
		if uint32(len(buf)) < segmentLen {
			return nil, nil, fmt.Errorf("segment length %d exceeds remaining payload %d", segmentLen, len(buf))
		}
		segment := make([]byte, segmentLen)
		copy(segment, buf[:segmentLen])
		return segment, buf[segmentLen:], nil
	}

	var (
		metaTx []byte
		ethTx  []byte
		pubKey []byte
		rest   = payload
		err    error
	)

	if metaTx, rest, err = readSegment(rest); err != nil {
		return nil, nil, nil, err
	}
	if ethTx, rest, err = readSegment(rest); err != nil {
		return nil, nil, nil, err
	}
	if pubKey, rest, err = readSegment(rest); err != nil {
		return nil, nil, nil, err
	}
	if len(rest) != 0 {
		return nil, nil, nil, fmt.Errorf("unexpected trailing bytes (%d)", len(rest))
	}

	return metaTx, ethTx, pubKey, nil
}

// writeJSONRPCError writes a standard JSON-RPC error response.
// Used by the ethSendRawTxMiddleware for MetaMask interception.
func writeJSONRPCError(w http.ResponseWriter, id interface{}, code int, message string) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
