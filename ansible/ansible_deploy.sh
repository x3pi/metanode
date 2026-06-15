#!/bin/bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║  ANSIBLE MULTI-SERVER CLUSTER DEPLOYMENT WRAPPER                  ║
# ║                                                                   ║
# ║  Usage: ./ansible_deploy.sh [OPTIONS]                             ║
# ║  Options:                                                         ║
# ║    --start             Start nodes (re-distribute binaries)       ║
# ║    --setup             Fresh setup (gen keys, clears data)        ║
# ║    --stop              Stop nodes                                 ║
# ║    --clean             Clear data before starting nodes           ║
# ║    --only-node N       Only apply actions to node N               ║
# ║    --restore-node N    Restore node N from snapshot url           ║
# ║    --snapshot-url U    Snapshot URL to use (e.g. http://ip:8604)  ║
# ║    --open-ports        Open firewall ports for the nodes          ║
# ╚═══════════════════════════════════════════════════════════════════╝

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INVENTORY="${SCRIPT_DIR}/inventory.yml"
PLAYBOOK="${SCRIPT_DIR}/deploy.yml"

# Defaults
ACTION="start"
KEEP_DATA="true"
TARGET_NODE="all"
RESTORE_NODE="none"
SNAPSHOT_URL=""
OPEN_PORTS="false"

# Parse arguments
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --start) ACTION="start"; KEEP_DATA="true" ;;
        --reset-all) ACTION="setup"; KEEP_DATA="false" ;;
        --stop) ACTION="stop" ;;
        --clean) KEEP_DATA="false" ;;
        --only-node) TARGET_NODE="$2"; shift ;;
        --restore-node) RESTORE_NODE="$2"; shift ;;
        --snapshot-url) SNAPSHOT_URL="$2"; shift ;;
        --open-ports) OPEN_PORTS="true" ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            exit 0
            ;;
        *) echo "Unknown parameter passed: $1"; exit 1 ;;
    esac
    shift
done

echo -e "\n🚀 Starting Ansible Deployment with:"
echo "   Action:        $ACTION"
echo "   Target Node:   $TARGET_NODE"
echo "   Keep Data:     $KEEP_DATA"
echo "   Restore Node:  $RESTORE_NODE"
echo "   Open Ports:    $OPEN_PORTS"

# Prepare extra vars
EXTRA_VARS="ansible_action=${ACTION} target_node=${TARGET_NODE} keep_data=${KEEP_DATA} restore_node=${RESTORE_NODE} open_ports=${OPEN_PORTS}"
if [ -n "$SNAPSHOT_URL" ]; then
    EXTRA_VARS="${EXTRA_VARS} snapshot_url='${SNAPSHOT_URL}'"
fi

cd "$SCRIPT_DIR"
ansible-playbook -i "$INVENTORY" "$PLAYBOOK" -e "$EXTRA_VARS"
