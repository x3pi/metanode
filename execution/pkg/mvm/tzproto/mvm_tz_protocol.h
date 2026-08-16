#pragma once
/*
 * mvm_tz_protocol.h — CA<->TA wire protocol for execution_mode=trustzone.
 *
 * Shared verbatim by both sides of the boundary: the Normal-World Go host
 * (via cgo, GĐ2 loopback and later the real transport) and the Secure-World
 * TA C++ build (GĐ3). Pure C, no C++ features — must compile under both
 * glibc (host) and chcore-libc/musl (TA).
 *
 * See note/tee_dual_mode_execution_plan.md for the design rationale. This
 * header is GĐ1's deliverable: paper design, not yet wired into any build.
 *
 * ─── Framing ──────────────────────────────────────────────────────────
 * One shared page holds ONE control block (mvm_tz_channel_t) plus a blob
 * region. Because execution_mode=trustzone runs strictly SERIALIZED at this
 * stage (see plan's "Hệ quả thiết kế then chốt" #5 — the Go host holds a
 * mutex around every call into the TZ engine), there is exactly one
 * request/response slot, not an N-slot ring. GĐ2's loopback and GĐ3's real
 * transport must each enforce single-flight themselves; this header does
 * not defend against concurrent requesters.
 *
 * Synchronization mirrors tz-llm-trustzone's proven pattern EXACTLY
 * (tz-llm/llama.cpp/src/interface.h): a spinlock built purely from
 * std::atomic/C11 _Atomic CAS, NEVER a libc mutex — glibc (host) and musl
 * (TA) lay out pthread_mutex_t differently, and a shared std::mutex across
 * this exact kind of boundary has previously crashed on real hardware
 * (confirmed in tz-llm-trustzone's history). request_ready / response_ready
 * are plain _Atomic(bool) flags polled by each side; the spinlock itself
 * only guards the rare case of the header fields being written non-
 * atomically as a group.
 *
 * ─── Blob region ──────────────────────────────────────────────────────
 * Every variable-length field (calldata, code, storage change arrays,
 * Xapian payloads, ...) is written into the trailing blob region as a
 * sequence of [uint32_t len][len bytes] records, in the FIXED ORDER
 * documented per command below — never as a nested pointer. This is the
 * "blob length-prefixed" scheme called for by the plan; it is the direct
 * analogue of ExecuteResult's parallel byte-array fields
 * (linker/include/mvm_linker.hpp:17-72), just flattened into one
 * self-describing stream instead of N raw C++ pointers, because raw
 * pointers do not survive a world-switch.
 *
 * ─── Versioning ───────────────────────────────────────────────────────
 * MVM_TZ_PROTOCOL_VERSION bumps on any wire-incompatible change to this
 * file. mvm_tz_channel_t.protocol_version is set by whichever side
 * initializes the shared page; the other side MUST refuse to proceed on
 * mismatch rather than guess field layout.
 */

#include <stdint.h>
#include <stdbool.h>

#define MVM_TZ_PROTOCOL_VERSION 1u

/* Placeholder — MUST be re-derived from the real target board's secure
 * memory ceiling in Giai đoạn 3 (note/tee_dual_mode_execution_plan.md §5).
 * Do not carry the 3GB TZASC figure from the tz-llm-trustzone board over
 * unverified; that is a different piece of hardware. */
#define MVM_TZ_BLOB_REGION_SIZE (4u * 1024u * 1024u) /* 4 MiB, placeholder */

/* ───────────────────────── Command IDs ──────────────────────────────
 * Every command below is annotated with its cgo call-site counterpart
 * (file:line, as of 2026-08-15) for traceability back to
 * note/tee_dual_mode_execution_plan.md §3's inventory. Commands NOT listed
 * here (ClearProcessingPointers; commit_full_db/revert_full_db/
 * MVM_cancelTransaction, which are declared in mvm_linker.hpp but have no
 * Go call site at all; testMemLeak/testMemLeakGS, debug-only) are
 * confirmed dead and intentionally excluded from the wire protocol.
 */
typedef enum {
    MVM_TZ_CMD_NONE = 0,

    /* ---- Forward (Go -> TA): 7 execution entry points ----
     * mvm_api.go: call:811 execute:904 deploy:1283 executeBatch:1030
     * sendNative:1098 processNativeMintBurn:1154 noncePlusOne:1202
     * All but EXECUTE_BATCH return mvm_tz_execute_result_t (below).
     * EXECUTE_BATCH has ZERO production callers today (confirmed
     * 2026-08-15) — included for interface completeness, lowest
     * implementation priority in GĐ2. */
    MVM_TZ_CMD_CALL                       = 1,
    MVM_TZ_CMD_EXECUTE                    = 2,
    MVM_TZ_CMD_DEPLOY                     = 3,
    MVM_TZ_CMD_EXECUTE_BATCH              = 4, /* dead in Go today; keep for interface parity */
    MVM_TZ_CMD_SEND_NATIVE                = 5,
    MVM_TZ_CMD_PROCESS_NATIVE_MINT_BURN   = 6,
    MVM_TZ_CMD_NONCE_PLUS_ONE             = 7,

    /* ---- Forward (Go -> TA): lifecycle / setup ----
     * mvm_api.go: clearAllStateInstances:199,1654 updateStateNonce:207
     * updateStateBalance:217 clear/commit_xapian_tx_buffer(_batch):
     * 1666,1675,1696,1717 MVM_commitAllXapian:1658 ReplayFullDbLogs:190 */
    MVM_TZ_CMD_CLEAR_ALL_STATE_INSTANCES     = 8,
    MVM_TZ_CMD_UPDATE_STATE_NONCE            = 9,
    MVM_TZ_CMD_UPDATE_STATE_BALANCE          = 10,
    MVM_TZ_CMD_CLEAR_XAPIAN_TX_BUFFER        = 11,
    MVM_TZ_CMD_COMMIT_XAPIAN_TX_BUFFER       = 12,
    MVM_TZ_CMD_CLEAR_XAPIAN_TX_BUFFER_BATCH  = 13,
    MVM_TZ_CMD_COMMIT_XAPIAN_TX_BUFFER_BATCH = 14,
    MVM_TZ_CMD_COMMIT_ALL_XAPIAN             = 15,
    MVM_TZ_CMD_REPLAY_FULL_DB_LOGS           = 16,
    /* SetXapianBasePath (mvm_api.go:265) is NOT wired here — in TZ mode
     * Xapian has no filesystem path at all; see the XAPIAN_FILE_* reverse
     * commands below. InitCppFileLog/CloseCppFileLog (logger.go:36,46) are
     * cgo-mode-only and have no TZ equivalent (no TA-side log file). */

    /* ---- Reverse (TA -> Go): 6 live callbacks ----
     * See note/tee_dual_mode_execution_plan.md §2.1 / §3.3. ONE request at
     * a time — the TA blocks on response_ready after posting one of these;
     * the interpreter thread inside the TA is synchronously suspended, same
     * as a cgo call blocks its calling goroutine today. */
    MVM_TZ_RCMD_GLOBAL_STATE_GET                  = 101, /* my_global_state.cpp:81,131,200 */
    MVM_TZ_RCMD_GET_STORAGE_VALUE                 = 102, /* my_storage.cpp:66, xapian_handlers.cpp:89 */
    MVM_TZ_RCMD_EXTENSION_CALL_GET_API             = 103, /* my_extension.cpp:250 — live HTTP GET */
    MVM_TZ_RCMD_EXTENSION_EXTRACT_JSON_FIELD       = 104, /* my_extension.cpp:254 */
    MVM_TZ_RCMD_EXTENSION_BLST                     = 105, /* my_extension.cpp:258 */
    MVM_TZ_RCMD_EXTENSION_GET_OR_CREATE_SIMPLE_DB  = 106, /* my_extension.cpp:523 — B3, reopened, see plan §2.1 */

    /* MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS — NEW (plan §5b, 2026-08-16).
     * TA-initiated, lazy, per-address: issued the first time in the current
     * TA session that a GlobalStateGet/Xapian lookup for `address` comes up
     * empty in the TA's own (freshly-booted, in-memory-only) State/Xapian
     * singleton. Host answers from a small address-indexed store kept
     * alongside the existing per-block BackupDb write
     * (block_processor_commit.go) — see plan §5b for why the existing
     * block-indexed BackupDb alone is NOT enough for this (point lookup by
     * address, not a block-number scan). Response reuses
     * mvm_tz_replay_full_db_logs_req_t's blob shape (see below) — the TA
     * feeds it straight into the same ReplayFullDbLogs() C++ entry point
     * cgo mode already uses for the identical "C++-side cache missing data"
     * situation during node sync (executor/unix_socket_handler_sync.go:1039).
     * Supersedes the 4 MVM_TZ_RCMD_XAPIAN_FILE_* ids this header used to
     * reserve here (201-204, now removed): those assumed Xapian would do
     * real file I/O in the TA, which plan §5's InMemory-backend build
     * (2026-08-16, confirmed working end to end) has ruled out — there is
     * no file to proxy. */
    MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS = 107,
} mvm_tz_cmd_t;

/* Which side currently owns the channel / is expected to act next. */
typedef enum {
    MVM_TZ_DIR_HOST_TO_TA = 0, /* Go host issued a forward command, TA must consume */
    MVM_TZ_DIR_TA_TO_HOST = 1, /* TA issued a reverse call, Go host must consume */
} mvm_tz_direction_t;

/* raw_spinlock, byte-for-byte the same construction as
 * tz-llm/llama.cpp/src/interface.h's raw_spinlock — CAS loop over a plain
 * atomic int, zero libc/OS primitives. Duplicated here (not #included from
 * the other repo) since the two projects are independent; keep the two
 * copies in sync by inspection if either changes. */
typedef struct {
    volatile int32_t locked; /* 0 = unlocked, 1 = locked; accessed via atomic ops by both sides */
} mvm_tz_spinlock_t;

/* Inline CAS spinlock ops — GCC/Clang __atomic builtins, available under
 * both glibc (host) and musl (TA), zero OS/libc primitives (no futex, no
 * pthread). Semantically identical to tz-llm's std::atomic-based
 * raw_spinlock, just expressed in C rather than C++. `static inline` so
 * this header stays link-free (safe to include from both sides without a
 * shared .c/.o). */
static inline void mvm_tz_spinlock_lock(mvm_tz_spinlock_t *lk) {
    int32_t expected;
    do {
        expected = 0;
    } while (!__atomic_compare_exchange_n(&lk->locked, &expected, 1, 0,
                                           __ATOMIC_ACQUIRE, __ATOMIC_RELAXED));
}

static inline void mvm_tz_spinlock_unlock(mvm_tz_spinlock_t *lk) {
    __atomic_store_n(&lk->locked, 0, __ATOMIC_RELEASE);
}

/* Atomic helpers for the request_ready/response_ready flags — plain
 * uint8_t fields accessed via the same __atomic builtins (no _Atomic
 * qualifier needed for GCC/Clang builtins to operate correctly). */
static inline void mvm_tz_flag_set(volatile uint8_t *flag, uint8_t value) {
    __atomic_store_n(flag, value, __ATOMIC_RELEASE);
}

static inline uint8_t mvm_tz_flag_get(volatile uint8_t *flag) {
    return __atomic_load_n(flag, __ATOMIC_ACQUIRE);
}

/* CAS on a flag: succeeds and sets *flag = desired only if *flag ==
 * expected. Mirrors tz-llm ca-backend.cpp's
 * compare_exchange_strong(expected, false) "consume exactly once" pattern
 * (see ca_backend_poll_final_answer). */
static inline int mvm_tz_flag_cas(volatile uint8_t *flag, uint8_t expected, uint8_t desired) {
    uint8_t exp = expected;
    return __atomic_compare_exchange_n(flag, &exp, desired, 0,
                                        __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE);
}

/* ─────────────────────── Control block ──────────────────────────────
 * Lives at the start of the shared page. Everything after it (up to
 * MVM_TZ_BLOB_REGION_SIZE) is the blob region for the CURRENTLY ACTIVE
 * request or response — the two never overlap in time because the
 * channel is strictly request/response, single-flight.
 */
typedef struct {
    uint32_t protocol_version;   /* MVM_TZ_PROTOCOL_VERSION; mismatch = refuse to proceed */
    mvm_tz_spinlock_t lock;      /* guards non-atomic multi-field updates below */

    volatile uint8_t request_ready;  /* atomic bool: host has posted a forward command */
    volatile uint8_t response_ready; /* atomic bool: other side has posted its response */

    mvm_tz_direction_t direction;    /* who must act next */
    mvm_tz_cmd_t cmd;                /* command being carried right now */

    uint64_t seq;                    /* monotonic, debug/tracing only — not used for correctness */

    uint32_t header_len;             /* bytes of the fixed per-command header, at blob_region[0] */
    uint32_t blob_len;                /* bytes of blob-stream payload immediately following the header */

    /* blob_region: header_len bytes of fixed struct (one of the
     * mvm_tz_*_req_t / mvm_tz_*_resp_t types below, selected by `cmd`),
     * followed by blob_len bytes of [uint32_t len][bytes] records. */
    uint8_t blob_region[MVM_TZ_BLOB_REGION_SIZE];
} mvm_tz_channel_t;

/* ═══════════════════ Forward command headers ════════════════════════
 * Fixed part only — calldata/constructor bytes, address arrays etc. live
 * in the blob stream that follows, in the field order documented in each
 * comment.
 */

/* MVM_TZ_CMD_CALL — mirrors mvm_api.go Call()'s parameter list exactly
 * (mvm_api.go:42-61 in engine.go's interface). Blob stream, in order:
 *   [0] bSender (20)  [1] bContractAddress (20)  [2] bInput (calldata)
 *   [3] bTxHash (32)  [4] relatedAddresses (20*N, N from related_count)
 */
typedef struct {
    uint8_t  amount[32];           /* big-endian uint256 */
    uint64_t gas_price;
    uint64_t gas_limit;
    uint64_t block_prevrandao;
    uint64_t block_gas_limit;
    uint64_t block_time;
    uint64_t block_base_fee;
    uint64_t block_number;
    uint8_t  block_coinbase[20];
    uint8_t  mvm_id[20];
    uint8_t  read_only;            /* bool */
    uint8_t  is_debug;             /* bool */
    uint8_t  is_off_chain;         /* bool */
    uint32_t related_addresses_count;
} mvm_tz_call_req_t;

/* MVM_TZ_CMD_EXECUTE — mirrors Execute() (engine.go:63-81). Blob stream:
 *   [0] bSender (20) [1] bContractAddress (20) [2] bInput
 *   [3] bTxHash (32) [4] relatedAddresses (20*N) */
typedef struct {
    uint8_t  amount[32];
    uint64_t gas_price;
    uint64_t gas_limit;
    uint64_t block_prevrandao;
    uint64_t block_gas_limit;
    uint64_t block_time;
    uint64_t block_base_fee;
    uint64_t block_number;
    uint8_t  block_coinbase[20];
    uint8_t  mvm_id[20];
    uint8_t  is_debug;
    uint8_t  is_cache;
    uint32_t related_addresses_count;
} mvm_tz_execute_req_t;

/* MVM_TZ_CMD_DEPLOY — mirrors Deploy() (engine.go:94-111). Blob stream:
 *   [0] bSender (20) [1] bContractConstructor [2] bTxHash (32) */
typedef struct {
    uint8_t  amount[32];
    uint64_t gas_price;
    uint64_t gas_limit;
    uint64_t block_prevrandao;
    uint64_t block_gas_limit;
    uint64_t block_time;
    uint64_t block_base_fee;
    uint64_t block_number;
    uint8_t  block_coinbase[20];
    uint8_t  mvm_id[20];
    uint8_t  is_debug;
    uint8_t  is_cache;
    uint8_t  is_off_chain;
} mvm_tz_deploy_req_t;

/* MVM_TZ_CMD_EXECUTE_BATCH — mirrors ExecuteBatch() (engine.go:83-92).
 * Dead code today (see cmd enum comment); blob stream carries N
 * serialized ExecuteBatchInput records, format TBD if this is ever
 * actually implemented in GĐ2 — deliberately not fleshed out further,
 * per plan's KISS/YAGNI guidance, until a real caller exists. */
typedef struct {
    uint32_t input_count;
    uint64_t block_prevrandao;
    uint64_t block_gas_limit;
    uint64_t block_time;
    uint64_t block_base_fee;
    uint64_t block_number;
    uint8_t  block_coinbase[20];
    uint8_t  mvm_id[20];
} mvm_tz_execute_batch_req_t;

/* MVM_TZ_CMD_SEND_NATIVE — mirrors SendNative() (engine.go:113-127).
 * Blob stream: [0] bSender (20) [1] bContractAddress (20) */
typedef struct {
    uint8_t  amount[32];
    uint64_t gas_price;
    uint64_t gas_limit;
    uint64_t block_prevrandao;
    uint64_t block_gas_limit;
    uint64_t block_time;
    uint64_t block_base_fee;
    uint64_t block_number;
    uint8_t  block_coinbase[20];
    uint8_t  mvm_id[20];
    uint8_t  is_cache;
} mvm_tz_send_native_req_t;

/* MVM_TZ_CMD_PROCESS_NATIVE_MINT_BURN — mirrors ProcessNativeMintBurn()
 * (engine.go:129-144). Blob stream: [0] bFrom (20) [1] bTo (20) */
typedef struct {
    uint8_t  amount[32];
    uint64_t operation_type; /* 0=mint, 1=burn */
    uint64_t gas_price;
    uint64_t gas_limit;
    uint64_t block_prevrandao;
    uint64_t block_gas_limit;
    uint64_t block_time;
    uint64_t block_base_fee;
    uint64_t block_number;
    uint8_t  block_coinbase[20];
    uint8_t  mvm_id[20];
    uint8_t  is_cache;
} mvm_tz_process_native_mint_burn_req_t;

/* MVM_TZ_CMD_NONCE_PLUS_ONE — mirrors NoncePlusOne() (engine.go:146-158).
 * Blob stream: [0] bSender (20) */
typedef struct {
    uint64_t gas_price;
    uint64_t gas_limit;
    uint64_t block_prevrandao;
    uint64_t block_gas_limit;
    uint64_t block_time;
    uint64_t block_base_fee;
    uint64_t block_number;
    uint8_t  block_coinbase[20];
    uint8_t  mvm_id[20];
    uint8_t  is_cache;
} mvm_tz_nonce_plus_one_req_t;

/* ═══════════════ Forward command response: ExecuteResult ═════════════
 * Shared by CALL / EXECUTE / DEPLOY / SEND_NATIVE /
 * PROCESS_NATIVE_MINT_BURN / NONCE_PLUS_ONE.
 *
 * IMPORTANT — this is NOT a byte-for-byte mirror of C++ `struct
 * ExecuteResult` (linker/include/mvm_linker.hpp:17-72). The Go side never
 * hands out that raw struct: `helpers.go`'s extractExecuteResult() already
 * converts it into `MVMExecuteResult` (pkg/mvm/types.go), a map-based Go
 * type keyed by hex address string, BEFORE any wire-protocol code ever
 * sees it (GĐ2's loopback engine calls the real *MVMApi and gets this Go
 * type back). This wire format therefore serializes MVMExecuteResult
 * directly, with its own clean per-entry layout chosen for this protocol
 * — NOT a copy of the C++ struct's internal padding quirks (which are
 * inconsistent between fields today: e.g. storage_change's length
 * excludes its address prefix while storage_read's includes it — an
 * accident of the current cgo implementation, not a contract worth
 * perpetuating over a new wire).
 *
 * Per-entry formats used in the blob stream below (all addresses are
 * plain 20 bytes, no 32-byte zero-padding):
 *   full_db_hash / add_balance_change / nonce_change / sub_balance_change:
 *     [20-byte address][32-byte value]                         (52 bytes)
 *   full_db_logs / code_change:
 *     [20-byte address][uint32_t data_len][data_len bytes]
 *   storage_change:
 *     [20-byte address][uint32_t pair_count][pair_count * (32-byte key + 32-byte value)]
 *   storage_read:
 *     [20-byte address][uint32_t key_count][key_count * 32-byte key]
 *   public_key_bls / new_device_key:
 *     [20-byte address][uint32_t data_len][data_len bytes]
 *   account_type:
 *     [20-byte address][1-byte type]
 *
 * public_key_bls_count / account_type_count / new_device_key_count /
 * has_simple_db_hash exist for MVMExecuteResult interface parity
 * (MapPublicKeyBls/MapAccountType/MapNewDeviceKey/SimpleDbHash,
 * types.go:36-46) — confirmed 2026-08-15 that NOTHING in pkg/mvm
 * populates these today (dead struct surface), so they are always
 * 0/empty in practice right now. Included anyway so the wire format
 * doesn't need a version bump the day something starts setting them.
 *
 * Blob stream order (do not reorder without bumping
 * MVM_TZ_PROTOCOL_VERSION):
 *   [0] exmsg
 *   [1] output
 *   [2..) full_db_hash entries        (full_db_hash_count)
 *   [..)  full_db_logs entries        (full_db_logs_count)
 *   [..)  add_balance_change entries  (add_balance_change_count)
 *   [..)  nonce_change entries        (nonce_change_count)
 *   [..)  sub_balance_change entries  (sub_balance_change_count)
 *   [..)  code_change entries         (code_change_count)
 *   [..)  storage_change entries      (storage_change_count)
 *   [..)  storage_read entries        (storage_read_count)
 *   [..)  public_key_bls entries      (public_key_bls_count)
 *   [..)  account_type entries        (account_type_count)
 *   [..)  new_device_key entries      (new_device_key_count)
 *   [..]  simple_db_hash (32 bytes, only if has_simple_db_hash != 0)
 *   [..]  event_logs_json
 *   [last] native_logs (packed [flag][len][msg] blob, see MyLogger)
 */
typedef struct {
    uint8_t  status;         /* pb.RECEIPT_STATUS, from MVMExecuteResult.Status */
    uint8_t  exception;      /* pb.EXCEPTION, from MVMExecuteResult.Exception */
    uint8_t  has_simple_db_hash;
    uint64_t gas_used;

    uint32_t full_db_hash_count;
    uint32_t full_db_logs_count;
    uint32_t add_balance_change_count;
    uint32_t nonce_change_count;
    uint32_t sub_balance_change_count;
    uint32_t code_change_count;
    uint32_t storage_change_count;
    uint32_t storage_read_count;
    uint32_t public_key_bls_count;
    uint32_t account_type_count;
    uint32_t new_device_key_count;
} mvm_tz_execute_result_hdr_t;

/* ═══════════════════ Lifecycle command headers ═══════════════════════ */

/* MVM_TZ_CMD_CLEAR_ALL_STATE_INSTANCES — no payload. */

typedef struct {
    uint8_t  address[20];
    uint64_t nonce;
} mvm_tz_update_state_nonce_req_t;

typedef struct {
    uint8_t address[20];
    uint8_t balance[32];
} mvm_tz_update_state_balance_req_t;

typedef struct {
    uint8_t tx_hash[32];
} mvm_tz_xapian_tx_buffer_req_t; /* CLEAR_XAPIAN_TX_BUFFER / COMMIT_XAPIAN_TX_BUFFER */

typedef struct {
    uint32_t tx_hash_count; /* blob stream: tx_hash_count * 32-byte hashes, back to back */
} mvm_tz_xapian_tx_buffer_batch_req_t; /* CLEAR_.._BATCH / COMMIT_.._BATCH */

/* MVM_TZ_CMD_COMMIT_ALL_XAPIAN — no payload. */

/* Dual use (plan §5b): as MVM_TZ_CMD_REPLAY_FULL_DB_LOGS's forward-command
 * payload (Host proactively pushing N entries — e.g. bulk seeding, not yet
 * implemented in the Go codec/loopback as of 2026-08-16), AND as
 * MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS's reverse-command RESPONSE payload
 * (Host answering a single lazy per-address pull with 0 or 1 entries). Same
 * shape either way because both feed the identical TA-side
 * ReplayFullDbLogs() call — reusing one struct means that TA-side code path
 * does not need to care which direction produced the entries. */
typedef struct {
    uint32_t entry_count; /* blob stream: entry_count records, each
                            * [20-byte address][32-bit log_len][log bytes],
                            * matching LogReplayEntryC (mvm_linker.hpp:7-12) */
} mvm_tz_replay_full_db_logs_req_t;

/* MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS request (TA -> Host) — no blob
 * stream, just the address being asked about. */
typedef struct {
    uint8_t address[20];
} mvm_tz_get_latest_full_db_logs_req_t;

/* ═══════════════════ Reverse command headers ═════════════════════════
 * TA -> Go. The TA posts one of these with direction = TA_TO_HOST and
 * blocks on response_ready; Go answers synchronously — same blocking
 * shape as today's cgo call, just across the shared page instead of a
 * function call. See note/tee_dual_mode_execution_plan.md §4 point 5 for
 * why this is safe under the chosen serialized-session model.
 */

typedef struct {
    uint8_t mvm_id[20];
    uint8_t address[20];
} mvm_tz_global_state_get_req_t;

/* status: 0 = not found (create fresh) / 1 = found / 2 = addressNotInRelated
 * / 3 = Block-STM suspend (ErrEstimateHit) — SAME semantics as today's
 * GlobalStateGet_return.status (my_global_state.cpp:83-116). The TA MUST
 * throw immediately on status==3, exactly as my_global_state.cpp does
 * today — see plan's confirmed finding: the actual "please retry" signal
 * is a Go-side-only side effect (mvccDB.BlockingVersion) set BEFORE this
 * response is even written, so the TA does not need to do anything
 * special beyond replicating today's unwind. Blob stream (status==1 only):
 *   [0] balance (32) [1] nonce (32) [2] code */
typedef struct {
    int32_t status;
} mvm_tz_global_state_get_resp_t;

typedef struct {
    uint8_t mvm_id[20];
    uint8_t address[20];
    uint8_t key[32];
} mvm_tz_get_storage_value_req_t;

/* status: STORAGE_SUCCESS=0 / STORAGE_NOT_FOUND=1 / STORAGE_SUSPEND=2,
 * same as today's GetStorageValue_return.status (mvm_linker.hpp:226-235).
 * status==2 unwinds exactly like GlobalStateGet's status==3 above — same
 * side-channel reasoning applies. Blob stream (status==0 only): [0] value (32) */
typedef struct {
    int32_t status;
} mvm_tz_get_storage_value_resp_t;

/* MVM_TZ_RCMD_EXTENSION_CALL_GET_API /
 * MVM_TZ_RCMD_EXTENSION_EXTRACT_JSON_FIELD / MVM_TZ_RCMD_EXTENSION_BLST —
 * all three share this shape (bytes-in, bytes-out), matching their
 * identical C signature `Extension_return Fn(unsigned char*, int)`
 * (mvm_linker.hpp:243-245). No fixed header beyond the blob stream itself:
 *   request blob:  [0] input bytes
 *   response blob: [0] output bytes (empty = the Extension_return{nullptr,0}
 *                  failure case, e.g. ExtensionCallGetApi's HTTP error path) */

/* MVM_TZ_RCMD_EXTENSION_GET_OR_CREATE_SIMPLE_DB — matches
 * ExtensionGetOrCreateSimpleDb(unsigned char*, int, unsigned char*
 * address, unsigned char* mvmId) (mvm_linker.hpp:246-249). Reopened B3
 * (plan §2.1): no Merkle-witness needed since the TA trusts the host as
 * fully as it does today. Request blob: [0] input bytes (ABI-encoded
 * dispatch code + args — GET_OR_CREATE_SIMPLE_DB/SET/GET/GET_ALL/
 * SEARCH_BY_VALUE/SINPLE_DB_DELETE/SINPLE_GET_NEXT_KEYS, see
 * linker/include/my_extension/constants.h — dispatch stays encoded inside
 * the payload exactly as it is today, this protocol does not need to
 * understand it). Response blob: [0] output bytes. */
typedef struct {
    uint8_t address[20];
    uint8_t mvm_id[20];
} mvm_tz_get_or_create_simple_db_req_t;

/* Xapian file I/O proxy commands (previously reserved here as reverse ids
 * 201-204) were removed 2026-08-16: they assumed Xapian would need real
 * file I/O inside the TA. Plan §5's InMemory-backend build (confirmed
 * working end to end, same day) ruled that out — there is no file to
 * proxy. Xapian's actual TA persistence need (surviving a TA restart) is
 * instead handled by MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS above, reusing the
 * same full_db_logs mechanism cgo mode already relies on for node sync. */
