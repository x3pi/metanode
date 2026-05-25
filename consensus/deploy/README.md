# 🚀 Production Deployment — Metanode Chain

Thư mục này chứa toàn bộ scripts và hướng dẫn triển khai Metanode Chain lên production.

## 📁 Cấu trúc

```
deploy/
├── README.md                    ← File này
├── setup_chain.sh               ← Full setup: build + genesis + start (dev/CI)
├── prod_setup_nodes.sh          ← Cài systemd service trên từng server
├── prod_deploy.sh               ← Deploy binary + config sang servers remote
├── prod_monitor.sh              ← Health check + Telegram alert (chạy qua cron)
└── prod_deploy.env.template     ← Template config — copy thành prod_deploy.env
```

> **`prod_deploy.env` (file thực tế)** KHÔNG có trong git — chứa IP, SSH key, credentials.
> Copy từ template rồi điền giá trị thật: `cp prod_deploy.env.template prod_deploy.env`

---

## ⚡ Quick Start — Deploy Production

```bash
# Bước 1: Tạo config deploy
cd /home/abc/nhat/con-chain-v2/metanode/consensus/deploy
cp prod_deploy.env.template prod_deploy.env
nano prod_deploy.env   # Điền IP thật, SSH key path

# Bước 2: Deploy toàn bộ cluster
./prod_deploy.sh --all

# Bước 3: Cài systemd service (auto-restart)
./prod_setup_nodes.sh

# Bước 4: Setup monitoring
crontab -e
# Thêm dòng: * * * * * /home/abc/nhat/con-chain-v2/metanode/consensus/deploy/prod_monitor.sh
```

---

## 🗺️ Kiến trúc

```
Internet
    │
    ▼ JSON-RPC (port 8545-8550)
[RPC Proxy per node]
    │
    ▼
┌────────────────────────────────────┐
│ SERVER A  (NODE_A_IP)              │
│  ├── Node 0  P2P:9000  RPC:8757   │
│  └── Node 1  P2P:9001  RPC:10747  │
├────────────────────────────────────┤
│ SERVER B  (NODE_B_IP)              │
│  └── Node 2  P2P:9002  RPC:10749  │
├────────────────────────────────────┤
│ SERVER C  (NODE_C_IP)              │
│  ├── Node 3  P2P:9003  RPC:10750  │
│  └── Node 4  P2P:9004  RPC:10748  │
└────────────────────────────────────┘
```

### Ports cần mở trên firewall giữa tất cả servers

| Port | Giao thức | Mục đích |
|------|-----------|----------|
| 9000-9004 | TCP | Consensus P2P (Rust FFI) |
| 19000-19004 | TCP | Peer Discovery RPC |
| 4000-4400 | TCP | Go P2P state sync |
| 8600-8604 | TCP | Snapshot HTTP server |
| 8757 | TCP | Node 0 RPC |
| 10747-10750 | TCP | Node 1-4 RPC |
| 8545-8550 | TCP | External JSON-RPC (cho clients) |

---

## 📋 Checklist Production

### Hạ tầng
- [ ] NTP chrony đồng bộ trên tất cả servers (`chronyc tracking`, offset < 100ms)
- [ ] Firewall ports đã mở (xem bảng trên)
- [ ] Disk: ≥ 200GB/server | RAM: ≥ 16GB | CPU: ≥ 4 cores

### Bảo mật
- [ ] SSH key-based auth (không dùng password)
- [ ] `prod_deploy.env` KHÔNG được commit vào git
- [ ] Keys backed up offline trước khi deploy
- [ ] File permissions: `chmod 600 *_key.json`

### Build & Deploy
- [ ] `./update_ips.sh` với IP thật (trong `../metanode/scripts/node/`)
- [ ] `../metanode/scripts/build_check.sh` pass sạch
- [ ] `./prod_deploy.sh --all` thành công
- [ ] Block height tăng liên tục trên tất cả 5 nodes
- [ ] Peer count = 4 trên mỗi node

### Production Hardening
- [ ] Systemd service enabled (`./prod_setup_nodes.sh`)
- [ ] Monitoring/alert hoạt động
- [ ] Snapshot schedule định kỳ

---

## 🔑 Key Management

### Phân phối keys đúng server

| Server | Nodes | Keys cần có |
|--------|-------|-------------|
| Server A | Node 0, 1 | `node_0_*_key.json`, `node_1_*_key.json` |
| Server B | Node 2 | `node_2_*_key.json` |
| Server C | Node 3, 4 | `node_3_*_key.json`, `node_4_*_key.json` |

Keys nằm tại: `../metanode/config/node_N_protocol_key.json` và `node_N_network_key.json`

```bash
# Backup keys trước (BẮT BUỘC)
BACKUP=~/keys-backup-$(date +%Y%m%d)
mkdir -p "$BACKUP" && chmod 700 "$BACKUP"
cp ../metanode/config/*_key.json "$BACKUP/"
chmod 600 "$BACKUP/"*.json
```

---

## 🚑 Recovery

### Node crash → systemd tự restart (không cần làm gì)

```bash
journalctl -u metanode-node0 --since "10 minutes ago"
```

### Node lệch state → restore từ snapshot

```bash
../metanode/scripts/node/restore_node.sh <node_id>
```

### Toàn cluster restart sạch

```bash
../metanode/scripts/mtn-orchestrator.sh restart --fresh
```
