# Node thực thi (rollup) — mục lục bàn giao cho dev

> Cập nhật: 2026-09-25. Nhánh `dev`. **Chưa có dòng code nào của tính năng này**; chỉ có tài liệu (và việc gỡ `RecoveryCommittee`, đã commit `0a30dfc1`).

## 1. Đọc theo thứ tự

| # | Tài liệu | Vai trò |
|---|---|---|
| 0 | `NEXT_STEPS_PLAN.md` | **Việc tiếp theo (bàn giao):** hiện trạng đã kiểm chứng, quyết định chờ chủ dự án, danh sách việc N1–N9 có tiêu chí nghiệm thu, danh sách kiểm tra trước PR |
| 1 | `SEQUENCER_STEP_BY_STEP_PLAN.md` | **Kế hoạch triển khai chính** (quyết định 0.1, phát hiện đã xác minh 0.5, kiến trúc 0.6, các giai đoạn A–F, điểm móc H1–H8, bảng chưa xác minh mục 3, quyết định mục 4) |
| 2 | `SEQUENCER_SCHEMAS_AND_TEST_PLAN.md` | Schema, cấu hình, ABI, 11 bất biến, danh mục test |
| 3 | `SEQUENCER_ERC20_STANDARD_TX_PROOF.md` | **Đặc tả cuối cùng** của bằng chứng gian lận ERC20 (giai đoạn F); mục 14 là kết quả F0, mục 15 là quyết định đã chốt |
| 4 | `SEQUENCER_DESIGN.md`, `SEQUENCER_DIAGRAMS_AND_OPEN_ISSUES.md` | Bối cảnh và sơ đồ |

**Đã bị thay thế — chỉ đọc để hiểu lịch sử, KHÔNG làm theo:** `SEQUENCER_IMPLEMENTATION_PLAN.md` (mục 2, 4, 6), `SEQUENCER_PARENT_ANCHORING.md` (đặc biệt mục về bằng chứng giao dịch/receipt: `receiptRoot`/`transactionsRoot` **không** phải cây Merkle, xem STANDARD_TX_PROOF mục 14), `SEQUENCER_ERC20_DISPUTE.md`, `SEQUENCER_FRAUD_PROOF_DESIGN.md` (phần sổ sự kiện theo log), `SEQUENCER_ERC20_USER_HISTORY_PROOF.md` (trừ mục 0 và 13 về phạm vi/riêng tư). Khi mâu thuẫn: STANDARD_TX_PROOF > STEP_BY_STEP_PLAN > các tài liệu còn lại.

## 2. Đã chốt (không hỏi lại)

- Chế độ mới `consensus_mode = "raft"` của `simple_chain`; không thêm binary, không thêm RPC; chỉ bỏ Rust consensus; xử lý block Go giữ nguyên.
- Raft = **`hashicorp/raft`**; bầu leader tự động; timeout chỉ dùng cho Raft (ngoại lệ có duyệt của AGENTS.md nguyên tắc 2, **không** dùng timeout để quyết định dispatch).
- **Một khoá ký chung** cho mọi replica; uỷ ban = 1 khoá trên Parent Chain; HA khoá người dùng (`cmd/rpc`) để sau.
- `RecoveryCommittee` đã gỡ hẳn; unregister tự ký có nonce; `DeadChains` chỉ qua `SlashOnEquivocation`.
- Chứng minh ERC20: chế độ giao dịch chuẩn có chữ ký (A0–A4); thêm `txset_root` (lá `(tx_hash,status)`) vào `AnchorLeaf`; **không** đổi backend trie.
- Điểm móc mặc định-tắt trong code cũ: H1–H6 (Raft), **H7** (`privacy_mode`), **H8** (RPC ký phản hồi) — đã duyệt.

## 3. Mức sẵn sàng theo giai đoạn (nói thẳng)

| Giai đoạn | Sẵn sàng giao dev? | Điều kiện / chặn |
|---|---|---|
| **B1** (máy trạng thái thuần), **A0** (so với `PerChainAllocation`/Gateway) | ✅ Có | Không phụ thuộc gì chưa xác minh |
| **C0** (spike xác định: cùng chuỗi `ExecutableBlock` → cùng state root trên 2 tiến trình, không Rust) | ◐ **Cổng của kế hoạch chính (P1–P2) đã đạt qua PR #130; tiêu chí thoát C0 theo SCHEMAS 2.5 (`T-DET-01..07`) chưa đủ** | Còn thiếu: lặp ≥ 20 lần (T-DET-01), tải song song lớn (02), đổi `GOMAXPROCS` (03), đảo thứ tự tx (05); T-DET-04 chuyển sang C2. Xem `NEXT_STEPS_PLAN.md` **N0**. **Cổng cho C1–C6 vẫn là ☑** |
| **C1–C6** (Raft mode) | ⏳ Sau C0 | Chốt `is_authoritative_gei`, số replica, kênh chuyển tiếp ở C0/C2 |
| **Kiểm chứng gỡ `RecoveryCommittee`** | ✅ Xong (2026-09-25) | `./ci.sh run-now --reset` trên cụm thử cục bộ (.232, 4 validator + 1 sync-only): wipe + deploy lại, TPS Blast ~7623 tx/s PASS, Chaos Rolling Restart 6 vòng PASS (62m33s), Zero-Fork khớp block hash + state root trên cả 5 node, dữ liệu Xapian/EVM của 19 contract đồng nhất. Chưa kiểm: đường `unregisterChainWithCert` trên cụm thật (chỉ có unit test) |
| **F0 còn lại** | ✅ Có (S) | Xác nhận `transactionsRoot` tích luỹ; giao dịch chuẩn có R,S,V hay chỉ BLS (quyết định Parent kiểm chữ ký thế nào); chạy thử revert token thật |
| **F1–F6** (bằng chứng ERC20) | ❌ Chưa | Chờ F0; chờ **A3** (chính sách token trong phạm vi + cảnh báo ví dùng router — quyết định vận hành); tham số mục 10 chưa đo; chi phí kiểm chữ ký trong Gateway chưa benchmark |

## 4. Còn mở (chủ sở hữu → cần ai quyết)

1. Chính sách A3 (danh sách token thuộc phạm vi) — **vận hành/sản phẩm**.
2. Số replica N (mặc định 3), HTTP nội bộ cho kênh follower→leader, timeout Reclaim — chốt bằng số đo ở C2/B8.
3. Có bỏ phụ thuộc build vào `libmetanode` (`validation_transaction.go`) — mặc định không, xét sau C1.
4. Dọn các tài liệu đã bị thay thế thành bản lưu trữ khi giai đoạn F bắt đầu.

## 5. Quy tắc bắt buộc khi code (AGENTS.md)

Zero-Fork (thà pending còn hơn fork); không timeout để dispatch; mọi queue/worker có giới hạn bộ đệm; không I/O chặn trong vòng lặp async; `build_check.sh` sạch (Go + Rust + FFI) sau mỗi thay đổi; cập nhật `PROJECT_STRUCTURE.md` khi thêm package/entrypoint/kênh liên tầng; hook vào code cũ chỉ dạng `if raftfeed.Enabled()` mặc định tắt.
