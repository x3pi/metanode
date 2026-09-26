# Kế hoạch triển khai N1 và N4

> Ngày lập: 2026-09-26  
> Baseline: nhánh `chore/handover-2026-09-26`, commit `69c2f28b`  
> Phạm vi: N1 durability audit; A1 → A2 → B1 thuộc N4  
> Trạng thái: N1 `☑` (N1-BASE, N1-FIX-01..05 hoàn tất), A0 `☑` (đã chốt văn bản), A1 `☑` (hoàn tất tài liệu và sơ đồ Raft SMR), A2 `☑` (chốt schema, bỏ state 20, chuẩn hóa recordRole), B1 `☑` (FSM Next chuẩn hóa, 286 Cartesian triples pass, FuzzMutualExclusion 109k execs pass)

## 1. Mục tiêu và nguyên tắc

Kế hoạch này biến yêu cầu N1/N4 thành các PR nhỏ, có bằng chứng kiểm thử và không đánh dấu hoàn thành dựa trên tài liệu hoặc kết quả tạm thời.

Các nguyên tắc bắt buộc:

1. Không coi dữ liệu là committed nếu store mà state/header tham chiếu tới chưa được ghi bền hoặc chưa có recovery path được kiểm chứng.
2. Nếu persistence thất bại hoặc chưa xác minh xong, block/commit phải fail-closed hoặc giữ pending; không tiếp tục bằng giả định.
3. Không dùng timeout, `sleep` hay thời gian cục bộ để quyết định dispatch commit. Timeout trong test chỉ là watchdog để phát hiện test treo.
4. Mỗi lỗ hổng durability có PR riêng, test hồi quy riêng và số đo chi phí riêng.
5. A2 và phần sửa B1 phải ở cùng một PR để tài liệu và code không lệch nhau tại bất kỳ commit merge nào.
6. Chỉ đổi `☐`/`◐` thành `☑` khi artifact, test output và bằng chứng PR thực sự tồn tại trong Git.

## 2. Baseline đã kiểm tra

Các lệnh sau pass tại thời điểm lập kế hoạch:

```bash
cd execution
go test -race -count=1 ./pkg/rollup
go test -count=1 ./pkg/storage ./pkg/smart_contract_db
go test -tags c0spike -run '^$' ./cmd/simple_chain
```

Kết quả này chỉ xác nhận baseline compile/unit/race hiện tại. Nó không chứng minh các store chịu được `kill -9`, không chứng minh power-loss safety và chưa đóng Definition of Done của B1.

Các vấn đề đã thấy trước khi triển khai:

- `N1_DURABILITY_REPORT.md` chưa tồn tại.
- `BlockChain.Commit()` chuyển dirty mapping sang goroutine rồi trả về trước khi `BatchPut` hoàn tất.
- `commitToMemoryParallel()` ghi trong comment rằng `PersistAsync` chạy inline nhưng account, stake và receipts thực tế vẫn chạy bằng goroutine.
- Lỗi của các goroutine persistence chủ yếu chỉ được log, không truyền về đường commit.
- `CommitBlockState()` chỉ `SyncDurable` block DB; `simple_chain` hiện mở block, mapping, receipt, transaction, code và smart-contract DB thành các database vật lý riêng.
- Event log được `BatchPut` nhưng chưa có durability barrier riêng.
- C0 chỉ kill theo progress của block, chưa kill tại ranh giới ghi/sync của từng store.
- Test Descartes B1 chỉ bắt buộc các cặp bất hợp lệ trả lỗi; chưa bắt buộc mọi cặp hợp lệ phải thành công.
- Chưa có Go fuzz target `Fuzz...` thật.
- Tài liệu vẫn có state `20 OBSERVED`, code không có; `Event.Role` trong code là optional.
- N0 từng quan sát một divergence chưa giải thích. C0 phải tiếp tục ở `◐`; N1/N4 có thể làm độc lập, nhưng C1 không được mở vì lý do đó.

## 3. Sơ đồ thứ tự công việc

```text
P0 sửa trạng thái/kết luận sai trong tài liệu kế hoạch
├── N1-BASE: inventory + report + failpoint + oracle + benchmark baseline
│   ├── N1-FIX-01 mapping durability/order
│   ├── N1-FIX-02 account/stake/receipt async persistence và error propagation
│   ├── N1-FIX-03 transaction/receipt durability
│   ├── N1-FIX-04 event-log/contract-storage durability
│   ├── N1-FIX-05 backup/progress/device-key durability
│   └── N1-FIX-06 explorer/index recovery (nếu audit chứng minh cần sửa)
└── A0 acceptance gate
    └── A1 tài liệu Raft
        └── A2 + B1 schema/code/tests
```

Không bắt đầu B2 trước khi A2+B1 đạt `☑`. Không bắt đầu C1 trước khi N0/C0 được đóng đúng tiêu chí.

## 4. PR P0 — Sửa nguồn sự thật của kế hoạch

### Công việc

1. Xóa tuyên bố mâu thuẫn rằng C0 đã hoàn tất trong khi cùng tài liệu vẫn ghi divergence chưa giải thích.
2. Giữ C0 ở `◐` cho tới khi N0 có kết luận.
3. Ghi rõ N1 và N4 có thể triển khai độc lập với N0.
4. Đồng bộ trạng thái giữa:
   - `NEXT_STEPS_PLAN.md`;
   - `SEQUENCER_STEP_BY_STEP_PLAN.md`;
   - `README.md` của thư mục rollup;
   - `C0_VERIFICATION_REPORT.md` nếu báo cáo đang tuyên bố quá mức bằng chứng.

### Nghiệm thu

- Không còn tài liệu nào vừa ghi divergence chưa giải thích vừa đánh C0 `☑`.
- Không thay đổi production code.
- PR có bảng trước/sau cho mọi status bị đổi.

## 5. PR N1-BASE — Báo cáo thật và harness kiểm chứng

### 5.1. Artifact bắt buộc

Tạo file:

```text
execution/pkg/rollup/N1_DURABILITY_REPORT.md
```

Báo cáo phải được sinh từ audit thật, không điền `Đạt` trước khi có test. Mỗi dòng store có các cột:

| Cột | Nội dung |
|---|---|
| Logical store | Tên store mà code sử dụng |
| Physical path/domain | Thư mục DB thực tế và có dùng chung physical DB hay không |
| Backend | Lazy Pebble, Pebble `NoSync`, NOMT, Xapian, Memory/Dummy |
| Writer | Hàm/call site thực hiện ghi |
| Scheduling | Đồng bộ, goroutine, worker queue |
| Referenced by | Header root, state root, recovery metadata hay chỉ lookup index |
| Commit point | Thời điểm hệ thống hiện coi dữ liệu là committed |
| Durability barrier | `SyncDurable`, NOMT commit, Xapian commit hoặc không có |
| Error propagation | Trả lỗi, chỉ log, hay bỏ qua |
| Loss impact | Fork/root sai, không replay được, thiếu recovery, hoặc chỉ mất index |
| Rebuild path | Có thể rebuild từ nguồn canonical nào |
| Test/evidence | Test ID, lệnh chạy, kết quả và PR |

### 5.2. Inventory tối thiểu

Audit cả logical store và physical store sau:

1. Block DB và `lastBlockHashKey`.
2. Block-number → hash mapping.
3. Tx-hash → block-number mapping.
4. Transaction state.
5. Receipts.
6. Smart-contract bytecode.
7. Smart-contract event log (`dbSmartContract`).
8. Contract storage trie.
9. Account state NOMT/MPT.
10. Stake state NOMT/MPT.
11. Trie database và state changelog.
12. Backup DB block payload.
13. Consensus progress keys: GEI, commit index, epoch, executed commit hash.
14. Backup device-key store.
15. Explorer/Xapian và index-range sidecar.
16. File backup `last_block.dat` và epoch/recovery sidecars.

### 5.3. Timeline phải dựng

Với một block state-changing, báo cáo phải ghi thứ tự thực tế của:

```text
SmartContractDB.Commit
txDB.Commit / receipts.CommitPipeline
account/stake CommitPipeline
account/stake/receipt PersistAsync
enqueue CommitJob
SaveLastBlock
mapping Commit/BatchPut
block DB SyncDurable
NOMT CommitAsync
DoneChan
backup worker
explorer worker/Commit
```

Mọi writer chạy sau durability barrier phải được đánh dấu rõ. Không suy luận từ comment; phải đối chiếu code thực thi.

### 5.4. Failpoint cho `c0_spike`

Thêm hook chỉ tồn tại dưới `//go:build c0spike`. Production build dùng no-op hook.

Failpoint tối thiểu:

```text
after-logical-write
after-batch-put
before-sync-durable
after-sync-durable
before-block-barrier
after-block-barrier
before-nomt-commit
after-nomt-commit
before-done-signal
after-done-signal-before-backup
```

Coordinator đợi marker failpoint rồi gửi `SIGKILL`. Không chèn `sleep` vào production path để tạo race.

### 5.5. Oracle từng store

Sau restart, ngoài block hash và roots, harness phải kiểm trực tiếp:

| Store | Oracle |
|---|---|
| Block DB | Đọc block bytes bằng hash; header/GEI/tx count khớp clean run |
| Mapping | Cả number→hash và tx→block lookup đúng |
| Transaction state | Đọc lại từng transaction và kiểm `TransactionsRoot` |
| Receipts | Đọc từng receipt, status/log đúng và `ReceiptRoot` khớp |
| Bytecode | `codeHash → bytecode` đúng từng byte |
| Contract storage | Mọi slot đã chạm đúng; storage root/account root khớp |
| Event logs | Lookup theo log hash/address/topic trả đủ dữ liệu |
| Account/stake | NOMT handle root, origin root và header root giống nhau |
| Backup | Deserialize được và chạy recovery/sync fixture thành công |
| Progress keys | GEI/commit index/epoch không vượt quá durable block |
| Device key | Lookup đúng hoặc chứng minh rebuild path |
| Explorer | Search trả đủ tx; nếu mất index, canonical chain vẫn không đổi và reindex phục hồi được |

### 5.6. Phân biệt SIGKILL và power loss

`kill -9` không làm mất Linux page cache, vì vậy chưa chứng minh `pebble.NoSync` an toàn khi mất điện. Báo cáo phải tách:

- SIGKILL/userspace-buffer result;
- injected I/O/fsync failure result;
- power-loss/VM hard-stop result nếu có môi trường chạy;
- giới hạn chưa kiểm chứng nếu chưa chạy được power-loss test.

Không được dùng kết quả SIGKILL để tuyên bố đã chứng minh hardware power-loss safety.

## 6. PR N1-FIX — Quy tắc cho từng lỗ hổng

Mỗi lỗ hổng có một PR riêng. Không gom nhiều durability domain vào cùng PR trừ khi chúng dùng chung một atomic barrier và không thể tách hợp lý.

Mỗi PR phải gồm:

1. Root cause và invariant bị vi phạm.
2. Blast radius: writer, reader, recovery, sync và API query.
3. Test failpoint tái hiện lỗi trên code cũ.
4. Bản sửa tối thiểu.
5. Mutation evidence: bỏ/revert dòng sửa làm test thất bại.
6. Đo latency và throughput trước/sau.
7. Cập nhật đúng dòng trong `N1_DURABILITY_REPORT.md`.
8. Không đổi store từ rebuildable thành consensus-critical nếu không có quyết định kiến trúc.

### Thứ tự ứng viên cần kiểm chứng

#### N1-FIX-01 — Mapping

- Kiểm chứng race giữa `BlockChain.Commit()` goroutine và block DB barrier.
- Chứng minh hậu quả của việc mất number→hash hoặc tx→block mapping.
- Ưu tiên API commit có completion/error rõ ràng thay vì fire-and-forget goroutine.
- Không thêm worker/channel mới nếu synchronous batch nhỏ là đủ.

#### N1-FIX-02 — Async persistence và error propagation

- Đối chiếu comment “inline” với code account/stake/receipt goroutine.
- Kiểm `persistReady` khi persistence trả lỗi.
- Chứng minh block không được công bố thành công khi store consensus-critical thất bại.
- Mọi goroutine phải có owner, completion signal và đường trả lỗi; không chỉ log rồi tiếp tục.

#### N1-FIX-03 — Transaction và receipts

- Chốt chúng là replay-critical hay rebuildable.
- Nếu header root tham chiếu dữ liệu không thể rebuild, barrier phải có trước commit acknowledgement.
- Test success, revert, event-heavy và block nhiều transaction.

#### N1-FIX-04 — Smart contract event/storage

- Kiểm event log chỉ là index hay là nguồn recovery/sync.
- Kiểm contract storage NOMT/MPT ở cả `CommitAllStorage` và `LateBindRoots`.
- Test deploy, update nhiều slot, phát nhiều event và restart N+1.

#### N1-FIX-05 — Backup/progress/device key

- Progress key không được đi trước durable canonical block.
- Backup mất không được làm node bỏ qua commit chưa durable.
- Nếu dữ liệu chỉ phục vụ sub-node sync, phải có retry/rebuild path được test.

#### N1-FIX-06 — Explorer

- Explorer không được nằm trên consensus-critical path.
- Lỗi index phải quan sát được và không được đánh dấu range đã index thành công.
- Reindex phải phục hồi đủ dữ liệu từ canonical block/tx/receipt stores.

## 7. Benchmark durability

Không mặc định thêm một fsync cho mỗi logical store. Trước khi sửa phải xác định physical durability domain và xem có thể gom barrier hay không.

### 7.1. Quy tắc gom barrier vật lý (Barrier Coalescing Pattern)

- **Nguy cơ sụt giảm TPS:** Nếu thêm `SyncDurable()` riêng rẽ cho từng kho (block DB, mapping, receipts, tx_state, event logs, code storage), mỗi block sẽ tốn 5–10 lần `fsync()` đĩa riêng biệt. Tần suất này sẽ bóp nghẹt I/O, đẩy p99 latency lên cao và làm sụt giảm nghiêm trọng TPS của chuỗi.
- **Xác định physical domain:** Cần phân định rõ các logical store nào thực tế dùng chung một physical database instance (ví dụ: chung Pebble DB hoặc chung WAL/directory) và kho nào tách biệt hoàn toàn.
- **Gom barrier ở cuối commit cycle:** Thay vì fsync rải rác ở từng hàm thành phần, tất cả các cập nhật trạng thái trong block phải được ghi vào dirty batch trước, và chỉ kích hoạt một barrier đồng bộ tập trung duy nhất ở ranh giới commit của block (`CommitBlockState`), ngay trước khi phát tín hiệu `DoneChan`.
- **Ngoại lệ:** Chỉ áp dụng barrier độc lập nếu luồng đó chạy bất đồng bộ hoàn toàn ngoài consensus loop và không giữ khóa giao dịch.

### 7.2. Workload benchmark


1. Block native transfer, không deploy code.
2. Block deploy contract.
3. Block cập nhật nhiều storage slot và phát nhiều event.
4. Block lớn có nhiều tx/receipt/mapping.
5. Block không state-changing.

Mỗi kết quả ghi:

- p50, p95, p99 và max commit latency;
- số lần fsync/block;
- số bytes và số entries mỗi batch;
- TPS trước/sau;
- disk/filesystem/mount options;
- warm-cache và cold-start;
- ít nhất 3 lượt chạy, cùng config và cùng workload.

Không ghi một con số TPS đơn lẻ là bằng chứng đủ.

## 8. A0 acceptance gate trước A1

Kế hoạch chính ghi `A0 → A1`. Trước khi A1 được đánh `☑`, phải chốt bằng văn bản:

1. Mở rộng Gateway/`PerChainAllocation` hiện tại hay xây `NodeFloatAccount` song song.
2. Ownership của `NodeFloatAccount`, `ClaimedMessages`, nonce và replay guard.
3. Quyền gọi Transfer, MarkClaimed, Refund và Reclaim.
4. Luồng hiện có nào được reuse, thay thế hoặc giữ song song.

Nếu chưa chốt A0, A1 chỉ có thể cập nhật phần topology Raft và vẫn phải giữ `◐`.

## 9. PR A1 — Viết lại tài liệu theo Raft

### File phải cập nhật

- `SEQUENCER_DESIGN.md`;
- `SEQUENCER_DIAGRAMS_AND_OPEN_ISSUES.md`;
- các link/trạng thái liên quan trong README và kế hoạch chính.

### Nội dung bắt buộc

1. Gỡ banner chuyển tiếp/pre-Raft.
2. Mô tả đúng `consensus_mode = "raft"` trong `simple_chain`, không tạo binary/RPC mới.
3. Pipeline: gom tx → tạo batch → Raft propose → majority durable commit → `FSM.Apply` → `ExecutableBlock` → Go execution.
4. Chỉ leader nhận/propose batch; replica không tự đọc external event rồi tự đổi chain state.
5. External action và response thành công chỉ xuất từ entry đã Raft commit.
6. Leader election, failover, add/remove replica và catch-up.
7. Shared signing key: ownership, rotation, recovery và operational risk.
8. `CommitIndex`, snapshot và restart semantics.
9. Sơ đồ và bảng state phải khớp từng cạnh.
10. Không mô tả timeout bầu leader như timeout quyết định dispatch state/commit.

### Nghiệm thu A1

- Không còn đoạn topology pre-Raft trái mục 0.1/0.6.
- Sơ đồ và prose thống nhất về commit point.
- A0 đã được chốt hoặc A1 vẫn giữ `◐` với blocker rõ ràng.
- PR chỉ đánh A1 `☑` khi reviewer có thể lần theo đầy đủ propose → commit → apply → action.

## 10. PR A2+B1 — Chốt schema và FSM trong cùng PR

### 10.1. Quyết định state `20 OBSERVED`

Không quyết định chỉ dựa trên việc code hiện chưa có state 20. Trước hết phải chốt durability model của action:

#### Phương án A — Giữ `OBSERVED`

Dùng khi chưa có durable outbox:

```text
NONE --CreditObserved--> OBSERVED (persist observation)
OBSERVED --submit MarkClaimed--> pending claimed state
parent confirmation --> credit/refund action
```

Phải định nghĩa event/cạnh vào-ra, replay và crash ở mọi điểm.

#### Phương án B — Bỏ `OBSERVED`

Chỉ hợp lệ khi action được lưu trong durable outbox cùng transaction với state:

```text
NONE --CreditObserved--> 21/22 + durable MarkClaimed action
worker retry cùng action id cho tới confirmation
```

State 21/22 phải có resume rule phát lại đúng action chưa hoàn tất. Không được persist state rồi chỉ giữ action trong memory.

Quyết định được khuyến nghị: chọn theo thiết kế B2. Nếu B2 chỉ lưu `RollupRecord` và chưa có durable outbox, giữ một pre-action state rõ ràng để không có crash window.

### 10.2. Quyết định `Role`

`role` tiếp tục nằm trong `RollupRecord` như invariant persisted:

- `SENDER` chỉ dùng sender states/events;
- `RECEIVER` chỉ dùng receiver states/events;
- `StateNone` vẫn phải được kiểm bằng role của record;
- unknown/mismatch role trả lỗi, giữ nguyên state và không phát action.

Không để `Event.Role` optional. Chọn một API và ghi cố định trong schema:

```go
Next(current State, recordRole Role, event Event)
```

hoặc bắt buộc `Event.Role != RoleUnknown`. Phương án truyền `recordRole` riêng được ưu tiên vì Role là thuộc tính record, không phải dữ liệu do event tự khai.

### 10.3. Một transition table làm nguồn sự thật

Tạo table-driven fixture mô tả mỗi cạnh:

- from state;
- role;
- event và điều kiện;
- to state;
- actions đầy đủ;
- forward hay idempotent replay;
- invalid variants.

Test và tài liệu 1.7 phải được đối chiếu với cùng danh sách cạnh. Không dùng một `allowedPairs` rời dễ lệch khỏi test forward transition.

### 10.4. Test B1 bắt buộc

1. Phủ mọi forward edge.
2. Phủ mọi replay edge và xác nhận không phát action lần hai.
3. Tích Descartes `(State, Role, EventType)`:
   - cặp có trong bảng với fixture hợp lệ phải thành công;
   - cặp không có trong bảng phải trả lỗi;
   - lỗi giữ nguyên state và không phát action.
4. Kiểm chính xác action type, target, amount và outcome từng cạnh.
5. Mutation `Event.Value` sau `Next` không đổi bất kỳ `Action.Amount` nào.
6. `nil`, zero, negative và giá trị lớn cho mọi cạnh phát amount.
7. Biên `uint64`: exact threshold, sớm một đơn vị, overflow và max values.
8. Unknown state/event/role/outcome không panic.
9. Terminal state không đi sang terminal outcome đối nghịch.
10. Go fuzz target thật, ví dụ `FuzzMutualExclusion`, chứng minh không thể vừa success vừa refunded.
11. Nếu giữ state 20: fuzz/replay phải gồm toàn bộ cạnh mới.
12. Nếu bỏ state 20: test durable-action/resume thuộc B2 phải được ghi là dependency bắt buộc, không tuyên bố crash-safe chỉ bằng unit test B1.

### 10.5. File phải đồng bộ trong cùng PR

- `SEQUENCER_SCHEMAS_AND_TEST_PLAN.md`;
- `SEQUENCER_IMPLEMENTATION_PLAN.md`;
- `SEQUENCER_DESIGN.md` và diagrams nếu state graph đổi;
- `types.go`;
- `statemachine.go`;
- `statemachine_test.go` và fuzz tests;
- bảng trạng thái trong `SEQUENCER_STEP_BY_STEP_PLAN.md`;
- `NEXT_STEPS_PLAN.md`.

### 10.6. Nghiệm thu A2+B1 (ĐÃ HOÀN TẤT `☑`)

**Lệnh chạy kiểm chứng thực tế:**
```bash
cd execution
go test -v -race -count=1 ./pkg/rollup
go test -run=^$ -fuzz=FuzzMutualExclusion -fuzztime=10s ./pkg/rollup
```

**Bằng chứng kiểm thử:**
- `TestValidTransitionsTable`: Đạt 7/7 kịch bản (Sender Happy Path, Refund Path, Reclaim Won/Lost; Receiver Happy Path, Refund Path, Duplicate).
- `TestCartesianProductRejection`: Đạt 286/286 triples (`13 states * 2 roles * 11 event types`), xác nhận 29 valid edge variants passed và 260 illegal combinations bị từ chối sạch sẽ, không đổi state và không phát action dở dang.
- `TestReplayNeverEmitsActions`: Đạt 14/14 replay idempotency cases (0 actions emitted).
- `TestRoleInvariantValidation`: Đạt kiểm tra bắt buộc role invariant (`RoleUnknown`, invalid role, state-role mismatch, event-declared role mismatch đều bị từ chối).
- `TestBigIntAliasingMutation`: Đạt (mutation `Event.Value` sau `Next` không làm đổi `Action.Amount`).
- `TestReclaimUint64OverflowSafety`: Đạt (chống tràn số `uint64` khi cộng timeout).
- `TestValueValidation`: Đạt (chặn `nil`, 0, âm).
- `FuzzMutualExclusion`: Đạt 109.585 lần thực thi song song trên 104 worker mà không có bất kỳ vi phạm nào (không thể vừa SUCCESS vừa REFUNDED).

A2 và B1 đã đạt `☑`:
- Schema (`SEQUENCER_SCHEMAS_AND_TEST_PLAN.md`) và Code (`types.go`, `statemachine.go`) đã đồng bộ 100%: bỏ hoàn toàn state 20 `OBSERVED`, chuẩn hóa `Next(current State, recordRole Role, event Event)`.
- Mọi cạnh trong bảng 1.7 đều có test và khớp từng trường.
- Tích Descartes hai chiều pass 100%.
- Fuzz target `FuzzMutualExclusion` pass 100%.
- Không cần tăng `schema_version` (vẫn là version 1).

## 11. Cổng build và CI cho mọi PR có code

Sau mỗi PR sửa Go/Rust/FFI hoặc commit path:

```bash
cd execution
go test -race -count=1 <packages-bi-anh-huong>

cd ../consensus/metanode/scripts
./build_check.sh
```

Nếu đụng commit pipeline, state, Block-STM hoặc Gateway, chạy thêm trên cụm local được phép:

```bash
./ci.sh run-now --reset
```

Bằng chứng PR phải lưu:

- SHA được test;
- câu lệnh đầy đủ;
- exit code;
- output tóm tắt;
- benchmark trước/sau;
- đường dẫn artifact/report;
- xác nhận mutation test thất bại khi bỏ bản sửa.

Không cập nhật `PROJECT_STRUCTURE.md` cho thay đổi logic/test nội bộ. Chỉ cập nhật nếu tạo module/package, entrypoint, FFI/proto hoặc cross-layer channel mới.

## 12. Checklist bàn giao cuối

### N1

- [ ] `N1_DURABILITY_REPORT.md` tồn tại trong Git.
- [ ] Mọi store trong inventory có backend, barrier, impact và test evidence.
- [ ] Kill test chạy tại failpoint từng store, không chỉ sau block.
- [ ] SIGKILL và power-loss claims được phân biệt.
- [ ] Mọi store fork/replay-critical có barrier hoặc recovery proof.
- [ ] Mỗi lỗ hổng có PR/test/benchmark riêng.
- [ ] `build_check.sh` và CI sạch.

### A1

- [ ] A0 acceptance gate được chốt hoặc blocker được ghi rõ.
- [ ] Không còn banner/topology pre-Raft.
- [ ] Propose/commit/apply/action và failover được mô tả nhất quán.
- [ ] Sơ đồ khớp prose và commit semantics.

### A2

- [ ] State 20 được giữ/bỏ với lý do crash-safety cụ thể.
- [ ] Role được chốt thành persisted invariant.
- [ ] Schema version/compatibility được quyết định.
- [ ] Tất cả tài liệu state graph được đồng bộ.

### B1

- [ ] Mọi cạnh được table-test.
- [ ] Descartes kiểm cả valid và invalid pairs.
- [ ] Replay không phát action lần hai.
- [ ] Không alias `*big.Int`.
- [ ] Biên `uint64` đầy đủ.
- [ ] Fuzz target thật pass.
- [ ] Không thể vừa success vừa refunded.
- [ ] `go test -race -count=1 ./pkg/rollup` sạch.

## 13. Definition of Done tổng thể

N1 và N4 chỉ hoàn thành khi tất cả điều sau cùng đúng:

1. Report và code tồn tại trong Git, không chỉ trong output tạm hoặc mô tả PR.
2. Không còn store consensus/replay-critical được ghi sau commit acknowledgement mà không có completion/error propagation.
3. Crash/restart cho từng store cho kết quả giống clean run hoặc có rebuild path được chứng minh.
4. Schema, diagrams, FSM code và tests khớp từng cạnh.
5. Không có commit/action được dispatch dựa trên timeout cục bộ hoặc dữ liệu chưa verified.
6. Build Go, Rust và FFI sạch; race tests sạch; CI zero-fork sạch.
7. Các status A1, A2, B1, N1 chỉ đổi sang `☑` trong PR chứa bằng chứng tương ứng.

## 14. Kế hoạch thực thi từng phase ("Chậm mà chắc")

Để đảm bảo an toàn hệ thống, kiểm soát chặt chẽ blast radius và không để xảy ra sai sót hoặc hồi quy, khối lượng công việc được chia thành 5 phase tuần tự. Mỗi phase có phạm vi độc lập, tiêu chí nghiệm thu rõ ràng và có thể tạo PR riêng biệt:

### 📌 Phase 0 (PR P0) — Chuẩn hóa nguồn sự thật tài liệu
- **Mục tiêu:** Xóa bỏ mâu thuẫn về trạng thái C0/N0 giữa các file tài liệu trước khi tiến hành code.
- **Nội dung thực hiện:**
  1. Chuyển trạng thái C0 về `◐` trong `NEXT_STEPS_PLAN.md`, `C0_VERIFICATION_REPORT.md` và `SEQUENCER_STEP_BY_STEP_PLAN.md` do vẫn còn divergence chưa giải thích tại N0.
  2. Khẳng định rõ ràng N1 và N4 được tiến hành độc lập với N0.
- **Tiêu chuẩn nghiệm thu:**
  - Không sửa bất kỳ dòng code nào.
  - Tất cả các file kế hoạch đồng nhất về trạng thái `◐` của C0 và trạng thái mở của N1/N4.

---

### 📌 Phase 1 (PR N1-BASE) — Khảo sát thực tế 16 kho & Khởi tạo N1_DURABILITY_REPORT.md
- **Mục tiêu:** Kiểm tra code thực tế để lập bảng ma trận bền vững của mọi kho dữ liệu và thiết lập failpoint harness cho spike.
- **Nội dung thực hiện:**
  1. Khảo sát chi tiết call sites: `CommitBlockState`, `SmartContractDB.Commit()`, `BlockChain.Commit()`, `commitToMemoryParallel()`.
  2. Tạo file `execution/pkg/rollup/N1_DURABILITY_REPORT.md` THẬT với bảng ma trận 16 kho (Physical path, Backend, Writer, Scheduling, Durability barrier, Hậu quả nếu mất ghi gần nhất, Phân biệt SIGKILL vs Power Loss).
  3. Bổ sung các Failpoint hooks vào `cmd/simple_chain/c0_spike.go` (chỉ kích hoạt với `-tags c0spike`): `after-logical-write`, `after-batch-put`, `before-sync-durable`, `before-block-barrier`, `before-nomt-commit`...
  4. Thiết lập công cụ đo benchmark baseline (latency p50/p99, TPS, số lần fsync/block).
- **Tiêu chuẩn nghiệm thu:**
  - File `N1_DURABILITY_REPORT.md` tồn tại trong Git với dữ liệu audit từ code thật.
  - `go test -tags c0spike -run '^$' ./cmd/simple_chain` compile sạch sẽ.

---

### 📌 Phase 2 (PR A0 + A1) — Chốt kiến trúc A0 & Hoàn thiện đặc tả Sequencer Raft A1
- **Mục tiêu:** Giải quyết dứt điểm các câu hỏi kiến trúc nền tảng và viết lại tài liệu thiết kế Sequencer theo chuẩn Raft.
- **Nội dung thực hiện:**
  1. **A0:** Chốt bằng văn bản phương án mở rộng Gateway/`PerChainAllocation` vs `NodeFloatAccount`, quyền sở hữu nonce và replay protection.
  2. **A1:** Cập nhật `SEQUENCER_DESIGN.md` và `SEQUENCER_DIAGRAMS_AND_OPEN_ISSUES.md`:
     - Gỡ bỏ hoàn toàn banner pre-Raft.
     - Mô tả chi tiết pipeline: Propose batch → Majority commit → `FSM.Apply` → `ExecutableBlock` → Execution.
     - Đặc tả cơ chế bầu leader, failover, snapshot và xử lý partition.
- **Tiêu chuẩn nghiệm thu:**
  - Sơ đồ tuần tự và nội dung tài liệu thống nhất 100% với mục 0.1/0.6.
  - Reviewer có thể lần theo trọn vẹn luồng từ khi nhận transaction đến khi commit và phát sinh external action.

---

### 📌 Phase 3 (PR A2 + B1) — Đồng bộ Schema, FSM Code & Kiểm thử toàn diện
- **Mục tiêu:** Đồng bộ tuyệt đối giữa đặc tả schema và code bộ chuyển trạng thái (FSM) trong cùng một PR duy nhất.
- **Nội dung thực hiện:**
  1. **A2:** Chốt trạng thái `20 OBSERVED` (giữ nếu chưa có Durable Outbox ở B2, bỏ nếu đã có Outbox). Khóa chặt `Role` vào `RollupRecord` (`recordRole`).
  2. **B1 Code:** Chuẩn hóa API `Next(current State, recordRole Role, event Event)` trong `statemachine.go`, ánh xạ chính xác 100% các cạnh trong bảng 1.7.
  3. **B1 Tests (`statemachine_test.go`):**
     - Table-driven test cho mọi forward transition và replay idempotent.
     - Test tích Descartes 3 chiều `(State, Role, EventType)` kiểm 2 chiều: cặp hợp lệ phải thành công; cặp bất hợp lệ phải trả lỗi, giữ nguyên state và không phát action.
     - Test immutability: sửa đổi `Event.Value` sau khi gọi `Next` không ảnh hưởng `Action.Amount`.
     - Test biên `uint64` (overflow, exact threshold).
     - Viết Go native fuzz test: `FuzzMutualExclusion` (chứng minh record không thể vừa success vừa refunded).
- **Tiêu chuẩn nghiệm thu:**
  - `go test -race -count=1 ./pkg/rollup` PASS sạch.
  - `go test -run=^$ -fuzz=FuzzMutualExclusion -fuzztime=60s ./pkg/rollup` PASS.
  - `./consensus/metanode/scripts/build_check.sh` PASS sạch không cảnh báo.

---

### 📌 Phase 4 (Các PR N1-FIX-01 → N1-FIX-06) — Khắc phục các lỗ hổng Durability
- **Mục tiêu:** Xử lý triệt để từng lỗ hổng tìm thấy từ Phase 1 theo nguyên tắc PR độc lập và gom barrier vật lý.
- **Thứ tự thực hiện:**
  - **N1-FIX-01 (Mapping):** Chuyển việc ghi mapping từ goroutine fire-and-forget sang cơ chế đồng bộ hoặc có completion barrier rõ ràng trước khi báo block commit.
  - **N1-FIX-02 (Async persistence & Error propagation):** Đảm bảo lỗi từ goroutine lưu trữ account, stake, receipts được truyền về caller, không chỉ log đơn thuần; hoàn thiện xử lý `persistReady`.
  - **N1-FIX-03 (Transaction & Receipts):** Áp dụng quy tắc gom barrier vật lý tại `CommitBlockState`.
  - **N1-FIX-04 (Event log & Contract storage):** Đảm bảo event logs và storage trie được commit bền vững trước khi acknowledge commit.
  - **N1-FIX-05 (Consensus progress keys & Backup):** Đảm bảo progress keys (GEI, epoch, commit index) không đi trước block durable.
  - **N1-FIX-06 (Explorer / Indexer):** Đảm bảo lỗi index không làm sai lệch canonical chain và có đường dẫn reindex phục hồi.
- **Tiêu chuẩn nghiệm thu mỗi PR:**
  - Có test failpoint tái hiện lỗi trên code cũ và pass trên code mới.
  - Mutation test chứng minh test sẽ fail nếu bỏ bản sửa.
  - Đo lường latency (p50/p99) và TPS trước/sau để đảm bảo không làm sụt giảm hiệu năng.
  - Cập nhật dòng tương ứng trong `N1_DURABILITY_REPORT.md`.

