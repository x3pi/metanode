#include "my_global_state.h"
#include "mvm/exception.h"
#include "mvm/gas.h"
#include "mvm/util.h"
#include "mvm_linker.hpp"
#include "state.h"
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
  // Gọi callback để lấy data từ Go
  GlobalStateGet_return accountQueryData =
      GlobalStateGet(blockContext.mvmId, b_address + 12);

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
    const auto acc = accounts.find(addr);
    if (gas_tracker != nullptr) {
      gas_tracker->add_gas_used(getUnTouchedAddressGasCost());
    }
    ClearProcessingPointers(blockContext.mvmId);
    
    // Free memory allocated by Go C.CBytes
    free(accountQueryData.balance_p);
    free(accountQueryData.code_p);
    free(accountQueryData.nonce);

    if (isCache) {
      auto state = State::getInstance(addr);
      state->setBalance(balance);
      state->setNonce(nonce);
      state->setCode(code);
    }
    return acc->second;
  }
  return create(addr, 0, {}, 0);
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
    const auto acc = accounts.find(addr);
    ClearProcessingPointers(blockContext.mvmId);

    // Free memory allocated by Go C.CBytes
    free(accountQueryData.balance_p);
    free(accountQueryData.code_p);
    free(accountQueryData.nonce);

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
    const auto acc = accounts.find(addr);
    if (gas_tracker != nullptr) {
      gas_tracker->add_gas_used(getUnTouchedAddressGasCost());
    }
    ClearProcessingPointers(blockContext.mvmId);

    // Free memory allocated by Go C.CBytes
    free(accountQueryData.balance_p);
    free(accountQueryData.code_p);
    free(accountQueryData.nonce);

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

uint256_t MyGlobalState::get_block_hash(int blockNumber) {
  Value_return valueReturn = GetBlockHash(blockNumber);
  uint256_t hash = mvm::bytes_to_uint256(valueReturn.data_p);
  free(valueReturn.data_p); // Free memory from Go
  return hash;
}

uint256_t MyGlobalState::get_chain_id() {
  Value_return valueReturn = GetChainId();
  uint256_t chainId = mvm::bytes_to_uint256(valueReturn.data_p);
  free(valueReturn.data_p); // Free memory from Go
  return chainId;
}

std::vector<uint8_t> MyGlobalState::get_cross_chain_sender() {
  unsigned char *currentMvmId = blockContext.mvmId;
  if (currentMvmId == nullptr) {
    return {};
  }
  Value_return vr = GetCrossChainSender(currentMvmId);
  if (!vr.success || vr.data_p == nullptr || vr.data_size <= 0) {
    if (vr.data_p != nullptr) free(vr.data_p);
    return {};
  }
  std::vector<uint8_t> result(vr.data_p, vr.data_p + vr.data_size);
  free(vr.data_p);
  return result;
}

std::vector<uint8_t> MyGlobalState::get_cross_chain_source_id() {
  unsigned char *currentMvmId = blockContext.mvmId;
  if (currentMvmId == nullptr) {
    return {};
  }
  Value_return vr = GetCrossChainSourceId(currentMvmId);
  if (!vr.success || vr.data_p == nullptr || vr.data_size <= 0) {
    if (vr.data_p != nullptr) free(vr.data_p);
    return {};
  }
  std::vector<uint8_t> result(vr.data_p, vr.data_p + vr.data_size);
  free(vr.data_p);
  return result;
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

void MyGlobalState::add_addresses_add_balance_change(const Address &addr,
                                                     const uint256_t &amount) {
  addresses_add_balance_change[addr] += amount;
};
void MyGlobalState::set_addresses_nonce_change(const Address &addr,
                                               const uint256_t &nonce) {
  addresses_nonce_change[addr] = nonce;

    // 🔒 NONCE-FIX: Also update State::instances cache immediately.
    // This is critical for CREATE opcode (deploy transactions) which calls
    // set_addresses_nonce_change but doesn't go through incrementSenderNonce().
    // Without this, State cache stays stale and next TX in same block gets wrong nonce.
    if (isCache && State::instanceExists(addr)) {
      State::getInstance(addr)->setNonce(nonce);
    }
};

void MyGlobalState::add_addresses_sub_balance_change(const Address &addr,
                                                     const uint256_t &amount) {
  addresses_sub_balance_change[addr] += amount;
};

const std::vector<std::vector<uint8_t>> MyGlobalState::get_newly_deploy() {
  std::vector<std::vector<uint8_t>> result;

  for (const auto &p : addresses_newly_deploy) {
    std::vector<uint8_t> address_with_code(32 + p.second.size());
    mvm::to_big_endian(p.first, address_with_code.data());
    std::memcpy(address_with_code.data() + 32, p.second.data(),
                p.second.size());
    result.push_back(address_with_code);
  }
  if (this->isCache) {
    iterate_newly_deployed([](const mvm::Address &addr, const mvm::Code &code) {
      auto state = State::getInstance(addr);
      state->setCode(code);
    });
  }
  return result;
}

const std::vector<std::vector<uint8_t>> MyGlobalState::get_storage_change() {
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
  if (this->isCache) {
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

const std::vector<std::vector<uint8_t>>
MyGlobalState::get_add_balance_change() {
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
  if (this->isCache) {
    iterate_add_balance_changes(
        [](const mvm::Address &addr, const uint256_t &value) {
          auto state = State::getInstance(addr);
          auto balance_after = state->getBalance() + value;
          state->setBalance(balance_after);
        });
  }
  return result;
}

const std::vector<std::vector<uint8_t>>
MyGlobalState::get_sub_balance_change() {
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
  if (this->isCache) {
    iterate_sub_balance_changes(
        [](const mvm::Address &addr, const uint256_t &value) {
          auto state = State::getInstance(addr);
          auto balance_after = state->getBalance() - value;
          state->setBalance(balance_after);
        });
  }
  return result;
}

const std::vector<std::vector<uint8_t>> MyGlobalState::get_nonce_change() {
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
  if (this->isCache) {
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
    addresses_add_balance_change.clear();
    addresses_sub_balance_change.clear();
    addresses_nonce_change.clear();
}

} // namespace mvm
