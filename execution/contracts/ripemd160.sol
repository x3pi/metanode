// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0; // Bạn có thể dùng phiên bản 0.8.x hoặc mới hơn

/**
 * @title RIPEMD160Test
 * @dev Contract này dùng để kiểm tra hàm băm RIPEMD-160 tích hợp sẵn trong Solidity.
 */
contract RIPEMD160Test {

    /**
     * @notice Tính toán giá trị băm RIPEMD-160 cho một dãy byte đầu vào.
     * @dev Hàm này nhận trực tiếp một dãy byte động.
     * Khi gọi từ bên ngoài, bạn thường cung cấp giá trị byte dưới dạng hex (ví dụ: "0x616263" cho "abc").
     * @param inputData Dãy byte cần được băm.
     * @return result Giá trị băm RIPEMD-160 (dạng bytes20).
     */
    function calculateRIPEMD160_Bytes(bytes memory inputData)
        public
        pure
        returns (bytes20 result)
    {
        // Gọi hàm ripemd160 tích hợp sẵn của Solidity
        // Nó nhận 'bytes' và trả về 'bytes20' (160 bits)
        result = ripemd160(inputData);
    }

    /**
     * @notice Tính toán giá trị băm RIPEMD-160 cho một chuỗi ký tự đầu vào.
     * @dev Chuỗi sẽ được chuyển đổi thành bytes bằng abi.encodePacked trước khi băm.
     * Đây là cách phổ biến nếu bạn muốn băm một chuỗi cụ thể.
     * @param inputString Chuỗi ký tự cần được băm.
     * @return result Giá trị băm RIPEMD-160 (dạng bytes20).
     */
    function calculateRIPEMD160_String(string memory inputString)
        public
        pure
        returns (bytes20 result)
    {
        // 1. Mã hóa chuỗi thành bytes (thường là UTF-8)
        bytes memory encodedData = abi.encodePacked(inputString);

        // 2. Băm dãy byte đã mã hóa bằng ripemd160
        result = ripemd160(encodedData);
    }
}