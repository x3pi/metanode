# Kế hoạch hoàn thiện PR N1 + N4

> Ngày rà soát: 2026-09-26
>
> Phạm vi duy nhất: N1 và A1 → A2 → B1 (N4)
>
> Trạng thái hiện tại: **CHƯA SẴN SÀNG TẠO PR**

## 1. Kết luận rà soát hiện tại

Các test race liên quan và `build_check.sh` đang chạy sạch, nhưng chưa đủ để nghiệm thu. Các blocker còn lại:

1. `commitWorker` không truyền lỗi `CommitBlockState` cho caller. Đóng `DoneChan` khi commit lỗi làm caller không phân biệt được thành công và thất bại; job không có `DoneChan` chỉ ghi log rồi bị bỏ qua.
2. Mapping đã được ghi đồng bộ nhưng chưa bền vững. `LazyPebbleDB.BatchPut` chỉ ghi vào memory cache; barrier của block DB không sync database mapping vật lý riêng biệt.
3. C0 spike mới kill theo `commit_progress.txt`, chưa kill theo từng marker trước/sau ghi hoặc durability barrier của từng kho.
4. Test failpoint hiện không có assertion nên không chứng minh marker/hook hoạt động.
5. `N1_DURABILITY_REPORT.md` có nội dung không khớp code hiện tại và chưa có bằng chứng kill-per-store, injected I/O failure, mutation test và chi phí fsync/block.
6. Tài liệu đang vừa đánh N1 hoàn tất vừa ghi N1 chưa làm.
7. Worktree có B2 RecordStore/protobuf ngoài phạm vi yêu cầu và cần được chuyển sang branch/PR riêng.
8. **Contract Storage NOMT Atomicity Gap (Lỗ hổng Zero-Fork - Bàn giao kỹ thuật):** Phát hiện tại failpoint `after-mapping-barrier` của block #2; mutation bị áp dụng lần 2 khi replay sau crash. Do đòi hỏi thay đổi sâu kiến trúc quản lý session NOMT handle hoặc two-phase commit giữa Go và FFI Rust, lỗi này được lập hồ sơ bàn giao kỹ thuật tại [HANDOVER_NOMT_CONTRACT_STORAGE_ATOMICITY.md](./HANDOVER_NOMT_CONTRACT_STORAGE_ATOMICITY.md) để bàn giao cho chuyên gia storage/core giải quyết trong PR riêng; tuyệt đối không nới lỏng test để che giấu lỗi.

Không được đánh N1, A1, A2 hoặc B1 là `☑` chỉ dựa trên build/test tổng quát; mỗi mục phải có artifact và bằng chứng tương ứng trong PR. Trạng thái N1 tiếp tục giữ `◐`.

## 2. Phạm vi giữ lại và phạm vi tách ra

### 2.1. Giữ trong chuỗi PR N1/N4

- `cmd/simple_chain/c0_spike.go`
- `pkg/failpoint/*`
- Các thay đổi durability trong `pkg/blockchain`, `pkg/smart_contract_db` và test hồi quy tương ứng
- `N1_DURABILITY_REPORT.md`
- `SEQUENCER_DESIGN.md`
- `SEQUENCER_DIAGRAMS_AND_OPEN_ISSUES.md`
- `SEQUENCER_SCHEMAS_AND_TEST_PLAN.md`
- `SEQUENCER_STEP_BY_STEP_PLAN.md`
- `statemachine.go`
- `statemachine_test.go`

### 2.2. Tách sang branch/PR B2, không xóa vĩnh viễn

- `pkg/rollup/record.go`
- `pkg/rollup/store.go`
- `pkg/rollup/store_test.go`
- `pkg/proto/rollup.proto`
- `pkg/proto/rollup.pb.go`
- `pkg/proto/rollup_vtproto.pb.go`
- Phần N5/B2 trong `NEXT_STEPS_PLAN.md`
- Các mô tả B2 tương ứng trong `PROJECT_STRUCTURE.md`

`pland.md` là transcript làm việc, không phải artifact nghiệm thu và không đưa vào PR.

## 3. Thứ tự triển khai

### P0 — Làm sạch phạm vi và trạng thái tài liệu

1. Lưu công việc B2 trên branch hoặc commit riêng.
2. Loại B2 và các tuyên bố N5/B2 khỏi diff N1/N4.
3. Chuyển N1 về `◐` hoặc `☐`; bỏ mọi tuyên bố hoàn tất chưa có bằng chứng.
4. Sửa `git diff --check` sạch, gồm trailing whitespace và blank line cuối file.

**Cổng hoàn tất:** diff chỉ còn file thuộc N1 hoặc N4; không có trạng thái mâu thuẫn.

### P1 — Sửa fail-closed cho đường commit

1. Thay tín hiệu `DoneChan chan struct{}` không mang kết quả bằng cơ chế trả kết quả có `error`, hoặc bổ sung result channel có ownership rõ ràng.
2. Chỉ phát tín hiệu thành công sau khi toàn bộ durability barrier bắt buộc hoàn tất.
3. Khi commit lỗi, không cập nhật progress, không broadcast/index/backup như block đã commit và không để fence sau đó che mất lỗi trước.
4. Với pipeline không chờ từng block, lưu lỗi đầu tiên trong state được bảo vệ đồng bộ; fence/`WaitForPersistence` phải trả lại lỗi đó.
5. Không dùng timeout hoặc sleep để biến commit chưa xác minh thành thành công. Trạng thái phải fail-closed/pending.

**Test bắt buộc:**

- Inject lỗi `CommitBlockState`, caller nhận đúng lỗi.
- Không phát success signal khi commit lỗi.
- Fence sau một job lỗi vẫn trả lỗi job trước.
- Không chạy các bước hậu commit sau lỗi.
- `go test -race -count=1 ./cmd/simple_chain/processor` sạch.

### P2 — Hoàn thành inventory N1 bằng bằng chứng code

Lập bảng cho từng logical store và physical durability domain:

- Block DB
- Block-number → hash mapping
- Tx-hash → block mapping
- Receipts
- `transaction_state`
- Smart-contract event logs (`dbSmartContract`)
- Smart-contract bytecode
- Contract storage, account state và stake state
- Backup/sub-node data
- Explorer index
- Consensus progress/checkpoint metadata nếu được cập nhật trên cùng đường commit

Mỗi dòng bắt buộc có:

- File/hàm ghi dữ liệu.
- Backend thực tế: Lazy Pebble, Pebble `NoSync`, NOMT, Memory hoặc external index.
- Physical database dùng chung hay riêng.
- Durability barrier thực tế và vị trí của nó.
- Thời điểm dữ liệu được coi là committed hoặc được tham chiếu bởi state khác.
- Hậu quả nếu mất lần ghi cuối: sai state root, thiếu dữ liệu replay, mất progress, hay chỉ mất index có thể rebuild.
- Cách phục hồi đã được code/test chứng minh; không ghi “rebuildable 100%” nếu chưa có test.

### P3 — Sửa từng lỗ hổng durability bằng PR riêng

#### P3.1. Mapping

1. Chứng minh bằng test rằng synchronous `BatchPut` vẫn mất dữ liệu Lazy Pebble khi process chết trước flush.
2. Thêm durability barrier cho mapping trước khi block được báo committed, hoặc chuyển mapping vào cùng physical durability domain với block DB.
3. Nếu barrier lỗi, trả lỗi lên commit worker theo P1.
4. Có mutation test: bỏ barrier thì test phải fail.

#### P3.2. Các store còn lại

Với mỗi store có hậu quả consensus/replay:

1. Tạo reproduction độc lập.
2. Sửa tối thiểu, không gom nhiều lỗi không liên quan vào cùng PR.
3. Thêm regression test thất bại khi gỡ fix.
4. Ghi rõ số fsync phát sinh và durability ordering trước/sau.

Store chỉ chứa index có thể rebuild không mặc nhiên cần fsync/block, nhưng phải có test rebuild và phải bảo đảm mất index không ảnh hưởng canonical state hoặc replay.

### P4 — Mở rộng C0 spike theo từng store boundary

1. Định nghĩa danh sách failpoint ổn định, tối thiểu gồm trước/sau:
   - code write và code barrier;
   - event-log write và event-log barrier;
   - mapping write và mapping barrier;
   - block write và block barrier;
   - NOMT payload commit/boundary phù hợp.
2. Crash coordinator phải chờ đúng `failpoint_<name>.marker`, sau đó gửi `SIGKILL` cho worker.
3. Marker phải chứa block number, failpoint name và run ID để không đọc nhầm marker cũ.
4. Workload phải thực sự chạm store đang kiểm tra: deploy/call contract, ghi storage, phát event, nhiều tx/receipt/mapping và nhiều block liên tiếp.
5. Sau restart, so với clean run:
   - block hash từng height;
   - account, stake, transaction và receipt root;
   - code hash/code bytes;
   - contract storage và event logs;
   - mapping/RPC lookup;
   - khả năng replay block kế tiếp.
6. Tách rõ kết luận `SIGKILL` và power-loss: `kill -9` không chứng minh `NoSync` an toàn khi mất điện.

**Test failpoint bắt buộc:** build thường chứng minh no-op; build `-tags c0spike` assertion hook và marker được tạo đúng; lỗi ghi marker phải được nhìn thấy thay vì bị bỏ qua.

### P5 — Đo chi phí durability

Đo cùng workload, cùng cấu hình và cùng thiết bị:

- Baseline trước fix và kết quả sau từng fix.
- Số lần `SyncDurable`/fsync trên mỗi block.
- Commit latency p50/p95/p99.
- TPS và độ sâu commit queue.
- Block có và không có deploy/event/storage write.

Báo cáo cả giá trị tuyệt đối và phần trăm thay đổi. Không tuyên bố “zero-cost” nếu chỉ suy luận từ nhánh code.

### P6 — Chốt A1 → A2 → B1

#### A1

1. Đối chiếu hai tài liệu thiết kế với mục 0.1 và 0.6.
2. Gỡ toàn bộ banner hoặc mô tả “pre-Raft”.
3. Mô tả thống nhất propose → majority durable commit → apply → durable block → external action.
4. Không mô tả timeout như điều kiện dispatch commit.

#### A2

1. Chốt bỏ state 20 `OBSERVED` để khớp code hiện tại, hoặc bổ sung nó đồng thời vào code/test; không để hai nguồn sự thật khác nhau.
2. Chốt `RoleSender`/`RoleReceiver` và quy tắc role của từng state.
3. Cập nhật bảng 1.7 thành nguồn sự thật duy nhất.

#### B1

Test table-driven phải xác minh cho mọi cạnh:

- State đích.
- Toàn bộ danh sách action theo đúng thứ tự, không chỉ action đầu tiên.
- `Action.Type`, target, amount và outcome.
- Tích Descartes từ chối mọi cặp/triple không có trong bảng.
- Replay không phát action lần hai.
- Mutation `Event.Value` không đổi `Action.Amount`.
- Biên `uint64`, bao gồm overflow.
- Property/fuzz không thể đi tới cả success và refunded.

**Cổng hoàn tất:** `go test -race -count=1 ./pkg/rollup` sạch và log test được đính kèm PR. Chỉ lúc đó mới đánh A1, A2 và B1 `☑`.

## 4. Cấu trúc PR bắt buộc

1. **PR N4-A1-A2-B1:** tài liệu Raft, schema, FSM và test; không chứa B2.
2. **PR N1-harness-report:** inventory ban đầu và spike/failpoint; trạng thái N1 còn `◐` nếu chưa kiểm đủ store.
3. **PR N1-mapping-durability:** fix mapping, regression/mutation test và benchmark.
4. **Một PR cho mỗi lỗ hổng N1 khác:** không gộp các physical store không liên quan.
5. **PR N1-FIX-NOMT-ATOMICITY (Bàn giao kỹ thuật):** Tách thành PR riêng giao cho nhân sự chuyên trách storage/core để giải quyết dứt điểm Atomicity Gap của Contract Storage NOMT theo đặc tả tại [HANDOVER_NOMT_CONTRACT_STORAGE_ATOMICITY.md](./HANDOVER_NOMT_CONTRACT_STORAGE_ATOMICITY.md).
6. **PR B2 riêng:** chỉ mở sau khi scope hiện tại hoàn tất hoặc được ưu tiên lại.

## 5. Checklist nghiệm thu cuối

- [ ] `git diff --check` sạch.
- [ ] Không còn B2 trong diff N1/N4.
- [ ] Commit failure được truyền đến caller; không có false-success `DoneChan`.
- [ ] `N1_DURABILITY_REPORT.md` khớp code và có bảng đầy đủ.
- [ ] Kill theo từng failpoint/store đã chạy và lưu kết quả so sánh clean/recovery.
- [ ] Có injected failure và mutation test cho từng fix.
- [ ] Có số đo fsync/block, latency và TPS trước/sau.
- [ ] `go test -race -count=1 ./pkg/rollup` sạch.
- [ ] Race test các package commit liên quan sạch.
- [ ] `consensus/metanode/scripts/build_check.sh` sạch.
- [ ] `ci.sh run-now --reset` sạch nếu đây là cổng CI của PR.
- [ ] Chỉ đánh `☑` cho artifact đã tồn tại và bằng chứng nằm trong PR.
