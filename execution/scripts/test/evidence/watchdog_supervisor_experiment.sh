#!/usr/bin/env bash
# Real-systemd experiment for the execution watchdog fail-stop (pkg/utils/watchdog.go).
#
# Question: when the watchdog aborts a stuck process (SIGABRT), does the supervisor configuration used by our deploy
# templates restart it? This runs the REAL watchdog code (wd_probe links pkg/utils) under a transient *user* systemd
# unit (no sudo) with the same Restart=/RestartSec=/StartLimit* values as the templates, and records what systemd did.
#
# Usage: watchdog_supervisor_experiment.sh <wd_probe_binary> <out_dir>
# Only touches units named wdexp-*; stops and resets them at the end. Touches nothing else.
set -u
PROBE="$1"; OUT="$2"; mkdir -p "$OUT"
U="systemctl --user"
log() { echo "[$(date +%H:%M:%S)] $*" | tee -a "$OUT/experiment.log"; }
show() { $U show "$1" -p ActiveState,SubState,Result,NRestarts,ExecMainStatus,ExecMainCode,InvocationID 2>/dev/null | tr '\n' ' '; echo; }
cleanup() { for n in $($U list-units --all --plain --no-legend 'wdexp-*' 2>/dev/null | awk '{print $1}'); do $U stop "$n" >/dev/null 2>&1; $U reset-failed "$n" >/dev/null 2>&1; done; }
trap cleanup EXIT
cleanup

# run_unit <name> <mode> <abort_secs> <Restart> <RestartSec> <StartLimitIntervalSec> <StartLimitBurst> [extra systemd-run args...]
run_unit() {
  local name="$1" mode="$2" abort="$3" restart="$4" rsec="$5" slint="$6" slburst="$7"; shift 7
  systemd-run --user --quiet --unit="$name" \
    -p Restart="$restart" -p RestartSec="$rsec" -p StartLimitIntervalSec="$slint" -p StartLimitBurst="$slburst" \
    -p Environment="MODE=$mode" -p Environment="METANODE_EXEC_STUCK_ABORT_SECS=$abort" -p Environment="GOTRACEBACK=crash" \
    -p LimitCORE=0 -p Type=simple "$@" "$PROBE" 2>>"$OUT/experiment.log" || { log "FATAL: systemd-run failed for $name; experiment invalid"; exit 1; }
}

observe() { # name seconds
  local name="$1" secs="$2" i
  for i in $(seq 1 "$secs"); do sleep 1; if [ $((i % 5)) -eq 0 ]; then log "  $name t+${i}s: $(show "$name.service")"; fi; done
}

section() { log ""; log "=== $* ==="; }

section "E1: stuck, abort after 3 s, Restart=on-failure (deploy/ansible template value), RestartSec=2"
run_unit wdexp-e1 stuck 3 on-failure 2 300 5; observe wdexp-e1 20
$U show wdexp-e1.service -p NRestarts >>"$OUT/E1_nrestarts.txt"; journalctl --user -u wdexp-e1.service --no-pager 2>/dev/null | grep -E "probe start|WATCHDOG|abort|Main process exited|signal|Scheduled restart" | head -20 >"$OUT/E1_journal.txt"
$U stop wdexp-e1.service >/dev/null 2>&1; $U reset-failed wdexp-e1.service >/dev/null 2>&1

section "E2: stuck, abort after 3 s, Restart=always (ansible_clusters template value), RestartSec=3"
run_unit wdexp-e2 stuck 3 always 3 10 5; observe wdexp-e2 20
journalctl --user -u wdexp-e2.service --no-pager 2>/dev/null | grep -E "probe start|WATCHDOG|Main process exited|signal|Scheduled restart" | head -20 >"$OUT/E2_journal.txt"
$U stop wdexp-e2.service >/dev/null 2>&1; $U reset-failed wdexp-e2.service >/dev/null 2>&1

section "E3 (control): stuck but METANODE_EXEC_STUCK_ABORT_SECS=0 (disabled) => NO restart expected, process stays hung"
run_unit wdexp-e3 stuck 0 on-failure 2 300 5; observe wdexp-e3 15
$U stop wdexp-e3.service >/dev/null 2>&1; $U reset-failed wdexp-e3.service >/dev/null 2>&1

section "E4 (control): healthy phase (watchdog stopped after 0.5 s), abort_secs=3 => NO restart expected"
run_unit wdexp-e4 healthy 3 on-failure 2 300 5; observe wdexp-e4 15
$U stop wdexp-e4.service >/dev/null 2>&1; $U reset-failed wdexp-e4.service >/dev/null 2>&1

section "E5: crash loop trips StartLimit (StartLimitIntervalSec=300, StartLimitBurst=5, RestartSec=1, abort 1 s)"
run_unit wdexp-e5 stuck 1 on-failure 1 300 5; observe wdexp-e5 30
log "  E5 final: $(show wdexp-e5.service)"
$U stop wdexp-e5.service >/dev/null 2>&1; $U reset-failed wdexp-e5.service >/dev/null 2>&1

section "E6: dependent unit with Requires=+After= on the stuck unit (mirrors metanode-consensus -> metanode-execution)"
run_unit wdexp-e6exec stuck 3 on-failure 2 300 5
sleep 1
systemd-run --user --quiet --unit=wdexp-e6cons -p Restart=on-failure -p RestartSec=2 -p Requires=wdexp-e6exec.service -p After=wdexp-e6exec.service \
  -p LimitCORE=0 /bin/sleep 3600 2>>"$OUT/experiment.log" || { log "FATAL: systemd-run failed for wdexp-e6cons"; exit 1; }
for i in $(seq 1 20); do
  sleep 1
  if [ $((i % 4)) -eq 0 ]; then log "  t+${i}s exec: $(show wdexp-e6exec.service)"; log "  t+${i}s cons: $(show wdexp-e6cons.service)"; fi
done
journalctl --user -u wdexp-e6cons.service --no-pager 2>/dev/null | grep -E "Started|Stopped|Stopping|dependency|Dependency|Failed" | head -20 >"$OUT/E6_cons_journal.txt"

log ""; log "environment: $(uname -r); $(systemctl --version | head -1)"
log "done"
