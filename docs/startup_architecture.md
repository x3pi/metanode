# Metanode Startup Architecture

> Kiến trúc khởi động tuần tự, không over-engineering, đảm bảo chính xác dữ liệu.

---

## 1. Tổng Quan Kiến Trúc

Metanode gồm 2 engine chạy trong cùng 1 process:

| Engine | Ngôn ngữ | Vai trò | Source of Truth |
|--------|---------|---------|-----------------|
| **Consensus** | Rust | DAG, Leader Election, GEI, Epoch | GEI, CommitIndex, Leader, Timestamp |
| **Execution** | Go | State Trie (NOMT), Block Storage, EVM | Block Number, StateRoot, AccountState |

Giao tiếp qua **FFI Bridge** (CGo) và **Unix Domain Socket** (IPC).

### Nguyên Tắc Cốt Lõi

```
Rust KHÔNG ĐƯỢC tự suy đoán trạng thái Go.
Go KHÔNG ĐƯỢC tự suy đoán trạng thái Rust.
Mỗi engine hỏi trực tiếp engine kia khi cần dữ liệu.
```

---

## 2. Chuỗi Khởi Động 4 Pha

Khởi động diễn ra **tuần tự nghiêm ngặt**. Mỗi pha phải hoàn thành trước khi pha sau bắt đầu.

```
┌──────────────────────────────────────────────────────────┐
│  PHASE 1: setup_storage()                                │
│  ├─ Khởi tạo ExecutorClient (IPC tới Go)                 │
│  ├─ Đợi Go ready (is_ready=true, max 30 retry)           │
│  ├─ Đọc: block_number, epoch, committee từ Go            │
│  ├─ Đọc: last_handled_commit_index từ Go                 │
│  └─ Xác định identity (own_index trong committee)        │
├──────────────────────────────────────────────────────────┤
│  PHASE 2: setup_consensus()                              │
│  ├─ Tính go_replay_after từ commit_index                 │
│  ├─ Tạo CommitProcessor + CommitConsumer                 │
│  ├─ STARTUP-SYNC: Bắt kịp mạng lưới                     │
│  │   ├─ So sánh local_block vs peer_block                │
│  │   ├─ Fetch blocks từ peers (nếu behind)               │
│  │   ├─ Gửi blocks tới Go qua FFI                        │
│  │   └─ Cập nhật commit_index sau sync                   │
│  ├─ initialize_from_go() — ĐỒNG BỘ (không async)        │
│  ├─ GEI Cross-check với peers                            │
│  ├─ Health Check: block_parity + gei_parity              │
│  └─ Khởi động Authority (Mysticeti consensus core)       │
├──────────────────────────────────────────────────────────┤
│  PHASE 3: setup_networking()                             │
│  └─ Clock/NTP sync                                       │
├──────────────────────────────────────────────────────────┤
│  PHASE 4: setup_epoch_management()                       │
│  ├─ StateTransitionManager                               │
│  ├─ EpochMonitor                                         │
│  └─ SyncController                                       │
└──────────────────────────────────────────────────────────┘
```

---

## 3. Chi Tiết Từng Pha

### 3.1 Phase 1: setup_storage() — Đọc Trạng Thái Go

**Mục đích**: Thu thập dữ liệu nền (block, epoch, committee) từ Go làm baseline.

**Luồng**:
```
Rust                              Go
  │                                │
  ├─ get_last_block_number() ─────►│ Đợi is_ready=true
  │◄───── (block=893, epoch=1) ────┤
  │                                │
  ├─ get_current_epoch() ─────────►│
  │◄───── (epoch=1) ──────────────┤
  │                                │
  ├─ get_epoch_boundary_data() ───►│ Committee + boundary
  │◄───── (validators, GEI) ──────┤
  │                                │
  ├─ get_last_handled_commit() ───►│ Commit tracking
  │◄───── (commit_idx, GEI) ──────┤
```

**Gate**: Go phải trả về `is_ready=true` trước khi Rust tin tưởng giá trị block_number. Điều này đảm bảo Go đã load xong toàn bộ DB (bao gồm snapshot data).

**Không làm**:
- ❌ Không hỏi peers để lấy epoch → gây deadlock nếu Go chưa sẵn sàng
- ❌ Không dùng `max()` để inflate GEI → gây fork

### 3.2 Phase 2: setup_consensus() — Sync + Khởi Động Consensus

**Mục đích**: Bắt kịp mạng, đồng bộ trạng thái, khởi động consensus core.

#### 3.2.1 STARTUP-SYNC

Đây là bước quan trọng nhất để tránh fork sau snapshot restore.

```
Rust                    Peers                   Go
  │                       │                      │
  ├─ query_peer_info() ──►│                      │
  │◄── (peer_block=894) ──┤                      │
  │                       │                      │
  │  local=893 < peer=894 → CẦN SYNC            │
  │                       │                      │
  ├─ fetch_blocks(894) ──►│                      │
  │◄── [Block 894 data] ──┤                      │
  │                       │                      │
  ├─ FFI: send_block(894) ──────────────────────►│
  │                       │                      ├─ Execute TXs
  │                       │                      ├─ Update State
  │◄──────────── (done) ─────────────────────────┤
  │                       │                      │
  │  Lặp lại cho đến khi local >= peer           │
```

**Gate**: STARTUP-SYNC phải hoàn thành **TRƯỚC KHI** Authority bắt đầu produce commits.

#### 3.2.2 initialize_from_go() — Đồng Bộ Cuối Cùng

```rust
// PHẢI chạy ĐỒNG BỘ, không được spawn async
executor_client_for_proc.initialize_from_go().await;
```

Hàm này:
1. Đọc `last_block_number` từ Go → set `next_block_number = last_block + 1`
2. Đọc `last_gei` từ Go → set `next_expected_index = last_gei + 1`
3. Đọc `last_handled_commit_index` → cập nhật replay guard

**Tại sao đồng bộ**: Nếu chạy async, CommitProcessor có thể bắt đầu xử lý commits **trước** khi guards được cập nhật → tạo block trùng lặp → fork.

#### 3.2.3 GEI Cross-Check

```
Rust                    Peers
  │                       │
  ├─ local GEI = 898      │
  ├─ query_peer_gei() ───►│
  │◄── peer GEI = 898 ────┤
  │                       │
  │  GEI khớp → ✅ PASS    │
```

Cảnh báo nhưng không chặn nếu GEI lệch (peer có thể tạm thời ahead).

#### 3.2.4 Health Check

```rust
HealthCheckResult {
    block_parity: true,   // Block number khớp peers
    gei_parity: true,     // GEI khớp peers
    state_root_match: true, // StateRoot khớp peers
    committee_match: true,  // Committee khớp peers
}
```

Log kết quả nhưng không chặn khởi động (non-blocking diagnostic).

### 3.3 Phase 3: setup_networking()

Clock/NTP sync — đơn giản, không ảnh hưởng consensus.

### 3.4 Phase 4: setup_epoch_management()

Khởi tạo các module quản lý epoch transition, mode switching (Validator ↔ SyncOnly).

---

## 4. Luồng Dữ Liệu Sau Khởi Động

Sau khi khởi động xong, dữ liệu chảy một chiều:

```
┌─────────┐  commits   ┌─────────────┐  ExecutableBlock  ┌──────────┐
│  Rust   │ ──────────►│  Commit     │ ─────────────────►│   Go     │
│  DAG    │            │  Processor  │   (FFI Bridge)    │  EVM     │
└─────────┘            └─────────────┘                   └──────────┘
                                                              │
                              ┌────────────────────────────────┘
                              │ Block created + committed
                              ▼
                        ┌──────────┐
                        │ PebbleDB │  (Persistent storage)
                        │ + NOMT   │
                        └──────────┘
```

Mỗi `ExecutableBlock` từ Rust chứa **tất cả** metadata cần thiết:

| Trường | Nguồn | Deterministic? |
|--------|-------|:--------------:|
| `transactions` | DAG commit | ✅ |
| `global_exec_index` | Rust CommitProcessor | ✅ |
| `commit_index` | Rust DAG | ✅ |
| `epoch` | Rust epoch tracking | ✅ |
| `commit_timestamp_ms` | Median stake-weighted | ✅ |
| `leader_author_index` | DAG leader election | ✅ |
| `leader_address` | 20-byte ETH address | ✅ |
| `block_number` | Rust sequential counter | ✅ |
| `commit_hash` | DAG commit digest | ✅ |

Go **không tự tính** bất kỳ giá trị nào từ bảng trên. Go chỉ:
1. Giải mã transactions
2. Thực thi EVM → tính StateRoot
3. Tạo block với metadata từ Rust + StateRoot từ EVM

---

## 5. Block Hash — Trường Tham Gia

Block hash được tính từ **9 trường**, tất cả đều deterministic:

```
Hash = Keccak256(Proto(
    BlockNumber,        ← Rust
    AccountStatesRoot,  ← Go EVM execution
    StakeStatesRoot,    ← Go EVM execution
    ReceiptRoot,        ← Go receipt trie
    LeaderAddress,      ← Rust (20-byte direct)
    TimeStamp,          ← Rust (commit_timestamp_ms / 1000)
    TransactionsRoot,   ← Go tx trie
    Epoch,              ← Rust
    GlobalExecIndex,    ← Rust
))
```

**Loại trừ** (không tham gia hash):
- `LastBlockHash` — cho phép sync linh hoạt
- `AggregateSignature` — BLS signature
- `CommitIndex` — Rust internal tracking

---

## 6. Snapshot Recovery — Quy Trình Phục Hồi

```
Bước 1: Dừng node
       │
       ▼
Bước 2: Restore snapshot (LVM/Btrfs → PebbleDB + NOMT)
       │
       ▼
Bước 3: Xóa DAG (consensus_db) → Rust bắt đầu fresh
       │
       ▼
Bước 4: Khởi động lại
       │
       ▼
Bước 5: Phase 1 — Go load snapshot data
       │  Go reports: block=893, epoch=1
       ▼
Bước 6: Phase 2 — STARTUP-SYNC
       │  Rust phát hiện: local=893 < peer=900
       │  Fetch blocks 894..900 từ peers
       │  Gửi từng block tới Go qua FFI
       │  Go thực thi → cập nhật state
       ▼
Bước 7: initialize_from_go()
       │  next_block = Go.last_block + 1
       │  next_gei = Go.last_gei + 1
       ▼
Bước 8: Authority starts
       │  Consensus core bắt đầu produce commits
       │  Commits mới tiếp tục từ đúng điểm mạng
```

### Điều Kiện Gây Fork (Phải Tránh)

| Điều kiện | Hậu quả | Cách tránh |
|-----------|---------|------------|
| Consensus produce commits TRƯỚC khi sync xong | Block với metadata sai (leader, timestamp, GEI) | STARTUP-SYNC gate |
| `initialize_from_go()` chạy async | CommitProcessor bypass replay guard | Chạy đồng bộ |
| Dùng `max()` để inflate GEI | GEI mapping sai → block number lệch | Đọc trực tiếp từ Go |
| Go import P2P blocks song song với consensus | Trie state bị overwrite bởi foreign data | Disabled trên Master |

---

## 7. Module Dependencies

```
                    ┌─────────────────────┐
                    │   ConsensusNode     │
                    │   (orchestrator)    │
                    └─────────┬───────────┘
                              │
              ┌───────────────┼───────────────┐
              │               │               │
    ┌─────────▼──────┐  ┌────▼─────┐  ┌──────▼──────────┐
    │ ExecutorClient │  │ Commit   │  │ Authority       │
    │ (IPC to Go)    │  │ Processor│  │ (Mysticeti DAG) │
    └─────────┬──────┘  └────┬─────┘  └──────┬──────────┘
              │              │               │
              │         ┌────▼─────┐         │
              │         │ Block    │         │
              │         │ Sending  │         │
              │         └────┬─────┘         │
              │              │               │
    ┌─────────▼──────────────▼───────────────▼──┐
    │              FFI Bridge (CGo)              │
    └─────────────────────┬─────────────────────┘
                          │
    ┌─────────────────────▼─────────────────────┐
    │           Go BlockProcessor               │
    │  ├─ processSingleEpochData()              │
    │  ├─ createBlockFromResults()              │
    │  ├─ commitWorker() — synchronous commit   │
    │  └─ persistWorker() — async LevelDB write │
    └───────────────────────────────────────────┘
```

### Thứ Tự Khởi Tạo Module

1. **ExecutorClient** — kết nối tới Go (phải có trước tất cả)
2. **StorageSetup** — đọc state từ Go qua ExecutorClient
3. **CommitProcessor** — cần StorageSetup data
4. **STARTUP-SYNC** — cần CommitProcessor + peers
5. **initialize_from_go()** — sau STARTUP-SYNC
6. **Authority** — sau initialize_from_go()

---

## 8. Anti-Patterns (Tránh Over-Engineering)

| ❌ Không làm | ✅ Thay thế |
|-------------|------------|
| Hash committee rồi so sánh hash | So sánh trực tiếp sorted authority keys |
| Tạo ExecutorClient mới cho mỗi RPC | Dùng chung `executor_client_for_proc` |
| GEI tolerance / fuzzy matching | Đọc chính xác từ Go, retry nếu fail |
| Parent hash fatal (crash node) | Warning log, hash excludes parentHash |
| Import P2P blocks trên Master | Chỉ Master tạo block từ consensus |
| Async initialize_from_go() | Synchronous — fast (<1ms UDS call) |
| Multiple source of truth | Go = state truth, Rust = consensus truth |
