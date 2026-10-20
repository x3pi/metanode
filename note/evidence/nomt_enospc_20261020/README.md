Fault injection: val0's NOMT dir on a 268 MB tmpfs (265 MB used at start), 4 validators, `secp_tps_blast -count 400000` (50000 txs were actually sent).
run3 = binary before the fix (bins_dev2): val0 keeps running after ENOSPC, block hash differs from block 4 on.
run4 = binary with fail-stop in CommitAsync (bins_fix): val0 exits at the first failed commit, val1..3 agree.
Reproduce: `sudo mount -t tmpfs -o size=268m,uid=$(id -u) tmpfs /mnt/nomt_small; python3 enospc_inject.py <bins> /mnt/nomt_small <outdir>`.
run5 (`enospc_recover.py`, binary with fail-stop): after val0 exited on ENOSPC, the tmpfs was remounted to 700 MB and `run_env.sh start_node val0` restarted it. 40 s later all four validators were at height 6 with identical block hash and stateRoot (`recover_summary.json`). Cluster was idle after the load; recovery under continuing load was not tested.
run6 was INVALID (val0 failed to bind port 31646 because a duplicate val0 started by an earlier script version was still running) and is not kept.
run7 and run8 (`enospc_ns.py <bins> account_state <out> 3`, binary rebuilt after the unit-test seam, guards against leftover processes and double starts): account_state dir on a 17 MB tmpfs.
- Phase 1 (50000 txs): val0 exits at the first failed commit, val1..3 keep going.
- tmpfs remounted to 900 MB, val0 restarted.
- run8: val0 caught up in 34 s (height 6), phase 2 (50000 new txs, all 4 up): loss 0, parity OK at #10, hash/stateRoot identical on all four, 100000 txs in the chain on val0 and val1 (`run8_val*_commits.txt`).
- run7: phase 2 was started while val0 was still catching up (height 2 of 7). Parity was still OK (#15, identical on all four) but 8301 of the 50000 phase-2 txs were never committed (loss_vs_sent 8301, `run7_p2.out`: validators report received=50000 rejected=0). Cause not investigated: availability of fire-and-forget ingress on a catching-up node, not a consistency problem.
`stake_db` and `smart_contract_storage` were not driven: `secp_tps_blast` sends plain transfers only, which never write them (the stake_db tmpfs stayed at 251 MB used); the code path is the same `CommitAsync`.
