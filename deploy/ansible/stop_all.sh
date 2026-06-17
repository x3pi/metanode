#!/bin/bash
# stop_all.sh
# Script to stop all deployment-related background processes

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

STOP_CLUSTER=false
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --cluster) STOP_CLUSTER=true ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
    shift
done

echo "🛑 Stopping local background monitors..."
pkill -f "start_monitors.sh health" || true
pkill -f "block_hash_checker" || true
echo "✅ Local monitors stopped."

echo "🛑 Stopping git auto-rebuild watcher daemon..."
pkill -f "auto_rebuild_deploy.sh" || true
echo "✅ Watcher daemon stopped."

echo "🛑 Stopping any active ansible-playbook runs..."
pkill -f "ansible-playbook" || true
echo "✅ Active playbooks stopped."

if [ "$STOP_CLUSTER" = true ]; then
    echo "🚀 Stopping remote cluster nodes..."
    bash "${SCRIPT_DIR}/ansible_deploy.sh" --stop
else
    echo -e "\nℹ️  To also stop the remote cluster nodes, run:"
    echo "   ./stop_all.sh --cluster"
fi

echo -e "\n🎉 Done!"
