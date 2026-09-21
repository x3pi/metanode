# Hướng dẫn Cơ chế Data Availability (DA) & Fallback Recovery cho Private Chain

Tài liệu này mô tả chi tiết cơ chế lưu trữ dữ liệu dự phòng (Data Availability) của Private Chain lên Root Anchor và cách thức khôi phục (Recovery) khi hệ thống gặp sự cố.

## 1. Tổng quan kiến trúc

Trong hệ sinh thái đa chuỗi (Multi-chain), Private Chain tạo ra các giao dịch nhưng không tự lưu trữ mãi mãi mà sẽ "gửi gắm" dữ liệu này lên Public Chain (Root Anchor) để tận dụng tính phi tập trung và minh bạch.

**Quy trình bình thường:**
1. Các node trong Private Chain sinh ra block mới chứa danh sách các giao dịch.
2. `CommitAttestationWorker` trên Private Chain sẽ đóng gói (serialize) danh sách giao dịch này và nén lại bằng thuật toán **zlib** để giảm thiểu dung lượng.
3. Dữ liệu sau khi nén được gửi đến Gateway của Root Anchor thông qua API `submitCommitAttestation`.

**Quy trình lỗi mạng (Fallback):**
Nếu Root Anchor gặp sự cố (bảo trì, chết node, đứt mạng), việc gửi dữ liệu sẽ thất bại. Để đảm bảo không bị mất khối dữ liệu nào:
- `CommitAttestationWorker` sẽ phát hiện lỗi kết nối.
- Dữ liệu nén sẽ được ghi thẳng xuống ổ cứng nội bộ của Private Chain với định dạng `batch_<blockHash>.dat`.
- Vị trí lưu trữ mặc định: `<DataDir>/execution/pending_batches/`.

---

## 2. Các thành phần mã nguồn

- **Cơ chế nén và Fallback:** Nằm tại `metanode/execution/pkg/blockchain/tx_processor/gateway_handler.go`. 
  - Worker: `CommitAttestationWorker`
- **Công cụ khôi phục (Recovery Tool):** Nằm tại `metanode/execution/cmd/tool/recover_from_anchor/`.

---

## 3. Hướng dẫn sử dụng Công cụ Khôi phục (Recovery Tool)

Khi Private Chain bị hỏng dữ liệu cục bộ, bạn có thể dùng công cụ này để kết nối tới Root Anchor, đọc lại toàn bộ các batch giao dịch đã từng nén và submit lại vào Private Chain nội bộ.

### Biên dịch và Chạy:

Chuyển tới thư mục chứa công cụ:
```bash
cd /home/abc/nhat/con-chain-v2/metanode/execution/cmd/tool/recover_from_anchor
```

Chạy công cụ với các tham số tương ứng (thay đổi URL nếu cần):
```bash
go run . -chainid=101 \
         -root=http://192.168.1.234:10746 \
         -private=http://192.168.1.234:8546
```

**Các tham số:**
- `-chainid`: ID của Private Chain cần khôi phục (ví dụ: `101`).
- `-root`: Địa chỉ RPC của Root Anchor (Public Chain).
- `-private`: Địa chỉ RPC của Private Chain đang cần được bơm lại dữ liệu.

**Luồng hoạt động của công cụ:**
1. Lấy danh sách block mới nhất từ Root Anchor.
2. Quét các logs/sự kiện của Gateway liên quan đến hành động nạp Batch (Submit Attestation) của đúng `chainid`.
3. Tải payload nén dạng zlib từ Root Anchor.
4. Giải nén payload thành mảng các giao dịch nguyên thủy.
5. Gửi từng giao dịch (thông qua `eth_sendRawTransaction`) vào mempool của Private Chain để khôi phục trạng thái.

---

## 4. Kịch bản chạy thử (Demo) nhanh

Nếu bạn muốn kiểm chứng tính năng nén file `.dat` khi Rớt mạng (Root Anchor chết) mà không cần phải giả lập chạy cả hệ thống consensus phức tạp, bạn có thể chạy đoạn script mô phỏng sau:

1. Mở file script có sẵn:
   ```bash
   cat /home/abc/.gemini/antigravity-ide/brain/05a5dc03-9b9f-4498-9764-2afb3fa041b3/scratch/demo_fallback.go
   ```
2. Chạy file giả lập:
   ```bash
   cd /home/abc/nhat/con-chain-v2/metanode/execution
   go run /home/abc/.gemini/antigravity-ide/brain/05a5dc03-9b9f-4498-9764-2afb3fa041b3/scratch/demo_fallback.go
   ```
3. Kết quả: Script sẽ tự động tạo một giao dịch ảo, cố gắng gửi lên Root Anchor (nhưng cố tình kết nối tới port chết `9999`). Khi thất bại, script kích hoạt fallback và lưu thành công file `batch_1.dat` siêu nén trên ổ cứng.
