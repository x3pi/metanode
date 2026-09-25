# 📋 Báo Cáo Nghiệm Thu C0 Spike — Determinism & State Mutation Verification

**Ngày thực hiện:** 2026-09-25 07:19:09 UTC
**Môi trường:** Linux x86_64, NOMT state trie backend, Raft consensus mode (Hook H1/H3/H5)
**Cấu hình:** 2 OS processes độc lập, separate data dirs (`/tmp/c0_node_process1`, `/tmp/c0_node_process2`)

---

## 1. Tóm Tắt Kết Quả (Executive Summary)

| Hạng mục kiểm tra | Kết quả | Ghi chú |
|---|:---:|---|
| **Determinism (Proc 1 vs Proc 2)** | **PASS (100%)** | Toàn bộ Block Hash, State Root, Receipts Root, Txs Root giống nhau tuyệt đối |
| **State Mutation Verification** | **PASS (100%)** | 100% receipts `Status=1`, Sender Nonce tăng tuần tự, Recipient Balance tăng chính xác |
| **Block-STM Conflict Handling** | **PASS (100%)** | Xử lý triệt để đồng thời Read-Write (cùng sender, consecutive nonces) và Write-Write (3 txs cùng recipient) |
| **Restart Bypass & N+1 Continuation** | **PASS (100%)** | Bỏ qua an toàn blocks 1..5, thực thi và commit thành công Block #6 (N+1) |

---

## 2. Số Liệu Hiệu Năng (Execution Metrics)

- **Process 1 (5 blocks, 30 txs):** 5.84890628s (~1.169781256s/block)
- **Process 2 (5 blocks, 30 txs):** 5.951896344s (~1.190379268s/block)
- **Restart Bypass & Block #6 Continuation:** 1.347280694s

---

## 3. Bảng Đối Chiếu Determinism & State Mutation (Proc 1 vs Proc 2)

| Block | Txs | Block Hash | State Root | Receipts Root | Sender Nonce | Recip Balance (wei) | All Receipts OK |
|---|:---:|---|---|---|:---:|---:|:---:|
| #1 | 6 | `0x7e02c7053912f3...` | `0x64e5906d074387...` | `0x0675c5a44649b5...` | 3 | 6000000000000000 | ✅ true |
| #2 | 6 | `0x87e01b79172ddc...` | `0x39030e67f34787...` | `0x200f7a89654abc...` | 5 | 12000000000000000 | ✅ true |
| #3 | 6 | `0xfde97c23de6b0f...` | `0x640a1b6b8bd2ca...` | `0xbd276acdd0a71e...` | 7 | 18000000000000000 | ✅ true |
| #4 | 6 | `0x46fb0414797dd8...` | `0x380f411400c9cd...` | `0xaf2c4f995f9627...` | 9 | 24000000000000000 | ✅ true |
| #5 | 6 | `0xeb8a33cd8a935b...` | `0x24b16f924653cf...` | `0x2b065533df7452...` | 11 | 30000000000000000 | ✅ true |

---

## 4. Kiểm Tra Workload Conflicts (Block-STM Concurrency)

Workload mỗi block gồm 6 giao dịch được thiết kế đặc thù gây xung đột:
1. **Read-Write Conflict (Sequential Nonce):** Tx #0 và Tx #1 cùng Sender `0x294f...846` với nonce liên tiếp ($N$ và $N+1$). Block-STM phát hiện và sắp thứ tự phụ thuộc chính xác.
2. **Write-Write Conflict (Shared Hot Recipient):** Tx #0, Tx #1, và Tx #2 từ 2 senders khác nhau cùng chuyển tiền vào Recipient `0x1111...1111`. Cả hai process đều hội tụ về cùng một số dư cuối cùng không sai lệch 1 wei.
3. **Parallel Disjoint Writes:** Tx #3, #4, #5 gửi đến các recipients độc lập, kiểm tra song song hóa an toàn.

---

## 5. Kiểm Tra Restart Bypass & Block #6 ($N+1$ Continuation)

- **Blocks 1..5:** Tái khởi động node 1 từ disk; hệ thống nhận diện `lastHeight >= targetHeight`, bypass hoàn toàn việc thực thi lại mà không làm biến đổi bất kỳ hash hay root nào.
- **Block #6 ($N+1$ Continuation):** Submit Block #6 sau khi bypass; node thực thi qua Block-STM và commit thành công:
  - **Block #6 Hash:** `0x17b76f9aece85d39265b75170d4e33d071d553a50f0344b27bd5e791cbc2c4d9`
  - **Block #6 StateRoot:** `0x75e947dec6e090a9db95056dd66122924f0e4cf8b94736d15910d5eb68041c19`
  - **Block #6 Receipts:** 100% `Status == 1` (true)
  - **Sender Nonce sau block #6:** `13` (tiến triển từ `11`)
  - **Recipient Balance sau block #6:** `36000000000000000` wei

---

## 6. Kết Luận & Cổng Nghiệm Thu Đợt 1 (Status: ◐ In-Progress)

Spike C0 đã đạt các tiêu chí cơ bản của bước kiểm chứng xác định:
- [x] Khởi tạo `blockIngestionQueue` đồng bộ trong constructor `NewBlockProcessor`.
- [x] Đóng `stopChan` qua `sync.Once` trong `StopWait()` an toàn không panic.
- [x] 100% determinism giữa 2 process độc lập có state mutation thật (receipts, nonce, balance).
- [x] Block-STM xử lý chính xác cả RW lẫn WW conflicts.
- [x] Restart bypass đối chiếu identity (GEI & tx count) và tiếp tục tiến triển sang block $N+1$.

### 🛑 Các cổng nghiệm thu bắt buộc trước khi chuyển C0/C1 sang ☑:
1. Workload có EVM contract và giao dịch tương tác Gateway/barrier chạy nhiều vòng liên tục (multi-round).
2. Restart thử nghiệm bằng `kill -9` đột ngột (thay vì shutdown tuần tự) và đối chiếu identity toàn vẹn.
3. Bằng chứng không khởi động Rust runtime (`InitFFIBridge` không được gọi) qua log và strace.
4. Rà soát danh sách H1–H6 cuối cùng và kiểm tra `go test -race` toàn diện.

**Trạng thái:** `◐ ĐẠT ĐỢT 1 / ĐANG CHỜ CỔNG P1–P2 CHO NGHIỆM THU TOÀN DIỆN`
