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
	// THAY ĐỔI: Chuyển từ map thông thường sang sync.Map
	// subscribers map[common.Address][]network.Connection
	// mapConnectionSubcribeAddresses map[network.Connection][]common.Address
	subscribers                    sync.Map // Kiểu: common.Address -> []network.Connection
	mapConnectionSubcribeAddresses sync.Map // Kiểu: network.Connection -> []common.Address
	messageSender                  network.MessageSender
}

func NewSubscribeProcessor(
	messageSender network.MessageSender,
) *SubscribeProcessor {
	// sync.Map không cần khởi tạo với make, giá trị zero của nó đã sẵn sàng để sử dụng.
	sp := &SubscribeProcessor{
		messageSender: messageSender,
	}
	// Khởi chạy goroutine cleanup để tránh rò rỉ bộ nhớ
	go sp.cleanupDisconnectedSubscribers()
	return sp
}

func (p *SubscribeProcessor) ProcessSubscribeToAddress(request network.Request) error {
	address := common.BytesToAddress(request.Message().Body())
	clientConnection := request.Connection()

	// Cập nhật map subscribers
	actual, _ := p.subscribers.LoadOrStore(address, []network.Connection{clientConnection})
	connections := actual.([]network.Connection)

	// Kiểm tra nếu clientConnection đã tồn tại thì không thêm nữa
	existed := false
	for _, c := range connections {
		if c == clientConnection {
			existed = true
			break
		}
	}
	if !existed {
		connections = append(connections, clientConnection)
		p.subscribers.Store(address, connections)
	}

	// Cập nhật map ngược
	actual, _ = p.mapConnectionSubcribeAddresses.LoadOrStore(clientConnection, []common.Address{address})
	addresses := actual.([]common.Address)
	existed = false
	for _, addr := range addresses {
		if addr == address {
			existed = true
			break
		}
	}
	if !existed {
		addresses = append(addresses, address)
		p.mapConnectionSubcribeAddresses.Store(clientConnection, addresses)
	}

	return nil
}

func (p *SubscribeProcessor) BroadcastLogToSubscriber(
	address common.Address,
	eventLogList []types.EventLog,
) {
	if connections, ok := p.subscribers.Load(address); ok {
		clients := connections.([]network.Connection)
		wg := &sync.WaitGroup{}
		newClients := make([]network.Connection, 0, len(clients))

		for _, client := range clients {
			// Bổ sung kiểm tra nil hoặc disconnect
			if client == nil || client.TcpRemoteAddr() == nil {
				// Xóa luôn map ngược nếu connection không hợp lệ
				p.mapConnectionSubcribeAddresses.Delete(client)
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
		// Nếu có thay đổi (loại bỏ nil/disconnect), cập nhật lại map
		if len(newClients) != len(clients) {
			if len(newClients) > 0 {
				p.subscribers.Store(address, newClients)
			} else {
				p.subscribers.Delete(address)
			}
		}
		wg.Wait()
	}
}

func (p *SubscribeProcessor) RemoveSubcriber(conn network.Connection) {
	// THAY ĐỔI: Dùng Load() và Store()/Delete() để cập nhật an toàn
	if addresses, ok := p.mapConnectionSubcribeAddresses.Load(conn); ok {
		for _, address := range addresses.([]common.Address) {
			if subscribers, ok := p.subscribers.Load(address); ok {
				oldSubscribers := subscribers.([]network.Connection)
				newSubscribers := make([]network.Connection, 0, len(oldSubscribers)-1)
				for _, subscriber := range oldSubscribers {
					if subscriber != conn {
						newSubscribers = append(newSubscribers, subscriber)
					}
				}

				if len(newSubscribers) > 0 {
					// Cập nhật lại danh sách subscribers cho address
					p.subscribers.Store(address, newSubscribers)
				} else {
					// Nếu không còn subscriber nào, xóa key đi
					p.subscribers.Delete(address)
				}
			}
		}
		// Xóa connection khỏi map ngược
		p.mapConnectionSubcribeAddresses.Delete(conn)
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

		// Duyệt qua tất cả connections trong mapConnectionSubcribeAddresses
		var connectionsToRemove []network.Connection
		p.mapConnectionSubcribeAddresses.Range(func(key, value interface{}) bool {
			conn := key.(network.Connection)
			// Kiểm tra nếu connection đã disconnect (nil hoặc không có remote address)
			if conn == nil || conn.TcpRemoteAddr() == nil {
				connectionsToRemove = append(connectionsToRemove, conn)
			}
			return true
		})

		// Xóa các disconnected connections
		for _, conn := range connectionsToRemove {
			p.RemoveSubcriber(conn)
			removedConnections++
		}

		// Dọn dẹp các addresses không còn subscribers
		p.subscribers.Range(func(key, value interface{}) bool {
			address := key.(common.Address)
			connections := value.([]network.Connection)
			newConnections := make([]network.Connection, 0, len(connections))
			for _, conn := range connections {
				if conn != nil && conn.TcpRemoteAddr() != nil {
					newConnections = append(newConnections, conn)
				}
			}
			if len(newConnections) != len(connections) {
				if len(newConnections) > 0 {
					p.subscribers.Store(address, newConnections)
				} else {
					p.subscribers.Delete(address)
					removedAddresses++
				}
			}
			return true
		})

		if removedConnections > 0 || removedAddresses > 0 {
			logger.Info("cleanupDisconnectedSubscribers: Đã xóa %d disconnected connections và %d addresses không còn subscribers",
				removedConnections, removedAddresses)
		}

		// Log tổng số subscribers mỗi lần cleanup
		subscriberCount := 0
		p.subscribers.Range(func(key, value interface{}) bool {
			subscriberCount++
			return true
		})
		if subscriberCount > 10000 {
			logger.Warn("cleanupDisconnectedSubscribers: Số lượng subscribers lớn (%d), có thể có vấn đề", subscriberCount)
		}
	}
}
