#!/usr/bin/env bash
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "🛑 Stopping Metanode Single Chain (Chain ID 1337)..."

for pid_file in "$DIR"/node-*/node-*.pid "$DIR"/node-*/consensus-*.pid; do
    if [ -f "$pid_file" ]; then
        PID=$(cat "$pid_file")
        if kill -0 "$PID" 2>/dev/null; then
            echo "  → Stopping node process PID $PID..."
            kill -15 "$PID" 2>/dev/null || true
            sleep 0.5
            kill -9 "$PID" 2>/dev/null || true
        fi
        rm -f "$pid_file"
    fi
done

echo "✅ Single Chain 1337 stopped."
