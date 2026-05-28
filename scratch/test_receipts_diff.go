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
	m0Url := "http://127.0.0.1:8757"
	m1Url := "http://127.0.0.1:10749"
	blockNumHex := "0x1fe" // 510

	m0Block, _ := callRPC(m0Url, "eth_getBlockByNumber", []interface{}{blockNumHex, true})
	m0Txs := m0Block["transactions"].([]interface{})

	diffCount := 0
	for _, txI := range m0Txs {
		tx := txI.(map[string]interface{})
		hash := tx["hash"].(string)

		rcp0, _ := callRPC(m0Url, "eth_getTransactionReceipt", []interface{}{hash})
		rcp1, _ := callRPC(m1Url, "eth_getTransactionReceipt", []interface{}{hash})

		if rcp0 == nil || rcp1 == nil {
			fmt.Printf("Receipt is nil for %s: m0=%v, m1=%v\n", hash, rcp0 != nil, rcp1 != nil)
			diffCount++
			continue
		}

		// Compare fields: status, gasUsed, logs, contractAddress
		status0 := rcp0["status"]
		status1 := rcp1["status"]
		gasUsed0 := rcp0["gasUsed"]
		gasUsed1 := rcp1["gasUsed"]
		logs0, _ := json.Marshal(rcp0["logs"])
		logs1, _ := json.Marshal(rcp1["logs"])

		if status0 != status1 || gasUsed0 != gasUsed1 || string(logs0) != string(logs1) {
			fmt.Printf("Diff in Receipt for tx=%s:\n", hash)
			if status0 != status1 {
				fmt.Printf("  Status: m0=%v, m1=%v\n", status0, status1)
			}
			if gasUsed0 != gasUsed1 {
				fmt.Printf("  GasUsed: m0=%v, m1=%v\n", gasUsed0, gasUsed1)
			}
			if string(logs0) != string(logs1) {
				fmt.Printf("  Logs: \n    m0: %s\n    m1: %s\n", string(logs0), string(logs1))
			}
			diffCount++
		}
	}
	if diffCount == 0 {
		fmt.Println("All receipts are identical!")
	} else {
		fmt.Printf("Total divergent receipts: %d\n", diffCount)
	}
}
