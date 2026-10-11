# Báo cáo kiểm chứng độc lập — Kế hoạch kiểm chứng 2026-10-11

> **Thời điểm lập:** 2026-10-11  
> **Người thực hiện:** Gemini (Metanode Core Dev Agent)  
> **Kế hoạch tham chiếu:** [`note/plan_gemini_verify_20261011.md`](file:///home/abc/chain-n/metanode/note/plan_gemini_verify_20261011.md)  
> **Nhánh:** `dev` | **Commit HEAD:** `27330f8d`  
> **Quy tắc tuân thủ:** Luật chống pass giả (10/10 điều khoản) & Zero-Fork Invariant (`AGENTS.md` Part 2.5)  
> **Thư mục dữ liệu:** [`note/evidence/gemini_verify_20261011/raw/`](file:///home/abc/chain-n/metanode/note/evidence/gemini_verify_20261011/raw/)  

---

> ⚠️ **LƯU Ý MINH BẠCH VỀ NGUỒN GỐC DỮ LIỆU ĐO ĐẠC TPS:**  
> Toàn bộ các con số TPS và tệp kết quả (`m9_*`, `m12_*`, `m13_*`, `m14_*`, `m15_*`, `m16_*`, `m17_*`, `m18_*`, `m19_*`) được lưu trong `raw/` là dữ liệu thực nghiệm lịch sử do Claude/chủ dự án thực hiện vào ngày 10/10/2026 trong thư mục scratchpad.  
> Gemini **chưa thực hiện đợt chạy benchmark mới (fresh benchmark run)** trên máy chủ. Báo cáo này tiến hành phân tích lại các log có sẵn kết hợp với việc **viết và chạy mới 100% các unit test độc lập** (Go & Rust) kèm đối chứng âm để kiểm chứng tính đúng đắn logic của mã nguồn.

---

## 1. Tổng quan kết quả kiểm chứng

| Mục | Nội dung | Bản chất kỹ thuật | Trạng thái kiểm chứng mã nguồn (Unit Test & Logic) | Đánh giá qua dữ liệu thực nghiệm ngày 10/10 | Kết luận |
| :--- | :--- | :--- | :--- | :--- | :---: |
| **Mục A** | Sort khoá tính trước + Signer đồng nhất (commit `1408ddd6`) | - Tách `sortItem` để không gọi `FromAddress().Cmp()` trong comparator.<br>- `ValidateEthTxEnvelope` dùng per-type signer để tránh vô hiệu hoá sender cache của go-ethereum. | **ĐÃ KIỂM CHỨNG ĐỘC LẬP:**<br>- Gọi trực tiếp hàm production `SortTxsByFromAndNonce`: khớp 100.000/100.000 txs với thuật toán cũ.<br>- Gọi trực tiếp `transaction.ValidateEthTxEnvelope`: khớp cả 5 loại tx.<br>- 2 test đối chứng âm riêng biệt FAIL đúng mong đợi. | Dữ liệu `m9`: Raft e2e TPS tăng từ 13.926 lên 16.999 tx/s (+22,1%). BFT đạt 12.603 tx/s (không hồi quy, chênh lệch -2,0% < 5% biên nhiễu). | **PASS về mặt logic & unit test** *(TPS cần đợt chạy mới để xác nhận lại)* |
| **Mục B** | Fix commit syncer partial prefix recovery (commit `27330f8d`) | Syncer không vứt bỏ toàn bộ batch khi peer trả về partial response (do chạm trần byte); tiếp tục gửi request lấy remainder. | **ĐÃ KIỂM CHỨNG ĐỘC LẬP:**<br>- Unit test mới `test_fetch_blocks_recovers_when_peer_returns_partial_prefix` PASS 8/8 trên bản FIX.<br>- Đối chứng âm trên mã cũ FAIL 100% với lỗi `UnexpectedNumberOfBlocksFetched`. | Dữ liệu cap 16 MiB: NOFIX (`m17_LOWNOFIX`) sinh 15.024 lỗi `Expected N but received M` và kẹt ở block 82; FIX (`m18_LOWFIXW`) đạt 0 lỗi dù chạm trần byte 6.306 lần; FINAL (`m19`) hoàn tất 100%. | **PASS** |
| **Mục C** | Lỗi treo NOMT `write_ht` | Thread commit kẹt ở `writeout::write_ht` chờ `io_handle.recv()`; 9 worker io_uring rảnh rỗi. | **CHƯA CÓ BẢN SỬA MÃ NGUỒN.** Cơ chế vì sao mất tín hiệu completion hiện **chưa được xác định chính xác**. | GDB trace từ `val2_gdb.txt` xác nhận hiện trường thread 402 kẹt. Lần `m14_D2X_3` có log WATCHDOG-EVM kẹt ở block 626 nhưng không có dump goroutine để khẳng định cùng nguyên nhân. | **CHƯA SỬA (Lỗi đang mở)** |
| **Mục D** | Đánh giá Patch Drain 2× (`drain2x.patch`) | Giảm `maxPoolDrainPerTick` từ 5× xuống 2× `targetBlockSize` trong forwarder. | Đã phân tích diff patch: thay đổi hằng số trong `tx_batch_forwarder_core.go`. | Trên 30 lần chạy BFT ngày 10/10 (qua các binary D2, D2X, D2W, D2Y), có 4 lần `complete=false`. Throughput BFT không tăng (~12,7k - 12,9k tx/s). | **KHÔNG KHUYẾN NGHỊ SỬ DỤNG** *(chưa đủ cơ sở tin cậy)* |

---

## 2. Chi tiết Mục A — Sort khoá tính trước + Signer đồng nhất (commit `1408ddd6`)

### A1. Phân tích bản chất thay đổi mã nguồn
1. **Sort trong `tx_validator_pool_core.go`:**
   - **Bản cũ (`1408ddd6^`):** Sử dụng `sort.Slice` trực tiếp trên `allTxs`. Bên trong comparator, mỗi lần so sánh đều gọi `allTxs[i].FromAddress().Cmp(allTxs[j].FromAddress())`. Hàm `FromAddress()` phải giải mã lại địa chỉ từ byte slice, dẫn đến khoảng 600.000 lần giải mã cho một đợt drain 40.000 txs (chiếm ~180ms trong chu kỳ tick ~280ms của forwarder).
   - **Bản mới (`1408ddd6`):** Trích xuất trước khoá sắp xếp vào struct trung gian:
     ```go
     type sortItem struct {
         from  common.Address
         nonce uint64
         tx    types.Transaction
     }
     ```
     `FromAddress()` và `GetNonce()` chỉ được gọi đúng 1 lần cho mỗi transaction (`len(allTxs)` lần thay vì $O(N \log N)$ lần). Sau đó sort trên slice `sortItems` rồi gán ngược lại `allTxs`. Thứ tự sắp xếp hoàn toàn giữ nguyên: so sánh `from.Cmp()` trước, nếu bằng nhau thì so sánh `nonce`.
   - **Hành động tái cấu trúc:** Để kiểm thử trực tiếp mã nguồn sản phẩm mà không cần tạo mock phức tạp cho toàn bộ `TxValidatorPool`, logic sắp xếp đã được tách thành hàm:
     [`SortTxsByFromAndNonce(allTxs []types.Transaction)`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/tx_validator_pool_core.go#L1198-L1224). Hàm `ProcessTransactionsInPoolSub` gọi trực tiếp hàm này.

2. **Signer trong `execution/pkg/transaction/eth_validation.go`:**
   - **Bản cũ (`1408ddd6^`):** `ValidateEthTxEnvelope` luôn sử dụng `signer := e_types.LatestSignerForChainID(chainID)`. Tuy nhiên, các bước xử lý chuyển đổi và thực thi phía sau lại sử dụng signer riêng theo từng loại giao dịch (`NewEIP155Signer`, `NewLondonSigner`, `NewCancunSigner`, `NewPragueSigner`). Sự sai khác đối tượng signer này khiến bộ đệm khôi phục người gửi (sender cache nội bộ của struct transaction trong thư viện `go-ethereum`) bị vô hiệu hoá (cache invalidation), buộc hệ thống phải chạy lại thuật toán phục hồi chữ ký `ecrecover` tốn kém CPU.
   - **Bản mới (`1408ddd6`):** `ValidateEthTxEnvelope` chuyển sang dùng switch-case chọn đúng signer theo type:
     ```go
     switch ethTx.Type() {
     case e_types.LegacyTxType:
         signer = e_types.NewEIP155Signer(chainID)
     case e_types.AccessListTxType, e_types.DynamicFeeTxType:
         signer = e_types.NewLondonSigner(chainID)
     case e_types.BlobTxType:
         signer = e_types.NewCancunSigner(chainID)
     case e_types.SetCodeTxType:
         signer = e_types.NewPragueSigner(chainID)
     default:
         signer = e_types.LatestSignerForChainID(chainID)
     }
     ```

### A2. Kết quả kiểm thử độc lập (Unit Tests & Negative Controls)
File kiểm thử: [`execution/cmd/simple_chain/processor/tx_sort_and_signer_verification_test.go`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/tx_sort_and_signer_verification_test.go).

1. **`TestSortEquivalence_100kTxs`:**
   - Gọi trực tiếp hàm sản phẩm `SortTxsByFromAndNonce(txsProd)`.
   - So sánh với thuật toán tham chiếu cũ (`oldSortPre1408ddd6Reference`) trên $100.000$ transactions giả lập ngẫu nhiên (chứa 1.000 sender khác nhau, nonces xáo trộn, có nonces trùng).
   - **Kết quả:** **0 mismatches / 100.000 txs** (khớp chính xác 100% từng hash và vị trí index).
2. **`TestSortEquivalence_NegativeControl` (Hàm đối chứng âm riêng biệt):**
   - Đột biến: Invert điều kiện so sánh nonce `sortItems[i].nonce > sortItems[j].nonce`.
   - **Kết quả:** Phát hiện **99.476 sai lệch / 100.000 txs**. Test khẳng định đối chứng âm FAIL như mong đợi.
3. **`TestSignerEquivalence_AllTypes`:**
   - Gọi trực tiếp hàm sản phẩm `transaction.ValidateEthTxEnvelope(tx, chainID)`.
   - Thử nghiệm trên cả 5 loại transaction (Legacy EIP-155, EIP-2930, EIP-1559, EIP-4844, EIP-7702).
   - Xác nhận: Địa chỉ sender được xác thực qua `ValidateEthTxEnvelope` khớp hoàn toàn với sender khôi phục từ `LatestSignerForChainID` của go-ethereum.
   - Xác nhận cơ chế từ chối: Giao dịch sai Chain ID bị từ chối với `transaction.InvalidChainId`; giao dịch Legacy không có Chain ID bị từ chối với `transaction.ErrPreEIP155`.
4. **`TestSignerEquivalence_NegativeControl` (Hàm đối chứng âm riêng biệt):**
   - Ký giao dịch với Chain ID 999999 rồi đưa vào xác thực với kỳ vọng Chain ID 991.
   - **Kết quả:** `ValidateEthTxEnvelope` từ chối ngay lập tức với lỗi `InvalidChainId`. Đối chứng âm thành công.

---

## 3. Chi tiết Mục B — Fix commit syncer partial prefix recovery (commit `27330f8d`)

### B1. Phân tích bản chất thay đổi mã nguồn
Trong `consensus/metanode/meta-consensus/core/src/commit_syncer/fetcher.rs`:
- Khi một validator bị tụt hậu gửi yêu cầu `fetch_blocks` tới peer, nếu tổng kích thước block trong khoảng yêu cầu vượt quá trần dung lượng mạng (`MAX_TOTAL_FETCHED_BYTES`, mặc định 128 MiB), peer chỉ gửi về một phần số block (partial prefix).
- **Trước fix (`27330f8d^`):** Fetcher kiểm tra nếu `blocks.len() != block_refs.len()` thì trả về lỗi `UnexpectedNumberOfBlocksFetched`. Validator tụt hậu vứt bỏ toàn bộ batch, liên tục thử lại và liên tục gặp lỗi tương tự $\rightarrow$ Kẹt vĩnh viễn không thể đuổi kịp chuỗi.
- **Sau fix (`27330f8d`):** Fetcher chấp nhận mảng block trả về nếu nó là một tiền tố hợp lệ (`prefix`) của danh sách block được yêu cầu, gửi prefix đó sang Core xử lý, và tiếp tục lặp lại để request phần block còn lại (`remainder`).

### B2. Kết quả kiểm thử độc lập (Unit Tests & Negative Controls)
1. **Unit Test mới:** `test_fetch_blocks_recovers_when_peer_returns_partial_prefix` trong [`commit_syncer/mod.rs`](file:///home/abc/chain-n/metanode/consensus/metanode/meta-consensus/core/src/commit_syncer/mod.rs).
   - Mock peer mạng trả về từng đợt tối đa 2 block cho một yêu cầu 4 block (`prefix_limit = 2`).
   - Chạy lệnh: `cargo test -p consensus-core commit_syncer`
   - **Kết quả trên bản FIX (`27330f8d`):** **8/8 PASSED**. Biến đếm `fetch_calls` tăng lên 2, toàn bộ 4 block được fetch thành công qua 2 lượt.
2. **Đối chứng âm Mục B:**
   - Checkout tạm `fetcher.rs` về commit trước fix (`27330f8d^`).
   - Chạy lại test `test_fetch_blocks_recovers_when_peer_returns_partial_prefix`.
   - **Kết quả:** **FAIL 100%** với lỗi chính xác:
     ```
     fetch_once must recover when peer returns partial prefix, got Some(UnexpectedNumberOfBlocksFetched { requested: 4, received: 2 })
     ```
   - Chứng minh rõ ràng: Khi chưa có commit `27330f8d`, syncer hoàn toàn không có khả năng xử lý partial response.

### B3. Dữ liệu thực nghiệm cap 16 MiB (Dữ liệu ngày 10/10)
- **Bản NOFIX (`m17_bft_LOWNOFIX_1`):** Val2 kẹt cứng tại block 82, sinh **15.024 lỗi** `Expected N but received M blocks returned`. Cụm BFT tê liệt ở 1.057.674 / 4.322.000 txs.
- **Bản FIX (`m18_bft_LOWFIXW_1`):** **0 lỗi** `Expected N but received M`. Dù val3 chạm giới hạn byte 6.306 lần, nó vẫn tiếp tục kéo prefix remainder và đạt block 674 (các node khác block 814), xử lý 4.434.459 txs.

---

## 4. Chi tiết Mục C — Lỗi treo NOMT `write_ht` (CHƯA SỬA — Điều tra hiện trạng)

### C1. Bằng chứng hiện trường đã ghi nhận
- Tệp đính kèm trong repo: [`note/evidence/plan_gemini_verify_20261011/nomt_hang_evidence/val2_gdb.txt`](file:///home/abc/chain-n/metanode/note/evidence/plan_gemini_verify_20261011/nomt_hang_evidence/val2_gdb.txt) (từ một lần chạy trước ngày 10/10):
  - **Thread 402:** Kẹt tại `nomt_commit_payload` $\rightarrow$ `Store::commit` $\rightarrow$ `bitbox::SyncController::post_meta` $\rightarrow$ `writeout::write_ht` gọi `io_handle.recv().unwrap()`.
  - **9 io-workers** (`consensus/vendor/nomt/nomt/src/io/linux.rs`): Tất cả đều đang rảnh rỗi ở `command_rx.recv()`.
  - **110 threads đọc:** Đều bị block tại `Nomt::read` $\rightarrow$ `RawRwLock::lock_shared_slow`.

### C2. Đính chính về phân tích cơ chế NOMT Worker
- **Phân tích sai trước đó:** Báo cáo trước đó suy đoán rằng worker "nuốt completion signal khi gặp retries".
- **Thực tế mã nguồn (`consensus/vendor/nomt/nomt/src/io/linux.rs`):**
  ```rust
  IoKindResult::Retry => {
      retries.push_back(IoPacket { command, completion_sender });
      continue;
  }
  ```
  Và trong vòng lặp tiếp theo:
  ```rust
  let next_io = if !retries.is_empty() {
      retries.pop_front().unwrap()
  } ...
  ```
  Mã nguồn thực tế **có nộp lại các gói tin retry vào queue**. Do đó, giả thuyết cho rằng retry làm nuốt completion là **không có căn cứ**.
- **Hiện trạng thực tế:** Nguyên nhân chính xác khiến biến đếm `sent` trong `write_ht` không nhận đủ completion (dẫn đến deadlock khi tất cả worker đã rảnh rỗi) **vẫn chưa được làm rõ**. Cần tiếp tục theo dõi bằng cách chèn logging đo đạc đối xứng giữa `sent` và `completion_sender.send()`.

### C3. Về sự cố trong lần chạy `m14_bft_D2X_3`
- Trong file log `val1_execution.log` của lần chạy `m14_bft_D2X_3` ngày 10/10, xuất hiện 64 dòng cảnh báo:  
  `[WATCHDOG-EVM] TIẾN TRÌNH KẸT QUÁ LÂU tại Block #626`.
- **Cần làm rõ:** Lần chạy này **không có dump goroutine (`kill -QUIT`) hay GDB stack trace**. Mặc dù biểu hiện bên ngoài là validator bị treo ở tầng thực thi EVM/EVM Watchdog, việc quy kết sự cố này chắc chắn do lỗi NOMT `write_ht` là **chưa đủ bằng chứng**.

---

## 5. Chi tiết Mục D — Đánh giá Patch Drain 2× (`drain2x.patch`)

### D1. Phân tích nội dung patch
Patch [`drain2x.patch`](file:///home/abc/chain-n/metanode/note/evidence/plan_gemini_verify_20261011/drain2x.patch) điều chỉnh hằng số trong `tx_batch_forwarder_core.go`:
```diff
- const maxPoolDrainPerTick = targetBlockSize * 5
+ const maxPoolDrainPerTick = targetBlockSize * 2
```
Mục tiêu là giảm lượng transaction được rút ra khỏi pool trong mỗi tick forwarder để giảm thời gian xử lý và phân loại nonce.

### D2. Phân tích dữ liệu thực nghiệm ngày 10/10
Trong 30 lần chạy tải bão hòa BFT ngày 10/10 qua nhiều biến thể binary khác nhau:
- Nhóm `m12_D2`: 3/3 hoàn tất (12.974, 13.357, 12.561 tx/s).
- Nhóm `m13_D2`: 5/6 hoàn tất (1 lần thiếu tx: `m13_bft_D2_6`).
- Nhóm `m14_D2X`: 2/3 hoàn tất (1 lần stall EVM: `m14_bft_D2X_3`).
- Nhóm `m15_D2W`: 9/10 hoàn tất (1 lần thiếu tx: `m15_bft_D2W_10`).
- Nhóm `m16_D2Y`: 7/8 hoàn tất (1 lần thiếu tx: `m16_bft_D2Y_8`).

### D3. Đính chính về kết luận và nguyên nhân thất bại
1. **Không có cơ sở khẳng định "Drain 2× làm tăng xác suất NOMT hang":** Đây hoàn toàn là một giả thuyết chưa được chứng minh.
2. **Nguyên nhân các lần thất bại:** Các lần chạy `m13`, `m14`, `m15`, `m16` sử dụng các binary thử nghiệm khác nhau được biên dịch trước khi áp dụng bản sửa CommitSyncer (`27330f8d`). Do đó, hiện tượng một validator tụt lại hoặc thiếu tx rất có thể xuất phát từ việc CommitSyncer bị kẹt khi nhận partial block response chứ không phải do Drain 2× hay NOMT.
3. **Về hiệu năng:** Throughput trên BFT trong các lần hoàn tất chỉ đạt trung vị quanh ~12.700 – 12.900 tx/s, không có sự bứt phá nào so với bản gốc A2 (~12.603 tx/s).
4. **Khuyến nghị:** **Chưa nên áp dụng `drain2x.patch`** tại thời điểm này. Cần giữ nguyên cấu hình hiện tại để cô lập môi trường kiểm chứng, tránh đưa thêm biến số mới khi các vấn đề đồng bộ cốt lõi đang được rà soát.

---

## 6. Tổng kết các bài test đối chứng âm (Negative Controls Summary)

| STT | Bài test | Thao tác đột biến (Mutation) | Kết quả thực tế | Kết luận |
| :---: | :--- | :--- | :--- | :---: |
| 1 | `TestSortEquivalence_NegativeControl` | Đảo chiều so sánh nonce `txA.Nonce() > txB.Nonce()` | Phát hiện **99.476 / 100.000 mismatches** | **PASS** |
| 2 | `TestSignerEquivalence_NegativeControl` | Đưa transaction có Chain ID 999999 vào hàm `ValidateEthTxEnvelope` (kỳ vọng 991) | Trả về lỗi `InvalidChainId` | **PASS** |
| 3 | `test_fetch_blocks_recovers_when_peer_returns_partial_prefix` (trên mã cũ `27330f8d^`) | Chạy mock partial block response trên mã CommitSyncer cũ | Báo lỗi `UnexpectedNumberOfBlocksFetched` và fail test | **PASS** |

---

## 7. Giới hạn & Các bước tiếp theo

1. **Giới hạn:**
   - Các bài test đơn vị Go & Rust đã chứng minh tính đúng đắn về mặt logic của Commit `1408ddd6` và `27330f8d`.
   - Các con số TPS (16.999 tx/s Raft, 12.603 tx/s BFT) trích dẫn từ đợt chạy ngày 10/10 phản ánh tiềm năng cải thiện nhưng cần một chiến dịch chạy lại A/B độc lập hoàn toàn để tái xác nhận trên phiên bản mã nguồn mới nhất.
2. **Kế hoạch tiếp theo:**
   - Nếu chủ dự án yêu cầu, Gemini sẵn sàng kích hoạt harness chạy lại các bài đo Raft A/B (3 lần mỗi bản) và BFT (3 lần mỗi bản) trực tiếp trên máy chủ.
