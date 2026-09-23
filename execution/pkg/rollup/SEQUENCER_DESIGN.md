# Thiết kế Kiến trúc BLS Node & Node Float Account (Cross-Node Value Transfer)

> **v2 — CHỐT HƯỚNG:** Từ bản này, cơ chế chuyển giá trị cross-node chính thức chuyển sang mô hình **Node Float Account** — Parent Chain giữ thật "quỹ liên-node" của từng node (không phải trần phân bổ + tiền cọc răn đe như bản trước). Toàn bộ nội dung liên quan tới `Outbound`/`BatchOutboundCommit`/`ClaimMessage`/`FundedAmount`-`ClaimedAmount`/velocity-limit-cho-mọi-giao-dịch/Withdrawal-Delay-72h-cho-toàn-bộ-giá-trị đã bị loại bỏ khỏi tài liệu vì không còn phù hợp. **`NodeFloatAccount` không có bước "nạp quỹ" rời rạc nào cả — nó là 1 bất biến tự động, luôn bằng đúng tổng số dư user của node đó (mục 3.2)** — pipeline Snapshot/Delay-72h chỉ còn giữ lại cho đúng 1 việc: chứng minh PHÂN BỔ cho từng user khi node chết (mục 6.3), không phải chứng minh tổng số. Giao dịch nội bộ cùng node **không đổi, vẫn tức thời**.

> ⚠️ **Nền tảng kỹ thuật vẫn là `execution/pkg/cross_chain/gateway.go`** (đăng ký chain qua stake, `SecurityBond`, `RecoveryCommittee`, `DeclareChainDeadWithCert`...) — chỉ riêng cơ chế **di chuyển giá trị giữa các node** được thiết kế lại thành ghi sổ trực tiếp (Float Account) thay vì mô hình mint-theo-trần-rồi-claim.

---

## 1. Tổng quan Kiến trúc

**Mục tiêu cốt lõi:**
- **Thực thi phân tán:** Mỗi Node (`cmd/rpc`) là 1 `chainID` độc lập (đã chốt — mục 2.4), tự quản state, balance, smart contract của user thuộc node đó.
- **Parent Chain = Root Anchor có sẵn** (đã chốt), đóng 2 vai trò: (1) sổ danh bạ (Account Registry, ChainRegistry), (2) **nơi giữ thật "quỹ liên-node"** của từng node (`NodeFloatAccount`) — khác bản trước ở chỗ đây là **tiền thật**, không phải trần phân bổ trừu tượng.
- **Cross-Node Interoperability:** Chuyển giá trị = ghi sổ chuyển khoản trực tiếp giữa 2 `NodeFloatAccount` trên Parent Chain — atomic, không cần dance "khoá batch → attest → claim" nhiều bước như bản trước.

---

## 2. Phân chia Vai trò

### 2.1. Parent Chain / Root Anchor
- **Account Registry:** `user_address -> chainID` (mục 5.1).
- **ChainRegistry:** danh tính + `NodeBlsPublicKey` từng node (có sẵn trong `GatewayEngine`).
- **`NodeFloatAccount` (MỚI, thay cho `PerChainAllocation`-làm-trần):** `chainID -> balance` — **tiền thật**, Parent Chain trực tiếp enforce không cho âm. Đây là state duy nhất Parent Chain giữ ngoài registry — vẫn **không giữ balance của từng user cuối** (state đó vẫn 100% ở local mỗi node).
- **`SecurityBondLedger` + `RecoveryCommittee`:** vẫn giữ nguyên vai trò, nhưng **phạm vi bảo vệ thu hẹp lại chỉ còn đăng ký chain + chống khai khống PHÂN BỔ khi node chết** (mục 4.1b).

### 2.2. BLS Node (Execution Layer)
- **State nội bộ:** LevelDB riêng — balance, nonce, contract state của user thuộc node.
- **Custody Private Key (PKS):** giữ nguyên như thiết kế `cmd/rpc` gốc.
- **Giao dịch nội bộ:** xử lý ngay lập tức, không đụng Parent Chain (mục 13.2 — không đổi).
- **Giao dịch cross-node:** gọi thẳng thao tác chuyển khoản trên `NodeFloatAccount` của mình (mục 3), không còn qua `Outbound`/`ClaimMessage` 3 bước.

### 2.3. Rủi ro Custody tập trung (PKS giữ Device Key)

Không đổi so với bản trước: Node giữ 100% device key để ký hộ — nếu bị hack, kẻ tấn công ký được giao dịch nội bộ giả mà không để lại bằng chứng phân biệt được với user thật. Không giải quyết triệt để bằng kỹ thuật được (đánh đổi cố hữu của mô hình ký hộ); giảm thiểu bằng: (1) ngưỡng rút + delay cho giao dịch lớn, (2) anomaly detection, (3) tuỳ chọn non-custodial cho tài khoản lớn — bắt buộc xây cả 3 làm baseline trước go-live (mục 9.1 Q9-phần-build). Mức rủi ro còn lại có chấp nhận được với quy mô tài sản thật hay không là quyết định threat-model của đội (Q9-phần-rủi-ro, còn mở).

### 2.4. 1 Node `cmd/rpc` = 1 `chainID` riêng (ĐÃ CHỐT — Q13)

Không đổi so với bản trước — vẫn là quyết định nền tảng. Mỗi node đăng ký `chainID` riêng qua `RegisterChainViaStake`, không nhóm nhiều node dưới 1 chain. Lý do: `GatewayEngine` (`ChainRegistry`, `SecurityBond`, `RecoveryCommittee`) hoạt động ở đúng granularity `chainID`, ngầm giả định đây là 1 đơn vị tin cậy duy nhất — nhóm nhiều node độc lập dưới 1 chain phá vỡ giả định này. Hệ quả: committee mỗi node = 1 (chính nó), không có redundancy signer thật.

---

## 3. Cơ chế Chuyển giá trị Cross-Node — Node Float Account

### 3.1. Vì sao chuyển từ mô hình cũ (trần phân bổ + bond) sang mô hình này

**Mô hình cũ (đã bỏ):** mỗi node có `PerChainAllocation` — một **trần phân bổ**, không phải tiền thật — cộng với `SecurityBond` — tiền cọc **tách biệt**, chỉ có tác dụng răn đe SAU KHI phát hiện gian lận. Mỗi giao dịch cross-node phải qua 3 bước (`Outbound` → `BatchOutboundCommit`+`QuorumCert` → `ClaimMessage` với hard-cap `FundedAmount`/`ClaimedAmount`), và khi node chết, tài sản treo cần cả 1 pipeline nặng (Snapshot định kỳ, Archival Service, Velocity Limit, Withdrawal Delay 72h, chống Data-Availability-Withholding) mới rút lại được.

**Mô hình mới:** mỗi node có 1 **Float Account** trên Parent Chain — **tiền thật**, Parent Chain tự enforce không cho âm. Chuyển giá trị cross-node = 1 lệnh ghi sổ trực tiếp, atomic — không cần "khoá rồi chờ bên kia claim".

⚠️ **Hiểu đúng — mô hình mới KHÔNG loại bỏ hoàn toàn lỗ hổng gốc, nó THU HẸP phạm vi:** Parent Chain "không lưu state ứng dụng" nên vẫn không thể tự verify 1 node có thật sự sở hữu giá trị nó khai báo hay không — lỗ hổng này bị đẩy lùi về đúng **1 điểm duy nhất: bước Nạp quỹ** (mục 3.2), thay vì tồn tại ở MỌI giao dịch cross-node như mô hình cũ. Đây là cải tiến thật (bề mặt tấn công thu nhỏ, tần suất thấp hơn, dễ giám sát tập trung hơn), không phải loại bỏ hoàn toàn.

### 3.2. `NodeFloatAccount` KHÔNG cần "bước nạp quỹ" — nó là 1 bất biến tự động, luôn bằng tổng số dư user

⚠️ **Sửa lại tư duy nền tảng so với 2 bản trước — cả "1 đường Deposit" lẫn "2 đường Deposit A/B" đều SAI khung:** cả 2 bản trước coi `NodeFloatAccount` là 1 **quỹ dự trữ rời rạc** mà node phải chủ động "nạp" vào — điều này ngầm giả định luôn có 1 khoảng chênh lệch giữa quỹ và số dư user thật (cần snapshot để chứng minh, cần bond để răn đe). **Đúng khung phải là:** `NodeFloatAccount[node]` **LUÔN BẰNG** tổng số dư của mọi user thuộc node đó — đây là 1 **bất biến tự động**, không phải 1 con số node "quyết định nạp bao nhiêu". Tiền chỉ có thể vào `NodeFloatAccount` qua đúng 2 cách, cả 2 đều **atomic với chính việc số dư 1 user cụ thể tăng lên** — không còn "tuyên bố tổng hợp cần tin cậy" nào nữa:

1. **User tự nạp tiền vào TÀI KHOẢN CỦA CHÍNH HỌ** (không phải "nạp cho node" chung chung): bất kỳ ví nào có số dư thật trên Parent Chain chuyển tiền vào — **trong CÙNG 1 giao dịch**, Parent Chain vừa ghi nhận user đó được cộng X, vừa cộng thật X vào `NodeFloatAccount[node]` của node đang quản lý user đó. Không cần snapshot, không cần bond — Parent Chain tự verify được ví nguồn có đủ tiền thật (như mọi transaction bình thường), không có gì để "khai khống" vì đây là hành động gắn với 1 user cụ thể, xác minh được trực tiếp.
2. **Nhận Transfer cross-node đến** (mục 3.3) — đã atomic sẵn: `NodeFloatAccount` bên gửi giảm, bên nhận tăng, đúng cùng lúc user gửi bị trừ / user nhận được cộng.

**Hệ quả — giải quyết luôn cả Q5 lẫn "bootstrap vốn ban đầu" mà không cần khái niệm nào mới:** 1 node mới tinh chưa có user nào thì `NodeFloatAccount = 0`, đúng bằng tổng số dư user (cũng = 0) — khi user đầu tiên nạp tiền vào tài khoản của họ (cách 1), `NodeFloatAccount` tự động tăng theo, không cần 1 bước "vốn khởi đầu cho node" tách biệt nào cả. **Không còn khái niệm "Bond-vs-Deposit" (mục 4.1 cũ đã bỏ), không còn khái niệm "giới hạn nạp quỹ mỗi chu kỳ"** — vì không còn hành động "nạp quỹ" rời rạc nào để giới hạn nữa.

**Điều vẫn CÒN, không biến mất — chỉ đổi mục đích (xem mục 6.3):** Parent Chain giờ biết chắc chắn **TỔNG** `NodeFloatAccount` của 1 node là thật (không thể khai khống, vì mỗi đồng đều gắn với 1 giao dịch xác minh được) — nhưng **KHÔNG biết tổng đó chia cho user nào bao nhiêu**, vì đó vẫn là state cục bộ chỉ node giữ. Đây chính là lý do Snapshot Pipeline (mục 6.3) vẫn cần, nhưng để chứng minh **phân bổ**, không phải để chứng minh **tổng số** như trước.

### 3.3. Chuyển giá trị cross-node (Transfer) — luồng thành công

1. User A (Node 1) gửi yêu cầu chuyển cho User B (Node 2).
2. ⚠️ **Sửa lại (mục 8 #11 — đã đổi bản chất):** vì `NodeFloatAccount` luôn ≥ balance của A (mục 3.5), **không còn kịch bản "Float không đủ" cần phân biệt với mất-kết-nối nữa** — bước này giờ chỉ còn thuần tuý là **kiểm tra số dư cục bộ của A** (bình thường như mọi hệ thống, không liên quan gì Parent Chain) trước khi trừ. Việc đọc-kiểm tra-rồi-ghi vẫn cần node tự serialize/khoá nội bộ (không xử lý song song nhiều outbound Transfer cùng lúc mà không khoá), để tránh 2 yêu cầu cùng lúc từ CÙNG 1 user A đọc thấy "đủ" rồi cùng trừ vượt quá số dư thật của A — đây là race-condition thông thường ở tầng LevelDB cục bộ, không còn liên quan gì đến "Float Account cấp node" nữa.
3. Qua được pre-flight check: Node 1 trừ balance cục bộ của A, đồng thời gửi 1 giao dịch **duy nhất, atomic** lên Parent Chain: `NodeFloatAccount[1] -= V`, `NodeFloatAccount[2] += V`, kèm metadata (`Target: B`, `MessageID` duy nhất, `Payload` nếu là contract-call). **Không cần batch-rồi-chờ-ký-committee** (committee = 1 nên chữ ký chỉ là chính Node 1 tự ký, gộp luôn vào transaction này) — đơn giản hơn hẳn 3 bước của mô hình cũ.
4. Ngay khi transaction confirm trên Parent Chain: **tiền đã thật sự nằm ở `NodeFloatAccount[2]`** — không còn khái niệm "Pending chờ claim" cho phần GIÁ TRỊ.
5. Node 2 theo dõi Parent Chain, thấy có credit mới addressed cho mình. ⚠️ **Chống xử lý trùng bắt buộc (mục 8 #13):** Node 2 phải tự kiểm tra `MessageID` này đã xử lý (credit hoặc refund) chưa trước khi làm bất cứ gì — mô hình cũ có `ErrAlreadyClaimed` tự động trong `ClaimMessage`, mô hình mới KHÔNG còn hàm trung tâm nào làm việc này hộ, node phải tự giữ 1 bảng "MessageID đã xử lý" cục bộ.
6. Node 2 kiểm tra local: `B` có phải account hợp lệ đang được chính mình quản lý không (không đổi so với bản trước).
7. Nếu hợp lệ: Node 2 **trước tiên** gửi 1 transaction nhỏ lên Parent Chain đánh dấu `MessageID` này = `Claimed` (mục 3.6 — cần cho cơ chế Reclaim-theo-timeout), **rồi mới** cộng balance cục bộ cho B. Không cần cert thành công gửi ngược lại Node 1 để "đóng Pending" như mô hình cũ, vì tiền đã an toàn ngay từ bước 4 — đánh dấu `Claimed` chỉ để phối hợp với mục 3.6, không phải điều kiện an toàn giá trị.

### 3.4. Nếu kiểm tra local thất bại (account không hợp lệ) hoặc Contract Call bị revert

Tiền **đã thật sự nằm ở `NodeFloatAccount[2]`** (khác mô hình cũ, nơi tiền chỉ là 1 "quyền claim" chưa thực sự di chuyển) — nên hoàn tiền giờ đơn giản là **1 giao dịch Transfer khác, chiều ngược lại**, dùng lại đúng cơ chế ở mục 3.3, không cần `FinalizeFailedAfterExecutionRevert`/`destFailureCert`/`Refund` 3 bước như trước:

1. Node 2 phát hiện B không hợp lệ, hoặc Contract Call revert (hoàn tác toàn bộ thay đổi state đã thử).
2. ⚠️ **Chống hoàn tiền 2 lần bắt buộc (mục 8 #12):** mô hình cũ có guard tường minh trong `Refund()` (chỉ chạy khi `MessageStatus == Pending`). Mô hình mới không còn hàm trung tâm này — Node 2 **phải tự đánh dấu `MessageID` = `Claimed`** trên Parent Chain (đúng bước đã thêm ở mục 3.3 bước 7, dùng chung 1 cơ chế cho cả credit thành công lẫn hoàn tiền) **TRƯỚC KHI** gửi Transfer hoàn tiền — nếu crash giữa chừng và retry lại từ đầu, Node 2 phải kiểm tra `MessageID` này đã `Claimed` chưa trước khi tạo giao dịch hoàn tiền mới, tránh gửi hoàn tiền 2 lần (tự bào mòn quỹ của chính Node 2, không phải lỗi hệ thống nhưng vẫn là mất tiền thật cho node đó).
3. Node 2 gửi Transfer ngược: `NodeFloatAccount[2] -= Value` (**chỉ đúng phần `Value`**), `NodeFloatAccount[1] += Value`.
4. Node 1 nhận lại, cộng trả cho User A.
5. ⚠️ **`GasFee` KHÔNG được hoàn** (quy tắc không đổi so với bản trước — vẫn cần giữ, xem mục 8 #6): Node 2 đã thực sự tốn chi phí tính toán để thử thực thi/kiểm tra dù kết quả thất bại. Nếu hoàn cả `GasFee`, kẻ tấn công spam giao dịch cố tình gây revert để bào CPU/RAM Node 2 miễn phí.

### 3.5. "Thanh khoản cạn" — ĐÃ SỬA: bất khả thi bằng toán học với giao dịch hợp lệ, không phải vấn đề vận hành nữa

⚠️ **Sửa lại hoàn toàn so với 2 bản trước — mục này từng mô tả sai 1 "vấn đề mới" thực ra không tồn tại nếu áp đúng bất biến ở mục 3.2.** Vì `NodeFloatAccount[node] = Σ balance mọi user thuộc node đó`, và mọi số dư đều `≥ 0`, nên `NodeFloatAccount ≥` balance của **bất kỳ 1 user nào**. User A không bao giờ được phép gửi quá số dư CỦA CHÍNH A (kiểm tra cơ bản đã có sẵn ở mọi hệ thống) — nên `NodeFloatAccount ≥ balance(A) ≥ V` LUÔN đúng khi A định gửi V. **Không có kịch bản nào 1 giao dịch hợp lệ bị "chặn vì Float không đủ"** — vấn đề "giống bài toán thanh khoản Lightning Network" ở 2 bản trước là do hiểu sai `NodeFloatAccount` như 1 quỹ dự trữ RỜI RẠC (phải chủ động nạp, có thể cạn) thay vì 1 bất biến tự động.

**Điều duy nhất còn lại — cửa sổ ngắn khi crash giữa chừng (không phải "thanh khoản cạn"):** giữa lúc trừ balance cục bộ và lúc Parent Chain xác nhận Transfer (mục 3.3 bước 2-3) vẫn có 1 khoảng ngắn có thể gián đoạn nếu crash/mất mạng — đây là vấn đề **crash-recovery** (đã xử lý bằng Retry Queue, mục 6.1) chứ không phải thiếu vốn. Không cần cảnh báo/auto top-up "ngưỡng Float Account" nào nữa (Q(ngưỡng Float Account) coi như đóng, không còn ý nghĩa).

### 3.6. Node đích còn sống nhưng kẹt/chậm xử lý — Timeout & Reclaim (mục 8 #15, MỚI)

Khác với node chết hẳn (mục 6, cần `RecoveryCommittee`), trường hợp này là Node 2 **vẫn hoạt động bình thường** nhưng vì lý do nào đó (backlog, bug, quá tải) không xử lý (không credit local, không hoàn tiền) 1 credit đã nhận trong thời gian dài — tiền nằm im ở `NodeFloatAccount[2]`, User A không được phục vụ cũng không được hoàn, không có điểm dừng theo thời gian nếu không thiết kế thêm.

- **Cơ chế Reclaim:** nếu quá 1 khoảng thời gian cấu hình (ví dụ tương tự `TimeoutTimestamp` cũ) kể từ lúc Transfer confirm mà `MessageID` vẫn CHƯA được đánh dấu `Claimed` (mục 3.3 bước 7), **chính Node 1** (thay mặt User A) có quyền gửi 1 transaction Reclaim thẳng lên Parent Chain: `NodeFloatAccount[2] -= Value`, `NodeFloatAccount[1] += Value` — **không cần Node 2 hợp tác**, vì tiền vốn dĩ do Parent Chain custody thật (điểm mạnh của mô hình Float Account so với mô hình cũ, nơi phải phụ thuộc đích tạo `destFailureCert`).
- ⚠️ **Điều kiện "quá timeout" phải do chính Parent Chain kiểm tra on-chain (so sánh `blockTime` hiện tại với thời điểm Transfer confirm), KHÔNG được chỉ là quy ước Node 1 tự giác tuân theo ở tầng client.** Nếu không enforce on-chain, 1 implementation lỗi/hấp tấp ở Node 1 có thể Reclaim ngay lập tức sau khi gửi, trước khi Node 2 (dù hoàn toàn khoẻ mạnh, chỉ hơi chậm) kịp xử lý — không làm mất tiền của ai (vẫn có guard chặn race ở dưới), nhưng phá hỏng đúng mục đích của cơ chế Reclaim (cho Node 2 một khoảng thời gian xử lý hợp lý) và tạo ma sát/trải nghiệm xấu không cần thiết cho Node 2 vốn dĩ không có lỗi gì.
- **Chặn race Reclaim-vs-Claimed (bắt buộc):** Parent Chain chỉ cho Reclaim thành công nếu `MessageID` đó **chưa** được đánh dấu `Claimed` tại thời điểm xử lý Reclaim — nếu Node 2 vừa kịp đánh dấu `Claimed` (đang credit local cho B) đúng lúc Node 1 gửi Reclaim, chỉ 1 trong 2 thắng (Parent Chain xử lý tuần tự, ai đến trước với `MessageID` chưa bị đánh dấu thì thắng) — bên thua bị từ chối, không có chuyện cả 2 cùng thành công.

---

## 4. Bảo mật kinh tế — thu hẹp phạm vi theo mô hình Float Account

### 4.1. ~~Bất biến Bond-vs-Deposit~~ — ĐÃ BỎ, không còn cần thiết (mục 3.2 đã sửa lại)

Bất biến này (yêu cầu `SecurityBond ≥ hệ_số × giới_hạn_nạp_quỹ_mỗi_chu_kỳ`) được thiết kế để giới hạn thiệt hại nếu node **khai khống** khi "nạp quỹ". Sau khi sửa lại mục 3.2 — `NodeFloatAccount` là 1 bất biến tự động, không còn hành động "nạp quỹ" rời rạc nào để node khai khống nữa (mỗi đồng vào đều gắn với 1 giao dịch xác minh được riêng lẻ) — **không còn gì để bất biến này bảo vệ**. `SecurityBond`/`RecoveryCommittee` vẫn giữ nguyên vai trò cho các mục đích KHÁC vốn có sẵn trong `GatewayEngine` (đăng ký chain, `SlashOnEquivocation` khi double-sign checkpoint, `DeclareChainDeadWithCert`) — chỉ riêng vai trò "giới hạn thiệt hại khi nạp quỹ" là không còn áp dụng.

### 4.1b. Vai trò còn lại của `SecurityBond`/gian lận — chuyển hẳn sang mục 6.3 (phân bổ, không phải tổng số)

Rủi ro gian lận duy nhất còn sót lại trong toàn hệ thống là **node khai khống PHÂN BỔ** khi chết (gán tổng tiền thật cho 1 địa chỉ nó kiểm soát thay vì chia đúng cho user) — không còn liên quan gì đến "nạp quỹ" nữa. Xem mục 6.3 để biết cơ chế bảo vệ (Snapshot + DA-Withholding + Velocity + Delay 72h, giờ bảo vệ đúng rủi ro này).

### 4.2. Velocity-limit cho Transfer: không cần để chống MINT SAI, nhưng vẫn cần vì lý do KHÁC (đã sửa lại — xem mục 4.4)

Mô hình cũ cần `checkAndRecordVelocity` áp cho mọi `AttestCommit` vì mỗi message là 1 "tuyên bố mint" cần giới hạn thiệt hại nếu sai. Mô hình mới: mỗi Transfer là ghi sổ trực tiếp trên số dư THẬT đã có sẵn trong `NodeFloatAccount` — không có gì để "mint sai" ở bước này, nên **đúng là không cần velocity-limit CHO LÝ DO CHỐNG-GIAN-LẬN**. ⚠️ **Nhưng đây không phải toàn bộ câu chuyện — velocity-limit còn có vai trò thứ 2 hoàn toàn khác mà bản thảo đầu của mục này đã bỏ sót: giới hạn thiệt hại khi KHOÁ KÝ CỦA NODE BỊ LỘ (không phải khi node cố ý gian lận). Xem mục 4.4 — vai trò này vẫn cần, chỉ là lý do khác.**

### 4.3. Bất biến tổng cung — ĐÃ ĐƠN GIẢN HOÁ

Sau khi sửa mục 3.2, bất biến này gọn lại đáng kể: **`Σ NodeFloatAccount == genesis_total_supply`** (không còn số hạng "local balance chưa nạp quỹ" nào — mọi số dư user đều tự động phản ánh trong `NodeFloatAccount` ngay khi phát sinh). Đây là bất biến **kiểm tra tự động được hoàn toàn**, không còn phần nào "không kiểm chứng được on-chain" như bản trước — vì `NodeFloatAccount` giờ chính xác là tổng thật, không phải trần/ước lượng.

> Lưu ý nhỏ: `NodeFloatAccount` có thể gồm thêm phần doanh thu `GasFee` node đã thu (mục 3.4 điểm 5 — không hoàn khi thất bại) chưa gắn với user cụ thể nào — phần này vẫn nằm trong tổng bảo toàn (chỉ chuyển từ `NodeFloatAccount` node gửi sang node nhận), không phá vỡ bất biến, chỉ là 1 phần nhỏ không thuộc "Σ balance user" theo nghĩa hẹp.

### 4.4. [MỚI — mục 8 #14] Velocity-limit cho Transfer OUTFLOW: giới hạn thiệt hại khi khoá ký node bị lộ

**Vấn đề:** vì Float Account là tiền thật và Transfer không còn hard-cap/velocity nào (mục 4.2), nếu khoá ký (device key/BLS key) của 1 node bị lộ, kẻ tấn công có thể gửi 1 Transfer DUY NHẤT rút sạch **TOÀN BỘ** `NodeFloatAccount` của node đó sang 1 địa chỉ kiểm soát bởi kẻ tấn công, ngay lập tức — không có gì cản. Đây là rủi ro **nặng hơn** cả #9 (custody local, mục 2.3), vì quỹ liên-node có thể lớn hơn hẳn tổng số dư local của node (mục 3.5 khuyến khích giữ đủ thanh khoản để không bị block giao dịch).

**Quyết định:** tái áp dụng velocity-limit, nhưng cho **Transfer OUTFLOW** (không phải cho Deposit như mục 4.1 — đây là 1 giới hạn khác, mục đích khác):

> Tổng giá trị gửi ra (Transfer outflow) của 1 `NodeFloatAccount` trong 1 cửa sổ trượt (ví dụ 24h) không được vượt quá X% số dư đầu cửa sổ — mặc định đề xuất giữ nguyên **20%/24h** (số đã dùng quen ở bản trước cho mục đích khác, nhưng cùng bậc độ lớn hợp lý cho mục đích mới này) — cần đội xác nhận lại theo dữ liệu traffic thật.

Đây **không phải** hard-cap chống gian lận (không có gì để gian lận ở bước Transfer) mà là **circuit-breaker chống lộ khoá** — giống hệt lý do các sàn/ngân hàng đặt hạn mức rút tiền/ngày ngay cả với tài khoản đã xác minh đầy đủ danh tính. Khi vượt ngưỡng: giao dịch bị tạm giữ + cảnh báo operator ngay (không tự động từ chối vĩnh viễn, vì đây có thể là traffic hợp lệ tăng đột biến, không phải luôn là dấu hiệu bị hack).

⚠️ **Loại trừ bắt buộc (mục 8 #17, MỚI):** ngưỡng này **CHỈ áp dụng cho Transfer gửi mới** (mục 3.3) — **KHÔNG áp dụng cho Hoàn tiền** (mục 3.4) hay **Reclaim** (mục 3.6). Lý do: hoàn tiền/reclaim chỉ bao giờ trả lại đúng giá trị đã thực sự nhận trước đó (bị chặn trên bởi số tiền đã vào, không phải 1 bề mặt tấn công mới để lộ khoá khai thác), trong khi Transfer gửi mới mới là nơi kẻ tấn công có khoá lộ có thể tạo ra dòng tiền HOÀN TOÀN MỚI ra 1 địa chỉ nó kiểm soát. Nếu lỡ áp chung 1 ngưỡng cho cả 2 loại: 1 node đang bị tấn công spam-revert (#6) có thể bị chính circuit-breaker này chặn luôn cả việc hoàn tiền hợp lệ cho user vô tội đang chờ — tạo ra DoS tầng 2 do chính cơ chế phòng thủ gây ra.

---

## 5. Đăng ký & Tra cứu Account/Contract

*(Không đổi so với bản trước — cơ chế này độc lập với việc chuyển giá trị dùng mô hình nào.)*

### 5.1. Account Registry

| Bảng | Key | Value | Vai trò |
|---|---|---|---|
| Account Registry | `user_address` | `chainID` (của node sở hữu) | Định tuyến `Target` đến đúng node; node đó tự kiểm tra hợp lệ trước khi credit (mục 3.3 bước 4) |

- **Đăng ký 1 lần, chống ghi đè:** chỉ chấp nhận lần đầu, hoặc cần chữ ký của node hiện tại để chuyển nhượng (Migration, mục 5.3).
- **Chặn double-registration:** cơ chế chặn nằm ở chính write vào Account Registry trên Parent Chain (transaction xử lý tuần tự = điểm serialize duy nhất) — không coi admin-confirm cục bộ tại node là đã chốt quyền sở hữu (chi tiết đầy đủ mục 12 Q1).
- **An toàn trước DoS:** đăng ký qua `handleSetBlsPublicKey` + admin confirm (2 bước, có gate) — không permissionless.
- **Vốn khởi tạo khi đăng ký node mới (đã đóng — Q5):** `RegisterChainViaStake` dùng đúng số tiền thật operator/nhà đầu tư tự bỏ ra từ ví của họ (bond đăng ký) — **không còn rút từ 1 "pool Reserve" do Parent Chain khởi tạo genesis** như tàn dư của mô hình `PerChainAllocation`-làm-trần cũ. Vốn khởi đầu cho Float Account của node cũng vậy — nạp trực tiếp từ ví (mục 3.2 đường A). Không cần đội lập kế hoạch cấp vốn Reserve cho N node nữa.

### 5.2. Contract Registry — KHÔNG dùng registry toàn cục

Không đổi: bên gửi (`Target`+`chainID` đích) đã phải tự biết trước contract nằm ở node nào — không có API đăng ký contract để tránh DoS/race-condition tra cứu. Kiểm tra tồn tại cục bộ tại đích (mục 3.3 bước 4) là đủ.

### 5.3. Migration Account (CHỈ User, KHÔNG áp dụng Contract)

Không đổi: Contract không được migrate (vì mục 5.2 đã bỏ Contract Registry, contract đổi node sẽ "tàng hình"). Giao thức 3 pha cho User Account: **Freeze** (khoá tx nội bộ mới, giữ lại request đến trong hàng đợi tạm, không revert ngay) → **Export & Attest** (đóng gói balance+nonce, ký `QuorumCert`) → **Import & Flip con trỏ** (chỉ flip Account Registry SAU KHI node mới xác nhận import xong, không lạc quan).

---

## 6. Xử lý Mất kết nối & Node chết vĩnh viễn

### 6.1. Mất kết nối tạm thời

Không đổi: giao dịch nội bộ không bị ảnh hưởng. Giao dịch cross-node gửi thất bại do mất mạng: giữ nguyên trong Retry Queue cục bộ, gửi lại đúng transaction đã ký, không tạo transaction mới cho cùng 1 yêu cầu.

### 6.2. Node chết hẳn — ai xác nhận, xử lý ra sao

- Tiêu chí trigger: 2 lớp — (1) tự động cảnh báo sau N lần bỏ lỡ chu kỳ hoạt động bình thường liên tiếp, (2) **bắt buộc xác nhận thủ công của operator** trước khi thực sự gọi `DeclareChainDeadWithCert` — không tự động hoá hoàn toàn vì hậu quả quá lớn.
- `DeclareChainDeadWithCert` đòi hỏi `QuorumCert` của **`RecoveryCommittee`** — một thực thể quyền lực riêng, cố định, set 1 lần từ config lúc triển khai, **chưa được định nghĩa trong tài liệu này** (ai ngồi trong đó, bao nhiêu người, ngưỡng quorum — mục 8 #8, còn mở). Không phải quyết định đơn phương của node còn lại.

### 6.3. Rút lại giá trị khi node chết — ĐÃ VIẾT LẠI: không còn "2 trường hợp A/B", chỉ còn 1 bài toán PHÂN BỔ

⚠️ **Sửa lại hoàn toàn so với 2 bản trước.** Sau khi sửa mục 3.2 (`NodeFloatAccount` là bất biến tự động, không còn "chưa nạp quỹ"), **"Trường hợp B" không còn tồn tại như 1 trạng thái đứng yên nữa** — không có chuyện 1 phần số dư user "chưa kịp nạp" nằm chờ dài hạn, vì mọi số dư user LUÔN tự động phản ánh trong `NodeFloatAccount` ngay khi phát sinh. Khi node chết, chỉ còn đúng **1 bài toán duy nhất**: `NodeFloatAccount[node chết]` chắc chắn có THẬT (Parent Chain tự verify được, mục 4.3) — nhưng **Parent Chain không biết tổng đó phải CHIA cho user nào bao nhiêu**, vì phân bổ chi tiết chỉ tồn tại trên local node đã chết.

**Phần xử lý ngay, không cần chờ gì (giống "Trường hợp A" cũ, không đổi):**
- Transfer đang "đi ngang" tới node chết nhưng chưa `Claimed`: dùng cơ chế **Reclaim** (mục 3.6), Node 1 tự đòi lại, kích hoạt ngay khi `RecoveryCommittee` xác nhận chết — không cần Transfer ngược (mục 3.4, cần node sống mới gửi được).
- Không cần Merkle proof cho bước này — đây chỉ là ghi sổ 2 chiều bình thường trên Parent Chain.

**Bài toán còn lại — chứng minh PHÂN BỔ (khác bản chất so với "chứng minh tổng số" ở 2 bản trước, nhưng vẫn cần đúng những cơ chế sau):**
1. **Snapshot Export định kỳ, PROACTIVE** (mỗi 15 phút, Q12) — export cây Merkle **phân bổ** account state ra ngoài node + publish `AccountTreeRoot` có xác thực lên Parent Chain. Root này giờ không còn dùng để "chứng minh tổng có thật" (đã tự động đúng) mà để chứng minh **"tổng đã biết trước đó (NodeFloatAccount) được chia cho ai bao nhiêu"**.
2. **Chống Data Availability Withholding (fail-closed):** vẫn cần — 1 node có thể publish 1 cây phân bổ giả (gán hết cho ví nó kiểm soát) dù TỔNG là thật, rồi giấu dữ liệu chi tiết để Archival Service không đối chiếu được. Archival Service phải xác nhận nhận đủ dữ liệu khớp root trong vài phút sau publish; không đủ = VETO ngay, không đợi hết thời gian chờ.
3. **`ClaimDeadChainBalance` + Withdrawal Delay tối thiểu 72 giờ:** cho operator/Archival thời gian phát hiện cây phân bổ giả mạo trước khi giải ngân thật. ⚠️ **Không còn cần lớp velocity-limit riêng cho bước này nữa** (khác 2 bản trước) — vì TỔNG tiền đã được `NodeFloatAccount` giới hạn chính xác từ trước (không thể rút vượt tổng thật), rủi ro duy nhất còn lại là phân bổ sai giữa các user trong CÙNG tổng đó, không phải rút vượt tổng — Delay 72h + DA-defense đã đủ xử lý rủi ro này, velocity-limit (vốn để giới hạn TỐC ĐỘ rút, không liên quan phân bổ) không còn cần thiết ở đây.
4. **Cửa sổ mất mát = tần suất export** (mặc định 15 phút) — giao dịch NỘI BỘ (cùng node, không qua `NodeFloatAccount`) sau lần export cuối vẫn có thể không chứng minh được PHÂN BỔ chính xác nếu node chết ngay sau đó (dù tổng tiền vẫn an toàn) — đây là lý do vẫn cần export định kỳ dù tổng đã luôn đúng.
5. **Ai tính proof hộ user:** dịch vụ archival độc lập, không bắt user tự giữ Merkle proof.

**Không còn "khuyến nghị nạp quỹ thường xuyên để thu hẹp phạm vi" như 2 bản trước** — không còn ý nghĩa vì không còn "chưa nạp quỹ" nào để thu hẹp; tần suất Snapshot giờ chỉ ảnh hưởng độ chính xác của PHÂN BỔ khi node chết đột ngột, không ảnh hưởng độ an toàn của TỔNG tiền (luôn an toàn từ đầu).

---

## 7. Tóm tắt Cấu trúc Data Model

### Tại Parent Chain / Root Anchor
| Bảng / Struct | Nguồn | Vai trò |
|---|---|---|
| Account Registry | Mở rộng `AccountManager` contract có sẵn | `user_address -> chainID` |
| `ChainRegistry` | Có sẵn (`gateway.go`) | Danh tính + `NodeBlsPublicKey` từng node |
| **`NodeFloatAccount` (MỚI)** | Cần xây | `chainID -> balance` — tiền thật, thay cho `PerChainAllocation`-làm-trần |
| **`ClaimedMessages` (MỚI)** | Cần xây | `MessageID -> bool` — đánh dấu đã xử lý (credit local hoặc hoàn tiền), dùng để chống xử lý trùng (#13), chống hoàn tiền 2 lần (#12), và làm điều kiện chặn Reclaim (mục 3.6, #15) |
| `SecurityBondLedger` | Có sẵn | Bond, phạm vi thu hẹp chỉ còn bảo vệ đăng ký chain + chống khai khống PHÂN BỔ khi node chết (mục 4.1b) |
| `DeadChains` | Có sẵn | Node đã tuyên bố chết, chặn outflow mới |

### Tại mỗi BLS Node (Local DB)
| Bảng / Struct | Key | Value | Vai trò |
|---|---|---|---|
| Account State | `user_address` | `balance`, `nonce`, `contract_state` | State thực sự |
| Retry Queue | `message_id` | `payload`, `status` | Chỉ cho mất-kết-nối tạm thời (mục 6.1) |
| Local state machine | `message_id` | `LOCAL_APPLIED_PENDING_SEND / SENT_CONFIRMED / ...` | Xem mục 13.4 — đơn giản hơn bản trước vì Transfer atomic |

---

## 8. Danh sách vấn đề logic/bảo mật còn hiệu lực với mô hình Float Account

> Danh sách này đã dọn bỏ các vấn đề chỉ tồn tại trong mô hình cũ (3 bước `Outbound`/`AttestCommit`/`ClaimMessage`, bond-vs-exposure cho từng giao dịch, ACK/NACK 2 chiều cho phần giá trị) — chúng tự triệt tiêu khi chuyển sang Float Account (mục 3.1). Chỉ giữ lại vấn đề vẫn còn hiệu lực thật.

| # | Vấn đề | Rủi ro cụ thể | Xử lý |
|---|---|---|---|
| 1 | Committee mỗi node = 1 (chính nó) — không có redundancy signer thật | `QuorumCert` về bản chất là chữ ký đơn | Phòng thủ không nằm ở số lượng chữ ký mà ở giới hạn thiệt hại: velocity-limit cho Transfer OUTFLOW khi khoá bị lộ (mục 4.4) + Snapshot/Delay 72h khi node chết hẳn (mục 6.3) |
| 2 | ~~Nạp quỹ (Deposit) vẫn là điểm duy nhất còn cần "tin cậy có kiểm chứng"~~ | Node có thể khai khống số dư local khi nạp quỹ | ✅ **RESOLVED (mục 3.2 viết lại):** không còn hành động "nạp quỹ" rời rạc nào để khai khống — `NodeFloatAccount` là bất biến tự động, mỗi đồng gắn với 1 giao dịch xác minh được riêng lẻ |
| 3 | Account Registry cho phép ghi đè mapping tuỳ ý nếu không kiểm soát | Report cũ/replay có thể "cướp" account sang node khác | Chỉ chấp nhận đăng ký lần đầu hoặc chữ ký của node hiện tại để chuyển nhượng (mục 5.1, 5.3) |
| 4 | Contract tự sinh (deploy trong node) không thể đăng ký registry toàn cục | DoS/state-bloat lên Parent Chain nếu bắt đăng ký như Account | Bỏ hẳn Contract Registry toàn cục; dùng `chainID` đích tường minh từ người gửi + kiểm tra tồn tại cục bộ (mục 5.2) |
| 5 | Chuyển nhượng account (Migration) không atomic nếu không thiết kế kỹ | Message đến đúng lúc đang chuyển giao có thể bị kẹt/mất | Giao thức 3 pha Freeze → Export & Attest → Import & flip con trỏ (mục 5.3) |
| 6 | `GasFee` bị hoàn nhầm khi giao dịch cross-node thất bại | Spam-revert trở thành DoS miễn phí lên node đích | Transfer hoàn tiền (mục 3.4) CHỈ hoàn `Value`, không hoàn `GasFee` |
| 7 | Node chết hẳn — Parent Chain biết TỔNG tiền thật nhưng không biết PHÂN BỔ cho user nào bao nhiêu (đã đổi bản chất, mục 6.3 viết lại — không còn là "chưa nạp quỹ") | Node có thể khai khống PHÂN BỔ (gán hết cho ví nó kiểm soát) dù tổng đã chắc chắn đúng | Snapshot & Archival Pipeline + chống DA-Withholding + Withdrawal Delay 72h — bảo vệ đúng rủi ro phân bổ, không còn cần velocity-limit riêng cho bước này (mục 6.3) |
| 8 | `RecoveryCommittee` — thực thể duy nhất có quyền tuyên bố node chết, tịch thu bond, thay khoá ký bất kỳ node nào — chưa được định nghĩa | Lộ/compromise `RecoveryCommittee` ảnh hưởng TOÀN hệ thống, nặng hơn lộ 1 node đơn lẻ | Cần đội xác định thành viên, ngưỡng quorum, quy trình bảo vệ khoá — quyết định tổ chức thật, không tự đề xuất được (mục 9.1, còn mở) |
| 9 | Custody 100% device key (PKS) — Node bị hack có thể ký giao dịch nội bộ giả | Mất tiền không để lại bằng chứng mật mã, ngoài phạm vi bảo vệ của Float Account (tiền không rời node) | Không giải quyết triệt để bằng kỹ thuật — giảm thiểu bằng delay/anomaly-detection/non-custodial tuỳ chọn (mục 2.3) |
| 10 | ~~Thanh khoản Float Account cạn giữa chừng~~ | Giao dịch outbound bị block cho tới khi node kịp nạp thêm | ✅ **RESOLVED (mục 3.5 viết lại):** bất khả thi bằng toán học với giao dịch hợp lệ — `NodeFloatAccount` luôn ≥ balance của bất kỳ user nào |
| 11 | ~~Không phân biệt "mất kết nối" với "Float Account không đủ"~~ | User bị trừ tiền cục bộ nhưng giao dịch kẹt vô thời hạn | ✅ **RESOLVED (mục 3.3 bước 2 viết lại):** "Float không đủ" không còn là kịch bản có thể xảy ra nữa, chỉ còn kiểm tra số dư cục bộ bình thường của user |
| 12 | **[MỚI]** Reverse Transfer (hoàn tiền, mục 3.4) không có cơ chế chống gửi 2 lần — mô hình cũ có guard `MessageStatus==Pending` trong `Refund()`, mô hình mới chưa thay thế | Crash giữa chừng rồi retry có thể gửi hoàn tiền 2 lần, tự bào mòn quỹ của chính node đang hoàn tiền | Đánh dấu `MessageID` = `Claimed` trên Parent Chain TRƯỚC khi gửi hoàn tiền, kiểm tra lại trạng thái này trước khi retry (mục 3.3 bước 7, mục 3.4 bước 2) |
| 13 | **[MỚI]** Node đích không có cơ chế chống xử lý trùng 1 credit đến — mô hình cũ có `ErrAlreadyClaimed` tự động trong `ClaimMessage`, mô hình mới không còn hàm trung tâm nào làm việc này hộ | Node đích có thể credit local 2 lần cho cùng 1 `MessageID` nếu logic theo dõi/watcher bị lỗi hoặc quét lại block cũ | Node đích tự giữ bảng "MessageID đã xử lý", kiểm tra trước khi làm bất cứ gì (mục 3.3 bước 5) |
| 14 | **[MỚI, NGHIÊM TRỌNG]** Bỏ velocity-limit cho Transfer với lý do "không mint sai được" đúng cho gian lận nhưng bỏ sót vai trò giới hạn thiệt hại khi KHOÁ KÝ node bị lộ | Node bị lộ khoá có thể bị rút sạch TOÀN BỘ Float Account trong 1 giao dịch tức thời — nặng hơn cả #9 (custody local) vì quỹ liên-node có thể lớn hơn local balance | Tái áp dụng velocity-limit cho Transfer OUTFLOW — circuit-breaker chống lộ khoá, không phải hard-cap chống gian lận (mục 4.4) |
| 15 | **[MỚI]** Không có timeout nếu node đích còn sống nhưng kẹt/chậm xử lý 1 credit đã nhận (khác node chết hẳn, không cần `RecoveryCommittee`) | Tiền nằm im ở Float Account của đích, User A gốc không được phục vụ cũng không được hoàn, không có điểm dừng theo thời gian | Cơ chế Reclaim: quá timeout mà `MessageID` chưa `Claimed`, Node 1 tự reclaim thẳng từ Parent Chain không cần Node 2 hợp tác — có chặn race với việc Node 2 vừa kịp `Claimed` (mục 3.6) |
| 16 | **[MỚI]** Node đích không có bước phục hồi nếu crash ĐÚNG GIỮA lúc đánh dấu `Claimed` (đã gửi lên Parent Chain) và lúc credit local cho B (chưa kịp làm) | Restart mà không kiểm tra đúng trạng thái này có thể credit local 2 lần (nếu logic không biết đã `Claimed` rồi mà cứ credit lại), hoặc bỏ sót vĩnh viễn (nếu chỉ thấy "đã Claimed" rồi coi như xong mà quên credit) | Node đích cần state machine cục bộ riêng cho bước này: `MARKED_CLAIMED_PENDING_CREDIT` → `CREDITED` — khi restart, nếu thấy `Claimed` trên Parent Chain nhưng local chưa ghi `CREDITED`, phải tiếp tục credit chứ không được re-mark `Claimed` cũng không được bỏ qua (mục 13.3) |
| 17 | **[MỚI]** Velocity-limit chống lộ khoá cho Transfer outflow (#14/mục 4.4) chưa nói rõ có loại trừ Hoàn tiền (mục 3.4)/Reclaim (mục 3.6) hay không | Nếu áp chung 1 ngưỡng cho cả 2 loại, 1 node đang bị tấn công spam-revert (#6) có thể bị chính circuit-breaker này chặn luôn cả việc hoàn tiền hợp lệ cho user vô tội đang chờ — DoS tầng 2 do chính cơ chế phòng thủ gây ra | Loại trừ tường minh: ngưỡng chỉ áp cho Transfer gửi MỚI (mục 3.3); Hoàn tiền/Reclaim luôn được miễn vì chỉ trả lại đúng giá trị đã thực nhận trước đó, không phải bề mặt tấn công mới (mục 4.4) |

---

## 9. Production Readiness Checklist

### 9.1. Decision Log

| # | Câu hỏi | Quyết định | Căn cứ |
|---|---|---|---|
| Q(mô hình giá trị) | Chuyển hẳn sang Node Float Account hay giữ mô hình bond cũ? | **Đã chốt: chuyển hẳn**, phạm vi giới hạn ở quỹ liên-node (giao dịch nội bộ không đổi) | mục 3 |
| Q13 | 1 node = 1 chainID? | **Đã chốt**, không đổi từ bản trước | mục 2.4 |
| Q3 | Committee mỗi node bao nhiêu validator? | **Chấp nhận = 1** (chính node) | mục 2.4 |
| Q(hệ số Bond-vs-Deposit) | Hệ số an toàn cho bất biến mục 4.1? | **✅ ĐÃ ĐÓNG — không còn áp dụng.** mục 4.1 đã bỏ (mục 3.2 viết lại: không còn "nạp quỹ", `NodeFloatAccount` là bất biến tự động) | mục 3.2, mục 4.1 |
| Q(giới hạn nạp quỹ mỗi chu kỳ) | Bao nhiêu mỗi chu kỳ? | **✅ ĐÃ ĐÓNG — không còn áp dụng.** Không còn khái niệm "nạp quỹ theo chu kỳ" (mục 3.2) | mục 3.2 |
| Q(tần suất snapshot) | Bao nhiêu lâu 1 lần? | **Mặc định 15 phút**, có thể tăng sau khi đo chi phí thật — Snapshot giờ phục vụ chứng minh PHÂN BỔ, không phải tổng số (mục 6.3) | mục 6.3 |
| Q(ngưỡng Float Account) | Ngưỡng tối thiểu cảnh báo/auto top-up? | **✅ ĐÃ ĐÓNG — không còn áp dụng.** `NodeFloatAccount` luôn ≥ tổng số dư user do construction (mục 3.5), không còn khái niệm "cạn thanh khoản" cần cảnh báo | mục 3.5 |
| Q(velocity Transfer outflow) | Ngưỡng circuit-breaker chống lộ khoá cho Transfer (mục 4.4)? | **Mặc định khởi điểm 20%/24h**, giữ nguyên tinh thần bản trước nhưng đổi mục đích — cần đội xác nhận lại theo traffic thật, không chặn triển khai ban đầu | mục 4.4, mục 8 #14 |
| Q(timeout Reclaim) | Bao lâu thì Node 1 được phép Reclaim nếu Node 2 chưa `Claimed`? | Công thức tương tự `TimeoutTimestamp` cũ (mục 6.2 bản trước) — chưa có số tuyệt đối, cần đo chu kỳ xử lý bình thường thật trước khi chốt | mục 3.6, mục 8 #15 |
| Q(RecoveryCommittee) | Ai ngồi trong đó, bao nhiêu người, ngưỡng quorum? | **CÒN MỞ THẬT SỰ** — quyết định tổ chức/nhân sự, không tự đề xuất được. **Chặn cứng go-live**: code không chạy được nếu thiếu config này | mục 8 #8 |
| Q5 | Vốn Reserve ban đầu cho N node? | **✅ ĐÃ ĐÓNG — không còn áp dụng.** Không cần Parent Chain khởi tạo/quy hoạch pool Reserve nữa — node lấy vốn khởi đầu trực tiếp từ ví operator/nhà đầu tư (mục 3.2 đường A), đúng bản chất "chuyển khoản ví-sang-ví, chuyển bao nhiêu thì node có bấy nhiêu" | mục 3.2, mục 5.1 |
| Q9-rủi-ro | Mức rủi ro custody PKS chấp nhận được với quy mô tài sản thật? | **CÒN MỞ** — khẩu vị rủi ro kinh doanh thật | mục 2.3 |

**2 mục còn mở thật sự, không tự đề xuất số được:** `RecoveryCommittee` thành viên, Q9-rủi-ro (custody risk acceptance). (Q5 đã đóng — xem trên.)

### 9.2. Vận hành

- **Quản lý khoá `RecoveryCommittee`** (ưu tiên cao hơn khoá node — mục 8 #8): multisig/HSM/threshold-signing riêng, tách biệt quy trình vận hành khoá node thường.
- **Giám sát Snapshot Pipeline** (phục vụ chứng minh phân bổ khi node chết, mục 6.3): cảnh báo nếu 1 node bỏ lỡ chu kỳ export.
- **Backup & DR:** backup LevelDB từng node + state Parent Chain (`ChainRegistry`/`NodeFloatAccount`/`SecurityBondLedger`/`DeadChains`).
- **Runbook:** kịch bản node bị nghi compromise (khi nào trigger `SlashOnEquivocation`/`DeclareChainDeadWithCert`).
- **Quản lý tăng trưởng `ClaimedMessages`:** bảng này ghi vĩnh viễn mỗi `MessageID` đã xử lý — cần kế hoạch archive/prune định kỳ các bản ghi cũ (ví dụ sau N tháng) để tránh phình state Parent Chain vô hạn theo thời gian, không phải vấn đề bảo mật nhưng là vấn đề vận hành dài hạn cần tính trước.

### 9.3. Checklist bảo mật trước khi go-live

- [ ] #1–#17 ở mục 8 đã được review độc lập bởi người khác (không tự ký-tự duyệt) — đặc biệt #7 (Snapshot Pipeline chứng minh phân bổ khi node chết), #8 (`RecoveryCommittee`), #14/#17 (velocity-limit chống lộ khoá + loại trừ hoàn tiền — dễ bị bỏ sót nhất vì trực giác "Float Account tự an toàn" dễ khiến quên mất đây là rủi ro KHÁC, không phải gian lận), và #16 (crash-recovery giữa `Claimed` và credit local).
- [ ] `RecoveryCommittee`, Q9-rủi-ro đã có quyết định bằng văn bản từ đội — không được bỏ qua. (Q5 đã đóng, không còn cần quyết định gì thêm.)
- [ ] Đã test trên staging: (1) Transfer thành công, (2) Transfer thất bại → hoàn đúng `Value`, không hoàn `GasFee`, (3) Node chết → Parent Chain biết TỔNG số thật ngay (mục 4.3, tự động), Archival Service chạy đúng pipeline Snapshot+Velocity+Delay 72h để chứng minh PHÂN BỔ cho user (mục 6.3), (4) Migration có message đến giữa lúc Freeze, (5) node cố tình giấu dữ liệu snapshot → Archival Service VETO được, (6) retry crash-giữa-chừng ở bước hoàn tiền → không hoàn 2 lần (#12), (7) giả lập khoá node bị lộ, thử rút vượt ngưỡng velocity outflow → bị chặn (#14), (8) Node 2 chậm xử lý quá timeout → Node 1 Reclaim thành công mà không cần Node 2 hợp tác, và thử race Reclaim-vs-Claimed để xác nhận chỉ 1 bên thắng (#15), (9) crash Node 2 đúng giữa lúc `Claimed` và credit local, khởi động lại → xác nhận tự hoàn tất credit, không credit trùng, không bỏ sót (#16), (10) Node 2 chết hẳn khi đang có Transfer tới nhưng chưa `Claimed` → xác nhận dùng đúng cơ chế Reclaim (mục 3.6), không phải Transfer ngược (mục 3.4, vốn cần Node 2 sống), (11) giả lập 1 node vừa bị chạm ngưỡng velocity outflow (#14) vừa cần hoàn tiền hợp lệ cho user khác → xác nhận hoàn tiền vẫn đi qua bình thường, không bị chặn nhầm bởi circuit-breaker (#17).
- [ ] `Σ NodeFloatAccount == genesis_total_supply` (mục 4.3) được kiểm tra tự động định kỳ trên Parent Chain — đây là bất biến kiểm chứng được hoàn toàn, không có phần "không kiểm chứng được" nào còn sót lại.

---

## 10. Lộ trình triển khai

1. **Chốt 2 mục còn mở** (`RecoveryCommittee`, Q9-rủi-ro) trước khi viết code.
2. **Xây `NodeFloatAccount` trên Parent Chain** — cấu trúc dữ liệu mới, thay thế vai trò "trần phân bổ" của `PerChainAllocation` cho mục đích cross-node.
3. **Xây luồng user tự nạp tiền vào tài khoản của mình** — atomic tăng `NodeFloatAccount` cùng lúc với balance cục bộ user, không có bước "nạp quỹ" riêng của node (mục 3.2).
4. **Xây luồng Transfer atomic** (mục 3.3) thay thế `Outbound`/`BatchOutboundCommit`/`ClaimMessage` 3 bước cho phần giá trị — giữ nguyên cơ chế message/payload cho phần gọi Contract.
5. **Giữ nguyên Snapshot & Archival Pipeline** (mục 6.3) — giờ phục vụ chứng minh PHÂN BỔ khi node chết, không phải chứng minh tổng số (đã tự động ở mục 4.3), không xoá.
6. **Bổ sung Account Registry** (mục 5) — không đổi so với bản trước.
7. **Dựng hạ tầng vận hành** (mục 9.2) song song, không để tới sau khi code xong mới làm.
8. **Chạy checklist 9.3** trước khi cho traffic thật.

---

## 11. Sơ đồ các luồng chính

### 11.1. Kiến trúc tổng quan

```mermaid
flowchart TB
    subgraph RA["Parent Chain (Root Anchor)"]
        AR["Account Registry\nuser_address -> chainID"]
        CR["ChainRegistry\n1 entry = 1 node"]
        FA["NodeFloatAccount (MỚI)\nchainID -> balance THẬT\n(bất biến = Σ balance user, mục 3.2)"]
        SB["SecurityBondLedger\n(bảo vệ đăng ký/gian lận phân bổ, mục 4.1b)"]
        RC["RecoveryCommittee\n(chưa định nghĩa — mục 8 #8)"]
    end

    subgraph N1["Node 1 = chainID 1"]
        AH1["AccountHandler + PKS"]
        DB1[("LevelDB")]
    end

    subgraph N2["Node 2 = chainID 2"]
        AH2["AccountHandler + PKS"]
        DB2[("LevelDB")]
    end

    UserA(("User A")) --> AH1
    AH1 <--> DB1
    UserB(("User B")) --> AH2
    AH2 <--> DB2

    UserA -- "User tự nạp tiền vào TK của mình\n(atomic: balance cục bộ += V, FA[1] += V)" --> FA
    N1 -- "Transfer atomic (mỗi giao dịch)\nFA[1] -= V, FA[2] += V" --> FA
    FA -. "credit đến, Node 2 tự theo dõi" .-> N2
    N1 -- "RegisterChainViaStake / PostSecurityBond" --> CR
    RC -. "DeclareChainDeadWithCert /\nUnregisterChainWithCert" .-> CR

    style RA fill:#f4f4f4,stroke:#999,color:#333
    style N1 fill:#eef6ff,stroke:#6699cc,color:#333
    style N2 fill:#eef6ff,stroke:#6699cc,color:#333
```

**Tóm tắt bằng lời:** Mỗi node vẫn tự giữ dữ liệu tài khoản của mình như cũ. Điểm mới: Parent Chain giờ có thêm 1 "sổ quỹ" thật — mỗi node có 1 tài khoản trên đó, luôn bằng đúng tổng tiền user của node đó (không có bước "nạp quỹ" riêng của node — tiền vào chỉ qua user tự nạp cho chính mình, hoặc qua Transfer nhận từ node khác), và khi cần chuyển giá trị sang node khác thì chuyển thẳng từ tài khoản của mình sang tài khoản của node kia trên chính sổ đó, giống chuyển khoản ngân hàng — không còn phải "tuyên bố rồi chờ bên kia xác nhận" như trước.

### 11.2. Tra cứu Account/Contract → Node quản lý

```mermaid
flowchart LR
    Q["Cần gửi tới address X"] --> A{"User account\nhay Contract?"}
    A -- "User" --> B["Tra Account Registry\nuser_address -> chainID"]
    A -- "Contract" --> C["Người gửi tự biết chainID\ntừ trước — không tra Parent Chain"]
    B --> D["ChainRegistry: chainID -> NodeBlsPublicKey"]
    C --> D
    D --> E["Node đích tự kiểm tra tồn tại cục bộ\ntrước khi credit (mục 3.3 bước 4)"]
```

### 11.3. Chuyển giá trị Cross-Node — THÀNH CÔNG

```mermaid
sequenceDiagram
    participant A as User A
    participant N1 as Node 1
    participant RA as Parent Chain (NodeFloatAccount)
    participant N2 as Node 2
    participant B as User B

    Note over N1,RA: FA[1] luôn = Σ balance user của Node 1 (bất biến tự động, mục 3.2)\nkhông có bước "nạp quỹ" định kỳ nào ở đây

    A->>N1: Gửi yêu cầu chuyển cho B (Node 2)
    N1->>N1: Kiểm tra balance cục bộ của A ≥ V\n(bình thường, + serialize chống race cùng 1 user)
    N1->>N1: Trừ balance cục bộ của A
    N1->>RA: Transfer atomic: FA[1] -= V, FA[2] += V\n(kèm Target=B, MessageID duy nhất)
    RA-->>N1: Xác nhận — tiền đã thật ở FA[2]

    N2->>RA: Theo dõi, thấy credit mới addressed cho mình
    N2->>N2: Kiểm tra MessageID chưa có trong\nClaimedMessages cục bộ (#13)
    N2->>N2: Kiểm tra local: B có hợp lệ không?
    N2->>RA: Đánh dấu MessageID = Claimed (#12, #15)\nTRƯỚC KHI credit local
    N2->>B: Hợp lệ -> cộng balance cục bộ cho B

    Note over N1,N2: Tiền đã an toàn ngay khi Transfer confirm (bước ①-②).\nBước "Claimed" chỉ phối hợp chống trùng/Reclaim (mục 3.6),\nkhông phải điều kiện an toàn giá trị.
```

**Tóm tắt bằng lời:** Tài khoản của Node 1 trên Parent Chain luôn bằng đúng tổng tiền của toàn bộ user Node 1 đang quản lý — không có bước "nạp quỹ" riêng nào để làm trước. Khi User A muốn chuyển tiền cho User B (ở Node 2), Node 1 chỉ cần kiểm tra A có đủ tiền cục bộ như 1 giao dịch bình thường, trừ tiền A, rồi chuyển thẳng đúng số đó sang tài khoản Node 2 trên Parent Chain. Node 2 thấy tiền về, kiểm tra chưa xử lý giao dịch này lần nào, kiểm tra B có đúng là tài khoản của mình không, đánh dấu "đã nhận" trên sổ chung rồi mới cộng tiền cục bộ. Khác bản trước: tiền đã thật sự an toàn ngay khi bước chuyển khoản xác nhận xong.

### 11.4. Thất bại, Hoàn tiền & Reclaim-theo-timeout

```mermaid
sequenceDiagram
    participant N1 as Node 1
    participant RA as Parent Chain (NodeFloatAccount)
    participant N2 as Node 2

    Note over N1,N2: Tiếp nối 11.3 — Transfer đã confirm, tiền đã ở FA[2]

    alt Node 2 phát hiện lỗi (B không hợp lệ / Contract revert)
        N2->>RA: Đánh dấu MessageID = Claimed TRƯỚC (#12)\n(kiểm tra chưa Claimed trước đó, chống hoàn 2 lần)
        N2->>RA: Transfer ngược: FA[2] -= Value, FA[1] += Value\n(CHỈ Value — KHÔNG gửi lại GasFee, #6)
        RA-->>N1: Xác nhận — tiền đã về lại FA[1]
        N1->>N1: Cộng trả cho User A
    else Node 2 còn sống nhưng KẸT/CHẬM xử lý quá timeout (#15, MỚI)
        N1->>RA: Reclaim: kiểm tra MessageID CHƯA Claimed?
        RA-->>N1: Chưa Claimed -> cho Reclaim: FA[2] -= V, FA[1] += V
        Note over N1,N2: Nếu N2 vừa kịp Claimed đúng lúc N1 Reclaim,\nParent Chain xử lý tuần tự — chỉ 1 bên thắng, không double
        N1->>N1: Cộng trả cho User A
    end
```

**Tóm tắt bằng lời:** Có 2 tình huống cần hoàn tiền. Nếu Node 2 chủ động phát hiện lỗi (người nhận sai, hợp đồng lỗi), nó tự đánh dấu đã xử lý xong rồi chuyển khoản ngược lại — phần phí tính toán không được hoàn vì Node 2 đã thực sự tốn công. Tình huống thứ hai mới hơn: nếu Node 2 vẫn còn sống nhưng vì lý do gì đó không xử lý quá lâu, Node 1 không cần chờ Node 2 hợp tác — có thể tự đòi lại tiền thẳng từ Parent Chain sau khi hết thời gian chờ, miễn là Node 2 chưa kịp đánh dấu "đã xử lý" giao dịch đó. Nếu cả hai xảy ra gần như cùng lúc, chỉ một bên thắng, không bao giờ có chuyện tiền bị xử lý 2 lần.

### 11.5. Node chết — 1 bài toán PHÂN BỔ (không còn 2 trường hợp A/B)

```mermaid
flowchart TD
    Dead["RecoveryCommittee xác nhận\n1 node chết hẳn (mục 6.2)"] --> Total["TỔNG số dư node đó đã biết ngay,\ntự động, on-chain: chính FA[node] (mục 4.3)"]
    Total --> Inflight["Transfer đang bay tới node chết\nmà chưa Claimed"] --> Reclaim["Reclaim ngay lập tức\n(mục 3.6, không chờ timeout thường)"]
    Total --> Distrib["Câu hỏi còn lại: TIỀN NÀY CỦA AI\n(phân bổ theo từng user)?"]
    Distrib --> Pipeline["Snapshot + chống DA-Withholding\n+ Delay 72h (mục 6.3)\n— chứng minh PHÂN BỔ, không phải TỔNG"]

    style Total fill:#d4edda,color:#333
    style Pipeline fill:#fff3cd,color:#333
```

**Tóm tắt bằng lời:** Khi 1 node chết hẳn, KHÔNG còn phải phân loại "tiền đã nạp quỹ hay chưa" — vì mọi tiền của user LUÔN nằm sẵn trong tài khoản Float của node đó trên Parent Chain (mục 4.3), nên tổng số biết ngay, tự động, không cần bằng chứng gì thêm. Có 2 việc cần làm: (1) nếu đang có Transfer bay tới node này mà chưa được đánh dấu nhận, đòi lại ngay bằng Reclaim, không cần chờ hết timeout thường. (2) câu hỏi khó duy nhất còn lại là AI sở hữu bao nhiêu trong tổng số đó — vì chỉ node đã chết mới biết chi tiết từng user, nên vẫn cần đúng quy trình sao lưu (Snapshot) + chống giấu dữ liệu + chờ 72 giờ như thiết kế cũ, nhưng giờ mục đích là chứng minh PHÂN BỔ cho từng user, không phải chứng minh tổng số tiền có thật hay không.

### 11.6. Migration Account (CHỈ User)

```mermaid
sequenceDiagram
    participant Old as Node cũ
    participant PC as Parent Chain (Account Registry)
    participant New as Node mới

    Note over Old,New: Chỉ User Account. Contract KHÔNG migrate (mục 5.2/5.3).

    Old->>Old: FREEZE — khoá tx nội bộ mới, giữ request đến trong hàng đợi
    Old->>Old: EXPORT — đóng gói balance+nonce, ký QuorumCert
    Old->>New: Gửi gói state + cert
    New->>New: IMPORT — verify, nạp state
    New->>PC: Báo import xong
    PC->>PC: FLIP con trỏ Account Registry\n(CHỈ SAU KHI xác nhận import xong)
    New-->>Old: Xác nhận flip xong
    Old->>New: Relay tiếp các request đã giữ ở bước FREEZE
```

---

## 12. Câu hỏi thường gặp (Q&A)

### Q1. Làm sao tránh 1 account đăng ký cùng lúc ở 2 node khác nhau?

3 lớp chặn: (1) chữ ký ECDSA thật của chính `user_address` (chặn squatting, không chặn chính chủ tự gửi 2 nơi), (2) quyền quyết định cuối cùng nằm ở ghi vào Account Registry trên Parent Chain (transaction xử lý tuần tự = điểm serialize duy nhất, node thua bị Parent Chain từ chối on-chain), (3) node không coi admin-confirm cục bộ là đã sở hữu chắc chắn — phải đợi Parent Chain xác nhận, nếu bị từ chối phải rollback trạng thái cục bộ. Cần thêm job reconciliation định kỳ đối chiếu Account Registry thật, phòng trường hợp thông báo từ chối bị mất do mất kết nối.

### Q2. Vì sao chuyển sang Node Float Account thay vì giữ mô hình bond?

Vì mô hình bond cần cả 1 pipeline nặng (Snapshot+Velocity+Delay 72h+chống DA-Withholding) cho MỌI giá trị cross-node đang treo, kể cả phần chưa từng gặp sự cố gì. Float Account biến `NodeFloatAccount` thành 1 bất biến tự động — luôn bằng đúng tổng số dư user của node đó, không có bước "nạp quỹ" rời rạc nào để khai khống (mục 3.2) — nên TỔNG số tiền của node luôn có thật 100%, tự động kiểm chứng được (mục 4.3), không cần tin tưởng gì cả. Pipeline nặng đó giờ chỉ còn cần cho 1 việc hẹp hơn hẳn: chứng minh PHÂN BỔ theo từng user khi node chết (mục 6.3), không còn phải chứng minh tổng số có thật hay không.

### Q3. Mô hình mới có loại bỏ hoàn toàn rủi ro node khai khống không?

Rủi ro khai khống TỔNG SỐ bị loại bỏ hoàn toàn — không còn bước "nạp quỹ" nào để node tự khai báo số dư, `NodeFloatAccount` luôn khớp thật với tiền user do chính cơ chế ghi sổ atomic (mục 3.2, 4.3). Rủi ro còn lại chỉ nằm ở PHÂN BỔ: node vẫn có thể khai khống ai-sở-hữu-bao-nhiêu trong nội bộ sổ cục bộ của mình (ví dụ khi node chết, cố khai user X sở hữu nhiều hơn thực tế) — đây là đúng vấn đề mà Snapshot + chống DA-Withholding + Delay 72h (mục 6.3, mục 4.1b) xử lý.

### Q4. Vì sao không cần Contract Registry toàn cục?

Người gửi luôn phải tự biết trước contract đích nằm ở node nào — kiểm tra tồn tại cục bộ tại đích là đủ, không cần Parent Chain tra cứu hộ. Xem mục 5.2.

### Q5. `RecoveryCommittee` là gì, vì sao quan trọng?

Thực thể BLS committee cố định, set 1 lần từ config lúc triển khai, duy nhất có quyền: tuyên bố 1 node chết + tịch thu bond, hoặc thay hẳn khoá ký của bất kỳ node nào. Chưa được định nghĩa trong tài liệu này (ai, bao nhiêu người, ngưỡng quorum) — chặn cứng go-live vì code bắt buộc cần config này mới chạy được. Xem mục 8 #8.

---

## 13. Luồng xử lý nội bộ tại 1 Node

### 13.1. Hai loại giao dịch

- **Nội bộ** (cùng node): xử lý ngay, không qua BLS/Parent Chain — chiếm đa số giao dịch thực tế.
- **Cross-node**: qua Transfer atomic (mục 3.3) — đơn giản hơn hẳn bản trước vì không còn 3 bước gom-batch-ký-gửi-chờ-claim.

### 13.2. Giao dịch nội bộ

1. Request đến `AccountHandler`.
2. Xác thực chữ ký người gửi.
3. Kiểm tra số dư/nonce.
4. Ghi 1 lần nguyên tử: trừ người gửi, cộng người nhận.
5. Trả kết quả ngay. Xong.

### 13.3. Giao dịch cross-node

1. **Kiểm tra balance (bình thường):** xác thực chữ ký + xác nhận balance cục bộ người gửi ≥ `V` — không còn bước tra `NodeFloatAccount` riêng, vì FA luôn ≥ balance cục bộ theo bất biến (mục 3.5). Chỉ cần serialize để tránh race giữa nhiều yêu cầu từ CÙNG 1 user.
2. Trừ balance cục bộ người gửi, **cùng 1 lần ghi** với việc tạo bản ghi "đang gửi" — không tách rời (tránh crash giữa chừng làm mất dấu).
   → Trạng thái local: `LOCAL_APPLIED_PENDING_SEND`.
3. Gửi Transfer atomic lên Parent Chain (mục 3.3) — **không cần bước gom batch/ký riêng như bản trước**, vì mỗi Transfer đã tự đủ điều kiện gửi ngay (committee = 1, chữ ký gộp luôn vào transaction).
   → Nếu gửi thất bại do mất kết nối: giữ nguyên trạng thái, vào Retry Queue, gửi lại đúng transaction đã có — không tạo transaction mới cho cùng 1 yêu cầu.
   → Khi Parent Chain xác nhận: trạng thái local: `SENT_CONFIRMED` — **tiền đã an toàn tuyệt đối tại đây**, khác bản trước (nơi phải chờ thêm cert từ phía đích mới coi là xong).
4. Nếu quá timeout mà chưa thấy Node đích đánh dấu `Claimed` (mục 3.6, #15): tự gửi Reclaim, không cần Node đích hợp tác.
5. Nhận thông báo Node đích đã `Claimed` (credit local xong hoặc hoàn tiền) — cập nhật `CONFIRMED_SUCCESS`/`CONFIRMED_REFUNDED` để làm audit trail. **Không còn ý nghĩa bảo mật cho phần giá trị** (tiền đã an toàn từ bước 3), nhưng giờ có thêm ý nghĩa vận hành: đây là điều kiện để KHÔNG tự động gửi Reclaim ở bước 4.

**Phía Node đích, khi thấy credit mới:** kiểm tra `MessageID` chưa có trong `ClaimedMessages` cục bộ (#13) → kiểm tra `B` hợp lệ → gửi transaction đánh dấu `Claimed` lên Parent Chain (#12, #15) [trạng thái local: `MARKED_CLAIMED_PENDING_CREDIT`] → rồi mới credit local cho B [trạng thái local: `CREDITED`] (hoặc gửi Transfer hoàn tiền nếu không hợp lệ/revert). ⚠️ **Phục hồi sau crash (mục 8 #16):** nếu node đích khởi động lại và thấy 1 `MessageID` đã `MARKED_CLAIMED_PENDING_CREDIT` cục bộ nhưng chưa `CREDITED`, phải tiếp tục credit ngay — không re-mark `Claimed` (đã làm rồi), không bỏ qua (tiền sẽ kẹt vĩnh viễn nếu bỏ qua).

### 13.4. Bảng trạng thái local (đã đơn giản hơn bản trước — bớt 3 trạng thái trung gian)

| Trạng thái | Ý nghĩa | Chuyển tiếp khi nào |
|---|---|---|
| `LOCAL_APPLIED_PENDING_SEND` | Đã trừ tiền cục bộ, chưa gửi Transfer lên Parent Chain | Khi gửi Transfer thành công |
| `SENT_CONFIRMED` | Parent Chain đã xác nhận Transfer — **tiền đã an toàn tuyệt đối** | Khi nhận thông báo `Claimed` từ đích, HOẶC khi tự gửi Reclaim vì quá timeout |
| `CONFIRMED_SUCCESS` / `CONFIRMED_REFUNDED` | Audit trail cuối, lưu vĩnh viễn | — |

So với mô hình cũ (5 trạng thái: `LOCAL_APPLIED_PENDING_BATCH` → `BATCHED_PENDING_SIGN` → `SIGNED_PENDING_SUBMIT` → `SUBMITTED_ATTESTED` → `CONFIRMED_*`), mô hình mới gọn hơn hẳn: không cần gom batch riêng, không cần tách bước ký khỏi bước gửi, và không còn cần `PENDING_FLOAT` (từng có ở 1 bản nháp giữa của v2) — vì không còn khái niệm "Float không đủ" để phải chờ, `NodeFloatAccount` luôn ≥ balance cục bộ theo bất biến (mục 3.5), nên bước kiểm tra chỉ còn là kiểm tra balance bình thường, không tách trạng thái riêng.

---

## 14. Trao đổi dữ liệu Node ↔ Parent Chain & Doanh thu

### 14.1. Node gửi gì lên Parent Chain, lấy gì về

**Gửi lên (tốn gas):** `RegisterChainViaStake`/`PostSecurityBond` (đăng ký, 1 lần), **User tự nạp tiền** (atomic cùng lúc balance cục bộ + `NodeFloatAccount`, không phải node "nạp quỹ" — mục 3.2), **Transfer** (mỗi giao dịch cross-node — mục 3.3, thay cho `AttestCommit` cũ), `AccountTreeRoot` snapshot (định kỳ, phục vụ chứng minh phân bổ khi node chết — mục 6.3), đăng ký/chuyển nhượng Account Registry.

**Lấy về (đọc, không tốn gas):** trạng thái `NodeFloatAccount` của chính mình, `ChainRegistry` (danh tính node khác), Account Registry (định tuyến), `DeadChains`.

### 14.2. Vai trò tổng thể của Parent Chain

1. Sổ danh bạ (account/contract thuộc node nào).
2. **Sổ quỹ thật (MỚI)** — giữ `NodeFloatAccount`, enforce trực tiếp không cho âm.
3. Nơi giữ bond + thực thi hình phạt (thu hẹp phạm vi, chỉ còn bảo vệ đăng ký/gian lận phân bổ — mục 4.1b).
4. Lưới an toàn khi node chết (dead-declare + claim TỔNG luôn tức thời và tự động — mục 4.3, chỉ còn cần pipeline nặng cho việc chứng minh phân bổ — mục 6.3).

### 14.3. Doanh thu Parent Chain — phân tích, chưa chốt

| # | Nguồn | Đã có sẵn? | Ghi chú |
|---|---|---|---|
| 1 | **Gas fee cơ bản** mọi giao dịch (đăng ký, nạp tiền, Transfer, snapshot...) | ✅ Có sẵn | Nguồn thu tự nhiên nhất, không cần thiết kế thêm |
| 2 | Phí đăng ký 1 lần (tách khỏi bond) | ❌ Chưa có | Cần thêm khoản phí không hoàn lại riêng |
| 3 | **Phí giữ quỹ / lãi suất trên `NodeFloatAccount` (MỚI, phát sinh từ mô hình Float)** | ❌ Chưa có | Parent Chain giờ thực sự custody tiền thật của user (không chỉ của node) — có thể tính phí custody định kỳ (như phí giữ tài khoản ngân hàng) hoặc chia sẻ lãi nếu Parent Chain đầu tư phần float nhàn rỗi (rủi ro thanh khoản cần cân nhắc kỹ nếu làm việc này, vì FA giờ PHẢI luôn ≥ Σ balance user theo bất biến mục 3.5 — không còn "phần nhàn rỗi" thật sự an toàn để đầu tư mà không phá bất biến) |
| 4 | Phí dịch vụ Snapshot Archival (chứng minh phân bổ khi node chết — mục 6.3) | ❌ Chưa có | Mô hình "bảo hiểm lưu trữ" |
| 5 | Phí ưu tiên xử lý | ❌ Chưa có, chỉ cần khi hệ thống lớn | Đầu cơ |

**Khuyến nghị:** #1 đủ cho giai đoạn ra mắt; #3 (phí custody đơn thuần) vẫn khả thi, nhưng phần "đầu tư float nhàn rỗi" của ý tưởng này nay không còn hợp lý — bất biến mục 3.5 nghĩa là không có "phần nhàn rỗi" nào tách rời khỏi tiền user để đầu tư mà không phá vỡ chính bất biến đó.
