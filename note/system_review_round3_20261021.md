**Review Raft → execution sau bản sửa round 2 — 2026-10-21**

Phạm vi: working tree trên commit `6d914bac2d503288a6c2cc93e2542d5941e708e8`, có sửa đổi chưa commit của người dùng. Theo yêu cầu, không review BFT. Không sửa production code hoặc test trong source tree; test bổ sung chạy qua overlay.

**Xác nhận sửa cũ**

[ĐO] Bộ test network, raftfeed, blockchain, processor đều PASS, gồm hai regression round 2 đã được người dùng đưa vào source. [Baseline](evidence/system_review_round3_20261021/baseline.log).

[ĐỌC CODE] Follower đã split trước forwarding; submitLocal đã enqueue/đợi commit theo chunk vừa queue. Hai trường hợp round 2 ở cấu hình và tốc độ test ban đầu đã được khắc phục.

**1. P2 — Retry batch nhiều chunk có thể không bao giờ tới phần cuối khi commit chậm**

[ĐỌC CODE] [node.go:403](../execution/pkg/rollup/raftfeed/node.go:403) tạo một timer 5 giây cho toàn bộ batch, kể cả nhiều chunk chạy nối tiếp. Khi timer hết, hàm trả 504; caller [tx_batch_forwarder_core.go:240](../execution/cmd/simple_chain/processor/tx_batch_forwarder_core.go:240) retry nguyên batch. Lần gọi mới bắt đầu lại ở chunk đầu, không biết phần trước đã commit. Nếu mỗi lần chỉ đủ thời gian xử lý prefix thì suffix không được propose, dù cluster vẫn commit bình thường. Đây khác với at-least-once đơn thuần: prefix lặp lại trong khi suffix không tiến triển.

[ĐO] Harness 1 node, queue 1, 10 pieces; test hook làm chậm mỗi lần ghi application log 1100 ms, không trì hoãn election log. Ba lần submit đều trả 504. Sau khi đợi phần đang ghi xong, số lần nonce xuất hiện là `map[1:3 2:3 3:3 4:3 5:2]`; nonce 6–10 chưa tới sink. Test `TestReviewChunkRetryMustReachTail` FAIL. [Log](evidence/system_review_round3_20261021/reproduction.log).

Điều kiện: batch vượt một chunk và tổng thời gian commit vượt budget; không khẳng định cấu hình mặc định/workload hiện tại của người dùng đang gặp. Chỉ đo delivery ở Raft sink, không đo thực thi giao dịch lặp hoặc mất tiền.

Hướng sửa ưu tiên: chia thành các đơn vị submit có giới hạn ở caller, chỉ retry đơn vị chưa được xác nhận, giữ suffix trong bộ nhớ để xử lý tiếp. Nếu giữ chunking trong Raft API thì cần giao kèo tiến độ rõ cho caller; không chỉ tăng timeout cố định. Follower cũng đang có vòng gửi pieces từ đầu sau mỗi lỗi, cần kiểm tra cùng nguyên tắc. Thêm regression commit chậm và lỗi sau khi prefix đã nhận; assert mọi hash cuối cùng đều đến sink, không chỉ status của một lần gọi.

**2. P2 — Split forwarding chưa chặn giới hạn transport nếu MaxBatchBytes > 64 MiB**

[ĐỌC CODE] [forward.go:99](../execution/pkg/rollup/raftfeed/forward.go:99) phát hiện batch vượt maxForwardBody nhưng lại gọi splitBatch với MaxBatchBytes. Nếu MaxBatchBytes lớn hơn body cap, piece vẫn có thể vượt 64 MiB. Cấu hình hiện chỉ yêu cầu giá trị dương, chưa cấm trường hợp này. HTTP handler trả 413 và processor bỏ batch như round 2.

[ĐO] Cấu hình MaxBatchBytes=128 MiB, batch 67256320 bytes gồm 1024 transaction nhỏ: split thành 1 piece, follower vẫn nhận 413. Test `TestReviewHighBatchLimitStillRespectsHTTP` FAIL. [Log](evidence/system_review_round3_20261021/reproduction.log).

Không ảnh hưởng trường hợp MaxBatchBytes mặc định 4 MiB mà regression cũ đã kiểm chứng PASS. Fixture là protobuf tầng feed, chưa kiểm chứng TCP/EVM đầu cuối.

Hướng sửa tối thiểu: dùng `min(MaxBatchBytes, maxForwardBody)` cho giới hạn mỗi request và thống nhất admission nếu cho phép transaction đơn lớn hơn transport. Hoặc reject cấu hình MaxBatchBytes vượt cap tại startup nếu hệ thống không cần hỗ trợ nó. Thêm test trên cap; giữ test mặc định đã pass.

**Chạy lại**

Baseline, từ execution:

```bash
go test ./pkg/network ./pkg/rollup/raftfeed ./pkg/blockchain ./cmd/simple_chain/processor -count=1 -timeout=180s
```

Overlay lưu nguyên snapshot test hiện tại cộng hai test mới. Từ root metanode:

```bash
python3 - <<'PY'
import json
from pathlib import Path
r = Path.cwd().resolve()
src = r / 'execution/pkg/rollup/raftfeed/raft_audit_test.go'
overlay = r / 'note/evidence/system_review_round3_20261021/raft_overlay.go.txt'
Path('/tmp/metanode-review3-overlay.json').write_text(json.dumps({'Replace': {str(src): str(overlay)}}))
PY
cd execution
go test -overlay /tmp/metanode-review3-overlay.json ./pkg/rollup/raftfeed -run 'TestReview(HighBatch|ChunkRetry)' -count=1 -timeout=60s -v
```

Khi source test thay đổi, chuyển riêng test mới thay vì tái dùng toàn bộ snapshot overlay cũ.

[ĐO] MVCC package chạy `go test -race ./pkg/blockchain/tx_processor/mvcc -count=1 -timeout=90s` PASS. [Log](evidence/system_review_round3_20261021/mvcc_race.log).

[ĐO] Nhóm regression Block-STM có race detector PASS, bao gồm suspend/wakeup, hot-recipient stress, rejected speculative transaction, barrier visibility, cascade invalidation và cleanup stale writes. Lệnh: `go test -race ./pkg/blockchain/tx_processor -run 'Test(TrueBlockSTM_(SuspendWakeup|HotRecipient|RejectedSpeculative|BarrierTx)|CascadeInvalidate|CleanupStaleWrites)' -count=1 -timeout=120s`. [Log](evidence/system_review_round3_20261021/stm_race.log). Chưa phát hiện lỗi mới của Block-STM trong phạm vi này; không đồng nghĩa mọi nhánh EVM/storage đều đã được chứng minh đúng.

[ĐO] Bộ rộng `go test ./pkg/blockchain/tx_processor/... -count=1 -timeout=180s` không hoàn thành: timeout toàn package sau 180 giây. Lúc hết hạn đang chạy `TestExecDifferential_GatewayAssetFlows_OptimisticVsTrueBlockSTMOnly` được 21 giây, stack ở bước tạo/chữ ký fixture. Không có assertion FAIL trước timeout trong log; chưa đủ bằng chứng gọi đây là deadlock Block-STM. [Log đầy đủ nén gzip](evidence/system_review_round3_20261021/stm_full.log.gz). Không ghi bộ này là PASS.

[ĐO] Một lượt race bổ sung hẹp hơn (`-run 'Test(TrueBlockSTM_(SuspendWakeup|BarrierTx)|CascadeInvalidate|CleanupStaleWrites)' -count=1 -timeout=60s`) in PASS cho tests nhưng tiến trình bị go test dừng sau 2 phút, exit 1. Stack ở `runtime.racefini → os.Exit(0)`, tức giai đoạn thoát race runtime, không phải worker thực thi giao dịch. [Log](evidence/system_review_round3_20261021/stm_focused_race.log). Chưa xác định nguyên nhân shutdown chậm; không gộp lượt này vào PASS của lệnh trước và không kết luận đây là deadlock production.

[CHƯA ĐO] Chưa chạy build toàn Go/Rust/FFI, power-cut recovery hoặc benchmark node production. Không suy ra toàn hệ thống sạch lỗi từ các test trên. Hai finding đều là lỗi vận hành có điều kiện, chưa phải bằng chứng sai consensus/state hoặc mất tài sản.
