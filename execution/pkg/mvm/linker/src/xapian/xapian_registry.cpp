#include "xapian_registry.h"
#include "mvm_linker.hpp"
#include "xapian/xapian_manager.h" // Giả định header cho XapianManager
#include <mvm/util.h>              // Giả định header cho các tiện ích mvm
#include "my_extension/utils.h"

#include <tbb/concurrent_hash_map.h>
#include <cstring>
#include <filesystem>
#include <iostream>
#include <optional>
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
#include <unordered_set>

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
    
    std::shared_lock<std::shared_mutex> inst_lock(XapianManager::instances_mutex);
    for (auto& pair : XapianManager::instances) {
        if (auto manager_ptr = pair.second) {
            std::lock_guard<std::mutex> buffer_lock(manager_ptr->tx_buffers_mutex);
            manager_ptr->tx_buffers.erase(txHashStr);
            manager_ptr->tx_counters.erase(txHashStr);
            manager_ptr->clear_writer_predecessors_for_tx(txHashStr);
        }
    }
}

void XapianRegistry::clearBuffersForTxHashBatch(const unsigned char* b_tx_hashes, int count) {
    if (!b_tx_hashes || count <= 0) return;
    std::vector<std::string> txHashStrs;
    txHashStrs.reserve(static_cast<size_t>(count));
    for (int i = 0; i < count; ++i) {
        uint256_t txHash = mvm::from_big_endian(reinterpret_cast<const uint8_t*>(b_tx_hashes + i * 32), 32u);
        txHashStrs.push_back(mvm::to_hex_string_fixed(txHash, 64));
    }

    std::shared_lock<std::shared_mutex> inst_lock(XapianManager::instances_mutex);
    for (auto& pair : XapianManager::instances) {
        if (auto manager_ptr = pair.second) {
            std::lock_guard<std::mutex> buffer_lock(manager_ptr->tx_buffers_mutex);
            for (const auto& txHashStr : txHashStrs) {
                manager_ptr->tx_buffers.erase(txHashStr);
                manager_ptr->tx_counters.erase(txHashStr);
                manager_ptr->clear_writer_predecessors_for_tx(txHashStr);
            }
        }
    }
}

void XapianRegistry::clearAllBuffers() {
    std::shared_lock<std::shared_mutex> inst_lock(XapianManager::instances_mutex);
    for (auto& pair : XapianManager::instances) {
        if (auto manager_ptr = pair.second) {
            manager_ptr->clearAllTxBuffers();
        }
    }
}

void XapianRegistry::commitBufferForTxHash(const uint256_t* txHash) {
    if (!txHash) return;
    std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
    
    std::shared_lock<std::shared_mutex> inst_lock(XapianManager::instances_mutex);
    for (auto& pair : XapianManager::instances) {
        auto manager_ptr = pair.second;
        if (manager_ptr) {
            std::vector<XapianLog::LogEntry> buffer_logs;
            {
                std::lock_guard<std::mutex> buffer_lock(manager_ptr->tx_buffers_mutex);
                auto it = manager_ptr->tx_buffers.find(txHashStr);
                if (it != manager_ptr->tx_buffers.end()) {
                    // BẮT BUỘC DÙNG COPY (Không dùng std::move)
                    // Nếu dùng std::move, mảng bên trong tx_buffers sẽ lập tức bị rỗng.
                    // Trong khoảng thời gian từ lúc move đến khi replay_log ghi xong vào Xapian DB,
                    // các transaction song song khác trong Block-STM (cross-reading) nếu query txHash này
                    // sẽ thấy mảng rỗng -> Phantom Read / sai lệch state.
                    // Việc COPY đảm bảo dữ liệu luôn khả dụng trong tx_buffers cho tới khi DB gốc đã ghi xong.
                    buffer_logs = it->second.xapian_doc_logs;
                }
            }
            
            if (!buffer_logs.empty()) {
                // Replay logs into the actual Xapian DB for this manager (replay_log will lock changes_mutex internally)
                bool ok = manager_ptr->replay_log(buffer_logs);
                if (!ok) {
                    std::cerr << "[FATAL] commitBufferForTxHash: replay_log FAILED for txHash: " << txHashStr
                              << " - aborting to prevent state divergence/fork!" << std::endl;
                    std::abort();
                }
                
                // Append them to comprehensive_log under changes_mutex so they can be extracted safely
                {
                    std::unique_lock<XapianManager::ChangesMutex> ch_lock(manager_ptr->changes_mutex);
                    manager_ptr->comprehensive_log.xapian_doc_logs.insert(
                        manager_ptr->comprehensive_log.xapian_doc_logs.end(),
                        std::make_move_iterator(buffer_logs.begin()),
                        std::make_move_iterator(buffer_logs.end())
                    );
                }
            }
            // Always clean up tx_buffers and tx_counters for this txHash
            {
                std::lock_guard<std::mutex> buffer_lock(manager_ptr->tx_buffers_mutex);
                manager_ptr->tx_buffers.erase(txHashStr);
                manager_ptr->tx_counters.erase(txHashStr);
                manager_ptr->clear_writer_predecessors_for_tx(txHashStr);
            }
        }
    }
}

void XapianRegistry::commitBuffersForTxHashBatch(const unsigned char* b_tx_hashes, int count) {
    if (!b_tx_hashes || count <= 0) return;
    std::vector<std::string> txHashStrs;
    txHashStrs.reserve(static_cast<size_t>(count));
    for (int i = 0; i < count; ++i) {
        uint256_t txHash = mvm::from_big_endian(reinterpret_cast<const uint8_t*>(b_tx_hashes + i * 32), 32u);
        txHashStrs.push_back(mvm::to_hex_string_fixed(txHash, 64));
    }

    std::shared_lock<std::shared_mutex> inst_lock(XapianManager::instances_mutex);
    for (auto& pair : XapianManager::instances) {
        auto manager_ptr = pair.second;
        if (!manager_ptr) continue;

        std::vector<XapianLog::LogEntry> coalesced_logs;
        {
            std::lock_guard<std::mutex> buffer_lock(manager_ptr->tx_buffers_mutex);
            if (manager_ptr->tx_buffers.empty() && manager_ptr->tx_counters.empty() &&
                manager_ptr->tx_to_predecessor_keys.empty()) {
                continue;
            }
            size_t total_entries = 0;
            for (const auto& txHashStr : txHashStrs) {
                auto it = manager_ptr->tx_buffers.find(txHashStr);
                if (it != manager_ptr->tx_buffers.end()) {
                    total_entries += it->second.xapian_doc_logs.size();
                }
            }
            if (total_entries > 0) {
                coalesced_logs.reserve(total_entries);
                for (const auto& txHashStr : txHashStrs) {
                    auto it = manager_ptr->tx_buffers.find(txHashStr);
                    if (it != manager_ptr->tx_buffers.end()) {
                        // Copy (do not move) until replay_log completes to preserve read consistency
                        const auto& src = it->second.xapian_doc_logs;
                        coalesced_logs.insert(coalesced_logs.end(), src.begin(), src.end());
                    }
                }
            }
        }

        if (!coalesced_logs.empty()) {
            bool ok = manager_ptr->replay_log(coalesced_logs);
            if (!ok) {
                std::cerr << "[FATAL] commitBuffersForTxHashBatch: replay_log FAILED for db="
                          << manager_ptr->getDbName()
                          << " - aborting to prevent state divergence/fork!" << std::endl;
                std::abort();
            }

            {
                std::unique_lock<XapianManager::ChangesMutex> ch_lock(manager_ptr->changes_mutex);
                manager_ptr->comprehensive_log.xapian_doc_logs.insert(
                    manager_ptr->comprehensive_log.xapian_doc_logs.end(),
                    std::make_move_iterator(coalesced_logs.begin()),
                    std::make_move_iterator(coalesced_logs.end())
                );
            }
        }

        {
            std::lock_guard<std::mutex> buffer_lock(manager_ptr->tx_buffers_mutex);
            for (const auto& txHashStr : txHashStrs) {
                manager_ptr->tx_buffers.erase(txHashStr);
                manager_ptr->tx_counters.erase(txHashStr);
                manager_ptr->clear_writer_predecessors_for_tx(txHashStr);
            }
        }
    }
}

XapianRegistry::XapianSnapshot XapianRegistry::takeSnapshotForTxHash(const uint256_t* txHash) {
    if (!txHash) return {};
    std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
    XapianSnapshot snap;
    
    std::shared_lock<std::shared_mutex> inst_lock(XapianManager::instances_mutex);
    for (auto& pair : XapianManager::instances) {
        if (auto mgr = pair.second) {
            std::lock_guard<std::mutex> buf_lock(mgr->tx_buffers_mutex);
            size_t log_count = 0;
            auto it = mgr->tx_buffers.find(txHashStr);
            if (it != mgr->tx_buffers.end()) {
                log_count = it->second.xapian_doc_logs.size();
            }
            snap.push_back({mgr, log_count});
        }
    }
    return snap;
}

void XapianRegistry::revertToSnapshotForTxHash(const uint256_t* txHash, const XapianSnapshot& snap) {
    if (!txHash) return;
    std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
    
    std::unordered_set<XapianManager*> snap_managers;
    for (const auto& item : snap) {
        if (auto mgr = item.manager.lock()) {
            snap_managers.insert(mgr.get());
            std::lock_guard<std::mutex> buf_lock(mgr->tx_buffers_mutex);
            auto it = mgr->tx_buffers.find(txHashStr);
            if (it != mgr->tx_buffers.end()) {
                if (it->second.xapian_doc_logs.size() > item.log_count) {
                    it->second.xapian_doc_logs.resize(item.log_count);
                }
            }
        }
    }
    
    std::shared_lock<std::shared_mutex> inst_lock(XapianManager::instances_mutex);
    for (auto& pair : XapianManager::instances) {
        if (auto mgr = pair.second) {
            if (snap_managers.find(mgr.get()) == snap_managers.end()) {
                std::lock_guard<std::mutex> buf_lock(mgr->tx_buffers_mutex);
                auto it = mgr->tx_buffers.find(txHashStr);
                if (it != mgr->tx_buffers.end()) {
                    it->second.xapian_doc_logs.clear();
                }
            }
        }
    }
}

// --- Exported C FFI entry points for Xapian lifecycle, replay, and crash recovery ---

extern "C" int ReplayFullDbLogs(LogReplayEntryC *entries, int num_entries) {
    return ReplayFullDbLogsAtBlock(entries, num_entries, 0);
}

extern "C" int ReplayFullDbLogsAtBlock(LogReplayEntryC *entries, int num_entries, unsigned long long block_number) {
    if (entries == nullptr || num_entries <= 0) {
        return 0;
    }

    std::unordered_set<std::string> rolled_back_in_batch;
    std::vector<std::shared_ptr<XapianManager>> managers_to_commit;
    std::unordered_set<XapianManager*> seen_managers;

    for (int i = 0; i < num_entries; ++i) {
        LogReplayEntryC &current_entry = entries[i];

        if (current_entry.address_ptr == nullptr ||
            current_entry.address_len != 20) {
            return 0;
        }
        if (current_entry.log_data_ptr == nullptr ||
            current_entry.log_data_len <= 0) {
            return 0;
        }

        mvm::Address address = mvm::from_big_endian(current_entry.address_ptr,
                                                    current_entry.address_len);
        std::string addr_hex = mvm::address_to_hex_string(address);

        std::vector<uint8_t> serialized_log_data(current_entry.log_data_ptr,
                                                 current_entry.log_data_ptr +
                                                     current_entry.log_data_len);

        std::optional<XapianLog::ComprehensiveLog> deserialized_log_opt =
            XapianLog::ComprehensiveLog::deserialize(serialized_log_data);

        if (!deserialized_log_opt) {
            return 0;
        }
        XapianLog::ComprehensiveLog &comp_log = *deserialized_log_opt;

        if (comp_log.db_name.empty()) {
            std::cerr << "[C++ Replay Error] DB db_name in log is empty for address "
                      << addr_hex << ". Cannot determine target manager."
                      << std::endl;
            return 0;
        }

        std::shared_ptr<XapianManager> target_manager = nullptr;
        std::filesystem::path replay_db_path = mvm::createFullPath(address, comp_log.db_name);

        if (!mvm::IsXapianBasePathEmpty() && !std::filesystem::exists(replay_db_path)) {
            std::error_code ec;
            std::filesystem::create_directories(replay_db_path, ec);
            if (ec) {
                std::cerr << "[C++ Replay FATAL] Cannot create directory for db_name='"
                          << comp_log.db_name << "' path=" << replay_db_path.string()
                          << " error=" << ec.message() << std::endl;
                return 0;
            }
        }

        try {
            target_manager =
                XapianManager::getInstance(comp_log.db_name, address, false);
        } catch (const Xapian::DatabaseLockError &e) {
            std::cerr << "[C++ Replay FATAL] DatabaseLockError for db_name '"
                      << comp_log.db_name << "' addr=" << addr_hex
                      << " path=" << replay_db_path.string()
                      << ": " << e.get_msg() << " — Aborting batch immediately (Zero-Fork Invariant)." << std::endl;
            return 0;
        } catch (const Xapian::Error &e) {
            std::cerr << "[C++ Replay FATAL] Xapian::Error for db_name '"
                      << comp_log.db_name << "' addr=" << addr_hex
                      << " path=" << replay_db_path.string()
                      << " type=" << e.get_type()
                      << " msg=" << e.get_msg()
                      << " context=" << e.get_context() << " — Aborting batch." << std::endl;
            return 0;
        } catch (const std::exception &e) {
            std::cerr << "[C++ Replay FATAL] std::exception for db_name '"
                      << comp_log.db_name << "' addr=" << addr_hex
                      << " path=" << replay_db_path.string()
                      << ": " << e.what() << " — Aborting batch." << std::endl;
            return 0;
        } catch (...) {
            std::cerr << "[C++ Replay FATAL] Unknown exception for db_name '"
                      << comp_log.db_name << "' addr=" << addr_hex
                      << " path=" << replay_db_path.string() << " — Aborting batch." << std::endl;
            return 0;
        }

        if (!target_manager) {
            std::cerr << "[C++ Replay FATAL] Failed to get/create XapianManager for db_name '"
                      << comp_log.db_name << "' and address " << addr_hex
                      << ". Aborting replay for entire batch." << std::endl;
            return 0;
        }

        if (block_number > 0) {
            std::string mgr_key = replay_db_path.string();
            if (rolled_back_in_batch.find(mgr_key) == rolled_back_in_batch.end()) {
                rolled_back_in_batch.insert(mgr_key);
                uint64_t committed_head = target_manager->getCommittedHeadBlock();
                if (committed_head >= static_cast<uint64_t>(block_number)) {
                    uint64_t target_rb = static_cast<uint64_t>(block_number - 1);
                    if (!target_manager->rollbackAboveBlock(target_rb)) {
                        std::cerr << "[C++ Replay FATAL] rollbackAboveBlock(" << target_rb
                                  << ") FAILED for addr=" << addr_hex
                                  << " db_name='" << comp_log.db_name << "' — Aborting batch." << std::endl;
                        return 0;
                    }
                }
            }
        }

        if (!comp_log.xapian_doc_logs.empty()) {
            if (!target_manager->replay_log(comp_log.xapian_doc_logs)) {
                std::cerr << "[C++ Replay FATAL] replay_log FAILED for entry " << i
                          << " addr=" << addr_hex
                          << " db_name='" << comp_log.db_name << "'"
                          << " ops=" << comp_log.xapian_doc_logs.size() << " — Aborting batch." << std::endl;
                return 0;
            }
        }

        if (seen_managers.insert(target_manager.get()).second) {
            managers_to_commit.push_back(target_manager);
        }
    }

    for (const auto &mgr : managers_to_commit) {
        try {
            if (!mgr->saveAllAndCommit(static_cast<uint64_t>(block_number))) {
                std::cerr << "[C++ Replay FATAL] saveAllAndCommit returned false for db_name='"
                          << mgr->getDbName() << "' — Aborting batch." << std::endl;
                return 0;
            }
        } catch (const Xapian::Error &e) {
            std::cerr << "[C++ Replay FATAL] Xapian::Error during saveAllAndCommit for db_name='"
                      << mgr->getDbName() << "' type=" << e.get_type()
                      << " msg=" << e.get_msg() << " — Aborting batch." << std::endl;
            return 0;
        } catch (const std::exception &e) {
            std::cerr << "[C++ Replay FATAL] Exception during saveAllAndCommit for db_name='"
                      << mgr->getDbName() << "': " << e.what() << " — Aborting batch." << std::endl;
            return 0;
        } catch (...) {
            std::cerr << "[C++ Replay FATAL] Unknown exception during saveAllAndCommit for db_name='"
                      << mgr->getDbName() << "' — Aborting batch." << std::endl;
            return 0;
        }
    }
    return 1;
}

extern "C" void MVM_commitAllXapian() {
    XapianManager::commitAllInstances(0);
}

extern "C" void MVM_commitAllXapianAtBlock(unsigned long long block_number) {
    XapianManager::commitAllInstances(static_cast<uint64_t>(block_number));
}

extern "C" int MVM_rollbackXapianAboveBlock(unsigned long long target_block) {
    return XapianManager::rollbackAllAboveBlock(static_cast<uint64_t>(target_block));
}

extern "C" void MVM_clearAllXapianTxBuffers() {
    registry.clearAllBuffers();
}

extern "C" void MVM_destroyAllXapianInstances() {
    XapianManager::destroyAllInstances();
}

extern "C" void clear_xapian_tx_buffer(unsigned char *b_tx_hash) {
    uint256_t txHash = mvm::from_big_endian((uint8_t *)b_tx_hash, 32u);
    registry.clearBufferForTxHash(&txHash);
}

extern "C" void commit_xapian_tx_buffer(unsigned char *b_tx_hash) {
    if (b_tx_hash) {
        uint256_t txHash = mvm::from_big_endian((uint8_t *)b_tx_hash, 32u);
        registry.commitBufferForTxHash(&txHash);
    }
}

extern "C" void clear_xapian_tx_buffer_batch(unsigned char *b_tx_hashes, int count) {
    registry.clearBuffersForTxHashBatch(b_tx_hashes, count);
}

extern "C" void commit_xapian_tx_buffer_batch(unsigned char *b_tx_hashes, int count) {
    registry.commitBuffersForTxHashBatch(b_tx_hashes, count);
}

extern "C" struct ExportedXapianLogArray MVM_exportAllXapianLogs() {
    ExportedXapianLogArray result;
    result.data = nullptr;
    result.count = 0;

    std::vector<ExportedXapianLog> logs_vec;
    std::shared_lock<std::shared_mutex> inst_lock(XapianManager::instances_mutex);

    for (auto& pair : XapianManager::instances) {
        auto manager = pair.second;
        if (manager) {
            std::lock_guard<XapianManager::ChangesMutex> lock(manager->changes_mutex);
            if (!manager->comprehensive_log.xapian_doc_logs.empty()) {
                try {
                    std::vector<uint8_t> serialized = manager->comprehensive_log.serialize();
                    if (!serialized.empty()) {
                        ExportedXapianLog log_entry;
                        uint8_t temp_addr[32];
                        mvm::to_big_endian(manager->address, temp_addr);
                        memcpy(log_entry.address, temp_addr + 12, 20);
                        log_entry.logs_length = serialized.size();
                        log_entry.logs = new char[log_entry.logs_length];
                        memcpy(log_entry.logs, serialized.data(), log_entry.logs_length);
                        logs_vec.push_back(log_entry);
                    }
                } catch (const std::exception& e) {
                    std::cerr << "[ERROR] MVM_exportAllXapianLogs: Failed to serialize logs for " << manager->getDbName() << ": " << e.what() << std::endl;
                }
            }
        }
    }

    if (!logs_vec.empty()) {
        result.count = logs_vec.size();
        result.data = new ExportedXapianLog[result.count];
        for (int i = 0; i < result.count; i++) {
            result.data[i] = logs_vec[i];
        }
    }

    return result;
}

extern "C" void MVM_freeExportedXapianLogs(struct ExportedXapianLogArray array) {
    if (array.data) {
        for (int i = 0; i < array.count; i++) {
            delete[] array.data[i].logs;
        }
        delete[] array.data;
    }
}
