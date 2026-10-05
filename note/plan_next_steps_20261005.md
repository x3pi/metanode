# Kế hoạch tiếp theo cho agent khác (2026-10-05)

Bối cảnh: account gate (secp + `parent_registered`) đã E2E 14/14 trên cụm cô lập; parent và mọi cụm exec dùng chung chain ID 991 (cấu hình từ genesis). Commit mới nhất: `09b912e6` (dev, **chưa push**). Đọc trước: `note/plan_account_registration_gate.md` (§9 rủi ro mở, §10 thiết kế mục A), `note/account_gate_e2e_20261005.md`, `AGENTS.md`, `PROJECT_STRUCTURE.md`.

## Quy tắc chung (bắt buộc)
- Chỉ test local, KHÔNG đụng cụm 231/230, `/opt/metanode`, hay `parent_chain` đang chạy của user. Dùng môi trường cô lập: `execution/scripts/test/gate_e2e/gen_env.py --base <dir> --bin <bindir>` + `run_env.sh <dir> {start|stop}` (cổng 31xxx). Trước khi dừng/xóa env luôn `run_env.sh stop`; không xóa thư mục `pids` khi tiến trình còn chạy.
- Zero-fork (AGENTS.md Part 2.5): không dùng timeout/sleep để quyết định dispatch; thiếu bằng chứng thì giữ PENDING.
- Sau mỗi thay đổi: `gofmt`, `go vet`, `go test` (thêm `-race` cho phần đồng thời), `consensus/metanode/scripts/build_check.sh` phải sạch. Thử mutation (bỏ kiểm tra → test phải đỏ).
- Commit theo tên file (`git add <file>`, không `git add <dir>`; `*.py` bị gitignore → `git add -f`). Không push nếu user chưa yêu cầu. Cuối mỗi báo cáo có khối "📋 Tóm tắt thay đổi" tiếng Việt.
- Kết quả test phải tự chạy lại và đọc output thật, không tin báo cáo của agent khác.

## A. Xác thực sự kiện hệ thống bằng co-attestation f+1 (ưu tiên cao, **cần user xác nhận hướng (a) trước**)
Vấn đề: bất kỳ validator nào cũng giả được sự kiện `account_registered`/credit/lock bằng danh tính BLS node. Parent header không có chữ ký nên proof Merkle một mình không đủ (chi tiết plan §10).
Việc làm:
1. Xác định tx_processor đọc committee (khóa BLS validator) từ đâu một cách deterministic (xem `CommitteeAttestationWorker`, `pkg/rollup`). Nếu không có nguồn trong state → dừng, báo user.
2. Thêm envelope `attestations` vào payload; `AccountRegistryHandler.Apply` (`pkg/rollup/account_registry_handler.go`) kiểm tra ≥ f+1 chữ ký BLS của thành viên committee khác nhau trên digest `keccak("ACCT_REG_ATTEST_V1"||chainID||user||clusterKey||parentSeq)` TRƯỚC khi áp dụng. Cụm 1 node: f+1 = 1.
3. `RegistrationWorker` (`worker_registration.go`): mỗi validator tự xác minh qua `QuorumClient`, ký, trao đổi chữ ký qua queue **có giới hạn**; chỉ đề xuất tx khi đủ f+1; chưa đủ → PENDING.
4. Áp dụng cùng envelope cho credit/lock event nếu cùng đường dẫn.
Test: chữ ký giả, trùng, ngoài committee, payload bị sửa, thiếu chữ ký; E2E mở rộng (thêm kịch bản forged-by-node-identity bị từ chối); cần cụm ≥ 4 validator để phủ đa validator.
Xong khi: E2E E8 mở rộng pass, mutation test đỏ khi bỏ kiểm chữ ký, cập nhật plan §10 + `PROJECT_STRUCTURE.md` (đổi định dạng payload ⇒ ghi rõ cần deploy đồng thời).

## B. Kiểm tra chain ID khi khởi động (nhỏ)
Hiện cụm exec chỉ tự đặt chain ID parent theo genesis của nó; nếu genesis hai bên lệch, giao dịch lên parent bị từ chối âm thầm. Việc làm: lúc khởi động exec hỏi parent (`/status` hoặc endpoint tương đương, thêm trường chain ID nếu thiếu) và từ chối chạy khi khác. Parent không truy cập được → không được đoán; ghi log và để cơ chế hiện có (relay PENDING). Test: lệch chain ID ⇒ lỗi rõ ràng.

## C. Hàng đợi đăng ký bền (trung bình)
`RegistrationRelay` (`pkg/rollup/registration_relay.go`) giữ yêu cầu trong bộ nhớ → mất khi restart. Lưu bền (cùng kiểu storage node đang dùng), vẫn có giới hạn kích thước, idempotent khi replay. Test: kill -9 giữa PENDING rồi khởi động lại ⇒ vẫn lên CONFIRMED.

## D. Việc nhỏ
- `run_devnet.sh` đang dùng chung chainId 991 giữa các cụm: xác nhận phù hợp với quyết định "một chain ID" hiện tại, cập nhật ghi chú nếu cần.
- MVM: kiểm `creatorPublicKey` khi deploy contract từ tài khoản chỉ có secp (C++ MVM) — chỉ điều tra và báo cáo, chưa sửa nếu chưa chắc.
- Legacy chain vẫn chưa có chain-binding ở exec-filter cho giao dịch không phải 0xFF: ghi vào plan §9 (không sửa ngoài phạm vi).
- Xóa branch local thừa `pr155`, `sec-rollup-system-auth` (sau khi xác nhận đã nằm trong dev: `git branch --contains`).
- Hai file `portal/` đang sửa dở (`AccountGateCard.jsx`, `metanodeRpc.js`) không thuộc đợt này: không commit, hỏi user.

## E. Phase 2 (tài khoản tạm khi mất parent) — KHÔNG làm
User đã chọn giai đoạn 1 (chặn hẳn/chờ parent). Chỉ làm khi user yêu cầu lại; khi đó đọc plan §8.1–8.2 (loser bị LOCKED hoàn toàn, chưa có rút tiền).

## Thứ tự đề xuất
B → C → D (độc lập, ít rủi ro) song song với A sau khi user xác nhận hướng; push khi user bảo.
