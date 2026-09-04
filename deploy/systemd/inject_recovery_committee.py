#!/usr/bin/env python3
"""inject_recovery_committee.py — computes cross_chain.recovery_committee_json (2026-09-04,
replacing the deleted GovernanceEngine's propose/vote/execute gate for
declareChainDeadWithCert/unregisterChainWithCert/updateCommitteeWithRecoveryCert -- see
GatewayEngine.RecoveryCommittee's own doc comment, gateway.go) and injects it into every locally
generated node's execution.json.

Called by deploy/ansible/roles/local_build/tasks/main.yml's "Compute and inject RecoveryCommittee
config" task, AFTER "Generate Validator Keys" has produced a complete genesis.json (this script
reads it for the definitive validator address list, rather than trusting every node-*_keys/
directory under this same folder -- that directory accumulates unrelated leftovers from other
experiments over time, e.g. a real stale 5th key set found live 2026-09-04 whose address wasn't
in the actual running committee; registering a phantom extra committee member would misrepresent
the real committee and skew QuorumThreshold math).

Reuses this cluster's OWN real validator authority keys as the RecoveryCommittee (matches this
devnet's trust model: the same operators already trusted to run consensus for the whole chain).
The real BLS pubkey derivation + Proof-of-Possession signature is computed by the Go helper
gen_recovery_committee (execution/cmd/tool/gen_recovery_committee) -- not safe to reimplement in
Python.

Usage: inject_recovery_committee.py <deploy_dir> <num_nodes> <gen_recovery_committee_bin>
"""
import json
import os
import subprocess
import sys


def main():
    if len(sys.argv) != 4:
        print("usage: inject_recovery_committee.py <deploy_dir> <num_nodes> <gen_recovery_committee_bin>", file=sys.stderr)
        sys.exit(1)
    deploy_dir, num_nodes, gen_bin = sys.argv[1], int(sys.argv[2]), sys.argv[3]

    genesis_path = os.path.join(deploy_dir, "genesis.json")
    try:
        with open(genesis_path) as f:
            genesis = json.load(f)
    except FileNotFoundError:
        print(f"WARNING: {genesis_path} not found -- RecoveryCommittee left unconfigured")
        return

    live_addresses = {
        v.get("address", "").lower()
        for v in genesis.get("validators", [])
        if v.get("address")
    }

    secrets = []
    for entry in sorted(os.listdir(deploy_dir)):
        key_dir = os.path.join(deploy_dir, entry)
        if not (entry.startswith("node-") and entry.endswith("_keys") and os.path.isdir(key_dir)):
            continue
        summary_path = os.path.join(key_dir, "keys_summary.json")
        auth_path = os.path.join(key_dir, "authority_key.json")
        if not (os.path.exists(summary_path) and os.path.exists(auth_path)):
            continue
        with open(summary_path) as f:
            eth_addr = json.load(f).get("eth_address", "").lower()
        if eth_addr not in live_addresses:
            print(f"  (skipping {entry}: address {eth_addr} not in live genesis -- stale/unrelated key dir)")
            continue
        with open(auth_path) as f:
            secrets.append(json.load(f).get("private_key_hex", ""))

    if not secrets:
        print("WARNING: no local validator keys matched the live genesis -- RecoveryCommittee left unconfigured")
        return

    result = subprocess.run([gen_bin] + secrets, capture_output=True, text=True)
    if result.returncode != 0:
        print(f"ERROR: {gen_bin} failed: {result.stderr}", file=sys.stderr)
        sys.exit(1)
    committee_json = result.stdout.strip()

    for node_id in range(num_nodes):
        path = os.path.join(deploy_dir, f"node-{node_id}_keys", "execution.json")
        try:
            with open(path) as f:
                cfg = json.load(f)
        except FileNotFoundError:
            continue
        cc = cfg.setdefault("cross_chain", {})
        cc["recovery_committee_json"] = committee_json
        cc["recovery_quorum_threshold"] = 6667
        # devnet_governance_timelock_seconds_override (GovernanceEngine, removed 2026-09-04) is
        # dead config -- no Go struct field reads it any more (pkg/config/config.go). Drop it
        # while patching this file anyway, rather than leaving stale config that misleadingly
        # suggests it still does something.
        cc.pop("devnet_governance_timelock_seconds_override", None)
        with open(path, "w") as f:
            json.dump(cfg, f, indent=2)
        print(f"  ✅ node-{node_id}: recovery_committee_json injected ({len(secrets)} validators, {len(committee_json)} bytes)")


if __name__ == "__main__":
    main()
