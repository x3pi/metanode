// SPDX-License-Identifier: MIT
pragma solidity ^0.8.19;

/**
 * @title EcMulCaller
 * @notice Hợp đồng gọi precompile ECMUL (0x07 - nhân vô hướng alt_bn128)
 * với điểm và số vô hướng do người dùng cung cấp, và lưu trữ kết quả.
 */
contract EcMulCaller {

    // Địa chỉ precompile ECMUL
    address constant ECMUL_PRECOMPILE_ADDRESS = address(0x07);
    // Cung cấp đủ gas cho precompile (chi phí cơ bản là 6000) + chi phí gọi
    uint constant PRECOMPILE_GAS = 7000;

    // --- Biến trạng thái public để lưu trữ kết quả của lần gọi cuối cùng ---
    uint256 public resultX; // Tọa độ X của điểm kết quả [s]P
    uint256 public resultY; // Tọa độ Y của điểm kết quả [s]P
    bool public lastCallSuccess; // Trạng thái thành công của lần gọi precompile cuối cùng
    string public lastError; // Lưu trữ thông báo lỗi nếu có

    /**
     * @notice Thực hiện phép nhân vô hướng [s]P(pX, pY) trên đường cong alt_bn128
     * sử dụng precompile tại địa chỉ 0x07.
     * Kết quả (x, y) hoặc trạng thái lỗi được lưu vào các biến public.
     * @param pX Tọa độ X của điểm P.
     * @param pY Tọa độ Y của điểm P.
     * @param s Số vô hướng để nhân.
     */
    function performEcMul(
        uint256 pX, uint256 pY, // Tọa độ điểm P
        uint256 s             // Số vô hướng s
    ) public {
        // Đặt lại trạng thái từ các lần gọi trước
        lastCallSuccess = false;
        lastError = "";
        resultX = 0;
        resultY = 0;

        // 1. Đóng gói 3 tham số uint256 thành 96 bytes dữ liệu đầu vào (32+32+32)
        bytes memory inputData = abi.encodePacked(pX, pY, s);
        // Kiểm tra cơ bản (luôn là 96 nếu đầu vào là uint256)
        require(inputData.length == 96, "Internal error: Input encoding failed");

        // 2. Gọi precompile bằng staticcall
        (bool success, bytes memory result) = ECMUL_PRECOMPILE_ADDRESS.staticcall{gas: PRECOMPILE_GAS}(inputData);

        // 3. Xử lý kết quả trả về từ precompile
        if (success) {
            // Lệnh gọi thành công ở cấp độ EVM
            if (result.length == 64) {
                // Độ dài output mong đợi cho phép nhân vô hướng thành công
                // Giải mã 64 bytes kết quả thành hai giá trị uint256 (x, y)
                (uint256 rX, uint256 rY) = abi.decode(result, (uint256, uint256));
                // Lưu kết quả vào biến trạng thái public
                resultX = rX;
                resultY = rY;
                lastCallSuccess = true;
                lastError = ""; // Không có lỗi
            } else if (result.length == 0) {
                // Precompile thành công nhưng không trả về dữ liệu -> đầu vào không hợp lệ
                // (ví dụ: điểm không trên đường cong, tọa độ >= modulus)
                lastCallSuccess = false;
                lastError = "Precompile success, but no data returned (Invalid input point)";
            } else {
                // Precompile thành công nhưng trả về độ dài dữ liệu không mong đợi
                lastCallSuccess = false;
                lastError = "Precompile success, but returned unexpected data length";
            }
        } else {
            // Lệnh gọi thất bại ở cấp độ EVM (ví dụ: hết gas, revert nội bộ)
            lastCallSuccess = false;
            lastError = "Precompile staticcall failed (check gas or internal error)";
        }
    }

    /**
     * @notice Hàm xem (view) tiện ích để lấy kết quả cuối cùng.
     * @return success Trạng thái của lần gọi cuối cùng.
     * @return x Tọa độ X kết quả (0 nếu lỗi).
     * @return y Tọa độ Y kết quả (0 nếu lỗi).
     * @return err Thông báo lỗi (rỗng nếu thành công).
     */
    function getLastResult() public view returns (bool success, uint256 x, uint256 y, string memory err) {
        return (lastCallSuccess, resultX, resultY, lastError);
    }
}

