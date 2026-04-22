package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic" // Import package atomic
	"time"
)

// Các cấu trúc RPC không thay đổi
type RPCRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}
type RPCResponse struct {
	Jsonrpc string    `json:"jsonrpc"`
	ID      int       `json:"id"`
	Result  Block     `json:"result"`
	Error   *RPCError `json:"error,omitempty"`
}
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type Block struct {
	Number       string   `json:"number"`
	Transactions []string `json:"transactions"`
}

// Hàm callRPC không thay đổi
func callRPC(url string, method string, params []interface{}) (*Block, error) {
	reqBody := RPCRequest{
		Jsonrpc: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	}
	data, _ := json.Marshal(reqBody)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := ioutil.ReadAll(resp.Body)
	var rpcResp RPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, err
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC Error: %s", rpcResp.Error.Message)
	}
	return &rpcResp.Result, nil
}

// --- PHẦN LOGIC MỚI ---

// Worker sẽ nhận công việc từ channel `jobs` và gửi kết quả tới channel `results`
// Nó cũng sẽ cập nhật biến lowestEmptyBlock một cách an toàn
func worker(id int, url string, wg *sync.WaitGroup, jobs <-chan int, results chan<- []string, lowestEmptyBlock *atomic.Int64) {
	defer wg.Done()

	for blockNum := range jobs {
		// Tối ưu: Nếu block hiện tại đã lớn hơn block rỗng thấp nhất đã tìm thấy, bỏ qua
		if int64(blockNum) >= lowestEmptyBlock.Load() {
			continue
		}

		blockHex := "0x" + strconv.FormatInt(int64(blockNum), 16)
		block, err := callRPC(url, "eth_getBlockByNumber", []interface{}{blockHex, false})
		if err != nil {
			log.Printf("Worker %d: Lỗi khi gọi block %d: %v\n", id, blockNum, err)
			continue
		}

		// Nếu block rỗng, cập nhật lại giá trị block rỗng thấp nhất
		if block == nil || len(block.Transactions) == 0 {
			// Sử dụng vòng lặp CompareAndSwap để cập nhật một cách an toàn
			for {
				currentMin := lowestEmptyBlock.Load()
				if int64(blockNum) >= currentMin {
					break // Block rỗng này không thấp hơn, không cần cập nhật
				}
				// Nếu giá trị hiện tại vẫn là `currentMin`, thì cập nhật nó thành `blockNum`
				if lowestEmptyBlock.CompareAndSwap(currentMin, int64(blockNum)) {
					fmt.Printf("Worker %d: Tìm thấy block rỗng mới: %d. Cập nhật giới hạn quét.\n", id, blockNum)
					break
				}
			}
			continue // Không gửi gì vào channel results
		}

		fmt.Printf("Worker %d: Block %d có %d transactions\n", id, blockNum, len(block.Transactions))
		results <- block.Transactions
	}
}

func main() {
	url := "http://192.168.1.230:8747" // Thay bằng RPC endpoint của bạn

	numWorkers := 100          // Tăng số lượng worker để xử lý nhanh hơn
	maxBlockToCheck := 2000000 // Giới hạn trên để quét

	jobs := make(chan int, numWorkers)
	results := make(chan []string, numWorkers*2) // Tăng buffer cho kết quả

	// Dùng atomic.Int64 để lưu số hiệu block rỗng thấp nhất một cách an toàn
	var lowestEmptyBlock atomic.Int64
	// Khởi tạo giá trị lớn hơn maxBlockToCheck để không ảnh hưởng ban đầu
	lowestEmptyBlock.Store(int64(maxBlockToCheck + 1))

	var wg sync.WaitGroup
	allTxs := []string{}

	// 1. Khởi tạo các worker
	for i := 1; i <= numWorkers; i++ {
		wg.Add(1)
		go worker(i, url, &wg, jobs, results, &lowestEmptyBlock)
	}

	// 2. Goroutine để gửi công việc (job feeder)
	go func() {
		// Đóng channel jobs sau khi vòng lặp kết thúc
		defer close(jobs)
		for i := 1; i <= maxBlockToCheck; i++ {
			// Nếu `i` đã lớn hơn hoặc bằng block rỗng thấp nhất, dừng gửi việc
			if int64(i) >= lowestEmptyBlock.Load() {
				fmt.Printf("Đã đạt đến block rỗng thấp nhất là %d. Dừng gửi công việc mới.\n", lowestEmptyBlock.Load())
				break
			}
			jobs <- i
		}
	}()

	// 3. Goroutine để thu thập kết quả
	var collectorWg sync.WaitGroup
	collectorWg.Add(1)
	go func() {
		defer collectorWg.Done()
		for txs := range results {
			allTxs = append(allTxs, txs...)
		}
	}()

	// Đợi tất cả worker hoàn thành công việc
	// Worker sẽ tự dừng khi channel `jobs` được đóng và không còn việc
	wg.Wait()

	// Sau khi tất cả worker đã dừng, đóng channel `results` để báo cho collector
	close(results)

	// Đợi collector xử lý xong tất cả kết quả đã nhận
	collectorWg.Wait()

	fmt.Println("=====================================================================")
	fmt.Printf("=== Hoàn thành! Quét đến block %d.                               ===\n", lowestEmptyBlock.Load()-1)
	fmt.Printf("=== Tổng số txHash thu thập được: %d                                ===\n", len(allTxs))
	fmt.Println("=====================================================================")
}
