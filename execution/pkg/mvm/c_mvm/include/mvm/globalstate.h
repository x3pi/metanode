// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once

#include "account.h"
#include "block_context.h"
#include "storage.h"

#include <map>

namespace mvm
{
  /**
   * An account and its storage
   */
  struct AccountState
  {
    Account &acc;
    Storage &st;

    template <
        typename T,
        typename U,
        typename = std::enable_if_t<std::is_base_of<Account, T>::value>,
        typename = std::enable_if_t<std::is_base_of<Storage, U>::value>>
    AccountState(std::pair<T, U> &p) : acc(p.first), st(p.second)
    {
    }
    AccountState(Account &acc, Storage &st) : acc(acc), st(st) {}
  };

  /**
   * Abstract interface for interacting with EVM world state
   */
  struct GlobalState
  {
    virtual void remove(const Address &addr) = 0;
    virtual bool is_cache() = 0;

    /**
     * Creates a new zero-initialized account under the given address if none
     * exists
     */
    virtual AccountState get(const Address &addr, GasTracker *gas_tracker = NULL) = 0;
    virtual AccountState get(const Address &addr, GasTracker *gas_tracker, const uint256_t &lashHash) = 0;
    virtual AccountState getUpdate(const Address &addr) = 0;

    virtual AccountState create(
        const Address &addr, const uint256_t &balance, const Code &code, const uint256_t &nonce) = 0;
    virtual const BlockContext &get_block_context() = 0;
    virtual void set_block_context(const BlockContext &blockContext) = 0;

    virtual uint256_t get_chain_id() = 0;
    virtual uint256_t get_block_hash(int) = 0;

    // Cross-chain precompile (address 263): trả về context từ Go handler
    virtual std::vector<uint8_t> get_cross_chain_sender() = 0;
    virtual std::vector<uint8_t> get_cross_chain_source_id() = 0;

    virtual void add_addresses_newly_deploy(const Address &addr, const Code &code) = 0;
    virtual void add_addresses_storage_change(const Address &addr, const uint256_t &key, const uint256_t &value) = 0;
    virtual void add_addresses_add_balance_change(const Address &addr, const uint256_t &amount) = 0;
    virtual void add_addresses_sub_balance_change(const Address &addr, const uint256_t &amount) = 0;
    virtual void set_addresses_nonce_change(const Address &addr, const uint256_t &amount) = 0;
  };
} // namespace mvm
