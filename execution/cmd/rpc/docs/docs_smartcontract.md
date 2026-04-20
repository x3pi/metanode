# Tài liệu Smart Contract AccountManager
Contract file ở `/contracts/Account.sol`
Tài liệu này mô tả các sự kiện (Events) và các hàm (Functions) trong Smart Contract `AccountManager`. Contract này quản lý việc đăng ký khóa BLS, xác thực tài khoản và quản lý trạng thái người dùng.

## 1. Events (Sự kiện)

Các sự kiện được emit ra blockchain để frontend hoặc các dịch vụ backend lắng nghe và xử lý.

### `RegisterBls`
Được bắn ra khi người dùng gọi hàm đăng ký khóa BLS. Admin lắng nghe sự kiện này để biết có yêu cầu đăng ký mới cần duyệt.

**Parameters:**
- `account` (`address`): Địa chỉ ví của người dùng thực hiện đăng ký.
- `time` (`uint`): Thời điểm gửi yêu cầu (timestamp).
- `publicKey` (`bytes`): Chuỗi bytes của khóa công khai BLS mà người dùng muốn đăng ký.
- `message` (`string`): Thông điệp kèm theo hoặc ghi chú hệ thống (nếu có).

---

### `AccountConfirmed`
Được bắn ra khi Admin chấp nhận yêu cầu đăng ký. Frontend người dùng lắng nghe sự kiện này để cập nhật trạng thái giao diện.

**Parameters:**
- `account` (`address`): Địa chỉ ví của người dùng vừa được duyệt.
- `time` (`uint`): Thời điểm được duyệt (timestamp).
- `message` (`string`): Thông báo xác nhận (ví dụ: "Registration Successful").

---

### `TransferFrom`
Được bắn ra khi có giao dịch chuyển tiền từ một tài khoản sang tài khoản khác. Cả người gửi và người nhận đều lắng nghe sự kiện này để cập nhật trạng thái real-time.

**Parameters:**
- `from` (`address`): Địa chỉ ví người gửi tiền.
- `to` (`address`): Địa chỉ ví người nhận tiền.
- `amount` (`uint256`): Số tiền chuyển (tính bằng wei).
- `time` (`uint256`): Thời điểm thực hiện giao dịch (timestamp).
- `message` (`string`): Thông báo mô tả giao dịch.

**Example:**
```json
{
  "from": "0xAAA...",
  "to": "0xBBB...",
  "amount": "100000000000000000000",
  "time": "1234567890",
  "message": "Transfer 100 from 0xAAA... to 0xBBB..."
}
```

**Frontend Handling:**
- Nếu connected account = `from` → Hiển thị "You transferred X to 0x..."
- Nếu connected account = `to` → Hiển thị "You received X from 0x..."
- Nếu không phải cả hai → Bỏ qua event

---

## 2. Functions (Hàm)

### `setBlsPublicKey`
Đăng ký khóa công khai BLS lên hệ thống.

```solidity
function setBlsPublicKey(bytes memory _publicKey) external
```

**Parameters:**
- `_publicKey` (`bytes`): Khóa công khai BLS của người dùng cần đăng ký.

---

### `setAccountType`
Cài đặt kiểu xác thực chữ ký cho tài khoản.

```solidity
function setAccountType(uint8 _type) external
```

**Parameters:**
- `_type` (`uint8`): Loại cấu hình chữ ký.
  - `0`: Sử dụng 1 chữ ký (Single Signature).
  - `1`: Sử dụng 2 chữ ký (Dual/Multi Signature).

---

### `getAllAccount`
Admin lấy danh sách tài khoản (hỗ trợ lọc và phân trang).

```solidity
function getAllAccount(
    bytes memory _sign, 
    bytes memory _publicKeyBls, 
    uint _time, 
    uint _page, 
    uint _pageSize, 
    bool _isConfirm
) external
```

**Parameters:**
- `_sign` (`bytes`): Chữ ký xác thực quyền của Admin.
- `_publicKeyBls` (`bytes`): (Tùy chọn) Dùng để tìm kiếm theo khóa BLS cụ thể.
- `_time` (`uint`): Thời gian thực hiện request (dùng để chống tấn công replay).
- `_page` (`uint`): Số thứ tự trang hiện tại (bắt đầu từ 1).
- `_pageSize` (`uint`): Số lượng tài khoản hiển thị trên một trang.
- `_isConfirm` (`bool`): Trạng thái lọc danh sách.
  - `false`: Lấy danh sách đang chờ duyệt (Pending).
  - `true`: Lấy danh sách đã được duyệt (Approved).

---

### `confirmAccount`
Admin xác nhận duyệt đăng ký cho một tài khoản cụ thể.

```solidity
function confirmAccount(address _account, uint time, bytes memory _sign) external
```

**Parameters:**
- `_account` (`address`): Địa chỉ ví của người dùng cần xác nhận.
- `time` (`uint`): Thời điểm xác nhận.
- `_sign` (`bytes`): Chữ ký xác thực của Admin để cấp quyền duyệt.

---

### `transferFrom`
Chuyển tiền từ tài khoản người dùng đến địa chỉ nhận.

```solidity
function transferFrom(address to, uint amount, uint time, bytes memory _sign) external
```

**Parameters:**
- `to` (`address`): Địa chỉ ví người nhận tiền.
- `amount` (`uint`): Số tiền cần chuyển (tính bằng wei).
- `time` (`uint`): Thời điểm thực hiện giao dịch (timestamp).
- `_sign` (`bytes`): Chữ ký xác thực của người gửi.

**Chữ ký (Signature) cấu trúc:**
```typescript
// Frontend tính chữ ký từ message:
const message = encodePacked(
  ["address", "uint256", "uint256"],
  [toAddress, amount, timestamp]
);
const signature = await walletClient.signMessage({
  account: account.address,
  message: { raw: message }
});
```

**Message structure:**
- `toAddress` (20 bytes): Địa chỉ người nhận
- `amount` (32 bytes): Số tiền chuyển (uint256)
- `timestamp` (32 bytes): Thời gian thực hiện (uint256)

**Validation:**
- Timestamp phải nằm trong vòng **5 phút** (300 giây) so với hiện tại
- Amount phải lớn hơn 0
- Chữ ký phải hợp lệ và khớp với sender

---

### `getNotifications`
Lấy danh sách thông báo của người dùng.

```solidity
function getNotifications(address _account, uint page, uint pageSize) external
```

**Parameters:**
- `_account` (`address`): Địa chỉ ví người dùng muốn xem thông báo.
- `page` (`uint`): Số thứ tự trang cần xem.
- `pageSize` (`uint`): Số lượng thông báo trên mỗi trang.



```solidity
function addContractFreeGas(address contractAddress, uint time, bytes memory _sign) external
```

**Parameters:**
- `contractAddress` (address): Địa chỉ của contract cần thêm vào danh sách Free Gas.
- `time` (uint): Mốc thời gian (timestamp) dùng để xác thực chữ ký.
- `_sign` (bytes): Chữ ký xác thực quyền thực hiện thao tác này.

---

### removeContractFreeGas
Loại bỏ một smart contract khỏi danh sách được hỗ trợ miễn phí gas.

```solidity
function removeContractFreeGas(address contractAddress, uint time, bytes memory _sign) external
```

**Parameters:**
- `contractAddress` (address): Địa chỉ của contract cần xóa khỏi danh sách Free Gas.
- `time` (uint): Mốc thời gian (timestamp) dùng để xác thực chữ ký.
- `_sign` (bytes): Chữ ký xác thực quyền thực hiện thao tác này.

---

### getAllContractFreeGas
Lấy danh sách các smart contract đang được hỗ trợ miễn phí gas (có phân trang).

```solidity
function getAllContractFreeGas(uint256 page, uint256 pageSize, uint256 time, bytes memory _sign) external
```

**Parameters:**
- `page` (uint256): Số thứ tự trang cần xem.
- `pageSize` (uint256): Số lượng contract hiển thị trên mỗi trang.
- `time` (uint256): Mốc thời gian (timestamp) dùng để xác thực chữ ký.
- `_sign` (bytes): Chữ ký xác thực quyền thực hiện thao tác này.