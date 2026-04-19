#!/bin/bash
export GOTOOLCHAIN=go1.23.5

SESSION="simple_chain"

# Paths
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

MASTER_DATA="$SCRIPT_DIR/sample/simple/data"
WRITE_DATA="$SCRIPT_DIR/sample/simple/data-write"

# Xapian search index paths
MASTER_XAPIAN="$MASTER_DATA/data/xapian_node"
WRITE_XAPIAN="$WRITE_DATA/data/xapian_node"

# EVM/MVM (C++ VM) log output directories - separated from Go app logs
MASTER_MVM_LOG="$MASTER_DATA/logs"
WRITE_MVM_LOG="$WRITE_DATA/logs"

# ─── Cleanup old session ──────────────────────────────────────────────────────
if tmux has-session -t $SESSION 2>/dev/null; then
    echo "[run.sh] Killing existing tmux session: $SESSION"
    tmux kill-session -t $SESSION
    sleep 1
fi

# ─── Check port conflicts ─────────────────────────────────────────────────────
PORTS=(4200 4201 8646 8747)
CONFLICT=0
for PORT in "${PORTS[@]}"; do
    PID=$(ss -tlnp | grep ":$PORT " | grep -oP 'pid=\K[0-9]+' | head -1)
    if [ -n "$PID" ]; then
        CMD=$(ps -p $PID -o comm= 2>/dev/null || echo "unknown")
        echo "[run.sh] WARNING: Port $PORT is in use by PID=$PID ($CMD)"
        CONFLICT=1
    fi
done
if [ "$CONFLICT" -eq 1 ]; then
    echo "[run.sh] Port conflicts detected. Kill old processes first or change config ports."
    echo "[run.sh] Tip: pkill -f 'simple_chain' to kill all simple_chain processes"
    read -p "[run.sh] Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# ─── Ensure directories exist ─────────────────────────────────────────────────
mkdir -p "$MASTER_XAPIAN" "$WRITE_XAPIAN"
mkdir -p "$MASTER_MVM_LOG" "$WRITE_MVM_LOG"

# ─── Start tmux session ───────────────────────────────────────────────────────
tmux new-session -d -s $SESSION -n "NODES"

# Pane 0: MASTER node
tmux send-keys -t $SESSION:0.0 "export XAPIAN_BASE_PATH=$MASTER_XAPIAN && export MVM_LOG_DIR=$MASTER_MVM_LOG && go run . -config=config-master.json --debug --pprof-addr=localhost:6061" C-m

# Pane 1: WRITE node
tmux split-window -h -t $SESSION:0
tmux send-keys -t $SESSION:0.1 "sleep 1 && export XAPIAN_BASE_PATH=$WRITE_XAPIAN && export MVM_LOG_DIR=$WRITE_MVM_LOG && go run . -config=config-sub-write.json --debug --pprof-addr=localhost:6062" C-m

# WRITE-2 (uncomment if needed):
# tmux split-window -v -t $SESSION:0.1
# tmux send-keys -t $SESSION:0.2 "sleep 15 && go run . -config=config-sub-write-2.json" C-m

tmux select-layout -t $SESSION:0 tiled
tmux attach-session -t $SESSION
