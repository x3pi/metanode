# MetaNode Disaster Recovery (DR) Runbook

This document outlines the standard operating procedures (SOPs) for disaster recovery scenarios in the MetaNode (mtn-simple-2025) production environment.

## 1. Node Crash & Auto-Recovery
**Scenario:** A MetaNode instance crashes due to application errors, OOM, or hardware failure.

**Action:**
1. Wait for `systemd` or Kubernetes to automatically restart the node.
2. The node will perform an auto-recovery:
   - Read the last committed block from PebbleDB.
   - Replay WAL (Write-Ahead Logs) if necessary.
   - Sync missing blocks from the Master Node (if Sub) or via P2P (if Master).
3. **Verification:**
   - Check `GET /health` to ensure `"synced": true` and `"nomt": "ready"`.
   - Monitor `master_txs_processed_total` in Grafana to ensure the node is processing blocks again.

## 2. State Corruption (Trie / Database Corruption)
**Scenario:** The node fails to start due to `leveldb/pebble` corruption or state root mismatch.

**Action (Restore from Snapshot):**
1. Stop the node: `systemctl stop metanode` or `kubectl scale sts metanode --replicas=0`.
2. Locate the latest healthy snapshot in `<DATA_DIR>/snapshots/snap_epoch_<N>_block_<M>`.
3. Clear the current corrupted data directory: `rm -rf <DATA_DIR>/data/{xapian_node,pebble_dbs,nomt_dbs}`
4. Restore from the snapshot:
   - For environments using Btrfs/XFS (Reflink): The restore script will instantaneously clone the snapshot to the active data directory.
   - Command: `bash scripts/restore_snapshot.sh <SNAPSHOT_DIR> <DATA_DIR>`
5. Restart the node. The node will sync any blocks created since the snapshot's epoch.

## 3. Network Partition (Split-Brain Consensus)
**Scenario:** The network is partitioned, and the Go Execution Layer stops receiving blocks from the Rust Consensus Layer.

**Action:**
1. Verify the connection between Go and Rust via the IPC Socket:
   - Check the socket file: `ls -la /tmp/rust-go.sock` (or the configured path).
2. Check Grafana `BlockStalled` alert. If `time() - master_current_block_timestamp > 30`, consensus is halted.
3. If Rust is halted: Restart the Rust consensus process (`systemctl restart mtn-consensus`). The Go node will wait indefinitely until Rust resumes and sends the next valid `global_exec_index`.
4. The system favors consistency over availability; it will not execute transactions without Rust's ordering.

## 4. Emergency Epoch Rollback
**Scenario:** A critical consensus bug or malicious committee act requires rolling back to the previous epoch.

**Action:**
1. **DANGER:** This requires coordination across the entire validator committee.
2. Stop all validator nodes (Go and Rust processes).
3. On every node, restore the state using the snapshot that corresponds to the boundary block of the target prior epoch.
   - Example: To rollback to Epoch 10, restore `snap_epoch_10_block_...`.
4. Delete all consensus logs in the Rust layer newer than the target epoch.
5. Restart all nodes simultaneously. The chain will resume from the rolled-back state.

## 5. Storage Exhaustion
**Scenario:** Disk space reaches 100% despite the State Pruning System.

**Action:**
1. Temporarily increase disk capacity (EBS volume expansion, etc.) and resize the filesystem.
2. Verify that the `PruningManager` is running properly:
   - Check logs for `[PRUNING]` tags.
   - If pruning is failing, ensure `epochs_to_keep` and `prune_interval_blocks` in `<DATA_DIR>/config.json` are appropriately aggressive (e.g., set `epochs_to_keep = 3`).
3. Manually trigger a cleanup of unused unreferenced files: `go run tool/db_cleanup/main.go --data-dir <DATA_DIR>`.

## Contacts & Escalation
- L1 Support: Monitoring Team (Slack: #alert-metanode)
- L2 Support: Infrastructure Leads
- L3 Support: Core Blockchain Engineers
