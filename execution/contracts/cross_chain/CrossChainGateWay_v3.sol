// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/**
 * @title CrossChainGateway
 * @notice Bidirectional cross-chain communication channel (v2 - simplified)
 * @dev Removed: nonce management, relayer system, relatedAddresses
 *      Kept: lockAndBridge, sendMessage, receiveMessage (owner only), confirmations
 *
 * KEY DESIGN:
 * - sourceId = chainId của chain hiện tại (lấy từ CrossChainConfigRegistry)
 * - destinationId = chain đích (phải tồn tại trong registeredChainIds)
 * - Cả lockAndBridge và sendMessage đều nhận destinationId làm tham số
 */
contract CrossChainGateway {
    enum MessageType {
        ASSET_TRANSFER, // Bridge native coin
        CONTRACT_CALL   // Remote contract execution
    }

    enum MessageStatus {
        SUCCESS,
        FAILED
    }

    // ══════════════════════════════════════════════════════════════════════════
    //                              TYPES & STRUCTS
    // ══════════════════════════════════════════════════════════════════════════

    struct CrossChainPacket {
        uint256 sourceNationId;
        uint256 destNationId;
        uint256 timestamp;
        address sender;
        address target;
        uint256 value;
        bytes payload;
    }

    struct ConfirmationParam {
        bytes32 messageId;
        uint256 sourceBlockNumber;
        bool isSuccess;
        bytes returnData;
        address sender;
        uint256 value;
    }

    struct OutboundMessage {
        bytes32 messageId;
        address sender;
        address target;
        uint256 amount;
        MessageType msgType;
        bool isConfirmed;
        bool isRefunded;
        uint256 timestamp;
    }

    // ══════════════════════════════════════════════════════════════════════════
    //                              STATE VARIABLES
    // ══════════════════════════════════════════════════════════════════════════

    address public owner;

    /// @notice Locked balances per user
    mapping(address => uint256) public lockedBalances;

    /// @notice Outbound messages tracking
    mapping(bytes32 => OutboundMessage) public outboundMessages;

    /// @notice Track executed messages (prevent replay)
    mapping(bytes32 => bool) public messageExecuted;

    /// @notice Total locked native coin
    uint256 public totalLocked;

    /// @notice Inbound state
    uint256 public inboundLastProcessedBlock;

    /// @notice Confirmation state
    uint256 public confirmationLastProcessedBlock;

    // ══════════════════════════════════════════════════════════════════════════
    //                              EVENTS
    // ══════════════════════════════════════════════════════════════════════════

    event MessageSent(
        uint256 indexed sourceNationId,
        uint256 indexed destNationId,
        bytes32 indexed msgId,  // txHash gốc của user — dùng để filter và track OutboundResult
        bool isEVM,
        address sender,
        address target,
        uint256 value,
        bytes payload,
        uint256 timestamp
    );

    event MessageReceived(
        uint256 indexed sourceNationId,
        uint256 indexed destNationId,
        bytes32 indexed msgId,  // txHash gốc — scanner chain A đọc Topics[3] để set ConfirmationParam.MessageId
        MessageType msgType,
        MessageStatus status,
        bytes returnData,
        address sender,
        uint256 amount
    );

    event OwnershipTransferred(
        address indexed previousOwner,
        address indexed newOwner
    );

    event OutboundResult(
        bytes32 indexed msgId,  // txHash gốc — client subscribe event này filter theo msgId để biết tx A đã hoàn tất
        address indexed sender,
        MessageType msgType,
        bool isSuccess,
        uint256 amount,
        bytes reason
    );

    event ChannelStateSet(
        uint256 confirmationLastBlock,
        uint256 inboundLastBlock
    );

    // ══════════════════════════════════════════════════════════════════════════
    //                              MODIFIERS
    // ══════════════════════════════════════════════════════════════════════════

    modifier onlyOwner() {
        require(msg.sender == owner, "Only owner can call");
        _;
    }

    modifier onlySelf() {
        require(msg.sender == address(this), "Only self-call allowed");
        _;
    }

    // ══════════════════════════════════════════════════════════════════════════
    //                              CONSTRUCTOR
    // ══════════════════════════════════════════════════════════════════════════

    constructor() {
        owner = msg.sender;
    }

    // ══════════════════════════════════════════════════════════════════════════
    //                              OUTBOUND FUNCTIONS
    // ══════════════════════════════════════════════════════════════════════════

    /// @notice Bridge native coin sang chain đích
    /// @param recipient Địa chỉ nhận trên chain đích
    /// @param destinationId Nation ID của chain đích (phải registered)
    /// @dev sourceId được Go handler set từ cached chainId,
    ///      destinationId được validate against registeredChainIds
    function lockAndBridge(address recipient, uint256 destinationId) external payable {
        require(recipient != address(0), "Recipient address cannot be zero");
        require(msg.value > 0, "Amount must be greater than zero");
        require(destinationId > 0, "Invalid destinationId");

        // 1. BURN TRỰC TIẾP: Giống hệt processNativeMintBurn(1) của Go layer
        // Vì Solidity không thể hủy coin hệ thống (native), ta gửi thẳng vào địa chỉ "dead"
        payable(0x000000000000000000000000000000000000dEaD).transfer(msg.value);

        // 2. Không lưu vào lockedBalances / outboundMessages vì Go layer không làm trò đó
        // (Tiết kiệm gas lưu trữ cực lớn, chỉ Rely vào Event sinh ra)
        
        bytes memory payload = abi.encode(recipient);
        uint256 sourceId = block.chainid; // Lấy chuẩn chainId thật của node

        // 3. Bắn Event MessageSent: Truyền tham số hệt như Go handler packer
        // isEVM=true: Solidity không biết txHash — Go handler sẽ overwrite toàn bộ event này (isEVM=false)
        // msgId = keccak256(context) — unique đủ dùng cho EVM direct call edge case
        bytes32 msgId = keccak256(abi.encodePacked(msg.sender, block.number, destinationId, msg.value));
        emit MessageSent(
            sourceId,
            destinationId,
            msgId,
            true,               // isEVM = true vì đang chạy trong lõi Solidity của EVM
            msg.sender,
            address(0),
            msg.value,
            payload,
            block.timestamp
        );
    }

    /// @notice Gửi cross-chain message (contract call) sang chain đích
    /// @param target Địa chỉ contract đích
    /// @param payload Calldata cho contract đích
    /// @param destinationId Nation ID của chain đích (phải registered)
    function sendMessage(
        address target,
        bytes calldata payload,
        uint256 destinationId
    ) external payable {
        require(target != address(0), "Target cannot be zero (use lockAndBridge for asset transfer)");
        require(destinationId > 0, "Invalid destinationId");

        // 1. BURN: Nếu người gửi kẹp msg.value > 0, ta burn y hệt Go (processNativeMintBurn)
        if (msg.value > 0) {
            payable(0x000000000000000000000000000000000000dEaD).transfer(msg.value);
        }

        uint256 sourceId = block.chainid;

        // 2. Bắn Event MessageSent chuẩn xác
        // msgId = keccak256(context) — Go handler (isEVM=false) sẽ overwrite bằng txHash thật
        bytes32 msgId = keccak256(abi.encodePacked(msg.sender, target, payload, block.number));
        emit MessageSent(
            sourceId,
            destinationId,
            msgId,
            true,               // isEVM = true
            msg.sender,
            target,
            msg.value,
            payload,
            block.timestamp
        );
    }

    // ══════════════════════════════════════════════════════════════════════════
    //                    UNIFIED BATCH SUBMIT (Embassy → Chain)
    // ══════════════════════════════════════════════════════════════════════════

    /// @notice Loại event mà embassy gửi lên
    /// @dev INBOUND = MessageSent phát trên remote chain → cần thực thi trên chain này
    ///      CONFIRMATION = MessageReceived phát trên remote chain → cần xác nhận outbound
    enum EventKind { INBOUND, CONFIRMATION }

    /// @notice Unified event struct — gộp cả inbound lẫn confirmation
    /// @dev Khi eventKind = INBOUND:   dùng packet
    ///      Khi eventKind = CONFIRMATION: dùng confirmation
    ///      blockNumber = block chứa log trên remote chain (dùng chung cho cả 2 loại)
    struct EmbassyEvent {
        EventKind eventKind;
        uint256 blockNumber;
        // --- INBOUND (MessageSent từ remote) ---
        CrossChainPacket packet;
        // --- CONFIRMATION (MessageReceived trên remote) ---
        ConfirmationParam confirmation;
    }

    /// @notice Gộp cả inbound và confirmation vào 1 TX duy nhất
    /// @dev Embassy scan 1 batch logs → gom tất cả → gửi 1 lần
    ///      Chain phân loại theo eventKind và xử lý riêng
    /// @param events Danh sách các event cần xử lý
    /// @param embassyPubKey BLS public key (48 bytes) của embassy gửi TX
    ///        Chain dùng để lookup trực tiếp trong danh sách embassy registered → verify O(1)
    ///        Vote key = sha256(ABI(events)) — loại bỏ pubkey để tất cả embassy cùng hash
    function batchSubmit(
        EmbassyEvent[] calldata events,
        bytes calldata embassyPubKey
    ) external onlyOwner {
    }


    receive() external payable {
        totalLocked += msg.value;
    }
}
