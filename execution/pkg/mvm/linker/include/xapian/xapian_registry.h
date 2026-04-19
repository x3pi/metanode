#pragma once

#include <tbb/concurrent_hash_map.h> // Dùng cho bản đồ (map) đồng thời
#include <vector>                    // Dùng cho std::vector
#include <string>                    // Dùng cho std::string
#include <memory>                    // Dùng cho std::shared_ptr
#include <array>                     // Dùng cho std::array trong getGroupHash
#include <cstdint>                   // Dùng cho uint8_t
#include <iomanip>                   // Dùng cho các hàm trợ giúp printHex
#include <iostream>                  // Dùng cho các hàm trợ giúp printHex
#include <mvm/address.h>
#include "xapian/xapian_log.h"
// Khai báo trước để tránh việc bao gồm toàn bộ tệp tiêu đề XapianManager ở đây
class XapianManager;

// --- Hàm trợ giúp (có thể để bên ngoài hoặc là static private) ---
// Tiện ích để in các vùng chứa byte dưới dạng hex (hữu ích cho việc gỡ lỗi các giá trị băm)
template <typename Container>
void printHex(const Container &data, const std::string &label = "Hex")
{
    std::cerr << label << ": ";
    for (uint8_t byte : data)
    {
        std::cerr << std::hex
                  << std::setw(2) << std::setfill('0')
                  << static_cast<int>(byte);
    }
    std::cerr << std::dec << std::endl; // Chuyển lại sang định dạng thập phân
}

// --- Lớp Registry ---
class XapianRegistry
{
public:
    // Hàm khởi tạo (mặc định có lẽ là đủ)
    XapianRegistry() = default;

    // --- Giao diện công khai ---

    /**
     * @brief Đăng ký một thực thể XapianManager cho một mvmId nhất định.
     * Liên kết trình quản lý với khóa mvmId cụ thể. Ngăn chặn
     * việc đăng ký một trình quản lý đã được liên kết với một khóa *khác*.
     * @param mvmId Con trỏ thô đến dữ liệu mvmId (giả định là 32 byte).
     * @param manager Một con trỏ chia sẻ đến thực thể XapianManager.
     */
    void registerManager(unsigned char *mvmId, std::shared_ptr<XapianManager> manager);

    /**
     * @brief Truy xuất tất cả các thực thể XapianManager được liên kết với một mvmId nhất định.
     * @param mvmId Con trỏ thô đến dữ liệu mvmId (giả định là 32 byte).
     * @return Một vector các con trỏ chia sẻ đến các XapianManagers được liên kết.
     * Trả về một vector rỗng nếu mvmId không hợp lệ hoặc không tìm thấy.
     */
    std::vector<std::shared_ptr<XapianManager>> getManagersForMvmId(unsigned char *mvmId) const; // Đánh dấu là const

    /**
     * @brief Hủy đăng ký một thực thể XapianManager cụ thể cho một mvmId nhất định.
     * Chỉ hủy đăng ký nếu khóa liên kết của trình quản lý khớp với khóa của mvmId được cung cấp.
     * Loại bỏ mục mvmId khỏi registry nếu nó trở nên rỗng.
     * @param mvmId Con trỏ thô đến dữ liệu mvmId (giả định là 32 byte).
     * @param manager_to_remove Thực thể trình quản lý cụ thể cần loại bỏ.
     */
    void unregisterManager(unsigned char *mvmId, std::shared_ptr<XapianManager> manager_to_remove);

    /**
     * @brief Hủy đăng ký tất cả các thực thể XapianManager được liên kết với một mvmId nhất định
     * và loại bỏ khóa mvmId khỏi registry.
     * Xóa khóa liên kết trong mỗi trình quản lý bị loại bỏ.
     * @param mvmId Con trỏ thô đến dữ liệu mvmId (giả định là 32 byte).
     */
    void unregisterAllManagersForMvmId(unsigned char *mvmId);

    /**
     * @brief Truy xuất nhật ký thay đổi kết hợp cho tất cả các XapianManagers được liên kết
     * với một mvmId nhất định, được nhóm theo địa chỉ.
     * Sử dụng phương thức getComprehensiveChangeLogs() của mỗi trình quản lý và tổng hợp chúng.
     * @param mvmId Con trỏ thô đến dữ liệu mvmId (giả định là 32 byte).
     * @return Một bản đồ trong đó khóa là địa chỉ và giá trị là các cấu trúc ComprehensiveLog
     * chứa các thay đổi tổng hợp cho tất cả các trình quản lý được liên kết với địa chỉ đó.
     * Trả về một bản đồ rỗng nếu không tìm thấy trình quản lý nào.
     */
    std::map<mvm::Address, XapianLog::ComprehensiveLog> getGroupChangeLogsForMvmId(unsigned char *mvmId) const;

    /**
     * @brief Tính toán một giá trị băm mật mã kết hợp đại diện cho trạng thái
     * của tất cả các XapianManagers được liên kết với một mvmId nhất định.
     * Sử dụng phương thức getComprehensiveStateHash() của mỗi trình quản lý.
     * @param mvmId Con trỏ thô đến dữ liệu mvmId (giả định là 32 byte).
     * @return Một mảng 32 byte đại diện cho giá trị băm Keccak-256. Trả về một giá trị băm bằng không
     * nếu không tìm thấy trình quản lý nào hoặc nếu xảy ra lỗi.
     */
    std::map<mvm::Address, std::array<uint8_t, 32u>> getGroupHashForMvmId(unsigned char *mvmId) const;

    /**
     * @brief Cố gắng xác nhận các thay đổi (ví dụ: lưu dữ liệu, lược đồ, thẻ)
     * cho tất cả các thực thể XapianManager được liên kết với một mvmId nhất định.
     * Gọi saveAllAndCommit() trên mỗi trình quản lý.
     * @param mvmId Con trỏ thô đến dữ liệu mvmId (giả định là 32 byte).
     * @return True nếu việc xác nhận thành công (hoặc không có trình quản lý nào cần xác nhận),
     * False nếu bất kỳ trình quản lý nào không xác nhận được hoặc nếu khóa mvmId không hợp lệ.
     */
    bool commitChangesForMvmId(unsigned char *mvmId);

    /**
     * @brief Cố gắng hoàn tác các thay đổi chưa được xác nhận cho tất cả các XapianManager
     * các thực thể được liên kết với một mvmId nhất định.
     * Điều này bao gồm việc xóa các nhật ký đã được dàn dựng và tải lại trạng thái từ lần xác nhận cuối cùng.
     * Gọi revertUncommittedChanges() trên mỗi trình quản lý liên quan.
     * @param mvmId Con trỏ thô đến dữ liệu mvmId (giả định là 32 byte).
     * @return True nếu việc hoàn tác thành công cho tất cả các trình quản lý (hoặc không có trình quản lý nào cần hoàn tác),
     * False nếu bất kỳ trình quản lý nào không hoàn tác được hoặc nếu khóa mvmId không hợp lệ.
     */
    bool revertChangesForMvmId(unsigned char *mvmId);

    // --- Hàm trợ giúp tĩnh (cũng có thể là private nếu chỉ được sử dụng nội bộ) ---
    /**
     * @brief Chuyển đổi các byte mvmId thô thành một khóa chuỗi được chuẩn hóa.
     * Lấy 20 byte đầu tiên dưới dạng hex và đệm phần còn lại bằng "00".
     * @param mvmId Con trỏ thô đến dữ liệu mvmId. Không được là null.
     * @return Khóa chuỗi được tạo ra, hoặc một chuỗi rỗng nếu mvmId là null.
     */
    static std::string generateMvmIdKey(const unsigned char *mvmId);

    
    void cancelTransaction(unsigned char* mvmId);
    void commitTransaction(unsigned char* mvmId);


private:
    // --- Dữ liệu thành viên ---
    // Bản đồ đồng thời cốt lõi lưu trữ dữ liệu registry.
    tbb::concurrent_hash_map<std::string, std::vector<std::shared_ptr<XapianManager>>> m_mvmId_to_managers;

    // --- Các hàm trợ giúp riêng (nếu cần) ---
    // Ví dụ: Nếu getMvmIdKey chỉ được sử dụng nội bộ, nó có thể là private static:
    // static std::string getMvmIdKey(unsigned char* mvmId);
};

extern XapianRegistry registry;