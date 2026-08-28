#!/usr/bin/env python3
"""
generate_chain_config.py
Generates and normalizes single chain configuration for Ansible deployment.
"""
import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description="Generate and normalize private chain config for Ansible")
    parser.add_argument("--chain-id", type=int, required=True, help="Chain ID")
    parser.add_argument("--ip", type=str, required=True, help="Node IP")
    parser.add_argument("--rpc-port", type=int, required=True, help="RPC Port")
    parser.add_argument("--port-offset", type=int, required=True, help="Port offset")
    parser.add_argument("--validators", type=int, default=1, help="Number of validator nodes")
    parser.add_argument("--root-anchor-rpc", type=str, required=True, help="Root Anchor RPC URL")
    parser.add_argument("--root-anchor-submitter-key", type=str, default="", help="Root Anchor submitter key")
    parser.add_argument("--target-dir", type=str, required=True, help="Target install directory (/opt/metanode/chain-XXX)")
    parser.add_argument("--output-dir", type=str, required=True, help="Local output directory for generated config/keys")
    parser.add_argument("--action", type=str, default="setup", help="Ansible action (setup, reset, etc.)")
    args = parser.parse_args()

    if not args.root_anchor_submitter_key:
        print("❌ ERROR: root_anchor_submitter_key chưa được cấu hình trong inventory.yml", file=sys.stderr)
        sys.exit(1)

    # 1. Resolve ReserveChainID dynamically from Root Anchor
    try:
        req = json.dumps({"jsonrpc": "2.0", "method": "eth_chainId", "params": [], "id": 1}).encode("utf-8")
        out = subprocess.check_output(
            ["curl", "-s", "-X", "POST", args.root_anchor_rpc, "-H", "Content-Type: application/json", "-d", req.decode("utf-8")],
            text=True,
            timeout=10,
        )
        res = json.loads(out)
        res_hex = res.get("result", "")
        if not res_hex:
            raise ValueError(f"Empty result in RPC response: {out}")
        reserve_chain_id = int(res_hex, 16)
        print(f"ℹ️  Reserve chain ID resolved from Root Anchor ({args.root_anchor_rpc}) = {reserve_chain_id}")
    except Exception as e:
        print(f"❌ ERROR: could not fetch Root Anchor's chain ID via eth_chainId from {args.root_anchor_rpc}: {e}", file=sys.stderr)
        sys.exit(1)

    out_dir = Path(args.output_dir)
    if args.action == "reset" and out_dir.exists():
        import shutil
        shutil.rmtree(out_dir)

    script_dir = Path(__file__).resolve().parent
    gen_script = script_dir.parent / "systemd" / "gen_single_chain.py"

    if not (out_dir / "node-0" / "config.json").exists():
        cmd = [
            sys.executable,
            str(gen_script),
            "--chain-id", str(args.chain_id),
            "--ip", args.ip,
            "--rpc-port", str(args.rpc_port),
            "--port-offset", str(args.port_offset),
            "--validators", str(args.validators),
            "--root-anchor-rpc", args.root_anchor_rpc,
            "--root-anchor-submitter-key", args.root_anchor_submitter_key,
            "--reserve-chain-id", str(reserve_chain_id),
            "--output-dir", str(out_dir),
        ]
        print(f"🚀 Running: {' '.join(cmd)}")
        subprocess.check_call(cmd)

    # 2. Normalize paths in config.json for all generated nodes
    t_dir = args.target_dir
    for node_dir in out_dir.glob("node-*"):
        cfg_path = node_dir / "config.json"
        if cfg_path.exists():
            with open(cfg_path) as f:
                c = json.load(f)
            c["log_path"] = f"{t_dir}/logs/execution"
            c["backup_path"] = f"{t_dir}/data/execution/backup"
            c["genesis_file_path"] = f"{t_dir}/config/genesis.json"
            c["rust_config_path"] = f"{t_dir}/config/node.toml"
            if "Databases" in c:
                c["Databases"]["RootPath"] = f"{t_dir}/data/execution/db"
                c["Databases"]["SnapshotPath"] = f"{t_dir}/data/execution/snapshots"
            with open(cfg_path, "w") as f:
                json.dump(c, f, indent=2)

        toml_path = node_dir / "node.toml"
        if toml_path.exists():
            with open(toml_path) as f:
                content = f.read()
            content = re.sub(r'protocol_key_path\s*=\s*".*?"', f'protocol_key_path = "{t_dir}/keys/protocol_key.json"', content)
            content = re.sub(r'network_key_path\s*=\s*".*?"', f'network_key_path = "{t_dir}/keys/network_key.json"', content)
            content = re.sub(r'storage_path\s*=\s*".*?"', f'storage_path = "{t_dir}/data/consensus"', content)
            with open(toml_path, "w") as f:
                f.write(content)

    print(f"✅ Generated and normalized configs for Chain {args.chain_id} in {out_dir}")


if __name__ == "__main__":
    main()
