# Kế hoạch Hành động Tiếp theo (Next Steps Execution Plan) — 2026-10-17

- **Ngày lập kế hoạch:** 2026-10-17
- **Nhánh cơ sở hiện tại (`dev`):** `3da4a0d3` (`HEAD == origin/dev`, đồng bộ đầy đủ PR #171 tại `b4482040`, gói tối ưu thực thi tại `5004cf0f` & `e26ad843`, và các bản vá CI/raftfeed `0b7deada..3da4a0d3`).
- **Trạng thái Build hiện tại:** `./consensus/metanode/scripts/build_check.sh` **PASS 5/5 (41s)**, working tree sạch 100%.
- **Mục đích tài liệu:** Đặc tả kỹ thuật chi tiết từng bước (kèm đường dẫn file, số dòng, đoạn mã cụ thể và lệnh kiểm chứng) để bàn giao cho Agent tiếp theo thực thi tuần tự.

---

## 🛡️ Nguyên tắc Bất khả xâm phạm (Mandatory Guardrails cho Agent Thực thi)

Trước và trong khi thực hiện bất kỳ pha nào dưới đây, Agent **BẮT BUỘC** tuân thủ `AGENTS.md`:
1. **Zero-Fork Invariant (100% Không Fork):**
   - Tuyệt đối không dùng `timeout`, `sleep`, `Duration` để quyết định dispatch commit hoặc thay đổi state đồng thuận.
   - Mọi thay đổi trạng thái tài khoản/chuỗi phải đi qua luồng đồng thuận tất định.
2. **Strict KISS & YAGNI:**
   - Chỉ viết lượng code tối thiểu, trực tiếp giải quyết đúng vấn đề nêu trong từng Pha; không tự ý thêm interface, channel hay worker nền không nằm trong đặc tả.
3. **Build & Race Verification:**
   - Sau mỗi Pha có sửa code, phải chạy `go test -race` trên các package liên quan và chạy `./consensus/metanode/scripts/build_check.sh` đảm bảo **PASS 5/5 (0 errors, 0 warnings)**.
4. **Quy tắc Bằng chứng (`note/CLAIMS_LEDGER.md`):**
   - Không thêm dòng `RE-VERIFIED` vào `note/CLAIMS_LEDGER.md` nếu chưa có file log/dữ liệu thô trong `note/evidence/` hoặc unit test chạy lại được.

---

## 🔴 PHA 1: Sửa lỗi Regression trong PR #172 / #173 (`origin/update-account` @ `e93ec4e9`) và Merge vào `dev`

### 1.1. Bối cảnh & Nguyên nhân gốc rễ (Root Cause)
- Trên nhánh `origin/update-account` (`e93ec4e9`, tương ứng `refs/pull/172/head` và `refs/pull/173/head`), hầu hết các commit đã được squash-merge vào `dev` qua PR #171 (`b4482040`), ngoại trừ commit mới `6959715bf7bca21acae8ec993d6e4e9f85ce34f3` (*"refactor: remove sub-node sync optimizations in transaction validation and clean up unused code and imports"*).
- Trong commit `6959715b`, khi gỡ bỏ biến `isSubNodeLagging` tại `execution/pkg/blockchain/tx_processor/validation.go`, nhánh kiểm tra chữ ký BLS đã bị sửa từ `else` thành `else if isNodeBLSIdentity(tx, as)`:
  ```go
  // Code lỗi trong 6959715b:
  } else if isNodeBLSIdentity(tx, as) {
      ...
  } else {
      return transaction.InvalidSign
  }
  ```
- **Tại sao đây là lỗi nghiêm trọng:**
  - Hàm `isNodeBLSIdentity(tx, as)` (`execution/pkg/blockchain/tx_processor/signature_enforcement.go:97-103`) kiểm tra `len(pub) > 0 && tx.FromAddress() == bls.GetAddressFromPublicKey(pub)`.
  - Ở chế độ mặc định/legacy (`TxSignatureMode == ""` hoặc `"bls"`, tức `pol.secp == false`), các tài khoản người dùng/dApp có địa chỉ Ethereum `0x...` đã đăng ký `PublicKeyBls` sẽ có `tx.FromAddress() != bls.GetAddressFromPublicKey(pub)` nên `isNodeBLSIdentity(tx, as) == false`.
  - Hệ quả là mọi giao dịch BLS hợp lệ từ tài khoản người dùng ở chế độ legacy đều rơi vào `else { return transaction.InvalidSign }`, làm **gãy hàng loạt unit test** trong:
    - `execution/pkg/blockchain/tx_processor/validation_verify_test.go` (`TestVerifyTransaction_Success`, `TestVerifyTransaction_FreeFeeBypass`, `TestVerifyTransaction_LaggingSubnode`)
    - `execution/pkg/blockchain/tx_processor/sig_policy_test.go:136` (`TestVerifyTransaction_SecpMode_NoLaggingBypass` — nhánh `legacy chain keeps the BLS dapp flow`)
    - `execution/pkg/blockchain/tx_processor/security_unit_test.go`
    - `execution/pkg/blockchain/tx_processor/chain_binding_test.go`

### 1.2. Các bước thực hiện chi tiết

1. **Áp dụng các thay đổi dọn dẹp hợp lệ từ `6959715b` lên nhánh làm việc từ `dev` (`3da4a0d3`):**
   - Các file dọn dẹp không lỗi trong `6959715b`:
     - `deploy/ansible_clusters/deploy.yml`
     - `deploy/ci/ci_config.yaml.example`
     - `execution/cmd/simple_chain/backend.go` (xóa field `processingChunkCount` không dùng)
     - `execution/cmd/simple_chain/rpc_transaction.go` (gộp alias import `eth_types` về `types`, xóa các hàm helper không dùng)
2. **Sửa đúng logic gỡ bỏ `isSubNodeLagging` trong `execution/pkg/blockchain/tx_processor/validation.go` (dòng 220–303 và 370–465):**
   - Xóa biến `isSubNodeLagging := len(as.PublicKeyBls()) == 0 && (tx.GetNonce() > 0 || as.Nonce() > 0)`.
   - Tại khối kiểm tra chữ ký (`validation.go:242-303`):
     - Khi `pol.secp && !isNodeBLSIdentity(tx, as)`: giữ nguyên kiểm tra `tx.ValidEthSign()`.
     - Ở nhánh còn lại (tức `isNodeBLSIdentity(tx, as)` trong chế độ `pol.secp`, HOẶC bất kỳ tài khoản nào có `len(as.PublicKeyBls()) > 0` trong chế độ legacy `!pol.secp`):
       ```go
       } else if len(as.PublicKeyBls()) > 0 {
           blsCacheKey := sigCacheKey(tx, as.PublicKeyBls())
           if !LoadVerifiedSignature(blsCacheKey) {
               request := transaction.NewVerifyTransactionRequest(
                   tx.Hash(),
                   common.PubkeyFromBytes(as.PublicKeyBls()),
                   tx.Sign(),
               )
               if !request.Valid() {
                   logger.Error("BLS Verification Failed! txHash=%s from=%s", txHash.Hex(), tx.FromAddress().Hex())
                   if !tx.ValidEthSign() {
                       return transaction.InvalidSign
                   }
               }
               StoreVerifiedSignature(blsCacheKey)
               bumpVerifiedSignatureCacheCount()
           }
       } else {
           if !tx.ValidEthSign() {
               return transaction.InvalidSign
           }
       }
       ```
     - Lưu ý: Khi `pol.secp == true`, nhánh `pol.secp && !isNodeBLSIdentity(tx, as)` ở phía trên đã bắt toàn bộ tài khoản không phải `isNodeBLSIdentity`, nên chỉ đúng `isNodeBLSIdentity` mới đi xuống nhánh `len(as.PublicKeyBls()) > 0`. Khi `pol.secp == false` (legacy mode), mọi tài khoản có `len(as.PublicKeyBls()) > 0` đều được xác thực chữ ký BLS đúng như thiết kế của `TestSigPolicy_ExecutionFilterMatrix` và `TestVerifyTransaction_Success`!
   - Tại khối kiểm tra deploy (`validation.go:375-380`) và kiểm tra phí/số dư (`validation.go:430-465`): bỏ điều kiện `!isSubNodeLagging` để luôn kiểm tra `ValidAmount`, `ValidMaxFee`, `ValidMaxGasPrice` một cách tất định.
3. **Cập nhật unit test `TestVerifyTransaction_LaggingSubnode` trong `execution/pkg/blockchain/tx_processor/validation_verify_test.go` (dòng 171–197):**
   - Đổi tên/cập nhật kỳ vọng của test này: khi tài khoản có `nonce > 0` nhưng không có `PublicKeyBls` và gửi giao dịch mang chữ ký BLS (không có chữ ký ETH hợp lệ), `VerifyTransaction(tx, cs, as)` **phải từ chối với `transaction.InvalidSign`** (thay vì được miễn kiểm tra như cơ chế `isSubNodeLagging` cũ).

### 1.3. Lệnh kiểm chứng Pha 1
```bash
go -C execution test -race ./pkg/blockchain/tx_processor/... ./cmd/simple_chain/...
cd consensus/metanode/scripts && ./build_check.sh
```

---

## 🟡 PHA 2: Đo Kiểm chứng A/B & Profile cho Commit `e26ad843` + Khảo sát/Tối ưu `_Cfunc_database_index_document` của Xapian

### 2.1. Bối cảnh & Dữ liệu Hiện tại
- Trong `note/CLAIMS_LEDGER.md` (dòng 22 & dòng 36) và `note/readloop_gate_explorer_pacing_report_20261016.md` (§7.2):
  1. Commit `e26ad843` (`perf(execution): pool Keccak256 state, inline tx accessors, bound txsCache, and eliminate mempool batch allocs`) đã được commit lên `dev` nhưng **chưa có lượt đo A/B thực nghiệm (1M & 4M tx) kèm trích xuất `pprof` (`cpu30.pb.gz`, `allocs.pb.gz`, `heap.pb.gz`)** để cập nhật vào `note/CLAIMS_LEDGER.md`.
  2. Trên profile CPU 30s của `val0` ở tải 4M tx (`note/evidence/readloop_gate_explorer_pacing_20261016/pprof_comparison.txt`), sau khi `_Cfunc_database_commit` đã giảm `-36.97%` xuống còn `7.74s` (`2.86%` CPU), điểm nóng CGo lớn nhất còn lại của `ExplorerSearchService` là `_Cfunc_database_index_document` chiếm **`45.14s` (`16.69%` tổng CPU)**.

### 2.2. Các bước thực hiện chi tiết

#### Bước 2A — Tối ưu đường ghi chỉ mục Xapian trong `execution/pkg/goxapian/xapian_wrapper.cpp` và `execution/pkg/explorer/service.go`:
1. **Sử dụng `doc.add_boolean_term(...)` (`wdfinc = 0`) thay cho `doc.add_term(...)` (`wdfinc = 1`) trong `execution/pkg/goxapian/xapian_wrapper.cpp:172`:**
   - Trong `ExplorerSearchService.doIndexTransaction` (`execution/pkg/explorer/service.go:346-386`), tất cả các term được đánh chỉ mục (`H<txHash>`, `F<fromAddr>`, `T<toAddr>`, `B<blockNum>`, `RH<receiptHash>`, `K<contractAddr>`, `TF<tokenFrom>`, `TT<tokenTo>`) đều là các **khóa định danh chính xác theo tiền tố (boolean prefix terms)**, không phải văn bản tự nhiên cần tính trọng số tần suất từ `WDF` (Within-Document Frequency) hay độ dài văn bản (`doclen`) cho thuật toán xếp hạng BM25.
   - Hiện tại `doc.add_term(std::string(p, len))` mặc định `wdfinc = 1`, buộc Xapian phải cập nhật thêm bảng `doclen` và ghi `wdf` cho từng term. Đổi sang `doc.add_boolean_term(std::string(p, len))` (tương đương `doc.add_term(term, 0)`) giúp Xapian bỏ qua cập nhật thống kê tần suất/độ dài tài liệu mà vẫn giữ nguyên 100% kết quả truy vấn `QueryParser` (`hash:`, `from:`, `to:`, `block:`, `token:`, `t_from:`, `t_to:`, `r_hash:`).
2. **Gom lô theo Shard (`IndexDocumentsBatch`) hoặc gom các giao dịch cùng Shard trong `ExplorerSearchService` (tùy chọn nếu kiểm tra an toàn và gọn):**
   - Hiện tại mỗi giao dịch gọi `s.locks[shardIndex].Lock()` + 1 lệnh CGo `db.IndexDocument(...)` riêng lẻ (`10,000` lần lock + `10,000` lần vượt biên giới CGo mỗi block 10k tx).
   - Có thể kiểm tra mức giảm CPU của `add_boolean_term` trước; nếu cần giảm thêm chi phí CGo/lock, gom các job cùng `shardIndex` đang có sẵn trong `s.indexQueue` (tối đa ví dụ `32` hoặc `64` job/lần lock) vào một lời gọi CGo batch.
3. **Kiểm chứng tính tương thích tìm kiếm:**
   - Chạy toàn bộ unit test trong `execution/pkg/goxapian/...` và `execution/pkg/explorer/...` để đảm bảo mọi truy vấn `SearchTransactions` trả về kết quả giống hệt 100%.

#### Bước 2B — Chạy Thực nghiệm A/B Đan xen (1M & 4M Tx) và Cập nhật `note/CLAIMS_LEDGER.md`:
1. Tham khảo quy trình và script điều phối chuẩn tại `note/evidence/readloop_gate_explorer_pacing_20261016/driver_ab_readloop.py.txt` và `PREREGISTERED.md`.
2. So sánh đối chứng giữa baseline trước `e26ad843` (`5004cf0f`) và bản mới trên `dev` (gồm `e26ad843` + tối ưu Xapian ở Bước 2A):
   - Thu thập đủ `cpu30.pb.gz`, `heap.pb.gz`, `allocs.pb.gz` trên `val0` cùng các chỉ số `tps_end_to_end`, `tps_during_injection`, `peak_rss_mb_tool`, `anomaly_sum`, `loss == 0`, và `parity == OK`.
   - Lưu toàn bộ file thô vào thư mục `note/evidence/<tên_thư_mục_20261017>/` kèm `MANIFEST.json` và `PREREGISTERED.md`.
   - Cập nhật dòng mới vào `note/CLAIMS_LEDGER.md` theo đúng quy tắc nhãn `[ĐO]`, `[TÍNH]`.

---

## 🟢 PHA 3: Đồng bộ `PROJECT_STRUCTURE.md` & Hoàn tất Unit Test Follow-up của PR #169

### 3.1. Cập nhật `PROJECT_STRUCTURE.md` (Bắt buộc theo `AGENTS.md` Part 6)
Cập nhật các mục sau trong `PROJECT_STRUCTURE.md` để phản ánh đúng trạng thái của `dev` sau khi gộp PR #171 (`b4482040`):
1. **Mục `cmd/simple_chain/` (khoảng dòng 193–216):**
   - Bổ sung `rollup_adapters.go` (Adapter kết nối `ChainState` / `AccountStateDB` với `AccountRegistryHandler`, `RegistrationRelay` và `RollupSystemHandler`).
   - Bổ sung `proposer_dedup_test.go` (Unit test kiểm chứng `rollupNonceManager` chống xung đột nonce và chống gửi trùng payload hệ thống).
   - Trong `cmd/simple_chain/processor/`: bổ sung `registration_tcp_test.go`.
2. **Mục `pkg/` — Shared Packages (khoảng dòng 263–264):**
   - Tại `rollup/`: cập nhật thêm 2 loại payload hệ thống mới (`SystemPayloadKindAccountProvisional`, `SystemPayloadKindAccountLocked`), danh sách `trustedValidators` cho `AccountRegistryHandler`, và cơ chế đối soát khóa/provisional của `RegistrationRelay`.
   - Tại `parentchain/`: cập nhật mô tả `QuorumClient` từ `>= f+1` sang ngưỡng BFT supermajority `(N+f)/2 + 1` (phân biệt `ErrQuorumNotReached` và `ErrNotFound`, tự động `ResetNonce()` khi gửi thất bại).
3. Cập nhật trường `> **Last updated:** 2026-10-17` ở đầu file `PROJECT_STRUCTURE.md`.

### 3.2. Hoàn tất 2 Unit Test tồn đọng trong `note/pr169_followups_20261015.md` (§4 "Chưa làm")
Tham chiếu `note/pr169_followups_20261015.md:30-32`:
1. **Unit test tự động cho `executeAndRespondReadTx` với kết nối giả (`mock Connection`):**
   - Tạo test trong `execution/cmd/simple_chain/processor/tx_virtual_executor_core_test.go` kiểm chứng `executeAndRespondReadTx` phản hồi đúng thông điệp `RETURNED` / `TRANSACTION_ERROR` qua mock connection mà không làm thay đổi state thật.
2. **Kiểm chứng `EstimateGas` qua đường TCP cho giao dịch EIP-7702 (`SetCode`) và hợp đồng gọi lồng / nhiều lần `SSTORE`:**
   - Bổ sung test kiểm chứng đường TCP `EstimateGas` tính đúng `TxGas + n*CallNewAccountGas + MINIMUM_BASE_FEE` cho giao dịch mang `AuthorizationList` (EIP-7702) và xác nhận các slot storage không bị biến đổi sau khi `EstimateGas` hoàn tất.
   - Sau khi test PASS, cập nhật mục `Chưa làm` trong `note/pr169_followups_20261015.md`.

---

## ✅ Danh sách Kiểm tra Nghiệm thu (Acceptance Criteria Checklist)

Agent thực thi đã đánh dấu và xác nhận các mục sau:
- [x] **Pha 1:** `go -C execution test -race ./pkg/blockchain/tx_processor/... ./cmd/simple_chain/...` PASS 100% sau khi gỡ bỏ `isSubNodeLagging`.
- [x] **Pha 2:** Bước 2A `add_boolean_term` tối ưu đường ghi chỉ mục Xapian (`xapian_wrapper.cpp`) đã hoàn thành; `go -C execution test ./pkg/goxapian/... ./pkg/explorer/...` PASS 100%. (Bước 2B đo A/B 16 runs kéo dài 2-3h bàn giao cho phiên kiểm thử độc lập trên máy chuyên dụng theo quy định của AGENTS.md Part 4).
- [x] **Pha 3:** `PROJECT_STRUCTURE.md` được cập nhật đầy đủ các file/module từ PR #171 (rollup adapters, dedup test, registration tcp test, BFT supermajority threshold); các unit test tồn đọng trong `note/pr169_followups_20261015.md` đã được viết đầy đủ trong `tx_virtual_executor_core_test.go` và PASS 100% với race detector.
- [x] **Kiểm tra Build tổng thể:** Chạy `./consensus/metanode/scripts/build_check.sh` đạt **5/5 PASS (0 errors, 0 warnings) — 38s**.
- [x] **Zero-Fork Invariant:** Không có bất kỳ thay đổi nào vi phạm nguyên tắc 100% không fork (tất định, không timeout, thà pending chứ không fork).
