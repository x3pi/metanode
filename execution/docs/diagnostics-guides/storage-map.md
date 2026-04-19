# Storage Map — Dữ Liệu Lưu Ở Đâu

## LevelDB Databases

Tất cả DB nằm dưới `RootPath` = `./sample/node{N}/data/data` (config `Databases.RootPath`)

| Database | Path | Dữ liệu | Thời gian lưu |
|:---|:---|:---|:---|
| `STORAGE_BLOCK` | `/blocks/` | Block header + transactions (protobuf) | **Vĩnh viễn** |
| | | `state_att:*` — StateAttestation (JSON) | **Vĩnh viễn** |
| `STORAGE_ACCOUNT` | `/account_state/` | Account state (balance, nonce, code hash) | **Vĩnh viễn** |
| `STORAGE_STAKE` | `/stake_db/` | Stake/validator state | **Vĩnh viễn** |
| `STORAGE_RECEIPTS` | `/receipts/` | Transaction receipts | **Vĩnh viễn** |
| `STORAGE_TRANSACTION` | `/transaction_state/` | Transaction execution state | **Vĩnh viễn** |
| `STORAGE_DATABASE_TRIE` | `/trie_database/` | Merkle trie nodes | **Vĩnh viễn** |
| `STORAGE_SMART_CONTRACT` | `/smart_contract_storage/` | Smart contract storage slots | **Vĩnh viễn** |
| `STORAGE_CODE` | `/smart_contract_code/` | Contract bytecode | **Vĩnh viễn** |
| `STORAGE_MAPPING_DB` | `/mapping/` | Block number ↔ hash mapping | **Vĩnh viễn** |
| `STORAGE_BACKUP_DB` | `/backup_db/` | Block backup data cho P2P sync | ⚠️ **50 blocks** gần nhất |
| `STORAGE_BACKUP_DEVICE_KEY` | `/backup_device_key_storage/` | Device key backup | **Vĩnh viễn** |

---

## STORAGE_BLOCK — Chi Tiết Key

```
STORAGE_BLOCK LevelDB (/blocks/)
│
├── [lastBlockHashKey]              → block mới nhất (protobuf)     Vĩnh viễn
│   (32 bytes, Keccak256 cố định)
│
├── [blockHash]                     → block data (protobuf)         Vĩnh viễn
│   (32 bytes, block header hash)
│
├── state_att:{block}:local:{addr}  → local attestation (JSON)      Vĩnh viễn
│   Ví dụ: state_att:100:local:0x4018...
│
└── state_att:{block}:peer:{addr}   → peer attestation (JSON)       Vĩnh viễn
    Ví dụ: state_att:100:peer:0x18c2...
```

> [!NOTE]
> Không xung đột key vì block keys là **raw 32-byte binary hash**, còn attestation keys là **ASCII string** bắt đầu bằng `state_att:`.

---

## Dữ Liệu Ngắn Hạn (RAM / Cache)

| Dữ liệu | Nơi lưu | Thời gian giữ | Mất khi restart? |
|:---|:---|:---|:---|
| `attestationCollector.attestations` | RAM (in-memory map) | ⚠️ **100 blocks** gần nhất | ✅ Mất |
| `attestationCollector.localAttestations` | RAM (in-memory map) | ⚠️ **100 blocks** gần nhất | ✅ Mất |
| Block backup cache (`HostNode.storage`) | RAM (in-memory map) | ⚠️ **50 blocks** gần nhất | ✅ Mất |
| `STORAGE_BACKUP_DB` (P2P backup) | LevelDB `/backup_db/` | ⚠️ Không tự cleanup | ❌ Không mất |
| Pending transactions (mempool) | RAM | Đến khi thực thi | ✅ Mất |

> [!IMPORTANT]
> Attestation data được lưu **2 nơi**: RAM (100 blocks, cho real-time fork detection) và LevelDB (vĩnh viễn, cho audit trail). RAM data mất khi restart nhưng không ảnh hưởng vì fork detection chỉ cần block hiện tại.

---

## Rust Consensus Storage

| Dữ liệu | Path | Config |
|:---|:---|:---|
| Consensus WAL + commits | `config/storage/node_{N}` | `node_{N}.toml → storage_path` |
| Committee config | `config/committee.json` | File JSON |
| Authority private keys | `config/node_{N}_authority_key.json` | File JSON (hex) |
| Protocol/Network keys | `config/node_{N}_{protocol|network}_key.json` | File JSON |

---

## Source Code References

| Component | File |
|:---|:---|
| Storage interface | [storage.go](file:///home/abc/chain-n/mtn-simple-2025/pkg/storage/storage.go) |
| Storage manager | [storage_manager.go](file:///home/abc/chain-n/mtn-simple-2025/pkg/storage/storage_manager.go) |
| DB initialization | [app_storage.go](file:///home/abc/chain-n/mtn-simple-2025/cmd/simple_chain/app_storage.go) |
| Block save | [block_database.go](file:///home/abc/chain-n/mtn-simple-2025/pkg/block/block_database.go) |
| Attestation persist | [block_processor_attestation.go](file:///home/abc/chain-n/mtn-simple-2025/cmd/simple_chain/processor/block_processor_attestation.go) |
| DB config | [config-master-node0.json](file:///home/abc/chain-n/mtn-simple-2025/cmd/simple_chain/config-master-node0.json) → `Databases` section |
