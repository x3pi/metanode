package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
)

func rpcCall(port int, method string, params []interface{}) (map[string]interface{}, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	})

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var res map[string]interface{}
	json.Unmarshal(body, &res)
	return res, nil
}

func main() {
	txHash := "0x0b1f8f10c89bcdb50e11db14f1ad4b9795f6bafe198e687885cf30ae056eac5f"
	tx0, err := rpcCall(8757, "eth_getTransactionByHash", []interface{}{txHash})
	if err != nil {
		fmt.Println("Error 8757:", err)
	} else {
		fmt.Println("Tx0:")
		out, _ := json.MarshalIndent(tx0["result"], "", "  ")
		fmt.Println(string(out))
	}

	tx3, err := rpcCall(10750, "eth_getTransactionByHash", []interface{}{txHash})
	if err != nil {
		fmt.Println("Error 10750:", err)
	} else {
		fmt.Println("Tx3:")
		out, _ := json.MarshalIndent(tx3["result"], "", "  ")
		fmt.Println(string(out))
	}
}
