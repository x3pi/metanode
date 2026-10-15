# Kế hoạch giao agent: giảm chi phí GC khi backlog lớn, A/B Raft, phân rã chi phí Rust (2026-10-15)

**Người giao:** agent trước (Claude), theo yêu cầu của chủ dự án. **Người thực hiện:** agent khác. **Quy tắc tối cao: không được báo cáo số liệu giả hoặc không có file thô.** Nếu một việc không chạy được, ghi đúng "chưa làm được, lý do", không điền số.

## 0. Bối cảnh đã được đo (đọc trước, đừng làm lại)
Mọi số dưới đây nằm trong repo; mở file để kiểm tra, không tin lời kể.

| Phát hiện | Nơi có bằng chứng |
|---|---|
| Lỗi mất tx giữa client và validator (~30% lượt sustained) do `SocketServer.HandleConnection` bỏ hàng đợi khi lỗi đọc không phải EOF; đã sửa (commit `1cea5f0e`), sau sửa 33/33 lượt không mất | `note/tps_ingress_rootcause_report_20261013.md`, `note/evidence/tps_ingress_rootcause_20261013/` |
| TPS giảm khi backlog tăng (BFT 4 validator, sustained): 13,1k (0,5 triệu tx) → 12,2k (1) → 11,5k (2) → 10,0k (4), Spearman −0,91 | `note/tps_by_volume_report_20261014.md` |
| Giữa pha xả backlog 4 triệu tx, GC của Go chiếm ~29–30% CPU (so với ~11% ở 1 triệu tx); heap Go sống ×2,3; đối tượng heap lớn: `smart_contract.NewExecuteSCResult`, `sync.Map.Swap`, `blockchain.AddTxToCache`, `tx_processor.storeVerifiedBound`, `ReceiptTracker.StoreTxHashConnEntry` | `note/node_profile_backlog_raft_report_20261014.md`, `note/evidence/node_profile_backlog_20261014/` |
| Node đặt sẵn `GOGC=800`, `GOMEMLIMIT=8 GB` (`execution/cmd/simple_chain/main.go:135-141,176-207`); biến `GOGC` bị bỏ qua; `META_GOMEMLIMIT` (GB) có tác dụng. Nâng 12 GB: +3,6% (p = 0,076, 4 cặp); 24 GB: GC 19,5% CPU, +6,6% nhưng RSS 32 GB mỗi validator | cùng báo cáo trên |
| Node cũ (`f97a2fbf`) làm chậm client (~0,86 triệu tx/lượt) nên số 14.072 tx/s đã công bố đo trên backlog nhỏ; ở khối lượng tx tương đương node mới nhanh hơn ~6% | `note/node_ab_old_vs_new_report_20261014.md` |
| Raft 3 replica (công cụ đếm): burst 25k 13,1k tx/s; sustained 1 triệu tx 20,3k tx/s, không mất tx, không "dừng giữa chừng" (báo cáo cũ ghi có) — **chưa biết nguyên nhân khác biệt** | báo cáo profile ở trên, mục 3.3 |
| Còn khoảng 7% tốc độ ở 4 triệu tx chưa giải thích sau khi gần như bỏ ràng buộc GC | cùng báo cáo, mục 4 |

Sổ tuyên bố: `note/CLAIMS_LEDGER.md` (dòng 12, 16, 17 liên quan).

## 1. Quy tắc bắt buộc (mọi việc)
- Đọc `AGENTS.md` và `PROJECT_STRUCTURE.md` trước. **Zero-Fork là tối cao**: mọi thay đổi mã node phải giữ nguyên thứ tự tx, kết quả thực thi và state root. Không dùng timeout/sleep để quyết định dispatch. Trước khi sửa mã node: phân tích blast radius (codegraph hoặc `grep -rn` theo `AGENTS.md` Phần 4) và ghi vào đầu báo cáo; chạy `consensus/metanode/scripts/build_check.sh` sau khi sửa, phải sạch lỗi và cảnh báo.
- **Nhãn bắt buộc** cho từng phát biểu: `[ĐO]` (lệnh bạn chạy, có file thô), `[TÍNH]` (suy ra từ số [ĐO], ghi công thức), `[ĐỌC CODE]` (chưa chạy), `[TRÍCH]` (lấy từ báo cáo khác), `[GIẢ THUYẾT]`, `[CHƯA ĐO]`. Không dùng từ tuyệt đối ("100%", "hoàn toàn", "chắc chắn", "chứng minh"…) nếu không kèm `evidence:<id>`.
- **Ghi tiêu chí trước khi đo**: với mỗi thí nghiệm, tạo `PREREGISTERED.md` trong thư mục evidence và commit **trước** khi chạy: giả thuyết, số lượt, tiêu chí đạt/không đạt, cách thống kê. Không đổi tiêu chí sau khi đã thấy số.
- **So sánh chỉ hợp lệ khi ghép cặp, xen kẽ và cùng một đợt chạy** (thứ tự mỗi cặp xáo ngẫu nhiên có seed). Số tuyệt đối dao động ±12% giữa các giờ/ngày trên máy dùng chung: không được so số của hai đợt khác nhau. Luôn kèm **đối chứng A/A** (cùng binary hai lần) để ước lượng nhiễu.
- **Số TPS luôn kèm khối lượng tx và chế độ đo** (xem báo cáo tps_by_volume).
- Mỗi báo cáo: manifest `MANIFEST.json` sinh bằng `execution/scripts/test/evidence/make_manifest.py`, rồi `python3 -I execution/scripts/test/evidence/verify_evidence.py <dir> --report <report.md> --strict` và `python3 execution/scripts/test/evidence/claims_lint.py <report.md>` phải qua. Báo cáo phải có mục "Giới hạn" và "Chưa làm". Extractor CSV cho mọi số trung bình/SD trong bảng.
- Cô lập: chỉ dùng cổng 31xxx (BFT 31200-31203, RPC 31646-31649, pprof 31446-31449; Raft dùng `run_raft_counted.py`: 316xx-319xx). **Không** đụng `/opt/metanode`, cụm 231/230, tiến trình của người khác. Không `sudo`. Không dùng `pkill -f <mẫu>` (đã từng giết nhầm shell của chính agent); dừng bằng pid hoặc `ps -eo pid,args | awk '/mẫu/ && !/awk/ {print $1}' | xargs -r kill`.
- **Bẫy đã gặp, tránh lại:** (1) `run_env.sh stop` bỏ qua node nếu `/proc/<pid>/exe` là symlink nằm ngoài thư mục `bins` → luôn dùng **bản sao thật** của binary, không symlink. (2) `shutil.copytree` làm đặc các tệp NOMT thưa và đầy đĩa → chỉ dùng `cp -a` (runner đã xử lý). (3) `go tool pprof` trên máy này lỗi khởi động → dùng `/tmp/p06_bins/pprof`. (4) Lượt "node chết" thường do cụm lượt trước chưa dừng: kiểm tra `ss -ltn | grep -c ":31[0-9][0-9][0-9]"` = 0 trước khi bắt đầu. (5) Công cụ burst bị giới hạn 50.000 tx (số khoá) → khối lượng lớn dùng sustained với `-max-txs`. (6) Đĩa dùng chung: kiểm `df -h /tmp` > 100 GB trước các lô dài; bộ nhớ: dừng nếu `MemAvailable` < 20 GB.
- **Không push, không merge, không tạo PR** khi chưa có lệnh của chủ dự án. `git add` theo tên file, không `git add <thư mục>` rộng. Commit kèm dòng `Co-Authored-By` theo hướng dẫn của phiên.
- Mỗi phản hồi kết thúc bằng khối "📋 Tóm tắt thay đổi" tiếng Việt (định dạng trong `AGENTS.md` Phần 5).
- Chạy dài: dùng `Monitor`/heartbeat để theo dõi tiến độ, đừng chờ người dùng hỏi trạng thái.

## 2. Công cụ có sẵn
- Runner BFT: `execution/scripts/test/evidence/run_tps_counted.py` (cờ: `--mode burst|sustained`, `--runs`, `--tag`, `--bins DIR` chứa `simple_chain`, `parent_chain`, `secp_tps_blast` là bản sao thật, `--out`, `--duration`, `--max-txs`, `--batch`, `--max-wait-sec`, `--profile-at S` chụp profile CPU/heap/goroutine của validator 0 + dòng thời gian CPU/RSS, `--node-env KEY=VALUE`). Cụm mới từ `/tmp/gate_4val_clean_template` cho mỗi lượt.
- Runner Raft: `execution/scripts/test/evidence/run_raft_counted.py`. Công cụ đo: `execution/cmd/tool/secp_tps_blast` (`-max-txs`).
- Thống kê: `execution/scripts/test/evidence/stats_util.py` (`welch_t_test`, `t_two_tailed_p`, `t_critical`, `spearman_correlation`).
- Ví dụ lô xen kẽ: các script `ab_driver.py.txt` và `ab_driver4.py.txt` trong `note/evidence/node_ab_old_vs_new_20261014/ab1_heavy_load/` và `.../ab2_matched_load/` (đổi tên bỏ `.txt`, đổi đường dẫn scratch cho phù hợp).
- Build node: `cd execution && CGO_ENABLED=1 go build -o <dir>/simple_chain ./cmd/simple_chain` (thư viện Rust đã build sẵn ở `target/release` và `consensus/metanode/target/release`).

## Việc 0 — Chuẩn bị và kiểm tra nền (làm trước)
1. Xác nhận cây sạch (`git status`), ghi commit đang đo. Dựng `bins_base/` từ HEAD hiện tại (bản sao thật của 3 binary).
2. Chạy 2 lượt sustained 1 triệu tx bằng `bins_base`; mỗi lượt phải gửi = nhận = commit và parity OK. Nếu không, **dừng và báo**, đừng làm tiếp.
3. Chạy đối chứng A/A: 4 cặp (`bins_base` hai lần, xen kẽ) ở 4 triệu tx để biết nhiễu giữa hai lượt cùng binary. Ghi độ lệch chuẩn của hiệu từng cặp: mọi khẳng định "nhanh hơn" sau này phải vượt nhiễu này.

## Việc 1 — Giảm số đối tượng Go giữ lại cho mỗi tx đang chờ (sửa mã node, cần duyệt thiết kế trước)
**Mục tiêu:** giảm heap Go sống và thời gian GC khi backlog lớn, giữ nguyên hành vi.
1. **Phân tích (chỉ đọc):** với từng đối tượng đầu bảng heap (`NewExecuteSCResult`, `sync.Map.Swap` trong các cache, `AddTxToCache`, `storeVerifiedBound`, `ReceiptTracker.StoreTxHashConnEntry`): ai cấp phát, vòng đời, khi nào được giải phóng, có bị giữ cho tx *chưa thực thi* không, kích thước mỗi đối tượng. Dùng file heap trong `note/evidence/node_profile_backlog_20261014/profile*/` (`/tmp/p06_bins/pprof -sample_index=inuse_space -top`, `-list`). Kết quả: một bảng "đối tượng / kích thước ước lượng mỗi tx / vì sao còn sống / có thể bỏ hay thu gọn không".
2. **Đề xuất tối thiểu (tài liệu, chưa sửa mã):** 1–3 thay đổi nhỏ nhất có thể (ví dụ cấp phát chậm, thu gọn struct, dùng slice thay map, giải phóng sớm). Với mỗi đề xuất: blast radius, rủi ro với Zero-Fork, cách kiểm chứng. **Dừng và báo cho chủ dự án duyệt thiết kế trước khi viết mã.** (KISS/YAGNI: không thêm tầng trừu tượng.)
3. **Sau khi được duyệt:** sửa, thêm test đơn vị cho hành vi giữ nguyên (kết quả thực thi, receipt, state root không đổi), chạy `go test -race` cho package liên quan và `build_check.sh`.
4. **Thí nghiệm A/B** (ghi `PREREGISTERED.md` trước): `bins_base` so với `bins_opt`, xen kẽ, **ít nhất 8 cặp mỗi mức** ở 1 và 4 triệu tx, thứ tự ngẫu nhiên có seed. Tiêu chí đề xuất (có thể chỉnh *trước* khi chạy): đạt nếu (a) mọi lượt gửi = nhận = commit, parity OK; (b) TPS đầu-cuối ở 4 triệu tx tăng ≥ 3% với p < 0,05 (ghép cặp) và vượt nhiễu A/A; (c) ở 1 triệu tx không giảm quá 2%; (d) profile `--profile-at` cho thấy tỉ lệ GC (`runtime.gcDrain` cum%) hoặc heap sống giảm. Không đạt thì báo "không đạt", đừng đổi tiêu chí.
5. Cập nhật `note/CLAIMS_LEDGER.md` (dòng 16/17 hoặc dòng mới) với trạng thái đúng.

## Việc 2 — Đề xuất thiết kế chặn backlog ở lớp nhận tx (chỉ tài liệu, không sửa mã)
Bản node cũ chặn client nên backlog nhỏ; bản mới nhận hết nên backlog lớn rồi chậm. Viết `note/design_backlog_bound_20261015.md` (so sánh phương án, **không triển khai**): (a) giới hạn mềm theo kích thước mempool đã có `MaxMempoolSize`/admission gate (`execution/pkg/network/admission.go`, `processor/`): hành vi hiện tại khi đầy là gì [ĐỌC CODE]; (b) cơ chế phản hồi ngược (TCP flow control hiện có); (c) ảnh hưởng với client fire-and-forget (khoảng trống nonce nếu bỏ tx; **không được bỏ tx**), với Zero-Fork và với tính tiến triển (không deadlock). Nêu phương án khuyến nghị và câu hỏi cần chủ dự án quyết định. Không đo gì ở việc này trừ khi cần để trả lời một câu hỏi cụ thể (khi đó ghi tiêu chí trước).

## Việc 3 — A/B Raft: binary cũ so với mới
**Câu hỏi:** hiện tượng "Raft sustained dừng tạo block giữa chừng, 19–23% tx không lên chuỗi" (`note/tps_full_measurement_report_20261009.md` mục 5) có do lỗi `HandleConnection` đã sửa không?
1. Dựng binary cũ lai: `git worktree add --detach <dir> f97a2fbf`; trong worktree tạo symlink **chỉ để build** tới `execution/pkg/mvm/c_mvm/build`, `execution/pkg/mvm/linker/build`, `consensus/metanode/target`, `target` của cây chính; `CGO_ENABLED=1 go build -o bins_old/simple_chain ./cmd/simple_chain`. **Trước khi `git worktree remove`: xóa 4 symlink bằng `rm <symlink>` (không có dấu `/` cuối), rồi mới remove**; xác nhận các thư mục build ở cây chính còn nguyên. Ghi rõ đây là bản lai (Go cũ + Rust hiện tại).
2. `PREREGISTERED.md` trước: 6 cặp xen kẽ ngẫu nhiên, sustained `-max-txs 1000000 -duration 300`, `run_raft_counted.py`. Chỉ số: số tx commit/số tx gửi, tốc độ đầu-cuối, replica sống, có dừng tạo block không (dựa vào dòng thời gian block hoặc `*_events.txt`).
3. Báo kết quả đúng như đo; nếu bản cũ cũng không dừng thì ghi "không tái hiện hiện tượng cũ trong phạm vi này" thay vì gán nguyên nhân.

## Việc 4 — Phân rã chi phí phía Rust/FFI (có điều kiện)
`runtime.cgocall` chiếm 22–35% CPU của validator nhưng profile Go không cho thấy bên trong. Kiểm tra `which perf`; nếu có thể lấy mẫu stack tiến trình của chính bạn **không cần sudo** (`perf record -g -p <pid>` hoặc `perf_event_paranoid` cho phép), lấy mẫu validator 0 trong lúc xả backlog 4 triệu tx, ký hiệu hóa khung Rust. Nếu **không** làm được nếu không có quyền quản trị, ghi "bị chặn: cần quyền X" và dừng việc này (không dùng sudo, không đòi mật khẩu).

## Việc 5 — Dọn dẹp và báo cáo
- Cập nhật `note/CLAIMS_LEDGER.md`, `PROJECT_STRUCTURE.md` (nếu thêm file/công cụ), sao lưu evidence nhỏ vào `note/evidence/<tên>_20261015/` (không đưa log lớn không cần thiết; bỏ binary).
- Dọn: dừng cụm, `rm -rf /tmp/gate_4val_tps_counted /tmp/raft_counted_cluster`, gỡ worktree (đúng quy trình ở Việc 3), kiểm `ss -ltn | grep -c ":31[0-9][0-9][0-9]"` = 0.
- **Tự kiểm tra cuối (bắt buộc):** chạy lại độc lập ít nhất một con số quan trọng nhất của báo cáo (ví dụ một cặp A/B) hoặc tính lại một thống kê từ file thô; ghi kết quả vào báo cáo. Chạy lại `verify_evidence --strict` và `claims_lint` trên mọi báo cáo.

## Ngoài phạm vi (không làm)
- Panic NOMT `failed to commit payload` (ledger dòng 15): đang chờ lần xảy ra tiếp theo (đã bật log đầy đủ chuỗi nguyên nhân); không sửa mã NOMT.
- Thu hồi token GitHub, đổi mật khẩu sudo trong repo, quyết định relayer/cutover chain 991/bounded-index: chỉ chủ dự án làm.
- Push/PR/merge.

## Tiêu chí hoàn thành
- Mỗi việc có trạng thái rõ: **Xong (có evidence qua verify --strict)**, **Không đạt (kết quả đo được, ghi nguyên văn)**, hoặc **Chưa làm được (lý do cụ thể)**. Không có việc nào ở trạng thái mơ hồ.
- Mọi con số trong báo cáo truy được tới tệp thô trong `note/evidence/`.
- Cây git sạch; commit theo tên file; không push.
- Một khối "📋 Tóm tắt thay đổi" tiếng Việt cuối báo cáo, nêu cả những gì **không** làm được.
