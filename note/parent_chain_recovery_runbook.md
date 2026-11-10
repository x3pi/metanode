# 📘 Sổ Tay Phục Hồi Cụm Parent Chain (Parent Chain Recovery Runbook)

> **Mục tiêu:** Hướng dẫn chi tiết quy trình ứng cứu sự cố và khôi phục cụm Parent Chain 4-node khi gặp lỗi kẹt đồng thuận BFT, lệch khóa/genesis hoặc khi chủ động thực hiện xoay khóa (key rotation) / reset cụm.
> **Quy tắc bất khả xâm phạm:** Tuân thủ triệt để nguyên tắc **ZERO-FORK** (thà pending chứ không fork), không dùng sleep/timeout để quyết định dispatch, và không được xóa DAG đồng thời trên toàn bộ các node.

---

## 1. Bản chất sự cố và cảnh báo an toàn

### 1.1. Nguyên nhân gốc của sự cố kẹt đồng thuận (Consensus Deadlock)
Vào sự cố ngày 2026-11-02, khóa Parent Chain tại `deploy/ansible_clusters/parent_keys` bị sinh lại nhưng lệnh chạy sau đó là `./deploy_clusters.sh --restart`:
- Lệnh `--restart` thực hiện render lại cấu hình `node.toml`, `parent_genesis.json` và phân phối lại các cặp khóa mới (`node_N_protocol_key.json`, `node_N_network_key.json`) sang các server.
- Tuy nhiên, lệnh `--restart` **KHÔNG** xóa dữ liệu DAG lịch sử trong thư mục `consensus/` và state Go trong `parentchain_db/`.
- Hậu quả: Các node khởi động lại với cặp khóa và genesis mới, nhưng đọc lại các block/commit cũ trong DAG được ký bởi cặp khóa cũ. Rust BFT engine liên tục từ chối chữ ký với lỗi:
  `Failed to verify the block's signature: Invalid signature`
  dẫn đến kích hoạt `DEADLOCK-BREAKER` liên tục và kẹt hoàn toàn tại độ cao commit cũ.

### 1.2. Cảnh báo quan trọng về Deadlock khởi động nguội (Cold Start Deadlock)
- **TUYỆT ĐỐI KHÔNG** xóa thư mục `consensus/` đồng thời trên cả 4 node khi state Go (`parentchain_db/`) vẫn còn giữ dữ liệu block cao hơn.
- Nếu xóa sạch DAG trên cả 4 node, Rust BFT engine khởi động lại từ round 0 / block 0, nhưng Go state engine nhận thấy commit 0 đã được thực thi và không thể đồng bộ ngược chiều, gây ra deadlock khởi động nguội vĩnh viễn.

---

## 2. Tình huống A: Lỡ sinh khóa mới hoặc sửa genesis trên cụm đang có dữ liệu

Áp dụng khi cụm đang chạy bình thường nhưng có thao tác nhầm lẫn làm thay đổi file trong `parent_keys/` hoặc sửa `parent_genesis.json` dẫn đến lỗi `Invalid signature` hoặc `genesis committee fingerprint mismatch`.

### Bước 1: Dừng khẩn cấp toàn bộ 4 node Parent Chain
Ngăn chặn các node tiếp tục ghi đè trạng thái hỏng hoặc gửi vote rác qua P2P:
```bash
# Dừng an toàn qua Ansible (hoặc ssh vào từng server kill theo PID / systemctl)
cd deploy/ansible_clusters
./deploy_clusters.sh --stop
```
Kiểm tra đảm bảo toàn bộ tiến trình `parent_chain` đã dừng:
```bash
pgrep -fl parent_chain || echo "Toàn bộ node đã dừng"
```

### Bước 2: Sao lưu (Backup) toàn bộ thư mục dữ liệu trên cả 4 node
Trước khi thực hiện bất kỳ thao tác can thiệp nào, bắt buộc tạo bản sao lưu snapshot nguyên trạng:
```bash
# Thực hiện trên mỗi server node (ví dụ node 0, 1, 2, 3):
DATA_DIR="/opt/metanode/parent_chain_0" # thay theo thư mục từng node
BACKUP_DIR="/opt/metanode/backup_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$BACKUP_DIR"
cp -r "$DATA_DIR/consensus" "$BACKUP_DIR/"
cp -r "$DATA_DIR/parentchain_db" "$BACKUP_DIR/"
echo "Đã sao lưu dữ liệu tại: $BACKUP_DIR"
```

### Bước 3: Xác định commit và block cao nhất đạt đồng thuận
Kiểm tra log của cả 4 node ngay trước thời điểm dừng để tìm block height và commit index cao nhất mà mạng đã chốt thành công:
```bash
grep -E "Committed block|last_block|CommitIndex" /opt/metanode/parent_chain_*/logs/parent_chain.log | tail -n 30
```
Ghi nhận giá trị `highest_committed_block` và hash tương ứng.

### Bước 4: Khôi phục lại khóa gốc và genesis nhất quán
1. Kiểm tra thư mục backup tự động của công cụ sinh khóa:
   `deploy/ansible_clusters/parent_keys.previous_<timestamp>/`
2. Khôi phục lại toàn bộ khóa nguyên bản trùng khớp với DAG hiện tại:
   ```bash
   cp deploy/ansible_clusters/parent_keys.previous_<timestamp>/* deploy/ansible_clusters/parent_keys/
   ```
3. Kiểm tra mã băm khóa để chắc chắn các node có đúng khóa cũ:
   ```bash
   sha256sum deploy/ansible_clusters/parent_keys/*.json
   ```

### Bước 5: Phân phối lại cấu hình và khởi động lại cụm
Sau khi đã khôi phục khóa và genesis gốc:
```bash
cd deploy/ansible_clusters
./deploy_clusters.sh --restart
```
Lúc này cờ kiểm tra Fingerprint (`genesis_committee_fingerprint`) sẽ xác nhận khóa trùng khớp hoàn toàn với state lưu trên đĩa và cho phép node tiếp tục chạy mà không bị abort.

---

## 3. Tình huống B: Muốn reset cụm có chủ đích (Key Rotation / Cài đặt Testnet mới)

Áp dụng khi chuẩn bị nâng cấp testnet, đổi toàn bộ ủy ban validator, hoặc xoay khóa bảo mật và chấp nhận xóa dữ liệu cũ.

### Bước 1: Dừng toàn bộ hệ thống
Dừng cả cụm Parent Chain và các cụm thực thi (Execution Clusters) để tránh việc Rollup Relayer bị treo khi Parent Chain biến mất:
```bash
cd deploy/ansible_clusters
./deploy_clusters.sh --stop
```

### Bước 2: Sinh bộ khóa mới an toàn với cờ xác nhận
Script `gen_parent_keys.py` yêu cầu bắt buộc phải có cờ `--i-understand-this-wipes-chain-data` để ngăn ngừa việc vô tình ghi đè:
```bash
python3 deploy/ansible_clusters/scripts/gen_parent_keys.py \
    --num-nodes 4 \
    --out-dir deploy/ansible_clusters/parent_keys \
    --overwrite \
    --i-understand-this-wipes-chain-data
```
Script sẽ tự động tạo một thư mục backup `parent_keys.previous_<YYYYMMDD_HHMMSS>` phòng trường hợp cần hoàn tác.

### Bước 3: Thực hiện Reset đồng bộ toàn mạng bằng `--reset`
**LƯU Ý:** Tuyệt đối KHÔNG dùng `--restart`. Bắt buộc phải dùng `--reset`:
```bash
./deploy_clusters.sh --reset --i-understand-this-wipes-chain-data
```
Lệnh này sẽ:
1. Xóa sạch thư mục Rust consensus (`wipe_safe_rust_consensus`).
2. Xóa sạch thư mục LevelDB / NOMT Go state (`parentchain_db/`).
3. Xóa file dấu vân tay cũ `genesis_committee_fingerprint`.
4. Render lại `parent_genesis.json` mới với danh sách validator và public key mới.
5. Khởi động lại cụm từ Block #0 (Genesis).

### Bước 4: Đồng bộ cấu hình sang các cụm Execution Clusters (Rollup)
Khi Parent Chain đã reset về Block #0:
1. Tất cả float accounts và account registry trên Parent Chain đã bị xóa.
2. Các Execution Clusters (`exec1`, `exec2`) không thể sử dụng account cũ đã đăng ký ở Parent Chain cũ.
3. Cần chạy lại quy trình bootstrap / register cluster trên Parent Chain mới:
   ```bash
   ./deploy_clusters.sh --setup --test
   ```

---

## 4. Tiêu chí nghiệm thu cụm đã "Thực sự sống lại" (Health Criteria)

Một cụm Parent Chain sau khi phục hồi chỉ được coi là hoạt động bình thường khi thỏa mãn đầy đủ cả 3 tiêu chí sau:

### Tiêu chí 1: Rust BFT Consensus đạt trạng thái Healthy trên cả 4 node
Kiểm tra log của cả 4 node:
```bash
grep -E "Phase transition|ConsensusState|DEADLOCK" /opt/metanode/parent_chain_*/logs/parent_chain.log | tail -n 20
```
- **Yêu cầu:** Thấy rõ dòng log chuyển pha:
  `Phase transition: CatchingUp -> Healthy`
- **Không còn lỗi:** Không xuất hiện lặp lại các dòng `Failed to verify the block's signature: Invalid signature` hoặc `DEADLOCK-BREAKER`.

### Tiêu chí 2: Độ cao Block (/status) tăng trưởng đều đặn và đồng nhất
Truy vấn trạng thái đồng thuận trên tất cả các node:
```bash
for port in 18601 18602 18603 18604; do
    echo "--- Port $port ---"
    curl -s http://127.0.0.1:$port/status | jq '{chain_id, last_block, last_hash, syncing, fork_detected}'
done
```
- **Yêu cầu:**
  + `fork_detected`: Phải là `false`.
  + `syncing`: Phải là `false`.
  + `last_block`: Tăng liên tục sau mỗi chu kỳ commit (không đứng yên).
  + `last_hash` và `state_root`: Hoàn toàn trùng khớp giữa các node tại cùng một độ cao block.

### Tiêu chí 3: Giao dịch thử nghiệm End-to-End được xác nhận thành công
Chạy một giao dịch test thực tế gửi lên Parent Chain:
```bash
# Gửi giao dịch nạp float hoặc đăng ký account test
curl -s -X POST http://127.0.0.1:18601/send_raw_transaction -d '{"tx": "..."}'
```
Sau đó kiểm tra receipt qua hash:
```bash
curl -s "http://127.0.0.1:18601/receipt?hash=<tx_hash>" | jq .
```
- **Yêu cầu:** Receipt trả về `status: 1` và có số block xác nhận thực tế.

