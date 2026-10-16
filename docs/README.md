# MetaNode Technical Documentation (`docs/`)

Thư mục này chứa các tài liệu kiến trúc chuyên sâu của hệ thống MetaNode:

- **[`BLOCK_PROCESSING_ARCHITECTURE.md`](./BLOCK_PROCESSING_ARCHITECTURE.md):** Kiến trúc xử lý block sau đồng thuận (từ khi nhận commit từ Rust Consensus qua CGo FFI đến phân nhóm giao dịch bằng Union-Find, thực thi song song và tạo block hoàn chỉnh).
- **[`tee_evm_architecture.md`](./tee_evm_architecture.md):** Kiến trúc triển khai máy ảo EVM không trạng thái (Stateless Execution) bên trong Môi trường Thực thi Tin cậy (TEE - Intel SGX/TDX) kết hợp với cây trạng thái `nomt` (Nearly Optimal Merkle Trie) và Witness verification.

Để xem bản đồ toàn bộ kho mã nguồn và danh mục tài liệu ở thư mục gốc, vui lòng tham khảo:
- **[`../README.md`](../README.md)** — Tổng quan hệ thống & hướng dẫn bắt đầu nhanh.
- **[`../PROJECT_STRUCTURE.md`](../PROJECT_STRUCTURE.md)** — Bản đồ cấu trúc dự án và giao diện FFI/IPC.
- **[`../OPERATIONS_GUIDE.md`](../OPERATIONS_GUIDE.md)** — Sổ tay triển khai và vận hành cụm node.
