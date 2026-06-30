# Kế Hoạch Triển Khai Chi Tiết: Kiến Trúc TEE (Hai Tầng Finality)

> **Loại tài liệu:** Kế hoạch triển khai (Implementation Plan)
> **Tài liệu tham chiếu:** Xem chi tiết kiến trúc đề xuất tại [Đề Xuất Kiến Trúc TEE](tee_architecture.md)
> **Mục tiêu:** Chia nhỏ các cột mốc lý thuyết thành các hành động kỹ thuật thực tế (Actionable Tasks) cho đội ngũ phát triển thực thi trên phần cứng yếu (Orange Pi, 16MB RAM).

---

## Bảng Công Việc (Checklist) Tổng Quan

- [ ] **Giai Đoạn 1: PoC Nền Tảng Lõi (Đánh Giá Phần Cứng)**
  - [ ] Bước 1: Biên dịch EVM (revm) cho TrustZone
  - [ ] Bước 2: Đo lường Benchmark phần cứng (Baseline)
- [ ] **Giai Đoạn 2: Xây Dựng Cơ Chế Chống Gian Lận Cốt Lõi**
  - [ ] Bước 3: Tích hợp Trusted Storage (RPMB) Chống Replay
  - [ ] Bước 4: Triển khai Tier 1 (Structured Query + Sorted MMR)
- [ ] **Giai Đoạn 3: Tích hợp Xapian & Fraud Proof (Tier 2)**
  - [ ] Bước 5: Cài đặt Xapian & Keyword-MMR
  - [ ] Bước 6: Luồng PENDING & Challenge Window trong TEE
- [ ] **Giai Đoạn 4: Cross-Shard & Network (Mở Rộng Bầy Đàn)**
  - [ ] Bước 7: Cơ chế Cross-Shard Ticket
  - [ ] Bước 8: Chữ Ký Ngưỡng & Testnet Swarm

---

## Lộ Trình Triển Khai Chi Tiết

### Giai Đoạn 1: PoC Nền Tảng Lõi (Đánh Giá Phần Cứng)

Giai đoạn này tập trung hoàn toàn vào việc trả lời câu hỏi cốt lõi: Orange Pi (16MB Secure RAM) có thể chạy được EVM không?

#### Bước 1: Biên dịch EVM (revm) cho TrustZone
- **Cấu hình môi trường:** Thiết lập toolchain Rust cross-compile (aarch64) và OP-TEE/Apache Teaclave framework.
- **Tối ưu Dependencies:** Chuyển đổi mã nguồn `revm` (Rust EVM) sang chế độ `#![no_std]`. Loại bỏ hoàn toàn thư viện chuẩn `std`, thay thế bằng `alloc`.
- **Đóng gói Trusted Application (TA):** Đưa `revm` no_std vào một TA của OP-TEE.
- **Mục tiêu nghiệm thu:** Build thành công file `.ta` và khởi chạy một smart contract rỗng. Giám sát bộ nhớ Heap + Stack, đảm bảo tổng RAM tiêu thụ **chắc chắn dưới 16MB**.

#### Bước 2: Đo lường Benchmark phần cứng (Baseline)
- **Đo Latency World-Switch:** Viết ứng dụng Host (Normal World) gọi SMC liên tục (ping-pong) vào TA. Tính toán độ trễ trung bình của 1 lần gọi (dự kiến tính bằng mili-giây).
- **Đo Tốc độ Crypto:** Chạy benchmark bên trong TEE cho các thuật toán: SHA-256, Keccak-256, và verify chữ ký ECDSA/BLS.
- **Mục tiêu nghiệm thu:** Ra được một bảng report thực tế các con số giới hạn vật lý của CPU Cortex-A53/A55 trên Orange Pi. Từ đó làm mốc chuẩn cho Epoch Batching sau này.

---

### Giai Đoạn 2: Xây Dựng Cơ Chế Chống Gian Lận Cốt Lõi

Sau khi chứng minh TEE chạy được EVM, ta bổ sung các cơ chế bảo mật xung quanh.

#### Bước 3: Tích hợp Trusted Storage (RPMB) Chống Replay
- **Cấu trúc lưu trữ:** Thiết kế dữ liệu trạng thái thu gọn gồm `(state_root: [u8; 32], monotonic_counter: u64)`.
- **Gọi API phần cứng:** Sử dụng API OP-TEE để đọc/ghi xuống phân vùng eMMC RPMB của Orange Pi.
- **Logic Anti-Replay:** Viết code chặn ở cổng vào SMC: TEE chỉ chấp nhận `state_root` do Host truyền vào nếu `monotonic_counter` lớn hơn giá trị đang lưu trong RPMB.
- **Mục tiêu nghiệm thu:** Viết script đóng giả Host gửi `state_root` cũ. TEE phải văng lỗi và chặn đứng giao dịch giả lập này (Replay Attack protection).

#### Bước 4: Triển khai Tier 1 (Structured Query + Sorted MMR)
- **Implement Sorted MMR:** Tự code hoặc dùng thư viện Rust Sorted Merkle Mountain Range tương thích `no_std`.
- **Sinh Proof ở Host:** Viết tool (LevelDB wrapper) để sinh **Range Proof** và **Non-membership Proof** (cho các tag).
- **Verify Proof ở TEE:** Cài đặt logic xác thực Proof gọn nhẹ bên trong TEE (tránh deserialize toàn cây). Nếu hợp lệ, gọi `revm` chạy contract.
- **Mục tiêu nghiệm thu:** Chạy thành công 1 giao dịch chuyển tiền cho các ví có "TAG_VIP" (K <= 5 tags) hoàn toàn trustless dựa trên Proof toán học. Số dư thành FINAL ngay lập tức.

---

### Giai Đoạn 3: Tích hợp Xapian & Fraud Proof (Tier 2)

Đây là phần phức tạp nhất, đưa full-text search vào đường đi tiền.

#### Bước 5: Cài đặt Xapian & Keyword-MMR
- **Môi trường Host:** Cài đặt Xapian/LevelDB trên Host Linux.
- **Pre-committed Tokenization:** Viết logic khi Host lưu 1 document, nó phải tự động cắt từ (tokenize), băm từng từ khóa, và đẩy vào cấu trúc Keyword-MMR.
- **Truy vấn Optimistic:** Host gọi Xapian, lấy kết quả truyền qua SMC vào TEE.

#### Bước 6: Luồng PENDING & Challenge Window trong TEE
- **Xử lý Pending:** `revm` đổi số dư thành công, nhưng thay vì ghi đè lên State Root hiện tại, TEE đưa kết quả vào hàng đợi `pending_root` trong RAM/RPMB, trả về chữ ký tạm. Host khóa cứng số dư này ở Frontend/DB.
- **Xử lý Fraud Proof:** Viết hàm nhận Challenge. Nếu Node khác phát hiện Host giấu tài liệu, nó gửi Keyword Hash + MMR Proof vào SMC.
- **Logic Slashing:** TEE đối chiếu Proof, nếu khớp (nghĩa là Host cố tình giấu tài liệu chứa từ khóa), TEE xóa sổ `pending_root`, trừ tiền cọc (bond) của Host và cộng cho người challenge. Nếu hết Challenge Window không ai khiếu nại, promote `pending_root` -> `finalized_root`.
- **Mục tiêu nghiệm thu:** Unit/Integration Test giả lập một kịch bản gian lận và kịch bản minh bạch. Quan sát số dư Pending bị Rollback (nếu gian lận) hoặc Final (nếu minh bạch).

---

### Giai Đoạn 4: Cross-Shard & Network (Mở Rộng Bầy Đàn)

Giai đoạn cuối để đưa Node đơn lẻ vào môi trường mạng phân mảnh thực tế.

#### Bước 7: Cơ chế Cross-Shard Ticket
- **Định nghĩa Ticket:** Tạo struct `CrossShardTicket { tx_id, from_shard, to_shard, asset, amount, signature }`.
- **Ký & Xác thực:** TEE Shard nguồn trừ tiền và ký vào Ticket (dùng private key TEE). TEE Shard đích nhận Ticket bất đồng bộ, verify chữ ký và cộng tiền.
- **Mục tiêu nghiệm thu:** Xác minh giao dịch xuyên shard thành công mà không gây deadlock như mô hình 2PC (Two-Phase Commit).

#### Bước 8: Chữ Ký Ngưỡng & Testnet Swarm
- **Threshold Crypto:** Triển khai thuật toán BLS Threshold Signature (VD: 667/1000) vào luồng ký finality. Cấu hình VRF để tự động xoay tua ủy ban giám sát (Committee).
- **Benchmark Mạng Cụm:** Kết nối 20-50 thiết bị Orange Pi thật để test mạng lưới bầy đàn, đo TPS mạng khi tải Xapian cao.
- **Mục tiêu nghiệm thu:** Đo lường TPS của mạng, báo cáo độ suy hao băng thông khi có giao dịch xuyên shard. Hoàn thiện Codebase tiến tới Audit.
