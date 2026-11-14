---
name: build-check
description: Run consensus/metanode/scripts/build_check.sh (C++ MVM linker, Rust, Go/CGo) after code edits, then fix every compile error and warning until the build is clean. Use after modifying Go, Rust or FFI code.
---

1. Pick scope from what changed in this session (`git diff --name-only`):
   - Only `execution/**/*.go` → `--go-only`
   - Only Rust (`consensus/`, `meta-consensus/`, `crates/`, `execution/pkg/nomt_ffi/rust_lib`) → `--rust-only`
   - FFI boundary (`ffi_bridge.go`, `nomt_ffi/bridge.go`, `mvm_api.go`, Rust `extern "C"`), C++ under `execution/pkg/mvm`, or unsure → no flag (full build).
   Add any flags from $ARGUMENTS.
2. Run it in the background (it is slow), logging to the scratchpad:
   `cd consensus/metanode/scripts && ./build_check.sh <flags> > <scratchpad>/build_check.log 2>&1; echo "exit=$?"`
3. Read the exit code and grep the log for `error`, `warning`, `undefined reference`, `FAIL`. Never report success without exit=0 from this run.
4. Fix errors and warnings in code you touched. Rules:
   - `undefined reference to MVM_*` / Xapian symbols: stale prebuilt C++ libs; rerun without `--go-only` so `execution/pkg/mvm/build.sh linux` rebuilds them.
   - Do not reformat whole files to silence anything; change only the offending lines.
   - Pre-existing warnings in files you did not touch: report them, do not fix unless asked.
5. Re-run until exit=0, then report the exact result (exit code + log path) with the [ĐO] label.
