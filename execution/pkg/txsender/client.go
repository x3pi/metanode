// File: txsender/sender.go
package txsender

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Client quản lý một bể kết nối bền bỉ để gửi giao dịch song song.
// Tối ưu cho localhost với thông lượng lớn và production-ready.
type Client struct {
	targetAddress string
	conns         chan net.Conn // Thay thế net.Conn và Mutex bằng một bể kết nối (channel)
	poolSize      int           // Kích thước pool để monitor
	stopMonitor   chan struct{} // Channel để stop background monitor

	// Metrics cho monitoring
	totalSent     atomic.Int64 // Tổng số transaction đã gửi
	totalFailed   atomic.Int64 // Tổng số transaction thất bại
	poolExhausted atomic.Int64 // Số lần pool bị exhausted
	connCreated   atomic.Int64 // Số connection được tạo mới

	// Connection health tracking
	mu          sync.RWMutex
	activeConns int32 // Số connection đang active
}

// NewClient khởi tạo một client mới với một bể kết nối có kích thước cho trước.
// Kết nối được tạo lười (lazy) — pool bắt đầu trống và được lấp đầy bởi monitorConnections.
// Điều này cho phép Go Sub khởi động trước Rust mà không bị broken pipe.
func NewClient(targetAddress string, poolSize int) (*Client, error) {
	if poolSize <= 0 {
		return nil, errors.New("kích thước bể kết nối (poolSize) phải lớn hơn 0")
	}

	// Tạo kênh có bộ đệm (pool bắt đầu trống — lazy init)
	connsChan := make(chan net.Conn, poolSize)

	client := &Client{
		targetAddress: targetAddress,
		conns:         connsChan,
		poolSize:      poolSize,
		stopMonitor:   make(chan struct{}),
	}

	client.activeConns = 0

	// monitorConnections sẽ tự động tạo connections khi target available
	go client.monitorConnections()

	fmt.Printf("🔌 [TX CLIENT] Pool initialized (lazy): target=%s, poolSize=%d — connections will be created when target is available\n", targetAddress, poolSize)
	return client, nil
}

// Connect là một hàm giữ lại để tương thích giao diện.
func (c *Client) Connect() error {
	return nil // No-op để duy trì tính tương thích của API
}

// monitorConnections là background goroutine để monitor và maintain connection pool
// Nó sẽ kiểm tra và thay thế các connection bị hỏng
func (c *Client) monitorConnections() {
	ticker := time.NewTicker(500 * time.Millisecond) // Tăng frequency từ 2s xuống 500ms để maintain pool nhanh hơn
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Kiểm tra số lượng connection trong pool
			currentPoolSize := len(c.conns)
			activeConns := int(atomic.LoadInt32(&c.activeConns))

			if currentPoolSize < c.poolSize {
				// Pool bị thiếu, thêm connection mới
				needed := c.poolSize - currentPoolSize
				created := 0

				for i := 0; i < needed; i++ {
					newConn, err := net.DialTimeout("tcp", c.targetAddress, 3*time.Second) // Giảm timeout xuống 3s
					if err == nil {
						// Tối ưu TCP connection cho localhost
						if tcpConn, ok := newConn.(*net.TCPConn); ok {
							tcpConn.SetNoDelay(true)
							tcpConn.SetKeepAlive(true)
							tcpConn.SetKeepAlivePeriod(30 * time.Second)
							tcpConn.SetReadBuffer(512 * 1024)
							tcpConn.SetWriteBuffer(512 * 1024)
						}

						// Non-blocking send, nếu pool đã đầy thì bỏ qua
						select {
						case c.conns <- newConn:
							atomic.AddInt32(&c.activeConns, 1)
							c.connCreated.Add(1)
							created++
						default:
							// Pool đã đầy, đóng connection này
							newConn.Close()
						}
					}
				}

				if created > 0 {
					fmt.Printf("✅ [TX CLIENT] Monitor: Đã thêm %d connections mới vào pool (current=%d, target=%d)\n",
						created, currentPoolSize+created, c.poolSize)
				}
			}

			// Log metrics mỗi 30 giây
			if time.Now().Unix()%30 == 0 {
				totalSent := c.totalSent.Load()
				totalFailed := c.totalFailed.Load()
				poolExhausted := c.poolExhausted.Load()
				connCreated := c.connCreated.Load()
				fmt.Printf("📊 [TX CLIENT] Metrics: sent=%d, failed=%d, pool_exhausted=%d, conn_created=%d, active_conns=%d, pool_size=%d\n",
					totalSent, totalFailed, poolExhausted, connCreated, activeConns, currentPoolSize)
			}
		case <-c.stopMonitor:
			return
		}
	}
}

// Close đóng tất cả các kết nối trong bể.
func (c *Client) Close() error {
	// Stop monitor goroutine
	close(c.stopMonitor)

	close(c.conns) // Đóng kênh
	var lastErr error
	// Lấy và đóng tất cả các kết nối còn lại trong kênh
	for conn := range c.conns {
		if err := conn.Close(); err != nil {
			lastErr = err
		}
		atomic.AddInt32(&c.activeConns, -1)
	}
	return lastErr
}

// writeData thực hiện logic gửi dữ liệu length-prefixed trên một kết nối cho trước.
// Tối ưu cho localhost với flush mechanism và error handling tốt hơn.
func writeData(conn net.Conn, payload []byte) error {
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(payload)))

	fullMessage := append(lenBuf, payload...)

	// Timeout cho localhost: ngắn hơn vì localhost nhanh
	timeout := 5 * time.Second
	if len(payload) > 100000 { // > 100KB
		timeout = 15 * time.Second
	}

	// Set deadline cho toàn bộ quá trình gửi
	deadline := time.Now().Add(timeout)
	conn.SetWriteDeadline(deadline)
	defer conn.SetWriteDeadline(time.Time{})

	// Gửi dữ liệu với retry logic
	totalWritten := 0
	maxRetries := 2
	retryCount := 0

	for totalWritten < len(fullMessage) {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout khi gửi payload (đã gửi %d/%d bytes, deadline exceeded)",
				totalWritten, len(fullMessage))
		}

		n, err := conn.Write(fullMessage[totalWritten:])
		if err != nil {
			if retryCount < maxRetries {
				errStr := err.Error()
				isRetryable := err == net.ErrWriteToConnected ||
					errStr == "write: broken pipe" ||
					errStr == "write: connection reset by peer" ||
					errStr == "i/o timeout"

				if isRetryable {
					retryCount++
					backoff := time.Duration(retryCount) * 10 * time.Millisecond
					time.Sleep(backoff)
					continue
				}
			}
			return fmt.Errorf("lỗi khi gửi payload giao dịch (đã gửi %d/%d bytes, retry=%d/%d): %w",
				totalWritten, len(fullMessage), retryCount, maxRetries, err)
		}
		totalWritten += n
		retryCount = 0
	}

	return nil
}

// SendTransaction gửi một gói dữ liệu giao dịch bằng cách sử dụng một kết nối từ bể.
// Production-ready với backpressure, circuit breaker, và metrics.
func (c *Client) SendTransaction(transactionPayload []byte) error {
	c.totalSent.Add(1)
	payloadSize := len(transactionPayload)

	// --- Bước 1: Mượn một kết nối từ bể với timeout ---
	// Thao tác này sẽ chờ nếu tất cả các kết nối đang bận, nhưng có timeout để tránh deadlock
	var conn net.Conn
	select {
	case conn = <-c.conns:
		// Có connection available
		atomic.AddInt32(&c.activeConns, -1)
	case <-time.After(5 * time.Second): // Block for up to 5s — never create new connections
		c.totalFailed.Add(1)
		c.poolExhausted.Add(1)
		return fmt.Errorf("connection pool exhausted: all %d connections busy for 5s", c.poolSize)
	}

	// --- Bước 2: Gửi dữ liệu ---
	err := writeData(conn, transactionPayload)

	// --- Bước 3: Xử lý kết quả và trả kết nối ---
	if err != nil {
		c.totalFailed.Add(1)

		fmt.Printf("❌ [TX CLIENT] Gửi thất bại: size=%d bytes, target=%s, error=%v\n",
			payloadSize, c.targetAddress, err)

		// Nếu có lỗi, kết nối này có thể đã hỏng. Đóng nó.
		conn.Close()

		// Cố gắng tạo một kết nối mới để thay thế và duy trì kích thước bể
		newConn, reconnErr := net.DialTimeout("tcp", c.targetAddress, 3*time.Second)
		if reconnErr == nil {
			// Tối ưu TCP connection cho localhost
			if tcpConn, ok := newConn.(*net.TCPConn); ok {
				tcpConn.SetNoDelay(true)
				tcpConn.SetKeepAlive(true)
				tcpConn.SetKeepAlivePeriod(30 * time.Second)
				tcpConn.SetReadBuffer(512 * 1024)
				tcpConn.SetWriteBuffer(512 * 1024)
			}

			// Trả kết nối mới về bể
			select {
			case c.conns <- newConn:
				atomic.AddInt32(&c.activeConns, 1)
				c.connCreated.Add(1)
			default:
				newConn.Close()
			}
		}

		return fmt.Errorf("gửi thất bại, kết nối đã bị hủy: %w", err)
	}

	// Trả connection về pool ngay lập tức (không cần sleep)
	select {
	case c.conns <- conn:
		atomic.AddInt32(&c.activeConns, 1)
	default:
		conn.Close()
	}

	return nil
}
