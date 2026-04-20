package connection_manager

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/meta-node-blockchain/meta-node/pkg/connection_manager/connection_client"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
)

// ChainConnectionPool quản lý pool các TCP connection đến chain node.
// Mỗi connection hỗ trợ multiplexing (nhiều request song song qua ID),
// pool chỉ cần 2-5 connection là đủ cho hàng nghìn request/s.
//
// Ưu điểm so với per-user connection:
//   - Không lãng phí connection cho user chỉ gửi 1-2 request
//   - Auto-reconnect khi conn bị lỗi
//   - Round-robin phân tải đều giữa các connection
type ChainConnectionPool struct {
	connections       []*poolEntry
	size              int
	index             uint64 // atomic, round-robin counter
	connectionAddress string
	msgSender         t_network.MessageSender
	mu                sync.RWMutex
	ctx               context.Context
	cancel            context.CancelFunc
}

type poolEntry struct {
	client *connection_client.ConnectionClient
	mu     sync.Mutex // protect reconnect
}

// NewChainConnectionPool tạo pool với N connections đến chain node.
// Khuyến nghị poolSize = 3 cho hầu hết use case.
func NewChainConnectionPool(
	connectionAddress string,
	poolSize int,
	messageSender t_network.MessageSender,
) (*ChainConnectionPool, error) {
	if poolSize <= 0 {
		poolSize = 3
	}
	if connectionAddress == "" {
		return nil, fmt.Errorf("chain address is empty")
	}

	ctx, cancel := context.WithCancel(context.Background())

	pool := &ChainConnectionPool{
		connections:       make([]*poolEntry, poolSize),
		size:              poolSize,
		connectionAddress: connectionAddress,
		msgSender:         messageSender,
		ctx:               ctx,
		cancel:            cancel,
	}

	// Tạo tất cả connections
	connectedCount := 0
	for i := 0; i < poolSize; i++ {
		key := fmt.Sprintf("chain_pool_%d", i)
		client := connection_client.NewConnectionClient(ctx, key, connectionAddress, messageSender)

		if err := client.Connect(); err != nil {
			logger.Warn("⚠️ ChainPool: failed to connect slot %d: %v (will retry on use)", i, err)
			pool.connections[i] = &poolEntry{client: client}
		} else {
			pool.connections[i] = &poolEntry{client: client}
			connectedCount++
		}
	}

	if connectedCount == 0 {
		cancel()
		return nil, fmt.Errorf("ChainPool: failed to connect any slot to %s", connectionAddress)
	}

	logger.Info("✅ ChainConnectionPool created: %d/%d connections to %s", connectedCount, poolSize, connectionAddress)
	return pool, nil
}

// Get trả về 1 ConnectionClient từ pool (round-robin).
// Nếu conn được chọn bị lỗi → tự reconnect.
// Nếu reconnect fail → thử conn tiếp theo.
func (p *ChainConnectionPool) Get() (*connection_client.ConnectionClient, error) {
	// Thử tối đa p.size lần (mỗi slot 1 lần)
	for attempt := 0; attempt < p.size; attempt++ {
		idx := int(atomic.AddUint64(&p.index, 1)-1) % p.size
		entry := p.connections[idx]

		client := entry.client
		if client != nil && client.IsConnected() {
			return client, nil
		}

		// Connection bị lỗi → reconnect
		reconnected := p.tryReconnect(entry, idx)
		if reconnected {
			return entry.client, nil
		}
		// Reconnect fail → thử slot tiếp theo
	}

	return nil, fmt.Errorf("ChainPool: all %d connections are down", p.size)
}

// tryReconnect cố gắng tạo lại connection cho 1 slot.
// Thread-safe: chỉ 1 goroutine reconnect tại 1 thời điểm.
func (p *ChainConnectionPool) tryReconnect(entry *poolEntry, idx int) bool {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	// Double-check sau khi có lock (có thể goroutine khác đã reconnect)
	if entry.client != nil && entry.client.IsConnected() {
		return true
	}
	// Tạo connection mới
	key := fmt.Sprintf("chain_pool_%d", idx)
	client := connection_client.NewConnectionClient(p.ctx, key, p.connectionAddress, p.msgSender)

	if err := client.Connect(); err != nil {
		logger.Error("❌ ChainPool: reconnect slot %d failed: %v", idx, err)
		entry.client = client // giữ lại để lần sau thử lại
		return false
	}

	entry.client = client
	logger.Info("🔄 ChainPool: reconnected slot %d to %s", idx, p.connectionAddress)
	return true
}

// GetAll trả về tất cả ConnectionClient đang active (dùng cho debug/monitoring)
func (p *ChainConnectionPool) GetAll() []*connection_client.ConnectionClient {
	result := make([]*connection_client.ConnectionClient, 0, p.size)
	for _, entry := range p.connections {
		if entry.client != nil && entry.client.IsConnected() {
			result = append(result, entry.client)
		}
	}
	return result
}

// ActiveCount trả về số connection đang hoạt động
func (p *ChainConnectionPool) ActiveCount() int {
	count := 0
	for _, entry := range p.connections {
		if entry.client != nil && entry.client.IsConnected() {
			count++
		}
	}
	return count
}

// Size trả về tổng số slot trong pool
func (p *ChainConnectionPool) Size() int {
	return p.size
}

// Close đóng tất cả connections trong pool
func (p *ChainConnectionPool) Close() {
	p.cancel()
	for i, entry := range p.connections {
		entry.mu.Lock()
		if entry.client != nil {
			entry.client.Disconnect()
		}
		entry.mu.Unlock()
		logger.Info("🔌 ChainPool: closed slot %d", i)
	}
	logger.Info("🔌 ChainConnectionPool closed (%d slots)", p.size)
}
