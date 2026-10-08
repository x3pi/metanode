# Báo Cáo Thiết Kế Kiến Trúc: Bounded Memory Indexes Cho Metanode Core
**Tài liệu tham chiếu:** `note/design_bounded_memory_indexes_20261007.md`  
**Ngày lập:** 2026-10-07  
**Tác giả:** Metanode Core Engineering Team  
**Trạng thái:** DRAFT — Trình duyệt trước khi chỉnh sửa mã nguồn cốt lõi (Theo Kế hoạch Giai đoạn 5)  

---

## 1. Đặt Vấn Đề (Problem Statement)

Trong đợt điều tra hiệu năng bộ nhớ RSS và Heap Allocation (`note/perf_rss_investigation_20261007.md`), hệ thống phát hiện hiện tượng phình bộ nhớ Go Heap tuyến tính theo số lượng giao dịch:
- **Tốc độ phình:** Tăng **+98.89 MB HeapAlloc** cho mỗi 100,000 transactions EIP-1559 được xử lý.
- **Top 2 vị trí cấp phát tích luỹ trong Heap Profile:**
  1. `execution/pkg/blockchain.(*ethHashMapBlsHashMap).StoreBatch` và `.Store`
  2. `execution/pkg/blockchain.(*txHashToBlockNumberMap).StoreBatch` và `.Store`
  3. `execution/pkg/blockchain.(*BlockChain).AddTxToCache` (`txsCache`)

Dù Go runtime chạy Garbage Collection định kỳ, bộ nhớ RAM thực tế (Resident Set Size - RSS) không giảm vì các mục này được giữ tham chiếu sống vĩnh viễn trong thời gian active window, và cấu trúc `map` của Go runtime không giải phóng dung lượng bucket rỗng về OS sau khi gọi `delete()`.

---

## 2. Phân Tích Hiện Trạng Mã Nguồn (Source Code Audit)

### 2.1 Cấu trúc hai bảng Mapping trong RAM

Tệp `execution/pkg/blockchain/blockchain.go`:
```go
type ethHashMapBlsHashMap struct {
    mu   sync.RWMutex
    data map[common.Hash]cachedHash
}

type cachedHash struct {
    hash    common.Hash // 32 bytes
    addedAt time.Time   // 24 bytes (wall, ext, loc)
}

type txHashToBlockNumberMap struct {
    mu   sync.RWMutex
    data map[common.Hash]cachedUint64
}

type cachedUint64 struct {
    value   uint64      // 8 bytes
    addedAt time.Time   // 24 bytes
}
```

### 2.2 Kích thước tính toán trên Heap (Heap Memory Per Entry)
- **`txHashToBlockNumberMap`:**
  - Key: `common.Hash` (32 bytes).
  - Value: `cachedUint64` (8 bytes `value` + 24 bytes `addedAt` = 32 bytes).
  - Go Runtime Map Bucket Overhead: 8 key-value pairs mỗi bucket (64 bytes header + $8 \times (32 + 32) = 576$ bytes $\rightarrow \approx 72$ bytes/entry).
  - Bổ sung pointer, hash bits và overflow chain: **~130 – 150 bytes / entry**.
  - Với 100,000 txs: $\approx 15$ MB. Với 1,000,000 txs: $\approx 150$ MB.
- **`ethHashMapBlsHashMap`:**
  - Key: `common.Hash` (32 bytes).
  - Value: `cachedHash` (32 bytes `hash` + 24 bytes `addedAt` = 56 bytes + 8 bytes padding = 64 bytes).
  - Overhead: **~150 – 170 bytes / entry**.
  - Với 100,000 txs: $\approx 17$ MB. Với 1,000,000 txs: $\approx 170$ MB.

### 2.3 Cơ chế dọn dẹp hiện tại (Prune Mechanism & Phân Tích Giới Hạn)
- Các hằng số tại `blockchain.go` (dòng 33-38):
  ```go
  txCacheTTL      = 2 * time.Minute
  blockCacheTTL   = 10 * time.Minute
  mappingCacheTTL = 30 * time.Minute
  cleanupInterval = 1 * time.Minute
  ```
- **Bản chất kỹ thuật:** Hai map `txHashToBlockNumberMap` và `ethHashMapBlsHashMap` trong RAM được **chặn theo thời gian (Time-bounded với TTL = 30 phút)**, nhưng **KHÔNG được chặn theo dung lượng phần tử (Unbounded by Capacity/Size)**.
- **Lý do Benchmark 8 Đợt (Wave 1 - 8) Tăng Tuyến Tính:**
  - Toàn bộ bài benchmark 8 đợt 400k txs chỉ kéo dài khoảng **3.5 phút** (mỗi wave ~25 giây).
  - Do thời gian chạy ngắn hơn rất nhiều so với ngưỡng `mappingCacheTTL = 30 * time.Minute`, toàn bộ entry được thêm vào đều chưa đến hạn hết hạn, nên các hàm `pruneTxHashCache` và `pruneEthHashCache` (chạy mỗi 1 phút) chưa thực hiện evict bất kỳ entry nào.
- **Kiểm chứng thực nghiệm 45 phút (Evidence `ttl_saturation_45min`):**
  - Thực nghiệm tiêm tải liên tục 50 tx/s trong 45 phút trên cụm kiểm thử cô lập (xem chi tiết tại Mục 7.3 của [note/perf_rss_investigation_20261007.md](file:///home/abc/chain-n/metanode/note/perf_rss_investigation_20261007.md)) đã xác nhận hiện tượng này: tại Cửa sổ 2 (sau 30 phút, khi TTL có hiệu lực), tốc độ tăng HeapAlloc đã giảm từ $+4.20$ MB/phút xuống $+0.74$ MB/phút (giảm 82.4%), nhưng kết luận theo tiêu chí định lượng vẫn là **CHƯA BÃO HOÀ (UNBOUNDED)** do hệ số góc vẫn dương và tỷ lệ đạt $17.58\% > 5\%$.
  - Kết quả này củng cố dứt khoát nguyên nhân kiến trúc: cơ chế thời gian (Time-bounded TTL) không thể thay thế được cơ chế chặn trần dung lượng (Bounded Memory Cache / LRU) vì Go runtime map không bao giờ giải phóng cấu trúc bucket sau thao tác `delete()`.
- **Rủi ro kiến trúc cần giải quyết:**
  1. **Không có giới hạn trần dung lượng (Unbounded by Size):** Dung lượng map chỉ phụ thuộc vào số lượng tx phát sinh trong vòng 30 phút. Nếu mạng gặp spam blast 5,000 tx/s, trong 30 phút sẽ tích luỹ $5,000 \times 1,800 = 9,000,000$ txs $\rightarrow$ RAM chiếm dụng **~1.5 GB đến 2 GB** chỉ cho hai map này trước khi bắt đầu prune.
  2. **Quét Range $O(N)$ định kỳ:** Cứ mỗi 1 phút, hàm `pruneTxHashCache` và `pruneEthHashCache` lấy `mu.Lock()` và lặp qua toàn bộ map (`for k, v := range m.data`). Khi map có hàng triệu phần tử, thao tác này gây lock contention nghiêm trọng, chặn đứng mọi thao tác đọc/ghi RPC và Block processing.
  3. **Đặc tính Go runtime map:** Khi gọi `delete(m.data, k)`, Go runtime chỉ đánh dấu ô trống trong bucket, **không co cụm hoặc giải phóng bộ nhớ buckets** về heap/OS, dẫn tới RAM chỉ tăng mà không giảm sau đợt blast.

---

## 3. Bản Đồ Tương Tác: Ai Ghi, Ai Đọc và Có Persist Xuống DB Không?

### 3.1 Đường Ghi (Write Path) & Bằng Chứng Bền Vững Đĩa Cứng
1. **Khi Block được Commit (`pkg/blockchain/block_state_commit.go:205`):**
   - Khi một block được thực thi xong, hàm `bc.SetTxHashMapBlockNumberBatch(txs, blockNum)` được gọi.
   - Thao tác thực hiện:
     ```go
     // pkg/blockchain/blockchain.go:634-645
     bc.storeBatchToDirty(dirtyKVs)        // 1. Chuẩn bị ghi vào Pebble DB storageMapping
     bc.txHashToBlockNumber.StoreBatch(...) // 2. Ghi vào RAM map
     ```
   - Tương tự, `bc.SetEthHashMapblsHash(ethHash, blsHash)` tại `pkg/blockchain/blockchain.go:693-708` ghi vào `dirtyStorage` và `bc.ethHashMapBlsHash`.
   - Sau đó, `bc.Commit()` tại `pkg/blockchain/blockchain.go:740-770` được kích hoạt, ghi toàn bộ `dirtyStorage` xuống **Pebble DB** (`storageManager.GetStorageMapping().BatchPut(...)`).
2. **Khẳng định bền vững & Bằng chứng thực nghiệm (Empirical Proof):**
   Toàn bộ dữ liệu `txHash -> blockNumber` và `ethHash -> blsHash` được lưu bền vững xuống Pebble DB đĩa cứng dưới các tiền tố:
   - `txHashPrefix0x<txHashHex>` $\rightarrow$ `uint64 blockNumber` (8 bytes big-endian)
   - `ethHashMapBlsHashPrefix0x<ethHashHex>` $\rightarrow$ `common.Hash blsHash` (32 bytes)
   - Khẳng định này đã được **kiểm chứng thực nghiệm chạy thật (real runtime proof)** qua hai bộ kiểm tra:
     * **Unit Test ShardelDB Pebble (`mapping_pebble_fallback_test.go`):** Khởi tạo Pebble DB thật trên đĩa, commit mappings, xóa sạch 100% entry khỏi RAM (`Delete`), gọi hàm đọc nhanh và xác nhận đọc thành công từ đĩa rồi nạp lại vào RAM. Nguồn bằng chứng: [pebble_fallback_go_test.log](file:///home/abc/chain-n/metanode/note/evidence/design_bounded_memory_indexes_20261007/pebble_fallback_go_test.log) <!-- evidence:pebble_fallback_go_test -->.
     * **End-to-End JSON-RPC Cold Cache (`verify_pebble_fallback_rpc.py`):** Khởi động cụm 4 node, blast 200 txs EIP-1559, tắt cụm để hủy toàn bộ RAM cache, khởi động lại cụm với RAM rỗng (xác nhận bởi comment tại `mapping_rebuild.go:113` không preload tx), gửi truy vấn `eth_getTransactionReceipt` và `eth_getTransactionByHash` qua JSON-RPC cổng 31646. Kết quả trả về đầy đủ status `0x1`, gas `20000`, block number chính xác. Nguồn bằng chứng: [pebble_fallback_rpc_verification.log](file:///home/abc/chain-n/metanode/note/evidence/design_bounded_memory_indexes_20261007/pebble_fallback_rpc_verification.log) <!-- evidence:pebble_fallback_rpc_verification -->.

### 3.2 Danh Sách Toàn Bộ Nơi Gọi (Caller Sites Audit)
Đã thực hiện rà soát tĩnh toàn bộ mã nguồn repo (`execution/` và `consensus/`) và lưu vết kiểm toán thô tại [caller_sites_audit.txt](file:///home/abc/chain-n/metanode/note/evidence/design_bounded_memory_indexes_20261007/caller_sites_audit.txt) <!-- evidence:caller_sites_audit -->:
1. `execution/pkg/utils/receipt_helper/receipt_helper.go:22`:
   - Hàm `GetTransactionReceipt()` gọi `GetBlockNumberByTxHashFast(txHash)` để lấy `blockNumber` trước khi tra cứu block và receipt.
2. `execution/cmd/simple_chain/rpc_transaction.go`:
   - Dòng 125: trong RPC `eth_getTransactionByHash` gọi `GetEthHashMapblsHash(hashEth)`.
   - Dòng 129: trong RPC `eth_getTransactionByHash` gọi `GetBlockNumberByTxHashFast(hashTx)`.
   - Dòng 415: trong RPC `eth_getTransactionReceipt` gọi `GetEthHashMapblsHash(hashEth)`.
   - Dòng 421: trong RPC `eth_getTransactionReceipt` gọi `GetBlockNumberByTxHashFast(searchHash)`.
3. `execution/cmd/simple_chain/processor/block_processor_receipt.go`:
   - Dòng 63: trong `getTransactionReceipt` gọi `GetEthHashMapblsHash(hashEth)`.
   - Dòng 69: trong `getTransactionReceipt` gọi `GetBlockNumberByTxHashFast(searchHash)`.
   - Dòng 233: trong `getTransactionByHash` gọi `GetEthHashMapblsHash(hashEth)`.
   - Dòng 239: trong `getTransactionByHash` gọi `GetBlockNumberByTxHashFast(searchHash)`.
4. `execution/cmd/simple_chain/debug_api.go`:
   - Dòng 120, 135: Debug API tra cứu `GetEthHashMapblsHash(hash)`.
   - Dòng 131, 140: Debug API tra cứu `GetBlockNumberByTxHash(hash)`.
5. Unit tests:
   - `execution/pkg/blockchain/mapping_rebuild_test.go:293, 301`: kiểm tra fallback Pebble DB sau khi mất mapping RAM.
   - `execution/pkg/blockchain/mapping_pebble_fallback_test.go:50, 54`: kiểm tra fallback Pebble DB khi evict RAM.
   - `execution/cmd/simple_chain/processor/raw_eth_ingress_test.go:285, 298`: kiểm tra hash mapping.

**Nhận định kiểm toán:**
- Toàn bộ các nơi gọi chỉ phục vụ JSON-RPC / P2P query (tra cứu giao dịch / receipt) và Debug API.
- Không có bất kỳ lời gọi nào nằm trong luồng Consensus (Raft/BFT/DAG ordering/propose/vote).
- Không có bất kỳ lời gọi nào nằm trong luồng EVM execution hay Account State transition (`ApplyTransaction`, `StateDB`, `TrieUpdate`).
- **Phạm vi bảo đảm:** Chưa phát hiện đường ảnh hưởng consensus trong phạm vi grep; cần user xác nhận trước khi triển khai code.

### 3.3 Cơ chế Fallback đọc Pebble DB đã tồn tại sẵn
Mã nguồn tại `execution/pkg/blockchain/blockchain.go` (dòng 665-685 và dòng 710-735) đã cài đặt sẵn cơ chế Fallback đọc từ đĩa:
```go
func (bc *BlockChain) GetBlockNumberByTxHashFast(txHash common.Hash) (uint64, bool) {
    // 1. Kiểm tra L1 In-Memory Cache
    if value, ok := bc.txHashToBlockNumber.Load(txHash); ok {
        if cached, ok := value.(cachedUint64); ok {
            if time.Since(cached.addedAt) <= mappingCacheTTL {
                return cached.value, true
            }
            bc.txHashToBlockNumber.Delete(txHash)
        }
    }

    // 2. FALLBACK L2: Đọc trực tiếp từ Pebble DB storageMapping
    key := []byte(txHashPrefix + txHash.Hex())
    data, err := bc.storageManager.GetStorageMapping().Get(key)
    if err == nil && data != nil && len(data) == 8 {
        blockNumber := binary.BigEndian.Uint64(data)
        // Nạp ngược lại vào L1 cache
        bc.txHashToBlockNumber.Store(txHash, cachedUint64{
            value:   blockNumber,
            addedAt: time.Now(),
        })
        return blockNumber, true
    }

    return 0, false
}
```

---

## 4. Phân Tích Tính Xác Định (Determinism) & Đánh Giá Tác Động Fork

### 4.1 Phạm vi phân tích và rủi ro ảnh hưởng phân nhánh (Fork)
- **Đánh giá rủi ro Fork:** Rất thấp trong phạm vi phân tích tĩnh hiện tại.
- **Cơ sở đánh giá kỹ thuật:**
  1. **Không nằm trong Consensus Engine:** Hai cấu trúc này không tham gia vào bất kỳ hàm tính toán State Root, Block Header Hash, Quorum Certificate, hay DAG Ordering nào (đã xác nhận qua grep toàn bộ thư mục `consensus/`).
  2. **Không nằm trong Tx Execution Function:** EVM và Account State transition chỉ truy cập `account_state_db` và `trie` (NOMT/Merkle trie). Chúng không gọi `GetBlockNumberByTxHashFast`.
  3. **Tính nhất quán dữ liệu (Data Consistency):** Khi một entry bị evict khỏi RAM do đạt giới hạn bộ nhớ hoặc quá hạn TTL, hàm `GetBlockNumberByTxHashFast` tự động fallback đọc Pebble DB (`storageMapping`) và trả về cùng một giá trị `blockNumber` duy nhất đã được commit bền vững.
  4. **Dữ liệu có thể tái dựng:** Dữ liệu mapping có thể tái dựng bằng cách quét lại các blocks trong `BlockDatabase` (hàm `RebuildTxMappings` trong `mapping_rebuild.go`).
- **Giới hạn và lưu ý an toàn:** Đây là kết luận dựa trên phân tích phạm vi grep trong codebase hiện tại. Trước khi can thiệp vào bất kỳ struct nào trong `execution/pkg/blockchain/`, cần có sự phê duyệt chính thức từ User.

---

## 5. Đề Xuất Các Phương Án Kỹ Thuật (Architecture Options)

### Phương Án 1: Bounded LRU Cache với Fallback Pebble DB (Khuyến Nghị)
- **Cơ chế:** Thay thế Go map bằng một bộ đệm LRU có giới hạn cứng (ví dụ `Capacity = 65,536` phần tử).
- **Cách thức hoạt động:**
  - Khi thêm entry mới (`Store`): Nếu kích thước vượt 65,536, phần tử ít được sử dụng nhất (Least Recently Used) tự động bị loại khỏi RAM.
  - Khi đọc (`Load`): Nếu hit $\rightarrow$ đưa lên đầu danh sách LRU. Nếu miss $\rightarrow$ đọc Pebble DB và nạp vào LRU.
  - Loại bỏ goroutine `Prune()` chạy vòng lặp $O(N)$ định kỳ mỗi 1 phút.
- **Ưu điểm:**
  - Bộ nhớ RAM bị chặn cứng ở mức trần cố định: $\le 10$ MB cho mỗi bảng.
  - Giảm thiểu stop-the-world và lock contention.
  - Tận dụng triệt để cơ chế Fallback Pebble DB sẵn có.
- **Kỹ thuật thực hiện:**
  - Sử dụng thư viện chuẩn hoặc cấu trúc LRU lock-free / striped mutex đã qua kiểm định (ví dụ `hashicorp/golang-lru/v2` hoặc mảng vòng fixed-size 2-generation).

### Phương Án 2: Bounded Two-Generation Ring Map (Zero External Dependency)
- **Cơ chế:** Dùng 2 maps: `current` và `old`.
  - Giới hạn: Khi `current` đạt `MaxEntries` (ví dụ 50,000), tráo con trỏ: `old = current`, `current = make(map, MaxEntries)`. Map `old` trước đó tự động được giải phóng cho GC.
- **Ưu điểm:**
  - Mã nguồn Go thuần, không thêm package bên ngoài.
  - Rất nhanh, phân bổ một lần, dọn dẹp theo batch cực sạch mà không tốn công việc dịch chuyển con trỏ LRU.
  - Entry tồn tại tối đa trong 2 chu kỳ nạp, đảm bảo luôn có trong RAM trong suốt đợt blast hiện tại.

### Phương Án 3: Loại bỏ L1 RAM Cache, dựa trực tiếp vào Pebble DB
- **Cơ chế:** Bỏ hẳn `txHashToBlockNumberMap` và `ethHashMapBlsHashMap` trong RAM. Mọi truy vấn `GetBlockNumberByTxHash` đọc trực tiếp qua `bc.storageManager.GetStorageMapping().Get(key)`.
- **Ưu điểm:**
  - Đạt chuẩn tối thượng KISS & YAGNI.
  - Pebble DB vốn đã có sẵn **Block Cache** nội tại cực mạnh viết bằng C/Go với cơ chế LRU quản lý chặt chẽ theo dung lượng byte cấu hình trước (ví dụ 64 MB hoặc 128 MB).
- **Rủi ro:**
  - Có thể tăng độ trễ truy vấn RPC của các client đọc receipt liên tục thêm vài micro-giây (Pebble DB in-memory cache lookup tốn ~1-2 $\mu$s so với Go map ~50 ns).

---

## 6. Phân Tích Rủi Ro & Kế Hoạch Kiểm Thử (Risk & Test Plan)

| Rủi Ro | Mức Độ | Biện Pháp Kiểm Soát |
| :--- | :---: | :--- |
| **Race Condition khi concurrent Read/Write** | Trung bình | Sử dụng Read-Write Mutex bảo vệ hoặc lock nội bộ của LRU; chạy với cờ `go test -race`. |
| **Suy giảm thông lượng RPC Receipt** | Thấp | Benchmark RPC read throughput trước và sau khi áp dụng bounded cache. |
| **Dung lượng Pebble DB tăng nhẹ** | Rất thấp | Pebble DB vốn dĩ đã lưu toàn bộ mapping này từ trước, không có thêm dữ liệu mới nào phát sinh. |
| **State Drift / Fork** | **Rất thấp** | Theo phạm vi phân tích tĩnh: dữ liệu chỉ phục vụ RPC/Debug API, không liên quan consensus hay EVM execution. Cần user xác nhận trước khi sửa code. |

---

## 7. Quyết Định & Kiến Nghị (Decision & Recommendation)

1. **Tuân thủ nguyên tắc Scope Gating (AGENTS.md Part 1 & Part 2):**
   - Không tự ý sửa mã nguồn cốt lõi trong pull request điều tra hiệu năng này.
   - Tài liệu thiết kế này được đệ trình để User và Tech Lead xem xét và duyệt phương án trước khi triển khai code.
2. **Khuyến nghị chọn Phương Án 2 (Bounded Two-Generation Ring Map) hoặc Phương Án 1:**
   - Đảm bảo giới hạn RAM cố định $\le 10$ MB.
   - Giữ nguyên hiệu năng truy vấn nhanh cho RPC.
   - Đáp ứng triệt để yêu cầu "Bounded Concurrency & Bounded Memory" của hệ thống Metanode Core.
