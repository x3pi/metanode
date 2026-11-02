#!/usr/bin/env python3
"""
gen_parent_keys.py — Key Generator & Manager for Parent Chain (Root Anchor) Nodes.

Inspects the configured parent_chain_nodes in inventory.yml and ensures each node has
its required validator keypair (protocol_key, network_key, authority_key, eth_key).

Behavior:
  - If keys already exist in the target keys directory: reuses existing keys without regenerating.
  - If keys do NOT exist (e.g. fresh setup or reset): generates new keys using `metanode keytool`.
  - With --overwrite: forces fresh key regeneration.
"""

import os
import sys
import json
import base64
import shutil
import argparse
import subprocess
import tempfile
from pathlib import Path


def green(s): return f"\033[32m{s}\033[0m"
def yellow(s): return f"\033[33m{s}\033[0m"
def red(s): return f"\033[31m{s}\033[0m"
def cyan(s): return f"\033[36m{s}\033[0m"


def parse_inventory_parent_nodes(inventory_path: str):
    """Parses parent_chain_nodes from inventory.yml safely handling !vault tags."""
    try:
        import yaml
    except ImportError:
        print(red("ERROR: PyYAML is required. Please install pyyaml or use python from system packages."))
        sys.exit(1)

    def vault_constructor(loader, node):
        return loader.construct_scalar(node)

    yaml.SafeLoader.add_constructor('!vault', vault_constructor)

    try:
        with open(inventory_path, 'r', encoding='utf-8') as f:
            data = yaml.safe_load(f)
    except Exception as e:
        print(red(f"ERROR: Failed to read inventory {inventory_path}: {e}"))
        sys.exit(1)

    if not isinstance(data, dict):
        print(red(f"ERROR: Invalid YAML in {inventory_path}"))
        sys.exit(1)

    all_section = data.get('all', {}) or {}
    global_vars = all_section.get('vars', {}) or {}
    children = all_section.get('children', {}) or {}
    parent_section = children.get('parent_chain_nodes', {}) or {}
    hosts = parent_section.get('hosts', {}) or {}

    nodes = []
    default_keys_dir = global_vars.get('parent_keys_dir', '')

    for idx, (h_name, h_vars) in enumerate(sorted(hosts.items())):
        if not isinstance(h_vars, dict):
            h_vars = {}
        node_id = h_vars.get('parent_node_id', idx)
        nodes.append({
            'name': h_name,
            'node_id': int(node_id),
            'host': h_vars.get('ansible_host', '127.0.0.1'),
            'p2p_port': h_vars.get('parent_p2p_port', 19001 + int(node_id)),
            'http_port': h_vars.get('parent_http_port', 18601 + int(node_id)),
        })

    # Default to 4 nodes if none found in inventory
    if not nodes:
        print(yellow(f"⚠️  No parent_chain_nodes found in {inventory_path}, defaulting to 4 validator nodes."))
        for i in range(4):
            nodes.append({
                'name': f'parent_node_{i}',
                'node_id': i,
                'host': '127.0.0.1',
                'p2p_port': 19001 + i,
                'http_port': 18601 + i,
            })

    return nodes, default_keys_dir


def find_metanode_binary(repo_root: Path, override_bin: str = None) -> str:
    """Finds the compiled metanode binary with keytool subcommand support."""
    if override_bin and os.path.isfile(override_bin) and os.access(override_bin, os.X_OK):
        return override_bin

    candidates = [
        repo_root / "target" / "release" / "metanode",
        repo_root / "consensus" / "metanode" / "target" / "release" / "metanode",
        Path("/opt/metanode/bin/metanode"),
        Path("/usr/local/bin/metanode"),
    ]

    for candidate in candidates:
        if candidate.is_file() and os.access(candidate, os.X_OK):
            return str(candidate)

    # Check PATH
    which_bin = shutil.which("metanode")
    if which_bin:
        return which_bin

    return ""


def build_metanode_binary_if_needed(repo_root: Path) -> str:
    """Builds metanode binary via cargo if not found."""
    found = find_metanode_binary(repo_root)
    if found:
        return found

    print(cyan("🔨 Binary metanode not found. Compiling via cargo build --release -p metanode..."))
    try:
        env = os.environ.copy()
        cargo_bin = os.path.expanduser("~/.cargo/bin")
        if cargo_bin not in env.get("PATH", ""):
            env["PATH"] = f"{cargo_bin}:{env.get('PATH', '')}"

        subprocess.run(
            ["cargo", "build", "--release", "-p", "metanode"],
            cwd=str(repo_root),
            env=env,
            check=True
        )
    except Exception as e:
        print(red(f"ERROR: Failed to compile metanode keytool: {e}"))
        sys.exit(1)

    found = find_metanode_binary(repo_root)
    if not found:
        print(red("ERROR: Cargo build completed but metanode binary was not found."))
        sys.exit(1)
    return found


def are_keys_present(keys_dir: Path, nodes: list) -> bool:
    """Checks whether protocol and network key files exist for all nodes."""
    for node in nodes:
        nid = node['node_id']
        proto_file = keys_dir / f"node_{nid}_protocol_key.json"
        net_file = keys_dir / f"node_{nid}_network_key.json"
        if not proto_file.is_file() or not net_file.is_file():
            return False
    return True


def to_base64_64byte(key_json_path: Path) -> str:
    """Extracts priv + pub from keytool JSON and encodes as 64-byte raw base64 string."""
    with open(key_json_path, 'r', encoding='utf-8') as f:
        data = json.load(f)
    priv_bytes = bytes.fromhex(data["private_key_hex"].strip())
    pub_bytes = base64.b64decode(data["public_key_base64"].strip())
    combined = priv_bytes + pub_bytes
    return base64.b64encode(combined).decode('utf-8')


def generate_node_keys(metanode_bin: str, keys_dir: Path, node: dict):
    """Runs `metanode keytool generate validator` for a single node and formats files."""
    nid = node['node_id']
    with tempfile.TemporaryDirectory() as tmp_dir:
        tmp_path = Path(tmp_dir)
        cmd = [metanode_bin, "keytool", "generate", "validator", "--out-dir", str(tmp_path)]
        res = subprocess.run(cmd, capture_output=True, text=True)
        if res.returncode != 0:
            print(red(f"ERROR: Key generation failed for node {nid}:\n{res.stderr}"))
            sys.exit(1)

        # 1. Protocol key: write 64-byte base64 string (compatible with Rust Consensus loader)
        proto_b64 = to_base64_64byte(tmp_path / "protocol_key.json")
        dest_proto = keys_dir / f"node_{nid}_protocol_key.json"
        dest_proto.write_text(proto_b64 + "\n", encoding='utf-8')
        os.chmod(dest_proto, 0o600)

        # 2. Network key: write 64-byte base64 string
        net_b64 = to_base64_64byte(tmp_path / "network_key.json")
        dest_net = keys_dir / f"node_{nid}_network_key.json"
        dest_net.write_text(net_b64 + "\n", encoding='utf-8')
        os.chmod(dest_net, 0o600)

        # 3. Authority key (BLS) & Eth key (secp256k1)
        dest_auth = keys_dir / f"node_{nid}_authority_key.json"
        shutil.copy2(tmp_path / "authority_key.json", dest_auth)
        os.chmod(dest_auth, 0o600)

        dest_eth = keys_dir / f"node_{nid}_eth_key.json"
        shutil.copy2(tmp_path / "eth_key.json", dest_eth)
        os.chmod(dest_eth, 0o600)

        # 4. Summary JSON with public keys for genesis generation
        with open(tmp_path / "keys_summary.json", 'r', encoding='utf-8') as f:
            summary_data = json.load(f)

        summary_data["node_id"] = nid
        summary_data["node_name"] = node['name']
        summary_data["host"] = node['host']
        summary_data["p2p_port"] = node['p2p_port']
        summary_data["http_port"] = node['http_port']

        dest_sum = keys_dir / f"node_{nid}_summary.json"
        dest_sum.write_text(json.dumps(summary_data, indent=2) + "\n", encoding='utf-8')
        os.chmod(dest_sum, 0o644)

        print(green(f"  ✅ node_{nid} keys generated: eth_address={summary_data.get('eth_address')}"))


def main():
    parser = argparse.ArgumentParser(description="Generate and manage Parent Chain validator keys.")
    parser.add_argument("--inventory", default="", help="Path to inventory.yml")
    parser.add_argument("--keys-dir", default="", help="Path to output parent_keys directory")
    parser.add_argument("--metanode-bin", default="", help="Explicit path to metanode binary")
    parser.add_argument("--overwrite", action="store_true", help="Force regenerate keys even if they exist")
    parser.add_argument("--check-only", action="store_true", help="Exit 0 if keys exist, 1 if missing")
    args = parser.parse_args()

    script_dir = Path(__file__).parent.resolve()
    ansible_clusters_dir = script_dir.parent
    repo_root = ansible_clusters_dir.parent.parent

    inventory_path = args.inventory or str(ansible_clusters_dir / "inventory.yml")
    if not os.path.isfile(inventory_path):
        inventory_path = str(ansible_clusters_dir / "inventory.example.yml")

    nodes, inv_keys_dir = parse_inventory_parent_nodes(inventory_path)

    # Determine keys_dir
    target_keys_dir_str = args.keys_dir or inv_keys_dir or str(ansible_clusters_dir / "parent_keys")
    if "{{ playbook_dir }}" in target_keys_dir_str:
        target_keys_dir_str = target_keys_dir_str.replace("{{ playbook_dir }}", str(ansible_clusters_dir))
    keys_dir = Path(target_keys_dir_str).resolve()
    keys_dir.mkdir(parents=True, exist_ok=True, mode=0o700)

    keys_exist = are_keys_present(keys_dir, nodes)

    if args.check_only:
        sys.exit(0 if keys_exist else 1)

    if keys_exist and not args.overwrite:
        print(cyan(f"🔑 [PARENT-KEYS] All {len(nodes)} Parent Chain validator keys exist in {keys_dir}."))
        print(cyan("   Reusing existing keys (no regeneration)."))
        sys.exit(0)

    # Need generation
    action_desc = "Forced regenerating" if args.overwrite else "Auto-generating missing"
    print(yellow(f"\n🔑 [PARENT-KEYS] {action_desc} Parent Chain validator keys in: {keys_dir}"))

    metanode_bin = build_metanode_binary_if_needed(repo_root)
    print(cyan(f"   Using keytool binary: {metanode_bin}"))

    all_summaries = []
    for node in nodes:
        nid = node['node_id']
        proto_file = keys_dir / f"node_{nid}_protocol_key.json"
        net_file = keys_dir / f"node_{nid}_network_key.json"
        summary_file = keys_dir / f"node_{nid}_summary.json"

        # If not overwrite, and this specific node has keys, keep it
        if not args.overwrite and proto_file.is_file() and net_file.is_file() and summary_file.is_file():
            print(cyan(f"  ⏭️  node_{nid} keys already exist. Keeping."))
            try:
                with open(summary_file, 'r', encoding='utf-8') as sf:
                    all_summaries.append(json.load(sf))
            except Exception:
                pass
            continue

        generate_node_keys(metanode_bin, keys_dir, node)
        try:
            with open(summary_file, 'r', encoding='utf-8') as sf:
                all_summaries.append(json.load(sf))
        except Exception:
            pass

    # Save all summaries together
    dest_all = keys_dir / "parent_validators_summary.json"
    dest_all.write_text(json.dumps(all_summaries, indent=2) + "\n", encoding='utf-8')
    os.chmod(dest_all, 0o644)

    print(green(f"\n✅ [PARENT-KEYS] All Parent Chain validator keys are ready in {keys_dir}!\n"))


if __name__ == "__main__":
    main()
