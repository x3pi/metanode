// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

/**
 * @title ModExpTester
 * @dev Hợp đồng để kiểm tra precompile MODEXP tại địa chỉ 0x05,
 * lưu kết quả cuối cùng vào biến trạng thái công khai.
 */
contract ModExpTester {

    address constant MODEXP_PRECOMPILE_ADDRESS = address(0x05);

    // --- Biến trạng thái công khai để lưu kết quả lần chạy cuối ---
    bytes public lastBase;
    bytes public lastExponent;
    bytes public lastModulus;
    bool public lastSuccess; // Trạng thái thành công của lời gọi precompile cuối
    bytes public lastResult;  // Kết quả trả về (hoặc lỗi) từ precompile cuối

    event ModExpCalledAndStored(
        bytes base,
        bytes exponent,
        bytes modulus,
        bool success,
        bytes result
    );

    /**
     * @dev Gọi precompile MODEXP và lưu kết quả vào các biến trạng thái.
     * @param base Giá trị cơ sở (B) dưới dạng bytes (big-endian).
     * @param exponent Giá trị số mũ (E) dưới dạng bytes (big-endian).
     * @param modulus Giá trị modulus (M) dưới dạng bytes (big-endian).
     * Hàm này không trả về giá trị trực tiếp, hãy đọc các biến public để xem kết quả.
     */
    function testModExp(
        bytes memory base,
        bytes memory exponent,
        bytes memory modulus
    ) public /* Xóa returns */ {
        uint256 bSize = base.length;
        uint256 eSize = exponent.length;
        uint256 mSize = modulus.length;

        // --- Xây dựng calldata cho precompile ---
        bytes memory callData = abi.encodePacked(
            bytes32(bSize),
            bytes32(eSize),
            bytes32(mSize),
            base,
            exponent,
            modulus
        );

        // --- Thực hiện low-level call ---
        (bool success, bytes memory returnData) = MODEXP_PRECOMPILE_ADDRESS.call{gas: 200000}(callData);

        // --- Lưu kết quả vào biến trạng thái ---
        lastBase = base;
        lastExponent = exponent;
        lastModulus = modulus;
        lastSuccess = success;
        lastResult = returnData; // Lưu cả khi thất bại (thường là bytes rỗng)

        // Phát sự kiện
        emit ModExpCalledAndStored(base, exponent, modulus, success, returnData);

        // Không cần require(success) nếu bạn muốn ghi lại cả trường hợp lỗi
        // Nếu bạn muốn revert khi lỗi, hãy đặt require ở đây:
        // require(success, "ModExpTester: Precompile call failed");

        // Không cần return
    }

     /**
      * @dev Hàm tiện ích để kiểm tra với các số nguyên uint256 và lưu kết quả.
      */
     function testModExpWithUint(
         uint256 base,
         uint256 exponent,
         uint256 modulus
     ) public /* Xóa returns */ {
         bytes memory baseBytes = abi.encodePacked(base);
         bytes memory exponentBytes = abi.encodePacked(exponent);
         bytes memory modulusBytes = abi.encodePacked(modulus);

         baseBytes = removeLeadingZeros(baseBytes);
         exponentBytes = removeLeadingZeros(exponentBytes);
         modulusBytes = removeLeadingZeros(modulusBytes);

         if (modulus == 0) {
            modulusBytes = bytes("");
         }
          if (base == 0) {
             baseBytes = bytes("");
         }
         if (exponent == 0) {
             exponentBytes = bytes("");
         }

         // Gọi hàm testModExp để thực hiện và lưu trữ
         testModExp(baseBytes, exponentBytes, modulusBytes);

         // Không cần return
     }

     /**
      * @dev Hàm nội bộ để xóa các byte 0 ở đầu một mảng bytes.
      */
     function removeLeadingZeros(bytes memory _b) internal pure returns (bytes memory) {
         uint256 firstNonZero = 0;
         while (firstNonZero < _b.length && _b[firstNonZero] == 0) {
             firstNonZero++;
         }
         if (firstNonZero == 0) return _b;
         if (firstNonZero == _b.length) return bytes("");

         bytes memory result = new bytes(_b.length - firstNonZero);
         for (uint256 i = 0; i < result.length; i++) {
             result[i] = _b[i + firstNonZero];
         }
         return result;
     }
}