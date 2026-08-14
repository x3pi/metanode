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
    // Only populated by deploy/call/execute. sendNative/
    // processNativeMintBurn/noncePlusOne never run the interpreter, so
    // their BlockContext correctly leaves these at zero-value defaults —
    // those opcodes are unreachable from those 3 entry points regardless.
    //
    // executeBatch is DIFFERENT (found by code review, 2026-08-14): it
    // also calls run() — the same interpreter entry point as
    // deploy/call/execute — per batch item, so CHAINID/BLOBHASH/
    // BLOBBASEFEE/the cross-chain precompile/BLOCKHASH ARE reachable from
    // it, but it does NOT yet receive this context (still calls
    // CreateBlockContext with only the 7 pre-B1 positional args). Left
    // unwired deliberately, not by oversight-turned-into-a-lie: as of this
    // writing executeBatch has zero live Go callers (only its own
    // MVMApi.ExecuteBatch wrapper defines it — see mvm_api.go), so this is
    // dead-code-only exposure today. It's also not a mechanical B1-style
    // fix if it's ever activated — items in one batch can be different
    // transactions with different blob/cross-chain context, so wiring it
    // properly needs per-item context fields on ExecuteBatchInputC, not
    // just the single shared BlockContext deploy/call/execute use; chain_id
    // is the one exception (chain-wide, not per-tx) that could be threaded
    // through cheaply on its own if this path is ever brought into use.
    //
    uint256_t chain_id{};

    std::vector<uint256_t> blob_versioned_hashes;
    uint256_t blob_base_fee{};

    // Empty sender (size() == 0) means "not a cross-chain-precompile call",
    // exactly matching the pre-B1 crossChainActive=false case — no separate
    // has-flag needed, this is already an unambiguous real value.
    std::vector<uint8_t> cross_chain_sender;
    uint64_t cross_chain_source_id{0};

    // GetBlockHash/BLOCKHASH's replacement (last of the 6 callbacks,
    // finished after the rest — see note/tee_core_packaging_plan.md).
    // block_hashes[i] = hash of block (number - 1 - i), i.e. index 0 is the
    // immediately preceding block, matching BLOCKHASH's own "how many
    // blocks back" framing. Deliberately sparse/empty on the common path:
    // the caller only populates this when the executing bytecode actually
    // contains opcode 0x40 (see HasBlockhashOpcode, mvm_api.go) — unlike
    // chain_id/blob context's single cheap reads, fetching up to 256 real
    // hashes (cache lookups, or on a miss, real DB reads / O(N) walkback —
    // see BlockChain.GetBlockHashByNumber) on every single call regardless
    // of use would be real, avoidable overhead on the hot path.
    std::vector<uint256_t> block_hashes;
  };

  inline bool operator==(const BlockContext &l, const BlockContext &r)
  {
    return l.coinbase == r.coinbase && l.number == r.number &&
           l.prevrandao == r.prevrandao && l.gas_limit == r.gas_limit &&
           l.time == r.time;
  }
} // namespace mvm
