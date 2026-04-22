package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
)

// --- Cấu hình ---
const (
	targetURL       = "http://127.0.0.1:8545"
	duration        = 30 * time.Second // Thời gian chạy test
	concurrency     = 1000             // Số lượng "worker" chạy song song. HÃY TĂNG DẦN SỐ NÀY
	expectedAddress = "0x781e6ec6ebdca11be4b53865a34c0c7f10b6da6e"
	requestTimeout  = 300 * time.Second
)

// Payload JSON
var requestBody = []byte(`{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "mtn_getAccountState",
    "params": [
      "0x781E6EC6EBDCA11Be4B53865a34C0c7f10b6da6e",
      "latest"
    ]
  }`)

// Biến đếm (sử dụng atomic để an toàn)
var (
	totalRequests    uint64
	successCorrect   uint64
	successIncorrect uint64
	failedRequests   uint64
	sampleErrorLogs  uint64
	sampleMismatch   uint64
)

// worker là một goroutine sẽ liên tục gửi request
func worker(wg *sync.WaitGroup, client *http.Client, done <-chan struct{}) {
	defer wg.Done()

	for {
		select {
		case <-done: // Nhận tín hiệu dừng
			return
		default:
			// Tạo request mới (phải tạo mới mỗi lần)
			req, err := http.NewRequest("POST", targetURL, bytes.NewBuffer(requestBody))
			if err != nil {
				continue // Bỏ qua nếu không tạo được req
			}
			req = req.WithContext(context.Background())
			ctx, cancel := context.WithTimeout(req.Context(), requestTimeout)
			req = req.WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")

			// Gửi request
			atomic.AddUint64(&totalRequests, 1) // Đếm tổng số
			res, err := client.Do(req)
			cancel()
			if err != nil {
				atomic.AddUint64(&failedRequests, 1) // Lỗi kết nối, timeout...
				continue
			}

			// Kiểm tra status code
			if res.StatusCode != http.StatusOK {
				atomic.AddUint64(&failedRequests, 1) // Lỗi server (5xx, 4xx)
				res.Body.Close()
				continue
			}

			// Đọc body
			body, err := io.ReadAll(res.Body)
			res.Body.Close() // Đóng body ngay lập tức
			if err != nil {
				atomic.AddUint64(&failedRequests, 1) // Lỗi khi đọc body
				continue
			}

			// Phân loại phản hồi lỗi JSON-RPC (có field error)
			if errField := gjson.GetBytes(body, "error"); errField.Exists() {
				atomic.AddUint64(&failedRequests, 1)
				if atomic.LoadUint64(&sampleErrorLogs) < 10 {
					if atomic.AddUint64(&sampleErrorLogs, 1) <= 10 {
						log.Printf("[RPC ERROR] %s", body)
					}
				}
				continue
			}

			// --- Đây là phần KIỂM TRA NỘI DUNG (tốn CPU) ---
			actualAddress := gjson.GetBytes(body, "result.address").String()

			if strings.EqualFold(actualAddress, expectedAddress) {
				atomic.AddUint64(&successCorrect, 1) // THÀNH CÔNG - ĐÚNG
			} else {
				atomic.AddUint64(&successIncorrect, 1) // THÀNH CÔNG - SAI
				if atomic.LoadUint64(&sampleMismatch) < 10 {
					if atomic.AddUint64(&sampleMismatch, 1) <= 10 {
						log.Printf("[MISMATCH] expect=%s got=%s body=%s", expectedAddress, actualAddress, body)
					}
				}
			}
		}
	}
}

func main() {
	// Tăng ulimit (file descriptors) trước khi chạy code này
	// `ulimit -n 65536`

	// Tạo một HTTP client có thể tái sử dụng
	// Tối ưu transport để giữ kết nối (keep-alive)
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        concurrency,
			MaxIdleConnsPerHost: concurrency,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: 30 * time.Second, // Timeout cho mỗi request
	}

	var wg sync.WaitGroup
	done := make(chan struct{}) // Channel để gửi tín hiệu dừng

	fmt.Printf("Bắt đầu spam %s với %d worker trong %s...\n", targetURL, concurrency, duration)

	// Khởi chạy các worker
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go worker(&wg, client, done)
	}

	// Chờ hết thời gian
	time.Sleep(duration)

	// Gửi tín hiệu dừng
	close(done)
	// Chờ tất cả worker dừng hoàn toàn
	wg.Wait()

	// In kết quả
	fmt.Println("\n--- KẾT QUẢ TEST ---")
	fmt.Printf("Thời gian chạy: %s\n", duration)
	fmt.Printf("Tổng số requests: %d\n", totalRequests)
	fmt.Printf("  Thành công (ĐÚNG data): %d\n", successCorrect)
	fmt.Printf("  Thành công (SAI data): %d\n", successIncorrect)
	fmt.Printf("  Thất bại (Lỗi): %d\n", failedRequests)
	fmt.Println("---")

	// Tính toán và in TPS
	totalSuccess := successCorrect + successIncorrect
	tpsSuccess := float64(totalSuccess) / duration.Seconds()
	tpsTotal := float64(totalRequests) / duration.Seconds()

	fmt.Printf("TPS (Tổng cộng): %.2f req/s\n", tpsTotal)
	fmt.Printf("TPS (Thành công): %.2f req/s\n", tpsSuccess)
}
