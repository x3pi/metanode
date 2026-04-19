package processor

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

type ConnectionProcessor struct {
	connectionsManager network.ConnectionsManager
	blockProcessor     *BlockProcessor
}

func NewConnectionProcessor(
	connectionsManager network.ConnectionsManager,
) *ConnectionProcessor {
	return &ConnectionProcessor{
		connectionsManager: connectionsManager,
	}
}

// SetBlockProcessor sets the block processor reference for pending receipt flushing
func (p *ConnectionProcessor) SetBlockProcessor(bp *BlockProcessor) {
	p.blockProcessor = bp
}

func (p *ConnectionProcessor) ProcessInitConnection(
	request network.Request,
) (err error) {
	// REMOVED: time.Sleep(100 * time.Millisecond) - Đây là test code, không nên có trong production
	// Nếu ProcessInitConnection chậm hơn 100ms, có thể gây ra các vấn đề:
	// 1. readLoop timeout khi gửi request vào requestChan (5s timeout)
	// 2. HandleConnection drop request nếu server's requestChan đầy
	// 3. Connection có thể bị cleanup/disconnect trước khi add vào manager
	// time.Sleep(100 * time.Millisecond)
	conn := request.Connection()
	if conn == nil {
		return fmt.Errorf("ProcessInitConnection: connection is nil")
	}

	// Check connection status trước khi xử lý
	if !conn.IsConnect() {
		logger.Warn(
			"ProcessInitConnection: connection không connected, có thể đã bị disconnect",
			"remoteAddr", conn.RemoteAddrSafe(),
		)
		// Vẫn tiếp tục xử lý để add vào manager (có thể connection đang trong quá trình connect)
	}

	initData := &pb.InitConnection{}
	err = request.Message().Unmarshal(initData)
	if err != nil {
		return fmt.Errorf("ProcessInitConnection: unmarshal error: %w", err)
	}

	address := common.BytesToAddress(initData.Address)
	cType := initData.Type

	// Init connection TRƯỚC khi add vào manager để đảm bảo connection có address và type
	conn.Init(address, cType)

	logger.Debug("init connection from %v type %v", address, cType)
	logger.Info(
		"ProcessInitConnection: mapped remote connection",
		"remoteAddr", conn.RemoteAddrSafe(),
		"address", address.Hex(),
		"type", cType,
		"replace", initData.Replace,
		"isConnected", conn.IsConnect(),
	)

	// Sử dụng AddConnectionWithAddress để tránh race condition:
	// conn.Init() là async, nhưng chúng ta cần address ngay lập tức
	// Nên truyền address trực tiếp từ initData thay vì gọi conn.Address()
	// QUAN TRỌNG: Add vào manager NGAY LẬP TỨC, không chờ đợi gì cả
	// Nếu chậm, connection có thể bị disconnect trước khi add vào manager
	logger.Info("[PROCESS_INIT] Adding connection: addr=%s type=%s remote=%s connected=%v replace=%v",
		address.Hex(), cType, conn.RemoteAddrSafe(), conn.IsConnect(), initData.Replace)

	p.connectionsManager.AddConnectionWithAddress(conn, address, initData.Replace, initData.Type)

	logger.Info("[PROCESS_INIT] ✅ Connection added successfully: addr=%s type=%s remote=%s",
		address.Hex(), cType, conn.RemoteAddrSafe())

	// Flush pending receipts for reconnected client (non-blocking)
	if cType == p_common.CLIENT_CONNECTION_TYPE && p.blockProcessor != nil && conn.IsConnect() {
		go p.blockProcessor.FlushPendingReceipts(address, conn)
	}

	return nil
}

func (p *ConnectionProcessor) ProcessPing(
	request network.Request,
) error {
	conn := request.Connection()
	if conn != nil {
		logger.Debug(
			"ProcessPing: received keepalive",
			"remoteAddr", conn.RemoteAddrSafe(),
			"address", conn.Address().Hex(),
		)
	}
	return nil
}
