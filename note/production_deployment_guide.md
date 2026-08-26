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

*Cập nhật lần cuối: 2026-08-26 (phiên làm việc thứ hai trong ngày — sau khi toàn bộ pipeline
relay tự động P4 được xây xong và một giao dịch chuyển giá trị liên chuỗi THẬT chạy trọn vẹn
đầu-cuối lần đầu tiên trong lịch sử dự án). Đây là bản trạng thái hiện tại — không phải nhật
ký theo thời gian. Lịch sử đầy đủ (ai tìm ra gì, khi nào, PR nào) nằm ở
`note/cross_chain_production_readiness_plan.md`; đọc ở đó nếu cần biết "vì sao", còn tài liệu
này chỉ trả lời "hiện đang ở đâu".*

| Thành phần | Sẵn sàng cho | Chưa sẵn sàng cho |
| :--- | :--- | :--- |
| 1 private chain đơn (Go execution + Rust consensus, Ansible/systemd) | Production nội bộ, testnet, mainnet doanh nghiệp riêng lẻ | — |
| Root Anchor / cầu nối liên chuỗi (`GatewayPrecompile`, governance, relayer tự động) | Testnet nội bộ, diễn tập ceremony, demo, chạy thật nhiều tiến trình 1 máy — **đã xác nhận sống một giao dịch chuyển native coin thật, đầu-cuối, hoàn toàn tự động** (mục 0 bên dưới) | **Giá trị thật trên mainnet** cho tới khi qua P5 (audit độc lập) + T2 (chạy thật nhiều máy vật lý) |

**Trạng thái code (quan trọng cho bất kỳ ai tiếp quản việc deploy):** toàn bộ các lỗ hổng
CRITICAL đã biết đều đã vá + có test hồi quy thật + xác nhận chạy sống — nhưng code này
**hiện nằm trên PR #73** (`feat/p4-relayer-commit-automation` → `dev`), **chưa merge**.
Trước khi triển khai bất cứ thứ gì thật, xác nhận PR #73 (hoặc PR kế tiếp nếu #73 đã đóng) đã
merge vào `dev` — đừng giả định `dev` đã có các fix dưới đây chỉ vì tài liệu này nói đã vá.
PR #73 gồm cả những PR trước đó nó build lên trên (#61/#63/#64/#68/#70/#71) — nếu chỉ merge
những PR cũ mà bỏ qua #73, các fix mô tả ngay dưới đây (đặc biệt là mục treo chain — nghiêm
trọng nhất) sẽ KHÔNG có trong `dev`.

**Đã vá + xác nhận sống (không còn là rủi ro mở):**
- Lớp xác minh mật mã (BLS/Merkle/anti-fraud) đã nối thật vào số dư thật cho cả native coin
  (`ProcessNativeMintBurn` gọi thật từ `outbound`/`claimMessage`) và tài sản tuỳ biến
  (`msg.sender`/`SetCode` đúng, custom-asset round trip chạy thật đầu-cuối trên 2 node thật).
- `GlobalSupplyLedger.PerChainAllocation` từng không có đường nào cấp phát được (trần luôn =
  0 vĩnh viễn) — đã vá bằng `ProposalAllocateSupply`/`GrantAllocation` qua governance thật.
- `vote()`/`executeProposal()` từng tin timestamp do caller tự khai (đủ để bypass timelock
  72h bắt buộc) — giờ luôn dùng block time thật.
- `bootstrapFoundingChains` từng không kiểm tra người gửi (front-run ceremony) —
  `CrossChainConfig.GenesisCoordinatorAddress` giờ khoá người gọi hợp lệ duy nhất (**bắt buộc
  phải set giá trị này trong config thật trước khi gửi giao dịch bootstrap** — bỏ trống vẫn
  giữ hành vi cũ để không phá vỡ devnet/test).
- Khoá đóng cứng trong `start_relayer_daemon.sh`/`register_chains` — giờ đọc từ biến môi trường
  `RELAYER_KEY`/cờ `-key` (**bắt buộc phải set thật** trước khi chạy cho vai trò thật, không
  set thì rơi về khoá devnet công khai kèm cảnh báo to). `register_private_chains_t2.py`
  (script Python cũ hơn, dùng `DEV_PRIV_KEY`) đã lỗi thời — dùng `register_private_chains_t2.sh`
  thay thế (mục 3).
- Root Anchor "Layer C": 1 cụm multi-validator thật không bao giờ đạt được trạng thái
  `Healthy` do lỗi trong script sinh cấu hình (`gen_root_anchor_chain.py` gán trùng cổng giữa
  server chẩn đoán và cổng gRPC P2P thật) — đã tìm ra nguyên nhân thật (không phải race Tokio
  như 2 lần đoán trước) và vá; xác nhận sống: cụm 4 validator sạch đạt `Healthy` cả 4 node, 0
  lỗi bind, block height tiếp tục tăng — lần đầu tiên trong lịch sử dự án.
- `QuorumThreshold` (ngưỡng % stake cần để 1 quorum cert hợp lệ) từng không có sàn an toàn ở
  bất kỳ đâu — có thể set dưới 2/3 BFT, cho 1 thiểu số giả mạo quorum. Đã vá: sàn 2/3 bắt buộc
  ở cả 4 nơi gán giá trị.
- `ProposalRegisterChain` từng nhận 1 uỷ ban (committee) không rỗng mà không verify PoP (lỗ
  hổng rogue-key) — khác với `BootstrapFoundingChains`/`ProposalUpdateCommittee`, cả 2 đều đã
  verify đúng. Đã vá: bắt buộc verify khi uỷ ban không rỗng.
- **🔴 [NGHIÊM TRỌNG NHẤT, PR #73] Giao dịch barrier (gateway/validator contract) không bao
  giờ tăng nonce người gửi** — phát hiện qua chạy sống P4: `HandleSuccessTransaction`/
  `HandleRevertedTransaction` gọi `ExecuteNonceOnly` để tăng nonce, nhưng hàm này CỐ Ý bỏ qua
  chính địa chỉ người gửi (giả định nonce "đã được tăng từ trước" — đúng với luồng EVM song
  song thường, nhưng SAI với luồng barrier-tx, vốn không hề có bước tăng nonce nào khác). Hậu
  quả: giao dịch gateway ĐẦU TIÊN từ 1 tài khoản luôn thành công, nhưng giao dịch THỨ HAI từ
  CÙNG tài khoản đó vĩnh viễn bị coi là "nonce tương lai" không bao giờ hợp lệ — và vì executor
  chỉ tạo block mới khi tx-pool có ít nhất 1 giao dịch hợp lệ, **toàn bộ việc sản xuất block
  của CHAIN ĐÓ bị treo vĩnh viễn** (không riêng gì giao dịch bị kẹt), xác nhận sống: block
  height đứng yên nhiều phút trong khi consensus Rust bên dưới vẫn commit bình thường. Đây là
  bug ảnh hưởng RẤT RỘNG (bất kỳ tài khoản nào gửi ≥2 giao dịch gateway liên tiếp nhanh, không
  riêng relayer) — nếu deploy từ `dev` mà chưa có PR #73, **tuyệt đối không cho phép bất kỳ tài
  khoản nào gửi 2 giao dịch tới `GATEWAY_CONTRACT_ADDRESS`/`VALIDATOR_CONTRACT_ADDRESS` liên
  tiếp** cho tới khi verify đã merge fix này. Có test hồi quy
  (`TestGatewayHandler_ConsecutiveTransactionsFromSameSenderAdvanceNonce`) xác nhận fail nếu
  thiếu fix, pass nếu có.
- **[PR #73] `register_chains` dùng `config.LoadConfig()` trong vòng lặp nhiều lần** —
  `LoadConfig` cache qua `sync.Once` toàn tiến trình, nên gọi lần 2 trở đi âm thầm trả về config
  của LẦN GỌI ĐẦU TIÊN bất kể đường dẫn truyền vào. Hậu quả: mọi chain đăng ký sau chain đầu
  tiên trong danh sách `--chains` đều bị gán NHẦM committee/BLS pubkey của chain đầu tiên —
  khiến chain đó không bao giờ tự nhận ra mình là thành viên uỷ ban của chính nó. Đã vá: đọc +
  parse trực tiếp từng file config, không qua singleton.
- **[PR #73] `ChainRegistry` là state cục bộ theo từng chain, không dùng chung** — đăng ký
  founding chains chỉ trên Root Anchor KHÔNG đủ để `attestCommit()` hoạt động giữa 2 private
  chain với nhau (mỗi chain cần biết committee của các chain khác qua chính registry CỦA NÓ).
  `register_chains` giờ có cờ `-target-rpcs` để bootstrap luôn từng private chain, không chỉ
  Root Anchor (xem mục 3 bước 3 cập nhật bên dưới).
- **[PR #73, xác nhận sống lần đầu tiên] Một giao dịch chuyển native coin liên chuỗi THẬT đã
  chạy trọn vẹn đầu-cuối, hoàn toàn tự động**: `outbound()` (khoá tiền thật) →
  `batchOutboundCommit()` → chữ ký BLS uỷ ban thật → `attestCommit()` → `claimMessage()` → số
  dư thật tăng đúng giá trị trên chain đích, xác nhận qua `eth_getBalance`, không cần can thiệp
  thủ công (relayer tự poll/batch/attest/claim qua cơ chế P4, xem mục 3 bước 4 cập nhật). Đây
  là bằng chứng sống đầu tiên rằng lớp xác minh mật mã + lớp giá trị thật hoạt động khớp nhau
  hoàn chỉnh, không chỉ đúng riêng lẻ từng phần qua unit test.

**2 điều kiện chặn cứng còn lại trước mainnet giá trị thật, không có đường tắt** (xem
`note/cross_chain_root_anchor_architecture.md` mục 8, lộ trình P0-P8):
1. **P5 — Audit bảo mật độc lập.** Chưa làm, cần bên thứ ba, không tự làm được. Phạm vi +
   tài liệu chuẩn bị: `note/external_security_audit_scope_p5.md`.
2. **T2 — Chạy thật trên nhiều máy vật lý/VM độc lập** đủ lâu để quan sát hành vi production
   thật (độ trễ mạng thật, tải thật). Mọi bằng chứng "chạy thật" tới nay đều trên **1 máy chia
   sẻ** (nhiều tiến trình riêng biệt, không phải hạ tầng nhiều máy) — Layer C đã hết là lý do
   chặn kỹ thuật cho việc này, giờ chỉ còn thiếu hạ tầng nhiều máy thật.

**Việc còn mở, không chặn nhưng chưa xong** (chi tiết đầy đủ, cho agent/dev tiếp theo:
`note/all_remaining_fixes_plan.md`):
- 2 quyết định thiết kế từng cần người phụ trách kiến trúc xác nhận (cơ chế epoch catch-up
  khi 1 chain kẹt nhiều epoch; `propose()` permissionless có phải chủ ý) **đã được quyết định
  + đóng 2026-08-26** (`ProposalUpdateCommittee` là câu trả lời chính thức cho epoch catch-up;
  permissionless-propose xác nhận là chủ ý, thêm metric quan sát tăng trưởng thay vì đoán
  rate-limit) — xem `all_remaining_fixes_plan.md` Mục 1/2 để có đầy đủ lý do + test hồi quy.
- Chưa có công cụ production thật để coordinator gửi `bootstrapFoundingChains()` với dữ liệu
  registry thật của ≥4 chain sáng lập (chỉ có công cụ devnet/test).
- Vài khoảng trống test nhỏ (nonce/double-submit khi `RelayerDaemon` restart giữa chừng; test
  tải thật cho Gate 1 trên cụm sống thay vì chỉ unit test).

Nếu mục tiêu hiện tại là **testnet nội bộ / diễn tập / demo cho đối tác** — hệ thống đã đủ
dùng (sau khi PR #73 merge), cứ đi theo quy trình dưới. Nếu mục tiêu là **mainnet với giá trị
thật** — việc cần làm tiếp theo là lên kế hoạch cho P5 + T2, không phải chạy thêm script.

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

# 1. Sinh + chạy Root Anchor (chain 9099, 4 validator) TRƯỚC — register_chains ở bước 3
#    cần Root Anchor đã sống để gửi bootstrapFoundingChains() tới.
bash setup_root_anchor.sh               # thêm --clean để làm lại từ đầu, --no-build để bỏ qua build
bash root_anchor_data/start_all.sh

# 2. Sinh + chạy 4 private chain (101-104)
bash setup_4_private_chains.sh

# 3. Đăng ký 4 chain — cả trên Root Anchor LẪN trên chính từng private chain
#    (ChainRegistry là state CỤC BỘ theo từng chain, không dùng chung — mỗi chain cần biết
#    committee của các chain khác qua chính registry của nó, không chỉ Root Anchor biết).
#    Dùng bootstrapFoundingChains() thật (KHÔNG phải propose()/vote() — ChainRegistry rỗng thì
#    không ai vote được, xem mục 5.3), tự build binary register_chains nếu chưa có.
bash register_private_chains_t2.sh

# 4. Chạy Relayer — TỰ ĐỘNG hoàn toàn (P4): daemon tự poll từng cặp (source, dest) chain, tự
#    gọi batchOutboundCommit() khi có outbound() đang chờ, tự chờ QuorumCert BLS thật, tự gọi
#    attestCommit()/claimMessage() — không cần gọi tay bất kỳ bước nào ở giữa nữa.
bash start_relayer_daemon.sh
```

**Muốn thử một giao dịch chuyển giá trị THẬT (không chỉ khởi động hạ tầng)?** Một chain mới
đăng ký có **allocation gửi-ra = 0** theo thiết kế (fail-closed — chain không thể gửi ra giá
trị nó chưa từng nhận được) — `outbound()` đầu tiên sẽ khoá tiền thành công, nhưng
`attestCommit()` ở chain đích sẽ revert với `"aggregate amount exceeds source chain allocation
ceiling... available 0"` cho tới khi chain đích tự cấp phát allocation cho chain nguồn qua
governance thật (`propose(kind=ProposalAllocateSupply, ...)` → `vote()` từ ≥2/3 chain đã đăng
ký → chờ đủ 72h timelock → `executeProposal()`). Đây không phải bug — là cơ chế bảo toàn giá
trị bắt buộc của Root Anchor, xác nhận qua `TestGateway_ProposalAllocateSupply_UnblocksAttestCommit`.
Để test được flow này trên devnet mà không phải chờ 72 giờ thật: set
`cross_chain.devnet_governance_timelock_seconds_override` (giây, ví dụ `10`) trong
`config.json` của TỪNG node liên quan **trước khi khởi động** — trường này **chỉ tồn tại để
test** (`config.go` có doc comment riêng cảnh báo), **không bao giờ được set trên bất kỳ mạng
nào có giá trị thật**. `gen_single_chain.py`/`gen_root_anchor_chain.py` giờ tự set giá trị này
(10 giây) cho mọi node devnet sinh ra — không cần chỉnh tay khi đi theo đúng con đường A.

**⚠️ CẢNH BÁO — khoá devnet đóng cứng trong repo public:**
`start_relayer_daemon.sh` (`RELAYER_KEY=0xd3ae7482...`, cũng là khoá mặc định của
`register_chains`/`-key`) dùng **một private key đã commit vào git** — đây là "known dev
account we just injected into the root anchor" theo comment gốc trong script, tồn tại để
devnet chạy được ngay không cần bước sinh/nạp khoá thủ công (`gen_single_chain.py`/
`gen_root_anchor_chain.py` tự đăng ký địa chỉ này với BLS pubkey devnet dùng chung trên MỌI
chain sinh ra, kể cả từng private chain — không chỉ Root Anchor). Vì repo là **public**, khoá
này coi như đã bị lộ tuyệt đối. **Không bao giờ dùng lại genesis/khoá này cho bất cứ mạng nào
có giá trị thật** — dù chỉ là testnet có người ngoài truy cập được RPC. Trước khi dùng con
đường A cho bất cứ mục đích nào ngoài rehearsal trên máy cá nhân: thay khoá này bằng khoá tự
sinh (`python3 -c "import secrets; print(secrets.token_hex(32))"`) truyền qua `-key`/
`RELAYER_KEY`, và đăng ký địa chỉ tương ứng (kèm allocation nếu cần) vào genesis thay vì dùng
giá trị đóng cứng. (`register_private_chains_t2.py`, một script Python cũ hơn cùng thư mục,
đã lỗi thời từ trước khi `register_chains`/`register_private_chains_t2.sh` được sửa đúng —
đừng dùng nó, dùng bản `.sh`.)

Dừng: `bash private_chains_data/stop_all.sh` và `bash root_anchor_data/stop_all.sh`.
Log THẬT theo từng giao dịch/block (không phải `node-0.log`, file đó chỉ có log khởi động):
`deploy/systemd/private_chains_data/chain_XXX/node-0/logs/execution/<YYYY-MM-DD>/execution.log`
(tương tự cho `root_anchor_data/node_*/logs/`).

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

**Phát hiện khi viết tài liệu này (2026-08-25 sáng, ✅ đã vá code cùng ngày tối, xem cuối mục
này):** lệnh này (mặc định, nếu không cấu hình mục dưới) **không kiểm tra người gửi giao
dịch** — bất kỳ địa chỉ nào cũng gọi được, miễn payload hợp lệ cấu trúc + PoP thật. Vì `founding_entry.json` được thiết kế "an toàn để công khai" (không có private key),
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

**✅ Đã vá (2026-08-25 tối):** `CrossChainConfig.GenesisCoordinatorAddress` (khối `cross_chain`
trong `config.json`, key `genesis_coordinator_address`) — set địa chỉ coordinator đã cam kết
từ trước (out-of-band, cùng kiểu với `genesis_digest.txt`) và `bootstrapFoundingChains` sẽ từ
chối mọi người gọi khác. **Bắt buộc phải set giá trị này trước khi gửi giao dịch bootstrap
thật** — bỏ trống vẫn giữ hành vi cũ (ai gọi cũng được, chỉ an toàn cho devnet/rehearsal). Chi
tiết: `note/cross_chain_production_readiness_plan.md`, commit "bootstrapFoundingChains
front-run gap".

### 5.4 Sau khi Root Anchor sống: bật attestation + relayer

- `CommitteeAttestationWorker`/`GatewayRegistryMonitor` tự chạy kèm node nếu config đúng
  (xem CẢNH BÁO mục 5.1).
- Chạy `RelayerDaemon` (`cmd/tool/cross_chain_relayer`) — **permissionless, ai cũng chạy
  được**, nhưng cần khoá của **chính người vận hành relayer đó**. **[PR #73]** Binary này giờ
  tự chạy 1 goroutine `WatchChainPair` cho MỌI cặp (chain nguồn, chain đích) trong danh sách
  `-chains` — mỗi cặp tự poll `getPendingOutboundCount()`, tự gọi `batchOutboundCommit()` khi
  có message chờ, tự chờ `CommitAttestationWorker` sản xuất QuorumCert BLS thật, rồi tự gọi
  `attestCommit()`/`claimMessage()`. Trước PR #73, binary này chỉ khởi tạo daemon rồi chờ tín
  hiệu tắt — không hề tự relay gì cả (dead code, `OnCommitFinalized` không nơi nào gọi tới).
  Không cần gọi tay bất kỳ bước trung gian nào nữa; chỉ cần chạy đúng lệnh dưới đây và để nó
  chạy nền liên tục. `start_relayer_daemon.sh`
  giờ đọc khoá từ biến môi trường `RELAYER_KEY` — **bắt buộc phải set** trước khi chạy cho vai
  trò thật; không set thì script vẫn chạy được nhưng rơi về khoá devnet công khai (kèm cảnh
  báo to, xem CẢNH BÁO mục 3), không bao giờ được dùng cho relayer thật. Ví dụ chạy với
  khoá riêng:
  ```bash
  ./cross_chain_relayer \
      -key "<khoá riêng của relayer, KHÔNG commit vào đâu cả>" \
      -root-anchor "http://<root-anchor-rpc>" \
      -chains "101=http://<chain101-rpc>,102=http://<chain102-rpc>,..." \
      -poll-interval-ms 500
  ```

### 5.5 Quản lý Khoá riêng RelayerDaemon & Submitter cho Production (KMS/HSM vs Plain Config)

- **Hiện trạng:** `RelayerDaemon` (`DaemonConfig.RelayerKeyHex`) và `CommitteeAttestationWorker` (`CrossChainConfig.RootAnchorSubmitterPrivateKeyHex`) đọc private key secp256k1 dạng plain hex string qua file config/biến môi trường (`RELAYER_KEY`, `ROOT_ANCHOR_SUBMITTER_KEY`).
- **Mức độ rủi ro & Mô hình vận hành:**
  - **RelayerDaemon là permissionless:** Khoá của relayer chỉ dùng để trả phí gas khi submit giao dịch `verifyAndExecute` lên destination chain và nhận tip qua `withdrawRelayerTip`. Nếu khoá relayer bị lộ, kẻ tấn công **chỉ rút được số dư tip và gas của tài khoản đó**, KHÔNG thể giả mạo chữ ký BLS của uỷ ban hay rút trộm tài sản của người dùng (vì chữ ký uỷ ban và bằng chứng Merkle được xác thực độc lập bởi `GatewayPrecompile`).
  - **Submitter của CommitteeAttestationWorker:** Khoá này dùng để gửi `submitCommitteeAttestation` lên Root Anchor. Tương tự, nếu bị lộ thì chỉ mất gas của tài khoản submitter — chữ ký BLS share đính kèm bắt buộc phải khớp với `PublicKeyBls` của validator đã đăng ký trên `ChainRegistry`.
- **Khuyến nghị cho Mainnet:**
  - **Giai đoạn Testnet / Early Mainnet (giá trị custody nhỏ):** Chấp nhận lưu khoá qua secret manager (HashiCorp Vault, AWS Secrets Manager, Kubernetes Secrets) và inject qua biến môi trường dạng ephemeral runtime.
  - **Giai đoạn Mainnet quy mô lớn (giá trị custody cao):** Khuyến nghị nâng cấp module submitter/relayer sang giao diện Signer abstraction hỗ trợ HSM/KMS (AWS KMS, GCP Cloud KMS, YubiHSM) qua PKCS#11 hoặc RPC signer tách rời (như HashiCorp Vault Transit Engine), không bao giờ giữ private key nguyên bản trong bộ nhớ tiến trình.

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

- [ ] PR #73 (không chỉ các PR cũ hơn nó build lên trên) đã merge vào `dev` — đặc biệt fix bug
      treo chain (barrier-tx không tăng nonce, mục 0). Kiểm tra bằng cách tìm hàm
      `TestGatewayHandler_ConsecutiveTransactionsFromSameSenderAdvanceNonce` có tồn tại trong
      `pkg/blockchain/tx_processor/gateway_handler_test.go` chưa — nếu không có, PR chưa vào.
- [ ] P5 — security review độc lập đã hoàn tất (mục 0).
- [ ] Đã chạy thật trên nhiều máy độc lập, không chỉ devnet 1 máy (mục 0, mục 6).
- [ ] Đã set `-key`/`RELAYER_KEY` (`start_relayer_daemon.sh`, `register_chains`) thành khoá
      thật của vai trò đó — không còn nơi nào rơi về khoá devnet công khai đóng cứng trong repo
      (mục 3, 5.4). (`register_private_chains_t2.py` đã lỗi thời — không dùng, không set biến
      môi trường cho nó.)
- [ ] `cross_chain.devnet_governance_timelock_seconds_override` **KHÔNG được set** (hoặc bằng
      0) trong mọi `config.json` thật — trường này chỉ tồn tại để rút ngắn 72h timelock cho
      test devnet (mục 3); còn set trên mạng thật nghĩa là governance có thể bị thi hành gần
      như ngay lập tức, phá vỡ toàn bộ mục đích của timelock.
- [ ] Đã set `CrossChainConfig.GenesisCoordinatorAddress` (config thật, không phải devnet)
      thành địa chỉ coordinator đã cam kết out-of-band, TRƯỚC KHI gửi giao dịch
      `bootstrapFoundingChains` (mục 5.3).
- [ ] Ceremony genesis (nếu dùng) đã áp dụng giảm thiểu mục 5.3: không công khai
      `founding_entry.json`, coordinator gửi bootstrap tx ngay lập tức.
- [ ] Mọi `config.json` khối `cross_chain` đã xác nhận field đúng snake_case bằng cách xem
      log thật (mục 5.1), không chỉ tin node khởi động không lỗi.
- [ ] Đã đọc và xử lý toàn bộ mục còn mở trong `note/cross_chain_production_readiness_plan.md`
      (roadmap P0-P8: P1-P4 đã xong tính tới PR #73 — bao gồm relay tự động P4 và bug treo
      chain nghiêm trọng nhất từng tìm được — P5-P8 chưa làm; xem
      `note/cross_chain_root_anchor_architecture.md` mục 8).
- [ ] Firewall/port chỉ mở đúng những gì cần (`open_ports.sh` sinh theo node, xem lại trước
      khi chạy trên mạng công cộng).
- [ ] Giám sát (Health/Resource Monitor, Block Hash Checker, Telegram alert) đã bật trước khi
      go-live, không phải sau sự cố đầu tiên (mục 4).

---

## 8. Tham chiếu nhanh

| Việc cần làm | Tài liệu/script |
| :--- | :--- |
| **Sổ tay thao tác: lệnh cụ thể + cách xác thực từng bước** | **`note/deployment_runbook_step_by_step.md`** |
| Kiến trúc thiết kế đầy đủ cross-chain | `note/cross_chain_root_anchor_architecture.md` |
| Tiến độ, bug đã sửa, lộ trình P0-P8 | `note/cross_chain_production_readiness_plan.md` |
| Việc còn lại (code + quyết định thiết kế), cho agent khác thực hiện | `note/all_remaining_fixes_plan.md` |
| Phạm vi + chuẩn bị cho audit bảo mật độc lập (P5) | `note/external_security_audit_scope_p5.md` |
| Lễ khai sinh Root Anchor nhiều tổ chức | `note/runbook_root_anchor_genesis_ceremony.md` |
| Vận hành 1 private chain đơn (Ansible) | `deploy/ansible/README.md` |
| Cụm nhiều node 1 máy (systemd) | `deploy/systemd/docs/systemd-cluster.md` |
| Devnet nhanh 2/4 chain 1 máy | `deploy/systemd/README.md` |
| Khôi phục từ snapshot | `deploy/systemd/docs/restore_snapshot_systemd.md` |
| Chết node, phục hồi | `note/runbook_chain_death_recovery.md` |
| Số node tối thiểu cho BFT | `note/bft_fault_tolerance_node_count.md` |

---

## 9. Bàn giao triển khai (đọc nếu bạn mới tiếp quản việc này)

**Trạng thái code tại thời điểm bàn giao (2026-08-26, phiên thứ hai):**
- Toàn bộ fix mô tả ở mục 0 nằm trên PR #73 (`feat/p4-relayer-commit-automation` → `dev`),
  **chưa merge**. PR này build lên trên chuỗi PR #61/#63/#64/#68/#70/#71 (đã mô tả trong các
  bản trước của tài liệu này) VÀ thêm: toàn bộ cơ chế relay tự động P4 (trước đó
  `cross_chain_relayer` là dead code, không tự relay gì), fix bug treo chain nghiêm trọng nhất
  từng tìm được (barrier-tx không tăng nonce — mục 0), fix `register_chains`'s
  `config.LoadConfig` singleton bug, và wiring `devnet_governance_timelock_seconds_override`.
- **Việc đầu tiên phải làm:** kiểm tra trạng thái PR #73 trên GitHub, đảm bảo CI xanh, merge
  vào `dev` trước khi tin bất kỳ dòng nào ở mục 0 phía trên là đã áp dụng cho môi trường thật.
- Không có gì đang chạy dở/hỏng — `go build/vet/test` (43/43 package) đều sạch tại thời điểm
  bàn giao, không có regression nào chưa xử lý. Có test hồi quy thật cho bug treo chain
  (`TestGatewayHandler_ConsecutiveTransactionsFromSameSenderAdvanceNonce`, xác nhận fail nếu
  thiếu fix).
- **Đã xác nhận sống (không chỉ unit test):** một chu trình chuyển native coin liên chuỗi đầy
  đủ — `outbound()` → `batchOutboundCommit()` → QuorumCert BLS thật → `attestCommit()` →
  `claimMessage()` → số dư thật tăng đúng giá trị ở chain đích — chạy trọn vẹn, hoàn toàn tự
  động qua relayer P4, trên 1 devnet 9-tiến-trình (4 validator Root Anchor + 4 private chain +
  1 relayer) trên **1 máy** (chưa phải T2 nhiều máy thật).

**Việc tiếp theo, theo đúng thứ tự ưu tiên:**
1. Merge PR (xem trên).
2. Xác định mục tiêu triển khai: testnet/demo (đã sẵn sàng, đi thẳng vào mục 3-5) hay mainnet
   giá trị thật (dừng lại lên kế hoạch cho 2 điều kiện chặn ở mục 0 — P5 và T2 — trước khi
   chạy thêm bất kỳ script nào).
3. Nếu mainnet: liên hệ đơn vị audit độc lập cho P5 (dùng
   `note/external_security_audit_scope_p5.md` làm tài liệu bàn giao cho họ), song song chuẩn
   bị hạ tầng nhiều máy vật lý/VM độc lập cho T2 (không phải devnet 1 máy chia sẻ).
4. Việc code còn lại (không chặn, nhưng nên làm trước khi coi Phase 1 là xong): giao
   `note/all_remaining_fixes_plan.md` cho 1 agent/dev khác — tài liệu đó đã liệt kê đầy đủ
   từng việc kèm file/hàm chính xác. 2 quyết định thiết kế từng cần hỏi trước (epoch catch-up,
   `propose()` gating) đã được quyết định + đóng 2026-08-26 (xem Mục 1/2 trong tài liệu đó để
   có lý do đầy đủ) — không còn câu hỏi nào cần người phụ trách kiến trúc trả lời trước khi
   giao việc còn lại cho agent khác.

**Nếu có nghi ngờ về bất cứ điều gì ở tài liệu này:** đọc trực tiếp
`note/cross_chain_production_readiness_plan.md` (log đầy đủ, trung thực, kể cả các lần kết
luận sai rồi tự sửa) thay vì tin lời tóm tắt — đây là quy ước xuyên suốt dự án: mọi khẳng định
"đã vá"/"đã xong" đều phải có bằng chứng thật (test hồi quy, log chạy sống) đi kèm, không phải
chỉ lời khẳng định.
