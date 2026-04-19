package connection_client

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

// GetLogs sends a GetLogs request and waits for the response
// Dùng Header.ID để match response
func (c *ConnectionClient) GetLogs(
	blockHash []byte,
	fromBlock string,
	toBlock string,
	addresses []common.Address,
	topics [][]common.Hash,
) (*pb.GetLogsResponse, error) {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return nil, fmt.Errorf("not connected to cluster %s", c.key)
	}

	id := uuid.New().String()

	// Build GetLogsRequest — không cần RequestId, chỉ filter params
	request := &pb.GetLogsRequest{}
	if len(blockHash) > 0 {
		request.BlockHash = blockHash
	}
	if fromBlock != "" {
		request.FromBlock = []byte(fromBlock)
	}
	if toBlock != "" {
		request.ToBlock = []byte(toBlock)
	}
	if len(addresses) > 0 {
		request.Addresses = make([][]byte, len(addresses))
		for i, addr := range addresses {
			request.Addresses[i] = addr.Bytes()
		}
	}
	if len(topics) > 0 {
		request.Topics = make([]*pb.TopicFilter, len(topics))
		for i, topicList := range topics {
			if len(topicList) > 0 {
				hashes := make([][]byte, len(topicList))
				for j, hash := range topicList {
					hashes[j] = hash.Bytes()
				}
				request.Topics[i] = &pb.TopicFilter{
					Hashes: hashes,
				}
			}
		}
	}

	requestBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GetLogsRequest: %w", err)
	}

	responseChan := make(chan interface{}, 1)
	c.pendingRequests.Store(id, responseChan)
	defer c.pendingRequests.Delete(id)

	// Tạo message với ID trong header
	msg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: cmdGetLogs,
			ID:      id,
		},
		Body: requestBytes,
	})

	logger.Info("GetLogs: Sending request with header ID: %s", id)

	if err := c.connection.SendMessage(msg); err != nil {
		return nil, fmt.Errorf("failed to send GetLogs request: %w", err)
	}

	for {
		select {
		case res := <-responseChan:
			if response, ok := res.(*pb.GetLogsResponse); ok {
				if response.Error != "" {
					return nil, fmt.Errorf("server error: %s", response.Error)
				}
				return response, nil
			}
			return nil, fmt.Errorf("invalid response type")
		case err := <-c.errorNotifyChan:
			return nil, fmt.Errorf("connection error: %w", err)
		case <-time.After(30 * time.Second):
			return nil, fmt.Errorf("timeout waiting for GetLogsResponse")
		case <-c.ctx.Done():
			return nil, fmt.Errorf("context cancelled")
		}
	}
}

// GetTransactionReceipt sends a GetTransactionReceipt request and waits for the response
// Dùng Header.ID để match response
func (c *ConnectionClient) GetTransactionReceipt(
	txHash common.Hash,
) (*pb.GetTransactionReceiptResponse, error) {
	if atomic.LoadInt32(&c.connected) != 1 || c.connection == nil {
		return nil, fmt.Errorf("not connected to cluster %s", c.key)
	}

	id := uuid.New().String()

	// Build request — chỉ TransactionHash, không cần RequestId
	request := &pb.GetTransactionReceiptRequest{
		TransactionHash: txHash.Bytes(),
	}

	requestBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GetTransactionReceiptRequest: %w", err)
	}

	responseChan := make(chan interface{}, 1)
	c.pendingRequests.Store(id, responseChan)
	defer c.pendingRequests.Delete(id)

	// Tạo message với ID trong header
	msg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: cmdGetTransactionReceipt,
			ID:      id,
		},
		Body: requestBytes,
	})

	logger.Info("GetTransactionReceipt: Sending request with header ID: %s", id)

	if err := c.connection.SendMessage(msg); err != nil {
		return nil, fmt.Errorf("failed to send GetTransactionReceipt request: %w", err)
	}

	for {
		select {
		case res := <-responseChan:
			logger.Info("GetTransactionReceipt: Received response, id: %s", id)
			if response, ok := res.(*pb.GetTransactionReceiptResponse); ok {
				if response.Error != "" {
					return nil, fmt.Errorf("server error: %s", response.Error)
				}
				return response, nil
			}
			return nil, fmt.Errorf("invalid response type")
		case err := <-c.errorNotifyChan:
			return nil, fmt.Errorf("connection error: %w", err)
		case <-time.After(30 * time.Second):
			return nil, fmt.Errorf("timeout waiting for GetReceipt")
		case <-c.ctx.Done():
			return nil, fmt.Errorf("context cancelled")
		}
	}
}
