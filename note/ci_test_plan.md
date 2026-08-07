# Kế hoạch CI Test đầy đủ cho MetaNode

> Kế hoạch xây dựng pipeline CI test tự động bắt buộc cho Pull Request (Core Node: Go execution + C++ MVM + Rust consensus/NOMT; Smart Contract: Solidity trên MVM tự chế).
> **Bản 3 — 2026-08-07.** Mọi con số trong tài liệu này đều đã đo/kiểm chứng trực tiếp trên repo và máy m0, không phải ước lượng lý thuyết.
> **Trạng thái: kế hoạch, CHƯA triển khai.** Repo chưa bị sửa gì.

---

## 0. Kết luận điều hành

Ba điều quyết định hình dạng kế hoạch, tất cả đều mới phát hiện khi kiểm chứng thực tế:

1. **✅ Rào cản hạ tầng đã được gỡ.** Máy m0 có **104 core, 188 GB RAM, 638 GB đĩa trống** — dư sức chạy cả `cargo test` toàn workspace lẫn cụm 5 node. Toàn bộ lo ngại "GitHub runner 14GB đĩa không đủ" ở bản trước **không còn là vấn đề** khi dùng self-hosted. Đây là điều kiện đủ để tự động hoá 100%, kể cả nhóm test nặng nhất.

2. **🔴 Nhưng baseline đang ĐỎ — đây là việc phải làm trước tiên.** `cargo test --workspace` **không build nổi**, dừng ở 2 lỗi độc lập (chi tiết §1.3). Nghĩa là 315 test Rust "có sẵn" hiện **chưa chạy được test nào**, chứ không phải "chỉ cần bật lên là có ngay giá trị" như bản 2 viết. Phải dọn nợ này trước, giống hệt những gì `go-ci.yml` từng phải làm cho phía Go.

3. **⚠️ Multi-machine chưa sẵn sàng.** m1 (192.168.1.233) và m2 (192.168.1.234) hiện **không SSH được** từ m0 (`Permission denied (publickey,password)`). Mọi test đa máy (Lớp D, §2.2) bị chặn cho tới khi thiết lập SSH key.

**Thứ tự đúng: dọn baseline đỏ (§4 bước 1-2) → dựng runner trên m0 (bước 3) → nối test theo lớp (bước 4+).**

---

## 1. Hiện trạng đã kiểm chứng

CI hiện tại (`.github/workflows/go-ci.yml`) chỉ có 1 job: `go build / go vet / go test` cho module `execution` trên `ubuntu-latest`. File tự ghi chú **chưa cover crate Rust `consensus/metanode`**.

| Hạng mục | Thực tế đo được | Khoảng trống |
|---|---|---|
| Go test | 90 file `_test.go` / **290 package** (~31% package có test) | Chưa `-race`; độ phủ mỏng |
| Rust test | **315 `#[test]`/`#[tokio::test]`** trong `consensus/metanode` (201 file .rs) | **Chưa chạy CI lần nào, và hiện không build được** (§1.3) |
| Workspace Rust | `consensus/metanode` nằm trong root workspace | `cargo test` ở root là cover được, không cần job riêng |
| Benchmark | 3 file bench (trie, flat state trie, account state db) | Chưa vào CI, chưa so baseline `main` |
| Spec test contract | `TestEcAdd/EcMul/EcPairingPrecompile.sol` | Chưa chạy tự động |
| **E2E / recovery** | **Nhiều script hoàn chỉnh đã có** (§1.2) | Chưa nối CI; 1 script cần `sudo` |
| Orchestrator | `mtn-orchestrator.sh`, `NUM_NODES=5` | Đã đạt BFT n≥4 (f=1) — **không cần sửa** |
| Fuzzing | **Không có gì** | Nhóm 4 phải viết từ đầu |
| Security scan | Không `gosec`/`cargo-audit`/`cargo-deny`/Slither | Toàn bộ |
| Repo remote | `https://github.com/x3pi/metanode.git` | — |
| `gh` CLI trên m0 | **Chưa cài** | Cần cho thao tác runner/PR |
| Self-hosted runner | **Chưa cài** (`~/actions-runner` không tồn tại, không có systemd service) | Cần dựng |
| m1 / m2 | **SSH Permission denied** | Chặn test đa máy |
| Port cụm trên m0 | 8757, 10747-10750 hiện **đang trống**, không có node nào chạy | Nhưng sẽ đụng nếu dev chạy cụm tay |

### ⚠️ Đính chính so với bản 1
`execution/auto_test.sh` **không tồn tại**. Bản 1 ghi "đã có sẵn" là sai — dựa vào `execution/auto_test_guide.md` mà không kiểm tra. File guide đó cũng đã mục: còn trỏ đường dẫn repo cũ `/home/abc/nhat/con-chain-v2/`. → Cần xoá hoặc viết lại (§4 bước 12).

### 1.2 Tài sản test sẵn có — điểm mạnh lớn nhất của dự án

Trong `consensus/metanode/scripts/`:

| Script | Nội dung | Exit code dùng được cho CI? |
|---|---|---|
| `e2e_test_suite.sh` | 5 test: hash parity đa node, quét log `FORK`/`PANIC`/`DIVERGE`, restart recovery, **DAG wipe recovery**, post-recovery parity | ✅ có `exit 1` |
| `test_snapshot_stability_loop.sh` | Lặp snapshot recovery nhiều vòng, 5 node, xoay vòng node đích, sinh báo cáo MD | ✅ `exit 1` khi có round fail |
| `fault_injection/test_zero_fork.sh` | Kiểm tra trực tiếp Zero-Fork Invariant | ⚠️ có, nhưng **cần `sudo`** |
| `test_epoch_stress.sh` | Stress biên epoch (đúng bug #5) | cần kiểm tra thêm |
| `test_validator_restart_rejoin.sh` | Validator rời & tái nhập consensus | cần kiểm tra thêm |
| `test_restart_loop.sh`, `test_single_restart.sh` | Vòng lặp restart | cần kiểm tra thêm |
| `test_genesis_mismatch.sh`, `..._multi_epoch.sh` | Phát hiện lệch genesis | cần kiểm tra thêm |
| `ci_monitor.sh` | Tự phát hiện node chết/zombie port, tự restart | công cụ hỗ trợ CI |
| `build_check.sh` | Kiểm tra build | công cụ hỗ trợ CI |

**Hệ quả:** phần lớn Nhóm 7 là **wiring**, không phải viết mới.

### 1.3 🔴 Sức khoẻ baseline: `cargo test --workspace` đang HỎNG

Đo trực tiếp, dừng ở 2 lỗi build **độc lập nhau**:

**Lỗi 1 — khai báo binary chết:**
```
error: couldn't read `crates/metanode-keytool/src/bin/test_key.rs`: No such file or directory
error: could not compile `metanode-keytool` (bin "test_key" test)
```
`crates/metanode-keytool/Cargo.toml` khai báo `[[bin]] name = "test_key"` trỏ tới file không tồn tại (thư mục `src/` chỉ có `main.rs` và `lib.rs`). Sửa = xoá khối `[[bin]]` đó, rủi ro gần như bằng 0.

**Lỗi 2 — test code lệch API (nặng hơn):**
```
error[E0308]: mismatched types
  --> crates/meta-tls/src/lib.rs:301
      expected `BTreeSet<[u8; 32]>`, found `BTreeSet<Ed25519PublicKey>`
error: could not compile `meta-tls` (lib test) due to 3 previous errors
```
Test trong `crates/meta-tls` gọi `verifier.rs` theo API cũ (nhận `Ed25519PublicKey`), trong khi `verifier.rs:48,54` giờ nhận `[u8; 32]`. Đây là **test bị bỏ quên khi refactor** — cần sửa test cho khớp API mới (hoặc xác định API mới sai).

**Ý nghĩa với kế hoạch:** bản 2 ghi "bật `cargo test` là việc rẻ nhất, 30-60 phút, có ngay 315 test giá trị" — **sai**. Thực tế phải dọn nợ trước, và **chưa biết bao nhiêu trong 315 test đó thực sự pass**, vì hiện chưa compile nổi để chạy. Đây chính xác là kịch bản mà comment trong `go-ci.yml` mô tả từng xảy ra phía Go ("stale files, duplicate func main, renamed test API"). Sau khi build được mới đo tiếp được số test pass/fail và thời gian chạy.

### 1.4 Đặc thù rủi ro riêng của MetaNode

`note/known_bugs/` ghi lại **5 vụ fork thật**:
1. `commit_info_sync_fork.md` — catch-up thiếu `CommitInfo`/`reputation_scores` → `LeaderSchedule` sai.
2. `missing_block_heuristic_fork.md` — bỏ qua block mồ côi khi phục hồi snapshot → subdag lệch → bầu leader khác nhau.
3. `snapshot_recovery_linearizer_fork.md` — script snapshot copy sai thư mục PebbleDB sharded + fallback dùng timestamp cục bộ → lệch 3ms → fork vĩnh viễn.
4. `startup_sync_persistence_fork.md` — `SaveBlockByHash()` không flush đồng bộ `lastBlockHashKey`.
5. `synthetic_baseline_patch_fork.md` — epoch auto-unsuppress mỗi node tự đặt `now_ms` → EndOfEpoch lệch round → txRoot fork.

**Điểm chung:** không vụ nào xảy ra lúc mạng chạy ổn định — **100% ở đường phục hồi/restart/cold-sync**. Mọi chỗ dùng **giá trị cục bộ (RAM cache, wall-clock `now()`, thứ tự đọc đĩa) thay vì giá trị mạng đã đồng thuận** đều là nguồn fork. **Test single-node không bắt được loại này** — cần ≥2 node ở 2 trạng thái khác nhau rồi so state root.

Đáng chú ý: bug #3 có nguyên nhân nằm **trong chính script test**. → Script test cũng là code cần bảo trì; đưa vào CI giúp nó không bị mục.

4 branch `fix-fork-xapian`, `fix-fork-v2`, `fix-fork-xv1`, `fix-xapian` cho thấy precompile **Xapian** (search engine chạy trong path đồng thuận) từng gây fork nhiều lần.

Hai khu vực rủi ro cao **chưa có sự cố và chưa có test**: TEE path (branch `tee`, crate `metanode-tee-revm`) cần parity 100% với path thường; và `cross_chain_handler` (bridge).

---

## 2. 🖥️ Phân loại test theo nhu cầu máy chủ

Đây là phần trả lời trực tiếp câu hỏi "test nào cần hệ thống máy chủ".

### 2.1 Bốn lớp

| Lớp | Test | Loại runner | Cần gì | Chạy khi nào |
|---|---|---|---|---|
| **A — Không cần máy chủ** | `gofmt`, `go vet`, `cargo fmt --check`, `cargo clippy`, `govulncheck`, `cargo audit/deny`, `gosec` | GitHub-hosted `ubuntu-latest` (miễn phí) | Không | Mọi PR |
| **B — Máy build & unit test** | `cargo test --workspace` (315 test), `go test` full (có CGO), `go test -race`, spec test precompile `.sol`, benchmark | Self-hosted, **không cần cụm** | ~16 core, 32 GB RAM, **300 GB đĩa** | Mọi PR |
| **C — Máy chạy cụm E2E** | `e2e_test_suite.sh`, `test_snapshot_stability_loop.sh`, `test_epoch_stress.sh`, `test_validator_restart_rejoin.sh`, `test_restart_loop.sh`, `test_zero_fork.sh`, devnet sanity | Self-hosted, **cần môi trường độc quyền** | ~16-32 core, 64 GB RAM, **500 GB đĩa**, quyền `sudo`, port 8757 + 10747-10750 độc quyền | PR đụng path consensus/sync + nightly |
| **D — Nhiều máy** | `deploy_cluster.sh` multi-machine, TPS blast 20k tx, deep fuzz 6-12h, stress dài | 3+ máy self-hosted | m0 + m1 + m2, **SSH key liên thông** | Nightly / theo lịch |

### 2.2 Đối chiếu với hạ tầng hiện có

| Nhu cầu | Hiện trạng | Kết luận |
|---|---|---|
| Lớp A | GitHub-hosted có sẵn | ✅ Sẵn sàng |
| Lớp B (16 core / 32 GB / 300 GB) | **m0 có 104 core / 188 GB / 638 GB** | ✅ Dư gấp nhiều lần |
| Lớp C (16-32 core / 64 GB / 500 GB) | m0 vẫn dư | ✅ Được, **nhưng xem cảnh báo bên dưới** |
| Lớp D (3 máy, SSH liên thông) | **m1/m2 SSH Permission denied** | ❌ **Bị chặn** — cần thiết lập SSH key trước |

**⚠️ Cảnh báo quan trọng về Lớp C:** m0 vừa là máy dev vừa là nơi chạy cụm tay. Script E2E dùng **port cố định** (8757, 10747-10750) và thao tác **xoá sạch dữ liệu node**. Nếu CI chạy Lớp C trên m0 trong lúc dev đang chạy cụm → **CI xoá mất dữ liệu dev, hoặc job đụng port và đỏ oan**. Ba cách xử lý, cần chọn 1:
- **(a) Máy riêng cho Lớp C** — sạch nhất, cần thêm 1 máy.
- **(b) Dùng chung m0 + `concurrency group` = 1 + quy ước không chạy cụm tay khi CI chạy** — không tốn máy, nhưng phụ thuộc kỷ luật con người.
- **(c) Tham số hoá port + thư mục dữ liệu trong script** — bền nhất về lâu dài, nhưng phải sửa nhiều script (~2-4 giờ).

**Khuyến nghị:** (a) nếu cấp được máy — vì Lớp C là lớp bảo vệ đúng loại rủi ro nguy hiểm nhất của dự án, không nên để nó flaky vì tranh chấp tài nguyên. Nếu chưa cấp được thì (b) trước, (c) làm dần sau.

### 2.3 Ghi chú cấu hình runner
- `gh` CLI **chưa cài trên m0** — cần cài để thao tác runner/PR.
- Runner cần đăng ký bằng token từ GitHub (`https://github.com/x3pi/metanode` → Settings → Actions → Runners) — **thao tác này cần user tự làm**, vì cần quyền admin repo.
- Nên chạy runner dưới **systemd service** để tự khởi động lại.
- Runner nhận PR từ **fork** là rủi ro bảo mật (code lạ chạy trên máy nội bộ) → nên giới hạn chỉ chạy với PR từ branch nội bộ, hoặc bật yêu cầu approval.
- Gắn **label** cho runner để phân lớp: ví dụ `self-hosted,build` (Lớp B) và `self-hosted,cluster` (Lớp C).

---

## 3. Bảy nhóm test

### Nhóm 1 — Static Analysis & Security *(Lớp A)*
`gofmt`, `go vet` (đã có), `cargo clippy -D warnings`, `cargo fmt --check`, `solhint`/`prettier` cho `.sol`; `govulncheck`, `cargo audit`/`cargo deny`, `gosec`. Slither cho contract **cần POC trước** — MVM không phải EVM chuẩn, chưa chắc parse được contract dùng precompile Xapian.

### Nhóm 2 — Unit Test & Determinism *(Lớp B)*
- Sau khi dọn baseline (§1.3): bật `cargo test --workspace` cho 315 test Rust.
- Mở rộng test BLS, thêm round-trip protobuf/codec.
- Determinism single-node: chạy lại 1 block qua Block-STM với thứ tự thread khác nhau → cùng `Hash(State_t+1)`. **Lưu ý: loại test này không bắt được 5 bug fork thật** (vốn cần đa node); nó bắt nhóm bug Block-STM (abort-storm, duplicate-GEI). Đáng làm, nhưng đừng nhầm là lá chắn chống fork.

### 3.2 ⚠️ Cảnh báo cụ thể về `go test -race` *(Lớp B)*
`execution/executor/ffi_bridge.go:66` đặt `executeBlockResponseTimeout = 10 * time.Second` — timeout wall-clock cứng qua biên CGO. Race detector làm Go chậm **2-20 lần**:
- Timeout 10s sẽ bắn oan → đỏ giả hàng loạt.
- Nghiêm trọng hơn: **đường xử lý timeout đó chính là đường liên quan fork** (log ghi rõ *"treating as failure so Rust can retry"*) → chạy `-race` đẩy hệ thống vào đúng nhánh hiếm ta đang muốn bảo vệ, kết quả test không phản ánh production.
- Commit `e1e8e9a2` cho thấy nhóm đã **cố ý giữ** timeout này (loại nó khỏi PR #37) → **không được nới hằng số này để làm CI xanh**.

**Khuyến nghị:** không bật `-race` toàn cục. (a) `-race` chỉ cho package thuần Go không CGO; (b) `-race` toàn bộ ở Lớp B với timeout FFI nới qua **biến môi trường chỉ dành cho test**, ghi rõ là cấu hình test.

### Nhóm 3 — Protocol State & Integration *(Lớp B + C)*
Không dùng Ethereum State Tests (MVM ≠ EVM). Nâng test precompile sẵn có thành bộ "spec test" riêng (Lớp B). JSON-RPC integration qua `cmd/tool/tool-test-chain/test-rpc` (Lớp C, cần cụm chạy).

### Nhóm 4 — Fuzzing & Property-Based *(Lớp C/D, nightly)*
Bounded fuzz cho P2P decoder, tx pool parser (`go test -fuzz`), biên FFI Go↔Rust; `cargo fuzz` cho parse consensus/NOMT. Invariant: tổng cung không đổi qua N block, trie root nhất quán đa node. **Tốn công nhất vì chưa có gì** — ưu tiên cuối.

### Nhóm 5 — Gas & Storage Benchmarking *(Lớp B, máy riêng)*
Chạy benchmark sẵn có, so baseline `main`, cảnh báo nếu lệch quá ngưỡng (5-10%, tinh chỉnh dần). **Chỉ có ý nghĩa trên máy tải ổn định** — chạy trên runner chia sẻ cho số liệu nhiễu, tạo cảnh báo rác. Gas regression cho contract cần instrumentation trong MVM trước (việc riêng, chưa ước tính).

### Nhóm 6 — Mini-Devnet & Consensus Simulation *(Lớp C)*
Dùng `mtn-orchestrator.sh` (sẵn 5 node = BFT f=1, **không cần sửa**). Sanity flow: gửi tx → đóng block → mọi node hội tụ cùng state root. Cần bản "lite": ít tx, timeout ngắn.

### Nhóm 7 — Recovery & Restart Fork Regression *(Lớp C — giá trị cao nhất)*

| Kịch bản | Khoá bug | Trạng thái |
|---|---|---|
| Hash parity đa node + quét log FORK/PANIC/DIVERGE | tất cả | ✅ `e2e_test_suite.sh` |
| Restart recovery + DAG wipe recovery | #2, #4 | ✅ `e2e_test_suite.sh` |
| Snapshot recovery lặp nhiều vòng | #1, #2, #3 | ✅ `test_snapshot_stability_loop.sh` |
| Validator rời & tái nhập | #1 | ✅ `test_validator_restart_rejoin.sh` |
| Stress biên epoch | #5 | ✅ `test_epoch_stress.sh` |
| Zero-Fork invariant | tất cả | ⚠️ `test_zero_fork.sh` (cần sudo) |
| Kill ngay sau catch-up, trước block mới | #4 | ❌ **chưa có, cần viết** |
| Xapian determinism đa node có restart xen giữa | branch `fix-fork-xapian*` | ❌ **chưa có, cần viết** |

Nguyên tắc khi viết 2 kịch bản còn thiếu: luôn dựng **≥2 node ở 2 trạng thái khác nhau** (1 live liên tục, 1 vừa restart/sync/suppress) rồi so state root.

---

## 4. Lộ trình triển khai

| Bước | Việc | Lớp | Ước tính | Ghi chú |
|---|---|---|---|---|
| **1** | **Sửa lỗi 1 (§1.3): xoá `[[bin]] test_key` chết trong `metanode-keytool/Cargo.toml`** | — | 5 phút | Rủi ro ~0 |
| **2** | **Sửa lỗi 2 (§1.3): đồng bộ test `meta-tls` với API `verifier.rs` mới** | — | 30-90 phút | Cần đọc kỹ xem test sai hay API sai |
| **3** | Đo lại: `cargo test --workspace` build bao lâu, **bao nhiêu test thực sự pass** | — | 30-60 phút | Có thể lộ thêm test hỏng → phát sinh |
| **4** | Cài `gh` CLI + dựng self-hosted runner trên m0 (systemd, gắn label `build`/`cluster`) | B, C | 1-2 giờ | **Cần user lấy token từ GitHub** |
| **5** | Workflow Lớp A trên GitHub-hosted: lint/format/audit | A | 1 giờ | Rẻ, độc lập, làm song song được |
| **6** | Workflow Lớp B: `cargo test`, `go test` full, spec test precompile | B | 1-2 giờ | Sau bước 3 |
| **7** | Chốt phương án cô lập Lớp C (§2.2: máy riêng / concurrency=1 / tham số hoá port) | C | Quyết định | **Chặn bước 8** |
| **8** | Nối `e2e_test_suite.sh` + `test_snapshot_stability_loop.sh` vào CI: chuẩn hoá exit code, timeout, thu artifact báo cáo MD | C | 2-4 giờ | **Mốc giá trị lớn nhất** — lần đầu rủi ro fork-khi-recovery được CI bảo vệ |
| 9 | Nối tiếp `test_epoch_stress.sh`, `test_validator_restart_rejoin.sh`, `test_restart_loop.sh` | C | 1-2 giờ | |
| 10 | Sửa `test_zero_fork.sh` bỏ phụ thuộc `sudo`/LVM (hoặc tách job riêng) | C | 1-2 giờ | |
| 11 | Viết 2 kịch bản còn thiếu: kill-sau-catch-up (#4), Xapian determinism | C | 2-3 giờ | Phần "viết mới" thật sự của Nhóm 7 |
| 12 | `-race` theo chiến lược §3.2 (tách package CGO/không-CGO) | B | 2-4 giờ + thời gian fix race lộ ra | **Rủi ro thời gian lớn nhất**, đừng đặt ở đường tới hạn |
| 13 | Determinism single-node + regression Block-STM (duplicate-GEI, abort-storm, walkback) | B | 2-4 giờ | |
| 14 | Benchmark so baseline `main` | B | 45-60 phút | Chỉ chạy trên máy tải ổn định |
| 15 | Xoá hoặc viết lại `auto_test_guide.md` (mô tả script không tồn tại) | — | 15-30 phút | Nợ tài liệu, rẻ, nên làm sớm |
| 16 | Thiết lập SSH key m0↔m1↔m2 để mở khoá Lớp D | D | 30-60 phút | Hiện đang bị chặn |
| 17 | Nightly: fuzzing (Go + Rust), stress dài, TPS blast, Slither POC | C, D | Lớn nhất, viết từ 0 | Ưu tiên cuối |
| 18 | Backlog: TEE-vs-non-TEE parity, cross-chain bridge validation | B, C | Chưa khảo sát đủ | Rủi ro cao theo thông lệ ngành |

**Ước tính gộp:**
- Bước 1-3 (dọn baseline): **~1-2.5 giờ** — bắt buộc trước mọi thứ, và có thể phát sinh nếu lộ thêm test hỏng.
- Bước 4-6 (runner + Lớp A/B): **~3-5 giờ**.
- Bước 7-11 (Lớp C, giá trị cao nhất): **~6-11 giờ**.
- Bước 12-18: **~8-14 giờ**, biến động lớn do `-race` và fuzzing.

**Đường ngắn nhất tới giá trị thật: 1 → 2 → 3 → 4 → 7 → 8.** Xong bước 8, loại rủi ro nguy hiểm nhất của dự án (fork khi recovery) lần đầu tiên được CI bảo vệ tự động.

---

## 5. Rủi ro & nguyên tắc

- **Zero-Fork Invariant:** mọi test consensus/Block-STM phải verify state root khớp tuyệt đối giữa các node — không nới lỏng để test "xanh" giả.
- **Không nới FFI timeout để làm CI xanh** (§3.2). `executeBlockResponseTimeout` là quyết định thiết kế có chủ ý; test đỏ vì nó thì sửa cách chạy test, không sửa hằng số production.
- **Nguyên tắc "no local-only fallback"** (rút từ cả 5 bug): mọi giá trị trong path consensus/sync phải bắt nguồn từ thứ mạng đã đồng thuận, không phải RAM/wall-clock/thứ tự đọc đĩa cục bộ. Nên thành câu hỏi bắt buộc khi review PR đụng path này.
- **Script test là code production của CI:** bug #3 có nguyên nhân nằm trong chính script test khi DB đổi sang PebbleDB sharded. Đổi cấu trúc lưu trữ → phải rà soát script test cùng lúc.
- **Cô lập môi trường Lớp C** (§2.2): script dùng port cố định và **xoá dữ liệu node** → tuyệt đối không để CI và cụm dev giẫm chân nhau.
- **Bảo mật self-hosted runner:** PR từ fork chạy code lạ trên máy nội bộ. Giới hạn trigger hoặc bật approval.
- **BFT n≥4:** `mtn-orchestrator.sh` đã dùng 5 node — giữ nguyên.
- **Không đưa fuzz/stress nặng vào PR-gate:** CI chậm/flaky làm team mất niềm tin rồi bỏ qua cảnh báo — tác dụng ngược.
- **CI đỏ vì hạ tầng nguy hiểm hơn không có CI:** thà để nightly còn hơn gate PR bằng job hay đỏ oan.
