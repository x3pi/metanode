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

## 4. Trạng thái cụm tại thời điểm viết tài liệu (2026-09-10, ~02:35 UTC)

⚠️ **Đây là snapshot trạng thái tại 1 thời điểm — kiểm tra lại trước khi dựa
vào, đừng coi là luôn đúng.**

- **Cụm 5 node** (`m0`/`m3` trên `192.168.1.234`, `m1`/`m2`/`m4` trên
  `192.168.1.230`): cả 5 `systemctl is-active` đều `active`, healthy.
- **Health monitor + block_hash_checker**: đang chạy bình thường trên `234`.
- **Không có iptables rule nào của drill còn sót lại** trên `230` (đã kiểm
  tra `iptables -L -n | grep DRILL` — sạch).
- **⚠️ QUAN TRỌNG: máy `234` (`~/nhat/con-chain-v2/metanode`) đang ở commit
  `45bcbda6` trên `dev` — CHẬM HƠN `dev` thật 3 commit** (`3a8727ce`
  pgrep-self-match fix, `d2dc12d0` summary-parse fix, `40ac5139` incident
  drills). Cố ý chưa `git pull` để tránh làm nhiễu lần chạy
  `production_readiness_check.sh` đang thực hiện lúc đó. **Người tiếp theo
  cần `cd ~/nhat/con-chain-v2/metanode && git pull origin dev` trước khi
  tin tưởng fix #7 đã có hiệu lực trên máy này**, hoặc trước khi dùng các
  drill script (chúng chỉ tồn tại từ commit `40ac5139` trở đi).
- **Có 1 phiên SSH khác đăng nhập trên `234`** từ `192.168.1.40` (không phải
  máy điều khiển `192.168.1.232`), đăng nhập từ 2026-09-09 12:17, còn hoạt
  động tại thời điểm viết tài liệu. Nghi ngờ đây là người đã can thiệp
  (`SIGKILL` trực tiếp lúc 01:54:13 ngày 10/9, xem log
  `journalctl -u metanode-execution-0`) làm 1 lần chạy
  `production_readiness_check.sh` fail giữa chừng — **nên hỏi người này
  trước khi chạy drill xâm lấn hoặc chiếm dụng cụm lâu dài.**
- **Lần chạy `production_readiness_check.sh` gần nhất** (`/tmp/production_readiness_20260910_011343.log`
  trên `234`) — FAIL ở Tầng 3 (blockstm_logic, node_chaos_restart,
  snapshot_recovery đều fail), nhiều khả năng do người ở `192.168.1.40` can
  thiệp giữa chừng chứ không phải bug thật (Tầng 1, Tầng 2 đã PASS thật).
  **Chưa có 1 lần chạy `production_readiness_check.sh` nào PASS trọn vẹn
  end-to-end** — chỉ có `verify_multi_reset_stability.sh`/`snapshot_recovery`
  chạy riêng lẻ đã PASS mạnh (100/100 vòng).

---

## 5. Việc còn mở — khuyến nghị cho người tiếp theo

1. `git pull origin dev` trên máy `234` (và bất kỳ máy nào khác đang deploy
   từ nhánh này).
2. Khi máy `234`/`230` rảnh hẳn (xác nhận với người ở `192.168.1.40`): chạy
   `deploy/ci/production_readiness_check.sh` một lần cho tới khi PASS sạch
   end-to-end — đây là điều kiện còn thiếu để coi là "go/no-go xanh" chính
   thức.
3. Diễn tập lần lượt 4 drill trong `deploy/ci/incident_drills/` theo thứ tự
   khuyến nghị trong README của thư mục đó.
4. Điều tra thêm `wait_rpc_ready_seconds` (sleep cố định 5s trong
   `deploy/ci/ci_runner.py`) — nghi ngờ cùng lớp lỗi với bug #1/#7 (fixed
   sleep thay vì chờ data-driven), nhưng CHƯA xác nhận được vì dữ liệu lần
   thử bị nhiễu bởi người can thiệp. Cần 1 lần chạy sạch để kết luận.
