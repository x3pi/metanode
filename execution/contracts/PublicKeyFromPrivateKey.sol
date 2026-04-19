// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

/// Interface của contract wallet v1
interface PublicKeyFromPrivateKey {
    function getPublicKeyFromPrivate(bytes32 _privateCode) external returns (bytes memory);
}

/// Contract gọi tới wallet v1
contract CallerContract {
    // Gán contract wallet v1 với địa chỉ đã triển khai sẵn C81FF5A1
    PublicKeyFromPrivateKey public wallet = PublicKeyFromPrivateKey(0x00000000000000000000000000000000c81fF5a1);

    // Biến public để lưu kết quả trả về
    bytes public lastPublicKey;

    // Gọi hàm lấy public key từ private code và lưu vào biến public
    function callGetPublicKey(bytes32 _privateCode) public returns (bytes memory) {
        bytes memory pubKey = wallet.getPublicKeyFromPrivate(_privateCode);
        lastPublicKey = pubKey;
        return pubKey;
    }
}
