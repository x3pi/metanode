package controllers

import (
	"io/ioutil"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/client_context"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/command"
	client_types "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/types"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"

	"fmt" // For formatted error messages

	"google.golang.org/protobuf/proto" // For marshaling protobuf messages
)

type TransactionController struct {
	clientContext *client_context.ClientContext
}

func NewTransactionController(
	clientContext *client_context.ClientContext,
) client_types.TransactionController {
	return &TransactionController{
		clientContext: clientContext,
	}
}

func (tc *TransactionController) SendTransaction(
	fromAddress common.Address,
	toAddress common.Address,
	pendingUse *big.Int,
	amount *big.Int,
	maxGas uint64,
	maxGasFee uint64,
	maxTimeUse uint64,
	data []byte,
	relatedAddress [][]byte,
	lastDeviceKey common.Hash,
	newDeviceKey common.Hash,
	nonce uint64,
	chainId uint64,
) (types.Transaction, error) {
	transaction := transaction.NewTransaction(
		fromAddress,
		toAddress,
		amount,
		maxGas,
		maxGasFee,
		maxTimeUse,
		data,
		relatedAddress,
		lastDeviceKey,
		newDeviceKey,
		nonce,
		chainId,
	)
	transaction.SetSign(tc.clientContext.KeyPair.PrivateKey())

	fmt.Printf(" [DEBUG] tx_sender Hash(): %x\n", transaction.Hash().Bytes())
bTransaction, err := transaction.Marshal()
	if err != nil {
		return nil, err
	}
	parentConnection := tc.clientContext.ConnectionsManager.ParentConnection()
	err = tc.clientContext.MessageSender.SendBytes(
		parentConnection,
		command.SendTransaction,
		bTransaction,
	)
	return transaction, err

}

func (tc *TransactionController) ReadTransaction(
	fromAddress common.Address,
	toAddress common.Address,
	pendingUse *big.Int,
	amount *big.Int,
	maxGas uint64,
	maxGasFee uint64,
	maxTimeUse uint64,
	data []byte,
	relatedAddress [][]byte,
	lastDeviceKey common.Hash,
	newDeviceKey common.Hash,
	nonce uint64,
	chainId uint64,
) (types.Transaction, error) {
	transaction := transaction.NewTransaction(
		fromAddress,
		toAddress,
		amount,
		maxGas,
		maxGasFee,
		maxTimeUse,
		data,
		relatedAddress,
		lastDeviceKey,
		newDeviceKey,
		nonce,
		chainId,
	)
	transaction.SetSign(tc.clientContext.KeyPair.PrivateKey())
	logger.Error("_ReadTransaction: %v", transaction)
	fmt.Printf(" [DEBUG] tx_sender Hash(): %x\n", transaction.Hash().Bytes())
bTransaction, err := transaction.Marshal()
	if err != nil {
		return nil, err
	}
	parentConnection := tc.clientContext.ConnectionsManager.ParentConnection()
	err = tc.clientContext.MessageSender.SendBytes(
		parentConnection,
		command.ReadTransaction,
		bTransaction,
	)
	return transaction, err
}

func (tc *TransactionController) SendTransactions(
	transactions []types.Transaction,
) error {

	bTransaction, err := transaction.MarshalTransactions(transactions)
	if err != nil {
		return err
	}
	parentConnection := tc.clientContext.ConnectionsManager.ParentConnection()
	err = tc.clientContext.MessageSender.SendBytes(
		parentConnection,
		command.SendTransactions,
		bTransaction,
	)
	return err
}

func (tc *TransactionController) SendTransactionWithDeviceKey(
	fromAddress common.Address,
	toAddress common.Address,
	pendingUse *big.Int,
	amount *big.Int,
	maxGas uint64,
	maxGasFee uint64,
	maxTimeUse uint64,
	data []byte,
	relatedAddress [][]byte,
	lastDeviceKey common.Hash,
	newDeviceKey common.Hash,
	nonce uint64,
	deviceKey []byte,
	chainId uint64,
) (types.Transaction, error) {
	transaction := transaction.NewTransaction(
		fromAddress,
		toAddress,
		amount,
		maxGas,
		maxGasFee,
		maxTimeUse,
		data,
		relatedAddress,
		lastDeviceKey,
		newDeviceKey,
		nonce,
		chainId,
	)
	transaction.SetSign(tc.clientContext.KeyPair.PrivateKey())
	logger.Info(transaction)

	// Create TransactionWithDeviceKey
	transactionWithDeviceKey := &pb.TransactionWithDeviceKey{
		Transaction: transaction.Proto().(*pb.Transaction),
		DeviceKey:   deviceKey,
	}

	// Serialize to bytes
	bTransactionWithDeviceKey, err := proto.Marshal(transactionWithDeviceKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal TransactionWithDeviceKey: %w", err)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal TransactionWithDeviceKey: %w", err)
	}
	parentConnection := tc.clientContext.ConnectionsManager.ParentConnection()
	err = tc.clientContext.MessageSender.SendBytes(
		parentConnection,
		command.SendTransactionWithDeviceKey,
		bTransactionWithDeviceKey,
	)
	return transaction, err
}

func (tc *TransactionController) SaveTransactionWithDeviceKeyToFile(
	fromAddress common.Address,
	toAddress common.Address,
	pendingUse *big.Int,
	amount *big.Int,
	maxGas uint64,
	maxGasFee uint64,
	maxTimeUse uint64,
	data []byte,
	relatedAddress [][]byte,
	lastDeviceKey common.Hash,
	newDeviceKey common.Hash,
	nonce uint64,
	deviceKey []byte,
	chainId uint64,
) error {
	transaction := transaction.NewTransaction(
		fromAddress,
		toAddress,
		amount,
		maxGas,
		maxGasFee,
		maxTimeUse,
		data,
		relatedAddress,
		lastDeviceKey,
		newDeviceKey,
		nonce,
		chainId,
	)
	transaction.SetSign(tc.clientContext.KeyPair.PrivateKey())
	logger.Info(transaction)

	// Tạo TransactionWithDeviceKey
	transactionWithDeviceKey := &pb.TransactionWithDeviceKey{
		Transaction: transaction.Proto().(*pb.Transaction),
		DeviceKey:   deviceKey,
	}

	// Serialize to bytes
	bTransactionWithDeviceKey, err := proto.Marshal(transactionWithDeviceKey)
	if err != nil {
		return fmt.Errorf("failed to marshal TransactionWithDeviceKey: %w", err)
	}

	// Đảm bảo thư mục 'txs' tồn tại
	txsDir := "txs" // Có thể cần phải làm cho nó trở thành một đường dẫn tuyệt đối nếu cần
	err = os.MkdirAll(txsDir, 0755)
	if err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Tạo đường dẫn tệp
	filePath := filepath.Join(txsDir, transaction.Hash().String())

	// Lưu vào tệp
	err = os.WriteFile(filePath, bTransactionWithDeviceKey, 0644)
	if err != nil {
		return fmt.Errorf("failed to write transaction to file: %w", err)
	}
	logger.Info("Save done: %v ", filePath)

	return nil
}

// LoadTransactionWithDeviceKeyFromFile tải giao dịch và device key từ một tệp.
func (tc *TransactionController) LoadTransactionWithDeviceKeyFromFile(
	filePath string, // Đường dẫn tệp để tải giao dịch
) (types.Transaction, []byte, error) {
	// Đọc từ tệp
	bTransactionWithDeviceKey, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read transaction from file: %w", err)
	}

	// Giải mã từ bytes
	transactionWithDeviceKey := &pb.TransactionWithDeviceKey{}
	err = proto.Unmarshal(bTransactionWithDeviceKey, transactionWithDeviceKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal TransactionWithDeviceKey: %w", err)
	}

	transaction := transaction.TransactionFromProto(transactionWithDeviceKey.Transaction)

	return transaction, transactionWithDeviceKey.DeviceKey, nil
}

// SendLoadedTransactionWithDeviceKeyFromFile tải giao dịch từ tệp và gửi nó.
func (tc *TransactionController) LoadedTransactionWithDeviceKeyFromFile(
	filePath string, // Đường dẫn tệp để tải giao dịch
) ([]byte, error) {
	transaction, deviceKey, err := tc.LoadTransactionWithDeviceKeyFromFile(filePath)
	if err != nil {
		return nil, err
	}

	// Tạo TransactionWithDeviceKey
	transactionWithDeviceKey := &pb.TransactionWithDeviceKey{
		Transaction: transaction.Proto().(*pb.Transaction),
		DeviceKey:   deviceKey,
	}

	// Serialize to bytes
	bTransactionWithDeviceKey, err := proto.Marshal(transactionWithDeviceKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal TransactionWithDeviceKey: %w", err)
	}

	return bTransactionWithDeviceKey, err
}

func (tc *TransactionController) SendAllTransactionsWidthDeviceKeyInDirectory(
	directoryPath string, // Đường dẫn đến thư mục chứa các tệp giao dịch
) error {

	parentConnection := tc.clientContext.ConnectionsManager.ParentConnection()
	// Kiểm tra nếu kết nối cha là nil

	clientConn := network.NewConnection(common.Address{}, "CLIENT_CONN", network.DefaultConfig())
	clientConn.SetRealConnAddr(parentConnection.RemoteAddr())

	if err := clientConn.Connect(); err != nil {
		log.Fatalf("Client không thể kết nối: %v", err)
	}

	defer clientConn.Disconnect()
	log.Printf("Client đã kết nối thành công!")

	files, err := ioutil.ReadDir(directoryPath)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}

	// Thu thập lỗi
	var combinedError error
	errorCount := 0

	// Duyệt qua từng tệp và xử lý tuần tự
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		filePath := filepath.Join(directoryPath, file.Name())
		log.Printf("Processing transaction from file %s", filePath)

		byteT, err := tc.LoadedTransactionWithDeviceKeyFromFile(filePath)
		if err != nil {
			log.Printf("Failed to LoadedTransactionWithDeviceKeyFromFile transaction from file %s: %v", filePath, err)
		}

		err = tc.clientContext.MessageSender.SendBytes(
			parentConnection,
			command.SendTransactionWithDeviceKey,
			byteT,
		)
		if err != nil {
			log.Printf("Failed to SendBytes transaction from file %s: %v", filePath, err)
		}
	}

	successCount := len(files) - errorCount
	log.Printf("--- Sending Complete ---")
	log.Printf("Total transactions to send: %d", len(files))
	log.Printf("Successfully sent: %d", successCount)
	log.Printf("Failed: %d", errorCount)
	log.Printf("------------------------")

	return combinedError
}

func (tc *TransactionController) SendAllTransactionsInDirectory(
	directoryPath string, // Đường dẫn đến thư mục chứa các tệp giao dịch
) error {

	parentConnection := tc.clientContext.ConnectionsManager.ParentConnection()
	// Kiểm tra nếu kết nối cha là nil

	clientConn := network.NewConnection(common.Address{}, "CLIENT_CONN", network.DefaultConfig())
	clientConn.SetRealConnAddr(parentConnection.RemoteAddr())

	if err := clientConn.Connect(); err != nil {
		log.Fatalf("Client không thể kết nối: %v", err)
	}

	defer clientConn.Disconnect()
	log.Printf("Client đã kết nối thành công!")

	files, err := ioutil.ReadDir(directoryPath)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}

	// Thu thập lỗi
	var combinedError error
	errorCount := 0
	var txs []types.Transaction
	// Duyệt qua từng tệp và xử lý tuần tự
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		filePath := filepath.Join(directoryPath, file.Name())
		log.Printf("Processing transaction from file %s", filePath)

		tx, _, err := tc.LoadTransactionWithDeviceKeyFromFile(filePath)
		if err != nil {
			log.Printf("Failed to LoadTransactionWithDeviceKeyFromFile transaction from file %s: %v", filePath, err)
			continue
		}

		// SỬA LỖI Ở ĐÂY: Gán kết quả của append vào txs
		txs = append(txs, tx)
	}

	bTransaction, err := transaction.MarshalTransactions(txs)
	if err != nil {
		return err
	}
	err = tc.clientContext.MessageSender.SendBytes(
		parentConnection,
		command.SendTransactions,
		bTransaction,
	)
	if err != nil {
		log.Printf("Failed to SendBytes transaction from file : %v", err)
	}
	successCount := len(files) - errorCount
	log.Printf("--- Sending Complete ---")
	log.Printf("Total transactions to send: %d", len(files))
	log.Printf("Successfully sent: %d", successCount)
	log.Printf("Failed: %d", errorCount)
	log.Printf("------------------------")

	return combinedError
}

// RunResult lưu trữ kết quả của một lần chạy benchmark
type RunResult struct {
	RunNumber         int
	Duration          time.Duration
	MessagesPerSecond float64
	SentCount         int64
	ReceivedCount     int64
	LostCount         int64
}

//-----------------------//

func (tc *TransactionController) SendNewTransactionWithDeviceKey(
	transaction types.Transaction,
	deviceKey []byte,
) (types.Transaction, error) {

	transaction.SetSign(tc.clientContext.KeyPair.PrivateKey())

	// Create TransactionWithDeviceKey
	transactionWithDeviceKey := &pb.TransactionWithDeviceKey{
		Transaction: transaction.Proto().(*pb.Transaction),
		DeviceKey:   deviceKey,
	}

	// Serialize to bytes
	bTransactionWithDeviceKey, err := proto.Marshal(transactionWithDeviceKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal TransactionWithDeviceKey: %w", err)
	}
	parentConnection := tc.clientContext.ConnectionsManager.ParentConnection()
	err = tc.clientContext.MessageSender.SendBytes(
		parentConnection,
		command.SendTransactionWithDeviceKey,
		bTransactionWithDeviceKey,
	)
	return transaction, err
}
