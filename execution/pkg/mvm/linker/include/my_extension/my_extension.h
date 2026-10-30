#pragma once

#include "constants.h"
#include "mvm/extension.h"
#include "xapian/xapian_registry.h"
#include <mpfr.h>
#include <vector>

namespace mvm {
using Code = std::vector<uint8_t>;
}

class MyExtension : public mvm::Extension {
public:
  unsigned char *mvmId;
  const uint256_t *txHash;
  bool isOffChain;
  std::vector<XapianRegistry::XapianSnapshot> xapian_checkpoints;

  MyExtension(unsigned char *id, bool offChain = false, const uint256_t *hash = nullptr)
      : mvmId(id), txHash(hash), isOffChain(offChain) {}
  mvm::Code CallGetApi(mvm::Code input);
  mvm::Code ExtractJsonField(mvm::Code input);
  mvm::Code Blst(mvm::Code input);
  mvm::Code Math(mvm::Code input);
  mvm::Code Ecrecover(mvm::Code input);
  mvm::Code Ripemd160(mvm::Code input);
  mvm::Code Sha256(mvm::Code input);
  mvm::Code Modexp(mvm::Code input);
  mvm::Code EcAdd(mvm::Code input);
  mvm::Code EcMul(mvm::Code input);
  mvm::Code EcPairing(mvm::Code input);
  mvm::Code Blake2f(mvm::Code input);
  mvm::Code PointEvaluationVerify(mvm::Code input);
  mvm::Code SimpleDatabase(mvm::Code input, mvm::Address address);
  mvm::Code FullDatabase(mvm::Code input, mvm::Address address, bool isReset,
                         uint256_t blockNumber, mvm::GlobalState* gs = nullptr) override;
  mvm::Code FullDatabaseV1(mvm::Code input, mvm::Address address, bool isReset,
                           uint256_t blockNumber, mvm::GlobalState* gs = nullptr) override;
  mvm::Code PublicKeyFromPrivateKey(mvm::Code input);

  size_t xapian_checkpoint(const mvm::Code& input = {}) override {
    if (input.size() >= 4) {
      uint32_t opCode =
          (input[0] << 24) | (input[1] << 16) | (input[2] << 8) | input[3];
      if (!mvm::FunctionSelector::isXapianWriteOpcode(opCode)) {
        return (size_t)-1;
      }
    }
    size_t mark = xapian_checkpoints.size();
    xapian_checkpoints.push_back(XapianRegistry::takeSnapshotForTxHash(txHash));
    return mark;
  }

  void xapian_rollback(size_t mark) override {
    if (mark < xapian_checkpoints.size()) {
      XapianRegistry::revertToSnapshotForTxHash(txHash, xapian_checkpoints[mark]);
      xapian_checkpoints.resize(mark);
    }
  }
};
