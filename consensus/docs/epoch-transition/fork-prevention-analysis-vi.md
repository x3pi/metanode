# Phân Tích Ngăn Chặn Fork Khi Chuyển Đổi Full Node -> Validator

Tài liệu này phân tích các rủi ro gây Fork khi một node chuyển từ chế độ SyncOnly (Full Node) sang Validator và các cơ chế an toàn đã được triển khai để khắc phục. Ngoài ra, tài liệu cung cấp cấu trúc dữ liệu Block được gửi từ Rust sang Go.

## 1. Vấn Đề Cốt Lõi: "The Continuity Gap"

Khi một node đang ở chế độ SyncOnly, nó chỉ là một **Observer** (Người quan sát). Nó nhận các block đã được mạng lưới consensus và chuyển cho Go Exec layer.
Khi chuyển sang Validator, nó trở thành **Proposer** (Người đề xuất).

**Rủi ro Fork xảy ra khi:**
1.  Mạng lưới (Peers) đã đạt đến Block `N` (thuộc epoch cũ hoặc đầu epoch mới).
2.  Node local mới chỉ sync đến Block `N-5`.
3.  Node chuyển thành Validator và bắt đầu propose Block `N-4` của riêng nó.
4.  **Hậu quả**: Block `N-4` do node tạo ra sẽ xung đột với Block `N-4` đã tồn tại trên mạng -> **FORK**.

---

## 2. Giải Pháp: Sync Barrier & Deterministic Handover

Để ngăn chặn vấn đề trên, hệ thống sử dụng cơ chế "Sync Barrier" (Rào chắn đồng bộ) trong `epoch_monitor/mod.rs`.

### 2.1. Sync Barrier Logic

Trước khi gọi `transition_to_epoch`, node thực hiện các bước kiểm tra nghiêm ngặt:

1.  **Peer Discovery**: Hỏi các peers khác trong committee: "Các bạn đang ở block nào?" (`peer_last_block`).
2.  **Local Check**: Hỏi Go Master của chính mình: "Đã execute đến block nào rồi?" (`go_last_block`).
3.  **The Wait Loop**:
    ```rust
    // Pseudocode logic trong epoch_monitor/mod.rs
    loop {
        if go_last_block >= peer_last_block {
            break; // AN TOÀN: Đã bắt kịp mạng lưới
        }
        sleep(500ms); // CHỜ: Không được phép transition
    }
    ```

### 2.2. Deterministic Handover (Chuyển Giao Xác Định)

Khi `transition_to_epoch` được gọi, tham số quan trọng nhất là `synced_global_exec_index`.

*   **Quy tắc**: Validator mới sẽ bắt đầu consensus state của mình từ `synced_global_exec_index + 1`.
*   Nếu `synced_global_exec_index` bị sai (nhỏ hơn thực tế), Validator sẽ cố gắng tạo lại các block đã tồn tại -> **Replay/Fork**.
*   **Khắc phục**: Rust Metanode sử dụng giá trị `last_block_number` từ Go Master làm chân lý (Source of Truth) thay vì state nội bộ có thể bị stale.

---

## 3. Cấu Trúc Dữ Liệu Block (Rust -> Go)

Dưới đây là cấu trúc dữ liệu `CommittedEpochData` được định nghĩa trong `metanode/proto/executor.proto` và được gửi qua Unix Domain Socket từ Rust sang Go.

### 3.1. Cấu Trúc Protobuf

```protobuf
// metanode/proto/executor.proto

message CommittedEpochData {
    // Danh sách các sub-blocks trong lần commit này
    repeated CommittedBlock blocks = 1;

    // QUAN TRỌNG: Global Index duy nhất trên toàn chuỗi (checkpoint sequence)
    // Đảm bảo mọi node execute theo đúng thứ tự tuyệt đối.
    uint64 global_exec_index = 2;

    // Index nội bộ trong epoch (reset về 0 khi sang epoch mới)
    uint32 commit_index = 3;

    // Metadata để Go Master điền vào Block Header
    uint64 epoch = 4;

    // QUAN TRỌNG: Timestamp thống nhất từ Consensus (không dùng time.Now() tại Go)
    // Giúp Block Hash là deterministic trên mọi node.
    uint64 commit_timestamp_ms = 5;
}

message CommittedBlock {
    uint64 epoch = 1;
    uint64 height = 2;
    repeated TransactionExe transactions = 3;
}

message TransactionExe {
    // Chứa RAW BYTES của transaction (không phải hash)
    bytes digest = 1; 
    uint32 worker_id = 2;
}
```

### 3.2. Đánh Giá Các Trường Quan Trọng

Để "khắc phục" và đảm bảo an toàn, bạn cần chú ý các trường sau khi debug log:

1.  **`global_exec_index`**:
    *   Đây là "nhịp tim" của chuỗi. Nó **phải tăng liên tục** (+1) và không bao giờ được có lỗ hổng (gap) hay trùng lặp.
    *   Nếu Log báo: `Duplicate global_exec_index` -> Có lỗi nghiêm trọng trong logic transition.

2.  **`commit_timestamp_ms`**:
    *   Rust tính toán timestamp này dựa trên median của các validators.
    *   Go **BẮT BUỘC** phải dùng timestamp này để tạo Block Header. Nếu Go dùng `time.Now()`, block hash sẽ khác nhau giữa các node -> **Consensus Failure**.

3.  **`digest` trong `TransactionExe`**:
    *   Lưu ý tên trường là `digest` nhưng thực chất chứa **Nội dung Transaction (Body)**.
    *   Go cần decode bytes này để execute transaction.

---

## 4. Checklist Khắc Phục & Kiểm Tra

Nếu bạn gặp vấn đề Fork hoặc Block Rejection, hãy kiểm tra theo thứ tự:

1.  **Kiểm tra Barrier Log**:
    *   Tìm log: `✅ [SYNC BARRIER] Go Master synced to block X (peer=Y)`.
    *   Nếu không thấy dòng này mà thấy `Switching to Validator`, nghĩa là Barrier bị bypass -> **LỖI**.

2.  **So Sánh Global Index**:
    *   Tại thời điểm chuyển giao, `global_exec_index` của Validator mới có khớp với `last_block` của mạng không?
    *   Log cần tìm: `Using synced_global_exec_index=X for transition`.

3.  **Kiểm Tra Timestamp**:
    *   Block Genesis của Epoch mới (do Validator mới tạo) có cùng Timestamp với các node khác không?
    *   Nếu khác -> Kiểm tra lại `commit_timestamp_ms` truyền sang Go.

---

## 5. Xác Định Chính Xác Block Cuối Epoch & Lấy Committee

Đây là phần **QUAN TRỌNG NHẤT** để Full Node và Validator biết chính xác khi nào epoch kết thúc và lấy committee cho epoch mới.

### 5.1. Cách Rust Phát Hiện Epoch Kết Thúc

Trong `commit_processor/mod.rs`, mỗi khi nhận được một `CommittedSubDag`, Rust kiểm tra xem có chứa **EndOfEpoch System Transaction** hay không:

```rust
// File: metanode/src/consensus/commit_processor.rs (dòng 290-320)

// Sau khi gửi commit cho Go, kiểm tra có EndOfEpoch không
if let Some((_block_ref, system_tx)) = subdag.extract_end_of_epoch_transaction() {
    if let Some((new_epoch, new_epoch_timestamp_ms, _)) = system_tx.as_end_of_epoch() {
        // 🎯 CHÍNH XÁC: global_exec_index tại commit này = EPOCH BOUNDARY
        info!("🎯 EndOfEpoch detected: commit_index={}, global_exec_index={}",
            commit_index, global_exec_index);
        
        // Gọi epoch transition callback
        callback(new_epoch, new_epoch_timestamp_ms, global_exec_index);
    }
}
```

**Kết Luận**: `global_exec_index` của commit chứa `EndOfEpoch` transaction chính là **EPOCH BOUNDARY BLOCK** (block cuối cùng của epoch cũ).

### 5.2. Cách Go Lưu Trữ Epoch Boundary

Khi Rust gọi `AdvanceEpoch`, Go lưu trữ `boundaryBlock` vào map `epochBoundaryBlocks`:

```go
// File: pkg/blockchain/chain_state.go (dòng 334-371)

type ChainState struct {
    epochBoundaryBlocks map[uint64]uint64   // epoch -> boundary_block (block cuối epoch trước)
}

func (cs *ChainState) AdvanceEpochWithBoundary(newEpoch, timestampMs, boundaryBlock uint64) error {
    // Lưu boundary block cho epoch mới
    cs.epochBoundaryBlocks[newEpoch] = boundaryBlock
    cs.currentEpoch = newEpoch
    cs.epochStartTimestampMs = timestampMs
    cs.SaveEpochData() // Persist to database
}
```

### 5.3. Cách Lấy Committee Cho Epoch Mới

Khi cần lấy committee (validators) cho epoch mới, **PHẢI** sử dụng `GetEpochBoundaryData`:

```go
// File: executor/unix_socket_handler.go (dòng 516-556)

func HandleGetEpochBoundaryDataRequest(request *pb.GetEpochBoundaryDataRequest) (*pb.EpochBoundaryData, error) {
    epoch := request.GetEpoch()
    
    // 1. LẤY BOUNDARY BLOCK - Block cuối cùng của epoch trước
    boundaryBlock, _ := rh.chainState.GetEpochBoundaryBlock(epoch)
    
    // 2. Lấy validators TẠI boundary block (validator snapshot)
    validators, _ := rh.GetValidatorsAtBlockInternal(boundaryBlock)
    
    return &pb.EpochBoundaryData{
        Epoch:         epoch,
        BoundaryBlock: boundaryBlock,    // 👈 LƯU Ý: Đây chính là global_exec_index cuối epoch cũ
        Validators:    validators.Validators,
    }, nil
}
```

### 5.4. Công Thức Xác Định (QUAN TRỌNG)

| Khái niệm | Giá trị | Giải thích |
|-----------|---------|------------|
| **Epoch N Boundary Block** | `global_exec_index` của EndOfEpoch commit | Block cuối cùng thuộc epoch N |
| **Epoch N+1 Committee** | Validators tại Boundary Block | Snapshot committee cho epoch mới |
| **Epoch N+1 Start Block** | `boundary_block + 1` | Block đầu tiên của epoch mới |

### 5.5. Ví Dụ Thực Tế

```
Timeline:
┌──────────────────────────────────────────────────────────────────┐
│                         EPOCH 0                                   │
├───────────┬───────────┬─────────────┬────────────────────────────┤
│ Block 0   │ Block 1   │ ...         │ Block 4273 (EndOfEpoch)    │
│           │           │             │ ← BOUNDARY BLOCK            │
└───────────┴───────────┴─────────────┴────────────────────────────┘
                                                     │
                              global_exec_index = 4273 = EPOCH 0 BOUNDARY
                                                     │
                                                     ▼
┌──────────────────────────────────────────────────────────────────┐
│                         EPOCH 1                                   │
├───────────────┬───────────┬───────────┬───────────┬─────────────┤
│ Block 4274    │ Block 4275│ ...       │           │             │
│ (Genesis E1)  │           │           │           │             │
└───────────────┴───────────┴───────────┴───────────┴─────────────┘
```

**Khi Full Node hoặc Validator muốn tham gia Epoch 1:**

1.  Gọi `GetEpochBoundaryData(epoch=1)` → Trả về `boundary_block=4273`
2.  Rust gọi `GetValidatorsAtBlock(4273)` để lấy committee
3.  Committee này sẽ bao gồm validator mới (nếu đã register trước block 4273)
4.  Full Node/Validator bắt đầu consensus từ `global_exec_index = 4274`

### 5.6. Kiểm Tra Thực Tế

Để xác định epoch boundary trong hệ thống đang chạy:

1.  **Kiểm tra Log Rust**: Tìm `EndOfEpoch detected: commit_index=X, global_exec_index=Y`
2.  **Kiểm tra Go State**: Xem `epoch_data_backup.json`:
    ```json
    {
      "current_epoch": 1,
      "epoch_start_timestamp_ms": 1234567890000,
      "epoch_boundary_blocks": {
        "1": 4273   // ← Epoch 1 bắt đầu sau block 4273
      }
    }
    ```
3.  **Gọi API**: `GetEpochBoundaryData(epoch=1)` phải trả về `boundary_block=4273`
