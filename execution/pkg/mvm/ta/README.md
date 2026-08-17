# pkg/mvm/ta — metanode's real TrustZone TA

`mvm_ta_main.cpp` is the entry point for `execution_mode=trustzone`'s
**real hardware** side (GĐ3 — see `note/tee_dual_mode_execution_plan.md`
§9). It is a **completely independent process**, deliberately separate from
`tz-llm-trustzone`'s own LLM TA (`tz-llm/llama.cpp`) — no shared code, no
shared struct, no shared build target, no shared runtime process. The two
projects only share (a) the chcore-libc SDK headers (the OS's own public
API, not project-specific) and (b) the `push_pages()`/
`usys_map_tzasc_cma_pmo` *mechanism* that project proved stable on this
exact hardware for model-weight streaming — reimplemented standalone here,
not linked from that repo.

## Status as of 2026-08-17 (honest, see the file's own top-of-file comment
for the authoritative, most current version)

**Compiled and link-verified on x86 with the real cross toolchain** (GCC
13.3.0 `aarch64-linux-musleabi`) against the real `libmvm_linker.a` +
`libmvm.a` + all 5 cross-built 3rd-party libs (GMP/MPFR/secp256k1/libuuid/
BLST) + Xapian + TBB + zlib + chcore-libc's real `libc.a` — **0 undefined
symbols**. This confirms the logical design (channel setup, reverse
-callback shims, `MVM_TZ_CMD_CALL` dispatch) is symbol-complete, but is
**not** a deployable build: it mixes musl-cross-make's own musl runtime
with chcore-libc's real musl `libc.a` in one binary, which is not a valid
combination for real hardware (see below for the actual build path).
**Nothing here has run on real hardware.**

- Channel setup (`push_pages_ex` + `usys_map_tzasc_cma_pmo`): implemented.
- Dispatch loop: implemented, busy-poll based (deliberately does not use
  `usys_tee_wait_switch_req` anywhere — see the file's design note on why:
  sidesteps a documented, previously-crash-causing SMC-ordering hazard in
  `tz-llm-trustzone`'s own history, rather than trying to prove sharing it
  safe).
- The 6 live reverse-callback shims (`GlobalStateGet`, `GetStorageValue`,
  `ExtensionCallGetApi`, `ExtensionExtractJsonField`, `ExtensionBlst`,
  `ExtensionGetOrCreateSimpleDb`): implemented, round-trip over the same
  channel.
- `MVM_TZ_CMD_CALL`: full round trip implemented, including
  `ExecuteResult` encoding for status/exception/gas_used/output/exmsg and
  the 3 simplest state-change arrays (`add_balance_change`,
  `sub_balance_change`, `nonce_change`).
- **NOT yet done**: `code_change`/`storage_change`/`storage_read`/
  `full_db_hash`/`full_db_logs`/`event_logs`/`native_logs` array encoding
  (counts are read correctly, entries are not written — a caller reading
  these off the wire today gets an empty result, not corrupted data).
  `MVM_TZ_CMD_EXECUTE`/`DEPLOY`/`SEND_NATIVE`/`PROCESS_NATIVE_MINT_BURN`/
  `NONCE_PLUS_ONE` (mechanically similar to `CALL` once the above is
  done). Real on-hardware testing of anything here.

## Real build path (not yet wired up)

This must be compiled the same way `tz-llm-trustzone`'s own LLM TA is —
`aarch64-linux-gnu-gcc-11` wrapped with chcore-libc's `musl-gcc.specs`
(`-specs .../musl-gcc.specs -nostdinc -isystem .../chcore-libc/include`),
**not** the generic GCC 13.3.0 `aarch64-linux-musleabi` toolchain used for
the x86 link-verification above (that toolchain's own musl runtime is not
ABI-compatible with chcore-libc's real musl build). See
`tz-llm-trustzone/scripts/kick-the-tires/build-llama-docker.sh`'s
`chcore_upload()`/`build-chcore` steps for the exact reference pipeline —
a parallel `mvm_ta` target needs to be added there (or alongside it), not
yet done.

## Companion kernel patch

`tz-llm-trustzone/tz-llm/tee_os_kernel/user/system-services/system-servers/
chanmgr/main.c` has a matching patch (2026-08-17) that launches this TA
(as `/mvm_ta`, expected to land in `oh_tee/apps/` the same way `llama-cli`
does) *before* `llama-cli`'s own launch — required so this TA's
`push_pages()` reservation is guaranteed the temporally-first allocation
on its chosen TZASC bank (see the plan doc §9.5 for why that's needed for
a deterministic `entry_index=0`). **That patch has not been compiled** —
it needs the full chanmgr/chcore build environment, which wasn't set up
this session; the first real build attempt will surface any mismatch.

## Why no CMakeLists.txt yet

Given the real build path goes through `tz-llm-trustzone`'s own Docker
pipeline (not a `metanode`-side CMake target the way `c_mvm`/`linker` are),
a `CMakeLists.txt` here would either duplicate that pipeline's toolchain
setup or invite confusion about which one is authoritative. The x86
link-verification above used direct compiler invocations instead — see
`note/tee_dual_mode_execution_plan.md` §9 for the exact commands, so they
can be reproduced without guessing.
