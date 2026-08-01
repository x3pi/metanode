# Metanode Single Chain

Dùng `setup_and_run.sh` để khởi tạo và chạy 1 node cục bộ.

## 🚀 Các lệnh chạy

> **Lưu ý:** Mặc định các lệnh `setup_and_run.sh` **LUÔN THỰC HIỆN BUILD** lại code (Go & Rust). Sử dụng thêm cờ `--no-build` nếu bạn không sửa code và muốn chạy nhanh hơn.


- **Chạy tiếp (Giữ nguyên data cũ và build):**
  ```bash
  bash setup_and_run.sh
  ```

- **Chạy mới hoàn toàn (Xóa sạch data cũ):**
  ```bash
  bash setup_and_run.sh --clean
  ```

- **Chạy nhanh (Bỏ qua quá trình biên dịch code):**
  ```bash
  bash setup_and_run.sh --no-build
  ```

- **Dừng hệ thống:**
  ```bash
  bash single_chain_data/stop_single_chain.sh
  ```

*(Có thể kết hợp các cờ: `bash setup_and_run.sh --clean --no-build`)*

---

## 🛠️ Dữ liệu sinh ra (`single_chain_data/`)

- `dev_accounts.json`: Chứa private key của 5 ví test (đã có sẵn tiền).
- `node-0/logs/node-0.log`: Log hoạt động của node (dùng để debug).
- `start_single_chain.sh` / `stop_single_chain.sh`: Script start/stop trực tiếp của node.
