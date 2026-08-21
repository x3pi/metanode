#include "my_global_state.h"
#include "mvm/exception.h"
#include "mvm/gas.h"
#include "mvm/util.h"
#include "mvm_linker.hpp"
#include "state.h"
#include <algorithm>
#include <iostream>

struct GlobalStateGet_return {
  int status;
  unsigned char *balance_p;
  unsigned char *nonce;
  unsigned char *code_p;
  int code_length;
};

std::string addressToHex(const uint256_t &address) {
  std::stringstream ss;
  ss << "0x";

  // Chuyển đổi con trỏ uint256_t thành con trỏ uint8_t
  const uint8_t *bytes = reinterpret_cast<const uint8_t *>(&address);

  // Lấy 20 byte đầu tiên nếu đã đảo ngược
  for (int i = 0; i < 20; ++i) {
    ss << std::hex << std::setw(2) << std::setfill('0')
       << static_cast<int>(bytes[i]);
  }

  return ss.str();
}

namespace mvm {
using ET = Exception::Type;
void MyGlobalState::remove(const Address &addr) { accounts.erase(addr); }

bool MyGlobalState::is_cache() { return isCache; }

AccountState MyGlobalState::get(const Address &addr, GasTracker *gas_tracker) {

  uint8_t b_address[32];
  mvm::to_big_endian(addr, b_address);
  const BlockContext &blockContext = get_block_context();

  // Check cache trước
  const auto acc = accounts.find(addr);
  if (acc != accounts.cend()) {
    std::string addr_hex = mvm::address_to_hex_string(addr);
    uint256_t cached_balance = acc->second.first.get_balance();
    // Kiểm tra State singleton nếu isCache = true
    if (gas_tracker != nullptr) {
      gas_tracker->add_gas_used(getTouchedAddressGasCost());
    }
    return acc->second;
  }
  if (isCache && !isAddressAllowed(addr)) {
    std::string addr_hex = mvm::address_to_hex_string(addr);
    std::string error_msg = "[MY_GLOBAL_STATE] ❌ FORBIDDEN ACCESS - Address " +
                            addr_hex + " not in allowed addresses list";
    std::cerr << error_msg << std::endl;
    // ✅ Exception message phải match với console log để client nhận được
    // message chi tiết
    throw Exception(ET::addressNotInRelated, error_msg);
  }

  if (isCache && State::instanceExists(addr)) {
    auto gBalance = State::getInstance(addr)->getBalance();
    auto gNonce = State::getInstance(addr)->getNonce();
    auto gcode = State::getInstance(addr)->getCode();
    insert({MyAccount(addr, gBalance, gcode, gNonce),
            MyStorage(addr, blockContext.mvmId, isCache)});
    const auto acc = accounts.find(addr);
    if (gas_tracker != nullptr) {
      gas_tracker->add_gas_used(getUnTouchedAddressGasCost());
    }
    return acc->second;
  }
  // [TZLLM_TRACE] bracketed tracing (2026-08-21) -- see sendNative()'s own
  // comment in mvm_linker.cpp for context. `to` in the hanging SEND_NATIVE
  // repro is always a fresh/never-before-seen address, so it takes THIS
  // reverse-call branch (not the isCache-hit branches above) -- bracket
  // it specifically since that's the one path sendNative()'s 2nd gs.get()
  // actually exercises in the repro.
  fprintf(stderr, "[TZLLM_TRACE] MyGlobalState::get: before GlobalStateGet reverse call\n");
  fflush(stderr);
  // Gọi callback để lấy data từ Go
  GlobalStateGet_return accountQueryData =
      GlobalStateGet(blockContext.mvmId, b_address + 12);
  fprintf(stderr, "[TZLLM_TRACE] MyGlobalState::get: after GlobalStateGet reverse call, status=%d\n",
      (int)accountQueryData.status);
  fflush(stderr);

  if (accountQueryData.status == 3) {
    throw Exception(ET::ErrExecutionReverted, "Block-STM: Estimate Hit (Suspend)");
  }
  if (accountQueryData.status == 2) {
    throw Exception(ET::addressNotInRelated,
                    "Address not in related addresses: " +
                        mvm::address_to_hex_string(addr));
  }
  if (accountQueryData.status == 1) {
    uint256_t balance = from_big_endian(accountQueryData.balance_p, 32u);
    std::vector<uint8_t> code(accountQueryData.code_p,
                              accountQueryData.code_p +
                                  accountQueryData.code_length);
    uint256_t nonce = from_big_endian(accountQueryData.nonce, 32u);

    insert({MyAccount(addr, balance, code, nonce),
            MyStorage(addr, blockContext.mvmId, isCache)});
    if (accountQueryData.balance_p) free(accountQueryData.balance_p);
    if (accountQueryData.code_p) free(accountQueryData.code_p);
    if (accountQueryData.nonce) free(accountQueryData.nonce);
    const auto acc = accounts.find(addr);
    if (gas_tracker != nullptr) {
      gas_tracker->add_gas_used(getUnTouchedAddressGasCost());
    }
    ClearProcessingPointers(blockContext.mvmId);

    if (isCache) {
      auto state = State::getInstance(addr);
      state->setBalance(balance);
      state->setNonce(nonce);
      state->setCode(code);
    }
    return acc->second;
  }
  fprintf(stderr, "[TZLLM_TRACE] MyGlobalState::get: status=0 branch, before create()\n");
  fflush(stderr);
  AccountState fresh = create(addr, 0, {}, 0);
  fprintf(stderr, "[TZLLM_TRACE] MyGlobalState::get: status=0 branch, after create(), returning\n");
  fflush(stderr);
  return fresh;
}

AccountState MyGlobalState::getUpdate(const Address &addr) {
  if (isCache && !isAddressAllowed(addr)) {
    throw Exception(ET::addressNotInRelated,
                    "Address not in related addresses: " +
                        mvm::address_to_hex_string(addr));
  }
  uint8_t b_address[32];
  mvm::to_big_endian(addr, b_address);
  const BlockContext &blockContext = get_block_context(); // Thêm dòng này

  GlobalStateGet_return accountQueryData =
      GlobalStateGet(blockContext.mvmId, b_address + 12);

  if (accountQueryData.status == 3) {
    throw Exception(ET::ErrExecutionReverted, "Block-STM: Estimate Hit (Suspend)");
  }
  if (accountQueryData.status == 2) {
    throw Exception(ET::addressNotInRelated,
                    "Address not in related addresses: " +
                        mvm::address_to_hex_string(addr));
  }
  if (accountQueryData.status == 1) {

    uint256_t balance = from_big_endian(accountQueryData.balance_p, 32u);
    std::vector<uint8_t> code(accountQueryData.code_p,
                              accountQueryData.code_p +
                                  accountQueryData.code_length);
    uint256_t nonce = from_big_endian(accountQueryData.nonce, 32u);

    insert({MyAccount(addr, balance, code, nonce),
            MyStorage(addr, blockContext.mvmId, isCache)});
    if (accountQueryData.balance_p) free(accountQueryData.balance_p);
    if (accountQueryData.code_p) free(accountQueryData.code_p);
    if (accountQueryData.nonce) free(accountQueryData.nonce);
    const auto acc = accounts.find(addr);
    ClearProcessingPointers(blockContext.mvmId);

    if (isCache) {
      auto state = State::getInstance(addr);
      state->setBalance(balance);
      state->setNonce(nonce);
      state->setCode(code);
    }
    return acc->second;
  }
  return create(addr, 0, {}, 0);
};

AccountState MyGlobalState::get(const Address &addr, GasTracker *gas_tracker,
                                const uint256_t &nonce) {
  uint8_t b_address[32];
  mvm::to_big_endian(addr, b_address);
  const BlockContext &blockContext = get_block_context(); // Thêm dòng này
  const auto acc = accounts.find(addr);
  if (acc != accounts.cend()) {
    if (gas_tracker != nullptr) {
      gas_tracker->add_gas_used(getTouchedAddressGasCost());
    }
    return acc->second;
  }
  if (isCache && !isAddressAllowed(addr)) {
    throw Exception(ET::addressNotInRelated,
                    "Address not in related addresses: " +
                        mvm::address_to_hex_string(addr));
  }
  if (isCache && State::instanceExists(addr)) {
    auto gBalance = State::getInstance(addr)->getBalance();
    auto gNonce = State::getInstance(addr)->getNonce();
    auto gcode = State::getInstance(addr)->getCode();

    insert({MyAccount(addr, gBalance, gcode, gNonce),
            MyStorage(addr, blockContext.mvmId, isCache)});
    const auto acc = accounts.find(addr);
    if (gas_tracker != nullptr) {
      gas_tracker->add_gas_used(getUnTouchedAddressGasCost());
    }
    return acc->second;
  }

  GlobalStateGet_return accountQueryData =
      GlobalStateGet(blockContext.mvmId, b_address + 12);

  if (accountQueryData.status == 3) {
    throw Exception(ET::ErrExecutionReverted, "Block-STM: Estimate Hit (Suspend)");
  }
  if (accountQueryData.status == 2) {
    throw Exception(ET::addressNotInRelated,
                    "Address not in related addresses: " +
                        mvm::address_to_hex_string(addr));
  }
  if (accountQueryData.status == 1) {
    uint256_t balance = from_big_endian(accountQueryData.balance_p, 32u);
    std::vector<uint8_t> code(accountQueryData.code_p,
                              accountQueryData.code_p +
                                  accountQueryData.code_length);
    uint256_t nonce = from_big_endian(accountQueryData.nonce, 32u);

    insert({MyAccount(addr, balance, code, nonce),
            MyStorage(addr, blockContext.mvmId, isCache)});
    if (accountQueryData.balance_p) free(accountQueryData.balance_p);
    if (accountQueryData.code_p) free(accountQueryData.code_p);
    if (accountQueryData.nonce) free(accountQueryData.nonce);
    const auto acc = accounts.find(addr);
    if (gas_tracker != nullptr) {
      gas_tracker->add_gas_used(getUnTouchedAddressGasCost());
    }
    ClearProcessingPointers(blockContext.mvmId);

    if (isCache) {
      auto state = State::getInstance(addr);
      state->setBalance(balance);
      state->setNonce(nonce);
      state->setCode(code);
    }
    return acc->second;
  }
  return create(addr, 0, {}, nonce);
};

AccountState MyGlobalState::create(const Address &addr,
                                   const uint256_t &balance, const Code &code,
                                   const uint256_t &nonce) {
  insert({MyAccount(addr, balance, code, nonce),
          MyStorage(addr, blockContext.mvmId, isCache)});

  return get(addr, nullptr);
}

bool MyGlobalState::exists(const Address &addr) {
  return accounts.find(addr) != accounts.end();
}

size_t MyGlobalState::num_accounts() { return accounts.size(); }

const BlockContext &MyGlobalState::get_block_context() { return blockContext; }

void MyGlobalState::set_block_context(const BlockContext &bc) {
  blockContext = bc;
}

// TEE-packaging B1, last of the 6 callbacks (note/tee_core_packaging_plan.md):
// reads straight from blockContext.block_hashes instead of calling back
// into Go. Also fixes a latent bug in the pre-B1 callback path: it never
// checked GetBlockHash's `success` flag before calling bytes_to_uint256 on
// its result pointer — an out-of-range query (success=false, data_p=null)
// was undefined behavior, not a clean 0. EVM semantics (and the fix): a
// queried block that is >= the current block, or further back than what
// block_hashes holds (populated only up to 256 entries, see
// HasBlockhashOpcode/mvm_api.go and block_context.h), resolves to 0.
uint256_t MyGlobalState::get_block_hash(int blockNumber) {
  if (blockNumber < 0) {
    return uint256_t{};
  }
  uint256_t queried = static_cast<uint64_t>(blockNumber);
  uint256_t current = blockContext.number;
  if (queried >= current) {
    return uint256_t{};
  }
  uint256_t distance = current - queried; // >= 1
  if (distance > blockContext.block_hashes.size()) {
    return uint256_t{};
  }
  size_t idx = static_cast<size_t>(static_cast<uint64_t>(distance)) - 1;
  return blockContext.block_hashes[idx];
}

// --- TEE-packaging B1 (note/tee_core_packaging_plan.md): these 5 getters
// used to call back into Go mid-execution (GetChainId/GetCrossChainSender/
// GetCrossChainSourceId/GetBlobHash/GetBlobBaseFee). They now read straight
// from blockContext, populated once by deploy/call/execute before the
// interpreter starts — see block_context.h's doc comment on why. The old
// //export Go functions still exist (pkg/mvm/mvm_api.go) but are no longer
// called from here; left in place rather than deleted, since Go still
// declares them and removing them is a separate, lower-value cleanup with
// no correctness upside on its own.

uint256_t MyGlobalState::get_chain_id() {
  return blockContext.chain_id;
}

std::vector<uint8_t> MyGlobalState::get_cross_chain_sender() {
  // Callers (cross_chain_precompile.cpp) require ABI-encoded address: 32
  // bytes, 12 zero bytes then the 20-byte address — matches the pre-B1 Go
  // callback's encoding exactly. blockContext.cross_chain_sender itself
  // holds the plain 20-byte address (or is empty when inactive); the
  // padding happens here, at the point of use, not in the context struct.
  if (blockContext.cross_chain_sender.empty()) {
    return {};
  }
  std::vector<uint8_t> result(32, 0);
  size_t n = blockContext.cross_chain_sender.size();
  size_t copyLen = n < 20 ? n : 20;
  std::copy(blockContext.cross_chain_sender.begin(),
            blockContext.cross_chain_sender.begin() + copyLen,
            result.begin() + (32 - copyLen));
  return result;
}

std::vector<uint8_t> MyGlobalState::get_cross_chain_source_id() {
  // Callers (cross_chain_precompile.cpp) require exactly 32 bytes,
  // big-endian — matches the pre-B1 Go callback's uint256.Bytes32()
  // encoding exactly (source_bin.size() == 32 is checked explicitly at the
  // consuming end, so this length is load-bearing, not cosmetic).
  if (blockContext.cross_chain_sender.empty()) {
    return {};
  }
  std::vector<uint8_t> result(32, 0);
  uint64_t sourceId = blockContext.cross_chain_source_id;
  for (int i = 31; i >= 24; --i) {
    result[i] = static_cast<uint8_t>(sourceId & 0xff);
    sourceId >>= 8;
  }
  return result;
}

uint256_t MyGlobalState::get_blob_hash(uint64_t index) {
  // EIP-4844: an out-of-range index resolves to 0, same as "no blob context
  // at all" — both are simply "index not found" here.
  if (index >= blockContext.blob_versioned_hashes.size()) {
    return uint256_t{};
  }
  return blockContext.blob_versioned_hashes[index];
}

uint256_t MyGlobalState::get_blob_base_fee() {
  return blockContext.blob_base_fee;
}

void MyGlobalState::insert(const StateEntry &p) {
  const auto ib = accounts.insert(std::make_pair(p.first.get_address(), p));

  if (!ib.second) {
    std::cerr << "[CACHE INSERT] FAILED to insert address: "
              << mvm::address_to_hex_string(p.first.get_address()) << std::endl;
  }

    if (!ib.second) {
      std::cerr << "[ERROR] MyGlobalState::insert failed: duplicate address "
                << mvm::address_to_hex_string(p.first.get_address()) << std::endl;
      return;
    }
  }

  // operator== intentionally removed — it was never called in production
  // and the previous implementation always returned true (bug).
  // If comparison is ever needed, implement field-by-field comparison.

// add changes functions
void MyGlobalState::add_addresses_newly_deploy(const Address &addr,
                                               const Code &code) {
  addresses_newly_deploy[addr] = code;
};

void MyGlobalState::add_addresses_storage_change(const Address &addr,
                                                 const uint256_t &key,
                                                 const uint256_t &value) {
  addresses_storage_change[addr][key] = value;
};

std::pair<bool, uint256_t> MyGlobalState::add_addresses_storage_read(const Address &addr,
                                               const uint256_t &key) {
  addresses_storage_read[addr].push_back(key);

  uint8_t b_address[32];
  mvm::to_big_endian(addr, b_address);
  uint8_t b_key[32];
  mvm::to_big_endian(key, b_key);
  
  auto ret = GetStorageValue(blockContext.mvmId, b_address + 12, b_key);
  if (ret.status == STORAGE_SUSPEND) {
      throw Exception(ET::ErrExecutionReverted, "Block-STM: Estimate Hit (Suspend)");
  }
  if (ret.status == STORAGE_SUCCESS && ret.value != nullptr) {
      uint256_t uncommitted_val = mvm::from_big_endian(ret.value, 32u);
      free(ret.value);
      return {true, uncommitted_val};
  }
  return {false, 0};
};

void MyGlobalState::add_addresses_add_balance_change(const Address &addr,
                                                     const uint256_t &amount) {
  addresses_add_balance_change[addr] += amount;
};
void MyGlobalState::set_addresses_nonce_change(const Address &addr,
                                               const uint256_t &nonce) {
  addresses_nonce_change[addr] = nonce;
};

void MyGlobalState::add_addresses_sub_balance_change(const Address &addr,
                                                     const uint256_t &amount) {
  addresses_sub_balance_change[addr] += amount;
};

const std::vector<std::vector<uint8_t>> MyGlobalState::get_newly_deploy(bool apply_to_cache) {
  std::vector<std::vector<uint8_t>> result;

  for (const auto &p : addresses_newly_deploy) {
    std::vector<uint8_t> address_with_code(32 + p.second.size());
    mvm::to_big_endian(p.first, address_with_code.data());
    std::memcpy(address_with_code.data() + 32, p.second.data(),
                p.second.size());
    result.push_back(address_with_code);
  }
  if (this->isCache && apply_to_cache) {
    iterate_newly_deployed([](const mvm::Address &addr, const mvm::Code &code) {
      auto state = State::getInstance(addr);
      state->setCode(code);
    });
  }
  return result;
}

const std::vector<std::vector<uint8_t>> MyGlobalState::get_storage_change(bool apply_to_cache) {
  int size = addresses_storage_change.size();
  std::vector<std::vector<uint8_t>> result(size);
  int count = 0;

  for (const auto &p : addresses_storage_change) {
    int storage_size = 64 * p.second.size();
    std::vector<uint8_t> storage(storage_size);
    int storage_count = 0;

    for (const auto &s : p.second) {
      int idx = storage_count * 64;
      mvm::to_big_endian(s.first, storage.data() + idx);
      mvm::to_big_endian(s.second, storage.data() + idx + 32);
      storage_count++;
    }

    std::vector<uint8_t> address_with_storage_change(32 + storage_size);
    mvm::to_big_endian(p.first, address_with_storage_change.data());
    std::memcpy(address_with_storage_change.data() + 32, storage.data(),
                storage_size);
    result[count] = address_with_storage_change;
    count++;
  }
  if (this->isCache && apply_to_cache) {
    iterate_storage_changes(
        [](const Address &addr, const uint256_t &key, const uint256_t &value) {
          uint8_t b_key[32];
          mvm::to_big_endian(key, b_key);
          KeyType keyE = State::toKeyType(b_key);
          State::getInstance(addr)->insertOrUpdate(keyE, value);
        });
  }
  return result;
}

const std::vector<std::vector<uint8_t>> MyGlobalState::get_storage_read(bool apply_to_cache) {
  int size = addresses_storage_read.size();
  std::vector<std::vector<uint8_t>> result(size);
  int count = 0;

  for (const auto &p : addresses_storage_read) {
    int storage_size = 32 * p.second.size();
    std::vector<uint8_t> storage(storage_size);
    int storage_count = 0;

    for (const auto &s : p.second) {
      int idx = storage_count * 32;
      mvm::to_big_endian(s, storage.data() + idx);
      storage_count++;
    }

    std::vector<uint8_t> address_with_storage_read(32 + storage_size);
    mvm::to_big_endian(p.first, address_with_storage_read.data());
    std::memcpy(address_with_storage_read.data() + 32, storage.data(),
                storage_size);
    result[count] = address_with_storage_read;
    count++;
  }
  return result;
}

const std::vector<std::vector<uint8_t>>
MyGlobalState::get_add_balance_change(bool apply_to_cache) {
  int size = addresses_add_balance_change.size();
  std::vector<std::vector<uint8_t>> result(size);
  int count = 0;

  for (const auto &p : addresses_add_balance_change) {
    std::vector<uint8_t> address_with_add_balance_change(64);
    mvm::to_big_endian(p.first, address_with_add_balance_change.data());
    mvm::to_big_endian(p.second, address_with_add_balance_change.data() + 32);
    result[count] = address_with_add_balance_change;
    count++;
  }
  if (this->isCache && apply_to_cache) {
    iterate_add_balance_changes(
        [](const mvm::Address &addr, const uint256_t &value) {
          State::getInstance(addr)->addBalance(value);
        });
  }
  return result;
}

const std::vector<std::vector<uint8_t>>
MyGlobalState::get_sub_balance_change(bool apply_to_cache) {
  int size = addresses_sub_balance_change.size();
  std::vector<std::vector<uint8_t>> result(size);
  int count = 0;

  for (const auto &p : addresses_sub_balance_change) {
    std::vector<uint8_t> address_with_sub_balance_change(64);
    mvm::to_big_endian(p.first, address_with_sub_balance_change.data());
    mvm::to_big_endian(p.second, address_with_sub_balance_change.data() + 32);
    result[count] = address_with_sub_balance_change;
    count++;
  }
  if (this->isCache && apply_to_cache) {
    iterate_sub_balance_changes(
        [](const mvm::Address &addr, const uint256_t &value) {
          State::getInstance(addr)->subBalance(value);
        });
  }
  return result;
}

const std::vector<std::vector<uint8_t>> MyGlobalState::get_nonce_change(bool apply_to_cache) {
  int size = addresses_nonce_change.size();
  std::vector<std::vector<uint8_t>> result(size);
  int count = 0;

  for (const auto &p : addresses_nonce_change) {
    std::vector<uint8_t> address_with_nonce_change(64);
    mvm::to_big_endian(p.first, address_with_nonce_change.data());
    mvm::to_big_endian(p.second, address_with_nonce_change.data() + 32);
    result[count] = address_with_nonce_change;
    count++;
  }
  if (this->isCache && apply_to_cache) {
    iterate_nonce_changes([](const mvm::Address &addr, const uint256_t &nonce) {
      State::getInstance(addr)->setNonce(nonce);
    });
  }
  return result;
}

void MyGlobalState::Clear() {
  for (const auto &a : accounts) {
    MyStorage storage = accounts[a.first].second;
    storage.Clear();
  }
}

void MyGlobalState::clear_differences() {
    addresses_newly_deploy.clear();
    addresses_storage_change.clear();
    addresses_storage_read.clear();
    addresses_add_balance_change.clear();
    addresses_sub_balance_change.clear();
    addresses_nonce_change.clear();
}

// --- Journal support (see _Processor::StateJournal in processor.cpp) ---
// These give the journal precise read/erase access to the diff-accumulator
// maps above, so a reverted call frame can restore exactly the entry (or
// absence of one) that existed before its own mutation, rather than only
// being able to wipe every diff for the whole transaction at once (which is
// all clear_differences() above offers).

bool MyGlobalState::has_storage_change(const Address &addr, const uint256_t &key) {
    auto it = addresses_storage_change.find(addr);
    if (it == addresses_storage_change.end()) return false;
    return it->second.find(key) != it->second.end();
}

uint256_t MyGlobalState::get_storage_change_value(const Address &addr, const uint256_t &key) {
    auto it = addresses_storage_change.find(addr);
    if (it == addresses_storage_change.end()) return 0;
    auto it2 = it->second.find(key);
    return it2 != it->second.end() ? it2->second : 0;
}

void MyGlobalState::erase_storage_change(const Address &addr, const uint256_t &key) {
    auto it = addresses_storage_change.find(addr);
    if (it == addresses_storage_change.end()) return;
    it->second.erase(key);
    if (it->second.empty()) addresses_storage_change.erase(it);
}

bool MyGlobalState::has_newly_deploy(const Address &addr) {
    return addresses_newly_deploy.find(addr) != addresses_newly_deploy.end();
}

Code MyGlobalState::get_newly_deploy_value(const Address &addr) {
    auto it = addresses_newly_deploy.find(addr);
    return it != addresses_newly_deploy.end() ? it->second : Code{};
}

void MyGlobalState::erase_newly_deploy(const Address &addr) {
    addresses_newly_deploy.erase(addr);
}

void MyGlobalState::undo_add_balance_change(const Address &addr, const uint256_t &amount) {
    auto it = addresses_add_balance_change.find(addr);
    if (it == addresses_add_balance_change.end()) return;
    it->second -= amount;
    if (it->second == 0) addresses_add_balance_change.erase(it);
}

std::map<uint256_t, uint256_t> MyGlobalState::snapshot_storage_change(const Address &addr) {
    auto it = addresses_storage_change.find(addr);
    if (it == addresses_storage_change.end()) return {};
    return it->second;
}

void MyGlobalState::clear_storage_change(const Address &addr) {
    addresses_storage_change.erase(addr);
}

void MyGlobalState::undo_sub_balance_change(const Address &addr, const uint256_t &amount) {
    auto it = addresses_sub_balance_change.find(addr);
    if (it == addresses_sub_balance_change.end()) return;
    it->second -= amount;
    if (it->second == 0) addresses_sub_balance_change.erase(it);
}

} // namespace mvm
