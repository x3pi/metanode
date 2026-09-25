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

**Còn ◐ (chưa ☑):** A0, B1, C0. **Chưa làm:** A1, A2, B2–B9, C1–C6, D1, F1–F6.

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
| D1 | Đánh dấu **C0 ☑** hay giữ ◐ | C1–C6 chỉ được bắt đầu khi C0 ☑. Các cổng thực nghiệm đã đạt (mục 1); các điểm Go→Rust chưa guard và `commit_index` uint32 là việc của C1 |
| D2 | Chính sách **A3**: token nào được bảo vệ; chỉ tài khoản ký bằng ví ECDSA được bảo vệ (tài khoản chỉ có BLS nằm ngoài) | Chặn F1–F6 |
| D3 | Duyệt triển khai bản sửa `SyncDurable` lên cụm thật 231/230 | Xem N2 |
| D4 | Thu hồi token GitHub đã dán trong chat | Bảo mật |
| D5 | Có cắt liên kết `libmetanode` khỏi bản build raft hay không | Xem N8 |
| D6 | Thiếu **phí gốc** (native coin) trong P8 kiểm không được từ lịch sử ERC20 — chấp nhận rủi ro báo oan/bỏ sót hay yêu cầu thêm dữ liệu | Chặn F3 |

---

## 3. Danh sách việc, theo thứ tự ưu tiên

### N1 — Rà soát bền vững các kho có bộ đệm trên đường commit (P0, độc lập, làm ngay)
**Vì sao:** lỗi vừa sửa (`smart_contract_code`) là một trường hợp của lớp lỗi "ghi có đệm rồi crash". Có thể còn kho khác, hậu quả là hash/state lệch giữa các node sau crash (fork).
**Việc:**
1. Liệt kê mọi kho ghi trong `CommitBlockState` và `SmartContractDB.Commit()`/`CommitAllStorage()`: block DB, mapping, receipts, `transaction_state`, kho log sự kiện (`dbSmartContract`), kho lưu trữ hợp đồng, backup, explorer… Với mỗi kho ghi: loại (Lazy Pebble / Pebble `NoSync` / NOMT / Memory), có `SyncDurable` trước khi trạng thái tham chiếu tới nó được coi là commit không, và **cái gì hỏng nếu mất ghi gần nhất** (state root sai, thiếu dữ liệu để replay, chỉ mất chỉ mục tra cứu…).
2. Tham chiếu: `storage.DurableSyncer` (`pkg/storage/storage.go`), barrier trong `CommitBlockState` (commit `634b0d39`), `SyncDurable(codeStorage)`.
3. Mở rộng spike để kiểm bằng `kill -9`: thêm workload chạm từng kho (ví dụ hợp đồng phát event, ghi storage nhiều, nhiều block liên tiếp) và kiểm hash/state root sau recovery so với lần chạy sạch.
4. Với mỗi lỗ hổng tìm được: PR riêng, có test hồi quy **thất bại khi gỡ bản sửa** (bài học: chứng minh bằng mutation), và **đo chi phí fsync mỗi block** trước/sau.
**Nghiệm thu:** bảng rà soát (kho × loại × được sync? × hậu quả) trong một tài liệu ngắn; mọi kho "hậu quả = fork/lệch state" đã được sync hoặc có lập luận vì sao không cần; số đo chi phí; `build_check.sh` và `ci.sh run-now --reset` sạch.
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
**Nghiệm thu:** cập nhật bảng mục 7 của báo cáo: không còn "CHƯA GUARD"; test cho từng guard; `go test -race` cho processor sạch; spike vẫn pass; `ci.sh run-now --reset` sạch (đường Rust mặc định không đổi).

### N4 — Tài liệu và schema: A1 → A2, rồi chốt B1
**A1:** viết lại `SEQUENCER_DESIGN.md` và `SEQUENCER_DIAGRAMS_AND_OPEN_ISSUES.md` cho khớp Raft (mục 0.1, 0.6 của kế hoạch chính); gỡ banner "pre-Raft".
**A2:** chốt đặc tả dữ liệu (`SEQUENCER_SCHEMAS_AND_TEST_PLAN.md` mục 1.x): giữ hay bỏ trạng thái `20 OBSERVED`, chốt `Role`; code B1 (`pkg/rollup/statemachine.go`) không có state 20.
**B1:** đóng các cổng còn lại của kế hoạch mục 2.1 (bảng transition khớp code từng cạnh); `go test -race -count=1 ./pkg/rollup`.
**Nghiệm thu:** tài liệu và code khớp; A1, A2, B1 đánh dấu ☑ kèm bằng chứng trong PR.

### N5 — B2 store per-key (sau N4/B1)
Lưu `RollupRecord` per-key trong `SmartContractDB` (không nạp cả blob), resume sau crash (`ScanNonTerminal`). `SmartContractDB` **không có duyệt theo tiền tố**: cần thiết kế chỉ mục riêng, ghi rõ trong PR.
**Nghiệm thu:** test crash/restart; chi phí **không tăng theo số record** (dùng cách đo ở N6).

### N6 — A0 → B3 Parent Chain (`NodeFloatAccount`, `ClaimedMessages`), lưu per-key
**A0:** đối chiếu lại bảng khoảng cách trong `SEQUENCER_DESIGN.md` mục 3.1 với symbol đang tồn tại trong `execution/pkg/cross_chain/gateway.go` và `tx_processor/gateway_handler.go`. Lưu ý: `RecoveryCommittee` đã gỡ; unregister tự ký có nonce; `DeadChains` chỉ qua `SlashOnEquivocation`.
**B3:** khoá lưu `rollup_fa_v1`, `rollup_claimed_v1` (per-key), tái dùng `ChainRegistry` và `SlashOnEquivocation`, **không** sửa hành vi `outbound`/`attestCommit`/`claimMessage` cũ. Bất biến: `FA[chainID] ≥ 0`; tổng `FA` bảo toàn; `ClaimedMessages` ghi một lần; reclaim bị từ chối khi đã Claimed.
**Nghiệm thu bắt buộc về hiệu năng:** viết `BenchmarkGatewayHandler_Outbound`-tương-đương cho hàm mới (`pkg/blockchain/tx_processor/gateway_handler_bench_test.go` là mẫu; mỗi vòng phải thành công) ở ít nhất 50, 500, 3.000 bản ghi: chi phí mỗi giao dịch **không được tăng tuyến tính** như Gateway cũ (0,7 → 5,4 → 31 ms).
**Triển khai:** đổi trạng thái tuần tự hoá Gateway ⇒ phải wipe + nâng cấp đồng thời mọi node; kiểm bằng `ci.sh run-now --reset`.

### N7 — Sửa test data race có sẵn (nhỏ)
`TestSubscribeProcessor_ConcurrentAccess` (`cmd/simple_chain/processor/subscribe_processor_test.go:228`, race giữa `subscribe_processor.go:47` đọc và `:54` ghi). Sửa code (khoá đúng) hoặc test nếu test sai; **không** chỉ xoá test. **Nghiệm thu:** `go test -race ./cmd/simple_chain/processor/` sạch không cần `-skip`.

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
  N1  rà soát bền vững         N4  A1 → A2 → B1        N6  A0 → B3 (sau A2)     N7  sửa test race
  N2  runbook triển khai (cần D3 để chạy thật)

Sau D1 (C0 ☑):
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
