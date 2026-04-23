package client

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/tcp-rpc/client-tcp/command"
	"google.golang.org/protobuf/proto"
)

// GetNonce lấy nonce cho address.
// Nếu directClient được set → dùng ChainGetNonce (TCP direct, ID-matching, không tranh channel).
// Fallback → dùng RpcGetPendingNonce (RPC proxy).
func (c *Client) GetNonce(address common.Address) (uint64, error) {
	return c.ChainGetNonce(address)
}

// ===================== Chain-Direct Methods =====================
// G\u1eedi th\u1eb3ng l\u00ean chain, d\u00f9ng header ID matching, kh\u00f4ng qua RPC proxy

// sendChainRequest gửi command trực tiếp lên chain và đợi response theo header ID
func (client *Client) sendChainRequest(cmd string, body []byte, timeout time.Duration) ([]byte, error) {
	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	if parentConn == nil || !parentConn.IsConnect() {
		return nil, fmt.Errorf("parent connection not available")
	}

	id := uuid.New().String()
	respCh := make(chan []byte, 1)

	client.pendingChainRequests.Store(id, respCh)

	msg := p_network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: cmd,
			ID:      id,
		},
		Body: body,
	})

	if err := parentConn.SendMessage(msg); err != nil {
		client.pendingChainRequests.Delete(id)
		return nil, fmt.Errorf("failed to send %s: %w", cmd, err)
	}

	select {
	case resp := <-respCh:
		return resp, nil
	case <-time.After(timeout):
		client.pendingChainRequests.Delete(id)
		return nil, fmt.Errorf("timeout waiting for %s (id=%s)", cmd, id)
	}
}

// ChainGetChainId lấy chain ID trực tiếp từ chain (raw uint64)
func (client *Client) ChainGetChainId() (uint64, error) {
	resp, err := client.sendChainRequest(command.GetChainId, nil, 60*time.Second)
	if err != nil {
		return 0, err
	}
	if len(resp) < 8 {
		return 0, fmt.Errorf("invalid chain id response: %d bytes", len(resp))
	}
	chainId := binary.BigEndian.Uint64(resp)
	logger.Info("✅ ChainGetChainId: %d", chainId)
	return chainId, nil
}

// ChainGetBlockNumber lấy block number trực tiếp từ chain (raw uint64)
func (client *Client) ChainGetBlockNumber() (uint64, error) {
	resp, err := client.sendChainRequest(command.GetBlockNumber, nil, 60*time.Second)
	if err != nil {
		return 0, err
	}
	if len(resp) < 8 {
		return 0, fmt.Errorf("invalid block number response: %d bytes", len(resp))
	}
	bn := binary.BigEndian.Uint64(resp)
	logger.Info("✅ ChainGetBlockNumber: %d", bn)
	return bn, nil
}

// ChainGetNonce lấy nonce của address trực tiếp từ chain (không qua RPC, dùng ID-matching).
// Khác RpcGetPendingNonce (RPC proxy): method này gửi thẳng đến chain node  → nhanh hơn,
// không tranh chấp channel nonce shared.
func (client *Client) ChainGetNonce(address common.Address) (uint64, error) {
	resp, err := client.sendChainRequest(command.GetNonce, address.Bytes(), 10*time.Second)
	if err != nil {
		return 0, fmt.Errorf("ChainGetNonce: %w", err)
	}
	if len(resp) < 8 {
		return 0, fmt.Errorf("ChainGetNonce: invalid response length %d", len(resp))
	}
	nonce := binary.BigEndian.Uint64(resp)
	return nonce, nil
}

// ChainGetTransactionReceipt lấy receipt trực tiếp từ chain theo txHash
// Trả về raw response bytes — caller tự unmarshal nếu cần
func (client *Client) ChainGetTransactionReceipt(txHash common.Hash) ([]byte, error) {
	resp, err := client.sendChainRequest(command.GetTransactionReceipt, txHash.Bytes(), 60*time.Second)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ChainGetLogs lấy logs từ chain theo filter criteria
// Trả về *pb.GetLogsResponse đã parsed
func (client *Client) ChainGetLogs(
	blockHash []byte,
	fromBlock string,
	toBlock string,
	addresses []common.Address,
	topics [][]common.Hash,
) (*pb.GetLogsResponse, error) {
	// Build GetLogsRequest proto
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

	requestBytes, err := proto.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GetLogsRequest: %w", err)
	}

	resp, err := client.sendChainRequest(command.GetLogs, requestBytes, 60*time.Second)
	if err != nil {
		return nil, err
	}

	// Parse response
	response := &pb.GetLogsResponse{}
	if err := proto.Unmarshal(resp, response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal GetLogsResponse: %w", err)
	}

	if response.Error != "" {
		return nil, fmt.Errorf("server error: %s", response.Error)
	}

	return response, nil
}
