# Biến Cấu Hình Bảo Mật (Secrets) — Bảng Tham Chiếu Duy Nhất

Viết 2026-08-27, sau khi rà toàn bộ config (`execution/pkg/config/config.go`, `deploy/systemd/`,
`deploy/ansible/`) để trả lời câu hỏi: cấu hình có đang rối không, và biến bảo mật có cấu trúc
rõ ràng không. Tài liệu này là **nguồn duy nhất** liệt kê mọi trường nhạy cảm trong hệ thống —
tên field JSON, biến môi trường override (nếu có), dùng để làm gì, và khuyến nghị vận hành.
Không lặp lại nội dung `note/production_deployment_guide.md`/`deployment_runbook_step_by_step.md`.

## 1. Bảng đầy đủ mọi trường nhạy cảm trong `config.json` (Go execution layer)

| Field JSON | Struct Go | Biến môi trường override | Dùng để làm gì |
|---|---|---|---|
| `private_key` | `SimpleChainConfig.PrivateKey` | `META_PRIVATE_KEY` ✅ (có sẵn) | Khoá secp256k1 vận hành chính của node |
| `reward_sender_private_key` | `RewardSenderPrivateKey` | `META_REWARD_PRIVATE_KEY` ✅ (có sẵn) | Khoá ký giao dịch phát thưởng mining |
| `securepassword` | `Securepassword` | `META_SECURE_PASSWORD` ✅ (có sẵn) | Mật khẩu bảo vệ keystore |
| `Databases.BLSPrivateKey` | `DatabasesConfig.BLSPrivateKey` | `META_BLS_PRIVATE_KEY` ✅ **mới thêm 2026-08-27** | Khoá BLS ký block / attestCommit (CommitAttestationWorker) |
| `cross_chain.root_anchor_submitter_private_key_hex` | `CrossChainConfig.RootAnchorSubmitterPrivateKeyHex` | `META_ROOT_ANCHOR_SUBMITTER_PRIVATE_KEY_HEX` ✅ **mới thêm 2026-08-27** | Khoá secp256k1 trả gas khi gửi giao dịch LÊN Root Anchor (registerCommitteePop/submitCommitteeAttestation) |
| `gateway_bls_key` | `GatewayBLSKey` | `META_GATEWAY_BLS_KEY` ✅ **mới thêm 2026-08-27** | Khoá BLS ký bảo lãnh giao dịch bị chặn (chỉ dùng khi `enable_private_gateway=true`) — xem cảnh báo mục 3 |
| `master_password` | `MasterPassword` | `META_MASTER_PASSWORD` ✅ **mới thêm 2026-08-27** | Mật khẩu master cho BLS keystore theo từng địa chỉ |
| `app_pepper` | `AppPepper` | `META_APP_PEPPER` ✅ **mới thêm 2026-08-27** | Pepper trộn thêm khi hash mật khẩu |
| `tls_cert` / `tls_key` | `TlsCert` / `TlsKey` | (không cần — chỉ là đường dẫn file, không phải khoá inline) | Đường dẫn chứng chỉ/khoá TLS cho RPC HTTPS |

**Trước 2026-08-27: chỉ 3/9 trường có đường thoát env var** (`private_key`,
`reward_sender_private_key`, `securepassword`) — 6 trường còn lại (kể cả khoá BLS ký block, khoá
trả gas Root Anchor thật) **bắt buộc phải nằm thẳng trong file JSON**, không có cách nào khác.
Đã vá bằng cách thêm 5 override còn thiếu (`pkg/config/config.go`, cùng PR với tài liệu này) —
theo đúng pattern đã có sẵn, không đổi hành vi mặc định (JSON vẫn hoạt động y hệt nếu không set
env var nào).

**Khuyến nghị vận hành thật**: dùng `EnvironmentFile=` của systemd (file `0600`, chỉ user chạy
service đọc được) để nạp các biến `META_*` này, thay vì để khoá thật nằm trong
`execution.json`/`consensus.toml` trên đĩa — ví dụ thêm vào `metanode-execution.service`:
```ini
EnvironmentFile=-/opt/metanode/node-%i/secrets.env
```
(dấu `-` đầu = không lỗi nếu file chưa tồn tại, cho phép chuyển dần từng node một). File
`secrets.env` chỉ chứa `META_PRIVATE_KEY=...` v.v., quyền `0600`, sở hữu bởi user `metanode`,
**không commit vào git**. Hiện tại `deploy/ansible`/`deploy/systemd` CHƯA dùng cơ chế này (khoá
vẫn nằm trong file JSON) — đây là khuyến nghị cho bước tiếp theo, chưa phải hiện trạng.

## 2. Quyền file trên server thật — đã có lỗ hổng, đã vá (2026-08-27)

`deploy/ansible` copy các file trên vào 2 nơi trên server: thư mục stage
(`/opt/metanode-deploy/node-N_keys/`) rồi tới đích thật
(`/opt/metanode/node-N/{config,keys}/`). Cả 2 nơi **trước đây đều world-readable**:
- Stage: `mode: '0755'` — bất kỳ user cục bộ nào trên máy cũng đọc được toàn bộ khoá.
- Đích thật (`execution.json`, `consensus.toml`, `protocol_key.json`, `network_key.json`,
  `authority_key.json`, `eth_key.json`): `mode: '0644'` — dù sở hữu bởi user `metanode`
  (đúng user chạy service), quyền `0644` vẫn cho MỌI user khác trên máy đọc được.

**Đã vá**: cả 2 nơi giờ `0600` (thư mục stage: `0700`) — service vẫn chạy bình thường (chạy
bằng chính user `metanode` sở hữu file, Ansible copy bằng quyền root qua `become: yes` nên
không bị ảnh hưởng bởi việc siết quyền này). Không có tác dụng phụ, chỉ bớt quyền đọc thừa.

## 3. 1 vấn đề đã sửa (2026-08-27), 1 vấn đề CHƯA sửa — cần bạn quyết định

### 3.1 [ĐÃ VÁ] `gateway_bls_key` — cùng 1 giá trị hardcode trên MỌI chain, kể cả tool ceremony thật

Cả 3 script sinh cấu hình chính thức (`gen_root_anchor_chain.py`, `gen_single_chain.py`, VÀ
`gen_validator_entry.py` — công cụ dùng cho vận hành viên ceremony THẬT) đều ghi cứng cùng 1
giá trị `gateway_bls_key` (`2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b`),
không có cờ CLI nào để sinh khoá riêng. Chỉ ảnh hưởng khi `enable_private_gateway=true`
(tính năng "Private Gateway"/speculative execution, mặc định TẮT) — nhưng nếu tính năng này bật
trên bất kỳ chain nào dùng tooling mặc định, khoá ký "bảo lãnh" là công khai (nằm sẵn trong mã
nguồn mở), ai cũng giả mạo được.

**Đã vá**: cả 3 script giờ có 2 cờ mới:
- `--gateway-bls-key <hex>`: truyền thẳng 1 khoá cụ thể.
- `--random-gateway-bls-key`: tự sinh khoá BLS ngẫu nhiên riêng cho từng node (dùng chính
  `metanode keytool generate validator`, không phát minh cơ chế crypto mới).

**Mặc định KHÔNG đổi** khi không truyền cờ nào — vẫn dùng đúng giá trị hardcode cũ, vì
`dev_accounts.json` của `gen_single_chain.py` đã đăng ký sẵn 1 `publicKeyBls` khớp CHÍNH XÁC
với khoá hardcode này (để giao dịch devnet thường qua được kiểm tra đăng ký BLS on-chain mà
không cần luồng đăng ký thật) — đổi mặc định sẽ âm thầm phá luồng smoke-test devnet hiện có.
Đã verify sống: chạy cả 2 chế độ, xác nhận mặc định ra đúng giá trị cũ (không đổi), còn
`--random-gateway-bls-key` ra khoá hex hợp lệ khác nhau cho từng node.

**Việc còn lại cho bất kỳ triển khai thật nào**: nhớ truyền `--random-gateway-bls-key` (hoặc
`--gateway-bls-key` với khoá tự quản lý) khi generate config cho 1 chain thật có bật
`enable_private_gateway` — script không tự ép buộc việc này, chỉ cung cấp lựa chọn.

### 3.2 [ĐÃ GỠ 2026-08-27] `pkg/devicekey/DeviceKey.go` — secret Telegram bot token hardcode trong mã nguồn

**Quyết định**: gỡ bỏ hẳn, không sửa lại. Người dùng xác nhận: hệ thống chỉ nên có **1 cơ chế
thông báo Telegram duy nhất** — cơ chế thật, đã dùng trong `deploy/ansible/monitors/`
(`TELEGRAM_BOT_TOKEN`/`TELEGRAM_CHAT_ID` đọc từ `.env`, không hardcode, đã gitignore đúng cách)
— không cần và không nên có 1 cơ chế Telegram thứ hai nằm trong chính binary node.

**Đã xoá**: toàn bộ `pkg/devicekey/` (hàm `telegramNoti` chứa bot token hardcode, `CalculateUUID`
đọc khoá SSH thật + cơ chế "device-activation" có ngày hết hạn cứng 2026-10-01), cùng 2 điểm gọi
(`initializeDeviceKey` trong `cmd/simple_chain/main.go` và `cmd/exec_node/main.go`), cờ `-ssh-key`
không còn dùng ở đâu (xác nhận bằng grep, không có script deploy nào từng truyền cờ này), và 3
biến `BuildTime`/`EnvDecryptKey`/`EnvFirstKey` không còn build script nào set. Xác nhận trước khi
xoá: không có nơi nào khác trong repo import bất kỳ hàm nào từ `pkg/devicekey` ngoài 2 điểm gọi
trên — xoá không ảnh hưởng gì khác. `go build/vet/test` sạch sau khi xoá.

## 4. Config sprawl ở `execution/cmd/{simple_chain,exec_node,mining,rpc-client}/`

14 file `config*.json`/`genesis*.json` nằm thẳng trong các thư mục `cmd/` — đây là config chạy
dev cục bộ thật (mặc định flag `-config` của `simple_chain` chính là `config.json`, nhiều
`run*.sh` trỏ thẳng vào `config-master*.json`), không phải rác — nhưng:
- Chứa 9 private key hardcode khác nhau, không đường nào nói rõ "đây là khoá test/devnet công
  khai, đừng dùng cho thật" (khác với `deploy/systemd/genesis.json.example` — có hậu tố
  `.example` rõ ràng).
- Field không nhất quán giữa các file: `config-tps.json` có CẢ `chainId` LẪN `chain_id` (2 tên
  khác nhau cho cùng khái niệm, dễ gõ nhầm field không tồn tại mà không có lỗi rõ ràng).

Không sửa (không phải rủi ro cao — chỉ dùng cho dev cục bộ, không nằm trong đường triển khai
thật của `deploy/`), nhưng nếu muốn dọn: gộp về 1 file `config.dev.json.example` + README ghi rõ
"khoá devnet công khai, không dùng thật" là đủ, không cần xoá các biến thể theo node.
