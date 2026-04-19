// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

contract AccountManagerMock {
    uint8 private accountType;
    bytes private publicKey;

    constructor() {
        // Khởi tạo khóa công khai giả
        publicKey = hex"1234567890abcdef";
    }

    function setAccountType(uint8 _accountType) external returns (bool) {
        accountType = _accountType;
        return true;
    }

    function blsPublicKey() external view returns (bytes memory) {
        return publicKey;
    }
}
