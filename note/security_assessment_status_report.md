# Báo cáo Tổng hợp: Tình trạng Đánh giá Bảo mật Toàn Dự án Metanode

Viết 2026-08-26. Mục đích: gom lại **một chỗ duy nhất** những gì đã thực sự được đánh giá bảo
mật (bởi ai/khi nào/tìm ra gì), và những gì **chưa** được đánh giá — để đội biết rõ ranh giới
giữa "đã kiểm tra kỹ" và "chưa ai nhìn qua" trước khi đưa giá trị thật vào hệ thống. Đây là báo
cáo tổng hợp (dựa trên các tài liệu review đã có sẵn trong repo), không phải một đợt audit mới.

**Kết luận ngắn gọn ở đầu:** dự án đã trải qua **nhiều đợt tự-review nội bộ nghiêm túc** (không
phải chỉ chạy test rồi thôi — có tên hàm test cụ thể, có PR cụ thể, có xác nhận chạy sống), tìm
và vá tổng cộng **~30 bug/lỗ hổng thật** trên các lớp khác nhau. Nhưng **chưa có một đợt audit
độc lập từ bên ngoài nào diễn ra** — mọi review tới nay đều do chính đội phát triển tự thực
hiện. Với hệ thống custody giá trị thật (cross-chain bridge), đây vẫn là một khoảng trống lớn,
đã được chính đội nhận diện và lên kế hoạch (mục 3 bên dưới) nhưng chưa thực hiện.

---

## 1. Những gì ĐÃ được đánh giá

### 1.1 Cross-chain Gateway / Root Anchor Bridge — lớp được review nhiều nhất

Đây là lớp custody giá trị thật xuyên chuỗi, và cũng là lớp có lịch sử review dày nhất: **5 đợt
review nội bộ độc lập** trên cùng một module (`note/cross_chain_production_readiness_plan.md`,
Phase 0 → 0.11), tìm ra **15 bug/gap thật**, tất cả đã vá + có test hồi quy thật (không mock)
đi kèm cùng commit:

| # | Bug | Mức độ | PR |
|---|---|---|---|
| 1 | `attestCommit()` verify `aggregateAmount` sai root | 🔴 Nghiêm trọng | #56 |
| 2 | `vote()` không xác thực người gửi | 🔴 Nghiêm trọng | #56 |
| 3 | `claimDeadChainBalance()` verify account proof sai root | 🔴 Nghiêm trọng | #58 |
| 4 | `Refund()` thiếu authorization | 🔴 Nghiêm trọng | #60 |
| 5 | Genesis governance deadlock (không ai vote được chain đầu tiên) | Chặn hoàn toàn | #61 |
| 6 | Giá trị native/custom-asset chưa từng nối vào số dư thật | 🔴 Lớn nhất | #63, #64 |
| 7 | `msg.sender` sai ở 4 điểm gọi EVM nội bộ | 🔴 Nghiêm trọng | #64 |
| 8 | `SmartContractDB.SetCode` không được gọi trong đường Gateway nội bộ | 🟠 | #64 |
| 9 | `PerChainAllocation` không có đường cấp phát nào — trần luôn = 0 | 🔴 Chặn hoàn toàn | — |
| 10 | `propose`/`vote`/`executeProposal` tin timestamp caller tự khai — bypass timelock 72h | 🔴 | — |
| 11 | `bootstrapFoundingChains` không kiểm tra người gửi — front-run ceremony | 🟡 | — |
| 12 | `QuorumThreshold` không sàn an toàn ở 4 nơi gán — cho phép thiểu số giả mạo quorum | 🔴 | — |
| 13 | `ProposalRegisterChain` nhận committee không rỗng mà không verify PoP — rogue-key | 🔴 | — |
| 14 | (Phase 0.10) `QuorumThreshold` thiếu bounds-check ở `ProposalUpdateCommittee` + `epoch_sync.go` | 🔴 | — |
| 15 | (Phase 0.10) `ProposalRegisterChain` — cùng lỗ hổng #13 tái xuất hiện ở đường ghi khác | 🔴 | — |

Ngoài ra: cơ chế **gas-lock/refund** (chống DoS khi `CONTRACT_CALL` payload thất bại giữa
chừng) đã được xác nhận **chạy sống thật** trên hạ tầng đa-process thật (không phải mock) ngày
2026-08-26 — cả đường thành công (refund đúng phần gas dư) lẫn đường fail-closed (revert đúng
lý do, state đích không bị đụng khi thiếu `gasFee`).

**Bộ test bảo mật chuyên biệt** (`execution/pkg/cross_chain/security_audit_test.go`, 9 test,
515 dòng), mỗi test đặt tên đúng theo kịch bản tấn công đang kiểm tra:
`TestAudit_BLSQuorumCertAndRogueKeyDefense`, `TestAudit_MerkleProofTamperResistance`,
`TestAudit_AntiReplayAndConcurrentDoubleClaim`, `TestAudit_AntiDoubleMintViaRefundRaceGuard`,
`TestAudit_OriginSenderContextIntegrity`, `TestAudit_HopCountBoundaryEnforcement`,
`TestAudit_AdversarialOverdrawAndSupplyCeiling`, `TestAudit_FailClosedEpochAlignment`,
`TestAudit_ZeroForkDestinationOfflineStability`.

**Phạm vi review đã đóng, đã có tài liệu bàn giao cho auditor bên ngoài**:
`note/external_security_audit_scope_p5.md` liệt kê rõ những gì trong/ngoài phạm vi, 3 dạng lỗi
lặp lại nhiều nhất tìm được (cơ chế code đúng nhưng chưa nối vào production path; giá trị caller
tự khai được tin mà không đối chiếu state thật; 1 trong nhiều đường ghi cùng field bị bỏ sót khi
thêm bảo vệ mới) — dùng làm checklist cho auditor bắt đầu từ đâu.

### 1.2 Bug chặn-toàn-chain nghiêm trọng nhất tìm được cả dự án — barrier-tx không tăng nonce

Tìm qua chạy sống thật (không phải review tĩnh), 2026-08-26, PR #73/#74: `HandleSuccessTransaction`/
`HandleRevertedTransaction` (đường xử lý barrier-tx của Gateway/Validator contract) gọi
`ExecuteNonceOnly`, nhưng hàm này **cố ý** bỏ qua việc tăng nonce của chính người gửi (đúng cho
luồng EVM song song thường, sai cho barrier-tx vốn không có bước tăng nonce nào khác). Hậu quả:
giao dịch thứ 2 của cùng 1 tài khoản gọi Gateway/Validator contract bị kẹt vĩnh viễn ở trạng
thái "nonce tương lai", và vì executor chỉ tiến khi tìm được ≥1 tx hợp lệ, **toàn bộ chain ngừng
sinh block** — không chỉ giao dịch đó. Đã vá (`receipt_helper.go`, gọi `SetNonce` tường minh),
có test hồi quy chứng minh fail-nếu-thiếu-fix/pass-nếu-có-fix
(`TestGatewayHandler_ConsecutiveTransactionsFromSameSenderAdvanceNonce`), xác nhận chạy sống.
Đây là bug **duy nhất tìm được cả dự án có khả năng treo toàn bộ chain** (không phải chỉ 1 tx),
nên xếp mức nghiêm trọng cao nhất trong toàn bộ danh sách.

### 1.3 Lớp thực thi EVM/MVM

Một đợt hardening + audit riêng (đã đóng, không phải phạm vi review mới của P5): EIP-4844/7702,
intrinsic gas, state journal, sửa SSTORE/refund/CREATE2/SELFDESTRUCT. Đợt audit hậu-hoàn-thành
(2026-08-14) tìm + vá thêm 4 khoảng trống: giới hạn số lượng blob, corruption deploy-info của
delegated-EOA, thiếu nil-guard ở `smart_contract_db.Code()`, biên kiểm tra `BLOBHASH`, cộng thêm
1 lỗi lớn — đường native fast-path từng bỏ qua EVM khi chuyển giá trị tới địa chỉ có code (nay
đã định tuyến đúng qua EVM).

### 1.4 Đồng thuận Rust (Block-STM / DAG)

`note/ci_test_plan.md` — phiên làm việc CI khép kín: sửa build, giải quyết 28/28 test consensus-
core từng fail, **0 test flaky còn lại** (189/189 pass, 3/3 lần chạy full-suite liên tiếp). Tìm
ra 2 bug PRODUCTION thật (không phải chỉ lỗi test):
- **Remote-DoS panic thật** trong `handle_send_block()` — chỉ số author index ngoài phạm vi bị
  dùng để index mảng TRƯỚC KHI validate, một node ác ý/lỗi có thể crash node khác từ xa.
- **Lỗi đúng-đắn đồng thuận thật** trong `DagState::is_committed()` fast path — fast path âm
  thầm bỏ sót transaction của các block mồ côi (orphaned) khỏi việc commit vĩnh viễn.

Cộng thêm: fix equivocation-risk khi phục hồi trạng thái amnesia trong `should_propose()`, và 4
bug Block-STM/consensus khác đã tìm+vá riêng trước đó (checkpoint scan sai của `CommitInfo`, fork
do thực thi trùng GEI, hang do "abort-storm" của Block-STM, bytecode test Xapian cũ). Ngoài ra
còn 5 file trong `note/known_bugs/` ghi lại các fork bug consensus/state đã tìm+vá từ các phiên
trước (`commit_info_sync_fork.md`, `missing_block_heuristic_fork.md`,
`snapshot_recovery_linearizer_fork.md`, `startup_sync_persistence_fork.md`,
`synthetic_baseline_patch_fork.md`).

**Phân tích chịu-lỗi BFT** (`note/bft_fault_tolerance_node_count.md`): công thức
`f=⌊(n-1)/3⌋` áp cho toàn hệ thống — kết luận quan trọng: **cụm 3-node hiện tại có ĐỘ CHỊU LỖI
BFT BẰNG 0** (cần n≥4 để chịu 1 node lỗi). Đây không phải lỗ hổng code, mà là một giới hạn cấu
hình/vận hành cần đội biết trước khi triển khai thật.

### 1.5 Audit thủ công khác (lịch sử)

`note/mvmid_lifecycle_audit.md` (2026-05-28): audit toàn bộ lifecycle `mvmId` (`GetOrCreateMVMApi`,
`ClearMVMApi`, `ProtectMVMApi`/`UnprotectMVMApi`, `MVM_cancelTransaction`) qua cả Go và C++, tìm
rò rỉ bộ nhớ và khả năng lệch hash.

### 1.6 Sự cố "audit giả" — đã tự phát hiện và sửa

Một bộ `audit_pack/` + script T0-T3 (2026-08-25) từng **báo cáo "PASSED" giả** cho kết quả bảo
mật/chaos/benchmark mà không thực sự chạy các bước đó. Đã bị chính đội phát hiện và sửa **ngay
trong ngày**, xác nhận lại: `go vet`/`cargo audit`/benchmark hiện chạy thật, ghi chú trung thực
"chưa triển khai" ở đúng chỗ, `audit_pack/` + khoá đã bị gitignore. Bài học: **một banner xanh
không đồng nghĩa bước đó thực sự chạy** — luôn đọc code chạy phía sau banner trước khi tin.

### 1.7 Kiểm tra bảo mật tự động (CI, chạy mỗi PR)

| Công cụ | Phạm vi | Nơi cấu hình |
|---|---|---|
| `go vet` (`enable-all`) | Toàn bộ Go execution layer | `.github/workflows/go-ci.yml` ×2, `execution/.golangci.yml` |
| `golangci-lint` (gồm `gosec`) | Lỗ hổng code Go phổ biến (SQL injection pattern, path traversal, weak crypto, v.v.) | `execution/.golangci.yml` |
| `govulncheck` | CVE đã biết trong dependency Go | `execution/.github/workflows/go-ci.yml` |
| `cargo clippy -D warnings` | Lint Rust nghiêm ngặt (cảnh báo = lỗi build) | `consensus/.github/workflows/rust-ci.yml` |
| `cargo audit` (RustSec) | CVE đã biết trong dependency Rust (`crates.io` advisory DB) | `consensus/.github/workflows/rust-ci.yml` |
| `go test`/`cargo test` full suite | Hồi quy chức năng (không phải bảo mật chuyên biệt, nhưng chặn PR nếu fail) | cả 2 workflow trên |

Đây là lớp bảo vệ **liên tục, tự động**, khác với các đợt review thủ công ở trên — chạy trên mọi
PR vào `main`/`dev`, không phải một lần rồi thôi.

---

## 2. Những gì CHƯA được đánh giá / còn mở

| Hạng mục | Trạng thái | Ghi chú |
|---|---|---|
| **Audit độc lập bên ngoài (P5)** | ❌ Chưa bắt đầu | Đã lên phạm vi đầy đủ (`external_security_audit_scope_p5.md`), nhưng chưa engage bất kỳ bên thứ ba nào. Đây là điều kiện chặn cứng trước khi bỏ giới hạn giá trị thật (Phase 4, mục 3). |
| **T2 — testnet đa-máy thật (≥4 chain, mạng thật)** | 🟡 Một phần | Đã chạy thật trên **1 máy, nhiều process** (2026-08-26) — không phải nhiều máy độc lập. Người dùng hiện đang tự triển khai multi-machine thật (`Nhat-server-233`), đang bị chặn ở bước khởi động Root Anchor (`AddrInUse`/panic Rust consensus) — chưa có số liệu T2 thật (độ trễ mạng thật, chi phí BLS/commit thật ở quy mô nhiều máy). |
| **T3 — kiểm thử đối kháng chủ động (chaos)** | ❌ Chưa bắt đầu | Phụ thuộc hạ tầng T2. Kịch bản dự kiến: chiếm 3/4 validator 1 chain nhỏ, tắt Reserve tạm thời, Chain-Death Recovery đầu-cuối trên testnet thật. |
| **T5 — Bug bounty** | ❌ Chưa bắt đầu | Khuyến nghị trước khi bỏ trần giá trị (Stage 3 rollout). |
| **Root Anchor Layer C (multi-validator thật)** | ❌ Chưa xong, chưa root-cause | `peer_rpc_port` bind collision khi node "full" khởi động sau node "early" trên cụm multi-validator thật — 2 lần thử fix chưa thành công. **Mọi bằng chứng "chạy thật" của bridge tới nay đều trên single-validator per chain** cho riêng Root Anchor. |
| **TEE/OHtee packaging — B3 (SimpleDb secondary trie)** | ❌ Chưa đánh giá đầy đủ | Xác nhận là 1 secondary trie thật gắn vào consensus state với key động — cùng hạng mục khó như `GlobalStateGet`, cần thống kê Merkle-witness statelessness đầy đủ. Người dùng đã xác nhận hoãn (out of scope hiện tại), không phải đã đóng. |
| **State history pruning — self-heal qua peer/Checkpoint** | 🟡 Tạm dừng | `Checkpoint()` gây stall hệ thống thật khi test — bước self-heal đang treo, chưa có thiết kế an toàn cuối cùng. |
| **Fuzzing** | ❌ Không tìm thấy | Không có harness fuzz nào trong repo (Go `go-fuzz`/`testing.F`, Rust `cargo-fuzz`) cho các hàm parse/decode calldata, ABI, Merkle proof, hay message P2P. |
| **Formal verification** | ❌ Không có | Không có bằng chứng hình thức cho các bất biến an toàn (BFT threshold, supply ledger conservation, v.v.) — chỉ dựa vào test hồi quy + review thủ công. |
| **Smart contract phía DApp (Solidity người dùng viết)** | ❌ Ngoài phạm vi | Mọi review tới nay chỉ phủ Gateway Contract (precompile) và cách nó được GỌI — không phủ logic hợp đồng do DApp developer tự triển khai ở chain đích (xem `note/dapp_cross_chain_developer_guide.md` mục 3 — trách nhiệm `isCalledByGateway()` là của DApp). |
| **Bảo mật hạ tầng/OS (systemd, firewall, quyền file, network segmentation)** | ❌ Chưa đánh giá | `deployment_runbook_step_by_step.md`/`production_deployment_guide.md` là hướng dẫn vận hành, không phải review bảo mật hạ tầng đối kháng (không có pentest OS-level, không audit quyền `systemd` unit, không audit rule firewall). |
| **Quản lý khoá (key management) sản xuất** | ❌ Chưa có tài liệu | Khoá devnet hiện là hardcode/file config (ví dụ `gen_single_chain.py`'s fallback key) — chưa có ghi chú nào về HSM, khoá ký production, hay quy trình xoay khoá (key rotation) cho môi trường thật. |
| **Supply-chain sâu hơn CVE-scan** | 🟡 Một phần | `govulncheck`/`cargo audit` chỉ bắt CVE đã biết trong advisory DB — chưa có review reproducible-build, ký release, hay xác minh toolchain pin thủ công. |
| **Weakest-link governance risk** (kiến trúc doc mục 5.2) | 🟡 Đã có cơ chế chặn, chưa có đánh giá độc lập | `per_chain_allocation` ceiling đã chủ động chặn, nhưng đây là quyết định kiến trúc-mức — `external_security_audit_scope_p5.md` mục 3 tự nói rõ: cần auditor tự đánh giá độc lập, không chỉ tin implementation đúng đặc tả là đủ. |

---

## 3. Bảng tổng hợp số liệu

| Chỉ số | Số liệu |
|---|---|
| Tổng số bug bảo mật/chức năng-chặn thật tìm + vá (toàn dự án, có test hồi quy) | **~30** (15 cross-chain bridge + 1 chain-halt nonce + 5 EVM hardening + 2 consensus production bug + 4 Block-STM/fork + vài khoản nhỏ khác) |
| Số đợt review nội bộ độc lập trên riêng module cross-chain | 5 (Phase 0 → 0.11) |
| Test bảo mật chuyên biệt (đặt tên theo kịch bản tấn công) | 9 (`TestAudit_*`, cross-chain) |
| Đợt audit độc lập từ bên ngoài đã thực hiện | **0** |
| Đợt chạy T2 (đa máy thật) đã hoàn thành | **0** (đang thử, đang bị chặn) |
| Đợt chạy T3 (đối kháng chủ động) đã hoàn thành | **0** |

---

## 4. Khuyến nghị

1. **Không xem "đã review nội bộ nhiều lần" là tương đương "đã audit"** — 5 đợt review là tín
   hiệu tốt về chất lượng code, nhưng tất cả do cùng 1 đội viết ra tự kiểm tra; vẫn cần con mắt
   độc lập bên ngoài trước khi bỏ trần giá trị thật (per Phase 3/4, mục 2).
2. **Việc trước mắt, ưu tiên cao nhất**: gỡ chặn T2 multi-machine thật (đang bị lỗi `AddrInUse`
   trên `Nhat-server-233`) — không có số liệu T2 thật thì auditor bên ngoài sẽ hỏi lại, và T3
   không thể bắt đầu nếu chưa có hạ tầng T2.
3. Đưa nguyên `note/cross_chain_production_readiness_plan.md` +
   `note/external_security_audit_scope_p5.md` cho auditor khi engage — đã ở dạng sẵn sàng nộp,
   không cần viết lại.
4. Các hạng mục "❌ Chưa đánh giá" ở mục 2 (đặc biệt: key management sản xuất, hạ tầng/OS,
   fuzzing) đáng đưa vào phạm vi audit hoặc lên kế hoạch riêng — hiện chưa nằm trong phạm vi P5
   đã định nghĩa (P5 chỉ tập trung code-level bridge logic).
