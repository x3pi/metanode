Fault injection: val0's NOMT dir on a 268 MB tmpfs (265 MB used at start), 4 validators, `secp_tps_blast -count 400000` (50000 txs were actually sent).
run3 = binary before the fix (bins_dev2): val0 keeps running after ENOSPC, block hash differs from block 4 on.
run4 = binary with fail-stop in CommitAsync (bins_fix): val0 exits at the first failed commit, val1..3 agree.
Reproduce: `sudo mount -t tmpfs -o size=268m,uid=$(id -u) tmpfs /mnt/nomt_small; python3 enospc_inject.py <bins> /mnt/nomt_small <outdir>`.
