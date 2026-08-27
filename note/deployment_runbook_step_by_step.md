# Runbook Triển Khai Từng Bước + Xác Thực — Metanode

Tài liệu này khác `note/production_deployment_guide.md` ở chỗ: tài liệu kia trả lời "hệ
thống đang ở đâu, cái gì sẵn sàng, cái gì chưa" (bối cảnh, cảnh báo, kiến trúc); tài liệu
**này** là **sổ tay thao tác** — từng lệnh cụ thể, theo đúng thứ tự, kèm **cách xác nhận
bước đó đã THÀNH CÔNG** trước khi làm bước tiếp theo. Đọc `production_deployment_guide.md`
mục 0 trước nếu chưa đọc — **đừng tin theo số PR/tên nhánh** (đã có 3 lần 1 PR hiện "merged"
trên GitHub nhưng vài commit cuối bị rớt khỏi `dev` — xem cảnh báo squash-merge ở đầu mục 0
của tài liệu đó); luôn xác nhận bằng grep cụ thể như Phần 0.2/0.3 dưới đây.

Quy ước xuyên suốt tài liệu này: mỗi bước có khối **"✅ Xác nhận thành công"** — nếu kết quả
thực tế không khớp, **dừng lại, không làm bước tiếp theo**, xem mục "Xử lý sự cố" (Phần E)
trước khi tiếp tục.

---

## Phần 0 — Chuẩn bị chung (bắt buộc cho mọi con đường)

### 0.1 Build & kiểm tra môi trường

```bash
cd /path/to/metanode
bash consensus/metanode/scripts/build_check.sh --release
```

**✅ Xác nhận thành công:** lệnh kết thúc với exit code `0`, không có dòng `error` màu đỏ.
Script này build cả Go (`execution/`) lẫn Rust (`consensus/metanode/`) qua CGo FFI — nếu 1
trong 2 lỗi, **không tiếp tục** bất kỳ bước nào dưới đây.

### 0.2 Kiểm tra mã nguồn đang ở đúng commit mong muốn

```bash
git log --oneline -1
git branch --show-current
```

**✅ Xác nhận thành công:** code có fix bug treo chain nghiêm trọng nhất. Đừng tin theo tên
nhánh hay số PR (đã có 3 lần "merged" trên GitHub mà commit thật vẫn thiếu — xem cảnh báo
squash-merge, `production_deployment_guide.md` mục 0) — cách kiểm tra chắc chắn nhất:

```bash
grep -c "TestGatewayHandler_ConsecutiveTransactionsFromSameSenderAdvanceNonce" \
  execution/pkg/blockchain/tx_processor/gateway_handler_test.go
```

Phải ra `1` (hoặc lớn hơn). Nếu ra `0` — code hiện tại **chưa có** fix bug treo chain nghiêm
trọng nhất (xem `production_deployment_guide.md` mục 0) — **dừng lại**, cập nhật code trước.

### 0.3 Kiểm tra các fix cấu hình bảo mật (2026-08-27) — bỏ qua được nếu chỉ chạy devnet Phần A

Chỉ thật sự cần cho Phần B/C (triển khai thật) — nhưng chạy luôn cho chắc, không tốn gì:

```bash
grep -c "META_GATEWAY_BLS_KEY" execution/pkg/config/config.go
grep -c "mode: '0600'" deploy/ansible/roles/node_setup/tasks/main.yml
grep -c "random-gateway-bls-key" deploy/systemd/gen_single_chain.py
```

Cả 3 lệnh phải ra `≥1`. Nếu ra `0` ở bất kỳ lệnh nào: code chưa có đợt vá cấu hình bảo mật mới
nhất (5 biến môi trường `META_*` còn thiếu, quyền file khoá `0755`/`0644` world-readable,
`gateway_bls_key` dùng chung cho mọi chain) — xem đầy đủ tại
`note/security_variables_reference.md`. Không chặn devnet Phần A, nhưng **bắt buộc** trước khi
đi Phần B/C với khoá thật.

---

## Phần A — Devnet 1 máy (luyện tập / demo / CI, KHÔNG dùng cho giá trị thật)

Toàn bộ Phần A đã được chạy sống, xác nhận từng bước trong phiên làm việc viết tài liệu này
(2026-08-26) — mọi lệnh dưới đây là lệnh THẬT, không phải suy đoán.

### A1. Sinh + chạy Root Anchor (chain 9099, 4 validator)

```bash
cd deploy/systemd
bash setup_root_anchor.sh --no-build   # bỏ --no-build nếu chưa build ở Phần 0
bash root_anchor_data/start_all.sh
sleep 8
```

**✅ Xác nhận thành công:**

```bash
pgrep -af simple_chain | grep -v grep | wc -l    # phải ra 4 (4 validator)
curl -s -X POST http://127.0.0.1:9099 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

Phải nhận được JSON có trường `"result":"0x..."` (một số hex, không phải lỗi kết nối). Nếu
`curl` báo `Connection refused` — node chưa lên kịp, đợi thêm vài giây rồi thử lại; nếu vẫn
lỗi sau 30s, xem log (`root_anchor_data/node_0/logs/node.log`) tìm dòng `ERROR`.

### A2. Sinh + chạy 4 private chain (101–104)

```bash
bash setup_4_private_chains.sh --no-build
sleep 5
```

**✅ Xác nhận thành công:**

```bash
pgrep -af simple_chain | grep -v grep | wc -l    # phải ra 8 (4 Root Anchor + 4 private chain)
for port in 8546 8547 8548 8549; do
  curl -s -X POST http://127.0.0.1:$port \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
  echo ""
done
```

Cả 4 lệnh `curl` phải trả JSON hợp lệ. **Lỗi thật đã gặp:** script tự sinh (bên trong
`setup_4_private_chains.sh`) từng gọi nhầm tên file khởi động không tồn tại
(`start_nodes.sh` thay vì `start_single_chain.sh`) — nếu `pgrep` ra ít hơn 8, kiểm tra
`private_chains_data/chain_XXX/node.log` xem có dòng `No such file or directory` không (đã
vá ở PR #73, nhưng nếu code cũ hơn vẫn có thể gặp).

### A3. Đăng ký 4 chain — cả trên Root Anchor lẫn trên từng private chain

```bash
bash register_private_chains_t2.sh
```

**✅ Xác nhận thành công:** output phải có đúng **5 dòng** `✅ bootstrapFoundingChains
succeeded on ...` (Root Anchor + chain 101 + 102 + 103 + 104 — **không phải chỉ 1 dòng**).
Nếu chỉ thấy dòng cho Root Anchor mà thiếu 4 dòng còn lại, đây là dấu hiệu chưa có fix
`-target-rpcs` (PR #73) — `attestCommit()` giữa 2 private chain sau này sẽ luôn revert với
`"unknown source chain ID"`.

Xác nhận sâu hơn (tuỳ chọn nhưng khuyến nghị lần đầu triển khai): mỗi chain phải thấy ĐÚNG
committee của CHÍNH NÓ, không phải của chain khác — dấu hiệu của bug `config.LoadConfig`
singleton (đã vá PR #73) là mọi chain đều báo cùng 1 pubkey (của chain đầu tiên trong danh
sách). Cách kiểm tra: gọi view method `getChainRegistry(chainId)` qua `eth_call` tới từng RPC
private chain cho cả 4 `chainId`, so sánh trường `committeePubkeys` — phải KHÁC NHAU cho mỗi
`chainId`.

### A4. Chạy Relayer (tự động hoàn toàn, không cần thao tác gì thêm)

```bash
mkdir -p relayer_logs
nohup bash start_relayer_daemon.sh > relayer_logs/relayer.log 2>&1 &
disown
sleep 3
```

**✅ Xác nhận thành công:**

```bash
pgrep -af cross_chain_relayer | grep -v grep
grep "watching" relayer_logs/relayer.log
```

Phải thấy tiến trình đang chạy và dòng log `👀 [CROSS-CHAIN RELAYER] watching 12 chain
pair(s) for real outbound messages` (12 = 4×3, mọi cặp (nguồn, đích) có thứ tự trong số 4
chain). Nếu số cặp không phải 12, kiểm tra lại số chain đã cấu hình trong
`start_relayer_daemon.sh`'s biến `CHAINS`.

### A5. (Tuỳ chọn nhưng khuyến nghị) Test một giao dịch chuyển giá trị THẬT đầu-cuối

Bước này xác nhận **toàn bộ pipeline hoạt động đúng**, không chỉ từng tiến trình còn sống —
đây là bài test có giá trị nhất trong toàn bộ runbook. Một chain mới đăng ký có allocation
gửi-ra = 0 theo thiết kế (xem `production_deployment_guide.md` mục 3), nên cần cấp phát qua
governance trước — dùng `devnet_governance_timelock_seconds_override` (đã tự cấu hình 10
giây bởi `gen_single_chain.py`/`gen_root_anchor_chain.py`) để không phải chờ 72 giờ thật.

Quy trình đầy đủ (propose → vote ≥2/3 chain đã đăng ký → chờ timelock → executeProposal →
outbound → chờ relayer tự relay → kiểm tra số dư) cần viết một script Go ngắn gọi ABI trực
tiếp — không có sẵn dưới dạng 1 lệnh CLI đơn (đây là việc nên làm: đóng gói bài test này
thành 1 tool CLI thật, xem mục "Việc nên làm tiếp theo" cuối tài liệu). Các bước ABI chính
xác (tên hàm, tham số, cách ký) đã được xác nhận chạy đúng và mô tả chi tiết trong lịch sử
PR #73 (`ProposalAllocateSupply`, `propose`/`vote`/`executeProposal`, rồi `outbound`).

**✅ Xác nhận thành công (nếu chạy bài test):** số dư `eth_getBalance` của địa chỉ nhận trên
chain đích **tăng đúng bằng giá trị đã gửi** ở `outbound()` — xác nhận bằng 2 lần gọi
`eth_getBalance` (trước và sau), không chỉ tin log "relayed thành công" (log đó, trước PR
#73, từng báo "relayed" ngay cả khi `claimMessage` thất bại thầm lặng phía sau).

### A6. Dừng hệ thống

```bash
pkill -TERM -f cross_chain_relayer
bash private_chains_data/stop_all.sh
bash root_anchor_data/stop_all.sh
```

**✅ Xác nhận thành công:** `pgrep -af "simple_chain|cross_chain_relayer"` không còn kết quả
nào (ngoài chính lệnh `pgrep` đang chạy). Nếu vẫn còn tiến trình sau `stop_all.sh` (từng gặp
trong môi trường sandbox — script `stop` không phải lúc nào cũng dọn sạch), dùng
`kill -9 <pid>` cho từng tiến trình còn sót, xác nhận lại bằng `pgrep`.

---

## Phần B — Production nhiều máy: 1 private chain (Ansible)

Con đường đã kiểm chứng qua production thật nhiều lần (khác Phần A, vốn chỉ để luyện tập).

### B1. Chuẩn bị `inventory.yml`

```bash
cd deploy/ansible
cp inventory.example.yml inventory.yml   # nếu chưa có
# Sửa inventory.yml: IP thật, tài khoản SSH, mật khẩu (hoặc SSH key) cho từng máy
```

**✅ Xác nhận thành công:** SSH thủ công vào TỪNG máy trong `inventory.yml` bằng đúng
tài khoản đã khai báo, xác nhận đăng nhập được và có quyền `sudo` (role `systemd_services`
cần quyền này để đăng ký service) **trước khi** chạy Ansible — Ansible sẽ báo lỗi mơ hồ hơn
nhiều nếu SSH/sudo sai ngay từ đầu.

### B2. Mở port firewall (chỉ cần 1 lần, máy mới)

```bash
./ansible_deploy.sh --start --open-ports
```

**✅ Xác nhận thành công:** SSH vào 1 máy bất kỳ, chạy `sudo ufw status` — phải thấy các rule
`ALLOW` cho các port: Execution RPC, Execution P2P, Meta RPC, Consensus P2P, Consensus Peer
RPC, Consensus Metrics, Snapshot Server (danh sách chính xác, port cụ thể theo từng node, nằm
trong `deploy/systemd/node-N_keys/open_ports.sh` được Ansible sinh ra và chạy hộ).

### B3. Triển khai lần đầu (sinh key/genesis mới, xoá data cũ)

```bash
./ansible_deploy.sh --reset-all
```

**✅ Xác nhận thành công (từng bước, theo đúng 8 role Ansible chạy tuần tự — chi tiết:
`deploy/ansible/README.md` Phần 2):**
1. `local_build` — không lỗi biên dịch (giống Phần 0.1, nhưng chạy tại máy điều khiển).
2. `node_setup` — output Ansible báo `changed` (không phải `failed`) cho mọi host.
3. `systemd_services` — output báo các service `metanode-execution-N`/`metanode-consensus-N`
   đã `started`.

### B4. Xác nhận TỪNG NODE đã lên đúng (bắt buộc, đừng chỉ tin Ansible báo "ok")

SSH vào từng máy, với mỗi node `N` trên máy đó:

```bash
# 1. Service có đang chạy không (không bị crash-loop)
sudo systemctl status metanode-execution-N --no-pager
sudo systemctl status metanode-consensus-N --no-pager

# 2. Execution RPC có trả lời không
curl -s -X POST http://127.0.0.1:<execution_rpc_port> \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

# 3. Consensus Peer RPC có sống không (health check riêng, độc lập với Execution)
curl -s http://127.0.0.1:<consensus_peer_rpc_port>/health
```

**✅ Xác nhận thành công:**
- Cả 2 `systemctl status` phải là `active (running)`, KHÔNG phải `activating`/`failed`.
  Boot sequence đúng là Execution lên trước, ngủ 5 giây, rồi Consensus lên sau (Rust nối vào
  Execution qua FFI socket) — nếu Consensus `failed` ngay sau khi start, khả năng cao Execution
  chưa kịp mở socket, kiểm tra lại thứ tự/thời gian chờ.
- `eth_blockNumber` trả JSON hợp lệ (không `Connection refused`).
- `/health` trả đúng `{"status":"ok"}`.

### B5. Xác nhận CONSENSUS ĐỒNG BỘ thật (không chỉ từng node còn sống riêng lẻ)

Đây là bước hay bị bỏ qua nhất — 1 node có thể "sống" (RPC trả lời) mà KHÔNG đồng bộ được
với phần còn lại của cụm (chưa đạt quorum BFT). Với **mỗi node**, kiểm tra log tìm dòng trạng
thái hợp nhất:

```bash
tail -100 /path/to/node/logs/... | grep "UNIFIED STATE"
```

**✅ Xác nhận thành công:** dòng gần nhất phải có `Phase: Healthy`, và `Lag: 0` (hoặc rất
nhỏ, không tăng dần theo thời gian). Nếu thấy `Phase` khác `Healthy` (ví dụ đang chờ đồng bộ)
kéo dài liên tục nhiều phút không tự phục hồi, đây là dấu hiệu cụm CHƯA đạt BFT quorum — xem
`note/bft_fault_tolerance_node_count.md` để xác nhận số node tối thiểu (`n ≥ 4` để chịu được
`f=1` node lỗi) đã đủ chưa.

Kiểm tra thêm, so sánh **chiều cao block giữa các node** (phải khớp nhau, hoặc lệch tối đa
1-2 block do độ trễ mạng, KHÔNG được lệch xa và không thu hẹp lại):

```bash
# Chạy trên từng node, ghi lại kết quả để so sánh chéo
curl -s -X POST http://<ip-node>:<rpc-port> \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

Tốt hơn: dùng **Block Hash Checker** (đã có sẵn, xem B6) thay vì so tay — nó so cả
`parentHash`/`stateRoot`, không chỉ số block, nên phát hiện được fork sớm.

### B6. Bật giám sát liên tục (bắt buộc trước khi coi là "go-live", không phải sau sự cố đầu tiên)

```bash
./ansible_deploy.sh --start --all-monitors
```

**✅ Xác nhận thành công:** vào Telegram (kênh đã cấu hình `telegram_bot_token`/
`telegram_chat_id` trong `inventory.yml`), xác nhận có tin nhắn khởi động monitor. Chủ động
**test thử 1 lần**: dừng tạm 1 node (`./ansible_deploy.sh --stop --only-node N`), xác nhận
Telegram báo cảnh báo đỏ trong vòng vài phút (Health Monitor's chu kỳ kiểm tra), rồi khởi
động lại (`./ansible_deploy.sh --start --only-node N`) — đừng đợi tới sự cố thật mới biết
giám sát có hoạt động hay không.

### B7. Cập nhật code sau này (giữ nguyên key/data)

```bash
./ansible_deploy.sh --start
```

**✅ Xác nhận thành công:** lặp lại B4 — cả 2 service `active (running)`, `eth_blockNumber`
TIẾP TỤC TĂNG (gọi 2 lần cách nhau vài giây, số phải khác nhau) so với trước khi cập nhật.
**Đây là bước xác nhận quan trọng nhất sau bất kỳ lần cập nhật code nào** — số block ĐỨNG
YÊN dù RPC vẫn trả lời là chính xác dấu hiệu của bug treo chain đã tìm thấy hôm 2026-08-26
(xem `production_deployment_guide.md` mục 0): consensus tầng dưới vẫn khoẻ, nhưng tầng
executor không tạo block mới được nữa — `eth_blockNumber` KHÔNG BÁO LỖI, chỉ đứng yên, dễ bị
bỏ sót nếu chỉ kiểm tra RPC có trả lời hay không.

### B8. Dừng cụm

```bash
./ansible_deploy.sh --stop
```

**✅ Xác nhận thành công:** `systemctl status metanode-execution-N`/`metanode-consensus-N`
trên từng máy báo `inactive (dead)`, không phải `failed`.

---

## Phần C — Root Anchor nhiều tổ chức độc lập (production multi-org)

Quy trình đầy đủ nằm ở `note/runbook_root_anchor_genesis_ceremony.md` — đọc file đó trước
khi làm thật, tài liệu này chỉ tóm tắt các mốc xác thực chính (khớp
`production_deployment_guide.md` mục 5.2/5.3):

| Bước | Việc làm | ✅ Xác nhận thành công |
| :--- | :--- | :--- |
| 1 | Mỗi tổ chức tự sinh key + `founding_entry.json` | File **không chứa private key** — có test hồi quy `TestBuildFoundingEntry_NoPrivateKeyLeakage` đảm bảo, chạy `go test -run TestBuildFoundingEntry_NoPrivateKeyLeakage ./...` để tự xác nhận trước khi gửi file đi. |
| 2 | Gửi `founding_entry.json` riêng tư cho coordinator | Xác nhận qua kênh khác (điện thoại/gặp trực tiếp) rằng coordinator đã nhận đúng file, không qua group chat công khai. |
| 3 | Coordinator chạy `assemble_root_anchor assemble` | Sinh ra `genesis.json` + `genesis_digest.txt` — xác nhận `genesis_digest.txt` không rỗng, và số lượng entry trong `genesis.json`'s `validators` khớp đúng số tổ chức tham gia. |
| 4 | Công bố `genesis_digest.txt` qua kênh khác | Mỗi tổ chức nhận digest qua kênh KHÁC với kênh nhận `genesis.json` — nếu chỉ có 1 kênh, không có gì chống giả mạo giữa đường. |
| 5 | Mỗi node chạy `assemble_root_anchor verify --expect-digest ...` **trước khi start** | Lệnh phải thoát với exit code `0` và in ra thông báo khớp digest — **không start node nếu bước này thất bại**, dù chỉ 1 tổ chức. |
| 6 | Start node, đăng ký `bootstrapFoundingChains` | Coordinator gửi giao dịch NGAY sau khi nhận đủ file (giảm cửa sổ front-run, xem mục 5.3) — xác nhận qua `getChainRegistry()` trả `exists=true` cho đủ số chain sáng lập mong đợi. |
| 7 | Diễn tập trước khi làm thật | `bash deploy/systemd/rehearse_root_anchor_ceremony.sh` — script tự chạy toàn bộ quy trình trên với 4 thư mục key giả lập, xác nhận đạt BFT quorum thật. **Bắt buộc chạy qua 1 lần thành công trước khi làm ceremony thật**, không phải tuỳ chọn. |

---

## Phần D — Bảng tổng hợp "Node deploy thành công là gì?"

Dùng bảng này làm checklist nhanh sau BẤT KỲ lần triển khai/cập nhật nào, bất kể Phần A/B/C:

| Kiểm tra | Lệnh | Kết quả THÀNH CÔNG | Kết quả LỖI (dừng lại, điều tra) |
| :--- | :--- | :--- | :--- |
| Tiến trình còn sống | `pgrep -af simple_chain` / `systemctl status` | Đúng số tiến trình mong đợi, `active (running)` | Thiếu tiến trình, hoặc `failed`/crash-loop |
| Execution RPC trả lời | `eth_blockNumber` qua `curl` | JSON `{"result":"0x..."}` | `Connection refused` / timeout |
| Block height ĐANG TĂNG | Gọi `eth_blockNumber` 2 lần cách nhau ≥10s | Số khác nhau, tăng dần | Số ĐỨNG YÊN dù RPC vẫn trả lời (dấu hiệu bug treo chain) |
| Consensus khoẻ | `grep "UNIFIED STATE"` trong log | `Phase: Healthy`, `Lag: 0` | `Phase` khác `Healthy` kéo dài, hoặc `Lag` tăng dần |
| Peer RPC khoẻ | `curl .../health` | `{"status":"ok"}` | Không trả lời / lỗi kết nối |
| (Cross-chain) Registry đúng | `getChainRegistry(chainId)` qua `eth_call` mỗi chain | Mỗi chain thấy ĐÚNG committee của các chain khác (kể cả chính nó) | Thiếu entry, hoặc mọi chain trả về CÙNG 1 pubkey (dấu hiệu bug `config.LoadConfig` singleton) |
| (Cross-chain) Relayer đang chạy | `pgrep cross_chain_relayer` + log `watching N chain pair(s)` | N = (số chain)×(số chain − 1) | Tiến trình không tồn tại, hoặc N sai |
| (Cross-chain) Giá trị chuyển thật | `eth_getBalance` trước/sau 1 giao dịch `outbound()` đầy đủ | Số dư đích tăng ĐÚNG bằng giá trị gửi | Số dư không đổi dù log báo "relayed thành công" |

---

## Phần E — Xử lý sự cố nhanh (lỗi thật đã gặp, không phải lý thuyết)

| Triệu chứng | Nguyên nhân thật đã xác nhận | Cách sửa |
| :--- | :--- | :--- |
| `node-0.log`/`node.log` không thấy log giao dịch nào, dù chain đang chạy tốt | File đó **chỉ chứa log khởi động**. Toàn bộ log theo từng giao dịch/block nằm ở file khác | Xem `logs/execution/<YYYY-MM-DD>/execution.log` (cùng thư mục node, phân theo ngày) — đừng tốn thời gian nghi ngờ code sai chỉ vì không thấy log ở `node-0.log`. |
| `eth_sendRawTransaction`: `"account 0x... has no BLS public key registered on-chain"` | Tài khoản gửi giao dịch chưa có `publicKeyBls` hợp lệ trong genesis/alloc CỦA CHÍNH CHAIN đang gửi tới — đây là gate chung cho MỌI giao dịch, không riêng cross-chain | Với dev account: dùng tài khoản trong `dev_accounts.json` (đã có alloc đúng). Với tài khoản tự tạo: phải được đăng ký `publicKeyBls` (48-byte min-pk/G1, hex có tiền tố `0x`) trong genesis của ĐÚNG chain đang gửi giao dịch tới. |
| `attestCommit()` revert `"unknown source chain ID: chain N"` | `ChainRegistry` là state CỤC BỘ theo từng chain — chain đích chưa từng biết về chain nguồn | Chạy `register_chains` với `-target-rpcs` liệt kê MỌI private chain, không chỉ Root Anchor (Phần A3). |
| `attestCommit()` revert `"aggregate amount exceeds source chain allocation ceiling... available 0"` | KHÔNG phải bug — chain nguồn chưa từng được cấp phát allocation gửi-ra (thiết kế fail-closed) | Chạy `ProposalAllocateSupply` qua governance thật trên CHAIN ĐÍCH (propose → vote ≥2/3 chain đã đăng ký → chờ timelock → executeProposal) trước khi thử lại. Devnet: dùng `devnet_governance_timelock_seconds_override` để không phải chờ 72h thật. |
| `vote()` revert `"signer is not a member of chain N's current committee"` | Rất có thể do bug `register_chains`/`config.LoadConfig` singleton — mọi chain bị gán nhầm committee của chain đầu tiên | Xác nhận fix đã có trên `dev` (Phần 0.2, grep, không tin theo PR). Nếu đã có mà vẫn gặp, kiểm tra lại đúng `Databases.BLSPrivateKey` trong `config.json` của chain đang vote khớp với key dùng để build committee entry lúc `register_chains`. |
| Block height đứng yên vĩnh viễn, RPC vẫn trả lời bình thường, không có lỗi rõ ràng trong log | Bug treo chain: giao dịch barrier (gateway/validator contract) thứ 2 liên tiếp từ CÙNG 1 tài khoản không bao giờ được chấp nhận do nonce không tăng | Xác nhận fix đã có trên `dev` (Phần 0.2, grep tên test cụ thể). Log dấu hiệu: `TxsProcessor2: Race condition detected! pool_size=1->1, but retrieved 0 transactions` lặp lại liên tục không dừng. |
| `register_chains` báo thành công cho TẤT CẢ chain nhưng thực ra mọi chain trả về CÙNG 1 committee pubkey | Bug `config.LoadConfig` singleton — hàm này cache qua `sync.Once` toàn tiến trình, gọi 2 lần trở lên trong 1 lần chạy chỉ đọc đúng config CỦA LẦN GỌI ĐẦU | Xác nhận fix đã có trên `dev`; dùng `getChainRegistry()` (Phần A3, Phần D) để tự kiểm chứng thay vì chỉ tin dòng log "succeeded". |
| Consensus `failed` ngay sau khi Execution start | Thứ tự/thời gian khởi động sai — Consensus (Rust) nối vào Execution (Go) qua FFI socket, phải đợi Execution mở socket trước | Đảm bảo thứ tự start Execution trước, đợi ≥5 giây, rồi mới start Consensus (Ansible role `systemd_services` đã tự làm đúng thứ tự này — nếu tự viết systemd unit thủ công, sao chép đúng `After=`/`ExecStartPre=sleep 5` từ template Ansible sinh ra). |
| Rust consensus log lặp lại `Error starting consensus server: Os { code: 98, kind: AddrInUse, ... }` rồi panic `Failed to start consensus server within required deadline`, `eth_blockNumber` đứng ở `0x0` (thường gặp nhất khi chạy `setup_root_anchor.sh`, nhiều validator cùng chain) | 2 khả năng, đã gặp cả 2: (1) checkout cũ hơn 2026-08-25, chưa có fix tách `peer_rpc_port` khỏi `network_port` trong `gen_root_anchor_chain.py` (2 listener khác nhau từng bị gán trùng số cổng — "Layer C", xem `production_deployment_guide.md` mục 0); (2) process cũ từ lần chạy trước chưa bị dọn, vẫn giữ cổng | Trước tiên: `grep -n "peer_rpc_port.*29200" deploy/systemd/gen_root_anchor_chain.py` — phải ra kết quả (nếu không, `git pull` code mới hơn rồi generate lại config). Nếu đã có mà vẫn lỗi: `ss -tlnp \| grep <port>` tìm PID đang giữ cổng, kill rồi chạy lại `start_all.sh`. |
| Devnet treo/không phản hồi khi restart từ dữ liệu cũ trên máy chia sẻ | Đã điều tra kỹ 1 lần (pprof, I/O, ulimit) — kết luận: KHÔNG phải lỗi code, do 1 tiến trình KHÁC không liên quan chiếm 653% CPU / 110GB+ RAM trên cùng máy | Không chạy T2/production trên máy/VM chia sẻ tải nặng không kiểm soát được — dùng máy/VM riêng biệt (chi tiết: `note/cross_chain_production_readiness_plan.md`). |
| `execution.json`/khoá thật world-readable trên server (`ls -l` thấy `-rw-r--r--` hoặc thoáng hơn) | Quyền file cũ trong `deploy/ansible` (`0755`/`0644`) — đã siết về `0600`/`0700` (2026-08-27) | Xác nhận `grep "mode: '0600'" deploy/ansible/roles/node_setup/tasks/main.yml` ra kết quả (Phần 0.3). Nếu không, `git pull` code mới hơn trước khi deploy thật. |

---

## Việc nên làm tiếp theo (không chặn, nhưng nên làm)

- **Đóng gói bài test A5 (chuyển giá trị thật đầu-cuối) thành 1 CLI tool thật** thay vì phải
  tự viết script Go mỗi lần — hiện tại đây là thao tác thủ công tốn thời gian nhất trong toàn
  bộ runbook, và là bài test giá trị nhất để xác nhận 1 cụm mới triển khai hoạt động đúng.
- Chạy thủ công **Block Hash Checker** (không cần `--all-monitors`) ngay sau B5 để tự xác
  nhận fork/lệch state thay vì chỉ tin log riêng lẻ từng node:
  ```bash
  cd deploy/ansible/monitors/block_hash_checker
  go run main.go --watch --interval 5s \
    --nodes "node0=http://<ip0>:<rpc0>,node1=http://<ip1>:<rpc1>,..."
  ```
  **✅ Xác nhận thành công:** output liên tục báo các node cùng `blockNumber`/`hash`/
  `parentHash`/`stateRoot` khớp nhau (lệch tối đa 1-2 block do độ trễ mạng là bình thường,
  KHÔNG được lệch `hash` ở CÙNG 1 số block — đó là fork thật).
- Xem `note/production_deployment_guide.md` mục 0 để biết 2 điều kiện chặn cứng còn lại
  trước khi coi hệ thống cross-chain sẵn sàng cho **giá trị thật trên mainnet** (P5 — audit
  độc lập; T2 — chạy thật trên nhiều máy vật lý, không chỉ nhiều tiến trình 1 máy).
