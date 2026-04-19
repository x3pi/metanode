# Security Guide — MetaNode (Go Layer)

This document describes the security configuration, hardening measures, and operational
best practices for the MetaNode Go node (`mtn-simple-2025`).

## Table of Contents

1. [Environment Variable Secrets](#environment-variable-secrets)
2. [Network Security](#network-security)
3. [Authentication](#authentication)
4. [Rate Limiting](#rate-limiting)
5. [CORS Policy](#cors-policy)
6. [IPC / UDS Security](#ipc--uds-security)
7. [Cryptographic Security](#cryptographic-security)
8. [Fork Safety](#fork-safety)
9. [Debug Endpoints](#debug-endpoints)

---

## Environment Variable Secrets

Sensitive configuration values can be injected via environment variables instead of
storing them in `config.json`. **Environment variables take precedence** over config file values.

| Environment Variable | Config Key | Description |
|---|---|---|
| `META_PRIVATE_KEY` | `private_key` | BLS validator private key |
| `META_REWARD_PRIVATE_KEY` | `reward_sender_private_key` | ETH private key for reward distribution |
| `META_SECURE_PASSWORD` | `securepassword` | Admin API password |

### Usage

```bash
# Set secrets via environment (recommended for production)
export META_PRIVATE_KEY="0x..."
export META_REWARD_PRIVATE_KEY="0x..."
export META_SECURE_PASSWORD="your-strong-password"

# Start the node — config.json secrets are overridden
./simple_chain --config config.json
```

After setting environment variables, you can remove the corresponding plaintext values
from `config.json` to reduce secret exposure.

---

## Network Security

### Ports and Protocols

| Port | Protocol | Purpose | Authentication |
|---|---|---|---|
| `rpc_port` (config) | HTTP/WS/HTTPS | RPC API (MetaMask, wallets, dApps) | None (public) |
| `4200` (hardcoded) | TCP | P2P block/TX communication (Go↔Go) | None |
| `peer_rpc_port` (config) | TCP | Peer discovery RPC | None |
| QUIC | QUIC/TLS 1.2+ | File chunk uploads | TLS (InsecureSkipVerify*) |

> **⚠️ Note**: QUIC connections use `InsecureSkipVerify: true` because peers use self-signed
> certificates. To fully secure, deploy a **private CA** and distribute certificates to
> all validators.

### Recommendations

- Run nodes on a **private network** or behind a **VPN/WireGuard** tunnel
- Use firewall rules to restrict P2P ports (4200, peer_rpc_port) to known validator IPs
- Expose only the RPC port to the public internet

---

## Authentication

### Admin API

The admin API (`admin_login`, `admin_setState`, `admin_createBackup`) uses a password
stored in config as `securepassword` (overridable via `META_SECURE_PASSWORD`).

**Security measures:**
- Password comparison uses **`crypto/subtle.ConstantTimeCompare()`** to prevent timing attacks
- Admin endpoints are **not** accessible via CORS from browsers (CORS restricted to RPC root only)

### RPC API

The RPC API (`eth_*`, `mtn_*`, `debug_*`) is **unauthenticated** by design (standard Ethereum RPC).
Use firewall rules to restrict access.

---

## Rate Limiting

Global and per-IP rate limiting is applied to all RPC requests.

| Parameter | Value | Description |
|---|---|---|
| Global rate | 300,000 req/s | Maximum across all clients |
| Global burst | 50,000 | Burst allowance |
| Per-IP rate | 10,000 req/s | Maximum per client IP |
| Per-IP burst | 2,000 | Per-client burst |
| IP cache size | 10,000 entries | Maximum tracked IPs |
| Cache cleanup | 5 minutes | Stale IP entry eviction |

Configuration is in `cmd/simple_chain/rpc_rate_limiter.go`.

---

## CORS Policy

CORS `Access-Control-Allow-Origin: *` is applied **only** to:
- `/` — Main RPC endpoint (required for MetaMask / browser wallets)
- `/ws` — WebSocket RPC (required for Ethereum subscription API)

**All other endpoints** (admin, debug, pipeline, metrics) do **NOT** have CORS headers,
preventing cross-site browser attacks.

---

## IPC / UDS Security

Communication between Go and Rust uses Unix Domain Sockets (UDS).

| Socket | Permission | Purpose |
|---|---|---|
| `/tmp/executor{N}.sock` | OS default | Rust → Go block delivery |
| `/tmp/metanode-tx-{N}.sock` | `0660` | Go → Rust TX submission |
| `/tmp/metanode-notification-{N}.sock` | `0660` | Go → Rust epoch notifications |

Socket permissions restrict access to the owner and group. Ensure the Go and Rust
processes run under the **same user or group**.

---

## Cryptographic Security

| Component | Algorithm | Details |
|---|---|---|
| BLS Key Storage | AES-256-GCM + Scrypt | N=32768, r=8, p=1 |
| Transaction Signing | ECDSA secp256k1 | Standard Ethereum |
| Block Signing | BLS12-381 | Master signs, Sub verifies |
| Xapian DB Path | Keccak-256 hash | DB name hashed to prevent path traversal |

---

## Fork Safety

The system implements multiple layers of fork prevention:

1. **State Attestation**: Quorum-based state attestation with automatic halt on divergence
2. **Consensus Timestamps**: All timestamps derived from Rust consensus (not local clock)
3. **Deterministic TX Ordering**: Transactions sorted by hash before execution
4. **Excluded Items Clearing**: Pool-local data cleared before processing consensus blocks
5. **Critical Halt**: `os.Exit(1)` / `process::exit(1)` on unrecoverable state inconsistency

---

## Debug Endpoints

> **⚠️ Warning**: Debug endpoints expose sensitive operational data. Restrict access
> in production using firewall rules.

| Endpoint | Type | Auth | Description |
|---|---|---|---|
| `debug_traceTransaction` | RPC | None | Re-execute transaction with tracing |
| `debug_traceBlock` | RPC | None | Re-execute entire block with tracing |
| `debug_listLogFiles` | RPC | None | List log files by epoch |
| `debug_getLogFileContent` | RPC | None | Read log file contents |
| `/debug/logs/ws` | HTTP/WS | None | Real-time log streaming |
| `/debug/logs/content` | HTTP GET | None | Log file preview |

**Mitigation**: Bind RPC to `127.0.0.1` in production, or use iptables to restrict
access to trusted management IPs only.
