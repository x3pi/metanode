// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.
#include "mvm/util.h"
#include "mvm/processor.h"
#include <sstream>

#include "mvm/rlp.h"

#include <iomanip>

using namespace std;

namespace mvm {
string strip(const string &s) {
  return (s.size() >= 2 && s[1] == 'x') ? s.substr(2) : s;
}

uint64_t to_uint64(const std::string &s) {
  return strtoull(s.c_str(), nullptr, 16);
}

vector<uint8_t> to_bytes(const string &_s) {
  auto s = strip(_s);

  const size_t byte_len = (s.size() + 1) / 2; // round up
  vector<uint8_t> v(byte_len);

  // Handle odd-length strings
  size_t n = 0;
  if (s.size() % 2 != 0) {
    v[0] = static_cast<uint8_t>(strtoul(s.substr(0, 1).c_str(), nullptr, 16));
    ++n;
  }

  auto x = n;
  for (auto i = n; i < byte_len; ++i, x += 2) {
    v[i] = static_cast<uint8_t>(strtoul(s.substr(x, 2).c_str(), nullptr, 16));
  }
  return v;
}

using uint256_t = intx::uint<256>; // Sử dụng intx::uint<256> làm uint256_t

// Hàm trích xuất 20 byte cuối từ uint256_t
std::vector<uint8_t> extract_last_20_bytes(const uint256_t &sender) {
  std::vector<uint8_t> result(20);

  // Trích xuất từng byte từ sender (Big-Endian)
  for (size_t i = 0; i < 20; ++i) {
    result[19 - i] = static_cast<uint8_t>(sender >> (i * 8));
  }

  return result;
}

// Hàm mã hóa RLP
std::vector<uint8_t> rlp_encode(const std::vector<uint8_t> &sender,
                                uint64_t nonce) {
  std::vector<uint8_t> rlp;

  // Chuyển nonce thành bytes trước
  std::vector<uint8_t> nonce_bytes;
  if (nonce == 0) {
    nonce_bytes = {0x80}; // Encoding đặc biệt cho 0
  } else {
    // Chuyển nonce thành bytes, bỏ qua các bytes 0 ở đầu
    while (nonce > 0) {
      nonce_bytes.insert(nonce_bytes.begin(),
                         static_cast<uint8_t>(nonce & 0xFF));
      nonce >>= 8;
    }

    // Xử lý encoding cho nonce
    if (nonce_bytes.size() == 1 && nonce_bytes[0] < 0x80) {
      // Giữ nguyên byte đơn nếu < 0x80
    } else {
      // Thêm length prefix cho các trường hợp khác
      std::vector<uint8_t> length_encoded = {
          static_cast<uint8_t>(0x80 + nonce_bytes.size())};
      nonce_bytes.insert(nonce_bytes.begin(), length_encoded.begin(),
                         length_encoded.end());
    }
  }

  // Tính toán tổng độ dài của RLP list
  size_t total_length =
      sender.size() + nonce_bytes.size() + 1; // +1 cho sender prefix

  // Thêm list prefix
  if (total_length <= 55) {
    rlp.push_back(static_cast<uint8_t>(0xc0 + total_length));
  } else {
    // Xử lý trường hợp list dài
    std::vector<uint8_t> length_bytes;
    size_t temp_length = total_length;
    while (temp_length > 0) {
      length_bytes.insert(length_bytes.begin(),
                          static_cast<uint8_t>(temp_length & 0xFF));
      temp_length >>= 8;
    }
    rlp.push_back(static_cast<uint8_t>(0xf7 + length_bytes.size()));
    rlp.insert(rlp.end(), length_bytes.begin(), length_bytes.end());
  }

  // Thêm sender
  rlp.push_back(0x94); // prefix cho 20 bytes
  rlp.insert(rlp.end(), sender.begin(), sender.end());

  // Thêm nonce đã được encode
  rlp.insert(rlp.end(), nonce_bytes.begin(), nonce_bytes.end());

  return rlp;
}

// Hàm tính toán địa chỉ triển khai
Address generate_address(const Address &sender, uint64_t nonce) {
  // Trích xuất 20 byte cuối từ sender
  std::vector<uint8_t> sender_last_20 = extract_last_20_bytes(sender);

  // Mã hóa RLP
  std::vector<uint8_t> rlp_encoding = rlp_encode(sender_last_20, nonce);

  // Hiển thị dữ liệu RLP trước khi băm (tuỳ chọn để debug)
  std::stringstream ss;
  for (uint8_t byte : rlp_encoding) {
    ss << std::hex << std::setw(2) << std::setfill('0')
       << static_cast<int>(byte);
  }

  // Tính Keccak-256
  uint8_t buffer[32];
  keccak_256(rlp_encoding.data(),
             static_cast<unsigned int>(rlp_encoding.size()), buffer);

  // Lấy 20 byte cuối từ Keccak-256 làm địa chỉ
  std::vector<uint8_t> result(20);
  // std::memcpy(result.data(), buffer + 12, 20);
  return from_big_endian(buffer + 12u, 20u);
}

std::string addressToHex(const Address &address) {
  std::stringstream ss;
  ss << std::hex << std::setfill('0')
     << std::setw(40); // 40 characters for 20 bytes (20 * 2 hex digits)

  // Assuming Address is a byte array or similar
  const unsigned char *bytes =
      reinterpret_cast<const unsigned char *>(&address);
  for (size_t i = 0; i < sizeof(Address); ++i) {
    ss << std::setw(2) << std::setfill('0') << static_cast<int>(bytes[i]);
  }
  return ss.str();
}

Address generate_contract_address(const Address &sender,
                                  uint256_t sender_last_hash) {

  uint8_t b_sender[32];
  mvm::to_big_endian(sender, b_sender);

  uint8_t b_sender_last_hash[32];
  mvm::to_big_endian(sender_last_hash, b_sender_last_hash);

  uint8_t byte_data[52];
  std::memcpy(byte_data, b_sender + 12, 20);
  std::memcpy(byte_data + 20, b_sender_last_hash, 32);

  uint8_t buffer[32u] = {};
  keccak_256((unsigned char *)byte_data, 52u, buffer);

  Address address = from_big_endian(buffer + 12u, 20u);

  return address;
}

Address generate_contract_address_with_nonce(const Address &sender,
                                             uint256_t sender_last_hash,
                                             size_t nonce) {

  uint8_t b_sender[32];
  mvm::to_big_endian(sender, b_sender);

  uint8_t b_sender_last_hash[32];
  mvm::to_big_endian(sender_last_hash, b_sender_last_hash);

  // Lưu trữ nonce vào một mảng byte
  uint8_t b_nonce[sizeof(size_t)];
  memcpy(b_nonce, &nonce, sizeof(size_t));

  // Tính toán kích thước của byte_data
  size_t byte_data_size = 20 + 32 + sizeof(size_t);
  uint8_t byte_data[byte_data_size];

  // Sao chép dữ liệu vào byte_data
  std::memcpy(byte_data, b_sender + 12, 20);
  std::memcpy(byte_data + 20, b_sender_last_hash, 32);
  std::memcpy(byte_data + 20 + 32, b_nonce, sizeof(size_t));

  uint8_t buffer[32u] = {};
  keccak_256((unsigned char *)byte_data,
             static_cast<unsigned int>(
                 byte_data_size), // Sử dụng kích thước chính xác của byte_data
             buffer);

  Address address = from_big_endian(buffer + 12u, 20u);

  return address;
}

void print_hex(uint8_t *s, size_t len) {
  for (size_t i = 0; i < len; i++) {
    printf("%02x", s[i]);
  }
  printf("\n");
}

int getSignUint256(uint256_t v) {
  if (v == 0) {
    return 0;
  }
  uint8_t b_v[32];
  mvm::to_big_endian(v, b_v);
  if (b_v[0] >= 128) {
    return -1;
  }
  return 1;
}

Address generate_contract_address_2(const Address &sender, uint256_t salt,
                                    vector<uint8_t> init_code) {
  uint8_t b_sender[32];
  mvm::to_big_endian(sender, b_sender);

  uint8_t b_salt[32];
  mvm::to_big_endian(salt, b_salt);

  // mvm::to_big_endian(sender_last_hash, byte_data+20);

  // 1 byte (0xff) + 20 byte address + 32 byte salt + 32 byte code hash = 85
  uint8_t byte_data[85];
  // 1 byte
  byte_data[0] = 255;
  // 21 byte
  std::memcpy(byte_data + 1, b_sender + 12, 20);
  // 53 byte
  std::memcpy(byte_data + 21, b_salt, 32);

  uint8_t buffer_code_hash[32u] = {};
  keccak_256((unsigned char *)init_code.data(), init_code.size(),
             buffer_code_hash);
  // 85 byte
  std::memcpy(byte_data + 53, buffer_code_hash, 32);

  uint8_t buffer[32u] = {};
  keccak_256((unsigned char *)byte_data, 85u, buffer);

  return from_big_endian(buffer + 12u, 20u);
}

uint256_t getPaddedAddressSelector(const std::string &functionSignature) {
  uint32_t selector;

  KeccakHash hash = keccak_256(functionSignature);
  selector = (static_cast<uint32_t>(hash[0]) << 24) |
             (static_cast<uint32_t>(hash[1]) << 16) |
             (static_cast<uint32_t>(hash[2]) << 8) |
             static_cast<uint32_t>(hash[3]);

  uint256_t paddedAddress;
  paddedAddress = uint32_to_uint256(selector);
  return paddedAddress;
}

std::vector<uint8_t> uchar_to_vector(const unsigned char *data, size_t size) {
  if (data == nullptr || size == 0) {
    return std::vector<uint8_t>();
  }

  return std::vector<uint8_t>(data, data + size);
}

unsigned char *vector_to_uchar(const std::vector<uint8_t> &vec) {
  if (vec.empty()) {
    return nullptr;
  }

  unsigned char *result = new unsigned char[vec.size()];
  std::memcpy(result, vec.data(), vec.size());

  return result;
}

Address public_key_to_address(const std::vector<uint8_t> &public_key) {
  if (public_key.empty()) {
    return Address(0); // Trả về địa chỉ 0 nếu public key rỗng
  }

  // Tính Keccak-256 hash của public key
  uint8_t hash[32];
  keccak_256(public_key.data(), public_key.size(), hash);

  // Lấy 20 bytes cuối làm địa chỉ (bỏ qua 12 bytes đầu)
  return from_big_endian(hash + 12, 20);
}

void print_address(const Address &addr) {
  std::cout << "Address: 0x" << std::hex << addr << std::dec << std::endl;
}

std::string vector_to_string_format(const std::vector<uint8_t> &vec,
                                    bool add_hex_prefix, bool add_spaces) {
  if (vec.empty()) {
    return add_hex_prefix ? "0x" : "";
  }

  std::stringstream ss;
  if (add_hex_prefix) {
    ss << "0x";
  }

  for (size_t i = 0; i < vec.size(); ++i) {
    ss << std::hex << std::setfill('0') << std::setw(2)
       << static_cast<int>(vec[i]);
    if (add_spaces && i < vec.size() - 1) {
      ss << " ";
    }
  }

  return ss.str();
}

std::vector<uint8_t> uint256_to_vector(const uint256_t &value) {
  std::vector<uint8_t> result(32); // uint256 = 32 bytes

  // Sử dụng to_big_endian để chuyển đổi
  to_big_endian(value, result.data());

  return result;
}

std::vector<uint8_t> encode_abi_bytes(const std::vector<uint8_t> &data) {
  size_t data_actual_size = data.size();

  // Tính toán padding_needed một cách chính xác:
  // Nếu data_actual_size là bội số của 32, không cần padding (padding_needed =
  // 0). Ngược lại, padding_needed = 32 - (data_actual_size % 32).
  size_t remainder = data_actual_size % 32;
  size_t padding_needed = (remainder == 0) ? 0 : (32 - remainder);

  // Tổng kích thước = 32 (offset) + 32 (length) + kích thước dữ liệu + padding
  size_t total_size = 32 + 32 + data_actual_size + padding_needed;

  // Khởi tạo vector 'result' với kích thước cuối cùng, các phần tử được khởi
  // tạo bằng 0.
  std::vector<uint8_t> result(total_size, 0);

  // 1. Offset (32 byte đầu tiên)
  // Vector 'result' đã được điền sẵn bằng 0, chỉ cần đặt byte cuối của phần
  // offset.
  if (total_size >=
      32) { // Đảm bảo vector đủ lớn (luôn đúng nếu total_size tính đúng)
    result[31] = 0x20;
  }

  // 2. Length (32 byte tiếp theo, từ chỉ số 32 đến 63)
  if (total_size >= 64) { // Đảm bảo vector đủ lớn
    uint256_t length_val = data_actual_size;
    // Sử dụng một buffer tạm thời nếu to_big_endian yêu cầu con trỏ không trỏ
    // vào result đang được xây dựng hoặc nếu bạn muốn đảm bảo to_big_endian
    // không bị ảnh hưởng bởi các giá trị khác trong result. Tuy nhiên, ở đây ta
    // có thể ghi trực tiếp vào result.begin() + 32 nếu to_big_endian an toàn.
    to_big_endian(length_val, result.data() + 32);
  }

  // 3. Data bytes (bắt đầu từ chỉ số 64)
  if (data_actual_size > 0 &&
      total_size >= 64 + data_actual_size) { // Đảm bảo có dữ liệu và đủ chỗ
    std::copy(data.begin(), data.end(), result.begin() + 64);
  }

  // 4. Padding (các byte cuối cùng)
  // Đã được xử lý vì vector 'result' được khởi tạo với 'total_size' và điền sẵn
  // bằng 0. Không cần hành động thêm nếu các byte padding phải là 0.

  return result;
}

std::vector<uint8_t> encode_revert_string(const std::string &msg) {
  std::vector<uint8_t> result;

  // 1. Function selector: Error(string) = 0x08c379a0
  result.push_back(0x08);
  result.push_back(0xc3);
  result.push_back(0x79);
  result.push_back(0xa0);

  // 2. Offset to string data = 0x20 (32) — 32 bytes, big-endian
  for (int i = 0; i < 31; i++)
    result.push_back(0x00);
  result.push_back(0x20);

  // 3. String length — 32 bytes, big-endian
  uint64_t len = msg.size();
  for (int i = 0; i < 24; i++)
    result.push_back(0x00);
  for (int i = 7; i >= 0; i--) {
    result.push_back(static_cast<uint8_t>((len >> (i * 8)) & 0xFF));
  }

  // 4. String data (raw UTF-8 bytes)
  for (char c : msg) {
    result.push_back(static_cast<uint8_t>(c));
  }

  // 5. Pad to 32-byte boundary
  size_t remainder = msg.size() % 32;
  if (remainder != 0) {
    size_t padding = 32 - remainder;
    for (size_t i = 0; i < padding; i++) {
      result.push_back(0x00);
    }
  }

  return result;
}

} // namespace mvm
