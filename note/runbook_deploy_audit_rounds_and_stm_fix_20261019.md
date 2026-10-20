# Runbook: triển khai đợt sửa execution (audit round 2/3 + Block-STM) và thay đổi RPC, 2026-10-19

Trạng thái: **DỰ THẢO, chưa diễn tập trên cụm nhiều máy.** Runbook này không thay thế hai tài liệu nền; nó chỉ chọn quy trình đúng và liệt kê phần mới:

- [`runbook_cutover_chain991_proto.md`](./runbook_cutover_chain991_proto.md): wipe + redeploy một lần (các pha 0–7, rollback, Go/No-Go).
- [`block_stm_estimate_fixes_upgrade_runbook.md`](./block_stm_estimate_fixes_upgrade_runbook.md): nâng cấp thay đổi consensus Block-STM, mục 6 là bản sửa mới nhất.

Nhãn bằng chứng: `[ĐO]` có log hoặc lệnh chạy trong phiên 2026-10-19; `[ĐỌC CODE]`; `[CHƯA ĐO]`.

---

## 1. Bản phát hành

Commit đề xuất: **`7e1c95ef`** (gồm `9c9caa93` và `c96640f4`; CI xanh ở commit này; các commit sau `9c9caa93` là mempool PR #179 và sửa test, không đổi luật consensus `[ĐỌC CODE]`) (hoặc commit mới hơn trên `dev` đã qua lại các kiểm tra ở mục 3). Mọi binary của mọi node phải build từ **cùng một commit**; ghi `sha256sum` từng binary vào biên bản triển khai.

### 1.1. Thay đổi consensus (kết quả thực thi của một số block thay đổi)
| Commit | Nội dung |
|---|---|
| `e73617c8`, `e7acb645`, `c77829da` | Một luật phí/nonce/gas cho native fast path và Block-STM; không thưởng leader cho phí chưa trả; không ghi state từ tx bị từ chối; tiêu nonce cho tx blob native bị từ chối |
| `b0fc0528` | Audit round 3: receipt không báo gas chưa trừ, tiêu nonce cho tx barrier hỏng, phân loại đủ địa chỉ barrier (0x1001, 0x1002, 0x1003, 0x72) |
| `e5e6fc85` | `senderExisted` dùng state thật; miễn phí đăng ký chỉ khi đúng selector `setBlsPublicKey` và khóa 48 byte |
| `c96640f4` | Block-STM: vô hiệu hóa tx đã đọc khóa mà lần chạy lại không còn ghi (hết nondeterminism, xem STM runbook mục 6) |
| `9c9caa93` | `StakeStateDB.getOrCreateValidatorState`: `registerValidator` ngay sau `deregisterValidator` của cùng địa chỉ trong một block từng gây **panic nil pointer** khi thực thi block (không có `recover`); nay tạo validator mới thay cho tombstone. Mọi validator chạy block đó đều bị ảnh hưởng, nên phải nâng cấp đồng loạt |

Hệ quả `[ĐỌC CODE]`: node chạy code cũ và node chạy code mới có thể ra kết quả khác nhau cho cùng một block. **Không nâng cấp cuốn chiếu.** Thà pending chứ không fork.

### 1.2. Thay đổi không đổi consensus (RPC / cấu hình)
| Thay đổi | Việc vận hành cần làm |
|---|---|
| `mtn_sendCrossChainTransfer` chỉ chạy khi config có `"devnet_rpc": true` | Mạng thật: để `false`. Template `exec_config.json.j2` giờ đặt `true` chỉ khi `metanode_env=devnet`, mặc định `false` |
| `eth_verifyHistoricalRoot` bị gỡ; thay bằng `admin_verifyHistoricalRoot(password, block)` | Đặt mật khẩu admin (xem dưới); script ngoài repo đang gọi RPC cũ sẽ nhận "method does not exist" |
| `/pipeline/reset` cần header `X-Admin-Password`, không `Origin` | `pipeline_monitor -reset` cần thêm `-password` |
| `eth_getProof` block lịch sử: chỉ một lần dựng lại tại một thời điểm; trả lỗi "retry shortly" khi bận; từ chối khi root dựng lại ≠ `stateRoot` | Client cần retry; không phải thay đổi bắt buộc cho node |
| Mật khẩu admin | Template `exec_config.json.j2` **không** đặt `securepassword`. Đặt qua biến môi trường `META_SECURE_PASSWORD` (ghi đè config, `execution/pkg/config/config.go`), không ghi vào file. Không đặt thì mọi RPC `admin_*` và `/pipeline/reset` trả "chưa cấu hình mật khẩu" (an toàn nhưng không dùng được) |

---

## 2. Chọn quy trình theo trạng thái cụm

| Trạng thái cụm | Quy trình |
|---|---|
| Dữ liệu từ **trước** đợt eth-only (ví dụ cụm 231/230 nâng cấp lên `56df6d18` ngày 2026-09-29; có 452 commit từ đó tới `HEAD`) | Dữ liệu cũ không replay được. Dùng **cutover có wipe**: [`runbook_cutover_chain991_proto.md`](./runbook_cutover_chain991_proto.md), một lần, đồng thời parent và mọi cụm exec |
| Cụm đã chạy code eth-only + cutover, dữ liệu hợp lệ | **Dừng toàn cụm → thay binary → khởi động**, giữ dữ liệu. Theo STM runbook mục 1–2, không rolling |
| Cụm devnet/CI chạy bằng `ci.sh` | `./ci.sh run-now --reset` (build + reset + test); không cần runbook này |

Nếu một block cũ từng dính lỗi nondeterministic, code mới replay có thể ra state khác state đã commit (STM runbook mục 1.2). Trước khi giữ dữ liệu cũ, chạy replay đối chiếu (mục 4, bước 3).

---

## 3. Kiểm tra trước khi triển khai (pre-flight)

Đã làm `[ĐO]` (máy 192.168.1.232, một máy):
- `build_check.sh` ở `c96640f4`: ALL BUILDS PASSED 5/5, 40 giây, 0 warning, exit 0 (log `/tmp/claude-1001/build_check_20261019b.log`).
- `go test ./pkg/blockchain/... ./pkg/grouptxns/` pass; `go test -race -run 'TestTrueBlockSTM|TestExecDifferential'` sạch.
- Cụm 4 validator BFT local dựng từ binary `dev`: 5 đợt burst × 4000 tx, confirmed 4000/4000, loss 0, `zero_fork_verified=True`, `node_roots_consistent=True` (`/tmp/claude-1001/-home-abc-chain-n-metanode/f559d18a-31d9-4c06-80c5-c4f912349eee/scratchpad/e2e_out2/b1..b5.json`). 19/19 block dựng lại khớp `stateRoot`.
- Differential test: native fast path vs Block-STM (300 block ngẫu nhiên) và block lẫn EVM/barrier (120 block) khớp; block barrier có calldata hợp lệ: 200 block khớp oracle tuần tự, `-race` sạch (2026-10-20, `go test ./pkg/blockchain/tx_processor/ -run ValidBarrierTxs`); regression test của lỗi STM fail trên code cũ.

**Chưa làm `[CHƯA ĐO]`, phải làm hoặc chấp nhận rủi ro bằng văn bản:**
1. Chạy trên nhiều máy thật, có độ trễ mạng, và chạy dài (soak).
2. Replay đối chiếu trên bản sao dữ liệu thật (mục 4, bước 3).
3. Diễn tập đủ các pha của cutover runbook trên cụm cô lập cùng inventory.
4. Differential test (`exec_differential_barrier_test.go`) phủ calldata hợp lệ của 8 hàm validator contract (0x1001, riêng `distributeRewards` luôn bị từ chối theo thiết kế), gateway `outbound` (0x1002) và sự kiện rollup (0x72), nhưng **chưa phủ** parent-chain gateway (0x1003) và các hàm gateway khác ngoài `outbound` `[CHƯA ĐO]`.

Điều kiện Go/No-Go trước khi dừng cụm: `git status` sạch; `git rev-parse HEAD` đúng commit đã ghi; hash binary đã ghi; backup đã kiểm (`tar -tzf`) trên mọi host; có người trực.

---

## 4. Các bước (cụm giữ dữ liệu, dừng toàn cụm)

Mọi lệnh dưới đây có trong `OPERATIONS_GUIDE.md` và đã đối chiếu cờ với script `[ĐỌC CODE]`.

1. **Đóng cổng client** ở tầng proxy/firewall (cutover runbook pha 1).
2. **Dừng toàn cụm**, exec trước, parent sau:
   ```bash
   cd deploy/ansible_clusters
   ./deploy_clusters.sh --stop --exec-only
   ./deploy_clusters.sh --stop --parent-only
   ```
   Cụm đơn BFT (`deploy/ansible`): `./ansible_deploy.sh --stop`. Xác nhận `systemctl is-active metanode-<host>` = `inactive` ở mọi host; không dùng `pkill`.
3. **Replay đối chiếu (khuyên dùng, trên bản sao dữ liệu, không trên dữ liệu đang phục vụ).** Với binary cũ và mới:
   ```bash
   cd execution
   go run ./cmd/tool/tz_replay_check -config <config.json> -from <A> -to <B> -out /tmp/replay_old.json   # binary cũ
   go run ./cmd/tool/tz_replay_check -config <config.json> -from <A> -to <B> -out /tmp/replay_new.json   # binary mới
   go run ./cmd/tool/tz_replay_check -compare-a /tmp/replay_old.json -compare-b /tmp/replay_new.json
   ```
   Có block khác nhau nghĩa là dữ liệu cũ không khớp luật mới → không giữ dữ liệu, chuyển sang cutover có wipe (mục 2, dòng 1).
4. **Backup** từng host (cutover runbook pha 3) và lưu binary cũ: `cp -a /opt/metanode/bin /opt/metanode/bin.pre_$(date +%Y%m%d)`.
5. **Thay binary** cùng commit trên mọi host, kiểm `sha256sum` khớp biên bản. Cụm ansible: `./deploy_clusters.sh --deploy` (đa cụm) hoặc `./ansible_deploy.sh --start` (cụm đơn, giữ dữ liệu). Đặt `METANODE_ENV=production` (`--env=production`) và `META_SECURE_PASSWORD` qua biến môi trường của unit/vault.
6. **Khởi động parent trước, exec sau**; đợi độ cao tăng đều.
7. **Mở cổng** chỉ khi mục 5 đạt.

---

## 5. Kiểm tra sau triển khai (Go/No-Go)

| Kiểm tra | Cách làm | Đạt khi |
|---|---|---|
| Cụm sống | `./deploy_clusters.sh --status`; `eth_blockNumber` mọi node | Độ cao tăng đều, bằng nhau (± vài block) |
| Không fork | `deploy/ansible/monitors/block_hash_checker` (`go run main.go --watch --interval 5s --config config-m-nodes.json --no-stop-flag`) | Không `ERR`; hash + stateRoot khớp mọi node |
| Smoke tải | `secp_tps_blast ... -verify-parity` với `-count` nhỏ | `loss = 0`, dòng `ZERO-FORK OK: all N validators agree` |
| RPC mới | `eth_verifyHistoricalRoot` → "does not exist"; `admin_verifyHistoricalRoot` sai mật khẩu → "invalid credentials"; `mtn_sendCrossChainTransfer` → "devnet-only RPC is disabled" (production) | Đúng như bên trái |
| Dựng lại state | `admin_verifyHistoricalRoot` cho vài block | `match=true` |
| Cấu hình | `grep devnet_rpc` trong config mọi node production | Không có `true` |

Dừng và giữ trạng thái PENDING (không ép node chạy tiếp) nếu có node lệch hash hoặc không tiến triển; cần ≥ 2f+1 node cùng khớp thì mạng mới tiếp tục.

## 6. Rollback

Binary cũ **không** an toàn để chạy lại trên dữ liệu đã được binary mới xử lý block (kết quả có thể khác). Rollback = dừng toàn cụm, khôi phục **cả binary lẫn bản backup dữ liệu** ở bước 4, khởi động lại toàn cụm (cutover runbook mục 5). Không rollback từng node.

## 7. Điều chưa biết
- Chưa thấy lỗi STM xảy ra trên cụm thật và chưa đo tác động lên TPS của `c96640f4` `[CHƯA ĐO]`.
- Chưa có cơ chế kích hoạt theo block height cho thay đổi luật; mọi thay đổi consensus hiện cần deploy đồng loạt `[CHƯA ĐO]`.
