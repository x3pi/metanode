# Account Handler API

Contract address: `0x00000000000000000000000000000000D844bb55`

Tất cả hàm đều gọi thông qua RPC client (`eth_sendRawTransaction` hoặc `eth_call`).
Handler **không ghi lên chain** — chúng intercepted tại tầng RPC, xử lý bởi `AccountHandlerNoReceipt`.

---

## Phân loại

| Phân loại | Cơ chế | Ghi chú |
| --- | --- | --- |
| **Transaction** (`eth_sendRawTransaction`) | Ký bằng ETH private key, handler kiểm tra `fromAddress` từ chữ ký | Không có on-chain receipt |
| **eth_call** | Dùng `fromAddress` của RPC client (thường là owner) | Chỉ đọc dữ liệu |

---

## Events (ABI)

| Event | Fields | Mô tả |
| --- | --- | --- |
| `AccountConfirmed` | `account address, time uint256, message string` | Emit khi xác thực account thành công |
| `RegisterBls` | `account address, time uint256, publicKey bytes, message string` | Emit khi đăng ký BLS key |
| `TransferFrom` | `from address, to address, amount uint256, time uint256, message string` | Emit khi chuyển token |

---

## Hàm Transaction (`eth_sendRawTransaction`)

### `setBlsPublicKey(bytes _publicKey)`

**Mô tả:** Đăng ký BLS public key cho tài khoản. Tài khoản sẽ ở trạng thái **chờ xác nhận** (`isConfirmed=false`) cho đến khi admin confirm.

**Quyền:** Bất kỳ ai (không cần quyền riêng).

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `_publicKey` | `bytes` | BLS public key (lấy từ `getPublickeyBls`) |

**Xử lý:**
1. Lưu `BlsAccountData` vào `LdbBlsWallet` (unconfirmed)
2. Lưu `PendingTransaction` (raw hex tx) vào storage
3. Lưu notification cho admin
4. Broadcast event `RegisterBls`

**Output (txHash):** Hash giả, không lên chain.

---

### `setBlsPublicKeyAutoConfirm(bytes _publicKey)`

**Mô tả:** Đăng ký BLS key và tự động xác nhận luôn (không cần admin confirm).

**Quyền:** Bất kỳ ai.

**Input:** Giống `setBlsPublicKey`.

**Xử lý:**
1. Lưu account data với `IsConfirmed=false`
2. Lưu pending tx
3. Gọi ngay `confirmAccountWithoutSign` nội bộ — gửi tx đến chain

**Output:** txHash của tx thực gửi lên chain.

---

### `confirmAccountWithoutSign(address _account)`

**Mô tả:** Admin xác nhận account đã đăng ký BLS key. Lấy raw tx từ pending storage và gửi lên chain.

**Quyền:** Chỉ **Root Owner** (`owner_rpc_address` trong config).

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `_account` | `address` | Địa chỉ tài khoản cần xác nhận |

**Xử lý:**
1. Lấy `PendingTransaction` của `_account`
2. Rebuild tx với device key của chain
3. Gửi tx lên chain qua `SendRawTransactionBinary`
4. Broadcast event `AccountConfirmed`

**Output:** txHash thực từ chain.

---

### `confirmAccount(address _account, uint256 time, bytes _sign)`

**Mô tả:** Xác nhận account bằng chữ ký BLS (thay vì admin confirm thủ công).

**Quyền:** Bất kỳ ai, nhưng chữ ký `_sign` phải hợp lệ theo BLS key của server.

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `_account` | `address` | Địa chỉ cần xác nhận |
| `time` | `uint256` | Timestamp của chữ ký (chống replay) |
| `_sign` | `bytes` | Chữ ký BLS: `sign(hash(account + time))` |

**Xử lý:** Xác minh chữ ký BLS, gửi tx lên chain.

---

### `transferFrom(address to, uint256 amount, uint256 time, bytes _sign)`

**Mô tả:** Chuyển token từ ví owner sang ví khác. Được xếp vào hàng đợi `ownerTxQueue` để xử lý tuần tự.

**Quyền:** Người gọi phải là tài khoản đã được xác nhận (`IsConfirmed=true`).

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `to` | `address` | Địa chỉ nhận |
| `amount` | `uint256` | Số lượng token (đơn vị nhỏ nhất) |
| `time` | `uint256` | Timestamp |
| `_sign` | `bytes` | Chữ ký BLS xác thực lệnh chuyển |

**Xử lý:** Đưa vào `ownerTxQueue`, worker gửi tx từ ví owner lên chain.

---

### `addContractFreeGas(address contractAddress)`

**Mô tả:** Thêm contract vào danh sách free gas (giao dịch đến contract này được miễn phí gas).

**Quyền:** Root Owner **hoặc** Admin **hoặc** Authorized Wallet.

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `contractAddress` | `address` | Địa chỉ contract cần thêm |

**Lưu:** `ContractFreeGasData { contractAddress, addedAt, addedBy }` vào LevelDB (proto).

**Output:** txHash giả (intercepted).

---

### `removeContractFreeGas(address contractAddress)`

**Mô tả:** Xóa contract khỏi danh sách free gas.

**Quyền:** Root Owner **hoặc** người đã `addContractFreeGas` contract đó (`added_by == fromAddress`).

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `contractAddress` | `address` | Địa chỉ contract cần xóa |

**Output:** txHash giả.

---

### `addAuthorizedWallet(address walletAddress)`

**Mô tả:** Thêm ví vào danh sách Authorized Wallets (được phép add contract free gas).

**Quyền:** Root Owner **hoặc** Admin.

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `walletAddress` | `address` | Địa chỉ ví cần cấp quyền |

---

### `removeAuthorizedWallet(address walletAddress)`

**Mô tả:** Thu hồi quyền của Authorized Wallet.

**Quyền:** Root Owner **hoặc** Admin.

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `walletAddress` | `address` | Địa chỉ ví cần thu hồi |

---

### `addAdmin(address adminAddress)`

**Mô tả:** Thêm Admin vào hệ thống. Admin có thể quản lý Authorized Wallets.

**Quyền:** Chỉ **Root Owner**.

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `adminAddress` | `address` | Địa chỉ cần bổ nhiệm làm Admin |

---

### `removeAdmin(address adminAddress)`

**Mô tả:** Xóa Admin khỏi hệ thống.

**Quyền:** Chỉ **Root Owner**.

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `adminAddress` | `address` | Địa chỉ Admin cần xóa |

---

### `setAccountType(uint8 _type)`

**Mô tả:** Đặt loại tài khoản. **Hiện không xử lý** (handler trả về `false`).

---

## Hàm eth_call (chỉ đọc)

> eth_call được gửi như transaction thông thường nhưng handler xử lý nội bộ và trả về JSON.
> `fromAddress` = địa chỉ của RPC client (từ `eth_private_key` trong config).

---

### `getPublickeyBls()`

**Mô tả:** Trả về BLS public key của server (dùng để client sign khi đăng ký).

**Quyền:** Không giới hạn.

**Input:** Không có.

**Output:**
```
"0x<bls_public_key_hex>"
```

---

### `getAllAccount(bytes _sign, bytes _publicKeyBls, uint _time, uint _page, uint _pageSize, bool _isConfirm)`

**Mô tả:** Lấy danh sách tài khoản BLS đã đăng ký (có phân trang, có thể lọc theo `isConfirm`).

**Quyền:** Chỉ Root Owner (xác minh bằng chữ ký BLS `_sign`).

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `_sign` | `bytes` | Chữ ký BLS của caller trên `hash(publicKey + time)` |
| `_publicKeyBls` | `bytes` | BLS public key của caller |
| `_time` | `uint256` | Timestamp (chống replay, phải trong 5 phút) |
| `_page` | `uint256` | Trang (0-based) |
| `_pageSize` | `uint256` | Số item/trang (max 100) |
| `_isConfirm` | `bool` | `true` = lấy confirmed, `false` = lấy unconfirmed |

**Output:**
```json
{
  "page": 0,
  "page_size": 10,
  "total": 5,
  "total_pages": 1,
  "accounts": [
    {
      "address": "0x...",
      "bls_public_key": "0x...",
      "is_confirmed": true,
      "registered_at": 1234567890,
      "register_tx_hash": "0x..."
    }
  ]
}
```

---

### `getNotifications(address _account, uint page, uint pageSize)`

**Mô tả:** Lấy danh sách thông báo của một tài khoản (phân trang).

**Quyền:** Không giới hạn (caller có thể query bất kỳ account nào).

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `_account` | `address` | Địa chỉ cần lấy notification |
| `page` | `uint256` | Trang (0-based) |
| `pageSize` | `uint256` | Số item/trang |

**Output:**
```json
{
  "page": 0,
  "page_size": 10,
  "total": 3,
  "notifications": [
    {
      "message": "BLS registered for account 0x...",
      "created_at": 1234567890
    }
  ]
}
```

---

### `getAllContractFreeGas(uint256 page, uint256 pageSize, uint256 time, bytes _sign)`

**Mô tả:** Lấy toàn bộ danh sách contract free gas (phân trang).

**Quyền:** Xác minh chữ ký BLS (giống `getAllAccount`).

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `page` | `uint256` | Trang |
| `pageSize` | `uint256` | Số item/trang (max 100) |
| `time` | `uint256` | Timestamp |
| `_sign` | `bytes` | Chữ ký BLS |

**Output:**
```json
{
  "page": 0,
  "page_size": 10,
  "total": 2,
  "total_pages": 1,
  "contracts": [
    {
      "contract_address": "0x1111...",
      "added_at": 1234567890,
      "added_by": "0xOwner..."
    }
  ]
}
```

---

### `getMyContracts(address adder, uint256 page, uint256 pageSize)`

**Mô tả:** Lấy danh sách contract free gas do địa chỉ `adder` đã thêm.

**Quyền:**
- `adder = 0x000...0` (zero address) → tự query contract của `fromAddress`
- `adder = 0xXXX` → chỉ Root Owner hoặc Admin được chỉ định query của người khác

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `adder` | `address` | Địa chỉ cần query (zero = caller) |
| `page` | `uint256` | Trang (0-based) |
| `pageSize` | `uint256` | Số item/trang (max 100) |

**Output:**
```json
{
  "adder": "0x5e58...",
  "page": 0,
  "page_size": 10,
  "total": 2,
  "total_pages": 1,
  "contracts": [
    {
      "contract_address": "0x1111...",
      "added_at": 1234567890,
      "added_by": "0x5e58..."
    }
  ]
}
```

---

### `getAllAuthorizedWallets(uint256 page, uint256 pageSize)`

**Mô tả:** Lấy danh sách Authorized Wallets.

**Quyền:** Chỉ Root Owner hoặc Admin (xác minh `fromAddress`).

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `page` | `uint256` | Trang |
| `pageSize` | `uint256` | Số item/trang (max 100) |

**Output:**
```json
{
  "page": 0,
  "page_size": 10,
  "total": 1,
  "total_pages": 1,
  "wallets": [
    {
      "wallet_address": "0x5e58...",
      "added_at": 1234567890,
      "added_by": "0x0b14..."
    }
  ]
}
```

---

### `getAllAdmins(uint256 page, uint256 pageSize)`

**Mô tả:** Lấy danh sách Admin.

**Quyền:** Chỉ Root Owner.

**Input:**

| Tham số | Kiểu | Mô tả |
| --- | --- | --- |
| `page` | `uint256` | Trang |
| `pageSize` | `uint256` | Số item/trang (max 100) |

**Output:**
```json
{
  "page": 0,
  "page_size": 10,
  "total": 2,
  "total_pages": 1,
  "admins": [
    {
      "admin_address": "0xAAAA...",
      "added_at": 1234567890,
      "added_by": "0x0b14..."
    }
  ]
}
```

---

## Phân cấp quyền tóm tắt

```
Root Owner (owner_rpc_address trong config)
├── addAdmin / removeAdmin / getAllAdmins
├── addAuthorizedWallet / removeAuthorizedWallet / getAllAuthorizedWallets
├── addContractFreeGas / removeContractFreeGas (mọi contract)
├── getMyContracts (mọi adder)
└── confirmAccountWithoutSign

Admin
├── addAuthorizedWallet / removeAuthorizedWallet
└── addContractFreeGas / removeContractFreeGas (mọi contract)

Authorized Wallet
└── addContractFreeGas / removeContractFreeGas (chỉ contract do mình thêm)

Bất kỳ ai
├── setBlsPublicKey / setBlsPublicKeyAutoConfirm
├── confirmAccount (nếu có valid BLS signature)
├── transferFrom (nếu account đã confirmed)
├── getPublickeyBls
└── getNotifications
```

---

## Lưu ý kỹ thuật

- Tất cả transaction hàm quản lý (addAdmin, addAuthorizedWallet, addContractFreeGas, ...) đều **bị intercepted** tại tầng RPC — **không có on-chain receipt**, trả về txHash giả ngay lập tức.
- eth_call dùng `fromAddress` của **RPC client** (không phải user). Để query contracts của user khác, dùng `getMyContracts(userAddr, page, pageSize)` với caller là owner/admin.
- Dữ liệu lưu dưới dạng **Protocol Buffers** (không phải JSON) trong LevelDB để tối ưu bộ nhớ.
