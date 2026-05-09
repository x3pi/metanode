# Luồng Hạ Cấp Node: Validator → SyncOnly

> **Mục đích**: Tài liệu này mô tả chi tiết toàn bộ quá trình một Consensus Node bị hạ cấp từ vai trò **Validator** (tham gia đồng thuận DAG-BFT) xuống vai trò **SyncOnly** (chỉ đồng bộ block từ các peer), bao gồm các bước kỹ thuật, file mã nguồn liên quan, và các lỗi đã được sửa.

---

## 1. Bức Tranh Tổng Thể

### Khi nào xảy ra hạ cấp?

Một node bị hạ cấp xuống SyncOnly khi **node đó không còn nằm trong danh sách validator (committee) của epoch mới**. Ví dụ:
- Node 0 là Validator trong Epoch 0.
- Đến cuối Epoch 0, mạng bỏ phiếu và chọn ra committee cho Epoch 1.
- Node 0 không được chọn → Node 0 phải hạ cấp xuống SyncOnly.

### Hai vai trò hoạt động khác nhau như thế nào?

| Tiêu chí | Validator | SyncOnly |
|---|---|---|
| **Nhiệm vụ** | Tham gia bỏ phiếu DAG-BFT, tạo block | Lấy block đã được xác nhận từ peer |
| **Cơ sở hạ tầng** | `ConsensusAuthority` (engine BFT) | `RustSyncNode` (P2P download engine) |
| **DB** | `epochs/epoch_N/consensus_db` (write) | `epochs/epoch_N/consensus_db` (write, riêng biệt) |
| **Gửi block sang Go** | Qua `CommitProcessor` → FFI | Qua `sync_blocks` → FFI |
| **Epoch monitor** | Chạy như backup để phát hiện epoch mới | Chạy là **chính** để mở cổng epoch boundary |

---

## 2. Sơ Đồ Luồng Chi Tiết

```
Mạng lưới phát tín hiệu EndOfEpoch
            │
            ▼
┌─────────────────────────────────────────────────────────────────┐
│  epoch_transition.rs::transition_to_epoch_from_system_tx()     │
│  (Đây là điểm vào chính cho mọi quá trình chuyển epoch)        │
└───────────────────────────┬─────────────────────────────────────┘
                            │
          ┌─────────────────┼─────────────────────┐
          │                 │                     │
       CASE 1            CASE 2                CASE 3
    SyncOnly→Validator  Đã là Validator     Chuyển Epoch đầy đủ
    (bỏ qua tài liệu này)  → Skip             (xử lý tại đây)
                                                  │
                            ┌─────────────────────┘
                            │
              ┌─────────────▼─────────────┐
              │   STEP 1: Guard Check     │
              │   - swap_epoch_transitioning(true)
              │   - Prevent concurrent transitions
              └─────────────┬─────────────┘
                            │
              ┌─────────────▼─────────────┐
              │   STEP 2: Discover Source │
              │   - CommitteeSource::discover()
              │   - Lấy thông tin epoch từ local Go hoặc peer
              └─────────────┬─────────────┘
                            │
              ┌─────────────▼─────────────────────┐
              │   STEP 4: stop_authority_and_poll  │
              │   - Flush buffer → Go (pre-stop)   │
              │   - authority.take() + take_store()│
              │   - legacy_store_manager.add_store │ ← GIỮ KHÓA DB CŨ!
              │   - auth.stop().await              │
              │   - Flush buffer → Go (post-stop)  │
              │   - poll_go_until_synced() ≤ 30s   │
              └─────────────┬─────────────────────┘
                            │
              ┌─────────────▼─────────────┐
              │   STEP 5: State Update    │
              │   - node.current_epoch = N+1
              │   - node.last_global_exec_index = synced
              │   - Reset commit_index = 0
              │   - Memory cleanup
              └─────────────┬─────────────┘
                            │
              ┌─────────────▼─────────────┐
              │   STEP 6: Advance Go Epoch│
              │   - executor_client.advance_epoch(N+1, ts, block, gei)
              │   - Go Master nhảy sang Epoch mới
              └─────────────┬─────────────┘
                            │
              ┌─────────────▼─────────────┐
              │   STEP 7: Determine Role  │
              │   - determine_role_and_check_transition()
              │   - Fetch committee cho epoch mới
              │   - Kiểm tra node có trong committee không?
              │   ┌──────────────────────────────┐
              │   │ Có trong committee → Validator│
              │   │ Không có → SyncOnly           │ ← Trường hợp này!
              │   └──────────────────────────────┘
              └─────────────┬─────────────┘
                            │
              ┌─────────────▼─────────────────────┐
              │   check_and_update_node_mode()     │
              │   (node_methods.rs: dòng 96-303)   │
              │                                    │
              │   Validator → SyncOnly branch:     │
              │   1. Stop authority (again, safety)│
              │   2. node.node_mode = SyncOnly     │
              │   3. Notify Go: set_sync_start_block│
              │   4. start_sync_task()             │
              │   5. stop_epoch_monitor() (old)    │
              │   6. start_unified_epoch_monitor() │
              └─────────────┬─────────────────────┘
                            │
              ┌─────────────▼─────────────────────┐
              │   setup_synconly_sync()            │
              │   (consensus_setup.rs: dòng 163+)  │
              └─────────────────────────────────────┘
```

---

## 3. Chi Tiết `setup_synconly_sync()` — Trái Tim Của Hạ Cấp

File: `src/node/transition/consensus_setup.rs` (dòng 163–331)

### 3.1 Tạo CommitProcessor (Giám sát EndOfEpoch)

```rust
let (_commit_consumer, commit_receiver, mut block_receiver) =
    CommitConsumerArgs::new(go_replay_after_sync, ...);

let mut processor = CommitProcessor::new(commit_receiver)
    .with_epoch_transition_callback(epoch_cb)  // Callback khi nhận EndOfEpoch
    ...;

tokio::spawn(async move { processor.run().await });
```

> **Mục đích**: Mặc dù SyncOnly không tham gia đồng thuận, nó vẫn cần một `CommitProcessor` nhỏ để lắng nghe tín hiệu `EndOfEpoch` khi network chuyển sang epoch tiếp theo.

### 3.2 Tạo ExecutorClient Mới Cho Sync

```rust
let rust_sync_executor = Arc::new(ExecutorClient::new(
    true,  // can_commit = true — có quyền gửi block sang Go Master
    true,
    config.executor_send_socket_path.clone(),
    ...
));
rust_sync_executor.initialize_from_go().await; // Đồng bộ trạng thái với Go
```

### 3.3 Tính Toán `db_path` Đúng Cho Epoch Mới (Bản Sửa Lỗi)

```rust
// consensus_setup.rs dòng 288-293
let mut parameters = node.parameters.clone();
parameters.db_path = node
    .storage_path
    .join("epochs")
    .join(format!("epoch_{}", new_epoch))  // epoch_1, epoch_2, ...
    .join("consensus_db");
```

> **Tại sao cần điều này?** Xem chi tiết tại [Section 5](#5-lỗi-gốc-rocksdb-deadlock-và-cách-sửa).

### 3.4 Tạo Network Context Và Khởi Động RustSyncNode

```rust
let sync_context = Arc::new(consensus_core::Context::new(
    epoch_timestamp,
    AuthorityIndex::new_for_test(0),  // Dummy index, SyncOnly không có authority
    committee.clone(),                 // Committee của epoch MỚI
    parameters,                        // Đã có db_path đúng
    ...
));

// Spawn P2P sync engine
start_rust_sync_task_with_network(rust_sync_executor, ..., sync_context, ...).await
// → RocksDBStore::new(epoch_1) → RustSyncNode → sync_loop (background task)
```

---

## 4. Sau Khi Hạ Cấp: Hai Tiến Trình Song Song

### 4.1 `sync_loop` — Máy Bơm Block (RustSyncNode)

**File**: `src/node/rust_sync_node/sync_loop.rs`

```
Vòng lặp vô tận (mỗi 50ms khi turbo mode):
  1. Hỏi Go: go_last_gei, go_epoch
  2. Hỏi peer: network_epoch (để bật Turbo Mode nếu bị lag)
  3. Turbo Mode ON nếu: epoch_behind || network_behind || mới start
  4. PHASE 1: Sync queue với Go (đánh dấu block nào đã được nhận)
  5. PHASE 2: Lấy danh sách peer → gọi /get_blocks HTTP API
  6. PHASE 3: Gửi từng block vào Go Master qua FFI (sync_blocks)
  7. Sleep 50ms, lặp lại
```

**Giới hạn**: `sync_loop` không thể tự mình thực hiện chuyển epoch. Nó sẽ bị kẹt ở ranh giới epoch (epoch boundary) vì:
- Ranh giới epoch có "empty commit" đặc biệt, không có trong queue block thường.
- Go cần nhận lệnh `advance_epoch()` mới chịu nhảy sang epoch mới.

### 4.2 `epoch_monitor` — Người Mở Cổng Epoch

**File**: `src/node/epoch_monitor.rs`

```
Vòng lặp (mỗi 10-30 giây):
  1. Hỏi Go: local_go_epoch
  2. Hỏi mạng (CommitteeSource): network_epoch
  3. Nếu SyncOnly VÀ local_go_epoch < network_epoch:
     → bypass gate condition! (dòng 149-151)
     → Fetch empty commits từ peer (GEI boundary)
     → Gọi advance_epoch() để ép Go nhảy epoch
  4. Sau khi Go nhảy epoch:
     → sync_loop phát hiện go_epoch tăng → auto_epoch_sync()
     → sync_loop tiếp tục kéo block của epoch mới
```

### 4.3 Sơ Đồ Phối Hợp

```
SyncOnly Node Timeline:

Epoch 0:         [kéo block 0→226]
                                    ↓ Kẹt ở ranh giới
epoch_monitor:                       → fetch empty commits → advance_epoch(1)
Epoch 1: [kéo block 227→450]
                                    ↓ Kẹt ở ranh giới
epoch_monitor:                       → fetch empty commits → advance_epoch(2)
Epoch 2: [kéo block 451→...]
        ↑──────────────────────────── sync_loop hoạt động liên tục
```

---

## 5. Lỗi Gốc: RocksDB Deadlock Và Cách Sửa

### 5.1 Hiểu RocksDB File Lock

RocksDB sử dụng cơ chế **exclusive file lock** (khóa file `LOCK`) trong thư mục database. Chỉ một tiến trình được mở mỗi thư mục DB tại một thời điểm.

### 5.2 Vòng Đời Database Qua Epoch Transition

```
Epoch 0:
  ConsensusAuthority ─── mở và sử dụng ──► epoch_0/consensus_db
                                                    │
Khi chuyển epoch, authority bị stop:               │
  legacy_store_manager.add_store(epoch_0, store)   │
  ─── vẫn GIỮ KHÓA FILE ──────────────────────────► epoch_0/consensus_db ← LOCKED!
  (Giữ để phục vụ legacy block read cho node đi sau)
```

### 5.3 Tại Sao Validator Không Bị Lỗi Này?

Bên **Validator**: `setup_validator_consensus()` nhận `db_path` mới qua tham số từ caller và dùng đúng:
```rust
// epoch_transition.rs dòng 273-277: tạo db_path mới
let db_path = node.storage_path.join("epochs").join(format!("epoch_{}", new_epoch)).join("consensus_db");

// consensus_setup.rs dòng 119-120: gán đúng cho params
let mut params = node.parameters.clone();
params.db_path = db_path;  // db_path = "epoch_1/consensus_db" — không bị khóa
```

Bên **SyncOnly**: code cũ **không nhận `db_path` mới** mà copy thẳng `node.parameters`:
```rust
// Code CŨ (bị lỗi):
let sync_context = Arc::new(Context::new(
    ...
    node.parameters.clone(),  // db_path vẫn là "epoch_0/consensus_db"!
    ...
));
```

### 5.4 Chuỗi Deadlock

```
1. setup_synconly_sync() tạo sync_context với db_path = "epoch_0/consensus_db"
2. start_rust_sync_task_with_network(sync_context)
3. → start.rs dòng 97:
       let store = Arc::new(RocksDBStore::new("epoch_0/consensus_db"));
4. RocksDB C++ cố gắng lấy file LOCK tại epoch_0/consensus_db
5. File LOCK đang bị legacy_store_manager giữ!
6. Thread bị BLOCK (đứng đợi vô thời hạn)
7. sync_node.start() KHÔNG BAO GIỜ được gọi
8. Vòng lặp sync_loop KHÔNG BAO GIỜ được spawn
9. Node 0 không kéo được block nào → KẸT vĩnh viễn!
```

### 5.5 Cách Sửa

```rust
// Code MỚI (đã sửa) — consensus_setup.rs dòng 288-293:
let mut parameters = node.parameters.clone();
parameters.db_path = node
    .storage_path
    .join("epochs")
    .join(format!("epoch_{}", new_epoch))  // Tạo đường dẫn MỚI cho epoch hiện tại
    .join("consensus_db");
// → "epoch_1/consensus_db" — thư mục trống, không bị ai khóa
```

---

## 6. Quản Lý Vòng Đời `epoch_monitor`

### Vấn Đề: Zombie Tasks

Nếu `epoch_monitor` được khởi động nhiều lần mà không dừng cái cũ, sẽ có nhiều instance cùng chạy, cùng gọi `advance_epoch()`, gây **double-transition** và `is_transitioning` deadlock.

### Giải Pháp: Stop-Before-Start Pattern

```rust
// node_methods.rs — Validator → SyncOnly (dòng 284)
epoch_monitor::stop_epoch_monitor(self.epoch_monitor_handle.take()).await;
if let Ok(Some(handle)) = epoch_monitor::start_unified_epoch_monitor(...) {
    self.epoch_monitor_handle = Some(handle);
}

// node_methods.rs — SyncOnly → Validator (dòng 215)
epoch_monitor::stop_epoch_monitor(self.epoch_monitor_handle.take()).await;
if let Ok(Some(handle)) = epoch_monitor::start_unified_epoch_monitor(...) {
    self.epoch_monitor_handle = Some(handle);
}

// demotion.rs — demote_to_synconly_and_catchup (dòng 128)
crate::node::epoch_monitor::stop_epoch_monitor(node.epoch_monitor_handle.take()).await;
if let Ok(Some(handle)) = start_unified_epoch_monitor(...) {
    node.epoch_monitor_handle = Some(handle);
}
```

### Hàm `stop_epoch_monitor`

```rust
pub async fn stop_epoch_monitor(handle: Option<JoinHandle<()>>) {
    if let Some(h) = handle {
        h.abort();        // Gửi tín hiệu cancel đến tokio task
        let _ = h.await;  // Chờ task thực sự dừng
    }
}
```

---

## 7. Sequence Diagram Toàn Bộ

```
Rust Node                            Go Master              Peer Nodes
    │                                    │                      │
    │ [EndOfEpoch commit received]        │                      │
    │                                    │                      │
    ├─ stop_authority()                   │                      │
    ├─ flush_buffer() ───────────────────►│                      │
    ├─ legacy_store_manager.add(db_0)    │                      │
    │     (Giữ LOCK epoch_0/consensus_db)│                      │
    ├─ poll_go_until_synced() ◄──────────│ (Go xác nhận GEI)    │
    │                                    │                      │
    ├─ node.current_epoch = 1            │                      │
    ├─ advance_epoch(1, ts, ...) ────────►│                      │
    │                                    │ [Go nhảy epoch 1]    │
    │                                    │                      │
    ├─ determine_role() → SyncOnly        │                      │
    │                                    │                      │
    ├─ setup_synconly_sync(epoch=1)       │                      │
    │   ├─ Tạo CommitProcessor (backup)  │                      │
    │   ├─ Tạo ExecutorClient            │                      │
    │   ├─ db_path = epoch_1/consensus_db (không bị khóa!)      │
    │   ├─ Tạo Context với db_path mới   │                      │
    │   └─ start_rust_sync_task()        │                      │
    │         ├─ RocksDBStore::new(epoch_1) // Thành công!      │
    │         └─ spawn(sync_loop)        │                      │
    │               │                   │                      │
    │               ├─[mỗi 50ms]        │                      │
    │               ├─ get_last_gei ────►│                      │
    │               ├─ /get_blocks ───────────────────────────────►
    │               ◄────────────────────────────── [blocks] ──┤
    │               ├─ sync_blocks() ───►│                      │
    │               │                   │ [Go xử lý block]     │
    │               │                   │                      │
    │   ┌── [Kẹt ở epoch boundary] ───────────────────────────────
    │   │                               │                      │
    ├─ epoch_monitor (mỗi 30s)          │                      │
    │   ├─ get_current_epoch ───────────►│ → trả về epoch=1     │
    │   ├─ network_epoch ────────────────────────────────────────►
    │   ◄─────────────────────────────────── trả về epoch=2 ───┤
    │   ├─ SyncOnly VÀ 1 < 2 → bypass gate!                    │
    │   ├─ fetch empty commits ──────────────────────────────────►
    │   ◄──────────────────────────────── [empty commits] ─────┤
    │   ├─ advance_epoch(2, ...) ────────►│                      │
    │   │                                │ [Go nhảy epoch 2]   │
    │   │                                │                      │
    │   └─ sync_loop: auto_epoch_sync() → tiếp tục epoch 2 ─────►
```

---

## 8. File Mã Nguồn Liên Quan

| File | Vai Trò |
|---|---|
| `src/node/transition/epoch_transition.rs` | Entry point chính, orchestrate toàn bộ 9 bước |
| `src/node/transition/consensus_setup.rs` | `setup_synconly_sync()` — khởi tạo hạ tầng SyncOnly |
| `src/node/transition/demotion.rs` | `demote_to_synconly_and_catchup()` — hạ cấp cross-epoch |
| `src/node/node_methods.rs` | `check_and_update_node_mode()` — điều phối chuyển mode |
| `src/node/sync.rs` | `start_sync_task()` / `stop_sync_task()` |
| `src/node/rust_sync_node/start.rs` | Khởi tạo `RustSyncNode`, mở RocksDB (nơi deadlock xảy ra) |
| `src/node/rust_sync_node/sync_loop.rs` | Vòng lặp kéo block từ peer |
| `src/node/epoch_monitor.rs` | Monitor phát hiện epoch mới, mở cổng boundary |

---

## 9. Các Lỗi Đã Được Sửa

| # | Lỗi | Triệu Chứng | File Sửa | Cách Sửa |
|---|---|---|---|---|
| 1 | **RocksDB Deadlock** | Node kẹt tại block 226, không log gì | `consensus_setup.rs:288-293` | Tính `db_path` mới theo `new_epoch` thay vì copy tham số mặc định |
| 2 | **SyncOnly gate bị chặn** | `epoch_monitor` không fetch empty commits | `epoch_monitor.rs:149-151` | Thêm `synconly_go_behind` bypass condition |
| 3 | **Zombie epoch_monitor** | Double transition, deadlock `is_transitioning` | `node_methods.rs:215,284`, `demotion.rs:128` | Gọi `stop_epoch_monitor()` trước mỗi lần start mới |
