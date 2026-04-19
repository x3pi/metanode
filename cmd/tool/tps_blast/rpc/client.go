package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RPCClient is a simple JSON-RPC client for interacting with the Go Master node.
type RPCClient struct {
	Endpoint string
	client   *http.Client
}

// NewRPCClient creates a new RPCClient.
func NewRPCClient(endpoint string) *RPCClient {
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}

	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 100
	t.MaxConnsPerHost = 100
	t.MaxIdleConnsPerHost = 100

	return &RPCClient{
		Endpoint: endpoint,
		client: &http.Client{
			Transport: t,
			Timeout:   10 * time.Second,
		},
	}
}

// rpcRequest represents a JSON-RPC request.
type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

// rpcResponse represents a JSON-RPC response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Call making an RPC Request
func (c *RPCClient) call(method string, params ...interface{}) ([]byte, error) {
	reqBody := rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %v", err)
	}

	maxRetries := 5
	baseDelay := 50 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err := c.client.Post(c.Endpoint, "application/json", bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("rpc request failed: %v", err)
		}

		if resp.StatusCode == 429 {
			resp.Body.Close()
			time.Sleep(baseDelay)
			baseDelay *= 2 // Exponential backoff
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("rpc request returned status: %d", resp.StatusCode)
		}

		var rpcResp rpcResponse
		if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
			return nil, fmt.Errorf("failed to decode response: %v", err)
		}

		if rpcResp.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
		}

		return rpcResp.Result, nil
	}

	return nil, fmt.Errorf("rpc request exceeded max retries for 429 Too Many Requests")
}

// GetBlockNumber returns the current highest block number.
func (c *RPCClient) GetBlockNumber() (uint64, error) {
	result, err := c.call("eth_blockNumber")
	if err != nil {
		return 0, err
	}

	var hexStr string
	if err := json.Unmarshal(result, &hexStr); err != nil {
		return 0, fmt.Errorf("invalid response format: %v", err)
	}

	hexStr = strings.TrimPrefix(hexStr, "0x")
	num, err := strconv.ParseUint(hexStr, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse block number %q: %v", hexStr, err)
	}

	return num, nil
}

// Block represents basic information about a block.
type Block struct {
	Number       uint64
	Hash         string
	Transactions []string
}

// GetBlockByNumber fetches a block by its number.
func (c *RPCClient) GetBlockByNumber(number uint64) (*Block, error) {
	hexNumber := fmt.Sprintf("0x%x", number)
	result, err := c.call("eth_getBlockByNumber", hexNumber, false) // false means we only want transaction hashes
	if err != nil {
		return nil, err
	}

	if string(result) == "null" {
		return nil, nil // Block not found
	}

	var rawBlock struct {
		Number       string   `json:"number"`
		Hash         string   `json:"hash"`
		Transactions []string `json:"transactions"`
	}

	if err := json.Unmarshal(result, &rawBlock); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %v", err)
	}

	var num uint64
	if rawBlock.Number != "" {
		hexStr := strings.TrimPrefix(rawBlock.Number, "0x")
		n, err := strconv.ParseUint(hexStr, 16, 64)
		if err == nil {
			num = n
		}
	}
	return &Block{
		Number:       num,
		Hash:         rawBlock.Hash,
		Transactions: rawBlock.Transactions,
	}, nil
}

// AccountStateResult represents the result of mtn_getAccountState
type AccountStateResult struct {
	PublicKeyBls string   `json:"publicKeyBls"`
	Address      string   `json:"address"`
	Nonce        int      `json:"nonce"`
	BalanceStr   string   `json:"balance"` // decimal string from RPC
	Balance      *big.Int `json:"-"`       // parsed as big.Int
	RawPayload   string   `json:"-"`
}

// GetAccountState fetches account state via JSON-RPC (concurrency-safe)
func (c *RPCClient) GetAccountState(address string) (*AccountStateResult, error) {
	result, err := c.call("mtn_getAccountState", address, "latest")
	if err != nil {
		return nil, err
	}

	if string(result) == "null" {
		return nil, nil
	}

	var state AccountStateResult
	if err := json.Unmarshal(result, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal account state: %v", err)
	}
	state.RawPayload = string(result)
	// Parse balance string → *big.Int
	state.Balance = new(big.Int)
	if state.BalanceStr != "" {
		state.Balance.SetString(state.BalanceStr, 10)
	}
	return &state, nil
}

// GetReceipt fetches transaction receipt via JSON-RPC
func (c *RPCClient) GetReceipt(txHash string) (map[string]interface{}, error) {
	result, err := c.call("eth_getTransactionReceipt", txHash)
	if err != nil {
		return nil, err
	}

	if string(result) == "null" {
		return nil, nil
	}

	var receipt map[string]interface{}
	if err := json.Unmarshal(result, &receipt); err != nil {
		return nil, fmt.Errorf("failed to unmarshal receipt: %v", err)
	}

	return receipt, nil
}
