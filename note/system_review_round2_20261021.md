**Review tiếp sau bản sửa admission — 2026-10-21**

Phạm vi: working tree trên commit `6d914bac2d503288a6c2cc93e2542d5941e708e8`, gồm các sửa đổi chưa commit của người dùng. Không sửa production code trong lượt review này.

**Các điểm cũ đã xử lý**

[ĐỌC CODE] Helper kiểm tra kích thước và marshal error đã được gọi ở cả single và batch admission: [tx_validator_pool_core.go](../execution/cmd/simple_chain/processor/tx_validator_pool_core.go:282). [MaxBatchBytes](../execution/pkg/rollup/raftfeed/raftfeed.go:209) đã đọc cấu hình khi chưa có cluster. Hai finding A/B của báo cáo followup trước không còn mô tả đúng bản hiện tại.

[ĐO] Test hiện có của network, raftfeed, blockchain, processor đều PASS. [Baseline](evidence/system_review_round2_20261021/baseline.log). Lệnh chạy từ execution: `go test ./pkg/network ./pkg/rollup/raftfeed ./pkg/blockchain ./cmd/simple_chain/processor -count=1 -timeout=180s`.

**1. P2 — Batch có các transaction vừa giới hạn vẫn bị HTTP forwarding từ chối**

[ĐỌC CODE] [Forwarder](../execution/cmd/simple_chain/processor/tx_batch_forwarder_core.go:205) chia theo số lượng transaction, chưa chặn tổng byte serialize. Follower gửi nguyên batch; [HTTP handler](../execution/pkg/rollup/raftfeed/forward.go:65) giới hạn body 64 MiB trước khi split. HTTP 413 trở thành StatusTooLarge và [processor](../execution/cmd/simple_chain/processor/tx_batch_forwarder_core.go:252) bỏ cả batch, giải phóng in-flight rồi tiếp tục. ReleaseInFlightWithDrainID chỉ xóa reservation, không đưa transaction về pool.

[ĐO] Test qua follower thật của harness nhận status 413 cho batch 67256320 bytes, chứa 1024 transaction; splitBatch phía Raft tách được 17 pieces và giữ đủ transaction. [Reproduction](evidence/system_review_round2_20261021/reproduction.log), test `TestReviewForwardBodyLimitMustNotRejectSplittableBatch`.

Ảnh hưởng: khi gom đủ giao dịch có data lớn và gửi qua follower, batch hợp lệ về giới hạn từng transaction bị bỏ khỏi đường forward. Không cần cấu hình MaxBatchBytes bất thường để gặp giới hạn HTTP này.

[CHƯA ĐO] Fixture là protobuf ở tầng Raft feed, chưa gửi giao dịch có chữ ký qua TCP/EVM từ đầu đến cuối. Việc processor bỏ batch là kết luận đọc code; không tuyên bố đã đo mất giao dịch client hoặc mất tài sản.

Hướng sửa: chia batch theo số byte serialize, tính cả protobuf overhead, trước khi gửi; thống nhất giới hạn với transport. Khi gặp 413 do tổng batch, tách nhỏ và xử lý tiếp phần hợp lệ thay vì bỏ tất cả. Test leader/follower cùng tập transaction, kiểm tra đủ hash được nhận; thêm test processor chứng minh không mất transaction khi vượt giới hạn body.

**2. P2 — Batch tách thành nhiều pieces hơn sức chứa queue sẽ luôn nhận 429**

[ĐỌC CODE] [submitLocal](../execution/pkg/rollup/raftfeed/node.go:402) đòi queue còn đủ chỗ cho tất cả pieces cùng lúc. Khi số pieces lớn hơn cap(proposeQ), điều kiện luôn sai kể cả queue rỗng. [Processor](../execution/cmd/simple_chain/processor/tx_batch_forwarder_core.go:260) retry nguyên batch nên batch đó chặn tiến trình forward tiếp theo.

[ĐO] Test queue capacity 1 và batch tách thành 3 pieces: cả ba lần gọi đều trả 429, queueLen luôn 0. [Reproduction](evidence/system_review_round2_20261021/reproduction.log), test `TestReviewSplitLargerThanQueueMustProgress`.

Điều kiện: cần số pieces vượt tổng sức chứa queue, ví dụ cấu hình queue nhỏ hoặc MaxBatchBytes nhỏ. Không kết luận cấu hình mặc định luôn bị kẹt.

Hướng sửa: giới hạn batch phía caller để từng lần submit vừa queue, hoặc enqueue/đợi commit theo nhóm có giới hạn. Giữ rõ tiến độ phần đã commit khi retry/lost leadership; không chỉ đổi 429 thành 413 rồi bỏ batch. Test queue nhỏ phải xử lý hết transaction và tiếp tục batch sau.

**Bằng chứng và chạy lại**

Hai test bổ sung đều FAIL trên bản hiện tại; được chạy bằng overlay, không ghi đè test của người dùng. [Test overlay](evidence/system_review_round2_20261021/raft_overlay.go.txt).

Từ root metanode:

```bash
python3 - <<'PY'
import json
from pathlib import Path
r = Path.cwd().resolve()
source = r / 'execution/pkg/rollup/raftfeed/raft_audit_test.go'
overlay = r / 'note/evidence/system_review_round2_20261021/raft_overlay.go.txt'
Path('/tmp/metanode-round2-overlay.json').write_text(json.dumps({'Replace': {str(source): str(overlay)}}))
PY
cd execution
go test -overlay /tmp/metanode-round2-overlay.json ./pkg/rollup/raftfeed -run 'TestReview(SplitLarger|ForwardBody)' -count=1 -timeout=90s -v
```

Overlay lưu cả test hiện tại và test bổ sung, nên khi source thay đổi nên chuyển riêng hai test bổ sung vào bộ test mới thay vì dùng lại snapshot cũ.

**Đánh giá**

Đây là hai lỗi vận hành có điều kiện, nên sửa, không phải lỗi format hay tối ưu tùy chọn. Có thể merge dev có theo dõi nếu workload/cấu hình chưa chạm điều kiện trên; nên sửa trước production có transaction data lớn hoặc cấu hình queue nhỏ. Chưa có bằng chứng từ lượt này cho lỗi fork/sai state/mất tài sản.

[CHƯA ĐO] Không chạy lại race, build toàn Go/Rust/FFI, crash/recovery hoặc cluster production trong lượt này; chưa rà sâu Rust BFT và Block-STM. Vì vậy kết quả này không xác nhận toàn source đã sạch lỗi.
