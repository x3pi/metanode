// Unit tests for standalone helper functions in crypto_handlers.cpp
// These test the byte-manipulation utility functions that don't depend on
// the full MVM class runtime (MyExtension).
//
// Functions tested:
//   - bytes_to_hex_string
//   - read_le64 / write_le64
//   - read_be32
//   - read_le32

// Workaround: GCC 13+ glibc makes SIGSTKSZ a runtime value
#include <signal.h>
#undef SIGSTKSZ
#define SIGSTKSZ 8192

#define DOCTEST_CONFIG_IMPLEMENT_WITH_MAIN
#include <doctest/doctest.h>

#include <cstdint>
#include <cstring>
#include <string>
#include <sstream>
#include <iomanip>
#include <vector>
#include <limits>

// =====================================================================
// Re-implement the standalone helper functions from crypto_handlers.cpp
// since they are declared 'inline' in the .cpp (not header-exported),
// we duplicate them here to test the logic independently.
// If these are ever moved to a shared header, replace with #include.
// =====================================================================

namespace crypto_helpers {

std::string bytes_to_hex_string(const std::vector<uint8_t>& bytes) {
    std::ostringstream oss;
    oss << "0x";
    for (uint8_t b : bytes) {
        oss << std::hex << std::setw(2) << std::setfill('0') << static_cast<int>(b);
    }
    return oss.str();
}

inline uint64_t read_le64(const uint8_t* ptr) {
    uint64_t value = 0;
    std::memcpy(&value, ptr, sizeof(uint64_t));
#if __BYTE_ORDER__ == __ORDER_BIG_ENDIAN__
    value = __builtin_bswap64(value);
#endif
    return value;
}

inline void write_le64(uint8_t* ptr, uint64_t value) {
#if __BYTE_ORDER__ == __ORDER_BIG_ENDIAN__
    value = __builtin_bswap64(value);
#endif
    std::memcpy(ptr, &value, sizeof(uint64_t));
}

inline uint32_t read_be32(const uint8_t* ptr) {
    uint32_t value = 0;
    std::memcpy(&value, ptr, sizeof(uint32_t));
#if __BYTE_ORDER__ == __ORDER_LITTLE_ENDIAN__
    value = __builtin_bswap32(value);
#endif
    return value;
}

inline uint32_t read_le32(const uint8_t* ptr) {
    uint32_t value = 0;
    for (int i = 0; i < 4; ++i) {
        value |= static_cast<uint32_t>(ptr[i]) << (i * 8);
    }
    return value;
}

} // namespace crypto_helpers

using namespace crypto_helpers;

// =====================================================================
// bytes_to_hex_string
// =====================================================================
TEST_SUITE("bytes_to_hex_string") {
    TEST_CASE("empty vector") {
        std::vector<uint8_t> v;
        CHECK(bytes_to_hex_string(v) == "0x");
    }

    TEST_CASE("single byte zero") {
        std::vector<uint8_t> v = {0x00};
        CHECK(bytes_to_hex_string(v) == "0x00");
    }

    TEST_CASE("single byte 0xFF") {
        std::vector<uint8_t> v = {0xFF};
        CHECK(bytes_to_hex_string(v) == "0xff");
    }

    TEST_CASE("multiple bytes") {
        std::vector<uint8_t> v = {0xDE, 0xAD, 0xBE, 0xEF};
        CHECK(bytes_to_hex_string(v) == "0xdeadbeef");
    }

    TEST_CASE("leading zeros preserved") {
        std::vector<uint8_t> v = {0x00, 0x01, 0x02};
        CHECK(bytes_to_hex_string(v) == "0x000102");
    }

    TEST_CASE("all zeros 32 bytes") {
        std::vector<uint8_t> v(32, 0);
        std::string expected = "0x" + std::string(64, '0');
        CHECK(bytes_to_hex_string(v) == expected);
    }
}

// =====================================================================
// read_le64 / write_le64 roundtrip
// =====================================================================
TEST_SUITE("read_le64_write_le64") {
    TEST_CASE("roundtrip zero") {
        uint8_t buf[8] = {};
        write_le64(buf, 0);
        CHECK(read_le64(buf) == 0);
    }

    TEST_CASE("roundtrip value 1") {
        uint8_t buf[8] = {};
        write_le64(buf, 1);
        CHECK(read_le64(buf) == 1);
        // In little-endian, first byte should be 1
        CHECK(buf[0] == 1);
        for (int i = 1; i < 8; ++i) CHECK(buf[i] == 0);
    }

    TEST_CASE("roundtrip 0x0102030405060708") {
        uint64_t val = 0x0102030405060708ULL;
        uint8_t buf[8] = {};
        write_le64(buf, val);
        CHECK(read_le64(buf) == val);
        // Little-endian: LSB first
        CHECK(buf[0] == 0x08);
        CHECK(buf[1] == 0x07);
        CHECK(buf[2] == 0x06);
        CHECK(buf[3] == 0x05);
        CHECK(buf[4] == 0x04);
        CHECK(buf[5] == 0x03);
        CHECK(buf[6] == 0x02);
        CHECK(buf[7] == 0x01);
    }

    TEST_CASE("roundtrip max uint64") {
        uint64_t val = std::numeric_limits<uint64_t>::max();
        uint8_t buf[8] = {};
        write_le64(buf, val);
        CHECK(read_le64(buf) == val);
        for (int i = 0; i < 8; ++i) CHECK(buf[i] == 0xFF);
    }

    TEST_CASE("roundtrip 0xDEADBEEF00000000") {
        uint64_t val = 0xDEADBEEF00000000ULL;
        uint8_t buf[8] = {};
        write_le64(buf, val);
        CHECK(read_le64(buf) == val);
    }

    TEST_CASE("write to non-zero buffer preserves surroundings") {
        uint8_t buf[16];
        std::memset(buf, 0xAA, 16);
        write_le64(buf + 4, 42);
        // Surrounding bytes untouched
        for (int i = 0; i < 4; ++i) CHECK(buf[i] == 0xAA);
        for (int i = 12; i < 16; ++i) CHECK(buf[i] == 0xAA);
        // Written value correct
        CHECK(read_le64(buf + 4) == 42);
    }
}

// =====================================================================
// read_be32
// =====================================================================
TEST_SUITE("read_be32") {
    TEST_CASE("zero") {
        uint8_t buf[4] = {0, 0, 0, 0};
        CHECK(read_be32(buf) == 0);
    }

    TEST_CASE("value 1 stored as big-endian") {
        uint8_t buf[4] = {0x00, 0x00, 0x00, 0x01};
        CHECK(read_be32(buf) == 1);
    }

    TEST_CASE("value 0x01020304") {
        uint8_t buf[4] = {0x01, 0x02, 0x03, 0x04};
        CHECK(read_be32(buf) == 0x01020304);
    }

    TEST_CASE("max uint32") {
        uint8_t buf[4] = {0xFF, 0xFF, 0xFF, 0xFF};
        CHECK(read_be32(buf) == 0xFFFFFFFF);
    }

    TEST_CASE("0x00000100 (256)") {
        uint8_t buf[4] = {0x00, 0x00, 0x01, 0x00};
        CHECK(read_be32(buf) == 256);
    }

    TEST_CASE("BLAKE2f rounds value 12") {
        // In BLAKE2f precompile, rounds is stored as 4-byte big-endian
        uint8_t buf[4] = {0x00, 0x00, 0x00, 0x0C};
        CHECK(read_be32(buf) == 12);
    }
}

// =====================================================================
// read_le32
// =====================================================================
TEST_SUITE("read_le32") {
    TEST_CASE("zero") {
        uint8_t buf[4] = {0, 0, 0, 0};
        CHECK(read_le32(buf) == 0);
    }

    TEST_CASE("value 1 stored as little-endian") {
        uint8_t buf[4] = {0x01, 0x00, 0x00, 0x00};
        CHECK(read_le32(buf) == 1);
    }

    TEST_CASE("value 0x04030201 stored as little-endian bytes 01 02 03 04") {
        uint8_t buf[4] = {0x01, 0x02, 0x03, 0x04};
        CHECK(read_le32(buf) == 0x04030201);
    }

    TEST_CASE("max uint32") {
        uint8_t buf[4] = {0xFF, 0xFF, 0xFF, 0xFF};
        CHECK(read_le32(buf) == 0xFFFFFFFF);
    }

    TEST_CASE("256 in little-endian") {
        uint8_t buf[4] = {0x00, 0x01, 0x00, 0x00};
        CHECK(read_le32(buf) == 256);
    }
}

// =====================================================================
// Cross-validation: read_be32 vs read_le32 with same bytes
// =====================================================================
TEST_SUITE("be32_vs_le32") {
    TEST_CASE("same bytes produce different values") {
        uint8_t buf[4] = {0x01, 0x02, 0x03, 0x04};
        uint32_t be = read_be32(buf);
        uint32_t le = read_le32(buf);
        CHECK(be == 0x01020304);
        CHECK(le == 0x04030201);
        CHECK(be != le);
    }

    TEST_CASE("symmetric values are equal in both encodings") {
        // All same bytes → same value regardless of endianness
        uint8_t buf[4] = {0xAB, 0xAB, 0xAB, 0xAB};
        CHECK(read_be32(buf) == read_le32(buf));
    }
}

// =====================================================================
// BLAKE2f-specific test: roundtrip state vector h[8]
// Simulates the input parsing in Blake2f precompile
// =====================================================================
TEST_SUITE("blake2f_state_roundtrip") {
    TEST_CASE("write 8 uint64 LE then read back") {
        uint64_t h[8] = {
            0x6A09E667F3BCC908ULL,
            0xBB67AE8584CAA73BULL,
            0x3C6EF372FE94F82BULL,
            0xA54FF53A5F1D36F1ULL,
            0x510E527FADE682D1ULL,
            0x9B05688C2B3E6C1FULL,
            0x1F83D9ABFB41BD6BULL,
            0x5BE0CD19137E2179ULL
        };

        // Write state vector
        uint8_t buf[64];
        for (int i = 0; i < 8; ++i) {
            write_le64(buf + i * 8, h[i]);
        }

        // Read back and verify
        for (int i = 0; i < 8; ++i) {
            CHECK(read_le64(buf + i * 8) == h[i]);
        }
    }

    TEST_CASE("write and read t[2] counter") {
        uint64_t t[2] = {128, 0}; // Common BLAKE2 counter values
        uint8_t buf[16];
        write_le64(buf, t[0]);
        write_le64(buf + 8, t[1]);
        CHECK(read_le64(buf) == 128);
        CHECK(read_le64(buf + 8) == 0);
    }
}
