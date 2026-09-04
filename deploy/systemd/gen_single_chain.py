#!/usr/bin/env python3
"""
gen_single_chain.py — Single Chain Initializer and Configuration Generator.

Generates complete genesis, validator keys, pre-funded developer accounts,
Go execution configs, Rust consensus configs, and start/stop scripts for a
Metanode Single Chain (supporting 1 validator).

Usage:
  python3 gen_single_chain.py \
    --chain-id 1337 \
    --validators 1 \
    --output-dir ./single_chain_data \
    --alloc-balance 1000000 \
    --dev-accounts 5

Options:
  --chain-id INT       EVM Chain ID for single chain (default: 1337)
  --validators INT     Number of validator nodes (default: 1 for single validator)
  --ip IP              IP address for nodes (default: 127.0.0.1)
  --output-dir DIR     Output directory for chain configs & data (default: ./single_chain_data)
  --alloc-balance INT  Initial balance in MTN for pre-funded accounts (default: 1000000 MTN)
  --dev-accounts INT   Number of additional pre-funded dev ETH accounts (default: 5)
  --metanode-bin PATH  Path to metanode binary (auto-detected if omitted)

Deterministic-genesis mode (2026-09-04, opt-in -- everything above is unchanged when these are
omitted): pass BOTH --initial-supply-wallet and --initial-supply to make this run cross-check
against Root Anchor and produce a genesis whose ENTIRE native-coin supply is exactly the amount
that chain was credited when it registered via RegisterChainViaStake -- no --alloc-balance, no
dev-account funding, no genesis.json.example injection, no free money anywhere. Every validator of
that chain runs this with the SAME two values (shared out-of-band by whoever registered, e.g. via
`register_chains -action query-alloc-raw`) to produce byte-identical genesis.json files, then
publishes/verifies its digest via `register_chains -action publish-genesis-digest|verify-genesis`
-- see note/eurozone_unified_native_coin_plan.md and GatewayEngine.RegisterChainViaStake /
SetGenesisDigest's own doc comments for the full design.

  --initial-supply-wallet 0x...   Wallet that must hold the chain's entire initial supply.
  --initial-supply WEI            Exact initial supply, base-10 wei (verified, not trusted).
"""

import json
import sys
import os
import subprocess
import argparse
import shutil
import base64
import secrets
import hashlib
from pathlib import Path

def derive_devnet_submitter_account(chain_id: int, node_index: int = 0):
    """Deterministically derive a devnet-only secp256k1 keypair for this node's
    CommitAttestationWorker "submitter" account, keyed by (chain_id, node_index).

    Must match gen_root_anchor_chain.py's derive_devnet_submitter_account()
    bit-for-bit -- Root Anchor's genesis pre-registers this same account so
    submitCommitAttestation() txs from it aren't rejected for "no BLS public
    key registered on-chain". See that function's docstring for the full
    root-cause writeup.

    DEVNET ONLY. This key is derivable by anyone who reads this source file --
    never use it to hold real value. Production deployments must generate a
    real, secret, per-chain, per-node submitter key and register it on Root
    Anchor (a real registration transaction/process, not a hardcoded genesis
    alloc).
    """
    if node_index == 0:
        seed = f"metanode-devnet-submitter-chain-{chain_id}".encode()
    else:
        seed = f"metanode-devnet-submitter-chain-{chain_id}-node-{node_index}".encode()
    priv_hex = hashlib.sha256(seed).hexdigest()
    try:
        from eth_account import Account
        address = Account.from_key(priv_hex).address
    except ImportError:
        import eth_keys
        address = eth_keys.keys.PrivateKey(bytes.fromhex(priv_hex)).public_key.to_checksum_address()
    return priv_hex, address

def address_from_privkey_hex(priv_hex: str) -> str:
    """Derive an ETH address from a raw secp256k1 private key hex string -- same eth_account/
    eth_keys fallback pattern as derive_devnet_submitter_account (kept as a separate small
    function rather than refactoring that one, to avoid touching an already-working code path)."""
    priv_hex = priv_hex.strip()
    if priv_hex.startswith("0x") or priv_hex.startswith("0X"):
        priv_hex = priv_hex[2:]
    try:
        from eth_account import Account
        return Account.from_key(priv_hex).address
    except ImportError:
        import eth_keys
        return eth_keys.keys.PrivateKey(bytes.fromhex(priv_hex)).public_key.to_checksum_address()

# Fixed 4-byte ABI selectors for the 2 read-only Gateway views the deterministic-genesis
# cross-check below needs (getAllocation(uint256), getChainRegistry(uint256)) -- computed once
# from execution/pkg/blockchain/tx_processor/abi_contract/gatewayAbi.go's ABI and hardcoded here
# rather than recomputed at runtime, since Python's stdlib has no Keccak256 (hashlib's "sha3_256"
# is the different NIST-finalized SHA-3, not the Keccak Ethereum actually uses) and pulling in a
# non-stdlib dependency just for 2 fixed selectors isn't worth it. If gatewayAbi.go's function
# signatures for these two ever change, these must be recomputed (see scratchpad note in the
# 2026-09-04 session that first added them, or just run any ABI's Method.ID for the new signature).
_SELECTOR_GET_ALLOCATION = "5a5804b3"
_SELECTOR_GET_CHAIN_REGISTRY = "e4dad163"
GATEWAY_CONTRACT_ADDRESS = "0x0000000000000000000000000000000000001002"

def _eth_call(rpc_url: str, selector: str, chain_id: int, timeout: float = 5.0) -> str:
    """Raw JSON-RPC eth_call to the Gateway contract, calldata = selector + chainId as a single
    left-padded uint256 word (the shape every view method touched here happens to share: exactly
    one uint256 chainId argument). Returns the raw 0x-prefixed hex result string."""
    import urllib.request
    data = "0x" + selector + f"{chain_id:064x}"
    payload = json.dumps({
        "jsonrpc": "2.0", "method": "eth_call",
        "params": [{"to": GATEWAY_CONTRACT_ADDRESS, "data": data}, "latest"],
        "id": 1,
    }).encode()
    req = urllib.request.Request(rpc_url, data=payload, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        rdata = json.loads(resp.read().decode())
    if "error" in rdata:
        raise RuntimeError(f"eth_call to {rpc_url} failed: {rdata['error']}")
    result = rdata.get("result")
    if not result or not isinstance(result, str):
        raise RuntimeError(f"eth_call to {rpc_url} returned no result: {rdata}")
    return result

def fetch_allocation_from_root_anchor(rpc_url: str, chain_id: int) -> int:
    """Root Anchor's live SupplyLedger.PerChainAllocation[chain_id], in wei."""
    result = _eth_call(rpc_url, _SELECTOR_GET_ALLOCATION, chain_id)
    return int(result, 16)

def fetch_genesis_wallet_from_root_anchor(rpc_url: str, chain_id: int) -> str:
    """Root Anchor's live ChainRegistry[chain_id].GenesisWallet -- getChainRegistry's ABI-encoded
    tuple has (bool exists, bytes[] pubkeys, ...) with genesisWallet as its 12th (index 11) word;
    every dynamic-typed field before it (bytes[]/uint64[]/bytes[]/string) is encoded as a 32-byte
    OFFSET at its fixed slot, not inline, so genesisWallet's own fixed slot is still exactly word
    index 11 regardless of how much dynamic data follows -- verified against gateway_handler.go's
    getChainRegistry Pack() argument order (exists, pubkeys, stakes, popSignatures, epoch,
    quorumThreshold, gatewayContract, stateRoot, accountTreeRoot, archivalEndpoint, registeredAt,
    genesisWallet, genesisDigest)."""
    result = _eth_call(rpc_url, _SELECTOR_GET_CHAIN_REGISTRY, chain_id)
    hexbody = result[2:] if result.startswith("0x") else result
    word_11_start = 11 * 64
    word_11 = hexbody[word_11_start:word_11_start + 64]
    return "0x" + word_11[-40:]

# ─── Colors ───────────────────────────────────────────────────────────────────
def green(s):  return f"\033[32m{s}\033[0m"
def yellow(s): return f"\033[33m{s}\033[0m"
def red(s):    return f"\033[31m{s}\033[0m"
def cyan(s):   return f"\033[36m{s}\033[0m"
def bold(s):   return f"\033[1m{s}\033[0m"

SCRIPT_DIR = Path(__file__).parent.resolve()
REPO_ROOT  = SCRIPT_DIR.parent.parent  # metanode/

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

def generate_validator_keys(metanode_bin: str, keys_dir: str) -> tuple:
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

    for fpath in [auth_file, proto_file, net_file, eth_file]:
        if not os.path.exists(fpath):
            print(red(f"ERROR: Key file not found after generation: {fpath}"))
            sys.exit(1)

    with open(auth_file) as f: auth_data = json.load(f)
    with open(proto_file) as f: proto_data = json.load(f)
    with open(net_file) as f: net_data = json.load(f)
    with open(eth_file) as f: eth_data = json.load(f)

    def rewrite_key_as_base64(key_data, file_path):
        priv_bytes = bytes.fromhex(key_data["private_key_hex"])
        pub_bytes = base64.b64decode(key_data["public_key_base64"])
        combined = priv_bytes + pub_bytes
        b64_str = base64.b64encode(combined).decode('utf-8')
        with open(file_path, "w") as fw:
            fw.write(b64_str)

    rewrite_key_as_base64(proto_data, proto_file)
    rewrite_key_as_base64(net_data, net_file)

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

_BLS_PUBKEY_BIN_CACHE = None

def derive_min_pk_pubkey(secret_hex: str) -> str:
    """Derives the real pkg/bls (min-pk, 48-byte G1) public key from a BLS secret scalar, as
    base64. Real fix for a genesis-generation bug found 2026-08-26 while live-testing P4 relayer
    automation: this script used to write the min_sig (96-byte G2, Rust consensus authority)
    public key straight into an account's genesis publicKeyBls field -- but
    AccountState.SetPublicKeyBls (execution/pkg/state/account_state.go) requires EXACTLY 48
    bytes, the min-pk convention every cross-chain BLS call in the Go codebase actually uses
    (CommitteeAttestationWorker/CommitAttestationWorker signing with Databases.BLSPrivateKey,
    register_chains building founding committees). Writing the wrong 96-byte encoding meant a
    validator's own on-chain identity never matched the min-pk pubkey it (and register_chains)
    actually signs with -- committeeContains() never found a match, so validators silently never
    submitted a single real commit/committee attestation share, and cross-chain automation
    (RelayerDaemon.WatchChainPair) hung forever waiting for a quorum that could never form.
    Same secret scalar, but min-pk and min-sig derive genuinely different, incompatible public
    keys from it -- there is no way to convert one to the other, only to derive both separately
    (metanode-keytool already generated the min_sig half; this derives the min-pk half via the
    same execution/pkg/bls Go library every real cross-chain caller uses)."""
    global _BLS_PUBKEY_BIN_CACHE
    if _BLS_PUBKEY_BIN_CACHE is None:
        candidates = [
            SCRIPT_DIR / "bin" / "bls_pubkey",
            SCRIPT_DIR / "bls_pubkey",
            REPO_ROOT / "execution" / "bls_pubkey",
            REPO_ROOT / "execution" / "cmd" / "tool" / "bls_pubkey" / "bls_pubkey",
            Path(shutil.which("bls_pubkey") or ""),
        ]
        bin_path = None
        for c in candidates:
            if c and c.is_file():
                bin_path = c
                break

        if bin_path is None:
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

def generate_eth_dev_account(metanode_bin=None):
    """Generates a random secp256k1 Ethereum private key and derives its 0x address without external dependencies."""
    try:
        from eth_keys import keys
        priv_key_bytes = secrets.token_bytes(32)
        pk = keys.PrivateKey(priv_key_bytes)
        return {
            "private_key": "0x" + priv_key_bytes.hex(),
            "address": pk.public_key.to_checksum_address()
        }
    except ImportError:
        if metanode_bin:
            import tempfile
            with tempfile.TemporaryDirectory() as tmpdir:
                subprocess.run([metanode_bin, "keytool", "generate", "validator", "--out-dir", tmpdir], capture_output=True)
                eth_file = os.path.join(tmpdir, "eth_key.json")
                if os.path.exists(eth_file):
                    with open(eth_file) as f:
                        data = json.load(f)
                    return {
                        "private_key": data["ETH_PRIVATE_KEY"],
                        "address": data["ETH_ADDRESS"]
                    }
        print(red("ERROR: 'eth_keys' python module or 'metanode' binary is required to generate dev accounts."))
        sys.exit(1)

def load_or_generate_private_dev_keys(keys_file: Path, chain_id: int, count: int = 6, metanode_bin: str = None) -> tuple:
    """
    Loads fixed/persistent developer keys from a local (git-ignored) JSON file.
    If the file or the chain_id entry does not exist, generates fresh accounts and persists them locally.
    Returns:
      (current_chain_dev_accounts, all_system_dev_accounts)
    """
    data = {}
    if keys_file.exists():
        try:
            with open(keys_file, "r") as f:
                data = json.load(f)
        except Exception as e:
            print(yellow(f"  ⚠️ Warning reading {keys_file}: {e}. Creating new storage."))
            data = {}

    chain_key_str = str(chain_id)
    if chain_key_str not in data or not isinstance(data[chain_key_str], list) or len(data[chain_key_str]) == 0:
        roles = [
            f"Sender ({'A' if chain_id % 2 != 0 else 'B'}0)",
            f"Dev {'A' if chain_id % 2 != 0 else 'B'}1",
            f"Dev {'A' if chain_id % 2 != 0 else 'B'}2",
            f"Dev {'A' if chain_id % 2 != 0 else 'B'}3",
            f"Dev {'A' if chain_id % 2 != 0 else 'B'}4",
            f"Relayer {'A' if chain_id % 2 != 0 else 'B'} ({'A' if chain_id % 2 != 0 else 'B'}5)",
        ]
        new_chain_accounts = []
        for i in range(max(count, len(roles))):
            acc = generate_eth_dev_account(metanode_bin)
            role_name = roles[i] if i < len(roles) else f"Dev {i}"
            acc["role"] = role_name
            new_chain_accounts.append(acc)

        data[chain_key_str] = new_chain_accounts
        try:
            with open(keys_file, "w") as fw:
                json.dump(data, fw, indent=2)
            os.chmod(keys_file, 0o600)
            print(green(f"  🔑 Generated & saved {len(new_chain_accounts)} dev keys to local ignored file: {keys_file.name}"))
        except Exception as e:
            print(yellow(f"  ⚠️ Could not persist dev keys to {keys_file}: {e}"))

    current_chain_accounts = data.get(chain_key_str, [])
    all_system_accounts = []
    for cid_str, accs in data.items():
        if isinstance(accs, list):
            all_system_accounts.extend(accs)

    return current_chain_accounts, all_system_accounts

# Devnet-only fallback: the literal value every gateway_bls_key defaulted to before
# --random-gateway-bls-key existed (see note/security_variables_reference.md mục 3.1). Kept as
# the default so existing devnet/smoke-test flows are byte-for-byte unchanged -- dev_accounts.json
# above pre-registers a publicKeyBls derived from THIS EXACT secret (see its own comment) so that
# plain eth_sendRawTransaction from those throwaway accounts passes the on-chain BLS registration
# check without a real registration flow. Changing this default would silently break that.
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
    parser = argparse.ArgumentParser(description="Generate single chain configs for Metanode")
    parser.add_argument("--chain-id", type=int, default=1337, help="EVM Chain ID (default: 1337)")
    parser.add_argument("--validators", type=int, default=1, help="Number of validators (default: 1)")
    parser.add_argument("--ip", default="127.0.0.1", help="Node IP address (default: 127.0.0.1)")
    parser.add_argument("--output-dir", default="./single_chain_data", help="Output directory (default: ./single_chain_data)")
    parser.add_argument("--alloc-balance", type=int, default=1000000, help="Initial MTN balance per account (default: 1000000)")
    parser.add_argument("--dev-accounts", type=int, default=5, help="Number of funded dev accounts (default: 5)")
    parser.add_argument("--dev-keys-file", default=None, help="Path to local dev keys JSON file (default: deploy/systemd/private_dev_keys.json)")
    parser.add_argument("--metanode-bin", default=None, help="Path to metanode binary")
    parser.add_argument("--rpc-port", type=int, default=8545, help="Base RPC Port (default: 8545)")
    parser.add_argument("--port-offset", type=int, default=0, help="Port offset for primary, worker, p2p, dns ports (default: 0)")
    parser.add_argument("--is-rpc", action="store_true", help="Enable RPC node mode for the validators")
    parser.add_argument("--epochs-to-keep", type=int, default=None, help="Number of epochs to keep (default: 0 if --is-rpc else 5)")
    parser.add_argument("--genesis-template", default=None, help="Path to genesis template file (default: deploy/systemd/genesis.json.example)")
    parser.add_argument("--no-example-alloc", action="store_true", help="Do not inject accounts from genesis.json.example")
    parser.add_argument("--inject-example-alloc", action="store_true", default=True, help="Inject accounts from genesis.json.example (default: True)")
    parser.add_argument("--root-anchor-rpc", type=str, default="", help="Comma-separated list of Root Anchor RPC URLs (e.g. http://127.0.0.1:9099)")
    parser.add_argument("--root-anchor-submitter-key", type=str, default="", help="ECDSA private key for the committee attestation worker")
    parser.add_argument("--gateway-bls-key", type=str, default=None, help="Explicit BLS secret (hex) for gateway_bls_key (Private Gateway signing). Default: shared devnet-only key -- pass this or --random-gateway-bls-key for any real deployment.")
    parser.add_argument("--random-gateway-bls-key", action="store_true", help="Generate a fresh, independent gateway_bls_key per node instead of the shared devnet default. Recommended for any real deployment; does nothing to existing devnet/smoke-test flows unless passed explicitly.")
    parser.add_argument("--reserve-chain-id", type=int, default=None, help="Chain ID of the system's Reserve chain (default: same as --chain-id)")
    parser.add_argument("--debug", action="store_true", help="Generate nodes with debug logging and a pprof HTTP listener enabled (localhost only). Off by default -- intended for local benchmarking, not production deploys.")
    # Deterministic-genesis flags (2026-09-04) -- see this file's own module docstring update and
    # note/eurozone_unified_native_coin_plan.md. When BOTH are set, this run switches from the old
    # "make up an alloc_balance for every account" model to: genesis native-coin alloc == EXACTLY
    # the amount this chain was credited when it registered on Root Anchor (RegisterChainViaStake
    # -> SupplyLedger.PerChainAllocation, see GatewayEngine.RegisterChainViaStake's own doc
    # comment), assigned to the exact wallet that paid for it. No other account gets a free
    # balance in this mode -- total genesis supply is provably == what was actually registered,
    # not whatever an operator happens to type into --alloc-balance.
    parser.add_argument("--initial-supply-wallet", type=str, default="", help="Deterministic-genesis mode: the wallet address (0x...) that must hold this chain's entire initial native-coin supply -- must equal the address that paid the RegisterChainViaStake stake. Requires --initial-supply and --root-anchor-rpc.")
    parser.add_argument("--initial-supply", type=str, default="", help="Deterministic-genesis mode: exact initial supply in wei (base-10 string) -- cross-checked against Root Anchor's live SupplyLedger.PerChainAllocation for this chain ID; the run aborts on any mismatch instead of silently using an unverified number.")
    args = parser.parse_args()

    print(bold(cyan("\n=== 🌐 Metanode Single Chain Initializer ===")))
    
    metanode_bin = find_metanode_bin(args.metanode_bin)
    if not metanode_bin:
        print(yellow("⚠️  metanode binary not found. Attempting to build target/release/metanode ..."))
        build_res = subprocess.run(["cargo", "build", "--release", "-p", "metanode-node"], cwd=str(REPO_ROOT))
        if build_res.returncode != 0:
            print(red("❌ Failed to build metanode binary!"))
            sys.exit(1)
        metanode_bin = str(REPO_ROOT / "target/release/metanode")

    print(f"  Using Metanode binary: {green(metanode_bin)}")
    out_dir = Path(args.output_dir).resolve()
    os.makedirs(out_dir, exist_ok=True)
    print(f"  Output directory:      {green(str(out_dir))}")
    print(f"  Chain ID:              {cyan(args.chain_id)}")
    print(f"  Validators:            {cyan(args.validators)}")

    # Auto-resolve reserve_chain_id from Root Anchor RPC if not explicitly provided
    if args.reserve_chain_id is None:
        if args.root_anchor_rpc:
            detected_id = None
            try:
                import urllib.request
                req = urllib.request.Request(
                    args.root_anchor_rpc,
                    data=b'{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}',
                    headers={"Content-Type": "application/json"}
                )
                with urllib.request.urlopen(req, timeout=3) as resp:
                    rdata = json.loads(resp.read().decode())
                    if "result" in rdata and isinstance(rdata["result"], str):
                        detected_id = int(rdata["result"], 16)
            except Exception:
                pass
            args.reserve_chain_id = detected_id if detected_id is not None else 991
        else:
            args.reserve_chain_id = args.chain_id

    # Default submitter key to shared devnet key if Root Anchor is configured
    if not args.root_anchor_submitter_key and args.root_anchor_rpc:
        args.root_anchor_submitter_key = "d3d8157f2571153bcb664233f998a82b9b475fe509f92caf65ca2461bae7f1a9"

    # Deterministic-genesis mode (2026-09-04): cross-check --initial-supply/--initial-supply-wallet
    # against Root Anchor's OWN live record BEFORE trusting them for anything -- this is what makes
    # every validator's independently-generated genesis.json byte-identical and provably tied to a
    # real, already-paid registration, not just "whatever number was typed into this one run". See
    # note/eurozone_unified_native_coin_plan.md and RegisterChainViaStake's own doc comment.
    deterministic_genesis = bool(args.initial_supply_wallet) or bool(args.initial_supply)
    if deterministic_genesis:
        if not args.initial_supply_wallet or not args.initial_supply:
            print(red("❌ --initial-supply-wallet and --initial-supply must be given TOGETHER (deterministic-genesis mode requires both)."))
            sys.exit(1)
        if not args.root_anchor_rpc:
            print(red("❌ Deterministic-genesis mode requires --root-anchor-rpc to cross-check the supplied amount/wallet against Root Anchor's own record."))
            sys.exit(1)
        try:
            initial_supply_int = int(args.initial_supply)
        except ValueError:
            print(red(f"❌ --initial-supply must be a base-10 wei integer, got: {args.initial_supply}"))
            sys.exit(1)
        if initial_supply_int <= 0:
            print(red("❌ --initial-supply must be > 0."))
            sys.exit(1)

        verify_rpc = args.root_anchor_rpc.split(",")[0].strip()
        print(f"\n🔒 Deterministic-genesis mode: đối chiếu lại với Root Anchor ({verify_rpc}) trước khi tin bất kỳ số nào truyền vào ...")
        try:
            remote_alloc = fetch_allocation_from_root_anchor(verify_rpc, args.chain_id)
        except Exception as e:
            print(red(f"❌ Không đọc được PerChainAllocation từ Root Anchor: {e}"))
            sys.exit(1)
        if remote_alloc != initial_supply_int:
            print(red(f"❌ LỆCH: --initial-supply={initial_supply_int} nhưng Root Anchor ghi nhận chain {args.chain_id} có allocation={remote_alloc}. Dừng lại -- không tự ý dùng số không khớp."))
            sys.exit(1)

        try:
            remote_wallet = fetch_genesis_wallet_from_root_anchor(verify_rpc, args.chain_id)
        except Exception as e:
            print(red(f"❌ Không đọc được GenesisWallet từ Root Anchor: {e}"))
            sys.exit(1)
        if remote_wallet.lower() != args.initial_supply_wallet.lower():
            print(red(f"❌ LỆCH: --initial-supply-wallet={args.initial_supply_wallet} nhưng Root Anchor ghi nhận GenesisWallet={remote_wallet} cho chain {args.chain_id}. Dừng lại."))
            sys.exit(1)

        print(green(f"  ✅ Khớp: chain {args.chain_id} có allocation={initial_supply_int} wei, genesis_wallet={remote_wallet} -- đúng như Root Anchor ghi nhận."))

    # 1. Generate keys for validators
    validators_entries = []
    validator_keys_list = []
    alloc_list = []
    stake_wei = "1000000000000000000000" # 1000 MTN
    # Deterministic-genesis mode: no account gets a "made up" balance -- total native-coin supply
    # at genesis must equal EXACTLY the amount verified against Root Anchor above, assigned to
    # exactly one wallet (--initial-supply-wallet), added as the sole alloc entry further below.
    alloc_wei = "0" if deterministic_genesis else str(args.alloc_balance * (10**18))

    for node_id in range(args.validators):
        node_keys_dir = out_dir / f"node-{node_id}" / "keys"
        print(f"\n🔑 Generating keys for Validator node-{node_id} ...")
        bls, eth = generate_validator_keys(metanode_bin, str(node_keys_dir))
        # Real min-pk (48-byte G1) pubkey derived from the SAME secret scalar authority_key
        # uses -- see derive_min_pk_pubkey's doc comment for why this is a separate value from
        # bls["authority_key"] (that one stays min_sig/G2, used only for consensus identity).
        bls["min_pk_pubkey_b64"] = derive_min_pk_pubkey(bls["authority_key_private"])
        validator_keys_list.append((bls, eth))

        eth_addr = eth["address"].lower()
        p2p_port = 10200 + args.port_offset + node_id
        primary_port = 4200 + args.port_offset + node_id
        worker_port = 5012 + args.port_offset + node_id

        val_entry = {
            "address": eth_addr,
            "eth_private_key": eth["private_key"],
            "primary_address": f"{args.ip}:{primary_port}",
            "worker_address": f"{args.ip}:{worker_port}",
            "p2p_address": f"/ip4/{args.ip}/tcp/{p2p_port}",
            "description": f"Single Chain Validator node-{node_id}",
            "website": "",
            "image": "",
            "commission_rate": 5,
            "min_self_delegation": "1000000000000000000",
            "accumulated_rewards_per_share": "0",
            "delegator_stakes": [
                {"address": eth_addr, "amount": stake_wei}
            ],
            "total_staked_amount": stake_wei,
            "network_key": bls["network_key"],
            "hostname": f"node-{node_id}",
            "authority_key": bls["authority_key"],
            "protocol_key": bls["protocol_key"],
        }
        validators_entries.append(val_entry)

        # Fund validator account in genesis alloc
        alloc_list.append({
            "address": eth_addr,
            "balance": alloc_wei,
            "pending_balance": "0",
            "last_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
            "device_key": "0x0000000000000000000000000000000000000000000000000000000000000000",
            # Real min-pk pubkey (see derive_min_pk_pubkey's doc comment) -- NOT
            # bls["authority_key"] (min_sig/G2, consensus-only). Hex-encoded to match this
            # field's own convention (see the dev-account publicKeyBls entries below).
            "publicKeyBls": "0x" + base64.b64decode(bls["min_pk_pubkey_b64"]).hex()
        })

    # 2. Load or generate developer accounts from local ignored file (no secrets in git)
    dev_keys_path = Path(args.dev_keys_file) if args.dev_keys_file else (SCRIPT_DIR / "private_dev_keys.json")
    print(f"\n💰 Loading pre-funded developer accounts for Chain {args.chain_id} from {dev_keys_path.name} ...")
    dev_accounts, all_system_dev_accounts = load_or_generate_private_dev_keys(dev_keys_path, args.chain_id, args.dev_accounts, metanode_bin)

    seen_addrs = set(a["address"].lower() for a in alloc_list)
    for acc in all_system_dev_accounts:
        addr_str = acc["address"].lower()
        if addr_str in seen_addrs:
            continue
        seen_addrs.add(addr_str)
        alloc_list.append({
            "address": addr_str,
            "balance": alloc_wei,
            "pending_balance": "0",
            "last_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
            "device_key": "0x0000000000000000000000000000000000000000000000000000000000000000",
            "publicKeyBls": "0x86d5de6f7c9c13cc0d959a553cc0e4853ba5faae45a28da9bddc8ef8e104eb5d3dece8dfaa24f11b4243ec27537e3184"
        })

    with open(out_dir / "dev_accounts.json", "w") as f:
        json.dump(dev_accounts, f, indent=2)

    # Also pre-register the shared cross-chain RELAYER dev account (address derived
    # from start_relayer_daemon.sh's public devnet-only fallback RELAYER_KEY,
    # 0xd3ae7482f46f11cee2447bc711e9eb0fb79d4f2549781554cb962f54604e50f8) on THIS
    # chain's own genesis too -- gen_root_anchor_chain.py already registers it on
    # Root Anchor, but the RelayerDaemon submits batchOutboundCommit() to the
    # SOURCE private chain and attestCommit()/claimMessage() to the DESTINATION
    # private chain (never just Root Anchor), so without this every real relay
    # was rejected with "no BLS public key registered on-chain" the moment a
    # single relayer identity tried to act on ANY private chain. Found + fixed
    # 2026-08-26 via live E2E testing of the relayer's WatchChainPair loop.
    alloc_list.append({
        "address": "0x7d8bfbaba9268b59bab9ef8ff3f314d3f5747366",
        "balance": alloc_wei,
        "pending_balance": "0",
        "last_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
        "device_key": "0x0000000000000000000000000000000000000000000000000000000000000000",
        "publicKeyBls": "0x86d5de6f7c9c13cc0d959a553cc0e4853ba5faae45a28da9bddc8ef8e104eb5d3dece8dfaa24f11b4243ec27537e3184"
    })

    # 3. Create genesis.json
    total_stake = args.validators * 1000
    fault_tolerance = (total_stake - 1) // 3 if total_stake > 1 else 0
    quorum_threshold = total_stake - fault_tolerance
    validity_threshold = fault_tolerance + 1

    # Inject all accounts from genesis template (default: genesis.json.example) with deduplication
    template_path = None
    if args.genesis_template:
        template_path = Path(args.genesis_template)
    else:
        candidates = [
            SCRIPT_DIR / "genesis.json.example",
            SCRIPT_DIR / "config" / "genesis.json.example",
            REPO_ROOT / "deploy" / "systemd" / "genesis.json.example",
        ]
        for c in candidates:
            if c.exists():
                template_path = c
                break
    # Skipped entirely in deterministic-genesis mode: the template's accounts carry their OWN
    # hardcoded nonzero balances (not the local alloc_wei == "0" this run otherwise uses), which
    # would silently reintroduce free, unverified money into a genesis whose whole point is that
    # its total supply is provably == what Root Anchor actually credited.
    if deterministic_genesis:
        print(f"  🔒 Deterministic-genesis mode: bỏ qua injection từ {template_path.name if template_path else 'genesis template'} (không được có tiền free ngoài số đã xác minh).")
    elif not args.no_example_alloc and template_path and template_path.exists():
        try:
            with open(template_path, "r") as ef:
                example_data = json.load(ef)
                if "alloc" in example_data:
                    injected_count = 0
                    for ex_acc in example_data["alloc"]:
                        addr = ex_acc.get("address", "").lower()
                        if not addr or addr in seen_addrs:
                            continue
                        seen_addrs.add(addr)
                        alloc_list.append(ex_acc)
                        injected_count += 1
                    print(f"  💉 Injected {injected_count} accounts from {template_path.name} (Total unique alloc accounts: {len(alloc_list)})")
        except Exception as e:
            print(yellow(f"  ⚠️ Warning: could not merge {template_path} allocs: {e}"))

    if deterministic_genesis:
        # The ONLY balance in the entire genesis, matching exactly what was verified against Root
        # Anchor above -- every account created earlier in this run (validators, dev accounts, the
        # relayer devnet identity) still got REGISTERED (their BLS pubkey entries exist, needed so
        # e.g. the relayer's committee-attestation identity resolves), just with balance "0".
        wallet_addr = args.initial_supply_wallet.lower()
        if wallet_addr in seen_addrs:
            # The wallet coincides with an already-added account (e.g. a validator or dev
            # account) -- top up that SAME entry in place rather than appending a duplicate
            # address (a duplicate would silently double the chain's real total supply the
            # moment two "alloc" entries share one address, since nothing in this script
            # deduplicates by summing).
            for entry in alloc_list:
                if entry["address"].lower() == wallet_addr:
                    entry["balance"] = args.initial_supply
                    break
        else:
            alloc_list.append({
                "address": wallet_addr,
                "balance": args.initial_supply,
                "pending_balance": "0",
                "last_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
                "device_key": "0x0000000000000000000000000000000000000000000000000000000000000000",
                "publicKeyBls": "0x86d5de6f7c9c13cc0d959a553cc0e4853ba5faae45a28da9bddc8ef8e104eb5d3dece8dfaa24f11b4243ec27537e3184"
            })
        actual_total = sum(int(a["balance"]) for a in alloc_list)
        if actual_total != initial_supply_int:
            # Must be unreachable given the code above, but this is exactly the invariant the
            # whole feature exists to guarantee -- verify it directly rather than trust the logic
            # that's supposed to produce it, same "assert the invariant, don't just hope for it"
            # pattern GlobalSupplyLedger.VerifyInvariant() uses on the Go side.
            print(red(f"❌ INTERNAL: tổng alloc thực tế ({actual_total}) khác initial_supply đã xác minh ({initial_supply_int}) -- dừng lại, không ghi genesis sai."))
            sys.exit(1)
        print(green(f"  ✅ Tổng cung genesis == {initial_supply_int} wei, đúng bằng đúng 1 ví {wallet_addr} -- không còn tài khoản nào khác có số dư."))

    genesis_data = {
        "config": {
            "chainId": args.chain_id,
            "epoch": 0,
            "epoch_timestamp_ms": 1781315442000,
            "attestation_interval": 10,
            "epoch_duration_seconds": 345600
        },
        "validators": validators_entries,
        "alloc": alloc_list,
        "total_stake": total_stake,
        "quorum_threshold": quorum_threshold,
        "validity_threshold": validity_threshold
    }

    genesis_path = out_dir / "genesis.json"
    with open(genesis_path, "w") as f:
        json.dump(genesis_data, f, indent=2)
    print(f"  ✅ Written genesis.json to {green(str(genesis_path))}")

    # Xuất thêm file genesis-<chain_id>.json tại deploy/systemd/ để quản lý tập trung và phân biệt rõ từng chain
    systemd_genesis_path = REPO_ROOT / "deploy" / "systemd" / f"genesis-{args.chain_id}.json"
    try:
        with open(systemd_genesis_path, "w") as f:
            json.dump(genesis_data, f, indent=2)
        print(f"  📑 Exported chain-specific genesis template to {green(str(systemd_genesis_path))}")
    except Exception as e:
        print(yellow(f"  ⚠️ Warning: could not export {systemd_genesis_path}: {e}"))

    if deterministic_genesis:
        # Publishing/verifying the digest needs real Keccak256 -- deliberately NOT reimplemented
        # in Python here (see the _SELECTOR_* comment above for why); shell out to register_chains,
        # which already computes it with the exact same primitive the node itself will later use
        # (crypto.Keccak256Hash, pkg/cross_chain/ceremony.Digest). This only PRINTS the commands --
        # it does not run them, so this script never needs a signing key of its own.
        print(f"\n📐 Bước tiếp theo (mỗi validator tự chạy, sau khi genesis.json này giống hệt nhau trên mọi node):")
        print(f"   1) Người đăng ký (chỉ 1 lần, dùng đúng khoá đã trả cọc) publish digest lên Root Anchor:")
        print(f"      register_chains -action publish-genesis-digest -root-anchor {args.root_anchor_rpc.split(',')[0].strip()} -chains {args.chain_id} -genesis-file {genesis_path} -key <khoá đã dùng để đăng ký>")
        print(f"   2) MỌI validator (kể cả người vừa publish) tự xác minh lại trước khi chạy node:")
        print(f"      register_chains -action verify-genesis -root-anchor {args.root_anchor_rpc.split(',')[0].strip()} -chains {args.chain_id} -genesis-file {genesis_path}")

    # 4. Generate per-node runtime configs (config.json & node.toml)
    for node_id in range(args.validators):
        bls, eth = validator_keys_list[node_id]
        node_dir = out_dir / f"node-{node_id}"
        os.makedirs(node_dir / "logs", exist_ok=True)
        os.makedirs(node_dir / "data" / "execution" / "db", exist_ok=True)
        os.makedirs(node_dir / "data" / "consensus" / "db", exist_ok=True)

        rpc_port = args.rpc_port + node_id
        primary_port = 4200 + args.port_offset + node_id
        dns_port = 13000 + args.port_offset + node_id
        peer_rpc_port = 20200 + args.port_offset + node_id
        consensus_port = 10200 + args.port_offset + node_id
        meta_rpc_port = 11100 + args.port_offset + node_id
        metrics_port = 12100 + args.port_offset + node_id

        # Node peers
        rust_peers = [f"{args.ip}:{20200 + args.port_offset + j}" for j in range(args.validators) if j != node_id]

        if args.gateway_bls_key:
            gateway_bls_key = args.gateway_bls_key
        elif args.random_gateway_bls_key:
            gateway_bls_key = generate_fresh_bls_secret(metanode_bin)
            print(f"  🔑 node-{node_id}: generated a fresh, independent gateway_bls_key")
        else:
            gateway_bls_key = DEVNET_GATEWAY_BLS_KEY

        # Derive dedicated per-node submitter key so multiple validators never collide on Root Anchor nonce/LastHash
        node_submitter_key, node_submitter_addr = derive_devnet_submitter_account(args.chain_id, node_id)
        if args.root_anchor_submitter_key and args.validators == 1:
            node_submitter_key = args.root_anchor_submitter_key
            node_submitter_addr = address_from_privkey_hex(node_submitter_key)

        free_fee_list = ["55798165960a62cED34a0d86e36B1758D1303907"]
        if deterministic_genesis:
            # Infra identities (committee-attestation-worker submitter, cross-chain relayer) still
            # need to submit real transactions even though this chain's genesis deliberately gives
            # them zero balance -- free_fee_addresses (gas exemption), not a funded balance, is the
            # correct mechanism here: it keeps the bridge's own plumbing running without
            # reintroducing free circulating money that would violate this run's whole point
            # (total supply == exactly what Root Anchor verified above).
            _submitter_addr_bare = node_submitter_addr[2:] if node_submitter_addr.lower().startswith("0x") else node_submitter_addr
            free_fee_list.append(_submitter_addr_bare)
            free_fee_list.append("7d8bfbaba9268b59bab9ef8ff3f314d3f5747366")  # shared relayer devnet identity

        exec_config = {
            "debug": False,
            "tx_trace_enabled": False,
            "go_mem_limit_gb": 8,
            "mvm_cache_enabled": True,
            "enable_private_gateway": True,
            "gateway_bls_key": gateway_bls_key,
            "chainId": args.chain_id,
            "private_key": bls["authority_key_private"],
            "address": eth["address"].lstrip("0x").lower(),
            "log_path": str(node_dir / "logs" / "execution"),
            "epochs_to_keep": args.epochs_to_keep if args.epochs_to_keep is not None else (0 if args.is_rpc else 5),
            "backup_path": str(node_dir / "data" / "execution" / "backup"),
            "last_block_save_path": "/last_block.dat",
            "transaction_block_number_last_hash_path": "/transaction_block_number_last_hash",
            "block_hash_to_number_db_root_path": "/block_hash_to_number_db_root_path",
            "free_fee_addresses": free_fee_list,
            "cross_chain": {
                "config_contract": "0x4c1c27b3147820915431554F2B2383175FAAd198",
                "reserve_chain_id": args.reserve_chain_id if args.reserve_chain_id is not None else args.chain_id,
                # Keys MUST match execution/pkg/config/config.go's CrossChainConfig json tags
                # exactly (snake_case) — encoding/json silently leaves a field at its zero value
                # on a case/spelling mismatch instead of erroring, so a wrong key here doesn't
                # fail loudly: it just silently disables the ChainRegistry refresh worker /
                # CommitteeAttestationWorker on every node this script generates. Verified against
                # config.go directly, not assumed.
                "root_anchor_rpc_urls": args.root_anchor_rpc.split(",") if args.root_anchor_rpc else [],
                "root_anchor_submitter_private_key_hex": node_submitter_key,
                "root_anchor_poll_interval_seconds": 1,
                "min_native_stake_to_register_wei": "1000000000000000000",
                "root_anchor_circuit_breaker_max_failures": 5,
                "root_anchor_circuit_breaker_timeout_seconds": 10,
                # DEVNET/TESTING ONLY (see config.go's own doc comment on this field) -- shortens
                # GovernanceEngine's mandatory 72h ProposalAllocateSupply/etc. timelock to 10s so
                # the full propose->vote->timelock->execute governance path (required to grant any
                # chain an initial cross-chain spending allocation -- see
                # TestGateway_ProposalAllocateSupply_UnblocksAttestCommit) is actually exercisable
                # on a local devnet instead of requiring a literal 72-hour wait. NEVER set this on
                # a real deployment -- gen_single_chain.py is devnet tooling only.
                "devnet_governance_timelock_seconds_override": 10
            },
            "meta_node_rpc_address": f"{args.ip}:{meta_rpc_port}",
            "connection_address": f"0.0.0.0:{primary_port}",
            "dns_server_address": f"{args.ip}:{dns_port}",
            "version": "0.0.1.0",
            "rpc_port": f":{rpc_port}",
            "peer_rpc_port": peer_rpc_port,
            "db_type": 2,
            "genesis_file_path": str(genesis_path),
            "rust_config_path": str(node_dir / "node.toml"),
            "snapshot_enabled": False,
            "is_rpc_node": args.is_rpc,
            "state_backend": "nomt",
            "Databases": {
                "RootPath": str(node_dir / "data" / "execution" / "db"),
                "DBEngine": "sharded",
                "Version": "0.0.1.0",
                "BLSPrivateKey": bls["authority_key_private"],
                "SnapshotPath": str(node_dir / "data" / "execution" / "snapshots"),
                "MaxPartSizeMB": 100,
                "ArchiveBaseName": "snapshot_archive"
            },
            "nodes": {
                "network_sync_enabled": (args.validators > 1),
                "dynamic_discovery": True
            },
            "log": {
                "level": "debug" if args.debug else "info",
                "format": "text",
                "console_output": True,
                "file_output": True
            }
        }

        with open(node_dir / "config.json", "w") as f:
            json.dump(exec_config, f, indent=2)

        # Build node.toml for Rust consensus
        peers_toml = ", ".join([f'"{p}"' for p in rust_peers])
        toml_content = f"""# Rust Consensus Configuration for Single Chain Node {node_id}
node_id = {node_id}
network_address = "127.0.0.1:{consensus_port}"
protocol_key_path = "{node_dir}/keys/protocol_key.json"
network_key_path = "{node_dir}/keys/network_key.json"
storage_path = "{node_dir}/data/consensus/db"

enable_metrics = true
metrics_port = {metrics_port}
peer_rpc_port = {peer_rpc_port}
peer_rpc_addresses = [{peers_toml}]
executor_read_enabled = true
executor_commit_enabled = true
time_based_epoch_change = true
"""
        with open(node_dir / "node.toml", "w") as f:
            f.write(toml_content)

    # 5. Generate start_single_chain.sh & stop_single_chain.sh
    start_sh = out_dir / "start_single_chain.sh"
    stop_sh  = out_dir / "stop_single_chain.sh"

    local_sc = SCRIPT_DIR / "bin" / "simple_chain"
    simple_chain_bin = local_sc if local_sc.exists() else (REPO_ROOT / "execution" / "cmd" / "simple_chain" / "simple_chain")
    
    start_script_content = f"""#!/usr/bin/env bash
set -e
DIR="$(cd "$(dirname "${{BASH_SOURCE[0]}}")" && pwd)"
echo "🚀 Starting Metanode Single Chain (Chain ID {args.chain_id})..."

METANODE_BIN="{metanode_bin}"
SIMPLE_CHAIN_BIN="{simple_chain_bin}"

if [ ! -f "$SIMPLE_CHAIN_BIN" ]; then
    echo "🔨 Building simple_chain Go binary..."
    (cd "{REPO_ROOT}/execution/cmd/simple_chain" && go build -o simple_chain .)
fi

"""
    for node_id in range(args.validators):
        node_flags = f" --debug --pprof-addr=127.0.0.1:{6060 + node_id}" if args.debug else ""
        start_script_content += f"""
echo "  → Starting Node-{node_id} (RPC: http://{args.ip}:{args.rpc_port + node_id})..."
mkdir -p "$DIR/node-{node_id}/logs"
(cd "$DIR/node-{node_id}" && "$SIMPLE_CHAIN_BIN" --config "$DIR/node-{node_id}/config.json"{node_flags} > logs/node-{node_id}.log 2>&1 & echo $! > node-{node_id}.pid)
"""

    start_script_content += f"""
echo "✅ Single Chain {args.chain_id} started successfully!"
echo "   Node-0 RPC URL: http://{args.ip}:{args.rpc_port}"
echo "   Chain ID: {args.chain_id}"
echo "   Check logs in $DIR/node-0/logs/node-0.log"
"""

    stop_script_content = f"""#!/usr/bin/env bash
DIR="$(cd "$(dirname "${{BASH_SOURCE[0]}}")" && pwd)"
echo "🛑 Stopping Metanode Single Chain (Chain ID {args.chain_id})..."

for pid_file in "$DIR"/node-*/node-*.pid "$DIR"/node-*/consensus-*.pid; do
    if [ -f "$pid_file" ]; then
        PID=$(cat "$pid_file")
        if kill -0 "$PID" 2>/dev/null; then
            echo "  → Stopping node process PID $PID..."
            kill -15 "$PID" 2>/dev/null || true
            for i in $(seq 1 10); do
                if ! kill -0 "$PID" 2>/dev/null; then
                    break
                fi
                sleep 0.5
            done
            if kill -0 "$PID" 2>/dev/null; then
                kill -9 "$PID" 2>/dev/null || true
            fi
        fi
        rm -f "$pid_file"
    fi
done

# Fallback: Kill any simple_chain process running with config inside this directory
pkill -f "$DIR" 2>/dev/null || true
"""
    for node_id in range(args.validators):
        stop_script_content += f"""fuser -k {args.rpc_port + node_id}/tcp 2>/dev/null || true\n"""

    stop_script_content += f"""
echo "✅ Single Chain {args.chain_id} stopped."
"""

    with open(start_sh, "w") as f:
        f.write(start_script_content)
    os.chmod(start_sh, 0o755)

    with open(stop_sh, "w") as f:
        f.write(stop_script_content)
    os.chmod(stop_sh, 0o755)

    # 6. Print summary
    print(bold(green("\n🎉 Single Chain Environment Initialized Successfully!")))
    print(f"  • Genesis file:     {cyan(str(genesis_path))}")
    print(f"  • Dev Accounts:     {cyan(str(out_dir / 'dev_accounts.json'))}")
    print(f"  • Start Script:     {green(str(start_sh))}")
    print(f"  • Stop Script:      {red(str(stop_sh))}")
    print(f"\n💡 To start the single chain, run:")
    print(cyan(f"   bash {start_sh}\n"))

if __name__ == "__main__":
    main()
