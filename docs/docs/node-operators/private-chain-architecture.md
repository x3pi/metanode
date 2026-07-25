---
sidebar_position: 8
title: 📐 So sánh Kiến trúc & Lưu trữ Tệp
---

# 📐 So sánh Kiến trúc Triển khai Private Chain & Giải pháp Lưu trữ Tệp (File Storage)

Tài liệu này đánh giá chi tiết **ưu/nhược điểm** của các mô hình triển khai Private Chain từ một Public Chain, đồng thời đưa ra kiến trúc giải pháp tối ưu nhất để tích hợp tính năng **Upload / Download Tệp tin (File Storage)** cho Private Chain.

---

## 🏗️ Part 1: Đánh giá Ưu & Nhược điểm Các Mô hình Triển khai Private Chain

### Mô hình 1: Hub-and-Spoke Native Protocol (Công nghệ: Metanode Hub, Cosmos ICS)
Public Chain đóng vai trò **Hub** điều phối, quản lý danh sách `ChainRegistry`, giữ cọc Stake (Slashing Vault) và nhận Checkpoint định kỳ từ các Private Chain (**Spokes**).

- **Ưu điểm:**
  - **Tính toàn vẹn cao:** Private Chain được bảo vệ bởi toàn bộ giá trị vốn hóa (Stake) của Public Chain. Nếu Private Chain giấu transaction hoặc fork trái phép, Validator sẽ bị tịch thu stake trên Public Chain.
  - **Tiết kiệm hạ tầng:** Cho phép chia sẻ dàn Validator (Shared Security) giữa Public Chain và Private Chain.
  - **Cross-Chain Native:** Dễ dàng luân chuyển tài sản và thông điệp qua lại giữa Public Chain và Private Chains.
- **Nhược điểm:**
  - Cần phát triển bộ giao thức liên chuỗi (Cross-chain Bridge / Relayer) ổn định ở cấp độ giao thức.

---

### Mô hình 2: ZK Rollup / Validium (Công nghệ: Polygon CDK, zkSync Hyperchains)
Private Chain gửi bằng chứng toán học (Zero-Knowledge Proof - ZK-SNARK/STARK) về Public Chain. Ở chế độ Validium, dữ liệu giao dịch được lưu riêng tư (Off-chain) bởi một Data Availability Committee (DAC).

- **Ưu điểm:**
  - **Bảo mật tuyệt đối:** Tính chính xác của giao dịch được đảm bảo bằng toán học (ZK Proof), không cần tin tưởng Validator.
  - **Quyền riêng tư dữ liệu (Privacy):** Chi tiết giao dịch nội bộ ở chế độ Validium hoàn toàn không xuất hiện trên Public Chain.
- **Nhược điểm:**
  - **Chi phí phần cứng cao:** Cần máy chủ cực mạnh trang bị GPU đắt tiền để sinh ZK Proof (Prover).
  - Tốc độ xác nhận khối phụ thuộc vào thời gian tính toán bằng chứng ZK.

---

### Mô hình 3: Subnet / Permissioned L1 (Công nghệ: Avalanche Subnets, Cosmos SDK AppChain)
Mỗi Private Chain chạy độc lập với bộ Validator riêng (được quy định trong Whitelist KYC/AML), sử dụng đồng thuận BFT riêng.

- **Ưu điểm:**
  - **Hiệu năng & Tùy biến tối đa:** Tự do tùy biến Gas Token, Block Size, Block Time và logic thực thi mà không bị giới hạn bởi Public Chain.
  - Tốc độ xử lý cực nhanh (hàng nghìn TPS).
- **Nhược điểm:**
  - **Rủi ro an ninh độc lập:** Nếu bộ Validator riêng của Private Chain quá nhỏ (ví dụ < 4 node), Private Chain dễ bị tấn công 51% hoặc gian lận nội bộ nếu không nộp checkpoint lên Public Chain.

---

### Mô hình 4: Single-Node / Autonomous Private Chain (Metanode Standalone Private Chain)
Private Chain chạy với 1 Validator độc lập hoặc cụm Validator nội bộ doanh nghiệp.

- **Ưu điểm:**
  - **Chi phí thấp nhất & Dễ triển khai nhất:** Chỉ cần 1 lệnh script (`init_private_chain.sh`) là có thể khởi chạy một mạng blockchain hoàn chỉnh.
  - **Latency siêu thấp (<1s):** Phù hợp tuyệt đối cho môi trường Dev, Staging, hoặc hệ thống nội bộ doanh nghiệp closed-loop.
- **Nhược điểm:**
  - Cần nộp Checkpoint Digest định kỳ lên Public Chain để ngăn chặn việc chỉnh sửa lịch sử dữ liệu (Data Tampering).

---

## 📊 Bảng So sánh Tổng hợp Các Mô hình

| Tiêu chí | Hub-and-Spoke (Metanode/Cosmos) | ZK Validium (Polygon CDK) | Subnet L1 (Avalanche) | Standalone (Metanode 1-Val) |
| :--- | :--- | :--- | :--- | :--- |
| **Bảo mật (Security)** | 🟢 Rất cao (Shared Stake) | 🟢 Tuyệt đối (Math ZK) | 🟡 Phụ thuộc vào Validator | 🟡 Cần Checkpoint |
| **Quyền riêng tư (Privacy)**| 🟡 Trung bình | 🟢 Tuyệt đối (Off-chain Data)| 🟢 Cao (Private Network) | 🟢 Tuyệt đối (Nội bộ) |
| **Chi phí hạ tầng** | 🟢 Thấp / Trung bình | 🔴 Rất cao (Cần GPU Prover)| 🟡 Trung bình | 🟢 Rất thấp |
| **Tốc độ (TPS & Latency)**| 🟢 Cao (< 1s) | 🟡 Phụ thuộc Prover | 🟢 Siêu cao (< 1s) | 🟢 Siêu cao (< 100ms) |
| **Độ phức tạp triển khai** | 🟢 Dễ (Script 1-click) | 🔴 Rất phức tạp | 🟡 Trung bình | 🟢 Rất dễ |

---

## 📁 Part 2: Giải pháp Tải & Tải tệp (File Upload / Download) cho Private Chain

### ⚠️ Thách thức kỹ thuật
Blockchain EVM **không thể lưu trực tiếp các tệp dung lượng lớn** (ảnh, PDF, video, tài liệu MB/GB) trực tiếp vào state DB (PebbleDB/NOMT). Nếu lưu trực tiếp:
- State DB bị phình to (State Bloat), lãng phí RAM/Disk.
- Phí Gas giao dịch sẽ bị đẩy lên cực cao.
- Làm nghẽn băng thông đồng thuận P2P giữa các Validator.

### 🏗️ Kiến trúc Giải pháp Tối ưu: Hybrid On-chain Metadata + Off-chain Encrypted Storage

Mô hình kết hợp giữa **Smart Contract kiểm soát phân quyền (On-chain)** và **Cụm lưu trữ blob mã hóa (Off-chain Storage Cluster)**.

```mermaid
sequenceDiagram
    autonumber
    actor User as Client / App
    participant Storage as Storage Node (MinIO / IPFS / S3)
    participant Chain as Private Chain (FileRegistry.sol)

    Note over User,Chain: 📤 QUY TRÌNH UPLOAD FILE
    User->>User: 1. Mã hóa File cục bộ (AES-256-GCM)
    User->>Storage: 2. Upload Encrypted Blob
    Storage-->>User: 3. Trả về Content ID (File CID / Hash)
    User->>Chain: 4. Gửi Tx `registerFile(fileHash, fileName, size, accessList)`
    Chain-->>User: 5. Xác nhận lưu File Metadata thành công (Emits Event)

    Note over User,Chain: 📥 QUY TRÌNH DOWNLOAD FILE
    User->>Chain: 6. Query `checkAccess(fileHash, userAddress)`
    Chain-->>User: 7. Xác thực quyền truy cập thành công (True)
    User->>Storage: 8. Tải Encrypted Blob bằng File CID
    User->>User: 9. Giải mã File nguyên bản bằng Key cá nhân
```

---

### 💻 Smart Contract Phân quyền Lưu trữ Tệp (`FileRegistry.sol`)

Dưới đây là Smart Contract chuẩn EVM được deploy lên Private Chain để quản lý phân quyền và kiểm tra tính toàn vẹn của tệp tin:

```solidity
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

contract FileRegistry {
    struct FileMetadata {
        string fileHash;       // SHA-256 hoặc IPFS CID của tệp mã hóa
        string fileName;       // Tên tệp
        uint256 fileSize;      // Kích thước tệp (bytes)
        address owner;         // Chủ sở hữu tệp
        uint256 createdAt;     // Thời gian tạo
        bool isRestricted;     // True: Chỉ cho phép danh sách được cấp quyền
    }

    // Mapping: fileHash => FileMetadata
    mapping(string => FileMetadata) private _files;
    
    // Mapping: fileHash => (userAddress => hasAccess)
    mapping(string => mapping(address => bool)) private _accessPermissions;

    event FileRegistered(string indexed fileHash, string fileName, address indexed owner, uint256 fileSize);
    event AccessGranted(string indexed fileHash, address indexed grantee);
    event AccessRevoked(string indexed fileHash, address indexed revokee);

    modifier onlyFileOwner(string memory fileHash) {
        require(_files[fileHash].owner == msg.sender, "FileRegistry: Caller is not file owner");
        _;
    }

    /// @notice Đăng ký tệp tin mới lên Private Chain
    function registerFile(
        string memory fileHash,
        string memory fileName,
        uint256 fileSize,
        bool isRestricted
    ) external {
        require(_files[fileHash].owner == address(0), "FileRegistry: File already registered");
        require(bytes(fileHash).length > 0, "FileRegistry: Invalid file hash");

        _files[fileHash] = FileMetadata({
            fileHash: fileHash,
            fileName: fileName,
            fileSize: fileSize,
            owner: msg.sender,
            createdAt: block.timestamp,
            isRestricted: isRestricted
        });

        // Chủ sở hữu mặc định có quyền truy cập
        _accessPermissions[fileHash][msg.sender] = true;

        emit FileRegistered(fileHash, fileName, msg.sender, fileSize);
    }

    /// @notice Cấp quyền xem/tải tệp cho một địa chỉ ví khác
    function grantAccess(string memory fileHash, address grantee) external onlyFileOwner(fileHash) {
        require(grantee != address(0), "FileRegistry: Invalid grantee");
        _accessPermissions[fileHash][grantee] = true;
        emit AccessGranted(fileHash, grantee);
    }

    /// @notice Thu hồi quyền xem/tải tệp của một địa chỉ ví
    function revokeAccess(string memory fileHash, address revokee) external onlyFileOwner(fileHash) {
        require(revokee != msg.sender, "FileRegistry: Cannot revoke owner access");
        _accessPermissions[fileHash][revokee] = false;
        emit AccessRevoked(fileHash, revokee);
    }

    /// @notice Kiểm tra địa chỉ ví có quyền tải tệp hay không
    function checkAccess(string memory fileHash, address user) external view returns (bool) {
        FileMetadata memory file = _files[fileHash];
        require(file.owner != address(0), "FileRegistry: File does not exist");
        if (!file.isRestricted) {
            return true; // Tệp công khai nội bộ
        }
        return _accessPermissions[fileHash][user];
    }

    /// @notice Lấy thông tin metadata của tệp
    function getFileMetadata(string memory fileHash) external view returns (FileMetadata memory) {
        require(_files[fileHash].owner != address(0), "FileRegistry: File does not exist");
        return _files[fileHash];
    }
}
```

---

## 🔗 Liên kết Dự án Thực tế & Công nghệ Tham chiếu

### 🌐 Mạng Blockchain Hub & Subnet
- **Avalanche Subnets:** [https://docs.avax.network/subnets](https://docs.avax.network/subnets)
- **Polygon CDK:** [https://polygon.technology/polygon-cdk](https://polygon.technology/polygon-cdk)
- **Arbitrum Orbit:** [https://docs.arbitrum.io/launch-orbit-chain/orbit-gentle-introduction](https://docs.arbitrum.io/launch-orbit-chain/orbit-gentle-introduction)
- **Cosmos IBC:** [https://ibcprotocol.dev/](https://ibcprotocol.dev/)

### 📦 Giải pháp Lưu trữ Tệp Off-chain Doanh nghiệp
- **MinIO High-Performance Object Storage:** [https://min.io/](https://min.io/) *(Phù hợp nhất cho Private Enterprise Cloud)*
- **IPFS Private Cluster:** [https://ipfs.tech/](https://ipfs.tech/) *(Mạng lưu trữ phi tập trung riêng tư)*
- **Arweave DevDocs:** [https://www.arweave.org/](https://www.arweave.org/)
