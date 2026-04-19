// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

contract TestCrossChain {
    uint256 public storedValue;
    uint256 public balanceReceived; 

    // Biến lưu trữ kết quả từ precompile để test (có thể đọc qua view function)
    address public lastSender;
    uint256 public lastSourceId;

    /**
     * @notice Hàm set giá trị và nhận tiền từ Gateway
     * @dev Nếu _newValue > 20, giao dịch sẽ bị revert.
     * Khi revert, toàn bộ trạng thái sẽ quay về ban đầu và tiền (nếu có) sẽ được trả lại.
     */
    function setValue(uint256 _newValue) public payable {    
        // Kiểm tra điều kiện: Nếu > 20 thì báo lỗi và dừng giao dịch
        require(_newValue <= 20, "Value exceeds limit: Must be 20 or less");
        storedValue = _newValue;
        balanceReceived += msg.value; 

        // Gọi thử Precompile và lưu lại trạng thái
        lastSender = getCrossChainSender();
        lastSourceId = getCrossChainSourceId();
    }
    function callSetValues(address payable _receiver, uint256 _newValue) public payable {
        bytes memory payload = abi.encodeWithSignature("setValue(uint256)", _newValue);
        (bool success, bytes memory returnData) = _receiver.call{value: msg.value}(payload);
        require(success, "Giao dich that bai! Kiem tra lai dieu kien ben Contract B");
    }
    function getContractBalance() public view returns (uint256) {
        return address(this).balance;
    }

    // ══════════════════════════════════════════════════════════════════
    // CROSS-CHAIN PRECOMPILE (address 263 = 0x107)
    // Chỉ có giá trị khi được gọi trong cross-chain context.
    // ══════════════════════════════════════════════════════════════════

    /// @notice Lấy address gốc của user trên chain nguồn (pkt.Sender)
    function getCrossChainSender() public returns (address) {
        (bool ok, bytes memory data) = address(263).call(
            abi.encodeWithSignature("getOriginalSender()")
        );
        
        // Trả về address(0) (mặc định) nếu precompile thất bại hoặc không có dữ liệu trả về 
        if (ok && data.length >= 32) {
            return abi.decode(data, (address));
        }
        return address(0);
    }

    /// @notice Lấy chainId của chain nguồn (pkt.SourceNationId)
    function getCrossChainSourceId() public returns (uint256) {
        (bool ok, bytes memory data) = address(263).call(
            abi.encodeWithSignature("getSourceChainId()")
        );
        
        // Trả về 0 (mặc định) nếu precompile thất bại hoặc không có dữ liệu trả về
        if (ok && data.length >= 32) {
            return abi.decode(data, (uint256));
        }
        return 0;
    }
}