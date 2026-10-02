# 📘 Sổ tay vận hành Cụm Parent Chain Multi-Node (Runbook)

> **Phạm vi:** Dành cho kỹ sư vận hành (DevOps/SRE) và lập trình viên hệ thống MetaNode.  
> **Kiến trúc:** Cụm đồng thuận BFT 4 validator (ngưỡng chịu lỗi Byzantine $f=1$, $3f+1=4$ nodes, quorum $\ge 2f+1=3$).  
> **Nguyên tắc cốt lõi:** ZERO-FORK — Thà pending/chờ xác nhận từ 2f+1 peer chứ tuyệt đối không bao giờ fork. Không dùng timeout/sleep để tự ý dispatch hay thoát deadlock.

---

## 1. Tổng quan Kiến trúc & Cổng Mạng

| Thành phần | Giao thức / Port Mẫu (Local Dev) | Vai trò |
| :--- | :--- | :--- |
| **HTTP RPC** | `:18601` – `:18604` (Production: `:8547`) | Tra cứu state (`/status`, `/proof`, `/block`, `/state_root`, `/validators`), tiếp nhận giao dịch (`/send_raw_transaction`), xuất số liệu (`/metrics`). |
| **P2P Consensus** | `:19001` – `:19004` (Production: `:9000+`) | Kênh truyền khối, vote và DAG consensus giữa các node BFT. |
| **Peer RPC** | `:19501` – `:19504` (Production: `:19300+`) | Đồng bộ block & digest giữa các peer (`CommitVoteMonitor`, `SyncBlocks`, `GetBlocksRange`). |
| **Prometheus Metrics**| `:18601/metrics` (tích hợp trên HTTP RPC) | Giám sát độ cao block, state root, tổng txs, cảnh báo fork. |

---

## 2. Các Chỉ số Giám sát Trọng yếu (Prometheus Metrics)

Node xuất bản các metric tiêu chuẩn tại endpoint `/metrics`:

- `parent_chain_last_block` (Gauge): Độ cao block đã được xác thực và áp dụng cục bộ.
- `parent_chain_state_root{state_root="..."}` (GaugeVec): Giá trị băm Merkle/NOMT state root hiện tại.
- `parent_chain_fork_detected` (Gauge): **Cờ cảnh báo nguy cấp.**
  - `0`: Trạng thái bình thường, dữ liệu đồng nhất.
  - `1`: Phát hiện xung đột block (cùng block height nhưng khác commit digest/txs_root). Node lập tức ngừng xử lý block tiếp theo để bảo vệ tính toàn vẹn state.
- `parent_chain_txs_total` (Counter): Tổng số lượng giao dịch Parent Chain đã thực thi.
- `parent_chain_blocks_total` (Counter): Tổng số khối đồng thuận đã xử lý thành công.

### Công cụ giám sát thời gian thực
Sử dụng công cụ `parent_chain_monitor`:
```bash
# Kiểm tra nhanh 1 lần (dùng cho CI/CD hoặc healthcheck script, exit code 0=healthy, 1=mismatch/error)
go run ./execution/cmd/tool/parent_chain_monitor --once

# Giám sát liên tục dạng live dashboard mỗi 2 giây
go run ./execution/cmd/tool/parent_chain_monitor --interval 2s --nodes "http://127.0.0.1:18601,http://127.0.0.1:18602,http://127.0.0.1:18603,http://127.0.0.1:18604"
```

---

## 3. Quy trình Vận hành Chi tiết

### 3.1. Khởi động lại cụm (Cluster Restart)

Khi cần bảo trì hoặc khởi động lại:
```bash
# Khởi động lại an toàn cụm local
cd deploy/cluster/local_parent_chain
./run.sh restart

# Kiểm tra trạng thái đồng bộ sau khi khởi động
./run.sh status
```
- Khi khởi động lại, `BlockProcessor` đọc LevelDB (`ParentChainBlockProgress`), xác minh state root trong DB với NOMT sparse merkle tree.
- Nếu state khớp, node phục hồi ngay tại block cuối cùng mà không cần resync.

---

### 3.2. Xử lý Node bị tụt lại (Node Lag / Catchup)

- **Triệu chứng:** Một node có `last_block` nhỏ hơn các node còn lại.
- **Cơ chế tự động:** Khi node nhận được block $N+k$ trong khi block hiện tại là $N$, cơ chế `GapDetection` phát hiện khoảng trống (`ErrBlockGap`) và kích hoạt `SyncCallback(N+1)`. Node tự động kéo các block bị thiếu từ các peer qua `/block?number=...` hoặc `GetBlocksRange`.
- **Can thiệp thủ công (nếu kẹt mạng quá lâu):**
  1. Kiểm tra log của node bị tụt: `tail -f node-X/logs/node-X.log`.
  2. Khởi động lại tiến trình của riêng node đó:
     ```bash
     PID=$(cat node-X/node-X.pid)
     kill -TERM $PID
     # Chạy lại lệnh start của node-X như trong run.sh
     ```
  3. Node sẽ kết nối lại peer RPC và tự động bắt kịp.

---

### 3.3. Xóa dữ liệu và Đồng bộ lại từ đầu (Wipe & Resync)

Khi database của một node bị hỏng vật lý hoặc muốn thử nghiệm đồng bộ nguội (cold sync):
1. **Dừng node:**
   ```bash
   kill -TERM $(cat node-X/node-X.pid)
   ```
2. **Xóa sạch thư mục data của node (giữ lại keys):**
   ```bash
   rm -rf node-X/data/*
   ```
3. **Khởi động lại node:**
   ```bash
   ./parent_chain -rust-config node-X/node.toml -data-dir node-X/data -http :1860X -genesis parent_genesis.json > node-X/logs/node-X.log 2>&1 &
   echo $! > node-X/node-X.pid
   ```
4. **Kiểm tra tiến độ:** Node sẽ bắt đầu từ block 0, kéo các block 1..N từ 3 node còn lại, thực thi tuần tự tất định và tính toán lại state root NOMT. Kết thúc quá trình, state root của node này PHẢI trùng khớp 100% với các node kia.

---

### 3.4. Xử lý khi mất Quorum (Loss of Quorum, $\ge 2$ nodes offline)

- Trong cụm 4 node ($f=1$), nếu $\ge 2$ node bị sập, số node còn lại ($\le 2$) không đạt đa số Byzantine $\ge 2f+1=3$.
- **Hành vi thiết kế:**
  - Hệ thống **tạm dừng tạo block mới** (liveness tạm dừng).
  - Không có fork xảy ra (safety được bảo toàn tuyệt đối theo Invariant Zero-Fork).
  - Các giao dịch gửi đến sẽ được hàng đợi (queue) giữ lại hoặc trả về chờ xác nhận.
- **Khắc phục:**
  1. Kiểm tra nguyên nhân các node sập (hết disk, nghẽn mạng, lỗi tiến trình).
  2. Bật lại ít nhất 1 node để tổng số node online $\ge 3$.
  3. Ngay khi đạt $\ge 3$ node online, mạng đồng thuận BFT tự động khôi phục và tiếp tục tạo block, giải phóng hàng đợi.

---

### 3.4b. Cả 4 node online nhưng không tiến / chia 2 nhóm (sau khi nhiều node bị `kill -9` rồi bật lại)

**Dấu hiệu:** `last_block` của các node lệch nhau và không tự đều lại sau ~60 giây dù cả 4 node online; `fork_detected=false`; log Rust có hàng nghìn dòng `Rejecting commits ... insufficient quorum votes (accumulated_stake=... needed=2f+1)` và `DIGEST-GATE ... DIVERGENT`; một nhóm ở `CatchingUp`, nhóm kia ở `Healthy`.

**Tính chất:** mất liveness, KHÔNG fork (mỗi nhóm chỉ vote digest riêng nên không ai đủ 2f+1 — đúng nguyên tắc thà pending chứ không fork).

**Nguyên nhân đã biết (2026-10-02):** sau restart, node chèn "baseline" từ mạng (`DAG-RESET Baseline injected`) cả khi lệch nhỏ, và bỏ qua `set_committed` cho block của commit lịch sử, nên sub-dag/commit local lệch với mạng. Đã sửa (commit consensus `commit_syncer` + `linearizer`: chỉ chèn baseline khi DAG rỗng hoặc lệch > `gc_depth`; luôn đánh dấu committed). Trước sửa lỗi gặp ~1/3 lượt e2e mới triển khai; sau sửa 0 lần trong 8 lượt e2e mới + 15 chu kỳ crash-restart + 12 chu kỳ có kiểm tra hash từng block + 8 lượt T-I1..T-I8. Vẫn chưa chứng minh tuyệt đối.

**Khắc phục (không wipe dữ liệu):**
1. Xác nhận bằng `parent_chain_monitor` hoặc `GET /status` từng node (height/hash khác nhau, không có `fork_detected`).
2. Restart **lần lượt từng node đang tụt lại** (SIGTERM, chờ lên hẳn rồi mới sang node kế). Đã quan sát một node tụt lại hồi phục đúng bit sau lần restart thứ hai.
3. Nếu vẫn kẹt: dừng cả 4 node rồi bật lại cả 4 (dữ liệu giữ nguyên), kiểm tra parity.
4. Nếu node báo `fork_detected=true`: chuyển sang 3.5.
5. Đặt cảnh báo: height các node lệch > 1 trong > 60 giây (`block_hash_checker --watch` hoặc `parent_chain_monitor`).

### 3.5. Ứng phó Sự cố Fork (`parent_chain_fork_detected = 1`)

- **Triệu chứng:**
  - Metric `parent_chain_fork_detected` nhảy lên `1`.
  - Log xuất hiện cảnh báo đỏ: `🚨 Parent Chain: CRITICAL FORK DETECTED at block #N! State conflict...`.
  - Node từ chối xử lý mọi block tiếp theo (`REFUSING TO PROCESS block #N+1`).
- **Nguyên nhân có thể:**
  - Database của node bị can thiệp bên ngoài hoặc bị bit-rot hỏng dữ liệu.
  - Một validator độc hại đề xuất block có cùng số thứ tự nhưng khác commit digest / nội dung giao dịch.
- **Quy trình xử lý khẩn cấp:**
  1. **Cô lập node:** Lập tức ngắt kết nối node này khỏi load balancer / reverse proxy để không phục vụ client.
  2. **So sánh với Quorum:** Dùng `parent_chain_monitor --once` để xem 3 node còn lại có cùng một state root không.
  3. **Nếu 3 node kia đồng thuận cùng root A, chỉ node này bị lệch (Local Corrupted):**
     - Thực hiện quy trình **Wipe & Resync** ở mục 3.3 cho node này.
  4. **Nếu có phân rã thực sự (2 node root A, 2 node root B):**
     - Dừng toàn bộ cụm ngay lập tức.
     - Trích xuất BlockRecord tại block phát hiện xung đột (`/block?number=N`) từ cả 2 nhóm để phân tích digest và chữ ký người đề xuất.

---

### 3.6. Thay đổi Committee Thủ công (Manual Committee Change with Wipe)

Khi cấu hình lại danh sách validator hoặc thay đổi stake/khóa trong môi trường dev/staging:
1. Dừng toàn bộ cụm: `./run.sh down`.
2. Tạo file `parent_genesis.json` mới với danh sách validator và public key mới.
3. Đồng bộ file `parent_genesis.json` tới tất cả các node.
4. Cập nhật `node.toml` và các file khóa tương ứng của từng node.
5. Thực hiện wipe dữ liệu toàn cụm: `./run.sh clean`.
6. Khởi động lại cụm mới: `./run.sh up`.
7. Kiểm tra `/validators` trên tất cả các node để xác nhận committee mới đã có hiệu lực.

## 4. Genesis: ai được đăng ký làm cluster (BẮT BUỘC đọc trước khi chạy production)

Chứng nhận của một cluster đã đăng ký là thứ cho phép `depositToFloat` (đúc float). Vì vậy việc đăng ký cluster do **genesis** quyết định, giống hệt trên mọi validator:

| Trường genesis | Ý nghĩa |
|---|---|
| `open_cluster_registration: true` | Bất kỳ khóa nào cũng tự đăng ký được cluster rồi đúc float. **CHỈ cho devnet/test.** Node parent in cảnh báo khi khởi động. |
| `open_cluster_registration: false` + `clusters: ["<48-byte BLS pubkey hex>", ...]` | Chỉ các khóa trong danh sách được `registerCluster` (receipt lỗi mã 221 với khóa khác). **Cấu hình production.** |
| (thiếu cả hai) | Mặc định đóng, danh sách rỗng: không ai đăng ký được. |

Ansible: biến `parent_open_cluster_registration` (mặc định `false` = an toàn; devnet phải đặt `true` tường minh, production phải khai `parent_allowed_clusters` — playbook dừng nếu cả hai bỏ trống) và `parent_allowed_clusters` trong inventory. Kiểm tra bằng:

```bash
cd execution && go run ./cmd/tool/parent_chain_security_check -url http://<node>:<port> [-expect-closed]
```
Công cụ tấn công thật (POST /tx, deposit không nguồn/nguồn lạ/chứng nhận giả/không chữ ký, registerCluster lạ) và thoát mã 1 nếu có kẻ tấn công nào lọt.

### 4.1. Mô hình float của production (2026-10-02): tổng cung cố định, BLS là tài khoản

- **Tổng cung = tổng số dư genesis, không bao giờ tăng.** `depositToFloat` (đường mint duy nhất) bị **tắt** trừ khi genesis đặt `allow_deposit_to_float: true` (chỉ devnet/test); khi tắt, mọi deposit nhận receipt lỗi **222**, state không đổi.
- **Số dư khởi tạo trong genesis:** `float_accounts: [{"bls_public_key": "<48-byte hex>", "balance": "<số thập phân, đơn vị nhỏ nhất>"}]` (1 đơn vị = 10^18). Áp dụng tất định trong **block 1** (nằm trong state root block 1), nên **mọi validator phải dùng cùng một file genesis**. Khóa trùng, số dư không dương hay khóa sai độ dài làm node không khởi động.
- **Chuyển tiền thường giữa các BLS:** method `TRANSFER_BALANCE` (người gửi ký tx như mọi tx, nonce tuần tự). Trừ người gửi, cộng người nhận, không đổi tổng cung, không sự kiện inbound. Lỗi: **223** (CallData hỏng), **224** (thiếu số dư/số tiền không hợp lệ/tự chuyển cho mình). Người nhận tự được ghi vào AccountRegistry nên ký được tx tiếp theo. Không có phí và không có trần velocity (đã chọn đơn giản; trần velocity chỉ áp cho `TransferFloat` xuyên cụm).
- **Đăng ký cluster:** khóa thuộc `clusters` (tập sáng lập) **hoặc** có số dư ≥ `min_float_to_register` (production: `"1000000000000000000000"` = 1000 đơn vị nguyên) đều đăng ký được; còn lại **221**. Không có khóa bond: số dư chỉ được kiểm lúc đăng ký (có thể chuyển đi sau đó; cân nhắc bond nếu cần).
- **Chỉ cluster đã đăng ký (`authorized`) mới được làm nguồn chứng nhận** (`depositToFloat`, nguồn của `transferFloat`, `submitStateRoot`). Một khóa chỉ *nhận* tiền không còn trở thành cluster tin cậy (đã sửa lỗ hổng bỏ qua chính sách).
- **Đổi định dạng state ⇒ phải wipe + redeploy đồng loạt** (trường `authorized` trong ChainRegistry, method mới, hook genesis). Không có migrate.
- Ansible: `parent_float_accounts`, `parent_min_float_to_register` (mặc định 1000 đơn vị), `parent_allow_deposit_to_float` (mặc định `false`; devnet đặt `true`). Playbook dừng nếu cả `parent_open_cluster_registration`, `parent_allowed_clusters` và `parent_float_accounts` đều trống.
- Kiểm chứng trực tiếp trên cụm khởi động bằng genesis tương ứng:
```bash
cd execution
go run ./cmd/tool/test_account_model -print-genesis   # trường genesis cho 3 khóa test (CHỈ test, khóa công khai)
go run ./cmd/tool/test_account_model                  # genesis→chuyển→ngưỡng 1000→không mint→parity 4 node
```

## 5. Giao dịch: chỉ có một đường

Mọi thay đổi trạng thái là `pb.Transaction` ký BLS trên hash (kèm `ChainID=990` và nonce tuần tự), gửi **raw proto bytes** tới `POST /send_raw_transaction`. Không còn `POST /tx` JSON, không còn giao dịch không ký. `GET /nonce?address=0x...` trả nonce đã commit của người gửi (client tự tính nonce tiếp theo, `QuorumClient` làm sẵn).

## 6. Kiểm tra không rẽ nhánh sau restart (G11 đã sửa)

`execution/scripts/test/parent_chain_fork_hunt.sh` lặp T-I2 (một node dừng/bật) và T-I3 (mất quorum rồi khôi phục) trên cụm local 4 node, sau mỗi bước chờ hội tụ (có giới hạn) rồi so hash block của mọi node ở mọi chiều cao. Phải báo `no fork and always converged`. Nếu một node vẫn lệch hash ở cùng chiều cao: wipe dữ liệu node đó rồi đồng bộ lại (T-I6 xác nhận ra đúng root) và báo lại kèm log. Chi tiết nguyên nhân/bằng chứng: `note/parent_chain_next_plan.md` mục 7 (G11). Lưu ý: node restart khi chain có hàng nghìn commit rỗng có thể mất vài chục giây đến vài phút để bắt kịp (chậm, không phải fork).
