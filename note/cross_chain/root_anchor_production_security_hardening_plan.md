# Root Anchor Production Security Hardening — Kế hoạch

> **Nguồn gốc:** user paste 1 bản phân tích (do 1 AI khác viết) về những gì Root Anchor/Reserve lưu
> trữ về private chain, so sánh với Polkadot/Cosmos/Rollups, nêu 4 thiếu sót. Tài liệu này: (1) đối
> chiếu lại 4 phát hiện đó với code + tài liệu THẬT đang có trong repo (một số đã được thiết kế chi
> tiết từ trước, chưa build; một số là gap thật, chưa ai giải quyết), (2) đưa ra kế hoạch cải thiện
> theo hướng production, ưu tiên theo tỉ lệ giá-trị/công-sức, không phát minh lại những gì đã có.

---

## 0. Đối chiếu 4 phát hiện với hiện trạng thật

| # | Phát hiện (bản paste) | Đối chiếu code/tài liệu thật | Kết luận |
| :-- | :-- | :-- | :-- |
| 1 | 51%/67% validator của private chain bị chiếm có thể ký giả StateRoot/mint khống, chỉ có velocity-limit 20%/24h giảm nhẹ | **ĐÃ có thiết kế chi tiết, CHƯA build**: `note/cross_chain/shard_design_ton_real.md` mục 5.5/8.8 (`SecurityBond` + `SlashOnEquivocation` + ceiling-cap-theo-bond), qua 19 vòng phản biện độc lập. Thiết kế này dùng đúng field/hàm đã tồn tại thật trong `gateway.go` hôm nay (`DeadChains`, `RecoveryCommittee`, `DeclareChainDeadWithCert`, `MinNativeStakeToRegister`, `PerChainAllocation`) — **không phụ thuộc việc có pivot sang kiến trúc "shard" hay không**, build thẳng lên Root Anchor hiện tại được. | Việc cần làm là **implement**, không phải **thiết kế lại từ đầu**. Xem Phase A. |
| 2 | Không có Data Availability — `ArchivalEndpoint` chỉ là 1 chuỗi URL, không xác minh | **Xác nhận ĐÚNG, gap thật, chưa ai giải quyết.** Grep toàn repo: `ArchivalEndpoint` (`types.go:68`) chỉ được đọc/ghi thụ động qua `getChainRegistry` (`gateway_handler.go:2294`), không có logic verify/health-check/enforcement nào. `shard_design_ton_real.md` mục 5.6 có `ShardCheckpoint` (StateRootHash + ValidatorSetHash định kỳ) làm tín hiệu liveness cho `RecoveryCommittee`, nhưng đó KHÔNG phải DA thật (chỉ chứng minh "chain còn tạo được 1 hash", không chứng minh "dữ liệu đứng sau hash đó publicly reconstructable"). | Gap thật, chưa có kế hoạch nào giải quyết trong repo. Xem Phase B. |
| 3 | Không có cơ chế slashing/fraud-proof tự động, hoàn toàn phụ thuộc `RecoveryCommittee` thủ công | **Đúng cho hiện trạng CODE hôm nay** (0 dòng code slashing tồn tại — tự xác nhận qua grep `DeadChains[` chỉ xuất hiện 2 chỗ: ghi ở `DeclareChainDeadWithCert`, đọc ở `ClaimDeadChainBalance`, không hề chặn `attestCommitInternal`/outbound mới — xem Quick Win #0 bên dưới). Nhưng **thiết kế mục 5.5/8.8 đã có 1 đường permissionless, tự động phát hiện được** (`SlashOnEquivocation` — ai cũng submit được 2 `SignedBlock`/`QuorumCert` mâu thuẫn, không cần `RecoveryCommittee` họp) — hẹp hơn "fraud proof" tổng quát (chỉ bắt equivocation, không bắt 1 phát biểu sai nhất quán) nhưng KHÔNG phải hoàn toàn thủ công như bản paste mô tả, một khi được build. | Sẽ tự động được cải thiện khi Phase A build xong; không cần thiết kế thêm phần "tự động hoá" riêng ngoài đường C.2 đã có sẵn. |
| 4 | Không xác minh tính đúng đắn của state transition (không WASM/ZK) — "trust model" chứ không "trustless" | **Xác nhận ĐÚNG, và dự án đã TỰ NHẬN THỨC + GHI RÕ đây là giới hạn có chủ đích**, không phải thiếu sót bị bỏ sót: `shard_design_ton_real.md` mục 5.5 điểm D + mục 10 nói thẳng "giới hạn CĂN BẢN của mọi hệ dựa thuần vào chữ ký BFT", và kiến trúc được xác định rõ là **đa chuỗi kiểu Cosmos** (mỗi chain tự chủ bảo mật, tin qua light-client/chữ ký chéo), **không phải shared-security kiểu TON/Polkadot, không phải Rollup có validity/fraud proof**. Đây là lựa chọn kiến trúc, không phải bug. | Không đưa vào roadmap gần hạn — xem Phase D (giải thích vì sao). |

**Bài học rút ra khi viết plan này:** đừng lấy 1 bản phân tích ngoài (dù đúng về mặt code hiện tại) làm điểm xuất phát duy nhất — luôn đối chiếu với `note/cross_chain/*.md` trước, vì phần lớn các gap "nghe mới" trong lĩnh vực cross-chain của dự án này đã được 1 vòng thiết kế/phản biện sâu đi qua trước đó ít nhất 1 lần.

---

## Quick Win #0 (mới phát hiện khi viết plan này) — `DeadChains` không thực sự chặn gì

**Phát hiện:** `DeadChains[chainID] = true` (do `DeclareChainDeadWithCert`, uỷ quyền bởi `RecoveryCommittee`) hiện **CHỈ** được đọc ở đúng 1 chỗ — `ClaimDeadChainBalance` (`gateway.go:2222`, luồng thu hồi số dư cho người dùng của 1 chain đã chết). Nó **KHÔNG** được kiểm tra ở `attestCommitInternal` (nơi debit `PerChainAllocation[sourceChainID]`, tức là nơi rút tiền THẬT xảy ra) hay ở bất kỳ đường `outbound()`/`claimMessage()` nào khác.

**Hệ quả thật, ngay hôm nay:** nếu `RecoveryCommittee` phát hiện 1 chain bị chiếm và gọi `DeclareChainDeadWithCert` (đúng quy trình khẩn cấp đã thiết kế), **validator bị chiếm của chain đó vẫn tiếp tục `attestCommit()`/rút `PerChainAllocation` bình thường** — "nút dừng khẩn cấp" không thực sự dừng gì cả. Đây chính xác là gap mà `shard_design_ton_real.md` mục 5.5 đã cảnh báo ("Tịch thu Bond KHÔNG tự động chặn rút phần `PerChainAllocation` đã tích luỹ TRƯỚC ĐÓ... cần `require(!DeadChains[sourceChainID])`") — nhưng gap này **tồn tại độc lập với việc có `SecurityBond` hay không**, và có thể vá NGAY, riêng biệt, rẻ, không cần chờ Phase A.

**Đề xuất fix (độc lập, làm trước Phase A cũng được):**
```go
// attestCommitInternal, ngay sau dòng lấy `registry, exists := g.ChainRegistry[sourceChainID]`
if g.DeadChains[sourceChainID] {
    return nil, fmt.Errorf("%w: chain %d", ErrChainDeclaredDead, sourceChainID)
}
```
Cũng nên chặn tương tự ở `Outbound()` (đừng nhận thêm giao dịch gửi ĐẾN 1 chain đã biết chết — đỡ người dùng tự khoá tiền vào ngõ cụt, đúng tinh thần mục 5.3's "đích không chặn giao dịch gửi đến 1 chain đã chết ở hop-1" mà shard-doc đã liệt kê).

**Độ ưu tiên: CAO, độ phức tạp: THẤP** (2 dòng code + test). Khuyến nghị làm ngay, không chờ toàn bộ Phase A.

---

## Phase A — `SecurityBond` + `SlashOnEquivocation` (build thiết kế đã có, ưu tiên cao nhất)

**Đã thiết kế đầy đủ ở `note/cross_chain/shard_design_ton_real.md` mục 5.5 (thiết kế) + mục 8.8 (việc cần code) — tài liệu này KHÔNG lặp lại chi tiết, chỉ tóm tắt phạm vi build và điều chỉnh cho việc build thẳng lên `gateway.go` hiện tại (không phải chờ shard pivot):**

1. **`SecurityBondLedger`** (`gateway.go`, field mới trên `GatewayEngine`, tách biệt `GlobalSupplyLedger`) — khoản khoá vĩnh viễn làm collateral, KHÔNG chuyển thành `PerChainAllocation` lưu thông (khác hẳn `MinNativeStakeToRegister` hiện tại, vốn CHUYỂN THẲNG thành allocation lưu thông — xác nhận qua `RegisterChainViaStake`'s code, dòng ~417-440).
2. **`RegisterChainViaStake`** nhận thêm tham số `bondAmount`, bắt buộc `>= MIN_BOND_TO_REGISTER`.
3. **Ceiling-cap-theo-bond**: `require(PerChainAllocation[X] <= BOND_LEVERAGE * SecurityBond[X])` chèn vào mọi nơi `PerChainAllocation[X]` TĂNG (`TransferAllocationWithCert`, `CreditReserveAllocation`, `RefundReserveAllocation`'s hoàn tiền — trừ nhánh refund cần MIỄN TRỪ theo đúng lý do mục 5.5 đã giải thích, kẻo tự kẹt tiền user đang cố hoàn).
4. **2 đường tịch thu:**
   - Chậm: `DeclareChainDeadWithCert` (đã có) → thêm nhánh tịch thu `SecurityBond[X]`.
   - Nhanh, permissionless: `SlashOnEquivocation(chainID, commitA, commitB)` (hàm mới) — verify 2 `QuorumCert` mâu thuẫn qua `VerifyQuorumCertAgainstRegistry` (đã có, tái dùng nguyên vẹn).
5. **Unbonding Period**: `UnregisterChainWithCert` không hoàn Bond ngay — giữ đủ `UNBONDING_PERIOD >= MAX_TIMEOUT_BLOCKS_tương_đương + T_SLASH_BUFFER` để `SlashOnEquivocation` không bị "rút trước khi bị bắt" vô hiệu hoá (chain hiện tại chưa có khái niệm `MAX_TIMEOUT_BLOCKS` xuyên chain — cần định nghĩa mới hoặc dùng 1 hằng số thời gian độc lập, xem mục "Cần làm thêm so với bản gốc" bên dưới).
6. **Cả 2 đường tịch thu bắt buộc set `DeadChains[X]=true` ngay lập tức** (đóng băng rút thêm) — **phụ thuộc trực tiếp vào Quick Win #0 đã làm xong trước đó**, nếu không tịch thu Bond xong mà `PerChainAllocation[X]` vẫn rút được bình thường thì toàn bộ Phase A vô nghĩa.

### Cần làm thêm so với bản gốc (vì build thẳng lên Root Anchor hiện tại, không qua "shard" framing)

- Bản gốc (mục 5.5) viết trong ngữ cảnh `PerChainAllocation[Y]` tăng qua 3-hop `InTransitTo[Y]` buffer (dành cho luồng value-transfer nội bộ mạng "shard" mới, mục 5.3). Trên `gateway.go` hiện tại, tương đương gần nhất là **`CreditReserveAllocation`** (ghi có sau khi `claimMessage` thành công thật ở đích, xác nhận qua success cert) — ceiling-cap-theo-bond nên chèn ở đây, không phải ở `ClaimMessage` tại chỗ claim (đúng tinh thần "check sớm nhất có thể, trước khi tiền thực sự tới tay user", nhưng với dữ liệu ceiling hiện tại — cần xác nhận lại điểm chèn chính xác khi code, không suy luận suông).
- `MAX_TIMEOUT_BLOCKS`/`UNBONDING_PERIOD` cần định nghĩa mới cho Root Anchor hiện tại — chain hiện tại không có khái niệm "timeout height" xuyên chain thống nhất như thiết kế shard mới (mục 5.4/8.5) đã có; cần 1 hằng số thời gian độc lập hoặc dùng lại đúng `MaxHopCount`/thời gian khối trung bình làm cơ sở ước lượng.
- Cần review lại 7 điểm mục 10 đã tự liệt kê là "cần review code-level độc lập của người trước khi coi là final" TRƯỚC khi code Phase A thật — đừng bỏ qua bước này chỉ vì nó nằm trong 1 doc khác.

**Ưu tiên: CAO NHẤT trong 4 hướng của plan này** — trực tiếp trả lời "thiệt hại tối đa nếu 1 chain bị chiếm" bằng 1 con số cụ thể, đã thiết kế xong 90%, chỉ còn code + test + review người.

---

## Phase B — Data Availability: 3 tầng, từ rẻ tới đắt

Không có kế hoạch nào có sẵn trong repo cho hướng này — đây là phần thật sự MỚI của tài liệu này.

### Tầng 1 (rẻ, làm được ngay) — Checkpoint liveness thật, không chỉ URL thụ động

- Build `ShardCheckpoint`/liveness signal đã spec ở mục 5.6 (StateRootHash + ValidatorSetHash định kỳ, mỗi K block) — dù mục 5.6 viết cho ngữ cảnh "shard", cơ chế báo cáo định kỳ này áp dụng được y hệt cho private chain hiện tại, không cần chờ pivot.
- Thêm 1 job giám sát (chạy cạnh `RelayerDaemon` hoặc độc lập) **chủ động health-check `ArchivalEndpoint`** theo chu kỳ (HTTP GET/RPC ping thật, không chỉ lưu chuỗi) — biến field thụ động thành tín hiệu thật.
- Formalize: N epoch liên tiếp không nộp checkpoint HOẶC `ArchivalEndpoint` không phản hồi → tự động tạo 1 "cảnh báo" (không tự động `DeclareChainDeadWithCert` — đó vẫn là quyết định của người qua `RecoveryCommittee`, nhưng tín hiệu đầu vào giờ tự động thay vì phải ai đó tình cờ phát hiện).

### Tầng 2 (trung bình) — Bắt buộc archival redundancy tối thiểu

- Yêu cầu mỗi chain đăng ký tối thiểu 2 `ArchivalEndpoint` độc lập (không cùng 1 tổ chức/máy chủ) thay vì 1 URL đơn — giảm rủi ro 1 điểm lỗi duy nhất, chưa phải DA thật nhưng nâng độ tin cậy thực tế đáng kể với chi phí gần như 0.
- Cân nhắc thêm 1 checksum/Merkle-proof-of-inclusion cho 1 tập giao dịch mẫu ngẫu nhiên (không phải toàn bộ) mỗi checkpoint, để phát hiện "archival endpoint trả dữ liệu không khớp StateRoot đã submit" — phát hiện data-withholding một phần mà không cần full DA sampling.

### Tầng 3 (đắt, R&D lớn — không khuyến nghị theo đuổi gần hạn)

- DA committee + erasure coding kiểu Celestia, hoặc yêu cầu mọi node validator lưu trữ redundant. Đây là 1 hạng mục R&D độc lập, quy mô tương đương xây 1 hệ DA riêng — không phù hợp cho "production hardening" ngắn/trung hạn của 1 bridge hiện có. Chỉ nên cân nhắc nếu dự án thật sự muốn tiến gần mô hình Rollup, đi kèm quyết định kiến trúc lớn hơn nhiều so với phạm vi tài liệu này.

**Ưu tiên: TRUNG BÌNH** (Tầng 1 nên làm cùng lúc với Phase A vì rẻ và bù đắp trực tiếp cho hạn chế "chỉ bắt equivocation, không bắt 1 phát biểu sai nhất quán" của Phase A — 1 chain ngừng nộp checkpoint thật/archival endpoint chết cũng là 1 tín hiệu mạnh cho `RecoveryCommittee` cân nhắc, độc lập với có bắt được chữ ký sai hay không).

---

## Phase C — Tự động hoá tín hiệu đầu vào cho `RecoveryCommittee`

Không phải "tự động slashing" (đó là Phase A's `SlashOnEquivocation`) mà là tự động hoá việc PHÁT HIỆN + ĐỀ XUẤT, để `RecoveryCommittee` không phải tự rà soát thủ công:

- 1 dịch vụ giám sát độc lập (có thể mở rộng từ `RelayerDaemon`'s metrics hoặc 1 service riêng) theo dõi: checkpoint staleness (Phase B tầng 1), velocity-limit bị chạm trần liên tục (dấu hiệu rút ồ ạt), `attestCommit` revert bất thường tăng đột biến (dấu hiệu committee bất đồng/bị tấn công) — tự động đẩy cảnh báo (Slack/PagerDuty/dashboard) kèm bằng chứng thô cho `RecoveryCommittee` xem xét, KHÔNG tự động hành động thay người (đúng nguyên tắc Zero-Fork Invariant của dự án — không tự dispatch dựa trên heuristic, chỉ con người mới quyết định `DeclareChainDeadWithCert`).

**Ưu tiên: THẤP HƠN A/B** — giá trị thật nhưng phụ thuộc Phase A/B có dữ liệu để giám sát trước; làm sau khi 2 phase kia có nền tảng.

---

## Phase D — State-transition verification (ZK/fraud proof) — KHÔNG khuyến nghị theo đuổi gần hạn

**Đây là điều duy nhất trong 4 phát hiện của bản phân tích mà dự án đã tự xác nhận KHÔNG có ý định giải quyết**, và tài liệu này đồng tình với lựa chọn đó, vì:

- Kiến trúc đã xác định rõ ràng là **đa chuỗi kiểu Cosmos** (mỗi chain tự chủ bảo mật), không phải Rollup — thêm ZK-validity hay fraud-proof cho state transition tuỳ ý (EVM đầy đủ) là 1 hạng mục R&D ngang tầm 1 dự án ZK-EVM riêng (Ethereum tự bản thân đã bỏ hướng "execution sharding cấp L1" để chuyển sang Rollups vì độ phức tạp này — dẫn đúng lý do `shard_design_ton_real.md` mục 692 đã nêu).
- Phase A (bound thiệt hại bằng `SecurityBond`) + Phase B (phát hiện sớm qua DA/liveness) + Phase C (tự động hoá cảnh báo) đã giải quyết đúng câu hỏi thực dụng nhất: **"thiệt hại tối đa là bao nhiêu, và phát hiện nhanh tới đâu"** — mà không cần chứng minh toán học tuyệt đối tính đúng đắn của mọi state transition.
- Nếu dự án sau này thực sự muốn tiến tới mô hình trustless hơn, đó là 1 quyết định kiến trúc lớn (đổi cả trust model, không phải 1 hạng mục "hardening" thêm vào) — nên tách thành 1 sáng kiến riêng, không trộn vào roadmap production-hardening ngắn hạn này.

---

## Bảng ưu tiên tổng hợp

| Hạng mục | Ưu tiên | Độ phức tạp | Phụ thuộc | Đã thiết kế sẵn? |
| :-- | :-- | :-- | :-- | :-- |
| Quick Win #0 — `DeadChains` chặn thật | **CAO, làm trước tiên** | Thấp (giờ-vài giờ) | Không | Có (mục 5.5 đã nêu, chưa build) |
| Phase A — `SecurityBond`/`SlashOnEquivocation` | **CAO NHẤT** | Trung bình-Cao | Quick Win #0 | Có, 90% (mục 5.5/8.8) |
| Phase B tầng 1 — checkpoint liveness thật | Trung bình-Cao | Thấp-Trung bình | Không (song song Phase A) | Có 1 phần (mục 5.6), phần health-check là mới |
| Phase B tầng 2 — archival redundancy | Trung bình | Thấp | Không | Không, mới hoàn toàn |
| Phase B tầng 3 — DA thật (Celestia-style) | Thấp (không khuyến nghị gần hạn) | Rất cao | — | Không |
| Phase C — tự động hoá cảnh báo cho RecoveryCommittee | Trung bình | Trung bình | Phase A + B | Không, mới hoàn toàn |
| Phase D — ZK/fraud proof state-transition | **Không khuyến nghị gần hạn** | Rất cao (R&D riêng) | — | Không, và không nên |

**Thứ tự khuyến nghị:** Quick Win #0 → Phase A (song song Phase B tầng 1) → Phase B tầng 2 → Phase C → (Phase D chỉ nếu có quyết định kiến trúc lớn riêng trong tương lai, ngoài phạm vi doc này).
