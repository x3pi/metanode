# Kiểm toán giả định "có supervisor khởi động lại" của watchdog fail-stop — 2026-10-13

Nhãn: **[ĐO]** = kết quả của lệnh tôi chạy trong phiên này (có evidence); **[ĐỌC CODE]** = đọc mã/cấu hình, chưa chạy thật; **[CHƯA ĐO]** = chưa làm.
Evidence: `note/evidence/watchdog_supervisor_20261013/` (manifest `MANIFEST.json`, mã id `wdexp_systemd_user`).

## 1. Câu hỏi
`pkg/utils/watchdog.go` (`StartWatchdogFatal`, mặc định 300 s, `METANODE_EXEC_STUCK_ABORT_SECS=0` tắt) dump goroutine rồi `SIGABRT` để node được supervisor khởi động lại và đồng bộ lại từ peer.
Giả định này có đúng với cấu hình deploy trong repo không?

## 2. Tiêu chí (ghi trong phần đầu `execution/scripts/test/evidence/watchdog_supervisor_experiment.sh`, trước khi chạy)
- E1/E2: bị kẹt + abort ⇒ supervisor phải khởi động lại (`NRestarts` tăng).
- E3 (đối chứng): `METANODE_EXEC_STUCK_ABORT_SECS=0` ⇒ không abort, không restart.
- E4 (đối chứng): pha kết thúc bình thường ⇒ không abort.
- E5: vòng lặp crash vượt `StartLimitBurst` ⇒ unit chuyển `failed` và dừng restart.
- E6: unit phụ thuộc `Requires=`+`After=` vào unit kẹt ⇒ quan sát phản ứng.

## 3. Kiểm toán cấu hình [ĐỌC CODE]
- `deploy/ansible/roles/systemd_services/templates/metanode-execution.service.j2`: `Restart=on-failure` (dòng 21), `RestartSec=15s`, `StartLimitIntervalSec=300`, `StartLimitBurst=5` (dòng 7–8), `GOTRACEBACK=crash`, `LimitCORE=infinity`.
- `.../metanode-consensus.service.j2`: `Requires=metanode-execution-{{ item }}.service` (dòng 6), `Restart=on-failure` (dòng 21), `StartLimitBurst=5` trong 300 s.
- `deploy/ansible_clusters/roles/exec_cluster/templates/metanode-exec-cluster.service.j2` và `.../parent_chain/templates/metanode-parentchain.service.j2`: `Restart=always`, `RestartSec=3s`, không khai báo `StartLimit*` (dùng mặc định hệ thống của systemd), `LimitCORE=infinity`; không đặt `GOTRACEBACK`.
- `execution/scripts/test/gate_e2e/run_env.sh` (`start_exec`): chạy `simple_chain` nền bằng shell, **không có supervisor**: khi watchdog abort thì node ở đó chết hẳn.
- Mặc định systemd trên máy này: `DefaultStartLimitIntervalUSec=10s`, `DefaultStartLimitBurst=5` (đọc bằng `systemctl show`).
- `core_pattern` trên máy này là `|/usr/share/apport/apport ...` và `apport` đang `active`.

## 4. Thí nghiệm [ĐO] (systemd 255, kernel 6.8.0-139, chạy ở `systemd --user`, không `sudo`)
Probe `wd_probe` liên kết đúng mã `pkg/utils/watchdog.go`; ngưỡng abort được co xuống 1–3 s để quan sát được trong vài giây (không phải ngưỡng 300 s thật).
- E1 (`Restart=on-failure`): sau khi abort, journal ghi `Main process exited, code=dumped, status=6/ABRT` rồi `Scheduled restart job`; `NRestarts` tăng 1 → 2 → 3 trong 20 s (evidence:wdexp_systemd_user).
- E2 (`Restart=always`): cùng hành vi, `NRestarts` đạt 2 trong 20 s (evidence:wdexp_systemd_user).
- E3 (đối chứng, abort tắt): `NRestarts` giữ 0 và `ActiveState=active` suốt 15 s, tiến trình kẹt vẫn sống (evidence:wdexp_systemd_user).
- E4 (đối chứng, pha bình thường): `NRestarts` giữ 0 (evidence:wdexp_systemd_user).
- E5 (`StartLimitBurst=5`, 300 s): sau khi `NRestarts` đạt 5, unit ở `ActiveState=failed` và không được khởi động lại thêm trong các lần quan sát tiếp theo (evidence:wdexp_systemd_user).
- E6 (`Requires=` + `After=`): mỗi lần unit kẹt tự khởi động lại, unit phụ thuộc được dừng và khởi động lại theo (journal ghi `Stopping`, `Stopped`, `Started`; `InvocationID` đổi, `NRestarts` của unit phụ thuộc vẫn 0) (evidence:wdexp_systemd_user). Unit phụ thuộc trong thí nghiệm là `/bin/sleep`, không phải node consensus thật.

## 5. Diễn giải và rủi ro
- `Restart=on-failure` và `Restart=always` đều khởi động lại sau `SIGABRT` do watchdog: **giả định supervisor đúng với 4 template deploy trong repo** [ĐO, E1/E2].
- **`run_env.sh` (môi trường test cô lập) không có supervisor**: watchdog abort sẽ để node chết; mọi benchmark/test cục bộ cần lưu ý khi đặt `METANODE_EXEC_STUCK_ABORT_SECS` [ĐỌC CODE].
- **Giới hạn khởi động lại:** với mẫu `deploy/ansible/...` (5 lần/300 s) mỗi chu kỳ abort cần ít nhất ngưỡng 300 s kẹt cộng `RestartSec`, nên watchdog đơn thuần không chạm giới hạn này; nhưng một vòng crash khác (ví dụ không khởi động nổi) sẽ chạm và node dừng hẳn đến khi `reset-failed` thủ công (cơ chế đã quan sát ở E5, [ĐO]).
- **Khởi động lại execution kéo theo khởi động lại consensus** ở mẫu có `Requires=` (E6, [ĐO] với unit thay thế). Chưa đo thời gian đồng bộ lại của node thật sau khi cả hai khởi động lại ([CHƯA ĐO]).
- **Core dump:** mọi template đặt `LimitCORE=infinity`, `SIGABRT` làm tiến trình có RSS vài GB tạo core dump; cách xử lý phụ thuộc `core_pattern` của máy đích (máy này dùng `apport`). Nếu máy đích ghi core thẳng vào `WorkingDirectory`, mỗi lần abort có thể tiêu thụ vài GB đĩa [ĐỌC CẤU HÌNH, chưa chạy thật, `LimitCORE=0` được dùng trong thí nghiệm].
- `exec_cluster` không đặt `GOTRACEBACK=crash`; watchdog vẫn tự ghi dump goroutine ra `os.TempDir()` trước khi abort (`watchdog.go`, `abortProcess`) nên post-mortem không phụ thuộc biến này [ĐỌC CODE].
- Watchdog chỉ dừng TIẾN TRÌNH CỤC BỘ bằng `SIGABRT`; mã không có đường trả phản hồi sai cho Rust trước khi abort (hàm `abortProcess` chỉ dump rồi `syscall.Kill(self, SIGABRT)`) [ĐỌC CODE].

## 6. Giới hạn của thí nghiệm
- Probe không phải node thật; thời gian abort được co lại; không đo thời gian đồng bộ lại ở node thật.
- Chạy ở `systemd --user`, không phải instance hệ thống (cùng engine nhưng khác `User=`, `LimitCORE` đặt 0).
- Unit phụ thuộc là `sleep`, không phải tiến trình consensus Rust.

## 7. Chưa làm
- Thử abort do watchdog trên node thật (cần hook gây kẹt có kiểm soát trong một build thử nghiệm) và đo thời gian đồng bộ lại cùng parity state root.
- Đo kích thước core dump thực tế với `core_pattern` khác `apport`.
- Cân nhắc đặt `StartLimitIntervalSec/StartLimitBurst` tường minh cho hai template `ansible_clusters` (hiện dùng mặc định hệ thống, `Restart=always` với `RestartSec=3s` không chạm giới hạn mặc định).
