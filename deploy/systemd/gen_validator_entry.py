#!/usr/bin/env python3
"""
gen_validator_entry.py  — All-in-one validator key + genesis entry generator.

Generates BLS keys + ETH keys + complete genesis validator entry in ONE STEP using Rust keytool.
No Go/key_eth.go dependency.

Usage:
  python3 gen_validator_entry.py \
    --hostname  node-0 \
    --ip        1.2.3.4 \
    --description "My Validator" \
    --website   "https://myvalidator.com" \
    --keys-dir  ./validator_keys \
    --output    my_validator.json

  # Then merge into genesis:
  python3 update_genesis.py genesis.json my_validator.json

Options:
  --hostname    NAME      Validator hostname, e.g. node-0 (required)
  --ip          IP        Public IP of this validator (default: 127.0.0.1)
  --p2p-port    PORT      Rust consensus P2P port (default: 9000 + node_id)
  --primary-port PORT     Go P2P primary port (default: 6200 + node_id)
  --worker-port  PORT     Go worker port (default: 4012 + node_id)
  --description TEXT      Validator description
  --website     URL       Validator website
  --image       URL       Validator image URL
  --stake       AMOUNT    Stake in wei (default: 1000 MTN = 1000000000000000000000)
  --commission  RATE      Commission rate % (default: 5)
  --keys-dir    DIR       Where to save all generated keys (default: ./<hostname>_keys)
  --output      FILE      Output genesis entry JSON (default: <hostname>_genesis.json)
  --metanode-bin PATH     Path to metanode binary (auto-detected if not set)
"""

import json
import sys
import os
import subprocess
import argparse
import shutil
from pathlib import Path


# ─── Color output ─────────────────────────────────────────────────────────────
def green(s):  return f"\033[32m{s}\033[0m"
def yellow(s): return f"\033[33m{s}\033[0m"
def red(s):    return f"\033[31m{s}\033[0m"
def cyan(s):   return f"\033[36m{s}\033[0m"
def bold(s):   return f"\033[1m{s}\033[0m"


# ─── Auto-detect paths ────────────────────────────────────────────────────────
SCRIPT_DIR = Path(__file__).parent.resolve()
REPO_ROOT   = SCRIPT_DIR.parent.parent  # metanode/

METANODE_BIN_CANDIDATES = [
    SCRIPT_DIR / "bin" / "metanode",
    REPO_ROOT / "target/release/metanode",
    REPO_ROOT / "consensus/metanode/target/release/metanode",
    Path("/opt/metanode/bin/metanode"),
    Path(shutil.which("metanode") or ""),
]


def find_metanode_bin(override=None):
    if override:
        p = Path(override)
        if p.exists():
            return str(p)
        print(red(f"ERROR: metanode binary not found at: {override}"))
        sys.exit(1)
    for candidate in METANODE_BIN_CANDIDATES:
        if candidate and candidate.exists():
            return str(candidate)
    return None


# ─── Step 1 & 2: Generate keys via Rust keytool ─────────────────────────────
def generate_keys_via_keytool(metanode_bin: str, keys_dir: str) -> tuple:
    """
    Calls: metanode keytool generate validator --out-dir <keys_dir>
    Returns (bls_dict, eth_dict) by reading generated JSONs.
    """
    print(f"  → Generating keys via {cyan(metanode_bin)} keytool ...")
    os.makedirs(keys_dir, exist_ok=True)
    
    result = subprocess.run(
        [metanode_bin, "keytool", "generate", "validator", "--out-dir", keys_dir],
        capture_output=True, text=True
    )
    if result.returncode != 0:
        print(red(f"ERROR: metanode keytool failed:\n{result.stderr}"))
        sys.exit(1)

    # Read generated files
    auth_file = os.path.join(keys_dir, "authority_key.json")
    proto_file = os.path.join(keys_dir, "protocol_key.json")
    net_file = os.path.join(keys_dir, "network_key.json")
    eth_file = os.path.join(keys_dir, "eth_key.json")

    for fpath in [auth_file, proto_file, net_file, eth_file]:
        if not os.path.exists(fpath):
            print(red(f"ERROR: Key file not found after generation: {fpath}"))
            sys.exit(1)

    with open(auth_file) as f:
        auth_data = json.load(f)
    with open(proto_file) as f:
        proto_data = json.load(f)
    with open(net_file) as f:
        net_data = json.load(f)
    with open(eth_file) as f:
        eth_data = json.load(f)

    # Rust Metanode expects protocol and network keys to be a raw base64 string
    # containing 64 bytes (32 byte private key + 32 byte public key).
    import base64
    def rewrite_key_as_base64(key_data, file_path):
        priv_bytes = bytes.fromhex(key_data["private_key_hex"])
        pub_bytes = base64.b64decode(key_data["public_key_base64"])
        combined = priv_bytes + pub_bytes
        b64_str = base64.b64encode(combined).decode('utf-8')
        with open(file_path, "w") as fw:
            fw.write(b64_str)

    rewrite_key_as_base64(proto_data, proto_file)
    rewrite_key_as_base64(net_data, net_file)

    bls = {
        "authority_key": auth_data["public_key_base64"],
        "protocol_key": proto_data["public_key_base64"],
        "network_key": net_data["public_key_base64"],
        "authority_key_private": auth_data["private_key_hex"],
    }
    eth = {
        "private_key": eth_data["ETH_PRIVATE_KEY"],
        "address": eth_data["ETH_ADDRESS"]
    }
    return bls, eth


# ─── Step 3: Build validator entry ───────────────────────────────────────────
def build_validator_entry(bls: dict, eth: dict, args) -> dict:
    eth_address_lower = eth["address"].lower()  # with 0x
    primary_address   = f"{args.ip}:{args.primary_port}"
    worker_address    = f"{args.ip}:{args.worker_port}"
    p2p_address       = f"/ip4/{args.ip}/tcp/{args.p2p_port}"

    return {
        "address":                       eth_address_lower,
        "eth_private_key":               eth["private_key"],
        "primary_address":               primary_address,
        "worker_address":                worker_address,
        "p2p_address":                   p2p_address,
        "description":                   args.description or f"Validator {args.hostname}",
        "website":                       args.website,
        "image":                         args.image,
        "commission_rate":               args.commission,
        "min_self_delegation":           "1000000000000000000",
        "accumulated_rewards_per_share": "0",
        "delegator_stakes": [
            {"address": eth_address_lower, "amount": args.stake}
        ],
        "total_staked_amount": args.stake,
        "network_key":    bls["network_key"],
        "hostname":       args.hostname,
        "authority_key":  bls["authority_key"],
        "protocol_key":   bls["protocol_key"],
    }


def write_node_configs(bls: dict, eth: dict, args, keys_dir: str):
    """
    Write execution.json and consensus.toml directly instead of .env.
    """
    import json
    bls_private_hex  = bls.get("authority_key_private", "")
    eth_priv_stripped = eth["private_key"].lstrip("0x")
    eth_addr_stripped = eth["address"].lstrip("0x").lower()

    is_validator = (args.node_type == "validator")
    node_id          = getattr(args, "node_id",           0)
    install_dir      = f"/opt/metanode/node-{node_id}"
    rpc_port         = getattr(args, "rpc_port",          f":{10746 + node_id}")
    p2p_port         = getattr(args, "primary_port",      6200 + node_id)
    dns_port         = getattr(args, "dns_port",          9080 + node_id)
    peer_rpc_port    = getattr(args, "peer_rpc_port",     19200 + node_id)
    consensus_port   = getattr(args, "p2p_port",          9100 + node_id)
    snapshot_port    = getattr(args, "snapshot_port",     8600 + node_id)
    meta_rpc_port    = getattr(args, "meta_node_rpc_port",10100 + node_id)
    metrics_port     = getattr(args, "metrics_port",      9200 + node_id)
    
    # RPC is independent of validator status, controlled by --is-rpc
    is_rpc_node      = args.is_rpc
    
    # Explorer defaults to True for synconly nodes, unless specified
    is_explorer = getattr(args, "is_explorer", False) or (args.node_type == "synconly")
    
    snapshot_enabled = getattr(args, "snapshot_enabled", False)
    epochs_to_keep   = 5 if is_validator else 0
    commit_batch_size= 500 if is_validator else 100
    commit_batches_ahead = 128 if is_validator else 64

    # Parse peers map
    peers_map = {}
    if getattr(args, "peers_map", None):
        for pair in args.peers_map.split(","):
            if "=" in pair:
                nid_str, ip_str = pair.split("=", 1)
                peers_map[int(nid_str)] = ip_str

    # Dynamically build PEER_RPC_ADDRESSES
    peers = []
    for i in range(args.total_nodes):
        if i != node_id:
            peer_ip = peers_map.get(i, args.ip)
            peers.append(f'"{peer_ip}:{19200 + i}"')
    peer_rpc_str = ", ".join(peers)

    # Dynamically build Go list_sub_address
    go_peers = []
    for i in range(args.total_nodes):
        if i != node_id:
            peer_ip = peers_map.get(i, args.ip)
            go_peers.append(f"{peer_ip}:{6200 + i}")

    exec_json = {
        "debug": False,
        "go_mem_limit_gb": 32,
        "mvm_cache_enabled": False,
        "enable_private_gateway": True,
        "gateway_bls_key": "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
        "chainId": 991,
        "private_key": bls_private_hex,
        "address": eth_addr_stripped,
        "log_path": f"{install_dir}/logs/execution/go-master",
        "epochs_to_keep": epochs_to_keep,
        "backup_path": f"{install_dir}/data/execution/backup",
        "last_block_save_path": "/last_block.dat",
        "transaction_block_number_last_hash_path": "/transaction_block_number_last_hash",
        "block_hash_to_number_db_root_path": "/block_hash_to_number_db_root_path",
        "free_fee_addresses": [
            "55798165960a62cED34a0d86e36B1758D1303907",
            "0000000000000000000000000000000000000001",
            "Ea004b9aE1F60516210df2fDfcE9342618729d98"
        ],
        "cross_chain": {
            "config_contract": "0x4c1c27b3147820915431554F2B2383175FAAd198"
        },
        "meta_node_rpc_address": f"0.0.0.0:{meta_rpc_port}",
        "connection_address": f"0.0.0.0:{p2p_port}",
        "dns_server_address": f"0.0.0.0:{dns_port}",
        "version": "0.0.1.0",
        "list_type_service": "SUB-WRITE || MASTER || SUB-READ",
        "service_type": "MASTER",
        "rpc_port": rpc_port,
        "securepassword": "mysecretpassword",
        "peer_rpc_port": peer_rpc_port,
        "db_type": 2,
        "genesis_file_path": f"{install_dir}/config/genesis.json",
        "snapshot_enabled": snapshot_enabled,
        "snapshot_frequency_blocks": 50,
        "snapshot_block_offset": 0,
        "snapshot_max_snapshots": 3,
        "Databases": {
            "RootPath": f"{install_dir}/data/execution/db",
            "DBEngine": "sharded",
            "Version": "0.0.1.0",
            "BLSPrivateKey": bls_private_hex,
            "SnapshotPath": f"{install_dir}/data/execution/snapshots",
            "MaxPartSizeMB": 100,
            "ArchiveBaseName": "snapshot_archive",
            "pebble_cache_size_mb": 4096,
            "pebble_mem_table_size_mb": 256
        },
        "nodes": {
            "network_sync_enabled": False,
            "list_sub_address": go_peers,
            "dynamic_discovery": True
        },
        "snapshot_method": "hybrid",
        "snapshot_source_dir": f"{install_dir}/data/execution",
        "snapshot_server_port": snapshot_port,
        "state_backend": "nomt",
        "nomt_commit_concurrency": args.nomt_commit_concurrency,
        "nomt_page_cache_mb": 4096,
        "nomt_leaf_cache_mb": 4096,
        "rust_config_path": f"{install_dir}/config/consensus.toml",
        "is_explorer": is_explorer,
        "is_rpc_node": is_rpc_node,
        "explorer_db_path": f"{install_dir}/data/execution/explorer",
        "explorer_read_only_db_path": f"{install_dir}/data/execution/explorer-read-only",
        "log": {
            "level": "info",
            "format": "text",
            "console_output": True,
            "file_output": False
        },
        "tx_trace_enabled": False
    }

    import os
    exec_file = os.path.join(keys_dir, "execution.json")
    with open(exec_file, "w") as f:
        json.dump(exec_json, f, indent=4)

    consensus_toml = f"""node_id = {node_id}
network_address = "0.0.0.0:{consensus_port}"
protocol_key_path = "{install_dir}/keys/protocol_key.json"
network_key_path = "{install_dir}/keys/network_key.json"
storage_path = "{install_dir}/data/consensus"
enable_metrics = true
metrics_port = {metrics_port}
speed_multiplier = 1.0
time_based_epoch_change = true
max_clock_drift_seconds = 5
enable_ntp_sync = false
ntp_servers = [
    "pool.ntp.org",
    "time.google.com"
]
ntp_sync_interval_seconds = 300
executor_read_enabled = true
executor_commit_enabled = {str(is_validator).lower()}
commit_sync_batch_size = {commit_batch_size}
commit_sync_parallel_fetches = 32
commit_sync_batches_ahead = {commit_batches_ahead}
adaptive_catchup_enabled = true
adaptive_delay_enabled = false
adaptive_delay_ms = 20
min_round_delay_ms = 25
compact_blocks_enabled = true
leader_timeout_ms = 200
epoch_transition_optimization = "fast"
enable_gradual_shutdown = true
gradual_shutdown_user_cert_drain_secs = 2
gradual_shutdown_consensus_cert_drain_secs = 1
gradual_shutdown_final_drain_secs = 1
epoch_monitor_poll_interval_secs = 5
peer_rpc_port = {peer_rpc_port}
peer_rpc_addresses = [{peer_rpc_str}]
epochs_to_keep = {epochs_to_keep}
consensus_max_num_transactions_in_block = {args.consensus_max_txs_per_block}

[log]
level = "info"
format = "text"
console_output = true
file_output = false
"""
    cons_file = os.path.join(keys_dir, "consensus.toml")
    with open(cons_file, "w") as f:
        f.write(consensus_toml)

    # Write open_ports.sh
    open_ports_sh = f"""#!/bin/bash
# Auto-generated firewall rules for {args.hostname}

# Execution
sudo ufw allow {rpc_port.replace(':', '')}/tcp comment 'Execution RPC'
sudo ufw allow {p2p_port}/tcp comment 'Execution P2P'
sudo ufw allow {meta_rpc_port}/tcp comment 'Meta RPC'

# Consensus
sudo ufw allow {consensus_port}/tcp comment 'Consensus P2P'
sudo ufw allow {peer_rpc_port}/tcp comment 'Consensus Peer RPC'
sudo ufw allow {metrics_port}/tcp comment 'Consensus Metrics'

# Sync/Snapshot
sudo ufw allow {snapshot_port}/tcp comment 'Snapshot Server'

sudo ufw reload
echo "Ports opened for {args.hostname}!"
"""
    ports_script_file = os.path.join(keys_dir, "open_ports.sh")
    with open(ports_script_file, "w") as f:
        f.write(open_ports_sh)
    os.chmod(ports_script_file, 0o755)

    return exec_file, cons_file



# ─── Main ─────────────────────────────────────────────────────────────────────
def parse_args():
    parser = argparse.ArgumentParser(
        description="All-in-one: generate keys via keytool + genesis validator entry"
    )
    parser.add_argument("--hostname",     required=True, help="Validator hostname, e.g. node-0")
    parser.add_argument("--node-type",    default="validator", choices=["validator", "synconly"],
                        help="Node type: validator (default) or synconly")
    parser.add_argument("--is-rpc",       action="store_true", help="Enable RPC for this node")
    parser.add_argument("--is-explorer",  action="store_true", help="Enable Explorer for this node")
    parser.add_argument("--node-id",      type=int, default=0, help="Node index in genesis (default: 0)")
    parser.add_argument("--total-nodes",  type=int, default=5, help="Total number of nodes for auto-generating peers")
    parser.add_argument("--ip",           default="127.0.0.1")
    parser.add_argument("--p2p-port",     type=int, default=None, help="Rust consensus P2P port (default: 9100 + node_id)")
    parser.add_argument("--primary-port", type=int, default=None, help="Go P2P primary port (default: 6200 + node_id)")
    parser.add_argument("--worker-port",  type=int, default=None, help="Go worker port (default: 4012 + node_id)")
    parser.add_argument("--peers-map",    default=None, help="Comma-separated map of node_id=ip (e.g. 0=192.168.1.1,1=192.168.1.2)")
    parser.add_argument("--description",  default=None)
    parser.add_argument("--website",      default="")
    parser.add_argument("--image",        default="")
    parser.add_argument("--stake",        default="1000000000000000000000")
    parser.add_argument("--commission",   type=int, default=5)
    parser.add_argument("--keys-dir",     default=None, help="Directory to save keys (default: ./<hostname>_keys)")
    parser.add_argument("--output",       default=None, help="Output genesis entry JSON file")
    parser.add_argument("--metanode-bin", default=None)
    parser.add_argument("--consensus-max-txs-per-block", type=int, default=4000,
                        help="Maximum number of transactions proposed in a single consensus block (default: 4000)")
    parser.add_argument("--nomt-commit-concurrency", type=int, default=4,
                        help="NOMT commit worker count per trie instance (default: 4, matching NOMT's own "
                             "recommended default). Each node opens 2 NOMT instances, and each instance spins "
                             "up ~2x this many dedicated OS threads (beatree-sync + nomt-commit thread pools) — "
                             "independent of GOMAXPROCS/tokio tuning. The previous hardcoded value of 64 was "
                             "sized for a single validator with exclusive access to a large machine; on a "
                             "single-box rig running N co-located nodes it multiplies out to N x 2 x ~130 "
                             "threads, which was traced to real CPU/scheduler contention (load average > 2x "
                             "core count) during TX bursts. Raise this back up for genuine "
                             "one-node-per-machine production deployments.")
    parser.add_argument("--snapshot-enabled", action="store_true",
                        help="Enable snapshotting (requires Btrfs/XFS)")
    return parser.parse_args()


def main():
    args = parse_args()

    # Auto-calculate port defaults if not specified, based on node_id
    if args.p2p_port is None:
        args.p2p_port = 9100 + args.node_id
    if args.primary_port is None:
        args.primary_port = 6200 + args.node_id
    if args.worker_port is None:
        args.worker_port = 4012 + args.node_id

    keys_dir   = args.keys_dir or f"./{args.hostname}_keys"
    output     = args.output   or os.path.join(keys_dir, f"{args.hostname}_genesis.json")
    env_type   = args.node_type  # "validator" or "synconly"

    print(bold(f"\n🚀 Generating all keys for {env_type}: {cyan(args.hostname)}"))
    print(f"   Node type    : {cyan(env_type)}")
    print(f"   Keys will be saved to: {cyan(keys_dir)}")
    print(f"   Genesis entry will be: {cyan(output)}")
    print()

    # Auto-detect tools
    metanode_bin = find_metanode_bin(args.metanode_bin)

    if not metanode_bin:
        print(red("ERROR: metanode binary not found. Build it first:"))
        print(red("  cd consensus/metanode && cargo build --release"))
        print(red("  Or specify: --metanode-bin /path/to/metanode"))
        sys.exit(1)

    # Step 1 & 2: Generate keys directly via keytool
    print(bold("Step 1/2 — Generating validator keys (BLS + Ed25519 + ETH)"))
    bls, eth = generate_keys_via_keytool(metanode_bin, keys_dir)
    print(green(f"  ✅ Keys generated and saved to {keys_dir}"))
    print(green(f"  ✅ ETH address: {eth['address']}"))

    # Step 3: Build genesis entry
    print(bold("\nStep 2/2 — Building genesis validator entry"))
    entry = build_validator_entry(bls, eth, args)

    # Write genesis entry
    with open(output, "w") as f:
        json.dump(entry, f, indent=2)
        f.write("\n")

    # Auto merge into genesis.json if we are creating a validator
    if env_type == "validator":
        genesis_target = "genesis.json"
        genesis_template = "genesis.json.example"
        
        # Read from genesis.json if it exists, otherwise start from the example template
        genesis_source = genesis_target if os.path.exists(genesis_target) else genesis_template
        
        if os.path.exists(genesis_source):
            try:
                with open(genesis_source, "r") as gf:
                    g_data = json.load(gf)
                
                if "validators" in g_data:
                    updated = False
                    for i, v in enumerate(g_data["validators"]):
                        if v.get("hostname") == args.hostname:
                            g_data["validators"][i] = entry
                            updated = True
                            break
                    
                    if not updated:
                        g_data["validators"].append(entry)
                    
                    with open(genesis_target, "w") as gf:
                        json.dump(g_data, gf, indent=2)
                        gf.write("\n")
                    
                    print(bold(green(f"\n  ✅ Successfully auto-merged entry into {genesis_target} (source: {genesis_source})")))
                    
                    # Copy to simple_chain folder if it exists (source mode)
                    target_dir = os.path.join("..", "execution", "cmd", "simple_chain")
                    target_genesis = os.path.join(target_dir, "genesis.json")
                    if os.path.exists(target_dir):
                        shutil.copy2(genesis_target, target_genesis)
                        print(bold(green(f"  ✅ Automatically copied to {target_genesis}")))
                    else:
                        # In standalone release mode, we just copy to configs/genesis.json if configs/ exists
                        configs_dir = "configs"
                        if os.path.exists(configs_dir):
                            shutil.copy2(genesis_target, os.path.join(configs_dir, "genesis.json"))
                            print(bold(green(f"  ✅ Automatically copied to {configs_dir}/genesis.json")))
            except Exception as e:
                print(f"\n  ❌ Failed to auto-merge into {genesis_target}: {e}")
        else:
            print(f"\n  ⚠️ Warning: Neither {genesis_target} nor {genesis_template} found. Could not auto-merge.")
    else:
        # synconly node: must NOT be a consensus committee member. If this
        # hostname has a stale entry in genesis.json from a previous run
        # (e.g. it used to be a validator, or was misconfigured), remove it.
        # Leaving it in place silently inflates committee_size/quorum without
        # the node ever running a consensus authority — every real validator
        # then retries a doomed block-subscription to it forever, which was
        # traced to periodic multi-second block-delivery stalls under load.
        genesis_target = "genesis.json"
        if os.path.exists(genesis_target):
            try:
                with open(genesis_target, "r") as gf:
                    g_data = json.load(gf)

                if "validators" in g_data:
                    before = len(g_data["validators"])
                    g_data["validators"] = [
                        v for v in g_data["validators"] if v.get("hostname") != args.hostname
                    ]
                    removed = before - len(g_data["validators"])

                    if removed > 0:
                        with open(genesis_target, "w") as gf:
                            json.dump(g_data, gf, indent=2)
                            gf.write("\n")
                        print(bold(yellow(
                            f"\n  🧹 Removed stale validator entry for {args.hostname} from {genesis_target} "
                            f"(now synconly, not a committee member)"
                        )))

                        target_dir = os.path.join("..", "execution", "cmd", "simple_chain")
                        target_genesis = os.path.join(target_dir, "genesis.json")
                        if os.path.exists(target_dir):
                            shutil.copy2(genesis_target, target_genesis)
                            print(bold(green(f"  ✅ Automatically copied to {target_genesis}")))
                        else:
                            configs_dir = "configs"
                            if os.path.exists(configs_dir):
                                shutil.copy2(genesis_target, os.path.join(configs_dir, "genesis.json"))
                                print(bold(green(f"  ✅ Automatically copied to {configs_dir}/genesis.json")))
            except Exception as e:
                print(f"\n  ❌ Failed to prune stale entry from {genesis_target}: {e}")

    os.chmod(keys_dir, 0o700)

    # Write complete ready-to-use .env file
    exec_file, cons_file = write_node_configs(bls, eth, args, keys_dir)

    # Print summary
    print()
    print(bold(green("══════════════════════════════════════════════")))
    print(bold(green(f"  ✅ Done! All files saved to: {keys_dir}/")))
    print(bold(green("══════════════════════════════════════════════")))
    print()
    print(f"  📁 {keys_dir}/")
    for fname in sorted(os.listdir(keys_dir)):
        marker = "   "
        print(f"  {marker}  {fname}")
    print()
    print(f"  📄 Genesis entry : {cyan(output)}")
    print(f"  📄 Configs: execution.json, consensus.toml")
    print()
    print(bold("  🔴 BACKUP YOUR KEYS IMMEDIATELY:"))
    print(f"     cp -r {keys_dir} ~/backup_{args.hostname}_$(date +%Y%m%d)")
    print()
    print(bold("  📋 Next steps:"))
    print(f"  1. Use configs directly with install.sh:")
    print(f"       sudo bash install.sh --config-dir {keys_dir}")
    

    print()


if __name__ == "__main__":
    main()
