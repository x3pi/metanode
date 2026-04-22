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

// Thêm một document mới vào database
Xapian::docid XapianManager::new_document(const std::string &data, uint256_t blockNumber)
{
    touch();
    std::lock_guard<std::mutex> lock(changes_mutex);

    try
    {
        bool just_started = false;
        if (!this->has_started)
        {
            db.begin_transaction();
            this->has_started = true;
            just_started = true;
        }
        Xapian::Document doc;
        doc.set_data(data); // Đặt dữ liệu thô cho document
        // Thêm block number tạo document vào slot 253 (đã serialize)
        auto blockNb_serialised = Xapian::sortable_serialise(mvm::uint256_to_double(blockNumber));
        doc.add_value(253, blockNb_serialised);

        Xapian::docid id = db.add_document(doc); // Thêm document vào database

        // FORK-SAFETY: Use deterministic logical ID based on db_name + docid
        // instead of random UUID to ensure identical Xapian state across all nodes.
        auto localId = db_name + "_" + std::to_string(id);
        doc.add_term(LOGICAL_ID_GENERATED_PREFIX + localId);
        db.replace_document(id, doc); // Update document with the deterministic term

        // Ghi log thay đổi (staged)
        XapianLog::LogEntry entry;
        entry.op = XapianLog::Operation::NEW_DOC;
        XapianLog::NewDocData logData;
        logData.docid = id;  // Lưu docid được trả về
        logData.data = data; // Lưu dữ liệu gốc
        entry.data = logData;
        if (just_started)
        {
            entry.command_type = XapianLog::CommandType::START;
        }
        comprehensive_log.push_back(entry);
        return id; // Trả về docid của document mới
    }
    catch (const Xapian::Error &)
    {
        return 0; /* Trả về 0 nếu có lỗi Xapian */
    }
    catch (const std::exception &)
    {
        return 0; /* Trả về 0 nếu có lỗi standard */
    }
}


// Đánh dấu một document là đã bị xóa (soft delete) tại một block number cụ thể
bool XapianManager::delete_document(Xapian::docid did, uint256_t blockNumber)
{
    touch(); // Cập nhật thời gian truy cập
    std::lock_guard<std::mutex> lock(changes_mutex);
    try
    {
        bool just_started = false;
        if (!this->has_started)
        {
            db.begin_transaction();
            this->has_started = true;
            just_started = true;
        }
        // Lấy document hiện tại để đảm bảo nó tồn tại trước khi ghi log
        Xapian::Document doc = db.get_document(did);
        // Thêm block number xóa vào slot 254 (đã serialize)
        auto blockNb_serialised = Xapian::sortable_serialise(mvm::uint256_to_double(blockNumber));
        doc.add_value(254, blockNb_serialised);
        // Thay thế document cũ bằng document đã cập nhật (thêm slot 254)
        db.replace_document(did, doc);
        // Ghi log thay đổi (staged)
        XapianLog::LogEntry entry;
        entry.op = XapianLog::Operation::DEL_DOC;
        XapianLog::DelDocData logData;
        logData.docid = did;
        entry.data = logData;
        if (just_started)
        {
            entry.command_type = XapianLog::CommandType::START;
        }
        comprehensive_log.push_back(entry);
        
        return true;
    }
    catch (const Xapian::DocNotFoundError &)
    {
        return false; /* Document không tồn tại */
    }
    catch (const Xapian::Error &)
    {
        return false; /* Lỗi Xapian khác */
    }
    catch (const std::exception &)
    {
        return false; /* Lỗi standard */
    }
}

// Thêm một value vào một slot của document, có xử lý versioning theo blockNumber
Xapian::docid XapianManager::add_value(Xapian::docid did, Xapian::valueno slot, const std::string &value, bool isSerialise, uint256_t blockNumber)
{
    touch(); // Cập nhật thời gian truy cập
    std::lock_guard<std::mutex> lock(changes_mutex);
    try
    {
        bool just_started = false;
        if (!this->has_started)
        {
            db.begin_transaction();
            this->has_started = true;
            just_started = true;
        }
        Xapian::Document old_doc = db.get_document(did);                                           // Lấy phiên bản cũ
        auto blockNb_serialised = Xapian::sortable_serialise(mvm::uint256_to_double(blockNumber)); // Block number hiện tại
        std::string existing_blockNb_serialised;
        try
        {
            existing_blockNb_serialised = old_doc.get_value(253);
        }
        catch (...)
        {
        } // Lấy block number tạo của phiên bản cũ

        std::string value_to_add = value;
        std::string value_to_log = value;

        // Serialize giá trị nếu được yêu cầu
        if (isSerialise)
        {
            try
            {
                double num = std::stod(value);
                value_to_add = Xapian::sortable_serialise(num);
                value_to_log = value_to_add; // Log giá trị đã serialize
            }
            catch (const std::invalid_argument &)
            {
                return 0; /* Định dạng số không hợp lệ */
            }
            catch (const std::out_of_range &)
            {
                return 0; /* Số ngoài phạm vi */
            }
        }

        Xapian::docid result_id = 0;

        // Nếu block number hiện tại trùng với block number tạo của phiên bản cũ -> cập nhật tại chỗ
        if (existing_blockNb_serialised == blockNb_serialised)
        {
            // Cùng block: update tại chỗ, trả về did gốc
            old_doc.add_value(slot, value_to_add);
            db.replace_document(did, old_doc);
            result_id = did;
        }
        else // Nếu block number khác -> tạo phiên bản mới
        {
            // Khác block: tạo version mới, trả về docid mới
            Xapian::Document new_version_doc = clone_document(old_doc);
            new_version_doc.add_value(slot, value_to_add);
            new_version_doc.add_value(253, blockNb_serialised);

            old_doc.add_value(254, blockNb_serialised);
            db.replace_document(did, old_doc);
            result_id = db.add_document(new_version_doc); // Lấy docid mới
        }
        
        // Ghi log thay đổi (staged)
        XapianLog::LogEntry entry;
        entry.op = XapianLog::Operation::ADD_VALUE;
        XapianLog::AddValueData logData;
        logData.docid = did;
        logData.slot = slot;
        logData.value = value_to_log;
        logData.is_serialised = isSerialise;
        entry.data = logData;
        if (just_started)
        {
            entry.command_type = XapianLog::CommandType::START;
        }
        comprehensive_log.push_back(entry);
        
        return result_id;
    }
    catch (const Xapian::DocNotFoundError &)
    {
        return 0;
    }
    catch (const Xapian::Error &)
    {
        return 0;
    }
    catch (const std::exception &)
    {
        return 0;
    }
}


// Thêm một term vào document, có xử lý versioning theo blockNumber
Xapian::docid XapianManager::add_term(Xapian::docid did, const std::string &term, uint256_t blockNumber)
{
    touch(); // Cập nhật thời gian truy cập
    std::lock_guard<std::mutex> lock(changes_mutex);
    try
    {
        bool just_started = false;
        if (!this->has_started)
        {
            db.begin_transaction();
            this->has_started = true;
            just_started = true;
        }
        Xapian::Document old_doc = db.get_document(did);
        auto blockNb_serialised = Xapian::sortable_serialise(mvm::uint256_to_double(blockNumber));
        std::string existing_blockNb_serialised;
        try
        {
            existing_blockNb_serialised = old_doc.get_value(253);
        }
        catch (...)
        {
        }

        Xapian::docid result_id = 0;

        // Cập nhật tại chỗ hoặc tạo phiên bản mới
        if (existing_blockNb_serialised == blockNb_serialised)
        {
            // Cùng block: update tại chỗ, trả về did gốc
            old_doc.add_term(term);
            db.replace_document(did, old_doc);
            result_id = did;
        }
        else
        {
            // Khác block: tạo version mới, trả về docid mới
            Xapian::Document new_version_doc = clone_document(old_doc);
            new_version_doc.add_term(term);
            new_version_doc.add_value(253, blockNb_serialised);

            old_doc.add_value(254, blockNb_serialised);
            db.replace_document(did, old_doc);
            result_id = db.add_document(new_version_doc); // Lấy docid mới
        }
        
        // Ghi log thay đổi (staged)
        XapianLog::LogEntry entry;
        entry.op = XapianLog::Operation::ADD_TERM;
        XapianLog::AddTermData logData;
        logData.docid = did;
        logData.term = term;
        entry.data = logData;
        if (just_started)
        {
            entry.command_type = XapianLog::CommandType::START;
        }
        comprehensive_log.push_back(entry);
        
        return result_id;
    }
    catch (const Xapian::DocNotFoundError &)
    {
        return 0;
    }
    catch (const Xapian::Error &)
    {
        return 0;
    }
    catch (const std::exception &)
    {
        return 0;
    }
}

// Index một đoạn text vào document (thêm các term được tạo ra từ text)
Xapian::docid XapianManager::index_text(Xapian::docid did, const std::string &text_to_index, Xapian::termcount wdf_inc, const std::string prefix, uint256_t blockNumber)
{
    touch(); // Cập nhật thời gian truy cập
    std::lock_guard<std::mutex> lock(changes_mutex);
    try
    {
        bool just_started = false;
        if (!this->has_started)
        {
            db.begin_transaction();
            this->has_started = true;
            just_started = true;
        }
        Xapian::Document old_doc = db.get_document(did);
        auto blockNb_serialised = Xapian::sortable_serialise(mvm::uint256_to_double(blockNumber));
        std::string existing_blockNb_serialised;
        try
        {
            existing_blockNb_serialised = old_doc.get_value(253);
        }
        catch (...)
        {
        }

        Xapian::TermGenerator term_generator; // Cần tạo mới mỗi lần dùng
        Xapian::docid result_id = 0;

        // Cập nhật tại chỗ hoặc tạo phiên bản mới
        if (existing_blockNb_serialised == blockNb_serialised)
        {
            // Cùng block: update tại chỗ, trả về did gốc
            term_generator.set_document(old_doc);
            term_generator.index_text(text_to_index, wdf_inc, prefix);
            db.replace_document(did, old_doc);
            result_id = did;
        }
        else
        {
            // Khác block: tạo version mới, trả về docid mới
            Xapian::Document new_version_doc = clone_document(old_doc);
            term_generator.set_document(new_version_doc);
            term_generator.index_text(text_to_index, wdf_inc, prefix);
            new_version_doc.add_value(253, blockNb_serialised);

            old_doc.add_value(254, blockNb_serialised);
            db.replace_document(did, old_doc);
            result_id = db.add_document(new_version_doc); // Lấy docid mới
        }
        
        // Ghi log thay đổi (staged)
        XapianLog::LogEntry entry;
        entry.op = XapianLog::Operation::INDEX_TEXT;
        XapianLog::IndexTextData logData;
        logData.docid = did;
        logData.text = text_to_index;
        logData.wdf_inc = wdf_inc;
        logData.prefix = prefix;
        entry.data = logData;
        if (just_started)
        {
            entry.command_type = XapianLog::CommandType::START;
        }
        comprehensive_log.push_back(entry);
        
        return result_id;
    }
    catch (const Xapian::DocNotFoundError &)
    {
        return 0;
    }
    catch (const Xapian::Error &)
    {
        return 0;
    }
    catch (const std::exception &)
    {
        return 0;
    }
}

// Đặt (ghi đè) dữ liệu thô cho một document, có xử lý versioning
Xapian::docid XapianManager::set_data(Xapian::docid did, const std::string &new_data, uint256_t blockNumber)
{
    touch(); // Cập nhật thời gian truy cập
    std::lock_guard<std::mutex> lock(changes_mutex);
    try
    {
        bool just_started = false;
        if (!this->has_started)
        {
            db.begin_transaction();
            this->has_started = true;
            just_started = true;
        }
        Xapian::Document old_doc = db.get_document(did);
        auto blockNb_serialised = Xapian::sortable_serialise(mvm::uint256_to_double(blockNumber));
        std::string existing_blockNb_serialised;
        try
        {
            existing_blockNb_serialised = old_doc.get_value(253);
        }
        catch (...)
        {
        }

        Xapian::docid result_id = 0;

        // Cập nhật tại chỗ hoặc tạo phiên bản mới
        if (existing_blockNb_serialised == blockNb_serialised)
        {
            // Cùng block: update tại chỗ, trả về did gốc
            old_doc.set_data(new_data);
            db.replace_document(did, old_doc);
            result_id = did;
        }
        else
        {
            // Khác block: tạo version mới, trả về docid mới
            Xapian::Document new_version_doc = clone_document(old_doc); // Clone để giữ term/value cũ
            new_version_doc.set_data(new_data);                         // Đặt dữ liệu mới
            new_version_doc.add_value(253, blockNb_serialised);         // Đặt block number tạo

            old_doc.add_value(254, blockNb_serialised); // Đánh dấu phiên bản cũ
            db.replace_document(did, old_doc);
            result_id = db.add_document(new_version_doc); // Thêm phiên bản mới, lấy docid mới
        }
        
        // Ghi log thay đổi (staged)
        XapianLog::LogEntry entry;
        entry.op = XapianLog::Operation::SET_DATA;
        XapianLog::SetDataData logData;
        logData.docid = did; // Log tham chiếu đến docid gốc
        logData.data = new_data;
        entry.data = logData;
        if (just_started)
        {
            entry.command_type = XapianLog::CommandType::START;
        }
        comprehensive_log.push_back(entry);
        
        return result_id; // Trả về docid (cũ nếu cùng block, mới nếu khác block)
    }
    catch (const Xapian::DocNotFoundError &)
    {
        return 0; // Document không tồn tại
    }
    catch (const Xapian::Error &)
    {
        return 0; // Lỗi Xapian
    }
    catch (const std::exception &)
    {
        return 0; // Lỗi standard
    }
}


// Lấy thông tin (data, value slot 1, value slot 2) của document tại một block number
DocumentInfo XapianManager::get_document(Xapian::docid did, uint256_t blockNumber)
{
    touch(); // Cập nhật thời gian truy cập
    std::lock_guard<std::mutex> lock(changes_mutex);
    try
    {
        Xapian::Document doc = db.get_document(did);

        // Kiểm tra xem document có bị "xóa" (đánh dấu ở slot 254) trước hoặc tại blockNumber không
        std::string slot254_str = doc.get_value(254);
        if (!slot254_str.empty())
        {
            double slot254_val = Xapian::sortable_unserialise(slot254_str);
            if (slot254_val <= mvm::uint256_to_double(blockNumber))
            {
                return {"", "", ""}; // Bị loại do đã bị xóa
            }
        }

        // Kiểm tra xem document có được tạo *sau* blockNumber không (dựa vào slot 253)
        std::string slot253_str = doc.get_value(253);
        if (!slot253_str.empty())
        {
            double slot253_val = Xapian::sortable_unserialise(slot253_str);
            if (slot253_val > mvm::uint256_to_double(blockNumber))
            {
                // Phiên bản document này được tạo sau blockNumber truy vấn
                return {"", "", ""}; // Bị loại do chưa tồn tại tại thời điểm đó
            }
        }

        // Lấy các giá trị cần thiết (giả định slot 1, 2 có ý nghĩa cụ thể)
        std::string author_val = doc.get_value(1);
        std::string content_val = doc.get_value(2);
        return {doc.get_data(), author_val, content_val};
    }
    catch (const Xapian::DocNotFoundError &)
    {
        return {"", "", ""};
    }
    catch (const Xapian::Error &)
    {
        return {"", "", ""};
    }
}


// Lấy dữ liệu thô của document tại một block number
std::string XapianManager::get_data(Xapian::docid did, uint256_t blockNumber) {
  touch();
  std::lock_guard<std::mutex> db_lock(changes_mutex);
  try {
    Xapian::Document doc = db.get_document(did);
    // Kiểm tra slot 254 (đã bị xóa)
    std::string slot254_str = doc.get_value(254);
    if (!slot254_str.empty()) {
      double slot254_val = Xapian::sortable_unserialise(slot254_str);
      if (slot254_val <= mvm::uint256_to_double(blockNumber)) {
        return "";
      }
    }
    // Kiểm tra slot 253 (chưa được tạo)
    std::string slot253_str = doc.get_value(253);
    if (!slot253_str.empty()) {
      double slot253_val = Xapian::sortable_unserialise(slot253_str);
      if (slot253_val > mvm::uint256_to_double(blockNumber)) {
        return "";
      }
    }
    return doc.get_data(); // Trả về dữ liệu thô
  } catch (const Xapian::DocNotFoundError &) {
    return "";
  } catch (const Xapian::Error &) {
    return "";
  }
}

// Lấy giá trị từ một slot của document tại một block number, có tùy chọn
// unserialize
std::string XapianManager::get_value(Xapian::docid did, Xapian::valueno slot,
                                     bool isSerialise, uint256_t blockNumber) {
  touch();
  std::lock_guard<std::mutex> db_lock(changes_mutex);
  try {
    Xapian::Document doc = db.get_document(did);
    // Kiểm tra slot 254 (đã bị xóa)
    std::string slot254_str = doc.get_value(254);
    if (!slot254_str.empty()) {
      double slot254_val = Xapian::sortable_unserialise(slot254_str);
      if (slot254_val <= mvm::uint256_to_double(blockNumber)) {
        return "";
      }
    }
    // Kiểm tra slot 253 (chưa được tạo)
    std::string slot253_str = doc.get_value(253);
    if (!slot253_str.empty()) {
      double slot253_val = Xapian::sortable_unserialise(slot253_str);
      if (slot253_val > mvm::uint256_to_double(blockNumber)) {
        return "";
      }
    }

    std::string data = doc.get_value(slot); // Lấy giá trị từ slot
    // Nếu yêu cầu unserialize và có dữ liệu
    if (isSerialise && !data.empty()) {
      try {
        double value = Xapian::sortable_unserialise(data); // Unserialize
        // Chuyển double thành string với định dạng mong muốn
        std::ostringstream oss;
        oss << std::fixed << std::setprecision(10) << value;
        std::string str_val = oss.str();
        // Xóa các số 0 thừa ở cuối phần thập phân
        str_val.erase(str_val.find_last_not_of('0') + 1, std::string::npos);
        // Xóa dấu '.' nếu nó là ký tự cuối cùng
        if (!str_val.empty() && str_val.back() == '.')
          str_val.pop_back();
        return str_val;
      } catch (const Xapian::Error &) {
        return ""; /* Lỗi unserialize */
      }
    }
    return data; // Trả về dữ liệu gốc nếu không unserialize hoặc không có dữ
                 // liệu
  } catch (const Xapian::DocNotFoundError &) {
    return "";
  } catch (const Xapian::Error &) {
    return "";
  }
}

// Lấy danh sách các term của document tại một block number
std::vector<std::string> XapianManager::get_terms(Xapian::docid did,
                                                  uint256_t blockNumber) {
  touch();
  std::lock_guard<std::mutex> db_lock(changes_mutex);
  std::vector<std::string> terms;
  try {
    Xapian::Document doc = db.get_document(did);
    // Kiểm tra slot 254 (đã bị xóa)
    std::string slot254_str = doc.get_value(254);
    if (!slot254_str.empty()) {
      double slot254_val = Xapian::sortable_unserialise(slot254_str);
      if (slot254_val <= mvm::uint256_to_double(blockNumber)) {
        return {}; // Trả về vector rỗng
      }
    }
    // Kiểm tra slot 253 (chưa được tạo)
    std::string slot253_str = doc.get_value(253);
    if (!slot253_str.empty()) {
      double slot253_val = Xapian::sortable_unserialise(slot253_str);
      if (slot253_val > mvm::uint256_to_double(blockNumber)) {
        return {}; // Trả về vector rỗng
      }
    }

    // Lặp qua danh sách term và thêm vào vector kết quả
    for (Xapian::TermIterator it = doc.termlist_begin();
         it != doc.termlist_end(); ++it) {
      terms.push_back(*it);
    }
  } catch (
      const Xapian::DocNotFoundError &) { /* Trả về vector rỗng (đã khởi tạo) */
  } catch (const Xapian::Error &) {       /* Trả về vector rỗng */
  }
  return terms;
}

// Commit các thay đổi đã được staged vào database Xapian
bool XapianManager::commit_changes() {
  touch(); // Cập nhật thời gian truy cập
  std::lock_guard<std::mutex> lock(
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

// Tính toán hash của các thay đổi đã được staged
std::array<uint8_t, 32u> XapianManager::getChangeHash() {
  std::lock_guard<std::mutex> lock(
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
  std::lock_guard<std::mutex> lock(
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
                std::shared_lock<std::shared_mutex> read_lock(XapianManager::instances_mutex);
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
bool XapianManager::replay_log(
    const std::vector<XapianLog::LogEntry> &log_to_replay) {
  touch();
  std::lock_guard<std::mutex> db_lock(db_mutex);
  if (log_to_replay.empty()) {
    return true; // Không có gì để replay
  }

  std::lock_guard<std::mutex> changes_lock(
      changes_mutex); // Khóa log thay đổi trong suốt quá trình replay

  // Quan trọng: Replay không nên thêm lại vào staged_changes_log
  // Nó phải trực tiếp thay đổi trạng thái database

  for (const auto &entry : log_to_replay) {
    bool success_op = false;
    try {
      // Sử dụng std::visit để xử lý từng loại operation trong log entry
      success_op = std::visit(
          [this](
              const auto &data_arg) -> bool { // Capture 'this' để truy cập 'db'
            using T = std::decay_t<decltype(data_arg)>;
            try {
              // Logic replay cho từng loại operation
              if constexpr (std::is_same_v<T, XapianLog::NewDocData>) {
                Xapian::Document doc;
                doc.set_data(data_arg.data);
                // Cần đảm bảo replay NEW_DOC xử lý đúng ID và các term/value
                // mặc định nếu có
                this->db.replace_document(
                    data_arg.docid,
                    doc); // Giả định replace hoạt động cho cả ID mới
                return true;
              } else if constexpr (std::is_same_v<T, XapianLog::DelDocData>) {
                // Logic gốc dùng replace_document để soft delete. Replay nên
                // làm tương tự. Nếu chỉ có docid trong log, cần fetch, thêm
                // value 254 rồi replace. Hoặc nếu replay có nghĩa là xóa cứng:
                this->db.delete_document(data_arg.docid);
                return true;
              } else if constexpr (std::is_same_v<T, XapianLog::AddValueData>) {
                Xapian::Document doc = this->db.get_document(data_arg.docid);
                doc.add_value(
                    data_arg.slot,
                    data_arg.value); // Giá trị trong log đã được xử lý
                this->db.replace_document(data_arg.docid, doc);
                return true;
              } else if constexpr (std::is_same_v<T, XapianLog::AddTermData>) {
                Xapian::Document doc = this->db.get_document(data_arg.docid);
                doc.add_term(data_arg.term);
                this->db.replace_document(data_arg.docid, doc);
                return true;
              } else if constexpr (std::is_same_v<T, XapianLog::SetDataData>) {
                Xapian::Document doc = this->db.get_document(data_arg.docid);
                doc.set_data(data_arg.data);
                this->db.replace_document(data_arg.docid, doc);
                return true;
              } else if constexpr (std::is_same_v<T,
                                                  XapianLog::IndexTextData>) {
                Xapian::Document doc = this->db.get_document(data_arg.docid);
                Xapian::TermGenerator tg;
                tg.set_document(doc);
                tg.index_text(data_arg.text, data_arg.wdf_inc, data_arg.prefix);
                this->db.replace_document(data_arg.docid, doc);
                return true;
              } else if constexpr (std::is_same_v<T, std::monostate>) {
                return true; // Bỏ qua monostate (có thể là log không hợp lệ)
              }
              return false; // Kiểu dữ liệu không xác định trong variant
            } catch (const Xapian::DocNotFoundError &) {
              // Xử lý khi document không tìm thấy trong lúc replay (có thể bỏ
              // qua)
              return true; // Theo logic gốc là bỏ qua lỗi này
            } catch (const Xapian::Error &) {
              return false; /* Lỗi Xapian khác */
            }
          },
          entry.data);
    }
    // Catch các lỗi có thể xảy ra khi truy cập variant hoặc lỗi Xapian chung
    catch (const std::bad_variant_access &) {
      return false;
    } catch (const Xapian::Error &) {
      return false;
    } catch (const std::exception &) {
      return false;
    } catch (...) {
      return false;
    }

    // Nếu một operation thất bại, dừng replay và trả về false
    if (!success_op) {
      return false;
    }
  }
  // Replay thành công tất cả các operation
  // Hàm này không tự commit, caller phải gọi commit_changes() nếu muốn lưu kết
  // quả replay
  return true;
}

// Khôi phục trạng thái về lần commit cuối cùng bằng cách xóa log staged và mở
// lại DB
bool XapianManager::revertUncommittedChanges() {
  std::lock_guard<std::mutex> lock(changes_mutex); // Khóa để thao tác an toàn
  
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

// Lấy một bản ghi log tổng hợp chứa tất cả các thay đổi đã staged
XapianLog::ComprehensiveLog XapianManager::getComprehensiveChangeLogs() const
{
    std::lock_guard<std::mutex> lock(changes_mutex);
    return comprehensive_log;
}

// Lấy một bản ghi log tổng hợp chứa tất cả các thay đổi đã staged
XapianLog::ComprehensiveLog XapianManager::removeLogsUntilNearestEndCommand()
{
    std::lock_guard<std::mutex> lock(changes_mutex);
    comprehensive_log.removeLogsUntilNearestEndCommand();
    return comprehensive_log;
}