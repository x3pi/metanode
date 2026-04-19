// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

interface ICrossChainGateway {
    function sendMessage(address target, bytes calldata payload, uint256 destinationId) external payable;
}

interface IMetaNodeContext {
    function getOriginalSender() external view returns (address);
    function getSourceChainId() external view returns (uint256);
}

/**
 * @title CrossChainTicTacToe
 * @notice Game Caro 3x3 giữa 2 người chơi ở 2 Blockchain khác nhau
 * - Player X: Chơi ở Chain 1 (có thể là Node 1)
 * - Player O: Chơi ở Chain 2 (có thể là Node 2)
 */
contract CrossChainTicTacToe {
    ICrossChainGateway public immutable gateway = ICrossChainGateway(0x00000000000000000000000000000000B429C0B2);
    IMetaNodeContext constant CROSS_CHAIN_CONTEXT = IMetaNodeContext(0x00000000000000000000000000000000B429C0B2);
    

    // 0 = Trống, 1 = X, 2 = O
    uint8[9] public board;
    
    // 0 = Chưa bắt đầu, 1 = Lượt X, 2 = Lượt O
    uint8 public currentTurn = 0; 
    
    uint256 public opponentChainId;
    address public opponentContract; // <-- Thêm: Địa chỉ contract của đối thủ ở Chain kia
    address public playerX;
    address public playerO;
    
    // 0 = Đang chơi, 1 = X thắng, 2 = O thắng, 3 = Hòa (Tie)
    uint8 public winner = 0; 

    event GameStarted(address playerX, uint256 opponentChain);
    event MovePlayed(uint8 player, uint8 position);
    event GameEnded(uint8 winner);

    constructor() {
    }

    // =========================================================
    // 1. NGƯỜI CHƠI BẮT ĐẦU GAME (Vd: Gọi ở Chain 1)
    // =========================================================
    function startGame(uint256 _opponentChainId, address _opponentContract, address _opponentWallet) external {
        require(currentTurn == 0 || winner != 0, "Game already active");
        require(_opponentContract != address(0), "Invalid opponent contract");
        require(_opponentWallet != address(0), "Invalid opponent wallet");
        
        // Reset bàn cờ
        for(uint i = 0; i < 9; i++) {
            board[i] = 0;
        }
        
        playerX = msg.sender;
        playerO = _opponentWallet; // Ghi nhận cứng người chơi O ngay từ đầu
        opponentChainId = _opponentChainId;
        opponentContract = _opponentContract; // Ghi nhận địa chỉ contract đối thủ
        currentTurn = 1; // Lượt của X đi trước
        winner = 0;

        // Bắn một CC Message sang Chain đối thủ để "Đồng bộ khởi tạo Game"
        bytes memory payload = abi.encodeWithSignature(
            "syncStartGame(address,address)", 
            msg.sender,
            _opponentWallet
        );
        gateway.sendMessage(opponentContract, payload, opponentChainId);
        
        emit GameStarted(msg.sender, opponentChainId);
    }

    // =========================================================
    // 2. CHAIN 2 SẼ NHẬN MESSAGE "INIT GAME" NÀY TỪ OBSERVER
    // =========================================================
    function syncStartGame(address _playerX, address _playerO) external {
        // Tự động Ghi nhận nguồn gửi và Nation ID từ Gateway (bảo mật tuyệt đối, chống bypass)
        address sender;
        uint256 sourceNationId;
        try CROSS_CHAIN_CONTEXT.getOriginalSender() returns (address s) { sender = s; } catch { revert("Not CC"); }
        try CROSS_CHAIN_CONTEXT.getSourceChainId() returns (uint256 id) { sourceNationId = id; } catch { revert("Not CC"); }
        
        for(uint i = 0; i < 9; i++) {
            board[i] = 0;
        }
        playerX = _playerX;
        playerO = _playerO; // Khóa chết playerO
        opponentChainId = sourceNationId; // Lưu luôn Nation ID mà Sender nằm ở đó
        opponentContract = sender;        // Tự động móc nối với địa chỉ đã gọi sang mình
        currentTurn = 1; // Nhận thấy lượt của X, chờ X đánh
        winner = 0;
        
        emit GameStarted(_playerX, opponentChainId);
    }

    // =========================================================
    // 3. ĐÁNH MỘT NƯỚC CỜ BẤT KỲ 
    // =========================================================
    function playMove(uint8 position) external {
        require(currentTurn != 0 && winner == 0, "Game over or not started");
        require(position < 9, "Invalid pos");
        require(board[position] == 0, "Cell taken");

        uint8 myPlayerId;
        if (msg.sender == playerX) {
            require(currentTurn == 1, "Not X turn");
            myPlayerId = 1;
        } else {
            require(msg.sender == playerO, "Not player O");
            require(currentTurn == 2, "Not O turn");
            myPlayerId = 2;
        }
        // Cập nhật state nước đi của mình
        _processMove(myPlayerId, position);

        // Bắn action đó sang cho Chain bên kia update cờ y hệt mình
        bytes memory payload = abi.encodeWithSignature(
            "syncMove(uint8,uint8)", 
            myPlayerId, position
        );
        gateway.sendMessage(opponentContract, payload, opponentChainId);
    }

    // =========================================================
    // 4. CHAIN KIA NHẬN UPDATE NƯỚC CỜ VÀ ĐỔI LƯỢT TIẾP THEO
    // =========================================================
    function syncMove(uint8 playerId, uint8 position) external {
        _assertCrossChain();
        _processMove(playerId, position);
    }

    // =========================================================
    // HÀM HELPER NỘI BỘ
    // =========================================================
    function _processMove(uint8 playerId, uint8 position) internal {
        board[position] = playerId;
        emit MovePlayed(playerId, position);
        if (_checkWin(playerId)) {
            winner = playerId;
            emit GameEnded(winner);
        } else if (_isTie()) {
            winner = 3;
            emit GameEnded(winner);
        } else {
            currentTurn = playerId == 1 ? 2 : 1; // Đổi lượt
        }
    }

    function _assertCrossChain() internal view {
        address sender;
        try CROSS_CHAIN_CONTEXT.getOriginalSender() returns (address s) { sender = s; } catch { revert("Not CC"); }
        require(sender == opponentContract, "Hack: Origin sender is not opponent contract!");
    }

    function _checkWin(uint8 p) internal view returns (bool) {
        uint8[9] memory b = board;
        // Kiểm tra ngang
        if (b[0] == p && b[1] == p && b[2] == p) return true;
        if (b[3] == p && b[4] == p && b[5] == p) return true;
        if (b[6] == p && b[7] == p && b[8] == p) return true;
        // Kiểm tra dọc
        if (b[0] == p && b[3] == p && b[6] == p) return true;
        if (b[1] == p && b[4] == p && b[7] == p) return true;
        if (b[2] == p && b[5] == p && b[8] == p) return true;
        // Kiểm tra chéo
        if (b[0] == p && b[4] == p && b[8] == p) return true;
        if (b[2] == p && b[4] == p && b[6] == p) return true;
        return false;
    }

    function _isTie() internal view returns (bool) {
        for(uint i=0; i<9; i++) {
            if (board[i] == 0) return false;
        }
        return true;
    }
}

// =========================================================================
// HỢP ĐỒNG DEPLOYER DÙNG CHUNG ĐỊA CHỈ
// =========================================================================
contract TicTacToeFactory {
    event Deployed(address addr, bytes32 salt);

    function deployGame(bytes32 salt) external returns (address) {
        bytes memory bytecode = type(CrossChainTicTacToe).creationCode;
        address addr;
        assembly {
            addr := create2(0, add(bytecode, 0x20), mload(bytecode), salt)
        }
        require(addr != address(0), "CREATE2 Deploy Failed");
        emit Deployed(addr, salt);
        return addr;
    }
}
