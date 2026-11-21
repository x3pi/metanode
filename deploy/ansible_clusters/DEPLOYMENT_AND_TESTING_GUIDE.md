# 🚀 HƯỚNG DẪN TRIỂN KHAI & VẬN HÀNH CỤM METANODE

Sổ tay hướng dẫn triển khai, vận hành, kiểm thử và quản lý cụm **Parent Chain & Sharded Execution Clusters (Child Chains)** bằng Ansible.

---

## ⚡ 1. CÚ PHÁP TỔNG QUÁT & BẢNG TRA CỨU LỆNH NHANH

Mọi lệnh đều thực thi tại thư mục: `deploy/ansible_clusters`

```bash
./deploy_clusters.sh [HÀNH ĐỘNG] [-i file_inventory] [CỜ BỔ TRỢ]
```

### 🚩 Bảng Cờ Bổ Trợ Phổ Biến (Flags):
| Cờ (Flag) | Ý Nghĩa Kỹ Thuật | Ví Dụ |
| :--- | :--- | :--- |
| **`-i <file>` / `--inventory=<file>`** | **Chỉ định file inventory khác** (mặc định dùng `inventory.yml`) | `-i inventory.prod.yml` |
| **`--rolling`** | **Rolling restart từng node 1** cho Child Chain (Zero-Downtime, giữ Raft quorum) | `--restart --rolling` |
| **`--skip-build`** | Bỏ qua bước cargo/go build, chỉ restart dịch vụ với binary hiện có | `--restart --skip-build` |
| **`--exec-only`** | Chỉ thao tác trên các Chain con (Execution Clusters), bỏ qua Parent Chain | `--restart --exec-only` |
| **`--parent-only`** | Chỉ thao tác trên Parent Chain (chỉ dừng/bật/restart Parent Chain & monitor của nó) | `--stop --parent-only` |
| **`-n <node>` / `--node=<node>`** | Chỉ thao tác trên đúng 1 node cụ thể (vd: `exec1_r1`, `p0`, `exec2_r1`) | `--restart -n exec1_r1` |
| **`--test`** | Tự động kích hoạt bộ 11 kịch bản kiểm thử E2E ngay sau khi xong | `--restart --test` |

---

### 📋 Bảng Lệnh Thực Dụng Thường Dùng:

| Thao Tác | Lệnh Mặc Định (`inventory.yml`) | Lệnh Với Inventory Khác (`-i`) |
| :--- | :--- | :--- |
| **Cài đặt & chạy từ đầu** | `./deploy_clusters.sh --setup` | `./deploy_clusters.sh --setup -i inventory.prod.yml` |
| **Cài đặt & chạy kèm Test** | `./deploy_clusters.sh --setup --test` | `./deploy_clusters.sh --setup --test -i inventory.prod.yml` |
| **Khởi động lại toàn bộ** | `./deploy_clusters.sh --restart` | `./deploy_clusters.sh --restart -i inventory.prod.yml` |
| **🔄 Rolling Restart Child Chain** | `./deploy_clusters.sh --restart --rolling` | `./deploy_clusters.sh --restart --rolling -i inventory.prod.yml` |
| **Restart nhanh (không build)** | `./deploy_clusters.sh --restart --skip-build` | `./deploy_clusters.sh --restart --skip-build -i inventory.prod.yml` |
| **Rolling Restart nhanh (không build)** | `./deploy_clusters.sh --restart --rolling --skip-build` | `./deploy_clusters.sh --restart --rolling --skip-build -i inventory.prod.yml` |
| **Chỉ restart Chain con** | `./deploy_clusters.sh --restart --exec-only` | `./deploy_clusters.sh --restart --exec-only -i inventory.prod.yml` |
| **Chỉ restart Parent Chain** | `./deploy_clusters.sh --restart --parent-only` | `./deploy_clusters.sh --restart --parent-only -i inventory.prod.yml` |
| **Dừng riêng Parent Chain** | `./deploy_clusters.sh --stop --parent-only` | `./deploy_clusters.sh --stop --parent-only -i inventory.prod.yml` |
| **Dừng riêng Chain con** | `./deploy_clusters.sh --stop --exec-only` | `./deploy_clusters.sh --stop --exec-only -i inventory.prod.yml` |
| **Kiểm tra trạng thái cụm** | `./deploy_clusters.sh --status` | `./deploy_clusters.sh --status -i inventory.prod.yml` |
| **Chỉ chạy bộ kiểm thử E2E** | `./deploy_clusters.sh --test-only` | `./deploy_clusters.sh --test-only -i inventory.prod.yml` |
| **Dừng an toàn toàn bộ cụm** | `./deploy_clusters.sh --stop` | `./deploy_clusters.sh --stop -i inventory.prod.yml` |
| **Reset về Block 0 (Clean DB)** | `./deploy_clusters.sh --reset` | `./deploy_clusters.sh --reset -i inventory.prod.yml` |
| **Rollback binary trước đó** | `./deploy_clusters.sh --rollback` | `./deploy_clusters.sh --rollback -i inventory.prod.yml` |

---

## 🎯 2. HƯỚNG DẪN DÙNG FILE INVENTORY KHÁC (`-i` HOẶC `--inventory`)

Khi bạn muốn triển khai trên server thật (Production), mạng thử nghiệm (Testnet), hay môi trường khác:

1. **Tạo file inventory mới:** Ví dụ `inventory.prod.yml` (hoặc copy từ `inventory.example.yml`).
2. **Chạy bất kỳ lệnh nào với cờ `-i`:**
   ```bash
   # Ví dụ 1: Setup từ đầu cụm Production:
   ./deploy_clusters.sh --setup -i inventory.prod.yml

   # Ví dụ 2: Rolling Restart không gián đoạn dịch vụ:
   ./deploy_clusters.sh --restart --rolling -i inventory.prod.yml

   # Ví dụ 3: Kiểm tra block height và RPC trên cụm Production:
   ./deploy_clusters.sh --status -i inventory.prod.yml
   ```

### 💡 Lưu ý quan trọng khi dùng inventory khác:
* **Mật khẩu Vault (`.vault_pass`):** Script tự động dùng `.vault_pass` hiện tại để giải mã. Mọi chuỗi mã hóa (`ansible_become_pass`, `parent_chain_rpc_token`, private keys) trong inventory mới phải được mã hóa cùng mật khẩu của `.vault_pass`.
* **Môi trường Production (`metanode_env: production`):** Tuyệt đối không để mật khẩu dạng plaintext trong inventory.
* **Tự động cách ly Monitor:** Mỗi inventory sẽ tự động tạo một nhóm monitor và namespace độc lập (theo tên file inventory), không bị đè log hay PID của nhau.

---

## 🎯 3. THAO TÁC TRÊN TỪNG NODE CHỈ ĐỊNH (`-n` HOẶC `--node=`)

Khi bạn muốn kiểm tra khả năng chịu lỗi (failover), bảo trì, hoặc bật/tắt riêng một node cụ thể mà không ảnh hưởng tới toàn bộ mạng:

### 📌 Danh sách tên node (Aliases) được hỗ trợ:
- **Child Chain 1 (Exec 1):** `exec1_r1`, `exec1_r2`, `exec1_r3` (hoặc viết tắt: `r1`, `r2`, `r3`). Cả cụm: `exec1`.
- **Child Chain 2 (Exec 2):** `exec2_r1`, `exec2_r2`, `exec2_r3` (hoặc viết tắt: `r4`, `r5`, `r6`). Cả cụm: `exec2`.
- **Parent Chain:** `p0`, `p1`, `p2`, `p3` (hoặc `parent_node_0` .. `parent_node_3`). Cả cụm: `parent`.

---

### 📋 Các lệnh mẫu thực tế:

#### 1. Dừng riêng 1 node (Stop Single Node):
```bash
# Dừng replica 1 của Chain con 1:
./deploy_clusters.sh --stop -n exec1_r1

# Dừng replica 3 của Chain con 1:
./deploy_clusters.sh --stop -n exec1_r3

# Dừng 1 validator của Parent Chain:
./deploy_clusters.sh --stop -n p0

# Dừng 1 node trên inventory Production:
./deploy_clusters.sh --stop -n exec1_r1 -i inventory.prod.yml
```
> *(Hệ thống tự động đánh dấu tắt monitor cho node bị dừng để không spam cảnh báo Telegram, trong khi các node còn lại vẫn được giám sát bình thường).*

#### 2. Bật lại riêng 1 node (Start Single Node):
```bash
# Bật lại replica 1 của Chain con:
./deploy_clusters.sh --start -n exec1_r1

# Bật lại validator Parent Chain:
./deploy_clusters.sh --start -n p0

# Bật lại trên inventory khác:
./deploy_clusters.sh --start -n exec1_r1 -i inventory.prod.yml
```

#### 3. Khởi động lại riêng 1 node (Restart Single Node):
```bash
# Khởi động lại 1 replica (kèm --skip-build để không build lại):
./deploy_clusters.sh --restart -n exec1_r1 --skip-build

# Khởi động lại 1 validator Parent Chain:
./deploy_clusters.sh --restart -n p0 --skip-build
```

---

## 🔄 4. CƠ CHẾ ROLLING RESTART CHO CHAIN CON (`--rolling`)

### Tại sao nên dùng `--rolling`?
Khi chạy `./deploy_clusters.sh --restart --rolling`:
1. **Parent Chain:** Được restart đồng loạt trước để cập nhật và sẵn sàng route RPC (Rust BFT yêu cầu đồng bộ toàn cụm).
2. **Child Chain (Execution Clusters):** Được restart **lần lượt từng node 1 (`serial: 1`)**.
   - **Zero Downtime:** Cụm Child Chain chạy đồng thuận **Raft HA (3 node, quorum 2)**. Khi node 1 tắt và bật lại, 2 node còn lại vẫn hoạt động, nhận RPC và tạo block bình thường.
   - **Chống sập cụm (Fail-Stop Blast Radius):** Nếu code mới có lỗi nghiêm trọng (panic lúc khởi động), node 1 thất bại tại Gate G1-G3 ➔ Ansible dừng ngay lập tức. **Node 2 và Node 3 vẫn tiếp tục chạy bình thường bằng binary cũ**, bảo vệ hệ sinh thái không bị sập hoàn toàn.

---

## 🛡️ 5. CƠ CHẾ DỪNG ĐỘC LẬP & CÁC CỔNG KIỂM TRA SỨC KHỎE

### 📌 Dừng độc lập từng chuỗi (Parent Chain vs Child Chain):
- **Khi dừng riêng Parent Chain (`--stop --parent-only`):**
  - Hệ thống **chỉ dừng các node Parent Chain và monitor của Parent Chain**.
  - Toàn bộ các node và monitor ngầm của **Execution Clusters (Child Chains) vẫn tiếp tục chạy bình thường 100%**.
- **Khi dừng riêng Child Chain (`--stop --exec-only`):**
  - Chỉ dừng các node Child Chain và monitor tương ứng, Parent Chain và monitor của nó vẫn chạy bình thường.

### 📌 5 Cổng kiểm tra sức khỏe tự động (Health Gates G1 - G5):
- **Gate G1 (Process):** `systemctl is-active` = `active`.
- **Gate G2 (RPC Ready):** Cổng RPC phản hồi HTTP 200.
- **Gate G3 (Height Check):** `eth_consensusReady == true` và `blockNumber >= pre_stop_height` (đảm bảo không bị lùi block hoặc mất dữ liệu).
- **Gate G4 (Anti-Fork):** Kiểm tra hash block tại `min_height` giữa các peer trong cluster để loại bỏ nguy cơ chia nhánh mạng.
- **Gate G5 (Binary Checksum):** Xác thực SHA-256 binary trên máy chủ khớp với bản build tại controller.

---

## 🔐 6. HƯỚNG DẪN MÃ HÓA ANSIBLE VAULT

Nếu bạn tạo inventory mới hoặc đổi mật khẩu:

```bash
cd deploy/ansible_clusters

# 1. Tạo file .vault_pass (chỉ cần làm 1 lần):
echo "mat_khau_vault_cua_ban" > .vault_pass
chmod 600 .vault_pass

# 2. Mã hóa mật khẩu sudo của máy chủ:
ansible-vault encrypt_string 'mat_khau_sudo_that' --name ansible_become_pass

# 3. Mã hóa token RPC Parent Chain:
ansible-vault encrypt_string "$(openssl rand -hex 32)" --name parent_chain_rpc_token

# 4. Copy các khối "!vault | ..." dán vào mục all.vars trong file inventory.
```

---

## 🧪 7. BỘ 11 KỊCH BẢN KIỂM THỬ TÍCH HỢP (E2E TESTS)

Khi chạy `./deploy_clusters.sh --test` hoặc `./deploy_clusters.sh --test-only`, hệ thống tự động kiểm tra 11 kịch bản thực tế:
1. **Kịch bản 1:** Đăng ký tài khoản mới vào Account Registry trên Parent Chain.
2. **Kịch bản 2:** Nạp tiền Float & ghi nhận số dư trên Child Chain (`ReceiveWorker`).
3. **Kịch bản 3:** Thực thi Smart Contract EVM nội bộ trên Exec 1.
4. **Kịch bản 4:** Chuyển tiền xuyên 2 cụm node (Exec 1 ➔ Exec 2 qua Float Transfers).
5. **Kịch bản 5:** Khả năng hoạt động độc lập khi Parent Chain offline.
6. **Kịch bản 6:** Tự động khôi phục và xử lý tiếp giao dịch liên cụm khi Parent Chain online lại.
7. **Kịch bản 7:** Gọi Smart Contract xuyên 2 cụm node qua cầu nối.
8. **Kịch bản 8:** Chịu lỗi khi mất 1/4 node Parent Chain (vẫn đủ quorum 3/4).
9. **Kịch bản 9:** Chịu lỗi mất Quorum Parent Chain (pending an toàn, không tạo tiền / zero-fork).
10. **Kịch bản 10:** Recipient Gate & phân quyền tài khoản đăng ký.
11. **Kịch bản 11:** Giao dịch nội bộ offline & xử lý tranh chấp đa cụm (Reconciliation).

---

## 🖥️ 8. QUẢN LÝ DỊCH VỤ SYSTEMD & LOGS

```bash
# Xem trạng thái service:
sudo systemctl status metanode-parent_node_0
sudo systemctl status metanode-exec1_replica1

# Xem log thời gian thực:
journalctl -u metanode-exec1_replica1.service -f
journalctl -u metanode-parent_node_0.service -f

# Khởi động lại riêng 1 node qua systemd:
sudo systemctl restart metanode-exec1_replica1
```
