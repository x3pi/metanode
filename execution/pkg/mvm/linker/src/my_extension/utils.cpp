
#include <cstring>
#include <iostream>
#include <math.h>
#include <my_extension/constants.h>
#include <my_extension/utils.h>
#include <sstream>

// Global Xapian base path — set by Go via CGo call to SetXapianBasePath().
// This replaces the XAPIAN_BASE_PATH environment variable approach.
static std::string g_xapian_base_path = "";

// Called from Go (pkg/mvm/mvm_api.go) via CGo after loading config.json.
// Must be called before any Xapian operation.
extern "C" void SetXapianBasePath(const char *path) {
  if (path && *path != '\0') {
    g_xapian_base_path = path;
    std::cerr << "SetXapianBasePath "
              << g_xapian_base_path << std::endl;
  } else {
    std::cerr << "[SetXapianBasePath] Warning: empty path provided, using "
                 "current directory."
              << std::endl;
    g_xapian_base_path = "";
  }
}

namespace mvm {

// Hàm băm chuỗi sử dụng Keccak-256
std::string keccak256(const std::string &input) {
  KeccakHash hash = keccak_256(input);
  std::stringstream ss;
  for (uint8_t byte : hash) {
    ss << std::hex << std::setw(2) << std::setfill('0')
       << static_cast<int>(byte);
  }
  return ss.str();
}

// Hàm tạo fullPath — đọc từ global var thay vì env var
std::filesystem::path createFullPath(const mvm::Address &address,
                                     const std::string &dbname) {
  // Băm dbname (giữ nguyên)
  std::string hashedDbname = keccak256(dbname);

  // Tạo đường dẫn sử dụng g_xapian_base_path (set từ config.json qua Go/CGo)
  std::filesystem::path fullPath = std::filesystem::path(g_xapian_base_path) /
                                   intx::to_string(address) / hashedDbname;

  return fullPath;
}

std::vector<uint8_t> evm_encode_mpfr(const mpfr_t &value) {
  // Chuyển đổi mpfr_t thành chuỗi string với độ chính xác cao
  char *str_value = nullptr;
  mpfr_exp_t exponent;
  size_t len;

  // Lấy giá trị dưới dạng chuỗi (độ chính xác cao)
  mpfr_asprintf(&str_value, "%.18Rf", value);

  // Xử lý độ dài chuỗi
  std::string str_value_str(str_value);
  len = str_value_str.length();
  size_t padded_len =
      std::ceil(static_cast<double>(len) / WORD_SIZE) * WORD_SIZE;

  // Bộ đệm mã hóa với 64 + độ dài chuỗi đã pad
  std::vector<uint8_t> encoded(64 + padded_len, 0);

  // Đặt Start offset = 32 bytes (0x20)
  std::vector<uint8_t> start(WORD_SIZE, 0);
  start[31] = 0x20; // Giá trị 32 ở vị trí cuối cùng
  std::copy(start.begin(), start.end(), encoded.begin());

  // Đặt Length (độ dài chuỗi)
  std::vector<uint8_t> length_bytes(WORD_SIZE, 0);
  uint64_t len_uint64 = static_cast<uint64_t>(len);
  for (int i = 0; i < 8; i++) {
    length_bytes[31 - i] = (len_uint64 >> (i * 8)) & 0xFF;
  }
  std::copy(length_bytes.begin(), length_bytes.end(),
            encoded.begin() + WORD_SIZE);

  // Thêm nội dung chuỗi vào bộ đệm đã padding
  std::copy(str_value_str.begin(), str_value_str.end(),
            encoded.begin() + 2 * WORD_SIZE);

  // Giải phóng bộ nhớ chuỗi mpfr_t đã chuyển thành string
  mpfr_free_str(str_value);

  return encoded;
}

void hexToSignedInt(mpfr_t result, const std::vector<uint8_t> &bytes) {
  mpfr_init2(result, 256);
  mpfr_set_zero(result, 1);

  // Chuyển đổi từng byte một
  for (size_t i = 0; i < bytes.size(); ++i) {
    mpfr_t byte_val;
    mpfr_init2(byte_val, 256);
    mpfr_set_ui(byte_val, bytes[i], MPFR_RNDN);
    mpfr_mul_2exp(result, result, 8, MPFR_RNDN);
    mpfr_add(result, result, byte_val, MPFR_RNDN);
    mpfr_clear(byte_val);
  }

  // Kiểm tra số âm (bù hai)
  if (bytes[0] & 0x80) { // MSB = 1 → số âm
    mpfr_t two_power_256, temp;
    mpfr_init2(two_power_256, 256);
    mpfr_ui_pow_ui(two_power_256, 2, bytes.size() * 8,
                   MPFR_RNDN); // 2^(số bit thực sự)

    mpfr_init2(temp, 256);
    mpfr_sub(temp, result, two_power_256, MPFR_RNDN); // result - 2^256
    mpfr_set(result, temp, MPFR_RNDN);

    mpfr_clear(two_power_256);
    mpfr_clear(temp);
  }
}

void signedIntToHex(std::vector<uint8_t> &result_bytes, const mpfr_t number) {
  result_bytes.resize(32, 0);

  // Tạo bản sao để làm việc
  mpfr_t work_number, two_power_256, adjusted_number;
  mpfr_init2(work_number, 256);
  mpfr_set(work_number, number, MPFR_RNDN);

  mpfr_init2(two_power_256, 256);
  mpfr_ui_pow_ui(two_power_256, 2, 256, MPFR_RNDN); // 2^256

  mpfr_init2(adjusted_number, 256);

  // Nếu số âm, chuyển sang dạng bù hai
  if (mpfr_sgn(work_number) < 0) {
    mpfr_add(adjusted_number, work_number, two_power_256, MPFR_RNDN);
  } else {
    mpfr_set(adjusted_number, work_number, MPFR_RNDN);
  }

  // Chuyển đổi từng byte
  for (int i = 31; i >= 0; --i) {
    mpfr_t remainder;
    mpfr_init2(remainder, 256);

    mpfr_fmod_ui(remainder, adjusted_number, 256, MPFR_RNDN);
    result_bytes[i] = mpfr_get_ui(remainder, MPFR_RNDN);

    mpfr_div_ui(adjusted_number, adjusted_number, 256, MPFR_RNDN);
    mpfr_floor(adjusted_number, adjusted_number); // Làm tròn xuống

    mpfr_clear(remainder);
  }

  mpfr_clear(work_number);
  mpfr_clear(two_power_256);
  mpfr_clear(adjusted_number);
}
} // namespace mvm