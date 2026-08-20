// Host (x86, glibc) repro harness for the mvm_ta contract-call NULL ptr
// crash. Calls the SAME execute() entry point mvm_ta_main.cpp's
// mvm_dispatch_execute() calls, with the SAME parameters mvm_ca_test.cpp's
// "contract call (SSTORE/SLOAD)" test sent, and the SAME GlobalStateGet/
// GetStorageValue callback behavior -- entirely in-process, no TA/hardware,
// so any crash lands directly in gdb with a full backtrace + line numbers
// (this build already carries -g by default per linker/CMakeLists.txt's
// Release flags).
#include <cstdio>
#include <cstring>
#include <cstdlib>
#include <cstdint>
#include <ctime>
#include "mvm_linker.hpp"

// Mirrors mvm_ta_main.cpp's own local definition exactly (mirrors
// linker/src/my_global_state.cpp's local definition). Defined locally
// since mvm_linker.hpp only exposes these under -DMVM_LINKER_BUILD.
struct GlobalStateGet_return {
  int status;
  unsigned char *balance_p;
  unsigned char *nonce;
  unsigned char *code_p;
  int code_length;
};

struct GetStorageValue_return {
  unsigned char *value;
  int status;
};

static const uint8_t g_contract_addr[20] = {
    0x33,0x33,0x33,0x33,0x33,0x33,0x33,0x33,0x33,0x33,
    0x33,0x33,0x33,0x33,0x33,0x33,0x33,0x33,0x33,0x33};
// PUSH1 0x2a PUSH1 0x00 SSTORE PUSH1 0x00 SLOAD PUSH1 0x00 MSTORE
// PUSH1 0x20 PUSH1 0x00 RETURN
static const uint8_t g_contract_code[] = {
    0x60,0x2a, 0x60,0x00, 0x55, 0x60,0x00, 0x54,
    0x60,0x00, 0x52, 0x60,0x20, 0x60,0x00, 0xf3};

extern "C" {

GlobalStateGet_return GlobalStateGet(unsigned char *mvmId, unsigned char *address) {
    (void)mvmId;
    GlobalStateGet_return ret = {0};
    if (memcmp(address, g_contract_addr, 20) == 0) {
        ret.status = 1;
        ret.balance_p = (unsigned char *)calloc(1, 32);
        ret.nonce = (unsigned char *)calloc(1, 32);
        ret.code_length = (int)sizeof(g_contract_code);
        ret.code_p = (unsigned char *)malloc(sizeof(g_contract_code));
        memcpy(ret.code_p, g_contract_code, sizeof(g_contract_code));
        printf("[host_repro] GlobalStateGet: returning REAL code (%d bytes)\n", ret.code_length);
    } else {
        ret.status = 0;
    }
    return ret;
}

GetStorageValue_return GetStorageValue(unsigned char *mvmId, unsigned char *address, unsigned char *key) {
    (void)mvmId; (void)address; (void)key;
    GetStorageValue_return ret = {0};
    ret.status = 1; // NOT_FOUND -- every slot is fresh, mirrors mvm_ca_test.cpp
    return ret;
}

void ClearProcessingPointers(unsigned char *) {}

Extension_return ExtensionCallGetApi(unsigned char *, int) { return {nullptr, 0}; }
Extension_return ExtensionExtractJsonField(unsigned char *, int) { return {nullptr, 0}; }
Extension_return ExtensionBlst(unsigned char *, int) { return {nullptr, 0}; }
Extension_return ExtensionGetOrCreateSimpleDb(unsigned char *, int, unsigned char *, unsigned char *) {
    return {nullptr, 0};
}

} // extern "C"

static ExecuteResult *do_execute(const char *label,
    const uint8_t sender[20], const uint8_t recipient[20], uint64_t amount_u64) {
    uint8_t amount[32] = {0};
    amount[24] = (uint8_t)(amount_u64 >> 56); amount[25] = (uint8_t)(amount_u64 >> 48);
    amount[26] = (uint8_t)(amount_u64 >> 40); amount[27] = (uint8_t)(amount_u64 >> 32);
    amount[28] = (uint8_t)(amount_u64 >> 24); amount[29] = (uint8_t)(amount_u64 >> 16);
    amount[30] = (uint8_t)(amount_u64 >> 8);  amount[31] = (uint8_t)(amount_u64);
    uint8_t tx_hash[32];
    memset(tx_hash, 0xAB, 32);
    uint8_t block_number[32] = {0};
    block_number[31] = 1;
    uint8_t block_coinbase[20] = {0};
    uint8_t mvm_id[20] = {0};

    unsigned char related_flat[40];
    memcpy(related_flat, sender, 20);
    memcpy(related_flat + 20, recipient, 20);

    printf("[host_repro] calling execute() (%s)...\n", label);
    fflush(stdout);

    ExecuteResult *rs = execute(
        (unsigned char *)sender, (unsigned char *)recipient,
        nullptr, 0,
        (unsigned char *)amount,
        1 /*gas_price*/, 200000 /*gas_limit*/,
        0 /*prevrandao*/, 30000000 /*block_gas_limit*/, (unsigned long long)time(nullptr) /*block_time*/, 1 /*base_fee*/,
        block_number, block_coinbase,
        mvm_id, tx_hash,
        true /*is_debug*/,
        related_flat, 2,
        false /*is_cache*/,
        nullptr, nullptr, 0, nullptr, nullptr, nullptr, nullptr, 0
    );

    printf("[host_repro] execute() RETURNED (%s, no crash) exitReason=%d exception=%d gas_used=%llu\n",
        label, rs->b_exitReason, rs->b_exception, rs->gas_used);
    return rs;
}

int main() {
    uint8_t sender[20], plain_recipient[20], contract_recipient[20];
    memset(sender, 0x11, 20);
    memset(plain_recipient, 0x22, 20);
    memcpy(contract_recipient, g_contract_addr, 20);

    // Mirrors mvm_ca_test.cpp's real ordering exactly: plain EOA->EOA
    // transfer FIRST (same mvm_id/tx_hash pattern reused), THEN the
    // contract call in the SAME process -- tests whether the crash is a
    // cross-call state issue (State singleton, Xapian tx buffer, etc.)
    // rather than something specific to the contract call in isolation.
    ExecuteResult *rs1 = do_execute("native transfer", sender, plain_recipient, 100);
    freeResult(rs1);

    ExecuteResult *rs2 = do_execute("contract call (SSTORE/SLOAD)", sender, contract_recipient, 0);
    freeResult(rs2);

    printf("[host_repro] BOTH calls completed, no crash.\n");
    return 0;
}
