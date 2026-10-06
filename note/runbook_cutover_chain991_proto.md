# Runbook: cutover toàn mạng sang Chain ID 991 + payload protobuf

- **Trạng thái:** DỰ THẢO. Lệnh đã được đối chiếu với `deploy/ansible_clusters` (`deploy_clusters.sh`, `deploy.yml`, role `common`/`build`/`exec_cluster`/`parent_chain`, `inventory.example.yml`) và `execution/cmd/simple_chain/backend.go`, nhưng **chưa diễn tập end-to-end**. Phải diễn tập (kể cả rollback) trên cụm cô lập dựng từ template trước khi dùng thật (kế hoạch: `plan_production_launch_20261006.md` giai đoạn 3.3).
- **Chỉ viết, không thực thi trên cụm thật.** Mọi bước phá hủy dữ liệu (wipe) CẦN user xác nhận bằng văn bản TỪNG LẦN.
- **Bất biến (AGENTS.md 2.5):** không dùng timeout/sleep để quyết định dispatch; thà PENDING chứ không fork. Không `pkill` theo mẫu/cổng: dừng bằng `deploy_clusters.sh --stop` hoặc `systemctl stop metanode-<host>`.
- **Lịch sử sửa:** bản đầu của runbook này dùng tên unit, đường dẫn, cổng, tên playbook và tên message proto KHÔNG tồn tại trong repo. Đã viết lại theo mã thật; nếu cấu hình triển khai của bạn khác (inventory riêng), thay các biến theo inventory của bạn.

## 1. Vì sao phải cutover đồng thời
1. **Chain ID parent 990 → 991** (genesis parent `chain_id`, `parent_chain_id` trong ansible; mọi cụm exec dùng cùng 991). Giao dịch ký với chain ID cũ bị từ chối.
2. **Payload tx hệ thống JSON → protobuf** (`execution/pkg/proto/rollup.proto`: `RollupSystemPayloadProto`, `RollupSystemAttestedPayloadProto`, `AccountRegistrationPayloadProto`; digest attestation `ACCT_REG_ATTEST_V1` / `ROLLUP_SYS_EVENT_ATTEST_V1`). Node cũ không parse được payload mới và ngược lại.
3. **Co-attestation f+1** bật mặc định: validator cần khóa committee hợp lệ (mục 3).
4. Không có đường nâng cấp tương thích ngược ⇒ wipe + redeploy đồng thời parent và mọi cụm exec.

## 2. Biến theo inventory (điền trước khi chạy)
| Biến | Lấy ở đâu | Ví dụ trong `inventory.example.yml` |
|---|---|---|
| Host parent | nhóm `parent_chain_nodes` | `parent_node_0..3`, `parent_http_port` 18601.., `node_data_dir` `/opt/metanode/parent_chain_N` |
| Host exec | nhóm `exec_clusters` → `cluster_N` → hosts | `exec1_r1..r3`, `node_data_dir` `/opt/metanode/exec1_rN`, `rpc_port` 8747 |
| Unit systemd | role dựng `metanode-{{ inventory_hostname }}.service` | `metanode-parent_node_0`, `metanode-exec1_r1` |
| Binary | `{{ bin_dir }}` (mặc định `/opt/metanode/bin`) | `simple_chain`, `parent_chain` |
| Endpoint exec | cổng RPC của node | `/health`, `/readiness`, `/metrics` (cùng cổng RPC) |
| Endpoint parent | `parent_http_port` | `/status` (có `chain_id`) |

## 3. Yêu cầu khóa validator (BẮT BUỘC)
Với mỗi validator: `AccountState.PublicKeyBls` của địa chỉ validator trong genesis (`alloc[...].publicKeyBls`) PHẢI bằng public key sinh từ khóa attestation của node (`Databases.BLSPrivateKey`; template ansible đặt từ `bls_priv`). Lệch ⇒ đăng ký/credit PENDING mãi (an toàn nhưng treo), `/readiness` trả 503, metric `master_validator_committee_key_valid=0`.
- Triển khai kiểu ansible hiện tại dùng **một khóa BLS cho cả cụm** (`bls_pubkey` ghi cho mọi alloc, genesis validator duy nhất) ⇒ committee có đúng 1 khóa phân biệt, f+1 = 1. Committee đếm khóa phân biệt nên các node chung khóa không làm ngưỡng phình lên.
- Sinh/kiểm khóa: `cd execution && go run ./cmd/tool/bls_pubkey -stdin -hex < <(printf %s "$BLS_PRIV")` (đưa khóa qua stdin, KHÔNG qua tham số dòng lệnh). Cờ `-hex` in `0x` + 96 hex.
- **Pre-flight tự động (đã kiểm với ansible thật):** role `exec_cluster` chạy trên controller với tool `bls_pubkey` do role `build` dựng; khóa qua stdin + `no_log`. FAIL deploy nếu: tool thiếu, `bls_pubkey` không cấu hình khi có `bls_priv` (genesis từng rơi về dùng chính khóa riêng làm public key), hoặc khóa dẫn xuất ≠ `bls_pubkey`.
- **Chặn bí mật devnet:** khi không khai báo `metanode_env=devnet`/`node_env=devnet`, deploy FAIL nếu `master_password`, `app_pepper`, `pk_admin_file_storage` còn giá trị mặc định devnet. Đặt chúng trong Ansible Vault.

## 4. Quy trình (mỗi pha có Go/No-Go)

### Pha 0 — Chuẩn bị (không phá hủy)
1. Build binary từ commit đã verify (go/no-go: `build_check.sh` sạch; `go test -race` rollup/tx_processor/simple_chain; các E2E của `note/production_readiness_assessment_20261006.md` xanh trên đúng commit).
2. Cập nhật inventory: `parent_chain_id: 991`, `tx_signature_mode: secp`, `account_gate: parent_registered`, `metanode_env: production`, secrets qua vault (`vault_ansible_become_pass`, `master_password`, `app_pepper`, `pk_admin_file_storage`…). Tạo `.vault_pass` bằng `openssl rand -base64 24 > .vault_pass` (đã gitignore).
3. Chạy trước trên cụm cô lập/staging cùng inventory (diễn tập) — chỉ sau đó mới sang cụm thật.
4. Lưu binary cũ (đúng phiên bản đang chạy) để rollback: `cp -a /opt/metanode/bin /opt/metanode/bin.pre_cutover_$(date +%Y%m%d)` trên từng host.

### Pha 1 — Đóng cổng
Chặn traffic client vào cổng RPC công khai ở tầng proxy/firewall của hạ tầng (cách làm phụ thuộc môi trường; ghi lại rule đã thêm để gỡ ở pha 7). Không dùng thay đổi firewall làm tác dụng phụ của deploy (UFW là opt-in `--open-ports`).
- **G1:** không còn kết nối client mới; request đang bay đã xong.

### Pha 2 — Dừng (exec trước, parent sau)
```bash
cd deploy/ansible_clusters
./deploy_clusters.sh --stop --exec-only      # đợi hoàn tất
./deploy_clusters.sh --stop --parent-only
```
Hoặc từng host: `sudo systemctl stop metanode-<host>`. Xác nhận bằng `systemctl is-active metanode-<host>` (phải `inactive`) trên mọi host; không dùng pkill.
- **G2:** mọi unit inactive, không còn tiến trình `simple_chain`/`parent_chain` của deploy này (kiểm theo thư mục làm việc `node_data_dir`, không theo cổng).

### Pha 3 — Sao lưu và kiểm backup (mỗi host)
```bash
TS=$(date +%Y%m%d_%H%M%S); B=/opt/metanode_backups/pre_cutover_$TS; sudo mkdir -p "$B"
# D = node_data_dir của host (xem bảng mục 2), ví dụ /opt/metanode/exec1_r1
sudo tar -czf "$B/$(basename "$D").tar.gz" -C "$(dirname "$D")" "$(basename "$D")"
tar -tzf "$B/$(basename "$D").tar.gz" >/dev/null && echo "backup OK"
( cd "$B" && sha256sum *.tar.gz | sudo tee SHA256SUMS )
df -h /opt/metanode_backups
```
Sao lưu cả `{{ install_dir }}/genesis_base.json`, `raft_secret.key`, thư mục khóa. Mang một bản sang máy khác.
- **G3:** `tar -t` không lỗi, checksum đã lưu, đủ dung lượng đĩa.

### Pha 4 — Wipe + deploy (CẦN XÁC NHẬN BẰNG VĂN BẢN CỦA USER)
> 🚨 Phá hủy dữ liệu. Chỉ thực hiện sau khi user xác nhận trong phiên, đúng thời điểm này, sau khi G1–G3 đã đạt.
```bash
cd deploy/ansible_clusters
./deploy_clusters.sh --reset            # xóa DB, phân phối binary mới (role common), sinh genesis mới, khởi chạy lại
```
`--reset` chạy các pre-flight (khóa BLS, bí mật devnet) trước khi sinh genesis; nếu FAIL thì DỪNG và xử lý nguyên nhân, không bỏ qua kiểm tra.
- **G4:** playbook `failed=0`; pre-flight BLS PASS.

### Pha 5 — Khởi động và xác nhận (parent trước, exec sau)
`--reset` tự khởi động; nếu cần thủ công: `./deploy_clusters.sh --start --parent-only`, đợi parent sẵn sàng rồi `--start --exec-only`.
```bash
curl -s http://<parent_host>:<parent_http_port>/status | jq '.chain_id'     # kỳ vọng 991
```
- **G5:** parent `chain_id` = 991; mọi unit `active`; log exec có `[COMMITTEE-KEY-OK]` (validator) hoặc `[COMMITTEE-KEY-INFO]` (node không phải validator); không panic/FFI/NOMT lỗi.

### Pha 6 — Kiểm tra sau triển khai
```bash
R=http://<exec_host>:<rpc_port>
curl -s -X POST -H 'Content-Type: application/json' --data '{"jsonrpc":"2.0","method":"mtn_getClusterIdentity","params":[],"id":1}' $R | jq .
curl -s $R/health | jq .                       # status ok, committee_key = ok (validator) | not_validator
curl -s -o /dev/null -w '%{http_code}\n' $R/readiness   # 200 (503 nếu committee_key=mismatch)
curl -s $R/metrics | grep -E 'master_validator_committee_key_valid|master_parent_chain_id_mismatch|master_rollup_signatures_rejected_total|master_rollup_committee_read_errors_total'
```
Kỳ vọng: `master_validator_committee_key_valid` 1 (validator) hoặc -1 (không phải), `master_parent_chain_id_mismatch` 0, các counter lỗi 0.
Chạy lại bộ test thật trên cụm vừa dựng (không phải trên dữ liệu production): gate E2E, co-attestation, cross-chain (`execution/scripts/test/gate_e2e/*`), và: đăng ký một tài khoản mẫu ⇒ `PENDING → CONFIRMED`; chuyển xuyên cụm; so state root giữa các node cùng cụm.
- **G6:** health/readiness 200, metric lỗi 0, đăng ký CONFIRMED, state root đồng nhất, 0 fork.

### Pha 7 — Mở cổng
Gỡ rule đã thêm ở pha 1; thông báo hoàn tất. Theo dõi PENDING age (`master_account_registration_pending_max_age_seconds`) và `master_rollup_signatures_rejected_total` trong 24 giờ đầu.

## 5. Rollback
**Điều kiện:** pha 4/5 thất bại không khắc phục được trong 15 phút; `/readiness` 503 không rõ lý do; state root lệch giữa các node; quyết định No-Go.
1. `./deploy_clusters.sh --stop` (cả exec và parent).
2. Khôi phục binary cũ: `sudo rm -rf /opt/metanode/bin && sudo cp -a /opt/metanode/bin.pre_cutover_<ngày> /opt/metanode/bin` (từng host).
3. Khôi phục dữ liệu từng host từ backup pha 3: xóa `node_data_dir` mới sinh (xác nhận đúng đường dẫn bằng `echo "$D"` trước khi `rm -rf "$D"`), rồi `sudo tar -xzf "$B/<tên>.tar.gz" -C "$(dirname "$D")"`. Khôi phục cả genesis/khóa đã backup.
4. Khởi động bằng `systemctl start metanode-<host>` (parent trước, exec sau) — KHÔNG chạy `--reset`/`--setup` vì sẽ sinh lại genesis và ghi đè binary.
5. Kiểm tra: parent `/status` `chain_id` = 990 (giá trị cũ), exec `/health` ok, state root khớp giữa các node. Gỡ rule chặn cổng.
6. Ghi nguyên nhân thất bại, không thử lại cho tới khi hiểu rõ.

## 6. Bảng Go/No-Go
| Mốc | Go | No-Go |
|---|---|---|
| G1 đóng cổng | không còn client | còn request dở |
| G2 dừng | mọi unit inactive | unit treo, không stop sạch |
| G3 backup | tar test + checksum + đủ đĩa | backup rỗng/lỗi/thiếu đĩa |
| G4 wipe+deploy | user xác nhận; playbook failed=0; pre-flight PASS | pre-flight FAIL, lỗi playbook |
| G5 khởi động | parent 991; unit active; log sạch | crash-loop, panic, lỗi FFI/NOMT |
| G6 kiểm tra | health/readiness 200; metric lỗi 0; CONFIRMED; state root đồng nhất | mismatch khóa, lệch state root, attestation treo |

## 7. Cần diễn tập để đóng các điểm chưa chắc
- `--reset` có xóa/ghi lại các tệp khóa hay không (đọc `roles/*/tasks/main.yml` các khối `clean`/`reset` và thử trên cụm cô lập; ghi kết quả vào bản runbook này).
- Thời gian thực tế từng pha; chỗ nào cần lệnh thủ công thay cho `deploy_clusters.sh`.
- Rollback đầy đủ (kể cả khôi phục binary/genesis) có đưa cụm cũ về trạng thái chạy được không.
