# Công Thức Chịu Lỗi BFT & Số Node Tối Thiểu Cần Sống

Tài liệu này giải thích công thức tính khả năng chịu lỗi (fault tolerance) của cụm validator trong Metanode, dựa trên chuẩn Byzantine Fault Tolerance (BFT) `n ≥ 3f + 1`, và cho biết với mỗi số lượng validator cụ thể thì cần tối thiểu bao nhiêu node còn sống để cụm tiếp tục hoạt động (liveness).

Phát hiện này xuất phát từ 1 sự cố thực tế: cụm test 3 node bị dừng cứng hoàn toàn khi 1 node chết đột ngột (không rõ nguyên nhân), dù 2 node còn lại vẫn khỏe mạnh. Điều tra cho thấy đây **không phải bug** mà là hệ quả tất yếu của việc chỉ chạy 3 validator.

---

## 1. Công thức gốc trong code

Nguồn: `consensus/metanode/meta-consensus/config/src/committee.rs`

```rust
let fault_tolerance = (total_stake - 1) / 3;      // f — số lỗi tối đa chịu được
let quorum_threshold = total_stake - fault_tolerance;  // số stake tối thiểu cần đồng ý mỗi round
let validity_threshold = fault_tolerance + 1;
```

Với giả định **mỗi validator có 1 đơn vị stake bằng nhau** (đúng với cụm test hiện tại), `total_stake` chính là **số node** (`n`). Khi đó:

```
f (số node được phép chết/lỗi)  = ⌊(n − 1) / 3⌋
quorum_threshold (số node tối thiểu cần sống mỗi round) = n − f
```

Đây chính là công thức chuẩn BFT `n ≥ 3f + 1`, viết ngược lại để tính `f` lớn nhất có thể từ `n` cho trước.

**Quan trọng**: `f` là số node **được phép mất** (chết, mất kết nối, hoặc thậm chí ác ý/Byzantine) mà cụm vẫn tiếp tục hoạt động bình thường (an toàn *và* sống). Nếu số node lỗi vượt quá `f`, cụm vẫn **an toàn** (không bao giờ fork/sai dữ liệu — thuộc tính safety không bao giờ mất) nhưng sẽ **dừng tiến triển** (mất liveness) cho tới khi đủ node quay lại.

---

## 2. Bảng tra cứu nhanh (n = 1 → 8)

| Số validator (n) | f = ⌊(n−1)/3⌋ (chịu được tối đa) | quorum_threshold = n − f (tối thiểu cần sống) | Ghi chú |
|---:|---:|---:|---|
| 1 | 0 | 1 | Không chịu lỗi — node duy nhất chết là dừng hẳn |
| 2 | 0 | 2 | Không chịu lỗi — cần cả 2 |
| **3** | **0** | **3** | **Cấu hình cụm test hiện tại — không chịu được 1 node nào chết** |
| 4 | 1 | 3 | Mốc BFT nhỏ nhất chịu được 1 lỗi |
| 5 | 1 | 4 | Vẫn chỉ chịu 1 lỗi (dư 1 node so với mức 4) |
| 6 | 1 | 5 | Vẫn chỉ chịu 1 lỗi |
| 7 | 2 | 5 | Mốc chịu được 2 lỗi |
| 8 | 2 | 6 | Vẫn chỉ chịu 2 lỗi |

Nhận xét: `f` chỉ tăng thêm 1 mỗi khi `n` tăng thêm **3** — nghĩa là các mốc "đáng thêm node" là `n = 4, 7, 10, 13, ...` (mỗi lần tăng `f` lên 1). Thêm node ở giữa các mốc đó (ví dụ từ 4 lên 5 hoặc 6) chỉ tăng độ dự phòng, không tăng khả năng chịu lỗi.

---

## 3. Áp dụng cho tình huống thực tế

- **Cụm hiện tại (3 node)**: `f = 0`. Bất kỳ node nào chết — vì lý do gì (crash, mất mạng, bảo trì, cạn tài nguyên) — đều khiến 2 node còn lại **không thể tiến round tiếp** (đã quan sát trực tiếp: round consensus dừng hẳn, cả 2 node còn lại liên tục báo `Connection refused` khi cố liên lạc node đã chết).
- **Muốn chịu được 1 node chết mà vẫn hoạt động**: cần tối thiểu **4 validator**.
- **Muốn chịu được 2 node chết cùng lúc**: cần tối thiểu **7 validator**.

Công thức trên giả định stake đồng đều giữa các validator. Nếu stake không đồng đều, ngưỡng `quorum_threshold`/`validity_threshold` vẫn tính theo **tổng stake**, không phải số node — một validator stake lớn có thể một mình chiếm phần đáng kể của `f`, khiến "số node tối thiểu cần sống" không còn tương ứng 1:1 với bảng trên.

---

## 4. Không phải bug — là chi phí của việc dùng ít validator

Việc cụm 3 node dừng khi mất 1 node là **đúng theo thiết kế BFT chuẩn**, không phải lỗi có thể sửa bằng code (không có giá trị timeout/retry nào khắc phục được việc thiếu node — vấn đề là toán học chịu lỗi, không phải hiệu năng hay bug logic). Đây là lý do các mạng blockchain production thường chạy validator set lớn hơn nhiều so với 3 (ví dụ 21, 100, hoặc hàng trăm/nghìn validator), để vừa có `f` đủ lớn chịu được nhiều lỗi/tấn công đồng thời, vừa có dự phòng vận hành (bảo trì, nâng cấp luân phiên) mà không làm gián đoạn mạng.

Với môi trường **test** (như cụm 3 node hiện tại), việc dừng khi mất 1 node là chấp nhận được — chỉ cần biết đây là giới hạn cố ý, không phải điều cần điều tra thêm như 1 bug phần mềm.
