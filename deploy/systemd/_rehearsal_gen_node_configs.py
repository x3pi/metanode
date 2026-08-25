#!/usr/bin/env python3
"""
_rehearsal_gen_node_configs.py — internal helper for
rehearse_root_anchor_ceremony.sh. Writes a Go execution config.json + Rust
consensus node.toml for each of N already-keyed rehearsal operators, pointed
at an already-assembled genesis.json.

This is NOT the tool a real ceremony operator uses to provision their node —
that remains deploy/systemd/gen_validator_entry.py / deploy/ansible (see
note/private_chain_guide.md and note/runbook_root_anchor_genesis_ceremony.md
step 7). This script exists only so the local rehearsal can prove the
assembled genesis actually boots, using local filesystem paths instead of the
/opt/metanode/node-N layout gen_validator_entry.py's write_node_configs()
assumes for a real systemd install.

Port scheme (all loopback, chosen to avoid colliding with the checked-in
5-node dev cluster in consensus/metanode/config/ which uses 4200s/6200s/9000s/
19200s): Go P2P 6200+i, Rust consensus P2P 9100+i, Go RPC 18757+i, meta RPC
15100+i, peer RPC 29200+i, metrics 29100+i, snapshot server 28600+i — the
exact scheme rehearse_root_anchor_ceremony.sh's own founding_entry calls
already used for each validator's genesis p2p_address/primary_address, so
genesis and runtime config agree.
"""
import argparse
import json
import os


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--workdir", required=True)
    ap.add_argument("--genesis", required=True)
    ap.add_argument("--chain-id", type=int, required=True)
    ap.add_argument("--num-nodes", type=int, default=4)
    args = ap.parse_args()

    workdir = os.path.abspath(args.workdir)
    genesis_path = os.path.abspath(args.genesis)
    n = args.num_nodes

    for i in range(n):
        op_dir = os.path.join(workdir, "operators", f"op-{i}")
        keys_dir = os.path.join(op_dir, "keys")
        node_dir = os.path.join(op_dir, "node")
        os.makedirs(node_dir, exist_ok=True)
        os.makedirs(os.path.join(node_dir, "data"), exist_ok=True)

        with open(os.path.join(keys_dir, "authority_key.json")) as f:
            auth = json.load(f)
        with open(os.path.join(keys_dir, "eth_key.json")) as f:
            eth = json.load(f)

        bls_priv = auth["private_key_hex"]
        eth_addr = eth["ETH_ADDRESS"].lstrip("0x").lower()

        go_p2p_port = 6200 + i
        rust_p2p_port = 9100 + i
        peer_rpc_port = 29200 + i
        meta_rpc_port = 15100 + i
        rpc_port = 18757 + i
        dns_port = 17081 + i
        metrics_port = 29100 + i
        snapshot_port = 28600 + i

        go_peers = [f"127.0.0.1:{6200 + j}" for j in range(n) if j != i]

        exec_json = {
            "debug": True,
            "tx_trace_enabled": False,
            "mvm_cache_enabled": False,
            "chainId": args.chain_id,
            "private_key": bls_priv,
            "address": eth_addr,
            "log_path": os.path.join(node_dir, "logs", "go-master"),
            "epochs_to_keep": 5,
            "backup_path": os.path.join(node_dir, "data", "backup"),
            "last_block_save_path": "/last_block.dat",
            "meta_node_rpc_address": f"127.0.0.1:{meta_rpc_port}",
            "transaction_block_number_last_hash_path": "/transaction_block_number_last_hash",
            "block_hash_to_number_db_root_path": "/block_hash_to_number_db_root_path",
            "free_fee_addresses": [],
            "connection_address": f"0.0.0.0:{go_p2p_port}",
            "dns_server_address": f"0.0.0.0:{dns_port}",
            "version": "0.0.1.0",
            "rpc_port": f":{rpc_port}",
            "securepassword": "mysecretpassword",
            "peer_rpc_port": peer_rpc_port,
            "db_type": 2,
            "genesis_file_path": genesis_path,
            "snapshot_enabled": False,
            "snapshot_frequency_blocks": 500,
            "snapshot_block_offset": 0,
            "Databases": {
                "RootPath": os.path.join(node_dir, "data", "db"),
                "DBEngine": "sharded",
                "Version": "0.0.1.0",
                "BLSPrivateKey": bls_priv,
                "SnapshotPath": os.path.join(node_dir, "data", "snapshots"),
                "MaxPartSizeMB": 100,
                "ArchiveBaseName": "snapshot_archive",
            },
            "nodes": {"list_sub_address": go_peers},
            "snapshot_method": "hybrid",
            "snapshot_source_dir": os.path.join(node_dir, "data"),
            "snapshot_server_port": snapshot_port,
            # MPT, not NOMT: this rehearsal only needs to prove the ceremony's
            # genesis is bootable, not exercise the NOMT storage backend —
            # MPT has fewer moving parts (no page/leaf cache tuning) and is
            # the lower-risk choice for a CI-friendly rehearsal script.
            "state_backend": "mpt",
            "rust_config_path": os.path.join(node_dir, "..", "node.toml"),
            "is_explorer": False,
            "explorer_db_path": os.path.join(node_dir, "data", "explorer"),
            "explorer_read_only_db_path": os.path.join(node_dir, "data", "explorer-ro"),
            "is_rpc_node": True,
            "log": {"level": "info", "format": "text", "console_output": True, "file_output": True},
        }
        with open(os.path.join(node_dir, "config.json"), "w") as f:
            json.dump(exec_json, f, indent=2)

        peer_rpc_addrs = ", ".join(f'"127.0.0.1:{29200 + j}"' for j in range(n) if j != i)
        proto_path = os.path.join(keys_dir, "protocol_key.json")
        net_path = os.path.join(keys_dir, "network_key.json")
        storage_path = os.path.join(node_dir, "consensus_storage")

        toml = f"""node_id = {i}
network_address = "127.0.0.1:{rust_p2p_port}"
protocol_key_path = "{proto_path}"
network_key_path = "{net_path}"
storage_path = "{storage_path}"
enable_metrics = true
metrics_port = {metrics_port}
speed_multiplier = 1.0
time_based_epoch_change = true
max_clock_drift_seconds = 5
enable_ntp_sync = false
ntp_servers = []
ntp_sync_interval_seconds = 300
executor_read_enabled = true
executor_commit_enabled = true
commit_sync_batch_size = 500
commit_sync_parallel_fetches = 32
commit_sync_batches_ahead = 128
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
peer_rpc_addresses = [{peer_rpc_addrs}]
epochs_to_keep = 5

[log]
level = "info"
format = "text"
console_output = true
file_output = false
"""
        with open(os.path.join(node_dir, "..", "node.toml"), "w") as f:
            f.write(toml)

        # Rust needs protocol_key.json/network_key.json as a raw base64
        # string of priv(32B)||pub(32B), not the JSON `metanode keytool`
        # writes by default (same rewrite deploy/systemd/gen_validator_entry.py
        # does — see its rewrite_key_as_base64).
        for name in ("protocol_key.json", "network_key.json"):
            path = os.path.join(keys_dir, name)
            with open(path) as f:
                raw = f.read()
            try:
                d = json.loads(raw)
            except json.JSONDecodeError:
                continue  # already rewritten
            import base64
            priv = bytes.fromhex(d["private_key_hex"])
            pub = base64.b64decode(d["public_key_base64"])
            with open(path, "w") as f:
                f.write(base64.b64encode(priv + pub).decode())

    print(f"wrote node configs for {n} nodes under {workdir}/operators/op-*/node/")


if __name__ == "__main__":
    main()
