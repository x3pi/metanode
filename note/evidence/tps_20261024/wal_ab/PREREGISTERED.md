# Payload WAL A/B pre-registration (written before any measurement)
- Base commit: 0c6b27ca841a41cb720e58557027902a6bef0397 + uncommitted Layer-1 patch (payload_store.rs, transaction.rs, setup_consensus/mod.rs)
- Single binary, A/B by env: A = METANODE_PAYLOAD_WAL=0, B = default (WAL on)
- sha256 simple_chain: 5aa343ab2488d5e7ab507b86e7b9102fd794ada9f5a5fea2b49a8715a1a9d76b
- sha256 tool: 8b129d05ca04d867a94de2d971ef1214ee038236c364039c97c0ed38f98a1154 (rebuilt from execution/cmd/tool/secp_tps_blast; the old p06 tool exited 2)
- sha256 parent_chain: 35152a71ec3364cde8ca917c2c805688f47126199e4f7e44991ab2e2b9405675
- Note: first attempt failed at cluster start (parent_chain missing from bins dir); no measurement taken; moved to failed_first_attempt/
- Method: run_tps_counted.py sustained, --max-txs 840000, fresh 4-validator cluster per run, interleaved A,B x3.
- Metric: tps_end_to_end. Hypothesis: B within noise of A. Report whatever comes out.
