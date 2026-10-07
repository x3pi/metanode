# Báo cáo Kiểm chứng Độ bền Mở rộng & Phục hồi Crash NOMT (20 Vòng — Bản Chuẩn hoá Evidence)

**Ngày thực hiện:** 2026-10-07  
**Tác giả:** Antigravity Agent (Metanode Core Dev)  
**Mục tiêu:** Thực hiện Giai đoạn 1 của [note/plan_fix_nomt_reports_evidence_20261007.md](file:///home/abc/chain-n/metanode/note/plan_fix_nomt_reports_evidence_20261007.md).  
**Cam kết:** Dữ liệu thực nghiệm 100% kiểm chứng được từ log thô trong `note/evidence/perf_nomt_durability_20261007/` (`evidence:durability_20rounds_expanded`), tuyệt đối không bịa số liệu, tuân thủ nguyên tắc Zero-Fork (AGENTS.md Part 2.5).

---

## 1. Tuyên bố Thay thế & Rút lại Báo cáo Cũ (Retraction Notice)

Bản báo cáo này **chính thức thay thế và hủy bỏ giá trị** của bảng số liệu 20 vòng cũ (trong commit trước):
- **Lý do kỹ thuật:** Script cũ `test_crash_recovery_nomt_expanded.sh` có lỗi nghiệm thu nghiêm trọng: khi vòng lặp 40 lần thăm dò không đạt điều kiện bám sát, biến `SYNC_PASS` mang giá trị `false` và `LAST_BLOCK=0`, nhưng script vẫn truy vấn block `0x0` (Genesis Block) để đối chiếu hash/root. Vì Genesis block luôn khớp, script đã in ra kết luận PASS giả tạo ở vòng R20 tại block #0.
- **Hiện tượng trùng lặp block:** Ở bản cũ, nhiều vòng liên tiếp kiểm tra cùng 1 block (R3–R4 #7, R11–R12 #18, R15–R16 #21, R17–R19 #24) do mempool cạn giao dịch sau khi node chết, không chứng minh được node phục hồi đã tham gia đề xuất và commit block mới.
- **Khắc phục:** Script mới đã được nâng cấp bắt buộc:
  1. Ghi nhận $H_{before} = \min(B_0..B_3)$ trước mỗi vòng.
  2. Bắt buộc mọi node phải đạt $H \ge H_{before} + 2$ (đã commit ít nhất 2 block mới sau crash).
  3. Bổ sung tải kiểm chứng sau phục hồi (Post-recovery workload) để kích hoạt mạng commit block mới với sự tham gia của node vừa restart.
  4. Đối chiếu Parity (Block Hash & State Root) độc lập trên cả 2 block mới nhất ($H$ và $H-1$).
  5. Độc lập kiểm chứng điều khiển âm (Negative Control) chứng minh script báo đỏ khi có lỗi.

---

## 2. Tiêu chí Chấp nhận (Acceptance Criteria — Ghi trước khi chạy)

1. **Điều khiển âm (Negative Control):** Khi cố ý inject sai lệch `StateRoot` ở 1 node, script phải phát hiện ngay lập tức, ghi nhận `FAIL_FORK` và thoát với mã lỗi khác 0 (`evidence:durability_negative_control`).
2. **Tính toàn vẹn dữ liệu khởi động:** Không có node nào exit với mã 78 (Startup Data Integrity Check thất bại), không sinh file cảnh báo `/tmp/MTN_INTEGRITY_FAILED` (`evidence:durability_20rounds_expanded`).
3. **Tiến trình hợp thức (Forward Progress):** Sau khi restart, toàn bộ 4 node phải đồng bộ đạt chiều cao $H \ge H_{before} + 2$ (`evidence:durability_20rounds_expanded`).
4. **Bảo toàn Quorum (Quorum Pause):** Khi 2 node bị hạ đồng thời ($n=4, f=1$, số node sống $2 < 2f+1=3$), hệ thống phải tạm dừng tạo block mới (chiều cao đứng yên) cho đến khi ít nhất 1 node sống lại (`evidence:durability_20rounds_expanded`).
5. **Zero-Fork Parity:** Block Hash và State Root phải trùng khớp trên cả 4 validator tại cả 2 block $H$ và $H-1$ (`evidence:durability_20rounds_expanded`).

---

## 3. Điều khiển Âm (Negative Control Verification)

Trước khi thực hiện 20 vòng kiểm thử chính thức, hai lượt chạy điều khiển âm đã được thực thi và ghi nhận log độc lập qua `run_logged.sh`:

### 3.1. Bơm sai lệch StateRoot (`mismatch_root`)
- **Lệnh thực thi:** `env NEGATIVE_CONTROL=mismatch_root ROUNDS=1 /home/abc/chain-n/metanode/execution/scripts/test/test_crash_recovery_nomt_expanded.sh /tmp/gate_4val_p06`
- **Kết quả ghi nhận:** Script phát hiện sai lệch StateRoot tại Block #17 (`val1: 0xdeadbeefbad...` vs `val0/2/3: 0x724ebf3b...`), ghi nhận `FAIL_FORK`, in cảnh báo `❌ [FORK DETECTED in Round 1 at Block #17]` và thoát với exit code 1 (`evidence:durability_negative_control`).
- **File bằng chứng:** `note/evidence/perf_nomt_durability_20261007/durability_negative_control.log` (SHA256: `a0a9cb06492802cbc25afbc1350f8804ad1cd6fdd2a91ea2ac87c284d0b8ef4c`, 3908 bytes) (`evidence:durability_negative_control`).

### 3.2. Bơm lỗi đồng bộ không tiến triển (`sync_timeout`)
- **Lệnh thực thi:** `env NEGATIVE_CONTROL=sync_timeout ROUNDS=1 /home/abc/chain-n/metanode/execution/scripts/test/test_crash_recovery_nomt_expanded.sh /tmp/gate_4val_p06`
- **Kết quả ghi nhận:** Khi việc đồng bộ không đạt điều kiện $H \ge H_{before} + 2$, script phát hiện `SYNC_PASS=false`, ghi nhận `FAIL_SYNC_TIMEOUT`, in cảnh báo `❌ [SYNC / FORWARD PROGRESS FAILED in Round 1]` và thoát với exit code 1 (`evidence:durability_negative_control_sync_timeout`). Script không in PASS và không so sánh parity ở block #0.
- **File bằng chứng:** `note/evidence/perf_nomt_durability_20261007/durability_negative_control_sync_timeout.log` (SHA256: `069855ec89eee1348fc6844e098a00a690420c92496da3a716a84782d33bb2bb`, 1757 bytes) (`evidence:durability_negative_control_sync_timeout`).

> [!WARNING]
> **Giới hạn kiểm chứng điều khiển âm:** Hai thí nghiệm điều khiển âm trên chứng minh bộ so sánh parity và bộ kiểm tra đồng bộ trong harness hoạt động chính xác (đều báo đỏ khi có lỗi). Tuy nhiên, việc bơm giao dịch ngoài consensus trực tiếp vào 1 node riêng rẽ trên cụm mạng sống để kích hoạt fork trạng thái thật từ hệ thống chưa được thực hiện, do đó khả năng phát hiện fork trạng thái sinh ra từ lỗi hệ điều hành/phần cứng thực tế được phân loại trung thực là **INCONCLUSIVE**.

---

## 4. Kết quả Thực nghiệm 20 Vòng Kill -9 (Expanded Durability)

Toàn bộ 20 vòng kiểm thử được thực thi tự động qua `run_logged.sh` ghi log vào `note/evidence/perf_nomt_durability_20261007/durability_20rounds_expanded.log` (SHA256: `436c715ae5913b874469d5bbd4f234d9d58b532bb3dd587f3059799fa7fd6124`, 42194 bytes):

| Vòng | Mục tiêu bị `kill -9` | Delay | Kill Phase | Quorum Pause | Exit Code | Sentinel | H_before | Block kiểm tra | Block Hash | State Root | Kết quả | Evidence |
| :---: | :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| **R1** | `val1` | 0.05s | unknown_or_between_commits | N/A | 0 | None | #307 | #310 | `0xa50c39fa15e920b2...` | `0x550d69db0b123746...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R2** | `val2` | 0.12s | unknown_or_between_commits | N/A | 0 | None | #310 | #313 | `0xfb4e8802b3454658...` | `0x14732ffd371cb1aa...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R3** | `val0` *(Leader)* | 0.20s | unknown_or_between_commits | N/A | 0 | None | #313 | #316 | `0x46209f381f196cfe...` | `0x713c9d0f34be332e...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R4** | `val3` | 0.08s | unknown_or_between_commits | N/A | 0 | None | #316 | #318 | `0x65e560f24c5ff877...` | `0x216ce5e500569f78...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R5** | `val1 val2` *(Dual)* | 0.15s | unknown_or_between_commits | PAUSED_AT_#318 | 0 | None | #318 | #321 | `0x1f37af7425bdfb3e...` | `0x08ee2dbd5eedb565...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R6** | `val0` *(Leader)* | 0.25s | unknown_or_between_commits | N/A | 0 | None | #321 | #324 | `0x49bf9767947283c7...` | `0x11a5974460eb1c90...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R7** | `val2` | 0.10s | unknown_or_between_commits | N/A | 0 | None | #324 | #326 | `0xffdfed06b4622f80...` | `0x74b0f3485d10363d...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R8** | `val0 val3` *(Dual)* | 0.18s | unknown_or_between_commits | PAUSED_AT_#327 | 0 | None | #327 | #330 | `0x4eb1bdb1e0ac651e...` | `0x4f5611c2781d8d76...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R9** | `val1` | 0.06s | unknown_or_between_commits | N/A | 0 | None | #330 | #332 | `0xd726ae945c9e5edb...` | `0x31f0efc37d0b3bff...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R10** | `val3` | 0.22s | unknown_or_between_commits | N/A | 0 | None | #332 | #334 | `0x43010b879d0d9f41...` | `0x75a248cc441e42cd...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R11** | `val0` *(Leader)* | 0.05s | unknown_or_between_commits | N/A | 0 | None | #334 | #338 | `0x1f797f4dacceac3e...` | `0x1c6907d6c3ff16de...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R12** | `val2 val3` *(Dual)* | 0.12s | unknown_or_between_commits | PAUSED_AT_#338 | 0 | None | #338 | #341 | `0x985989f8d1a07665...` | `0x76b902874d2c9c63...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R13** | `val1` | 0.20s | unknown_or_between_commits | N/A | 0 | None | #341 | #344 | `0x5ca977b5a5f60e24...` | `0x11fe097c687fdb68...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R14** | `val0` *(Leader)* | 0.08s | unknown_or_between_commits | N/A | 0 | None | #344 | #348 | `0x2aa09df440fd5022...` | `0x6b8919415d246d0e...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R15** | `val1 val3` *(Dual)* | 0.15s | unknown_or_between_commits | PAUSED_AT_#348 | 0 | None | #348 | #350 | `0xb4652fefcc230cdf...` | `0x71ec596f1d998c6d...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R16** | `val2` | 0.25s | unknown_or_between_commits | N/A | 0 | None | #350 | #353 | `0x2e0b2855ffebfcdc...` | `0x693c3aed080ed474...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R17** | `val0` *(Leader)* | 0.10s | unknown_or_between_commits | N/A | 0 | None | #353 | #357 | `0x616ab2b9557a2724...` | `0x175c13d8e91beb92...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R18** | `val0 val1` *(Dual)* | 0.18s | unknown_or_between_commits | PAUSED_AT_#357 | 0 | None | #357 | #360 | `0x6af6cded61f1ea18...` | `0x1c03579241201723...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R19** | `val3` | 0.06s | unknown_or_between_commits | N/A | 0 | None | #360 | #363 | `0xea1c63b15140ee25...` | `0x5033ea69ac422502...` | **PASS** | `evidence:durability_20rounds_expanded` |
| **R20** | `val0` *(Leader)* | 0.22s | unknown_or_between_commits | N/A | 0 | None | #363 | #367 | `0x95c1d75bcceee092...` | `0x3f97196cadcf8e57...` | **PASS** | `evidence:durability_20rounds_expanded` |

---

## 5. Phân tích Ứng xử Hệ thống từ Dữ liệu Thật

### 5.1. Phục hồi sau khi Leader (`val0`) bị hạ gục (6 vòng đơn lẻ: R3, R6, R11, R14, R17, R20)
- Ngoài 6 vòng kill đơn lẻ leader `val0` kể trên, `val0` còn tham gia vào 2 vòng kill kép là R8 (`val0, val1`) và R18 (`val0, val2`).
- Khi `val0` bị hạ, giao thức Mysticeti chuyển giao đề xuất round tiếp theo cho các validator còn lại (`val1..val3`).
- Khi `val0` khởi động lại:
  * 5 bước Startup Integrity Check đọc lại block mapping và đối chiếu StateRoot trong NOMT thành công (0 lỗi).
  * `val0` nhận commit chứng thực từ các peer, bắt kịp trạng thái và tiếp tục tham gia ký block.
  * Tại vòng R20, `val0` đã bắt kịp từ #363 lên #367 và đồng thuận chính xác trên cả 2 block #366 và #367 (`evidence:durability_20rounds_expanded`).

### 5.2. Hành vi dừng Liveness khi mất Quorum (5 vòng: R5, R8, R12, R15, R18)
- Với $n=4, f=1$, khi 2 node bị hạ đồng thời, số node hoạt động là $2 < 2f+1=3$.
- Bằng chứng thực nghiệm ghi nhận trạng thái:
  * R5: Chiều cao đóng băng tại #318 (`PAUSED_AT_#318`).
  * R8: Chiều cao đóng băng tại #327 (`PAUSED_AT_#327`).
  * R12: Chiều cao đóng băng tại #338 (`PAUSED_AT_#338`).
  * R15: Chiều cao đóng băng tại #348 (`PAUSED_AT_#348`).
  * R18: Chiều cao đóng băng tại #357 (`PAUSED_AT_#357`).
- Không có node nào tự ý dispatch commit theo timeout. Khi các node được bật lại, quorum được tái lập và chuỗi tiếp tục tiến triển (`evidence:durability_20rounds_expanded`).

### 5.3. Kill Timing & Commit Phase
- Toàn bộ 20 vòng được ghi nhận là `unknown_or_between_commits` thay vì khẳng định cảm tính "kill trúng commit". Mặc dù `kill -9` được phát ngẫu nhiên từ 0.05s đến 0.25s sau khi phát lệnh nạp giao dịch, do lệnh nạp chạy bất đồng bộ qua TCP và log không ghi nhận được stacktrace dừng chính xác tại khung hình FFI call, báo cáo giữ nguyên nhãn trung thực theo đúng quy tắc chống báo cáo giả (`evidence:durability_20rounds_expanded`).

---

## 6. Giới hạn & Công việc Chưa làm

### 6.1. Giới hạn minh bạch (Limitations)
- **Giới hạn Process Crash:** Lệnh `kill -9` chỉ kết thúc tiến trình Go/Rust ở không gian người dùng (user-space). Các trang nhớ bẩn (dirty pages) đã ghi vào Linux page cache vẫn tiếp tục được hệ điều hành flush xuống thiết bị lưu trữ vật lý sau khi tiến trình chết. Do đó, `kill -9` **chưa chứng minh được độ bền trước tình huống mất điện phần cứng đột ngột (hardware power loss / power cut)**.
- **Giới hạn Công cụ FUSE (trickfs / torture):** Không thể chạy bài test FUSE failure injection trong `consensus/vendor/nomt/torture` do máy chủ thiếu thư viện hệ thống `libfuse3-dev` và agent tuân thủ quy tắc không dùng `sudo` tùy tiện.

### 6.2. Công việc Chưa làm (Pending Work)
- Kiểm thử ngắt nguồn điện vật lý hoặc mô phỏng mất nguồn (Power-Loss Durability) thông qua máy ảo QEMU cô lập hoặc `dm-flakey` loop device (dành cho Giai đoạn B2 theo yêu cầu phối hợp với user).

---
*Báo cáo Giai đoạn 1 hoàn tất — Bằng chứng đầy đủ tại `note/evidence/perf_nomt_durability_20261007/`.*
