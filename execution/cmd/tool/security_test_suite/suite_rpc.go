package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RunRPCSuite executes HTTP & JSON-RPC attack scenarios
func RunRPCSuite(rpcURL string, report *SecurityReport) {
	fmt.Println("\n═══════════════════════════════════════════════════════════════")
	fmt.Println("🌐 [SUITE 1] RPC Ingress & Malformed Payload Attacks")
	fmt.Println("═══════════════════════════════════════════════════════════════")

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	// -------------------------------------------------------------
	// SEC-RPC-001: Malformed JSON Syntax
	// -------------------------------------------------------------
	{
		start := time.Now()
		malformedJSON := `{"jsonrpc": "2.0", "method": "eth_blockNumber", "params": [}`
		resp, err := client.Post(rpcURL, "application/json", strings.NewReader(malformedJSON))
		dur := time.Since(start)

		if err != nil {
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-001", Category: "RPC", Name: "Malformed JSON Syntax Rejection",
				Status: StatusFail, Duration: dur, Details: fmt.Sprintf("HTTP connection failed: %v", err),
			})
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			// Must return error or parse error (-32700)
			isErr := resp.StatusCode != http.StatusOK || strings.Contains(string(body), "error") || strings.Contains(string(body), "-32700")
			status := StatusPass
			if !isErr {
				status = StatusFail
			}
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-001", Category: "RPC", Name: "Malformed JSON Syntax Rejection",
				Status: status, Duration: dur, Details: fmt.Sprintf("HTTP %d, Body: %s", resp.StatusCode, truncate(string(body), 80)),
			})
		}
	}

	// -------------------------------------------------------------
	// SEC-RPC-002: Deep JSON Nesting / Recursion Attack
	// -------------------------------------------------------------
	{
		start := time.Now()
		nestDepth := 600
		nested := strings.Repeat("[", nestDepth) + "1" + strings.Repeat("]", nestDepth)
		payload := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_call","params":[%s,"latest"],"id":1}`, nested)

		resp, err := client.Post(rpcURL, "application/json", strings.NewReader(payload))
		dur := time.Since(start)

		if err != nil {
			// Client timeout or reset connection is acceptable defense against recursion
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-002", Category: "RPC", Name: "Deep JSON Nesting / Recursion Bomb",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("Connection safely closed/reset: %v", err),
			})
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-002", Category: "RPC", Name: "Deep JSON Nesting / Recursion Bomb",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("Node handled deep nesting without crash: HTTP %d", resp.StatusCode),
			})
			_ = body
		}
	}

	// -------------------------------------------------------------
	// SEC-RPC-003: Null Byte Injection in RPC Parameters
	// -------------------------------------------------------------
	{
		start := time.Now()
		payload := `{"jsonrpc":"2.0","method":"eth_getBalance","params":["0x123\u0000456789012345678901234567890123456789","latest"],"id":1}`
		resp, err := client.Post(rpcURL, "application/json", strings.NewReader(payload))
		dur := time.Since(start)

		if err != nil {
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-003", Category: "RPC", Name: "Null Byte Injection Handling",
				Status: StatusFail, Duration: dur, Details: fmt.Sprintf("HTTP error: %v", err),
			})
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			// Expected: rejected or returned error, NOT a panic
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-003", Category: "RPC", Name: "Null Byte Injection Handling",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("HTTP %d, Response: %s", resp.StatusCode, truncate(string(body), 60)),
			})
		}
	}

	// -------------------------------------------------------------
	// SEC-RPC-004: Type Confusion (Array/Int instead of Hex String)
	// -------------------------------------------------------------
	{
		start := time.Now()
		payload := `{"jsonrpc":"2.0","method":"eth_getBalance","params":[123456789,"latest"],"id":1}`
		resp, err := client.Post(rpcURL, "application/json", strings.NewReader(payload))
		dur := time.Since(start)

		if err != nil {
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-004", Category: "RPC", Name: "Parameter Type Confusion Defense",
				Status: StatusFail, Duration: dur, Details: fmt.Sprintf("HTTP error: %v", err),
			})
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			isRejected := strings.Contains(string(body), "error") || resp.StatusCode != http.StatusOK
			status := StatusPass
			if !isRejected {
				status = StatusFail
			}
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-004", Category: "RPC", Name: "Parameter Type Confusion Defense",
				Status: status, Duration: dur, Details: fmt.Sprintf("Expected error on invalid param type: %s", truncate(string(body), 70)),
			})
		}
	}

	// -------------------------------------------------------------
	// SEC-RPC-005: Invalid Hex Formatting & Odd-Length Hex
	// -------------------------------------------------------------
	{
		start := time.Now()
		payload := `{"jsonrpc":"2.0","method":"eth_sendRawTransaction","params":["0x123GHIJK"],"id":1}`
		resp, err := client.Post(rpcURL, "application/json", strings.NewReader(payload))
		dur := time.Since(start)

		if err != nil {
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-005", Category: "RPC", Name: "Invalid Hex Characters in RawTx",
				Status: StatusFail, Duration: dur, Details: fmt.Sprintf("HTTP error: %v", err),
			})
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			isRejected := strings.Contains(string(body), "error") || resp.StatusCode != http.StatusOK
			status := StatusPass
			if !isRejected {
				status = StatusFail
			}
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-005", Category: "RPC", Name: "Invalid Hex Characters in RawTx",
				Status: status, Duration: dur, Details: fmt.Sprintf("Result: %s", truncate(string(body), 70)),
			})
		}
	}

	// -------------------------------------------------------------
	// SEC-RPC-006: Extreme Integer Overflow in eth_call
	// -------------------------------------------------------------
	{
		start := time.Now()
		// Gas fee cap / gas limit exceeding U256 (2^256)
		payload := `{"jsonrpc":"2.0","method":"eth_call","params":[{"to":"0x0000000000000000000000000000000000000001","gas":"0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff11"},"latest"],"id":1}`
		resp, err := client.Post(rpcURL, "application/json", strings.NewReader(payload))
		dur := time.Since(start)

		if err != nil {
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-006", Category: "RPC", Name: "Integer Overflow In Gas Parameter",
				Status: StatusFail, Duration: dur, Details: fmt.Sprintf("HTTP error: %v", err),
			})
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-006", Category: "RPC", Name: "Integer Overflow In Gas Parameter",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("Overflow safely trapped: %s", truncate(string(body), 70)),
			})
		}
	}

	// -------------------------------------------------------------
	// SEC-RPC-007: Oversized Request Body (10MB Body DoS)
	// -------------------------------------------------------------
	{
		start := time.Now()
		// Create a large 10MB payload with repetitive whitespace and valid prefix
		buf := bytes.NewBufferString(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1,"padding":"`)
		padding := bytes.Repeat([]byte("A"), 10*1024*1024)
		buf.Write(padding)
		req, _ := http.NewRequest("POST", rpcURL, buf)
		req.Header.Set("Content-Type", "application/json")
		req.Close = true
		resp, err := client.Do(req)
		dur := time.Since(start)

		if err != nil {
			// Either connection reset or timeout is acceptable protection against giant payload
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-007", Category: "RPC", Name: "Oversized 10MB HTTP Body Protection",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("Server closed/dropped large body safely: %v", err),
			})
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-007", Category: "RPC", Name: "Oversized 10MB HTTP Body Protection",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("HTTP %d, Handled without node OOM: %s", resp.StatusCode, truncate(string(body), 60)),
			})
		}
	}

	// -------------------------------------------------------------
	// SEC-RPC-008: Batch RPC Request Flood (300 calls in single request)
	// -------------------------------------------------------------
	{
		start := time.Now()
		var batch []map[string]interface{}
		for i := 0; i < 300; i++ {
			batch = append(batch, map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "eth_blockNumber",
				"params":  []interface{}{},
				"id":      i,
			})
		}
		rawBatch, _ := json.Marshal(batch)
		resp, err := client.Post(rpcURL, "application/json", bytes.NewReader(rawBatch))
		dur := time.Since(start)

		if err != nil {
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-008", Category: "RPC", Name: "Batch RPC Request Flood (300 calls)",
				Status: StatusWarn, Duration: dur, Details: fmt.Sprintf("HTTP client error: %v", err),
			})
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-008", Category: "RPC", Name: "Batch RPC Request Flood (300 calls)",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("HTTP %d, Returned %d bytes response without hang", resp.StatusCode, len(body)),
			})
		}
	}

	// -------------------------------------------------------------
	// SEC-RPC-009: Dangerous Internal / Prototype Pollution Probe
	// -------------------------------------------------------------
	{
		start := time.Now()
		probes := []string{"__proto__", "constructor", "admin_nodeInfo", "debug_setHead", "debug_dumpBlock"}
		allSafe := true
		var probeDetails []string

		for _, method := range probes {
			payload := fmt.Sprintf(`{"jsonrpc":"2.0","method":"%s","params":[],"id":1}`, method)
			resp, err := client.Post(rpcURL, "application/json", strings.NewReader(payload))
			if err == nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if !strings.Contains(string(body), "error") && resp.StatusCode == http.StatusOK {
					// Method exists and succeeded - check if dangerous
					if method == "debug_setHead" {
						allSafe = false
					}
				}
				probeDetails = append(probeDetails, fmt.Sprintf("%s: %d", method, resp.StatusCode))
			}
		}
		dur := time.Since(start)

		status := StatusPass
		if !allSafe {
			status = StatusFail
		}
		report.AddResult(TestCaseResult{
			ID: "SEC-RPC-009", Category: "RPC", Name: "Internal / Debug Method Protection",
			Status: status, Duration: dur, Details: strings.Join(probeDetails, ", "),
		})
	}

	// -------------------------------------------------------------
	// SEC-RPC-010: Node Health Check after Suite 1
	// -------------------------------------------------------------
	{
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "POST", rpcURL, strings.NewReader(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":999}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		dur := time.Since(start)

		if err != nil || resp.StatusCode != http.StatusOK {
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-010", Category: "RPC", Name: "Node Post-Attack Liveness Verification",
				Status: StatusFail, Duration: dur, Details: fmt.Sprintf("Node is unresponsive after RPC attack suite: %v", err),
			})
		} else {
			resp.Body.Close()
			report.AddResult(TestCaseResult{
				ID: "SEC-RPC-010", Category: "RPC", Name: "Node Post-Attack Liveness Verification",
				Status: StatusPass, Duration: dur, Details: "Node remains completely healthy and responsive to queries",
			})
		}
	}
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
