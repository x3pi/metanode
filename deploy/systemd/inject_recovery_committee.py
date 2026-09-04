#!/usr/bin/env python3
"""inject_recovery_committee.py — computes cross_chain.recovery_committee_json (2026-09-04,
replacing the deleted GovernanceEngine's propose/vote/execute gate for
declareChainDeadWithCert/unregisterChainWithCert/updateCommitteeWithRecoveryCert -- see
GatewayEngine.RecoveryCommittee's own doc comment, gateway.go) and injects it into every locally
generated node's execution.json.

Called by deploy/ansible/roles/local_build/tasks/main.yml's "Compute and inject RecoveryCommittee
config" task, after gen_recovery_committee_keys.py has ensured this cluster's own INDEPENDENT
RecoveryCommittee key material exists (see that script's own doc comment for why RecoveryCommittee
must NOT reuse the Root Anchor's own consensus validator keys -- a real power-concentration finding
from a 2026-09-04 security re-review, fixed the same day).

Two sources, in priority order:
  1. --override-file: a pre-built []cross_chain.ValidatorEntry JSON file (pubkey + PopSignature
     only, no private key material) supplied by the operator out-of-band -- the ONLY appropriate
     source for a real, non-devnet deployment (see gen_recovery_committee_keys.py's doc comment).
     Used verbatim, unmodified, if present.
  2. --keys-dir: this cluster's own auto-generated, independent RecoveryCommittee keys (devnet
     default) -- computed into public ValidatorEntry material by the Go helper
     gen_recovery_committee (real BLS pubkey derivation + a real Proof-of-Possession signature;
     not safe to reimplement in Python).

Usage: inject_recovery_committee.py <deploy_dir> <num_nodes> <gen_recovery_committee_bin> <keys_dir> [--override-file FILE]
"""
import argparse
import glob
import json
import os
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("deploy_dir")
    parser.add_argument("num_nodes", type=int)
    parser.add_argument("gen_recovery_committee_bin")
    parser.add_argument("keys_dir")
    parser.add_argument("--override-file", default=None)
    args = parser.parse_args()

    if args.override_file and os.path.exists(args.override_file):
        with open(args.override_file) as f:
            committee_json = f.read().strip()
        print(f"  ℹ️  Using operator-supplied RecoveryCommittee override: {args.override_file}")
    else:
        secrets = []
        for auth_file in sorted(glob.glob(os.path.join(args.keys_dir, "member_*", "authority_key.json"))):
            with open(auth_file) as f:
                secrets.append(json.load(f)["private_key_hex"])
        if not secrets:
            print(f"WARNING: no RecoveryCommittee keys found under {args.keys_dir} -- RecoveryCommittee left unconfigured (fail-closed by design)")
            return
        result = subprocess.run([args.gen_recovery_committee_bin] + secrets, capture_output=True, text=True)
        if result.returncode != 0:
            print(f"ERROR: {args.gen_recovery_committee_bin} failed: {result.stderr}", file=sys.stderr)
            sys.exit(1)
        committee_json = result.stdout.strip()
        print(f"  ℹ️  Using this cluster's own auto-generated RecoveryCommittee ({len(secrets)} members, devnet default)")

    for node_id in range(args.num_nodes):
        path = os.path.join(args.deploy_dir, f"node-{node_id}_keys", "execution.json")
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
        print(f"  ✅ node-{node_id}: recovery_committee_json injected ({len(committee_json)} bytes)")


if __name__ == "__main__":
    main()
