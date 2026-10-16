# MetaNode Consensus Engine (`consensus/metanode`)

Động cơ đồng thuận **Mysticeti DAG-BFT** hiệu năng cao của MetaNode, được biên dịch thành thư viện C-ABI (`libmetanode`) và nhúng trực tiếp vào tiến trình Go Execution Engine (`cmd/simple_chain`) thông qua **CGo FFI**.

---

## 🎯 Tổng quan & Nguyên tắc Thiết kế

1. **Kiến trúc Tiến trình Hợp nhất (Unified Single-Process FFI):**
   - Toàn bộ quá trình nhận batch giao dịch từ Go (`submit_transactions_ffi`), đẩy commit đã chốt sang Go (`SendCommittedSubDag` callback) và truy vấn Ủy ban/Epoch đều diễn ra trực tiếp qua bộ nhớ CGo FFI, loại bỏ độ trễ tuần tự hóa qua socket giữa hai tiến trình.
2. **Nguyên tắc Tối thượng — Zero-Fork Invariant:**
   - **Thà giữ commit ở trạng thái `PENDING` (chờ) chứ tuyệt đối không phân nhánh (fork).**
   - Sử dụng `CommitVoteMonitor` và `CoordinationHub` để kiểm chứng bằng chứng $2f+1$ peers đồng ý trên cùng digest (`PeerAttestResult { Ok, Conflict, Insufficient }`).
   - **Cấm tuyệt đối** việc dùng `sleep()` hoặc `timeout()` để ép dispatch commit chưa được xác minh.
3. **Chuyển giao Epoch An toàn (Advance-First Epoch Transition):**
   - Khi một commit chứa giao dịch hệ thống `EndOfEpoch` đạt quorum, Rust gọi `AdvanceEpoch` sang Go trước, sau đó truy vấn danh sách Ủy ban (Committee) mới tại đúng `last_global_exec_index` để khởi tạo `ConsensusAuthority` cho epoch tiếp theo.
4. **Linearization Tất định (Ancestor-Only Linearization):**
   - Chỉ các DAG block truy vết nhân quả trực tiếp từ Leader mới được đưa vào `CommittedSubDag`, đảm bảo 100% tính tất định trên mọi validator.

---

## 🏗️ Kiến trúc Luồng Dữ Liệu

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│                  UNIFIED NODE PROCESS (cmd/simple_chain)                     │
│                                                                              │
│  ┌────────────────────────────────────┐   FFI    ┌────────────────────────┐  │
│  │        Go Execution Layer          │ ──────►  │   Rust TxReceiver      │  │
│  │  • TxPool & Batch Forwarder        │ (Batch)  │  (network/tx_receiver) │  │
│  │  • BlockProcessor & Block-STM      │          └───────────┬────────────┘  │
│  │  • NOMT State Trie (libmtn_nomt)   │                      ▼               │
│  │  • Validator / Epoch State         │          ┌────────────────────────┐  │
│  └─────────────────▲──────────────────┘          │ Mysticeti DAG-BFT Core │  │
│                    │                             │  • AuthorityNode       │  │
│                    │ FFI Callback                │  • Linearizer          │  │
│                    │ (ExecutableBlock)           │  • CommitVoteMonitor   │  │
│  ┌─────────────────┴──────────────────┐          │  • CommitSyncer        │  │
│  │        Rust CommitProcessor        │ ◄────────┴────────────────────────┘  │
│  │  • Zero-Fork Peer Attestation      │                                      │
│  │  • Advance-First Epoch Transition  │          ┌────────────────────────┐  │
│  └────────────────────────────────────┘          │ RocksDB Per-Epoch DAG  │  │
│                                                  └────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────┘
```

---

## 📂 Cấu trúc Thư mục Chính

- `src/lib.rs` & `src/ffi/`: Điểm vào C-ABI FFI (`start_consensus_node_ffi`, `submit_transactions_ffi`, callbacks sang Go).
- `src/node/`: Quản lý vòng đời `ConsensusNode`, khởi tạo Ủy ban (`committee.rs`), đồng bộ Epoch (`epoch_monitor.rs`, `transition/`) và `executor_client/`.
- `src/consensus/`: Điều phối `commit_processor/`, `epoch_transition.rs`, `tx_recycler.rs`.
- `meta-consensus/core/`: Lõi thuật toán Mysticeti DAG-BFT (`authority_node.rs`, `linearizer.rs`, `commit_vote_monitor.rs`, `coordination_hub.rs`, `commit_syncer/`).
- `scripts/`:
  - **`build_check.sh`**: Script chuẩn để kiểm tra biên dịch toàn diện Rust + C++ + Go FFI không lỗi và không cảnh báo.
  - **`CLUSTER_OPERATIONS.md` & `README.md`**: Hướng dẫn sử dụng các script vận hành và kiểm thử cụm cục bộ.

---

## ⚡ Biên dịch & Kiểm tra

Luôn sử dụng `build_check.sh` để xác nhận cả Rust Consensus, NOMT FFI và Go Execution biên dịch đồng bộ:

```bash
cd scripts
./build_check.sh
```

Xem thêm chi tiết về kiến trúc đồng thuận tại [`../CONSENSUS_GUIDE.md`](../CONSENSUS_GUIDE.md).
