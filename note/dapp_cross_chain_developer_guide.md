# Metanode Cross-Chain: Hướng Dẫn Dành Cho DApp Developer

Tài liệu này hướng dẫn chi tiết từng bước cho các lập trình viên DApp cách thức gọi lệnh, chuyển tiền và thực thi hợp đồng thông minh xuyên chuỗi (Cross-chain Contract Call) trên hệ thống Metanode.

---

## 1. Tổng quan Kiến trúc Tương tác

Để chuyển tài sản hoặc gọi một contract ở chain khác, DApp (hoặc người dùng) **không giao tiếp trực tiếp với relayer**. Thay vào đó, bạn chỉ cần gửi một giao dịch (transaction) gọi hàm vào **Gateway Contract** có sẵn trên mọi chain của Metanode (private chain lẫn Root Anchor).

> **Địa chỉ cố định của Gateway Contract trên mọi chain:**
> `0x0000000000000000000000000000000000001002`

Gateway Contract này thực chất là một Native Precompile (viết bằng Go, xử lý qua "barrier transaction" — chạy tuần tự, không qua worker EVM song song) để đảm bảo tính đúng đắn tuyệt đối cho logic tài chính xuyên chuỗi, nhưng nó hỗ trợ chuẩn giao tiếp ABI giống hệt các Smart Contract Solidity thông thường — gọi qua `ethers.js`/`web3.js` như một contract bình thường.

Phần còn lại của pipeline (đợi BLS quorum cert từ uỷ ban chain nguồn, gọi `attestCommit()`/`claimMessage()` ở chain đích) do **RelayerDaemon** tự động xử lý hoàn toàn — dApp/người dùng không cần và không nên tự gọi các hàm đó.

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
- **`value` (uint256):** Số lượng token muốn gửi sang chain đích. Số này bị **khoá/đốt thật** ở chain nguồn ngay khi `outbound()` thành công. **Lưu ý fail-closed quan trọng:** một chain mới, chưa từng NHẬN giá trị thật nào từ chain khác, có allocation gửi-ra = 0 theo thiết kế — `outbound()` vẫn khoá tiền thành công, nhưng bước `attestCommit()` (relayer tự làm) ở chain đích sẽ revert cho tới khi chain đích được cấp phát allocation qua governance thật (`propose(ProposalAllocateSupply)` → vote → timelock 72h → execute) — đây không phải lỗi, hỏi đội vận hành Root Anchor nếu gặp tình huống này lần đầu.
- **`tip` (uint256):** Tiền thưởng (boa) cho Relayer để khuyến khích họ xử lý giao dịch này nhanh hơn. Nếu để `0`, relayer vẫn xử lý bình thường (relayer hiện tại tự động quét mọi message đang chờ) nhưng không có gì thưởng thêm.
- **`gasFee` (uint256):** Ngân sách Gas (native coin) bạn ứng trước để chi trả cho việc chạy Smart Contract tại chain đích khi `target` là 1 contract thật. Nếu chạy không hết, phần dư được hoàn trả tự động. Nếu chỉ chuyển tiền (`payload = 0x` hoặc `target` không có code), để `0`.
- **`hopCount` (uint8):** Bộ đếm số lần thông điệp này đã được định tuyến lại (dùng cho các luồng đi qua trạm trung gian/Reserve nhiều bước) — **không phải** "số chain phải đi qua". Với một message gửi **trực tiếp** giữa 2 chain (kể cả 2 private chain khác nhau), dùng `hopCount = 0`. Giới hạn tối đa (`MaxHopCount`) là `6`; vượt quá giá trị này giao dịch revert với `hop count exceeds maximum limit of 6`.
- **`ordered` (bool):** Nếu `true`, các thông điệp sẽ được đảm bảo xử lý theo đúng thứ tự gửi. Thường dùng `false` để tăng tối đa thông lượng (parallel execution).

---

## 3. Nhận diện người gửi thực sự ở Chain Đích

Khi `target` là một Smart Contract ở chain đích, contract này được Gateway gọi thông qua `call()`. `msg.sender` lúc này là địa chỉ của Gateway Contract (`0x00...001002`), **không phải** địa chỉ người dùng ban đầu.

Gateway cung cấp 2 hàm view riêng cho đúng tình huống này:

```solidity
// Xác nhận CHẮC CHẮN cuộc gọi hiện tại đến từ Gateway (không phải ai mạo danh gọi trực
// tiếp vào contract của bạn) — LUÔN gọi hàm này đầu tiên trước khi tin bất kỳ điều gì
// dưới đây, đừng tự so `msg.sender == <địa chỉ gateway hardcode>` bằng tay.
function isCalledByGateway() external view returns (bool result);

// Chỉ đáng tin nếu isCalledByGateway() vừa trả về true.
function getOriginalSender() external view returns (address sender, uint256 sourceChainId);
```

Ví dụ dùng trong contract Solidity ở chain đích:

```solidity
interface IGateway {
    function isCalledByGateway() external view returns (bool);
    function getOriginalSender() external view returns (address sender, uint256 sourceChainId);
}

contract MyReceiver {
    IGateway constant GATEWAY = IGateway(0x0000000000000000000000000000000000001002);

    function onCrossChainCall() external {
        require(GATEWAY.isCalledByGateway(), "not called via Gateway");
        (address originalSender, uint256 sourceChainId) = GATEWAY.getOriginalSender();
        // ... logic của bạn, dùng originalSender/sourceChainId thay vì msg.sender
    }
}
```

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
