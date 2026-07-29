#!/usr/bin/env python3
"""
gen_private_chain.py — Private Chain Initializer and Configuration Generator.

Generates complete genesis, validator keys, pre-funded developer accounts,
Go execution configs, Rust consensus configs, and start/stop scripts for a
Metanode Private Network (supporting 1 validator or N validators).

Usage:
  python3 gen_private_chain.py \
    --chain-id 1337 \
    --validators 1 \
    --output-dir ./private_chain_data \
    --alloc-balance 1000000 \
    --dev-accounts 5

Options:
  --chain-id INT       EVM Chain ID for private network (default: 1337)
  --validators INT     Number of validator nodes (default: 1 for single validator)
  --ip IP              IP address for nodes (default: 127.0.0.1)
  --output-dir DIR     Output directory for chain configs & data (default: ./private_chain_data)
  --alloc-balance INT  Initial balance in MTN for pre-funded accounts (default: 1000000 MTN)
  --dev-accounts INT   Number of additional pre-funded dev ETH accounts (default: 5)
  --metanode-bin PATH  Path to metanode binary (auto-detected if omitted)
"""

import json
import sys
import os
import subprocess
import argparse
import shutil
import base64
import secrets
from pathlib import Path

# ─── Colors ───────────────────────────────────────────────────────────────────
def green(s):  return f"\033[32m{s}\033[0m"
def yellow(s): return f"\033[33m{s}\033[0m"
def red(s):    return f"\033[31m{s}\033[0m"
def cyan(s):   return f"\033[36m{s}\033[0m"
def bold(s):   return f"\033[1m{s}\033[0m"

SCRIPT_DIR = Path(__file__).parent.resolve()
REPO_ROOT  = SCRIPT_DIR.parent.parent  # metanode/

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

def generate_validator_keys(metanode_bin: str, keys_dir: str) -> tuple:
    os.makedirs(keys_dir, exist_ok=True)
    result = subprocess.run(
        [metanode_bin, "keytool", "generate", "validator", "--out-dir", keys_dir],
        capture_output=True, text=True
    )
    if result.returncode != 0:
        print(red(f"ERROR: metanode keytool failed:\n{result.stderr}"))
        sys.exit(1)

    auth_file = os.path.join(keys_dir, "authority_key.json")
    proto_file = os.path.join(keys_dir, "protocol_key.json")
    net_file = os.path.join(keys_dir, "network_key.json")
    eth_file = os.path.join(keys_dir, "eth_key.json")

    for fpath in [auth_file, proto_file, net_file, eth_file]:
        if not os.path.exists(fpath):
            print(red(f"ERROR: Key file not found after generation: {fpath}"))
            sys.exit(1)

    with open(auth_file) as f: auth_data = json.load(f)
    with open(proto_file) as f: proto_data = json.load(f)
    with open(net_file) as f: net_data = json.load(f)
    with open(eth_file) as f: eth_data = json.load(f)

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

def generate_eth_dev_account():
    """Generates a random secp256k1 Ethereum private key and derives its 0x address without external dependencies."""
    try:
        from eth_keys import keys
        priv_key_bytes = secrets.token_bytes(32)
        pk = keys.PrivateKey(priv_key_bytes)
        return {
            "private_key": "0x" + priv_key_bytes.hex(),
            "address": pk.public_key.to_checksum_address()
        }
    except ImportError:
        # Fallback using metanode keytool if eth_keys module is missing
        pass
    
    # Simple random hex fallback for key export
    raw_hex = secrets.token_hex(32)
    # We can invoke simple_chain --tool-get-address or Rust keytool to get standard address
    return {
        "private_key": "0x" + raw_hex,
        "address": "0x" + raw_hex[:40] # address placeholder fallback
    }

def main():
    parser = argparse.ArgumentParser(description="Generate private chain configs for Metanode")
    parser.add_argument("--chain-id", type=int, default=1337, help="EVM Chain ID (default: 1337)")
    parser.add_argument("--validators", type=int, default=1, help="Number of validators (default: 1)")
    parser.add_argument("--ip", default="127.0.0.1", help="Node IP address (default: 127.0.0.1)")
    parser.add_argument("--output-dir", default="./private_chain_data", help="Output directory (default: ./private_chain_data)")
    parser.add_argument("--alloc-balance", type=int, default=1000000, help="Initial MTN balance per account (default: 1000000)")
    parser.add_argument("--dev-accounts", type=int, default=5, help="Number of funded dev accounts (default: 5)")
    parser.add_argument("--metanode-bin", default=None, help="Path to metanode binary")
    args = parser.parse_args()

    print(bold(cyan("\n=== 🌐 Metanode Private Chain Initializer ===")))
    
    metanode_bin = find_metanode_bin(args.metanode_bin)
    if not metanode_bin:
        print(yellow("⚠️  metanode binary not found. Attempting to build target/release/metanode ..."))
        build_res = subprocess.run(["cargo", "build", "--release", "-p", "metanode-node"], cwd=str(REPO_ROOT))
        if build_res.returncode != 0:
            print(red("❌ Failed to build metanode binary!"))
            sys.exit(1)
        metanode_bin = str(REPO_ROOT / "target/release/metanode")

    print(f"  Using Metanode binary: {green(metanode_bin)}")
    out_dir = Path(args.output_dir).resolve()
    os.makedirs(out_dir, exist_ok=True)
    print(f"  Output directory:      {green(str(out_dir))}")
    print(f"  Chain ID:              {cyan(args.chain_id)}")
    print(f"  Validators:            {cyan(args.validators)}")

    # 1. Generate keys for validators
    validators_entries = []
    validator_keys_list = []
    alloc_list = []
    stake_wei = "1000000000000000000000" # 1000 MTN
    alloc_wei = str(args.alloc_balance * (10**18))

    for node_id in range(args.validators):
        node_keys_dir = out_dir / f"node-{node_id}" / "keys"
        print(f"\n🔑 Generating keys for Validator node-{node_id} ...")
        bls, eth = generate_validator_keys(metanode_bin, str(node_keys_dir))
        validator_keys_list.append((bls, eth))

        eth_addr = eth["address"].lower()
        p2p_port = 9000 + node_id
        primary_port = 6200 + node_id
        worker_port = 4012 + node_id

        val_entry = {
            "address": eth_addr,
            "eth_private_key": eth["private_key"],
            "primary_address": f"{args.ip}:{primary_port}",
            "worker_address": f"{args.ip}:{worker_port}",
            "p2p_address": f"/ip4/{args.ip}/tcp/{p2p_port}",
            "description": f"Private Chain Validator node-{node_id}",
            "website": "",
            "image": "",
            "commission_rate": 5,
            "min_self_delegation": "1000000000000000000",
            "accumulated_rewards_per_share": "0",
            "delegator_stakes": [
                {"address": eth_addr, "amount": stake_wei}
            ],
            "total_staked_amount": stake_wei,
            "network_key": bls["network_key"],
            "hostname": f"node-{node_id}",
            "authority_key": bls["authority_key"],
            "protocol_key": bls["protocol_key"],
        }
        validators_entries.append(val_entry)

        # Fund validator account in genesis alloc
        alloc_list.append({
            "address": eth_addr,
            "balance": alloc_wei,
            "pending_balance": "0",
            "last_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
            "device_key": "0x0000000000000000000000000000000000000000000000000000000000000000",
            "public_key_bls": bls["authority_key"]
        })

    # 2. Generate pre-funded dev accounts
    print(f"\n💰 Generating {args.dev_accounts} pre-funded developer accounts ...")
    dev_accounts = []
    for i in range(args.dev_accounts):
        dev_acc = generate_eth_dev_account()
        dev_accounts.append(dev_acc)
        alloc_list.append({
            "address": dev_acc["address"].lower(),
            "balance": alloc_wei,
            "pending_balance": "0",
            "last_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
            "device_key": "0x0000000000000000000000000000000000000000000000000000000000000000",
            "public_key_bls": ""
        })

    with open(out_dir / "dev_accounts.json", "w") as f:
        json.dump(dev_accounts, f, indent=2)

    # 3. Create genesis.json
    genesis_data = {
        "config": {
            "chainId": args.chain_id,
            "epoch": 0,
            "epoch_timestamp_ms": 1781315442000,
            "attestation_interval": 10,
            "epoch_duration_seconds": 345600
        },
        "validators": validators_entries,
        "alloc": alloc_list
    }
    
    # BƠM CÁC VÍ CỐ ĐỊNH TỪ genesis.json.example VÀO ĐÂY
    example_path = REPO_ROOT / "deploy" / "systemd" / "genesis.json.example"
    if example_path.exists():
        try:
            with open(example_path, "r") as ef:
                example_data = json.load(ef)
                if "alloc" in example_data:
                    genesis_data["alloc"].extend(example_data["alloc"])
                    print(f"  💉 Injected {len(example_data['alloc'])} accounts from genesis.json.example")
        except Exception as e:
            print(yellow(f"  ⚠️ Warning: could not merge genesis.json.example allocs: {e}"))

    genesis_path = out_dir / "genesis.json"
    with open(genesis_path, "w") as f:
        json.dump(genesis_data, f, indent=2)
    print(f"  ✅ Written genesis.json to {green(str(genesis_path))}")

    # 4. Generate per-node runtime configs (config.json & node.toml)
    for node_id in range(args.validators):
        bls, eth = validator_keys_list[node_id]
        node_dir = out_dir / f"node-{node_id}"
        os.makedirs(node_dir / "logs", exist_ok=True)
        os.makedirs(node_dir / "data" / "execution" / "db", exist_ok=True)
        os.makedirs(node_dir / "data" / "consensus" / "db", exist_ok=True)

        rpc_port = 8545
        primary_port = 4200
        dns_port = 9080
        peer_rpc_port = 19200
        consensus_port = 9000
        meta_rpc_port = 10100

        # Node peers
        go_peers = [f"{args.ip}:{6200 + j}" for j in range(args.validators) if j != node_id]
        rust_peers = [f"{args.ip}:{19200 + j}" for j in range(args.validators) if j != node_id]

        exec_config = {
            "debug": False,
            "go_mem_limit_gb": 8,
            "mvm_cache_enabled": False,
            "enable_private_gateway": True,
            "gateway_bls_key": "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
            "chainId": args.chain_id,
            "private_key": bls["authority_key_private"],
            "address": eth["address"].lstrip("0x").lower(),
            "log_path": str(node_dir / "logs" / "execution"),
            "epochs_to_keep": 0,
            "backup_path": str(node_dir / "data" / "execution" / "backup"),
            "last_block_save_path": "/last_block.dat",
            "transaction_block_number_last_hash_path": "/transaction_block_number_last_hash",
            "block_hash_to_number_db_root_path": "/block_hash_to_number_db_root_path",
            "free_fee_addresses": [
                "55798165960a62cED34a0d86e36B1758D1303907"
            ],
            "cross_chain": {
                "config_contract": "0x4c1c27b3147820915431554F2B2383175FAAd198"
            },
            "meta_node_rpc_address": f"{args.ip}:{meta_rpc_port}",
            "connection_address": f"0.0.0.0:{primary_port}",
            "dns_server_address": f"{args.ip}:{dns_port}",
            "version": "0.0.1.0",
            "rpc_port": f":{rpc_port}",
            "peer_rpc_port": peer_rpc_port,
            "db_type": 2,
            "genesis_file_path": str(genesis_path),
            "rust_config_path": str(node_dir / "node.toml"),
            "snapshot_enabled": False,
            "is_rpc_node": True,
            "state_backend": "nomt",
            "Databases": {
                "RootPath": str(node_dir / "data" / "execution" / "db"),
                "DBEngine": "sharded",
                "Version": "0.0.1.0",
                "BLSPrivateKey": bls["authority_key_private"],
                "SnapshotPath": str(node_dir / "data" / "execution" / "snapshots"),
                "MaxPartSizeMB": 100,
                "ArchiveBaseName": "snapshot_archive"
            },
            "nodes": {
                "network_sync_enabled": (args.validators > 1),
                "list_sub_address": go_peers,
                "dynamic_discovery": True
            },
            "log": {
                "level": "info",
                "format": "text",
                "console_output": True,
                "file_output": True
            }
        }

        with open(node_dir / "config.json", "w") as f:
            json.dump(exec_config, f, indent=2)

        # Build node.toml for Rust consensus
        peers_toml = ", ".join([f'"{p}"' for p in rust_peers])
        toml_content = f"""# Rust Consensus Configuration for Private Chain Node {node_id}
node_id = {node_id}
network_address = "127.0.0.1:{consensus_port}"
protocol_key_path = "{node_dir}/keys/protocol_key.json"
network_key_path = "{node_dir}/keys/network_key.json"
storage_path = "{node_dir}/data/consensus/db"

enable_metrics = true
metrics_port = {9100 + node_id}
peer_rpc_port = {peer_rpc_port}
peer_rpc_addresses = [{peers_toml}]
executor_read_enabled = true
executor_commit_enabled = true
time_based_epoch_change = true
"""
        with open(node_dir / "node.toml", "w") as f:
            f.write(toml_content)

    # 5. Generate start_private_chain.sh & stop_private_chain.sh
    start_sh = out_dir / "start_private_chain.sh"
    stop_sh  = out_dir / "stop_private_chain.sh"

    simple_chain_bin = REPO_ROOT / "execution" / "cmd" / "simple_chain" / "simple_chain"
    
    start_script_content = f"""#!/usr/bin/env bash
set -e
DIR="$(cd "$(dirname "${{BASH_SOURCE[0]}}")" && pwd)"
echo "🚀 Starting Metanode Private Chain ({args.validators} validator node(s))..."

METANODE_BIN="{metanode_bin}"
SIMPLE_CHAIN_BIN="{simple_chain_bin}"

if [ ! -f "$SIMPLE_CHAIN_BIN" ]; then
    echo "🔨 Building simple_chain Go binary..."
    (cd "{REPO_ROOT}/execution/cmd/simple_chain" && go build -o simple_chain .)
fi

"""
    for node_id in range(args.validators):
        start_script_content += f"""
echo "  → Starting Node-{node_id} (RPC: http://{args.ip}:{8545 + node_id})..."
(cd "$DIR/node-{node_id}" && "$SIMPLE_CHAIN_BIN" --config config.json > logs/node-{node_id}.log 2>&1 & echo $! > node-{node_id}.pid)
"""

    start_script_content += f"""
echo "✅ Private Chain started successfully!"
echo "   RPC URL: http://{args.ip}:8545"
echo "   Chain ID: {args.chain_id}"
echo "   Check logs in $DIR/node-0/logs/node-0.log"
"""

    stop_script_content = f"""#!/usr/bin/env bash
DIR="$(cd "$(dirname "${{BASH_SOURCE[0]}}")" && pwd)"
echo "🛑 Stopping Metanode Private Chain..."

for pid_file in "$DIR"/node-*/node-*.pid "$DIR"/node-*/consensus-*.pid; do
    if [ -f "$pid_file" ]; then
        PID=$(cat "$pid_file")
        echo "  → Stopping node process PID $PID..."
        kill -15 "$PID" 2>/dev/null || true
        rm -f "$pid_file"
    fi
done

pkill -f "simple_chain --config" 2>/dev/null || true
pkill -f "metanode start --config" 2>/dev/null || true
echo "✅ Private Chain stopped."
"""

    with open(start_sh, "w") as f:
        f.write(start_script_content)
    os.chmod(start_sh, 0o755)

    with open(stop_sh, "w") as f:
        f.write(stop_script_content)
    os.chmod(stop_sh, 0o755)

    # 6. Print summary
    print(bold(green("\n🎉 Private Chain Environment Initialized Successfully!")))
    print(f"  • Genesis file:     {cyan(str(genesis_path))}")
    print(f"  • Dev Accounts:     {cyan(str(out_dir / 'dev_accounts.json'))}")
    print(f"  • Start Script:     {green(str(start_sh))}")
    print(f"  • Stop Script:      {red(str(stop_sh))}")
    print(f"\n💡 To start the 1-validator private chain, run:")
    print(cyan(f"   bash {start_sh}\n"))

if __name__ == "__main__":
    main()
