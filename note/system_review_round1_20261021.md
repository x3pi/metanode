**Rà soát vòng 1 — network, Raft ingestion và commit block**

Commit được rà: `773e62b926b86232ac96fda42fc4b9bd48ff1a1a`.
Ngày thực hiện: 2026-10-21. Chỉ thêm báo cáo và bằng chứng; không sửa source production, không khởi động lại/deploy node.

**Kết quả và giới hạn**

- [ĐO] Test có sẵn của `pkg/network`, `pkg/rollup/raftfeed`, `pkg/blockchain` đều PASS. Bằng chứng: [network/Raft](evidence/system_review_round1_20261021/network_raft_baseline.log), [blockchain](evidence/system_review_round1_20261021/blockchain_baseline.log).
- [ĐO] Năm test bổ sung kiểm tra hành vi mong đợi đều FAIL tại assertion tương ứng, tái hiện năm vấn đề bên dưới. Đây là test gọi code thực tế với transport/storage giả lập lỗi; không phải kết quả đo trên cluster production. [Log tái hiện](evidence/system_review_round1_20261021/reproduction.log).
- [CHƯA ĐO] Chưa thử mất điện, restart cluster sau snapshot hoặc benchmark tải thực tế. Chưa rà sâu Rust BFT, epoch transition, toàn bộ EVM/Block-STM, Xapian và cross-chain.
- Không chạy `build_check.sh` vì không thay đổi mã production. Test bổ sung được nạp bằng Go overlay; file `.go.txt` trong bằng chứng không được tự động đưa vào build.

**1. P1 — Raft nhận nhầm block chưa durable là đã durable**

[ĐỌC CODE] [CommitBlockState](../execution/pkg/blockchain/block_state_commit.go:219) gọi `storage.UpdateLastBlockNumber(blockNum)` trước các barrier lưu receipts, transactions và block DB. Barrier block DB nằm tại [dòng 294](../execution/pkg/blockchain/block_state_commit.go:294). Nếu barrier lỗi, hàm trả lỗi nhưng bộ đếm đã tăng.

[ĐỌC CODE] [Wiring Raft](../execution/cmd/simple_chain/processor/block_processor_network.go:123) truyền `storage.GetLastBlockNumber` vào `ClusterConfig.Durable`. [FSM.Snapshot](../execution/pkg/rollup/raftfeed/fsm.go:152) chỉ chờ bộ đếm này đạt `delivered`, rồi tạo snapshot metadata. Nó không lấy `commitMutex` của `CommitBlockState`.

[ĐO] `TestAuditHeightMustNotAdvanceBeforeDurability` gọi `CommitBlockState` với storage có `SyncDurable()` trả lỗi. Tại thời điểm vào barrier và sau khi hàm trả lỗi, bộ đếm đều đã công bố block 1. Xem [test](evidence/system_review_round1_20261021/durability_audit_test.go.txt) và [log](evidence/system_review_round1_20261021/reproduction.log).

[ĐỌC CODE] Hậu quả có điều kiện: snapshot chạy vào cửa sổ này có thể ghi metadata vượt durable tip. Nếu crash làm mất block chưa được sync, [FSM.Restore](../execution/pkg/rollup/raftfeed/fsm.go:190) từ chối khởi động vì DB nằm sau snapshot. Không khẳng định đã tái hiện toàn bộ chuỗi crash/restore, hoặc rằng dữ liệu chắc chắn mất khỏi mọi replica.

Hướng sửa đề xuất, chưa triển khai: cung cấp một durable watermark riêng cho Raft, chỉ công bố sau khi toàn bộ dữ liệu cần cho recovery đã durable. Rà cả NOMT/changelog trước khi quyết định vị trí cập nhật; không chỉ đổi tên bộ đếm hiện tại. Test cần giữ barrier đang chờ và chứng minh snapshot chưa được vượt qua.

**2. P2 — Một transaction quá lớn làm mất cả batch nhưng Submit báo thành công**

[ĐỌC CODE] [submitLocal](../execution/pkg/rollup/raftfeed/node.go:373) trả `statusAccepted` khi `splitBatch` lỗi. [splitBatch](../execution/pkg/rollup/raftfeed/node.go:447) trả lỗi cho toàn bộ input khi gặp một transaction vượt `max_batch_bytes`, kể cả khi input còn transaction nhỏ có thể xử lý. [Forwarder](../execution/cmd/simple_chain/processor/tx_batch_forwarder_core.go:241) thoát retry ngay khi `Submit` trả true.

[ĐO] `TestAuditOversizeTxMustNotAcknowledgeLostBatch` dùng Raft một node với `MaxBatchBytes` nhỏ để kích hoạt nhánh này: `Submit=true`, `Dropped=1`. Batch có một transaction nhỏ và một transaction lớn hơn giới hạn. Xem [test](evidence/system_review_round1_20261021/raft_audit_test.go.txt), [log](evidence/system_review_round1_20261021/reproduction.log).

Điều kiện áp dụng: transaction đã tới feed nhưng kích thước protobuf vượt cấu hình batch; đáng chú ý khi hạ `max_batch_bytes`. Fixture kiểm tra hợp đồng của tầng feed, không phải bài gửi transaction đã ký qua ingress production. Chưa chứng minh trường hợp này xảy ra với cấu hình mặc định và mọi kiểm tra ingress hiện tại.

Hướng sửa đề xuất, chưa triển khai: thống nhất kiểm tra kích thước với ingress; không xác nhận một batch chưa được replicate. Phải giữ các transaction còn dùng được và phân biệt lỗi vĩnh viễn với retry. Chỉ đổi `true` thành `false` sẽ khiến forwarder retry vô hạn với transaction quá lớn.

**3. P2 — Lỗi ghi TCP làm chết writer nhưng kết nối vẫn nhận message mới**

[ĐỌC CODE] [writeLoop](../execution/pkg/network/connection.go:791) chỉ log rồi return khi Write/Flush lỗi. Không báo lỗi về owner, không đóng socket và không cập nhật trạng thái kết nối tại nhánh này. [SendMessage](../execution/pkg/network/connection.go:398) vẫn có thể enqueue thành công khi còn chỗ trong `sendChan`.

[ĐO] `TestAuditWriterFailureMustDisconnect` giả lập lỗi Write trong kết nối có read side còn mở. Sau lỗi, `IsConnect()` vẫn true và một lần gửi `Receipt` tiếp theo trả nil mặc dù writer đã thoát. Xem [test](evidence/system_review_round1_20261021/network_audit_test.go.txt), [log](evidence/system_review_round1_20261021/reproduction.log).

[ĐỌC CODE] Hậu quả: message mới nằm trong queue không còn consumer cho tới khi cơ chế khác dọn kết nối. Đặc biệt cần xử lý write timeout khi peer còn gửi dữ liệu hoặc outbound connection không có idle timeout.

Hướng sửa đề xuất, chưa triển khai: mọi lỗi I/O kết thúc writer phải thông báo owner để đóng connection và từ chối gửi tiếp. Không đợi cleanup trực tiếp từ writer nếu cleanup lại `Wait` chính writer đó.

**4. P2 — Gate InitConnection chỉ bảo đảm thứ tự enqueue, không bảo đảm khởi tạo xong**

[ĐỌC CODE] [HandleConnection](../execution/pkg/network/server.go:220) đóng `initReady` ngay sau enqueue, trong khi [worker pool](../execution/pkg/network/server.go:97) chạy nhiều handler đồng thời. Nhánh timeout/drop InitConnection cũng đi tới việc mở gate.

[ĐO] `TestAuditInitMustFinishBeforeNextRequest` giữ handler InitConnection đang chờ bằng channel; handler SendRawTransactions của cùng connection vẫn được worker khác chạy. Xem [test](evidence/system_review_round1_20261021/network_audit_test.go.txt), [log](evidence/system_review_round1_20261021/reproduction.log).

[ĐỌC CODE] [ProcessInitConnection](../execution/cmd/simple_chain/processor/connection_processor.go:65) mới là nơi thiết lập address/type và đăng ký connection vào manager. Request tiếp theo do đó có thể chạy trước các thao tác này. Test xác nhận sai thứ tự; chưa đo mất receipt trên production hay khẳng định đây là lỗ hổng vượt xác thực.

Hướng sửa đề xuất, chưa triển khai: chỉ cho request tiếp theo đi qua sau khi InitConnection xử lý thành công; có thể xử lý handshake đồng bộ trên từng connection. Khi handshake bị drop/lỗi, không đánh dấu đã sẵn sàng.

**5. P2 — splitBatch không tính overhead protobuf nên vượt giới hạn cấu hình**

[ĐỌC CODE] [splitBatch](../execution/pkg/rollup/raftfeed/node.go:443) cộng `len(tx.Marshal())`, nhưng output là `pb.Transactions`, có thêm tag và length prefix cho mỗi transaction. Output không được kiểm tra lại kích thước.

[ĐO] `TestAuditSplitMustRespectSerializedLimit`: giới hạn 272 bytes nhưng piece đầu ra dài 278 bytes. Đây là số từ [log test](evidence/system_review_round1_20261021/reproduction.log), không phải ước lượng hiệu năng.

Hướng sửa đề xuất, chưa triển khai: tính đúng kích thước encoding của repeated message, kiểm tra piece cuối cùng và thêm test đúng sát biên cấu hình. Chưa có bằng chứng lỗi này riêng lẻ làm fork hoặc crash node.

**Chạy lại bằng chứng**

Từ thư mục gốc `metanode`, tạo Go overlay bằng đường dẫn workspace hiện tại:

```bash
python3 - <<'PY'
import json
from pathlib import Path
root = Path.cwd().resolve()
ev = root / 'note/evidence/system_review_round1_20261021'
mapping = {
    'pkg/network': 'network_audit_test.go.txt',
    'pkg/rollup/raftfeed': 'raft_audit_test.go.txt',
    'pkg/blockchain': 'durability_audit_test.go.txt',
}
overlay = {'Replace': {
    str(root / 'execution' / package / 'review_audit_test.go'): str(ev / fixture)
    for package, fixture in mapping.items()
}}
Path('/tmp/metanode-review-overlay.json').write_text(json.dumps(overlay))
PY
cd execution
go test -vet=off -overlay /tmp/metanode-review-overlay.json \
  ./pkg/network ./pkg/rollup/raftfeed ./pkg/blockchain \
  -run TestAudit -count=1 -timeout=45s -v
```

Kỳ vọng trên commit được rà: exit code 1, năm test FAIL như log. `-vet=off` chỉ dùng cho overlay thêm file ảo: Go vet trên môi trường này báo không tìm thấy file vật lý; baseline đã chạy không tắt vet. Trong quá trình dựng fixture, đã sửa lỗi setup stake DB nil và import alias trước lần tái hiện cuối; đó không phải lỗi production được báo ở đây.

**Phạm vi vòng tiếp theo**

- Rust BFT: commit attestation, cold-start, epoch transition, đồng bộ khi reconnect.
- Execution: so sánh kết quả native fast path/Block-STM/barrier, nonce và fee; kiểm tra xử lý root lỗi.
- Recovery: thứ tự durable block/changelog/NOMT, replay sau crash, snapshot Raft so với execution state.

Ưu tiên xử lý phát hiện 1 trước. Báo cáo này không kết luận hệ thống đã an toàn hoặc không còn lỗi ngoài các mục trên.
