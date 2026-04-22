# Thiết kế hệ thống Free Gas có phân quyền

## 1. Tổng quan

Sử dụng **1 LevelDB duy nhất** (`LdbContractFreeGas`) cho cả 3 chức năng:

- Quản lý **danh sách Admin** (`adm:`) — admin phụ do Root Owner bổ nhiệm
- Quản lý **danh sách Authorized Wallets** (`aw:`) — ví được admin cấp quyền thêm contract
- Quản lý **danh sách contract free gas** (`cfg:`) — hợp đồng được miễn phí Gas

Phân biệt bằng **prefix key**. Dữ liệu lưu dưới dạng **Protobuf (proto3)** thay vì JSON — tiết kiệm bộ nhớ hơn (address 20 bytes raw thay vì string hex 42 ký tự).

### Quy tắc đặt tên prefix

| Prefix | Viết tắt của | Ý nghĩa |
|--------|-------------|----------|
| `adm:` | **Ad**m**i**n | Reverse index cho Admin |
| `admd:` | Admin **D**ata | Dữ liệu chi tiết admin |
| `admc:` | Admin **C**ount | Đếm tổng số admin |
| `aw:` | **A**uthorized **W**allet | Reverse index cho authorized wallet |
| `awd:` | **A**uthorized **W**allet **D**ata | Dữ liệu chi tiết wallet |
| `awc:` | **A**uthorized **W**allet **C**ount | Đếm tổng số wallet |
| `cfg:` | **C**ontract **F**ree **G**as | Reverse index cho contract |
| `cfgd:` | **C**ontract **F**ree **G**as **D**ata | Dữ liệu chi tiết contract |
| `cfgc:` | **C**ontract **F**ree **G**as **C**ount | Đếm tổng số contract |

> Quy tắc chung: **tên viết tắt** → index, thêm `d` → data, thêm `c` → count.

> [!IMPORTANT]
> Xác thực người gọi dựa vào `fromAddress` recover từ chữ ký ECDSA trên transaction.
> KHÔNG cần thêm signature phụ — transaction Ethereum đã đảm bảo không giả mạo được.

---

## 2. Cấu trúc LevelDB

### 2.1 Nhóm Authorized Wallets — prefix `aw`

Lưu danh sách wallet được admin cấp quyền thêm/xóa contract free gas.

| Key | Value | Mô tả |
|-----|-------|-------|
| `awc:count` | `uint64` (8 bytes) | Tổng số wallet |
| `aw:<wallet_address>` | `"0000000001"` (padded ID) | Reverse index — tìm nhanh wallet |
| `awd:<padded_id>` | JSON | Dữ liệu wallet |

**JSON format cho `awd:<padded_id>`:**

```json
{
  "wallet_address": "0xWalletA...",
  "added_at": 1710288000,
  "added_by": "0xAdminOwner..."
}
```

### 2.2 Nhóm Contract Free Gas — prefix `cfg` (đã có, cải tiến)

| Key | Value | Mô tả |
|-----|-------|-------|
| `cfgc:count` | `uint64` (8 bytes) | Tổng số contract |
| `cfg:<contract_address>` | `"0000000001"` (padded ID) | Reverse index |
| `cfgd:<padded_id>` | JSON | Dữ liệu contract |

**JSON format cho `cfgd:<padded_id>` — CẢI TIẾN:**

```diff
 {
   "contract_address": "0xContract...",
-  "added_at": 1710288000
+  "added_at": 1710288000,
+  "added_by": "0xWalletA..."
 }
```

> [!NOTE]
> Field `added_by` mới thêm để biết ai đã thêm contract → dùng cho phân quyền xóa.

---

## 3. Ví dụ dữ liệu thực tế trong LevelDB

```
┌─────────────────────────────────────────────────────────────────┐
│                    1 LevelDB DUY NHẤT                           │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ── AUTHORIZED WALLETS ──                                       │
│                                                                 │
│  awc:count              → 2                                     │
│  aw:0xWalletA...        → "0000000001"                          │
│  aw:0xWalletB...        → "0000000002"                          │
│  awd:0000000001         → {                                     │
│                              "wallet_address": "0xWalletA...",  │
│                              "added_at": 1710288000,            │
│                              "added_by": "0xAdmin..."           │
│                           }                                     │
│  awd:0000000002         → {                                     │
│                              "wallet_address": "0xWalletB...",  │
│                              "added_at": 1710288100,            │
│                              "added_by": "0xAdmin..."           │
│                           }                                     │
│                                                                 │
│  ── CONTRACT FREE GAS ──                                        │
│                                                                 │
│  cfgc:count             → 3                                     │
│  cfg:0xContract1...     → "0000000001"                          │
│  cfg:0xContract2...     → "0000000002"                          │
│  cfg:0xContract3...     → "0000000003"                          │
│  cfgd:0000000001        → {                                     │
│                              "contract_address": "0xContract1", │
│                              "added_at": ...,                   │
│                              "added_by": "0xAdmin..."           │
│                           }                                     │
│  cfgd:0000000002        → {                                     │
│                              "contract_address": "0xContract2", │
│                              "added_at": ...,                   │
│                              "added_by": "0xWalletA..."         │
│                           }                                     │
│  cfgd:0000000003        → {                                     │
│                              "contract_address": "0xContract3", │
│                              "added_at": ...,                   │
│                              "added_by": "0xWalletA..."         │
│                           }                                     │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

---

## 4. Bảng phân quyền

Hệ thống có 3 cấp quyền: **Root Owner** > **Admin** > **Authorized Wallet**.

| Cấp | Mô tả | Nguồn gốc |
|------|---------|------------|
| Root Owner | Quyền tối cao, mặc định | `owner_rpc_address` trong config.json |
| Admin | Quyền trung gian | Do Root Owner cấp qua `addAdmin()` |
| Authorized Wallet | Quyền thêm/xóa contract | Do Root Owner hoặc Admin cấp qua `addAuthorizedWallet()` |

### 4.1 Thao tác trên Admin List (chỉ Root Owner)

| Thao tác | Điều kiện | Verify bằng |
|----------|-----------|-------------|
| `addAdmin` | `fromAddress == rootOwner` | `tx.FromAddress()` |
| `removeAdmin` | `fromAddress == rootOwner` | `tx.FromAddress()` |
| `getAllAdmins` | `fromAddress == rootOwner` | `decoded.FromAddress` (eth_call) |

### 4.2 Thao tác trên Authorized Wallets

| Thao tác | Điều kiện | Verify bằng |
|----------|-----------|-------------|
| `addAuthorizedWallet` | `fromAddress == rootOwner` **HOẶC** `adm:<fromAddress>` tồn tại | Check LevelDB |
| `removeAuthorizedWallet` | `fromAddress == rootOwner` **HOẶC** `adm:<fromAddress>` tồn tại | Check LevelDB |
| `getAllAuthorizedWallets` | `fromAddress == rootOwner` | `decoded.FromAddress` (eth_call) |

### 4.3 Thao tác trên Contract Free Gas

| Thao tác | Điều kiện | Verify bằng |
|----------|-----------|-------------|
| `addContractFreeGas` | `fromAddress == rootOwner` **HOẶC** admin **HOẶC** `aw:<fromAddress>` tồn tại | Check LevelDB |
| `removeContractFreeGas` | `fromAddress == rootOwner` **HOẶC** `added_by == fromAddress` | Đọc `cfgd:` lấy `added_by` so sánh |
| `getAllContractFreeGas` | `fromAddress == rootOwner` | Owner signature |

---

## 5. Luồng xử lý chi tiết

### 5.1 Admin thêm wallet_A vào danh sách authorized

```
wallet_A gọi → addAuthorizedWallet(0xWalletA)
                    │
                    ▼
            fromAddress = recover từ tx
                    │
                    ▼
            fromAddress == admin? ──── KHÔNG ──→ ❌ Từ chối
                    │
                   CÓ
                    │
                    ▼
            aw:0xWalletA đã tồn tại? ──── CÓ ──→ ❌ "Wallet đã có"
                    │
                  CHƯA
                    │
                    ▼
            Batch write:
              aw:0xWalletA → "0000000001"
              awd:0000000001 → {"wallet_address":"0xWalletA","added_by":"admin"}
              awc:count → count + 1
                    │
                    ▼
                ✅ Thành công
```

### 5.2 wallet_A thêm contract vào free gas

```
wallet_A gọi → addContractFreeGas(0xContractXYZ)
                    │
                    ▼
            fromAddress = recover từ tx = 0xWalletA
                    │
                    ▼
            fromAddress == admin? ──── CÓ ──→ ✅ cho phép (nhảy xuống lưu)
                    │
                  KHÔNG
                    │
                    ▼
            aw:0xWalletA tồn tại? ──── KHÔNG ──→ ❌ "Không có quyền"
                    │
                   CÓ
                    │
                    ▼
            cfg:0xContractXYZ đã tồn tại? ──── CÓ ──→ ❌ "Contract đã có"
                    │
                  CHƯA
                    │
                    ▼
            Batch write:
              cfg:0xContractXYZ → "0000000003"
              cfgd:0000000003 → {
                "contract_address": "0xContractXYZ",
                "added_by": "0xWalletA"     ← LƯU AI ĐÃ THÊM
              }
              cfgc:count → count + 1
                    │
                    ▼
                ✅ Thành công
```

### 5.3 Xóa contract — kiểm tra quyền

```
wallet_B gọi → removeContractFreeGas(0xContractXYZ)
                    │
                    ▼
            fromAddress = 0xWalletB
                    │
                    ▼
            Đọc cfgd → added_by = "0xWalletA"
                    │
                    ▼
            fromAddress == admin? ──── CÓ ──→ ✅ cho phép xóa
                    │
                  KHÔNG
                    │
                    ▼
            fromAddress == added_by?
            (0xWalletB == 0xWalletA?) ──── KHÔNG ──→ ❌ "Không có quyền xóa"
                    │
                   CÓ
                    │
                    ▼
                ✅ Cho phép xóa (swap + batch delete)
```

---

## 6. Các hàm ABI (Đã cập nhật)

Việc xác thực được thực hiện hoàn toàn tự động qua `fromAddress` của Transaction.

| Hàm | Params | Loại | Cấp quyền yêu cầu |
|-----|--------|------|--------------------|
| `addAdmin` | `address adminAddress` | transaction | Chỉ Root Owner |
| `removeAdmin` | `address adminAddress` | transaction | Chỉ Root Owner |
| `getAllAdmins` | `uint256 page, uint256 pageSize` | eth_call | Chỉ Root Owner |
| `addAuthorizedWallet` | `address walletAddress` | transaction | Root Owner hoặc Admin |
| `removeAuthorizedWallet` | `address walletAddress` | transaction | Root Owner hoặc Admin |
| `getAllAuthorizedWallets` | `uint256 page, uint256 pageSize` | eth_call | Chỉ Root Owner |
| `addContractFreeGas` | `address contractAddress` | transaction | Root Owner, Admin, hoặc Authorized Wallet |
| `removeContractFreeGas` | `address contractAddress` | transaction | Root Owner hoặc người đã thêm (AddedBy) |
| `getAllContractFreeGas` | `uint256 page, uint256 pageSize, uint256 time, bytes _sign` | eth_call | Cần Admin signature |

---

## 7. Các file cần thay đổi

| File | Thay đổi | Mức độ |
|------|----------|--------|
| [contract_free_gas.proto](file:///home/abc/nhat/metaCoSign/pkg/proto/contract_free_gas.proto) | Thêm `added_by` vào `ContractFreeGasData`, thêm `AuthorizedWalletData`, `AuthorizedWalletList`, `AdminData`, `AdminList` | Trung bình |
| [contract_free_gas_storage.go](file:///home/abc/nhat/metaCoSign/pkg/storage/contract_free_gas_storage.go) | Chuyển toàn bộ từ JSON sang proto marshal/unmarshal, thêm Admin CRUD | Lớn |
| [accountAbi.go](file:///home/abc/nhat/metaCoSign/pkg/account_handler/abi_account/accountAbi.go) | Thêm 3 hàm ABI admin mới | Nhỏ |
| [account_handler.go](file:///home/abc/nhat/metaCoSign/pkg/account_handler/account_handler.go) | Thêm 3 handler admin mới, cập nhật logic phân quyền | Lớn |
| [Account.sol](file:///home/abc/nhat/metaCoSign/contracts/Account.sol) | Thêm `addAdmin`, `removeAdmin`, `getAllAdmins` | Nhỏ |

> [!TIP]
> **KHÔNG cần sửa**: `context.go`, `config.go` — vì dùng chung 1 LevelDB hiện có.

---

## 8. Hướng dẫn chi tiết cách thức hoạt động và quản lý người dùng

Tính năng **Free Gas** xoay quanh cốt lõi là việc hệ thống MetaCoSign sẽ tự động trả phí Gas giùm người dùng nếu họ tương tác với một Smart Contract nằm trong "Danh sách ưu tiên". Để hệ thống an toàn và linh hoạt, tính năng này được chia làm **2 cấp độ quản lý** và thao tác qua **2 danh sách LevelDB**.

### 8.1. Cấp độ quản lý (Roles)

1. **Admin (DApp Owner):**
   - Là địa chỉ ví được cấu hình cố định trong hệ thống từ ban đầu (biến `OwnerRpcAddress` trong `config.json`).
   - Có toàn quyền tối cao: Thêm/Xoá thành viên, Thêm/Xoá mọi Contract.

2. **Authorized Wallet (Ví được cấp quyền / Developer):**
   - Là những địa chỉ ví bình thường nhưng được **Admin** đưa vào **Danh sách Authorized Wallets**.
   - Quyền hạn: Được phép đưa Smart Contract của riêng họ vào **Danh sách Free Gas**, và chỉ được quyền xoá những Contract do chính tay họ thêm vào.

3. **End-User (Khách hàng):**
   - Không có quyền quản lý.
   - Hưởng lợi trực tiếp: Khi gọi vào các Contract nằm trong **Danh sách Free Gas**, End-User sẽ không bị trừ Gas.

---

### 8.2. Hai danh sách quản lý cốt lõi

Hệ thống hoạt động dựa trên 1 database LevelDB duy nhất, phân biệt qua `prefix`:

1. **Danh sách Authorized Wallets (`aw:` & `awd:`):**
   - Lưu trữ danh sách các Developer/Ví tin cậy. Dữ liệu lưu bao gồm thời gian được phê duyệt và được thêm bởi ai (thường là Admin).

2. **Danh sách Contract Free Gas (`cfg:` & `cfgd:`):**
   - Lưu trữ địa chỉ của các Smart Contract được miễn phí Gas.
   - **Đặc biệt:** Dữ liệu có lưu trường `added_by` để ghi nhớ xem Admin hay Authorized Wallet nào đã đăng ký Contract này. Đây là yếu tố then chốt để phân quyền xóa.

---

### 8.3. Mô phỏng luồng thao tác thực tế (Scenarios)

Để hiểu rõ cách 2 danh sách này vận hành, hãy cùng đi qua một kịch bản phát triển đầy đủ.

**Giả sử có các nhân vật / địa chỉ sau:**
- Yêu cầu cấu hình hệ thống: `OwnerRpcAddress = 0xAdmin000...`
- Lập trình viên DApp A: `0xDevAAA...`
- Lập trình viên DApp B: `0xDevBBB...`
- Smart Contract của DApp A: `0xContractA1...` và `0xContractA2...`

#### Giai đoạn 1: Khi dự án mới bắt đầu

Khi lập trình viên DApp A (`0xDevAAA`) muốn đưa game của mình lên mạng, anh ta gửi một Transaction gọi hàm `addContractFreeGas(0xContractA1)` vào hợp đồng `AccountManager`.

- **Mạng lưới xử lý:** Trích xuất địa chỉ người gọi là `0xDevAAA`.
- **Hệ thống xác thực:** 
  1. Kiểm tra xem `0xDevAAA` có phải là `0xAdmin000...` không? (Kết quả: **Không**).
  2. Tiếp tục tìm trong "Danh sách Authorized Wallets". Vì chưa ai được cấp quyền, tìm kiếm này thất bại (Kết quả: **Không**).
- **Kết quả trả về:** Giao dịch thất bại (Revert error: *"only owner or authorized wallet can add contract free gas"*).

#### Giai đoạn 2: Khởi tạo quyền hạn (Cấp quyền)

Thấy vậy, Admin hệ thống quyết định hợp tác với DApp A.

- **Admin thao tác:** Admin dùng ví `0xAdmin000...` gọi hàm `addAuthorizedWallet(0xDevAAA)`.
- **Hệ thống xử lý:** Chấp nhận vì lệnh được gọi từ chính Admin.
- **Dữ liệu LevelDB tạo ra:**

  ```json
  // Tại prefix "awd:<id>"
  {
     "wallet_address": "0xDevAAA...",
     "added_at": 1715000000,
     "added_by": "0xAdmin000..."
  }
  ```

Khác với DevAAA, DevBBB lên mạng và thử lại lệnh thêm `0xContractA1` nhưng do Admin từ chối cấp quyền, `0xDevBBB` vẫn hoàn toàn bị block bởi hệ thống mạng.

#### Giai đoạn 3: Uỷ quyền phát huy tác dụng

Developer A hiện nay đã là Authorized Wallet.

- **Lần thử nghiệm thứ 2:** `0xDevAAA` gọi hàm `addContractFreeGas(0xContractA1)`.
- **Hệ thống xử lý:** Trích xuất `From: 0xDevAAA`. Phát hiện và khớp với dữ liệu trong "Danh sách Authorized Wallets".
- **Ghi nhận thành công:** Contract được thêm vào "Danh sách Contract Free Gas". Chú ý trường `added_by`:

  ```json
  // Tại prefix "cfgd:<id>"
  {
     "contract_address": "0xContractA1...",
     "added_at": 1715002000,
     "added_by": "0xDevAAA..."
  }
  ```

> **Hệ quả:** Mọi end-users dùng ứng dụng đều có thể tương tác với `0xContractA1` mà không tốn Gas. (Hệ thống MetaCoSign sẽ tự gánh phí theo cấu hình định trước).

#### Giai đoạn 4: Cập nhật hoặc phá hoại

Smart contract có bug và DevA buộc phải xoá phiên bản `0xContractA1` để đăng ký `0xContractA2`.

Trong khi đó, DevBBB muốn chơi xấu phá hoại dự án.

- **Hành động phá hoại của DevBBB:** Gửi một hàm gọi `removeContractFreeGas(0xContractA1)`.
  - Hệ thống lôi thông tin `cfgd` của `0xContractA1` ra và phân tích: Người ký lệnh là `0xDevBBB`, nhưng trường `added_by` lại là `0xDevAAA`.
  - System nói *"Bạn không phải sếp (Admin), cũng không phải tác giả (0xDevAAA), bạn không có quyền huỷ của người khác!"*.
  - Giao dịch Revert. Kẻ xấu thất bại.

- **Hành động đúng chuẩn của DevAAA:** DevA gửi hàm gọi `removeContractFreeGas(0xContractA1)`.
  - Hệ thống kiểm tra: Người gửi là `0xDevAAA`. So khớp với trường `added_by` của hợp đồng đó đúng là `0xDevAAA`.
  - Giao dịch **Thành công**! `0xContractA1` biến mất khỏi danh sách Free Gas.
  - Ngay sau đó, DevAAA gọi `addContractFreeGas(0xContractA2)` để áp dụng Free Gas cho hợp đồng mới. Mọi thứ hoạt động bình thường theo các luồng đã kiểm duyệt.

#### Giai đoạn 5: Ngừng hợp tác

Khi chu kỳ của DApp A đã hết hạn, hoặc dự án có dấu hiệu vi phạm điều khoản Free Gas. Admin quyết định cắt ưu đãi.

- **Thao tác thu hồi:** `0xAdmin` gọi `removeAuthorizedWallet(0xDevAAA)`. Lệnh ngay lập tức thành công.
- Ngay sau thời điểm lệnh được chạy, `0xDevAAA` trở thành người dùng bình thường và không thể thay đổi danh sách cấp độ Contract nữa. Admin có thể tự tay gọi `removeContractFreeGas(0xContractA2)` để triệt tiêu nốt lượng Free Gas còn tồn tại trên server.

### Tóm lược

Với cơ chế hai danh sách (Wallets cho phép khai báo / Contracts được khai báo) móc nối với nhau qua tham số `added_by` và trích xuất xác thực tự động trực tiếp từ `tx.FromAddress()`, hệ thống trở nên:

- **An toàn tuyệt đối:** Tránh nạn giả chữ ký dữ liệu nhờ cơ chế ký transaction nền cơ sở của Blockchain.
- **Tiện dụng cho Admins:** Có thể giao task uỷ quyền (delegate) cho từng Project Manager/Developer tự chủ công việc quản lý version Contract của họ, mà không sợ bị phá hoại chéo (cross-sabotage) từ các Developer khác. Admin không cần can thiệp mỗi lúc nâng cấp update smart contract.
