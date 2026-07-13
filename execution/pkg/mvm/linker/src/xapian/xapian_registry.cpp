#include "xapian_registry.h"
#include "xapian/xapian_manager.h" // Giả định header cho XapianManager
#include <mvm/util.h>              // Giả định header cho các tiện ích mvm
#include "my_extension/utils.h"

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

std::string XapianRegistry::generateMvmIdKey(const unsigned char *mvmId)
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



void XapianRegistry::clearBufferForTxHash(const uint256_t* txHash) {
    if (!txHash) return;
    std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
    
    XapianManager::instances_mutex.lock_shared();
    for (auto& pair : XapianManager::instances) {
        if (auto manager_ptr = pair.second) {
            std::lock_guard<std::mutex> buffer_lock(manager_ptr->tx_buffers_mutex);
            manager_ptr->tx_buffers.erase(txHashStr);
            manager_ptr->tx_counters.erase(txHashStr);
        }
    }
    XapianManager::instances_mutex.unlock_shared();
}

void XapianRegistry::commitBufferForTxHash(const uint256_t* txHash) {
    if (!txHash) return;
    std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
    std::cerr << "[DEBUG] commitBufferForTxHash CALLED for txHash: " << txHashStr << std::endl;
    
    std::shared_lock<std::shared_mutex> inst_lock(XapianManager::instances_mutex);
    for (auto& pair : XapianManager::instances) {
        auto manager_ptr = pair.second;
        if (manager_ptr) {
            std::vector<XapianLog::LogEntry> buffer_logs;
            {
                std::lock_guard<std::mutex> buffer_lock(manager_ptr->tx_buffers_mutex);
                auto it = manager_ptr->tx_buffers.find(txHashStr);
                if (it != manager_ptr->tx_buffers.end()) {
                    buffer_logs = it->second.xapian_doc_logs; // COPY instead of move
                }
            }
            
            if (!buffer_logs.empty()) {
                std::cerr << "[DEBUG] commitBufferForTxHash FOUND " << buffer_logs.size() << " logs for txHash: " << txHashStr << ". Committing!" << std::endl;
                
                // Replay logs into the actual Xapian DB for this manager (replay_log will lock changes_mutex internally)
                manager_ptr->replay_log(buffer_logs);
                

                
                // Append them to comprehensive_log so they can be extracted
                manager_ptr->comprehensive_log.xapian_doc_logs.insert(
                    manager_ptr->comprehensive_log.xapian_doc_logs.end(),
                    std::make_move_iterator(buffer_logs.begin()),
                    std::make_move_iterator(buffer_logs.end())
                );
                
                // NOW it is safe to erase from tx_buffers, because read_db has the data
                {
                    std::lock_guard<std::mutex> buffer_lock(manager_ptr->tx_buffers_mutex);
                    manager_ptr->tx_buffers.erase(txHashStr);
                    manager_ptr->tx_counters.erase(txHashStr);
                }
            }
        }
    }
}
