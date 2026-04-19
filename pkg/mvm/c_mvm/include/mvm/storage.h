// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once

#include "bigint.h"
#include "gas.h"

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
    virtual ~Storage() {}
  };
} // namespace mvm
