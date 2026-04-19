package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

const (
	defaultLogLevel     = logger.FLAG_DEBUGP
	defaultDataFile     = "data.json"
	defaultSkipKeyPress = false
	REPLACE_ADDRESS     = "1510151015101510151015101510151015101510"
	protocolVersion     = "1.0.0"

	ServerAddress          = "0.0.0.0:4200"
	HardcodedPrivateKeyHex = "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b"

	defaultNumTotalTransactions = 1000000
	defaultConnectionPoolSize   = 200 // <<< THAY ĐỔI MỚI: Thêm hằng số cho giá trị mặc định
)

var (
	DATA_FILE_PATH         string
	LOG_LEVEL              int
	SKIP_KEY_PRESS         bool
	NUM_TOTAL_TRANSACTIONS int
	CONNECTION_POOL_SIZE   int // <<< THAY ĐỔI MỚI: Thêm biến toàn cục mới
)

type SCData struct {
	FromAddress    string   `json:"from_address"`
	Action         string   `json:"action"`
	Input          string   `json:"input"`
	Amount         string   `json:"amount"`
	Address        string   `json:"address"`
	ReplaceAddress []int    `json:"replace_address"`
	Name           string   `json:"name"`
	RelatedAddress []string `json:"related_address"`
}

type Client struct {
	id          int
	conn        t_network.Connection
	cancelFunc  context.CancelFunc
	receiptChan chan *receipt.Receipt
}

func NewClient(ctx context.Context, clientID int, serverAddress string) (*Client, error) {
	clientCtx, cancel := context.WithCancel(ctx)
	clientConfig := &network.Config{
		MaxMessageLength:       10 * 1024 * 1024,
		RequestChanSize:        100,
		ErrorChanSize:          100,
		WriteTimeout:           10 * time.Second,
		RequestChanWaitTimeout: 5 * time.Second,
		DialTimeout:            10 * time.Second,
	}

	connection := network.NewConnection(common.HexToAddress(fmt.Sprintf("0x%040x", clientID)), "TRANSACTION_CLIENT", clientConfig)
	connection.SetRealConnAddr(serverAddress)
	if err := connection.Connect(); err != nil {
		cancel()
		return nil, fmt.Errorf("client %d không thể kết nối: %w", clientID, err)
	}

	c := &Client{
		id:          clientID,
		conn:        connection,
		cancelFunc:  cancel,
		receiptChan: make(chan *receipt.Receipt, 1),
	}

	go c.conn.ReadRequest()
	go c.messageReceiver(clientCtx)

	return c, nil
}

func (c *Client) messageReceiver(ctx context.Context) {
	requestChan, errorChan := c.conn.RequestChan()
	for {
		select {
		case <-ctx.Done():
			return
		case req, ok := <-requestChan:
			if !ok {
				return
			}
			switch req.Message().Command() {
			case command.Receipt:
				r := &receipt.Receipt{}
				if err := r.Unmarshal(req.Message().Body()); err != nil {
					log.Printf("Client %d: Lỗi giải mã receipt: %v", c.id, err)
					continue
				}
				select {
				case c.receiptChan <- r:
				case <-time.After(1 * time.Second):
					log.Printf("Client %d: Gửi receipt vào kênh bị timeout.", c.id)
				}
			case p_common.ServerBusy:
				log.Printf("Client %d: NHẬN TÍN HIỆU SERVER BUSY! Tạm dừng 2 giây...", c.id)
				time.Sleep(2 * time.Second)
			case command.TransactionError:
				log.Printf("Client %d: Nhận được TransactionError!", c.id)
				// Giải mã TransactionError từ body
				errProto := &pb.TransactionHashWithError{}
				err := proto.Unmarshal(req.Message().Body(), errProto)
				if err != nil {
					log.Printf("Client %d: Lỗi giải mã TransactionHashWithError: %v", c.id, err)
					continue
				}
				log.Printf(
					"Client %d: TransactionError - Hash: %x, Code: %d, Description: %s",
					c.id, errProto.Hash, errProto.Code, errProto.Description,
				)
			}
		case err, ok := <-errorChan:
			if !ok {
				return
			}
			if !isNetworkClosedError(err) {
				log.Printf("Client %d: Nhận được lỗi: %v", c.id, err)
			}
			return
		}
	}
}

func (c *Client) SendTransactionAndReceiveReceipt(
	fromAddress common.Address,
	toAddress common.Address,
	amount *big.Int,
	maxGas uint64,
	maxGasPrice uint64,
	bData []byte,
	relatedAddresses []common.Address,
) (*receipt.Receipt, error) {
	lastDeviceKey := common.HexToHash("0x0")
	newDeviceKey := common.HexToHash("0x0")
	bRelatedAddresses := make([][]byte, len(relatedAddresses))
	for i, v := range relatedAddresses {
		bRelatedAddresses[i] = v.Bytes()
	}
	tx := transaction.NewTransaction(
		fromAddress, toAddress, amount, maxGas, maxGasPrice, 30,
		bData, bRelatedAddresses, lastDeviceKey, newDeviceKey, 0, 911,
	)

	bTransaction, err := tx.Marshal()
	if err != nil {
		return nil, fmt.Errorf("lỗi mã hóa giao dịch: %w", err)
	}
	// sizeHeader := unsafe.Sizeof(bTransaction)                           // thường là 24 bytes
	// sizeData := len(bTransaction) * int(unsafe.Sizeof(bTransaction[0])) // 5 * 1
	// totalSize := int(sizeHeader) + sizeData

	// fmt.Println("Header:", sizeHeader, "bytes")
	// fmt.Println("Data:", sizeData, "bytes")
	// fmt.Println("Total:", totalSize, "bytes")
	msg := generateTransactionMessage(command.ReadTransaction, bTransaction)

	if err := c.conn.SendMessage(msg); err != nil {
		return nil, fmt.Errorf("lỗi gửi ReadTransaction: %w", err)
	}

	select {
	case receivedReceipt := <-c.receiptChan:
		return receivedReceipt, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("client %d: timeout khi chờ receipt", c.id)
	}
}

func (c *Client) Stop() {
	if c.cancelFunc != nil {
		c.cancelFunc()
	}
	if c.conn != nil {
		_ = c.conn.Disconnect()
	}
}

func main() {
	flag.IntVar(&LOG_LEVEL, "log-level", defaultLogLevel, "Log level")
	flag.StringVar(&DATA_FILE_PATH, "data", defaultDataFile, "Data file path")
	flag.BoolVar(&SKIP_KEY_PRESS, "skip", defaultSkipKeyPress, "Skip press to run new transaction")
	flag.IntVar(&NUM_TOTAL_TRANSACTIONS, "num-tx", defaultNumTotalTransactions, "Total number of transactions to send")
	// <<< THAY ĐỔI MỚI: Thêm cờ mới để thiết lập kích thước connection pool
	flag.IntVar(&CONNECTION_POOL_SIZE, "pool-size", defaultConnectionPoolSize, "Number of concurrent connections in the pool")
	flag.Parse()

	logger.SetConfig(&logger.LoggerConfig{
		Flag:    LOG_LEVEL,
		Outputs: []*os.File{os.Stdout},
	})

	log.Println("--- CHƯƠNG TRÌNH CLIENT GIAO DỊCH ---")

	datas := loadTransactionData()
	log.Printf("Đã tải %d giao dịch từ %s\n", len(datas), DATA_FILE_PATH)
	processTransactions(ServerAddress, common.FromHex(HardcodedPrivateKeyHex), datas)

	waitForExit()
}

func checkErr(err error, msg string) {
	if err != nil {
		fullMsg := fmt.Sprintf("%s: %v", msg, err)
		log.Fatal(fullMsg)
	}
}

func loadTransactionData() []SCData {
	dat, err := os.ReadFile(DATA_FILE_PATH)
	checkErr(err, fmt.Sprintf("lỗi đọc file dữ liệu %s", DATA_FILE_PATH))
	var scDatas []SCData
	err = json.Unmarshal(dat, &scDatas)
	checkErr(err, fmt.Sprintf("lỗi giải mã dữ liệu từ %s", DATA_FILE_PATH))
	return scDatas
}

func resolveAddress(addrStr string, addressList []string) common.Address {
	if len(addrStr) >= 40 {
		return common.HexToAddress(addrStr)
	}
	index, err := strconv.Atoi(addrStr)
	checkErr(err, fmt.Sprintf("định dạng chỉ số địa chỉ không hợp lệ: %s", addrStr))
	if index < 0 || index >= len(addressList) {
		log.Fatalf("chỉ số địa chỉ %d ngoài giới hạn (kích thước danh sách %d)", index, len(addressList))
	}
	return common.HexToAddress(addressList[index])
}

func buildRelatedAddresses(data *SCData, addressList []string, privateKey []byte) []common.Address {
	relatedAddresses := make([]common.Address, len(data.RelatedAddress)+1)
	for i, v := range data.RelatedAddress {
		relatedAddresses[i] = resolveAddress(v, addressList)
	}
	relatedAddresses[len(data.RelatedAddress)] = bls.NewKeyPair(privateKey).Address()
	return relatedAddresses
}

func replaceInputAddresses(input string, replaceIndices []int, addressList []string) string {
	output := input
	for _, index := range replaceIndices {
		if index < 0 || index >= len(addressList) {
			log.Fatalf("chỉ số replace_address %d ngoài giới hạn (kích thước danh sách %d)", index, len(addressList))
		}
		addrWithoutPrefix := strings.TrimPrefix(addressList[index], "0x")
		output = strings.Replace(output, REPLACE_ADDRESS, addrWithoutPrefix, 1)
	}
	return output
}

func generateTransactionMessage(cmd string, body []byte) t_network.Message {
	header := &pb.Header{Command: cmd, Version: protocolVersion, ID: uuid.NewString()}
	return network.NewMessage(&pb.Message{Header: header, Body: body})
}

func processTransactions(serverAddr string, privateKey []byte, datas []SCData) {
	addressList := []string{}
	maxGas := uint64(10000000)
	maxGasPrice := uint64(p_common.MINIMUM_BASE_FEE)

	for i := range datas {
		data := &datas[i]
		logger.Info("Đang xử lý giao dịch:", data.Name, "Hành động:", data.Action)

		fromAddress := common.HexToAddress(data.FromAddress)
		amount, success := new(big.Int).SetString(data.Amount, 10)
		if !success {
			checkErr(fmt.Errorf("định dạng số tiền không hợp lệ: %s", data.Amount), "Không thể phân tích số tiền")
		}

		inputHex := replaceInputAddresses(data.Input, data.ReplaceAddress, addressList)
		inputBytes := common.FromHex(inputHex)

		var toAddress common.Address
		var bData []byte

		switch data.Action {
		case "call", "read":
			toAddress = resolveAddress(data.Address, addressList)
			callData := transaction.NewCallData(inputBytes)
			var err error
			bData, err = callData.Marshal()
			checkErr(err, "Không thể mã hóa call data")

			if data.Action == "read" {
				var wg sync.WaitGroup
				var successfulTransactions uint64 = 0
				var failedTransactions uint64 = 0

				numTotalTransactions := NUM_TOTAL_TRANSACTIONS
				// <<< THAY ĐỔI MỚI: Sử dụng biến toàn cục thay vì giá trị cứng
				connectionPoolSize := CONNECTION_POOL_SIZE

				clientPool := make(chan *Client, connectionPoolSize)
				logger.Info(fmt.Sprintf("Chuẩn bị tạo connection pool với %d kết nối...", connectionPoolSize))

				for j := 0; j < connectionPoolSize; j++ {
					client, err := NewClient(context.Background(), j, serverAddr)
					if err != nil {
						log.Fatalf("Không thể tạo client %d cho pool: %v", j, err)
					}
					clientPool <- client
				}
				defer func() {
					close(clientPool)
					for client := range clientPool {
						client.Stop()
					}
				}()

				logger.Info(fmt.Sprintf("Chuẩn bị gửi %d read transactions sử dụng pool %d kết nối...", numTotalTransactions, connectionPoolSize))
				startTime := time.Now()

				for j := 0; j < numTotalTransactions; j++ {
					wg.Add(1)
					go func(jobID int, transactionData []byte) {
						defer wg.Done()
						client := <-clientPool
						defer func() { clientPool <- client }()

						relatedAddresses := buildRelatedAddresses(data, addressList, privateKey)

						receivedReceipt, err := client.SendTransactionAndReceiveReceipt(
							fromAddress, toAddress, amount, maxGas, maxGasPrice, transactionData, relatedAddresses,
						)
						if err != nil {
							logger.Error("Lỗi gửi giao dịch hoặc nhận receipt", "job_id", jobID, "error", err)
							atomic.AddUint64(&failedTransactions, 1)
							return
						}

						if receivedReceipt.Status() == pb.RECEIPT_STATUS_RETURNED {
							atomic.AddUint64(&successfulTransactions, 1)
						} else {
							atomic.AddUint64(&failedTransactions, 1)
							log.Printf("receivedReceipt thất bại:  %v", receivedReceipt)
							logger.Warn("Giao dịch thất bại với trạng thái", "status", receivedReceipt.Status(), "job_id", jobID)
						}
					}(j, bData)
					time.Sleep(100 * time.Nanosecond)
				}
				wg.Wait()

				elapsedTime := time.Since(startTime)
				totalSubmitted := successfulTransactions + failedTransactions
				var tps float64
				if elapsedTime.Seconds() > 0 {
					tps = float64(successfulTransactions) / elapsedTime.Seconds()
				}

				log.Printf("================ THỐNG KÊ GIAO DỊCH ================")
				log.Printf("Tổng thời gian thực thi: %.2f giây", elapsedTime.Seconds())
				log.Printf("Tổng số giao dịch đã gửi: %d", totalSubmitted)
				log.Printf("Giao dịch thành công: %d", successfulTransactions)
				log.Printf("Giao dịch thất bại: %d", failedTransactions)
				log.Printf("Tốc độ xử lý (TPS): %.2f giao dịch/giây", tps)
				log.Printf("=====================================================")
				log.Printf("Tốc độ xử lý (TPS): %.2f giao dịch/giây", tps)

				continue
			}

		default:
			logger.Warn("Loại hành động không xác định:", data.Action, "- bỏ qua.")
			continue
		}
	}
}

func waitForInput(prompt string) {
	if !SKIP_KEY_PRESS {
		logger.Debug(prompt)
		input := bufio.NewScanner(os.Stdin)
		input.Scan()
	}
}

func waitForExit() {
	waitForInput("Hoàn tất. Nhấn Enter để thoát.")
}

func isNetworkClosedError(err error) bool {
	return err != nil && (errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "connection reset by peer") || strings.Contains(err.Error(), "kết nối đã bị ngắt"))
}
