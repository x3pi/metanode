# Tiêu Chí & Giả Thuyết Đăng Ký Trước (Pre-registered Criteria & Hypotheses)
## Điều Tra Nút Thắt Hiệu Năng TPS Cụm 4 Validator (2026-10-08)

**Tài liệu tham chiếu:** `note/plan_tps_investigation_20261008.md`, `note/perf_secp_tps_20261007.md`, `note/perf_nomt_commit_20261007.md`  
**Commit base:** `0e9cb3319f10bb49d1ac6740d45bb7de88fd932d`  
**Ngày đăng ký:** 2026-10-08  
**Cam kết:** Mọi giả thuyết, tiêu chí đo lường và phương pháp phân tích dưới đây được đăng ký và commit vào Git TRƯỚC KHI thực hiện bất kỳ lệnh benchmark nào. Tuyệt đối không thay đổi tiêu chí sau khi quan sát số liệu thực tế.

---

## 1. Thiết Lập Môi Trường Đo & Phần Cứng

- **Máy chủ thử nghiệm:**
  - CPU: Intel(R) Xeon(R) Platinum 8272CL CPU @ 2.60GHz, 2 Sockets, 2 NUMA nodes, 104 vCPUs vật lý/logic.
  - RAM: 188 GB RAM, 8 GB Swap.
  - HĐH: Linux x86_64 Ubuntu.
- **Cấu hình cụm blockchain:**
  - 4 Validator Mysticeti cô lập (`val0`, `val1`, `val2`, `val3`) sử dụng dải cổng `31xxx`:
    - `val0`: RPC `31646`, TCP Ingest `31200`, P2P `31100`, Metrics `31400`, PProf `31446`
    - `val1`: RPC `31647`, TCP Ingest `31201`, P2P `31101`, Metrics `31401`, PProf `31447`
    - `val2`: RPC `31648`, TCP Ingest `31202`, P2P `31102`, Metrics `31402`, PProf `31448`
    - `val3`: RPC `31649`, TCP Ingest `31203`, P2P `31103`, Metrics `31403`, PProf `31449`
  - Cơ sở dữ liệu: Dựng từ template sạch `/tmp/gate_4val_clean_template` (50,000 tài khoản Secp256k1 tạo sẵn tại genesis, block ban đầu #0, NOMT state trie).
  - Chế độ xác thực: Native Ethereum EIP-1559 (`tx_signature_mode = secp`), chữ ký bật đầy đủ (`SKIP_MEMPOOL_SIG_VERIFY` không đặt).
- **Công cụ đo tải:**
  - `execution/cmd/tool/secp_tps_blast` (bản build từ HEAD hiện tại).
  - Workload: 25,000 giao dịch native EIP-1559, batch 1,000 qua raw TCP stream, rate limit = 0 (unlimited/blast), cờ `-verify-parity`.
- **Số lượt lặp baseline:** Tối thiểu 5 lượt độc lập (Wipe sạch cụm từ template sạch trước mỗi lượt).
- **Chỉ số thu thập mỗi lượt:**
  - Throughput (Effective TPS), Ingest TPS, Commit Duration (s).
  - Receipt Latency: P50, P90, P95, P99.
  - Số blocks sản sinh, số txs/block.
  - Peak CPU (%) và Peak RSS (MB) trên toàn cụm.
  - Trạng thái Zero-Fork (xác nhận 100% khớp Block Hash và State Root trên 4 node).

---

## 2. Các Giả Thuyết Khoa Học (Hypotheses)

### Giả thuyết H1 — Giao diện Rust Consensus → Go Execution là nút thắt tuần tự (Serial Delivery Bottleneck)
- **Nội dung:** Rust Mysticeti chuyển giao các subdag đã commit sang Go thông qua kênh/FFI tuần tự từng block một (`send_committed_subdag` hoặc tương đương chờ Go xử lý xong mới đẩy tiếp). Trong khi Go bận xử lý block $N$, Rust có thể bị block hoặc Go không chuẩn bị/verify trước được block $N+1$.
- **Dấu hiệu kiểm chứng:** Khoảng cách thời gian (idle time) giữa các commit liên tiếp, tỷ lệ thời gian Rust đứng chờ Go phản hồi.

### Giả thuyết H2 — Thực thi Block trong Go chiếm phần lớn đường găng (Go Execution Critical Path)
- **Nội dung:** Thời gian commit ~4.0s cho 25,000 txs (~3-4 blocks) tương đương mỗi block 7,000–10,000 txs mất ~1.0–1.2s. Đường găng bao gồm:
  - `[SIG-ENFORCE]`: ~1.2–1.5 µs/tx (~12–15 ms/10k txs) → Đã chứng minh không phải nút thắt.
  - Block-STM parallel execution & Conflict resolution.
  - Compute State Root & Account Trie updates.
  - NOMT commit & fsync (~11–18 ms/1k keys).
  - Block header hashing, raw block database write & Receipt indexing.
- **Dấu hiệu kiểm chứng:** Phân rã microsecond/millisecond cho từng khâu trong pipeline thực thi của Go. Khâu nào chiếm >40% tổng thời gian block sẽ được xác định là nút thắt chính.

### Giả thuyết H3 — Độ trễ đồng thuận DAG (Mysticeti Propose-to-Commit Latency)
- **Nội dung:** Mysticeti cần 3 rounds DAG để commit một block leader. Tần suất propose và trao đổi votes giữa 4 node trên cùng 1 máy bị trễ do nhịp commit vote hoặc lock contention trong consensus state (`GLOBAL_TX_CACHE`, `dag_state`).
- **Dấu hiệu kiểm chứng:** Thời gian từ lúc tx vào DAG block đến lúc leader round commit; lock contention profile trong Rust (`perf record`, mutex wait time).

### Giả thuyết H4 — Tranh chấp tài nguyên do Co-location (Co-location Resource Contention)
- **Nội dung:** 4 validator chạy chung trên một host 104 vCPU gây tranh chấp CPU context switch, L3 cache eviction và đĩa I/O khiến TPS thấp hơn so với khi tài nguyên được phân bổ cách ly.
- **Dấu hiệu kiểm chứng:** Thử nghiệm giới hạn số validator hoặc CPU pinning (`taskset`) để đánh giá biến thiên latency và throughput.

---

## 3. Tiêu Chí Nghiệm Thu Định Lượng & Bất Biến

1. **Zero-Fork Tuyệt Đối:**
   - 100% khớp Block Hash và State Root trên cả 4 validator sau mọi lượt chạy.
   - Tuyệt đối không dùng `sleep()`, `timeout()`, hay giả định để dispatch commit.
2. **Tính Trung Thực & Bằng Chứng:**
   - Tất cả con số xuất phát từ log thực tế được ghi vào thư mục `note/evidence/tps_investigation_20261008/`.
   - File `MANIFEST.json` chứa danh sách lệnh, tham số, mã băm SHA256 của từng file log và file báo cáo JSON.
3. **Phân Rã Thời Gian Chi Tiết (Time Breakdown):**
   - Xác định rõ tỷ lệ % thời gian trên đường găng của từng giai đoạn: Ingest, Consensus DAG, Delivery Rust→Go, Execution (Sig, Block-STM, State, NOMT, DB).
   - Đưa ra kết luận có cơ sở định lượng về nút thắt lớn nhất (Bottleneck #1) và nút thắt thứ hai (Bottleneck #2).
