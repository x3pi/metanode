# Node Restart Architecture — Hướng dẫn chi tiết cho người mới

**Cập nhật lần cuối:** 2026-05-22

## Mục lục
0. [**Thứ tự hàm được gọi khi khởi động**](#0-thứ-tự-hàm-được-gọi-khi-khởi-động) ← **Đọc mục này trước!**
1. [Tổng quan kiến trúc 2 lớp (Rust + Go)](#1-tổng-quan-kiến-trúc-2-lớp)
2. [Trình tự khởi động tổng quát](#2-trình-tự-khởi-động-tổng-quát)
3. [Phase 1: Go Master khởi động (Execution Layer)](#3-phase-1-go-master-khởi-động)
4. [Phase 2: Rust khởi động — setup_storage()](#4-phase-2-rust-khởi-động--setup_storage)
5. [Phase 3: Rust — setup_consensus() & STARTUP-SYNC](#5-phase-3-rust--setup_consensus--startup-sync)
6. [Phase 4: Rust — setup_networking() & setup_epoch_management()](#6-phase-4-rust--networking--epoch-management)
7. [Luồng xử lý block đồng bộ (HandleSyncBlocksRequest)](#7-luồng-xử-lý-block-đồng-bộ)
8. [SyncOnly Node Restart](#8-synconly-node-restart)
9. [Phase State Machine (ConsensusCoordinationHub)](#9-phase-state-machine)
10. [Các cơ chế bảo vệ an toàn](#10-các-cơ-chế-bảo-vệ-an-toàn)
11. [Các thuật ngữ quan trọng](#11-các-thuật-ngữ-quan-trọng)

---

## 0. Thứ tự hàm được gọi khi khởi động

> Đây là **bản đồ code** — cho bạn biết chính xác file nào, hàm nào được gọi theo thứ tự nào.
> Mỗi dòng có link thẳng vào code thực tế. Đọc phần này trước khi đọc giải thích chi tiết.

### ═══ PROCESS A: Go Master (`simple_chain`) ═══

```
main()                           main.go:49
│
├── initializeDeviceKey()        main.go:149
│
├── NewApp()                     app.go:94
│   ├── config.LoadConfig()      app.go:108        ← đọc config-master-nodeX.json
│   ├── mt_trie.InitNomtDB()     app.go:149        ← mở NOMT database (Merkle trie)
│   ├── storage.NewStorageManager()  app.go:154
│   ├── backupDB.Open()          app.go:166        ← mở PebbleDB backup
│   │
│   ├── app.initNetwork()        app.go:174        ← app_network.go
│   │   └── Tạo SocketServer, MessageSender, ConnectionsManager
│   │
│   ├── app.initStorage()        app.go:179        ← app_storage.go:12
│   │   └── initStorageDatabases()  app_storage.go:41
│   │       ├── Mở blocks DB
│   │       ├── Mở receipts DB
│   │       ├── Mở transaction_state DB
│   │       ├── Mở mapping DB
│   │       ├── Mở smart_contract_code DB
│   │       ├── Mở smart_contract_storage DB
│   │       └── Mở stake_db DB
│   │
│   ├── app.initBlockchain()     app.go:184        ← app_blockchain.go:28
│   │   ├── block.NewBlockDatabase()              ← mở block storage
│   │   ├── blockDatabase.GetLastBlock()          ← load block cuối từ disk
│   │   │   └── [NẾU LỖI] → kiểm tra có phải snapshot restore không
│   │   │       └── WasCleanShutdown() → detect crash
+   │   ├── blockchain.New()                      ← tạo Blockchain instance
│   │   ├── initSnapshotSystem()                  ← executor/snapshot_init.go:20
│   │   │   ├── NewSnapshotManager()
│   │   │   ├── sm.SetCheckpointCallback()        ← đăng ký PebbleDB checkpoint
│   │   │   ├── sm.SetNomtSnapshotCallback()      ← đăng ký NOMT snapshot
│   │   │   ├── storage.SetBlockCommitCallback()  ← gọi OnBlockCommitted mỗi block
│   │   │   └── StartSnapshotServer()             ← HTTP server :8700
│   │   └── app.startUnixSocketServer()           ← mở UDS, sẵn sàng nhận lệnh Rust
│   │
│   ├── storage.SetBlockchainInitDone()  app.go:192  ← is_ready = true 🟢
│   │
│   ├── app.initProcessors()     app.go:260
│   │   └── processor.InitProcessors()           ← BlockProcessor, TxProcessor...
│   │
│   └── app.initRoutes()         app.go:235        ← đăng ký RPC handlers
│
├── go app.Run()                 main.go:180       ← chạy trong goroutine riêng
│   ├── app.pruningManager.Start()                ← dọn dẹp state cũ định kỳ
│   ├── go app.socketServer.Listen()              ← listen P2P connections
│   └── app.blockProcessor.StartBackgroundWorkers()  ← commit worker, delivery worker
│
└── startRPCServer(app)          main.go:231       ← HTTP RPC :8757 (eth_* API)
```

### ═══ PROCESS B: Rust Metanode (`metanode`) ═══

```
main()                           src/main.rs:40
│
└── InitializedNode::initialize()  src/node/startup.rs:52
    │
    ├── FFI_TX_SENDER init       startup.rs:65     ← channel nhận tx từ Go FFI
    ├── start_prometheus_server()                  ← metrics (nếu enabled)
    │
    ├── ConsensusNode::new_with_registry_and_service()  src/node/consensus_node.rs
    │   │   (Đây là hàm QUAN TRỌNG NHẤT — toàn bộ startup logic nằm ở đây)
    │   │
    │   ├── ─── PHASE 1: setup_storage() ───────────────────────────────
    │   │   │                               consensus_node.rs:~141
    │   │   ├── ExecutorClient::new()                ← kết nối Go qua UDS
    │   │   │
    │   │   ├── [LOOP] executor_client.get_last_block_number()
    │   │   │   └── Retry vô hạn cho đến khi is_ready=true
    │   │   │       (Go phải SetBlockchainInitDone() trước)
    │   │   │
    │   │   ├── executor_client.get_current_epoch()
    │   │   ├── executor_client.get_epoch_boundary_data(epoch)
    │   │   ├── build_committee_with_eth_addresses()
    │   │   │
    │   │   ├── [IF cold-start] verify_committee_with_peers()
    │   │   │   └── Hỏi từng peer, so sánh committee hash
    │   │   │       HALT nếu có mismatch!
    │   │   │
    │   │   ├── calculate_last_global_exec_index()   ← tính GEI
    │   │   └── get_safe_epoch_boundary_data_with_force()  ← validate boundary_gei
    │   │
    │   ├── ─── PHASE 2: setup_consensus() ─────────────────────────────
    │   │   │                               consensus_node.rs:~1103
    │   │   ├── [detect dag_has_history]    ← kiểm tra epochs/epoch_N/consensus_db
    │   │   │
    │   │   ├── coordination_hub.activate_recovery_barrier()  ← BLOCK proposals
    │   │   ├── coordination_hub.set_schedule_recovery_pending(true)
    │   │   │
    │   │   ├── [calculate go_replay_after]  ← commit index Go đã xử lý
    │   │   │
    │   │   ├── CommitProcessor::new()       ← thiết lập bộ xử lý commit DAG
    │   │   │   ├── .with_next_expected_index(go_replay_after + 1)
    │   │   │   ├── .with_go_last_commit_index(go_replay_after)
    │   │   │   └── .with_digest_verifier()
    │   │   │
    │   │   ├── ══ STARTUP-SYNC BARRIER ══  consensus_node.rs:~1460
    │   │   │   │
    │   │   │   ├── [EARLY] Start PeerRpcServer   ← tránh deadlock với peers
    │   │   │   │
    │   │   │   └── [LOOP] sync cho đến khi local_block >= peer_block
    │   │   │       ├── query_peer_info(peer_addr)       ← hỏi block cao nhất
    │   │   │       ├── [IF behind] fetch_blocks_from_peer()
    │   │   │       ├── recovery::sync_and_execute_blocks()
    │   │   │       │   └── executor_client.sync_blocks_batch()
    │   │   │       │       └── → Go: HandleSyncBlocksRequest()
    │   │   │       │               app_blockchain.go / unix_socket_handler_epoch.go:1422
    │   │   │       └── executor_client.get_last_block_number()  ← re-query sau sync
    │   │   │
    │   │   ├── CommitProcessor final update   ← cập nhật go_replay_after sau sync
    │   │   │
    │   │   ├── ConsensusAuthority::start()    ← khởi động DAG engine (vote/propose)
    │   │   ├── BlockDeliveryManager::start()  ← chuyển block Rust → Go
    │   │   └── CommitSyncerSupervisor::start() ← đồng bộ DAG với peers
    │   │
    │   ├── ─── PHASE 3: setup_networking() ─────────────────────────────
    │   │   │                               consensus_node.rs:~3387
    │   │   └── ClockSyncManager::start()    ← NTP sync
    │   │
    │   └── ─── PHASE 4: setup_epoch_management() ───────────────────────
    │                                       consensus_node.rs:~3414
    │       ├── StateTransitionManager::start()   ← xử lý epoch transition
    │       ├── epoch_monitor task (tokio::spawn) ← poll epoch mỗi 10s
    │       └── fork_detection_check (tokio::spawn)  ← kiểm tra hash divergence
    │
    ├── set_transition_handler_node()       startup.rs:101
    ├── RpcServer::start()                  startup.rs:116  ← HTTP RPC Rust
    ├── TxSocketServer::start()             startup.rs:195  ← UDS tx channel
    ├── PeerRpcServer::start()              startup.rs:224  ← P2P RPC server
    │
    └── run_main_loop()                     startup.rs:249
        └── [LOOP mỗi 5s] node.is_alive()  ← Supervisor: auto-restart nếu crash
```

### Tóm tắt thứ tự gọi hàm theo timeline

| Thứ tự | Process | Hàm | File | Mục đích |
|:---:|:---:|---|---|---|
| 1 | **Go** | `main()` | `main.go:49` | Entry point |
| 2 | **Go** | `initializeDeviceKey()` | `main.go:149` | Xác thực node identity |
| 3 | **Go** | `NewApp()` | `app.go:94` | Khởi tạo toàn bộ Go layer |
| 4 | **Go** | `initNetwork()` | `app.go:174` | Tạo socket, connections |
| 5 | **Go** | `initStorage()` | `app.go:179` | Mở tất cả databases |
| 6 | **Go** | `initBlockchain()` | `app.go:184` | Load blockchain state |
| 7 | **Go** | `InitSnapshotSystem()` | `snapshot_init.go:20` | Đăng ký snapshot callbacks |
| 8 | **Go** | `SetBlockchainInitDone()` | `app.go:192` | 🟢 is_ready = true |
| 9 | **Go** | `app.Run()` | `app.go:325` | Start workers, pruner |
| 10 | **Go** | `startRPCServer()` | `main.go:231` | Mở HTTP RPC cho user |
| — | — | *[Rust process start]* | — | — |
| 11 | **Rust** | `main()` | `main.rs:40` | Entry point |
| 12 | **Rust** | `InitializedNode::initialize()` | `startup.rs:52` | Orchestrate startup |
| 13 | **Rust** | `ConsensusNode::new()` | `consensus_node.rs` | **Core startup** |
| 14 | **Rust** | `setup_storage()` | `consensus_node.rs:~141` | Query Go, load epoch/GEI |
| 15 | **Rust** | `get_last_block_number()` | UDS call → Go | Lấy block number (retry until ready) |
| 16 | **Rust** | `verify_committee_with_peers()` | `consensus_node.rs:~750` | Anti-fork committee check |
| 17 | **Rust** | `setup_consensus()` | `consensus_node.rs:~1103` | Thiết lập DAG engine |
| 18 | **Rust** | `activate_recovery_barrier()` | via CoordinationHub | Block mọi proposals |
| 19 | **Rust** | `CommitProcessor::new()` | `consensus_node.rs:~1315` | Cấu hình commit pipeline |
| 20 | **Rust** | **STARTUP-SYNC loop** | `consensus_node.rs:~1460` | Đồng bộ blocks còn thiếu |
| 21 | **Go** | `HandleSyncBlocksRequest()` | `unix_socket_handler_epoch.go:1422` | Nhận và apply blocks |
| 22 | **Rust** | `ConsensusAuthority::start()` | `consensus_node.rs:~2200` | DAG bắt đầu chạy |
| 23 | **Rust** | `setup_networking()` | `consensus_node.rs:~3387` | NTP sync |
| 24 | **Rust** | `setup_epoch_management()` | `consensus_node.rs:~3414` | Epoch monitor |
| 25 | **Rust** | `run_main_loop()` | `startup.rs:249` | Supervisor loop |

---

## 1. Tổng quan kiến trúc 2 lớp

Metanode có 2 chương trình chạy song song, giao tiếp qua Unix Domain Socket (UDS):

```
┌─────────────────────────────────────────┐
│           Rust (Consensus Layer)        │
│  • Quản lý DAG (đồ thị biểu quyết)    │
│  • Chạy BFT consensus (vote, propose)  │
│  • Xác định thứ tự block               │
│  • Giao tiếp P2P với node khác         │
└────────────────┬────────────────────────┘
                 │ Unix Domain Socket (UDS)
                 │ Protobuf messages
┌────────────────▼────────────────────────┐
│           Go (Execution Layer)          │
│  • Lưu trữ block, account state        │
│  • Thực thi giao dịch (EVM/MVM)        │
│  • Quản lý Merkle Tree (NOMT)          │
│  • Phục vụ RPC cho user (eth_*)        │
│  • Lưu lịch sử (StateChangelogDB)     │
└─────────────────────────────────────────┘
```

**Khi node khởi động lại**, Go LUÔN chạy trước, Rust chạy sau. Rust sẽ hỏi Go "anh đang ở block bao nhiêu?" rồi quyết định cần làm gì tiếp.

---

## 2. Trình tự khởi động tổng quát

```
Node Process Start
│
├── 1. Go Master khởi động
│   ├── Mở tất cả database (PebbleDB, NOMT, LevelDB)
│   ├── Load block cuối cùng từ ổ đĩa
│   ├── Rebuild NOMT trie từ state đã lưu
│   ├── Mở Unix Socket, sẵn sàng nhận lệnh
│   └── Đánh dấu is_ready = true ✅
│
├── 2. Rust khởi động — setup_storage()
│   ├── Kết nối Go qua UDS
│   ├── Hỏi Go: "block hiện tại?" (retry cho đến khi is_ready=true)
│   ├── Hỏi Go: "epoch hiện tại?"
│   ├── Load committee (danh sách validator)
│   ├── Tính toán GEI (Global Execution Index)
│   └── Xác định danh tính node trong committee
│
├── 3. Rust — setup_consensus()
│   ├── Thiết lập CommitProcessor
│   ├── ═══ STARTUP-SYNC BARRIER ═══
│   │   ├── Hỏi peers: "block cao nhất mạng là bao nhiêu?"
│   │   ├── Nếu local < network: Tải block thiếu từ peer
│   │   └── Gửi blocks cho Go xử lý (sync_and_execute_blocks)
│   ├── Khởi động ConsensusAuthority (DAG engine)
│   └── Khởi động BlockDeliveryManager
│
├── 4. Rust — setup_networking()
│   └── Đồng bộ đồng hồ NTP
│
└── 5. Rust — setup_epoch_management()
    ├── Khởi động epoch_monitor (poll 10s)
    ├── Khởi động StateTransitionManager
    └── Kiểm tra fork detection
```

---

## 3. Phase 1: Go Master khởi động

### Tóm tắt
Go là chương trình lưu trữ và thực thi giao dịch. Khi khởi động, nó mở tất cả database, load trạng thái blockchain cuối cùng, và sẵn sàng nhận lệnh từ Rust.

### Code flow

**Bắt đầu tại** [app.go:184](file:///home/abc/nhat/con-chain-v2/metanode/execution/cmd/simple_chain/app.go#L184):
```go
// Initialize blockchain components
if err := app.initBlockchain(); err != nil {
    return nil, fmt.Errorf("failed to initialize blockchain: %v", err)
}
// CRITICAL: Mark blockchain initialization as complete
storage.SetBlockchainInitDone()  // ← Rust chỉ tin giá trị Go sau dòng này
```

**Bên trong** [app_blockchain.go:28](file:///home/abc/nhat/con-chain-v2/metanode/execution/cmd/simple_chain/app_blockchain.go#L28):
```go
func (app *App) initBlockchain() error {
    // 1. Mở Block Database (PebbleDB)
    blockDatabase := block.NewBlockDatabase(app.storageManager.GetStorageBlock())

    // 2. Load block cuối cùng đã lưu trên ổ đĩa
    lastBlock, err := blockDatabase.GetLastBlock()
    if err != nil {
        // Nếu mất dữ liệu → kiểm tra có cần recovery không
        // (xem SAFETY GUARD bên dưới)
    }

    // 3. Tạo Blockchain instance từ lastBlock
    // 4. Rebuild NOMT state trie
    // 5. Mở Unix Socket, sẵn sàng nhận lệnh từ Rust
}
```

### Phát hiện crash recovery
Khi node bị tắt đột ngột (kill -9, mất điện), Go phát hiện qua cơ chế `WasCleanShutdown()`:
- Nếu `true`: Node tắt đàng hoàng → dữ liệu trên ổ đĩa đáng tin cậy.
- Nếu `false`: Node bị crash → có thể mất dữ liệu chưa flush. Go sẽ:
  - Kiểm tra NOMT session chưa flush (`pendingFinishedSession`)
  - Load lại từ file backup (`lastBlock_crash_recovery.dat`) nếu có
  - Verify tính toàn vẹn dữ liệu trước khi phục vụ

### `is_ready` flag
Rust cần đợi Go hoàn tất khởi tạo trước khi bắt đầu. Go đánh dấu `is_ready=true` **CHỈ SAU** khi `initBlockchain()` hoàn tất. Giá trị block number trước `is_ready` có thể sai (do metadata.json chưa load xong).

---

## 4. Phase 2: Rust khởi động — setup_storage()

### Tóm tắt
Rust cần biết: *Node này đang ở đâu? Epoch nào? Ai trong committee? GEI bao nhiêu?*

### Code flow

**File:** [consensus_node.rs:141](file:///home/abc/nhat/con-chain-v2/metanode/consensus/metanode/src/node/consensus_node.rs#L141)

#### Bước 1: Kết nối Go và lấy block number

```rust
async fn setup_storage(config: &NodeConfig) -> Result<StorageSetup> {
    // Tạo ExecutorClient — giao tiếp Go qua UDS
    let executor_client = Arc::new(ExecutorClient::new(/*...*/));

    // Retry vô hạn cho đến khi Go trả is_ready=true
    let latest_block_number = loop {
        match executor_client.get_last_block_number().await {
            Ok((n, _, true, _, _)) => {
                // is_ready=true → block number đáng tin cậy
                break n;
            }
            Ok((n, _, false, _, _)) => {
                // Go chưa sẵn sàng, đợi 500ms rồi thử lại
                tokio::time::sleep(Duration::from_millis(500)).await;
            }
            Err(e) => {
                // Lỗi kết nối, đợi rồi thử lại
                tokio::time::sleep(Duration::from_millis(500)).await;
            }
        }
    };
```

> **Tại sao retry vô hạn?** Vì Rust PHẢI có block number đúng. Nếu dùng giá trị sai → tính GEI sai → sai block hash → FORK (mạng bị chia đôi).

#### Bước 2: Lấy epoch từ local Go

```rust
    // CRITICAL: LUÔN dùng epoch từ local Go, KHÔNG dùng epoch từ peer!
    // Lý do: Nếu dùng peer epoch (VD: 100) mà Go chỉ ở epoch 1,
    //        Rust sẽ đợi Go đến boundary → Go không có dữ liệu → DEADLOCK.
    let epoch = executor_client.get_current_epoch().await?;
```

#### Bước 3: Load committee (danh sách validator)

```rust
    // Lấy thông tin committee từ Go's epoch boundary data
    let (epoch, timestamp, boundary_block, validators, epoch_dur, boundary_gei) =
        executor_client.get_epoch_boundary_data(current_epoch).await?;

    // Build committee + ETH addresses
    let (committee, validator_eth_addresses) =
        build_committee_with_eth_addresses(validators, current_epoch)?;
```

#### Bước 4: Xác thực committee với peers (Cold-start)

Khi DAG rỗng (sau snapshot restore), committee từ local Go có thể cũ. Rust sẽ:
1. Tính hash của committee
2. Hỏi TỪNG peer "committee hash của bạn là gì?"
3. Nếu có BẤT KỲ peer nào mismatch → **HALT ngay** (tránh fork)
4. Nếu ≥1 peer match → an toàn để tiếp tục
5. Nếu không ai trả lời → retry vô hạn (đợi ít nhất 1 peer online)

```rust
    // Simplified logic:
    if mismatching_peers > 0 {
        bail!("Committee hash MISMATCH! HALTING NODE to prevent fork.");
    }
    if matching_peers > 0 {
        break; // Safe to proceed
    }
    // All unreachable → retry forever
```

#### Bước 5: Tính GEI (Global Execution Index)

```rust
    let (_, last_global_exec_index, last_executed_commit_hash, ..) =
        Self::calculate_last_global_exec_index(config, &executor_client, /*...*/);
```

GEI là con số đếm tuần tự cho MỌI commit (bao gồm empty). Công thức:
```
GEI = epoch_base_index + commit_index + fragment_offset
```

---

## 5. Phase 3: Rust — setup_consensus() & STARTUP-SYNC

### Tóm tắt
Đây là phase quan trọng nhất: Rust thiết lập bộ máy consensus và **đảm bảo Go bắt kịp mạng** trước khi bắt đầu tạo block mới.

### Code flow

**File:** [consensus_node.rs:1103](file:///home/abc/nhat/con-chain-v2/metanode/consensus/metanode/src/node/consensus_node.rs#L1103)

#### Bước 1: Bật Recovery Barrier

```rust
    // Nếu Go đã có state (block > 0), kích hoạt hàng rào bảo vệ
    if go_has_state {
        coordination_hub.activate_recovery_barrier();
        coordination_hub.set_schedule_recovery_pending(true);
        // → TẤT CẢ proposals bị chặn cho đến khi recovery hoàn tất
    }
```

> **Tại sao cần Recovery Barrier?** Nếu không có, node có thể bắt đầu vote/propose ngay khi khởi động nhưng dữ liệu Go chưa bắt kịp mạng → tạo block sai → bị slash hoặc gây fork.

#### Bước 2: Tính go_replay_after

```rust
    // go_replay_after = số commit Go đã xử lý trong epoch hiện tại
    let go_replay_after = if let Some(commit_index) = storage.last_handled_commit_index {
        commit_index  // VD: 1000 (Go đã xử lý 1000 commits)
    } else {
        0  // Fresh start hoặc snapshot restore
    };
```

> `go_replay_after` nói cho CommitProcessor: "Go đã xử lý đến commit #1000. Bỏ qua các commit ≤ 1000, chỉ gửi commit #1001 trở đi."

#### Bước 3: Thiết lập CommitProcessor

```rust
    let commit_processor = CommitProcessor::new(commit_receiver)
        .with_delivery_sender(delivery_tx)        // → gửi block cho Go
        .with_epoch_info(current_epoch)            // epoch hiện tại
        .with_next_expected_index(go_replay_after + 1)  // commit tiếp theo cần xử lý
        .with_go_last_commit_index(go_replay_after)
        // ... nhiều tham số khác
```

#### Bước 4: ═══ STARTUP-SYNC BARRIER ═══

Đây là bước **cực kỳ quan trọng**: Rust tải block thiếu từ mạng và gửi cho Go xử lý, **TRƯỚC KHI** consensus bắt đầu.

```
STARTUP-SYNC Loop:
│
├─ Round 1:
│   ├─ Hỏi tất cả peers: "block cao nhất là bao nhiêu?"
│   │   ├─ Peer A: block 600
│   │   ├─ Peer B: block 605
│   │   └─ Peer C: timeout
│   │
│   ├─ highest_peer_block = 605
│   ├─ local_go_block = 520 (đã kiểm tra ở Phase 1)
│   │
│   ├─ Gap = 85 blocks → CẦN SYNC!
│   │
│   ├─ fetch_blocks_from_peer(521..605) → tải 85 blocks từ Peer B
│   ├─ sync_and_execute_blocks(blocks) → Gửi cho Go xử lý qua UDS
│   │   └─ Go: HandleSyncBlocksRequest()
│   │       ├─ Verify parent-hash continuity
│   │       ├─ Apply backup batches (NOMT state)
│   │       ├─ Save block to database
│   │       └─ Update block number & GEI
│   │
│   └─ Re-query Go: local_go_block = 605 ✅
│
├─ Round 2:
│   ├─ highest_peer_block = 608 (mạng tiến thêm)
│   ├─ local_go_block = 605
│   ├─ Gap = 3 → sync thêm
│   └─ ...
│
└─ Round N:
    ├─ local_go_block >= highest_peer_block
    └─ EXIT LOOP → "Local state in sync" ✅
```

**Code thực tế** (simplified) tại [consensus_node.rs:~1460](file:///home/abc/nhat/con-chain-v2/metanode/consensus/metanode/src/node/consensus_node.rs#L1460):

```rust
    loop {
        // Hỏi peers block cao nhất
        let mut highest_peer_block = 0u64;
        for peer_addr in &config.peer_rpc_addresses {
            if let Ok(info) = query_peer_info(peer_addr).await {
                highest_peer_block = highest_peer_block.max(info.last_block);
            }
        }

        if local_go_block >= highest_peer_block {
            info!("✅ [STARTUP-SYNC] Local state in sync");
            break; // Thoát vòng lặp
        }

        // Tải blocks thiếu
        let blocks = recovery::fetch_blocks_from_peer(
            peer_addr, local_go_block + 1, highest_peer_block
        ).await?;

        // Gửi cho Go xử lý
        recovery::sync_and_execute_blocks(
            &executor_client, blocks, current_epoch, true // is_pre_consensus=true
        ).await?;
    }
```

#### Bước 5: Sau STARTUP-SYNC — Khởi động Consensus

```rust
    // Khởi động ConsensusAuthority (DAG engine)
    let authority = ConsensusAuthority::start(/*...*/);

    // Khởi động BlockDeliveryManager (chuyển block từ Rust → Go)
    // Khởi động CommitProcessor (xử lý commits từ DAG)
    // Khởi động CommitSyncerSupervisor (đồng bộ DAG với peers)
```

---

## 6. Phase 4: Rust — Networking & Epoch Management

### setup_networking()
**File:** [consensus_node.rs:3387](file:///home/abc/nhat/con-chain-v2/metanode/consensus/metanode/src/node/consensus_node.rs#L3387)

Chỉ khởi tạo `ClockSyncManager` cho đồng bộ thời gian NTP. Node cần đồng hồ chính xác để timestamp block đúng.

### setup_epoch_management()
**File:** [consensus_node.rs:3414](file:///home/abc/nhat/con-chain-v2/metanode/consensus/metanode/src/node/consensus_node.rs#L3414)

- **StateTransitionManager**: Quản lý chuyển đổi epoch (thay đổi committee).
- **epoch_monitor**: Chạy nền, poll mỗi 10s, kiểm tra `local_epoch vs network_epoch`:
  - Nếu chậm epoch → step-through từng epoch, tải blocks, chuyển epoch.
  - Nếu cùng epoch → kiểm tra SyncOnly → Validator promotion.
- **fork_detection_check**: Kiểm tra block hash local vs network.

---

## 7. Luồng xử lý block đồng bộ (HandleSyncBlocksRequest)

Khi Rust gửi blocks cho Go qua lệnh `sync_and_execute_blocks`, Go nhận và xử lý tại:

**File:** [unix_socket_handler_epoch.go:1422](file:///home/abc/nhat/con-chain-v2/metanode/execution/executor/unix_socket_handler_epoch.go#L1422)

```
HandleSyncBlocksRequest(blocks)
│
├── Cho mỗi block trong danh sách:
│   │
│   ├── Kiểm tra: Block này đã có trong DB chưa?
│   │   ├── CÓ → Bỏ qua (continue), chỉ update mapping
│   │   └── CHƯA → Tiếp tục xử lý ↓
│   │
│   ├── Kiểm tra Parent-Hash Continuity
│   │   └── block.parentHash phải = hash của block trước đó trong DB
│   │       └── Nếu KHÁC → REJECT (tránh chain corruption)
│   │
│   ├── Khôi phục lastHandledCommitIndex từ block header
│   │   └── Để Rust biết Go đã xử lý đến đâu
│   │
│   ├── Apply Backup Batches (State Changes):
│   │   ├── Account State → NOMT trie
│   │   ├── Smart Contract Storage → NOMT per-contract trie
│   │   ├── Stake State → NOMT stake trie
│   │   ├── Transaction/Receipt → PebbleDB
│   │   └── FullDbLogs → MVM database replay
│   │
│   ├── Lưu block vào database
│   │   ├── Block header + body → Block storage
│   │   ├── Tx hash mapping → Mapping storage
│   │   └── Block number → hash index
│   │
│   └── Update block number & GEI
│
├── Flush NOMT sessions to disk
│
├── Update StateChangelogDB startBlock (nếu cần)
│
└── Return: { synced_count, last_synced_block }
```

### Điểm quan trọng về StateChangelogDB
- Trong quá trình sync (STARTUP-SYNC), **changelog vẫn ghi lại** state changes cho mỗi block.
- Nếu node bị crash trước đó khiến NOMT on-disk bị stale, changelog có thể ghi sai. Đã có fix: `pebble.Sync` đảm bảo changelog flush xuống đĩa ngay, và hệ thống Snapshot Checkpoint bao gồm cả changelog.

---

## 8. SyncOnly Node Restart

SyncOnly node KHÔNG tham gia consensus (không vote, không propose). Nó chỉ theo dõi và đồng bộ blocks.

```
SyncOnly Node Restart
│
├─ Go khởi động (giống Validator)
│
├─ Rust khởi động với RustSyncNode thay vì ConsensusNode
│   ├─ get_last_block_number() → go_last_block (VD: 500)
│   ├─ BlockQueue.new(go_last_block + 1)
│   │
│   └─ sync_loop() [50ms turbo trong 30s đầu]
│       ├─ sync_once()
│       │   ├─ get_last_block_number() → go_block hiện tại
│       │   ├─ fetch_blocks_from_peer(go_block+1 .. go_block+2000)
│       │   ├─ sync_and_execute_blocks(blocks) → Go
│       │   └─ prefetch next batch
│       │
│       ├─ auto_epoch_sync() [khi go_epoch > rust_epoch]
│       │   └─ Chuyển epoch tự động
│       │
│       └─ Lặp vô hạn, bắt kịp cluster
```

**File liên quan:**
- `rust_sync_node/start.rs` — Khởi tạo
- `rust_sync_node/sync_loop.rs` — Vòng lặp sync chính
- `rust_sync_node/fetch.rs` — Tải block từ P2P

---

## 9. Phase State Machine (ConsensusCoordinationHub)

Node trải qua các trạng thái (phase) trước khi được phép tạo block:

```
                         Go has no state (Genesis)
                        ─────────────────────────────► ┌─────────┐
┌──────────────┐                                       │ Healthy │
│ GoSyncing    │  Go has state  ┌──────────────┐       │         │
│              │──────────────► │ DagCatchingUp│       │ • Propose│
│ • STARTUP-   │                │              │       │ • Vote  │
│   SYNC runs  │                │ • Turbo sync │       │ • Normal│
│ • No propose │                │ • No propose │       └─────────┘
└──────────────┘                └──────┬───────┘           ▲
                                       │                    │
                                       ▼                    │
                                ┌────────────────┐          │
                                │ScheduleVerify  │──ok───────┘
                                │ • Verify leader│
                                │   schedule     │
                                └────────────────┘
```

| Phase | Cho phép Propose? | Mô tả |
|-------|:-:|---|
| **GoSyncing** | ❌ | STARTUP-SYNC đang chạy, Go chưa bắt kịp mạng |
| **DagCatchingUp** | ❌ | DAG đang đồng bộ commits từ peers |
| **ScheduleVerifying** | ❌ | Đang xác minh leader schedule từ mạng |
| **Healthy** | ✅ | Node đã sẵn sàng, tham gia consensus bình thường |

---

## 10. Các cơ chế bảo vệ an toàn

### 10a. Anti-Fork: Committee Hash Verification
Trước khi bắt đầu, node tính hash của committee và xác thực với peers. Nếu mismatch → **HALT** (tránh fork).

### 10b. Anti-Equivocation: Recovery Barrier
Node KHÔNG ĐƯỢC propose block cho đến khi trải qua đủ các phase. Nếu propose sớm mà DAG chưa sync → propose block trùng round → bị phạt (slash).

### 10c. GEI Guard
Go kiểm tra GEI trước khi xử lý block: nếu GEI ≤ giá trị đã xử lý → skip (tránh xử lý trùng).

### 10d. Parent-Hash Continuity Check
Mỗi block khi sync phải có `parentHash` trùng với hash block trước đó. Nếu khác → reject block (tránh chain corruption).

### 10e. StateChangelogDB Protection
- **pebble.Sync**: Changelog ghi synchronous xuống ổ đĩa, tránh mất dữ liệu lịch sử khi crash.
- **Checkpoint Snapshot**: Khi tạo snapshot, changelog được checkpoint cùng → node mới có lịch sử đầy đủ.

### 10f. CommitSyncerSupervisor Auto-Restart
CommitSyncer được bọc trong Supervisor. Nếu crash → tự restart với backoff (1s → 2s → 4s → 8s → 10s cap).

### 10g. Cross-Epoch BFT Sync Guard
Khi một node bị chậm hơn so với mạng lưới (`highest_seen_epoch > local_epoch`), tiến trình BFT Sync của `CommitSyncer` sẽ tự động tạm dừng (pause). Điều này nhường quyền ưu tiên tuyệt đối cho `epoch_monitor` và Go block sync để tải và thực thi tuần tự các block lịch sử chuẩn xác từ Peers qua các mốc Epoch, chặn đứng hoàn toàn nguy cơ sinh block rỗng sai timestamp làm rẽ nhánh (Zero-Fork).

### 10h. Unified Millisecond Precision Guard
Toàn bộ hệ thống thống nhất độ chính xác thời gian mili-giây (ms), loại bỏ hoàn toàn các phép làm tròn giây `(ts / 1000) * 1000`. Điều này triệt tiêu hoàn toàn sự lệch lạc timestamp giữa node chạy trực tiếp và node đồng bộ đuổi bắt, đảm bảo `BlockHash` được tạo ra luôn đồng nhất tuyệt đối trên toàn mạng lưới.

---

## 11. Các thuật ngữ quan trọng

| Thuật ngữ | Giải thích |
|---|---|
| **GEI** (Global Execution Index) | Con số đếm tuần tự cho TẤT CẢ commits, dùng để đồng bộ giữa Rust và Go |
| **Epoch** | Một "nhiệm kỳ" của committee. Committee có thể thay đổi mỗi epoch |
| **epoch_base_exec_index** | GEI tại điểm bắt đầu epoch hiện tại |
| **Committee** | Danh sách validator có quyền vote/propose trong epoch |
| **DAG** | Directed Acyclic Graph — cấu trúc dữ liệu lưu trữ biểu quyết consensus |
| **NOMT** | Near-Optimal Merkle Trie — cây Merkle lưu trạng thái account |
| **STARTUP-SYNC** | Cơ chế đồng bộ block từ peers **TRƯỚC** khi consensus bắt đầu |
| **CommitProcessor** | Component nhận commits từ DAG và chuyển thành blocks cho Go |
| **BlockDeliveryManager** | Component trung gian chuyển block từ Rust → Go qua channel |
| **go_replay_after** | Số commit Go đã xử lý, dùng để skip commits đã cũ |
| **UDS** | Unix Domain Socket — kênh giao tiếp local giữa Rust và Go |
| **Fork** | Mạng bị chia đôi do 2+ nodes tạo block khác nhau cùng vị trí |
| **Equivocation** | Node tạo 2 block khác nhau cùng round → bị phạt (slash) |
| **StateChangelogDB** | Database PebbleDB lưu lịch sử thay đổi state theo từng block |
| **Recovery Barrier** | Cơ chế chặn proposals cho đến khi node hoàn tất recovery |
