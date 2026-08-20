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
#include <ctime>
#include <cerrno>
#include <pthread.h>
#include <atomic>
#include <vector>

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
// Confirmed root cause, live on hardware, 2026-08-18: two INDEPENDENT,
// confirmed race conditions can silently and PERMANENTLY swallow this
// TA's push_pages() SMC request during early boot, each losing the
// parked thread's pointer forever with no way for anyone -- TA-side or
// driver-side -- to resume it (see plan doc §9.13 for the full
// evidence trail):
//   1. kernel/arch/aarch64/trustzone/spd/opteed/smc.c's
//      sys_tee_switch_req() has a per-CPU bool not_first_smc[cpu] gate:
//      the FIRST-EVER call on a given CPU is unconditionally consumed as
//      a one-time "CPU entry done" boot handshake, discarding whatever
//      real payload the caller sent.
//   2. tzdriver/core/tc_client_driver.c's llm_tee_os_init() (Linux
//      kernel module_init time) runs its OWN, completely separate
//      boot-time SHM-handshake probe loop that only checks
//      out.ret==SMC_EXIT_PREEMPTED and otherwise discards ANY other
//      response (including a genuine SMC_EXIT_SHADOW meant for this TA)
//      without reading out.target/exit_reason at all.
//
// A prior fix attempt (single/double/24x "priming" calls before the
// real request, then a wall-clock-bounded wait) was tried and RETRACTED
// after finding, live, that it doesn't reduce risk -- it multiplies it:
// EVERY individual SMC call (priming or real) has its own independent
// chance of being swallowed by either race, so issuing more of them only
// increases the cumulative probability that at least one gets
// permanently stuck. Confirmed live: the 24x-priming loop itself got
// stuck mid-loop on one of its own priming calls, hanging the whole TA.
//
// REDESIGNED (2026-08-19, see plan doc §9.18): no priming calls at all
// -- instead, mvm_push_pages_resilient() below runs each push_pages()
// ATTEMPT on its own fresh, disposable pthread, watched by THIS thread
// with a bounded wall-clock timeout. If an attempt's worker doesn't
// report done within the timeout, it's presumed swallowed: abandon that
// worker (deliberately leaked, detached, whatever result struct it might
// still eventually write to left allocated) and retry on a brand new
// thread. This works BECAUSE both hazards are bounded and resolve
// themselves given enough real elapsed time regardless of what mvm_ta
// does (chanmgr's own idle threads eventually all run; llm_tee_os_init()
// gives up after its own ~15s/300-attempt cap) -- so each successive
// retry, spaced apart by real wall-clock time via its own watchdog
// window, has a progressively higher chance of landing after both races
// have already resolved on their own. Doesn't touch tc_client_driver.c/
// opteed/smc.c/chanmgr's own code at all -- stays entirely inside this
// TA's own scope, per the project's standing separation requirement.
static int mvm_push_pages(unsigned long size, int cma_index) {
    struct smc_registers req = {0};
    req.x1 = SMC_EXIT_SHADOW;
    req.x2 = 1;
    req.x3 = ROUND_UP(size, PAGE_SIZE) | (unsigned long)cma_index;
    fprintf(stderr, "[mvm_ta] mvm_push_pages: about to call usys_tee_switch_req "
        "(x1=%#lx x2=%#lx x3=%#lx)\n", req.x1, req.x2, req.x3);
    fflush(stderr);
    int ret = (int)usys_tee_switch_req(&req);
    fprintf(stderr, "[mvm_ta] mvm_push_pages: usys_tee_switch_req returned %d (%#x)\n",
        ret, (unsigned int)ret);
    fflush(stderr);
    return ret;
}

// STRATEGIC RE-EVALUATION (2026-08-19, plan doc §9.20): everything below
// replaces the watchdog+retry-with-fresh-thread machinery from §9.18/
// §9.19 (24x priming, 20x retries, nested 30x meta-verify loops). That
// approach worked in principle but kept growing in complexity chasing
// symptoms, and its OWN complexity produced a new, unexplained bug live
// (the watchdog itself failed to time out as designed after ~10 spawned
// threads had accumulated) -- a sign of solving the wrong problem: making
// individual swallow-prone SMC calls resilient, rather than simply not
// making them during the swallow-prone window at all.
//
// SECOND RE-EVALUATION (2026-08-19, same day, plan §9.21): the first pass
// below still had a "wait a fixed number of seconds, then give up loudly"
// shape (kMaxWaitSec: 90 -> observed insufficient on hardware -> bumped
// to 240 with no better justification than "bigger guess"). That is
// exactly the class of bug it looks like: nothing in this system --
// neither this TA nor anything documented about chanmgr/Normal-World
// boot timing -- gives an actual upper bound on how long it takes before
// the first real Normal-World-initiated yielding SMC happens. Any
// concrete number here is a guess about board/load conditions on THIS
// test run, not a fact. Picking a bigger guess after the first one fails
// just delays the same failure, it doesn't fix it.
//
// Corrected principle: NEVER treat elapsed wall-clock time alone as
// grounds to declare failure. Only two kinds of signal are trustworthy:
// (a) a VERIFIED positive result (the specific meta entry this push
// claims to own actually has the right size -- plan §9.19), or (b) an
// explicit negative result from the platform (a real error code, not a
// timeout). Elapsed time may only ever be used to decide "try again" /
// "log a heartbeat so this is diagnosable", never "give up". Below:
// - mvm_wait_for_boot_settled() polls forever (safe: issues zero SMCs of
//   its own, so nothing it does can be swallowed) until the meta array
//   write lands, with only a throttled heartbeat log, no cap.
// - mvm_push_pages_resilient() attempts push_pages() with a fresh
//   disposable worker+watchdog on every apparent stall/swallow, retrying
//   indefinitely until the entry is independently verified -- a
//   watchdog "timeout" here only ever means "try again", never "abort".
//
// Root cause recap (plan §9.13): TWO independent races can permanently
// swallow an SMC this TA sends during early boot --
// opteed/smc.c's per-CPU not_first_smc handshake, and tzdriver's
// llm_tee_os_init() boot-time SHM-handshake probe loop (Linux kernel
// module_init time, bounded: up to 300 attempts / ~50ms apart / ~15s
// worst case, THEN IT EXITS FOR GOOD). Both are one-shot, self-resolving
// boot-time phenomena -- neither recurs once its own window has passed.
// Confirmed independently: llama-cli's own main() (examples/main/
// main.cpp) blocks on usys_tee_wait_switch_req() for a REAL CA-sent task
// before it ever touches TZASC for tensor data -- i.e. llm_tee_os_init()
// exists specifically to deliver THAT handshake reliably, and its whole
// probe loop is done and gone long before any real inference could ever
// be requested. There is no rush to push before some fixed "boot is
// still forming" instant; there is only a rush to push before llama-cli
// ITSELF starts loading tensors into the same cma_index range (plan
// §9.5), which requires a real external trigger this test setup doesn't
// send automatically.
//
// New design: WAIT (safely, no SMC at all -- see
// mvm_wait_for_boot_settled() below) until both hazards' windows have
// almost certainly closed, THEN issue exactly ONE push_pages() attempt,
// watched by exactly ONE disposable thread (kept, not removed --
// converts "silently parked forever" into a loud, diagnosable FATAL
// instead of a silent hang; still cheaper and far simpler than the
// retry-with-fresh-thread version since it's no longer the PRIMARY
// mechanism, just a safety net for an attempt expected to succeed).

// Shared between the single push_pages() attempt's worker thread and the
// watchdog that spawns it. Heap-allocated and, if the attempt times out,
// DELIBERATELY never freed -- the worker may still be parked and could
// still write to it arbitrarily far in the future; freeing it then would
// be a use-after-free.
struct mvm_push_attempt {
    std::atomic<bool> done{false};
    int entry_index = -1;
};

static void *mvm_push_worker(void *arg) {
    mvm_push_attempt *att = (mvm_push_attempt *)arg;
    int idx = mvm_push_pages(MVM_CHANNEL_SIZE, MVM_TZASC_CMA_INDEX);
    att->entry_index = idx;
    att->done.store(true, std::memory_order_release);
    return nullptr;
}

// Safe wait: no SMC issued by this TA at all, so nothing here can be
// swallowed -- just cheap, yield-paced polling of
// usys_map_tzasc_cma_meta() (a plain secure-world-internal syscall,
// proven earlier this session to correctly return -EAGAIN vs 0 without
// any swallow risk of its own). Waits for that global write to land
// (proof SOME Normal-World-initiated yielding SMC has happened -- in
// practice, chanmgr's own idle-thread priming, independent of anything
// this TA does). NO CAP: this board has been observed to take highly
// variable, unbounded-in-practice time to reach that point (14s in one
// boot, still not landed after 240s in another, for reasons entirely
// outside this TA -- whatever Normal-World activity happens to trigger
// the first yielding SMC). Since polling here is provably zero-risk,
// there is no safety reason to ever give up -- only a throttled
// heartbeat so a long wait is diagnosable on UART instead of silent.
// PRIORITY FIX (2026-08-19, plan §9.22): root-caused live via UART + kernel
// source reading after 3 consecutive clean (correct-baud, verified non-stale)
// boots each stalled 25-30+ minutes with ZERO Normal-World boot text ever
// appearing, far beyond anything seen before. chanmgr's OWN "idle" threads
// (kernel/sched/sched.c's per-CPU TYPE_IDLE/TYPE_SHADOW slots, both created
// at IDLE_PRIO=MIN_PRIO=0 -- kernel/include/sched/sched.h) are what actually
// hand control to Normal World: chanmgr/main.c's idle() thread body lowers
// itself to prio 1 via usys_set_prio(0, 1) then loops calling the real
// blocking usys_tee_switch_req() -- THAT loop is what lets Normal World run.
// pbrr (kernel/sched/policy_pbrr.c) is priority-based: it always prefers any
// higher-priority ready thread over a lower one. This function's own poll
// loop was running at chcore-libc's DEFAULT_PRIO=10 (never explicitly set),
// so on whichever CPU it landed on (kernel's own throttled kinfo print
// showed it pinned to "CPU 0" every single time), it perpetually outranked
// and starved that CPU's priority-1 idle/SMC-yield thread -- silently
// preventing Normal World from ever getting scheduled there at all. This
// matches an ALREADY-DOCUMENTED finding from earlier this exact session
// (plan §9.11 point 3: "spin nhanh ... có thể tự chặn Normal World ... một
// dạng bế tắc tự gây ra") that this redesign accidentally reintroduced by
// removing the old cap without also addressing the underlying starvation.
// Fix: lower this thread's OWN priority to 1 (matching chanmgr's idle()
// threads exactly -- proven-safe, extensively used elsewhere in this same
// codebase, e.g. chcore-libc's own futex.c prio_spin_lock()) for the
// duration of this passive wait, restoring the prior priority once landed
// so the real work in mvm_push_pages_resilient() below gets normal
// scheduling again. usys_set_prio(0, prio): cap=0 always means "self".
static void mvm_wait_for_boot_settled(unsigned long meta_vaddr) {
    const long kHeartbeatIntervalSec = 20; // diagnostic only -- never a give-up threshold
    // ROUND 6 RETRACTED (2026-08-19, plan §9.22): tried usys_tee_wait_switch_req()
    // for pacing, on the theory that it's the same primitive llama-cli's own
    // main() already uses successfully. CONFIRMED FATAL live on hardware,
    // immediately, first boot: `usys_tee_wait_switch_req` has a per-CPU
    // SINGLE-WAITER slot (kernel `BUG: sys_tee_wait_switch_req:142 on (expr)
    // percpu->waiting_thread`) -- the instant llama-cli's own process reached
    // its own "SHM INIT: Main thread is waiting for smc" call on the SAME CPU
    // mvm_ta was already parked on, the kernel hit a hard BUG_ON and the
    // whole board froze (UART output stopped completely, required a fresh
    // flash to recover). This is strictly worse than every prior round's
    // "silently slow" symptom -- a genuine crash, not just a stall. This
    // primitive is confirmed UNSAFE for mvm_ta to use at all while llama-cli
    // (or anything else) might concurrently hold or want the same per-CPU
    // slot -- do not retry this without first fixing the shared kernel code
    // itself (out of scope, see the project's own separation requirement).
    //
    // Reverting to round 5: plain usys_set_prio(0, 1) once, matching
    // chanmgr's own idle-thread pattern exactly, then a plain usys_yield()
    // spin -- the only variant actually PROVEN, repeatedly, live, to do no
    // harm (5/5 clean boots let Linux boot in ~13s). This round's only
    // change from round 5 is patience: give it a genuinely long,
    // uninterrupted run this time instead of abandoning it early again.
    //
    // In hindsight this exact hazard was ALREADY documented elsewhere in
    // this same file before round 6 ever happened -- mvm_reverse_round_trip's
    // and mvm_ta_run's own design-note comments below explicitly call out
    // "the shared per-CPU SMC rendezvous that llama.cpp's own main.cpp wait
    // loop also uses" as a "previously-hardware-crash-causing hazard" and
    // deliberately busy-poll instead for exactly that reason. Round 6 should
    // have cross-referenced that before ever trying usys_tee_wait_switch_req()
    // here -- noting it now so this mistake isn't repeated a third time.
    usys_set_prio(0, 1);
    struct timespec t_start, t_now;
    clock_gettime(CLOCK_MONOTONIC, &t_start);
    long last_heartbeat_s = 0;
    for (;;) {
        if (usys_map_tzasc_cma_meta(meta_vaddr) == 0) {
            clock_gettime(CLOCK_MONOTONIC, &t_now);
            fprintf(stderr, "[mvm_ta] mvm_wait_for_boot_settled: meta write "
                "landed after %lds\n", t_now.tv_sec - t_start.tv_sec);
            fflush(stderr);
            return;
        }
        clock_gettime(CLOCK_MONOTONIC, &t_now);
        long elapsed_s = t_now.tv_sec - t_start.tv_sec;
        if (elapsed_s - last_heartbeat_s >= kHeartbeatIntervalSec) {
            last_heartbeat_s = elapsed_s;
            fprintf(stderr, "[mvm_ta] mvm_wait_for_boot_settled: still waiting "
                "for meta write to land (%lds so far, no cap -- this is "
                "expected to be slow and variable, not stuck)\n", elapsed_s);
            fflush(stderr);
        }
        usys_yield();
    }
}

// Attempt push_pages(), retried with a fresh disposable worker+watchdog on
// every apparent stall, until independently verified successful. A
// watchdog "timeout" here means only "this specific attempt is probably
// swallowed or abnormally slow -- try again", NEVER "give up": there is
// no known bound on how many attempts this might take, so none is
// assumed (plan §9.21). This is safe to retry indefinitely because, per
// plan §9.13, a genuinely swallowed attempt has NO server-side effect
// (the whole nature of both known races is that the request is discarded
// -- treated as an unrelated boot handshake -- before it ever reaches the
// real push_pages logic), so retrying cannot double-count or corrupt
// anything. Worst case, a merely-slow (not actually swallowed) abandoned
// attempt also eventually lands later and just consumes one otherwise-
// unused meta-array slot -- harmless.
static int mvm_push_pages_resilient(unsigned long meta_vaddr) {
    const long kRetryPaceSec = 15; // how long to wait on ONE attempt before trying a
                                    // fresh one -- NOT a give-up bound. Too short just
                                    // means occasional redundant (harmless) retries.
    const int kMetaVerifyAttempts = 20; // quick, cheap yield-paced checks -- once a push
                                         // has genuinely landed the write is visible
                                         // essentially immediately (plan §9.12); this
                                         // paces for memory-visibility, not boot timing.

    struct tzasc_cma_meta *meta_arr_check = (struct tzasc_cma_meta *)meta_vaddr;
    // This function's own loops below use plain usys_yield() at whatever
    // priority is current, NOT usys_tee_wait_switch_req() -- see the
    // "NOT usys_tee_wait_switch_req() here, deliberately" comment at the
    // watchdog loop below for why (confirmed live: that primitive triggers
    // a fatal kernel BUG_ON, "percpu->waiting_thread", when two threads
    // -- ours and llama-cli's -- try to wait on it concurrently on the
    // same CPU; see mvm_wait_for_boot_settled()'s own retraction comment
    // for the full incident). By the time this function runs, boot has
    // already settled (mvm_wait_for_boot_settled() already returned), so
    // the Normal-World-starvation risk that motivates priority-1 usys_yield()
    // in THAT function is much smaller here -- this function does not
    // lower its own priority.

    for (long attempt_num = 1; ; attempt_num++) {
        mvm_push_attempt *att = new mvm_push_attempt();
        pthread_t worker;
        if (pthread_create(&worker, nullptr, mvm_push_worker, att) != 0) {
            fprintf(stderr, "[mvm_ta] FATAL: pthread_create(push worker) failed\n");
            abort();
        }
        pthread_detach(worker);
        fprintf(stderr, "[mvm_ta] push_pages: attempt #%ld started\n", attempt_num);
        fflush(stderr);

        // NOT usys_tee_wait_switch_req() here, deliberately: `worker` above
        // has its OWN outstanding usys_tee_switch_req() (a send-then-block
        // call) in flight for the real push_pages() attempt. If this
        // watchdog thread also parked in the RECEIVE-only wait, it could
        // consume the response meant for the worker's own pending call
        // (plan §9.13's exact swallow-race shape, but between our own two
        // threads this time) -- silently orphaning a worker that would
        // otherwise have succeeded. This loop only needs to pace checking a
        // purely local atomic flag, so plain usys_yield() is correct here;
        // by this point Linux has already booted successfully (this
        // function only runs after mvm_wait_for_boot_settled() returns), so
        // the earlier boot-starvation risk that motivated round 6 for THAT
        // function does not apply to this one the same way.
        struct timespec t_start, t_now;
        clock_gettime(CLOCK_MONOTONIC, &t_start);
        bool completed = false;
        for (;;) {
            if (att->done.load(std::memory_order_acquire)) {
                completed = true;
                break;
            }
            clock_gettime(CLOCK_MONOTONIC, &t_now);
            if (t_now.tv_sec - t_start.tv_sec >= kRetryPaceSec) {
                break; // stop waiting on THIS attempt and try a fresh one -- not a failure
            }
            usys_yield();
        }

        if (!completed) {
            // `att` and its worker thread are deliberately abandoned (leaked), not
            // freed/joined -- see the struct's own comment for why. If this attempt
            // wasn't actually swallowed, just slow, it may still complete later;
            // per this function's own header comment that's harmless.
            fprintf(stderr, "[mvm_ta] push_pages: attempt #%ld did not complete "
                "within %lds, retrying with a fresh attempt (not a failure)\n",
                attempt_num, kRetryPaceSec);
            fflush(stderr);
            continue;
        }

        int idx = att->entry_index;
        if (idx < 0) {
            fprintf(stderr, "[mvm_ta] push_pages: attempt #%ld returned error %d, "
                "retrying\n", attempt_num, idx);
            fflush(stderr);
            continue;
        }

        // Verify it's genuine (plan §9.19: entry_index alone, or even a successful
        // array-level map, are BOTH insufficient proof on their own -- only the
        // specific entry's own .size field is conclusive proof this exact push
        // actually landed).
        bool verified = false;
        for (int mattempt = 0; mattempt < kMetaVerifyAttempts; mattempt++) {
            if (usys_map_tzasc_cma_meta(meta_vaddr) == 0
                && meta_arr_check[MVM_TZASC_CMA_INDEX].entry[idx].size >= MVM_CHANNEL_SIZE) {
                verified = true;
                break;
            }
            usys_yield();
        }

        if (!verified) {
            fprintf(stderr, "[mvm_ta] push_pages: attempt #%ld reported "
                "entry_index=%d but entry.size never verified after %d tries -- "
                "fake success (plan §9.19), retrying with a fresh attempt\n",
                attempt_num, idx, kMetaVerifyAttempts);
            fflush(stderr);
            continue;
        }

        if (idx != 0) {
            // Not fatal: correctness only needs THIS verified slot, not index 0
            // specifically. A nonzero index just means something else (e.g. real
            // tensor loading, since MVM_TZASC_CMA_INDEX collides with the model-
            // tensor cycling range -- plan §9.5) already used this cma_index's
            // slot 0 before this attempt landed.
            fprintf(stderr, "[mvm_ta] push_pages: WARNING attempt #%ld succeeded "
                "at entry_index=%d, not the usual 0 -- proceeding, this specific "
                "entry is independently verified genuine\n", attempt_num, idx);
            fflush(stderr);
        }

        fprintf(stderr, "[mvm_ta] push_pages succeeded on attempt #%ld "
            "(entry_index=%d, entry.size verified)\n", attempt_num, idx);
        fflush(stderr);
        return idx; // `att` deliberately left allocated -- harmless, tiny, one-time
    }
}

// Reserve + map metanode's dedicated shared channel. entry_index is
// USUALLY 0 (see plan §9.5 — read directly from
// tzasc_cma_push_pages_with_index()'s kernel source: entry_index =
// g_tzasc_cma_meta->count, pre-increment, per cma_index) as long as this
// runs before llama-cli's OWN tensor-loading pipeline ever touches
// MVM_TZASC_CMA_INDEX -- confirmed NOT to require racing to be the
// literal first SMC of the whole boot (see the retraction trail in plan
// doc §9.9-§9.19 for the long way this was found out): llama-cli's own
// main() blocks on a real CA-sent task before it ever loads tensor data,
// so metanode's channel setup has the entire early-boot window, not a
// fixed instant, to complete safely in. It is no longer treated as a hard
// requirement, though (plan §9.21) -- see mvm_push_pages_resilient()'s own
// handling of a nonzero index. See mvm_wait_for_boot_settled() and
// mvm_push_pages_resilient() above for the current (2026-08-19, plan
// §9.21) mechanism: wait, with no time cap, using only safe non-SMC
// polling, for proof Normal World has started; THEN attempt the real push,
// retried with a fresh disposable thread on every apparent stall, until
// independently verified successful -- no step in this path ever gives up
// based on elapsed time alone.
static void mvm_channel_init(void) {
    unsigned long meta_vaddr = chcore_alloc_vaddr(PAGE_SIZE << 10);
    if (meta_vaddr == 0) {
        fprintf(stderr, "[mvm_ta] FATAL: chcore_alloc_vaddr(meta) failed\n");
        abort();
    }
    struct tzasc_cma_meta *meta_arr = (struct tzasc_cma_meta *)meta_vaddr;
    mvm_wait_for_boot_settled(meta_vaddr);
    int entry_index = mvm_push_pages_resilient(meta_vaddr);
    // By this point mvm_push_pages_resilient() has already internally
    // confirmed usys_map_tzasc_cma_meta(meta_vaddr) == 0 and the specific
    // winning entry's .size for this exact entry_index -- no separate
    // retry block needed here anymore.

    // Defensive check, added 2026-08-18 after finding this exact failure
    // mode escape undetected on the TA side: with the old (backwards)
    // map/push order, push_pages() returned a "successful" entry_index=0
    // while the kernel's own CMA metadata entry silently carried
    // size==0, only ever surfacing later as an opaque mmap(EINVAL) on
    // the CA side (dmesg: "entry->size=0"). Catch it here instead,
    // immediately and loudly, so a regression of this kind fails at the
    // TA's own boot rather than requiring a separate CA-side repro.
    unsigned long entry_size = meta_arr[MVM_TZASC_CMA_INDEX].entry[entry_index].size;
    if (entry_size < MVM_CHANNEL_SIZE) {
        fprintf(stderr,
            "[mvm_ta] FATAL: push_pages entry_index=%d has size=%#lx, want "
            ">= %#lx — CMA metadata wasn't actually populated (regression "
            "of the 2026-08-18 map/push ordering fix?)\n",
            entry_index, entry_size, (unsigned long)MVM_CHANNEL_SIZE);
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

    // 2026-08-20 (plan §9.26 follow-up): bracketing diagnostics for the
    // NULL ptr crash investigation -- cheap, UART-only (no file I/O per
    // CLAUDE.md), left in deliberately loud so a future run can grep for
    // exactly which reverse-call round trip was in flight (or never even
    // started) when the fault hit.
    fprintf(stderr, "[mvm_ta][DIAG] round_trip ENTER cmd=%d req_header_len=%u req_blob_len=%u\n",
        cmd, req_header_len, req_blob_len);
    fflush(stderr);

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

    fprintf(stderr, "[mvm_ta][DIAG] round_trip EXIT cmd=%d resp_header_len=%u resp_blob_len=%u\n",
        cmd, hlen, *resp_blob_len_out);
    fflush(stderr);
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
    fprintf(stderr, "[mvm_ta][DIAG] GlobalStateGet RETURN status=%d code_length=%d code_p=%p balance_p=%p nonce=%p\n",
        ret.status, ret.code_length, (void *)ret.code_p, (void *)ret.balance_p, (void *)ret.nonce);
    fflush(stderr);
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
    fprintf(stderr, "[mvm_ta][DIAG] GetStorageValue RETURN status=%d value=%p\n",
        ret.status, (void *)ret.value);
    fflush(stderr);
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

// ─── MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS (plan §5b, 2026-08-16; wired up
// 2026-08-20, plan §9.28) ───
//
// TA-initiated, lazy, per-address pull of previously-saved full_db_logs —
// the TZ-mode counterpart of what cgo mode already does via
// mvm.CallReplayFullDbLogs during node sync (executor/
// unix_socket_handler_sync.go:1039), just reached over the reverse-
// callback channel instead of a direct Go call. Not one of the "6 live
// reverse-callback shims" above because it has no direct 1:1 cgo-callback
// equivalent — it's a plain internal helper (no extern "C"/no existing
// libmvm_linker.a call site expects this exact name), callable whenever
// TA-side code decides a given address's Xapian-backed full-database state
// might be stale/missing for this (freshly-booted, in-memory-only) TA
// session.
//
// NOT YET auto-triggered from any interpreter code path — deciding exactly
// where ("the first time a Xapian lookup for `address` comes up empty",
// per the protocol header's own doc comment) requires tracing
// xapian_handlers.cpp's actual DB-open/lookup call sites plus a
// per-session "already fetched for this address" dedupe (to avoid a
// reverse round trip on every single touch), which is real, separate
// design work — see plan doc §9.28's "Việc tiếp theo". This function is
// the reverse-call mechanics half only: issue the round trip, decode the
// response, feed it into ReplayFullDbLogs(). Returns ReplayFullDbLogs's
// own return value (nonzero = success per mvm_linker.cpp:1367), or 1
// (no-op success, nothing to do) if the Host had zero entries for this
// address — not a failure, just "nothing new since last time".
static int mvm_fetch_and_replay_full_db_logs(const unsigned char address[20]) {
    mvm_tz_get_latest_full_db_logs_req_t req = {0};
    memcpy(req.address, address, 20);

    // Response reuses mvm_tz_replay_full_db_logs_req_t's shape (see that
    // struct's own doc comment in mvm_tz_protocol.h for why one struct
    // serves both the forward bulk-push command and this reverse pull).
    mvm_tz_replay_full_db_logs_req_t resp_hdr = {0};
    uint32_t resp_hdr_len = 0, resp_blob_len = 0;
    const uint8_t *resp_blob = nullptr;
    mvm_reverse_round_trip(MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS,
        &req, sizeof(req), nullptr, 0,
        &resp_hdr, sizeof(resp_hdr), &resp_hdr_len, &resp_blob, &resp_blob_len);

    if (resp_hdr.entry_count == 0) {
        return 1; // nothing to replay -- success, not a failure
    }

    // Blob layout: entry_count records of [20-byte address, RAW, no length
    // prefix][log_len (u32)][log bytes] -- matches tz_codec.go's
    // writeAddrBytesMap exactly (encodeReplayFullDbLogsResp reuses it), NOT
    // BlobReader::readBytes' usual "everything is length-prefixed"
    // convention, since the address field's length is already fixed/known.
    BlobReader r{resp_blob, resp_blob_len};
    std::vector<LogReplayEntryC> entries;
    entries.reserve(resp_hdr.entry_count);
    for (uint32_t i = 0; i < resp_hdr.entry_count; i++) {
        if (r.off + 20 > r.len) {
            fprintf(stderr, "[mvm_ta] FATAL: full_db_logs entry %u: BlobReader underflow (address)\n", i);
            abort();
        }
        const uint8_t *addr_p = r.buf + r.off; // zero-copy: stable for the
        r.off += 20;                            // round trip's duration
        uint32_t log_len = 0;
        const uint8_t *log_p = r.readBytes(&log_len);

        LogReplayEntryC entry;
        entry.address_ptr = const_cast<unsigned char *>(addr_p);
        entry.address_len = 20;
        entry.log_data_ptr = const_cast<unsigned char *>(log_p);
        entry.log_data_len = (int)log_len;
        entries.push_back(entry);
    }

    return ReplayFullDbLogs(entries.data(), (int)entries.size());
}

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
    //
    // 2026-08-20 (plan §9.26 follow-up): the single most valuable
    // diagnostic bracket for the NULL ptr crash investigation -- if
    // "execute() CALLING" prints but "execute() RETURNED" never does, the
    // fault is genuinely inside the interpreter itself (execute()/c_mvm),
    // not in any of this file's own reverse-callback plumbing (which is
    // already independently bracketed above in mvm_reverse_round_trip/
    // GlobalStateGet/GetStorageValue).
    fprintf(stderr, "[mvm_ta][DIAG] execute() CALLING contract=%02x%02x...%02x input_len=%d\n",
        b_contract[0], b_contract[1], b_contract[19], (int)input_len);
    fflush(stderr);
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
    fprintf(stderr, "[mvm_ta][DIAG] execute() RETURNED rs=%p exitReason=%d exception=%d gas_used=%llu\n",
        (void *)rs, rs ? rs->b_exitReason : -1, rs ? rs->b_exception : -1, rs ? rs->gas_used : 0ULL);
    fflush(stderr);

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
    // Heap-allocated, NOT static BSS arrays: 3 x ~4MiB of static BSS here
    // inflated this executable's own RW LOAD segment to ~8.5MB (vs
    // llama-cli's ~8KB), which map_library() must reserve virtual address
    // space for during the INITIAL image load (chcore_alloc_vaddr(map_len)
    // in dynlink.c) -- a plausible contributor to a real, confirmed
    // "Not a valid dynamic program" rejection at launch (2026-08-17, see
    // plan doc §9.10). Allocating on the heap at startup instead keeps the
    // executable's own static image small; the memory is still reserved
    // (just later, via malloc, not baked into the ELF's own MemSiz).
    static uint8_t *resp_blob_scratch = (uint8_t *)malloc(MVM_TZ_BLOB_REGION_SIZE);
    static uint8_t *req_header_copy = (uint8_t *)malloc(512);
    static uint8_t *req_blob_copy = (uint8_t *)malloc(MVM_TZ_BLOB_REGION_SIZE);
    if (!resp_blob_scratch || !req_header_copy || !req_blob_copy) {
        fprintf(stderr, "[mvm_ta] FATAL: malloc failed for dispatch scratch buffers\n");
        abort();
    }

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
        if (header_len > 512) {
            mvm_tz_spinlock_unlock(&g_channel->lock);
            fprintf(stderr, "[mvm_ta] FATAL: forward header_len=%u exceeds scratch cap\n", header_len);
            abort();
        }
        memcpy(req_header_copy, g_channel->blob_region, header_len);
        memcpy(req_blob_copy, g_channel->blob_region + header_len, blob_len);
        mvm_tz_spinlock_unlock(&g_channel->lock);

        BlobWriter w{resp_blob_scratch, MVM_TZ_BLOB_REGION_SIZE};
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

// 2026-08-20 (plan §9.29 follow-up): the Xapian InMemory hardware
// selftest (round 2, per-call bracketing) ROOT-CAUSED the crash --
// removed after that, see memory xapian-inmemory-ta-backend-fix and plan
// doc §9.29's final entry for the full story. Summary: getInstance()/
// write/commit/read/revert's delete_document() ALL succeeded correctly
// (confirmed via UART bracketing) -- the crash is specifically
// `get_overlayed_document()` (xapian_manager.cpp) throwing
// `Xapian::DocNotFoundError("Document not found")` when re-reading a
// just-deleted doc, a THROW STATEMENT IN METANODE'S OWN SOURCE that is
// immediately wrapped by a textually-matching `catch (const
// Xapian::DocNotFoundError&)` one frame up in get_data() -- yet still
// reaches std::terminate() uncaught. Root cause: libxapian.a (and
// libtbb.a/libz.a) were cross-built with a DIFFERENT GCC generation
// (13.3.0-era, per this project's own 2026-08-17 build notes) than
// everything else linked into mvm_ta (GCC 11.5.0 via musl-gcc) -- a
// cross-GCC-version C++ exception-handling ABI mismatch, not a logic bug.
// Constructing a Xapian::DocNotFoundError calls into Xapian's own
// (GCC13-compiled) Error base class; a GCC11-compiled catch clause's RTTI
// comparison against that object's type_info can fail even when the
// source-level types match exactly. Real fix needs rebuilding Xapian
// (+ TBB/zlib) with the SAME GCC 11.5.0 toolchain as everything else --
// flagged as an open risk back in 2026-08-17, never addressed until this
// crash surfaced it for real. NOT attempted in this session (large,
// separate task, needs its own dedicated pass) -- do not re-add this
// selftest (or attempt the real GET_LATEST_FULL_DB_LOGS auto-trigger,
// which would hit the exact same ABI issue the first time it needs to
// distinguish "found" from "not found") until Xapian is rebuilt with a
// matching GCC generation.

int main(int argc, char **argv) {
    (void)argc; (void)argv;
    printf("[mvm_ta] starting\n");
    fflush(stdout);

    // NOTE (2026-08-18): this used to pin the thread to CPU 0 here,
    // guessing the g_tzasc_cma_meta_paddr==0 BUG_ON was a cross-core
    // visibility/page-table problem. That guess was wrong (see the much
    // longer retraction in mvm_channel_init()) -- it's a boot-ordering
    // race (this TA launches before the writer has ever run, on any
    // core), fixed properly there with a retry loop. CPU affinity is
    // irrelevant to that fix, so removed rather than left in as dead
    // weight that implies a diagnosis we've since disproven.

    // 2026-08-20 (plan §9.26/§9.27): root-caused the NULL ptr crash from a
    // real contract-code EXECUTE -- the interpreter's saveDebugInfo() (only
    // reached once dispatch() actually runs, i.e. never by a pure
    // native-transfer tx) does real filesystem I/O for any tx with
    // is_debug=1, which this TA build has none of. See
    // MVM_SetDebugFileLoggingEnabled()'s doc comment (mvm_linker.hpp) and
    // memory mvm-ta-evm-interpreter-nullptr-crash for the full story.
    MVM_SetDebugFileLoggingEnabled(false);

    mvm_channel_init();
    mvm_ta_run();
    return 0; // unreachable
}
