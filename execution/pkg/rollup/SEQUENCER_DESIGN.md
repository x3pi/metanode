# Thiết kế Kiến trúc BLS Cluster & Giao tiếp Cross-Cluster (Thay thế Parent Chain Execution)

> **Cập nhật mới:** Parent Chain hiện tại KHÔNG CÒN xử lý logic smart contract hay chuyển tiền. Parent Chain chỉ đóng vai trò là **Registry (Đăng ký tài khoản, phân bổ BLS)** và **Relay/Router (Luân chuyển tin nhắn Cross-Cluster)**. Mọi logic state (balance, contract) được xử lý hoàn toàn nội bộ tại các Cụm BLS (BLS Clusters).

> ⚠️ **CẢNH BÁO KIẾN TRÚC QUAN TRỌNG (đọc trước khi triển khai):** Bài toán "nhiều cụm độc lập giữ state riêng, cần chuyển giá trị/gọi contract an toàn giữa các cụm" **đã tồn tại và đã được audit nhiều vòng** trong chính repo này, tại `execution/pkg/cross_chain/gateway.go` (`GatewayEngine`). Hệ thống đó đã có: `ChainRegistry` (đăng ký chain qua stake), `PerChainAllocation` (bất biến tổng cung — `ErrInvariantViolation`), `QuorumCert` + Merkle-proof cho việc claim message (`ClaimMessage`), hard-cap `FundedAmount`/`ClaimedAmount`, `SecurityBond`+`SlashOnEquivocation`, `DeadChains`/`DeclareChainDeadWithCert`+`ClaimDeadChainBalance`, `Refund`/`FinalizeFailedAfterExecutionRevert`, `TimeoutTimestamp` trên message. Bản thiết kế "Cross-Cluster Message (CCM)" ở mục 3 (bản trước) tự viết lại y hệt bài toán này nhưng **thiếu gần hết các cơ chế trên** — mỗi phần thiếu tương ứng với một lỗi logic/bảo mật cụ thể được liệt kê ở mục 8. Khuyến nghị: **coi mỗi BLS Cluster là một `chainID` đăng ký trong `ChainRegistry`, và tái dùng `GatewayEngine` làm tầng chuyển giá trị/message giữa các cụm**, thay vì xây `CrossClusterMessage`/`Outbox` riêng.

---

## 1. Tổng quan Kiến trúc

**Mục tiêu cốt lõi:**
- **Thực thi phân tán (Sharded Execution):** Mỗi Cụm BLS (Cluster) hoạt động như một shard độc lập, quản lý state, balance và smart contract của tập người dùng thuộc Cluster đó.
- **Parent Chain làm mỏ neo (Anchor):** Parent Chain chỉ quản lý danh bạ tài khoản (Account → Cluster) và xác thực danh tính các Cluster, hoàn toàn giải phóng khỏi việc xử lý giao dịch. **Về bản chất Parent Chain ở đây chính là vai trò Root Anchor trong `pkg/cross_chain`** — cần chốt xem đây là cùng một chain hay hai chain khác nhau (câu hỏi mở, xem mục 10.1 Q1).
- **Cross-Cluster Interoperability:** Chuyển tiền và gọi smart contract giữa 2 Cụm BLS khác nhau, dùng lại cơ chế `CrossChainMessage` + `QuorumCert` đã có, không phát minh giao thức mới.

---

## 2. Phân chia Vai trò

### 2.1. Parent Chain / Root Anchor (Coordination Layer)
- **Đăng ký tài khoản (Account Registry):** Giữ nguyên luồng đăng ký tài khoản với BLS như chain cũ. Map `User_Address -> Cluster_ID`. **(Đây là phần MỚI, chưa có sẵn trong `pkg/cross_chain` — chi tiết mục 5.)**
- **Đăng ký & xác thực danh tính Cluster:** Mỗi Cluster đăng ký như một `chainID` qua `GatewayEngine.RegisterChainViaStake` (đã có sẵn) — tự động có `NodeBlsPublicKey`/committee, `SecurityBond`, và tham gia được `PerChainAllocation`.
- **Relay/Attest tin nhắn:** Dùng nguyên cơ chế checkpoint + `QuorumCert` đã có (`SubmitCheckpoint`, `AttestCommit`, `BatchOutboundCommit`) — **không dùng chữ ký đơn lẻ của 1 node** cho việc di chuyển giá trị (xem mục 8, vấn đề #1).
- **KHÔNG LƯU STATE ứng dụng:** Không lưu balance/contract-state của người dùng cuối. Nếu đóng vai trò relay/hub cho route "qua Root Anchor" (mục 3.1 bước 2), Root Anchor có thể giữ một bản cache `AttestedCommits` để tiện chuyển tiếp cho Cluster đích — nhưng đây vẫn là sổ cái **kế toán liên-cụm** (protocol ledger), không phải state ứng dụng, và **không phải bản duy nhất/quyền uy nhất**: mỗi Cluster vẫn tự giữ và tự verify lại bản của mình trước khi tin (xem mục 3.2, mục 7 để tránh hiểu nhầm đây là 1 bảng dùng chung toàn hệ thống).

### 2.2. BLS Cluster (Execution Layer)
- **Quản lý State Nội bộ:** Mỗi Cluster sở hữu database riêng (LevelDB) lưu trữ balance, nonce, và state của smart contract.
- **Custody Private Key (PKS):** Cluster giữ device key của người dùng để ký hộ (không đổi, xem bản thiết kế `cmd/rpc` trước).
- **Thực thi Giao dịch Nội bộ (Intra-Cluster):** Giao dịch giữa User A và User B (cùng Cluster) được xử lý ngay lập tức, cập nhật state nội bộ mà không cần gửi lên Parent Chain.
- **Xử lý Giao dịch Liên cụm (Cross-Cluster):** Gọi `GatewayEngine.Outbound(...)` của chính Cluster mình (mỗi Cluster chạy 1 instance `GatewayEngine` với `LocalChainID = cluster_id`) khi đích đến nằm ở Cluster khác — không tự đóng gói/tự ký CCM thủ công.

---

## 3. Cơ chế Chuyển giá trị/Gọi Contract Cross-Cluster (tái dùng `GatewayEngine`)

Khi User A (Cluster 1) muốn chuyển tiền hoặc gọi Contract cho User/Contract B (Cluster 2).

### 3.1. Luồng hoạt động (Workflow)

1. **Khởi tạo tại Cluster 1 (Nguồn):**
   - User A gửi yêu cầu tới Cluster 1's `AccountHandler`.
   - Cluster 1 gọi `GatewayEngine.Outbound(sender=A, params{DestChainID: 2, Target: B, Value, Payload, GasFee, Tip}, txHash)` — hàm này **đã tự** gán `MessageID`, `Sequence`, set `MessageStatusPending`, và đưa vào `PendingOutboundMessages[2]`. **Không tự trừ tiền cục bộ trước** rồi mới build message riêng — việc khoá state phải nằm trong cùng logic với `Outbound` (xem vấn đề #2, mục 8) để tránh trừ tiền xong nhưng message không bao giờ được ghi nhận nếu crash giữa chừng.
   - Cluster 1 **không tự ký CCM bằng khoá node đơn lẻ**. Giá trị chỉ thực sự di chuyển được sau khi message nằm trong một **checkpoint đã được cả committee của Cluster 1 attest** (`QuorumCert`), theo đúng cách `AttestCommit`/`SubmitCheckpoint` đang hoạt động.

2. **Luân chuyển & Attest (qua Root Anchor):**
   - Batch các outbound message định kỳ: `BatchOutboundCommit(destChainID=2, epoch)` → sinh `commitRoot` + Merkle tree.
   - Root Anchor nhận `QuorumCert` (đa chữ ký của committee Cluster 1, không phải 1 chữ ký đơn) xác nhận `commitRoot` này là thật → lưu vào `AttestedCommits`, đồng thời hard-cap `FundedAmount` cho commit đó.
   - Cluster 2 lấy `commitRoot` + Merkle proof của message của mình từ Root Anchor (hoặc P2P trực tiếp, tự verify lại `QuorumCert` — như "Cách 2" ở bản trước, vẫn hợp lệ vì không phụ thuộc phải hỏi Root Anchor real-time).

3. **Thực thi đích (Tại Cluster 2 - Đích):**
   - Cluster 2 gọi `GatewayEngine.ClaimMessage(message, proof, commitRoot, relayer, blockTime)`. Hàm này đã tự:
     - Từ chối claim trùng (`currentStatus != MessageStatusPending` → lỗi `ErrAlreadyClaimed`) — **đây chính là chống-replay bắt buộc**, không phải tính năng tuỳ chọn như bản CCM cũ.
     - Verify Merkle proof + domain-separated leaf hash.
     - Kiểm tra timeout (`TimeoutTimestamp`) — tự động set `MessageStatusFailedTimeout` nếu quá hạn.
     - Kiểm tra đúng `DestChainID == LocalChainID` — chặn 1 message bị claim nhầm chain.
     - Hard-cap `ClaimedAmount <= FundedAmount` — chặn Cluster 2 (hoặc relayer) claim vượt số đã thực sự attest.
   - `ClaimMessage` set `MessageStatus[messageID] = Success` **trên chính engine cục bộ của Cluster 2** (mỗi `GatewayEngine` là 1 instance riêng — Cluster 1 và Cluster 2 KHÔNG chia sẻ chung 1 bảng `MessageStatus`, xem lưu ý ở mục 3.2).
   - **Bước MỚI cần thêm cho model "Account theo user" (chưa có sẵn trong `GatewayEngine`, vì engine chỉ làm việc ở mức `chainID`, không biết B là ai):** ngay sau khi `ClaimMessage` trả `Success`, Cluster 2's `AccountHandler` kiểm tra local: **`B` có phải account hợp lệ đang được chính Cluster 2 quản lý không** (tra `Account Registry` cục bộ, mục 5). Nếu không hợp lệ:
     1. Gọi **ngay lập tức** `FinalizeFailedAfterExecutionRevert(message, commitRoot, relayer)` — hàm này **chỉ chấp nhận gọi khi status hiện tại đúng là `Success` vừa được `ClaimMessage` set** (guard chặn gọi trễ/gọi lại) — nó tự đảo ngược `ClaimedAmount`/`PerChainAllocation` cục bộ và set `MessageStatus = Failed`.
     2. Committee của Cluster 2 gom chữ ký thất bại qua `AddPendingMessageFailureAttestationShare(key, share)` cho đến khi đủ ngưỡng quorum, rồi tổng hợp thành `destFailureCert` (`QuorumCert`).
     3. Gửi `destFailureCert` + proof + `commitRoot` về Cluster 1.

### 3.2. Đóng gói kết quả về Cluster nguồn — KHÔNG có bảng `MessageStatus` dùng chung

⚠️ Sửa một hiểu lầm quan trọng ở bản trước: mỗi `GatewayEngine` là **một instance riêng cho từng chain**, nên `MessageStatus` của Cluster 1 (nguồn) và của Cluster 2 (đích) là **hai bảng độc lập, không tự đồng bộ cho nhau**. Cluster 1 gọi `GetMessageStatus(messageID)` trên chính engine của mình sẽ **mãi mãi thấy `Pending`** cho tới khi chính Cluster 1 tự cập nhật — không có cơ chế "tự động" nào làm việc đó thay nó. Do đó bắt buộc phải có bước tường minh sau, đúng như cách hệ thống hiện có xử lý (không tự chế ACK/NACK riêng, chỉ nối đúng các hàm đã có):

- **Trường hợp thất bại (đích revert, ví dụ account không hợp lệ ở mục 3.1 bước 3):** Cluster 1 nhận `destFailureCert` (mục 3.1), gọi `Refund(message, messageProof, commitRoot, destFailureCert)` **trên chính engine của Cluster 1** (hàm này bắt buộc `message.SourceChainID == LocalChainID` — chỉ gọi được ở đúng chain nguồn). `Refund` tự kiểm tra `MessageStatus` hiện tại phải là `Pending` (chưa từng xử lý) trước khi mint lại cho User A — đây chính là **idempotency guard bắt buộc, chống refund 2 lần** nếu `destFailureCert` bị gửi lặp.
- **Trường hợp thành công:** cơ chế song song `AddPendingMessageSuccessAttestationShare`/`GetPendingMessageSuccessAttestationShares` tồn tại đúng để committee Cluster 2 chứng thực "đã xử lý thành công", cho Cluster 1 (hoặc Reserve, trong định tuyến 2-hop) đóng trạng thái cục bộ của mình một cách có xác thực (dọn `Pending`, giải phóng `Tip` đã khoá nếu có) — **không suy luận "không thấy Failed thì coi như thành công"**, vì im lặng có thể chỉ là do mất kết nối tạm thời (mục 6.1), không phải bằng chứng thành công.

→ Nói ngắn gọn: **cả 2 chiều (thành công/thất bại) đều cần một chứng thực (`QuorumCert`) đi kèm gửi ngược về nguồn**, không có chiều nào được phép suy luận từ sự im lặng.

---

## 4. Bảo mật kinh tế cho Cluster (mới — bắt buộc, không phải tuỳ chọn)

Vấn đề nghiêm trọng nhất của bản thiết kế CCM cũ: **không có gì ngăn một Cluster (do bug hoặc cố ý) phát ra vô hạn message "mint" giá trị ở Cluster khác** mà không thực sự khoá tài sản tương ứng ở Cluster nguồn — vì Parent Chain "KHÔNG LƯU STATE" nên không tự kiểm tra được. Đây là lý do bắt buộc phải dùng nguyên cụm cơ chế đã có:

1. **`PostSecurityBond`**: mỗi Cluster phải ký quỹ bond khi đăng ký (`RegisterChainViaStake`) — mất bond nếu gian lận.
2. **`SlashOnEquivocation`**: phát hiện Cluster ký 2 checkpoint mâu thuẫn (double-sign) → slash bond ngay.
3. **Hard-cap theo `FundedAmount`/`ClaimedAmount`**: một commit chỉ cho claim tối đa đúng số đã thực sự được attest, không thể "mint" vượt mức.
4. **`PerChainAllocation` + bất biến `Σ per_chain_allocation == genesis_total_supply`** (`ErrInvariantViolation`): mọi giao dịch cross-cluster phải giữ tổng cung không đổi — nếu một cluster tự ý cộng tiền không qua `ClaimMessage`, tổng sẽ lệch và bị phát hiện.
5. **Giới hạn tốc độ rút (velocity limit)** — `checkAndRecordVelocity`: đã có sẵn cửa sổ trượt 24h, mặc định chặn nếu 1 Cluster attest-outflow vượt quá **20% allocation tại thời điểm mở cửa sổ** trong 24h. Đây chính là cơ chế chống-spam/giới hạn thiệt hại nếu 1 Cluster bị compromise — không cần thiết kế rate-limit riêng cho CCM, chỉ cần xác nhận ngưỡng 20%/24h này có phù hợp với quy mô BLS Cluster hay cần điều chỉnh (mục 10).

→ Không thiết kế lại các cơ chế này cho BLS Cluster; **đăng ký mỗi Cluster như một `chainID` bình thường trong `GatewayEngine`** là đủ để thừa hưởng toàn bộ tầng bảo mật kinh tế này.

---

## 5. Đăng ký & Tra cứu Account (phần thực sự MỚI, GatewayEngine không có sẵn)

`GatewayEngine` chỉ biết đến `chainID`, không biết định danh user cụ thể nào thuộc cluster nào — đây là phần Parent Chain/Root Anchor cần bổ sung riêng cho use-case này:

| Bảng | Key | Value | Vai trò |
|---|---|---|---|
| Account Registry | `user_address` | `cluster_id` | Định tuyến `Target` của message đến đúng account; Cluster đích dùng để kiểm tra hợp lệ trước khi credit (mục 3.1 bước 3) |

**Vấn đề cần xử lý (đã áp dụng ở mục 3.1):**
- **Đăng ký 1 lần, chống ghi đè tuỳ ý:** chỉ chấp nhận đăng ký lần đầu, hoặc yêu cầu chữ ký của chính Cluster đang giữ account hiện tại nếu muốn "chuyển nhượng" account sang cluster khác — tránh một report cũ/replay vô tình đổi chủ account.
- **Cluster đích luôn tự kiểm tra lại** account có thuộc mình không trước khi credit (không tin tưởng mù quáng vào `Target` trong message), vì Account Registry ở Parent Chain có thể chưa đồng bộ kịp lúc User A gửi giao dịch.

**Ràng buộc vốn khởi tạo khi 1 Cluster mới đăng ký (đã có sẵn trong `RegisterChainViaStake`, cần đưa vào kế hoạch vận hành):** stake đăng ký của Cluster mới được cấp bằng `TransferAllocation` **rút từ pool của Reserve chain** (không phải mint tự do — điểm này từng bị phát hiện là lỗ hổng mint-không-giới-hạn và đã fix bằng cách bắt buộc rút từ Reserve). Hệ quả: **nếu Reserve không còn đủ allocation, đăng ký Cluster mới sẽ thất bại.** Cần có kế hoạch cấp vốn ban đầu cho Reserve tương ứng với số lượng Cluster dự kiến ra mắt, đây là một precondition vận hành, không phải lỗi logic — liệt kê trong checklist production (mục 10).

---

## 6. Xử lý Mất kết nối & Cluster chết vĩnh viễn

Bản trước chỉ xử lý mất kết nối *tạm thời*. Cần tách rõ 2 tình huống khác nhau vì cách xử lý và rủi ro khác hẳn nhau:

### 6.1. Mất kết nối tạm thời
- **Giao dịch Nội bộ:** Vẫn hoạt động 100% bình thường kể cả khi mất mạng với Parent Chain, vì state nằm hoàn toàn tại Cluster.
- **Giao dịch Cross-Cluster:** Nếu gửi checkpoint/claim thất bại do mất mạng, Cluster giữ nguyên item trong `PendingOutboundMessages`/hàng đợi gửi checkpoint cục bộ (LevelDB), retry theo `MessageID` cố định — **không build lại message mới với ID khác cho cùng 1 yêu cầu**, nếu không có thể tạo 2 message hợp lệ cho cùng 1 ý định chi tiêu.

### 6.2. Cluster đích không phản hồi vĩnh viễn (chết hẳn, không phải mất mạng tạm thời)
Đây là lỗ hổng bị bỏ sót hoàn toàn ở bản trước: nếu Cluster 1 tự ý "timeout rồi unlock/refund cho User A" theo phán đoán riêng của mình, trong khi Cluster 2 thực ra chỉ chậm chứ không chết hẳn và **sau đó vẫn credit cho User B** → giá trị bị tạo ra 2 lần từ 1 lần khoá (double-spend kinh điển ở các cầu nối liên chuỗi). Vì vậy:
- **Không cho một Cluster tự ý unlock theo timeout riêng của nó.** Dùng `TimeoutTimestamp` đã có trên `CrossChainMessage` — khi hết hạn, `ClaimMessage` tự chuyển `MessageStatusFailedTimeout` một cách **thống nhất trên Root Anchor** (nguồn sự thật duy nhất), rồi Cluster 1 mới được refund dựa trên trạng thái đó.
- **Nếu Cluster chết hẳn (không còn khả năng claim vĩnh viễn):** dùng `DeclareChainDeadWithCert` (cần `QuorumCert`, không phải quyết định đơn phương của Cluster 1) + `ClaimDeadChainBalance` để user rút lại tài sản một cách có kiểm soát, thay vì "Pending/Locked" vô thời hạn như bản thiết kế cũ.

---

## 7. Tóm tắt Cấu trúc Data Model

### Tại Parent Chain / Root Anchor
| Bảng / Struct | Nguồn | Vai trò |
|---|---|---|
| Account Registry | Mới (mục 5) | `user_address -> cluster_id` |
| `ChainRegistry` | Có sẵn (`gateway.go`) | Danh tính + committee + bond của từng Cluster (chạy trên instance `GatewayEngine` của chính Root Anchor) |
| `SecurityBondLedger` | Có sẵn | Bond + slash cho hành vi gian lận/double-sign |
| `DeadChains` | Có sẵn | Cluster đã tuyên bố chết, chặn outflow mới |

### Tại mỗi BLS Cluster (Local DB — MỖI Cluster tự chạy 1 instance `GatewayEngine` riêng)
| Bảng / Struct | Key | Value | Vai trò |
|---|---|---|---|
| Account State | `user_address` | `balance`, `nonce`, `contract_state` | State thực sự |
| `GatewayEngine` instance | — | `LocalChainID = cluster_id` | Client cục bộ gọi `Outbound`/`ClaimMessage`/`Refund` |
| `PerChainAllocation` (bản cục bộ) | `chain_id` | `allocation` | Bất biến tổng cung — **cộng dồn qua các engine khác nhau, không phải 1 bảng dùng chung** (mục 3.2) |
| `AttestedCommits` (bản cục bộ) | `sourceChain:commitRoot:assetId` | `FundedAmount`, `ClaimedAmount` | Hard-cap claim, chỉ tồn tại trên engine đã nhận attest cho commit đó |
| `MessageStatus` (bản cục bộ) | `message_id` | `Pending/Success/Failed/FailedTimeout` | **Local cho từng engine** — Cluster nguồn và Cluster đích có 2 bản ghi độc lập cho cùng 1 `message_id`, đồng bộ qua `destFailureCert`/success-cert (mục 3.2), không tự động giống nhau |
| Retry Queue (checkpoint gửi đi) | `message_id` | `payload`, `status` | Chỉ cho mất-kết-nối tạm thời (mục 6.1) |

> Lưu ý: bảng trên giả định mỗi Cluster (kể cả Root Anchor) tự chạy một instance `GatewayEngine` độc lập — cần đội cross-chain hiện tại xác nhận đúng topology triển khai thật (mục 10, mục Q1).

---

## 8. Danh sách vấn đề logic/bảo mật đã phát hiện & cách xử lý trong bản này

| # | Vấn đề ở bản CCM cũ | Rủi ro cụ thể | Xử lý trong bản này |
|---|---|---|---|
| 1 | Cross-cluster message chỉ ký bằng 1 `NodeBlsPrivateKey` của Cluster nguồn | 1 khoá node bị lộ → giả mạo message "mint" giá trị ở cluster khác vô hạn | Bắt buộc `QuorumCert` (đa chữ ký committee) qua checkpoint/`AttestCommit`, không chấp nhận chữ ký đơn lẻ cho giá trị (mục 3.1-3.2, mục 4) |
| 2 | Trừ tiền cục bộ trước, tạo/gửi message sau, là 2 bước tách rời | Crash giữa chừng → tiền mất, message không tồn tại | Gộp vào `Outbound(...)` cùng 1 lần ghi (mục 3.1 bước 1) |
| 3 | Đánh dấu chống-replay ở đích là "tuỳ chọn" | Gửi lại message do retry → credit 2 lần | Bắt buộc, dựa trên `MessageStatus` guard có sẵn trong `ClaimMessage` (mục 3.1 bước 3) |
| 4 | Chỉ có NACK khi thất bại, không có xác nhận khi thành công | Cluster nguồn không bao giờ biết chắc để dọn trạng thái "Pending/Locked" | Dùng `MessageStatus` (Pending/Success/Failed/FailedTimeout) làm nguồn sự thật duy nhất thay vì kênh ACK riêng (mục 3.2) |
| 5 | Không có cơ chế nào chặn 1 cluster "mint" giá trị không có thật ở cluster khác | Phá vỡ toàn bộ tính an toàn kinh tế của hệ thống | `SecurityBond` + `SlashOnEquivocation` + hard-cap `FundedAmount/ClaimedAmount` + bất biến `PerChainAllocation` (mục 4) |
| 6 | Không xử lý cluster đích chết vĩnh viễn — tài sản bị khoá vô thời hạn | Người dùng mất quyền truy cập tài sản không lý do | `TimeoutTimestamp` + `DeclareChainDeadWithCert` + `ClaimDeadChainBalance` (mục 6.2) |
| 7 | Cluster nguồn tự ý quyết định timeout rồi tự unlock | Double-spend nếu cluster đích thực ra chỉ chậm, không chết | Timeout được phân xử tập trung trên Root Anchor (nguồn sự thật duy nhất), không cho quyết định đơn phương (mục 6.2) |
| 8 | Không có phí/thưởng cho cluster đích khi phải thực thi hộ contract-call | Không có động lực kinh tế, dễ bị spam CCM miễn phí | Tái dùng field `GasFee`/`Tip` đã có sẵn trong `CrossChainMessage`/`OutboundParams` |
| 9 | Message chỉ có `DstCluster`, không xác minh `B` thật sự thuộc Cluster đó | Credit nhầm/"treo" tiền cho account không tồn tại ở cluster đích | Cluster đích tự kiểm tra Account Registry cục bộ trước khi credit; nếu sai, `Refund` như một revert bình thường (mục 3.1 bước 3, mục 5) |
| 10 | Account Registry cho phép ghi đè mapping tuỳ ý | Report cũ/replay có thể "cướp" account sang cluster khác | Chỉ chấp nhận đăng ký lần đầu hoặc có chữ ký của cluster hiện tại để chuyển nhượng (mục 5) |

---

## 10. Production Readiness Checklist

### 10.1. Quyết định còn mở — PHẢI chốt trước khi triển khai (không phải lỗi kỹ thuật, mà là quyết định thiếu sẽ chặn triển khai)

| # | Câu hỏi | Vì sao quan trọng |
|---|---|---|
| Q1 | "Parent Chain" ở tài liệu này có phải chính là Root Anchor đang chạy, hay là 1 chain hoàn toàn mới? | Quyết định có cần deploy thêm hạ tầng committee/attest mới hay dùng lại committee đã có (mục 1, mục 7 ghi chú) |
| Q2 | Ngưỡng `TimeoutTimestamp` mặc định cho message cross-cluster là bao lâu? | Quá ngắn → refund oan khi mạng chậm bình thường; quá dài → tài sản User A bị khoá lâu khi Cluster đích thực sự có vấn đề (mục 6.2) |
| Q3 | `QuorumThreshold` (số chữ ký committee tối thiểu) cho mỗi Cluster là bao nhiêu, và committee của 1 Cluster gồm bao nhiêu validator? | Ảnh hưởng trực tiếp mức độ chống giả mạo của `QuorumCert` (mục 4) — 1 Cluster chỉ có 1-2 node thì "committee" gần như vô nghĩa, cần tối thiểu bao nhiêu node/validator độc lập mới coi là an toàn |
| Q4 | Ngưỡng velocity limit mặc định (20%/24h, đã có sẵn trong code) có phù hợp quy mô BLS Cluster không, hay cần chỉnh riêng theo từng Cluster? | Cluster nhỏ có thể cần ngưỡng chặt hơn; Cluster lớn/nhiều giao dịch hợp lệ có thể bị chặn oan nếu để mặc định (mục 4 điểm 5) |
| Q5 | Kế hoạch cấp vốn Reserve ban đầu cho N Cluster dự kiến ra mắt là bao nhiêu? | `RegisterChainViaStake` thất bại nếu Reserve không đủ allocation (mục 5, ghi chú vốn khởi tạo) |
| Q6 | Mỗi Cluster (kể cả Root Anchor) có thực sự chạy 1 instance `GatewayEngine` riêng, hay có thiết kế tập trung hoá khác? | Ảnh hưởng trực tiếp cách đồng bộ `MessageStatus`/`PerChainAllocation` mô tả ở mục 3.2, mục 7 |

### 10.2. Vận hành (operational, cần có trước khi nhận traffic thật)

- **Quản lý khoá committee:** quy trình tạo/backup/rotate BLS key cho committee của từng Cluster — nếu khoá node/validator bị lộ, cần quy trình rotate không làm gián đoạn message đang `Pending`.
- **Giám sát bắt buộc:** cảnh báo khi (a) `MessageStatus` ở trạng thái `Pending` quá lâu (ngưỡng theo Q2) mà chưa có cert thành công/thất bại, (b) `SecurityBondLedger` của 1 Cluster giảm gần ngưỡng slash, (c) `DeadChains` có entry mới, (d) velocity limit bị chạm liên tục (dấu hiệu bất thường hoặc ngưỡng sai — Q4).
- **Backup & Disaster Recovery:** backup định kỳ LevelDB của từng Cluster (Account State + `AttestedCommits`/`MessageStatus` cục bộ) và state của Root Anchor (`ChainRegistry`/`SecurityBondLedger`/`DeadChains`); quy trình khôi phục 1 Cluster từ backup mà không làm lệch `PerChainAllocation` toàn hệ thống.
- **Runbook xử lý sự cố:** ít nhất 2 kịch bản — (1) 1 Cluster bị nghi compromise (khi nào trigger `SlashOnEquivocation`/`DeclareChainDeadWithCert` thủ công), (2) message kẹt `Pending` do mất cert cả 2 chiều (mục 3.2) cần người vận hành can thiệp thế nào.

### 10.3. Checklist bảo mật trước khi go-live (đối chiếu mục 8)

- [ ] #1–#10 ở mục 8 đã được review độc lập bởi người khác ngoài người viết thiết kế này (không tự ký-tự duyệt).
- [ ] Q1–Q6 ở mục 10.1 đã có quyết định bằng văn bản, không còn để ngỏ.
- [ ] Đã chạy thử ít nhất 1 kịch bản thất bại thật (Cluster đích revert do account không hợp lệ → refund) và 1 kịch bản Cluster chết (`DeclareChainDeadWithCert` → `ClaimDeadChainBalance`) trên môi trường staging, không chỉ đọc code.
- [ ] Đã xác nhận `Σ per_chain_allocation == genesis_total_supply` được kiểm tra tự động (không chỉ khi có lỗi) — ví dụ một job định kỳ so khớp, không chỉ dựa vào việc code tự throw `ErrInvariantViolation` khi có thao tác vi phạm.

---

## 11. Lộ trình triển khai (Next Steps)

1. **Chốt Q1–Q6 (mục 10.1)** trước khi viết code — đây là các quyết định kiến trúc/vận hành, không thể lùi lại sau.
2. **Refactor Parent Chain:** Gỡ bỏ các module xử lý State Machine (EVM/WASM, Balance) ở tầng ứng dụng, chỉ giữ Account Registry (mục 5) và **tái dùng nguyên `GatewayEngine`** làm tầng liên-cụm — không viết `CrossClusterMessage`/`Outbox` mới.
3. **Nâng cấp Cụm BLS:** Mỗi Cluster chạy 1 instance `GatewayEngine` với `LocalChainID = cluster_id`, đăng ký qua `RegisterChainViaStake`. Tích hợp `AccountHandler` nội bộ gọi `Outbound`/`ClaimMessage`/`Refund`/`FinalizeFailedAfterExecutionRevert` thay vì tự ký/tự verify CCM.
4. **Bổ sung Account Registry (mục 5):** Đây là phần thực sự cần code mới — mapping `user_address -> cluster_id`, kèm quy tắc chống ghi-đè và bước kiểm tra tại cluster đích trước khi credit (mục 3.1 bước 3, mục 8 #9-#10).
5. **Dựng hạ tầng vận hành (mục 10.2)** song song với bước 2-4, không để tới sau khi code xong mới làm.
6. **Chạy checklist 10.3** trước khi cho phép traffic thật (không chỉ testnet nội bộ).
