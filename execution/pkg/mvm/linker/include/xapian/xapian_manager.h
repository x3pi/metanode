// linker/include/xapian/xapian_manager.h
#ifndef XAPIAN_MANAGER_H
#define XAPIAN_MANAGER_H

#include <xapian.h>
#include <iostream>
#include <vector>
#include <map>
#include <tbb/concurrent_hash_map.h>
#include <memory>
#include <unordered_map>
#include <shared_mutex>
#include <sstream>
#include <condition_variable>
#include <atomic>
#include <filesystem>
#include <chrono>     // Needed for time_point
#include <mutex>      // Needed for mutex
#include <string>     // Needed for std::string
#include <vector>     // Needed for std::vector
#include <functional> // Needed for std::hash
#include <mvm/address.h>
#include "xapian_log.h" // <-- Include file định nghĩa LogEntry
class XapianRegistry; // Forward declaration
struct DocumentInfo {
  std::string data;
  std::string author;
  std::string content;
};

class XapianManager {
public:
  mvm::Address address;

  // --- Static members and Singleton ---
  static std::unordered_map<std::string, std::shared_ptr<XapianManager>> instances;
  static std::shared_mutex instances_mutex;
  // static std::string DB_PATH;
  static std::shared_ptr<XapianManager> getInstance(const std::string &db_name,
                                                    const mvm::Address &addr,
                                                    bool isReset);
  static constexpr const char *LOGICAL_ID_GENERATED_PREFIX = "uuid:";
  static std::string generateUuidLogicalId();
  static void commitAllInstances();
  // --- Member Variables ---
  Xapian::WritableDatabase db;
  mutable std::shared_mutex changes_mutex; // shared_mutex: cho phép nhiều reader song song, exclusive khi write/commit

  // --- Search Database Pool ---
  // Mỗi goroutine search cần Xapian::Database riêng (không thread-safe).
  // Pool chứa sẵn MAX_CONCURRENT_SEARCHES Database objects, tái sử dụng thay vì create/destroy mỗi call.
  static constexpr int MAX_CONCURRENT_SEARCHES = 4;
  struct SearchDbPool {
    std::vector<Xapian::Database*> pool; // Pre-created Database objects
    std::vector<uint8_t> in_use;         // Non-zero nếu DB đang được dùng
    std::vector<uint64_t> last_gen;      // Generation mà slot này đã reopen() gần nhất
    std::mutex pool_mutex;
    std::condition_variable pool_cv;
  };
  SearchDbPool search_pool;
  // Tăng lên mỗi khi db.commit() thành công. acquireSearchDb() so sánh với
  // last_gen[i] của từng slot để biết slot đó có "cũ" hơn commit gần nhất hay
  // không, và reopen() ngay trước khi giao cho caller — bất kể lúc commit
  // slot đó đang bận hay rảnh. Nhờ vậy không còn cửa sổ race giữa
  // "commit xong nhưng slot đang bận nên bỏ qua reopen".
  std::atomic<uint64_t> db_generation{0};

  // Lấy 1 Xapian::Database từ pool (đợi nếu tất cả đang bận), tự reopen()
  // nếu slot đó đang cũ hơn generation hiện tại trước khi trả về.
  // Caller PHẢI gọi releaseSearchDb(db) sau khi xong.
  Xapian::Database* acquireSearchDb();
  void releaseSearchDb(Xapian::Database* db);

  mutable std::mutex db_mutex; // Mutex to protect all operations on db

  // Thêm thành viên để lưu khóa mvmId liên kết

  // --- Constructor ---
  XapianManager(const std::string &db_path, const mvm::Address &addr);

  // --- Document Operations (Log changes before execution) ---
  std::string new_document(const std::string &data, uint256_t blockNumber, const unsigned char *mvmId = nullptr, const uint256_t *txHash = nullptr);
  bool delete_document(const std::string& virtualDocId, uint256_t blockNumber, const unsigned char *mvmId = nullptr, const uint256_t *txHash = nullptr);
  std::string add_value(const std::string& virtualDocId, Xapian::valueno slot,
                          const std::string &value, bool isSerialise,
                          uint256_t blockNumber, const unsigned char *mvmId = nullptr, const uint256_t *txHash = nullptr);
  std::string add_term(const std::string& virtualDocId, const std::string &term,
                         uint256_t blockNumber, const unsigned char *mvmId = nullptr, const uint256_t *txHash = nullptr);
  std::string set_data(const std::string& virtualDocId, const std::string &data,
                         uint256_t blockNumber, const unsigned char *mvmId = nullptr, const uint256_t *txHash = nullptr);
  std::string index_text(const std::string& virtualDocId, const std::string &text_to_index,
                           Xapian::termcount wdf_inc, const std::string prefix,
                           uint256_t blockNumber, const unsigned char *mvmId = nullptr, const uint256_t *txHash = nullptr);
  Xapian::Document clone_document(const Xapian::Document &source_doc);

  // --- Read Operations ---
  std::string get_data(const std::string& virtualDocId, uint256_t blockNumber, const uint256_t *txHash = nullptr, const uint256_t *writerHash = nullptr);
  std::string get_value(const std::string& virtualDocId, Xapian::valueno slot, bool isSerialise, uint256_t blockNumber, const uint256_t *txHash = nullptr, const uint256_t *writerHash = nullptr);
  std::vector<std::string> get_terms(const std::string& virtualDocId, uint256_t blockNumber, const uint256_t *txHash = nullptr, const uint256_t *writerHash = nullptr);
  DocumentInfo get_document(const std::string& virtualDocId, uint256_t blockNumber, const uint256_t *txHash = nullptr, const uint256_t *writerHash = nullptr);
  Xapian::Document get_overlayed_document(const std::string& virtualDocIdStr, const uint256_t* txHash, const uint256_t* writerHash = nullptr, Xapian::Database* search_db = nullptr);

  // --- Commit and Change Tracking ---
  bool commit_changes();
  std::array<uint8_t, 32u> getChangeHash();
  std::vector<XapianLog::LogEntry> getChangeLogs(); // <-- Kiểu mới
  std::array<uint8_t, 32u> getCombinedTagsChangeHash();
  std::array<uint8_t, 32u> getComprehensiveStateHash();

  // --- Idle Management ---
  void touch();
  bool is_idle_for(std::chrono::minutes duration);
  static bool destroyInstance(const std::string &db_path); // Hàm hủy tức thì
  bool saveAllAndCommit();
  bool revertUncommittedChanges();
  bool mvmCommitTransaction();
  void mvmCancelTransaction();
  void dump_all_documents(uint256_t blockNumber);

  // (Optional) Cung cấp getter/setter an toàn luồng nếu cần

  // Chỉ nên được gọi bởi registry


  bool replay_log(const std::vector<XapianLog::LogEntry> &log_to_replay);
  void apply_buffered_tx(const std::string& txHashStr);
  void clear_buffer(const std::string& txHashStr);

  XapianLog::ComprehensiveLog extractComprehensiveChangeLogs();
  XapianLog::ComprehensiveLog removeLogsUntilNearestEndCommand();
  std::string getDbName() const; // <-- Thêm khai báo này
  bool has_started = false;
  friend class XapianRegistry; // Cho phép Registry truy cập changes_mutex

private:
  // --- Idle Tracking ---
  std::chrono::steady_clock::time_point last_access_time;
  std::mutex access_mutex;
  // --- Change Tracking ---
  // std::vector<XapianLog::LogEntry> staged_changes_log; // <-- Kiểu mới
  XapianLog::ComprehensiveLog comprehensive_log;

  std::string db_name; // <-- Biến lưu đường dẫn DB Xapian

  // Bản đồ lưu tạm các thao tác Xapian cho mỗi transaction (dựa trên txHash)
  std::unordered_map<std::string, XapianLog::ComprehensiveLog> tx_buffers;
  std::mutex tx_buffers_mutex;
  std::unordered_map<std::string, int> tx_counters; // Để sinh UUID tuần tự trong 1 giao dịch
  
  // Resolves a virtual docId (e.g. 256-bit UUID) to native Xapian uint32 docid
  Xapian::docid resolveVirtualDocId(const std::string& virtualDocIdStr, Xapian::Database* search_db = nullptr);
  
  // Áp dụng buffer vào một Xapian::Document ảo để mô phỏng (dùng khi get_data)
  void replayBufferToDocument(Xapian::Document& doc, const XapianLog::ComprehensiveLog& buffer);

  bool commitTransaction(unsigned char *txHashes, int numHashes);
};

#endif // XAPIAN_MANAGER_H