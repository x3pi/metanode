#!/usr/bin/env python3
"""Generate a fully ISOLATED test environment for the account-gate end-to-end test.

One single-validator Parent Chain and TWO execution clusters (Raft, one replica each) that deliberately share chain ID 991
(the cross-cluster replay precondition). Both clusters run tx_signature_mode="secp" and account_gate="parent_registered",
and register themselves with the parent (open cluster registration, float funded for their BLS identities).

Everything lives under --base and uses ports from --port-base upwards, so it can run next to any other cluster on the
machine. It never touches /opt/metanode, consensus/metanode/config/storage or any shared directory.

Usage: gen_env.py --base DIR --bin DIR [--port-base 31000] [--repo REPO_ROOT]
"""
import argparse, json, os, shutil, time

# Devnet identities (the same ones scripts/test/run_devnet.sh and the ansible deployment use).
CLUSTERS = [
    dict(name="exec1", cluster_id=1, address="0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5",
         private_key="0f326c0b9bb86353ac317dd8f9b045fd1877473674ba24500139fed777b26a0c",
         bls_priv="5fb8d1ceadf4059adca5c106dbd91452be8c433b2c38c5dd85c50f1c7da4c85c",
         bls_pub="0x944488b425d29336c7913a3b45946adee6b9bfbd0838c6c8f422f4b4277066f26b3da0530c9f9865e6e534a05ae6c128"),
    dict(name="exec2", cluster_id=2, address="0x0d4CC97b62a149a8fe8DE81262270426A80B0935",
         private_key="2a61eac9235fab64ae377b2b7e39f8fa9648c5737094d28c7ee68a14b5086d39",
         bls_priv="2d21f977fd594b7587439a91ab1bb5523573f02e5ba7e3e61cd8e895470d400a",
         bls_pub="0x83221629eeff1a69aa96ac6aadea402a7b62a74647633c0743cd517b71dcd5cd39fec42841b953fc481dac039bceb465"),
]
# Funded secp account used by the test driver (mtn_api.go devnetSenderPrivateKeyHex); present in every genesis alloc.
FUNDER = dict(address="0xb4eb43848E94de7BE8e2b551063dcE2aBeB8ba24",
              bls_pub="0xb518c65d0f5f23858fd28f0473cb1fbaccc8aaa960880aee841585861f245abc0c4e4dce5b3693cfe60da4902d9484bc")
ZERO32 = "0x" + "00" * 32
BIG = "2000000000000000000000000000000"
FLOAT = "20238000000001900000000000000000000"

PARENT_TOML = """node_id = 0
network_address = "127.0.0.1:{p2p}"
protocol_key_path = "{base}/parent/node_0_protocol_key.json"
network_key_path = "{base}/parent/node_0_network_key.json"
storage_path = "{base}/parent/consensus_storage"
enable_metrics = true
metrics_port = {metrics}
speed_multiplier = 1.0
time_based_epoch_change = false
max_clock_drift_seconds = 5
enable_ntp_sync = false
ntp_servers = ["pool.ntp.org", "time.google.com"]
ntp_sync_interval_seconds = 300
executor_read_enabled = true
executor_commit_enabled = true
commit_sync_batch_size = 500
commit_sync_parallel_fetches = 32
commit_sync_batches_ahead = 128
adaptive_catchup_enabled = true
adaptive_delay_enabled = false
adaptive_delay_ms = 50
min_round_delay_ms = 25
compact_blocks_enabled = true
leader_timeout_ms = 200
epoch_transition_optimization = "fast"
enable_gradual_shutdown = true
gradual_shutdown_user_cert_drain_secs = 2
gradual_shutdown_consensus_cert_drain_secs = 1
gradual_shutdown_final_drain_secs = 1
epoch_monitor_poll_interval_secs = 5
peer_rpc_port = {peer_rpc}
peer_rpc_addresses = []
epochs_to_keep = 5

[log]
level = "info"
format = "text"
console_output = true
file_output = false
"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True)
    ap.add_argument("--bin", required=True)
    ap.add_argument("--port-base", type=int, default=31000)
    ap.add_argument("--repo", default=os.path.abspath(os.path.join(os.path.dirname(__file__), "../../../..")))
    ap.add_argument("--gate", default="parent_registered", help="account_gate value for exec2 (exec1 always uses parent_registered); '' = off")
    a = ap.parse_args()
    base, pb = os.path.abspath(a.base), a.port_base
    ports = dict(
        parent_http=pb + 601, parent_p2p=pb + 1, parent_metrics=pb + 901, parent_peer_rpc=pb + 501,
        exec1=dict(rpc=pb + 646, conn=pb + 200, raft=pb + 110, forward=pb + 210),
        exec2=dict(rpc=pb + 647, conn=pb + 202, raft=pb + 111, forward=pb + 211),
    )
    os.makedirs(base, exist_ok=True)

    # ---------------- parent ----------------
    pdir = os.path.join(base, "parent")
    os.makedirs(pdir, exist_ok=True)
    cfg = os.path.join(a.repo, "consensus/metanode/config")
    for f in ("node_0_protocol_key.json", "node_0_network_key.json"):
        shutil.copy(os.path.join(cfg, f), os.path.join(pdir, f))
    with open(os.path.join(pdir, "node_parent.toml"), "w") as f:
        f.write(PARENT_TOML.format(base=base, p2p=ports["parent_p2p"], metrics=ports["parent_metrics"], peer_rpc=ports["parent_peer_rpc"]))
    with open(os.path.join(pdir, "config.json"), "w") as f:
        json.dump({"http_port": ":%d" % ports["parent_http"], "log_level": 3}, f, indent=2)
    parent_genesis = {
        "chain_id": 990, "epoch_duration_seconds": 86400, "epoch_timestamp_ms": 1700000000000,
        "validators": [{
            "name": "node-0", "address": "0x7e615e4a500ab42b7bb3fdbb62fbb8bd10385fc5", "stake": "1000000000000000000",
            "authority_key": "kUYrYvf/fDUygF8+nIdNATAAlnQU3BZSD3aGuHNoAZQv3OJOIZKW+Uw+UbH/1LWCAlbyWnQra9vUSDJfFVIxlV4XlraaNkLsZSb3HMJJQK3qEc1L20Yqb5YM8uGRXvnB",
            "protocol_key": "fN/BNA8PFyjE3hclyjxnkYgjFlR6M27jpbocq7X847Y=",
            "network_key": "jZ/kDNNPBsZXUD28FcxMLLZ+vCZCbEJoUvdB8zgRvug=",
            "p2p_address": "/ip4/127.0.0.1/tcp/%d" % ports["parent_p2p"],
        }],
        "accounts": [{"address": "0x7e615e4a500ab42b7bb3fdbb62fbb8bd10385fc5", "balance": "1000000000000000000000"}],
        "open_cluster_registration": True, "clusters": [],
        "float_accounts": [{"bls_public_key": c["bls_pub"], "balance": FLOAT} for c in CLUSTERS],
        "min_float_to_register": "1000000000000000000000", "allow_deposit_to_float": True,
    }
    with open(os.path.join(pdir, "parent_genesis.json"), "w") as f:
        json.dump(parent_genesis, f, indent=2)

    # ---------------- exec clusters ----------------
    with open(os.path.join(a.repo, "execution/cmd/simple_chain/genesis.json")) as f:
        gbase = json.load(f)
    secret = os.path.join(base, "raft_secret.key")
    with open(secret, "w") as f:
        f.write(os.urandom(32).hex())
    os.chmod(secret, 0o600)

    for c in CLUSTERS:
        d = os.path.join(base, c["name"])
        os.makedirs(d, exist_ok=True)
        p = ports[c["name"]]
        gate = "parent_registered" if c["name"] == "exec1" else a.gate
        config = {
            "debug": True, "cluster_id": c["cluster_id"], "enable_private_gateway": False,
            "master_password": "devnet-test-password", "app_pepper": "devnet-test-pepper",
            "private_key": c["private_key"], "address": c["address"],
            "log_path": d + "/logs", "backup_path": d + "/backup",
            "explorer_db_path": d + "/explorer", "explorer_read_only_db_path": d + "/explorer-ro",
            "is_explorer": True, "connection_address": "0.0.0.0:%d" % p["conn"], "version": "0.0.1.0",
            "rpc_port": ":%d" % p["rpc"], "db_type": 2, "genesis_file_path": d + "/genesis.json",
            "pk_admin_file_storage": "87d931eaa2f76709f2615586e0d560ca9b80f247c9cc431e197ba3e7167db623",
            "bls_admin_storage": "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
            "owner_file_storage_address": "0xC6E6474A8DEAD25B0e75b1aeA5d35FA19f69588a",
            "Databases": {"RootPath": d + "/data", "DBEngine": "sharded", "Version": "0.0.1.0",
                          "BLSPrivateKey": c["bls_priv"], "SnapshotPath": d + "/snapshot"},
            "is_rpc_node": True, "consensus_mode": "raft", "snapshot_enabled": False,
            "tx_signature_mode": "secp",
            "raft": {
                "node_id": c["name"] + "_r1", "bind_address": "127.0.0.1:%d" % p["raft"],
                "advertise_address": "127.0.0.1:%d" % p["raft"], "data_dir": d + "/raft", "bootstrap": True,
                "forward_bind_address": "127.0.0.1:%d" % p["forward"], "forward_secret_file": secret,
                "sequencer_address": c["address"], "heartbeat_timeout_ms": 100, "election_timeout_ms": 200,
                "leader_lease_timeout_ms": 80, "commit_timeout_ms": 30, "propose_queue_size": 1024,
                "peers": [{"id": c["name"] + "_r1", "address": "127.0.0.1:%d" % p["raft"],
                           "forward_address": "127.0.0.1:%d" % p["forward"]}],
            },
        }
        if gate:
            config["account_gate"] = gate
        with open(os.path.join(d, "config.json"), "w") as f:
            json.dump(config, f, indent=2)

        # genesis: small alloc (fast start). Every alloc account carries the cluster BLS key like the ansible genesis.
        def acc(addr, bls):
            return {"address": addr, "balance": BIG, "pending_balance": "0", "last_hash": ZERO32,
                    "device_key": ZERO32, "publicKeyBls": bls}
        alloc = {}
        for x in gbase["alloc"][:20]:
            alloc[x["address"].lower()] = dict(x, publicKeyBls=c["bls_pub"])
        alloc[FUNDER["address"].lower()] = acc(FUNDER["address"], FUNDER["bls_pub"])
        for o in CLUSTERS:  # both cluster identities exist on both chains (rollup signature verification)
            alloc[o["address"].lower()] = acc(o["address"], o["bls_pub"])
        auth = gbase["validators"][0]
        stake = "1000000000000000000000"
        g = dict(gbase)
        g["config"] = dict(gbase["config"], chainId=991, epoch_timestamp_ms=int(time.time() * 1000))
        g["validators"] = [{
            "address": c["address"], "primary_address": "127.0.0.1:4000", "worker_address": "127.0.0.1:4012",
            "p2p_address": auth.get("p2p_address", auth.get("address", "127.0.0.1:9000")),
            "description": "E2E %s validator" % c["name"], "website": "", "image": "", "commission_rate": 5,
            "min_self_delegation": "1000000000000000000", "accumulated_rewards_per_share": "0",
            "delegator_stakes": [{"address": c["address"], "amount": stake}], "total_staked_amount": stake,
            "network_key": auth.get("network_key", ""), "hostname": auth.get("hostname", ""),
            "authority_key": auth.get("authority_key", ""), "protocol_key": auth.get("protocol_key", ""),
        }]
        g["total_stake"], g["quorum_threshold"], g["validity_threshold"] = 1000, 1000, 1000
        g["alloc"] = list(alloc.values())
        if gate:
            g["registered_accounts"] = []
        with open(os.path.join(d, "genesis.json"), "w") as f:
            json.dump(g, f)

    env = dict(base=base, bin=os.path.abspath(a.bin), ports=ports, funder=FUNDER["address"],
               clusters={c["name"]: dict(address=c["address"], bls_pub=c["bls_pub"]) for c in CLUSTERS})
    with open(os.path.join(base, "env.json"), "w") as f:
        json.dump(env, f, indent=2)
    print(json.dumps(env, indent=2))


if __name__ == "__main__":
    main()
