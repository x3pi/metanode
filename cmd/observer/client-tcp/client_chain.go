package client

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"sync"
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
	"google.golang.org/protobuf/proto"
)

// ===================== WalletPool =====================
// Pool ví con cho embassy dùng gửi TX song song mà không đợi receipt.
// Mỗi ví giữ nonce riêng. Khi receipt về → đánh dấu ví sẵn sàng lại.

type walletEntry struct {
	address common.Address
	ready   atomic.Bool // true = không có TX đang pending
}

// WalletPool quản lý N ví con để gửi TX song song
type WalletPool struct {
	wallets []*walletEntry
}

// NewWalletPool tạo pool với danh sách address, tất cả ví khởi tạo ready=true
func NewWalletPool(addresses []common.Address) *WalletPool {
	wallets := make([]*walletEntry, len(addresses))
	for i, addr := range addresses {
		e := &walletEntry{address: addr}
		e.ready.Store(true)
		wallets[i] = e
	}
	return &WalletPool{wallets: wallets}
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

// Acquire tìm ví rảnh (ready=true).
// Nếu tất cả ví đều busy (đang chờ receipt), block cho đến khi có ví rảnh.
func (p *WalletPool) Acquire() *walletEntry {
	for {
		for _, w := range p.wallets {
			if w.ready.CompareAndSwap(true, false) {
				return w
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// MarkReady đánh dấu ví sẵn sàng sau khi receipt về
func (p *WalletPool) MarkReady(address common.Address) {
	for _, w := range p.wallets {
		if w.address == address {
			w.ready.Store(true)
			return
		}
	}
}

// Address trả về địa chỉ của wallet entry (exported để external packages dùng được)
func (w *walletEntry) Address() common.Address {
	return w.address
}

// ===================== Chain-Direct Methods =====================
// Gửi thẳng lên chain, dùng header ID matching, không qua RPC proxy

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

// ChainGetTransactionReceipt lấy receipt trực tiếp từ chain theo txHash
func (client *Client) ChainGetTransactionReceipt(txHash common.Hash) (*pb.GetTransactionReceiptResponse, error) {
	req := &pb.GetTransactionReceiptRequest{
		TransactionHash: txHash.Bytes(),
	}
	requestBytes, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GetTransactionReceiptRequest: %w", err)
	}

	respBytes, err := client.sendChainRequest(command.GetTransactionReceipt, requestBytes, 60*time.Second)
	if err != nil {
		return nil, err
	}

	resp := &pb.GetTransactionReceiptResponse{}
	if err := proto.Unmarshal(respBytes, resp); err != nil {
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

	resp, err := client.sendChainRequest(command.GetLogs, requestBytes, 60*time.Second)
	if err != nil {
		return nil, err
	}
	response := &pb.GetLogsResponse{}
	if err := proto.Unmarshal(resp, response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal GetLogsResponse: %w", err)
	}
	if response.Error != "" {
		return nil, fmt.Errorf("server error: %s", response.Error)
	}
	logger.Info("✅ ChainGetLogs: %d logs found", len(response.Logs))
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
	nonceResp, err := client.sendChainRequest(command.GetNonce, fromAddress.Bytes(), 10*time.Second)
	if err != nil {
		return common.Hash{}, 0, fmt.Errorf("get nonce failed: %w", err)
	}
	var nonce uint64
	if len(nonceResp) >= 8 {
		nonce = binary.BigEndian.Uint64(nonceResp)
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
		return common.Hash{}, 0, fmt.Errorf("marshal tx failed: %w", err)
	}

	// Gửi qua sendChainRequest kèm header ID → nhận TransactionSuccess / TransactionError
	resp, err := client.sendChainRequest(command.SendTransaction, bTransaction, 120*time.Second)
	if err != nil {
		return common.Hash{}, 0, fmt.Errorf("send tx failed: %w", err)
	}
	txErr := &pb.TransactionHashWithError{}
	if parseErr := proto.Unmarshal(resp, txErr); parseErr == nil {
		return common.Hash{}, 0, fmt.Errorf("tx rejected (code=%d): %s", txErr.Code, txErr.Description)
	}
	logger.Info("✅ SendTransactionFromWallet: %v", tx)
	return tx.Hash(), nonce, nil
}

// StartWalletPoolReceiptWatcher theo dõi các TX do WalletPool gửi bằng cách poll
// GetTransactionReceipt — vì các ví trong pool là derived addresses (không phải embassy),
// receipt không trở về qua receiptChan mà phải chủ động hỏi chain.
//
// Cách hoạt động:
//   - Mỗi giây scan txHashToWallet map, spawn goroutine poll cho từng txHash chưa tracked.
//   - Goroutine poll gọi ChainGetTransactionReceipt cho đến khi nhận được (infinite retry).
//   - Khi receipt về → pool.MarkReady(walletAddr), xóa khỏi map,
//     và parse BlockNumber từ RpcReceipt → gửi vào localBlockCh.
func (client *Client) StartWalletPoolReceiptWatcher(pool *WalletPool, txHashToWallet *sync.Map, localBlockCh chan<- uint64) {
	const (
		scanInterval   = 400 * time.Millisecond // tần suất scan map tìm TX mới
		pollInterval   = 50 * time.Millisecond  // tần suất poll receipt cho mỗi TX
		maxPollWorkers = 8                      // giới hạn goroutines poll receipt đồng thời
	)

	// semaphore: giới hạn tối đa maxPollWorkers goroutines poll cùng lúc
	sem := make(chan struct{}, maxPollWorkers)
	// tracking: các txHash đang được poll để không spawn duplicate goroutine
	var tracking sync.Map
	go func() {
		ticker := time.NewTicker(scanInterval)
		defer ticker.Stop()
		for range ticker.C {
			txHashToWallet.Range(func(key, val any) bool {
				txHash := key.(common.Hash)
				walletAddr := val.(common.Address)
				// chỉ spawn 1 goroutine poll cho mỗi txHash
				if _, already := tracking.LoadOrStore(txHash, struct{}{}); already {
					return true
				}
				sem <- struct{}{}
				go func(txHash common.Hash, walletAddr common.Address) {
					defer func() { <-sem }() // trả lại slot khi xong
					startTime := time.Now()
					for {
						// Nếu quá 30 giây mà vẫn chưa có receipt thì xoá khỏi hàng đợi
						if time.Since(startTime) > 30*time.Second {
							logger.Error("❌ [WalletPool] Timeout receipt for txHash=%s after 30s! Marking tx as SUCCESS", txHash.Hex())
							pool.MarkReady(walletAddr)
							txHashToWallet.Delete(txHash)
							tracking.Delete(txHash)
							return
						}

						resp, err := client.ChainGetTransactionReceipt(txHash)
						if err != nil {
							logger.Error("❌ [WalletPool] ChainGetTransactionReceipt failed for txHash=%s: %v", txHash.Hex(), err)
						} else if resp != nil && resp.Receipt != nil {
							// Thực sự đã có Receipt (đã mint block) thì mới báo Ready
							pool.MarkReady(walletAddr)
							txHashToWallet.Delete(txHash)
							tracking.Delete(txHash)
							// Parse receipt response → lấy BlockNumber trực tiếp
							if localBlockCh != nil {
								blockNumHex := resp.Receipt.GetBlockNumber()
								if blockNumHex != "" {
									var bn uint64
									if _, scanErr := fmt.Sscanf(blockNumHex, "0x%x", &bn); scanErr == nil && bn > 0 {
										select {
										case localBlockCh <- bn:
										default:
										}
									}
								}
							}

							statusStr := "SUCCESS"
							if resp.Receipt.Status != pb.RECEIPT_STATUS_RETURNED && resp.Receipt.Status != pb.RECEIPT_STATUS_HALTED {
								statusStr = fmt.Sprintf("REVERTED (status=%v)", resp.Receipt.Status)
								logger.Info("Error receipt: %v", resp.GetReceipt())
							}
							logger.Info("✅ [WalletPool] Receipt confirmed txHash=%s → wallet %s ready | Status: %s",
								txHash.Hex(), walletAddr.Hex(), statusStr)
							return
						}
						// Nếu request lỗi, hoặc response trả về chưa có Receipt -> tiếp tục poll
						// logger.Info("⏳ [WalletPool] Polling receipt txHash=%s", txHash.Hex())
						time.Sleep(pollInterval)
					}
				}(txHash, walletAddr)
				return true
			})
		}
	}()
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
