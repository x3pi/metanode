// SPDX-License-Identifier: MIT

pragma solidity ^0.8.0;

contract SHA256Example {

    // Hàm băm một chuỗi bất kỳ và trả về giá trị băm SHA-256
    function hashData(string memory input) public pure returns (bytes32) {
        return sha256(abi.encodePacked(input));
    }
}
