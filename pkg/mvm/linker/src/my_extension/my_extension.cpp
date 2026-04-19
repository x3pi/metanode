// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.
#include <mvm/crypto/blake2b.hpp>
#include <mvm/crypto/bn254.hpp>
#include <mvm/crypto/kzg.hpp>
#include <mvm/crypto/ripemd160.hpp>
#include <mvm/crypto/secp256k1.hpp>
#include <mvm/crypto/sha256.hpp>

#include "mvm/util.h"
#include "mvm_linker.hpp"
#include "my_extension/constants.h"
#include "my_extension/my_extension.h"
#include "xapian/xapian_manager.h"
#include "xapian/xapian_registry.h"
#include "xapian/xapian_search.h"
#include <fstream>
#include <iostream>
#include <math.h>
#include <mpfr.h>
#include <vector>

#include "my_extension/utils.h"
#include <abi_decode.hpp>
#include <abi_encode.hpp>
#include <algorithm>   // Thêm thư viện này để dùng transform()
#include <arpa/inet.h> // For ntohl
#include <charconv>
#include <cstring> // Cần cho memcpy
#include <ctime>
#include <filesystem>
#include <random>
#include <span>
#include <sstream>
#include <unordered_map>
#include <xapian.h>

#define DBNAME_MAX_LEN 128 // Giới hạn cho dbname
#define TERM_MAX_LEN 64
extern "C" {
#include <secp256k1.h>
#include <secp256k1_recovery.h> // Cần thiết cho chức năng phục hồi khóa công khai
}

using namespace evmmax::bn254;

template <typename T>
T getJsonValue(const json &j, const std::string &path1,
               const std::string &path2, T defaultValue) {
  try {
    return j[path1][path2].get<T>();
  } catch (...) {
    return defaultValue;
  }
}

void printHex(const std::vector<uint8_t> &bytes) {
  for (uint8_t byte : bytes) {
    std::cout << std::hex << std::setw(2) << std::setfill('0')
              << static_cast<int>(byte);
  }
  std::cout << std::dec << std::endl;
}
mvm::Code convertToCode(Extension_return data) {
  std::vector<uint8_t> vec(data.data_p, data.data_p + data.data_size);
  return vec;
}

std::vector<uint8_t> hexString32ToBytes(const std::string &hex_input_str) {
  // 1. Check if the input hex string has the correct length (64 characters)
  //    Optionally handle "0x" prefix if your ABI decode includes it.
  std::string hex_to_convert = hex_input_str;
  if (hex_to_convert.rfind("0x", 0) == 0 ||
      hex_to_convert.rfind("0X", 0) == 0) {
    hex_to_convert = hex_to_convert.substr(2); // Remove "0x" prefix
  }

  if (hex_to_convert.length() != 64) {
    throw std::invalid_argument(
        "Input hex string must represent 32 bytes (be 64 characters long). "
        "Actual length after prefix removal: " +
        std::to_string(hex_to_convert.length()));
  }

  std::vector<uint8_t> result_bytes;
  result_bytes.reserve(32); // Reserve space for 32 bytes

  // 2. Convert hex pairs to bytes
  for (size_t i = 0; i < hex_to_convert.length(); i += 2) {
    std::string byte_string = hex_to_convert.substr(i, 2);
    try {
      // Use stoul (string to unsigned long) with base 16
      uint8_t byte = static_cast<uint8_t>(std::stoul(byte_string, nullptr, 16));
      result_bytes.push_back(byte);
    } catch (const std::invalid_argument &e) {
      throw std::invalid_argument("Invalid hex character found in string: " +
                                  byte_string);
    } catch (const std::out_of_range &e) {
      // This shouldn't happen for 2 hex chars, but good practice
      throw std::out_of_range("Hex value out of range for uint8_t: " +
                              byte_string);
    }
  }

  // 3. Final check (should always be 32 if logic above is correct)
  if (result_bytes.size() != 32) {
    // This indicates an internal logic error
    throw std::runtime_error(
        "Internal error: Conversion resulted in unexpected byte count: " +
        std::to_string(result_bytes.size()));
  }

  return result_bytes;
}
std::vector<uint8_t> ecrecover(const std::vector<uint8_t> &message_hash,
                               uint8_t v, // Should be 27 or 28 usually
                               const std::vector<uint8_t> &r,
                               const std::vector<uint8_t> &s) {
  std::cerr << "ecrecover" << std::endl;

  // 1. Validation
  if (message_hash.size() != 32) {
    std::cerr << "Error: Message hash must be 32 bytes long." << std::endl;
    return {}; // Return empty vector on failure
  }
  std::cerr << "ecrecover 1" << std::endl;

  if (r.size() != 32) {
    std::cerr << "Error: Signature 'r' component must be 32 bytes long."
              << std::endl;
    return {};
  }
  std::cerr << "ecrecover 2" << std::endl;

  if (s.size() != 32) {
    std::cerr << "Error: Signature 's' component must be 32 bytes long."
              << std::endl;
    return {};
  }
  // Note: v validation allowing 27/28 happens below when calculating recid

  // 2. Prepare for libsecp256k1
  // Use SECP256K1_CONTEXT_RECOVER for recovery functions

  secp256k1_context *ctx = secp256k1_context_create(SECP256K1_CONTEXT_SIGN |
                                                    SECP256K1_CONTEXT_VERIFY);
  if (ctx == nullptr) {
    std::cerr << "Error: secp256k1_context_create returned nullptr."
              << std::endl;
    return {};
  }
  std::cerr << "ecrecover 4" << std::endl;

  // Calculate recovery ID (recid). Common Ethereum values for v are 27 and 28.
  // recid is typically 0 or 1 for these. libsecp256k1 expects 0, 1, 2, or 3.
  if (v < 27 ||
      v > 34) { // Check broader range used by some systems, adjust if needed
    std::cerr << "Error: v value (" << (int)v << ") out of expected range."
              << std::endl;
    secp256k1_context_destroy(ctx);
    return {};
  }
  std::cerr << "ecrecover 5" << std::endl;

  int recid = v - 27; // Adjust based on your specific 'v' standard (e.g., some
                      // might use 0/1 directly)

  // Combine r and s into a single 64-byte array for parse_compact
  std::vector<uint8_t> input64(64);
  std::copy(r.begin(), r.end(), input64.begin()); // Copy r to first 32 bytes
  std::copy(s.begin(), s.end(),
            input64.begin() + 32); // Copy s to next 32 bytes

  secp256k1_ecdsa_recoverable_signature recoverable_sig;

  // Parse the compact signature (r, s, recid)
  if (!secp256k1_ecdsa_recoverable_signature_parse_compact(
          ctx, &recoverable_sig, input64.data(), recid)) {
    std::cerr << "Error: Failed to parse compact signature (invalid r, s, or "
                 "recid?). recid used: "
              << recid << std::endl;
    secp256k1_context_destroy(ctx);
    return {}; // Return empty vector on failure
  }
  std::cerr << "ecrecover 6" << std::endl;

  // 3. Recover the public key
  secp256k1_pubkey pubkey;
  if (!secp256k1_ecdsa_recover(ctx, &pubkey, &recoverable_sig,
                               message_hash.data())) {
    std::cerr << "Error: Failed to recover public key from signature."
              << std::endl;
    secp256k1_context_destroy(ctx);
    return {};
  }

  // 4. Serialize the public key to uncompressed format (65 bytes, starting with
  // 0x04)
  std::vector<uint8_t> pubkey_serialized(65);
  size_t output_len = pubkey_serialized.size();
  if (!secp256k1_ec_pubkey_serialize(ctx, pubkey_serialized.data(), &output_len,
                                     &pubkey, SECP256K1_EC_UNCOMPRESSED)) {
    std::cerr << "Error: Failed to serialize public key." << std::endl;
    secp256k1_context_destroy(ctx);
    return {};
  }

  // Check if serialization produced the expected 65 bytes for uncompressed
  // format
  if (output_len != 65 || pubkey_serialized[0] != 0x04) {
    std::cerr
        << "Error: Unexpected serialized public key format or length. Length: "
        << output_len << std::endl;
    secp256k1_context_destroy(ctx);
    return {};
  }

  // 5. Calculate the Ethereum address
  // Hash the public key bytes (excluding the 0x04 prefix byte) using Keccak-256
  mvm::KeccakHash pubkey_hash =
      mvm::keccak_256(pubkey_serialized.data() + 1,
                      64); // Hash the 64 bytes X and Y coordinates

  // The Ethereum address is the last 20 bytes of the Keccak-256 hash
  std::vector<uint8_t> address_bytes;
  address_bytes.reserve(20);
  // Copy bytes from index 12 to the end (total 32 bytes in hash, 32 - 12 = 20
  // bytes)
  std::copy(pubkey_hash.begin() + 12, pubkey_hash.end(),
            std::back_inserter(address_bytes));
  std::cerr << "ecrecover 7" << std::endl;

  // 6. Clean up the secp256k1 context
  secp256k1_context_destroy(ctx);
  std::cerr << "ecrecover 8" << std::endl;

  // 7. Return the 20-byte address
  return address_bytes;
}

std::optional<uint8_t> getFirstByteFromString(const std::string &input_str) {
  if (input_str.empty()) {
    std::cerr << "Error: Input string is empty." << std::endl;
    return std::nullopt;
  }
  return reinterpret_cast<const uint8_t *>(input_str.data())[0];
}

mvm::Code MyExtension::CallGetApi(mvm::Code input) {
  return convertToCode(ExtensionCallGetApi(input.data(), input.size()));
}

mvm::Code MyExtension::ExtractJsonField(mvm::Code input) {
  return convertToCode(ExtensionExtractJsonField(input.data(), input.size()));
}

mvm::Code MyExtension::Blst(mvm::Code input) {
  return convertToCode(ExtensionBlst(input.data(), input.size()));
}

mvm::Code MyExtension::Math(mvm::Code input) {
  // Check for valid input size
  if (input.size() < 4) {
    return mvm::Code(32, 0); // Return error for invalid input
  }

  // Get operation code from first 4 bytes
  uint32_t opCode =
      (input[0] << 24) | (input[1] << 16) | (input[2] << 8) | input[3];
  std::vector<uint8_t> remainingBytes(input.begin() + 4, input.end());

  // Initialize MPFR variables with precision
  mpfr_t result, num1, num2;
  mpfr_init2(result, 256);
  mpfr_init2(num1, 256);
  mpfr_init2(num2, 256);

  // Parse first number if available
  if (remainingBytes.size() >= 32) {
    std::vector<uint8_t> firstNumber(remainingBytes.begin(),
                                     remainingBytes.begin() + 32);
    mvm::hexToSignedInt(num1, firstNumber);

    // Scale down by SCALE_FACTOR (1e18)
    mpfr_t divisor;
    mpfr_init_set_d(divisor, SCALE_FACTOR, MPFR_RNDN);
    mpfr_div(num1, num1, divisor, MPFR_RNDN);
    mpfr_clear(divisor);
  }

  // Parse second number if available
  if (remainingBytes.size() == 64) {
    std::vector<uint8_t> secondNumber(remainingBytes.begin() + 32,
                                      remainingBytes.begin() + 64);
    mvm::hexToSignedInt(num2, secondNumber);

    // Scale down by SCALE_FACTOR (1e18)
    mpfr_t divisor;
    mpfr_init_set_d(divisor, SCALE_FACTOR, MPFR_RNDN);
    mpfr_div(num2, num2, divisor, MPFR_RNDN);
    mpfr_clear(divisor);

    // Binary operations (two operands)
    if (opCode == mvm::FunctionSelector::ADD) {
      mpfr_add(result, num1, num2, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::SUB) {
      mpfr_sub(result, num1, num2, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::MUL) {
      mpfr_mul(result, num1, num2, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::DIV) {
      mpfr_div(result, num1, num2, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::POW) {
      mpfr_pow(result, num1, num2, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::ATAN2) {
      mpfr_atan2(result, num1, num2, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::MOD) {
      mpfr_fmod(result, num1, num2, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::ROOT) {
      mpfr_t inverse;
      mpfr_init2(inverse, 256);
      mpfr_ui_div(inverse, 1, num2, MPFR_RNDN);
      mpfr_pow(result, num1, inverse, MPFR_RNDN);
      mpfr_clear(inverse);
    } else if (opCode == mvm::FunctionSelector::GCD ||
               opCode == mvm::FunctionSelector::LCM) {
      mpz_t int_x, int_y, res;
      mpz_init(int_x);
      mpz_init(int_y);
      mpz_init(res);

      // Convert floats to integers
      mpfr_get_z(int_x, num1, MPFR_RNDN);
      mpfr_get_z(int_y, num2, MPFR_RNDN);

      // Calculate GCD or LCM
      if (opCode == mvm::FunctionSelector::GCD) {
        mpz_gcd(res, int_x, int_y);
      } else {
        mpz_lcm(res, int_x, int_y);
      }

      // Convert result back to float
      mpfr_set_z(result, res, MPFR_RNDN);

      // Free GMP memory
      mpz_clear(int_x);
      mpz_clear(int_y);
      mpz_clear(res);
    } else {
      mpfr_clear(result);
      mpfr_clear(num1);
      mpfr_clear(num2);
      return mvm::Code(32, 0); // Invalid operation
    }
  } else if (remainingBytes.size() == 32) {
    // Unary operations (one operand)
    if (opCode == mvm::FunctionSelector::ABS) {
      mpfr_abs(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::SIN) {
      mpfr_sin(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::COS) {
      mpfr_cos(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::TAN) {
      mpfr_tan(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::ASIN) {
      mpfr_asin(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::ACOS) {
      mpfr_acos(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::ATAN) {
      mpfr_atan(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::SINH) {
      mpfr_sinh(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::COSH) {
      mpfr_cosh(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::TANH) {
      mpfr_tanh(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::EXP) {
      mpfr_exp(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::LOG) {
      mpfr_log(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::LOG10) {
      mpfr_log10(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::LOG2) {
      mpfr_log2(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::SQRT) {
      mpfr_sqrt(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::CEIL) {
      mpfr_ceil(result, num1);
    } else if (opCode == mvm::FunctionSelector::FLOOR) {
      mpfr_floor(result, num1);
    } else if (opCode == mvm::FunctionSelector::ROUND) {
      mpfr_round(result, num1);
    } else if (opCode == mvm::FunctionSelector::COT) {
      mpfr_cot(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::CSC) {
      mpfr_csc(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::SEC) {
      mpfr_sec(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::EXP2) {
      mpfr_exp2(result, num1, MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::SIGN) {
      mpfr_set_si(result, mpfr_sgn(num1), MPFR_RNDN);
    } else if (opCode == mvm::FunctionSelector::ENCODE_MPFR) {
      std::vector<uint8_t> encodedResult = mvm::evm_encode_mpfr(num1);
      mpfr_clear(result);
      mpfr_clear(num1);
      mpfr_clear(num2);
      return encodedResult;
    } else {
      mpfr_clear(result);
      mpfr_clear(num1);
      mpfr_clear(num2);
      return mvm::Code(32, 0); // Invalid operation
    }
  } else if (remainingBytes.empty()) {
    // Constants
    if (opCode == mvm::FunctionSelector::PI) {
      mpfr_const_pi(result, MPFR_RNDN);
    } else {
      mpfr_clear(result);
      mpfr_clear(num1);
      mpfr_clear(num2);
      return mvm::Code(32, 0); // Invalid operation
    }
  } else {
    // Invalid input size
    mpfr_clear(result);
    mpfr_clear(num1);
    mpfr_clear(num2);
    return mvm::Code(32, 0);
  }

  // Scale result back by SCALE_FACTOR
  mpfr_t scaleFactor;
  mpfr_init_set_str(scaleFactor, "1e18", 10, MPFR_RNDN);
  mpfr_mul(result, result, scaleFactor, MPFR_RNDN);
  mpfr_clear(scaleFactor);

  // Convert result to bytes
  std::vector<uint8_t> resultBytes;
  mvm::signedIntToHex(resultBytes, result);

  // Clean up MPFR variables
  mpfr_clear(result);
  mpfr_clear(num1);
  mpfr_clear(num2);

  return resultBytes;
}

// Hàm mã hóa offset thành 32-byte (big-endian)
std::vector<uint8_t> encode_offset(uint32_t offset) {
  std::vector<uint8_t> encoded(WORD_SIZE, 0);
  encoded[31] = offset & 0xFF; // Chỉ lưu 1 byte cuối
  return encoded;
}

// Hàm mã hóa số nguyên thành 32-byte (big-endian)
std::vector<uint8_t> encode_length(uint32_t length) {
  std::vector<uint8_t> encoded(WORD_SIZE, 0);
  encoded[31] = length & 0xFF; // Chỉ lưu 1 byte cuối
  return encoded;
}

// Hàm mã hóa chuỗi theo EVM ABI
std::vector<uint8_t> evm_encode_string(const std::string &input) {
  uint32_t len = input.length();
  uint32_t padded_len = ((len + WORD_SIZE - 1) / WORD_SIZE) *
                        WORD_SIZE; // Căn chỉnh bội số của 32

  std::vector<uint8_t> encoded;

  // Thêm offset (luôn là 32)
  std::vector<uint8_t> offset_bytes = encode_offset(WORD_SIZE);
  encoded.insert(encoded.end(), offset_bytes.begin(), offset_bytes.end());

  // Thêm độ dài chuỗi (32 byte)
  std::vector<uint8_t> length_bytes = encode_length(len);
  encoded.insert(encoded.end(), length_bytes.begin(), length_bytes.end());

  // Thêm nội dung chuỗi (UTF-8)
  encoded.insert(encoded.end(), input.begin(), input.end());

  // Thêm padding 0x00 nếu cần
  encoded.resize(encoded.size() + (padded_len - len), 0);

  return encoded;
}

mvm::Code MyExtension::SimpleDatabase(mvm::Code input, mvm::Address address) {
  // Check for valid input size
  if (input.size() < 4) {
    return mvm::Code(32, 0); // Return error for invalid input
  }

  // Get operation code from first 4 bytes
  uint32_t opCode =
      (input[0] << 24) | (input[1] << 16) | (input[2] << 8) | input[3];
  if (this->isOffChain)
    {
        bool isWriteOp = (
            opCode == mvm::FunctionSelector::SET ||
            opCode == mvm::FunctionSelector::SINPLE_DB_DELETE ||
            opCode == mvm::FunctionSelector::GET_OR_CREATE_SIMPLE_DB
        );
        if (isWriteOp)
        {
            // Off-chain: trả về success giả mà không thực sự ghi
            return mvm::Code(32, 1);
        }
    }
  // Address là uint256_t
  uint256_t addr = address;

  // Chuyển đổi uint256_t thành mảng byte
  std::vector<uint8_t> addressBytes(20); // 256 bits = 32 bytes

  // Sao chép dữ liệu từ uint256_t vào addressBytes
  for (size_t i = 0; i < 20; ++i) {
    addressBytes[19 - i] = static_cast<uint8_t>(addr >> (i * 8));
  }

  Extension_return data = ExtensionGetOrCreateSimpleDb(
      input.data(), input.size(), addressBytes.data(), this->mvmId);

  if (opCode == mvm::FunctionSelector::GET_OR_CREATE_SIMPLE_DB ||
      opCode == mvm::FunctionSelector::SET ||
      opCode == mvm::FunctionSelector::GET ||
      opCode == mvm::FunctionSelector::GET_ALL ||
      opCode == mvm::FunctionSelector::SEARCH_BY_VALUE ||
      opCode == mvm::FunctionSelector::SINPLE_DB_DELETE ||
      opCode == mvm::FunctionSelector::SINPLE_GET_NEXT_KEYS) {
    return convertToCode(data);
  }
  return mvm::Code(32, 0);
}

// Hàm chuyển hex string thành vector<uint8_t>
std::vector<uint8_t> hexToBytes(const std::string &hex) {
  std::vector<uint8_t> bytes;
  for (size_t i = 0; i < hex.length(); i += 2) {
    std::string byteString = hex.substr(i, 2);
    uint8_t byte = (uint8_t)strtol(byteString.c_str(), nullptr, 16);
    bytes.push_back(byte);
  }
  return bytes;
}

std::string uint32ToHexString(uint32_t value) {
  std::stringstream ss;
  ss << std::hex << std::setfill('0') << std::setw(8)
     << value; // 8 hex digits for uint32_t
  return ss.str();
}

// Hàm kiểm tra ABI hợp lệ

bool parseABI(const std::vector<uint8_t> &data, std::string &selector_out,
              std::string &extracted_string) {
  if (data.size() < 68) {
    std::cerr << "ABI Error: Data too short (less than 68 bytes)." << std::endl;
    return false;
  }

  std::stringstream selector_ss;
  for (int i = 0; i < 4; i++) {
    selector_ss << std::hex << std::setw(2) << std::setfill('0')
                << static_cast<int>(data[i]);
  }
  selector_out = selector_ss.str();

  uint32_t offset;
  std::memcpy(&offset, &data[32], sizeof(offset));
  offset = ntohl(offset);

  if (offset != 32) {
    std::cerr << "ABI Error: Invalid string offset (" << offset << ")."
              << std::endl;
    return false;
  }

  uint32_t str_length;
  std::memcpy(&str_length, &data[64], sizeof(str_length));
  str_length = ntohl(str_length);

  if (str_length == 0) {
    std::cerr << "ABI Error: String length is zero." << std::endl;
    return false;
  }

  if (offset + str_length > data.size()) {
    std::cerr << "ABI Error: String data exceeds data size." << std::endl;
    return false;
  }

  // Kiểm tra padding (nếu cần)
  // ...

  extracted_string.resize(str_length);
  std::copy(data.begin() + 68, data.begin() + 68 + str_length,
            extracted_string.begin());

  return true;
}
std::string decimalToHex(int decimal) {
  std::stringstream ss;
  ss << std::hex << decimal;
  return ss.str();
}

vector<uint8_t> encodeStringArray(const vector<string> &docInfo) {
  vector<uint8_t> result;

  // 1. Đầu tiên encode độ dài của mảng (offset 0x20)
  result.insert(result.end(), 31, 0x00);
  result.push_back(0x20);

  // 2. Encode số lượng phần tử trong mảng
  uint32_t length = docInfo.size();
  for (int i = 0; i < 28; i++) {
    result.push_back(0x00);
  }
  result.push_back((length >> 24) & 0xFF);
  result.push_back((length >> 16) & 0xFF);
  result.push_back((length >> 8) & 0xFF);
  result.push_back(length & 0xFF);

  // 3. Tính và thêm các offset cho từng string
  uint32_t currentOffset =
      32 * (docInfo.size() + 1); // offset bắt đầu sau phần header
  for (size_t i = 0; i < docInfo.size(); i++) {
    for (int j = 0; j < 28; j++) {
      result.push_back(0x00);
    }
    result.push_back((currentOffset >> 24) & 0xFF);
    result.push_back((currentOffset >> 16) & 0xFF);
    result.push_back((currentOffset >> 8) & 0xFF);
    result.push_back(currentOffset & 0xFF);

    currentOffset += 32 + ((docInfo[i].length() + 31) / 32) * 32;
  }

  // 4. Encode từng string
  for (const string &str : docInfo) {
    // Encode độ dài của string
    uint32_t strLength = str.length();
    for (int i = 0; i < 28; i++) {
      result.push_back(0x00);
    }
    result.push_back((strLength >> 24) & 0xFF);
    result.push_back((strLength >> 16) & 0xFF);
    result.push_back((strLength >> 8) & 0xFF);
    result.push_back(strLength & 0xFF);

    // Encode nội dung string
    result.insert(result.end(), str.begin(), str.end());

    // Padding cho đủ 32 bytes
    size_t padding = (32 - (str.length() % 32)) % 32;
    result.insert(result.end(), padding, 0x00);
  }

  return result;
}
// Hoặc phiên bản ngắn gọn hơn:
void printDocInfo(const std::vector<std::string> &docInfo) {
  std::cout << "[\n";
  for (const auto &str : docInfo) {
    std::cout << "  \"" << str << "\",\n";
  }
  std::cout << "]" << std::endl;
}
// Helper functions
std::vector<uint8_t> getInputWithoutOpcode(const mvm::Code &input) {
  std::vector<uint8_t> result;
  result.reserve(input.size() - 4);
  for (size_t i = 4; i < input.size(); ++i) {
    result.push_back(input[i]);
  }
  return result;
}

// Function to format hex for display
void printHexInput(const mvm::Code &input) {
  std::cout << "FullDatabase Opcode 2 (hex): 0x";
  for (uint8_t byte : input) {
    std::cout << std::hex << std::setw(2) << std::setfill('0') << (int)byte;
  }
  std::cout << std::endl;
}

// Hàm chuyển đổi một string thành std::vector<uint8_t> theo chuẩn ABI
std::vector<uint8_t> toABI(const std::string &str) {
  // Tạo vector chứa các byte của chuỗi
  std::vector<uint8_t> result(
      32, 0); // Đảm bảo kích thước là 32 bytes (32 * 8 = 256 bits)

  // Chuyển chuỗi thành vector<uint8_t> và chèn vào đầu vector (đảm bảo dữ liệu
  // bắt đầu từ đầu)
  for (size_t i = 0; i < str.size() && i < 32; ++i) {
    result[i] = static_cast<uint8_t>(str[i]);
  }

  return result;
}

// Hàm chuyển đổi 2 string thành vector<uint8_t> thỏa mãn chuẩn ABI
std::vector<uint8_t> convertStringsToABI(const std::string &str1,
                                         const std::string &str2) {
  std::vector<uint8_t> abiData;

  // Chuyển đổi từng chuỗi thành vector<uint8_t> theo chuẩn ABI
  std::vector<uint8_t> abi1 = toABI(str1);
  std::vector<uint8_t> abi2 = toABI(str2);

  // Thêm vào vector kết quả theo chuẩn ABI (mỗi chuỗi 32 bytes)
  abiData.insert(abiData.end(), abi1.begin(), abi1.end());
  abiData.insert(abiData.end(), abi2.begin(), abi2.end());

  return abiData;
}

// Hàm tách chuỗi tại dấu ":"
std::pair<std::string, std::string> splitString(const std::string &input) {
  size_t pos = input.find(":");

  if (pos != std::string::npos) {
    // Trả về một std::pair chứa hai phần chuỗi
    return {input.substr(0, pos), input.substr(pos + 1)};
  } else {
    // Nếu không tìm thấy ":", trả về hai chuỗi rỗng
    return {"", ""};
  }
}

uint64_t hex_to_uint64(const std::string &hex_str) {
  uint64_t result = 0;
  std::istringstream(hex_str) >> std::hex >> result;
  return result;
}
std::optional<int64_t> hex_to_int64(const std::string &hex_str) {
  // Kiểm tra chuỗi rỗng
  if (hex_str.empty()) {
    return std::nullopt;
  }

  // Con trỏ bắt đầu và kết thúc của chuỗi gốc
  const char *start_parse_ptr = hex_str.data();
  const char *const end_ptr_str = start_parse_ptr + hex_str.length();

  // Xử lý tiền tố "0x" hoặc "0X"
  if (hex_str.length() >= 2 && hex_str[0] == '0' &&
      (hex_str[1] == 'x' || hex_str[1] == 'X')) {
    start_parse_ptr += 2; // Di chuyển con trỏ qua tiền tố
  }

  // Kiểm tra nếu không còn gì sau tiền tố (vd: chuỗi là "0x")
  if (start_parse_ptr == end_ptr_str) {
    return std::nullopt;
  }

  // Tính số lượng ký tự hex còn lại
  size_t num_hex_digits = end_ptr_str - start_parse_ptr;

  // *** Logic cắt bớt ***
  // Nếu số ký tự nhiều hơn 16, điều chỉnh con trỏ bắt đầu để chỉ lấy 16 ký tự
  // cuối
  if (num_hex_digits > 16) {
    start_parse_ptr = end_ptr_str - 16; // Đặt con trỏ vào đầu của 16 ký tự cuối
    num_hex_digits = 16;                // Chỉ xử lý 16 ký tự
  }
  // *** Kết thúc logic cắt bớt ***

  // Kiểm tra nếu sau khi điều chỉnh không còn ký tự nào (trường hợp hy hữu)
  if (num_hex_digits == 0) {
    return std::nullopt;
  }

  // Phân tích phần hex (tối đa 16 ký tự) thành uint64_t
  uint64_t unsigned_val = 0;
  const char *end_parse_ptr =
      start_parse_ptr + num_hex_digits; // Con trỏ kết thúc phần cần phân tích
  auto result = std::from_chars(start_parse_ptr, end_parse_ptr, unsigned_val,
                                16); // Cơ số 16

  // Kiểm tra lỗi từ std::from_chars
  if (result.ec != std::errc()) {
    // Có lỗi (ký tự không hợp lệ hoặc ngoài phạm vi uint64_t)
    return std::nullopt;
  }

  // Kiểm tra xem std::from_chars có xử lý hết các ký tự dự kiến không
  if (result.ptr != end_parse_ptr) {
    // Không xử lý hết (ví dụ: "123G" trong 16 ký tự cuối)
    return std::nullopt;
  }

  // Sử dụng memcpy để diễn giải lại các bit từ unsigned_val sang signed_val
  int64_t signed_val;
  // Đảm bảo kích thước khớp nhau tại thời điểm biên dịch - rất quan trọng cho
  // memcpy
  static_assert(sizeof(signed_val) == sizeof(unsigned_val),
                "Size mismatch between int64_t and uint64_t");
  std::memcpy(&signed_val, &unsigned_val, sizeof(signed_val));

  // Trả về giá trị đã diễn giải
  return signed_val;
}

// Hàm chuyển đổi chuỗi hex string thành vector<uint8_t>
std::vector<uint8_t> hexStringToByteVector(const std::string &hexString) {
  std::vector<uint8_t> byteVector;
  // Duyệt qua từng cặp ký tự hex trong chuỗi
  for (size_t i = 0; i < hexString.length(); i += 2) {
    // Chuyển cặp ký tự hex thành giá trị uint8_t
    std::string byteStr = hexString.substr(i, 2);
    uint8_t byte = static_cast<uint8_t>(std::stoul(byteStr, nullptr, 16));
    byteVector.push_back(byte);
  }
  return byteVector;
}


// ============================================================================
// NOTE: FullDatabase (Xapian handlers) extracted to xapian_handlers.cpp
// NOTE: Crypto handlers extracted to crypto_handlers.cpp
// The build system must include both files alongside this file.
// ============================================================================
