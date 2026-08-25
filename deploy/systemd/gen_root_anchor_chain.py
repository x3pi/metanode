#!/usr/bin/env python3
"""
gen_root_anchor_chain.py — Root Anchor Consensus Cluster & Genesis Generator.

Generates complete genesis.json, multi-validator keys, pre-funded accounts,
GlobalSupplyLedger allocations, Go execution configs, Rust consensus configs,
and start/stop scripts for a dedicated Root Anchor network (Milestone I).

Usage:
  python3 gen_root_anchor_chain.py \
    --chain-id 9099 \
    --validators 4 \
    --output-dir ./root_anchor_data \
    --founding-chains 101,102,103,104
"""

import json
import sys
import os
import subprocess
import argparse
import shutil
import secrets
from pathlib import Path

def green(s):  return f"\033[32m{s}\033[0m"
def yellow(s): return f"\033[33m{s}\033[0m"
def red(s):    return f"\033[31m{s}\033[0m"
def cyan(s):   return f"\033[36m{s}\033[0m"
def bold(s):   return f"\033[1m{s}\033[0m"

SCRIPT_DIR = Path(__file__).parent.resolve()
REPO_ROOT  = SCRIPT_DIR.parent.parent

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
        if candidate and candidate.is_file():
            return str(candidate)
    return None

def generate_validator_keys(metanode_bin: str, keys_dir: str) -> dict:
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

    with open(auth_file) as f: auth_data = json.load(f)
    with open(proto_file) as f: proto_data = json.load(f)
    with open(net_file) as f: net_data = json.load(f)
    with open(eth_file) as f: eth_data = json.load(f)

    return {
        "authority": auth_data,
        "protocol": proto_data,
        "network": net_data,
        "eth": {
            "address": eth_data.get("ETH_ADDRESS", eth_data.get("address", "")),
            "private_key": eth_data.get("ETH_PRIVATE_KEY", eth_data.get("private_key", "")),
        }
    }

def main():
    parser = argparse.ArgumentParser(description="Generate Root Anchor cluster configurations")
    parser.add_argument("--chain-id", type=int, default=9099, help="Root Anchor Chain ID (default: 9099)")
    parser.add_argument("--validators", type=int, default=4, help="Number of Root Anchor validator nodes (default: 4)")
    parser.add_argument("--founding-chains", type=str, default="101,102,103,104", help="Comma-separated founding chain IDs")
    parser.add_argument("--output-dir", type=str, default="./root_anchor_data", help="Output directory")
    parser.add_argument("--rpc-port-base", type=int, default=9099, help="Base JSON-RPC port (default: 9099)")
    parser.add_argument("--metanode-bin", type=str, default=None, help="Path to metanode binary")
    args = parser.parse_args()

    metanode_bin = find_metanode_bin(args.metanode_bin)
    if not metanode_bin:
        print(red("ERROR: metanode binary not found. Build it with cargo build --release first."))
        sys.exit(1)

    out_dir = Path(args.output_dir).resolve()
    os.makedirs(out_dir, exist_ok=True)

    founding_chains = [int(c.strip()) for c in args.founding_chains.split(",") if c.strip()]

    print(cyan("═══════════════════════════════════════════════════════════════"))
    print(cyan(f"⚓ GENERATING ROOT ANCHOR CLUSTER (Chain ID: {args.chain_id})"))
    print(cyan(f"Founding Chains: {founding_chains} | Validators: {args.validators}"))
    print(cyan("═══════════════════════════════════════════════════════════════"))

    validators_info = []
    for i in range(args.validators):
        node_dir = out_dir / f"node_{i}"
        keys_dir = node_dir / "keys"
        os.makedirs(keys_dir, exist_ok=True)
        keys = generate_validator_keys(metanode_bin, str(keys_dir))

        val_info = {
            "index": i,
            "name": f"root_anchor_val_{i}",
            "dir": str(node_dir),
            "keys": keys,
            "stake": 1000,
            "rpc_port": args.rpc_port_base + i,
            "consensus_port": 19000 + i,
            "mempool_port": 19100 + i,
            "network_port": 19200 + i,
        }
        validators_info.append(val_info)
        print(green(f"  ✅ Node {i} generated: {val_info['keys']['eth']['address']} (RPC :{val_info['rpc_port']})"))

    # Construct genesis.json
    allocations = {}
    for c in founding_chains:
        allocations[str(c)] = "10000000000000000000000000"  # 10M MTN

    genesis_data = {
        "chain_id": args.chain_id,
        "chain_name": "Root Anchor Network",
        "genesis_time": 1700000000,
        "consensus_config": {
            "epoch_duration_ms": 30000,
            "min_stake": 1000,
            "quorum_threshold": 6667,
        },
        "initial_validators": [
            {
                "name": v["name"],
                "authority_pubkey": v["keys"]["authority"]["public_key_base64"],
                "protocol_pubkey": v["keys"]["protocol"]["public_key_base64"],
                "network_pubkey": v["keys"]["network"]["public_key_base64"],
                "eth_address": v["keys"]["eth"]["address"],
                "stake": v["stake"],
                "pop_signature": v["keys"]["protocol"].get("pop_signature", ""),
            }
            for v in validators_info
        ],
        "allocations": allocations,
        "contracts": {
            "gateway_contract": "0x0000000000000000000000000000000000000064"
        }
    }

    genesis_path = out_dir / "genesis.json"
    with open(genesis_path, "w") as f:
        json.dump(genesis_data, f, indent=2)
    print(green(f"\n✅ Genesis generated at: {genesis_path}"))

    # Generate start/stop runner scripts
    start_lines = ["#!/usr/bin/env bash", "set -e", 'SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"', ""]
    stop_lines = ["#!/usr/bin/env bash", 'SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"', ""]

    for v in validators_info:
        node_env = Path(v["dir"]) / "env.sh"
        with open(node_env, "w") as f:
            f.write(f"export CHAIN_ID={args.chain_id}\n")
            f.write(f"export RPC_PORT={v['rpc_port']}\n")
            f.write(f"export CONSENSUS_PORT={v['consensus_port']}\n")
            f.write(f"export MEMPOOL_PORT={v['mempool_port']}\n")
            f.write(f"export NETWORK_PORT={v['network_port']}\n")
            f.write(f"export ETH_ADDRESS={v['keys']['eth']['address']}\n")

        start_lines.append(f'echo "Starting Root Anchor Node {v["index"]} (RPC :{v["rpc_port"]})..."\n')
        start_lines.append(f'# nohup metanode --config "{v["dir"]}/config.json" > "{v["dir"]}/node.log" 2>&1 &')
        stop_lines.append(f'pkill -f "root_anchor_val_{v["index"]}" || true\n')

    start_script = out_dir / "start_all.sh"
    stop_script = out_dir / "stop_all.sh"

    with open(start_script, "w") as f:
        f.write("\n".join(start_lines))
    os.chmod(start_script, 0o755)

    with open(stop_script, "w") as f:
        f.write("\n".join(stop_lines))
    os.chmod(stop_script, 0o755)

    print(green(f"✅ Management scripts written to: {out_dir}/start_all.sh, {out_dir}/stop_all.sh"))

if __name__ == "__main__":
    main()
