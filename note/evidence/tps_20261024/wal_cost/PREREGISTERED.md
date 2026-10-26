# WAL cost decomposition (pre-registered before measurement)
- Base commit: b891b9e90a7f1543e6dbbbfd8f1dc70bfc7ef71b + uncommitted Layer-1 patch (+ METANODE_PAYLOAD_WAL_FSYNC / _DEBUG_DISCARD knobs)
- sha256 simple_chain: 586a7793545e9bbed1af89ce5231803441c3d2fd88f47b6dcabfb0640256cd1b
- sha256 tool: 8b129d05ca04d867a94de2d971ef1214ee038236c364039c97c0ed38f98a1154
- sha256 parent_chain: 35152a71ec3364cde8ca917c2c805688f47126199e4f7e44991ab2e2b9405675
- Modes (same binary): off=METANODE_PAYLOAD_WAL=0; discard=WAL on, writer drops items (enqueue cost only); nofsync=WAL on, METANODE_PAYLOAD_WAL_FSYNC=0; full=default.
- Method: run_tps_counted.py sustained --max-txs 840000, fresh 4-validator cluster per run, interleaved off,discard,nofsync,full x3.
- Metrics: tps_end_to_end and tps_during_injection. Hypothesis: cost is dominated by fdatasync/disk contention (nofsync closes most of the gap). Report whatever comes out.
