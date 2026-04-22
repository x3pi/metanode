// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

contract ContractHelper {
    // Helper 1: Tạo bytes cho hàm setValue(uint256)
    function getBytesForSetValue(uint256 _value) public pure returns (bytes memory) {
        return abi.encodeWithSignature("setValue(uint256)", _value);
    }
    // Helper 2: Tạo bytes cho hàm getValue()
    // Hàm này KHÔNG cần tham số đầu vào vì getValue() bên kia rỗng
    function getBytesForGetValue() public pure returns (bytes memory) {
        // Signature là "getValue()"
        return abi.encodeWithSignature("getValue()"); 
    }
    function getBytesForBalanceReceived() public pure returns (bytes memory) {
        // Vì balanceReceived là biến public, Solidity tự tạo getter function: balanceReceived()
        return abi.encodeWithSignature("balanceReceived()");
    }

    function getBytesForCallSetValues(address _receiver, uint256 _newValue) public pure returns (bytes memory) {
        // Lưu ý: Trong signature KHÔNG được để dấu cách giữa address và uint256
        return abi.encodeWithSignature("callSetValues(address,uint256)", _receiver, _newValue);
    }

    function getBytesForLastSender() public pure returns (bytes memory) {
        // Gọi đến public variable getter 'lastSender'
        return abi.encodeWithSignature("lastSender()");
    }

    function getBytesForLastSourceId() public pure returns (bytes memory) {
        // Gọi đến public variable getter 'lastSourceId'
        return abi.encodeWithSignature("lastSourceId()");
    }
}