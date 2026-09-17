#!/bin/bash
# run.sh - Runner script for MetaNode Validator Vote Monitor
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Build if binary doesn't exist or main.go is newer
if [ ! -f "${SCRIPT_DIR}/vote_monitor" ] || [ "${SCRIPT_DIR}/main.go" -nt "${SCRIPT_DIR}/vote_monitor" ]; then
    echo "🔨 Compiling vote_monitor..."
    cd "${SCRIPT_DIR}"
    go build -buildvcs=false -o vote_monitor main.go
fi

# Locate config if first arg is a file
CONFIG_ARG=()
if [ -n "$1" ] && [ ! "${1:0:1}" = "-" ] && [ -f "$1" ]; then
    CONFIG_ARG=("--config" "$1")
    shift
fi

echo "🚀 Starting Vote Monitor..."
exec "${SCRIPT_DIR}/vote_monitor" "${CONFIG_ARG[@]}" "$@"
