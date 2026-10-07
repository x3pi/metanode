# P0-6 Multi-Validator Real Production Evidence Report

- **Execution Timestamp**: `2026-10-07T01:01:02Z`
- **Cluster Topology**: 4 Isolated Validators (Mysticeti Consensus, $N=4, f=1, 2f+1=3$)
- **Ports**: Dedicated isolated range `31xxx` (val0: 31646, val1: 31647, val2: 31648, val3: 31649)
- **Chain ID**: `991`
- **Safety Invariant**: 100%% Zero-Fork, Deterministic State Transition, Data-Driven Deadlock-Free

### Detailed Test Execution Matrix

| Step | Phase / Description | Status | Duration | Observation / Parity Audit Details |
| :--- | :--- | :--- | :--- | :--- |
| `P1.1` | register and fund user1 (0x5568Ed68) | ✅ PASS | 3.327s | user1 confirmed & funded with 5 ETH (tx 0x86152d60) |
| `P1.2` | register and fund user2 (0x43127d69) | ✅ PASS | 3.022s | user2 confirmed & funded with 5 ETH (tx 0x87389219) |
| `P1.3` | register and fund user3 (0x915BC5f1) | ✅ PASS | 2.726s | user3 confirmed & funded with 5 ETH (tx 0x104b0f00) |
| `P2.1` | Native ETH Transfer via RPC (Legacy EIP-155) | ✅ PASS | 1.211s | tx 0x01b9ef09eb9a08839511979359ba9474df8cad95bcf0848f9243e811c92ca062 confirmed (status 1) |
| `P2.2` | Native ETH Transfer via RPC (DynamicFee EIP-1559) | ✅ PASS | 1.213s | EIP-1559 tx 0x7103a4aa3bfee48af97b1d312159514699cac191210d375cc57672b7dca6404b confirmed via val1 RPC |
| `P2.3` | Native ETH Transfer via Raw TCP (SendRawTransaction) | ✅ PASS | 1.21s | raw TCP tx 0xa5c7a04b6293598c48e62110267cd1a3902f565c5ec13157e918e81100e1bc0a confirmed via val2 TCP |
| `P2.4` | Batch of 3 Native ETH Transfers via Raw TCP (SendRawTransactions) | ✅ PASS | 1.514s | 3 batch txs confirmed via val3 TCP RLP batch ingress |
| `P2.5` | Smart Contract Deployment via RPC (TestCounter) | ✅ PASS | 909ms | contract deployed at 0x417B5dF6a387f3fA3178A890Ae8Ac450e6007f4C (tx: 0x7c59efb2) |
| `P2.6` | Smart Contract Invocation via RPC (increment()) | ✅ PASS | 1.211s | increment() called successfully (tx: 0x7df8d61951a3fec4afb44bb30a076ab02d1c44fbdc86f43c59837b350753523f, st=1) |
| `P2.7` | Smart Contract Invocation via Raw TCP (increment()) | ✅ PASS | 1.51s | increment() via TCP confirmed (tx: 0xc6c27c6e9db71252779099cecb451758bb269bbe12d3fdd2c78f7bdfce8159cd, st=1) |
| `P2.8` | Error Tx 1: Stale Nonce Replay is rejected | ✅ PASS | 2ms | properly rejected: nonce too low |
| `P2.9` | Error Tx 2: Overdraft transaction fails execution (receipt status 0) | ✅ PASS | 1.21s | overdraft transaction executed and failed with receipt status 0 (insufficient funds) |
| `P2.9b` | Error Tx 2b: Forged Signature on REGISTERED account is rejected | ✅ PASS | 3ms | properly rejected: RPC raw tx caught invalid signature (malleable signature: s exceeds curve order / 2 (EIP-2)) & proto binding caught impersonation (transaction fields do not match raw envelope: FromAddress mismatch) |
| `P2.10` | Error Tx 3: Wrong Chain ID replay is rejected | ✅ PASS | 1ms | properly rejected: invalid chain id |
| `P2.11` | Sustained Mixed Ingress: 20 transactions across 4 validators (TCP batch, RPC calls) | ✅ PASS | 1.862s | 20 sustained mixed transactions confirmed successfully across all 4 validators |
| `P3.1` | State Root and Block Hash Parity after Mixed Load | ✅ PASS | 133ms | All blocks #1..#22 verified 100% identical across all 4 nodes! Latest StateRoot: 0x0508e7e58d0ade2345ff77f31c2ceb5c9df98108fbd17db9d098022107ad5aa8 |
| `P4.1` | Byzantine Validator introduces mutated tx (proto Amount != envelope Amount) directly into consensus via PeerRPC | ✅ PASS | 1.088s | Byzantine mutated tx submitted to Rust consensus; honest tx 0xb251f673 committed in block |
| `P4.2` | Verify all 4 validators dropped the Byzantine mutated tx at BlockSTM (receipt=null, balance untouched) | ✅ PASS | 5ms | Byzantine tx strictly dropped by FilterInvalidSignatures across all 4 nodes (receipt=null, balance safe: 4999624676998500000 wei) |
| `P4.3` | Zero State Drift: All 4 nodes remain in 100% agreement after Byzantine proposal | ✅ PASS | 117ms | Zero State Drift verified across all 4 nodes at height #24 (StateRoot: 0x477401f1d730eeb8534b97c976e59eedf1d0c1beb3e024fa7aa2ec8168ef7f27) |
| `P5.1` | Kill -9 validator val3 mid-traffic | ✅ PASS | 1.084s | val3 terminated with SIGKILL |
| `P5.2` | Remaining 3 validators (>= 2f+1=3) continue making blocks and confirming txs | ✅ PASS | 13.016s | Consensus progressed #24 -> #36 with 3 active nodes; all 12 txs confirmed with st=1 |
| `P5.3` | Restart val3 and verify catchup to latest block | ✅ PASS | 28.232s | val3 successfully restarted and caught up to latest block #36 |
| `P5.4` | Final 100% Block-by-Block Audit across all 4 nodes after Chaos recovery | ✅ PASS | 178ms | ZERO-FORK VERIFIED: All blocks #1..#36 have 100% identical hash and stateRoot (0x558ddbf2785d4b5cb2afacc963c955b39c45c98e39332b690647f89644789829) across all 4 nodes! |

### Verification Summary

1. **Mixed Ingress (TCP + RPC)**: Sustained traffic (native transfers, EVM contract deploy & increment calls, TCP batching) confirmed cleanly across all 4 validators.
2. **Error Transaction Handling**: Stale nonces, balance overdrafts, cross-chain replays, and forged signatures on REGISTERED accounts are strictly rejected at admission/verification without node crash or state drift.
3. **Byzantine Fault Resistance (P0-9 / Real Consensus Proposal)**: Mutated protobuf fields matching a valid `RawEnvelope` submitted directly to Rust consensus via PeerRPC are strictly dropped by `FilterInvalidSignatures` at `TrueBlockSTM` across all 4 validators, maintaining 100% identical block hashes and state roots.
4. **Chaos Resilience**: With `kill -9` on val3, the remaining 3 validators (>= 2f+1=3) progressed without interruption under a 12-tx workload. Val3 caught up upon restart, achieving 100% identical block hashes and state roots across all 4 nodes.
5. **Remote CI Pipeline (`ci.sh run-now`)**: Marked as 'Chưa chạy' (cấu hình trỏ tới IP 192.168.1.232 / 231 / 230, tuân thủ nguyên tắc không can thiệp cluster từ xa khi chưa có lệnh).
