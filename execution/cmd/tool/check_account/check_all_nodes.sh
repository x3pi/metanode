#!/bin/bash
NODES=("192.168.1.234:6200" "192.168.1.233:6201" "192.168.1.232:6202" "192.168.1.231:6203" "192.168.1.230:6204")
NAMES=("Node 0" "Node 1" "Node 2" "Node 3" "Node 4")

for i in "${!NODES[@]}"; do
    ADDR=${NODES[$i]}
    NAME=${NAMES[$i]}
    echo "=== Checking $NAME ($ADDR) ==="
    cat <<EOF > config_temp.json
{
    "version": "0.0.1.0",
    "private_key": "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
    "parent_connection_address": "$ADDR",
    "parent_address": "0x40a83142d35a180965a8965470075ff569973682",
    "chain_id": 991,
    "parent_connection_type": "client"
}
EOF
    go run main.go config_temp.json 0x43Ec9D6170Aa97824ba8B5EC9e37b48927612F30 2>/dev/null | grep -E 'Nonce|Balance'
done
rm -f config_temp.json
