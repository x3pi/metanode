// metanode's own, fully independent TrustZone TA entry point
// (execution_mode=trustzone, GĐ3 — see
// note/tee_dual_mode_execution_plan.md §9, 2026-08-17).
//
// Deliberately has ZERO dependency on tz-llm-trustzone/tz-llm/llama.cpp:
// no shared structs, no shared process, no shared build target — per the
// user's explicit direction to keep the two projects' TA work cleanly
// separated. The only things this file shares with that project are (a)
// the chcore-libc SDK headers (chcore/syscall.h, chcore/memory.h,
// chcore/llm.h — the OS's own public API, not project-specific) and (b)
// the push_pages()/usys_map_tzasc_cma_pmo *mechanism* that project
// pioneered and proved stable on this exact hardware, reimplemented here
// standalone (not linked/included from that repo).
//
// Wire protocol: pkg/mvm/tzproto/mvm_tz_protocol.h, shared verbatim with
// the Go host (pkg/mvm/tz_codec.go/tz_channel.go). See that header for
// the full design rationale.
//
// Scope as of 2026-08-17 (honest, not overstated):
//   - Channel setup (push_pages + map_tzasc_cma_pmo) — full.
//   - Dispatch loop (busy-poll, no wait_switch_req — see the design note
//     in mvm_reverse_round_trip's doc comment) — full.
//   - MVM_TZ_CMD_CALL (read-only/eth_call path) and MVM_TZ_CMD_EXECUTE
//     (the REAL state-changing tx entry point block processing actually
//     uses — mvm_api.go's Execute(), called once per tx by
//     true_block_stm.go): both full round trips, including the 6
//     reverse-callback shims routed back over this same channel.
//   - ExecuteResult encoding: status/exception/gas_used/output/exmsg and
//     the state-change arrays that actually matter for "did this block
//     change state" — add_balance_change/sub_balance_change/nonce_change
//     (uniform [20-byte addr][32-byte value]), full_db_hash (same shape),
//     code_change ([20-byte addr][data_len][code bytes]), storage_change
//     ([20-byte addr][pair_count][(32-byte key+32-byte value)*pair_count])
//     and storage_read ([20-byte addr][key_count][32-byte key*key_count])
//     are all fully wired now, byte layouts cross-checked against
//     pkg/mvm/helpers.go's real extraction code (extractCodeChange/
//     extractStorageChange/extractStorageRead/extractMapFullDbHash), not
//     guessed from the protocol header's prose alone.
//     full_db_logs/event_logs/native_logs/public_key_bls/account_type/
//     new_device_key are NOT yet wired -- counts are read correctly from
//     the real ExecuteResult but encoded as 0 entries for now (TODO,
//     tracked in the plan doc; the latter 3 are confirmed dead/unpopulated
//     in pkg/mvm today regardless). NOT silently wrong: a future caller
//     reading a real full_db_logs/event off this wire gets an empty
//     result, not corrupted data.
//   - MVM_TZ_CMD_DEPLOY/SEND_NATIVE/PROCESS_NATIVE_MINT_BURN/
//     NONCE_PLUS_ONE: NOT yet wired (mechanically identical to CALL/
//     EXECUTE's pattern — deferred, not attempted this pass to avoid
//     rushing unverifiable code).
//
// Built successfully (stripped, 5.1MB) and flashed to real hardware
// 2026-08-17 (DEPLOYED_STATE.md, tz-llm-trustzone) — but chanmgr's
// create_process("/mvm_ta") has NOT yet been observed to actually fire at
// runtime (needs a CA to open the first secure-world session). This file's
// *logic* has NEVER executed on real hardware. Every syscall/struct-layout
// choice here is grounded in a real, already-proven-on-this-board call
// site (cited in each comment) — but the *composition* of them for this
// new purpose is unverified until an actual runtime invocation.

#include "mvm_tz_protocol.h"
#include "mvm_linker.hpp"

#include <chcore/syscall.h>
#include <chcore/memory.h>
#include <chcore/llm.h>

#include <cstdio>
#include <cstring>
#include <cstdlib>
#include <cstdint>

// PAGE_SIZE/ROUND_UP already come from chcore/defs.h (included via
// chcore/syscall.h below) — don't redefine, just use them.

// ─────────────────────────── channel setup ───────────────────────────

// Which TZASC bank metanode's channel reserves from. Deliberately NOT
// TZASC_NR_MODEL/TZASC_NR_NPU_SCRATCH (index TZASC_NR-1) to reduce (not
// eliminate — a large enough model's tensor-pool overflow can still land
// here, see the plan doc) overlap with the LLM's own allocations.
#define MVM_TZASC_CMA_INDEX 1
#define MVM_CHANNEL_SIZE ROUND_UP(sizeof(mvm_tz_channel_t), PAGE_SIZE)

static mvm_tz_channel_t *g_channel = nullptr;

// Self-contained push_pages — deliberately NOT calling into
// tz-llm-trustzone's alloc-stage-chcore.cpp (separate project; also
// unreachable here regardless, since that file's globals live in a
// different chcore process's address space). This is a single
// allocation at TA startup, before anything else has touched TZASC (see
// the launch-order constraint in the plan doc §9.5) — no retry loop for
// transient ENOMEM/-EINTR the way that file's tuned-for-heavy-concurrent-
// load version needs; if this one call fails there is nothing else
// competing for the pool yet, so failure means something is genuinely
// wrong, not "try again".
static int mvm_push_pages(unsigned long size, int cma_index) {
    struct smc_registers req = {0};
    req.x1 = SMC_EXIT_SHADOW;
    req.x2 = 1;
    req.x3 = ROUND_UP(size, PAGE_SIZE) | (unsigned long)cma_index;
    return (int)usys_tee_switch_req(&req);
}

// Reserve + map metanode's dedicated shared channel. MUST run before
// anything else in this whole boot touches TZASC memory: entry_index is
// only guaranteed to be 0 (see plan §9.5 — read directly from
// tzasc_cma_push_pages_with_index()'s kernel source: entry_index =
// g_tzasc_cma_meta->count, pre-increment, per cma_index) if this is
// truly the FIRST allocation on MVM_TZASC_CMA_INDEX. chanmgr must launch
// this TA before it launches llama-cli — see the plan doc for the
// chanmgr.c patch this depends on.
static void mvm_channel_init(void) {
    unsigned long meta_vaddr = chcore_alloc_vaddr(PAGE_SIZE << 10);
    if (meta_vaddr == 0) {
        fprintf(stderr, "[mvm_ta] FATAL: chcore_alloc_vaddr(meta) failed\n");
        abort();
    }
    if (usys_map_tzasc_cma_meta(meta_vaddr) != 0) {
        fprintf(stderr, "[mvm_ta] FATAL: usys_map_tzasc_cma_meta failed\n");
        abort();
    }
    struct tzasc_cma_meta *meta_arr = (struct tzasc_cma_meta *)meta_vaddr;

    int entry_index = mvm_push_pages(MVM_CHANNEL_SIZE, MVM_TZASC_CMA_INDEX);
    if (entry_index != 0) {
        // Loud and immediate rather than a silent later mismatch: the
        // CA's discovery of this channel assumes (cma_index, 0) with no
        // live handshake (plan §9.5) — a nonzero entry_index here means
        // the launch-order constraint was violated (something else
        // already allocated on this bank first).
        fprintf(stderr,
            "[mvm_ta] FATAL: push_pages returned entry_index=%d, want 0 — "
            "launch-order constraint violated (something already used "
            "cma_index=%d before this TA started)\n",
            entry_index, MVM_TZASC_CMA_INDEX);
        abort();
    }

    unsigned long paddr = meta_arr[MVM_TZASC_CMA_INDEX].entry[entry_index].paddr;
    unsigned long vaddr = chcore_alloc_vaddr(MVM_CHANNEL_SIZE);
    if (vaddr == 0) {
        fprintf(stderr, "[mvm_ta] FATAL: chcore_alloc_vaddr(channel) failed\n");
        abort();
    }
    if (usys_map_tzasc_cma_pmo(vaddr, MVM_CHANNEL_SIZE, paddr) != 0) {
        fprintf(stderr, "[mvm_ta] FATAL: usys_map_tzasc_cma_pmo failed\n");
        abort();
    }

    g_channel = (mvm_tz_channel_t *)vaddr;
    memset(g_channel, 0, sizeof(*g_channel));
    g_channel->protocol_version = MVM_TZ_PROTOCOL_VERSION;
    printf("[mvm_ta] channel ready: cma_index=%d entry_index=%d paddr=%#lx vaddr=%#lx size=%#lx\n",
        MVM_TZASC_CMA_INDEX, entry_index, paddr, vaddr, (unsigned long)MVM_CHANNEL_SIZE);
    fflush(stdout);
}

// ───────────────────────── blob-stream helpers ─────────────────────────
// C mirror of tz_codec.go's blobWriter/blobReader — same wire shapes,
// same [uint32 LE len][data] framing for writeBytes/readBytes. Operates
// directly on a caller-supplied uint8_t* cursor into g_channel's own
// blob_region (or a scratch buffer for outgoing reverse-call requests),
// no separate allocation.

struct BlobWriter {
    uint8_t *buf;
    uint32_t cap;
    uint32_t off = 0;

    void writeRaw(const void *p, uint32_t n) {
        if (off + n > cap) { fprintf(stderr, "[mvm_ta] BlobWriter overflow\n"); abort(); }
        memcpy(buf + off, p, n);
        off += n;
    }
    void writeU32(uint32_t v) { writeRaw(&v, 4); } // host is little-endian (aarch64)
    void writeBytes(const void *p, uint32_t n) { writeU32(n); if (n) writeRaw(p, n); }
};

struct BlobReader {
    const uint8_t *buf;
    uint32_t len;
    uint32_t off = 0;

    void readRaw(void *out, uint32_t n) {
        if (off + n > len) { fprintf(stderr, "[mvm_ta] BlobReader underflow\n"); abort(); }
        memcpy(out, buf + off, n);
        off += n;
    }
    uint32_t readU32() { uint32_t v; readRaw(&v, 4); return v; }
    // Returns a pointer INTO buf (no copy) plus length via *out_len — safe
    // because the whole blob_region is a stable snapshot for the duration
    // of one dispatch (single-flight channel, no concurrent writer).
    const uint8_t *readBytes(uint32_t *out_len) {
        uint32_t n = readU32();
        if (off + n > len) { fprintf(stderr, "[mvm_ta] BlobReader underflow (bytes)\n"); abort(); }
        const uint8_t *p = buf + off;
        off += n;
        *out_len = n;
        return p;
    }
};

// ─────────────────── reverse-callback round trip ───────────────────
//
// Called from INSIDE libmvm_linker.a's own C++ code (my_global_state.cpp
// etc., via the extern "C" shims below) while a forward command is being
// processed. Reuses the SAME channel/fields the outer forward command
// used — safe because by this point the forward request's own header/
// blob have already been fully read out into local C++ state before
// call() (or whichever entry point) was invoked; see mvm_dispatch_call's
// ordering.
//
// Design note: deliberately does NOT use usys_tee_wait_switch_req for
// this wait, or for the outer dispatch loop's "wait for next forward
// request" either (see mvm_ta_run below) — busy-poll (yield-spin) on the
// shared atomic flag instead. This sidesteps entirely the documented,
// previously-hardware-crash-causing hazard of multiple call sites
// consuming each other's SMC events on that shared per-CPU FIFO
// rendezvous (STATUS.md, tz-llm-trustzone; see plan doc §9.4) — this
// design never touches that mechanism for anything except the one-time
// push_pages() SMC at startup, above. The cost is CPU spent busy-polling
// instead of sleeping; acceptable for a first correctness-focused pass.
static void mvm_reverse_round_trip(
    mvm_tz_cmd_t cmd,
    const void *req_header, uint32_t req_header_len,
    const void *req_blob, uint32_t req_blob_len,
    void *resp_header_out, uint32_t resp_header_cap, uint32_t *resp_header_len_out,
    const uint8_t **resp_blob_out, uint32_t *resp_blob_len_out) {

    if (req_header_len + req_blob_len > MVM_TZ_BLOB_REGION_SIZE) {
        fprintf(stderr, "[mvm_ta] FATAL: reverse request too large for blob region (cmd=%d)\n", cmd);
        abort();
    }

    mvm_tz_spinlock_lock(&g_channel->lock);
    if (req_header_len) memcpy(g_channel->blob_region, req_header, req_header_len);
    if (req_blob_len) memcpy(g_channel->blob_region + req_header_len, req_blob, req_blob_len);
    g_channel->cmd = cmd;
    g_channel->direction = MVM_TZ_DIR_TA_TO_HOST;
    g_channel->header_len = req_header_len;
    g_channel->blob_len = req_blob_len;
    g_channel->seq++;
    mvm_tz_spinlock_unlock(&g_channel->lock);

    mvm_tz_flag_set(&g_channel->request_ready, 1);

    while (!mvm_tz_flag_cas(&g_channel->response_ready, 1, 0)) {
        usys_yield();
    }

    mvm_tz_spinlock_lock(&g_channel->lock);
    uint32_t hlen = g_channel->header_len;
    if (hlen > resp_header_cap) {
        mvm_tz_spinlock_unlock(&g_channel->lock);
        fprintf(stderr, "[mvm_ta] FATAL: reverse resp header_len=%u exceeds cap=%u (cmd=%d)\n",
            hlen, resp_header_cap, cmd);
        abort();
    }
    memcpy(resp_header_out, g_channel->blob_region, hlen);
    *resp_header_len_out = hlen;
    *resp_blob_len_out = g_channel->blob_len;
    *resp_blob_out = g_channel->blob_region + hlen; // valid until the NEXT round trip reuses the region
    mvm_tz_spinlock_unlock(&g_channel->lock);
}

// ─────────────────── the 6 live reverse-callback shims ───────────────────
// Signatures/semantics mirror mvm_api.go's cgo exports and extension.go's
// exactly (see pkg/mvm/mvm_api.go, pkg/mvm/extension.go) — these are what
// libmvm_linker.a's my_global_state.cpp/my_storage.cpp/my_extension.cpp
// call as `extern` C functions today; on the cgo path Go provides them
// in-process, here they instead round-trip over mvm_reverse_round_trip.
// Wire shapes: note/tee_dual_mode_execution_plan.md §2.1 / GĐ1,
// mvm_tz_protocol.h's "Reverse command headers" section.

// Mirrors linker/src/my_global_state.cpp's local definition exactly.
struct GlobalStateGet_return {
  int status;
  unsigned char *balance_p;
  unsigned char *nonce;
  unsigned char *code_p;
  int code_length;
};

// Mirrors mvm_linker.hpp's own definition, normally only visible under
// -DMVM_LINKER_BUILD (mvm_tz_get_storage_value_resp_t.status uses the
// same StorageStatus values: 0=SUCCESS/1=NOT_FOUND/2=SUSPEND). Defined
// locally so this file doesn't depend on that build flag being set
// exactly right — self-contained, same spirit as GlobalStateGet_return
// above.
struct GetStorageValue_return {
  unsigned char *value;
  int status;
};

extern "C" {

GlobalStateGet_return GlobalStateGet(unsigned char *mvmId, unsigned char *address) {
    mvm_tz_global_state_get_req_t req = {0};
    memcpy(req.mvm_id, mvmId, 20);
    memcpy(req.address, address, 20);

    mvm_tz_global_state_get_resp_t resp_hdr;
    uint32_t resp_hdr_len = 0, resp_blob_len = 0;
    const uint8_t *resp_blob = nullptr;
    mvm_reverse_round_trip(MVM_TZ_RCMD_GLOBAL_STATE_GET,
        &req, sizeof(req), nullptr, 0,
        &resp_hdr, sizeof(resp_hdr), &resp_hdr_len, &resp_blob, &resp_blob_len);

    GlobalStateGet_return ret = {0};
    ret.status = resp_hdr.status;
    if (ret.status == 1) {
        BlobReader r{resp_blob, resp_blob_len};
        ret.balance_p = (unsigned char *)malloc(32);
        r.readRaw(ret.balance_p, 32);
        ret.nonce = (unsigned char *)malloc(32);
        r.readRaw(ret.nonce, 32);
        uint32_t code_len = 0;
        const uint8_t *code = r.readBytes(&code_len);
        ret.code_length = (int)code_len;
        if (code_len > 0) {
            ret.code_p = (unsigned char *)malloc(code_len);
            memcpy(ret.code_p, code, code_len);
        }
    }
    return ret;
}

GetStorageValue_return GetStorageValue(unsigned char *mvmId, unsigned char *address, unsigned char *key) {
    mvm_tz_get_storage_value_req_t req = {0};
    memcpy(req.mvm_id, mvmId, 20);
    memcpy(req.address, address, 20);
    memcpy(req.key, key, 32);

    mvm_tz_get_storage_value_resp_t resp_hdr;
    uint32_t resp_hdr_len = 0, resp_blob_len = 0;
    const uint8_t *resp_blob = nullptr;
    mvm_reverse_round_trip(MVM_TZ_RCMD_GET_STORAGE_VALUE,
        &req, sizeof(req), nullptr, 0,
        &resp_hdr, sizeof(resp_hdr), &resp_hdr_len, &resp_blob, &resp_blob_len);

    GetStorageValue_return ret = {0};
    ret.status = resp_hdr.status;
    if (ret.status == 0) {
        ret.value = (unsigned char *)malloc(32);
        memcpy(ret.value, resp_blob, 32);
    }
    return ret;
}

void ClearProcessingPointers(unsigned char *) {
    // Confirmed dead in Go today (mvm_api.go's own comment: "HÀM NÀY
    // KHÔNG CÒN CẦN THIẾT"), kept only so the C++ side still links. No
    // wire round trip needed — see plan doc §8.
}

static Extension_return mvm_extension_bytes_call(mvm_tz_cmd_t cmd, unsigned char *bytes, int size) {
    uint32_t resp_hdr_len = 0, resp_blob_len = 0;
    const uint8_t *resp_blob = nullptr;
    uint8_t no_header;
    mvm_reverse_round_trip(cmd, nullptr, 0, bytes, (uint32_t)size,
        &no_header, 0, &resp_hdr_len, &resp_blob, &resp_blob_len);

    Extension_return ret = {nullptr, 0};
    if (resp_blob_len > 0) {
        ret.data_p = (unsigned char *)malloc(resp_blob_len);
        memcpy(ret.data_p, resp_blob, resp_blob_len);
        ret.data_size = (int)resp_blob_len;
    }
    return ret;
}

Extension_return ExtensionCallGetApi(unsigned char *bytes, int size) {
    return mvm_extension_bytes_call(MVM_TZ_RCMD_EXTENSION_CALL_GET_API, bytes, size);
}

Extension_return ExtensionExtractJsonField(unsigned char *bytes, int size) {
    return mvm_extension_bytes_call(MVM_TZ_RCMD_EXTENSION_EXTRACT_JSON_FIELD, bytes, size);
}

Extension_return ExtensionBlst(unsigned char *bytes, int size) {
    return mvm_extension_bytes_call(MVM_TZ_RCMD_EXTENSION_BLST, bytes, size);
}

Extension_return ExtensionGetOrCreateSimpleDb(unsigned char *bytes, int size, unsigned char *address, unsigned char *mvmId) {
    mvm_tz_get_or_create_simple_db_req_t req_hdr = {0};
    memcpy(req_hdr.address, address, 20);
    memcpy(req_hdr.mvm_id, mvmId, 20);

    uint32_t resp_hdr_len = 0, resp_blob_len = 0;
    const uint8_t *resp_blob = nullptr;
    uint8_t no_header;
    mvm_reverse_round_trip(MVM_TZ_RCMD_EXTENSION_GET_OR_CREATE_SIMPLE_DB,
        &req_hdr, sizeof(req_hdr), bytes, (uint32_t)size,
        &no_header, 0, &resp_hdr_len, &resp_blob, &resp_blob_len);

    Extension_return ret = {nullptr, 0};
    if (resp_blob_len > 0) {
        ret.data_p = (unsigned char *)malloc(resp_blob_len);
        memcpy(ret.data_p, resp_blob, resp_blob_len);
        ret.data_size = (int)resp_blob_len;
    }
    return ret;
}

} // extern "C"

// ───────────────────────── ExecuteResult encoding ─────────────────────────
// Mirrors tz_codec.go's encodeExecuteResult field-for-field (see that
// function's own doc comment for the full blob order this must match).
// See this file's top-of-file comment for exactly which pieces are wired
// vs deferred as of 2026-08-17.
static void mvm_encode_execute_result(const ExecuteResult *r,
    mvm_tz_execute_result_hdr_t *hdr_out, BlobWriter *w) {

    hdr_out->status = (uint8_t)r->b_exitReason;
    hdr_out->exception = (uint8_t)r->b_exception;
    hdr_out->has_simple_db_hash = 0; // TODO: not yet threaded through from ExecuteResult
    hdr_out->gas_used = (uint64_t)r->gas_used;

    hdr_out->full_db_hash_count = (uint32_t)r->length_full_db_hash;
    hdr_out->full_db_logs_count = 0;       // TODO
    hdr_out->add_balance_change_count = (uint32_t)r->length_add_balance_change;
    hdr_out->nonce_change_count = (uint32_t)r->length_nonce_change;
    hdr_out->sub_balance_change_count = (uint32_t)r->length_sub_balance_change;
    hdr_out->code_change_count = (uint32_t)r->length_code_change;
    hdr_out->storage_change_count = (uint32_t)r->length_storage_change;
    hdr_out->storage_read_count = (uint32_t)r->length_storage_read;
    hdr_out->public_key_bls_count = 0;     // TODO (confirmed dead today, see protocol header)
    hdr_out->account_type_count = 0;       // TODO (confirmed dead today, see protocol header)
    hdr_out->new_device_key_count = 0;     // TODO (confirmed dead today, see protocol header)

    w->writeBytes(r->b_exmsg, (uint32_t)r->length_exmsg);
    w->writeBytes(r->b_output, (uint32_t)r->length_output);

    // full_db_hash: each entry is a 64-byte [32-byte left-padded
    // address][32-byte hash] buffer (confirmed via helpers.go's
    // extractMapFullDbHash: expectedLen=64, addr=bytes[12:32],
    // hash=bytes[32:64]) — same shape as add_balance_change, so reuse the
    // lambda below.

    // add_balance_change / nonce_change / sub_balance_change / full_db_hash:
    // each entry is a 64-byte [32-byte left-padded address][32-byte value]
    // buffer (confirmed via helpers.go's extractAddBalance/extractSubBalance/
    // extractMapFullDbHash — identical shape), matching writeAddrValueMap's
    // wire shape exactly: [20-byte addr][32-byte value], so we slice the
    // address's last 20 bytes out of the 32-byte left-padded field.
    auto write_addr_value_array = [&](char **arr, int count) {
        for (int i = 0; i < count; i++) {
            const uint8_t *entry = (const uint8_t *)arr[i]; // 64 bytes
            w->writeRaw(entry + 12, 20); // address (last 20 of the first 32)
            w->writeRaw(entry + 32, 32); // value
        }
    };
    write_addr_value_array(r->full_db_hash, r->length_full_db_hash);
    // full_db_logs: TODO, 0 entries written (count above is 0)
    write_addr_value_array(r->b_add_balance_change, r->length_add_balance_change);
    write_addr_value_array(r->b_nonce_change, r->length_nonce_change);
    write_addr_value_array(r->b_sub_balance_change, r->length_sub_balance_change);

    // code_change: entry buffer is [32-byte left-padded address][code
    // bytes], entry LENGTH (length_codes[i]) INCLUDES the 32-byte address
    // prefix (confirmed via helpers.go's extractCodeChange: v=length_codes[i]
    // is the buffer's total byte count, code=addrWithCode[32:]). Wire shape
    // per mvm_tz_protocol.h: [20-byte addr][uint32_t data_len][data_len bytes].
    for (int i = 0; i < r->length_code_change; i++) {
        const uint8_t *entry = (const uint8_t *)r->b_code_change[i];
        int total_len = r->length_codes[i];
        int data_len = total_len - 32;
        w->writeRaw(entry + 12, 20);       // address
        w->writeU32((uint32_t)data_len);
        if (data_len > 0) w->writeRaw(entry + 32, (uint32_t)data_len);
    }

    // storage_change: entry buffer is [32-byte left-padded address]
    // [pair_count * (32-byte key + 32-byte value)], entry LENGTH
    // (length_storages[i]) EXCLUDES the 32-byte address prefix (confirmed
    // via helpers.go's extractStorageChange: v=length_storages[i], buffer
    // actually read is v+32 bytes, storageCount=v/64). Wire shape:
    // [20-byte addr][uint32_t pair_count][pair_count*(32-byte key+32-byte value)].
    for (int i = 0; i < r->length_storage_change; i++) {
        const uint8_t *entry = (const uint8_t *)r->b_storage_change[i];
        int data_len = r->length_storages[i]; // excludes addr prefix, per above
        uint32_t pair_count = (uint32_t)(data_len / 64);
        w->writeRaw(entry + 12, 20);       // address
        w->writeU32(pair_count);
        if (data_len > 0) w->writeRaw(entry + 32, (uint32_t)data_len); // pairs follow the addr
    }

    // storage_read: entry buffer is [32-byte left-padded address]
    // [key_count * 32-byte key], entry LENGTH (length_storages_read[i])
    // INCLUDES the 32-byte address prefix (confirmed via helpers.go's
    // extractStorageRead: v=length_storages_read[i] is the total buffer
    // size, storageCount=(v-32)/32). Wire shape:
    // [20-byte addr][uint32_t key_count][key_count*32-byte key].
    for (int i = 0; i < r->length_storage_read; i++) {
        const uint8_t *entry = (const uint8_t *)r->b_storage_read[i];
        int total_len = r->length_storages_read[i];
        int keys_len = total_len - 32;
        uint32_t key_count = (uint32_t)(keys_len / 32);
        w->writeRaw(entry + 12, 20);       // address
        w->writeU32(key_count);
        if (keys_len > 0) w->writeRaw(entry + 32, (uint32_t)keys_len);
    }

    // public_key_bls, account_type, new_device_key: TODO, 0 entries
    // written — confirmed dead in pkg/mvm today (protocol header comment).
    // simple_db_hash: not written (has_simple_db_hash=0 above).

    // event_logs_json: TODO — write an empty JSON array so the blob
    // shape stays well-formed for whatever reads it next.
    static const char empty_json[] = "[]";
    w->writeBytes(empty_json, 2);

    // native_logs: TODO — empty.
    w->writeBytes(nullptr, 0);
}

// ───────────────────────── MVM_TZ_CMD_CALL dispatch ─────────────────────────
static void mvm_dispatch_call(const uint8_t *req_header, uint32_t req_header_len,
    const uint8_t *req_blob, uint32_t req_blob_len,
    BlobWriter *resp_blob_writer, mvm_tz_execute_result_hdr_t *resp_hdr_out) {

    if (req_header_len != sizeof(mvm_tz_call_req_t)) {
        fprintf(stderr, "[mvm_ta] CALL: bad header_len=%u want=%zu\n",
            req_header_len, sizeof(mvm_tz_call_req_t));
        abort();
    }
    mvm_tz_call_req_t req;
    memcpy(&req, req_header, sizeof(req));

    BlobReader r{req_blob, req_blob_len};
    uint32_t n;
    const uint8_t *b_sender = r.readBytes(&n);           // [0] bSender (20)
    const uint8_t *b_contract = r.readBytes(&n);          // [1] bContractAddress (20)
    uint32_t input_len;
    const uint8_t *b_input = r.readBytes(&input_len);     // [2] bInput
    const uint8_t *b_tx_hash = r.readBytes(&n);            // [3] bTxHash (32)

    unsigned char *related_flat = nullptr;
    if (req.related_addresses_count > 0) {
        related_flat = (unsigned char *)malloc(20u * req.related_addresses_count);
        for (uint32_t i = 0; i < req.related_addresses_count; i++) {
            uint32_t alen;
            const uint8_t *a = r.readBytes(&alen); // [4..] relatedAddresses, 20 each
            memcpy(related_flat + i * 20, a, 20);
        }
    }

    // call()'s b_block_number wants a 32-byte big-endian buffer (see
    // mvm_api.go's own Call(): big.NewInt(int64(blockNumber)).FillBytes(...))
    // — the wire header carries block_number as a plain uint64_t, so
    // convert here.
    uint8_t b_block_number[32] = {0};
    {
        uint64_t bn = req.block_number;
        for (int i = 0; i < 8; i++) {
            b_block_number[31 - i] = (uint8_t)(bn & 0xff);
            bn >>= 8;
        }
    }

    // MVM_B1_CONTEXT_PARAMS (chain_id/blob context/cross-chain context):
    // not yet threaded through this protocol version — pass all-NULL,
    // matching "not supplied" semantics documented in mvm_linker.hpp.
    // Exact param order verified against the real declaration in
    // mvm_linker.hpp (call()), not re-derived/guessed.
    ExecuteResult *rs = call(
        (unsigned char *)b_sender, (unsigned char *)b_contract,
        (unsigned char *)b_input, (int)input_len,
        (unsigned char *)req.amount,
        req.gas_price, req.gas_limit,
        req.block_prevrandao, req.block_gas_limit, req.block_time, req.block_base_fee,
        b_block_number, (unsigned char *)req.block_coinbase,
        (unsigned char *)req.mvm_id,
        req.read_only != 0, (unsigned char *)b_tx_hash,
        req.is_debug != 0,
        related_flat, (int)req.related_addresses_count,
        req.is_off_chain != 0,
        nullptr, nullptr, 0, nullptr, nullptr, nullptr, nullptr, 0
    );

    mvm_encode_execute_result(rs, resp_hdr_out, resp_blob_writer);
    freeResult(rs);
    if (related_flat) free(related_flat);
}

// ───────────────────────── MVM_TZ_CMD_EXECUTE dispatch ─────────────────────────
// The REAL state-changing transaction entry point (calls C++ execute(), not
// call()) — this is what actual block processing uses (mirrors
// mvm_api.go's Execute(), which every tx in a block goes through, unlike
// Call() which is the read-only/eth_call path with no committed state
// change). Structurally identical to mvm_dispatch_call() above except:
// no read_only/is_off_chain fields (execute() has neither), is_cache
// instead, and calls execute() rather than call().
static void mvm_dispatch_execute(const uint8_t *req_header, uint32_t req_header_len,
    const uint8_t *req_blob, uint32_t req_blob_len,
    BlobWriter *resp_blob_writer, mvm_tz_execute_result_hdr_t *resp_hdr_out) {

    if (req_header_len != sizeof(mvm_tz_execute_req_t)) {
        fprintf(stderr, "[mvm_ta] EXECUTE: bad header_len=%u want=%zu\n",
            req_header_len, sizeof(mvm_tz_execute_req_t));
        abort();
    }
    mvm_tz_execute_req_t req;
    memcpy(&req, req_header, sizeof(req));

    BlobReader r{req_blob, req_blob_len};
    uint32_t n;
    const uint8_t *b_sender = r.readBytes(&n);            // [0] bSender (20)
    const uint8_t *b_contract = r.readBytes(&n);           // [1] bContractAddress (20)
    uint32_t input_len;
    const uint8_t *b_input = r.readBytes(&input_len);      // [2] bInput
    const uint8_t *b_tx_hash = r.readBytes(&n);             // [3] bTxHash (32)

    unsigned char *related_flat = nullptr;
    if (req.related_addresses_count > 0) {
        related_flat = (unsigned char *)malloc(20u * req.related_addresses_count);
        for (uint32_t i = 0; i < req.related_addresses_count; i++) {
            uint32_t alen;
            const uint8_t *a = r.readBytes(&alen); // [4..] relatedAddresses, 20 each
            memcpy(related_flat + i * 20, a, 20);
        }
    }

    // Same blockNumber big.Int-FillBytes conversion as mvm_dispatch_call
    // (mvm_api.go's Execute() does the identical
    // big.NewInt(int64(blockNumber)).FillBytes(...) before calling C.execute).
    uint8_t b_block_number[32] = {0};
    {
        uint64_t bn = req.block_number;
        for (int i = 0; i < 8; i++) {
            b_block_number[31 - i] = (uint8_t)(bn & 0xff);
            bn >>= 8;
        }
    }

    // MVM_B1_CONTEXT_PARAMS: not yet threaded through this protocol
    // version — pass all-NULL, same as mvm_dispatch_call.
    ExecuteResult *rs = execute(
        (unsigned char *)b_sender, (unsigned char *)b_contract,
        (unsigned char *)b_input, (int)input_len,
        (unsigned char *)req.amount,
        req.gas_price, req.gas_limit,
        req.block_prevrandao, req.block_gas_limit, req.block_time, req.block_base_fee,
        b_block_number, (unsigned char *)req.block_coinbase,
        (unsigned char *)req.mvm_id, (unsigned char *)b_tx_hash,
        req.is_debug != 0,
        related_flat, (int)req.related_addresses_count,
        req.is_cache != 0,
        nullptr, nullptr, 0, nullptr, nullptr, nullptr, nullptr, 0
    );

    mvm_encode_execute_result(rs, resp_hdr_out, resp_blob_writer);
    freeResult(rs);
    if (related_flat) free(related_flat);
}

// ───────────────────────── main dispatch loop ─────────────────────────
//
// Design note (see mvm_reverse_round_trip's comment for the full
// rationale): busy-polls request_ready via yield-spin instead of
// usys_tee_wait_switch_req, to avoid touching the shared per-CPU SMC
// rendezvous that llama.cpp's own main.cpp wait loop also uses — this TA
// never calls usys_tee_wait_switch_req at all, sidestepping the
// documented ordering hazard (plan §9.4) entirely rather than trying to
// prove it safe.
static void mvm_ta_run(void) {
    static uint8_t resp_blob_scratch[MVM_TZ_BLOB_REGION_SIZE];

    for (;;) {
        while (!mvm_tz_flag_cas(&g_channel->request_ready, 1, 0)) {
            usys_yield();
        }

        mvm_tz_spinlock_lock(&g_channel->lock);
        mvm_tz_cmd_t cmd = g_channel->cmd;
        uint32_t header_len = g_channel->header_len;
        uint32_t blob_len = g_channel->blob_len;
        // Copy the request out before releasing the lock / before any
        // reverse-callback round trip reuses blob_region for its own
        // traffic (mvm_reverse_round_trip is only safe to call AFTER
        // this point, never before).
        static uint8_t req_header_copy[512];
        static uint8_t req_blob_copy[MVM_TZ_BLOB_REGION_SIZE];
        if (header_len > sizeof(req_header_copy)) {
            mvm_tz_spinlock_unlock(&g_channel->lock);
            fprintf(stderr, "[mvm_ta] FATAL: forward header_len=%u exceeds scratch cap\n", header_len);
            abort();
        }
        memcpy(req_header_copy, g_channel->blob_region, header_len);
        memcpy(req_blob_copy, g_channel->blob_region + header_len, blob_len);
        mvm_tz_spinlock_unlock(&g_channel->lock);

        BlobWriter w{resp_blob_scratch, sizeof(resp_blob_scratch)};
        mvm_tz_execute_result_hdr_t resp_hdr = {0};
        bool handled = true;

        switch (cmd) {
        case MVM_TZ_CMD_CALL:
            mvm_dispatch_call(req_header_copy, header_len, req_blob_copy, blob_len, &w, &resp_hdr);
            break;
        case MVM_TZ_CMD_EXECUTE:
            mvm_dispatch_execute(req_header_copy, header_len, req_blob_copy, blob_len, &w, &resp_hdr);
            break;
        // MVM_TZ_CMD_DEPLOY/SEND_NATIVE/PROCESS_NATIVE_MINT_BURN/
        // NONCE_PLUS_ONE: not yet wired (see this file's top comment) —
        // fall through to the default error response below.
        default:
            handled = false;
            fprintf(stderr, "[mvm_ta] cmd=%d not yet implemented\n", cmd);
            break;
        }

        mvm_tz_spinlock_lock(&g_channel->lock);
        if (handled) {
            memcpy(g_channel->blob_region, &resp_hdr, sizeof(resp_hdr));
            memcpy(g_channel->blob_region + sizeof(resp_hdr), resp_blob_scratch, w.off);
            g_channel->header_len = sizeof(resp_hdr);
            g_channel->blob_len = w.off;
        } else {
            g_channel->header_len = 0;
            g_channel->blob_len = 0;
        }
        g_channel->cmd = cmd;
        g_channel->direction = MVM_TZ_DIR_HOST_TO_TA; // response to a host-initiated request
        g_channel->seq++;
        mvm_tz_spinlock_unlock(&g_channel->lock);

        mvm_tz_flag_set(&g_channel->response_ready, 1);
    }
}

int main(int argc, char **argv) {
    (void)argc; (void)argv;
    printf("[mvm_ta] starting\n");
    fflush(stdout);
    mvm_channel_init();
    mvm_ta_run();
    return 0; // unreachable
}
