#!/bin/bash
set -e

echo "==> Building binaries..."
cd ../../cmd/parent_chain && go build -o ../../scripts/test/parent_chain
cd ../simple_chain && go build -o ../../scripts/test/simple_chain
cd ../../scripts/test

echo "==> Cleaning up old data..."
rm -rf devnet_data
mkdir -p devnet_data/parent
mkdir -p devnet_data/exec1
mkdir -p devnet_data/exec2
rm -rf ../../../consensus/metanode/config/storage/*

echo "==> CONFIGURE PARENT CHAIN"
cat << 'EOF' > devnet_data/parent/config.json
{
  "http_port": ":8547",
  "log_level": 3
}
EOF

echo "==> CONFIGURE EXECUTION CLUSTERS"
# Use existing genesis, plus a self-generated devnet test account (own ECDSA+BLS keypair,
# not a well-known/shared one) funded and BLS-registered so mtn_sendCrossChainTransfer's
# devnet sender has a real, matching key pair to sign with — see mtn_api.go's
# devnetSenderPrivateKeyHex/devnetSenderBLSPrivateKeyHex for the matching private keys.
python3 -c "
import json
with open('../../cmd/simple_chain/genesis.json') as f:
    g = json.load(f)
g['alloc'].append({
    'address': '0xB3b7335d78eEA5DA565dD7C726d063A2A4C520e1',
    'balance': '2000000000000000000000000000000',
    'pending_balance': '0',
    'last_hash': '0x0000000000000000000000000000000000000000000000000000000000000000',
    'device_key': '0x0000000000000000000000000000000000000000000000000000000000000000',
    'publicKeyBls': '0x87a5944933935c634d15186b464d20b3c9ed69d12d74c6aec44e130b6e052124e387b7c472a9b67a557b9fc5fcc980f3',
})
with open('devnet_data/genesis.json', 'w') as f:
    json.dump(g, f, indent=2)
"

cat << 'EOF' > devnet_data/exec1/config.json
{
  "debug": true,
  "enable_private_gateway": false,
  "master_password": "devnet-test-password",
  "app_pepper": "devnet-test-pepper",
  "private_key": "0f326c0b9bb86353ac317dd8f9b045fd1877473674ba24500139fed777b26a0c",
  "address": "0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5",
  "log_path": "./devnet_data/exec1/logs",
  "backup_path": "./devnet_data/exec1/backup",
  "explorer_db_path": "./devnet_data/exec1/explorer",
  "explorer_read_only_db_path": "./devnet_data/exec1/explorer-ro",
  "is_explorer": true,
  "connection_address": "0.0.0.0:4200",
  "version": "0.0.1.0",
  "rpc_port": ":8646",
  "db_type": 2,
  "genesis_file_path": "./devnet_data/genesis.json",
  "pk_admin_file_storage": "87d931eaa2f76709f2615586e0d560ca9b80f247c9cc431e197ba3e7167db623",
  "bls_admin_storage": "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
  "owner_file_storage_address": "0xC6E6474A8DEAD25B0e75b1aeA5d35FA19f69588a",
  "Databases": {
    "RootPath": "./devnet_data/exec1/data",
    "DBEngine": "sharded",
    "Version": "0.0.1.0",
    "BLSPrivateKey": "5fb8d1ceadf4059adca5c106dbd91452be8c433b2c38c5dd85c50f1c7da4c85c",
    "SnapshotPath": "./devnet_data/exec1/snapshot"
  },
  "rust_config_path": "../../../consensus/metanode/config/node_devnet_exec1.toml",
  "is_rpc_node": true
}
EOF

cat << 'EOF' > devnet_data/exec2/config.json
{
  "debug": true,
  "enable_private_gateway": false,
  "master_password": "devnet-test-password",
  "app_pepper": "devnet-test-pepper",
  "private_key": "0f326c0b9bb86353ac317dd8f9b045fd1877473674ba24500139fed777b26a0c",
  "address": "0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5",
  "log_path": "./devnet_data/exec2/logs",
  "backup_path": "./devnet_data/exec2/backup",
  "explorer_db_path": "./devnet_data/exec2/explorer",
  "explorer_read_only_db_path": "./devnet_data/exec2/explorer-ro",
  "is_explorer": true,
  "connection_address": "0.0.0.0:4202",
  "version": "0.0.1.0",
  "rpc_port": ":8647",
  "db_type": 2,
  "genesis_file_path": "./devnet_data/genesis.json",
  "pk_admin_file_storage": "87d931eaa2f76709f2615586e0d560ca9b80f247c9cc431e197ba3e7167db623",
  "bls_admin_storage": "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
  "owner_file_storage_address": "0xC6E6474A8DEAD25B0e75b1aeA5d35FA19f69588a",
  "Databases": {
    "RootPath": "./devnet_data/exec2/data",
    "DBEngine": "sharded",
    "Version": "0.0.1.0",
    "BLSPrivateKey": "2d21f977fd594b7587439a91ab1bb5523573f02e5ba7e3e61cd8e895470d400a",
    "SnapshotPath": "./devnet_data/exec2/snapshot"
  },
  "rust_config_path": "../../../consensus/metanode/config/node_devnet_exec2.toml",
  "is_rpc_node": true
}
EOF

echo "==> STARTING NODES"
./parent_chain -data-dir ./devnet_data/parent -http :8547 -rust-config ../../../consensus/metanode/config/node_devnet_parent.toml > ./devnet_data/parent/node.log 2>&1 &
PARENT_PID=$!
echo "Parent Chain PID: $PARENT_PID"

sleep 2

./simple_chain -config ./devnet_data/exec1/config.json > ./devnet_data/exec1/node.log 2>&1 &
EXEC1_PID=$!
echo "Exec 1 PID: $EXEC1_PID"

./simple_chain -config ./devnet_data/exec2/config.json > ./devnet_data/exec2/node.log 2>&1 &
EXEC2_PID=$!
echo "Exec 2 PID: $EXEC2_PID"

echo "==> Devnet is running!"
echo "To view logs:"
echo "  tail -f devnet_data/parent/node.log"
echo "  tail -f devnet_data/exec1/node.log"
echo "  tail -f devnet_data/exec2/node.log"
echo "To stop the devnet, run:"
echo "  kill $PARENT_PID $EXEC1_PID $EXEC2_PID"
