package network

import (
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// CircuitBreakerState đại diện cho trạng thái của Circuit Breaker
type CircuitBreakerState int

const (
	StateClosed CircuitBreakerState = iota
	StateOpen
	StateHalfOpen
)

func (s CircuitBreakerState) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}

// CircuitBreaker thực hiện pattern Circuit Breaker để bảo vệ hệ thống khỏi quá tải
type CircuitBreaker struct {
	mu          sync.RWMutex
	disabled    bool
	maxFailures int           // Số lỗi tối đa trước khi mở circuit
	maxRequests int           // Số request tối đa trong trạng thái HALF_OPEN
	interval    time.Duration // Thời gian chờ trước khi thử HALF_OPEN
	timeout     time.Duration // Thời gian giữ trạng thái OPEN

	state           CircuitBreakerState
	failures        int
	requests        int
	lastFailureTime time.Time
	lastSuccessTime time.Time

	// Metrics
	totalRequests  int64
	totalFailures  int64
	totalSuccesses int64
	totalRejected  int64
}

// CircuitBreakerConfig cấu hình cho Circuit Breaker
type CircuitBreakerConfig struct {
	Disabled    bool          // Nếu true, circuit breaker luôn cho phép thực hiện (bỏ qua mở mạch)
	MaxFailures int           // Mặc định: 10
	MaxRequests int           // Mặc định: 5
	Interval    time.Duration // Mặc định: 10s
	Timeout     time.Duration // Mặc định: 60s
}

// DefaultCircuitBreakerConfig trả về cấu hình mặc định
func DefaultCircuitBreakerConfig() *CircuitBreakerConfig {
	return &CircuitBreakerConfig{
		Disabled:    false,
		MaxFailures: 10,
		MaxRequests: 5,
		Interval:    10 * time.Second,
		Timeout:     60 * time.Second,
	}
}

// NewCircuitBreaker tạo một Circuit Breaker mới
func NewCircuitBreaker(config *CircuitBreakerConfig) *CircuitBreaker {
	if config == nil {
		config = DefaultCircuitBreakerConfig()
	}

	return &CircuitBreaker{
		disabled:    config.Disabled,
		maxFailures: config.MaxFailures,
		maxRequests: config.MaxRequests,
		interval:    config.Interval,
		timeout:     config.Timeout,
		state:       StateClosed,
	}
}

// CanExecute kiểm tra xem có thể thực hiện request hay không
func (cb *CircuitBreaker) CanExecute() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.disabled {
		return true
	}

	cb.totalRequests++

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if time.Since(cb.lastFailureTime) > cb.timeout {
			cb.state = StateHalfOpen
			cb.requests = 0
			logger.Info("Circuit Breaker: Chuyển từ OPEN sang HALF_OPEN")
			return true
		}
		cb.totalRejected++
		return false
	case StateHalfOpen:
		if cb.requests >= cb.maxRequests {
			cb.totalRejected++
			return false
		}
		cb.requests++
		return true
	}

	return false
}

// RecordSuccess ghi nhận một request thành công
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.disabled {
		return
	}

	cb.totalSuccesses++
	cb.lastSuccessTime = time.Now()

	switch cb.state {
	case StateHalfOpen:
		// REVIEW FIX (2026-09-05, PR #102): this used to close unconditionally on the FIRST
		// success while HALF_OPEN. That's an unscoped behavior change to a package shared by
		// every CircuitBreaker consumer, not just RelayerDaemon -- the daemon's actual bug (a
		// legitimate short node restart tripping a 60s lockout) is already fully fixed by the
		// `Disabled` flag PR #102 added, which short-circuits this whole method before it's ever
		// reached. Restored the original "N consecutive successful trial requests required"
		// canary semantics for every OTHER consumer that still runs with the breaker enabled
		// (e.g. GatewayRegistryMonitor's Root Anchor client in block_processor_core.go) --
		// closing on a single lucky success while still HALF_OPEN would make recovery flap on a
		// still-flaky endpoint for them, with no relation to the relayer fix this PR was about.
		if cb.requests >= cb.maxRequests {
			cb.state = StateClosed
			cb.failures = 0
			cb.requests = 0
			logger.Info("Circuit Breaker: Chuyển từ HALF_OPEN sang CLOSED")
		}
	case StateClosed:
		cb.failures = 0 // Reset failure count
	}
}

// RecordFailure ghi nhận một request thất bại
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.disabled {
		return
	}

	cb.totalFailures++
	cb.failures++
	cb.lastFailureTime = time.Now()

	switch cb.state {
	case StateClosed:
		if cb.failures >= cb.maxFailures {
			cb.state = StateOpen
			logger.Warn("Circuit Breaker: Chuyển từ CLOSED sang OPEN (failures: %d/%d)",
				cb.failures, cb.maxFailures)
		}
	case StateHalfOpen:
		cb.state = StateOpen
		cb.requests = 0
		logger.Warn("Circuit Breaker: Chuyển từ HALF_OPEN sang OPEN do request thất bại")
	}
}

// GetState trả về trạng thái hiện tại
func (cb *CircuitBreaker) GetState() CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// GetStats trả về thống kê của Circuit Breaker
func (cb *CircuitBreaker) GetStats() map[string]interface{} {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return map[string]interface{}{
		"state":           cb.state.String(),
		"failures":        cb.failures,
		"requests":        cb.requests,
		"total_requests":  cb.totalRequests,
		"total_failures":  cb.totalFailures,
		"total_successes": cb.totalSuccesses,
		"total_rejected":  cb.totalRejected,
		"last_failure":    cb.lastFailureTime,
		"last_success":    cb.lastSuccessTime,
	}
}

// Reset đặt lại Circuit Breaker về trạng thái ban đầu
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.state = StateClosed
	cb.failures = 0
	cb.requests = 0
	logger.Info("Circuit Breaker: Đã reset về trạng thái CLOSED")
}
