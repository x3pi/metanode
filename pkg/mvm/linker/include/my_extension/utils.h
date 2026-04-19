#pragma once

#include "mvm/util.h"
#include <cstdint>
#include <filesystem>
#include <mpfr.h>
#include <vector>

namespace mvm {
void hexToSignedInt(mpfr_t result, const std::vector<uint8_t> &bytes);
void signedIntToHex(std::vector<uint8_t> &result_bytes, const mpfr_t number);
std::vector<uint8_t> evm_encode_mpfr(const mpfr_t &value);
std::filesystem::path createFullPath(const mvm::Address &address,
                                     const std::string &dbname);
} // namespace mvm

// Called from Go via CGo to set the Xapian base path from config file.
// Replaces the XAPIAN_BASE_PATH environment variable approach.
// Must be called before any Xapian operation (i.e., during app startup).
extern "C" void SetXapianBasePath(const char *path);