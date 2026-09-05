# P5 — Phạm vi & chuẩn bị cho Security Review độc lập (Root Anchor Cross-Chain Bridge)

Viết 2026-08-25, sau khi Phase 0.6-0.9 (`cross_chain_production_readiness_plan.md`) đóng lại
toàn bộ các lỗ hổng/khoảng-trống nội bộ đã tìm được qua nhiều đợt review — đây là điểm mà
`note/cross_chain_root_anchor_architecture.md` mục 8/12 gọi là "P5", điều kiện chặn cuối
cùng còn lại trước khi cho giá trị thật chạy qua Root Anchor (xem
`note/production_deployment_guide.md` mục 0). Tài liệu này không thay thế review độc lập —
nó chỉ gom lại **cần đưa gì cho auditor** và **auditor nên tập trung vào đâu trước**, dựa trên
những gì 2 review nội bộ (nhiều đợt, tổng ~10 bug tìm+sửa) đã học được về lớp code này.

## 1. Phạm vi (những gì cần audit)

**Trong phạm vi — bề mặt tấn công thật, custody giá trị thật:**
- `execution/pkg/cross_chain/` toàn bộ: `gateway.go` (attestCommit/claimMessage/outbound/
  refund/verifyAndExecute/claimDeadChainBalance/registerChainViaStake/
  allocateSupplyWithCert/transferAllocationWithCert/declareChainDeadWithCert/
  unregisterChainWithCert/updateCommitteeWithRecoveryCert), `types.go`
  (`GlobalSupplyLedger`), `epoch_sync.go` (domain-separated digest functions cho từng hàm
  cert-authorized ở trên), `asset_registry.go`, `relayer.go`, `chain_death_recovery` (nếu có
  file riêng), `rootanchor/`. (`governance.go` — toàn bộ `GovernanceEngine`
  propose/vote/timelock/execute — đã bị xoá hẳn 2026-09-04, không còn tồn tại trong repo;
  thay bằng mô hình tự-ký/RecoveryCommittee ở trên, xem
  `note/eurozone_unified_native_coin_plan.md`.)
- `execution/pkg/blockchain/tx_processor/gateway_handler.go` — lớp ABI-dispatch, ranh giới
  tin cậy thật giữa calldata bên ngoài và các hàm `GatewayEngine` phía trên (đây là nơi 2/10
  bug của đợt review 2026-08-25 nằm: `msg.sender` sai khi gọi EVM nội bộ, và
  propose/vote/executeProposal tin timestamp caller tự khai).
- `execution/pkg/blockchain/tx_processor/abi_contract/gatewayAbi.go` — bề mặt ABI công khai.
- BLS aggregate signature + PoP (`pkg/bls/`) đúng như dùng trong luồng attestCommit/vote/
  committeeUpdate — không cần audit toàn bộ thư viện BLS, chỉ cách nó được dùng ở đây.
- Merkle proof construction/verify (`BuildCommitTree`, `VerifyMerkleProof`,
  `AggregateValueLeaf`) — đặc biệt điểm rủi ro #20 mục 2.3.1 kiến trúc doc (aggregateAmount tự
  khai phải được ràng buộc mật mã vào commit, không phải số caller tự nói).
- `deploy/systemd/runbook_root_anchor_genesis_ceremony.md` — vẫn còn dùng, nhưng chỉ cho
  genesis CONSENSUS (Rust); `bootstrapFoundingChains`/`GenesisCoordinator` (cơ chế
  ChainRegistry-bootstrap cũ mà tài liệu ceremony này từng dẫn tới) đã bị xoá hẳn 2026-08-28 —
  đăng ký ChainRegistry giờ chỉ còn `registerChainViaStake`, xem file trên.
- `deploy/systemd/gen_recovery_committee_keys.py` + `inject_recovery_committee.py` +
  `deploy/ansible/roles/local_build/tasks/main.yml` — quy trình sinh/tiêm `RecoveryCommittee`
  (2026-09-04, thay cho `GovernanceEngine`). Trọng tâm audit: khoá RecoveryCommittee KHÔNG
  được trùng với khoá consensus/validator của bất kỳ chain nào (tập trung quyền lực — phát
  hiện + vá cùng ngày, xem `note/eurozone_unified_native_coin_plan.md`), và đường
  `recovery_committee_json_override_file` cho production thật không để lộ private key qua
  tooling này.

**Ngoài phạm vi cho đợt P5 này** (đã audit/hardening riêng, không phải trọng tâm mới):
- EVM/MVM core (SSTORE/CREATE2/SELFDESTRUCT/EIP-4844/7702...) — xem
  `project_evm_production_hardening` (lịch sử nội bộ), đã qua 1 đợt hardening + audit riêng.
- Đồng thuận Rust (Block-STM, DAG, commit sync) — ngoài phạm vi bridge, trừ phần
  `peer_rpc` liên quan trực tiếp tới Root Anchor multi-validator (xem mục 3 bên dưới).
- 1 private chain đơn không tham gia cross-chain — đã chạy production nhiều lần, không phải
  code mới.

## 2. Điều đã tự tìm + tự vá — auditor nên xác nhận lại, không audit từ đầu

Nộp nguyên `note/cross_chain_production_readiness_plan.md` (Phase 0 → 0.9) cho auditor —
đây là log đầy đủ, trung thực (kể cả các lần kết luận sai rồi tự sửa) của mọi bug tìm+vá tới
nay. Danh sách rút gọn theo mức độ (mức độ theo quy ước của chính doc kiến trúc, mục 7):

| # | Bug | Mức độ | Trạng thái |
|---|---|---|---|
| 1 | `attestCommit()` verify `aggregateAmount` sai root | 🔴 | Đã vá (PR #56) |
| 2 | `vote()` không xác thực người gửi | 🔴 | Đã vá (PR #56) |
| 3 | `claimDeadChainBalance()` verify account proof sai root | 🔴 | Đã vá (PR #58) |
| 4 | `Refund()` thiếu authorization | 🔴 | Đã vá (PR #60) |
| 5 | Genesis governance deadlock (không ai vote được chain đầu tiên) | Chức năng chặn hoàn toàn | Đã vá — `bootstrapFoundingChains` (PR #61) |
| 6 | Giá trị native/custom-asset chưa từng nối vào số dư thật (chỉ sửa ledger nội bộ) | 🔴 lớn nhất | Đã vá — native (PR #63), custom-asset (PR #64 + theo sau) |
| 7 | `msg.sender` sai ở 4 điểm gọi EVM nội bộ (outbound/claimMessage/refund/verifyAndExecute) | 🔴 | Đã vá (PR #64) |
| 8 | `SmartContractDB.SetCode` không được gọi trong đường Gateway nội bộ | 🟠 | Đã vá (PR #64) |
| 9 | `GlobalSupplyLedger.PerChainAllocation` không có đường nào (governance/bootstrap) từng cấp phát được — trần luôn = 0 vĩnh viễn | 🔴 chức năng chặn hoàn toàn | Đã vá — `ProposalAllocateSupply`/`GrantAllocation` |
| 10 | `propose()`/`vote()`/`executeProposal()` tin timestamp caller tự khai — bypass timelock 72h | 🔴 | Đã vá — luôn dùng block time thật |
| 11 | `bootstrapFoundingChains` không kiểm tra người gửi — front-run ceremony | 🟡 | Đã vá — `GenesisCoordinatorAddress` (opt-in, khoá 1 lần) |
| 12 | `QuorumThreshold` không có sàn an toàn ở 4 nơi gán giá trị — có thể set dưới 2/3 BFT, cho phép thiểu số giả mạo quorum cert | 🔴 | Đã vá — `ValidateQuorumThreshold`, sàn 2/3 |
| 13 | `ProposalRegisterChain` nhận committee không rỗng mà không verify PoP — lỗ hổng rogue-key | 🔴 | Đã vá — `ValidateCommittee` bắt buộc khi committee không rỗng |

**Việc đáng làm nhất cho auditor:** không tin danh sách "đã vá" này tại lời — mỗi mục đều có
test hồi quy thật (BLS/Merkle thật, không mock) đi kèm trong cùng commit, tên test nêu ở
readiness-plan. Chạy `go test ./execution/... -v` và đọc trực tiếp; test title mô tả đúng
kịch bản tấn công.

## 3. Việc audit KHÔNG che được — vẫn cần thêm trước/song song

- **Root Anchor Layer C chưa xong**: cụm multi-validator thật (không phải devnet
  single-validator) có `peer_rpc_port` bind collision khi node "full" khởi động sau node
  "early" — chưa root-cause được (2 lần thử fix không thành, cần `strace`/`lsof` thật). Nghĩa
  là: **mọi bằng chứng "chạy thật" trong Phase 0.9 đều trên single-validator per chain** —
  auditor nên biết rõ ranh giới này, không giả định đã có bằng chứng multi-validator BFT thật
  cho riêng Root Anchor (private chain đơn thì multi-validator đã chạy production nhiều lần,
  không liên quan Layer C).
- **T2 (testnet ≥4 chain thật, nhiều máy độc lập) chưa chạy** — mọi live-verification trong
  Phase 0.7-0.9 đều trên 1 máy chia sẻ. Audit code không thay thế quan sát hành vi production
  thật qua thời gian (độ trễ mạng thật, máy chủ độc lập, tải thật).
- **T3 (kiểm thử đối kháng chủ động)** — mô phỏng chiếm 3/4 validator 1 chain nhỏ, tắt Reserve
  tạm thời, Chain-Death Recovery đầu-cuối trên testnet thật — chưa làm, nên làm song song hoặc
  ngay sau P5, không thay thế P5.
- **Weakest-link risk (mục 5.2 kiến trúc doc)**: đã có cơ chế chặn (`per_chain_allocation`
  ceiling chủ động), nhưng đây là quyết định kiến trúc-mức, auditor nên tự đánh giá độc lập
  chứ không chỉ tin implementation đúng đặc tả là đủ.

## 4. Gợi ý cho auditor tập trung trước (dựa trên tỷ lệ tìm bug thật của nhiều đợt review nội bộ)

Base rate quan sát được: **13 bug/gap thật tìm được qua ~5 đợt review độc lập trên cùng 1
module** (không phải review 1 lần rồi xong) — phần lớn nằm ở đúng 2 dạng lặp lại:

1. **"Cơ chế đã thiết kế/code đúng nhưng chưa từng được nối vào production path nào"** — bug
   #6, #9, #11 ở trên đều dạng này: field/hàm tồn tại, test unit riêng lẻ pass, nhưng 0 nơi
   gọi tới từ đường transaction thật. Cách tìm: với mỗi cơ chế bảo vệ, hỏi "hàm/field này
   được set/gọi từ constructor hay ABI handler thật nào, hay chỉ test tự set giá trị?" —
   `grep -rn` tên hàm ngoài file test là bước đầu tiên, không phải bước cuối.
2. **"Giá trị caller tự khai được tin mà không đối chiếu state thật"** — bug #1, #7, #10, #12
   đều dạng này (root sai, sender sai, timestamp sai, `QuorumThreshold` không sàn). Với mọi
   tham số ABI đi vào 1 hàm verify/mint/timelock, hỏi "giá trị này có thể được ràng buộc mật
   mã/đối chiếu state thật không, hay chỉ đang được echo lại?"
3. **"1 trong nhiều đường ghi cùng 1 field bị bỏ sót khi thêm bảo vệ mới"** — bug #13
   (`ProposalRegisterChain` thiếu PoP dù `BootstrapFoundingChains`/`ProposalUpdateCommittee`
   đều có) là ví dụ: khi 1 bảo vệ (PoP, sàn threshold) được thêm ở 1 đường ghi, luôn `grep -rn`
   TẤT CẢ các đường ghi khác tới cùng field/effect đó — bug #12 cũng là dạng này (áp dụng cho
   cả 4 nơi gán `QuorumThreshold`, không chỉ đường mới thêm).

## 5. Cách build + chạy test cho auditor

```bash
cd execution && go build ./... && go vet ./... && go test ./... -v
```

Không có bước ẩn — không cần thiết lập gì thêm để build/test. Để chạy thử devnet thật (không
bắt buộc cho audit code, hữu ích để xem hành vi runtime): `deploy/systemd/README.md`.
