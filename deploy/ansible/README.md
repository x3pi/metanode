# Hệ Thống Triển Khai Metanode (Ansible Edition)

Hệ thống Ansible của Metanode là bộ công cụ quản trị tập trung, cho phép bạn triển khai, cấu hình và quản lý cả **Public Chain (Root Anchor - Chain 991)** lẫn **Mạng lưới Private Chains (Cross-Chain Ecosystem)** chỉ bằng 1 file cấu hình [`inventory.yml`](inventory.yml) và 1 script điều khiển duy nhất [`ansible_deploy.sh`](ansible_deploy.sh).

---

## 📑 Mục lục tài liệu
- **[Phần 1: Cẩm Nang Public Chain (Root Anchor - Chain 991)](#phần-1-cẩm-nang-public-chain-root-anchor---chain-991)**
- **[Phần 2: Cẩm Nang Private Chains (Cross-Chain Ecosystem)](#phần-2-cẩm-nang-private-chains-cross-chain-ecosystem)**
- **[Phần 3: Kiến Trúc Ansible Hoạt Động Như Thế Nào?](#phần-3-kiến-trúc-ansible-hoạt-động-như-thế-nào)**
- **[Phần 4: Hệ Thống Giám Sát & Công Cụ Tiện Ích](#phần-4-hệ-thống-giám-sát--công-cụ-tiện-ích)**

---

## Phần 1: Cẩm Nang Public Chain (Root Anchor - Chain 991)

Mặc định khi bạn chạy `./ansible_deploy.sh` (không truyền cờ `--private`), hệ thống sẽ thao tác trên nhóm **`metanode_cluster`** (Public Chain).

### 📌 Danh sách các cờ (Flags) cho Public Chain

| Tham số (Flag) | Giá trị Mặc định | Ý nghĩa & Tác dụng |
| --- | --- | --- |
| `--start` | Được gọi tự động | Khởi động Node, cập nhật binary mới nhất. **Giữ nguyên** Database và Keys. |
| `--reset-all` | N/A | Cài mới từ đầu. **Xóa sạch** Data cũ, đúc lại bộ Keys/Genesis mới tinh và chạy lại từ block 0. |
| `--stop` | N/A | Dừng toàn bộ các dịch vụ (Execution, Consensus, RPC Proxy) trên server. |
| `--clean` | `false` | Xóa sạch Database cũ nhưng giữ nguyên Keys/Genesis. Kết hợp: `./ansible_deploy.sh --start --clean`. |
| `--restart` | N/A | Khởi động lại nhanh systemd services (`systemctl restart`) mà không build hay copy lại file. |
| `--only-node N` | `all` | Chỉ thao tác (start, stop, reset) **duy nhất** trên Node số `N`. |
| `--restore-node N` | `none` | Khôi phục dữ liệu cho Node `N` từ Snapshot URL. |
| `--snapshot-url U` | `""` | Đường dẫn tải Snapshot (ví dụ: `http://192.168.1.230:8604`). |
| `--open-ports` | `false` | Mở tường lửa (UFW) tự động cho tất cả các cổng P2P, RPC, Consensus. |
| `--all-monitors` | `false` | Bật giám sát chéo (Mutual Cross-Monitoring) trên tất cả các server. |
| `--fast` | `false` | Biên dịch Rust ở chế độ debug để test nhanh. |

### 💡 Các lệnh Public Chain thông dụng
```bash
./ansible_deploy.sh --reset-all           # Cài mới sạch cụm Public Chain từ đầu
./ansible_deploy.sh --start               # Cập nhật binary và khởi động lại (giữ data)
./ansible_deploy.sh --stop                # Dừng toàn bộ cụm Public Chain
./ansible_deploy.sh --restart             # Restart nhanh toàn bộ node
./ansible_deploy.sh --only-node 0 --start # Chỉ khởi động riêng Node 0
```

---

## Phần 2: Cẩm Nang Private Chains (Cross-Chain Ecosystem)

Private Chains là mạng lưới các blockchain riêng biệt (Chain 101, 102, 103, 104,...) kết nối với Public Chain (Root Anchor) thông qua cổng Gateway và Relayer Daemon.

### 🌟 1. Cấu hình Private Chains trong `inventory.yml`

Mở file [`inventory.yml`](inventory.yml) và khai báo trong khối **`private_chains`**:

```yaml
all:
  vars:
    # URL RPC của Public Chain (Root Anchor)
    root_anchor_rpc: "http://192.168.1.233:10746"
    root_anchor_submitter_key: "d3d8157f2571153bcb664233f998a82b9b475fe509f92caf65ca2461bae7f1a9"
    ansible_user: "abc"
    ansible_ssh_pass: "1234@abcd"
    ansible_become_pass: "1234@abcd"

  children:
    # Cấu hình danh sách Private Chains
    private_chains:
      hosts:
        private_chain_101:
          ansible_host: 192.168.1.233
          ansible_connection: local   # Dùng local nếu chạy trên máy chủ hiện tại, hoặc ssh nếu qua mạng
          chain_id: 101               # ID định danh của Chain
          validators: 1               # Số validator node (Mặc định: 1)
          rpc_port: 8546              # Cổng RPC của chain
          port_offset: 10             # Độ lệch cổng để tránh xung đột trên cùng máy
          install_dir: "/opt/metanode/chain-101"

        private_chain_102:
          ansible_host: 192.168.1.233
          ansible_connection: local
          chain_id: 102
          validators: 1
          rpc_port: 8550
          port_offset: 20
          install_dir: "/opt/metanode/chain-102"

        private_chain_103:
          ansible_host: 192.168.1.233
          ansible_connection: local
          chain_id: 103
          validators: 1
          rpc_port: 8554
          port_offset: 30
          install_dir: "/opt/metanode/chain-103"

        private_chain_104:
          ansible_host: 192.168.1.233
          ansible_connection: local
          chain_id: 104
          validators: 1
          rpc_port: 8558
          port_offset: 40
          install_dir: "/opt/metanode/chain-104"
```

---

### 🚀 2. Quy trình 3 bước triển khai nhanh Private Chains

#### 🔹 Bước 1: Triển khai mới toàn bộ Private Chains (`--private --reset-all`)
Lệnh này sẽ tự động sinh cấu hình, tạo service, khởi chạy các chuỗi và **tự động đăng ký lên Gateway của Root Anchor**:
```bash
./ansible_deploy.sh --private --reset-all
```

#### 🔹 Bước 2: Kiểm tra trạng thái hoạt động (`--private --status`)
```bash
./ansible_deploy.sh --private --status
```
> Kết quả mong đợi: Cả 4 chuỗi đều báo `Block Number 0x1` (hoặc cao hơn).

#### 🔹 Bước 3: Khởi chạy Cross-Chain Relayer Daemon (`--relayer start`)
Khởi chạy Relayer ngầm trong phiên `tmux` để tự động chuyển tiếp giao dịch cross-chain giữa các chuỗi:
```bash
./ansible_deploy.sh --relayer start
```

---

### 📋 3. Bảng tra cứu lệnh Private Chains

| Nhu cầu thao tác | Lệnh thực thi |
| --- | --- |
| **Cài mới sạch tất cả Private Chains** | `./ansible_deploy.sh --private --reset-all` |
| **Khởi động tất cả Private Chains** | `./ansible_deploy.sh --private --start` |
| **Dừng tất cả Private Chains** | `./ansible_deploy.sh --private --stop` |
| **Restart tất cả Private Chains** | `./ansible_deploy.sh --private --restart` |
| **Xóa sạch DB (giữ nguyên Keys/Configs)** | `./ansible_deploy.sh --private --clean-data` |
| **Kiểm tra trạng thái RPC & Block** | `./ansible_deploy.sh --private --status` |
| **Chỉ khởi động riêng Chain 101** | `./ansible_deploy.sh --chain=101 --start` |
| **Chỉ reset riêng Chain 102** | `./ansible_deploy.sh --chain=102 --reset-all` |
| **Chỉ dừng riêng Chain 103** | `./ansible_deploy.sh --chain=103 --stop` |
| **Đăng ký lại tất cả chains lên Gateway** | `./ansible_deploy.sh --register` |
| **Đăng ký riêng 1 chain lên Gateway** | `./ansible_deploy.sh --register --chain=101` |

---

### 🌉 4. Quản lý Cross-Chain Relayer (Tmux Controller)

Script tích hợp sẵn cơ chế quản lý Relayer chạy ngầm độc lập:

```bash
./ansible_deploy.sh --relayer start     # Bật Relayer ngầm trong tmux
./ansible_deploy.sh --relayer status    # Kiểm tra tình trạng hoạt động và log mới nhất
./ansible_deploy.sh --relayer logs      # Theo dõi log realtime (tail -f)
./ansible_deploy.sh --relayer restart   # Khởi động lại Relayer
./ansible_deploy.sh --relayer stop      # Tắt Relayer và đóng session tmux
./ansible_deploy.sh --relayer attach    # Vào màn hình tmux (Thoát: Ctrl+B rồi bấm D)
```

---

### 📥 5. Kéo Log Private Chains về máy quản trị

```bash
./fetch_node_logs.sh --private               # Kéo log toàn bộ các Private Chains
./fetch_node_logs.sh --chain=101             # Chỉ kéo log của Chain 101
./fetch_node_logs.sh --chain=102 --lines=200 # Kéo 200 dòng log journalctl của Chain 102
```

---

### ⚙️ 6. Bảng công thức cổng (Port Formula) của Private Chains
Để nhiều Private Chain chạy cùng trên 1 máy chủ mà không bao giờ bị đụng độ cổng, mỗi chain được gán một **`port_offset`** riêng (10, 20, 30, 40...):

| Loại Cổng | Công thức tính | Ví dụ Chain 101 (`offset=10`) | Ví dụ Chain 102 (`offset=20`) |
| --- | --- | --- | --- |
| **RPC** | `rpc_port` | `8546` | `8550` |
| **Primary TCP** | `4200 + port_offset` | `4210` | `4220` |
| **Consensus P2P** | `10200 + port_offset` | `10210` | `10220` |
| **Meta RPC** | `11100 + port_offset` | `11110` | `11120` |
| **Metrics** | `12100 + port_offset` | `12110` | `12120` |
| **DNS Discovery** | `13000 + port_offset` | `13010` | `13020` |
| **Peer RPC** | `20200 + port_offset` | `20210` | `20220` |

---

## Phần 3: Kiến Trúc Ansible Hoạt Động Như Thế Nào?

**Ansible** là một công cụ "Quản lý Cấu hình và Triển khai" cực kỳ mạnh mẽ. Hệ thống triển khai của Metanode bao gồm 2 Playbook chuyên biệt:
- **`deploy.yml`**: Dành cho cụm Public Chain với kiến trúc 8 Roles tuần tự (`local_build`, `stop_services`, `clean_data`, `node_setup`, `snapshot_restore`, `node_exporter`, `systemd_services`, `restart_services`).
- **`deploy_private.yml`**: Dành riêng cho Private Chains với role gọn nhẹ `roles/private_node`, cho phép sinh cấu hình chuẩn hóa tự động và quản lý độc lập từng chain.

---

## Phần 4: Hệ Thống Giám Sát & Công Cụ Tiện Ích

Bộ công cụ Ansible deploy đi kèm bộ giám sát (Monitors) chạy ngầm nội bộ độc lập hoàn toàn, hỗ trợ giám sát sức khỏe cụm node và tính nhất quán của chuỗi khối.

### 1. Bộ Giám Sát (Monitors)
Bộ giám sát nằm tại thư mục [`monitors/`](monitors/) bao gồm:
- **Health Monitor** (`start_monitors.sh health`): Liên tục kiểm tra các endpoint RPC của các node. Nếu phát hiện node chết, tự động kéo thư mục logs bị crash về máy phát hiện và gửi cảnh báo đỏ lên Telegram.
- **Resource Monitor** (`start_monitors.sh resources`): Kiểm tra RAM, CPU, Disk usage trên toàn bộ Server định kỳ mỗi 5 phút.
- **Block Hash Checker** (`block_hash_checker`): Liên tục so sánh chiều cao block, hash, stateRoot giữa các node để phát hiện phân nhánh.

### 2. Các Lệnh Quản Lý Monitor Thủ Công
- **Bật monitor trên máy hiện tại:**
  ```bash
  cd deploy/ansible/monitors
  ./start_monitors.sh
  ```
- **Bật monitor chéo trên TẤT CẢ Server:**
  ```bash
  cd deploy/ansible/monitors
  ./start_monitors.sh --all-hosts
  ```
- **Dừng toàn bộ monitor:**
  ```bash
  cd deploy/ansible/monitors
  ./start_monitors.sh --stop-all
  ```

### 3. Dừng các tiến trình nền (`stop_all.sh`)
```bash
./stop_all.sh           # Tắt monitors & watcher daemon
./stop_all.sh --cluster # Tắt monitors, watcher và dừng cả cụm Node từ xa
```
