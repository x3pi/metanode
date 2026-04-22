// network/config.go

package network

import (
	"runtime"
	"time"
)

// Config chứa tất cả các tham số cấu hình cho module network.
type Config struct {
	MaxMessageLength       uint64
	RequestChanSize        int
	ErrorChanSize          int
	WriteTimeout           time.Duration
	RequestChanWaitTimeout time.Duration
	DialTimeout            time.Duration
	RetryParentInterval    time.Duration
	HandlerWorkerPoolSize  int
	SendChanSize           int // <-- THÊM DÒNG NÀY
	MaxConnections         int // GO-M2: max concurrent TCP connections (0 = default 1000)
}

// DefaultConfig tự động tạo ra một cấu hình mặc định hợp lý
// dựa trên tài nguyên hệ thống có sẵn (cụ thể là số nhân CPU).
// Sử dụng số workers tối thiểu, sẽ tự động scale up khi cần.
func DefaultConfig() *Config {
	numCPU := runtime.NumCPU()
	var numWorkers int
	// Sử dụng số workers tối thiểu để tiết kiệm tài nguyên
	// Với 1-2 giao dịch, chỉ cần vài workers
	// Worker pool sẽ tự động scale up khi có nhiều requests
	if numCPU < 4 {
		numWorkers = 8 // Giảm xuống 8 workers (đủ cho 1-2 giao dịch)
	} else if numCPU < 8 {
		numWorkers = 16 // Tối thiểu 16 workers
	} else if numCPU < 16 {
		numWorkers = 32 // Tối thiểu 32 workers
	} else {
		numWorkers = 64 // Tối thiểu 64 workers cho hệ thống lớn
		// Max workers vẫn giữ ở 256 để có thể scale up khi cần
		if numWorkers > 256 {
			numWorkers = 256
		}
	}

	// Tăng RequestChanSize lên 500,000 để hỗ trợ burst traffic khổng lồ (200000 TXs)
	// Server dùng non-blocking send nên overflow sẽ bị drop ngay lập tức
	requestQueueSize := 500000

	return &Config{
		MaxMessageLength:       256 * 1024 * 1024, // 256MB (genesis state sync can be 170MB+ with many accounts and full trie data)
		HandlerWorkerPoolSize:  numWorkers,
		RequestChanSize:        requestQueueSize,
		SendChanSize:           500000, // Kích thước buffer cho kênh gửi (500K — hỗ trợ burst 200K+ TX)
		ErrorChanSize:          2000,
		WriteTimeout:           10 * time.Second,
		RequestChanWaitTimeout: 30 * time.Second,
		DialTimeout:            10 * time.Second,
		RetryParentInterval:    5 * time.Second,
		MaxConnections:         1000, // GO-M2: default cap; override in Config if needed
	}
}
