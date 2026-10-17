# Bản đăng ký trước (Preregistration) — Tối ưu `e26ad843`, Giới hạn `blockCache` (64 entries), Gom lô CGo Xapian theo Shard (`IndexDocumentsBatch`), và Tối ưu `add_boolean_term` (2026-10-17)

- **Ngày đăng ký:** 2026-10-17 (ghi nhận trước khi bắt đầu bất kỳ lượt đo A/B nào)
- **Tài liệu cơ sở:** `note/plan_blockcache_xapian_batch_ab_20261017.md` và `note/CLAIMS_LEDGER.md` (Dòng 22 & Dòng 23)
- **Commit đối chứng cơ sở (`bins_prev`):** `5004cf0f2de1d2f727b15785b20b15585516b7fa` (mốc dòng 22 đã được kiểm chứng trong `note/CLAIMS_LEDGER.md`)
  - `bins_prev/simple_chain` SHA256: `ca856017300d5a5e8f9b4590b0057306a46f78cf55bdd1bf1fcdf00ba30cd301`
  - `bins_prev/parent_chain` SHA256: `5dbc58e2918fc924840dec6f6befb6c994c011b3f143c8d36ed22ef6dfc573c8`
  - `bins_prev/secp_tps_blast` SHA256: `6cbc5a35f29dbd07fee0e8468c754903fb5ab5cc082e1adbbe2c2d31d09f3614`
- **Binary ứng viên (`bins_next`):** Biên dịch từ `HEAD` mới nhất (gồm `e26ad843` keccakStatePool/inlining/bounded txsCache + `cb4a8d56` add_boolean_term + giới hạn `blockCache` 64 entries + gom lô CGo `IndexDocumentsBatch` theo shard)
  - `bins_next/simple_chain` SHA256: `d7095796b753b212ed3523925b1cd8fd7afbb1f594d14578044daa984778edb4`
  - `bins_next/parent_chain` SHA256: `5dbc58e2918fc924840dec6f6befb6c994c011b3f143c8d36ed22ef6dfc573c8`
  - `bins_next/secp_tps_blast` SHA256: `6cbc5a35f29dbd07fee0e8468c754903fb5ab5cc082e1adbbe2c2d31d09f3614`

---

## 1. Mục tiêu Kiểm chứng Định lượng

1. **Hiệu quả tổng hợp `e26ad843` (Cơ chế 1):**
   - Loại bỏ cấp phát `keccakStatePool`, inlining accessor `FromAddress()` / `Hash()`, bounded `txsCacheMap`, và batch pooling trong Mempool.
   - So sánh `alloc_space` và `alloc_objects` của `golang.org/x/crypto/sha3.NewLegacyKeccak256` và `TransactionPool.AddTransactions`.

2. **Hiệu quả Giới hạn `blockCache` (Cơ chế 2 — Pha 1):**
   - Giới hạn cứng `maxBlockCacheEntries = 64` (loại bỏ phình to RAM của hàng triệu `ExecuteSCResult` trong suốt 10 phút).
   - So sánh `inuse_space` và `inuse_objects` của `smart_contract.NewExecuteSCResult` trên `heap.pb.gz` ở quy mô 4M tx (`t=150s`). Kỳ vọng giảm mạnh từ `274.03 MB` / `2,565,540` objects xuống `<= 50 MB` / `< 500k` objects.

3. **Hiệu quả `add_boolean_term` + `IndexDocumentsBatch` (Cơ chế 3 — Pha 2):**
   - `doc.add_boolean_term` loại bỏ thống kê doclen / BM25 cho các khóa prefix `H`, `F`, `T`, `B`, `RH`.
   - `IndexDocumentsBatch` đóng gói nhiều tài liệu theo shard vào 1 contiguous packed buffer, giảm 20–30 lần số lần gọi `runtime.cgocall` và mutex lock trên mỗi block 10k tx.
   - So sánh CPU (`s` và `%`) của `_Cfunc_database_index_document` / `_Cfunc_database_index_documents_batch_packed` và `runtime.cgocall` trên `cpu30.pb.gz`.

---

## 2. Thiết kế Thí nghiệm A/B Đan xen

- **Môi trường:** Cụm 4 validator BFT cục bộ (bản sao sạch từ template mỗi lượt chạy), chế độ `sustained`, batch 1000.
- **Quy mô 1M tx (`ab_1m`):** 4 cặp đan xen (`prev` vs `next`, tổng 8 lượt, seed `2026101701`), kèm `--profile-at 20` ở Cặp 1 (`ab_1m_prev_p1`, `ab_1m_next_p1`).
- **Quy mô 4M tx (`ab_4m`):** 4 cặp đan xen (`prev` vs `next`, tổng 8 lượt, seed `2026101702`), kèm `--profile-at 150` ở Cặp 1 (`ab_4m_prev_p1`, `ab_4m_next_p1`).
- **Ràng buộc:** Cả 16/16 lượt chạy bắt buộc đạt `loss_vs_sent == 0` và `parity_status` bắt đầu bằng `"OK:"` (Zero-Fork Invariant).
