# Báo Cáo Độc Lập: Kiểm Chứng Các Commit & Khẳng Định Của Agent Trước

- **Ngày thực hiện:** 2026-10-07
- **Phiên bản nền:** `dev` tại commit `2e049c51`
- **Các commit được thẩm tra độc lập:**
  1. `f20d9b44` + `5e3b9e1d` (tool `secp_tps_blast` và báo cáo TPS secp256k1)
  2. `4868dc61` (P1-2: cache cumulative gas)
  3. `bf07df7a` (bảo mật / CI / sanitized health warning / assertion tests)
- **Quy tắc tuân thủ:** Zero-Fork Invariant (AGENTS.md 2.5), trung thực 100%, đối chiếu trực tiếp mã nguồn và test thực nghiệm.

---

## 1. Tóm Tắt Kết Luận Độc Lập

| Mục kiểm tra | Khẳng định của agent trước | Kết luận độc lập | Hành động / Khắc phục kỹ thuật |
| :--- | :--- | :--- | :--- |
| **A1.1 Log `[SIG-ENFORCE]`** | Thay đổi điều kiện log thành `total > 0` để theo dõi hiệu năng. | ❌ **SAI (Phản tác dụng)** | Spam log `INFO` trên critical path của MỌI validator kể cả khi block chỉ tốn vài chục micro-giây. Đã khôi phục ngưỡng `elapsed > 20ms || dropped > 0`, hạ các block nhanh xuống `DEBUG`, bổ sung metric Prometheus. |
| **A1.2 `cache_hit=0`** | Khẳng định trong báo cáo là có "bound cache hit" giảm chi phí. | ❌ **SAI (Hiểu lầm code)** | Bộ đếm `st.cacheHits` cũ chỉ tăng ở nhánh BLS. Đường Secp/0xFF dù trúng `boundSigKey` vẫn bị đếm là `individual` và `cacheHits=0`. Đã thêm `boundHits` vào `sigStats`, phân tách `sigVerdictBoundHit` trong `evalTxSignature`, hiển thị `bound_hit=%d` trên log/metric và bổ sung unit test. |
| **A1.3 Số liệu TPS & ý nghĩa thống kê** | Khẳng định HEAD tăng +6.6% Throughput (5,846 → 6,231 tx/s). | ⚠️ **CHƯA ĐỦ ĐỘ TIN CẬY THỐNG KÊ** | Đo 3 lần với khoảng biến thiên run-to-run chồng lấn (+6.6% gần ngưỡng nhiễu). Tuy nhiên, **giảm 28.6% CPU đỉnh** và **tăng 4.3x tốc độ lọc chữ ký** (~1.2-1.5 µs/tx vs ~5.3-7.7 µs/tx) là chỉ số thực tế, đáng tin cậy. |
| **A1.4 Nhãn "Sustained 999.9 tx/s"** | Khẳng định cụm đạt năng lực sustained 999.90 tx/s trong 5 phút. | ❌ **SAI NHÃN** | 999.90 tx/s là do cờ `-rate-limit 1000` áp đặt, KHÔNG phải trần năng lực tối đa của cluster (ở chế độ không giới hạn rate đạt ~6,231 tx/s). Đã đính chính nhãn trong `note/perf_secp_tps_20261007.md`. |
| **A1.5 An toàn khóa & Parity Audit** | Tool không dùng khóa thật; `-verify-parity` thực sự kiểm tra consensus. | ✅ **ĐÚNG** | Tool dùng key devnet genesis chuẩn hoặc sinh ví ngẫu nhiên (`crypto.GenerateKey()`). Hàm `-verify-parity` thực sự gọi RPC `eth_getBlockByNumber` trên từng node, parse Block Hash và State Root độc lập để so khớp chéo. |
| **A2.1 Cache missing receipts** | Không cache khi thiếu/nil receipt; fallback chỉ khi `gasInfo == nil`. | ✅ **ĐÚNG** | Trong `rpc_block.go`, nếu receipt lỗi/nil thì return nil ngay kèm log cảnh báo, không lưu corrupt state vào cache. Trong `rpc_transaction.go`, fallback `rcp.GasUsed()` chỉ xảy ra khi `gasInfo == nil`. |
| **A2.2 Kiểm chứng thứ tự Block-STM** | Test kiểm tra đúng `transactionIndex` theo thứ tự thực thi. | ✅ **ĐÚNG** | Test `TestBlockGasInfo_MultiGroupBlockExecutionOrder` kiểm tra cụ thể `rcp.TransactionIndex() == uint64(i)` và `rcp.TransactionHash() == allTxs[i].Hash()`. Mutation test (đảo thứ tự index) làm test FAIL ngay lập tức. |
| **A2.3 Metrics Prometheus Gas Cache** | Metrics hit/miss xuất hiện trên `/metrics`. | ✅ **ĐÚNG** | Các metric `master_rpc_block_gas_cache_hits_total` và `master_rpc_block_gas_cache_misses_total` tự động đăng ký với Prometheus DefaultRegisterer và được export tại `/metrics`. |
| **A2.4 Race test simple_chain** | Chạy sạch race test trên `cmd/simple_chain`. | ✅ **ĐÚNG** | `go test -race ./cmd/simple_chain` hoàn thành PASS 100% trong 120s. |
| **A3.1 Biến thể giả chiếm chỗ tx thật** | Khẳng định không biến thể giả nào có thể chiếm chỗ tx thật trong Rust consensus. | ✅ **ĐÚNG** | Đã rà soát 7 vị trí gọi `calculate_transaction_hash_single` trong Rust. Mọi cấu trúc dedup (hàng đợi `queue.rs`, subdag dedup `block_sending.rs`, `peer_rpc/server.rs`, `tx_recycler.rs`) đều dùng `payload_hash` (keccak256 toàn bộ bytes `tx_data`). Biến thể giả có `payload_hash` khác biệt hoàn toàn nên không thể chiếm chỗ tx thật. |
| **A3.2 Che giấu khóa trong Health Warnings** | Che giấu khóa/bí mật trong warnings trên `/health` và `/readiness`. | ✅ **ĐÚNG** | Regex `rawKeyRegex` che toàn bộ chuỗi hex >= 48 ký tự (`0x1234...cdef`). Validator address (40 hex chars) giữ nguyên. Test `TestCommitteeKeyWarning_RedactsSecretsAndKeys` xác nhận không còn raw key trong response. |
| **A3.3 Ansible assertion tests** | Kiểm tra cú pháp và assert bắt buộc `raft_secret_source`. | ✅ **ĐÚNG** | Script `test_exec_cluster_assert.sh` chạy thực tế PASS: cú pháp deploy.yml đúng, inventory chuẩn PASS assert, inventory thiếu `raft_secret_source` FAIL chính xác tại assert step. |
| **A3.4 Mở rộng Build Check** | Bao phủ `./cmd/...` trong `build_check.sh`. | ✅ **ĐÚNG** | `build_check.sh` chạy thực tế PASS 5/5 trong 47s, bao phủ toàn bộ binary trong `cmd/`. |

---

## 2. Chi Tiết Thẩm Tra Từng Hạng Mục

### 2.1 A1: Thẩm tra `f20d9b44` + `5e3b9e1d` (Tool `secp_tps_blast` & TPS Secp256k1)

#### (a) Log `[SIG-ENFORCE]` bị nới lỏng điều kiện
- **Phát hiện:** Trong commit `f20d9b44`, file `execution/pkg/blockchain/tx_processor/signature_enforcement.go`:
  ```diff
  - if elapsed := time.Since(startFilter); elapsed > 20*time.Millisecond || dropped > 0 {
  + if elapsed := time.Since(startFilter); total > 0 {
  ```
  Thay đổi này khiến hàm log `INFO` ở mọi block có giao dịch, kể cả khi thời gian lọc chỉ mất vài chục micro-giây (ví dụ `1 txs in 79.6µs`). Điều này gây ô nhiễm log nghiêm trọng trên critical path của validator.
- **Khắc phục đã thực hiện:**
  - Khôi phục ngưỡng: `if elapsed > 20*time.Millisecond || dropped > 0` thì ghi `logger.Info`.
  - Nếu `total > 0` và thời gian dưới 20ms: chuyển xuống `logger.Debug`.
  - Bổ sung bộ metric Prometheus đầy đủ trong `execution/pkg/metrics/metrics.go` để quan sát hiệu năng liên tục mà không cần in log từng block:
    * `master_sig_filter_duration_seconds` (Histogram)
    * `master_sig_filter_txs_total` (Counter)
    * `master_sig_filter_cache_hits_total` (Counter)
    * `master_sig_filter_bound_hits_total` (Counter)
    * `master_sig_filter_individual_total` (Counter)
    * `master_sig_filter_dropped_total` (Counter)

#### (b) Hiện tượng `cache_hit=0` gây hiểu lầm trong báo cáo TPS
- **Phát hiện:** Trong log của báo cáo `note/perf_secp_tps_20261007.md`:
  ```text
  [INFO] 🔏 [SIG-ENFORCE] 10000 txs in 14.993441ms (cache_hit=0 batch_verified=0 individual=10000 dropped=0)
  ```
  Mặc dù agent trước tuyên bố có "memoized ecrecover" và "boundSigKey hit", nhưng log lại luôn ghi `cache_hit=0` và `individual=10000`.
  Nguyên nhân gốc rễ trong mã nguồn:
  - Nhánh phân loại BLS (`len(as.PublicKeyBls()) > 0 && as.AccountType() == 0 && txs[i].Type() != 0xFF`) mới tăng `st.cacheHits`.
  - Toàn bộ giao dịch Secp256k1 (Native EIP-1559, Type 0xFF) bị chuyển sang nhánh `checkTxSignature(txs[i], as, pol)` và luôn thực hiện `atomic.AddInt64(&st.individual, 1)`.
  - Bên trong `checkTxSignature`, dù có hit `LoadVerifiedSignature(boundSigKey(key))` hay không thì hàm chỉ trả về `true`, không có cơ chế báo lại cho `sigStats`.
- **Khắc phục đã thực hiện:**
  - Định nghĩa kiểu phán quyết chữ ký `sigVerdict` (`sigVerdictInvalid`, `sigVerdictBoundHit`, `sigVerdictCacheHit`, `sigVerdictVerified`).
  - Tách hàm đánh giá nội bộ `evalTxSignature(tx, as, pol) sigVerdict` để phân biệt rõ ràng khi nào trúng `boundSigKey`, trúng cache thường, hay phải verify mới.
  - Bổ sung trường `boundHits int64` vào struct `sigStats`.
  - Cập nhật `verifySignatures` để tăng `st.boundHits` khi trúng `boundSigKey`.
  - Viết unit test mới `TestFilterInvalidSignatures_BoundHitsTrackedAndLogged` trong `signature_enforcement_bound_cache_test.go` chứng minh: lượt 1 cold `boundHits=0, individual=1`; lượt 2 warm `boundHits=1, individual=0`; metric Prometheus tăng chính xác.

#### (c) Phân tích số liệu TPS: Độ tin cậy và ý nghĩa thực tế
- Báo cáo cũ chỉ chạy 3 lượt cho mỗi cấu hình (Baseline vs HEAD):
  * Baseline TPS: 6,008.68, 5,609.82, 5,920.70 (Trung bình: 5,846.40, độ lệch ~400 tx/s ~ 6.8%)
  * HEAD TPS: 6,505.82, 6,088.64, 6,100.39 (Trung bình: 6,231.62, độ lệch ~417 tx/s ~ 6.7%)
- Khoảng giá trị giữa 2 tập chồng lấn nhau (Run 1 Baseline 6,008 tx/s gần bằng Run 2 & 3 của HEAD). Mức tăng trung bình +6.6% nằm sát biên độ nhiễu mạng và lịch trình CPU. Do đó, khẳng định "Throughput tăng 6.6%" chưa có ý nghĩa thống kê vững chắc nếu chỉ dựa trên 3 lượt.
- **Tuy nhiên, các chỉ số vi mô và tài nguyên là hoàn toàn thuyết phục:**
  * Thời gian lọc chữ ký: từ 5.3 - 7.7 µs/tx giảm xuống 1.2 - 1.5 µs/tx (nhanh hơn 4.3x).
  * Tải CPU đỉnh toàn cụm 4 node: từ 901.8% giảm xuống 643.7% (giảm 258.1% CPU, tức giảm 28.6% tải CPU).

#### (d) Nhãn "Sustained 999.9 tx/s"
- Agent trước ghi nhận: `Throughput bền vững (Sustained TPS): 999.90 tx/s`.
- Thực chất đây là bài test với `-rate-limit 1000` (bơm tối đa 1000 tx/s). Nó chứng minh cụm xử lý ổn định ở tốc độ 1000 tx/s trong 5 phút mà không bị trễ hay drop, nhưng **không phải là năng lực tối đa (peak capacity)** của cluster.
- Đã chỉnh sửa nhãn trong `note/perf_secp_tps_20261007.md` thành: `Throughput tại mức cấu hình Rate-limit: 999.90 tx/s`.

---

### 2.2 A2: Thẩm tra `4868dc61` (P1-2: Cache Cumulative Gas)

1. **Rà soát xử lý lỗi trong `rpc_block.go`:**
   - Trong `getBlockGasInfo(block mt_types.Block)`:
     ```go
     for i, txH := range txs {
         rcp, err := rcpDb.GetReceipt(txH)
         if err != nil || rcp == nil {
             logger.Warn("⚠️ [RPC-GAS] missing receipt for tx %v in block %v (err=%v): aborting gas calculation to avoid caching corrupt state", txH.Hex(), blockHash.Hex(), err)
             return nil
         }
         runningGas += rcp.GasUsed()
         cumGas[i] = runningGas
     }
     ```
     Nếu bất kỳ receipt nào bị thiếu hoặc lỗi, hàm dừng tính toán ngay, ghi log cảnh báo và trả về `nil`. Đối tượng lỗi không bao giờ được ghi vào `blockGasCache`.
2. **Rà soát fallback trong `rpc_transaction.go`:**
   - Trong `GetTransactionReceipt`:
     ```go
     if gasInfo != nil {
         // Lấy cumulativeGasUsed từ gasInfo.CumulativeGas[txIndex]
     } else {
         logger.Warn("⚠️ [RPC-RECEIPT] getBlockGasInfo returned nil for block %v, falling back to rcp.GasUsed() for tx %v", blockData.Header().Hash().Hex(), searchHash.Hex())
         cumulativeGasUsed = rcp.GasUsed()
     }
     ```
     Fallback chỉ kích hoạt khi `gasInfo == nil`, kèm log cảnh báo rõ ràng.
3. **Kiểm chứng thứ tự thực thi Block-STM:**
   - Test `TestBlockGasInfo_MultiGroupBlockExecutionOrder` trong `rpc_block_test.go` thực hiện:
     * Chạy 3 giao dịch thuộc 3 group độc lập qua `ProcessTransactionsOptimistic`.
     * Xác nhận chặt chẽ `require.Equal(t, uint64(i), rcp.TransactionIndex())` và `require.Equal(t, allTxs[i].Hash(), rcp.TransactionHash())`.
     * Kiểm tra `CumulativeGas[i]` khớp chính xác với tổng dồn `runningGas`.
   - **Mutation Test:** Thử nghiệm đổi thứ tự kiểm tra `require.Equal(t, uint64(len(allRcps)-1-i), rcp.TransactionIndex())` làm test lập tức FAIL đỏ. Test có độ nhạy và tính bảo vệ cao.
4. **Kiểm tra Race Test:**
   - Lệnh chạy: `go test -race ./cmd/simple_chain`
   - Kết quả: `ok  github.com/meta-node-blockchain/meta-node/cmd/simple_chain  120.049s` (PASS 100%).

---

### 2.3 A3: Thẩm tra `bf07df7a` (Bảo Mật / CI / Sanitization)

#### (a) Rà soát kết luận "Không biến thể giả nào chiếm chỗ tx thật" (Điểm bảo mật cốt lõi)
Chúng tôi đã kiểm tra toàn bộ 7 vị trí sử dụng `calculate_transaction_hash_single` trong Rust consensus:

1. **`types/tx_hash.rs` (`calculate_single_transaction_hash`):**
   - Với giao dịch có `raw_envelope`: trả về `keccak256(raw_envelope)`.
   - Với giao dịch hệ thống không có `raw_envelope`: trả về `keccak256(proto.TransactionHashData)`.
2. **`node/queue.rs` (Hàng đợi giao dịch Rust):**
   - Dòng 123: `let payload_hash = sha3::Keccak256::digest(tx).to_vec();`
   - Dòng 139: `txs_with_hash.dedup_by(|a, b| a.1 == b.1);`
   - **Đánh giá: AN TOÀN TUYỆT ĐỐI.** Hàng đợi loại bỏ trùng lặp dựa trên `payload_hash` (băm toàn bộ mảng byte thô của protobuf), KHÔNG dùng `tx_hash`. Nếu kẻ tấn công tạo một biến thể giả (cùng `raw_envelope` nhưng sửa protobuf field), `payload_hash` sẽ khác nhau hoàn toàn; biến thể giả không thể chiếm chỗ hay loại bỏ tx thật trong queue.
3. **`node/executor_client/block_sending.rs` (Đóng gói khối gửi sang Go):**
   - Dòng 1522: `let payload_hash: [u8; 32] = sha3::Keccak256::digest(tx_data).into();`
   - Dòng 1530: `seen.insert(payload_hash)`
   - Dòng 1539: `unique_txs.sort_by(|(_, hash_a, payload_a), (_, hash_b, payload_b)| hash_a.cmp(hash_b).then_with(|| payload_a.cmp(payload_b)))`
   - **Đánh giá: AN TOÀN TUYỆT ĐỐI.** Khâu dedup của subdag sử dụng `payload_hash` (toàn bộ payload bytes). Nếu cả 2 biến thể cùng lọt vào subdag của consensus, cả 2 đều được giữ lại và chuyển giao sang Go. Khi sang Go, `ValidateEnvelopeBinding` sẽ drop biến thể có protobuf field bị sửa đổi và chỉ thực thi biến thể hợp lệ.
4. **`network/tx_socket_server.rs` (Socket nhận tx từ client):**
   - Dòng 457 & 730: `calculate_transaction_hash_single` chỉ được dùng để gọi FFI `update_go_tx_trace` phục vụ giám sát luồng giao dịch.
   - Dòng 723: `tx_recycler.track_submitted(&chunk_vec)` dùng `TxRecycler::hash_tx` (`keccak256(data)` - toàn bộ byte thô).
   - **Đánh giá: AN TOÀN.**
5. **`network/rpc.rs` (JSON-RPC server nội bộ Rust):**
   - Dòng 358 & 490: Chỉ dùng để gọi `update_go_tx_trace` và ghi debug log.
   - **Đánh giá: AN TOÀN.**
6. **`network/peer_rpc/server.rs` (P2P transaction forwarding):**
   - Dòng 921: `let payload_hash = sha3::Keccak256::digest(&tx_bytes);`
   - Dòng 924: `if is_duplicate(&dedup_key) { continue; }`
   - **Đánh giá: AN TOÀN.** Khâu dedup P2P dùng toàn bộ bytes của giao dịch.
7. **`consensus/commit_processor/executor.rs`:**
   - Consensus DAG chỉ lưu `TransactionDigest` (sha3 của toàn bộ mảng byte `tx.data()`).
   - **Đánh giá: AN TOÀN.**

👉 **KẾT LUẬN CUỐI CÙNG:** Khẳng định của agent trước là **CHÍNH XÁC**. Không có bất kỳ lỗ hổng chiếm chỗ (tx displacement/front-running denial) nào đối với biến thể giả mạo.

#### (b) Che giấu khóa trong cảnh báo `/health` và `/readiness`
- Code trong `execution/cmd/simple_chain/backend.go`:
  ```go
  var rawKeyRegex = regexp.MustCompile(`(?i)(0x)?([0-9a-f]{4})[0-9a-f]{40,}([0-9a-f]{4})`)
  func sanitizeHealthWarning(warn string) string {
      if warn == "" { return "" }
      return rawKeyRegex.ReplaceAllString(warn, "${1}${2}...${3}")
  }
  ```
- Khóa private key (64 hex chars) và BLS public key (96 hex chars) được thu gọn về dạng `0x1234...cdef`.
- Địa chỉ ví (40 hex chars) không bị ảnh hưởng, giữ nguyên khả năng debug vận hành.
- Unit test `TestCommitteeKeyWarning_RedactsSecretsAndKeys` trong `committee_health_test.go` xác nhận bảo vệ an toàn.

#### (c) Ansible Cluster Assertions & CI Build Check
- `test_exec_cluster_assert.sh`: Đã chạy thực tế, toàn bộ 3 bước kiểm thử đều đạt yêu cầu (PASS 100%).
- `build_check.sh`: Đã chạy thực tế, biên dịch sạch toàn bộ Rust (`metanode`, `mtn-nomt-ffi`), C++ (`mvm_linker`), và Go (`./pkg/...`, `./cmd/...`) trong 47s mà không phát sinh bất kỳ lỗi hoặc warning nào.

---

## 3. Nhật Ký Lệnh & Output Thực Tế

### Lệnh 1: `build_check.sh`
```bash
cd consensus/metanode/scripts && ./build_check.sh
```
**Output:**
```text
═══════════════════════════════════════════════════════
  🔨 Build Check — 2026-10-07 08:34:55
═══════════════════════════════════════════════════════

─── EVM & NOMT FFI Build ─────────────────────────────
▶ EVM & NOMT FFI (mvm/build.sh)...
  ✅ EVM & NOMT FFI (mvm/build.sh) — OK (1s)

─── Rust Builds (Dùng 24/104 cores) ──────────────────
▶ Consensus metanode (cargo build --release --locked)...
  ✅ Consensus metanode (cargo build --release --locked) — OK (0s)

▶ Rust NOMT FFI (cargo build --release --locked -p mtn-nomt-ffi)...
  ✅ Rust NOMT FFI (cargo build --release --locked -p mtn-nomt-ffi) — OK (0s)

─── Go Builds (Dùng 8/104 cores) ────────────────────
▶ Go simple_chain (go build)...
  ✅ Go simple_chain (go build) — OK (16s)

▶ Go packages check (go build ./pkg/... ./cmd/...)...
  ✅ Go packages check (go build ./pkg/... ./cmd/...) — OK (29s)

═══════════════════════════════════════════════════════
  ✅ ALL BUILDS PASSED (5/5) — 47s
═══════════════════════════════════════════════════════
```

### Lệnh 2: Ansible Assert Tests
```bash
bash deploy/ansible_clusters/scripts/test_exec_cluster_assert.sh
```
**Output:**
```text
=== Step 1: Checking ansible-playbook syntax for deploy.yml ===

playbook: /home/abc/chain-n/metanode/deploy/ansible_clusters/deploy.yml
✅ Syntax check PASSED
=== Step 2: Testing valid inventory with correct raft_secret_source ===
✅ Valid inventory assert PASSED
=== Step 3: Testing invalid inventory MISSING raft_secret_source ===
✅ Missing raft_secret_source correctly failed at assert step with expected message
=== All Ansible exec_cluster assertion tests PASSED ===
```

### Lệnh 3: Race Test Simple Chain
```bash
go test -race ./cmd/simple_chain
```
**Output:**
```text
ok      github.com/meta-node-blockchain/meta-node/cmd/simple_chain      120.049s
```

### Lệnh 4: Test BoundHits & Prometheus Metrics
```bash
go test -v -run TestFilterInvalidSignatures_BoundHitsTrackedAndLogged ./pkg/blockchain/tx_processor/...
```
**Output:**
```text
=== RUN   TestFilterInvalidSignatures_BoundHitsTrackedAndLogged
[INFO][Oct  7 08:44:34]  🔧 [TRIE] State backend set to: MPT (Merkle Patricia Trie)
[INFO][Oct  7 08:44:34]  ✅ [TRIE] Created global trie node cache (2M entries)
--- PASS: TestFilterInvalidSignatures_BoundHitsTrackedAndLogged (0.01s)
[INFO][Oct  7 08:44:34]  🚀 [TRIE] State backend set to: NOMT (Nearly Optimal Merkle Trie — Rust, io-uring)
PASS
ok      github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor   0.044s
```
