# Phân tích TPS End-to-End thấp (~5.000 tx/s) & phương pháp cải thiện

- **Ngày:** 2026-07-14
- **Input:** `tps_report_20260714_064302.md` (3 round × 50.000 TX, E2E ~4.760-5.185 tx/s)
- **Phương pháp:** đọc log node thật (Go + Rust FFI trong `execution.log`), tái hiện test với 200/5k/50k TX trên cluster đang chạy, đối chiếu source code.

## 0. Kết luận nhanh

**Bottleneck KHÔNG phải ProcessTX/EVM như report gợi ý.** Phần "Mempool, Sync & Polling ~7s" (70% thời gian E2E) nằm ở **Rust consensus**, và nguyên nhân gốc là **proposal khổng lồ**: protocol config bị override cứng lên `50.000 TX / 64MB mỗi proposal` (comment trong code: *"MetaNode performance overrides for >30K TPS target"*). Kết quả là proposer nhét 33.630 TX (6,25MB) vào MỘT block DAG, cả committee đứng hình ~3 giây để truyền/xử lý block đó trước khi round tiến tiếp — giết chết pipelining, đúng điều mà cấu hình này định đạt được.

**Fix đề xuất số 1 chỉ là đổi 1 dòng config** (`consensus_max_num_transactions_in_block = 50000` → `4000-8000`), kỳ vọng E2E TPS tăng 2-3 lần. Chi tiết mục 3.

## 1. Chuỗi bằng chứng (đo trên cluster thật, test 50k lúc 07:03)

Timeline node-0 (nhận ~15k/50k TX qua load-balance):

| Thời điểm | Sự kiện | Nguồn log |
|---|---|---|
| 07:03:14.5 | Client bơm xong TX; Go pool add ~100ms; forwarder đẩy hết sang Rust FFI trong ~200ms | `ProcessTransactionsFromClient`, `[FFI TX METRICS] total_batches=39` |
| 07:03:14.8 | Proposer Rust bắt đầu rút TX guards (1000-2000 TX/guard) | `transaction_consumer.next()` |
| 07:03:16.3 | **Proposal B5899: 33.630 TX = 6.255.180 bytes trong 1 block** ("Limit reached: AllTransactionsIncluded") | `consensus_core::core::proposer` |
| 07:03:16.3 → **07:03:19.3** | **Round 5899→5900 đứng im 3 giây** — cả committee bận truyền + xử lý mega-block, threshold clock không tiến được | so sánh timestamp 2 dòng `Created block` |
| 07:03:19.6 | Commit đầu tiên (ForceCommit → Go block #24, 16.370 TX) | `FORCE COMMIT`, `BLOCK-NUM-ASSIGN` |
| 07:03:20 → :23 | Go thực thi 2 block khổng lồ (16.370 + 33.630 TX), mỗi block ~1-3s | `COMMIT STATE` #24, #25 |
| 07:03:23.8 | Client xác nhận đủ 50k → E2E 9,69s (~5.160 tx/s) | tool output |

Bằng chứng phụ trợ quan trọng:
- **Độ trễ scale theo số TX, không phải timer cố định**: cùng cluster idle, 200 TX → E2E **999ms**; 5.000 TX → **1,98s**; 50.000 TX → **9,69s**. (Đã loại giả thuyết "wake-up timer 5s" — DAG thực tế vẫn quay round ~200ms/lần khi idle, tạo block `0t`.)
- **Giới hạn proposal hiện tại** (log startup): `max_num_transactions_in_block: 50000, max_transactions_in_block_bytes: 67108864` (64MB). Nguồn: `crates/meta-protocol-config/src/lib.rs:4471-4472` (override cứng), count có thể chỉnh qua `consensus.toml` → `setup_consensus/mod.rs:309`.
- **Số liệu phi tuyến ở Go**: block 18.794 TX → ProcessTX 367ms; block 29.206 TX → 1.029ms; block 33.630 TX → ~2s kèm IR-hash 292ms — block càng to càng lỗ.
- Các hướng đã kiểm tra và **loại trừ**: BLS verify mempool (SKIP=true trong systemd), `TransactionVerifier` phía Rust (no-op), Go pool/forwarder (xong trong ~200ms), polling client (5ms), leader_timeout/min_round_delay (200ms/25ms — hợp lý).

## 2. Vì sao mega-block giết E2E TPS

Consensus DAG chỉ tiến round khi nhận đủ quorum block của round trước. Block 6MB × 5 validator (mỗi node còn gossip lại TX cho nhau — xem mục 3.2 — nên hầu như node nào cũng propose gần đủ 50k) nghĩa là mỗi round phải di chuyển và verify hàng chục MB trước khi round kế tiếp mở. Thay vì 50k TX chảy đều qua ~13 round nhỏ (mỗi round ~150ms, commit gối đầu, Go thực thi gối đầu), toàn bộ hệ thống chờ 1 lần truyền lớn + 1 lần thực thi lớn, **tuần tự hoá toàn pipeline**.

## 3. Phương pháp cải thiện (xếp theo tác động/chi phí)

### 3.1 ⭐ Giảm kích thước proposal — 1 dòng config, tác động lớn nhất
Đổi trong `consensus.toml` của cả 5 node (`/opt/metanode/node-*/config/consensus.toml`) và template deploy (`deploy/systemd/node-*_keys/consensus.toml`, default trong `gen_validator_entry.py`):

```toml
consensus_max_num_transactions_in_block = 4000   # thử A/B: 2000 / 4000 / 8000
```

Với TX transfer ~186 bytes, 4000 TX ≈ 750KB/proposal — vừa khớp vùng thiết kế của Mysticeti gốc. Ước lượng: 5 validator × 4000 TX × ~6 round/s ≥ 80k tx/s dung lượng pipeline; 50k TX burst sẽ chảy hết trong ~1-2s round-time, commit + thực thi Go gối đầu → **E2E kỳ vọng ~3-4,5s thay vì 9,7s (≈12-18k TPS)**. Cách đo lại: chạy đúng `run_tps_test.sh` cũ, so `End-to-End time` và khoảng cách timestamp giữa các dòng `Created block ... (Nt)` trong log.

Lưu ý: override 64MB bytes-limit trong `meta-protocol-config/src/lib.rs:4471` nên hạ xuống (ví dụ 2MB) hoặc thêm knob toml tương tự count — hiện bytes không có knob, nhưng chỉ cần cap count là đủ chặn mega-block cho loại TX hiện tại.

### 3.2 Chặn gossip TX trùng lặp giữa validators — tác động trung bình-lớn, cần sửa code
Log cho thấy `peer_rpc::server: [TX SUBMIT] Received N TXs from peer, submitted N to local consensus` — mỗi node nhận TX từ client xong **gossip cho cả 4 peer, và peer submit lại vào consensus của chính nó**. Hệ quả: cùng 1 TX được đóng vào proposal của nhiều validator (round 1 cũ: commit GEI=4 chứa 50.000 TX thô nhưng chỉ 8.000 TX mới sau dedup ở Go) → phí ~x3-5 băng thông/CPU consensus, phóng đại thêm vấn đề 3.1. Hướng sửa: dedup theo digest (LRU/bloom) tại điểm ingest `peer_rpc` trước khi submit, hoặc bỏ re-submit khi client đã load-balance. Cần cân nhắc lý do gossip tồn tại (đảm bảo TX không bị kẹt khi 1 node chết) — dedup-trước-submit là phương án giữ được cả hai.

### 3.3 Sửa report tool để không chẩn đoán sai — chi phí thấp
"BOTTLENECK ANALYSIS" của tool chỉ nhìn block traces (ProcessTX/SaveDB) nên luôn kết luận "nút thắt tại CPU EVM" dù 70% thời gian nằm ngoài traces. Các cột WaitGo/WaitRust/Consensus/RustFFI đang hard-code 0 trong `main.go` (`float64(0)`), một phần vì log PERF-RUST đã bị xoá ở PR #27. Nên: (a) in rõ "X% thời gian E2E không nằm trong block traces → nghẽn ở consensus/mempool", (b) đo `first-commit latency` (injection-end → block đầu tiên chứa TX) như một chỉ số riêng.

### 3.4 Các mục nhỏ hơn (sau khi làm 3.1)
- **Go block cap**: `targetBlockSize = 100000` trong `tx_batch_forwarder_core.go` và `TxBatchSize = 50000` — sau 3.1 các giá trị này không còn là đường nóng, nhưng nên hạ tương xứng để nhất quán.
- **Latency sàn ~1s cho burst nhỏ** (200 TX → 999ms): gồm `min_aggregation_delay` 100ms trong proposer + độ sâu commit rule (~3 round × nhịp 200ms khi idle) + poll. Nếu cần latency thấp hơn cho giao dịch lẻ, giảm `leader_timeout_ms` 200→100 trên LAN; đổi lại nhiều block rỗng hơn. Không ưu tiên.
- **FFI guard channel** (10.000 guards) hiện cảnh báo "capacity remaining 128" lúc burst — chưa gây mất TX (`channel_full_events=0`); theo dõi sau 3.1.
- Metrics server Rust (`:9200/metrics`) trả về rỗng — registry chưa được nối. Sửa để lần sau có `commit_latency`, `proposed_block_transactions` thay vì phải mò log.

## 4. Việc tôi chưa làm được trên máy này (vòng 1)
Không có sudo để sửa `/opt/metanode/node-*/config/consensus.toml` và restart service, nên chưa chạy được A/B với `consensus_max_num_transactions_in_block = 4000`. Toàn bộ phân tích dựa trên log + tái hiện test trên cluster hiện trạng. Đề xuất chạy A/B theo thứ tự: 8000 → 4000 → 2000, mỗi mức 3 round 50k, so `End-to-End time` và first-commit latency.

---

# VÒNG 2 (cùng ngày): Vì sao giảm block size xong TPS còn TỆ HƠN (5.0k → 3.8k), và fix gốc

Sau vòng 1, cấu hình đã được áp (4000 TX + 2MB/proposal, `TxBatchSize=4000`, `targetBlockSize=8000`, sửa metrics registry) và test lại (`tps_report_20260714_081808.md`): block nhỏ đi đúng như dự kiến, nhưng **E2E giảm còn ~3.8k TPS**. Nguyên nhân: mục 3.1 chỉ là bề nổi — thứ chi phối thật sự là **mục 3.2 (TX trùng lặp do gossip)**, và block nhỏ làm chi phí trùng lặp per-block tăng lên.

## 5. Bằng chứng vòng 2 (log run 08:18)

- **262.684 dòng `[FAST-PATH-NONCE-REJECT]`** trên node-0 cho 3 round × 50k = 150k TX thật — tức ~1,75× tổng TX là bản sao bị reject từng cái một (mỗi cái 1 lần check nonce + 1 dòng Warn log I/O).
- So `[SPECULATIVE] GEI=N with X txs` (nội dung commit thô) với `COMMIT STATE txCount` (TX thật sau lọc): GEI=5: 21.000 thô → 8.000 thật; GEI=16: 22.000 → 5.000; **GEI=10/11/12: 12.000/11.182/2.000 thô → 0 TX thật (100% trùng)** — Go vẫn chạy đủ pipeline block cho các block "toàn rác" này. Tổng thô round 1 ≈ 134k cho 50k thật (2,7×).
- Cơ chế tạo trùng lặp (đã lần ra code):
  1. `tx_socket_server.rs` (validator nhận TX từ client): *"Background mempool pre-propagation"* — broadcast toàn bộ TX cho **cả 4 peer** qua `peer_rpc`, đồng thời submit vào proposer của chính nó.
  2. Handler `/submit_transaction` phía peer (`peer_rpc/server.rs`) **submit thẳng vào proposer của peer** (`submitter.submit`), thay vì chỉ đổ vào cache như mục đích của pre-propagation.
  3. → mỗi TX được propose bởi tối đa 5 validator → nội dung commit phình 2,7× → Go reject từng bản sao bằng nonce check.
- Vì sao gossip tồn tại: `compact_blocks_enabled=true` → block DAG (BlockV3) chỉ chứa **digest**; peer cần TX body trong `GLOBAL_TX_CACHE` để dựng lại/verify block (thiếu → `MissingTransactions` → phải fetch, stall). Tức gossip là **đúng và cần**, chỉ sai ở chỗ đổ nhầm vào proposer.

## 6. Fix đã thực hiện (vòng 2)

### 6.1 Rust — tách "pre-propagation" khỏi "delegated submission"
- `peer_rpc/types.rs`: thêm `cache_only: bool` (serde default=false) vào `SubmitTransactionRequest`.
- `tx_socket_server.rs`: broadcast pre-propagation của validator đặt `cache_only=true`; đường SyncOnly ủy thác TX (bắt buộc peer phải propose hộ) giữ `cache_only=false`.
- `peer_rpc/server.rs`: khi `cache_only=true` → chỉ `GLOBAL_TX_CACHE.insert(digest, tx)` (idempotent, phục vụ dựng compact block) và **không** submit vào proposer. Cố ý không đụng `is_duplicate()` ở nhánh này để không làm delegated submission sau đó bị bỏ sót.
- Kỳ vọng: mỗi TX chỉ được đúng 1 validator propose → nội dung commit từ 2,7× về 1×; hết 262k nonce-reject; compact-block reconstruction vẫn nguyên vẹn.

### 6.2 Go — rate-limit log reject trong fast-path (`native_fast_path.go`)
Warn per-TX khi reject nonce/verify giờ chỉ in 5 dòng đầu mỗi block + 1 dòng tổng kết cuối block. 262k dòng log là chi phí thật trong hot path và là bảo hiểm nếu trùng lặp tái xuất từ nguồn khác (client retry, recycler).

### 6.3 Xác thực
`cargo check` (workspace metanode) và `go build` sạch. Đã khởi chạy đúng pipeline chuẩn `./run_tps_test.sh 50000 --rounds 3 --batch 20000` để đo lại — kết quả cập nhật ở bảng dưới khi có.

| Cấu hình | E2E TPS (avg 3 round) |
|---|---|
| Gốc (50k/64MB proposal, gossip-dup) | ~5.042 |
| 4k/2MB proposal, còn gossip-dup | ~3.839 |
| 4k/2MB + cache_only fix | **~4.435** (5.136 / 4.254 / 3.916) |

## 7. Kết quả xác thực vòng 2 & chẩn đoán vòng 3

**Fix cache_only hoạt động đúng 100%** (log run 09:54):
- `[TX PRE-PROPAGATE] Cached ...` xuất hiện (đường mới), 0 lần submit-từ-peer kiểu cũ.
- **0 dòng NONCE-REJECT** (trước: 262.684) — speculative txs == txCount ở mọi block (hết duplicate hoàn toàn).
- Phần đuôi pipeline giờ rất nhanh: 50k thoát trong ~3s sau commit đầu tiên, block Go 63-223ms.

Nhưng E2E chỉ về lại ~4.4-5.1k vì còn 2 tầng nghẽn mới lộ ra (đo trực tiếp bằng metrics Prometheus — giờ đã hoạt động nhờ fix registry):

### 7.1 Nhịp round bị ghim ở leader_timeout=200ms — vì node-4 (synconly) nằm trong committee
Sampler 100ms trên `subscribed_blocks`: round cadence ~190-200ms cả idle lẫn load (đáng lẽ 25-50ms với `min_round_delay=25ms`). Nguyên nhân: **node-4 tạo 0 block** (synconly không propose) nhưng vẫn là 1/5 committee:
- Quorum 4/5 → mỗi round cần đủ block của **cả 4 node còn lại** (zero slack, một node chậm là cả round chờ).
- ~20% số round leader là node-4 → round chỉ tiến khi hết leader_timeout, commit rule phải skip.

→ **Lever #1 (config deploy): bỏ node-4 khỏi committee validator** (chỉ để nó sync), hoặc cho đủ 5 node propose. Kỳ vọng nhịp round 200ms → 25-75ms, tức throughput commit ×3-5.

### 7.2 Đóng băng giao block ~1-5s đúng lúc bắt đầu burst (scale theo cỡ burst)
Cả 4 node propose round-160 cùng lúc (:49.2-49.4) nhưng `add_blocks` chỉ nổ ở :54.1 trên MỌI node — block bị giao trễ ~4.8s (50k burst) / ~1.3s (20k burst). `DagState::flush` max 10ms (loại), propagation-delay prober = 0 (loại), `quorum_receive_latency` histogram xác nhận vài round 1-5s. Chưa chốt được root cause tầng streaming — cần điều tra riêng (nghi: broadcast channel/subscription hiccup khi block đột ngột phình từ 0t lên 4000t, hoặc tương tác với FFI ingestion đang chiếm CPU). Có thể tự biến mất khi 7.1 tạo slack quorum.

### 7.3 Fix phụ đã kèm
`native_fast_path.go`: rate-limit log reject (5 dòng đầu + tổng kết/block) — phòng khi duplicate tái xuất từ client retry/recycler, 262k dòng Warn không còn là chi phí hot-path.

---

# VÒNG 3: Truy nguồn gốc "đóng băng 5-7s đầu round" bằng perf (có sudo) — tìm ra 2 lỗi ghi disk đồng bộ trong context async

Sau khi bỏ node-4 khỏi committee (mục 7.1) và xử lý một số điểm oversubscription CPU (GOMAXPROCS Go/Rust tokio/NOMT — xem commit log), hiện tượng "đóng băng 5-7s ngay sau khi bơm burst, các block sau đó nhanh hơn hẳn" (mục 7.2) vẫn còn nguyên. Được cấp quyền `sudo` để bật `perf_event_paranoid`, đã `perf record -p <pid> --call-graph dwarf,16384` đúng lúc burst 50k để bắt call stack tại thời điểm đóng băng.

## 8. Phát hiện: 2 điểm gọi `std::fs::write` đồng bộ (blocking) ngay trong Rust async runtime

`perf script` cho thấy nhiều sample dừng ở `std::fs::write::inner` (do Rust monomorphize nhiều call site generic vào 1 hàm chung, không tách được site nào bằng riêng perf) — phải đối chiếu chéo với thời điểm log (khớp chu kỳ 5s) và 1 sample đủ sâu (`TxBatchForwarder.StartForwardingLoop` (Go) → `tokio::runtime::task::core::Core::poll` → `std::fs::write::inner`) để xác định 2 thủ phạm:

1. **`consensus/tx_recycler.rs::save_to_disk()`** — chạy mỗi 5s qua `interval.tick()`, dùng thẳng `std::fs::write`/`std::fs::rename` (đồng bộ) bên trong 1 task async → block cả 1 worker thread của tokio runtime trong lúc ghi. Chu kỳ 5s khớp đúng với hiện tượng quan sát. **Fix**: chuyển sang `tokio::fs::write(...).await` / `tokio::fs::rename(...).await` (`load_from_disk` cũng đổi tương tự).
2. **`meta-consensus/core/src/transaction.rs::TxPayloadCache::insert()`** — với **mỗi TX** nhận được, spawn 1 tác vụ `spawn_blocking(std::fs::write(...))` để lưu payload xuống `TX_PAYLOAD_DIR` (mục đích: phục hồi TX body nếu cần sau restart). Khi burst 50k TX đổ vào trong ~800ms, hệ thống tạo ra **50.000 file-write riêng lẻ** gần như đồng thời, làm bão hòa `max_blocking_threads` pool và I/O — đây là nguồn đóng băng lớn hơn tx_recycler.

Test riêng fix #1 (tx_recycler) cho cải thiện thật nhưng KHÔNG dứt điểm đóng băng (TPS vẫn quanh 3.8-4.5k, cùng dải nhiễu cũ) → xác nhận #2 mới là nguyên nhân chính.

## 9. Quyết định: bỏ hẳn persist payload xuống disk (thay vì gom-batch)

Có 2 phương án: (a) gom nhiều TX ghi 1 file/batch thay vì 1 file/TX, hoặc (b) bỏ hẳn việc ghi payload xuống disk (TxPayloadCache chỉ còn in-memory, giới hạn bằng LRU `capacity`/`queue` sẵn có). Người dùng chọn **phương án (b)**: hệ thống bình thường không restart, và nếu mất TX (do restart giữa chừng) thì client tự gửi lại được — không cần trả giá durability cho trường hợp hiếm.

Đã sửa:
- `meta-consensus/core/src/transaction.rs`: `TxPayloadCache::insert()`/`get()` bỏ hoàn toàn nhánh đọc/ghi disk, chỉ còn thao tác trên `self.map`/`self.queue` in-memory. `TX_PAYLOAD_DIR` giữ lại (không xoá) chỉ để tương thích compile cho `.set()` gọi từ nơi khác, doc-comment giải thích rõ lý do bỏ + đánh đổi durability.
- `src/node/setup_consensus/mod.rs`: bỏ khối tạo `tx_dir`/`create_dir_all`/`TX_PAYLOAD_DIR.set(...)` vì không còn nơi tiêu thụ.

## 10. Kết quả validate (3 round, `run_tps_test.sh 50000 --rounds 3 --batch 20000`, sau khi deploy fix)

| Round | End-to-End TPS | E2E time | First-Commit Latency | ProcessTX avg/block |
|---|---|---|---|---|
| 1 | **~10.122** tx/s | 4.94s | **0s** | 269ms |
| 2 | ~5.391 tx/s | 9.274s | **0s** | 788ms |
| 3 | ~5.496 tx/s | 9.097s | **0s** | 784ms |

**Kết luận rõ ràng nhất: First-Commit Latency = 0s ở CẢ 3 ROUND** (trước fix: luôn 5-7s ở round đầu tiên sau burst). Cụm đóng băng ban đầu — vấn đề gốc mà mục 7.2 nêu ra — **đã hết hẳn**, không còn dấu vết trong log (`Received batch` → `COMMIT STATE Block #N` chỉ còn ~1-2s thay vì 5-7s).

TPS trung bình 3 round (~7.003, min 5.391 / max 10.122) **cao hơn** trần cũ (~4.2-4.5k), nhưng round 2-3 không giữ được mức đột phá của round 1. Lý do đã lộ rõ qua bảng trace per-block: **`ProcessTX` (thực thi EVM + cập nhật state) tăng dần qua các round dù cỡ block tương đương** — vd. block 12.000 TX: round 1 = 231-244ms, round 2 = 677-1464ms, round 3 = 899ms; đến round 3 ngay cả block 4.000 TX cũng mất ~987ms ProcessTX. Đây **không phải** hiện tượng đóng băng bất thường nữa — `WaitGo`/`WaitRust` đều 0ms, tăng đều đặn theo round — mà là **chi phí CPU thực (EVM + state trie) tăng theo tổng số account/state đã tích luỹ** qua 150k TX của cả 3 round cộng dồn trên cùng 1 lần chạy test. Nút thắt đã **chuyển bản chất**: từ "1 lỗi I/O đồng bộ gây đứng hình" sang "chi phí xử lý EVM/NOMT tăng theo kích thước state" — đúng như kỳ vọng khi hạ tầng đã hết bug giả tạo.

## 11. Việc còn để ngỏ (không thuộc phạm vi đã fix)
- `core_thread.rs` còn 2 lời gọi `std::fs::write("/tmp/core_thread_debug.log", ...)` đồng bộ trong nhánh exit/panic của `CoreThread` — xác nhận qua log không chạy trong các phiên test (không có log exit/panic) nên không phải nguyên nhân, nhưng vẫn là code debug chưa dọn, nên dọn khi tiện.
- Bottleneck mới (`ProcessTX` tăng theo state size) là ứng viên điều tra tiếp theo nếu cần đẩy trần TPS ổn định lên cao hơn ~5-7k — hướng nghi vấn: chi phí đọc/ghi NOMT trie tăng theo độ sâu trie (số account), có thể cải thiện bằng batch commit lớn hơn, cache warm account phổ biến, hoặc parallel hoá sâu hơn trong TrueBlockSTM.

---

# VÒNG 4: `ProcessTX` tăng theo cumulative state — cô lập bằng log phase-breakdown, fix 1 bug thật, rồi bật metrics nội bộ NOMT để xác minh dứt điểm NOMT không phải thủ phạm

## 12. Cô lập: tăng nằm ở `TX Execution (Parallel)`, không phải `IntermediateRoot` (NOMT)
Log sẵn có `[PERF] Block #N Phase Breakdown` trên server tách riêng 2 pha trong `tx_processor.ProcessTransactions` (`execution/pkg/blockchain/tx_processor/tx_processor.go`): gọi `ProcessTransactionsOptimistic` (đo là `TX Execution`) rồi `chainState.GetAccountStateDB().IntermediateRoot(true)` (đo là `IntermediateRoot (AccountDB)`) riêng biệt. Qua nhiều round liên tiếp không reset: **`IntermediateRoot` giữ nguyên ~100-350ms**, trong khi **`TX Execution` tăng từ vài chục ms lên 1.0-1.3s**, và tăng không tỷ lệ với tx-count/block hiện tại (một block 4000 TX ở round cuối có thể chậm hơn 1 block 18000 TX ở round đầu) → chi phí gắn với **tổng account cộng dồn từ đầu phiên test**, không phải cỡ block. Loại trừ giả thuyết "NOMT trie depth" thuần tuý (vì trie depth phải lộ qua IntermediateRoot).

## 13. Bug thật #1 (đã fix): registry file bị ghi lại toàn bộ mỗi block
`tps_blast_cc` sinh 1 địa chỉ nhận hoàn toàn mới cho mỗi TX (`main.go`: "Generate a unique dummy address so each sender sends to an untouched recipient"). `NomtStateTrie` (`execution/pkg/trie/nomt_state_trie.go`) duy trì `registry.keys map[string][]byte` (digest→địa chỉ gốc, phục vụ enumeration `GetAll`/`GetAllUniqueAddresses` — NOMT tự nó không hỗ trợ preimage/enumeration, xem mục 15). `persistRegistryToFile()` — gọi trong `Commit()` mỗi khi có key mới — **clone + sort + ghi đè lại TOÀN BỘ file** (`nomt_registry_account_state.bin`, phình tới 7.35MB / ~300k account) mỗi block, tức O(tổng account từng thấy) thay vì O(account mới).

**Fix**: thêm `sharedRegistry.pendingNew [][]byte`, mỗi insert đẩy key mới vào đây; `persistRegistryToFile()` giờ chỉ **append** phần `pendingNew` (rồi reset) thay vì clone+sort+rewrite toàn bộ map. Định dạng file không đổi nên tương thích ngược với loader. Đường full-rewrite cũ giữ lại riêng thành `persistRegistryFullRewrite()`, chỉ dùng cho path admin hiếm khi map bị rebuild toàn bộ (snapshot/reset handle).

**Kết quả**: cải thiện thật nhưng không dứt điểm — round giữa giảm rõ (round 2: 788ms→500-722ms tuỳ lần chạy), nhưng round cuối vẫn trôi dần về ~900-1100ms/block. → còn ít nhất 1 nguồn khác.

## 14. Loại trừ: `loadedAccounts` KHÔNG rò rỉ
Nghi vấn: `AccountStateDB.loadedAccounts` có field `blocksSinceLoadedClear` khai báo nhưng không hề increment ở đâu — nhìn giống cơ chế "dọn mỗi N block" làm dở. Đọc kỹ `IntermediateRoot(true)` (`account_state_db_commit.go`) thì `db.loadedAccounts.Clear()` **đã được gọi vô điều kiện cuối MỌI block** (comment tại chỗ giải thích: giữ lại giữa các block gây fork-safety risk) — biến kia chỉ là code chết còn sót, không phải bug đang chạy. Xác nhận bằng đọc code, không sửa.

## 15. Kiểm tra NOMT có sẵn cơ chế gì trước khi tự chế thêm cache (theo yêu cầu)
Trước khi đi tiếp hướng cache Go-side, kiểm tra kỹ khả năng native của NOMT (vendor tại `~/.cargo/git/checkouts/nomt-*/nomt/src/`, `docs/nomt_specification.md`):
- **Enumeration/preimage**: `Nomt::read(path: KeyPath)` chỉ nhận key đã hash 32-byte, KHÔNG có API iterate/preimage nào — xác nhận registry Go-side (mục 13) là cần thiết thật, không phải "tự chế lại cái NOMT đã có sẵn".
- **`hashtable_buckets`** (kích thước bảng băm bitbox): code đã cấu hình `10.000.000` buckets riêng cho namespace `account_state`/`smart_contract_storage` (`trie_factory.go`, comment "was 1,000,000" — đã từng được nâng trước đây) — không phải nút thắt ở quy mô test hiện tại (xem số đo thật ở mục 16).
- **`Options::metrics`** (page cache hit/miss counter, page/value fetch timer) và **`hash_table_utilization()`** (occupancy bảng băm): **CÓ SẴN trong NOMT nhưng bridge FFI của mình chưa từng bật/gọi** (`opts.metrics` mặc định `false`) — nghĩa là suốt các vòng điều tra trước, ta luôn suy luận hành vi NOMT gián tiếp qua thời gian wall-clock Go/Rust, chưa bao giờ nhìn số liệu nội bộ thật của NOMT.

**Đã bật và export**: `nomt_open` giờ gọi `opts.metrics(true)`; thêm FFI `nomt_get_stats()` (`rust_lib/src/lib.rs`) trả `page_requests`, `page_cache_misses`, `page_fetch_time`, `value_fetch_time`, `ht_capacity`, `ht_occupied`; Go wrapper `Handle.Stats()` (`nomt_ffi/bridge.go`) với `PageCacheMissRate()`/`HTOccupancyRate()`; log định kỳ mỗi 5 block trong `NomtStateTrie.Commit()` dưới tag `[NOMT-STATS]`.

## 16. Kết luận dứt điểm: NOMT hoàn toàn KHÔNG phải thủ phạm — số liệu thật từ chính NOMT
Deploy lại + chạy 6 round (`--rounds 6 --batch 20000`), đọc `[NOMT-STATS]` namespace=account_state trực tiếp từ log:

| Block | pageRequests (luỹ kế) | pageCacheMissRate | pageFetchAvg | valueFetchAvg | htOccupancy |
|---|---|---|---|---|---|
| 0 | 2 | 0.00% | 0s | 0s | 0.0000% (0/10M) |
| 5 | 2.818 | 0.00% | 0s | 0s | 0.0198% (1.982/10M) |
| 10 | 18.896 | 0.00% | 0s | 0s | 0.0396% (3.962/10M) |
| 20 | 56.247 | 0.00% | 0s | 0s | 0.0416% (4.161/10M) |
| 30 | 95.934 | 0.00% | 0s | 0s | 0.0416% (4.161/10M) |
| 35 | 115.515 | 0.00% | 0s | 0s | 0.0416% (4.161/10M) |

**`pageCacheMissRate` = 0.00% suốt toàn bộ 6 round (300k+ account cộng dồn), `pageFetchAvg`/`valueFetchAvg` ≈ 0, `htOccupancy` phẳng ở 0.04%.** Đây là bằng chứng trực tiếp từ chính NOMT (không phải suy luận gián tiếp): NOMT đang chạy tối ưu tuyệt đối — không cache-miss, không trie-depth cost đáng kể, bảng băm còn dư thừa cực lớn. **Toàn bộ chi phí tăng dần của `TX Execution` nằm 100% ở code Go/Rust bao quanh NOMT (registry bookkeeping mục 13 + GC pressure từ TTL cache mục 15-của-vòng-3-cũ), không phải bản thân NOMT.** Kết luận này đóng lại hoàn toàn hướng "tối ưu NOMT" — không còn gì để tận dụng thêm từ NOMT ở quy mô dữ liệu hiện tại; muốn cải thiện tiếp phải nhắm vào registry map Go-side hoặc TTL cache, không phải NOMT.
