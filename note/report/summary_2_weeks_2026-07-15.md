# Tóm tắt công việc 2 tuần (2026-07-02 → 2026-07-15)

63 commit trên `dev`, nhiều người tham gia. Tóm tắt theo nhóm việc, không liệt kê từng commit.

## 1. Xapian search — giảm RAM, sửa race condition (PR #27, khối lượng lớn nhất)
- Chuyển từ physical docid sang virtual ID, buffer theo transaction, `std::map`→`std::unordered_map`.
- Sửa hàng loạt lock: `shared_lock` cho đọc song song, `recursive_mutex`/`unique_lock` cho ghi, khoá TOCTOU khi huỷ `XapianManager`/xoá database.
- Thêm dedicated read pool, bỏ commit-disk thủ công không cần thiết, retry khi gặp `DatabaseModifiedError`.
- Fail-stop: abort tiến trình khi Xapian lỗi thay vì tiếp tục chạy sai state (tránh fork).
- Kết quả: merge PR #27 sau 4 vòng review (`8cd8135b`), có báo cáo review riêng từng vòng trong `note/report/pr_27_review_*.md`.

## 2. TrueBlockSTM / execution engine
- Sửa lỗi barrier transaction và leader reward tính sai khi chạy song song → ép sequential ở chỗ cần thiết.
- Thêm unbounded task queue + telemetry đo thời gian thực thi.
- Task scheduling context-aware để tránh deadlock/miss trong STM.
- Sửa integer overflow và bounds-check khi decode ABI.

## 3. Hạ tầng build/CI (Go)
- Sửa `go build`/`go vet`/`go test` chạy sạch trên checkout mới (trước đó fail).
- Thêm workflow CI build+test cho module `execution`.
- Dọn code chết (`AddRelatedAddress`/`UpdateRelatedAddresses` không có tác dụng).

## 4. Điều tra & cải thiện TPS end-to-end (phiên gần nhất, 07-15)
Chi tiết đầy đủ ở `note/report/tps_e2e_analysis_2026-07-14.md` (16 mục, 4 vòng điều tra). Tóm tắt các fix đã merge:
- Giảm kích thước block/proposal để giảm độ trễ commit.
- Sửa lỗi TX bị gossip trùng lặp giữa các validator (mỗi TX từng bị propose bởi tới 5 node).
- Loại node synconly ra khỏi committee đồng thuận (trước đó làm nhịp round bị ghim ở mức chậm nhất).
- Sửa oversubscription CPU: cap thread pool Go/Rust theo `GOMAXPROCS` thay vì `NumCPU()` (cluster test chạy 5 node dồn trên 1 máy).
- Tìm và sửa 2 chỗ ghi file đồng bộ (blocking) trong context async gây đứng hình 5-7 giây ngay sau mỗi đợt burst TX — nguyên nhân gốc khiến TPS end-to-end thấp bất thường.
- Tối ưu việc ghi registry key của NOMT (trước đó ghi lại toàn bộ file mỗi block, giờ chỉ ghi phần mới).
- Bật cơ chế metrics nội bộ có sẵn của NOMT (trước đó chưa từng dùng) để xác nhận NOMT không phải nút thắt — số liệu thật cho thấy 0% cache miss, hashtable dư thừa.
- Kết quả đo được: TPS đỉnh từ ~5.6k lên ~10k tx/s, độ trễ đứng hình đầu round từ 5-7s xuống 0s.

## 5. Vận hành / triển khai
- Thêm hỗ trợ snapshot (reflink) trong deploy, cấu hình `nomt_commit_concurrency`/cache theo tài nguyên máy thật.
- Bật debug symbol, core dump, `GOTRACEBACK` để dễ điều tra sự cố production.
- Cập nhật ansible deploy (timeout, đảm bảo thư mục tồn tại, đồng bộ genesis khi đổi loại node).

## Việc còn mở
- Nút thắt TPS còn lại (sau khi hết các bug trên) nằm ở registry map + 2 cache TTL 30 phút phía Go (`ethHashMapBlsHashMap`, `txHashToBlockNumberMap`) — gây áp lực GC khi test dồn hàng trăm nghìn TX trong vài phút. **Chủ động không xử lý tiếp ở Go** vì định hướng sắp tới hợp nhất toàn bộ execution logic sang Rust.
- `core_thread.rs` còn code debug (`std::fs::write` ra `/tmp`) chưa dọn, không ảnh hưởng production nhưng nên dọn khi tiện.
