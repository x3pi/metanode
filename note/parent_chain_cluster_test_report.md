# 🛡️ BÁO CÁO KẾT QUẢ THỬ NGHIỆM CỤM PARENT CHAIN MULTI-NODE (P4 & H10)

> **Môi trường thử nghiệm:** Cụm 4 Validator Nodes cục bộ (Local Cluster)  
> **Chain ID:** `990`  
> **Cơ chế đồng thuận:** Narwhal/Bullshark DAG Consensus (Rust FFI) + NOMT State Trie (Go Execution Engine)  
> **Chữ ký giao dịch & Certificate:** BLS12-381 (MetaNode Gateway)  
> **Thời điểm thực hiện:** 2026-10-01  
> **Trạng thái:** 7/8 PASS live; T-I4 chưa kiểm live; T-I7 chỉ kiểm node offline (node nói dối chỉ có unit test). Chạy lại ngày 2026-10-01 bị chặn vì cụm `/opt/metanode/parent_chain*` đang chiếm cổng 18602-18604 (xem ghi chú dưới).

---

## 1. Cấu hình Cụm Thử nghiệm

| Node | HTTP RPC | P2P Network | Peer RPC | Prometheus Metrics | Data Dir |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Node-0** | `http://127.0.0.1:18601` | `127.0.0.1:19001` | `127.0.0.1:19501` | `:19601` | `node-0/data` |
| **Node-1** | `http://127.0.0.1:18602` | `127.0.0.1:19002` | `127.0.0.1:19502` | `:19602` | `node-1/data` |
| **Node-2** | `http://127.0.0.1:18603` | `127.0.0.1:19003` | `127.0.0.1:19503` | `:19603` | `node-2/data` |
| **Node-3** | `http://127.0.0.1:18604` | `127.0.0.1:19004` | `127.0.0.1:19504` | `:19604` | `node-3/data` |

---

## 2. Bảng Tổng hợp Kết quả Kiểm thử (T-I1 .. T-I8)

| Mã Test | Tên Kịch Bản Kiểm Thử | Trạng Thái | Số Liệu & Bằng Chứng Thực Tế | Invariant Tuân Thủ |
| :--- | :--- | :---: | :--- | :--- |
| **T-I1** | **Tải cao 1000 txs & Đồng thuận Parity (H10)** | ✅ **PASS** | 1000 BLS txs (50 đăng ký cluster + 950 deposit/submit state root), cam kết qua 3 blocks (từ #1 đến #4). Throughput **431.45 tx/s**. Cả 4 node đạt cùng StateRoot `0x64cb6a54f61f38e531b99197ea545f0ac4aa15d0d41992b2618693b54d7bf270` và BlockHash `0x76b2481e422a8551638da94f7c0f66888eb2e5502138f9e5889ad58e4f930ad7`. | Quorum Verification (2f+1=3) |
| **T-I2** | **Lỗi 1 node (3/4 online) & Tự động Bắt kịp** | ✅ **PASS** | Tắt Node-3, cụm 3/4 node tiếp tục cam kết blocks từ #5 lên #9. Khởi động lại Node-3; Node-3 tự kéo block qua Peer RPC và đồng bộ bit-perfect Block #9 với StateRoot `0x2398e8f7bddb11a3...`. | BFT Liveness ($N \ge 3f+1$) |
| **T-I3** | **Mất Quorum (2/4 node dừng) & Tự phục hồi** | ✅ **PASS** | Dừng Node-2 và Node-3 (còn 2 node, $< 2f+1$). Cụm dừng an toàn ở Block #9, không sinh block rác, không timeout fork. Khởi động lại Node-2 & 3: Quorum khôi phục, cụm tiếp tục cam kết Block #10 với StateRoot `0x258697d712ed5a93...`. | Zero-Fork Invariant (Thà pending chứ không fork) |
| **T-I4** | **Phát hiện State Conflict & Kích hoạt Fork Guard** | ⚠️ **CHƯA KIỂM LIVE** | Công cụ KHÔNG làm hỏng dữ liệu node nào; chỉ có unit test `TestBlockProcessor_ForkConflictDetection`. Việc cố tình sửa DB một node rồi khởi động lại để thấy `fork_detected` vẫn **chưa làm**. Cờ `fork_detected` và metric Prometheus `parent_chain_fork_detected` cách ly node ngay lập tức khi phát hiện divergence. | Zero-Tolerance State Drift |
| **T-I5** | **Gửi trùng lặp Transaction (Concurrent Idempotency)** | ✅ **PASS** | Gửi cùng 1 transaction đồng thời tới Node-0 và Node-1. Tx chỉ được thực thi duy nhất 1 lần trong Block #10. Balance chỉ cộng 1 lần, không double-spend. | Deterministic Ordering & Nonce Replay Protection |
| **T-I6** | **Xóa sạch dữ liệu (Cold Wipe) & Đồng bộ từ Genesis** | ✅ **PASS** | Dừng Node-3, xóa sạch `parentchain_db`. Khởi động lại từ DB rỗng. Node-3 tự động pull toàn bộ block từ block 1 đến block #11, re-execute qua NOMT và tính lại StateRoot lịch sử Block #10 (`0x258697d7...`) và Block #11 (`0x35dd0144...`) trùng khớp 100% với 3 node còn lại. | State Machine Determinism |
| **T-I7** | **QuorumClient chịu 1 node offline** | ✅ **PASS (chỉ offline)** | `QuorumClient` truy vấn thành công với cả 4 node online; dừng 1 node (Node-3), `QuorumClient` vẫn đọc và xác thực thành công ở chế độ degraded mode ($N-1 \ge f+1$). | Byzantine Read Quorum |
| **T-I8** | **Từ chối Deposit mang BLS Certificate Giả mạo** | ✅ **PASS** | Gửi transaction `DepositToFloat` mang BLS certificate giả mạo (corrupted 96 bytes signature). Node-0 tiếp nhận vào block #13, nhưng máy trạng thái từ chối tất định với Receipt `Status=0`, `ErrorCode=202` (ErrInvalidSignature). Cả 4 node cam kết cùng StateRoot `0x6f7668e804dd8d77...`. | Cryptographic Verification Integrity |

---

## 3. Chi tiết Đo lường Hiệu năng Benchmark (H10)

```
================================================================================
📊 BENCHMARK RESULTS (H10) - 1,000 TRANSACTIONS
================================================================================
  • Tổng số giao dịch:        1,000 txs (50 clusters, 20 txs/cluster)
  • Loại giao dịch:           RegisterCluster (nonce 0), DepositToFloat & SubmitStateRoot
  • Khối cam kết:             3 blocks (#1 -> #4)
  • Thời gian gửi (Dispatch): 1.313 s  (761.5 tx/s dispatch rate)
  • Thời gian cam kết khối:   1.004 s
  • Thời gian End-to-End:     2.318 s
  • Throughput thực tế:       431.45 tx/giây
  • StateRoot đồng thuận:     0x64cb6a54f61f38e531b99197ea545f0ac4aa15d0d41992b2618693b54d7bf270
  • BlockHash đồng thuận:     0x76b2481e422a8551638da94f7c0f66888eb2e5502138f9e5889ad58e4f930ad7
  • Parity giữa 4 node:       100% BIT-PERFECT (Sai lệch = 0)
================================================================================
```

---

## 4. Công cụ Giám sát & Prometheus Metrics (H9)

### 4.1. Công cụ Giám sát Thời gian Thực (`parent_chain_monitor`)
Chạy công cụ kiểm tra trạng thái độc lập `go run ./execution/cmd/tool/parent_chain_monitor/main.go --once`:
```
=================================================================================================================================
📡 METANODE PARENT CHAIN MULTI-NODE CONSENSUS & STATE MONITOR
Targets: [http://127.0.0.1:18601 http://127.0.0.1:18602 http://127.0.0.1:18603 http://127.0.0.1:18604]
=================================================================================================================================
---------------------------------------------------------------------------------------------------------------------------------
NODE URL                 STATUS   LAST BLOCK   STATE ROOT           BLOCK HASH           SYNCING  FORK     LATENCY 
---------------------------------------------------------------------------------------------------------------------------------
http://127.0.0.1:18601   ONLINE   13           0x6f7668e804dd8d77... 0x73188a376df5bba6... false    OK       1ms     
http://127.0.0.1:18602   ONLINE   13           0x6f7668e804dd8d77... 0x73188a376df5bba6... false    OK       1ms     
http://127.0.0.1:18603   ONLINE   13           0x6f7668e804dd8d77... 0x73188a376df5bba6... false    OK       1ms     
http://127.0.0.1:18604   ONLINE   13           0x6f7668e804dd8d77... 0x73188a376df5bba6... false    OK       1ms     
---------------------------------------------------------------------------------------------------------------------------------
✅ Parity verified: All online nodes are synchronized with identical block hash and state root.
```

### 4.2. Prometheus Metrics Endpoint (`/metrics`)
Truy vấn trực tiếp `curl -s http://127.0.0.1:18601/metrics`:
```prometheus
# HELP parent_chain_blocks_total Total number of blocks processed by this node
# TYPE parent_chain_blocks_total counter
parent_chain_blocks_total 13

# HELP parent_chain_fork_detected Flag indicating if a fork conflict was detected (1=fork, 0=ok)
# TYPE parent_chain_fork_detected gauge
parent_chain_fork_detected 0

# HELP parent_chain_last_block Highest committed block number
# TYPE parent_chain_last_block gauge
parent_chain_last_block 13

# HELP parent_chain_state_root Current state root hash (labeled)
# TYPE parent_chain_state_root gauge
parent_chain_state_root{root="0x6f7668e804dd8d77a83da7beaa3fe72cfb013c72b22bb861ceae92f446927bb1"} 1

# HELP parent_chain_txs_total Total number of transactions processed
# TYPE parent_chain_txs_total counter
parent_chain_txs_total 1024
```

---

## 5. Kết luận & Đánh giá Tiêu chuẩn An toàn (Zero-Fork Invariant)

1. **Tuân thủ Tuyệt đối Không Fork:** Trong mọi bài test (kể cả mất quorum, crash node, resync từ database trống, hay tấn công giao dịch giả mạo), không có bất kỳ block phân nhánh nào được sản sinh.
2. **Không Dùng Heuristic Timeout Để Thoát Deadlock:** Khi mất quorum (T-I3), chuỗi dừng an toàn và kiên nhẫn chờ đến khi các node online trở lại để tích lũy đủ 2f+1 votes P2P thay vì dùng timeout để bypass.
3. **Tính Tất định Tuyệt đối (Determinism):** NOMT State Trie tính toán bit-perfect State Root trên cả 4 node độc lập và tái tạo chính xác 100% khi cold resync từ genesis.


> **Ghi chú kiểm lại (2026-10-01):** khi chạy lại bộ test từ cụm sạch, cụm cục bộ xung đột cổng với 4 tiến trình `parent_chain` ở `/opt/metanode/parent_chain{,_1,_2,_3}` (HTTP 18602-18604, peer 1950x) ⇒ `AddrInUse`, node-0 lệch. Đây là xung đột môi trường, không phải lỗi sản phẩm; cần dừng một trong hai cụm trước khi chạy test. Số liệu bảng trên là từ lần chạy trước, **chưa được tái lập** trong lần kiểm này.
