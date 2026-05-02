package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

type RPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type RPCResponse struct {
	Result map[string]interface{} `json:"result"`
}

func getBlock(url string, number string) map[string]interface{} {
	reqData := RPCRequest{
		JSONRPC: "2.0",
		Method:  "eth_getBlockByNumber",
		Params:  []interface{}{number, true},
		ID:      1,
	}
	body, _ := json.Marshal(reqData)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Println("Error:", err)
		return nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var rpcResp RPCResponse
	json.Unmarshal(respBody, &rpcResp)
	return rpcResp.Result
}

func main() {
	b0 := getBlock("http://127.0.0.1:8757", "0x931") // 2353 in hex
	b1 := getBlock("http://127.0.0.1:10747", "0x931")

	fmt.Printf("Node 0: hash=%v stateRoot=%v txRoot=%v receiptsRoot=%v parentHash=%v\n", b0["hash"], b0["stateRoot"], b0["transactionsRoot"], b0["receiptsRoot"], b0["parentHash"])
	fmt.Printf("Node 1: hash=%v stateRoot=%v txRoot=%v receiptsRoot=%v parentHash=%v\n", b1["hash"], b1["stateRoot"], b1["transactionsRoot"], b1["receiptsRoot"], b1["parentHash"])
}
