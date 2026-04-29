package listener

import (
	"crypto/sha256"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/contracts/cross_chain_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// ─────────────────────────────────────────────────────────────────────────────
// Submit lên chain qua WalletPool
// ─────────────────────────────────────────────────────────────────────────────

// calculateBatchId tính idBatch bằng cách sha256 nội dung của events
func (s *CrossChainScanner) calculateBatchId(events []cross_chain_contract.EmbassyEventInput) ([32]byte, error) {
	solEvents := make([]cross_chain_contract.EmbassyEventSolidity, len(events))
	for i, ev := range events {
		solEvents[i] = cross_chain_contract.ToSolEvent(ev)
	}
	batchMethod, ok := s.cfg.CrossChainAbi.Methods["batchSubmit"]
	if !ok {
		return [32]byte{}, fmt.Errorf("method batchSubmit not found")
	}
	packed, err := batchMethod.Inputs[:1].Pack(solEvents)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(packed), nil
}

// submitBatch lấy ví từ WalletPool và gọi cross_chain_contract.BatchSubmit (fire-and-forget).
// Caller lưu txHash → walletAddr vào txHashToWallet map để receiptWatcher gọi MarkReady.
func (s *CrossChainScanner) submitBatch(rc tcp_config.RemoteChain, events []cross_chain_contract.EmbassyEventInput, forceIndex int, blockNum uint64) (common.Hash, int, bool, error) {
	contractAddr := common.HexToAddress(s.cfg.CrossChainContract_)

	batchIdByte, err := s.calculateBatchId(events)
	if err != nil {
		return common.Hash{}, 0, false, fmt.Errorf("calculateBatchId err: %w", err)
	}
	batchIdHex := fmt.Sprintf("%x", batchIdByte[:4])

	targetIndex := forceIndex
	if targetIndex < 0 {
		batchIdBig := new(big.Int).SetBytes(batchIdByte[:])
		mod := big.NewInt(int64(len(s.localClients)))
		targetIndex = int(new(big.Int).Mod(batchIdBig, mod).Int64())
	}
	// Delegate quản lý node sống chết cho GetActiveClient
	selectedClient, actualIndex := s.GetActiveClient(targetIndex)

	wallet := s.walletPool.Acquire()

	// --- Check Nonce ---
	expectedNonce := wallet.ExpectedNonce

	nodeNonce, nonceErr := selectedClient.ChainGetNonce(wallet.Address())
	if nonceErr != nil {
		// Node did not respond to GetNonce -> node is likely dead/unreachable
		s.walletPool.MarkReady(wallet.Address())
		return common.Hash{}, actualIndex, false, fmt.Errorf("failed to fetch nonce from Node[%d] (%s): %w. Node might be dead", actualIndex, selectedClient.GetNodeAddr(), nonceErr)
	}

	if nodeNonce != expectedNonce {
		// Node is lagging or hasn't updated its nonce yet!
		s.walletPool.MarkReady(wallet.Address())
		return common.Hash{}, actualIndex, false, fmt.Errorf("node %d (%s) is lagging for wallet %s: expected nonce >= %d, got %d", actualIndex, selectedClient.GetNodeAddr(), wallet.Address().Hex(), expectedNonce, nodeNonce)
	}

	logger.Info("📡 [submitBatch] Gửi lên Node[%d] → %s (wallet: %s, checking nonce...)", actualIndex, selectedClient.GetNodeAddr(), wallet.Address().Hex()[:10])

	txHash, isQuorum, err := cross_chain_contract.BatchSubmit(
		selectedClient,
		s.cfg,
		contractAddr,
		wallet.Address(),
		events,
		s.cfg.BlsPublicKey(), // BLS public key của embassy → chain verify O(1)
		nil,
		batchIdHex,
	)
	if err != nil {
		s.walletPool.MarkReady(wallet.Address())
		return txHash, actualIndex, isQuorum, fmt.Errorf("submitBatch using client[%d]: %w", actualIndex, err)
	}

	// Khi BatchSubmit thành công, gán nonce + 1
	wallet.ExpectedNonce = nodeNonce + 1
	s.txHashToWallet.Store(txHash, TrackedTx{Wallet: wallet.Address(), IsQuorum: isQuorum, TargetIndex: actualIndex, RemoteNationId: rc.NationId, RemoteBlock: blockNum})
	return txHash, actualIndex, isQuorum, nil
}
