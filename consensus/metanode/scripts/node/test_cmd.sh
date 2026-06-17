#!/bin/bash
SSH_AUTH="password"
SSH_PASSWORD="1234@abcd"
REMOTE_DEPLOY_DIR="/home/abc"
r_node="2"
RESTORE_SNAPSHOT_URL="http://url"
CMD_SEQ="
    set -euo pipefail;
    cd '${REMOTE_DEPLOY_DIR}/metanode/deploy/systemd/cluster'
    SSH_AUTH='${SSH_AUTH:-}'
    SSH_PASSWORD='${SSH_PASSWORD:-}'
    if [ \"\${SSH_AUTH:-key}\" == \"password\" ] && [ -n \"\${SSH_PASSWORD:-}\" ]; then
        echo \"\${SSH_PASSWORD}\" | sudo -S bash restore_snapshot_systemd.sh --node $r_node --snapshot-url '${RESTORE_SNAPSHOT_URL}'
    else
        sudo bash restore_snapshot_systemd.sh --node $r_node --snapshot-url '${RESTORE_SNAPSHOT_URL}'
    fi
"
echo "$CMD_SEQ"
