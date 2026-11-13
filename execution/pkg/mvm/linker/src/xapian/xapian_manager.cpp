#include <algorithm>
#include <atomic>
#include <charconv>
#include <cmath>
#include <cstdio>
#include <cstring>
#include "xapian/xapian_manager.h"
#include "my_extension/utils.h"
#include "xapian/xapian_log.h" // Giả định chứa định nghĩa XapianLog::LogEntry
#include "xapian/xapian_registry.h"
#include "mvm_linker.hpp" // GetXapianPruneRetentionBlocks / GetCurrentBlockNumberForXapianPrune

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

namespace {
constexpr const char *kMetaHeadBlock   = "__mtn_meta_head_block";
constexpr const char *kMetaUndoIndex   = "__mtn_meta_undo_index";
constexpr const char *kMetaNextDid     = "__mtn_meta_next_did";
constexpr const char *kMetaPrunedFloor = "__mtn_meta_pruned_floor_block";
constexpr const char *kMetaUndoPrefix  = "__mtn_undo_";
constexpr const char  kUndoMagic[4]    = {'X', 'U', 'J', '1'};
constexpr size_t      kMaxUndoNidLen   = 1024;
constexpr uint32_t    kMaxUndoEntries  = 16 * 1024 * 1024;

std::string encodeU64BE(uint64_t v) {
  std::string out(8, '\0');
  for (int i = 0; i < 8; ++i) {
    out[7 - i] = static_cast<char>((v >> (i * 8)) & 0xFF);
  }
  return out;
}

bool decodeU64BE(const std::string &s, uint64_t &out) {
  if (s.size() != 8) return false;
  uint64_t v = 0;
  for (int i = 0; i < 8; ++i) {
    v = (v << 8) | static_cast<uint8_t>(s[i]);
  }
  out = v;
  return true;
}

std::string encodeU32BE(uint32_t v) {
  std::string out(4, '\0');
  for (int i = 0; i < 4; ++i) {
    out[3 - i] = static_cast<char>((v >> (i * 8)) & 0xFF);
  }
  return out;
}

bool decodeU32BE(const std::string &s, uint32_t &out) {
  if (s.size() != 4) return false;
  uint32_t v = 0;
  for (int i = 0; i < 4; ++i) {
    v = (v << 8) | static_cast<uint8_t>(s[i]);
  }
  out = v;
  return true;
}

void appendU32BE(std::string &dst, uint32_t v) {
  char buf[4];
  for (int i = 0; i < 4; ++i) {
    buf[3 - i] = static_cast<char>((v >> (i * 8)) & 0xFF);
  }
  dst.append(buf, 4);
}

void appendU64BE(std::string &dst, uint64_t v) {
  char buf[8];
  for (int i = 0; i < 8; ++i) {
    buf[7 - i] = static_cast<char>((v >> (i * 8)) & 0xFF);
  }
  dst.append(buf, 8);
}

bool readU32BE(const std::string &src, size_t &off, uint32_t &out) {
  if (off + 4 > src.size()) return false;
  uint32_t v = 0;
  for (int i = 0; i < 4; ++i) {
    v = (v << 8) | static_cast<uint8_t>(src[off + i]);
  }
  off += 4;
  out = v;
  return true;
}

bool readU64BE(const std::string &src, size_t &off, uint64_t &out) {
  if (off + 8 > src.size()) return false;
  uint64_t v = 0;
  for (int i = 0; i < 8; ++i) {
    v = (v << 8) | static_cast<uint8_t>(src[off + i]);
  }
  off += 8;
  out = v;
  return true;
}

std::string encodeUndoIndex(const std::vector<uint64_t> &blocks) {
  std::string out;
  out.reserve(blocks.size() * 8);
  for (uint64_t bn : blocks) {
    appendU64BE(out, bn);
  }
  return out;
}

bool decodeUndoIndex(const std::string &raw, std::vector<uint64_t> &blocks) {
  blocks.clear();
  if (raw.empty()) return true;
  if (raw.size() % 8 != 0) return false;
  size_t count = raw.size() / 8;
  blocks.reserve(count);
  size_t off = 0;
  for (size_t i = 0; i < count; ++i) {
    uint64_t bn = 0;
    if (!readU64BE(raw, off, bn)) return false;
    blocks.push_back(bn);
  }
  return true;
}

struct SerializedUndoEntry {
  std::string nid;
  bool existed = false;
  Xapian::docid original_did = 0;
  std::string serialized_doc;
};

struct SerializedUndoRecord {
  uint64_t block_number = 0;
  Xapian::docid prev_next_did = 1;
  std::map<std::string, SerializedUndoEntry> entries;
};

std::string serializeUndoRecord(const SerializedUndoRecord &rec) {
  std::string out;
  out.append(kUndoMagic, 4);
  appendU64BE(out, rec.block_number);
  appendU32BE(out, static_cast<uint32_t>(rec.prev_next_did));
  appendU32BE(out, static_cast<uint32_t>(rec.entries.size()));
  for (const auto &kv : rec.entries) {
    const SerializedUndoEntry &e = kv.second;
    appendU32BE(out, static_cast<uint32_t>(e.nid.size()));
    out.append(e.nid);
    out.push_back(e.existed ? '\x01' : '\x00');
    appendU32BE(out, static_cast<uint32_t>(e.original_did));
    if (e.existed) {
      appendU32BE(out, static_cast<uint32_t>(e.serialized_doc.size()));
      out.append(e.serialized_doc);
    }
  }
  return out;
}

bool deserializeUndoRecord(const std::string &raw, SerializedUndoRecord &out) {
  out = SerializedUndoRecord{};
  if (raw.size() < 20) return false;
  if (std::memcmp(raw.data(), kUndoMagic, 4) != 0) return false;
  size_t off = 4;
  uint32_t prev_did = 0;
  uint32_t count = 0;
  if (!readU64BE(raw, off, out.block_number)) return false;
  if (!readU32BE(raw, off, prev_did)) return false;
  if (!readU32BE(raw, off, count)) return false;
  if (count > kMaxUndoEntries) return false;
  out.prev_next_did = static_cast<Xapian::docid>(prev_did);
  for (uint32_t i = 0; i < count; ++i) {
    uint32_t nid_len = 0;
    if (!readU32BE(raw, off, nid_len)) return false;
    if (nid_len == 0 || nid_len > kMaxUndoNidLen || off + nid_len + 5 > raw.size()) {
      return false;
    }
    SerializedUndoEntry e;
    e.nid.assign(raw.data() + off, nid_len);
    off += nid_len;
    uint8_t ex = static_cast<uint8_t>(raw[off++]);
    if (ex > 1) return false;
    e.existed = (ex == 1);
    uint32_t orig_did = 0;
    if (!readU32BE(raw, off, orig_did)) return false;
    e.original_did = static_cast<Xapian::docid>(orig_did);
    if (e.existed) {
      uint32_t doc_len = 0;
      if (!readU32BE(raw, off, doc_len)) return false;
      if (off + doc_len > raw.size()) return false;
      e.serialized_doc.assign(raw.data() + off, doc_len);
      off += doc_len;
    }
    out.entries.emplace(e.nid, std::move(e));
  }
  return off == raw.size();
}

Xapian::docid resolveDocIdInDb(const Xapian::Database &target_db, const std::string &virtualDocIdStr) {
  if (virtualDocIdStr.empty()) return 0;
  try {
    std::string clean_id = XapianManager::normalize_docid(virtualDocIdStr);
    if (clean_id.length() >= 2 && clean_id.substr(0, 2) == "0x") {
      clean_id = clean_id.substr(2);
    }
    if (clean_id.empty()) return 0;
    std::string term = "Q" + clean_id;
    Xapian::PostingIterator it = target_db.postlist_begin(term);
    if (it != target_db.postlist_end(term)) {
      return *it;
    }
    return 0;
  } catch (...) {
    return 0;
  }
}

bool endsWithStr(const std::string &s, const std::string &suffix) {
  return s.size() >= suffix.size() &&
         s.compare(s.size() - suffix.size(), suffix.size(), suffix) == 0;
}

template <typename ReadMetaFn, typename WriteMetaFn>
bool rollbackWritableDbAboveBlock(Xapian::WritableDatabase &target_db,
                                  uint64_t targetBlock,
                                  ReadMetaFn readMetaFn,
                                  WriteMetaFn writeMetaFn,
                                  Xapian::docid &inout_next_did,
                                  bool &did_mutate) {
  did_mutate = false;

  uint64_t head_block = 0;
  std::string head_raw = readMetaFn(kMetaHeadBlock);
  if (!head_raw.empty() && !decodeU64BE(head_raw, head_block)) {
    std::cerr << "[Xapian Undo FATAL] Corrupt __mtn_meta_head_block metadata" << std::endl;
    return false;
  }

  std::vector<uint64_t> undo_index;
  std::string index_raw = readMetaFn(kMetaUndoIndex);
  if (!decodeUndoIndex(index_raw, undo_index)) {
    std::cerr << "[Xapian Undo FATAL] Corrupt __mtn_meta_undo_index metadata" << std::endl;
    return false;
  }

  std::vector<uint64_t> to_undo;
  for (uint64_t bn : undo_index) {
    if (bn > targetBlock) {
      to_undo.push_back(bn);
    }
  }

  if (head_block <= targetBlock && to_undo.empty()) {
    return true;
  }

  uint64_t pruned_floor = 0;
  std::string floor_raw = readMetaFn(kMetaPrunedFloor);
  if (!floor_raw.empty() && !decodeU64BE(floor_raw, pruned_floor)) {
    std::cerr << "[Xapian Undo FATAL] Corrupt __mtn_meta_pruned_floor_block metadata" << std::endl;
    return false;
  }

  if (targetBlock < pruned_floor) {
    std::cerr << "[Xapian Undo FATAL] Cannot roll back to block " << targetBlock
              << ": undo history was pruned up to block " << pruned_floor << std::endl;
    return false;
  }

  if (head_block > targetBlock && to_undo.empty()) {
    std::cerr << "[Xapian Undo FATAL] Inconsistent metadata: head_block=" << head_block
              << " > targetBlock=" << targetBlock << " but undo_index has no entries" << std::endl;
    return false;
  }

  std::sort(to_undo.begin(), to_undo.end(), std::greater<uint64_t>());

  for (uint64_t bn : to_undo) {
    std::string ukey = std::string(kMetaUndoPrefix) + std::to_string(bn);
    std::string uraw = readMetaFn(ukey);
    SerializedUndoRecord rec;
    if (uraw.empty() || !deserializeUndoRecord(uraw, rec) || rec.block_number != bn) {
      std::cerr << "[Xapian Undo FATAL] Missing or corrupt undo journal entry for block " << bn << std::endl;
      return false;
    }

    for (const auto &kv : rec.entries) {
      const SerializedUndoEntry &ue = kv.second;
      Xapian::docid cur_did = resolveDocIdInDb(target_db, ue.nid);
      if (ue.existed) {
        Xapian::Document restored = Xapian::Document::unserialise(ue.serialized_doc);
        Xapian::docid restore_did = (ue.original_did > 0) ? ue.original_did : cur_did;
        if (cur_did > 0 && cur_did != restore_did) {
          try {
            target_db.delete_document(cur_did);
          } catch (const Xapian::DocNotFoundError &) {}
        }
        if (restore_did > 0) {
          target_db.replace_document(restore_did, restored);
        } else {
          target_db.add_document(restored);
        }
      } else {
        if (cur_did > 0) {
          try {
            target_db.delete_document(cur_did);
          } catch (const Xapian::DocNotFoundError &) {}
        }
        if (ue.original_did > 0 && ue.original_did != cur_did) {
          try {
            target_db.delete_document(ue.original_did);
          } catch (const Xapian::DocNotFoundError &) {}
        }
      }
    }

    inout_next_did = (rec.prev_next_did > 0) ? rec.prev_next_did : 1;
    writeMetaFn(ukey, "");
  }

  std::vector<uint64_t> remaining_index;
  remaining_index.reserve(undo_index.size());
  for (uint64_t bn : undo_index) {
    if (bn <= targetBlock) {
      remaining_index.push_back(bn);
    }
  }
  std::sort(remaining_index.begin(), remaining_index.end());

  uint64_t new_head = remaining_index.empty() ? 0 : remaining_index.back();
  if (new_head == 0 && pruned_floor > 0) {
    new_head = std::min(head_block, targetBlock);
  }

  writeMetaFn(kMetaNextDid, encodeU32BE(static_cast<uint32_t>(inout_next_did)));
  writeMetaFn(kMetaUndoIndex, encodeUndoIndex(remaining_index));
  writeMetaFn(kMetaHeadBlock, new_head > 0 ? encodeU64BE(new_head) : "");
  target_db.commit();
  did_mutate = true;
  return true;
}
} // namespace

// Tạo một bản sao đầy đủ (deep clone: data + mọi slot 32-bit + terms + wdf + term positions)
Xapian::Document
XapianManager::clone_document(const Xapian::Document &source_doc) {
  return Xapian::Document::unserialise(source_doc.serialise());
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

    // --- BẮT ĐẦU HANDLE EVICTION ---
    constexpr size_t MAX_ACTIVE_DBS = 50;
    std::vector<std::pair<std::chrono::steady_clock::time_point, std::shared_ptr<XapianManager>>> candidates;
    for (const auto& pair : instances) {
        // FORK-SAFETY / DATA-LOSS FIX: is_evictable() must gate this -- an instance can have
        // use_count()==1 (no active caller holding it right now) while still carrying
        // uncommitted Xapian writes staged between two commit points (tx_buffers /
        // comprehensive_log / has_uncommitted_writes). Closing its db handle in that state
        // (evict_handles() -> db.close()) discards those uncommitted writes -- Xapian does not
        // persist a WritableDatabase's pending changes across close(), and ensure_db_open()'s
        // later reopen only reads back the last COMMITTED state from disk, with no log replay.
        if (pair.second && pair.second.use_count() == 1 && pair.second->is_db_open
            && pair.second->is_evictable()) {
            candidates.push_back({pair.second->last_access_time, pair.second});
        }
    }
    
    if (candidates.size() >= MAX_ACTIVE_DBS) {
        std::sort(candidates.begin(), candidates.end(), 
            [](const auto& a, const auto& b) { return a.first < b.first; });
        size_t to_evict = (candidates.size() - MAX_ACTIVE_DBS) + 1;
        for (size_t i = 0; i < to_evict; ++i) {
            candidates[i].second->evict_handles();
        }
    }
    // --- KẾT THÚC HANDLE EVICTION ---

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
        throw std::runtime_error("Unknown error creating XapianManager instance for " + db_path_str);
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
  // createFullPath() nests 2 levels below g_xapian_base_path
  // (<base>/<address>/<hashed_dbname>) -- Xapian's glass backend can create
  // the leaf directory itself on DB_CREATE_OR_OPEN, but NOT missing
  // intermediate parents (e.g. a brand-new contract address that has never
  // had an XAPIAN_GET_OR_CREATE_DB call). Only that one opcode handler
  // pre-creates the path today; every other XAPIAN_* opcode (NEW_DOCUMENT,
  // SET_DATA_DOCUMENT, ...) calls getInstance() directly and can be the
  // FIRST call for a given (address, db_name) pair -- confirmed live via a
  // 16-thread/200-distinct-DB concurrent stress test that reliably threw
  // Xapian::DatabaseCreateError here, uncaught past every "if (!manager)"
  // guard in xapian_handlers.cpp since getInstance() re-throws rather than
  // returning nullptr -- an uncontrolled std::terminate() crashing the
  // whole node process instead of a graceful per-tx failure. Ensuring the
  // directory here, once, covers every opcode/call site instead of relying
  // on each one to remember its own create_directories() pre-step.
  std::filesystem::path db_path = mvm::createFullPath(addr, db_name);
  std::error_code ec;
  std::filesystem::create_directories(db_path, ec);
  return Xapian::WritableDatabase(db_path.string(), Xapian::DB_CREATE_OR_OPEN);
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

    reloadNextDidLocked();
}

std::string XapianManager::readMetaLocked(const std::string &key) const {
    if (mvm::IsXapianBasePathEmpty()) {
        auto it = in_memory_meta_.find(key);
        return it != in_memory_meta_.end() ? it->second : std::string();
    }
    return db.get_metadata(key);
}

void XapianManager::writeMetaLocked(const std::string &key, const std::string &val) {
    if (mvm::IsXapianBasePathEmpty()) {
        if (val.empty()) {
            in_memory_meta_.erase(key);
        } else {
            in_memory_meta_[key] = val;
        }
        return;
    }
    db.set_metadata(key, val);
}

void XapianManager::reloadNextDidLocked() {
    try {
        std::string raw = readMetaLocked(kMetaNextDid);
        uint32_t stored = 0;
        if (!raw.empty() && decodeU32BE(raw, stored) && stored >= 1) {
            next_did_ = static_cast<Xapian::docid>(stored);
            return;
        }
        next_did_ = db.get_lastdocid() + 1;
        if (next_did_ == 0) next_did_ = 1;
    } catch (...) {
        next_did_ = 1;
    }
}

void XapianManager::clearAllTxBuffers() {
    std::lock_guard<std::mutex> buffer_lock(tx_buffers_mutex);
    tx_buffers.clear();
    tx_counters.clear();
    doc_writer_predecessors.clear();
    tx_to_predecessor_keys.clear();
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
            search_pool.in_use[i] = true;
            Xapian::Database* slot_db = search_pool.pool[i];
            uint64_t slot_gen = search_pool.last_gen[i];
            lock.unlock(); // Release lock before disk I/O
            if (slot_db == nullptr) {
                // Lazy init
                try {
                    slot_db = new Xapian::Database(
                        mvm::createFullPath(address, db_name).string());
                } catch (const Xapian::Error& e) {
                    std::cerr << "[FATAL] acquireSearchDb: failed to init slot " << i
                              << ": " << e.get_description() << std::endl;
                    lock.lock();
                    search_pool.in_use[i] = false;
                    search_pool.pool_cv.notify_one();
                    return nullptr;
                } catch (const std::exception& e) {
                    std::cerr << "[FATAL] acquireSearchDb: failed to init slot " << i
                              << ": " << e.what() << std::endl;
                    lock.lock();
                    search_pool.in_use[i] = false;
                    search_pool.pool_cv.notify_one();
                    return nullptr;
                }
                lock.lock();
                search_pool.pool[i] = slot_db;
                search_pool.last_gen[i] = current_gen;
                return slot_db;
            } else if (slot_gen < current_gen) {
                // Reopen existing
                try {
                    slot_db->reopen();
                } catch (const Xapian::Error& e) {
                    std::cerr << "[ERROR] acquireSearchDb: reopen() failed for slot " << i
                              << ": " << e.get_description() << ", recreating..." << std::endl;
                    delete slot_db;
                    slot_db = nullptr;
                    try {
                        slot_db = new Xapian::Database(
                            mvm::createFullPath(address, db_name).string());
                    } catch (const Xapian::Error& e2) {
                        std::cerr << "[FATAL] acquireSearchDb: failed to recreate slot " << i
                                  << ": " << e2.get_description() << std::endl;
                        lock.lock();
                        search_pool.pool[i] = nullptr;
                        search_pool.last_gen[i] = 0;
                        search_pool.in_use[i] = false;
                        search_pool.pool_cv.notify_one();
                        return nullptr;
                    } catch (const std::exception& e2) {
                        std::cerr << "[FATAL] acquireSearchDb: failed to recreate slot " << i
                                  << ": " << e2.what() << std::endl;
                        lock.lock();
                        search_pool.pool[i] = nullptr;
                        search_pool.last_gen[i] = 0;
                        search_pool.in_use[i] = false;
                        search_pool.pool_cv.notify_one();
                        return nullptr;
                    }
                } catch (const std::exception& e) {
                    std::cerr << "[ERROR] acquireSearchDb: reopen() failed for slot " << i
                              << ": " << e.what() << ", recreating..." << std::endl;
                    delete slot_db;
                    slot_db = nullptr;
                    try {
                        slot_db = new Xapian::Database(
                            mvm::createFullPath(address, db_name).string());
                    } catch (const Xapian::Error& e2) {
                        std::cerr << "[FATAL] acquireSearchDb: failed to recreate slot " << i
                                  << ": " << e2.get_description() << std::endl;
                        lock.lock();
                        search_pool.pool[i] = nullptr;
                        search_pool.last_gen[i] = 0;
                        search_pool.in_use[i] = false;
                        search_pool.pool_cv.notify_one();
                        return nullptr;
                    } catch (const std::exception& e2) {
                        std::cerr << "[FATAL] acquireSearchDb: failed to recreate slot " << i
                                  << ": " << e2.what() << std::endl;
                        lock.lock();
                        search_pool.pool[i] = nullptr;
                        search_pool.last_gen[i] = 0;
                        search_pool.in_use[i] = false;
                        search_pool.pool_cv.notify_one();
                        return nullptr;
                    }
                }
                lock.lock();
                search_pool.pool[i] = slot_db;
                search_pool.last_gen[i] = current_gen;
                return slot_db;
            }
            return slot_db;
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

    auto start_wait = std::chrono::steady_clock::now();
    std::unique_lock<std::mutex> lock(simple_read_pool.pool_mutex);
    bool acquired = simple_read_pool.pool_cv.wait_for(lock, std::chrono::seconds(5), [this]() {
        for (size_t i = 0; i < simple_read_pool.in_use.size(); ++i)
            if (!simple_read_pool.in_use[i])
                return true;
        return false;
    });
    auto wait_ms = std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - start_wait).count();
    if (wait_ms > 10) {
        std::cerr << "⏱️ [XAPIAN-PERF-WARN] acquireSimpleReadDb pool wait took " << wait_ms << "ms" << std::endl;
    }

    if (!acquired) {
        std::cerr << "[ERROR] acquireSimpleReadDb: Timeout (5s) waiting for available DB." << std::endl;
        return nullptr;
    }

    uint64_t current_gen = db_generation.load(std::memory_order_acquire);
    for (size_t i = 0; i < simple_read_pool.pool.size(); ++i) {
        if (!simple_read_pool.in_use[i]) {
            simple_read_pool.in_use[i] = true;
            Xapian::Database* slot_db = simple_read_pool.pool[i];
            uint64_t slot_gen = simple_read_pool.last_gen[i];
            lock.unlock(); // Release lock before disk I/O
            if (slot_db == nullptr) {
                // Lazy init
                try {
                    slot_db = new Xapian::Database(
                        mvm::createFullPath(address, db_name).string());
                } catch (const Xapian::Error& e) {
                    std::cerr << "[FATAL] acquireSimpleReadDb: failed to init slot " << i
                              << ": " << e.get_description() << std::endl;
                    lock.lock();
                    simple_read_pool.in_use[i] = false;
                    simple_read_pool.pool_cv.notify_one();
                    return nullptr;
                } catch (const std::exception& e) {
                    std::cerr << "[FATAL] acquireSimpleReadDb: failed to init slot " << i
                              << ": " << e.what() << std::endl;
                    lock.lock();
                    simple_read_pool.in_use[i] = false;
                    simple_read_pool.pool_cv.notify_one();
                    return nullptr;
                }
                lock.lock();
                simple_read_pool.pool[i] = slot_db;
                simple_read_pool.last_gen[i] = current_gen;
                return slot_db;
            } else if (slot_gen < current_gen) {
                // Reopen existing
                try {
                    slot_db->reopen();
                } catch (const Xapian::Error& e) {
                    std::cerr << "[ERROR] acquireSimpleReadDb: reopen() failed for slot " << i
                              << ": " << e.get_description() << ", recreating..." << std::endl;
                    delete slot_db;
                    slot_db = nullptr;
                    try {
                        slot_db = new Xapian::Database(
                            mvm::createFullPath(address, db_name).string());
                    } catch (const Xapian::Error& e2) {
                        std::cerr << "[FATAL] acquireSimpleReadDb: failed to recreate slot " << i
                                  << ": " << e2.get_description() << std::endl;
                        lock.lock();
                        simple_read_pool.pool[i] = nullptr;
                        simple_read_pool.last_gen[i] = 0;
                        simple_read_pool.in_use[i] = false;
                        simple_read_pool.pool_cv.notify_one();
                        return nullptr;
                    } catch (const std::exception& e2) {
                        std::cerr << "[FATAL] acquireSimpleReadDb: failed to recreate slot " << i
                                  << ": " << e2.what() << std::endl;
                        lock.lock();
                        simple_read_pool.pool[i] = nullptr;
                        simple_read_pool.last_gen[i] = 0;
                        simple_read_pool.in_use[i] = false;
                        simple_read_pool.pool_cv.notify_one();
                        return nullptr;
                    }
                } catch (const std::exception& e) {
                    std::cerr << "[ERROR] acquireSimpleReadDb: reopen() failed for slot " << i
                              << ": " << e.what() << ", recreating..." << std::endl;
                    delete slot_db;
                    slot_db = nullptr;
                    try {
                        slot_db = new Xapian::Database(
                            mvm::createFullPath(address, db_name).string());
                    } catch (const Xapian::Error& e2) {
                        std::cerr << "[FATAL] acquireSimpleReadDb: failed to recreate slot " << i
                                  << ": " << e2.get_description() << std::endl;
                        lock.lock();
                        simple_read_pool.pool[i] = nullptr;
                        simple_read_pool.last_gen[i] = 0;
                        simple_read_pool.in_use[i] = false;
                        simple_read_pool.pool_cv.notify_one();
                        return nullptr;
                    } catch (const std::exception& e2) {
                        std::cerr << "[FATAL] acquireSimpleReadDb: failed to recreate slot " << i
                                  << ": " << e2.what() << std::endl;
                        lock.lock();
                        simple_read_pool.pool[i] = nullptr;
                        simple_read_pool.last_gen[i] = 0;
                        simple_read_pool.in_use[i] = false;
                        simple_read_pool.pool_cv.notify_one();
                        return nullptr;
                    }
                }
                lock.lock();
                simple_read_pool.pool[i] = slot_db;
                simple_read_pool.last_gen[i] = current_gen;
                return slot_db;
            }
            return slot_db;
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

bool XapianManager::is_evictable() const {
  if (has_uncommitted_writes.load(std::memory_order_acquire)) {
    return false;
  }
  std::shared_lock<ChangesMutex> ch_lock(changes_mutex);
  std::lock_guard<std::mutex> lock(const_cast<std::mutex&>(tx_buffers_mutex));
  return tx_buffers.empty() && comprehensive_log.xapian_doc_logs.empty() &&
         !has_uncommitted_writes.load(std::memory_order_acquire);
}

// Dump tất cả tài liệu trong database với đầy đủ thông tin để debug
void XapianManager::dump_all_documents(uint256_t blockNumber) {
  touch();
  try {
    ensure_db_open();
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
    std::cerr << "Xapian error dumping database: " << e.get_msg() << std::endl;
  } catch (const std::exception &e) {
    std::cerr << "Error dumping database: " << e.what() << std::endl;
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
    if (slot > 0xFFFFFFFD || slot == 253 || slot == 254) {
        std::cerr << "[ERROR] XapianManager::add_value: Slot " << slot << " is out of range or reserved." << std::endl;
        return "";
    }
    std::string value_to_add = value;
    if (isSerialise && !value.empty()) {
        double num = 0.0;
        const char *first = value.data();
        const char *last = first + value.size();
        auto [ptr, ec] = std::from_chars(first, last, num, std::chars_format::general);
        if (ec == std::errc::result_out_of_range) {
            std::cerr << "[ERROR] XapianManager::add_value: Number out of range: " << value << std::endl;
            return "";
        }
        if (ec != std::errc() || ptr != last || !std::isfinite(num)) {
            std::cerr << "[ERROR] XapianManager::add_value: Invalid number: " << value << std::endl;
            return "";
        }
        value_to_add = Xapian::sortable_serialise(num);
    }
    if (txHash != nullptr) {
        std::string txHashStr = mvm::to_hex_string_fixed(*txHash, 64);
        XapianLog::AddValueData logData{};
        logData.docid = virtualDocId;
        logData.slot = slot;
        logData.value = value_to_add;
        logData.is_serialised = isSerialise;
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
    if (isReservedSystemDocIdTerm(term)) {
        std::cerr << "[ERROR] XapianManager::add_term: Term collides with reserved system document identifier: " << term << std::endl;
        return "";
    }
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
    if (prefix.length() >= 64) {
        std::cerr << "[ERROR] XapianManager::index_text: Prefix length exceeds maximum: " << prefix.length() << std::endl;
        return "";
    }
    if (isReservedSystemPrefix(prefix)) {
        std::cerr << "[ERROR] XapianManager::index_text: Prefix collides with reserved system document identifier: " << prefix << std::endl;
        return "";
    }
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
std::string XapianManager::normalize_docid(const std::string& id) {
    if (id.empty()) return id;
    std::string s = id;
    if (s.length() >= 2 && (s[0] == '0' && (s[1] == 'x' || s[1] == 'X'))) {
        s = s.substr(2);
    }
    for (char &c : s) {
        c = static_cast<char>(std::tolower(static_cast<unsigned char>(c)));
    }
    return "0x" + s;
}

void XapianManager::record_doc_writer_predecessor(const std::string& virtualDocId, const uint256_t& currentTxHash, const uint256_t& prevWriterHash) {
    std::string curTxStr = mvm::to_hex_string_fixed(currentTxHash, 64);
    std::string prevTxStr = mvm::to_hex_string_fixed(prevWriterHash, 64);
    if (curTxStr == prevTxStr || prevTxStr.empty() || prevWriterHash == 0) return;

    std::string nid = normalize_docid(virtualDocId);
    std::string key = nid + "_" + curTxStr;

    std::lock_guard<std::mutex> lock(tx_buffers_mutex);
    if (doc_writer_predecessors.find(key) == doc_writer_predecessors.end()) {
        tx_to_predecessor_keys[curTxStr].push_back(key);
    }
    doc_writer_predecessors[key] = prevTxStr;
}

void XapianManager::clear_writer_predecessors_for_tx(const std::string& txHashStr) {
    // Note: tx_buffers_mutex must be held by caller (e.g. registry) or taken here
    auto it = tx_to_predecessor_keys.find(txHashStr);
    if (it != tx_to_predecessor_keys.end()) {
        for (const auto& k : it->second) {
            doc_writer_predecessors.erase(k);
        }
        tx_to_predecessor_keys.erase(it);
    }
}

Xapian::Document XapianManager::get_overlayed_document(const std::string& virtualDocId, const uint256_t *txHash, const uint256_t *writerHash, Xapian::Database* search_db) {
    Xapian::Document doc;
    bool found = false;
    bool is_deleted = false;
    std::string nid = normalize_docid(virtualDocId);
    
    try {
        ensure_db_open();
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

    // 1. Trace the writer predecessor chain in chronological order
    std::vector<std::string> writer_chain;
    std::unordered_set<std::string> visited;

    if (writerHash != nullptr) {
        std::string current_writer = mvm::to_hex_string_fixed(*writerHash, 64);
        while (!current_writer.empty() && visited.find(current_writer) == visited.end()) {
            visited.insert(current_writer);
            writer_chain.push_back(current_writer);
            auto it = doc_writer_predecessors.find(nid + "_" + current_writer);
            if (it != doc_writer_predecessors.end()) {
                current_writer = it->second;
            } else {
                break;
            }
        }
        std::reverse(writer_chain.begin(), writer_chain.end());
    }

    // 2. Append current transaction if it also wrote to buffer and not already in chain
    if (txHash != nullptr) {
        std::string currentTxStr = mvm::to_hex_string_fixed(*txHash, 64);
        if (visited.find(currentTxStr) == visited.end()) {
            writer_chain.push_back(currentTxStr);
        }
    }

    // 3. Replay all buffered operations from the writer chain in chronological order
    for (const auto& txHashStr : writer_chain) {
        auto it = tx_buffers.find(txHashStr);
        if (it == tx_buffers.end()) continue;

        const auto& entries = it->second.xapian_doc_logs;
        for (size_t i = 0; i < entries.size(); ++i) {
            const auto& entry = entries[i];
            if (entry.op == XapianLog::Operation::NEW_DOC) {
                auto data = std::get<XapianLog::NewDocData>(entry.data);
                if (normalize_docid(data.docid) == nid) {
                    found = true;
                    is_deleted = false;
                    doc = Xapian::Document();
                    doc.set_data(data.data);
                }
            } else if (entry.op == XapianLog::Operation::DEL_DOC) {
                auto data = std::get<XapianLog::DelDocData>(entry.data);
                if (normalize_docid(data.docid) == nid) {
                    found = false;
                    is_deleted = true;
                    doc = Xapian::Document();
                }
            } else if (entry.op == XapianLog::Operation::SET_DATA) {
                auto data = std::get<XapianLog::SetDataData>(entry.data);
                if (normalize_docid(data.docid) == nid) {
                    found = true;
                    is_deleted = false;
                    doc.set_data(data.data);
                }
            } else if (entry.op == XapianLog::Operation::ADD_VALUE) {
                auto data = std::get<XapianLog::AddValueData>(entry.data);
                if (normalize_docid(data.docid) == nid) {
                    found = true;
                    is_deleted = false;
                    doc.add_value(data.slot, data.value);
                }
            } else if (entry.op == XapianLog::Operation::ADD_TERM) {
                auto data = std::get<XapianLog::AddTermData>(entry.data);
                if (normalize_docid(data.docid) == nid) {
                    found = true;
                    is_deleted = false;
                    doc.add_term(data.term);
                }
            } else if (entry.op == XapianLog::Operation::INDEX_TEXT) {
                auto data = std::get<XapianLog::IndexTextData>(entry.data);
                if (normalize_docid(data.docid) == nid) {
                    found = true;
                    is_deleted = false;
                    Xapian::TermGenerator termgenerator;
                    termgenerator.set_stemmer(Xapian::Stem("english"));
                    termgenerator.set_stemming_strategy(Xapian::TermGenerator::STEM_SOME);
                    termgenerator.set_max_word_length(180);
                    termgenerator.set_document(doc);
                    termgenerator.index_text(data.text, data.wdf_inc, data.prefix);

                    // Sanitize generated terms: prevent term collision with internal docid of another document
                    std::string own_clean_id = nid;
                    if (own_clean_id.length() >= 2 && own_clean_id.substr(0, 2) == "0x") {
                        own_clean_id = own_clean_id.substr(2);
                    }
                    std::string own_system_term = "Q" + own_clean_id;
                    std::vector<std::string> colliding_terms;
                    for (auto it = doc.termlist_begin(); it != doc.termlist_end(); ++it) {
                        std::string t = *it;
                        if (isReservedSystemDocIdTerm(t) && t != own_system_term) {
                            colliding_terms.push_back(t);
                        }
                    }
                    for (const auto& t : colliding_terms) {
                        std::cerr << "[ERROR] XapianManager: removing colliding reserved system docid term: " << t << " from doc " << nid << std::endl;
                        doc.remove_term(t);
                    }
                }
            }
        }
    }

    if (is_deleted || !found) {
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
  auto start = std::chrono::steady_clock::now();
  std::shared_lock<ChangesMutex> read_lock(changes_mutex);
  ScopedSimpleReadDb scopedDb(this);
  if (!scopedDb.get()) throw std::runtime_error("Failed to acquire DB slot for get_data");
  try {
    Xapian::Document doc = get_overlayed_document(virtualDocId, txHash, writerHash, scopedDb.get());
    auto dur_ms = std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - start).count();
    if (dur_ms > 20) {
        std::cerr << "⏱️ [XAPIAN-PERF-WARN] get_data took " << dur_ms << "ms (doc=" << virtualDocId << ")" << std::endl;
    }
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
    if (slot > 0xFFFFFFFD) {
        std::cerr << "[ERROR] XapianManager::get_value: Slot " << slot << " is out of range." << std::endl;
        return "";
    }
    std::shared_lock<ChangesMutex> read_lock(changes_mutex);
    ScopedSimpleReadDb scopedDb(this);
    if (!scopedDb.get()) throw std::runtime_error("Failed to acquire DB slot for get_value");
    try {
        Xapian::Document doc = get_overlayed_document(virtualDocId, txHash, writerHash, scopedDb.get());
        std::string data = doc.get_value(slot);
        if (isSerialise && !data.empty()) {
            try {
                double value = Xapian::sortable_unserialise(data);
                if (!std::isfinite(value)) {
                    return "";
                }
                if (value == 0.0) {
                    return "0";
                }
                char buf[1152];
                auto [ptr, ec] = std::to_chars(buf, buf + sizeof(buf), value, std::chars_format::fixed);
                if (ec == std::errc()) {
                    return std::string(buf, ptr);
                }
                return "";
            } catch (const Xapian::Error &) {
                return "";
            }
        }
        return data;
    } catch (const Xapian::DocNotFoundError&) {
        // Normal, ignore
    } catch (...) {
        throw;
    }
    return "";
}

// Lấy thông tin (data, value slot 1, value slot 2) của document tại một block number
DocumentInfo XapianManager::get_document(const std::string& virtualDocId, uint256_t blockNumber, const uint256_t *txHash, const uint256_t *writerHash) {
  std::shared_lock<ChangesMutex> read_lock(changes_mutex);
  ScopedSimpleReadDb scopedDb(this);
  DocumentInfo info;
  if (!scopedDb.get()) throw std::runtime_error("Failed to acquire DB slot for get_document");
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
  std::shared_lock<ChangesMutex> read_lock(changes_mutex);
  ScopedSimpleReadDb scopedDb(this);
  std::vector<std::string> terms;
  if (!scopedDb.get()) throw std::runtime_error("Failed to acquire DB slot for get_terms");
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
bool XapianManager::commit_changes(uint64_t blockNumber) {
  touch(); // Cập nhật thời gian truy cập
  std::lock_guard<ChangesMutex> lock_changes(changes_mutex);
  bool has_writes = has_uncommitted_writes.load(std::memory_order_acquire);
  if (!has_writes && comprehensive_log.xapian_doc_logs.empty()) {
    return true; // Không có thay đổi nào cần commit -> Thoát ngay lập tức (zero-cost)
  }
  // SELF-DEADLOCK FIX: ensure_db_open() takes db_mutex itself (unconditionally, even
  // when no reopen is actually needed) -- it MUST run here, before we take our own
  // db_mutex below, not after. Calling it while already holding db_mutex (the previous
  // code had both in the same scope) is a non-recursive std::mutex re-lock by the same
  // thread, which blocks forever. Confirmed live via gdb: this deadlocked on the very
  // first real commit_changes() call with pending writes (the has_writes early-return
  // above is the only path that ever skipped it before). Safe to call here, still
  // outside our own db_mutex: this instance can't be concurrently evicted while we
  // already hold changes_mutex (evict_handles() takes changes_mutex first too).
  ensure_db_open();
  std::lock_guard<std::mutex> lock(db_mutex);
  try {
    uint64_t prev_head = 0;
    std::string head_raw = readMetaLocked(kMetaHeadBlock);
    if (!head_raw.empty() && !decodeU64BE(head_raw, prev_head)) {
      std::cerr << "[Xapian Undo FATAL] Corrupt __mtn_meta_head_block metadata in commit_changes" << std::endl;
      return false;
    }

    if (blockNumber == 0 && prev_head > 0) {
      std::cerr << "[Xapian Undo FATAL] Refusing unjournaled commit_changes(0) on block-tracked DB '"
                << db_name << "' (head_block=" << prev_head << ")" << std::endl;
      return false;
    }

    if (blockNumber > 0) {
      std::vector<uint64_t> undo_index;
      std::string index_raw = readMetaLocked(kMetaUndoIndex);
      if (!decodeUndoIndex(index_raw, undo_index)) {
        std::cerr << "[Xapian Undo FATAL] Corrupt __mtn_meta_undo_index metadata in commit_changes" << std::endl;
        return false;
      }

      uint64_t pruned_floor = 0;
      std::string floor_raw = readMetaLocked(kMetaPrunedFloor);
      if (!floor_raw.empty() && !decodeU64BE(floor_raw, pruned_floor)) {
        std::cerr << "[Xapian Undo FATAL] Corrupt __mtn_meta_pruned_floor_block metadata in commit_changes" << std::endl;
        return false;
      }

      writeMetaLocked(kMetaNextDid, encodeU32BE(static_cast<uint32_t>(next_did_)));

      std::string ukey = std::string(kMetaUndoPrefix) + std::to_string(blockNumber);
      std::string existing_uraw = readMetaLocked(ukey);
      SerializedUndoRecord rec;
      if (!existing_uraw.empty() && deserializeUndoRecord(existing_uraw, rec) &&
          rec.block_number == blockNumber) {
        // Merge multiple commits within the same blockNumber: keep the earliest
        // pre-image (and earliest prev_next_did) observed at the start of this block.
        for (const auto &kv : undo_snapshot_) {
          if (rec.entries.find(kv.first) == rec.entries.end()) {
            SerializedUndoEntry se;
            se.nid = kv.first;
            se.existed = kv.second.existed;
            se.original_did = kv.second.original_did;
            if (se.existed) {
              se.serialized_doc = kv.second.serialized_doc;
            }
            rec.entries.emplace(se.nid, std::move(se));
          }
        }
        writeMetaLocked(ukey, serializeUndoRecord(rec));
      } else {
        // Fast single-allocation serialization directly from undo_snapshot_ (common hot path)
        Xapian::docid prev_did_val = has_uncommitted_prev_next_did_ ? uncommitted_prev_next_did_ : next_did_;
        size_t total_bytes = 20;
        for (const auto &kv : undo_snapshot_) {
          total_bytes += 9 + kv.first.size() + (kv.second.existed ? 4 + kv.second.serialized_doc.size() : 0);
        }
        std::string fast_uraw;
        fast_uraw.reserve(total_bytes);
        fast_uraw.append(kUndoMagic, 4);
        appendU64BE(fast_uraw, blockNumber);
        appendU32BE(fast_uraw, static_cast<uint32_t>(prev_did_val));
        appendU32BE(fast_uraw, static_cast<uint32_t>(undo_snapshot_.size()));
        for (const auto &kv : undo_snapshot_) {
          appendU32BE(fast_uraw, static_cast<uint32_t>(kv.first.size()));
          fast_uraw.append(kv.first);
          fast_uraw.push_back(kv.second.existed ? '\x01' : '\x00');
          appendU32BE(fast_uraw, static_cast<uint32_t>(kv.second.original_did));
          if (kv.second.existed) {
            appendU32BE(fast_uraw, static_cast<uint32_t>(kv.second.serialized_doc.size()));
            fast_uraw.append(kv.second.serialized_doc);
          }
        }
        writeMetaLocked(ukey, fast_uraw);
      }

      if (std::find(undo_index.begin(), undo_index.end(), blockNumber) == undo_index.end()) {
        undo_index.push_back(blockNumber);
        std::sort(undo_index.begin(), undo_index.end());
      }

      while (undo_index.size() > MAX_UNDO_BLOCKS) {
        uint64_t oldest_bn = undo_index.front();
        undo_index.erase(undo_index.begin());
        writeMetaLocked(std::string(kMetaUndoPrefix) + std::to_string(oldest_bn), "");
        if (oldest_bn > pruned_floor) {
          pruned_floor = oldest_bn;
        }
      }
      if (pruned_floor > 0) {
        writeMetaLocked(kMetaPrunedFloor, encodeU64BE(pruned_floor));
      }
      writeMetaLocked(kMetaUndoIndex, encodeUndoIndex(undo_index));

      uint64_t new_head = std::max(prev_head, blockNumber);
      writeMetaLocked(kMetaHeadBlock, encodeU64BE(new_head));
    } else {
      writeMetaLocked(kMetaNextDid, encodeU32BE(static_cast<uint32_t>(next_did_)));
    }

    db.commit(); // Thực hiện commit Xapian nguyên tử (cả documents + undo journal metadata)
    comprehensive_log.xapian_doc_logs.clear(); // Xóa các log đã staged sau khi commit thành công
    has_uncommitted_writes.store(false, std::memory_order_release);
    undo_snapshot_.clear();
    undo_captured_dids_.clear();
    has_uncommitted_prev_next_did_ = false;
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
  // Đánh dấu có 1 generation mới: acquireSearchDb() / acquireSimpleReadDb()
  // sẽ tự reopen() lazy bên ngoài pool_mutex khi thực sự có reader mượn slot.
  db_generation.fetch_add(1, std::memory_order_acq_rel);
  return true;
}

uint64_t XapianManager::getCommittedHeadBlock() {
  std::shared_lock<ChangesMutex> lock_changes(changes_mutex);
  ensure_db_open();
  std::lock_guard<std::mutex> lock(db_mutex);
  try {
    std::string raw = readMetaLocked(kMetaHeadBlock);
    uint64_t head = 0;
    if (!raw.empty() && decodeU64BE(raw, head)) {
      return head;
    }
  } catch (...) {}
  return 0;
}

bool XapianManager::rollbackAboveBlock(uint64_t targetBlock) {
  touch();
  clearAllTxBuffers();

  // First discard any uncommitted in-memory mutations safely.
  bool need_revert_uncommitted = false;
  {
    std::shared_lock<ChangesMutex> check_lock(changes_mutex);
    need_revert_uncommitted = has_uncommitted_writes.load(std::memory_order_acquire) ||
                              !comprehensive_log.xapian_doc_logs.empty();
  }
  if (need_revert_uncommitted) {
    if (!revertUncommittedChanges()) {
      return false;
    }
  }

  std::unique_lock<ChangesMutex> lock_changes(changes_mutex);
  ensure_db_open();

  bool did_mutate = false;
  {
    std::lock_guard<std::mutex> lock_db(db_mutex);
    try {
      auto readFn = [this](const std::string &k) { return this->readMetaLocked(k); };
      auto writeFn = [this](const std::string &k, const std::string &v) { this->writeMetaLocked(k, v); };
      if (!rollbackWritableDbAboveBlock(db, targetBlock, readFn, writeFn, next_did_, did_mutate)) {
        return false;
      }
      undo_snapshot_.clear();
      undo_captured_dids_.clear();
      has_uncommitted_prev_next_did_ = false;
    } catch (const Xapian::Error &e) {
      std::cerr << "[Xapian Undo ERROR] Xapian::Error in rollbackAboveBlock(" << targetBlock
                << ") for " << db_name << ": " << e.get_msg() << std::endl;
      return false;
    } catch (const std::exception &e) {
      std::cerr << "[Xapian Undo ERROR] std::exception in rollbackAboveBlock(" << targetBlock
                << ") for " << db_name << ": " << e.what() << std::endl;
      return false;
    } catch (...) {
      std::cerr << "[Xapian Undo ERROR] Unknown exception in rollbackAboveBlock(" << targetBlock
                << ") for " << db_name << std::endl;
      return false;
    }
  }

  if (did_mutate) {
    db_generation.fetch_add(1, std::memory_order_acq_rel);
  }
  return true;
}


// Tổng dung lượng (bytes) của mọi file bên trong 1 thư mục database Xapian
// (glass backend lưu nhiều file trong 1 thư mục, không phải 1 file đơn).
// Best-effort: bỏ qua entry nào gây lỗi (race với 1 thao tác I/O khác) thay vì
// làm hỏng toàn bộ phép đo.
static uint64_t directorySizeBytes(const std::filesystem::path &dir) {
  std::error_code ec;
  if (!std::filesystem::exists(dir, ec) || ec) return 0;
  uint64_t total = 0;
  for (auto it = std::filesystem::recursive_directory_iterator(
           dir, std::filesystem::directory_options::skip_permission_denied, ec);
       !ec && it != std::filesystem::recursive_directory_iterator(); it.increment(ec)) {
    std::error_code entry_ec;
    if (it->is_regular_file(entry_ec) && !entry_ec) {
      auto sz = it->file_size(entry_ec);
      if (!entry_ec) total += sz;
    }
  }
  return total;
}

bool XapianManager::compactInPlace(size_t minSizeBytes, std::chrono::minutes minInterval) {
  // InMemory mode (TA / no base path) has no on-disk directory to compact.
  if (mvm::IsXapianBasePathEmpty()) {
    return false;
  }

  // Self-throttle: never run more than once per minInterval for this
  // instance, regardless of how often the caller (cleaner_thread) invokes
  // us. Checked before taking changes_mutex so a busy instance being
  // compacted too often doesn't even contend for that lock.
  {
    std::lock_guard<std::mutex> t_lock(compact_time_mutex_);
    auto now = std::chrono::steady_clock::now();
    if (last_compact_attempt_.time_since_epoch().count() != 0 &&
        now - last_compact_attempt_ < minInterval) {
      return false;
    }
    last_compact_attempt_ = now;
  }

  touch();
  std::unique_lock<ChangesMutex> lock(changes_mutex);

  // Never compact mid-transaction: replay_log()/commit_changes() must be
  // able to find the db exactly where they left it. has_started mirrors the
  // guard the rest of this class uses for "a transaction is in flight";
  // has_uncommitted_writes / a non-empty comprehensive_log cover the
  // buffered-but-not-yet-replayed-and-committed case.
  if (has_started || has_uncommitted_writes.load(std::memory_order_acquire) ||
      !comprehensive_log.xapian_doc_logs.empty()) {
    return false;
  }

  std::filesystem::path full_path = mvm::createFullPath(address, db_name);
  uint64_t current_size = directorySizeBytes(full_path);
  if (current_size < minSizeBytes) {
    return false; // Not worth the I/O yet.
  }

  ensure_db_open();
  std::filesystem::path tmp_path = full_path;
  tmp_path += ".compact_tmp";
  std::filesystem::path backup_path = full_path;
  backup_path += ".pre_compact_backup";

  std::error_code ec;
  std::filesystem::remove_all(tmp_path, ec);   // Clean up any crashed previous attempt.
  std::filesystem::remove_all(backup_path, ec); // Same.

  try {
    // Reads from `db` (currently open), writes a fresh compacted copy to
    // tmp_path. DBCOMPACT_NO_RENUMBER: this is a single source database, not
    // a merge, and callers (resolveVirtualDocId's cache, external Merkle-style
    // references) depend on docids staying exactly what they were.
    db.compact(tmp_path.string(), Xapian::DBCOMPACT_NO_RENUMBER);
  } catch (const Xapian::Error &e) {
    std::cerr << "[XapianCompact ERROR] compact() failed for " << db_name
               << ": " << e.get_msg() << " -- original database untouched." << std::endl;
    std::filesystem::remove_all(tmp_path, ec);
    return false;
  } catch (const std::exception &e) {
    std::cerr << "[XapianCompact ERROR] compact() failed for " << db_name
               << ": " << e.what() << " -- original database untouched." << std::endl;
    std::filesystem::remove_all(tmp_path, ec);
    return false;
  }

  // Everything from here on mutates `db`/the on-disk layout. `db` itself is
  // fully safe to close here: every OTHER method that touches it
  // (replay_log via commit_changes, revertUncommittedChanges, ...) also
  // requires this same exclusive changes_mutex, so nothing else can be
  // calling a method on `db` right now.
  //
  // The read pools are a DIFFERENT story and need care: acquireSearchDb()/
  // acquireSimpleReadDb() mark a slot in_use BEFORE the caller takes its own
  // changes_mutex shared_lock (see get_data() etc.) -- so a slot can be
  // legitimately in_use here even though we're holding changes_mutex
  // exclusively. Force-closing+deleting an in_use slot would hand that
  // caller a dangling pointer the instant it resumes (use-after-free).
  // commit_changes()'s own pool-reopen loop already only ever touches
  // `!in_use` slots for exactly this reason -- follow the same rule here:
  // only close+delete IDLE slots now; leave in_use slots' Xapian::Database
  // objects alone. This is safe to leave for later because on Linux,
  // rename()/remove_all() on files with open fds elsewhere never invalidates
  // those fds (delete-on-last-close semantics) -- an in-use slot keeps
  // reading a fully self-consistent pre-compaction snapshot until its
  // current caller releases it, and then self-heals via the existing
  // db_generation/last_gen reopen() check on its *next* acquire (same
  // mechanism commit_changes() already relies on for busy slots).
  try { db.close(); } catch (...) {}
  {
    std::lock_guard<std::mutex> pool_lock(search_pool.pool_mutex);
    for (size_t i = 0; i < search_pool.pool.size(); ++i) {
      if (search_pool.pool[i] && !search_pool.in_use[i]) {
        try { search_pool.pool[i]->close(); } catch (...) {}
        delete search_pool.pool[i];
        search_pool.pool[i] = nullptr;
        search_pool.last_gen[i] = 0;
      }
    }
  }
  {
    std::lock_guard<std::mutex> pool_lock(simple_read_pool.pool_mutex);
    for (size_t i = 0; i < simple_read_pool.pool.size(); ++i) {
      if (simple_read_pool.pool[i] && !simple_read_pool.in_use[i]) {
        try { simple_read_pool.pool[i]->close(); } catch (...) {}
        delete simple_read_pool.pool[i];
        simple_read_pool.pool[i] = nullptr;
        simple_read_pool.last_gen[i] = 0;
      }
    }
  }

  // Swap: original -> backup, tmp -> original. Keep backup_path around until
  // we've confirmed the reopen below actually works.
  std::filesystem::rename(full_path, backup_path, ec);
  if (ec) {
    std::cerr << "[XapianCompact ERROR] rename(original->backup) failed for " << db_name
               << ": " << ec.message() << " -- reopening original in place." << std::endl;
    std::filesystem::remove_all(tmp_path, ec);
    db = Xapian::WritableDatabase(full_path.string(), Xapian::DB_CREATE_OR_OPEN);
    return false;
  }
  std::filesystem::rename(tmp_path, full_path, ec);
  if (ec) {
    std::cerr << "[XapianCompact ERROR] rename(tmp->original) failed for " << db_name
               << ": " << ec.message() << " -- restoring original from backup." << std::endl;
    std::filesystem::rename(backup_path, full_path, ec); // best-effort restore
    db = Xapian::WritableDatabase(full_path.string(), Xapian::DB_CREATE_OR_OPEN);
    return false;
  }

  // Reopen the compacted copy as the live `db`. If this somehow throws,
  // restore the pre-compact backup so the instance never ends up unusable.
  try {
    db = Xapian::WritableDatabase(full_path.string(), Xapian::DB_CREATE_OR_OPEN);
  } catch (const Xapian::Error &e) {
    std::cerr << "[XapianCompact ERROR] reopen of compacted db failed for " << db_name
               << ": " << e.get_msg() << " -- restoring pre-compact backup." << std::endl;
    std::filesystem::remove_all(full_path, ec);
    std::filesystem::rename(backup_path, full_path, ec);
    try {
      db = Xapian::WritableDatabase(full_path.string(), Xapian::DB_CREATE_OR_OPEN);
    } catch (...) {}
    return false;
  } catch (const std::exception &e) {
    std::cerr << "[XapianCompact ERROR] reopen of compacted db failed for " << db_name
               << ": " << e.what() << " -- restoring pre-compact backup." << std::endl;
    std::filesystem::remove_all(full_path, ec);
    std::filesystem::rename(backup_path, full_path, ec);
    try {
      db = Xapian::WritableDatabase(full_path.string(), Xapian::DB_CREATE_OR_OPEN);
    } catch (...) {}
    return false;
  }

  std::filesystem::remove_all(backup_path, ec); // best-effort; a leftover backup is harmless clutter, not a correctness issue
  uint64_t new_size = directorySizeBytes(full_path);

  // Bump the generation counter -- this alone is enough. DO NOT also stamp
  // last_gen[i] = current_gen for every slot here: a slot we deliberately
  // left alone above (in_use, still pointing at the pre-compaction files)
  // has NOT been reopened, so its last_gen must stay at its old value --
  // otherwise the "last_gen[i] < current_gen" check in acquire*Db() would
  // wrongly conclude it's already fresh and skip the reopen() it genuinely
  // still needs on its next acquire. Slots we did close (now nullptr) don't
  // need last_gen touched either: acquire*Db()'s lazy-init branch (pool[i]
  // == nullptr) always creates a fresh Xapian::Database and sets last_gen
  // itself at that point.
  db_generation.fetch_add(1, std::memory_order_acq_rel);

  std::cerr << "[XapianCompact] " << db_name << ": " << current_size << " -> " << new_size
             << " bytes (" << (current_size > 0 ? 100 - (new_size * 100 / current_size) : 0)
             << "% reduction)" << std::endl;
  return true;
}

size_t XapianManager::pruneOldVersions(uint64_t currentBlockHeight, uint64_t retentionBlocks,
                                       size_t maxDeletePerCall) {
  if (retentionBlocks == 0) {
    return 0; // OFF switch -- absolute no-op, never even takes changes_mutex.
  }
  if (mvm::IsXapianBasePathEmpty()) {
    return 0; // InMemory: nothing on disk to prune, and this legacy pattern
              // only ever existed in the on-disk (glass) backend anyway.
  }
  if (currentBlockHeight <= retentionBlocks) {
    return 0; // Nothing could be older than the retention window yet.
  }
  uint64_t cutoff = currentBlockHeight - retentionBlocks;

  touch();
  std::unique_lock<ChangesMutex> lock(changes_mutex);

  // Never prune mid-transaction -- same guard as compactInPlace(): a
  // transaction in flight means replay_log()/commit_changes() still expect
  // the db in exactly the state they left it.
  if (has_started || has_uncommitted_writes.load(std::memory_order_acquire) ||
      !comprehensive_log.xapian_doc_logs.empty()) {
    return 0;
  }

  ensure_db_open();
  std::vector<Xapian::docid> to_delete;
  try {
    // valuestream_begin(254): by construction, ONLY documents that have a
    // value set at slot 254 are visited here -- the write-path that used to
    // set it (removed at commit 4108fe70) marked exactly and only the
    // documents it was superseding via clone_document(). An active document
    // (post-4108fe70 or never superseded) never has a slot 254 value, so it
    // can never appear in this loop -- this is a property of the data
    // itself, not something this function additionally checks for.
    for (Xapian::ValueIterator it = db.valuestream_begin(254);
        it != db.valuestream_end(254); ++it) {
      double deleted_at;
      try {
        deleted_at = Xapian::sortable_unserialise(*it);
      } catch (const std::exception &) {
        continue; // Malformed value -- skip rather than risk mis-deleting.
      }
      if (deleted_at < 0) continue;
      if (static_cast<uint64_t>(deleted_at) < cutoff) {
        to_delete.push_back(it.get_docid());
        if (to_delete.size() >= maxDeletePerCall) break;
      }
    }
  } catch (const Xapian::Error &e) {
    std::cerr << "[XapianPrune ERROR] scanning slot 254 failed for " << db_name
               << ": " << e.get_msg() << std::endl;
    return 0;
  }

  size_t deleted_count = 0;
  for (Xapian::docid did : to_delete) {
    try {
      db.delete_document(did);
      ++deleted_count;
    } catch (const Xapian::DocNotFoundError &) {
      // Already gone (e.g. a previous call's commit raced with this scan) --
      // harmless, not an error.
    } catch (const Xapian::Error &e) {
      std::cerr << "[XapianPrune ERROR] delete_document(" << did << ") failed for "
                 << db_name << ": " << e.get_msg() << std::endl;
    }
  }

  if (deleted_count > 0) {
    try {
      db.commit();
    } catch (const Xapian::Error &e) {
      std::cerr << "[XapianPrune ERROR] commit() after prune failed for " << db_name
                 << ": " << e.get_msg() << std::endl;
      return deleted_count; // Deletes are already applied in-memory to `db`;
                            // report what was queued even if the flush to
                            // disk failed -- next commit_changes()/prune call
                            // will retry the flush.
    }
    // Same "new generation" protocol as commit_changes()/compactInPlace().
    db_generation.fetch_add(1, std::memory_order_acq_rel);
    uint64_t current_gen = db_generation.load(std::memory_order_acquire);
    {
      std::lock_guard<std::mutex> pool_lock(search_pool.pool_mutex);
      for (size_t i = 0; i < search_pool.pool.size(); ++i) {
        if (search_pool.pool[i] && !search_pool.in_use[i]) {
          try { search_pool.pool[i]->reopen(); search_pool.last_gen[i] = current_gen; } catch (...) {}
        }
      }
    }
    {
      std::lock_guard<std::mutex> pool_lock(simple_read_pool.pool_mutex);
      for (size_t i = 0; i < simple_read_pool.pool.size(); ++i) {
        if (simple_read_pool.pool[i] && !simple_read_pool.in_use[i]) {
          try { simple_read_pool.pool[i]->reopen(); simple_read_pool.last_gen[i] = current_gen; } catch (...) {}
        }
      }
    }
    std::cerr << "[XapianPrune] " << db_name << ": deleted " << deleted_count
               << " legacy superseded document(s) (cutoff block " << cutoff << ")" << std::endl;
  }
  return deleted_count;
}

void XapianManager::commitAllInstances(uint64_t blockNumber) {
  if (blockNumber == 0) {
    std::cerr << "[Xapian Undo ERROR] commitAllInstances(0) refused: blockNumber > 0 is required for undo journal safety" << std::endl;
    return;
  }
  std::unique_lock<std::shared_mutex> lock(instances_mutex);
  for (auto &pair : instances) {
    auto manager = pair.second;
    if (manager && !manager->has_started) {
      manager->commit_changes(blockNumber);
    }
  }
}

int XapianManager::rollbackAllAboveBlock(uint64_t targetBlock) {
  std::unique_lock<std::shared_mutex> lock(instances_mutex);
  int rolled_back_count = 0;
  std::unordered_set<std::string> live_canonical_paths;
  std::vector<std::shared_ptr<XapianManager>> live_managers;

  for (const auto &pair : instances) {
    if (pair.second) {
      live_managers.push_back(pair.second);
      std::error_code ec;
      auto canon = std::filesystem::weakly_canonical(std::filesystem::path(pair.first), ec);
      if (!ec) {
        live_canonical_paths.insert(canon.string());
      } else {
        live_canonical_paths.insert(pair.first);
      }
    }
  }

  for (auto &mgr : live_managers) {
    uint64_t before_head = mgr->getCommittedHeadBlock();
    bool had_uncommitted = false;
    {
      std::shared_lock<ChangesMutex> check_lock(mgr->changes_mutex);
      had_uncommitted = mgr->has_uncommitted_writes.load(std::memory_order_acquire) ||
                        !mgr->comprehensive_log.xapian_doc_logs.empty();
    }
    if (!mgr->rollbackAboveBlock(targetBlock)) {
      return -1;
    }
    uint64_t after_head = mgr->getCommittedHeadBlock();
    if ((before_head > targetBlock && after_head <= targetBlock) || had_uncommitted) {
      rolled_back_count++;
    }
  }

  if (mvm::IsXapianBasePathEmpty()) {
    return rolled_back_count;
  }

  std::error_code ec;
  std::filesystem::path base_dir(mvm::GetXapianBasePath());
  if (!std::filesystem::exists(base_dir, ec) || ec || !std::filesystem::is_directory(base_dir, ec)) {
    return rolled_back_count;
  }

  for (auto addr_it = std::filesystem::directory_iterator(base_dir, ec);
       !ec && addr_it != std::filesystem::directory_iterator(); addr_it.increment(ec)) {
    std::error_code sub_ec;
    if (addr_it->is_symlink(sub_ec) || !addr_it->is_directory(sub_ec) || sub_ec) {
      continue;
    }
    for (auto db_it = std::filesystem::directory_iterator(addr_it->path(), sub_ec);
         !sub_ec && db_it != std::filesystem::directory_iterator(); db_it.increment(sub_ec)) {
      std::error_code leaf_ec;
      if (db_it->is_symlink(leaf_ec) || !db_it->is_directory(leaf_ec) || leaf_ec) {
        continue;
      }
      std::filesystem::path db_dir = db_it->path();
      std::string fname = db_dir.filename().string();
      if (endsWithStr(fname, ".compact_tmp") || endsWithStr(fname, ".pre_compact_backup")) {
        continue;
      }
      if (!std::filesystem::exists(db_dir / "iamglass", leaf_ec) || leaf_ec) {
        continue;
      }

      auto canon = std::filesystem::weakly_canonical(db_dir, leaf_ec);
      std::string canon_str = (!leaf_ec) ? canon.string() : db_dir.string();
      if (live_canonical_paths.find(canon_str) != live_canonical_paths.end()) {
        continue;
      }

      // Fast read-only probe: skip opening WritableDatabase if head_block <= targetBlock
      bool needs_rollback = false;
      uint64_t head_block = 0;
      uint64_t pruned_floor = 0;
      try {
        Xapian::Database ro_db(db_dir.string());
        std::string head_raw = ro_db.get_metadata(kMetaHeadBlock);
        if (!head_raw.empty()) {
          if (!decodeU64BE(head_raw, head_block)) {
            std::cerr << "[Xapian Undo FATAL] Corrupt head_block metadata in " << db_dir << std::endl;
            return -1;
          }
        }
        std::string index_raw = ro_db.get_metadata(kMetaUndoIndex);
        std::vector<uint64_t> idx;
        if (!decodeUndoIndex(index_raw, idx)) {
          std::cerr << "[Xapian Undo FATAL] Corrupt undo_index metadata in " << db_dir << std::endl;
          return -1;
        }
        if (head_block > targetBlock) {
          needs_rollback = true;
        } else {
          for (uint64_t bn : idx) {
            if (bn > targetBlock) {
              needs_rollback = true;
              break;
            }
          }
        }
        if (needs_rollback) {
          std::string floor_raw = ro_db.get_metadata(kMetaPrunedFloor);
          if (!floor_raw.empty() && !decodeU64BE(floor_raw, pruned_floor)) {
            std::cerr << "[Xapian Undo FATAL] Corrupt pruned_floor metadata in " << db_dir << std::endl;
            return -1;
          }
        }
        ro_db.close();
      } catch (const Xapian::Error &e) {
        std::cerr << "[Xapian Undo FATAL] Failed probing Xapian DB at " << db_dir
                  << ": " << e.get_msg() << std::endl;
        return -1;
      } catch (const std::exception &e) {
        std::cerr << "[Xapian Undo FATAL] Exception probing Xapian DB at " << db_dir
                  << ": " << e.what() << std::endl;
        return -1;
      }

      if (!needs_rollback) {
        continue;
      }

      try {
        Xapian::WritableDatabase rw_db(db_dir.string(), Xapian::DB_OPEN);
        Xapian::docid next_did = 1;
        uint32_t stored_did = 0;
        std::string did_raw = rw_db.get_metadata(kMetaNextDid);
        if (!did_raw.empty() && decodeU32BE(did_raw, stored_did) && stored_did >= 1) {
          next_did = static_cast<Xapian::docid>(stored_did);
        } else {
          next_did = rw_db.get_lastdocid() + 1;
          if (next_did == 0) next_did = 1;
        }

        bool did_mutate = false;
        auto readFn = [&rw_db](const std::string &k) { return rw_db.get_metadata(k); };
        auto writeFn = [&rw_db](const std::string &k, const std::string &v) { rw_db.set_metadata(k, v); };
        if (!rollbackWritableDbAboveBlock(rw_db, targetBlock, readFn, writeFn, next_did, did_mutate)) {
          rw_db.close();
          return -1;
        }
        rw_db.close();
        if (did_mutate) {
          rolled_back_count++;
        }
      } catch (const Xapian::Error &e) {
        std::cerr << "[Xapian Undo FATAL] Failed rolling back Xapian DB at " << db_dir
                  << " to block " << targetBlock << ": " << e.get_msg() << std::endl;
        return -1;
      } catch (const std::exception &e) {
        std::cerr << "[Xapian Undo FATAL] Exception rolling back Xapian DB at " << db_dir
                  << " to block " << targetBlock << ": " << e.what() << std::endl;
        return -1;
      }
    }
  }

  return rolled_back_count;
}


// Tính toán hash của các thay đổi đã được staged
std::array<uint8_t, 32u> XapianManager::getChangeHash() {
  std::lock_guard<ChangesMutex> lock(
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
  std::lock_guard<ChangesMutex> lock(
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
bool XapianManager::saveAllAndCommit(uint64_t blockNumber) {
  return this->commit_changes(blockNumber); // Gọi hàm commit Xapian
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
                if (!mvm::IsXapianBasePathEmpty()) {
                    for (auto it = XapianManager::instances.begin(); it != XapianManager::instances.end(); ++it)
                    {
                        // Kiểm tra con trỏ hợp lệ, trạng thái idle và không còn dữ liệu chưa commit
                        if (it->second && it->second->is_idle_for(std::chrono::minutes(1)) &&
                            it->second->is_evictable())
                        {
                            std::shared_ptr<XapianManager> temp_ptr = it->second;
                            // <= 2 nghĩa là chỉ có map và temp_ptr đang giữ tham chiếu
                            if (temp_ptr.use_count() <= 2)
                            {
                                keys_to_erase.push_back(it->first);
                            }
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

    // Giai đoạn 3: compact các instance vẫn đang "sống" (không nằm trong
    // keys_to_erase) -- đây chính là các DB bị ghi liên tục, không bao giờ
    // idle đủ lâu để lọt vào Giai đoạn 1, nên phải có đường riêng để nén
    // định kỳ thay vì chỉ trông chờ vào lúc bị huỷ (xem doc comment của
    // compactInPlace()). Lấy shared_ptr rồi thả instances_mutex TRƯỚC khi
    // gọi compactInPlace() -- compact có thể mất vài trăm ms tới vài giây
    // trên DB lớn, không được giữ lock của toàn bộ map trong lúc đó.
    std::vector<std::shared_ptr<XapianManager>> live_instances;
    {
      std::shared_lock<std::shared_mutex> read_lock(XapianManager::instances_mutex);
      for (auto &pair : XapianManager::instances) {
        if (pair.second &&
            std::find(keys_to_erase.begin(), keys_to_erase.end(), pair.first) ==
                keys_to_erase.end()) {
          live_instances.push_back(pair.second);
        }
      }
    }
    for (auto &manager : live_instances) {
      // Tự throttle bên trong (mặc định tối đa 1 lần/giờ/instance) -- gọi
      // mỗi phút ở đây là vô hại, compactInPlace() tự no-op khi chưa tới hạn.
      manager->compactInPlace();
    }

    // Giai đoạn 4: dọn document Xapian "mồ côi" (đánh dấu slot 254 bởi
    // write-path cũ trước commit 4108fe70) -- xem doc comment của
    // pruneOldVersions(). retentionBlocks/currentBlockHeight giống nhau cho
    // mọi instance trong 1 vòng lặp, nên chỉ cần lấy 1 lần, không phải mỗi
    // instance 1 lần callback sang Go.
    {
      Value_return retentionRet = GetXapianPruneRetentionBlocks();
      if (retentionRet.success && retentionRet.data_p && retentionRet.data_size == 8) {
        uint64_t retentionBlocks = 0;
        for (int i = 0; i < 8; ++i) {
          retentionBlocks = (retentionBlocks << 8) | static_cast<uint64_t>(retentionRet.data_p[i]);
        }
        free(retentionRet.data_p); // Go's C.CBytes() -> caller (đây) phải free, xem GetChainId's own comment

        if (retentionBlocks > 0) { // Bỏ hẳn callback lấy block height nếu tắt (retentionBlocks==0) -- không tốn gì thêm khi feature đang OFF.
          Value_return heightRet = GetCurrentBlockNumberForXapianPrune();
          if (heightRet.success && heightRet.data_p && heightRet.data_size == 8) {
            uint64_t currentHeight = 0;
            for (int i = 0; i < 8; ++i) {
              currentHeight = (currentHeight << 8) | static_cast<uint64_t>(heightRet.data_p[i]);
            }
            free(heightRet.data_p);

            for (auto &manager : live_instances) {
              manager->pruneOldVersions(currentHeight, retentionBlocks);
            }
          } else if (heightRet.data_p) {
            free(heightRet.data_p);
          }
        }
      } else if (retentionRet.data_p) {
        free(retentionRet.data_p);
      }
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
    std::unique_lock<ChangesMutex> db_lock(changes_mutex);
    ensure_db_open();
    has_uncommitted_writes.store(true, std::memory_order_release);
    if (!has_uncommitted_prev_next_did_) {
        uncommitted_prev_next_did_ = next_did_;
        has_uncommitted_prev_next_did_ = true;
    }

    const bool db_initially_empty = (db.get_lastdocid() == 0);

    // Document staging cache for batch coalescing (tối ưu hóa gom các thao tác trên cùng 1 docid trong 1 block).
    // std::map keeps modified_docs sorted lexicographically by normalized docid (nid),
    // ensuring newly allocated physical Xapian::docids match legacy replay_log / FullDbLogs order.
    std::map<std::string, Xapian::Document> modified_docs;
    std::set<std::string> deleted_docs;
    std::unordered_map<std::string, Xapian::docid> resolved_dids;

    auto resolve_cached_did = [&](const std::string& nid) -> Xapian::docid {
        auto it = resolved_dids.find(nid);
        if (it != resolved_dids.end()) {
            return it->second;
        }
        if (db_initially_empty) {
            resolved_dids.emplace(nid, 0);
            return 0;
        }
        Xapian::docid did = resolveVirtualDocId(nid);
        resolved_dids.emplace(nid, did);
        return did;
    };

    auto alloc_next_did = [&]() -> Xapian::docid {
        if (next_did_ == 0) {
            next_did_ = db.get_lastdocid() + 1;
            if (next_did_ == 0) next_did_ = 1;
        }
        return next_did_++;
    };

    // Single-lookup, single-load, single-serialise pre-image capture & document loader.
    // Avoids calling resolveVirtualDocId / get_document / serialise multiple times per doc
    // and never calls Document::unserialise on the normal commit hot path.
    auto capture_undo_and_optionally_load = [&](const std::string& nid, bool want_doc, Xapian::Document* out_doc) -> bool {
        Xapian::docid did = resolve_cached_did(nid);
        bool need_undo = (undo_snapshot_.find(nid) == undo_snapshot_.end());
        if (did == 0 || db_initially_empty) {
            if (need_undo) {
                undo_snapshot_.emplace(nid, UndoEntry{false, 0, {}});
            }
            return false;
        }
        if (need_undo && undo_captured_dids_.find(did) != undo_captured_dids_.end()) {
            need_undo = false;
        }
        if (!need_undo && !want_doc) {
            return false;
        }
        try {
            Xapian::Document existing_doc = db.get_document(did);
            if (need_undo) {
                undo_captured_dids_.insert(did);
                undo_snapshot_.emplace(nid, UndoEntry{true, did, existing_doc.serialise()});
            }
            if (want_doc && out_doc != nullptr) {
                *out_doc = std::move(existing_doc);
                return true;
            }
            return false;
        } catch (const Xapian::DocNotFoundError&) {
            if (need_undo) {
                undo_snapshot_.emplace(nid, UndoEntry{false, 0, {}});
            }
            return false;
        } catch (const Xapian::Error&) {
            if (need_undo) {
                undo_snapshot_.emplace(nid, UndoEntry{false, 0, {}});
            }
            return false;
        } catch (const std::exception&) {
            if (need_undo) {
                undo_snapshot_.emplace(nid, UndoEntry{false, 0, {}});
            }
            return false;
        }
    };

    auto get_or_load_doc = [&](const std::string& v_docid) -> Xapian::Document& {
        std::string nid = normalize_docid(v_docid);
        auto it = modified_docs.find(nid);
        if (it != modified_docs.end()) {
            return it->second;
        }

        Xapian::Document doc;
        bool was_deleted = (deleted_docs.find(nid) != deleted_docs.end());
        bool loaded = false;
        if (was_deleted) {
            capture_undo_and_optionally_load(nid, false, nullptr);
        } else {
            loaded = capture_undo_and_optionally_load(nid, true, &doc);
        }

        if (!loaded) {
            std::string clean_id = nid;
            if (clean_id.length() >= 2 && clean_id.substr(0, 2) == "0x") clean_id = clean_id.substr(2);
            doc.add_term("Q" + clean_id);
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
                if (modified_docs.find(nid) == modified_docs.end()) {
                    capture_undo_and_optionally_load(nid, false, nullptr);
                }
                // IDEMPOTENCY FIX: NEW_DOC always initializes a brand-new clean document
                // with only its Q<clean_id> system term and raw data, never inheriting
                // stale terms/wdf/positions/values from any prior document with the same nid.
                Xapian::Document fresh_doc;
                std::string clean_id = nid;
                if (clean_id.length() >= 2 && clean_id.substr(0, 2) == "0x") clean_id = clean_id.substr(2);
                fresh_doc.add_term("Q" + clean_id);
                fresh_doc.set_data(data.data);
                modified_docs[nid] = std::move(fresh_doc);
                deleted_docs.erase(nid);
            } else if (entry.op == XapianLog::Operation::DEL_DOC) {
                auto data = std::get<XapianLog::DelDocData>(entry.data);
                std::string nid = normalize_docid(data.docid);
                capture_undo_and_optionally_load(nid, false, nullptr);
                modified_docs.erase(nid);
                deleted_docs.insert(nid);
            } else if (entry.op == XapianLog::Operation::ADD_VALUE) {
                auto data = std::get<XapianLog::AddValueData>(entry.data);
                Xapian::Document& doc = get_or_load_doc(data.docid);
                doc.add_value(data.slot, data.value);
            } else if (entry.op == XapianLog::Operation::ADD_TERM) {
                auto data = std::get<XapianLog::AddTermData>(entry.data);
                Xapian::Document& doc = get_or_load_doc(data.docid);
                doc.add_term(data.term);
            } else if (entry.op == XapianLog::Operation::SET_DATA) {
                auto data = std::get<XapianLog::SetDataData>(entry.data);
                Xapian::Document& doc = get_or_load_doc(data.docid);
                doc.set_data(data.data);
            } else if (entry.op == XapianLog::Operation::INDEX_TEXT) {
                auto data = std::get<XapianLog::IndexTextData>(entry.data);
                std::string nid = normalize_docid(data.docid);
                Xapian::Document& doc = get_or_load_doc(nid);
                Xapian::TermGenerator termgenerator;
                termgenerator.set_stemmer(Xapian::Stem("english"));
                termgenerator.set_stemming_strategy(Xapian::TermGenerator::STEM_SOME);
                termgenerator.set_max_word_length(180);
                termgenerator.set_document(doc);
                termgenerator.index_text(data.text, data.wdf_inc, data.prefix);

                // Sanitize generated terms: prevent term collision with internal docid of another document
                std::string own_clean_id = nid;
                if (own_clean_id.length() >= 2 && own_clean_id.substr(0, 2) == "0x") {
                    own_clean_id = own_clean_id.substr(2);
                }
                std::string own_system_term = "Q" + own_clean_id;
                std::vector<std::string> colliding_terms;
                for (auto it = doc.termlist_begin(); it != doc.termlist_end(); ++it) {
                    std::string t = *it;
                    if (isReservedSystemDocIdTerm(t) && t != own_system_term) {
                        colliding_terms.push_back(t);
                    }
                }
                for (const auto& t : colliding_terms) {
                    std::cerr << "[ERROR] XapianManager: removing colliding reserved system docid term: " << t << " from doc " << nid << std::endl;
                    doc.remove_term(t);
                }
            }
        }

        // Deduplicate all physical docids slated for deletion and catch Xapian::DocNotFoundError
        // (Xapian::Error does NOT inherit from std::exception; uncaught DocNotFoundError on a
        // non-existent docid would otherwise abort replay_log and crash the node!)
        std::set<Xapian::docid> final_dids_to_delete;
        for (const auto& v_docid : deleted_docs) {
            Xapian::docid did = resolve_cached_did(v_docid);
            if (did > 0) {
                final_dids_to_delete.insert(did);
            }
        }
        for (Xapian::docid did : final_dids_to_delete) {
            try {
                db.delete_document(did);
            } catch (const Xapian::DocNotFoundError&) {
                // Idempotent no-op when deleting a non-existent document
            } catch (const Xapian::Error& e) {
                std::cerr << "[Xapian Replay WARN] delete_document(" << did << ") Xapian::Error: " << e.get_msg() << std::endl;
            } catch (const std::exception& e) {
                std::cerr << "[Xapian Replay WARN] delete_document(" << did << ") error: " << e.what() << std::endl;
            }
        }

        // Apply coalesced modified documents (1 write per docid, deterministic did in sorted nid order)
        for (auto& pair : modified_docs) {
            const std::string& v_docid = pair.first;
            Xapian::Document& doc = pair.second;
            Xapian::docid did = resolve_cached_did(v_docid);
            if (did > 0) {
                db.replace_document(did, doc);
            } else {
                db.replace_document(alloc_next_did(), doc);
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
  std::unique_lock<ChangesMutex> lock_changes(changes_mutex); // Khóa để thao tác an toàn
  bool has_writes = has_uncommitted_writes.load(std::memory_order_acquire);
  
  // FORK-SAFETY: Return immediately if there are no uncommitted changes
  if (!has_writes && comprehensive_log.xapian_doc_logs.empty()) {
      return true;
  }

  ensure_db_open();
  std::lock_guard<std::mutex> lock(db_mutex);
  
  try {
    // 1. Xóa các thay đổi đang chờ trong log và reset cờ dirty
    comprehensive_log.xapian_doc_logs.clear();
    has_uncommitted_writes.store(false, std::memory_order_release);

    // 2. Khôi phục từ undo_snapshot_ cho cả InMemory và on-disk WritableDatabase.
    // LƯU Ý QUAN TRỌNG: Xapian::WritableDatabase::close() tự động flush/commit các thay đổi
    // đang buffer xuống đĩa (nếu không dùng begin_transaction), nên KHÔNG ĐƯỢC dùng db.close()
    // để hủy thay đổi chưa commit! Phải áp ngược undo_snapshot_ trực tiếp lên db.
    for (auto &entry : undo_snapshot_) {
      const std::string &nid = entry.first;
      UndoEntry &ue = entry.second;
      Xapian::docid did = resolveDocIdInDb(db, nid);
      try {
        if (ue.existed && !ue.serialized_doc.empty()) {
          Xapian::Document restored = Xapian::Document::unserialise(ue.serialized_doc);
          Xapian::docid restore_did = (ue.original_did > 0) ? ue.original_did : did;
          if (did > 0 && did != restore_did) {
            try { db.delete_document(did); } catch (const Xapian::DocNotFoundError &) {}
          }
          if (restore_did > 0) {
            db.replace_document(restore_did, restored);
          } else {
            db.add_document(restored);
          }
        } else {
          if (did > 0) {
            try { db.delete_document(did); } catch (const Xapian::DocNotFoundError &) {}
          }
          if (ue.original_did > 0 && ue.original_did != did) {
            try { db.delete_document(ue.original_did); } catch (const Xapian::DocNotFoundError &) {}
          }
        }
      } catch (const Xapian::Error &) {
      } catch (const std::exception &) {
      } catch (...) {
      }
    }
    if (has_uncommitted_prev_next_did_) {
      next_did_ = uncommitted_prev_next_did_;
    }
    undo_snapshot_.clear();
    undo_captured_dids_.clear();
    has_uncommitted_prev_next_did_ = false;
    if (!mvm::IsXapianBasePathEmpty()) {
      db.commit();
      db_generation.fetch_add(1, std::memory_order_acq_rel);
    }
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
    std::unique_lock<std::shared_mutex> write_lock(instances_mutex);
    auto it = instances.find(db_path_str);
    if (it == instances.end())
    {
        return false; // Không tìm thấy instance để hủy
    }

    if (onlyIfIdle)
    {
        // Re-kiểm tra atomic dưới cùng lock với erase: không bao giờ hủy trong chế độ
        // InMemory hoặc khi còn tx_buffers / has_uncommitted_writes chưa commit!
        if (mvm::IsXapianBasePathEmpty() || !it->second ||
            !it->second->is_idle_for(std::chrono::minutes(1)) ||
            it->second.use_count() > 1 || !it->second->is_evictable())
        {
            return false;
        }
    }

    std::shared_ptr<XapianManager> instance_ptr = it->second;
    if (instance_ptr)
    {
        bool need_revert = false;
        {
            std::shared_lock<ChangesMutex> check_lock(instance_ptr->changes_mutex);
            need_revert = instance_ptr->has_uncommitted_writes.load(std::memory_order_acquire) ||
                          !instance_ptr->comprehensive_log.xapian_doc_logs.empty();
        }
        if (need_revert)
        {
            instance_ptr->revertUncommittedChanges();
        }
        // Khóa changes_mutex và đóng db TRƯỚC KHI nhả instances_mutex (tuân thủ đúng thứ tự
        // khóa toàn cục instances_mutex -> changes_mutex như getInstance/commitAllInstances),
        // triệt tiêu hoàn toàn cửa sổ race flintlock DatabaseLockError khi thread khác mở lại DB.
        std::unique_lock<ChangesMutex> changes_lock(instance_ptr->changes_mutex);
        try {
            instance_ptr->db.close();
        } catch (const Xapian::Error &) {
        } catch (const std::exception &) {
        } catch (...) {
        }
        instance_ptr->is_db_open = false;
    }

    instances.erase(it);
    return instance_ptr != nullptr;
}

void XapianManager::destroyAllInstances() {
    std::vector<std::string> keys;
    {
        std::shared_lock<std::shared_mutex> lock(instances_mutex);
        for (const auto &pair : instances) {
            keys.push_back(pair.first);
        }
    }
    for (const auto &key : keys) {
        destroyInstance(key, false);
    }
}

// Lấy một bản ghi log tổng hợp chứa tất cả các thay đổi đã staged VÀ XÓA CHÚNG KHỎI MANAGER
XapianLog::ComprehensiveLog XapianManager::extractComprehensiveChangeLogs()
{
    std::lock_guard<ChangesMutex> lock(changes_mutex);
    XapianLog::ComprehensiveLog log_copy = std::move(comprehensive_log);
    comprehensive_log.xapian_doc_logs.clear();
    comprehensive_log.db_name = this->db_name; // Restore db_name after std::move
    return log_copy;
}

// Lấy một bản ghi log tổng hợp chứa tất cả các thay đổi đã staged
XapianLog::ComprehensiveLog XapianManager::removeLogsUntilNearestEndCommand()
{
    std::lock_guard<ChangesMutex> lock(changes_mutex);
    comprehensive_log.removeLogsUntilNearestEndCommand();
    return comprehensive_log;
}
void XapianManager::ensure_db_open() {
    if (mvm::IsXapianBasePathEmpty()) return;
    std::lock_guard<std::mutex> lock(db_mutex);
    if (!is_db_open) {
        try {
            db = openXapianDb(address, db_name);
            is_db_open = true;
        } catch (...) {}
    }
}

void XapianManager::evict_handles() {
    if (mvm::IsXapianBasePathEmpty()) return;
    std::unique_lock<ChangesMutex> ch_lock(changes_mutex);
    std::lock_guard<std::mutex> lock(db_mutex);
    if (is_db_open) {
        try { db.close(); } catch (...) {}
        is_db_open = false;
    }
    
    {
        std::lock_guard<std::mutex> s_lock(search_pool.pool_mutex);
        for (size_t i = 0; i < search_pool.pool.size(); ++i) {
            if (!search_pool.in_use[i] && search_pool.pool[i]) {
                try { search_pool.pool[i]->close(); } catch (...) {}
                delete search_pool.pool[i];
                search_pool.pool[i] = nullptr;
            }
        }
    }
    
    {
        std::lock_guard<std::mutex> r_lock(simple_read_pool.pool_mutex);
        for (size_t i = 0; i < simple_read_pool.pool.size(); ++i) {
            if (!simple_read_pool.in_use[i] && simple_read_pool.pool[i]) {
                try { simple_read_pool.pool[i]->close(); } catch (...) {}
                delete simple_read_pool.pool[i];
                simple_read_pool.pool[i] = nullptr;
            }
        }
    }
}
