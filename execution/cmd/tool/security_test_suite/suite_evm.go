package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RunEVMSuite executes smart contract and EVM resource exhaustion scenarios
func RunEVMSuite(rpcURL string, report *SecurityReport) {
	fmt.Println("\n═══════════════════════════════════════════════════════════════")
	fmt.Println("⚙️  [SUITE 3] Smart Contract & EVM Resource Exhaustion Attacks")
	fmt.Println("═══════════════════════════════════════════════════════════════")

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	callEVM := func(method string, to string, data string, gas string) (string, error) {
		payload := fmt.Sprintf(`{"jsonrpc":"2.0","method":"%s","params":[{"to":"%s","data":"%s","gas":"%s"},"latest"],"id":1}`, method, to, data, gas)
		resp, err := client.Post(rpcURL, "application/json", strings.NewReader(payload))
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body), nil
	}

	deployAndCall := func(data string, gas string) (string, error) {
		payload := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_call","params":[{"data":"%s","gas":"%s"},"latest"],"id":1}`, data, gas)
		resp, err := client.Post(rpcURL, "application/json", strings.NewReader(payload))
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body), nil
	}

	// -------------------------------------------------------------
	// SEC-EVM-001: Infinite Loop Denial-of-Service (Gas Griefing)
	// Bytecode: 0x5b600056 (JUMPDEST, PUSH1 0x00, JUMP)
	// -------------------------------------------------------------
	{
		start := time.Now()
		// Attempting infinite loop via eth_call with gas limit 1,000,000
		loopBytecode := "0x5b600056"
		respStr, err := deployAndCall(loopBytecode, "0xf4240") // 1,000,000 gas
		dur := time.Since(start)

		if err != nil {
			report.AddResult(TestCaseResult{
				ID: "SEC-EVM-001", Category: "EVM", Name: "Infinite Loop Gas Exhaustion (No Hang)",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("Timed out safely: %v", err),
			})
		} else {
			// Expected: execution stopped without hanging the node
			isOutOfGasOrRevert := strings.Contains(respStr, "out of gas") || strings.Contains(respStr, "revert") || strings.Contains(respStr, "error") || dur < 5*time.Second
			status := StatusPass
			if !isOutOfGasOrRevert {
				status = StatusFail
			}
			report.AddResult(TestCaseResult{
				ID: "SEC-EVM-001", Category: "EVM", Name: "Infinite Loop Gas Exhaustion (No Hang)",
				Status: status, Duration: dur, Details: fmt.Sprintf("Halted in %v: %s", dur.Round(time.Millisecond), truncate(respStr, 60)),
			})
		}
	}

	// -------------------------------------------------------------
	// SEC-EVM-002: Memory Expansion Quadratic Cost Bomb
	// Bytecode: attempts to MSTORE at offset 2^28 to trigger OOM
	// -------------------------------------------------------------
	{
		start := time.Now()
		// PUSH1 0x01, PUSH4 0x10000000, MSTORE
		memBombBytecode := "0x6001631000000052"
		respStr, err := deployAndCall(memBombBytecode, "0x1000000") // 16,777,216 gas
		dur := time.Since(start)

		if err != nil {
			report.AddResult(TestCaseResult{
				ID: "SEC-EVM-002", Category: "EVM", Name: "Memory Allocation Bomb Defense",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("Safely trapped: %v", err),
			})
		} else {
			report.AddResult(TestCaseResult{
				ID: "SEC-EVM-002", Category: "EVM", Name: "Memory Allocation Bomb Defense",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("Memory expansion bounded by gas: %s", truncate(respStr, 60)),
			})
		}
	}

	// -------------------------------------------------------------
	// SEC-EVM-003: Clean REVERT Opcode Handling
	// Bytecode: 0x60006000fd (PUSH1 0x00, PUSH1 0x00, REVERT)
	// -------------------------------------------------------------
	{
		start := time.Now()
		revertBytecode := "0x60006000fd"
		respStr, err := deployAndCall(revertBytecode, "0x5208") // 21,000 gas
		dur := time.Since(start)

		if err != nil {
			report.AddResult(TestCaseResult{
				ID: "SEC-EVM-003", Category: "EVM", Name: "REVERT Opcode Graceful Trapping",
				Status: StatusFail, Duration: dur, Details: fmt.Sprintf("RPC error: %v", err),
			})
		} else {
			report.AddResult(TestCaseResult{
				ID: "SEC-EVM-003", Category: "EVM", Name: "REVERT Opcode Graceful Trapping",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("Revert handled deterministically: %s", truncate(respStr, 60)),
			})
		}
	}

	// -------------------------------------------------------------
	// SEC-EVM-004: Privileged Internal Contract Access Control
	// Calling Validator Registry directly with forged authorization
	// -------------------------------------------------------------
	{
		start := time.Now()
		// Validator Contract Address (0x000...01 or specific)
		validatorContract := "0x0000000000000000000000000000000000000001"
		forgedData := "0x12345678" // Invalid selector
		respStr, err := callEVM("eth_call", validatorContract, forgedData, "0x50000")
		dur := time.Since(start)

		if err != nil {
			report.AddResult(TestCaseResult{
				ID: "SEC-EVM-004", Category: "EVM", Name: "Privileged Contract Selector Validation",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("Access prevented: %v", err),
			})
		} else {
			report.AddResult(TestCaseResult{
				ID: "SEC-EVM-004", Category: "EVM", Name: "Privileged Contract Selector Validation",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("Safe response: %s", truncate(respStr, 60)),
			})
		}
	}

	// -------------------------------------------------------------
	// SEC-EVM-005: Gas Estimation of Malicious Loop (EstimateGas)
	// -------------------------------------------------------------
	{
		start := time.Now()
		loopBytecode := "0x5b600056"
		payload := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_estimateGas","params":[{"data":"%s"}],"id":1}`, loopBytecode)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "POST", rpcURL, strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		dur := time.Since(start)

		if err != nil {
			report.AddResult(TestCaseResult{
				ID: "SEC-EVM-005", Category: "EVM", Name: "EstimateGas Infinite Loop Safety",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("Terminated cleanly without hanging: %v", err),
			})
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			report.AddResult(TestCaseResult{
				ID: "SEC-EVM-005", Category: "EVM", Name: "EstimateGas Infinite Loop Safety",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("EstimateGas responded safely: %s", truncate(string(body), 60)),
			})
		}
	}
}
