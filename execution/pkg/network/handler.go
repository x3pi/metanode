// @title network/handler.go
// @markdown `network/handler.go`
package network

import (
	"errors"
	"fmt"
	"sync"
	"time"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// handleServerBusy là hàm xử lý mặc định cho lệnh ServerBusy.
// Nó được gọi khi server quá tải và không thể xử lý thêm yêu cầu.
func handleServerBusy(r network.Request) error {
	logger.Error("--- NHẬN TÍN HIỆU SERVER BUSY TỪ CLIENT: %s ---", r.Connection().RemoteAddrSafe())
	return nil
}

// tpsTracker quản lý việc theo dõi và thống kê TPS cho các route.
// Đây là struct mới được thêm vào để phục vụ tính năng đo lường hiệu suất.
type tpsTracker struct {
	mu          sync.Mutex       // Mutex để đảm bảo an toàn khi truy cập 'counts' từ nhiều goroutine.
	counts      map[string]int64 // Map để lưu số lượng request cho mỗi lệnh (route) trong khoảng thời gian 1 giây.
	lastLogTime time.Time        // Thời điểm cuối cùng mà log TPS được ghi.
}

// Handler chịu trách nhiệm xử lý các request đến dựa trên các route đã đăng ký.
type Handler struct {
	routes map[string]func(network.Request) error // Map lưu trữ các "route", ánh xạ từ tên lệnh sang hàm xử lý tương ứng.

	// WaitGroup dùng để đảm bảo rằng tất cả các tác vụ chạy ngầm (nếu có) trong các handler hoàn thành trước khi shutdown.
	wg sync.WaitGroup

	// CircuitBreaker là một pattern giúp ngăn chặn các lỗi dây chuyền, sẽ được tích hợp sau.
	circuitBreaker *CircuitBreaker

	// Con trỏ tới struct tpsTracker để quản lý việc đếm TPS.
	tps *tpsTracker
}

// NewHandler tạo một instance mới của Handler.
func NewHandler(
	routes map[string]func(network.Request) error,
	limits map[string]int, // Tham số này hiện không dùng nhưng được giữ lại để tương thích.
) *Handler {
	// Nếu không có route nào được cung cấp, tạo một map rỗng để tránh lỗi.
	if routes == nil {
		routes = make(map[string]func(network.Request) error)
		logger.Warn("NewHandler: 'routes' được cung cấp là nil.")
	}
	// Tự động đăng ký một handler mặc định cho lệnh 'ServerBusy'.
	if _, exists := routes[p_common.ServerBusy]; !exists {
		routes[p_common.ServerBusy] = handleServerBusy
		logger.Info("Đã tự động đăng ký handler cho lệnh 'ServerBusy'.")
	}

	h := &Handler{
		routes: routes,
		// Khởi tạo tpsTracker.
		tps: &tpsTracker{
			counts:      make(map[string]int64),
			lastLogTime: time.Now(),
		},
		// Tạm thời tạo một circuit breaker giả để tránh lỗi.
		circuitBreaker: NewCircuitBreaker(DefaultCircuitBreakerConfig()),
	}

	// **QUAN TRỌNG**: Khởi chạy một goroutine riêng để thực hiện việc ghi log TPS định kỳ.
	go h.logTPS()

	return h
}

// logTPS là goroutine chạy nền, có nhiệm vụ ghi log TPS mỗi giây.
func (h *Handler) logTPS() {
	// THAY ĐỔI: Tạo một Ticker sẽ "bắn" tín hiệu sau mỗi 100ms.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop() // Dọn dẹp Ticker khi hàm kết thúc.

	// Vòng lặp vô hạn chờ tín hiệu từ Ticker.
	for range ticker.C {
		h.tps.mu.Lock() // Khóa mutex để đọc và reset dữ liệu một cách an toàn.

		now := time.Now()
		// Tính khoảng thời gian chính xác từ lần log cuối cùng.
		duration := now.Sub(h.tps.lastLogTime).Seconds()
		if duration < 0.05 { // Tránh trường hợp chia cho 0 hoặc số quá nhỏ.
			h.tps.mu.Unlock()
			continue
		}

		// **QUAN TRỌNG**: Reset lại bộ đếm để bắt đầu đếm cho 100ms tiếp theo.
		// Log TPS đã được loại bỏ để giảm noise
		h.tps.counts = make(map[string]int64)
		h.tps.lastLogTime = now

		h.tps.mu.Unlock() // Mở khóa mutex.
	}
}

// HandleRequest xử lý một request đến. Hàm này được thực thi đồng bộ
// bởi một worker trong Worker Pool của SocketServer.
func (h *Handler) HandleRequest(r network.Request) (err error) {
	// Validate request
	if r == nil || r.Message() == nil {
		err = errors.New("request hoặc message không hợp lệ")
		return err
	}

	cmd := r.Message().Command()

	// Critical commands BYPASS the circuit breaker entirely.
	// Consensus commands: must never be rejected, otherwise blocks can't be processed.
	// Client-facing commands: must never be rejected, otherwise TXs get stuck.
	// The circuit breaker should only gate internal/recovery commands (e.g., BlockResponse).
	isCritical := cmd == p_common.BlockDataTopic ||
		cmd == p_common.TransactionsFromSubTopic ||
		cmd == "InitConnection" ||
		cmd == "SendTransaction" ||
		cmd == "SendTransactions" ||
		cmd == "SendTransactionWithDeviceKey" ||
		cmd == "GetAccountState" ||
		cmd == "GetNonce" ||
		cmd == "ReadTransaction" ||
		cmd == "Receipt" ||
		cmd == "Ping"

	if !isCritical {
		// Record success/failure in circuit breaker after execution (non-critical only)
		defer func() {
			if err != nil {
				h.circuitBreaker.RecordFailure()
			} else {
				h.circuitBreaker.RecordSuccess()
			}
		}()

		// Circuit breaker check — reject if circuit is open (non-critical only)
		if !h.circuitBreaker.CanExecute() {
			err = fmt.Errorf("circuit breaker open: rejecting command %s", cmd)
			return err
		}
	}

	// Update TPS counter
	// h.tps.mu.Lock()
	// h.tps.counts[cmd]++
	// h.tps.mu.Unlock()

	// Dispatch to route handler
	route, routeExists := h.routes[cmd]
	if !routeExists {
		err = fmt.Errorf("không tìm thấy lệnh: %s", cmd)
		return err
	}

	err = route(r)
	return err
}

// Shutdown chờ tất cả các goroutine (nếu có) do các route handler tạo ra hoàn thành.
func (h *Handler) Shutdown() {
	// Nếu các hàm xử lý của bạn có tạo ra goroutine và dùng h.wg.Add(),
	// thì h.wg.Wait() sẽ chờ chúng hoàn thành.
	h.wg.Wait()
}

// GetCircuitBreakerStats trả về thống kê của Circuit Breaker.
func (h *Handler) GetCircuitBreakerStats() map[string]interface{} {
	if h.circuitBreaker != nil {
		return h.circuitBreaker.GetStats()
	}
	return nil
}

// ResetCircuitBreaker đặt lại Circuit Breaker về trạng thái ban đầu.
func (h *Handler) ResetCircuitBreaker() {
	if h.circuitBreaker != nil {
		h.circuitBreaker.Reset()
	}
}
