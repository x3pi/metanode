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
	b0 := getBlock("http://127.0.0.1:8757", "0x931") // 2353
	b1 := getBlock("http://127.0.0.1:10747", "0x931") // 2353

	out := fmt.Sprintf("Block 2353\nNode 0: hash=%v stateRoot=%v txRoot=%v receiptsRoot=%v parentHash=%v timestamp=%v\nNode 1: hash=%v stateRoot=%v txRoot=%v receiptsRoot=%v parentHash=%v timestamp=%v\n", 
		b0["hash"], b0["stateRoot"], b0["transactionsRoot"], b0["receiptsRoot"], b0["parentHash"], b0["timestamp"],
		b1["hash"], b1["stateRoot"], b1["transactionsRoot"], b1["receiptsRoot"], b1["parentHash"], b1["timestamp"])

	os.WriteFile("/home/abc/chain-n/metanode/consensus/metanode/scripts/out_2353.txt", []byte(out), 0644)
}
