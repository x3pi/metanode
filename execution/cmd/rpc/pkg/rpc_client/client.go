package rpc_client

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/gorilla/websocket"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	cfgCom "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/config"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"google.golang.org/protobuf/proto"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"

	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Client struct chứa các kết nối HTTP và WebSocket
type ClientRPC struct {
	HttpConn *http.Client
	WsConn   *websocket.Conn
	UrlHTTP  string
	UrlWS    string
	KeyPair  *bls.KeyPair
	ChainId  *big.Int
}
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"` // Giữ nguyên, có thể chứa raw revert data hoặc decoded error JSON
	// Decoded error fields (optional, chỉ có khi error được decode)
	Decoded      bool                   `json:"decoded,omitempty"`
	ErrorType    string                 `json:"error_type,omitempty"` // "panic", "custom", "standard", "unknown"
	ErrorName    string                 `json:"error_name,omitempty"`
	ErrorSig     string                 `json:"error_signature,omitempty"`
	Arguments    map[string]interface{} `json:"arguments,omitempty"`
	PanicCode    string                 `json:"panic_code,omitempty"`
	ContractAddr string                 `json:"contract_address,omitempty"`
	ContractName string                 `json:"contract_name,omitempty"`
	PC           uint64                 `json:"pc,omitempty"`
	CallDepth    int                    `json:"call_depth,omitempty"`
	Function     string                 `json:"function,omitempty"`
}

type JSONRPCRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	Id      interface{}   `json:"id"`
}

type JSONRPCResponse struct {
	Jsonrpc string        `json:"jsonrpc"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"` // Sử dụng con trỏ và thêm `omitempty`
	Id      interface{}   `json:"id"`
}

const (
	maxHTTPRetries = 3
	baseRetryDelay = 2 * time.Second
)

var (
	errNoMessageToEncode = errors.New("no message to encode")
	protoBytesPool       = sync.Pool{
		New: func() interface{} {
			buf := make([]byte, 0, 256*1024)
			return &buf
		},
	}
)

type binaryPayloadReader struct {
	segments []binaryPayloadSegment
	index    int
}

type binaryPayloadSegment struct {
	header    [4]byte
	headerPos int
	data      []byte
	dataPos   int
}

func newBinaryPayloadReader(parts ...[]byte) (*binaryPayloadReader, int) {
	segments := make([]binaryPayloadSegment, len(parts))
	total := 0
	for i, p := range parts {
		binary.BigEndian.PutUint32(segments[i].header[:], uint32(len(p)))
		segments[i].data = p
		total += len(p) + 4
	}
	return &binaryPayloadReader{segments: segments}, total
}

func (r *binaryPayloadReader) Read(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		if r.index >= len(r.segments) {
			if written == 0 {
				return 0, io.EOF
			}
			return written, nil
		}

		seg := &r.segments[r.index]

		if seg.headerPos < len(seg.header) {
			n := copy(p[written:], seg.header[seg.headerPos:])
			seg.headerPos += n
			written += n
			if written == len(p) {
				return written, nil
			}
			continue
		}

		if seg.dataPos < len(seg.data) {
			n := copy(p[written:], seg.data[seg.dataPos:])
			seg.dataPos += n
			written += n
			if written == len(p) {
				return written, nil
			}
			continue
		}

		r.index++
	}
	return written, nil
}

// NewClient tạo một đối tượng Client mới
func NewClientRPC(urlHTTP, urlWS, privateKey string, chainId *big.Int) (*ClientRPC, error) {
	// Tạo custom transport với connection pooling cho high concurrency
	transport := &http.Transport{
		MaxIdleConns:          1000,              // Tổng số idle connections
		MaxIdleConnsPerHost:   500,               // Idle connections per host (default: 2)
		MaxConnsPerHost:       0,                 // Unlimited active connections
		IdleConnTimeout:       480 * time.Second, // Giữ connections lâu
		DisableCompression:    true,              // Tắt compression để tăng tốc
		DisableKeepAlives:     false,             // Bật keep-alive
		ForceAttemptHTTP2:     true,              // HTTP/2 nếu có
		TLSHandshakeTimeout:   60 * time.Second,  // Tăng thời gian handshake TLS
		ExpectContinueTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 240 * time.Second, // Thêm timeout cho header
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		DialContext: (&net.Dialer{
			Timeout:   240 * time.Second,
			KeepAlive: 240 * time.Second,
		}).DialContext,
	}

	// Khởi tạo kết nối HTTP với custom transport
	httpConn := &http.Client{
		Timeout:   1200 * time.Second,
		Transport: transport,
	}

	// Khởi tạo kết nối WebSocket
	// WsConn, _, err := websocket.DefaultDialer.Dial(urlWS, nil)
	// if err != nil {
	// 	return nil, fmt.Errorf("không thể kết nối WebSocket: %w", err)
	// }
	keyPair := bls.NewKeyPair(common.FromHex(privateKey))
	return &ClientRPC{
		HttpConn: httpConn,
		// WsConn:   WsConn,
		UrlHTTP: urlHTTP,
		UrlWS:   urlWS,
		KeyPair: keyPair,
		ChainId: chainId,
	}, nil
}

// SendHTTPRequest gửi yêu cầu HTTP đến server
func (c *ClientRPC) SendHTTPRequest(request *JSONRPCRequest) *JSONRPCResponse {
	requestBody, err := json.Marshal(request)
	if err != nil {
		return &JSONRPCResponse{
			Jsonrpc: "2.0",
			Error: &JSONRPCError{
				Code:    -1,
				Message: err.Error(),
			}, // Chuyển đổi lỗi thành JSONRPCError
			Id: request.Id,
		}
	}

	var resp *http.Response
	for attempt := 1; attempt <= maxHTTPRetries; attempt++ {
		resp, err = c.HttpConn.Post(c.UrlHTTP, "application/json", bytes.NewBuffer(requestBody))
		if err != nil {
			if attempt == maxHTTPRetries || !isRetryableError(err) {
				return &JSONRPCResponse{
					Jsonrpc: "2.0",
					Error: &JSONRPCError{
						Code:    -1,
						Message: err.Error(),
					},
					Id: request.Id,
				}
			}
			logger.Warn("Retrying HTTP request attempt %d/%d due to error: %v", attempt, maxHTTPRetries, err)
			time.Sleep(time.Duration(attempt) * baseRetryDelay)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			if shouldRetryStatus(resp.StatusCode) && attempt < maxHTTPRetries {
				logger.Warn("Retrying HTTP request due to status %d (attempt %d/%d)", resp.StatusCode, attempt, maxHTTPRetries)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				time.Sleep(time.Duration(attempt) * baseRetryDelay)
				continue
			}
			defer resp.Body.Close()
			return &JSONRPCResponse{
				Jsonrpc: "2.0",
				Error: &JSONRPCError{
					Code:    -1,
					Message: fmt.Sprintf("HTTP status code: %d", resp.StatusCode),
				},
				Id: request.Id,
			}
		}

		break
	}

	if resp == nil {
		return &JSONRPCResponse{
			Jsonrpc: "2.0",
			Error: &JSONRPCError{
				Code:    -1,
				Message: "unexpected nil response after retries",
			},
			Id: request.Id,
		}
	}
	defer resp.Body.Close()

	var response JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return &JSONRPCResponse{
			Jsonrpc: "2.0",
			Error: &JSONRPCError{
				Code:    -1,
				Message: err.Error(),
			},
			Id: request.Id,
		}
	}
	return &response
}

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if ne, ok := err.(net.Error); ok && (ne.Timeout() || ne.Temporary()) {
		return true
	}
	msg := err.Error()
	if strings.Contains(msg, "use of closed network connection") {
		return true
	}
	if strings.Contains(msg, "connection reset by peer") {
		return true
	}
	if strings.Contains(msg, "broken pipe") {
		return true
	}
	return false
}

func shouldRetryStatus(status int) bool {
	switch status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

// SendWSRequest gửi yêu cầu WebSocket đến server
func (c *ClientRPC) SendWSRequest(request *JSONRPCRequest) *JSONRPCResponse {
	if err := c.WsConn.WriteJSON(request); err != nil {
		return &JSONRPCResponse{
			Jsonrpc: "2.0",
			Error: &JSONRPCError{
				Code:    -1,
				Message: err.Error(),
			}, // Chuyển đổi lỗi thành JSONRPCError
			Id: request.Id,
		}
	}

	var response JSONRPCResponse
	if err := c.WsConn.ReadJSON(&response); err != nil {
		return &JSONRPCResponse{
			Jsonrpc: "2.0",
			Error: &JSONRPCError{
				Code:    -1,
				Message: err.Error(),
			}, // Chuyển đổi lỗi thành JSONRPCError
			Id: request.Id,
		}
	}

	return &response
}

func (c *ClientRPC) GetAccountState(address common.Address, blockNrOrHash rpc.BlockNumberOrHash) (mt_types.AccountState, error) {
	request := &JSONRPCRequest{
		Jsonrpc: "2.0",
		Method:  "mtn_getAccountState",
		Params:  []interface{}{address.String(), blockNrOrHash.String()}, // Thay đổi thành []interface{}
		Id:      1,
	}

	response := c.SendHTTPRequest(request)
	if response.Error != nil {
		return nil, fmt.Errorf("lỗi từ server: code=%d, message=%s", response.Error.Code, response.Error.Message)
	}
	if response.Result != nil {
		resultValue, ok := (response.Result).(map[string]interface{}) // Ép kiểu an toàn
		if !ok {
			return nil, fmt.Errorf("kết quả không phải là map: %v", response.Result)
		}
		// Khởi tạo JsonAccountState từ map JSON
		jsonAccountState := &state.JsonAccountState{
			Address:        resultValue["address"].(string),
			Balance:        resultValue["balance"].(string),
			PendingBalance: resultValue["pendingBalance"].(string),
			LastHash:       resultValue["lastHash"].(string),
			DeviceKey:      resultValue["deviceKey"].(string),
			Nonce:          uint64(resultValue["nonce"].(float64)), // Chuyển đổi từ float64 sang uint64
			PublicKeyBls:   resultValue["publicKeyBls"].(string),
			AccountType:    int32(resultValue["accountType"].(float64)), // Chuyển đổi từ float64 sang int32
		}
		accountState := jsonAccountState.ToAccountState()
		return accountState, nil
	}
	return nil, fmt.Errorf("kết quả không hợp lệ: %v", response.Result)
}

func (c *ClientRPC) GetDeviceKey(hash common.Hash) (common.Hash, error) {
	request := &JSONRPCRequest{
		Jsonrpc: "2.0",
		Method:  "mtn_getDeviceKey",
		Params:  []interface{}{hash.String()}, // Thay đổi thành []interface{}
		Id:      1,
	}

	response := c.SendHTTPRequest(request)
	if response.Error != nil {
		return common.Hash{}, fmt.Errorf("lỗi từ server: code=%d, message=%s", response.Error.Code, response.Error.Message)
	}
	if response.Result != nil {
		return common.HexToHash(response.Result.(string)), nil
	}
	return common.Hash{}, fmt.Errorf("kết quả không hợp lệ: %v", response.Result)
}

func (c *ClientRPC) SendRawTransaction(input []byte, ethInput []byte, pubKeyBls []byte) JSONRPCResponse {
	request := &JSONRPCRequest{
		Jsonrpc: "2.0",
		Method:  "eth_sendRawTransactionWithDeviceKey",
		Params: []interface{}{
			input,
			ethInput,
			pubKeyBls,
		},
		Id: 1,
	}

	response := c.SendHTTPRequest(request)
	return *response

}

func (c *ClientRPC) SendRawTransactionBinary(metaTx []byte, releaseMeta func(), ethTx []byte, releaseEth func(), pubKeyBls []byte) JSONRPCResponse {
	reader, totalLen := newBinaryPayloadReader(metaTx, ethTx, pubKeyBls)
	targetURL := strings.TrimRight(c.UrlHTTP, "/") + "/mtn/sendRawTransactionBin"

	req, err := http.NewRequest(http.MethodPost, targetURL, io.NopCloser(reader))
	if err != nil {
		if releaseMeta != nil {
			releaseMeta()
		}
		if releaseEth != nil {
			releaseEth()
		}
		return JSONRPCResponse{
			Jsonrpc: "2.0",
			Error: &JSONRPCError{
				Code:    -1,
				Message: err.Error(),
			},
		}
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(totalLen)

	resp, err := c.HttpConn.Do(req)
	if err != nil {
		if releaseMeta != nil {
			releaseMeta()
		}
		if releaseEth != nil {
			releaseEth()
		}
		return JSONRPCResponse{
			Jsonrpc: "2.0",
			Error: &JSONRPCError{
				Code:    -1,
				Message: err.Error(),
			},
		}
	}
	defer resp.Body.Close()
	defer func() {
		if releaseMeta != nil {
			releaseMeta()
		}
		if releaseEth != nil {
			releaseEth()
		}
	}()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return JSONRPCResponse{
			Jsonrpc: "2.0",
			Error: &JSONRPCError{
				Code:    -1,
				Message: readErr.Error(),
			},
		}
	}

	if resp.StatusCode != http.StatusOK {
		var backendErr struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    string `json:"data,omitempty"`
		}
		if err := json.Unmarshal(body, &backendErr); err != nil {
			return JSONRPCResponse{
				Jsonrpc: "2.0",
				Error: &JSONRPCError{
					Code:    -1,
					Message: fmt.Sprintf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))),
				},
			}
		}
		return JSONRPCResponse{
			Jsonrpc: "2.0",
			Error: &JSONRPCError{
				Code:    backendErr.Code,
				Message: backendErr.Message,
				Data:    backendErr.Data,
			},
		}
	}

	if len(body) != common.HashLength {
		return JSONRPCResponse{
			Jsonrpc: "2.0",
			Error: &JSONRPCError{
				Code:    -1,
				Message: fmt.Sprintf("unexpected hash length %d", len(body)),
			},
		}
	}
	var txHash common.Hash
	copy(txHash[:], body)

	return JSONRPCResponse{
		Jsonrpc: "2.0",
		Result:  txHash.Hex(),
	}
}

func (c *ClientRPC) SendCallTransaction(input hexutil.Bytes) JSONRPCResponse {
	request := &JSONRPCRequest{
		Jsonrpc: "2.0",
		Method:  "eth_call",
		Params:  []interface{}{input.String()}, // Thay đổi thành []interface{}		Id:      1,
	}

	response := c.SendHTTPRequest(request)
	return *response

}

func (c *ClientRPC) SendEstimateGas(input hexutil.Bytes) JSONRPCResponse {
	request := &JSONRPCRequest{
		Jsonrpc: "2.0",
		Method:  "eth_estimateGas",
		Params:  []interface{}{input.String()},
	}
	response := c.SendHTTPRequest(request)
	return *response

}

// SendDeployContract calls eth_deployContract RPC method to get runtime bytecode from constructor bytecode
// Note: This method calls directly to the chain RPC server (rpc_server_url) to execute DeployContract
// Method eth_deployContract is automatically registered in backend.go with namespace "eth"
func (c *ClientRPC) SendDeployContract(constructorBytecode hexutil.Bytes) JSONRPCResponse {
	// Gọi trực tiếp đến chain RPC server với method eth_deployContract
	// Method này đã được đăng ký tự động trong backend.go với namespace "eth"
	// (DeployContract -> eth_deployContract)
	request := &JSONRPCRequest{
		Jsonrpc: "2.0",
		Method:  "eth_deployContract",
		Params:  []interface{}{constructorBytecode.String()},
		Id:      1,
	}

	// SendHTTPRequest đã gọi đến c.UrlHTTP (chain RPC server)
	response := c.SendHTTPRequest(request)
	return *response
}
func resolveUint64Param(value string, defaultVal uint64, field string) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultVal, nil
	}

	var (
		parsed uint64
		err    error
	)

	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		parsed, err = hexutil.DecodeUint64(value)
	} else {
		parsed, err = strconv.ParseUint(value, 10, 64)
	}
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q: %w", field, value, err)
	}
	return parsed, nil
}

func resolveUint64WithFallback(values []string, defaultVal uint64, field string) (uint64, error) {
	for _, val := range values {
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		parsed, err := resolveUint64Param(val, defaultVal, field)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	}
	return defaultVal, nil
}
func (c *ClientRPC) BuildCallTransaction(decoded DecodedCallObject) ([]byte, error) {
	maxGas, err := resolveUint64Param(decoded.Schema.Gas, 100_000_000, "gas")
	if err != nil {
		return nil, err
	}
	maxGasPrice, err := resolveUint64WithFallback(
		[]string{
			decoded.Schema.GasPrice,
			decoded.Schema.MaxFeePerGas,
			decoded.Schema.MaxPriorityFeePerGas,
		},
		uint64(mt_common.MINIMUM_BASE_FEE),
		"gasPrice",
	)
	if err != nil {
		return nil, err
	}
	lastDeviceKey := common.HexToHash(
		"0000000000000000000000000000000000000000000000000000000000000000",
	)
	newDeviceKey := common.HexToHash(
		"0000000000000000000000000000000000000000000000000000000000000000",
	)

	as, err := c.GetAccountState(decoded.FromAddress, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))

	if err != nil {
		return nil, fmt.Errorf("BuildCallTransaction lỗi khi get acccount state: %v", err) // Cập nhật thông báo lỗi
	}
	bRelatedAddresses := make([][]byte, 0)

	var bData []byte

	callData := mt_transaction.NewCallData(decoded.Payload)

	bData, err = callData.Marshal()
	if err != nil {
		return nil, fmt.Errorf("lỗi convert callData: %w", err) // Cập nhật thông báo lỗi
	}

	txx := mt_transaction.NewTransaction(
		decoded.FromAddress,
		decoded.ToAddress,
		big.NewInt(0),
		maxGas,
		maxGasPrice,
		600,
		bData,
		bRelatedAddresses,
		lastDeviceKey,
		newDeviceKey,
		as.Nonce(),
		c.ChainId.Uint64(),
	)
	txx.SetSign(c.KeyPair.PrivateKey())
	bTransaction, err := txx.Marshal()
	return bTransaction, err
}

func (c *ClientRPC) BuildDeployTransaction(decoded DecodedCallObject) ([]byte, error) {
	fromAddress := decoded.FromAddress
	maxGas, err := resolveUint64Param(decoded.Schema.Gas, 100000000, "gas")
	if err != nil {
		return nil, err
	}
	logger.Info("BuildDeployTransaction maxGas: %v", maxGas)
	maxGasPrice, err := resolveUint64WithFallback(
		[]string{
			decoded.Schema.GasPrice,
			decoded.Schema.MaxFeePerGas,
			decoded.Schema.MaxPriorityFeePerGas,
		},
		uint64(mt_common.MINIMUM_BASE_FEE),
		"gasPrice",
	)
	if err != nil {
		return nil, err
	}
	lastDeviceKey := common.HexToHash(
		"0000000000000000000000000000000000000000000000000000000000000000",
	)
	newDeviceKey := common.HexToHash(
		"0000000000000000000000000000000000000000000000000000000000000000",
	)

	as, err := c.GetAccountState(fromAddress, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))

	if err != nil {
		return nil, fmt.Errorf("BuildDeployTransaction lỗi khi get acccount state: %v", err) // Cập nhật thông báo lỗi
	}
	bRelatedAddresses := make([][]byte, 0)

	var bData []byte

	callData := mt_transaction.NewCallData(decoded.Payload)

	bData, err = callData.Marshal()
	if err != nil {
		return nil, fmt.Errorf("lỗi convert callData: %w", err) // Cập nhật thông báo lỗi
	}
	toAddress := common.Address{}

	txx := mt_transaction.NewTransaction(
		fromAddress,
		toAddress,
		big.NewInt(0),
		maxGas,
		maxGasPrice,
		6000000,
		bData,
		bRelatedAddresses,
		lastDeviceKey,
		newDeviceKey,
		as.Nonce(),
		c.ChainId.Uint64(),
	)
	txx.SetSign(c.KeyPair.PrivateKey())
	logger.Info("BuildDeployTransaction nonce: %v", txx)
	bTransaction, err := txx.Marshal()
	return bTransaction, err
}

func (c *ClientRPC) BuildTransactionWithDeviceKey(
	ethTx *types.Transaction,
) ([]byte, mt_types.Transaction, func(), error) {
	sg := types.NewCancunSigner(ethTx.ChainId())
	fromAddress, err := sg.Sender(ethTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("lỗi khi get fromAddress : %w", err)
	}
	as, err := c.GetAccountState(fromAddress, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("BuildTransactionWithDeviceKeyFromEthTx lỗi khi get acccount state %v: %v", fromAddress, err)
	}
	if ethTx.To() == nil || *ethTx.To() != utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		if len(as.PublicKeyBls()) == 0 {
			return nil, nil, nil, fmt.Errorf("lỗi tài khoản chưa đăng ký public key bls trên chain")
		}

		if !bytes.Equal(as.PublicKeyBls(), c.KeyPair.BytesPublicKey()) {
			logger.Info("lỗi tài khoản chưa đăng ký private key bls với rpc: %x orgiginal %x", as.PublicKeyBls(), c.KeyPair.BytesPublicKey())
			return nil, nil, nil, fmt.Errorf("lỗi tài khoản chưa đăng ký private key bls với rpc")
		}
	}

	deviceKey, err := c.GetDeviceKey(as.LastHash())
	if err != nil {
		logger.Info("lỗi khi get deviceKey", err)
	}

	rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d", hex.EncodeToString(as.LastHash().Bytes()), time.Now().Unix()))

	rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)

	newDeviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

	bRelatedAddresses := make([][]byte, 0)
	transaction, err := mt_transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("error buidl  NewTransactionFromEth: %w", err)
	}
	transaction.UpdateRelatedAddresses(bRelatedAddresses)
	transaction.UpdateDeriver(deviceKey, newDeviceKey)
	transaction.SetSign(c.KeyPair.PrivateKey())

	// Create TransactionWithDeviceKey
	transactionWithDeviceKey := &mt_proto.TransactionWithDeviceKey{
		Transaction: transaction.Proto().(*mt_proto.Transaction),
		DeviceKey:   rawNewDeviceKey,
	}

	data, release, err := marshalProtoMessage(transactionWithDeviceKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal TransactionWithDeviceKey: %w", err)
	}
	return data, transaction, release, err
}

func (c *ClientRPC) BuildTransactionWithDeviceKeyFromEthTx(
	ethTx *types.Transaction,
	cfg *config.ClientConfig,
	cfgCom *cfgCom.Config,
	ldbContractFree *storage.ContractFreeGasStorage,
	isSetNonce bool,
	topUpFunc func(toAddress common.Address) error,
) ([]byte, mt_types.Transaction, func(), error) {
	sg := types.NewCancunSigner(ethTx.ChainId())
	fromAddress, err := sg.Sender(ethTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("lỗi khi get fromAddress : %w", err)
	}
	as, err := c.GetAccountState(fromAddress, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("BuildTransactionWithDeviceKeyFromEthTx lỗi khi get acccount state %v: %v", fromAddress, err)
	}

	if ethTx.To() == nil || *ethTx.To() != utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		if len(as.PublicKeyBls()) == 0 {
			return nil, nil, nil, fmt.Errorf("lỗi tài khoản chưa đăng ký public key bls trên chain")
		}
		if !bytes.Equal(as.PublicKeyBls(), c.KeyPair.BytesPublicKey()) {
			logger.Info("lỗi tài khoản chưa đăng ký private key bls với rpc: %x orgiginal %x", as.PublicKeyBls(), c.KeyPair.BytesPublicKey())

			return nil, nil, nil, fmt.Errorf("lỗi tài khoản chưa đăng ký private key bls với rpc")
		}
	}
	if cfg == nil {
		return nil, nil, nil, fmt.Errorf("cfg is nil")
	}
	// Chỉ check free gas khi tài khoản cần được top-up (balance thấp, đã có lịch sử giao dịch)
	if !cfgCom.DisableFreeGas && ethTx.To() != nil && as.Balance().Cmp(cfgCom.GetFreeGasMinBalance()) < 0 && as.Nonce() != 0 {
		exist, _ := ldbContractFree.HasContract(*ethTx.To())
		if exist && topUpFunc != nil {
			// Đưa vào hàng chờ owner để tránh nonce conflict
			if err := topUpFunc(fromAddress); err != nil {
				return nil, nil, nil, fmt.Errorf("topUpFunc failed: %v", err)
			}
		}
	}
	deviceKey, err := c.GetDeviceKey(as.LastHash())
	if err != nil {
		logger.Info("lỗi khi get deviceKey", err)
	}

	rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d", hex.EncodeToString(as.LastHash().Bytes()), time.Now().Unix()))

	rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)

	newDeviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

	bRelatedAddresses := make([][]byte, 0)
	transaction, err := mt_transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("error buidl  NewTransactionFromEth: %w", err)
	}
	if isSetNonce {
		transaction.SetNonce(as.Nonce())
	}
	transaction.UpdateRelatedAddresses(bRelatedAddresses)
	transaction.UpdateDeriver(deviceKey, newDeviceKey)
	transaction.SetSign(c.KeyPair.PrivateKey())
	// Create TransactionWithDeviceKey
	transactionWithDeviceKey := &mt_proto.TransactionWithDeviceKey{
		Transaction: transaction.Proto().(*mt_proto.Transaction),
		DeviceKey:   rawNewDeviceKey,
	}

	data, release, err := marshalProtoMessage(transactionWithDeviceKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal TransactionWithDeviceKey: %w", err)
	}
	// if !isFirst {
	// 	logger.Info(")))))))))))))))))))))) build robot transaction: %v", transaction)
	// }
	logger.Info("BuildTransactionWithDeviceKeyFromEthTx: %v", transaction)
	return data, transaction, release, err
}

func (c *ClientRPC) BuildTransactionWithDeviceKeyFromEthTxAndBlsPrivateKey(
	ethTx *types.Transaction,
	cfg *config.ClientConfig,
	cfgCom *cfgCom.Config,
	ldbContractFree *storage.ContractFreeGasStorage,
	private mt_common.PrivateKey,
	topUpFunc func(toAddress common.Address) error,
) ([]byte, mt_types.Transaction, func(), error) {

	sg := types.NewCancunSigner(ethTx.ChainId())
	fromAddress, err := sg.Sender(ethTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("lỗi khi get fromAddress : %w", err) // Cập nhật thông báo lỗi
	}
	as, err := c.GetAccountState(fromAddress, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("BuildTransactionWithDeviceKeyFromEthTxAndBlsPrivateKey lỗi khi get acccount state: %v", err) // Cập nhật thông báo lỗi
	}
	// Chỉ check free gas khi tài khoản cần được top-up (balance thấp, đã có lịch sử giao dịch)
	// if !cfgCom.DisableFreeGas && ethTx.To() != nil && as.Balance().Cmp(cfgCom.GetFreeGasMinBalance()) < 0 && as.Nonce() != 0 {
	// 	exist, err := ldbContractFree.HasContract(*ethTx.To())
	// 	if err != nil {
	// 		return nil, nil, nil, fmt.Errorf("lỗi khi kiểm tra contract free gas: %v", err)
	// 	}
	// 	if exist && topUpFunc != nil {
	// 		// Đưa vào hàng chờ owner để tránh nonce conflict
	// 		if err := topUpFunc(fromAddress); err != nil {
	// 			return nil, nil, nil, fmt.Errorf("topUpFunc failed: %v", err)
	// 		}
	// 	}
	// }
	deviceKey, err := c.GetDeviceKey(as.LastHash())
	if err != nil {
		logger.Info("lỗi khi get deviceKey", err)
	}

	rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d", hex.EncodeToString(as.LastHash().Bytes()), time.Now().Unix()))

	rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)

	newDeviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

	bRelatedAddresses := make([][]byte, 0)

	transaction, err := mt_transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("error buidl  NewTransactionFromEth: %w", err)
	}
	transaction.UpdateRelatedAddresses(bRelatedAddresses)
	transaction.UpdateDeriver(deviceKey, newDeviceKey)
	// Cập nhật nonce từ account state
	transaction.SetNonce(as.Nonce())
	transaction.SetSign(private)
	// Create TransactionWithDeviceKey
	transactionWithDeviceKey := &mt_proto.TransactionWithDeviceKey{
		Transaction: transaction.Proto().(*mt_proto.Transaction),
		DeviceKey:   rawNewDeviceKey,
	}

	data, release, err := marshalProtoMessage(transactionWithDeviceKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal TransactionWithDeviceKey: %w", err)
	}
	return data, transaction, release, nil
}

// BuildTransferTransaction tạo giao dịch chuyển tiền
func (c *ClientRPC) BuildTransferTransaction(
	fromAddress common.Address,
	toAddress common.Address,
	amount *big.Int,
) ([]byte, mt_types.Transaction, func(), error) {
	// Lấy account state để có nonce
	as, err := c.GetAccountState(fromAddress, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("BuildTransferTransaction lỗi khi get account state: %v", err)
	}
	// Kiểm tra balance có đủ không
	if as.Balance().Cmp(amount) < 0 {
		return nil, nil, nil, fmt.Errorf("số dư không đủ: có %s, cần %s", as.Balance().String(), amount.String())
	}
	// Kiểm tra BLS key
	if len(as.PublicKeyBls()) == 0 {
		return nil, nil, nil, fmt.Errorf("tài khoản chưa đăng ký public key BLS")
	}
	if !bytes.Equal(as.PublicKeyBls(), c.KeyPair.BytesPublicKey()) {
		return nil, nil, nil, fmt.Errorf("private key BLS không khớp với account")
	}
	// Tạo device key
	deviceKey, err := c.GetDeviceKey(as.LastHash())
	if err != nil {
		logger.Info("lỗi khi get deviceKey", err)
	}

	rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d", hex.EncodeToString(as.LastHash().Bytes()), time.Now().Unix()))
	rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)
	newDeviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

	// Cấu hình gas
	maxGas := uint64(21000) // Gas cho transfer thông thường
	maxGasPrice := uint64(mt_common.MINIMUM_BASE_FEE)

	bRelatedAddresses := make([][]byte, 0)

	// Tạo transaction với data rỗng (transfer thuần túy)
	var bData []byte

	txx := mt_transaction.NewTransaction(
		fromAddress,
		toAddress,
		amount,
		maxGas,
		maxGasPrice,
		600, // timeout
		bData,
		bRelatedAddresses,
		deviceKey,
		newDeviceKey,
		as.Nonce(),
		c.ChainId.Uint64(),
	)

	// Ký transaction
	txx.SetSign(c.KeyPair.PrivateKey())

	// Tạo TransactionWithDeviceKey
	transactionWithDeviceKey := &mt_proto.TransactionWithDeviceKey{
		Transaction: txx.Proto().(*mt_proto.Transaction),
		DeviceKey:   rawNewDeviceKey,
	}

	data, release, err := marshalProtoMessage(transactionWithDeviceKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal TransactionWithDeviceKey: %w", err)
	}

	return data, txx, release, nil
}

func marshalProtoMessage(msg proto.Message) ([]byte, func(), error) {
	bufPtr := protoBytesPool.Get().(*[]byte)
	buf := *bufPtr
	buf = buf[:0]
	buf, err := proto.MarshalOptions{}.MarshalAppend(buf, msg)
	if err != nil {
		*bufPtr = buf
		protoBytesPool.Put(bufPtr)
		return nil, nil, err
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if cap(buf) > 1024*1024 {
			buf = make([]byte, 0, 256*1024)
		} else {
			buf = buf[:0]
		}
		*bufPtr = buf
		protoBytesPool.Put(bufPtr)
	}
	return buf, release, nil
}
