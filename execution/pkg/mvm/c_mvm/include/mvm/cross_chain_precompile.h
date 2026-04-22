#pragma once

#include "globalstate.h"
#include <vector>
#include <cstdint>

namespace mvm {

struct AccountState;
struct LogHandler;

bool handle_cross_chain_precompile(
    GlobalState &gs,
    const std::vector<uint8_t> &input,
    std::vector<uint8_t> &output,
    AccountState &acc,
    const uint256_t value,
    LogHandler &log_handler,
    const uint256_t timestamp,
    const Address addr);
} // namespace mvm
