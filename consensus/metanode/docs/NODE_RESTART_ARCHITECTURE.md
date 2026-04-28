# Node Restart Architecture — Snapshot & Epoch Recovery

**Cập nhật lần cuối:** 2026-04-25

## Tổng Quan

Tài liệu mô tả kiến trúc khởi động lại node cho cả hai chế độ: **SyncOnly** và **Validator**.
Node có thể restart trong nhiều tình huống: crash, nâng cấp, hoặc phục hồi từ snapshot.

Kiến trúc hiện tại sử dụng **ConsensusCoordinationHub** làm trung tâm quản lý phase, kết hợp với **BlockDeliveryManager** cho unified block delivery và **CommitSyncerSupervisor** cho fault-tolerant DAG synchronization.

---

## 1. SyncOnly Node Restart

### Yêu cầu
- Cơ chế khởi động (Cold-Start) và Catch-up cũ đã bị loại bỏ. Toàn bộ quá trình đồng bộ khởi động hiện được Hợp nhất (Unified) thông qua trung tâm phân phối **`BlockDeliveryManager`**.
- SyncOnly Node sẽ nối tiếp ngay mốc `go_last_block` của Go mà không bị chặn bởi các rào cản nhân tạo (SYNC-FIRST barrier).

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

### 2a. 4-Phase Startup (consensus_node.rs)

Toàn bộ `ConsensusNode::new()` chia thành 4 pha tuần tự, mỗi pha phải hoàn thành trước khi pha tiếp theo bắt đầu:

```
ConsensusNode::new()
│
├─ Phase 1: setup_storage()
│   ├─ ExecutorClient kết nối Go Master (UDS socket)
│   ├─ Retry get_last_block_number() tối đa 30 lần × 500ms
│   │   └─ Đợi Go load xong snapshot/DB trước khi query
│   ├─ get_current_epoch() → go_epoch (LUÔN dùng local Go, KHÔNG peer)
│   ├─ Peer discovery: query peer_rpc_addresses cho peer_last_block
│   ├─ Chọn best_socket (ưu tiên peer có block mới nhất)
│   ├─ calculate_last_global_exec_index()
│   │   └─ So sánh: local Go GEI vs persisted GEI vs peer block
│   │   └─ Luôn return (local_go_gei, last_executed_commit_hash)
│   ├─ Fetch committee qua Go's epoch boundary data
│   ├─ Xác định own_index trong committee (protocol_key match)
│   └─ Return StorageSetup { epoch, gei, committee, eth_addresses, ... }
│
├─ Phase 2: setup_consensus()
│   ├─ CommitConsumerArgs::new(go_replay_after, go_replay_after, commit_hash)
│   │   └─ go_replay_after = (last_gei - epoch_base) hoặc 0
│   ├─ ConsensusCoordinationHub.set_initial_global_exec_index(epoch_base)
│   ├─ Detect empty DAG (snapshot restore) → log warning
│   ├─ CommitProcessor::new() with:
│   │   ├─ with_epoch_info(epoch, epoch_base)  ← epoch_base_index_override
│   │   ├─ with_next_expected_index(next_expected)
│   │   ├─ with_delivery_sender(delivery_tx)
│   │   └─ with_lag_alert_sender(lag_sender)
│   ├─ ══════ STARTUP-SYNC BARRIER ══════
│   │   ├─ Query local Go block
│   │   ├─ Query all peers for max block
│   │   ├─ if local < peer_max:
│   │   │   ├─ fetch_blocks_from_peer(local+1, peer_max)
│   │   │   └─ sync_and_execute_blocks(blocks)  ← TRƯỚC consensus
│   │   └─ else: "Local state in sync"
│   ├─ Spawn BlockDeliveryManager (tokio::spawn)
│   ├─ Spawn LagMonitor handler (tokio::spawn)
│   ├─ Spawn CommitProcessor (tokio::spawn)
│   ├─ Phase → Bootstrapping (chặn propose)
│   └─ ConsensusAuthority::start() hoặc SyncOnly holder
│
├─ Phase 3: setup_networking()
│   └─ ClockSyncManager: NTP sync + drift monitor
│
└─ Phase 4: setup_epoch_management()
    ├─ Init StateTransitionManager
    ├─ Load legacy epoch stores (cross-epoch sync support)
    ├─ Start epoch_transition_handler
    ├─ check_and_update_node_mode()
    ├─ Start sync task (SyncOnly) hoặc skip (Validator)
    ├─ Start unified epoch_monitor
    └─ perform_fork_detection_check()
```

### 2b. Epoch Catch-Up (Khi chậm epoch)

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
│       ├─ Guard: swap_epoch_transitioning(true) + Drop Guard
│       ├─ Discover committee source
│       ├─ Wait commit processor flush (Validator only)
│       ├─ Stop old authority → preserve store in LegacyEpochStoreManager
│       ├─ poll_go_until_synced(30s, ForceCommit mỗi 3s, tolerance ≤5 GEI)
│       ├─ advance_epoch(new_epoch, timestamp, boundary, gei)
│       ├─ Fetch committee → determine role
│       ├─ setup_validator_consensus() OR setup_synconly_sync()
│       ├─ Reset fragment offset
│       └─ set_epoch_transitioning(false) + verify_epoch_consistency()
│
└─ [network_epoch == rust_epoch] → Same-epoch checks:
    ├─ SyncOnly trong committee? → Chờ Go bắt kịp (gap ≤ 3)
    └─ transition_mode_only() → SyncOnly → Validator
```

### 2c. DAG Sync → Consensus Participation

```
ConsensusAuthority::start()
│
├─ CommitConsumerArgs::new(go_replay_after, go_replay_after, last_executed_commit_hash)
│   └─ go_replay_after = last_global_exec_index - epoch_base (VD: 1000)
│
├─ DagState::new() → empty (snapshot đã xóa DAG)
│
├─ CommitObserver::recover_and_send_commits()
│   ├─ Anti-Fork Hash Check:
│   │   ├─ hash == [0;32] → SKIP (sentinel: snapshot restore)
│   │   └─ hash != [0;32] → Compare DAG digest vs Go hash → PANIC if mismatch
│   └─ Store trống + replay_after=1000 → skip replay, log, return
│      (Trước fix: assert_eq!(1000, 0) → PANIC!)
│
├─ Core::recover()
│   ├─ try_commit([]) → nothing to commit
│   └─ try_propose(true) → should_propose()
│       └─ Hub.should_skip_proposal() = true (Bootstrapping) → return false
│          (Trước fix: propose block round 1 → EQUIVOCATION!)
│
├─ CommitSyncerSupervisor (song song, auto-restart):
│   ├─ update_state()
│   │   ├─ local_commit=0, highest_handled=1000
│   │   └─ reset_to_network_baseline(1000)
│   │       ├─ DAG.gc_round = 1000
│   │       └─ synced_commit_index = 1000
│   │
│   ├─ patch_baseline_if_needed()
│   │   └─ Fetch real digest + timestamp from peer for commit #1000
│   │
│   ├─ Phase: Bootstrapping → CatchingUp (khi quorum > 0)
│   │   └─ Fetch commits 1001..quorum_commit từ peers
│   │   └─ 4x batch size, 150ms poll interval
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
┌──────────────┐ ─────────────────────────────────────────────────► ┌─────────┐
│ Bootstrapping │                                                   │ Healthy │
│              │                                                    │         │
│ • No propose │    highest_handled > 0 (SNAPSHOT)                  │ • Propose│
│ • Detect Go  │ ────► fast-forward ────► quorum > 0 ────►         │ • Vote   │
│   state      │         baseline         detected                  │ • Normal │
└──────────────┘                     ┌────────────┐                └─────────┘
                                     │ CatchingUp  │──local>=quorum──►│
                                     │ • No propose│                    
                                     │ • Turbo sync│
                                     └────────────┘
                                          │
                                     lag > 50,000
                                          │
                                     ┌────────────┐
                                     │StateSyncing │
                                     │ • P2P pause │
                                     │ • Wait snap │
                                     └────────────┘
```

### Các điểm kiểm tra phase:

| Component | Check | Hành vi |
|-----------|-------|---------|
| `Core::should_propose()` | `Hub.should_skip_proposal()` | Block proposals chỉ khi Healthy |
| `CommitSyncer` | `is_catching_up()` | 4x batch size, skip throttle, 150ms poll |
| `CommitSyncer` | `is_healthy()` | Normal batch, apply backpressure, 2s standby |
| `CommitSyncer` | `is_state_syncing()` | Pause ALL scheduling |
| `CommitProcessor` | `is_transitioning` | Busy-wait 100ms loop |
| `Core::try_propose` | `is_bootstrapping()` | Return false (no proposals) |

### Phase transition rules (update_state):

| Condition | Result Phase |
|---|---|
| `local_commit == 0 && highest_handled > 0` | Fast-forward baseline, then evaluate |
| `lag > 50,000` | StateSyncing |
| `local_commit < quorum_commit` | CatchingUp |
| `local_commit >= quorum_commit` | Healthy |
| Bootstrapping + Genesis | → Healthy immediately |
| Bootstrapping + Snapshot + quorum > 0 | → Evaluate (CatchingUp or Healthy) |

---

## 4. CommitConsumerArgs Initialization Points

Tất cả các điểm khởi tạo CommitConsumerArgs **phải** truyền Go state + commit hash:

| File | Ngữ cảnh | Giá trị |
|------|----------|---------|
| `consensus_node.rs:991` | Initial startup | `new(go_replay_after, go_replay_after, last_executed_commit_hash)` |
| `consensus_setup.rs:35` | Validator epoch transition | `new(go_replay_after, go_replay_after, commit_hash)` |
| `consensus_setup.rs:159` | SyncOnly epoch transition | `new(go_replay_after_sync, go_replay_after_sync, commit_hash)` |
| `mode_transition.rs:237` | SyncOnly→Validator promotion | `new(go_replay_after, go_replay_after, commit_hash)` |

Công thức:
```rust
let go_replay_after = if executor_read_enabled && last_global_exec_index > epoch_base_exec_index {
    (last_global_exec_index - epoch_base_exec_index) as u32
} else {
    0  // Genesis hoặc không có executor
};
```

> [!IMPORTANT]
> `last_executed_commit_hash` (tham số thứ 3) là **bắt buộc** cho Anti-Fork Hash Check. Go lưu commit hash qua `pushAsyncGEIUpdate()` và trả lại qua `LastBlockNumberResponse`. Hash = [0;32] là sentinel value hợp lệ (snapshot restore/uninitialized).

---

## 5. CommitProcessor Startup Initialization

CommitProcessor nhận nhiều tham số khởi tạo quan trọng cho fork safety:

```rust
CommitProcessor::new(commit_receiver)
    .with_delivery_sender(delivery_tx)          // → BlockDeliveryManager
    .with_epoch_info(epoch, epoch_base_index)   // epoch_base_index_override (CRITICAL)
    .with_next_expected_index(next_expected)     // = (last_gei - epoch_base + 1)
    .with_shared_last_global_exec_index(gei)    // shared GEI counter
    .with_is_transitioning(is_transitioning)    // epoch transition lock
    .with_epoch_eth_addresses(cache)            // leader address lookup
    .with_lag_alert_sender(lag_sender)          // → LagMonitor
    .with_storage_path(path)                    // fragment offset persistence
    .with_tx_recycler(recycler)                 // TX confirmation tracking
```

### Điểm quan trọng:

| Tham số | Why Critical | Failure Mode |
|---------|-------------|-------------|
| `epoch_base_index_override` | GEI = base + commit_index + offset | Wrong base → wrong GEI → hash diverge → FORK |
| `next_expected_index` | Skip/jump detection | Default 1 → AUTO-JUMP on first commit → GEI miscalculation |
| `delivery_sender` | Route to Go via BlockDeliveryManager | None → early panic "delivery_sender is None" |
| `is_transitioning` | Pause during epoch transition | Missing → race condition with Go re-initialization |

---

## 6. Lưu Ý Quan Trọng

### 6a. GEI (Global Execution Index) vs Commit Index
- **GEI**: Đếm tuần tự TẤT CẢ commits (bao gồm empty) + fragmentation offset, dùng trong Go execution.
- **Commit Index**: Epoch-local, reset về 1 mỗi epoch mới.
- **Formula**: `GEI = epoch_base_index + commit_index + cumulative_fragment_offset`
- **epoch_base_index**: Cố định suốt epoch, lấy từ Go epoch boundary data. Lưu qua `with_epoch_info()` — KHÔNG derive runtime từ shared_last_global_exec_index.

### 6b. Khôi Phục Snapshot & Nhảy Xa (FORWARD-JUMP Batch Drain)
- Snapshot xóa toàn bộ DAG storage nhưng giữ Go state trong BackupDB.
- Khoảnh khắc khởi động: DAG trống (`last_commit_index = 0`) nhưng Go đã neo ở Block cao (`last_global_exec_index = N`).
- Khi P2P trả về các Commit mới (Vd: Commit số `N+5000`), xuất hiện khoảng trống gap 5000 commits.
- Cơ chế **FORWARD-JUMP Batch Drain** tự động phát hiện gap (≥10 pending, gap >20), nhảy vọt tới vị trí commit đang chờ (pending), batch-skip empty commits, giúp node tiếp tục mà không deadlock.

### 6c. Giao Hàng Tập Trung (BlockDeliveryManager)
Kiến trúc hoàn toàn loại bỏ `cold_start`, `SYNC-FIRST` barrier (cũ), và `CatchupManager`. Tất cả các luồng phân phối block hội tụ duy nhất về `BlockDeliveryManager`:
- Go Master lấy dữ liệu từ một đường ống duy nhất (MPSC channel buffer 10,000).
- Validator và SyncOnly dùng chung mã cốt lõi, dễ dàng thăng cấp qua lại.
- **STARTUP-SYNC barrier** (mới) chạy trước consensus để đảm bảo Go state parity.

### 6d. Equivocation Prevention
- **Equivocation**: Node propose 2 block khác nhau cùng round → bị slash.
- Fix: `ConsensusCoordinationHub.should_skip_proposal()` = true cho mọi phase trừ `Healthy`.
- Bootstrapping → chờ DAG baseline → CatchingUp → bắt kịp quorum → Healthy → propose.

### 6e. Empty Commit Fast-Skip (Catch-up Optimization)
- Khi catching up, 90%+ DAG commits là **empty** (không có giao dịch).
- **Trước**: Mỗi empty commit đi qua full pipeline: Leader resolution → Protobuf → FFI → Go.
- **Sau**: Empty commits skip toàn bộ pipeline, chỉ update GEI counter + `next_expected_index`.
- **Kết quả**: Catch-up 4000+ empty commits giảm từ ~8s xuống ~100ms.
- **File**: `executor.rs` (fast-path return), `skip_empty_commit()`

### 6f. Fragment Offset Recovery
- Commits lớn (>12K TXs) bị fragment thành N blocks, mỗi block tiêu thụ 1 GEI → offset += (N-1).
- Offset được persist to disk sau mỗi thay đổi (`persist_fragment_offset()`).
- Startup recovery: load from disk → nếu 0, tính math từ `last_gei - epoch_base - (next_expected - 1)`.
- Epoch transition: reset offset về 0 (`reset_fragment_offset()`).

### 6g. CommitSyncerSupervisor Auto-Restart
- CommitSyncer được bọc trong `CommitSyncerSupervisor` thay vì chạy trực tiếp.
- Nếu CommitSyncer panic hoặc exit bất thường → Supervisor tự restart sau backoff:
  - 1s → 2s → 4s → 8s → 10s (cap)
- Supervisor chỉ dừng khi nhận shutdown signal từ ConsensusAuthority.

### 6h. LagMonitor & P2P Recovery
- `LagMonitor` chạy song song, so sánh Rust GEI vs Go GEI/block định kỳ.
- **ModerateLag**: Log warning, tiếp tục giám sát.
- **SevereLag**: Tự động fetch missing blocks từ P2P peers → execute trong Go.
  - `fetch_blocks_from_peer(go_block+1, go_block+gap)`
  - `sync_and_execute_blocks(blocks)`
- **Recovered**: Log "Go has caught up", normal operations resumed.

---

## 7. STARTUP-SYNC Barrier vs SYNC-FIRST Barrier (Removed)

| Aspect | SYNC-FIRST (ĐÃ XÓA) | STARTUP-SYNC (HIỆN TẠI) |
|--------|----------------------|--------------------------|
| Vị trí | Sau CommitProcessor init | Trước ConsensusAuthority start |
| Cơ chế | CatchupManager + 2s sleep | P2P block fetch + execute |
| Mục đích | DAG catch-up | Go state parity |
| Redundancy | Trùng với CommitSyncer | Unique — CommitSyncer chỉ sync DAG, không sync Go state |
| Kết quả | Thêm ~2s delay, ~70 lines duplicate | Đảm bảo Go state matching network trước consensus |

> [!NOTE]
> STARTUP-SYNC barrier (`consensus_node.rs:1153-1241`) query peers cho latest block, fetch missing blocks, và execute chúng trong Go TRƯỚC KHI consensus bắt đầu. Điều này khác với CommitSyncer (sync DAG) — STARTUP-SYNC sync Go execution state.
