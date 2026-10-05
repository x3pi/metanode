# Kế hoạch đưa account gate + cross-chain lên production (2026-10-05)

Dành cho agent khác. Đọc trước: `AGENTS.md`, `PROJECT_STRUCTURE.md`, `note/plan_account_registration_gate.md` (§9, §10), `note/plan_next_steps_20261005.md` (mục A, A2, C+), `note/account_gate_e2e_20261005.md`. Commit mới nhất liên quan: `108afa0f`, `f6565269` (dev, chưa push).

## Quy tắc (bắt buộc)
- Chỉ test local trên môi trường cô lập (cổng 31xxx, thư mục scratchpad). KHÔNG đụng cụm 231/230, `/opt/metanode`, hay cụm đang chạy của user. Luôn dừng env bằng `run_env.sh <BASE> stop`; không xóa thư mục `pids` khi tiến trình còn chạy; sau khi rebuild binary, kiểm tra không còn tiến trình cũ (`ps | grep <BASE>/bin`).
- Zero-fork (AGENTS.md 2.5): không dùng timeout/sleep để quyết định dispatch; thiếu bằng chứng thì giữ PENDING. Mọi hàng đợi/worker mới phải có giới hạn bộ nhớ.
- Sau mỗi thay đổi: `gofmt`, `go vet`, `go test -race`, `consensus/metanode/scripts/build_check.sh` sạch, thêm mutation test (bỏ kiểm tra ⇒ test phải đỏ). Tự chạy lại và đọc output thật; không tin báo cáo agent khác.
- Commit theo tên file (không `git add <dir>`; `*.py` bị gitignore ⇒ `git add -f`). Không push khi user chưa yêu cầu. Khối "📋 Tóm tắt thay đổi" tiếng Việt ở cuối mỗi báo cáo. Không làm hộ các file `portal/` đang sửa dở.

## P0 — chặn production (làm theo thứ tự)

### P0-1. Môi trường ≥4 validator để kiểm co-attestation thật
Hiện chỉ có cụm 1 validator (f+1=1) nên cơ chế cộng dồn mới được kiểm ở unit/integration, chưa ở cụm thật.
- Dựng cụm cô lập 4 validator (consensus Mysticeti, không phải Raft 1 node): mở rộng `execution/scripts/test/gate_e2e/gen_env.py` (thêm `--validators 4`) hoặc dùng fixture sẵn có nếu chạy được (đọc `note/` về `private_chain_3node`; lưu ý ghi nhận cũ: fixture đó từng hỏng ở đăng ký BLS pubkey — kiểm lại trước khi tin). Mỗi validator: địa chỉ riêng, `PublicKeyBls` (alloc genesis) = public key của `Databases.BLSPrivateKey` của chính node đó (dùng `cmd/tool/bls_pubkey`), stake > 0.
- Kịch bản bắt buộc: (a) đăng ký tài khoản ⇒ CONFIRMED chỉ sau khi ≥2 validator đã chứng thực; (b) tắt 1 validator ⇒ vẫn CONFIRMED (đủ 2f+1 hoạt động); (c) tắt 2 validator ⇒ giữ PENDING, KHÔNG fork, bật lại ⇒ CONFIRMED; (d) 1 validator bị sửa để gửi sự kiện đăng ký giả (user chưa đăng ký ở parent) ⇒ không bao giờ thành `ParentRegistered`; (e) kill -9 một validator giữa chừng ⇒ không lệch state root giữa các node.
- Kiểm tra mọi node cùng state root và cùng cờ `ParentRegistered` sau mỗi kịch bản (so state root theo block).
Xong khi: script lặp lại được (như `restart_durability.sh`), 3 lần chạy liên tiếp PASS, ghi báo cáo `note/`.

### P0-2. Co-attestation cho sự kiện rollup credit/lock/refund (mục A2)
Hiện một validator Byzantine vẫn giả được `CreditObserved`… (mint). Thiết kế đề xuất (đã ghi ở plan_next_steps A2):
- Envelope chung `{inner, attestations}`, digest `keccak(domain || chainID || keccak(inner))`; cộng dồn trong contract storage `RollupSystemAddress` giống `ApplyAttested`; chỉ dispatch `inner` khi đủ f+1; **thêm dấu "đã áp dụng" (tombstone)** để validator đến muộn không kích hoạt lại (credit không idempotent như cờ đăng ký — kiểm xem state machine của `CrossNodeHandler` có chặn trùng không, KHÔNG dựa vào đó).
- Bọc ở `eventProposer` (send/recv/reclaim worker, `cmd/simple_chain/app.go`), ký bằng cùng khoá committee. Chú ý bookkeeping in-flight/nonce của `eventProposer` (đã từng gây wedge — xem comment trong `rollup_system_handler.go`).
- Test: unit + integration như `account_registry_accumulate_test.go`; mutation; sau đó chạy lại **luồng cross-chain đầy đủ** (credit end-to-end đã live-verified commit `15761838` — không được làm hỏng) trên cụm P0-1.
Xong khi: một validator đơn lẻ không mint được (kiểm trên cụm 4 validator), luồng credit thật vẫn PASS.

### P0-3. Kiểm tra khoá committee ↔ tài khoản validator lúc khởi động
Nếu `PublicKeyBls` của địa chỉ validator ≠ public key của `Databases.BLSPrivateKey` (hoặc khoá node khi không đặt), đăng ký sẽ PENDING mãi và không có cảnh báo. Thêm kiểm tra lúc khởi động (và/hoặc metric + log ERROR định kỳ): khoá attestation của node có thuộc committee hiện tại không. Chỉ cảnh báo/metric, không tự sửa, không chặn khởi động node không phải validator.

### P0-4. Gỡ các lỗ hổng deploy đã biết (GitHub x3pi/metanode)
Issue #103 (CRITICAL: `SKIP_MEMPOOL_SIG_VERIFY` bật cứng trong template deploy — chữ ký tx không được kiểm; liên quan trực tiếp tới secp/gate), #104 (mật khẩu SSH/become dạng plaintext, chưa có vault), #105 (`parse_inventory.py` có chế độ dump credential). Xác minh trạng thái hiện tại (có thể đã sửa một phần ở commit `b9810996`), sửa phần còn lại trong `deploy/ansible_clusters`, đối chiếu: node production phải kiểm chữ ký ở exec filter (commit sig-enforcement 2026-09-30).

## P1 — trước khi mở cho người dùng thật
- **P1-1. Runbook đổi chain ID 991 / wipe + redeploy đồng thời** (parent và mọi cụm; chữ ký cũ vô hiệu): viết `note/runbook_chain_id_991_cutover.md` — thứ tự dừng/khởi động, kiểm chain ID parent khớp exec (đã có check lúc khởi động, chỉ cảnh báo khi parent không truy cập được), cách xác nhận bằng `mtn_getClusterIdentity` và `/status`.
- **P1-2. Genesis production:** kiểm tra khoá BLS của chính các node exec đã được đăng ký/float trong genesis parent (ghi chú cũ: "possible production genesis gap — exec nodes' own BLS key registration — not yet checked"). Kiểm bằng cụm cô lập khởi tạo từ template ansible thật, không từ `gen_env.py`.
- **P1-3. Chain-binding cho legacy chain:** simple chain cũ (`bls_legacy`) chưa có chain-binding ở exec filter cho giao dịch không phải 0xFF (plan §9). Chỉ đánh giá mức rủi ro và đề xuất; không đổi hành vi dapp cũ.
- **P1-4. MVM `creatorPublicKey` cho deploy từ tài khoản chỉ có secp:** agent trước kết luận EVM/WASM không dùng; xác nhận bằng test deploy contract thật từ tài khoản secp (EVM) và đọc lại nhánh C++ MVM.
- **P1-5. Quan sát:** metric/log cho: số đăng ký PENDING/CONFIRMED/REJECTED, tuổi PENDING lâu nhất, số attestation đang chờ, chữ ký bị từ chối (non-committee/sai digest), lệch chain ID. Không đưa vào đường nóng.
- **P1-6. Tải/độ bền:** 10k đăng ký đồng thời (relay giới hạn 4096 theo dõi; kiểm `ErrRegistrationBusy` lan ra RPC đúng), restart giữa tải, parent mất 10 phút rồi quay lại.

## P2 — dọn dẹp
- Đưa `restart_durability.sh`, E2E 14 kịch bản và kịch bản P0-1 vào CI (`ci.sh`) nếu chạy < vài phút; nếu dài thì chỉ chạy nightly.
- Cập nhật `PROJECT_STRUCTURE.md` + docs ansible mỗi khi đổi cấu trúc/định dạng payload.
- Phase 2 (tài khoản tạm khi mất parent) **không làm**; user đã chọn chặn hẳn/chờ parent.
- Push + PR: chỉ khi user yêu cầu; sau khi merge phải `git show origin/dev:<path>` kiểm tra không bị mất commit (đã xảy ra 3 lần với squash-merge).

## Tiêu chí "sẵn sàng production" (checklist)
- [x] P0-1 cụm ≥4 validator: 5 kịch bản PASS 3 lần liên tiếp, state root các node khớp.
- [x] P0-2 không validator đơn lẻ nào mint/credit được; cross-chain credit thật vẫn PASS.
- [x] P0-3 cảnh báo khi khoá attestation không thuộc committee.
- [x] P0-4 #103/#104/#105 đóng, chữ ký tx được kiểm ở exec filter trên deploy thật.
- [ ] P1-1/P1-2 runbook cutover và genesis production đã chạy thử trên cụm cô lập.
- [ ] `go test -race`, `build_check.sh` sạch; mọi báo cáo có output thật kèm commit hash.
