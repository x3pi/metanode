// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once

#include "account.h"
#include "globalstate.h"
#include "native_logger.h"
#include "trace.h"
#include "transaction.h"
#include "extension.h"
#include "log.h"

#include <cstdint>
#include <vector>

namespace mvm
{
  enum class ExitReason : uint8_t
  {
    returned = 0,
    halted,
    threw
  };

  struct ExecResult
  {
    ExitReason er = {};
    Exception::Type ex = {};
    std::string exmsg = {};
    std::vector<uint8_t> output = {};
    unsigned long long gas_used = 0;
  };

  /**
   * Ethereum bytecode processor.
   */
  class Processor
  {
  private:
    GlobalState& gs;
    LogHandler& log_handler;
    Extension& extension;
    NativeLogger& native_logger;

  public:
    Processor(
      GlobalState& gs, 
      LogHandler& log_handler,
      Extension& extension,
      NativeLogger& native_logger
    );
    /**
     * @brief The main entry point for the EVM.
     *
     * Runs the callee's code in the caller's context. VM exceptions (ie,
     * mvm::Exception) will be caught and returned in the result.
     *
     * @param tx the transaction
     * @param caller the caller's address
     * @param callee the callee's account state
     * @param input the raw byte input
     * @param call_value the call value
     * @param tr [optional] a pointer to a trace object. If given, a trace of
     * the execution will be collected.
     * @return ExecResult the execution result
     */
    ExecResult run(
      Transaction& tx,
      bool deploy,
      const Address& caller,
      AccountState callee,
      const std::vector<uint8_t>& input,
      const uint256_t& call_value,
      Trace* tr = nullptr,
      bool readOnly = false
    );
  };
} // namespace mvm
