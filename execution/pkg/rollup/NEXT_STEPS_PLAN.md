# Kế hoạch các việc tiếp theo (bàn giao cho agent/developer)

> **Cập nhật:** 2026-09-25. **Cơ sở:** `origin/dev` tại `a52d28f2` (đã gồm PR #128, #129, #130).
> Đọc trước: `README.md` (mục lục thư mục này), `SEQUENCER_STEP_BY_STEP_PLAN.md` (mục 0.1, 0.6, 2, 3), `SEQUENCER_SCHEMAS_AND_TEST_PLAN.md`.
> Tài liệu này **không thay** kế hoạch chính; nó xếp thứ tự và đặt tiêu chí nghiệm thu cho các việc kế tiếp. ID `N1..N9` ở đây khác ID `A/B/C/F` của kế hoạch chính (mỗi việc ghi rõ bước tương ứng).

---

## 0. Quy tắc chung (bắt buộc)

**Ràng buộc kỹ thuật (AGENTS.md):**
- **Zero-Fork:** thà pending chứ không fork. Không dùng `sleep`/timeout để quyết định dispatch (timeout chỉ được dùng cho bầu leader/heartbeat của Raft).
- Mọi queue/worker mới phải có giới hạn bộ đệm rõ ràng; không I/O chặn trong vòng lặp async.
- Không đổi hành vi đường Rust mặc định (`consensus_mode` rỗng). Hook vào code cũ chỉ dạng mặc định-tắt.
- Sau khi sửa code: chạy `consensus/metanode/scripts/build_check.sh` (Go + Rust + FFI) sạch, không cảnh báo, và dán kết quả vào PR.
- Cập nhật `PROJECT_STRUCTURE.md` khi thêm package/entrypoint/cờ/khoá cấu hình. Code comment bằng tiếng Anh; cuối mỗi phản hồi có khối tóm tắt tiếng Việt theo mẫu ở AGENTS.md.

**Quy trình git (đã có sự cố, đừng lặp lại):**
- Mỗi việc một nhánh + một `git worktree` riêng, mở PR vào `dev` (cần 1 approve). Không đẩy thẳng lên `dev`.
- **Không `git add <thư mục>`**: add từng file theo tên. Hai agent dùng chung một thư mục làm việc đã làm file scratch (`test_*.go`, `check_*.go`, `package main`, có khoá riêng) lọt vào `dev` và làm hỏng `go build ./pkg/...`. Code thử viết ở thư mục ngoài repo.
- Sau khi PR được squash-merge, kiểm lại nội dung thật: `git show origin/dev:<đường-dẫn>` (đã có tiền lệ commit bị mất khi squash).
- Không commit khoá/token/secret. Không dán token vào chat.
- **Không đụng cụm thật 231/230.** Cụm thử cục bộ là máy `192.168.1.232` (4 validator + node-4 sync-only, cổng RPC 10746–10750). `./ci.sh run-now --reset` **xoá dữ liệu và deploy lại** cụm cục bộ, mất khoảng 70 phút.

**Quy tắc bằng chứng (nhiều kết luận sai đã xảy ra vì thiếu điều này):**
- Không ghi PASS nếu không đo. Mọi con số phải đến từ lệnh chạy thật và ghi kèm lệnh.
- Benchmark: dùng đối tượng mới mỗi vòng (cache trong `ToEthTransaction`/`types.Sender` làm số đo thấp giả gấp ~50 lần), **mỗi vòng phải thành công** (không đo nhánh từ chối sớm), và so với một cấp độ hợp lý.
- Khi agent khác báo kết quả: chạy lại trước khi ghi vào tài liệu.

---

## 1. Hiện trạng đã kiểm chứng (đừng làm lại)

| Chủ đề | Kết quả | Nguồn |
|---|---|---|
| Gỡ `RecoveryCommittee` | Đã kiểm trên cụm thử: TPS ~7598–7675 tx/s, chaos restart 6 vòng, zero-fork (`ci.sh run-now --reset`, hai lần) | README, commit `0a30dfc1` |
| B1 state machine (`pkg/rollup`) | Có, `go test -race` pass; còn ◐ vì chờ A2 (trạng thái 20 `OBSERVED`, `Role`) | PR #128 |
| C0 spike (`-tags c0spike`) | 2 vòng × 2 tiến trình giống hệt; workload Native + EVM + Gateway `outbound()`; `kill -9` tại block 1, 2, N-1 → bypass có đối chiếu GEI + số tx, hash lịch sử trùng lần chạy sạch; `InitFFIBridge`=0, 0 thread tokio/consensus, 0 socket LISTEN thuộc tiến trình | PR #130, `C0_VERIFICATION_REPORT.md` (sinh bởi spike, không commit) |
| Lỗi bền vững thật (đã sửa) | Kho `smart_contract_code` (Lazy Pebble, đệm 5s) không được fsync sau khi ghi bytecode → sau crash mã hợp đồng mất, replay cho hash khác. Sửa: `SyncDurable(codeStorage)` trong `SmartContractDB.Commit()` + 3 test hồi quy | PR #130 |
| `transactionsRoot`/`receiptRoot` | Bộ cộng dồn theo **từng block** (`FlatStateTrie`), không phải cây Merkle | `SEQUENCER_ERC20_STANDARD_TX_PROOF.md` F0-1/F0-2 |
| Chữ ký giao dịch Ethereum | Node tự ký BLS bằng khoá cổng; chỉ ECDSA (R,S,V) chứng minh người dùng; R,S,V giữ nguyên kể cả EIP-1559 | F0-7, F0-10 |
| Receipt lỗi | Revert, hết gas, "intrinsic gas too low" đều `status 0` + `Error(string)`; RPC không trả `Exception` | F0-10 |
| Chi phí | ECDSA ~54 µs/lần; Gateway cũ `outbound` 0,7 → 5,4 → 31 ms khi tích luỹ 50 → 500 → 3.000 message (tăng tuyến tính, blob JSON nạp/ghi cả khối mỗi giao dịch barrier) | F0-8, F0-11 |
| Không dùng Rust consensus | Đúng ở chế độ raft; nhưng binary vẫn link `libmetanode`, NOMT (Rust) vẫn dùng cho state, MVM/Xapian là C++ | xem N8 |

**Đã ☑:** A0, A1, A2, B1, N7, **C0** (theo D1, xem mục 2), **C1 / N3** (2026-09-26), **C2** (2026-09-26, tập con khả thi — xem N3b), **C4** (2026-09-27, xem N3c). **Còn ◐ (chưa ☑):** N1. **Chưa làm:** B2 (đã tách sang branch riêng `feat/rollup-b2-store`), B3–B9, C3, C5–C6, D1, F1–F6.

**Bẫy đã gặp khi chạy:**
- Spike cần genesis có ví gửi `0x294f…f846` và `0x6169…9C49`; dùng `deploy/systemd/genesis.json` (do `ci.sh` sinh). Với `cmd/simple_chain/config.json` mặc định (genesis không có ví) spike thoát lỗi rõ ràng.
- Log `FAST-PATH-NONCE-REJECT` khi chạy spike là bình thường (Block-STM chạy giao dịch nonce liên tiếp song song rồi rơi về đường chậm).
- `go test -race ./cmd/simple_chain/processor` có 1 test fail **có sẵn**: `TestSubscribeProcessor_ConcurrentAccess` (data race ở `subscribe_processor.go:47/54`). Dùng `-skip TestSubscribeProcessor_ConcurrentAccess` cho tới khi xong N7.

**Lệnh chạy spike:**
```
cd execution
go build -tags c0spike -o /tmp/sc ./cmd/simple_chain
/tmp/sc -config <config.json> -tool-c0-spike verify -c0-blocks 5 -c0-report stdout
```

---

## 2. Việc chờ quyết định của chủ dự án (agent không tự quyết)

| ID | Quyết định | Vì sao chặn |
|---|---|---|
| D1 | ~~Đánh dấu **C0 ☑** hay giữ ◐~~ **ĐÃ QUYẾT (2026-09-26): chủ dự án yêu cầu tiếp tục kế hoạch → C0 ☑** | Ghi nhận rõ **điều kiện chưa đo** khi ☑: (a) độ trễ commit p50/p95/p99 và số `fsync()` vật lý với tải contract; (b) `kill -9` trên cluster nhiều máy (CI chaos chỉ restart êm, chưa từng kích hoạt rollback); (c) `verify` cố định hạt giống theo số vòng nên các vòng lặp lại cùng cấu hình. Bằng chứng đã có: T-DET (2000 + 600 vòng không lệch, 200 vòng workload hai hợp đồng), `verify` 7-8 kill point trên `dev` sau merge, CI `spam_contract`/`node_chaos_restart` PASS |
| D2 | Chính sách **A3**: token nào được bảo vệ; chỉ tài khoản ký bằng ví ECDSA được bảo vệ (tài khoản chỉ có BLS nằm ngoài) | Chặn F1–F6 |
| D3 | Duyệt triển khai bản sửa `SyncDurable` lên cụm thật 231/230 | Xem N2 |
| D4 | Thu hồi token GitHub đã dán trong chat | Bảo mật |
| D5 | Có cắt liên kết `libmetanode` khỏi bản build raft hay không | Xem N8 |
| D6 | Thiếu **phí gốc** (native coin) trong P8 kiểm không được từ lịch sử ERC20 — chấp nhận rủi ro báo oan/bỏ sót hay yêu cầu thêm dữ liệu | Chặn F3 |

---

## 3. Danh sách việc, theo thứ tự ưu tiên

### N0 — Đóng nốt tiêu chí thoát C0 (`T-DET-*`) bằng cách mở rộng spike (P0, nhỏ, làm trước N3)
Tiêu chí thoát C0 theo `SEQUENCER_SCHEMAS_AND_TEST_PLAN.md` mục 2.5: **T-DET-01..07 đạt, có tài liệu kết luận; nếu T-DET-01/02 lệch thì dừng, không sang C1.** Đối chiếu với spike hiện tại (`cmd/simple_chain/c0_spike.go`):

| Test | Yêu cầu (mục 2.3) | Hiện trạng | Việc còn lại |
|---|---|---|---|
| T-DET-01 | Cùng chuỗi `ExecutableBlock` (EVM + Gateway) trên 2 tiến trình, **lặp ≥ 20 lần**, so `state_root` và `block_hash` từng block | **Đạt (2026-09-26).** Lệch từng thấy (1/~200 vòng) là **lỗi mất cập nhật của Block-STM** (`ErrEstimateHit` bị nuốt), đã sửa và merge (#132/#133). Sau sửa: hunt 2000 và 600 vòng (2 tiến trình, đổi `GOMAXPROCS` và thứ tự tx) **0 lệch**, 200 vòng workload hai hợp đồng 0 lệch, `verify` trên `dev` sau merge đạt | Giới hạn: `verify` cố định hạt giống theo số vòng nên các lượt lặp lại cùng cấu hình (lỗi phụ thuộc thời điểm chứ không phụ thuộc đầu vào, nên vẫn có giá trị) |
| T-DET-02 | Như trên nhưng có tải song song (Block-STM nhiều worker) | Một phần: block lớn 188 tx (`C0_LARGE_BLOCK=1`, mặc định bật trong `verify`); chưa đổi số worker của Block-STM | Phụ thuộc kết luận T-DET-01; thêm biến thể đổi số worker nếu cấu hình được |
| T-DET-03 | Đổi `GOMAXPROCS`, đổi thứ tự lên lịch goroutine | Đã chạy: tiến trình 1 dùng `GOMAXPROCS` 1/2, tiến trình 2 dùng 4/8 (đan xen theo vòng); **lần lệch nói trên xảy ra ở cặp 1 vs 4** | Phụ thuộc kết luận T-DET-01 |
| T-DET-04 | `Apply` trên 2 replica cho cùng `BatchRecord` → dãy byte `ExecutableBlock` giống hệt | **Chưa thể** (chưa có `FSM.Apply`) | Chuyển sang C2 (ghi rõ trong báo cáo, không tính vào C0) |
| T-DET-05 | Đảo thứ tự tx **trong batch** → kết quả block giống nhau (Go sắp theo hash sau dedup) | Đã chạy: mỗi tiến trình xáo thứ tự tx theo hạt giống riêng (`C0_TX_SHUFFLE_SEED`), hai tiến trình luôn khác thứ tự (bản đầu của agent dùng chung một hạt giống nên **không** kiểm được điều này; đã sửa) | Phụ thuộc kết luận T-DET-01 |
| T-DET-06 | `BlockProcessor` không gọi `InitFFIBridge` vẫn commit block, không panic vì kênh authoritative `nil` | **Đạt** | — |
| T-DET-07 | Link `libmetanode` nhưng không khởi động → không tạo thread/socket/file của Rust | **Đạt** (đo `/proc/self/task`, socket theo inode fd, `InitFFIBridgeCallCount`=0; thread NOMT là ngoại lệ có chủ đích) | Tuỳ chọn: bổ sung `strace -f -e trace=network,execve` |

**Lệch chưa giải thích (2026-09-26) — phải xử lý trước khi ghi Đạt hoặc đánh dấu C0 ☑:** trong một lượt `verify -c0-rounds 20`, **Round 3** báo `Block #4 Hash mismatch` giữa tiến trình 1 (`GOMAXPROCS=1`, hạt giống 1003) và tiến trình 2 (`GOMAXPROCS=4`, hạt giống 2003). Từ log: cùng số tx (188), **cùng `ReceiptsRoot`**, block 1–3 khớp, **block hash khác** ở block 4 (và block 5 lệch theo). Vì `ReceiptsRoot` bằng nhau mà hash khác, gần như chắc chắn `AccountStatesRoot` (hoặc `StakeStatesRoot`/`TransactionsRoot`) khác — nhưng bản chạy đó so hash trước rồi xoá dữ liệu nên **không biết root nào**. Đã thử tái hiện: 12 lần chạy riêng cặp (1, 4) cùng hạt giống, 16 lần dưới tải CPU 96 luồng, rồi thêm 6+ lượt `verify` 20 vòng (mỗi lượt 40 tiến trình) đều giống hệt. Tỉ lệ khoảng 1 trên ~200 vòng, **chưa tái hiện được lần hai**. Đã cải thiện công cụ để lần sau bắt được bằng chứng: `compareBlockRecords` báo **mọi** trường lệch, và `verify` **giữ nguyên thư mục dữ liệu khi lỗi**.
**Nguyên nhân đã tìm ra (2026-09-26), sửa trên nhánh `fix/stm-native-estimate-lost-credit` (chờ review/merge):** lỗi **mất cập nhật trong Block-STM** (`TrueBlockSTM.execOne`, nhánh chuyển native): khi đọc/cộng tiền người nhận gặp `ErrEstimateHit` (người nhận đang có ESTIMATE từ một tx thấp hơn đang chạy lại) thì lỗi bị **bỏ qua** — người gửi đã bị trừ tiền, biên lai "thành công", nhưng người nhận **không được cộng**. Tx nào mất phụ thuộc lịch chạy goroutine nên hai node có thể ra state khác nhau (rủi ro fork thật, không chỉ lỗi của spike). Bằng chứng: test tiến trình `TestTrueBlockSTM_HotRecipientNoLostUpdate_Stress` (cùng dạng block của spike) tái hiện ngay ở vòng đầu với `GOMAXPROCS>=2` trước khi sửa; săn 2000 lượt chạy tiến trình: 2/2000 lệch trước sửa, 0/2000 hai lần sau sửa. Khi kiểm chứng bản sửa còn phát hiện **thêm một lỗi có từ trước**: rò rỉ ESTIMATE gây livelock (tx treo tạm dừng đưa mình vào hàng đợi trước khi khôi phục sổ estimate trong `defer`, worker khác chạy xong rồi `defer` khôi phục "ma" → ESTIMATE vĩnh viễn, mọi tx đọc địa chỉ đó quay vô hạn; khoảng 1 lần/3.5k–9k vòng) — đã sửa cùng nhánh (`markSuspended()`), 3×8000 vòng sạch. **T-DET-01 vẫn CHƯA ghi Đạt** cho tới khi chạy lại `verify -c0-rounds 20` đủ nhiều lượt trên bản đã sửa; và bản sửa là thay đổi consensus-critical: cần nâng cấp đồng bộ mọi node, khuyến nghị `ci.sh run-now --reset` trước khi lên cluster.
**Việc tiếp theo cho N0:** (1) chạy `verify -c0-rounds 20` lặp lại (hàng chục lượt, cả khi máy đang tải) cho tới khi tái hiện hoặc đủ tin cậy thống kê; (2) khi lệch xảy ra: đọc các trường lệch, so trạng thái từng tài khoản giữa hai thư mục dữ liệu (nguồn nghi ngờ: xử lý song song Block-STM, thứ tự áp dụng thay đổi state, dữ liệu phụ thuộc thời gian); (3) chỉ ghi Đạt khi không còn lệch. **Nếu là lệch thật: dừng, báo cáo, không làm C1.**

**Nghiệm thu:** báo cáo spike liệt kê từng `T-DET-*` với trạng thái đạt/không kèm số lần lặp thực tế; T-DET-01/02/03/05 đạt; ghi rõ T-DET-04 chuyển sang C2. **Nếu bất kỳ lần chạy nào lệch hash/state root: dừng, báo cáo, không làm C1.** (C0 tiếp tục giữ trạng thái `◐` cho tới khi N0 giải thích được divergence; N1 và N4 được mở triển khai độc lập).

### N0.5 — Audit `ErrEstimateHit` trong `TrueBlockSTM` (đã rà từng lời gọi; còn 1 quan sát mở)

**Cơ chế:** chỉ có **hai** nguồn ESTIMATE — `MVCCAccountStateDB.AccountState` (`mvcc_state_db.go:40-50`) và `MVCCSmartContractDB.StorageValue` (`mvcc_smart_contract_db.go:66-75`); cả hai đặt `BlockingVersion` (cờ dính, không bao giờ tự xoá trong một incarnation). Mọi `Set*/AddBalance/SubBalance` của `MVCCAccountStateDB` đều đi qua `AccountState`, và sau lần đọc thành công địa chỉ đó nằm trong `localState` nên **lần đọc lặp lại không thể gặp ESTIMATE**. Quan trọng: các `Set*` **ghi ngay vào bản đồ MVCC dùng chung** (không chờ cuối `execOne`), nên mọi lần tạm dừng SAU khi đã ghi phải lưu write set (hợp của cũ và mới) qua `suspendOnEstimate(..., mvccDB, scDB)`; truyền `nil, nil` chỉ đúng khi mới chỉ có lần đọc.

| `true_block_stm.go` | Lời gọi | Xử lý ESTIMATE | Kết luận |
|---|---|---|---|
| :497 | `AccountState(from)` | `errors.Is` → `suspendOnEstimate(nil,nil)` :513 (chưa ghi gì; nếu không có phiên bản chặn thì xếp lại tx, không bỏ mất) | An toàn (đã đổi sang hàm chung) |
| :589, :618 | `PlusOneNonce/SetLastHash(fromAddr)` (account-setting) | `fromAddr` đã nằm trong `localState` từ :497 → không thể gặp ESTIMATE | An toàn |
| :681 | `processAuthorizationList` (`authorization.go:67`) | Hàm **nuốt** lỗi bằng `continue`. Chốt ngay sau lời gọi :687-690, `suspendOnEstimate(mvccDB,scDB)` | **Lỗi đã sửa** (test `EstimateHit`, `PartialAuth/second-authority`) |
| :699 | `AccountState(to)` (native) | `errors.Is` → :707 `suspendOnEstimate(mvccDB,scDB)` | **Lỗi đã sửa** (trước đây `nil,nil` sau khi authority đã được ghi; `PartialAuth/native-recipient`) |
| :734, :738 | `SubTotalBalance/PlusOneNonce/SetLastHash(from)` | `from` đã ở `localState` | An toàn |
| :752 | `AddBalance(to)` | `errors.Is` → :760 (PR #132). `to` đã ở `localState` sau :699 nên thực tế không xảy ra, giữ làm phòng thủ | An toàn |
| :779 → :785-793 | `ExecuteTransactionWithMvmId`; VM đọc qua `mvm_api.go:1425` (AccountState → status 3), `:1452` (Code), `:1548-1556` (StorageValue → status suspend) | Lỗi bị VM nuốt/đổi thành receipt "halted", nhưng cờ dính được kiểm tra ngay sau khi VM chạy (:785) trước khi dùng kết quả | **Lỗi đã sửa**: nhánh này trước đây tạm dừng mà **không lưu write set** (authority của 7702 đã ghi); `PartialAuth/contract-call` |
| :844, :864-910 | Áp kết quả VM: `SubTotalBalance`, `SetNonce`, `AddBalance`, `BatchSetStorageValues`, `SetCodeHash`, `SetCreatorPublicKey`, `SetStorageAddress` | Lỗi bị bỏ qua, nhưng cờ dính được kiểm tra lại :922-933 | **Lỗi đã sửa**: trước đây khối này **thay** write set bằng bản ghi dở thay vì hợp với bản cũ, làm mục cũ không còn được theo dõi/dọn |
| :918-919 | `SetLastHash/SetNewDeviceKey(from)` | `from` đã ở `localState` | An toàn |
| `validateOne` :1036 | Đọc thẳng `accountMap/storageMap` (không qua wrapper) | Gặp ESTIMATE ⇒ `blockingVer != Base` ⇒ tx bị huỷ và chạy lại (vô hiệu hoá theo thiết kế) | An toàn |
| `runBarrierTx` :1262, `gateway_handler.go:378` | Dùng `chainState` toàn cục, không có MVCC | Không sinh ESTIMATE | Không áp dụng |

**Đã tìm và sửa (cùng họ, cùng gốc "ESTIMATE bị nuốt / write set không được lưu"):** (1) mất cộng tiền người nhận; (2) ESTIMATE rò rỉ gây livelock (`markSuspended`); (3) bỏ qua uỷ quyền EIP-7702; (4) tạm dừng giữa danh sách uỷ quyền bằng `nil,nil`; (5) đọc người nhận sau khi đã ghi authority bằng `nil,nil`; (6) nhánh contract tạm dừng không lưu write set và khối "áp kết quả" thay vì hợp write set. Số liệu (mỗi cấu hình 2000–3000 vòng, `GOMAXPROCS` 2/4/8/16): chưa sửa — `native-recipient` 40–51/2000 lỗi, `contract-call` 55–70/2000 lỗi; sau sửa — 0 lỗi ở cả 3 chế độ `PartialAuth`, test `EstimateHit` (5 mức GOMAXPROCS) và stress gốc 8000 vòng.

**Quan sát `commitDeviceKeyIfPending` — ĐÃ ĐÓNG (đọc code, 2026-09-26): không phải nguy cơ fork.** `sm.CommitDeviceKey(txHash)` (`pkg/storage/storage_manager.go:273`) chỉ chuyển device key thô từ RAM (`pendingDeviceKeys`, chỉ node nhận tx qua RPC/mempool mới có; `SavePendingDeviceKey`) sang kho **backup cục bộ** `StorageBackupDeviceKey`. Kho này chỉ được đọc bởi các hàm tra cứu phía client (`TransactionProcessor.GetDeviceKey`, `state_processor.go:207/235`), **không** nằm trong state root và **không** ảnh hưởng tx hợp lệ hay không (trạng thái đồng thuận lưu `lastHash/newDeviceKey` qua `mvccDB.SetLastHash/SetNewDeviceKey`, đã đi qua MVCC). `LoadAndDelete` làm nó chạy tối đa một lần cho mỗi `txHash`. Hệ quả tối đa của một incarnation cũ gọi hàm này: một bản backup cục bộ cho tx mà thực thi cuối cùng bỏ qua (dữ liệu thừa, vô hại). Giới hạn của kết luận: chỉ dựa trên đọc các nơi tiêu thụ đã liệt kê, chưa truy vết phía người yêu cầu của `state_processor` ở node khác.

### N1 — Rà soát bền vững các kho có bộ đệm trên đường commit (Trạng thái: `◐` Đang thực hiện)
**Vì sao:** lỗi vừa sửa (`smart_contract_code`) là một trường hợp của lớp lỗi "ghi có đệm rồi crash". Có thể còn kho khác, hậu quả là hash/state lệch giữa các node sau crash (fork).
**Việc:**
1. Liệt kê mọi kho ghi trong `CommitBlockState` và `SmartContractDB.Commit()`/`CommitAllStorage()`: block DB, mapping, receipts, `transaction_state`, kho log sự kiện (`dbSmartContract`), kho lưu trữ hợp đồng, backup, explorer… Với mỗi kho ghi: loại (Lazy Pebble / Pebble `NoSync` / NOMT / Memory), có `SyncDurable` trước khi trạng thái tham chiếu tới nó được coi là commit không, và **cái gì hỏng nếu mất ghi gần nhất** (state root sai, thiếu dữ liệu để replay, chỉ mất chỉ mục tra cứu…).
2. Tham chiếu: `storage.DurableSyncer` (`pkg/storage/storage.go`), barrier trong `CommitBlockState` (commit `634b0d39`), `SyncDurable(codeStorage)`.
3. Mở rộng spike để kiểm bằng `kill -9`: thêm workload chạm từng kho (ví dụ hợp đồng phát event, ghi storage nhiều, nhiều block liên tiếp) và kiểm hash/state root sau recovery so với lần chạy sạch.
4. Với mỗi lỗ hổng tìm được: PR riêng, có test hồi quy **thất bại khi gỡ bản sửa** (bài học: chứng minh bằng mutation), và **đo chi phí fsync mỗi block** trước/sau.
**Tiến độ N1 (2026-09-26):**
- Đã hoàn thành rà soát thực tế 16 kho dữ liệu logic & vật lý trong [N1_DURABILITY_REPORT.md](./N1_DURABILITY_REPORT.md).
- Đã bổ sung durability barrier cho mapping, smart contract events, receipts và transaction state; changelog NOMT được ghi `pebble.Sync` trước khi block được công bố.
- Đã sửa fail-closed error propagation cho commit pipeline (`ErrChan` trong `CommitJob`, loại bỏ false-success `DoneChan`, lưu root error cho fence/`WaitForPersistence`).
- **Lỗ hổng Contract Storage NOMT (`after-mapping-barrier`) đã có bản sửa (2026-09-26, nhánh `fix/nomt-storage-atomicity`):** gộp mọi hợp đồng của block vào một session NOMT, ghi changelog + root từng block, rollback tại chỗ khi khởi động rồi xác minh bằng root đã ghi (fail-closed). Đo được: `verify` đạt cả 7-8 kill point (200 vòng determinism + 10 lần kill -9 recovery, 67/67 rollback khớp root, workload hai hợp đồng); CI `spam_contract` PASS; `node_chaos_restart` PASS 63m7s (676 block × 5 node giống hệt). Chi tiết, các lỗi đã sửa so với bản bàn giao và bằng chứng: [NOMT_STORAGE_ATOMICITY_DESIGN.md](./NOMT_STORAGE_ATOMICITY_DESIGN.md).
- **⚠️ Thay đổi đồng thuận (hard fork), đã được chọn có chủ đích:** bản sửa gán một `StorageRoot` cuối cho mọi hợp đồng bị đụng trong block (code cũ: root sau phiên của riêng từng hợp đồng). Cùng workload cho `account_states_root`/hash khác từ block 2. Cần nâng cấp đồng bộ mọi node và **reset chuỗi** (chuỗi cũ không tái chạy được lịch sử); xem mục 8 trong `note/block_stm_estimate_fixes_upgrade_runbook.md`.
- **Còn ◐, chưa ☑:** chưa đo độ trễ commit p50/p95/p99 và số `fsync()` vật lý mỗi block với tải contract; chưa thử kill -9 trên cluster nhiều máy; chaos chỉ restart êm nên chưa kích hoạt rollback trong CI. C0 (D1) vẫn do người dùng quyết.
**Rủi ro:** thêm fsync trên đường commit mỗi block làm giảm thông lượng — đo bằng `ci.sh` (TPS Blast, so với ~7600 tx/s).

### N2 — Kế hoạch triển khai bản sửa `SyncDurable` lên cụm thật (cần D3)
**Việc:** viết runbook (không tự chạy trên cụm thật): thứ tự nâng cấp (rolling restart từng node, đợi đồng bộ, kiểm zero-fork bằng `block_hash_checker` sau mỗi bước), tiêu chí dừng/quay lại, nhắc **mọi node cùng phiên bản** (tránh chạy lẫn), sao lưu trước, cách kiểm binary thực chạy bằng `md5sum` (bài học: `--restart` không copy binary mới, chỉ `--start --prebuilt-bin` mới copy). Đối chiếu `OPERATIONS_GUIDE.md` và `deploy/ansible/ansible_deploy.sh`.
**Nghiệm thu:** runbook được chủ dự án duyệt; đã thử tập dợt trên cụm cục bộ `.232`.

### N3 — C1: chế độ `raft` chạy trên 1 node (sau D1) — gồm việc guard các điểm gọi Go→Rust
Tham chiếu: kế hoạch chính bước C1, hook H1–H6, `C0_VERIFICATION_REPORT.md` mục 7.
**Việc** (mỗi điểm: fail-closed + test):
- `executor.SubmitTransactionBatch` (`tx_batch_forwarder_core.go`): hiện gọi thẳng `C.metanode_submit_transaction_batch` và **thử lại vô hạn** khi trả `false`. Ở chế độ raft phải đổi đích sang Raft (H2), không được treo hay gọi Rust chưa khởi tạo.
- `GetConsensusVotes`, `GetCommitVotes` (`rpc_block.go`, `mtn_api.go`), `AttestPayloadLoss`, `AttestPayloadLossForCommit` (`admin_api.go`): trả lỗi "unsupported in raft mode" (H4).
- `PauseRustConsensus`, `ResumeRustConsensus`, `InitSnapshotSystem` (`block_processor_core.go`): guard/thay thế (snapshot).
- `RunSocketExecutor` (`peer_discovery_socket.go`): guard theo `ConsensusMode`.
- `ConsensusReady()` hiện luôn `false` ở raft: khi có nguồn cấp block thật phải phản ánh leader + đã bắt kịp tip.
- Chốt `is_authoritative_gei`, ánh xạ `commit_index` uint32 ↔ chỉ số Raft uint64 (quyết định thiết kế mở), snapshot gắn `CommitIndex`.
**Tiến độ (2026-09-26, đã có trong code + test):** package `pkg/rollup/raftfeed` (công tắc `Enabled()` = `consensus_mode` đúng bằng `"raft"`; `Submit`/`Ready` là **stub fail-closed**). Đã guard, mỗi điểm có test (`cmd/simple_chain/raft_guards_test.go`, `cmd/simple_chain/processor/raft_guards_test.go`, `pkg/rollup/raftfeed/raftfeed_test.go`): `GetConsensusVotes`/`GetCommitVotes` (cả `MetaAPI` và `MtnAPI`) và `AttestPayloadLoss`/`AttestPayloadLossForCommit` trả "unsupported in raft mode" (xác thực mật khẩu vẫn chạy trước); `runPeerDiscoverySocket` không bind cổng; callback `PauseRustConsensus`/`ResumeRustConsensus` của snapshot bị bỏ qua; `ConsensusReady()` dùng `raftfeed.Ready()`; `tx_batch_forwarder` gọi `raftfeed.Submit` thay vì `executor.SubmitTransactionBatch`.
**Đã xong ở N3/C1 (2026-09-26):** `raftfeed` là bộ cấp block thật cho 1 node (không còn stub): `Submit` chỉ trả `true` khi batch vào hàng đợi có trần (`SubmitQueueCap`=256; đầy hoặc chưa chạy → `false` để `tx_batch_forwarder` giữ và thử lại, không mất batch); một goroutine đóng dấu timestamp (không bao giờ lùi/lặp), cấp `GlobalExecIndex`/số block liên tiếp, xích `commit_hash`, rồi đẩy `ExecutableBlock` vào hàng đợi nạp block có trần (đầy thì chặn, không bỏ block đã đánh số). `CommitIndex` (uint32) ↔ chỉ số (uint64): **tràn uint32 thì dừng fail-closed**, không quấn vòng. Batch không thể thành block (rỗng/hỏng) bị bỏ và đếm (`Dropped()`), không chặn đường ống. `Ready()` = đang chạy, chưa lỗi, hàng đợi chưa đầy → `eth_consensusReady` phản ánh nguồn cấp thật. Đánh số tiếp nối sau **cả** GEI cuối và commit index cuối đã xử lý khi khởi động lại. `InitSnapshotSystem`: `snapshot_enabled` + `consensus_mode=raft` bị **từ chối lúc khởi động** (`raftfeed.ValidateConfig`) cho tới C4. `commit_index`↔Raft index và snapshot gắn `CommitIndex` được chốt ở C2/C4.
**Bằng chứng C1:** (1) node thật chạy `consensus_mode=raft`, gửi tx qua RPC cũ (`eth_sendRawTransaction`) → receipt `status 0x1`; **`kill -9`** rồi khởi động lại cùng dữ liệu: block và nonce giữ nguyên (không thực thi lại), 3 receipt cũ còn, tx mới vào block tiếp theo; (2) spike cấp block qua `raftfeed.Submit` (`C0_VIA_RAFTFEED=1`, dây nối production tự khởi động feeder) so với cấp trực tiếp: `account/stake/receipts/txs root` **giống hệt** ở cả 5 block, cả workload một và hai hợp đồng (chỉ `hash` khác vì feeder đóng dấu timestamp bằng đồng hồ thật; địa chỉ leader phải trùng, `C0_LEADER_FROM_NODE=1`); (3) đường mặc định không đổi: `batchSubmitter` chọn Raft chỉ khi `consensus_mode` đúng bằng `raft`, `go test -race` sạch cho `raftfeed`/`processor`/`executor`/`config`/`cmd/simple_chain`, `verify` 8 kill point đạt, `build_check` 4/4 0 warning, `tps_blast` PASS ~7013 tx/s.
**Giới hạn đã biết của C1 (không HA, đúng thiết kế):** batch đã được `Submit` nhận (forwarder đã xoá tx khỏi pool) nhưng chưa thực thi sẽ mất nếu node crash; `commit_hash` khởi động lại từ chuỗi rỗng. C2 (log Raft bền, `Submit` = "đã nhân bản") xoá cả hai. **Quan sát có sẵn, không sửa ở đây:** trong `tx_batch_forwarder_core.go` phần ghi nhận sau chuyển tiếp (`AdvanceNoncesCacheForForwarded`, trace `FORWARDED_TO_RUST`, thống kê) nằm sau nhánh thất bại của `if success { break }`, nên chỉ chạy khi thử lại chứ không chạy khi thành công; cần chủ module xác nhận rồi mới đổi vì là đường nóng.
**Nghiệm thu:** cập nhật bảng mục 7 của báo cáo: không còn "CHƯA GUARD"; test cho từng guard; `go test -race` cho processor sạch; spike vẫn pass; `ci.sh run-now --reset` sạch (đường Rust mặc định không đổi).

### N3b — C2: Raft nhân bản batch, FSM cấp block (2026-09-26, tập con khả thi; nhánh `feat/raft-c2`)

**Đã làm** (`execution/pkg/rollup/raftfeed/`, thư viện `hashicorp/raft` v1.7.1 + `raft-boltdb/v2` v2.3.0 — ghim phiên bản cũ vì bản mới đòi Go ≥ 1.26):
- `stamper.go`: **một** nơi đóng dấu block (đánh số, timestamp tăng nghiêm ngặt, xích `commit_hash`), dùng chung cho feeder C1 và FSM Raft ⇒ cùng batch + cùng timestamp ⇒ cùng block.
- `fsm.go`: `Apply` (mọi replica) giải mã `BatchRecord`, kiểm `schema_version`/`tx_count`/batch rỗng — **sai thì replica DỪNG, không bỏ qua** (bỏ qua sẽ làm chuỗi lệch các replica khác); không đọc đồng hồ/ngẫu nhiên (test tĩnh `T-AP-06`). Replay sau restart: block đã bền trong DB **không giao lại** nhưng bộ đếm + xích băm vẫn tiến qua (giống replica không restart). `Snapshot()` **chờ** (theo bộ đếm cục bộ, không theo peer/đồng hồ) tới khi mọi block đã giao đều bền trong DB rồi mới trả về — Raft chỉ thu gọn log tới đúng chỉ số đó, nên crash sau thu gọn không mất entry (`T-AP-07/08`). `Restore()` chỉ khôi phục bộ đếm và **từ chối** nếu DB chưa có block mà snapshot nói (replica dựng lại rỗng ⇒ C4).
- `node.go`: kho log/stable bằng bolt (fsync mỗi ghi), transport TCP, `Bootstrap` một lần, hàng đợi propose **có trần** (`propose_queue_size`), một goroutine gọi `raft.Apply`, một goroutine đợi kết quả; propose thất bại (mất leader trước commit) thì **chuyển lại cho leader mới**, tối đa 40 lần cách 250ms rồi mới tính `Lost()` (mất) — có thể nhân đôi batch đã commit thực ra (kết quả không rõ), Go khử trùng theo hash tx (`T-SUB-05`). Batch hỏng/rỗng bị bỏ và đếm ở leader (không bao giờ vào log — vào log sẽ làm mọi replica dừng); batch vượt `max_batch_bytes` bị **tách theo ranh giới tx**, không cắt.
- `forward.go`: follower → leader qua HTTP nội bộ + HMAC-SHA256 (`node_id ‖ ts ‖ sha256(body)`), ts chỉ chống phát lại kênh nội bộ (`T-SUB-02`).
- Cấu hình `raft{...}` trong `SimpleChainConfig` (`pkg/config/raft_config.go`), `raftfeed.ValidateConfig` kiểm mọi ràng buộc lúc khởi động (sai ⇒ thoát). Không có khối `raft` ⇒ vẫn là feeder 1 node của C1. `raft.sequencer_address` phải bằng địa chỉ ký của node, lệch ⇒ thoát (`T-R1-06`). FSM báo lỗi nghiêm trọng ⇒ `fatal.Exit` (thà dừng còn hơn phục vụ chuỗi có thể lệch).

**Lệch so với `SEQUENCER_SCHEMAS_AND_TEST_PLAN.md` (đã ghi vào tài liệu đó):** `commit_index`/`global_exec_index` là **bộ đếm liền mạch của FSM**, không phải Raft index (Raft index có lỗ do entry cấu hình/no-op; C1 đã kiểm chứng Go chạy với bộ đếm liền mạch); công thức `commit_hash` giữ như C1 (`keccak256(prev‖index‖ts‖batch)`, không có tiền tố `ROLLUP_BATCH_V1:`); ngưỡng 413 ở follower ⇒ **bỏ + đếm** thay vì trả `false` (forwarder thử lại `false` vô hạn ⇒ kẹt); không có `block_queue_size` riêng (dùng hàng đợi 5000 sẵn có); mỗi peer có thêm `forward_address`, node có `forward_bind_address`.

**Phát hiện quan trọng khi chạy thật (không phải lỗi của Raft, nhưng chặn chế độ raft):** với dòng block nhỏ, dồn dập (nhiều tài khoản × 1500 tx), pipeline thực thi **treo vĩnh viễn** ở `IntermediateRoot` (watchdog báo "KẸT QUÁ LÂU"; tái hiện được cả ở feeder 1 node C1, không cần Raft). Goroutine dump (pprof): hai goroutine chờ `BeginSession` ở `nomt_ffi/bridge.go:524` vì `activeCount > 0` mà **không goroutine nào giữ** ⇒ một `FinishedSession` NOMT bị bỏ rơi. Nhật ký: đúng một lần `COMMITTER-CONFLICT` (`specParentHash ≠ actualParentHash`) ở `commitSpeculativeResult` (`speculative_executor.go`) ngay trước khi treo. Giải thích khớp mọi quan sát: executor suy đoán block n trên cha là block n-2 khi n-1 chưa commit ⇒ xung đột ⇒ thực thi lại tuần tự, nhưng `ClonedState` bị bỏ của lần suy đoán (đã `Finish` ở `IntermediateRoot`, đang giữ `activeCount`) không được huỷ trước khi thực thi lại ⇒ `BeginSession` của lần thực thi lại chờ mãi. **Cập nhật 2026-09-27: đã sửa hẳn ở executor** (2 phần): (1) không rò session nữa (`AbortSpeculative`, PR #141); (2) **cổng IR có thứ tự** — thực thi suy đoán block n chỉ chạm handle NOMT sau khi block n-1 đã commit và chỉ khi cha vẫn là đỉnh chuỗi, nếu không thì bỏ *trước khi mở session* và committer thực thi lại (`HANDOVER_SPECULATIVE_CONFLICT_NOMT_SESSION_LEAK.md`). Nhờ đó **đã bỏ cổng giao block của `raftfeed`**: bỏ cổng mà có cổng IR thì bản lặp lại (C1, 2 tài khoản × 1500 tx) chạy hết 5/5 lần, 0 watchdog; cụm 3 node 100k tx: 9.2–10.7k tx/s, không đổi so với khi còn cổng.

**Bằng chứng (đo thật, 2026-09-26):**
- `go test -race` gói `raftfeed`: đạt lặp ≥ 8 lần, 0 flake; đột biến (bỏ chờ bền ở Snapshot; không bỏ qua replay; không tiến xích khi bỏ qua; không kiểm `tx_count`; không kiểm MAC / ts; Restore không kiểm DB; không chuyển lại batch; batch hỏng vào log; cắt thay vì tách; bỏ cổng giao block ở FSM và feeder) đều làm test đỏ.
- Trong tiến trình (3 node, transport in-memory có thể ngắt): `T-RF-01` (1000 batch, cả 3 replica byte-giống-hệt, thứ tự giữ nguyên), `T-RF-02` (+`T-RF-11`: mất leader → có leader mới ~250ms với `election_timeout_ms=100`), `T-RF-04`, `T-RF-05`, `T-RF-06`/`T-RF-03`/`T-SUB-04` (leader bị cô lập nhận batch không commit được → không thực thi ở đâu cả, sau nối lại log khớp, mỗi tx đúng 1 lần), `T-RF-07`, `T-AP-01..08`, `T-DET-04`, `T-SUB-01/02/03/05`, replica hỏng dừng còn cụm chạy tiếp.
- **Cụm thật 3 tiến trình `simple_chain` trên máy local** (cổng riêng, không đụng cụm 231/230 hay cụm CI): 4500 tx từ 3 tài khoản gửi vào 3 node khác nhau → cả 3 node cùng chiều cao, `hash`/`stateRoot`/`timestamp`/`miner` khớp ở các block 100/768/1570; **kill -9 leader giữa tải** → node còn lại bầu leader mới, **không mất tx nào** (mọi tx đều có receipt), khởi động lại node bị giết → bắt kịp và khớp `hash`/`stateRoot`; snapshot thật được tạo trong tải (`snapshot_threshold=300`); **kill -9 cả 3 node rồi khởi động lại** từ snapshot + log → cùng chiều cao, khớp hash, nhận tx tiếp.

**Bổ sung cùng ngày (sau khi merge PR #139):**
- **`T-RF-09` đã làm** (`attest.go`): mỗi replica định kỳ (1s, chỉ để nhịp kiểm tra, không quyết định giao block) so **hash header** của block checkpoint (bội của 10; hash này bao gồm các state root) với các peer qua kênh nội bộ có HMAC (`GET /raft/v1/blockhash?n=`). Replica có hash **khác giá trị mà đa số cụm cùng giữ** ⇒ `Mismatches()++`, gọi `OnFatal` (thoát tiến trình) và dừng Raft; không có đa số (peer tắt/chậm/chia đôi) ⇒ **không quyết định gì, để pending**. Test: cụm khoẻ 40 block không bao giờ bị gắn cờ (không dương tính giả) và có kiểm chứng đã chạy (`Attested()>0`); replica nói dối hash bị dừng, 2 replica còn lại tiếp tục commit và byte-giống-hệt; endpoint từ chối MAC sai/node lạ/query khác; đột biến (không so / không kiểm MAC) làm test đỏ. Chạy thật 3 tiến trình: 16–22 lần "matches the majority (3 of 3)" mỗi node trong tải 100k tx, 0 lệch. Dùng `blockchain.GetBlockChainInstance().GetBlockHashByNumber`. **Giới hạn:** so hash header chứ chưa so riêng từng state root; chưa tiêm lệch thật ở cụm tiến trình thật (chỉ ở test trong tiến trình).
- **`T-RF-08` dạng "ổ log hỏng"** (không phải mất điện giữa fsync thật): kho log của leader bắt đầu từ chối ghi ⇒ `failClosedLogStore` biến lần ghi lỗi đầu tiên thành lỗi nghiêm trọng của replica (thoát cụm, không tiếp tục nhận/ack entry có thể chưa bền); 2 replica còn lại bầu leader mới, mọi tx đã nhân bản chạy đúng 1 lần, chuỗi của node hỏng là tiền tố của chuỗi đa số. **Batch chỉ mới được node hỏng nhận vào hàng đợi (chưa nhân bản) mất cùng node đó, như kill -9 — người gửi phải gửi lại** (đã ghi trong test). Đột biến (bỏ wrapper) làm test đỏ. Mất điện giữa fsync thật vẫn chưa mô phỏng.
- **TPS chế độ raft (đo thật, 3 tiến trình trên 1 máy, RPC `eth_sendRawTransaction`, native transfer, 2000 tài khoản × 50 tx = 100k tx, 3 lần):** end-to-end **~9181 / ~9275 / ~9209 tx/s** (thực thi hết 100000/100000, 128–149 block, hash `latest` giống nhau trên 3 node, 0 watchdog); lần đầu 500 tài khoản × 60: ~8025 tx/s. Với 10 tài khoản × 3000 tx: ~1290 tx/s nhưng **bị giới hạn bởi bộ phát** (injection 1461 tx/s), cụm theo kịp. So sánh thô: đường Rust `tps_blast` ~6954 tx/s — **không cùng phương pháp** (TCP thô 25k tx vs RPC 100k tx, một máy chạy cả 3 node), chỉ dùng để nói cổng giao block không phải nút cổ chai, không dùng để so hơn kém.
- `HANDOVER_SPECULATIVE_CONFLICT_NOMT_SESSION_LEAK.md`: ghi chú giao việc cho chủ module (cách tái hiện, goroutine dump, giải thích, lý do chưa sửa, hướng sửa).

**Bổ sung 2026-09-27 (cụm thật 3 tiến trình `simple_chain` trên 1 máy, cổng riêng, không đụng 231/230):**
- **`Submit` = "đã commit" (khớp kế hoạch C2), không còn "đã vào hàng đợi".** Chạy chaos thật lần 1 (10 vòng, mỗi vòng kill -9 một node ngẫu nhiên giữa tải 12k tx) cho thấy 549 tx của các tài khoản gửi vào node *còn sống* bị mất: batch đã được leader nhận vào hàng đợi propose (forwarder đã xoá khỏi pool) rồi leader chết ⇒ tài khoản kẹt nonce. Sửa: `Submit`/handler forward chỉ trả `true` khi entry đã **commit** bởi đa số (chờ tối đa `submitCommitTimeout`=5s, hết hạn/mất leader ⇒ `false` ⇒ forwarder giữ và gửi lại; có thể nhân đôi, Go khử trùng). Bỏ cơ chế re-route cũ (không còn cần). Chạy lại chaos: **0 tx mất ở các tài khoản gửi vào node còn sống** (trước: 549); phần còn thiếu (~3.9k / 118k) hoàn toàn thuộc tài khoản gửi vào chính node bị kill lúc pool RAM của nó chưa kịp chuyển (đặc tính của mempool RAM, không phải của Raft). Test: `TestCluster_SubmitIsNotAcknowledgedBeforeCommit` (đột biến trả sớm ⇒ đỏ), các test cô lập leader viết lại theo kiểu forwarder (giữ gửi lại tới khi được nhận).
- **Chaos kill -9 ngẫu nhiên, 10 vòng ×2 lần** (kill node ngẫu nhiên gồm cả leader giữa tải, khởi động lại sau 3s): cả 3 node **cùng chiều cao, cùng số tx đã thực thi, hash/stateRoot trùng ở block 64/256/mới nhất**, 0 lệch attest, 0 watchdog, 0 lỗi FSM.
- **`T-RF-09` ở cụm tiến trình thật:** một node dựng bằng tag `rollup_faults` (`attest_fault.go`, chỉ có trong binary thử nghiệm; bản phát hành dùng `attest_nofault.go` = hàm đồng nhất, `T-OFF-05`) bắt đầu báo hash sai khi có file cờ ⇒ ~3s sau (checkpoint block 130) nó tự dừng với `block 130 hash … differs from the value … held by 2 of 3 replicas`, tiến trình thoát; 2 node còn lại không bị gắn cờ, tiếp tục và thực thi thêm 20k tx.
- **TPS chế độ raft sau các thay đổi:** ~10.2–10.7k tx/s end-to-end (100k tx, 2000 tài khoản, 3 lần).
- **Không làm được ở môi trường này (nói thẳng):** (a) *kill -9 đa máy thật*: chỉ có 1 máy cho cụm thử; máy khác là cụm 231/230 thật, cấm động vào; (b) *mất điện giữa fsync thật*: cần quyền root (dm-flakey/ngắt nguồn) hoặc chèn fsync; thay bằng kill -9 ngẫu nhiên nhiều điểm + test ổ log từ chối ghi (bolt là copy-on-write có meta page nên crash giữa ghi để lại DB nhất quán ở lần commit trước; entry chưa fsync xong chưa bao giờ được ack).

**Chưa làm / giới hạn (ghi trung thực):** mất điện giữa fsync thật và kill -9 đa máy thật (không làm được ở đây, xem trên), replica dựng lại rỗng/thêm replica (C4), **`Lost()` khác 0 ⇒ người gửi phải gửi lại tx** (chưa có trong thử nghiệm thật vì kill -9 không làm mất tx nào, nhưng cơ chế cho phép). Hard fork StorageRoot và cụm 231/230 (D3/N2) không đổi.

### N3c — C4: đổi leader, thêm / thay replica (2026-09-27; nhánh `feat/raft-c4`)

**Đã làm:** `admin.go` / `adminclient.go` / `membership.go` trong `raftfeed`, công cụ `execution/cmd/tool/rollup_cluster` (`check`, `transfer-leader`, `add-replica`, `remove-replica`, `prepare-replica`, đều có `--dry-run` và từ chối khi thiếu điều kiện an toàn), runbook `RAFT_CLUSTER_RUNBOOK.md`.
- Kênh điều khiển = cổng nội bộ có HMAC (dùng chung với kênh submit), id `admin` cho công cụ; **mọi thao tác kiểm lại ở node** (client bỏ qua bước kiểm vẫn bị từ chối).
- Thành viên động: `raft.forward_port_offset` (cổng nội bộ = cổng Raft + offset) để replica thêm sau không cần sửa cấu hình node cũ; `raft.join_existing_chain` cho replica có DB sao chép từ peer (state Raft rỗng + DB có block; nếu không đặt cờ thì node từ chối khởi động). Phát lại log/snapshot bỏ qua block DB đã có, bộ đếm FSM lấy từ log/snapshot.
- **Trạng thái không truyền qua mạng** (snapshot Raft chỉ có bộ đếm; chọn: sao chép thư mục dữ liệu của peer đã DỪNG, `cp -a --reflink=auto`, công cụ từ chối nếu peer còn trả lời). Replica rỗng chỉ vào được khi log của leader còn đủ từ entry 1; log đã thu gọn ⇒ công cụ từ chối, ép ở mức node ⇒ replica tự dừng.
- Nâng voter chỉ khi đã bắt kịp **cả** entry (≤ 64 so với commit index) **lẫn** block đã thực thi (≤ 64 so với leader) và hash block tip trùng leader; thêm thẳng voter bị từ chối. (Phát hiện khi chạy thật: chỉ dựa vào `applied_index` là sai — FSM chỉ đẩy block vào pipeline, replica đang phát lại log dài "applied" từ lâu trước khi thực thi; đã sửa + test có đột biến.)
- Xoá replica: từ chối nếu số voter sống còn lại < đa số của cụm sau khi bỏ, hoặc xoá leader không có `--transfer-first`.
- Chuyển leader: drain leader (ngừng nhận batch, đợi áp dụng hết) rồi `LeadershipTransferToServer`; đích phải là voter sống, không `failed`, chênh ≤ 64.
- Attest (T-RF-09) giờ dùng voter của cấu hình Raft hiện tại; replica non-voter vẫn bị so với đa số voter (mang state sai sẽ tự dừng trước khi được nâng).

**Test (T-LD-*, T-CL-* dạng trong tiến trình, `-race`, có đột biến cho từng luật an toàn: quorum, độ trễ khi chuyển, độ trễ khi nâng, chuỗi khác, log thu gọn, chưa thực thi, drain):** chuyển leader dưới tải không mất tx; từ chối đích tụt hậu; từ chối xoá làm mất quorum (cả ở công cụ lẫn node); dry-run không đổi gì; thêm replica có state sao chép / rỗng (log đủ) / rỗng (log thu gọn: từ chối + tự dừng) / state của chuỗi khác (từ chối); thay replica chết dưới tải; `check` khoẻ + phát hiện khoá ký khác nhau; cờ `join_existing_chain`.
**Chạy thật (3→6 tiến trình `simple_chain` trên 1 máy, cổng riêng):** chuyển leader n0→n1 giữa tải 36 000 tx (không mất); dừng n2 và `prepare-replica` (8,6 GB, reflink 0,5 s; từ chối khi n2 còn chạy) → thêm n3 giữa tải 24 000 tx (4 voter, cùng chiều cao/số tx/hash); **giết leader n1 vĩnh viễn giữa tải**, `remove-replica n1`, sao chép từ n3, thêm n4 (152 000 tx, 4 replica cùng hash/stateRoot); restart cả cụm 5 replica; thêm **replica rỗng** n6 (phát lại toàn log) giữa tải, chỉ được nâng sau khi thực thi kịp: 6 replica cùng 164 000 tx, block 350, hash/stateRoot trùng, 0 lệch attest.

**Cập nhật 2026-09-27 (khoá ký mã hoá tại chỗ, T-CL-02 phần "mã hoá" — xong):** `pkg/keyvault` + `pkg/config/secrets.go` + `cmd/tool/encrypt_secret`; mọi bí mật của `config.json` có thể là `enc:v1:…`, giải mã một lần lúc khởi động; mật khẩu từ `META_KEY_PASSWORD` hoặc file 0600; sai/thiếu mật khẩu hoặc `require_encrypted_keys` mà còn khoá dạng rõ ⇒ node không khởi động (đã chạy thật: 3 trường hợp; có khoá mã hoá + file mật khẩu thì node ký giao dịch bình thường). Chi tiết trong `RAFT_CLUSTER_RUNBOOK.md`.

**Cập nhật 2026-09-27 (truyền trạng thái qua mạng — xong):** `raftfeed/state_transfer.go` + `cmd/simple_chain/processor/raft_state_source.go` + lệnh `fetch-state` / `hold-snapshots`. Replica rỗng lấy snapshot **nguyên tử** của một replica **đang chạy** qua kênh nội bộ có HMAC (donor dùng lại `PauseExecution`/`FlushAll`/`CheckpointAll`/`SnapshotAllNomtDBs` + xapian, dừng thực thi ngắn rồi băm file ngoài lúc dừng), manifest có MAC, tệp thưa truyền theo đoạn dữ liệu (NOMT 77 GB logic → 404 MB thật), mỗi file kiểm hash, chạy lại được, từ chối bố cục lạ / thiếu thư mục. `CheckpointChangelogs` (dùng chung với snapshot manager) nay có thêm `changelog_db_sc` và `blob_store` mà snapshot cũ bỏ sót. `hold-snapshots` giữ snapshot Raft không vượt qua state đã sao chép (nếu không replica mới bị từ chối). Đo thật (ext4, `state_transfer_allow_copy`): dừng thực thi 0,65 s, cả `fetch-state` 3 s giữa tải; replica rỗng gia nhập cụm đã thu gọn log giữa tải 30 000 tx: 4 replica cùng 300 000 tx, cùng hash/stateRoot; dựng lại replica tụt hậu = xoá + thêm id mới (không dùng lại id sau khi xoá thư mục Raft: mất lịch sử phiếu bầu).
- **Lỗi hồi quy tìm thấy khi làm việc này và đã sửa (PR #145):** cổng IR của #142 chờ committer trong lúc giữ `ExecutionMutex.RLock`; `PauseExecution` (snapshot) hoặc P2P sync đang chờ ghi chặn reader mới ⇒ committer không commit được block trước ⇒ deadlock ba chiều (node đứng ở "waiting for ExecutionMutex.Lock()"). Cổng nay nhả khoá đọc khi chờ và lấy lại trước khi trả về; test với RWMutex thật + đột biến.

**Chưa làm / giới hạn:** nhiều máy thật; `fetch-state` trên filesystem reflink (btrfs/xfs) chưa chạy thật (máy thử không ghi được vào phân vùng btrfs), chỉ đường bản sao đầy đủ; so từng state root riêng; kênh nội bộ không mã hoá (chỉ xác thực).

### N4 — Tài liệu và schema: A1 → A2, rồi chốt B1 (ĐÃ HOÀN TẤT `☑`)
**A1 (Đã ☑):** Đã viết lại `SEQUENCER_DESIGN.md` và `SEQUENCER_DIAGRAMS_AND_OPEN_ISSUES.md` cho khớp Raft (mục 0.1, 0.6 của kế hoạch chính); đã gỡ bỏ hoàn toàn banner "pre-Raft", bổ sung quy trình Raft SMR Pipeline (mục 2.5) và sơ đồ sequence diagram (A.1b).
**A2 (Đã ☑):** Đã chốt đặc tả dữ liệu trong `SEQUENCER_SCHEMAS_AND_TEST_PLAN.md` mục 1.7: loại bỏ hoàn toàn state 20 `OBSERVED`, chuẩn hóa bắt buộc `recordRole Role` (`RoleSender` / `RoleReceiver`).
**B1 (Đã ☑):** Chuẩn hóa `Next(current State, recordRole Role, event Event)` trong `statemachine.go`, phủ 100% cạnh bảng 1.7, kiểm chứng tích Descartes hai chiều (286 triples: 29 valid passed, 260 rejected), pass 109k fuzz executions (`FuzzMutualExclusion`); `go test -race -count=1 ./pkg/rollup` PASS.
**Nghiệm thu:** tài liệu và code khớp 100%; A1, A2, B1 đánh dấu ☑ kèm đầy đủ bằng chứng kiểm thử.

### N5 — B2 store per-key (sau N4/B1 — tách riêng sang branch/PR `feat/rollup-b2-store`)
Lưu `RollupRecord` per-key trong `SmartContractDB` (không nạp cả blob), resume sau crash (`ScanNonTerminal`). Thiết kế chỉ mục riêng qua `RollupIndex` (`keccak256("rollup_idx_v1")`) có trần kích thước tường minh `MaxInFlight` (ngăn backpressure vô hạn).
**Trạng thái phạm vi:** Đã hoàn thành code và benchmark $O(1)$ tại local, nhưng được tách sang branch/PR riêng (`feat/rollup-b2-store`) theo kế hoạch nghiệm thu PR N1/N4 để đảm bảo nguyên tắc tách biệt phạm vi (Scope Isolation).
**Nghiệm thu hiệu năng:** `BenchmarkRecordStore_Get_` xác nhận chi phí là **$O(1)$ tuyệt đối**, hoàn toàn không tăng theo số lượng bản ghi:
- 50 records: `5049 ns/op` (~5.0 µs, 1312 B/op, 14 allocs)
- 500 records: `5204 ns/op` (~5.2 µs, 1312 B/op, 14 allocs)
- 3.000 records: `4877 ns/op` (~4.8 µs, 1312 B/op, 14 allocs)
(Khắc phục triệt để vấn đề của Gateway cũ vốn tăng tuyến tính từ 0.7 ms → 5.4 ms → 31 ms).

### N6 — A0 → B3 Parent Chain (`NodeFloatAccount`, `ClaimedMessages`), lưu per-key
**A0:** đối chiếu lại bảng khoảng cách trong `SEQUENCER_DESIGN.md` mục 3.1 với symbol đang tồn tại trong `execution/pkg/cross_chain/gateway.go` và `tx_processor/gateway_handler.go`. Lưu ý: `RecoveryCommittee` đã gỡ; unregister tự ký có nonce; `DeadChains` chỉ qua `SlashOnEquivocation`.
**B3:** khoá lưu `rollup_fa_v1`, `rollup_claimed_v1` (per-key), tái dùng `ChainRegistry` và `SlashOnEquivocation`, **không** sửa hành vi `outbound`/`attestCommit`/`claimMessage` cũ. Bất biến: `FA[chainID] ≥ 0`; tổng `FA` bảo toàn; `ClaimedMessages` ghi một lần; reclaim bị từ chối khi đã Claimed.
**Nghiệm thu bắt buộc về hiệu năng:** viết `BenchmarkGatewayHandler_Outbound`-tương-đương cho hàm mới (`pkg/blockchain/tx_processor/gateway_handler_bench_test.go` là mẫu; mỗi vòng phải thành công) ở ít nhất 50, 500, 3.000 bản ghi: chi phí mỗi giao dịch **không được tăng tuyến tính** như Gateway cũ (0,7 → 5,4 → 31 ms).
**Triển khai:** đổi trạng thái tuần tự hoá Gateway ⇒ phải wipe + nâng cấp đồng thời mọi node; kiểm bằng `ci.sh run-now --reset`.

### N7 — Sửa test data race có sẵn (ĐÃ HOÀN TẤT `☑`)
`TestSubscribeProcessor_ConcurrentAccess` (`cmd/simple_chain/processor/subscribe_processor_test.go:228`).
**Đã xác minh:** Chạy toàn bộ package `go test -race -count=1 ./cmd/simple_chain/processor/` vượt qua 100% không có data race nào, không cần cờ `-skip` (thời gian chạy ~120s, exit code 0).

### N8 — (Tuỳ chọn, cần D5) Cắt liên kết `libmetanode` khỏi bản build raft
**Hiện trạng:** `executor/ffi_bridge.go` có `#cgo LDFLAGS: -lmetanode`; binary `simple_chain` link thư viện Rust này dù không khởi động (chỉ tốn kích thước và bước build). Package `executor` còn được import từ ít nhất `pkg/blockchain/tx_processor/validation_transaction.go`. NOMT (Rust) và MVM/Xapian (C++) **vẫn cần** cho state/EVM: mục này không loại bỏ chúng; thay NOMT bằng backend Go (`mpt`, `flat`) làm giảm thông lượng (trước đây đồng bộ trie bằng NOMT cho giao dịch/receipt mất >3,5 s/block nên đã chuyển sang `flat`) — chỉ xét khi có số đo.
**Việc (chỉ phân tích, chưa sửa):** liệt kê importers của `executor`, đề xuất build tag (ví dụ tách phần `executor` không cgo), ước lượng phạm vi sửa và tác động lên build/CI, kiểm binary raft không còn phụ thuộc `libmetanode` (`ldd`/`go list -deps`).
**Nghiệm thu:** tài liệu quyết định (làm/không, chi phí, rủi ro) để chủ dự án duyệt.

### N9 — Giai đoạn F (bằng chứng gian lận ERC20) — **chưa bắt đầu**
Chờ: C5 (chaos đa máy), D2 (A3), D6. Việc chuẩn bị không cần code: đo `G_std` (gas thật của `transfer`/`transferFrom`/`approve` thành công) cho từng token thuộc phạm vi; thiết kế `txset_root` trong `AnchorLeaf`; benchmark lại Gateway sau B3. Đặc tả cuối: `SEQUENCER_ERC20_STANDARD_TX_PROOF.md` (mục 14 = kết quả F0, mục 15 = quyết định đã chốt).

---

## 4. Thứ tự và song song hoá

```
Ngay bây giờ (song song, không phụ thuộc nhau):
  N0  T-DET còn thiếu (mở rộng spike)   N1  rà soát bền vững         N4  A1 → A2 → B1        N6  A0 → B3 (sau A2)     N7  sửa test race
  N2  runbook triển khai (cần D3 để chạy thật)

Sau N0 và D1 (C0 ☑):
  N3  C1 (guard Go→Rust, nguồn cấp Raft)  →  C2 → C3 → C4 → C5 → C6   (theo kế hoạch chính)
Sau N4:   N5 (B2) → B4 → B5 → B6 → B7 → B8 → B9  (chuỗi tuần tự)
Tuỳ chọn: N8 (cần D5)            Chưa bắt đầu: N9 (cần C5, D2, D6)
```

---

## 5. Danh sách kiểm tra trước khi mở PR (mọi việc có code)

- [ ] `go build ./pkg/... ./cmd/simple_chain/... ./executor/...` (mặc định và `-tags c0spike`), `go vet` sạch.
- [ ] `go test -race -count=1` cho các package bị ảnh hưởng (kèm `-skip TestSubscribeProcessor_ConcurrentAccess` tới khi xong N7).
- [ ] `consensus/metanode/scripts/build_check.sh` 4/4, không cảnh báo (dán đầu ra).
- [ ] Nếu đụng đường commit/Block-STM/Gateway/state: `./ci.sh run-now --reset` sạch (TPS Blast ~7600 tx/s, chaos restart 6 vòng, zero-fork) — chỉ trên cụm cục bộ `.232`.
- [ ] Test hồi quy chứng minh **thất bại khi gỡ bản sửa** (mutation).
- [ ] Số đo có lệnh kèm theo; benchmark đúng quy tắc mục 0.
- [ ] `PROJECT_STRUCTURE.md` cập nhật nếu thêm package/cờ/khoá cấu hình.
- [ ] Sau merge: `git show origin/dev:<file>` kiểm nội dung thật.
- [ ] Ghi trạng thái ◐/☑ trong bảng theo dõi của `SEQUENCER_STEP_BY_STEP_PLAN.md` **chỉ khi** đạt cổng và có bằng chứng trong PR.


---

## 6. Schema — nguồn sự thật, trạng thái và quy tắc thay đổi

**Nguồn sự thật:** `SEQUENCER_SCHEMAS_AND_TEST_PLAN.md` mục 1 (schema), `SEQUENCER_ERC20_STANDARD_TX_PROOF.md` mục 4 (schema bằng chứng gian lận), và code đã cài. **Khi code và tài liệu lệch nhau, sửa tài liệu trong cùng PR (A2) — không để hai bản khác nhau.**

| Schema | Định nghĩa ở | Trạng thái | Quy tắc quan trọng |
|---|---|---|---|
| Máy trạng thái B1: `State`, `EventType`, `ActionType`, `Outcome`, `Role` | code `pkg/rollup/types.go`, `statemachine.go`; bảng chuyển ở SCHEMAS 1.7 | **Đã cài** (B1 ◐) | Số trạng thái cố định: gửi 10, 11, 12, 13, 18, 19; nhận 21, 22, 23, 27, 28, 29. Code **không có** trạng thái `20 OBSERVED` của tài liệu (chốt ở A2). Cặp `(state,event)` không có trong bảng ⇒ **lỗi, không panic**. `Value` phải > 0 (nil/≤ 0 bị từ chối). Không I/O, không thời gian, idempotent |
| `BatchRecord` (entry log Raft) | SCHEMAS 1.2 (`raftfeed/proto/batch.proto`) | **Đề xuất** | Protobuf `Deterministic: true`: **cùng batch ⇒ cùng dãy byte**. Có `schema_version` (=1), `timestamp_ms` do **leader** đóng dấu, `txs` = đúng output `transaction.MarshalTransactions`, `tx_count` kiểm chéo (lệch ⇒ từ chối), **không bao giờ propose batch rỗng**, `len(txs) ≤ max_batch_bytes` |
| Ánh xạ batch → `pb.ExecutableBlock` | SCHEMAS 1.3 (15 trường) | **Đề xuất giá trị, các trường đã xác minh** | `transactions[i].digest` = bytes tx **đầy đủ** (không phải hash); `commit_index = uint32(raftIndex)`, `raftIndex > 2^32-1` ⇒ dừng replica, **không cắt cụt**; `leader_address` = `raft.sequencer_address` cố định trên mọi replica; `commit_hash = keccak256("ROLLUP_BATCH_V1:" ‖ prev ‖ be64(ts) ‖ keccak256(txs))`; bỏ entry có `block_number ≤ storage.GetLastBlockNumber()` khi restart; `Apply` **cấm** đọc đồng hồ/ngẫu nhiên |
| `FsmSnapshotMeta` + bố cục kho Raft | SCHEMAS 1.4 | Đề xuất | Snapshot chỉ ghi metadata cho block **đã bền trong DB** (`last_block_number` ≤ block bền); không thu gọn log vượt dữ liệu đã fsync |
| Cấu hình | SCHEMAS 1.1; code `pkg/config/config.go` | **Chỉ có `consensus_mode`** (rỗng = Rust như cũ, `"raft"`); khối `raft.*` là đề xuất | `consensus_mode` rỗng: **không** đọc/tạo thư mục `raft/`, không đổi hành vi (chứng minh bằng `T-OFF-*`) |
| `RollupRecord` và khoá lưu | SCHEMAS 1.6; khoá `rollup_msg_v1` | Đề xuất (vị trí ghi [TẠM]) | Lưu **per-key**, không nạp cả blob; `SmartContractDB` không có duyệt tiền tố ⇒ cần chỉ mục riêng cho `ScanNonTerminal` |
| Kênh chuyển tiếp follower → leader | SCHEMAS 1.5 | Đề xuất | Hàng đợi có trần rõ ràng |
| Sự kiện quan sát từ Parent Chain | SCHEMAS 1.8 | Đề xuất | Trùng `(parent_block, log_index)` ⇒ bỏ qua (idempotent) |
| Parent Chain: `NodeFloatAccount`, `ClaimedMessages`, ABI | SCHEMAS 1.9 (**[TẠM — chốt sau A0]**); khoá `rollup_fa_v1`, `rollup_claimed_v1` | Chưa cài | `FA ≥ 0`, `Σ FA == supply`, `Claimed` ghi một lần; đổi ABI Gateway ⇒ nâng cấp đồng loạt |
| Metric, log, đầu ra công cụ | SCHEMAS 1.10 | Đề xuất | Đầu ra `check` phải đúng schema JSON (`T-CL-01`) |
| Bằng chứng gian lận ERC20: `TxRecord`, `txset_root` (lá `(tx_hash,status)`) trong `AnchorLeaf`, sổ tối giản theo block, `ns_root`, `AnchorState` + MMR | `SEQUENCER_ERC20_STANDARD_TX_PROOF.md` mục 4 | Đề xuất (giai đoạn F) | `transactionsRoot` **không** có bằng chứng thành viên (bộ cộng dồn theo block); giao dịch hợp lệ = có `ValidEthSign()` (ECDSA), không phải chữ ký BLS |

**Quy tắc phiên bản và thay đổi schema (SCHEMAS 1.11, bắt buộc):**
1. Mọi proto có `schema_version`. Replica **từ chối** entry/record có phiên bản lớn hơn phiên bản nó hiểu (dừng, không đoán).
2. Thêm trường proto chỉ ở **cuối**, không đổi số thứ tự, không tái dùng số đã bỏ.
3. Bất kỳ thay đổi nào làm đổi **byte** của state (record, khoá, thứ tự ghi) là thay đổi **state root** ⇒ phải nâng cấp **đồng loạt** mọi replica (bài học khi gỡ `RecoveryCommittee`: chạy lẫn binary cũ/mới sẽ lệch state); trên cụm thử cần wipe + deploy lại.
4. Thứ tự làm việc khi đổi schema: (a) sửa tài liệu SCHEMAS (A2) và tăng `schema_version` nếu cần; (b) thêm **golden vector** (mục 7.2); (c) code; (d) test tương thích/từ chối phiên bản; (e) ghi vào PR mọi khoá/byte bị đổi và tác động lên state root.
5. Không dùng `map` để tuần tự hoá hay lặp khi sinh dữ liệu ảnh hưởng state (thứ tự lặp map trong Go là ngẫu nhiên ⇒ không xác định ⇒ fork). Sắp xếp khoá tường minh.
6. Không I/O, đồng hồ hay ngẫu nhiên trong logic thuần và trong `FSM.Apply`.

---

## 7. Yêu cầu kiểm thử (bắt buộc, không thương lượng)

Nguồn chi tiết: `SEQUENCER_SCHEMAS_AND_TEST_PLAN.md` mục 2 (danh mục test có ID, hạ tầng test, 11 bất biến, tiêu chí thoát theo phase). Dưới đây là **yêu cầu tối thiểu** cho mọi PR có code.

### 7.1. Nguyên tắc
1. **Test trước khi coi là xong:** không có test thì bước đó chưa xong, dù code chạy được.
2. **Xác định trước, chaos sau:** logic thuần test bằng bảng; Raft trong tiến trình; sau đó mới đa tiến trình/đa máy.
3. **Kill tại mọi điểm chuyển trạng thái**, không chỉ vài điểm ví dụ (`T-TX-16`, `T-CH-01`): sinh test từ bảng chuyển 1.7.
4. **Không `time.Sleep` để "chờ đủ lâu"** trong test logic; dùng `FakeClock` và điều kiện đồng bộ. Chỉ test cụm thật được đợi theo sự kiện có giới hạn.
5. **Mọi test hồi quy phải thất bại khi gỡ bản sửa** (kiểm mutation thủ công: tạm xoá dòng sửa, test phải đỏ). Cách đã dùng với `SyncDurable(codeStorage)`.
6. **Test chứng minh "mặc định-tắt"** (`T-OFF-*`) là điều kiện bắt buộc trước khi sửa file cũ (hook H1–H6).
7. Chaos/cụm: **lặp ≥ 20 lần, 0 flake**; đồng thời `go test -race -count=1`.
8. Không ghi PASS nếu không đo; benchmark đúng quy tắc ở mục 0.

### 7.2. Yêu cầu riêng cho schema (mỗi schema/proto/khoá lưu)
- **Golden vector:** với mỗi message/khoá, một test so **từng byte** đầu ra với vector cố định lưu trong repo (đổi byte ⇒ test đỏ ⇒ buộc người sửa thừa nhận đổi state root).
- **Marshal xác định:** cùng đối tượng, marshal nhiều lần và trên nhiều goroutine ⇒ cùng dãy byte; không phụ thuộc thứ tự lặp `map`.
- **Round-trip:** encode → decode → encode cho ra cùng byte; kiểm cả giá trị biên (`uint64` tối đa, số dư `big.Int` rất lớn, chuỗi/bytes rỗng và dài tối đa).
- **Từ chối phiên bản lạ:** `schema_version` lớn hơn ⇒ lỗi rõ ràng, replica dừng, không đoán.
- **Từ chối dữ liệu sai:** `tx_count` lệch, batch rỗng, `raftIndex > 2^32-1`, trường bắt buộc thiếu ⇒ từ chối, không panic (`T-AP-02/03/05`).
- **Fuzz decode:** `go test -fuzz` cho bộ giải mã (không panic, không cấp phát vô hạn, không đọc ngoài biên).
- **Tương thích:** đọc bản ghi cũ sau khi thêm trường cuối vẫn đúng; thay đổi làm đổi byte state phải có test nâng cấp đồng loạt (ví dụ `ci.sh run-now --reset`).
- **Ổn định state root:** cùng chuỗi giao dịch ⇒ cùng `state_root`/`block_hash` trên 2 tiến trình (`TwoProcessHarness`, `T-DET-*`).

### 7.3. Test tối thiểu theo việc

| Việc | Test bắt buộc (ID trong SCHEMAS mục 2.3 nếu có) | Ghi chú |
|---|---|---|
| N0 | `T-DET-01,02,03,05` (+ `06,07` đã có) | Nếu lệch: dừng |
| N1 | Test `kill -9` cho từng kho; test có `FaultyStore` (lỗi/ghi chậm/mất điện giữa fsync); test hồi quy có mutation; đo chi phí fsync | Bảng rà soát kho là sản phẩm |
| N3 | `T-OFF-01..07` (đường Rust mặc định không đổi), `T-R1-*`, test riêng cho **từng** guard (trả lỗi rõ ràng, không treo, không gọi FFI) | Diff file cũ chỉ được gồm nhánh H1–H6 (script kiểm `scripts/rollup_cluster/check_default_off_diff.sh` là **dự kiến trong SCHEMAS 2.5, chưa có**: viết nó cùng C1) |
| N4 | `T-SM-*` (bảng phủ **mọi** cạnh 1.7; tích Descartes `(State,EventType)` từ chối mọi cặp không có; replay không phát hành động lần hai; mutation `Event.Value` sau `Next` không đổi `Action.Amount`; biên `uint64`; property/fuzz: không vừa success vừa refunded) | `go test -race -count=1 ./pkg/rollup` |
| N5 | `T-ST-*`: ghi/đóng/mở lại `ChainState` đọc đúng từng trường; `ScanNonTerminal` đúng; kill giữa ghi; chi phí không tăng theo số record | Golden vector cho khoá `rollup_msg_v1` |
| N6 | `T-PC-*` (chuỗi `depositToFloat/transferFloat/reclaimFloat` ngẫu nhiên: `FA ≥ 0`, `Σ FA == supply` sau mỗi bước), `T-TX-*` (số dư không đủ, trùng, đua Reclaim vs `markClaimed`…), `T-SEC-*`, hồi quy Gateway cũ, benchmark 50/500/3.000 bản ghi | Đổi trạng thái tuần tự hoá ⇒ wipe + nâng cấp đồng loạt |
| N7 | `go test -race` sạch không cần `-skip`; giữ nguyên ý nghĩa test | Không xoá test |
| C2 (theo kế hoạch chính) | `T-RF-*`, `T-SUB-*`, `T-AP-*` gồm `T-AP-07/08` (thu gọn log vs block chưa bền); `T-DET-04` (2 replica cùng `BatchRecord` ⇒ cùng byte) | Lặp ≥ 20 lần, 0 flake |
| C5 | `T-CH-*` (gồm cắt điện thật `T-CH-09`), `T-E2E-04..05`, `Σ FA == supply` xuyên suốt, `ci.sh run-now` | |
| F (sau này) | `T-SD-01..30` (`SEQUENCER_ERC20_STANDARD_TX_PROOF.md` mục 13) và các test riêng tư `T-PV-*` (`SEQUENCER_ERC20_USER_HISTORY_PROOF.md` mục 13) | Sau C5 |

### 7.4. 11 bất biến toàn cục (`InvariantChecker`) phải kiểm sau **mọi** kịch bản L3–L6
1. Tiền tố log đã commit giống nhau giữa mọi cặp replica. 2. Cùng `block_number` ⇒ cùng `state_root` và `block_hash` trên mọi replica. 3. `commit_index` chỉ tiến. 4. Mỗi tx thực thi tối đa một lần trên mỗi replica, mọi replica cùng tập tx. 5. Không hành động ra ngoài từ batch chưa commit. 6. `Σ NodeFloatAccount == supply` và `FA ≥ 0`. 7. Mỗi `messageID` `Claimed` tối đa 1 lần; credit/hoàn tối đa 1 lần. 8. Không entry nào bị thu gọn khỏi log trước khi block tương ứng đã bền trong DB. 9. Không record nào kẹt vô hạn. 10. `leader_address` trong mọi block = `sequencer_address`. 11. Số tx đã có receipt ⊆ số tx đã commit.

### 7.5. Tiêu chí thoát theo phase (SCHEMAS 2.5) — không được bỏ qua
| Phase | Điều kiện thoát |
|---|---|
| B (B1–B9) | `T-SM`, `T-ST`, `T-PC`, `T-TX` đạt; `T-E2E-01..03` đạt lặp lại; `build_check.sh` sạch |
| **C0** | **`T-DET-01..07` đạt, có tài liệu kết luận; nếu `T-DET-01/02` lệch: dừng, không sang C1** |
| C1 | `T-OFF-01..07`, `T-R1-*` đạt; diff file cũ chỉ gồm H1–H6 |
| C2 | `T-RF`, `T-SUB`, `T-AP` đạt (gồm `T-AP-07/08`), lặp ≥ 20 lần, 0 flake |
| C3 | `T-REL-*` đạt |
| C4 | `T-LD-*`, `T-CL-*` đạt |
| C5 | `T-CH-*` (gồm cắt điện thật), mọi kịch bản lặp ≥ 20 lần; `T-E2E-04..05`; `Σ FA == supply` xuyên suốt; `ci.sh run-now` sạch |
| C6 | `T-PF-*` có số đo được ghi lại; `PROJECT_STRUCTURE.md` cập nhật |
