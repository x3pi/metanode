// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once
#include "address.h"

#include <nlohmann/json.hpp>
#include <vector>

namespace mvm
{
  /**
   * Ethereum transaction
   */
  struct Transaction
  {
    const Address origin;
    const uint256_t value;
    const uint64_t gas_price;
    const uint64_t gas_limit;
    const uint256_t tx_hash;
    const bool is_debug;

    Transaction(
        const Address origin,
        const uint256_t value,
        const uint64_t gas_price,
        const uint64_t gas_limit,
        const uint256_t tx_hash,
        const bool is_debug

        ) : origin(origin),
            value(value),
            gas_price(gas_price),
            gas_limit(gas_limit),
            tx_hash(tx_hash),
            is_debug(is_debug)
    {
    }
  };
} // namespace mvm
