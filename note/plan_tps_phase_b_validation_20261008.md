# Kế hoạch giao Gemini: xác nhận B1 và tìm nút thắt thật (2026-10-08)

**Bối cảnh:** B1 (`8d5aa6cc`, tính song song `txsRoot`/`receiptsRoot`) báo +24,6% TPS (10.898 → 13.580 tx/s, blast 60 s). Review độc lập cho thấy số liệu chưa vững (xem mục 0). Chưa làm Giai đoạn D (gỡ gate `waitCommitted`) cho tới khi hoàn tất kế hoạch này và được user duyệt.

## 0. Vấn đề cần giải quyết (đã kiểm tra từ evidence)
1. Commit B1 ghi baseline SD = 391 và Welch t = 12,5. Tính lại từ `note/evidence/tps_improvement_20261008/phase_a_steady_state_summary.json`: A1 = 14.061 / 10.118 / 10.137 / 9.626 / 10.547, trung bình 10.898, **SD = 1.798**, t ≈ **3,3**. SD 391 chỉ ra được nếu bỏ lượt đầu 14.061.
2. Lượt A1 đầu đạt 14.061 (ngang B1) và A/B không chạy xen kẽ, nên chênh lệch có thể là trạng thái máy theo thời gian.
3. Mô hình Phase A (`ta` ≈ 166 ms, `tb` ≈ 34 ms) so với chu kỳ block ~950 ms: B1 tiết kiệm ~20–25 ms/block, tức ~2–3% chu kỳ, không giải thích được +24,6%. Khoảng ~750 ms chu kỳ **chưa được gán cho giai đoạn nào**.
4. Có hai giao thức "TPS" khác nhau: burst 25k tx (~6,1k) và blast 60 s (~10,9k–13,6k). Không so sánh chéo.

## Quy tắc bắt buộc
- Đọc `AGENTS.md` (zero-fork tối thượng, không timeout để dispatch, thà pending chứ không fork) và `PROJECT_STRUCTURE.md`.
- Mọi số liệu phải từ lệnh đã chạy thật, lưu log vào `note/evidence/tps_phase_b_validation_20261008/` kèm `MANIFEST.json` (sha256). **Tự tính lại thống kê từ file thô**, không chép số từ báo cáo cũ.
- Ghi giả thuyết + tiêu chí vào `PREREGISTERED.md` và commit **trước** khi chạy. Không đổi tiêu chí sau khi thấy kết quả.
- Không đổi hành vi consensus/execution trong kế hoạch này. Chỉ thêm quan sát (log/timestamp) và nêu rõ.
- `git add` theo tên file, không push/merge main khi chưa được user duyệt, không squash. Dừng tiến trình theo PID, không `pkill -f` chuỗi khớp chính shell.
- `consensus/metanode/scripts/build_check.sh` phải sạch lỗi/warning sau mỗi thay đổi code (kể cả code quan sát).

## Việc 1 — A/B xen kẽ để xác nhận B1
1. Build hai binary từ cùng toolchain: `BEFORE` = ngay trước `8d5aa6cc` (`0818f2f1`), `AFTER` = `8d5aa6cc`. Ghi commit hash, `sha256sum` binary, thông số máy và load trước khi chạy.
2. Thứ tự **xen kẽ ngẫu nhiên có seed** (vd. A B B A A B ...), **≥ 8 lượt mỗi bên**, mỗi lượt: wipe cụm 4 validator cổng 31xxx từ `/tmp/gate_4val_clean_template`, chạy `secp_tps_blast` 60 s `-batch 1000 -verify-parity`. Nghỉ cố định giữa các lượt để máy ổn định.
3. Báo: trung bình, **trung vị**, SD, min/max, Welch t-test và khoảng tin cậy 95% của hiệu; kết quả có và không loại lượt đầu. Zero-fork FAIL ở bất kỳ lượt nào = dừng ngay và báo.
4. **Tiêu chí ghi trước:** B1 được coi là xác nhận nếu hiệu trung vị ≥ +5% và khoảng tin cậy 95% không chứa 0. Dưới ngưỡng: ghi "không xác nhận", đề xuất giữ hoặc revert theo rủi ro.
5. Nếu hiệu lớn hơn nhiều so với ~3%: tìm nguyên nhân (vd. bỏ vòng băm forensic do `logger.IsDebugEnabled()`, tác dụng phụ khác) bằng cách tách từng thay đổi của `8d5aa6cc` thành binary riêng.

## Việc 2 — Gán nguồn cho ~750 ms chu kỳ chưa giải thích
1. Thêm log timestamp tuyệt đối (ns) **phía Rust**: nhận commit từ consensus, bắt đầu `send_committed_subdag`, nhận phản hồi từ Go, gửi commit kế tiếp. Phía Go: nhận, qua gate, kết thúc exec, kết thúc root, kết thúc persist, trả lời Rust. Chỉ log, không đổi hành vi.
2. Chạy blast 60 s ≥ 5 lượt trên binary AFTER. Với mỗi block steady-state (bỏ block 1–2) dựng timeline tuyệt đối và tính: thời gian Go bận, thời gian Go idle chờ Rust, thời gian Rust chờ Go, khoảng chu kỳ giữa hai commit.
3. Trả lời bằng số: ~950 ms chu kỳ gồm bao nhiêu % ở Go (exec/root/persist), Rust, hàng đợi/IPC? Nút thắt thật nằm ở đâu?
4. Nếu Go idle phần lớn: nút thắt nằm ở Rust/đồng thuận, ngăn không làm Giai đoạn D. Nếu Rust idle chờ Go: Giai đoạn D có cơ sở, tính lại cận trên Amdahl bằng số đo mới.

## Việc 3 — Kiểm tra đường discard/recovery của B1
1. Đọc `PrecomputeRoots`/`createBlockFromResults`/`speculative_executor.go`: khi session bị hủy (`CleanGEI`, Conflict, halt-not-guess, crash giữa chừng) `TxDB` và trie receipts tạo sẵn có bị rò, có ghi nhầm xuống storage dùng chung (`GetStorageTransaction`/`GetStorageReceipt`) không, goroutine có bị treo không.
2. Viết test: hủy session sau khi `PrecomputeRoots` chạy; precompute lỗi (`Err != nil`) phải rơi về đường tính tuần tự và cho cùng kết quả; `-race`.
3. Chạy kịch bản `kill -9` ngẫu nhiên khi đang blast (≥5 chu kỳ), sau đó so block hash + state root giữa 4 node và giữa replay với live (`recovery.rs` vs `executor.rs`, nguồn drift GEI cũ).
4. Chạy lại `go test -race ./cmd/simple_chain/processor/...` đủ bộ.

## Việc 4 — Sửa tài liệu và số liệu sai
1. Cập nhật `note/evidence/tps_improvement_20261008/b1_speculative_roots_summary.json` hoặc thêm file đính chính: SD baseline thực = 1.798, t = 3,3. Không sửa lịch sử git; thêm note rõ ràng.
2. Cập nhật `plan_tps_improvement_impl_20261008.md` (checklist B1): đổi "+24,6% xác nhận" thành kết quả của Việc 1.
3. Ghi rõ trong mọi báo cáo giao thức đo (burst 25k vs blast 60 s) cạnh mỗi con số TPS.

## Việc 5 — Báo cáo và quyết định
Viết `note/tps_phase_b_validation_report_20261008.md`:
- Kết quả Việc 1–3 với số liệu thô và thống kê.
- B1: xác nhận / không xác nhận, giữ / revert, rủi ro còn lại.
- Phân bổ thời gian chu kỳ theo Go/Rust/hàng đợi và nút thắt thật.
- Đề xuất bước tiếp theo xếp hạng theo số đo (tối ưu `block_stm_exec`, hay phía Rust, hay Giai đoạn D). **Không cài đặt Giai đoạn D** nếu chưa có phê duyệt bằng văn bản của user.

## Ngoài phạm vi
RSS/heap (xem `note/design_bounded_memory_indexes_20261007.md`, thí nghiệm 45 phút còn FAIL); tối ưu B2 (đã bỏ); đổi `MaxBatchTxCount`.

## Tiêu chí hoàn thành
- [ ] `PREREGISTERED.md` commit trước khi chạy
- [ ] A/B xen kẽ ≥ 8 lượt mỗi bên, thống kê tự tính, zero-fork PASS
- [ ] Timeline Go + Rust gán nguồn cho chu kỳ block, nêu nút thắt thật bằng số
- [ ] Test discard/precompute lỗi + chaos kill -9 + replay khớp live
- [ ] Số liệu sai của B1 được đính chính, giao thức TPS ghi rõ
- [ ] Báo cáo kết luận, `build_check.sh` sạch
