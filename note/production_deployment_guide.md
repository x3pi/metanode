# Tài liệu Triển khai Hệ thống Metanode — Từ Devnet đến Production

Tài liệu này gom lại thành **một quy trình duy nhất, đầu-cuối** những gì đã có rải rác ở
`deploy/ansible/`, `deploy/systemd/`, và các runbook trong `note/`, để trả lời câu hỏi
"triển khai hệ thống thật (private chain + Root Anchor cross-chain) thì làm theo thứ tự
nào". Nó không thay thế các tài liệu chi tiết hơn — mỗi bước đều trỏ tới tài liệu gốc để
tra cứu chi tiết cờ/tham số.

**Đọc mục 0 trước khi làm bất cứ điều gì khác.** Hệ thống có 2 phần rất khác nhau về mức độ
sẵn sàng: phần **1 chain đơn (private chain)** đã chạy production nhiều lần qua Ansible; phần
**cầu nối liên chuỗi Root Anchor** mới xong Phase 0 bảo mật nội bộ, **chưa qua audit độc lập
(P5)**, và **chưa từng chạy trên mạng nhiều máy thật**.

---

## 0. Tóm tắt điều hành (đọc trước)

| Thành phần | Sẵn sàng cho | Chưa sẵn sàng cho |
| :--- | :--- | :--- |
| 1 private chain đơn (Go execution + Rust consensus, Ansible/systemd) | Production nội bộ, testnet, mainnet doanh nghiệp riêng lẻ | — |
| Root Anchor / cầu nối liên chuỗi (`GatewayPrecompile`, governance, relayer) | Testnet nội bộ, diễn tập ceremony, demo | **Giá trị thật** cho tới khi qua P5 (security review độc lập) — xem `note/cross_chain_production_readiness_plan.md` |

**Cổng bắt buộc trước khi cho giá trị thật chạy qua Root Anchor** (không phải gợi ý, là điều
kiện chặn — xem `note/cross_chain_root_anchor_architecture.md` mục 8, lộ trình P0-P8):
0. **🔴 QUAN TRỌNG NHẤT, phát hiện 2026-08-25:** lớp xác minh mật mã (BLS/Merkle/anti-fraud)
   đã xong và test kỹ, nhưng **chưa một dòng code nào thực sự di chuyển giá trị thật** —
   `outbound()`/`claimMessage()`/`verifyAndExecute()`/`refund()`/`claimDeadChainBalance()`
   chỉ sửa 1 ledger nội bộ trong bộ nhớ, chưa bao giờ gọi
   `AccountStateDB.AddBalance`/`SubBalance` hay `VmProcessor.ProcessNativeMintBurn` (hàm đã
   có sẵn, đúng như kiến trúc mô tả, nhưng 0 nơi gọi nó). Nghĩa là: mọi lần "chuyển tiền liên
   chuỗi thành công" trong test/devnet từ trước tới nay chưa bao giờ thực sự đổi số dư của
   ai. Kế hoạch vá đầy đủ: `note/cross_chain_full_implementation_plan.md` Task 1 — đây là
   việc lớn nhất, phải xong **trước** mọi mục 1-4 dưới đây, vì các mục đó đều giả định tính
   năng di chuyển giá trị đã tồn tại.
1. P5 — security review độc lập cho toàn bộ luồng verify (BLS + Merkle + replay + double-mint
   qua refund + xác thực origin sender 2 chiều **+ toàn bộ code mới ở mục 0**) — **chưa làm**.
2. Đã chạy thật trên **nhiều máy vật lý/VM độc lập** (không phải devnet 1 máy) đủ lâu để quan
   sát hành vi production thật (T4, mục 12 tài liệu kiến trúc) — **chưa làm**, xem mục 6 bên
   dưới về lý do lần chạy gần nhất bị chặn bởi tài nguyên máy chia sẻ.
3. Xử lý (ít nhất mitigat quy trình, lý tưởng là vá code) mục "hardening item" mới nhất trong
   `cross_chain_production_readiness_plan.md` (nguy cơ front-run `bootstrapFoundingChains` khi
   làm lễ genesis) — xem mục 5.3 bên dưới.
4. Thay khoá đóng cứng trong `deploy/systemd/start_relayer_daemon.sh` /
   `register_private_chains_t2.py` (`RELAYER_KEY` / `dev_priv_key`) — đây là khoá devnet công
   khai trong repo public, **không bao giờ được dùng cho relayer/submitter thật** — xem mục
   5.4 và mục 7.

**Việc cần làm tiếp theo, đầy đủ, theo thứ tự:** `note/cross_chain_full_implementation_plan.md`
— viết cho 1 agent/dev mới hoàn toàn để triển khai hết các mục trên.

Nếu mục tiêu hiện tại là **testnet nội bộ / diễn tập / demo cho đối tác** — hệ thống đã đủ
dùng, cứ đi theo quy trình dưới. Nếu mục tiêu là **mainnet với giá trị thật** — dừng ở đây,
việc cần làm tiếp theo là lên kế hoạch P5, không phải chạy thêm script.

---

## 1. Kiến trúc tổng quan

```
┌──────────────┐   outbound()/attestCommit()/claimMessage()   ┌──────────────┐
│ Private Chain │ ───────────────────────────────────────────▶│  Root Anchor  │
│   (vd. 101)   │◀─────────────────────────────────────────── │  (chain 9099) │
└──────────────┘         RelayerDaemon (permissionless)        └──────────────┘
   Go execution +                                                 Cùng loại
   Rust consensus,                                              stack, đóng vai
   1 mạng Mysticeti                                             trò custodian/
   độc lập                                                      Reserve thật
```

- **Mỗi private chain** là 1 mạng Mysticeti độc lập (Go execution layer +
  Rust consensus qua CGo FFI), tự vận hành, tự chủ quyền dữ liệu.
- **Root Anchor** là **một mạng cùng loại** (không phải thành phần đặc biệt) nhưng đóng vai
  trò custodian thật của native coin liên chuỗi (`GlobalSupplyLedger.per_chain_allocation`
  là trần thực thi chủ động, không phải audit thụ động).
- **`GatewayPrecompile`** (địa chỉ `0x1002`) chạy trên cả private chain lẫn Root Anchor,
  expose `outbound`, `attestCommit`, `claimMessage`, `verifyAndExecute`,
  `claimDeadChainBalance`, `refund`, `propose`/`vote`/`executeProposal`,
  `bootstrapFoundingChains`.
- **`CommitteeAttestationWorker`** và **`GatewayRegistryMonitor`** chạy kèm mỗi node (cấu
  hình qua khối `cross_chain` trong `config.json`, xem mục 5.1) — theo dõi trạng thái
  registry, ký attest.
- **`RelayerDaemon`** (`cmd/tool/cross_chain_relayer`) là dịch vụ độc lập, permissionless —
  bất kỳ ai cũng chạy được, relay message giữa các chain và Root Anchor.
- **Governance** trên Root Anchor: `propose`/`vote`/`executeProposal`, timelock 72h, quorum
  ≥2/3, 1-chain-1-vote. Root Anchor genesis rỗng nên có vòng gà-trứng (không ai vote được vì
  chưa ai đăng ký) — giải quyết bằng `bootstrapFoundingChains` (mục 5.2/5.3) hoặc ceremony
  (mục 5.3).

Tài liệu thiết kế đầy đủ: `note/cross_chain_root_anchor_architecture.md`. Tiến độ/lỗi đã sửa:
`note/cross_chain_production_readiness_plan.md`.

---

## 2. Yêu cầu hạ tầng

- **Build**: Go ≥ phiên bản trong `execution/go.mod`, Rust toolchain (`consensus/metanode`),
  cả 2 build qua CGo FFI — dùng `consensus/metanode/scripts/build_check.sh` (kiểm tra cả 2,
  dùng bởi mọi script `setup_*.sh` trong `deploy/systemd/`).
- **Máy chủ**: mỗi node cần đủ CPU/RAM cho cả execution (Go) + consensus (Rust) + EVM
  worker pool song song. Tối thiểu để rehearsal: 1 máy nhiều core. Cho production nhiều
  máy: xem mục 4 (Ansible) — mỗi node 1 máy/VM riêng.
- **Port**: mỗi private chain cần RPC port riêng + P2P/primary/worker port cho consensus
  (xem `open_ports.sh` sinh cùng key mỗi node). Root Anchor dùng bộ port riêng (mặc định
  RPC `9099`).
- **Khoá**: nguyên tắc bắt buộc — **không bao giờ** truyền private key qua kênh không mã
  hoá, không bao giờ commit vào git (xem `.gitignore`/`execution/.gitignore` đã loại trừ
  `single_chain_data/`, `eth_key.json`, `root_anchor_data/`, v.v. — nhưng vẫn phải tự kiểm
  tra trước khi `git add`, gitignore không phải lưới an toàn duy nhất).

---

## 3. Con đường A — Devnet 1 máy (rehearsal, KHÔNG dùng thẳng cho production)

Dùng để tập dượt quy trình, kiểm thử tích hợp, demo nội bộ. Chạy hết trên **một máy**, dùng
khoá devnet đóng cứng trong tooling — **không đại diện cho một triển khai production thật**.

```bash
cd deploy/systemd

# 1. Sinh + chạy 4 private chain (101-104)
bash setup_4_private_chains.sh          # thêm --clean để làm lại từ đầu, --no-build để bỏ qua build

# 2. Sinh + chạy Root Anchor (chain 9099, 4 validator)
bash setup_root_anchor.sh
bash root_anchor_data/start_all.sh

# 3. Đăng ký 4 chain vào Root Anchor
#    (dùng propose()/vote() bình thường CHỈ hoạt động sau khi ChainRegistry không còn rỗng —
#     script dưới dùng khoá dev đã inject sẵn trong genesis Root Anchor devnet, xem CẢNH BÁO)
bash register_private_chains_t2.sh

# 4. Chạy Relayer
bash start_relayer_daemon.sh
```

**⚠️ CẢNH BÁO — khoá devnet đóng cứng trong repo public:**
`start_relayer_daemon.sh` (`RELAYER_KEY=0xd3ae7482...`) và `register_private_chains_t2.py`
(`dev_priv_key`, cùng giá trị) dùng **chung một private key đã commit vào git** — đây là
"known dev account we just injected into the root anchor" theo comment gốc trong script,
tồn tại để devnet chạy được ngay không cần bước sinh/nạp khoá thủ công. Vì repo là
**public**, khoá này coi như đã bị lộ tuyệt đối. **Không bao giờ dùng lại genesis/khoá này
cho bất cứ mạng nào có giá trị thật** — dù chỉ là testnet có người ngoài truy cập được RPC.
Trước khi dùng con đường A cho bất cứ mục đích nào ngoài rehearsal trên máy cá nhân: thay
khoá này bằng khoá tự sinh (`python3 -c "import secrets; print(secrets.token_hex(32))"`)
và nạp allocation tương ứng vào genesis Root Anchor thay vì dùng giá trị đóng cứng.

Dừng: `bash private_chains_data/stop_all.sh` và `bash root_anchor_data/stop_all.sh`.
Log: `deploy/systemd/private_chains_data/chain_XXX/node-0/logs/node-0.log`,
`deploy/systemd/root_anchor_data/node-*/logs/`.

Chi tiết thêm (dashboard giám sát P7, danh sách port): `deploy/systemd/README.md`.

---

## 4. Con đường B.1 — Production thật: 1 private chain qua Ansible

Đây là con đường **đã kiểm chứng qua production thật**, dùng cho mọi private chain đơn lẻ
(kể cả Root Anchor tự thân, vì nó chỉ là "một mạng nữa" — xem mục 1).

```bash
cd deploy/ansible
# Sửa inventory.yml 1 lần: IP, tài khoản SSH cho từng node

# Cài mới hoàn toàn (sinh key/genesis mới, xoá data cũ)
./ansible_deploy.sh --reset-all --open-ports    # --open-ports chỉ cần chạy lần đầu

# Cập nhật code, giữ nguyên key/data (dùng cho các lần deploy sau)
./ansible_deploy.sh --start
```

Quy trình 6 role tuần tự: `local_build` → `stop_services` → `clean_data` (bỏ qua nếu
`--start`) → `node_setup` → `snapshot_restore` (chỉ khi `--restore-node`) →
`systemd_services`. Chi tiết đầy đủ (khôi phục snapshot, thao tác riêng 1 node, giám sát
chéo đa máy `--all-monitors`, quản lý RPC proxy): `deploy/ansible/README.md`.

Cho cụm 1 máy nhiều node (không dùng Ansible đa máy): `deploy/systemd/cluster/systemd-cluster.sh`
— xem `deploy/systemd/docs/systemd-cluster.md`. Nguyên tắc khởi động: **Execution (Go)
trước, Consensus (Rust) sau** (Rust nối vào Execution qua FFI socket); dừng theo thứ tự
ngược lại để không mất dữ liệu.

Giám sát production: `deploy/ansible/monitors/` (Health Monitor, Resource Monitor, Block
Hash Checker — tất cả có cảnh báo Telegram, xem mục Phần 3 của `deploy/ansible/README.md`).

---

## 5. Con đường B.2 — Production thật: Root Anchor nhiều tổ chức độc lập

Dùng khi **≥ 4 tổ chức độc lập**, không ai có (hay cần) quyền SSH/sudo vào máy người khác,
và không private key nào rời khỏi máy nó được sinh ra — đúng mô hình trust của Root Anchor
thật (khác hẳn con đường A, vốn giả định 1 người vận hành tất cả).

### 5.1 Mỗi tổ chức triển khai private chain của mình

Làm theo mục 4 (Ansible) — **độc lập hoàn toàn**, không cần phối hợp với ai khác ở bước này.
Điểm khác biệt duy nhất so với 1 private chain đơn thuần: bật khối `cross_chain` trong
`config.json` (RPC Root Anchor sẽ trỏ tới, submitter key riêng của chain đó) — script sinh
config (`gen_single_chain.py`) đã hỗ trợ cờ `--root-anchor-rpc`/`--root-anchor-submitter-key`.
**Lưu ý đã từng gặp lỗi thật:** khối JSON này dùng key dạng `snake_case`
(`root_anchor_rpc_urls`, không phải `RootAnchorRpcUrls`) — Go's `encoding/json` không báo lỗi
khi tên trường sai, nó chỉ âm thầm bỏ qua field và tắt luôn `GatewayRegistryMonitor`/
`CommitteeAttestationWorker` mà không có dấu hiệu gì trong log. Luôn xác nhận bằng cách xem
log node có dòng `✅ Committee Attestation Worker started` không, đừng tin config đã đúng chỉ
vì node khởi động không lỗi.

### 5.2 Lễ khai sinh Root Anchor (genesis ceremony) — con đường trust-minimized đúng nghĩa

Đây là quy trình **khuyến nghị cho production thật nhiều tổ chức**, khác với
`bootstrapFoundingChains` (mục 5.3) ở chỗ **không một coordinator nào giữ khoá của ai
khác**, và có bước xác minh digest bắt buộc chống lệch genesis âm thầm.

Quy trình đầy đủ, từng bước: **`note/runbook_root_anchor_genesis_ceremony.md`** — đọc file
đó trước khi làm thật, tài liệu này chỉ tóm tắt:

1. Mỗi tổ chức tự sinh key (`metanode keytool generate validator --out-dir ./my_keys`,
   không rời máy mình) rồi tự build `founding_entry.json`
   (`execution/cmd/tool/founding_entry`) — file này **không chứa private key** (có test
   regression đảm bảo, `TestBuildFoundingEntry_NoPrivateKeyLeakage`).
2. **⚠️ Gửi `founding_entry.json` riêng tư cho coordinator, KHÔNG đăng công khai / group
   chat.** Dù file không có private key, nó vẫn là nguyên liệu đủ để ai đó dùng cho một cuộc
   tấn công front-run có thật đã tìm thấy khi viết tài liệu này — xem mục 5.3 ngay dưới đây,
   **đọc trước khi làm ceremony thật**.
3. Coordinator gom ≥ 4 file, chạy `assemble_root_anchor assemble` → sinh `genesis.json` +
   `genesis_digest.txt`.
4. Coordinator công bố `genesis_digest.txt` qua **kênh khác** với kênh đã gửi `genesis.json`
   (out-of-band, chống giả mạo trong lúc truyền).
5. **Mỗi node** tự chạy `assemble_root_anchor verify --expect-digest ...` trước khi start —
   đây là lớp phòng thủ duy nhất chống lệch genesis, vì bản thân consensus **không** kiểm tra
   gì ở epoch 0 (`setup_storage/mod.rs` bỏ qua peer-verification lúc genesis).
6. Start node như một private chain bình thường (mục 4).

Diễn tập trước khi làm thật (bắt buộc, không phải tuỳ chọn):
`bash deploy/systemd/rehearse_root_anchor_ceremony.sh` — chạy toàn bộ quy trình trên với 4
thư mục key độc lập giả lập 4 tổ chức, tới tận việc start 4 node thật và xác nhận đạt BFT
quorum.

### 5.3 ⚠️ Rủi ro cần biết trước khi làm ceremony thật: front-run `bootstrapFoundingChains`

Root Anchor mới sinh có `ChainRegistry` rỗng → không ai vote được (`Vote()` yêu cầu người
vote đã có trong `ChainRegistry`) → vòng gà-trứng. `bootstrapFoundingChains(bytes[]
payloads)` phá vòng lặp này: nạp thẳng ≥4 `ChainRegistry` (yêu cầu PoP thật cho mọi thành
viên committee), tự khoá vĩnh viễn ngay sau lần gọi thành công đầu tiên.

**Phát hiện khi viết tài liệu này (2026-08-25, đã ghi vào
`note/cross_chain_production_readiness_plan.md`, chưa vá code):** lệnh này **không kiểm tra
người gửi giao dịch** — bất kỳ địa chỉ nào cũng gọi được, miễn payload hợp lệ cấu trúc + PoP
thật. Vì `founding_entry.json` được thiết kế "an toàn để công khai" (không có private key),
ai lấy được ≥3 trong 4 file thật của các nhà sáng lập **trước khi** giao dịch bootstrap của
coordinator được xác nhận trên chain, có thể ghép chúng với 1 entry tự tạo (khoá + PoP của
chính họ, không cần chiếm đoạt gì) rồi đua gửi giao dịch `bootstrapFoundingChains` của riêng
mình trước. Vì lệnh tự khoá sau lần gọi đầu tiên thành công, kẻ tấn công sẽ chiếm vĩnh viễn 1
ghế committee/governance thay vì nhà sáng lập thật thứ 4.

**Mức độ nghiêm trọng: 🟡 trung bình, không phải mất giá trị thật** — `ChainRegistry` không
mang trường số dư nào, nên lúc genesis không có gì để rút; `ProposalUnregisterChain` đã tồn
tại nên 3 nhà sáng lập thật (đủ quorum `ceil(2*4/3)=3`) có thể vote loại chain giả ra ngay,
rồi đăng ký lại nhà sáng lập thật thứ 4 qua governance bình thường (lúc này đã hoạt động vì
`ActiveChains` không còn rỗng). Đây là tấn công gây rối/trì hoãn có thể khắc phục, không phải
lỗ hổng rút tiền — nhưng vẫn cần xử lý trước khi coi ceremony là "đáng tin cậy tuyệt đối".

**Việc phải làm khi ceremony thật (giảm thiểu quy trình, đã áp dụng ở mục 5.2 bước 2):**
gửi `founding_entry.json` **trực tiếp, riêng tư** cho coordinator (không kênh công khai);
coordinator gửi giao dịch bootstrap **ngay** sau khi nhận đủ 4 file, và theo dõi mempool xem
có giao dịch nào khác gọi cùng địa chỉ Gateway đang chờ xử lý trước khi giao dịch của mình
xác nhận không.

**Việc cần vá trong code (chưa làm, ghi lại để giao việc)：** giới hạn người gọi
`bootstrapFoundingChains` vào 1 địa chỉ coordinator đã cam kết từ trước (out-of-band, cùng
kiểu với `genesis_digest.txt`), hoặc yêu cầu khai báo trước tập chain ID kỳ vọng để việc thay
thế trở nên bất khả thi về mặt cấu trúc thay vì chỉ có thể khắc phục sau khi xảy ra.

### 5.4 Sau khi Root Anchor sống: bật attestation + relayer

- `CommitteeAttestationWorker`/`GatewayRegistryMonitor` tự chạy kèm node nếu config đúng
  (xem CẢNH BÁO mục 5.1).
- Chạy `RelayerDaemon` (`cmd/tool/cross_chain_relayer`) — **permissionless, ai cũng chạy
  được**, nhưng cần khoá của **chính người vận hành relayer đó**, không dùng lại
  `start_relayer_daemon.sh`'s khoá devnet đóng cứng (xem CẢNH BÁO mục 3). Ví dụ chạy với
  khoá riêng:
  ```bash
  ./cross_chain_relayer \
      -key "<khoá riêng của relayer, KHÔNG commit vào đâu cả>" \
      -root-anchor "http://<root-anchor-rpc>" \
      -chains "101=http://<chain101-rpc>,102=http://<chain102-rpc>,..." \
      -poll-interval-ms 500
  ```

---

## 6. Bài học vận hành thật (đã gặp, không phải lý thuyết)

- **3 bug tooling đã gặp và sửa khi chạy T2 devnet** (PascalCase vs snake_case JSON, tên cờ
  CLI sai `--rpc` vs `--root-anchor`, hex-vs-base64 khi encode `pubkey_bls`) — chung 1 dạng:
  payload JSON sinh bởi script không khớp field Go thật, **không báo lỗi tại điểm sai**. Quy
  tắc rút ra: đừng tin bất kỳ payload JSON mới nào cho tới khi đã unmarshal thử qua đúng
  Go struct đích (`go run` 5 dòng là đủ).
- **Treo devnet khi restart từ dữ liệu cũ** — đã điều tra kỹ (pprof goroutine/heap, I/O
  throughput, ulimit), kết luận **không phải lỗi code** mà do 1 tiến trình khác không liên
  quan chiếm 653% CPU / 110GB+ RAM trên cùng máy chia sẻ. Bài học: chạy T2/production thật
  trên **máy/VM riêng biệt**, đừng chia sẻ máy với tải nặng khác không kiểm soát được — đây
  chính là lý do mục 0 điểm 2 yêu cầu chạy trên nhiều máy độc lập trước khi coi là sẵn sàng.
- Chi tiết đầy đủ cả 2 mục trên: `note/cross_chain_production_readiness_plan.md`.

---

## 7. Checklist an ninh trước khi go-live (tổng hợp)

- [ ] P5 — security review độc lập đã hoàn tất (mục 0).
- [ ] Đã chạy thật trên nhiều máy độc lập, không chỉ devnet 1 máy (mục 0, mục 6).
- [ ] Không còn khoá devnet đóng cứng nào từ `deploy/systemd/` được dùng cho vai trò thật
      (relayer, submitter) — mỗi vai trò có khoá riêng, tự sinh, không commit git (mục 3, 5.4).
- [ ] Ceremony genesis (nếu dùng) đã áp dụng giảm thiểu mục 5.3: không công khai
      `founding_entry.json`, coordinator gửi bootstrap tx ngay lập tức.
- [ ] Mọi `config.json` khối `cross_chain` đã xác nhận field đúng snake_case bằng cách xem
      log thật (mục 5.1), không chỉ tin node khởi động không lỗi.
- [ ] Đã đọc và xử lý toàn bộ mục còn mở trong `note/cross_chain_production_readiness_plan.md`
      (roadmap P0-P8 hiện đang ở P1-P3, còn P4-P8 chưa làm — xem
      `note/cross_chain_root_anchor_architecture.md` mục 8).
- [ ] Firewall/port chỉ mở đúng những gì cần (`open_ports.sh` sinh theo node, xem lại trước
      khi chạy trên mạng công cộng).
- [ ] Giám sát (Health/Resource Monitor, Block Hash Checker, Telegram alert) đã bật trước khi
      go-live, không phải sau sự cố đầu tiên (mục 4).

---

## 8. Tham chiếu nhanh

| Việc cần làm | Tài liệu/script |
| :--- | :--- |
| Kiến trúc thiết kế đầy đủ cross-chain | `note/cross_chain_root_anchor_architecture.md` |
| Tiến độ, bug đã sửa, lộ trình P0-P8 | `note/cross_chain_production_readiness_plan.md` |
| Kế hoạch triển khai đầy đủ (code còn thiếu, cho agent khác thực hiện) | `note/cross_chain_full_implementation_plan.md` |
| Lễ khai sinh Root Anchor nhiều tổ chức | `note/runbook_root_anchor_genesis_ceremony.md` |
| Vận hành 1 private chain đơn (Ansible) | `deploy/ansible/README.md` |
| Cụm nhiều node 1 máy (systemd) | `deploy/systemd/docs/systemd-cluster.md` |
| Devnet nhanh 2/4 chain 1 máy | `deploy/systemd/README.md` |
| Khôi phục từ snapshot | `deploy/systemd/docs/restore_snapshot_systemd.md` |
| Chết node, phục hồi | `note/runbook_chain_death_recovery.md` |
| Số node tối thiểu cho BFT | `note/bft_fault_tolerance_node_count.md` |
