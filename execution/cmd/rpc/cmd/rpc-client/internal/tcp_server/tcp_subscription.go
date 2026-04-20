package tcp_server

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

// TcpSubscription lưu thông tin subscription của 1 TCP client
type TcpSubscription struct {
	ID                string               // Subscription ID (0xabc...)
	Conn              t_network.Connection // TCP connection tới client
	ContractAddresses []string             // Địa chỉ contract đang subscribe
	Topics            []string             // Topics filter
	CreatedAt         time.Time            // Thời gian tạo
}

// TcpSubscriptionManager quản lý subscriptions qua TCP
type TcpSubscriptionManager struct {
	mu            sync.RWMutex
	subscriptions map[string]*TcpSubscription           // Key: Subscription ID
	connections   map[t_network.Connection][]*TcpSubscription // Key: Connection
	messageSender t_network.MessageSender
}

// NewTcpSubscriptionManager tạo manager mới
func NewTcpSubscriptionManager(messageSender t_network.MessageSender) *TcpSubscriptionManager {
	return &TcpSubscriptionManager{
		subscriptions: make(map[string]*TcpSubscription),
		connections:   make(map[t_network.Connection][]*TcpSubscription),
		messageSender: messageSender,
	}
}

// CreateSubscription tạo subscription mới cho TCP client
func (m *TcpSubscriptionManager) CreateSubscription(
	conn t_network.Connection,
	contractAddrs []string,
	topics []string,
) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	subID := generateSubID()
	sub := &TcpSubscription{
		ID:                subID,
		Conn:              conn,
		ContractAddresses: contractAddrs,
		Topics:            topics,
		CreatedAt:         time.Now(),
	}
	m.subscriptions[subID] = sub
	m.connections[conn] = append(m.connections[conn], sub)

	logger.Info("✅ TCP Subscription created: id=%s, contracts=%v, topics=%v (total: %d)",
		subID, contractAddrs, topics, len(m.subscriptions))

	return subID
}

// RemoveSubscription xóa 1 subscription
func (m *TcpSubscriptionManager) RemoveSubscription(subID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	sub, exists := m.subscriptions[subID]
	if !exists {
		return false
	}

	delete(m.subscriptions, subID)

	// Cleanup connections map
	if subs, ok := m.connections[sub.Conn]; ok {
		filtered := make([]*TcpSubscription, 0, len(subs))
		for _, s := range subs {
			if s.ID != subID {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			delete(m.connections, sub.Conn)
		} else {
			m.connections[sub.Conn] = filtered
		}
	}

	logger.Info("🗑️ TCP Subscription removed: id=%s", subID)
	return true
}

// RemoveByConnection xóa tất cả subscriptions của 1 connection, trả về số subs đã xóa
func (m *TcpSubscriptionManager) RemoveByConnection(conn t_network.Connection) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	subs, exists := m.connections[conn]
	if !exists {
		return 0
	}

	count := len(subs)
	for _, sub := range subs {
		delete(m.subscriptions, sub.ID)
		logger.Info("🗑️ TCP Subscription removed (disconnect): id=%s, contracts=%v", sub.ID, sub.ContractAddresses)
	}
	delete(m.connections, conn)
	return count
}

// BroadcastEvent gửi event tới tất cả subscriber matching contract + topics
// Sử dụng protobuf RpcEvent (pure proto, không JSON) để tối ưu tốc độ
func (m *TcpSubscriptionManager) BroadcastEvent(contractAddr string, topics []string, eventData map[string]interface{}) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, sub := range m.subscriptions {
		// Check contract address match
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

		// Check topic match (nếu subscriber có topic filter)
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

		// Build RpcEvent protobuf (subscription_id + RpcLogEntry)
		logEntry := &pb.RpcLogEntry{
			Address:          getString(eventData, "address"),
			Data:             getString(eventData, "data"),
			BlockNumber:      getString(eventData, "blockNumber"),
			TransactionHash:  getString(eventData, "transactionHash"),
			BlockHash:        getString(eventData, "blockHash"),
			TransactionIndex: getString(eventData, "transactionIndex"),
			LogIndex:         getString(eventData, "logIndex"),
			Removed:          getBool(eventData, "removed"),
		}

		// Extract topics từ eventData
		if topicsRaw, ok := eventData["topics"].([]string); ok {
			logEntry.Topics = topicsRaw
		} else if topicsRaw, ok := eventData["topics"].([]interface{}); ok {
			for _, t := range topicsRaw {
				if ts, ok := t.(string); ok {
					logEntry.Topics = append(logEntry.Topics, ts)
				}
			}
		}

		rpcEvent := &pb.RpcEvent{
			SubscriptionId: sub.ID,
			Log:            logEntry,
		}

		eventBytes, err := proto.Marshal(rpcEvent)
		if err != nil {
			logger.Error("TCP BroadcastEvent: proto marshal error: %v", err)
			continue
		}

		// Gửi qua TCP (pure protobuf, 1 lần serialize)
		if err := m.messageSender.SendBytes(sub.Conn, CmdRpcEvent, eventBytes); err != nil {
			logger.Error("TCP BroadcastEvent: send error to sub %s: %v", sub.ID, err)
		} else {
			count++
		}
	}

	if count > 0 {
		logger.Info("📡 TCP Broadcasted event to %d subscribers of contract %s", count, contractAddr)
	}
}

// Helper functions để extract data từ map[string]interface{}
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}


func generateSubID() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return "0x" + hex.EncodeToString(bytes)
}

// SubscriptionCount trả về số lượng subscription hiện tại
func (m *TcpSubscriptionManager) SubscriptionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.subscriptions)
}
