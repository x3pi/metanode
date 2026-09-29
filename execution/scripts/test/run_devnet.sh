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
# exec1 and exec2 are each meant to be an ISOLATED single-node chain (self-quorum), not
# members of the real 4-validator committee that cmd/simple_chain/genesis.json ships with by
# default. That default genesis.json's `validators` array embeds the SAME committee identity
# (authority_key/protocol_key/network_key) used by other, already-running clusters on this
# shared machine — reusing it unmodified means exec1/exec2 can only ever hold 1000/4000 of the
# stake needed for quorum (2666), so they'd NEVER produce a block past genesis (confirmed live:
# 13+ minute permanent freeze). Each exec node gets its OWN genesis with a single validator
# entry matching its OWN freshly generated Rust identity (consensus/metanode/config/devnet_execN_keys/,
# generated once via `metanode generate --nodes 1`, wired into node_devnet_execN.toml's
# protocol_key_path/network_key_path) so its own signature alone reaches quorum (f=0).
python3 -c "
import json
with open('../../cmd/simple_chain/genesis.json') as f:
    base = json.load(f)

devnet_sender_alloc = {
    'address': '0xb4eb43848E94de7BE8e2b551063dcE2aBeB8ba24',
    'balance': '2000000000000000000000000000000',
    'pending_balance': '0',
    'last_hash': '0x0000000000000000000000000000000000000000000000000000000000000000',
    'device_key': '0x0000000000000000000000000000000000000000000000000000000000000000',
    'publicKeyBls': '0xb518c65d0f5f23858fd28f0473cb1fbaccc8aaa960880aee841585861f245abc0c4e4dce5b3693cfe60da4902d9484bc',
}

def build_genesis(exec_name, validator_address, committee_path, self_alloc):
    with open(committee_path) as f:
        committee = json.load(f)
    authority = committee['authorities'][0]
    stake_amount = '1000000000000000000000'  # 1000 whole tokens, matches base genesis's per-validator convention
    g = dict(base)
    g['validators'] = [{
        'address': validator_address,
        'primary_address': '127.0.0.1:4000',
        'worker_address': '127.0.0.1:4012',
        'p2p_address': authority['address'],
        'description': f'Devnet {exec_name} self-quorum validator',
        'website': '',
        'image': '',
        'commission_rate': 5,
        'min_self_delegation': '1000000000000000000',
        'accumulated_rewards_per_share': '0',
        'delegator_stakes': [{'address': validator_address, 'amount': stake_amount}],
        'total_staked_amount': stake_amount,
        'network_key': authority['network_key'],
        'hostname': authority['hostname'],
        'authority_key': authority['authority_key'],
        'protocol_key': authority['protocol_key'],
    }]
    # Single validator: quorum/validity thresholds just need to be reachable by itself (f=0).
    g['total_stake'] = 1000
    g['quorum_threshold'] = 1000
    g['validity_threshold'] = 1000
    # Extra independent devnet senders (devnet_senders.json, used by stress_concurrent_transfers.go)
    # so concurrent transfers each get their own nonce sequence instead of racing on one account.
    with open('devnet_senders.json') as sf:
        extra = [{
            'address': s['address'],
            'balance': '2000000000000000000000000000000',
            'pending_balance': '0',
            'last_hash': '0x0000000000000000000000000000000000000000000000000000000000000000',
            'device_key': '0x0000000000000000000000000000000000000000000000000000000000000000',
            'publicKeyBls': s['public_key_bls'],
        } for s in json.load(sf)]
    g['alloc'] = list(base.get('alloc', [])) + [devnet_sender_alloc, self_alloc] + extra
    return g

# Each exec node's own app.keyPair identity (bls.NewKeyPair(config.PrivateKey), address =
# keccak256(compressed BLS pubkey)[12:]) is what SignS its own ROLLUP-PROPOSER system event
# txs (rollup.RollupSystemAddress, submitted from app.go's eventProposer). This address happens
# to equal validator_address above (both derived from the same private_key), but the genesis
# validators[] entry above only carries Rust-consensus-level authority_key/protocol_key -- it
# does NOT populate the Go-side AccountState.PublicKeyBls() field that transaction signature
# verification (pkg/blockchain/tx_processor/validation.go) checks against. Without a matching
# alloc[] entry (the ONLY genesis section that sets PublicKeyBls, same mechanism as
# devnet_sender_alloc above), every self-signed system tx from this address permanently fails
# with 'invalid sign' -- found live: ReceiveWorker successfully proposed EventCreditObserved to
# TxValidatorPool, but every attempt was rejected at signature verification since
# AccountStateReadOnly(app.keyPair.Address()).PublicKeyBls() came back empty. These addresses
# and BLS public keys are precomputed from run_devnet.sh's own hardcoded exec1/exec2
# private_key values below (bls.KeyPair.Address()/BytesPublicKey()) -- if those private_key
# values are ever regenerated, these must be recomputed too (see execution/scripts/test's
# throwaway go-run snippet used to derive them).
exec1_self_alloc = {
    'address': '0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5',
    'balance': '2000000000000000000000000000000',
    'pending_balance': '0',
    'last_hash': '0x0000000000000000000000000000000000000000000000000000000000000000',
    'device_key': '0x0000000000000000000000000000000000000000000000000000000000000000',
    'publicKeyBls': '0x944488b425d29336c7913a3b45946adee6b9bfbd0838c6c8f422f4b4277066f26b3da0530c9f9865e6e534a05ae6c128',
}
exec2_self_alloc = {
    'address': '0x0d4CC97b62a149a8fe8DE81262270426A80B0935',
    'balance': '2000000000000000000000000000000',
    'pending_balance': '0',
    'last_hash': '0x0000000000000000000000000000000000000000000000000000000000000000',
    'device_key': '0x0000000000000000000000000000000000000000000000000000000000000000',
    'publicKeyBls': '0x83221629eeff1a69aa96ac6aadea402a7b62a74647633c0743cd517b71dcd5cd39fec42841b953fc481dac039bceb465',
}

exec1_genesis = build_genesis('exec1', '0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5', '../../../consensus/metanode/config/devnet_exec1_keys/committee.json', exec1_self_alloc)
exec2_genesis = build_genesis('exec2', '0x0d4CC97b62a149a8fe8DE81262270426A80B0935', '../../../consensus/metanode/config/devnet_exec2_keys/committee.json', exec2_self_alloc)

with open('devnet_data/exec1/genesis.json', 'w') as f:
    json.dump(exec1_genesis, f, indent=2)
with open('devnet_data/exec2/genesis.json', 'w') as f:
    json.dump(exec2_genesis, f, indent=2)
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
  "genesis_file_path": "./devnet_data/exec1/genesis.json",
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
  "private_key": "2a61eac9235fab64ae377b2b7e39f8fa9648c5737094d28c7ee68a14b5086d39",
  "address": "0x0d4CC97b62a149a8fe8DE81262270426A80B0935",
  "log_path": "./devnet_data/exec2/logs",
  "backup_path": "./devnet_data/exec2/backup",
  "explorer_db_path": "./devnet_data/exec2/explorer",
  "explorer_read_only_db_path": "./devnet_data/exec2/explorer-ro",
  "is_explorer": true,
  "connection_address": "0.0.0.0:4202",
  "version": "0.0.1.0",
  "rpc_port": ":8647",
  "db_type": 2,
  "genesis_file_path": "./devnet_data/exec2/genesis.json",
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

# Staggered on purpose: starting exec1 and exec2 back-to-back was observed to race during Rust
# consensus network-server startup (both processes transiently colliding on the same port
# regardless of their own configured network_address, one winning and the other panicking with
# AddrInUse and permanently losing its consensus engine for the rest of the run). Not fully
# root-caused; this delay is a mitigation, not a fix.
sleep 3

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
