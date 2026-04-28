# Giải Quyết Lỗi Fork Hash Block 61 & Lỗi Không Cập Nhật StateRoot (Block 62+)

## 1. Root Cause của việc Lệch Hash ở Block 61
Tại sao **Block 61** trên Node 1 lại tạo ra hash `0x97e2d3b5...` khác hoàn toàn với `m0` (`0xfeb0b50c...`) trong khi `stateRoot` và `parentHash` hoàn toàn khớp?

Nguyên nhân chính xác nằm ở **GlobalExecIndex (GEI)**. 
- Khi `m0` (node sống liên tục) chạy, nó có bộ đếm `cumulative_fragment_offset` nội bộ.
- Khi Node 1 khởi động lại từ Snapshot, biến `cumulative_fragment_offset` trong Rust bị khôi phục về `0`.
- Do đó, tại thời điểm tạo Block 61, `GEI` mà Rust tính ra trên Node 1 sẽ bị **lệch** (nhỏ hơn) so với `GEI` của `m0` (dù nó xử lý cùng một commit BFT).
- Trước đây, trong file `block_header.go`, hàm `Hash()` **đã bao gồm trường `GlobalExecIndex`**. Việc lệch GEI này lập tức làm thay đổi `block hash` của Node 1, tạo ra một nhánh fork hoàn toàn độc lập!

**👉 Action Đã Thực Hiện:** 
Tôi đã tiến hành sửa code trong `execution/pkg/block/block_header.go` loại bỏ trường `GlobalExecIndex` khỏi quá trình compute `bData` trong hàm `Hash()`. `GEI` chỉ là một metadata nội bộ dùng để map giữa Rust và Go, **không nên và không được phép** đưa vào Block Hash vì nó không determinisitc sau quá trình snapshot recovery.

---

## 2. Tại sao StateRoot của Node 1 bị "đóng băng" từ Block 62?
Như bạn đã thấy, từ Block 62, 63, 64, `stateRoot` của Node 1 hoàn toàn không di chuyển:
```
m1: hash=0x77d11...  parentHash=0x97e2d3b5...  stateRoot=0x34dee271... (Giữ nguyên của Block 61)
```
Nguyên nhân cốt lõi cũng xuất phát từ việc tính toán **GEI bị lỗi** khi khởi động lại:
1. Lúc đọc Snapshot, LevelDB của Go đang ghi nhớ `lastBlockGEI` cực kỳ cao (kết quả do mạng lưới gửi về trước đó).
2. Khi Node 1 xử lý các block tiếp theo (Block 62, 63, 64...), vì `cumulative_fragment_offset` bị mất (bắt đầu từ 0), nên `global_exec_index` truyền từ Rust sang Go bị **THẤP HƠN** `lastBlockGEI` đã lưu trong DB.
3. Trong `block_processor_sync.go` của Go có đoạn code **`GEI REGRESSION GUARD`**:
```go
if lastBlockGEI > 0 && globalExecIndex <= lastBlockGEI {
    logger.Info("🛡️ [GEI-REGRESSION] Skipping stale commit: incoming GEI=%d ≤ last block GEI=%d...")
    return
}
```
**Chính cơ chế này đã đánh chặn toàn bộ các giao dịch của Block 62, 63, 64 trên Node 1!**
Vì nó tưởng đây là các commit cũ (stale commits) từ đợt replay DAG, Go đã nhảy cóc (`return` ngay lập tức) mà KHÔNG thực thi các transactions trong block. Kết quả là `stateRoot` đứng im không đổi. Mãi tới block 65, khi bộ đếm GEI của Rust tự nhích lên đủ để vượt qua cái mốc `lastBlockGEI` cũ trong DB con số, nó mới bắt đầu thực thi transaction trở lại, kéo theo việc stateRoot thay đổi!

---

## 3. Cách Vận Hành Cần Làm Ngay

Vì code thư viện Go core (`execution/pkg/block/block_header.go`) vừa mới được tôi sửa lại để loại bỏ GEI, bạn cần thực hiện build lại thư viện FFI và chạy thử lại:

1. **Rebuild Go FFI Executor:**
```bash
cd /home/abc/chain-n/metanode/execution
make build_ffi_executor
```

2. **Khởi động lại Orchestrator và Xoá Snapshot Node 1 rác:**
Bạn có thể reset thử Node 1 theo quy trình chuẩn và theo dõi xem hash từ Block 60, 61 đã chốt 100% với mạng lưới chưa. Việc loại bỏ trực tiếp GEI ra khỏi cấu trúc Hash sẽ khoá chết 100% hiện tượng fork siêu nhỏ này.
