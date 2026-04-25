//go:build tool

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	RPCURLs        []string               `json:"rpc_urls"`
	RPCURL         string                 `json:"rpc_url"`
	Type           string                 `json:"type"`
	TimeoutSeconds int                    `json:"timeout_seconds"`
	Params         map[string]interface{} `json:"params"`
}

type RPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type AccountStateResult struct {
	Nonce uint64 `json:"nonce"`
}

func main() {
	configPath, err := resolveConfigPath(os.Args[1:])
	if err != nil {
		fmt.Printf("Invalid arguments: %v\n", err)
		fmt.Println("Usage: go run -tags tool base.go [-config=./config-local.json]")
		os.Exit(1)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Printf("Failed to load config: %v\n", err)
		os.Exit(1)
	}

	if len(cfg.RPCURLs) == 0 {
		fmt.Println("Config error: rpc_urls is required (or rpc_url for backward compatibility)")
		os.Exit(1)
	}

	handlers := map[string]func(string, Config) (string, error){
		"get_logs":                runGetLogs,
		"account_state":           runAccountState,
		"get_chain_id":            runGetChainID,
		"get_transaction_by_hash": runGetTransactionByHash,
		"get_block_transactions":  runGetBlockTransactions,
	}

	runType := strings.ToLower(strings.TrimSpace(cfg.Type))
	handler, ok := handlers[runType]
	if !ok {
		fmt.Printf("Unsupported type: %q\n", cfg.Type)
		fmt.Println("Supported types: get_logs, account_state, get_chain_id")
		os.Exit(1)
	}

	fmt.Printf("Running type: %s\n", runType)
	fmt.Printf("%-30s | %-40s\n", "RPC URL", "Result")
	fmt.Println(strings.Repeat("-", 76))

	for _, url := range cfg.RPCURLs {
		result, err := handler(url, cfg)
		if err != nil {
			fmt.Printf("%-30s | Error: %v\n", url, err)
			continue
		}
		fmt.Printf("%-30s | %s\n", url, result)
	}
}

func loadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}

	if len(cfg.RPCURLs) == 0 && strings.TrimSpace(cfg.RPCURL) != "" {
		cfg.RPCURLs = []string{cfg.RPCURL}
	}

	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 10
	}

	return cfg, nil
}

func runGetLogs(url string, cfg Config) (string, error) {
	fromBlockRaw, ok := readParam(cfg.Params, "from_block")
	if !ok {
		return "", fmt.Errorf("params.from_block is required")
	}
	fromBlock, err := normalizeBlockParam(fromBlockRaw)
	if err != nil {
		return "", fmt.Errorf("params.from_block: %w", err)
	}

	toBlock := fromBlock
	if toBlockRaw, ok := readParam(cfg.Params, "to_block"); ok {
		toBlock, err = normalizeBlockParam(toBlockRaw)
		if err != nil {
			return "", fmt.Errorf("params.to_block: %w", err)
		}
	} else {
		toBlock = fromBlock
	}

	rawFilter := map[string]interface{}{
		"fromBlock": fromBlock,
		"toBlock":   toBlock,
	}
	rawCount, err := getLogsCount(url, cfg.TimeoutSeconds, rawFilter)
	if err != nil {
		return "", fmt.Errorf("raw logs: %w", err)
	}

	filteredFilter := map[string]interface{}{
		"fromBlock": fromBlock,
		"toBlock":   toBlock,
	}

	if address, ok := readStringParam(cfg.Params, "address"); ok {
		filteredFilter["address"] = address
	}

	if topics, ok := cfg.Params["topics"]; ok {
		filteredFilter["topics"] = topics
	}

	filteredCount, err := getLogsCount(url, cfg.TimeoutSeconds, filteredFilter)
	if err != nil {
		return "", fmt.Errorf("filtered logs: %w", err)
	}

	return fmt.Sprintf("raw_logs=%d | filtered_logs=%d", rawCount, filteredCount), nil
}

func getLogsCount(url string, timeoutSeconds int, filter map[string]interface{}) (int, error) {
	result, err := callRPC(url, timeoutSeconds, "eth_getLogs", []interface{}{filter})
	if err != nil {
		return 0, err
	}

	var logs []json.RawMessage
	if err := json.Unmarshal(result, &logs); err != nil {
		return 0, fmt.Errorf("failed to parse logs result: %w", err)
	}

	return len(logs), nil
}

func runAccountState(url string, cfg Config) (string, error) {
	address, ok := readStringParam(cfg.Params, "address")
	if !ok {
		return "", fmt.Errorf("params.address is required")
	}

	blockTag, ok := readStringParam(cfg.Params, "block_tag")
	if !ok {
		blockTag = "latest"
	}

	result, err := callRPC(url, cfg.TimeoutSeconds, "mtn_getAccountState", []interface{}{address, blockTag})
	if err != nil {
		return "", err
	}

	var state AccountStateResult
	if err := json.Unmarshal(result, &state); err != nil {
		return "", fmt.Errorf("failed to parse account state result: %w", err)
	}

	return fmt.Sprintf("nonce=%d", state.Nonce), nil
}

func runGetChainID(url string, cfg Config) (string, error) {
	result, err := callRPC(url, cfg.TimeoutSeconds, "eth_chainId", []interface{}{})
	if err != nil {
		return "", err
	}

	var chainID string
	if err := json.Unmarshal(result, &chainID); err != nil {
		return "", fmt.Errorf("failed to parse chain id: %w", err)
	}

	return fmt.Sprintf("chain_id=%s", chainID), nil
}

func runGetTransactionByHash(url string, cfg Config) (string, error) {
	hash, ok := readStringParam(cfg.Params, "hash")
	if !ok {
		return "", fmt.Errorf("params.hash is required")
	}

	result, err := callRPC(url, cfg.TimeoutSeconds, "eth_getTransactionByHash", []interface{}{hash})
	if err != nil {
		return "", err
	}

	if string(result) == "null" {
		return "null (không tìm thấy transaction)", nil
	}

	var tx map[string]interface{}
	if err := json.Unmarshal(result, &tx); err != nil {
		return "", fmt.Errorf("failed to parse result: %w", err)
	}

	blockHash, _ := tx["blockHash"].(string)
	blockNumber, _ := tx["blockNumber"].(string)
	txIndex, _ := tx["transactionIndex"].(string)

	return fmt.Sprintf("blockNumber=%s | blockHash=%s | txIndex=%s", blockNumber, blockHash, txIndex), nil
}

func runGetBlockTransactions(url string, cfg Config) (string, error) {
	blockRaw, ok := readParam(cfg.Params, "block")
	if !ok {
		return "", fmt.Errorf("params.block is required")
	}
	block, err := normalizeBlockParam(blockRaw)
	if err != nil {
		return "", fmt.Errorf("params.block: %w", err)
	}

	result, err := callRPC(url, cfg.TimeoutSeconds, "eth_getBlockByNumber", []interface{}{block, false})
	if err != nil {
		return "", err
	}

	if string(result) == "null" {
		return "null (block không tồn tại)", nil
	}

	var blockData map[string]interface{}
	if err := json.Unmarshal(result, &blockData); err != nil {
		return "", fmt.Errorf("failed to parse result: %w", err)
	}

	txsRaw, ok := blockData["transactions"].([]interface{})
	if !ok {
		return "0 transactions", nil
	}

	txCount := len(txsRaw)
	if txCount == 0 {
		return "0 transactions", nil
	}

	var txHashes []string
	for _, tx := range txsRaw {
		if hashStr, ok := tx.(string); ok {
			txHashes = append(txHashes, hashStr)
		}
	}

	return fmt.Sprintf("%d transactions: %v", txCount, txHashes), nil
}

func callRPC(url string, timeoutSeconds int, method string, params interface{}) (json.RawMessage, error) {
	request := RPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var response RPCResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, err
	}

	if response.Error != nil {
		return nil, fmt.Errorf("rpc error (%d): %s", response.Error.Code, response.Error.Message)
	}

	return response.Result, nil
}

func readStringParam(params map[string]interface{}, key string) (string, bool) {
	value, ok := readParam(params, key)
	if !ok {
		return "", false
	}
	str, ok := value.(string)
	if !ok || strings.TrimSpace(str) == "" {
		return "", false
	}
	return str, true
}

func readParam(params map[string]interface{}, key string) (interface{}, bool) {
	if params == nil {
		return nil, false
	}
	value, ok := params[key]
	if !ok {
		return nil, false
	}
	return value, true
}

func normalizeBlockParam(value interface{}) (string, error) {
	switch v := value.(type) {
	case string:
		val := strings.TrimSpace(v)
		if val == "" {
			return "", fmt.Errorf("value is empty")
		}
		if isBlockTag(val) {
			return val, nil
		}
		if strings.HasPrefix(strings.ToLower(val), "0x") {
			if _, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(val), "0x"), 16, 64); err != nil {
				return "", fmt.Errorf("invalid hex block %q", val)
			}
			return strings.ToLower(val), nil
		}
		num, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return "", fmt.Errorf("invalid decimal block %q", val)
		}
		return fmt.Sprintf("0x%x", num), nil
	case float64:
		if v < 0 || v != float64(uint64(v)) {
			return "", fmt.Errorf("invalid numeric block %v", v)
		}
		return fmt.Sprintf("0x%x", uint64(v)), nil
	default:
		return "", fmt.Errorf("unsupported block type %T", value)
	}
}

func isBlockTag(value string) bool {
	switch strings.ToLower(value) {
	case "latest", "pending", "earliest", "safe", "finalized":
		return true
	default:
		return false
	}
}

func resolveConfigPath(args []string) (string, error) {
	configPath := "config-local.json"
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			continue
		}
		if strings.HasPrefix(arg, "-config=") || strings.HasPrefix(arg, "--config=") {
			parts := strings.SplitN(arg, "=", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
				return "", fmt.Errorf("missing value for -config")
			}
			configPath = strings.TrimSpace(parts[1])
			continue
		}
		if arg == "-config" || arg == "--config" {
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return "", fmt.Errorf("missing value for -config")
			}
			configPath = strings.TrimSpace(args[i+1])
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return "", fmt.Errorf("unknown flag %q", arg)
		}
		configPath = arg
	}
	return configPath, nil
}
