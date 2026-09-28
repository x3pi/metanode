package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"
)

func rpcCall(url, method string, params []interface{}) (map[string]interface{}, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	})

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var res map[string]interface{}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	if errResp, ok := res["error"]; ok {
		return nil, fmt.Errorf("rpc error: %v", errResp)
	}
	return res, nil
}

func main() {
	targetAddress := "0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5"
	amountHex := "0x3e8" // 1000

	fmt.Printf("1. Bắn giao dịch TransferFloat (1000) từ Cluster 1 (8646) tới %s\n", targetAddress)
	res, err := rpcCall("http://localhost:8646", "mtn_sendCrossChainTransfer", []interface{}{targetAddress, amountHex})
	if err != nil {
		fmt.Printf("❌ Lỗi: %v\n", err)
		return
	}
	fmt.Printf("✅ Gửi thành công! MessageID: %v\n", res["result"])

	fmt.Println("\n2. Chờ 5 giây để P2P Network (Parent Chain) đồng bộ tin nhắn Claimed -> Refund -> Credit...")
	time.Sleep(5 * time.Second)

	fmt.Printf("\n3. Kiểm tra số dư của %s trên Cluster 2 (8647)\n", targetAddress)
	res2, err := rpcCall("http://localhost:8647", "mtn_getAccountState", []interface{}{targetAddress, "latest"})
	if err != nil {
		fmt.Printf("❌ Lỗi: %v\n", err)
		return
	}
	
	resultMap, ok := res2["result"].(map[string]interface{})
	if !ok || resultMap == nil {
		fmt.Printf("⚠️ Account chưa tồn tại (hoặc balance = 0).\n")
	} else {
		fmt.Printf("✅ Balance hiện tại: %v\n", resultMap["balance"])
	}
}
