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
Queue transactions độc lập: Không chặn cứng quá trình tạo DAG bằng các tác vụ mạng đồng bộ. Các giao dịch trong lúc khởi tạo hoặc chuyển epoch được hàng đợi (queue) an toàn, tránh rơi rớt giao dịch đầu tiên.
```

---

## 2. Chuỗi Khởi Động 4 Pha

Khởi động diễn ra **tuần tự nghiêm ngặt**. Mỗi pha phải hoàn thành trước khi pha sau bắt đầu.

```mermaid
flowchart TD
    START(["ConsensusNode::new()"]) --> P1

    subgraph P1["Phase 1: setup_storage()"]
        direction TB
        A1["ExecutorClient::new(IPC)"] --> A2["get_last_block_number()\n⏳ Infinite retry until is_ready=true"]
        A2 --> A3["get_current_epoch() — LOCAL Go only\n(peers gây deadlock snapshot)"]
        A3 --> A4["get_epoch_boundary_data()\nvalidators + boundary_gei + timestamp"]
        A4 --> A5["Committee Hash Verify vs Peers\n(cold-start only)"]
        A5 --> A6["calculate_last_global_exec_index()\nGEI + CommitIndex + CommitHash"]
        A6 --> A7["Identity: find own_index in committee"]
    end

    P1 --> P2

    subgraph P2["Phase 2: setup_consensus()"]
        direction TB
        B1["go_replay_after = last_handled_commit_index"] --> B2["Create CommitProcessor + CommitConsumer"]
        B2 --> B3["Early Peer RPC Server\n(ngăn deadlock cluster)"]
        B3 --> B4

        subgraph B4["STARTUP-SYNC (retry loop)"]
            direction TB
            S1["Anti-Fork: verify local block hash vs peers\n🔴 PANIC nếu mismatch"] --> S2["Fetch blocks từ peers (chunk by epoch)"]
            S2 --> S3["Execute qua Go FFI"]
            S3 --> S4["Re-query Go: GEI, CommitIndex, Epoch"]
            S4 --> S5{"Gap ≤ 2?"}
            S5 -->|"Không"| S2
            S5 -->|"Có"| S6["Post-Sync block hash verify\n🔴 EXIT(1) nếu mismatch"]
        end

        B4 --> B5["initialize_from_go() — ĐỒNG BỘ\nnext_block, next_gei, replay_guard"]
        B5 --> B6["Sync Parity Assertion\nCP vs ExecutorClient vs Go"]
        B6 --> B7["GEI Cross-check vs peers"]
        B7 --> B8["Post-Sync Committee Resolution\n(epoch > 0, retry 3x + peer fallback)"]
        B8 --> B9["Start Authority\n(Bootstrapping phase)"]
        B9 --> B10["DAG-GATE: async wait Healthy\nthen unlock proposals"]
    end

    P2 --> P3["Phase 3: setup_networking()\nClock/NTP sync"]
    P3 --> P4["Phase 4: setup_epoch_management()\nTransitionManager + EpochMonitor + SyncController"]
    P4 --> LIVE(["🟢 Node Live"])
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

#### 3.2.3 Anti-Fork Hash Verification (Mandatory State Parity)

Trong quá trình `STARTUP-SYNC`, Rust thực hiện cross-check block hiện tại của Go Master với mạng lưới:

```
Rust                    Peers                   Go
  │                       │                      │
  ├─ fetch_blocks(local) ─►│                      │
  │◄── [Peer Block X] ────┤                      │
  │                       │                      │
  ├─ get_blocks_range(local) ───────────────────►│
  │◄── [Local Block X] ──────────────────────────┤
  │                       │                      │
  │  Compare:             │                      │
  │  - block_hash         │                      │
  │  - timestamp_ms       │                      │
  │  - txRoot & receiptRoot                      │
  │                       │                      │
```

**Gate (FATAL PANIC):** Nếu bất kỳ trường dữ liệu nào (đặc biệt là hash, timestamp, txRoot, stateRoot) của `local` không khớp với `peer`, node sẽ bị **chặn hoàn toàn (panic!)** thay vì chỉ warning và gán `local_block = 0`. Điều này ngăn chặn việc state bị hỏng (corrupted DAG hoặc snapshot cũ) tiếp tục chạy và lan truyền state lỗi lên consensus, buộc operator phải wipe DB và sync lại từ đầu.

#### 3.2.4 GEI Cross-Check

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

#### 3.2.5 Sync Parity Assertion (Post-init Verification)

Ngay sau `initialize_from_go()`, hệ thống thực hiện kiểm tra chéo 3 chiều:

```mermaid
flowchart LR
    GO["Go Authoritative\ncommit_index, gei, block, epoch"] --> CHECK{"Sync Parity\nAssertion"}
    CP["CommitProcessor\ngo_last_commit_index\nnext_expected_index"] --> CHECK
    EC["ExecutorClient\nnext_expected_index"] --> CHECK
    CHECK -->|"Match"| OK["✅ PASS"]
    CHECK -->|"CP ahead of Go"| FIX1["⚠️ Reset CP to Go values"]
    CHECK -->|"Go at 0, CP > 0"| FIX2["⚠️ Reset CP to (0, 1)"]
    CHECK -->|"EC ≠ CP > 1 delta"| WARN["⚠️ Log divergence warning"]
```

**Mục đích**: Ngăn chặn CommitProcessor bỏ sót commits (nếu ahead) hoặc replay commits đã xử lý (nếu behind) so với trạng thái Go thực tế.

#### 3.2.6 Post-Sync Committee Resolution

Sau khi STARTUP-SYNC hoàn tất và epoch có thể đã thay đổi, hệ thống **giải lại committee**:

1. Query Go cho epoch hiện tại (retry 3 lần, mỗi lần cách 500ms)
2. Nếu Go thất bại → fallback sang peers (`get_safe_epoch_boundary_data_with_force`)
3. Nếu cả Go và peers đều thất bại → **graceful degradation** sang SyncOnly mode (không panic)
4. Cập nhật `storage.committee`, `validator_eth_addresses`, `own_index`, `epoch_timestamp_ms`

**Gate**: Không panic. Node tự chuyển sang SyncOnly nếu không thể xác định committee.

#### 3.2.7 DAG-GATE (Dynamic Proposal Unlock)

Sau khi Authority khởi động ở trạng thái `Bootstrapping`, hệ thống **không cho phép tạo proposals** ngay lập tức:

```mermaid
sequenceDiagram
    participant Auth as Authority (Bootstrapping)
    participant CS as CommitSyncer
    participant Hub as CoordinationHub
    participant Gate as DAG-GATE (async)

    Auth->>Hub: startup_sync_active = true
    Note over Auth: Proposals BLOCKED
    Gate->>Gate: Wait 10s (Network Discovery)
    CS->>CS: Replay DAG from peers
    CS->>Hub: Phase = Healthy
    Gate->>Hub: startup_sync_active = false
    Note over Auth: Proposals UNLOCKED ✅
```

**Mục đích**: CommitSyncer cần thời gian để tái tạo DAG từ peers (leader schedule, commit history). Nếu unlock proposals quá sớm, node sẽ tạo blocks với metadata sai (timestamp, txRoot khác cluster) → fork.

#### 3.2.8 Runtime Fork Guard (500-block Background Verification)

Sau khi STARTUP-SYNC đồng bộ blocks, hệ thống spawn một task nền kiểm tra **500 block đầu tiên** sau khởi động:

- Mỗi 10 blocks, so sánh hash của block mới nhất với peers
- Nếu mismatch → `process::exit(1)` dừng node ngay lập tức
- Nếu pass 500 blocks → task tự huỷ

**Mục đích**: Bắt các fork muộn xuất phát từ lỗi timestamp non-determinism hoặc state drift nhỏ mà POST-SYNC-VERIFY bỏ sót (vì nó chỉ kiểm tra block cuối cùng được sync).

#### 3.2.9 Health Check

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
| Timeout trong `stop_authority_and_poll_go` dùng Go's trailing GEI | GEI overlap, bỏ sót block epoch mới | Dùng `synced_global_exec_index` từ callback |
| Go import P2P blocks song song với consensus | Trie state bị overwrite bởi foreign data | Disabled trên Master |
| Update GEI và CommitIndex rời rạc | Sequence shifting, duplicate blocks sau khi restart | Dùng `BatchPut` lưu atomic (Atomic Consensus Persistence) |

---

## 7. Synchronized Snapshot Pipeline (Phòng Chống Mất Dữ Liệu GEI)

**Vấn đề cốt lõi:**
Trước đây, khi `snapshot_manager` kích hoạt tạo snapshot (PebbleDB Checkpoint), nó chỉ chờ `persistWorker` xả xong bộ nhớ của Trie (tức là stateRoot) mà bỏ qua tiến trình của `commitWorker` và `backupDbWorker`. Do quá trình lưu block payloads và cập nhật `GlobalExecIndex` (GEI) diễn ra bất đồng bộ, snapshot bị tạo ra **trước khi** GEI mới nhất được ghi xuống `backup_db`. Hậu quả là node sau khi restore snapshot sẽ bị mất GEI (GEI = 0) và tự động rơi vào trạng thái fork hoặc panic fail-fast.

**Kiến trúc mới:**
Quá trình tạo snapshot nay yêu cầu một khóa bảo vệ liên hoàn (Synchronized Drain) quét qua toàn bộ 3 pipeline lưu trữ ngầm trước khi cho phép đóng băng hệ thống để lấy Checkpoint.

```mermaid
sequenceDiagram
    autonumber
    participant SM as SnapshotManager
    participant BP as BlockProcessor
    participant CW as CommitWorker
    participant PW as PersistWorker
    participant BW as BackupDbWorker
    participant DB as PebbleDB
    
    SM->>BP: PauseExecution() -> WaitForPersistence()
    
    rect rgb(230, 240, 250)
        Note over BP,CW: Pipeline 1: Xả Commit Worker
        BP->>CW: Gửi CommitJob{DoneChan}
        CW-->>BP: Đóng DoneChan (Toàn bộ GEI/Block đã xử lý vào RAM)
    end
    
    rect rgb(230, 250, 230)
        Note over BP,PW: Pipeline 2: Xả Persist Worker
        BP->>PW: Gửi PersistJob{DoneSignal}
        PW-->>BP: Tín hiệu Done (Toàn bộ Trie Node ghi xong xuống LevelDB)
    end
    
    rect rgb(250, 230, 230)
        Note over BP,BW: Pipeline 3: Xả BackupDb Worker
        BP->>BW: backupDbWg.Wait()
        BW-->>BP: Bộ đếm về 0 (Toàn bộ GEI/CommitIndex/Payload ghi xong đĩa)
    end
    
    SM->>DB: CheckpointAll()
    Note right of DB: Snapshot được tạo với dữ liệu đồng nhất 100% giữa Go và Rust!
```

Nhờ kiến trúc rào chắn 3 lớp này, `metadata.json` chứa `stateRoot` và `backup_db` chứa `GEI/CommitIndex` luôn khớp hoàn hảo, đảm bảo không có giao dịch nào bị thất thoát qua khe hở async.

---

## 8. Atomic Consensus Persistence & Crash Recovery

**Vấn đề cốt lõi:**
Khi Go xử lý xong block từ Rust, hệ thống phải cập nhật 2 giá trị vào ổ đĩa (`BackupDb` - PebbleDB) để đánh dấu tiến độ: `last_global_exec_index` (GEI) và `last_handled_commit_index`.

Nếu 2 thao tác này diễn ra rời rạc (non-atomic), một sự cố sập nguồn (crash) xảy ra giữa chừng sẽ đẩy DB vào trạng thái bất nhất: **GEI đã nhảy số, nhưng CommitIndex thì chưa**.

**Quá trình gây Fork:**
1. Khi khởi động lại, Rust đọc lại tiến độ. Do CommitIndex cũ, Rust lôi Commit cũ ra bắt Go chạy lại (Replay).
2. Khi cấp GEI cho block chạy lại này, Rust thấy GEI đã nâng lên, nên cấp GEI mới toanh (X+1).
3. Do GEI X+1 lớn hơn GEI cũ X trên blockchain của Go, `GEI-REGRESSION GUARD` của Go bị đánh lừa! Nó tưởng đây là khối mới, tạo ra bản duplicate của block cũ nhưng với GEI mới!
4. **Hệ quả:** Toàn bộ chuỗi sau đó bị đẩy lùi 1 GEI. `txRoot`, `receiptsRoot`, `leaderAddress` và `timestamp` của các block sau đó lệch hoàn toàn giữa các node (do node sập có thêm một block rác chen vào giữa). Dù GEI giống nhau, nhưng nó ánh xạ tới một Commit khác biệt (thời gian trễ hơn, leader khác), tạo ra Fork hệ thống.

**Giải Pháp Kiến Trúc:**
Từ phiên bản tối ưu, quá trình này **bắt buộc** phải sử dụng `BatchPut` để đóng gói `GlobalExecIndex`, `CommitIndex`, và `Epoch` vào một transaction Atomic duy nhất (`updateAndPersistConsensusState`). Đảm bảo một là ghi đủ, hai là không ghi gì cả.

---

## 9. CommitSyncer Gap Handling & Consumer Monitor Synchronization

**Vấn đề cốt lõi:**
Khi CommitSyncer nhận thấy `local_commit_index` (của DAG) vượt xa tiến độ xử lý `highest_handled_commit` (của Go) quá 10 commit (Gap > 10), nó sẽ kích hoạt cơ chế kéo lùi `synced_commit_index` về vị trí `highest_handled_commit` để ưu tiên fetch lại block cho Go đuổi kịp.

**Quá trình gây lặp fetch và fork state:**
1. Trong các thiết kế trước, `highest_handled_commit` **chỉ được cập nhật duy nhất 1 lần** trong quá trình `STARTUP-SYNC` ở Phase 1. Trong suốt thời gian consensus hoạt động (Healthy/CatchingUp), biến này **không hề nhúc nhích**.
2. Vì `highest_handled_commit` bị kẹt ở chỉ số cũ (ví dụ: 967), khoảng cách (Gap) giữa nó và tiến độ tạo block của DAG (ví dụ: 1803) sẽ ngày càng giãn rộng ra.
3. Khi Gap > 10, CommitSyncer lôi ngược `synced_commit_index` từ 1803 về 967.
4. CommitSyncer đi hỏi P2P để kéo lại các commit từ 968 -> 1803. Khi kéo về, nó ném vào `Core`.
5. `Core` đẩy các commit này sang `CommitProcessor`. Tuy nhiên, `CommitProcessor` lại đang có `next_expected_index` = 1804. Nó thấy commit 968 < 1804 nên nó drop toàn bộ!
6. Vòng lặp trên diễn ra liên tục. Trong khi đó, Go Master không nhận được block mới (kể cả empty commits cũng bị drop). State bị kẹt. Node rơi vào trạng thái fork với state root cũ kỹ so với leader.

**Giải Pháp Kiến Trúc (Synchronous Consumer Monitor):**
`CommitProcessor` nay được cấp quyền truy cập trực tiếp vào `CommitConsumerMonitor` thông qua callback (`create_commit_index_callback`).
Bất kể commit đó có chứa transaction hay là empty commit (bị `dispatch_commit` skip qua FAST-PATH), ngay sau khi xử lý xong, `CommitProcessor` sẽ đồng thời:
1. Cập nhật `current_commit_index`
2. Cập nhật `highest_handled_commit` trong Monitor

Từ đó, `highest_handled_commit` luôn bám sát nút `local_commit` của DAG. Gap luôn được giữ mức ~0-1 (trừ khi Go thực sự treo do IO/Backpressure), ngăn chặn hoàn toàn việc CommitSyncer bị ảo giác về tiến trình và kéo lùi sync network một cách mù quáng.

---

## 10. Transaction Decoupling (Tách Biệt Nhận Giao Dịch và Quản Lý DAG)

**Vấn đề cốt lõi:**
Trước đây, khi tạo Genesis hoặc chuyển Epoch, quá trình `STARTUP-SYNC` hoặc `poll_go_until_synced` diễn ra đồng bộ và chặn luồng. Lúc này `TxSocketServer` bị vướng timeout (giới hạn số lần thử) và tự động drop "giao dịch đầu tiên" nếu việc khởi tạo DAG kéo dài (vd: mất hơn 2 phút chờ Go). Điều này gây ra việc mất giao dịch hệ thống và giao dịch người dùng ngay sau khi tạo epoch/genesis.

**Giải Pháp Kiến Trúc:**
Từ bản fix tháng 05/2026, luồng nhận giao dịch qua FFI/UDS hoàn toàn tách biệt:
1. **No Drop Timeout:** `TxSocketServer` xóa bỏ giới hạn số lần retry (timeout). Giao dịch đến từ Go Master qua FFI sẽ được safely buffered (queue vô hạn với periodic log `Delayed TXs`) trong lúc Node thực hiện chuyển giao Epoch hoặc bắt kịp tiến độ (SyncOnly).
2. **Smooth DAG Initialization:** `DagState::new` chỉ chạy sau khi quá trình đồng bộ hoàn tất (Phase 2). Việc này không còn bị vướng chặn timeout của Network UDS, tạo sự mượt mà lúc chuyển mạng và giữ an toàn tuyệt đối cho transactions ở buffer.
3. **Backpressure:** Nếu hàng đợi của `ffi_tx_receiver` quá đầy (capacity = 1000 batches), Go Master sẽ nhận được `false` và tự động retry bên phía Go, đảm bảo 0% mất mát.

---

## 11. Epoch Transition Timestamp Determinism

Quá trình chuyển giao Epoch (Epoch Transition) tiềm ẩn rủi ro fork lớn nhất nếu xử lý không cẩn thận.
Nguyên tắc bất biến:
1. **Timestamp**: Phải dùng `boundary_timestamp_ms` (lấy từ commit chứa EndOfEpoch). Go tuyệt đối **KHÔNG ĐƯỢC** dùng `time.Now()` hay tự fallback timestamp cho block chuyển giao epoch.
2. **Deterministic GEI Base**: Khi epoch mới bắt đầu, GEI phải được nối tiếp chính xác từ GEI của block chuyển giao epoch (`synced_global_exec_index` từ `epoch_transition_callback`).
3. **Go Processing Lag**: Trong lúc Rust đã sẵn sàng sang epoch mới, Go có thể đang xử lý các commit cuối của epoch cũ trong pipeline (Go lag). Rust **phải** dùng `synced_global_exec_index` làm `effective_synced` thay vì dùng giá trị GEI mà Go trả về lúc đó (`get_last_global_exec_index`). Nếu dùng GEI trả về từ Go khi Go chưa xử lý xong, epoch mới sẽ bị gán đè GEI lên các commit đang chờ xử lý của epoch cũ, dẫn đến mất transactions và fork (do GEI-REGRESSION guard của Go sẽ skip).

### 11.1 SystemTransactionProvider Startup Sync (FIX 7 — May 2026)

**Vấn đề**: `SystemTransactionProvider` được tạo **TRƯỚC** STARTUP-SYNC (line ~1186) với `epoch_timestamp_ms` từ snapshot (rất cũ). FIX 6 reset thành `now()`, nhưng sau 120s (`epoch_duration`), provider trigger `EndOfEpoch` → node nhảy epoch liên tục (3→4→5→6) trong khi cluster giữ epoch 3.

**Triệu chứng**: m3 fork tại block 1113 với:
- Khác `txRoot` (chứa EndOfEpoch system TX)
- Khác `receiptsRoot`, `leaderAddress` (epoch-scoped)
- Khác `timestamp` (epoch boundary timestamp)
- GEI nhảy 3 mỗi block (do 3 system TX/block)

**Nguyên tắc**: Node vừa phục hồi snapshot **KHÔNG ĐƯỢC** tự quyết định khi nào EndOfEpoch. Epoch timing là quyết định **toàn mạng**, node chưa sẵn sàng phải **THEO** chứ không **DẪN**.

**Giải pháp (FIX 7 — Suppression Flag)**:

1. **Suppress**: Khi phát hiện timestamp cũ (`elapsed >= epoch_duration`), đặt `epoch_change_suppressed = true`
2. **Follow**: Node vẫn **XỬ LÝ** EndOfEpoch từ committed blocks, chỉ không **ĐỀ XUẤT** chúng
3. **Auto-clear (từ cluster)**: Khi `update_epoch()` nhận timestamp **mới** (`elapsed < epoch_duration`) — tức cluster đã commit EndOfEpoch thật → suppression tự động tắt
4. **Re-sync**: Sau Post-Sync Committee Resolution, gọi `system_transaction_provider.update_epoch()` — nếu timestamp vẫn cũ, suppression vẫn giữ
5. **Auto-unsuppress (safety net)**: Nếu node đã ở Healthy **>= 2×epoch_duration** (240s) mà chưa nhận EndOfEpoch từ cluster → auto-clear suppression. Xử lý trường hợp **tất cả node restart đồng thời** (không ai trigger EndOfEpoch).

```mermaid
sequenceDiagram
    participant STP as SystemTransactionProvider
    participant CN as ConsensusNode
    participant Cluster as Other Nodes

    CN->>STP: new(epoch=1, ts=genesis)
    Note over STP: 🛡️ ts cũ → suppressed=TRUE<br/>Node sẽ THEO, không DẪN

    CN->>CN: STARTUP-SYNC (catch up)
    CN->>STP: update_epoch(epoch=3, ts=epoch3_start)
    Note over STP: ts vẫn cũ → suppressed=TRUE

    CN->>CN: DAG-GATE unlock → Healthy
    CN->>STP: notify_healthy()
    Note over STP: Bắt đầu đếm auto-unsuppress

    alt Cluster có node khỏe (bình thường)
        Cluster->>Cluster: Leader khác trigger EndOfEpoch
        Cluster->>CN: Committed block chứa EndOfEpoch
        CN->>STP: update_epoch(epoch=4, ts=NOW)
        Note over STP: ✅ ts mới → suppressed=FALSE
    else Full cluster restart (tất cả suppressed)
        Note over STP: Healthy >= 2×epoch_duration (240s)
        STP->>STP: Auto-unsuppress + reset epoch_start=now
        Note over STP: ✅ Node trigger EndOfEpoch bình thường
    end
```

---

## 12. Module Dependencies

```mermaid
flowchart TD
    CN["ConsensusNode\n(orchestrator)"] --> EC["ExecutorClient\n(IPC to Go)"]
    CN --> CP["CommitProcessor\n(Stage 3)"]
    CN --> AUTH["Authority\n(Mysticeti DAG)"]
    CN --> HUB["CoordinationHub\n(shared state)"]
    
    CP --> BDM["BlockDeliveryManager\n(Stage 4)"]
    CP --> TR["TxRecycler"]
    CP --> LM["LagMonitor"]
    
    AUTH --> CS["CommitSyncer"]
    CS --> HUB
    
    EC --> FFI["FFI Bridge (CGo + UDS)"]
    BDM --> FFI
    
    FFI --> GO_BP["Go BlockProcessor"]
    
    subgraph Go["Go Execution Engine"]
        GO_BP --> CW["commitWorker"]
        GO_BP --> PW["persistWorker\n(LevelDB trie)"]
        GO_BP --> BW["backupDbWorker\n(PebbleDB backup)"]
        GO_BP --> GW["geiWorker\n(coalesced GEI)"]
        GO_BP --> SM["SnapshotManager\n(WaitForPersistence)"]
    end
```

### Thứ Tự Khởi Tạo Module

1. **ExecutorClient** — kết nối tới Go (phải có trước tất cả)
2. **CoordinationHub** — shared state container (startup_sync_active, GEI, phase)
3. **StorageSetup** — đọc state từ Go qua ExecutorClient
4. **CommitProcessor** — cần StorageSetup data
5. **Early Peer RPC Server** — ngăn deadlock cluster
6. **STARTUP-SYNC** — cần CommitProcessor + peers
7. **initialize_from_go()** — sau STARTUP-SYNC (đồng bộ)
8. **Sync Parity Assertion** — sau initialize_from_go()
9. **Post-Sync Committee Resolution** — sau GEI cross-check
10. **SystemTransactionProvider re-sync** — `update_epoch()` với epoch/timestamp mới (suppression giữ nếu timestamp cũ)
11. **Authority** — sau committee resolution (Bootstrapping phase)
12. **DAG-GATE** — async, unlock proposals khi phase == Healthy

---

## 13. Anti-Patterns (Tránh Over-Engineering)

| ❌ Không làm | ✅ Thay thế |
|-------------|------------|
| Hash committee rồi so sánh hash | So sánh trực tiếp sorted authority keys |
| Tạo ExecutorClient mới cho mỗi RPC | Dùng chung `executor_client_for_proc` |
| GEI tolerance / fuzzy matching | Đọc chính xác từ Go, retry nếu fail |
| Parent hash fatal (crash node) | Warning log, hash excludes parentHash |
| Import P2P blocks trên Master | Chỉ Master tạo block từ consensus |
| Async initialize_from_go() | Synchronous — fast (<1ms UDS call) |
| Multiple source of truth | Go = state truth, Rust = consensus truth |
| Reset epoch_start = now() (FIX 6) | Suppress EndOfEpoch, chờ cluster quyết định (FIX 7) |
| Node vừa restore tự trigger EndOfEpoch | Node THEO cluster, không tự DẪN epoch transition |

