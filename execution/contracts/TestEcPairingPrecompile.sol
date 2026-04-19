// SPDX-License-Identifier: MIT
pragma solidity ^0.8.9;

contract PairingExampleModified {
    // Biến công khai để lưu trữ kết quả của phép pairing
    // Solidity sẽ tự động tạo một hàm getter cho biến public này
    uint public pairingResult;

    // Hàm này sẽ thực hiện phép tính pairing và lưu kết quả vào biến pairingResult
    function runPairingCheck() public {
        uint256[12] memory input;
        //(G1)x
        input[0] = uint256(0x2cf44499d5d27bb186308b7af7af02ac5bc9eeb6a3d147c186b21fb1b76e18da);
        //(G1)y
        input[1] = uint256(0x2c0f001f52110ccfe69108924926e45f0b0c868df0e7bde1fe16d3242dc715f6);
        //(G2)x_1
        input[2] = uint256(0x1fb19bb476f6b9e44e2a32234da8212f61cd63919354bc06aef31e3cfaff3ebc);
        //(G2)x_0
        input[3] = uint256(0x22606845ff186793914e03e21df544c34ffe2f2f3504de8a79d9159eca2d98d9);
        //(G2)y_1
        input[4] = uint256(0x2bd368e28381e8eccb5fa81fc26cf3f048eea9abfdd85d7ed3ab3698d63e4f90);
        //(G2)y_0
        input[5] = uint256(0x2fe02e47887507adf0ff1743cbac6ba291e66f59be6bd763950bb16041a0a85e);
        //(G1)x
        input[6] = uint256(0x0000000000000000000000000000000000000000000000000000000000000001);
        //(G1)y
        input[7] = uint256(0x30644e72e131a029b85045b68181585d97816a916871ca8d3c208c16d87cfd45);
        //(G2)x_1
        input[8] = uint256(0x1971ff0471b09fa93caaf13cbf443c1aede09cc4328f5a62aad45f40ec133eb4);
        //(G2)x_0
        input[9] = uint256(0x091058a3141822985733cbdddfed0fd8d6c104e9e9eff40bf5abfef9ab163bc7);
        //(G2)y_1
        input[10] = uint256(0x2a23af9a5ce2ba2796c1f4e453a370eb0af8c212d9dc9acd8fc02c2e907baea2);
        //(G2)y_0
        input[11] = uint256(0x23a8eb0b0996252cb548a4487da97b02422ebc0e834613f954de6c7e0afdc1fc);

        // Biến tạm để lưu kết quả từ precompile
        uint success;

        // Thực hiện gọi precompile pairing check (địa chỉ 0x08)
        assembly {
            // call(gas, to, value, in, insize, out, outsize)
            // Kết quả (0 hoặc 1) sẽ được ghi vào vùng nhớ của biến 'success'
            success := call(gas(), 0x08, 0, input, 0x0180, 0, 0x20)
            // Nếu lời gọi thất bại (success == 0), revert transaction
            if iszero(success) {
                revert(0, 0)
            }
            // Đọc kết quả từ vùng nhớ trả về (đặt tại offset 0, dài 32 bytes) và lưu vào biến success
            success := mload(0)
        }

        // Lưu kết quả (1 nếu thành công, sẽ là giá trị trong success sau khi mload)
        // vào biến trạng thái công khai pairingResult
        pairingResult = success;

        // Hàm này không cần trả về gì nữa vì kết quả đã được lưu trữ
    }

    // Bạn có thể gọi hàm getter công khai được tạo tự động `pairingResult()`
    // để đọc giá trị đã lưu trữ sau khi chạy `runPairingCheck()`.
}