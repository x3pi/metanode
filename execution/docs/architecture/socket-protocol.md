# Socket Protocol: Go ↔ Rust Communication

## Overview

The system uses a **dual-socket architecture** for communication between the Rust consensus layer (MetaNode) and the Go execution layer (Master). All communication uses **Protocol Buffers** for serialization.

```
┌─────────────────────┐         ┌─────────────────────┐
│   Rust MetaNode      │         │   Go Master          │
│   (Consensus)        │         │   (Execution)        │
│                      │         │                      │
│  ExecutorClient ─────┼─ [1] ──▶│  Listener            │
│   (block_sending.rs) │  SEND   │  (listener.go)       │
│                      │         │                      │
│  ExecutorClient ─────┼─ [2] ──▶│  UnixSocket          │
│   (rpc_queries.rs)   │  RPC    │  (unix_socket_       │
│                      │         │   handler.go)        │
└─────────────────────┘         └─────────────────────┘
```

### Socket 1: Send Socket (One-Way)
- **Direction**: Rust → Go (one-way)
- **Purpose**: Send committed blocks from consensus to executor
- **Message Type**: `CommittedEpochData` (from `executor.proto`)
- **Framing**: **Uvarint length prefix** + protobuf payload
- **Go Receiver**: `executor/listener.go` → `handleConnection()`

### Socket 2: RPC Socket (Request/Response)
- **Direction**: Rust ↔ Go (bidirectional)
- **Purpose**: Query Go state, epoch management, transition handoff
- **Message Types**: `Request` / `Response` oneof (from `validator_rpc.proto`)
- **Framing**: **4-byte BigEndian length prefix** + protobuf payload
- **Go Handler**: `executor/unix_socket_handler.go` → `RequestHandler`

---

## Proto Files

### `executor.proto` — Block Data

```protobuf
message TransactionExe {
    bytes digest = 1;      // Raw transaction data (protobuf bytes)
    uint32 worker_id = 2;  // (unused, set to 0)
}

message CommittedBlock {
    uint64 epoch = 1;
    uint64 height = 2;                       // commit_index
    repeated TransactionExe transactions = 3;
}

message CommittedEpochData {
    repeated CommittedBlock blocks = 1;
    uint64 global_exec_index = 2;    // Globally unique, monotonically increasing
    uint32 commit_index = 3;         // Epoch-local commit index
    uint64 epoch = 4;
    uint64 commit_timestamp_ms = 5;  // Consensus median timestamp (ms since epoch)
    uint32 leader_author_index = 6;  // Leader authority index in committee
    bytes leader_address = 7;        // 20-byte Ethereum address of leader
}
```

### `validator_rpc.proto` — RPC Commands

14 RPC command types via `oneof` in `Request`:

| Field# | Request Type | Response Type | Purpose |
|--------|------------|--------------|---------|
| 1 | `BlockRequest` | `ValidatorList` | Get block (legacy) |
| 2 | `StatusRequest` | `ServerStatus` | Get status (legacy) |
| 3 | `GetActiveValidatorsRequest` | `ValidatorInfoList` | Get active validators |
| 4 | `GetValidatorsAtBlockRequest` | `ValidatorInfoList` | Get validators at block N |
| 5 | `GetLastBlockNumberRequest` | `LastBlockNumberResponse` | Get last committed block |
| 6 | `GetCurrentEpochRequest` | `GetCurrentEpochResponse` | Get current epoch number |
| 7 | `GetEpochStartTimestampRequest` | `GetEpochStartTimestampResponse` | Get epoch timestamp |
| 8 | `AdvanceEpochRequest` | `AdvanceEpochResponse` | Advance to new epoch |
| 9 | `GetEpochBoundaryDataRequest` | `EpochBoundaryData` | Get unified epoch boundary |
| 10 | `SetConsensusStartBlockRequest` | `SetConsensusStartBlockResponse` | Consensus start handoff |
| 11 | `SetSyncStartBlockRequest` | `SetSyncStartBlockResponse` | Sync start handoff |
| 12 | `WaitForSyncToBlockRequest` | `WaitForSyncToBlockResponse` | Wait for sync completion |
| 20 | `NotifyEpochChangeRequest` | `NotifyEpochChangeResponse` | Epoch change notification |
| 21 | `GetBlocksRangeRequest` | `GetBlocksRangeResponse` | Batch block fetch |
| 22 | `SyncBlocksRequest` | `SyncBlocksResponse` | Push blocks for sync |

---

## Framing Protocols

### Send Socket: Uvarint Framing

Used by Rust `block_sending.rs` → Go `listener.go`:

```
┌──────────────────────┬─────────────────────┐
│  Uvarint Length (1-9B)│  Protobuf Payload   │
│  (variable length)    │  (CommittedEpochData)│
└──────────────────────┴─────────────────────┘
```

- **Rust writes**: `write_uvarint()` from `persistence.rs` (matches Go `encoding/binary` format)
- **Go reads**: `binary.ReadUvarint(reader)` in `listener.go`
- Each byte uses 7 data bits + 1 continuation bit (MSB)

### RPC Socket: 4-Byte BigEndian Framing

Used by Rust `rpc_queries.rs` → Go `unix_sokcet_protocol.go`:

```
┌──────────────────────┬─────────────────────┐
│  Length (4 bytes BE)  │  Protobuf Payload   │
│  binary.BigEndian     │  (Request/Response) │
└──────────────────────┴─────────────────────┘
```

- **Go writes**: `binary.BigEndian.PutUint32` in `WriteMessage()`
- **Go reads**: `binary.BigEndian.Uint32` in `ReadMessage()`
- **Rust writes**: `write_all(&(len as u32).to_be_bytes())`
- **Rust reads**: `read_exact(&mut [0u8; 4])` → `u32::from_be_bytes`

---

## Connection Lifecycle

### Startup Sequence

```
1. Go Master starts first, creates socket files:
   - Send socket: /tmp/executor0.sock (or TCP address)
   - RPC socket:  /tmp/validator_socket.sock (or TCP address)

2. Rust MetaNode starts, connects lazily:
   - ExecutorClient::connect()      → send socket
   - ExecutorClient::connect_request() → RPC socket

3. Rust initializes from Go state:
   - get_last_block_number()  → set next_expected_index
   - get_current_epoch()      → set current epoch
   - get_validators_at_block(0) → load initial committee
```

### Reconnection

```
On send failure:
  1. Clear connection: *conn_guard = None
  2. Retry connect() → SocketStream::connect()
  3. Retry send (with timeout)
  4. On timeout/failure → block stays in buffer, retried on next commit

On RPC failure:
  1. Clear request_connection: *conn_guard = None
  2. Circuit breaker tracks failures (per-method)
  3. After threshold → CircuitState::Open (reject for cooldown)
  4. After cooldown → CircuitState::HalfOpen (probe with retry)
  5. On success → CircuitState::Closed (reset counters)
```

### Socket Types

Supports both Unix Domain Sockets (local) and TCP (remote):

```
SocketAddress::parse():
  "/tmp/socket.sock"         → Unix socket
  "unix:///tmp/socket.sock"  → Unix socket
  "192.168.1.100:9001"       → TCP socket
  "tcp://192.168.1.100:9001" → TCP socket
```

---

## Block Sending Flow

### Rust → Go Block Flow

```
send_committed_subdag()
  │
  ├── [1] Check enabled + can_commit
  ├── [2] Replay protection: skip if global_exec_index < next_expected
  ├── [3] Dual-stream dedup: skip if already sent
  ├── [4] convert_to_protobuf(): CommittedSubDag → CommittedEpochData bytes
  │     ├── Filter out SystemTransaction (BCS format)
  │     ├── Verify protobuf validity (from_address check)
  │     ├── Sort transactions by hash (fork-safety)
  │     └── Sort blocks by height (deterministic ordering)
  ├── [5] Buffer commit: send_buffer.insert(global_exec_index, data)
  └── [6] flush_buffer()
        ├── Connect if needed
        ├── If large gap (>100 blocks): sync with Go (get_last_block_number)
        └── Send all consecutive blocks:
              ├── send_block_data(): uvarint_len + protobuf_bytes
              ├── Update next_expected_index += 1
              ├── Persist last_sent_index (atomic write + rename)
              ├── Track in sent_indices (dedup)
              └── Periodic Go verification (every 10 blocks)
```

### Go Receive Flow

```
listener.go: handleConnection()
  │
  ├── [1] binary.ReadUvarint(reader) → msgLen
  ├── [2] io.ReadFull(reader, buf) → protobuf bytes
  ├── [3] proto.Unmarshal(buf, &epochData)
  └── [4] dataChan <- &epochData (10,000 buffer)

block_processor_network.go: processRustEpochData()
  │
  ├── [1] Receive from dataChan
  ├── [2] Sequential ordering: pendingBlocks map for gaps
  ├── [3] Create block from CommittedEpochData:
  │     ├── Use commit_timestamp_ms (not time.Now())
  │     ├── Use leader_address (not local lookup)
  │     └── Use global_exec_index as block number
  ├── [4] Execute transactions (EVM)
  ├── [5] Commit block to storage
  └── [6] Handle epoch transitions (advance_epoch)
```

---

## Epoch Transition Flow

```
Epoch N ending → Epoch N+1

[Rust] Consensus detects epoch boundary
  │
  ├── [1] Rust calls advance_epoch(N+1, timestamp_ms, boundary_block)
  │     → Go stores: epoch=N+1, timestamp, boundary_block
  │
  ├── [2] Rust calls get_epoch_boundary_data(N+1)
  │     → Go returns: validators snapshot at boundary_block
  │
  ├── [3] Rust builds new committee from validators
  │
  ├── [4] For mode transitions:
  │     ├── Validator → SyncOnly:
  │     │     └── set_sync_start_block(last_consensus_block)
  │     └── SyncOnly → Validator:
  │           ├── wait_for_sync_to_block(target_block)
  │           └── set_consensus_start_block(next_block)
  │
  └── [5] New epoch begins with new committee
```

---

## Error Handling

### Timeouts
| Operation | Timeout | Location |
|-----------|---------|----------|
| Block send | 10s | `block_sending.rs` `SEND_TIMEOUT` |
| Read deadline (header) | 2 min | `listener.go` read deadline |
| Read deadline (body) | 30s | `listener.go` body read |
| dataChan send | 30s | `listener.go` send timeout |
| TCP connect | configurable | `socket_stream.rs` `timeout_secs` |

### Crash Recovery
- **Rust**: Persists `last_sent_index` + `commit_index` to `executor_state/last_sent_index.bin` (atomic write + rename)
- **Go**: Persists block data to RocksDB, last block number via `StorageManager`
- On restart: Rust loads persisted index, syncs with Go via `get_last_block_number()`

### Circuit Breaker (RPC)
- **Per-method** state machine: Closed → Open → HalfOpen → Closed
- **Threshold**: configurable failures before opening
- **Cooldown**: configurable duration before half-open probe
- Location: `rpc_circuit_breaker.rs`

---

## Source File Reference

### Rust (MetaNode)
| File | Purpose |
|------|---------|
| `executor_client/mod.rs` | ExecutorClient struct, connect/reconnect |
| `executor_client/block_sending.rs` | Block buffering + sending (703 lines) |
| `executor_client/rpc_queries.rs` | RPC queries to Go (645 lines) |
| `executor_client/transition_handoff.rs` | Epoch transition APIs (396 lines) |
| `executor_client/socket_stream.rs` | Unix/TCP socket abstraction (239 lines) |
| `executor_client/persistence.rs` | Crash recovery (143 lines) |
| `rpc_circuit_breaker.rs` | Circuit breaker pattern (465 lines) |

### Go (Master)
| File | Purpose |
|------|---------|
| `executor/listener.go` | Receives blocks from Rust (191 lines) |
| `executor/unix_socket_handler.go` | RPC request handlers (922 lines) |
| `executor/unix_sokcet_protocol.go` | Framing: 4-byte BigEndian (59 lines) |
| `executor/socket_abstraction.go` | Unix/TCP socket abstraction |
| `processor/block_processor_network.go` | Processes received blocks |

### Proto Definitions
| File | Messages |
|------|----------|
| `proto/executor.proto` | `CommittedEpochData`, `CommittedBlock`, `TransactionExe` |
| `proto/validator_rpc.proto` | `Request`, `Response`, all RPC types (278 lines) |
| `proto/transaction.proto` | `Transaction`, `CallData`, etc. (173 lines) |
