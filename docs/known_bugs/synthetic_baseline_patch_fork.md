# Synthetic Baseline Patch Fork (Lỗi Fork do Không Cập Nhật LeaderSchedule sau khi Restore Snapshot)

## Mô tả lỗi (Bug Description)
Trong quá trình node khôi phục từ snapshot (STARTUP-SYNC), node khởi tạo DAG trống và gọi `reset_to_network_baseline` để tua nhanh (fast-forward) `synced_commit_index` tới `baseline_target` (bằng với chỉ số Go block gần nhất). Quá trình này chèn một commit giả (synthetic commit) với mã hash là `CommitDigest::MIN` và điểm uy tín (reputation scores) là `None`.

Cơ chế `patch_baseline_if_needed` được thiết kế để phát hiện commit giả này, sau đó fetch dữ liệu thực tế từ mạng (bao gồm `reputation_scores` thực tế) và cập nhật lại DAG, từ đó giúp cập nhật `LeaderSchedule`.

**Tuy nhiên, có một lỗi logic nghiêm trọng trong hàm `patch_baseline_if_needed`:**
```rust
let last_commit_index = self.inner.dag_state.read().last_commit_index();
if self.synced_commit_index <= last_commit_index { return; }
```
Vì hàm `reset_to_network_baseline` đã gán `last_commit_index` bằng chính `synced_commit_index`, điều kiện `self.synced_commit_index <= last_commit_index` luôn luôn trả về `TRUE`! Do đó, hàm `patch_baseline_if_needed` thoát ngay lập tức (early return) mà không bao giờ thực hiện việc patch (cập nhật).

## Hậu quả (Impact)
1. Commit giả vĩnh viễn ở lại trong DAG với `reputation_scores` là `None`.
2. Khi node bắt đầu xử lý block tiếp theo, nó tiếp tục tính toán lịch bầu leader (LeaderSchedule) dựa trên dữ liệu mặc định thay vì điểm uy tín của mạng.
3. Node chọn sai leader cho vòng đồng thuận tiếp theo.
4. Mặc dù cùng tham gia mạng, node này lại xác nhận (commit) danh sách block khác với phần còn lại của mạng, dẫn đến sự sai lệch giá trị `txRoot` trong Go Execution Layer.
5. Sự cố này đặc biệt nguy hiểm khi xảy ra quanh thời điểm chuyển epoch (Epoch Boundary), vì node sẽ sinh ra hoặc xử lý transaction hệ thống `EndOfEpoch` sai thời điểm, gây rẽ nhánh (fork) vĩnh viễn trên toàn cụm. (Ví dụ: Node 3 rẽ nhánh thành Epoch 2 trong khi cả cụm vẫn ở Epoch 1 tại block 1362).

## Giải pháp (Resolution)
Thay vì dùng điều kiện so sánh index thông thường, hàm `patch_baseline_if_needed` đã được sửa để kiểm tra trực tiếp xem commit cuối cùng có phải là commit giả hay không thông qua việc kiểm tra mã băm `CommitDigest::MIN`:

```rust
let is_synthetic_baseline = {
    let dag = self.inner.dag_state.read();
    if let Some(last_commit) = dag.last_commit() {
        last_commit.index() == self.synced_commit_index && 
        last_commit.reference().digest == crate::commit::CommitDigest::MIN
    } else {
        false
    }
};

if !is_synthetic_baseline { return; }
```

Nhờ vậy, `patch_baseline_if_needed` sẽ chính xác bỏ qua các commit thật, nhưng vẫn tiến hành fetch và patch thành công các commit giả do quá trình snapshot restore sinh ra. Lúc này `LeaderSchedule` sẽ được khôi phục chuẩn xác, chấm dứt hiện tượng fork.
