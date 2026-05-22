# Historical Data Storage & Querying Architecture

This document outlines the architecture for storing and querying historical blockchain data within the Metanode Execution Engine.

## Archive Node Mode

Metanode supports an "Archive Node" configuration designed to retain full historical state, blocks, and logs without pruning.

To run a node in Archive Mode, configure the `epochs_to_keep` parameter in `config.json` to `0`:

```json
{
  "epochs_to_keep": 0,
  "ArchiveBaseName": "snapshot_archive"
}
```

**How it works:**
The `log_cleaner.go` service monitors node disk usage and prunes data older than `epochs_to_keep`. Setting this to `0` acts as a flag that outright disables the cleanup routine. The node will persist all blocks indefinitely.

## Storage Mediums

Metanode employs a multi-tiered storage approach for its database persistence:

1. **Trie Database (`trie_database/`):**
   Stores the MPT (Merkle Patricia Trie) nodes for Smart Contracts and state variables.
   
2. **Account State & Stake State DBs:**
   Stores the raw account balances, nonces, and validator stakes.
   
3. **State Changelog (`StateChangelogDB`):**
   Logs block-by-block differences to enable rapid state reversals in case of forks.
   
4. **Snapshot Storage:**
   Used during sync or recovery. Archive nodes will naturally generate much larger snapshots since no historical data is pruned. When an archive is created or downloaded, it relies on the split-archive capability defined in `file_transfer.go` to handle multi-gigabyte files.

## Historical Query Flow (RPC Gateway)

When querying historical data (e.g., historical balances, receipts, or blocks by hash), the system processes requests primarily through the RPC API Gateway (`execution/cmd/rpc/`).

### Direct State Queries
To eliminate dependency on stale internal caches and ensure determinism, historical state queries directly probe the Go `AccountStateDB`:

1. **Request Intake:** The user submits a JSON-RPC request (e.g., `eth_getTransactionCount` or `mtn_getAccountState`) to the HTTP Proxy Handler.
2. **TCP Interception:** The proxy (`http_handler.go`) intercepts the request and sends a lightweight TCP command (`GetAccountState`) via the internal connection pool to the main Meta-Node backend.
3. **Database Access:** The Execution Engine accesses the active `AccountStateDB`, retrieves the absolute latest state (including nonces and balances), and serializes it using Protobuf (`pb.AccountState`).
4. **Response Delivery:** The data is sent back across the TCP stream to the proxy, which reformats it as a standard JSON-RPC response for the client.

### Block & Transaction Queries
For methods like `eth_getBlockByHash` or `eth_getTransactionReceipt`:
- The request is typically routed from the proxy to the `rpc-server` where `rpc_block.go` and `rpc_transaction.go` query the underlying `ChainState`.
- These modules interact with `BlockChainDB` to retrieve the historical block headers and bodies from persistent storage.

---
> [!NOTE] 
> **Performance Considerations**
> Archive nodes require substantially higher disk capacity. Ensure adequate NVMe/SSD space if `epochs_to_keep` is set to `0`, as the `StateChangelogDB` will grow linearly with every block.
