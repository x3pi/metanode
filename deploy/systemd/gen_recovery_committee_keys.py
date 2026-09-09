#!/usr/bin/env python3
"""gen_recovery_committee_keys.py — generates (or reuses, if already present) an INDEPENDENT set
of real BLS keys for cross_chain.RecoveryCommittee (2026-09-04 -- see GatewayEngine.
RecoveryCommittee's own doc comment, gateway.go), deliberately SEPARATE from any chain's own
consensus/committee validator keys.

Why separate: RecoveryCommittee is the sole authorizer for declareChainDeadWithCert/
unregisterChainWithCert/updateCommitteeWithRecoveryCert -- actions with real, chain-wide blast
radius (it can install a brand-new committee for, or entirely remove, ANY registered chain,
including the Root Anchor's own self-registration). An earlier version of this devnet's deploy
tooling reused the Root Anchor's OWN 4 consensus validator keys as RecoveryCommittee (convenient
for live-verification, since those keys already existed) -- found in a follow-up security review
to be a real, avoidable concentration of power: whoever holds >= quorum of those SAME keys both
controls Root Anchor's real BFT consensus AND has full RecoveryCommittee authority over every
other registered chain. This script exists so RecoveryCommittee gets its own, independent identity
by default instead.

Idempotent by design and REQUIRED to be: unlike validator keys (which this repo's other genesis
generators regenerate fresh on every `Action: setup` redeploy -- see
note/eurozone_unified_native_coin_plan.md's "Phát hiện vận hành" section), RecoveryCommittee is
meant to be a long-lived, stable, out-of-band trusted authority. Silently rotating it on every
redeploy would be a real footgun: any state a prior RecoveryCommittee touched (chains it declared
dead, committees it installed) would become inconsistent with the new committee's identity, and
worse, a REAL production deployment re-running `setup` would silently and invisibly swap out its
own trusted recovery authority. If keys already exist under --out-dir, this script reuses them
and does nothing else.

For a REAL (non-devnet) deployment: do not rely on this script's auto-generated keys at all --
generate RecoveryCommittee's keys and PoP entirely out-of-band (ideally on air-gapped hardware,
one member per distinct physical custodian), and set the `recovery_committee_json` field directly
via inventory.yml's `recovery_committee_json_override` instead of letting this script run. This
script's auto-generated keys are written to local disk in plaintext -- appropriate for devnet
live-verification, not for real recovery authority over real value.

Usage: gen_recovery_committee_keys.py --out-dir DIR --metanode-bin PATH [--count N]
"""
import argparse
import json
import os
import subprocess
import sys


def generate_one_key(metanode_bin: str, member_dir: str) -> str:
    os.makedirs(member_dir, exist_ok=True)
    result = subprocess.run(
        [metanode_bin, "keytool", "generate", "validator", "--out-dir", member_dir],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        print(f"ERROR: metanode keytool failed for {member_dir}:\n{result.stderr}", file=sys.stderr)
        sys.exit(1)
    auth_file = os.path.join(member_dir, "authority_key.json")
    if not os.path.exists(auth_file):
        print(f"ERROR: {auth_file} not found after generation", file=sys.stderr)
        sys.exit(1)
    with open(auth_file) as f:
        return json.load(f)["private_key_hex"]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--out-dir", required=True, help="Directory to store (or reuse) RecoveryCommittee member keys")
    parser.add_argument("--metanode-bin", required=True, help="Path to the metanode binary (for keytool)")
    parser.add_argument("--count", type=int, default=4, help="Number of RecoveryCommittee members (default: 4)")
    args = parser.parse_args()

    secrets = []
    for i in range(args.count):
        member_dir = os.path.join(args.out_dir, f"member_{i}")
        auth_file = os.path.join(member_dir, "authority_key.json")
        if os.path.exists(auth_file):
            with open(auth_file) as f:
                secrets.append(json.load(f)["private_key_hex"])
            print(f"  ℹ️  member_{i}: reusing existing key ({auth_file})")
        else:
            secret = generate_one_key(args.metanode_bin, member_dir)
            secrets.append(secret)
            print(f"  ✅ member_{i}: generated a fresh, independent RecoveryCommittee key")

    print(" ".join(secrets))


if __name__ == "__main__":
    main()
