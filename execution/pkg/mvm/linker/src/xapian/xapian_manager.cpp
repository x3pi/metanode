#include <atomic>
#include <cstdio>
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
#include <map>
#include <set>

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
        std::shared_lock<std::shared_mutex> read_lock(instances_mutex);
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

// 2026-08-20 (plan §9.29): opens the real on-disk Glass-backed database
// (cgo mode, base path set from config.json) OR an in-memory-only
// Xapian::InMemory database (TA mode, no base path -- no filesystem at
// all, confirmed via the exact same crash class as the saveDebugInfo()
// fix, see memory mvm-ta-evm-interpreter-nullptr-crash). Both branches
// return a Xapian::WritableDatabase (InMemory::open()'s own declared
// return type), so this can sit directly in the mem-initializer-list
// below unchanged. See IsXapianBasePathEmpty()'s own doc comment
// (my_extension/utils.h) for why checking this at runtime, not a
// build-time flag, is correct here.
static Xapian::WritableDatabase openXapianDb(const mvm::Address &addr,
                                             const std::string &db_name) {
  if (mvm::IsXapianBasePathEmpty()) {
    // Equivalent to (deprecated) Xapian::InMemory::open() -- calling the
    // underlying constructor directly avoids -Wdeprecated-declarations.
    return Xapian::WritableDatabase(std::string(), Xapian::DB_BACKEND_INMEMORY);
  }
  return Xapian::WritableDatabase(mvm::createFullPath(addr, db_name).string(),
                                  Xapian::DB_CREATE_OR_OPEN);
}

// Constructor của XapianManager
XapianManager::XapianManager(const std::string &db_name,
                             const mvm::Address &addr)
    : db(openXapianDb(addr, db_name)), // Mở hoặc tạo database (path-based
                                       // hoặc InMemory, xem openXapianDb)
      address(addr),                 // Lưu địa chỉ liên kết
      last_access_time(
          std::chrono::steady_clock::now()), // Khởi tạo thời gian truy cập
      db_name(db_name)                       // Lưu tên database
{
    // Fix: Gán db_name cho comprehensive_log để truyền qua P2P Sync đúng đắn
    comprehensive_log.db_name = db_name;

    // Khởi tạo pool các Database objects để tái sử dụng cho concurrent search.
    // Mỗi goroutine search lấy 1 DB từ pool → đảm bảo không có 2 goroutine dùng chung.
    std::string db_path_str = mvm::createFullPath(addr, db_name).string();
    search_pool.pool.resize(MAX_CONCURRENT_SEARCHES, nullptr);
    search_pool.in_use.resize(MAX_CONCURRENT_SEARCHES, false);
    search_pool.last_gen.resize(MAX_CONCURRENT_SEARCHES, 0);

    simple_read_pool.pool.resize(MAX_CONCURRENT_SIMPLE_READS, nullptr);
    simple_read_pool.in_use.resize(MAX_CONCURRENT_SIMPLE_READS, false);
    simple_read_pool.last_gen.resize(MAX_CONCURRENT_SIMPLE_READS, 0);
}

XapianManager::~XapianManager() {
    for (size_t i = 0; i < search_pool.pool.size(); ++i) {
        if (search_pool.pool[i]) {
            try {
                search_pool.pool[i]->close();
            } catch (...) {}
            delete search_pool.pool[i];
            search_pool.pool[i] = nullptr;
        }
    }
    for (size_t i = 0; i < simple_read_pool.pool.size(); ++i) {
        if (simple_read_pool.pool[i]) {
            try {
                simple_read_pool.pool[i]->close();
            } catch (...) {}
            delete simple_read_pool.pool[i];
            simple_read_pool.pool[i] = nullptr;
        }
    }
}


// Lấy 1 DB từ pool (đợi tối đa 5s, nếu tất cả đang bận hoặc hỏng thì trả về nullptr).
// Trước khi trả về, nếu slot đó cũ hơn generation hiện tại (tức có commit xảy ra
// từ lần cuối slot này được dùng/reopen), ta reopen() ngay tại đây — bất kể lúc
// commit slot này đang bận hay rảnh. Đây là điểm mấu chốt để không bao giờ giao
// cho caller một Database đã stale so với dữ liệu đã commit trên đĩa.
Xapian::Database* XapianManager::acquireSearchDb()
{
    // 2026-08-20 (plan §9.29): the pool below exists to hand out
    // independent read-only Database handles re-opened from the same
    // on-disk path, for concurrent search from multiple threads. Neither
    // half of that makes sense for Xapian::InMemory (no path to reopen
    // from) -- and the TA's own execution model never has concurrent
    // interpreter threads anyway (single serialized dispatch loop, see
    // note/tee_dual_mode_execution_plan.md's "Hệ quả thiết kế then chốt"
    // #3), so there's no real pooling need to replace, just bypass. `db`
    // (WritableDatabase, IS-A Database) is used directly -- safe since
    // there is never more than one caller in flight in this mode.
    // releaseSearchDb() already no-ops safely on a pointer it doesn't
    // recognize (it just doesn't find a matching pool slot), so no paired
    // change is needed there.
    if (mvm::IsXapianBasePathEmpty()) {
        return &db;
    }

    std::unique_lock<std::mutex> lock(search_pool.pool_mutex);
    bool acquired = search_pool.pool_cv.wait_for(lock, std::chrono::seconds(5), [this]() {
        for (size_t i = 0; i < search_pool.in_use.size(); ++i)
            if (!search_pool.in_use[i])
                return true;
        return false;
    });

    if (!acquired) {
        std::cerr << "[ERROR] acquireSearchDb: Timeout (5s) waiting for available DB." << std::endl;
        return nullptr;
    }

    uint64_t current_gen = db_generation.load(std::memory_order_acquire);
    for (size_t i = 0; i < search_pool.pool.size(); ++i) {
        if (!search_pool.in_use[i]) {
            if (search_pool.pool[i] == nullptr) {
                // Lazy init
                try {
                    search_pool.pool[i] = new Xapian::Database(
                        mvm::createFullPath(address, db_name).string());
                    search_pool.last_gen[i] = current_gen;
                } catch (const std::exception& e) {
                    std::cerr << "[FATAL] acquireSearchDb: failed to init slot " << i
                              << ": " << e.what() << std::endl;
                    continue; // try next slot
                }
            } else if (search_pool.last_gen[i] < current_gen) {
                // Reopen existing
                try {
                    search_pool.pool[i]->reopen();
                    search_pool.last_gen[i] = current_gen;
                } catch (const std::exception& e) {
                    std::cerr << "[ERROR] acquireSearchDb: reopen() failed for slot " << i
                              << ": " << e.what() << ", recreating..." << std::endl;
                    delete search_pool.pool[i];
                    search_pool.pool[i] = nullptr;
                    try {
                        search_pool.pool[i] = new Xapian::Database(
                            mvm::createFullPath(address, db_name).string());
                        search_pool.last_gen[i] = current_gen;
                    } catch (const std::exception& e2) {
                        std::cerr << "[FATAL] acquireSearchDb: failed to recreate slot " << i
                                  << ": " << e2.what() << std::endl;
                        continue;
                    }
                }
            }
            search_pool.in_use[i] = true;
            return search_pool.pool[i];
        }
    }
    return nullptr;
}

// Trả DB về pool sau khi search xong.
void XapianManager::releaseSearchDb(Xapian::Database* db_returned)
{
    std::lock_guard<std::mutex> lock(search_pool.pool_mutex);
    for (size_t i = 0; i < search_pool.pool.size(); ++i) {
        if (search_pool.pool[i] == db_returned) {
            search_pool.in_use[i] = false;
            search_pool.pool_cv.notify_one();
            return;
        }
    }
}

Xapian::Database* XapianManager::acquireSimpleReadDb()
{
    // Same InMemory bypass as acquireSearchDb() above -- see that
    // function's comment for the full reasoning.
    if (mvm::IsXapianBasePathEmpty()) {
        return &db;
    }

    std::unique_lock<std::mutex> lock(simple_read_pool.pool_mutex);
    bool acquired = simple_read_pool.pool_cv.wait_for(lock, std::chrono::seconds(5), [this]() {
        for (size_t i = 0; i < simple_read_pool.in_use.size(); ++i)
            if (!simple_read_pool.in_use[i])
                return true;
        return false;
    });

    if (!acquired) {
        std::cerr << "[ERROR] acquireSimpleReadDb: Timeout (5s) waiting for available DB." << std::endl;
        return nullptr;
    }

    uint64_t current_gen = db_generation.load(std::memory_order_acquire);
    for (size_t i = 0; i < simple_read_pool.pool.size(); ++i) {
        if (!simple_read_pool.in_use[i]) {
            if (simple_read_pool.pool[i] == nullptr) {
                // Lazy init
                try {
                    simple_read_pool.pool[i] = new Xapian::Database(
                        mvm::createFullPath(address, db_name).string());
                    simple_read_pool.last_gen[i] = current_gen;
                } catch (const std::exception& e) {
                    std::cerr << "[FATAL] acquireSimpleReadDb: failed to init slot " << i
                              << ": " << e.what() << std::endl;
                    continue; // try next slot
                }
            } else if (simple_read_pool.last_gen[i] < current_gen) {
                // Reopen existing
                try {
                    simple_read_pool.pool[i]->reopen();
                    simple_read_pool.last_gen[i] = current_gen;
                } catch (const std::exception& e) {
                    std::cerr << "[ERROR] acquireSimpleReadDb: reopen() failed for slot " << i
                              << ": " << e.what() << ", recreating..." << std::endl;
                    delete simple_read_pool.pool[i];
                    simple_read_pool.pool[i] = nullptr;
                    try {
                        simple_read_pool.pool[i] = new Xapian::Database(
                            mvm::createFullPath(address, db_name).string());
                        simple_read_pool.last_gen[i] = current_gen;
                    } catch (const std::exception& e2) {
                        std::cerr << "[FATAL] acquireSimpleReadDb: failed to recreate slot " << i
                                  << ": " << e2.what() << std::endl;
                        continue;
                    }
                }
            }
            simple_read_pool.in_use[i] = true;
            return simple_read_pool.pool[i];
        }
    }
    return nullptr;
}

void XapianManager::releaseSimpleReadDb(Xapian::Database* db_returned)
{
    std::lock_guard<std::mutex> lock(simple_read_pool.pool_mutex);
    for (size_t i = 0; i < simple_read_pool.pool.size(); ++i) {
        if (simple_read_pool.pool[i] == db_returned) {
            simple_read_pool.in_use[i] = false;
            simple_read_pool.pool_cv.notify_one();
            return;
        }
    }
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
        
        // Tạo virtualDocId tất định dựa trên txHash và doc_index (luôn có tiền tố 0x chuẩn uint256)
        std::string input_for_hash = txHashStr + "_" + std::to_string(doc_index);
        virtualDocId = "0x" + mvm::keccak256(input_for_hash);
        
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
Xapian::Document XapianManager::get_overlayed_document(const std::string& virtualDocId, const uint256_t *txHash, const uint256_t *writerHash, Xapian::Database* search_db) {
    Xapian::Document doc;
    bool found = false;
    
    try {
        Xapian::docid did = resolveVirtualDocId(virtualDocId, search_db);
        if (did != 0) {
            if (search_db != nullptr) {
                doc = search_db->get_document(did);
            } else {
                doc = db.get_document(did);
            }
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

struct ScopedSearchDb {
    XapianManager* manager;
    Xapian::Database* db;
    ScopedSearchDb(XapianManager* mgr) : manager(mgr), db(mgr->acquireSearchDb()) {}
    ~ScopedSearchDb() {
        if (db) manager->releaseSearchDb(db);
    }
    Xapian::Database* get() const { return db; }
};

struct ScopedSimpleReadDb {
    XapianManager* manager;
    Xapian::Database* db;
    ScopedSimpleReadDb(XapianManager* mgr) : manager(mgr), db(mgr->acquireSimpleReadDb()) {}
    ~ScopedSimpleReadDb() {
        if (db) manager->releaseSimpleReadDb(db);
    }
    Xapian::Database* get() const { return db; }
};

std::string XapianManager::get_data(const std::string& virtualDocId, uint256_t blockNumber, const uint256_t *txHash, const uint256_t *writerHash) {
  ScopedSimpleReadDb scopedDb(this);
  if (!scopedDb.get()) throw std::runtime_error("Failed to acquire DB slot for get_data");
  std::shared_lock<std::shared_mutex> read_lock(changes_mutex);
  try {
    Xapian::Document doc = get_overlayed_document(virtualDocId, txHash, writerHash, scopedDb.get());
    return doc.get_data();
  } catch (const Xapian::DocNotFoundError&) {
    // Normal, ignore
  } catch (...) {
    throw;
  }
  return "";
}

// Lấy giá trị từ một slot của document tại một block number, có tùy chọn
// unserialize
std::string XapianManager::get_value(const std::string& virtualDocId, Xapian::valueno slot, bool isSerialise, uint256_t blockNumber, const uint256_t *txHash, const uint256_t *writerHash) {
    ScopedSimpleReadDb scopedDb(this);
    if (!scopedDb.get()) throw std::runtime_error("Failed to acquire DB slot for get_value");
    std::shared_lock<std::shared_mutex> read_lock(changes_mutex);
    try {
        Xapian::Document doc = get_overlayed_document(virtualDocId, txHash, writerHash, scopedDb.get());
        return doc.get_value(slot);
    } catch (const Xapian::DocNotFoundError&) {
        // Normal, ignore
    } catch (...) {
        throw;
    }
    return "";
}

// Lấy thông tin (data, value slot 1, value slot 2) của document tại một block number
DocumentInfo XapianManager::get_document(const std::string& virtualDocId, uint256_t blockNumber, const uint256_t *txHash, const uint256_t *writerHash) {
  ScopedSimpleReadDb scopedDb(this);
  DocumentInfo info;
  if (!scopedDb.get()) throw std::runtime_error("Failed to acquire DB slot for get_document");
  std::shared_lock<std::shared_mutex> read_lock(changes_mutex);
  try {
    Xapian::Document doc = get_overlayed_document(virtualDocId, txHash, writerHash, scopedDb.get());
    info.data = doc.get_data();
  } catch (const Xapian::DocNotFoundError&) {
    // Normal, ignore
  } catch (...) {
    throw;
  }
  return info;
}

// Lấy danh sách các term của document tại một block number
std::vector<std::string> XapianManager::get_terms(const std::string& virtualDocId,
                                                  uint256_t blockNumber, const uint256_t *txHash, const uint256_t *writerHash) {
  ScopedSimpleReadDb scopedDb(this);
  std::vector<std::string> terms;
  if (!scopedDb.get()) throw std::runtime_error("Failed to acquire DB slot for get_terms");
  std::shared_lock<std::shared_mutex> read_lock(changes_mutex);
  try {
    Xapian::Document doc = get_overlayed_document(virtualDocId, txHash, writerHash, scopedDb.get());
    for (auto term_it = doc.termlist_begin(); term_it != doc.termlist_end(); ++term_it) {
      terms.push_back(*term_it);
    }
  } catch (const Xapian::DocNotFoundError&) {
    // Normal, ignore
  } catch (...) {
    throw;
  }
  return terms;
}

// Commit các thay đổi đã được staged vào database Xapian
bool XapianManager::commit_changes() {
  touch(); // Cập nhật thời gian truy cập
  std::lock_guard<std::shared_mutex> lock(changes_mutex);
  bool has_writes = has_uncommitted_writes.load(std::memory_order_acquire);
  if (!has_writes && comprehensive_log.xapian_doc_logs.empty()) {
    return true; // Không có thay đổi nào cần commit -> Thoát ngay lập tức (zero-cost)
  }
  try {
    db.commit(); // Thực hiện commit Xapian (áp dụng cả comprehensive_log và replay_log)
    comprehensive_log.xapian_doc_logs.clear(); // Xóa các log đã staged sau khi commit thành công
    has_uncommitted_writes.store(false, std::memory_order_release);
    // 2026-08-20 (plan §9.29): these writes are now permanent (db.commit()
    // succeeded) -- the InMemory-mode undo snapshot for them is no longer
    // needed. No-op (empty map) in cgo/disk mode. See
    // revertUncommittedChanges()'s own doc comment for the full picture.
    undo_snapshot_.clear();
  } catch (const Xapian::Error &e) {
    std::cerr << "[Xapian Commit ERROR] Xapian::Error in commit_changes: " << e.get_msg() << std::endl;
    return false;
  } catch (const std::exception &e) {
    std::cerr << "[Xapian Commit ERROR] std::exception in commit_changes: " << e.what() << std::endl;
    return false;
  } catch (...) {
    std::cerr << "[Xapian Commit ERROR] Unknown exception in commit_changes" << std::endl;
    return false;
  }
  // Đánh dấu có 1 generation mới
  db_generation.fetch_add(1, std::memory_order_acq_rel);
  uint64_t current_gen = db_generation.load(std::memory_order_acquire);

  // Best-effort: reopen ngay các slot đang rảnh trong search_pool
  {
    std::lock_guard<std::mutex> pool_lock(search_pool.pool_mutex);
    for (size_t i = 0; i < search_pool.pool.size(); ++i) {
      if (search_pool.pool[i] && !search_pool.in_use[i]) {
        try {
          search_pool.pool[i]->reopen();
          search_pool.last_gen[i] = current_gen;
        } catch (...) {}
      }
    }
  }

  // Best-effort: reopen ngay các slot đang rảnh trong simple_read_pool
  {
    std::lock_guard<std::mutex> pool_lock(simple_read_pool.pool_mutex);
    for (size_t i = 0; i < simple_read_pool.pool.size(); ++i) {
      if (simple_read_pool.pool[i] && !simple_read_pool.in_use[i]) {
        try {
          simple_read_pool.pool[i]->reopen();
          simple_read_pool.last_gen[i] = current_gen;
        } catch (...) {}
      }
    }
  }
  return true;
}


void XapianManager::commitAllInstances() {
  std::unique_lock<std::shared_mutex> lock(instances_mutex);
  for (auto &pair : instances) {
    auto manager = pair.second;
    if (manager && !manager->has_started) {
      manager->commit_changes();
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
                    if (it->second && it->second->is_idle_for(std::chrono::minutes(1))) // Ngưỡng idle là 1 phút
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

    // Giai đoạn 2: Thực hiện xóa các instance đã xác định.
    // onlyIfIdle=true để destroyInstance() tự re-kiểm tra idle/refcount một
    // cách atomic ngay trước khi erase, phòng trường hợp có request mới
    // (getInstance/search) xen vào giữa Giai đoạn 1 và Giai đoạn 2.
    for (const std::string &key : keys_to_erase) {
      // Gọi hàm destroyInstance để xử lý việc đóng DB, dọn dẹp và xóa khỏi map
      XapianManager::destroyInstance(key, /*onlyIfIdle=*/true);
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

// Áp dụng lại một danh sách các log entry vào database hiện tại với Doc Coalescing
bool XapianManager::replay_log(const std::vector<XapianLog::LogEntry> &log_to_replay) {
    if (log_to_replay.empty()) return true;
    std::unique_lock<std::shared_mutex> db_lock(changes_mutex);
    has_uncommitted_writes.store(true, std::memory_order_release);

    // Document staging cache for batch coalescing (tối ưu hóa gom các thao tác trên cùng 1 docid trong 1 block)
    std::map<std::string, Xapian::Document> modified_docs;
    std::set<std::string> deleted_docs;

    auto normalize_docid = [](const std::string& id) -> std::string {
        if (id.length() >= 2 && (id[0] == '0' && (id[1] == 'x' || id[1] == 'X'))) return id;
        return "0x" + id;
    };

    // 2026-08-20 (plan §9.29): InMemory-mode-only pre-image capture for
    // revertUncommittedChanges() -- see undo_snapshot_'s own doc comment
    // (xapian_manager.h) for the full design. Cheap early-return in
    // cgo/disk mode (the common case), so zero added cost there.
    const bool undo_tracking_needed = mvm::IsXapianBasePathEmpty();
    auto capture_undo_if_needed = [&](const std::string& nid) {
        if (!undo_tracking_needed) return;
        if (undo_snapshot_.find(nid) != undo_snapshot_.end()) return; // already captured this batch
        Xapian::docid did = resolveVirtualDocId(nid);
        if (did > 0) {
            try {
                undo_snapshot_[nid] = clone_document(db.get_document(did));
                return;
            } catch (const Xapian::DocNotFoundError&) {
                // fall through -- treat as "did not exist"
            } catch (const std::exception&) {
                // fall through -- best-effort: treat as "did not exist"
                // rather than skip tracking it entirely (leaving a gap
                // revert couldn't undo would be worse)
            }
        }
        undo_snapshot_[nid] = std::nullopt; // did not exist before this batch
    };

    auto get_or_load_doc = [&](const std::string& v_docid) -> Xapian::Document& {
        std::string nid = normalize_docid(v_docid);
        capture_undo_if_needed(nid);
        auto it = modified_docs.find(nid);
        if (it != modified_docs.end()) {
            return it->second;
        }

        Xapian::Document doc;
        Xapian::docid did = resolveVirtualDocId(nid);
        bool loaded = false;
        if (did > 0) {
            try {
                doc = db.get_document(did);
                loaded = true;
            } catch (const Xapian::DocNotFoundError&) {
                loaded = false;
            } catch (const std::exception& e) {
                std::cerr << "[Xapian Replay WARN] get_document(" << did << ") error: " << e.what() << std::endl;
                loaded = false;
            }
        }

        if (!loaded) {
            std::string clean_id = nid;
            if (clean_id.length() >= 2 && clean_id.substr(0, 2) == "0x") clean_id = clean_id.substr(2);
            doc.add_term("Q" + clean_id);
            std::cerr << "[Xapian Replay INFO] docid '" << nid << "' not present in local DB (did=0). Initializing in memory with term Q" << clean_id << std::endl;
        }

        auto inserted = modified_docs.emplace(nid, std::move(doc));
        deleted_docs.erase(nid);
        return inserted.first->second;
    };

    try {
        for (const auto& entry : log_to_replay) {
            if (entry.op == XapianLog::Operation::NEW_DOC) {
                auto data = std::get<XapianLog::NewDocData>(entry.data);
                std::string nid = normalize_docid(data.docid);
                Xapian::Document& doc = get_or_load_doc(nid);
                doc.set_data(data.data);
            } else if (entry.op == XapianLog::Operation::DEL_DOC) {
                auto data = std::get<XapianLog::DelDocData>(entry.data);
                std::string nid = normalize_docid(data.docid);
                capture_undo_if_needed(nid); // doesn't go through get_or_load_doc
                modified_docs.erase(nid);
                deleted_docs.insert(nid);
            } else if (entry.op == XapianLog::Operation::ADD_VALUE) {
                auto data = std::get<XapianLog::AddValueData>(entry.data);
                std::string nid = normalize_docid(data.docid);
                Xapian::Document& doc = get_or_load_doc(nid);
                doc.add_value(data.slot, data.value);
            } else if (entry.op == XapianLog::Operation::ADD_TERM) {
                auto data = std::get<XapianLog::AddTermData>(entry.data);
                std::string nid = normalize_docid(data.docid);
                Xapian::Document& doc = get_or_load_doc(nid);
                doc.add_term(data.term);
            } else if (entry.op == XapianLog::Operation::SET_DATA) {
                auto data = std::get<XapianLog::SetDataData>(entry.data);
                std::string nid = normalize_docid(data.docid);
                Xapian::Document& doc = get_or_load_doc(nid);
                doc.set_data(data.data);
            } else if (entry.op == XapianLog::Operation::INDEX_TEXT) {
                auto data = std::get<XapianLog::IndexTextData>(entry.data);
                std::string nid = normalize_docid(data.docid);
                Xapian::Document& doc = get_or_load_doc(nid);
                Xapian::TermGenerator termgenerator;
                termgenerator.set_stemmer(Xapian::Stem("english"));
                termgenerator.set_stemming_strategy(Xapian::TermGenerator::STEM_SOME);
                termgenerator.set_document(doc);
                termgenerator.index_text(data.text, data.wdf_inc, data.prefix);
            }
        }

        // Apply deletions
        for (const auto& v_docid : deleted_docs) {
            Xapian::docid did = resolveVirtualDocId(v_docid);
            if (did > 0) {
                try {
                    db.delete_document(did);
                } catch (const std::exception& e) {
                    std::cerr << "[Xapian Replay WARN] delete_document(" << did << ") error: " << e.what() << std::endl;
                }
            }
        }

        // Apply coalesced modified documents (1 write per docid, avoiding B-Tree saturation)
        for (auto& pair : modified_docs) {
            const std::string& v_docid = pair.first;
            Xapian::Document& doc = pair.second;
            Xapian::docid did = resolveVirtualDocId(v_docid);
            if (did > 0) {
                db.replace_document(did, doc);
            } else {
                Xapian::docid new_did = db.add_document(doc);
                std::cerr << "[Xapian Replay INFO] Persisted new document on disk for '" << v_docid << "' -> assigned internal did=" << new_did << std::endl;
            }
        }
        return true;
    } catch (const Xapian::Error& e) {
        std::cerr << "[Xapian Replay ERROR] Xapian::Error in replay_log: " << e.get_msg() << " (context: " << e.get_context() << ")" << std::endl;
        return false;
    } catch (const std::exception& e) {
        std::cerr << "[Xapian Replay ERROR] std::exception in replay_log: " << e.what() << std::endl;
        return false;
    } catch (...) {
        std::cerr << "[Xapian Replay ERROR] Unknown exception in replay_log" << std::endl;
        return false;
    }
}

// Khôi phục trạng thái về lần commit cuối cùng bằng cách xóa log staged và mở
// lại DB
bool XapianManager::revertUncommittedChanges() {
  std::unique_lock<std::shared_mutex> lock(changes_mutex); // Khóa để thao tác an toàn
  bool has_writes = has_uncommitted_writes.load(std::memory_order_acquire);
  
  // FORK-SAFETY: Return immediately if there are no uncommitted changes
  if (!has_writes && comprehensive_log.xapian_doc_logs.empty()) {
      return true;
  }
  
  try {
    // 1. Xóa các thay đổi đang chờ trong log và reset cờ dirty
    comprehensive_log.xapian_doc_logs.clear();
    has_uncommitted_writes.store(false, std::memory_order_release);

    if (mvm::IsXapianBasePathEmpty()) {
      // 2026-08-20 (plan §9.29): Xapian::InMemory's commit()/cancel() are
      // BOTH hard no-ops -- confirmed by reading Xapian's own source
      // (backends/inmemory/inmemory_database.cc: "We implicitly commit
      // each modification right away, so nothing to do here" for both).
      // So the close()+reopen-from-disk trick below isn't just
      // inconvenient for InMemory, it's architecturally impossible:
      // InMemoryDatabase::close() destroys ALL data unconditionally
      // (there is no "disk" underneath to reload from -- close() clears
      // every internal map and marks the db closed for good). Manually
      // replay the pre-image snapshot captured by replay_log()'s
      // capture_undo_if_needed() instead: restore what existed before
      // this uncommitted batch, delete what didn't exist before it. See
      // undo_snapshot_'s own doc comment (xapian_manager.h) for the full
      // design and how/when it's populated.
      // 2026-08-20 (plan §9.29 follow-up): granular per-call bracketing --
      // a hardware run of this exact loop threw an uncaught
      // Xapian::DocNotFoundError that escaped ALL THREE surrounding
      // try/catch layers (this function's own, the caller's, main()'s) and
      // crashed the whole TA (chcore/musl's std::terminate() path itself
      // isn't fully implemented -- "Unsupported syscall 130" -- so an
      // uncaught exception is unrecoverable here, not just noisy). Each
      // individual Xapian call gets its own try/catch here so the NEXT
      // hardware run pinpoints exactly which one throws, instead of
      // needing a second round to add this.
      for (auto &entry : undo_snapshot_) {
        const std::string &nid = entry.first;
        std::optional<Xapian::Document> &pre_image = entry.second;
        fprintf(stderr, "[xapian_manager][DIAG] revert: resolving nid=%s\n", nid.c_str());
        fflush(stderr);
        Xapian::docid did = resolveVirtualDocId(nid);
        fprintf(stderr, "[xapian_manager][DIAG] revert: did=%u has_pre_image=%d\n",
            (unsigned)did, (int)pre_image.has_value());
        fflush(stderr);
        try {
          if (pre_image.has_value()) {
            if (did > 0) {
              fprintf(stderr, "[xapian_manager][DIAG] revert: about to replace_document(%u)\n", (unsigned)did);
              fflush(stderr);
              db.replace_document(did, *pre_image);
              fprintf(stderr, "[xapian_manager][DIAG] revert: replace_document OK\n");
              fflush(stderr);
            } else {
              // Existed before this batch, but resolveVirtualDocId can't
              // find it now -- shouldn't happen (nothing removes a doc's
              // own "Q<id>" term without also deleting the doc itself), but
              // re-add rather than silently drop real pre-existing data if
              // it ever does.
              fprintf(stderr, "[xapian_manager][DIAG] revert: about to add_document (re-add)\n");
              fflush(stderr);
              db.add_document(*pre_image);
              fprintf(stderr, "[xapian_manager][DIAG] revert: add_document OK\n");
              fflush(stderr);
            }
          } else if (did > 0) {
            fprintf(stderr, "[xapian_manager][DIAG] revert: about to delete_document(%u)\n", (unsigned)did);
            fflush(stderr);
            db.delete_document(did); // did not exist before this batch
            fprintf(stderr, "[xapian_manager][DIAG] revert: delete_document OK\n");
            fflush(stderr);
          }
        } catch (const Xapian::Error &e) {
          fprintf(stderr, "[xapian_manager][DIAG] revert: Xapian::Error on nid=%s: %s (type=%s)\n",
              nid.c_str(), e.get_msg().c_str(), e.get_type());
          fflush(stderr);
          // Best-effort: this one entry's undo failed, but don't let it
          // take down the whole revert (or the TA) -- continue with the
          // rest of the snapshot.
        } catch (const std::exception &e) {
          fprintf(stderr, "[xapian_manager][DIAG] revert: std::exception on nid=%s: %s\n", nid.c_str(), e.what());
          fflush(stderr);
        } catch (...) {
          fprintf(stderr, "[xapian_manager][DIAG] revert: unknown exception on nid=%s\n", nid.c_str());
          fflush(stderr);
        }
      }
      undo_snapshot_.clear();
      fprintf(stderr, "[xapian_manager][DIAG] revert: InMemory branch RETURN true\n");
      fflush(stderr);
      return true;
    }

    // 2. cgo/disk mode (unchanged): đóng và mở lại database để hủy các
    // thay đổi chưa commit trong bộ nhớ Xapian
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
bool XapianManager::destroyInstance(const std::string &db_path_str, bool onlyIfIdle)
{
    std::shared_ptr<XapianManager> instance_ptr;

    {
        std::unique_lock<std::shared_mutex> write_lock(instances_mutex);
        auto it = instances.find(db_path_str);
        if (it != instances.end())
        {
            if (onlyIfIdle)
            {
                // Re-kiểm tra atomic dưới cùng lock với erase: nếu có thread khác
                // vừa lấy một tham chiếu mới (getInstance) hoặc vẫn đang giữ tham
                // chiếu từ trước kể từ lần kiểm tra sơ bộ (Giai đoạn 1 ở cleaner
                // thread), use_count() ở đây sẽ > 1 (chỉ map giữ tham chiếu là
                // baseline = 1) và ta bỏ qua việc hủy để tránh đóng `db` trong
                // lúc đang được sử dụng.
                if (!it->second || !it->second->is_idle_for(std::chrono::minutes(1)) ||
                    it->second.use_count() > 1)
                {
                    return false;
                }
            }
            instance_ptr = it->second; // Giữ một tham chiếu tạm thời
            instances.erase(it);
        }
    }

    if (instance_ptr)
    {
        try
        {
            // Khóa changes_mutex trước khi đóng db, cùng quy ước với mọi thao
            // tác khác ghi vào `db` (add_document/replace_document/commit/
            // revertUncommittedChanges), để tránh race đóng db trong lúc một
            // thread khác đang ghi vào nó.
            std::unique_lock<std::shared_mutex> changes_lock(instance_ptr->changes_mutex);

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
    comprehensive_log.db_name = this->db_name; // Restore db_name after std::move
    return log_copy;
}

// Lấy một bản ghi log tổng hợp chứa tất cả các thay đổi đã staged
XapianLog::ComprehensiveLog XapianManager::removeLogsUntilNearestEndCommand()
{
    std::lock_guard<std::shared_mutex> lock(changes_mutex);
    comprehensive_log.removeLogsUntilNearestEndCommand();
    return comprehensive_log;
}