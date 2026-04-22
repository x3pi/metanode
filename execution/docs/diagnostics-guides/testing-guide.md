# 📋 Hướng dẫn Test toàn bộ hệ thống MTN

> Tài liệu mô tả quy trình test hệ thống MTN (mtn-simple-2025 + mtn-consensus).
> Cập nhật: 2026-03-05

---

## Tổng quan kiến trúc

```
┌──────────────────────────┐     UDS/Protobuf     ┌──────────────────────────┐
│   mtn-consensus (Rust)   │ ◄──────────────────► │   mtn-simple-2025 (Go)   │
│   Consensus Layer        │                      │   Executor Layer         │
│   - DAG-BFT (Mysticeti)  │                      │   - Block Processing     │
│   - Transaction Pool     │                      │   - State Management     │
│   - Epoch Management     │                      │   - RPC Server           │
│   - Network (TCP)        │                      │   - Smart Contracts      │
└──────────────────────────┘                      └──────────────────────────┘
```

---

## Quy trình Test — 5 Tầng

| Tầng | Loại test | Cần cluster? | Thời gian |
|------|-----------|-------------|-----------|
| 1 | Unit Test (Go + Rust) | ❌ | ~50s |
| 2 | Integration Test | ❌ | ~2s |
| 3 | System Test | ✅ | ~1 phút |
| 4 | Performance Test (TPS) | ✅ | ~5-30 phút |
| 5 | Chaos / Resilience | ✅ | ~10-30 phút |

---

## Tầng 1: Unit Test

### Go (mtn-simple-2025)

```bash
# Chạy toàn bộ
cd /path/to/mtn-simple-2025
go test ./... -v -count=1 -timeout 300s

# Chạy từng nhóm
go test ./cmd/simple_chain/processor/... -v     # Block processor (9 files)
go test ./pkg/bls/... -v                        # BLS crypto
go test ./pkg/trie/... -v                       # Merkle trie
go test ./pkg/storage/... -v                    # Storage layer
go test ./pkg/state/... -v                      # State management
go test ./pkg/blockchain/... -v                 # Blockchain + Epoch
go test ./pkg/logger/... -bench=. -benchmem     # Logger benchmark
```

**28 test files** bao gồm:

| Nhóm | Files | Mô tả |
|------|-------|--------|
| Block Processor | 9 files trong `cmd/simple_chain/processor/` | Xử lý block, transaction, subscription, TPS tracking |
| BLS Crypto | `pkg/bls/bls_test.go`, `key_pair_test.go` + 3 blst tests | BLS signature, aggregate sign, key pair |
| Trie/State | `pkg/trie/hasher_test.go`, `node/encoding_test.go`, `node_test.go` | Merkle trie operations |
| Storage | `pkg/storage/memory_db_test.go`, `memory_db_iterator_test.go` | In-memory database |
| Blockchain | `pkg/blockchain/chain_state_epoch_test.go` | Epoch data serialization, advance logic, persistence |
| Khác | `utilities_test.go`, `grouptxns_test.go`, `logger_test.go`, `leader_schedule_test.go` | Utilities |

### Rust (mtn-consensus)

```bash
cd /path/to/mtn-consensus/metanode
cargo test --workspace

# Test cụ thể
cargo test -p meta-consensus-core         # Core consensus (committer, DAG, network, storage)
cargo test -p meta-consensus-config       # Committee, parameters
```

**138 tests** bao gồm: committer tests (base, pipelined, universal, randomized), DAG tests, network tests, storage tests, block queue, sync loop, epoch transitions, circuit breaker, fetch tests.

---

## Tầng 2: Integration Test

### Epoch Transition Integration (40 test cases)

```bash
go test ./executor/... -v -run TestEpochTransition -timeout 120s
```

Test giao tiếp Go ↔ Rust qua Unix Domain Socket (UDS), gồm 5 nhóm:

| Nhóm | Tests | Mô tả |
|------|-------|--------|
| Happy-Path | `HappyPath`, `MultiEpochSequential` | Epoch advance cơ bản |
| Error Handling | `BackwardRejected`, `DuplicateIdempotent`, `InitialEpochIsZero`, `UnknownRequest` | Edge cases |
| Connection Resilience | `ConnectionDrop_ServerSurvives`, `Reconnect_StatePersistedAcrossConnections` | Connection drop/reconnect |
| Concurrency | `ConcurrentAdvances`, `MultipleClientsReadWrite` | Concurrent access |
| Socket Lifecycle | Socket create/cleanup | UDS lifecycle |

---

## Tầng 3: System Test

> ⚠️ **Yêu cầu**: Cluster 4 nodes đang chạy

### Kiểm tra hệ thống cơ bản

```bash
# Test nhanh ports + connectivity
cd /path/to/mtn-consensus/metanode
bash simple_test.sh

# Test đầy đủ (4 bước: nodes, ports, connectivity, transactions)
bash scripts/test_system.sh
```

### Fork Safety — Block Hash Checker

```bash
cd /path/to/mtn-simple-2025/cmd/tool/block_hash_checker

# Quét 1 lần
go run . --nodes "master=http://localhost:8747,node4=http://localhost:10748" --from 1

# Giám sát liên tục (khuyến nghị)
go run . --watch \
  --nodes "master=http://localhost:8747,node4=http://localhost:10748" \
  --interval 10s --check-last 10
```

### Validator Test Suite (5 Phases)

```bash
cd /path/to/mtn-simple-2025/cmd/tool/validator_test_suite
go run . --master-rpc http://localhost:8747 --node4-rpc http://localhost:10748
```

| Phase | Nội dung | Kiểm tra |
|-------|---------|----------|
| 1 - Baseline | Hệ thống ổn định | Hash match giữa nodes |
| 2 - Deregister | Hủy validator | Chain vẫn consensus |
| 3 - Restart Node4 | Restart node | Đồng bộ lại thành công |
| 4 - Register | Đăng ký lại validator | Epoch transition đúng |
| 5 - Restart Node1 | Restart node khác | Fork safety |

---

## Tầng 4: Performance Test

### TPS Blast

```bash
cd /path/to/mtn-simple-2025/cmd/tool/tps_blast

# Chạy 1 lần
go run . -rpc http://localhost:8545 -duration 30s -workers 100

# Chạy 30 lần lấy trung bình
bash run_30_tests.sh

# Load trên nhiều nodes
bash run_multinode_load.sh
```

### RPC Stress Test

```bash
cd /path/to/mtn-simple-2025/cmd/tool/spam_rpc
go run .     # 1000 workers, 30s, đo RPC TPS
```

### RPC Benchmark

```bash
cd /path/to/mtn-simple-2025/cmd/tool/rpc_bench
go run .     # Benchmark từng RPC method
```

### Analysis Scripts

```bash
cd /path/to/mtn-consensus/metanode/scripts/analysis

bash calculate_real_tps.sh        # Tính TPS thực từ logs
bash analyze_bad_nodes.sh         # Phân tích node bất thường
bash analyze_epoch_transition.sh  # Phân tích epoch transition
bash analyze_stuck_system.sh      # Phân tích system bị stuck
bash check_epoch_status.sh        # Kiểm tra trạng thái epoch
```

---

## Tầng 5: Chaos / Resilience Test

| Test | Cách thực hiện | Kiểm tra |
|------|---------------|----------|
| Kill node đột ngột | `kill -9 <pid>` 1 node bất kỳ | 3 node còn lại vẫn consensus |
| Restart node | Restart qua validator_test_suite | Đồng bộ và recovery |
| Deregister validator | validator_test_suite Phase 2 | Chain không fork |
| Connection drop | Epoch integration test | Server survive, reconnect OK |

---

## Monitoring Dashboard

```bash
cd /path/to/mtn-consensus/metanode/monitoring
bash start_monitor.sh
# Mở browser: http://localhost:5050
```

---

## Checklist chạy test đầy đủ

```bash
# ═══ OFFLINE TESTS (không cần cluster) ═══

# 1. Go Unit Test
go test ./... -count=1 -timeout 300s 2>&1 | tee /tmp/go_test.log

# 2. Rust Unit Test
cd metanode && cargo test --workspace 2>&1 | tee /tmp/rust_test.log

# 3. Integration Test
go test ./executor/... -v -run TestEpochTransition -timeout 120s

# ═══ ONLINE TESTS (cần cluster đang chạy) ═══

# 4. System Test
bash scripts/test_system.sh

# 5. Fork Safety (chạy nền)
go run cmd/tool/block_hash_checker/. --watch --nodes "..." &

# 6. TPS Benchmark
bash cmd/tool/tps_blast/run_30_tests.sh

# 7. Validator Lifecycle
go run cmd/tool/validator_test_suite/.
```

> **Lưu ý**: Bước 5 (block_hash_checker) nên chạy **song song** với bước 6 (tps_blast) để phát hiện fork dưới tải cao.

---

## Kết quả test gần nhất (2026-03-05)

| Test | Kết quả |
|------|---------|
| Go Unit Test (processor) | ✅ PASS (6.3s) |
| Go Unit Test (trie, storage, state, common, logger, poh, grouptxns) | ✅ PASS |
| Go Unit Test (blockchain) | ✅ PASS (0.033s) |
| Go Unit Test (bls) | ✅ PASS (42s) |
| Rust Unit Test | ✅ 138/138 PASS (8s) |
| Epoch Integration Test | ✅ PASS (2.1s) |
