package connection_client

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

// Command constants (inline để tránh import cmd/simple_chain/command — cross-workspace)
const (
	cmdReadTransaction              = "ReadTransaction"
	cmdSendTransactionWithDeviceKey = "SendTransactionWithDeviceKey"
	cmdSendTransaction              = "SendTransaction"
	cmdReceipt                      = "Receipt"
	cmdTransactionError             = "TransactionError"
	cmdTransactionSuccess           = "TransactionSuccess"
	cmdTransactionReceipt           = "TransactionReceipt"
	cmdLogs                         = "Logs"
	cmdBlockNumber                  = "BlockNumber"
	cmdGetTransactionsByBlockNumber = "GetTransactionsByBlockNumber"
	cmdGetBlockNumber               = "GetBlockNumber"
	cmdGetLogs                      = "GetLogs"
	cmdGetTransactionReceipt        = "GetTransactionReceipt"
	cmdGetAccountState              = "GetAccountState"
	cmdAccountState                 = "AccountState"
	cmdGetDeviceKey                 = "GetDeviceKey"
	cmdDeviceKey                    = "DeviceKey"
	cmdGetNonce                     = "GetNonce"
	cmdNonce                        = "Nonce"
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

// SendTransactionWithDeviceKey gửi SendTransactionWithDeviceKey command lên chain
// và chỉ đợi TransactionSuccess hoặc TransactionError response.
// Trả về txHash bytes (32 bytes) ngay khi chain xác nhận nhận TX thành công.
// Không đợi Receipt.
func (c *ConnectionClient) SendTransactionWithDeviceKey(txWithDKBytes []byte, timeout time.Duration) ([]byte, error) {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return nil, fmt.Errorf("not connected to cluster %s", c.key)
	}

	id := uuid.New().String()

	responseChan := make(chan interface{}, 2)
	c.pendingRequests.Store(id, responseChan)
	defer c.pendingRequests.Delete(id)

	// Tạo message với ID trong header để dispatch response về đúng caller
	msg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: cmdSendTransactionWithDeviceKey,
			ID:      id,
		},
		Body: txWithDKBytes,
	})

	if err := c.connection.SendMessage(msg); err != nil {
		return nil, fmt.Errorf("failed to send SendTransactionWithDeviceKey: %w", err)
	}

	// Đợi TransactionSuccess hoặc TransactionError
	deadline := time.After(timeout)
	for {
		select {
		case res := <-responseChan:
			switch v := res.(type) {
			case *TransactionSuccessResponse:
				// Got txHash — return immediately
				return v.Body, nil
			case *TransactionErrorResponse:
				// TransactionError from chain
				txErr := &pb.TransactionHashWithError{}
				if unmarshalErr := proto.Unmarshal(v.Body, txErr); unmarshalErr == nil {
					return nil, fmt.Errorf("transaction error from chain (code=%d): %s", txErr.Code, txErr.Description)
				}
				return nil, fmt.Errorf("transaction error from chain: 0x%x", v.Body)
			case []byte:
				// Receipt arrived before TransactionSuccess (unlikely but possible)
				return v, nil
			default:
				return nil, fmt.Errorf("invalid response type for SendTransactionWithDeviceKey: %T", res)
			}
		case err := <-c.errorNotifyChan:
			return nil, fmt.Errorf("connection error: %w", err)
		case <-deadline:
			return nil, fmt.Errorf("timeout waiting for SendTransactionWithDeviceKey response (id=%s)", id)
		case <-c.ctx.Done():
			return nil, fmt.Errorf("context cancelled")
		}
	}
}

// SendTransactionWithDeviceKeyAndWaitReceipt gửi SendTransactionWithDeviceKey command lên chain
// và đợi Receipt trực tiếp (proto Receipt bytes).
// TransactionSuccess bị bỏ qua (không cần chờ). TransactionError → trả error.
// Timeout → trả error.
func (c *ConnectionClient) SendTransactionWithDeviceKeyAndWaitReceipt(txWithDKBytes []byte, timeout time.Duration) ([]byte, error) {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return nil, fmt.Errorf("not connected to cluster %s", c.key)
	}

	id := uuid.New().String()

	responseChan := make(chan interface{}, 2) // Buffer 2: có thể nhận TransactionSuccess + Receipt
	c.pendingRequests.Store(id, responseChan)
	defer c.pendingRequests.Delete(id)

	msg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: cmdSendTransactionWithDeviceKey,
			ID:      id,
		},
		Body: txWithDKBytes,
	})

	if err := c.connection.SendMessage(msg); err != nil {
		return nil, fmt.Errorf("failed to send SendTransactionWithDeviceKey: %w", err)
	}

	// Đợi Receipt trực tiếp — không cần 2 phase
	// Receipt có thể đến trước hoặc sau TransactionSuccess, đều handle được
	deadline := time.After(timeout)
	for {
		select {
		case res := <-responseChan:
			switch v := res.(type) {
			case []byte:
				// Receipt bytes (proto Receipt) — trả về ngay
				return v, nil
			case *TransactionErrorResponse:
				// TransactionError from chain
				txErr := &pb.TransactionHashWithError{}
				if unmarshalErr := proto.Unmarshal(v.Body, txErr); unmarshalErr == nil {
					return nil, fmt.Errorf("transaction error from chain (code=%d): %s", txErr.Code, txErr.Description)
				}
				return nil, fmt.Errorf("transaction error from chain: 0x%x", v.Body)
			case *TransactionSuccessResponse:
				// Bỏ qua TransactionSuccess — tiếp tục đợi Receipt
				continue
			default:
				return nil, fmt.Errorf("invalid response type: %T", res)
			}
		case err := <-c.errorNotifyChan:
			return nil, fmt.Errorf("connection error: %w", err)
		case <-deadline:
			return nil, fmt.Errorf("timeout waiting for receipt (id=%s)", id)
		case <-c.ctx.Done():
			return nil, fmt.Errorf("context cancelled")
		}
	}
}

// GetAccountState gửi GetAccountState command lên chain qua TCP và đợi AccountState response.
// Trả về raw AccountState proto bytes để caller unmarshal.
func (c *ConnectionClient) GetAccountState(address []byte, timeout time.Duration) ([]byte, error) {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return nil, fmt.Errorf("not connected to cluster %s", c.key)
	}

	id := uuid.New().String()

	responseChan := make(chan interface{}, 1)
	c.pendingRequests.Store(id, responseChan)
	defer c.pendingRequests.Delete(id)

	msg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: cmdGetAccountState,
			ID:      id,
		},
		Body: address,
	})

	if err := c.connection.SendMessage(msg); err != nil {
		return nil, fmt.Errorf("failed to send GetAccountState: %w", err)
	}

	for {
		select {
		case res := <-responseChan:
			if data, ok := res.([]byte); ok {
				return data, nil
			}
			return nil, fmt.Errorf("invalid response type for GetAccountState: %T", res)
		case err := <-c.errorNotifyChan:
			return nil, fmt.Errorf("connection error: %w", err)
		case <-time.After(timeout):
			return nil, fmt.Errorf("timeout waiting for GetAccountState (id=%s)", id)
		case <-c.ctx.Done():
			return nil, fmt.Errorf("context cancelled")
		}
	}
}

// GetDeviceKey gửi GetDeviceKey command lên chain qua TCP và đợi DeviceKey response.
// Chain trả về hashBytes + deviceKeyBytes (concatenated).
// Trả về raw response bytes.
func (c *ConnectionClient) GetDeviceKey(hashBytes []byte, timeout time.Duration) ([]byte, error) {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return nil, fmt.Errorf("not connected to cluster %s", c.key)
	}

	id := uuid.New().String()

	responseChan := make(chan interface{}, 1)
	c.pendingRequests.Store(id, responseChan)
	defer c.pendingRequests.Delete(id)

	msg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: cmdGetDeviceKey,
			ID:      id,
		},
		Body: hashBytes,
	})

	if err := c.connection.SendMessage(msg); err != nil {
		return nil, fmt.Errorf("failed to send GetDeviceKey: %w", err)
	}

	for {
		select {
		case res := <-responseChan:
			if data, ok := res.([]byte); ok {
				return data, nil
			}
			return nil, fmt.Errorf("invalid response type for GetDeviceKey: %T", res)
		case err := <-c.errorNotifyChan:
			return nil, fmt.Errorf("connection error: %w", err)
		case <-time.After(timeout):
			return nil, fmt.Errorf("timeout waiting for GetDeviceKey (id=%s)", id)
		case <-c.ctx.Done():
			return nil, fmt.Errorf("context cancelled")
		}
	}
}

// GetTransactionReceipt gửi GetTransactionReceipt command lên chain qua TCP.
// Body chứa GetTransactionReceiptRequest proto (TransactionHash).
// Trả về raw GetTransactionReceiptResponse proto bytes.
func (c *ConnectionClient) GetTransactionReceipt(reqBytes []byte, timeout time.Duration) ([]byte, error) {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return nil, fmt.Errorf("not connected to cluster %s", c.key)
	}

	id := uuid.New().String()

	responseChan := make(chan interface{}, 1)
	c.pendingRequests.Store(id, responseChan)
	defer c.pendingRequests.Delete(id)

	msg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: cmdGetTransactionReceipt,
			ID:      id,
		},
		Body: reqBytes,
	})

	if err := c.connection.SendMessage(msg); err != nil {
		return nil, fmt.Errorf("failed to send GetTransactionReceipt: %w", err)
	}

	for {
		select {
		case res := <-responseChan:
			if data, ok := res.([]byte); ok {
				return data, nil
			}
			return nil, fmt.Errorf("invalid response type for GetTransactionReceipt: %T", res)
		case err := <-c.errorNotifyChan:
			return nil, fmt.Errorf("connection error: %w", err)
		case <-time.After(timeout):
			return nil, fmt.Errorf("timeout waiting for GetTransactionReceipt (id=%s)", id)
		case <-c.ctx.Done():
			return nil, fmt.Errorf("context cancelled")
		}
	}
}

// GetNonce gửi GetNonce command lên chain qua TCP.
// Body chứa address bytes (20 bytes).
// Chain trả về Nonce command với body = uint64 big-endian.
func (c *ConnectionClient) GetNonce(addressBytes []byte, timeout time.Duration) (uint64, error) {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return 0, fmt.Errorf("not connected to cluster %s", c.key)
	}

	id := uuid.New().String()

	responseChan := make(chan interface{}, 1)
	c.pendingRequests.Store(id, responseChan)
	defer c.pendingRequests.Delete(id)

	msg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: cmdGetNonce,
			ID:      id,
		},
		Body: addressBytes,
	})

	if err := c.connection.SendMessage(msg); err != nil {
		return 0, fmt.Errorf("failed to send GetNonce: %w", err)
	}

	for {
		select {
		case res := <-responseChan:
			if nonce, ok := res.(uint64); ok {
				return nonce, nil
			}
			return 0, fmt.Errorf("invalid response type for GetNonce: %T", res)
		case err := <-c.errorNotifyChan:
			return 0, fmt.Errorf("connection error: %w", err)
		case <-time.After(timeout):
			return 0, fmt.Errorf("timeout waiting for GetNonce (id=%s)", id)
		case <-c.ctx.Done():
			return 0, fmt.Errorf("context cancelled")
		}
	}
}
