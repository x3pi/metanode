# 🚀 MetaNode Multi-Cluster Ansible Deployment & Automated Testing

Hệ thống Ansible tự động hóa triển khai, quản lý vòng đời và kiểm thử tích hợp cho kiến trúc **Parent Chain + Sharded Execution Clusters (Rollup Architecture)** của MetaNode, đi kèm tích hợp thông báo trạng thái & cảnh báo thời gian thực qua **Telegram Bot**.

> 📚 **Tài liệu hướng dẫn toàn diện:** Chi tiết kiến trúc, cấu hình đa máy chủ, giải thích 5 kịch bản kiểm thử, cơ chế phục hồi offline và xử lý lỗi được trình bày đầy đủ tại:  
> 👉 [**DEPLOYMENT_AND_TESTING_GUIDE.md**](DEPLOYMENT_AND_TESTING_GUIDE.md)

---

## 📐 Kiến Trúc Triển Khai

| Thành Phần | Định Danh & Cổng | Vai Trò & Cơ Chế |
| :--- | :--- | :--- |
| **Parent Chain** | ChainID: `991`<br>HTTP RPC: `:8547`<br>P2P: `:4000` | Native State Store (Float Accounts, Account Registry, Cluster Registry, Claimed Messages). Quản lý cọc float và điều phối bảo lãnh chuyển tiền. |
| **Exec Cluster 1** | ClusterID: `1`<br>EVM ChainID: `991`<br>RPC: `:8646`<br>P2P: `:4200` | Cụm thực thi Shard 1. Chạy Rollup workers (`SendWorker`, `ReceiveWorker`, `ReclaimWorker`). Tiếp nhận giao dịch EVM chuẩn. |
| **Exec Cluster 2** | ClusterID: `2`<br>EVM ChainID: `991`<br>RPC: `:8647`<br>P2P: `:4202` | Cụm thực thi Shard 2. Tương tác giao dịch xuyên cụm (cross-cluster transfer) với Cluster 1 qua Float Account trên Parent Chain. |

---

## 📁 Cấu Trúc Thư Mục

```
deploy/ansible_clusters/
├── ansible.cfg                # Cấu hình Ansible tối ưu (pipelining, timeouts, callbacks)
├── inventory.example.yml      # Mẫu file inventory tham khảo (che các khóa/mật khẩu bí mật)
├── inventory.yml              # File inventory thực tế (đã đưa vào .gitignore chống lộ key)
├── .env.example               # Mẫu cấu hình Telegram Bot Token & Chat ID
├── .env                       # File cấu hình Telegram bí mật (gitignore)
├── deploy.yml                 # Ansible Playbook chính (Build, Parent Chain, Exec Clusters, Test)
├── deploy_clusters.sh         # Script điều phối 1-click tích hợp thông báo Telegram
├── group_vars/
│   └── all.yml                # Biến toàn cục (paths, RPC URLs, log dirs)
├── roles/
│   ├── build/                 # Biên dịch binaries (parent_chain, simple_chain)
│   ├── common/                # Tạo thư mục /opt/metanode, phân phối binaries
│   ├── parent_chain/          # Tạo config, template systemd/daemon, khởi chạy & health check :8547
│   ├── exec_cluster/          # Sinh genesis, cấu hình cluster_id, khởi chạy & health check RPC
│   ├── testing/               # Chạy bộ test tích hợp 5 kịch bản thực tế
│   └── telegram/              # Gửi thông báo Telegram theo sự kiện playbook
├── scripts/
│   └── telegram_notify.py     # Module Python gửi tin nhắn HTML đẹp qua Telegram API (zero-dep)
├── DEPLOYMENT_AND_TESTING_GUIDE.md  # 📘 Hướng dẫn chuyên sâu từ A-Z (Kiến trúc, Deploy, 5 Scenarios Test, Khắc phục lỗi)
└── README.md                  # Hướng dẫn nhanh này
```

---

## ⚡ Hướng Dẫn Sử Dụng Nhanh (1-Click)

### File endpoint dùng chung

`--reset`, `--export-config`, `--test` và `--test-only` xuất cấu hình cluster vào
`/tmp/rpc_nodes.json`, giữ nguyên node public chain `m0–m4` cùng metadata hiện có.
Các map `nodes`, `rpc_nodes`, `ws_nodes`, `tcp_nodes`, `raft_nodes`, `forward_nodes`
được bổ sung node cluster theo tên inventory (`exec1_replica1`, `exec2_replica1`, ...).
Node cluster cũ có tiền tố `exec`/`parent_node_` được thay bằng dữ liệu inventory hiện tại.

Bộ test tích hợp đọc `root_anchor`, `private_chains.chain_a.rpc_url` và
`private_chains.chain_b.rpc_url` từ file này để truyền vào `PARENT_CHAIN_URL`,
`EXEC1_URL`, `EXEC2_URL`. `chain_a`/`chain_b` tương ứng cluster ID 1/2;
`root_anchor` là endpoint dịch vụ `parent_chain` từ inventory, không phải alias cho `m0`.
Khi nhóm parent rỗng, endpoint này lấy từ `parent_chain_host`/`parent_chain_rpc_port`.
Do file này chứa private-chain credentials, nó luôn được ghi với quyền `0600`.

Public chain (`ansible_deploy.sh`) cũng tự gộp endpoint khi chạy, giữ nguyên cluster đã xuất;
khởi động lần lượt public trước hay cluster trước đều không làm mất cấu hình bên còn lại.
Không cần chạy export thủ công sau khi khởi động.

Có thể cập nhật riêng file mà không reset node bằng `./deploy_clusters.sh --export-config`.
Việc dùng chung endpoint không thay thế yêu cầu API `parent_chain` của bộ test;
kịch bản lỗi node 8–9 vẫn phụ thuộc cổng/đường dẫn local được định nghĩa trong test Go.

### 1. Cấu hình Telegram (Tùy chọn)
Chỉnh sửa file `.env`:
```bash
TELEGRAM_BOT_TOKEN="your_bot_token_here"
TELEGRAM_CHAT_ID="your_chat_id_here"
```
Kiểm tra kết nối Bot:
```bash
python3 scripts/telegram_notify.py --test
```

### 2. Triển khai toàn bộ cụm node và chạy test tự động
```bash
cd deploy/ansible_clusters
./deploy_clusters.sh --setup --test
```
Lệnh trên sẽ:
1. Gửi thông báo 🚀 **Deploy Bắt đầu** lên Telegram (kèm commit hash, author, nhánh git).
2. Tự động kiểm tra và build các binary Go (`parent_chain`, `simple_chain`).
3. Khởi chạy **Parent Chain** trên cổng `:8547` và kiểm tra HTTP RPC sẵn sàng.
4. Khởi chạy **Exec Cluster 1** (`:8646`, ChainID `991`) và **Exec Cluster 2** (`:8647`, ChainID `991`).
5. Gửi thông báo ✅ **Dịch Vụ Sẵn Sàng** lên Telegram (kèm block heights và ports).
6. Tự động thực thi **Bộ kiểm thử tích hợp 5 kịch bản thực tế**:
   - *Kịch bản 1:* Đăng ký tài khoản mới & ánh xạ vào Account Registry trên Parent Chain.
   - *Kịch bản 2:* Nạp tiền Float & Rollup ReceiveWorker ghi có số dư.
   - *Kịch bản 3:* Tương tác gọi Smart Contract nội bộ trên node thực thi.
   - *Kịch bản 4:* Giao dịch chuyển tiền xuyên 2 cụm node (Exec 1 -> Exec 2).
   - *Kịch bản 5:* Khả năng tự vận hành độc lập khi Parent Chain offline.
7. Gửi báo cáo chi tiết 🎉 **Kết Quả Kiểm Thử** lên Telegram.

---

## ⚠️ CẢNH BÁO NGUY HIỂM: `--restart` vs `--reset` VÀ XOAY KHÓA

> 🚨 **QUAN TRỌNG:**
> - `./deploy_clusters.sh --restart` **KHÔNG** xóa dữ liệu đĩa (`consensus/` và `parentchain_db/`), nhưng **RENDER LẠI** file cấu hình `node.toml`, `parent_genesis.json` và phân phối lại khóa từ `parent_keys/`.
> - Nếu ai đó đã sinh lại khóa trong `parent_keys/` mà chạy `./deploy_clusters.sh --restart`, cụm sẽ rơi vào tình trạng **KẸT ĐỒNG THUẬN VĨNH VIỄN** (`Invalid signature` loop) do node dùng khóa mới để xác thực DAG lịch sử cũ.
> - **Khi muốn đổi/xoay khóa hoặc khởi tạo lại chain:** Bắt buộc dùng `./deploy_clusters.sh --reset --force`. Không bao giờ được sinh khóa mới rồi dùng `--restart`.
> - Hệ thống đã tích hợp chốt chặn an toàn (Safety Guard): Playbook và binary sẽ từ chối khởi động nếu phát hiện fingerprint của ủy ban genesis không khớp với DAG hiện hữu. Xem chi tiết tại [Runbook phục hồi Parent Chain](../../note/parent_chain_recovery_runbook.md).

---

## 🔍 Phân Biệt Cấu Hình: `parent_chain_*` vs `parent_prod_*`

- **`parent_chain_*` (trong `inventory.yml`):**
  + Cụm Parent Chain 4 validator chuyên dụng cho hệ thống Sharded Execution Clusters (Rollup Architecture).
  + Đảm nhiệm việc đăng ký cụm (`Cluster Registry`), quản lý tài khoản người dùng (`Account Registry`), và ký quỹ cọc (`Float Accounts`).
  + Các Execution Clusters (`exec1`, `exec2`) kết nối trực tiếp đến các endpoint này qua `QuorumClient` và `RegistrationRelay`.
- **`parent_prod_*` (trong `inventory.production.yml`):**
  + Cấu hình Parent Chain độc lập dùng cho môi trường Production thực tế.
  + Đóng quyền đăng ký tự do (`parent_open_cluster_registration: false`), chế độ bảo tồn float nghiêm ngặt (`bls_conservation_mode: "enforce"`), yêu cầu mã hóa secrets và cấu hình tường lửa IP chặt chẽ.

---

## 📊 Giám Sát Cụm Parent Chain (`parent_chain_monitor.py`)

Công cụ `scripts/parent_chain_monitor.py` tự động đọc thông tin node và cấu hình Telegram từ `inventory.yml` để giám sát độ sống còn, tính toàn vẹn trạng thái và quét log phát hiện bất thường:

### Cách chạy:
```bash
# Kiểm tra nhanh 1 lần (dùng cho CI/CD hoặc kiểm tra sau deploy):
python3 scripts/parent_chain_monitor.py --once

# Khởi chạy chế độ daemon chạy ngầm liên tục:
python3 scripts/parent_chain_monitor.py --daemon

# Dừng tiến trình daemon:
python3 scripts/parent_chain_monitor.py --stop

# Xem log giám sát thời gian thực:
tail -f /tmp/parent_chain_monitor.log
```

### Các cơ chế cảnh báo:
- **Node Down / Offline:** Cảnh báo tức thời qua Telegram khi phát hiện node không phản hồi HTTP RPC. Tự động gửi thông báo phục hồi khi node hoạt động trở lại.
- **Fork Detection & Hash Mismatch:** Cảnh báo tức thời nếu cờ `fork_detected` trên node chuyển sang `true`, hoặc nếu phát hiện `last_hash` / `state_root` khác nhau giữa các node tại cùng độ cao block.
- **Invalid Signature (`INVALID_SIG_COUNT_THRESHOLD = 5`):** Quét log cục bộ và cảnh báo ngay khi phát hiện từ 5 lỗi `Invalid signature` / `Failed to verify the block's signature` mới (dấu hiệu lệch khóa consensus).

---

## 🛠️ Danh Sách Các Lệnh Vận Hành

| Lệnh | Ý nghĩa |
| :--- | :--- |
| `./deploy_clusters.sh --status` | Xem trạng thái RPC và Block Height hiện tại của tất cả cụm node |
| `./deploy_clusters.sh --test-only` | Chỉ chạy bộ kiểm thử kịch bản thực tế (không deploy lại) |
| `./deploy_clusters.sh --deploy` | Cập nhật mã nguồn/binary mới và restart các cụm |
| `./deploy_clusters.sh --restart` | Khởi động lại toàn bộ Parent Chain và các Execution Clusters |
| `./deploy_clusters.sh --stop` | Dừng an toàn toàn bộ tiến trình cụm node |
| `./deploy_clusters.sh --clean` | Xóa database và logs, giữ lại cấu hình và keys |
| `./deploy_clusters.sh --reset` | Reset toàn bộ hệ sinh thái về Block 0 và khởi chạy lại |
| `./deploy_clusters.sh --systemd` | Sử dụng `systemd` service thay vì background daemon |
| `./deploy_clusters.sh --env=production` | Đặt môi trường deploy (`devnet` hoặc `production`). Mặc định là `devnet` cho test cluster. |
| `./deploy_clusters.sh --vault-password-file FILE` | Chỉ định file mật khẩu Ansible Vault để giải mã credentials |

---

## 🌐 Triển Khai Multi-Server (Production)

Mặc định `inventory.yml` chạy trên localhost (`127.0.0.1`) cho môi trường devnet. Để deploy lên nhiều server vật lý hoặc VPS môi trường production:
1. Mở `inventory.yml` và thay đổi `ansible_host` thành IP của server tương ứng.
2. Đổi `ansible_connection: ssh`.
3. Bổ sung SSH key hoặc mã hóa mật khẩu bằng Ansible Vault (`!vault | ...`):
```yaml
parent_chain_nodes:
  hosts:
    parent_node:
      ansible_host: 192.168.1.100
      ansible_user: metanode
      ansible_ssh_private_key_file: ~/.ssh/id_ed25519

exec_clusters:
  hosts:
    exec_cluster_1:
      ansible_host: 192.168.1.101
      ansible_user: metanode
      cluster_id: 1
    exec_cluster_2:
      ansible_host: 192.168.1.102
      ansible_user: metanode
      cluster_id: 2
```

> 🔒 **Quy tắc Bảo mật Credentials:**
> - Ở môi trường `--env=production`, hệ thống sẽ **chặn hoàn toàn** nếu inventory chứa mật khẩu plaintext (`ansible_become_pass`, `ansible_ssh_pass`, `ansible_password`, `ansible_sudo_pass`).
> - Nếu dùng mật khẩu, bắt buộc mã hóa qua Ansible Vault (`ansible-vault encrypt_string`) và truyền cờ `--vault-password-file`.
> - Nếu deploy với inventory plaintext, bạn PHẢI đặt `--env=devnet` (hoặc `export METANODE_ENV=devnet`).

4. Chạy với cờ `--systemd` và `--env=production`:
```bash
./deploy_clusters.sh --setup --systemd --env=production --vault-password-file .vault_pass
```
Mỗi server sẽ tự động tạo systemd service riêng (`metanode-parentchain.service`, `metanode-cluster-1.service`, `metanode-cluster-2.service`) với cấu hình tự khởi động lại (`Restart=always`) và giới hạn file descriptors cao (`LimitNOFILE=65536`).

## Firewall (UFW) — opt-in và giới hạn nguồn

Triển khai bình thường (`setup`, `deploy`, `restart`, `reset`) **không** thay đổi tường lửa. Chỉ khi chạy với `--open-ports`
(`deploy_action=open_ports`) và UFW đang bật thì role mới thêm rule:

- Cổng **client** (RPC của exec, HTTP RPC của parent chain): mở cho mọi nguồn.
- Cổng **nội bộ** (P2P, Raft, Forward của exec; peer RPC và metrics của parent chain): chỉ mở cho IP của các node khác trong
  inventory (`exec_clusters` + `parent_chain_nodes`, bỏ `127.0.0.1`), cộng thêm danh sách `ufw_extra_sources` nếu khai báo
  (ví dụ máy giám sát lấy metrics). Raft và Forward là kênh nội bộ giữa các node, không nên mở ra toàn mạng.

Ví dụ thêm máy giám sát: `-e '{"ufw_extra_sources":["10.0.0.5"]}'`.
