# Preregistration: Envelope Binding Cache Fix, Signer Caching & Xapian Batch Drain 256 (2026-10-17)

## 1. Mục tiêu kiểm chứng
Kiểm chứng 5 cải tiến theo [note/plan_envelope_binding_and_xapian_drain_20261017.md](file:///home/abc/chain-n/metanode/note/plan_envelope_binding_and_xapian_drain_20261017.md):
1. **Sửa lệch khóa `sigCacheKey` trong `evalTxSignature`**: Chỉ truyền `PublicKeyBls` khi `pol.blsAllowed(tx, as)` và kiểm tra `IsVerifiedEnvelopeBound(tx)` để trả về `sigVerdictBoundHit` ngay trước khi validate lại.
2. **Loại bỏ gọi đúp `validateEnvelopeBindingTx` trong `VerifyTransaction`**: Dùng cờ `envelopeBoundOK` tránh gọi lại `ValidEthSign` khi đã xác thực envelope ở admission.
3. **Nâng `maxVerifiedSignaturesCacheSize` lên 2,500,000**: Đảm bảo cache lưu được 5,000,000 entries (2 epochs) để toàn bộ tx cục bộ không bị xoay vòng sớm trước khi được commit vào block.
4. **Cache bất biến `e_types.Signer` theo `chainID`**: Tái sử dụng `LondonSigner`, `CancunSigner`, `PragueSigner`, `EIP155Signer` qua `atomic.Pointer`.
5. **Nâng `maxWorkerBatchDrain` lên 256**: Giảm thiểu nhánh gọi đơn `_Cfunc_database_index_document` xuống `< 0.5%` CPU.

## 2. Binaries & SHA256 đối chứng

| Role | Commit / Source | Binary Path | SHA256 Hash |
| :--- | :--- | :--- | :--- |
| **BASE (`bins_prev`)** | `09b4d706` (sau Pha 1 & 2) | `/home/abc/.gemini/antigravity-ide/brain/f00e90cc-ba38-4bf9-9958-55a73afe6492/scratch/bins_envelope_prev/simple_chain` | `d7095796b753b212ed3523925b1cd8fd7afbb1f594d14578044daa984778edb4` |
| **OPT (`bins_next`)** | `8a42c8aa` (Việc 1 + 2, release Rust FFI) | `/home/abc/.gemini/antigravity-ide/brain/f00e90cc-ba38-4bf9-9958-55a73afe6492/scratch/bins_envelope_next/simple_chain` | `70c4a0be299ac3b17ee90c0e5659cc740586a600bf8abe1fe464d143bd141cf6` |
| **Parent Chain** | Chung | `/home/abc/.gemini/antigravity-ide/brain/f00e90cc-ba38-4bf9-9958-55a73afe6492/scratch/bins_envelope_next/parent_chain` | `5dbc58e2918fc924840dec6f6befb6c994c011b3f143c8d36ed22ef6dfc573c8` |
| **Load Tool** | Chung | `/home/abc/.gemini/antigravity-ide/brain/f00e90cc-ba38-4bf9-9958-55a73afe6492/scratch/bins_envelope_next/secp_tps_blast` | `6cbc5a35f29dbd07fee0e8468c754903fb5ab5cc082e1adbbe2c2d31d09f3614` |

## 3. Tiêu chí Đạt (Pass Criteria)
1. **Zero-Fork Invariant (`AGENTS.md` Part 2.5):** Mọi lượt chạy bắt buộc có `loss_vs_sent == 0` và `parity_status` bắt đầu bằng `OK: all 4 validators agree on hash+stateRoot`.
2. **Profile tiêu biểu (`cpu30.pb.gz`, `allocs.pb.gz`):**
   - Chi phí `ValidateEnvelopeBinding` trong `evalTxSignature` giảm về `~0` nhờ `sigVerdictBoundHit`.
   - Cấp phát heap từ `NewLondonSigner` giảm `> 95%`.
   - Chi phí CPU của `_Cfunc_database_index_document` giảm xuống `< 0.5%`.
3. **Hiệu năng hệ thống:** TPS end-to-end của `bins_next` cao hơn hoặc bằng `bins_prev`, latency p50 giảm.

## 4. Ghi chú kiểm tra trước khi commit (2026-10-19)
- `[ĐO]` `sha256sum` của 4 binary ở mục 2 trùng khớp với giá trị đã ghi; hai binary `simple_chain` BASE và OPT khác nhau.
- `[ĐỌC CODE]` Commit BASE `09b4d706` tồn tại. Commit OPT `8a42c8aa` (2026-10-17 17:39:34) chứa `maxVerifiedSignaturesCacheSize = 2_500_000` và `maxWorkerBatchDrain = 256`.
- `[CHƯA ĐO]` Chưa build lại từ `8a42c8aa` để chứng minh binary OPT (mtime 17:39:06, trước commit 28 giây) sinh ra từ đúng cây mã đó; đây là suy luận từ thời gian, chưa phải bằng chứng. Chưa có kết quả đo nào trong thư mục này.
