// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

contract EventDemo {
    event ValueChanged(
        address indexed caller,
        uint256 oldValue,
        uint256 newValue,
        uint256 blockTime
    );

    // Sự kiện khi giá trị được tăng thêm
    event ValueIncreased(
        address indexed user, 
        uint256 amountAdded, 
        uint256 newTotal
    );

    uint256 public value;

    // Hàm cập nhật giá trị
    function setValue(uint256 _newValue) external {
        uint256 old = value;
        value = _newValue;

        emit ValueChanged(
            msg.sender,
            old,
            _newValue,
            block.timestamp
        );
    }
    // Hàm tăng giá trị cũ thêm một khoảng _amount
    function increaseValue(uint256 _amount) external {
        require(_amount > 0, "Amount must be greater than 0");
        
        value += _amount; // Cộng dồn

        // Bắn sự kiện
        emit ValueIncreased(msg.sender, _amount, value);
    }
    // Hàm lấy giá trị hiện tại
    function getValue() external view returns (uint256) {
        return value;
    }
}