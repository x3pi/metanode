# 🚀 HƯỚNG DẪN TRIỂN KHAI & KIỂM THỬ CỤM METANODE (SỔ TAY LỆNH NHANH)
> **Kiến trúc:** Parent Chain (Coordinator `:8547`, P2P `:4000`, ChainID `990`) + Exec Shard 1 (Raft HA `:8646`) + Exec Shard 2 (Raft Single `:8647`) — Exec shards dùng EVM ChainID `991`.

---

## 🏛️ 1. SƠ ĐỒ KIẾN TRÚC: PARENT CHAIN & CÁC CHAIN CON (EXEC SHARDS)

```
                                ┌──────────────────────────────────────────────────────────┐
                                │             PARENT CHAIN (COORDINATOR ROOT)              │
                                │  • Consensus Engine: 100% Rust BFT (HotStuff 2f+1)       │
                                │  • Invariant: ZERO-FORK (Thà pending chứ tuyệt đối ko fork)│
                                │  • HTTP RPC: http://127.0.0.1:8547 | P2P: 127.0.0.1:4000 │
                                │  • State Engine: LevelDB/NOMT Native Store (ChainID 990) │
                                │  • Vai trò: Cluster Registry, Account Registry, Inbound  │
                                └─────────────▲──────────────────────────────▲─────────────┘
                                              │                              │
                     Chuyển tiền xuyên cụm:   │                              │ Nhận tiền & Xác nhận:
                     - Float Deposit / Send   │                              │ - Poll Inbound Transfers
                     - Ký chứng thực BLS      │                              │ - SendMarkClaimed (Xác nhận)
                                              │                              │
                    ┌─────────────────────────┴────────┐            ┌────────┴─────────────────────────┐
                    │     CHAIN CON 1: EXEC SHARD 1    │            │     CHAIN CON 2: EXEC SHARD 2    │
                    │  (Mô hình 3-Replica Raft HA)     │            │  (Mô hình Single Feed Raft)      │
                    ├──────────────────────────────────┤            ├──────────────────────────────────┤
                    │ • ClusterID: 1 | EVM ChainID: 991│            │ • ClusterID: 2 | EVM ChainID: 991│
                    │ • Động cơ: HashiCorp Raft v1.7.1 │            │ • Động cơ: HashiCorp Raft v1.7.1 │
                    │ • Auto-Failover: ~200ms (CFT)    │            │ • 1 Node: exec2_replica1 (:8647) │
                    │ • 3 Nodes:                       │            │ • Transport :7120 | Fwd :7220    │
                    │   - exec1_replica1 (RPC :8646)   │            │ • Rollup Workers:                │
                    │   - exec1_replica2 (RPC :8648)   │            │   - SendWorker (gửi cross-chain) │
                    │   - exec1_replica3 (RPC :8649)   │            │   - ReceiveWorker (nhận & credit)│
                    │ • Raft Net: :7110, :7111, :7112  │            │   - ReclaimWorker (hoàn cọc)     │
                    │ • Admin/Fwd: :7210, :7211, :7212 │            │                                  │
                    │ • Rollup Workers:                │            │                                  │
                    │   - SendWorker (gửi cross-chain) │            │                                  │
                    │   - ReceiveWorker (nhận & credit)│            │                                  │
                    │   - ReclaimWorker (hoàn cọc)     │            │                                  │
                    └──────────────────────────────────┘            └──────────────────────────────────┘
```

### Bảng phân bổ mạng & Cổng (Network Topology):
| Thực Thể | Vai Trò Hệ Thống | ChainID | HTTP RPC | P2P Consensus | Raft Transport | Admin / Forward | Node Hosts |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **Parent Chain** | Điều phối thanh khoản, Float & Registry | `990` | `http://127.0.0.1:8547` | `127.0.0.1:4000` | N/A (Rust BFT) | N/A | `parent_node` |
| **Chain con 1 (Exec 1)** | Cụm thực thi Rollup HA (3 Replicas) | `991` | `:8646`, `:8648`, `:8649` | `:4200`, `:4201`, `:4203` | `:7110`, `:7111`, `:7112` | `:7210`, `:7211`, `:7212` | `exec1_replica1`, `exec1_replica2`, `exec1_replica3` |
| **Chain con 2 (Exec 2)** | Cụm thực thi Rollup Single Node | `991` | `http://127.0.0.1:8647` | `127.0.0.1:4202` | `127.0.0.1:7120` | `127.0.0.1:7220` | `exec2_replica1` |

---

## ⚡ 2. BẢNG TRA CỨU LỆNH NHANH (ALL-IN-ONE CHEAT SHEET)

| Thao Tác | Lệnh Chạy Trực Tiếp | Ghi Chú |
| :--- | :--- | :--- |
| **Triển khai 1-Click (Devnet Daemon)** | `./deploy_clusters.sh --setup --test` | Build, cấu hình genesis, chạy daemon, test 5 kịch bản & báo Telegram |
| **Chỉ chạy/deploy các Chain con** | `./deploy_clusters.sh --setup --exec-only` | Triển khai chỉ các cụm execution cluster (bỏ qua Parent Chain) |
| **Dừng/Bật 1 node cụ thể** | `./deploy_clusters.sh --stop --node=exec1_r3` | Dừng/bật an toàn riêng 1 replica để test chịu lỗi (vd: `exec1_r3`) |
| **Triển khai Production (Systemd)** | `./deploy_clusters.sh --setup --systemd --test` | Quản lý vòng đời qua systemd unit, tự restart khi sự cố |
| **Kiểm tra trạng thái & Ports** | `./deploy_clusters.sh --status` | In bảng port RPC/WS/TCP/Raft & ping block height |
| **Xuất cấu hình cổng vào /tmp** | `./deploy_clusters.sh --export-config` | Xuất file thống nhất `/tmp/rpc_nodes.json` |
| **Chỉ chạy bộ test tích hợp** | `./deploy_clusters.sh --test-only` | Chạy 5 kịch bản E2E thực tế, bắn kết quả lên Telegram |
| **Test chịu lỗi Raft (Auto-Failover)** | `go run execution/scripts/test/test_raft_fault_tolerance.go` | Giả lập kill leader, đo thời gian bầu cử (~200ms) & zero-fork |
| **Chạy test E2E trực tiếp bằng Go** | `go run execution/scripts/test/test_real_world_scenarios.go` | Debug luồng giao dịch thực tế chi tiết từng bước |
| **Khởi động lại toàn bộ cụm** | `./deploy_clusters.sh --restart` | Khởi động lại Parent Chain và tất cả Exec Clusters |
| **Dừng an toàn toàn bộ cụm** | `./deploy_clusters.sh --stop` | Tắt an toàn các tiến trình, không làm hỏng dữ liệu LevelDB |
| **Cập nhật mã nguồn & restart** | `./deploy_clusters.sh --deploy` | Rebuild code mới, reload cấu hình và restart |
| **Reset toàn bộ về Genesis (Block 0)** | `./deploy_clusters.sh --reset` | Xóa sạch DB cũ, sinh lại genesis từ đầu |
| **Dọn dẹp logs tạm thời** | `./deploy_clusters.sh --clean` | Dọn dẹp logs cũ trong `/var/log/metanode/` |
| **Kiểm tra biên dịch (Build Check)** | `cd consensus/metanode/scripts && ./build_check.sh` | Kiểm tra build sạch Go, Rust BFT, NOMT FFI và C++/EVM |
| **Xuất cấu hình cổng vào /tmp** | `python3 scripts/parse_inventory.py inventory.yml export` | Cập nhật `/tmp/rpc_nodes.json` |
| **Bắn danh sách port lên Telegram** | `python3 scripts/telegram_notify.py --ready` | Báo danh sách port hiện tại lên bot Telegram |
| **Đồng bộ sang metanode-suite** | `bash ../metanode-suite/scripts/update-ip/update-ip.sh --chain 991` | Đồng bộ IP/Port sang bộ test dApp |

---

## 🛠️ 3. CHI TIẾT SCRIPT ĐIỀU PHỐI: `deploy_clusters.sh`

File thực thi: [`deploy/ansible_clusters/deploy_clusters.sh`](file:///home/abc/nhat/con-chain-v2/metanode/deploy/ansible_clusters/deploy_clusters.sh)

### Cú pháp:
```bash
./deploy_clusters.sh [HÀNH ĐỘNG CHÍNH] [CỜ BỔ TRỢ]
```

### Bảng tham số đầy đủ:

| Nhóm | Tham Số (Flags) | Ý Nghĩa Kỹ Thuật |
| :--- | :--- | :--- |
| **Hành động (Action)** | `--setup` | Triển khai hoàn chỉnh từ đầu: build binary, sinh genesis, gán port, khởi động cụm |
| | `--deploy` | Biên dịch lại và cập nhật mã nguồn mới vào các node đang chạy |
| | `--start` | Bật lại các node đã cấu hình sẵn (toàn bộ hoặc lọc theo `--exec-only` / `--node`) |
| | `--stop` | Dừng an toàn các tiến trình (toàn bộ hoặc lọc theo `--exec-only` / `--node`) |
| | `--restart` | Khởi động lại các node (toàn bộ hoặc lọc theo `--exec-only` / `--node`) |
| | `--status` | Kiểm tra tình trạng kết nối RPC, lấy block number, in bảng toàn bộ port mạng |
| | `--export-config` | Xuất file cấu hình thống nhất `/tmp/rpc_nodes.json` |
| | `--test-only` | Chỉ chạy bộ kiểm thử tích hợp (không tác động trạng thái node) |
| | `--clean` | Dọn dẹp các file log cũ để giải phóng dung lượng đĩa |
| | `--reset` | Xóa trắng dữ liệu state database, đưa tất cả node về Genesis (Block #0) |
| **Phạm vi (Target & Scope)** | `--exec-only` | **Chỉ thao tác trên các Chain con (Execution Clusters)** (bỏ qua Parent Chain) |
| | `--parent-only` | Chỉ thao tác riêng trên Parent Chain (bỏ qua Chain con) |
| | `--node=NAME`, `-n` | **Chỉ thao tác trên 1 node cụ thể** (vd: `exec1_r1`, `exec1_r2`, `exec1_r3`, `parent`) |
| **Bổ trợ (Modifier)** | `--test` | Tự động kích hoạt test tích hợp ngay sau khi setup/deploy hoàn tất |
| | `--systemd` | Chạy dưới dạng Systemd service thay vì Background daemon |
| | `--notify` | Bật thông báo Telegram (mặc định bật nếu có file `.env`) |
| | `--no-notify` | Tắt hoàn toàn thông báo Telegram |
| | `--monitor` | (Mặc định) Kích hoạt monitor ngầm (Health check, Block Hash Checker qua `start_monitors.sh`) |
| | `--no-monitor` | Tắt không kích hoạt monitor ngầm sau khi deploy |
| | `--monitor-status` | Kiểm tra trạng thái các tiến trình monitor ngầm đang chạy |
| | `--stop-monitor` | Dừng các tiến trình monitor ngầm của cluster |
| | `--monitor-only` | Chỉ khởi động hệ thống monitor ngầm mà không deploy |
| | `-i <file>` / `--inventory=<file>` | Chỉ định file inventory tùy chọn (mặc định: `inventory.yml`) |
| | `--rpc-nodes-file=<file>` | Chỉ định đường dẫn file cấu hình RPC JSON tùy chọn (mặc định: `/tmp/rpc_nodes.json`) |

### Ví dụ phối hợp cờ lệnh thực tế:
```bash
# 1. CHỈ CHẠY CÁC CHAIN CON (Execution Clusters - không đụng tới Parent Chain):
./deploy_clusters.sh --setup --exec-only

# 2. Khởi động lại hoặc dừng riêng các Chain con:
./deploy_clusters.sh --restart --exec-only
./deploy_clusters.sh --stop --exec-only

# 3. Dừng và bật lại 1 node cụ thể của Chain con (vd: replica 3 của cluster 1):
./deploy_clusters.sh --stop --node=exec1_r3
./deploy_clusters.sh --start --node=exec1_r3

# 4. Xuất file cấu hình endpoint vào /tmp/rpc_nodes.json để test chain con:
./deploy_clusters.sh --export-config

# 5. Xuất cấu hình RPC vào file tùy ý hoặc DÙNG CHUNG FILE VỚI CHAIN 2:
./deploy_clusters.sh --export-config --rpc-nodes-file /tmp/rpc_nodes.custom.json

# 6. Triển khai cụm Cluster và phối hợp chung file RPC với Chain 2:
./deploy_clusters.sh --setup --rpc-nodes-file /tmp/rpc_nodes.shared.json
# Sau đó bên cụm Chain 2 (deploy/ansible):
# ./ansible_deploy.sh deploy --all --inventory ./inventory.chain2.yml --rpc-nodes-file /tmp/rpc_nodes.shared.json
# File rpc_nodes.shared.json sẽ tự động tích hợp cả parent_nodes, exec1/2 và chain_2 an toàn!

# 7. Triển khai toàn bộ (cả Parent Chain + Chain con) và chạy test:
./deploy_clusters.sh --setup --test

# 8. Kiểm tra nhanh trạng thái các node, danh sách port và monitor ngầm:
./deploy_clusters.sh --status

# 9. Quản lý hệ thống Monitor ngầm (Tái sử dụng start_monitors.sh & block_hash_checker):
./deploy_clusters.sh --monitor-status            # Kiểm tra trạng thái các tiến trình monitor
./deploy_clusters.sh --stop-monitor              # Dừng các tiến trình monitor ngầm
./deploy_clusters.sh --monitor-only --exec-only  # Kích hoạt riêng monitor cho chain con
```

**Các bước diễn ra tự động:**
1. 📢 Bắn thông báo **Deploy Bắt Đầu** lên Telegram kèm thông tin commit hash và author.
2. 🔨 Tự động kiểm tra và build nhị phân Go (`parent_chain`, `simple_chain`).
3. 🚀 Khởi chạy **Parent Chain** tại cổng `:8547` và kiểm tra RPC sẵn sàng.
4. ⚡ Khởi chạy đồng thời **Exec Cluster 1** (`:8646`) và **Exec Cluster 2** (`:8647`).
5. 📊 Ping lấy Block Height, Validator Address, BLS Key và bắn thông báo **Dịch Vụ Sẵn Sàng** lên Telegram.
6. 🧪 Tự động kích hoạt **Bộ kiểm thử 5 kịch bản thực tế**.
7. 🏆 Báo cáo kết quả kiểm thử (Thành công / Thất bại) lên Telegram.

### 3.2 Cách 2: Triển khai qua Ansible Playbook trực tiếp
Nếu muốn can thiệp chi tiết bằng lệnh Ansible gốc:
```bash
# Triển khai toàn bộ không chạy test
ansible-playbook -i inventory.yml deploy.yml

# Triển khai và bao gồm chạy test
ansible-playbook -i inventory.yml deploy.yml -e "run_tests=true"

# Chỉ triển khai riêng Parent Chain
ansible-playbook -i inventory.yml deploy.yml --tags parent_chain

# Chỉ triển khai riêng các Execution Clusters
ansible-playbook -i inventory.yml deploy.yml --tags exec_clusters
```

### 3.3 Chế độ Systemd Service vs Daemon
- **Mặc định (Daemon Mode):** Phù hợp môi trường local devnet; tiến trình chạy ngầm qua background job, log ghi vào thư mục `/opt/metanode/logs/` (hoặc `devnet_data/`).
- **Chế độ Systemd (Production Mode):** Để chạy dưới dạng dịch vụ Linux có quản lý vòng đời và tự khởi động lại:
  ```bash
  ./deploy_clusters.sh --setup --systemd --test
  ```
  Hệ thống sẽ tạo 3 service độc lập:
  - `metanode-parentchain.service`
  - `metanode-cluster-1.service`
  - `metanode-cluster-2.service`

### 3.4 Quản Lý Môi Trường & Bảo Mật Credentials (Ansible Vault)
Hệ thống triển khai phân tách rõ giữa môi trường Production và Devnet:
- **Môi trường Production (`--env=production` hoặc `METANODE_ENV=production`):**
  - Script pre-flight `check_inventory_security.py` và playbook Ansible sẽ **chặn đứng** quá trình triển khai nếu phát hiện bất kỳ mật khẩu plaintext nào (`ansible_become_pass`, `ansible_ssh_pass`, `ansible_password`, `ansible_sudo_pass`).
  - Bắt buộc phải sử dụng SSH Key không mật khẩu hoặc mã hóa mật khẩu bằng Ansible Vault (`!vault | ...`).
  - Binary `simple_chain` / `metanode` trên server sẽ chạy với cờ bảo vệ production, nghiêm cấm bypass chữ ký mempool.
- **Môi trường Devnet (`--env=devnet` hoặc `METANODE_ENV=devnet`):**
  - Cho phép sử dụng inventory chứa mật khẩu plaintext phục vụ mục đích kiểm thử và phát triển nhanh trong mạng nội bộ cô lập.
  - Mặc định script `deploy_clusters.sh` thiết lập `--env=devnet` để thuận tiện cho việc chạy bộ test 5 kịch bản.

#### 🔐 Hướng dẫn mã hóa thông tin bảo mật bằng Ansible Vault (Từng bước):

Hệ thống yêu cầu 2 biến bí mật mã hóa trong `all.vars` của `inventory.yml`:
- **`ansible_become_pass`:** Mật khẩu `sudo` thực tế của tài khoản (ví dụ tài khoản `abc`) để phân quyền chạy service và tạo thư mục `/opt/metanode`.
- **`parent_chain_rpc_token`:** Token bảo mật 32-byte dùng để xác thực các lệnh gọi RPC lên Parent Chain (bắt buộc khi chạy kiểm thử E2E).

---

##### 1. Tạo file chìa khóa bí mật của Vault (`.vault_pass`):
```bash
cd deploy/ansible_clusters
umask 077
read -rs -p "Nhập mật khẩu cho file Vault: " p; printf '\n'
printf '%s' "$p" > .vault_pass
unset p
```
*(💡 File `.vault_pass` đã nằm trong `.gitignore`, tuyệt đối an toàn không bị commit lên Git).*

---

##### 2. Mã hóa mật khẩu `sudo` máy thật (`ansible_become_pass`):
Thay `'mat_khau_sudo_that'` bằng chính xác mật khẩu đăng nhập máy tính (`sudo`) của bạn:
```bash
ansible-vault encrypt_string --vault-password-file .vault_pass 'mat_khau_sudo_that' --name ansible_become_pass
```

---

##### 3. Tạo ngẫu nhiên và mã hóa Token bảo mật Parent Chain (`parent_chain_rpc_token`):
Chạy lệnh sau để tự sinh chuỗi hex 32-byte ngẫu nhiên và mã hóa trực tiếp vào Vault:
```bash
ansible-vault encrypt_string --vault-password-file .vault_pass "$(openssl rand -hex 32)" --name parent_chain_rpc_token
```

---

##### 4. Dán 2 khối mã hóa vào file `inventory.yml`:
Mở file `inventory.yml` và dán 2 khối vừa sinh vào phần `all.vars`:
```yaml
all:
  vars:
    ansible_user: "abc"
    install_dir: "/opt/metanode"

    # 1. Khối mật khẩu sudo:
    ansible_become_pass: !vault |
          $ANSIBLE_VAULT;1.1;AES256
          6561366230616162326232326166663438346364...

    # 2. Khối token RPC Parent Chain (bắt buộc khi chạy test):
    parent_chain_rpc_token: !vault |
          $ANSIBLE_VAULT;1.1;AES256
          6134316363353539336466383639663630343462...

    raft_secret_file: "/opt/metanode/raft_secret.key"
    parent_chain_rpc_port: 18601
    parent_chain_host: "127.0.0.1"
    chain_id: 991
    parent_chain_id: 990
    devnet_sender_bls_pubkey: "0xb518c65d0f5f23858fd28f0473cb1fbaccc8aaa960880aee841585861f245abc0c4e4dce5b3693cfe60da4902d9484bc"
    parent_open_cluster_registration: true
```

---

##### 5. Thực thi triển khai & kiểm thử:
Script `deploy_clusters.sh` sẽ tự động tìm file `.vault_pass` trong thư mục hiện tại để giải mã:
```bash
# Triển khai reset từ đầu và tự động chạy toàn bộ 9 kịch bản test:
./deploy_clusters.sh --reset --test
```
Ansible sẽ tự phân phối token này cho Parent Chain và truyền tự động vào script test `test_real_world_scenarios.go`. Không cần phải gõ lệnh export thủ công.
---

## 🧪 4. BỘ SCRIPT KIỂM THỬ CHUYÊN SÂU (TESTING SCRIPTS)

### 4.1 Kiểm thử Tích hợp 5 Kịch bản Thực tế (E2E Integration Test)
File script: [`execution/scripts/test/test_real_world_scenarios.go`](file:///home/abc/nhat/con-chain-v2/metanode/execution/scripts/test/test_real_world_scenarios.go)

#### Cách chạy:
```bash
# Cách 1: Chạy qua pipeline tự động (khuyến nghị, tự bắn Telegram):
cd deploy/ansible_clusters
./deploy_clusters.sh --test-only

# Cách 2: Chạy trực tiếp qua Go từ thư mục repo gốc (để xem log chi tiết từng tx):
go run execution/scripts/test/test_real_world_scenarios.go

# Cách 3: Chạy với endpoint tùy biến (khi test remote server):
PARENT_CHAIN_URL="http://127.0.0.1:8547" \
EXEC1_URL="http://127.0.0.1:8646" \
EXEC2_URL="http://127.0.0.1:8647" \
go run execution/scripts/test/test_real_world_scenarios.go
```

#### Tóm tắt 9 kịch bản kiểm thử thực tế (E2E Test Suite):
1. **Kịch bản 1 (Account Registry):** Đăng ký ví mới vào Parent Chain với chữ ký kép (ECDSA + BLS cụm). Xác minh `GetAccountRegistry(addr)`.
2. **Kịch bản 2 (Float Deposit):** Nạp tiền từ Parent Chain vào ví trên Cluster 2. `ReceiveWorker` bắt giao dịch và credit số dư `eth_getBalance`.
3. **Kịch bản 3 (Smart Contract MVM):** Thực thi smart contract nội bộ trên Cluster 1 (ChainID `991`), gọi hàm `setBlsPublicKey`, sinh receipt và block hash.
4. **Kịch bản 4 (Cross-Cluster Transfer):** Chuyển tiền liên shard (Cluster 1 -> Cluster 2). `SendWorker` trừ ví nguồn -> Parent Chain ghi nhận -> `ReceiveWorker` credit ví đích.
5. **Kịch bản 5 (Parent Chain Offline Resilience):** Tắt Parent Chain (`pkill parent_chain`), gửi giao dịch nội bộ Cluster 1 -> Cụm vẫn tự đóng block bình thường. Bật lại Parent Chain -> tự động catch-up.
6. **Kịch bản 6 (Parent Chain Reconnect & Recovery):** Khôi phục Parent Chain sau sự cố -> Cầu nối Rollup và các worker tự động tái kết nối, xử lý tiếp giao dịch liên cụm.
7. **Kịch bản 7 (Cross-Cluster Smart Contract Call):** Exec 1 kích hoạt luồng gọi Smart Contract sang Exec 2 thông qua Parent Chain, Exec 2 xử lý và cập nhật hợp đồng EVM thành công.
8. **Kịch bản 8 (Chịu lỗi mất 1/4 node Parent Chain):** Dừng 1 node BFT HotStuff, còn 3/4 node (đủ Quorum 2f+1) -> Giao dịch chuyển xuyên cụm vẫn hoàn tất bình thường.
9. **Kịch bản 9 (Chịu lỗi mất Quorum Parent Chain - Zero-Fork):** Dừng tiếp node thứ 2, còn 2/4 node (< 2f+1) -> Toàn bộ giao dịch dừng an toàn ở trạng thái PENDING (tuyệt đối không tạo tiền/fork). Khi bật lại các node, giao dịch hoàn tất đúng 1 lần duy nhất (Exactly-once).

---

### 4.2 Kiểm thử Khả Năng Chịu Lỗi Cụm Raft (CFT / Auto-Failover Test)
File script: [`execution/scripts/test/test_raft_fault_tolerance.go`](file:///home/abc/nhat/con-chain-v2/metanode/execution/scripts/test/test_raft_fault_tolerance.go)

#### Cách chạy:
```bash
# Từ thư mục gốc repo:
go run execution/scripts/test/test_raft_fault_tolerance.go
```

#### Diễn biến kiểm thử tự động:
1. Khởi chạy cụm 3 replica Raft (`n0`, `n1`, `n2`), xác minh `n0` là Leader ban đầu. Đóng Block #1.
2. Giả lập sự cố phần cứng: Cưỡng chế tắt Leader (`kill -9 n0`).
3. Tự động bầu Leader mới: 2 node còn lại phát hiện mất tín hiệu, tự bầu `n1`/`n2` làm Leader mới trong **~200ms**.
4. Tiếp tục nhận giao dịch và đóng Block #2 thành công khi đang thiếu 1 node (đủ Quorum 2/3).
5. Phục hồi node cũ: Bật lại `n0`, node tự động tái kết nối, catch-up đầy đủ Block #3 (Zero-Fork 100%).

---

## 🔧 5. CÁC SCRIPT BỔ TRỢ & TIỆN ÍCH QUẢN TRỊ

### 5.1 Tra cứu & Xuất cấu hình cổng: `parse_inventory.py`
File script: [`deploy/ansible_clusters/scripts/parse_inventory.py`](file:///home/abc/nhat/con-chain-v2/metanode/deploy/ansible_clusters/scripts/parse_inventory.py)

```bash
cd deploy/ansible_clusters

# 1. In bảng danh sách toàn bộ port (RPC, WS, TCP, Raft) ra terminal:
python3 scripts/parse_inventory.py inventory.yml summary

# 2. Xuất dữ liệu cấu hình vào /tmp (tự động gộp với Public Chain và Chain 2):
python3 scripts/parse_inventory.py inventory.yml export

# 3. Xuất ra file cấu hình tùy ý (hoặc file dùng chung với Chain 2):
python3 scripts/parse_inventory.py inventory.yml export /tmp/rpc_nodes.custom.json

# 4. Xuất JSON thô để script khác sử dụng:
python3 scripts/parse_inventory.py inventory.yml json
```

*Quy cách file tạm sinh ra:*
- [`/tmp/rpc_nodes.json`](file:///tmp/rpc_nodes.json) hoặc custom file: Gộp chung thông minh các node Public Chain (`m0`..`m4`), Chain 2 (`m5`..`m8` trong `public_chains.chain_2`) và Cluster nodes (`parent_nodes`, `exec1_replica*`, `exec2_replica*`).
- Cung cấp đầy đủ topology cho `metanode-suite` và các test scripts mà không bị ghi đè lẫn nhau.

---

### 5.2 Bắn thông báo Telegram: `telegram_notify.py`
File script: [`deploy/ansible_clusters/scripts/telegram_notify.py`](file:///home/abc/nhat/con-chain-v2/metanode/deploy/ansible_clusters/scripts/telegram_notify.py)

```bash
cd deploy/ansible_clusters

# 1. Test kết nối bot Telegram:
python3 scripts/telegram_notify.py --test

# 2. Bắn thông báo danh sách port hiện tại lên Telegram:
python3 scripts/telegram_notify.py --ready

# 3. Bắn thông báo deploy bắt đầu:
python3 scripts/telegram_notify.py --start
```

---

### 5.3 Kiểm tra toàn bộ mã nguồn: `build_check.sh`
File script: [`consensus/metanode/scripts/build_check.sh`](file:///home/abc/nhat/con-chain-v2/metanode/consensus/metanode/scripts/build_check.sh)

```bash
cd consensus/metanode/scripts
./build_check.sh
```
*Tác vụ:* Biên dịch và kiểm tra 100% không warning/lỗi cho 4 module:
1. `mvm/build.sh` (EVM & NOMT C++ FFI)
2. `cargo build --release` (Consensus BFT Rust)
3. `mtn-nomt-ffi` (Rust NOMT Storage FFI)
4. `go build` (`simple_chain` Execution Engine)

---

### 5.4 Đồng bộ sang `metanode-suite` (update-ip.sh)
File script: `metanode-suite/scripts/update-ip/update-ip.sh`

```bash
# Tự động cập nhật RPC, WebSocket và TCP vào các file config của metanode-suite:
bash ../metanode-suite/scripts/update-ip/update-ip.sh --chain 991
```

---

## 📡 6. TRUY VẤN NHANH QUA CURL (JSON-RPC)

Sau khi node khởi động, có thể kiểm tra trực tiếp bằng 1 dòng lệnh:

```bash
# 1. Kiểm tra Block Height Parent Chain:
curl -s -X POST http://127.0.0.1:8547 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | jq

# 2. Kiểm tra Block Height Exec Cluster 1:
curl -s -X POST http://127.0.0.1:8646 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | jq

# 3. Kiểm tra EVM ChainID Cluster 1 (kết quả trả về: 991):
curl -s -X POST http://127.0.0.1:8646 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"net_version","params":[],"id":1}' | jq

# 4. Kiểm tra số dư ví Sequencer trên Cluster 1:
curl -s -X POST http://127.0.0.1:8646 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_getBalance","params":["0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5","latest"],"id":1}' | jq

# 5. Kiểm tra hàng đợi Inbound trên Parent Chain:
curl -s http://127.0.0.1:8547/inbound | jq
```

---

## 🖥️ 7. VẬN HÀNH SYSTEMD & THEO DÕI LOGS

### Quản lý dịch vụ qua Systemd:
```bash
# Xem trạng thái tất cả các node trong cụm:
sudo systemctl status metanode-parentchain metanode-exec1_replica1 metanode-exec1_replica2 metanode-exec1_replica3 metanode-exec2_replica1

# Khởi động lại riêng 1 node bị lỗi:
sudo systemctl restart metanode-exec1_replica1

# Tắt toàn bộ dịch vụ:
sudo systemctl stop metanode-parentchain metanode-exec1_replica* metanode-exec2_replica*
```

### Xem Log thời gian thực:
```bash
# Nếu chạy ở chế độ Systemd:
journalctl -u metanode-exec1_replica1.service -f
journalctl -u metanode-parentchain.service -f

# Nếu chạy ở chế độ Daemon:
tail -f /var/log/metanode/exec1_replica1.log
tail -f /var/log/metanode/parent_chain.log
```

---

## 🚨 8. XỬ LÝ NHANH SỰ CỐ (TROUBLESHOOTING)

1. **Lỗi trùng cổng (`Address already in use`):**
   ```bash
   # Tắt cưỡng chế các tiến trình cũ chiếm cổng:
   ./deploy_clusters.sh --stop
   sudo fuser -k 8547/tcp 8646/tcp 8647/tcp 4000/tcp 4200/tcp
   ```
2. **Lỗi thư mục làm việc Parent Chain (`os error 2`):**
   Tiến trình `parent_chain` yêu cầu working directory tương đối tại `execution/scripts/test`. Luôn sử dụng `./deploy_clusters.sh` để CWD được tự động gán chuẩn xác.
3. **Reset nhanh môi trường khi bị phân mảnh state:**
   ```bash
   ./deploy_clusters.sh --reset && ./deploy_clusters.sh --setup --test
   ```
