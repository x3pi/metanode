#pragma once
// TLS-free drop-in replacement for <uuid/uuid.h>'s uuid_generate_random()/
// uuid_unparse_lower(), used ONLY for the chcore/musl TA build (GĐ3).
//
// Root cause this exists to work around (found via a real boot-time UART
// capture, 2026-08-17): chcore's secure-world dynamic loader
// (libc_shared.so) rejects any ELF with a PT_TLS segment outright
// ("Not a valid dynamic program") -- it has no TLS-block-allocation
// support at all. The real libuuid.a's uuid_generate_random() (via
// la-gen_uuid.o/libuuid_la-randutils.o) uses thread-local state
// internally, so *statically* linking it into mvm_ta unconditionally
// produces a PT_TLS segment in the final executable, regardless of
// whether mvm_ta's own code ever spawns a second thread. llama-cli never
// hits this because it never links libuuid at all.
//
// Point this repo's ONLY caller (linker/src/xapian/xapian_manager.cpp's
// generateUuidLogicalId()) at this header instead by putting this
// directory earlier than the real 3rdparty/include on the TA build's -I
// search path -- xapian_manager.cpp itself is untouched, so the real x86/
// cgo production build (which still links the real libuuid, no TLS
// concern on that target) is completely unaffected.
//
// Safety of this substitution: generateUuidLogicalId() only needs a
// "probably locally unique, per-process runtime tag" for internal
// XapianManager instance bookkeeping (see that function's own call site)
// -- it is NEVER a cryptographic value, NEVER compared across nodes, and
// NEVER part of consensus. A non-cryptographic, global- (not thread-)
// local PRNG is a strictly appropriate replacement for this specific use,
// not a shortcut that weakens anything real.

#include <cstdint>
#include <cstdio>
#include <ctime>

typedef unsigned char uuid_t[16];

namespace mvm_ta_uuid_detail {

// Global (NOT thread_local -- that's the entire point) xorshift128+ state.
// mvm_ta's own dispatch loop is single-threaded (see mvm_ta_main.cpp's
// design note on busy-poll over threads), so no lock is needed; even if
// that ever changes, a torn read here would only affect this non-security
// -critical ID's distribution, never correctness of anything else.
inline uint64_t g_state0 = 0;
inline uint64_t g_state1 = 0;
inline bool g_seeded = false;

inline void seed_once() {
    if (g_seeded) return;
    // Best-effort entropy for a non-cryptographic per-process tag: current
    // time (seconds), a local stack address (varies with ASLR/stack
    // layout if enabled), and this function's own code address (varies
    // with PIE load bias). None of this needs to be unpredictable to an
    // adversary -- only "different enough, run to run" to avoid two
    // XapianManager instances in the same process colliding.
    int local;
    uint64_t t = (uint64_t)time(nullptr);
    uint64_t stack_addr = (uint64_t)(uintptr_t)&local;
    uint64_t code_addr = (uint64_t)(uintptr_t)&seed_once;
    g_state0 = t ^ (stack_addr * 0x9E3779B97F4A7C15ULL);
    g_state1 = code_addr ^ (t * 0xBF58476D1CE4E5B9ULL) ^ 0xD1B54A32D192ED03ULL;
    if (g_state0 == 0 && g_state1 == 0) g_state0 = 1; // xorshift can't start at all-zero
    g_seeded = true;
}

inline uint64_t xorshift128plus() {
    seed_once();
    uint64_t x = g_state0;
    uint64_t const y = g_state1;
    g_state0 = y;
    x ^= x << 23;
    x ^= x >> 17;
    x ^= y ^ (y >> 26);
    g_state1 = x;
    return x + y;
}

} // namespace mvm_ta_uuid_detail

inline void uuid_generate_random(uuid_t out) {
    uint64_t a = mvm_ta_uuid_detail::xorshift128plus();
    uint64_t b = mvm_ta_uuid_detail::xorshift128plus();
    for (int i = 0; i < 8; i++) out[i] = (unsigned char)(a >> (i * 8));
    for (int i = 0; i < 8; i++) out[8 + i] = (unsigned char)(b >> (i * 8));
    // RFC4122 version/variant bits, same as the real libuuid does, so the
    // output still LOOKS like a valid UUIDv4 to anything that inspects it
    // (nothing in this codebase does today, but no reason not to match).
    out[6] = (unsigned char)((out[6] & 0x0F) | 0x40); // version 4
    out[8] = (unsigned char)((out[8] & 0x3F) | 0x80); // variant 10
}

inline void uuid_unparse_lower(const uuid_t uu, char *out) {
    // Standard 8-4-4-4-12 lowercase hex, matching the real libuuid's own
    // uuid_unparse_lower() output shape exactly.
    snprintf(out, 37,
        "%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
        uu[0], uu[1], uu[2], uu[3], uu[4], uu[5], uu[6], uu[7],
        uu[8], uu[9], uu[10], uu[11], uu[12], uu[13], uu[14], uu[15]);
}
