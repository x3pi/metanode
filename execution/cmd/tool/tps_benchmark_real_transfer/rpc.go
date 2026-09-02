package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// JSON-RPC Client — adapted from tps_blast/rpc/client.go with added methods
// ═══════════════════════════════════════════════════════════════════════════════

// RPCClient is a simple JSON-RPC client for interacting with the blockchain node.
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
	// MaxConnsPerHost=100 silently capped total concurrent connections to the
	// target node regardless of -workers: every worker goroutine for a node
	// shares this one RPCClient/Transport, so raising -workers past ~100 just
	// added goroutines queueing for the same 100 connections instead of any
	// real additional concurrency — measured directly: 100 workers reached
	// ~15-17k inject tx/s, but 300 and 600 workers did *not* go higher (and
	// were occasionally slightly lower from added queueing/scheduling
	// overhead), which is the signature of a saturated connection pool, not a
	// saturated target node. 0 means unlimited in net/http.
	t.MaxIdleConns = 0
	t.MaxConnsPerHost = 0
	t.MaxIdleConnsPerHost = 2048

	return &RPCClient{
		Endpoint: endpoint,
		client: &http.Client{
			Transport: t,
			Timeout:   15 * time.Second,
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

	maxRetries := 5
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

// ═══════════════════════════════════════════════════════════════════════════════
// Block Height & Info
// ═══════════════════════════════════════════════════════════════════════════════

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

	return &BlockInfo{
		Number:       num,
		Hash:         rawBlock.Hash,
		Transactions: rawBlock.Transactions,
	}, nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// Fork Check — Block with full headers for hash comparison
// ═══════════════════════════════════════════════════════════════════════════════

// BlockFull represents a block with all hash fields for fork comparison.
type BlockFull struct {
	Number           uint64
	Hash             string
	ParentHash       string
	StateRoot        string
	TransactionsRoot string
	ReceiptsRoot     string
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

	return &BlockFull{
		Number:           num,
		Hash:             rawBlock.Hash,
		ParentHash:       rawBlock.ParentHash,
		StateRoot:        rawBlock.StateRoot,
		TransactionsRoot: rawBlock.TransactionsRoot,
		ReceiptsRoot:     rawBlock.ReceiptsRoot,
		TxCount:          len(rawBlock.Transactions),
	}, nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// Send Transaction
// ═══════════════════════════════════════════════════════════════════════════════

// SendRawTransaction sends a signed raw transaction via eth_sendRawTransaction.
// Returns the transaction hash on success.
func (c *RPCClient) SendRawTransaction(txBytes []byte) (string, error) {
	hexData := "0x" + hex.EncodeToString(txBytes)
	result, err := c.call("eth_sendRawTransaction", hexData)
	if err != nil {
		return "", err
	}

	var txHash string
	if err := json.Unmarshal(result, &txHash); err != nil {
		return "", fmt.Errorf("failed to parse tx hash: %v", err)
	}

	return txHash, nil
}

// GetTransactionCount returns the account's current nonce.
func (c *RPCClient) GetTransactionCount(address string) (uint64, error) {
	result, err := c.call("eth_getTransactionCount", address, "latest")
	if err != nil {
		return 0, err
	}
	var hexStr string
	if err := json.Unmarshal(result, &hexStr); err != nil {
		return 0, fmt.Errorf("invalid response format: %v", err)
	}
	hexStr = strings.TrimPrefix(hexStr, "0x")
	return strconv.ParseUint(hexStr, 16, 64)
}

// GetBalance returns the account's balance in wei as a hex string (0x...).
func (c *RPCClient) GetBalance(address string) (string, error) {
	result, err := c.call("eth_getBalance", address, "latest")
	if err != nil {
		return "", err
	}
	var hexStr string
	if err := json.Unmarshal(result, &hexStr); err != nil {
		return "", fmt.Errorf("invalid response format: %v", err)
	}
	return hexStr, nil
}

// TxReceipt is the subset of eth_getTransactionReceipt fields this tool checks.
type TxReceipt struct {
	Status  string `json:"status"`
	GasUsed string `json:"gasUsed"`
	Return  string `json:"return"`
}

// GetTransactionReceipt fetches a receipt, or nil if not yet mined.
func (c *RPCClient) GetTransactionReceipt(txHash string) (*TxReceipt, error) {
	result, err := c.call("eth_getTransactionReceipt", txHash)
	if err != nil {
		return nil, err
	}
	if string(result) == "null" || len(result) == 0 {
		return nil, nil
	}
	var r TxReceipt
	if err := json.Unmarshal(result, &r); err != nil {
		return nil, fmt.Errorf("failed to unmarshal receipt: %v", err)
	}
	return &r, nil
}
