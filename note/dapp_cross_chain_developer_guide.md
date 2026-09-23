# Metanode Cross-Chain: Hướng Dẫn Dành Cho DApp Developer

Tài liệu này hướng dẫn chi tiết từng bước cho các lập trình viên DApp cách thức gọi lệnh, chuyển tiền và thực thi hợp đồng thông minh xuyên chuỗi (Cross-chain Contract Call) trên hệ thống Metanode.

---

## 1. Tổng quan Kiến trúc Tương tác

Để chuyển tài sản hoặc gọi một contract ở chain khác, DApp (hoặc người dùng) **không giao tiếp trực tiếp với relayer**. Thay vào đó, bạn chỉ cần gửi một giao dịch (transaction) gọi hàm vào **Gateway Contract** có sẵn trên mọi chain của Metanode (private chain lẫn Root Anchor).

> **Địa chỉ cố định của Gateway Contract trên mọi chain:**
> `0x0000000000000000000000000000000000001002`

Gateway Contract này thực chất là một Native Precompile (viết bằng Go, xử lý qua "barrier transaction" — chạy tuần tự, không qua worker EVM song song) để đảm bảo tính đúng đắn tuyệt đối cho logic tài chính xuyên chuỗi, nhưng nó hỗ trợ chuẩn giao tiếp ABI giống hệt các Smart Contract Solidity thông thường — gọi qua `ethers.js`/`web3.js` như một contract bình thường.

Phần còn lại của pipeline (đợi BLS quorum cert từ uỷ ban chain nguồn, gọi `attestCommit()`/`claimMessage()` ở chain đích) do **RelayerDaemon** tự động xử lý hoàn toàn — dApp/người dùng không cần và không nên tự gọi các hàm đó. Bản thân bước "đợi quorum cert" này cũng được tự động hoá hoàn toàn ở tầng validator node qua 1 cơ chế vote/attestation BLS 2f+1 — xem mục 6 (Phụ lục) nếu bạn tò mò về cơ chế bên trong, dApp thông thường có thể bỏ qua hoàn toàn mục đó.

---

## 2. Gọi lệnh `outbound` (Gửi thông điệp xuyên chuỗi)

Để bắt đầu một lệnh cross-chain, DApp cần gọi hàm `outbound` trên Gateway Contract:

```solidity
function outbound(
    uint256 destChainId,
    address target,
    bytes calldata payload,
    uint256 assetId,
    uint256 value,
    uint256 tip,
    uint256 gasFee,
    uint8 hopCount,
    bool ordered
) external returns (bytes32 messageId);
```

**Lưu ý quan trọng:**
- Hàm này **`nonpayable`** — bạn **không** gửi ETH/native coin kèm giao dịch (`tx.value`/`msg.value` luôn phải là `0`). `value`/`tip`/`gasFee` là 3 tham số **uint256 riêng biệt** trong ABI; số dư tương ứng bị trừ thẳng từ tài khoản gửi qua kế toán nội bộ của Gateway (burn thật, không qua cơ chế `msg.value` của EVM).
- Giá trị trả về là **`bytes32 messageId`** (một hash) — đây chính là ID bạn cần để tra trạng thái sau này (mục 4). Vì `outbound` là 1 giao dịch ghi (không phải `eth_call`), bạn không lấy được `messageId` trực tiếp từ lệnh gọi trong `ethers.js` — phải lấy từ event `MessageSent` trong receipt (mục 4).

### Giải thích các tham số:

- **`destChainId` (uint256):** ID của chain đích muốn gửi đến (ví dụ: `102` nếu bạn đang ở chain `101`).
- **`target` (address):** Địa chỉ người nhận hoặc Smart Contract nhận ở chain đích.
  - Nếu chỉ chuyển tiền thuần túy: điền địa chỉ ví người dùng.
  - Nếu muốn gọi contract xuyên chuỗi: điền địa chỉ của Smart Contract đích **đã có code triển khai sẵn** ở chain đích — Gateway tự kiểm tra `target` có code hay không để quyết định có thực thi `payload` như 1 lệnh gọi contract hay không (một `target` không có code, dù `payload` khác rỗng, chỉ được xử lý như chuyển tiền thuần).
- **`payload` (bytes):** Dữ liệu mã hóa (calldata) sẽ được truyền sang contract ở chain đích.
  - Nếu chỉ chuyển tiền thuần túy (không gọi contract): gửi dữ liệu rỗng `0x`.
- **`assetId` (uint256):** ID của loại tài sản muốn chuyển. Chuyển Native Token mặc định của mạng lưới dùng `0`.
- **`value` (uint256):** Số lượng token muốn gửi sang chain đích. Số này bị **khoá/đốt thật** ở chain nguồn ngay khi `outbound()` thành công. **Lưu ý fail-closed quan trọng:** một chain mới, chưa từng NHẬN giá trị thật nào từ chain khác, có allocation gửi-ra = 0 theo thiết kế — `outbound()` vẫn khoá tiền thành công, nhưng bước `attestCommit()` (relayer tự làm) ở chain đích sẽ revert cho tới khi chain đích được cấp allocation. **Đây không phải việc dApp/người dùng tự làm được.** (Cập nhật 2026-09-04: toàn bộ `GovernanceEngine` propose/vote/timelock/execute đã bị xoá — xem `note/eurozone_unified_native_coin_plan.md`.) Hiện tại: `allocateSupplyWithCert` chỉ dùng để mint tổng cung genesis đúng 1 lần cho chính chain Reserve, tự-ký bởi uỷ ban BLS thật của Reserve, không qua vote; 1 chain mới nhận allocation bằng cách Reserve chuyển 1 phần cung đã mint cho nó qua `transferAllocationWithCert` — cũng tự-ký bởi uỷ ban Reserve, không cần vote/timelock. Nếu gặp tình huống chain đích chưa có allocation, liên hệ đội vận hành Root Anchor để họ thực hiện bước chuyển này (qua `register_chains -action transfer-alloc`), không phải lỗi của giao dịch bạn gửi.
- **`tip` (uint256):** Tiền thưởng (boa) cho Relayer để khuyến khích họ xử lý giao dịch này nhanh hơn. Nếu để `0`, relayer vẫn xử lý bình thường (relayer hiện tại tự động quét mọi message đang chờ) nhưng không có gì thưởng thêm.
- **`gasFee` (uint256):** Ngân sách Gas (native coin) bạn ứng trước để chi trả cho việc chạy Smart Contract tại chain đích khi `target` là 1 contract thật. Nếu chạy không hết, phần dư được hoàn trả tự động. Nếu chỉ chuyển tiền (`payload = 0x` hoặc `target` không có code), để `0`.
- **`hopCount` (uint8):** Bộ đếm số lần thông điệp này đã được định tuyến lại (dùng cho các luồng đi qua trạm trung gian/Reserve nhiều bước) — **không phải** "số chain phải đi qua". Với một message gửi **trực tiếp** giữa 2 chain (kể cả 2 private chain khác nhau), dùng `hopCount = 0`. Giới hạn tối đa (`MaxHopCount`) là `6`; vượt quá giá trị này giao dịch revert với `hop count exceeds maximum limit of 6`.
- **`ordered` (bool):** Nếu `true`, các thông điệp sẽ được đảm bảo xử lý theo đúng thứ tự gửi. Thường dùng `false` để tăng tối đa thông lượng (parallel execution).

---

## 3. Nhận diện người gửi thực sự ở Chain Đích

Khi `target` là một Smart Contract ở chain đích, contract này được Gateway gọi thông qua `call()`. `msg.sender` lúc này là địa chỉ của Gateway Contract (`0x00...001002`), **không phải** địa chỉ người dùng ban đầu.

> **⚠️ Đính chính (2026-09-23):** Phiên bản trước của mục này hướng dẫn gọi `IGateway(gatewayAddress).isCalledByGateway()`/`.getOriginalSender()` như 2 hàm ABI trên chính Gateway Contract. Đó là interface CŨ, đã **chết từ lâu trong production** (contract ở chain đích luôn thấy `ActiveContext` rỗng khi tự gọi ngược lại Gateway theo cách đó — Gateway engine load lại state mới từ storage cho MỖI transaction, không giữ `ActiveContext` xuyên transaction) và **hiện tại còn không thể gọi được nữa** — case dispatch cho 2 hàm này đã bị xoá khỏi `handleView` (dọn dẹp code chết, 2026-09-22), gọi sẽ revert với `unhandled gateway view method`. Cơ chế THẬT SỰ hoạt động (và luôn hoạt động) là 1 **Native Precompile riêng, KHÔNG nằm trên Gateway Contract** — xem bên dưới.

### Cơ chế đúng: `CROSS_CHAIN_CONTEXT` Precompile

```
Địa chỉ: 0x00000000000000000000000000000000B429C0B2
```

Đây là 1 precompile độc lập (khác hẳn `0x00...001002` của Gateway), chỉ trả dữ liệu khi contract của bạn đang thực sự được Gateway gọi bên trong `claimMessage()`/`verifyAndExecute()` (giai đoạn thực thi payload ở chain đích) — mọi lúc khác (kể cả khi bạn tình cờ có code trùng địa chỉ hoặc ai đó gọi trực tiếp không qua Gateway), cuộc gọi **thất bại hoàn toàn** (`success = false` ở tầng `CALL`, không phải trả về giá trị rỗng) — đây chính là cách thay thế `isCalledByGateway()` cũ: kiểm tra cờ `success` của chính lệnh gọi thấp cấp, không có hàm boolean riêng.

```solidity
address constant CROSS_CHAIN_CONTEXT = 0x00000000000000000000000000000000B429C0B2;

contract MyReceiver {
    function onCrossChainCall() external {
        (bool ok1, bytes memory ret1) =
            CROSS_CHAIN_CONTEXT.call(abi.encodeWithSignature("getOriginalSender()"));
        require(ok1, "not called via Gateway (no active cross-chain context)");
        address originalSender = abi.decode(ret1, (address));

        (bool ok2, bytes memory ret2) =
            CROSS_CHAIN_CONTEXT.call(abi.encodeWithSignature("getSourceChainId()"));
        require(ok2, "not called via Gateway (no active cross-chain context)");
        uint256 sourceChainId = abi.decode(ret2, (uint256));

        // ... logic của bạn, dùng originalSender/sourceChainId thay vì msg.sender
    }
}
```

**Lưu ý quan trọng:**
- Đây là **2 hàm riêng biệt** (`getOriginalSender()` trả về 1 `address`, `getSourceChainId()` trả về 1 `uint256`) — KHÔNG gộp chung thành 1 hàm trả 2 giá trị như interface cũ đã (sai) mô tả.
- Dùng `.call()` (không phải `.staticcall()`) — đây là cách đã được kiểm chứng thật (xem `pkg/mvm/ta_boundary_harness_test.go`, `TestTABoundary_CrossChain_SurvivesSerialization`); engine coi đây như 1 lệnh gọi bình thường trong luồng thực thi.
- Phòng thủ thêm (khuyến nghị, không bắt buộc): vẫn có thể kiểm tra thêm `msg.sender == 0x0000000000000000000000000000000000001002` (địa chỉ Gateway) song song với check `success` ở trên — cả 2 cùng đúng thì chắc chắn 100% đây là 1 cuộc gọi cross-chain thật do Gateway dispatch, không phải ai mạo danh.
- Context này chỉ tồn tại **trong đúng 1 lệnh gọi** (Gateway set ngay trước khi gọi `target`, tự xoá ngay sau khi gọi xong, kể cả khi payload revert) — không lưu lại giữa các transaction, không đọc được từ 1 giao dịch/contract không nằm trong chuỗi gọi đó.

---

## 4. Theo dõi trạng thái Giao dịch

### 4.1 Qua Event (khuyến nghị cho UI/Explorer lắng nghe thời gian thực)

Gateway Contract emit các event sau:

```solidity
event MessageSent(bytes32 indexed messageId, uint256 indexed destChainId, uint256 sequence);
event MessageStatusChanged(bytes32 indexed messageId, uint8 status);
```

- **`MessageSent`**: phát ra ngay khi `outbound()` thành công — đây là nơi bạn lấy `messageId` thật (không lấy được từ giá trị trả về của `outbound()` như đã nói ở mục 2, vì nó là 1 giao dịch ghi, không phải `eth_call`).
- **`MessageStatusChanged`**: phát ra mỗi khi trạng thái message thay đổi. `status` là `uint8`, ứng với:
  | Giá trị | Ý nghĩa |
  | :-- | :-- |
  | `0` | Pending (đang chờ relay) |
  | `1` | Success (đã claim thành công ở chain đích) |
  | `2` | Failed |
  | `3` | Refunded (đã hoàn tiền về chain nguồn) |

  Trạng thái hoàn tiền được báo qua chính `MessageStatusChanged(messageId, 3)`, lọc theo
  `status == 3` — không có event riêng cho việc hoàn tiền.

### 4.2 Qua gọi trực tiếp (view, không cần lắng nghe log lịch sử)

```solidity
function getMessageStatus(bytes32 messageId) external view returns (uint8 status);
```

Dùng khi bạn đã có `messageId` (từ `MessageSent`) và chỉ cần hỏi trạng thái hiện tại — tiện cho polling từ backend/UI mà không cần chạy 1 event indexer đầy đủ.

---

## 5. Ví dụ mã (Ethers.js v5)

```javascript
const gatewayAbi = [
  "function outbound(uint256 destChainId, address target, bytes calldata payload, uint256 assetId, uint256 value, uint256 tip, uint256 gasFee, uint8 hopCount, bool ordered) external returns (bytes32 messageId)",
  "function getMessageStatus(bytes32 messageId) external view returns (uint8 status)",
  "event MessageSent(bytes32 indexed messageId, uint256 indexed destChainId, uint256 sequence)",
];
const gatewayAddress = "0x0000000000000000000000000000000000001002";

const gatewayContract = new ethers.Contract(gatewayAddress, gatewayAbi, signer);

// Chuyển 100 token (native coin) sang Chain 102 cho ví 0xABC..., trực tiếp (không qua contract)
const tx = await gatewayContract.outbound(
    102,                              // destChainId
    "0xABC1230000000000000000000000000000000000", // target (ví người nhận, không phải contract)
    "0x",                             // payload rỗng (chỉ chuyển tiền)
    0,                                // assetId (0 = native coin)
    ethers.utils.parseEther("100"),   // value
    ethers.utils.parseEther("0.1"),   // tip cho relayer
    0,                                // gasFee = 0 vì không chạy contract
    0,                                // hopCount = 0 (gửi trực tiếp)
    false                             // ordered
);
// KHÔNG truyền {value: ...} vào lệnh gọi — outbound() là nonpayable.

const receipt = await tx.wait();

// Lấy messageId thật từ event, không phải từ giá trị trả về của outbound()
const sentEvent = receipt.events.find(e => e.event === "MessageSent");
const messageId = sentEvent.args.messageId;
console.log("Đã gửi lệnh cross-chain, messageId =", messageId);

// (Tuỳ chọn) Poll trạng thái sau đó
const status = await gatewayContract.getMessageStatus(messageId);
console.log("Trạng thái hiện tại:", status); // 0=Pending, 1=Success, 2=Failed, 3=Refunded
```

---

## 6. Phụ lục: Cơ chế Vote/Attestation (KHÔNG dành cho DApp thông thường)

> **DApp/người dùng cuối KHÔNG BAO GIỜ cần gọi bất kỳ hàm nào ở mục này.** Chỉ đọc mục này nếu bạn
> đang xây dựng: (a) chính phần mềm validator node của Metanode, (b) 1 relayer/aggregator tự viết
> thay thế `RelayerDaemon` có sẵn, hoặc (c) 1 dashboard vận hành cần hiển thị tiến độ quorum. Toàn
> bộ phần này Gateway Contract đã có sẵn ABI, dùng chung interface `ethers.js`/`web3.js` như mục 2,
> chỉ liệt kê ở đây cho đầy đủ tài liệu.

### 6.1 Vì sao cần "vote"?

Metanode không tin bất kỳ 1 validator đơn lẻ nào — mọi thay đổi trạng thái xuyên chuỗi quan trọng
(xoay vòng uỷ ban, xác nhận 1 batch commit thật sự tồn tại, xác nhận 1 message đã thực thi thành
công/thất bại ở chain đích) đều cần **2f+1 stake thật sự đồng ý**, chứng minh bằng chữ ký BLS thật
của từng validator ("attestation share" = 1 phiếu vote), tổng hợp lại thành 1 `QuorumCert` trước khi
được chấp nhận. Đây chính là nguyên tắc "Zero-Fork Invariant" của dự án (xem `AGENTS.md`): **không
bao giờ dispatch dựa trên timeout/ước lượng, chỉ dispatch khi có bằng chứng data-driven rằng đủ
2f+1 peer đồng ý**.

Trong thực tế production, **bạn không cần tự vận hành bước này** — mỗi validator node tự động ký và
gọi các hàm `submit*Attestation` bên dưới ngay khi node đó tự quan sát được sự kiện tương ứng (batch
mới commit xong, message vừa thực thi xong, epoch mới sẵn sàng...), và `RelayerDaemon` tự động poll
+ tổng hợp chữ ký + gửi `QuorumCert` kèm giao dịch thật (`attestCommit`/`refund`/
`creditReserveAllocation`/`committeeUpdate`...). Mục này chỉ để tham khảo nếu bạn cần tự xây lại 1
phần trong số đó.

### 6.2 Ngưỡng quorum (áp dụng cho MỌI loại vote bên dưới)

```
threshold = (2 * totalStake) / 3 + 1        // mặc định (2f+1 an toàn cho mọi totalStake)
threshold = (totalStake * QuorumThreshold + 9999) / 10000   // nếu chain đăng ký QuorumThreshold riêng (đơn vị: phần vạn, vd 6667 = 66.67%)
```

`totalStake` là tổng stake của **uỷ ban hiện tại đang đăng ký trên chính chain đó** (`ChainRegistry.Committee`), không phải tổng toàn mạng. `Sig` phải đến từ đúng những public key nằm trong uỷ ban tại epoch được vote, kiểm tra qua `registerCommitteePop` (mục 6.3) trước.

### 6.3 `registerCommitteePop` — Đăng ký Proof-of-Possession

```solidity
function registerCommitteePop(bytes calldata pubkeyBls, bytes calldata popSignature) external;
function getRegisteredPop(bytes calldata pubkeyBls) external view returns (bytes memory popSignature);
```

Bất kỳ ai cũng gọi được (permissionless, tự xác thực qua `PopVerify` — không cần đã là thành viên
uỷ ban). Một public key BLS **phải** có PoP đăng ký sẵn ở đây trước khi có thể xuất hiện trong danh
sách uỷ ban mới của 1 lần `committeeUpdate` (mục 6.4) — PoP không bao giờ được tin trực tiếp từ
calldata của chính giao dịch `committeeUpdate`, để chặn kẻ tấn công giả mạo 1 PoP "trông hợp lệ" cho
1 khoá họ không thực sự giữ private key.

### 6.4 Vote xoay vòng uỷ ban — `submitCommitteeAttestation` + `committeeUpdate`

```solidity
function submitCommitteeAttestation(
    uint256 sourceChainId, uint64 oldEpoch, bytes32 payloadHash,
    bytes calldata signerPubkeyBls, bytes calldata signature
) external;

function getCommitteeAttestationShares(uint256 sourceChainId, uint64 oldEpoch, bytes32 payloadHash)
    external view returns (bytes[] memory pubkeys, bytes[] memory signatures);
```

- `payloadHash` = digest xác định 1 lần rotate cụ thể (chain, epoch mới, uỷ ban mới, state root,
  account tree root) — bất kỳ khác biệt nhỏ nào trong nội dung rotate đều ra digest khác, không thể
  đổi nội dung sau khi đã có chữ ký.
- Người ký (`signerPubkeyBls`) phải đang là thành viên uỷ ban **hiện tại** (`oldEpoch`, uỷ ban SẮP bị
  thay thế) — không phải uỷ ban mới.
- Sau khi đủ 2f+1 stake vote (đọc qua `getCommitteeAttestationShares`), bên tổng hợp (thường là
  `RelayerDaemon`) tự BLS-aggregate các `signatures` lại rồi gọi `committeeUpdate(...)` (mục Gateway
  ABI đầy đủ, xem `gatewayAbi.go`) kèm `aggPubkeys`/`aggSignature` — hàm này verify lại toàn bộ
  (thành viên, digest khớp, đủ stake, chữ ký hợp lệ) trước khi thật sự đổi `ChainRegistry`, không tin
  bất kỳ điều gì đã được verify trước đó ở bước vote.

### 6.5 Vote xác nhận batch commit — `submitCommitAttestation`

```solidity
function submitCommitAttestation(
    uint256 sourceChainId, uint64 epoch, bytes32 commitRoot,
    bytes calldata signerPubkeyBls, bytes calldata signature
) external;

function getCommitAttestationShares(uint256 sourceChainId, uint64 epoch, bytes32 commitRoot)
    external view returns (bytes[] memory pubkeys, bytes[] memory signatures);
```

Uỷ ban của `sourceChainId` vote xác nhận "`commitRoot` là 1 batch commit thật, do chính chain này tạo
ra ở `epoch` hiện tại" — `QuorumCert` tổng hợp từ đây chính là tham số `cert` của `attestCommit()`/
`attestReserveIssuedCommit()` (mục 2) mà relayer gửi sang chain đích.

### 6.6 Vote xác nhận message thất bại/thành công

```solidity
function submitMessageFailureAttestation(
    uint256 destChainId, bytes32 messageId, uint64 epoch,
    bytes calldata signerPubkeyBls, bytes calldata signature
) external;
function getMessageFailureAttestationShares(uint256 destChainId, bytes32 messageId, uint64 epoch)
    external view returns (bytes[] memory pubkeys, bytes[] memory signatures);

function submitMessageSuccessAttestation(
    uint256 destChainId, bytes32 messageId, uint64 epoch,
    bytes calldata signerPubkeyBls, bytes calldata signature
) external;
function getMessageSuccessAttestationShares(uint256 destChainId, bytes32 messageId, uint64 epoch)
    external view returns (bytes[] memory pubkeys, bytes[] memory signatures);
```

Uỷ ban của **chính `destChainId`** (chain đích thật sự đã thực thi payload) vote xác nhận điều chính
họ vừa quan sát được cục bộ:
- **Failure cert** (`submitMessageFailureAttestation`) — bắt buộc phải có trước khi chain nguồn được
  phép gọi `refund()`/`refundReserveAllocation()` hoàn tiền cho người gửi (mục 2's `value` note).
  Không có cert này, tiền vẫn ở trạng thái Pending, không bao giờ tự hoàn — đúng nguyên tắc "thà
  pending chứ không đoán/fork".
- **Success cert** (`submitMessageSuccessAttestation`) — bắt buộc trước khi Reserve được phép gọi
  `creditReserveAllocation()` ghi nhận đúng số dư đã thật sự chuyển tới `destChainId`.

### 6.7 Tổng hợp bảng ABI (tham khảo nhanh)

| Hàm | Loại | Ai gọi trong production |
| :-- | :-- | :-- |
| `registerCommitteePop` | write, permissionless | Bất kỳ ai giữ khoá BLS muốn đăng ký |
| `getRegisteredPop` | view | Ai cũng gọi được |
| `submitCommitteeAttestation` | write | Validator node tự động (CommitteeAttestationWorker) |
| `getCommitteeAttestationShares` | view | RelayerDaemon / dashboard vận hành |
| `submitCommitAttestation` | write | Validator node tự động (CommitAttestationWorker) |
| `getCommitAttestationShares` | view | RelayerDaemon / dashboard vận hành |
| `submitMessageFailureAttestation` | write | Validator node tự động (MessageFailureAttestationWorker) |
| `getMessageFailureAttestationShares` | view | RelayerDaemon / dashboard vận hành |
| `submitMessageSuccessAttestation` | write | Validator node tự động (MessageSuccessAttestationWorker) |
| `getMessageSuccessAttestationShares` | view | RelayerDaemon / dashboard vận hành |
| `committeeUpdate` | write, tự-verify lại toàn bộ | RelayerDaemon (hoặc bất kỳ ai, sau khi đủ quorum) |

Chi tiết ABI đầy đủ (tên tham số, thứ tự) nằm trong
`execution/pkg/blockchain/tx_processor/abi_contract/gatewayAbi.go`; logic verify thật nằm trong
`execution/pkg/blockchain/tx_processor/gateway_handler.go` (case `submit*Attestation`/
`committeeUpdate`) và `execution/pkg/cross_chain/gateway.go`.
