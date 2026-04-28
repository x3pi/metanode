// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/**
 * @title CrossChainConfigRegistry
 * @notice Quản lý danh sách đăng ký đại sứ quán, chainId, và registered IDs
 * @dev Features:
 *   - Embassy public key registry (BLS/ECDSA)
 *   - Chain ID registry (danh sách các chain đã đăng ký)
 *   - Registered ID mapping (nationId => metadata)
 */
contract CrossChainConfigRegistry {

    // ══════════════════════════════════════════════════════════════════════════
    //                              STRUCTS
    // ══════════════════════════════════════════════════════════════════════════

    /// @notice Thông tin đăng ký của một chain/nation
    struct RegisteredChain {
        uint256 chainId;        // Chain ID (EVM chain ID)
        uint256 nationId;       // Nation ID trong cross-chain system
        string name;            // Tên chain (vd: "MetaNode-A", "MetaNode-B")
        address gateway;        // Địa chỉ CrossChainGateway contract trên chain đó
        bool isActive;          // Trạng thái hoạt động
        uint256 registeredAt;   // Thời gian đăng ký
    }

    /// @notice Thông tin đăng ký đầy đủ của một embassy
    /// @dev Lưu cả BLS public key và ETH address để xác thực 2 pha:
    ///      - BLS pubkey: xác thực chữ ký BLS trong tx_processor.go (Go side)
    ///      - ETH address: xác thực msg.sender khi embassy gửi TX lên chain
    struct Embassy {
        bytes   blsPublicKey;   // BLS public key (dùng vớify chữ ký cross-chain)
        address ethAddress;     // ETH address (dùng gửi TX và scan progress)
        bool    isActive;       // Trạng thái hoạt động
        uint256 registeredAt;   // Thời gian đăng ký
        uint256 index;          // Vị trí trong embassyKeys array
        uint8   scanMode;       // Cấu hình load block (0: Max block, 1: Latest block)
    }

    // ══════════════════════════════════════════════════════════════════════════
    //                              STATE VARIABLES
    // ══════════════════════════════════════════════════════════════════════════

    /// @notice Danh sách owner của contract
    address[] public ownersList;

    /// @notice Kiểm tra địa chỉ có phải là owner không
    mapping(address => bool) public isOwner;

    /// @notice Chain ID của chain hiện tại (chain deploy contract này)
    uint256 public chainId;

    // ── Embassy Registry ────────────────────────────────────────────────────

    /// @notice Danh sách key của tất cả embassies (key = keccak256(blsPublicKey))
    bytes32[] public embassyKeys;

    /// @notice Thông tin embassy theo BLS pubkey hash
    /// key = keccak256(blsPublicKey) => Embassy struct
    mapping(bytes32 => Embassy) public embassies;

    /// @notice Tra cứu ngược: ETH address => keccak256(blsPublicKey)
    /// Dùng để kiểm tra nhanh "address này có phải embassy không" và lấy BLS pubkey
    mapping(address => bytes32) public addressToEmbassyKey;


    /// @notice Danh sách registered chain IDs
    uint256[] public registeredChainIds;

    /// @notice Mapping: nationId => RegisteredChain info
    mapping(uint256 => RegisteredChain) public registeredChains;

    /// @notice Mapping: chainId => nationId (reverse lookup)
    mapping(uint256 => uint256) public chainIdToNationId;

    /// @notice Check if a nationId is registered
    mapping(uint256 => bool) public isRegistered;

    // ══════════════════════════════════════════════════════════════════════════
    //                       SCAN PROGRESS TRACKING
    // ══════════════════════════════════════════════════════════════════════════

    struct ProgressData {
        uint256 remoteBlock; // Block đã scan trên remote chain
        uint256 localBlock;  // Block tương ứng đã confirm trên local chain
    }

    /// @notice Lưu Mốc Chính Thức đã đạt Quorum TOÀN MẠNG
    /// @dev Lưu duy nhất 1 mốc cho cả mạng (bỏ embassyAddress)
    mapping(uint256 => ProgressData) public networkQuorumProgress;

    /// @notice Lưu Block lớn nhất trong lịch sử của từng Embassy để tính Quorum
    mapping(address => mapping(uint256 => uint256)) public maxHistoryBlock;

    /// @notice Ghi nhớ các block đã được đề xuất là Quorum (ít nhất 1 embassy đánh cờ isQuorum=true)
    /// @dev destNationId => blockNumber => bool
    mapping(uint256 => mapping(uint256 => bool)) public isProposedQuorum;

    // ══════════════════════════════════════════════════════════════════════════
    //                       EMBASSY ADDRESS MAPPING
    // ══════════════════════════════════════════════════════════════════════════

    /// @notice ETH address của từng embassy (tương ứng với embassyList public keys)
    address[] public embassyAddresses;

    /// @notice Check embassy address đã đăng ký chưa
    mapping(address => bool) public isEmbassyAddress;

    // (Moved to Embassy struct)
    // mapping(address => uint8) public embassyScanMode;

    // ══════════════════════════════════════════════════════════════════════════
    //                              EVENTS
    // ══════════════════════════════════════════════════════════════════════════

    event EmbassyAdded(bytes publicKey, uint256 timestamp);
    event EmbassyRemoved(bytes publicKey, uint256 timestamp);
    event ChainIdUpdated(uint256 oldChainId, uint256 newChainId);
    event OwnerAdded(address indexed newOwner);
    event OwnerRemoved(address indexed removedOwner);

    event ChainRegistered(
        uint256 indexed nationId,
        uint256 chainId,
        string name,
        address gateway,
        uint256 timestamp
    );

    event ChainUnregistered(
        uint256 indexed nationId,
        uint256 timestamp
    );

    event ChainGatewayUpdated(
        uint256 indexed nationId,
        address oldGateway,
        address newGateway
    );


    // ══════════════════════════════════════════════════════════════════════════
    //                              MODIFIER
    // ══════════════════════════════════════════════════════════════════════════

    modifier onlyOwner() {
        require(isOwner[msg.sender], "Only owner");
        _;
    }

    // ══════════════════════════════════════════════════════════════════════════
    //                              CONSTRUCTOR
    // ══════════════════════════════════════════════════════════════════════════

    constructor(uint256 _chainId) {
        require(_chainId > 0, "Invalid chainId");
        chainId = _chainId;
        isOwner[msg.sender] = true;
        ownersList.push(msg.sender);
        emit OwnerAdded(msg.sender);
    }

    // ══════════════════════════════════════════════════════════════════════════
    //                              OWNERSHIP MANAGEMENT
    // ══════════════════════════════════════════════════════════════════════════

    /**
     * @notice Thêm một owner mới
     * @param _newOwner Địa chỉ owner mới
     */
    function addOwner(address _newOwner) external onlyOwner {
        require(_newOwner != address(0), "Zero address");
        require(!isOwner[_newOwner], "Already owner");
        
        isOwner[_newOwner] = true;
        ownersList.push(_newOwner);
        emit OwnerAdded(_newOwner);
    }

    /**
     * @notice Xóa một owner hiện tại
     * @param _owner Địa chỉ owner cần xóa
     */
    function removeOwner(address _owner) external onlyOwner {
        require(isOwner[_owner], "Not an owner");
        require(ownersList.length > 1, "Cannot remove last owner");
        
        isOwner[_owner] = false;
        
        for (uint256 i = 0; i < ownersList.length; i++) {
            if (ownersList[i] == _owner) {
                ownersList[i] = ownersList[ownersList.length - 1];
                ownersList.pop();
                break;
            }
        }
        
        emit OwnerRemoved(_owner);
    }

    /**
     * @notice Lấy danh sách tất cả các owner hiện tại
     * @return Mảng địa chỉ của các owner
     */
    function getOwners() external view returns (address[] memory) {
        return ownersList;
    }

    // ══════════════════════════════════════════════════════════════════════════
    //                       EMBASSY MANAGEMENT
    // ══════════════════════════════════════════════════════════════════════════

    /**
     * @notice Đăng ký embassy với BLS public key và ETH address
     * @dev Khi addEmbassy, cả 2 thông tin được lưu trong cùng 1 struct Embassy:
     *   - blsPublicKey: Go side (tx_processor.go) đọc để verify BLS aggregate sig
     *   - ethAddress:   Solidity side (msg.sender) để authorize batchUpdateScanProgress
     * @param _blsPublicKey BLS public key bytes của embassy
     * @param _ethAddress ETH address embassy dùng để gửi TX
     */
    function addEmbassy(
        bytes calldata _blsPublicKey,
        address _ethAddress
    ) external onlyOwner {
        require(_blsPublicKey.length > 0, "Empty BLS public key");
        require(_ethAddress != address(0), "Zero ETH address");

        bytes32 keyHash = keccak256(_blsPublicKey);
        require(embassies[keyHash].ethAddress == address(0), "BLS key already registered");
        require(addressToEmbassyKey[_ethAddress] == bytes32(0), "ETH address already registered");

        embassies[keyHash] = Embassy({
            blsPublicKey: _blsPublicKey,
            ethAddress:   _ethAddress,
            isActive:     true,
            registeredAt: block.timestamp,
            index:        embassyKeys.length,
            scanMode:     1
        });
        embassyKeys.push(keyHash);
        addressToEmbassyKey[_ethAddress] = keyHash;

        emit EmbassyAdded(_blsPublicKey, block.timestamp);
    }

    /**
     * @notice Xóa embassy khỏi danh sách
     * @param _blsPublicKey BLS public key bytes cần xóa
     */
    function removeEmbassy(bytes calldata _blsPublicKey) external onlyOwner {
        bytes32 keyHash = keccak256(_blsPublicKey);
        Embassy storage emb = embassies[keyHash];
        require(emb.ethAddress != address(0), "Not registered");

        // Xóa reverse lookup
        delete addressToEmbassyKey[emb.ethAddress];

        // Swap with last + pop trong embassyKeys array
        uint256 idx = emb.index;
        uint256 lastIdx = embassyKeys.length - 1;
        if (idx != lastIdx) {
            bytes32 lastKey = embassyKeys[lastIdx];
            embassyKeys[idx] = lastKey;
            embassies[lastKey].index = idx;
        }
        embassyKeys.pop();

        emit EmbassyRemoved(_blsPublicKey, block.timestamp);
        delete embassies[keyHash];
    }

    /**
     * @notice Đặt trạng thái active/inactive cho embassy (không xóa data)
     */
    function setEmbassyActive(bytes calldata _blsPublicKey, bool _active) external onlyOwner {
        bytes32 keyHash = keccak256(_blsPublicKey);
        require(embassies[keyHash].ethAddress != address(0), "Not registered");
        embassies[keyHash].isActive = _active;
    }


    // ══════════════════════════════════════════════════════════════════════════
    //                       CHAIN REGISTRATION
    // ══════════════════════════════════════════════════════════════════════════

    /**
     * @notice Đăng ký một chain/nation vào hệ thống cross-chain
     * @param _nationId Nation ID (unique identifier trong cross-chain system)
     * @param _chainId EVM Chain ID
     * @param _name Tên chain
     * @param _gateway Địa chỉ CrossChainGateway contract trên chain đó
     */
    function registerChain(
        uint256 _nationId,
        uint256 _chainId,
        string calldata _name,
        address _gateway
    ) external onlyOwner {
        require(_nationId > 0, "Invalid nationId");
        require(_chainId > 0, "Invalid chainId");
        require(!isRegistered[_nationId], "NationId already registered");
        require(chainIdToNationId[_chainId] == 0, "ChainId already mapped");

        registeredChains[_nationId] = RegisteredChain({
            chainId: _chainId,
            nationId: _nationId,
            name: _name,
            gateway: _gateway,
            isActive: true,
            registeredAt: block.timestamp
        });

        registeredChainIds.push(_nationId);
        chainIdToNationId[_chainId] = _nationId;
        isRegistered[_nationId] = true;

        emit ChainRegistered(_nationId, _chainId, _name, _gateway, block.timestamp);
    }

    /**
     * @notice Hủy đăng ký một chain/nation
     * @param _nationId Nation ID cần hủy
     */
    function unregisterChain(uint256 _nationId) external onlyOwner {
        require(isRegistered[_nationId], "Not registered");

        RegisteredChain storage chain_ = registeredChains[_nationId];
        chain_.isActive = false;

        // Remove from chainIdToNationId
        chainIdToNationId[chain_.chainId] = 0;
        isRegistered[_nationId] = false;

        // Remove from registeredChainIds array (swap with last + pop)
        for (uint256 i = 0; i < registeredChainIds.length; i++) {
            if (registeredChainIds[i] == _nationId) {
                registeredChainIds[i] = registeredChainIds[registeredChainIds.length - 1];
                registeredChainIds.pop();
                break;
            }
        }

        emit ChainUnregistered(_nationId, block.timestamp);
    }

    /**
     * @notice Cập nhật gateway address cho một chain đã đăng ký
     * @param _nationId Nation ID
     * @param _newGateway Địa chỉ gateway mới
     */
    function updateChainGateway(uint256 _nationId, address _newGateway) external onlyOwner {
        require(isRegistered[_nationId], "Not registered");

        address oldGateway = registeredChains[_nationId].gateway;
        registeredChains[_nationId].gateway = _newGateway;

        emit ChainGatewayUpdated(_nationId, oldGateway, _newGateway);
    }

    // ══════════════════════════════════════════════════════════════════════════
    //                       CHAINID & OWNERSHIP
    // ══════════════════════════════════════════════════════════════════════════

    /**
     * @notice Cập nhật chainId
     * @param _newChainId ChainId mới
     */
    function setChainId(uint256 _newChainId) external onlyOwner {
        require(_newChainId > 0, "Invalid chainId");
        uint256 oldChainId = chainId;
        chainId = _newChainId;
        emit ChainIdUpdated(oldChainId, _newChainId);
    }



    // ══════════════════════════════════════════════════════════════════════════
    //                     SCAN PROGRESS FUNCTIONS
    // ══════════════════════════════════════════════════════════════════════════

    /**
     * @notice Batch update block đã scan cho nhiều destinationChain trong 1 TX
     * @dev Observer ngồi trên Chain A, scan logs từ Chain A (địa phương),
     *      theo dõi "đã xử lý messages gửi đến Chain B đến block X".
     *      Mỗi embassy chạy N goroutine scan song song (mỗi destChain 1 goroutine)
     *      rồi gom kết quả vào 1 TX duy nhất để tránh nonce conflict.
     * @param destNationIds Danh sách nationId của các destination chains đang scan
     * @param lastScannedBlocks Block cuối đã scan tương ứng với mỗi destNationId
     */
    function batchUpdateScanProgress(
        uint256[] calldata destNationIds,
        uint256[] calldata lastScannedBlocks,
        uint256 localBlockNumber,
        bool[] calldata isQuorums
    ) external {
        require(destNationIds.length == lastScannedBlocks.length, "Length mismatch");
        require(destNationIds.length == isQuorums.length, "Quorum mismatch");
        require(destNationIds.length > 0 || localBlockNumber > 0, "Empty arrays");

        // Embassy phải đã đăng ký mới được update scan progress
        bytes32 myKey = addressToEmbassyKey[msg.sender];
        require(myKey != bytes32(0) && embassies[myKey].isActive, "Not a registered embassy");

        for (uint256 i = 0; i < destNationIds.length; i++) {
            uint256 destNationId = destNationIds[i];
            uint256 newBlock = lastScannedBlocks[i];
            require(newBlock > 0, "Block must be > 0");

            // 1. Luôn lưu lại mốc cao nhất của embassy này (Cache)
            if (newBlock > maxHistoryBlock[msg.sender][destNationId]) {
                maxHistoryBlock[msg.sender][destNationId] = newBlock;
            }
            // 2. Ghi nhớ nếu có embassy báo cáo block này là Quorum
            if (isQuorums[i]) {
                isProposedQuorum[destNationId][newBlock] = true;
            }

            // 3. Nếu block này ĐÃ TỪNG được ai đó đề xuất là Quorum (có thể là trước đây hoặc bây giờ)
            // thì ta sẽ kiểm tra lại xem ĐÃ ĐỦ QUORUM CHƯA
            if (isProposedQuorum[destNationId][newBlock]) {
                // Chỉ xử lý nếu block mới cao hơn Quorum hiện tại (bỏ qua block nhỏ hơn)
                if (newBlock > networkQuorumProgress[destNationId].remoteBlock) {
                    uint256 confirmCount = 0;
                    uint256 activeCount = 0;
                    
                    // Quét qua mốc Cache của các Embassy đang active
                    for (uint256 k = 0; k < embassyKeys.length; k++) {
                        if (!embassies[embassyKeys[k]].isActive) continue;
                        activeCount++;
                        
                        // Nếu mốc lớn nhất của Node này >= newBlock -> nó ủng hộ block này
                        if (maxHistoryBlock[embassies[embassyKeys[k]].ethAddress][destNationId] >= newBlock) {
                            confirmCount++;
                        }
                    }
                    
                    // Tính Quorum theo đúng công thức: ceil(2N/3) = (N*2 + 2) / 3
                    uint256 q = (activeCount * 2 + 2) / 3;
                    
                    if (confirmCount >= q) {
                        // Cập nhật mốc GLOBAL của toàn mạng!
                        networkQuorumProgress[destNationId].remoteBlock = newBlock;
                        networkQuorumProgress[destNationId].localBlock = localBlockNumber;
                    }
                }
            }
        }
    }

    /**
     * @notice Lấy block đã scan của một embassy cho một destination chain
     */
    /**
     * @notice Lấy block đã scan duy nhất của toàn mạng cho destination chain
     * @dev Trả về mốc Quorum đã được xác nhận của mạng lưới
     * @param destNationId Nation ID của destination chain cần kiểm tra
     * @return quorumBlock Block đã đạt Quorum (> 50% majority)
     */
    function getNetworkQuorumBlock(uint256 destNationId) external view returns (ProgressData memory) {
        return networkQuorumProgress[destNationId];
    }

    /**
     * @notice Lấy progress của tất cả embassies cho 1 destination chain
     */
    function getAllScanProgress(
        uint256 destNationId
    ) external view returns (address[] memory addresses, uint256[] memory blocks) {
        uint256 count = embassyKeys.length;
        addresses = new address[](count);
        blocks = new uint256[](count);

        for (uint256 i = 0; i < count; i++) {
            address embAddr = embassies[embassyKeys[i]].ethAddress;
            addresses[i] = embAddr;
            blocks[i] = maxHistoryBlock[embAddr][destNationId];
        }
    }



    // ══════════════════════════════════════════════════════════════════════════
    //                          VIEW FUNCTIONS
    // ══════════════════════════════════════════════════════════════════════════

    /**
     * @notice Kiểm tra BLS public key có phải đại sứ quán đang active không
     */
    function checkEmbassy(bytes calldata _publicKey) external view returns (bool) {
        bytes32 keyHash = keccak256(_publicKey);
        return embassies[keyHash].isActive;

    }
    /**
     * @notice Lấy danh sách tất cả embassies (cả BLS pubkey + ETH address)
     */
    function getAllEmbassies() external view returns (Embassy[] memory result) {
        uint256 activeCount = 0;
        for (uint256 i = 0; i < embassyKeys.length; i++) {
            if (embassies[embassyKeys[i]].isActive) {
                activeCount++;
            }
        }
        result = new Embassy[](activeCount);
        uint256 index = 0;
        for (uint256 i = 0; i < embassyKeys.length; i++) {
            if (embassies[embassyKeys[i]].isActive) {
                result[index] = embassies[embassyKeys[i]];
                index++;
            }
        }
    }

    /**
     * @notice Số lượng embassy đã đăng ký
     */
    function getEmbassyCount() external view returns (uint256) {
        return embassyKeys.length;
    }

    /**
     * @notice Tra cứu embassy bằng ETH address
     * @dev Dùng khi cần kiểm tra "msg.sender có phải embassy không"
     *      và lấy BLS pubkey để verify trong tx_processor.go
     */
    function getEmbassyByAddress(address _ethAddress) external view returns (Embassy memory) {
        bytes32 key = addressToEmbassyKey[_ethAddress];
        return embassies[key];
    }

    /**
     * @notice Tra cứu embassy bằng BLS public key
     */
    function getEmbassyByBLSKey(bytes calldata _blsPublicKey) external view returns (Embassy memory) {
        return embassies[keccak256(_blsPublicKey)];
    }

    /**
     * @notice Kiểm tra ETH address có phải embassy hợp lệ không
     */
    function isActiveEmbassy(address _ethAddress) external view returns (bool) {
        bytes32 key = addressToEmbassyKey[_ethAddress];
        return key != bytes32(0) && embassies[key].isActive;
    }

    /**
     * @notice Cấu hình chế độ quét block cho một đại sứ quán
     * @param _embassy Địa chỉ đại sứ quán
     * @param _mode 0: Từ block 0, 1: Max block, 2: Latest block
     */
    function setEmbassyScanMode(address _embassy, uint8 _mode) external {
        bytes32 key = addressToEmbassyKey[_embassy];
        require(key != bytes32(0), "Not a registered embassy");
        require(isOwner[msg.sender] || msg.sender == _embassy, "Only owner or embassy itself");
        require(_mode <= 2, "Invalid mode");
        embassies[key].scanMode = _mode;
    }

    /**
     * @notice Lấy cấu hình chế độ quét block của một đại sứ quán
     */
    function getEmbassyScanMode(address _embassy) external view returns (uint8) {
        bytes32 key = addressToEmbassyKey[_embassy];
        return embassies[key].scanMode;
    }

    /**
     * @notice Lấy thông tin chain đã đăng ký theo nationId
     */
    function getRegisteredChain(uint256 _nationId) external view returns (RegisteredChain memory) {
        return registeredChains[_nationId];
    }

    /**
     * @notice Lấy danh sách tất cả nationId đã đăng ký
     */
    function getRegisteredChainIds() external view returns (uint256[] memory) {
        return registeredChainIds;
    }

    /**
     * @notice Số lượng chain đã đăng ký
     */
    function getRegisteredChainCount() external view returns (uint256) {
        return registeredChainIds.length;
    }

    /**
     * @notice Lấy nationId từ chainId
     */
    function getNationIdByChainId(uint256 _chainId) external view returns (uint256) {
        return chainIdToNationId[_chainId];
    }

    /**
     * @notice Lấy tất cả thông tin registry: embassyCount, chainId, registeredChainCount
     */
    function getRegistryInfo() external view returns (
        uint256 _chainId,
        uint256 embassyCount,
        uint256 registeredCount
    ) {
        return (chainId, embassyAddresses.length, registeredChainIds.length);
    }

}
