# MetaNode Consensus Layer (`consensus/`)

Đây là phân hệ **Rust Consensus & Authenticated State FFI Workspace** của hệ thống blockchain MetaNode. Phân hệ này bao gồm động cơ đồng thuận **Mysticeti DAG-BFT** (`consensus/metanode`) và thư viện cây trạng thái **NOMT (Nearly Optimal Merkle Trie)** (vendored tại `vendor/nomt`; lớp FFI nằm ở `execution/pkg/nomt_ffi/rust_lib`), được biên dịch thành thư viện tĩnh/động C-ABI và nhúng trực tiếp vào tiến trình Go thông qua CGo FFI.

---

## Cấu trúc Workspace

- **`metanode/`**: Động cơ đồng thuận Mysticeti DAG-BFT và cầu nối FFI (`libmetanode.a`):
  - **Zero-Fork Peer Attestation (`CommitVoteMonitor`):** Đảm bảo 100% không phân nhánh (Zero-Fork Invariant). Một commit chỉ được chuyển sang Go thực thi khi có bằng chứng $2f+1$ peers đồng ý trên cùng digest (`PeerAttestResult::Ok`) hoặc ở trạng thái `TRUE cold-start`. Tuyệt đối không sử dụng timeout để ép thực thi commit.
  - **Epoch transition:** Chuyển giao Epoch và lấy danh sách Ủy ban (Committee) từ trạng thái Go qua FFI.
  - **Quorum CommitSyncer & CatchupManager:** Đồng bộ `CertifiedCommit` và khôi phục lịch sử cho các node khởi động lại hoặc chạy ở chế độ `SyncOnly`.
- **`vendor/nomt/`**: NOMT được vendor; thư viện C-ABI `libmtn_nomt` (crate `mtn-nomt-ffi`, `execution/pkg/nomt_ffi/rust_lib`) đóng gói NOMT phục vụ lưu trữ trạng thái xác thực tốc độ cao và sinh bằng chứng Merkle (`eth_getProof`) cho `account_db_nomt` và `stake_db_nomt`.
- **`../crates/`** (ở root repo): Các thư viện nền tảng dùng chung: mật mã học BLS12-381/Ed25519 (`shared-crypto`), lưu trữ RocksDB (`typed-store`), mạng P2P (`mysten-network` / `anemo`) và Prometheus metrics (`mysten-metrics`).

---

## Biên dịch & Kiểm tra

```bash
# Kiểm tra biên dịch toàn bộ Rust FFI + C++ + Go (không lỗi, không cảnh báo):
cd metanode/scripts
./build_check.sh

# Hoặc biên dịch riêng các thư viện Rust ở chế độ release:
cd ..
cargo build --release --lib
# NOMT FFI:
cd ../../execution/pkg/nomt_ffi/rust_lib
cargo build --release
```

## Tài liệu Liên quan
- [`CONSENSUS_GUIDE.md`](./CONSENSUS_GUIDE.md) — Kiến trúc chi tiết của `mtn-consensus`, vòng đời Epoch và giao diện CGo FFI.
- [`metanode/Readme.md`](./metanode/Readme.md) — Hướng dẫn cấu hình và luồng xử lý giao dịch trong `consensus/metanode`.
- [`SECURITY_VI.md`](./SECURITY_VI.md) / [`SECURITY.md`](./SECURITY.md) — Cơ chế bảo mật và phòng chống tấn công tầng đồng thuận.
