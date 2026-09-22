#include "mvm/cross_chain_precompile.h"
#include "mvm/account.h"
#include "mvm/globalstate.h"
#include "mvm/log.h"
#include "my_extension/constants.h"
#include <cstring>

namespace mvm {

bool handle_cross_chain_precompile(
    GlobalState &gs, const std::vector<uint8_t> &input,
    std::vector<uint8_t> &output, AccountState &acc, const uint256_t value,
    LogHandler &log_handler, const uint256_t timestamp, const Address addr) {
  if (input.size() < 4) {
    return false;
  }

  uint32_t selector = (uint32_t(input[0]) << 24) | (uint32_t(input[1]) << 16) |
                      (uint32_t(input[2]) << 8) | uint32_t(input[3]);

  static const uint32_t SEL_SENDER =
      FunctionSelector::getFunctionSelectorFromString("getOriginalSender()");
  static const uint32_t SEL_CHAINID =
      FunctionSelector::getFunctionSelectorFromString("getSourceChainId()");

  if (selector == SEL_SENDER) {
    output = gs.get_cross_chain_sender();
    return !output.empty();
  } else if (selector == SEL_CHAINID) {
    output = gs.get_cross_chain_source_id();
    return !output.empty();
  }

  static const uint32_t SEL_LOCK_AND_BRIDGE =
      FunctionSelector::getFunctionSelectorFromString(
          "lockAndBridge(address,uint256)");
  static const uint32_t SEL_SEND_MESSAGE =
      FunctionSelector::getFunctionSelectorFromString(
          "sendMessage(address,bytes,uint256)");

  // SECURITY FIX (cross-chain audit): lockAndBridge/sendMessage used to lock/deduct the caller's
  // balance here and emit a raw EVM log, but nothing on the Go side ever decodes that log back
  // into a queued CrossChainMessage -- GatewayEngine.PendingOutboundMessages (the only structure
  // the relayer/batch pipeline reads from) is only ever appended to by the native Go
  // outbound()/handleWrite path in gateway_handler.go. A Solidity contract calling this precompile
  // therefore had its msg.value permanently deducted with no relay path ever discovering the
  // message -- funds silently and permanently stranded. Until the EVM-side call is properly wired
  // into GatewayEngine.Outbound() (a cross-language integration touching the general, consensus-
  // critical EVM transaction-execution pipeline shared by every contract call on every validator --
  // deliberately out of scope for an in-place patch here, tracked separately), fail closed: reject
  // the call with no balance mutation and no log, instead of silently accepting value it can never
  // deliver. The real, fully-wired way to send a cross-chain message today is the native
  // outbound() ABI method on the Gateway contract (see gateway_handler.go's "outbound" case).
  if (selector == SEL_LOCK_AND_BRIDGE || selector == SEL_SEND_MESSAGE) {
    (void)acc;
    (void)value;
    (void)timestamp;
    (void)addr;
    (void)log_handler;
    output.clear();
    return false;
  }

  return false;
}

} // namespace mvm
