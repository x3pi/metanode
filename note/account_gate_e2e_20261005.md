# Account gate E2E report (2026-10-05)

Base commit: 835206e5 (+ uncommitted E2E tooling committed right after). Isolated local env only (parent + 2 single-node exec clusters, chain ID 991, ports 31xxx). Driver: `execution/cmd/tool/e2e_account_gate`, env: `execution/scripts/test/gate_e2e`.

## Run A — gate on both clusters: 14 passed / 0 failed
```
== account gate E2E: exec1 gate=true exec2 gate=true (exec2 gate off => replay CONTROL mode)
✅ E1     liveness: genesis-registered funder funds users on both clusters — exec1 block 9 -> 12; 3 users funded on both clusters (same addresses, same nonce 0)
✅ E2a    unregistered user: ETH tx over RPC is refused with 'not registered', balance untouched — refused: error: account not registered on parent chain
✅ E2b    unregistered user: 0xFF tx over TCP is refused (node log shows the gate error) — refused: account not registered on parent chain}[0m [ERROR][Oct  5 10:36:24]  Transaction verification failed: &{69 account not registe
✅ E3a    mtn_getClusterIdentity matches the cluster identity — 0x944488b425d29336…
✅ E3b    user registers with the node only; status goes PENDING -> CONFIRMED automatically — status path [PENDING PENDING CONFIRMED] (the user called nothing but mtn_registerAccount)
✅ E3c    the Parent Chain registry names exec1 as the home cluster — parent /account -> 0x944488b425d29336…
✅ E4a    registered user: ETH tx over RPC succeeds with a receipt — tx 0x526ba8e72c nonce 0, recipient +10000000000000000
✅ E4b    registered user: 0xFF tx over TCP succeeds with a receipt — receipt status RETURNED
✅ E5     REPLAY on exec2 (same chain ID 991, funded, same nonce) is refused: account not registered there — refused: error: account not registered on parent chain
✅ E6     same address registers on exec2 afterwards: REJECTED, home cluster is exec1, still cannot send there — REJECTED, home = exec1, tx refused
✅ E7     race: u2 registers on BOTH clusters at once; exactly one CONFIRMED, the other REJECTED — winner=exec2 loser=exec1 (parent registry agrees); winner sends, loser refuses
✅ E8a    forged account_registered system event from a registered user is refused (unauthorized sender) — refused: error: unauthorized sender for a rollup system event
✅ E8b    forged credit events (mint) from a registered user are refused; target balance stays 0 — refused; loot balance 0
✅ E9     health: parent reports no fork; no FORK/PANIC/DIVERGE in any node log — parent last_block=7 fork_detected=false; logs clean
== RESULT: 14 passed, 0 failed, 0 skipped
```

## Run B — CONTROL (gate off on exec2): 12 passed / 0 failed
E5-control shows the same signed tx IS replayed on exec2 when the gate is off, i.e. the gate is what blocks it.
```
== account gate E2E: exec1 gate=true exec2 gate=false (exec2 gate off => replay CONTROL mode)
✅ E1     liveness: genesis-registered funder funds users on both clusters — exec1 block 0 -> 3; 3 users funded on both clusters (same addresses, same nonce 0)
✅ E2a    unregistered user: ETH tx over RPC is refused with 'not registered', balance untouched — refused: error: account not registered on parent chain
✅ E2b    unregistered user: 0xFF tx over TCP is refused (node log shows the gate error) — refused: account not registered on parent chain}[0m [ERROR][Oct  5 10:38:08]  Transaction verification failed: &{69 account not registe
✅ E3a    mtn_getClusterIdentity matches the cluster identity — 0x944488b425d29336…
✅ E3b    user registers with the node only; status goes PENDING -> CONFIRMED automatically — status path [PENDING PENDING CONFIRMED] (the user called nothing but mtn_registerAccount)
✅ E3c    the Parent Chain registry names exec1 as the home cluster — parent /account -> 0x944488b425d29336…
✅ E4a    registered user: ETH tx over RPC succeeds with a receipt — tx 0xc131702033 nonce 0, recipient +10000000000000000
✅ E4b    registered user: 0xFF tx over TCP succeeds with a receipt — receipt status RETURNED
✅ E5-control CONTROL (gate off on exec2): the very same signed tx IS replayed and executed — replay executed: exec2 balance 1000000000000000000 -> 989955000000000000 (this is the risk the gate removes)
✅ E8a    forged account_registered system event from a registered user is refused (unauthorized sender) — refused: error: unauthorized sender for a rollup system event
✅ E8b    forged credit events (mint) from a registered user are refused; target balance stays 0 — refused; loot balance 0
✅ E9     health: parent reports no fork; no FORK/PANIC/DIVERGE in any node log — parent last_block=4 fork_detected=false; logs clean
== RESULT: 12 passed, 0 failed, 0 skipped
```

## Not covered
- Forged system events by a *validator node identity* (needs parent-verifiable proof, item 2). - Single-node clusters (no multi-validator consensus). - Durable registration queue.

## Run C — after unifying chain ID 991 (parent + exec): 14 passed / 0 failed
Parent genesis chain_id=991 (was 990), ParentChainID now a startup-configurable var. Report: same scenarios as Run A.
