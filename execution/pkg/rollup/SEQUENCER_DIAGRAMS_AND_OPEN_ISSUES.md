# Sơ đồ Luồng & Vấn đề Còn Mở — BLS Node / Node Float Account

> Tách từ `SEQUENCER_DESIGN.md` (mục 8, 9.1, 11 gốc) để tài liệu chính gọn hơn, tập trung vào kiến trúc. File này chỉ chứa (1) toàn bộ sơ đồ minh hoạ các luồng chính và (2) danh sách vấn đề/quyết định còn mở — không lặp lại nội dung thiết kế, chỉ tham chiếu ngược bằng số mục (ví dụ "mục 3.3") sang `SEQUENCER_DESIGN.md`. Các trích dẫn dạng `#N` (ví dụ `#12`) trong cả 2 file đều trỏ vào đúng bảng "1. Danh sách vấn đề" ở file này.

---

## Phần A — Sơ đồ các luồng chính

### A.1. Kiến trúc tổng quan

```mermaid
flowchart TB
    subgraph RA["Parent Chain (Root Anchor)"]
        AR["Account Registry\nuser_address -> chainID"]
        CR["ChainRegistry\n1 entry = 1 node"]
        FA["NodeFloatAccount\nchainID -> balance THẬT\n(bất biến = Σ balance user, mục 3.2)"]
        SB["SecurityBondLedger\n(bảo vệ đăng ký/gian lận phân bổ, mục 4.1)"]
        RC["RecoveryCommittee\n(chưa định nghĩa — #7)"]
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

**Quy trình từng bước:**
1. **Lưu trữ dữ liệu:** Mỗi Node thực thi (Node 1, Node 2) tự lưu trữ và quản lý số dư của User trên cơ sở dữ liệu cục bộ (LevelDB) của riêng mình.
2. **Sổ quỹ Parent Chain:** Thay vì giữ chi tiết từng User, Parent Chain tạo ra một "Sổ quỹ liên-node" (`NodeFloatAccount`). Tại đây, mỗi Node có 1 tài khoản tổng.
3. **Bất biến của Quỹ:** Số dư quỹ của 1 Node trên Parent Chain luôn tự động **≥** tổng số dư của tất cả User thuộc Node đó, và hội tụ về đúng bằng (chênh lệch chỉ tồn tại trong 1 cửa sổ ngắn watch-rồi-credit, luôn lệch theo chiều an toàn — mục 3.2, 3.5). Tiền chảy vào quỹ chỉ thông qua 2 đường: User tự nạp vào trực tiếp, hoặc nhận chuyển khoản từ Node khác.
4. **Giao dịch liên-node:** Khi cần chuyển tiền giữa các Node, hệ thống chỉ cần trừ quỹ Node gửi và cộng quỹ Node nhận ngay trên Parent Chain (giống hệt thanh toán bù trừ liên ngân hàng).

### A.2. Tra cứu Account/Contract → Node quản lý

```mermaid
flowchart LR
    Q["Cần gửi tới address X"] --> A{"User account\nhay Contract?"}
    A -- "User" --> B["Tra Account Registry\nuser_address -> chainID"]
    A -- "Contract" --> C["Người gửi tự biết chainID\ntừ trước — không tra Parent Chain"]
    B --> D["ChainRegistry: chainID -> NodeBlsPublicKey"]
    C --> D
    D --> E["Node đích tự kiểm tra tồn tại cục bộ\ntrước khi credit (mục 3.3 bước 4)"]
```

**Quy trình từng bước:**
1. **Xác định đối tượng:** Khi User gửi yêu cầu đến 1 địa chỉ đích (X), hệ thống sẽ phân loại xem X là User Account (tài khoản người dùng) hay Smart Contract (hợp đồng).
2. **Trường hợp User Account:** Hệ thống truy vấn `Account Registry` trên Parent Chain để biết ví của User X đang nằm ở Node (`chainID`) nào.
3. **Trường hợp Smart Contract:** Hệ thống KHÔNG cần tra cứu Parent Chain. Người gửi phải tự biết trước và chỉ định rõ Contract đích đang nằm ở Node nào.
4. **Xác thực Node & Giao dịch:** Sau khi có được `chainID` đích, hệ thống lấy khoá công khai của Node đích từ `ChainRegistry` để chuẩn bị liên lạc. Tại đích, Node nhận sẽ tự kiểm tra lại lần cuối xem X có thực sự tồn tại trong dữ liệu của nó hay không trước khi cộng tiền.

### A.3. Chuyển giá trị Cross-Node — THÀNH CÔNG

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
    N2->>N2: Kiểm tra MessageID chưa có trong\nClaimedMessages cục bộ (#10)
    N2->>N2: Kiểm tra local: B có hợp lệ không?
    N2->>RA: Đánh dấu MessageID = Claimed (#9, #12)\nTRƯỚC KHI credit local
    N2->>B: Hợp lệ -> cộng balance cục bộ cho B

    Note over N1,N2: Tiền đã an toàn ngay khi Transfer confirm (bước ①-②).\nBước "Claimed" chỉ phối hợp chống trùng/Reclaim (mục 3.6),\nkhông phải điều kiện an toàn giá trị.
```

**Quy trình từng bước:**
1. **Khởi tạo:** User A (thuộc Node 1) yêu cầu chuyển tiền cho User B (thuộc Node 2).
2. **Trừ tiền cục bộ:** Node 1 kiểm tra xem User A có đủ tiền trong database cục bộ hay không. Nếu đủ, Node 1 lập tức trừ tiền của A.
3. **Ghi sổ Parent Chain:** Cùng lúc đó, Node 1 gửi 1 giao dịch Atomic lên Parent Chain để báo: Trừ quỹ của Node 1 và Cộng quỹ cho Node 2. Ngay khi giao dịch này xác nhận, **tiền đã thật sự nằm ở Quỹ của Node 2**.
4. **Bắt sóng & Kiểm tra:** Node 2 theo dõi Parent Chain và phát hiện có tiền gửi đến. Nó tự kiểm tra chống trùng lặp (`MessageID`) và xác minh User B có hợp lệ trong mạng của nó hay không.
5. **Ghi nhận & Hoàn tất:** Nếu B hợp lệ, Node 2 gửi tín hiệu báo cáo "Đã Claimed" lên Parent Chain để chốt sổ. Ngay sau đó, Node 2 cộng tiền vào số dư cục bộ của User B. Mọi thứ hoàn tất.

### A.4. Thất bại, Hoàn tiền & Reclaim-theo-timeout

```mermaid
sequenceDiagram
    participant N1 as Node 1
    participant RA as Parent Chain (NodeFloatAccount)
    participant N2 as Node 2

    Note over N1,N2: Tiếp nối A.3 — Transfer đã confirm, tiền đã ở FA[2]

    alt Node 2 phát hiện lỗi (B không hợp lệ / Contract revert)
        N2->>RA: Đánh dấu MessageID = Claimed TRƯỚC (#9)\n(kiểm tra chưa Claimed trước đó, chống hoàn 2 lần)
        N2->>RA: Transfer ngược: FA[2] -= Value, FA[1] += Value\n(CHỈ Value — KHÔNG gửi lại GasFee, #5)
        RA-->>N1: Xác nhận — tiền đã về lại FA[1]
        N1->>N1: Cộng trả cho User A
    else Node 2 còn sống nhưng KẸT/CHẬM xử lý quá timeout (#12)
        N1->>RA: Reclaim: kiểm tra MessageID CHƯA Claimed?
        RA-->>N1: Chưa Claimed -> cho Reclaim: FA[2] -= V, FA[1] += V
        Note over N1,N2: Nếu N2 vừa kịp Claimed đúng lúc N1 Reclaim,\nParent Chain xử lý tuần tự — chỉ 1 bên thắng, không double
        N1->>N1: Cộng trả cho User A
    end
```

**Quy trình từng bước:**

**Kịch bản 1: Thất bại (Tài khoản sai hoặc Contract bị lỗi)**
1. Mặc dù tiền đã vào Quỹ của Node 2, nhưng khi Node 2 kiểm tra nội bộ lại phát hiện User B không tồn tại (hoặc Contract đích bị Revert do lỗi).
2. Node 2 lập tức đánh dấu "Đã Claimed" trên Parent Chain để chốt giao dịch (không cho xử lý lại).
3. Node 2 chủ động tạo một giao dịch **chuyển ngược tiền** (`Value`) về lại Quỹ của Node 1. (Lưu ý: Tiền Gas bị tịch thu để chống Hacker spam giao dịch lỗi).
4. Node 1 nhận lại tiền và cộng hoàn trả vào số dư của User A.

**Kịch bản 2: Reclaim (Node 2 bị treo/chậm)**
1. Node 2 nhận được tiền vào Quỹ, nhưng vì lý do nào đó (treo máy, quá tải) không chịu xử lý sau một thời gian dài (Timeout).
2. Node 1 chủ động gửi lệnh "Đòi tiền" (Reclaim) trực tiếp lên Parent Chain mà không cần chờ Node 2 đồng ý.
3. Parent Chain kiểm tra xem Node 2 đã đánh dấu "Claimed" chưa. Nếu chưa, Parent Chain tự động trừ tiền từ Quỹ Node 2 và trả lại Quỹ Node 1.
4. *(Chặn Race Condition)*: Nếu Node 2 đột nhiên tỉnh dậy và đánh dấu Claimed CÙNG LÚC với lệnh Reclaim của Node 1, Parent Chain sẽ xử lý tuần tự: Lệnh nào đến trước sẽ thắng, đảm bảo không bao giờ bị xử lý đúp.

### A.5. Node chết — bài toán PHÂN BỔ

```mermaid
flowchart TD
    Dead["RecoveryCommittee xác nhận\n1 node chết hẳn (mục 6.2)"] --> Total["TỔNG số dư node đó đã biết ngay,\ntự động, on-chain: chính FA[node] (mục 4.3)"]
    Total --> Inflight["Transfer đang bay tới node chết\nmà chưa Claimed"] --> Reclaim["Reclaim ngay lập tức\n(mục 3.6, không chờ timeout thường)"]
    Total --> Distrib["Câu hỏi còn lại: TIỀN NÀY CỦA AI\n(phân bổ theo từng user)?"]
    Distrib --> Pipeline["Snapshot + chống DA-Withholding\n+ Delay 72h (mục 6.3)\n— chứng minh PHÂN BỔ, không phải TỔNG"]

    style Total fill:#d4edda,color:#333
    style Pipeline fill:#fff3cd,color:#333
```

**Quy trình từng bước:**
1. **Xác nhận Node tử vong:** `RecoveryCommittee` (Hội đồng phục hồi) chính thức xác nhận một Node đã chết hoàn toàn (offline vĩnh viễn).
2. **Chốt Quỹ tổng:** Quỹ Float Account của Node đó trên Parent Chain lập tức bị đóng băng. Vì toàn bộ tiền thật luôn nằm sẵn ở Quỹ này, hệ thống bảo toàn 100% tổng tài sản.
3. **Cứu hộ giao dịch treo:** Những giao dịch đang chuyển dở dang tới Node chết sẽ được Node nguồn kích hoạt lệnh `Reclaim` để đòi lại ngay lập tức (không cần đợi hết hạn Timeout).
4. **Bài toán Phân bổ:** Vấn đề duy nhất còn lại là số tiền lớn trong Quỹ thuộc về những User nào (vì danh sách số dư chi tiết nằm ở Node đã chết).
5. **Chứng minh & Giải ngân:** Hệ thống sử dụng bản sao lưu (Snapshot) định kỳ do Archival Service nắm giữ. Sau thời gian thử thách 72 giờ (để chống hacker tung Snapshot giả giấu dữ liệu), tiền từ Quỹ sẽ được phân bổ lại và giải ngân đúng cho từng User.

### A.6. Migration Account (CHỈ User) — có thể hoãn, xem `SEQUENCER_DESIGN.md` mục 5.3

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

**Quy trình từng bước:**
1. **Khoá Tài khoản (Freeze):** Node cũ tiến hành khoá tài khoản User cần chuyển mạng. Các giao dịch nội bộ mới bị chặn, nhưng các giao dịch từ nơi khác gửi đến vẫn được Node cũ lưu tạm vào một hàng đợi chờ xử lý.
2. **Đóng gói & Ký xác nhận (Export):** Node cũ đóng gói số dư và nonce của User, tự ký xác nhận (QuorumCert) chốt dữ liệu cuối cùng, rồi gửi sang Node mới.
3. **Nhập & Xác thực (Import):** Node mới nhận gói dữ liệu, kiểm tra chữ ký và nạp số dư vào cơ sở dữ liệu nội bộ của mình.
4. **Cập nhật Sổ Danh Bạ:** Sau khi nạp thành công, Node mới báo cáo lên Parent Chain để cập nhật lại `Account Registry` (chuyển địa chỉ ví sang quản lý bởi Node mới).
5. **Xử lý Tồn đọng:** Node mới thông báo cho Node cũ biết đã sang tên đổi chủ xong. Node cũ tiến hành chuyển tiếp (Relay) toàn bộ các giao dịch đang nằm trong hàng đợi ở Bước 1 sang Node mới để xử lý nốt.

---

## Phần B — Vấn đề & Quyết định còn mở

### B.1. Danh sách vấn đề logic/bảo mật còn hiệu lực

| # | Vấn đề | Rủi ro cụ thể | Xử lý |
|---|---|---|---|
| 1 | Committee mỗi node = 1 (chính nó) — không có redundancy signer thật | `QuorumCert` về bản chất là chữ ký đơn | Phòng thủ không nằm ở số lượng chữ ký mà ở giới hạn thiệt hại: velocity-limit cho Transfer OUTFLOW khi khoá bị lộ (mục 4.4) + Snapshot/Delay 72h khi node chết hẳn (mục 6.3) |
| 2 | Account Registry cho phép ghi đè mapping tuỳ ý nếu không kiểm soát | Report cũ/replay có thể "cướp" account sang node khác | Chỉ chấp nhận đăng ký lần đầu hoặc chữ ký của node hiện tại để chuyển nhượng (mục 5.1, 5.3) |
| 3 | Contract tự sinh (deploy trong node) không thể đăng ký registry toàn cục | DoS/state-bloat lên Parent Chain nếu bắt đăng ký như Account | Bỏ hẳn Contract Registry toàn cục; dùng `chainID` đích tường minh từ người gửi + kiểm tra tồn tại cục bộ (mục 5.2) |
| 4 | Chuyển nhượng account (Migration) không atomic nếu không thiết kế kỹ — **có thể không phát sinh nếu Migration (mục 5.3) hoãn triển khai** | Message đến đúng lúc đang chuyển giao có thể bị kẹt/mất | Giao thức 3 pha Freeze → Export & Attest → Import & flip con trỏ (mục 5.3) |
| 5 | `GasFee` bị hoàn nhầm khi giao dịch cross-node thất bại | Spam-revert trở thành DoS miễn phí lên node đích | Transfer hoàn tiền (mục 3.4) CHỈ hoàn `Value`, không hoàn `GasFee` |
| 6 | Node chết hẳn — Parent Chain biết TỔNG tiền thật nhưng không biết PHÂN BỔ cho user nào bao nhiêu | Node có thể khai khống PHÂN BỔ (gán hết cho ví nó kiểm soát) dù tổng đã chắc chắn đúng | Snapshot & Archival Pipeline + chống DA-Withholding + Withdrawal Delay 72h — bảo vệ đúng rủi ro phân bổ (mục 6.3) |
| 7 | **[CÒN MỞ]** `RecoveryCommittee` — thực thể duy nhất có quyền tuyên bố node chết, tịch thu bond, thay khoá ký bất kỳ node nào — chưa được định nghĩa | Lộ/compromise `RecoveryCommittee` ảnh hưởng TOÀN hệ thống, nặng hơn lộ 1 node đơn lẻ | Cần đội xác định thành viên, ngưỡng quorum, quy trình bảo vệ khoá — quyết định tổ chức thật, không tự đề xuất được (mục B.2, còn mở) |
| 8 | Custody 100% device key (PKS) — Node bị hack có thể ký giao dịch nội bộ giả | Mất tiền không để lại bằng chứng mật mã, ngoài phạm vi bảo vệ của Float Account (tiền không rời node) | Không giải quyết triệt để bằng kỹ thuật — giảm thiểu bằng delay/anomaly-detection/non-custodial tuỳ chọn (mục 2.3); phục hồi thật qua `UpdateCommitteeWithRecoveryCert` (mục 6.2) |
| 9 | Reverse Transfer (hoàn tiền, mục 3.4) không có cơ chế chống gửi 2 lần | Crash giữa chừng rồi retry có thể gửi hoàn tiền 2 lần, tự bào mòn quỹ của chính node đang hoàn tiền | Đánh dấu `MessageID` = `Claimed` trên Parent Chain TRƯỚC khi gửi hoàn tiền, kiểm tra lại trạng thái này trước khi retry (mục 3.3 bước 7, mục 3.4 bước 2) |
| 10 | Node đích không có cơ chế chống xử lý trùng 1 credit đến | Node đích có thể credit local 2 lần cho cùng 1 `MessageID` nếu logic theo dõi/watcher bị lỗi hoặc quét lại block cũ | Node đích tự giữ bảng "MessageID đã xử lý", kiểm tra trước khi làm bất cứ gì (mục 3.3 bước 5) |
| 11 | **[NGHIÊM TRỌNG]** Không có velocity-limit cho Transfer, dù đúng là không cần để chống mint sai — bỏ sót vai trò giới hạn thiệt hại khi KHOÁ KÝ node bị lộ | Node bị lộ khoá có thể bị rút sạch TOÀN BỘ Float Account trong 1 giao dịch tức thời — nặng hơn cả #8 (custody local) vì FA ≈ tổng tiền của MỌI user node đó | Tái áp dụng velocity-limit cho Transfer OUTFLOW — circuit-breaker chống lộ khoá, không phải hard-cap chống gian lận (mục 4.4) |
| 12 | Không có timeout nếu node đích còn sống nhưng kẹt/chậm xử lý 1 credit đã nhận (khác node chết hẳn, không cần `RecoveryCommittee`) | Tiền nằm im ở Float Account của đích, User A gốc không được phục vụ cũng không được hoàn, không có điểm dừng theo thời gian | Cơ chế Reclaim: quá timeout mà `MessageID` chưa `Claimed`, Node 1 tự reclaim thẳng từ Parent Chain không cần Node 2 hợp tác — có chặn race với việc Node 2 vừa kịp `Claimed` (mục 3.6) |
| 13 | Node đích không có bước phục hồi nếu crash ĐÚNG GIỮA lúc đánh dấu `Claimed` (đã gửi lên Parent Chain) và lúc credit local cho B (chưa kịp làm) | Restart mà không kiểm tra đúng trạng thái này có thể credit local 2 lần, hoặc bỏ sót vĩnh viễn | Node đích cần state machine cục bộ riêng: `MARKED_CLAIMED_PENDING_CREDIT` → `CREDITED` — khi restart, nếu thấy `Claimed` trên Parent Chain nhưng local chưa ghi `CREDITED`, phải tiếp tục credit chứ không được re-mark `Claimed` cũng không được bỏ qua (mục 13.3) |
| 14 | Velocity-limit chống lộ khoá cho Transfer outflow (#11/mục 4.4) chưa nói rõ có loại trừ Hoàn tiền (mục 3.4)/Reclaim (mục 3.6) hay không | Nếu áp chung 1 ngưỡng cho cả 2 loại, 1 node đang bị tấn công spam-revert (#5) có thể bị chính circuit-breaker này chặn luôn cả việc hoàn tiền hợp lệ cho user vô tội đang chờ — DoS tầng 2 do chính cơ chế phòng thủ gây ra | Loại trừ tường minh: ngưỡng chỉ áp cho Transfer gửi MỚI (mục 3.3); Hoàn tiền/Reclaim luôn được miễn vì chỉ trả lại đúng giá trị đã thực nhận trước đó, không phải bề mặt tấn công mới (mục 4.4) |
| 15 | **[CÒN MỞ]** Không có cơ chế nào để user report "node thực thi sai" ở cấp giao dịch cá nhân — `RecoveryCommittee`/Snapshot Pipeline chỉ hoạt động cấp toàn-chain (node chết/double-sign), không có nhánh cho node vẫn sống nhưng tính sai/censorship/rollback 1 giao dịch | User bị hại không có kênh nào để khiếu nại có bằng chứng, cũng không có bằng chứng chống chối bỏ (response giao dịch hiện không được ký) | Signed Receipt (bắt buộc, nền tảng) + kênh report vận hành qua `RecoveryCommittee` (Hướng A) làm baseline trước go-live; fraud-proof đầy đủ qua publish tx log (Hướng B) để dành roadmap dài hạn — chi tiết đầy đủ + đánh đổi từng hướng ở mục 15 (`SEQUENCER_DESIGN.md`), **cần đội chọn hướng, không tự đề xuất được** |
| 16 | **[CÒN MỞ, MỚI]** Chưa xác nhận `SlashOnEquivocation` (permissionless, dựa trên `commitRoot` của cơ chế `BatchOutboundCommit` cũ) có bắt được double-sign `AccountTreeRoot` — artifact MỚI của thiết kế này — hay không (mục 4.1, mục 6.2) | Nếu không, node có thể double-sign `AccountTreeRoot` (2 bản phân bổ khác nhau cho cùng 1 chu kỳ) mà không bị phạt gì — lỗ hổng ngay tại đúng cơ chế được kỳ vọng bắt gian lận phân bổ | Cần xác nhận khi implement: hoặc wire `AccountTreeRoot` qua đúng con đường tạo `commitRoot` tương thích, hoặc xây cơ chế equivocation-detection riêng cho nó — không mặc định thừa hưởng miễn phí từ code cũ |

### B.2. Decision Log

| # | Câu hỏi | Quyết định | Căn cứ |
|---|---|---|---|
| Q(mô hình giá trị) | Chuyển hẳn sang Node Float Account hay giữ mô hình bond cũ? | **Đã chốt: chuyển hẳn**, phạm vi giới hạn ở quỹ liên-node (giao dịch nội bộ không đổi) | mục 3 |
| Q13 | 1 node = 1 chainID? | **Đã chốt** | mục 2.4 |
| Q3 | Committee mỗi node bao nhiêu validator? | **Chấp nhận = 1** (chính node) | mục 2.4 |
| Q(tần suất snapshot) | Bao nhiêu lâu 1 lần? | **Mặc định 15 phút**, có thể tăng sau khi đo chi phí thật — Snapshot phục vụ chứng minh PHÂN BỔ (mục 6.3) | mục 6.3 |
| Q(velocity Transfer outflow) | Ngưỡng circuit-breaker chống lộ khoá cho Transfer (mục 4.4)? | **Mặc định khởi điểm 20%/24h** — cần đội xác nhận lại theo traffic thật, không chặn triển khai ban đầu | mục 4.4, #11 |
| Q(timeout Reclaim) | Bao lâu thì Node 1 được phép Reclaim nếu Node 2 chưa `Claimed`? | Chưa có số tuyệt đối, cần đo chu kỳ xử lý bình thường thật trước khi chốt | mục 3.6, #12 |
| Q(RecoveryCommittee) | Ai ngồi trong đó, bao nhiêu người, ngưỡng quorum? | **CÒN MỞ THẬT SỰ** — quyết định tổ chức/nhân sự, không tự đề xuất được. **Chặn cứng go-live**: code không chạy được nếu thiếu config này | #7 |
| Q9-rủi-ro | Mức rủi ro custody PKS chấp nhận được với quy mô tài sản thật? | **CÒN MỞ** — khẩu vị rủi ro kinh doanh thật | mục 2.3 |
| Q(report node sai) | Hướng A (report vận hành) hay Hướng B (fraud-proof đầy đủ) cho #15? | **CÒN MỞ THẬT SỰ** — khuyến nghị Hướng A làm baseline (mục 15.5), nhưng cần đội chốt chính thức trước khi viết code | mục 15, #15 |
| Q(SlashOnEquivocation/AccountTreeRoot) | `SlashOnEquivocation` có bắt double-sign `AccountTreeRoot` không? | **CÒN MỞ, kỹ thuật** — cần xác nhận trong code khi implement, không phải quyết định business | mục 4.1, mục 6.2, #16 |

**4 mục còn mở thật sự, không tự đề xuất số được (3 mục đầu là quyết định tổ chức/kinh doanh, mục cuối là việc kỹ thuật cần xác nhận khi code):** `RecoveryCommittee` thành viên, Q9-rủi-ro (custody risk acceptance), Q(report node sai) — hướng A hay B, và Q(SlashOnEquivocation/AccountTreeRoot).
