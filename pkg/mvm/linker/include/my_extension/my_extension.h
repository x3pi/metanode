#pragma once

#include "constants.h"
#include "mvm/extension.h"
#include <mpfr.h>
#include <vector>

namespace mvm {
using Code = std::vector<uint8_t>;
}

class MyExtension : public mvm::Extension {
public:
  unsigned char *mvmId;
  bool isOffChain;
  MyExtension(unsigned char *id, bool offChain = false)
      : mvmId(id), isOffChain(offChain) {}
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
                         uint256_t blockNumber);
  mvm::Code FullDatabaseV1(mvm::Code input, mvm::Address address, bool isReset,
                           uint256_t blockNumber);
  mvm::Code PublicKeyFromPrivateKey(mvm::Code input);
};
