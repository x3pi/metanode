---
name: go-test
description: Run Go tests the way CI does (-p 1 -count=1, CGO) for the packages affected by the current change, or for a package/test given as argument. Use after modifying Go code under execution/.
---

1. Target: if $ARGUMENTS names a package or `-run` pattern, use it. Otherwise derive packages from `git diff --name-only -- execution/` (directory of each changed `.go` file), plus direct importers when an exported symbol changed (`grep -rln '<pkg import path>' execution --include=*.go`).
2. Run from `execution/`:
   `CGO_ENABLED=1 go test -p 1 -count=1 <pkgs> 2>&1 | tee <scratchpad>/go_test.log`
   - `./pkg/rollup/raftfeed` must run alone with `-v -timeout 15m` (as in `.github/workflows/go-ci.yml`).
   - For a single test add `-run '<Regex>' -v`.
   - If linking fails with `undefined reference`, native libs are stale: run the `/build-check` skill first.
3. Also run `go vet` on the same packages.
4. Report per package ok/FAIL straight from the log with the [ĐO] label and the log path. For failures, show the failing test name and assertion output; never summarize a failure as a pass or skip it.
5. When writing new tests: they must call the real function under test and assert behavior that would fail if the fix were reverted. Check this by temporarily reverting the fix (keep a backup under the scratchpad, `mkdir -p` it first) and confirming the test fails, then restore.
