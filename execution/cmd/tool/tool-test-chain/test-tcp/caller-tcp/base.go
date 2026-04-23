//go:build tool

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp/config"
)

type ToolConfig struct {
	ConnectionAddresses []string               `json:"connection_addresses"`
	Type                string                 `json:"type"`
	Params              map[string]interface{} `json:"params"`
}

func main() {
	configPath, err := resolveConfigPath(os.Args[1:])
	if err != nil {
		fmt.Printf("Invalid arguments: %v\n", err)
		fmt.Println("Usage: go run -tags tool base.go [-config=./base/config-local-multi.json]")
		os.Exit(1)
	}

	baseCfg, toolCfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Printf("Failed to load config: %v\n", err)
		os.Exit(1)
	}

	if len(toolCfg.ConnectionAddresses) == 0 {
		fmt.Println("Config error: connection_addresses is required (or parent_connection_address for fallback)")
		os.Exit(1)
	}

	handlers := map[string]func(*client_tcp.Client, ToolConfig) (string, error){
		"get_logs":      runGetLogs,
		"account_state": runAccountState,
		"get_chain_id":  runGetChainID,
	}

	runType := strings.ToLower(strings.TrimSpace(toolCfg.Type))
	handler, ok := handlers[runType]
	if !ok {
		fmt.Printf("Unsupported type: %q\n", toolCfg.Type)
		fmt.Println("Supported types: get_logs, account_state, get_chain_id")
		os.Exit(1)
	}

	fmt.Printf("Running TCP type: %s\n", runType)
	fmt.Printf("%-30s | %-45s\n", "Connection", "Result")
	fmt.Println(strings.Repeat("-", 86))

	for _, connAddr := range toolCfg.ConnectionAddresses {
		cfgCopy := *baseCfg
		cfgCopy.ParentConnectionAddress = connAddr

		cli, err := client_tcp.NewClient(&cfgCopy)
		if err != nil {
			fmt.Printf("%-30s | Error: connect failed: %v\n", connAddr, err)
			continue
		}

		// Give connection a short moment to be fully ready.
		time.Sleep(300 * time.Millisecond)

		result, err := handler(cli, toolCfg)
		if err != nil {
			fmt.Printf("%-30s | Error: %v\n", connAddr, err)
			continue
		}
		fmt.Printf("%-30s | %s\n", connAddr, result)
	}
}

func loadConfig(path string) (*tcp_config.ClientConfig, ToolConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, ToolConfig{}, err
	}

	baseCfgI, err := tcp_config.LoadConfig(path)
	if err != nil {
		return nil, ToolConfig{}, err
	}
	baseCfg := baseCfgI.(*tcp_config.ClientConfig)

	var toolCfg ToolConfig
	if err := json.Unmarshal(raw, &toolCfg); err != nil {
		return nil, ToolConfig{}, err
	}

	if len(toolCfg.ConnectionAddresses) == 0 && strings.TrimSpace(baseCfg.ParentConnectionAddress) != "" {
		toolCfg.ConnectionAddresses = []string{baseCfg.ParentConnectionAddress}
	}

	return baseCfg, toolCfg, nil
}

func runGetLogs(cli *client_tcp.Client, cfg ToolConfig) (string, error) {
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
	}

	rawResp, err := cli.ChainGetLogs(nil, fromBlock, toBlock, nil, nil)
	if err != nil {
		return "", fmt.Errorf("raw logs: %w", err)
	}
	rawCount := len(rawResp.Logs)

	addresses, err := parseAddressList(cfg.Params["addresses"])
	if err != nil {
		return "", fmt.Errorf("params.addresses: %w", err)
	}
	if len(addresses) == 0 {
		if oneAddress, ok := readStringParam(cfg.Params, "address"); ok {
			if !common.IsHexAddress(oneAddress) {
				return "", fmt.Errorf("params.address is not a valid hex address")
			}
			addresses = []common.Address{common.HexToAddress(oneAddress)}
		}
	}

	topics, err := parseTopics(cfg.Params["topics"])
	if err != nil {
		return "", fmt.Errorf("params.topics: %w", err)
	}

	filteredResp, err := cli.ChainGetLogs(nil, fromBlock, toBlock, addresses, topics)
	if err != nil {
		return "", fmt.Errorf("filtered logs: %w", err)
	}

	return fmt.Sprintf("raw_logs=%d | filtered_logs=%d", rawCount, len(filteredResp.Logs)), nil
}

func runAccountState(cli *client_tcp.Client, cfg ToolConfig) (string, error) {
	address, ok := readStringParam(cfg.Params, "address")
	if !ok {
		return "", fmt.Errorf("params.address is required")
	}
	if !common.IsHexAddress(address) {
		return "", fmt.Errorf("params.address is not a valid hex address")
	}

	nonce, err := cli.ChainGetNonce(common.HexToAddress(address))
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("nonce=%d", nonce), nil
}

func runGetChainID(cli *client_tcp.Client, _ ToolConfig) (string, error) {
	chainID, err := cli.ChainGetChainId()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("chain_id=%d", chainID), nil
}

func parseAddressList(raw interface{}) ([]common.Address, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("must be an array of hex addresses")
	}
	addresses := make([]common.Address, 0, len(list))
	for _, item := range list {
		value, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("address value must be string")
		}
		value = strings.TrimSpace(value)
		if !common.IsHexAddress(value) {
			return nil, fmt.Errorf("invalid address %q", value)
		}
		addresses = append(addresses, common.HexToAddress(value))
	}
	return addresses, nil
}

func parseTopics(raw interface{}) ([][]common.Hash, error) {
	if raw == nil {
		return nil, nil
	}
	topicGroups, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("must be nested array, example: [[\"0x...\",\"0x...\"]]")
	}

	result := make([][]common.Hash, 0, len(topicGroups))
	for _, groupRaw := range topicGroups {
		group, ok := groupRaw.([]interface{})
		if !ok {
			return nil, fmt.Errorf("each topic group must be an array")
		}
		hashes := make([]common.Hash, 0, len(group))
		for _, topicRaw := range group {
			topic, ok := topicRaw.(string)
			if !ok {
				return nil, fmt.Errorf("topic must be string")
			}
			topic = strings.TrimSpace(topic)
			if !isHexTopic(topic) {
				return nil, fmt.Errorf("invalid topic %q", topic)
			}
			hashes = append(hashes, common.HexToHash(topic))
		}
		result = append(result, hashes)
	}
	return result, nil
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
			return strings.ToLower(val), nil
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
		return hexutil.EncodeUint64(num), nil
	case float64:
		if v < 0 || v != float64(uint64(v)) {
			return "", fmt.Errorf("invalid numeric block %v", v)
		}
		return hexutil.EncodeUint64(uint64(v)), nil
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

func isHexTopic(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(strings.ToLower(value), "0x") {
		return false
	}
	_, err := hexutil.Decode(value)
	return err == nil
}

func resolveConfigPath(args []string) (string, error) {
	configPath := "./base/config-local-multi.json"
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
