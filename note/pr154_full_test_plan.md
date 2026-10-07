# Kế hoạch test đầy đủ sau merge PR #154 (dành cho agent thực thi)

> Ngày lập: 2026-10-04. Áp dụng cho `origin/dev` ≥ `17a7c701`.
> Mục tiêu: xác nhận PR #154 (`tps-test`) + 2 commit sửa tiếp (`16dce434`, `17a7c701`) không gây hồi quy,
> đặc biệt đường **khởi động / rebuild mapping**, **RPC receipt/tx lookup**, **raftfeed admin**.
> **PHẠM VI: CHỈ TEST LOCAL trên máy hiện tại (192.168.1.232). KHÔNG chạm bất kỳ máy nào khác (231, 230, 233, 234, ...).**
> Agent thực thi: làm đúng thứ tự các giai đoạn, ghi kết quả THẬT vào mục "Báo cáo" cuối file. Không ghi PASS nếu chưa đọc output.

## 0. Thay đổi cần phủ (để biết test nhắm vào đâu)

| Thay đổi | File | Rủi ro |
| :--- | :--- | :--- |
| Startup rebuild mapping: duyệt block→hash, tx→block, eth→bls; flush theo batch 5000; marker `full_mapping_rebuild_v1_complete`; lỗi chỉ log (không Fatal) | `execution/pkg/blockchain/mapping_rebuild.go`, `execution/cmd/simple_chain/app_blockchain.go` | startup chậm, mapping thiếu, node không lên |
| `GetBlockNumberByTxHash` = alias của `...Fast` (bỏ lazy walkback); xoá code walkback chết + `MarkSubmittedPending` | `execution/pkg/blockchain/blockchain.go`, `cmd/simple_chain/rpc_transaction.go` | tx cũ "not found" nếu mapping mất |
| `GetTransactionReceipt` đọc tx trực tiếp từ txDB của block; lỗi → `nil` | `cmd/simple_chain/rpc_transaction.go` | receipt sai type/nonce/contractAddress/blob fields |
| Xoá `pkg/file_handler`, chuyển `abi_file` → `pkg/abi_file` | `pkg/utils/file_handler_helper`, `cmd/rpc-client`, `cmd/tool/...` | compile / upload file chunk |
| AdminClient AddReplica/RemoveReplica retry leader 5s; test raftfeed nới timeout, skip khi <2 CPU | `pkg/rollup/raftfeed/*` | test flaky |

## 1. Quy tắc an toàn (BẮT BUỘC đọc)

1. **Chỉ local.** Máy chạy test là 192.168.1.232 (`hostname -I` để xác nhận). Cấm SSH/scp/rsync/ansible tới máy khác. Cụm 231/230 và mọi máy ngoài 232 là **ngoài phạm vi**, không đụng.
2. Trước KHÔNG chạy bất kỳ lệnh deploy nào mà chưa kiểm tra đích: `deploy/ansible/inventory.yml` hiện chỉ có host 192.168.1.232 (`ansible_connection: local`) — OK. Nếu inventory bị đổi có host khác → DỪNG, hỏi user. Không dùng `inventory.example.yml` (có .234/.230).
3. Máy đang chạy cụm **local parent_chain** (cổng HTTP 18601-18604, thư mục `deploy/cluster/local_parent_chain/`). Đây là tiến trình của user: **KHÔNG kill / reset / ghi đè** khi chưa hỏi. Kiểm tra `pgrep -af 'simple_chain|metanode|parent_chain'` và `ss -ltnp` TRƯỚC khi dựng cụm test; chọn cổng và thư mục dữ liệu không trùng. Nếu trùng cổng → hỏi user.
4. Các lệnh phá dữ liệu (`--reset`, `--reset-exec`, `--restart-chain`, `--fresh`, `ansible_deploy.sh --reset-all`) chỉ chạy trên cụm test do agent tự dựng trong thư mục riêng (vd. dưới scratchpad hoặc `private_chain_3node/`), nói rõ thư mục/cổng trước khi chạy. `ci.sh run-now` có `pre_action` gọi `ansible_deploy.sh --reset-all` lên **chain mặc định của `deploy/systemd`/`deploy/ansible` trên máy này** → chỉ chạy khi user đã đồng ý reset cụm đó; không thì dùng orchestrator/script trực tiếp (xem phần 5).
5. Không `git add <dir>`; chỉ `git add <file>` theo tên. Không `git push` khi chưa hỏi user.
6. Không sửa logic consensus để "cho test xanh". Test đỏ → ghi lại + báo, hoặc sửa nguyên nhân thật rồi chạy lại từ đầu giai đoạn đó.
7. Không dùng timeout/sleep để quyết định dispatch commit (Zero-Fork Invariant). Test fail do fork/divergence = **dừng hết, báo user ngay**.
8. Không in token Telegram trong `deploy/ci/ci_config.yaml` ra báo cáo. Khi chạy test local, tắt thông báo (`telegram.enabled: false` trong bản config local, KHÔNG commit) để khỏi gửi tin ra kênh thật.
9. Mọi kết quả (PASS/FAIL, số liệu, log path) phải đọc từ output thật. Từng có script in "PASSED" giả (`audit_pack`) và agent báo sai benchmark — luôn đọc script thực chạy gì.

## 2. Giai đoạn A — Build & static (không cần cụm, ~5-10 phút)

```bash
cd /home/abc/chain-n/metanode
git fetch origin && git checkout dev && git pull --ff-only
git log --oneline -3                      # phải thấy 17a7c701
cd consensus/metanode/scripts && ./build_check.sh      # kỳ vọng: ALL BUILDS PASSED (4/4)
cd /home/abc/chain-n/metanode/execution
gofmt -l pkg/blockchain/mapping_rebuild.go pkg/blockchain/blockchain.go cmd/simple_chain/app_blockchain.go cmd/simple_chain/rpc_transaction.go pkg/rollup/raftfeed/
go vet ./pkg/blockchain/ ./pkg/rollup/raftfeed/ ./cmd/simple_chain/...
grep -rn 'pkg/file_handler"' --include=*.go . | grep -v '^./cmd/rpc/'   # kỳ vọng: rỗng
grep -rn 'MarkSubmittedPending\|walkbackNotFound\|rebuildTxMappingByWalkback' --include=*.go . | grep -v '^./cmd/rpc/'  # kỳ vọng: rỗng
```
Ghi nhận: `cmd/rpc/` là module Go riêng có bản `pkg/file_handler` copy riêng — không phải lỗi.
Cũng chạy: `cd cmd/rpc && go build ./...` (module riêng; link lỗi native thì lọc theo `*.go:` như cách đã làm, đừng nhầm với lỗi compile).

**Tiêu chí qua:** build_check 4/4, vet không có lỗi `.go:NN`, gofmt rỗng cho các file trên, hai grep rỗng.

## 3. Giai đoạn B — Go unit/integration test (không cần cụm, ~10-30 phút)

Cần thư viện native đã build (`target/release/libmtn_nomt`, `mvm_linker.hpp`). Chạy ở cây làm việc chính, không ở worktree trống.

```bash
cd /home/abc/chain-n/metanode/execution
go test ./pkg/blockchain/... 2>&1 | tee /tmp/B1_blockchain.txt
go test -race ./pkg/blockchain/ 2>&1 | tee /tmp/B2_blockchain_race.txt
go test ./pkg/rollup/raftfeed/... 2>&1 | tee /tmp/B3_raftfeed.txt          # có thể chậm, tối thiểu 2 CPU
go test -count=3 ./pkg/rollup/raftfeed/ 2>&1 | tee /tmp/B4_raftfeed_x3.txt   # kiểm tra flaky
go test ./cmd/simple_chain/... 2>&1 | tee /tmp/B5_simple_chain.txt
go test ./pkg/utils/... ./pkg/transaction_state_db/... 2>&1 | tee /tmp/B6_utils.txt
```

Test mới cần VIẾT (nếu chưa có — agent kiểm tra `grep -n RebuildMappingsFromBlock -r --include=*_test.go`):
1. `TestRebuildMappingsFromBlock_*` trong `pkg/blockchain/`:
   - dựng chuỗi N block giả với tx; xoá mapping block→hash, tx→block, eth→bls trong storage mapping → gọi rebuild → mapping khôi phục đủ.
   - `maxBlocks=50` chỉ duyệt đúng 50 block (không đọc cha của block thứ 50).
   - `lastPruned>0`: dừng đúng ở `lastPruned+1`, không lỗi "cannot walk to parent".
   - mapping tồn tại nhưng sai hash → được sửa.
   - parent thiếu → trả `error` (không panic), phần đã rebuild vẫn flush.
2. `GetBlockNumberByTxHash` sau khi mất mapping trong DB: trả `false` (không còn lazy walkback) — test ghi rõ hành vi mới.
3. `GetTransactionReceipt`: tx type≠0, contract deploy (contractAddress = CreateAddress(from, nonce)), blob tx (blobGasUsed/blobGasPrice), tx không có trong block → `nil, nil`.

**Tiêu chí qua:** B1,B2,B3,B5,B6 xanh; B4 xanh 3/3 lần (nếu flaky: ghi tên test + tỉ lệ fail, không bỏ qua). `-race` không có DATA RACE.

## 4. Giai đoạn C — Rust consensus test (không cần cụm, ~10-20 phút)

```bash
cd /home/abc/chain-n/metanode
cargo test --workspace --locked 2>&1 | tee /tmp/C1_cargo_test.txt
```
Kỳ vọng: không hồi quy so với trạng thái đã ghi (189/189 consensus-core pass, 0 flake — xem `note/ci_test_plan.md`). PR #154 không chạm Rust nên mọi fail ở đây là fail có sẵn hoặc flake: chạy lại riêng test đó 5 lần, ghi nhận.

## 5. Giai đoạn D — Cụm 1 chain (E2E, cụm TEST)

Chọn một, tất cả đều local trên 232: `private_chain_3node/` (3 node, KHÔNG có dung sai BFT, chỉ để smoke) hoặc cụm 4-5 node dựng bằng `consensus/metanode/scripts/mtn-orchestrator.sh` (`start --fresh`, NUM_NODES=5, một process Go/node). Ưu tiên 4-5 node cho test restart/kill. Cụm parent_chain đang chạy (18601-18604) là của user, không dùng làm cụm test và không dừng nó.

Các lệnh `ci.sh run-now ...` bên dưới chỉ dùng được khi user đồng ý cho reset chain mặc định trên máy này (xem quy tắc 4). Nếu chưa được đồng ý: tự dựng cụm bằng orchestrator rồi chạy trực tiếp script của từng bài (`cwd`/`command` trong `deploy/ci/ci_config.yaml`, ví dụ `run_restart_test.sh --loop 6 --sleep-between 15 --count 15`, `run_snapshot_test.sh --loop 6 --count 15`, `run_tps_test.sh --no-reset ...` ở metanode-suite), bỏ qua `pre_action`.

### D1. Smoke + CI bản có sẵn (config `deploy/ci/ci_config.yaml`)
```bash
cd /home/abc/chain-n/metanode
./ci.sh run-now --dry-run                    # xem kế hoạch trước, đọc kỹ pre_action nào sẽ reset
./ci.sh run-now --only tps_blast             # pre_action prepare_tps (xác nhận KHÔNG chạy trên 231/230)
./ci.sh run-now --only node_chaos_restart    # restart chain + 6 vòng rolling restart + zero-fork
./ci.sh run-now --only snapshot_recovery     # reset_chain → CHỈ trên cụm test
./ci.sh run-now --only blockstm_logic        # 32+ kịch bản Block-STM
./ci.sh run-now --only spam_contract
```
(`blockstm_logic`, `snapshot_recovery`, `spam_contract` đang `enabled: false` trong config — dùng `--only` để ép chạy, nếu không được thì tạm bật trong bản config local, KHÔNG commit.)
Đọc `deploy/ci/README.md` để biết file log và cách báo kết quả. Lần chạy full gần nhất đã PASS 100% (`./ci.sh run-now --reset`, 2026-09-25) → baseline để so.

### D2. Kịch bản đặc thù cho PR #154 (kiểm tra thủ công có script)

| # | Kịch bản | Cách làm | Kỳ vọng |
| :--- | :--- | :--- | :--- |
| D2.1 | Rebuild lần đầu | Chạy cụm với dữ liệu cũ chưa có marker; xem log `[STARTUP-REBUILD]` | log "Marked full mapping rebuild complete"; ghi thời gian; node lên được |
| D2.2 | Restart sạch | Stop bình thường → start | `rebuildMaxBlocks=50`, log "All N checked ... intact", thời gian < vài giây |
| D2.3 | Crash | `kill -9` 1 node (không phải quorum) → start lại | full walk, mapping khôi phục, node bắt kịp, KHÔNG fork |
| D2.4 | Receipt tx cũ | Gửi ≥2000 tx, restart node, gọi `eth_getTransactionReceipt` + `eth_getTransactionByHash` cho tx đầu/giữa/cuối | trả đúng, các trường `type`, `contractAddress` (tx deploy), `status`, `blockNumber` khớp với node không restart |
| D2.5 | Tx không tồn tại / pending | receipt cho hash ngẫu nhiên và tx vừa submit | `null` (không phải error), pending vẫn lấy được qua cache |
| D2.6 | Rebuild lỗi | Giả lập: xoá 1 block cha trong block DB của node test (bản copy data) | node VẪN lên, log ERROR `[STARTUP-REBUILD] ... incomplete`, marker KHÔNG được ghi, lần start sau thử lại |
| D2.7 | Node pruned | Node có `lastPruned>0` | rebuild dừng ở ranh giới, không lỗi parent |
| D2.8 | So sánh đa node | Sau D2.3, so `eth_getBlockByNumber`/receipt giữa tất cả node cho cùng dải block | hash khớp 100% |
| D2.9 | Hot path submit | `tps_blast` trước/sau (so với `note/...` và `tps_report_*.json` gần nhất) | không tụt TPS (baseline ~6400 tx/s sau sig-enforcement; ghi số đo thật) |
| D2.10 | Upload file chunk | Gửi tx `uploadChunk` tới `OwnerFileStorageAddress` qua `SendRawTransactionWithDeviceKey` | PR bỏ nhánh đặc biệt: giờ MỌI tx đều `AddTxToCache`; xác nhận không làm phình RAM bất thường khi gửi chunk lớn (ghi RSS trước/sau) |

Với mỗi kịch bản: lưu lệnh đã chạy, đường dẫn log (`logs/execution/<date>/execution.log`), kết quả.

### D3. Chaos / zero-fork
```bash
cd /home/abc/chain-n/metanode/execution/scripts && cat chaos_burnin.sh | head -60   # đọc usage trước
```
Chạy `chaos_burnin.sh` (có mode `QUORUM_LOSS` theo commit `d118b7a1`) trên cụm test. Tham chiếu kết quả đã ghi: 200/200 và 12/12 long-outage — không được tệ hơn.
Sau mỗi chu kỳ: `grep -rEi 'FORK|PANIC|DIVERGE|fork_guard' logs/` phải rỗng; commit index/hash đồng nhất giữa node.
Chạy `consensus/metanode/scripts/e2e_test_suite.sh` (5 test: hash parity, quét log fork, restart recovery, DAG wipe recovery, post-recovery parity).

## 6. Giai đoạn E — Raft/rollup & cross-chain (chỉ local, tuỳ chọn nhưng nên chạy cho "full")

1. raftfeed cluster test thật (không phải harness trong process): theo `note/` về rollup Raft (memory: `execution/pkg/rollup/`), thêm/bớt replica bằng AdminClient trong lúc leader đang bầu lại → xác nhận retry 5s hoạt động, không treo vô hạn.
2. Cross-chain credit flow end-to-end (đã đóng 2026-09-29, commit `15761838`): chạy lại kịch bản credit, xác nhận số dư cộng bằng `AddBalance` đúng; conservation test `note/conservation_live_test_report_20261002.md` → chạy lại live conservation test, tổng cung không đổi.
3. Parent chain readiness: `deploy/ci/production_readiness_check.sh` — ĐỌC script trước; nếu nó SSH/gọi máy khác thì bỏ qua. Cụm parent_chain local đang chạy là của user, chỉ đọc (RPC GET), không restart.

## 7. Giai đoạn F — Drill vận hành (chỉ khi user cho phép, cụm test)

`deploy/ci/incident_drills/` (đọc kỹ từng script — nếu có SSH/máy khác thì bỏ qua; `drill_disk_full.sh`/`drill_network_partition.sh` có thể cần sudo/iptables: chỉ chạy khi user cho phép, trên cụm test local): `drill_node_full_resync.sh`, `drill_network_partition.sh`, `drill_disk_full.sh`. Bỏ `drill_telegram_alert.sh` (gửi tin thật ra kênh Telegram) trừ khi user yêu cầu.

## 8. Điều kiện kết luận

| Mức | Điều kiện |
| :--- | :--- |
| **GREEN – an toàn đóng PR** | A,B,C xanh + D1 (tps_blast, node_chaos_restart) xanh + D2.1-D2.8 đạt + D3 không có FORK/PANIC |
| **YELLOW** | Mọi thứ xanh nhưng D2.9 TPS tụt >5% hoặc D2.1 rebuild lần đầu quá lâu (ghi số) hoặc có test flaky |
| **RED – dừng, báo user** | Bất kỳ fork/divergence, panic, node không start, receipt sai dữ liệu, hoặc fail không giải thích được |

## 9. Báo cáo (agent điền)

Tạo `note/pr154_test_report_<YYYYMMDD>.md` với bảng: giai đoạn | lệnh | kết quả thật (PASS/FAIL) | log path | ghi chú.
Kèm: commit hash đã test, cụm đã dùng, các test đã viết mới (đường dẫn), mọi bất thường. Kết thúc báo cáo bằng khối tóm tắt tiếng Việt theo `AGENTS.md` Part 5.
Commit báo cáo bằng `git add <tên file>` cụ thể; không push khi chưa hỏi user.
