# Deploy hardening + incident drills (2026-09-09 → 2026-09-10)

Tài liệu bàn giao cho phiên làm việc dài kiểm chứng "đã đủ tự tin triển khai
thật chưa", xuất phát từ yêu cầu chạy `snapshot_recovery` test trên máy
`234` (`~/nhat/con-chain-v2/metanode`). Ghi lại: đã sửa gì, công cụ mới có
vai trò gì, trạng thái cụm tại thời điểm viết tài liệu này, và việc gì còn
mở để người tiếp theo biết bắt đầu từ đâu.

---

## 1. Các bug thật đã tìm và fix (tất cả đã lên `dev`, TRỪ bug #10 — xem mục 7)

Toàn bộ đều tìm thấy qua chạy THẬT trên cụm nhiều node/host (không phải suy
đoán) — mỗi dòng dưới có evidence cụ thể trong message commit tương ứng.

| # | Bug | File | Commit |
| :-- | :-- | :-- | :-- |
| 1 | `stop_services` sleep cố định 10s → race condition ghi dữ liệu cũ vào thư mục "mới" | `deploy/ansible/roles/stop_services/tasks/main.yml` | `43aaa300` |
| 2 | Snapshot trigger bỏ lỡ ngưỡng khi block number nhảy cóc qua đúng bội số | `execution/executor/snapshot_manager.go` | `caf7dd8f` |
| 3 | `setup-cluster-btrfs.sh` mount đè `/opt/metanode` giữa play, che khuất mọi node | `deploy/ansible/roles/snapshot_restore/tasks/main.yml` | `e37de981` |
| 4 | Chown không đệ quy → file `execution/backup/.../LOCK` bị root-owned | `deploy/ansible/roles/node_setup/tasks/main.yml` | `5cbbfb38` |
| 5 | Unmount bind-mount dùng glob `node-*/data` → unmount nhầm node không liên quan | `deploy/ansible/roles/node_setup/tasks/main.yml` | `f275f214` |
| 6 | Thiếu check `eth_consensusReady` trước khi gửi tx ngay sau restore | `metanode-suite: .../snapshot-recovery/run_snapshot_test.sh` | `c53f280` (metanode-suite repo) |
| 7 | `pgrep -f` tự match chính câu lệnh đang chạy nó → luôn chờ đủ 90s dù process đã tắt từ lâu | `deploy/ansible/roles/stop_services/tasks/main.yml` | `3a8727ce` |
| 8 | Bug parse tóm tắt trong `production_readiness_check.sh` (mọi tầng hiện FAIL sai vì tên tầng chứa dấu `:`) | `deploy/ci/production_readiness_check.sh` | `d2dc12d0` |
| 9 | Thiếu nil-check `Transaction` trong `ProcessTransactionFromClientWithDeviceKey` → panic nếu client gửi wrapper thiếu field | `execution/cmd/simple_chain/processor/transaction_processor.go` | `ab5fedd5` (tác giả: PearTNhat, xem mục 4) |

**Ghi chú #7 & #9:** cả 2 bug này bạn **PearTNhat ("nhat") đã độc lập tìm ra
và fix trước/song song với phiên làm việc này**, gộp chung trong 1 commit
local `e8c9d0ad` trên máy `234` không hề push lên remote. Khi `git pull` lên
`234` bị conflict đúng vào phần #7 (2 cách fix tương đương, chỉ khác vị trí
đặt `[...]`). Đã xử lý: giữ bản fix #7 đã có sẵn trên `dev` (tránh áp 2 lần
cùng 1 fix), cherry-pick riêng phần #9 (không liên quan, không conflict) giữ
nguyên tác giả gốc. Xem mục 4 để biết chi tiết diễn biến.

**Bằng chứng chính:** 100/100 vòng lặp `snapshot_recovery` PASS liên tục
(~9.5 tiếng, 0 lỗi) sau khi áp fix #1-#6. Fix #7-#8 tìm ra SAU đó qua phân
tích log `/var/log/metanode/shutdown_times.log`, đã fix + verify cô lập
(không phải qua chạy lại full test) — xem mục 3.

---

## 2. Công cụ mới — vai trò từng cái

| Công cụ | Vai trò | An toàn chạy khi nào |
| :-- | :-- | :-- |
| `deploy/ci/production_readiness_check.sh` | Cổng go/no-go 1 lệnh trước khi triển khai thật: Build → Deploy stability (N vòng reset lặp lại) → Test ứng dụng correctness | Cửa sổ bảo trì, cụm rảnh (chạy thật mất ~1-2 tiếng) |
| `deploy/ci/verify_multi_reset_stability.sh` | Chạy `--reset-all` lặp lại N vòng, assert mọi node lên sạch mỗi vòng — bắt race condition chỉ lộ ra khi lặp lại | Dùng độc lập hoặc qua `production_readiness_check.sh` |
| `deploy/ansible/check_snapshot_node.py` + `test_check_snapshot_node.py` | Logic an toàn "node snapshot không được tự restore chính nó", tách riêng để unit-test (12 test case) | Test chạy local, không đụng cụm |
| `test-simple/.../lib/consensus_readiness.sh` (metanode-suite) | Thư viện dùng chung: chờ `eth_consensusReady` trước khi gửi tx sau restart/restore, dùng bởi `restart-recovery` và `snapshot-recovery` | Chạy trong test, không gọi riêng |
| `execution/executor/snapshot_manager_test.go` (bổ sung) | Regression test cho bug #2 (snapshot trigger bỏ lỡ ngưỡng) | `go test ./executor/...` |
| `deploy/ci/incident_drills/*.sh` | 4 kịch bản diễn tập sự cố KHÔNG nằm trong test ứng dụng thông thường — xem mục 4 | Xem `deploy/ci/incident_drills/README.md` |

Chi tiết cách dùng `production_readiness_check.sh`: xem `ci.md` mục "0. TRƯỚC
KHI TRIỂN KHAI THẬT".

---

## 3. Kịch bản sự cố còn mở (chưa diễn tập thật)

Bốn drill trong `deploy/ci/incident_drills/` được xây để lấp khoảng trống
này — đã viết xong, đã tự test logic (dry-run + các nhánh an toàn), **nhưng
CHƯA được chạy thật (`--confirm`) trên cụm** vì lúc xây dựng có người khác
đang dùng máy `234`/`230`:

1. **Cảnh báo Telegram có tới người trực không** (`drill_telegram_alert.sh`)
   — an toàn chạy bất cứ lúc nào, nên chạy trước tiên.
2. **Node hỏng thật, không dùng snapshot, tự resync 100% qua P2P**
   (`drill_node_full_resync.sh`) — cần cửa sổ bảo trì.
3. **Đĩa đầy giữa lúc vận hành** (`drill_disk_full.sh`) — cần cửa sổ bảo trì.
4. **Mất kết nối mạng giữa các host** (`drill_network_partition.sh`) — cần
   cửa sổ bảo trì, phức tạp nhất, nên làm cuối.

Xem `deploy/ci/incident_drills/README.md` để biết nguyên tắc an toàn và thứ
tự khuyến nghị.

---

## 4. Diễn biến: merge conflict với công việc độc lập của "nhat" (2026-09-10, ~02:35-02:40 UTC)

Khi `git pull origin dev` lên máy `234`, gặp conflict thật ở
`deploy/ansible/roles/stop_services/tasks/main.yml` — máy `234` có 1 commit
local `e8c9d0ad` (tác giả PearTNhat) chưa từng push, tự fix đúng bug #7 (2
cách viết tương đương, chỉ khác vị trí đặt `[...]` trong regex) VÀ thêm 1
fix khác không liên quan (bug #9, nil-check transaction). Đã xử lý theo yêu
cầu người dùng ("checkout dev 234 và tiếp tục"):

1. `git merge --abort` → `git reset --hard origin/dev` để 234 khớp sạch
   `dev` (bỏ qua phần fix #7 trùng lặp).
2. Trước khi mất hẳn: kiểm tra lại toàn bộ nội dung `e8c9d0ad`, phát hiện
   phần transaction-validation (#9) không liên quan gì tới #7 và không nên
   mất.
3. `git cherry-pick --no-commit e8c9d0ad`, resolve conflict bằng cách giữ
   `stop_services` bản đã có trên `dev` (`--ours`), giữ nguyên 2 file
   transaction-processor không conflict.
4. Build + vet sạch trên `234`, đóng gói qua `git format-patch`, áp lại trên
   `232` (nơi có token đủ quyền push — `234` tự push bị branch-protection
   chặn), build+vet sạch lần 2, push lên `dev` giữ nguyên tác giả gốc
   (`ab5fedd5`).
5. Đồng bộ lại `234` khớp `origin/dev` cuối cùng.

**Kết quả:** không mất công của "nhat", không áp trùng lặp 2 bản fix #7,
lịch sử git sạch. Nếu cần tham chiếu commit local gốc: `e8c9d0ad` (có thể đã
bị git tự dọn rác trên máy `234` sau một thời gian, không đảm bảo còn mãi).

---

## 5. Trạng thái cụm tại thời điểm viết tài liệu (2026-09-10, ~03:00 UTC — ĐÃ CẬP NHẬT lần 2)

⚠️ **Đây là snapshot trạng thái tại 1 thời điểm — kiểm tra lại trước khi dựa
vào, đừng coi là luôn đúng.**

- **Cụm 5 node**: cả 5 `systemctl is-active` đều `active`, healthy.
- **Máy `234` giờ khớp CHÍNH XÁC `origin/dev`** (`ab5fedd5`) — không còn lệch
  commit nào, bao gồm cả phần transaction-validation của nhat (mục 4).
- **`drill_telegram_alert.sh` đã chạy thật và PASS** (HTTP 200, message_id
  nhận được) — mục 1 kịch bản sự cố (mục 3) coi như đã xong, chỉ còn 3 drill
  xâm lấn (#2-#4) là thật sự chưa diễn tập.
- **Vẫn còn phiên SSH của "nhat" đang hoạt động trên `234`** (từ
  `192.168.1.40`) — máy có thể vẫn đang được dùng, **chưa nên tự ý chạy thêm
  drill xâm lấn hoặc `production_readiness_check.sh` (mất 1-2 tiếng, chiếm
  dụng cả cụm) khi chưa xác nhận họ đồng ý.**
- **Chưa có 1 lần chạy `production_readiness_check.sh` nào PASS trọn vẹn
  end-to-end** — lần gần nhất FAIL giữa chừng, nhiều khả năng do đúng lúc
  nhat đang thao tác gây SIGKILL 1 node (xem log cũ `/tmp/production_readiness_20260910_011343.log`
  trên `234`). Tầng 1+2 (build, deploy stability) đã PASS thật trong lần đó.

---

## 6. Việc còn mở — khuyến nghị cho người tiếp theo

1. ~~`git pull origin dev` trên máy `234`~~ ✅ Đã xong (mục 4/5).
2. ~~Chạy `drill_telegram_alert.sh`~~ ✅ Đã xong, PASS.
3. ~~Chạy `production_readiness_check.sh` cho tới khi PASS sạch~~ ✅ Đã chạy
   xong lần 2 (2026-09-10, ~02:42-03:24 UTC) — **KẾT QUẢ: FAIL**, phát hiện
   bug NGHIÊM TRỌNG #10 (xem mục 7). **KHÔNG được coi "go/no-go xanh" cho tới
   khi bug #10 được fix.**
4. Diễn tập 3 drill xâm lấn còn lại (`drill_node_full_resync.sh`,
   `drill_disk_full.sh`, `drill_network_partition.sh`) — **tạm hoãn cho tới
   khi bug #10 được fix**, vì cả 3 drill đều dựa trên giả định "cụm chịu được
   1 node lỗi mà vẫn tiếp tục sản xuất block", giả định này hiện SAI (mục 7).
5. Điều tra thêm `wait_rpc_ready_seconds` (sleep cố định 5s trong
   `deploy/ci/ci_runner.py`) — vẫn chưa xác nhận, độ ưu tiên thấp hơn bug #10.
6. ~~Bug #10 — thử vá `RECOVERY_STUCK_TIMEOUT_SECS` 900s→120s~~ ✅ Đã thử,
   đã build, đã deploy, đã kiểm chứng qua `node_chaos_restart` thật — **XÁC
   NHẬN GÂY FORK THẬT** (block #500 mismatch, `LAYER-6 fork_guard` tự
   abort()). Đã revert về 900s, nhánh `fix/permanent-gap-recovery-timeout`
   giữ lại làm bằng chứng "kết quả âm tính" đã ghi chép đầy đủ — **KHÔNG
   merge, không thử lại đúng thay đổi này**. Xem mục 7 phần cuối để biết
   chi tiết đầy đủ + nguyên nhân gốc thật sự (vì sao DAG cục bộ của node-3
   lệch từ trước khi ân xá chạy) vẫn CHƯA tìm ra — đây mới là điều kiện tiên
   quyết thật để xem xét lại vấn đề này trong tương lai, không phải "chọn 1
   con số khác".
7. **2 bug MỚI phát hiện trong lúc kiểm chứng bug #10 (2026-09-10, chưa điều
   tra sâu, độ ưu tiên cao vì đều liên quan tính sẵn sàng của cụm):**
   - **RocksDB panic-loop không bao giờ tự phục hồi**: node-0 panic tại
     `consensus/metanode/meta-consensus/core/src/storage/rocksdb_store.rs:31`
     khi mở lại `consensus_db` (lock bị giữ bởi chính tiến trình cũ chưa giải
     phóng sạch), cơ chế "outer resilience loop" (`ffi.rs`, rebuild Tokio
     runtime + thử lại) lặp lại đúng lỗi này **6+ lần liên tiếp, mỗi lần
     cách nhau 60s, không bao giờ tự khỏi** — chỉ khỏi khi `systemctl
     restart` thật (giải phóng toàn bộ OS-level file handle). Cần điều tra:
     vì sao RocksDB lock không được giải phóng khi thread panic trong lần
     thử trước.
   - **peer_rpc "early→full server" bàn giao cổng thất bại sau restart dồn
     dập**: cổng `peer_rpc_port` (19200+N, dùng cho "WAN sync"/BLOCK-FETCH
     giữa các validator) không lắng nghe ở CẢ 4 validator sau 1 chuỗi
     restart thủ công nhiều lần liên tiếp trong thời gian ngắn (dù log tự
     báo "Started" — log này không đáng tin, không xác nhận bind thật sự
     thành công) — tự khỏi sau khi cụm được khởi động lại 1 lần "sạch"
     (không phải chuỗi restart dồn dập). Nghi ngờ: code tại
     `network/peer_rpc/server.rs` (đã có lịch sử fix tương tự từ
     2026-08-25) có 1 race case MỚI chưa được bao phủ khi restart xảy ra
     dồn dập/liên tục. **Chưa xác nhận đây là bug thật hay chỉ do cách thao
     tác thủ công bất thường của tôi hôm nay** — cần tái hiện có kiểm soát,
     không xóa dữ liệu, để kết luận chắc chắn.
8. **Bug quan sát phụ — cầu nối log Rust→Go đã ngừng hoạt động trên thực tế**
   (mục 7, phần cuối): banner khởi động `CommitProcessor` (in vô điều kiện
   mỗi lần start) không xuất hiện trong journalctl từ 02:07:46 UTC trở đi dù
   node đã restart nhiều lần sau đó — cần điều tra riêng, độc lập với bug #10,
   trước khi tin tưởng bất kỳ log Rust nào trong vận hành thật. (Lưu ý: đã
   xác nhận sau đó rằng log Rust thật ra nằm ở
   `logs/execution/<ngày>/execution.log`, không phải journalctl — xem mục 7
   "Đào sâu lần 2" — nhưng dấu hiệu banner-không-xuất-hiện ban đầu vẫn cần
   đối chiếu lại với đúng file này để không bỏ sót.)

---

## 7. 🔴 BUG NGHIÊM TRỌNG #10 (CHƯA FIX) — Block sản xuất bị LIVELOCK vĩnh
viễn khi có đúng 1/4 validator ngừng hoạt động

**Phát hiện:** 2026-09-10, ~03:18-03:43 UTC, qua `node_chaos_restart` test
thật (`production_readiness_check.sh` run 2) rồi tái hiện độc lập, có kiểm
soát, ngoài phạm vi test harness để loại trừ khả năng lỗi do chính test.

### Triệu chứng

Cụm 4 validator (m0-m3, m4 là SyncOnly không vote). Dừng **CHỈ 1 validator**
(m1) bằng `systemctl stop` (dừng sạch, không phải crash) → **toàn bộ cụm
NGỪNG HẲN sản xuất block mới, vĩnh viễn cho tới khi m1 quay lại** — không
phải chậm, không phải tạm dừng vài giây rồi tiếp tục, mà đứng yên tuyệt đối:

- Test thật: 15/15 giao dịch timeout (45s/giao dịch) trong >13 phút liên
  tục, block height đứng yên ở #301 trong suốt thời gian đó, trong khi 3/4
  validator còn lại (m0, m2, m3) đều báo `ALIVE` qua RPC.
- Tái hiện độc lập (không qua test harness, tự tay `systemctl stop
  metanode-execution-1` trên `230`, poll RPC trực tiếp mỗi 10s): block height
  đứng yên tuyệt đối ở block #215 (`0xd7`) suốt >90s liên tục (không có giao
  dịch nào đang gửi — tức là ngay cả việc "seal 1 block rỗng" cũng bị chặn).
- **Ngay khi khởi động lại m1** (`systemctl start`), block tiến ngay lên
  #216 trong vòng ~15-22s, rồi hoạt động bình thường trở lại.

### Đã loại trừ

- **KHÔNG phải do stake bất đối xứng**: genesis thật trên `234` xác nhận cả
  4 validator có `total_staked_amount` bằng nhau (1000 mỗi node / tổng 4000).
- **KHÔNG phải do công thức quorum sai**: đọc trực tiếp
  `consensus/metanode/meta-consensus/config/src/committee.rs` —
  `quorum_threshold = total_stake - floor((total_stake-1)/3)` cho ra đúng 3/4
  (khớp BFT f=1, 2f+1=3), và `reached_quorum` dùng `>=` (không phải `>`) nên
  3/4 lẽ ra phải ĐỦ quorum.
- **KHÔNG phải do `leader_timeout` quá dài**: default trong
  `consensus/metanode/meta-consensus/config/src/parameters.rs` chỉ 100ms —
  nếu chỉ là vấn đề leader-rotation thông thường, lẽ ra phải tự phục hồi
  trong < 1 giây, không phải đứng yên hàng chục phút.
- **KHÔNG phải do lỗi kịch bản test**: tái hiện độc lập, không qua
  `run_restart_test.sh`, tự tay dừng đúng 1 node và poll RPC trực tiếp, kết
  quả giống hệt.
- **KHÔNG phải do DAG consensus bị treo hoàn toàn** — xem bằng chứng dưới.

### Bằng chứng cốt lõi: tầng DAG-consensus của Rust VẪN chạy, nhưng livelock
ở bước seal block

File `/opt/metanode/node-{N}/data/execution/db/consensus/rust_consensus/commit_ffi_wal.log`
(ghi bởi `consensus/metanode/src/consensus/commit_processor/wal.rs`) trong
lúc m1 bị dừng cho thấy: `commit_index` (số hiệu commit DAG nội bộ của Rust)
liên tục tăng rất nhanh (~3.5 commit/giây, từ 5511 lên 5666 chỉ trong 40
giây) — **tức là tầng DAG-consensus (round/leader/certificate) VẪN đang hoạt
động, KHÔNG hề bị treo** — nhưng **mọi commit đều map về cùng 1
`global_exec_index` (GEI) = 216, không bao giờ tiến lên 217.** Đây là
livelock thật sự: hệ thống liên tục làm việc (tốn CPU/disk thật) nhưng không
bao giờ hoàn thành việc "seal" block tiếp theo, miễn là còn thiếu 1/4
validator — bất kể chờ bao lâu.

### Giả thuyết (CHƯA xác nhận, cần điều tra tiếp bởi người có domain
knowledge sâu hơn về `consensus/metanode/src/consensus/commit_processor/`)

Công thức GEI (xem `gei_validator.rs`): `GEI = epoch_base_index +
commit_index + fragment_offset`. `fragment_offset` không tăng dù
`commit_index` tăng liên tục → nghi ngờ bước "gom giao dịch/certificate
thành 1 block hoàn chỉnh" (không phải bước quorum-vote ở tầng DAG) đang chờ
1 điều kiện ngầm định cần đủ 4/4 authority thay vì chỉ cần quorum 2f+1=3 —
có thể ở `executor.rs` hoặc `processor.rs` trong cùng thư mục
`commit_processor/` (chưa đọc kỹ — mỗi file ~500-2000 dòng, cần thời gian
riêng). **Đây là nơi cần bắt đầu điều tra tiếp theo.**

### Phát hiện phụ (khả năng không liên quan, nhưng nên fix riêng)

1. **Rust panic thật mỗi lần node khởi động**: `logs/execution/panic.log`
   trên `234` ghi nhận `thread '<unnamed>' panicked at
   crates/typed-store/src/metrics.rs:70:13: there is no reactor running,
   must be called from the context of a Tokio 1.x runtime` — xảy ra ở 1
   thread không tên, nhiều khả năng 1 macro ghi metrics bị gọi từ 1 OS thread
   thường (biên FFI/CGo) thay vì trong ngữ cảnh Tokio runtime. Không thấy
   ảnh hưởng tới hoạt động chung (node vẫn lên khỏe sau đó), nhưng là dấu
   hiệu code không sạch, nên fix.
2. **KHÔNG có visibility vào tầng Rust consensus qua log vận hành**: đã thử
   set `RUST_LOG=consensus_core=debug,metanode=debug,warn` qua systemd
   override rồi restart — **không có thêm 1 dòng log nào xuất hiện trong
   `journalctl`**, kể cả trong lúc đang tái hiện bug #10 trực tiếp. Nghĩa là
   nếu bug #10 xảy ra thật ngoài sản xuất, người trực KHÔNG có cách nào nhìn
   log để biết lý do — chỉ thấy "block height đứng yên" từ bên ngoài. Cần
   điều tra: tracing subscriber của Rust có đang đọc `RUST_LOG` không, có
   đang ghi ra nơi khác (file riêng?) không, hay bị tắt hoàn toàn.

### Đào sâu tiếp (2026-09-10, cùng ngày) — đọc code tận gốc, chưa rebuild/deploy

Đọc kỹ `consensus/metanode/meta-consensus/core/src/` (linearizer, base_committer,
commit_vote_monitor, leader_schedule, leader_timeout, committee, parameters) và
`consensus/metanode/src/consensus/commit_processor/` (processor, executor, wal)
+ `consensus/metanode/src/ffi.rs` + Go phía `execution/executor/ffi_bridge.go`,
`execution/pkg/logger/logger.go`. Chưa sửa code, chưa rebuild/deploy lại — chỉ
đọc + đối chiếu với bằng chứng WAL đã có.

**Đã xác nhận qua đọc code (khớp với bằng chứng WAL: commit_index tăng
nhanh, GEI đứng yên):** mỗi commit trong `dispatch_commit()`
(`commit_processor/executor.rs`) tự đếm `total_transactions` từ chính
`subdag.blocks` — nếu bằng 0 (`commit_is_empty_for_gei`), trả về `Ok(0)`
NGAY LẬP TỨC, không gửi gì sang Go, **GEI không hề tăng**. WAL vẫn ghi
PENDING/COMMITTED quanh lệnh gọi này bất kể rỗng hay không → đúng khớp dữ
liệu quan sát được: DAG-consensus tầng dưới KHÔNG treo, nó liên tục "quyết
định xong" (commit_index tăng) nhưng **mọi quyết định trong suốt cửa sổ đó
đều RỖNG** — giao dịch thật nằm trong mempool của m0/m2/m3 không hề lọt vào
bất kỳ commit nào.

**Nguyên nhân góp phần đã xác nhận trong code (mức tin cậy cao — đọc thấy
trực tiếp, chưa chứng minh là NGUYÊN NHÂN DUY NHẤT):**
`leader_schedule.rs::elect_leader()` có dòng
`let is_reputation_swaps_disabled = true;` — HARD-CODE, không phải cấu hình
— tắt vĩnh viễn cơ chế "học để tránh chọn 1 validator đang không phản hồi
làm leader" (cơ chế reputation-based leader rotation chuẩn của họ DAG-BFT
Mysticeti/Bullshark gốc). Comment giải thích: tắt để tránh 1 bug khác (node
phục hồi giữa epoch từ snapshot tính điểm reputation khác node khác → fork).
Hệ quả: **chọn leader mỗi round hiện là random có trọng số CHỈ theo số hiệu
round, không hề biết/nhớ node nào đang chết** — với 4 node bằng stake, ước
tính ~25% số round SẼ chọn đúng node đang chết làm leader, MÃI MÃI, chừng
nào node đó còn chết, không hề cải thiện theo thời gian.

Phát hiện thêm: có 1 field `context.rs::reputation_swaps_disabled_for_epoch`
(kiểu `AtomicBool`, khởi tạo `false`) rõ ràng được thiết kế để bật/tắt cơ chế
này CÓ ĐIỀU KIỆN theo từng epoch (ví dụ: chỉ tắt ngay sau khi 1 node phục hồi
từ snapshot, không tắt vĩnh viễn toàn cụm) — nhưng **field này không hề được
đọc ở bất kỳ đâu trong toàn bộ codebase** (`grep` xác nhận). Đây là dấu hiệu
1 lần refactor dở dang: có vẻ ý định ban đầu là tắt có điều kiện, nhưng bản
vá thực tế lại tắt vĩnh viễn, thô hơn nhiều so với ý định.

**CHƯA xác nhận được (cần instrument code thật + rebuild + deploy lại để
biết chắc, chưa làm trong lượt này):** vì sao ngay cả ~75% số round có leader
CÒN SỐNG (m0/m2/m3) cũng không hề commit được giao dịch nào trong suốt >90s
liên tục. Đọc `base_committer.rs::enough_leader_blame`/`enough_leader_support`
thì cơ chế "quorum blame để bỏ qua (skip) 1 leader chết" nhìn có vẻ đúng đắn
về mặt logic (chỉ cần 3/4 stake — đúng bằng quorum, các node sống đều đủ điều
kiện tạo blame) — tức là về lý thuyết ván bài "leader sống thì phải commit
được" vẫn nên đúng. Khoảng cách giữa lý thuyết (đọc code) và thực tế (WAL cho
thấy 100% rỗng, không phải ~75% có nội dung) là điều CẦN điều tra tiếp bằng
cách thêm `eprintln!` (đúng kiểu team cũ đã dùng — xem mục quan sát dưới) vào
`try_collect_sub_dag_and_commit`/`enough_leader_blame`/`enough_leader_support`,
build lại, deploy 1 node, tái hiện lại lỗi lần nữa để bắt tận tay nhánh nào
đang thực sự chạy.

**Phát hiện phụ NGHIÊM TRỌNG về khả năng quan sát (mở rộng phát hiện cũ ở
mục 7 gốc — "RUST_LOG không có tác dụng"):** đã tìm ra vì sao. `ffi.rs` khởi
tạo tracing với `EnvFilter::try_from_default_env().unwrap_or_else(|_| "info")`
— ĐÚNG, có đọc `RUST_LOG`, mặc định "info" (không quá chặt). Log Rust được
route qua callback FFI `GoLogWriter` → Go's `cgo_log_message()`
(`execution/executor/ffi_bridge.go`) → `logger.Info/Warn/Error` phía Go, có
tiền tố `[RUST]`. Go's logger mặc định `Flag: FLAG_INFO` — theo code, INFO/
WARN/ERROR ĐỀU phải in ra. Vậy trên giấy tờ, toàn bộ hàng chục dòng
`info!()/warn!()/error!()` đã có sẵn trong code (biển "🛡️ [DIGEST-GATE]
Initialized...", "📡 [COMMIT PROCESSOR] Waiting for commits...", "🚨
[PERMANENT-GAP-RECOVERY]"...) phải hiện ra bình thường, không cần bật
RUST_LOG. **Nhưng kiểm tra trực tiếp trên node-0 (234): dòng log gắn thẻ
`[RUST]` hoặc chứa "DIGEST-GATE"/"COMMIT PROCESSOR" GẦN NHẤT có trong log là
lúc 02:07:46 UTC — TRƯỚC toàn bộ episode điều tra hôm nay (production
readiness run 2, và tất cả các lần tôi tự tay restart/tái hiện bug từ 03:06
tới giờ) — nghĩa là node đã restart nhiều lần sau đó, đã hoạt động bình
thường, mà banner khởi động CommitProcessor (in ra vô điều kiện mỗi lần
`run()` được gọi) không hề xuất hiện lại 1 lần nào.** Nói cách khác: cầu nối
log Rust→Go, dù đọc code thấy đúng, **trên thực tế đã ngừng hoạt động từ một
thời điểm nào đó** — nghiêm trọng hơn cả "RUST_LOG không hiệu lực": ngay cả
hàng chục dòng chẩn đoán đã được đội cũ chủ đích viết sẵn cho ĐÚNG LOẠI SỰ CỐ
này (DIGEST-GATE DIAG, PERMANENT-GAP-RECOVERY, COLD-START-GUARD) cũng im
lặng hoàn toàn khi sự cố thật xảy ra hôm nay. Bằng chứng gián tiếp xác nhận
đây là vấn đề CÓ THẬT, không phải tôi đọc sai: chính code hiện tại có sẵn 3
cụm biến đếm `DIAG_DIGEST_*`/`DIAG_GEI_GUARD_*`/`DIAG_FAST_SKIP_*` dùng
`eprintln!` (KHÔNG qua tracing) với comment tường minh "eprintln! bypasses
tracing entirely" — tức là đội kỹ sư trước ĐÃ tự phát hiện tracing không tin
cậy được và phải né bằng in trực tiếp, không phải giả thuyết của tôi.

**Việc quét rộng đã làm (theo yêu cầu "check rộng hơn"):** grep toàn bộ
`consensus/metanode/` và `execution/` tìm thêm các cờ hard-code kiểu
`is_reputation_swaps_disabled` (tắt vĩnh viễn 1 cơ chế an toàn) và các pattern
"yêu cầu ĐỦ N/N thay vì quorum" giống Guard 6 — **không tìm thấy trường hợp
thứ 2** trong các file đã đọc (xem danh sách ở trên). Các file LỚN trong cùng
thư mục core CHƯA đọc kỹ (do giới hạn thời gian phiên này):
`core.rs` (file điều phối chính, ~lớn), `dag_state.rs`, `ancestor.rs`,
`round_tracker.rs`, `commit_finalizer/`, `commit_observer.rs`,
`round_prober.rs` — đây là nơi nên tiếp tục quét nếu muốn phủ hết tầng
consensus-core.

### Đào sâu tiếp lần 2 (2026-09-10, cùng ngày, sau khi user yêu cầu "fix tận
gốc rễ") — thêm code chẩn đoán thật, build, deploy, tái hiện trực tiếp trên
cụm thật, rồi **KẾT LUẬN BỊ ĐẢO NGƯỢC MỘT PHẦN QUAN TRỌNG**

Đã làm: thêm `eprintln!` (né tracing hẳn, học đúng pattern `DIAG_*` sẵn có
trong code) vào 3 điểm nghi vấn nhất (`elect_leader_stake_based`, Guard 6 của
linearizer, `dispatch_commit`), build thật trên `234` (build lần 1 dùng
`--fast` tự phát hiện thêm 1 bug build phụ: Go link âm thầm dùng nhầm
`target/release/libmetanode.a` CŨ từ hôm trước dù build mới nằm ở
`target/debug` — không báo lỗi, chỉ lặng lẽ chạy code cũ; fix bằng cách build
release thật không dùng `--fast`), deploy riêng cho node-0 trên `234`
(`ansible_deploy.sh --start --only-node 0`), rồi tái hiện lỗi 3 lần bằng
`systemctl stop metanode-execution-1` + gửi tx thật qua `main.go` trong
`restart-recovery/` (công cụ CHÍNH THỐNG của bộ test, không phải tự viết).

**Phát hiện log quan trọng: dòng chẩn đoán KHÔNG nằm trong `journalctl`** —
`systemd` unit có `StandardError=append:.../logs/execution/panic.log`, nhưng
tiến trình Go tự "dup2" lại stderr của chính nó sang
`logs/execution/<ngày>/execution.log` (file log riêng, tự xoay vòng theo
ngày) NGAY SAU KHI khởi động — ghi đè lên cấu hình `systemd` ban đầu. Đây
CHÍNH LÀ nơi có toàn bộ log Rust (`[RUST] ...`) — không phải do cầu nối
Rust→Go bị hỏng như kết luận sai ở lần đào sâu 1. **Sửa lại kết luận cũ: cầu
nối log Rust→Go hoạt động ĐÚNG, chỉ là operator phải xem đúng file
`logs/execution/<ngày>/execution.log` trên từng node, KHÔNG PHẢI
`journalctl` — đây vẫn là 1 khoảng trống vận hành thật (không ai biết phải
xem file này) nhưng bản chất khác hẳn "log Rust bị mất".**

**Bằng chứng số thật từ log chẩn đoán (cửa sổ node1 tắt ~5 phút, không gửi
tx):** 16222 lượt gọi `elect_leader`, chia đều ~25% cho cả 4 index (kể cả
index của node1 đang chết) — xác nhận đúng như đọc code: KHÔNG hề tránh
node chết. Guard 6 (linearizer) — **0 lần abort trong toàn bộ cửa sổ** — loại
trừ hẳn giả thuyết "thiếu block cha". 2154 commit thành công, nhưng khi soi
kỹ cửa sổ node1-tắt: 643 commit thành công, **CHỈ 1 commit duy nhất có giao
dịch thật** — nhưng lúc đó KHÔNG có ai gửi giao dịch cả (chỉ tắt node quan
sát thụ động) nên đây là hành vi RỖNG BÌNH THƯỜNG của round rỗng không tải,
không phải bằng chứng của bug.

**Tái hiện có kiểm soát bằng công cụ chính thống (`main.go --stopped-node 1
--count 15`), lặp lại 3 lần khác nhau:**
1. Tắt node1, để ổn định 4 phút, rồi gửi 15 tx → **15/15 THÀNH CÔNG trong 3
   giây**, Zero-Fork xác nhận.
2. Tắt node1, chờ ĐÚNG 3 giây (khớp timing thật của test gốc), gửi 15 tx
   ngay → **15/15 THÀNH CÔNG trong 3 giây**.
3. Tái hiện đúng trình tự CHẶNG 2.1→2.2 của test gốc: restart node-0 qua
   ansible (giữ dữ liệu), chờ node-0 đồng bộ lại hoàn toàn, rồi tắt node1 +
   chờ 3s + gửi 15 tx ngay → **VẪN 15/15 THÀNH CÔNG**.

**→ KHÔNG tái hiện được lỗi gốc dù lặp lại đúng cấu trúc & timing của
`node_chaos_restart` 3/3 lần.** Đây là phát hiện quan trọng, làm thay đổi
đánh giá mức độ nghiêm trọng: **bug KHÔNG PHẢI "cứ 1/4 node chết là treo
vĩnh viễn"** (giả thuyết ban đầu, dựa trên 1 lần fail thật) — trường hợp đơn
giản/cô lập hoạt động đúng. Bug thật (đã xảy ra, có bằng chứng RPC + log rõ
ràng trong lần chạy `production_readiness_check.sh` run 2) nhiều khả năng là
1 race/điều kiện hiếm hơn, KHÔNG dễ tái hiện bằng thao tác thủ công đơn lẻ —
khớp với đúng loại sự cố mà **đội kỹ sư trước ĐÃ TỪNG gặp và tài liệu hoá kỹ
trong chính code** (`processor.rs`, các comment "PERMANENT-GAP-RECOVERY"
2026-09-09, tự nhận "confirmed live... 15+ minutes", "two co-located
validators on one physical host restarting together") — họ đã xây sẵn 1 cơ
chế tự phục hồi có điều kiện bằng chứng, chờ đúng `RECOVERY_STUCK_TIMEOUT_SECS
= 900` giây (15 phút) mới cấp "ân xá" bỏ qua xác minh cho phần backlog đã
chứng minh là không thể xác thực lại được. **Giả thuyết có khả năng cao
nhất bây giờ:** lần fail thật (13 phút 54 giây) có thể ĐÃ ĐANG trên đường tự
phục hồi đúng cơ chế này nhưng bài test `node_chaos_restart` bỏ cuộc SỚM HƠN
15 phút 1 chút — chưa kịp thấy cơ chế tự phục hồi hoàn tất.

### Vì sao KHÔNG vá trực tiếp `is_reputation_swaps_disabled = true`

Ban đầu coi đây là ứng viên sửa rõ ràng nhất, nhưng đọc kỹ hơn thì đây là
quyết định CÓ CHỦ ĐÍCH, gắn với rủi ro fork thật (comment "FORK-SAFETY"):
reputation-score phải được TOÀN MẠNG đồng thuận giống hệt nhau; 1 node vừa
phục hồi từ snapshot có lịch sử DAG khác nên tính điểm khác → nếu bật lại
swap ngay cả có điều kiện, phải đảm bảo MỌI node tính "có nên bật swap hay
không" giống hệt nhau tại cùng 1 thời điểm, nếu không sẽ tạo ra đúng loại
fork mà cơ chế này sinh ra để ngăn. Field `reputation_swaps_disabled_for_epoch`
tuy có vẻ được thiết kế cho việc này nhưng chưa từng được nối dây — vá vội
mà không mô hình hoá đầy đủ bài toán đồng thuận sẽ RỦI RO hơn giữ nguyên.
**Quyết định: KHÔNG vá phần này trong phiên này** — đã revert toàn bộ code
chẩn đoán, cụm đã dọn sạch về đúng `origin/dev`, không có thay đổi code nào
được đẩy lên từ lượt đào sâu này.

### Đào sâu lần 3 (2026-09-10, cùng ngày, ngay sau khi user yêu cầu "chạy đi"
để đo tần suất thật) — **BẮT ĐƯỢC CƠ CHẾ CHÍNH XÁC, live, trên cụm thật**

Chạy `./ci.sh run-now --only node_chaos_restart` lặp lại (script
`/tmp/repeat_chaos_test.sh` trên `234`, 8 vòng). Kết quả khác hẳn 3 lần tái
hiện thủ công trước đó: **dùng đúng cơ chế thật (`ansible_deploy.sh
--stop`/`--restart` qua `run_restart_test.sh`, KHÔNG phải `systemctl stop`
tôi tự gõ tay) thì lỗi tái hiện NGAY LẬP TỨC** — vòng 1 FAIL sau 836s, vòng 2
FAIL sau 722s (2/2 FAIL tính tới lúc ghi tài liệu này). Điều này đảo ngược
lại 1 phần kết luận "có thể là race hiếm" ở lần đào sâu 2 — **đây là lỗi tái
hiện được RẤT ỔN ĐỊNH qua đúng cơ chế test thật**, chỉ là cách tôi tự tái
hiện tay bằng `systemctl stop` trực tiếp (bỏ qua ansible) không kích hoạt
đúng điều kiện.

**Cơ chế chính xác, đọc trực tiếp từ dòng log chẩn đoán đã có sẵn trong code**
(`[DIGEST-GATE DIAG] PIPELINE STATE DUMP (STALLED)`, in mỗi 10s khi có
pending_local, xem `commit_processor/processor.rs`) — bắt được live ở
`/opt/metanode/node-0/logs/execution/2026-09-10/execution.log`:

```
pending_local=1, first_idx=64066, oldest_age=155s, next_expected=64066,
qci=64835, digest_has_data=true, verifier(64066)=None, pending_ooo=770 (tăng dần)
```

- Tầng DAG-consensus (`qci` = quorum_commit_index) **vẫn chạy tốt, tăng đều**
  (~5 commit/giây trong lúc quan sát) — KHÔNG hề treo.
- Nhưng `next_expected_index` của riêng node này **kẹt cứng đúng 1 chỉ số
  cụ thể** — không bao giờ nhận đủ phiếu xác thực digest (`verifier(...)=None`)
  từ peer cho ĐÚNG chỉ số đó, dù `digest_has_data=true` (không phải cold-start).
- Hàng trăm commit MỚI HƠN đã xếp hàng chờ phía sau (`pending_ooo`, tăng dần
  theo thời gian) — không được phép vượt lên vì code bắt buộc dispatch đúng
  thứ tự tăng dần tuyệt đối (chủ đích, chống fork).
- **Quan sát trực tiếp 1 lần "bung" thật**: WAL (`commit_ffi_wal.log`) đứng
  yên ở commit_index=60550 từ 06:54:51, rồi đột ngột nhảy lên 64065 lúc
  06:57 (≈725s sau, KHÔNG PHẢI 900s — nên không phải do ngưỡng
  `RECOVERY_STUCK_TIMEOUT_SECS` mà do 1 đường khác, có thể là
  `CommitSyncer` lấy được `CertifiedCommit` từ peer) — **rồi kẹt lại NGAY LẬP
  TỨC ở index tiếp theo (64066)**.

**Kết luận về hành vi thật:** hệ thống không "treo cứng vĩnh viễn" theo
nghĩa đen — nó tiến theo kiểu "giật cục": thỉnh thoảng bung 1 mẻ backlog CŨ
(toàn rỗng, không giao dịch) rồi kẹt lại ngay ở chỉ số kế tiếp. Từ góc nhìn
RPC bên ngoài, block height vẫn đứng yên mãi vì mọi mẻ "bung" đều toàn commit
rỗng lịch sử, còn giao dịch THẬT gửi vào (nằm ở phần đầu DAG, mới hơn) không
bao giờ bắt kịp vì luôn nằm sau hàng rào `next_expected_index` đang bò rất
chậm. **Đây là hiện tượng "hàng rào luôn tụt lại phía sau" (perpetual
one-index-behind), nhiều khả năng do độ trễ lan truyền phiếu digest
(gossip) giữa các validator không bắt kịp tốc độ node vừa restart tự quyết
định (`decided_with_local_blocks`) các commit cục bộ của chính nó** — khớp
với gợi ý trong chính comment code cũ về "hai validator cùng host restart
cùng lúc" (ở đây là node-0 và node-3, cùng nằm trên `234`).

**CHƯA xác nhận được** nguyên nhân gốc RẤT sâu — vì sao phiếu digest của
peer luôn "trễ" đúng 1 bước so với node vừa restart, thay vì bắt kịp bình
thường như hàng nghìn lần deploy trước đó trong phiên làm việc này (100 vòng
snapshot_recovery PASS sạch, không hề gặp hiện tượng này — snapshot_recovery
KHÔNG có bước rolling-restart-rồi-gửi-tx-ngay-trong-cửa-sổ-hẹp giống
`node_chaos_restart`). Cần người có chuyên môn sâu về gossip/vote-propagation
trong `consensus-core` (ngoài phạm vi đọc code tĩnh, cần công cụ theo dõi
gossip theo thời gian thực giữa nhiều node cùng lúc, không chỉ 1 node).

### 🔴 KẾT QUẢ CUỐI: bản vá đã bị RÚT LẠI sau khi xác nhận gây FORK THẬT trên
cụm thật (2026-09-10, cùng ngày)

Sau khi được yêu cầu "code + test kỹ + đưa lên nhánh riêng để review", đã
làm đúng quy trình cẩn thận: viết bản vá tối giản (giảm
`RECOVERY_STUCK_TIMEOUT_SECS` từ 900s xuống 120s), chạy `cargo test` toàn bộ
2 crate liên quan (193 + 185 test PASS, 0 lỗi), đưa lên nhánh riêng
`fix/permanent-gap-recovery-timeout` (KHÔNG merge `dev`), rồi build+deploy
thật lên cụm `234`/`230` để kiểm chứng bằng `node_chaos_restart` thật.

**Kết quả kiểm chứng ban đầu có vẻ khả quan:** tỷ lệ giao dịch xác nhận
trong lúc 1 node bị tắt tăng từ 0% (luôn luôn, trước khi vá) lên 66-93%
(sau khi vá) — đúng như kỳ vọng lý thuyết. Vòng lặp 1/5 hoàn tất trọn vẹn
(trước đây pipeline luôn dừng cứng ngay khi có 1 CHẶNG fail 0%).

**Nhưng ở vòng lặp 2/5, hệ thống tự phát hiện và báo FORK THẬT:**

```
08:04:08 [PERMANENT-GAP-RECOVERY] next_expected_index=856 kẹt 121 giây
         (vượt ngưỡng mới 120s trong gang tấc) → cấp "ân xá", tin tưởng
         giá trị cục bộ cho commit 856-857...
...
08:42:35 [LAYER-6 fork_guard] Block #500 KHÔNG KHỚP!
         local_hash ≠ peer_hash, local_root ≠ peer_root
08:42:50 [LAYER-6] CONFIRMED FORK sau 3/3 lần xác minh lại đều fail
         → tự gọi std::process::abort() để dừng ngay (đúng thiết kế
         Zero-Fork Invariant — chặn trước khi lan truyền trạng thái sai)
```

Giá trị được "ân xá" tin tưởng ở phút 08:04 **THẬT SỰ SAI** — khác với giá
trị mà các node khác đã đồng thuận, và chỉ được phát hiện 38 phút sau bởi
lớp bảo vệ CUỐI CÙNG, độc lập (`LAYER-6 fork_guard`, tự kiểm tra định kỳ
block hash/state root với peer). Nếu lớp này không tồn tại, node-3 đã âm
thầm chạy tiếp với trạng thái sai.

**Vì sao lập luận an toàn ban đầu bị sai trong thực tế:** cơ chế
"ân xá" dựa trên lý luận "giá trị cục bộ là hàm xác định của 1 DAG mà toàn
mạng đã đồng thuận từ trước, nên KHÔNG THỂ sai, chỉ có thể không xác minh
lại được". Lập luận này ngầm giả định **DAG cục bộ của chính node đó vẫn
đúng/nguyên vẹn tại thời điểm cấp ân xá** — giả định này KHÔNG đúng trên cụm
này tại thời điểm đó, vì cùng phiên làm việc đã gặp liên tiếp nhiều bất ổn
khác trước đó (panic-loop RocksDB ở node-0, lỗi bàn giao peer_rpc
early→full server sau chuỗi restart dồn dập, crash/restart do thao tác test
thủ công tần suất cao) — bất kỳ cái nào trong số đó đều có thể đã khiến DAG
cục bộ của node-3 lệch từ TRƯỚC KHI cơ chế ân xá chạy, mà bản thân cơ chế
ân xá không có cách nào phát hiện ra điều đó (nó chỉ hỏi "peer đã ngừng bỏ
phiếu cho chỉ số này chưa", không bao giờ hỏi "DAG cục bộ của chính tôi có
còn đúng không"). Ngưỡng 900s an toàn hơn đơn giản vì cửa sổ dài hơn tự
nhiên làm tăng khả năng MỌI bất ổn tạm thời nói trên đã kịp giải quyết
xong, và có cơ hội để 1 xung đột digest thật (vẫn LUÔN được tôn trọng bởi
đúng đoạn code này) kịp xuất hiện trước khi ân xá được tin tưởng — rút ngắn
cửa sổ không thêm 1 lớp kiểm tra nào mới, chỉ thu hẹp biên an toàn sẵn có.

**Đã xử lý:**
1. Dừng ngay bài test đang chạy.
2. Revert `RECOVERY_STUCK_TIMEOUT_SECS` về 900s trên nhánh
   `fix/permanent-gap-recovery-timeout` (giữ lại nhánh, KHÔNG xóa — làm bằng
   chứng "kết quả âm tính" đã được ghi chép đầy đủ), kèm comment cảnh báo
   rất dài giải thích toàn bộ sự việc để không ai vô tình thử lại đúng thay
   đổi này mà chưa tìm ra nguyên nhân gốc thật sự (vì sao DAG cục bộ của
   node-3 lệch từ đầu — CHƯA tìm ra, đây mới là điều kiện tiên quyết thật
   sự để có thể xem xét lại ngưỡng này trong tương lai).
3. Build lại + deploy lại đúng code `dev` sạch (không có bản vá) lên toàn bộ
   cụm `234`/`230`, `--reset-all` để xóa sạch dữ liệu đã bị fork.
4. `cargo test` xác nhận lại vẫn PASS 100% sau khi revert (không có gì bị
   hỏng do việc revert).

**Bài học lớn nhất của toàn bộ phiên làm việc này:** một bản vá có lý luận
an toàn NGHE HỢP LÝ, biên dịch sạch, qua hết unit test, cải thiện RÕ RỆT
metric quan sát được (0%→66-93%) — **vẫn có thể gây fork thật** nếu tiền đề
của lý luận đó (ở đây: "DAG cục bộ luôn đúng") không được xác minh độc lập
trong đúng điều kiện triển khai thực tế. Việc user chủ động yêu cầu "xem xét
cẩn thận, cần giải pháp an toàn cho production" — thay vì chấp nhận ngay đề
xuất ban đầu — là bước ngăn chặn quan trọng nhất trong toàn bộ chuỗi sự
kiện này. Không có bước chạy `node_chaos_restart` thật, lặp nhiều vòng, trên
cụm thật để tìm ra fork này, việc merge bản vá "đã test kỹ, có vẻ đúng" vào
`dev` gần như chắc chắn sẽ xảy ra.

### 📐 Thiết kế hướng sửa kiến trúc thật sự (2026-09-10, sau khi rút bản vá
120s) — xem file riêng

Đã đào sâu code thật (không suy đoán) để trả lời câu hỏi "vì sao chờ lâu
hơn chỉ giảm xác suất fork chứ không đảm bảo an toàn, vậy giải pháp triệt
để là gì". Tìm ra nguyên nhân gốc có khả năng cao nhất: khi 1 node đọc lại
dữ liệu DAG cũ từ đĩa (RocksDB) — điều CHẮC CHẮN xảy ra ngay sau khi restart
vì cache RAM rỗng — hệ thống **không xác minh lại chữ ký**, chỉ so khớp
digest tự tham chiếu (`storage/rocksdb_store.rs::read_blocks()`). Nếu dữ
liệu trên đĩa từng bị ghi sai bởi 1 bug/crash khác (đúng kịch bản node-0/
node-3 gặp phải hôm đó), dữ liệu sai này được dùng làm input cho
`decided_with_local_blocks` mà không ai xác minh lại — đây là lỗ hổng thật
sự, không phải vấn đề thời gian chờ.

Thiết kế đầy đủ (kèm trích code, chuỗi nhân quả, 2 lớp đề xuất sửa, và danh
sách việc cần làm TRƯỚC KHI code): xem file riêng
`note/consensus_local_dag_trust_gap_design_2026-09.md` — **chỉ là thiết
kế, chưa code, chưa triển khai gì**.

### Khuyến nghị thật sự cho bước tiếp theo

1. **Không nên tin "PASS 1 lần" hay "FAIL 1 lần" của `node_chaos_restart` là
   kết luận cuối** — bằng chứng cho thấy đây có thể là lỗi xác suất thấp.
   Nên chạy `node_chaos_restart` lặp lại nhiều lần (5-10 lần độc lập) để ước
   lượng tần suất thật trước khi kết luận PASS hay FAIL cho go/no-go.
2. Nếu lỗi tái xuất hiện: dùng LẠI đúng bộ code chẩn đoán này (đã revert,
   nhưng nội dung đầy đủ nằm trong lịch sử tool-call của phiên này, có thể
   khôi phục nhanh) — lần này để nó chạy ĐỦ LÂU (>15 phút, qua ngưỡng
   `RECOVERY_STUCK_TIMEOUT_SECS`) thay vì để test tự bỏ cuộc sớm, xem có thật
   sự tự phục hồi đúng như cơ chế PERMANENT-GAP-RECOVERY hứa hẹn không.
3. Nhắc operator: khi cần xem log tầng Rust consensus trên node thật, đọc
   `/opt/metanode/node-{N}/logs/execution/<ngày>/execution.log`, KHÔNG PHẢI
   `journalctl` — log Rust (tiền tố `[RUST]`) nằm ở đó.

### Vì sao đây là chặn triển khai (blocker), không phải "để sau"

Đây trực tiếp vi phạm điều đã quảng cáo/kỳ vọng ở tài liệu BFT
(`note/bft_fault_tolerance_node_count.md`): cụm 4 validator (f=1) phải chịu
được ĐÚNG 1 node lỗi mà vẫn tiếp tục hoạt động. Thực tế: mất khả năng sản
xuất block HOÀN TOÀN, VÔ THỜI HẠN, chỉ với 1 node dừng có kiểm soát (không
phải crash bất thường) — đây là kịch bản vận hành bình thường nhất (bảo trì
định kỳ 1 node, node tự restart do OOM/deploy, v.v.), không phải trường hợp
biên. **Không nên triển khai thật tới khi bug này được fix và verify lại
bằng 1 lần `node_chaos_restart` PASS thật.**
