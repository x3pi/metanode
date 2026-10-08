# Bản Đăng Ký Trước (Preregistration): Đo Lường & Tối Ưu Hóa PrepareTransactions và FFI Serialization

**Ngày đăng ký:** 2026-10-08  
**Tài liệu kế hoạch:** `note/plan_prepare_transactions_ffi_opt_20261008.md`  
**Mục tiêu:** Đo lường nano-giây trực tiếp từng thành phần bên trong `PrepareTransactions` và FFI serialization; xác định chính xác nút thắt thực sự; chỉ tối ưu hóa các thành phần chiếm $\ge 10\%$ chu kỳ block; kiểm định A/B xen kẽ độc lập.

---

## 1. Giả thuyết nghiên cứu & Cổng quyết định (Decision Gates)

### Giả thuyết Giai đoạn 1 (Đo lường trực tiếp):
- Giả thuyết 1A: `sort.Slice` trong `PrepareTransactions` không chiếm phần lớn thời gian trễ (dự kiến $< 30\text{ ms}$ cho 8.000 txs vì hash 32 bytes đã được cache).
- Giả thuyết 1B: Vòng lặp tuần tự gọi `transaction.ValidateEnvelopeBinding(tx)` (thực hiện RLP decode và `ecrecover` qua `e_types.Sender`) chiếm tỷ trọng áp đảo bên trong `PrepareTransactions` (> 200 ms).
- Giả thuyết 1C: Giới hạn 16 workers trong `ParallelUnmarshalTransactions` và việc unmarshal 2 tầng (digest proto -> tx proto) chiếm tỷ trọng đáng kể.

### Cổng quyết định chuyển sang Giai đoạn 2 (Decision Gate to Phase 2):
- **Ngưỡng sàng lọc:** Chỉ xem xét tối ưu hóa (Giai đoạn 2) đối với thành phần nào chiếm $\ge 10\%$ tổng chu kỳ block ($T_{cycle} \approx 550\text{ ms}$, tức $\ge 55\text{ ms/block}$) trên số đo thực tế tại cụm 4 validator.
- **Tính toán cận trên Amdahl:** Cận trên cải thiện TPS tiềm năng sẽ được tính toán trực tiếp từ tỷ lệ thời gian đo được của thành phần đó:
  $$S_{max} = \frac{1}{1 - p}$$
  trong đó $p$ là tỷ lệ thời gian thành phần đó chiếm trong chu kỳ block thực tế.

---

## 2. Tiêu chí xác nhận Giai đoạn 2 (A/B Interleaved Validation)

Nếu một thành phần vượt qua Cổng quyết định và được tối ưu hóa an toàn:
1. **Kiểm định A/B xen kẽ:**
   - Số lượt: $\ge 8$ lượt BEFORE và $\ge 8$ lượt AFTER (tổng $\ge 16$ lượt).
   - Thứ tự: Xen kẽ ngẫu nhiên có seed (`20261008`).
   - Môi trường: Cụm 4 validator độc lập cổng 31xxx, wipe sạch dữ liệu từ `/tmp/gate_4val_clean_template` trước mỗi lượt.
   - Tải phát: `secp_tps_blast` sustained 60 giây, batch size 1000 txs, mode TCP EIP-1559, cờ `-verify-parity`.
2. **Tiêu chí Thành công (PASS/FAIL):**
   - **Xác nhận (PASS):** Hiệu số TPS trung vị (Median Improvement) $\ge +5,0\%$ VÀ Khoảng tin cậy 95% (Welch 95% CI) của hiệu số KHÔNG chứa giá trị 0 ($p < 0,05$).
   - **Không xác nhận (FAIL/Inconclusive):** Hiệu số trung vị $< +5,0\%$ HOẶC khoảng tin cậy 95% chứa giá trị 0.
   - **Zero-Fork Invariant (Bất khả xâm phạm):** Tỷ lệ Zero-Fork Verified PHẢI ĐẠT 100% trên tất cả các lượt chạy. Bất kỳ sự sai lệch block hash hoặc state root nào giữa các node $\rightarrow$ DỪNG NGAY LẬP TỨC và hủy bỏ thay đổi.

---

## 3. Ràng buộc bảo toàn kết quả (Zero State Drift & Bit-for-Bit Parity)

- Mọi thay đổi trong `PrepareTransactions` **TUYỆT ĐỐI KHÔNG ĐƯỢC THAY ĐỔI THỨ TỰ, NỘI DUNG HOẶC SỐ LƯỢNG GIAO DỊCH**.
- Đầu ra của `PrepareTransactions` tối ưu phải **giống hệt bit-for-bit** hàm gốc trên mọi tập dữ liệu (kể cả dữ liệu bất thường, envelope binding sai, giao dịch trùng lặp, digest rỗng).
- Phải có property test kiểm chứng tính đồng nhất 100% trước khi đưa binary vào cụm test.
