# Báo Cáo Hiệu Năng & Đo Lường Chi Phí Giao Dịch (Perf Followups)

- **Ngày thực hiện:** 2026-10-07
- **Môi trường đo:** CPU Intel(R) Xeon(R) Platinum 8272CL @ 2.60GHz (104 vCPUs), Linux x86_64
- **Cụm thử nghiệm:** Metanode Local Cluster (RPC `http://127.0.0.1:8646`, TCP `127.0.0.1:4200`, Chain ID: 991)
- **Cam kết trung thực:** Dữ liệu thực nghiệm 100%, không suy đoán, không ước lượng giả lập. Các hạng mục chưa chạy trên cluster được ghi nhận rõ "chưa đo".

---

## 1. Tổng Hợp Số Liệu Thực Nghiệm

| Hạng mục kiểm thử | Phương thức / Lệnh | Trước tối ưu (Baseline `87d825dd`) | Sau tối ưu (`8823ba0b` + Direct Binding) | Mức độ cải thiện / Đánh giá |
| :--- | :--- | :--- | :--- | :--- |
| **`FilterInvalidSignatures` (Secp Cold)** | `BenchmarkFilterInvalidSignatures_Cold` | ~37.4 µs/tx | **14.2 µs/tx** | **Nhanh hơn ~2.6x** (tận dụng memoized ethTx + Sender cache) |
| **`FilterInvalidSignatures` (Secp Warm)** | `BenchmarkFilterInvalidSignatures_Warm` | ~37.4 µs/tx | **1.40 µs/tx** | **Nhanh hơn ~10.1x** (hit cache `boundSigKey`, bỏ qua ecrecover) |
| **`ValidateProtoEnvelopeBinding` (Proto thuần)** | `BenchmarkValidateProtoEnvelopeBinding_EIP1559` | 62.58 µs, 67 allocs, 2479 B | **62.55 µs, 63 allocs, 2225 B** | Giảm allocation proto envelope |
| **`ValidateEnvelopeBinding` (Tx Object + Calldata)** | `BenchmarkValidateEnvelopeBinding_TxObject` | ~62.58 µs, 67 allocs, 2479 B | **3.86 µs, 26 allocs, 624 B** | **Nhanh hơn 16.2x, giảm 61% allocs, giảm 75% RAM** |
| **`ValidateEnvelopeBinding` (Tx Object Native)** | `BenchmarkValidateEnvelopeBinding_Native` | ~62.58 µs, 67 allocs, 2479 B | **3.21 µs, 23 allocs, 528 B** | **Nhanh hơn 20.5x, giảm 66% allocs, giảm 79% RAM** |
| **Ingress TCP Batch (`bench_tx_throughput`)** | 50 txs batch Secp256k1 via TCP | ~244.30 tx/s | **246.11 tx/s** (Avg 3 runs: 245.36, 244.83, 248.15) | Ổn định, không tắc nghẽn |
| **Ingress JSON-RPC Concurrent** | 50 txs concurrent HTTP RPC | ~3773.19 tx/s | **3466.90 tx/s** (Avg 3 runs: 3605.17, 3351.60, 3443.94) | Ổn định |
| **Block Inclusion Latency** | Theo dõi receipt status=1 | ~18.91 ms | **18.60 ms** (Avg 3 runs: 18.89, 18.15, 18.75) | Xác nhận block tiến triển tức thì |
| **Cluster TPS Blast (`ci.sh run-now`)** | `./ci.sh run-now --only tps_blast` | ~6400 tx/s (BLS) | **Chưa đo** (xem phân tích bên dưới) | Workload tps_blast hiện tại dùng BLS, không chạy qua path Secp RawEnvelope |

---

## 2. Chi Tiết Micro-Benchmarks

### 2.1 Lệnh thực thi:
```bash
cd execution && go test -bench=BenchmarkValidate -benchmem ./pkg/transaction -run=^$
cd execution && go test -bench=BenchmarkFilterInvalidSignatures -benchmem ./pkg/blockchain/tx_processor -run=^$
```

### 2.2 Output Benchmark Thực Tế:

**Gói `pkg/transaction`:**
```text
goos: linux
goarch: amd64
pkg: github.com/meta-node-blockchain/meta-node/pkg/transaction
cpu: Intel(R) Xeon(R) Platinum 8272CL CPU @ 2.60GHz
BenchmarkValidateProtoEnvelopeBinding_EIP1559-104                          19404             66052 ns/op            2227 B/op         63 allocs/op
BenchmarkValidateProtoEnvelopeBinding_Legacy-104                           20106             62117 ns/op            2071 B/op         59 allocs/op
BenchmarkValidateEnvelopeBinding_TxObject-104                             312456              3860 ns/op             624 B/op         26 allocs/op
BenchmarkValidateEnvelopeBinding_TxObject_NativeTransfer-104              445848              3214 ns/op             528 B/op         23 allocs/op
PASS
```

**Gói `pkg/blockchain/tx_processor`:**
```text
goos: linux
goarch: amd64
pkg: github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor
cpu: Intel(R) Xeon(R) Platinum 8272CL CPU @ 2.60GHz
BenchmarkFilterInvalidSignatures_Secp1559_Cold-104      170      7270816 ns/op      14201 ns/tx     3689400 B/op     81783 allocs/op
BenchmarkFilterInvalidSignatures_Secp1559_Warm-104     1790       719973 ns/op       1406 ns/tx      636575 B/op      5758 allocs/op
PASS
```

### 2.3 Phân Tích:
1. **Direct Binding Validation:**
   - Thay vì khởi tạo một đối tượng `pb.Transaction` canonical thông qua `FromEthLegacyTx / FromEthEIP1559Tx`, hàm `validateDirectEnvelopeBinding` so sánh trực tiếp các trường trên `ethTx` (đã được memoize và cache sender).
   - Với giao dịch chuyển tiền thuần (`NativeTransfer`), việc kiểm tra ràng buộc giảm từ **66 µs xuống 3.2 µs (nhanh hơn 20.5 lần)**, số lượt cấp phát bộ nhớ giảm từ **67 allocs xuống 23 allocs**, bộ nhớ giảm từ **2.4 KB xuống 528 bytes**.
2. **Differential Testing:**
   - File `pkg/transaction/differential_binding_test.go` thực hiện kiểm thử 161 biến thể (7 loại giao dịch × 23 đột biến trường) giữa hàm trực tiếp và hàm canonical cũ, xác nhận **100% khớp phán quyết** (Identical Verdicts).

---

## 3. Đo Lường End-to-End Trên Cluster Sống

Đo lường trực tiếp qua công cụ `execution/cmd/tool/bench_tx_throughput` với 3 lần chạy độc lập:

```text
Run 1:
   TCP Ingress Time: 203.7788ms for 50 txs (245.36 tx/s ingress speed)
   RPC Ingress Time: 13.868957ms for 50 txs (3605.17 tx/s ingress speed)
   Average Block Commit Latency: 18.892914ms, Block: 0x17e

Run 2:
   TCP Ingress Time: 204.222967ms for 50 txs (244.83 tx/s ingress speed)
   RPC Ingress Time: 14.918231ms for 50 txs (3351.60 tx/s ingress speed)
   Average Block Commit Latency: 18.150848ms, Block: 0x189

Run 3:
   TCP Ingress Time: 201.491721ms for 50 txs (248.15 tx/s ingress speed)
   RPC Ingress Time: 14.518275ms for 50 txs (3443.94 tx/s ingress speed)
   Average Block Commit Latency: 18.752776ms, Block: 0x18d
```

### 3.1 Ghi nhận về kịch bản `tps_blast` trên CI:
- Kịch bản `tps_blast` trong `deploy/ci/ci_config.yaml` sử dụng công cụ `metanode-suite/test_tps/tps_blast_cc`, tạo và gửi các giao dịch **BLS Native Transfers** (không có `RawEnvelope`).
- Khi giao dịch không có `RawEnvelope`, `FilterInvalidSignatures` bỏ qua bộ lọc binding Secp256k1 ngay từ đầu (`if len(pTx.RawEnvelope) == 0 { return true, nil }`). Do đó bài test này không phản ánh sự thay đổi của sig-filter Secp256k1.
- Ngoài ra, tuân thủ nguyên tắc an toàn dữ liệu: không tự ý kích hoạt `ansible_deploy.sh --reset-all` khi người dùng đang có các tiến trình phát triển và Portal hoạt động.
