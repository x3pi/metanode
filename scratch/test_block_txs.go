package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type RPCRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	Id      int           `json:"id"`
}

type RPCResponse struct {
	Jsonrpc string                 `json:"jsonrpc"`
	Result  map[string]interface{} `json:"result"`
	Error   interface{}            `json:"error"`
	Id      int                    `json:"id"`
}

func callRPC(url string, method string, params []interface{}) (map[string]interface{}, error) {
	reqBody := RPCRequest{
		Jsonrpc: "2.0",
		Method:  method,
		Params:  params,
		Id:      1,
	}
	bodyBytes, _ := json.Marshal(reqBody)

	resp, err := http.Post(url, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	var rpcResp RPCResponse
	json.Unmarshal(respBytes, &rpcResp)
	return rpcResp.Result, nil
}

func main() {
	m0Url := "http://127.0.0.1:8757"
	blockNumHex := "0x1fe" // 510

	block, _ := callRPC(m0Url, "eth_getBlockByNumber", []interface{}{blockNumHex, true})
	txs := block["transactions"].([]interface{})

	fmt.Printf("Block 503 Txs (%d):\n", len(txs))
	for i, txI := range txs {
		tx := txI.(map[string]interface{})
		hash := tx["hash"].(string)
		from := tx["from"].(string)
		to := tx["to"].(string)
		input := tx["input"].(string)
		groupId := tx["groupId"]
		txIdx := tx["transactionIndex"]

		// If input is long, truncate it
		if len(input) > 20 {
			input = input[:20] + "..."
		}
		fmt.Printf("  [%2d] tx=%s from=%s to=%s grp=%v txIdx=%v input=%s\n", i, hash[:10], from[:10], to[:10], groupId, txIdx, input)
	}
}
