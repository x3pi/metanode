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

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <cstdint>
#include <ctime>

#include <fcntl.h>
#include <unistd.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <errno.h>

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

// mvm_ta_main.cpp's own channel placement (mvm_ta_main.cpp:
// MVM_TZASC_CMA_INDEX=1, entry_index guaranteed 0 by the launch-order
// constraint chanmgr.c's patch enforces -- see plan doc §9.5).
#define MVM_TZASC_CMA_INDEX 1
#define MVM_TZASC_ENTRY_INDEX 0

static mvm_tz_channel_t *g_channel = nullptr;

// ─── reverse-call handling (TA -> this CA) ───
// Answers only what a single native-value-transfer EXECUTE between two
// never-before-seen synthetic addresses can plausibly need. Anything else
// aborts loudly (a clean, diagnosable failure) rather than silently
// hanging or fabricating wrong data.
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
        // Every address in this test is synthetic/never-before-seen ->
        // status=0 ("not found, create fresh") is the only correct answer.
        mvm_tz_global_state_get_resp_t resp = {0};
        resp.status = 0;
        memcpy(resp_buf, &resp, sizeof(resp));
        resp_hdr_len = sizeof(resp);
        resp_blob_len = 0; // status==0 -> no blob per protocol header
        break;
    }
    case MVM_TZ_RCMD_GET_STORAGE_VALUE: {
        // Shouldn't fire for a pure EOA->EOA native transfer (no code at
        // either address) -- included so a wrong assumption fails loud
        // (STORAGE_NOT_FOUND=1) rather than hanging.
        mvm_tz_get_storage_value_resp_t resp = {0};
        resp.status = 1;
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

// ─── blob writer (mirrors mvm_ta_main.cpp's BlobWriter) ───
struct BlobWriter {
    uint8_t *buf;
    uint32_t off = 0;
    void writeRaw(const void *p, uint32_t n) { memcpy(buf + off, p, n); off += n; }
    void writeU32(uint32_t v) { writeRaw(&v, 4); }
    void writeBytes(const void *p, uint32_t n) { writeU32(n); if (n) writeRaw(p, n); }
};

int main(void) {
    printf("[mvm_ca_test] opening %s\n", DEVICE_NAME);
    int fd = open(DEVICE_NAME, O_RDWR);
    if (fd < 0) { perror("open"); return 1; }

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

    // ─── build MVM_TZ_CMD_EXECUTE request: trivial native transfer ───
    // sender=0x11*20, contract(recipient)=0x22*20, amount=100 wei,
    // empty input (pure value transfer, no bytecode execution), tx_hash=
    // arbitrary nonzero 32 bytes, relatedAddresses=[sender,recipient]
    // (declares the expected read/write set -- avoids GlobalStateGet's
    // status==2 "addressNotInRelated" path, see my_global_state.cpp).
    uint8_t sender[20], recipient[20], tx_hash[32];
    memset(sender, 0x11, 20);
    memset(recipient, 0x22, 20);
    memset(tx_hash, 0xAB, 32);

    mvm_tz_execute_req_t req = {0};
    req.amount[31] = 100; // 100 wei, big-endian
    req.gas_price = 1;
    req.gas_limit = 21000;
    req.block_prevrandao = 0;
    req.block_gas_limit = 30000000;
    req.block_time = (uint64_t)time(nullptr);
    req.block_base_fee = 1;
    req.block_number = 1;
    memset(req.block_coinbase, 0, 20);
    memset(req.mvm_id, 0, 20);
    req.is_debug = 1;
    req.is_cache = 0;
    req.related_addresses_count = 2;

    static uint8_t blob_scratch[4096];
    BlobWriter w{blob_scratch};
    w.writeBytes(sender, 20);
    w.writeBytes(recipient, 20);
    w.writeBytes(nullptr, 0); // empty input
    w.writeBytes(tx_hash, 32);
    w.writeBytes(sender, 20);    // relatedAddresses[0]
    w.writeBytes(recipient, 20); // relatedAddresses[1]

    printf("[mvm_ca_test] sending MVM_TZ_CMD_EXECUTE: sender=0x11..11 recipient=0x22..22 amount=100wei\n");
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

    // ─── dispatch loop: consume nested reverse calls until the real
    // response (direction==HOST_TO_TA, meaning "this is the answer to my
    // own forward command") arrives. Watches BOTH flags in one loop --
    // watching them sequentially (response_ready first, request_ready
    // only after giving up) would spuriously time out whenever a nested
    // reverse call happens well within the window, since that only ever
    // flips request_ready, never response_ready. ───
    const int TIMEOUT_S = 60;
    for (int round = 0; ; round++) {
        time_t start = time(nullptr);
        uint64_t last_seq = (uint64_t)-1;
        int which = -1; // 0 = response_ready fired, 1 = request_ready fired
        while (which < 0) {
            uint8_t exp = 1;
            if (__atomic_compare_exchange_n(&g_channel->response_ready, &exp, 0, 0, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) {
                which = 0;
                break;
            }
            exp = 1;
            if (__atomic_compare_exchange_n(&g_channel->request_ready, &exp, 0, 0, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) {
                which = 1;
                break;
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
            usleep(10000); // 10ms poll -- this is a probe tool, not the TA's own hot loop
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

    // ─── decode ExecuteResult ───
    mvm_tz_execute_result_hdr_t hdr;
    memcpy(&hdr, g_channel->blob_region, sizeof(hdr));
    printf("\n=== ExecuteResult ===\n");
    printf("status=%u exception=%u gas_used=%llu\n", hdr.status, hdr.exception, (unsigned long long)hdr.gas_used);
    printf("add_balance_change_count=%u sub_balance_change_count=%u nonce_change_count=%u\n",
        hdr.add_balance_change_count, hdr.sub_balance_change_count, hdr.nonce_change_count);
    printf("code_change_count=%u storage_change_count=%u storage_read_count=%u full_db_hash_count=%u\n",
        hdr.code_change_count, hdr.storage_change_count, hdr.storage_read_count, hdr.full_db_hash_count);

    const uint8_t *blob = g_channel->blob_region + sizeof(hdr);
    uint32_t off = 0;
    auto readU32 = [&](void) { uint32_t v; memcpy(&v, blob + off, 4); off += 4; return v; };
    auto skipBytes = [&](void) { uint32_t n = readU32(); off += n; return n; };

    skipBytes(); // exmsg
    skipBytes(); // output
    for (uint32_t i = 0; i < hdr.full_db_hash_count; i++) off += 52;
    for (uint32_t i = 0; i < hdr.full_db_logs_count; i++) { /* not wired yet, count should be 0 */ }

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

    printf("\n[mvm_ca_test] DONE\n");
    return 0;
}
