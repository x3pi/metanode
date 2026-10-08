# 🚀 Báo Cáo Đo Đỉnh Trần Năng Lực Cụm 4-Validator BFT (Mysticeti DAG)

**Ngày thực hiện:** 2026-10-08  
**Tác giả:** Metanode Core Developer Agent  
**Môi trường:** Linux x86_64, 104 cores CPU, 256GB RAM, NOMT State Trie Backend, Sharded Storage Engine  
**Binary thử nghiệm:** `/tmp/p06_bins/simple_chain` (đã tích hợp Phase 2b `PrepareTransactions` song song hóa)  
**Cấu hình mạng:** Cụm 4 Validator DAG BFT (`val0`, `val1`, `val2`, `val3` - Quorum 3/4)  
**Bộ dữ liệu:** 50.000 ví độc lập pre-funded từ [`generated_keys.json`](file:///home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json), ký EIP-1559 Dynamic Fee  

---

## 1. Bối Cảnh & Mục Tiêu

Trong báo cáo ngày hôm qua (2026-10-07 tại [`note/perf_secp_tps_20261007.md`](file:///home/abc/chain-n/metanode/note/perf_secp_tps_20261007.md)), cụm 4 Validator BFT đã ghi nhận Throughput bão hòa ở mức **6.231,62 tx/s** (đỉnh 6.505,82 tx/s) khi bơm 25.000 giao dịch.

Hôm nay (2026-10-08), chúng ta đã triển khai thành công bản vá tối ưu hóa **Giai đoạn 2b: Song song hóa `PrepareTransactions`** (giảm thời gian xác thực chữ ký của block 8.000 txs từ 486 ms xuống **93 ms**, tiết kiệm **~394 ms/block**).

Bài kiểm tra này đo lại chính xác trần công suất tối đa của cụm 4 Validator BFT trên các nấc tải bão hòa (10k, 20k, 25k, 30k txs) để đánh giá mức độ bứt phá thực tế.

---

## 2. Bảng Kết Quả Khảo Sát Đỉnh Thông Lượng BFT

| Nấc tải | Số lượng Txs | Tốc độ Bơm Ingest | Thời Gian Commit On-Chain | **Effective On-Chain TPS** | Số Block Sinh Ra | Txs Bình Quân / Block | Latency P50 | Latency P99 | Tải CPU Đỉnh | Bộ Nhớ Đỉnh (RSS) | Zero-Fork Parity (Cả 4 Node) |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| **Nấc 1** | 10.000 txs | 207.059 tx/s | **2,259 giây** | **4.426,67 tx/s** | 2 blocks | 5.000 tx/block | 2.058,3 ms | 2.307,4 ms | 492,6% | 4.011 MB | **100% PASS** |
| **Nấc 2** | 20.000 txs | 211.823 tx/s | **2,436 giây** | **8.209,23 tx/s** | 3 blocks | 6.666 tx/block | 2.131,0 ms | 2.530,7 ms | 752,5% | 7.453 MB | **100% PASS** |
| **Nấc 3** | **25.000 txs** | 252.826 tx/s | **2,628 giây** | **9.512,77 tx/s** 🏆 | 3 blocks | **8.333 tx/block** | **2.026,0 ms** | 2.727,0 ms | 904,3% | 11.779 MB | **100% PASS** |
| **Nấc 4** | 30.000 txs | 200.378 tx/s | **3,490 giây** | **8.595,97 tx/s** | 5 blocks | 6.000 tx/block | 2.625,7 ms | 3.639,8 ms | 1061,7% | 15.362 MB | **100% PASS** |

---

## 3. So Sánh Đối Đầu Tại Mốc 25.000 Giao Dịch: Trước vs Sau Tối Ưu Phase 2b

| Chỉ số đo lường | Trước tối ưu Phase 2b (Hôm qua 07/10) | Sau tối ưu Phase 2b (Hôm nay 08/10) | Mức Cải Thiện Kỹ Thuật |
|---|:---:|:---:|:---:|
| **Thông lượng đỉnh (Effective TPS)** | 6.231,62 tx/s (đỉnh 6.505,82) | **9.512,77 tx/s** | **TĂNG +3.281,15 tx/s (+52,6%)** 🚀 |
| **Thời gian chốt sổ 25k txs (`commit_duration`)** | **4,016 giây** | **2,628 giây** | **Rút ngắn 1,388 giây (-34,6% thời gian)** ⚡ |
| **Độ trễ cam kết trung bình (Receipt P50)** | **3.025 ms** | **2.026 ms** | **Giảm 999 ms (-33,0% latency)** |
| **Độ trễ cam kết đuôi (Receipt P99)** | 4.132 ms | **2.727 ms** | **Giảm 1.405 ms (-34,0%)** |
| **Số blocks sản sinh** | 3 – 4 blocks (~7k - 11k tx/block) | 3 blocks (~8.333 tx/block) | Đóng block tối ưu dung lượng |
| **Tính bất biến Zero-Fork** | 100% PASS | **100% PASS** | Cả 4 validator khớp tuyệt đối State Root |

---

## 4. Phân Tích Kỹ Thuật: Vì sao BFT bứt phá từ 6.2k lên 9.5k TPS?

1. **Giải phóng nút thắt lớn nhất của Block:**
   Trong bài test 25.000 txs, mỗi block chứa bình quân **8.333 giao dịch**.
   - Trước đây (07/10): `PrepareTransactions` chạy đơn luồng tuần tự mất tới **~486 ms/block**. Cả 3 block tốn mất **~1,45 giây** chỉ để giải nén protobuf và kiểm tra ràng buộc phong bì chữ ký (`ValidateEnvelopeBinding`).
   - Hôm nay (08/10): Nhờ worker pool 16 cores, thời gian `PrepareTransactions` cho mỗi block 8.333 txs giảm xuống chỉ còn **~93 ms/block**. Cả 3 block tiết kiệm được **~1,18 giây** thời gian chạy trên critical path!
2. **Rút ngắn thời gian toàn trình:**
   Nhờ tiết kiệm gần 1,2 giây ở khâu chuẩn bị block, toàn bộ 25.000 txs hoàn tất quy trình đồng thuận DAG BFT, gom chữ ký BLS, thực thi Block-STM và commit đĩa NOMT trong vẻn vẹn **2,628 giây** (so với 4,016 giây của hôm qua).
3. **Độ đầy của khối (Block Amortization) phát huy tối đa:**
   Khi block đạt ~8.333 tx/block, chi phí đồng thuận cố định của BFT (~300 - 400 ms) được chia đều cho lượng txs khổng lồ, đưa thông lượng thực tế chạm mốc **9.512,77 TPS**!

---

## 5. Tổng Kết Toàn Diện Hai Chế Độ Đồng Thuận Của Metanode

Sau hai đợt đo đạc bão hòa ngày 08/10/2026:

```
[BFT Consensus] 4 Validator DAG Mysticeti:   ██████████████████ 9.513 TPS
[Raft Consensus] 3-Node Rollup Sequencer:    ██████████████████████████ 13.811 TPS
```

| Tiêu chí | Chế độ BFT (4 Validator DAG Mysticeti) | Chế độ Raft (3-Node Rollup Sequencer) |
|---|:---:|:---:|
| **Mô hình an ninh** | Byzantine Fault Tolerant (kháng 33% node ác ý) | Crash Fault Tolerant (kháng 50% node sập) |
| **Đỉnh thông lượng thực tế** | **9.512,77 tx/s** | **13.811,16 tx/s** |
| **Thời gian commit 25k–30k txs** | **2,62 giây** (25k txs) | **2,33 giây** (30k txs) |
| **Receipt Latency P50** | **2.026 ms** | **916 – 1.517 ms** |
| **Zero-Fork Parity** | **100% Khớp Bit-for-bit** | **100% Khớp Bit-for-bit** |
| **Vai trò kiến trúc** | **Parent Chain (Bảo mật & Quyết toán)** | **Execution Layer (Xử lý ứng dụng tốc độ cao)** |

---

## 6. Danh Mục Bằng Chứng (Evidence Files)

- Script tái hiện: [`execution/scripts/test/evidence/run_bft_peak_search.py`](file:///home/abc/chain-n/metanode/execution/scripts/test/evidence/run_bft_peak_search.py)
- Tổng hợp kết quả đo đỉnh BFT: [`note/evidence/tps_prepare_tx_opt_20261008/bft_peak_search_summary.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/bft_peak_search_summary.json)
- Báo cáo chi tiết từng nấc:
  - Nấc 10k: [`bft_peak_Stage_10k.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/bft_peak_Stage_10k.json)
  - Nấc 20k: [`bft_peak_Stage_20k.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/bft_peak_Stage_20k.json)
  - Nấc 25k: [`bft_peak_Stage_25k.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/bft_peak_Stage_25k.json)
  - Nấc 30k: [`bft_peak_Stage_30k.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/bft_peak_Stage_30k.json)
- Checksum bảo toàn toàn vẹn: [`note/evidence/tps_prepare_tx_opt_20261008/MANIFEST.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/MANIFEST.json)
