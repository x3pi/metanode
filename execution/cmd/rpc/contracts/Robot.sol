// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

contract RobotMultiChat {

    struct Message {
        bytes question;
        bytes answer;
        uint256 timestamp;
    }

    struct ChatSession {
        address user;
        Message[] history;
    }

    mapping(uint256 => ChatSession) private sessions;

    // Sự kiện 1: Phát ra khi con người hỏi
    event QuestionAsked(address user, uint256 conversationId, bytes question);
    
    // Sự kiện 2: Phát ra khi Robot trả lời và lưu trữ thành công
    event AnswerStored(address robot, uint256 conversationId, bytes answer);
      event EmitError(
        bytes32 txHash,
        string message
    );
    /**
     * @dev Bước 1: Người dùng phát tín hiệu câu hỏi.
     * Hàm này không lưu vào Storage để tiết kiệm tối đa gas cho người dùng.
     */
    function emitQuestion(address _user ,uint256 _conversationId, bytes calldata _question) public {
        emit QuestionAsked(_user, _conversationId, _question);
    }

    /**
     * @dev Bước 2: Robot thực hiện trả lời và lưu cả cặp (Hỏi - Đáp) vào Contract.
     * @param _user Địa chỉ ví của người hỏi (lấy từ sự kiện QuestionAsked)
     * @param _conversationId ID của cuộc hội thoại
     * @param _question Nội dung câu hỏi ban đầu
     * @param _answer Nội dung robot trả lời
     */
    function emitAnswer(
        address _user,
        uint256 _conversationId, 
        bytes calldata _question, 
        bytes calldata _answer
    ) public {
        ChatSession storage session = sessions[_conversationId];
        // Gán chủ sở hữu cho session nếu đây là câu đầu tiên của ID này
        if (session.user == address(0)) {
            session.user = _user;
        }
        // Lưu trữ cặp Q&A vào history
        session.history.push(Message({
            question: _question,
            answer: _answer,
            timestamp: block.timestamp
        }));

        emit AnswerStored(_user, _conversationId, _answer);
    }

    // --- Các hàm lấy dữ liệu giữ nguyên logic phân trang ---

    function getMessageCount(uint256 _conversationId) public view returns (uint256) {
        return sessions[_conversationId].history.length;
    }

    function getMessagesByPagination(uint256 _conversationId, uint256 _offset, uint256 _limit) 
        public 
        view 
        returns (Message[] memory) 
    {
        Message[] storage history = sessions[_conversationId].history;
        uint256 totalMessages = history.length;

        if (_offset >= totalMessages) return new Message[](0);

        uint256 available = totalMessages - _offset;
        uint256 count = available < _limit ? available : _limit;

        Message[] memory result = new Message[](count);

        for (uint256 i = 0; i < count; i++) {
            result[i] = history[totalMessages - 1 - _offset - i];
        }

        return result;
    }

     // SỬA LỖI: Thêm 's' vào bytes32 và truyền đủ tham số cho emit
    function emitError(bytes32 txHash, string memory message) external virtual {
        emit EmitError(txHash, message);
    }
    function getDataByTxhash(bytes32 txHash) external view virtual {
    }
}