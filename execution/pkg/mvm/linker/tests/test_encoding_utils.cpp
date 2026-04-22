// Unit tests for encoding_utils.cpp (ABI encoding/decoding)
// Uses doctest framework (reused from c_mvm/3rdparty)

// Workaround: GCC 13+ glibc makes SIGSTKSZ a runtime value, older doctest needs constexpr.
#include <signal.h>
#undef SIGSTKSZ
#define SIGSTKSZ 8192

#define DOCTEST_CONFIG_IMPLEMENT_WITH_MAIN
#include <doctest/doctest.h>

#include "abi/encoding_utils.h"
#include <vector>
#include <string>
#include <cstdint>
#include <limits>

using namespace encoding;

// =====================================================================
// Helper: verify that a 32-byte slot is all zeros except where noted
// =====================================================================
static void requireSlotZeroExcept(const std::vector<uint8_t>& buf,
                                   size_t slotStart,
                                   size_t exceptStart,
                                   size_t exceptLen)
{
    for (size_t i = slotStart; i < slotStart + 32; ++i) {
        if (i >= exceptStart && i < exceptStart + exceptLen) continue;
        REQUIRE(buf[i] == 0);
    }
}

// =====================================================================
// appendUint8Padded
// =====================================================================
TEST_SUITE("appendUint8Padded") {
    TEST_CASE("zero value") {
        std::vector<uint8_t> buf;
        appendUint8Padded(buf, 0);
        REQUIRE(buf.size() == 32);
        for (auto b : buf) CHECK(b == 0);
    }

    TEST_CASE("non-zero value stored at byte 31") {
        std::vector<uint8_t> buf;
        appendUint8Padded(buf, 0xAB);
        REQUIRE(buf.size() == 32);
        CHECK(buf[31] == 0xAB);
        requireSlotZeroExcept(buf, 0, 31, 1);
    }

    TEST_CASE("max uint8") {
        std::vector<uint8_t> buf;
        appendUint8Padded(buf, 0xFF);
        CHECK(buf[31] == 0xFF);
        requireSlotZeroExcept(buf, 0, 31, 1);
    }

    TEST_CASE("appends to existing buffer") {
        std::vector<uint8_t> buf = {0x01, 0x02};
        appendUint8Padded(buf, 0x42);
        REQUIRE(buf.size() == 34); // 2 + 32
        CHECK(buf[0] == 0x01);
        CHECK(buf[1] == 0x02);
        CHECK(buf[33] == 0x42); // byte 31 of the slot (offset 2+31=33)
    }
}

// =====================================================================
// appendUint16Padded
// =====================================================================
TEST_SUITE("appendUint16Padded") {
    TEST_CASE("zero") {
        std::vector<uint8_t> buf;
        appendUint16Padded(buf, 0);
        REQUIRE(buf.size() == 32);
        for (auto b : buf) CHECK(b == 0);
    }

    TEST_CASE("value 0x1234 stored big-endian at bytes 30-31") {
        std::vector<uint8_t> buf;
        appendUint16Padded(buf, 0x1234);
        REQUIRE(buf.size() == 32);
        CHECK(buf[30] == 0x12);
        CHECK(buf[31] == 0x34);
        requireSlotZeroExcept(buf, 0, 30, 2);
    }

    TEST_CASE("max uint16") {
        std::vector<uint8_t> buf;
        appendUint16Padded(buf, 0xFFFF);
        CHECK(buf[30] == 0xFF);
        CHECK(buf[31] == 0xFF);
    }
}

// =====================================================================
// appendUint64Padded / appendUint256FromUint64 / appendUint256
// =====================================================================
TEST_SUITE("appendUint64") {
    TEST_CASE("zero fills 32 bytes with zeros") {
        std::vector<uint8_t> buf;
        appendUint64Padded(buf, 0);
        REQUIRE(buf.size() == 32);
        for (auto b : buf) CHECK(b == 0);
    }

    TEST_CASE("value 1 stored at byte 31") {
        std::vector<uint8_t> buf;
        appendUint256(buf, 1);
        CHECK(buf[31] == 1);
        requireSlotZeroExcept(buf, 0, 31, 1);
    }

    TEST_CASE("0x0102030405060708 stored big-endian in last 8 bytes") {
        std::vector<uint8_t> buf;
        uint64_t val = 0x0102030405060708ULL;
        appendUint256FromUint64(buf, val);
        REQUIRE(buf.size() == 32);
        CHECK(buf[24] == 0x01);
        CHECK(buf[25] == 0x02);
        CHECK(buf[26] == 0x03);
        CHECK(buf[27] == 0x04);
        CHECK(buf[28] == 0x05);
        CHECK(buf[29] == 0x06);
        CHECK(buf[30] == 0x07);
        CHECK(buf[31] == 0x08);
        // First 24 bytes must be zero
        for (int i = 0; i < 24; ++i) CHECK(buf[i] == 0);
    }

    TEST_CASE("max uint64") {
        std::vector<uint8_t> buf;
        appendUint256(buf, std::numeric_limits<uint64_t>::max());
        REQUIRE(buf.size() == 32);
        for (int i = 24; i < 32; ++i) CHECK(buf[i] == 0xFF);
        for (int i = 0; i < 24; ++i) CHECK(buf[i] == 0);
    }
}

// =====================================================================
// appendBoolPadded
// =====================================================================
TEST_SUITE("appendBoolPadded") {
    TEST_CASE("true") {
        std::vector<uint8_t> buf;
        appendBoolPadded(buf, true);
        REQUIRE(buf.size() == 32);
        CHECK(buf[31] == 1);
        requireSlotZeroExcept(buf, 0, 31, 1);
    }

    TEST_CASE("false") {
        std::vector<uint8_t> buf;
        appendBoolPadded(buf, false);
        REQUIRE(buf.size() == 32);
        for (auto b : buf) CHECK(b == 0);
    }
}

// =====================================================================
// appendBytesPadded
// =====================================================================
TEST_SUITE("appendBytesPadded") {
    TEST_CASE("empty data produces no output") {
        std::vector<uint8_t> buf;
        appendBytesPadded(buf, nullptr, 0);
        CHECK(buf.size() == 0);
    }

    TEST_CASE("1 byte padded to 32") {
        std::vector<uint8_t> buf;
        uint8_t data[] = {0xAB};
        appendBytesPadded(buf, data, 1);
        REQUIRE(buf.size() == 32);
        CHECK(buf[0] == 0xAB);
        for (size_t i = 1; i < 32; ++i) CHECK(buf[i] == 0);
    }

    TEST_CASE("32 bytes no extra padding") {
        std::vector<uint8_t> buf;
        uint8_t data[32];
        for (int i = 0; i < 32; ++i) data[i] = static_cast<uint8_t>(i);
        appendBytesPadded(buf, data, 32);
        REQUIRE(buf.size() == 32);
        for (int i = 0; i < 32; ++i) CHECK(buf[i] == static_cast<uint8_t>(i));
    }

    TEST_CASE("33 bytes padded to 64") {
        std::vector<uint8_t> buf;
        uint8_t data[33];
        for (int i = 0; i < 33; ++i) data[i] = static_cast<uint8_t>(i);
        appendBytesPadded(buf, data, 33);
        REQUIRE(buf.size() == 64);
        for (int i = 0; i < 33; ++i) CHECK(buf[i] == static_cast<uint8_t>(i));
        for (size_t i = 33; i < 64; ++i) CHECK(buf[i] == 0);
    }
}

// =====================================================================
// appendString
// =====================================================================
TEST_SUITE("appendString") {
    TEST_CASE("empty string: length=0, no data") {
        std::vector<uint8_t> buf;
        appendString(buf, "");
        // 32 bytes for length (0) + 0 bytes for data
        REQUIRE(buf.size() == 32);
        // The length slot should be all zeros
        for (auto b : buf) CHECK(b == 0);
    }

    TEST_CASE("short string") {
        std::vector<uint8_t> buf;
        std::string s = "hello";
        appendString(buf, s);
        // 32 bytes for length + 32 bytes for padded "hello" (5 chars)
        REQUIRE(buf.size() == 64);
        // Length slot: last byte = 5
        CHECK(buf[31] == 5);
        // Data: "hello"
        CHECK(buf[32] == 'h');
        CHECK(buf[33] == 'e');
        CHECK(buf[34] == 'l');
        CHECK(buf[35] == 'l');
        CHECK(buf[36] == 'o');
        // Padding
        for (size_t i = 37; i < 64; ++i) CHECK(buf[i] == 0);
    }

    TEST_CASE("32-char string: no extra padding") {
        std::vector<uint8_t> buf;
        std::string s(32, 'A');
        appendString(buf, s);
        // 32 (length) + 32 (data, exactly fills one slot)
        REQUIRE(buf.size() == 64);
        CHECK(buf[31] == 32);
        for (int i = 0; i < 32; ++i) CHECK(buf[32 + i] == 'A');
    }
}

// =====================================================================
// appendInt256 (two's complement)
// =====================================================================
TEST_SUITE("appendInt256") {
    TEST_CASE("positive value 42") {
        std::vector<uint8_t> buf;
        appendInt256(buf, 42);
        REQUIRE(buf.size() == 32);
        CHECK(buf[31] == 42);
        for (int i = 0; i < 31; ++i) CHECK(buf[i] == 0);
    }

    TEST_CASE("zero") {
        std::vector<uint8_t> buf;
        appendInt256(buf, 0);
        REQUIRE(buf.size() == 32);
        for (auto b : buf) CHECK(b == 0);
    }

    TEST_CASE("negative value -1 is all 0xFF") {
        std::vector<uint8_t> buf;
        appendInt256(buf, -1);
        REQUIRE(buf.size() == 32);
        for (auto b : buf) CHECK(b == 0xFF);
    }

    TEST_CASE("negative value -2") {
        std::vector<uint8_t> buf;
        appendInt256(buf, -2);
        REQUIRE(buf.size() == 32);
        CHECK(buf[31] == 0xFE);
        for (int i = 0; i < 31; ++i) CHECK(buf[i] == 0xFF);
    }

    TEST_CASE("INT64_MIN") {
        std::vector<uint8_t> buf;
        appendInt256(buf, std::numeric_limits<int64_t>::min());
        REQUIRE(buf.size() == 32);
        // First 24 bytes = 0xFF (sign extension)
        for (int i = 0; i < 24; ++i) CHECK(buf[i] == 0xFF);
        // Byte 24 = 0x80 (MSB of int64)
        CHECK(buf[24] == 0x80);
        // Bytes 25-31 = 0x00
        for (int i = 25; i < 32; ++i) CHECK(buf[i] == 0x00);
    }
}

// =====================================================================
// readUint256
// =====================================================================
TEST_SUITE("readUint256") {
    TEST_CASE("reads value written by appendUint256") {
        std::vector<uint8_t> buf;
        appendUint256(buf, 12345);
        uint64_t val = readUint256(buf, 0);
        CHECK(val == 12345);
    }

    TEST_CASE("reads max uint64") {
        std::vector<uint8_t> buf;
        appendUint256(buf, std::numeric_limits<uint64_t>::max());
        uint64_t val = readUint256(buf, 0);
        CHECK(val == std::numeric_limits<uint64_t>::max());
    }

    TEST_CASE("buffer overflow throws") {
        std::vector<uint8_t> buf(16, 0); // too short
        CHECK_THROWS_AS(readUint256(buf, 0), std::out_of_range);
    }

    TEST_CASE("offset overflow throws") {
        std::vector<uint8_t> buf(32, 0);
        CHECK_THROWS_AS(readUint256(buf, 1), std::out_of_range);
    }
}

// =====================================================================
// readUint64Padded
// =====================================================================
TEST_SUITE("readUint64Padded") {
    TEST_CASE("roundtrip") {
        std::vector<uint8_t> buf;
        appendUint64Padded(buf, 0xDEADBEEF);
        uint64_t val = readUint64Padded(buf, 0);
        CHECK(val == 0xDEADBEEF);
    }

    TEST_CASE("buffer too small throws") {
        std::vector<uint8_t> buf(31, 0);
        CHECK_THROWS_AS(readUint64Padded(buf, 0), std::out_of_range);
    }
}

// =====================================================================
// readUint256AsUint64
// =====================================================================
TEST_SUITE("readUint256AsUint64") {
    TEST_CASE("roundtrip") {
        std::vector<uint8_t> buf;
        appendUint256(buf, 999999);
        uint64_t val = readUint256AsUint64(buf, 0);
        CHECK(val == 999999);
    }

    TEST_CASE("overflow returns max uint64") {
        std::vector<uint8_t> buf(32, 0);
        buf[0] = 0x01; // Set a high byte, value > uint64 max
        uint64_t val = readUint256AsUint64(buf, 0);
        CHECK(val == std::numeric_limits<uint64_t>::max());
    }

    TEST_CASE("buffer too small throws") {
        std::vector<uint8_t> buf(16, 0);
        CHECK_THROWS_AS(readUint256AsUint64(buf, 0), std::out_of_range);
    }
}

// =====================================================================
// readBoolPadded
// =====================================================================
TEST_SUITE("readBoolPadded") {
    TEST_CASE("true roundtrip") {
        std::vector<uint8_t> buf;
        appendBoolPadded(buf, true);
        CHECK(readBoolPadded(buf, 0) == true);
    }

    TEST_CASE("false roundtrip") {
        std::vector<uint8_t> buf;
        appendBoolPadded(buf, false);
        CHECK(readBoolPadded(buf, 0) == false);
    }

    TEST_CASE("buffer too small throws") {
        std::vector<uint8_t> buf(16, 0);
        CHECK_THROWS_AS(readBoolPadded(buf, 0), std::out_of_range);
    }
}

// =====================================================================
// readString (length-prefixed)
// =====================================================================
TEST_SUITE("readString") {
    TEST_CASE("roundtrip with appendString") {
        std::vector<uint8_t> buf;
        appendString(buf, "Hello World");
        std::string result = readString(buf, 0);
        CHECK(result == "Hello World");
    }

    TEST_CASE("empty string roundtrip") {
        std::vector<uint8_t> buf;
        appendString(buf, "");
        std::string result = readString(buf, 0);
        CHECK(result == "");
    }

    TEST_CASE("buffer too small for length throws") {
        std::vector<uint8_t> buf(16, 0);
        CHECK_THROWS_AS(readString(buf, 0), std::out_of_range);
    }
}

// =====================================================================
// readBytesPadded
// =====================================================================
TEST_SUITE("readBytesPadded") {
    TEST_CASE("roundtrip") {
        std::vector<uint8_t> buf;
        uint8_t data[] = {0x01, 0x02, 0x03, 0x04, 0x05};
        appendBytesPadded(buf, data, 5);
        auto result = readBytesPadded(buf, 0, 5);
        REQUIRE(result.size() == 5);
        for (int i = 0; i < 5; ++i) CHECK(result[i] == data[i]);
    }

    TEST_CASE("buffer overflow throws") {
        std::vector<uint8_t> buf(3, 0);
        CHECK_THROWS_AS(readBytesPadded(buf, 0, 10), std::out_of_range);
    }
}

// =====================================================================
// readStringFromData
// =====================================================================
TEST_SUITE("readStringFromData") {
    TEST_CASE("reads exact substring") {
        std::vector<uint8_t> buf = {'H', 'e', 'l', 'l', 'o', 0, 0, 0};
        std::string result = readStringFromData(buf, 0, 5);
        CHECK(result == "Hello");
    }

    TEST_CASE("buffer overflow throws") {
        std::vector<uint8_t> buf = {'H', 'i'};
        CHECK_THROWS_AS(readStringFromData(buf, 0, 10), std::out_of_range);
    }
}

// =====================================================================
// readStringDynamic
// =====================================================================
TEST_SUITE("readStringDynamic") {
    TEST_CASE("reads dynamically-offset string") {
        // Build an ABI-like buffer:
        // Offset 0: offset pointer (value = 32, points to data at byte 32)
        // Offset 32: length of string (value = 5)
        // Offset 64: string data "hello" + padding
        std::vector<uint8_t> buf;
        appendUint256(buf, 32); // offset pointer -> data starts at byte 32
        appendUint256(buf, 5);  // length = 5
        // data "hello" padded to 32 bytes
        appendBytesPadded(buf, reinterpret_cast<const uint8_t*>("hello"), 5);

        std::string result = readStringDynamic(buf, 0);
        CHECK(result == "hello");
    }

    TEST_CASE("offset out of bounds throws") {
        std::vector<uint8_t> buf(16, 0);
        CHECK_THROWS_AS(readStringDynamic(buf, 0), std::out_of_range);
    }
}

// =====================================================================
// getPaddedSize
// =====================================================================
TEST_SUITE("getPaddedSize") {
    TEST_CASE("zero") {
        CHECK(getPaddedSize(0) == 0);
    }

    TEST_CASE("1 -> 32") {
        CHECK(getPaddedSize(1) == 32);
    }

    TEST_CASE("31 -> 32") {
        CHECK(getPaddedSize(31) == 32);
    }

    TEST_CASE("32 -> 32") {
        CHECK(getPaddedSize(32) == 32);
    }

    TEST_CASE("33 -> 64") {
        CHECK(getPaddedSize(33) == 64);
    }

    TEST_CASE("64 -> 64") {
        CHECK(getPaddedSize(64) == 64);
    }

    TEST_CASE("65 -> 96") {
        CHECK(getPaddedSize(65) == 96);
    }
}

// =====================================================================
// Multi-slot roundtrip tests
// =====================================================================
TEST_SUITE("roundtrip_multi") {
    TEST_CASE("multiple values in one buffer") {
        std::vector<uint8_t> buf;

        // Write: uint64=42, bool=true, uint8=0xFF, uint16=0x1234
        appendUint256(buf, 42);
        appendBoolPadded(buf, true);
        appendUint8Padded(buf, 0xFF);
        appendUint16Padded(buf, 0x1234);

        REQUIRE(buf.size() == 128); // 4 * 32

        // Read back at correct offsets
        CHECK(readUint256(buf, 0) == 42);
        CHECK(readBoolPadded(buf, 32) == true);
        // readUint256 for uint8 slot
        CHECK(readUint256(buf, 64) == 0xFF);
        CHECK(readUint256(buf, 96) == 0x1234);
    }

    TEST_CASE("string followed by uint64") {
        std::vector<uint8_t> buf;
        appendString(buf, "test");
        size_t afterString = buf.size();
        appendUint256(buf, 7777);

        // Read string
        std::string s = readString(buf, 0);
        CHECK(s == "test");

        // Read uint64 after string
        uint64_t val = readUint256(buf, afterString);
        CHECK(val == 7777);
    }
}
