# Kiến Trúc Luồng Xử Lý Giao Dịch (Transaction Flow)

**⚠️ LƯU Ý QUAN TRỌNG CHO DEVELOPER:**  
Bất cứ ai khi tham gia chỉnh sửa mã nguồn (đặc biệt là các module tiếp nhận giao dịch, module đồng thuận Rust, hoặc module đồng bộ trạng thái Go) **TUYỆT ĐỐI PHẢI** tuân thủ và nắm rõ luồng đi của một giao dịch dưới đây. Việc hiểu sai hoặc làm tắt luồng sẽ dẫn đến phá vỡ tính đồng thuận (Forks), sai lệch State Master-Sub, hoặc giao dịch bị kẹt.

## Luồng Đi Tiêu Chuẩn Của Một Giao Dịch (TX)

Quá trình vòng đời của một giao dịch diễn ra theo đúng thứ tự luồng xử lý đa tầng sau:

1. **[Client] Gửi Giao Dịch**
   - Client (người dùng/ví) tạo, ký giao dịch và gửi lên cổng RPC/API của **Sub Node**.

2. **[Sub] Tiền Xử Lý (Pre-processing)**
   - Sub Node đóng vai trò là "lớp bảo vệ" tiếp nhận giao dịch.
   - Tại đây, Sub Node tiến hành kiểm tra nhanh tính hợp lệ cơ bản của giao dịch (như tính chính xác của Chữ ký BLS/ECDSA, định dạng, verify format) nhằm lọc sạch rác (spam) trước khi tiến sâu vào trong.

3. **[Sub ➔ Rust] Đưa Vào Đồng Thuận & Đóng Gói Khối**
   - Các giao dịch vượt qua vòng tiền xử lý sẽ được đẩy qua cho **Rust Consensus Layer** (Metanode).
   - Tầng Rust chịu trách nhiệm gom cụm (batching/sub-batching hàng chục ngàn TXs), chạy thuật toán đồng thuận tốc độ cao để chốt thứ tự giao dịch (ordering) và chính thức đóng gói chúng tạo thành một **Block**.

4. **[Rust ➔ Master] Thực Thi Block (Execution)**
   - Khối (Block) sau khi đã được chốt từ Rust sẽ được dội ngược về cho **Master Node** (Go Layer).
   - Master Node bắt đầu Thực thi (Execute) toàn bộ các giao dịch trong Block đó (kích hoạt Smart Contract qua C++ MVM, tính toán phí, và tính toán State Root mới). Master Node chính là "nguồn chân lý" duy nhất ra quyết định cập nhật State.

5. **[Master ➔ Sub] Đồng Bộ Trạng Thái & Dữ Liệu Khối**
   - Sau khi Master thi hành xong Block và chốt State, nó sẽ thực hiện luồng đồng bộ (Sync) dữ liệu Block và kết quả thực thi (State/Receipts) sang lại cho toàn bộ các **Sub Nodes**.
   - Các Sub Nodes ghi nhận và cập nhật trực tiếp thay đổi vào Cơ sở dữ liệu nội bộ (PebbleDB/RocksDB) của mình.

6. **[Sub ➔ Client] Trả Kết Quả Receipt**
   - Ngay khi Sub Node đã đồng bộ hoàn tất (ghi nhận State thành công từ Master), nó sẽ xuất và trả lại **Receipt** (biên lai kết quả giao dịch) cho **Client**.
   - Vòng đời giao dịch hoàn tất.

---
### 📝 Tóm Tắt Ngắn Gọn (Sơ đồ Tuyến tính)

`Client` ➔ `Sub` *(Tiền xử lý)* ➔ `Rust` *(Đồng thuận & Chốt Block)* ➔ `Master` *(Thực thi EVM/Contract)* ➔ `Block Sync` *(Đồng bộ Master sang Sub)* ➔ `Sub` *(Trả Receipt)* ➔ `Client`
