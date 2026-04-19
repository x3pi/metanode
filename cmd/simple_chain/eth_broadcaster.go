// eth_broadcaster.go (hoặc tương tự)
package main

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/meta-node-blockchain/meta-node/pkg/logger" // Import your logger
	// Import package mining nếu bạn muốn sử dụng interface TransactionBroadcaster từ đó
)

// EthTransactionBroadcaster implements mining.TransactionBroadcaster for Ethereum
type EthTransactionBroadcaster struct {
	client  *ethclient.Client
	chainID *big.Int // Chain ID của mạng Ethereum bạn đang kết nối
}

// NewEthTransactionBroadcaster tạo một instance mới của EthTransactionBroadcaster
func NewEthTransactionBroadcaster(rpcURL string) (*EthTransactionBroadcaster, error) {
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Ethereum client: %w", err)
	}

	chainID, err := client.ChainID(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get chain ID: %w", err)
	}

	return &EthTransactionBroadcaster{
		client:  client,
		chainID: chainID,
	}, nil
}

// SendRewardTransaction implements the mining.TransactionBroadcaster interface to send actual ETH.
func (etb *EthTransactionBroadcaster) SendRewardTransaction(
	from common.Address,
	to common.Address,
	amount float64,
	privateKey string,
) (common.Hash, error) {
	ctx := context.Background()

	// 1. Chuyển đổi private key từ hex string sang ECDSA private key
	privKeyEth, err := crypto.HexToECDSA(strings.TrimPrefix(privateKey, "0x"))
	if err != nil {
		return common.Hash{}, fmt.Errorf("invalid private key: %w", err)
	}

	// Xác nhận địa chỉ của private key
	publicKey := privKeyEth.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return common.Hash{}, errors.New("cannot assert public key to *ecdsa.PublicKey")
	}
	senderAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	if senderAddress != from {
		return common.Hash{}, fmt.Errorf("provided private key does not match 'from' address: %s != %s", senderAddress.Hex(), from.Hex())
	}

	// 2. Lấy nonce hiện tại của tài khoản gửi
	nonce, err := etb.client.PendingNonceAt(ctx, senderAddress)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to get account nonce: %w", err)
	}

	// 3. Chuyển đổi số tiền từ float64 sang *big.Int (Wei)
	// Sử dụng big.Float để tránh lỗi làm tròn của float64
	// 1 ETH = 10^18 Wei
	amountBigFloat := new(big.Float).SetFloat64(amount)
	weiFactor := new(big.Float).SetInt(big.NewInt(1e18))
	amountInWeiBigFloat := new(big.Float).Mul(amountBigFloat, weiFactor)

	amountInWei := new(big.Int)
	amountInWeiBigFloat.Int(amountInWei) // Convert to big.Int

	// 4. Lấy Gas Price gợi ý từ mạng
	gasPrice, err := etb.client.SuggestGasPrice(ctx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to suggest gas price: %w", err)
	}

	// 5. Ước tính Gas Limit (có thể là một giá trị cố định cho chuyển ETH đơn giản: 21000)
	// Hoặc ước tính tự động (nếu có data hoặc phức tạp hơn)
	gasLimit := uint64(21000) // Fixed gas limit for simple ETH transfer

	// 6. Tạo giao dịch
	// params: nonce, toAddress, amount, gasLimit, gasPrice, data (nil for simple transfer)
	tx := types.NewTransaction(nonce, to, amountInWei, gasLimit, gasPrice, nil)

	// 7. Ký giao dịch
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(etb.chainID), privKeyEth)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// 8. Gửi giao dịch
	err = etb.client.SendTransaction(ctx, signedTx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to send transaction: %w", err)
	}

	logger.Info("Transaction sent successfully! Tx Hash: %s", signedTx.Hash().Hex())
	return signedTx.Hash(), nil
}

// Close đóng kết nối RPC
func (etb *EthTransactionBroadcaster) Close() {
	if etb.client != nil {
		etb.client.Close()
	}
}
