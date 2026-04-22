// Channel-based sender với multiple workers cho localhost
// Tối ưu cho việc gửi liên tục các nhóm giao dịch
package txsender

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// ChannelBasedSender sử dụng buffered channel và multiple workers
// để gửi transaction batches liên tục
type ChannelBasedSender struct {
	// Buffered channel để queue batches (1000 batches buffer)
	batchChan chan []byte

	// Retry channel: batches bị reject do epoch transition sẽ được retry sau
	retryChan chan []byte

	// Workers: nhiều goroutines đọc từ channel và gửi
	workers []*ChannelWorker

	// Target address (UDS hoặc TCP)
	targetAddress string

	// Số lượng workers (mỗi worker có persistent connection riêng)
	workerCount int

	// Metrics
	totalSent    atomic.Int64
	totalFailed  atomic.Int64
	totalBatches atomic.Int64
	totalRetried atomic.Int64

	// Control
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// ChannelWorker là một worker goroutine có persistent connection riêng
type ChannelWorker struct {
	id        int
	conn      net.Conn // Persistent connection
	batchChan <-chan []byte
	stopChan  <-chan struct{}
	sender    *ChannelBasedSender
}

// NewChannelBasedSender tạo sender mới với channel-based architecture
func NewChannelBasedSender(targetAddress string, workerCount int, channelBuffer int) (*ChannelBasedSender, error) {
	if workerCount <= 0 {
		workerCount = 10 // Default: 10 workers
	}
	if channelBuffer <= 0 {
		channelBuffer = 1000 // Default: 1000 batches buffer
	}

	sender := &ChannelBasedSender{
		batchChan:     make(chan []byte, channelBuffer),
		retryChan:     make(chan []byte, channelBuffer), // Retry channel với cùng buffer size
		workers:       make([]*ChannelWorker, 0, workerCount),
		targetAddress: targetAddress,
		workerCount:   workerCount,
		stopChan:      make(chan struct{}),
	}

	// Tạo workers với persistent connections
	for i := 0; i < workerCount; i++ {
		worker, err := sender.createWorker(i)
		if err != nil {
			// Log warning nhưng tiếp tục với các workers khác
			logger.Warn("⚠️  [CHANNEL SENDER] Không thể tạo worker %d: %v (sẽ retry sau)", i, err)
			continue
		}
		sender.workers = append(sender.workers, worker)
	}

	if len(sender.workers) == 0 {
		return nil, fmt.Errorf("không thể tạo bất kỳ worker nào")
	}

	// Start workers
	for _, worker := range sender.workers {
		sender.wg.Add(1)
		go worker.start()
	}

	// Start retry worker: retry các batches bị reject do epoch transition
	sender.wg.Add(1)
	go sender.retryWorker()

	logger.Info("✅ [CHANNEL SENDER] Đã tạo %d workers với channel buffer %d", len(sender.workers), channelBuffer)

	return sender, nil
}

// createWorker tạo một worker với persistent connection
func (s *ChannelBasedSender) createWorker(id int) (*ChannelWorker, error) {
	// Tạo persistent connection
	var conn net.Conn
	var err error

	// Thử UDS trước (localhost) - UDS path bắt đầu bằng '/'
	if len(s.targetAddress) > 0 && s.targetAddress[0] == '/' {
		// UDS path
		conn, err = net.Dial("unix", s.targetAddress)
	} else {
		// TCP
		conn, err = net.DialTimeout("tcp", s.targetAddress, 5*time.Second)
	}

	if err != nil {
		return nil, fmt.Errorf("không thể tạo connection cho worker %d: %w", id, err)
	}

	// Tối ưu connection cho localhost
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
		tcpConn.SetReadBuffer(512 * 1024)
		tcpConn.SetWriteBuffer(512 * 1024)
	}

	// Tối ưu cho UDS: set buffers lên 32MB để tránh nghẽn khi gửi batch lớn
	if unixConn, ok := conn.(*net.UnixConn); ok {
		if err := unixConn.SetWriteBuffer(32 * 1024 * 1024); err != nil {
			logger.Warn("⚠️  [CHANNEL WORKER %d] Could not set UDS write buffer to 32MB: %v", id, err)
		}
		if err := unixConn.SetReadBuffer(32 * 1024 * 1024); err != nil {
			logger.Warn("⚠️  [CHANNEL WORKER %d] Could not set UDS read buffer to 32MB: %v", id, err)
		}
	}

	return &ChannelWorker{
		id:        id,
		conn:      conn,
		batchChan: s.batchChan,
		stopChan:  s.stopChan,
		sender:    s,
	}, nil
}

// start worker loop: đọc từ channel và gửi
func (w *ChannelWorker) start() {
	defer w.sender.wg.Done()
	defer w.conn.Close()

	logger.Info("🚀 [CHANNEL WORKER %d] Started with persistent connection", w.id)

	for {
		select {
		case batch := <-w.batchChan:
			// Lightweight batch logging (no deserialization)
			logger.Debug("📥 [CHANNEL WORKER %d] Received batch from channel: size=%d bytes, channel_remaining=%d",
				w.id, len(batch), len(w.batchChan))

			// Gửi batch qua persistent connection và đọc response
			// IN-WORKER RETRY: If rejected (epoch transition), retry HERE with backoff
			// instead of bouncing through retryChan which causes infinite loops.
			const maxEpochRetries = 30              // Max 30 retries
			const epochRetryDelay = 4 * time.Second // 4s between retries (120s total max, matches receipt timeout)

			for attempt := 0; attempt <= maxEpochRetries; attempt++ {
				rejected, err := w.sendBatchWithResponse(batch)
				if err != nil {
					logger.Error("❌ [CHANNEL WORKER %d] Failed to send batch (size=%d, attempt=%d): %v", w.id, len(batch), attempt, err)

					// Retry: tạo connection mới và gửi lại batch
					if newConn, reconnErr := w.reconnect(); reconnErr == nil {
						w.conn.Close()
						w.conn = newConn
						logger.Info("✅ [CHANNEL WORKER %d] Reconnected, will retry batch (size=%d) in %v", w.id, len(batch), epochRetryDelay)
						// GIẢI QUYẾT LỖI MẤT BATCH: Bắt buộc sleep kể cả khi reconnect thành công ngay lập tức!
						// Nếu không có sleep này, Go sẽ retry 30 lần trong vài mili-giây nếu Rust liên tục trả về lỗi app-level, dẫn đến drop batch vĩnh viễn
						time.Sleep(epochRetryDelay)
						continue // Retry with new connection
					} else {
						if attempt < maxEpochRetries {
							logger.Warn("⚠️  [CHANNEL WORKER %d] Reconnect failed: %v, retrying %d/%d in %v", w.id, reconnErr, attempt+1, maxEpochRetries, epochRetryDelay)
							time.Sleep(epochRetryDelay)
							continue
						} else {
							w.sender.totalFailed.Add(1)
							logger.Warn("⚠️  [CHANNEL WORKER %d] Batch DROPPED after %d network retries (size=%d)", w.id, maxEpochRetries, len(batch))
							break // Give up after max retries
						}
					}
				} else if rejected {
					if attempt < maxEpochRetries {
						logger.Info("⏳ [CHANNEL WORKER %d] Batch rejected (epoch transition), retry %d/%d in %v (size=%d)",
							w.id, attempt+1, maxEpochRetries, epochRetryDelay, len(batch))
						time.Sleep(epochRetryDelay)
						continue // Retry after delay
					} else {
						w.sender.totalFailed.Add(1)
						logger.Warn("⚠️  [CHANNEL WORKER %d] Batch DROPPED after %d epoch-transition retries (size=%d)", w.id, maxEpochRetries, len(batch))
						break // Give up after max retries
					}
				} else {
					w.sender.totalSent.Add(1)
					logger.Debug("✅ [CHANNEL WORKER %d] Sent batch: size=%d bytes", w.id, len(batch))
					break // Success
				}
			}
		case <-w.stopChan:
			logger.Info("🛑 [CHANNEL WORKER %d] Stopped", w.id)
			return
		}
	}
}

// sendBatchWithResponse gửi batch và đọc response từ Rust
// Returns (rejected, error) where rejected=true nếu bị reject do epoch transition
func (w *ChannelWorker) sendBatchWithResponse(batch []byte) (bool, error) {
	// Length-prefixed: [4 bytes: length][data]
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(batch)))

	fullMessage := append(lenBuf, batch...)

	// Set write deadline (120s safety net for extreme blasts and epoch transitions)
	w.conn.SetWriteDeadline(time.Now().Add(120 * time.Second))

	// Gửi data payload + header atomical sequence to prevent TCP issues
	_, err := w.conn.Write(fullMessage)
	if err != nil {
		w.conn.SetWriteDeadline(time.Time{})
		return false, err
	}

	// Đọc response từ Rust: [4 bytes: response_length][response_json]
	// Response format: {"success":true/false,"error":"..."}
	w.conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	defer w.conn.SetReadDeadline(time.Time{})
	defer w.conn.SetWriteDeadline(time.Time{})

	// Đọc response length (4 bytes)
	respLenBuf := make([]byte, 4)
	if _, err := io.ReadFull(w.conn, respLenBuf); err != nil {
		return false, fmt.Errorf("failed to read response length: %w", err)
	}

	respLen := binary.BigEndian.Uint32(respLenBuf)
	if respLen == 0 || respLen > 1024*1024 { // Max 1MB response
		return false, fmt.Errorf("invalid response length: %d", respLen)
	}

	// Đọc response data
	respData := make([]byte, respLen)
	if _, err := io.ReadFull(w.conn, respData); err != nil {
		return false, fmt.Errorf("failed to read response data: %w", err)
	}

	// Parse JSON response to check if rejected
	// Response format: {"success":true/false,"queued":true/false,"error":"..."}
	respStr := strings.ToLower(string(respData))
	if len(respStr) > 0 && (respStr[0] == '{' || respStr[0] == '[') {
		// SUCCESS CHECK: If response contains "success":true or "queued":true, treat as accepted
		if strings.Contains(respStr, `"success":true`) || strings.Contains(respStr, `"queued":true`) {
			return false, nil // Not rejected — Rust accepted or queued the TX
		}
		// REJECTION CHECK: Only reject if success is false AND contains error keywords
		if strings.Contains(respStr, "epoch transition") ||
			strings.Contains(respStr, "not ready") ||
			strings.Contains(respStr, "node not ready") ||
			strings.Contains(respStr, "fetching") ||
			strings.Contains(respStr, "initializing") ||
			strings.Contains(respStr, "catching up") ||
			strings.Contains(respStr, "initialization") {
			return true, nil // Rejected (Retryable)
		}
		// Handle UNKNOWN errors explicitly: return the error so it's not silently treated as a success
		return false, fmt.Errorf("UDS sender reported error: %s", respStr)
	}

	// Non-JSON response (hoặc rỗng)
	return false, fmt.Errorf("invalid response format from UDS sender: %s", respStr)
}

// reconnect tạo connection mới
func (w *ChannelWorker) reconnect() (net.Conn, error) {
	var conn net.Conn
	var err error

	// UDS path bắt đầu bằng '/'
	if len(w.sender.targetAddress) > 0 && w.sender.targetAddress[0] == '/' {
		conn, err = net.Dial("unix", w.sender.targetAddress)
	} else {
		conn, err = net.DialTimeout("tcp", w.sender.targetAddress, 5*time.Second)
	}

	if err != nil {
		return nil, err
	}

	// Tối ưu connection
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
		tcpConn.SetReadBuffer(512 * 1024)
		tcpConn.SetWriteBuffer(512 * 1024)
	}

	// Tối ưu cho UDS: set buffers lên 32MB để tránh nghẽn khi gửi batch lớn
	if unixConn, ok := conn.(*net.UnixConn); ok {
		if err := unixConn.SetWriteBuffer(32 * 1024 * 1024); err != nil {
			logger.Warn("⚠️  [CHANNEL WORKER %d] Could not set UDS write buffer to 32MB on reconnect: %v", w.id, err)
		}
		if err := unixConn.SetReadBuffer(32 * 1024 * 1024); err != nil {
			logger.Warn("⚠️  [CHANNEL WORKER %d] Could not set UDS read buffer to 32MB on reconnect: %v", w.id, err)
		}
	}

	return conn, nil
}

// SendBatch gửi batch vào channel (non-blocking nếu channel đầy)
func (s *ChannelBasedSender) SendBatch(batch []byte) error {
	s.totalBatches.Add(1)

	batchHash := ""
	if len(batch) > 8 {
		batchHash = fmt.Sprintf("%x", batch[:8])
	}

	select {
	case s.batchChan <- batch:
		// Batch đã được queue thành công
		logger.Info("📤 [CHANNEL SENDER] Batch queued successfully: size=%d bytes, hash_preview=%s, channel_len=%d/%d",
			len(batch), batchHash, len(s.batchChan), cap(s.batchChan))
		return nil
	case <-time.After(100 * time.Millisecond):
		// Channel đầy, log warning
		s.totalFailed.Add(1)
		logger.Warn("⚠️  [CHANNEL SENDER] Channel full! Cannot queue batch: size=%d bytes, hash_preview=%s, buffer_size=%d",
			len(batch), batchHash, cap(s.batchChan))
		return fmt.Errorf("channel đầy, không thể queue batch (buffer size: %d)", cap(s.batchChan))
	}
}

// retryWorker retry các batches bị reject do epoch transition
func (s *ChannelBasedSender) retryWorker() {
	defer s.wg.Done()

	retryTicker := time.NewTicker(2 * time.Second) // Retry mỗi 2 giây
	defer retryTicker.Stop()

	for {
		select {
		case <-s.stopChan:
			// Flush retry channel trước khi dừng
			for {
				select {
				case batch := <-s.retryChan:
					// Đưa lại vào batch channel để retry
					select {
					case s.batchChan <- batch:
						logger.Info("🔄 [RETRY WORKER] Flushed batch from retry queue")
					default:
						logger.Warn("⚠️  [RETRY WORKER] Cannot flush batch, channel full")
					}
				default:
					return
				}
			}
		case <-retryTicker.C:
			// Retry một batch từ retry queue
			select {
			case batch := <-s.retryChan:
				// Đưa lại vào batch channel để workers xử lý
				select {
				case s.batchChan <- batch:
					batchHash := ""
					if len(batch) > 8 {
						batchHash = fmt.Sprintf("%x", batch[:8])
					}
					logger.Info("🔄 [RETRY WORKER] Retrying batch: hash_preview=%s", batchHash)
				default:
					// Batch channel đầy, đưa lại vào retry channel
					select {
					case s.retryChan <- batch:
						// Queued lại
					default:
						// Retry channel cũng đầy, log warning
						logger.Warn("⚠️  [RETRY WORKER] Both channels full, batch may be lost")
						s.totalFailed.Add(1)
					}
				}
			default:
				// Không có batch nào cần retry
			}
		}
	}
}

// Close dừng tất cả workers
func (s *ChannelBasedSender) Close() error {
	close(s.stopChan)
	s.wg.Wait()

	// Close tất cả connections
	for _, worker := range s.workers {
		if worker.conn != nil {
			worker.conn.Close()
		}
	}

	logger.Info("✅ [CHANNEL SENDER] Closed: sent=%d, failed=%d, retried=%d, batches=%d",
		s.totalSent.Load(), s.totalFailed.Load(), s.totalRetried.Load(), s.totalBatches.Load())

	return nil
}

// Stats trả về thống kê
func (s *ChannelBasedSender) Stats() (sent, failed, retried, batches int64) {
	return s.totalSent.Load(), s.totalFailed.Load(), s.totalRetried.Load(), s.totalBatches.Load()
}
