# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@AGENTS.md

## Build

- Full check (C++ MVM linker + Rust + Go/CGo): `cd consensus/metanode/scripts && ./build_check.sh` (flags: `--go-only`, `--rust-only`, `--debug`, `--fast`). Exit 0 only if every step passed. Use the `/build-check` skill.
- Go links prebuilt native libs through hard-coded `#cgo` paths (no CGO_LDFLAGS needed):
  - `execution/executor/ffi_bridge.go` → `consensus/metanode/target/release/libmetanode.a`. The Cargo workspace builds into the ROOT `target/`, so `libmetanode.a` must be copied there (build_check.sh does it) or Go links a stale copy.
  - `execution/pkg/nomt_ffi/bridge.go` → root `target/release/libmtn_nomt`.
  - `execution/pkg/mvm/mvm_api.go` → static libs from `execution/pkg/mvm/build.sh linux` (cmake in `c_mvm/build`, `linker/build`). "undefined reference to MVM_*" means these libs are stale: rebuild them, it is not a Go bug.
- Go module root is `execution/` (run `go` commands from there). Rust is a single workspace at the repo root.
- Go protobuf: regenerate with `execution/pkg/proto/protoc.sh` run from inside `pkg/proto`; generated `*.pb.go` / `*_vtproto.pb.go` are committed. Rust protos compile in `build.rs`.

## Tests

- Match CI (`.github/workflows/go-ci.yml`): from `execution/`, `CGO_ENABLED=1 go test -p 1 -count=1 ./<pkg>/...`; `pkg/rollup/raftfeed` runs alone with `-timeout 15m`. Use the `/go-test` skill.
- Native libs must be built (build_check.sh) before Go tests in packages that import cgo bridges.
- CI runs no Rust tests and no golangci-lint; `execution/.golangci.yml` exists for local use.
- Evidence checks for claims: `python3 execution/scripts/test/evidence/claims_lint.py note/CLAIMS_LEDGER.md`.

## Formatting

- Many existing Go (gofmt) and Rust (rustfmt) files are NOT format-clean. Never run gofmt/cargo fmt on whole files or the repo; keep diffs limited to the lines you change. New files should be gofmt/rustfmt clean.

## Git

- Conventional commits (`fix:`, `refactor:`, `feat:`, ...). PRs target `main`.
