# MetaNode Execution Layer (`execution/`)

Đây là phân hệ **Go Execution Engine** (module `github.com/meta-node-blockchain/meta-node`) của hệ thống blockchain MetaNode. Phân hệ này chịu trách nhiệm thực thi giao dịch song song (Block-STM & Union-Find), quản lý trạng thái xác thực (NOMT + PebbleDB), vận hành **Parent Settlement Chain** (`cmd/parent_chain`), **Rollup / Raft Sequencer** (`pkg/rollup`), máy ảo C++ MVM, đánh chỉ mục tìm kiếm toàn văn Xapian (`pkg/goxapian`) và cung cấp giao diện mạng kép (Ethereum JSON-RPC/WebSocket + Protobuf TCP).

---

## Cấu trúc Thư mục Chính

- **`cmd/simple_chain/`**: Tiến trình Node hợp nhất (`Validator`, `SyncOnly`/`RPC`, `Explorer`), nhúng trực tiếp thư viện đồng thuận Rust (`libmetanode`) và cây trạng thái NOMT (`libmtn_nomt`) qua CGo FFI.
- **`cmd/parent_chain/`**: Tiến trình **Parent Settlement & Registry Chain** chạy trên Rust BFT consensus (`--rust-config`), cung cấp HTTP REST API (`pkg/parentchain/http_rpc.go`); ChainID lấy từ genesis (mặc định `991`).
- **`cmd/tool/cross_chain_relayer/` & `cmd/tool/register_chains/`**: Daemon chuyển tiếp thông điệp xuyên chuỗi và công cụ khởi tạo/đăng ký chuỗi tự động.
- **`cmd/tool/`**: Các công cụ kiểm thử tải (`tps_blast`, `secp_tps_blast`), sinh khóa (`gen_spam_keys`, `keygen`) và kiểm thử RPC/TCP.
- **`executor/`**: Cầu nối CGo FFI giữa Go và Rust Consensus (`libmetanode`), điều phối chu kỳ Epoch và quản lý Snapshot.
- **`pkg/blockchain/tx_processor/` (Block-STM: `block_stm.go`, `true_block_stm.go`) & `pkg/grouptxns/`**: Động cơ thực thi song song suy đoán (Block-STM) và gom nhóm giao dịch tất định bằng thuật toán Union-Find.
- **`pkg/nomt_ffi/` & `pkg/account_state_db/`**: Quản lý trạng thái tài khoản/validator trên cây Merkle nhị phân NOMT kết hợp PebbleDB (`pkg/storage/`).
- **`pkg/parentchain/`, `pkg/rollup/` (gồm `account_registry_handler.go`)**: Logic xử lý của chuỗi mẹ, bộ sắp xếp chuỗi Raft (`raftfeed`), đồng chứng thực ủy ban BLS ($f+1$) và cổng kiểm soát tài khoản (`AccountGateParentRegistered`).
- **`pkg/mvm/` & `pkg/goxapian/`**: Tích hợp C++ Meta Virtual Machine và cơ sở dữ liệu tìm kiếm toàn văn C++ Xapian.

---

## Biên dịch & Kiểm thử

```bash
# Kiểm tra biên dịch toàn diện (Rust FFI + C++ MVM/Xapian + Go):
cd ../consensus/metanode/scripts
./build_check.sh

# Chạy Unit & Race Tests trên các gói cốt lõi:
cd ../../execution
go test -race ./pkg/parentchain/... ./pkg/rollup/... ./pkg/blockchain/tx_processor/...
```

## Tài liệu Liên quan
- [`EXECUTION_GUIDE.md`](./EXECUTION_GUIDE.md) — Kiến trúc chi tiết của tầng Go Execution và luồng xử lý của `BlockProcessor`.
- [`../PROJECT_STRUCTURE.md`](../PROJECT_STRUCTURE.md) — Bản đồ cấu trúc toàn bộ monorepo và giao diện FFI.
- [`../docs/BLOCK_PROCESSING_ARCHITECTURE.md`](../docs/BLOCK_PROCESSING_ARCHITECTURE.md) — Quy trình xử lý block sau đồng thuận và 6 quy tắc an toàn song song.
