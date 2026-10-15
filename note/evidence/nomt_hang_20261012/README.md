# NOMT hang evidence (2026-10-12)

- `nomt_hang_dump_*_call*_*.txt`: per-call diagnostic dumps captured live (thread states, io_uring fdinfo, counters). The 1602847 dump shows `completion_channel_len=11, channel_is_empty=false` while `write_ht` waited.
- `h2_gdb.txt`, `head_binary_hang_gdb_ab_HEAD_1.txt`: gdb stacks of hung validators (same signature: `write_ht -> Receiver::recv` parked, ~107-110 readers in `lock_shared_slow`).
- `nomt_diag_v3_per_call.patch`: the diagnostic build that caught it (per-call `write_ht` guard, `Receiver::len()`, io_uring fdinfo, global packet counters). Applies to current `dev` (`git apply --check` verified). NOT part of the product build; apply in a scratch worktree only.
  Rebuild: `git worktree add /tmp/wt_diag <commit> && cd /tmp/wt_diag && git apply <this patch>`; copy `execution/pkg/mvm/{c_mvm,linker}/build` from the main tree (ignored build outputs), `cargo build --release --locked -p mtn-nomt-ffi`, touch `execution/executor/ffi_bridge.go execution/pkg/nomt_ffi/bridge.go`, `go build ./cmd/simple_chain`.
- `soak_*.log`, `ab_head_vs_fix.log`: soak/A-B run logs (51 runs with the fix: 0 hangs).
