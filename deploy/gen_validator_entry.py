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
  python3 update_genesis.py genesis-main.json my_validator.json

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
REPO_ROOT   = SCRIPT_DIR.parent  # metanode/

METANODE_BIN_CANDIDATES = [
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


def write_validator_env(bls: dict, eth: dict, args, keys_dir: str) -> str:
    """
    Write a complete, ready-to-use validator.env (or synconly.env)
    with all generated keys pre-filled.
    """
    bls_private_hex  = bls.get("authority_key_private", "")
    eth_priv_stripped = eth["private_key"].lstrip("0x")
    eth_addr_stripped = eth["address"].lstrip("0x").lower()

    keys_dir_abs = os.path.abspath(keys_dir)
    protocol_key_path = os.path.join(keys_dir_abs, "protocol_key.json")
    network_key_path  = os.path.join(keys_dir_abs, "network_key.json")

    is_validator = (args.node_type == "validator")

    if is_validator:
        env_filename = os.path.join(keys_dir, "validator.env")
        node_type_comment = "validator"
        keys_section = f"""\
# ─── Network Keys (Validator only) ───────────────────────────────────────
PROTOCOL_KEY_FILE={protocol_key_path}
NETWORK_KEY_FILE={network_key_path}
"""
        snapshot_defaults = "SNAPSHOT_ENABLED=false\nSNAPSHOT_FREQUENCY=500\nSNAPSHOT_OFFSET=0"
        explorer_defaults  = "IS_EXPLORER=false\nEPOCHS_TO_KEEP=5"
    else:
        env_filename = os.path.join(keys_dir, "synconly.env")
        node_type_comment = "synconly"
        keys_section = f"""\
# ─── Network Keys (Sync-only still needs keys for P2P auth) ───────────────
PROTOCOL_KEY_FILE={protocol_key_path}
NETWORK_KEY_FILE={network_key_path}
"""
        snapshot_defaults = "SNAPSHOT_ENABLED=true\nSNAPSHOT_FREQUENCY=500\nSNAPSHOT_OFFSET=100"
        explorer_defaults  = "IS_EXPLORER=true\nEPOCHS_TO_KEEP=0"

    node_id          = getattr(args, "node_id",           0)
    rpc_port         = getattr(args, "rpc_port",          f":{10746 + node_id}")
    p2p_port         = getattr(args, "primary_port",      6200 + node_id)
    dns_port         = getattr(args, "dns_port",          9080 + node_id)
    peer_rpc_port    = getattr(args, "peer_rpc_port",     19200 + node_id)
    consensus_port   = getattr(args, "p2p_port",          9000 + node_id)
    snapshot_port    = getattr(args, "snapshot_port",     8600 + node_id)
    meta_rpc_port    = getattr(args, "meta_node_rpc_port",10100 + node_id)
    metrics_port     = getattr(args, "metrics_port",      9100 + node_id)

    # Dynamically build PEER_RPC_ADDRESSES
    peers = []
    for i in range(args.total_nodes):
        if i != node_id:
            peers.append(f'"{args.ip}:{19200 + i}"')
    peer_rpc_str = ", ".join(peers)

    content = f"""\
# ════════════════════════════════════════════════════════════════════
# Metanode {node_type_comment.title()} Node Configuration
# Generated by gen_validator_entry.py for hostname: {args.hostname}
#
# ✅ Keys are pre-filled. You still need to set:
#    - GENESIS_FILE   : path to genesis.json received from the team
#    - PEER_RPC_ADDRESSES : IP:port of ALL OTHER validators
#
# Usage:
#   sudo bash install.sh --config {os.path.basename(env_filename)}
# ════════════════════════════════════════════════════════════════════

# ─── Node Identity ───────────────────────────────────────────────────────
NODE_TYPE={node_type_comment}
NODE_ID={node_id}

{keys_section}
# ─── Execution Layer Keys ─────────────────────────────────────────────────
# Auto-generated — DO NOT share these with anyone
BLS_PRIVATE_KEY={bls_private_hex}
ETH_PRIVATE_KEY={eth_priv_stripped}
ETH_ADDRESS={eth_addr_stripped}

# ─── Source & Install paths ───────────────────────────────────────────────
REPO_URL=https://github.com/x3pi/metanode.git
REPO_BRANCH=main
BUILD_DIR=/opt/metanode/node-{node_id}/src
INSTALL_DIR=/opt/metanode/node-{node_id}
METANODE_USER=metanode


# ─── Network Ports ────────────────────────────────────────────────────────
RPC_PORT={rpc_port}
P2P_PORT={p2p_port}
DNS_PORT={dns_port}
PEER_RPC_PORT={peer_rpc_port}
CONSENSUS_PORT={consensus_port}
SNAPSHOT_SERVER_PORT={snapshot_port}
META_NODE_RPC_PORT={meta_rpc_port}
METRICS_PORT={metrics_port}

# ─── Peers ────────────────────────────────────────────────────────────────
# ⚠️  FILL THIS IN: IP:port of ALL OTHER validators (not yourself)
# Auto-generated for local cluster test ({args.total_nodes} nodes):
PEER_RPC_ADDRESSES='{peer_rpc_str}'

# ─── Snapshot settings ────────────────────────────────────────────────────
{snapshot_defaults}

# ─── Explorer / Archive ───────────────────────────────────────────────────
{explorer_defaults}
"""

    with open(env_filename, "w") as f:
        f.write(content)
    os.chmod(env_filename, 0o600)

    # ─── Write a custom firewall script for this node ────────────────────────
    fw_filename = os.path.join(keys_dir, "setup_firewall.sh")
    fw_content = f"""#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  Cấu hình Firewall (UFW) cho Node {node_id} ({node_type_comment})
#  Chạy dưới quyền root (sudo)
# ═══════════════════════════════════════════════════════════════════

if [[ $EUID -ne 0 ]]; then
   echo "❌ Script này cần chạy dưới quyền root (sudo)"
   exit 1
fi

echo "🔧 Đang cấu hình luật tường lửa UFW cho Node {node_id}..."

# Đảm bảo SSH không bị chặn
ufw allow ssh

# Mở cổng đồng thuận (Rust Consensus P2P)
ufw allow {consensus_port}/tcp comment "Metanode Consensus P2P"

# Mở cổng đồng bộ mempool (Go P2P)
ufw allow {p2p_port}/tcp comment "Metanode Execution P2P"

# Mở cổng Peer RPC (attest/sync)
ufw allow {peer_rpc_port}/tcp comment "Metanode Peer RPC"

# Mở cổng Snapshot Server
ufw allow {snapshot_port}/tcp comment "Metanode Snapshot Server"

# Mở cổng Metrics
ufw allow {metrics_port}/tcp comment "Metanode Metrics"

# Mở cổng RPC Client (MetaMask)
ufw allow {8545 + node_id}/tcp comment "Metanode RPC Proxy"

echo ""
echo "🚀 Cấu hình tường lửa hoàn tất!"
ufw status verbose
"""
    with open(fw_filename, "w") as f:
        f.write(fw_content)
    os.chmod(fw_filename, 0o700)

    return env_filename


# ─── Main ─────────────────────────────────────────────────────────────────────
def parse_args():
    parser = argparse.ArgumentParser(
        description="All-in-one: generate keys via keytool + genesis validator entry"
    )
    parser.add_argument("--hostname",     required=True, help="Validator hostname, e.g. node-0")
    parser.add_argument("--node-type",    default="validator", choices=["validator", "synconly"],
                        help="Node type: validator (default) or synconly")
    parser.add_argument("--node-id",      type=int, default=0, help="Node index in genesis (default: 0)")
    parser.add_argument("--total-nodes",  type=int, default=5, help="Total number of nodes for auto-generating peers")
    parser.add_argument("--ip",           default="127.0.0.1")
    parser.add_argument("--p2p-port",     type=int, default=None, help="Rust consensus P2P port (default: 9000 + node_id)")
    parser.add_argument("--primary-port", type=int, default=None, help="Go P2P primary port (default: 6200 + node_id)")
    parser.add_argument("--worker-port",  type=int, default=None, help="Go worker port (default: 4012 + node_id)")
    parser.add_argument("--description",  default=None)
    parser.add_argument("--website",      default="")
    parser.add_argument("--image",        default="")
    parser.add_argument("--stake",        default="1000000000000000000000")
    parser.add_argument("--commission",   type=int, default=5)
    parser.add_argument("--keys-dir",     default=None, help="Directory to save keys (default: ./<hostname>_keys)")
    parser.add_argument("--output",       default=None, help="Output genesis entry JSON file")
    parser.add_argument("--metanode-bin", default=None)
    return parser.parse_args()


def main():
    args = parse_args()

    # Auto-calculate port defaults if not specified, based on node_id
    if args.p2p_port is None:
        args.p2p_port = 9000 + args.node_id
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

    # Auto merge into genesis-main.json if it exists
    genesis_main_path = "genesis-main.json"
    if os.path.exists(genesis_main_path):
        try:
            with open(genesis_main_path, "r") as gf:
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
                
                with open(genesis_main_path, "w") as gf:
                    json.dump(g_data, gf, indent=2)
                    gf.write("\n")
                
                print(bold(green(f"\n  ✅ Successfully auto-merged entry into {genesis_main_path}")))
                
                # Copy to simple_chain folder
                target_genesis = "../execution/cmd/simple_chain/genesis.json"
                shutil.copy2(genesis_main_path, target_genesis)
                print(bold(green(f"  ✅ Automatically copied to {target_genesis}")))
                
        except Exception as e:
            print(f"\n  ❌ Failed to auto-merge into {genesis_main_path}: {e}")

    os.chmod(keys_dir, 0o700)

    # Write complete ready-to-use .env file
    env_file = write_validator_env(bls, eth, args, keys_dir)

    # Print summary
    print()
    print(bold(green("══════════════════════════════════════════════")))
    print(bold(green(f"  ✅ Done! All files saved to: {keys_dir}/")))
    print(bold(green("══════════════════════════════════════════════")))
    print()
    print(f"  📁 {keys_dir}/")
    for fname in sorted(os.listdir(keys_dir)):
        marker = "⚠️ " if fname.endswith(".env") else "   "
        print(f"  {marker}  {fname}")
    print()
    print(f"  📄 Genesis entry : {cyan(output)}")
    print(f"  📄 Install config: {cyan(env_file)}")
    print()
    print(bold("  🔴 BACKUP YOUR KEYS IMMEDIATELY:"))
    print(f"     cp -r {keys_dir} ~/backup_{args.hostname}_$(date +%Y%m%d)")
    print()
    print(bold("  📋 Next steps:"))
    print(f"  1. Edit {cyan(env_file)}:")
    print(f"       - Set PEER_RPC_ADDRESSES to the other validators' IPs")
    print(f"  2. Entry auto-merged into {cyan('genesis-main.json')}.")
    print(f"  3. Open firewall ports for this node:")
    print(f"       sudo bash {keys_dir}/setup_firewall.sh")
    print(f"  4. Once you receive genesis.json:")
    print(f"       sudo bash install.sh --config {env_file}")
    print()


if __name__ == "__main__":
    main()
