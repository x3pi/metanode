# Execution Layer Guide (Go)

Module `github.com/meta-node-blockchain/meta-node` (Go 1.23, xem `go.mod`). Tầng này nhận các commit đã được sắp thứ tự từ Rust consensus (hoặc từ Raft feed), thực thi giao dịch, cập nhật world state và phục vụ RPC. Bản đồ thư mục: [`README.md`](./README.md) và [`../PROJECT_STRUCTURE.md`](../PROJECT_STRUCTURE.md).

## Kiến trúc tiến trình

Một tiến trình duy nhất (`cmd/simple_chain`) nhúng Rust consensus (`libmetanode`) và NOMT (`libmtn_nomt`) qua CGo FFI. Không còn mô hình Master/Sub, AccountBatch hay IPC qua Unix socket giữa hai tiến trình Go.

Vai trò node (Validator, SyncOnly/RPC, Explorer) và chế độ đồng thuận (`consensus_mode`: `raft` hoặc để trống = Rust BFT) được chọn bằng file config JSON, ví dụ `cmd/simple_chain/config-master-node0.json`. Chạy: `./simple_chain --config <file>`.

## Pipeline xử lý giao dịch và block

Mọi file nằm trong `cmd/simple_chain/processor/` trừ khi ghi khác.

1. **Ingress:** `connection_processor.go` (TCP/WS) và JSON-RPC (`rpc_transaction.go`) → `transaction_processor.go` (`SendRawTransaction(s)`).
2. **Validate:** `tx_validator_pool_core.go`.
3. **Forward:** `tx_batch_forwarder_core.go` gửi batch sang Rust qua FFI, hoặc vào Raft feed ở chế độ `raft`.
4. **Nhận commit:** `executor/listener.go` nhận sub-DAG đã commit từ Rust qua FFI callback.
5. **Thực thi:** `pkg/blockchain/tx_processor/` — `TrueBlockSTM` (`true_block_stm.go`), `native_fast_path.go` cho block thuần native transfer, `block_stm.go` (`ProcessTransactionsOptimistic`); nhóm giao dịch bằng Union-Find ở `pkg/grouptxns/`.
6. **Tạo block:** `block_processor_processing.go`; **commit:** `block_processor_commit.go` (state ghi qua NOMT + PebbleDB).
7. **Phát block:** `block_processor_broadcast.go` cho SyncOnly/RPC node.

## Lưu trữ

State tài khoản/validator dùng NOMT (`pkg/trie/nomt_state_trie.go`, FFI ở `pkg/nomt_ffi/`) kèm PebbleDB (`pkg/storage/`). Cấu trúc thư mục dữ liệu: [`../DATABASE_STRUCTURE.md`](../DATABASE_STRUCTURE.md).

## Bất biến cần giữ

- Timestamp block lấy từ Rust consensus, không dùng `time.Now()`.
- Kết quả thực thi phải tất định giữa các validator: cùng commit → cùng state root, bất kể mức song song. Thà pending chứ không fork ([`../AGENTS.md`](../AGENTS.md), Part 2.5).
- Mọi thay đổi luật thực thi/phí là thay đổi đồng thuận: phải deploy đồng loạt tất cả validator.
- Chuỗi allocation CGo phải được giải phóng đúng (`defer C.free`).

## Build và test

```bash
# Kiểm tra Rust FFI + C++ MVM/Xapian + Go (bắt buộc sau khi sửa code)
cd ../consensus/metanode/scripts && ./build_check.sh

# Unit test Go
cd execution && go test ./pkg/blockchain/... ./pkg/grouptxns/ ./cmd/simple_chain/...
```

Triển khai cụm: [`../OPERATIONS_GUIDE.md`](../OPERATIONS_GUIDE.md).
