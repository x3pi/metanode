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
		// Try unmarshalling result as interface if not map
		var rawResp struct {
			Result interface{} `json:"result"`
		}
		json.Unmarshal(respBytes, &rawResp)
		if rawResp.Result != nil {
			return map[string]interface{}{"raw_result": rawResp.Result}, nil
		}
		return nil, fmt.Errorf("unmarshal error: %w (body: %s)", err, string(respBytes))
	}
	return rpcResp.Result, nil
}

func main() {
	m0Url := "http://127.0.0.1:8757"
	m1Url := "http://127.0.0.1:10747"
	blockNumHex := "0x1fe" // 510 is 0x1fe

	fmt.Println("=== Fetching Block 503 from m0 ===")
	m0Block, err := callRPC(m0Url, "eth_getBlockByNumber", []interface{}{blockNumHex, true})
	if err != nil {
		fmt.Printf("Error fetching m0 block: %v\n", err)
		return
	}

	fmt.Println("=== Fetching Block 503 from m1 ===")
	m1Block, err := callRPC(m1Url, "eth_getBlockByNumber", []interface{}{blockNumHex, true})
	if err != nil {
		fmt.Printf("Error fetching m1 block: %v\n", err)
		return
	}

	if m0Block == nil || m1Block == nil {
		fmt.Println("One of the blocks is nil!")
		return
	}

	m0Txs, ok0 := m0Block["transactions"].([]interface{})
	m1Txs, ok1 := m1Block["transactions"].([]interface{})
	if !ok0 || !ok1 {
		fmt.Println("Transactions not found or not slice!")
		return
	}

	fmt.Printf("Tx Count: m0=%d, m1=%d\n", len(m0Txs), len(m1Txs))

	m0TxsMap := make(map[string]map[string]interface{})
	for _, txI := range m0Txs {
		tx := txI.(map[string]interface{})
		hash := tx["hash"].(string)
		m0TxsMap[hash] = tx
	}

	m1TxsMap := make(map[string]map[string]interface{})
	for _, txI := range m1Txs {
		tx := txI.(map[string]interface{})
		hash := tx["hash"].(string)
		m1TxsMap[hash] = tx
	}

	fmt.Println("\n--- Comparing Transaction Details ---")
	diffCount := 0
	for i, txI := range m0Txs {
		tx0 := txI.(map[string]interface{})
		hash := tx0["hash"].(string)
		tx1, exists := m1TxsMap[hash]
		if !exists {
			fmt.Printf("Tx %s exists in m0 but not in m1!\n", hash)
			diffCount++
			continue
		}

		groupId0 := tx0["groupId"]
		txIdx0 := tx0["transactionIndex"]
		groupId1 := tx1["groupId"]
		txIdx1 := tx1["transactionIndex"]

		// Fetch receipts to double check groupIndex / transactionIndex
		rcp0, _ := callRPC(m0Url, "eth_getTransactionReceipt", []interface{}{hash})
		rcp1, _ := callRPC(m1Url, "eth_getTransactionReceipt", []interface{}{hash})

		var rgroupId0, rtxIdx0, rgroupId1, rtxIdx1 interface{}
		if rcp0 != nil {
			rgroupId0 = rcp0["groupIndex"]
			rtxIdx0 = rcp0["transactionIndex"]
		}
		if rcp1 != nil {
			rgroupId1 = rcp1["groupIndex"]
			rtxIdx1 = rcp1["transactionIndex"]
		}

		if groupId0 != groupId1 || txIdx0 != txIdx1 || rgroupId0 != rgroupId1 || rtxIdx0 != rtxIdx1 {
			fmt.Printf("Diff at pos %d, tx=%s:\n", i, hash)
			fmt.Printf("  m0: block[groupId=%v, txIdx=%v], receipt[groupId=%v, txIdx=%v]\n", groupId0, txIdx0, rgroupId0, rtxIdx0)
			fmt.Printf("  m1: block[groupId=%v, txIdx=%v], receipt[groupId=%v, txIdx=%v]\n", groupId1, txIdx1, rgroupId1, rtxIdx1)
			diffCount++
		}
	}

	if diffCount == 0 {
		fmt.Println("✅ ALL transaction grouping & indexing details are identical between m0 and m1!")
	} else {
		fmt.Printf("❌ Found %d differences in transaction grouping/indexing!\n", diffCount)
	}
}
