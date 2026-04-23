package client

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"

	"github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/command"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

// ===================== WalletPool =====================
// Pool ví con cho embassy dùng gửi TX song song mà không đợi receipt.
// Mỗi ví giữ nonce riêng. Khi receipt về → đánh dấu ví sẵn sàng lại.

type WalletEntry struct {
	address       common.Address
	ready         atomic.Bool // true = không có TX đang pending
	ExpectedNonce uint64
}

// WalletPool quản lý N ví con để gửi TX song song
type WalletPool struct {
	Wallets []*WalletEntry
	nextIdx uint32
}

// NewWalletPool tạo pool với danh sách address, tất cả ví khởi tạo ready=true
func NewWalletPool(addresses []common.Address) *WalletPool {
	wallets := make([]*WalletEntry, len(addresses))
	for i, addr := range addresses {
		e := &WalletEntry{address: addr}
		e.ready.Store(true)
		wallets[i] = e
	}
	return &WalletPool{Wallets: wallets}
}

// DeriveEmbassyWalletAddresses tạo n địa chỉ ví giả deterministic từ embassyAddr + index.
// Quy tắc: seed = "embassy:" || embassyAddr(20 bytes) || index(8 bytes big-endian)
//
//	address = keccak256(seed)[12:]   → 20 bytes cuối (chuẩn Ethereum)
//
// Mỗi embassy node có embassyAddr riêng (derive từ BLS private key riêng biệt)
// → virtual wallet pool riêng biệt → không bao giờ conflict nonce giữa các embassy.
func DeriveEmbassyWalletAddresses(embassyAddr common.Address, n int) []common.Address {
	prefix := []byte("embassy:")
	addrs := make([]common.Address, n)
	buf := make([]byte, 8) // index(8)
	for i := 0; i < n; i++ {
		binary.BigEndian.PutUint64(buf[0:8], uint64(i))
		seed := append(prefix, embassyAddr.Bytes()...)
		seed = append(seed, buf...)
		hash := crypto.Keccak256(seed)
		var addr common.Address
		copy(addr[:], hash[12:])
		addrs[i] = addr
	}
	return addrs
}

// Acquire tìm ví rảnh (ready=true) theo cơ chế Round-Robin để phân tải đều.
// Tránh việc 1 ví đứng đầu mảng bị lặp lại liên tục khi xảy ra rủi ro lỗi nonce.
func (p *WalletPool) Acquire() *WalletEntry {
	total := uint32(len(p.Wallets))
	if total == 0 {
		return nil
	}
	for {
		for i := uint32(0); i < total; i++ {
			// Tăng nextIdx an toàn cho goroutine
			idx := atomic.AddUint32(&p.nextIdx, 1) % total
			w := p.Wallets[idx]
			if w.ready.CompareAndSwap(true, false) {
				return w
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// MarkReady đánh dấu ví sẵn sàng sau khi receipt về
func (p *WalletPool) MarkReady(address common.Address) {
	for _, w := range p.Wallets {
		if w.address == address {
			w.ready.Store(true)
			return
		}
	}
}

// Address trả về địa chỉ của wallet entry (exported để external packages dùng được)
func (w *WalletEntry) Address() common.Address {
	return w.address
}

// ===================== Chain-Direct Methods =====================
// Gửi thẳng lên chain, dùng header ID matching, không qua RPC proxy

// sendChainRequest gửi command trực tiếp lên chain và đợi response theo header ID
func (client *Client) sendChainRequest(cmd string, body []byte, timeout time.Duration) (network.Message, error) {
	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	if parentConn == nil || !parentConn.IsConnect() {
		return nil, fmt.Errorf("parent connection not available")
	}

	id := uuid.New().String()
	respCh := make(chan network.Message, 1)

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
	respMsg, err := client.sendChainRequest(command.GetChainId, nil, 60*time.Second)
	if err != nil {
		return 0, err
	}
	resp := respMsg.Body()
	if len(resp) < 8 {
		return 0, fmt.Errorf("invalid chain id response: %d bytes", len(resp))
	}
	chainId := binary.BigEndian.Uint64(resp)
	logger.Info("✅ ChainGetChainId: %d", chainId)
	return chainId, nil
}

// ChainGetNonce lấy nonce trực tiếp từ chain (dùng lệnh GetNonce)
func (client *Client) ChainGetNonce(address common.Address) (uint64, error) {
	respMsg, err := client.sendChainRequest(command.GetNonce, address.Bytes(), 10*time.Second)
	if err != nil {
		return 0, err
	}
	resp := respMsg.Body()
	var nonce uint64
	if len(resp) >= 8 {
		nonce = binary.BigEndian.Uint64(resp)
	}
	return nonce, nil
}

// ChainGetBlockNumber lấy block number trực tiếp từ chain (raw uint64)
func (client *Client) ChainGetBlockNumber() (uint64, error) {
	respMsg, err := client.sendChainRequest(command.GetBlockNumber, nil, 60*time.Second)
	if err != nil {
		return 0, err
	}
	resp := respMsg.Body()
	if len(resp) < 8 {
		return 0, fmt.Errorf("invalid block number response: %d bytes", len(resp))
	}
	bn := binary.BigEndian.Uint64(resp)
	return bn, nil
}

// ChainGetTransactionReceipt lấy receipt trực tiếp từ chain theo txHash
func (client *Client) ChainGetTransactionReceipt(txHash common.Hash) (*pb.GetTransactionReceiptResponse, error) {
	req := &pb.GetTransactionReceiptRequest{
		TransactionHash: txHash.Bytes(),
	}
	requestBytes, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GetTransactionReceiptRequest: %w", err)
	}

	respMsg, err := client.sendChainRequest(command.GetTransactionReceipt, requestBytes, 60*time.Second)
	if err != nil {
		return nil, err
	}

	resp := &pb.GetTransactionReceiptResponse{}
	if err := proto.Unmarshal(respMsg.Body(), resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal GetTransactionReceiptResponse: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("server error: %s", resp.Error)
	}
	return resp, nil
}

// ChainGetLogs lấy logs từ chain theo filter criteria
func (client *Client) ChainGetLogs(
	blockHash []byte,
	fromBlock string,
	toBlock string,
	addresses []common.Address,
	topics [][]common.Hash,
) (*pb.GetLogsResponse, error) {
	logger.Info(
		"[Client][GetLogs] node=%s blockHash=%d from=%s to=%s addresses=%d topics=%d",
		client.GetNodeAddr(),
		len(blockHash),
		fromBlock,
		toBlock,
		len(addresses),
		len(topics),
	)
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
				request.Topics[i] = &pb.TopicFilter{Hashes: hashes}
			}
		}
	}

	requestBytes, err := proto.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GetLogsRequest: %w", err)
	}

	respMsg, err := client.sendChainRequest(command.GetLogs, requestBytes, 60*time.Second)
	if err != nil {
		return nil, err
	}
	response := &pb.GetLogsResponse{}
	if err := proto.Unmarshal(respMsg.Body(), response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal GetLogsResponse: %w", err)
	}
	if response.Error != "" {
		return nil, fmt.Errorf("server error: %s", response.Error)
	}
	logger.Info("[Client][GetLogs] node=%s resultLogs=%d", client.GetNodeAddr(), len(response.Logs))
	return response, nil
}

// SendTransactionFromWallet gửi TX với fromAddress tùy chọn.
// Gửi kèm header ID → nhận response (TransactionSuccess / TransactionError) qua pendingChainRequests.
// Không ảnh hưởng transactionErrorChan shared channel của SendTransactionWithDeviceKey.
func (client *Client) SendTransactionFromWallet(
	fromAddress common.Address,
	toAddress common.Address,
	amount *big.Int,
	data []byte,
	relatedAddress []common.Address,
	maxGas uint64,
	maxGasPrice uint64,
	maxTimeUse uint64,
) (common.Hash, uint64, error) {
	if client.clientContext == nil || client.clientContext.ConnectionsManager == nil {
		return common.Hash{}, 0, fmt.Errorf("client not ready")
	}

	parentConn := client.clientContext.ConnectionsManager.ParentConnection()
	if parentConn == nil || !parentConn.IsConnect() {
		if err := client.ReconnectToParent(); err != nil {
			return common.Hash{}, 0, err
		}
		parentConn = client.clientContext.ConnectionsManager.ParentConnection()
	}

	// Lấy nonce từ chain (dùng ID matching, không dùng shared nonce channel)
	nonce, err := client.ChainGetNonce(fromAddress)
	if err != nil {
		return common.Hash{}, 0, fmt.Errorf("get nonce failed: %w", err)
	}
	bRelatedAddresses := make([][]byte, len(relatedAddress))
	for i, v := range relatedAddress {
		bRelatedAddresses[i] = v.Bytes()
	}

	// Build TX trực tiếp
	tx := mt_transaction.NewTransaction(
		fromAddress,
		toAddress,
		amount,
		maxGas,
		maxGasPrice,
		maxTimeUse,
		data,
		bRelatedAddresses,
		common.Hash{}, // lastDeviceKey
		common.Hash{}, // newDeviceKey
		nonce,
		client.clientContext.Config.ChainId,
	)
	tx.SetSign(client.clientContext.KeyPair.PrivateKey())

	bTransaction, err := tx.Marshal()
	if err != nil {
		return tx.Hash(), nonce, fmt.Errorf("marshal tx failed: %w", err)
	}

	// Gửi qua sendChainRequest kèm header ID → nhận TransactionSuccess / TransactionError
	respMsg, err := client.sendChainRequest(command.SendTransaction, bTransaction, 120*time.Second)
	if err != nil {
		return tx.Hash(), nonce, fmt.Errorf("send tx failed: %w", err)
	}

	if respMsg.Command() == command.TransactionError {
		txErr := &pb.TransactionHashWithError{}
		if parseErr := proto.Unmarshal(respMsg.Body(), txErr); parseErr == nil {
			desc := txErr.Description
			logger.Error("❌ SendTransactionFromWallet: tx rejected code=%d desc=%q txHash=%s nonce=%d from=%s to=%s",
				txErr.Code, desc, tx.Hash().Hex(), nonce, fromAddress.Hex(), toAddress.Hex())
			return tx.Hash(), nonce, fmt.Errorf("tx rejected (code=%d): %s", txErr.Code, desc)
		} else {
			logger.Warn("❌ SendTransactionFromWallet: received TransactionError but failed to parse: %v", parseErr)
			return tx.Hash(), nonce, fmt.Errorf("tx rejected but failed to parse error details")
		}
	}
	logger.Info("✅ SendTransactionFromWallet: txHash=%s nonce=%d from=%s", tx.Hash().Hex(), nonce, fromAddress.Hex())
	return tx.Hash(), nonce, nil
}

// parseBlockNumberFromReceiptResponse parse GetTransactionReceiptResponse proto,
// lấy RpcReceipt.BlockNumber (hex string "0x123") → uint64.
// Trả về 0 nếu parse lỗi hoặc receipt nil.
func parseBlockNumberFromReceiptResponse(resp *pb.GetTransactionReceiptResponse) uint64 {
	if resp == nil || resp.Receipt == nil {
		return 0
	}
	blockNumHex := resp.Receipt.GetBlockNumber()
	if blockNumHex == "" {
		return 0
	}
	// Parse hex string "0x1a3f" → uint64
	var bn uint64
	if _, err := fmt.Sscanf(blockNumHex, "0x%x", &bn); err != nil {
		return 0
	}
	return bn
}
