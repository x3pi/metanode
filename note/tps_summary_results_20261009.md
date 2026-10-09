# Báo Cáo Tóm Tắt Kết Quả Đo Lường TPS & Khả Năng Thực Thi (2026-10-09)

**Kính gửi:** Ban Lãnh Đạo  
**Dự án:** Metanode Core Blockchain  
**Tiêu chuẩn dữ liệu:** Đo lường vi mô trực tiếp (Micro-measurements), 114 tệp log/JSON thô độc lập, Zero-Fork 100%.

---

## 1. Hiệu Năng Riêng Tầng Thực Thi (Execution Engine)

Tầng thực thi của Metanode có tốc độ vượt trội và **hoàn toàn không phải nút thắt cổ chai**:

| Cấp độ đo lường | Thông lượng (TPS) | Thời gian xử lý block 8.000 txs | Ghi chú kỹ thuật |
| :--- | :---: | :---: | :--- |
| **Parallel EVM (Block-STM)** | **~129.345 tx/s** | **61,85 ms** | Thực thi song song C++ FFI trên 24 luồng |
| **Go Pipeline (EVM + RAM State Trie)** | **~74.067 tx/s** | **108,01 ms** | Toàn bộ pipeline Go trên RAM (EVM + Merkle Roots) |
| **Go Full Pipeline (kèm tiền xử lý)** | **~26.000 – 27.000 tx/s** | ~300 ms | Bao gồm decode, unpack, kiểm tra signature ban đầu |
| **PebbleDB Disk Commit** | *Không nghẽn* | Gối đầu không chặn | Ghi đĩa chạy song song (pipelining) với block sau |

---

## 2. Bảng Tổng Hợp Kết Quả Thực Nghiệm Consensus (BFT vs Raft)

| Kịch bản kiểm thử | Số lượt ($n$) | TPS Trung vị | TPS Đỉnh | Độ trễ P50 | Trạng thái kỹ thuật |
| :--- | :---: | :---: | :---: | :---: | :--- |
| **Raft Burst 25k** | 8 | **13.398 tx/s** | **14.152 tx/s** | 1,28 s | Hàng đợi thông suốt (`statusFull = 0`, `splitBatch = 0`) |
| **Raft Burst 20k / 30k** | 20 | **~13.200 tx/s** | **13.654 tx/s** | 1,06 – 1,48 s | Ổn định đồng đều ở mọi mức tải burst |
| **Raft Sustained (Duy trì 60s)** | 8 | **3.932 tx/s** | **4.164 tx/s** | 5,90 s | Giới hạn bởi chu kỳ tạo block cố định 43,94 ms/block |
| **BFT Sustained (Duy trì 60s)** | 8 | **13.863 tx/s** | **15.363 tx/s** | 43,48 s* | Gom block lớn (5k–8k tx/block) liên tục |
| **BFT Burst 25k (Warm-up)** | 4 | **9.887 tx/s** | **10.324 tx/s** | 1,88 s | Đã làm ấm DAG round & cache (+27,5% so với cold-start) |
| **BFT Burst 25k (Cold-start)** | 4 | **7.751 tx/s** | **8.500 tx/s** | 2,54 s | Khởi động nguội sau khi xóa sạch DB |

*\*Ghi chú: Độ trễ P50 của BFT Sustained kéo dài do client bơm tải cực đại 200k tx/s dồn vào mempool chờ đóng block.*

---

## 3. Bản Chất 3 Vấn Đề Cốt Lõi

1. **Vì sao Raft Sustained thấp hơn BFT Sustained (~3,9k vs ~14k)?**
   * Raft có chi phí tạo block cố định **43,94 ms/block** (`raft.Apply` sao chép mạng & fsync chiếm **79,1%**). Khi bơm batch rời rạc 1.000 txs, trần tần suất bị chặn ở mức ~22,7 block/s $\rightarrow$ thông lượng tối đa ~4.000 – 10.000 tx/s. BFT gom được block lớn (5.000 – 8.000 txs) nên đạt ~14.000 tx/s.
2. **Vì sao BFT Burst 25k lúc đạt 7,8k, lúc đạt 10,6k?**
   * Do **hiệu ứng làm ấm (Warm-up)**: Khi vừa khởi động cụm từ đầu (Cold-start), DAG cần tích lũy các vòng đầu và cache trống $\rightarrow$ đạt ~7,8k tx/s. Khi được làm ấm với tải mồi trước đó, BFT vọt lên **~10.300 tx/s** (khớp với số đo độc lập).
3. **An toàn hệ thống:**
   * Đạt **100% Zero-Fork parity** trên toàn bộ 114 lượt thử nghiệm.

---

## 4. Hướng Hành Động Tiếp Theo

1. **Tối ưu Raft (Batch Coalescing):** Tự động gộp các batch đang chờ trong hàng đợi thành 1 proposal Raft duy nhất $\rightarrow$ Giảm 79,1% chi phí `Apply` dư thừa, **tiềm năng tăng Throughput +39,5%**.
2. **Tối ưu BFT (Cache Warm-up):** Khởi tạo trước DAG state và làm ấm cache bộ nhớ để BFT burst luôn đạt đỉnh $\ge 10.000\text{ tx/s}$.
