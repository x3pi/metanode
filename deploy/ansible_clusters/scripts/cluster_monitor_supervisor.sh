#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════════════
#  🛡️ METANODE CLUSTER MONITOR SUPERVISOR (Systemd-Managed)
#  Foreground supervisor for Execution Cluster Monitors (health, resources,
#  block_hash_checker) and Parent Chain Monitor (liveness + zero-fork hash check).
#
#  Features:
#    • Boot grace delay when server just booted after power loss (uptime < 90s)
#    • Persistent endpoint config generation (/opt/metanode/monitors/<slug>/)
#      immune to /tmp reboot wipes and systemd-tmpfiles 10-day cleanups
#    • Active child-PID watchdog: exits non-zero if any worker dies so systemd
#      (Restart=always, KillMode=control-group) cleanly restarts all workers
#    • Clean SIGTERM/SIGINT shutdown (0 orphan processes)
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ANSIBLE_CLUSTERS_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
METANODE_ROOT="$(cd "${ANSIBLE_CLUSTERS_DIR}/../.." && pwd)"
MONITOR_SCRIPT="${METANODE_ROOT}/deploy/ansible/monitors/start_monitors.sh"
PARENT_MONITOR_PY="${SCRIPT_DIR}/parent_chain_monitor.py"
PARSE_INVENTORY_PY="${SCRIPT_DIR}/parse_inventory.py"

INVENTORY="${MONITOR_INVENTORY:-${ANSIBLE_CLUSTERS_DIR}/inventory.yml}"
INV_SLUG="${MONITOR_SLUG:-inventory}"
EXEC_ONLY="${MONITOR_EXEC_ONLY:-false}"
PARENT_ONLY="${MONITOR_PARENT_ONLY:-false}"
STATE_DIR="${MONITOR_STATE_DIR:-/opt/metanode/monitors/${INV_SLUG}}"
BOOT_UPTIME_WINDOW_SEC="${MONITOR_BOOT_UPTIME_WINDOW_SEC:-90}"
BOOT_GRACE_SEC="${MONITOR_BOOT_GRACE_SEC:-30}"
WATCHDOG_INTERVAL_SEC="${MONITOR_WATCHDOG_INTERVAL_SEC:-10}"
LOG_RETENTION_DAYS="${MONITOR_LOG_RETENTION_DAYS:-3}"
LOG_MAX_SIZE_MB="${MONITOR_LOG_MAX_SIZE_MB:-20}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --inventory=*)
            INVENTORY="${1#*=}"
            shift
            ;;
        --slug=*)
            INV_SLUG="${1#*=}"
            shift
            ;;
        --state-dir=*)
            STATE_DIR="${1#*=}"
            shift
            ;;
        --exec-only)
            EXEC_ONLY="true"
            shift
            ;;
        --parent-only)
            PARENT_ONLY="true"
            shift
            ;;
        *)
            shift
            ;;
    esac
done

if [ ! -f "$INVENTORY" ]; then
    echo "❌ [Monitor Supervisor] Inventory file not found: ${INVENTORY}" >&2
    exit 1
fi

mkdir -p "$STATE_DIR" 2>/dev/null || {
    STATE_DIR="/var/tmp/metanode-monitors/${INV_SLUG}"
    mkdir -p "$STATE_DIR"
}
chmod 0700 "$STATE_DIR" 2>/dev/null || true
rm -f "${STATE_DIR}/supervisor.ready" 2>/dev/null || true

ACTIVE_NAMESPACES=()
PARENT_PID=""
SLEEP_PID=""
SHUTTING_DOWN=0

cleanup() {
    if [ "$SHUTTING_DOWN" -eq 1 ]; then
        return
    fi
    SHUTTING_DOWN=1
    echo "🛑 [Monitor Supervisor] Stopping all monitor workers for ${INV_SLUG} (${INVENTORY})..."

    if [ -n "$SLEEP_PID" ] && kill -0 "$SLEEP_PID" 2>/dev/null; then
        kill "$SLEEP_PID" 2>/dev/null || true
    fi

    if [ -n "$PARENT_PID" ] && kill -0 "$PARENT_PID" 2>/dev/null; then
        kill -15 "$PARENT_PID" 2>/dev/null || true
    fi
    rm -f "${STATE_DIR}/supervisor.ready" "${STATE_DIR}/parent_chain_monitor.pid" "/tmp/parent_chain_monitor.${INV_SLUG}.pid" 2>/dev/null || true
    if [ "$INV_SLUG" = "inventory" ]; then
        rm -f "/tmp/parent_chain_monitor.pid" 2>/dev/null || true
    fi

    if [ -f "$MONITOR_SCRIPT" ]; then
        local ns
        for ns in "${ACTIVE_NAMESPACES[@]+"${ACTIVE_NAMESPACES[@]}"}"; do
            [ -n "$ns" ] || continue
            MONITOR_NAMESPACE="$ns" MONITOR_INVENTORY="$INVENTORY" \
                bash "$MONITOR_SCRIPT" --stop --namespace "$ns" >/dev/null 2>&1 || true
        done
    fi

    echo "✅ [Monitor Supervisor] Cleanly stopped all monitor workers for ${INV_SLUG}."
}

on_term() {
    cleanup
    exit 0
}

trap on_term SIGTERM SIGINT SIGHUP

prune_monitor_logs() {
    local max_bytes=$(( LOG_MAX_SIZE_MB * 1024 * 1024 ))
    local log_dirs=("$STATE_DIR")
    local ns
    for ns in "${ACTIVE_NAMESPACES[@]+"${ACTIVE_NAMESPACES[@]}"}"; do
        [ -n "$ns" ] || continue
        if [ -d "/tmp/metanode-monitors-${ns}" ]; then
            log_dirs+=("/tmp/metanode-monitors-${ns}")
        fi
    done

    # 1. Xóa các bản log xoay vòng cũ và thư mục crash dump quá LOG_RETENTION_DAYS ngày (mặc định 3 ngày)
    local d
    for d in "${log_dirs[@]}"; do
        [ -d "$d" ] || continue
        find "$d" -maxdepth 3 -type f \( -name "*.log.*" -o -name "*.csv" \) -mtime +"$LOG_RETENTION_DAYS" -delete 2>/dev/null || true
    done
    local crash_root="${METANODE_ROOT}/deploy/ansible/monitors/logs_crash"
    if [ -n "$METANODE_ROOT" ] && [ -f "$MONITOR_SCRIPT" ] && [ -d "$crash_root" ]; then
        find "$crash_root" -mindepth 1 -maxdepth 1 -type d \( -name "node_*_crash_*" -o -name "server_*_reboot_*" \) -mtime +"$LOG_RETENTION_DAYS" -exec rm -rf {} + 2>/dev/null || true
    fi

    # 2. Giới hạn dung lượng tối đa (mặc định 20MB/file) cho các file .log đang ghi trực tiếp
    local lf fsize keep_tmp
    for d in "${log_dirs[@]}"; do
        [ -d "$d" ] || continue
        while IFS= read -r -d '' lf; do
            fsize=$(stat -c%s "$lf" 2>/dev/null || echo "0")
            if [ "${fsize:-0}" -gt "$max_bytes" ]; then
                keep_tmp="${lf}.trim_tmp"
                tail -n 2000 "$lf" > "$keep_tmp" 2>/dev/null || : > "$keep_tmp"
                cat "$keep_tmp" > "$lf" 2>/dev/null || : > "$lf"
                rm -f "$keep_tmp" 2>/dev/null || true
            fi
        done < <(find "$d" -maxdepth 3 -type f -name "*.log" -print0 2>/dev/null || true)
    done
}

# ── 1. Boot Grace Delay (avoid false alerts right after power outage/reboot) ──
UPTIME_SEC=$(awk '{print int($1)}' /proc/uptime 2>/dev/null || echo "999999")
if [ "${UPTIME_SEC:-999999}" -lt "$BOOT_UPTIME_WINDOW_SEC" ] && [ "$BOOT_GRACE_SEC" -gt 0 ]; then
    echo "⏳ [Monitor Supervisor] Host recently booted (uptime=${UPTIME_SEC}s < ${BOOT_UPTIME_WINDOW_SEC}s). Waiting ${BOOT_GRACE_SEC}s for network/router & nodes..."
    sleep "$BOOT_GRACE_SEC" &
    SLEEP_PID=$!
    wait "$SLEEP_PID" 2>/dev/null || true
    SLEEP_PID=""
fi

# ── 2. Export persistent endpoint configs (survives reboot & /tmp cleanups) ──
echo "📦 [Monitor Supervisor] Exporting endpoint configurations from ${INVENTORY} into ${STATE_DIR}..."
CLUSTER_LIST_RAW=$(python3 - "$INVENTORY" "$SCRIPT_DIR" "$STATE_DIR" <<'PY'
import json
import os
import sys
import tempfile

inv_path, scripts_dir, state_dir = sys.argv[1:4]
sys.path.insert(0, scripts_dir)
import parse_inventory as pi

info = pi.parse_inventory(inv_path)

def write_atomic_json(path, payload):
    os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
    fd, tmp_path = tempfile.mkstemp(prefix=".rpc_nodes_", dir=os.path.dirname(os.path.abspath(path)))
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(payload, f, indent=2)
        os.chmod(tmp_path, 0o600)
        os.replace(tmp_path, path)
    finally:
        if os.path.exists(tmp_path):
            os.unlink(tmp_path)

write_atomic_json(os.path.join(state_dir, "rpc_nodes.json"), info)

clusters_out = []
for cid, c_data in sorted(info.get("clusters", {}).items()):
    c_name = c_data.get("cluster_name", f"exec{cid}")
    c_replicas = c_data.get("replicas", {})
    if not c_replicas:
        continue
    c_nodes = {r_k: r_v["rpc_url"] for r_k, r_v in c_replicas.items()}
    c_content = {
        "nodes": c_nodes,
        "rpc_nodes": c_nodes,
        "ws_nodes": {r_k: r_v["ws_url"] for r_k, r_v in c_replicas.items()},
        "tcp_nodes": {r_k: f"{r_v['ip']}:{r_v['p2p_port']}" for r_k, r_v in c_replicas.items()},
        "raft_nodes": {r_k: f"{r_v['ip']}:{r_v['raft_port']}" for r_k, r_v in c_replicas.items()},
        "chain_id": c_data.get("chain_id", 991),
        "cluster_name": c_name,
    }
    write_atomic_json(os.path.join(state_dir, f"rpc_nodes.{c_name}.json"), c_content)
    # Also ensure /tmp/rpc_nodes.<c_name>.json exists for external status tools without deleting other inventories
    write_atomic_json(f"/tmp/rpc_nodes.{c_name}.json", c_content)
    clusters_out.append(c_name)

has_parent = "true" if info.get("parent_nodes") else "false"
print(f"HAS_PARENT={has_parent}")
for name in clusters_out:
    print(f"CLUSTER={name}")
PY
)

HAS_PARENT="false"
while IFS= read -r line; do
    case "$line" in
        HAS_PARENT=*)
            HAS_PARENT="${line#HAS_PARENT=}"
            ;;
        CLUSTER=*)
            ns="${line#CLUSTER=}"
            if [ -n "$ns" ]; then
                ACTIVE_NAMESPACES+=("$ns")
            fi
            ;;
    esac
done <<< "$CLUSTER_LIST_RAW"

EXPECTED_PID_FILES=()

# ── 3. Launch Execution Cluster Monitors ─────────────────────────────────────
if [ "$PARENT_ONLY" != "true" ] && [ -f "$MONITOR_SCRIPT" ]; then
    for ns in "${ACTIVE_NAMESPACES[@]+"${ACTIVE_NAMESPACES[@]}"}"; do
        [ -n "$ns" ] || continue
        cfg_file="${STATE_DIR}/rpc_nodes.${ns}.json"
        echo "▶️  [Monitor Supervisor] Starting Execution Cluster Monitor for '${ns}' (config: ${cfg_file})..."
        MONITOR_NAMESPACE="$ns" MONITOR_INVENTORY="$INVENTORY" \
            bash "$MONITOR_SCRIPT" --stop --namespace "$ns" >/dev/null 2>&1 || true
        MONITOR_NAMESPACE="$ns" MONITOR_INVENTORY="$INVENTORY" \
            bash "$MONITOR_SCRIPT" --namespace "$ns" --config "$cfg_file" --no-vote

        EXPECTED_PID_FILES+=("/tmp/metanode-monitors-${ns}/health.pid")
        EXPECTED_PID_FILES+=("/tmp/metanode-monitors-${ns}/resources.pid")
        if [ -f "/tmp/metanode-monitors-${ns}/block_hash_checker.pid" ]; then
            EXPECTED_PID_FILES+=("/tmp/metanode-monitors-${ns}/block_hash_checker.pid")
        elif [ -x "${METANODE_ROOT}/deploy/ansible/monitors/block_hash_checker/block_hash_checker" ]; then
            echo "⚠️  [Monitor Supervisor] block_hash_checker.pid missing for '${ns}' despite binary existing; registering for watchdog recovery."
            EXPECTED_PID_FILES+=("/tmp/metanode-monitors-${ns}/block_hash_checker.pid")
        else
            echo "⚠️  [Monitor Supervisor] block_hash_checker.pid not present for '${ns}' (binary not built); skipping hash-checker watchdog."
        fi
    done
fi

# ── 4. Launch Parent Chain Monitor ───────────────────────────────────────────
if [ "$EXEC_ONLY" != "true" ] && [ "$HAS_PARENT" = "true" ] && [ -f "$PARENT_MONITOR_PY" ]; then
    PARENT_LOG="${STATE_DIR}/parent_chain_monitor.log"
    echo "▶️  [Monitor Supervisor] Starting Parent Chain Monitor (inventory: ${INVENTORY}, log: ${PARENT_LOG})..."
    pkill -f "[p]arent_chain_monitor\.py.*${INVENTORY}" >/dev/null 2>&1 || true
    python3 -u "$PARENT_MONITOR_PY" --inventory="$INVENTORY" --interval=5 >> "$PARENT_LOG" 2>&1 &
    PARENT_PID=$!
    echo "$PARENT_PID" > "${STATE_DIR}/parent_chain_monitor.pid"
    echo "$PARENT_PID" > "/tmp/parent_chain_monitor.${INV_SLUG}.pid"
    if [ "$INV_SLUG" = "inventory" ]; then
        echo "$PARENT_PID" > "/tmp/parent_chain_monitor.pid"
    fi
    ln -sf "$PARENT_LOG" "/tmp/parent_chain_monitor.${INV_SLUG}.log" 2>/dev/null || true
    if [ "$INV_SLUG" = "inventory" ]; then
        ln -sf "$PARENT_LOG" "/tmp/parent_chain_monitor.log" 2>/dev/null || true
    fi
    EXPECTED_PID_FILES+=("${STATE_DIR}/parent_chain_monitor.pid")
fi

if [ "${#EXPECTED_PID_FILES[@]}" -eq 0 ]; then
    echo "⚠️  [Monitor Supervisor] No monitor targets configured for ${INVENTORY}. Exiting cleanly."
    exit 0
fi

echo "✅ [Monitor Supervisor] Supervising ${#EXPECTED_PID_FILES[@]} worker(s) for ${INV_SLUG} (log retention: ${LOG_RETENTION_DAYS}d, max: ${LOG_MAX_SIZE_MB}MB/file)."
prune_monitor_logs
echo "$$" > "${STATE_DIR}/supervisor.ready"

# ── 5. Active Watchdog & Periodic Log Cleanup Loop ───────────────────────────
LAST_PRUNE_TS=$(date +%s)
while true; do
    sleep "$WATCHDOG_INTERVAL_SEC" &
    SLEEP_PID=$!
    wait "$SLEEP_PID" 2>/dev/null || true
    SLEEP_PID=""

    if [ "$SHUTTING_DOWN" -eq 1 ]; then
        exit 0
    fi

    NOW_TS=$(date +%s)
    if [ $(( NOW_TS - LAST_PRUNE_TS )) -ge 3600 ]; then
        prune_monitor_logs
        LAST_PRUNE_TS="$NOW_TS"
    fi

    for pid_file in "${EXPECTED_PID_FILES[@]}"; do
        if [ ! -f "$pid_file" ]; then
            echo "❌ [Monitor Supervisor] PID file disappeared: ${pid_file}. Exiting to trigger systemd restart..." >&2
            cleanup
            exit 1
        fi
        worker_pid=$(cat "$pid_file" 2>/dev/null || echo "")
        if [ -z "$worker_pid" ] || ! kill -0 "$worker_pid" 2>/dev/null; then
            echo "❌ [Monitor Supervisor] Worker died unexpectedly (${pid_file}, PID=${worker_pid:-none}). Exiting to trigger systemd restart..." >&2
            cleanup
            exit 1
        fi
    done
done
