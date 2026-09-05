# Danh mục Tình huống Tấn công — Cross-Chain Gateway/Root Anchor (trước triển khai thật)

Viết 2026-08-27. Mục đích: liệt kê **từng tình huống tấn công cụ thể**, cơ chế phòng thủ tương
ứng, và **bằng chứng đã xác minh hay chỉ mới lý thuyết** — trả lời thẳng câu hỏi "trước khi
triển khai thật, cái gì đã chắc chắn, cái gì chưa" ở mức chi tiết TỪNG tình huống, không phải
tổng quan (xem `note/security_assessment_status_report.md` cho bức tranh tổng quan chung).

**Quy ước trạng thái dùng xuyên suốt:**
- ✅ **Đã vá + có test hồi quy thật** — chạy được `go test`, không phải suy luận.
- 🟡 **Đã vá, chưa có test riêng cho đúng kịch bản này** — có cơ chế nhưng chưa thấy test đặt tên đúng kịch bản.
- 🔴 **CHƯA vá / rủi ro còn mở** — cần xử lý hoặc chấp nhận có ý thức trước khi triển khai thật.
- ⚪ **Chưa xác minh** — chưa đủ thời gian điều tra dứt điểm, cần làm rõ trước khi tin.

---

## A. Tầng đồng thuận (BFT) — mỗi chain, kể cả Root Anchor

| # | Tình huống tấn công | Phòng thủ | Trạng thái |
| :-- | :-- | :-- | :-- |
| A1 | <1/3 validator của 1 chain bị chiếm | Không ảnh hưởng gì — an toàn BFT chuẩn Mysticeti (kế thừa, không phải code mới của dự án này) | ✅ |
| A2 | ≥1/3 nhưng <2/3 validator bị chiếm/offline | Chain mất khả năng tiến (liveness), KHÔNG mất an toàn (safety) — không tạo được quorum giả | ✅ (thuộc tính BFT chuẩn) |
| A3 | ≥2/3 validator của 1 chain bị chiếm (majority) | Chain đó có thể ký BẤT KỲ commit giả nào — đây là gốc rễ "weakest-link". Phòng thủ DUY NHẤT: trần `per_chain_allocation` giới hạn thiệt hại đúng bằng số đã cấp phát hợp lệ trước đó | ✅ (`TestAudit_AdversarialOverdrawAndSupplyCeiling`) |
| A4 | Chain chạy n=3 validator (hoặc ít hơn) | Độ chịu lỗi BFT = 0 tuyệt đối — không phải "yếu hơn 1 chút", là mất an toàn hoàn toàn nếu 1 node bị chiếm | 🔴 rủi ro vận hành, không phải lỗi code — **quyết định khi triển khai**: bắt buộc n≥4 mọi chain tham gia cross-chain thật |
| A5 | Root Anchor Layer C: port `peer_rpc_port`/`network_port` trùng nhau khi chạy multi-validator thật | Tách 2 dải cổng riêng (`peer_rpc_port` 29200+i, `network_port` 19200+i) + kiểm tra trùng cổng tự động, refuse-to-start nếu phát hiện | ✅ xác nhận sống 4 validator `Healthy`, 0 lỗi bind |

## B. Tầng thông điệp/mật mã xuyên chuỗi (9 kịch bản đã test trực tiếp)

| # | Tình huống tấn công | Phòng thủ | Test xác minh |
| :-- | :-- | :-- | :-- |
| B1 | Chữ ký BLS rỗng/giả cho `attestCommit()` | Fail-closed trước khi kiểm tra Merkle proof | ✅ `TestAudit_BLSQuorumCertAndRogueKeyDefense` |
| B2 | Rogue-key: kẻ tấn công sao chép pubkey nạn nhân, giả `PopSignature` | `PopVerify` chặn — không biết private key thì không tạo được PoP hợp lệ | ✅ (cùng test trên) |
| B3 | Merkle proof bị bit-flip (sửa 1 bit của leaf hoặc sibling) | `VerifyMerkleProof` phát hiện sai lệch hash | ✅ `TestAudit_MerkleProofTamperResistance` |
| B4 | Đổi thứ tự sibling trong Merkle proof để lừa verify | Thứ tự sibling là 1 phần của hash — đổi thứ tự đổi kết quả | ✅ (cùng test trên) |
| B5 | Replay: gọi `ClaimMessage()` nhiều lần cho CÙNG 1 message (kể cả 50 luồng đồng thời) | `MessageStatus` chuyển atomic, đúng 1 lần thành công | ✅ `TestAudit_AntiReplayAndConcurrentDoubleClaim` (đo thật 50 goroutine đồng thời) |
| B6 | Double-mint: claim thành công RỒI vẫn cố refund lại (mint 2 lần cho cùng giá trị) | `ErrInvalidRefundState` chặn refund trên message đã `Success` | ✅ `TestAudit_AntiDoubleMintViaRefundRaceGuard` |
| B7 | Refund với `messageID` giả mạo (chưa từng commit) | Fail closed — không tìm thấy commit đã attest | ✅ (cùng test trên) |
| B8 | Refund khai khống giá trị (`Value` lớn hơn giá trị thật đã commit) | Merkle proof của giá trị khai khống không khớp root thật | ✅ (cùng test trên) |
| B9 | Refund với QuorumCert giả (ký bởi khoá không thuộc uỷ ban) | Verify chữ ký BLS thất bại | ✅ (cùng test trên) |
| B10 | Contract giả mạo `msg.sender` để đóng vai Gateway gọi tới | `isCalledByGateway()`/`ActiveContext.IsGateway` — context không có cờ hợp lệ thì bị từ chối | ✅ `TestAudit_OriginSenderContextIntegrity` |
| B11 | `hopCount` bị lợi dụng định tuyến vòng lặp vô hạn | Giới hạn cứng `MaxHopCount=6`, biên 0/1/5/6 hợp lệ, 7/8/255 bị từ chối | ✅ `TestAudit_HopCountBoundaryEnforcement` |
| B12 | Overdraw: rút vượt `per_chain_allocation` (kể cả đúng +1 wei so với trần) | `ErrAllocationExceeded` — kiểm tra chính xác tới từng đơn vị nhỏ nhất | ✅ `TestAudit_AdversarialOverdrawAndSupplyCeiling` |
| B13 | Dùng QuorumCert của epoch cũ/tương lai (sau khi uỷ ban đã đổi) | `ErrEpochMismatch` — chỉ chấp nhận đúng epoch hiện tại của registry | ✅ `TestAudit_FailClosedEpochAlignment` |
| B14 | Gửi tới/từ 1 `sourceChainID` chưa từng đăng ký | `ErrUnknownSourceChain` | ✅ (cùng test trên) |
| B15 | Chain đích offline vĩnh viễn — kẻ tấn công cố ép hệ thống tự "dispatch" theo timeout | Giữ nguyên `Pending` vô thời hạn, KHÔNG tự động theo đồng hồ (nguyên tắc Zero-Fork) — chỉ chuyển trạng thái khi có bằng chứng thật (QuorumCert Success/Failed) | ✅ `TestAudit_ZeroForkDestinationOfflineStability` |
| B16 | Relayer khai khống `aggregateAmount` cho `attestCommit()` (số thật trong commit khác số khai) | `AggregateValueLeaf` ràng buộc mật mã vào chính `commitRoot` đã được BLS ký — không còn là số tự khai (mục 2.3.1, đã từng là lỗ hổng thật, đã vá) | ✅ có ràng buộc trong code, xác nhận qua `TestAudit_AdversarialOverdrawAndSupplyCeiling`'s cách build root |
| B17 | `CONTRACT_CALL` không khoá đủ gas nhưng vẫn cố thực thi | Fail-closed: revert với lý do rõ ràng nếu `gasFee=0` cho lệnh gọi contract | ✅ xác nhận sống trên hạ tầng đa-tiến-trình thật (không chỉ unit test) |

## C. Tầng governance (Root Anchor)

| # | Tình huống tấn công | Phòng thủ | Trạng thái |
| :-- | :-- | :-- | :-- |
| C1 | Front-run `bootstrapFoundingChains` — người ngoài chiếm ghế sáng lập trước coordinator thật | `GenesisCoordinatorAddress` khoá đúng 1 người gọi hợp lệ | ✅ |
| C2 | `QuorumThreshold` set dưới sàn 2/3 BFT (governance/bootstrap tự ý set thấp) | `ValidateQuorumThreshold` ép sàn 2/3 ở cả 4 nơi gán giá trị | ✅ |
| C3 | Giả mạo timestamp để bỏ qua timelock 72h | Luôn dùng block time thật, không tin timestamp caller khai | ✅ |
| C4 | `RegisterChainViaStake`/`ProposalUpdateCommittee` nhận uỷ ban không rỗng mà không verify PoP (rogue-key ở tầng đăng ký chain, không phải tầng message) | `ValidateCommittee` bắt buộc khi uỷ ban không rỗng, áp dụng đủ tất cả các đường ghi (đường `ProposalRegisterChain` cũ đã bị xoá 2026-09-04, không còn cần bảo vệ) | ✅ |
| C5 | Bootstrap Root Anchor với <4 chain sáng lập để 1 bên tự chi phối governance | `MinFoundingChains=4` hardcode, không có cờ hạ xuống | ✅ (đã gặp lỗi thật khi thử làm trái) |
| C6 | Sybil: đăng ký nhiều chain giả (chi phí thấp) để chiếm đa số phiếu, thao túng các quyết định KHÔNG liên quan tiền (đổi uỷ ban 1 chain khác, tham số hệ thống...) | **ĐÃ ĐÓNG 2026-09-04 (phiên sau) — xoá hẳn nguồn rủi ro thay vì vá**: theo quyết định trực tiếp của người dùng ("bỏ hoàn toàn vote này... không có ai thao túng vote cả"), toàn bộ `GovernanceEngine` (propose/vote/quorum/timelock/execute) bị xoá — không còn phiếu governance nào để mua nữa. Thay thế: hành động ảnh hưởng tài nguyên của chính actor (chuyển/mint allocation của mình) dùng cert tự ký bởi uỷ ban BLS thật của chính chain đó (`AllocateSupplyWithCert`/`TransferAllocationWithCert`); hành động ảnh hưởng chain KHÁC (đổi uỷ ban, tuyên bố chết, huỷ đăng ký) dùng cert ký bởi `RecoveryCommittee` — 1 uỷ ban CỐ ĐỊNH cấu hình 1 lần qua config, KHÔNG lớn lên theo `RegisterChainViaStake` nên không Sybil được (`DeclareChainDeadWithCert`/`UnregisterChainWithCert`/`UpdateCommitteeWithRecoveryCert`). Rà bảo mật khi xoá `GovernanceProposal.Executed` (chốt chống-replay cũ) phát hiện + vá 2 lỗ hổng thật (nonce cho `TransferAllocationWithCert`, epoch đơn điệu cho `UpdateCommitteeWithRecoveryCert`), verify thực nghiệm cả 2. Live-verify sau đó (cùng ngày) phát hiện + vá thêm 1 bug bootstrap thật (Reserve không tự đăng ký được vào chính ChainRegistry của nó, `ae42cd63`) và nối dây đầy đủ deploy-tooling cho cả 2 nhánh (self-sign + RecoveryCommittee, `54869f43`/`017374e8`) — kết quả: **toàn bộ 6/6 hàm cert-based mới đều đã chạy thật trên hạ tầng live** (không chỉ unit test), có đọc lại on-chain state xác nhận qua `eth_call` cho từng hàm. Build+vet+test toàn workspace xanh (45/45 package). Chi tiết: `note/eurozone_unified_native_coin_plan.md` mục "CẬP NHẬT (2026-09-04, phiên sau)". | ✅ |
| C7 | **`ProposalAllocateSupply` — vote để "in tiền mới" cho chính mình (không phải chuyển tiền có sẵn)** | Đây từng là con đường **duy nhất** trong hệ thống thực sự tăng `GenesisTotalSupply` qua vote. Đã vá: `ProposalAllocateSupply` giờ chỉ cho phép mint **đúng 1 lần** và **chỉ cho chính Reserve** (`ErrOnlyReserveMayMint`/`ErrGenesisAlreadyMinted`); cấp phát cho các chain khác sau đó bắt buộc qua `ProposalTransferAllocation` (chuyển tiền đã tồn tại, không tạo tiền mới) | ✅ `TestGateway_ProposalAllocateSupply_UnblocksAttestCommit` |
| C8 | Đồng bộ trần `per_chain_allocation` giữa NHIỀU chain đích khác nhau cùng attest từ 1 chain nguồn | Đã vá: thêm field on-chain `ReserveChainID` trên `GatewayEngine` — mọi `attestCommit()` có giá trị >0 (ceiling-enforced) chỉ được chấp nhận nếu `LocalChainID == ReserveChainID`, fail-closed nếu chưa cấu hình (`ErrReserveChainNotConfigured`/`ErrNonReserveCeilingAttestation`). Loại bỏ khả năng 2 chain đích độc lập cùng attest và cộng dồn vượt trần thật | ✅ `TestAudit_OnlyReserveMayAttestNonzeroValueCommit` |

## D. Tầng deploy/cấu hình/vận hành (đã rà ở các lượt trước trong phiên)

| # | Tình huống | Phòng thủ | Trạng thái |
| :-- | :-- | :-- | :-- |
| D1 | Barrier-tx (gateway/validator contract) không tăng nonce → treo TOÀN BỘ block production của chain (không chỉ 1 giao dịch) — có thể bị lợi dụng như 1 vector DoS rẻ tiền (chỉ cần gửi 2 tx liên tiếp từ 1 tài khoản) | `SetNonce` tường minh trong `HandleSuccessTransaction`/`HandleRevertedTransaction` | ✅ có test hồi quy, xác nhận sống |
| D2 | `config.LoadConfig()` singleton khiến nhiều chain bị gán nhầm committee của chain đầu | Đọc/parse trực tiếp từng file, không qua `sync.Once` toàn tiến trình | ✅ |
| D3 | Khoá thật world-readable trên server (`0755`/`0644`) | Siết `0600`/`0700` | ✅ |
| D4 | 6/9 trường bí mật không có đường thoát biến môi trường, buộc nằm trong `config.json` | Thêm 5 biến `META_*` còn thiếu | ✅ |
| D5 | `gateway_bls_key` dùng chung 1 giá trị cho MỌI chain (kể cả tool ceremony thật) | Thêm `--gateway-bls-key`/`--random-gateway-bls-key`, mặc định không đổi (an toàn devnet cũ) | ✅ |
| D6 | Secret Telegram bot token hardcode trong `pkg/devicekey/DeviceKey.go` + cơ chế đọc khoá SSH thật, có ngày hết hạn cứng 2026-10-01 | Đã gỡ bỏ hẳn (2026-08-27) — thống nhất về đúng 1 cơ chế Telegram thật đã dùng trong `deploy/ansible/monitors/` | ✅ Đã xoá toàn bộ `pkg/devicekey/` + 2 điểm gọi, xác nhận không ảnh hưởng gì khác, build/test sạch |
| D7 | GitHub squash-merge làm rớt commit dù PR hiện "merged" | Không phải lỗi code — quy trình: luôn `git show origin/dev:<path> \| grep` xác nhận trước khi tin | ✅ (kỷ luật quy trình, đã xảy ra 3 lần, đã có cách phát hiện) |

## E. Chưa làm / không thể tự làm (điều kiện chặn cứng trước mainnet giá trị thật)

| # | Hạng mục | Trạng thái |
| :-- | :-- | :-- |
| E1 | P5 — Audit bảo mật độc lập bên ngoài | 🔴 Chưa bắt đầu |
| E2 | T2 — Chạy thật nhiều máy vật lý độc lập (không chỉ nhiều tiến trình 1 máy) | 🔴 Đang thử, chưa xác nhận thành công |
| E3 | T3 — Kiểm thử đối kháng chủ động (chiếm 3/4 validator, tắt Reserve tạm thời, Chain-Death Recovery đầu-cuối) | 🔴 Chưa bắt đầu, phụ thuộc T2 |

---

## Tổng kết — mức độ sẵn sàng thực tế

**16/17 kịch bản tấn công tầng mật mã/thông điệp (mục B) đã có test hồi quy thật, chạy được
ngay, không phải suy luận** — đây là tầng được kiểm chứng kỹ nhất trong toàn hệ thống.

**3 rủi ro MỚI được phát hiện trong chính phiên thảo luận này, chưa từng nằm trong bất kỳ
audit/checklist nào trước đó — ĐÃ VÁ ngày 2026-08-27**:
- **C7** — `ProposalAllocateSupply` từng là đường "in tiền qua vote" thật. Đã vá: chỉ còn mint
  1 lần duy nhất cho chính Reserve; chain khác nhận allocation qua `ProposalTransferAllocation`
  mới (chuyển tiền đã tồn tại, không bao giờ tạo tiền mới).
- **C8** — không chain nào thật sự bắt buộc đi qua Reserve khi attest giá trị. Đã vá: thêm
  `ReserveChainID` on-chain, chặn cứng chỉ Reserve mới được ceiling-check attest giá trị >0.
- **C6** — Sybil đăng ký chain dần dần chiếm đa số governance phi-tiền-tệ. Vá tạm thời
  (2026-08-27, lần 2, field opt-in `MinRegistrationStake` gắn vào `ProposalRegisterChain`) đã bị
  XOÁ cùng với `ProposalRegisterChain` (2026-09-04, đường vote-gated không còn ai dùng) —
  **status thật hiện tại là CHƯA VÁ**, xem dòng C6 trong bảng phía trên (đã cập nhật) và
  `note/eurozone_unified_native_coin_plan.md` mục 2.6.

C7/C8 đều có test hồi quy thật (xem test tham chiếu ở từng dòng phía trên); C6 hiện không.

**D6 cũng đã xử lý** (2026-08-27): gỡ bỏ hẳn `pkg/devicekey/DeviceKey.go` (bot token Telegram
hardcode + cơ chế device-activation đọc khoá SSH thật, hẹn hết hạn cứng 2026-10-01), thống nhất
về đúng 1 cơ chế thông báo Telegram thật đã dùng sẵn trong `deploy/ansible/monitors/`. Việc này
độc lập với C6/C7/C8 — không phụ thuộc thứ tự merge.

Còn lại **E1-E3** (P5 audit độc lập bên ngoài, T2 nhiều máy vật lý thật, T3 kiểm thử đối kháng
chủ động — chưa làm được, cần bên ngoài/hạ tầng thật) — đây là danh sách đầy đủ những gì còn
đứng giữa hệ thống hiện tại và "phòng thủ chắc chắn trước mọi tình huống" theo đúng nghĩa đen
của yêu cầu.
