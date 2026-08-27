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
import base64
import hashlib
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

_BLS_PUBKEY_BIN_CACHE = None

def derive_min_pk_pubkey(secret_hex: str) -> str:
    """Derives the real pkg/bls (min-pk, 48-byte G1) public key from a BLS secret scalar, as
    base64 -- same real fix as gen_single_chain.py's derive_min_pk_pubkey (see its doc comment
    for the full story). This script had the identical bug: writing
    v["keys"]["authority"]["public_key_base64"] (min_sig/G2, AND missing the "0x" hex prefix
    AccountState's genesis loader requires -- common.FromHex() on a bare base64 string silently
    decodes to 0 bytes, confirmed empirically) straight into alloc[].publicKeyBls."""
    global _BLS_PUBKEY_BIN_CACHE
    if _BLS_PUBKEY_BIN_CACHE is None:
        bin_path = REPO_ROOT / "execution" / "bls_pubkey"
        if not bin_path.exists():
            print(cyan("🔨 Building bls_pubkey helper (execution/cmd/tool/bls_pubkey)..."))
            result = subprocess.run(
                ["go", "build", "-o", str(bin_path), "./cmd/tool/bls_pubkey"],
                cwd=str(REPO_ROOT / "execution"), capture_output=True, text=True,
            )
            if result.returncode != 0:
                print(red(f"ERROR: failed to build bls_pubkey helper:\n{result.stderr}"))
                sys.exit(1)
        _BLS_PUBKEY_BIN_CACHE = str(bin_path)
    result = subprocess.run(
        [_BLS_PUBKEY_BIN_CACHE, "-secret", secret_hex],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        print(red(f"ERROR: bls_pubkey helper failed:\n{result.stderr}"))
        sys.exit(1)
    return result.stdout.strip()

def derive_devnet_submitter_account(chain_id: int):
    """Deterministically derive a devnet-only secp256k1 keypair for the
    CommitAttestationWorker "submitter" account of a given private chain ID.

    Root Anchor's genesis is generated BEFORE the private chains it will later
    register (gen_root_anchor_chain.py runs first in setup_root_anchor.sh, then
    setup_4_private_chains.sh generates the private chains and their submitter
    keys) -- so there is no way for a *randomly*-generated submitter key to ever
    be present in Root Anchor's genesis alloc. Every submitCommitAttestation()
    tx from such a key was rejected by Root Anchor's tx-validation gate with
    "no BLS public key registered on-chain" (the same generic "every tx sender
    needs a registered BLS pubkey" gate documented next to gateway_bls_key
    below), permanently blocking Milestone F's real BLS-share submission.

    Fix: derive the key deterministically from the chain ID alone (sha256 of a
    fixed seed string), so BOTH this script (at Root Anchor genesis time) and
    setup_4_private_chains.sh (at private-chain genesis time) can independently
    compute the *same* keypair for a given chain ID with zero data-passing
    between the two scripts, and pre-register its address here with the same
    shared devnet BLS pubkey already used for the "known dev account" below.

    DEVNET ONLY. This key is derivable by anyone who reads this source file --
    never use it to hold real value. Production deployments must generate a
    real, secret, per-chain submitter key and register it on Root Anchor
    (a real registration transaction/process, not a hardcoded genesis alloc).
    """
    from eth_account import Account
    seed = f"metanode-devnet-submitter-chain-{chain_id}".encode()
    priv_hex = hashlib.sha256(seed).hexdigest()
    address = Account.from_key(priv_hex).address
    return priv_hex, address

# Devnet-only fallback -- see note/security_variables_reference.md mục 3.1. Kept as the
# default so existing devnet/smoke-test flows are unchanged; pass --gateway-bls-key or
# --random-gateway-bls-key for any real deployment.
DEVNET_GATEWAY_BLS_KEY = "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b"

def generate_fresh_bls_secret(metanode_bin: str) -> str:
    """Generates an independent, freshly-random BLS secret scalar via the same `metanode
    keytool` call used for authority_key -- used for gateway_bls_key when the operator asks
    for a unique per-node key instead of DEVNET_GATEWAY_BLS_KEY. Real chains must not share
    this key across nodes/deployments (found 2026-08-27: all 3 genesis generators hardcoded
    the identical literal here, which is fine for a single-machine devnet smoke test but a
    real gap the moment enable_private_gateway is ever turned on for anything else)."""
    import tempfile
    with tempfile.TemporaryDirectory() as tmpdir:
        result = subprocess.run(
            [metanode_bin, "keytool", "generate", "validator", "--out-dir", tmpdir],
            capture_output=True, text=True
        )
        if result.returncode != 0:
            print(red(f"ERROR: metanode keytool failed (gateway BLS key generation):\n{result.stderr}"))
            sys.exit(1)
        with open(os.path.join(tmpdir, "authority_key.json")) as f:
            return json.load(f)["private_key_hex"]

def main():
    parser = argparse.ArgumentParser(description="Generate Root Anchor cluster configurations")
    parser.add_argument("--chain-id", type=int, default=9099, help="Root Anchor Chain ID (default: 9099)")
    parser.add_argument("--validators", type=int, default=4, help="Number of Root Anchor validator nodes (default: 4)")
    parser.add_argument("--founding-chains", type=str, default="101,102,103,104", help="Comma-separated founding chain IDs")
    parser.add_argument("--output-dir", type=str, default="./root_anchor_data", help="Output directory")
    parser.add_argument("--rpc-port-base", type=int, default=9099, help="Base JSON-RPC port (default: 9099)")
    parser.add_argument("--metanode-bin", type=str, default=None, help="Path to metanode binary")
    parser.add_argument("--port-offset", type=int, default=0, help="Port offset")
    parser.add_argument("--gateway-bls-key", type=str, default=None, help="Explicit BLS secret (hex) for gateway_bls_key (Private Gateway signing). Default: shared devnet-only key -- pass this or --random-gateway-bls-key for any real deployment.")
    parser.add_argument("--random-gateway-bls-key", action="store_true", help="Generate a fresh, independent gateway_bls_key per node instead of the shared devnet default. Recommended for any real deployment; does nothing to existing devnet/smoke-test flows unless passed explicitly.")
    args = parser.parse_args()

    ip_address = "127.0.0.1"

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
        # Real min-pk (48-byte G1) pubkey derived from the same secret authority["private_key_hex"]
        # uses -- see derive_min_pk_pubkey's doc comment. keys["authority"]["public_key_base64"]
        # stays min_sig/G2, used only for consensus identity.
        keys["min_pk_pubkey_hex"] = "0x" + base64.b64decode(
            derive_min_pk_pubkey(keys["authority"]["private_key_hex"])
        ).hex()

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
            # peer_rpc_port MUST be a distinct port from network_port/p2p_port (both 19200+i
            # below): network_port feeds the validator's real gRPC consensus P2P listener
            # (tonic_network.rs, permanent for the process's life), while peer_rpc_port is a
            # SEPARATE diagnostic HTTP server (PeerRpcServer) with its own early/full startup
            # handoff. Reusing the same port number for both (the bug this comment replaces)
            # meant the diagnostic server's "full" instance could never bind after handoff --
            # not a transient race, but a permanent collision with the real P2P listener, which
            # starts later and keeps the port for good. Root-caused live 2026-08-25 (see
            # note/cross_chain_production_readiness_plan.md Phase 0.7's "Layer C") after 2
            # earlier attempts wrongly assumed a Tokio task-cancellation race and treated it as
            # such (bind-retry-with-backoff, abort()+await in the Rust code) -- both real,
            # harmless hardening in their own right, but neither could have fixed this, since
            # the true occupant of the port never releases it at all. 29200 matches the
            # disjoint-range convention already used by _rehearsal_gen_node_configs.py.
            "peer_rpc_port": 29200 + i,
        }
        validators_info.append(val_info)
        print(green(f"  ✅ Node {i} generated: {val_info['keys']['eth']['address']} (RPC :{val_info['rpc_port']})"))

    # Fail loudly, at generation time, if any two of the CONFIRMED-real distinct OS listeners
    # ever collide across the whole cluster -- this is exactly the class of bug that caused
    # Layer C (see peer_rpc_port's own comment above): two DIFFERENT real listeners silently
    # assigned the same port number, which only surfaces at runtime as a mystifying "Address
    # already in use" days later. Deliberately scoped to only the ports directly confirmed live
    # (2026-08-25) to be real, independent binds -- rpc_port (Go client JSON-RPC), network_port
    # (Rust gRPC consensus P2P / p2p_address), peer_rpc_port (Rust diagnostic PeerRpcServer), and
    # metrics_port (Rust Prometheus exporter). consensus_port/mempool_port/primary_port/
    # worker_port are NOT included: their actual runtime binding behavior wasn't verified this
    # session (some may be informational-only metadata, e.g. consensus_port only ever appears as
    # the self-reported "network_address" string, never bound), and asserting uniqueness on
    # fields whose collision might be intentional risks a false-positive crash worse than the bug
    # this check exists to catch. Extend this set only after confirming a field really is bound.
    port_assignments = {}
    for i, v in enumerate(validators_info):
        candidates = {
            f"node{i}.rpc_port": v["rpc_port"],
            f"node{i}.network_port (p2p_address)": v["network_port"],
            f"node{i}.peer_rpc_port": v["peer_rpc_port"],
            f"node{i}.metrics_port": 12100 + args.port_offset + i,
        }
        for label, port in candidates.items():
            if port in port_assignments:
                print(red(
                    f"ERROR: port {port} assigned to both '{port_assignments[port]}' and '{label}' "
                    f"-- refusing to generate a cluster with a self-inflicted port collision."
                ))
                sys.exit(1)
            port_assignments[port] = label

    # Construct genesis.json in the new format for simple_chain
    validators_entries = []
    alloc_list = []
    stake_wei = "1000000000000000000000" # 1000 MTN
    alloc_wei = "1000000000000000000000000"

    for v in validators_info:
        node_id = v["index"]
        eth_addr = v["keys"]["eth"]["address"].lower()
        primary_port = 14200 + args.port_offset + node_id
        worker_port = 15012 + args.port_offset + node_id
        p2p_port = 19200 + args.port_offset + node_id

        val_entry = {
            "address": eth_addr,
            "eth_private_key": v["keys"]["eth"]["private_key"],
            "primary_address": f"{ip_address}:{primary_port}",
            "worker_address": f"{ip_address}:{worker_port}",
            "p2p_address": f"/ip4/{ip_address}/tcp/{p2p_port}",
            "description": f"Root Anchor Validator node-{node_id}",
            "website": "",
            "image": "",
            "commission_rate": 5,
            "min_self_delegation": "1000000000000000000",
            "accumulated_rewards_per_share": "0",
            "delegator_stakes": [
                {"address": eth_addr, "amount": stake_wei}
            ],
            "total_staked_amount": stake_wei,
            "network_key": v["keys"]["network"]["public_key_base64"],
            "hostname": f"node-{node_id}",
            "authority_key": v["keys"]["authority"]["public_key_base64"],
            "protocol_key": v["keys"]["protocol"]["public_key_base64"],
        }
        validators_entries.append(val_entry)

        alloc_list.append({
            "address": eth_addr,
            "balance": alloc_wei,
            "pending_balance": "0",
            "last_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
            "device_key": "0x0000000000000000000000000000000000000000000000000000000000000000",
            # Real min-pk pubkey (see derive_min_pk_pubkey's doc comment) -- NOT
            # public_key_base64 (min_sig/G2, consensus-only, and not even hex-encoded so the
            # genesis loader's common.FromHex() silently decoded it to 0 bytes).
            "publicKeyBls": v["keys"]["min_pk_pubkey_hex"]
        })

    # Inject a known dev account so we can send transactions via web3
    alloc_list.append({
        "address": "0x7d8bfbaba9268b59bab9ef8ff3f314d3f5747366",
        "balance": "1000000000000000000000000",
        "pending_balance": "0",
        "last_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
        "device_key": "0x0000000000000000000000000000000000000000000000000000000000000000",
        "publicKeyBls": "0x86d5de6f7c9c13cc0d959a553cc0e4853ba5faae45a28da9bddc8ef8e104eb5d3dece8dfaa24f11b4243ec27537e3184"
    })

    # Pre-register each founding chain's deterministic devnet submitter account
    # (see derive_devnet_submitter_account() docstring for why this is needed:
    # CommitAttestationWorker.submitMyShare() sends submitCommitAttestation()
    # txs to Root Anchor from this account, and Root Anchor rejects txs from
    # any account with no BLS pubkey registered on its own chain).
    for founding_chain_id in founding_chains:
        _submitter_priv, submitter_address = derive_devnet_submitter_account(founding_chain_id)
        alloc_list.append({
            "address": submitter_address,
            "balance": "1000000000000000000000000",
            "pending_balance": "0",
            "last_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
            "device_key": "0x0000000000000000000000000000000000000000000000000000000000000000",
            "publicKeyBls": "0x86d5de6f7c9c13cc0d959a553cc0e4853ba5faae45a28da9bddc8ef8e104eb5d3dece8dfaa24f11b4243ec27537e3184"
        })
        print(f"  🔑 Pre-registered devnet submitter account for chain {founding_chain_id}: {submitter_address}")

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
    example_path = Path(__file__).parent / "genesis.json.example"
    if example_path.exists():
        try:
            with open(example_path, "r") as ef:
                example_data = json.load(ef)
                if "alloc" in example_data:
                    genesis_data["alloc"].extend(example_data["alloc"])
                    print(f"  💉 Injected {len(example_data['alloc'])} accounts from genesis.json.example")
        except Exception as e:
            print(f"  ⚠️ Warning: could not merge genesis.json.example allocs: {e}")

    genesis_path = out_dir / "genesis.json"
    with open(genesis_path, "w") as f:
        json.dump(genesis_data, f, indent=2)
    print(green(f"\n✅ Genesis generated at: {genesis_path}"))

    # Generate config.json and node.toml for each validator
    for v in validators_info:
        node_dir = Path(v["dir"])
        node_id = v["index"]
        os.makedirs(node_dir / "logs", exist_ok=True)
        os.makedirs(node_dir / "data" / "execution" / "db", exist_ok=True)
        os.makedirs(node_dir / "data" / "consensus" / "db", exist_ok=True)

        go_peers = [f"{ip_address}:{17200 + args.port_offset + j}" for j in range(args.validators) if j != node_id]
        # Must point at each peer's peer_rpc_port (the diagnostic PeerRpcServer), NOT p2p_port
        # (19200+j, the real gRPC consensus network layer) -- see the peer_rpc_port field's own
        # comment above for why conflating the two was the actual root cause of Layer C.
        rust_peers = [f"{ip_address}:{29200 + j}" for j in range(args.validators) if j != node_id]

        if args.gateway_bls_key:
            gateway_bls_key = args.gateway_bls_key
        elif args.random_gateway_bls_key:
            gateway_bls_key = generate_fresh_bls_secret(metanode_bin)
            print(f"  🔑 node-{node_id}: generated a fresh, independent gateway_bls_key")
        else:
            gateway_bls_key = DEVNET_GATEWAY_BLS_KEY

        exec_config = {
            "debug": False,
            "tx_trace_enabled": False,
            "go_mem_limit_gb": 8,
            "mvm_cache_enabled": False,
            "enable_private_gateway": True,
            "gateway_bls_key": gateway_bls_key,
            "chainId": args.chain_id,
            "private_key": v["keys"]["authority"]["private_key_hex"],
            "address": v["keys"]["eth"]["address"].lstrip("0x").lower(),
            "log_path": str(node_dir / "logs" / "execution"),
            "epochs_to_keep": 5,
            "backup_path": str(node_dir / "data" / "execution" / "backup"),
            "last_block_save_path": "/last_block.dat",
            "transaction_block_number_last_hash_path": "/transaction_block_number_last_hash",
            "block_hash_to_number_db_root_path": "/block_hash_to_number_db_root_path",
            "free_fee_addresses": [
                "55798165960a62cED34a0d86e36B1758D1303907"
            ],
            "cross_chain": {
                "config_contract": "0x4c1c27b3147820915431554F2B2383175FAAd198",
                # DEVNET/TESTING ONLY -- see the matching field in gen_single_chain.py for the
                # full rationale. NEVER set this on a real deployment.
                "devnet_governance_timelock_seconds_override": 10
            },
            "meta_node_rpc_address": f"{ip_address}:{11100 + args.port_offset + node_id}",
            "connection_address": f"0.0.0.0:{14200 + args.port_offset + node_id}",
            "dns_server_address": f"{ip_address}:{13000 + args.port_offset + node_id}",
            "version": "0.0.1.0",
            "rpc_port": f":{v['rpc_port']}",
            "peer_rpc_port": v['peer_rpc_port'],
            "db_type": 2,
            "genesis_file_path": str(genesis_path),
            "rust_config_path": str(node_dir / "node.toml"),
            "snapshot_enabled": False,
            "is_rpc_node": False,
            "state_backend": "nomt",
            "Databases": {
                "RootPath": str(node_dir / "data" / "execution" / "db"),
                "DBEngine": "sharded",
                "Version": "0.0.1.0",
                "BLSPrivateKey": v["keys"]["authority"]["private_key_hex"],
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

        peers_toml = ", ".join([f'"{p}"' for p in rust_peers])
        toml_content = f"""# Rust Consensus Configuration for Root Anchor Node {node_id}
node_id = {node_id}
network_address = "{ip_address}:{v['consensus_port']}"
protocol_key_path = "{node_dir}/keys/protocol_key.json"
network_key_path = "{node_dir}/keys/network_key.json"
storage_path = "{node_dir}/data/consensus/db"

enable_metrics = true
metrics_port = {12100 + args.port_offset + node_id}
peer_rpc_port = {v['peer_rpc_port']}
peer_rpc_addresses = [{peers_toml}]
executor_read_enabled = true
executor_commit_enabled = true
time_based_epoch_change = true
"""
        with open(node_dir / "node.toml", "w") as f:
            f.write(toml_content)

    simple_chain_bin = REPO_ROOT / "execution" / "cmd" / "simple_chain" / "simple_chain"
    start_lines = ["#!/usr/bin/env bash", "set -e", 'SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"', f'SIMPLE_CHAIN_BIN="{simple_chain_bin}"', ""]
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
        start_lines.append(f'(cd "{v["dir"]}" && "$SIMPLE_CHAIN_BIN" --config config.json > logs/node.log 2>&1 & echo $! > node.pid)')
        stop_lines.append(f'if [ -f "{v["dir"]}/node.pid" ]; then kill -15 $(cat "{v["dir"]}/node.pid") 2>/dev/null || true; rm "{v["dir"]}/node.pid"; fi\n')

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
