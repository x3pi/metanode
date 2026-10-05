# Kế hoạch tiếp theo cho agent khác (2026-10-05)

Bối cảnh: account gate (secp + `parent_registered`) đã E2E 14/14 trên cụm cô lập; parent và mọi cụm exec dùng chung chain ID 991 (cấu hình từ genesis). Commit mới nhất: `09b912e6` (dev, **chưa push**). Đọc trước: `note/plan_account_registration_gate.md` (§9 rủi ro mở, §10 thiết kế mục A), `note/account_gate_e2e_20261005.md`, `AGENTS.md`, `PROJECT_STRUCTURE.md`.

## Quy tắc chung (bắt buộc)
- Chỉ test local, KHÔNG đụng cụm 231/230, `/opt/metanode`, hay `parent_chain` đang chạy của user. Dùng môi trường cô lập: `execution/scripts/test/gate_e2e/gen_env.py --base <dir> --bin <bindir>` + `run_env.sh <dir> {start|stop}` (cổng 31xxx). Trước khi dừng/xóa env luôn `run_env.sh stop`; không xóa thư mục `pids` khi tiến trình còn chạy.
- Zero-fork (AGENTS.md Part 2.5): không dùng timeout/sleep để quyết định dispatch; thiếu bằng chứng thì giữ PENDING.
- Sau mỗi thay đổi: `gofmt`, `go vet`, `go test` (thêm `-race` cho phần đồng thời), `consensus/metanode/scripts/build_check.sh` phải sạch. Thử mutation (bỏ kiểm tra → test phải đỏ).
- Commit theo tên file (`git add <file>`, không `git add <dir>`; `*.py` bị gitignore → `git add -f`). Không push nếu user chưa yêu cầu. Cuối mỗi báo cáo có khối "📋 Tóm tắt thay đổi" tiếng Việt.
- Kết quả test phải tự chạy lại và đọc output thật, không tin báo cáo của agent khác.

## Trạng thái triển khai thực tế (Cập nhật 2026-10-05)

### B. Kiểm tra chain ID khi khởi động — ✅ ĐÃ HOÀN THÀNH
- **Thực thi:**
  - Parent Chain HTTP `/status` (`HTTPServer.handleStatus`) trả về `"chain_id": ParentChainID`.
  - `QuorumClient.GetStatus()` đưa `chainID` vào `statusKey` để đạt quorum Byzantine trên cả chain ID.
  - `execution/cmd/simple_chain/app.go` gọi `verifyParentChainID(parentClient, parentchain.ParentChainID)` khi khởi động: nếu parent báo `chain_id > 0` và khác cấu hình local ⇒ từ chối khởi động với lỗi rõ ràng; nếu parent unreachable ⇒ log warning và để cơ chế retry xử lý (không đoán mò).
- **Kiểm thử:** Unit test `execution/cmd/simple_chain/startup_chain_id_test.go` (`TestVerifyParentChainID_Scenarios`) pass 5/5 ca (nil client, matching ID, mismatched ID, parent offline, legacy parent with zero ID).

### C. Hàng đợi đăng ký bền — ✅ ĐÃ HOÀN THÀNH
- **Thực thi:**
  - Thêm interface `RegistrationRelayStore` và `KVStore` (tương thích `storage.Storage`) trong `pkg/rollup/registration_relay.go`.
  - `RegistrationRelay` lưu bền mọi thay đổi trạng thái (`Submit`, `update`, `retryOrFail`, `resolveFound`).
  - Khi khởi động lại (`SetStore`), relay tự quét store, khôi phục bảng `entries`, tự đồng bộ các tài khoản đã on-chain lên `CONFIRMED`, và re-queue các yêu cầu chưa submit vào `queue`.
  - `cmd/simple_chain/app.go` kết nối `app.regRelay.SetStore(rollup.NewKVRelayStore(app.storageManager.GetStorageMapping()))`.
- **Kiểm thử:** Unit test `execution/pkg/rollup/registration_relay_test.go` (`TestRegistrationRelay_DurableQueueSurvivesRestartAndReplays`) pass: tắt relay giữa chừng khi request đang `PENDING` ⇒ relay mới nạp lại từ store và tiếp tục relay lên parent đến khi `CONFIRMED` tự động.

### D. Việc nhỏ — ✅ ĐÃ HOÀN THÀNH
- **D.1:** Xác nhận `run_devnet.sh` dùng chung `chainId: 991` và tách biệt bằng `cluster_id: 1` vs `2` là hoàn toàn đúng với quyết định kiến trúc "một chain ID cho parent và các exec cluster".
- **D.2:** Điều tra MVM `creatorPublicKey`: C++ MVM (EVM/WASM engine) hoàn toàn không sử dụng hay phụ thuộc vào `creatorPublicKey`. Go chỉ lưu metadata từ `senderState.PublicKeyBls()`, với tài khoản secp-only trường này rỗng (0 byte), không ảnh hưởng đến bytecode hay execution state, hoàn toàn an toàn và xác định giữa các node.
- **D.3:** Ghi nhận thiếu sót chain-binding của legacy chain ở exec-filter vào `note/plan_account_registration_gate.md` §9 (giữ nguyên, không sửa ngoài phạm vi).
- **D.4:** Đã xóa sạch 2 branch local thừa `pr155` và `sec-rollup-system-auth` sau khi xác nhận đã gộp trong `dev`.
- **D.5:** Portal UI đã hoàn thiện và push trong commit `09452746`.

### A. Xác thực sự kiện hệ thống bằng co-attestation f+1 — ✅ ĐÃ BẬT TRÊN NODE (hướng (a), 2026-10-05)
- **Cơ chế:** mỗi validator tự xác minh sự kiện qua `QuorumClient` rồi gửi **tx hệ thống riêng** chứa chữ ký BLS của chính nó (khoá committee `Databases.BLSPrivateKey`, nếu trống thì dùng khoá node). `AccountRegistryHandler.ApplyAttested` kiểm chữ ký (thuộc committee, không trùng, đúng digest `ACCT_REG_ATTEST_V1||chainID||user||clusterKey||parentSeq`), **cộng dồn** chữ ký vào contract storage của `RollupSystemAddress` (`NewDBAttestationStore`), đủ ≥ f+1 chữ ký phân biệt (f=(N-1)/3) mới đặt `ParentRegistered`, rồi xoá bộ nhớ tạm. Chưa đủ ⇒ receipt thành công nhưng giữ PENDING (không timeout, không đoán). Không cần kênh P2P mới: chính consensus là nơi trao đổi chữ ký.
- **Committee** (`tx_processor/rollup_committee.go`): validator không bị jail, stake > 0, tài khoản có `PublicKeyBls` 48 byte, đọc từ chính chain state mà tx đang chạy ⇒ mọi replica cùng kết quả. Chữ ký của validator đã rời committee không được tính.
- **Worker:** bỏ qua gửi lại nếu chữ ký của mình đã nằm trên chain (`HasPendingAttestation`) ⇒ không spam khi chờ validator khác.
- **Yêu cầu triển khai:** với mỗi validator, `AccountState.PublicKeyBls` của địa chỉ validator PHẢI bằng public key của `Databases.BLSPrivateKey` (hoặc khoá node nếu không đặt BLSPrivateKey); lệch ⇒ đăng ký PENDING mãi (an toàn nhưng treo). Đổi định dạng payload ⇒ deploy đồng thời mọi node. `GetAllValidators` giới hạn top 21.
- **Đã kiểm:** unit (cộng dồn, trùng, ngoài committee, sai digest, thứ tự, committee đổi giữa chừng; 5 mutation đều bị bắt), integration thật qua `HandleTransaction` với committee 4 validator (1 validator lặp lại nhiều lần không đăng ký được; validator thứ 2 hoàn tất; node ngoài committee bị từ chối), `-race`, E2E cụm cô lập 14/14 với enforcement bật.
- **Chưa kiểm:** E2E nhiều validator thật (≥4) — môi trường cô lập chỉ có cụm 1 validator (f+1=1). Nên chạy trước khi lên production thật.

### C+. Độ bền hàng đợi đăng ký — ✅ kiểm bằng kill -9 thật
`scripts/test/gate_e2e/restart_durability.sh <BASE>`: (A) đã CONFIRMED rồi kill -9 ⇒ vẫn CONFIRMED; (B) đăng ký khi parent tắt, kill -9 node, bật lại ⇒ lên CONFIRMED. 3/3 lần PASS. Ghi mỗi request có `SyncDurable` (Pebble NoSync không đủ khi mất điện). Nạp lại bị chặn bộ nhớ (maxTracked), bản ghi của user đã đăng ký tự xoá.
