package tcp_server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gorilla/websocket"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/app"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

// RpcTcpServer sử dụng module pkg/network (SocketServer) để lắng nghe TCP
type RpcTcpServer struct {
	AppCtx             *app.Context
	connectionsManager t_network.ConnectionsManager
	messageSender      t_network.MessageSender
	socketServer       t_network.SocketServer
	keyPair            *bls.KeyPair
	ctx                context.Context
	cancel             context.CancelFunc
	TcpSubManager      *TcpSubscriptionManager // quản lý TCP subscriptions
	wsRelayRunning     bool                    // WS relay đã start chưa
	wsRelayMu          sync.Mutex              // bảo vệ wsRelayRunning
	wsRelayCtx         context.Context         // context riêng cho WS relay
	wsRelayCancel      context.CancelFunc      // cancel WS relay

	// clientConnections lưu wallet address → TCP connection.
	// Dùng để gửi receipt cho người nhận khi TX là chuyển tiền.
	clientConnections sync.Map // key: common.Address, value: t_network.Connection
}

// RpcHandler implements network.Handler interface
type RpcHandler struct {
	srv *RpcTcpServer
}

func (h *RpcHandler) HandleRequest(request t_network.Request) error {
	return h.srv.HandleRequest(request)
}

// New tạo RpcTcpServer instance
func New(appCtx *app.Context) (*RpcTcpServer, error) {
	srv := &RpcTcpServer{
		AppCtx: appCtx,
	}
	srv.ctx, srv.cancel = context.WithCancel(context.Background())

	// Initialize BLS
	srv.keyPair = appCtx.ClientRpc.KeyPair

	// Initialize network components
	srv.connectionsManager = network.NewConnectionsManager()
	srv.messageSender = network.NewMessageSender("1.0.0")

	// Initialize TCP subscription manager
	srv.TcpSubManager = NewTcpSubscriptionManager(srv.messageSender)

	// Gắn TcpSubManager vào SubInterceptor để broadcastEvent() phát cả WS + TCP
	appCtx.SubInterceptor.SetTcpBroadcaster(srv.TcpSubManager)

	handler := &RpcHandler{srv: srv}

	// Create socket server
	socketServer, err := network.NewSocketServer(
		nil,
		srv.keyPair,
		srv.connectionsManager,
		handler,
		"RPC_TCP_SERVER",
		"1.0.0",
	)
	if err != nil {
		return nil, err
	}
	socketServer.SetContext(srv.ctx, srv.cancel)
	srv.socketServer = socketServer

	// Đăng ký callback khi client ngắt kết nối → cleanup subscriptions
	socketServer.AddOnDisconnectedCallBack(func(conn t_network.Connection) {
		srv.OnConnectionClosed(conn)
	})
	logger.Info("🔌 TCP RPC Server initialized (with subscription support)")
	return srv, nil
}

// ListenAndServe khởi động TCP server
// WS relay chỉ start khi có subscription cho non-intercepted contracts (lazy)
func (srv *RpcTcpServer) ListenAndServe(address string) error {
	return srv.socketServer.Listen(address)
}

// Stop dừng TCP server
func (srv *RpcTcpServer) Stop() {
	if srv.socketServer != nil {
		srv.socketServer.Stop()
	}
	if srv.cancel != nil {
		srv.cancel()
	}
	logger.Info("TCP RPC Server stopped")
}

// startWsEventRelay kết nối WS đến chain và relay events về TCP subscribers
// Dùng wsRelayCtx để có thể stop khi không còn subscription
func (srv *RpcTcpServer) startWsEventRelay() {
	defer func() {
		srv.wsRelayMu.Lock()
		srv.wsRelayRunning = false
		srv.wsRelayMu.Unlock()
		logger.Info("📡 WS Event Relay stopped")
	}()

	wsURL := srv.AppCtx.Cfg.WSSServerURL
	if wsURL == "" {
		httpURL := srv.AppCtx.Cfg.RPCServerURL
		if strings.HasPrefix(httpURL, "https") {
			wsURL = "wss" + strings.TrimPrefix(httpURL, "https")
		} else {
			wsURL = "ws" + strings.TrimPrefix(httpURL, "http")
		}
	}

	for {
		select {
		case <-srv.ctx.Done():
			return
		case <-srv.wsRelayCtx.Done():
			logger.Info("📡 WS Event Relay: stopped (no more subscriptions)")
			return
		default:
		}
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{})
		if err != nil {
			logger.Error("📡 TCP Event Relay: WS connect failed: %v, retrying in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}

		srv.relayWsEvents(conn)
		conn.Close()

		// Kiểm tra nếu relay bị stop (do không còn sub)
		select {
		case <-srv.wsRelayCtx.Done():
			return
		default:
		}
		logger.Warn("📡 TCP Event Relay: WS disconnected, reconnecting in 2s...")
		time.Sleep(2 * time.Second)
	}
}

// relayWsEvents đọc events từ WS và broadcast qua TCP subscriptions
// Có ping keepalive mỗi 30s để giữ kết nối WS
func (srv *RpcTcpServer) relayWsEvents(conn *websocket.Conn) {
	// Subscribe to logs on chain
	subscribeReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "eth_subscribe",
		"params":  []interface{}{"logs", map[string]interface{}{}},
	}

	wsMu := &sync.Mutex{}
	wsMu.Lock()
	err := conn.WriteJSON(subscribeReq)
	wsMu.Unlock()
	if err != nil {
		logger.Error("📡 TCP Event Relay: failed to subscribe: %v", err)
		return
	}

	// Ping keepalive goroutine - gửi ping mỗi 30s
	stopPing := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-srv.ctx.Done():
				return
			case <-srv.wsRelayCtx.Done():
				// Không còn subscription → đóng WS connection để read loop thoát
				conn.Close()
				return
			case <-ticker.C:
				wsMu.Lock()
				err := conn.WriteMessage(websocket.PingMessage, []byte("keepalive"))
				wsMu.Unlock()
				if err != nil {
					logger.Warn("📡 TCP Event Relay: ping failed: %v", err)
					return
				}
			}
		}
	}()

	// Xử lý pong response từ server
	conn.SetPongHandler(func(appData string) error {
		logger.Debug("📡 TCP Event Relay: pong received")
		return nil
	})

	defer close(stopPing)

	for {
		select {
		case <-srv.ctx.Done():
			return
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			logger.Error("📡 TCP Event Relay: read error: %v", err)
			return
		}

		// Parse event
		var wsMsg map[string]interface{}
		if err := json.Unmarshal(message, &wsMsg); err != nil {
			continue
		}

		// Kiểm tra nếu đây là eth_subscription notification
		method, _ := wsMsg["method"].(string)
		if method != "eth_subscription" {
			continue
		}

		params, ok := wsMsg["params"].(map[string]interface{})
		if !ok {
			continue
		}

		result, ok := params["result"].(map[string]interface{})
		if !ok {
			continue
		}

		// Extract contract address và topics từ event
		contractAddr, _ := result["address"].(string)
		var topics []string
		if topicsRaw, ok := result["topics"].([]interface{}); ok {
			for _, t := range topicsRaw {
				if ts, ok := t.(string); ok {
					topics = append(topics, ts)
				}
			}
		}

		if contractAddr != "" {
			// Broadcast đến TCP subscribers
			srv.TcpSubManager.BroadcastEvent(contractAddr, topics, result)
		}
	}
}

// GetSubManager trả về subscription manager (để bên ngoài có thể broadcast nếu cần)
func (srv *RpcTcpServer) GetSubManager() *TcpSubscriptionManager {
	return srv.TcpSubManager
}

// handleEthSubscribe xử lý TCP eth_subscribe request
// Nhận proto TcpSubscribeRequest (binary addresses + topics)
func (srv *RpcTcpServer) handleEthSubscribe(request t_network.Request) error {
	conn := request.Connection()
	msgID := request.Message().ID()
	body := request.Message().Body()

	// Parse proto TcpSubscribeRequest
	tcpReq := &pb.TcpSubscribeRequest{}
	if err := proto.Unmarshal(body, tcpReq); err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32602,
			Message: "Invalid params: failed to parse TcpSubscribeRequest",
		})
	}

	// Convert bytes → hex strings
	var contractAddrs []string
	for _, addrBytes := range tcpReq.Addresses {
		contractAddrs = append(contractAddrs, common.BytesToAddress(addrBytes).Hex())
	}
	var topics []string
	for _, topicBytes := range tcpReq.Topics {
		topics = append(topics, common.BytesToHash(topicBytes).Hex())
	}

	if len(contractAddrs) == 0 {
		logger.Warn("TCP eth_subscribe: no contract address specified")
	}

	// Kiểm tra xem contracts có nằm trong interceptor hay không
	isIntercepted := false
	if len(contractAddrs) > 0 {
		allIntercepted := true
		for _, addr := range contractAddrs {
			if !srv.AppCtx.SubInterceptor.IsMonitoredContract(addr) {
				allIntercepted = false
				break
			}
		}
		isIntercepted = allIntercepted
	}

	// Create subscription
	subID := srv.TcpSubManager.CreateSubscription(conn, contractAddrs, topics)

	if isIntercepted {
		logger.Info("✅ TCP eth_subscribe (intercepted): subID=%s, contracts=%v from %s",
			subID, contractAddrs, conn.RemoteAddrSafe())
	} else {
		srv.ensureWsRelay()
		logger.Info("✅ TCP eth_subscribe (chain relay): subID=%s, contracts=%v from %s",
			subID, contractAddrs, conn.RemoteAddrSafe())
	}

	// Respond with subscription ID as raw bytes
	return srv.sendRpcResponse(conn, msgID, []byte(subID), nil)
}

// ensureWsRelay đảm bảo WS relay đang chạy (lazy start)
func (srv *RpcTcpServer) ensureWsRelay() {
	srv.wsRelayMu.Lock()
	defer srv.wsRelayMu.Unlock()
	if srv.wsRelayRunning {
		return
	}
	srv.wsRelayRunning = true
	srv.wsRelayCtx, srv.wsRelayCancel = context.WithCancel(srv.ctx)
	go srv.startWsEventRelay()
	logger.Info("📡 WS Event Relay started (lazy - có non-intercepted subscription)")
}

// stopWsRelayIfEmpty dừng WS relay nếu không còn subscription nào
func (srv *RpcTcpServer) stopWsRelayIfEmpty() {
	if srv.TcpSubManager.SubscriptionCount() > 0 {
		return
	}
	srv.wsRelayMu.Lock()
	defer srv.wsRelayMu.Unlock()
	if !srv.wsRelayRunning {
		return
	}
	if srv.wsRelayCancel != nil {
		srv.wsRelayCancel()
		logger.Info("📡 WS Event Relay: stopping (no more subscriptions)")
	}
}

// handleEthUnsubscribe xử lý TCP eth_unsubscribe request
// Nhận proto TcpUnsubscribeRequest
func (srv *RpcTcpServer) handleEthUnsubscribe(request t_network.Request) error {
	conn := request.Connection()
	msgID := request.Message().ID()
	body := request.Message().Body()

	// Parse proto TcpUnsubscribeRequest
	tcpReq := &pb.TcpUnsubscribeRequest{}
	if err := proto.Unmarshal(body, tcpReq); err != nil || tcpReq.SubscriptionId == "" {
		// Trả false nếu parse fail
		return srv.sendRpcResponse(conn, msgID, []byte{0}, nil)
	}

	subID := tcpReq.SubscriptionId
	removed := srv.TcpSubManager.RemoveSubscription(subID)
	logger.Info("TCP eth_unsubscribe: subID=%s, removed=%v from %s",
		subID, removed, conn.RemoteAddrSafe())

	// Nếu không còn subscription nào → stop WS relay
	srv.stopWsRelayIfEmpty()

	// Trả 1 byte: 1=true, 0=false
	result := byte(0)
	if removed {
		result = 1
	}
	return srv.sendRpcResponse(conn, msgID, []byte{result}, nil)
}

// OnConnectionClosed cleanup subscriptions + clientConnections khi client ngắt kết nối
func (srv *RpcTcpServer) OnConnectionClosed(conn t_network.Connection) {
	removed := srv.TcpSubManager.RemoveByConnection(conn)
	if removed > 0 {
		logger.Info("🔌 TCP client disconnected: %s → cleaned up %d subscription(s)",
			conn.RemoteAddrSafe(), removed)
		// Nếu không còn subscription nào → stop WS relay
		srv.stopWsRelayIfEmpty()
	}

	// Xóa client connection mapping
	srv.clientConnections.Range(func(key, value interface{}) bool {
		if value.(t_network.Connection) == conn {
			srv.clientConnections.Delete(key)
			logger.Info("🔌 Removed client connection mapping: addr=%s", key.(common.Address).Hex())
		}
		return true
	})
}

// handleInitConnection lưu address → connection mapping khi client kết nối.
func (srv *RpcTcpServer) handleInitConnection(request t_network.Request) error {
	conn := request.Connection()
	if conn == nil {
		return nil
	}

	initData := &pb.InitConnection{}
	if err := proto.Unmarshal(request.Message().Body(), initData); err != nil {
		logger.Warn("handleInitConnection: unmarshal error: %v", err)
		return nil
	}
	address := common.BytesToAddress(initData.Address)
	// Lưu address → connection
	srv.clientConnections.Store(address, conn)
	logger.Info("📌 [RPC] Client registered: addr=%s remote=%s",
		address.Hex(), conn.RemoteAddrSafe())

	return nil
}

// GetClientConnection tìm connection theo wallet address.
// Dùng để gửi receipt cho người nhận (toAddress) khi TX là chuyển tiền.
func (srv *RpcTcpServer) GetClientConnection(addr common.Address) t_network.Connection {
	if val, ok := srv.clientConnections.Load(addr); ok {
		conn := val.(t_network.Connection)
		if conn.IsConnect() {
			return conn
		}
		// Connection đã disconnect → xóa
		srv.clientConnections.Delete(addr)
	}
	return nil
}
