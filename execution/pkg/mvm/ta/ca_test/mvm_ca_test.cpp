// Minimal normal-world CA test tool for metanode's mvm_ta (GĐ3, real
// hardware). NOT part of any production CA — this exists solely to prove,
// for the first time, that the full round trip actually works on real
// hardware: chanmgr launches /mvm_ta -> mvm_ta pushes its TZASC CMA
// channel -> THIS program discovers/maps that exact same physical page
// from normal-world Linux -> sends a real MVM_TZ_CMD_EXECUTE -> answers
// mvm_ta's GlobalStateGet reverse calls -> reads back a real ExecuteResult
// with actual state-change entries.
//
// Deliberately independent of tz-llm-trustzone's own LLM CA/TA code (per
// the standing project-separation requirement) -- but the device-open/
// ioctl/mmap SEQUENCE below is copied verbatim from tz-llm-trustzone's
// OWN already-proven-on-this-exact-board pattern
// (tz-llm/llama.cpp/src/alloc-stage.cpp's AllocTask::step()), because
// this is the OS/driver's own public mechanism (not LLM-specific logic)
// for mapping a TZASC CMA (cma_index, entry_index) pair from normal
// world -- confirmed by reading tzdriver/core/tc_client_driver.c's
// llm_set_pages()/llm_client_mmap() directly, not guessed.
//
// Build: standard aarch64 glibc cross-compile (aarch64-linux-gnu-g++),
// NOT musl-gcc/chcore-libc -- this runs as an ordinary normal-world Linux
// process, same runtime as llama-cli's own CA side.
//
// See note/tee_dual_mode_execution_plan.md GĐ3 §9.9 for the full
// narrative and known caveats (this is a ONE-SHOT correctness probe, not
// a real CA -- e.g. it answers GlobalStateGet with a hardcoded "not
// found, create fresh" for every address, which is only valid because
// this test targets two never-before-seen synthetic addresses on a
// freshly booted TA with no prior state).

#include "mvm_tz_protocol.h"
#include "blst.h"

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <cstdint>
#include <ctime>
#include <string>
#include <vector>
#include <map>

#include <fcntl.h>
#include <unistd.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <errno.h>
#include <pthread.h>
#include <sched.h>

// ─── driver ioctl/struct definitions ───
// Byte-identical copy of tz-llm-trustzone's tzdriver/tc_ns_client.h
// struct (cross-checked against tz-llm/llama.cpp/src/alloc-stage.cpp's
// own hand-copy, which itself carries a static_assert(sizeof==24) guarding
// exactly this drift risk -- reproduced here for the same reason).
struct llm_client_op_pages {
    int cma_index;
    int entry_index;
    unsigned long size;
    unsigned long offset;
};
static_assert(sizeof(struct llm_client_op_pages) == 24,
    "llm_client_op_pages size drifted from tzdriver's tc_ns_client.h");

#define DEVICE_NAME "/dev/tc_ns_client"
#define TC_NS_CLIENT_IOC_MAGIC 't'
#define LLM_CLIENT_IOCTL_SET_PAGES \
    _IOWR(TC_NS_CLIENT_IOC_MAGIC, 27, struct llm_client_op_pages)
// Cmd 24, byte-identical to tz-llm-trustzone/llama.cpp/examples/main/
// fake_ca.cpp's own #define -- the relay/service ioctl. Added 2026-08-18
// after finding, via real hardware + kernel source reading (dmesg showing
// "entry->size=0" no matter what order mvm_ta called its own two
// syscalls in), that mvm_ta's push_pages() is TA-*initiated*
// (usys_tee_switch_req(SMC_EXIT_SHADOW)) and structurally cannot
// complete until SOME Normal-World thread is actively looping on this
// exact ioctl -- see tzdriver/core/tc_client_driver.c's
// smc_call_cpu_resume(), only ever called from llm_run()'s
// LLM_CLIENT_IOCTL_RUN handler, itself only ever invoked by userspace
// "ca_thread" pthreads (fake_ca.cpp's own, for the LLM TA). Nothing
// analogous previously existed in this file -- mvm_ta's push_pages() SMC
// request just sat parked (blocked, not spinning) with nothing servicing
// it until this relay thread was added.
#define LLM_CLIENT_IOCTL_RUN \
    _IOWR(TC_NS_CLIENT_IOC_MAGIC, 24, int)
enum smc_loop_exit {
    SMC_LOOP_EXIT_FINISH = 1,
    SMC_LOOP_EXIT_NPU_SUBMIT,
    SMC_LOOP_EXIT_NPU_DONE,
    SMC_LOOP_EXIT_IO_STEP,
};

// mvm_ta_main.cpp's own channel placement (mvm_ta_main.cpp:
// MVM_TZASC_CMA_INDEX=1, entry_index guaranteed 0 by the launch-order
// constraint chanmgr.c's patch enforces -- see plan doc §9.5).
#define MVM_TZASC_CMA_INDEX 1
#define MVM_TZASC_ENTRY_INDEX 0

static mvm_tz_channel_t *g_channel = nullptr;
static volatile bool g_relay_stop = false;

// Mirrors fake_ca.cpp's ca_thread() minus the LLM-specific
// ca_backend_io_step()/[SECURE_LOGIT_DIAG] polling (mvm_ta doesn't use
// the io-frontend/logit-diag mechanisms those exist for -- this relay's
// only job is to keep calling the kernel's SMC-servicing ioctl so
// whatever mvm_ta is blocked on inside a yielding SMC eventually gets
// answered). Same idle-backoff logic (rate-limit sleeping once genuinely
// idle) for the same documented reason: an unthrottled busy-poll loop on
// this exact ioctl has previously starved wifi/display board-wide within
// minutes even with zero kernel soft-lockup.
static void *ca_relay_thread(void *arg) {
    int fd = *(int *)arg;
    printf("[mvm_ca_test] relay thread started (fd=%d)\n", fd);
    fflush(stdout);
    unsigned int consecutive_idle = 0;
    const unsigned int idle_backoff_threshold = 2000;
    while (!g_relay_stop) {
        int out_cmd = -1;
        ioctl(fd, LLM_CLIENT_IOCTL_RUN, &out_cmd);
        if (out_cmd == SMC_LOOP_EXIT_FINISH) {
            if (++consecutive_idle > idle_backoff_threshold) {
                usleep(1000);
            }
        } else {
            consecutive_idle = 0;
        }
        sched_yield();
    }
    return nullptr;
}

// ─── blob writer (mirrors mvm_ta_main.cpp's BlobWriter) ───
struct BlobWriter {
    uint8_t *buf;
    uint32_t off = 0;
    void writeRaw(const void *p, uint32_t n) { memcpy(buf + off, p, n); off += n; }
    void writeU32(uint32_t v) { writeRaw(&v, 4); }
    void writeBytes(const void *p, uint32_t n) { writeU32(n); if (n) writeRaw(p, n); }
};

// ─── reverse-call handling (TA -> this CA) ───
// Answers only what a single native-value-transfer EXECUTE between two
// never-before-seen synthetic addresses can plausibly need. Anything else
// aborts loudly (a clean, diagnosable failure) rather than silently
// hanging or fabricating wrong data.
// Contract-call test (2026-08-20, plan §9.24 follow-up): to exercise a
// REAL contract's code path (SSTORE/SLOAD -> real GetStorageValue traffic,
// not the "shouldn't fire" stub) without needing MVM_TZ_CMD_DEPLOY (not
// wired in mvm_ta yet -- see that file's own comment), simulate "this
// address already has code" entirely from the CA side: GlobalStateGet's
// response for ONE specific address returns status=1 with real bytecode
// in the code blob. mvm_ta's own EVM interpreter (unmodified, already
// proven via cgo mode) does the rest -- this only fakes what a real
// Host's state lookup would have returned for an already-deployed
// contract, nothing about mvm_ta's own logic is being test-specific here.
static const uint8_t g_contract_addr[20] = {
    0x33,0x33,0x33,0x33,0x33,0x33,0x33,0x33,0x33,0x33,
    0x33,0x33,0x33,0x33,0x33,0x33,0x33,0x33,0x33,0x33};
// PUSH1 0x2a PUSH1 0x00 SSTORE PUSH1 0x00 SLOAD PUSH1 0x00 MSTORE
// PUSH1 0x20 PUSH1 0x00 RETURN -- store 42 at slot 0, read it back,
// return it. Plain, well-known EVM bytecode, not metanode-specific.
static const uint8_t g_contract_code[] = {
    0x60,0x2a, 0x60,0x00, 0x55, 0x60,0x00, 0x54,
    0x60,0x00, 0x52, 0x60,0x20, 0x60,0x00, 0xf3};

// 2026-08-20 (plan §9.28 follow-up): a SEPARATE contract address whose
// bytecode does a BARE SLOAD (no SSTORE first, in this tx or any prior
// one) -- unlike g_contract_addr's test above, this one exercises
// GET_STORAGE_VALUE's REVERSE-CALL path with a real, non-trivial,
// pre-existing value the Host (this CA test tool) fabricates, instead of
// always answering NOT_FOUND for a slot this same tx just wrote. This is
// the actual "storage reverse-call with real data" case: a value that
// genuinely round-trips over the wire (GET_STORAGE_VALUE resp blob ->
// my_storage.cpp's load() -> SLOAD's stack result -> MSTORE -> RETURN),
// not just mvm_ta's own in-process SSTORE-then-SLOAD cache.
static const uint8_t g_storage_test_addr[20] = {
    0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,
    0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44};
// PUSH1 0x00 SLOAD PUSH1 0x00 MSTORE PUSH1 0x20 PUSH1 0x00 RETURN --
// read slot 0 (never written this tx), return it.
static const uint8_t g_storage_read_code[] = {
    0x60,0x00, 0x54, 0x60,0x00, 0x52, 0x60,0x20, 0x60,0x00, 0xf3};
// The value handle_reverse_call() answers GET_STORAGE_VALUE with for
// (g_storage_test_addr, slot 0) -- 0x1337 (4919 decimal), chosen to be
// unmistakable in the printed result (not 0, not something a bug could
// produce by accident like a leftover 0x2a from the other test).
static const uint8_t g_storage_test_value[32] = {
    0,0,0,0, 0,0,0,0, 0,0,0,0, 0,0,0,0,
    0,0,0,0, 0,0,0,0, 0,0,0,0, 0,0,0x13,0x37};

// ─── minimal Solidity ABI helpers for the SIMPLE_DATABASE_ADDRESS (261)
// precompile's set(string,string,string)/get(string,string) dispatch --
// 2026-08-20 (plan §9.31, first of the 4 extension reverse-calls to get
// REAL data, per plan doc/mvm_tz_protocol.h's own scoping note). Just
// enough encode/decode for this test's own fixed, short (<=32 bytes)
// strings -- NOT a general ABI codec (no multi-chunk dynamic data,
// nested types, etc.), matching this file's own stated scope (diagnostic
// probe, not a production CA). Selectors are real go-ethereum-standard
// `keccak256(signature)[0:4]` values (verified via `cast sig`), matching
// exactly what extension.go's ExtensionGetOrCreateSimpleDb (the real cgo
// implementation) parses from the same ABI (see that file's abiString).
static const uint32_t SIMPLEDB_SET_SELECTOR = 0xda465d74; // set(string,string,string)
static const uint32_t SIMPLEDB_GET_SELECTOR = 0x3e10510b; // get(string,string)

static void abi_write_u256_be(uint8_t *out, uint32_t v) {
    memset(out, 0, 32);
    out[28] = (uint8_t)(v >> 24); out[29] = (uint8_t)(v >> 16);
    out[30] = (uint8_t)(v >> 8);  out[31] = (uint8_t)v;
}
static uint32_t abi_read_u256_be(const uint8_t *in) {
    return ((uint32_t)in[28] << 24) | ((uint32_t)in[29] << 16)
         | ((uint32_t)in[30] << 8) | (uint32_t)in[31];
}

// Encodes calldata for a function taking N "string" args, each <=32
// bytes (fits in a single 32-byte data chunk -- no multi-word strings in
// this test). Writes selector + ABI head/tail into out, returns total
// length.
static uint32_t abi_encode_string_args(uint32_t selector,
    const std::vector<std::string> &args, uint8_t *out) {
    out[0] = (uint8_t)(selector >> 24); out[1] = (uint8_t)(selector >> 16);
    out[2] = (uint8_t)(selector >> 8);  out[3] = (uint8_t)selector;
    uint32_t n = (uint32_t)args.size();
    uint32_t off = 4 + n * 32;      // absolute write cursor
    uint32_t data_cursor = n * 32;  // offset relative to args-area start
    for (uint32_t i = 0; i < n; i++) {
        if (args[i].size() > 32) {
            fprintf(stderr, "[mvm_ca_test] FATAL: abi_encode_string_args: "
                "arg %u is %zu bytes, >32 unsupported by this minimal codec\n",
                i, args[i].size());
            exit(1);
        }
        abi_write_u256_be(out + 4 + i * 32, data_cursor);
        abi_write_u256_be(out + off, (uint32_t)args[i].size());
        memcpy(out + off + 32, args[i].data(), args[i].size());
        memset(out + off + 32 + args[i].size(), 0, 32 - args[i].size());
        off += 64;
        data_cursor += 64;
    }
    return off;
}

// Decodes N "string" args (<=32 bytes each) from a calldata blob's args
// area (i.e. blob+4, past the selector). Returns false on any bounds
// violation -- never trusts a malformed/adversarial payload, matches this
// file's stated "fail loud over guessing" policy.
static bool abi_decode_string_args(const uint8_t *args, uint32_t args_len,
    uint32_t n, std::vector<std::string> &out) {
    out.clear();
    for (uint32_t i = 0; i < n; i++) {
        if ((uint64_t)(i + 1) * 32 > args_len) return false;
        uint32_t rel_off = abi_read_u256_be(args + i * 32);
        if ((uint64_t)rel_off + 32 > args_len) return false;
        uint32_t len = abi_read_u256_be(args + rel_off);
        if (len > 32 || (uint64_t)rel_off + 32 + len > args_len) return false;
        out.emplace_back((const char *)(args + rel_off + 32), len);
    }
    return true;
}

// Encodes a single "bool" return value (32-byte word, 0 or 1) -- matches
// set(...)'s real ABI ("outputs":[{"type":"bool"}]) in extension.go.
static uint32_t abi_encode_bool(bool v, uint8_t *out) {
    abi_write_u256_be(out, v ? 1 : 0);
    return 32;
}

// Encodes a single "string" return value (<=32 bytes) -- matches
// get(...)'s real ABI ("outputs":[{"type":"string"}]) in extension.go.
static uint32_t abi_encode_string_ret(const std::string &s, uint8_t *out) {
    if (s.size() > 32) {
        fprintf(stderr, "[mvm_ca_test] FATAL: abi_encode_string_ret: "
            "%zu bytes, >32 unsupported by this minimal codec\n", s.size());
        exit(1);
    }
    abi_write_u256_be(out, 32);                    // offset
    abi_write_u256_be(out + 32, (uint32_t)s.size()); // length
    memcpy(out + 64, s.data(), s.size());
    memset(out + 64 + s.size(), 0, 32 - s.size());
    return 96;
}

// Fake Host-side "simple DB" -- a plain in-process map standing in for
// what a real Host would persist via its own storage layer (this reverse
// call's whole point per mvm_tz_protocol.h's doc comment: the TA never
// touches storage directly here, the Host does). Keyed by "dbName\0key"
// (NUL can't appear in this test's own fixed ASCII test strings).
static std::map<std::string, std::string> g_fake_simpledb;

// 2026-08-20 (plan §9.31): a THIRD synthetic contract address, whose code
// is a generic "calldata forwarder to a fixed precompile address" --
// copies the tx's own calldata into memory, CALLs SIMPLE_DATABASE_ADDRESS
// (261 = 0x0105) with it verbatim, then RETURNs whatever that call
// returns. A standard EVM proxy/forwarder pattern, not metanode-specific.
// Lets ONE piece of bytecode exercise both set(...) and get(...) just by
// varying the calldata each EXECUTE call passes in (mvm_tz_execute_req_t
// already supports a real `input`/`input_len` -- see
// run_execute_and_print's own signature -- unlike g_contract_code/
// g_storage_read_code above, which never needed real calldata).
static const uint8_t g_simpledb_test_addr[20] = {
    0x55,0x55,0x55,0x55,0x55,0x55,0x55,0x55,0x55,0x55,
    0x55,0x55,0x55,0x55,0x55,0x55,0x55,0x55,0x55,0x55};
static const uint8_t g_simpledb_forwarder_code[] = {
    0x36,                   // CALLDATASIZE
    0x60,0x00,               // PUSH1 0x00 (offset)
    0x60,0x00,               // PUSH1 0x00 (destOffset)
    0x37,                   // CALLDATACOPY -- mem[0:cds] = calldata
    0x60,0x00,               // PUSH1 0x00 (retSize)
    0x60,0x00,               // PUSH1 0x00 (retOffset)
    0x36,                   // CALLDATASIZE (argsSize)
    0x60,0x00,               // PUSH1 0x00 (argsOffset)
    0x60,0x00,               // PUSH1 0x00 (value)
    0x61,0x01,0x05,           // PUSH2 0x0105 (addr=261=SIMPLE_DATABASE_ADDRESS)
    0x5a,                   // GAS
    0xf1,                   // CALL
    0x50,                   // POP -- discard success flag (diagnostic
                             // test, a failure shows up as empty/wrong
                             // RETURN output instead, visible either way)
    0x3d,                   // RETURNDATASIZE
    0x60,0x00,               // PUSH1 0x00 (offset)
    0x60,0x00,               // PUSH1 0x00 (destOffset)
    0x3e,                   // RETURNDATACOPY -- mem[0:rds] = returndata
    0x3d,                   // RETURNDATASIZE
    0x60,0x00,               // PUSH1 0x00 (offset)
    0xf3                    // RETURN mem[0:rds]
};

// ─── BLST (2026-08-20, plan §9.31 follow-up: second of the 4 extension
// reverse-calls with real data) ───
//
// Same calldata-forwarder pattern as g_simpledb_forwarder_code above, just
// targeting precompile 259 (0x0103 = BLST) instead of 261. A FOURTH
// synthetic contract address (0x66x20) -- kept separate from
// g_simpledb_test_addr rather than reused, so each test's GlobalStateGet
// case stays a simple 1:1 address->code lookup.
static const uint8_t g_blst_test_addr[20] = {
    0x66,0x66,0x66,0x66,0x66,0x66,0x66,0x66,0x66,0x66,
    0x66,0x66,0x66,0x66,0x66,0x66,0x66,0x66,0x66,0x66};
static const uint8_t g_blst_forwarder_code[] = {
    0x36, 0x60,0x00, 0x60,0x00, 0x37,           // CALLDATASIZE/PUSH1 0/PUSH1 0/CALLDATACOPY
    0x60,0x00, 0x60,0x00,                       // PUSH1 0 (retSize) / PUSH1 0 (retOffset)
    0x36, 0x60,0x00, 0x60,0x00,                 // CALLDATASIZE(argsSize)/PUSH1 0(argsOffset)/PUSH1 0(value)
    0x61,0x01,0x03,                             // PUSH2 0x0103 (addr=259=BLST)
    0x5a, 0xf1, 0x50,                           // GAS / CALL / POP
    0x3d, 0x60,0x00, 0x60,0x00, 0x3e,           // RETURNDATASIZE/PUSH1 0/PUSH1 0/RETURNDATACOPY
    0x3d, 0x60,0x00, 0xf3                       // RETURNDATASIZE/PUSH1 0/RETURN
};

// verifySign(bytes,bytes,bytes) returns (bool) -- real selector via
// `cast sig` (foundry), matches extension.go's blstAbiStr exactly (the
// real cgo implementation's ABI). Args are ABI "bytes" not "string", but
// the wire encoding is identical (offset+length+right-padded-data) --
// see abi_encode_bytes_args/abi_decode_bytes_args below, which (unlike
// abi_encode_string_args/abi_decode_string_args above) support
// >32-byte args since a compressed G2 signature is 96 bytes.
static const uint32_t BLST_VERIFY_SIGN_SELECTOR = 0xee57fa59;

static uint32_t abi_encode_bytes_args(uint32_t selector,
    const std::vector<std::vector<uint8_t>> &args, uint8_t *out) {
    out[0] = (uint8_t)(selector >> 24); out[1] = (uint8_t)(selector >> 16);
    out[2] = (uint8_t)(selector >> 8);  out[3] = (uint8_t)selector;
    uint32_t n = (uint32_t)args.size();
    uint32_t off = 4 + n * 32;
    uint32_t data_cursor = n * 32;
    for (uint32_t i = 0; i < n; i++) {
        uint32_t len = (uint32_t)args[i].size();
        uint32_t padded = ((len + 31) / 32) * 32;
        abi_write_u256_be(out + 4 + i * 32, data_cursor);
        abi_write_u256_be(out + off, len);
        memcpy(out + off + 32, args[i].data(), len);
        if (padded > len) memset(out + off + 32 + len, 0, padded - len);
        off += 32 + padded;
        data_cursor += 32 + padded;
    }
    return off;
}

static bool abi_decode_bytes_args(const uint8_t *args, uint32_t args_len,
    uint32_t n, std::vector<std::vector<uint8_t>> &out) {
    out.clear();
    for (uint32_t i = 0; i < n; i++) {
        if ((uint64_t)(i + 1) * 32 > args_len) return false;
        uint32_t rel_off = abi_read_u256_be(args + i * 32);
        if ((uint64_t)rel_off + 32 > args_len) return false;
        uint32_t len = abi_read_u256_be(args + rel_off);
        if ((uint64_t)rel_off + 32 + len > args_len) return false;
        out.emplace_back(args + rel_off + 32, args + rel_off + 32 + len);
    }
    return true;
}

// Real (self-verified, see /tmp scratch gen_vector.cpp -- not checked in,
// throwaway generator built against this repo's own vendored
// pkg/bls/blst/{src,build,bindings} for an x86_64 host tool) BLS12-381
// min-pubkey-size test vector: sk derived from a fixed IKM via
// blst_keygen, pk = sk_to_pk_in_g1 compressed (48 bytes), sig =
// sign_pk_in_g1 over g_blst_test_msg using the SAME DST metanode's own
// pkg/bls/bls.go uses (dstMinPk), compressed (96 bytes). Not a randomly
// invented blob -- confirmed valid via blst_core_verify_pk_in_g1 before
// being embedded here.
static const uint8_t g_blst_test_pubkey[48] = {
    0xa9,0x4b,0xe7,0x25,0xaa,0x82,0x37,0x3c,0xeb,0xc0,0x22,0x08,
    0x6b,0x9e,0xe2,0x14,0x32,0x02,0x6c,0x25,0x80,0xc1,0x7f,0x9d,
    0xa0,0x26,0x5f,0xd3,0x8c,0xf9,0xe7,0x16,0xdb,0x04,0x1b,0x2d,
    0x7e,0xd7,0x12,0x8e,0xaa,0x73,0x65,0xcc,0x88,0x86,0x96,0x3a,
};
static const uint8_t g_blst_test_sig[96] = {
    0x93,0xd0,0xdc,0xc2,0x83,0x51,0xfb,0xd6,0xf7,0x2b,0xe6,0x95,
    0x49,0xb3,0x50,0xa9,0xda,0x3f,0x80,0x8b,0x7b,0xcc,0x33,0x05,
    0xc1,0x40,0x1a,0xcc,0x2a,0xf8,0x64,0x4a,0x1f,0xf8,0xda,0x1d,
    0x90,0x1f,0xb7,0x67,0x96,0x91,0x9b,0xfe,0x60,0xf0,0xe3,0x27,
    0x07,0x9b,0x83,0x4b,0x95,0x4e,0x78,0xd8,0xe0,0x82,0xf1,0x42,
    0xbf,0x8d,0x35,0x6e,0x6d,0x04,0xb6,0x5d,0x27,0x34,0x4e,0xcb,
    0x22,0x25,0xde,0xba,0x6b,0xb1,0x34,0x3c,0xcd,0x2d,0xdd,0xbb,
    0xeb,0xb2,0xa5,0x04,0x24,0xa9,0xe9,0x79,0x32,0x51,0x69,0xad,
};
static const char g_blst_test_msg[] = "mvm_ca_test BLST real data (plan section 9.31)";
static const uint32_t g_blst_test_msg_len = 46;

// Same DST as metanode's own pkg/bls/bls.go (dstMinPk) -- the real
// verifier MUST use the identical DST the signer used, or verification
// fails even for a genuinely valid signature.
static const uint8_t g_blst_dst[] = "BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_";

// Real BLS12-381 signature verification via libblst -- mirrors
// pkg/bls/bls.go's VerifySign exactly (min-pubkey-size scheme: pk in G1,
// sig in G2; sig group-checked, pk NOT group-checked, matching Go's
// VerifyCompressed(sigGroupcheck=true, pkValidate=false) call).
static bool blst_verify_sign(const std::vector<uint8_t> &pk,
    const std::vector<uint8_t> &sig, const std::vector<uint8_t> &msg) {
    if (pk.size() != 48 || sig.size() != 96) return false;
    blst_p1_affine pk_aff;
    if (blst_p1_uncompress(&pk_aff, pk.data()) != BLST_SUCCESS) return false;
    blst_p2_affine sig_aff;
    if (blst_p2_uncompress(&sig_aff, sig.data()) != BLST_SUCCESS) return false;
    if (!blst_p2_affine_in_g2(&sig_aff)) return false; // sig group check
    BLST_ERROR r = blst_core_verify_pk_in_g1(&pk_aff, &sig_aff, true,
        msg.data(), msg.size(), g_blst_dst, sizeof(g_blst_dst) - 1, nullptr, 0);
    return r == BLST_SUCCESS;
}

// ─── EXTRACT_JSON_FIELD (2026-08-20, plan §9.31 follow-up: third of the
// 4 extension reverse-calls with real data) ───
//
// Same calldata-forwarder pattern as g_simpledb_forwarder_code/
// g_blst_forwarder_code, targeting precompile 258 (0x0102 =
// EXTRACT_JSON_FIELD_EXTENSION). A FIFTH synthetic contract address.
static const uint8_t g_json_test_addr[20] = {
    0x77,0x77,0x77,0x77,0x77,0x77,0x77,0x77,0x77,0x77,
    0x77,0x77,0x77,0x77,0x77,0x77,0x77,0x77,0x77,0x77};
static const uint8_t g_json_forwarder_code[] = {
    0x36, 0x60,0x00, 0x60,0x00, 0x37,
    0x60,0x00, 0x60,0x00,
    0x36, 0x60,0x00, 0x60,0x00,
    0x61,0x01,0x02,                             // PUSH2 0x0102 (addr=258=EXTRACT_JSON_FIELD_EXTENSION)
    0x5a, 0xf1, 0x50,
    0x3d, 0x60,0x00, 0x60,0x00, 0x3e,
    0x3d, 0x60,0x00, 0xf3
};

// extractJsonField(string,string) -- extension.go's real
// ExtensionExtractJsonField doesn't verify this selector against an ABI
// (it just does bCallData[4:], any 4-byte prefix works), so this is
// picked for realism (real `cast sig` value) rather than something the
// Host actually checks -- matches this test's own stated policy of using
// real tooling over invented values wherever one exists.
static const uint32_t EXTRACT_JSON_FIELD_SELECTOR = 0x8da776da;

// Single-value ABI "bytes"/"string" return encoder (offset+length+
// right-padded data, NO 32-byte cap unlike abi_encode_string_ret above --
// a JSON blob or extracted field can exceed one word). Matches
// argument_encode.EncodeSingleString's real byte layout exactly
// (encoder.go: start=32, length, then the raw bytes).
static uint32_t abi_encode_bytes_ret(const std::vector<uint8_t> &data, uint8_t *out) {
    uint32_t len = (uint32_t)data.size();
    uint32_t padded = ((len + 31) / 32) * 32;
    abi_write_u256_be(out, 32);
    abi_write_u256_be(out + 32, len);
    memcpy(out + 64, data.data(), len);
    if (padded > len) memset(out + 64 + len, 0, padded - len);
    return 64 + padded;
}

// Minimal, scope-limited JSON field extractor -- parses a FLAT top-level
// JSON object ({"key":"value","key2":123,"key3":true,...}, no nesting,
// no unicode escapes) and returns the named field's value formatted the
// same way extension.go's real ExtractJsonField does for a scalar value:
// string -> raw (unquoted) text; bool -> "1"/"0" (that function's own
// explicit true/false remap); number/null -> its literal JSON text
// (matches Go's fmt.Sprintf("%v", ...) for a JSON-unmarshaled number,
// which prints an integer-valued float64 without a decimal point). NOT a
// general JSON parser -- this test only ever feeds it well-formed flat
// JSON, so it doesn't replicate extension.go's array-JSON/nested-object
// fallback branches.
static bool json_extract_flat_field(const std::string &json,
    const std::string &field, std::string &out) {
    size_t i = json.find('{');
    if (i == std::string::npos) return false;
    i++;
    size_t n = json.size();
    while (i < n) {
        while (i < n && (json[i] == ' ' || json[i] == '\n' || json[i] == '\t' || json[i] == ',')) i++;
        if (i >= n || json[i] == '}') break;
        if (json[i] != '"') return false;
        i++;
        size_t key_start = i;
        while (i < n && json[i] != '"') i++;
        if (i >= n) return false;
        std::string key = json.substr(key_start, i - key_start);
        i++; // closing quote
        while (i < n && (json[i] == ' ' || json[i] == ':')) i++;
        bool is_string_value = false;
        std::string value_str;
        if (i < n && json[i] == '"') {
            is_string_value = true;
            i++;
            size_t val_start = i;
            while (i < n && json[i] != '"') i++; // no escape handling
            if (i >= n) return false;
            value_str = json.substr(val_start, i - val_start);
            i++; // closing quote
        } else {
            size_t val_start = i;
            while (i < n && json[i] != ',' && json[i] != '}') i++;
            value_str = json.substr(val_start, i - val_start);
            while (!value_str.empty() && (value_str.back() == ' ' || value_str.back() == '\n')) value_str.pop_back();
        }
        if (key == field) {
            if (is_string_value) out = value_str;
            else if (value_str == "true") out = "1";
            else if (value_str == "false") out = "0";
            else out = value_str;
            return true;
        }
    }
    return false;
}

static void handle_reverse_call(void) {
    uint32_t header_len = g_channel->header_len;
    uint32_t blob_len = g_channel->blob_len;
    static uint8_t hdr_copy[512];
    static uint8_t blob_copy[MVM_TZ_BLOB_REGION_SIZE];
    memcpy(hdr_copy, g_channel->blob_region, header_len);
    memcpy(blob_copy, g_channel->blob_region + header_len, blob_len);
    mvm_tz_cmd_t cmd = g_channel->cmd;

    printf("[mvm_ca_test] reverse call cmd=%d header_len=%u blob_len=%u\n", cmd, header_len, blob_len);
    fflush(stdout);

    uint8_t resp_buf[512];
    uint32_t resp_hdr_len = 0, resp_blob_len = 0;

    switch (cmd) {
    case MVM_TZ_RCMD_GLOBAL_STATE_GET: {
        // mvm_tz_global_state_get_req_t: [0] mvm_id(20) [1] address(20).
        const uint8_t *req_address = hdr_copy + 20;
        const uint8_t *code = nullptr;
        size_t code_len = 0;
        if (header_len >= 40 && memcmp(req_address, g_contract_addr, 20) == 0) {
            code = g_contract_code;
            code_len = sizeof(g_contract_code);
        } else if (header_len >= 40 && memcmp(req_address, g_storage_test_addr, 20) == 0) {
            code = g_storage_read_code;
            code_len = sizeof(g_storage_read_code);
        } else if (header_len >= 40 && memcmp(req_address, g_simpledb_test_addr, 20) == 0) {
            code = g_simpledb_forwarder_code;
            code_len = sizeof(g_simpledb_forwarder_code);
        } else if (header_len >= 40 && memcmp(req_address, g_blst_test_addr, 20) == 0) {
            code = g_blst_forwarder_code;
            code_len = sizeof(g_blst_forwarder_code);
        } else if (header_len >= 40 && memcmp(req_address, g_json_test_addr, 20) == 0) {
            code = g_json_forwarder_code;
            code_len = sizeof(g_json_forwarder_code);
        }
        if (code) {
            // Blob shape (status==1 only): [0] balance(32) [1] nonce(32)
            // [2] code (length-prefixed).
            mvm_tz_global_state_get_resp_t resp = {0};
            resp.status = 1;
            memcpy(resp_buf, &resp, sizeof(resp));
            resp_hdr_len = sizeof(resp);
            BlobWriter w{resp_buf + resp_hdr_len};
            uint8_t zero32[32] = {0};
            w.writeRaw(zero32, 32); // balance = 0
            w.writeRaw(zero32, 32); // nonce = 0
            w.writeBytes(code, code_len);
            resp_blob_len = w.off;
            printf("[mvm_ca_test] GlobalStateGet: returning REAL code (%zu bytes)\n", code_len);
            fflush(stdout);
        } else {
            // Every other address in this test is synthetic/never-before-seen
            // -> status=0 ("not found, create fresh") is the correct answer.
            mvm_tz_global_state_get_resp_t resp = {0};
            resp.status = 0;
            memcpy(resp_buf, &resp, sizeof(resp));
            resp_hdr_len = sizeof(resp);
            resp_blob_len = 0; // status==0 -> no blob per protocol header
        }
        break;
    }
    case MVM_TZ_RCMD_GET_STORAGE_VALUE: {
        // mvm_tz_get_storage_value_req_t: [0] mvm_id(20) [1] address(20)
        // [2] key(32) -- 72 bytes total.
        const uint8_t *req_address = hdr_copy + 20;
        const uint8_t *req_key = hdr_copy + 40;
        static const uint8_t zero_key[32] = {0};
        if (header_len >= 72 && memcmp(req_address, g_storage_test_addr, 20) == 0
            && memcmp(req_key, zero_key, 32) == 0) {
            // 2026-08-20 (plan §9.28 follow-up): the real-data case -- a
            // slot this tx never wrote, answered with a genuine
            // pre-existing value instead of NOT_FOUND. See
            // g_storage_test_value's own comment for why 0x1337.
            mvm_tz_get_storage_value_resp_t resp = {0};
            resp.status = 0; // STORAGE_SUCCESS
            memcpy(resp_buf, &resp, sizeof(resp));
            resp_hdr_len = sizeof(resp);
            memcpy(resp_buf + resp_hdr_len, g_storage_test_value, 32);
            resp_blob_len = 32;
            printf("[mvm_ca_test] GetStorageValue: returning REAL value 0x1337 "
                "for storage-test address\n");
            fflush(stdout);
        } else {
            // Every other (address, key) in this test is fresh (first-ever
            // write, e.g. g_contract_addr's own SSTORE-then-SLOAD test) ->
            // NOT_FOUND=1 is the correct answer; the SSTORE itself updates
            // mvm_ta's own in-process State/Xapian, no reverse call needed
            // for the write side.
            mvm_tz_get_storage_value_resp_t resp = {0};
            resp.status = 1;
            memcpy(resp_buf, &resp, sizeof(resp));
            resp_hdr_len = sizeof(resp);
            resp_blob_len = 0;
        }
        break;
    }
    // Added 2026-08-20 (plan §9.24 follow-up): all 4 remaining live reverse
    // commands, so a future test exercising contract code (not just a pure
    // EOA->EOA native transfer) doesn't hit the FATAL default case. None of
    // these are exercised by THIS test's own transaction (no code at either
    // synthetic address), so this returns the documented "no result"
    // shape -- a real, correctly-formed response, just an empty one --
    // for now, rather than any speculative fabricated data (matches this
    // function's own stated policy: fail loud/return "not found" over
    // guessing). Filling in a REAL HTTP client here is future work, left
    // for last on purpose (BLST/EXTRACT_JSON_FIELD/GET_OR_CREATE_SIMPLE_DB
    // all got real data first, 2026-08-20, plan §9.31 -- see their own
    // cases below/above).
    case MVM_TZ_RCMD_EXTENSION_CALL_GET_API: {
        // No fixed header (mvm_tz_protocol.h line ~512) -- response is
        // just the raw output bytes verbatim, length via the channel's
        // own blob_len field (NOT length-prefixed -- confirmed 2026-08-20,
        // plan §9.31, by reading mvm_reverse_round_trip's actual response
        // handling in mvm_ta_main.cpp, not the older doc comment here
        // which wrongly implied a prefix). The true
        // Extension_return{nullptr,0} failure case is resp_blob_len=0,
        // full stop -- a previous version of this stub wrote a 4-byte
        // all-zero blob instead (harmless only because nothing exercised
        // this cmd yet).
        resp_hdr_len = 0;
        resp_blob_len = 0;
        break;
    }
    case MVM_TZ_RCMD_EXTENSION_EXTRACT_JSON_FIELD: {
        // 2026-08-20 (plan §9.31 follow-up): real data, third of the 4
        // extension reverse-calls (see g_json_test_addr/
        // json_extract_flat_field's own comments above). Blob = raw ABI
        // calldata for extractJsonField(string,string) -- same shape
        // extension.go's real ExtensionExtractJsonField decodes via
        // argument_encode.DecodeStringInput(bCallData[4:], idx) (NOT
        // go-ethereum's abi package for this one, but the wire encoding
        // is identical standard ABI string encoding either way).
        resp_hdr_len = 0;
        resp_blob_len = 0; // default: Extension_return{nullptr,0}
        if (blob_len >= 4) {
            const uint8_t *args = blob_copy + 4;
            uint32_t args_len = blob_len - 4;
            std::vector<std::vector<uint8_t>> byte_args;
            // extension.go's real handler never checks the selector
            // (just skips 4 bytes) -- match that here rather than
            // gatekeeping on EXTRACT_JSON_FIELD_SELECTOR, so this stays
            // a faithful stand-in for the real Host.
            if (abi_decode_bytes_args(args, args_len, 2, byte_args)) {
                std::string json((const char*)byte_args[0].data(), byte_args[0].size());
                std::string field((const char*)byte_args[1].data(), byte_args[1].size());
                std::string value;
                if (json_extract_flat_field(json, field, value)) {
                    resp_blob_len = abi_encode_bytes_ret(
                        std::vector<uint8_t>(value.begin(), value.end()), resp_buf);
                    printf("[mvm_ca_test] ExtractJsonField: json=%s field=%s -> \"%s\"\n",
                        json.c_str(), field.c_str(), value.c_str());
                } else {
                    printf("[mvm_ca_test] ExtractJsonField: field \"%s\" not found in json=%s "
                        "-- returning empty (Extension_return{nullptr,0})\n",
                        field.c_str(), json.c_str());
                }
                fflush(stdout);
            } else {
                printf("[mvm_ca_test] ExtractJsonField: malformed calldata (blob_len=%u) "
                    "-- returning empty (Extension_return{nullptr,0})\n", blob_len);
                fflush(stdout);
            }
        }
        break;
    }
    case MVM_TZ_RCMD_EXTENSION_BLST: {
        // 2026-08-20 (plan §9.31 follow-up): real data, second of the 4
        // extension reverse-calls (see g_blst_test_addr/blst_verify_sign's
        // own comments above). Same blob-only shape as CALL_GET_API/
        // EXTRACT_JSON_FIELD (no fixed header) -- blob = raw ABI calldata
        // for verifySign(bytes,bytes,bytes), the exact ABI extension.go's
        // real ExtensionBlst decodes via blstAbi.MethodById/
        // Inputs.UnpackIntoMap.
        resp_hdr_len = 0;
        resp_blob_len = 0; // default: Extension_return{nullptr,0}
        if (blob_len >= 4) {
            uint32_t selector = ((uint32_t)blob_copy[0] << 24) | ((uint32_t)blob_copy[1] << 16)
                | ((uint32_t)blob_copy[2] << 8) | (uint32_t)blob_copy[3];
            const uint8_t *args = blob_copy + 4;
            uint32_t args_len = blob_len - 4;
            std::vector<std::vector<uint8_t>> byte_args;
            if (selector == BLST_VERIFY_SIGN_SELECTOR
                    && abi_decode_bytes_args(args, args_len, 3, byte_args)) {
                bool ok = blst_verify_sign(byte_args[0], byte_args[1], byte_args[2]);
                resp_blob_len = abi_encode_bool(ok, resp_buf);
                printf("[mvm_ca_test] BLST verifySign: pk=%zuB sig=%zuB msg=%zuB -> %s\n",
                    byte_args[0].size(), byte_args[1].size(), byte_args[2].size(),
                    ok ? "VALID" : "invalid");
                fflush(stdout);
            } else {
                printf("[mvm_ca_test] BLST: unrecognized selector=0x%08x or malformed "
                    "args (blob_len=%u) -- returning empty (Extension_return{nullptr,0})\n",
                    selector, blob_len);
                fflush(stdout);
            }
        }
        break;
    }
    case MVM_TZ_RCMD_EXTENSION_GET_OR_CREATE_SIMPLE_DB: {
        // 2026-08-20 (plan §9.31): real data, first of the 4 extension
        // reverse-calls to get it (see g_fake_simpledb/g_simpledb_test_addr's
        // own comments above). Request header =
        // mvm_tz_get_or_create_simple_db_req_t{address,mvm_id} (unused --
        // this test has only one caller/mvm_id); blob = raw ABI calldata
        // (selector + args), the exact shape extension.go's
        // ExtensionGetOrCreateSimpleDb (the real cgo implementation)
        // decodes via go-ethereum's abi.MethodById/Inputs.Unpack. Response
        // blob is the raw ABI-encoded return value verbatim (no extra
        // length prefix -- resp_blob_len via the channel's own blob_len
        // field IS the length, see mvm_reverse_round_trip's response
        // handling) -- matches convertToCode()'s
        // Extension_return{data_p,data_size} -> mvm::Code copy exactly.
        resp_hdr_len = 0;
        resp_blob_len = 0; // default: Extension_return{nullptr,0} ("no result")
        if (blob_len >= 4) {
            uint32_t selector = ((uint32_t)blob_copy[0] << 24) | ((uint32_t)blob_copy[1] << 16)
                | ((uint32_t)blob_copy[2] << 8) | (uint32_t)blob_copy[3];
            const uint8_t *args = blob_copy + 4;
            uint32_t args_len = blob_len - 4;
            std::vector<std::string> strs;
            if (selector == SIMPLEDB_SET_SELECTOR && abi_decode_string_args(args, args_len, 3, strs)) {
                const std::string &db = strs[0], &key = strs[1], &value = strs[2];
                g_fake_simpledb[db + '\0' + key] = value;
                resp_blob_len = abi_encode_bool(true, resp_buf);
                printf("[mvm_ca_test] SimpleDb SET db=%s key=%s value=%s -> stored\n",
                    db.c_str(), key.c_str(), value.c_str());
                fflush(stdout);
            } else if (selector == SIMPLEDB_GET_SELECTOR && abi_decode_string_args(args, args_len, 2, strs)) {
                const std::string &db = strs[0], &key = strs[1];
                auto it = g_fake_simpledb.find(db + '\0' + key);
                std::string value = (it != g_fake_simpledb.end()) ? it->second : std::string();
                resp_blob_len = abi_encode_string_ret(value, resp_buf);
                printf("[mvm_ca_test] SimpleDb GET db=%s key=%s -> \"%s\" (%s)\n",
                    db.c_str(), key.c_str(), value.c_str(),
                    it != g_fake_simpledb.end() ? "found" : "NOT FOUND");
                fflush(stdout);
            } else {
                printf("[mvm_ca_test] SimpleDb: unrecognized selector=0x%08x or malformed "
                    "args (blob_len=%u) -- returning empty (Extension_return{nullptr,0})\n",
                    selector, blob_len);
                fflush(stdout);
            }
        }
        break;
    }
    case MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS: {
        // Added 2026-08-20 (plan §9.28) alongside mvm_ta_main.cpp's
        // mvm_fetch_and_replay_full_db_logs() -- not yet auto-triggered by
        // any interpreter code path (see that function's own doc comment),
        // so this test never actually sends this cmd today; handled here
        // anyway so a future test that DOES trigger it fails loud with a
        // real (if empty) response instead of hitting the FATAL default.
        // Response reuses mvm_tz_replay_full_db_logs_req_t's shape: a real
        // header (entry_count), unlike the blob-only extension cases above.
        mvm_tz_replay_full_db_logs_req_t resp = {0};
        resp.entry_count = 0; // "nothing to replay" -- valid, not a failure
        memcpy(resp_buf, &resp, sizeof(resp));
        resp_hdr_len = sizeof(resp);
        resp_blob_len = 0;
        break;
    }
    default:
        printf("[mvm_ca_test] FATAL: unhandled reverse cmd=%d -- aborting cleanly "
               "instead of hanging mvm_ta forever\n", cmd);
        fflush(stdout);
        exit(1);
    }

    mvm_tz_spinlock_lock(&g_channel->lock);
    memcpy(g_channel->blob_region, resp_buf, resp_hdr_len);
    if (resp_blob_len) memcpy(g_channel->blob_region + resp_hdr_len, resp_buf + resp_hdr_len, resp_blob_len);
    g_channel->header_len = resp_hdr_len;
    g_channel->blob_len = resp_blob_len;
    g_channel->seq++;
    mvm_tz_spinlock_unlock(&g_channel->lock);

    mvm_tz_flag_set(&g_channel->response_ready, 1);
}


// ─── send one MVM_TZ_CMD_EXECUTE, service reverse calls, decode+print
// the result. Factored out (2026-08-20) so main() can run it twice: once
// for the original EOA->EOA native transfer, once for a real contract
// call (see g_contract_addr/g_contract_code above) -- same protocol
// mechanics either way, just a different recipient/amount/input. Returns
// 0 on success, 1 on any failure (mirrors main()'s own prior return
// convention exactly, just reusable now). ───
static int run_execute_and_print(const char *label,
    const uint8_t sender[20], const uint8_t recipient[20],
    uint64_t amount, const uint8_t *input, uint32_t input_len,
    bool is_cache = false) {
    uint8_t tx_hash[32];
    memset(tx_hash, 0xAB, 32);

    mvm_tz_execute_req_t req = {0};
    req.amount[24] = (uint8_t)(amount >> 56); req.amount[25] = (uint8_t)(amount >> 48);
    req.amount[26] = (uint8_t)(amount >> 40); req.amount[27] = (uint8_t)(amount >> 32);
    req.amount[28] = (uint8_t)(amount >> 24); req.amount[29] = (uint8_t)(amount >> 16);
    req.amount[30] = (uint8_t)(amount >> 8);  req.amount[31] = (uint8_t)(amount);
    req.gas_price = 1;
    req.gas_limit = 200000; // headroom for the contract-call case's SSTORE/SLOAD (vs. 21000 for a plain transfer)
    req.block_prevrandao = 0;
    req.block_gas_limit = 30000000;
    req.block_time = (uint64_t)time(nullptr);
    req.block_base_fee = 1;
    req.block_number = 1;
    memset(req.block_coinbase, 0, 20);
    memset(req.mvm_id, 0, 20);
    req.is_debug = 1;
    // 2026-08-20 (plan §9.31): SIMPLE_DATABASE_ADDRESS's write-protection
    // check (processor.cpp: `ctxt->read_only || !gs.is_cache()`) throws
    // for ANY call to that precompile -- get() included, not just set() --
    // unless is_cache. Existing tests 1-3 never touch that precompile, so
    // this new default-false param changes nothing for them.
    req.is_cache = is_cache ? 1 : 0;
    req.related_addresses_count = 2;

    static uint8_t blob_scratch[4096];
    BlobWriter w{blob_scratch};
    w.writeBytes(sender, 20);
    w.writeBytes(recipient, 20);
    w.writeBytes(input, input_len);
    w.writeBytes(tx_hash, 32);
    w.writeBytes(sender, 20);    // relatedAddresses[0]
    w.writeBytes(recipient, 20); // relatedAddresses[1]

    printf("[mvm_ca_test] sending MVM_TZ_CMD_EXECUTE (%s)\n", label);
    fflush(stdout);

    mvm_tz_spinlock_lock(&g_channel->lock);
    memcpy(g_channel->blob_region, &req, sizeof(req));
    memcpy(g_channel->blob_region + sizeof(req), blob_scratch, w.off);
    g_channel->cmd = MVM_TZ_CMD_EXECUTE;
    g_channel->direction = MVM_TZ_DIR_HOST_TO_TA;
    g_channel->header_len = sizeof(req);
    g_channel->blob_len = w.off;
    g_channel->seq++;
    mvm_tz_spinlock_unlock(&g_channel->lock);

    mvm_tz_flag_set(&g_channel->request_ready, 1);

    const int TIMEOUT_S = 60;
    for (int round = 0; ; round++) {
        time_t start = time(nullptr);
        uint64_t last_seq = (uint64_t)-1;
        int which = -1;
        while (which < 0) {
            if (__atomic_load_n(&g_channel->response_ready, __ATOMIC_ACQUIRE) == 1
                && g_channel->direction == MVM_TZ_DIR_HOST_TO_TA) {
                uint8_t exp = 1;
                if (__atomic_compare_exchange_n(&g_channel->response_ready, &exp, 0, 0, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) {
                    which = 0;
                    break;
                }
            }
            if (__atomic_load_n(&g_channel->request_ready, __ATOMIC_ACQUIRE) == 1
                && g_channel->direction == MVM_TZ_DIR_TA_TO_HOST) {
                uint8_t exp = 1;
                if (__atomic_compare_exchange_n(&g_channel->request_ready, &exp, 0, 0, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) {
                    which = 1;
                    break;
                }
            }
            uint64_t seq = g_channel->seq;
            if (seq != last_seq) {
                printf("[mvm_ca_test] waiting (round=%d)... seq=%llu\n", round, (unsigned long long)seq);
                fflush(stdout);
                last_seq = seq;
            }
            if (time(nullptr) - start > TIMEOUT_S) {
                printf("[mvm_ca_test] TIMEOUT after %ds (round=%d, last seq=%llu) -- "
                       "genuinely stuck, not just slow (CLAUDE.md: don't assume otherwise)\n",
                       TIMEOUT_S, round, (unsigned long long)last_seq);
                return 1;
            }
            usleep(10000);
        }
        if (which == 0) {
            if (g_channel->direction == MVM_TZ_DIR_HOST_TO_TA) {
                printf("[mvm_ca_test] got final response (round=%d)\n", round);
                break;
            } else {
                printf("[mvm_ca_test] FATAL: response_ready set but direction=TA_TO_HOST "
                       "-- protocol confusion, aborting\n");
                return 1;
            }
        }
        handle_reverse_call();
    }

    mvm_tz_execute_result_hdr_t hdr;
    memcpy(&hdr, g_channel->blob_region, sizeof(hdr));
    printf("\n=== ExecuteResult (%s) ===\n", label);
    printf("status=%u exception=%u gas_used=%llu\n", hdr.status, hdr.exception, (unsigned long long)hdr.gas_used);
    printf("add_balance_change_count=%u sub_balance_change_count=%u nonce_change_count=%u\n",
        hdr.add_balance_change_count, hdr.sub_balance_change_count, hdr.nonce_change_count);
    printf("code_change_count=%u storage_change_count=%u storage_read_count=%u full_db_hash_count=%u\n",
        hdr.code_change_count, hdr.storage_change_count, hdr.storage_read_count, hdr.full_db_hash_count);

    const uint8_t *blob = g_channel->blob_region + sizeof(hdr);
    uint32_t off = 0;
    auto readU32 = [&](void) { uint32_t v; memcpy(&v, blob + off, 4); off += 4; return v; };
    auto skipBytes = [&](void) { uint32_t n = readU32(); off += n; return n; };

    uint32_t output_len = 0;
    const uint8_t *output_p = nullptr;
    {
        skipBytes(); // exmsg
        output_len = readU32();
        output_p = blob + off;
        off += output_len;
    }
    for (uint32_t i = 0; i < hdr.full_db_hash_count; i++) off += 52;

    printf("\n-- add_balance_change --\n");
    for (uint32_t i = 0; i < hdr.add_balance_change_count; i++) {
        printf("  addr=");
        for (int j = 0; j < 20; j++) printf("%02x", blob[off + j]);
        printf(" value=");
        for (int j = 0; j < 32; j++) printf("%02x", blob[off + 20 + j]);
        printf("\n");
        off += 52;
    }
    printf("-- nonce_change --\n");
    for (uint32_t i = 0; i < hdr.nonce_change_count; i++) {
        printf("  addr=");
        for (int j = 0; j < 20; j++) printf("%02x", blob[off + j]);
        printf(" value=");
        for (int j = 0; j < 32; j++) printf("%02x", blob[off + 20 + j]);
        printf("\n");
        off += 52;
    }
    printf("-- sub_balance_change --\n");
    for (uint32_t i = 0; i < hdr.sub_balance_change_count; i++) {
        printf("  addr=");
        for (int j = 0; j < 20; j++) printf("%02x", blob[off + j]);
        printf(" value=");
        for (int j = 0; j < 32; j++) printf("%02x", blob[off + 20 + j]);
        printf("\n");
        off += 52;
    }
    printf("-- storage_change --\n");
    for (uint32_t i = 0; i < hdr.storage_change_count; i++) {
        printf("  addr=");
        for (int j = 0; j < 20; j++) printf("%02x", blob[off + j]);
        off += 20;
        uint32_t pair_count = readU32();
        printf(" pair_count=%u\n", pair_count);
        for (uint32_t p = 0; p < pair_count; p++) {
            printf("    key=");
            for (int j = 0; j < 32; j++) printf("%02x", blob[off + j]);
            printf(" value=");
            for (int j = 0; j < 32; j++) printf("%02x", blob[off + 32 + j]);
            printf("\n");
            off += 64;
        }
    }
    printf("-- output (RETURN data, %u bytes) --\n", output_len);
    if (output_len > 0) {
        printf("  ");
        for (uint32_t j = 0; j < output_len; j++) printf("%02x", output_p[j]);
        printf("\n");
    }
    printf("(status=%u, exception=%u -- %s)\n", hdr.status, hdr.exception,
        hdr.exception ? "reverted/exception" : "success");
    return 0;
}

int main(void) {
    printf("[mvm_ca_test] opening %s\n", DEVICE_NAME);
    int fd = open(DEVICE_NAME, O_RDWR);
    if (fd < 0) { perror("open"); return 1; }

    // Start the relay thread FIRST, before anything else: mvm_ta has
    // been sitting parked inside its own push_pages() SMC call since
    // chanmgr launched it near boot (minutes before this program ever
    // runs) -- that request only completes once this ioctl loop is
    // actually running. It costs nothing to start early (idle-backoff
    // keeps it cheap once there's nothing to service).
    pthread_t relay_tid;
    if (pthread_create(&relay_tid, nullptr, ca_relay_thread, &fd) != 0) {
        perror("pthread_create(ca_relay_thread)");
        return 1;
    }
    // Give push_pages a moment to actually land before SET_PAGES checks
    // for it -- SET_PAGES itself doesn't depend on push having landed
    // (it just seeds file->private_data), but this makes the intent
    // (and any early failure) visible in the log in the right order.
    usleep(200 * 1000);

    struct llm_client_op_pages set_req = {0};
    set_req.cma_index = MVM_TZASC_CMA_INDEX;
    set_req.entry_index = MVM_TZASC_ENTRY_INDEX;
    set_req.size = 0; // not used by SET_PAGES, only by PUSH_PAGES
    set_req.offset = 0;
    if (ioctl(fd, LLM_CLIENT_IOCTL_SET_PAGES, &set_req) != 0) {
        perror("ioctl SET_PAGES");
        return 1;
    }
    printf("[mvm_ca_test] SET_PAGES OK (cma_index=%d entry_index=%d)\n",
        MVM_TZASC_CMA_INDEX, MVM_TZASC_ENTRY_INDEX);

    unsigned long channel_size = (sizeof(mvm_tz_channel_t) + 0xfffUL) & ~0xfffUL;
    printf("[mvm_ca_test] mmap size=%#lx\n", channel_size);
    void *addr = mmap(NULL, channel_size, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
    if (addr == MAP_FAILED) { perror("mmap"); return 1; }
    g_channel = (mvm_tz_channel_t *)addr;

    printf("[mvm_ca_test] mapped OK. protocol_version=%u (want %u)\n",
        g_channel->protocol_version, MVM_TZ_PROTOCOL_VERSION);
    if (g_channel->protocol_version != MVM_TZ_PROTOCOL_VERSION) {
        printf("[mvm_ca_test] FATAL: protocol_version mismatch -- mvm_ta hasn't "
               "initialized this page (wrong entry_index?) or a real bug. Aborting, "
               "not guessing.\n");
        return 1;
    }

    // ─── test 1: trivial EOA->EOA native transfer (already proven this
    // session) ───
    uint8_t sender[20], recipient[20];
    memset(sender, 0x11, 20);
    memset(recipient, 0x22, 20);
    if (run_execute_and_print("native transfer", sender, recipient, 100, nullptr, 0) != 0) {
        return 1;
    }

    // ─── test 2 (2026-08-20, plan §9.24 follow-up): real contract call.
    // recipient = g_contract_addr, whose GlobalStateGet response
    // (handle_reverse_call() above) is special-cased to return real
    // SSTORE/SLOAD/RETURN bytecode -- see that code's own comment for why
    // this doesn't need MVM_TZ_CMD_DEPLOY. amount=0, empty calldata (the
    // bytecode itself takes no branch on input). ───
    if (run_execute_and_print("contract call (SSTORE/SLOAD)", sender, g_contract_addr, 0, nullptr, 0) != 0) {
        return 1;
    }

    // ─── test 3 (2026-08-20, plan §9.28 follow-up): storage reverse-call
    // with REAL pre-existing data. recipient = g_storage_test_addr, whose
    // bytecode is a bare SLOAD (no prior SSTORE, this tx or any other) --
    // exercises GET_STORAGE_VALUE's reverse-call round trip returning a
    // genuine value (0x1337) instead of always NOT_FOUND, unlike test 2's
    // SSTORE-then-SLOAD (which never leaves mvm_ta's own in-process cache).
    // Expect the RETURNed output = 0x1337 -- confirmed on hardware
    // 2026-08-20 (storage_read_count itself came back 0, not 1 as first
    // guessed here; add_addresses_storage_read()'s tracking is Block-STM
    // -specific bookkeeping, separate from whether the value round-tripped
    // correctly, which the RETURN output is the real check for). ───
    if (run_execute_and_print("storage read (real pre-existing value)", sender, g_storage_test_addr, 0, nullptr, 0) != 0) {
        return 1;
    }

    // ─── test 4/5 (2026-08-20, plan §9.31): first of the 4 extension
    // reverse-calls exercised with REAL data --
    // MVM_TZ_RCMD_EXTENSION_GET_OR_CREATE_SIMPLE_DB via
    // g_simpledb_test_addr's calldata-forwarder bytecode (see that
    // constant's own comment) calling SIMPLE_DATABASE_ADDRESS (261)'s
    // set(...) then get(...). Expect test 5's RETURN output to ABI-decode
    // to "hello_ta" -- the exact value test 4 wrote via a REAL round trip
    // through handle_reverse_call()'s new SimpleDb SET/GET branches (not
    // mvm_ta's own in-process cache -- this precompile has no such cache,
    // every call is a fresh reverse call to the Host). ───
    {
        static uint8_t calldata[512];
        uint32_t len = abi_encode_string_args(SIMPLEDB_SET_SELECTOR,
            {"db1", "key1", "hello_ta"}, calldata);
        if (run_execute_and_print("simpledb set", sender, g_simpledb_test_addr,
                0, calldata, len, /*is_cache=*/true) != 0) {
            return 1;
        }
    }
    {
        static uint8_t calldata[512];
        uint32_t len = abi_encode_string_args(SIMPLEDB_GET_SELECTOR,
            {"db1", "key1"}, calldata);
        if (run_execute_and_print("simpledb get", sender, g_simpledb_test_addr,
                0, calldata, len, /*is_cache=*/true) != 0) {
            return 1;
        }
    }

    // ─── test 6 (2026-08-20, plan §9.31 follow-up): BLST extension
    // reverse-call with REAL data -- g_blst_test_addr's forwarder calls
    // verifySign(pk, sig, msg) with a genuine, self-verified BLS12-381
    // signature (see g_blst_test_pubkey/g_blst_test_sig's own comment).
    // Expect RETURN = ABI bool(true) (32 bytes, last byte 0x01). ───
    {
        static uint8_t calldata[512];
        std::vector<std::vector<uint8_t>> args = {
            std::vector<uint8_t>(g_blst_test_pubkey, g_blst_test_pubkey + 48),
            std::vector<uint8_t>(g_blst_test_sig, g_blst_test_sig + 96),
            std::vector<uint8_t>(g_blst_test_msg, g_blst_test_msg + g_blst_test_msg_len),
        };
        uint32_t len = abi_encode_bytes_args(BLST_VERIFY_SIGN_SELECTOR, args, calldata);
        if (run_execute_and_print("blst verifySign", sender, g_blst_test_addr,
                0, calldata, len) != 0) {
            return 1;
        }
    }

    // ─── test 7 (2026-08-20, plan §9.31 follow-up): EXTRACT_JSON_FIELD
    // extension reverse-call with REAL data -- g_json_test_addr's
    // forwarder calls extractJsonField(json, "value") against a flat
    // JSON object, expecting the numeric field's literal text back.
    // Expect RETURN to ABI-decode to "123" (json_extract_flat_field's own
    // number-formatting rule: literal JSON text, no quotes). ───
    {
        static uint8_t calldata[512];
        const std::string json_str = "{\"status\":\"ok\",\"value\":123,\"flag\":true}";
        std::vector<std::vector<uint8_t>> args = {
            std::vector<uint8_t>(json_str.begin(), json_str.end()),
            std::vector<uint8_t>({'v','a','l','u','e'}),
        };
        uint32_t len = abi_encode_bytes_args(EXTRACT_JSON_FIELD_SELECTOR, args, calldata);
        if (run_execute_and_print("extract json field", sender, g_json_test_addr,
                0, calldata, len) != 0) {
            return 1;
        }
    }

    printf("\n[mvm_ca_test] DONE\n");
    g_relay_stop = true;
    pthread_join(relay_tid, nullptr);
    return 0;
}
