// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once

#include "bigint.h"
#include "gas.h"

#include <map>

namespace mvm
{
  /**
   * Abstract interface for accessing EVM's permanent, per-address key-value
   * storage
   */
  struct Storage
  {
    virtual void store(const uint256_t& key, const uint256_t& value, GasTracker* gas_tracker = NULL) = 0;
    virtual uint256_t load(const uint256_t& key, GasTracker* gas_tracker = NULL) = 0;
    virtual bool remove(const uint256_t& key) = 0;

    // Journal support (see _Processor::StateJournal in processor.cpp): raw
    // access to the in-VM read/write cache, bypassing gas charges and the
    // Go/State-singleton FFI round-trip that load()/store() perform. Used
    // exclusively to snapshot a slot's cached value before a mutation and
    // restore it verbatim if the enclosing call frame reverts.
    virtual bool has_cached(const uint256_t& key) = 0;
    virtual uint256_t get_cached(const uint256_t& key) = 0;
    virtual void set_cached_raw(const uint256_t& key, const uint256_t& value) = 0;
    virtual void erase_cached(const uint256_t& key) = 0;

    // EIP-6780 support (see selfdestruct() in processor.cpp): snapshot the
    // whole in-VM cache before clearing it, so the journal can restore every
    // entry if an ancestor call frame later reverts the SELFDESTRUCT.
    virtual std::map<uint256_t, uint256_t> snapshot_cached() = 0;
    virtual void clear_all_cached() = 0;

    virtual ~Storage() {}
  };
} // namespace mvm
