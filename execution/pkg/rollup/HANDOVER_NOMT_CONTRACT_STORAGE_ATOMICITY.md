# Tài Liệu Bàn Giao Kỹ Thuật (Handover Issue): Lỗ Hổng Bất Đối Xứng Tính Nguyên Tử Của Contract Storage NOMT (Zero-Fork Blocker)

> **Mã định danh:** `HANDOVER-NOMT-ATOMICITY-01`  
> **Mức độ nghiêm trọng:** 🔴 **CRITICAL / ZERO-FORK INVARIANT VIOLATION**  
> **Trạng thái:** 🟡 **Có bản sửa chờ quyết định (2026-09-26)** — `verify` đạt 7 kill point nhưng bản sửa đổi quy tắc đồng thuận (hard fork), xem `NOMT_STORAGE_ATOMICITY_DESIGN.md` mục "Rà soát và xác minh"  
> **Thuộc phạm vi gốc:** N1 (Durability & Crash Recovery)  
> **Ngày lập:** 2026-09-26  

---

## 1. Lý Do Bàn Giao (Handover Reason)

Trong khuôn khổ đợt công việc **N1 & N4**, nhóm đã hoàn tất việc rà soát và bổ sung các rào cản bền vững (durability barriers) cũng như cơ chế commit fail-closed:
1. `BlockChain.Commit()`: Chuyển mapping sang đồng bộ và bổ sung `SyncDurable(mappingDB)`.
2. `SmartContractDB.Commit()`: Bổ sung `SyncDurable(codeStorage)` và `SyncDurable(dbSmartContract)` (event logs).
3. `CommitBlockState()`: Bổ sung `SyncDurable` cho Receipts và Transaction State.
4. `CommitWorker`: Fail-closed khi commit đĩa gặp lỗi (không nâng GEI, hủy phát DoneChan).
5. `statemachine.go` (N4): Kiểm chứng 100% tích Descartes và fuzz testing.

Tuy nhiên, trong quá trình chạy kiểm thử **C0 Spike Crash Recovery Matrix**, hệ thống đã phát hiện một lỗi bất đối xứng tính nguyên tử (Atomicity Gap) giữa **Contract Storage NOMT** và **Canonical Account State / Block DB**.

> ⚠️ **Quyết định bàn giao:** Việc khắc phục triệt để lỗi này đòi hỏi thay đổi kiến trúc quản lý session của NOMT handle (Rust FFI + Go wrapper) hoặc thiết kế cơ chế Two-Phase Commit / State Changelog cho Contract Storage. Công việc này vượt quá phạm vi của PR N1/N4 hiện tại (đang tập trung vào barrier và fail-closed của pipeline Go). Để đảm bảo tính minh bạch, tuân thủ nguyên tắc **Part 2.5 Zero-Fork Invariant** ("thà pending chứ tuyệt đối không fork, không nới test che lỗi"), lỗi này được ghi nhận chính thức và bàn giao cho chuyên gia storage/core blockchain xử lý trong một PR riêng biệt.

---

## 2. Mô Tả Lỗi (Issue Description) & Cơ Chế Sinh Lỗi

### 2.1. Hiện tượng
Tại kịch bản crash `crash_after_mapping_k2` (failpoint `after-mapping-barrier` của block #2):
1. Block #1 đã canonical (`storage.GetLastBlockNumber() == 1`).
2. Block #2 đến và được thực thi bởi EVM. Block #2 chứa giao dịch tương tác smart contract làm thay đổi storage slot (ví dụ hàm `increment()` tăng counter từ 0 lên 1).
3. Ngay sau khi thực thi giao dịch, `tx_processor.go` gọi `SmartContractDB.LateBindRoots()` để tính toán `StorageRoot` gắn vào `AccountState`.
4. Trong `LateBindRoots()`, code gọi:
   ```go
   // execution/pkg/smart_contract_db/smart_contract_db.go:480-486
   if nomtTrie, ok := t.(*trie.NomtStateTrie); ok {
       if err := nomtTrie.CommitPayload(); err != nil { ... }
   }
   ```
   Hàm này gọi FFI `fs.CommitPayload(n.handle)`, **ghi bền vững ngay lập tức (persist) dữ liệu storage slot mới (counter = 1) vào file database Beatree của NOMT trên đĩa**.
5. Ngay sau đó, tiến trình bị `SIGKILL` tại failpoint `after-mapping-barrier` của block #2 — **trước khi** `blockDatabase` kịp ghi và fsync block #2, và **trước khi** `AccountState` NOMT hoàn tất ghi bền.
6. Khi node restart:
   - Node đọc `storage.GetLastBlockNumber()` ra block #1 $\rightarrow$ block #1 là canonical tip.
   - Node chuẩn bị nhận và replay lại block #2.
7. Khi replay block #2:
   - Giao dịch của block #2 thực thi lại.
   - Smart contract đọc storage slot từ NOMT $\rightarrow$ **đọc ra giá trị 1** (vốn đã bị ghi bền từ lần chạy trước khi crash!).
   - Giao dịch thực hiện mutation lần thứ hai: tăng counter từ 1 lên 2!
   - `LateBindRoots()` tính ra `StorageRoot` dựa trên counter = 2.
   - `AccountStatesRoot` của block #2 sau khi replay khác hoàn toàn với lần chạy sạch (clean run, vốn counter = 1).
   - Block hash của node sau replay bị lệch so với toàn mạng $\rightarrow$ **XẢY RA NETWORK FORK NGAY LẬP TỨC**.

---

## 3. Các Điểm Mã Nguồn Bị Ảnh Hưởng (Affected Code Locations)

1. **[smart_contract_db.go:480-486](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/smart_contract_db/smart_contract_db.go#L480-L486):**
   - `nomtTrie.CommitPayload()` ghi đĩa quá sớm khi block mới chỉ đang ở giai đoạn draft/processing trong bộ nhớ.
2. **[block_processor_sync.go:80-145](file:///home/abc/nhat/con-chain-v2/metanode/execution/cmd/simple_chain/processor/block_processor_sync.go#L80-L145):**
   - Cơ chế `NOMT-SYNC-RECOVERY` và `nomtAheadReplay` chỉ kiểm tra và đồng bộ `"account_state"` và `"stake_db"`, hoàn toàn bỏ quên namespace `"smart_contract_storage"`.
3. **[chain_state.go:358-390](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/blockchain/chain_state.go#L358-L390):**
   - `updateStateForNewHeader()` chỉ gọi `AlignWithExpectedRoot()` cho Account và Stake. Không có cơ chế realign storage trie cho smart contracts về `StorageRoot` của header block trước đó.
4. **[nomt_state_trie.go:435, 808, 905](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/trie/nomt_state_trie.go#L435):**
   - Namespace `"smart_contract_storage"` bị `skipRegistry` và **không được gán `StateChangelogDB`**, khiến hệ thống không thể quay lui (undo) các slot đã ghi.

---

## 4. Cách Tái Hiện (Reproduction Steps)

Chạy kịch bản C0 Spike (lệnh cũ `go test -run TestC0CrashRecoveryMatrix` **không tồn tại**; dùng `verify`, tái hiện trong ~5 giây):
```bash
cd execution
go build -tags c0spike -o /đường/dẫn/c0spike ./cmd/simple_chain
C0_LARGE_BLOCK=1 /đường/dẫn/c0spike -config cmd/simple_chain/config.json -tool-c0-spike verify -c0-blocks 3 -c0-rounds 2
# tuỳ chọn: mỗi block đụng 2 hợp đồng
C0_TWO_CONTRACTS=1 C0_LARGE_BLOCK=1 /đường/dẫn/c0spike ... verify -c0-blocks 4 -c0-rounds 2
```
Quan sát kết quả ở kịch bản `crash_after_mapping_k2`:
- Worker bị kill tại block #2 sau khi mapping barrier hoàn tất.
- Sau restart từ block #1, node replay block #2.
- Log phát hiện: `AccountStatesRoot` hoặc `BlockHash` của block #2 sau recovery không khớp với `cleanRun`.

---

## 5. Khuyến Nghị Kỹ Thuật Cho Người Nhận Bàn Giao

Người tiếp nhận xử lý có thể lựa chọn 1 trong 3 phương án sau:

### Phương án 1: Trì hoãn CommitPayload (Two-Phase Commit cho Contract Storage) — Khuyên dùng
- **Ý tưởng:** Trong `LateBindRoots()`, chỉ gọi `session.Finish()` để chốt Merkle root trong bộ nhớ (đủ để lấy hash gán vào `AccountState`), **KHÔNG gọi `CommitPayload()`**.
- Lưu lại danh sách các `pendingFinishedSession` của contract storage.
- Chỉ gọi `CommitPayload()` ghi đĩa đồng bộ trong `CommitBlockState()` CÙNG LÚC với `AccountState` (sau khi các barrier an toàn đã vượt qua).
- **Thách thức:** Do engine NOMT dùng chung handle `"smart_contract_storage"` cho mọi hợp đồng, cần kiểm tra xem NOMT FFI có cho phép mở session mới khi session trước đã `Finish()` nhưng chưa `CommitPayload()` hay không. Nếu không, cần cơ chế batching gộp chung tất cả storage updates của các contract trong block vào 1 session duy nhất.

### Phương án 2: Bổ sung StateChangelog cho Contract Storage
- Khởi tạo và gán `StateChangelogDB` cho namespace `"smart_contract_storage"`.
- Khi node restart phát hiện `localTip == N-1` nhưng NOMT storage đã ghi ở block $N$, đọc changelog của block $N$ và ghi đè lại các giá trị cũ của block $N-1$ trước khi cho phép EVM replay.

### Phương án 3: Khôi phục Contract Storage View theo Root của Tip
- Trong `UpdateStateForNewHeader()`, duyệt qua các contract có thay đổi và reset view/trie về `StorageRoot` nằm trong canonical `AccountState` của block $N-1$.

---

## 6. Tiêu Chí Nghiệm Thu (Acceptance Criteria)

- [ ] Toàn bộ kịch bản trong `verify` (C0 spike) (đặc biệt là `crash_after_mapping_k2` và các điểm crash trước Block DB); `verify` dừng ở kịch bản lỗi đầu tiên nên phải chạy đủ cả 7-8 kịch bản chạy PASS 100%.
- [ ] So sánh sau recovery: `AccountStatesRoot`, `StorageRoot`, `EventLogs`, và `BlockHash` phải trùng khớp 100% với lần chạy sạch (`cleanRun`).
- [ ] Tuyệt đối KHÔNG nới lỏng điều kiện so sánh trong `c0_spike.go`.
- [ ] `consensus/metanode/scripts/build_check.sh` biên dịch sạch cả Go, Rust và FFI không có warning/error.
- [ ] Đo đạc và đính kèm benchmark I/O/fsync trước và sau khi fix.
