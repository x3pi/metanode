# Scaling to Hundreds of Thousands of Connections

## Phân tích hiện tại

### ✅ Đã tối ưu:
1. **sync.Map**: Non-blocking lookups
2. **Metadata cache**: < 1ms reads
3. **Large channel buffers**: 65K+ messages
4. **Worker pool**: Up to 2048 workers
5. **Sharding**: 32 shards

### ⚠️ Cần cải thiện cho hàng trăm ngàn connections:

## 1. Memory Usage Analysis

### Mỗi Connection chiếm:
```
cmdChan:        1000 × 8 bytes    = ~8 KB
requestChan:    204,800 × 8 bytes = ~1.6 MB (worst case)
sendChan:       65,536 × 8 bytes  = ~524 KB
errorChan:      2,000 × 8 bytes   = ~16 KB
Metadata cache: ~200 bytes
Goroutines:     3 × 2 KB          = ~6 KB
Total: ~2.15 MB per connection (worst case)
```

### Với 100,000 connections:
- **Memory**: ~215 GB (worst case)
- **Goroutines**: 300,000 goroutines
- **File descriptors**: 100,000+ (OS limit thường 65,536)

## 2. Các điểm cần cải thiện

### A. Tăng số Shards
**Vấn đề**: 32 shards có thể không đủ với 100K+ connections
- Mỗi shard: ~3,125 connections
- sync.Map có thể chậm với > 10K entries/shard

**Giải pháp**: Tăng số shards động dựa trên số connections
```go
// Dynamic sharding
func calculateNumShards(connectionCount int) int {
    if connectionCount < 1000 {
        return 32
    } else if connectionCount < 10000 {
        return 64
    } else if connectionCount < 100000 {
        return 128
    } else {
        return 256 // Max 256 shards
    }
}
```

### B. Giảm Channel Buffers (Memory Optimization)
**Vấn đề**: requestChan = 204,800 quá lớn cho mỗi connection
- Với 100K connections: 204,800 × 100K = 20.48 billion buffer slots

**Giải pháp**: Giảm buffer size dựa trên load
```go
// Adaptive buffer size
func calculateRequestChanSize(numWorkers int) int {
    // Giảm từ numWorkers * 100 xuống numWorkers * 10
    return numWorkers * 10 // Vẫn đủ cho burst
}
```

### C. Connection Pooling/Reuse
**Vấn đề**: Tạo mới connection tốn memory và time

**Giải pháp**: Reuse connections khi có thể
- Idle connection pool
- Connection lifecycle management

### D. Goroutine Pool cho I/O
**Vấn đề**: 3 goroutines/connection = 300K goroutines với 100K connections

**Giải pháp**: Shared goroutine pool cho I/O
- epoll/kqueue thay vì goroutine per connection
- Sử dụng `gnet` hoặc `evio` library

### E. Tăng Worker Pool
**Vấn đề**: Max 2048 workers có thể không đủ

**Giải pháp**: Tăng max workers
```go
if numWorkers > 4096 { // Tăng từ 2048
    numWorkers = 4096
}
```

### F. OS Limits
**Vấn đề**: File descriptor limit (thường 65,536)

**Giải pháp**: 
- Tăng ulimit: `ulimit -n 1000000`
- Sử dụng SO_REUSEPORT
- Load balancing với nhiều processes

## 3. Recommended Improvements

### Priority 1: Critical (Cần làm ngay)

1. **Tăng số Shards động**
   ```go
   const (
       minShards = 32
       maxShards = 256
   )
   ```

2. **Giảm RequestChanSize**
   ```go
   RequestChanSize: numWorkers * 10 // Thay vì * 100
   ```

3. **Tăng Worker Pool Max**
   ```go
   if numWorkers > 4096 {
       numWorkers = 4096
   }
   ```

### Priority 2: Important (Nên làm)

4. **Connection Lifecycle Management**
   - Idle timeout
   - Connection cleanup
   - Memory monitoring

5. **Rate Limiting**
   - Per-connection rate limit
   - Global rate limit
   - Adaptive rate limiting

### Priority 3: Nice to Have

6. **Metrics & Monitoring**
   - Connection count
   - Memory usage
   - Goroutine count
   - Channel queue lengths

7. **Load Balancing**
   - Multiple nodes
   - Connection distribution
   - Health checks

## 4. Implementation Plan

### Phase 1: Memory Optimization (Immediate)
- [ ] Giảm RequestChanSize từ *100 xuống *10
- [ ] Tăng số shards động (32 → 256)
- [ ] Tăng worker pool max (2048 → 4096)

### Phase 2: Scalability (Short term)
- [ ] Connection pooling
- [ ] Rate limiting
- [ ] Metrics collection

### Phase 3: Advanced (Long term)
- [ ] epoll/kqueue I/O
- [ ] Load balancing
- [ ] Horizontal scaling

## 5. Expected Results

### Sau khi optimize:
- **Memory**: ~50 GB (thay vì 215 GB)
- **Throughput**: 500K+ req/s
- **Connections**: 100K+ đồng thời
- **Latency**: < 10ms p99

## 6. Testing Recommendations

1. **Load Testing**: 
   - 10K, 50K, 100K connections
   - Monitor memory, CPU, goroutines

2. **Stress Testing**:
   - Burst traffic
   - Connection churn
   - Network failures

3. **Long Running**:
   - Memory leaks
   - Goroutine leaks
   - Connection cleanup

