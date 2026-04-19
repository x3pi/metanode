// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.
// NOTE: This file was extracted from my_extension.cpp for maintainability.
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
#include "my_extension/utils.h"
#include <algorithm>
#include <arpa/inet.h>
#include <charconv>
#include <cstring>
#include <ctime>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <math.h>
#include <mpfr.h>
#include <random>
#include <span>
#include <sstream>
#include <unordered_map>
#include <vector>
using namespace evmmax::bn254;

// Forward declarations for helpers defined in my_extension.cpp
extern std::vector<uint8_t>
hexString32ToBytes(const std::string &hex_input_str);
extern std::vector<uint8_t> ecrecover(const std::vector<uint8_t> &message_hash,
                                      uint8_t v, const std::vector<uint8_t> &r,
                                      const std::vector<uint8_t> &s);
extern void printHex(const std::vector<uint8_t> &bytes);
extern std::vector<uint8_t> getInputWithoutOpcode(const mvm::Code &input);

// Forward declaration for ABI decode/encode (defined in
// abi_decode.hpp/abi_encode.hpp, already compiled via my_extension.cpp — do NOT
// include here to avoid duplicate symbols)
extern nlohmann::json decode(std::vector<uint8_t> bytes, std::string strAbi);

mvm::Code MyExtension::Ecrecover(mvm::Code input) {
  try {
    // Kiểm tra kích thước input hợp lệ
    if (input.size() < 4) {
      std::cerr << "Error: Input size too small!" << std::endl;
      return mvm::Code(32, 0);
    }

    // Lấy opcode từ 4 byte đầu tiên
    uint32_t opCode =
        (input[0] << 24) | (input[1] << 16) | (input[2] << 8) | input[3];

    std::string inputABI = R"([
                  {"internalType": "bytes32", "name": "hash", "type": "bytes32"},
                  {"internalType": "uint256", "name": "v", "type": "uint8"},
                  {"internalType": "uint256", "name": "r", "type": "bytes32"},
                  {"internalType": "bool", "name": "s", "type": "bytes32"}
              ])";

    nlohmann::json input_argument = decode(input, inputABI);

    std::string hash = input_argument["hash"];
    std::string v = input_argument["v"];
    std::string r = input_argument["r"];
    std::string s = input_argument["s"];

    uint64_t v_uint64 = 0;

    // Assuming decode returns v as a hex string representation of uint8
    v_uint64 =
        std::stoull(input_argument.at("v").get<std::string>(), nullptr, 16);

    if (v_uint64 != 27 && v_uint64 != 28) { // Or other valid range if needed
      std::cerr << "Error: Invalid v value decoded from ABI: " << v_uint64
                << ". Expected 27 or 28." << std::endl;
      return mvm::Code(32, 0); // Return error immediately
    }
    uint8_t v_val = static_cast<uint8_t>(
        v_uint64); // This holds the correct v (e.g., 27 or 28)

    auto hbyte = hexString32ToBytes(hash);

    auto rbyte = hexString32ToBytes(r);
    auto sbyte = hexString32ToBytes(s);

    auto address = ecrecover(hbyte, v_val, rbyte, sbyte);
    mvm::Code result_code(32, 0);
    if (!address.empty() && address.size() == 20) {
      std::copy(address.begin(), address.end(), result_code.begin() + 12);
    } else if (address.empty()) {
      std::cerr << "ecrecover returned empty address, check logs." << std::endl;
    } else {
      std::cerr << "ecrecover returned address of unexpected size: "
                << address.size() << std::endl;
    }

    return result_code;
  } catch (const std::exception &e) {
    std::cerr << "[Ecrecover] Exception during execution: " << e.what()
              << std::endl;
    return mvm::Code(32, 0);
  } catch (...) {
    std::cerr << "[Ecrecover] Unknown exception during execution." << std::endl;
    return mvm::Code(32, 0);
  }
}

mvm::Code MyExtension::Sha256(mvm::Code input) {
  try {
    printHex(input);

    // Chuẩn bị bộ đệm cho kết quả hash (32 bytes)
    std::vector<std::byte> hash_result_buffer(mvm::crypto::SHA256_HASH_SIZE);

    // Gọi hàm sha256
    mvm::crypto::sha256(hash_result_buffer.data(), // Con trỏ đến bộ đệm kết quả
                        reinterpret_cast<const std::byte *>(
                            input.data()), // Con trỏ đến dữ liệu đầu vào
                        input.size()       // Kích thước dữ liệu đầu vào
    );

    // Chuyển đổi kết quả std::byte thành mvm::Code (std::vector<uint8_t>)
    mvm::Code final_hash_result;
    final_hash_result.resize(mvm::crypto::SHA256_HASH_SIZE);
    for (size_t i = 0; i < mvm::crypto::SHA256_HASH_SIZE; ++i) {
      final_hash_result[i] = static_cast<uint8_t>(hash_result_buffer[i]);
    }

    return final_hash_result; // Trả về hash 32 byte
  } catch (const std::exception &e) {
    std::cerr << "[Sha256] Exception during execution: " << e.what()
              << std::endl;
    return mvm::Code(32, 0);
  } catch (...) {
    std::cerr << "[Sha256] Unknown exception during execution." << std::endl;
    return mvm::Code(32, 0);
  }
}

mvm::Code MyExtension::Ripemd160(mvm::Code input) {
  try {
    // Chuẩn bị bộ đệm cho kết quả hash (20 bytes)
    std::vector<std::byte> hash_result_buffer(mvm::crypto::RIPEMD160_HASH_SIZE);

    // Gọi hàm ripemd160
    mvm::crypto::ripemd160(
        hash_result_buffer.data(), // Con trỏ đến bộ đệm kết quả
        reinterpret_cast<const std::byte *>(
            input.data()), // Con trỏ đến dữ liệu đầu vào
        input.size()       // Kích thước dữ liệu đầu vào
    );                     // Thêm noexcept nếu hàm thực sự là noexcept

    // Chuyển đổi kết quả std::byte thành mvm::Code (std::vector<uint8_t>)
    // Kết quả của RIPEMD160 là 20 byte, nhưng precompile Ethereum thường trả về
    // 32 byte với 12 byte đầu là 0.
    mvm::Code final_hash_result(32, 0); // Tạo vector 32 byte chứa giá trị 0
    // Sao chép 20 byte hash vào 20 byte cuối của vector kết quả
    for (size_t i = 0; i < mvm::crypto::RIPEMD160_HASH_SIZE; ++i) {
      final_hash_result[12 + i] = static_cast<uint8_t>(hash_result_buffer[i]);
    }

    return final_hash_result; // Trả về hash 32 byte (đã đệm)
  } catch (const std::exception &e) {
    std::cerr << "[Ripemd160] Exception during execution: " << e.what()
              << std::endl;
    return mvm::Code(32, 0);
  } catch (...) {
    std::cerr << "[Ripemd160] Unknown exception during execution." << std::endl;
    return mvm::Code(32, 0);
  }
}

// --- Hàm trợ giúp để in vector byte dưới dạng hex ---
std::string bytes_to_hex_string(const std::vector<uint8_t> &bytes) {
  std::ostringstream oss;
  oss << "0x";
  for (uint8_t b : bytes) {
    oss << std::hex << std::setw(2) << std::setfill('0') << static_cast<int>(b);
  }
  return oss.str();
}

// Hàm helper mới để đọc 32 bytes và chuyển thành uint32_t
// Trả về giá trị hoặc ném lỗi nếu giá trị quá lớn cho uint32_t
uint32_t read_uint256_as_uint32_be(const std::vector<uint8_t> &data,
                                   size_t offset) {
  if (offset + 32 > data.size()) {
    std::cerr << "[MODEXP LOG][ERROR] read_uint256_as_uint32_be out of range "
                 "at offset "
              << offset << std::endl;
    // Ném lỗi hoặc trả về giá trị đặc biệt tùy theo cách xử lý lỗi của bạn
    throw std::out_of_range("read_uint256_as_uint32_be out of range");
  }

  // Sử dụng hàm từ thư viện intx (giả sử mvm::from_big_endian tương tự)
  uint256_t value256 = mvm::from_big_endian(data.data() + offset, 32);

  // Kiểm tra xem giá trị có nằm trong giới hạn của uint32_t không
  if (value256 > std::numeric_limits<uint32_t>::max()) {
    std::cerr << "[MODEXP LOG][ERROR] Size value read from offset " << offset
              << " exceeds uint32_t max." << std::endl;
    throw std::overflow_error("Size value exceeds uint32_t max");
  }

  return static_cast<uint32_t>(value256); // Ép kiểu an toàn sau khi kiểm tra
}

mvm::Code MyExtension::Modexp(mvm::Code input) {
  // --- Sử dụng GMP ---
  mpz_t gmp_base, gmp_exp, gmp_mod, gmp_result;
  mpz_inits(gmp_base, gmp_exp, gmp_mod, gmp_result, nullptr);

  try {
    // --- Log đầu vào ---
    std::cerr << "[MODEXP LOG] Function Entry. Input data (" << input.size()
              << " bytes): " << bytes_to_hex_string(input) << std::endl;

    const size_t header_size = 3 * 32;
    if (input.size() < header_size) {
      std::cerr << "[MODEXP LOG][ERROR] Input size " << input.size()
                << " is less than header size " << header_size << std::endl;
      mpz_clears(gmp_base, gmp_exp, gmp_mod, gmp_result, nullptr);
      return {}; // Input quá ngắn
    }

    // --- Log các size đọc được ---
    uint32_t b_size = read_uint256_as_uint32_be(input, 0);
    uint32_t e_size = read_uint256_as_uint32_be(input, 32);
    uint32_t m_size = read_uint256_as_uint32_be(input, 64);
    std::cerr << "[MODEXP LOG] Parsed Sizes: Bsize=" << b_size
              << ", Esize=" << e_size << ", Msize=" << m_size << std::endl;

    uint64_t data_size = static_cast<uint64_t>(b_size) + e_size + m_size;
    uint64_t expected_min_size = header_size;
    if (std::numeric_limits<uint64_t>::max() - header_size < data_size) {
      std::cerr
          << "[MODEXP LOG][ERROR] Integer overflow calculating total data size."
          << std::endl;
      mpz_clears(gmp_base, gmp_exp, gmp_mod, gmp_result, nullptr);
      return {}; // Tràn số
    }
    expected_min_size += data_size;

    if (input.size() < expected_min_size) {
      std::cerr << "[MODEXP LOG][ERROR] Input size " << input.size()
                << " is less than expected minimum size " << expected_min_size
                << std::endl;
      mpz_clears(gmp_base, gmp_exp, gmp_mod, gmp_result, nullptr);
      return {}; // Kích thước input không đủ
    }

    // --- Log các offset tính toán được ---
    const size_t b_offset = header_size;
    const size_t e_offset = b_offset + b_size;
    const size_t m_offset = e_offset + e_size;
    std::cerr << "[MODEXP LOG] Calculated Offsets: B=" << b_offset
              << ", E=" << e_offset << ", M=" << m_offset << std::endl;

    std::cerr << "[MODEXP LOG] GMP variables initialized." << std::endl;

    std::vector<uint8_t> mutable_input = input; // Tạo bản sao không const

    // Import và Log Base
    if (b_size > 0) {
      mpz_import(gmp_base, b_size, 1, sizeof(uint8_t), 1, 0,
                 mutable_input.data() + b_offset);
      char *b_str = mpz_get_str(nullptr, 16, gmp_base); // Lấy chuỗi hex
      std::cerr << "[MODEXP LOG] Imported Base (B): 0x"
                << (b_str ? b_str : "null") << std::endl;
      if (b_str)
        free(b_str); // Giải phóng bộ nhớ cấp phát bởi mpz_get_str
    } else {
      std::cerr << "[MODEXP LOG] Imported Base (B): 0 (size was 0)"
                << std::endl;
    }

    // Import và Log Exponent
    if (e_size > 0) {
      mpz_import(gmp_exp, e_size, 1, sizeof(uint8_t), 1, 0,
                 mutable_input.data() + e_offset);
      char *e_str = mpz_get_str(nullptr, 16, gmp_exp);
      std::cerr << "[MODEXP LOG] Imported Exponent (E): 0x"
                << (e_str ? e_str : "null") << std::endl;
      if (e_str)
        free(e_str);
    } else {
      std::cerr << "[MODEXP LOG] Imported Exponent (E): 0 (size was 0)"
                << std::endl;
    }

    // Import và Log Modulus
    if (m_size > 0) {
      mpz_import(gmp_mod, m_size, 1, sizeof(uint8_t), 1, 0,
                 mutable_input.data() + m_offset);
      char *m_str = mpz_get_str(nullptr, 16, gmp_mod);
      std::cerr << "[MODEXP LOG] Imported Modulus (M): 0x"
                << (m_str ? m_str : "null") << std::endl;
      if (m_str)
        free(m_str);
    } else {
      std::cerr << "[MODEXP LOG] Imported Modulus (M): 0 (size was 0)"
                << std::endl;
    }

    // Xử lý M = 0
    if (m_size == 0 || mpz_sgn(gmp_mod) == 0) {
      std::cerr << "[MODEXP LOG] Modulus is zero. Returning empty result."
                << std::endl;
      mpz_clears(gmp_base, gmp_exp, gmp_mod, gmp_result, nullptr);
      return {};
    }

    // --- Log trước khi tính toán ---
    char *b_str_pre = mpz_get_str(nullptr, 16, gmp_base);
    char *e_str_pre = mpz_get_str(nullptr, 16, gmp_exp);
    char *m_str_pre = mpz_get_str(nullptr, 16, gmp_mod);
    std::cerr << "[MODEXP LOG] Calling mpz_powm with B=0x"
              << (b_str_pre ? b_str_pre : "null") << ", E=0x"
              << (e_str_pre ? e_str_pre : "null") << ", M=0x"
              << (m_str_pre ? m_str_pre : "null") << std::endl;
    if (b_str_pre)
      free(b_str_pre);
    if (e_str_pre)
      free(e_str_pre);
    if (m_str_pre)
      free(m_str_pre);

    // Tính B^E mod M bằng hàm của GMP
    mpz_powm(gmp_result, gmp_base, gmp_exp, gmp_mod);

    // --- Log kết quả tính toán (dạng số nguyên lớn) ---
    char *res_str = mpz_get_str(nullptr, 16, gmp_result);
    std::cerr << "[MODEXP LOG] mpz_powm result: 0x"
              << (res_str ? res_str : "null") << std::endl;
    if (res_str)
      free(res_str);

    // Export kết quả về dạng byte array, có kích thước bằng m_size
    mvm::Code result_bytes(m_size,
                           0); // Vector kết quả có kích thước m_size, đầy số 0
    size_t bytes_written = 0;
    // Tính toán offset để căn lề phải
    size_t result_num_bytes =
        (mpz_sizeinbase(gmp_result, 2) + 7) / 8; // Số byte thực tế của kết quả
    size_t export_offset =
        (m_size > result_num_bytes) ? (m_size - result_num_bytes) : 0;

    std::cerr << "[MODEXP LOG] Exporting result. Target size=" << m_size
              << ", Actual result bytes=" << result_num_bytes
              << ", Export offset=" << export_offset << std::endl;

    mpz_export(result_bytes.data() +
                   export_offset, // Ghi vào vị trí đã tính toán
               &bytes_written, 1, sizeof(uint8_t), 1, 0, gmp_result);

    std::cerr << "[MODEXP LOG] Bytes written by mpz_export: " << bytes_written
              << std::endl;
    std::cerr << "[MODEXP LOG] Final result bytes before return ("
              << result_bytes.size()
              << " bytes): " << bytes_to_hex_string(result_bytes) << std::endl;

    // Dọn dẹp bộ nhớ GMP
    mpz_clears(gmp_base, gmp_exp, gmp_mod, gmp_result, nullptr);
    std::cerr << "[MODEXP LOG] GMP variables cleared. Function exiting."
              << std::endl;

    return result_bytes;
  } catch (const std::exception &e) {
    std::cerr << "[MODEXP LOG] Exception during execution: " << e.what()
              << std::endl;
    mpz_clears(gmp_base, gmp_exp, gmp_mod, gmp_result, nullptr);
    return {};
  } catch (...) {
    std::cerr << "[MODEXP LOG] Unknown exception during execution."
              << std::endl;
    mpz_clears(gmp_base, gmp_exp, gmp_mod, gmp_result, nullptr);
    return {};
  }
}
/**
 * @brief Implements ECADD precompile (alt_bn128 addition)
 * Mimics the style of the Sha256 example function.
 * Handles input parsing, validation, point addition, and output serialization.
 * Returns empty Code on error.
 * NOTE: Gas handling is NOT included here and must be managed by the caller.
 *
 * @param input Input data (128 bytes: x1, y1, x2, y2)
 * @return mvm::Code Output data (64 bytes: x, y) or empty Code on error.
 */
mvm::Code MyExtension::EcAdd(mvm::Code input) {
  // Use the namespace definitions from the header provided by the user
  // Using directive inside the function scope for brevity

  constexpr size_t EXPECTED_INPUT_SIZE = 128;
  constexpr size_t COORD_SIZE = 32;
  constexpr size_t OUTPUT_SIZE = 64;

  if (input.size() != EXPECTED_INPUT_SIZE) {
    std::cerr << "ECADD Error: Invalid input size. Expected "
              << EXPECTED_INPUT_SIZE << ", got " << input.size() << std::endl;
    return {}; // Return empty Code on error
  }

  try {
    // 1. Parse Input Coordinates using confirmed function from util.h
    const uint8_t *data = input.data();
    uint256_t x1_u = mvm::from_big_endian(data + 0 * COORD_SIZE);
    uint256_t y1_u = mvm::from_big_endian(data + 1 * COORD_SIZE);
    uint256_t x2_u = mvm::from_big_endian(data + 2 * COORD_SIZE);
    uint256_t y2_u = mvm::from_big_endian(data + 3 * COORD_SIZE);

    // 2. Validate Coordinates are valid Field Elements (< Modulus)
    //    Use the FieldPrime constant from the user-provided header
    if (x1_u >= FieldPrime ||
        y1_u >= FieldPrime || // <-- Use identifier directly due to 'using
                              // namespace'
        x2_u >= FieldPrime || y2_u >= FieldPrime) {
      std::cerr << "ECADD Error: Coordinate out of field range." << std::endl;
      return {}; // Return empty Code on error
    }

    // 3. Create Point objects
    //    Use the Point type from the user-provided header
    //    Assuming Fp is essentially uint256_t based on Point definition.
    Point p1 = {uint256_t(x1_u), uint256_t(y1_u)};
    Point p2 = {uint256_t(x2_u), uint256_t(y2_u)};

    // Define infinity using the standard (0,0) encoding
    const Point infinity_point = {uint256_t(0), uint256_t(0)};

    // 4. Validate Points are on the curve
    //    Use the validate function from the user-provided header
    if (p1 != infinity_point && !validate(p1)) { // <-- Use identifier directly
      std::cerr << "ECADD Error: Point P1 is not on the curve." << std::endl;
      return {};
    }
    if (p2 != infinity_point && !validate(p2)) { // <-- Use identifier directly
      std::cerr << "ECADD Error: Point P2 is not on the curve." << std::endl;
      return {};
    }

    // 5. Perform Point Addition
    //    Use the add function from the user-provided header
    Point result_p = add(p1, p2); // <-- Use identifier directly

    // 6. Serialize Output Coordinates using confirmed function from util.h
    mvm::Code output(OUTPUT_SIZE);
    mvm::to_big_endian(uint256_t(result_p.x), output.data() + 0 * COORD_SIZE);
    mvm::to_big_endian(uint256_t(result_p.y), output.data() + 1 * COORD_SIZE);

    return output; // Return the 64-byte result
  } catch (const std::exception &e) {
    std::cerr << "ECADD Error: Exception during execution: " << e.what()
              << std::endl;
    return {};
  } catch (...) {
    std::cerr << "ECADD Error: Unknown exception during execution."
              << std::endl;
    return {};
  }
}

/**
 * @brief Implements ECMUL precompile (alt_bn128 scalar multiplication)
 * Mimics the style of the EcAdd example function.
 * Handles input parsing, validation, scalar multiplication, and output
 * serialization. Returns empty Code on error. NOTE: Gas handling is NOT
 * included here and must be managed by the caller.
 *
 * @param input Input data (96 bytes: x1, y1, s)
 * @return mvm::Code Output data (64 bytes: x, y) or empty Code on error.
 */
mvm::Code MyExtension::EcMul(mvm::Code input) {
  // Use the namespace definitions from the header provided by the user

  constexpr size_t EXPECTED_INPUT_SIZE =
      96; // ECMUL input is 96 bytes (Point + Scalar)
  constexpr size_t COORD_SIZE = 32;
  constexpr size_t SCALAR_OFFSET = 2 * COORD_SIZE; // Scalar starts after x1, y1
  constexpr size_t OUTPUT_SIZE = 64;

  if (input.size() != EXPECTED_INPUT_SIZE) {
    std::cerr << "ECMUL Error: Invalid input size. Expected "
              << EXPECTED_INPUT_SIZE << ", got " << input.size() << std::endl;
    return {}; // Return empty Code on error
  }

  try {
    // 1. Parse Input Coordinates and Scalar
    const uint8_t *data = input.data();
    uint256_t x1_u = mvm::from_big_endian(data + 0 * COORD_SIZE); //
    uint256_t y1_u = mvm::from_big_endian(data + 1 * COORD_SIZE); //
    uint256_t s_u = mvm::from_big_endian(data + SCALAR_OFFSET);   //

    // 2. Validate Coordinates are valid Field Elements (< Modulus)
    //    Use the FieldPrime constant
    if (x1_u >= FieldPrime || y1_u >= FieldPrime) {
      std::cerr << "ECMUL Error: Coordinate out of field range." << std::endl;
      return {}; // Return empty Code on error
    }
    // Scalar 's' does not need to be < FieldPrime, it's interpreted mod curve
    // order usually, but the precompile spec doesn't mention failing for large
    // scalars. The underlying 'mul' function should handle the scalar
    // correctly.

    // 3. Create Point object
    //    Use the Point type
    Point p1 = {uint256_t(x1_u), uint256_t(y1_u)};

    // Define infinity using the standard (0,0) encoding
    const Point infinity_point = {uint256_t(0), uint256_t(0)};

    // 4. Validate Point is on the curve
    //    Use the validate function
    if (p1 != infinity_point && !validate(p1)) {
      std::cerr << "ECMUL Error: Input point is not on the curve." << std::endl;
      return {};
    }

    // 5. Perform Scalar Multiplication
    //    Use the mul function from the header
    Point result_p = mul(p1, s_u);

    // 6. Serialize Output Coordinates
    mvm::Code output(OUTPUT_SIZE);
    mvm::to_big_endian(uint256_t(result_p.x),
                       output.data() + 0 * COORD_SIZE); //
    mvm::to_big_endian(uint256_t(result_p.y),
                       output.data() + 1 * COORD_SIZE); //

    return output; // Return the 64-byte result
  } catch (const std::exception &e) {
    std::cerr << "ECMUL Error: Exception during execution: " << e.what()
              << std::endl;
    return {};
  } catch (...) {
    std::cerr << "ECMUL Error: Unknown exception during execution."
              << std::endl;
    return {};
  }
}

/**
 * @brief Implements ECPAIRING precompile (alt_bn128 pairing check)
 * Mimics the style of the EcAdd/EcMul example functions.
 * Handles input parsing, pairing check, and output serialization.
 * Assumes the underlying pairing_check function handles point validation.
 * Returns empty Code on error.
 * NOTE: Gas handling (base + per-pair cost) is NOT included here and must be
 * managed by the caller.
 *
 * @param input Input data (multiple of 192 bytes: [x1_g1, y1_g1, x1_re_g2,
 * x1_im_g2, y1_re_g2, y1_im_g2]...)
 * @return mvm::Code Output data (32 bytes: 1 for success, 0 for failure) or
 * empty Code on error.
 */
mvm::Code MyExtension::EcPairing(mvm::Code input) {
  // using namespace evmmax::bn254; // Adjust if needed

  constexpr size_t G1_POINT_SIZE = 64;                        // 2 * 32 bytes
  constexpr size_t G2_POINT_SIZE = 128;                       // 4 * 32 bytes
  constexpr size_t PAIR_SIZE = G1_POINT_SIZE + G2_POINT_SIZE; // 192 bytes
  constexpr size_t COORD_SIZE = 32;  // Size of one coordinate field element
  constexpr size_t OUTPUT_SIZE = 32; // Standard EVM word size

  if (input.size() % PAIR_SIZE != 0) {
    std::cerr << "[EcPairing] Error: Invalid input size. Must be multiple of "
              << PAIR_SIZE << ", got " << input.size() << std::endl;
    return {}; // Return empty Code on error
  }

  const size_t num_pairs = input.size() / PAIR_SIZE;

  // If input is empty (0 pairs), the result is success (1)
  if (num_pairs == 0) {
    mvm::Code output(OUTPUT_SIZE, 0);
    mvm::to_big_endian(uint256_t(1), output.data()); // Success
    return output;
  }

  std::vector<std::pair<Point, ExtPoint>> pairs;
  pairs.reserve(num_pairs);

  try {
    const uint8_t *data = input.data();

    for (size_t i = 0; i < num_pairs; ++i) {
      const uint8_t *current_pair_data = data + i * PAIR_SIZE;

      // 1. Parse G1 Point (P) - No changes needed here
      const uint8_t *p1_data = current_pair_data;
      uint256_t p1_x = mvm::from_big_endian(p1_data + 0 * COORD_SIZE);
      uint256_t p1_y = mvm::from_big_endian(p1_data + 1 * COORD_SIZE);

      // 2. Parse G2 Point (Q) - **MODIFIED SECTION**
      // EVM ABI Input format: [x_im, x_re, y_im, y_re]
      const uint8_t *p2_data = current_pair_data + G1_POINT_SIZE;
      uint256_t p2_x_im_in = mvm::from_big_endian(
          p2_data + 0 * COORD_SIZE); // Read Imaginary X from input[0]
      uint256_t p2_x_re_in = mvm::from_big_endian(
          p2_data + 1 * COORD_SIZE); // Read Real X from input[1]
      uint256_t p2_y_im_in = mvm::from_big_endian(
          p2_data + 2 * COORD_SIZE); // Read Imaginary Y from input[2]
      uint256_t p2_y_re_in = mvm::from_big_endian(
          p2_data + 3 * COORD_SIZE); // Read Real Y from input[3]

      // 3. Validate Coordinates (< FieldPrime) - Basic check using parsed
      // values Check against the actual FieldPrime constant
      if (p1_x >= FieldPrime || p1_y >= FieldPrime ||
          p2_x_re_in >= FieldPrime ||
          p2_x_im_in >= FieldPrime || // Check both real and imaginary parts
          p2_y_re_in >= FieldPrime || p2_y_im_in >= FieldPrime) {
        std::cerr << "[EcPairing] Error: Coordinate out of field range in pair "
                  << i << std::endl;
        // Print specific error details if needed for debugging
        if (p1_x >= FieldPrime)
          std::cerr << "  P.x >= FieldPrime: " << mvm::to_hex_string(p1_x)
                    << std::endl;
        if (p1_y >= FieldPrime)
          std::cerr << "  P.y >= FieldPrime: " << mvm::to_hex_string(p1_y)
                    << std::endl;
        if (p2_x_re_in >= FieldPrime)
          std::cerr << "  Q.x_re >= FieldPrime: "
                    << mvm::to_hex_string(p2_x_re_in) << std::endl;
        if (p2_x_im_in >= FieldPrime)
          std::cerr << "  Q.x_im >= FieldPrime: "
                    << mvm::to_hex_string(p2_x_im_in) << std::endl;
        if (p2_y_re_in >= FieldPrime)
          std::cerr << "  Q.y_re >= FieldPrime: "
                    << mvm::to_hex_string(p2_y_re_in) << std::endl;
        if (p2_y_im_in >= FieldPrime)
          std::cerr << "  Q.y_im >= FieldPrime: "
                    << mvm::to_hex_string(p2_y_im_in) << std::endl;
        return {}; // Return empty Code on error
      }

      // 4. Create Point objects
      // G1 point is straightforward
      Point p1 = {p1_x, p1_y};
      // G2 point: Assign parsed values to ExtPoint structure, assuming it
      // expects {real, imaginary}
      ExtPoint p2 = {
          {p2_x_re_in, p2_x_im_in}, // Store as {real, imaginary}
          {p2_y_re_in, p2_y_im_in}  // Store as {real, imaginary}
      };

      // 5. Add pair to list
      pairs.emplace_back(p1, p2);
    }

    // *** Logging section (remains the same, uses the constructed pairs) ***
    int log_pair_idx = 0;
    for (const auto &pair_to_log : pairs) {
      const auto &p_log = pair_to_log.first;
      const auto &q_log = pair_to_log.second;
      // Add actual logging here if needed, e.g.:
      // std::cout << "[EcPairing] Debug Pair " << log_pair_idx << ":" <<
      // std::endl; std::cout << "  P: x=" << mvm::to_hex_string(p_log.x) << "
      // y=" << mvm::to_hex_string(p_log.y) << std::endl; std::cout << "  Q:
      // x={re=" << mvm::to_hex_string(q_log.x.first) << ", im=" <<
      // mvm::to_hex_string(q_log.x.second) << "}" << std::endl; std::cout << "
      // y={re=" << mvm::to_hex_string(q_log.y.first) << ", im=" <<
      // mvm::to_hex_string(q_log.y.second) << "}" << std::endl;
      log_pair_idx++;
    }
    // *** End Logging section ***

    // 6. Perform Pairing Check
    std::optional<bool> check_result =
        pairing_check(std::span{pairs}); // Pass the vector of pairs

    // 7. Process Pairing Result and Serialize Output
    mvm::Code output(OUTPUT_SIZE, 0);

    if (check_result.has_value()) {
      if (check_result.value()) {
        mvm::to_big_endian(uint256_t(1), output.data()); // Success
      } else {
        // Pairing check evaluated to false (valid points, but pairing equation
        // != 1)
        mvm::to_big_endian(uint256_t(0), output.data()); // Failure
      }
      return output;
    } else {
      // pairing_check returned std::nullopt, indicating an error during the
      // check This usually implies invalid points (e.g., not on curve, subgroup
      // checks) if the underlying library performs these checks.
      std::cerr << "[EcPairing] Error: pairing_check function indicated an "
                   "error (e.g., invalid point)."
                << std::endl;
      // Return 0 as per EIP-197 failure modes (invalid input points lead to
      // failure, not revert) However, the prompt asks for empty Code on error,
      // so we stick to that. If EVM compatibility requires returning 0 on point
      // validation failure within pairing_check, adjust here.
      return {}; // Return empty Code on error as per original function spec
                 // Alternative (closer to EIP-197 failure):
      // mvm::to_big_endian(uint256_t(0), output.data()); // Indicate failure
      // (0) return output;
    }
  } catch (const std::exception &e) {
    std::cerr << "[EcPairing] Error: Exception during execution: " << e.what()
              << std::endl;
    return {}; // Return empty Code on error
  } catch (...) {
    std::cerr << "[EcPairing] Error: Unknown exception during execution."
              << std::endl;
    return {}; // Return empty Code on error
  }
}

// --- Helper function to read little-endian uint64_t ---
inline uint64_t read_le64(const uint8_t *ptr) {
  uint64_t value = 0;
  std::memcpy(&value, ptr, sizeof(uint64_t));
  // Assuming the system is little-endian. If not, byte swap is needed.
  // On x86/x64, this memcpy is usually sufficient.
  // For cross-platform safety, check endianness or use explicit byte
  // manipulation.
#if __BYTE_ORDER__ == __ORDER_BIG_ENDIAN__
  value = __builtin_bswap64(value);
#endif
  return value;
}

// --- Helper function to write little-endian uint64_t ---
inline void write_le64(uint8_t *ptr, uint64_t value) {
#if __BYTE_ORDER__ == __ORDER_BIG_ENDIAN__
  value = __builtin_bswap64(value);
#endif
  std::memcpy(ptr, &value, sizeof(uint64_t));
}

// --- Helper function to read big-endian uint32_t ---
inline uint32_t read_be32(const uint8_t *ptr) {
  uint32_t value = 0;
  std::memcpy(&value, ptr, sizeof(uint32_t));
#if __BYTE_ORDER__ == __ORDER_LITTLE_ENDIAN__
  value = __builtin_bswap32(value); // Use GCC/Clang intrinsic for byte swap
#endif
  // For MSVC use _byteswap_ulong
  // #elif defined(_MSC_VER)
  //     value = _byteswap_ulong(value);
  // #endif
  return value;
}

// Helper function to read a little-endian uint32_t from a byte array
inline uint32_t read_le32(const uint8_t *ptr) {
  uint32_t value = 0;
  for (int i = 0; i < 4; ++i) {
    value |= static_cast<uint32_t>(ptr[i]) << (i * 8);
  }
  return value;
}

// --- Implementation for BLAKE2f Precompile ---
mvm::Code MyExtension::Blake2f(mvm::Code input) {

  // --- Constants for BLAKE2f input structure ---
  constexpr size_t EXPECTED_INPUT_SIZE = 213;
  constexpr size_t ROUNDS_SIZE = 4;
  constexpr size_t H_SIZE = 64;  // 8 * 8 bytes
  constexpr size_t M_SIZE = 128; // 16 * 8 bytes
  constexpr size_t T_SIZE = 16;  // 2 * 8 bytes
  constexpr size_t F_SIZE = 1;
  constexpr size_t OUTPUT_SIZE = 64; // Output is the new state vector h

  // Offsets
  constexpr size_t H_OFFSET = ROUNDS_SIZE;
  constexpr size_t M_OFFSET = H_OFFSET + H_SIZE;
  constexpr size_t T_OFFSET = M_OFFSET + M_SIZE;
  constexpr size_t F_OFFSET = T_OFFSET + T_SIZE;

  // --- Input Validation ---
  if (input.size() != EXPECTED_INPUT_SIZE) {
    std::cerr << "BLAKE2f Error: Invalid input size. Expected "
              << EXPECTED_INPUT_SIZE << ", got " << input.size() << std::endl;
    return {}; // Return empty code to signal error
  }

  const uint8_t *data = input.data();

  try {
    // --- Parse Input ---
    // 1. Rounds (4 bytes, big-endian unsigned integer)
    uint32_t rounds = read_be32(data); // Reads as uint32_t directly

    // 2. State vector h (64 bytes, 8 x 8-byte little-endian unsigned integers)
    //    Use C-style array matching the blake2b_compress signature
    uint64_t h_state[8];
    for (int i = 0; i < 8; ++i) {
      h_state[i] = read_le64(data + H_OFFSET + i * 8); //
    }

    // 3. Message block vector m (128 bytes, 16 x 8-byte little-endian unsigned
    // integers)
    //    Use const C-style array matching the blake2b_compress signature
    uint64_t
        m_block[16]; // Non-const needed for read_le64, but function takes const
    for (int i = 0; i < 16; ++i) {
      m_block[i] = read_le64(data + M_OFFSET + i * 8); //
    }

    // 4. Offset counters t (16 bytes, 2 x 8-byte little-endian integers)
    //    Use const C-style array matching the blake2b_compress signature
    uint64_t t_counters[2]; // Non-const needed for read_le64, but function
                            // takes const
    t_counters[0] = read_le64(data + T_OFFSET);     //
    t_counters[1] = read_le64(data + T_OFFSET + 8); //

    // 5. Final block indicator flag f (1 byte, 0 or 1)
    uint8_t f_byte = data[F_OFFSET]; //
    if (f_byte > 1) {
      std::cerr << "BLAKE2f Error: Invalid f flag value. Expected 0 or 1, got "
                << static_cast<int>(f_byte) << std::endl;
      return {}; // Invalid flag
    }
    bool final_flag = (f_byte == 1); //

    // --- Call Core BLAKE2f Compression Function ---
    // The signature expects: uint32_t rounds, uint64_t h[8], const uint64_t
    // m[16], const uint64_t t[2], bool last
    mvm::crypto::blake2b_compress(
        rounds,     // Already uint32_t
        h_state,    // Pass C-style array (will be modified in place)
        m_block,    // Pass C-style array
        t_counters, // Pass C-style array
        final_flag  // Pass bool flag
    );

    // --- Serialize Output ---
    // Output is the resulting state vector h (modified h_state)
    mvm::Code output(OUTPUT_SIZE);
    for (int i = 0; i < 8; ++i) {
      // Write the modified h_state back to the output buffer
      write_le64(output.data() + i * 8, h_state[i]); //
    }

    return output;
  } catch (const std::out_of_range &oor) {
    std::cerr << "BLAKE2f Error: Out of range during input parsing: "
              << oor.what() << std::endl;
    return {}; // Return empty code on parsing error
  } catch (const std::exception &e) {
    std::cerr << "BLAKE2f Error: Exception during execution: " << e.what()
              << std::endl;
    return {}; // Return empty code on other errors
  } catch (...) {
    std::cerr << "BLAKE2f Error: Unknown exception during execution."
              << std::endl;
    return {};
  }
}

// --- Implementation for Point Evaluation Precompile (0x0A) ---
mvm::Code MyExtension::PointEvaluationVerify(mvm::Code input) {

  // --- Constants for Point Evaluation input structure ---
  constexpr size_t VERSIONED_HASH_OFFSET = 0;
  constexpr size_t VERSIONED_HASH_SIZE_BYTES =
      mvm::crypto::VERSIONED_HASH_SIZE; // 32
  constexpr size_t Z_OFFSET =
      VERSIONED_HASH_OFFSET + VERSIONED_HASH_SIZE_BYTES; // 32
  constexpr size_t Z_SIZE = 32;
  constexpr size_t Y_OFFSET = Z_OFFSET + Z_SIZE; // 64
  constexpr size_t Y_SIZE = 32;
  constexpr size_t COMMITMENT_OFFSET = Y_OFFSET + Y_SIZE; // 96
  constexpr size_t COMMITMENT_SIZE = 48;
  constexpr size_t PROOF_OFFSET = COMMITMENT_OFFSET + COMMITMENT_SIZE; // 144
  constexpr size_t PROOF_SIZE = 48;
  constexpr size_t EXPECTED_INPUT_SIZE = PROOF_OFFSET + PROOF_SIZE; // 192
  constexpr size_t OUTPUT_SIZE = 64;

  // --- Input Validation ---
  if (input.size() != EXPECTED_INPUT_SIZE) {
    std::cerr << "PointEvaluation Error: Invalid input size. Expected "
              << EXPECTED_INPUT_SIZE << ", got " << input.size() << std::endl;
    // Return 64 bytes of 0 on failure as per EIP-4844 spec for failed
    // verification
    return mvm::Code(OUTPUT_SIZE, 0);
  }

  const uint8_t *data_ptr = input.data();

  // --- Prepare pointers for kzg_verify_proof (casting to std::byte*) ---
  // Ensure alignment is not an issue; direct casting should be fine if data is
  // byte-aligned.
  const std::byte *versioned_hash_ptr =
      reinterpret_cast<const std::byte *>(data_ptr + VERSIONED_HASH_OFFSET);
  const std::byte *z_ptr =
      reinterpret_cast<const std::byte *>(data_ptr + Z_OFFSET);
  const std::byte *y_ptr =
      reinterpret_cast<const std::byte *>(data_ptr + Y_OFFSET);
  const std::byte *commitment_ptr =
      reinterpret_cast<const std::byte *>(data_ptr + COMMITMENT_OFFSET);
  const std::byte *proof_ptr =
      reinterpret_cast<const std::byte *>(data_ptr + PROOF_OFFSET);

  // --- Call Core KZG Verification Function ---
  bool success = false;
  try {
    // Directly use the function from the provided header/namespace
    success = mvm::crypto::kzg_verify_proof(versioned_hash_ptr, z_ptr, y_ptr,
                                            commitment_ptr, proof_ptr);
  } catch (const std::exception &e) {
    // Catch potential exceptions from underlying crypto library if they exist
    std::cerr << "PointEvaluation Error: Exception during kzg_verify_proof: "
              << e.what() << std::endl;
    success = false; // Treat exceptions as verification failure
  } catch (...) {
    std::cerr
        << "PointEvaluation Error: Unknown exception during kzg_verify_proof."
        << std::endl;
    success = false; // Treat exceptions as verification failure
  }

  // --- Prepare Output ---
  mvm::Code output(OUTPUT_SIZE, 0); // Initialize with zeros

  if (success) {
    // On success, return (FIELD_ELEMENTS_PER_BLOB, BLS_MODULUS)
    try {
      // Use mvm::to_big_endian (or implement it if needed)
      mvm::to_big_endian(mvm::crypto::FIELD_ELEMENTS_PER_BLOB,
                         output.data()); // First 32 bytes
      mvm::to_big_endian(mvm::crypto::BLS_MODULUS,
                         output.data() + 32); // Next 32 bytes
    } catch (const std::exception &e) {
      std::cerr
          << "PointEvaluation Error: Exception during output serialization: "
          << e.what() << std::endl;
      // If serialization fails unexpectedly, revert to failure output
      std::fill(output.begin(), output.end(), 0);
    }
  } else {
    // On failure, the output is already initialized to 64 zeros.
  }

  return output;
}
// --- Implementation for Point Evaluation Precompile (0x0A) ---
mvm::Code MyExtension::PublicKeyFromPrivateKey(mvm::Code input) {
  mvm::Code output(32, 0);

  if (input.size() < 4) {
    return output;
  }

  try {
    // Lấy opcode từ 4 byte đầu tiên
    uint32_t opCode =
        (input[0] << 24) | (input[1] << 16) | (input[2] << 8) | input[3];

    auto input_without_opcode = getInputWithoutOpcode(input);

    if (opCode == mvm::FunctionSelector::ESCP_PFP) {
      std::vector<uint8_t> public_key_bytes;

      // Private key mẫu (32 bytes)
      auto private_key_hex =
          mvm::vector_to_string_format(input_without_opcode, true, false);

      intx::uint256 private_key =
          intx::from_string<intx::uint256>(private_key_hex);

      Point public_key =
          evmmax::secp256k1::private_key_to_public_key(private_key);
      if (public_key.is_inf()) {
        return output;
      } else {
        // Sử dụng hàm helper mới nhất
        std::vector<uint8_t> x_bytes =
            mvm::uint256_to_vector(public_key.x); // Sẽ có 32 bytes
        std::vector<uint8_t> y_bytes =
            mvm::uint256_to_vector(public_key.y); // Sẽ có 32 bytes

        printHex(x_bytes);
        // Construct the uncompressed public key string
        // Đặt trước dung lượng cho vector cuối cùng: 1 byte (prefix) + 32 bytes
        // (X) + 32 bytes (Y) = 65 bytes
        public_key_bytes.reserve(1 + x_bytes.size() + y_bytes.size());

        // Thêm byte tiền tố định dạng uncompressed (0x04)
        public_key_bytes.push_back(0x04);

        // Nối các byte của tọa độ X vào sau
        public_key_bytes.insert(public_key_bytes.end(), x_bytes.begin(),
                                x_bytes.end());

        // Nối các byte của tọa độ Y vào sau
        public_key_bytes.insert(public_key_bytes.end(), y_bytes.begin(),
                                y_bytes.end());

        return mvm::encode_abi_bytes(public_key_bytes);
      }
      return output;
    }

    return output;
  } catch (const std::exception &e) {
    std::cerr << "[PublicKeyFromPrivateKey] Exception: " << e.what()
              << std::endl;
    return output;
  } catch (...) {
    std::cerr << "[PublicKeyFromPrivateKey] Unknown exception." << std::endl;
    return output;
  }
}