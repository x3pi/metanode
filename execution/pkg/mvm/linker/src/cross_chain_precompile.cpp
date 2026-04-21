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
    std::cout << "[CROSS-CHAIN-DEBUG] getOriginalSender() called! Output size: "
              << output.size() << std::endl;
    if (!output.empty()) {
      std::cout << "[CROSS-CHAIN-DEBUG] Data (Hex): ";
      for (uint8_t byte : output) {
        printf("%02x", byte);
      }
      std::cout << std::endl;
    } else {
      std::cout << "[CROSS-CHAIN-DEBUG] ⚠️ getOriginalSender() returned EMPTY "
                   "(Failure)"
                << std::endl;
    }
    return !output.empty();
  } else if (selector == SEL_CHAINID) {
    output = gs.get_cross_chain_source_id();
    std::cout << "[CROSS-CHAIN-DEBUG] getSourceChainId() called! Output size: "
              << output.size() << std::endl;
    return !output.empty();
  }

  static const uint32_t SEL_LOCK_AND_BRIDGE =
      FunctionSelector::getFunctionSelectorFromString(
          "lockAndBridge(address,uint256)");
  static const uint32_t SEL_SEND_MESSAGE =
      FunctionSelector::getFunctionSelectorFromString(
          "sendMessage(address,bytes,uint256)");

  // Cross-Chain Gateway methods
  if (selector == SEL_LOCK_AND_BRIDGE || selector == SEL_SEND_MESSAGE) {
    if (value > 0) {
      if (acc.acc.get_balance() < value) {
        throw std::runtime_error(
            "CrossChain: Insufficient Native Token to bridge");
      }
      acc.acc.set_balance(acc.acc.get_balance() - value);
      gs.add_addresses_sub_balance_change(acc.acc.get_address(), value);
    }

    uint256_t destId = 0;
    uint256_t targetAddr = 0;
    std::vector<uint8_t> payload;

    if (selector == SEL_LOCK_AND_BRIDGE && input.size() >= 68) {
      destId = from_big_endian(input.data() + 36,
                               32); // Prama thứ 2 (bỏ 4 byte đầu + 32 byte đầu)
      payload.assign(input.data() + 4, input.data() + 36);
    } else if (selector == SEL_SEND_MESSAGE && input.size() >= 100) {
      targetAddr = from_big_endian(input.data() + 4, 32);
      destId = from_big_endian(input.data() + 68, 32);
      uint32_t offset =
          static_cast<uint32_t>(from_big_endian(input.data() + 36, 32));
      if (offset + 32 <= input.size()) {
        uint32_t length = static_cast<uint32_t>(
            from_big_endian(input.data() + 4 + offset, 32));
        if (4 + offset + 32 + length <= input.size()) {
          payload.assign(input.data() + 4 + offset + 32,
                         input.data() + 4 + offset + 32 + length);
        }
      }
    }

    std::vector<uint8_t> event_data;
    event_data.resize(6 * 32, 0);
    event_data[31] = 1; // isEVM = true

    uint8_t buf[32] = {0};
    to_big_endian(acc.acc.get_address(), buf);
    memcpy(event_data.data() + 32, buf, 32);

    to_big_endian(targetAddr, buf);
    memcpy(event_data.data() + 64, buf, 32);

    to_big_endian(value, buf);
    memcpy(event_data.data() + 96, buf, 32);

    uint256_t offset_payload = 6 * 32;
    to_big_endian(offset_payload, buf);
    memcpy(event_data.data() + 128, buf, 32);

    to_big_endian(timestamp, buf);
    memcpy(event_data.data() + 160, buf, 32);

    uint256_t payload_len = payload.size();
    to_big_endian(payload_len, buf);
    event_data.insert(event_data.end(), buf, buf + 32);

    event_data.insert(event_data.end(), payload.begin(), payload.end());
    size_t remainder = payload.size() % 32;
    if (remainder != 0) {
      event_data.resize(event_data.size() + (32 - remainder), 0);
    }

    LogEntry log;
    log.address = addr;
    std::cout << "[CROSS-CHAIN-PRECOMPILE] " << std::endl;
    uint8_t t0_buf[32] = {0xb5, 0x28, 0xe3, 0xa3, 0xd4, 0xcb, 0xfd, 0x0b,
                          0x61, 0xa8, 0x3c, 0xc2, 0x8a, 0x00, 0x4e, 0x80,
                          0x17, 0x77, 0xb8, 0xed, 0x62, 0x74, 0xad, 0xee,
                          0x62, 0xa7, 0x27, 0x63, 0x2f, 0xee, 0x66, 0xdd};
    log.topics.push_back(from_big_endian(t0_buf, 32));

    std::vector<uint8_t> source_bin = gs.get_cross_chain_source_id();
    uint256_t srcId = 1;
    if (source_bin.size() == 32) {
      srcId = from_big_endian(source_bin.data(), 32);
    }
    log.topics.push_back(srcId);  // Topics[1] = sourceNationId
    log.topics.push_back(destId); // Topics[2] = destNationId

    // Topics[3] = msgId = txHash của transaction hiện tại
    // Nhất quán với Go handler (isEVM=false) emit tx.Hash() vào Topics[3]
    uint256_t tx_hash_val = gs.get_block_context().tx_hash;
    log.topics.push_back(tx_hash_val); // Topics[3] = msgId
    std::cout << "[CROSS-CHAIN-PRECOMPILE] MessageSent: srcId=" << srcId
              << " destId=" << destId << " msgId(txHash)=" << tx_hash_val
              << std::endl;

    log.data = event_data;
    log_handler.handle(std::move(log));

    output.clear();
    return true;
  }

  return false;
}

} // namespace mvm
