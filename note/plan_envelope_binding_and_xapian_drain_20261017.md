# Kế hoạch giao Agent: Hoàn tất bộ đo 4M, Xử lý triệt để lỗi Miss Cache & Cấp phát lặp Envelope/Signature, và Tối ưu gom lô Xapian (2026-10-17)

**Quy tắc tối thượng:** Tuân thủ tuyệt đối [`AGENTS.md`](file:///home/abc/chain-n/metanode/AGENTS.md) **Phần 2.5 (Zero-Fork Invariant — Bất khả xâm phạm)** và **Phần 2.6 (Chống Hoang Tưởng / Chống Bịa Số Liệu)**.
- **Không có file raw thật = Không có số liệu.**
- Mọi phát biểu định lượng phải gắn nhãn `[ĐO]`, `[TÍNH]`, `[ĐỌC CODE]`, `[GIẢ THUYẾT]`, hoặc `[CHƯA ĐO]`.
- Hạng mục nào chưa chạy được hoặc lỗi môi trường: bắt buộc ghi rõ `[CHƯA ĐO]` kèm nguyên nhân, **tuyệt đối không tự điền số, không ngoại suy**.

---

## 0. Hiện trạng & Bằng chứng thực tế đã kiểm tra (Đọc kỹ trước khi làm)

Toàn bộ số liệu dưới đây được trích xuất trực tiếp từ bộ profile thực tế trên nhánh `dev` (`09b4d706`) tại thư mục `note/evidence/keccak_blockcache_xapian_batch_20261017/ab_4m/` (ký hiệu `$D`), lượt chạy `ab_4m_next_p1_run1`:

### 0.1. Bảng số liệu gốc từ `ab_4m_next_p1_run1`

| Hạng mục / Hàm | Số liệu thực tế | Nhãn | Lệnh / File kiểm chứng lại |
| :--- | :--- | :--- | :--- |
| **Phân bổ tx giữa 4 validator** | Tổng gửi = commit = `4,010,583` tx (`loss = 0`). Ingress theo từng validator: `val0` (`:31646`) = **`1,226,515` tx (`30.58%`)**, `val1` (`:31647`) = `767,000` (`19.12%`), `val2` (`:31648`) = `1,053,035` (`26.26%`), `val3` (`:31649`) = `964,033` (`24.04%`). | `[ĐO]` / `[TÍNH]` | `$D/ab_4m_next_p1_run1.json:30-52` |
| **Độ trễ mempool → commit & Backlog** | `inject_seconds = 143.91s`, `drain_seconds = 110.52s`, `backlog_at_inject_end = 1,685,048` tx; độ trễ trung vị **`latency_p50 = 124.78s`**, `latency_avg = 120.93s`, `latency_p90 = 183.35s`. | `[ĐO]` | `$D/ab_4m_next_p1_run1.json:14-18,37-41` |
| **CPU `ValidateEnvelopeBindingsParallel`** | Tổng mẫu CPU = `274.69s` (30s cửa sổ đo). `ValidateEnvelopeBindingsParallel.func1` = **`44.20s` cum (`16.09%`)**; `validateDirectEnvelopeBinding` = `41.35s` (`15.05%`); `core/types.Sender` = `36.39s` (`13.25%`); `secp256k1_ext_ecdsa_recover` = `33.02s` (`12.02%`). | `[ĐO]` | `/tmp/p06_bins/pprof -top -cum $D/ab_4m_next_p1_run1_val0_cpu30.pb.gz` |
| **CPU & Allocs của `evalTxSignature`** | `verifySignatures.func4` = **`12.30s` CPU (`4.48%`)**, trong đó `evalTxSignature` = **`9.52s` CPU (`3.47%`)** (`ValidateEnvelopeBinding` = `3.87s`, `storeVerifiedBound` = `1.81s`, `sigCacheKey` = `1.47s`, `LoadVerifiedSignature` = `0.98s`, `isNodeBLSIdentity` = `0.71s`). Về bộ nhớ cấp phát: `evalTxSignature` = **`5,164.70 MB` (`3.99%` tổng `alloc_space`)** (`ValidateEnvelopeBinding` = `1,971.56 MB`, `storeVerifiedBound` = `1,464.90 MB`, `blsAllowed`/`isNodeBLSIdentity` = `1,025.71 MB`). | `[ĐO]` | `/tmp/p06_bins/pprof -peek 'evalTxSignature' $D/ab_4m_next_p1_run1_val0_{cpu30,allocs}.pb.gz` |
| **Allocs trong `VerifyTransaction` (Admission)** | `VerifyTransaction` = **`3,402.79 MB` (`2.63%` `alloc_space`)**, trong đó gọi **`transaction.ValidateEnvelopeBinding` = `600.52 MB`** VÀ gọi **`(*Transaction).ValidEthSign` = `603.52 MB`** (bên trong `ValidEthSign` lại gọi `validateEnvelopeBindingTx` lần thứ 2!). | `[ĐO]` | `/tmp/p06_bins/pprof -peek 'VerifyTransaction$' $D/ab_4m_next_p1_run1_val0_allocs.pb.gz` |
| **Allocs của `e_types.NewLondonSigner`** | `e_types.NewLondonSigner` cấp phát mới liên tục trên mỗi tx: tổng **`2,237.58 MB` (`1.73%` `alloc_space`)**, gồm `1,757.56 MB` từ `validateDirectEnvelopeBinding`, `254.51 MB` từ `FromEthEIP1559Tx`, và `225.51 MB` từ `ValidateEthTxEnvelope`. | `[ĐO]` | `/tmp/p06_bins/pprof -peek 'NewLondonSigner' $D/ab_4m_next_p1_run1_val0_allocs.pb.gz` |
| **Heap `inuse_objects` (Ingress)** | Tổng `9,238,151` objects (`903.19 MB`). Cây con `convertRawEth` → `App.ConvertRawEthTxToMetaTx` chiếm **`5,756,383` objects (`62.31%`)** và **`330.57 MB` (`36.60%` `inuse_space`)**; riêng `NewTransactionFromEth` = `2,211,841` objects (`23.94%`, `189.06 MB`), `ethTx.UnmarshalBinary` = `2,026,239` objects (`21.93%`). | `[ĐO]` | `/tmp/p06_bins/pprof -sample_index=inuse_objects -top -cum $D/ab_4m_next_p1_run1_val0_heap.pb.gz` |
| **CPU Xapian CGo (Batch vs Single)** | `_Cfunc_database_index_documents_batch_packed` = **`31.83s` (`11.59%` CPU)**; hàm gọi đơn lẻ `_Cfunc_database_index_document` vẫn còn **`9.54s` (`3.47%` CPU)**. | `[ĐO]` | `/tmp/p06_bins/pprof -top -cum $D/ab_4m_next_p1_run1_val0_cpu30.pb.gz` |
| **Trạng thái bộ đo `ab_4m` hiện tại** | Mới chạy xong cặp `p1` (`n = 1` mỗi nhánh: TPS end-to-end `prev = 15,096.0`, `next = 15,763.2`, `loss = 0`). Các cặp `p2..p4` chưa chạy. | `[ĐO]` | `$D/ab_4m_{prev,next}_p1_summary.csv` |

---

### 0.2. Ba nguyên nhân gốc rễ (Root Causes) vừa phát hiện từ đối chiếu Code + Profile

1. **Root Cause #1 — Lệch khóa cache `sigCacheKey` trong [`evalTxSignature`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/signature_enforcement.go#L353-L362) làm miss cache 100% sau `ValidateEnvelopeBindingsParallel` (`[ĐỌC CODE]` + `[ĐO]`):**
   - Trong `PrepareTransactions` ([`speculative_executor.go:1192`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/speculative_executor.go#L1192)), `ValidateEnvelopeBindingsParallel` kiểm tra `IsVerifiedEnvelopeBound(tx)` và lưu kết quả qua [`RecordVerifiedEnvelopeBound(tx)`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/signature_enforcement.go#L64-L69) với khóa **`sigCacheKey(tx, nil)`** (`blsKey = nil`).
   - Tại bước admission [`VerifyTransaction`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/validation.go#L246-L258), nhánh `pol.secp && !isNodeBLSIdentity(tx, as)` cũng lưu `boundSigKey` với khóa **`sigCacheKey(tx, nil)`** (`blsKey = nil`).
   - **Thế nhưng** ngay sau `PrepareTransactions` vài mili-giây trong cùng block, khi `FilterInvalidSignatures` / `PrewarmSignatureCache` gọi [`evalTxSignature`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/signature_enforcement.go#L353-L362), các dòng 353–358 lại viết:
     ```go
     var blsKey []byte
     if as != nil {
         blsKey = as.PublicKeyBls()
     }
     hasEnvelope := len(tx.RawEnvelope()) > 0
     key := sigCacheKey(tx, blsKey)
     ```
   - Vì các tài khoản ví trong `gate_4val_p06` đều đã đăng ký `PublicKeyBls()` (48 bytes) từ template, `blsKey` luôn khác `nil` mặc dù `!pol.blsAllowed(tx, as)` (chế độ `secp` không dùng BLS cho user tx) và mặc dù ở dòng 381–382 mọi tx có `hasEnvelope && tx.Type() != 0xFF` đều có kết quả xác thực hoàn toàn độc lập với `blsKey`!
   - **Hậu quả `[ĐO]`:** `LoadVerifiedSignature(boundSigKey(key))` ở dòng 361 **miss 100%**, khiến `evalTxSignature` phải chạy lại `ValidateEnvelopeBinding(tx)` (`3.87s` CPU, `1,971.56 MB` allocs), chạy lại `storeVerifiedBound(key)` (`1.81s` CPU, `1,464.90 MB` allocs), và gọi `bumpVerifiedSignatureCacheCount()` thêm một lần nữa cho mọi tx trong block!

2. **Root Cause #2 — Xoay vòng sớm `rotatingSignatureCache` làm mất toàn bộ cache của `30.58%` tx cục bộ trước khi kịp vào block (`[ĐỌC CODE]` + `[TÍNH]` từ `[ĐO]`):**
   - Trong [`validation.go:132`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/validation.go#L132), `maxVerifiedSignaturesCacheSize = 500_000`. Khi `verifiedSignaturesCacheCount` chạm `500,000`, `active` chuyển thành `old` và `old` cũ bị xóa bỏ hoàn toàn → cửa sổ lưu trữ tối đa chỉ là `2 × 500,000 = 1,000,000` lần gọi `bumpVerifiedSignatureCacheCount()` `[ĐỌC CODE]`.
   - Trong lượt chạy 4M (`ab_4m_next_p1_run1.json`), mỗi tx cục bộ tăng biến đếm 3 lần (`VerifyTransaction` + `ValidateEnvelopeBindingsParallel` + `evalTxSignature`) và mỗi tx từ peer tăng 2 lần → tổng cộng **`~9.25 triệu` lần bump** trong `254.4s` (`~36,400` bumps/giây) `[TÍNH]`.
   - Với tốc độ `~36,400` bumps/giây, một entry trong `rotatingSignatureCache` bị xóa chỉ sau **`1,000,000 / 36,400 ≈ 27.5 giây`** `[TÍNH]`, trong khi thời gian trung vị từ lúc admission vào mempool tới lúc được commit vào block là **`latency_p50 = 124.78 giây`** (`backlog_at_inject_end = 1,685,048` tx) `[ĐO]`.
   - **Hậu quả:** Gần như toàn bộ `1,226,515` tx (`30.58%`) mà `val0` đã xác thực ở bước admission bị xóa khỏi `rotatingSignatureCache` trước khi được đóng gói vào block, buộc `ValidateEnvelopeBindingsParallel` phải giải mã lại `RawEnvelope` và chạy lại `secp256k1_ext_ecdsa_recover` cho cả tx cục bộ!

3. **Root Cause #3 — Gọi đúp `validateEnvelopeBindingTx` trong `VerifyTransaction` & cấp phát mới `e_types.NewLondonSigner` mỗi giao dịch (`[ĐỌC CODE]` + `[ĐO]`):**
   - Trong [`VerifyTransaction`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/validation.go#L180-L258):
     - Dòng 180–185 kiểm tra `!IsVerifiedEnvelopeBound(tx)` và gọi `transaction.ValidateEnvelopeBinding(tx)` (`600.52 MB` `alloc_space` `[ĐO]`).
     - Dòng 250 gọi tiếp `tx.ValidEthSign()` (`603.52 MB` `alloc_space` `[ĐO]`), mà bên trong [`ValidEthSign()`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/transaction.go#L1299-L1305) khi `len(t.proto.RawEnvelope) > 0` lại chỉ gọi `validateEnvelopeBindingTx(t)` lần thứ 2 trên chính giao dịch đó!
   - Trong [`validateDirectEnvelopeBinding`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/eth_validation.go#L190-L199), [`FromEthEIP1559Tx`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/transaction_eth.go), và [`ValidateEthTxEnvelope`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/eth_validation.go): mỗi lần xử lý 1 tx lại gọi `e_types.NewLondonSigner(txChainID)` tạo struct mới và `big.Int` mới trên heap, ngốn **`2,237.58 MB` `alloc_space`** `[ĐO]`.

---

## 1. Quy tắc bắt buộc cho Agent thực hiện

1. **Đọc tài liệu gốc:** Đọc [`AGENTS.md`](file:///home/abc/chain-n/metanode/AGENTS.md) và [`PROJECT_STRUCTURE.md`](file:///home/abc/chain-n/metanode/PROJECT_STRUCTURE.md) trước khi sửa code.
2. **Kiểm tra Blast Radius & Build:**
   - Dùng `grep -rn` hoặc công cụ `codegraph` để kiểm tra toàn bộ nơi gọi trước khi sửa.
   - Sau mỗi thay đổi code, bắt buộc chạy `./consensus/metanode/scripts/build_check.sh --fast` và `go test -v -race` cho các package bị ảnh hưởng; đảm bảo 0 lỗi, 0 cảnh báo.
3. **Cô lập môi trường đo:**
   - Chỉ sử dụng dải cổng `31xxx` (cụm test cô lập `/tmp/gate_4val_tps_counted`). Tuyệt đối không chạm vào `/opt/metanode`, cụm `230`/`231`, không dùng `sudo`, không dùng `pkill -f` diện rộng.
   - Trước mỗi lượt chạy: kiểm tra `ss -ltn | grep -c ":31[0-9][0-9][0-9]"` phải bằng `0`; `MemAvailable >= 20 GB`; `/tmp` trống `>= 100 GB`.
   - Dùng `/tmp/p06_bins/pprof` để đọc file `.pb.gz`.
4. **Chống bịa số liệu (`AGENTS.md` Phần 2.6):**
   - Trước khi chạy A/B test, ghi đầy đủ commit hash và `sha256sum` của cả 2 binary vào `PREREGISTERED.md`.
   - Báo cáo trung thực cả khi một metric không cải thiện hoặc `p >= 0.05`.

---

## 2. Chi tiết các Công việc cần thực hiện (Theo thứ tự)

### Việc 0 — Hoàn tất bộ đo `ab_4m` (`p2..p4`) của đợt tối ưu trước (Không sửa code)

Bộ đo tại `note/evidence/keccak_blockcache_xapian_batch_20261017/ab_4m/` mới hoàn thành cặp `p1` (`ab_4m_prev_p1` và `ab_4m_next_p1`).
1. Kiểm tra lại 2 thư mục binary `/tmp/ab_keccak_xapian_20261017/bins_prev` và `/tmp/ab_keccak_xapian_20261017/bins_next` đối chiếu với `sha256sum` trong [`note/evidence/keccak_blockcache_xapian_batch_20261017/PREREGISTERED.md`](file:///home/abc/chain-n/metanode/note/evidence/keccak_blockcache_xapian_batch_20261017/PREREGISTERED.md). (Nếu `/tmp/ab_keccak_xapian_20261017` đã bị dọn, build lại đúng từ commit `92727b9a` cho `bins_prev` và commit tương ứng cho `bins_next`, ghi rõ `sha256sum` vào `PREREGISTERED.md`).
2. Chạy tiếp các cặp `p2`, `p3`, `p4` theo đúng thứ tự trong `note/evidence/keccak_blockcache_xapian_batch_20261017/ab_4m/order.txt` (hoặc tạo script chạy riêng từ `p2` đến `p4` để không ghi đè `p1`).
3. Tính toán thống kê (`mean`, `SD`, `welch_t_test` / paired t-test từ `execution/scripts/test/evidence/stats_util.py`) trên đủ 4 cặp `p1..p4` và cập nhật dòng `#23` trong [`note/CLAIMS_LEDGER.md`](file:///home/abc/chain-n/metanode/note/CLAIMS_LEDGER.md#L30).

---

### Việc 1 — Sửa lỗi Miss Cache `evalTxSignature`, Gọi đúp `VerifyTransaction`, Xoay vòng sớm `rotatingSignatureCache`, và Cache `e_types.Signer`

> 🛡️ **Ràng buộc Zero-Fork (`AGENTS.md` Phần 2.5):** Mọi thay đổi dưới đây chỉ sửa khóa tra cứu cache cho khớp với ngữ nghĩa xác thực thực tế và tái sử dụng đối tượng `e_types.Signer` bất biến. **Tuyệt đối không bỏ qua kiểm tra chữ ký hay kiểm tra `ValidateEnvelopeBinding` đối với bất kỳ giao dịch nào chưa được xác thực đầy đủ trên chính validator đó.**

#### 1A. Sửa lệch khóa `sigCacheKey` trong `evalTxSignature` và `verifySignatures` ([`execution/pkg/blockchain/tx_processor/signature_enforcement.go`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/signature_enforcement.go))
- **Vị trí:** [`signature_enforcement.go:353-395`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/signature_enforcement.go#L353-L395) (`evalTxSignature`) và [`signature_enforcement.go:502-526`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/signature_enforcement.go#L502-L526) (`verifySignatures`).
- **Thay đổi cụ thể:**
  1. Trong `evalTxSignature(tx, as, pol)`:
     - Chỉ gán `blsKey = as.PublicKeyBls()` khi `tx.Type() != 0xFF && pol.blsAllowed(tx, as)`:
       ```go
       var blsKey []byte
       if as != nil && tx.Type() != 0xFF && pol.blsAllowed(tx, as) {
           blsKey = as.PublicKeyBls()
       }
       ```
     - Ngoài ra, với giao dịch có `hasEnvelope && tx.Type() != 0xFF && !tx.GetReadOnly()`, kiểm tra `if IsVerifiedEnvelopeBound(tx) { return sigVerdictBoundHit }` ngay trước khi gọi `transaction.ValidateEnvelopeBinding(tx)` (vì với `hasEnvelope && tx.Type() != 0xFF`, `ValidateEnvelopeBinding(tx)` đã chứng minh toàn bộ chữ ký secp256k1 và các trường proto khớp với `RawEnvelope`, độc lập với `blsKey`).
     - Khi `ok && hasEnvelope && tx.Type() != 0xFF && len(blsKey) == 0`, chỉ gọi `storeVerifiedBound(key)` một lần (đồng nhất với `RecordVerifiedEnvelopeBound(tx)`).
  2. Trong `verifySignatures` ([`signature_enforcement.go:502-515`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/signature_enforcement.go#L502-L515)):
     - Ở nhánh `hasEnvelope`, kiểm tra `if LoadVerifiedSignature(boundSigKey(key)) || IsVerifiedEnvelopeBound(txs[i])` để nếu `ValidateEnvelopeBindingsParallel` vừa verify xong `txs[i]` thì nhánh BLS cũng không gọi lại `ValidateEnvelopeBinding(txs[i])`.

#### 1B. Loại bỏ gọi đúp `validateEnvelopeBindingTx` trong `VerifyTransaction` ([`execution/pkg/blockchain/tx_processor/validation.go`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/validation.go))
- **Vị trí:** [`validation.go:179-186`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/validation.go#L179-L186) và [`validation.go:246-260`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/validation.go#L246-L260).
- **Thay đổi cụ thể:**
  - Tại dòng 179–186: giữ biến `envelopeBoundOK := false`. Nếu `tx != nil && len(tx.RawEnvelope()) > 0`:
    - Nếu `IsVerifiedEnvelopeBound(tx)` là `true` → `envelopeBoundOK = true`.
    - Ngược lại, gọi `transaction.ValidateEnvelopeBinding(tx)`; nếu `err != nil` trả về `transaction.ErrEnvelopeBindingMismatch`, nếu `err == nil` đặt `envelopeBoundOK = true`.
  - Tại dòng 246–260 (và dòng 280): khi cần kiểm tra `tx.ValidEthSign()`, nếu `envelopeBoundOK && len(tx.RawEnvelope()) > 0` thì **không cần gọi lại `tx.ValidEthSign()`** (bởi vì trong [`transaction.go:1299-1305`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/transaction.go#L1299-L1305), khi `len(t.proto.RawEnvelope) > 0`, `ValidEthSign()` chỉ gọi duy nhất `validateEnvelopeBindingTx(t)` — tức là chính hàm `ValidateEnvelopeBinding(tx)` vừa chạy ở dòng 181!).

#### 1C. Nâng `maxVerifiedSignaturesCacheSize` để không bị xóa cache trước khi tx cục bộ được đóng vào block ([`execution/pkg/blockchain/tx_processor/validation.go`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/validation.go))
- **Vị trí:** [`validation.go:132`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/validation.go#L132).
- **Thay đổi cụ thể:**
  - Tăng `maxVerifiedSignaturesCacheSize` từ `500_000` lên `2_500_000` (cửa sổ `active + old` giữ được `5,000,000` entries, tốn ~160 MB RAM trên máy chủ 256 GB và tự động được dọn bởi `StartSignatureCacheCleanup`).
  - Kết hợp với 1A (giảm một nửa số lần gọi `bumpVerifiedSignatureCacheCount()`), toàn bộ `1,226,515` tx cục bộ trên `val0` trong bài test 4M sẽ được giữ nguyên trong cache cho tới lúc `PrepareTransactions` → `ValidateEnvelopeBindingsParallel` thực thi!

#### 1D. Cache đối tượng bất biến `e_types.Signer` theo `chainID` ([`execution/pkg/transaction/eth_validation.go`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/eth_validation.go) & [`execution/pkg/transaction/transaction_eth.go`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/transaction_eth.go))
- **Vị trí:** [`eth_validation.go:70-115`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/eth_validation.go#L70-L115) (`ValidateEthTxEnvelope`), [`eth_validation.go:175-230`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/eth_validation.go#L175-L230) (`validateDirectEnvelopeBinding`), [`transaction.go:1518-1575`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/transaction.go#L1518-L1575) (`DeriveSenderFromEthTransaction`), và các hàm `FromEth*Tx` trong [`transaction_eth.go`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/transaction_eth.go).
- **Thay đổi cụ thể:**
  - Tạo các hàm helper thread-safe (ví dụ: `cachedEIP155Signer(chainID *big.Int)`, `cachedLondonSigner(chainID *big.Int)`, `cachedCancunSigner(chainID *big.Int)`, `cachedPragueSigner(chainID *big.Int)`) sử dụng `atomic.Pointer` lưu cặp `(chainIDUint64 uint64, signer e_types.Signer)` cho trường hợp phổ biến `chainID.IsUint64() && chainID.Sign() > 0` (như `chainID = 991`). Nếu `chainID` khớp với entry trong `atomic.Pointer`, trả về ngay `signer` đã tạo mà không cấp phát struct hay `big.Int` mới; nếu khác thì tạo mới và cập nhật `atomic.Pointer`.
  - Việc này hoàn toàn thuần túy (pure), thread-safe 100% (vì `e_types.Signer` của `go-ethereum` chỉ đọc sau khi khởi tạo), và loại bỏ **`2,237.58 MB` `alloc_space`** `[ĐO]`.

#### 1E. (Tùy chọn an toàn) Tránh gọi `MarshalBinary()` thừa trong `ConvertRawEthTxToMetaTx` / `NewTransactionFromEth`
- Trong [`eth_tx_converter.go:49`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/eth_tx_converter.go#L49) và [`transaction.go:141-185`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/transaction.go#L141-L185):
  - Lưu ý bảo toàn hash theo ADR D3 (`keccak256(RawEnvelope)`): chỉ được tránh `MarshalBinary()` nếu chứng minh bằng unit test hoặc giữ nguyên `MarshalBinary()` nếu muốn tuyệt đối không chạm vào byte `RawEnvelope` của RLP không chuẩn. **Khuyến nghị:** Giữ nguyên `MarshalBinary()` nếu không muốn rủi ro lệch byte với RLP không chuẩn; chỉ cần làm **1A + 1B + 1C + 1D** là đã giải quyết phần lớn chi phí CPU và cấp phát mà 100% không đổi bất kỳ byte dữ liệu nào của transaction!

#### Unit Tests bắt buộc cho Việc 1:
- Chạy và bổ sung test trong `execution/pkg/blockchain/tx_processor/` và `execution/pkg/transaction/`:
  1. Test chứng minh với tài khoản có `len(as.PublicKeyBls()) > 0` ở chế độ `secp`: sau khi gọi `RecordVerifiedEnvelopeBound(tx)` (hoặc `VerifyTransaction`), lời gọi `evalTxSignature(tx, as, pol)` trả về đúng `sigVerdictBoundHit` (thay vì `sigVerdictVerified` như trước khi sửa!).
  2. Test chứng minh `VerifyTransaction` chỉ chạy `ValidateEnvelopeBinding` đúng 1 lần và điền cả `secpCacheKey` lẫn `boundSigKey(secpCacheKey)`.
  3. Test chứng minh `cachedLondonSigner` / `cachedCancunSigner` / `cachedPragueSigner` trả về cùng kết quả `e_types.Sender` và `Signer.Hash` trên nhiều `chainID` khác nhau và an toàn dưới `go test -race`.

---

### Việc 2 — Tối ưu gom lô Xapian (`maxWorkerBatchDrain` trong `execution/pkg/explorer/service.go`)

- **Hiện trạng `[ĐO]`:** Trong `$D/ab_4m_next_p1_run1_val0_cpu30.pb.gz`, `_Cfunc_database_index_documents_batch_packed` chiếm `31.83s` (`11.59%` CPU), nhưng hàm gọi đơn lẻ `_Cfunc_database_index_document` vẫn tốn **`9.54s` (`3.47%` CPU)**.
- **Nguyên nhân `[ĐỌC CODE]`:** [`defaultNumShards = 32`](file:///home/abc/chain-n/metanode/execution/pkg/explorer/service.go#L25) nhưng [`maxWorkerBatchDrain = 64`](file:///home/abc/chain-n/metanode/execution/pkg/explorer/service.go#L28), nên mỗi lần worker gom tối đa 64 jobs chia cho 32 shards chỉ đạt trung bình `64 / 32 = 2` docs/shard. Khi một shard trong lô chỉ có đúng `1` doc (`len(docs) == 1`), [`IndexDocumentsBatch`](file:///home/abc/chain-n/metanode/execution/pkg/goxapian/xapian.go#L198-L200) rơi về nhánh gọi đơn `d.IndexDocument(docs[0])`.
- **Thay đổi cụ thể:**
  1. Trong [`execution/pkg/explorer/service.go:28`](file:///home/abc/chain-n/metanode/execution/pkg/explorer/service.go#L28), tăng `maxWorkerBatchDrain` từ `64` lên `256` (trung bình `256 / 32 = 8` docs/shard khi hàng đợi đầy, xác suất 1 shard chỉ có 1 doc giảm từ $\approx 27\%$ xuống $< 0.3\%$ theo phân phối Poisson($\lambda=8$) `[TÍNH]`).
  2. Chạy `go test -v -race ./pkg/goxapian/... ./pkg/explorer/...` để đảm bảo toàn bộ test hiện có pass sạch sẽ.

---

### Việc 3 — Đo kiểm chứng A/B thực tế cho Việc 1 + Việc 2

1. Tạo thư mục bằng chứng mới `note/evidence/envelope_cache_signer_xapian_20261017/`.
2. Viết file `PREREGISTERED.md` trước khi chạy:
   - Ghi rõ commit hash và `sha256sum` của `bins_prev` (commit `09b4d706`) và `bins_next` (commit sau khi làm Việc 1 + Việc 2).
   - Tiêu chí kiểm chứng:
     - Mọi lượt chạy phải có `loss_vs_sent == 0` và `parity_status` bắt đầu bằng `OK:`.
     - Trong `cpu30.pb.gz` và `allocs.pb.gz` của `bins_next`:
       - Chi phí `ValidateEnvelopeBinding` bên trong `evalTxSignature` (`3.87s` CPU / `1,971.56 MB` allocs ở `bins_prev`) giảm về `~0` (do đạt `sigVerdictBoundHit`).
       - Chi phí cấp phát `e_types.NewLondonSigner` (`2,237.58 MB` ở `bins_prev`) giảm `> 95%`.
       - Chi phí CPU của `ValidateEnvelopeBindingsParallel` (`16.09%` ở `bins_prev`) giảm nhờ giữ được cache `boundSigKey` cho ~30% tx cục bộ.
       - Chi phí CPU của `_Cfunc_database_index_document` (nhánh gọi đơn, `3.47%` ở `bins_prev`) giảm xuống `< 0.5%`.
3. Chạy A/B test ghép cặp xen kẽ bằng `execution/scripts/test/evidence/run_tps_counted.py` (tối thiểu 4 cặp ở 1M tx + các cặp ở 4M tx có bật `--profile-at 60`), thu thập đầy đủ `.json`, `.csv`, `.pb.gz`.
4. Tạo `manifest.json`, chạy `verify_evidence.py` và cập nhật `note/CLAIMS_LEDGER.md` kèm nhãn `[ĐO]` / `[TÍNH]` chính xác.
