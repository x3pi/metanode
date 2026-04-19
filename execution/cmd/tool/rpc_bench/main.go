package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ═══════════════════════════════════════════════════════════════════
// rpc_bench — JSON-RPC Read Benchmark Tool
//
// Tests read throughput of eth_blockNumber and eth_chainId endpoints.
// Supports configurable concurrency, duration, and multiple methods.
//
// Usage:
//   go run . -url http://127.0.0.1:8545 -workers 100 -duration 10s
//   go run . -method chainId -workers 500 -duration 30s
//   go run . -method all -workers 200 -duration 15s
// ═══════════════════════════════════════════════════════════════════

var (
	// JSON-RPC payloads (pre-built for zero allocation in hot path)
	blockNumberPayload    = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`)
	chainIdPayload        = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}`)
	gasPricePayload       = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_gasPrice","params":[]}`)
	getBlockLatestPayload = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["latest",false]}`)
	netVersionPayload     = []byte(`{"jsonrpc":"2.0","id":1,"method":"net_version","params":[]}`)
	getBalancePayload     = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x0000000000000000000000000000000000000000","latest"]}`)
)

type benchResult struct {
	totalRequests uint64
	successCount  uint64
	failedCount   uint64
	totalLatency  int64 // microseconds
	minLatency    int64 // microseconds
	maxLatency    int64 // microseconds
	// Debug: capture first few errors
	errMu     sync.Mutex
	errSample []string
}

func newBenchResult() *benchResult {
	return &benchResult{
		minLatency: 1<<63 - 1, // max int64
	}
}

func (r *benchResult) captureError(msg string) {
	r.errMu.Lock()
	if len(r.errSample) < 5 {
		r.errSample = append(r.errSample, msg)
	}
	r.errMu.Unlock()
}

func (r *benchResult) recordSuccess(latencyUs int64) {
	atomic.AddUint64(&r.totalRequests, 1)
	atomic.AddUint64(&r.successCount, 1)
	atomic.AddInt64(&r.totalLatency, latencyUs)

	// Update min (lockless CAS loop)
	for {
		cur := atomic.LoadInt64(&r.minLatency)
		if latencyUs >= cur || atomic.CompareAndSwapInt64(&r.minLatency, cur, latencyUs) {
			break
		}
	}
	// Update max
	for {
		cur := atomic.LoadInt64(&r.maxLatency)
		if latencyUs <= cur || atomic.CompareAndSwapInt64(&r.maxLatency, cur, latencyUs) {
			break
		}
	}
}

func (r *benchResult) recordFailure() {
	atomic.AddUint64(&r.totalRequests, 1)
	atomic.AddUint64(&r.failedCount, 1)
}

func worker(wg *sync.WaitGroup, client *http.Client, url string, payload []byte,
	result *benchResult, done <-chan struct{}) {
	defer wg.Done()

	for {
		select {
		case <-done:
			return
		default:
			req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
			if err != nil {
				result.recordFailure()
				continue
			}
			req.Header.Set("Content-Type", "application/json")

			start := time.Now()
			res, err := client.Do(req)
			latency := time.Since(start)

			if err != nil {
				result.recordFailure()
				result.captureError(fmt.Sprintf("http_err: %v", err))
				// Brief sleep to avoid tight error loop
				time.Sleep(5 * time.Millisecond)
				continue
			}

			// Drain and close body (MUST do this for keep-alive connection reuse)
			io.Copy(io.Discard, res.Body)
			res.Body.Close()

			if res.StatusCode != 200 {
				result.recordFailure()
				result.captureError(fmt.Sprintf("status=%d", res.StatusCode))
				continue
			}

			result.recordSuccess(latency.Microseconds())
		}
	}
}

// newHTTPClient creates an HTTP client that strictly reuses keep-alive
// connections without leaking ephemeral ports.
//
// KEY DESIGN: MaxConnsPerHost == workers ensures that the client
// will never dial more than `workers` TCP connections. Combined with
// DisableKeepAlives=false, connections are reused across requests.
// The client-level Timeout replaces per-request context.WithTimeout
// so that cancelled contexts don't break keep-alive connections.
func newHTTPClient(workers int) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:        workers,
			MaxIdleConnsPerHost: workers,
			MaxConnsPerHost:     workers, // STRICT: never exceed this many connections
			IdleConnTimeout:     90 * time.Second,
			DisableKeepAlives:   false,
			// Prevent lingering half-open connections
			ResponseHeaderTimeout: 10 * time.Second,
		},
		Timeout: 10 * time.Second,
	}
}

func runBench(name string, url string, payload []byte, workers int, duration time.Duration) *benchResult {

	// Each test gets its own HTTP client to avoid connection state
	// pollution between sequential tests (the core fix for eth_chainId
	// 100% failure after eth_blockNumber).
	client := newHTTPClient(workers)

	result := newBenchResult()
	done := make(chan struct{})
	var wg sync.WaitGroup

	fmt.Printf("  🚀 %-20s %d workers × %s ...", name, workers, duration)

	// Launch workers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go worker(&wg, client, url, payload, result, done)
	}

	// Live progress ticker
	ticker := time.NewTicker(time.Second)
	go func() {
		lastCount := uint64(0)
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				cur := atomic.LoadUint64(&result.successCount)
				delta := cur - lastCount
				lastCount = cur
				_ = delta // Could print live TPS here
			}
		}
	}()

	time.Sleep(duration)
	close(done)
	wg.Wait()
	ticker.Stop()

	// Close all connections from this test — releases ephemeral ports
	client.Transport.(*http.Transport).CloseIdleConnections()
	client.CloseIdleConnections()

	// Print inline result
	total := atomic.LoadUint64(&result.totalRequests)
	success := atomic.LoadUint64(&result.successCount)
	failed := atomic.LoadUint64(&result.failedCount)
	tps := float64(success) / duration.Seconds()

	var avgLatency, minLat, maxLat float64
	if success > 0 {
		avgLatency = float64(atomic.LoadInt64(&result.totalLatency)) / float64(success) / 1000.0 // ms
		minLat = float64(atomic.LoadInt64(&result.minLatency)) / 1000.0
		maxLat = float64(atomic.LoadInt64(&result.maxLatency)) / 1000.0
	}

	fmt.Printf("\r  ✅ %-20s %10.0f req/s | %d ok / %d fail / %d total | latency: %.2f / %.2f / %.2f ms (min/avg/max)\n",
		name, tps, success, failed, total, minLat, avgLatency, maxLat)

	// Print error samples if any
	if len(result.errSample) > 0 {
		fmt.Printf("     ⚠️  Sample errors:\n")
		for i, e := range result.errSample {
			if len(e) > 200 {
				e = e[:200] + "..."
			}
			fmt.Printf("       [%d] %s\n", i+1, e)
		}
	}

	return result
}

func main() {
	url := flag.String("url", "http://127.0.0.1:8545", "JSON-RPC endpoint URL")
	workers := flag.Int("workers", 100, "Number of concurrent workers")
	dur := flag.Duration("duration", 10*time.Second, "Test duration (e.g. 10s, 30s, 1m)")
	method := flag.String("method", "all", "Method to test: blockNumber, chainId, or all")
	flag.Parse()

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║  📊 RPC READ BENCHMARK                                   ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Printf("  🌐 URL:       %s\n", *url)
	fmt.Printf("  👥 Workers:   %d\n", *workers)
	fmt.Printf("  ⏱  Duration:  %s\n", *dur)
	fmt.Printf("  🎯 Method:    %s\n", *method)
	fmt.Println()

	// Quick connectivity check
	fmt.Print("  🔌 Checking connectivity... ")
	resp, err := http.Post(*url, "application/json", bytes.NewReader(blockNumberPayload))
	if err != nil {
		fmt.Printf("❌ FAILED: %v\n", err)
		os.Exit(1)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Printf("✅ OK (eth_blockNumber → %s)\n\n", string(body))

	type testCase struct {
		name    string
		payload []byte
	}

	var tests []testCase

	switch *method {
	case "blockNumber":
		tests = []testCase{{"eth_blockNumber", blockNumberPayload}}
	case "chainId":
		tests = []testCase{{"eth_chainId", chainIdPayload}}
	case "gasPrice":
		tests = []testCase{{"eth_gasPrice", gasPricePayload}}
	case "getBlock":
		tests = []testCase{{"eth_getBlockByNumber", getBlockLatestPayload}}
	case "getBalance":
		tests = []testCase{{"eth_getBalance", getBalancePayload}}
	case "netVersion":
		tests = []testCase{{"net_version", netVersionPayload}}
	case "all":
		tests = []testCase{
			{"eth_blockNumber", blockNumberPayload},
			{"eth_chainId", chainIdPayload},
			{"eth_gasPrice", gasPricePayload},
			{"eth_getBlockByNumber", getBlockLatestPayload},
			{"eth_getBalance", getBalancePayload},
		}
	default:
		fmt.Printf("  ❌ Unknown method: %s (use: blockNumber, chainId, gasPrice, getBlock, getBalance, netVersion, all)\n", *method)
		os.Exit(1)
	}

	fmt.Println("╔═══════════════════════════════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  Method                     TPS      Success / Failed / Total       Latency (min / avg / max)           ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════════════════════════════════════════════════════╣")

	var results []*benchResult
	for i, tc := range tests {
		if i > 0 {
			// Wait for TIME_WAIT sockets from previous test to clear
			fmt.Print("  ⏳ Cooldown 5s (wait for port recycling)...\r")
			time.Sleep(5 * time.Second)
		}
		r := runBench(tc.name, *url, tc.payload, *workers, *dur)
		results = append(results, r)
	}

	fmt.Println("╚═══════════════════════════════════════════════════════════════════════════════════════════════════════════╝")
	fmt.Println()

	// Summary
	fmt.Println("══════════════════════════════════════")
	fmt.Println("  📋 SUMMARY")
	fmt.Println("══════════════════════════════════════")
	for i, tc := range tests {
		r := results[i]
		success := atomic.LoadUint64(&r.successCount)
		tps := float64(success) / dur.Seconds()
		fmt.Printf("  %-25s %8.0f req/s\n", tc.name, tps)
	}
	fmt.Println("══════════════════════════════════════")
	fmt.Println()
}
