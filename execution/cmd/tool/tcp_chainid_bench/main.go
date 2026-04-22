package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// ═══════════════════════════════════════════════════════════════════
// tcp_chainid_bench — TCP-Direct GetChainId TPS Benchmark (PIPELINE)
//
// HIGH-THROUGHPUT: Sender và Receiver chạy song song (async pipeline).
// Sender bắn GetChainId liên tục, Receiver đếm ChainId response.
// Không chờ response trước khi gửi request tiếp → pipeline sâu.
//
// Usage:
//   go run . -addr 127.0.0.1:6200 -workers 200 -duration 10s
// ═══════════════════════════════════════════════════════════════════

type benchResult struct {
	successCount uint64
	failedCount  uint64
	sendCount    uint64
	totalLatency int64 // microseconds (approx, based on response timing)
	minLatency   int64 // microseconds
	maxLatency   int64 // microseconds
	errMu        sync.Mutex
	errSample    []string
}

func newBenchResult() *benchResult {
	return &benchResult{
		minLatency: 1<<63 - 1,
	}
}

func (r *benchResult) captureError(msg string) {
	r.errMu.Lock()
	if len(r.errSample) < 5 {
		r.errSample = append(r.errSample, msg)
	}
	r.errMu.Unlock()
}

func (r *benchResult) recordLatency(latencyUs int64) {
	atomic.AddInt64(&r.totalLatency, latencyUs)
	for {
		cur := atomic.LoadInt64(&r.minLatency)
		if latencyUs >= cur || atomic.CompareAndSwapInt64(&r.minLatency, cur, latencyUs) {
			break
		}
	}
	for {
		cur := atomic.LoadInt64(&r.maxLatency)
		if latencyUs <= cur || atomic.CompareAndSwapInt64(&r.maxLatency, cur, latencyUs) {
			break
		}
	}
}

// connectAndInit tạo 1 TCP connection, hoàn thành handshake InitConnection.
func connectAndInit(addr string) (network.Connection, chan network.Request, error) {
	cfg := p_network.DefaultConfig()
	conn := p_network.NewConnection(common.Address{}, "CLIENT", cfg)
	conn.SetRealConnAddr(addr)

	if err := conn.Connect(); err != nil {
		return nil, nil, fmt.Errorf("connect failed: %w", err)
	}

	reqChan, _ := conn.RequestChan()

	// Bước 1: Chờ server gửi InitConnection (server → client)
	select {
	case req, ok := <-reqChan:
		if !ok {
			return nil, nil, fmt.Errorf("request channel closed")
		}
		_ = req.Message().Command()
	case <-time.After(5 * time.Second):
		return nil, nil, fmt.Errorf("timeout waiting for InitConnection from server")
	}

	// Bước 2: Gửi InitConnection ngược lại (client → server) để mở khóa initReady gate
	initMsg := &pb.InitConnection{
		Address: common.Address{}.Bytes(),
		Type:    "CLIENT",
		Replace: true,
	}
	initBody, err := proto.Marshal(initMsg)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal InitConnection: %w", err)
	}
	initNetMsg := p_network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command:   command.InitConnection,
			ToAddress: conn.Address().Bytes(),
		},
		Body: initBody,
	})
	if err := conn.SendMessage(initNetMsg); err != nil {
		return nil, nil, fmt.Errorf("send InitConnection: %w", err)
	}

	// Chờ server xử lý InitConnection
	time.Sleep(50 * time.Millisecond)

	return conn, reqChan, nil
}

// Pre-build GetChainId message bytes (zero-alloc in hot path)
var getChainIdMsg = func() network.Message {
	return p_network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: command.GetChainId,
		},
		Body: nil,
	})
}()

// worker chạy PIPELINE: sender và receiver goroutines song song.
func worker(wg *sync.WaitGroup, addr string, result *benchResult,
	done <-chan struct{}, startWg *sync.WaitGroup, connectedCount *int64) {
	defer wg.Done()

	conn, reqChan, err := connectAndInit(addr)
	if err != nil {
		result.captureError(fmt.Sprintf("connect: %v", err))
		atomic.AddUint64(&result.failedCount, 1)
		atomic.AddInt64(connectedCount, 1)
		startWg.Done()
		return
	}
	defer conn.Disconnect()

	atomic.AddInt64(connectedCount, 1)
	startWg.Done()

	var workerWg sync.WaitGroup

	// ──── RECEIVER GOROUTINE: đếm ChainId responses liên tục ────
	workerWg.Add(1)
	go func() {
		defer workerWg.Done()
		for {
			select {
			case <-done:
				return
			case req, ok := <-reqChan:
				if !ok {
					return
				}
				msg := req.Message()
				if msg.Command() == command.ChainId {
					body := msg.Body()
					if len(body) == 8 {
						_ = binary.BigEndian.Uint64(body) // chainId value
						atomic.AddUint64(&result.successCount, 1)
					} else {
						atomic.AddUint64(&result.failedCount, 1)
					}
				}
				// Bỏ qua các message không phải ChainId (ServerBusy, etc.)
			}
		}
	}()

	// ──── SENDER GOROUTINE: bắn GetChainId liên tục ────
	workerWg.Add(1)
	go func() {
		defer workerWg.Done()
		for {
			select {
			case <-done:
				return
			default:
				// Tạo message mới mỗi lần (vì message có thể bị mutate bởi writeLoop)
				msg := p_network.NewMessage(&pb.Message{
					Header: &pb.Header{
						Command: command.GetChainId,
					},
					Body: nil,
				})
				if err := conn.SendMessage(msg); err != nil {
					atomic.AddUint64(&result.failedCount, 1)
					result.captureError(fmt.Sprintf("send: %v", err))
					time.Sleep(1 * time.Millisecond)
					continue
				}
				atomic.AddUint64(&result.sendCount, 1)
			}
		}
	}()

	workerWg.Wait()
}

func main() {
	addr := flag.String("addr", "127.0.0.1:6200", "Chain node TCP address (host:port)")
	workers := flag.Int("workers", 200, "Number of concurrent TCP connections")
	dur := flag.Duration("duration", 10*time.Second, "Test duration (e.g. 10s, 30s, 1m)")
	flag.Parse()

	// Tắt hoàn toàn pkg/network logger
	logger.SetConsoleOutputEnabled(false)
	logger.SetFlag(0)

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║  📊 TCP GetChainId BENCHMARK (PIPELINE)                  ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Printf("  🌐 Address:   %s\n", *addr)
	fmt.Printf("  👥 Workers:   %d\n", *workers)
	fmt.Printf("  ⏱  Duration:  %s\n", *dur)
	fmt.Println()

	// Quick connectivity check
	fmt.Print("  🔌 Checking connectivity... ")
	testConn, _, err := connectAndInit(*addr)
	if err != nil {
		fmt.Printf("❌ FAILED: %v\n", err)
		os.Exit(1)
	}
	testConn.Disconnect()
	fmt.Println("✅ OK")
	fmt.Println()

	time.Sleep(500 * time.Millisecond)

	result := newBenchResult()
	done := make(chan struct{})
	var wg sync.WaitGroup
	var startWg sync.WaitGroup
	var connectedCount int64

	fmt.Printf("  🚀 Connecting %d workers...\n", *workers)

	startWg.Add(*workers)
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go worker(&wg, *addr, result, done, &startWg, &connectedCount)
		if i%50 == 49 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	startWg.Wait()
	connected := atomic.LoadInt64(&connectedCount)
	fmt.Printf("  ✅ %d/%d workers connected\n", connected, *workers)
	fmt.Println()

	// Reset counters
	atomic.StoreUint64(&result.successCount, 0)
	atomic.StoreUint64(&result.failedCount, 0)
	atomic.StoreUint64(&result.sendCount, 0)
	result.errMu.Lock()
	result.errSample = nil
	result.errMu.Unlock()

	fmt.Printf("  ⏱  Running benchmark for %s ...\n", *dur)

	// Live progress
	ticker := time.NewTicker(time.Second)
	go func() {
		lastOk := uint64(0)
		lastSent := uint64(0)
		elapsed := 0
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				elapsed++
				curOk := atomic.LoadUint64(&result.successCount)
				curSent := atomic.LoadUint64(&result.sendCount)
				deltaOk := curOk - lastOk
				deltaSent := curSent - lastSent
				lastOk = curOk
				lastSent = curSent
				curFail := atomic.LoadUint64(&result.failedCount)
				fmt.Printf("\r     [%2ds] recv: %6d/s | sent: %6d/s | total: %d ok, %d fail   ",
					elapsed, deltaOk, deltaSent, curOk, curFail)
			}
		}
	}()

	time.Sleep(*dur)
	close(done)
	wg.Wait()
	ticker.Stop()

	// Results
	success := atomic.LoadUint64(&result.successCount)
	failed := atomic.LoadUint64(&result.failedCount)
	sent := atomic.LoadUint64(&result.sendCount)
	tps := float64(success) / dur.Seconds()
	sentPerSec := float64(sent) / dur.Seconds()

	fmt.Println()
	fmt.Println()
	fmt.Println("╔════════════════════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  📊 RESULTS                                                                                  ║")
	fmt.Println("╠════════════════════════════════════════════════════════════════════════════════════════════════╣")
	fmt.Printf("  ✅ GetChainId (TCP Pipeline)\n")
	fmt.Printf("     Response TPS:   %10.0f req/s\n", tps)
	fmt.Printf("     Send rate:      %10.0f req/s\n", sentPerSec)
	fmt.Printf("     Success:        %d\n", success)
	fmt.Printf("     Failed:         %d\n", failed)
	fmt.Printf("     Sent:           %d\n", sent)
	fmt.Println("╚════════════════════════════════════════════════════════════════════════════════════════════════╝")
	fmt.Println()

	if len(result.errSample) > 0 {
		fmt.Println("  ⚠️  Sample errors:")
		for i, e := range result.errSample {
			if len(e) > 200 {
				e = e[:200] + "..."
			}
			fmt.Printf("    [%d] %s\n", i+1, e)
		}
		fmt.Println()
	}
}
