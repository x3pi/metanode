# High-Performance RPC Client

## Tổng quan

`HighPerformanceClientRPC` là một RPC client được tối ưu hóa để xử lý hàng trăm ngàn requests với khả năng điều tiết khi quá tải mà không ảnh hưởng đến tốc độ chung. **Đặc biệt hỗ trợ xử lý hàng ngàn giao dịch lớn (>1MB mỗi giao dịch)**.

## Tính năng chính

### 1. **Worker Pool**
- Xử lý concurrent requests với worker pool
- Tự động điều chỉnh số lượng workers dựa trên cấu hình
- Queue size có thể cấu hình (mặc định: 100,000)

### 2. **Adaptive Rate Limiting**
- Tự động điều chỉnh rate limit dựa trên error rate từ server
- Giảm rate limit khi error rate cao (>10%)
- Tăng rate limit khi error rate thấp (<1%)
- Range: 1,000 - 100,000 req/s

### 3. **Circuit Breaker**
- Bảo vệ client khỏi gửi request khi server down
- Tự động mở lại sau khi server phục hồi
- Cấu hình: max failures (100), reset timeout (30s)

### 4. **Request Batching** (Tùy chọn)
- Gộp nhiều requests thành một batch để giảm số lượng HTTP calls
- Cấu hình batch size và timeout
- Tự động fallback về single requests nếu batch fail

### 5. **Retry Logic với Exponential Backoff**
- Tự động retry khi gặp lỗi có thể retry được
- Exponential backoff: 100ms, 200ms, 400ms...
- Max retries: 3 (normal), 5 (critical)
- Large requests có backoff tăng gấp đôi để tránh timeout

### 6. **Request Priority**
- 4 mức độ ưu tiên: Low, Normal, High, Critical
- Critical requests luôn được xử lý ngay cả khi quá tải
- Rate limiting và circuit breaker không áp dụng cho Critical requests

### 7. **Large Request Support** ⭐ MỚI
- **Hỗ trợ giao dịch lớn (>1MB)**: Tối ưu hóa cho hàng ngàn giao dịch lớn
- **Buffer size tăng**: Write/Read buffer 64KB (tăng từ 4KB mặc định)
- **Timeout linh hoạt**: 
  - Normal requests: 60 giây
  - Large requests (>1MB): 300 giây (5 phút)
- **Max request size**: 10MB (có thể cấu hình)
- **Response header size**: 10MB (tăng từ 1MB mặc định)
- **Large requests bypass rate limit**: Đảm bảo không bị timeout
- **Giảm retries cho large requests**: Tránh timeout do retry quá nhiều

### 8. **Metrics & Monitoring**
- Theo dõi request rate, success rate, error rate
- Response time tracking
- Active requests count
- Queue size monitoring

## Cách sử dụng

### Khởi tạo cơ bản

```go
client, err := rpc_client.NewHighPerformanceClientRPC(
    "http://localhost:8545",
    "ws://localhost:8546",
    "your_private_key_hex",
    big.NewInt(1),
)
```

### Khởi tạo với tùy chọn (bao gồm large request support)

```go
client, err := rpc_client.NewHighPerformanceClientRPC(
    "http://localhost:8545",
    "ws://localhost:8546",
    "your_private_key_hex",
    big.NewInt(1),
    rpc_client.WithMaxConcurrent(50000),                    // Max 50k concurrent
    rpc_client.WithQueueSize(100000),                      // Queue 100k
    rpc_client.WithRateLimit(rate.Limit(50000)),           // 50k req/s
    rpc_client.WithBatchEnabled(true, 100, 10*time.Millisecond), // Batch enabled
    rpc_client.WithMaxRequestSize(20*1024*1024),           // Max 20MB per request
    rpc_client.WithLargeRequestTimeout(600*time.Second),   // 10 phút cho large requests
)
```

### Gửi request đồng bộ (tương thích với API cũ)

```go
request := &rpc_client.JSONRPCRequest{
    Jsonrpc: "2.0",
    Method:  "eth_blockNumber",
    Params:  []interface{}{},
    Id:      1,
}
response := client.SendHTTPRequestSync(request)
```

### Gửi request bất đồng bộ với priority

```go
request := &rpc_client.JSONRPCRequest{
    Jsonrpc: "2.0",
    Method:  "eth_getBalance",
    Params:  []interface{}{address.String(), "latest"},
    Id:      2,
}

// Gửi với priority cao
responseChan := client.SendHTTPRequestAsync(request, rpc_client.PriorityHigh)

select {
case resp := <-responseChan:
    // Xử lý response
case <-time.After(5 * time.Second):
    // Timeout
}
```

### Gửi hàng ngàn giao dịch lớn (>1MB)

```go
var wg sync.WaitGroup
for i := 0; i < 10000; i++ {
    wg.Add(1)
    go func(reqID int) {
        defer wg.Done()
        
        // Tạo giao dịch lớn (>1MB)
        largeData := make([]byte, 2*1024*1024) // 2MB data
        // ... điền dữ liệu vào largeData ...
        
        req := &rpc_client.JSONRPCRequest{
            Jsonrpc: "2.0",
            Method:  "eth_sendRawTransaction",
            Params:  []interface{}{hexutil.Encode(largeData)},
            Id:      reqID,
        }
        
        // Large requests tự động được xử lý với timeout dài hơn
        responseChan := client.SendHTTPRequestAsync(req, rpc_client.PriorityNormal)
        resp := <-responseChan
        // Xử lý response
    }(i)
}
wg.Wait()
```

### Xem metrics

```go
metrics := client.GetMetrics()
fmt.Printf("Request rate: %.2f req/s\n", metrics["request_rate"])
fmt.Printf("Success rate: %.2f%%\n", metrics["success_rate"])
fmt.Printf("Error rate: %.2f%%\n", metrics["error_rate"])
fmt.Printf("Response time: %s\n", metrics["response_time"])
fmt.Printf("Active requests: %d\n", metrics["active_requests"])
fmt.Printf("Queue size: %d\n", metrics["queue_size"])
```

### Sử dụng các method có sẵn

Tất cả các method từ `ClientRPC` đều hoạt động với `HighPerformanceClientRPC`:

```go
// GetAccountState tự động sử dụng high-performance client
accountState, err := client.GetAccountState(address, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))

// SendRawTransaction tự động sử dụng high-performance client
response := client.SendRawTransaction(input, ethInput, pubKeyBls)
```

### Shutdown graceful

```go
defer client.Shutdown()
```

## Cấu hình mặc định

- **Max Concurrent**: 50,000 requests
- **Queue Size**: 100,000 requests
- **Workers**: Tự động tính toán (maxConcurrent / 100, min 100, max 1000)
- **Rate Limit**: 50,000 req/s (ban đầu)
- **Circuit Breaker**: Max failures 100, reset timeout 30s
- **Batch**: Tắt mặc định
- **Retry**: Max 3 retries (normal), 5 retries (critical)
- **Max Request Size**: 10MB (có thể cấu hình)
- **Large Request Timeout**: 300 giây (5 phút)
- **Write/Read Buffer**: 64KB (tăng từ 4KB mặc định)
- **Response Header Size**: 10MB (tăng từ 1MB mặc định)

## Điều tiết khi quá tải

Client tự động điều tiết khi quá tải:

1. **Rate Limiting**: Giảm số lượng requests được gửi đi khi error rate cao
2. **Circuit Breaker**: Tạm dừng gửi requests khi server down
3. **Queue Management**: Từ chối requests mới khi queue đầy
4. **Priority Handling**: Critical requests luôn được xử lý

Tất cả các cơ chế này hoạt động tự động và không ảnh hưởng đến tốc độ chung khi hệ thống hoạt động bình thường.

## So sánh với ClientRPC cơ bản

| Tính năng | ClientRPC | HighPerformanceClientRPC |
|-----------|-----------|-------------------------|
| Concurrent requests | Không giới hạn rõ ràng | Có giới hạn và quản lý |
| Rate limiting | Không | Có (adaptive) |
| Circuit breaker | Không | Có |
| Retry logic | Không | Có (exponential backoff) |
| Request batching | Không | Có (tùy chọn) |
| Priority | Không | Có (4 levels) |
| Metrics | Không | Có |
| Queue management | Không | Có |

## Large Request Handling

Client tự động phát hiện và xử lý các requests lớn (>1MB):

1. **Tự động phát hiện**: Request >1MB được đánh dấu là large request
2. **Timeout linh hoạt**: 
   - Normal requests: 60 giây
   - Large requests: 300 giây (có thể cấu hình)
3. **Bypass rate limit**: Large requests có thể bypass rate limit để tránh timeout
4. **Giảm retries**: Max 2 retries cho large requests (thay vì 3) để tránh timeout
5. **Backoff tăng**: Exponential backoff tăng gấp đôi cho large requests
6. **Buffer tối ưu**: Write/Read buffer 64KB để xử lý hiệu quả hơn

### Ví dụ: Gửi hàng ngàn giao dịch lớn

```go
// Khởi tạo client với cấu hình cho large requests
client, _ := rpc_client.NewHighPerformanceClientRPC(
    "http://localhost:8545",
    "ws://localhost:8546",
    "private_key",
    big.NewInt(1),
    rpc_client.WithMaxRequestSize(20*1024*1024),         // 20MB max
    rpc_client.WithLargeRequestTimeout(600*time.Second), // 10 phút
)

// Gửi 10,000 giao dịch lớn (>1MB mỗi giao dịch)
for i := 0; i < 10000; i++ {
    largeTx := createLargeTransaction(i) // Tạo giao dịch >1MB
    
    req := &rpc_client.JSONRPCRequest{
        Jsonrpc: "2.0",
        Method:  "eth_sendRawTransaction",
        Params:  []interface{}{hexutil.Encode(largeTx)},
        Id:      i,
    }
    
    // Client tự động phát hiện và xử lý với timeout dài hơn
    responseChan := client.SendHTTPRequestAsync(req, rpc_client.PriorityNormal)
    // Xử lý response...
}
```

