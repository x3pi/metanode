# Phân tích luồng giao dịch và Fork-Safety

## Tổng quan

Hệ thống có 3 luồng chính:
1. **Go Sub → Rust**: Gửi transactions từ Go sub nodes đến Rust consensus
2. **Rust → Go Master**: Gửi committed blocks từ Rust đến Go master executor
3. **Epoch Transition**: Chuyển đổi epoch mượt mà không bị fork

## 1. Luồng Go Pool → Rust FFI

### 1.1. Go Pool Forward transactions

**File**: `cmd/simple_chain/processor/block_processor_txs.go` (TxsProcessor2)

**Luồng**:
```
Go Pool (TxQueue) → batchTxWorkers → FFI Call → Rust TransactionReceiver
```

**Chi tiết**:
1. **batchTxWorkers**: Sử dụng goroutines thu thập các giao dịch hợp lệ định kỳ.
2. **FFI Call**: Truyền block dữ liệu qua bộ nhớ (CGo), loại bỏ hoàn toàn Protobuf framing overhead qua TCP/UDS.
3. **Retry mechanism**: Có retry queue/channel cho các batches bị reject do epoch transition.
4. **No UDS/TCP**: Truyền dữ liệu trực tiếp trong cùng một tiến trình hệ thống, đảm bảo an toàn bộ nhớ tĩnh trong quy trình nguyên khối.

**Fork-Safety**: ✅ Transactions được gửi qua FFI, không có race condition

### 1.2. Rust nhận transactions

**File**: `metanode/src/network/tx_receiver.rs`

**Luồng**:
```
FFI Call → TransactionReceiver → pending_transactions_queue → Consensus Core
```

**Fork-Safety**: ✅ Consensus core đảm bảo transactions được xử lý theo thứ tự

## 2. Luồng Rust → Go Unified Executor

### 2.1. Rust tạo committed blocks

**File**: `metanode/src/commit_processor.rs`

**Luồng**:
```
Consensus Core → CommittedSubDag → CommitProcessor → ExecutorClient
```

**Chi tiết**:
1. **Commit Processor**: Nhận `CommittedSubDag` từ consensus
2. **Global Exec Index Calculation**: 
   ```rust
   global_exec_index = last_global_exec_index + 1
   ```
   - **CRITICAL**: `global_exec_index` là sequential block number, không phụ thuộc vào epoch
   - Đảm bảo tất cả nodes tính cùng giá trị cho cùng commit
3. **Process Commit**: Gọi `executor_client.send_committed_subdag()`

**Fork-Safety**: ✅ `global_exec_index` được tính deterministic từ `last_global_exec_index`

### 2.2. Executor Client gửi blocks

**File**: `metanode/src/node/executor_client/mod.rs` (hoặc `commit_processor.rs`)

**Luồng**:
```
send_committed_subdag() → convert_to_protobuf() → buffer.insert() → flush_buffer() → call_golang_callback() → FFI Queue
```

**Chi tiết**:
1. **Sequential Buffering**: 
   - Blocks được thêm vào `send_buffer` (BTreeMap<u64, ...>)
   - Chỉ gửi blocks tuần tự từ `next_expected_index`
   - Đảm bảo Go nhận blocks theo đúng thứ tự

2. **Duplicate Detection**:
   - Kiểm tra duplicate `global_exec_index` trước khi insert
   - Nếu duplicate: so sánh epoch + commit_index để xác định có phải cùng commit không
   - Nếu cùng commit: skip (đã có trong buffer)
   - Nếu khác commit: log error nhưng vẫn overwrite để đảm bảo transactions được execute

3. **Flush Buffer**:
   - Gửi tất cả consecutive blocks từ `next_expected_index`
   - Nếu có gap: đợi missing blocks (không skip)
   - Đảm bảo sequential ordering

**Fork-Safety**: ✅ 
- Blocks được gửi tuần tự theo `global_exec_index`
- Duplicate detection đảm bảo không gửi cùng commit 2 lần
- Sequential buffering đảm bảo Go nhận blocks đúng thứ tự

### 2.3. Go Unified Node nhận blocks

**File**: `executor/listener.go` và `cmd/simple_chain/processor/block_processor_core.go`

**Luồng**:
```
FFI Callback → Listener → dataChan → Block Processor → Process Block
```

**Chi tiết**:
1. **Sequential Processing**:
   - Chỉ xử lý block với `global_exec_index == nextExpectedGlobalExecIndex`
   - Out-of-order blocks được lưu vào `pendingBlocks` map
   - Đợi missing blocks trước khi xử lý

2. **Fork-Safety Checks**:
   - **Duplicate Detection**: Kiểm tra nếu block đã được xử lý
   - **Empty Block Replacement**: Nếu nhận commit có transactions với cùng `global_exec_index` như empty block chưa commit, replace empty block
   - **Out-of-Order Handling**: Lưu out-of-order blocks và xử lý khi đến lượt

3. **Block Number Assignment**:
   ```go
   currentBlockNumber = globalExecIndex
   ```
   - **CRITICAL**: Block number = `global_exec_index`
   - Đảm bảo tất cả nodes assign cùng block number cho cùng commit

**Fork-Safety**: ✅
- Sequential processing đảm bảo blocks được xử lý đúng thứ tự
- Block number = `global_exec_index` đảm bảo deterministic block numbering
- Empty block replacement đảm bảo không mất transactions

## 3. Epoch Transition

### 3.1. Trigger Epoch Transition

**File**: `metanode/src/node.rs` (transition_to_epoch_from_system_tx)

**Luồng**:
```
EndOfEpoch System Transaction → Commit Processor → Epoch Transition Callback → transition_to_epoch_from_system_tx()
```

**Chi tiết**:
1. **EndOfEpoch Detection**: Commit processor phát hiện EndOfEpoch transaction trong commit
2. **Calculate Last Global Exec Index**:
   ```rust
   last_global_exec_index_at_transition = calculate_global_exec_index(
       old_epoch,
       commit_index,
       last_global_exec_index
   )
   ```
   - **CRITICAL**: Sử dụng `commit_index` từ EndOfEpoch transaction (deterministic)
   - Tất cả nodes sẽ tính cùng `last_global_exec_index_at_transition`

3. **Fetch New Committee**: Lấy validators từ Go state tại block `last_global_exec_index_at_transition`

4. **Create New Commit Processor**: 
   - Tạo commit processor mới với epoch mới
   - **CRITICAL**: Executor client được tạo với `initial_next_expected_index = last_global_exec_index_at_transition + 1`

**Fork-Safety**: ✅
- `last_global_exec_index_at_transition` được tính deterministic từ EndOfEpoch commit
- Tất cả nodes sẽ có cùng `last_global_exec_index_at_transition`
- Executor client được reset với đúng `next_expected_index`

### 3.2. Executor Client Initialization

**File**: `metanode/src/executor_client.rs` (initialize_from_go)

**Luồng**:
```
New Executor Client → initialize_from_go() → Query Go for last_block_number → Update next_expected_index
```

**Chi tiết**:
1. **Query Go State**: Gọi `get_last_block_number()` để lấy last block number từ Go
2. **Sync with Go State**:
   ```rust
   go_next_expected = last_block_number + 1
   if go_next_expected > current_next_expected {
       // Go is ahead - update to Go's state
       next_expected_index = go_next_expected
       // Clear buffered commits that Go has already processed
   }
   ```
   - **CRITICAL**: Sync với Go state để prevent duplicate commits
   - Nếu Go đã xử lý commits, update `next_expected_index` và clear buffer

**Fork-Safety**: ✅
- Sync với Go state đảm bảo không gửi duplicate commits
- Clear buffer đảm bảo không gửi commits đã được xử lý

### 3.3. New Epoch Block Creation

**File**: `metanode/src/commit_processor.rs`

**Luồng**:
```
New Commit Processor → First Commit (commit_index=1) → calculate_global_exec_index() → global_exec_index = last_global_exec_index + 1
```

**Chi tiết**:
1. **First Commit in New Epoch**:
   - `commit_index = 1` (mtn-consensus commit_index bắt đầu từ 1 mỗi epoch)
   - `global_exec_index = last_global_exec_index_at_transition + 1`
   - **CRITICAL**: Block number tiếp tục tuần tự, không reset

2. **Sequential Block Numbering**:
   ```
   Epoch 0: blocks 1, 2, 3, ..., N
   Epoch 1: blocks N+1, N+2, N+3, ...
   ```
   - Block numbers liên tục qua epochs
   - Không có gap hoặc reset

**Fork-Safety**: ✅
- Block numbers tiếp tục tuần tự qua epochs
- Tất cả nodes tính cùng `global_exec_index` cho cùng commit
- Không có fork do block numbering

## 4. Fork-Safety Mechanisms

### 4.1. Global Exec Index

**Formula**: `global_exec_index = last_global_exec_index + 1`

**Đảm bảo**:
- ✅ Deterministic: Tất cả nodes với cùng `last_global_exec_index` tính cùng giá trị
- ✅ Sequential: Block numbers liên tục, không có gap
- ✅ Cross-epoch: Block numbers tiếp tục qua epochs

### 4.2. Sequential Processing

**Rust Side**:
- ✅ Executor client chỉ gửi blocks tuần tự từ `next_expected_index`
- ✅ Buffer mechanism đảm bảo blocks được gửi đúng thứ tự

**Go Side**:
- ✅ Block processor chỉ xử lý block với `global_exec_index == nextExpectedGlobalExecIndex`
- ✅ Out-of-order blocks được lưu và xử lý khi đến lượt

### 4.3. Duplicate Detection

**Rust Side**:
- ✅ Kiểm tra duplicate `global_exec_index` trước khi insert vào buffer
- ✅ So sánh epoch + commit_index để xác định có phải cùng commit

**Go Side**:
- ✅ Kiểm tra duplicate block trước khi xử lý
- ✅ Empty block replacement đảm bảo không mất transactions

### 4.4. Epoch Transition

**Đảm bảo**:
- ✅ `last_global_exec_index_at_transition` được tính deterministic từ EndOfEpoch commit
- ✅ Executor client được reset với đúng `next_expected_index`
- ✅ Block numbers tiếp tục tuần tự qua epochs

## 5. Potential Issues và Solutions

### 5.1. Issue: Consensus Shutdown During Epoch Transition

**Vấn đề**: Consensus có thể shutdown trước khi commit processor mới được tạo

**Giải pháp**: 
- ✅ Đảm bảo commit processor mới được tạo TRƯỚC KHI consensus shutdown
- ✅ Đảm bảo consensus không shutdown khi đang có pending commits

### 5.2. Issue: Duplicate Global Exec Index

**Vấn đề**: Có thể có duplicate `global_exec_index` nếu `last_global_exec_index` không được update đúng cách

**Giải pháp**:
- ✅ Update `shared_last_global_exec_index` SYNCHRONOUSLY sau khi gửi block thành công
- ✅ Duplicate detection trong executor client
- ✅ So sánh epoch + commit_index để xác định có phải cùng commit

### 5.3. Issue: Out-of-Order Blocks

**Vấn đề**: Blocks có thể đến không đúng thứ tự

**Giải pháp**:
- ✅ Sequential buffering trong executor client
- ✅ Pending blocks buffer trong Go block processor
- ✅ Chỉ xử lý blocks tuần tự

### 5.4. Issue: Missing Blocks

**Vấn đề**: Blocks có thể bị mất trong quá trình gửi

**Giải pháp**:
- ✅ Retry mechanism trong executor client
- ✅ Timeout monitoring trong Go block processor
- ✅ Buffer mechanism đảm bảo blocks không bị mất

## 6. Checklist Fork-Safety

### 6.1. Block Numbering
- ✅ `global_exec_index` được tính deterministic
- ✅ Block numbers liên tục qua epochs
- ✅ Tất cả nodes assign cùng block number cho cùng commit

### 6.2. Sequential Processing
- ✅ Rust gửi blocks tuần tự
- ✅ Go xử lý blocks tuần tự
- ✅ Out-of-order blocks được handle đúng cách

### 6.3. Epoch Transition
- ✅ `last_global_exec_index_at_transition` được tính deterministic
- ✅ Executor client được reset với đúng `next_expected_index`
- ✅ Block numbers tiếp tục tuần tự

### 6.4. Duplicate Prevention
- ✅ Duplicate detection trong Rust
- ✅ Duplicate detection trong Go
- ✅ Empty block replacement đảm bảo không mất transactions

## 7. Kết luận

Hệ thống đã được thiết kế với các cơ chế fork-safety mạnh mẽ:

1. **Deterministic Block Numbering**: `global_exec_index` được tính deterministic, đảm bảo tất cả nodes có cùng block numbers
2. **Sequential Processing**: Blocks được gửi và xử lý tuần tự, đảm bảo không có race condition
3. **Epoch Transition**: Chuyển đổi epoch mượt mà với block numbers tiếp tục tuần tự
4. **Duplicate Prevention**: Có cơ chế detect và handle duplicates
5. **Out-of-Order Handling**: Có buffer mechanism để handle out-of-order blocks

**Tất cả các cơ chế này đảm bảo hệ thống không bị fork và giao dịch được xử lý mượt mà trong cấu trúc Monolithic (Go Unified Node ↔ Rust FFI).**
