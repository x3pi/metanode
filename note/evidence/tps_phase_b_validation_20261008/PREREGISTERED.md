# PREREGISTERED SCIENTIFIC PROTOCOL: PHASE B VALIDATION & ROOT CAUSE INVESTIGATION

**Date Registered:** 2026-10-08  
**Author:** Metanode Core Engineering  
**Target:** Independent Rigorous Verification of B1 (+24.61% Claim) & Source Attribution of ~750ms Block Cycle Gap  
**Status:** COMMITTED PRIOR TO EXPERIMENTATION  

---

## 1. RESEARCH QUESTIONS & HYPOTHESES

### Question 1: Does B1 (Parallel Speculative Root Derivation) Deliver True Throughput Gain?
- **Prior Claim (Commit `8d5aa6cc`):** Mean TPS increased from 10,898 to 13,580 (+24.61%), Welch t = 12.511.
- **Audit Finding:** Baseline A1 dataset `[14061.5, 10118.5, 10137.2, 9625.9, 10547.1]` has actual SD = **1,798.1 tx/s** (not 391.5). The reported SD 391.5 was mistakenly computed by omitting Run 1 (14,061.5 tx/s). The actual Welch t is ~3.3. Furthermore, Run 1 of A1 reached 14,061 tx/s (equivalent to B1), suggesting potential machine state confounding (non-interleaved execution).
- **Hypothesis 1 (True Gain):** Under strictly interleaved A/B execution with fresh cluster wipes per run, B1 achieves a median TPS gain $\ge +5.0\%$ over BEFORE, with 95% Confidence Interval strictly excluding zero.
- **Null Hypothesis $H_0$:** The difference in median TPS between BEFORE and AFTER is $< +5.0\%$, or the 95% CI overlaps with zero (indicating observed differences were driven by machine noise or run ordering).

### Question 2: What Accounts for the ~750ms Unexplained Gap in the Block Cycle?
- **Observation:** Average block cycle time $T_{cycle} \approx 950 - 1100\text{ ms}$. Within Go, execution $T_a \approx 148\text{ ms}$, persist $T_b \approx 40\text{ ms}$ (total $\approx 188\text{ ms}$). Approximately **750 - 900 ms per block cycle** is spent outside Go execution/persist.
- **Hypothesis 2A (Consensus / IPC Bottleneck):** Go is idle waiting for Rust consensus proposal, commit dispatch, or FFI IPC for $>60\%$ of the cycle time.
- **Hypothesis 2B (Forensic Hashing Side Effect):** B1's gain was predominantly driven by removing Keccak forensic hashing (`logger.IsDebugEnabled()`) rather than roots parallelism.

### Question 3: Is B1 100% Safe Under Discard and Crash-Recovery?
- **Hypothesis 3:** When a speculative session is aborted (`CleanGEI`, `Conflict`, shutdown, or kill -9 during concurrent root derivation), no uncommitted state leaks to shared DBs, no goroutines leak, and full node recovery preserves identical state roots.

---

## 2. EXPERIMENTAL DESIGN & PROTOCOL

### Workload 1: Randomized Interleaved A/B Benchmark (16 Runs)
- **Binary Pair:**
  - `BEFORE`: Built from commit `0818f2f1` (immediately prior to B1).
  - `AFTER`: Built from commit `8d5aa6cc` (B1 implementation).
  - Both compiled on same machine with identical Go and Rust compiler toolchains.
- **Interleaving Schedule:**
  - Fixed PRNG seed: `20261008`.
  - 16 runs total: 8 runs BEFORE (`A`), 8 runs AFTER (`B`).
  - Sequence: `A, B, B, A, A, B, B, A, B, A, A, B, B, A, A, B`.
- **Per-Run Environment:**
  - Full cluster wipe and fresh deployment from `/tmp/gate_4val_clean_template` (Genesis #0, 50,000 keys).
  - Fixed 10s cooldown between runs to ensure disk flushes and CPU thermal stabilization.
  - Workload parameters: 60s sustained blast, rate-limit = 0 (unlimited), batch size = 1,000, mode = TCP, type = 1559, flag = `-verify-parity`.
- **Zero-Fork Constraint:** Any parity mismatch between validators immediately fails the run and terminates the series.

### Workload 2: Absolute Timeline Decomposition & Bottleneck Attribution
- High-precision timestamp instrumentation (nanoseconds):
  - Rust side: consensus commit received, `send_committed_subdag` start, response received from Go, next commit dispatched.
  - Go side: request received, gate pass, Block-STM finish, roots finish, memory commit finish, disk persist dispatch, response sent.
- Measurement over $\ge 5$ sustained 60s runs on AFTER binary.
- Decomposition metrics:
  - $T_{Go\_busy}$: Active execution + root calc + memory commit.
  - $T_{Go\_idle}$: Time Go committer thread waits for Rust to supply next commit.
  - $T_{Rust\_wait}$: Time Rust waits for Go execution response.
  - $T_{Consensus\_round}$: Time Rust consensus takes to accumulate 2f+1 votes and form commit.

### Workload 3: Discard, Recovery & Chaos Resilience
- Unit tests:
  - Test session cancellation while `PrecomputeRoots` is actively computing.
  - Test fallback to sequential root calculation if `PrecomputeRoots` encounters error.
  - Full package `-race` detector execution.
- Chaos fault injection:
  - Random `kill -9` of validator nodes during active blast ($\ge 5$ cycles).
  - Verification of cold restart, WAL replay vs live execution state root consistency.

---

## 3. PREREGISTERED DECISION CRITERIA

| Test Item | Pass Criteria | Action If Failed |
| :--- | :--- | :--- |
| **B1 Interleaved Verification** | Median TPS gain $\ge +5.0\%$ AND 95% Confidence Interval does not overlap zero | Mark B1 as **UNCONFIRMED**. Revert or retain as neutral optimization based on risk analysis. |
| **Zero-Fork Parity** | 100% of runs pass `-verify-parity` on all 4 validators | **ABSOLUTE BLOCKER**. If any run fails, halt immediately. |
| **Block Cycle Attribution** | Fully account for 100% of the ~950ms block cycle time with empirical nanosecond breakdown | Must resolve before proposing any Phase D pipelining. |
| **Phase D Gate Prerequisite** | Phase D (removing `waitCommitted`) is ONLY considered if: (1) Rust idle wait on Go $> 40\%$ of cycle, AND (2) Amdahl headroom $\ge +15\%$, AND (3) User provides explicit written approval | Do NOT touch `waitCommitted` serialization gate. |
| **Errata Correction** | Update historical documentation with true baseline SD (1,798 tx/s) and protocol distinctions | Required for scientific integrity. |
