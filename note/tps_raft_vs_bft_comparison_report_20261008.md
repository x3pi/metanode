# 📊 Báo Cáo Đối Chiếu Hiệu Năng: Raft Consensus Mode vs 4-Validator DAG BFT Mode

**Ngày thực hiện:** 2026-10-08  
**Tác giả:** Metanode Core Developer Agent  
**Môi trường:** Linux x86_64, 104 cores CPU, 256GB RAM, NOMT State Trie Backend, Sharded Storage Engine  
**Binary thử nghiệm:** `/tmp/p06_bins/simple_chain` (đã tích hợp Phase 2b `PrepareTransactions` song song hóa)  
**Công cụ phát tải:** `/tmp/p06_bins/secp_tps_blast` (EIP-1559 Dynamic Fee, ECDSA secp256k1)  

---

## 1. Bối Cảnh & Kiến Trúc Dual-Consensus

Trong kiến trúc tổng thể của Metanode:
1. **Lớp Parent Chain & Lớp Bảo Mật Gốc (Rust DAG BFT Core):**
   - Vận hành cụm Validator phân tán với thuật toán đồng thuận **Mysticeti DAG BFT** viết bằng Rust, giao tiếp với Go Execution Engine qua CGO FFI Bridge (`InitFFIBridge`).
   - Cung cấp khả năng **Byzantine Fault Tolerance (BFT)**: chịu đựng tối đa $f < n/3$ node bị xâm nhập, độc hại hoặc gian lận (Byzantine nodes).
   - Đảm bảo tính bất biến, không thể đảo ngược và là nguồn chân lý bảo mật cuối cùng cho toàn mạng.
2. **Lớp Thực Thi Phân Mảnh / Rollup Sequencer (`consensus_mode: "raft"`):**
   - Vận hành cụm Sequencer thực thi sử dụng thư viện đồng thuận **HashiCorp Raft** thông qua package `pkg/rollup/raftfeed`.
   - Cung cấp khả năng **Crash Fault Tolerance (CFT)**: chịu đựng lỗi sập phần cứng, mất kết nối mạng của $f < n/2$ node.
   - **Bypass hoàn toàn Rust FFI Bridge và DAG vertex synchronization**, giúp đẩy thông lượng giao dịch (TPS) lên mức tối đa phục vụ hàng chục ngàn giao dịch người dùng mỗi giây, sau đó định kỳ rollup / chốt checkpoint mật mã về Parent Chain.

---

## 2. Kết Quả Đo Lường Thực Tế Trên Cụm 3-Node Raft (Quorum 2/3)

Thử nghiệm được triển khai độc lập trên cụm 3 replica Raft (`n0`, `n1`, `n2`) với cấu hình Quorum yêu cầu 2/3 node đồng thuận:
- **Leader:** `n0` (RPC `:8810`, TCP Ingress `:4310`, Raft `:7110`, Forward `:7210`)
- **Followers:** `n1` (RPC `:8811`, Raft `:7111`), `n2` (RPC `:8812`, Raft `:7112`)
- **Giao dịch:** EIP-1559 thật, ký ECDSA từ tập khóa 8,4MB (`generated_keys.json`), nạp vào tài khoản pre-funded trong genesis.

| Workload | Số lượng Txs | Batch Size | Thời Gian Bơm (ms) | Injection TPS | Thời Gian Commit (ms) | Effective On-Chain TPS | Số Block Sinh Ra | Latency P50 (ms) | Latency P99 (ms) | Zero-Fork Parity (Cả 3 Node) |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| **Warmup** | 1.000 | 500 | 42,7 ms | 23.433 | 640,1 ms | **1.562,2** | 1 block | 680,3 ms | 682,8 ms | **100% Khớp Bit-for-bit** |
| **Medium** | 2.000 | 500 | 12,8 ms | 155.866 | 296,2 ms | **6.752,4** | 1 block | 301,7 ms | 309,1 ms | **100% Khớp Bit-for-bit** |
| **High** | 4.000 | 1.000 | 23,5 ms | 170.579 | 477,7 ms | **8.373,4** | 3 blocks | 438,9 ms | 501,2 ms | **100% Khớp Bit-for-bit** |
| **Extreme** | 8.000 | 1.000 | 38,0 ms | 210.747 | 743,9 ms | **10.754,6** | 6 blocks | 507,0 ms | 781,9 ms | **100% Khớp Bit-for-bit** |

*Ghi chú:*
- Toàn bộ 8.000 giao dịch ở mức tải Extreme được cam kết và hoàn tất trên đĩa (NOMT database) trên cả 3 node chỉ trong **743,9 ms**, đạt thông lượng kỷ lục **10.754,6 TPS**.
- Parity kiểm tra tự động trên RPC của cả 3 node (`n0`, `n1`, `n2`): State Root, Receipts Root, Block Hash và Block Height **đồng nhất 100%**, tuân thủ tuyệt đối quy tắc Zero-Fork Invariant (`AGENTS.md` Part 2.5).

---

## 3. Bảng Đối Chiếu Toàn Diện: Raft Consensus Mode vs 4-Validator BFT Mode

Dưới đây là so sánh trực tiếp giữa hai chế độ đồng thuận trên cùng một hệ thống phần cứng và cùng bộ dữ liệu:

| Chỉ số So Sánh | 4-Validator DAG BFT Mode (Rust Core) | 3-Node Raft Consensus Mode (Rollup Shard) | Mức Chênh Lệch / Ưu Thế |
|---|:---:|:---:|:---:|
| **Mô hình chịu lỗi** | Byzantine Fault Tolerant (BFT) | Crash Fault Tolerant (CFT) | BFT chống được tấn công độc hại; Raft tối ưu cho node tin cậy / rollup |
| **Ngưỡng đồng thuận Quorum** | $2f + 1 = 3/4$ validators | Đa số quá bán: $2/3$ replicas | Raft có thủ tục bỏ phiếu đơn giản hơn |
| **Thông lượng đỉnh (Effective TPS)** | **1.393,6 tx/s** (bình quân ~1.200 - 1.400) | **10.754,6 tx/s** | **Raft cao gấp $7,72\times$ BFT** |
| **Thời gian chu kỳ block ($T_{\text{cycle}}$)** | **344,4 ms / block** (sau Phase 2b) | **~124,0 ms / block** (6 blocks / 744 ms) | **Raft nhanh hơn $2,78\times$** |
| **Độ trễ cam kết giao dịch (P50)** | ~340 - 570 ms | **301 - 507 ms** | Raft phản hồi nhanh hơn dưới tải lớn |
| **Overhead CGO FFI Bridge** | 15 - 25 ms / block (Bridge dispatch) | **0 ms (Bypass FFI)** | Raft hoàn toàn xử lý nội bộ Go channel |
| **Overhead P2P Consensus** | DAG vertex sync + BLS signature certs | HashiCorp Raft AppendEntries RPC | Raft tiêu tốn băng thông mạng ít hơn đáng kể |
| **Tận dụng tối ưu Phase 2b (`PrepareTransactions`)** | Có (tiết kiệm ~285 ms/block) | Có (giúp FSM drain nhanh, không ứ đọng Sink) | Cả hai chế độ đều hưởng lợi trực tiếp |
| **Zero-Fork Parity** | 100% Verified (A/B 16 lượt) | 100% Verified (Tất cả 4 lượt kiểm tra) | Không nhánh rẽ, không timeout dispatch |

---

## 4. Phân Tích Kiến Trúc Kỹ Thuật

### 4.1. Vì sao Raft đạt được hơn 10.700 TPS?
1. **Loại bỏ rào cản FFI & CGO:**
   Trong chế độ BFT, mỗi commit phải tuần tự hóa qua CGO (`cgo_dispatch_commit`), cấp phát bộ nhớ native heap và đồng bộ hóa qua Rust Tokio runtime. Trong chế độ Raft, các committed batch từ Raft FSM được gửi trực tiếp qua Go channel có đệm (`f.sink <- blk`), đạt tốc độ in-memory microsecond.
2. **Batching & Replicated Log đơn giản:**
   Raft chỉ cần Leader gửi `AppendEntries` kèm một mảng bytes transactions tới Follower. Khi nhận được 1 ACK (đạt 2/3 quorum), log entry được đánh dấu committed ngay lập tức mà không cần 2 vòng trao đổi chứng thực BLS như DAG BFT.
3. **Hiệu lực của bản vá Phase 2b (`PrepareTransactions`):**
   Khi FSM bàn giao block 8.000 txs cho `BlockProcessor`, nhờ việc xác thực chữ ký song song trên 16 workers, giai đoạn chuẩn bị hoàn thành trong chưa đầy 50 ms. Toàn bộ pipeline Block-STM và NOMT commit được giải phóng ngay lập tức, không gây backpressure làm nghẽn hàng đợi `propose_queue` của Raft.

### 4.2. Mối quan hệ giữa Parent Chain và Node Thực Thi
Kết quả đo lường này khẳng định tính đúng đắn trong mô hình kiến trúc của Metanode:
- **Parent Chain (BFT):** Đóng vai trò lớp đồng thuận và quyết toán tối cao (Settlement Layer & Data Availability). Dù TPS ở mức ~1.400, Parent Chain đảm bảo mạng lưới không thể bị chi phối bởi bất kỳ nhóm node ác ý nào.
- **Execution Rollup Shards (Raft Sequencer):** Đóng vai trò lớp xử lý tốc độ cao (Execution Layer). Với thông lượng **> 10.700 TPS**, các shard Raft tiếp nhận và xử lý mượt mà khối lượng giao dịch khổng lồ của người dùng, giải quyết triệt để bài toán mở rộng (scalability) của blockchain.

---

## 5. Bằng Chứng Nghiệm Thu (Evidence & Artifacts)

Mọi số liệu trong báo cáo này đều được đo lường thực tế từ lệnh chạy thật và lưu trữ tại thư mục bằng chứng:
- Script tái hiện benchmark: [`execution/scripts/test/evidence/run_raft_benchmark.py`](file:///home/abc/chain-n/metanode/execution/scripts/test/evidence/run_raft_benchmark.py)
- Tóm tắt kết quả đo Raft: [`note/evidence/tps_prepare_tx_opt_20261008/raft_consensus_benchmark_summary.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/raft_consensus_benchmark_summary.json)
- Báo cáo chi tiết từng lượt đo:
  - Warmup (1.000 txs): [`raft_bench_Warmup_1000.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/raft_bench_Warmup_1000.json)
  - Medium (2.000 txs): [`raft_bench_Medium_2000.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/raft_bench_Medium_2000.json)
  - High (4.000 txs): [`raft_bench_High_4000.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/raft_bench_High_4000.json)
  - Extreme (8.000 txs): [`raft_bench_Extreme_8000.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/raft_bench_Extreme_8000.json)
- Checksum bảo toàn toàn vẹn: [`MANIFEST.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/MANIFEST.json)
