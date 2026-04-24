# Node Restart Architecture — Snapshot & Epoch Recovery

## Tổng Quan

Tài liệu mô tả kiến trúc khởi động lại node cho cả hai chế độ: **SyncOnly** và **Validator**.
Node có thể restart trong nhiều tình huống: crash, nâng cấp, hoặc phục hồi từ snapshot.

---

## 1. SyncOnly Node Restart

### Yêu cầu
- Đồng bộ block tiếp tục từ nơi Go cuối cùng thực thi.

### Luồng xử lý

```
Node Restart (SyncOnly)
│
├─ ExecutorClient.get_last_block_number() → go_last_block (VD: 500)
├─ BlockQueue.new(go_last_block + 1)  ← queue bắt đầu từ block 501
│
└─ sync_loop() [50ms turbo trong 30s đầu]
    │
    ├─ sync_once()
    │   ├─ get_last_block_number() → go_block hiện tại
    │   ├─ fetch_blocks_from_peer(go_block+1 .. go_block+2000)
    │   ├─ sync_and_execute_blocks(blocks) → Go FFI
    │   └─ prefetch next batch (parallel với FFI)
    │
    ├─ auto_epoch_sync()  [khi go_epoch > rust_epoch]
    │   ├─ get_safe_epoch_boundary_data()
    │   ├─ Rebuild committee + TonicClient
    │   └─ Update epoch_base_index
    │
    └─ Lặp cho đến khi bắt kịp cluster
```

### File liên quan
- `rust_sync_node/start.rs` — Khởi tạo RustSyncNode với `go_last_block + 1`
- `rust_sync_node/sync_loop.rs` — Main sync loop, turbo mode, auto epoch sync
- `rust_sync_node/fetch.rs` — P2P block fetching
- `rust_sync_node/block_queue.rs` — Block ordering queue

---

## 2. Validator Node Restart

### Yêu cầu
- Đuổi kịp epoch nếu chậm epoch → đồng bộ block tới ít nhất block cuối cùng của epoch gần nhất.
- Đồng thời đồng bộ DAG gần nhất → khi kịp epoch chuyển sang lấy block từ DAG và tham gia vote.

### 2a. Epoch Catch-Up (Khi chậm epoch)

```
epoch_monitor (chạy liên tục, poll mỗi 10s)
│
├─ Query: local_go_epoch vs network_epoch
│
├─ [network_epoch > rust_epoch] → Multi-epoch step-through:
│   │
│   for target_epoch in (rust_epoch+1)..=network_epoch:
│   │
│   ├─ Fetch epoch boundary data từ peers
│   ├─ Fetch blocks tới boundary_block
│   ├─ sync_and_execute_blocks() → Go (thực thi qua NOMT)
│   ├─ advance_epoch() trên Go
│   ├─ try_start_epoch_transition() trên Rust
│   └─ transition_to_epoch_from_system_tx()
│       ├─ Setup new committee
│       ├─ Start ConsensusAuthority (nếu trong committee)
│       └─ hoặc Start RustSyncNode (nếu SyncOnly cho epoch này)
│
└─ [network_epoch == rust_epoch] → Same-epoch checks:
    ├─ SyncOnly trong committee? → Chờ Go bắt kịp (gap ≤ 3)
    └─ transition_mode_only() → SyncOnly → Validator
```

### 2b. DAG Sync → Consensus Participation

```
AuthorityNode::start()
│
├─ CommitConsumerArgs::new(go_replay_after, go_replay_after)
│   └─ go_replay_after = last_global_exec_index từ Go (VD: 1000)
│
├─ DagState::new() → empty (snapshot đã xóa DAG)
│
├─ CommitConsumerMonitor.set_highest_handled(max(go:1000, dag:0)) = 1000
│
├─ CommitObserver::recover_and_send_commits()
│   └─ Store trống + replay_after=1000 → skip replay, log, return
│      (Trước fix: assert_eq!(1000, 0) → PANIC!)
│
├─ Core::recover()
│   ├─ try_commit([]) → nothing to commit
│   └─ try_propose(true) → should_propose()
│       └─ is_bootstrapping() = true → return false
│          (Trước fix: propose block round 1 → EQUIVOCATION!)
│
├─ CommitSyncer (song song):
│   ├─ update_state()
│   │   ├─ local_commit=0, highest_handled=1000
│   │   └─ reset_to_network_baseline(1000)
│   │       ├─ DAG.gc_round = 1000
│   │       └─ synced_commit_index = 1000
│   │
│   ├─ Phase: Bootstrapping → CatchingUp (khi quorum > 0)
│   │   └─ Fetch commits 1001..quorum_commit từ peers
│   │
│   ├─ Phase: CatchingUp → Healthy (khi local >= quorum)
│   │   └─ Node đã bắt kịp network
│   │
│   └─ Core.should_propose() → true (Healthy)
│       └─ try_propose() → JOIN CONSENSUS ✅
│
└─ Synchronizer (song song):
    └─ Fetch blocks qua P2P subscription → DAG building
```

---

## 3. Phase State Machine (ConsensusCoordinationHub)

```
                          highest_handled == 0
                         (GENESIS - fresh start)
┌──────────────┐ ────────────────────────────────────────────────► ┌─────────┐
│ Bootstrapping │                                                  │ Healthy │
│              │                                                   │         │
│ • No propose │    highest_handled > 0 (SNAPSHOT)                 │ • Propose│
│ • Detect Go  │ ────► fast-forward ────► quorum > 0 ────►        │ • Vote   │
│   state      │         baseline         detected                 │ • Normal │
└──────────────┘                     ┌────────────┐               └─────────┘
                                     │ CatchingUp  │──local>=quorum──►│
                                     │ • No propose│                    
                                     │ • Turbo sync│
                                     └────────────┘
```

### Các điểm kiểm tra phase:
| Component | Check | Hành vi |
|-----------|-------|---------|
| `Core::should_propose()` | `is_bootstrapping() \|\| is_catching_up()` | Block proposals |
| `CommitSyncer` | `is_catching_up()` | 4x batch size, skip throttle |
| `CommitSyncer` | `is_healthy()` | Normal batch, apply backpressure |

---

## 4. CommitConsumerArgs Initialization Points

Tất cả các điểm khởi tạo CommitConsumerArgs **phải** truyền Go state:

| File | Ngữ cảnh | Giá trị |
|------|----------|---------|
| `consensus_node.rs:847` | Initial startup | `new(go_replay_after, go_replay_after)` |
| `consensus_setup.rs:35` | Validator epoch transition | `new(go_replay_after, go_replay_after)` |
| `consensus_setup.rs:159` | SyncOnly epoch transition | `new(go_replay_after_sync, go_replay_after_sync)` |
| `mode_transition.rs:237` | SyncOnly→Validator promotion | `new(go_replay_after, go_replay_after)` |

Công thức:
```rust
let go_replay_after = if executor_commit_enabled && last_global_exec_index > 0 {
    last_global_exec_index as u32
} else {
    0  // Genesis hoặc không có executor
};
```

---

## 5. Lưu Ý Quan Trọng

### 5a. GEI (Global Execution Index) vs Commit Index
- **GEI**: Đếm tuần tự TẤT CẢ commits (bao gồm empty), dùng trong Go execution.
- **Commit Index**: Epoch-local, reset về 1 mỗi epoch mới.
- Mapping: `GEI = epoch_base_index + commit_index`
- Trong fix này, ta dùng `last_global_exec_index` làm proxy cho commit index vì chúng ánh xạ 1:1.

### 5b. Snapshot Restore
- Snapshot xóa toàn bộ DAG storage nhưng giữ Go state.
- → DAG trống (`last_commit_index = 0`)
- → Go đã xử lý (`last_global_exec_index = N`)
- Fix đảm bảo Rust nhận biết Go state qua `CommitConsumerArgs`.

### 5c. Equivocation Prevention
- **Equivocation**: Node propose 2 block khác nhau cùng round → bị slash.
- Fix chặn propose trong Bootstrapping phase, đợi DAG baseline được thiết lập.
- Chỉ khi Healthy phase, Core mới được phép propose.

### 5d. Empty Commit Fast-Skip (Catch-up Optimization)
- Khi catching up, 90%+ DAG commits là **empty** (không có giao dịch).
- **Trước**: Mỗi empty commit đi qua full pipeline: Leader resolution → Protobuf → FFI → Go.
- **Sau**: Empty commits skip toàn bộ pipeline, chỉ update GEI counter + `next_expected_index`.
- **Kết quả**: Catch-up 4000+ empty commits giảm từ ~8s xuống ~100ms.
- **File**: `executor.rs` (fast-path return), `mod.rs` (`skip_empty_commit()`)

