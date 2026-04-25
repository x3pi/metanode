# Hướng dẫn sử dụng `CrossChainConfigRegistry`

Contract `CrossChainConfigRegistry` đóng vai trò là một cơ sở dữ liệu (Registry) trung tâm cho toàn bộ hệ thống Cross-Chain (Giao thức liên chuỗi). Nó quản lý danh sách các đại sứ quán (Embassies), các mạng (Chains/Nations) được đăng ký, quản lý quyền sở hữu đa chữ ký (Multi-Owner) và theo dõi tiến độ quét sự kiện (Scan Progress).

Dưới đây là tài liệu hướng dẫn chi tiết cách sử dụng và tương tác với contract này:

---

## 1. Quản lý Quyền sở hữu (Ownership Management)

Contract hỗ trợ chế độ **Đa chủ sở hữu (Multi-Owner)**, cho phép nhiều địa chỉ có quyền quản trị ngang nhau.

*   **`addOwner(address _newOwner)`**: Thêm một chủ sở hữu mới vào danh sách. (Chỉ owner hiện tại mới có quyền gọi).
*   **`removeOwner(address _owner)`**: Xóa một chủ sở hữu khỏi danh sách. Hệ thống sẽ ngăn chặn việc xóa chủ sở hữu cuối cùng để tránh contract bị mồ côi.
*   **`getOwners()`**: Trả về danh sách tất cả các địa chỉ owner hiện tại.
*   **`isOwner(address)`**: Kiểm tra nhanh một địa chỉ có quyền owner hay không.

---

## 2. Quản lý Đại sứ quán (Embassy Management)

Đại sứ quán (Embassy) là các observer có nhiệm vụ lắng nghe event trên các chain và gửi thông điệp xác nhận (vote).

*   **`addEmbassy(bytes _blsPublicKey, address _ethAddress)`**:
    *   **Mục đích**: Đăng ký một đại sứ quán mới.
    *   **Cách hoạt động**: Lưu trữ BLS Public Key (xác thực off-chain) và ETH Address (xác thực on-chain qua `msg.sender`).
*   **`removeEmbassy(bytes _blsPublicKey)`**: Xoá hoàn toàn một đại sứ quán.
*   **`setEmbassyActive(bytes _blsPublicKey, bool _active)`**: Tạm ngưng/Kích hoạt lại hoạt động.
*   **`setEmbassyScanMode(address _embassy, uint8 _mode)`**:
    *   **Mục đích**: Cấu hình chiến lược quét block cho từng đại sứ quán khi khởi động lại.
    *   **Các chế độ (_mode)**:
        *   `0`: Bắt đầu từ Block 0 (mặc định).
        *   `1`: Bắt đầu từ **Max Block** (Block cao nhất mà bất kỳ embassy nào đã quét được trong hệ thống).
        *   `2`: Bắt đầu từ **Latest Block** (Block mới nhất hiện tại của mạng đó).
*   **`getEmbassyScanMode(address _embassy)`**: Lấy cấu hình chế độ quét hiện tại.

---

## 3. Quản lý Chain & Nation (Chain Registration)

*   **`registerChain(uint256 _nationId, uint256 _chainId, string _name, address _gateway)`**: Đăng ký một mạng mới vào hệ thống cross-chain.
*   **`unregisterChain(uint256 _nationId)`**: Hủy đăng ký một mạng.
*   **`updateChainGateway(uint256 _nationId, address _newGateway)`**: Cập nhật địa chỉ Gateway cho mạng đã đăng ký.

---

## 4. Theo dõi Tiến độ Quét (Scan Progress Tracking)

Giúp Scanner xác định điểm bắt đầu quét log, tránh trùng lặp hoặc bỏ sót dữ liệu.

### Ghi nhận Tiến độ (Dành cho Embassy)
*   **`batchUpdateScanProgress(uint256[] destNationIds, uint256[] lastScannedBlocks, uint256 localBlockNumber)`**:
    *   Cập nhật cùng lúc tiến độ quét của nhiều mạng đích và block hiện tại trên local chain.
    *   **Quyền gọi**: Phải là một Embassy đang hoạt động.
    *   **Logic**: Chỉ ghi đè nếu block mới **lớn hơn** block hiện tại (chống rollback).

### Truy xuất Tiến độ
*   **`getScanProgress(address embassy, uint256 destNationId)`**: Lấy block đã quét cuối cùng của một embassy cho một mạng đích cụ thể.
*   **`getScanBlockRange(uint256 destNationId)`**:
    *   Trả về bộ đôi `(minBlock, maxBlock)` đã quét của **tất cả** embassy.
    *   **Đặc biệt**: Nếu `destNationId == 0`, hàm sẽ trả về khoảng block trên **Local Chain**. Điều này cực kỳ quan trọng cho logic **Vote Recovery** khi hệ thống restart.

---

## 5. Cơ chế Khởi động Scanner (Off-chain Logic)

Hiện tại, việc quyết định load block bắt đầu không còn phụ thuộc vào tham số dòng lệnh (`-scan-from-latest`) mà đã được **on-chain hóa**:

1.  Scanner khởi động, gọi SMC lấy `getEmbassyScanMode`.
2.  Nếu tiến độ hiện tại là `0`:
    *   Nếu mode là `2`: Lấy block mới nhất từ Node và bắt đầu từ đó.
    *   Nếu mode là `1`: Gọi `getScanBlockRange` lấy `maxBlock` và bắt đầu từ đó.
    *   Nếu mode là `0`: Bắt đầu từ Block 0.
3.  Nếu tiến độ hiện tại `> 0`: Tiếp tục quét từ block đã lưu.

Cơ chế này giúp quản trị viên có thể điều khiển hành vi của hàng loạt Observer chỉ bằng một giao dịch thay đổi cấu hình trên Smart Contract.
