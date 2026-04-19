// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once

extern "C" {
#include "../3rdparty/keccak/KeccakHash.h"
}
#include "address.h"

#include "fmt/format_header_only.h"
#include "nlohmann/json.hpp"
#include <iomanip>
#include <limits>
#include <sstream>
#include <vector>

namespace mvm {
inline auto from_big_endian(const uint8_t *begin, size_t size = 32u) {
  if (begin == nullptr) // Kiểm tra con trỏ null
  {
    return uint256_t{}; // Trả về giá trị 000000 cho trường hợp null
  }

  if (size == 32) {
    return intx::be::unsafe::load<uint256_t>(begin);
  } else if (size > 32) {
    throw std::logic_error("Calling from_big_endian with oversized array");
  } else {
    // TODO: Find out how common this path is, make it the caller's
    // responsibility
    uint8_t tmp[32] = {};
    const auto offset = 32 - size;
    memcpy(tmp + offset, begin, size);

    return intx::be::load<uint256_t>(tmp);
  }
}

inline void to_big_endian(const uint256_t &v, uint8_t *out) {
  // TODO: Is this cast safe?
  // uint8_t(&arr)[32] =
  // *static_cast<uint8_t(*)[32]>(static_cast<void*>(out));
  intx::be::unsafe::store(out, v);
}

inline void keccak_256(const unsigned char *input, unsigned int inputByteLen,
                       unsigned char *output) {
  // Ethereum started using Keccak and called it SHA3 before it was finalised.
  // Standard SHA3-256 (the FIPS accepted version) uses padding 0x06, but
  // Ethereum's "Keccak-256" uses padding 0x01.
  // All other constants are copied from Keccak_HashInitialize_SHA3_256 in
  // KeccakHash.h.
  Keccak_HashInstance hi;
  Keccak_HashInitialize(&hi, 1088, 512, 256, 0x01);
  Keccak_HashUpdate(&hi, input,
                    inputByteLen * std::numeric_limits<unsigned char>::digits);
  Keccak_HashFinal(&hi, output);
}

using KeccakHash = std::array<uint8_t, 32u>;

inline KeccakHash keccak_256(const uint8_t *begin, size_t byte_len) {
  KeccakHash h;
  keccak_256(begin, byte_len, h.data());
  return h;
}

inline KeccakHash keccak_256(const std::string &s) {
  return keccak_256((const uint8_t *)s.data(), s.size());
}

inline KeccakHash keccak_256(const std::vector<uint8_t> &v) {
  return keccak_256(v.data(), v.size());
}

template <size_t N>
inline KeccakHash keccak_256(const std::array<uint8_t, N> &a) {
  return keccak_256(a.data(), N);
}

template <typename T>
inline KeccakHash keccak_256_skip(size_t skip, const T &t) {
  skip = std::min(skip, t.size());
  return keccak_256((const uint8_t *)t.data() + skip, t.size() - skip);
}

std::string strip(const std::string &s);
std::vector<uint8_t> to_bytes(const std::string &s);

template <typename Iterator>
std::string to_hex_string(Iterator begin, Iterator end) {
  return fmt::format("0x{:02x}", fmt::join(begin, end, ""));
}

template <size_t N>
std::string to_hex_string(const std::array<uint8_t, N> &bytes) {
  return to_hex_string(bytes.begin(), bytes.end());
}

inline std::string to_hex_string(const std::vector<uint8_t> &bytes) {
  return to_hex_string(bytes.begin(), bytes.end());
}

inline std::string to_hex_string(uint64_t v) {
  return fmt::format("0x{:x}", v);
}

inline std::string to_hex_string(const uint256_t &v) {
  return fmt::format("0x{}", intx::hex(v));
}

inline std::string to_hex_string_fixed(const uint256_t &v,
                                       size_t min_hex_chars = 64) {
  return fmt::format("0x{:0>{}}", intx::hex(v), min_hex_chars);
}

inline auto address_to_hex_string(const Address &v) {
  return to_hex_string_fixed(v, 40);
}

template <typename T> std::string to_lower_hex_string(const T &v) {
  auto s = to_hex_string(v);
  std::transform(s.begin(), s.end(), s.begin(), ::tolower);
  return s;
}

inline uint256_t to_uint256(const std::string &s) {
  return intx::from_string<uint256_t>(s);
}

inline std::string to_checksum_address(const Address &a) {
  auto s = address_to_hex_string(a);

  // Start at index 2 to skip the "0x" prefix
  const auto h = keccak_256_skip(2, s);

  for (size_t i = 0; i < s.size() - 2; ++i) {
    auto &c = s[i + 2];
    if (c >= 'a' && c <= 'f') {
      if (h[i / 2] & (i % 2 == 0 ? 0x80 : 0x08)) {
        c = std::toupper(c);
      } else {
        c = std::tolower(c);
      }
    }
  }

  return s;
}

inline bool is_checksum_address(const std::string &s) {
  const auto cs = to_checksum_address(to_uint256(s));
  return cs == s;
}

inline uint256_t bytes_to_uint256(const unsigned char *bytes) {
  uint256_t result = 0;
  for (int i = 0; i < 32; ++i) {
    result <<= 8;
    result |= bytes[i];
  }
  return result;
}

// Hàm chuyển đổi uint32_t sang uint256
inline uint256_t uint32_to_uint256(uint32_t value) {
  intx::uint256 result = 0;

  // Chuyển đổi 4 bytes của uint32_t
  for (int i = 0; i < 4; ++i) {
    result <<= 8;
    result |= (value >> (24 - i * 8)) & 0xFF;
  }

  return result;
}

inline double uint256_to_double(const intx::uint256 &value) {
  // Tối ưu hóa: Nếu số đủ nhỏ để vừa trong uint64_t, chuyển đổi trực tiếp hơn
  // vì double có thể biểu diễn chính xác tất cả các giá trị uint64_t lên đến
  // 2^53. Chuyển đổi uint64_t -> double thường nhanh hơn và tránh cấp phát
  // chuỗi.
  const intx::uint256 max_uint64 = std::numeric_limits<uint64_t>::max();
  if (value <= max_uint64) {
    uint64_t val64 = static_cast<uint64_t>(value);
    // Cảnh báo nếu giá trị vẫn lớn hơn giới hạn biểu diễn nguyên chính xác của
    // double
    if (val64 >
        (1ULL << std::numeric_limits<double>::digits)) { // digits thường là 53
      std::cerr << "Warning: uint256_t value (fits in uint64_t but > 2^"
                << std::numeric_limits<double>::digits
                << ") converting to double may lose precision.\n";
    }
    return static_cast<double>(val64);
  }

  // Nếu lớn hơn uint64_t, sử dụng chuyển đổi qua string
  try {
    std::string str_value = intx::to_string(value);

    // Cảnh báo về việc mất độ chính xác tiềm ẩn khi chuyển đổi số lớn
    std::cerr
        << "Warning: Converting large uint256_t value to double via string."
        << " Precision loss is highly likely.\n";

    // Sử dụng std::stod để chuyển đổi chuỗi thành double
    size_t pos = 0;
    double result = std::stod(str_value, &pos);

    // Kiểm tra xem toàn bộ chuỗi đã được xử lý chưa
    // (Mặc dù với số nguyên từ intx::to_string thì không cần thiết lắm)
    // if (pos != str_value.length()) {
    //     std::cerr << "Error: std::stod did not consume the entire string.\n";
    //     return std::numeric_limits<double>::quiet_NaN();
    // }

    return result;
  } catch (const std::out_of_range &oor) {
    // Giá trị quá lớn hoặc quá nhỏ để biểu diễn bằng double
    std::cerr << "Error converting uint256_t to double: Value out of range for "
                 "double representation. "
              << oor.what() << std::endl;
    return std::numeric_limits<double>::quiet_NaN(); // Trả về Not-a-Number
  } catch (const std::invalid_argument &ia) {
    // Lỗi không mong muốn nếu intx::to_string tạo ra chuỗi không hợp lệ
    std::cerr << "Error converting uint256_t to double: Invalid argument for "
                 "std::stod. "
              << ia.what() << std::endl;
    return std::numeric_limits<double>::quiet_NaN(); // Trả về Not-a-Number
  } catch (const std::exception &e) {
    // Bắt các lỗi khác có thể xảy ra
    std::cerr << "Error converting uint256_t to double: " << e.what()
              << std::endl;
    return std::numeric_limits<double>::quiet_NaN();
  }
}

Address generate_address(const Address &sender, uint64_t nonce);
Address generate_contract_address(const Address &sender,
                                  uint256_t sender_last_hash);
Address generate_contract_address_with_nonce(const Address &sender,
                                             uint256_t sender_last_hash,
                                             size_t nonce);
Address generate_contract_address_2(const Address &sender, uint256_t salt,
                                    std::vector<uint8_t> init_code);
uint64_t to_uint64(const std::string &s);
void print_hex(uint8_t *s, size_t len);
int getSignUint256(uint256_t v);
inline uint64_t get_word_size(uint64_t size) { return (size + 32u - 1) / 32u; };
uint256_t getPaddedAddressSelector(const std::string &functionSignature);
int private_to_public(const unsigned char *private_key,
                      unsigned char *public_key);
std::vector<uint8_t> uchar_to_vector(const unsigned char *data, size_t size);
unsigned char *vector_to_uchar(const std::vector<uint8_t> &vec);
Address public_key_to_address(const std::vector<uint8_t> &public_key);
void print_address(const Address &addr);
std::string addressToHex(const Address &address);
std::string vector_to_string_format(const std::vector<uint8_t> &vec,
                                    bool add_hex_prefix = true,
                                    bool add_spaces = false);
std::vector<uint8_t> uint256_to_vector(const uint256_t &value);
std::vector<uint8_t> encode_abi_bytes(const std::vector<uint8_t> &data);
std::vector<uint8_t> encode_revert_string(const std::string &msg);

} // namespace mvm
