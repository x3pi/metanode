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


VALS_4 = [
    dict(name="val0", id=0, address="0x4018b2e0572e446ca130999Ce9F218dF2b49c812",
         bls_priv="0109751113666a88b1d335c312a6fb49a0981c2147489f5ec264e91177b42777",
         bls_pub="0x985265185a2a1a627af51c143de2f21d939970ed7099544775324b9eebf0bbe0d324ce053cd7d4192238724829f8ba66",
         protocol_key="fN/BNA8PFyjE3hclyjxnkYgjFlR6M27jpbocq7X847Y=",
         network_key="jZ/kDNNPBsZXUD28FcxMLLZ+vCZCbEJoUvdB8zgRvug=",
         authority_key="kUYrYvf/fDUygF8+nIdNATAAlnQU3BZSD3aGuHNoAZQv3OJOIZKW+Uw+UbH/1LWCAlbyWnQra9vUSDJfFVIxlV4XlraaNkLsZSb3HMJJQK3qEc1L20Yqb5YM8uGRXvnB"),
    dict(name="val1", id=1, address="0x18c2540C5Afd4a578CbF447583D0dcC797973b1b",
         bls_priv="36c2664ab4b4a972643e6808a5eb1fadc9ad9a5b4cb7983a292b21cbf53f5d0d",
         bls_pub="0xb50487eaa620729d2dd2cc1fb15540184bad7bdbd48e91eae157c08cee617f80bda480dbe8cbae45b0ae5063cfc1dba3",
         protocol_key="ngmCtN3emEfN6PD/oOtB4YoBaHRFn1M3GzhdtdnZzLw=",
         network_key="LFHUv5KJyfpRHkJrLGTItE8+LQI1y8su7PVrId8Dqzs=",
         authority_key="iRQSDQ6sV7V5jQVsevn8QpS11kG/0tCrN8lSuPqEcb1fDjbtRyzPFgRzY3SM8KAYEycufZ61UBQXMFKEPqAQ41hl4IOed47OQK/NZsQyuccw53CV0F89gaTELIFO/jx9"),
    dict(name="val2", id=2, address="0x2771697D85c09F579B8147c4Ad5797DADbE08114",
         bls_priv="39cebcf1b9a3e78ffc959db97c106b6da609f19b98cfb36d2c40c3d0c793ce33",
         bls_pub="0xa9c65eacecdf90e8f920a4925e0a98ebc427db7d2726a77f1c8648a1eb15110685312d6c81d84fe907b05f83a863a03b",
         protocol_key="SJMLmNrKGJHkextl8SN2px1Nq1LBsj6BX6nsnb8gU8Y=",
         network_key="ursUG3M+DRgSL1q6oKQY0FM1EEOla67/EzAw+u75iHM=",
         authority_key="hK+5uAd/qeLvGo98NE7OCFwYioigpG0uYXn5ocETYSrUMLbCkC+PPH6E4AtRgo5jF7aDhhUmZPX1jAppMs2g9aJzAXQVMNo7Hj5sJq5/r5ogz9HyHsMPvDKvi3HBIuq5"),
    dict(name="val3", id=3, address="0x52f6A7a34d1FD2B91E86BE5ef150341304F74319",
         bls_priv="54299679cc61c8930e92b8c9b3c9ff0bd84425a3270419f4407a27f608f3ee97",
         bls_pub="0x87ac85af3b1a502e835152c0965f48f3c8bf77e880cb0587f87cc84227ce97ffcb7a2610f73e960575798fc1e8165c4c",
         protocol_key="2EhvdGj7yPTg2fPLQOy6ytlApvgpUhgc1nxzsPEkGCA=",
         network_key="tmkvDNdrvCX0F6Da6n6zr36cQDej3PUirrbUYOceq4w=",
         authority_key="lexRcMNpxBeR/rHNqC3d2+8FOPbDzhXj1mYAdkimbQuCr1WZtSD4Z1sOS0jumEFiFovhoOugnMDIxmOPEZLg7QbzDYGjmpd2+j4ARJTsm70Ophw/q5XmPlzaJL8R0lJO"),
]

VAL_TOML = """node_id = {node_id}
network_address = "127.0.0.1:{p2p}"
protocol_key_path = "{base}/{name}/node_protocol_key.json"
network_key_path = "{base}/{name}/node_network_key.json"
storage_path = "{base}/{name}/consensus_storage"
enable_metrics = false
metrics_port = {metrics}
speed_multiplier = 1.0
time_based_epoch_change = false
max_clock_drift_seconds = 5
enable_ntp_sync = false
executor_read_enabled = true
executor_commit_enabled = true
commit_sync_batch_size = 500
commit_sync_parallel_fetches = 32
commit_sync_batches_ahead = 128
adaptive_catchup_enabled = true
adaptive_delay_enabled = false
compact_blocks_enabled = true
leader_timeout_ms = 200
epoch_transition_optimization = "fast"
peer_rpc_port = {peer_rpc}
peer_rpc_addresses = {peer_rpc_addresses}
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
    ap.add_argument("--validators", type=int, default=1, choices=[1, 4], help="1 = Raft 2-cluster replay test, 4 = Mysticeti 4-validator co-attestation")
    a = ap.parse_args()
    base, pb = os.path.abspath(a.base), a.port_base
    if a.validators == 4:
        ports = dict(
            parent_http=pb + 601, parent_p2p=pb + 1, parent_metrics=pb + 901, parent_peer_rpc=pb + 501,
        )
        for i in range(4):
            ports[f"val{i}"] = dict(
                rpc=pb + 646 + i,
                conn=pb + 200 + i,
                p2p=pb + 100 + i,
                peer_rpc=pb + 300 + i,
                metrics=pb + 400 + i,
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
            "chain_id": 991, "epoch_duration_seconds": 86400, "epoch_timestamp_ms": 1700000000000,
            "validators": [{
                "name": "node-0", "address": "0x7e615e4a500ab42b7bb3fdbb62fbb8bd10385fc5", "stake": "1000000000000000000",
                "authority_key": "kUYrYvf/fDUygF8+nIdNATAAlnQU3BZSD3aGuHNoAZQv3OJOIZKW+Uw+UbH/1LWCAlbyWnQra9vUSDJfFVIxlV4XlraaNkLsZSb3HMJJQK3qEc1L20Yqb5YM8uGRXvnB",
                "protocol_key": "fN/BNA8PFyjE3hclyjxnkYgjFlR6M27jpbocq7X847Y=",
                "network_key": "jZ/kDNNPBsZXUD28FcxMLLZ+vCZCbEJoUvdB8zgRvug=",
                "p2p_address": "/ip4/127.0.0.1/tcp/%d" % ports["parent_p2p"],
            }],
            "accounts": [{"address": "0x7e615e4a500ab42b7bb3fdbb62fbb8bd10385fc5", "balance": "1000000000000000000000"}],
            "open_cluster_registration": True, "clusters": [],
            "float_accounts": [{"bls_public_key": CLUSTERS[0]["bls_pub"], "balance": FLOAT}],
            "min_float_to_register": "1000000000000000000000", "allow_deposit_to_float": True,
        }
        with open(os.path.join(pdir, "parent_genesis.json"), "w") as f:
            json.dump(parent_genesis, f, indent=2)

        # ---------------- 4 validators ----------------
        with open(os.path.join(a.repo, "execution/cmd/simple_chain/genesis.json")) as f:
            gbase = json.load(f)

        def acc(addr, bls):
            return {"address": addr, "balance": BIG, "pending_balance": "0", "last_hash": ZERO32,
                    "device_key": ZERO32, "publicKeyBls": bls}
        alloc = {}
        for x in gbase["alloc"][:20]:
            alloc[x["address"].lower()] = dict(x, publicKeyBls=CLUSTERS[0]["bls_pub"])
        alloc[FUNDER["address"].lower()] = acc(FUNDER["address"], FUNDER["bls_pub"])
        alloc[CLUSTERS[0]["address"].lower()] = acc(CLUSTERS[0]["address"], CLUSTERS[0]["bls_pub"])
        for v in VALS_4:
            alloc[v["address"].lower()] = acc(v["address"], v["bls_pub"])

        stake = "1000000000000000000000"
        validators = [
            {
                "address": v["address"],
                "primary_address": "127.0.0.1:4000",
                "worker_address": "127.0.0.1:4012",
                "p2p_address": f"/ip4/127.0.0.1/tcp/{ports[v['name']]['p2p']}",
                "description": f"E2E {v['name']} validator",
                "website": "", "image": "", "commission_rate": 5,
                "min_self_delegation": "1000000000000000000", "accumulated_rewards_per_share": "0",
                "delegator_stakes": [{"address": v["address"], "amount": stake}], "total_staked_amount": stake,
                "network_key": v["network_key"], "hostname": v["name"],
                "authority_key": v["authority_key"], "protocol_key": v["protocol_key"],
            } for v in VALS_4
        ]
        g = dict(gbase)
        g["config"] = dict(gbase["config"], chainId=991, epoch_timestamp_ms=int(time.time() * 1000))
        g["validators"] = validators
        g["total_stake"], g["quorum_threshold"], g["validity_threshold"] = 4000, 2667, 1334
        g["alloc"] = list(alloc.values())
        g["registered_accounts"] = []

        gpath = os.path.join(base, "genesis.json")
        with open(gpath, "w") as f:
            json.dump(g, f, indent=2)

        for i, v in enumerate(VALS_4):
            d = os.path.join(base, v["name"])
            os.makedirs(d, exist_ok=True)
            p = ports[v["name"]]
            # Copy keys
            shutil.copy(os.path.join(cfg, f"node_{i}_protocol_key.json"), os.path.join(d, "node_protocol_key.json"))
            shutil.copy(os.path.join(cfg, f"node_{i}_network_key.json"), os.path.join(d, "node_network_key.json"))
            shutil.copy(gpath, os.path.join(d, "genesis.json"))

            # Write node_val.toml
            peer_rpcs = [f"127.0.0.1:{ports[f'val{j}']['peer_rpc']}" for j in range(4) if j != i]
            with open(os.path.join(d, "node_val.toml"), "w") as f:
                f.write(VAL_TOML.format(
                    node_id=i,
                    p2p=p["p2p"],
                    base=base,
                    name=v["name"],
                    metrics=p["metrics"],
                    peer_rpc=p["peer_rpc"],
                    peer_rpc_addresses=json.dumps(peer_rpcs),
                ))

            config = {
                "debug": True, "cluster_id": 1, "enable_private_gateway": False,
                "master_password": "devnet-test-password", "app_pepper": "devnet-test-pepper",
                "private_key": CLUSTERS[0]["private_key"], "address": v["address"],
                "log_path": d + "/logs", "backup_path": d + "/backup",
                "explorer_db_path": d + "/explorer", "explorer_read_only_db_path": d + "/explorer-ro",
                "is_explorer": True, "connection_address": "0.0.0.0:%d" % p["conn"], "version": "0.0.1.0",
                "rpc_port": ":%d" % p["rpc"], "peer_rpc_port": p["peer_rpc"], "db_type": 2,
                "genesis_file_path": os.path.join(d, "genesis.json"),
                "rust_config_path": os.path.join(d, "node_val.toml"),
                "Databases": {"RootPath": d + "/data", "DBEngine": "sharded", "Version": "0.0.1.0",
                              "BLSPrivateKey": v["bls_priv"], "SnapshotPath": d + "/snapshot"},
                "is_rpc_node": True, "snapshot_enabled": False,
                "tx_signature_mode": "secp", "account_gate": "parent_registered",
            }
            with open(os.path.join(d, "config.json"), "w") as f:
                json.dump(config, f, indent=2)

        env = dict(
            base=base, bin=os.path.abspath(a.bin), ports=ports, funder=FUNDER["address"],
            mode="mysticeti_4val",
            cluster=dict(address=CLUSTERS[0]["address"], bls_pub=CLUSTERS[0]["bls_pub"], private_key=CLUSTERS[0]["private_key"]),
            validators={v["name"]: dict(id=v["id"], address=v["address"], bls_pub=v["bls_pub"], bls_priv=v["bls_priv"]) for v in VALS_4}
        )
        with open(os.path.join(base, "env.json"), "w") as f:
            json.dump(env, f, indent=2)
        print(json.dumps(env, indent=2))
        return

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
        "chain_id": 991, "epoch_duration_seconds": 86400, "epoch_timestamp_ms": 1700000000000,
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
                          "BLSPrivateKey": c["private_key"], "SnapshotPath": d + "/snapshot"},
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

