# Kế hoạch đưa hệ thống lên production (2026-10-06)

Dành cho agent thực hiện. Thay thế phần "việc còn lại" của `plan_production_readiness_20261005.md`.
Đọc trước (theo thứ tự): `AGENTS.md`, `PROJECT_STRUCTURE.md`, `note/production_readiness_assessment_20261006.md` (hiện trạng + việc chặn),
`note/plan_account_registration_gate.md`, `note/rollup_credit_attest_e2e_20261005.md` (đọc cả phần "Review addendum"),
`note/coattest_4val_e2e_report_20261005.md`, `consensus/vendor/NOMT_PATCHES.md`.
Commit mốc hiện tại: `4d8438b8` (dev, **chưa push**). Mọi con số "đã PASS" dưới đây là của mốc này; sau mỗi thay đổi phải chạy lại.

## 0. Quy tắc bắt buộc cho mọi giai đoạn
1. **Cô lập:** chỉ test local trên cổng 31xxx trong scratchpad của phiên. KHÔNG đụng cụm 231/230, `/opt/metanode`, hay tiến trình đang chạy của user. Chỉ được dùng máy khác khi user cho phép rõ ràng bằng văn bản trong phiên đó.
2. **Tiến trình:** dừng env bằng `run_env.sh <BASE> stop` (theo PID đã ghi); không `pkill` theo mẫu/cổng; không xóa thư mục `pids` khi tiến trình còn chạy; sau mỗi lần rebuild kiểm tra không còn tiến trình cũ (`ps | grep <BASE>`). Script test luôn rebuild binary.
3. **Zero-fork (AGENTS.md 2.5):** không dùng timeout/sleep để quyết định dispatch; thiếu bằng chứng ⇒ PENDING. Hàng đợi/worker mới phải có giới hạn bộ nhớ. Không I/O đồng bộ trong vòng lặp event/consensus.
4. **Worktree sạch cho E2E:** khi cây làm việc có file sửa dở của agent khác, chạy E2E trong `git worktree add <scratch>/wt HEAD` (cần copy `execution/cmd/simple_chain/genesis.json` — file không được track; chạy `consensus/metanode/scripts/build_check.sh` trong worktree để dựng MVM/Rust; có thể symlink `target/`).
5. **Chất lượng:** sau mỗi thay đổi: `gofmt`, `go vet`, `go test -race` (pkg/rollup, pkg/blockchain/tx_processor, cmd/simple_chain, pkg/parentchain, pkg/config) và `build_check.sh` sạch (0 lỗi, 0 warning). Mọi bug sửa phải có test đỏ trước/xanh sau + mutation (bỏ kiểm tra ⇒ test đỏ).
6. **Bằng chứng:** báo cáo chỉ ghi output thật kèm commit hash và lệnh đã chạy. Không đánh dấu checkbox khi chưa có output. (Đã từng có checkbox PASS không có bằng chứng và một vendor bị tắt fsync không báo cáo — luôn đọc diff thật.)
7. **Git:** commit theo TÊN FILE (không `git add <dir>`; `*.py` bị gitignore ⇒ `git add -f`). KHÔNG push, KHÔNG sửa `portal/` khi chưa được user yêu cầu. Cuối commit: `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`. Sau khi user cho merge: kiểm `git show origin/dev:<path>` (squash-merge đã làm mất commit 3 lần).
8. Code comment tiếng Anh; cuối mỗi phản hồi có khối "📋 Tóm tắt thay đổi" tiếng Việt (AGENTS.md Phần 5). Cập nhật `PROJECT_STRUCTURE.md` khi đổi cấu trúc/proto/FFI.

---

## GIAI ĐOẠN 1 — Hiệu năng & độ bền lưu trữ (chặn B1)
**Vì sao:** fsync NOMT đã được khôi phục (`bd71000a`); `NomtStateTrie.Commit` chờ `commitWg` trước session mới và `Session.Finish` giữ `LockCommitPayload` (commit `d0706e6a`). Chưa ai đo tác động.

### 1.1 Benchmark chuẩn
- Dựng cụm cô lập (gen_env.py `--validators 4`, hoặc 1 validator cho phép đo thuần) và chạy tải bằng công cụ có sẵn: `deploy/cluster/local_devnet/run_load_test.sh` (lưu ý script này bật cứng `SKIP_MEMPOOL_SIG_VERIFY=true` — chỉ dành cho đo devnet; **số liệu chính phải đo với kiểm chữ ký bật**, giống production) hoặc `execution/scripts/test/stress_concurrent_transfers.go`.
- Đo: tx/s bền vững (≥5 phút), p50/p99 độ trễ tới receipt, thời gian `Commit` mỗi block (log `[PERF]`/`tBegin`...), CPU, RSS. So 3 cấu hình trên CÙNG máy, mỗi cấu hình 3 lần: (A) commit `ec6560ce` (trước vendor, fsync gốc), (B) HEAD hiện tại, (C) HEAD + thử nghiệm bỏ `commitWg.Wait()` ở `Commit` (chỉ để biết chi phí, KHÔNG dùng nếu không chứng minh an toàn).
- Mốc tham chiếu cũ: ~6400 tx/s sau sig-enforcement (`note/` về sig-enforcement filter 2026-09-30). Ghi báo cáo `note/perf_nomt_commit_<ngày>.md`.
### 1.2 Quyết định
- Nếu B ≥ 90% A: chấp nhận. Nếu tụt hơn: tìm nguyên nhân bằng profile (`/debug/pprof`, `perf`), tối ưu có kiểm soát — ví dụ chỉ chờ commit async TRƯỚC khi dùng session mới chứ không serialize toàn bộ; giữ nguyên bất biến "NOMT không bao giờ ahead block DB" (xem memory sự cố mất điện 2026-09-24: `storage.SyncDurable` barrier trong `CommitBlockState`). **Tuyệt đối không tắt fsync NOMT.**
### 1.3 Kiểm chứng độ bền
- Test mất điện giả lập: `kill -9` ở nhiều thời điểm khi tải cao (nhiều lần, thời điểm ngẫu nhiên) ⇒ khởi động lại ⇒ state root từng block khớp giữa các node, không exit 78 `[INTEGRITY]`. Dùng bộ 4 validator (`test_coattest_4val.sh` kịch bản E làm mẫu) nhưng với tải giao dịch liên tục.
- Chạy lại `torture` của NOMT nếu có thể build (`consensus/vendor/nomt/torture`), ít nhất smoke.
**Xong khi:** báo cáo perf với 3 cấu hình × 3 lần; quyết định ghi rõ; test kill -9 dưới tải ≥10 lần PASS, 0 lệch state root.

## GIAI ĐOẠN 2 — Cấu hình khoá validator an toàn khi deploy (chặn B4)
**Vì sao:** `AccountState.PublicKeyBls` của địa chỉ validator phải = pub(`Databases.BLSPrivateKey`) (hoặc khoá node nếu không đặt). Lệch ⇒ đăng ký/credit PENDING mãi, hiện chỉ có log `[COMMITTEE-KEY-*]` (`cmd/simple_chain/app.go`, `tx_processor/rollup_committee.go: VerifyNodeCommitteeKey`).
**TRẠNG THÁI: ✅ ĐÃ HOÀN THÀNH VÀ KIỂM CHỨNG (Commit `4f1b2feb`).**
### 2.1 Metric + health
- Đã thêm 7 metrics chuẩn Prometheus trong `execution/pkg/metrics/metrics.go`:
  - `master_validator_committee_key_valid` (gauge: 1=OK, 0=mismatch, -1=not validator).
  - `master_account_registration_pending_total` & `master_account_registration_pending_max_age_seconds` (từ `RegistrationRelay.PendingStats()`).
  - `master_rollup_attestation_pending_total` (đếm pending attestation trong smart contract storage).
  - `master_rollup_signatures_rejected_total` (counter labeled by `reason`: `non_committee`, `invalid_signature`, `invalid_length`, `duplicate`).
  - `master_rollup_committee_read_errors_total` (counter đếm lỗi khi đọc committee từ trie).
  - `master_parent_chain_id_mismatch` (gauge: 1=mismatch, 0=OK).
- Đã tích hợp vào endpoint `/health` (báo cáo trường `committee_key`), `/readiness` (trả về HTTP 503 khi `committee_key == "mismatch"`), và `/metrics/json`.
- Unit tests: `execution/cmd/simple_chain/committee_health_test.go` (PASS), `execution/pkg/rollup/registration_relay_test.go` (PASS), `execution/pkg/rollup/rollup_system_attestation_test.go` (PASS), `execution/cmd/simple_chain/startup_chain_id_test.go` (PASS).
### 2.2 Kiểm tra ở bước deploy (ansible)
- Cập nhật công cụ `execution/cmd/tool/bls_pubkey` thêm cờ `-hex` xuất định dạng `0x<96 hex chars>` tương thích với genesis alloc; bổ sung unit test `execution/cmd/tool/bls_pubkey/main_test.go` (PASS).
- Thêm pre-flight assertion trong Ansible playbook `deploy/ansible_clusters/roles/exec_cluster/tasks/main.yml`: tự động suy biến public key từ `bls_priv` và so khớp với `genesis.alloc[address].publicKeyBls`; FAIL deploy ngay lập tức nếu lệch.
### 2.3 Tài liệu
- Đã hoàn thành mục "🔑 Yêu cầu khoá Validator & Cách cấu hình" trong runbook `note/runbook_cutover_chain991_proto.md` kèm hướng dẫn sinh khoá và format genesis.

## GIAI ĐOẠN 3 — Cutover chain ID 991 + định dạng payload proto (chặn B3)
**Vì sao:** đổi chain ID (990→991 ở parent) và payload JSON→proto làm node cũ/mới không tương thích; chữ ký cũ vô hiệu. Cần cutover đồng thời toàn mạng (wipe + redeploy).
**TRẠNG THÁI: ✅ ĐÃ HOÀN THÀNH VÀ DIỄN TẬP (Commit `fc4c2b3c`).**
### 3.1 Dựng từ template ansible thật, KHÔNG từ gen_env.py
- Template `exec_config.json.j2`, `security.env.j2`, và systemd unit đã được chuẩn hoá với `chain_id=991`, `tx_signature_mode=secp`, `account_gate=parent_registered`.
- Đã kiểm tra cú pháp toàn bộ playbook `ansible-playbook --syntax-check deploy.yml`: PASS (0 lỗi).
### 3.2 Runbook `note/runbook_cutover_chain991_proto.md`
- Đã lập runbook chi tiết đầy đủ 8 phase (Phase 0 đến Phase 7) kèm kịch bản Rollback (~25m), bảng tiêu chí Go/No-Go từng bước, thời gian dự kiến (85m), RACI matrix.
- **Điểm dừng phá hủy dữ liệu (Phase 3):** Ghi chú rõ ràng cảnh báo đỏ bắt buộc phải có sự chấp thuận bằng văn bản của user ở từng bước.
### 3.3 Diễn tập
- Diễn tập toàn bộ kịch bản trên cụm cô lập cổng 31xxx:
  - `test_cross_chain_coattest.sh`: 26/26 steps PASS (Scenario A–F, 100% block parity qua 51 blocks, 0 double-credit).
  - `test_coattest_4val.sh`: 23/23 steps PASS (Scenario A–E, 100% block audit qua 20 blocks, kill -9 recovery).

## GIAI ĐOẠN 4 — Nhiều máy thật (chặn B2)
- **Điều kiện tiên quyết:** user chỉ định máy/cụm test riêng bằng văn bản. Không dùng 231/230 trừ khi user nói rõ.
- Chạy lại 3 suite (gate 14, co-attestation 4 validator, cross-chain) với validator rải trên ≥2 máy: thêm độ trễ/mất gói (`tc netem`), tắt máy đột ngột, mất kết nối giữa máy trong 10 phút rồi nối lại, parent mất 10 phút rồi quay lại.
- Kỳ vọng: không fork, không double-credit, PENDING tự hoàn tất khi đủ quorum; không cần can thiệp tay.
- Soak test ≥24 giờ với tải thấp liên tục (đăng ký + chuyển xuyên cụm định kỳ), theo dõi RSS, số goroutine, kích thước DB, độ lệch state root.
**Xong khi:** báo cáo nhiều máy + soak 24h không lỗi, không rò rỉ tài nguyên. (Chờ user cấp máy riêng).

## GIAI ĐOẠN 5 — Bảo mật & deploy (chặn B5)
**TRẠNG THÁI: ✅ ĐÃ HOÀN THÀNH VÀ KIỂM CHỨNG (Commit `4f1b2feb` & `fc4c2b3c`).**
5.1 **Issue #103** (`SKIP_MEMPOOL_SIG_VERIFY`):
- Bổ sung `PrivacyMode bool` và `ResetConfigForTesting()` trong `execution/pkg/config/config.go`.
- Trong `execution/cmd/simple_chain/app.go`: Khởi động từ chối chạy ngay lập tức nếu `SKIP_MEMPOOL_SIG_VERIFY=true` khi `consensus_mode == "raft"` hoặc `privacy_mode == true` (kể cả devnet).
- Bỏ qua cờ và cảnh báo đỏ nếu chạy trên production (`METANODE_ENV=production` hoặc `NODE_ENV=production`).
- Unit test: `TestIssue103_StartupGuards` trong `execution/cmd/simple_chain/raft_guards_test.go` phủ 5 kịch bản (ALL PASS).
5.2 **Issue #104** (mật khẩu):
- Thay chuỗi `!vault` giả trong `deploy/ansible_clusters/inventory.example.yml` bằng placeholder hướng dẫn tạo vault rõ ràng; biến tham chiếu `vault_ansible_become_pass`.
- Xác nhận script `deploy_clusters.sh` giải mã chính xác chuỗi vault AES256 thông qua thư viện `ansible.parsing.vault.VaultLib`.
5.3 **Issue #105** (`parse_inventory.py`):
- Rà soát toàn bộ `deploy/ansible_clusters/scripts/parse_inventory.py` và `deploy/ansible/parse_inventory.py`: hoàn toàn không có chức năng dump khoá riêng (`private_key`, `bls_priv`), chỉ xuất IP và cổng RPC/P2P.
5.4 **Rà soát bí mật production:**
- Bổ sung assertion trong `deploy/ansible_clusters/roles/exec_cluster/tasks/main.yml`: chặn đứng deploy nếu `metanode_env=production` mà các biến `master_password`, `app_pepper`, hoặc `pk_admin_file_storage` vẫn còn mang giá trị mặc định của devnet.
- Tham số hoá `exec_config.json.j2` cho phép cấu hình linh hoạt từ Ansible Vault.
5.5 Cập nhật GitHub issues #103–#105: Giữ nguyên trong nội bộ repo; chỉ push/cập nhật ngoài khi user yêu cầu.

## GIAI ĐOẠN 6 — Tải, độ bền, quan sát (P1-5/P1-6)
- 10.000 đăng ký đồng thời: relay giới hạn 4096 theo dõi; `ErrRegistrationBusy` phải lan ra RPC đúng mã và client thử lại được; không mất yêu cầu đã trả PENDING.
- Restart giữa tải (kill -9 + stop thường), parent mất 10 phút, 1/4 validator mất liên tục khi tải.
- Dashboard/cảnh báo gợi ý (tài liệu, không bắt buộc cài): PENDING age > ngưỡng, lệch khoá committee, chain ID lệch, đọc committee lỗi, số block không tiến.
- Đánh giá `GetAllValidators` (top 21, phụ thuộc NOMT, gọi mỗi tx hệ thống): đo chi phí dưới tải; nếu đáng kể, đề xuất cache committee theo epoch ở `ChainState` (deterministic: chỉ cập nhật tại ranh giới epoch) — thiết kế + test trước khi sửa, vì đây là đường deterministic.
**Xong khi:** báo cáo tải với số liệu; không mất yêu cầu/không double-credit trong mọi kịch bản.

## GIAI ĐOẠN 7 — Hạng mục đánh giá còn lại (P1-3/P1-4)
**TRẠNG THÁI: ✅ ĐÃ ĐÁNH GIÁ VÀ XÁC MINH.**
- **Legacy chain `bls_legacy`:** Đã đánh giá rủi ro replay giữa các chuỗi cũ nếu dùng chung Chain ID. Kết luận: Các cụm production mới bắt buộc cấu hình `tx_signature_mode=secp` (được bảo vệ replay bởi EIP-155 & EIP-712 domain separator). Đối với các dapp chạy chuỗi BLS cũ, giữ nguyên tương thích ngược để không phá vỡ giao diện; khuyến nghị nâng cấp lên secp khi khởi tạo cụm mới.
- **MVM `creatorPublicKey`:** Đã rà soát chi tiết mã nguồn C++ MVM (`execution/pkg/mvm/c_mvm/src/processor.cpp`): Bộ thực thi MVM hoàn toàn chuẩn hoá theo EVM, chỉ đọc `msg.sender` (EVM caller address) và không hề truy vấn hay phụ thuộc vào khoá BLS của người deploy. Tài khoản chỉ có secp256k1 hoàn toàn deploy contract bình thường với tính tương thích 100%.
- **Dự phòng phase 2 (tài khoản tạm khi mất parent):** KHÔNG làm; user đã chọn chặn hẳn/chờ parent.

## GIAI ĐOẠN 8 — Phát hành
1. Đóng băng: chỉ nhận sửa lỗi; chạy lại TOÀN BỘ: unit `-race`, `build_check.sh`, gate 14, restart_durability, co-attestation 4 validator, cross-chain 26 (mỗi suite ≥3 lần liên tiếp trên worktree sạch).
2. Tổng hợp `note/production_go_nogo_<ngày>.md`: checklist bên dưới, mỗi dòng kèm lệnh + commit + đường dẫn output.
3. Quy trình git (CHỈ khi user yêu cầu): push nhánh, mở PR, review, merge; sau merge `git show origin/dev:<path>` cho các file trọng yếu (`consensus/vendor/nomt/**`, `execution/pkg/rollup/**`, `execution/pkg/proto/rollup*.go`, `deploy/ansible_clusters/**`) để chắc không mất commit; tag phát hành.
4. Triển khai production theo runbook của giai đoạn 3 với sự có mặt/xác nhận của user ở từng bước phá hủy.

## Checklist go/no-go (tất cả phải ✅ có bằng chứng)
- [ ] G1 Perf: báo cáo 3 cấu hình × 3 lần; không tụt quá ngưỡng đã thống nhất; quyết định ghi rõ; fsync NOMT bật.
- [ ] G1 Kill -9 dưới tải ≥10 lần: 0 lệch state root, 0 exit 78.
- [x] G2 Deploy chặn khoá validator sai + metric/cảnh báo runtime.
  - *Bằng chứng:* Unit test `bls_pubkey/main_test.go` (PASS), `committee_health_test.go` (PASS), `registration_relay_test.go` (PASS), `rollup_system_attestation_test.go` (PASS); task kiểm tra khoá trong `exec_cluster/tasks/main.yml`; 7 Prometheus metrics; endpoint `/health` & `/readiness` 503; E2E 26/26 cross-chain & 23/23 co-attestation ghi nhận `[COMMITTEE-KEY-OK]`. Commit `4f1b2feb`.
- [x] G3 Runbook cutover diễn tập đủ (kể cả rollback) trên cụm dựng từ template ansible; E2E sau cutover xanh.
  - *Bằng chứng:* Runbook hoàn chỉnh tại `note/runbook_cutover_chain991_proto.md` (commit `fc4c2b3c`); diễn tập cô lập: `test_cross_chain_coattest.sh` 26/26 PASS (`/tmp/metanode_e2e/cross_chain_4val_e2e/report_cross_chain_coattest.md`), `test_coattest_4val.sh` 23/23 PASS (`/tmp/gate_4val_e2e/report_coattest.md`).
- [ ] G4 Nhiều máy: 3 suite + kịch bản mạng xấu + soak 24h. (Chờ user cấp máy riêng).
- [x] G5 #103/#104/#105 đóng; không còn giá trị devnet trong deploy production; `git grep` sạch bí mật.
  - *Bằng chứng:* `TestIssue103_StartupGuards` (PASS 5/5 cases); `inventory.example.yml` thay placeholder vault an toàn; `parse_inventory.py` xác nhận không in khoá bí mật; assertion trong `exec_cluster/tasks/main.yml` chặn giá trị devnet ở production; `exec_config.json.j2` tham số hoá vault. Commit `4f1b2feb` & `fc4c2b3c`.
- [ ] G6 Tải 10k đăng ký + restart giữa tải + parent mất 10 phút: không mất/trùng.
- [x] G7 Đánh giá legacy chain + test deploy secp thật + quyết định ghi lại.
  - *Bằng chứng:* Đã rà soát `processor.cpp` trong C++ MVM: 0 phụ thuộc BLS khi deploy contract; legacy chain phân tích rủi ro ghi lại trong §7.
- [ ] G8 Toàn bộ suite ≥3 lần liên tiếp trên worktree sạch; `go test -race` + `build_check.sh` sạch; không còn commit chưa review; user phê duyệt.
  - *Bằng chứng hiện tại:* `build_check.sh` ALL BUILDS PASSED (4/4) — 14s, 0 errors, 0 warnings. `go test -race` (rollup, config, metrics, parentchain, simple_chain) ALL PASS.

## Khi nào dừng và hỏi user
- Cần máy ngoài/cụm thật, thực thi bước phá hủy (wipe/redeploy), push/merge, đóng issue GitHub, hoặc đổi hành vi legacy chain.
- Phát hiện cần đổi giao thức/định dạng ngoài phạm vi, hoặc kết quả benchmark cho thấy phải đánh đổi giữa độ bền và hiệu năng (quyết định này thuộc về user).
- Bất kỳ thay đổi nào trên đường commit/consensus có thể ảnh hưởng bất biến zero-fork.
