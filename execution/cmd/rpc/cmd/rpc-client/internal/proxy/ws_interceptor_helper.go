package proxy

import (
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/gorilla/websocket"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/internal/ws_writer"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/models"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// handleSubscribeRequest xử lý logic phức tạp cho eth_subscribe
func (p *RpcReverseProxy) HandleSubscribeRequest(req models.JSONRPCRequestRaw,
	clientConn, targetConn *websocket.Conn,
	clientWriter, targetWriter *ws_writer.WebSocketWriter,
	errChan chan<- error,
	quit <-chan struct{}) error {
	var params []interface{}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return fmt.Errorf("failed to parse subscribe params: %w", err)
	}

	if len(params) < 2 {
		return fmt.Errorf("invalid eth_subscribe params: expected at least 2 params")
	}

	filterMap, ok := params[1].(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid filter format: expected object")
	}
	// ✅ Lấy danh sách addresses (hỗ trợ cả string và array)
	var contractAddrs []string
	if addr, ok := filterMap["address"].(string); ok {
		contractAddrs = []string{addr}
	} else if addrs, ok := filterMap["address"].([]interface{}); ok {
		for _, a := range addrs {
			if addrStr, ok := a.(string); ok {
				contractAddrs = append(contractAddrs, addrStr)
			}
		}
	}
	if len(contractAddrs) == 0 {
		logger.Info("No address filter, forwarding to chain")
		return p.forwardToUpstream(req, targetWriter, errChan, quit)
	}
	shouldIntercept, err := p.AppCtx.SubInterceptor.ValidateSubscriptionAddresses(contractAddrs)
	if err != nil {
		logger.Error("Subscription validation failed: %v", err)
		return err
	}
	if shouldIntercept {
		// ✅ TẤT CẢ addresses đều nằm trong danh sách theo dõi → BẮT LẠI
		logger.Info("✅ All addresses are monitored. Intercepting subscription: %v", contractAddrs)
		// Lấy topics
		var topics []string
		if topicsRaw, ok := filterMap["topics"].([]interface{}); ok {
			for _, t := range topicsRaw {
				if topicArr, ok := t.([]interface{}); ok && len(topicArr) > 0 {
					if topicStr, ok := topicArr[0].(string); ok {
						topics = append(topics, topicStr)
					}
				}
			}
		}
		// Tạo subscription (lưu tất cả addresses)
		// Note: Bạn cần cập nhật SubManager để hỗ trợ nhiều addresses
		subID := p.AppCtx.SubInterceptor.CreateSubscription(clientConn, clientWriter, contractAddrs, topics)
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.Id,
			"result":  subID,
		}
		return clientWriter.WriteJSON(response)
	}
	// Forward lên chain nếu không intercept
	return p.forwardToUpstream(req, targetWriter, errChan, quit)
}

func (p *RpcReverseProxy) TriggerFakePingEvent(contractAddr string, id uint64, message string) error {
	const PingEventHash = "0xa08082c7663f884e3c4d325ad1de149f6e167a84556be205103c16b1595d22cc"
	// Tạo dữ liệu event
	fakeArgs := abi.Arguments{{Type: abi.Type{T: abi.StringTy}}}
	packedData, err := fakeArgs.Pack(message)
	if err != nil {
		return fmt.Errorf("failed to pack message: %w", err)
	}

	indexedParamHex := fmt.Sprintf("0x%064x", id)
	fakeDataHex := fmt.Sprintf("0x%x", packedData)

	eventData := map[string]interface{}{
		"address":          contractAddr,
		"topics":           []string{PingEventHash, indexedParamHex},
		"data":             fakeDataHex,
		"blockNumber":      "0x3DF",
		"transactionHash":  "0xa08082c7663f884e3c4d325ad1de149f6e167a84556be205103c16b1595d22cc",
		"blockHash":        "0xa08082c7663f884e3c4d325ad1de149f6e167a84556be205103c16b1595d22cc",
		"logIndex":         "0x0",
		"transactionIndex": "0x0",
		"removed":          false,
	}

	// Gửi tới tất cả subscribers
	topics := []string{PingEventHash}
	p.AppCtx.SubInterceptor.BroadcastEventToContract(contractAddr, topics, eventData)
	logger.Info("🔥 Triggered fake Ping event: contract=%s, id=%d, message=%s", contractAddr, id, message)
	return nil
}

// forwardToUpstream helper function để gửi request lên chain
func (p *RpcReverseProxy) forwardToUpstream(
	req models.JSONRPCRequestRaw,
	targetWriter *ws_writer.WebSocketWriter,
	errChan chan<- error,
	quit <-chan struct{},
) error {
	if err := targetWriter.WriteJSON(req); err != nil {
		select {
		case errChan <- fmt.Errorf("upstream write error: %w", err):
		case <-quit:
		}
		return err
	}
	return nil
}
