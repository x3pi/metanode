# Kết quả Stress Test: 10,000 BLS Registration

## Tóm tắt kết quả

| Chỉ số | Giá trị |
|:---|:---|
| Tổng giao dịch gửi | 10,000 |
| Injection TPS (max) | **~208,000 tx/s** (`sleep=0`) |
| Injection TPS (stable) | **~883 tx/s** (`sleep=1`) |
| Execution TPS | **~940 tx/s** |
| Consensus | **~19.5 blocks/s** |
| Thành công | **10,000 / 10,000 (100%)** |
| Max TX/block | ~510 |

## 💯 100% Guaranteed Success (Auto-Retry Feature)

- **Nguyên nhân drop ban đầu**: Khi đẩy 200,000+ tx/s qua TCP, tầng execution của node sẽ drop 4-5% giao dịch khi thực hiện epoch transitions do đầy buffer nội bộ không thể tiêu thụ kịp.
- **Giải pháp cũ**: Chỉnh TCP delay thủ công (`--sleep 1`). 
- **Giải pháp NHỊP ĐỘ CAO mới an toàn 100%**: `tps_blast` hiện tích hợp tính năng **Auto-Retry Loop**. Script sẽ bắn 20k+ tx/s, chờ 60s, kiểm tra on-chain thông qua RPC và *tự động gửi lại* chính xác những transaction bị node drop, tự động lặp lại logic này cho đến khi 10,000/10,000 account đích thực được confirm. Kết quả đảm bảo 100% thành công không cần can thiệp.

## Hướng dẫn chi tiết

Xem [`BLS_SPAM_TPS_GUIDE.md`](./BLS_SPAM_TPS_GUIDE.md)

## Chạy nhanh

```bash
cd cmd/tool/tps_blast
go build -o tps_blast . && ./tps_blast --count 10000
```

---
*Ngày test: 2026-02-21 — Cluster: 5 nodes mtn-consensus — 100% success rate*

## ⏱️ Phương pháp Đo lường TPS (Trong Load Test run_multinode_load.sh)

Thông số **SYSTEM TPS** (ví dụ trong kết quả báo `~10000 tx/s`) và **Thời gian xử lý** được script đo đạc chặt chẽ để phản ánh chính xác hiệu suất thực tế của **Consensus Engine (Backend)**, tách biệt hoàn toàn với độ trễ từ phía máy bơm TX (Client Injection).

### 1. Cách Đo Thời Gian Xử Lý (`Thời gian xử lý / PROC_SEC`)

Thời gian xử lý (Processing Time) được tính bằng khoảng thời gian từ lúc **Engine nhận mẻ giao dịch đầu tiên** cho đến khi **Block chứa các giao dịch cuối cùng được tạo thành công**. 

Cụ thể, script sử dụng độ ưu tiên sau:
* **Chính xác nhất (Mặc định)**: Tính bằng `[Thời điểm xuất bản block cuối cùng chứa TX] - [Thời điểm Go Sub Node kích hoạt đẩy lô TX đầu tiên cho Rust Engine]`. Thời điểm bắt đầu này được Backend chủ động ghi lại vào file logs (`/tmp/backend_start_ms.log`) dưới mức độ chính xác milli-giây.
* **Dự phòng 1**: Lấy `[Thời điểm xuất bản block cuối cùng chứa TX] - [Thời điểm Client phát tín hiệu START bắn TX]`.
* **Dự phòng 2**: Lấy `[Thời điểm chain rảnh hoàn toàn đợi 10s] - [Thời điểm Client START] - 10 giây`.

Giữ mốc thời gian lấy từ **Backend Trigger** giúp kết quả TPS ở đây không bị bóp méo hay giảm xuống bởi thời gian Client tốn kém để ký (sign), đóng gói và gửi hàng chục nghìn packets qua mạng/TCP.

### 2. Cách Tính TPS Hệ Thống (`SYSTEM TPS`)

Sau khi có lưới thời gian chính xác, thông lượng được tính theo công thức:

> **SYSTEM TPS** = Tổng số TX đã nằm trong Blocks / Thời gian xử lý

Ghi chú:
* **Tổng số TX**: Được script trích xuất trực tiếp từ dòng log xác nhận block (`createBlockFromResults`) trên node Master.
* TPS thể hiện ở đây là **True Throughput** đi qua toàn bộ chặng đường cốt lõi của node: *Tập hợp lô (Sub Go) -> Đẩy vào State Layer (Rust FFI) -> Xử lý máy ảo song song (Execution) -> Đồng thuận BFT (Consensus) -> Chốt Block (Commit)*.
