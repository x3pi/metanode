**Đánh giá sau sửa — phần còn tồn đọng và kế hoạch thực hiện**

Commit kiểm tra: `6d914bac2d503288a6c2cc93e2542d5941e708e8`.

[ĐO] Bộ test hiện có của `pkg/network`, `pkg/rollup/raftfeed`, `pkg/blockchain`, `cmd/simple_chain/processor` đều PASS, gồm các test audit đã đưa vào repo. [Log](evidence/system_review_followup_20261021/baseline.log).

[ĐỌC CODE] Luồng mới đã truyền trạng thái từ leader qua follower tới forwarder; `StatusTooLarge` không còn bị retry như lỗi tạm thời trong forwarder. Phần này đã khắc phục blocker của lượt trước. [Forwarder](../execution/cmd/simple_chain/processor/tx_batch_forwarder_core.go:240).

**Tồn đọng A — P2: đường nhận batch chưa kiểm tra kích thước Raft**

[ĐỌC CODE] Kiểm tra mới nằm trong `addTransactionToPoolInternalCap`, nhưng `addTransactionsToPoolInternalCap` có vòng kiểm tra riêng tại [tx_validator_pool_core.go:541](../execution/cmd/simple_chain/processor/tx_validator_pool_core.go:541), chưa gọi kiểm tra kích thước. TCP batch dùng `AddAdmittedTransactionsToPool`, đi vào hàm batch này.

[ĐO] `TestReviewRaftBatchIngressMustEnforceSameSizeLimit` dùng cùng transaction ở hai nhánh: single từ chối kích thước 4194443 bytes, batch trả thành công và pool có 1 transaction. Fixture dùng đường pre-verified để cô lập kiểm tra kích thước khỏi kiểm tra chữ ký; đây không phải bài gửi transaction Ethereum 4 MiB qua mạng. [Log](evidence/system_review_followup_20261021/reproduction.log).

[ĐỌC CODE] Hậu quả khi giao dịch vượt giới hạn thực tế đã vào pool: `splitBatch` bỏ giao dịch quá lớn tại [node.go:464](../execution/pkg/rollup/raftfeed/node.go:464). Batch hỗn hợp vẫn được xác nhận thành công cho phần còn lại, trong khi forwarder ghi nhận toàn bộ `batchTxs` là đã forward. Batch toàn giao dịch quá lớn bị forwarder log rồi giải phóng in-flight, chưa gửi kết quả từ chối đến client tại nhánh đó. Không được coi việc tránh vòng retry vô hạn là đã giải quyết đầy đủ vòng đời giao dịch.

**Tồn đọng B — P2: giới hạn admission phụ thuộc trạng thái runtime**

[ĐỌC CODE] [MaxBatchBytes](../execution/pkg/rollup/raftfeed/raftfeed.go:209) chỉ đọc cluster đang chạy; nếu chưa có cluster thì luôn trả mặc định, không đọc giá trị đã cấu hình.

[ĐO] `TestReviewConfiguredLimitBeforeClusterStart`: cấu hình `max_batch_bytes=256` nhưng helper trả `4194304` trước `StartCluster`. [Log](evidence/system_review_followup_20261021/reproduction.log).

[CHƯA ĐO] Chưa tái hiện bằng gửi TCP đúng cửa sổ startup trên cluster thật. Sai giá trị helper đã được kiểm chứng; hậu quả thực tế phụ thuộc thời điểm admission được mở. Không kết luận lỗi này xảy ra trên mọi lần khởi động.

**Kế hoạch thực hiện tiếp**

1. **Một quy tắc kích thước dùng chung cho single và batch.**
   - Trong `tx_validator_pool_core.go`, tách kiểm tra serialize + overhead thành helper nhỏ, gọi từ cả hai đường trước khi insert/evict.
   - Marshal lỗi phải trả lỗi, không bỏ qua kiểm tra.
   - Với batch, điền lỗi đúng index trong `errorsList`; transaction hợp lệ khác vẫn được nhận.
   - Test single/batch với kích thước dưới, bằng và trên giới hạn; xác nhận transaction bị từ chối không nằm trong pool. Chạy cho cả đường admitted và pre-verified.

2. **Giới hạn phải xác định từ cấu hình đã chuẩn hóa trước admission.**
   - Chỉnh `raftfeed.MaxBatchBytes` hoặc truyền giới hạn đã chuẩn hóa vào validator; không mặc định lại chỉ vì cluster chưa được gán.
   - Dùng cùng quy tắc default với `effectiveRaftConfig`.
   - Test trước/sau StartCluster: cùng cấu hình phải ra cùng giới hạn; thêm ca giá trị 0 dùng default.

3. **Chốt xử lý transaction bị từ chối sau admission.**
   - Quy định `StatusAccepted` không che giấu việc loại một phần input. Nếu vẫn cho phép partial acceptance, kết quả phải chỉ rõ transaction nào đã nhận và transaction nào bị từ chối; forwarder chỉ cập nhật nonce/tracking cho phần đã nhận.
   - Nếu chọn API all-or-nothing, bỏ cơ chế lọc âm thầm trong `splitBatch`; trả lỗi rõ trước khi enqueue bất kỳ piece nào. Forwarder phải phân loại/giữ transaction còn hợp lệ, báo từ chối vĩnh viễn cho transaction sai và tiếp tục xử lý batch kế tiếp. Không chỉ log rồi mất giao dịch đã nhận.
   - Giữ giới hạn thống nhất giữa các member hoặc xác định rõ cách xử lý khi leader có giới hạn khác; admission trên follower không đủ bảo đảm nếu hai nơi dùng giới hạn khác nhau.
   - Test batch hỗn hợp: kiểm tra transaction trong block, kết quả từ chối, nonce cache và in-flight. Test toàn bộ bị từ chối: batch hợp lệ tiếp theo vẫn được xử lý. Test leader/follower cho cùng kết quả có cùng ý nghĩa.

4. **Kiểm chứng trước merge.**
   - Chạy test bốn package nêu trên và các regression mới; chạy race cho network/Raft và test processor liên quan.
   - Chạy `consensus/metanode/scripts/build_check.sh`; kiểm tra cả exit code lẫn warnings.
   - Kiểm tra snapshot chờ durability barrier và bài crash/recovery riêng trước khi tuyên bố an toàn cho production.

**Chạy lại hai test bổ sung**

Hai file overlay lưu nguyên bộ test hiện tại cộng thêm test tái hiện, dùng với đúng commit đã ghi ở đầu báo cáo. Chạy từ root `metanode`:

```bash
python3 - <<'PY'
import json
from pathlib import Path
r = Path.cwd().resolve()
e = r / 'note/evidence/system_review_followup_20261021'
mapping = {
    str(r / 'execution/cmd/simple_chain/processor/tx_validator_pool_core_test.go'): str(e / 'processor_overlay.go.txt'),
    str(r / 'execution/pkg/rollup/raftfeed/raft_audit_test.go'): str(e / 'raft_overlay.go.txt'),
}
Path('/tmp/metanode-followup-overlay.json').write_text(json.dumps({'Replace': mapping}))
PY
cd execution
go test -overlay /tmp/metanode-followup-overlay.json \
  ./cmd/simple_chain/processor ./pkg/rollup/raftfeed \
  -run 'TestReview(RaftBatchIngress|ConfiguredLimit)' -count=1 -timeout=60s -v
```

[ĐO] Kết quả tại commit kiểm tra: hai assertion FAIL, exit code 1. Chỉ thêm tài liệu/bằng chứng trong lượt review này, không sửa production code. Chưa chạy lại build toàn hệ thống hoặc race trên bản mới này. Chưa rà sâu Rust BFT và Block-STM, vì vậy đây không phải xác nhận toàn hệ thống đã sạch lỗi.
