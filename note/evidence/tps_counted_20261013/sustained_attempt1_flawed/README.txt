FLAWED FIRST ATTEMPT — kept as raw evidence of two measurement defects, NOT a result to report:
1. TCP ingress of validator 4 (127.0.0.1:31203) refused the connection when the tool started; the tool used 3 of 4 endpoints,
   so that validator received 0 txs (received_by_validator shows 31649 = 0 in all 5 runs).
2. Blocks of ~20-28k txs timed out in the post-hoc full-block fetch (5 s HTTP timeout): committed_ours is a LOWER bound
   (loss_vs_sent > 0 is an artifact; committed_total_txs equals bytes_sent in every run).
Both are fixed in the next commit (runner waits for all TCP ports and fails a run that used fewer endpoints; full-block fetch
timeout 180 s and committed_ours_upper_bound is reported). Re-run results are in ../sustained_v2/ once available.
