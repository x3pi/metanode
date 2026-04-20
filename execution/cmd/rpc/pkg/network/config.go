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

	// Giảm RequestChanSize từ *100 xuống *10 để tiết kiệm memory
	// Vẫn đủ cho burst traffic với worker pool
	requestQueueSize := numWorkers * 10

	return &Config{
		MaxMessageLength:      1024 * 1024 * 1024,
		HandlerWorkerPoolSize: numWorkers,
		RequestChanSize:       requestQueueSize,
		SendChanSize:          65536, // Kích thước buffer cho kênh gửi
		ErrorChanSize:         2000,
		WriteTimeout:          10 * time.Second,
		// RequestChanWaitTimeout: Timeout khi gửi request vào requestChan
		// Tăng từ 5s lên 30s để phù hợp với mạng chậm và xử lý chậm
		// Đặc biệt quan trọng cho InitConnection request - nếu timeout thì connection không được add vào manager
		// 30 giây đủ cho mạng chậm và các handler phức tạp (như ProcessInitConnection)
		RequestChanWaitTimeout: 30 * time.Second,
		DialTimeout:            10 * time.Second,
		RetryParentInterval:    5 * time.Second,
	}
}
