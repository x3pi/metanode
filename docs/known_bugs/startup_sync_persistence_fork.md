# Lỗi Phân Nhánh (Fork) Sau Khi Phục Hồi Snapshot: Mất Đồng Bộ `lastBlockHashKey`

## Triệu Chứng (Symptoms)
Khi một node bị dừng và phục hồi lại từ một snapshot (thông qua quá trình `STARTUP-SYNC`), đôi khi node đó sẽ rẽ nhánh (fork) và tạo ra một mã băm khối (block hash) không chính xác (ví dụ: `0x2d14...` thay vì `0x5df2...`). 

Lỗi này thường xuất hiện nếu node bị khởi động lại (restart) hoặc gặp sự cố (crash) ngay sau khi vừa bắt kịp (catch up) dữ liệu đồng bộ với cụm mạng, nhưng lại xảy ra trước khi node kịp tham gia vào quá trình đồng thuận (consensus) bình thường để tạo ra khối mới.

## Nguyên Nhân (Root Cause)
Lỗi này bắt nguồn từ sự chậm trễ trong việc ghi dữ liệu xuống đĩa (persistence lag) của quá trình `STARTUP-SYNC`:

1. **Lưu Trữ Bất Đồng Bộ**: Trong quá trình đồng bộ lạnh (cold-sync), hàm `HandleSyncBlocksRequest` ở tầng Go Execution nhận các khối bị thiếu và lưu chúng vào CSDL thông qua lệnh `SaveBlockByHash()`.
2. **Mất Khóa Trạng Thái Cuối**: Lệnh `SaveBlockByHash()` lưu dữ liệu khối thành công nhưng **không** ép buộc cập nhật khóa `lastBlockHashKey` trên ổ đĩa một cách đồng bộ (synchronous flush). Thay vào đó, thao tác cập nhật này nằm lơ lửng ở bộ nhớ đệm (memtable) của PebbleDB.
3. **Mất Dữ Liệu Tạm Thời**: Nếu node bị tắt đột ngột (hoặc background cache-eviction diễn ra) trước khi có một khối đồng thuận mới (vốn sẽ gọi lệnh `SaveLastBlock()` chuẩn), khóa `lastBlockHashKey` chưa kịp ghi xuống đĩa sẽ bị bay hơi (evaporated).
4. **Hệ Quả Phân Nhánh**: Khi khởi động lại, dù thư mục dữ liệu đã có đủ block từ snapshot, hàm `GetLastBlock()` sẽ thất bại vì không tìm thấy `lastBlockHashKey`. Hệ thống lùi lại sử dụng logic khởi tạo dự phòng, tải lại `metadata.json`, và trong quá trình chạy tự động vá lỗi (patch) các root của NOMT Trie, dẫn tới việc tính toán lại header và tạo ra một block hash sai lệch (`0x2d14...`), gây rẽ nhánh toàn mạng.

## Cách Khắc Phục (Resolution)

Để giải quyết triệt để lỗi này, 2 biện pháp phòng vệ (guardrails) đã được triển khai vào mã nguồn:

### 1. Hàng Rào Lưu Trữ Đồng Bộ (Synchronous Execution Barrier)
* **Vị trí sửa:** `execution/executor/unix_socket_handler_epoch.go`
* **Chi tiết:** Bổ sung lệnh `SaveLastBlockSync(lastBlk)` ngay tại điểm kết thúc của vòng lặp đồng bộ `STARTUP-SYNC`. Lệnh này ép buộc PebbleDB phải đẩy (flush) toàn bộ dữ liệu từ bộ nhớ đệm thành các file SST xuống ổ cứng và cập nhật `lastBlockHashKey` một cách nguyên tử (atomically). 
Nhờ đó, ngay cả khi node crash ngay lập tức sau khi đồng bộ, trạng thái khởi động lại vẫn được bảo toàn nguyên vẹn.

### 2. Công Cụ Chẩn Đoán Bắt Buộc (`[FORK-DIAG]` Instrumentation)
* **Vị trí sửa:** `execution/cmd/simple_chain/app_blockchain.go`
* **Chi tiết:** Bổ sung log giám sát chuyên biệt để in ra và so sánh gốc trạng thái của NOMT (`nomtAccountRoot`, `nomtStakeRoot`) với gốc được lưu trong Block Header vào *mọi lần khởi động* (every startup). 
Điều này cung cấp khả năng quan sát (observability) minh bạch để nhận diện ngay lập tức nếu trạng thái của cây Trie trở nên cũ (stale) trước khi bất kỳ thao tác thay đổi/vá lỗi header nào được kích hoạt.
