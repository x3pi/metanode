// abi/encoding_utils.h
#ifndef ENCODING_UTILS_H
#define ENCODING_UTILS_H

#include <vector>
#include <string>
#include <cstdint>
#include <cstddef> // Cho size_t
#include <cmath>   // Cho std::ceil
#include <cstring> // Cho std::memcpy

namespace encoding {

// --- Append functions (Provided) ---
void appendUint256(std::vector<uint8_t>& buffer, uint64_t value); // Giả sử encode uint64 vào 32 byte BE
void appendUint16Padded(std::vector<uint8_t>& buffer, uint16_t value);
void appendUint8Padded(std::vector<uint8_t>& buffer, uint8_t value);


void appendBytesPadded(std::vector<uint8_t>& buffer, const uint8_t* data, size_t len);
void appendString(std::vector<uint8_t>& buffer, const std::string& str);

// --- Append functions (Added/Modified for completeness) ---
void appendUint64Padded(std::vector<uint8_t>& buffer, uint64_t value); // uint64 chiếm 32 byte slot
void appendBoolPadded(std::vector<uint8_t>& buffer, bool value);     // bool chiếm 32 byte slot
void appendUint256FromUint64(std::vector<uint8_t>& buffer, uint64_t value); // uint256 từ uint64
uint64_t readUint256(const std::vector<uint8_t>& buffer, size_t offset);

// --- Read functions (Added) ---
uint64_t readUint64Padded(const std::vector<uint8_t>& buffer, size_t offset);
bool readBoolPadded(const std::vector<uint8_t>& buffer, size_t offset);
uint64_t readUint256AsUint64(const std::vector<uint8_t>& buffer, size_t offset); // Đọc uint256, trả về uint64 (kiểm tra tràn nếu cần)
std::vector<uint8_t> readBytesPadded(const std::vector<uint8_t>& buffer, size_t offset, size_t len); // Đọc bytes không có padding độ dài
std::string readStringFromData(const std::vector<uint8_t>& buffer, size_t data_offset, size_t len); // Đọc string từ vị trí dữ liệu + độ dài
std::string readStringDynamic(const std::vector<uint8_t>& buffer, size_t offset_ptr); // Đọc string động từ vị trí chứa offset
std::string readString(const std::vector<uint8_t>& buffer, size_t offset);
void appendInt256(std::vector<uint8_t>& buffer, int64_t value);
// --- Utility functions ---
size_t getPaddedSize(size_t len);

} // namespace encoding

#endif // ENCODING_UTILS_H