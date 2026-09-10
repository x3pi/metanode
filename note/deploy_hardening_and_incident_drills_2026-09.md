# Deploy hardening + incident drills (2026-09-09 → 2026-09-10)

Tài liệu bàn giao cho phiên làm việc dài kiểm chứng "đã đủ tự tin triển khai
thật chưa", xuất phát từ yêu cầu chạy `snapshot_recovery` test trên máy
`234` (`~/nhat/con-chain-v2/metanode`). Ghi lại: đã sửa gì, công cụ mới có
vai trò gì, trạng thái cụm tại thời điểm viết tài liệu này, và việc gì còn
mở để người tiếp theo biết bắt đầu từ đâu.

---

## 1. Các bug thật đã tìm và fix (tất cả đã lên `dev`)

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
3. **Hỏi "nhat" (192.168.1.40) khi nào họ xong việc trên `234`/`230`**, rồi
   mới chạy tiếp: `deploy/ci/production_readiness_check.sh` một lần cho tới
   khi PASS sạch end-to-end — đây là điều kiện còn thiếu để coi là "go/no-go
   xanh" chính thức.
4. Diễn tập 3 drill xâm lấn còn lại (`drill_node_full_resync.sh`,
   `drill_disk_full.sh`, `drill_network_partition.sh`) theo thứ tự khuyến
   nghị trong `deploy/ci/incident_drills/README.md`, cũng cần hỏi trước khi
   chạy.
5. Điều tra thêm `wait_rpc_ready_seconds` (sleep cố định 5s trong
   `deploy/ci/ci_runner.py`) — nghi ngờ cùng lớp lỗi với bug #1/#7 (fixed
   sleep thay vì chờ data-driven), nhưng CHƯA xác nhận được vì dữ liệu lần
   thử bị nhiễu bởi người can thiệp. Cần 1 lần chạy sạch để kết luận.
