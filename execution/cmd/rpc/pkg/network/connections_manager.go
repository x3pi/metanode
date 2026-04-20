package network

import (
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

const (
	MaxConnectionTypes  = 20
	numConnectionShards = 32 // Base shards, có thể tăng động nếu cần
)

// calculateDynamicShards tính số shards động dựa trên số connections
// Giúp scale tốt hơn với hàng trăm ngàn connections
func calculateDynamicShards(connectionCount int) int {
	if connectionCount < 1000 {
		return 32
	} else if connectionCount < 10000 {
		return 64
	} else if connectionCount < 100000 {
		return 128
	} else {
		return 256 // Max 256 shards cho 100K+ connections
	}
}

type connectionRecord struct {
	address common.Address
	conn    network.Connection
}

type connectionShard struct {
	// Chỉ dùng sync.Map để tránh blocking hoàn toàn - không cần Mutex
	connections sync.Map // Key: string (address hex), Value: connectionRecord
}

type ConnectionsManager struct {
	parentMu         sync.RWMutex
	parentConnection network.Connection
	typeToShards     [][]connectionShard
}

func NewConnectionsManager() network.ConnectionsManager {
	cm := &ConnectionsManager{
		typeToShards: make([][]connectionShard, MaxConnectionTypes),
	}
	for i := range cm.typeToShards {
		cm.typeToShards[i] = make([]connectionShard, numConnectionShards)
		// sync.Map không cần khởi tạo, nó tự động empty
	}
	return cm
}

// calculateShardIndex tính toán shard index từ address một cách nhất quán
// Đảm bảo Add và Lookup luôn dùng cùng cách tính
func calculateShardIndex(address common.Address) int {
	return int(address[common.AddressLength-1]) & (numConnectionShards - 1)
}

func (cm *ConnectionsManager) getShard(cType int, address common.Address) (*connectionShard, error) {
	if cType < 0 || cType >= len(cm.typeToShards) {
		return nil, fmt.Errorf("cType %d nằm ngoài phạm vi", cType)
	}
	shardIndex := calculateShardIndex(address)
	return &cm.typeToShards[cType][shardIndex], nil
}

func addressKey(address common.Address) string {
	return address.Hex()
}

func (cm *ConnectionsManager) ConnectionsByType(cType int) map[common.Address]network.Connection {
	if cType < 0 || cType >= len(cm.typeToShards) {
		logger.Warn("ConnectionsByType: cType %d nằm ngoài phạm vi hợp lệ.", cType)
		return make(map[common.Address]network.Connection)
	}

	fullMap := make(map[common.Address]network.Connection)
	shardsForType := cm.typeToShards[cType]

	for i := range shardsForType {
		shard := &shardsForType[i]
		// sync.Map Range - non-blocking, không cần lock
		// Trả về tất cả connections, để caller quyết định có dùng hay không
		// Không filter ở đây vì cache có thể stale và gây race condition
		shard.connections.Range(func(key, value interface{}) bool {
			record := value.(connectionRecord)
			// Chỉ kiểm tra conn != nil, không check IsConnect() ở đây
			// Caller sẽ check IsConnect() khi cần thiết
			if record.conn != nil {
				fullMap[record.address] = record.conn
			}
			return true
		})
	}
	return fullMap
}

func (cm *ConnectionsManager) ConnectionByTypeAndAddress(cType int, address common.Address) network.Connection {
	shard, err := cm.getShard(cType, address)
	if err != nil {
		logger.Warn("ConnectionByTypeAndAddress: Lỗi khi lấy shard cho cType %d: %v", cType, err)
		return nil
	}

	key := addressKey(address)
	// shardIndex := calculateShardIndex(address)

	// sync.Map Load - non-blocking, không cần lock
	if val, ok := shard.connections.Load(key); ok {
		record := val.(connectionRecord)
		// Chỉ kiểm tra conn != nil, không check IsConnect() ở đây
		// Caller sẽ check IsConnect() khi cần thiết để tránh race condition với cache
		if record.conn != nil {
			// logger.Info(
			// 	"ConnectionByTypeAndAddress: tìm thấy connection",
			// 	"cType", cType,
			// 	"address", address.Hex(),
			// 	"recordAddress", record.address.Hex(),
			// 	"shardIndex", shardIndex,
			// )
			return record.conn
		}
	}

	// logger.Warn(
	// 	"ConnectionByTypeAndAddress: không tìm thấy connection",
	// 	"cType", cType,
	// 	"address", address.Hex(),
	// 	"key", key,
	// 	"shardIndex", shardIndex,
	// )
	return nil
}

func (cm *ConnectionsManager) ConnectionsByTypeAndAddresses(cType int, addresses []common.Address) map[common.Address]network.Connection {
	result := make(map[common.Address]network.Connection, len(addresses))
	for _, addr := range addresses {
		if conn := cm.ConnectionByTypeAndAddress(cType, addr); conn != nil {
			result[addr] = conn
		}
	}
	return result
}

func (cm *ConnectionsManager) FilterAddressAvailable(cType int, addresses map[common.Address]*uint256.Int) map[common.Address]*uint256.Int {
	availableAddresses := make(map[common.Address]*uint256.Int)
	for address, value := range addresses {
		if conn := cm.ConnectionByTypeAndAddress(cType, address); conn != nil && conn.IsConnect() {
			availableAddresses[address] = value
		}
	}
	return availableAddresses
}

func (cm *ConnectionsManager) ParentConnection() network.Connection {
	cm.parentMu.RLock()
	defer cm.parentMu.RUnlock()
	// Trả về parent connection nếu có, không check IsConnect() ở đây
	// Caller sẽ check IsConnect() khi cần để tránh race condition với cache
	return cm.parentConnection
}

func (cm *ConnectionsManager) Stats() *pb.NetworkStats {
	pbNetworkStats := &pb.NetworkStats{
		TotalConnectionByType: make(map[string]int32, MaxConnectionTypes),
	}

	for cType, shardsForType := range cm.typeToShards {
		total := 0
		for i := range shardsForType {
			shard := &shardsForType[i]
			// sync.Map Range - non-blocking
			shard.connections.Range(func(key, value interface{}) bool {
				total++
				return true
			})
		}

		if total > 0 {
			connectionTypeName := p_common.MapIndexToConnectionType(cType)
			if connectionTypeName == "" {
				connectionTypeName = fmt.Sprintf("UNKNOWN_TYPE_%d", cType)
			}
			pbNetworkStats.TotalConnectionByType[connectionTypeName] = int32(total)
		}
	}
	return pbNetworkStats
}

func (cm *ConnectionsManager) AddParentConnection(conn network.Connection) {
	cm.parentMu.Lock()
	defer cm.parentMu.Unlock()
	if conn == nil {
		fmt.Printf("[CONN_MANAGER] ❌ AddParentConnection: Connection là nil\n")
		logger.Warn("AddParentConnection: Cố gắng thêm một kết nối cha nil. Bỏ qua.")
		return
	}
	cm.parentConnection = conn
	fmt.Printf("[CONN_MANAGER] ✅ AddParentConnection: Đã thêm/cập nhật parent connection\n")
	fmt.Printf("[CONN_MANAGER] Parent address: %s\n", conn.Address().Hex())
	fmt.Printf("[CONN_MANAGER] Parent remote: %s\n", conn.RemoteAddrSafe())
	fmt.Printf("[CONN_MANAGER] Parent type: %s\n", conn.Type())
	fmt.Printf("[CONN_MANAGER] Parent connected: %v\n", conn.IsConnect())
	logger.Info("AddParentConnection: Đã thêm/cập nhật kết nối cha: %s", conn.String())
}

func (cm *ConnectionsManager) RemoveConnection(conn network.Connection) {
	if conn == nil {
		logger.Warn("RemoveConnection: Cố gắng xóa một kết nối nil. Bỏ qua.")
		return
	}

	address := conn.Address()
	cTypeStr := conn.Type()
	cType := p_common.MapConnectionTypeToIndex(cTypeStr)

	shard, err := cm.getShard(cType, address)
	if err != nil {
		logger.Warn("RemoveConnection: Loại kết nối '%s' không hợp lệ. Lỗi: %v", cTypeStr, err)
		return
	}

	key := addressKey(address)
	shardIndex := calculateShardIndex(address)

	// sync.Map Load và Delete - non-blocking
	if val, ok := shard.connections.Load(key); ok {
		record := val.(connectionRecord)
		if record.conn == conn {
			shard.connections.Delete(key)
			logger.Info(
				"RemoveConnection: Đã xóa kết nối",
				"address", address.Hex(),
				"type", cTypeStr,
				"cType", cType,
				"key", key,
				"shardIndex", shardIndex,
			)
		} else {
			logger.Warn(
				"RemoveConnection: Connection không khớp",
				"address", address.Hex(),
				"type", cTypeStr,
				"key", key,
				"shardIndex", shardIndex,
			)
		}
	} else {
		logger.Warn(
			"RemoveConnection: Không tìm thấy connection để xóa",
			"address", address.Hex(),
			"type", cTypeStr,
			"key", key,
			"shardIndex", shardIndex,
		)
	}

	cm.parentMu.Lock()
	if cm.parentConnection == conn {
		cm.parentConnection = nil
		fmt.Printf("[CONN_MANAGER] ⚠️ RemoveConnection: Parent connection đã được xóa\n")
		logger.Info("RemoveConnection: Kết nối cha đã được xóa: %s", conn.String())
	}
	cm.parentMu.Unlock()
}

// AddConnectionWithAddress thêm connection với address được chỉ định để tránh race condition
func (cm *ConnectionsManager) AddConnectionWithAddress(conn network.Connection, address common.Address, replace bool, cTypeOverride string) {
	if conn == nil {
		logger.Warn("AddConnectionWithAddress: Cố gắng thêm một kết nối nil. Bỏ qua.")
		return
	}

	cTypeStr := cTypeOverride
	if cTypeStr == "" {
		cTypeStr = conn.Type()
		logger.Info(
			"AddConnection: override type rỗng, sử dụng conn.Type()",
			"address", address.Hex(),
			"type", cTypeStr,
		)
	} else {
		logger.Info(
			"AddConnection: sử dụng override type",
			"address", address.Hex(),
			"type", cTypeStr,
		)
	}
	if cTypeStr == "" {
		logger.Warn(
			"AddConnection: không xác định được loại kết nối, mặc định NONE",
			"address", address.Hex(),
		)
	}
	cType := p_common.MapConnectionTypeToIndex(cTypeStr)

	shard, err := cm.getShard(cType, address)
	if err != nil {
		logger.Error("AddConnection: Loại kết nối '%s' không hợp lệ. Lỗi: %v", cTypeStr, err)
		return
	}

	key := addressKey(address)
	record := connectionRecord{
		address: address,
		conn:    conn,
	}

	// sync.Map Load và Store - non-blocking
	existingVal, exists := shard.connections.Load(key)
	if exists && !replace {
		logger.Info(
			"AddConnectionWithAddress: Bỏ qua vì đã tồn tại kết nối và replace=false",
			"address", address.Hex(),
			"type", cTypeStr,
			"remoteAddr", conn.RemoteAddrSafe(),
		)
		return
	}

	if replace && exists && existingVal != nil {
		existingRecord := existingVal.(connectionRecord)
		if existingRecord.conn != nil && existingRecord.conn != conn {
			logger.Info("AddConnection: Thay thế kết nối hiện có")
			if existingRecord.conn.IsConnect() {
				go func(oldConn network.Connection) {
					_ = oldConn.Disconnect()
				}(existingRecord.conn)
			}
		}
	}

	shard.connections.Store(key, record)
	shardIndex := calculateShardIndex(address)
	logger.Info(
		"AddConnectionWithAddress: Đã thêm/thay thế kết nối",
		"address", address.Hex(),
		"type", cTypeStr,
		"cType", cType,
		"key", key,
		"shardIndex", shardIndex,
		"remoteAddr", conn.RemoteAddrSafe(),
		"replaceRequested", replace,
	)

	totalForType := cm.countConnectionsByType(cType)
	logger.Info(
		"AddConnectionWithAddress: snapshot sau khi ghi nhận kết nối",
		"address", address.Hex(),
		"type", cTypeStr,
		"totalByType", totalForType,
	)
}

func (cm *ConnectionsManager) AddConnection(conn network.Connection, replace bool, cTypeOverride string) {
	if conn == nil {
		logger.Warn("AddConnection: Cố gắng thêm một kết nối nil. Bỏ qua.")
		return
	}

	address := conn.Address()
	// Gọi AddConnectionWithAddress với address từ conn.Address()
	// Lưu ý: Có thể có race condition nếu conn.Init() chưa được xử lý
	cm.AddConnectionWithAddress(conn, address, replace, cTypeOverride)
}

func (cm *ConnectionsManager) countConnectionsByType(cType int) int {
	if cType < 0 || cType >= len(cm.typeToShards) {
		return 0
	}

	total := 0
	shards := cm.typeToShards[cType]
	for i := range shards {
		shard := &shards[i]
		// sync.Map Range - non-blocking
		shard.connections.Range(func(key, value interface{}) bool {
			total++
			return true
		})
	}
	return total
}

func (cm *ConnectionsManager) HealthCheck() {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		for range ticker.C {
			logger.Info("Running connection health check...")
			totalChecked := 0
			totalRemoved := 0
			for cType, shardsForType := range cm.typeToShards {
				for i := range shardsForType {
					shard := &shardsForType[i]
					// sync.Map Range - non-blocking
					shard.connections.Range(func(key, value interface{}) bool {
						totalChecked++
						record := value.(connectionRecord)
						isConnected := record.conn.IsConnect()
						if !isConnected {
							shard.connections.Delete(key)
							totalRemoved++
							logger.Info(
								"HealthCheck: Removed dead connection",
								"cType", cType,
								"address", record.address.Hex(),
								"key", key.(string),
								"remoteAddr", record.conn.RemoteAddrSafe(),
							)
						}
						return true
					})
				}
			}
			logger.Info(
				"HealthCheck: Completed",
				"totalChecked", totalChecked,
				"totalRemoved", totalRemoved,
			)
		}
	}()
}
