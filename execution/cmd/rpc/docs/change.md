# 🚀 Cập nhật Hệ thống Free Gas & Quản lý Contract

Tài liệu này mô tả các thay đổi trong cấu hình hệ thống, cơ chế hỗ trợ phí Gas tự động và các API quản lý danh sách Contract được phép hoạt động (Whitelist).

## 1. Cập nhật Cấu hình (`config-client-tcp.json`)

Bổ sung các tham số mới để quản lý việc tài trợ phí gas cho người dùng.

```json
{
  "extra_account": "1000000000000000000",
  "disable_free_gas": false
}
```

| Tham số | Mô tả |
| :--- | :--- |
| **`extra_account`** | Số tiền (Wei) hệ thống sẽ gửi thêm cho người dùng để làm phí giao dịch nếu họ không đủ số dư. |
| **`disable_free_gas`** | `true`: Tắt chế độ Free Gas (người dùng tự trả phí).<br>`false`: Bật chế độ hỗ trợ Free Gas. |

## 2. Cơ chế Hoạt động Trả Phí Gas

Hệ thống sẽ tự động kiểm tra và hỗ trợ phí gas cho người dùng dựa trên logic sau:

*   **Điều kiện kích hoạt:** Khi tài khoản người dùng thực hiện giao dịch và có số dư **< 0.001 MTN**.
*   **Hành động:** Hệ thống sẽ chuyển một lượng coin bằng giá trị của `extra_account` vào ví người dùng để thực hiện giao dịch.

## 3. Quy định về Contract (Whitelist)
> ⚠️ **Lưu ý Quan trọng:**
> *   Chỉ những contract có trong **Danh sách Free Gas** mới được phép thực thi trên RPC này.
> *   Các contract không nằm trong danh sách sẽ bị **chặn** hoàn toàn.

**Ngoại lệ tự động:**
Contract hệ thống (Contract hỗ trợ các chức năng Frontend) sẽ tự động được thêm vào danh sách Free Gas mặc định:
*   **Address:** `0x00000000000000000000000000000000D844bb55`

---
## 4. API Quản lý Danh sách Free Gas

Các hàm dùng để thêm, xóa và tra cứu danh sách các contract được hỗ trợ Free Gas.

### `addContractFreeGas`
Thêm một smart contract vào danh sách được phép hoạt động và hỗ trợ phí gas.

```solidity
function addContractFreeGas(address contractAddress, uint time, bytes memory _sign) external
```
*   **contractAddress** `(address)`: Địa chỉ của contract cần thêm vào danh sách.
*   **time** `(uint)`: Mốc thời gian (timestamp) xác thực.
*   **_sign** `(bytes)`: Chữ ký xác thực quyền quản trị.

---

### `removeContractFreeGas`
Loại bỏ một smart contract khỏi danh sách Free Gas (Contract sẽ bị chặn sau khi xóa).

```solidity
function removeContractFreeGas(address contractAddress, uint time, bytes memory _sign) external
```
*   **contractAddress** `(address)`: Địa chỉ của contract cần xóa khỏi danh sách.
*   **time** `(uint)`: Mốc thời gian (timestamp) xác thực.
*   **_sign** `(bytes)`: Chữ ký xác thực quyền quản trị.

---

### `getAllContractFreeGas`
Lấy danh sách các smart contract đang nằm trong Whitelist Free Gas (hỗ trợ phân trang).

```solidity
function getAllContractFreeGas(uint256 page, uint256 pageSize, uint256 time, bytes memory _sign) external
```
*   **page** `(uint256)`: Số thứ tự trang cần xem.
*   **pageSize** `(uint256)`: Số lượng contract hiển thị trên mỗi trang.
*   **time** `(uint256)`: Mốc thời gian (timestamp) xác thực.
*   **_sign** `(bytes)`: Chữ ký xác thực quyền quản trị.