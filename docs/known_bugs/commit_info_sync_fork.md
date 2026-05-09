# Lỗi Fork do Thiếu Đồng bộ CommitInfo (Reputation Scores)

## Mô tả Lỗi (Bug Description)
Khi một node khởi động lại hoặc tham gia mạng và cần bắt kịp trạng thái (catch-up) thông qua quá trình `STARTUP-SYNC`, nó sẽ sử dụng RPC `FetchCommits` để tải các commit bị thiếu từ các peer khác.
Tuy nhiên, cấu trúc `FetchCommitsResponse` ban đầu chỉ trả về danh sách các `Commit` và các block xác nhận (`certifier_blocks`). Nó **bỏ sót `CommitInfo`**, nơi chứa dữ liệu quan trọng là `reputation_scores` (điểm uy tín) tại các ranh giới thay đổi lịch trình leader (Leader Schedule boundary).

Do điểm uy tín này được dùng để tính toán `LeaderSchedule` cho epoch hoặc đợt tiếp theo, các node mới đồng bộ sẽ không có thông tin điểm uy tín chính thức (authoritative scores) mà mạng lưới đã đồng thuận. Dẫn đến việc các node này có thể tự tính toán ra một `LeaderSchedule` khác biệt so với các node luôn online (những node đã chạy toàn bộ luồng chấm điểm consensus bình thường). Sự sai lệch về `LeaderSchedule` này gây ra lỗi Fork (phân nhánh DAG) ngay sau khi node bắt kịp trạng thái và chuyển sang tham gia consensus.

## Phân tích Nguyên nhân Cốt lõi (Root Cause)
1. Trong Metanode, quá trình tính điểm leader được thực hiện ở tầng consensus. Khi một commit thay đổi lịch trình xảy ra, `reputation_scores` sẽ được tính toán và lưu trong `CommitInfo` thay vì bản thân đối tượng `Commit`.
2. Khi node bị tụt hậu (lag) và chạy `commit_syncer`, nó kéo các commit về nhưng không kéo được `CommitInfo`.
3. Hàm `reset_to_network_baseline` trong `DagState` bị gọi với `reputation_scores = None`. 
4. Node tự tính lại lịch trình mà thiếu điểm số, dẫn đến lịch trình leader cục bộ (local) bị sai lệch (divergence) so với toàn mạng (quorum).

## Giải pháp (Fix)
Chúng ta đã khắc phục vấn đề này bằng cách đưa metadata của consensus trở thành dữ liệu chuẩn cần được đồng bộ qua RPC:
1. **Mở rộng RPC (`FetchCommits`)**: Cập nhật Protobuf (`FetchCommitsResponse`) và logic mạng (`tonic_network.rs`, `NetworkClient`) để trả về thêm một tuple element thứ ba: `commit_infos`.
2. **Cập nhật Storage Layer**: Thêm phương thức `read_commit_info` vào trait `BlockStoreAPI` (cho cả `RocksDBStore` và `MemStore`) để có thể truy xuất chính xác `CommitInfo` theo index và digest từ cơ sở dữ liệu.
3. **Truyền tải Dữ liệu (Handler)**: Cập nhật `AuthorityService::handle_fetch_commits` để tự động đính kèm `CommitInfo` tương ứng với mỗi `Commit` được gửi cho peer đang catch-up.
4. **Sửa lỗi trên Node Nhận (Syncer)**: Tại `commit_syncer.rs`, trích xuất `reputation_scores` từ byte array `commit_infos` nhận được, sau đó truyền vào hàm `reset_to_network_baseline`. Điều này đảm bảo node khởi động lại sẽ sử dụng chính xác điểm số do mạng lưới cung cấp để xây dựng `LeaderSchedule` mà không bị sai lệch.

## Kết quả
Node khi khởi động và đồng bộ qua `STARTUP-SYNC` hiện tại đã có khả năng khôi phục chính xác `LeaderSchedule` y hệt như các node đang online, qua đó chấm dứt hoàn toàn tình trạng chia nhánh (fork) sau khi phục hồi snapshot và tái hòa nhập consensus.
