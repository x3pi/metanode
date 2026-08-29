# Mô hình Triển khai & Yêu cầu Phần cứng MetaNode

Dựa trên yêu cầu của hệ thống gồm:
1. **Root Anchor**: Chạy nhiều Validator (tối thiểu 4 Validator để đảm bảo BFT).
2. **Private Chain 1**: Chạy 1 Validator.
3. **Private Chain 2**: Chạy nhiều Validator (tối thiểu 4 Validator).
4. **Cross-Chain Relayer**: 1 tiến trình Relayer Daemon.

Tổng cộng hệ thống cần chạy **9 tiến trình Validator** và **1 Relayer**.
Dưới đây là các phương án triển khai từ 1 máy chủ (All-in-one) cho đến phân tán, kèm theo yêu cầu phần cứng tối thiểu cho môi trường **Testnet/Staging**.

> [!NOTE] 
> **Tiêu chuẩn tài nguyên cho 1 Validator (Testnet)**: 
> CPU: 2-4 Cores | RAM: 4-8 GB | SSD: 50-100GB (NVMe). 
> Khi chạy gộp nhiều tiến trình trên cùng 1 máy (Docker Compose), tài nguyên có thể được dùng chung, nhưng RAM cần phải cộng dồn để tránh OOM (Out Of Memory).

---

## 1. Mô hình 1 Máy chủ (All-in-One)
Phù hợp nhất để **thử nghiệm (PoC), phát triển cục bộ** hoặc khi ngân sách phần cứng cực kỳ hạn chế. Toàn bộ mạng lưới được giả lập trên một máy duy nhất.

- **Cấu trúc**: Tất cả 9 Validators + Relayer chạy qua Docker Compose trên cùng 1 máy.
- **Yêu cầu phần cứng tối thiểu**:
  - **CPU**: 8 - 16 Cores 
  - **RAM**: 32 GB (Khuyến nghị 64 GB)
  - **Disk**: 500GB SSD
- **Ưu điểm**: Dễ dàng deploy bằng 1 file `docker-compose.yml`, không có độ trễ mạng (latency) giữa các node, dễ debug.
- **Nhược điểm (Rủi ro)**: Không có khả năng chịu lỗi (SPOF - Single Point of Failure). Máy chủ sập là toàn bộ hệ thống ngừng hoạt động. Không mô phỏng được môi trường mạng thực tế.

---

## 2. Mô hình 2 Máy chủ
Chia tải hệ thống ra làm 2 máy chủ để giảm áp lực phần cứng.

- **Cấu trúc phân bổ (Gợi ý)**:
  - **Máy 1 (Root Anchor + Relayer)**: Chạy 4 Validators của Root Anchor + tiến trình Relayer.
  - **Máy 2 (Private Chains)**: Chạy 1 Validator của Chain 1 + 4 Validators của Chain 2.
- **Yêu cầu phần cứng (Mỗi máy)**:
  - **CPU**: 8 Cores
  - **RAM**: 16 GB - 24 GB
  - **Disk**: 250GB SSD
- **Ưu điểm**: Giảm tải cho 1 máy, bắt đầu có giao tiếp mạng qua IP nội bộ/LAN.
- **Nhược điểm**: Vẫn chịu rủi ro tập trung. Nếu Máy 1 sập, Root Anchor ngưng trệ. Nếu Máy 2 sập, cả 2 Private Chains đều dừng. Chức năng đồng thuận BFT chưa được phát huy thực sự.

---

## 3. Mô hình 3 Máy chủ
Tách biệt rõ ràng từng chuỗi (Chain) ra các máy chủ vật lý/VM riêng biệt.

- **Cấu trúc phân bổ**:
  - **Máy 1 (Root Anchor)**: 4 Validators Root Anchor.
  - **Máy 2 (Private Chain 2 + Relayer)**: 4 Validators của Private Chain 2 + tiến trình Relayer.
  - **Máy 3 (Private Chain 1)**: 1 Validator của Private Chain 1.
- **Yêu cầu phần cứng**:
  - **Máy 1 & Máy 2**: CPU 8 Cores | RAM 16GB | SSD 200GB (Mỗi máy chạy 4-5 tiến trình).
  - **Máy 3**: CPU 4 Cores | RAM 8GB | SSD 100GB (Chỉ chạy 1 tiến trình).
- **Ưu điểm**: Độc lập tài nguyên giữa các Chain. Chain này tải cao không ảnh hưởng đến Chain kia.
- **Nhược điểm**: Bản thân mỗi Chain (ví dụ Root Anchor trên Máy 1) vẫn đang chạy tập trung trên 1 máy chủ vật lý, chưa đạt được tính phân tán (Decentralization) ở cấp độ đồng thuận.

---

## 4. Mô hình Phân tán Thực sự (Tối thiểu 4 Máy chủ)
> [!IMPORTANT]
> Đây là mô hình **BẮT BUỘC** nếu bạn muốn kiểm thử cơ chế đồng thuận BFT (Byzantine Fault Tolerance) và tính chịu lỗi của mạng trong thực tế. 
> Hệ thống chịu được tối đa $f = 1$ máy chủ bị lỗi mạng, sập nguồn hoặc bị tấn công mà mạng vẫn tiếp tục sinh block ($n \ge 3f + 1 = 4$ node).

Thay vì gom cụm theo Chain, chúng ta sẽ **phân bổ các Node của cùng một Chain rải đều ra các máy chủ khác nhau**.

- **Cấu trúc phân bổ (Khuyên dùng cho Staging/Production)**:
  - **Máy Chủ 1**: Root Val 1 + Priv2 Val 1 + Priv1 Val 1 + Relayer.
  - **Máy Chủ 2**: Root Val 2 + Priv2 Val 2.
  - **Máy Chủ 3**: Root Val 3 + Priv2 Val 3.
  - **Máy Chủ 4**: Root Val 4 + Priv2 Val 4.
- **Yêu cầu phần cứng (Mỗi máy)**:
  - **Máy 1**: CPU 8 Cores | RAM 16 GB | SSD 200 GB.
  - **Máy 2, 3, 4**: CPU 4-6 Cores | RAM 8-12 GB | SSD 100 GB.
  - **Mạng**: Latency thấp (< 50ms) giữa 4 máy chủ, kết nối LAN hoặc VPC.
- **Ưu điểm vượt trội**: 
  - Đảm bảo **sự an toàn thực sự của BFT**. Nếu bất kỳ 1 trong 4 máy chủ bị sập, Root Anchor và Private Chain 2 vẫn còn 3 Validators hoạt động $\Rightarrow$ Đủ túc số (Quorum 2f+1) để tiếp tục tạo block bình thường.
  - Sát với môi trường Production nhất.

---

## Tóm tắt Khuyến nghị

1. **Nếu bạn đang test dev cục bộ (chỉ cần code chạy được)**: Chọn **Mô hình 1 Máy chủ** (All-in-one). Dùng tmux hoặc docker-compose để bật tất cả lên.
2. **Nếu bạn muốn test hiệu năng, cross-chain thực tế hoặc show demo staging**: Bắt buộc chọn **Mô hình 4 (Phân tán 4 máy chủ)**. Nó chứng minh được tính kháng lỗi (Fault Tolerance) của Metanode mà các mô hình 1, 2, 3 máy chủ không thể hiện được.
