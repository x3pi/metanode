package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RPCClient is a JSON-RPC client for interacting with the blockchain node.
type RPCClient struct {
	Endpoint string
	client   *http.Client
}

// NewRPCClient creates a new RPCClient with connection pooling.
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
			Timeout:   5 * time.Second,
		},
	}
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

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

// call makes an RPC request with retry and backoff for rate limiting.
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

	maxRetries := 3
	baseDelay := 50 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err := c.client.Post(c.Endpoint, "application/json", bytes.NewReader(payload))
		if err != nil {
			if attempt < maxRetries-1 {
				time.Sleep(baseDelay)
				baseDelay *= 2
				continue
			}
			return nil, fmt.Errorf("rpc request failed: %v", err)
		}

		if resp.StatusCode == 429 {
			resp.Body.Close()
			time.Sleep(baseDelay)
			baseDelay *= 2
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

// BlockInfo represents basic information about a block (tx hashes only).
type BlockInfo struct {
	Number       uint64
	Hash         string
	Timestamp    uint64
	Transactions []string
}

// GetBlockByNumber fetches a block with tx hashes.
func (c *RPCClient) GetBlockByNumber(number uint64) (*BlockInfo, error) {
	hexNumber := fmt.Sprintf("0x%x", number)
	result, err := c.call("eth_getBlockByNumber", hexNumber, false)
	if err != nil {
		return nil, err
	}

	if string(result) == "null" {
		return nil, nil
	}

	var rawBlock struct {
		Number       string   `json:"number"`
		Hash         string   `json:"hash"`
		Timestamp    string   `json:"timestamp"`
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

	var ts uint64
	if rawBlock.Timestamp != "" {
		hexStr := strings.TrimPrefix(rawBlock.Timestamp, "0x")
		t, err := strconv.ParseUint(hexStr, 16, 64)
		if err == nil {
			ts = t
		}
	}

	return &BlockInfo{
		Number:       num,
		Hash:         rawBlock.Hash,
		Timestamp:    ts,
		Transactions: rawBlock.Transactions,
	}, nil
}

// BlockFull represents a block with all hash fields for fork comparison.
type BlockFull struct {
	Number           uint64
	Hash             string
	ParentHash       string
	StateRoot        string
	TransactionsRoot string
	ReceiptsRoot     string
	Timestamp        uint64
	TxCount          int
}

// GetBlockFull fetches a block with full header information for fork checking.
func (c *RPCClient) GetBlockFull(number uint64) (*BlockFull, error) {
	hexNumber := fmt.Sprintf("0x%x", number)
	result, err := c.call("eth_getBlockByNumber", hexNumber, false)
	if err != nil {
		return nil, err
	}

	if string(result) == "null" {
		return nil, nil
	}

	var rawBlock struct {
		Number           string   `json:"number"`
		Hash             string   `json:"hash"`
		ParentHash       string   `json:"parentHash"`
		StateRoot        string   `json:"stateRoot"`
		TransactionsRoot string   `json:"transactionsRoot"`
		ReceiptsRoot     string   `json:"receiptsRoot"`
		Timestamp        string   `json:"timestamp"`
		Transactions     []string `json:"transactions"`
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

	var ts uint64
	if rawBlock.Timestamp != "" {
		hexStr := strings.TrimPrefix(rawBlock.Timestamp, "0x")
		t, err := strconv.ParseUint(hexStr, 16, 64)
		if err == nil {
			ts = t
		}
	}

	return &BlockFull{
		Number:           num,
		Hash:             rawBlock.Hash,
		ParentHash:       rawBlock.ParentHash,
		StateRoot:        rawBlock.StateRoot,
		TransactionsRoot: rawBlock.TransactionsRoot,
		ReceiptsRoot:     rawBlock.ReceiptsRoot,
		Timestamp:        ts,
		TxCount:          len(rawBlock.Transactions),
	}, nil
}
