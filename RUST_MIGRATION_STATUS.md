# 🗺️ Báo Cáo Chuyển Đổi Hệ Thống Sang Rust & Trạng Thái Hiện Tại

Tài liệu này chi tiết hóa trạng thái hiện tại của quá trình chuyển đổi (migration) toàn bộ hệ thống Metanode từ Go sang Rust, xác định rõ các thành phần đã chạy bằng Rust và các phần còn lại của Go.

---

## 📐 1. Tổng Quan Kiến Trúc Sau Chuyển Đổi (Runtime Isolation)

Hiện tại, hệ thống đã hoàn thành **Phase 1: Hybrid Execution Mode (Go FFI Bypass)**. Mục tiêu cốt lõi là cô lập hoàn toàn luồng chạy (runtime) của Consensus và Execution để chạy độc lập trong Rust.

```mermaid
graph TD
    subgraph Rust Consensus Engine (100% Rust)
        Consensus[Mysticeti Consensus] --> |Sends blocks| ExecClient[ExecutorClient]
        ExecClient -->|rust_execution_enabled = true| LocalExec[Rust-native Mock Execution]
        LocalExec -->|Keccak256 StateRoot| Consensus
        LocalExec -->|Load local committee| CommSource[CommitteeSource]
    end
    
    subgraph Legacy Go Engine (Bypassed)
        GoFFI[Go CGo FFI Gateway] -.->|Bypassed at runtime| GoEVM[Go-EVM / AccountStateDB]
    end
    
    CommSource -.->|Read local file| CommJSON[config/committee.json]
```

---

## 🔄 2. Các Thành Phần Đã Chuyển Đổi Sang Rust Hoàn Toàn

Các thành phần sau đây hiện chạy **100% bằng Rust** và không còn thực hiện bất kỳ lệnh gọi FFI hay IPC nào sang Go khi bật `rust_execution_enabled = true`:

| Thành phần | Cơ chế thực thi cũ (Go) | Cơ chế thực thi mới (Rust) | Trạng thái |
| :--- | :--- | :--- | :--- |
| **Đồng thuận (Consensus)** | Gọi FFI khởi động, chạy Mysticeti. | Standalone Rust binary khởi động qua `main.rs`. | **100% Rust** |
| **Thực thi Khối (Block Exec)** | Gửi block bytes qua CGo callback, Go thực thi EVM. | Băm các block bytes bằng Keccak256 tạo StateRoot tất định. | **Rust-native Mock** |
| **Quản lý Epoch (Epoch Transition)**| Query Go Master để lấy thông tin epoch và timestamps. | Rust tự duy trì số epoch và timestamp qua `load_local_validators`. | **100% Rust** |
| **Ủy ban Validator (Committee)** | Lấy validator từ Go state DB qua FFI. | Tải trực tiếp validator từ file `committee.json` cục bộ. | **100% Rust** |
| **Đồng bộ chuỗi (Block Sync)** | Chuyển tiếp block cho Go qua FFI để lưu và sync. | Thực hiện lưu trữ và xử lý tiến trình sync độc lập trong Rust. | **100% Rust** |
| **Mạng lưới P2P (Networking)** | Gọi Go Network sync. | Mysticeti P2P gRPC + RustSyncNode thực hiện P2P block sync. | **100% Rust** |

---

## ⚠️ 3. Phần Nào Còn Chạy Hoặc Nằm Trong Go?

Mặc dù **Runtime** đã được cô lập 100% sang Rust (không gọi FFI lúc chạy nữa), mã nguồn Go vẫn nằm trong repository và có các liên kết biên dịch sau:

### A. Mã nguồn Go EVM (~237K dòng code Go)
Tệp tin tại `/home/abc/chain-n/metanode/execution` chứa toàn bộ EVM (Go-EVM), cơ chế Snapshot, MVM extensions và JSON-RPC Transaction Pool.
* **Tình trạng ở Runtime:** Không được triệu gọi (Bypassed) khi cờ `rust_execution_enabled = true`.
* **Tình trạng lúc biên dịch:** Vẫn được biên dịch thông qua lệnh `go build` trong bộ build check tổng để phục vụ các kịch bản chạy chế độ Hybrid cũ (khi cờ cấu hình là `false`).

### B. CGo FFI C-Bindings
Tệp [ffi.rs](file:///home/abc/chain-n/metanode/consensus/metanode/src/ffi.rs) vẫn chứa các hàm `extern "C"` như `metanode_register_callbacks` hay `metanode_start_consensus`.
* **Mục đích giữ lại:** Đảm bảo tương thích ngược để ứng dụng Go Master vẫn có thể liên kết tĩnh (static link) với thư viện Rust `libmetanode.a` mà không gây lỗi compile-time.

---

## 🚀 4. Lộ Trình Loại Bỏ Hoàn Toàn Mã Nguồn Go (Zero-Go)

Để đạt được mục tiêu loại bỏ 100% mã nguồn Go khỏi dự án (không còn file `.go` nào cần compile), hệ thống cần triển khai các bước tiếp theo trong **Phase 2 & 3** của lộ trình:

1. **Tích hợp TEE-REVM (Rust EVM):**
   * Thay thế mock Keccak256 StateRoot bằng việc thực thi giao dịch thực tế thông qua REVM của Rust (nằm trong `crates/metanode-tee-revm`).
   * Tích hợp cơ sở dữ liệu lưu trữ trạng thái tài khoản (Account State DB) viết bằng Rust.
2. **Xây dựng JSON-RPC Server bằng Rust:**
   * Viết RPC Server (axum/jsonrpsee) trong Rust để nhận giao dịch từ người dùng và đẩy trực tiếp vào mempool của Rust Consensus, loại bỏ hoàn toàn Go Mempool.
3. **Loại bỏ CGo static library:**
   * Thay thế tệp cấu hình biên dịch staticlib trong `Cargo.toml`.
   * Xóa thư mục `/execution` (Go) khỏi repo.

---

## 🛠️ 5. Hướng Dẫn Kích Hoạt Chế Độ Thực Thi Rust Thuần

Hiện tại, cấu hình mặc định đã được thiết lập để ưu tiên chạy Rust thuần. Tuy nhiên, bạn có thể kiểm tra cấu hình trong tệp `node.toml` của các node:

```toml
# consensus/metanode/config/node_0.toml

# Bật chế độ thực thi Rust thuần (Không gọi Go FFI)
rust_execution_enabled = true

# Các cổng RPC ngang hàng (P2P) kết nối trực tiếp giữa các node Rust
peer_rpc_port = 19200
peer_rpc_addresses = ["127.0.0.1:19201", "127.0.0.1:19202", "127.0.0.1:19203", "127.0.0.1:19204"]
```

Khi chạy ứng dụng bằng Rust binary độc lập:
```bash
cargo run --release --bin metanode -- start --config config/node_0.toml
```
Hệ thống sẽ chạy hoàn toàn độc lập với Go.
