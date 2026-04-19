// SPDX-License-Identifier: MIT
pragma solidity ^0.8.19;

/**
 * @title EcAddCaller
 * @notice Hợp đồng gọi precompile ECADD (0x06) với đầu vào là 4 tọa độ
 * và lưu trữ kết quả vào các biến public.
 */
contract EcAddCaller {

    address constant ECADD_PRECOMPILE_ADDRESS = address(0x06);
    // Cung cấp đủ gas cho precompile (chi phí cơ bản là 150) + chi phí gọi
    uint constant PRECOMPILE_GAS = 500;

    // --- Biến trạng thái public để lưu trữ kết quả của lần gọi cuối cùng ---
    uint256 public resultX; // Tọa độ X của điểm kết quả
    uint256 public resultY; // Tọa độ Y của điểm kết quả
    bool public lastCallSuccess; // Trạng thái thành công của lần gọi precompile cuối cùng
    string public lastError; // Lưu trữ thông báo lỗi nếu có

    /**
     * @notice Thực hiện phép cộng điểm P1(x1, y1) + P2(x2, y2) trên đường cong alt_bn128
     * sử dụng precompile tại địa chỉ 0x06.
     * Kết quả (x, y) hoặc trạng thái lỗi được lưu vào các biến public.
     * @param x1 Tọa độ X của điểm thứ nhất.
     * @param y1 Tọa độ Y của điểm thứ nhất.
     * @param x2 Tọa độ X của điểm thứ hai.
     * @param y2 Tọa độ Y của điểm thứ hai.
     */
    function performEcAdd(
        uint256 x1, uint256 y1,
        uint256 x2, uint256 y2
    ) public {
        // Đặt lại trạng thái từ các lần gọi trước
        lastCallSuccess = false;
        lastError = "";
        resultX = 0;
        resultY = 0;

        // 1. Đóng gói 4 tham số uint256 thành 128 bytes dữ liệu đầu vào
        bytes memory inputData = abi.encodePacked(x1, y1, x2, y2);
        // Kiểm tra cơ bản (luôn là 128 nếu đầu vào là uint256)
        require(inputData.length == 128, "Internal error: Input encoding failed");

        // 2. Gọi precompile bằng staticcall (an toàn hơn vì không thay đổi state)
        (bool success, bytes memory result) = ECADD_PRECOMPILE_ADDRESS.staticcall{gas: PRECOMPILE_GAS}(inputData);

        // 3. Xử lý kết quả trả về từ precompile
        if (success) {
            // Lệnh gọi thành công ở cấp độ EVM
            if (result.length == 64) {
                // Độ dài output mong đợi cho phép cộng điểm thành công
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
                lastError = "Precompile success, but no data returned (Invalid input points)";
            } else {
                // Precompile thành công nhưng trả về độ dài dữ liệu không mong đợi
                lastCallSuccess = false;
                lastError = "Precompile success, but returned unexpected data length";
            }
        } else {
            // Lệnh gọi thất bại ở cấp độ EVM (ví dụ: hết gas, revert nội bộ)
            // Lưu ý: Lỗi precompile do đầu vào không hợp lệ thường trả về success=true, result.length=0
            // Đường dẫn này thường chỉ ra lỗi OOG hoặc vấn đề sâu hơn.
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