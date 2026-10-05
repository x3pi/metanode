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

### A. Xác thực sự kiện hệ thống bằng co-attestation f+1 — ⚠️ MỚI XONG PHẦN XÁC MINH Ở HANDLER, CHƯA BẬT TRÊN NODE THẬT
> Review 2026-10-05: **chưa có hiệu lực bảo mật.** (1) `CommitteeProvider` chỉ là interface, chưa có cài đặt (`GetActiveCommitteeBLSKeys`) và chưa gắn vào `simple_chain` ⇒ handler không ép f+1. (2) Worker chỉ đính kèm chữ ký CỦA CHÍNH NÓ, chưa có trao đổi chữ ký giữa validator (bước 3 còn thiếu) ⇒ bật provider với committee f+1>1 sẽ làm mọi đăng ký PENDING mãi. (3) Chưa có E2E nhiều validator. Việc còn lại: cài `CommitteeProvider` từ state (nguồn ở bước 1), cơ chế thu chữ ký có giới hạn, E2E ≥4 validator. Đã sửa lỗi: handler từng mặc định chain ID 991 cứng nên lệch với worker khi chain ID cấu hình khác ⇒ mọi đăng ký bị từ chối; nay dùng `parentchain.ParentChainID`.
- **Bước 1 (Deterministic Committee Source):** Đã xác định nguồn uỷ ban đọc trực tiếp từ state qua `chainState.GetStakeStateDB().GetAllValidators()` kết hợp `chainState.GetAccountStateDB().AccountState(v.Address()).PublicKeyBls()`.
- **Bước 2 (Envelope & Verification):**
  - Thêm domain `ACCT_REG_ATTEST_V1` và hàm `ComputeAccountRegistrationAttestDigest(chainID, user, clusterKey, parentSeq)`.
  - Mở rộng `AccountRegistrationPayload` với `Attestations []RegistrationAttestation`.
  - `AccountRegistryHandler.Apply` kiểm tra $\ge f+1$ chữ ký BLS của các thành viên uỷ ban khác nhau trên digest trước khi áp dụng.
  - `RegistrationWorker.pollAndProcess()` tự ký và đính kèm attestation của node khi đề xuất.
- **Kiểm thử:** Unit test `execution/pkg/rollup/account_registry_coattest_test.go` (`TestAccountRegistryHandler_CoAttestation`) pass 5/5 kịch bản:
  1. Đủ chữ ký uỷ ban $\ge f+1$ ⇒ thành công.
  2. Thiếu chữ ký uỷ ban $< f+1$ ⇒ bị từ chối rõ ràng.
  3. Trùng chữ ký từ cùng một validator ⇒ bị từ chối.
  4. Chữ ký từ node ngoài uỷ ban ⇒ bị từ chối.
  5. Payload bị sửa (digest lệch) ⇒ chữ ký không hợp lệ bị từ chối.
