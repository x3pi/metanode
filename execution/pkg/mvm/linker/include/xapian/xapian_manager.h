// linker/include/xapian/xapian_manager.h
#ifndef XAPIAN_MANAGER_H
#define XAPIAN_MANAGER_H

#include <xapian.h>
#include <iostream>
#include <vector>
#include <map>
#include <memory>
#include <unordered_map>
#include <shared_mutex>
#include <sstream>
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
  // --- Member Variables ---
  Xapian::WritableDatabase db;
  mutable std::mutex db_mutex; // Mutex to protect all operations on db

  // Thêm thành viên để lưu khóa mvmId liên kết
  std::string associatedMvmIdKey;
  std::mutex mvmIdKeyMutex; // Mutex để bảo vệ associatedMvmIdKey nếu cần truy
                            // cập từ nhiều luồng

  // --- Constructor ---
  XapianManager(const std::string &db_path, const mvm::Address &addr);

  // --- Document Operations (Log changes before execution) ---
  Xapian::docid new_document(const std::string &data, uint256_t blockNumber);
  bool delete_document(Xapian::docid did, uint256_t blockNumber);
  Xapian::docid add_value(Xapian::docid did, Xapian::valueno slot,
                          const std::string &value, bool isSerialise,
                          uint256_t blockNumber);
  Xapian::docid add_term(Xapian::docid did, const std::string &term,
                         uint256_t blockNumber);
  Xapian::docid set_data(Xapian::docid did, const std::string &data,
                         uint256_t blockNumber);
  Xapian::docid index_text(Xapian::docid did, const std::string &text_to_index,
                           Xapian::termcount wdf_inc, const std::string prefix,
                           uint256_t blockNumber);
  Xapian::Document clone_document(const Xapian::Document &source_doc);

  // --- Read Operations ---
  std::string get_data(Xapian::docid did, uint256_t blockNumber);
  std::string get_value(Xapian::docid did, Xapian::valueno slot,
                        bool isSerialise, uint256_t blockNumber);
  std::vector<std::string> get_terms(Xapian::docid did, uint256_t blockNumber);
  DocumentInfo get_document(Xapian::docid did, uint256_t blockNumber);

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
  std::string getAssociatedMvmIdKey() {
    std::lock_guard<std::mutex> lock(mvmIdKeyMutex);
    return associatedMvmIdKey;
  }

  // Chỉ nên được gọi bởi registry
  void setAssociatedMvmIdKeyInternal(const std::string &key) {
    std::lock_guard<std::mutex> lock(mvmIdKeyMutex);
    associatedMvmIdKey = key;
  }

  void clearAssociatedMvmIdKeyInternal() {
    std::lock_guard<std::mutex> lock(mvmIdKeyMutex);
    associatedMvmIdKey = "";
  }

  bool replay_log(const std::vector<XapianLog::LogEntry> &log_to_replay);

  XapianLog::ComprehensiveLog getComprehensiveChangeLogs() const;
  XapianLog::ComprehensiveLog removeLogsUntilNearestEndCommand();
  std::string getDbName() const; // <-- Thêm khai báo này
  bool has_started = false;
  friend class XapianRegistry; // Cho phép Registry truy cập changes_mutex

private:
  // --- Idle Tracking ---
  std::chrono::steady_clock::time_point last_access_time;
  std::mutex access_mutex;
  mutable std::mutex changes_mutex;
  // --- Change Tracking ---
  // std::vector<XapianLog::LogEntry> staged_changes_log; // <-- Kiểu mới
  XapianLog::ComprehensiveLog comprehensive_log;

  std::string db_name; // <-- Biến lưu đường dẫn DB Xapian
};

#endif // XAPIAN_MANAGER_H