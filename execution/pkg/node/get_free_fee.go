package node

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// HandleFreeFeeRequest xử lý yêu cầu fee addresses qua TCP route "FreeFeeRequest".
// Master node trả về danh sách fee addresses dưới dạng JSON.
func (node *HostNode) HandleFreeFeeRequest(request network.Request) error {
	addresses := node.GetFeeAddresses()

	addressListData, err := json.Marshal(addresses)
	if err != nil {
		return fmt.Errorf("error marshaling fee addresses: %w", err)
	}

	conn := request.Connection()
	if conn == nil || !conn.IsConnect() {
		return fmt.Errorf("connection not available for FreeFeeRequest response")
	}

	if node.MessageSender == nil {
		return fmt.Errorf("MessageSender not initialized")
	}

	return node.MessageSender.SendBytes(conn, "FreeFeeResponse", addressListData)
}

// GetFeeAddressesFromMaster lấy danh sách fee addresses từ Master qua TCP.
// Gửi request "FreeFeeRequest" và chờ response "FreeFeeResponse" qua route handler.
func (node *HostNode) GetFeeAddressesFromMaster() ([]string, error) {
	if node.ConnectionsManager == nil || node.MessageSender == nil {
		return nil, fmt.Errorf("network components not initialized")
	}

	const maxRequestAttempts = 10
	const requestRetryDelay = 10 * time.Second

	var lastErr error

	for attempt := 0; attempt < maxRequestAttempts; attempt++ {
		logger.Info("Requesting fee addresses from master (attempt %d/%d)...", attempt+1, maxRequestAttempts)

		// Tìm master connection
		masterConns := node.ConnectionsManager.ConnectionsByType(
			common.MapConnectionTypeToIndex(common.MASTER_CONNECTION_TYPE))

		if len(masterConns) == 0 {
			lastErr = fmt.Errorf("no master connection available")
			logger.Error(lastErr.Error())
			time.Sleep(requestRetryDelay)
			continue
		}

		// Gửi request tới master
		for _, conn := range masterConns {
			if conn == nil || !conn.IsConnect() {
				continue
			}

			err := node.MessageSender.SendBytes(conn, "FreeFeeRequest", nil)
			if err != nil {
				lastErr = fmt.Errorf("failed to send FreeFeeRequest: %w", err)
				logger.Error(lastErr.Error())
				continue
			}

			// NOTE: Fee addresses response sẽ đến qua route handler "FreeFeeResponse"
			// Tạm thời return empty list và log — caller sẽ xử lý
			logger.Info("Sent FreeFeeRequest to master, awaiting response via route handler.")

			// Chờ response — fee addresses sẽ được set qua SetFeeAddresses khi master respond
			time.Sleep(3 * time.Second)

			// Check if fee addresses were set
			addresses := node.GetFeeAddresses()
			if len(addresses) > 0 {
				logger.Info("Successfully got %d fee addresses from master", len(addresses))
				return addresses, nil
			}

			lastErr = fmt.Errorf("no fee addresses received from master after waiting")
		}

		time.Sleep(requestRetryDelay)
	}

	finalErr := fmt.Errorf("không thể lấy danh sách địa chỉ phí từ master sau %d lần thử. Lỗi cuối: %w",
		maxRequestAttempts, lastErr)
	logger.Error(finalErr.Error())
	return nil, finalErr
}
