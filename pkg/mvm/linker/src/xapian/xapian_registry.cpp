#include "xapian_registry.h"
#include "xapian/xapian_manager.h" // Giả định header cho XapianManager
#include <mvm/util.h>              // Giả định header cho các tiện ích mvm

#include <tbb/concurrent_hash_map.h>
#include <sstream>
#include <iomanip>
#include <algorithm> // Cần cho std::find, std::remove
#include <vector>
#include <memory> // Cần cho std::shared_ptr
#include <string>
#include <map>
#include <array>
#include <stdexcept> // Vẫn có thể cần nếu XapianManager ném std::exception
#include <iterator>  // Cần cho std::make_move_iterator
#include <utility>   // Cần cho std::pair, std::make_pair, std::move

// Biến registry toàn cục.
XapianRegistry registry;

// Định danh kiểu (type alias) để mã dễ đọc hơn.
using ManagerList = std::vector<std::shared_ptr<XapianManager>>;
// Định nghĩa kiểu cho map, dùng trong value_type
using MvmIdKeyMap = tbb::concurrent_hash_map<std::string, ManagerList>;

// Namespace ẩn danh cho các hàm trợ giúp nội bộ
namespace
{

    /**
     * @brief Logic nội bộ để tạo khóa định danh từ mvmId.
     * @param mvmId Con trỏ đến mảng byte mvmId (20 bytes).
     * @return Chuỗi khóa định danh hoặc chuỗi rỗng nếu mvmId là null.
     */
    std::string generateMvmIdKeyInternal(const unsigned char *mvmId)
    {
        if (mvmId == nullptr)
        {
            return ""; // Trả về rỗng nếu đầu vào là null
        }

        std::stringstream ss;
        ss << std::hex << std::setfill('0');
        // Chuyển đổi 20 byte địa chỉ thành hex
        for (size_t i = 0; i < 20; ++i)
        {
            ss << std::setw(2) << static_cast<int>(mvmId[i]);
        }
        // Thêm phần đệm 12 byte (24 ký tự '0') để đủ 32 byte (64 ký tự hex)
        ss << std::string(24, '0');
        return ss.str();
    }

    /**
     * @brief Hàm trợ giúp nội bộ để nhóm các manager theo địa chỉ mvm::Address của chúng.
     * @param managers Danh sách các con trỏ manager cần nhóm.
     * @return Một map với key là mvm::Address và value là danh sách các manager thuộc địa chỉ đó.
     */
    std::map<mvm::Address, ManagerList> groupManagersByAddress(const ManagerList &managers)
    {
        std::map<mvm::Address, ManagerList> groups;
        for (const auto &manager_ptr : managers)
        {
            if (manager_ptr)
            {                                                        // Chỉ xử lý con trỏ hợp lệ
                groups[manager_ptr->address].push_back(manager_ptr); // Thêm vào nhóm tương ứng
            }
            // Bỏ qua các con trỏ manager null.
        }
        return groups;
    }

} // namespace ẩn danh

//----------------------------------------------------------------------------
// Triển khai các phương thức của lớp XapianRegistry.
//----------------------------------------------------------------------------

/*static*/ std::string XapianRegistry::generateMvmIdKey(const unsigned char *mvmId)
{
    // Hàm bao bọc tĩnh công khai, gọi hàm nội bộ
    return generateMvmIdKeyInternal(mvmId);
}

// Đăng ký một XapianManager với một mvmId cụ thể.
void XapianRegistry::registerManager(unsigned char *mvmId, std::shared_ptr<XapianManager> manager)
{
    // 1. Xác thực đầu vào
    if (!manager)
    {
        // Không làm gì nếu con trỏ manager không hợp lệ
        return;
    }
    const std::string newKey = generateMvmIdKeyInternal(mvmId); // Tạo khóa từ mvmId
    if (newKey.empty())
    {
        // Không làm gì nếu khóa không hợp lệ (mvmId là null)
        return;
    }

    // 2. Kiểm tra xem manager đã được liên kết với khóa nào khác chưa
    const std::string currentAssociatedKey = manager->getAssociatedMvmIdKey();
    if (!currentAssociatedKey.empty())
    {
        if (currentAssociatedKey != newKey)
        {
            // [FIX] Manager đang liên kết với một khóa cũ.
            // (Thường xảy ra do Deploy transaction không bao giờ chạy CommitFullDb, khiến khóa treo vĩnh viễn)
            // Ta BẮT BUỘC phải hủy liên kết cũ trước khi gán cho khóa mới!
            bool old_list_became_empty = false;
            {
                MvmIdKeyMap::accessor old_acc;
                if (m_mvmId_to_managers.find(old_acc, currentAssociatedKey))
                {
                    auto &old_managers = old_acc->second;
                    auto new_end = std::remove(old_managers.begin(), old_managers.end(), manager);
                    if (new_end != old_managers.end())
                    {
                        old_managers.erase(new_end, old_managers.end());
                        old_list_became_empty = old_managers.empty();
                    }
                }
            } // old_acc hết scope và unlock map.
            if (old_list_became_empty)
            {
                m_mvmId_to_managers.erase(currentAssociatedKey);
            }
            manager->clearAssociatedMvmIdKeyInternal(); // Xóa khóa cũ
        }
        else
        {
            // Manager đã được liên kết chính xác với khóa này rồi. Không cần làm gì thêm.
            return;
        }
    }

    // 3. Tiến hành đăng ký/truy cập bằng TBB accessor để đảm bảo an toàn luồng
    MvmIdKeyMap::accessor acc;
    // **SỬA LỖI:** Sử dụng std::make_pair để tạo value_type tường minh cho insert
    // Thay vì: m_mvmId_to_managers.insert(acc, {newKey, {}});
    m_mvmId_to_managers.insert(acc, std::make_pair(newKey, ManagerList{}));
    // insert sẽ tạo mục mới nếu newKey chưa tồn tại, nếu đã tồn tại thì acc sẽ trỏ đến mục đó.

    // 4. Thêm manager vào vector (danh sách manager) cho khóa này nếu chưa có
    auto &managers = acc->second; // Tham chiếu đến vector<shared_ptr> trong map
    // Kiểm tra xem manager đã có trong danh sách chưa
    if (std::find(managers.begin(), managers.end(), manager) == managers.end())
    {
        managers.push_back(manager);                    // Thêm vào cuối nếu chưa có
        manager->setAssociatedMvmIdKeyInternal(newKey); // Đặt khóa liên kết trên manager
    }
    // Accessor (acc) được tự động giải phóng khi ra khỏi scope (RAII).
}

// Lấy danh sách các XapianManager được đăng ký với một mvmId.
std::vector<std::shared_ptr<XapianManager>> XapianRegistry::getManagersForMvmId(unsigned char *mvmId) const
{
    const std::string key = generateMvmIdKeyInternal(mvmId); // Tạo khóa
    if (key.empty())
    {
        return {}; // Trả về vector rỗng nếu khóa không hợp lệ
    }

    MvmIdKeyMap::const_accessor const_acc; // Accessor chỉ đọc
    // Tìm kiếm trong map
    if (m_mvmId_to_managers.find(const_acc, key))
    {
        // Tìm thấy, trả về bản sao của vector manager
        return const_acc->second;
    }

    return {}; // Không tìm thấy khóa, trả về vector rỗng
}

// Hủy đăng ký một XapianManager cụ thể khỏi một mvmId.
void XapianRegistry::unregisterManager(unsigned char *mvmId, std::shared_ptr<XapianManager> manager_to_remove)
{
    // 1. Xác thực đầu vào
    if (!manager_to_remove)
    {
        return; // Bỏ qua nếu manager null
    }
    const std::string key = generateMvmIdKeyInternal(mvmId); // Tạo khóa
    if (key.empty())
    {
        return; // Bỏ qua nếu khóa không hợp lệ
    }

    // 2. Xác minh manager thực sự được liên kết với khóa này trước khi hủy đăng ký.
    std::string managerKey = manager_to_remove->getAssociatedMvmIdKey();
    if (managerKey != key)
    {
        // Nếu manager không liên kết với khóa này, không làm gì cả.
        return;
    }

    // 3. Tìm và xóa manager khỏi danh sách liên kết với khóa
    bool removed_from_list = false; // Cờ theo dõi việc xóa khỏi vector
    bool list_became_empty = false; // Cờ theo dõi nếu vector trở nên rỗng
    {                               // Sử dụng scope riêng cho accessor để đảm bảo nó được giải phóng trước khi xóa map (nếu cần)
        MvmIdKeyMap::accessor acc;
        if (m_mvmId_to_managers.find(acc, key))
        {                                 // Tìm khóa trong map
            auto &managers = acc->second; // Lấy tham chiếu đến vector manager
            // Sử dụng thuật toán remove-erase để xóa manager khỏi vector
            auto new_end = std::remove(managers.begin(), managers.end(), manager_to_remove);

            if (new_end != managers.end())
            {                                                         // Nếu tìm thấy manager và đã đưa về cuối
                managers.erase(new_end, managers.end());              // Thực hiện xóa khỏi vector
                manager_to_remove->clearAssociatedMvmIdKeyInternal(); // Xóa liên kết trên đối tượng manager
                removed_from_list = true;                             // Đánh dấu đã xóa
                list_became_empty = managers.empty();                 // Kiểm tra xem vector có rỗng không sau khi xóa
            }
            // Nếu không tìm thấy (new_end == managers.end()), không làm gì cả
        }
        // Accessor (acc) được tự động giải phóng khi ra khỏi scope.
    } // Kết thúc phạm vi accessor

    // 4. Nếu danh sách manager cho khóa này trở nên rỗng sau khi xóa, xóa luôn khóa khỏi map.
    if (removed_from_list && list_became_empty)
    {
        // Thực hiện xóa map bên ngoài phạm vi lock của accessor
        m_mvmId_to_managers.erase(key);
    }
}

// Hủy đăng ký tất cả các XapianManager liên kết với một mvmId.
void XapianRegistry::unregisterAllManagersForMvmId(unsigned char *mvmId)
{
    const std::string key = generateMvmIdKeyInternal(mvmId); // Tạo khóa
    if (key.empty())
    {
        return; // Bỏ qua nếu khóa không hợp lệ
    }

    bool key_found = false; // Cờ để biết có cần thử xóa khóa khỏi map hay không
    {                       // Scope riêng cho accessor
        MvmIdKeyMap::accessor acc;
        if (m_mvmId_to_managers.find(acc, key))
        { // Tìm khóa
            key_found = true;
            auto &managers = acc->second; // Lấy tham chiếu đến vector

            // Xóa liên kết khóa khỏi tất cả các manager trong danh sách này
            for (const auto &manager_ptr : managers)
            {
                if (manager_ptr)
                {
                    manager_ptr->clearAssociatedMvmIdKeyInternal(); // Gọi hàm xóa liên kết trên manager
                }
            }
            // Vector managers sẽ bị xóa khi mục trong map bị xóa bên dưới
            // Accessor (acc) tự động giải phóng.
        }
    } // Kết thúc scope accessor

    if (key_found)
    { // Chỉ thử xóa khỏi map nếu khóa đã được tìm thấy
        // Thực hiện xóa map bên ngoài phạm vi lock của accessor
        m_mvmId_to_managers.erase(key);
    }
}

// Tính toán và trả về hash trạng thái tổng hợp cho từng nhóm địa chỉ liên kết với mvmId.
std::map<mvm::Address, std::array<uint8_t, 32u>>
XapianRegistry::getGroupHashForMvmId(unsigned char *mvmId) const
{
    std::map<mvm::Address, std::array<uint8_t, 32u>> result_map; // Map kết quả
    // Lấy danh sách các manager cho mvmId này
    std::vector<std::shared_ptr<XapianManager>> managers = getManagersForMvmId(mvmId);

    if (managers.empty())
    {
        return result_map; // Trả về map rỗng nếu không có manager nào
    }

    // Nhóm các manager theo địa chỉ mvm::Address của chúng
    std::map<mvm::Address, ManagerList> groups = groupManagersByAddress(managers);

    if (groups.empty())
    {
        return result_map; // Trả về map rỗng nếu không tạo được nhóm nào
    }

    // Lặp qua từng nhóm địa chỉ
    for (const auto &[group_address, managers_in_group] : groups)
    {
        std::vector<uint8_t> concatenated_hashes_in_group; // Vector chứa các hash nối lại của nhóm
        // Dự trữ bộ nhớ để tăng hiệu quả
        concatenated_hashes_in_group.reserve(managers_in_group.size() * 32);

        // Lặp qua từng manager trong nhóm
        for (const auto &manager_ptr : managers_in_group)
        {
            if (manager_ptr)
            {
                // Lấy hash trạng thái toàn diện (bao gồm log, tags, schema nếu có) từ mỗi manager
                std::array<uint8_t, 32u> individual_hash = manager_ptr->getComprehensiveStateHash();
                // Nối hash cá nhân vào vector tổng hợp của nhóm
                concatenated_hashes_in_group.insert(concatenated_hashes_in_group.end(), individual_hash.begin(), individual_hash.end());
            }
        }

        // Tính hash cuối cùng cho nhóm nếu có dữ liệu hash
        if (!concatenated_hashes_in_group.empty())
        {
            // Tính toán hash Keccak-256 cho toàn bộ các hash đã nối của nhóm
            mvm::KeccakHash group_hash = mvm::keccak_256(concatenated_hashes_in_group);
            result_map[group_address] = group_hash; // Lưu hash của nhóm vào map kết quả
        }
        else
        {
            // Nếu không có hash nào được tạo cho nhóm (vd: nhóm rỗng hoặc các manager không có thay đổi)
            result_map[group_address] = std::array<uint8_t, 32u>{0}; // Thêm hash không vào kết quả
        }
    }
    return result_map; // Trả về map chứa hash của từng nhóm địa chỉ
}

// Lấy log thay đổi tổng hợp cho từng nhóm địa chỉ liên kết với mvmId.
std::map<mvm::Address, XapianLog::ComprehensiveLog>
XapianRegistry::getGroupChangeLogsForMvmId(unsigned char *mvmId) const
{
    std::map<mvm::Address, XapianLog::ComprehensiveLog> result_logs; // Map kết quả
    // Lấy danh sách manager
    std::vector<std::shared_ptr<XapianManager>> managers = getManagersForMvmId(mvmId);

    if (managers.empty())
    {
        return result_logs; // Trả về rỗng nếu không có manager
    }

    // Nhóm manager theo địa chỉ
    std::map<mvm::Address, ManagerList> groups = groupManagersByAddress(managers);

    if (groups.empty())
    {
        return result_logs; // Trả về rỗng nếu không có nhóm
    }

    // Lặp qua từng nhóm địa chỉ
    for (const auto &[group_address, managers_in_group] : groups)
    {
        if (managers_in_group.empty())
        {
            continue; // Bỏ qua nhóm rỗng
        }

        XapianLog::ComprehensiveLog aggregated_log_for_group; // Log tổng hợp cho nhóm này

        // Lấy db_name từ manager hợp lệ đầu tiên trong nhóm
        bool db_name_set = false;
        for (const auto &manager_ptr : managers_in_group)
        {
            if (manager_ptr)
            {
                aggregated_log_for_group.db_name = manager_ptr->getDbName(); // Lấy tên db
                db_name_set = true;
                break; // Chỉ cần lấy từ cái đầu tiên
            }
        }
        if (!db_name_set)
        {
            aggregated_log_for_group.db_name = ""; // Đặt là rỗng nếu không tìm thấy
        }

        // Tổng hợp các bản ghi log (LogEntry) từ tất cả manager trong nhóm
        for (const auto &manager_ptr : managers_in_group)
        {
            if (manager_ptr)
            {
                // Lấy ComprehensiveLog từ manager (chỉ chứa log của manager đó)
                XapianLog::ComprehensiveLog manager_log = manager_ptr->getComprehensiveChangeLogs();

                // Di chuyển (move) các bản ghi log từ manager_log vào log tổng hợp của nhóm
                // để tránh sao chép không cần thiết, tăng hiệu quả.
                aggregated_log_for_group.xapian_doc_logs.insert(
                    aggregated_log_for_group.xapian_doc_logs.end(),
                    std::make_move_iterator(manager_log.xapian_doc_logs.begin()),
                    std::make_move_iterator(manager_log.xapian_doc_logs.end()));
                // Logic tương tự nếu cần tổng hợp các loại log khác (schema, tags)
            }
        } // Kết thúc lặp qua manager trong nhóm

        // Lưu trữ log tổng hợp cho địa chỉ nhóm này vào map kết quả
        result_logs[group_address] = std::move(aggregated_log_for_group); // Sử dụng move

    } // Kết thúc lặp qua các nhóm địa chỉ

    return result_logs; // Trả về map chứa log tổng hợp của từng nhóm
}

// Commit tất cả các thay đổi đã staged cho các manager liên kết với mvmId.
bool XapianRegistry::commitChangesForMvmId(unsigned char *mvmId)
{
    std::vector<std::shared_ptr<XapianManager>> managers = getManagersForMvmId(mvmId);
    const std::string key = generateMvmIdKeyInternal(mvmId); // Tạo khóa để kiểm tra mvmId hợp lệ

    if (managers.empty())
    {
        std::cerr << "[DEBUG_COMMIT] commitChangesForMvmId (" << key << ") -> managers empty! 0 managers to commit." << std::endl;
        // Nếu mvmId hợp lệ nhưng không có manager, coi như thành công
        return !key.empty();
    }
    std::cerr << "[DEBUG_COMMIT] commitChangesForMvmId (" << key << ") -> iterating " << managers.size() << " managers to commit." << std::endl;

    bool all_succeeded = true; // Cờ theo dõi thành công tổng thể

    // Lặp qua từng manager và gọi commit
    for (const auto &manager_ptr : managers)
    {
        if (manager_ptr)
        {
            // Gọi saveAllAndCommit (hiện tại tương đương commit_changes)
            if (!manager_ptr->saveAllAndCommit())
            {
                all_succeeded = false; // Đánh dấu thất bại nếu một commit không thành công
                                       // Có thể dừng lại ngay hoặc tiếp tục commit các manager khác (hiện tại là tiếp tục)
            }
        }
        else
        {
            // Bỏ qua con trỏ null (lỗi logic nếu xảy ra)
            // all_succeeded = false; // Nếu muốn coi đây là lỗi
        }
    }

    // [FIX] Xóa khỏi registry sau khi commit để manager (static instance)
    // có thể được gán cho transaction (mvmId) tiếp theo, tránh leak memory và lỗi "managers empty!"
    unregisterAllManagersForMvmId(mvmId);

    return all_succeeded; // Trả về true nếu tất cả commit thành công
}

// Hoàn tác (revert) tất cả các thay đổi chưa commit cho các manager liên kết với mvmId.
bool XapianRegistry::revertChangesForMvmId(unsigned char *mvmId)
{
    std::vector<std::shared_ptr<XapianManager>> managers = getManagersForMvmId(mvmId);
    const std::string key = generateMvmIdKeyInternal(mvmId); // Tạo khóa để kiểm tra mvmId hợp lệ

    if (managers.empty())
    {
        // Nếu mvmId hợp lệ nhưng không có manager, coi như thành công
        return !key.empty();
    }

    bool all_succeeded = true; // Cờ theo dõi thành công tổng thể

    // Lặp qua từng manager và gọi revert
    for (const auto &manager_ptr : managers)
    {
        if (manager_ptr)
        {
            // Gọi hàm revert trên từng manager
            if (!manager_ptr->revertUncommittedChanges())
            {
                all_succeeded = false; // Đánh dấu thất bại nếu một revert không thành công
                                       // Có thể dừng lại ngay hoặc tiếp tục revert các manager khác (hiện tại là tiếp tục)
            }
        }
        else
        {
            // Bỏ qua con trỏ null
            // all_succeeded = false; // Nếu muốn coi đây là lỗi
        }
    }

    // [FIX] Xóa khỏi registry sau khi revert
    unregisterAllManagersForMvmId(mvmId);

    return all_succeeded; // Trả về true nếu tất cả revert thành công
}

// Hoàn tác (revert) tất cả các thay đổi chưa commit cho các manager liên kết với mvmId.
void XapianRegistry::cancelTransaction(unsigned char *mvmId)
{
    std::vector<std::shared_ptr<XapianManager>> managers = getManagersForMvmId(mvmId);
    const std::string key = generateMvmIdKeyInternal(mvmId);

    if (managers.empty())
    {
        return;
    }

    for (const auto &manager_ptr : managers)
    {
        if (manager_ptr)
        {
            std::lock_guard<std::mutex> lock(manager_ptr->changes_mutex);
            if (manager_ptr->has_started)
            {
                try {
                    manager_ptr->removeLogsUntilNearestEndCommand();
                    manager_ptr->db.cancel_transaction();
                } catch (...) {
                    // Bỏ qua lỗi khi cancel
                }
                manager_ptr->has_started = false;
            }
        }
    }
}

// Commit transaction cho các manager liên kết với mvmId.
void XapianRegistry::commitTransaction(unsigned char *mvmId)
{
    std::vector<std::shared_ptr<XapianManager>> managers = getManagersForMvmId(mvmId);
    const std::string key = generateMvmIdKeyInternal(mvmId);

    if (managers.empty())
    {
        return;
    }

    for (const auto &manager_ptr : managers)
    {
        if (manager_ptr)
        {
            std::lock_guard<std::mutex> lock(manager_ptr->changes_mutex);
            if (manager_ptr->has_started)
            {
                try {
                    manager_ptr->db.commit_transaction();
                } catch (...) {
                    // Bỏ qua lỗi khi commit
                }
                manager_ptr->has_started = false;
            }
        }
    }
}
