# Kiến Trúc Mở Rộng Metanode Đa-Chain: Root Anchor Chain & Native Light-Client Bridge

> **Ngày viết:** 2026-08-22 (v14 — bổ sung ước tính thời gian hoàn thành có hỗ trợ agent; xem mục 0, mục 15)
> **Phạm vi:** Thiết kế kiến trúc cho phép nhiều Metanode private chain trao đổi giao dịch, chuyển tài nguyên ví liên chuỗi, và thống nhất 1 đồng coin native — lấy cảm hứng mô hình Masterchain/Workchain của TON nhưng thiết kế lại phù hợp quy mô private-chain doanh nghiệp.
> **Lưu ý quan trọng:** Tài liệu này **thay thế hoàn toàn** hướng tiếp cận cross-chain cũ (`execution/contracts/cross_chain/CrossChainGateWay_v3.sol`, `CrossChainConfigRegistry.sol`, mô hình "Embassy"). Các file đó có lỗ hổng thiết kế nghiêm trọng (mục 5.1) và **không nên dùng làm nền tảng** — chỉ giữ lại làm tham chiếu lịch sử.

## Tóm tắt điều hành & hướng dẫn đọc (đọc trước tiên)

**Trong 3 câu:** Xây 1 mạng Metanode mới ("Root Anchor / Reserve Chain") làm nơi phát hành native coin duy nhất và registry chung cho mọi private chain; cross-chain message xác thực bằng quorum cert BFT có sẵn của Mysticeti (không xây PKI riêng); mọi chuyển giá trị native coin bắt buộc đi qua Reserve để Reserve thực thi 1 trần cấp phép (`per_chain_allocation`) chặn 1 chain bị chiếm rút giá trị khống ra toàn hệ thống.

**Nếu chỉ đọc 6 mục quan trọng nhất trước khi bắt tay code, đọc theo thứ tự này:**
1. Mục 1.3 — toàn bộ quyết định kiến trúc đã chốt (bảng 10 dòng, tra cứu nhanh).
2. Mục 2 — kiến trúc tổng thể + sơ đồ + cơ chế lõi (2.1-2.6).
3. Mục 5.2 — lý do kiến trúc phải như vậy (rủi ro weakest-link), để không "tối ưu nhầm" làm mất tính năng an toàn khi code.
4. Mục 11 — API/ABI cụ thể (bắt đầu code từ đây), đọc mục 11.1 trước để hiểu đây là pattern Go-native precompile, không phải Solidity contract thật.
5. Mục 13 — bắt buộc đọc nếu implement `attestCommit()`/`claimMessage()` (mục 2.2 điểm 5 và mục 11.3), vì thứ tự tách giao dịch sai sẽ làm mất hết lợi ích song song hoá.
6. **Mục 14 — nhận task cụ thể để làm và bài test bắt buộc phải PASS để coi là Done.** Đây là điểm bắt đầu thực tế nhất nếu bạn được giao 1 task cụ thể, không cần đọc lại toàn bộ mục 1-13 trước — mỗi dòng task ở mục 14 đã dẫn ngược lại đúng mục cần đọc.

Mục 0 (ngay dưới đây) là nhật ký rà soát qua từng phiên bản — hữu ích để hiểu **tại sao** thiết kế đi đến hình dạng hiện tại (đặc biệt nếu có ai hỏi "sao không làm đơn giản hơn"), nhưng không bắt buộc đọc để bắt đầu code. Mục 10 (kịch bản thực tế) và mục 12 (test/rollout) nên đọc trước khi viết integration test.

---

## 0. Nhật ký rà soát (v2 → v14)

Sau khi hoàn thành bản v1, đã chủ động rà soát lại toàn diện để tìm case/rủi ro còn sót. Các bổ sung quan trọng trong v2:

1. **Phát hiện mới, mức độ nghiêm trọng cao nhất trong toàn tài liệu**: rủi ro hệ thống "weakest-link" — an toàn của đồng coin thống nhất bị giới hạn bởi chain **yếu nhất** trong registry, không phải chain mạnh nhất (mục 5.2, risk #1 mới trong ma trận mục 7). Đây là lỗ hổng ở cấp *mô hình*, không phải lỗi code — cần quyết định kiến trúc rõ ràng trước khi triển khai (mục 5.2.1 đề xuất 2 phương án).
2. Bổ sung thiết kế còn thiếu ở v1: luồng hoàn tiền/timeout khi giao dịch đích thất bại (mục 2.5) — v1 mới thay thế phần verify, chưa thiết kế lại phần refund, để hở nguy cơ double-mint.
3. Bổ sung các rủi ro chưa xét: quản trị `ChainRegistry` (ai được thêm/xoá chain), rogue-key BLS, data-availability khi pruning, MEV ở nhánh gọi contract, giả mạo `AssetRegistry`, DoS kinh tế vào bước verify (mục 5.4).
4. Cập nhật ma trận rủi ro (mục 7) và lộ trình (mục 8) theo các phát hiện trên.

**Bổ sung v3 (theo yêu cầu rà soát kỹ 2 trường hợp cụ thể):**

5. Mục 2.6 (mới): ngữ nghĩa đầy đủ khi 2 private chain gọi lẫn nhau — không có tính nguyên tử liên chuỗi, vòng lặp gọi chéo cần giới hạn hop, chọn ordered/unordered channel, xác thực origin sender 2 chiều, định mức gas cho lệnh gọi inbound.
6. Mục 5.2.2 (mới): trả lời trực tiếp câu hỏi "chain chết thì rút tiền được không" — phân biệt 3 mức độ chết (tạm ngừng / chết còn dữ liệu / chết mất dữ liệu), chỉ ra **yêu cầu kiến trúc còn thiếu** (phải anchor state root từng account, không chỉ tổng số, mới có thể phục hồi), và quy trình "Chain-Death Recovery" có quản trị + giới hạn không thể vượt qua (data availability).

**Bổ sung v4 (đối chiếu với TON theo câu hỏi trực tiếp):**

7. Mục 5.2.3 (mới): so sánh với mô hình shard của TON — chỉ ra **TON không "giải" được bài toán weakest-link, TON tránh nó xuất hiện** bằng cách không cho shard có validator/an ninh độc lập (mọi shard dùng chung 1 pool stake toàn mạng, luân phiên phân công). Đây là khác biệt gốc rễ với yêu cầu "private chain có chủ quyền riêng" của Metanode — kết luận: không thể có cả an ninh đồng đều tuyệt đối kiểu TON *và* chủ quyền độc lập hoàn toàn cho từng private chain, đây là đánh đổi cần chấp nhận, không phải lỗ hổng có thể "vá" hết.

**Bổ sung v5 (chốt khuyến nghị theo câu hỏi trực tiếp "có nên đi theo TON không"):**

8. Mục 5.2.4 (mới): khuyến nghị dứt khoát **KHÔNG** chuyển toàn hệ thống sang mô hình "shared validator pool" kiểu TON — vì mâu thuẫn với giá trị cốt lõi "private chain tự chủ" đã là tính năng sản phẩm có sẵn (`private_chain_guide.md`, `gen_private_chain.py`), và chi phí kỹ thuật lớn hơn hẳn thiết kế Root Anchor hiện tại. Giữ nguyên phương án A + anchor state-root account-level (mục 5.2.1/5.2.2) là đủ để bọc rủi ro nghiêm trọng nhất. Đề xuất thêm (tuỳ chọn, không bắt buộc): 1 "Managed/Shared Validator Tier" song song cho khách hàng không cần chủ quyền tuyệt đối, tương tự mô hình Interchain Security của Cosmos/parachain của Polkadot.

**Bổ sung v6 (chốt kiến trúc thống nhất theo yêu cầu rà soát tổng quan trước khi triển khai thật):**

9. **Mục 1.3 (mới):** bảng quyết định cuối cùng cho toàn bộ 8 vấn đề còn để ngỏ ở v1-v5 — không còn "phương án A hay B", mỗi vấn đề có đúng 1 quyết định để implement.
10. **Phát hiện quan trọng khi rà soát tổng quan — lỗ hổng tích hợp giữa mục 2 và mục 5.2**: bản v1-v5 tuy *khuyến nghị* Phương án A (custodial) ở mục 5.2.1, nhưng cơ chế lõi ở mục 2.2/2.3 **chưa thực sự implement** phương án đó — `local_supply` chỉ là số các chain **tự báo cáo lên Root Anchor để đối chiếu sau** (passive audit), không có bước chặn chủ động nào ngăn 1 chain bị chiếm tự mint vượt mức rồi rút giá trị ra trước khi bị phát hiện. Đã viết lại mục 2.1-2.3: `GlobalSupplyLedger.per_chain_allocation` giờ là **trần thực thi chủ động** do chính Reserve ghi và kiểm tra TRƯỚC khi cho phép phát hành tiếp — đây là khác biệt giữa "khuyến nghị dùng custodial model" và "thực sự implement custodial model". Đã cập nhật sơ đồ kiến trúc (mục 2), độ trễ (mục 4), và kết luận rủi ro #1 (mục 5.2, mục 7) theo đúng cơ chế mới.
11. Phân loại rõ 2 loại message (mục 2.2): message thuần (value=0, đi thẳng A↔B, nhanh) vs message mang giá trị (value>0, bắt buộc qua Reserve, chậm hơn nhưng an toàn) — tránh hiểu nhầm mọi cross-chain call đều phải qua Reserve.

**Bổ sung v7 (rà soát tổng thể theo yêu cầu "tài liệu đã đầy đủ chưa" + bổ sung kịch bản thực tế):**

12. Bổ sung 2 quyết định còn thiếu vào mục 1.3 (#9, #10): xác nhận địa chỉ ví **không cần đổi khác nhau giữa các chain** (đã kiểm chứng bằng code — `crypto.PubkeyToAddress` không có chain_id salt); và phát hiện khoảng trống thật — **chưa có mô hình trả phí cho relayer**, đã bổ sung mô hình "relay tip" tối thiểu ở mục 2.2.1.
13. **Mục 10 (mới):** 8 kịch bản sử dụng thực tế viết theo góc nhìn người dùng/vận hành (chuyển coin đơn giản, gọi contract kèm giá trị, thất bại-hoàn tiền, chain đích tạm ngừng, gọi 2 chiều A→B→A, onboard chain mới, tấn công bị chặn bởi trần cấp phép, Chain-Death Recovery từ góc nhìn người dùng cuối) — dùng để kiểm chứng thiết kế "chạy trên giấy" trước khi code, không phát sinh cơ chế mới ngoài mục 1-9.
14. **Đánh giá độ đầy đủ:** tài liệu đã đủ chi tiết cho giai đoạn đặc tả kỹ thuật (P0-P3). Các khoảng trống còn lại, CHƯA cần giải quyết trước khi bắt đầu code (để ở P4 trở đi theo lộ trình mục 8): cơ chế đấu giá/tối ưu phí relayer nâng cao, API/ABI chi tiết của `GatewayPrecompile` (chi tiết cài đặt, không phải quyết định kiến trúc), kịch bản test/rollout theo giai đoạn (testnet trước mainnet) chưa được viết thành quy trình cụ thể — nên bổ sung trước P5.

**Bổ sung v8 (chi tiết hoá theo yêu cầu — 2 khoảng trống ở điểm 14 đã được vá):**

15. **Mục 11 (mới):** đặc tả API/ABI cụ thể của `GatewayPrecompile` — struct `CrossChainMessage`/`QuorumCert`/`MerkleProof`, các hàm ghi (`outbound`, `verifyAndExecute`, `claimDeadChainBalance`), hàm đọc bắt buộc dùng cho contract nhận cross-chain call (`getOriginalSender`, `isCalledByGateway`), và event `AllocationRejected` — dùng làm tín hiệu cảnh báo sớm ngay khi mục 2.3 chặn 1 chain rút vượt mức (thay vì chờ đối chiếu định kỳ). Đủ cụ thể để P2 bắt đầu code trực tiếp.
16. **Mục 12 (mới):** quy trình kiểm thử/rollout 6 giai đoạn (T0 unit test → T1 devnet chạy tự động 8 kịch bản mục 10 → T2 testnet ≥4 chain thật → T3 kiểm thử đối kháng chủ động mô phỏng đúng kịch bản 10.7/10.8 → P5 security review → T4 rollout mainnet 3 bước tăng dần hạn mức → T5 bug bounty tuỳ chọn) — mỗi giai đoạn có điều kiện qua (gate) rõ ràng, không giai đoạn nào được rút ngắn bằng suy đoán thay vì đo đạc thật.

**Bổ sung v9 (rà soát lại schema + phân tích thông lượng theo yêu cầu):**

17. **Mục 11.6 (mới):** rà soát lại phát hiện 5 struct được nhắc bằng lời ở các mục trước nhưng chưa có schema hình thức — bổ sung dứt điểm: `AssetEntry`, `MessageStatus`/`Channel`, `ValidatorEntry` (có `popSignature` tường minh), `GovernanceProposal` (biểu quyết theo SỐ CHAIN, không theo stake — tránh 1 chain lớn chi phối quản trị), `AccountLeaf` (leaf cho Merkle proof phục vụ Chain-Death Recovery). Mục 11 giờ đủ để implement trực tiếp không cần suy đoán thêm cấu trúc dữ liệu nào.
18. **Mục 13 (mới):** phân tích thông lượng (khác độ trễ ở mục 4) — phát hiện quan trọng: **Reserve là điểm hội tụ thông lượng của TOÀN mạng cho mọi message có giá trị** (đánh đổi trực tiếp của Phương án A), và **chi phí verify BLS pairing là trần thông lượng thực sự** nếu verify naive theo từng message (đối chiếu với chính precedent trong repo: BLS verify mempool đã bị tắt cho tx thường vì tốn hiệu năng — nhưng KHÔNG được tắt cho cross-chain vì đó là nền tảng an toàn). Đã cập nhật mục 12 (T2) thêm bước đo thông lượng thật, không chỉ đo độ trễ.

**Bổ sung v10 (thiết kế tận dụng thực thi song song, theo yêu cầu rà soát riêng):**

19. **Đính chính quan trọng ở mục 13.2 (v9):** sau khi đọc kỹ `note/block_stm_architecture_review.md` (cơ chế Block-STM/Union-Find thật của Metanode — song song hoá GIỮA CÁC GIAO DỊCH bằng `RelatedAddresses`/`AccessList`, KHÔNG song song hoá vòng lặp bên trong 1 giao dịch), phát hiện `verifyAndExecuteBatch()` (thiết kế v9) tuy tiết kiệm chi phí BLS nhưng **không** tận dụng được thực thi song song vì là 1 giao dịch duy nhất chạy vòng lặp nội bộ.
20. **Thiết kế lại mục 13.2-13.4: mô hình "Attest-then-Claim"** — tách thành `attestCommit()` (1 giao dịch/1 commit, verify BLS 1 lần, định tuyến vào threadpool "Native Go-Only (BLS)" có sẵn của Block-STM) và `claimMessage()` (N giao dịch riêng biệt, 1/message, rẻ, khai báo AccessList không đụng `per_chain_allocation` → Union-Find nhóm độc lập → chạy thật sự song song trên nhiều lõi CPU). Phát hiện thêm 2 "ô nhớ nóng" (hot storage) có thể gây gộp nhóm ngoài ý muốn nếu không tách đúng: `per_chain_allocation` (mục 13.3.1) và bộ đếm `sequence` phía gửi (mục 13.3.2, đã sửa bằng cách dùng `messageId=txHash` thay cho biến đếm dùng chung cho channel unordered). Cập nhật API mục 11.3, schema `Channel`/`AttestedCommit` mục 11.6, event `CommitAttested` mục 11.5.

**Bổ sung v11 (rà soát "đã sẵn sàng gửi dev chưa" — phát hiện và sửa mâu thuẫn nội bộ):**

21. **Phát hiện mâu thuẫn thật, mức độ nghiêm trọng cao** (đủ để trả lời "CHƯA sẵn sàng" tại thời điểm rà soát): mục 2.2/2.2.1/2.4 và bảng mục 4 — viết trước khi có thiết kế "Attest-then-Claim" (v10) — vẫn mô tả luồng "verify rồi thực thi atomically trong 1 giao dịch" và dùng "channel sequence number tăng dần" làm cơ chế chống replay CHÍNH, trực tiếp mâu thuẫn với mục 11.3/13 (2 pha `attestCommit`/`claimMessage`, chống replay bằng `messageId` cho channel unordered). Một dev đọc mục 2 trước sẽ nhận thông tin sai lệch so với đặc tả kỹ thuật thật ở mục 11/13. **Đã sửa toàn bộ các đoạn liên quan** (mục 2.2, 2.2.1, 2.4, bảng mục 4, struct `CrossChainMessage` mục 11.2 — thêm field `messageId` tường minh) để nhất quán 100% với thiết kế cuối.
22. Bổ sung làm rõ (mục 11.1): các khối code trong mục 11 là **hình dạng ABI**, không phải yêu cầu triển khai bằng Solidity contract thật — implement theo đúng pattern Go-native precompile interceptor đã có sẵn trong repo (`cross_chain_handler` cũ), tránh dev hiểu nhầm phải viết/deploy bytecode Solidity.
23. Thêm mục "Tóm tắt điều hành & hướng dẫn đọc" ở đầu tài liệu — tài liệu đã hơn 850 dòng qua 11 phiên bản, cần 1 điểm bắt đầu rõ ràng cho người đọc lần đầu thay vì phải đọc tuần tự cả nhật ký rà soát.
24. **Kết luận:** sau các sửa trên, tài liệu **sẵn sàng gửi dev** cho giai đoạn P0-P2. Xem câu trả lời đầy đủ trong hội thoại đã tạo ra bản v11 này.

**Bổ sung v12 (chia task cụ thể theo yêu cầu):**

25. **Mục 14 (mới):** chia lộ trình P0-P8 (mục 8) thành các task cụ thể giao được cho từng dev, mỗi task kèm **bài test bắt buộc phải PASS** mới coi là Done (không có khái niệm "code xong, test sau"). Bài test tham chiếu đúng kịch bản mục 10 và giai đoạn T0-T5 mục 12, không phát minh quy trình mới. Task nhạy cảm nhất (P2.2 `attestCommit()`, P2.3 `claimMessage()`) có bài test bắt buộc trực tiếp tái hiện đúng kịch bản tấn công 10.7 và lỗ hổng xác thực origin sender mục 2.6.4 điểm 2 — đảm bảo không chỉ review code mà phải chứng minh bằng test tự động.

**Bổ sung v13 (rà soát "sẵn sàng gửi dev" lần 2, theo đúng câu hỏi lặp lại):**

26. Rà soát lại lần nữa để tìm tham chiếu cũ còn sót sau khi thêm mục 14 — phát hiện 2 chỗ: mục 6 (bảng rủi ro vận hành, dòng "Khôi phục sau crash") còn ghi `channel_sequence` như cơ chế lưu trạng thái chính, và mục 12 (dòng T0 — Unit test) còn ghi "chống replay theo `sequence`" không phân biệt ordered/unordered. Cả 2 đã sửa khớp đúng thiết kế cuối (`Channel.statusByMessageId`/`AttestedCommit`, và messageId cho unordered + sequence chỉ cho ordered). Đã rà lại toàn bộ mục 5 (rủi ro bảo mật) — không phát hiện tham chiếu cũ nào còn sót ở đó.

**Bổ sung v14 (ước tính thời gian theo yêu cầu):**

27. **Mục 15 (mới):** ước tính thời gian hoàn thành khi có hỗ trợ agent, giả định nhóm 2-4 dev mỗi người làm cùng 1 agent. Tách rõ 2 loại thời gian: **phần agent rút ngắn được** (viết code+test theo spec mục 11/14 — ~6-9 tuần cho toàn bộ P0-P4/P6-P7) và **phần KHÔNG agent nào rút ngắn được** vì bị giới hạn bởi bên ngoài đội dev (đàm phán chain sáng lập P1.2, security audit ngoài P5 ~4-8 tuần, thời gian quan sát production có chủ đích ở T4 ~2-4+ tháng). Tổng: ~2-3 tháng tới lúc sẵn sàng audit, ~5-8 tháng tới lúc mainnet chạy đầy đủ — nêu rõ đây là ước tính lập kế hoạch kèm 3 biến số cần xác nhận, không phải cam kết.

---

## 1. Mục tiêu & Nguyên tắc thiết kế

### 1.1 Ba mục tiêu bắt buộc

1. **G1 — Trao đổi giao dịch giữa private chain**: Chain A và B gọi được contract, gửi message cho nhau.
2. **G2 — Chuyển tài nguyên ví liên chuỗi**: Ví trên A khoá/đốt tài nguyên, dùng được ở B.
3. **G3 — 1 đồng coin native thống nhất**: Tổng cung native coin toàn hệ thống là bất biến, không thể mint khống ở bất kỳ chain nào.

### 1.2 Nguyên tắc thiết kế cốt lõi (khác biệt với thiết kế cũ)

| Nguyên tắc | Thiết kế cũ (Embassy) | Thiết kế mới (Root Anchor) |
|---|---|---|
| Ai xác nhận 1 message là thật? | Tập hợp "Embassy" — 1 PKI riêng, tách biệt khỏi consensus của chain | **Chính uỷ ban validator (BFT committee) của chain nguồn** — dùng lại quorum certificate mà Mysticeti DAG-BFT đã tạo ra để finalize block |
| Ai được phép relay? | Danh sách Embassy cố định (permissioned) | **Bất kỳ ai** — relayer không có quyền lực đặc biệt, chỉ chuyển dữ liệu kèm bằng chứng tự-xác-minh (permissionless) |
| Registry đặt ở đâu? | Mỗi chain tự lưu 1 bản `CrossChainConfigRegistry` riêng | **1 bản duy nhất** trên Root Anchor Chain, các chain đọc cache có version |
| Điểm neo lỗi (single point of failure) | 1 embassy đơn lẻ có thể submit và hệ thống thực thi ngay (xem mục 5.1) | Phải phá vỡ an toàn BFT (>1/3 stake độc hại) của chain nguồn mới giả mạo được — kế thừa đúng mức độ an toàn hiện có của Metanode |

**Vì sao đổi hướng:** Bản chất bài toán "cross-chain message" **giống hệt** bài toán "một node xác nhận 1 block đã finalize" mà Mysticeti DAG-BFT (`meta-consensus/core/src/stake_aggregator.rs`, `commit.rs::CertifiedCommit`) đã giải xong và đang chạy production. Không cần phát minh lại 1 hệ thống tin cậy (PKI Embassy) song song — chỉ cần **xuất khẩu** bằng chứng finality đã có sẵn sang chain khác. Đây chính là mô hình "light client" mà IBC (Cosmos) và bridge của TON đều dùng — Metanode có lợi thế là quorum cert đã tồn tại sẵn, không phải build mới.

### 1.3 Quyết định kiến trúc cuối cùng (đã chốt — không còn để ngỏ)

Đây là bản tổng hợp **quyết định thật, không phải phương án để cân nhắc thêm**. Mọi mục "cần chốt"/"tuỳ chọn" ở các phiên bản trước (v1-v5) được chốt dứt điểm tại đây. Chi tiết lý do nằm ở mục tương ứng đã dẫn.

| # | Vấn đề | Quyết định cuối | Xem chi tiết |
|---|---|---|---|
| 1 | Mô hình chống weakest-link (mục 5.2.1: A hay B?) | **Phương án A — Custodial/Reserve**, và được **thực thi (enforce) chủ động**, không chỉ là khuyến nghị thụ động (xem mục 2.3 đã viết lại) | 2.3, 5.2 |
| 2 | Chain nào phải anchor state-root account-level? | **Bắt buộc với TẤT CẢ private chain đăng ký**, không có ngoại lệ — vì mọi chain đều giữ IOU của người dùng cần khôi phục được | 5.2.2 |
| 3 | Quản trị `ChainRegistry`/`AssetRegistry` | Biểu quyết on-chain bởi các chain đã đăng ký trên Root Anchor, ngưỡng **≥2/3 số chain đang active**, kèm **delay window 72 giờ** trước khi hiệu lực | 5.4 |
| 4 | Proof-of-possession cho BLS key | **Bắt buộc thêm bước `PopVerify` tường minh khi đăng ký committee lên `ChainRegistry`** (không phụ thuộc việc thư viện nền có sẵn PoP hay không — làm thêm 1 lớp phòng thủ độc lập, chi phí thấp) | 5.4 |
| 5 | Thành phần committee Root Anchor/Reserve lúc khởi động | **Tối thiểu 4 private chain sáng lập** cùng góp validator đại diện (stake-weighted, có trần tối đa % đóng góp mỗi chain để tránh 1 chain chi phối) — dưới 4 chain thì Reserve tự nó là 1 uỷ ban nhỏ, lặp lại đúng vấn đề weakest-link ở tầng cao nhất | 5.2.1, 5.2.4 |
| 6 | Ordered vs Unordered channel mặc định | **Unordered** cho toàn hệ thống theo mặc định; ordered chỉ bật per-channel khi ứng dụng khai báo rõ | 2.6.3 |
| 7 | `hop_count` tối đa | **6** (đủ cho request→response→ack 2 chiều có 1 lần retry, chặn được vòng lặp) | 2.6.2 |
| 8 | Có nên chuyển sang "shared validator pool" kiểu TON không? | **Không** — giữ chủ quyền private chain, xử lý weakest-link bằng quyết định #1/#2 thay vì bỏ chủ quyền | 5.2.4 |
| 9 | Địa chỉ ví có cần đổi khác nhau giữa các chain không? | **Không cần** — đã xác nhận trong code (`execution/pkg/common/common.go:AddressFromPubkey`, dùng `crypto.PubkeyToAddress` chuẩn kiểu Ethereum) địa chỉ chỉ phụ thuộc public key, không có "muối" chain_id. Cùng 1 ví/private key cho địa chỉ **giống hệt nhau** trên mọi private chain và Reserve — không cần thiết kế gì thêm | 10 |
| 10 | Ai trả phí cho relayer, vì sao họ chịu relay miễn phí? | **Chưa có** ở các bản trước — đây là khoảng trống thật, đã bổ sung mô hình "relay tip" ở mục 2.2.1 | 2.2.1 |

---

## 2. Kiến trúc tổng thể

```
                    ┌───────────────────────────────────────────────────┐
                    │      ROOT ANCHOR / RESERVE CHAIN                    │
                    │  (1 Metanode network riêng — NGƯỜI PHÁT HÀNH DUY    │
                    │   NHẤT của native coin, tái dùng nguyên             │
                    │   consensus/metanode + meta-consensus hiện có)      │
                    │                                                      │
                    │  ChainRegistry: { chain_id → committee, state_root, │
                    │                    archival_endpoint, ... }         │
                    │  GlobalSupplyLedger: per_chain_allocation[chain_id] │
                    │   ← TRẦN THỰC THI, chỉ Reserve tự ghi, không nhận  │
                    │     số tự báo cáo từ chain khác (mục 2.3)           │
                    └───────┬───────────────────────────────┬─────────────┘
                 (b) value>0: burn ở A → Reserve   (b) Reserve mint-authorize → B
                 kiểm tra trần per_chain_allocation[A] trước khi cho phát hành tiếp
                            │                                       │
              ┌─────────────▼────────┐         (a) value=0: message thuần    ┌┴───────────────────────┐
              │   PRIVATE CHAIN A     │◄────────đi thẳng A↔B, không qua──────►│   PRIVATE CHAIN B       │
              │  Mysticeti DAG-BFT    │         Reserve (mục 2.2)             │  Mysticeti DAG-BFT      │
              │  (không đổi gì)       │                                       │  (không đổi gì)         │
              │  GatewayPrecompile    │                                       │  GatewayPrecompile      │
              └──────────┬────────────┘                                       └────────────▲────────────┘
                         │ commit certified (per-commit, ~1-2s, KHÔNG chờ epoch)            │
                         ▼                                                                   │
              ┌──────────────────────────────────────────────────────────────────────────────┘
              │        RELAYER (permissionless, không có quyền đặc biệt — bất kỳ full-node nào)
              │  scan MessageOut → lấy commit + quorum_cert + merkle proof → submit tới đích
              │  (đích = Reserve nếu value>0, đích = chain kia trực tiếp nếu value=0)
              └──────────────────────────────────────────────────────────────────────────────
```

### 2.1 Root Anchor Chain — vai trò

Bản thân là **1 mạng Metanode độc lập** (deploy bằng đúng `deploy/ansible`/`deploy/systemd` sẵn có), không phải thành phần mới về công nghệ — chỉ mới về vai trò nghiệp vụ. Validator của Root Anchor nên là **liên uỷ ban (union committee)**: mỗi private chain cử ra 1-vài validator đại diện, stake-weighted theo tổng stake của chain đó. Điều này khiến Root Anchor **khó tấn công hơn từng chain lẻ** (vì kẻ tấn công phải kiểm soát >1/3 stake gộp của toàn hệ thống, không chỉ 1 chain), đúng tinh thần "kế thừa an ninh" mà thiết kế Embassy cũ hoàn toàn không có.

Root Anchor lưu 2 bảng dữ liệu, cả hai đều **ghi qua giao dịch thường, được BFT của chính Root Anchor finalize** — không có khái niệm `onlyOwner` như contract cũ:

```
ChainRegistry {
    chain_id: u64,
    committee: Vec<ValidatorEntry>,                   // xem struct ValidatorEntry mục 11.6 — có PopVerify (mục 1.3 #4)
    epoch: u64,                                       // epoch hiện tại của chain đó
    quorum_threshold: Stake,                          // = committee.quorum_threshold(), tái dùng logic Sui/Mysticeti có sẵn
    gateway_contract: Address,
    state_root: Hash,                                 // BẮT BUỘC (mục 1.3 #2): Merkle root account-tree, không chỉ số tổng — cập nhật mỗi epoch
    archival_endpoint: Uri,                           // nơi lưu preimage phục vụ Merkle proof khi cần Chain-Death Recovery (mục 5.2.2)
    registered_at: Timestamp,
}

// GlobalSupplyLedger là NGƯỜI PHÁT HÀNH DUY NHẤT — không phải sổ audit thụ động.
// per_chain_allocation là TRẦN THỰC THI (enforced ceiling), Root Anchor tự cập nhật
// khi CHÍNH NÓ xử lý 1 message mint/burn — KHÔNG nhận số liệu tự báo cáo từ chain khác.
GlobalSupplyLedger {
    genesis_total_supply: u256,                       // mint 1 lần lúc khởi tạo mạng lưới
    per_chain_allocation: Map<ChainId, u256>,          // trần được phép mint cục bộ của mỗi chain — CHỈ Root Anchor mới được ghi
    invariant_check: Σ per_chain_allocation == genesis_total_supply   // luôn đúng theo thiết kế, không phải "đối chiếu sau"
}
```

`ChainRegistry` được cập nhật **tự động mỗi khi 1 private chain hoàn tất epoch transition** (tái dùng `epoch_transition.rs`/`epoch_checkpoint.rs` đã có — chỉ thêm 1 bước: sau khi epoch mới bắt đầu, node leader gửi 1 giao dịch "CommitteeUpdate" kèm `state_root` mới lên Root Anchor). Vì epoch của Metanode mặc định 300-900s (`genesis-main.json: epoch_duration_seconds`), tần suất cập nhật registry rất thấp — không phải lo hiệu năng (mục 4).

### 2.2 Cơ chế xác minh liên chuỗi (thay cho Embassy)

**Phân biệt 2 loại message, vì chỉ loại (b) mới cần đi qua Reserve:**

- **(a) Message thuần tuý (`value = 0`, chỉ gọi contract)**: không tạo/di chuyển giá trị native coin → **đi trực tiếp A→B**, không cần qua Root Anchor/Reserve. Dùng đúng luồng verify dưới đây.
- **(b) Message có mang giá trị native coin (`value > 0`, hoặc asset dưới `AssetRegistry`)**: **bắt buộc đi qua Root Anchor/Reserve** làm bên phát hành trung gian (mục 2.3) — vì đây là nơi duy nhất có quyền tăng `per_chain_allocation` của 1 chain.

Cơ chế xác minh dùng chung cho cả 2 loại (khác nhau ở việc (b) có thêm 1 hop qua Reserve):

1. Chain nguồn (A, hoặc Reserve khi đóng vai người phát hành) phát sinh outbound message (qua 1 precompile `GatewayPrecompile.outbound(...)`, tương tự `lockAndBridge`/`sendMessage` cũ về mặt API nhưng KHÔNG có logic BLS-embassy).
2. Message này nằm trong 1 **commit** của Mysticeti DAG-BFT. Ngay khi commit đó đạt quorum (2f+1 stake, do `StakeAggregator<QuorumThreshold>` xác nhận — cơ chế **đã chạy production**, không phải xây mới), nó có 1 **`CertifiedCommit`** kèm chữ ký BLS aggregate của uỷ ban validator chain nguồn.
3. Relayer (bất kỳ ai, không cần đăng ký) lấy: `(message, merkle_proof_of_inclusion, CertifiedCommit.aggregate_signature, committee_epoch)` rồi gửi sang chain đích.
4. Chain đích verify độc lập, không cần tin relayer:
   - Lấy `ChainRegistry[nguồn].committee` tại đúng `committee_epoch` (đọc cache từ Root Anchor, tự refresh nếu thiếu — logic tương tự `isDestinationRegisteredWithRefresh` cũ nhưng nay trỏ vào Root Anchor thay vì registry cục bộ).
   - Verify chữ ký BLS aggregate khớp `quorum_threshold` của uỷ ban đó (tái dùng thư viện `pkg/bls`/`blst` đã có sẵn trong repo, KHÔNG cần PKI mới).
   - Verify Merkle proof rằng message thực sự nằm trong commit đã ký.
   - Kiểm tra chống replay: với channel **unordered** (mặc định, mục 1.3 #6) — kiểm tra `messageId` (= tx hash của giao dịch `outbound()` gốc) chưa từng được dùng (`usedMessageHash`/`statusByMessageId`, mục 13.3.2); với channel **ordered** (opt-in) — kiểm tra `sequence` tăng dần liên tục. Đơn giản hơn nhiều so với `eventVoteKey`/`sigAccum` cũ.
5. Nếu cả 3 điều kiện đúng → thực thi (gọi contract đích, hoặc — chỉ với message đến từ chính Reserve — `ProcessNativeMintBurn(0)` để mint). **Với khối lượng thấp** (đường đơn giản `verifyAndExecute()`, mục 11.3): verify + thực thi atomically trong 1 giao dịch. **Với khối lượng lớn** (đường mặc định cho production): tách thành `attestCommit()` (verify BLS 1 lần/commit) + N giao dịch `claimMessage()` riêng biệt để tận dụng thực thi song song — xem chi tiết và LÝ DO tách ở mục 13.2-13.3 (đừng gộp lại thành 1 giao dịch vòng lặp, sẽ mất hết lợi ích song song hoá).

**Hệ quả bảo mật:** Không còn khái niệm "Embassy đơn lẻ có thể submit và hệ thống tin ngay" (lỗ hổng mục 5.1). Muốn giả mạo, kẻ tấn công phải giả mạo được chữ ký BLS aggregate của uỷ ban validator thật của chain nguồn — tức là phải phá vỡ an toàn BFT của chain đó. Với message loại (a) (value=0), đây là mức an toàn kế thừa sẵn có của Metanode, chấp nhận được. Với message loại (b) (mint thật), mục 2.3 bổ sung thêm 1 lớp chặn nữa để không phụ thuộc duy nhất vào an toàn BFT của 1 chain đơn lẻ.

#### 2.2.1 Ai trả phí cho relayer? (khoảng trống phát hiện khi rà soát tổng quan — đã bổ sung)

Relayer được thiết kế **permissionless** (mục 1.2) — nhưng permissionless không có nghĩa là miễn phí: nếu không ai được trả công, về lâu dài sẽ không có relayer nào chủ động quét/relay message, hệ thống sẽ kẹt ở trạng thái `PENDING` vô thời hạn dù không có ai tấn công. Đây là khoảng trống thật, cần 1 mô hình kinh tế tối thiểu (chưa cần phức tạp, đủ dùng cho giai đoạn đầu):

- Người gửi (`outbound()`) khoá kèm 1 khoản **"relay tip"** bằng native coin tại thời điểm gửi (tương tự đã có cơ chế khoá "gas liên chuỗi" ở mục 2.6.5 cho `CONTRACT_CALL` — relay tip là 1 khoản tách biệt, áp dụng cho MỌI message kể cả không gọi contract).
- Relayer nào **submit thành công đầu tiên** (xác định qua trạng thái `statusByMessageId`/tương đương ordered-sequence, mục 13.3.2 — message đã resolve thì các lần submit sau bị từ chối, không tốn gì thêm) sẽ nhận trọn tip đó, trả trực tiếp vào địa chỉ relayer trong cùng giao dịch xử lý (`claimMessage()` cho đường mặc định, hoặc `verifyAndExecute()` cho đường đơn giản — mục 11.3/13.3).
- Vì tip trả theo nguyên tắc "ai xong trước nhận" (không cần đăng ký làm relayer), cơ chế vẫn giữ đúng tính permissionless — chỉ thêm động lực kinh tế, không thêm quyền lực hay danh sách cho phép nào.
- Mức tip nên để **người dùng/wallet SDK tự đặt** (giống phí ưu tiên gas kiểu EIP-1559 tip), không cố định cứng trong giao thức — cho phép thị trường tự điều chỉnh khi tải cao.

Đây là thiết kế mức tối thiểu để hệ thống có động lực vận hành liên tục; chi tiết đầy đủ hơn (relayer đấu giá, batch nhiều message 1 lúc để tiết kiệm phí…) để ở giai đoạn P4 (lộ trình mục 8), không phải điều kiện chặn P0-P3.

### 2.3 Thống nhất native coin (G3) — Reserve là người phát hành duy nhất, có thực thi trần cấp phép

**Đây là phần đã viết lại so với v1-v5 để thực sự khớp với quyết định "Phương án A" ở mục 5.2.1/1.3 — không chỉ là khuyến nghị, mà là cơ chế bắt buộc:**

Nguyên tắc nền tảng: **1 private chain không bao giờ có quyền tự mint local balance vượt quá mức Root Anchor/Reserve đã cấp phép cho nó (`per_chain_allocation[chain_id]`)**. Chain chỉ có toàn quyền tự do với 1 việc duy nhất: **đốt (burn) bớt phần balance nó đang có** — burn không bao giờ phá vỡ bất biến tổng cung, chỉ mint mới có thể.

Luồng cụ thể A→B (cả 2 đều là private chain thường, không phải Reserve):

1. **Burn ở A**: User gọi `outbound(dest=B, value=X)`. A chạy `ProcessNativeMintBurn(1)` (burn cục bộ, giữ nguyên cơ chế TrustZone hardware hiện có ở `tz_hardware_engine.go`) — luôn được phép, không cần xin phép trước.
2. **Relay tới Reserve (không phải thẳng tới B)**: Relayer mang quorum cert của A tới Root Anchor/Reserve. Reserve verify quorum cert của A (đúng cơ chế mục 2.2), sau đó **kiểm tra trần**: `per_chain_allocation[A] >= X`? 
   - Nếu **KHÔNG đủ** (dấu hiệu A đã bị chiếm và tự "burn" nhiều hơn mức từng được cấp) → Reserve **từ chối, không giảm ledger, không phát hành tiếp** — đây chính là điểm chặn kỹ thuật khiến kịch bản tấn công ở mục 5.2 (bước 3-4) **không thể xảy ra được nữa**: kẻ tấn công chiếm A chỉ có thể rút tối đa đúng bằng `per_chain_allocation[A]` hiện có, không thể tạo giá trị mới từ hư không.
   - Nếu **đủ** → Reserve cập nhật `per_chain_allocation[A] -= X`, `per_chain_allocation[B] += X`, rồi Reserve tự phát sinh 1 outbound message MỚI (ký bởi quorum cert của chính Reserve, không phải của A) gửi tới B: "mint X cho user".
3. **Mint ở B**: B verify quorum cert của **Reserve** (không phải của A — B không cần và không nên tin trực tiếp quorum cert của A cho việc mint) rồi chạy `ProcessNativeMintBurn(0)` để credit cho user.

Genesis: `genesis_total_supply` được mint **đúng 1 lần** tại Reserve, `per_chain_allocation` ban đầu của mỗi chain được cấp phát theo thoả thuận lúc onboarding (0 nếu chain mới tham gia chưa nhận allocation nào).

**Vì sao đây là điểm khác biệt quyết định so với v1-v2 (đã có lỗ hổng tích hợp cần sửa):** bản v1-v2 mô tả `local_supply` là **con số các chain tự báo cáo lên Root Anchor để đối chiếu sau** (passive audit) — nghĩa là 1 chain bị chiếm vẫn tự do mint/burn nội bộ trước, chỉ bị "phát hiện" ở lần đối chiếu tiếp theo (đã muộn, giá trị đã rút ra ngoài). Bản v6 này sửa lại: `per_chain_allocation` là **trần được Reserve chủ động thực thi TRƯỚC khi cho phép bất kỳ đợt phát hành mới nào**, không phải audit sau. Đây là khác biệt giữa "khuyến nghị dùng custodial model" và "thực sự implement custodial model" — thiếu bước enforce chủ động này thì phương án A ở mục 5.2.1 chỉ là khẩu hiệu, không có tác dụng chặn tấn công thật.

### 2.4 Xử lý thất bại, timeout và hoàn tiền (bổ sung v2 — v1 còn thiếu)

Bản v1 chỉ thiết kế lại đường "thành công" (verify → mint/gọi contract). Thiết kế cũ có đường hoàn tiền riêng (`executeConfirmation`, mint lại cho sender khi đích revert) — cần thiết kế lại đường này với cùng nguyên tắc "quorum cert thay embassy", không được bỏ sót:

1. Nếu bước thực thi ở B (`claimMessage()` cho khối lượng lớn, hoặc `verifyAndExecute()` cho đường đơn giản — mục 11.3/13.3) thất bại về mặt **logic nghiệp vụ** (VD: contract đích revert) nhưng bằng chứng (quorum cert + Merkle proof) vẫn hợp lệ → B **vẫn phải finalize** trạng thái "FAILED" cho message đó (không rollback im lặng), và message FAILED này cũng nằm trong 1 commit được B tự certify.
2. Muốn hoàn tiền ở A, cần **đối xứng**: relayer lấy quorum cert của B xác nhận "message X FAILED", mang về A, A verify quorum cert của B (giống hệt cơ chế 2.2 nhưng chiều ngược lại) rồi mới mint lại cho sender ở A.
3. **Điều kiện bắt buộc để tránh double-mint**: mỗi message (định danh bằng `messageId` với channel unordered — mặc định, hoặc `(source_chain, dest_chain, sequence)` nếu ordered, mục 13.3.2) chỉ được "giải quyết" (resolve) đúng **một lần duy nhất** — hoặc là "đã mint thành công ở B", hoặc là "đã hoàn tiền ở A", không bao giờ cả hai. Cách đảm bảo: A giữ 1 trạng thái `statusByMessageId[messageId] ∈ {PENDING, SUCCESS, FAILED, REFUNDED}` (struct `Channel`, mục 11.6) — refund chỉ được thực thi nếu trạng thái đang là `PENDING`, và set về `REFUNDED` atomically trong cùng giao dịch. Nếu thiếu bước này, 1 relayer ác ý có thể cố tình gửi CẢ "message gốc" lẫn "FAILED giả" (nếu chiếm được key sai) để kích hoạt mint 2 lần — do đó **tính atomic + idempotent của trạng thái resolve là yêu cầu bảo mật, không phải chi tiết cài đặt**.
4. Timeout (đích không phản hồi sau N epoch): **không tự động hoàn tiền theo đồng hồ** (vi phạm Zero-Fork — dùng timeout để quyết định state là nguồn gốc fork). Thay vào đó, giữ `PENDING` vô thời hạn cho tới khi có **1 trong 2 bằng chứng thật**: quorum cert "SUCCESS" hoặc quorum cert "FAILED" từ B. Đây là lựa chọn có đánh đổi UX (tiền có thể kẹt lâu nếu B thực sự chết hẳn) nhưng đúng nguyên tắc "thà pending chứ không fork" đã có trong `AGENTS.md` — nếu cần UX tốt hơn, phải là quyết định quản trị rõ ràng (VD: bỏ phiếu on-chain để tuyên bố B đã chết), không phải logic timeout ngầm.

### 2.5 Tổng quát hoá tài nguyên (G2, ngoài native coin)

`GatewayPrecompile.outbound()` nhận thêm trường `asset_id`:
- `asset_id = 0` → native coin (mint/burn qua MVM có sẵn).
- `asset_id != 0` → tra `AssetRegistry` (bảng trên Root Anchor, cùng mẫu với `ChainRegistry`): `asset_id → (home_chain, canonical_contract, wrapped_contract[chain_id])`. Token gốc bị khoá (lock) ở chain nhà, mint bản wrapped ở chain đích — dùng lại **y hệt** cơ chế verify quorum cert ở mục 2.2, không cần logic riêng.

### 2.6 Ngữ nghĩa khi 2 private chain gọi lẫn nhau (bổ sung theo yêu cầu rà soát G1)

G1 ("Chain A và B gọi được contract của nhau") tưởng đơn giản nhưng có nhiều bẫy nếu coi nó như 1 lời gọi hàm đồng bộ bình thường. Dưới đây là các trường hợp **bắt buộc phải xử lý tường minh trước khi code**, không phải chi tiết cài đặt có thể "sửa sau":

#### 2.6.1 Không có tính nguyên tử liên chuỗi (no cross-chain atomicity)

A gọi B **không phải** 1 giao dịch — nó là 2 giao dịch độc lập trên 2 máy trạng thái khác nhau, nối bằng message bất đồng bộ (độ trễ thực tế ~3-7s, mục 4). Hệ quả bắt buộc phải chấp nhận và thiết kế theo:
- Bước "outbound" ở A (burn/khoá tài nguyên nếu có `value`) **luôn thành công trước**, tách biệt hoàn toàn khỏi việc B có thực thi đúng ý muốn hay không.
- Không có khái niệm "A chờ B xong rồi mới quyết định A có nên revert hay không" như gọi hàm nội bộ EVM — nếu contract ở A được viết theo tư duy đó (giả định `outbound()` trả về kết quả thực thi ở B ngay lập tức), đó là lỗi thiết kế contract nghiêm trọng cần chặn ở tài liệu hướng dẫn dev, không phải lỗi hạ tầng.
- Kết quả thật (SUCCESS/FAILED) chỉ đến qua message xác nhận riêng (mục 2.4) — contract ở A muốn phản ứng theo kết quả phải implement theo mô hình **callback bất đồng bộ** (nhận 1 lệnh gọi riêng từ Gateway khi confirmation về), tương tự pattern request/response 2 chiều của IBC, không phải request/reply đồng bộ.

#### 2.6.2 Vòng lặp gọi chéo (A→B→A→…) — cần giới hạn hop cứng

Vì B nhận message có thể tự phát sinh 1 outbound message mới quay lại A (hoặc sang C), hệ thống cần chặn vòng lặp vô hạn/khuếch đại tải:
- Envelope message phải mang **`hop_count`** (hoặc `ttl`), tăng dần mỗi lần đi qua 1 Gateway, và **bị từ chối cứng** khi vượt ngưỡng (đề xuất mặc định: 4-8 hop, đủ cho các pattern request→response→ack hợp lệ, chặn được vòng lặp vô hạn do bug contract hoặc tấn công khuếch đại chi phí relayer).
- Đây khác về bản chất với reentrancy nội bộ EVM (không có call stack chung để phát hiện) — phải chặn bằng dữ liệu tường minh trong message, không thể dựa vào cơ chế gas-based reentrancy guard sẵn có của EVM.

#### 2.6.3 Thứ tự xử lý message: Ordered vs Unordered channel

Cần chọn tường minh 1 trong 2 mô hình (giống IBC phải chọn ordered/unordered channel), **không nên mặc định "cứ theo thứ tự" mà không cân nhắc**:
- **Unordered (khuyến nghị mặc định)**: mỗi message độc lập, `sequence` chỉ dùng để chống replay (không dùng để ép thứ tự xử lý) — B xử lý message nào tới trước xử lý trước, không chờ message có sequence nhỏ hơn. Ưu điểm: 1 message bị kẹt (VD: relayer quên, hoặc cần retry) không chặn các message sau nó — tránh **head-of-line blocking**.
- **Ordered (chỉ dùng khi ứng dụng thực sự cần)**: B bắt buộc xử lý đúng thứ tự `sequence` liên tiếp, message đến sau phải chờ message trước nó được xử lý xong. Rủi ro: 1 message hỏng/kẹt (VD: chứa lỗi khiến relayer không tạo được proof) sẽ **chặn đứng toàn bộ channel** A→B cho tới khi được xử lý — đây là rủi ro liveness cần đánh đổi rõ ràng với bên yêu cầu dùng ordered channel.
- Khuyến nghị: cho phép ứng dụng chọn per-channel (không phải toàn hệ thống 1 kiểu), nhưng **mặc định là unordered** trừ khi contract khai báo rõ cần ordered.

#### 2.6.4 Xác thực người gọi gốc (origin sender) — chống giả mạo `msg.sender`

Đây là rủi ro bảo mật cụ thể nhất trong mục 2.6: khi B thực thi lệnh gọi tới contract đích thay mặt A, `msg.sender` thực tế trong EVM ở bước thực thi **không thể** là địa chỉ ví gốc trên A (vì đó là 1 địa chỉ ở chain khác, không tồn tại on-chain ở B theo nghĩa ký được). Thiết kế cũ (`cross_chain_inbound.go`) đã có đúng hướng cần giữ lại: dùng `tx.FromAddress()` (địa chỉ hệ thống/relayer) làm `msg.sender` kỹ thuật, và truyền `(pkt.Sender, pkt.SourceNationId)` qua 1 **context riêng** (`SetCrossChainContext`) để contract đích đọc qua precompile (`getOriginalSender()`/`getSourceChainId()`) thay vì tin `msg.sender`.

**Điều bắt buộc khi thiết kế lại (không được lặp lại thiếu sót nếu có):**
1. Contract đích nhận cross-chain call **phải chủ động gọi precompile lấy origin sender**, không được tin `msg.sender` là người gửi gốc — đây là trách nhiệm của người viết contract, cần đưa vào tài liệu hướng dẫn dev bắt buộc đọc, kèm ví dụ contract sai để cảnh báo.
2. Ngược lại, **contract đích cũng phải verify `msg.sender == GATEWAY_ADDRESS` (địa chỉ hệ thống chính thức)** trước khi tin bất kỳ giá trị nào đọc từ context precompile — nếu thiếu bước này, 1 kẻ tấn công có thể gọi trực tiếp hàm nội bộ (không qua Gateway thật) và tự set context giả nếu API precompile bị lộ ra ngoài phạm vi hệ thống. Đây là kiểm tra 2 chiều bắt buộc (Gateway xác thực nguồn, contract xác thực Gateway), thiếu 1 trong 2 là có lỗ hổng giả mạo danh tính liên chuỗi.

#### 2.6.5 Định mức gas/tài nguyên cho lệnh gọi inbound

Payload `CONTRACT_CALL` có thể chứa lệnh gọi tuỳ ý, chi phí thực thi không cố định (khác hẳn nhánh mint native coin đơn giản). Nếu áp dụng "free gas" như thiết kế cũ (`MAX_GASS_FEE` với cost = 0) cho toàn bộ nhánh này, đây là **vector DoS rõ ràng**: 1 payload cố tình tốn nhiều gas (loop nặng, gọi lồng nhiều contract) được verify hợp lệ (quorum cert đúng) nhưng thực thi cực tốn CPU ở B, lặp lại nhiều lần vẫn miễn phí.

**Khuyến nghị:** người gửi ở A phải khoá (lock) kèm 1 khoản "gas liên chuỗi" tính bằng native coin cùng lúc với message gốc; B thực thi `CONTRACT_CALL` với **gas cap = khoản đã khoá quy đổi**, phần dư trả lại (qua message hoàn tiền, dùng chung cơ chế mục 2.4), phần đã dùng bị đốt thật (không "free"). Không áp dụng "free gas" cho nhánh `CONTRACT_CALL` như đã làm với nhánh mint đơn giản.

---

## 3. Đánh giá tương thích với Metanode hiện tại

| Thành phần Metanode hiện có | Dùng lại được không? | Ghi chú |
|---|---|---|
| Mysticeti DAG-BFT (`meta-consensus/core`) | ✅ Toàn bộ, không sửa | Quorum cert (`StakeAggregator<QuorumThreshold>`, `CertifiedCommit`) chính là nền tảng của bridge mới |
| BLS aggregate signature (`pkg/bls`, `execution/pkg/mvm` BLST binding) | ✅ Tái dùng | Đã có `VerifyAggregateSign` (`extension.go:302`) — dùng đúng hàm này để verify quorum cert ở chain đích |
| `ProcessNativeMintBurn` / `tz_hardware_engine.go` | ✅ Tái dùng nguyên vẹn | Đây là phần cơ chế mint/burn **duy nhất** cần giữ từ thiết kế cũ — nó độc lập với lỗi Embassy |
| Epoch transition (`epoch_transition.rs`, `epoch_checkpoint.rs`) | ✅ Mở rộng nhẹ | Thêm 1 bước gửi `CommitteeUpdate` lên Root Anchor sau mỗi epoch — không đổi luồng epoch nội bộ |
| FFI/UDS boundary Go↔Rust | ⚠️ Thêm tải nhẹ | Cần 1 kênh RPC mới: Go gọi Root Anchor (RPC ra ngoài mạng, không phải FFI nội bộ) — khác bản chất so với FFI hiện có, phải có circuit breaker riêng (tương tự `rpc_circuit_breaker.rs`) |
| Zero-Fork invariant ("thà pending, không fork") | ✅ Áp dụng nguyên trạng | Mọi thao tác mint ở đích đều chờ quorum cert xác nhận được — không dùng timeout để quyết định, khớp `AGENTS.md` Part 2.5 |
| `execution/contracts/cross_chain/*.sol` (Embassy) | ❌ Không dùng | Giữ tham chiếu lịch sử, không kế thừa logic (lý do: mục 5.1) |
| Ansible/systemd deploy | ✅ Tái dùng | Root Anchor deploy như 1 network Metanode bình thường thêm |
| `deploy/ansible/monitors` (health/fork monitor) | ✅ Mở rộng | Thêm dashboard theo dõi độ trễ relay + drift `ChainRegistry` |

**Kết luận tương thích:** Thiết kế mới **không đụng vào lõi consensus/execution hiện có**, chỉ thêm (a) 1 mạng Root Anchor mới triển khai bằng công cụ sẵn có, (b) 1 precompile Gateway mới thay thế contract Embassy cũ, (c) 1 bước nhỏ trong epoch transition. Rủi ro tương thích thấp vì không có "phát minh mới" ở tầng consensus.

---

## 4. Phân tích hiệu năng / tốc độ

Căn cứ số liệu đo thật đã có trong `note/report/tps_e2e_analysis_2026-07-14.md` và `HOW_TO_TUNE_BLOCK_SIZE.md`:

| Yếu tố | Số liệu nền hiện tại | Tác động của thiết kế mới | Đánh giá |
|---|---|---|---|
| Round time Mysticeti (LAN) | ~150-200ms/round | Không đổi (Root Anchor là mạng riêng, không chia sẻ round với private chain) | 🟢 Không ảnh hưởng |
| Finality của 1 commit (không phải epoch) | Vài trăm ms - vài giây tuỳ tải | Message cross-chain chỉ cần chờ **commit finality**, KHÔNG cần chờ epoch checkpoint (300-900s) — vì quorum cert được tạo mỗi commit | 🟢 Thiết kế mới nhanh hơn nhiều so với phương án "chờ epoch" mà bản nháp đầu tiên (hội thoại trước) đề xuất |
| Tần suất cập nhật `ChainRegistry` | N/A | 1 giao dịch / epoch / chain = với 100 chain, epoch 300s → ~0.33 tx/s lên Root Anchor | 🟢 Không phải nút thắt dù N lớn |
| Mega-block pathology (đã ghi nhận thật: 50k TX/block → E2E tụt còn ~5.000 tx/s vì round bị "đứng hình" 3s) | Đã xảy ra thật trên cluster | **Batch relay phải áp dụng đúng bài học này**: KHÔNG dồn hàng chục nghìn `claimMessage()` vào cùng 1 block không kiểm soát — giới hạn batch relay ở mức tương đương khuyến nghị hiện tại (4.000-8.000 tx/block LAN, 10.000-20.000 WAN; chi tiết riêng cho cross-chain ở mục 13.5) | 🟡 Rủi ro thật nếu không giới hạn — phải cấu hình cứng ngay từ đầu, không để lặp lại lỗi đã tìm thấy |
| Chi phí verify BLS aggregate | N/A (cũ: tích luỹ nhiều TX từ nhiều Embassy / message → tốn N_embassy giao dịch riêng) | Với mô hình "Attest-then-Claim" (mục 13.2-13.3, đường mặc định cho khối lượng lớn): **1 phép verify pairing / commit** (không phải / message), phần thực thi còn lại (`claimMessage()`) rẻ và song song hoá được — xem phân tích thông lượng đầy đủ ở mục 13 | 🟢 Rẻ hơn nhiều so với cả cơ chế Embassy cũ lẫn phương án "1 verify/message" naive |
| Độ trễ end-to-end — message thuần (a), value=0, đi thẳng A↔B | commit finality A (~1-2s) + relayer poll & submit (~1-3s) + block inclusion ở B (~1-2s) | ~3-7s tổng | 🟢 Chấp nhận được cho use-case doanh nghiệp |
| Độ trễ end-to-end — chuyển giá trị (b), value>0, phải qua Reserve (mục 2.3, sau khi chốt v6) | Thêm 1 hop trọn vẹn: A→Reserve (~3-7s như trên) rồi Reserve→B (thêm ~3-7s nữa, có thể chồng lấp 1 phần nếu relayer xử lý song song) | ~6-12s tổng, **không phụ thuộc epoch** | 🟡 Chậm hơn message thuần khoảng gấp đôi — đánh đổi bắt buộc để có bảo vệ trần cấp phép (mục 2.3); vẫn chấp nhận được cho use-case doanh nghiệp, cần nêu rõ với người dùng đây không phải chuyển khoản tức thời |
| Tải RPC mới Go↔Root Anchor | Chưa có (mới) | Cần connection pool + circuit breaker, tránh Root Anchor down làm nghẽn cross_chain_handler tại private chain | 🟡 Cần thiết kế timeout/circuit-breaker cẩn thận (xem mục 6) |

**Khuyến nghị cấu hình ngay từ thiết kế:** giới hạn cứng `max_messages_per_relay_batch` (đề xuất khởi điểm: 2.000-4.000, thấp hơn cả khuyến nghị TX thường vì payload proof/Merkle nặng hơn TX transfer đơn thuần) để tránh lặp lại đúng lỗi mega-block đã ghi nhận.

---

## 5. Phân tích rủi ro bảo mật

### 5.1 Vì sao thiết kế Embassy cũ bị loại bỏ (bằng chứng cụ thể từ code)

Đây là lý do kỹ thuật cụ thể khiến tài liệu này khuyến nghị **không** dùng lại `execution/contracts/cross_chain/*` và `cross_chain_handler` hiện tại làm nền tảng:

- `cross_chain_batch_submit.go:246` định nghĩa hàm `quorum(total int) int` (ngưỡng >50%) nhưng **hàm này không được gọi ở bất kỳ đâu** trong `handleBatchSubmit` — dead code.
- `handleBatchSubmit` (`cross_chain_batch_submit.go:258-343`) thực thi **trực tiếp từng event** trong batch, chỉ dựa vào comment "quorum đã được đảm bảo ở observer sub trước khi gửi TX" — không có xác minh mật mã học (BLS aggregate) nào của nội dung packet trong hàm này.
- `VerifyBatchSubmitSender` (`cross_chain_batch_submit.go:230-242`) chỉ kiểm tra **1 điều kiện**: địa chỉ gửi TX có trong danh sách embassy active hay không — không đếm số embassy đồng thuận, không verify chữ ký tổng hợp.
- Hệ quả: **1 embassy đơn lẻ bị lộ private key (hoặc là kẻ nội gián)** có thể tự soạn 1 `batchSubmit` với `INBOUND packet` giả (giá trị `Value` tuỳ ý, `Target = address(0)` để trigger nhánh mint) và hệ thống sẽ mint native coin ngay lập tức — **đúng kiểu tấn công đã xảy ra thật với các cầu nối multisig/PoA khác** (Ronin, Harmony...) mà chính `note/private_chain_solutions_evaluation.md` mục 5 đã cảnh báo trước.

→ Đây là lý do cốt lõi thiết kế mới **không tái sử dụng PKI Embassy độc lập**, mà bám vào quorum cert BFT đã được chứng minh an toàn của chính consensus engine.

### 5.2 Rủi ro cấp mô hình (không phải lỗi code): an toàn bị giới hạn bởi chain YẾU NHẤT — "Weakest-Link Problem"

**Đây là phát hiện quan trọng nhất trong toàn bộ tài liệu, cần được xác nhận/quyết định trước khi code bất kỳ dòng nào.**

Mô hình mục 2.2/2.3 cho phép **bất kỳ chain nào trong registry tự mint/burn native coin dựa trên quorum cert của chính uỷ ban validator chain đó**. Điều này có nghĩa: an toàn của "1 đồng coin thống nhất" (G3) **không được quyết định bởi chain mạnh nhất hay Root Anchor, mà bởi chain YẾU NHẤT đang được đăng ký**.

Kịch bản tấn công cụ thể:
1. Giả sử chain C là 1 private chain nhỏ (VD: 4 validator, tổng stake thấp) được thêm vào `ChainRegistry` để phục vụ 1 đối tác nhỏ.
2. Kẻ tấn công chỉ cần chiếm **2/4 validator của riêng chain C** (ngưỡng BFT 2f+1 của 1 uỷ ban nhỏ rẻ hơn rất nhiều so với chiếm uỷ ban của 1 chain lớn).
3. Với quyền kiểm soát BFT của C, kẻ tấn công cho C tự "chốt" (commit) một giao dịch **giả** kiểu "user X đã nạp/mint 1 triệu native coin trên C" (không có burn thật ở đâu tương ứng) — vì C tự kiểm soát tính hợp lệ nội bộ của chính nó, `ProcessNativeMintBurn` trên C sẽ chạy "hợp lệ" theo góc nhìn của C.
4. Sau đó bridge số coin "khống" này sang chain B (chain lớn, an toàn) qua đúng cơ chế 2.2 — B verify quorum cert của C, thấy hợp lệ (vì đúng là 2f+1 stake của C đã ký, dù C đã bị chiếm), B mint thật cho kẻ tấn công.
5. Kết quả: **giá trị bị rút ra khỏi hệ thống thống nhất mà không có burn thật tương ứng** — vi phạm trực tiếp bất biến tổng cung (G3), dù `GlobalSupplyLedger` ở Root Anchor cộng dồn đúng số liệu C tự báo cáo (vì chính C là nguồn báo cáo, cũng đã bị chiếm).

Đây **không phải lỗi lập trình** — nó là hệ quả tất yếu của mô hình "mỗi chain tự quyết an toàn coin của chính mình rồi lan ra toàn hệ thống", giống hệt giới hạn bảo mật đã biết của IBC (Cosmos): tài sản IBC an toàn tối đa bằng zone **yếu nhất** đã từng chuyển tài sản đó qua, không phải hub.

**Cập nhật sau khi chốt kiến trúc (mục 2.3 đã viết lại):** kịch bản tấn công ở bước 3-4 phía trên **không còn thực hiện được nữa** với cơ chế `per_chain_allocation` (trần thực thi chủ động, không phải audit thụ động) — kẻ tấn công chiếm C tối đa chỉ rút được đúng bằng `per_chain_allocation[C]` hiện có tại Reserve, không thể tạo giá trị mới từ hư không vì Reserve từ chối phát hành tiếp khi trần không đủ. Rủi ro không biến mất hoàn toàn (C bị chiếm vẫn có thể **rút cạn đúng phần đã được cấp phép cho C**, gây thiệt hại cục bộ cho người dùng của C), nhưng **không còn lan ra ngoài hệ thống thống nhất** — đây là khác biệt giữa "giảm thiểu" và "chặn đứng" đường tấn công nguy hiểm nhất.

#### 5.2.1 Hai phương án — ĐÃ CHỐT: Phương án A

| Phương án | Mô tả | Ưu điểm | Nhược điểm |
|---|---|---|---|
| **A — Custodial/Reserve model (khuyến nghị)** | Native coin **chỉ được mint/burn thật ở đúng 1 nơi** (Root Anchor, hoặc 1 "Reserve Chain" chỉ định, được bảo vệ bằng uỷ ban stake cao nhất hệ thống). Mọi private chain khác **không tự mint** — chỉ giữ "phiếu ghi nợ" (IOU) đại diện cho số coin đang custody ở Reserve. Chuyển A→B thực chất là: A giảm IOU, gửi message cho Reserve xác nhận, B tăng IOU — không có chain trung gian nào tự tạo ra giá trị mới | An toàn toàn hệ thống = an toàn của **1 chain mạnh nhất** (Reserve), không phụ thuộc chain yếu | Thêm 1 hop qua Reserve cho mọi giao dịch (thêm ~1-2s độ trễ); Reserve trở thành điểm phải luôn sẵn sàng (nhưng đã có mục 6 xử lý bằng cache + circuit breaker) |
| **B — Phân hạng quyền mint (tiering)** | Chỉ chain đạt ngưỡng tối thiểu (số validator, tổng stake, thời gian hoạt động ổn định) mới được cấp "quyền mint/burn native coin thật" (`can_mint_native = true` trong `ChainRegistry`). Chain dưới ngưỡng chỉ được bridge **synthetic/wrapped token có thế chấp** (escrow ở 1 chain đủ mạnh), không đụng vào native coin thật | Không cần thêm hop qua Reserve cho các chain đã đủ mạnh | Cần cơ chế quản trị định kỳ đánh giá lại "đủ mạnh" — bản thân tiêu chí này cũng có thể bị chơi (1 chain gian dối tăng validator ảo để đạt ngưỡng) |

**Khuyến nghị của tài liệu này: Phương án A.** Đơn giản hơn để suy luận về an toàn (chỉ cần bảo vệ 1 nơi cực kỳ tốt, thay vì audit định kỳ N chain), và khớp tự nhiên với vai trò Root Anchor đã thiết kế — Root Anchor không chỉ là registry mà còn là **custodian thật của native coin**. Mục 2.2-2.3 ở trên cần cập nhật theo hướng này trước khi implement (xem lộ trình mục 8, đã thêm quyết định P0).

#### 5.2.2 Câu hỏi cốt lõi: nếu 1 private chain "chết", người dùng có rút được tiền không?

**Trả lời ngắn gọn: TUỲ tình huống "chết" là gì, và tuỳ có anchor đủ dữ liệu từ trước hay không — không phải lúc nào cũng rút được, và đây là giới hạn vật lý (data availability), không phải điều thiết kế nào "sửa" được sau khi dữ liệu đã mất.**

Cần phân biệt rạch ròi 3 mức độ "chết", vì cách xử lý và khả năng rút tiền khác nhau hoàn toàn:

| Mức độ | Định nghĩa | Rút tiền được không? | Cơ chế |
|---|---|---|---|
| **(1) Tạm ngừng (degraded)** | Validator offline tạm thời, dữ liệu (state/DB) vẫn còn nguyên trên đĩa, sẽ khởi động lại được | ✅ **Được, đầy đủ** — không mất gì | Không cần cơ chế đặc biệt: giao dịch cross-chain liên quan chain này chỉ ở trạng thái `PENDING` (đúng Zero-Fork "thà pending"), tự động resolve khi chain khởi động lại. Không được coi đây là "chết" và kích hoạt bất kỳ quy trình khẩn cấp nào — làm vậy sớm sẽ tạo nguy cơ double-spend khi chain hồi phục (mục 2.4 điểm 3) |
| **(2) Chết hẳn nhưng còn dữ liệu (permanently halted, state preserved)** | Validator ngừng vĩnh viễn (VD: công ty vận hành giải thể) nhưng ai đó (VD: Root Anchor, hoặc bên thứ 3 lưu trữ) còn giữ **state root đã anchor** + dữ liệu để tạo Merkle proof | ⚠️ **Được, nhưng CHỈ với điều kiện đã chuẩn bị trước** (xem yêu cầu bên dưới) — và chỉ khôi phục đúng số dư tại thời điểm checkpoint cuối cùng, không phải số dư thực tế ngay trước khi chết (có thể lệch nếu chết giữa 2 lần checkpoint) | Quy trình "Chain-Death Recovery" — xem chi tiết bên dưới |
| **(3) Chết hoàn toàn, mất dữ liệu (total data loss)** | Không ai còn giữ state/dữ liệu nào của chain đó (server bị xoá, không backup) | ❌ **KHÔNG THỂ rút được, về mặt nguyên lý** | Không có thiết kế nào khắc phục được — không thể chứng minh 1 số dư mà không ai còn giữ bằng chứng. Đây là giới hạn của mọi hệ thống blockchain (kể cả TON/Cosmos), không riêng Metanode |

**Điều kiện bắt buộc để trường hợp (2) khả thi — đây là yêu cầu thiết kế MỚI cần thêm vào mục 2.1, chưa có ở v2:**

Thiết kế v2 (mục 2.1) hiện chỉ anchor **`local_supply` dạng số tổng** (aggregate) lên Root Anchor mỗi epoch — con số này **không đủ** để biết "user X có bao nhiêu" khi chain chết, chỉ biết "tổng cả chain có bao nhiêu". Muốn phục hồi theo từng người dùng, **bắt buộc** phải anchor thêm:
- **State root đầy đủ của account tree** (không chỉ số tổng), mỗi epoch, lên Root Anchor.
- Dữ liệu để dựng lại Merkle proof (preimage của account tree) phải được lưu trữ ở nơi **độc lập với chính chain đó** (Root Anchor lưu hash không đủ — cần ít nhất 1 bên thứ 3/archival node ngoài validator set của chain lưu snapshot đầy đủ, nếu không khi chain chết luôn dữ liệu preimage cũng chết theo dù đã có root hash).

→ Đây là 1 **quyết định kiến trúc P0 mới cần bổ sung**: có bắt buộc mọi chain đăng ký (đặc biệt chain nhỏ, rủi ro cao theo mục 5.2) phải publish state-root + archival snapshot định kỳ hay không. Khuyến nghị: **bắt buộc đối với mọi chain có `can_mint_native`/tham gia custody thật (phương án A)** — coi đây là điều kiện tiên quyết để được đăng ký vào `ChainRegistry`, không phải tính năng tuỳ chọn.

**Quy trình "Chain-Death Recovery" đề xuất (chỉ áp dụng cho mức (2), sau khi đã có anchor đủ dữ liệu):**

1. **Không tự động** — đây là hành động quản trị khẩn cấp có chủ đích, khác hẳn "quyết định dispatch commit" mà Zero-Fork cấm dùng timeout. Cần: (a) bằng chứng chain không sản xuất block trong thời gian rất dài (đề xuất mốc tối thiểu: hàng tuần, không phải giờ/ngày, để không nhầm với gián đoạn tạm thời), (b) biểu quyết siêu đa số (VD: >2/3 số chain còn lại đang hoạt động trong registry) xác nhận "declare chain X dead", ghi nhận **vĩnh viễn, không thể đảo ngược** trên Root Anchor.
2. Sau khi được "declare dead": Root Anchor **vĩnh viễn từ chối** mọi quorum cert tự xưng đến từ chain X trong tương lai (kể cả nếu validator cũ của X sống lại và cố gắng hoạt động tiếp) — đóng cửa vòng lặp "double-claim nếu chain hồi sinh sau khi đã cho rút bù".
3. Người dùng nộp **Merkle proof số dư của mình tại state root đã anchor cuối cùng** lên Reserve chain (phương án A) → Reserve mint/giải phóng đúng số đó cho họ, đánh dấu claim đã dùng (chống double-claim) trong 1 bảng bất biến trên Root Anchor.
4. Giới hạn phải nêu rõ với người dùng: chỉ khôi phục đúng số dư **tại thời điểm checkpoint cuối**, không phải số dư ngay trước khi chết — nếu chain chết giữa 2 kỳ checkpoint (khoảng 300-900s theo epoch hiện tại, có thể cấu hình ngắn hơn cho chain rủi ro cao), phần chênh lệch giữa checkpoint cuối và thời điểm chết **bị mất**, không có cách khôi phục (đúng bản chất data-availability, không phải lỗi thiết kế).

**Kết luận cần truyền đạt rõ cho người dùng/đối tác (không nên hứa quá mức):** hệ thống có thể bảo vệ tốt trước "chết tạm thời" (không mất gì) và "chết hẳn nhưng có anchor" (mất tối đa 1 khoảng chênh lệch nhỏ theo chu kỳ checkpoint), nhưng **không có cách nào bảo vệ trước mất dữ liệu hoàn toàn** — đây là lý do phải bắt buộc anchor + archival cho mọi chain tham gia custody thật, thay vì coi là tính năng "nice to have".

#### 5.2.3 So sánh với mô hình Shard của TON — vì sao TON không thực sự gặp vấn đề "shard chết mất tiền"

Đối chiếu với TON để hiểu rõ giới hạn của thiết kế Metanode và tránh ngộ nhận "TON làm được thì Metanode cũng làm được y hệt":

| Đặc điểm | Shard của TON | Private chain của Metanode (thiết kế hiện tại) |
|---|---|---|
| Ai xác thực 1 shard/chain? | **Cùng 1 tập validator toàn mạng** (bầu qua Elector contract theo stake Toncoin), được luân phiên phân công (reshuffle) qua các shard theo chu kỳ — không có "validator riêng cố định của 1 shard" | **Validator riêng, cố định, độc lập** cho từng private chain (đúng nhu cầu nghiệp vụ "private chain riêng cho từng đối tác") |
| Mức an ninh có đồng đều giữa các shard/chain không? | **Có** — mọi shard cùng mức an ninh (cùng pool stake) → không có "shard yếu" | **Không** — 1 private chain nhỏ (ít validator) có ngưỡng bị chiếm rẻ hơn hẳn 1 chain lớn → đây chính là nguồn gốc risk #1 (mục 5.2) |
| Anchor state root từng account lên chain gốc? | **Có, mặc định từ thiết kế gốc** — Masterchain include Merkle root thật của mọi shardchain mỗi block (~vài giây) | **Chưa có ở v2** — mới anchor tổng hợp (`local_supply`), phải bổ sung thành yêu cầu bắt buộc (đã đưa vào P0 ở mục 5.2.2) |
| Dữ liệu shard có bị cô lập ở 1 nhóm nhỏ không? | **Không** — do validator bị xáo trộn qua nhiều shard, dữ liệu lan rộng khắp mạng, không phụ thuộc 1 nhóm cố định | **Có nguy cơ** — mặc định chỉ chính validator của private chain đó giữ dữ liệu đầy đủ, trừ khi bắt buộc archival bên thứ 3 (đã nêu ở mục 5.2.2) |

**Kết luận quan trọng: TON không "giải" được bài toán weakest-link — TON tránh việc bài toán đó xuất hiện, bằng cách không cho phép shard có chủ quyền/an ninh độc lập.** Đây là đánh đổi gốc rễ mà Metanode cần đối mặt: yêu cầu nghiệp vụ "private chain A, B là 2 thực thể riêng, tự vận hành validator riêng" (khác bản chất với "shard" của TON, gần với mô hình liên minh chain có chủ quyền kiểu Cosmos zones hơn) **tự nó tái tạo ra rủi ro weakest-link mà TON không phải xử lý**. Không có thiết kế nào cho phép vừa (a) mỗi private chain có validator hoàn toàn độc lập, tự chủ, **vừa** (b) an ninh đồng đều tuyệt đối như TON — hai điều này loại trừ lẫn nhau.

**Hệ quả cho quyết định P0:** phương án A (custodial/Reserve, mục 5.2.1) là cách tiệm cận gần nhất với nguyên tắc của TON *trong giới hạn vẫn giữ được chủ quyền private chain* — bằng cách dồn phần "phải an toàn tuyệt đối" (giữ giá trị thật) vào 1 nơi duy nhất được bảo vệ ở mức cao nhất (giống vai trò Masterchain), còn phần "linh hoạt/chủ quyền riêng" (private chain tự vận hành) chỉ xử lý IOU/đại diện, không tự tạo ra giá trị mới. Đồng thời bắt buộc thêm (đã có ở mục 5.2.2): mọi private chain đủ điều kiện custody phải anchor **state root từng account** (không chỉ tổng số) lên Root Anchor — đúng nguyên tắc Masterchain của TON — để giới hạn thiệt hại khi 1 chain chết chỉ còn là "mất phần chênh lệch giữa 2 lần checkpoint", không phải "mất toàn bộ".

#### 5.2.4 Khuyến nghị cuối: KHÔNG rập khuôn "no weak shard" của TON cho toàn hệ thống

Câu hỏi trực tiếp cần trả lời dứt khoát: **có nên bỏ chủ quyền private chain, chuyển sang dùng chung 1 pool validator như TON để loại hẳn weakest-link không?**

**Khuyến nghị: KHÔNG.** Lý do:

1. **Mâu thuẫn với chính giá trị cốt lõi của "private chain"** — theo tài liệu sản phẩm đã có sẵn trong repo (`note/private_chain_guide.md`, công cụ triển khai `gen_private_chain.py`), private chain của Metanode vốn được thiết kế để 1 đối tác/khách hàng **tự vận hành hạ tầng, validator riêng, tách biệt dữ liệu** — đây là lý do khách hàng chọn "private chain" thay vì dùng chung 1 mạng. Chuyển sang mô hình chia sẻ 1 pool validator chung (kiểu TON) sẽ xoá bỏ đúng giá trị đó — về bản chất không còn là "private chain" nữa mà là 1 shard của 1 chain chung.
2. **Chi phí kỹ thuật lớn hơn hẳn** — thiết kế Root Anchor (mục 2) tái dùng gần như nguyên vẹn consensus hiện có (mỗi private chain vẫn là 1 mạng Mysticeti độc lập, không đổi lõi). Một hệ thống "shared validator pool luân phiên qua nhiều chain có luật khác nhau" (khác cả về throughput lẫn config, chứ không đồng nhất như shard TON) là 1 tầng điều phối validator hoàn toàn mới, chưa có tiền lệ trong Metanode — rủi ro triển khai và thời gian cao hơn nhiều bậc so với lộ trình P0-P8 hiện tại.
3. **Không cần thiết để đạt mức an toàn "đủ tốt"** — mục 5.2.1 (phương án A) + 5.2.2 (anchor state-root account-level) đã bọc được đúng phần nguy hiểm nhất (rút giá trị khống ra khỏi hệ thống thống nhất) mà không cần từ bỏ chủ quyền. Phần còn lại (1 chain nhỏ bị chiếm có thể tự phá hỏng chính nó) là rủi ro cục bộ, chấp nhận được và tương tự rủi ro mà bất kỳ chain độc lập nào (kể cả không có bridge) đều có sẵn.

**Nếu vẫn muốn có lựa chọn an toàn kiểu TON cho khách hàng không cần chủ quyền tuyệt đối:** có thể bổ sung như 1 **tuỳ chọn sản phẩm** (không bắt buộc toàn hệ thống) — 1 "Managed/Shared Validator Tier": khách hàng nhỏ không muốn tự vận hành validator có thể chọn để Root Anchor/1 nhóm validator uy tín cùng vận hành chain hộ (đổi lấy an ninh cao hơn, tương tự Cosmos Interchain Security hoặc parachain của Polkadot thuê an ninh từ Relay Chain) — trong khi khách hàng cần chủ quyền tuyệt đối vẫn chọn tier tự vận hành như hiện tại, chấp nhận giới hạn ở mục 5.2.1/5.2.2. Đây là quyết định sản phẩm (2 tier song song), không phải thay thế toàn bộ kiến trúc — nên chỉ cân nhắc sau khi P0-P8 đã ổn định, không phải điều kiện tiên quyết.

### 5.3 Rủi ro bảo mật còn lại của thiết kế mới (đánh giá trung thực)

| Rủi ro | Mức độ | Giải thích | Giảm thiểu |
|---|---|---|---|
| Root Anchor trở thành mục tiêu giá trị cao | 🔴 Cao nếu không thiết kế committee đúng | Nếu Root Anchor bị chiếm (>1/3 stake độc hại), attacker có thể đăng ký `ChainRegistry` giả cho 1 "chain ma" hoặc thay đổi committee của chain thật → mọi chain đích sẽ tin theo | Committee Root Anchor phải là liên uỷ ban stake-weighted từ TẤT CẢ private chain (không phải nhóm riêng nhỏ hơn); mọi thay đổi `ChainRegistry` nên có độ trễ (delay window) trước khi có hiệu lực để phát hiện bất thường |
| Committee rotation race | 🟡 Trung bình | Nếu B verify quorum cert bằng committee **cũ** trong khi A đã rotate, hoặc ngược lại dùng committee **chưa kịp anchor** | Quorum cert phải mang `epoch` tường minh; B bắt buộc từ chối nếu epoch đó chưa được Root Anchor xác nhận (fail-closed, đúng tinh thần Zero-Fork: thà pending) |
| Bug trong code verify Merkle proof / BLS mới viết | 🟡 Trung bình-Cao (do là code mới) | Đây là bề mặt tấn công mới hoàn toàn, khác các module đã chạy production lâu | Bắt buộc unit test với test vector cố định + code review bảo mật riêng (`/security-review`) trước khi lên mainnet, không rely vào "nhìn có vẻ đúng" |
| DoS qua giao dịch mint/burn miễn phí gas | 🟡 Trung bình | Kế thừa từ thiết kế cũ: `ProcessNativeMintBurn` gọi qua `proxy_tx` với gas = `MAX_GASS_FEE` nhưng cost 0 ("free gas") — nếu relayer path cũng miễn phí, có thể bị spam volume lớn message giả (dù không mint được vì fail verify, vẫn tốn CPU verify BLS) | Relay TX vẫn phải trả gas thật (không miễn phí như bước mint nội bộ); chỉ bước mint SAU KHI verify thành công mới nên miễn gas |
| Trust chain "TEE/TrustZone" cho phần ký cứng (nếu dùng Orange Swarm sau này) | 🟢 Thấp với thiết kế hiện tại (không bắt buộc TEE) | Thiết kế bridge mới **không phụ thuộc TEE** để an toàn (an toàn đến từ BFT quorum, không phải phần cứng) — TEE vẫn có thể dùng cho tối ưu vận hành riêng (`tee_master_architecture.md`) nhưng không phải điều kiện an toàn bridge | Giữ 2 mối quan tâm tách biệt: an toàn bridge (BFT) vs tối ưu triển khai node (TEE) |
| Relayer censorship (không phải giả mạo) | 🟢 Thấp | Relayer permissionless — nếu 1 relayer im lặng, bất kỳ ai khác cũng relay được vì proof tự-xác-minh, không cần quyền | Không cần giảm thiểu thêm, đây là thuộc tính có sẵn của thiết kế |

### 5.4 Các trường hợp biên bổ sung sau rà soát v2

| Trường hợp | Rủi ro | Đánh giá / cần làm |
|---|---|---|
| Quản trị `ChainRegistry`/`AssetRegistry` — ai được thêm/xoá 1 chain hoặc 1 asset? | Nếu chỉ là 1 khoá `onlyOwner` (như contract cũ) thì **tái tạo đúng điểm tập trung** mà toàn bộ thiết kế này đang cố loại bỏ | Bắt buộc: việc thêm/xoá chain phải là 1 giao dịch được chính BFT committee của Root Anchor finalize (biểu quyết đa số/siêu đa số qua giao dịch thường), không phải 1 EOA đơn lẻ ký. Cần đặc tả rõ ngưỡng biểu quyết ở P0 |
| Rogue public-key attack trên chữ ký BLS tổng hợp | Nếu không có proof-of-possession (PoP) khi đăng ký public key validator, 1 bên có thể chọn public key của mình phụ thuộc vào public key người khác để giả mạo chữ ký tổng hợp | Đã thấy `pkg/bls/bls.go` dùng ciphersuite DST `..._POP_` (chuẩn IETF BLS có hỗ trợ PoP) — **nhưng chưa xác nhận được có bước `PopVerify` khi đăng ký validator key hay không** trong phạm vi đã đọc. Cần xác minh cụ thể ở P0/P1 trước khi dùng cùng cơ chế cho `ChainRegistry`; nếu thiếu, phải thêm PoP-check khi 1 chain đăng ký committee lên Root Anchor |
| Data availability: pruning xoá block cũ trước khi relayer kịp tạo Merkle proof | `execution/pkg/pruning/` (`nomt_pruner.go`, `pruning_manager.go`) đã tồn tại cơ chế prune state/block — nếu cửa sổ giữ lại quá ngắn, message cross-chain "chậm" (relayer offline lâu) sẽ không thể relay được nữa vì thiếu dữ liệu tạo proof | Cần đảm bảo cửa sổ pruning giữ đủ lâu cho mọi message cross-chain CHƯA resolve (liên kết với trạng thái `PENDING` ở mục 2.4) — hoặc archive riêng dữ liệu cần cho proof, tách khỏi state pruning thông thường |
| MEV/front-run ở nhánh `CONTRACT_CALL` tại chain đích | Message cross-chain gọi contract kèm `payload` có thể bị người xem thấy trước (message đã public từ lúc certify ở nguồn) và bị front-run ở đích trước khi relayer nộp | Chấp nhận được cho hầu hết use-case doanh nghiệp (không phải trading); nếu cần chống MEV, thêm tuỳ chọn "commit-reveal" cho payload nhạy cảm — không cần giải quyết ở bản đầu, ghi nhận là rủi ro đã biết |
| Giả mạo `AssetRegistry` — 1 chain tự nhận là `home_chain` của 1 asset nó không thực sự sở hữu | Nếu đăng ký asset không qua xác thực, 1 chain ác ý có thể tự khai "tôi là home chain của token X" rồi mint wrapped token khống ở nơi khác | Đăng ký asset mới trên `AssetRegistry` phải đi qua cùng cơ chế quản trị đa số như thêm chain (dòng đầu bảng này), không phải tự-đăng-ký một chiều |
| Double-mint qua đường hoàn tiền (refund) | Đã thiết kế điều kiện atomic/idempotent ở mục 2.4 — liệt kê lại ở đây để không bị bỏ sót khi audit bảo mật độc lập (P5) | Xem chi tiết mục 2.4 điểm 3 — bắt buộc test case riêng khi security review |

---

## 6. Phân tích rủi ro vận hành

| Rủi ro | Mô tả | Giảm thiểu |
|---|---|---|
| Thêm 1 mạng phải vận hành (Root Anchor) | Thêm 1 bộ validator, 1 bộ giám sát, 1 quy trình nâng cấp | Tái dùng 100% `deploy/ansible`, `deploy/systemd`, `deploy/ansible/monitors` đã có — không phải xây quy trình vận hành mới, chỉ nhân bản |
| RPC Go↔Root Anchor là kết nối mạng thật (khác FFI nội bộ) | Nếu Root Anchor sập/mạng lag, `cross_chain_handler` ở private chain có thể bị treo chờ nếu không cache tốt | Bắt buộc: cache `ChainRegistry` cục bộ (refresh định kỳ, không phải mỗi giao dịch), có circuit breaker (mẫu `rpc_circuit_breaker.rs` đã có sẵn trong repo) — private chain vẫn hoạt động bình thường (tự vận hành nội bộ) khi Root Anchor tạm mất kết nối, chỉ riêng luồng cross-chain bị tạm hoãn (pending, đúng Zero-Fork) |
| Giám sát lệch `GlobalSupplyLedger` | Ai phát hiện khi tổng cung lệch? | Bổ sung dashboard riêng trong `deploy/ansible/monitors` cảnh báo Telegram giống Health Monitor/Block Hash Checker hiện có, chạy đối chiếu mỗi epoch |
| Khôi phục sau crash | Thiết kế cũ cần rebuild `sigAccum` phức tạp từ block log (`RecoverSigAccumulator`) | Thiết kế mới **đơn giản hơn nhiều**: verifier không giữ state tích luỹ giữa các lần gọi (stateless per-message, chỉ cần `Channel.statusByMessageId`/`AttestedCommit` on-chain, mục 11.6) — không cần logic phục hồi phức tạp khi restart |
| Watchdog phần cứng (nếu dùng TrustZone cho phần mint/burn nội bộ) | Đã có tiền lệ xử lý (`commit fa4edfa5`: auto-reboot watchdog cho `mvm_ta` hang, timeout `tzHardwareRoundTrip`) | Áp dụng đúng pattern đã có, không cần thiết kế mới cho phần này |
| Nâng cấp/rotate committee Root Anchor | Quy trình thêm/bớt chain khỏi hệ thống | Cần quy trình vận hành rõ ràng (runbook) tương tự thêm validator hiện tại, kèm delay window (mục 5.2) trước khi hiệu lực |
| Quan sát độ trễ relay thực tế | Không có số liệu thật cho tới khi triển khai | Thêm metric `cross_chain_relay_latency_seconds` ngay từ bản đầu (bài học từ `tps_e2e_analysis`: thiếu metric làm chẩn đoán sai bottleneck rất tốn thời gian) |

---

## 7. Ma trận rủi ro tổng hợp (ưu tiên xử lý, đã cập nhật v10)

| # | Rủi ro | Loại | Mức độ | Ưu tiên xử lý |
|---|---|---|---|---|
| 1 | **Weakest-link: 1 chain yếu trong registry rút giá trị khống ra toàn hệ thống** (mục 5.2) | Bảo mật — cấp mô hình | 🟡 Đã chặn phần nguy hiểm nhất (đã quyết định, xem 1.3 #1) | **ĐÃ CHỐT**: `per_chain_allocation` là trần thực thi chủ động tại Reserve (mục 2.3), không phải audit thụ động — kẻ tấn công chiếm 1 chain chỉ rút được tối đa phần đã cấp phép cho chain đó, không lan ra hệ thống. Việc còn lại (P2): implement đúng đặc tả mục 2.3, có test case riêng ở P5 |
| 2 | **Không anchor state root theo từng account → chain chết mất dữ liệu không thể phục hồi cho người dùng** (mục 5.2.2) | Bảo mật/Vận hành — cấp mô hình | 🔴 Rất cao | Phải quyết định ở P0: chain nào bắt buộc publish state-root + archival snapshot định kỳ. Nếu bỏ qua, mọi cam kết "an toàn" ở mục 5.2.1 phương án A chỉ đúng khi chain còn sống — không đúng khi chain chết hẳn |
| 3 | Xác thực origin sender ở contract đích thiếu 1 trong 2 chiều (Gateway xác thực nguồn / contract xác thực Gateway) (mục 2.6.4) | Bảo mật | 🔴 Cao | Bắt buộc checklist 2 chiều khi review contract nhận cross-chain call, đưa vào tài liệu hướng dẫn dev bắt buộc |
| 4 | Quản trị `ChainRegistry`/`AssetRegistry` tái tạo mô hình `onlyOwner` tập trung (mục 5.4) | Bảo mật | 🔴 Cao | Đặc tả cơ chế biểu quyết on-chain ngay ở P0, cùng lúc với #1 |
| 5 | Root Anchor (hoặc Reserve chain nếu chọn phương án A) bị chiếm do committee thiết kế sai | Bảo mật | 🔴 Cao | Thiết kế committee đại diện đủ rộng ngay từ đầu |
| 6 | Double-mint qua đường hoàn tiền nếu thiếu tính atomic/idempotent (mục 2.4) | Bảo mật | 🟡 Trung bình-Cao | Bắt buộc test case riêng khi security review (P5) |
| 7 | Bug trong code verify Merkle/BLS mới | Bảo mật | 🟡 Trung bình-Cao | Bắt buộc security review + test vector trước mainnet |
| 8 | Vòng lặp gọi chéo A→B→A không giới hạn hop (mục 2.6.2) | Bảo mật/Vận hành | 🟡 Trung bình | Bắt buộc trường `hop_count`/`ttl` trong envelope message ngay từ P2, từ chối cứng khi vượt ngưỡng |
| 9 | Gas không giới hạn cho lệnh gọi `CONTRACT_CALL` inbound → DoS (mục 2.6.5) | Bảo mật | 🟡 Trung bình | Khoá gas liên chuỗi từ nguồn, gas cap khi thực thi ở đích, không áp dụng "free gas" cho nhánh này |
| 10 | Rogue-key BLS nếu thiếu proof-of-possession khi đăng ký committee (mục 5.4) | Bảo mật | 🟡 Trung bình (cần xác minh mức độ thật) | Xác minh cơ chế hiện có ở P0/P1, bổ sung PoP-check nếu thiếu |
| 11 | Ordered channel gây head-of-line blocking nếu áp dụng sai use-case (mục 2.6.3) | Vận hành/Hiệu năng | 🟡 Trung bình | Mặc định unordered, chỉ dùng ordered khi ứng dụng khai báo rõ và chấp nhận đánh đổi |
| 12 | Mega-batch relay lặp lại lỗi mega-block đã ghi nhận | Hiệu năng | 🟡 Trung bình (đã có tiền lệ thật) | Cấu hình giới hạn batch cứng ngay từ bản đầu |
| 13 | RPC Go↔Root Anchor không có circuit breaker | Vận hành | 🟡 Trung bình | Thiết kế cache + breaker song song với code chính, không để sau |
| 14 | Committee rotation race giữa các chain | Bảo mật | 🟡 Trung bình | Fail-closed theo epoch, không dùng timeout |
| 15 | Data availability: pruning xoá dữ liệu cần cho Merkle proof thông thường (mục 5.4) | Vận hành/Bảo mật | 🟡 Trung bình | Cửa sổ giữ dữ liệu phải gắn với trạng thái `PENDING` của message, không theo lịch pruning mặc định |
| 16 | Thiếu giám sát lệch tổng cung | Vận hành | 🟢 Thấp-Trung bình | Thêm dashboard cùng đợt triển khai Root Anchor |
| 17 | Giả mạo `AssetRegistry` (asset ngoài native coin) | Bảo mật | 🟢 Thấp-Trung bình | Dùng chung cơ chế quản trị đa số với #4 |
| 18 | MEV/front-run ở nhánh `CONTRACT_CALL` | Bảo mật | 🟢 Thấp | Ghi nhận là rủi ro đã biết, không chặn bản đầu |
| 19 | Free-gas mint bị lợi dụng spam CPU verify | Bảo mật | 🟢 Thấp | Relay TX tính phí thật, chỉ miễn phí bước mint sau verify |

---

## 8. Lộ trình triển khai đề xuất (đã cập nhật v10)

| Giai đoạn | Nội dung | Phụ thuộc |
|---|---|---|
| **P0** | **Mọi quyết định kiến trúc đã CHỐT ở mục 1.3 — P0 giờ là hiện thực hoá thành đặc tả kỹ thuật chi tiết (không còn là "lựa chọn"):** (a) đặc tả chi tiết schema `ChainRegistry`/`GlobalSupplyLedger.per_chain_allocation` đúng theo mục 2.3 (trần thực thi chủ động); (b) đặc tả `state_root`/`archival_endpoint` bắt buộc cho mọi chain (mục 2.1); (c) đặc tả cơ chế biểu quyết on-chain ≥2/3 + delay 72h cho `ChainRegistry`/`AssetRegistry` (mục 1.3 #3); (d) thêm bước `PopVerify` tường minh khi đăng ký committee; (e) xác nhận ≥4 chain sáng lập góp validator cho Reserve trước khi go-live (mục 1.3 #5); (f) hiện thực `hop_count` mặc định 6, channel mặc định unordered | Không |
| P1 | Triển khai Root Anchor/Reserve Chain (dùng nguyên `consensus/`+`execution/` hiện có, deploy như 1 network mới) | P0 |
| P2 | Viết `GatewayPrecompile` mới (`outbound()` + `attestCommit()`/`claimMessage()` mục 13.3 làm đường mặc định, `verifyAndExecute()` làm đường đơn giản dự phòng + trần cấp phép `per_chain_allocation` mục 2.3 + đường hoàn tiền mục 2.4 + hop-count + gas cap inbound mục 2.6.5), thay thế hoàn toàn `cross_chain_handler` cũ | P1 |
| P3 | Thêm bước `CommitteeUpdate` + `StateRootCheckpoint` (account-level) vào epoch transition hiện có | P1 |
| P4 | Relayer reference implementation (permissionless, có thể chạy song song nhiều bên) | P2 |
| P5 | Security review độc lập cho toàn bộ luồng verify (BLS + Merkle + replay + double-mint qua refund + xác thực origin sender 2 chiều mục 2.6.4) trước khi thử nghiệm giá trị thật | P2-P4 |
| P6 | Mở rộng `AssetRegistry` cho token/NFT ngoài native coin, cùng cơ chế quản trị P0(c) | P2 |
| P7 | Dashboard giám sát (relay latency, supply drift, Root Anchor liveness, data-availability window cho message PENDING) | P1-P4 |
| P8 | Đặc tả và diễn tập quy trình "Chain-Death Recovery" (mục 5.2.2) — bao gồm runbook biểu quyết declare-dead, quy trình nộp Merkle proof claim | P3, P7 |

---

## 9. Phụ lục — Tham chiếu code đã kiểm chứng trong quá trình phân tích

- `execution/pkg/cross_chain_handler/cross_chain_batch_submit.go:246` — hàm `quorum()` không được gọi (dead code, bằng chứng lỗ hổng 5.1)
- `execution/pkg/cross_chain_handler/cross_chain_batch_submit.go:230-242` — `VerifyBatchSubmitSender`, chỉ verify 1 địa chỉ gửi, không verify ngưỡng
- `execution/contracts/cross_chain/CrossChainGateWay_v3.sol:257-261` — `batchSubmit` thân hàm rỗng, logic thật nằm ở Go interceptor
- `execution/pkg/mvm/tz_hardware_engine.go` — `ProcessNativeMintBurn`, `SendNative` (giữ lại dùng cho thiết kế mới)
- `execution/pkg/mvm/extension.go:285-302` — `bls.VerifySign`/`bls.VerifyAggregateSign` (tái dùng cho verify quorum cert)
- `consensus/metanode/meta-consensus/core/src/stake_aggregator.rs` — `StakeAggregator<QuorumThreshold>` (nền tảng quorum cert)
- `consensus/metanode/meta-consensus/core/src/commit.rs:273,294` — `CertifiedCommits`/`CertifiedCommit`
- `execution/executor/unix_socket_handler_epoch_state.go:560`, `execution/cmd/simple_chain/genesis-main.json:7` — `epoch_duration_seconds` (300-900s, dùng để ước lượng tần suất `ChainRegistry` update)
- `note/report/tps_e2e_analysis_2026-07-14.md` — bằng chứng thật về lỗi mega-block (50k TX/block → E2E tụt xuống ~5.000 tx/s), áp dụng trực tiếp vào giới hạn batch relay (mục 4)
- `HOW_TO_TUNE_BLOCK_SIZE.md` — khuyến nghị kích thước block WAN (10.000-20.000 TX, <5MB payload)
- `note/private_chain_solutions_evaluation.md` mục 5 — cảnh báo có sẵn về rủi ro cầu nối multisig/PoA tập trung, xác nhận lý do loại bỏ mô hình Embassy

**Bổ sung sau vòng rà soát v2:**

- `execution/pkg/bls/bls.go:20` — DST `BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_` (ciphersuite tương thích PoP-scheme); **chưa xác nhận được** có bước `PopVerify` khi đăng ký validator key hay không trong phạm vi đã đọc — cần theo dõi ở mục 5.4/P0
- `execution/pkg/pruning/nomt_pruner.go`, `execution/pkg/pruning/pruning_manager.go` — cơ chế pruning hiện có, cần đối chiếu cửa sổ giữ dữ liệu với yêu cầu Merkle proof cho message `PENDING` (mục 5.4)
- Bằng chứng weakest-link (mục 5.2): không có 1 dòng code cụ thể để trích — đây là kết luận suy luận từ chính mô hình dữ liệu `ChainRegistry`/`GlobalSupplyLedger` ở mục 2.1/2.3, không phải lỗi trong code hiện có
- `execution/pkg/common/common.go:56 AddressFromPubkey`, `execution/pkg/utils/sign_helper/helper.go:56` — dùng `crypto.PubkeyToAddress` chuẩn Ethereum (keccak của public key, không có chain_id salt) — bằng chứng cho quyết định mục 1.3 #9 (địa chỉ ví giống hệt nhau trên mọi chain)

**Bổ sung sau vòng rà soát v10 (thiết kế song song hoá):**

- `note/block_stm_architecture_review.md` — toàn bộ cơ chế Block-STM/Union-Find/`RelatedAddresses`/`AccessList` thật của Metanode Core, dùng làm nền tảng cho toàn bộ mục 13.2-13.6 (mô hình "Attest-then-Claim", phân loại "Native Go-Only (BLS)" vs "Regular Transfer", hiện tượng "Hot-Contract")

---

## 10. Kịch bản sử dụng thực tế (User Scenarios)

Mục đích: kiểm chứng thiết kế bằng cách "chạy thử" trên giấy qua các tình huống người dùng thật sẽ gặp, viết theo góc nhìn người dùng/vận hành thay vì thuần kỹ thuật. Mọi cơ chế nhắc tới đều đã có trong mục 1-9, không phát sinh thiết kế mới.

**Quy ước chung mọi kịch bản:** người dùng có **đúng 1 địa chỉ ví** dùng được trên mọi private chain và Reserve (mục 1.3 #9) — không cần đổi ví khi thao tác liên chuỗi.

### 10.1 Chuyển native coin từ ví A sang ví B (kịch bản phổ biến nhất — G2)

| Bước | Diễn ra ở đâu | Người dùng thấy gì | Thời gian |
|---|---|---|---|
| 1 | Ví trên chain A gọi `outbound(dest=B, value=100, tip=0.1)` | Trạng thái "Đang gửi..." | ngay lập tức |
| 2 | A burn 100 coin, commit certified | Trạng thái đổi thành "Đang xử lý liên chuỗi" | ~1-2s |
| 3 | Relayer (bất kỳ ai) lấy quorum cert + proof, nộp lên Reserve | (nền, người dùng không thấy) | ~1-3s |
| 4 | Reserve kiểm tra `per_chain_allocation[A] >= 100` → đủ → cập nhật ledger, phát hành message mint tới B, trả tip 0.1 cho relayer | (nền) | ~1-2s |
| 5 | Relayer khác (hoặc cùng 1 relayer) mang quorum cert của Reserve, nộp lên B | (nền) | ~1-3s |
| 6 | B verify, mint 100 coin cho ví người dùng | Trạng thái "Hoàn tất" — số dư 100 coin xuất hiện trên B | ~1-2s |
| **Tổng** | | | **~6-12s** (mục 4) |

### 10.2 Gọi contract kèm giá trị trên chain khác (VD: mua vật phẩm game trên chain B bằng coin từ chain A)

Giống 10.1 ở bước 1-5, khác ở bước cuối: thay vì mint thẳng cho ví người dùng, B mint cho địa chỉ hệ thống rồi **gọi contract đích** với `payload` kèm theo (mục 2.6.4 — contract đích đọc `getOriginalSender()` để biết đúng người mua là ai, không phải địa chỉ hệ thống). Nếu contract chạy thành công → người dùng thấy vật phẩm xuất hiện trong ví trên B. Độ trễ tương tự 10.1, cộng thêm thời gian thực thi contract (thường <1s, trừ khi payload phức tạp — xem giới hạn gas mục 2.6.5).

### 10.3 Gọi contract thất bại — hoàn tiền tự động (thể hiện mục 2.4)

Tiếp theo 10.2, nhưng contract trên B revert (VD: vật phẩm đã hết hàng đúng lúc giao dịch tới):

1. B vẫn finalize trạng thái "FAILED" cho message (không rollback im lặng), phát sinh quorum cert riêng cho kết quả FAILED này.
2. Relayer mang quorum cert FAILED của B về Reserve → Reserve verify → hoàn `per_chain_allocation` (tăng lại phần đã trừ của A tương ứng) → phát sinh message hoàn tiền tới A.
3. Ví người dùng trên A thấy số dư 100 coin **quay trở lại** sau khoảng thời gian tương đương 1 chu kỳ liên chuỗi nữa (~+6-12s so với lúc phát hiện FAILED).
4. UX cần hiển thị rõ 3 trạng thái tuần tự: "Đang xử lý" → "Thất bại ở chain đích, đang hoàn tiền" → "Đã hoàn tiền" — không nên chỉ hiện "Thất bại" rồi im lặng (người dùng sẽ tưởng mất tiền).

### 10.4 Chain đích tạm ngừng đúng lúc người dùng gửi (thể hiện mục 5.2.2 mức độ (1) và Zero-Fork)

1. Người dùng gửi 100 coin từ A sang B, nhưng B đang bảo trì/mất kết nối tạm thời.
2. Relayer không thể nộp được lên B (thử lại liên tục, không thành công).
3. Ví người dùng trên A hiển thị "Đang chờ xử lý ở chain đích" — **không hiện lỗi, không tự động hoàn tiền** (đúng nguyên tắc mục 2.4 điểm 4: không dùng timeout để quyết định).
4. Khi B khởi động lại (vài phút đến vài giờ sau), relayer nộp lại thành công → giao dịch hoàn tất bình thường, không mất gì, không cần người dùng làm gì thêm.
5. Đây là lý do UX **không nên** hiển thị đồng hồ đếm ngược hay thông báo "giao dịch thất bại" trong tình huống này — chỉ nên hiện "đang chờ", có thể kèm cảnh báo "chain đích hiện đang gián đoạn" nếu muốn minh bạch với người dùng.

### 10.5 Gọi qua lại 2 chiều A→B→A (request/response, thể hiện mục 2.6.2 hop_count)

VD: contract trên A hỏi giá 1 tài sản niêm yết trên B, B trả lời bằng 1 message gọi ngược lại A với kết quả:

1. A→B: `hop_count = 1`. B nhận, xử lý, phát sinh message phản hồi B→A: `hop_count = 2`.
2. A nhận phản hồi, xử lý xong (không phát sinh thêm hop) → `hop_count` dừng lại ở 2, còn cách xa giới hạn 6 (mục 1.3 #7) — pattern hợp lệ, không bị chặn.
3. Nếu do bug, contract trên A lại tự động phát sinh thêm 1 outbound message mới gửi lại B (vòng lặp ngoài ý muốn) — tới `hop_count = 7` sẽ bị Gateway từ chối cứng, ngăn vòng lặp tiêu tốn tài nguyên relayer vô hạn. Log/monitoring nên cảnh báo ngay khi 1 message chạm ngưỡng 4-5 hop (gần giới hạn) để phát hiện sớm bug dạng này trước khi bị chặn cứng.

### 10.6 Vận hành: thêm 1 private chain mới (chain D) vào hệ thống

Góc nhìn đội vận hành/đối tác mới, không phải người dùng cuối:

1. Chain D triển khai bằng công cụ có sẵn (`gen_private_chain.py`), chạy độc lập, **chưa kết nối** gì với hệ thống liên chuỗi.
2. Đại diện D gửi đề xuất "đăng ký chain D" lên Root Anchor kèm: `committee` (danh sách validator + PoP), `state_root` ban đầu, `archival_endpoint`.
3. Các chain đang active biểu quyết (mục 1.3 #3) — cần **≥2/3 đồng ý**. Sau khi đạt ngưỡng, có **72 giờ chờ (delay window)** trước khi hiệu lực — trong thời gian này bất kỳ ai cũng có thể phát hiện bất thường (VD: `committee` khai báo trùng khớp khả nghi với 1 chain khác).
4. Hết 72 giờ, D chính thức vào `ChainRegistry`, `per_chain_allocation[D] = 0` (chưa có coin nào, đúng thiết kế — D phải nhận allocation từ 1 giao dịch nạp/nhận từ chain khác trước, không được cấp sẵn).
5. Người dùng D giờ có thể nhận coin từ A/B/C qua đúng luồng 10.1 (D là chain đích) — muốn gửi đi thì D phải chờ có `per_chain_allocation[D] > 0` trước (tức phải nhận trước khi gửi được, hợp lý vì D mới, chưa ai gửi coin vào D thì D không có gì để gửi đi).

### 10.7 Chain nhỏ bị tấn công cố rút vượt mức — hệ thống chặn được (minh chứng cơ chế bảo mật mục 2.3 hoạt động)

1. Chain C (nhỏ, 4 validator) bị chiếm 3/4 validator. Kẻ tấn công cho C tự "commit" 1 block gian lận: local state của C hiển thị user X có 1 triệu coin (chưa từng có allocation tương ứng).
2. Kẻ tấn công gọi `outbound(dest=B, value=1000000)` từ C, có quorum cert hợp lệ (vì đúng là 3/4 validator của C đã ký, dù C đã bị chiếm).
3. Relayer mang quorum cert này tới Reserve. Reserve verify chữ ký **đúng** (không phát hiện được C bị chiếm chỉ từ chữ ký) — nhưng khi kiểm tra `per_chain_allocation[C]`, con số này chỉ là, ví dụ, 500 (mức C từng được cấp phép hợp pháp trước đó).
4. Reserve **từ chối** giao dịch vì `500 < 1.000.000` — không cập nhật ledger, không phát hành message mint tới B. Kẻ tấn công **tối đa chỉ rút được 500** (toàn bộ phần hợp pháp C từng có), không rút được 1 triệu khống.
5. Đây chính là ranh giới bảo mật đã chốt ở mục 2.3/5.2: thiệt hại bị giới hạn ở đúng phạm vi chain C, không lan ra `per_chain_allocation` của các chain khác.

### 10.8 Chain chết hẳn — người dùng cuối rút tiền qua Chain-Death Recovery (thể hiện mục 5.2.2)

1. Chain E ngừng hoạt động vĩnh viễn (công ty vận hành giải thể), nhưng đã tuân thủ yêu cầu bắt buộc (mục 1.3 #2): có anchor `state_root` + archival snapshot đầy đủ tới thời điểm epoch cuối cùng.
2. Sau nhiều tuần không có block mới, các chain còn lại biểu quyết ≥2/3 xác nhận "declare E dead" trên Root Anchor — quyết định vĩnh viễn, không thể đảo ngược.
3. Người dùng từng có 50 coin trên E (tại thời điểm checkpoint cuối) truy cập 1 công cụ ví/claim portal, nộp **Merkle proof** số dư của mình (dựa vào `state_root` cuối cùng đã anchor + dữ liệu archival).
4. Reserve verify proof, giải phóng 50 coin cho người dùng ngay trên Reserve (hoặc trên 1 chain khác người dùng chỉ định), đánh dấu claim đã dùng (chống nộp lại lần 2).
5. **Giới hạn cần truyền đạt rõ cho người dùng:** nếu người dùng có giao dịch phát sinh SAU thời điểm checkpoint cuối (VD: nhận thêm 20 coin ngay trước khi E sập, chưa kịp checkpoint) — 20 coin đó **không có trong proof**, không khôi phục được. Đây là giới hạn vật lý (data availability) đã nêu ở mục 5.2.2, không phải lỗi của quy trình claim.

---

## 11. Đặc tả kỹ thuật: API/ABI của `GatewayPrecompile`

Cụ thể hoá interface để P2 (lộ trình mục 8) có thể bắt đầu code trực tiếp — đây là chi tiết cài đặt bám theo mọi quyết định đã chốt ở mục 1.3, không mở lại bất kỳ quyết định kiến trúc nào.

### 11.1 Địa chỉ hệ thống & lưu ý về cách đọc mục 11

Giữ đúng convention hiện có của repo (`CROSS_CHAIN_CONTRACT_ADDRESS` cũ ở `0x1002`, xem `pkg/common`): dùng 1 địa chỉ precompile cố định mới, ví dụ `0x1010`, để không đụng namespace của contract cũ đang giữ tham chiếu lịch sử (mục 5.1).

**Lưu ý bắt buộc đọc trước khi code:** các khối code trong mục 11 viết dưới dạng cú pháp Solidity **chỉ để mô tả hình dạng ABI/kiểu dữ liệu** cho dễ đọc — **không có nghĩa là phải triển khai bằng 1 smart contract Solidity thật chạy qua EVM interpreter**. Đúng theo convention đã có sẵn của repo (`execution/pkg/cross_chain_handler/` cũ: dispatch theo địa chỉ contract cố định, logic thật nằm ở Go native handler, Solidity chỉ giữ vai trò khai báo ABI để encode/decode calldata) — implement mục 11 theo đúng pattern này: 1 Go package mới (thay thế `cross_chain_handler` cũ) intercept giao dịch gửi tới địa chỉ `0x1010`, decode theo ABI mô tả ở mục 11.2, chạy logic native Go (gọi `ProcessNativeMintBurn`, verify BLS qua `pkg/bls`, v.v.) — không phải deploy bytecode Solidity thật.

### 11.2 Cấu trúc dữ liệu dùng chung

```solidity
struct CrossChainMessage {
    bytes32 messageId;      // = tx hash của giao dịch outbound() gốc — khoá chống replay CHÍNH cho channel
                            // unordered (mặc định, mục 1.3 #6/13.3.2); độc lập tự nhiên giữa các người gửi,
                            // không cần biến đếm dùng chung nào
    uint256 sourceChainId;
    uint256 destChainId;
    uint256 sequence;       // CHỈ có ý nghĩa/được kiểm tra khi channel.ordered == true (mục 2.6.3, 13.3.2);
                            // với unordered, dùng messageId ở trên, field này có thể để 0
    uint8   hopCount;       // tăng dần mỗi lần qua Gateway, chặn cứng > 6 (mục 1.3 #7)
    address sender;         // địa chỉ gốc trên chain nguồn — cùng địa chỉ trên mọi chain (mục 1.3 #9)
    address target;         // address(0) nếu là asset-transfer thuần, khác 0 nếu CONTRACT_CALL
    uint256 assetId;        // 0 = native coin; khác 0 = tra AssetRegistry (mục 2.5)
    uint256 value;
    bytes   payload;
    uint256 tip;            // relay tip bằng native coin, khoá kèm lúc gửi (mục 2.2.1)
    bool    ordered;        // false mặc định (mục 1.3 #6); true nếu channel khai báo cần ordered
}

struct QuorumCert {
    uint64  epoch;                  // epoch của uỷ ban đã ký — B từ chối nếu epoch chưa được Root Anchor xác nhận (mục 5.3)
    bytes   aggregateSignature;     // BLS aggregate signature (pkg/bls/blst)
    uint256 signerBitmap;           // bitmap validator đã ký, dùng đối chiếu stake với ChainRegistry[chainId].committee
}

struct MerkleProof {
    uint256 leafIndex;
    bytes32[] siblings;
}
```

### 11.3 Hàm ghi (state-changing)

```solidity
// Gọi bởi user/wallet trên chain nguồn — tạo message, burn nếu value>0 hoặc assetId!=0.
// Trả về messageId (= tx hash gốc, dùng để tra cứu trạng thái qua getMessageStatus).
function outbound(
    uint256 destChainId,
    address target,
    bytes calldata payload,
    uint256 assetId,
    uint256 tip
) external payable returns (bytes32 messageId);

// Gọi bởi relayer (permissionless, bất kỳ ai) — nộp message + proof để chain đích/Reserve xử lý.
// Nội bộ: verify QuorumCert + MerkleProof + sequence (mục 2.2), rồi:
//   - nếu message.assetId == 0 && target == 0 && msg.sender's chain == Reserve: cập nhật per_chain_allocation (mục 2.3)
//   - nếu target != 0: gọi target.call(payload) với context origin sender (mục 2.6.4), có gas cap (mục 2.6.5)
// Trả tip cho relayer trong CÙNG giao dịch nếu xử lý thành công lần đầu (chống double-pay qua kiểm tra sequence).
function verifyAndExecute(
    CrossChainMessage calldata message,
    QuorumCert calldata cert,
    MerkleProof calldata proof
) external;

// ĐƯỜNG ĐƠN GIẢN (không tối ưu song song) — dùng cho trường hợp lẻ tẻ/khối lượng thấp
// (VD: 1 message hoàn tiền riêng lẻ, mục 2.4). Với khối lượng lớn, dùng attestCommit()+
// claimMessage() bên dưới (mục 13.3) — đó mới là đường MẶC ĐỊNH cho production.
function verifyAndExecute(
    CrossChainMessage calldata message,
    QuorumCert calldata cert,
    MerkleProof calldata proof
) external;

// ═══ ĐƯỜNG MẶC ĐỊNH CHO KHỐI LƯỢNG LỚN — mô hình "Attest-then-Claim" (mục 13.2-13.3) ═══
// Tách chi phí BLS (đắt, theo số CHAIN, làm 1 lần/commit) khỏi phần thực thi (rẻ, theo số
// MESSAGE, chạy song song thật qua Union-Find/Block-STM) — lý do chi tiết ở mục 13.2/13.3.

// PHA 1 — gọi 1 lần/commit. Nên định tuyến vào threadpool "Native Go-Only (BLS)" riêng đã có
// sẵn trong Block-STM (`block_stm_architecture_review.md` mục 4.3), không dùng chung worker
// pool EVM/MVM với claimMessage() bên dưới.
function attestCommit(
    uint256 sourceChainId,
    bytes32 commitRoot,
    uint256 aggregateAmount,   // tổng value mọi message value>0 trong commit — trừ per_chain_allocation 1 LẦN (mục 13.3.1)
    QuorumCert calldata cert
) external;

// PHA 2 — gọi N lần, MỖI LẦN LÀ 1 GIAO DỊCH RIÊNG (không gộp lại thành vòng lặp — mất hết lợi
// ích song song nếu gộp, xem mục 13.2). Khai báo AccessList = {recipient, target nếu có},
// KHÔNG đụng per_chain_allocation (mục 13.3.3).
function claimMessage(
    CrossChainMessage calldata message,
    MerkleProof calldata proof   // proof vào commitRoot đã attest ở Pha 1
) external;

// Chỉ dùng trên Reserve, sau khi chain bị "declare dead" (mục 5.2.2) — người dùng tự claim bằng Merkle proof
// số dư cá nhân tại state_root cuối cùng đã anchor của chain đó.
function claimDeadChainBalance(
    uint256 deadChainId,
    address account,
    uint256 amount,
    MerkleProof calldata accountProof   // proof vào state_root account-tree đã anchor (mục 2.1)
) external;
```

### 11.4 Hàm đọc (view) — dùng bởi contract nhận cross-chain call

```solidity
// Bắt buộc contract đích gọi hàm này để lấy người gửi gốc — KHÔNG được tin msg.sender (mục 2.6.4 điểm 1)
function getOriginalSender() external view returns (address sender, uint256 sourceChainId);

// Contract đích dùng để tự vệ — verify chính Gateway đang gọi mình, không phải TX giả mạo trực tiếp (mục 2.6.4 điểm 2)
function isCalledByGateway() external view returns (bool);

// Tra cứu trạng thái 1 message theo messageId — dùng cho UI ví hiển thị PENDING/SUCCESS/FAILED/REFUNDED (mục 10.1-10.4)
function getMessageStatus(bytes32 messageId) external view returns (uint8 status);

// Chỉ có ý nghĩa khi channel đó ordered=true (mục 11.6 Channel struct); với unordered (mặc định),
// dùng statusByMessageId theo messageId thay vì số thứ tự — xem mục 13.3.2
function getChannelSequence(uint256 sourceChainId, uint256 destChainId) external view returns (uint256);
```

### 11.5 Sự kiện (events)

```solidity
event MessageSent(bytes32 indexed messageId, uint256 indexed destChainId, uint256 sequence);
event MessageExecuted(bytes32 indexed messageId, bool success);
event MessageRefunded(bytes32 indexed messageId, uint256 amount);
event AllocationRejected(uint256 indexed chainId, uint256 requested, uint256 available); // log khi mục 2.3 chặn 1 chain rút vượt mức — dùng làm cảnh báo an ninh tức thời (mục 6)
event CommitAttested(uint256 indexed sourceChainId, bytes32 indexed commitRoot, uint256 fundedAmount); // Pha 1 xong (mục 13.3) — relayer dùng để biết khi nào có thể bắt đầu gửi hàng loạt claimMessage() cho commit này
```

`AllocationRejected` nên được đấu nối trực tiếp vào dashboard giám sát (mục 6) — đây chính là tín hiệu sớm nhất cho biết 1 chain có thể đang bị chiếm (kịch bản 10.7), cần cảnh báo ngay chứ không chờ đến đối chiếu định kỳ.

### 11.6 Bổ sung schema còn thiếu (rà soát lại theo yêu cầu)

Khi rà soát lại mục 11.2, các struct đã viết (`CrossChainMessage`, `QuorumCert`, `MerkleProof`) là đủ cho luồng chính, nhưng còn thiếu 1 số cấu trúc dữ liệu được NHẮC TỚI bằng lời ở các mục trước mà chưa có schema hình thức — bổ sung dứt điểm tại đây để mục 11 thực sự đầy đủ:

```solidity
// Thiếu ở v8 — mục 2.5 chỉ mô tả bằng lời, chưa có struct
struct AssetEntry {
    uint256 assetId;
    uint256 homeChainId;              // chain "gốc" giữ token thật, chỉ chain này được LOCK (không mint)
    address canonicalContract;        // địa chỉ contract token trên homeChainId
    mapping(uint256 => address) wrappedContract;  // chainId → địa chỉ bản wrapped trên chain đó
    bool active;
}
// Đăng ký/sửa AssetEntry đi qua ĐÚNG cơ chế quản trị ≥2/3 + delay 72h như ChainRegistry (mục 1.3 #3),
// không phải cơ chế riêng — tránh lặp lại rủi ro giả mạo AssetRegistry đã nêu ở mục 5.4.

// Thiếu ở v8 — mục 2.4 mô tả bằng lời "channel_sequence_status[seq] ∈ {PENDING, RESOLVED}",
// chưa định nghĩa enum + struct kênh hình thức.
// SỬA Ở v10 (mục 13.3.2): bỏ phụ thuộc vào 1 biến đếm `nextSequence` DÙNG CHUNG cho channel
// unordered (mặc định) — đây là 1 "ô nhớ nóng" gây tranh chấp ghi giữa MỌI người gửi trên
// cùng 1 channel, y hệt vấn đề per_chain_allocation ở 13.3.1 nhưng ở phía gửi. Thay bằng
// messageId = tx hash của outbound() (đã duy nhất tự nhiên, không cần biến đếm chung) làm
// khoá chống replay — các message của người gửi khác nhau ghi vào các slot mapping ĐỘC LẬP.
enum MessageStatus { PENDING, SUCCESS, FAILED, REFUNDED }

struct Channel {
    uint256 sourceChainId;
    uint256 destChainId;
    bool    ordered;                  // mục 1.3 #6 — false mặc định
    // --- Chỉ dùng khi ordered = true (chấp nhận đánh đổi serialize có chủ đích, mục 2.6.3) ---
    uint256 nextSequence;              // sequence kế tiếp cấp cho message mới — CHỈ cấp/kiểm tra khi ordered=true
    uint256 lastProcessedSequence;
    // --- Dùng khi ordered = false (mặc định, tối ưu song song, mục 13.3.2) ---
    mapping(bytes32 => MessageStatus) statusByMessageId;  // key = messageId (txHash outbound()), không phải sequence
}

// Mới ở v10 (mục 13.3) — trạng thái 1 commit đã được attestCommit() xác nhận,
// dùng để claimMessage() đọc (không ghi) khi verify từng message riêng lẻ.
struct AttestedCommit {
    uint256 sourceChainId;
    bytes32 commitRoot;
    uint64  epoch;
    uint256 fundedAmount;      // = aggregateAmount đã trừ ceiling ở attestCommit() (mục 13.3.1)
    uint256 claimedAmount;     // cộng dồn qua từng claimMessage() — dùng để phát hiện lệch nếu Merkle proof sai/giả (phòng vệ bổ sung, không phải cơ chế an toàn chính)
}
// mapping(bytes32 => AttestedCommit) attestedCommits — key = keccak256(sourceChainId, commitRoot)

// Thiếu ở v8 — mục 2.1 dùng tuple (validator_pubkey_bls, stake), cần thêm popSignature tường minh
struct ValidatorEntry {
    bytes   pubkeyBls;
    uint256 stake;
    bytes   popSignature;             // BẮT BUỘC verify khi đăng ký (mục 1.3 #4) — PopVerify(pubkeyBls, popSignature)
}

// Thiếu ở v8 — mục 1.3 #3 mô tả bằng lời "biểu quyết ≥2/3 + delay 72h", chưa có struct proposal
struct GovernanceProposal {
    bytes32 proposalId;
    uint8   kind;                     // 0=RegisterChain, 1=UnregisterChain, 2=RegisterAsset, 3=UpdateCommittee...
    bytes   payload;                  // ABI-encoded dữ liệu tương ứng với kind
    uint256 votesFor;                 // tính theo SỐ CHAIN active bỏ phiếu, không theo stake — 1 chain = 1 phiếu (mục 1.3 #3 dùng "số chain", không phải stake, để tránh 1 chain lớn chi phối quản trị)
    uint256 proposedAt;
    uint256 effectiveAt;              // = proposedAt + 72h SAU KHI đạt đủ ≥2/3, không phải ngay lúc đề xuất
    bool    executed;
}

// Thiếu ở v8 — mục 5.2.2/10.8 mô tả "Merkle proof số dư account", chưa định nghĩa leaf + chống double-claim
struct AccountLeaf {
    address account;
    uint256 balance;                  // số dư native coin tại state_root đã anchor cuối cùng (mục 2.1)
}
// mapping(chainId => mapping(address => bool)) deadChainClaimed — chống nộp claimDeadChainBalance() 2 lần (mục 10.8 điểm 4)
```

---

## 12. Quy trình kiểm thử & triển khai theo giai đoạn (Test & Rollout Plan)

Bổ sung phần còn thiếu đã nêu ở mục 0 (v7, điểm 14) — cụ thể hoá thành checklist trước khi cho phép P5 (security review) và go-live thật.

| Giai đoạn | Nội dung | Điều kiện qua (gate) |
|---|---|---|
| **T0 — Unit test** | Test độc lập từng cơ chế: verify BLS aggregate (test vector cố định), Merkle proof (kể cả proof sai/thiếu), chống replay theo `messageId` cho channel unordered + theo `sequence` cho channel ordered (mục 13.3.2), chặn `hop_count > 6`, chặn allocation vượt trần (mục 2.3), atomic/idempotent của resolve (mục 2.4) | 100% pass, coverage các nhánh lỗi (không chỉ happy path) |
| **T1 — Devnet cục bộ** | 1 Reserve + 2 private chain trên cùng máy. Chạy tự động hoá **toàn bộ 8 kịch bản mục 10** như integration test | Cả 8 kịch bản chạy đúng kỳ vọng, kể cả 10.3 (refund) và 10.7 (chặn tấn công) |
| **T2 — Testnet nội bộ nhiều chain** | Triển khai thật ≥4 chain (đúng mức tối thiểu mục 1.3 #5) trên hạ tầng riêng biệt (khác máy/mạng), có độ trễ mạng thật (không phải localhost 0ms — lưu ý bài học `tps_e2e_analysis` mục 4). **Đo thật chi phí verify BLS/commit** (mục 13.1) và thông lượng thật của mô hình `attestCommit()`+`claimMessage()` (mục 13.3) ở các kích cỡ lô khác nhau (500/2.000/4.000 message/lô) — **đối chiếu thêm tỷ lệ Abort/re-execution thật của Union-Find** khi nhiều `claimMessage()` cùng block (xác nhận không bị gộp thành siêu nhóm ngoài ý muốn, mục 13.3.1) để xác nhận hoặc điều chỉnh con số tham khảo ngành ở mục 13.1 | Đo được số liệu độ trễ thật (đối chiếu ước tính mục 4, ~6-12s cho message có giá trị) VÀ số liệu thông lượng thật (đối chiếu mục 13, bao gồm mức độ song song hoá thật đạt được so với lý thuyết) — không triển khai T4 nếu chưa có số đo thật cho cả 2 |
| **T3 — Kiểm thử đối kháng (chaos/adversarial)** | Chủ động mô phỏng: (a) chiếm 3/4 validator 1 chain nhỏ trên testnet, thử tấn công y hệt kịch bản 10.7 — xác nhận `AllocationRejected` bắn ra đúng lúc; (b) tắt Reserve tạm thời — xác nhận private chain khác vẫn hoạt động bình thường (mục 6); (c) mô phỏng "chết hẳn" 1 chain testnet, chạy thử toàn bộ quy trình Chain-Death Recovery (10.8) từ đầu đến cuối, kể cả bước biểu quyết declare-dead | Không có tấn công nào vượt qua trần `per_chain_allocation`; Chain-Death Recovery chạy được end-to-end ít nhất 1 lần thành công trên testnet trước khi tin tưởng đưa vào runbook thật |
| **P5 (đã có ở mục 8) — Security review độc lập** | Bên thứ 3 audit toàn bộ luồng verify + 3 kịch bản T3 | Không còn finding mức Cao/Nghiêm trọng chưa xử lý |
| **T4 — Rollout mainnet theo giai đoạn (staged)** | **Giai đoạn 1**: chỉ bật message thuần (`value=0`, mục 2.2) giữa các chain — rủi ro thấp nhất vì không đụng tới native coin thật. Theo dõi tối thiểu vài tuần. **Giai đoạn 2**: bật chuyển giá trị qua Reserve (mục 2.3) với hạn mức nhỏ ban đầu (VD: trần tổng `per_chain_allocation` toàn hệ thống giới hạn thấp trong 1-2 tháng đầu, tăng dần). **Giai đoạn 3**: gỡ hạn mức sau khi đã vận hành ổn định, có số liệu giám sát (mục 6) chứng minh không có bất thường | Mỗi giai đoạn chỉ mở tiếp khi giai đoạn trước không phát sinh sự cố nghiêm trọng trong thời gian theo dõi tối thiểu đã định |
| **T5 — Bug bounty** (khuyến nghị, không bắt buộc) | Mở thưởng lỗi công khai trước khi gỡ hạn mức ở T4 giai đoạn 3, tập trung vào đúng bề mặt tấn công mới (verify BLS/Merkle, `per_chain_allocation`, Chain-Death Recovery) | Tuỳ ngân sách — có thể bỏ qua nếu team tự tin sau T3 + P5, nhưng khuyến nghị làm nếu giá trị custody tại Reserve lớn |

**Nguyên tắc xuyên suốt của quy trình rollout:** không giai đoạn nào được rút ngắn bằng cách "tin tưởng" thay vì đo đạc thật — đúng tinh thần đã áp dụng nhất quán trong toàn tài liệu (Zero-Fork: thà chờ có bằng chứng, không suy đoán).

---

## 13. Phân tích thông lượng hệ thống (Throughput) & thiết kế tối ưu

Mục 4 đã phân tích **độ trễ** (latency — 1 giao dịch mất bao lâu). Mục này phân tích **thông lượng** (throughput — hệ thống chịu được bao nhiêu giao dịch/giây), vì đây là 2 trục khác nhau và tối ưu cho trục này không tự động tối ưu trục kia.

### 13.1 Xác định đúng tầng nào là trần thông lượng (bottleneck)

| Tầng | Trần thông lượng | Vì sao |
|---|---|---|
| Local TX trên 1 private chain (A hoặc B) | ~12-18k tx/s **sau khi tune đúng** theo `HOW_TO_TUNE_BLOCK_SIZE.md` (4.000-8.000 TX/block, tránh mega-block — đã đo thật, xem `tps_e2e_analysis`) | Đây là trần chung cho MỌI tx, kể cả `outbound()`/`verifyAndExecute()` — chúng cạnh tranh block space với tx thường của người dùng trên đúng chain đó |
| **Reserve — điểm hội tụ (funnel) của TOÀN BỘ message có giá trị (mục 2.3)** | **= đúng trần của 1 chain đơn (ví dụ ~12-18k tx/s), NHƯNG đây là trần dùng chung cho TẤT CẢ cặp chain trong hệ thống, không phải trần riêng của từng cặp** | Vì mọi message value>0 đều phải qua Reserve (mục 2.2), tổng lưu lượng chuyển giá trị của **toàn mạng** (bất kể có N private chain) bị giới hạn bởi đúng 1 con số: thông lượng của riêng Reserve. Đây là đánh đổi trực tiếp của việc chọn Phương án A (custodial, mục 5.2.1) để đổi lấy an toàn — **không miễn phí về mặt thông lượng** |
| Chi phí verify BLS aggregate mỗi commit (không phải mỗi message, xem 13.2) | **Chưa đo thật trên hạ tầng Metanode** — theo benchmark công khai điển hình của thư viện BLS12-381 (như blst đang dùng ở `pkg/bls`), 1 phép verify (gồm pairing) thường ở mức **vài trăm đến ~1-2 nghìn phép/giây/lõi CPU** trên phần cứng phổ thông — đây là **số liệu tham khảo ngành, KHÔNG PHẢI đã đo trên Metanode**, bắt buộc đo thật ở T2/T3 (mục 12) trước khi chốt cấu hình production | Quan trọng: đây là chi phí **không thể bỏ qua** cho message liên chuỗi (khác với tx thường, nơi BLS verify mempool đã bị tắt để tăng tốc — xem `tps_e2e_analysis`: "BLS verify mempool (SKIP=true trong systemd)"). An toàn của toàn bộ cơ chế mục 2.2 phụ thuộc vào việc verify này luôn chạy — không được tắt để đổi lấy tốc độ như đã làm với tx thường |

**Kết luận quan trọng nhất của mục này:** với thiết kế "1 BLS verify / 1 message" (naive), trần thông lượng cross-chain sẽ bị chi phí BLS pairing giới hạn ở mức **thấp hơn nhiều** so với ~12-18k tx/s của tx thường — có thể chỉ còn vài trăm đến ~1-2 nghìn message/giây/lõi nếu không tối ưu. Đây chính là lý do mục 13.2 dưới đây là **tối ưu bắt buộc**, không phải tuỳ chọn.

### 13.2 SỬA LẠI so với bản trước: `verifyAndExecuteBatch()` đơn lẻ KHÔNG tận dụng được Block-STM

**Đây là điểm cần đính chính sau khi đọc kỹ `note/block_stm_architecture_review.md`.** Bản v9 (mục 13.2 cũ) đề xuất gộp N message vào 1 lời gọi `verifyAndExecuteBatch()` duy nhất — điều này **đúng** cho việc tiết kiệm chi phí BLS (1 lần verify thay vì N), nhưng **sai** nếu kỳ vọng nó chạy song song nhờ Block-STM: theo đúng cơ chế đã tài liệu hoá (`block_stm_architecture_review.md` mục 1-2), Metanode song song hoá **GIỮA CÁC GIAO DỊCH** trong 1 block bằng Union-Find dựa trên `RelatedAddresses`/`AccessList` — **không** song song hoá vòng lặp *bên trong* 1 giao dịch. Một `verifyAndExecuteBatch()` xử lý N message bằng vòng lặp nội bộ vẫn chạy **tuần tự trên 1 worker** dù tiết kiệm được chi phí BLS. Muốn tận dụng thật sự khả năng thực thi song song, phải tách thành **2 pha ở 2 loại giao dịch riêng biệt**.

### 13.3 Thiết kế tối ưu: mô hình "Attest-then-Claim" (tách chi phí mật mã khỏi phần thực thi song song hoá được)

Định nghĩa API đầy đủ đã có ở mục 11.3 (`attestCommit()` + `claimMessage()`) — không nhắc lại ở đây, chỉ giải thích lý do thiết kế:

- **Pha 1 — Attest (1 giao dịch/1 commit, chi phí theo SỐ CHAIN):** verify BLS aggregate đúng 1 lần cho cả commit, trừ ceiling `per_chain_allocation` theo TỔNG giá trị cả lô 1 lần duy nhất (không trừ riêng từng message — lý do ở 13.3.1). Đây là giao dịch thuộc nhóm "Native Go-Only (BLS)" theo đúng phân loại có sẵn của Block-STM (`block_stm_architecture_review.md` mục 2.B.3) — nên định tuyến vào threadpool BLS riêng đã có sẵn trong kiến trúc (mục 4 điểm 3 của tài liệu đó: "Phân Luồng Riêng Cho Native TX"), không tranh chấp với worker pool đang chạy EVM/MVM cho Pha 2.
- **Pha 2 — Claim (N giao dịch riêng biệt, 1 giao dịch/1 message, chi phí theo SỐ MESSAGE nhưng rẻ và song song hoá được):** chỉ cần đọc `attestedCommits[sourceChainId][commitRoot]` đã funded chưa (đọc, không tranh chấp ghi), verify Merkle proof (thuần hash, rẻ), rồi credit cho đúng 1 recipient. Khai báo AccessList = {recipient, target contract nếu CONTRACT_CALL}, **không đụng `per_chain_allocation`** (đã xử lý xong ở Pha 1) → Union-Find nhóm các `claimMessage()` của các recipient khác nhau vào các RelativeGroup độc lập → chạy thật sự song song trên nhiều lõi CPU, đúng cách Block-STM đã xử lý rất tốt "Regular Transfer" (`block_stm_architecture_review.md` mục 2.C).

#### 13.3.1 Vì sao phải tách `per_chain_allocation` ra khỏi pha Claim — tránh đúng "Hot-Contract" đã cảnh báo sẵn

Nếu để mỗi `claimMessage()` tự trừ/cộng `per_chain_allocation[chainId]`, MỌI message cùng nguồn/đích sẽ cùng ghi vào 1 ô nhớ (storage slot) — Union-Find sẽ gộp **toàn bộ N giao dịch claim** thành 1 siêu nhóm (Meta-group) vì tất cả đều xung đột ghi trên cùng 1 slot, suy biến thành chạy tuần tự **y hệt hiện tượng "Hot-Contract" đã được `block_stm_architecture_review.md` mục 2.A cảnh báo trước** (VD IDO/Airdrop cùng chạm 1 contract). Cách khắc phục: **dồn phần chạm vào ô nhớ dùng chung (`per_chain_allocation`) về đúng Pha 1 (Attest, 1 lần/commit)** — Pha 2 (Claim) chỉ còn chạm vào tài khoản riêng của từng người nhận, vốn dĩ độc lập giữa các message (đúng category "Regular Transfer" — rẻ, Union-Find xử lý tốt, theo đúng bảng so sánh mục 2.C của tài liệu Block-STM).

#### 13.3.2 Điểm nghẽn thứ 2 đã phát hiện: bộ đếm `sequence` phía gửi cũng là 1 "ô nhớ nóng"

Rà soát lại `Channel.nextSequence` (mục 11.6): nếu MỌI người dùng gửi từ A sang B đều phải đọc-tăng chung 1 biến đếm này khi gọi `outbound()`, đây cũng là 1 ô nhớ dùng chung sẽ gây xung đột/gộp nhóm y hệt 13.3.1, nhưng ở **phía gửi** thay vì phía nhận.

**Khắc phục (cập nhật schema mục 11.6):** với **channel unordered (mặc định, mục 1.3 #6)**, bỏ hẳn yêu cầu `sequence` tăng dần liên tục — dùng **`messageId = tx hash của chính giao dịch `outbound()`** (đã là duy nhất tự nhiên, không cần biến đếm dùng chung nào — đúng cách thiết kế cũ từng làm, `cross_chain_outbound.go`: "msgId = txHash gốc của user") làm khoá chống replay: `mapping(bytes32 => bool) usedMessageHash` thay cho việc dựa vào `nextSequence`. Mỗi giao dịch `outbound()` của mỗi người dùng khác nhau tự nhiên có hash khác nhau, không tranh chấp ghi với nhau ở phía gửi lẫn phía nhận (`usedMessageHash[messageId]` là các slot mapping độc lập theo keccak, không phải 1 biến đếm chung). Chỉ **channel ordered** (opt-in, chấp nhận đánh đổi liveness ở mục 2.6.3) mới thực sự cần `nextSequence`/`lastProcessedSequence` tăng dần — lúc đó việc serialize là **chủ đích**, không phải tác dụng phụ ngoài ý muốn.

#### 13.3.3 Khai báo AccessList tường minh — biến xung đột động thành xung đột tĩnh

Theo đúng khuyến nghị có sẵn của Block-STM (`block_stm_architecture_review.md` mục 2.A, lý do tồn tại của `AccessListTxType`): relayer khi gửi `claimMessage()` nên **khai báo AccessList tường minh** = {recipient, target contract (nếu có), KHÔNG bao gồm `per_chain_allocation`} — giúp Union-Find gom nhóm chính xác ngay từ đầu (xung đột tĩnh), giảm tỷ lệ Abort/chạy lại ở Round 1 thay vì để hệ thống tự suy luận xung đột động.

### 13.4 Kết quả: 2 trục chi phí tách biệt, mỗi trục scale theo đúng thứ nó phụ thuộc

| Pha | Chi phí phụ thuộc vào | Đặc tính song song hoá |
|---|---|---|
| **Attest** | **Số CHAIN nguồn** đang gửi đồng thời (tối đa vài chục-trăm theo quy mô doanh nghiệp), KHÔNG phụ thuộc số message | Chi phí BLS cố định/commit; các chain khác nhau attest độc lập (ô nhớ `per_chain_allocation[chainId]` khác nhau) → tự nhiên song song giữa các chain, chỉ serialize giữa các commit CÙNG 1 chain (hiếm, ~1/round) |
| **Claim** | **Số MESSAGE** (có thể rất lớn) | Rẻ (category "Regular Transfer"), Union-Find + AccessList (13.3.3) cho phép chạy thật sự song song trên nhiều lõi CPU — đây là phần chiếm khối lượng lớn nhất nên phải là phần rẻ nhất và song song tốt nhất |

Đây là lý do mô hình 2 pha **tốt hơn hẳn** so với `verifyAndExecuteBatch()` đơn lẻ (mục 13.2 cũ): tách đúng phần "đắt nhưng hiếm" (BLS, theo số chain) khỏi phần "rẻ nhưng nhiều" (payout, theo số message) — mỗi phần được tối ưu bằng đúng cơ chế phù hợp với nó (threadpool BLS riêng cho Attest, Union-Find/Block-STM cho Claim).

**Vai trò còn lại của `verifyAndExecute()`/`verifyAndExecuteBatch()` (mục 11.3):** giữ lại làm đường đơn giản cho trường hợp lẻ tẻ/khối lượng thấp (VD: 1 message hoàn tiền riêng lẻ, mục 2.4) — nơi chi phí thêm 1 giao dịch Attest riêng không đáng để tách 2 pha. **`attestCommit()` + `claimMessage()` là đường MẶC ĐỊNH cho khối lượng lớn.**

### 13.5 Giới hạn kích thước lô — vẫn cần, nay vì 2 lý do độc lập

Không thể để 1 commit chứa vô hạn message: mục 4 đã ghi nhận bằng chứng thật (`tps_e2e_analysis`) rằng 1 proposal quá lớn (50k TX/block) làm round DAG "đứng hình" ~3 giây. Với mô hình 2 pha, còn thêm lý do thứ 2: nếu quá nhiều `claimMessage()` cùng vào 1 block nhưng vô tình đụng nhau (VD: nhiều message cùng target 1 contract "hot"), Union-Find vẫn có thể gộp thành siêu nhóm lớn — đúng hiện tượng Hot-Contract mục 2.A của Block-STM. Giữ nguyên khuyến nghị `max_messages_per_relay_batch` (số `claimMessage()` relayer gửi trong 1 khoảng ngắn) = **2.000-4.000**, và áp dụng "Chunking" nếu 1 nhóm bất thường lớn (đã có sẵn cơ chế trong Block-STM mục 4 điểm 1, tái dùng không cần xây mới).

### 13.6 Tối ưu bổ sung (thứ yếu, cân nhắc nếu 13.2-13.5 chưa đủ)

- **Work-Stealing Scheduler** (đã có trong lộ trình nâng cấp của Block-STM, mục 4 điểm 2 của `block_stm_architecture_review.md`): nếu 1 worker bị nghẽn bởi 1 nhóm `claimMessage()` hot (VD: airdrop cross-chain vào nhiều địa chỉ nhưng vài địa chỉ trùng), worker rảnh có thể "trộm" việc — tái dùng cơ chế có sẵn, không cần xây mới cho riêng cross-chain.
- **Gộp chữ ký nhiều commit khác nhau thành 1 phép verify** (nâng cao, KHÔNG cần cho giai đoạn đầu): dùng đúng `VerifyAggregateSign(pubs [][]byte, sig, msgs [][]byte)` sẵn có ở `execution/pkg/bls/bls.go:42` để gộp nhiều `attestCommit()` từ nhiều chain khác nhau thành 1 phép pairing-check duy nhất. Chỉ cân nhắc khi có rất nhiều chain nguồn attest đồng thời và 13.3/13.4 chưa đủ — đánh dấu tối ưu **P4+**.
- **Hạ tầng riêng cho Reserve**: vì mục 13.1/13.4 xác định Reserve chịu tải Attest của TOÀN mạng, nên cấp phần cứng validator Reserve nhiều lõi CPU hơn mức trung bình 1 private chain (threadpool BLS riêng + worker pool Claim riêng chạy song song thật) — khuyến nghị vận hành, không đổi kiến trúc.

### 13.7 Khi nào cần tính đến việc chia nhỏ (shard) chính Reserve? (chỉ ghi nhận, KHÔNG triển khai bây giờ)

Nếu, sau khi đã áp dụng hết 13.2-13.6, nhu cầu thông lượng chuyển giá trị **toàn mạng** thực tế vượt quá trần đã tối ưu của 1 Reserve chain — đó là lúc mới cần cân nhắc "nhiều Reserve song song" (sharding tầng phát hành). Đây **không phải nhu cầu ở quy mô private-chain doanh nghiệp hiện tại** (mục tiêu ban đầu là chục-trăm chain, không phải hàng nghìn) — cố làm trước khi có nhu cầu thật là vi phạm nguyên tắc chống over-engineering đã ghi trong `AGENTS.md` của dự án. Chỉ ghi nhận ở đây như 1 điểm cần theo dõi qua dashboard thông lượng (mục 6/11.5), không đưa vào lộ trình P0-P8 hiện tại.

---

## 14. Kế hoạch chia task cụ thể cho dev — mỗi task kèm bài test bắt buộc (Definition of Done)

Chia nhỏ lộ trình P0-P8 (mục 8) thành các task cụ thể, giao được cho từng dev/cặp dev. **Nguyên tắc bắt buộc: 1 task chỉ được coi là "Done" khi bài test tương ứng PASS — không có khái niệm "code xong, test sau".** Bài test tham chiếu đúng các kịch bản đã có ở mục 10 và các giai đoạn T0-T5 ở mục 12, không phát minh quy trình test mới ngoài đó.

### P0 — Đặc tả kỹ thuật (không code chạy được, nhưng có deliverable kiểm tra được)

| Task | Nội dung | Deliverable | Bài test bắt buộc (DoD) |
|---|---|---|---|
| P0.1 | Chuyển schema mục 2.1/11.2/11.6 (hiện viết dạng pseudo-code) sang định nghĩa thật: Rust struct (`ChainRegistry`, `GlobalSupplyLedger`) cho Root Anchor state, Go struct cho execution-layer types tương ứng | File `.rs`/`.go` định nghĩa struct + (de)serialize | Unit test round-trip serialize/deserialize cho mọi struct; **property-based test** (fuzz ngẫu nhiên nhiều phép cộng/trừ `per_chain_allocation`): bất biến `Σ per_chain_allocation == genesis_total_supply` KHÔNG BAO GIỜ bị vi phạm sau N phép biến đổi ngẫu nhiên |
| P0.2 | Cơ chế quản trị on-chain: `GovernanceProposal` lifecycle (propose → vote → ≥2/3 chain active → delay 72h → executed) | Module quản trị (Rust, chạy trên Root Anchor) | Unit test: (a) propose rồi vote đủ ≥2/3 số chain (không phải stake) → chuyển trạng thái đúng; (b) vote chưa đủ ngưỡng → không executable; (c) đủ ngưỡng nhưng CHƯA hết 72h → gọi execute() phải revert; (d) qua 72h → execute() thành công đúng 1 lần, gọi lại lần 2 phải revert (idempotent) |
| P0.3 | `PopVerify` khi đăng ký `ValidatorEntry` (mục 1.3 #4) | Hàm verify PoP + tích hợp vào luồng đăng ký committee | Unit test với test vector chuẩn BLS PoP (IETF); test **rogue-key attack**: 2 khoá công khai được chọn phụ thuộc lẫn nhau (không có PoP hợp lệ tương ứng) phải bị từ chối đăng ký |

### P1 — Triển khai Root Anchor/Reserve Chain

| Task | Nội dung | Bài test bắt buộc (DoD) |
|---|---|---|
| P1.1 | Deploy 1 network Metanode mới làm Root Anchor (dùng nguyên `deploy/ansible`/`deploy/systemd`), genesis riêng | Health check tương đương CI hiện có của private chain: node lên, sản xuất block liên tục N phút không lỗi, `deploy/ansible/monitors` báo xanh |
| P1.2 | Tích hợp ≥4 chain sáng lập vào committee ban đầu (mục 1.3 #5), tính `quorum_threshold` theo stake | Test: `quorum_threshold` tính đúng theo tổng stake 4 chain; test giả lập 1/4 chain offline → Root Anchor vẫn đạt quorum nếu còn ≥2f+1 (tái dùng test pattern có sẵn của `meta-consensus/core` cho BFT threshold, không viết lại từ đầu) |

### P2 — `GatewayPrecompile` (khối lượng công việc lớn nhất, chia nhỏ theo từng hàm ở mục 11.3)

| Task | Hàm/nội dung | Bài test bắt buộc (DoD) |
|---|---|---|
| P2.1 | `outbound()` | Burn đúng số dư qua `ProcessNativeMintBurn(1)`; `messageId` = đúng tx hash; `tip` bị khoá đúng số; định tuyến đúng nhánh theo `assetId` (0 = native, khác 0 = `AssetRegistry`) |
| P2.2 | `attestCommit()` | Verify BLS đúng/sai (test vector cố định, mục 12 T0); **test kịch bản 10.7** (chặn tấn công): `aggregateAmount` vượt `per_chain_allocation[sourceChainId]` phải bị từ chối, KHÔNG cập nhật ledger, event `AllocationRejected` bắn đúng; test `epoch` không khớp `ChainRegistry` hiện tại → fail-closed (không dùng epoch cũ/tương lai) |
| P2.3 | `claimMessage()` | Merkle proof đúng/sai; **double-claim cùng `messageId` bị chặn** (idempotent); `getOriginalSender()` trả đúng theo context Gateway set, KHÔNG phải `msg.sender`; **test bắt buộc riêng cho mục 2.6.4 điểm 2**: gọi trực tiếp hàm nội bộ (giả lập, không qua Gateway thật) để set context giả — PHẢI bị chặn bởi `isCalledByGateway()` |
| P2.4 | Đường hoàn tiền (mục 2.4) | **Test kịch bản 10.3 end-to-end** (revert ở đích → hoàn tiền ở nguồn); **test double-mint qua refund bị chặn** (mục 2.4 điểm 3): gửi cả message gốc "SUCCESS" lẫn "FAILED" giả cho cùng 1 `messageId` — chỉ 1 trong 2 được xử lý, lần thứ 2 phải revert vì trạng thái không còn `PENDING` |
| P2.5 | `hop_count` enforcement (mục 2.6.2) | **Test kịch bản 10.5**: `hop_count = 6` được chấp nhận, `hop_count = 7` bị từ chối cứng |
| P2.6 | Gas cap cho `CONTRACT_CALL` inbound (mục 2.6.5) | Payload tốn gas vượt mức bị cap đúng (không chạy "free"); phần gas dư được hoàn lại đúng qua cơ chế refund (P2.4) |
| P2.7 | `verifyAndExecute()` (đường đơn giản dự phòng) | Test tương đương P2.2+P2.3 gộp cho 1 message đơn lẻ, atomic trong 1 giao dịch |
| P2.8 | `claimDeadChainBalance()` | Claim đúng theo `AccountLeaf` + Merkle proof hợp lệ; **double-claim bị chặn** qua `deadChainClaimed` mapping; claim với proof của chain CHƯA được declare-dead phải bị từ chối |

### P3 — Mở rộng epoch transition

| Task | Nội dung | Bài test bắt buộc (DoD) |
|---|---|---|
| P3.1 | Gửi `CommitteeUpdate` lên Root Anchor sau mỗi epoch transition (tái dùng `epoch_transition.rs`/`epoch_checkpoint.rs`) | Test tích hợp: chạy 1 epoch transition thật trên devnet → xác nhận đúng 1 tx `CommitteeUpdate` xuất hiện trên Root Anchor, `committee`/`epoch` khớp dữ liệu thật của chain |
| P3.2 | `StateRootCheckpoint` account-level (mục 1.3 #2, không chỉ số tổng) | Test: dựng Merkle proof của 1 account cụ thể từ `state_root` đã anchor, verify đúng; test dữ liệu archival (`archival_endpoint`) đủ để dựng lại toàn bộ account-tree — **đây là điều kiện tiên quyết để P8 chạy được**, phải test độc lập trước khi ghép P8 |

### P4 — Relayer reference implementation

| Task | Nội dung | Bài test bắt buộc (DoD) |
|---|---|---|
| P4.1 | Relayer scan outbound message, gọi `attestCommit()`+`claimMessage()` | **Chạy tự động cả 8 kịch bản mục 10 trên devnet (= T1 mục 12)** — đây là gate chính thức để coi P4.1 hoàn thành, không phải review code |
| P4.2 | Relay tip claiming, "ai xong trước nhận" | Test 2 relayer cùng submit 1 `messageId` — chỉ 1 nhận tip, người thứ 2 không mất phí oan (giao dịch phải revert sớm/rẻ, không chạy hết logic rồi mới phát hiện trùng) |

### P5 — Security review (đã có ở mục 8, nhắc lại điều kiện gate)

| Task | Nội dung | Bài test bắt buộc (DoD) |
|---|---|---|
| P5.1 | Audit độc lập toàn bộ luồng verify (BLS, Merkle, replay, double-mint, origin-sender 2 chiều) | Không finding mức Cao/Nghiêm trọng còn mở; mọi finding phải có test case tương ứng bổ sung vào bộ test hiện có (không chỉ sửa code) trước khi đóng |

### P6 — `AssetRegistry` mở rộng

| Task | Nội dung | Bài test bắt buộc (DoD) |
|---|---|---|
| P6.1 | Đăng ký/quản trị `AssetEntry` (dùng chung cơ chế P0.2) | Test: đăng ký asset qua đúng luồng biểu quyết ≥2/3 + delay 72h; **test giả mạo bị chặn**: 1 chain tự khai "home_chain" cho asset nó không sở hữu phải bị từ chối (không có cơ chế tự-đăng-ký 1 chiều) |
| P6.2 | Lock/mint token qua `AssetRegistry`, tái dùng cơ chế verify mục 2.2 | Test end-to-end: lock token ở chain nhà → mint wrapped ở chain đích, số lượng khớp chính xác, không có sai lệch làm tròn |

### P7 — Dashboard giám sát

| Task | Nội dung | Bài test bắt buộc (DoD) |
|---|---|---|
| P7.1 | Metric `cross_chain_relay_latency_seconds` | Xác nhận số liệu hiển thị khớp với thời gian đo thủ công trong T2 (mục 12) |
| P7.2 | Cảnh báo tức thời khi `AllocationRejected` bắn (mục 11.5) | Test: kích hoạt lại đúng kịch bản 10.7 trên staging — cảnh báo phải tới trong vòng vài giây, không chờ chu kỳ đối chiếu định kỳ |
| P7.3 | Dashboard giám sát lệch tổng cung + data-availability window cho message `PENDING` | Test: cố tình tạo 1 message `PENDING` kéo dài quá ngưỡng pruning giả lập — dashboard phải cảnh báo trước khi dữ liệu bị prune mất |

### P8 — Chain-Death Recovery runbook

| Task | Nội dung | Bài test bắt buộc (DoD) |
|---|---|---|
| P8.1 | Viết + diễn tập runbook declare-dead + claim | **= T3(c) ở mục 12**: mô phỏng "chết hẳn" 1 chain testnet, chạy toàn bộ quy trình từ biểu quyết declare-dead → nộp Merkle proof → nhận lại coin, thành công end-to-end ít nhất 1 lần trên testnet thật trước khi coi task này Done — review tài liệu runbook suông KHÔNG đủ điều kiện Done |

### Nguyên tắc áp dụng chung cho toàn bộ bảng trên

- **Không merge code thiếu test tương ứng** — task chưa có bài test PASS thì coi như chưa xong, kể cả khi logic "nhìn có vẻ đúng" (đúng nguyên tắc đã nêu ở mục 5.3: không dựa vào "nhìn có vẻ đúng" cho code mới).
- Các task P2 nên làm theo đúng thứ tự bảng (P2.1 → P2.8) vì có phụ thuộc dữ liệu lẫn nhau (VD: P2.3 cần P2.2 xong để có `AttestedCommit` mà đọc).
- Task nào chạm tới rủi ro mức 🔴 trong ma trận mục 7 (P2.2, P2.3, P0.2, P6.1) nên ưu tiên pair-programming hoặc review kỹ hơn mức thông thường trước khi coi là Done, không chỉ dựa vào test tự động.

---

## 15. Ước tính thời gian hoàn thành (có hỗ trợ agent)

**Đây là ước tính lập kế hoạch, không phải cam kết** — giả định nhóm 2-4 dev, mỗi người làm cùng 1 agent hỗ trợ code. Điểm quan trọng nhất: agent chỉ rút ngắn được **phần kỹ thuật nội bộ**, không rút ngắn được phần bị giới hạn bởi lịch của bên ngoài đội dev.

### 15.1 Phần agent hỗ trợ nhanh được (viết code + test theo đúng spec mục 11/14)

| Nhóm task (mục 14) | Ước tính |
|---|---|
| P0 (schema, quản trị on-chain, PopVerify) | ~1-1.5 tuần (song song 3 task) |
| P1 (deploy Root Anchor — phần kỹ thuật) | ~1 tuần |
| P2 (`GatewayPrecompile`, 8 task) | ~3-4 tuần (P2.2/P2.3 mức 🔴 cần thêm thời gian review, không chỉ code) |
| P3 (mở rộng epoch transition) | ~1.5-2 tuần |
| P4 (Relayer + chạy T1) | ~1.5-2 tuần |
| P6/P7 (AssetRegistry + Dashboard, song song được) | ~1-1.5 tuần |

→ **Tổng phần kỹ thuật nội bộ (P0-P4, P6-P7, T0-T1): ~6-9 tuần.** Nhanh được vì phần lớn công sức "thiết kế" đã dồn vào tài liệu này (mục 11/14) — agent chỉ cần chuyển spec thành code+test, không phải tự thiết kế lại.

### 15.2 Phần KHÔNG agent nào rút ngắn được (giới hạn bởi bên ngoài đội dev)

| Giai đoạn | Lý do không rút ngắn được | Ước tính lịch |
|---|---|---|
| P1.2 — thuyết phục ≥4 chain sáng lập góp validator | Đàm phán kinh doanh, không phải việc kỹ thuật | Vài ngày (nội bộ) đến vài tháng (đối tác ngoài) — **biến số lớn nhất** |
| T2 — Testnet nhiều chain, đo số liệu thật | Cần hạ tầng chạy đủ lâu mới có số liệu tin cậy | 1-2 tuần |
| T3 — Kiểm thử đối kháng + diễn tập Chain-Death Recovery | Cần chạy thật nhiều lần, có thể phải lặp lại nếu phát hiện lỗ hổng | 1-1.5 tuần |
| **P5 — Security review độc lập** | Audit bên thứ 3 theo lịch riêng của họ, không nhanh lên vì code viết nhanh | **4-8 tuần** |
| **T4 — Rollout mainnet 3 giai đoạn** | Cố ý có thời gian quan sát thật (mục 12: giai đoạn 1 vài tuần, giai đoạn 2 giữ hạn mức 1-2 tháng) — an toàn có chủ đích, không phải vấn đề tốc độ | **2-4+ tháng** |

### 15.3 Tổng thời gian dự kiến

- **Engineering xong + qua testnet, sẵn sàng vào audit:** ~2-3 tháng.
- **Mainnet chạy đầy đủ, gỡ hết hạn mức (hết T4 giai đoạn 3):** ~5-8 tháng — phần lớn nằm ở audit ngoài + quan sát production theo giai đoạn, không phải thời gian code.

**3 biến số cần xác nhận trước khi coi số này là kế hoạch thật:** (1) 4 chain sáng lập là nội bộ hay đối tác ngoài; (2) có thuê audit ngoài ngay hay review nội bộ trước; (3) mức độ thận trọng muốn giữ ở T4 (rút ngắn thời gian quan sát = tăng rủi ro thật, không phải đánh đổi miễn phí).
