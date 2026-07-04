#include <atomic>
#include "xapian/xapian_manager.h"
#include "my_extension/utils.h"
#include "xapian/xapian_log.h" // Giả định chứa định nghĩa XapianLog::LogEntry
#include "xapian/xapian_registry.h"

#include <chrono>
#include <filesystem> // Cần cho std::filesystem::path
#include <functional>
#include <iomanip>
#include <memory>     // Cần cho std::shared_ptr
#include <mutex>      // Cần cho std::lock_guard
#include <mvm/util.h> // Giả định chứa mvm::address_to_hex_string, mvm::uint256_to_double, mvm::keccak_256
#include <numeric>
#include <sstream>
#include <stdexcept> // Cần cho các exception như std::runtime_error, std::out_of_range
#include <thread>
#include <uuid/uuid.h> // Cần header của libuuid
#include <variant>     // Cần cho std::visit, std::monostate
#include <vector>

// Map lưu trữ các instance XapianManager, khóa là đường dẫn database
std::unordered_map<std::string, std::shared_ptr<XapianManager>> XapianManager::instances;
std::shared_mutex XapianManager::instances_mutex;

// Tạo một UUID ngẫu nhiên và trả về dưới dạng chuỗi ký tự thường
std::string XapianManager::generateUuidLogicalId() {
  uuid_t uuid_bin;
  uuid_generate_random(uuid_bin); // Tạo UUID nhị phân

  std::vector<char> uuid_str_buf(37); // Buffer cho chuỗi UUID (36 chars + null)
  uuid_unparse_lower(uuid_bin,
                     uuid_str_buf.data()); // Chuyển sang chuỗi chữ thường

  std::string uuid_str(uuid_str_buf.data());
  return uuid_str; // Chỉ trả về chuỗi UUID
}

// Tạo một bản sao (clone) của một Xapian::Document
Xapian::Document
XapianManager::clone_document(const Xapian::Document &source_doc) {
  Xapian::Document new_doc;
  // Sao chép dữ liệu thô
  new_doc.set_data(source_doc.get_data());

  // Sao chép tất cả các term và trọng số (wdf) của chúng
  for (auto term = source_doc.termlist_begin();
       term != source_doc.termlist_end(); ++term) {
    new_doc.add_term(*term, term.get_wdf());
  }

  // Sao chép tất cả các value trong các slot hợp lệ (0-254)
  constexpr int MAX_SLOT = 255;
  for (int slot = 0; slot < MAX_SLOT; ++slot) {
    std::string val = source_doc.get_value(slot);
    if (!val.empty()) {
      new_doc.add_value(slot, val);
    }
  }
  // Metadata khác (ngoài data, term, value) không được Xapian hỗ trợ chính thức
  return new_doc;
}
// Lấy hoặc tạo một instance XapianManager cho một database cụ thể
std::shared_ptr<XapianManager> XapianManager::getInstance(const std::string &db_name, const mvm::Address &addr, bool isReset)
{
    // Tạo đường dẫn đầy đủ đến database
    std::filesystem::path db_path = mvm::createFullPath(addr, db_name);
    std::string db_path_str = db_path.string(); // Sử dụng string cho key của map

    // Nếu yêu cầu reset, hủy instance hiện tại (nếu có)
    if (isReset)
    {
        destroyInstance(db_path_str); // Hàm này sẽ xóa instance khỏi map `instances`
    }

    {
        std::unique_lock<std::shared_mutex> read_lock(instances_mutex);
        auto it = instances.find(db_path_str);
        if (!isReset && it != instances.end())
        {
            if (it->second)
                it->second->touch();
            return it->second;
        }
    }

    std::unique_lock<std::shared_mutex> write_lock(instances_mutex);
    auto it = instances.find(db_path_str);
    if (!isReset && it != instances.end())
    {
        if (it->second)
            it->second->touch();
        return it->second;
    }

    try
    {
        auto new_instance = std::make_shared<XapianManager>(db_name, addr);
        if (new_instance)
            new_instance->touch();
        instances[db_path_str] = new_instance;
        return new_instance;
    }
    catch (const Xapian::Error &e)
    {
        throw;
    }
    catch (const std::exception &e)
    {
        throw;
    }
    catch (...)
    {
        throw std::runtime_error("Lỗi không xác định khi tạo instance XapianManager cho " + db_path_str);
    }
}

// Constructor của XapianManager
XapianManager::XapianManager(const std::string &db_name,
                             const mvm::Address &addr)
    : db(mvm::createFullPath(addr, db_name).string(),
         Xapian::DB_CREATE_OR_OPEN), // Mở hoặc tạo database
      address(addr),                 // Lưu địa chỉ liên kết
      last_access_time(
          std::chrono::steady_clock::now()), // Khởi tạo thời gian truy cập
      db_name(db_name)                       // Lưu tên database
{}

void XapianManager::acquireSearchSlot()
{
    std::unique_lock<std::mutex> lock(search_semaphore_mutex);
    search_semaphore_cv.wait(lock, [this]() {
        return active_searches < MAX_CONCURRENT_SEARCHES;
    });
    active_searches++;
}

void XapianManager::releaseSearchSlot()
{
    std::lock_guard<std::mutex> lock(search_semaphore_mutex);
    active_searches--;
    search_semaphore_cv.notify_one();
}


// Lấy tên của database
std::string XapianManager::getDbName() const { return this->db_name; }

// Cập nhật thời gian truy cập cuối cùng
void XapianManager::touch() {
  std::lock_guard<std::mutex> lock(access_mutex); // Đảm bảo an toàn luồng
  last_access_time = std::chrono::steady_clock::now();
}

// Kiểm tra xem instance có bị bỏ không (idle) trong một khoảng thời gian không
bool XapianManager::is_idle_for(std::chrono::minutes duration) {
  std::lock_guard<std::mutex> lock(access_mutex); // Đảm bảo an toàn luồng
  auto now = std::chrono::steady_clock::now();
  auto idle_duration =
      std::chrono::duration_cast<std::chrono::minutes>(now - last_access_time);
  return idle_duration >= duration;
}
// Dump tất cả tài liệu trong database với đầy đủ thông tin để debug
void XapianManager::dump_all_documents(uint256_t blockNumber) {
  touch();
  try {
    std::cerr << "\n========== [DEBUG DUMP] Database: " << db_name
              << " (At Block: " << mvm::uint256_to_double(blockNumber)
              << ") ==========" << std::endl;

    Xapian::doccount last_docid = db.get_lastdocid();
    std::cerr << "Total Documents (last_docid): " << last_docid << std::endl;

    for (Xapian::docid i = 1; i <= last_docid; ++i) {
      try {
        Xapian::Document doc = db.get_document(i);
        std::cerr << "\n--------------------------------------------------"
                  << std::endl;
        std::cerr << ">> DocID: " << i << std::endl;

        // Kiểm tra trạng thái "active" dựa trên blockNumber
        std::string slot253_str = doc.get_value(253);
        std::string slot254_str = doc.get_value(254);
        double created_at = !slot253_str.empty()
                                ? Xapian::sortable_unserialise(slot253_str)
                                : -1.0;
        double deleted_at = !slot254_str.empty()
                                ? Xapian::sortable_unserialise(slot254_str)
                                : -1.0;
        double current_bn = mvm::uint256_to_double(blockNumber);

        bool is_active = true;
        if (created_at > current_bn)
          is_active = false;
        if (deleted_at != -1.0 && deleted_at <= current_bn)
          is_active = false;

        std::cerr << "   [Status]: "
                  << (is_active ? "ACTIVE" : "INACTIVE/DELETED") << std::endl;
        if (!slot253_str.empty())
          std::cerr << "   [Created At Block]: " << created_at << std::endl;
        if (!slot254_str.empty())
          std::cerr << "   [Deleted At Block]: " << deleted_at << std::endl;

        // Dữ liệu thô
        std::cerr << "   [Data]: " << doc.get_data() << std::endl;

        // Các giá trị (Values)
        std::cerr << "   [Values]:" << std::endl;
        for (Xapian::ValueIterator vit = doc.values_begin();
             vit != doc.values_end(); ++vit) {
          Xapian::valueno slot = vit.get_valueno();
          std::string raw_val = *vit;
          std::cerr << "      Slot " << slot << ": " << raw_val;
          // Thử unserialize nếu là slot đặc biệt hoặc trông giống số
          try {
            double d_val = Xapian::sortable_unserialise(raw_val);
            std::cerr << " (unserialized: " << d_val << ")";
          } catch (...) {
          }
          std::cerr << std::endl;
        }

        // Từ khóa (Terms)
        std::cerr << "   [Terms]: ";
        bool first_term = true;
        for (Xapian::TermIterator tit = doc.termlist_begin();
             tit != doc.termlist_end(); ++tit) {
          if (!first_term)
            std::cerr << ", ";
          std::cerr << *tit << "(" << tit.get_wdf() << ")";
          first_term = false;
        }
        std::cerr << std::endl;
      } catch (const Xapian::DocNotFoundError &) {
        std::cerr << "\n--------------------------------------------------"
                  << std::endl;
        std::cerr << ">> DocID: " << i << " [PERMANENTLY DELETED/NOT FOUND]"
                  << std::endl;
      }
    }
    std::cerr << "============================================================="
                 "=========\n"
              << std::endl;
  } catch (const Xapian::Error &e) {
    std::cerr << "Lỗi Xapian khi dump database: " << e.get_msg() << std::endl;
  } catch (const std::exception &e) {
    std::cerr << "Lỗi khi dump database: " << e.what() << std::endl;
  }
}

static std::string getMvmIdKey(const unsigned char *mvmId) {
    return XapianRegistry::generateMvmIdKey(mvmId);
}


// Thêm một document mới vào database
std::string XapianManager::new_document(const std::string &data, uint256_t blockNumber, const unsigned char *mvmId, const uint256_t *txHash) {
    std::string virtualDocId;
    
    if (txHash != nullptr) {
        std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
        
        std::lock_guard<std::mutex> lock2(tx_buffers_mutex);
        int doc_index = tx_counters[txHashStr]++; // Sinh ID tuần tự và tất định trong 1 transaction
        
        // Tạo virtualDocId tất định dựa trên txHash và doc_index
        std::string input_for_hash = txHashStr + "_" + std::to_string(doc_index);
        virtualDocId = mvm::keccak256(input_for_hash);
        
        XapianLog::NewDocData logData;
        logData.docid = virtualDocId;
        logData.data = data;
        XapianLog::LogEntry entry;
        entry.op = XapianLog::Operation::NEW_DOC;
        entry.data = logData;
        
        tx_buffers[txHashStr].xapian_doc_logs.push_back(entry);
    } else {
        // Fallback cho trường hợp không có txHash (Off-chain / Test)
        static std::atomic<uint64_t> virtual_doc_id_counter{4300000000ULL};
        uint64_t v_id_num = virtual_doc_id_counter++;
        virtualDocId = mvm::to_hex_string_fixed(intx::uint256(v_id_num), 64);
    }
    return virtualDocId;
}


// Đánh dấu một document là đã bị xóa (soft delete) tại một block number cụ thể
bool XapianManager::delete_document(const std::string& virtualDocId, uint256_t blockNumber, const unsigned char *mvmId, const uint256_t *txHash) {
    if (txHash != nullptr) {
        std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
        XapianLog::DelDocData logData;
        logData.docid = virtualDocId;
        XapianLog::LogEntry entry;
        entry.op = XapianLog::Operation::DEL_DOC;
        entry.data = logData;
        std::lock_guard<std::mutex> lock2(tx_buffers_mutex);
        tx_buffers[txHashStr].xapian_doc_logs.push_back(entry);
    }
    return true;
}

// Thêm một value vào một slot của document, có xử lý versioning theo blockNumber
std::string XapianManager::add_value(const std::string& virtualDocId, Xapian::valueno slot, const std::string &value, bool isSerialise, uint256_t blockNumber, const unsigned char *mvmId, const uint256_t *txHash) {
    if (txHash != nullptr) {
        std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
        XapianLog::AddValueData logData;
        logData.docid = virtualDocId;
        logData.slot = slot;
        logData.value = value;
        XapianLog::LogEntry entry;
        entry.op = XapianLog::Operation::ADD_VALUE;
        entry.data = logData;
        std::lock_guard<std::mutex> lock2(tx_buffers_mutex);
        tx_buffers[txHashStr].xapian_doc_logs.push_back(entry);
    }
    return virtualDocId;
}


// Thêm một term vào document, có xử lý versioning theo blockNumber
std::string XapianManager::add_term(const std::string& virtualDocId, const std::string &term, uint256_t blockNumber, const unsigned char *mvmId, const uint256_t *txHash) {
    if (txHash != nullptr) {
        std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
        XapianLog::AddTermData logData;
        logData.docid = virtualDocId;
        logData.term = term;
        XapianLog::LogEntry entry;
        entry.op = XapianLog::Operation::ADD_TERM;
        entry.data = logData;
        std::lock_guard<std::mutex> lock2(tx_buffers_mutex);
        tx_buffers[txHashStr].xapian_doc_logs.push_back(entry);
    }
    return virtualDocId;
}

// Index một đoạn text vào document (thêm các term được tạo ra từ text)
std::string XapianManager::index_text(const std::string& virtualDocId, const std::string &text_to_index, Xapian::termcount wdf_inc, const std::string prefix, uint256_t blockNumber, const unsigned char *mvmId, const uint256_t *txHash) {
    if (txHash != nullptr) {
        std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
        XapianLog::IndexTextData logData;
        logData.docid = virtualDocId;
        logData.text = text_to_index;
        logData.wdf_inc = wdf_inc;
        logData.prefix = prefix;
        XapianLog::LogEntry entry;
        entry.op = XapianLog::Operation::INDEX_TEXT;
        entry.data = logData;
        std::lock_guard<std::mutex> lock2(tx_buffers_mutex);
        tx_buffers[txHashStr].xapian_doc_logs.push_back(entry);
    }
    return virtualDocId;
}

// Đặt (ghi đè) dữ liệu thô cho một document, có xử lý versioning
std::string XapianManager::set_data(const std::string& virtualDocId, const std::string &new_data, uint256_t blockNumber, const unsigned char *mvmId, const uint256_t *txHash) {
    if (txHash != nullptr) {
        std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
        XapianLog::SetDataData logData;
        logData.docid = virtualDocId;
        logData.data = new_data;
        XapianLog::LogEntry entry;
        entry.op = XapianLog::Operation::SET_DATA;
        entry.data = logData;
        std::lock_guard<std::mutex> lock2(tx_buffers_mutex);
        tx_buffers[txHashStr].xapian_doc_logs.push_back(entry);
    }
    return virtualDocId;
}
Xapian::Document XapianManager::get_overlayed_document(const std::string& virtualDocId, const uint256_t *txHash, const uint256_t *writerHash) {
    Xapian::Document doc;
    bool found = false;
    
    try {
        Xapian::docid did = resolveVirtualDocId(virtualDocId);
        if (did != 0) {
            doc = db.get_document(did);
            found = true;
        }
    } catch (const Xapian::DocNotFoundError&) {
        found = false;
    } catch (...) {
        found = false;
    }
    
    std::lock_guard<std::mutex> buffer_lock(tx_buffers_mutex);

    // 1. Try to find in the writer transaction's buffer (provided by Block-STM dependency)
    if (writerHash != nullptr) {
        std::string writerHashStr = mvm::to_hex_string_fixed(*writerHash, 64);
        auto it = tx_buffers.find(writerHashStr);
        if (it != tx_buffers.end()) {
            const auto& entries = it->second.xapian_doc_logs;
            for (size_t i = 0; i < entries.size(); ++i) {
                const auto& entry = entries[i];
                if (entry.op == XapianLog::Operation::NEW_DOC) {
                    auto data = std::get<XapianLog::NewDocData>(entry.data);
                    if (data.docid == virtualDocId) {
                        found = true;
                        doc = Xapian::Document();
                        doc.set_data(data.data);
                    }
                } else if (entry.op == XapianLog::Operation::SET_DATA) {
                    auto data = std::get<XapianLog::SetDataData>(entry.data);
                    if (data.docid == virtualDocId) {
                        found = true;
                        doc.set_data(data.data);
                    }
                } else if (entry.op == XapianLog::Operation::ADD_VALUE) {
                    auto data = std::get<XapianLog::AddValueData>(entry.data);
                    if (data.docid == virtualDocId) {
                        found = true;
                        doc.add_value(data.slot, data.value);
                    }
                }
            }
        }
    }

    // 2. Try to find in current transaction's buffer (can overwrite writer's changes)
    if (txHash != nullptr) {
        std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
        auto it = tx_buffers.find(txHashStr);
        if (it != tx_buffers.end()) {
            const auto& entries = it->second.xapian_doc_logs;
            for (size_t i = 0; i < entries.size(); ++i) {
                const auto& entry = entries[i];
                if (entry.op == XapianLog::Operation::NEW_DOC) {
                    auto data = std::get<XapianLog::NewDocData>(entry.data);
                    if (data.docid == virtualDocId) {
                        found = true;
                        doc = Xapian::Document();
                        doc.set_data(data.data);
                    }
                } else if (entry.op == XapianLog::Operation::DEL_DOC) {
                    auto data = std::get<XapianLog::DelDocData>(entry.data);
                    if (data.docid == virtualDocId) {
                        found = false;
                        throw Xapian::DocNotFoundError("Document deleted in buffer");
                    }
                } else if (entry.op == XapianLog::Operation::ADD_VALUE) {
                    auto data = std::get<XapianLog::AddValueData>(entry.data);
                    if (data.docid == virtualDocId) {
                        doc.add_value(data.slot, data.value);
                    }
                } else if (entry.op == XapianLog::Operation::ADD_TERM) {
                    auto data = std::get<XapianLog::AddTermData>(entry.data);
                    if (data.docid == virtualDocId) {
                        doc.add_term(data.term);
                    }
                } else if (entry.op == XapianLog::Operation::SET_DATA) {
                    auto data = std::get<XapianLog::SetDataData>(entry.data);
                    if (data.docid == virtualDocId) {
                        doc.set_data(data.data);
                    }
                }
            }
        }
    }
    
    if (!found) {
        throw Xapian::DocNotFoundError("Document not found");
    }
    
    return doc;
}

std::string XapianManager::get_data(const std::string& virtualDocId, uint256_t blockNumber, const uint256_t *txHash, const uint256_t *writerHash) {
  std::shared_lock<std::shared_mutex> read_lock(changes_mutex);
  try {
    Xapian::Document doc = get_overlayed_document(virtualDocId, txHash, writerHash);
    return doc.get_data();
  } catch (...) {}
  return "";
}

// Lấy giá trị từ một slot của document tại một block number, có tùy chọn
// unserialize
std::string XapianManager::get_value(const std::string& virtualDocId, Xapian::valueno slot, bool isSerialise, uint256_t blockNumber, const uint256_t *txHash, const uint256_t *writerHash) {
    std::shared_lock<std::shared_mutex> read_lock(changes_mutex);
    try {
        Xapian::Document doc = get_overlayed_document(virtualDocId, txHash, writerHash);
        return doc.get_value(slot);
    } catch (...) {}
    return "";
}

// Lấy thông tin (data, value slot 1, value slot 2) của document tại một block number
DocumentInfo XapianManager::get_document(const std::string& virtualDocId, uint256_t blockNumber, const uint256_t *txHash, const uint256_t *writerHash) {
  std::shared_lock<std::shared_mutex> read_lock(changes_mutex);
  DocumentInfo info;
  try {
    Xapian::Document doc = get_overlayed_document(virtualDocId, txHash, writerHash);
    info.data = doc.get_data();
  } catch (...) {}
  return info;
}

// Lấy danh sách các term của document tại một block number
std::vector<std::string> XapianManager::get_terms(const std::string& virtualDocId,
                                                  uint256_t blockNumber, const uint256_t *txHash, const uint256_t *writerHash) {
  std::shared_lock<std::shared_mutex> read_lock(changes_mutex);
  std::vector<std::string> terms;
  try {
    Xapian::Document doc = get_overlayed_document(virtualDocId, txHash, writerHash);
    for (auto term_it = doc.termlist_begin(); term_it != doc.termlist_end(); ++term_it) {
      terms.push_back(*term_it);
    }
  } catch (...) {}
  return terms;
}

// Commit các thay đổi đã được staged vào database Xapian
bool XapianManager::commit_changes() {
  touch(); // Cập nhật thời gian truy cập
  std::lock_guard<std::shared_mutex> lock(
      changes_mutex); // Khóa để kiểm tra và xóa staged_changes_log
  if (comprehensive_log.xapian_doc_logs.empty()) {
    return true; // Không có gì để commit
  }
  try {
    db.commit(); // Thực hiện commit Xapian
    comprehensive_log.xapian_doc_logs
        .clear(); // Xóa các log đã staged sau khi commit thành công
    return true;
  } catch (const Xapian::Error &) {
    return false; // Commit thất bại
  } catch (const std::exception &) {
    return false; // Commit thất bại
  }
}

void XapianManager::commitAllInstances() {
  std::unique_lock<std::shared_mutex> lock(instances_mutex);
  for (auto &pair : instances) {
    auto manager = pair.second;
    if (manager) {
      std::lock_guard<std::shared_mutex> mgr_lock(manager->changes_mutex);
      if (!manager->has_started) {
        try {
          manager->db.commit();
          manager->comprehensive_log.xapian_doc_logs.clear();
        } catch (...) {
          // Ignore errors during background flush
        }
      }
    }
  }
}

// Tính toán hash của các thay đổi đã được staged
std::array<uint8_t, 32u> XapianManager::getChangeHash() {
  std::lock_guard<std::shared_mutex> lock(
      changes_mutex); // Khóa để truy cập staged_changes_log
  if (comprehensive_log.xapian_doc_logs.empty()) {
    return {0}; // Trả về hash 0 nếu không có thay đổi
  }

  std::vector<uint8_t> combined_bytes;
  // Ước tính kích thước để tối ưu cấp phát bộ nhớ
  size_t estimated_size = 0;
  for (const auto &entry : comprehensive_log.xapian_doc_logs) {
    estimated_size += 64;
  }
  combined_bytes.reserve(estimated_size);

  // Nối các byte đã serialize của từng log entry
  for (const auto &entry : comprehensive_log.xapian_doc_logs) {
    try {
      std::vector<uint8_t> entry_bytes =
          entry.serialize(); // Serialize log entry
      combined_bytes.insert(combined_bytes.end(), entry_bytes.begin(),
                            entry_bytes.end());
    } catch (const std::exception &) {
      // Lỗi khi serialize một log entry
      return {0}; // Trả về hash 0 để báo lỗi
    }
  }

  if (combined_bytes.empty()) {
    return {0}; // Nếu không có byte nào được nối (vd: lỗi serialize tất cả)
  }
  // Tính hash Keccak-256 của tất cả các byte đã nối
  return mvm::keccak_256(combined_bytes);
}

// Lấy danh sách các log entry đã được staged
std::vector<XapianLog::LogEntry> XapianManager::getChangeLogs() {
  std::lock_guard<std::shared_mutex> lock(
      changes_mutex); // Khóa để truy cập staged_changes_log
  return comprehensive_log.xapian_doc_logs; // Trả về bản sao của vector log
}

// Tính hash của các thay đổi liên quan đến tag (logic cụ thể không có trong
// snippet)
std::array<uint8_t, 32u> XapianManager::getCombinedTagsChangeHash() {
  // Logic tính hash cho tag sẽ ở đây nếu tag được quản lý riêng
  // Hiện tại trả về hash 0 theo snippet gốc
  return {0};
}

// Tính hash tổng hợp đại diện cho trạng thái thay đổi của manager
std::array<uint8_t, 32u> XapianManager::getComprehensiveStateHash() {
  std::vector<uint8_t> concatenated_data;

  // Lấy hash của log thay đổi document Xapian
  std::array<uint8_t, 32u> manager_log_hash = this->getChangeHash();
  concatenated_data.insert(concatenated_data.end(), manager_log_hash.begin(),
                           manager_log_hash.end());

  // Lấy hash của thay đổi tag (nếu có logic riêng)
  std::array<uint8_t, 32u> tags_hash = this->getCombinedTagsChangeHash();
  concatenated_data.insert(concatenated_data.end(), tags_hash.begin(),
                           tags_hash.end());

  // Nếu không có dữ liệu nào (log rỗng, tag không đổi), trả về hash 0
  if (concatenated_data.empty()) {
    return {0};
  }
  // Tính hash cuối cùng của tất cả dữ liệu đã nối
  return mvm::keccak_256(concatenated_data);
}

// Lưu và commit tất cả thay đổi (trong context này, chỉ commit Xapian)
bool XapianManager::saveAllAndCommit() {
  return this->commit_changes(); // Gọi hàm commit Xapian
}

// Namespace ẩn danh cho luồng dọn dẹp và các chức năng liên quan
namespace {
std::atomic<bool> cleaner_running = true; // Cờ điều khiển luồng dọn dẹp
// Luồng chạy nền để dọn dẹp các instance XapianManager không hoạt động
std::thread cleaner_thread([] {
  while (cleaner_running.load()) // Chạy tant khi cờ là true
  {
    std::this_thread::sleep_for(std::chrono::minutes(1)); // Ngủ 1 phút
    if (!cleaner_running.load())
      break; // Kiểm tra lại cờ sau khi ngủ

    std::vector<std::string>
        keys_to_erase; // Danh sách key của instance cần xóa
    // Giai đoạn 1: Xác định các instance ứng viên để xóa (không giữ accessor
    // lâu)
    {
                std::unique_lock<std::shared_mutex> read_lock(XapianManager::instances_mutex);
                for (auto it = XapianManager::instances.begin(); it != XapianManager::instances.end(); ++it)
                {
                    // Kiểm tra con trỏ hợp lệ và trạng thái idle
                    if (it->second && it->second->is_idle_for(std::chrono::minutes(5))) // Ngưỡng idle là 5 phút
                    {
                        // Kiểm tra use_count để xem có tham chiếu nào khác ngoài map không
                        std::shared_ptr<XapianManager> temp_ptr = it->second;
                        // <= 2 nghĩa là chỉ có map và temp_ptr đang giữ tham chiếu
                        if (temp_ptr.use_count() <= 2)
                        {
                            keys_to_erase.push_back(it->first); // Thêm key vào danh sách xóa
                        }
                    }
                }
            }

    // Giai đoạn 2: Thực hiện xóa các instance đã xác định
    for (const std::string &key : keys_to_erase) {
      // Gọi hàm destroyInstance để xử lý việc đóng DB, dọn dẹp và xóa khỏi map
      XapianManager::destroyInstance(key);
    }
  }
});

// Hàm dừng luồng dọn dẹp (ví dụ khi chương trình kết thúc)
void stopCleanerThread() {
  cleaner_running.store(false); // Đặt cờ dừng
  if (cleaner_thread.joinable()) {
    cleaner_thread.join(); // Chờ luồng kết thúc
  }
}

// Đối tượng RAII để tự động gọi stopCleanerThread khi kết thúc scope toàn cục
struct CleanerStopper {
  ~CleanerStopper() { stopCleanerThread(); }
} stopper_instance;
} // namespace

// Áp dụng lại một danh sách các log entry vào database hiện tại
bool XapianManager::replay_log(const std::vector<XapianLog::LogEntry> &log_to_replay) {
    std::unique_lock<std::shared_mutex> db_lock(changes_mutex);
    for (const auto& entry : log_to_replay) {
        try {
            if (entry.op == XapianLog::Operation::NEW_DOC) {
                Xapian::Document doc;
                doc.set_data(std::get<XapianLog::NewDocData>(entry.data).data);
                std::string v_docid = std::get<XapianLog::NewDocData>(entry.data).docid;
                std::string clean_id = v_docid;
                if (clean_id.substr(0, 2) == "0x") clean_id = clean_id.substr(2);
                doc.add_term("Q" + clean_id);
                db.add_document(doc);
            } else if (entry.op == XapianLog::Operation::DEL_DOC) {
                Xapian::docid did = resolveVirtualDocId(std::get<XapianLog::DelDocData>(entry.data).docid);
                if (did > 0) db.delete_document(did);
            } else if (entry.op == XapianLog::Operation::ADD_VALUE) {
                auto data = std::get<XapianLog::AddValueData>(entry.data);
                Xapian::docid did = resolveVirtualDocId(data.docid);
                if (did > 0) {
                    Xapian::Document doc = db.get_document(did);
                    doc.add_value(data.slot, data.value);
                    db.replace_document(did, doc);
                }
            } else if (entry.op == XapianLog::Operation::ADD_TERM) {
                auto data = std::get<XapianLog::AddTermData>(entry.data);
                Xapian::docid did = resolveVirtualDocId(data.docid);
                if (did > 0) {
                    Xapian::Document doc = db.get_document(did);
                    doc.add_term(data.term);
                    db.replace_document(did, doc);
                }
            } else if (entry.op == XapianLog::Operation::SET_DATA) {
                auto data = std::get<XapianLog::SetDataData>(entry.data);
                Xapian::docid did = resolveVirtualDocId(data.docid);
                if (did > 0) {
                    Xapian::Document doc = db.get_document(did);
                    doc.set_data(data.data);
                    db.replace_document(did, doc);
                }
            } else if (entry.op == XapianLog::Operation::INDEX_TEXT) {
                auto data = std::get<XapianLog::IndexTextData>(entry.data);
                Xapian::docid did = resolveVirtualDocId(data.docid);
                if (did > 0) {
                    Xapian::Document doc = db.get_document(did);
                    Xapian::TermGenerator termgenerator;
                    termgenerator.set_stemmer(Xapian::Stem("english"));
                    termgenerator.set_stemming_strategy(Xapian::TermGenerator::STEM_SOME);
                    termgenerator.set_document(doc);
                    termgenerator.index_text(data.text, data.wdf_inc, data.prefix);
                    db.replace_document(did, doc);
                }
            }
        } catch (...) { return false; }
    }
    return true;
}

// Khôi phục trạng thái về lần commit cuối cùng bằng cách xóa log staged và mở
// lại DB
bool XapianManager::revertUncommittedChanges() {
  std::unique_lock<std::shared_mutex> lock(changes_mutex); // Khóa để thao tác an toàn
  
  // FORK-SAFETY: Return immediately if there are no uncommitted changes
  if (comprehensive_log.xapian_doc_logs.empty()) {
      return true;
  }
  
  try {
    // 1. Xóa các thay đổi đang chờ trong log
    comprehensive_log.xapian_doc_logs.clear();

    // 2. Đóng và mở lại database để hủy các thay đổi chưa commit trong bộ nhớ
    // Xapian
    db.close();                      // Đóng kết nối hiện tại
    db = Xapian::WritableDatabase(); // Gán bằng đối tượng rỗng để giải phóng
                                     // tài nguyên cũ
    // Mở lại database từ đường dẫn đã lưu
    db = Xapian::WritableDatabase(
        mvm::createFullPath(address, db_name).string(), Xapian::DB_OPEN);
    return true; // Revert thành công
  } catch (const Xapian::Error &) {
    return false; /* Lỗi Xapian khi đóng/mở lại DB */
  } catch (const std::exception &) {
    return false; /* Lỗi standard */
  } catch (...) {
    return false; /* Lỗi không xác định */
  }
}

// Hủy một instance XapianManager và xóa nó khỏi map quản lý
bool XapianManager::destroyInstance(const std::string &db_path_str)
{
    std::shared_ptr<XapianManager> instance_ptr;

    {
        std::unique_lock<std::shared_mutex> write_lock(instances_mutex);
        auto it = instances.find(db_path_str);
        if (it != instances.end())
        {
            instance_ptr = it->second; // Giữ một tham chiếu tạm thời
            instances.erase(it);
        }
    }

    if (instance_ptr)
    {
        try
        {
            // Dọn dẹp tài nguyên nội bộ trước khi xóa khỏi map
            if (instance_ptr)
            {
                // Bước 1: Đóng database Xapian tường minh
                try
                {
                    instance_ptr->db.close();
                }
                catch (const Xapian::Error &)
                { /* Bỏ qua lỗi đóng DB */
                }
                catch (const std::exception &)
                { /* Bỏ qua lỗi đóng DB */
                }
                catch (...)
                { /* Bỏ qua lỗi đóng DB */
                }
            }

            return true; // Trả về true nếu xóa thành công
        }
        catch (const std::exception &)
        {
            return false;
        }
        catch (...)
        {
            return false;
        }
    }
    
    return false; // Không tìm thấy instance để hủy
}

// Lấy một bản ghi log tổng hợp chứa tất cả các thay đổi đã staged VÀ XÓA CHÚNG KHỎI MANAGER
XapianLog::ComprehensiveLog XapianManager::extractComprehensiveChangeLogs()
{
    std::lock_guard<std::shared_mutex> lock(changes_mutex);
    XapianLog::ComprehensiveLog log_copy = std::move(comprehensive_log);
    comprehensive_log.xapian_doc_logs.clear();
    return log_copy;
}

// Lấy một bản ghi log tổng hợp chứa tất cả các thay đổi đã staged
XapianLog::ComprehensiveLog XapianManager::removeLogsUntilNearestEndCommand()
{
    std::lock_guard<std::shared_mutex> lock(changes_mutex);
    comprehensive_log.removeLogsUntilNearestEndCommand();
    return comprehensive_log;
}