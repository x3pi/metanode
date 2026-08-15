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

    // EIP-4844: current tx's blob versioned hashes (BLOBHASH) and the current
    // block's blob base fee (BLOBBASEFEE), both sourced from Go per-mvmId
    // context (see MVMApi.SetBlobContext) rather than a BlockContext/Transaction
    // ABI field, to avoid changing the FFI signature of every exported mvm_linker
    // function. get_blob_hash returns 0 for an out-of-range index, matching
    // EIP-4844's BLOBHASH rule.
    virtual uint256_t get_blob_hash(uint64_t index) = 0;
    virtual uint256_t get_blob_base_fee() = 0;

    virtual void add_addresses_newly_deploy(const Address &addr, const Code &code) = 0;
    virtual void add_addresses_storage_change(const Address &addr, const uint256_t &key, const uint256_t &value) = 0;
    virtual void add_addresses_add_balance_change(const Address &addr, const uint256_t &amount) = 0;
    virtual void add_addresses_sub_balance_change(const Address &addr, const uint256_t &amount) = 0;
    virtual void set_addresses_nonce_change(const Address &addr, const uint256_t &amount) = 0;

    // Journal support (see _Processor::StateJournal in processor.cpp): lets
    // a reverted call frame precisely undo what the add_addresses_*/
    // set_addresses_* calls above recorded — has_*/get_*_value snapshot the
    // prior entry (if any) before a mutation, erase_*/undo_* restore it (or
    // remove the entry entirely if there was none) when that mutation needs
    // to be rolled back.
    virtual bool has_storage_change(const Address &addr, const uint256_t &key) = 0;
    virtual uint256_t get_storage_change_value(const Address &addr, const uint256_t &key) = 0;
    virtual void erase_storage_change(const Address &addr, const uint256_t &key) = 0;

    virtual bool has_newly_deploy(const Address &addr) = 0;
    virtual Code get_newly_deploy_value(const Address &addr) = 0;
    virtual void erase_newly_deploy(const Address &addr) = 0;

    // EIP-6780 support (see selfdestruct() in processor.cpp): a SELFDESTRUCT
    // of a contract created earlier in the SAME transaction fully clears its
    // storage diff, not just its balance. snapshot_storage_change captures
    // every (key, value) pair recorded for addr so the journal can restore
    // them all if an ancestor call frame later reverts the SELFDESTRUCT.
    virtual std::map<uint256_t, uint256_t>
    snapshot_storage_change(const Address &addr) = 0;
    virtual void clear_storage_change(const Address &addr) = 0;

    // add_addresses_add_balance_change/add_addresses_sub_balance_change
    // accumulate (+=), so undoing one is exactly subtracting the same
    // amount back out — no has_/erase_ pair needed the way storage/deploy
    // (last-write-wins) require.
    virtual void undo_add_balance_change(const Address &addr, const uint256_t &amount) = 0;
    virtual void undo_sub_balance_change(const Address &addr, const uint256_t &amount) = 0;
  };
} // namespace mvm
