#!/bin/bash
for i in 1 2 3 4; do
    echo "--- Node $i ---"
    # Get metrics
    metrics=$(curl -s http://127.0.0.1:800$i/metrics || echo "No metrics")
    echo "$metrics" | grep -E "threshold_clock_round|core_skipped_proposals|highest_handled_commit|leader_timeout_total|commit_sync_quorum_index|commit_sync_lead|local_commit_index|adaptive_delay_ms"
done
