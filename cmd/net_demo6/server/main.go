package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
)

const (
	protocolVersion = "1.0.0"
	listenAddress   = "0.0.0.0:4202"
	// Private key này chỉ dùng cho mục đích khởi tạo server, không ảnh hưởng đến giao dịch
	hardcodedPrivateKeyHex = "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b"
)

// handleTransactionRequest là logic phía server để xử lý các giao dịch đến.
func handleTransactionRequest(request t_network.Request) error {
	tx := &transaction.Transaction{}
	if err := tx.Unmarshal(request.Message().Body()); err != nil {
		logger.Error("Server: Lỗi giải mã giao dịch", "error", err)
		return err
	}

	logger.Info("Server: Đã nhận giao dịch", "hash", tx.Hash().Hex(), "from", request.Connection().RemoteAddrSafe())

	// Tạo một receipt giả lập và gửi lại ngay lập tức
	r := receipt.NewReceipt(
		tx.Hash(),
		tx.FromAddress(),
		tx.ToAddress(),
		tx.Amount(),
		pb.RECEIPT_STATUS_RETURNED,
		nil,
		pb.EXCEPTION_NONE,
		1000,
		0,
		nil,
		uint64(0),
		common.Hash{},
		0,
	)

	bReceipt, err := r.Marshal()
	if err != nil {
		logger.Error("Server: Lỗi mã hóa receipt", "error", err)
		return err
	}
	// Gửi receipt lại cho client đã yêu cầu
	err = network.SendBytes(request.Connection(), command.Receipt, bReceipt, protocolVersion)
	if err != nil {
		logger.Error("Server: Lỗi khi gửi receipt lại cho client", "error", err)
	} else {
		logger.Info("Server: Đã gửi receipt lại cho client", "tx_hash", tx.Hash().Hex())
	}
	return err
}

// runServer khởi tạo và bắt đầu socket server.
func runServer(ctx context.Context) {
	keyPair := bls.NewKeyPair(common.FromHex(hardcodedPrivateKeyHex))
	connectionsManager := network.NewConnectionsManager()

	// Định nghĩa các handler lệnh phía server
	routes := map[string]func(t_network.Request) error{
		command.ReadTransaction: handleTransactionRequest,
	}
	handler := network.NewHandler(routes, nil)

	server, err := network.NewSocketServer(
		network.DefaultConfig(),
		keyPair,
		connectionsManager,
		handler,
		"TRANSACTION_SERVER_NODE",
		protocolVersion,
	)
	checkErr(err, "Không thể tạo socket server")

	// Goroutine lắng nghe tín hiệu hủy context và dừng server
	go func() {
		<-ctx.Done()
		logger.Info("Server: Đã nhận tín hiệu dừng. Đang tắt...")
		server.Stop()
	}()

	logger.Info("🚀 Server đang khởi động và lắng nghe trên", "address", listenAddress)
	if err := server.Listen(listenAddress); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, context.Canceled) {
		logger.Error("Server: Listener thất bại", "error", err)
	}
	logger.Info("Server: Đã tắt hoàn toàn.")
}

func main() {
	// Thiết lập logger
	logger.SetConfig(&logger.LoggerConfig{
		Flag:    logger.FLAG_DEBUGP,
		Outputs: []*os.File{os.Stdout},
	})

	log.Println("--- CHƯƠNG TRÌNH SERVER GIAO DỊCH ---")

	serverCtx, cancelServer := context.WithCancel(context.Background())
	defer cancelServer()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runServer(serverCtx)
	}()

	// Chờ server chạy cho đến khi có tín hiệu dừng (ví dụ: Ctrl+C)
	// Hoặc có thể thêm logic chờ khác ở đây
	wg.Wait()
}

// Hàm trợ giúp kiểm tra lỗi
func checkErr(err error, msg string) {
	if err != nil {
		fullMsg := fmt.Sprintf("%s: %v", msg, err)
		log.Fatal(fullMsg)
	}
}
