// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once
#include "bigint.h"
#include "util.h"

namespace mvm
{
  /**
   * An Ethereum block descriptor; in particular, this is used to parse
   * cpp-ethereum test cases.
   */
  struct BlockContext
  {
      unsigned char *mvmId;
    uint64_t prevrandao;// Provides information for PREVRANDAO
    uint64_t gas_limit; // Provides information for GASLIMIT
    uint64_t time;      // Provides information for TIME
    uint64_t base_fee; // Provides information for BASEFEE
    uint256_t number;   // Provides information for NUMBER
    uint256_t coinbase; // Provides information for COINBASE
    uint256_t tx_hash;  // txHash của transaction hiện tại — dùng làm msgId trong cross-chain events
  };

  inline bool operator==(const BlockContext &l, const BlockContext &r)
  {
    return l.coinbase == r.coinbase && l.number == r.number &&
           l.prevrandao == r.prevrandao && l.gas_limit == r.gas_limit &&
           l.time == r.time;
  }
} // namespace mvm
