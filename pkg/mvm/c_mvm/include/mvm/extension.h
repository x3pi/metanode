#pragma once
#include "account.h"
#include "address.h"

namespace mvm {
const Address CALL_API_EXTENSION = 257;
const Address EXTRACT_JSON_FIELD_EXTENSION = 258;
const Address BLST = 259;
const Address MATH_EXTENSTON_ADDRESS = 260;
const Address SIMPLE_DATABASE_ADDRESS = 261;
const Address FULL_DATABASE_ADDRESS = 262;
const Address FULL_DATABASE_ADDRESS_V1 = 263;
const Address CROSS_CHAIN_ADDRESS = 0xB429C0B2; // Merged with Gateway CC

/**
 * An account and its storage
 */
struct Extension {
  virtual Code CallGetApi(Code input) = 0;
  virtual Code ExtractJsonField(Code input) = 0;
  virtual Code Blst(Code input) = 0;
  virtual Code Math(Code input) = 0;
  virtual Code Ecrecover(mvm::Code input) = 0;
  virtual Code SimpleDatabase(Code input, Address address) = 0;
  virtual Code FullDatabase(Code input, Address address, bool isReset,
                            uint256_t blockNumber) = 0;
  virtual Code FullDatabaseV1(Code input, Address address, bool isReset,
                              uint256_t blockNumber) = 0;
  virtual Code Sha256(mvm::Code input) = 0;
  virtual Code EcAdd(mvm::Code input) = 0;
  virtual Code Ripemd160(mvm::Code input) = 0;
  virtual Code Modexp(mvm::Code input) = 0;
  virtual Code EcMul(mvm::Code input) = 0;
  virtual Code EcPairing(mvm::Code input) = 0;
  virtual Code Blake2f(mvm::Code input) = 0;
  virtual Code PointEvaluationVerify(mvm::Code input) = 0;
  virtual Code PublicKeyFromPrivateKey(mvm::Code input) = 0;
};
} // namespace mvm
