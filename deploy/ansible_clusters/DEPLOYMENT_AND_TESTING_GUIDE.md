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
| **`-i <file>` / `--inventory=<file>`** | **Chỉ định file inventory khác** (mặc định dùng `inventory.yml`) | `-i inventory_cloud.yml` |
| **`--rolling`** | **Rolling restart từng node 1** cho Child Chain (Zero-Downtime, giữ Raft quorum) | `--restart --rolling` |
| **`--skip-build`** | Bỏ qua bước cargo/go build, chỉ restart dịch vụ với binary hiện có | `--restart --skip-build` |
| **`--exec-only`** | Chỉ thao tác trên các Chain con (Execution Clusters), bỏ qua Parent Chain | `--restart --exec-only` |
| **`--parent-only`** | Chỉ thao tác trên Parent Chain (chỉ dừng/bật/restart Parent Chain & monitor của nó) | `--stop --parent-only` |
| **`-n <node>` / `--node=<node>`** | Chỉ thao tác trên đúng 1 node cụ thể (vd: `pri_1a`, `exec1_r1`, `p0`) | `--restart -n pri_1a -i inventory_cloud.yml` |
| **`--check-blocks` / `--blocks`** | **Kiểm tra nhanh trạng thái online (Parent) & Block Height (Child Chain)** | `--check-blocks -i inventory_cloud.yml` |
| **`--test`** | Tự động kích hoạt bộ 11 kịch bản kiểm thử E2E ngay sau khi xong | `--restart --test` |

---

### 📋 Bảng Lệnh Thực Dụng Thường Dùng:

| Thao Tác | Lệnh Mặc Định (`inventory.yml`) | Lệnh Cụm Cloud (`-i inventory_cloud.yml`) |
| :--- | :--- | :--- |
| **Kiểm tra nhanh Block Height (Child) & Online (Parent)** | `./deploy_clusters.sh --check-blocks` | `./deploy_clusters.sh --check-blocks -i inventory_cloud.yml` |
| **Kiểm tra trạng thái cụm toàn diện** | `./deploy_clusters.sh --status` | `./deploy_clusters.sh --status -i inventory_cloud.yml` |
| **🔄 Rolling Restart Child Chain (Zero-Downtime)** | `./deploy_clusters.sh --restart --rolling` | `./deploy_clusters.sh --restart --rolling -i inventory_cloud.yml` |
| **Khởi động lại riêng 1 node** | `./deploy_clusters.sh --restart -n exec1_r1` | `./deploy_clusters.sh --restart -n pri_1a -i inventory_cloud.yml` |
| **Dừng riêng 1 node** | `./deploy_clusters.sh --stop -n exec1_r1` | `./deploy_clusters.sh --stop -n pri_1a -i inventory_cloud.yml` |
| **Bật lại riêng 1 node** | `./deploy_clusters.sh --start -n exec1_r1` | `./deploy_clusters.sh --start -n pri_1a -i inventory_cloud.yml` |
| **Cài đặt & chạy từ đầu** | `./deploy_clusters.sh --setup` | `./deploy_clusters.sh --setup -i inventory_cloud.yml` |
| **Cài đặt & chạy kèm Test** | `./deploy_clusters.sh --setup --test` | `./deploy_clusters.sh --setup --test -i inventory_cloud.yml` |
| **Khởi động lại toàn bộ cụm** | `./deploy_clusters.sh --restart` | `./deploy_clusters.sh --restart -i inventory_cloud.yml` |
| **Restart nhanh (không build lại)** | `./deploy_clusters.sh --restart --skip-build` | `./deploy_clusters.sh --restart --skip-build -i inventory_cloud.yml` |
| **Rolling Restart nhanh (không build)** | `./deploy_clusters.sh --restart --rolling --skip-build` | `./deploy_clusters.sh --restart --rolling --skip-build -i inventory_cloud.yml` |
| **Chỉ restart Chain con (Child Chain)** | `./deploy_clusters.sh --restart --exec-only` | `./deploy_clusters.sh --restart --exec-only -i inventory_cloud.yml` |
| **Chỉ restart Parent Chain** | `./deploy_clusters.sh --restart --parent-only` | `./deploy_clusters.sh --restart --parent-only -i inventory_cloud.yml` |
| **Dừng riêng Parent Chain** | `./deploy_clusters.sh --stop --parent-only` | `./deploy_clusters.sh --stop --parent-only -i inventory_cloud.yml` |
| **Dừng riêng Chain con** | `./deploy_clusters.sh --stop --exec-only` | `./deploy_clusters.sh --stop --exec-only -i inventory_cloud.yml` |
| **Chỉ chạy bộ kiểm thử E2E** | `./deploy_clusters.sh --test-only` | `./deploy_clusters.sh --test-only -i inventory_cloud.yml` |
| **Dừng an toàn toàn bộ cụm** | `./deploy_clusters.sh --stop` | `./deploy_clusters.sh --stop -i inventory_cloud.yml` |
| **Reset về Block 0 (Clean DB)** | `./deploy_clusters.sh --reset` | `./deploy_clusters.sh --reset -i inventory_cloud.yml` |
| **Rollback binary trước đó** | `./deploy_clusters.sh --rollback` | `./deploy_clusters.sh --rollback -i inventory_cloud.yml` |

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
- **Cụm Local (`inventory.yml`):**
  - **Child Chain 1 (Exec 1):** `exec1_r1`, `exec1_r2`, `exec1_r3` (hoặc viết tắt: `r1`, `r2`, `r3`). Cả cụm: `exec1`.
  - **Child Chain 2 (Exec 2):** `exec2_r1`, `exec2_r2`, `exec2_r3` (hoặc viết tắt: `r4`, `r5`, `r6`). Cả cụm: `exec2`.
  - **Parent Chain:** `p0`, `p1`, `p2`, `p3` (hoặc `parent_node_0` .. `parent_node_3`). Cả cụm: `parent`.
- **Cụm Cloud Server (`inventory_cloud.yml`):**
  - **Child Chain (`cluster_cloud`):** `pri_1a`, `pri_1b`, `pri_1c`, `pri_1d`, `pri_1e` (hoặc theo IP: `61.28.233.146`, `116.118.92.254`, `49.213.71.191`, `61.28.238.209`).
  - **Parent Chain:** `parent_prod_0`, `parent_prod_1`, `parent_prod_2`, `parent_prod_3` (hoặc `p0` .. `p3`).

---

### 📋 Các lệnh mẫu thực tế:

#### 1. Dừng riêng 1 node (Stop Single Node):
```bash
# Dừng replica 1 của Chain con (inventory.yml):
./deploy_clusters.sh --stop -n exec1_r1

# Dừng node pri_1a trên cụm Cloud (inventory_cloud.yml):
./deploy_clusters.sh --stop -n pri_1a -i inventory_cloud.yml
```
> *(Hệ thống tự động đánh dấu tắt monitor cho node bị dừng để không spam cảnh báo Telegram, trong khi các node còn lại vẫn được giám sát bình thường).*

#### 2. Bật lại riêng 1 node (Start Single Node):
```bash
# Bật lại replica 1 của Chain con:
./deploy_clusters.sh --start -n exec1_r1

# Bật lại node pri_1a trên cụm Cloud:
./deploy_clusters.sh --start -n pri_1a -i inventory_cloud.yml
```

#### 3. Khởi động lại riêng 1 node (Restart Single Node):
```bash
# Khởi động lại 1 replica Chain con (kèm --skip-build để không build lại):
./deploy_clusters.sh --restart -n exec1_r1 --skip-build

# Khởi động lại riêng node pri_1a trên cụm Cloud (bỏ qua build):
./deploy_clusters.sh --restart -n pri_1a -i inventory_cloud.yml --skip-build
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
- **Gate G2 (RPC Ready):** Cổng RPC phản hồi HTTP 200 (chờ tối đa `gate_rpc_timeout`, mặc định `180` giây, để kịp replay DB/WAL sau crash hoặc lag; trả về ngay khi cổng mở).
- **Gate G3 (Height Check):** `eth_consensusReady == true` và `blockNumber >= pre_stop_height` (đảm bảo không bị lùi block hoặc mất dữ liệu).
- **Gate G4 (Anti-Fork):** Kiểm tra hash block tại `min_height` giữa các peer trong cluster để loại bỏ nguy cơ chia nhánh mạng.
- **Gate G5 (Binary Checksum):** Xác thực SHA-256 binary trên máy chủ khớp với bản build tại controller.

#### Tùy chọn điều chỉnh / bỏ qua Health Gates (`-e`):
- **`-e gate_rpc_timeout=<giây>`** *(mặc định `180`)*: Tăng hoặc giảm thời gian tối đa chờ cổng RPC mở ở Gate G2 (áp dụng cho cả Parent Chain và Child Chain). Tương tự có `gate_ready_retries` / `gate_ready_delay` cho Gate G3 và `gate_hash_retries` / `gate_hash_delay` cho Gate G4.
- **`-e skip_health_gates=true`** *(chỉ áp dụng cho Child Chain / `exec_cluster`)*: Chỉ kiểm tra **Gate G1** rồi bỏ qua **Gates G2–G4** (vẫn in cảnh báo chưa kiểm tra fork/height) để node đang lag có thể tiếp tục sync ngầm.
  > **Lưu ý an toàn:** Không được kết hợp `skip_health_gates=true` với `--rolling` (`rolling_exec=true`) vì service dùng `Type=simple`, G1 không chứng minh node đã sẵn sàng nên rolling tiếp sẽ làm mất quorum Raft. Biến này phải truyền toàn cục qua `-e`, không đặt riêng lẻ theo từng host.

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

---

## ☁️ 9. HƯỚNG DẪN QUẢN TRỊ & KIỂM TRA BLOCK CỤM CLOUD (`inventory_cloud.yml`)

File cấu hình [inventory_cloud.yml](file:///home/abc/nhat/con-chain-v2/metanode/deploy/ansible_clusters/inventory_cloud.yml) quản lý cụm mạng hỗn hợp gồm:
- **Parent Chain (4 node LAN):** `parent_prod_0` .. `parent_prod_3` (IP `192.168.1.234`, `192.168.1.231`, `192.168.1.230`).
- **Child Chain (`cluster_cloud` - 5 node Raft HA):**
  - `pri_1a`: `http://61.28.233.146:8750` (Node 1 - Leader/Sequencer)
  - `pri_1b`: `http://116.118.92.254:8750` (Node 2)
  - `pri_1c`: `http://49.213.71.191:8750` (Node 3)
  - `pri_1d`: `http://61.28.238.209:8750` (Node 4 - Tiệm net qua FRP)
  - `pri_1e`: `http://61.28.238.209:8751` (Node 5 - Tiệm net qua FRP)

---

### 🔍 Các lệnh kiểm tra Block Height các Node Child Chain:

#### Cách 1: Dùng lệnh chính thức của Ansible CLI (Khuyên dùng)
```bash
# Kiểm tra nhanh trạng thái online (Parent) & Block Height (Child Chain):
./deploy_clusters.sh --check-blocks -i inventory_cloud.yml

# Hoặc dùng alias ngắn gọn:
./deploy_clusters.sh --blocks -i inventory_cloud.yml

# Kiểm tra trạng thái toàn diện (Block, Endpoint RPC/WS/TLS, và Monitor ngầm):
./deploy_clusters.sh --status -i inventory_cloud.yml
```
*Kết quả mẫu hiển thị:*
```text
🔍 Live Service Health Status (Live RPC Check):
  • parent_prod_0 (http://192.168.1.234:28601): ✅ ONLINE (HTTP RPC OK)
  • parent_prod_1 (http://192.168.1.231:28602): ✅ ONLINE (HTTP RPC OK)
  • parent_prod_2 (http://192.168.1.230:28603): ✅ ONLINE (HTTP RPC OK)
  • parent_prod_3 (http://192.168.1.231:28604): ✅ ONLINE (HTTP RPC OK)
  • pri_1a (http://61.28.233.146:8750): ✅ ONLINE (Block: 71250 / 0x11652)
  • pri_1b (http://116.118.92.254:8750): ✅ ONLINE (Block: 71250 / 0x11652)
  • pri_1c (http://49.213.71.191:8750): ✅ ONLINE (Block: 71250 / 0x11652)
  • pri_1d (http://61.28.238.209:8750): ✅ ONLINE (Block: 71250 / 0x11652)
  • pri_1e (http://61.28.238.209:8751): ✅ ONLINE (Block: 71250 / 0x11652)
```

#### Cách 2: Lệnh curl loop nhanh (Chạy ngay, không phụ thuộc Ansible)
Lệnh quét trực tiếp cổng RPC của cả 5 node Child Chain:
```bash
for node in "pri_1a:http://61.28.233.146:8750" "pri_1b:http://116.118.92.254:8750" "pri_1c:http://49.213.71.191:8750" "pri_1d:http://61.28.238.209:8750" "pri_1e:http://61.28.238.209:8751"; do
    name="${node%%:*}"
    url="${node#*:}"
    res=$(curl -s -m 2 -X POST "$url" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}')
    hex=$(echo "$res" | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
    if [ -n "$hex" ]; then
        printf "  • %-7s (%s): Block %d (%s)\n" "$name" "$url" "$((16#${hex#0x}))" "$hex"
    else
        printf "  • %-7s (%s): ❌ OFFLINE\n" "$name" "$url"
    fi
done
```

#### Cách 3: Theo dõi Block nhảy theo thời gian thực (Live Watch)
Cập nhật trạng thái & block height mỗi 1 giây:
```bash
watch -n 1 ./deploy_clusters.sh --check-blocks -i inventory_cloud.yml
```
*(Hoặc nếu dùng vòng lặp `curl` trực tiếp với `watch`, hãy dùng cờ `-x` để chạy qua `bash -c` thay vì `sh -c` (`dash`), vì `dash` không hỗ trợ cú pháp `$((16#...))` của Bash:)*
```bash
watch -n 1 -x bash -c 'for u in "http://61.28.233.146:8750" "http://116.118.92.254:8750" "http://49.213.71.191:8750" "http://61.28.238.209:8750" "http://61.28.238.209:8751"; do hex=$(curl -s -m 2 -X POST "$u" -H "Content-Type: application/json" -d "{\"jsonrpc\":\"2.0\",\"method\":\"eth_blockNumber\",\"params\":[],\"id\":1}" | grep -o "\"result\":\"[^\"]*\"" | cut -d"\"" -f4); if [ -n "$hex" ]; then printf "%-28s : Block %d\n" "$u" "$((hex))"; else printf "%-28s : OFFLINE\n" "$u"; fi; done'
```

---

### 🛠️ Các lệnh vận hành thường dùng cho `inventory_cloud.yml`:

```bash
# 1. Rolling Restart từng node Child Chain (Zero-Downtime, không dừng mạng):
./deploy_clusters.sh --restart --rolling -i inventory_cloud.yml

# 2. Khởi động lại riêng node pri_1a (nhanh, bỏ qua build):
./deploy_clusters.sh --restart -n pri_1a -i inventory_cloud.yml --skip-build

# 3. Dừng an toàn node pri_1a để bảo trì:
./deploy_clusters.sh --stop -n pri_1a -i inventory_cloud.yml

# 4. Bật lại node pri_1a:
./deploy_clusters.sh --start -n pri_1a -i inventory_cloud.yml

# 5. Khởi động lại riêng toàn bộ Child Chain (bỏ qua Parent Chain):
./deploy_clusters.sh --restart --exec-only -i inventory_cloud.yml --skip-build

./deploy_clusters.sh --restart --parent-only  -i inventory_cloud.yml 
```
