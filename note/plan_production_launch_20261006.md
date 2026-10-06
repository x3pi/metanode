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
### 2.1 Metric + health
- Xuất trạng thái khớp/lệch khoá ra metric hoặc endpoint health đã có của node (tìm cơ chế metric hiện hữu trong `cmd/simple_chain`/`pkg/` trước khi tạo mới; không đưa vào đường nóng). Thêm: số đăng ký PENDING và tuổi PENDING lâu nhất (từ `RegistrationRelay`), số attestation đang chờ (đếm khoá pending trong contract storage), số chữ ký bị từ chối theo lý do (non-committee/sai digest/sai độ dài), số lần đọc committee lỗi, chain ID parent lệch.
### 2.2 Kiểm tra ở bước deploy (ansible)
- Thêm bước trong `deploy/ansible_clusters` (role `exec_cluster`): với mỗi validator, tính public key từ `bls_priv` (dùng `execution/cmd/tool/bls_pubkey`) và so với `publicKeyBls` đã ghi trong genesis alloc của địa chỉ validator; FAIL deploy nếu lệch. Test bằng inventory mẫu: cố ý cấu hình sai ⇒ deploy phải dừng với thông báo rõ.
### 2.3 Tài liệu
- Mục "Yêu cầu khoá validator" trong runbook (giai đoạn 3) kèm cách sinh khoá và đưa vào genesis.
**Xong khi:** cấu hình sai bị chặn ở deploy VÀ phát cảnh báo ở runtime; metric đọc được; có test cho cả hai.

## GIAI ĐOẠN 3 — Cutover chain ID 991 + định dạng payload proto (chặn B3)
**Vì sao:** đổi chain ID (990→991 ở parent) và payload JSON→proto làm node cũ/mới không tương thích; chữ ký cũ vô hiệu. Cần cutover đồng thời toàn mạng (wipe + redeploy).
### 3.1 Dựng từ template ansible thật, KHÔNG từ gen_env.py
- Dùng `deploy/ansible_clusters` với inventory mẫu trỏ vào localhost (hoặc container/VM cục bộ nếu có) để dựng: parent + ≥2 cụm exec (1 cụm 4 validator Mysticeti + 1 cụm Raft). Nếu ansible không chạy được cục bộ, mô phỏng đúng các bước template (render `exec_config.json.j2`, `parent_genesis.json.j2`, systemd unit) và ghi rõ chỗ khác biệt.
- Kiểm tra: chain ID parent = chain ID mọi cụm (`/status`, `mtn_getClusterIdentity`), khoá BLS của chính các node exec đã có float/đăng ký trong genesis parent (ghi chú cũ "possible production genesis gap — exec nodes' own BLS key registration — chưa kiểm": xác nhận bằng cách chạy luồng đăng ký + chuyển xuyên cụm trên cụm dựng từ template), `tx_signature_mode=secp`, `account_gate=parent_registered`.
### 3.2 Runbook `note/runbook_cutover_chain991_proto.md`
- Thứ tự: thông báo/đóng cổng → dừng toàn bộ (exec trước, parent sau; theo thư mục làm việc, không theo cổng) → backup (đường dẫn, kiểm tra backup đọc được) → wipe dữ liệu parent+exec → deploy binary mới + genesis mới → khởi động parent → khởi động exec → kiểm tra (danh sách lệnh + kết quả mong đợi) → mở cổng. Kèm: kịch bản rollback (khôi phục backup + binary cũ), tiêu chí go/no-go từng bước, thời gian dự kiến, ai làm gì.
- **Lưu ý đặc biệt:** wipe là phá hủy dữ liệu — runbook chỉ viết, KHÔNG thực thi trên cụm thật; mọi bước phá hủy phải được user xác nhận từng lần.
### 3.3 Diễn tập
- Chạy toàn bộ runbook trên cụm cô lập ở 3.1, đo thời gian, ghi các chỗ vấp. Diễn tập cả rollback.
**Xong khi:** runbook được diễn tập đầy đủ (kể cả rollback) trên cụm dựng từ template; sau cutover chạy lại E2E gate 14/14 + co-attestation 4 validator + cross-chain.

## GIAI ĐOẠN 4 — Nhiều máy thật (chặn B2)
- **Điều kiện tiên quyết:** user chỉ định máy/cụm test riêng bằng văn bản. Không dùng 231/230 trừ khi user nói rõ.
- Chạy lại 3 suite (gate 14, co-attestation 4 validator, cross-chain) với validator rải trên ≥2 máy: thêm độ trễ/mất gói (`tc netem`), tắt máy đột ngột, mất kết nối giữa máy trong 10 phút rồi nối lại, parent mất 10 phút rồi quay lại.
- Kỳ vọng: không fork, không double-credit, PENDING tự hoàn tất khi đủ quorum; không cần can thiệp tay.
- Soak test ≥24 giờ với tải thấp liên tục (đăng ký + chuyển xuyên cụm định kỳ), theo dõi RSS, số goroutine, kích thước DB, độ lệch state root.
**Xong khi:** báo cáo nhiều máy + soak 24h không lỗi, không rò rỉ tài nguyên.

## GIAI ĐOẠN 5 — Bảo mật & deploy (chặn B5)
5.1 **Issue #103** (`SKIP_MEMPOOL_SIG_VERIFY`): xác minh trên deploy thật (template ansible `deploy/ansible*`, `mtn-orchestrator.sh`): production KHÔNG bật; `sigVerifyBypassedForDevnet` chỉ có hiệu lực khi `METANODE_DEVNET=true` và không ở production; thêm kiểm tra khởi động (đã gợi ý ở `SEQUENCER_ERC20_STANDARD_TX_PROOF.md` F0-5) từ chối chạy chế độ raft/privacy nếu cờ bật. Test: bật cờ + `METANODE_ENV=production` ⇒ cờ bị bỏ qua và có log cảnh báo. Xác nhận tx ký sai bị loại ở exec filter (không chỉ mempool).
5.2 **Issue #104** (mật khẩu): inventory mẫu hiện có chuỗi `!vault` GIẢ — thay bằng placeholder rõ ràng + hướng dẫn tạo vault thật; xác nhận `deploy_clusters.sh` chạy được với vault thật (tự tạo vault tạm để test). Không có mật khẩu plaintext trong repo (`git grep`).
5.3 **Issue #105** (`parse_inventory.py`): xác nhận không còn chế độ in khoá riêng; kiểm các consumer của file xuất (`bls_private_key` trong các tool) không vỡ — nếu vỡ, sửa consumer đọc khoá từ nguồn an toàn.
5.4 Rà soát bí mật: `git grep` các khoá/mật khẩu mẫu cứng trong template production (`pk_admin_file_storage`, `master_password: devnet-test-password`, `app_pepper` trong `exec_config.json.j2` đang là giá trị devnet) ⇒ bắt buộc đổi qua biến vault khi production; deploy phải FAIL nếu còn giá trị mặc định devnet khi `metanode_env=production`.
5.5 Cập nhật GitHub issues #103–#105 bằng `gh` chỉ khi user cho phép (hành động hướng ra ngoài).
**Xong khi:** `git grep` sạch; deploy production từ chối giá trị devnet; test chứng minh cờ bypass vô hiệu.

## GIAI ĐOẠN 6 — Tải, độ bền, quan sát (P1-5/P1-6)
- 10.000 đăng ký đồng thời: relay giới hạn 4096 theo dõi; `ErrRegistrationBusy` phải lan ra RPC đúng mã và client thử lại được; không mất yêu cầu đã trả PENDING.
- Restart giữa tải (kill -9 + stop thường), parent mất 10 phút, 1/4 validator mất liên tục khi tải.
- Dashboard/cảnh báo gợi ý (tài liệu, không bắt buộc cài): PENDING age > ngưỡng, lệch khoá committee, chain ID lệch, đọc committee lỗi, số block không tiến.
- Đánh giá `GetAllValidators` (top 21, phụ thuộc NOMT, gọi mỗi tx hệ thống): đo chi phí dưới tải; nếu đáng kể, đề xuất cache committee theo epoch ở `ChainState` (deterministic: chỉ cập nhật tại ranh giới epoch) — thiết kế + test trước khi sửa, vì đây là đường deterministic.
**Xong khi:** báo cáo tải với số liệu; không mất yêu cầu/không double-credit trong mọi kịch bản.

## GIAI ĐOẠN 7 — Hạng mục đánh giá còn lại (P1-3/P1-4)
- **Legacy chain `bls_legacy`:** chưa có chain-binding ở exec filter cho giao dịch không phải 0xFF (plan §9). Chỉ ĐÁNH GIÁ rủi ro (kịch bản replay giữa các legacy chain cùng chain ID) và đề xuất; không đổi hành vi dapp cũ khi chưa có chấp thuận.
- **MVM `creatorPublicKey`:** chạy test deploy contract THẬT (EVM) từ tài khoản chỉ có secp trên cụm cô lập; đọc nhánh C++ MVM (`pkg/mvm`) để chắc chắn không dùng khoá BLS của người tạo; ghi kết quả.
- **Dự phòng phase 2 (tài khoản tạm khi mất parent):** KHÔNG làm; user đã chọn chặn hẳn/chờ parent.

## GIAI ĐOẠN 8 — Phát hành
1. Đóng băng: chỉ nhận sửa lỗi; chạy lại TOÀN BỘ: unit `-race`, `build_check.sh`, gate 14, restart_durability, co-attestation 4 validator, cross-chain 26 (mỗi suite ≥3 lần liên tiếp trên worktree sạch).
2. Tổng hợp `note/production_go_nogo_<ngày>.md`: checklist bên dưới, mỗi dòng kèm lệnh + commit + đường dẫn output.
3. Quy trình git (CHỈ khi user yêu cầu): push nhánh, mở PR, review, merge; sau merge `git show origin/dev:<path>` cho các file trọng yếu (`consensus/vendor/nomt/**`, `execution/pkg/rollup/**`, `execution/pkg/proto/rollup*.go`, `deploy/ansible_clusters/**`) để chắc không mất commit; tag phát hành.
4. Triển khai production theo runbook của giai đoạn 3 với sự có mặt/xác nhận của user ở từng bước phá hủy.

## Checklist go/no-go (tất cả phải ✅ có bằng chứng)
- [ ] G1 Perf: báo cáo 3 cấu hình × 3 lần; không tụt quá ngưỡng đã thống nhất; quyết định ghi rõ; fsync NOMT bật.
- [ ] G1 Kill -9 dưới tải ≥10 lần: 0 lệch state root, 0 exit 78.
- [ ] G2 Deploy chặn khoá validator sai + metric/cảnh báo runtime.
- [ ] G3 Runbook cutover diễn tập đủ (kể cả rollback) trên cụm dựng từ template ansible; E2E sau cutover xanh.
- [ ] G4 Nhiều máy: 3 suite + kịch bản mạng xấu + soak 24h.
- [ ] G5 #103/#104/#105 đóng; không còn giá trị devnet trong deploy production; `git grep` sạch bí mật.
- [ ] G6 Tải 10k đăng ký + restart giữa tải + parent mất 10 phút: không mất/trùng.
- [ ] G7 Đánh giá legacy chain + test deploy secp thật + quyết định ghi lại.
- [ ] G8 Toàn bộ suite ≥3 lần liên tiếp trên worktree sạch; `go test -race` + `build_check.sh` sạch; không còn commit chưa review; user phê duyệt.

## Khi nào dừng và hỏi user
- Cần máy ngoài/cụm thật, thực thi bước phá hủy (wipe/redeploy), push/merge, đóng issue GitHub, hoặc đổi hành vi legacy chain.
- Phát hiện cần đổi giao thức/định dạng ngoài phạm vi, hoặc kết quả benchmark cho thấy phải đánh đổi giữa độ bền và hiệu năng (quyết định này thuộc về user).
- Bất kỳ thay đổi nào trên đường commit/consensus có thể ảnh hưởng bất biến zero-fork.
