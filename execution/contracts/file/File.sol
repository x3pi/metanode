// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;
// import "./interfaces/IEmailStorage.sol";
// Khai báo kiểu enum
enum FileStatus {
    Processing, // 0
    Active, // 1
    Deactive, // 2
    Deleted // 3
}
struct Info {
    address owner;
    bytes32 merkleRoot;
    uint64 contentLen;
    uint64 totalChunks;
    uint64 expireTime;
    string name;
    string ext;
    string contentDisposition;
    string contentID;
    FileStatus status;
}

struct FileProgress {
    bytes32 lastChunkHash;
    uint64 processedChunks;
    uint256 processedLength;
}

struct FileInfo {
    Info info;
    FileProgress progress;
    mapping(uint256 => bytes) chunks;
}
struct DownloadSession {
    bytes32 fileKey; // Key của file gốc
    address user; // Người dùng đã trả tiền
    address[] confirmations; // Danh sách các địa chỉ storage đã xác nhận
    bool isConfirmed; // Trạng thái đã đủ xác nhận hay chưa
}
contract Files {
    event FileAdded(bytes32 fileKey, string name, uint64 contentLen);
    event ChunkUploaded(bytes32 fileKey, uint256 chunkIndex);
    event FileDeleted(bytes32 fileKey);
    event FileLocked(bytes32 fileKey);
    event FileActivated(address user, bytes32 fileKey);
    event PaymentReceived(
        bytes32 fileKey,
        address payer,
        uint256 amount,
        uint256 downloadCount
    );
    event FundsWithdrawn(address owner, uint256 amount);
    event DownloadKeyGenerated(bytes32 downloadKey, bytes32  fileKey, address  user,    uint256 amount);
    event StorageConfirmed(bytes32 downloadKey, address  storageServer, uint256 currentConfirmations);
    event DownloadKeyConfirmed(bytes32 downloadKey, bytes32 fileKey);
    //
    uint256 public constant CONTRACT_IDENTIFIER = 11111;
    string[] public rustServerAddresses;
    //
    mapping(bytes32 => FileInfo) public mKeyToFileInfo;
    mapping(string => bytes32) public mNameToFileKey;

    mapping(bytes32 => DownloadSession) public mDownloadKeyToSession;
    mapping(address => bool) public storageServers;
    address[] public storageServerList;
    address public service;
    address public owner;

    // Role-based access control
    mapping(address => bool) public validators;
    address[] public validatorList; // Danh sách validators

    mapping(address => bool) public owners;
    address[] public ownerList; // Danh sách owners

    // Giá mỗi chunk (có thể thay đổi bởi owner)
    uint256 public pricePerChunk = 0.0001 ether; // Giá mặc định

    // Modifiers for role-based access control
    modifier onlyOwner() {
        require(msg.sender == owner, "Caller is not the owner");
        _;
    }

    modifier onlyValidator() {
        require(validators[msg.sender], "Caller is not a validator");
        _;
    }

    modifier onlyOwnerOrValidator() {
        require(
            msg.sender == owner || validators[msg.sender],
            "Caller is not owner or validator"
        );
        _;
    }
    modifier onlyStorage() {
        require(storageServers[msg.sender], "Caller is not a storage server");
        _;
    }
    constructor() payable {
        owner = msg.sender;
        owners[msg.sender] = true;
        ownerList.push(msg.sender);

        // address initialValidator = 0x781E6EC6EBDCA11Be4B53865a34C0c7f10b6da6e;

        // // Sử dụng biến địa chỉ, không phải chuỗi ký tự
        // validators[initialValidator] = true;
        // validatorList.push(initialValidator);

        // address server1 = 0xE21CC37677b652DfE753f94da98d79bbCF24E49a;
        // address server2 = 0xa673952B9e4Bd85478070Eca4aBdF6f479a7fB2D;

        // storageServers[server1] = true;
        // storageServerList.push(server1);

        // storageServers[server2] = true;
        // storageServerList.push(server2);
        // rustServerAddresses.push("192.168.1.234:7081"); // Index 0
        // rustServerAddresses.push("192.168.1.234:7082"); // Index 1
    }
    function setRustServerAddresses(string[] memory _addresses) external onlyOwner {
        delete rustServerAddresses; // Xóa mảng cũ
        for (uint i = 0; i < _addresses.length; i++) {
            rustServerAddresses.push(_addresses[i]);
        }
    }
    function getRustServerAddresses() external view returns (string[] memory) {
        return rustServerAddresses;
    }
    // === Owner Management ===

    // Thêm owner
    function addOwner(address _owner) external onlyOwner {
        require(_owner != address(0), "Invalid owner address");
        require(!owners[_owner], "Address is already an owner");
        owners[_owner] = true;
        ownerList.push(_owner);
    }

    // Xóa owner
    function removeOwner(address _owner) external onlyOwner {
        require(owners[_owner], "Address is not an owner");
        require(_owner != msg.sender, "Cannot remove yourself");
        owners[_owner] = false;

        // Xóa khỏi array
        for (uint256 i = 0; i < ownerList.length; i++) {
            if (ownerList[i] == _owner) {
                ownerList[i] = ownerList[ownerList.length - 1];
                ownerList.pop();
                break;
            }
        }
    }

    // Kiểm tra địa chỉ có phải owner không
    function isOwner(address _address) external view returns (bool) {
        return owners[_address];
    }

    // Lấy danh sách tất cả owners
    function getOwnerList() external view returns (address[] memory) {
        return ownerList;
    }


    // === Validator Management ===

    // Thêm validator
    function addValidator(address _validator) external onlyOwner {
        require(_validator != address(0), "Invalid validator address");
        require(!validators[_validator], "Address is already a validator");
        validators[_validator] = true;
        validatorList.push(_validator);
    }

    // Xóa validator
    function removeValidator(address _validator) external onlyOwner {
        require(validators[_validator], "Address is not a validator");
        validators[_validator] = false;

        // Xóa khỏi array
        for (uint256 i = 0; i < validatorList.length; i++) {
            if (validatorList[i] == _validator) {
                validatorList[i] = validatorList[validatorList.length - 1];
                validatorList.pop();
                break;
            }
        }
    }

    // Kiểm tra địa chỉ có phải validator không
    function isValidator(address _address) external view returns (bool) {
        return validators[_address];
    }

    // Lấy danh sách tất cả validators
    function getValidatorList() external view returns (address[] memory) {
        return validatorList;
    }

     // Thêm một storage server
    function addStorageServer(address _server) external onlyOwner {
        require(_server != address(0), "Invalid server address");
        require(!storageServers[_server], "Address is already a storage server");
        storageServers[_server] = true;
        storageServerList.push(_server);
    }

    // Xóa một storage server
    function removeStorageServer(address _server) external onlyOwner {
        require(storageServers[_server], "Address is not a storage server");
        storageServers[_server] = false;

        // Xóa khỏi array
        for (uint256 i = 0; i < storageServerList.length; i++) {
            if (storageServerList[i] == _server) {
                storageServerList[i] = storageServerList[storageServerList.length - 1];
                storageServerList.pop();
                break;
            }
        }
    }
    function isStorageServer(address _address) external view returns (bool) {
        return storageServers[_address];
    }

    // Lấy danh sách các storage server
    function getStorageServerList() external view returns (address[] memory) {
        return storageServerList;
    }
    

    // Hàm thay đổi giá per chunk (chỉ owner)
    function setPricePerChunk(uint256 _newPrice) external onlyOwner {
        pricePerChunk = _newPrice;
    }

    // Hàm A: Tính toán số tiền cần trả theo số chunk
    function calculatePrice(uint256 numChunks) public view returns (uint256) {
        return numChunks * pricePerChunk;
    }

    function pushFileInfo(
        Info memory info
    ) public payable returns (bytes32 fileKey) {
        require(
            info.expireTime > block.timestamp + 1 days,
            "Expire time must be at least 1 day in the future"
        );

        // Tính phí cần trả
        uint256 requiredPayment = calculatePrice(info.totalChunks);
        require(
            msg.value >= requiredPayment,
            "Insufficient payment for file upload"
        );

        fileKey = keccak256(
            abi.encodePacked(
                msg.sender,
                info.contentLen,
                info.expireTime,
                info.merkleRoot,
                info.name,
                info.ext,
                block.timestamp
            )
        );
        mNameToFileKey[info.name] = fileKey;
        require(
            mKeyToFileInfo[fileKey].info.merkleRoot == bytes32(0),
            "File already exists"
        );

        mKeyToFileInfo[fileKey].info = Info({
            owner: msg.sender,
            merkleRoot: info.merkleRoot,
            contentLen: info.contentLen,
            totalChunks: info.totalChunks,
            expireTime: info.expireTime,
            name: info.name,
            ext: info.ext,
            status: FileStatus.Processing,
            contentDisposition: info.contentDisposition,
            contentID: info.contentID
        });

        mKeyToFileInfo[fileKey].progress = FileProgress({
            lastChunkHash: bytes32(0),
            processedChunks: 0,
            processedLength: 0
        });

        emit FileAdded(fileKey, info.name, info.contentLen);
        return fileKey;
    }

    function getFileKeyFromName(
        string[] memory names
    ) external view returns (bytes32[] memory) {
        bytes32[] memory filekeys = new bytes32[](names.length);
        for (uint256 i; i < names.length; i++) {
            filekeys[i] = mNameToFileKey[names[i]];
        }
        return filekeys;
    }
    function uploadChunk(
        bytes32 fileKey,
        bytes memory chunkData,
        uint256 chunkIndex,
        bytes32[] memory merkleProof
    ) public {
        FileInfo storage file = mKeyToFileInfo[fileKey];
        bytes32 computedChunkHash = keccak256(
            abi.encodePacked(file.progress.lastChunkHash, chunkData)
        );

        file.progress.lastChunkHash = computedChunkHash;
        file.chunks[chunkIndex] = chunkData;
        file.progress.processedChunks++;
        file.progress.processedLength += uint64(chunkData.length);
        emit ChunkUploaded(fileKey, file.progress.processedChunks - 1);

        if (
            file.info.contentLen == file.progress.processedLength ||
            file.progress.processedChunks == file.info.totalChunks
        ) {
            file.info.status = FileStatus.Active;
            delete file.progress;
        }
    }

    function lockFile(bytes32 fileKey) external {
        FileInfo storage file = mKeyToFileInfo[fileKey];
        require(file.info.owner == msg.sender, "Caller is not the owner");
        require(file.info.status == FileStatus.Active, "File is not active");
        require(block.timestamp <= file.info.expireTime, "File has expired");

        file.info.status = FileStatus.Deactive;
        emit FileLocked(fileKey);
    }

    function deleteFile(bytes32 fileKey) external {
        FileInfo storage file = mKeyToFileInfo[fileKey];
        require(file.info.owner == msg.sender, "Caller is not the owner");
        require(file.info.status != FileStatus.Deleted, "File already deleted");
        delete mNameToFileKey[file.info.name];
        file.info.status = FileStatus.Deleted;
        for (uint256 i = 0; i < file.info.totalChunks; i++) {
            delete file.chunks[i];
        }
        delete file.progress;
        emit FileDeleted(fileKey);
    }

    function renewTime(bytes32 fileKey, uint64 _newExpireTime) external {
        FileInfo storage file = mKeyToFileInfo[fileKey];
        require(file.info.owner == msg.sender, "Caller is not the owner");
        require(
            file.info.status != FileStatus.Deleted,
            "File has been deleted"
        );
        require(
            _newExpireTime > block.timestamp + 1 days,
            "New expire time must be at least 1 day in the future"
        );

        file.info.expireTime = _newExpireTime;
    }
    function getFileInfo(bytes32 fileKey) external view returns (Info memory) {
        return mKeyToFileInfo[fileKey].info;
    }
    function getFilesInfo(
        bytes32[] memory fileKeys
    ) external view returns (Info[] memory infos) {
        infos = new Info[](fileKeys.length);
        for (uint256 i = 0; i < fileKeys.length; i++) {
            Info memory info = mKeyToFileInfo[fileKeys[i]].info;
            infos[i] = info;
        }
    }
    function getFileProgress(
        bytes32 fileKey
    ) external view returns (FileProgress memory) {
        FileInfo storage file = mKeyToFileInfo[fileKey];
        require(
            file.info.status == FileStatus.Processing,
            "File upload not exists"
        );
        return mKeyToFileInfo[fileKey].progress;
    }
    //     // Hàm tải xuống file theo chunk
    function downloadFile(
        bytes32 fileKey,
        uint256 start,
        uint256 limit
    ) external view returns (bytes[] memory) {
        FileInfo storage file = mKeyToFileInfo[fileKey];
        // require(file.info.status == FileStatus.Active, "File upload not actived");
        // require(file.info.contentLen > 0, "File does not exist");
        // require(start < file.info.totalChunks, "Start index out of range");
        // require(limit > 0, "Limit must be greater than zero");
        // require(block.timestamp <= file.info.expireTime, "File has expired");

        // Xác định số chunk tối đa có thể trả về
        uint256 end = start + limit > file.info.totalChunks
            ? file.info.totalChunks
            : start + limit;

        // Khởi tạo mảng trả về chunk
        bytes[] memory chunkData = new bytes[](end - start);

        for (uint256 i = start; i < end; i++) {
            chunkData[i - start] = file.chunks[i];
        }

        return chunkData;
    }
    function confirmFileActive(bytes32 fileKey) external {
        FileInfo storage file = mKeyToFileInfo[fileKey];
        require(
            file.info.status == FileStatus.Processing,
            "File is not in processing state"
        );
        file.info.status = FileStatus.Active;
        emit FileActivated(file.info.owner,fileKey);
    }

    // Hàm thanh toán để download file (có thể trả nhiều lần)
    function payForDownload(
        bytes32 fileKey,
        uint256 downloadTimes
    ) external payable  {
        require(downloadTimes > 0, "User must pay to dowload");
        FileInfo storage file = mKeyToFileInfo[fileKey];
        require(file.info.status == FileStatus.Active, "File is not active");
        require(block.timestamp <= file.info.expireTime, "File has expired");

        // Tính phí download cho số lần download
        uint256 downloadFee = calculatePrice(file.info.totalChunks) *
            downloadTimes;
        require(msg.value >= downloadFee, "Insufficient payment for download");

        bytes32 downloadKey = keccak256(abi.encodePacked(fileKey, block.timestamp));

        // Tạo một phiên download mới
        mDownloadKeyToSession[downloadKey] = DownloadSession({
            fileKey: fileKey,
            user: msg.sender,
            confirmations: new address[](0),
            isConfirmed: false
        });

        emit DownloadKeyGenerated(downloadKey, fileKey, msg.sender,msg.value);
    }
     function confirmServerDownload(bytes32 downloadKey) external onlyStorage {
        DownloadSession storage session = mDownloadKeyToSession[downloadKey];
        require(session.fileKey != bytes32(0), "Invalid download key");
        require(!session.isConfirmed, "Download key already fully confirmed");
        // Kiểm tra xem storage này đã xác nhận trước đó chưa
        for(uint i = 0; i < session.confirmations.length; i++){
            require(session.confirmations[i] != msg.sender, "Storage already confirmed");
        }
        // Thêm địa chỉ storage vào danh sách xác nhận
        session.confirmations.push(msg.sender);
        // Nếu đủ số lượng xác nhận, đánh dấu là đã hoàn tất và phát sự kiện
        if (session.confirmations.length >= storageServerList.length) {
            session.isConfirmed = true;
            emit DownloadKeyConfirmed(downloadKey, session.fileKey);
        }
    }

    // Hàm rút tiền (chỉ owner)
    function withdrawFunds() external onlyOwner {
        uint256 balance = address(this).balance;
        require(balance > 0, "No funds to withdraw");
        (bool success, ) = payable(owner).call{value: balance}("");
        require(success, "Transfer failed");

        emit FundsWithdrawn(owner, balance);
    }

    // Hàm rút một phần tiền (chỉ owner)
    function withdrawAmount(uint256 amount) external onlyOwner {
        require(amount > 0, "Amount must be greater than zero");
        require(
            address(this).balance >= amount,
            "Insufficient contract balance"
        );

        (bool success, ) = payable(owner).call{value: amount}("");
        require(success, "Transfer failed");

        emit FundsWithdrawn(owner, amount);
    }

    // Hàm xem balance của contract
    function getContractBalance() external view returns (uint256) {
        return address(this).balance;
    }

    function getDownloadSessionInfo(bytes32 downloadKey) external view returns (DownloadSession memory) {
        return mDownloadKeyToSession[downloadKey];
    }

}
