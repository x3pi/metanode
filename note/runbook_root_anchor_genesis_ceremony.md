# Runbook: Root Anchor Genesis Ceremony

Milestone D of the cross-chain Root Anchor wiring plan (see
`note/cross_chain_root_anchor_architecture.md`, section 1.3 #5 / 5.2.1, and
task P1.1/P1.2 in section 14). This is the procedure for bootstrapping a
**new** Root Anchor network from **≥ 4 independent founding private chains**,
where no single operator has (or needs) SSH/sudo access to any other
operator's machine and no private key ever leaves the machine it was
generated on.

This replaces, for this specific scenario, the older `deploy/ansible` +
`gen_validator_entry.py` auto-merge workflow — that workflow still works
correctly for its original purpose (one operator deploying/extending **their
own** private chain, sharing one `genesis.json`), but it assumes a single
trusted coordinator who generates and holds every validator's keys, which is
the wrong trust model for founding chains that are independent
organizations. See `note/private_chain_guide.md` for that path.

## Why this exists (background)

`execution/pkg/cross_chain/root_anchor.go` (`NewRootAnchorCommittee`) and
`execution/pkg/cross_chain/pop.go` (Proof-of-Possession) already implement and
test the actual committee-aggregation rules — ≥ 4 founding chains, PoP for
every validator, a hard cap on any one chain's stake share (default 33%).
Before this tooling existed there was no path from "an operator generates
keys on their own machine" to a real, bootable `genesis.json` that exercises
those rules: `deploy/systemd/gen_validator_entry.py`'s merge step required a
shared `genesis.json` one coordinator edits directly (and leaked
`eth_private_key` into the very file meant to be shared), had `chainId`
hardcoded to `991`, and its docstring referenced an `update_genesis.py` that
was never written.

## The two tools

- **`execution/cmd/tool/founding_entry`** — run by **each founding-chain
  operator**, on the machine that holds their validator's key material.
  Reads `metanode keytool generate validator --out-dir <dir>` output, derives
  the min-pk (blst, G1) execution-layer BLS key from the same secret scalar,
  signs a Proof-of-Possession, and writes a `founding_entry.json` containing
  **only public data** — no private key ever appears in it (enforced by a
  regression test,
  `TestBuildFoundingEntry_NoPrivateKeyLeakage` in
  `execution/pkg/cross_chain/ceremony`). This file is what gets published /
  emailed / posted to the coordinator.

- **`execution/cmd/tool/assemble_root_anchor`** — run by **one ceremony
  coordinator**, who collects the `founding_entry.json` files from all
  participating operators. Two subcommands:
  - `assemble` — validates every submission (schema, PoP, stake cap,
    duplicate/collision checks not covered by `NewRootAnchorCommittee` alone —
    see `execution/pkg/cross_chain/ceremony/assemble.go`), then
    deterministically writes `genesis.json`, `root_anchor_committee.json`, and
    `genesis_digest.txt`.
  - `verify` — recomputes the digest of an on-disk `genesis.json` and
    compares it to an expected value. **Every operator must run this before
    starting their node.** This is the *only* defense against a divergent
    genesis file: the consensus layer performs no such check at epoch 0 (see
    "Why the digest check matters" below).

Both tools share one schema/validation package
(`execution/pkg/cross_chain/ceremony`) so the two can never drift apart —
see its tests for the exact set of enforced invariants (≥ 4 chains, PoP,
33% stake cap, no duplicate address/hostname/key/port across operators,
deterministic output regardless of submission order).

## Step-by-step

### 1. Each founding-chain operator generates keys locally

```
metanode keytool generate validator --out-dir ./my_keys
```

This never touches the network and never needs to be run by, or shared with,
anyone else. Back the directory up immediately — losing `authority_key.json`
means losing the validator's committee identity permanently.

### 2. Each operator builds their founding_entry.json

```
founding_entry \
  --keys-dir    ./my_keys \
  --chain-id    <YOUR existing private chain's chain ID> \
  --chain-name  "My Chain" \
  --hostname    node-0 \
  --ip          <public IP or hostname this validator is reachable at> \
  --p2p-port 9100 --primary-port 6200 --worker-port 4012 \
  --stake       1000000000000000000000 \
  --out         founding_entry.json
```

`--chain-id` here is the operator's **own existing private chain's** ID (the
one they're contributing a committee seat from) — **not** the new Root
Anchor's chain ID, which the coordinator picks in step 4. `founding_entry`
fails closed: if the derived key/PoP doesn't self-validate, it refuses to
write the file rather than emit something broken.

Send `founding_entry.json` to the coordinator over any channel (email, chat,
a shared drive). **It contains no private key material and is safe to
publish** — this is exactly what
`TestBuildFoundingEntry_NoPrivateKeyLeakage` guards.

If you're also using `deploy/systemd/gen_validator_entry.py` to provision this
node's runtime configs, it can run this step for you in the same invocation
via `--founding-entry <path> --founding-chain-name "My Chain"` — it shells
out to `founding_entry` right after key generation. Either order of the two
tools works: `founding_entry` tolerates both the raw `metanode keytool`
output and `gen_validator_entry.py`'s in-place rewrite of
`protocol_key.json`/`network_key.json` into the raw-base64 form the Rust node
needs at runtime.

### 3. Coordinator collects ≥ 4 founding_entry.json files

Put them all in one directory, e.g. `./entries/alpha.json`,
`./entries/beta.json`, `./entries/gamma.json`, `./entries/delta.json`.
Fewer than 4 distinct founding chains is a hard failure
(`cross_chain.ErrInsufficientFoundingChains`) — this is not a soft warning,
it is the security model in section 1.3 #5: below 4 chains, the Reserve
committee degenerates back into a small trusted group, reproducing the
weakest-link problem the whole design exists to avoid.

### 4. Coordinator assembles genesis.json

```
assemble_root_anchor assemble \
  --entries               ./entries \
  --chain-id               9099 \
  --epoch-timestamp-ms     <unix ms, at or shortly after when nodes will start> \
  --epoch-duration-seconds 300 \
  --attestation-interval   10 \
  --out-dir                ./out
```

`--chain-id` is the **new** Root Anchor network's own chain ID — pick one
that does not collide with any founding chain's own ID (`assemble` rejects
it if it does). This writes:

- `out/genesis.json` — the file every validator's node actually loads.
- `out/root_anchor_committee.json` — the aggregated committee, for later use
  seeding `ChainRegistry` (see the wiring plan's Milestone A/C).
- `out/genesis_digest.txt` — the value every operator must verify against.

Assembly is **deterministic**: the same set of `founding_entry.json` files
produces byte-identical output (and therefore the same digest) no matter what
order they were collected in — see `TestAssemble_Deterministic`.

### 5. Coordinator publishes the digest, out of band from genesis.json itself

Post `genesis_digest.txt`'s value somewhere every operator can independently
see it — a group chat, a signed announcement, whatever channel isn't the same
one `genesis.json` itself travels over. The point of "out of band" is that an
attacker able to tamper with the genesis file in transit would also need to
separately compromise this second channel to hide the tampering.

### 6. Every operator verifies before starting their node

```
assemble_root_anchor verify \
  --genesis        genesis.json \
  --expect-digest  0x<digest from step 5>
```

`OK` means start the node. A mismatch prints both digests and refuses with a
non-zero exit — **do not start a node from a file that fails this check.**

### 7. Start the nodes

Genesis ceremony tooling stops here — from this point on, standing up the
network is the same as any other Metanode network deployment
(`note/private_chain_guide.md`, `deploy/ansible`, or `deploy/systemd`), just
pointed at the assembled `genesis.json` and a fresh chain ID instead of the
usual single-operator flow.

## Why the digest check matters (and its limit)

There is no genesis-hash or chain-identity commitment anywhere in the
consensus protocol itself: the block header carries no chain ID, and the one
committee-hash cross-check that does exist
(`consensus/metanode/src/node/committee_source.rs`) only covers
`protocol_key` + `stake` + `hostname` — **not** `authority_key`,
`network_key`, or network address — and is explicitly skipped at epoch 0
(`setup_storage/mod.rs`: *"Genesis epoch — committee from genesis.json, no
peer verification needed"*). Two operators booting with silently divergent
genesis files (different `alloc`, a typo'd `chainId`, a stale `authority_key`)
get no protocol-level warning; divergent state only surfaces once block 1+
state roots disagree, if it surfaces at all.

`assemble_root_anchor verify` is the entire defense against this at genesis
time. It is a process control, not a protocol one — treat step 6 as
mandatory, not optional, for every single node.

## Rehearsing before the real thing

`deploy/systemd/rehearse_root_anchor_ceremony.sh` runs this entire procedure
locally, end to end, with 4 fully independent key directories standing in for
4 independent operators (same tools, same commands, same digest check) —
including, by default, actually starting all 4 nodes against the assembled
genesis and confirming the network reaches BFT quorum and produces blocks.
Run it before a real ceremony to catch tooling/environment problems (missing
`metanode` binary, Go toolchain, etc.) before they cost real operators time.

## Known gaps (tracked, not silent)

- `consensus/metanode/src/types/{pop,root_anchor}.rs` (Rust) implements the
  same committee rules using fastcrypto **min-sig**, a different BLS scheme
  from the **min-pk** (blst) key this ceremony's `ChainRegistry` entries use.
  The two are not interoperable as-is. This is intentionally deferred to
  Milestone C (wiring `CommitteeUpdate` into epoch transition), which is the
  first point where Rust actually needs to consume `ChainRegistry` data.
- This ceremony produces a `genesis.json` and a `root_anchor_committee.json`.
  Actually registering that committee into a live `ChainRegistry` (so
  `GatewayHandler`, wired in Milestone A, stops seeding it by hand in tests)
  is Milestone B/C work, not part of genesis bootstrap itself.
