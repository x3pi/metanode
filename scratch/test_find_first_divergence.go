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

func getBlockHash(url string, blockNum int) (string, error) {
	blockNumHex := fmt.Sprintf("0x%x", blockNum)
	reqBody := RPCRequest{
		Jsonrpc: "2.0",
		Method:  "eth_getBlockByNumber",
		Params:  []interface{}{blockNumHex, false},
		Id:      1,
	}
	bodyBytes, _ := json.Marshal(reqBody)

	resp, err := http.Post(url, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	var rpcResp RPCResponse
	json.Unmarshal(respBytes, &rpcResp)
	if rpcResp.Result == nil {
		return "", fmt.Errorf("block %d is nil", blockNum)
	}
	return rpcResp.Result["hash"].(string), nil
}

func main() {
	m0Url := "http://127.0.0.1:8757"
	m2Url := "http://127.0.0.1:10749"

	for i := 1; i <= 510; i++ {
		h0, err0 := getBlockHash(m0Url, i)
		h2, err2 := getBlockHash(m2Url, i)
		if err0 != nil || err2 != nil {
			fmt.Printf("Block %d: err0=%v, err2=%v\n", i, err0, err2)
			break
		}
		if h0 != h2 {
			fmt.Printf("Divergence starts at Block %d:\n  m0 hash: %s\n  m2 hash: %s\n", i, h0, h2)
			return
		}
	}
	fmt.Println("All blocks up to 510 are identical.")
}
