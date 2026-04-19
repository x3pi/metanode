package connection_client

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// Command constants (inline để tránh import cmd/simple_chain/command — cross-workspace)
const (
	cmdReadTransaction              = "ReadTransaction"
	cmdSendTransactionWithDeviceKey = "SendTransactionWithDeviceKey"
	cmdSendTransaction              = "SendTransaction"
	cmdReceipt                      = "Receipt"
	cmdTransactionReceipt           = "TransactionReceipt"
	cmdLogs                         = "Logs"
	cmdBlockNumber                  = "BlockNumber"
	cmdGetTransactionsByBlockNumber = "GetTransactionsByBlockNumber"
	cmdGetBlockNumber               = "GetBlockNumber"
	cmdGetLogs                      = "GetLogs"
	cmdGetTransactionReceipt        = "GetTransactionReceipt"
)

// ===================== Chain-Direct Methods =====================
// Dùng cho RPC Server gọi chain qua TCP pool, ID-based request-response

// ReadTransaction gửi ReadTransaction command lên chain và đợi Receipt response.
// Body chứa transaction bytes đã marshal.
// Trả về raw receipt bytes để caller unmarshal.
func (c *ConnectionClient) ReadTransaction(txBytes []byte, timeout time.Duration) ([]byte, error) {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return nil, fmt.Errorf("not connected to cluster %s", c.key)
	}

	id := uuid.New().String()

	responseChan := make(chan interface{}, 1)
	c.pendingRequests.Store(id, responseChan)
	defer c.pendingRequests.Delete(id)

	// Tạo message với ID trong header
	msg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: cmdReadTransaction,
			ID:      id,
		},
		Body: txBytes,
	})

	if err := c.connection.SendMessage(msg); err != nil {
		return nil, fmt.Errorf("failed to send ReadTransaction: %w", err)
	}

	for {
		select {
		case res := <-responseChan:
			if receiptBytes, ok := res.([]byte); ok {
				return receiptBytes, nil
			}
			return nil, fmt.Errorf("invalid response type for ReadTransaction")
		case err := <-c.errorNotifyChan:
			return nil, fmt.Errorf("connection error: %w", err)
		case <-time.After(timeout):
			return nil, fmt.Errorf("timeout waiting for ReadTransaction receipt (id=%s)", id)
		case <-c.ctx.Done():
			return nil, fmt.Errorf("context cancelled")
		}
	}
}

// SendTransactionWithDeviceKey gửi SendTransactionWithDeviceKey command lên chain.
// Fire-and-forget: không đợi receipt (receipt sẽ được nhận qua event subscription hoặc polling).
// Body chứa TransactionWithDeviceKey proto bytes đã marshal.
func (c *ConnectionClient) SendTransactionWithDeviceKey(txWithDKBytes []byte) error {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return fmt.Errorf("not connected to cluster %s", c.key)
	}

	if err := c.messageSender.SendBytes(c.connection, cmdSendTransactionWithDeviceKey, txWithDKBytes); err != nil {
		return fmt.Errorf("failed to send SendTransactionWithDeviceKey: %w", err)
	}
	logger.Debug("SendTransactionWithDeviceKey sent via ConnectionClient (cluster=%s)", c.key)
	return nil
}

// SendTransaction gửi SendTransaction command lên chain (fire-and-forget).
func (c *ConnectionClient) SendTransaction(txBytes []byte) error {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return fmt.Errorf("not connected to cluster %s", c.key)
	}

	if err := c.messageSender.SendBytes(c.connection, cmdSendTransaction, txBytes); err != nil {
		return fmt.Errorf("failed to send SendTransaction: %w", err)
	}
	return nil
}
