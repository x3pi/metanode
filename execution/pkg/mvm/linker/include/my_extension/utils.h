#pragma once

#include "mvm/util.h"
#include <cstdint>
#include <filesystem>
#include <mpfr.h>
#include <vector>

namespace mvm {
std::string keccak256(const std::string &input);
void hexToSignedInt(mpfr_t result, const std::vector<uint8_t> &bytes);
void signedIntToHex(std::vector<uint8_t> &result_bytes, const mpfr_t number);
std::vector<uint8_t> evm_encode_mpfr(const mpfr_t &value);
std::filesystem::path createFullPath(const mvm::Address &address,
                                     const std::string &dbname);

// 2026-08-20 (plan §9.29): true if SetXapianBasePath() has never been
// called with a non-empty path -- the TA build never calls it at all (no
// filesystem to point it at), so this is how XapianManager tells "running
// in the TA, use Xapian::InMemory" apart from "running under cgo, use the
// real on-disk Glass backend at g_xapian_base_path" without needing a
// separate build-time flag. cgo mode's own startup always calls
// SetXapianBasePath from config.json before any Xapian operation, so this
// stays false there for the whole process lifetime -- zero behavior
// change on that path.
bool IsXapianBasePathEmpty();
} // namespace mvm

// Called from Go via CGo to set the Xapian base path from config file.
// Replaces the XAPIAN_BASE_PATH environment variable approach.
// Must be called before any Xapian operation (i.e., during app startup).
extern "C" void SetXapianBasePath(const char *path);