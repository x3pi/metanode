#include "mvm_linker.hpp"
#include "mvm/processor.h"
#include "my_global_state.h"
#include <iostream>
#include <string>
#include <tuple>

#include "my_extension/my_extension.h"
#include "my_logger.h"
#include "xapian/xapian_manager.h"

#include "my_extension/utils.h"
#include "xapian/xapian_registry.h"

#include "state.h"
#include <cassert>
#include <chrono>
#include <cstdio>
#include <fmt/format_header_only.h>
#include <fstream>
#include <iostream>
#include <mutex>
#include <nlohmann/json.hpp>
#include <random>
#include <sstream>
#include <stdbool.h> // Thêm include này
#include <stdlib.h>
#include <unordered_map>
#include <vector>

ExecuteResult *pendingResult;

nlohmann::json vectorLogsHandlerToJson(mvm::VectorLogHandler logHandler) {
  auto json_logs = nlohmann::json::array();
  for (const auto &log : logHandler.logs) {
    nlohmann::json json_log;
    mvm::to_json(json_log, log);
    json_logs.push_back(json_log);
  }

  return json_logs;
}

// Hàm helper để xóa mảng con trỏ, mỗi phần tử cũng là mảng
template <typename T> void safeDeleteArrayOfArrays(T **&ptrArray, int count) {
  if (ptrArray) {
    for (int i = 0; i < count; ++i) {
      if (ptrArray[i]) {
        delete[] ptrArray[i];
        ptrArray[i] = nullptr;
      }
    }
    delete[] ptrArray;
    ptrArray = nullptr;
  }
}

void cleanupProcessResultMemoryOnError(
    char *&b_output, char *&b_exmsg, char *&b_logs,
    uint8_t **&b_add_balance_change, int length_add_balance_change,
    uint8_t **&b_sub_balance_change, int length_sub_balance_change,
    uint8_t **&b_nonce_change, int length_nonce_change,
    uint8_t **&b_code_change, int length_code_change, int *&length_codes,
    uint8_t **&b_storage_change, int length_storage_change,
    int *&length_storages, char **&b_full_db_hash, int length_full_db_hash,
    int *&length_full_db_hashes) {
  std::cerr << "[CLEANUP] Cleaning up allocated memory due to error..."
            << std::endl;
  delete[] b_output;
  b_output = nullptr;
  delete[] b_exmsg;
  b_exmsg = nullptr;
  delete[] b_logs;
  b_logs = nullptr;

  safeDeleteArrayOfArrays(b_add_balance_change, length_add_balance_change);
  safeDeleteArrayOfArrays(b_sub_balance_change, length_sub_balance_change);
  safeDeleteArrayOfArrays(b_nonce_change, length_nonce_change);

  safeDeleteArrayOfArrays(b_code_change, length_code_change);
  delete[] length_codes;
  length_codes = nullptr;

  safeDeleteArrayOfArrays(b_storage_change, length_storage_change);
  delete[] length_storages;
  length_storages = nullptr;

  safeDeleteArrayOfArrays(b_full_db_hash, length_full_db_hash);
  delete[] length_full_db_hashes;
  length_full_db_hashes = nullptr;

  std::cerr << "[CLEANUP] Cleanup finished." << std::endl;
}

void append_argument(std::vector<uint8_t> &code, const uint256_t &arg) {
  const auto pre_size = code.size();
  code.resize(pre_size + 32u);
  mvm::to_big_endian(arg, code.data() + pre_size);
}

// Helper function để xử lý exception thống nhất
mvm::ExecResult handleException(const std::exception &e) {
  mvm::ExecResult error_result;
  error_result.er = mvm::ExitReason::threw;
  error_result.exmsg = e.what();

  // Kiểm tra xem có phải mvm::Exception không
  const mvm::Exception *mvm_ex = dynamic_cast<const mvm::Exception *>(&e);
  if (mvm_ex != nullptr) {
    error_result.ex = mvm_ex->type;
  } else {
    error_result.ex = mvm::Exception::Type::ErrExecutionReverted;
  }

  return error_result;
}

mvm::ExecResult handleUnknownException(const std::string &context = "") {
  mvm::ExecResult error_result;
  error_result.er = mvm::ExitReason::threw;
  error_result.ex = mvm::Exception::Type::ErrExecutionReverted;
  error_result.exmsg = context.empty()
                           ? "Unknown exception occurred"
                           : "Unknown exception occurred during " + context;
  return error_result;
}

// Tạo ExecuteResult an toàn khi processResult cũng thất bại - KHÔNG BAO GIỊ
// THROW
ExecuteResult *createSafeErrorResult() noexcept {
  try {
    ExecuteResult *rs = new ExecuteResult{};
    // Zero-initialize tất cả các trường
    rs->b_exitReason = (char)mvm::ExitReason::threw;
    rs->b_exception = (char)mvm::Exception::Type::ErrExecutionReverted;
    rs->b_exmsg = nullptr;
    rs->length_exmsg = 0;
    rs->b_output = nullptr;
    rs->length_output = 0;
    rs->full_db_hash = nullptr;
    rs->length_full_db_hash = 0;
    rs->length_full_db_hashes = nullptr;
    rs->full_db_logs = nullptr;
    rs->length_full_db_logs = 0;
    rs->length_full_db_logs_data = nullptr;
    rs->b_add_balance_change = nullptr;
    rs->length_add_balance_change = 0;
    rs->b_nonce_change = nullptr;
    rs->length_nonce_change = 0;
    rs->b_sub_balance_change = nullptr;
    rs->length_sub_balance_change = 0;
    rs->b_code_change = nullptr;
    rs->length_code_change = 0;
    rs->length_codes = nullptr;
    rs->b_storage_change = nullptr;
    rs->length_storage_change = 0;
    rs->length_storages = nullptr;
    rs->b_logs = nullptr;
    rs->length_logs = 0;
    rs->gas_used = 0;
    return rs;
  } catch (...) {
    // Nếu ngay cả new cũng thất bại (out of memory), trả về nullptr
    // Go sẽ phải kiểm tra nil
    return nullptr;
  }
}
// 🔒 NONCE-FIX: Helper to increment sender nonce using State cache as
// authority. When isCache=true (Master block processing), reads nonce from
// State::instances, increments by exactly 1, writes back immediately. When
// isCache=false (Sub virtual execution), uses the local account copy.
void incrementSenderNonce(mvm::MyGlobalState &gs, const mvm::Address &from,
                          mvm::AccountState &fromAc) {
  if (gs.is_cache() && State::instanceExists(from)) {
    // 🔒 State cache is the single source of truth
    auto state = State::getInstance(from);
    auto currentNonce = state->getNonce();
    auto newNonce = currentNonce + 1;

    // Update State cache immediately (so next TX in same block gets correct
    // nonce)
    state->setNonce(newNonce);
    // Update local copy for consistency (cast from uint256_t to uint64_t)
    fromAc.acc.set_nonce(static_cast<uint64_t>(newNonce));
    // Report to Go via MapNonce
    gs.set_addresses_nonce_change(from, newNonce);
  } else {
    // No cache (Sub virtual execution) — use local copy
    auto oldNonce = fromAc.acc.get_nonce();
    fromAc.acc.increment_nonce();
    auto newNonce = fromAc.acc.get_nonce();

    gs.set_addresses_nonce_change(from, newNonce);
  }
}

// Run input as an EVM transaction, check the result and return the output
mvm::ExecResult run(mvm::MyGlobalState &gs, bool deploy,
                    const mvm::Address &from, const mvm::Address &to,
                    const uint256_t &amount, uint64_t gas_price,
                    uint64_t gas_limit, mvm::VectorLogHandler &log_handler,
                    const mvm::Code &input, unsigned char *mvmId, bool readOnly,
                    const uint256_t &tx_hash, bool is_debug,
                    bool is_off_chain = false) {
  mvm::Transaction tx(from, amount, gas_price, gas_limit, tx_hash, is_debug);
  MyLogger logger = MyLogger();
  MyExtension extension = MyExtension(mvmId, is_off_chain, &tx_hash);
  mvm::Processor p(gs, log_handler, extension, logger);

  try {

    auto fromAc = gs.get(from, nullptr);
    auto toAc = gs.get(to, nullptr);
    if (amount > 0) {
      fromAc.acc.set_balance(fromAc.acc.get_balance() - amount);
      toAc.acc.set_balance(toAc.acc.get_balance() + amount);
      gs.add_addresses_sub_balance_change(from, amount);
      gs.add_addresses_add_balance_change(to, amount);
    }

    const auto exec_result = p.run(tx, deploy, from, gs.get(to, nullptr), input,
                                   amount, nullptr, readOnly);

    // 🔒 NONCE-FIX: Always increment nonce here (for both deploy and
    // non-deploy). CREATE opcode does NOT call gs.set_addresses_nonce_change()
    // — it only modifies MyAccount locally. The nonce change must be reported
    // from here. State cache sync is handled by set_addresses_nonce_change() in
    // my_global_state.cpp.
    incrementSenderNonce(gs, from, fromAc);

    return exec_result;
  } catch (const std::exception &e) {
    mvm::ExecResult error_result = handleException(e);

    // Increment nonce even on error (EVM behavior)
    try {
      auto fromAc = gs.get(from, nullptr);
      incrementSenderNonce(gs, from, fromAc);
    } catch (...) {
    }
    return error_result;
  } catch (...) {
    mvm::ExecResult error_result = handleUnknownException("run");

    // Increment nonce even on error (EVM behavior)
    try {
      auto fromAc = gs.get(from, nullptr);
      incrementSenderNonce(gs, from, fromAc);
    } catch (...) {
    }
    return error_result;
  }
}

mvm::BlockContext CreateBlockContext(
    unsigned char *mvmId, uint64_t prevrandao, uint64_t gas_limit,
    uint64_t time, uint64_t base_fee, uint256_t number, uint256_t coinbase,
    uint256_t tx_hash = 0,
    // TEE-packaging B1 (note/tee_core_packaging_plan.md): additive — every
    // existing caller that doesn't pass these keeps getting the same
    // zero-value BlockContext fields as before, so executeBatch/sendNative/
    // processNativeMintBurn/noncePlusOne (which never reach the interpreter
    // anyway) don't need to change at all.
    uint256_t chain_id = 0,
    const std::vector<uint256_t> &blob_versioned_hashes = {},
    uint256_t blob_base_fee = 0,
    const std::vector<uint8_t> &cross_chain_sender = {},
    uint64_t cross_chain_source_id = 0) {
  mvm::BlockContext block_context;
  block_context.mvmId = mvmId;
  block_context.prevrandao = prevrandao;
  block_context.gas_limit = gas_limit;
  block_context.time = time;
  block_context.base_fee = base_fee;
  block_context.number = number;
  block_context.coinbase = coinbase;
  block_context.tx_hash =
      tx_hash; // txHash của TX hiện tại — precompile dùng làm msgId
  block_context.chain_id = chain_id;
  block_context.blob_versioned_hashes = blob_versioned_hashes;
  block_context.blob_base_fee = blob_base_fee;
  block_context.cross_chain_sender = cross_chain_sender;
  block_context.cross_chain_source_id = cross_chain_source_id;
  return block_context;
}

// TEE-packaging B1: parses the new context parameters shared by
// deploy/call/execute (the 3 entry points that run the interpreter) out of
// their raw cgo byte-buffer form. A null pointer / zero count means "not
// supplied", matching each BlockContext field's zero-value default — see
// block_context.h.
struct ParsedTxContext {
  uint256_t chain_id = 0;
  std::vector<uint256_t> blob_versioned_hashes;
  uint256_t blob_base_fee = 0;
  std::vector<uint8_t> cross_chain_sender;
  uint64_t cross_chain_source_id = 0;
};

ParsedTxContext ParseTxContext(unsigned char *b_chain_id,
                               unsigned char *b_blob_versioned_hashes,
                               int blob_versioned_hashes_count,
                               unsigned char *b_blob_base_fee,
                               unsigned char *b_cross_chain_sender,
                               unsigned char *b_cross_chain_source_id) {
  ParsedTxContext ctx;
  if (b_chain_id != nullptr) {
    ctx.chain_id = mvm::from_big_endian((uint8_t *)b_chain_id, 32u);
  }
  if (b_blob_versioned_hashes != nullptr && blob_versioned_hashes_count > 0) {
    ctx.blob_versioned_hashes.reserve(blob_versioned_hashes_count);
    for (int i = 0; i < blob_versioned_hashes_count; ++i) {
      ctx.blob_versioned_hashes.push_back(mvm::from_big_endian(
          (uint8_t *)(b_blob_versioned_hashes + i * 32), 32u));
    }
  }
  if (b_blob_base_fee != nullptr) {
    ctx.blob_base_fee = mvm::from_big_endian((uint8_t *)b_blob_base_fee, 32u);
  }
  if (b_cross_chain_sender != nullptr) {
    ctx.cross_chain_sender.assign(b_cross_chain_sender,
                                  b_cross_chain_sender + 20);
  }
  if (b_cross_chain_source_id != nullptr) {
    uint64_t v = 0;
    for (int i = 0; i < 8; ++i) {
      v = (v << 8) | b_cross_chain_source_id[i];
    }
    ctx.cross_chain_source_id = v;
  }
  return ctx;
}

// --- Hàm processResult ---
ExecuteResult *processResult(mvm::ExecResult result, mvm::MyGlobalState &gs,
                             mvm::VectorLogHandler &log_handler,
                             bool isOffChain = false) {
  // --- Khởi tạo tất cả các con trỏ là nullptr ban đầu ---
  char *b_output = nullptr;
  int length_output = 0;
  char *b_exmsg = nullptr;
  int length_exmsg = 0;
  char *b_logs = nullptr;
  int length_logs = 0;
  uint8_t **b_add_balance_change = nullptr;
  int length_add_balance_change = 0;
  uint8_t **b_sub_balance_change = nullptr;
  int length_sub_balance_change = 0;
  uint8_t **b_nonce_change = nullptr;
  int length_nonce_change = 0;
  uint8_t **b_code_change = nullptr;
  int length_code_change = 0;
  int *length_codes = nullptr;
  uint8_t **b_storage_change = nullptr;
  int length_storage_change = 0;
  int *length_storages = nullptr;
  uint8_t **b_storage_read = nullptr;
  int length_storage_read = 0;
  int *length_storages_read = nullptr;
  char **b_full_db_hash = nullptr;
  int length_full_db_hash = 0;
  int *length_full_db_hashes = nullptr;
  char **b_full_db_logs = nullptr;
  int length_full_db_logs = 0;
  int *length_full_db_logs_data = nullptr;

  ExecuteResult *pendingResult = nullptr; // Khởi tạo con trỏ kết quả là nullptr

  try {
    // --- Xử lý Output ---
    if (!result.output.empty()) {
      length_output = static_cast<int>(result.output.size());
      b_output = new char[length_output];
      memcpy(b_output, result.output.data(), length_output);
    }

    // --- Xử lý Exception Message ---
    if (!result.exmsg.empty()) {
      length_exmsg = static_cast<int>(result.exmsg.size());
      b_exmsg = new char[length_exmsg + 1];
      memcpy(b_exmsg, result.exmsg.c_str(), length_exmsg);
      b_exmsg[length_exmsg] = '\0';
    }

    // --- Xử lý Logs ---
    try {
      auto json_logs = vectorLogsHandlerToJson(log_handler);
      std::string str_logs = json_logs.dump();
      if (!str_logs.empty() && str_logs != "[]") {
        length_logs = static_cast<int>(str_logs.size());
        b_logs = new char[length_logs + 1];
        memcpy(b_logs, str_logs.c_str(), length_logs);
        b_logs[length_logs] = '\0';
      }
    } catch (const nlohmann::json::exception &e) {
      std::cerr << "[ERROR] JSON dumping failed for logs: " << e.what()
                << std::endl;
    }

    bool apply_to_cache = !isOffChain && (result.er == mvm::ExitReason::returned || result.er == mvm::ExitReason::halted);

    // Add balance
    std::vector<std::vector<uint8_t>> add_balance_change =
        gs.get_add_balance_change(apply_to_cache);
    length_add_balance_change = add_balance_change.size();
    if (length_add_balance_change > 0) {
      b_add_balance_change = new uint8_t
          *[length_add_balance_change](); // Initialize with nullptrs
      for (int i = 0; i < length_add_balance_change; ++i) {
        size_t current_size = add_balance_change[i].size();
        if (current_size == 0)
          continue;
        b_add_balance_change[i] = new uint8_t[current_size];
        memcpy(b_add_balance_change[i], add_balance_change[i].data(),
               current_size);
      }
    }

    // Sub balance
    std::vector<std::vector<uint8_t>> sub_balance_change =
        gs.get_sub_balance_change(apply_to_cache);
    length_sub_balance_change = sub_balance_change.size();
    if (length_sub_balance_change > 0) {
      b_sub_balance_change = new uint8_t *[length_sub_balance_change]();
      for (int i = 0; i < length_sub_balance_change; ++i) {
        size_t current_size = sub_balance_change[i].size();
        if (current_size == 0)
          continue;
        b_sub_balance_change[i] = new uint8_t[current_size];
        memcpy(b_sub_balance_change[i], sub_balance_change[i].data(),
               current_size);
      }
    }

    // Nonce changes
    std::vector<std::vector<uint8_t>> nonce_change = gs.get_nonce_change(apply_to_cache);
    length_nonce_change = nonce_change.size();
    if (length_nonce_change > 0) {
      b_nonce_change = new uint8_t *[length_nonce_change]();
      for (int i = 0; i < length_nonce_change; ++i) {
        size_t current_size = nonce_change[i].size();
        if (current_size == 0)
          continue;
        b_nonce_change[i] = new uint8_t[current_size];
        memcpy(b_nonce_change[i], nonce_change[i].data(), current_size);
      }
    }

    // Code changes
    std::vector<std::vector<uint8_t>> code_change = gs.get_newly_deploy(apply_to_cache);
    length_code_change = code_change.size();
    if (length_code_change > 0) {
      length_codes = new int[length_code_change];
      b_code_change = new uint8_t *[length_code_change]();
      for (size_t i = 0; i < length_code_change; ++i) {
        length_codes[i] = code_change[i].size();
        if (length_codes[i] == 0) {
          b_code_change[i] = nullptr;
          continue;
        }
        b_code_change[i] = new uint8_t[length_codes[i]];
        memcpy(b_code_change[i], code_change[i].data(), length_codes[i]);
      }
    }

    // Storage changes
    std::vector<std::vector<uint8_t>> storage_change = gs.get_storage_change(apply_to_cache);
    length_storage_change = storage_change.size();
    if (length_storage_change > 0) {
      length_storages = new int[length_storage_change];
      b_storage_change = new uint8_t *[length_storage_change]();
      for (int i = 0; i < length_storage_change; ++i) {
        length_storages[i] = storage_change[i].size();
        if (length_storages[i] == 0) {
          b_storage_change[i] = nullptr;
          continue;
        }
        b_storage_change[i] = new uint8_t[length_storages[i]];
        memcpy(b_storage_change[i], storage_change[i].data(),
               length_storages[i]);
      }
    }

    // Storage reads
    std::vector<std::vector<uint8_t>> storage_read = gs.get_storage_read(apply_to_cache);
    length_storage_read = storage_read.size();
    if (length_storage_read > 0) {
      length_storages_read = new int[length_storage_read];
      b_storage_read = new uint8_t *[length_storage_read]();
      for (int i = 0; i < length_storage_read; ++i) {
        length_storages_read[i] = storage_read[i].size();
        if (length_storages_read[i] == 0) {
          b_storage_read[i] = nullptr;
          continue;
        }
        b_storage_read[i] = new uint8_t[length_storages_read[i]];
        memcpy(b_storage_read[i], storage_read[i].data(),
               length_storages_read[i]);
      }
    }

    // We no longer retrieve full db hashes or logs via registry here.
    // Xapian changes are committed atomically in block_processor_commit.go.

    //           << std::endl;
    pendingResult = new ExecuteResult{
      b_exitReason : (char)result.er,
      b_exception : (char)result.ex,
      b_exmsg : b_exmsg,
      length_exmsg : length_exmsg,
      b_output : b_output,
      length_output : length_output,
      full_db_hash : b_full_db_hash,
      length_full_db_hash : length_full_db_hash,
      length_full_db_hashes : length_full_db_hashes,
      full_db_logs : b_full_db_logs,
      length_full_db_logs : length_full_db_logs,
      length_full_db_logs_data : length_full_db_logs_data,
      b_add_balance_change : (char **)b_add_balance_change,
      length_add_balance_change : length_add_balance_change,
      b_nonce_change : (char **)b_nonce_change,
      length_nonce_change : length_nonce_change,
      b_sub_balance_change : (char **)b_sub_balance_change,
      length_sub_balance_change : length_sub_balance_change,
      b_code_change : (char **)b_code_change,
      length_code_change : length_code_change,
      length_codes : length_codes,
      b_storage_change : (char **)b_storage_change,
      length_storage_change : length_storage_change,
      length_storages : length_storages,
      b_storage_read : (char **)b_storage_read,
      length_storage_read : length_storage_read,
      length_storages_read : length_storages_read,
      b_logs : b_logs,
      length_logs : length_logs,
      gas_used : result.gas_used
    };

    return pendingResult;
  } catch (const std::bad_alloc &e) {
    std::cerr << "[FATAL] Allocation failed during processResult: " << e.what()
              << std::endl;
    cleanupProcessResultMemoryOnError(
        b_output, b_exmsg, b_logs, b_add_balance_change,
        length_add_balance_change, b_sub_balance_change,
        length_sub_balance_change, b_nonce_change, length_nonce_change,
        b_code_change, length_code_change, length_codes, b_storage_change,
        length_storage_change, length_storages, b_full_db_hash,
        length_full_db_hash, length_full_db_hashes);
    throw;
  } catch (const std::exception &e) {
    std::cerr << "[ERROR] Standard exception during processResult: " << e.what()
              << std::endl;
    cleanupProcessResultMemoryOnError(
        b_output, b_exmsg, b_logs, b_add_balance_change,
        length_add_balance_change, b_sub_balance_change,
        length_sub_balance_change, b_nonce_change, length_nonce_change,
        b_code_change, length_code_change, length_codes, b_storage_change,
        length_storage_change, length_storages, b_full_db_hash,
        length_full_db_hash, length_full_db_hashes);
    throw;
  } catch (...) {
    std::cerr << "[ERROR] Unknown exception during processResult." << std::endl;
    cleanupProcessResultMemoryOnError(
        b_output, b_exmsg, b_logs, b_add_balance_change,
        length_add_balance_change, b_sub_balance_change,
        length_sub_balance_change, b_nonce_change, length_nonce_change,
        b_code_change, length_code_change, length_codes, b_storage_change,
        length_storage_change, length_storages, b_full_db_hash,
        length_full_db_hash, length_full_db_hashes);
    throw;
  }
}

ExecuteResult *deploy(
    // transaction data
    unsigned char *b_caller_address, unsigned char *b_contract_constructor,
    int contract_constructor_length, unsigned char *b_amount,
    unsigned long long gas_price, unsigned long long gas_limit,
    // block context data
    unsigned long long block_prevrandao, unsigned long long block_gas_limit,
    unsigned long long block_time, unsigned long long block_base_fee,
    unsigned char *b_block_number, unsigned char *b_block_coinbase,
    unsigned char *mvmId, unsigned char *b_tx_hash, bool is_debug,
    bool is_cache, bool is_off_chain,
    // TEE-packaging B1 context (see ParseTxContext) — all may be nullptr
    unsigned char *b_chain_id, unsigned char *b_blob_versioned_hashes,
    int blob_versioned_hashes_count, unsigned char *b_blob_base_fee,
    unsigned char *b_cross_chain_sender,
    unsigned char *b_cross_chain_source_id) {
  // format argument to right data type
  uint256_t caller_address =
      mvm::from_big_endian((uint8_t *)b_caller_address, 20u);

  std::vector<uint8_t> contract_constructor((uint8_t *)b_contract_constructor,
                                            (uint8_t *)b_contract_constructor +
                                                contract_constructor_length);
  uint256_t amount = mvm::from_big_endian((uint8_t *)b_amount, 32u);

  uint256_t block_number = mvm::from_big_endian((uint8_t *)b_block_number, 32u);
  uint256_t block_coinbase =
      mvm::from_big_endian((uint8_t *)b_block_coinbase, 20u);

  uint256_t tx_hash = mvm::from_big_endian((uint8_t *)b_tx_hash, 32u);

  ParsedTxContext txContext = ParseTxContext(
      b_chain_id, b_blob_versioned_hashes, blob_versioned_hashes_count,
      b_blob_base_fee, b_cross_chain_sender, b_cross_chain_source_id);

  mvm::BlockContext blockContext = CreateBlockContext(
      mvmId, block_prevrandao, block_gas_limit, block_time, block_base_fee,
      block_number, block_coinbase, tx_hash, txContext.chain_id,
      txContext.blob_versioned_hashes, txContext.blob_base_fee,
      txContext.cross_chain_sender, txContext.cross_chain_source_id);
  mvm::MyGlobalState gs(blockContext, is_cache);

  try {
    //  init env
    mvm::VectorLogHandler log_handler;
    auto from = gs.get(caller_address, nullptr);
    const auto contract_address =
        mvm::generate_address(caller_address, from.acc.get_nonce());
    std::string caller_hex = mvm::to_hex_string(caller_address);
    std::string contract_address_hex = mvm::to_hex_string(contract_address);
    // Create contract account with EMPTY code — the constructor will be
    // executed by the EVM and produce runtime code in result.output
    auto contract = gs.create(contract_address, 0u, {}, 0);

    // Pass constructor as input — in deploy mode, _Processor::run() uses
    // input as exec_code (the constructor bytecode to execute)
    std::cerr << "[DEPLOY_DEBUG] constructor_size="
              << contract_constructor.size() << " gas_limit=" << gas_limit
              << " gas_price=" << gas_price << std::endl;
    auto result = run(gs, true, caller_address, contract_address, amount,
                      gas_price, gas_limit, log_handler, contract_constructor,
                      mvmId, false, tx_hash, is_debug, is_off_chain);
    std::cerr << "[DEPLOY_DEBUG] exit_reason=" << (int)result.er
              << " exception=" << (int)result.ex
              << " gas_used=" << result.gas_used
              << " output_size=" << result.output.size()
              << " exmsg=" << result.exmsg << std::endl;
    // EIP-3541: reject deploying code starting with 0xEF, same rule as the
    // nested CREATE/CREATE2 opcodes (see create()/create2() in
    // processor.cpp) — without this, a top-level deploy transaction could
    // forge a 23-byte EIP-7702 delegation designator (0xef0100 || address)
    // at an address the deployer fully controls, impersonating an
    // authorization the account owner never signed.
    if (result.er == mvm::ExitReason::returned && !result.output.empty() &&
        result.output[0] == 0xEF) {
      result.er = mvm::ExitReason::threw;
      result.ex = mvm::Exception::Type::ErrInvalidCode;
      result.exmsg = "invalid code: must not begin with 0xef (EIP-3541)";
      result.output.clear();
    }
    if (result.er == mvm::ExitReason::returned) {
      auto code = result.output;
      gs.add_addresses_newly_deploy(contract_address, code);
      contract.acc.set_code(std::move(code));
      // update output to contract address
      std::vector<uint8_t> deployed_address(32);
      mvm::to_big_endian(contract_address, deployed_address.data());
      std::vector<uint8_t> truncated_address(20);
      memcpy(truncated_address.data(), deployed_address.data() + 12, 20);

      result.output = truncated_address;
    }

    ExecuteResult *rs = processResult(result, gs, log_handler, is_off_chain);

    return rs;
  } catch (const std::exception &e) {
    try {
      mvm::ExecResult error_result = handleException(e);

      // Dùng log_handler mới vì có thể log_handler cũ đã bị corrupt
      mvm::VectorLogHandler clean_log_handler;

      ExecuteResult *rs =
          processResult(error_result, gs, clean_log_handler, is_off_chain);
      return rs;
    } catch (...) {
      return createSafeErrorResult();
    }
  } catch (...) {
    try {
      mvm::ExecResult error_result = handleUnknownException("deploy");

      mvm::VectorLogHandler clean_log_handler;

      ExecuteResult *rs =
          processResult(error_result, gs, clean_log_handler, is_off_chain);
      return rs;
    } catch (...) {
      return createSafeErrorResult();
    }
  }
}

ExecuteResult *call(
    // transaction data
    unsigned char *b_caller_address, unsigned char *b_contract_address,
    unsigned char *b_input, int length_input, unsigned char *b_amount,
    unsigned long long gas_price, unsigned long long gas_limit,
    // block context data
    unsigned long long block_prevrandao, unsigned long long block_gas_limit,
    unsigned long long block_time, unsigned long long block_base_fee,
    unsigned char *b_block_number, unsigned char *b_block_coinbase,
    unsigned char *mvmId, bool readOnly, unsigned char *b_tx_hash,
    bool is_debug, unsigned char *b_related_addresses,
    int related_addresses_count, bool is_off_chain,
    // TEE-packaging B1 context (see ParseTxContext) — all may be nullptr
    unsigned char *b_chain_id, unsigned char *b_blob_versioned_hashes,
    int blob_versioned_hashes_count, unsigned char *b_blob_base_fee,
    unsigned char *b_cross_chain_sender,
    unsigned char *b_cross_chain_source_id) {
  // format argument to right data type
  uint256_t caller_address =
      mvm::from_big_endian((uint8_t *)b_caller_address, 20u);
  uint256_t contract_address =
      mvm::from_big_endian((uint8_t *)b_contract_address, 20u);

  std::vector<uint8_t> input((uint8_t *)b_input,
                             (uint8_t *)b_input + length_input);
  uint256_t amount = mvm::from_big_endian((uint8_t *)b_amount, 32u);

  uint256_t block_number = mvm::from_big_endian((uint8_t *)b_block_number, 32u);
  uint256_t block_coinbase =
      mvm::from_big_endian((uint8_t *)b_block_coinbase, 20u);
  uint256_t tx_hash = mvm::from_big_endian((uint8_t *)b_tx_hash, 32u);
  std::vector<mvm::Address> relatedAddresses;
  bool hasRelatedAddresses =
      (b_related_addresses != nullptr && related_addresses_count > 0);
  if (hasRelatedAddresses) {
    relatedAddresses.reserve(related_addresses_count);
    for (int i = 0; i < related_addresses_count; ++i) {
      uint256_t addr =
          mvm::from_big_endian((uint8_t *)(b_related_addresses + i * 20), 20u);
      relatedAddresses.push_back(addr);
    }
  }

  ParsedTxContext txContext = ParseTxContext(
      b_chain_id, b_blob_versioned_hashes, blob_versioned_hashes_count,
      b_blob_base_fee, b_cross_chain_sender, b_cross_chain_source_id);

  mvm::BlockContext blockContext = CreateBlockContext(
      mvmId, block_prevrandao, block_gas_limit, block_time, block_base_fee,
      block_number, block_coinbase, tx_hash, txContext.chain_id,
      txContext.blob_versioned_hashes, txContext.blob_base_fee,
      txContext.cross_chain_sender, txContext.cross_chain_source_id);

  mvm::MyGlobalState gs(blockContext, false, relatedAddresses);
  //  init env
  mvm::VectorLogHandler log_handler;

  try {
    auto result = run(gs, false, caller_address, contract_address, amount,
                      gas_price, gas_limit, log_handler, input, mvmId, readOnly,
                      tx_hash, is_debug, is_off_chain);

    // processResult có thể throw exception, cần catch
    ExecuteResult *rs = processResult(result, gs, log_handler, is_off_chain);
    return rs;
  } catch (const std::exception &e) {
    try {
      mvm::ExecResult error_result = handleException(e);
      ExecuteResult *rs =
          processResult(error_result, gs, log_handler, is_off_chain);
      return rs;
    } catch (...) {
      return createSafeErrorResult();
    }
  } catch (...) {
    try {
      mvm::ExecResult error_result = handleUnknownException("call");
      ExecuteResult *rs =
          processResult(error_result, gs, log_handler, is_off_chain);
      return rs;
    } catch (...) {
      return createSafeErrorResult();
    }
  }
}


ExecuteResult *
execute(unsigned char *b_caller_address, unsigned char *b_contract_address,
        unsigned char *b_input, int length_input, unsigned char *b_amount,
        unsigned long long gas_price, unsigned long long gas_limit,
        unsigned long long block_prevrandao, unsigned long long block_gas_limit,
        unsigned long long block_time, unsigned long long block_base_fee,
        unsigned char *b_block_number, unsigned char *b_block_coinbase,
        unsigned char *mvmId, unsigned char *b_tx_hash, bool is_debug,
        unsigned char
            *b_related_addresses, // Flatten array: addr1(20) + addr2(20) + ...
        int related_addresses_count, // Số lượng addresses
        bool is_cache,
        // TEE-packaging B1 context (see ParseTxContext) — all may be nullptr
        unsigned char *b_chain_id, unsigned char *b_blob_versioned_hashes,
        int blob_versioned_hashes_count, unsigned char *b_blob_base_fee,
        unsigned char *b_cross_chain_sender,
        unsigned char *b_cross_chain_source_id
) {

  uint256_t caller_address =
      mvm::from_big_endian((uint8_t *)b_caller_address, 20u);
  uint256_t contract_address =
      mvm::from_big_endian((uint8_t *)b_contract_address, 20u);
  std::vector<uint8_t> input((uint8_t *)b_input,
                             (uint8_t *)b_input + length_input);
  uint256_t amount = mvm::from_big_endian((uint8_t *)b_amount, 32u);
  uint256_t block_number = mvm::from_big_endian((uint8_t *)b_block_number, 32u);
  uint256_t block_coinbase =
      mvm::from_big_endian((uint8_t *)b_block_coinbase, 20u);
  uint256_t tx_hash = mvm::from_big_endian((uint8_t *)b_tx_hash, 32u);

  std::vector<mvm::Address> relatedAddresses;
  if (b_related_addresses != nullptr && related_addresses_count > 0) {
    relatedAddresses.reserve(related_addresses_count);
    for (int i = 0; i < related_addresses_count; ++i) {
      uint256_t addr =
          mvm::from_big_endian((uint8_t *)(b_related_addresses + i * 20), 20u);
      relatedAddresses.push_back(addr);
    }
  }

  ParsedTxContext txContext = ParseTxContext(
      b_chain_id, b_blob_versioned_hashes, blob_versioned_hashes_count,
      b_blob_base_fee, b_cross_chain_sender, b_cross_chain_source_id);

  mvm::BlockContext blockContext = CreateBlockContext(
      mvmId, block_prevrandao, block_gas_limit, block_time, block_base_fee,
      block_number, block_coinbase, tx_hash, txContext.chain_id,
      txContext.blob_versioned_hashes, txContext.blob_base_fee,
      txContext.cross_chain_sender, txContext.cross_chain_source_id);

  mvm::MyGlobalState gs(blockContext, is_cache, relatedAddresses);
  mvm::VectorLogHandler log_handler;

  try {
    auto result =
        run(gs, false, caller_address, contract_address, amount, gas_price,
            gas_limit, log_handler, input, mvmId, false, tx_hash, is_debug);

    // processResult có thể throw exception, cần catch
    ExecuteResult *rs = processResult(result, gs, log_handler);
    return rs;
  } catch (const std::exception &e) {
    try {
      mvm::ExecResult error_result = handleException(e);
      ExecuteResult *rs = processResult(error_result, gs, log_handler);
      return rs;
    } catch (...) {
      return createSafeErrorResult();
    }
  } catch (...) {
    try {
      mvm::ExecResult error_result = handleUnknownException("execute");
      ExecuteResult *rs = processResult(error_result, gs, log_handler);
      return rs;
    } catch (...) {
      return createSafeErrorResult();
    }
  }
}

ExecuteBatchResultC *
executeBatch(ExecuteBatchInputC *inputs, int num_inputs,
             // block context data
             unsigned long long block_prevrandao,
             unsigned long long block_gas_limit, unsigned long long block_time,
             unsigned long long block_base_fee, unsigned char *b_block_number,
             unsigned char *b_block_coinbase, unsigned char *mvmId) {
  uint256_t block_number = mvm::from_big_endian((uint8_t *)b_block_number, 32u);
  uint256_t block_coinbase =
      mvm::from_big_endian((uint8_t *)b_block_coinbase, 20u);

  mvm::BlockContext blockContext =
      CreateBlockContext(mvmId, block_prevrandao, block_gas_limit, block_time,
                         block_base_fee, block_number, block_coinbase);

  std::vector<mvm::Address> relatedAddresses;
  for (int i = 0; i < num_inputs; ++i) {
    if (inputs[i].b_related_addresses != nullptr &&
        inputs[i].related_addresses_count > 0) {
      for (int j = 0; j < inputs[i].related_addresses_count; ++j) {
        uint256_t addr = mvm::from_big_endian(
            (uint8_t *)(inputs[i].b_related_addresses + j * 20), 20u);
        relatedAddresses.push_back(addr);
      }
    }
  }

  mvm::MyGlobalState gs(blockContext, false, relatedAddresses);

  ExecuteBatchResultC *batch_rs =
      (ExecuteBatchResultC *)malloc(sizeof(ExecuteBatchResultC));
  batch_rs->num_results = num_inputs;
  batch_rs->results =
      (ExecuteResult **)malloc(num_inputs * sizeof(ExecuteResult *));

  for (int i = 0; i < num_inputs; ++i) {
    uint256_t caller_address =
        mvm::from_big_endian((uint8_t *)inputs[i].b_caller_address, 20u);
    uint256_t contract_address =
        mvm::from_big_endian((uint8_t *)inputs[i].b_contract_address, 20u);
    std::vector<uint8_t> input((uint8_t *)inputs[i].b_input,
                               (uint8_t *)inputs[i].b_input +
                                   inputs[i].length_input);
    uint256_t amount = mvm::from_big_endian((uint8_t *)inputs[i].b_amount, 32u);
    uint256_t tx_hash =
        mvm::from_big_endian((uint8_t *)inputs[i].b_tx_hash, 32u);

    mvm::VectorLogHandler log_handler;

    try {
      auto result = run(gs, false, caller_address, contract_address, amount,
                        inputs[i].gas_price, inputs[i].gas_limit, log_handler,
                        input, mvmId, false, tx_hash, inputs[i].is_debug);
      batch_rs->results[i] = processResult(result, gs, log_handler);
    } catch (const std::exception &e) {
      mvm::ExecResult error_result = handleException(e);
      batch_rs->results[i] = processResult(error_result, gs, log_handler);
    } catch (...) {
      mvm::ExecResult error_result = handleUnknownException("executeBatch");
      batch_rs->results[i] = processResult(error_result, gs, log_handler);
    }

    // Clear difference arrays in gs to avoid leaking state changes into the
    // next ExecuteResult
    gs.clear_differences();
  }

  return batch_rs;
}

void freeBatchResult(ExecuteBatchResultC *batch) {
  if (batch == nullptr)
    return;
  for (int i = 0; i < batch->num_results; ++i) {
    if (batch->results[i] != nullptr) {
      freeResult(batch->results[i]);
    }
  }
  free(batch->results);
  free(batch);
}

ExecuteResult *processNativeMintBurn(
    unsigned char *b_from, unsigned char *b_to, unsigned char *b_amount,
    unsigned long long operation_type, // 0: mint, 1: burn
    unsigned long long gas_price, unsigned long long gas_limit,
    unsigned long long block_prevrandao, unsigned long long block_gas_limit,
    unsigned long long block_time, unsigned long long block_base_fee,
    unsigned char *b_block_number, unsigned char *b_block_coinbase,
    unsigned char *mvmId,
    bool is_cache) {
  uint256_t from = mvm::from_big_endian((uint8_t *)b_from, 20u);
  uint256_t to = mvm::from_big_endian((uint8_t *)b_to, 20u);
  uint256_t amount = mvm::from_big_endian((uint8_t *)b_amount, 32u);
  uint256_t block_number = mvm::from_big_endian((uint8_t *)b_block_number, 32u);
  uint256_t block_coinbase =
      mvm::from_big_endian((uint8_t *)b_block_coinbase, 20u);

  mvm::BlockContext blockContext =
      CreateBlockContext(mvmId, block_prevrandao, block_gas_limit, block_time,
                         block_base_fee, block_number, block_coinbase);

  mvm::MyGlobalState gs(blockContext, is_cache);
  mvm::VectorLogHandler log_handler;
  try {
    if (operation_type == 0) { // MINT
      // Mint: chỉ cộng tiền vào to address (không trừ từ đâu)
      auto toAc = gs.get(to, nullptr);
      toAc.acc.set_balance(toAc.acc.get_balance() + amount);
      gs.add_addresses_add_balance_change(to, amount);
    } else if (operation_type == 1) { // BURN
      // Burn: trừ tiền từ from address
      auto fromAc = gs.get(from, nullptr);

      // Kiểm tra balance đủ để burn
      if (fromAc.acc.get_balance() < amount) {
        throw std::runtime_error("insufficient balance for burn");
      }

      fromAc.acc.set_balance(fromAc.acc.get_balance() - amount);
      gs.add_addresses_sub_balance_change(from, amount);

      fromAc.acc.increment_nonce();
      auto new_nonce = fromAc.acc.get_nonce();
      gs.set_addresses_nonce_change(from, new_nonce);
    }

    mvm::ExecResult result;
    result.er = mvm::ExitReason::returned;
    result.gas_used = gas_limit;

    ExecuteResult *rs = processResult(result, gs, log_handler);
    return rs;
  } catch (const std::exception &e) {
    try {
      mvm::ExecResult error_result = handleException(e);
      ExecuteResult *rs = processResult(error_result, gs, log_handler);
      return rs;
    } catch (...) {
      return createSafeErrorResult();
    }
  } catch (...) {
    try {
      mvm::ExecResult error_result =
          handleUnknownException("handleTokenOperation");
      ExecuteResult *rs = processResult(error_result, gs, log_handler);
      return rs;
    } catch (...) {
      return createSafeErrorResult();
    }
  }
}

ExecuteResult *
sendNative(unsigned char *b_from, unsigned char *b_to, unsigned char *b_amount,
           unsigned long long gas_price, unsigned long long gas_limit,
           unsigned long long block_prevrandao,
           unsigned long long block_gas_limit, unsigned long long block_time,
           unsigned long long block_base_fee, unsigned char *b_block_number,
           unsigned char *b_block_coinbase, unsigned char *mvmId,
           bool is_cache) {

  uint256_t from = mvm::from_big_endian((uint8_t *)b_from, 20u);
  uint256_t to = mvm::from_big_endian((uint8_t *)b_to, 20u);
  uint256_t amount = mvm::from_big_endian((uint8_t *)b_amount, 32u);
  uint256_t block_number = mvm::from_big_endian((uint8_t *)b_block_number, 32u);
  uint256_t block_coinbase =
      mvm::from_big_endian((uint8_t *)b_block_coinbase, 20u);

  mvm::BlockContext blockContext =
      CreateBlockContext(mvmId, block_prevrandao, block_gas_limit, block_time,
                         block_base_fee, block_number, block_coinbase);

  mvm::MyGlobalState gs(blockContext, is_cache);
  mvm::VectorLogHandler log_handler;

  try {
    auto fromAc = gs.get(from, nullptr);
    auto toAc = gs.get(to, nullptr);

    if (fromAc.acc.get_balance() < amount) {
      throw std::runtime_error("insufficient balance for sendNative");
    }

    fromAc.acc.set_balance(fromAc.acc.get_balance() - amount);
    toAc.acc.set_balance(toAc.acc.get_balance() + amount);
    gs.add_addresses_sub_balance_change(from, amount);
    gs.add_addresses_add_balance_change(to, amount);

    // 🔒 NONCE-FIX: Use State cache as authority for nonce increment
    incrementSenderNonce(gs, from, fromAc);

    mvm::ExecResult result;
    result.er = mvm::ExitReason::returned;
    result.gas_used = gas_limit;

    ExecuteResult *rs = processResult(result, gs, log_handler);
    return rs;
  } catch (const std::exception &e) {
    try {

      mvm::ExecResult error_result = handleException(e);
      ExecuteResult *rs = processResult(error_result, gs, log_handler);
      return rs;
    } catch (...) {
      return createSafeErrorResult();
    }
  } catch (...) {
    try {
      mvm::ExecResult error_result = handleUnknownException("sendNative");
      ExecuteResult *rs = processResult(error_result, gs, log_handler);
      return rs;
    } catch (...) {
      return createSafeErrorResult();
    }
  }
}

ExecuteResult *
noncePlusOne(unsigned char *b_from, unsigned long long gas_price,
             unsigned long long gas_limit, unsigned long long block_prevrandao,
             unsigned long long block_gas_limit, unsigned long long block_time,
             unsigned long long block_base_fee, unsigned char *b_block_number,
             unsigned char *b_block_coinbase, unsigned char *mvmId,
             bool is_cache) {

  uint256_t from = mvm::from_big_endian((uint8_t *)b_from, 20u);
  uint256_t block_number = mvm::from_big_endian((uint8_t *)b_block_number, 32u);
  uint256_t block_coinbase =
      mvm::from_big_endian((uint8_t *)b_block_coinbase, 20u);

  mvm::BlockContext blockContext =
      CreateBlockContext(mvmId, block_prevrandao, block_gas_limit, block_time,
                         block_base_fee, block_number, block_coinbase);

  mvm::MyGlobalState gs(blockContext, is_cache);
  mvm::VectorLogHandler log_handler;

  try {
    auto fromAc = gs.get(from, nullptr);

    // 🔒 NONCE-FIX: Use State cache as authority for nonce increment
    incrementSenderNonce(gs, from, fromAc);

    mvm::ExecResult result;
    result.er = mvm::ExitReason::returned;
    result.gas_used = 50000;

    ExecuteResult *rs = processResult(result, gs, log_handler);
    return rs;
  } catch (const std::exception &e) {
    try {
      mvm::ExecResult error_result = handleException(e);
      ExecuteResult *rs = processResult(error_result, gs, log_handler);
      return rs;
    } catch (...) {
      return createSafeErrorResult();
    }
  } catch (...) {
    try {
      mvm::ExecResult error_result = handleUnknownException("noncePlusOne");
      ExecuteResult *rs = processResult(error_result, gs, log_handler);
      return rs;
    } catch (...) {
      return createSafeErrorResult();
    }
  }
}

void freeResult(ExecuteResult *pendingResult) {
  if (!pendingResult)
    return;

  delete[] pendingResult->b_exmsg;
  delete[] pendingResult->b_output;

  if (pendingResult->full_db_hash) {
    for (int i = 0; i < pendingResult->length_full_db_hash; ++i) {
      delete[] pendingResult->full_db_hash[i];
    }
    delete[] pendingResult->full_db_hash;
  }
  delete[] pendingResult->length_full_db_hashes;

  if (pendingResult->full_db_logs) {
    for (int i = 0; i < pendingResult->length_full_db_logs; ++i) {
      delete[] pendingResult->full_db_logs[i];
    }
    delete[] pendingResult->full_db_logs;
  }
  delete[] pendingResult->length_full_db_logs_data;

  if (pendingResult->b_add_balance_change) {
    for (int i = 0; i < pendingResult->length_add_balance_change; ++i) {
      delete[] pendingResult->b_add_balance_change[i];
    }
    delete[] pendingResult->b_add_balance_change;
  }

  if (pendingResult->b_sub_balance_change) {
    for (int i = 0; i < pendingResult->length_sub_balance_change; ++i) {
      delete[] pendingResult->b_sub_balance_change[i];
    }
    delete[] pendingResult->b_sub_balance_change;
  }

  if (pendingResult->b_nonce_change) {
    for (int i = 0; i < pendingResult->length_nonce_change; ++i) {
      delete[] pendingResult->b_nonce_change[i];
    }
    delete[] pendingResult->b_nonce_change;
  }

  if (pendingResult->b_code_change) {
    for (int i = 0; i < pendingResult->length_code_change; ++i) {
      delete[] pendingResult->b_code_change[i];
    }
    delete[] pendingResult->b_code_change;
  }
  delete[] pendingResult->length_codes;

  if (pendingResult->b_storage_change) {
    for (int i = 0; i < pendingResult->length_storage_change; ++i) {
      delete[] pendingResult->b_storage_change[i];
    }
    delete[] pendingResult->b_storage_change;
  }
  delete[] pendingResult->length_storages;

  if (pendingResult->b_storage_read) {
    for (int i = 0; i < pendingResult->length_storage_read; ++i) {
      delete[] pendingResult->b_storage_read[i];
    }
    delete[] pendingResult->b_storage_read;
  }
  delete[] pendingResult->length_storages_read;

  delete[] pendingResult->b_logs;

  delete pendingResult;
}

ExecuteResult *testMemLeak() {
  char *b_output = reinterpret_cast<char *>(malloc(32 * sizeof(char)));
  int length_output = static_cast<int>(32);

  int length_add_balance_change = 0;
  int length_sub_balance_change = 0;
  int length_code_change = 0;

  mvm::MyGlobalState gs;
  std::vector<std::vector<uint8_t>> storage_change;

  int length_storage_change = 100;
  int *length_storages = new int[length_storage_change];
  uint8_t **b_storage_change = new uint8_t *[length_storage_change];
  for (int i = 0; i < length_storage_change; ++i) {
    int allocSize = 64 * 10 + 32;
    length_storages[i] = allocSize;
    b_storage_change[i] = new uint8_t[allocSize];
    for (int u = 0; u < allocSize; u++) {
      b_storage_change[i][u] = i;
    }
  }

  std::string str_logs = "{}";

  ExecuteResult *pendingResult = new ExecuteResult{
    b_exitReason : (char)1,
    b_exception : (char)1,
    b_exmsg : new char[0],
    length_exmsg : 0,

    b_output : b_output,
    length_output : length_output,
    length_add_balance_change : length_add_balance_change,
    length_sub_balance_change : length_sub_balance_change,
    length_code_change : length_code_change,
    b_storage_change : (char **)b_storage_change,
    length_storage_change : length_storage_change,
    length_storages : length_storages,

    b_logs : (char *)malloc((int)str_logs.size() * sizeof(char)),
    length_logs : (int)str_logs.size(),

    gas_used : 0
  };

  memcpy(pendingResult->b_logs, str_logs.c_str(), str_logs.size());

  return pendingResult;
}

void testMemLeakGS(int total_address, unsigned char *b_contract_addresses) {
  mvm::MyGlobalState gs;
  for (int i = 0; i < total_address; i++) {
    uint256_t contract_address =
        mvm::from_big_endian(b_contract_addresses + (i * 20), 20u);
    gs.get(contract_address);
  }
}

// --- Triển khai hàm C được export ---
int ReplayFullDbLogs(LogReplayEntryC *entries, int num_entries) {
  if (entries == nullptr || num_entries <= 0) {
    return 0;
  }

  int successful_replays_count = 0;
  bool overall_operation_success = true;

  for (int i = 0; i < num_entries; ++i) {
    LogReplayEntryC &current_entry = entries[i];

    if (current_entry.address_ptr == nullptr ||
        current_entry.address_len != 20) {
      overall_operation_success = false;
      continue;
    }
    if (current_entry.log_data_ptr == nullptr ||
        current_entry.log_data_len <= 0) {
      overall_operation_success = false;
      continue;
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
      overall_operation_success = false;
      continue;
    }
    XapianLog::ComprehensiveLog &comp_log = *deserialized_log_opt;

    if (comp_log.db_name.empty()) {
      std::cerr << "[C++ Replay Error] DB db_name in log is empty for address "
                << addr_hex << ". Cannot determine target manager."
                << std::endl;
      std::cerr << "  - Entry index: " << i << std::endl;
      overall_operation_success = false;
      continue;
    }

    std::shared_ptr<XapianManager> target_manager = nullptr;
    std::filesystem::path replay_db_path = mvm::createFullPath(address, comp_log.db_name);

    // FORK-FIX: Ensure directory exists before opening Xapian DB.
    // The proposer creates this directory in XAPIAN_GET_OR_CREATE_DB handler,
    // but during replay on validators, getInstance() was called directly
    // without creating the directory first → Xapian::DB_CREATE_OR_OPEN throws
    // because it cannot create files in a non-existent directory.
    if (!std::filesystem::exists(replay_db_path)) {
      std::error_code ec;
      std::filesystem::create_directories(replay_db_path, ec);
      if (ec) {
        std::cerr << "[C++ Replay FATAL] Cannot create directory for db_name='"
                  << comp_log.db_name << "' path=" << replay_db_path.string()
                  << " error=" << ec.message() << std::endl;
        overall_operation_success = false;
        continue;
      }
      std::cerr << "[C++ Replay] Created directory: " << replay_db_path.string() << std::endl;
    }

    std::cerr << "[C++ Replay] Entry " << i << "/" << num_entries
              << ": db_name='" << comp_log.db_name
              << "' address=" << addr_hex
              << " db_path=" << replay_db_path.string()
              << " log_entries=" << comp_log.xapian_doc_logs.size()
              << std::endl;
    try {
      target_manager =
          XapianManager::getInstance(comp_log.db_name, address, true);
      std::cerr << "[C++ Replay] Entry " << i << ": getInstance OK" << std::endl;
    } catch (const Xapian::DatabaseLockError &e) {
      std::cerr << "[C++ Replay FATAL] DatabaseLockError for db_name '"
                << comp_log.db_name << "' addr=" << addr_hex
                << " path=" << replay_db_path.string()
                << ": " << e.get_msg() << std::endl;
      overall_operation_success = false;
      continue;
    } catch (const Xapian::Error &e) {
      std::cerr << "[C++ Replay FATAL] Xapian::Error for db_name '"
                << comp_log.db_name << "' addr=" << addr_hex
                << " path=" << replay_db_path.string()
                << " type=" << e.get_type()
                << " msg=" << e.get_msg()
                << " context=" << e.get_context() << std::endl;
      overall_operation_success = false;
      continue;
    } catch (const std::exception &e) {
      std::cerr << "[C++ Replay FATAL] std::exception for db_name '"
                << comp_log.db_name << "' addr=" << addr_hex
                << " path=" << replay_db_path.string()
                << ": " << e.what() << std::endl;
      overall_operation_success = false;
      continue;
    } catch (...) {
      std::cerr << "[C++ Replay FATAL] Unknown exception for db_name '"
                << comp_log.db_name << "' addr=" << addr_hex
                << " path=" << replay_db_path.string() << std::endl;
      overall_operation_success = false;
      continue;
    }

    if (!target_manager) {
      std::cerr << "[C++ Replay Error] Failed to get/create XapianManager for "
                   "db_name '"
                << comp_log.db_name << "' and address " << addr_hex
                << " after reset. Skipping replay for this entry." << std::endl;
      std::cerr << "  - Entry index: " << i << std::endl;
      overall_operation_success = false;
      continue;
    }

    bool replay_success_for_this_entry = true;

    if (!comp_log.xapian_doc_logs.empty()) {
      std::cerr << "[C++ Replay] Entry " << i << ": replaying "
                << comp_log.xapian_doc_logs.size() << " log operations..." << std::endl;
      if (!target_manager->replay_log(comp_log.xapian_doc_logs)) {
        std::cerr << "[C++ Replay FATAL] replay_log FAILED for entry " << i
                  << " addr=" << addr_hex
                  << " db_name='" << comp_log.db_name << "'"
                  << " ops=" << comp_log.xapian_doc_logs.size() << std::endl;
        replay_success_for_this_entry = false;
      } else {
        std::cerr << "[C++ Replay] Entry " << i << ": replay_log OK" << std::endl;
      }
    }

    if (replay_success_for_this_entry) {
      try {
        if (target_manager->saveAllAndCommit()) {
          successful_replays_count++;
          std::cerr << "[C++ Replay] Entry " << i << ": saveAllAndCommit OK" << std::endl;
        } else {
          std::cerr << "[C++ Replay FATAL] saveAllAndCommit returned false for addr="
                    << addr_hex << " db_name='" << comp_log.db_name << "'" << std::endl;
          overall_operation_success = false;
        }
      } catch (const Xapian::Error &e) {
        std::cerr << "[C++ Replay FATAL] Xapian::Error during saveAllAndCommit for addr="
                  << addr_hex << " type=" << e.get_type()
                  << " msg=" << e.get_msg() << std::endl;
        overall_operation_success = false;
      } catch (const std::exception &e) {
        std::cerr << "[C++ Replay FATAL] Exception during saveAllAndCommit for addr="
                  << addr_hex << ": " << e.what() << std::endl;
        overall_operation_success = false;
      } catch (...) {
        std::cerr << "[C++ Replay FATAL] Unknown exception during saveAllAndCommit for addr="
                  << addr_hex << std::endl;
        overall_operation_success = false;
      }
    } else {
      overall_operation_success = false;
    }
  }
  return overall_operation_success ? 1 : 0;
}

void clearAllStateInstances() { State::clearAllInstances(); }

// 🔒 NONCE-FIX: Update C++ State cache nonce from Go side.
// Called when Go changes nonce directly (BLS SetPublicKey, setAccountType)
// to keep C++ cache in sync.
void updateStateNonce(unsigned char *b_address, unsigned long long nonce) {
  uint256_t address = mvm::from_big_endian((uint8_t *)b_address, 20u);
  if (State::instanceExists(address)) {
    State::getInstance(address)->setNonce(uint256_t(nonce));
  }
}

// 🔒 BALANCE-FIX: Update C++ State cache balance from Go side.
void updateStateBalance(unsigned char *b_address, unsigned char *b_balance) {
  uint256_t address = mvm::from_big_endian((uint8_t *)b_address, 20u);
  if (State::instanceExists(address)) {
    uint256_t balance = mvm::from_big_endian((uint8_t *)b_balance, 32u);
    State::getInstance(address)->setBalance(balance);
  }
}
extern "C" void MVM_commitAllXapian() {
  XapianManager::commitAllInstances();
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
    if (b_tx_hashes && count > 0) {
        for (int i = 0; i < count; i++) {
            uint256_t txHash = mvm::from_big_endian((uint8_t *)(b_tx_hashes + i * 32), 32u);
            registry.clearBufferForTxHash(&txHash);
        }
    }
}

extern "C" void commit_xapian_tx_buffer_batch(unsigned char *b_tx_hashes, int count) {
    if (b_tx_hashes && count > 0) {
        for (int i = 0; i < count; i++) {
            uint256_t txHash = mvm::from_big_endian((uint8_t *)(b_tx_hashes + i * 32), 32u);
            registry.commitBufferForTxHash(&txHash);
        }
    }
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
            std::lock_guard<std::shared_mutex> lock(manager->changes_mutex);
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
