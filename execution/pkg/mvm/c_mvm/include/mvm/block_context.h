// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once
#include "bigint.h"
#include "util.h"
#include <cstdint>
#include <vector>

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

    // --- TEE-packaging B1 (see note/tee_core_packaging_plan.md): context
    // that used to be fetched via a C++->Go callback mid-execution
    // (GetChainId/GetCrossChainSender/GetCrossChainSourceId/GetBlobHash/
    // GetBlobBaseFee), now set once here by the caller before execution
    // starts — matching the shape a real TA session command needs (all
    // context arrives with the call, nothing is fetched mid-flight).
    //
    // Only populated by deploy/call/execute (the 3 entry points that run
    // the interpreter and can therefore reach CHAINID/BLOBHASH/
    // BLOBBASEFEE/the cross-chain precompile). executeBatch/sendNative/
    // processNativeMintBurn/noncePlusOne never run the interpreter, so
    // their BlockContext leaves these at their zero-value defaults, which
    // is correct: those opcodes are unreachable from those entry points
    // regardless.
    //
    // GetBlockHash (BLOCKHASH opcode) deliberately NOT covered here yet —
    // it needs an array of up to 256 preceding hashes rather than a single
    // value, a larger design than this pass's scope; still goes through
    // the old callback (see MyGlobalState::get_block_hash).
    uint256_t chain_id{};

    std::vector<uint256_t> blob_versioned_hashes;
    uint256_t blob_base_fee{};

    // Empty sender (size() == 0) means "not a cross-chain-precompile call",
    // exactly matching the pre-B1 crossChainActive=false case — no separate
    // has-flag needed, this is already an unambiguous real value.
    std::vector<uint8_t> cross_chain_sender;
    uint64_t cross_chain_source_id{0};
  };

  inline bool operator==(const BlockContext &l, const BlockContext &r)
  {
    return l.coinbase == r.coinbase && l.number == r.number &&
           l.prevrandao == r.prevrandao && l.gas_limit == r.gas_limit &&
           l.time == r.time;
  }
} // namespace mvm
