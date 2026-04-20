package ws_interceptor

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/config"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/internal/ws_writer"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// TcpBroadcaster interface để broadcast event qua TCP (tránh circular import)
type TcpBroadcaster interface {
	BroadcastEvent(contractAddr string, topics []string, eventData map[string]interface{})
}

type SubscriptionInterceptor struct {
	mu             sync.RWMutex
	subscriptions  map[string]*ClientSubscription // Key: Subscription ID
	connections    map[*websocket.Conn][]*ClientSubscription
	Cfg            *config.Config
	tcpBroadcaster TcpBroadcaster // broadcast song song cho TCP subscribers
}

// ClientSubscription lưu thông tin subscription của 1 client
type ClientSubscription struct {
	ID                string                     // Subscription ID (0xabc...)
	ClientConn        *websocket.Conn            // WebSocket connection tới client
	ClientWriter      *ws_writer.WebSocketWriter // Writer để gửi data về client
	ContractAddresses []string                   // Địa chỉ contract đang subscribe
	Topics            []string                   // Topics filter
	CreatedAt         time.Time                  // Thời gian tạo
	Metadata          map[string]interface{}     // Dữ liệu bổ sung
}

// NewSubscriptionInterceptor tạo manager mới
func NewSubscriptionInterceptor(cfg *config.Config) *SubscriptionInterceptor {
	return &SubscriptionInterceptor{
		subscriptions: make(map[string]*ClientSubscription),
		connections:   make(map[*websocket.Conn][]*ClientSubscription),
		Cfg:           cfg,
	}
}

// SetTcpBroadcaster gán TCP broadcaster (gọi sau khi TCP server khởi tạo)
func (sm *SubscriptionInterceptor) SetTcpBroadcaster(tb TcpBroadcaster) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.tcpBroadcaster = tb
	logger.Info("✅ SubscriptionInterceptor: TcpBroadcaster đã được gắn")
}

// CreateSubscription tạo một subscription mới cho client
func (sm *SubscriptionInterceptor) CreateSubscription(
	conn *websocket.Conn,
	writer *ws_writer.WebSocketWriter,
	contractAddrs []string,
	topics []string,
) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	subID := sm.generateSubscriptionID()

	sub := &ClientSubscription{
		ID:                subID,
		ClientConn:        conn,
		ClientWriter:      writer,
		ContractAddresses: contractAddrs,
		Topics:            topics,
		CreatedAt:         time.Now(),
		Metadata:          make(map[string]interface{}),
	}
	// Lưu vào map
	sm.subscriptions[subID] = sub
	// Lưu theo connection (để cleanup khi disconnect)
	sm.connections[conn] = append(sm.connections[conn], sub)

	logger.Info("✅ Created Subscription ID: %s for contracts %v with topics %v (Total: %d)",
		subID, contractAddrs, topics, len(sm.subscriptions))

	return subID
}

// RemoveByConnection xóa tất cả subscriptions của một connection
func (sm *SubscriptionInterceptor) RemoveByConnection(conn *websocket.Conn) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	subs, exists := sm.connections[conn]
	if !exists {
		return
	}

	// Xóa từng subscription
	for _, sub := range subs {
		delete(sm.subscriptions, sub.ID)
		logger.Info("🗑️ UNSUBCRIBE Removed Subscription ID: %s", sub.ID)
	}

	delete(sm.connections, conn)
}

// SendEventToSubscription gửi event về đúng subscription ID
func (sm *SubscriptionInterceptor) SendEventToSubscription(subID string, eventData map[string]interface{}) error {
	sm.mu.RLock()
	sub, exists := sm.subscriptions[subID]
	sm.mu.RUnlock()
	if !exists {
		return fmt.Errorf("subscription ID %s not found", subID)
	}
	// Đóng gói event theo chuẩn eth_subscription
	message := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_subscription",
		"params": map[string]interface{}{
			"subscription": subID,
			"result":       eventData,
		},
	}
	// Gửi về client
	if err := sub.ClientWriter.WriteJSON(message); err != nil {
		logger.Error("Failed to send event to subscription %s: %v", subID, err)
		return err
	}

	logger.Info("📤 Sent event to Subscription ID: %s", subID)
	return nil
}

// BroadcastEventToContract gửi event tới tất cả client đang subscribe contract này
// Broadcast song song cho cả WS subscribers VÀ TCP subscribers
func (sm *SubscriptionInterceptor) BroadcastEventToContract(contractAddr string, topics []string, eventData map[string]interface{}) {
	sm.mu.RLock()
	tcpBroadcaster := sm.tcpBroadcaster
	sm.mu.RUnlock()

	var wg sync.WaitGroup
	var wsCount int

	// === 1. Broadcast cho WS subscribers (goroutine) ===
	wg.Add(1)
	go func() {
		defer wg.Done()
		sm.mu.RLock()
		for subID, sub := range sm.subscriptions {
			// Kiểm tra contract address khớp (case-insensitive)
			addressMatch := false
			for _, addr := range sub.ContractAddresses {
				if strings.EqualFold(addr, contractAddr) {
					addressMatch = true
					break
				}
			}
			if !addressMatch {
				continue
			}
			if len(sub.Topics) > 0 && len(topics) > 0 {
				topicMatch := false
				for _, subTopic := range sub.Topics {
					for _, eventTopic := range topics {
						if strings.EqualFold(subTopic, eventTopic) {
							topicMatch = true
							break
						}
					}
					if topicMatch {
						break
					}
				}
				if !topicMatch {
					continue
				}
			}
			message := map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "eth_subscription",
				"params": map[string]interface{}{
					"subscription": sub.ID,
					"result":       eventData,
				},
			}
			if err := sub.ClientWriter.WriteJSON(message); err != nil {
				logger.Error("Failed to send event to %s: %v", sub.ID, err)
			} else {
				wsCount++
				logger.Info("✅ WS: Sent to subscription %s (created at %v)", subID, sub.CreatedAt)
			}
		}
		sm.mu.RUnlock()
	}()

	// === 2. Broadcast cho TCP subscribers (goroutine) ===
	wg.Add(1)
	go func() {
		defer wg.Done()
		if tcpBroadcaster != nil {
			tcpBroadcaster.BroadcastEvent(contractAddr, topics, eventData)
		}
	}()

	wg.Wait()

	logger.Info("📡 Broadcasted event to %d WS + TCP subscribers of contract %s", wsCount, contractAddr)
}

// GetAllSubscriptions trả về danh sách tất cả subscription IDs
func (sm *SubscriptionInterceptor) GetAllSubscriptions() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	ids := make([]string, 0, len(sm.subscriptions))
	for id := range sm.subscriptions {
		ids = append(ids, id)
	}
	return ids
}

// GetSubscriptionInfo lấy thông tin subscription
func (sm *SubscriptionInterceptor) GetSubscriptionInfo(subID string) (*ClientSubscription, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sub, exists := sm.subscriptions[subID]
	return sub, exists
}

// --- Helper Functions ---
func (sm *SubscriptionInterceptor) generateSubscriptionID() string {
	bytes := make([]byte, 16) // 128 bits
	rand.Read(bytes)
	return "0x" + hex.EncodeToString(bytes)
}

func (sm *SubscriptionInterceptor) addressMatch(addr1, addr2 string) bool {
	// So sánh không phân biệt hoa thường
	return len(addr1) == len(addr2) &&
		addr1[:2] == "0x" && addr2[:2] == "0x" &&
		addr1[2:] == addr2[2:] ||
		addr1 == addr2
}

// isMonitoredContract kiểm tra xem 1 address có trong danh sách theo dõi không
func (sm *SubscriptionInterceptor) IsMonitoredContract(addr string) bool {
	for _, monitored := range sm.Cfg.ContractsInterceptor {
		if strings.EqualFold(addr, monitored) {
			return true
		}
	}
	return false
}

func (sm *SubscriptionInterceptor) ValidateSubscriptionAddresses(addresses []string) (shouldIntercept bool, err error) {
	if len(addresses) == 0 {
		return false, fmt.Errorf("no addresses provided")
	}
	monitoredCount := 0
	notMonitoredCount := 0
	for _, addr := range addresses {
		if sm.IsMonitoredContract(addr) {
			monitoredCount++
		} else {
			notMonitoredCount++
		}
	}

	// Case 1: TẤT CẢ đều nằm trong danh sách theo dõi → Bắt lại
	if monitoredCount == len(addresses) {
		return true, nil
	}

	// Case 2: KHÔNG CÓ cái nào nằm trong danh sách theo dõi → Lên chain
	if notMonitoredCount == len(addresses) {
		return false, nil
	}
	// Case 3: MỘT PHẦN nằm, MỘT PHẦN không → Báo lỗi
	return false, fmt.Errorf("All addresses must be either monitored or non-monitored")
}
