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

func (p *SubscribeProcessor) ProcessSubscribeToAddress(request network.Request) error {
	address := common.BytesToAddress(request.Message().Body())
	clientConnection := request.Connection()
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
	clients, ok := p.subscribers[address]
	p.mu.RUnlock()

	if ok {
		wg := &sync.WaitGroup{}
		var newClients []network.Connection
		var modified bool

		for _, client := range clients {
			if client == nil || client.TcpRemoteAddr() == nil {
				modified = true
				p.mu.Lock()
				delete(p.mapConnectionSubcribeAddresses, client)
				p.mu.Unlock()
				continue
			}

			newClients = append(newClients, client)
			wg.Add(1)
			clientCopy := client
			go func(wg *sync.WaitGroup, c network.Connection) {
				defer wg.Done()
				eventLogs := smart_contract.NewEventLogs(eventLogList)
				bEventLogs, err := eventLogs.Marshal()
				if err != nil {
					return
				}
				err = p.messageSender.SendBytes(
					clientCopy,
					command.EventLogs,
					bEventLogs,
				)
				if err != nil {
					// Không log lỗi
				}
			}(wg, clientCopy)
		}
		// Nếu có thay đổi, cập nhật lại map
		if modified {
			p.mu.Lock()
			if len(newClients) > 0 {
				p.subscribers[address] = newClients
			} else {
				delete(p.subscribers, address)
			}
			p.mu.Unlock()
		}
		wg.Wait()
	}
}

func (p *SubscribeProcessor) RemoveSubcriber(conn network.Connection) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if addresses, ok := p.mapConnectionSubcribeAddresses[conn]; ok {
		for _, address := range addresses {
			if oldSubscribers, ok := p.subscribers[address]; ok {
				newSubscribers := make([]network.Connection, 0, len(oldSubscribers)-1)
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

// cleanupDisconnectedSubscribers dọn dẹp các disconnected connections để tránh rò rỉ bộ nhớ
// Chạy định kỳ mỗi 5 phút để xóa các connections đã disconnect
func (p *SubscribeProcessor) cleanupDisconnectedSubscribers() {
	ticker := time.NewTicker(5 * time.Minute) // Chạy mỗi 5 phút
	defer ticker.Stop()

	for range ticker.C {
		removedConnections := 0
		removedAddresses := 0
		var connectionsToRemove []network.Connection

		p.mu.RLock()
		for conn := range p.mapConnectionSubcribeAddresses {
			if conn == nil || conn.TcpRemoteAddr() == nil {
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
				if conn != nil && conn.TcpRemoteAddr() != nil {
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
		subscriberCount := len(p.subscribers)
		p.mu.Unlock()

		if removedConnections > 0 || removedAddresses > 0 {
			logger.Info("cleanupDisconnectedSubscribers: Đã xóa %d disconnected connections và %d addresses không còn subscribers",
				removedConnections, removedAddresses)
		}

		if subscriberCount > 10000 {
			logger.Warn("cleanupDisconnectedSubscribers: Số lượng subscribers lớn (%d), có thể có vấn đề", subscriberCount)
		}
	}
}
