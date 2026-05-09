# Synthetic Baseline Patch Fork + Epoch Auto-Unsuppress Race

## Bug 1: patch_baseline_if_needed Never Executes (FIXED — Session 1)

### Mô tả
Hàm `patch_baseline_if_needed` trong `commit_syncer.rs` được thiết kế để fetch reputation scores 
thật từ mạng sau khi node tạo commit giả (synthetic baseline) qua `reset_to_network_baseline`.

**Lỗi logic ban đầu (đã fix):**
```rust
let last_commit_index = self.inner.dag_state.read().last_commit_index();
if self.synced_commit_index <= last_commit_index { return; } // LUÔN TRUE → early return
```

**Lỗi logic thứ hai (đã fix):**
```rust
last_commit.reference().digest == crate::commit::CommitDigest::MIN // KHÔNG BAO GIỜ TRUE
```
`reference().digest` là hash tính từ serialized bytes, không bao giờ bằng `CommitDigest::MIN`.

### Giải pháp
Kiểm tra `previous_digest()` (trường được gán trực tiếp = MIN) và `leader().digest` (= BlockDigest::MIN):
```rust
use crate::commit::CommitAPI;
last_commit.index() == self.synced_commit_index
    && last_commit.previous_digest() == crate::commit::CommitDigest::MIN
    && last_commit.leader().digest == consensus_types::block::BlockDigest::MIN
```

---

## Bug 2: Epoch Auto-Unsuppress Race Condition (FIXED — Session 2)

### Mô tả
Khi tất cả node khởi động với `epoch_start_timestamp_ms` quá cũ, tất cả bị SUPPRESSED.
Cơ chế auto-unsuppress (healthy ≥ 240s) sẽ tự bỏ suppression. **Tuy nhiên**, mỗi node 
reset `epoch_start = now_ms` độc lập → mỗi node có epoch timer khác nhau → 
EndOfEpoch trigger ở các consensus round khác nhau → txRoot fork.

### Bằng chứng forensic (Block 1398)
- **Cùng leader** trên tất cả node (KHÔNG phải lỗi LeaderSchedule)
- **Khác timestamp** (lệch 2 giây) và **khác txRoot**
- Node m1 nhảy epoch 2→3 tại block 1423 trong khi cả cụm ở epoch 2

### Giải pháp
Khi auto-unsuppress, KHÔNG reset `epoch_start = now_ms`. Giữ nguyên giá trị cũ (stale).
Tất cả node có cùng giá trị stale → tính `elapsed_seconds` giống nhau → trigger EndOfEpoch 
cùng lúc. Block đầu tiên chứa EndOfEpoch được commit → tất cả node xử lý xác định.
