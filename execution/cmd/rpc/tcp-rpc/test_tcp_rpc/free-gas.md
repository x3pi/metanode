# Free gas — Tài liệu kỹ thuật

# Test chức năng của các hàm trong free gas

```go run main.go -test=freegas```

### `confirmAccountWithoutSign(address _account)`

Admin xác nhận account — lấy raw tx từ pending, rebuild với device key, gửi lên chain.

| | |
| --- | --- |
| **Quyền** | Root Owner |
| **Storage** | `BlsWallet.GetPendingTx` → build tx → `SendRawTransactionBinary` → `BlsWallet.MarkConfirmed` → `BlsWallet.DeletePendingTx` → `Notification.Save` |
| **Reward** | Nếu `RewardAmount > 0` → `sendOwnerTransfer` |
| **Output** | txHash thật |

---

### `addContractFreeGas(address contractAddress)`

Thêm contract vào danh sách free gas.

| | |
| --- | --- |
| **Quyền** | Root Owner → Admin → Authorized Wallet (kiểm tra lần lượt) |
| **Storage** | `FreeGas.IsAuthorized` → `FreeGas.IsAdmin` → **`FreeGas.AddContract`** |
| **Output** | txHash giả |

---

### `removeContractFreeGas(address contractAddress)`

Xóa contract khỏi danh sách free gas.

| | |
| --- | --- |
| **Quyền** | Root Owner OR `added_by == fromAddress` |
| **Storage** | **`FreeGas.GetContract`** (lấy `added_by`) → **`FreeGas.RemoveContract`** |
| **Output** | txHash giả |

---

### `addAuthorizedWallet(address walletAddress)`

Cấp quyền authorized wallet (được phép thêm contract free gas).

| | |
| --- | --- |
| **Quyền** | Root Owner hoặc Admin |
| **Storage** | `FreeGas.IsAdmin` → **`FreeGas.AddWallet`** |
| **Output** | txHash giả |

---

### `removeAuthorizedWallet(address walletAddress)`

Thu hồi quyền authorized wallet.

| | |
| --- | --- |
| **Quyền** | Root Owner hoặc Admin |
| **Storage** | `FreeGas.IsAdmin` → **`FreeGas.RemoveWallet`** |
| **Output** | txHash giả |

---

### `addAdmin(address adminAddress)`

Bổ nhiệm admin mới. Admin có thể quản lý authorized wallets.

| | |
| --- | --- |
| **Quyền** | Chỉ Root Owner |
| **Storage** | **`FreeGas.AddAdmin`** |
| **Output** | txHash giả |

---

### `removeAdmin(address adminAddress)`

Xóa admin khỏi hệ thống.

| | |
| --- | --- |
| **Quyền** | Chỉ Root Owner |
| **Storage** | **`FreeGas.RemoveAdmin`** |
| **Output** | txHash giả |

---

## eth_call Handlers

### `getAllContractFreeGas(uint256 page, uint256 pageSize, uint256 time, bytes _sign)`

Lấy toàn bộ contracts free gas, phân trang.

| | |
| --- | --- |
| **Quyền** | Root Owner — verify ECDSA `sign(hash(page + pageSize + time))` |
| **Storage** | **`FreeGas.GetContracts(page, size)`** |
| **Output** | `{ contracts[{contract_address, added_at, added_by}], total, page, page_size, total_pages }` |

---

### `getMyContracts(address adder, uint256 page, uint256 pageSize)`

Lấy contracts do `adder` thêm, phân trang (full scan + filter theo `added_by`).

| | |
| --- | --- |
| **Quyền** | `adder=0x0` → bất kỳ (tự query); `adder!=0x0` → Root Owner hoặc Admin |
| **Storage** | `FreeGas.IsAdmin` (nếu cần) → **`FreeGas.GetContractsByAdder(adder, page, size)`** |
| **Output** | `{ adder, contracts[{contract_address, added_at, added_by}], total, page, page_size, total_pages }` |

---

### `getAllAuthorizedWallets(uint256 page, uint256 pageSize)`

Lấy danh sách authorized wallets, phân trang.

| | |
| --- | --- |
| **Quyền** | Chỉ Root Owner |
| **Storage** | **`FreeGas.GetWallets(page, size)`** |
| **Output** | `{ wallets[{wallet_address, added_at, added_by}], total, page, page_size, total_pages }` |

---

### `getAllAdmins(uint256 page, uint256 pageSize)`

Lấy danh sách admins, phân trang.

| | |
| --- | --- |
| **Quyền** | Chỉ Root Owner |
| **Storage** | **`FreeGas.GetAdmins(page, size)`** |
| **Output** | `{ admins[{admin_address, added_at, added_by}], total, page, page_size, total_pages }` |

---
