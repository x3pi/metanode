# Phân tích Thiết kế Ban đầu của Epoch Monitor (Trước khi Refactor)

Tài liệu này mô tả chi tiết nguyên lý hoạt động của đoạn mã cũ trong `epoch_monitor.rs` trước khi được điều chỉnh theo thiết kế chuẩn phân tách Data-driven và Time-driven. 

Mã nguồn ban đầu của `start_unified_epoch_monitor` được thiết kế có phần "ôm đồm" (tham công tiếc việc), khi nó vừa đảm nhận vai trò theo dõi sự thay đổi Kỷ nguyên (Epoch), lại vừa kiêm luôn việc đồng bộ dữ liệu (Block Sync).

---

## 1. Đối với Validator Node (Time-driven)

Với Validator, thiết kế ban đầu tương đối phù hợp với bản chất Time-driven (Chủ động theo thời gian mạng lưới):

- **Lấy thông tin mạng:** Quá trình bắt đầu bằng việc Node hỏi các Peers thông qua giao thức P2P RPC (`query_peer_epochs_network`) để biết mạng lưới hiện tại đang ở Epoch nào (`network_epoch`).
- **Phát hiện chậm tiến độ:** So sánh `network_epoch` với epoch nội tại của Rust (`rust_epoch`). 
- **Ép chuyển đổi (Forced Transition):** Nếu `network_epoch > rust_epoch`, Validator biết rằng nó đã bị rớt lại phía sau. Nó sẽ vào vòng lặp (multi-epoch catch-up) chạy từ `rust_epoch + 1` đến `network_epoch`.
- **Fetch Boundary & Transition:** Trong vòng lặp này, Validator gọi RPC `query_peer_epoch_boundary_data` để lấy các thông số ranh giới (boundary_block, boundary_gei, timestamp). Sau đó, nó gọi `try_start_epoch_transition` (dẫn tới `transition_to_epoch_from_system_tx`) để **ép** Consensus dừng lại, cắt sổ Go Master, dọn dẹp và cập nhật sang Kỷ nguyên mới.

---

## 2. Đối với SyncOnly Node (Luồng xử lý cũ)

Đây là nơi thiết kế cũ gặp vấn đề nghiêm trọng khi nó **cố gắng áp đặt mô hình Time-driven của Validator lên một Node vốn dĩ phải chạy theo Data-driven**. 

Ở phiên bản cũ, `epoch_monitor` chứa một khối lệnh `if` khổng lồ dành riêng cho SyncOnly và fetch block:
`if matches!(current_mode, NodeMode::Validator) && !peer_rpc.is_empty()` (Đã xóa) và các nhánh xử lý fetch block đi kèm.

Những việc đoạn code cũ đã làm đối với SyncOnly:

### A. Cạnh tranh tải Block (Race Condition)
Thay vì để yên cho tiến trình chuyên biệt `sync_loop.rs` làm nhiệm vụ tải block, `epoch_monitor` cũ lại cố gắng tự tải block.
- Nó phát hiện `local_go_epoch` bị thụt lùi so với `network_epoch`.
- Nó gọi các hàm như `fetch_executable_blocks_from_peer` để xin tải block mới từ Peer về.
- **Hệ lụy:** Xảy ra xung đột tài nguyên (Race Condition) cực kỳ gắt gao. `sync_loop` và `epoch_monitor` cùng lúc cố gắng đẩy block tải được qua duy nhất một cổng UDS (Unix Domain Socket) vào Go Master. Việc tranh giành này thường xuyên dẫn tới lỗi đứt gãy kết nối (Broken Pipe), khiến tiến trình đồng bộ của SyncOnly bị treo vĩnh viễn.

### B. Bơm "Empty Commits" và Cắt đứt ranh giới GEI
Vì `epoch_monitor` nóng vội muốn cập nhật trạng thái Kỷ nguyên cho kịp với Mạng (nhưng lại chưa tải kịp block thật chứa giao dịch):
- Nó thấy Validator khác có `boundary_gei` cao hơn.
- Để "lấp liếm" khoảng cách GEI này, tiến trình sinh ra các `empty commits` (những block trống không có giao dịch) và nhồi vào Go Master.
- **Hệ lụy:** Đứt gãy giao dịch hoàn toàn. Ví dụ: Ranh giới thật là block 200, nhưng Node mới tải tới block 190. Thay vì đợi tải xong 191-200, code cũ nhồi empty commits bù vào vị trí từ 191-200. Khi chốt sổ, các giao dịch thực sự nằm ở 191-200 bị DROP hoàn toàn. Điều này sinh ra một Global Execution Index (GEI) ảo, làm Hash của state tree bị lệch vĩnh viễn so với mạng lưới.

### C. Ngăn cản sự thăng cấp (Promotion Blocked)
Đoạn code cũ có luồng rẽ nhánh riêng, bỏ qua hàm `transition_to_epoch_from_system_tx` đối với SyncOnly, hoặc gọi nó trong trạng thái GEI bị hỏng. Hậu quả là SyncOnly không bao giờ quét lại cách hợp lệ danh sách Committee mới. Ngay cả khi Mạng lưới đã bầu nó làm Validator, nó vẫn không nhận thức được để kích hoạt `setup_validator_consensus`.

---

## Tổng kết Lỗi Kiến Trúc (Architectural Flaw) của Code Cũ

Sự sai lầm cốt lõi của thiết kế ban đầu nằm ở việc **trộn lẫn trách nhiệm (Separation of Concerns)**:
1. Cho phép `epoch_monitor` làm công việc của `sync_loop`.
2. Bắt một Passive Node (SyncOnly) hành xử theo quy luật thời gian (Time-bound) thay vì quy luật Dữ liệu (Data-bound).

Việc gỡ bỏ các biến kiểm tra cũ và áp dụng triết lý `target_upper_bound = local_go_epoch` đối với SyncOnly đã trả lại đúng bản chất của mô hình State Machine: SyncOnly chỉ được phép sang Kỷ nguyên mới khi và chỉ khi dữ liệu P2P (Block) của nó đã chạy đến điểm đó.
