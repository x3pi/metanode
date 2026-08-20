# host_repro — x86 in-process repro harness for the TA's NULL ptr crash

Calls the SAME `execute()` entry point `mvm_ta_main.cpp`'s
`mvm_dispatch_execute()` calls, with the SAME parameters `mvm_ca_test.cpp`'s
"contract call (SSTORE/SLOAD)" test sent to real hardware (see plan doc
§9.25 / memory `mvm-ta-evm-interpreter-nullptr-crash`), but linked natively
against the existing in-tree x86 `linker/build/lib/static/libmvm_linker.a` +
`c_mvm/build/lib/static/libmvm.a` (already built Release+`-g` by
`linker/CMakeLists.txt`'s default flags — no rebuild needed). No TA, no
board, no cross toolchain — runs under `gdb` directly on the host, seconds
per iteration instead of a full build→flash→reboot cycle.

## Build

```bash
cd metanode/execution/pkg/mvm
g++ -std=c++20 -g -O0 -I linker/include \
  ta/host_repro/host_repro.cpp -o /tmp/host_repro \
  -Wl,--start-group \
  linker/build/lib/static/libmvm_linker.a \
  c_mvm/build/lib/static/libmvm.a \
  /usr/lib/x86_64-linux-gnu/libsecp256k1.a \
  <path-to-a-native-x86-libblst.a> \
  -lgmp -lmpfr -lm -ltbb -lxapian -lleveldb -lstdc++ -luuid \
  -Wl,--end-group \
  -lpthread
gdb -q -batch -ex run -ex bt --args /tmp/host_repro
```

(`libblst.a`: no system package; as of 2026-08-20 a native x86 build exists
at `~/.foundry/foundry-rs/foundry/target/release/build/blst-*/out/libblst.a`
on this machine — any native x86 static build of metanode's own vendored
`pkg/bls/blst` source works equally well.)

## 2026-08-20 result: does NOT reproduce the crash

Ran both the isolated contract-call case and the full 2-call sequence
(native transfer, then the contract call, same process/mvm_id — matching
the real board test's ordering exactly) under gdb. **Both completed
cleanly, no crash**, `exitReason=0 exception=0` for the contract call. This
rules out, as the sole cause:
- A generic C++ logic bug in the EVM interpreter's SSTORE/SLOAD handling.
- Cross-call `State`-singleton corruption from running 2 `execute()` calls
  in the same process.
- Xapian (confirmed separately: `my_storage.cpp`'s SSTORE/SLOAD path has
  zero Xapian references — this test never touches it either way).

Does **not** rule out: something specific to the aarch64/musl cross-build
codegen, or to `mvm_ta_main.cpp`'s own reverse-round-trip channel plumbing
(this harness's `GlobalStateGet`/`GetStorageValue` stubs return data
directly, bypassing the real `BlobWriter`/`BlobReader` wire marshal — that
code is simple/would abort loudly on a size mismatch rather than silently
NULL-deref, but hasn't been independently exercised here). Thread stack
size checked and ruled out too: `chcore/defs.h`'s `MAIN_THREAD_STACK_SIZE`/
`CHCORE_PTHREAD_DEFAULT_STACK_SIZE` both resolve to 8MB on 64-bit builds,
same order as glibc's default — not a smaller-stack-on-TA explanation.

**Next step** needs either on-device instrumentation (bracketing prints in
`mvm_ta_main.cpp` around the reverse-round-trip calls, one more
build→flash→reboot cycle) or closer inspection of the musl-gcc(GCC11) cross
-build's actual generated code at the fault site — not something this x86
harness alone can settle.
