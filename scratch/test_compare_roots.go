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

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rpcResp RPCResponse
	err = json.Unmarshal(respBytes, &rpcResp)
	if err != nil {
		return nil, err
	}
	return rpcResp.Result, nil
}

func main() {
	nodes := []struct {
		name string
		url  string
	}{
		{"m0", "http://127.0.0.1:8757"},
		{"m1", "http://127.0.0.1:10747"},
		{"m2", "http://127.0.0.1:10749"},
		{"m3", "http://127.0.0.1:10750"},
		{"m4", "http://127.0.0.1:10748"},
	}

	blockNumHex := "0x1fd" // 509

	fmt.Println("=== Block 510 Details ===")
	for _, n := range nodes {
		block, err := callRPC(n.url, "eth_getBlockByNumber", []interface{}{blockNumHex, false})
		if err != nil {
			fmt.Printf("%s: error: %v\n", n.name, err)
			continue
		}
		if block == nil {
			fmt.Printf("%s: block is nil\n", n.name)
			continue
		}
		txs, _ := block["transactions"].([]interface{})
		fmt.Printf("%s: hash=%s, stateRoot=%s, receiptsRoot=%s, txsRoot=%s, txCount=%d\n",
			n.name, block["hash"], block["stateRoot"], block["receiptsRoot"], block["transactionsRoot"], len(txs))
	}
}
