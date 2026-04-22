package rpc_client

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// WaitForReceipt waits for a transaction receipt with a timeout
func (c *ClientRPC) WaitForReceipt(txHash string, timeout time.Duration) (map[string]interface{}, error) {
	startTime := time.Now()
	checkInterval := 100 * time.Millisecond
	maxAttempts := int(timeout / checkInterval)

	if maxAttempts == 0 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		request := &JSONRPCRequest{
			Jsonrpc: "2.0",
			Method:  "eth_getTransactionReceipt",
			Params:  []interface{}{txHash},
			Id:      1,
		}

		response := c.SendHTTPRequest(request)

		if response.Error != nil {
			if strings.Contains(strings.ToLower(response.Error.Message), "not found") ||
				strings.Contains(strings.ToLower(response.Error.Message), "pending") {
				time.Sleep(checkInterval)
				continue
			}
			return nil, fmt.Errorf("RPC error: %s", response.Error.Message)
		}

		if response.Result != nil {
			resultStr := fmt.Sprintf("%v", response.Result)
			if resultStr == "<nil>" || resultStr == "null" || resultStr == "" {
				time.Sleep(checkInterval)
				continue
			}

			receiptBytes, err := json.Marshal(response.Result)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal receipt: %w", err)
			}

			var receipt map[string]interface{}
			if err := json.Unmarshal(receiptBytes, &receipt); err != nil {
				return nil, fmt.Errorf("failed to unmarshal receipt: %w", err)
			}

			if blockNumber, ok := receipt["blockNumber"]; ok && blockNumber != nil {
				return receipt, nil
			}
			time.Sleep(checkInterval)
			continue
		}

		time.Sleep(checkInterval)
	}

	elapsed := time.Since(startTime)
	return nil, fmt.Errorf("timeout (%v) waiting for receipt with txHash %s", elapsed, txHash)
}
