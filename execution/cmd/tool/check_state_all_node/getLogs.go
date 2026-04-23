//go:build ignore

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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

type RPCRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type RPCResponse struct {
	Jsonrpc string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Result  []interface{} `json:"result"`
	Error   interface{}   `json:"error"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <block_number>")
		os.Exit(1)
	}

	blockStr := os.Args[1]
	var blockNum uint64
	var blockHex string

	if strings.HasPrefix(blockStr, "0x") {
		blockHex = blockStr
		// Parse hex to decimal for display
		parsed, _ := strconv.ParseUint(blockStr[2:], 16, 64)
		blockNum = parsed
	} else {
		parsed, err := strconv.ParseUint(blockStr, 10, 64)
		if err != nil {
			fmt.Printf("Invalid block number: %s\n", blockStr)
			os.Exit(1)
		}
		blockNum = parsed
		blockHex = hexutil.EncodeUint64(blockNum)
	}

	endpoints := []string{
		"http://192.168.1.233:8757",
		"http://192.168.1.233:10747",
		"http://192.168.1.233:10749",
		"http://192.168.1.233:10750",
	}

	fmt.Printf("Checking logs for block: %d (%s) via HTTP RPC\n", blockNum, blockHex)
	fmt.Printf("%-25s | %-10s | %-10s\n", "Endpoint", "Filtered", "Raw")
	fmt.Println("-------------------------------------------------------")

	for _, url := range endpoints {
		count, err := getLogsCount(url, blockHex)
		rawCount, _ := getRawLogsCount(url, blockHex)
		if err != nil {
			fmt.Printf("%-25s | Error: %v\n", url, err)
		} else {
			fmt.Printf("%-25s | %-10d | %-10d\n", url, count, rawCount)
		}
	}
}

func getLogsCount(url, blockHex string) (int, error) {
	// Topics used by observer
	messageSentTopic := common.HexToHash("0xb528e3a3d4cbfd0b61a83cc28a004e801777b8ed6274adee62a727632fee66dd")
	messageReceivedTopic := common.HexToHash("0xa92be8788ad097ce638b4b327d9930cc1d8545abf05c0a399f37b7a6ce8b94ce")
	contractAddr := common.HexToAddress("0x00000000000000000000000000000000B429C0B2")

	// Filter params object
	filterParams := map[string]interface{}{
		"fromBlock": blockHex,
		"toBlock":   blockHex,
		"address":   contractAddr.Hex(),
		"topics": []interface{}{
			[]string{messageSentTopic.Hex(), messageReceivedTopic.Hex()},
		},
	}

	reqBody := RPCRequest{
		Jsonrpc: "2.0",
		ID:      1,
		Method:  "eth_getLogs",
		Params:  []interface{}{filterParams},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return 0, err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var rpcResp RPCResponse
	err = json.Unmarshal(body, &rpcResp)
	if err != nil {
		return 0, err
	}

	if rpcResp.Error != nil {
		return 0, fmt.Errorf("RPC Error: %v", rpcResp.Error)
	}

	return len(rpcResp.Result), nil
}

func getRawLogsCount(url, blockHex string) (int, error) {
	filterParams := map[string]interface{}{
		"fromBlock": blockHex,
		"toBlock":   blockHex,
	}

	reqBody := RPCRequest{
		Jsonrpc: "2.0",
		ID:      2,
		Method:  "eth_getLogs",
		Params:  []interface{}{filterParams},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return 0, err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var rpcResp RPCResponse
	err = json.Unmarshal(body, &rpcResp)
	if err != nil {
		return 0, err
	}

	if rpcResp.Error != nil {
		return 0, fmt.Errorf("RPC Error: %v", rpcResp.Error)
	}

	return len(rpcResp.Result), nil
}
