# Running a Metanode Validator

> **Chain ID:** 991 | **Network:** Metanode Mainnet
> **Source:** https://github.com/x3pi/metanode

This guide covers how to set up and run a **Validator Node** or a **Sync-Only Node** on the Metanode network.

---

## Overview — Two Node Types

| | Validator Node | Sync-Only Node |
|---|---|---|
| **Role** | Participates in consensus, signs blocks | Follows chain, serves RPC — no voting |
| **Keys required** | `protocol_key`, `network_key`, `authority_key`, `private_key` | `private_key` only (no consensus keys) |
| **In genesis?** | YES — must be registered in genesis | NO — joins after genesis |
| **Consensus port** | 9000-9004 (P2P) | Not needed |
| **Use case** | Core network validators | Public RPC, explorers, wallets |

---

## Prerequisites

**Hardware (minimum):**

| | Validator | Sync-Only |
|---|---|---|
| CPU | 8 cores | 4 cores |
| RAM | 32 GB | 16 GB |
| Disk | 500 GB SSD | 300 GB SSD |
| Network | 1 Gbps, stable | 100 Mbps |

**Software:**

```bash
# Ubuntu 22.04 LTS recommended
sudo apt update && sudo apt install -y curl wget git tmux python3 build-essential
```

**Time sync (CRITICAL — consensus requires < 1s drift):**

```bash
sudo apt install -y chrony
sudo systemctl enable --now chrony
chronyc tracking   # Verify: "System time" offset < 0.1s
```

**Open ports:**

| Port | Direction | Purpose |
|------|-----------|---------|
| 4000-4400 | inbound+outbound | Go P2P state sync |
| 9000-9004 | inbound+outbound | Consensus P2P (Validator only) |
| 19200-19204 | inbound+outbound | Peer RPC (Validator only) |
| 8757 / 10747-10750 | inbound | JSON-RPC for clients |
| 8600-8605 | inbound | Snapshot HTTP server |

```bash
sudo ufw allow 22/tcp
sudo ufw allow 4000:4400/tcp
sudo ufw allow 9000:9004/tcp
sudo ufw allow 19200:19204/tcp
sudo ufw allow 8757/tcp
sudo ufw allow 10747:10750/tcp
sudo ufw allow 8600:8605/tcp
sudo ufw enable
```

---

## Step 1 — Download the Binary

```bash
# Create working directory
mkdir -p ~/metanode && cd ~/metanode

# Option A: Download pre-built binary (recommended)
# Replace VERSION with the latest release tag
VERSION="v1.0.0"
wget https://github.com/x3pi/metanode/releases/download/${VERSION}/simple_chain-linux-amd64
chmod +x simple_chain-linux-amd64
mv simple_chain-linux-amd64 simple_chain

# Option B: Build from source
git clone https://github.com/x3pi/metanode.git
cd metanode
# Follow BUILD.md for Rust + Go compilation steps

# Verify binary
./simple_chain --version
```

---

## Step 2 — Download genesis.json

`genesis.json` is the **chain's initial state**. All nodes must use the **exact same file**.

```bash
cd ~/metanode

# Download from official source
wget https://github.com/x3pi/metanode/releases/download/genesis/genesis.json

# Verify checksum (compare with official announcement)
sha256sum genesis.json
```

> **Never modify `genesis.json`** — any change produces a different chain and your node will be rejected.

---

## Step 3A — Setup Validator Node

> Skip to [Step 3B](#step-3b--setup-sync-only-node) if you only want a Sync-Only node.

### 3A.1 — Generate Your Keys

A Validator requires **3 cryptographic key pairs**:

| Key | Purpose | Where stored |
|-----|---------|-------------|
| `protocol_key` | Signs consensus messages (BLS) | `protocol_key.json` |
| `network_key` | P2P identity (Ed25519) | `network_key.json` |
| `private_key` | Ethereum account (transaction signing) | `node_config.json` |

```bash
cd ~/metanode

# Generate all keys using the keygen tool
./simple_chain keygen --output-dir ./keys

# This creates:
# keys/protocol_key.json   — BLS private key
# keys/network_key.json    — Ed25519 private key
# keys/account_key.json    — Ethereum private key + address
```

**Example output of keygen:**
```json
{
  "address": "0xccc7f510435a99aefb619ba58bde2771dfef9308",
  "private_key": "27c8f505bcae593cc0e3ac8ae9ed1b1d4493eabd11736aabda176d4d3255fa69"
}
```

> [!CAUTION]
> **Private keys (`private_key`, `BLSPrivateKey`) are secret** — never share them.
> The **public keys** (`network_key`, `protocol_key`, `authority_key` in genesis) are safe to share.

**Back up your keys immediately:**
```bash
cp -r ~/metanode/keys ~/metanode-keys-backup-$(date +%Y%m%d)
chmod 700 ~/metanode-keys-backup-*/
chmod 600 ~/metanode-keys-backup-*/*.json
```

### 3A.2 — Submit Your Public Keys to Genesis

Before the network launches, you must provide your **public keys and network info** to the chain team to be included in `genesis.json`.

Submit the following information:

```json
{
  "address": "0xYOUR_ETH_ADDRESS",
  "primary_address": "YOUR_IP:4000",
  "worker_address": "YOUR_IP:4012",
  "p2p_address": "/ip4/YOUR_IP/tcp/9000",
  "description": "Your Validator Name",
  "website": "https://your-website.com",
  "network_key": "BASE64_ENCODED_PUBLIC_KEY_FROM_keys/network_key.json",
  "protocol_key": "BASE64_ENCODED_PUBLIC_KEY_FROM_keys/protocol_key.json",
  "authority_key": "BASE64_ENCODED_BLS_PUBLIC_KEY"
}
```

> After genesis is finalized, you will receive `genesis.json` with your entry included.

### 3A.3 — Create Validator Config

Create `~/metanode/node_config.json`:

```json
{
    "chainId": 991,
    "private_key": "YOUR_HEX_PRIVATE_KEY",
    "address": "YOUR_ETH_ADDRESS_WITHOUT_0x",

    "connection_address": "0.0.0.0:4000",
    "dns_server_address": "0.0.0.0:7081",
    "rpc_port": ":8757",
    "peer_rpc_port": 19200,

    "genesis_file_path": "genesis.json",

    "Databases": {
        "RootPath": "./data/data",
        "DBEngine": "sharded",
        "BLSPrivateKey": "YOUR_BLS_PRIVATE_KEY_HEX",
        "SnapshotPath": "./data/snapshots"
    },

    "snapshot_enabled": false,
    "snapshot_server_port": 8600,
    "state_backend": "nomt",
    "nomt_commit_concurrency": 32,
    "nomt_page_cache_mb": 1024,
    "nomt_leaf_cache_mb": 1024,

    "rust_config_path": "./consensus_config.toml",

    "backup_path": "./data/back_up",
    "log_path": "./logs/go-master",
    "epochs_to_keep": 0,

    "service_type": "MASTER",
    "is_explorer": false
}
```

**Fields you must fill in:**

| Field | Description | Where to get it |
|-------|-------------|----------------|
| `private_key` | Hex private key of your Ethereum account | `keys/account_key.json` |
| `address` | Ethereum address (without `0x`) | `keys/account_key.json` |
| `Databases.BLSPrivateKey` | BLS private key hex | `keys/protocol_key.json` |
| `connection_address` | Your server's P2P address | Use `0.0.0.0:4000`, change port if needed |
| `rpc_port` | JSON-RPC port for clients | `:8757` (default) |
| `peer_rpc_port` | Peer RPC port | `19200` (default) |

### 3A.4 — Create Consensus Config

Create `~/metanode/consensus_config.toml`:

```toml
node_id = 0                          # Your validator index in genesis.validators[]
network_address = "0.0.0.0:9000"    # P2P consensus port

protocol_key_path = "./keys/protocol_key.json"
network_key_path = "./keys/network_key.json"
storage_path = "./data/consensus"

enable_metrics = true
metrics_port = 9100

# Timing (do not change unless instructed)
max_clock_drift_seconds = 5
min_round_delay_ms = 200
epoch_transition_optimization = "fast"

# Peer RPC — list all OTHER validators' peer_rpc_port addresses
peer_rpc_port = 19200
peer_rpc_addresses = [
    "VALIDATOR_1_IP:19201",
    "VALIDATOR_2_IP:19202",
    "VALIDATOR_3_IP:19203",
    "VALIDATOR_4_IP:19204"
]

# Executor integration (FFI bridge — do not change)
executor_commit_enabled = true
executor_send_socket_path = "/tmp/executor0.sock"
executor_receive_socket_path = "/tmp/rust-go-node0-master.sock"

# Sync settings
commit_sync_batch_size = 500
commit_sync_parallel_fetches = 32
epochs_to_keep = 5
```

> **`node_id`** must match your index in `genesis.json`'s `validators` array (0-based).
> **`peer_rpc_addresses`** must list all OTHER validators (not yourself).

---

## Step 3B — Setup Sync-Only Node

A Sync-Only node does **not participate in consensus** and does **not need** `protocol_key` or `network_key`.

### 3B.1 — Generate Account Key

```bash
cd ~/metanode
./simple_chain keygen --type account --output-dir ./keys
# Creates: keys/account_key.json
```

### 3B.2 — Create Sync-Only Config

Create `~/metanode/synconly_config.json`:

```json
{
    "chainId": 991,
    "private_key": "YOUR_HEX_PRIVATE_KEY",
    "address": "YOUR_ETH_ADDRESS_WITHOUT_0x",

    "connection_address": "0.0.0.0:4206",
    "dns_server_address": "0.0.0.0:7086",
    "rpc_port": ":8762",
    "peer_rpc_port": 19205,

    "genesis_file_path": "genesis.json",

    "Databases": {
        "RootPath": "./data/data",
        "DBEngine": "sharded",
        "SnapshotPath": "./data/snapshots"
    },

    "snapshot_enabled": true,
    "snapshot_server_port": 8605,
    "state_backend": "nomt",
    "nomt_commit_concurrency": 32,
    "nomt_page_cache_mb": 1024,
    "nomt_leaf_cache_mb": 1024,

    "rust_config_path": "./consensus_config_synconly.toml",

    "backup_path": "./data/back_up",
    "log_path": "./logs/go-master",
    "epochs_to_keep": 0,

    "service_type": "MASTER",
    "is_explorer": true,
    "explorer_db_path": "./data/other/explorer",
    "explorer_read_only_db_path": "./data/other/explorer-read-only"
}
```

**Key differences from Validator config:**
- `snapshot_enabled: true` — sync-only nodes should serve snapshots
- No `BLSPrivateKey` needed in `Databases`
- `is_explorer: true` — enables block explorer queries

### 3B.3 — Create Consensus Config (Sync-Only)

Sync-Only nodes use a **passive consensus config** (they observe but don't vote):

Create `~/metanode/consensus_config_synconly.toml`:

```toml
node_id = 5                            # Any ID >= number of validators in genesis
network_address = "0.0.0.0:9005"      # Any available port

# No protocol_key or network_key needed
protocol_key_path = ""
network_key_path = ""

storage_path = "./data/consensus"

enable_metrics = true
metrics_port = 9105

executor_commit_enabled = false        # Sync-only: does not dispatch to executor
executor_read_enabled = true

executor_send_socket_path = "/tmp/executor5.sock"
executor_receive_socket_path = "/tmp/rust-go-node5-master.sock"

# Connect to validators to sync
peer_rpc_port = 19205
peer_rpc_addresses = [
    "VALIDATOR_0_IP:19200",
    "VALIDATOR_1_IP:19201",
    "VALIDATOR_2_IP:19202",
    "VALIDATOR_3_IP:19203"
]

commit_sync_batch_size = 500
commit_sync_parallel_fetches = 32
epochs_to_keep = 0    # 0 = archive mode (keep all history)
```

---

## Step 4 — Start the Node

### Using systemd (recommended for production)

```bash
# Create systemd service
sudo tee /etc/systemd/system/metanode.service > /dev/null << 'EOF'
[Unit]
Description=Metanode Chain Node
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
User=YOUR_UNIX_USER
WorkingDirectory=/home/YOUR_UNIX_USER/metanode
ExecStart=/home/YOUR_UNIX_USER/metanode/simple_chain -config=node_config.json
ExecStop=/bin/kill -SIGTERM $MAINPID
TimeoutStopSec=90
Restart=on-failure
RestartSec=15s

Environment=RUST_BACKTRACE=full
Environment=GOTRACEBACK=crash
LimitNOFILE=100000
LimitCORE=infinity

StandardOutput=journal
StandardError=journal
SyslogIdentifier=metanode

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable metanode
sudo systemctl start metanode
```

**Manage the node:**

```bash
# Check status
sudo systemctl status metanode

# View logs (real-time)
journalctl -u metanode -f

# View last 200 lines
journalctl -u metanode -n 200

# Stop (waits for DB flush)
sudo systemctl stop metanode

# Restart
sudo systemctl restart metanode
```

### Using tmux (alternative)

```bash
cd ~/metanode
tmux new-session -d -s metanode \
    "ulimit -n 100000 && ./simple_chain -config=node_config.json 2>&1 | tee logs/node.log"

# Attach to see live output
tmux attach -t metanode
# Detach: Ctrl+B, D
```

---

## Step 5 — Verify Node is Running

### Check block height via JSON-RPC

```bash
# Validator (port 8757)
curl -X POST http://localhost:8757 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

# Expected: {"jsonrpc":"2.0","id":1,"result":"0x1234"}
# Convert: echo $((16#1234)) → 4660
```

### Check node is syncing

```bash
# Block height should increase over time
watch -n 5 'curl -sf http://localhost:8757 \
  -H "Content-Type: application/json" \
  -d "{\"jsonrpc\":\"2.0\",\"method\":\"eth_blockNumber\",\"params\":[],\"id\":1}" \
  | python3 -c "import sys,json; print(int(json.load(sys.stdin)[\"result\"],16))"'
```

### Check logs for consensus

```bash
# Validator: look for commit messages
journalctl -u metanode -f | grep -i "commit\|epoch\|peer\|block"

# Expected healthy output:
# [Consensus] Committed block #12345 at epoch 3
# [Peer] Connected to 4/4 validators
```

---

## Key Reference

### Summary of all keys

| Key Name | Type | Secret? | In Genesis? | Purpose |
|----------|------|---------|-------------|---------|
| `private_key` | Ethereum hex | ✅ YES | ❌ NO | Sign transactions |
| `BLSPrivateKey` | BLS hex | ✅ YES | ❌ NO | Sign blocks (DB-level) |
| `protocol_key.json` | BLS file | ✅ YES | public key only | Consensus voting |
| `network_key.json` | Ed25519 file | ✅ YES | public key only | P2P identity |
| `protocol_key` in genesis | BLS public | ❌ NO | ✅ YES | Verify your signatures |
| `network_key` in genesis | Ed25519 public | ❌ NO | ✅ YES | Find you on P2P network |
| `authority_key` in genesis | BLS public (aggregate) | ❌ NO | ✅ YES | Validator identity |

### Key security rules

```
✅ DO:
  - Store private keys in chmod 600 files, owned by your unix user
  - Back up keys to offline storage before starting
  - Use separate keys for each node

❌ DON'T:
  - Commit private keys to git
  - Share private_key or BLSPrivateKey with anyone
  - Reuse keys across multiple nodes
```

---

## Troubleshooting

### Node not connecting to peers

```bash
# Check firewall
sudo ufw status | grep -E "9000|4000|19200"

# Check DNS/P2P port is reachable from another server
nc -zv YOUR_IP 9000

# Check logs for connection errors
journalctl -u metanode | grep -i "peer\|connect\|timeout"
```

### Node stuck at same block height

```bash
# Check if peers are connected
curl -sf http://localhost:8757 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"net_peerCount","params":[],"id":1}'

# Check NTP sync
chronyc tracking | grep "System time"
# Offset must be < 1s — consensus stops if clocks diverge
```

### Recover from corrupted state

```bash
# Stop the node
sudo systemctl stop metanode

# Option A: Restore from snapshot (fastest)
# Download snapshot from a trusted validator's HTTP server
wget http://TRUSTED_VALIDATOR_IP:8600/api/snapshots   # List available
wget -r http://TRUSTED_VALIDATOR_IP:8600/files/SNAPSHOT_NAME/

# Option B: Full resync from genesis (slowest)
rm -rf ~/metanode/data
sudo systemctl start metanode
```

---

## Config Field Reference

### `node_config.json` (Go execution layer)

| Field | Type | Description |
|-------|------|-------------|
| `chainId` | int | Chain ID — must match genesis (`991`) |
| `private_key` | hex string | Ethereum account private key |
| `address` | hex string | Ethereum address (no `0x` prefix) |
| `connection_address` | `IP:PORT` | P2P sync port (bind address) |
| `rpc_port` | `:PORT` | JSON-RPC port for clients |
| `peer_rpc_port` | int | Port for peer-to-peer RPC queries |
| `genesis_file_path` | path | Path to `genesis.json` |
| `Databases.RootPath` | path | Directory for blockchain state DB |
| `Databases.BLSPrivateKey` | hex | BLS private key (validator only) |
| `snapshot_enabled` | bool | Enable snapshot serving (sync-only: true) |
| `snapshot_server_port` | int | HTTP port for snapshot downloads |
| `state_backend` | string | Always `"nomt"` |
| `rust_config_path` | path | Path to `consensus_config.toml` |
| `epochs_to_keep` | int | `0` = archive mode, `N` = keep last N epochs |
| `is_explorer` | bool | Enable block explorer API |

### `consensus_config.toml` (Rust consensus layer)

| Field | Type | Description |
|-------|------|-------------|
| `node_id` | int | Validator index in genesis (0-based) |
| `network_address` | `IP:PORT` | Consensus P2P bind address |
| `protocol_key_path` | path | BLS private key file |
| `network_key_path` | path | Ed25519 private key file |
| `storage_path` | path | Consensus DAG storage directory |
| `peer_rpc_port` | int | Local peer RPC port |
| `peer_rpc_addresses` | array | OTHER validators' peer RPC addresses |
| `epochs_to_keep` | int | `0` = archive, `N` = keep last N epochs |
| `executor_commit_enabled` | bool | `true` for validators, `false` for sync-only |
