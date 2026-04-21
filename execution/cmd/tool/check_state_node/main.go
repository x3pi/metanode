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
	Jsonrpc string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type RPCResponse struct {
	Jsonrpc string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Result  struct {
		Nonce uint64 `json:"nonce"`
	} `json:"result"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <address>")
		os.Exit(1)
	}

	address := os.Args[1]
	endpoints := []string{
		"http://127.0.0.1:8757",
		"http://127.0.0.1:10747",
		"http://127.0.0.1:10748",
		"http://127.0.0.1:10749",
		"http://127.0.0.1:10750",
	}

	fmt.Printf("Checking account: %s\n", address)
	fmt.Printf("%-25s | %-10s\n", "Endpoint", "Nonce")
	fmt.Println("------------------------------------------")

	for _, url := range endpoints {
		nonce, err := getNonce(url, address)
		if err != nil {
			fmt.Printf("%-25s | Error: %v\n", url, err)
		} else {
			fmt.Printf("%-25s | %-10d\n", url, nonce)
		}
	}
}

func getNonce(url, address string) (uint64, error) {
	reqBody := RPCRequest{
		Jsonrpc: "2.0",
		ID:      1,
		Method:  "mtn_getAccountState",
		Params:  []interface{}{address, "latest"},
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

	return rpcResp.Result.Nonce, nil
}
