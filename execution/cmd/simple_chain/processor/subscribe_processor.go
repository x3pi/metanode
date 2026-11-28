package processor

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

type SubscribeProcessor struct {
	mu                             sync.RWMutex
	subscribers                    map[common.Address][]network.Connection
	mapConnectionSubcribeAddresses map[network.Connection][]common.Address
	messageSender                  network.MessageSender
}

func NewSubscribeProcessor(
	messageSender network.MessageSender,
) *SubscribeProcessor {
	sp := &SubscribeProcessor{
		subscribers:                    make(map[common.Address][]network.Connection),
		mapConnectionSubcribeAddresses: make(map[network.Connection][]common.Address),
		messageSender:                  messageSender,
	}
	// Khởi chạy goroutine cleanup để tránh rò rỉ bộ nhớ
	go sp.cleanupDisconnectedSubscribers()
	return sp
}

func isSubscriberAlive(conn network.Connection) bool {
	return conn != nil && conn.IsConnect()
}

func (p *SubscribeProcessor) ProcessSubscribeToAddress(request network.Request) error {
	address := common.BytesToAddress(request.Message().Body())
	clientConnection := request.Connection()
	if !isSubscriberAlive(clientConnection) {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	connections := p.subscribers[address]
	existed := false
	for _, c := range connections {
		if c == clientConnection {
			existed = true
			break
		}
	}
	if !existed {
		p.subscribers[address] = append(connections, clientConnection)
	}

	addresses := p.mapConnectionSubcribeAddresses[clientConnection]
	existed = false
	for _, addr := range addresses {
		if addr == address {
			existed = true
			break
		}
	}
	if !existed {
		p.mapConnectionSubcribeAddresses[clientConnection] = append(addresses, address)
	}

	return nil
}

func (p *SubscribeProcessor) BroadcastLogToSubscriber(
	address common.Address,
	eventLogList []types.EventLog,
) {
	p.mu.RLock()
	rawClients := p.subscribers[address]
	if len(rawClients) == 0 {
		p.mu.RUnlock()
		return
	}
	clientsSnapshot := make([]network.Connection, len(rawClients))
	copy(clientsSnapshot, rawClients)
	p.mu.RUnlock()

	liveClients := make([]network.Connection, 0, len(clientsSnapshot))
	var deadClients []network.Connection
	for _, client := range clientsSnapshot {
		if !isSubscriberAlive(client) {
			deadClients = append(deadClients, client)
			continue
		}
		liveClients = append(liveClients, client)
	}

	for _, dead := range deadClients {
		if dead == nil {
			p.removeNilSubscribersForAddress(address)
			continue
		}
		p.RemoveSubcriber(dead)
	}

	if len(liveClients) == 0 {
		return
	}

	eventLogs := smart_contract.NewEventLogs(eventLogList)
	bEventLogs, err := eventLogs.Marshal()
	if err != nil {
		return
	}

	var wg sync.WaitGroup
	var failedMu sync.Mutex
	var failedDead []network.Connection
	for _, client := range liveClients {
		wg.Add(1)
		go func(c network.Connection) {
			defer wg.Done()
			if err := p.messageSender.SendBytes(c, command.EventLogs, bEventLogs); err != nil && !c.IsConnect() {
				failedMu.Lock()
				failedDead = append(failedDead, c)
				failedMu.Unlock()
			}
		}(client)
	}
	wg.Wait()

	for _, dead := range failedDead {
		p.RemoveSubcriber(dead)
	}
}

func (p *SubscribeProcessor) removeNilSubscribersForAddress(address common.Address) {
	p.mu.Lock()
	defer p.mu.Unlock()
	conns, ok := p.subscribers[address]
	if !ok {
		return
	}
	filtered := make([]network.Connection, 0, len(conns))
	for _, c := range conns {
		if c != nil {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) > 0 {
		p.subscribers[address] = filtered
	} else {
		delete(p.subscribers, address)
	}
}

func (p *SubscribeProcessor) RemoveSubcriber(conn network.Connection) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if addresses, ok := p.mapConnectionSubcribeAddresses[conn]; ok {
		for _, address := range addresses {
			if oldSubscribers, ok := p.subscribers[address]; ok {
				newSubscribers := make([]network.Connection, 0, len(oldSubscribers))
				for _, subscriber := range oldSubscribers {
					if subscriber != conn {
						newSubscribers = append(newSubscribers, subscriber)
					}
				}

				if len(newSubscribers) > 0 {
					p.subscribers[address] = newSubscribers
				} else {
					delete(p.subscribers, address)
				}
			}
		}
		delete(p.mapConnectionSubcribeAddresses, conn)
	}
}

// cleanStaleSubscribers dọn dẹp toàn bộ connection đã ngắt kết nối khỏi các map subscriber.
func (p *SubscribeProcessor) cleanStaleSubscribers() (removedConnections int, removedAddresses int, subscriberCount int) {
	var connectionsToRemove []network.Connection

	p.mu.RLock()
	for conn := range p.mapConnectionSubcribeAddresses {
		if !isSubscriberAlive(conn) {
			connectionsToRemove = append(connectionsToRemove, conn)
		}
	}
	p.mu.RUnlock()

	for _, conn := range connectionsToRemove {
		p.RemoveSubcriber(conn)
		removedConnections++
	}

	p.mu.Lock()
	for address, connections := range p.subscribers {
		newConnections := make([]network.Connection, 0, len(connections))
		for _, conn := range connections {
			if isSubscriberAlive(conn) {
				newConnections = append(newConnections, conn)
			}
		}
		if len(newConnections) != len(connections) {
			if len(newConnections) > 0 {
				p.subscribers[address] = newConnections
			} else {
				delete(p.subscribers, address)
				removedAddresses++
			}
		}
	}
	subscriberCount = len(p.subscribers)
	p.mu.Unlock()
	return removedConnections, removedAddresses, subscriberCount
}

// cleanupDisconnectedSubscribers dọn dẹp các disconnected connections để tránh rò rỉ bộ nhớ
// Chạy định kỳ mỗi 5 phút để xóa các connections đã disconnect
func (p *SubscribeProcessor) cleanupDisconnectedSubscribers() {
	ticker := time.NewTicker(5 * time.Minute) // Chạy mỗi 5 phút
	defer ticker.Stop()

	for range ticker.C {
		removedConnections, removedAddresses, subscriberCount := p.cleanStaleSubscribers()

		if removedConnections > 0 || removedAddresses > 0 {
			logger.Info("cleanupDisconnectedSubscribers: Đã xóa %d disconnected connections và %d addresses không còn subscribers",
				removedConnections, removedAddresses)
		}

		if subscriberCount > 10000 {
			logger.Warn("cleanupDisconnectedSubscribers: Số lượng subscribers lớn (%d), có thể có vấn đề", subscriberCount)
		}
	}
}
