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

*Cập nhật lần cuối: 2026-08-27. Đây là bản trạng thái hiện tại của `dev` — không phải nhật ký
theo thời gian. Lịch sử phát triển đầy đủ (ai tìm ra gì, khi nào) nằm ở
`note/cross_chain_production_readiness_plan.md`; đọc ở đó nếu cần biết "vì sao", còn tài liệu
này chỉ trả lời "hiện đang ở đâu, cần làm gì".*

**Trước khi tin bất kỳ dòng "đã vá" nào dưới đây: tự xác nhận trên đúng bản code bạn sắp
deploy**, đừng chỉ tin tài liệu — mỗi mục ở checklist (mục 7) có kèm 1 lệnh kiểm tra cụ thể.

| Thành phần | Sẵn sàng cho | Chưa sẵn sàng cho |
| :--- | :--- | :--- |
| 1 private chain đơn (Go execution + Rust consensus, Ansible/systemd) | Production nội bộ, testnet, mainnet doanh nghiệp riêng lẻ | — |
| Root Anchor / cầu nối liên chuỗi (`GatewayPrecompile`, governance, relayer tự động) | Testnet nội bộ, diễn tập ceremony, demo, chạy thật nhiều tiến trình 1 máy — **đã xác nhận sống một giao dịch chuyển native coin thật, đầu-cuối, hoàn toàn tự động** (mục 0 bên dưới) | **Giá trị thật trên mainnet** cho tới khi qua P5 (audit độc lập) + T2 (chạy thật nhiều máy vật lý) |

**Trạng thái code (quan trọng cho bất kỳ ai tiếp quản việc deploy):** toàn bộ các lỗ hổng
CRITICAL đã biết đều đã vá + có test hồi quy thật + xác nhận chạy sống. Đừng tin danh sách
"đã vá" dưới đây tại lời — mỗi mục đều verify được bằng 1 lệnh cụ thể, xem checklist mục 7.

**Đã vá + xác nhận sống (không còn là rủi ro mở):**
- Lớp xác minh mật mã (BLS/Merkle/anti-fraud) đã nối thật vào số dư thật cho cả native coin
  (`ProcessNativeMintBurn` gọi thật từ `outbound`/`claimMessage`) và tài sản tuỳ biến
  (`msg.sender`/`SetCode` đúng, custom-asset round trip chạy thật đầu-cuối trên 2 node thật).
- `GlobalSupplyLedger.PerChainAllocation` từng không có đường nào cấp phát được (trần luôn =
  0 vĩnh viễn) — đã vá bằng `ProposalAllocateSupply`/`GrantAllocation` qua governance thật
  (2026-08-27; governance đó đã bị xoá hẳn 2026-09-04, xem mục 1 — đường hiện tại là
  `allocateSupplyWithCert`, tự-ký bởi Reserve, không qua vote).
- `vote()`/`executeProposal()` từng tin timestamp do caller tự khai (đủ để bypass timelock
  72h bắt buộc) — giờ luôn dùng block time thật (2 hàm này đã bị xoá hẳn cùng
  `GovernanceEngine` 2026-09-04, không còn timelock nào để bypass nữa).
- `bootstrapFoundingChains` từng không kiểm tra người gửi (front-run ceremony) —
  `CrossChainConfig.GenesisCoordinatorAddress` giờ khoá người gọi hợp lệ duy nhất. **Cả hàm
  `bootstrapFoundingChains` lẫn `GenesisCoordinatorAddress` đã bị xoá hẳn 2026-08-28** — xem
  mục 5.3 đã cập nhật cho cơ chế thay thế (`registerChainViaStake`).
- Khoá đóng cứng trong `start_relayer_daemon.sh`/`register_chains` — giờ đọc từ biến môi trường
  `RELAYER_KEY`/cờ `-key` (**bắt buộc phải set thật** trước khi chạy cho vai trò thật, không
  set thì rơi về khoá devnet công khai kèm cảnh báo to). `register_private_chains_t2.py`
  (script Python cũ hơn, dùng `DEV_PRIV_KEY`) đã **bị xoá khỏi repo (2026-08-27)** — dùng
  `register_private_chains_t2.sh` (mục 3).
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
- **🔴 [NGHIÊM TRỌNG NHẤT] Giao dịch barrier (gateway/validator contract) không bao
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
  riêng relayer) — **tuyệt đối không cho phép bất kỳ tài khoản nào gửi 2 giao dịch tới
  `GATEWAY_CONTRACT_ADDRESS`/`VALIDATOR_CONTRACT_ADDRESS` liên tiếp** cho tới khi xác nhận code
  đang chạy đã có fix này (mục 7). Có test hồi quy
  (`TestGatewayHandler_ConsecutiveTransactionsFromSameSenderAdvanceNonce`) xác nhận fail nếu
  thiếu fix, pass nếu có.
- **`register_chains` dùng `config.LoadConfig()` trong vòng lặp nhiều lần** —
  `LoadConfig` cache qua `sync.Once` toàn tiến trình, nên gọi lần 2 trở đi âm thầm trả về config
  của LẦN GỌI ĐẦU TIÊN bất kể đường dẫn truyền vào. Hậu quả: mọi chain đăng ký sau chain đầu
  tiên trong danh sách `--chains` đều bị gán NHẦM committee/BLS pubkey của chain đầu tiên —
  khiến chain đó không bao giờ tự nhận ra mình là thành viên uỷ ban của chính nó. Đã vá: đọc +
  parse trực tiếp từng file config, không qua singleton.
- **`ChainRegistry` là state cục bộ theo từng chain, không dùng chung** — đăng ký
  founding chains chỉ trên Root Anchor KHÔNG đủ để `attestCommit()` hoạt động giữa 2 private
  chain với nhau (mỗi chain cần biết committee của các chain khác qua chính registry CỦA NÓ).
  `register_chains` giờ có cờ `-target-rpcs` để bootstrap luôn từng private chain, không chỉ
  Root Anchor (xem mục 3 bước 3 cập nhật bên dưới).
- **Đã xác nhận sống: một giao dịch chuyển native coin liên chuỗi THẬT chạy trọn vẹn đầu-cuối,
  hoàn toàn tự động**: `outbound()` (khoá tiền thật) →
  `batchOutboundCommit()` → chữ ký BLS uỷ ban thật → `attestCommit()` → `claimMessage()` → số
  dư thật tăng đúng giá trị trên chain đích, xác nhận qua `eth_getBalance`, không cần can thiệp
  thủ công (relayer tự poll/batch/attest/claim qua cơ chế P4, xem mục 3 bước 4 cập nhật). Đây
  là bằng chứng sống đầu tiên rằng lớp xác minh mật mã + lớp giá trị thật hoạt động khớp nhau
  hoàn chỉnh, không chỉ đúng riêng lẻ từng phần qua unit test.
- **[2026-08-27, rà cấu trúc/cấu hình] 3 lỗ hổng cấu hình bảo mật đã vá** — chi tiết đầy đủ,
  bảng tham chiếu mọi biến bảo mật: `note/security_variables_reference.md`.
  1. Chỉ 3/9 trường bí mật trong `config.json` (`private_key`, `reward_sender_private_key`,
     `securepassword`) có đường thoát biến môi trường — 6 trường còn lại (`Databases.BLSPrivateKey`,
     `cross_chain.root_anchor_submitter_private_key_hex`, `gateway_bls_key`, `master_password`,
     `app_pepper`) bắt buộc phải nằm thẳng trong file JSON. Đã vá: thêm 5 biến
     `META_BLS_PRIVATE_KEY`/`META_ROOT_ANCHOR_SUBMITTER_PRIVATE_KEY_HEX`/`META_GATEWAY_BLS_KEY`/
     `META_MASTER_PASSWORD`/`META_APP_PEPPER`, cùng pattern với các biến `META_*` đã có.
  2. Khoá thật trên server qua `deploy/ansible` từng world-readable (`mode: '0755'`/`'0644'`) dù
     service chỉ chạy bằng 1 user (`metanode`) — đã siết về `0600`/`0700`.
  3. `gateway_bls_key` từng hardcode CÙNG 1 giá trị ở cả 3 script sinh config, kể cả tool
     ceremony thật (`gen_validator_entry.py`) — chỉ có rủi ro khi bật `enable_private_gateway`.
     Đã vá: thêm cờ `--gateway-bls-key`/`--random-gateway-bls-key` ở cả 3 script (mặc định
     không đổi để không phá devnet hiện có — phải chủ động truyền cờ cho triển khai thật).

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
- ~~Chưa có công cụ production thật để coordinator gửi `bootstrapFoundingChains()`~~ — MOOT
  (2026-08-28): `bootstrapFoundingChains` đã bị xoá hẳn, không còn coordinator/công cụ nào cần
  xây cho nó nữa. Đăng ký chain sáng lập giờ chỉ cần `registerChainViaStake` (mục 5.3).
- Vài khoảng trống test nhỏ (nonce/double-submit khi `RelayerDaemon` restart giữa chừng; test
  tải thật cho Gate 1 trên cụm sống thay vì chỉ unit test).

Nếu mục tiêu hiện tại là **testnet nội bộ / diễn tập / demo cho đối tác** — hệ thống đã đủ
dùng (sau khi xác nhận checklist mục 7 bằng grep trên `dev` thật), cứ đi theo quy trình dưới.
Nếu mục tiêu là **mainnet với giá trị thật** — việc cần làm tiếp theo là lên kế hoạch cho
P5 + T2, không phải chạy thêm script.

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
  `claimDeadChainBalance`, `refund`, `registerChainViaStake`,
  `allocateSupplyWithCert`/`transferAllocationWithCert`/`registerAssetWithCert` (tự-ký),
  `declareChainDeadWithCert`/`unregisterChainWithCert`/`updateCommitteeWithRecoveryCert`
  (RecoveryCommittee-ký). (`propose`/`vote`/`executeProposal` và `bootstrapFoundingChains` đã
  bị xoá — lần lượt 2026-09-04 và 2026-08-28, xem `note/eurozone_unified_native_coin_plan.md`.)
- **`CommitteeAttestationWorker`** và **`GatewayRegistryMonitor`** chạy kèm mỗi node (cấu
  hình qua khối `cross_chain` trong `config.json`, xem mục 5.1) — theo dõi trạng thái
  registry, ký attest.
- **`RelayerDaemon`** (`cmd/tool/cross_chain_relayer`) là dịch vụ độc lập, permissionless —
  bất kỳ ai cũng chạy được, relay message giữa các chain và Root Anchor.
- **Không còn governance/vote trên Root Anchor** (xoá 2026-09-04, quyết định trực tiếp của
  người dùng — xem `note/eurozone_unified_native_coin_plan.md`). Đăng ký chain mới:
  `registerChainViaStake` — không cần vote, không cần "ai đó đã đăng ký trước" (không còn
  vòng gà-trứng nữa). Hành động ảnh hưởng chain KHÁC (đổi uỷ ban/tuyên bố chết/huỷ đăng ký):
  ký bởi `RecoveryCommittee` — 1 uỷ ban cố định, cấu hình qua
  `cross_chain.recovery_committee_json`, KHÔNG lớn lên theo đăng ký chain mới. Production thật:
  sinh khoá RecoveryCommittee hoàn toàn out-of-band (air-gapped), set qua
  `recovery_committee_json_override_file` trong `inventory.yml` — KHÔNG dùng bộ tự sinh devnet
  (`deploy/systemd/gen_recovery_committee_keys.py`) cho mạng có giá trị thật (mục 7 checklist).

Tài liệu thiết kế đầy đủ: `note/cross_chain_root_anchor_architecture.md`. Tiến độ/lỗi đã sửa:
`note/cross_chain_production_readiness_plan.md`.

---

## 2. Yêu cầu hạ tầng

- **Build & Công cụ**: 
  - Go ≥ phiên bản trong `execution/go.mod`, Rust toolchain (`consensus/metanode`), cả 2 build qua CGo FFI — dùng `consensus/metanode/scripts/build_check.sh` (kiểm tra cả 2).
  - Python 3 và các thư viện hỗ trợ để chạy script sinh genesis/keys:
    ```bash
    pip3 install web3 eth-account eth-keys --break-system-packages
    ```
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
#    cần Root Anchor đã sống để gửi registerChainViaStake() tới.
bash setup_root_anchor.sh               # thêm --clean để làm lại từ đầu, --no-build để bỏ qua build
bash root_anchor_data/start_all.sh

# 2. Sinh + chạy 4 private chain (101-104)
bash setup_4_private_chains.sh

# 3. Đăng ký 4 chain — cả trên Root Anchor LẪN trên chính từng private chain
#    (ChainRegistry là state CỤC BỘ theo từng chain, không dùng chung — mỗi chain cần biết
#    committee của các chain khác qua chính registry của nó, không chỉ Root Anchor biết).
#    Dùng registerChainViaStake() (không cần vote, không cần coordinator -- xem mục 5.3),
#    tự build binary register_chains nếu chưa có.
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
ceiling... available 0"` cho tới khi chain đích được cấp allocation. Đây không phải bug — là
cơ chế bảo toàn giá trị bắt buộc của Root Anchor.

**Cập nhật 2026-09-04 (đơn giản hơn nhiều so với bản gốc):** không còn `GovernanceEngine`
(propose/vote/timelock/execute) nữa — cấp allocation giờ là `register_chains -fund-genesis`
gọi thẳng `allocateSupplyWithCert` (mint genesis supply đúng 1 lần cho Reserve, tự-ký bởi
chính uỷ ban BLS thật của Reserve) rồi `transferAllocationWithCert` (chuyển 1 phần cho mỗi
chain sáng lập, cũng tự-ký, không qua bên thứ 3). Không cần vote, không cần chờ timelock —
chạy xong ngay trong vài giây thật trên cả devnet lẫn production, không cần cấu hình rút ngắn
gì cả. Chi tiết đầy đủ: `note/eurozone_unified_native_coin_plan.md`.

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

Quy trình 8 role tuần tự: `local_build` → `stop_services` → `clean_data` (bỏ qua nếu
`--start`) → `node_setup` → `snapshot_restore` (chỉ khi `--restore-node`) → `node_exporter` →
`systemd_services` → `restart_services` (chỉ khi `--restart`, thay thế toàn bộ 7 role kia).
Chi tiết đầy đủ (khôi phục snapshot, thao tác riêng 1 node, giám sát chéo đa máy
`--all-monitors`, quản lý RPC proxy): `deploy/ansible/README.md`.

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
**Nếu chain của bạn bật `enable_private_gateway`**: nhớ truyền thêm `--random-gateway-bls-key`
(hoặc `--gateway-bls-key <hex tự quản lý>`) khi sinh config — mặc định không truyền cờ nào vẫn
dùng chung 1 khoá devnet công khai với mọi chain khác (xem
`note/security_variables_reference.md` mục 3.1).
**Lưu ý đã từng gặp lỗi thật:** khối JSON này dùng key dạng `snake_case`
(`root_anchor_rpc_urls`, không phải `RootAnchorRpcUrls`) — Go's `encoding/json` không báo lỗi
khi tên trường sai, nó chỉ âm thầm bỏ qua field và tắt luôn `GatewayRegistryMonitor`/
`CommitteeAttestationWorker` mà không có dấu hiệu gì trong log. Luôn xác nhận bằng cách xem
log node có dòng `✅ Committee Attestation Worker started` không, đừng tin config đã đúng chỉ
vì node khởi động không lỗi.

### 5.2 Lễ khai sinh Root Anchor (genesis ceremony) — con đường trust-minimized đúng nghĩa

Đây là quy trình **khuyến nghị cho production thật nhiều tổ chức**. Lưu ý: đây là ceremony
cho **genesis CONSENSUS (Rust)** — uỷ ban BFT thật của Root Anchor — hoàn toàn khác tầng với
`ChainRegistry` của `GatewayEngine` (Go), mà từ 2026-08-28 chỉ còn 1 đường ghi duy nhất là
`registerChainViaStake` (`bootstrapFoundingChains` đã bị xoá hẳn — xem mục 5.3 đã cập nhật).
Ceremony dưới đây **không một coordinator nào giữ khoá của ai khác**, và có bước xác minh
digest bắt buộc chống lệch genesis âm thầm.

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

### 5.3 SUPERSEDED (2026-08-28) — `bootstrapFoundingChains` đã bị xoá hẳn, không còn front-run risk để nói

Mục này (nguyên bản 2026-08-25) mô tả 1 rủi ro front-run thật của `bootstrapFoundingChains` —
hàm đó **đã bị xoá hoàn toàn khỏi codebase 2026-08-28**, cùng với `GenesisCoordinatorAddress`,
`founding_entry`/`assemble_root_anchor`'s ChainRegistry-bootstrap vai trò (không phải vai trò
genesis-consensus ở mục 5.2, vẫn còn dùng). Toàn bộ đoạn mô tả rủi ro dưới đây **không còn áp
dụng**, giữ lại làm nhật ký lịch sử — xem git history/`note/cross_chain_stake_and_value_flow.md`
nếu cần chi tiết quyết định retire.

**Cơ chế thay thế hiện tại (đơn giản hơn nhiều, đã đóng luôn class rủi ro này):**
`registerChainViaStake` là đường đăng ký DUY NHẤT vào `ChainRegistry` — của Root Anchor lẫn của
từng chain sáng lập — không cần coordinator, không cần gom file trước, không có "lần gọi đầu
khoá vĩnh viễn" để mà đua giành. Mỗi chain (kể cả Root Anchor tự đăng ký chính nó, xem
`RegisterChainViaStake`'s doc comment, `pkg/cross_chain/gateway.go`) tự gửi giao dịch
`registerChainViaStake` của riêng mình, trả tiền cọc thật từ chính ví của người gửi — không có
"payload công khai an toàn" nào của người KHÁC để front-run, vì tiền cọc luôn phải đến từ ví
của chính người gửi giao dịch đó. Không cần diễn tập/rehearsal riêng cho bước này nữa.

### 5.4 Sau khi Root Anchor sống: bật attestation + relayer

- `CommitteeAttestationWorker`/`GatewayRegistryMonitor` tự chạy kèm node nếu config đúng
  (xem CẢNH BÁO mục 5.1).
- Chạy `RelayerDaemon` (`cmd/tool/cross_chain_relayer`) — **permissionless, ai cũng chạy
  được**, nhưng cần khoá của **chính người vận hành relayer đó**. Binary này tự chạy 1
  goroutine `WatchChainPair` cho MỌI cặp (chain nguồn, chain đích) trong danh sách `-chains` —
  mỗi cặp tự poll `getPendingOutboundCount()`, tự gọi `batchOutboundCommit()` khi có message
  chờ, tự chờ `CommitAttestationWorker` sản xuất QuorumCert BLS thật, rồi tự gọi
  `attestCommit()`/`claimMessage()`. Không cần gọi tay bất kỳ bước trung gian nào nữa; chỉ cần
  chạy đúng lệnh dưới đây và để nó chạy nền liên tục. `start_relayer_daemon.sh`
  đọc khoá từ biến môi trường `RELAYER_KEY` — **bắt buộc phải set** trước khi chạy cho vai
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

- **Hiện trạng (đã sửa lại cho đúng 2026-08-27 — bản trước ghi nhầm tên biến môi trường):**
  - `RelayerDaemon` (binary `cross_chain_relayer`, chạy độc lập ngoài node): khoá secp256k1
    truyền qua cờ `-key`, hoặc `start_relayer_daemon.sh` đọc từ biến môi trường **`RELAYER_KEY`**
    rồi tự truyền vào `-key` (bản thân binary không tự đọc env var).
  - `CommitteeAttestationWorker` (chạy kèm mỗi node): đọc từ
    `CrossChainConfig.RootAnchorSubmitterPrivateKeyHex` trong `config.json`
    (`root_anchor_submitter_private_key_hex`) — giờ có thể override qua biến môi trường
    **`META_ROOT_ANCHOR_SUBMITTER_PRIVATE_KEY_HEX`** (mới thêm 2026-08-27, xem
    `note/security_variables_reference.md` mục 1 cho bảng đầy đủ mọi biến bí mật + override
    tương ứng — kể cả `Databases.BLSPrivateKey`, `gateway_bls_key`, `master_password`,
    `app_pepper` giờ đều có override, trước đó chỉ 3/9 trường có).
- **Mức độ rủi ro & Mô hình vận hành:**
  - **RelayerDaemon là permissionless:** Khoá của relayer chỉ dùng để trả phí gas khi submit giao dịch `verifyAndExecute` lên destination chain và nhận tip qua `withdrawRelayerTip`. Nếu khoá relayer bị lộ, kẻ tấn công **chỉ rút được số dư tip và gas của tài khoản đó**, KHÔNG thể giả mạo chữ ký BLS của uỷ ban hay rút trộm tài sản của người dùng (vì chữ ký uỷ ban và bằng chứng Merkle được xác thực độc lập bởi `GatewayPrecompile`).
  - **Submitter của CommitteeAttestationWorker:** Khoá này dùng để gửi `submitCommitteeAttestation` lên Root Anchor. Tương tự, nếu bị lộ thì chỉ mất gas của tài khoản submitter — chữ ký BLS share đính kèm bắt buộc phải khớp với `PublicKeyBls` của validator đã đăng ký trên `ChainRegistry`.
- **Khuyến nghị cho Mainnet:**
  - **Giai đoạn Testnet / Early Mainnet (giá trị custody nhỏ):** Chấp nhận lưu khoá qua secret manager (HashiCorp Vault, AWS Secrets Manager, Kubernetes Secrets) và inject qua biến môi trường dạng ephemeral runtime — dùng `EnvironmentFile=` của systemd (file `0600`, chỉ user `metanode` đọc được) trỏ tới các biến `META_*` ở mục trên, thay vì để khoá nằm trong `config.json` trên đĩa. `deploy/ansible`/`deploy/systemd` **hiện CHƯA làm việc này** (khoá vẫn nằm trong JSON) — đây là khuyến nghị cho bước kế tiếp, không phải hiện trạng.
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

- [ ] **Trên chính `dev` bạn sắp deploy**, chạy
      `git show origin/dev:execution/pkg/blockchain/tx_processor/gateway_handler_test.go
      | grep TestGatewayHandler_ConsecutiveTransactionsFromSameSenderAdvanceNonce` — fix bug
      treo chain (barrier-tx không tăng nonce) phải có mặt, đây là bug nghiêm trọng nhất từng
      tìm được.
- [ ] Tương tự, xác nhận các fix cấu hình bảo mật đã có trên `dev`:
      `git show origin/dev:execution/pkg/config/config.go | grep META_GATEWAY_BLS_KEY` và
      `git show origin/dev:deploy/ansible/roles/node_setup/tasks/main.yml | grep "mode: '0600'"`
      — cả 2 phải ra kết quả khớp, không rỗng.
- [ ] P5 — security review độc lập đã hoàn tất (mục 0).
- [ ] Đã chạy thật trên nhiều máy độc lập, không chỉ devnet 1 máy (mục 0, mục 6).
- [ ] Đã set `-key`/`RELAYER_KEY` (`start_relayer_daemon.sh`, `register_chains`) thành khoá
      thật của vai trò đó — không còn nơi nào rơi về khoá devnet công khai đóng cứng trong repo
      (mục 3, 5.4).
- [ ] Với mọi trường bí mật khác (`Databases.BLSPrivateKey`, `root_anchor_submitter_private_key_hex`,
      `gateway_bls_key`, `master_password`, `app_pepper`, `securepassword`): đã cân nhắc dùng
      biến môi trường `META_*` tương ứng thay vì để khoá thật nằm trong `config.json` trên đĩa
      (bảng đầy đủ + khuyến nghị `EnvironmentFile=`: `note/security_variables_reference.md`).
- [ ] Nếu chain có bật `enable_private_gateway`: `gateway_bls_key` được sinh riêng qua
      `--random-gateway-bls-key`/`--gateway-bls-key`, không phải giá trị devnet mặc định dùng
      chung cho mọi chain (mục 5.1, `security_variables_reference.md` mục 3.1).
- [ ] ~~`cross_chain.devnet_governance_timelock_seconds_override` KHÔNG được set~~ — MOOT
      (2026-09-04): trường này đã bị xoá khỏi code, `GovernanceEngine`/72h timelock không còn
      tồn tại nữa (mục 1). Không cần kiểm tra field này nữa.
- [ ] ~~Đã set `CrossChainConfig.GenesisCoordinatorAddress`~~ / ~~ceremony giảm thiểu front-run
      `bootstrapFoundingChains`~~ — MOOT (2026-08-28): cả field lẫn hàm đã bị xoá (mục 5.3).
- [ ] **`cross_chain.recovery_committee_json` được set từ khoá RecoveryCommittee sinh HOÀN
      TOÀN out-of-band/air-gapped** (`recovery_committee_json_override_file` trong
      `inventory.yml`) — **KHÔNG** dùng bộ khoá tự sinh của
      `deploy/systemd/gen_recovery_committee_keys.py` (mặc định devnet), và **KHÔNG** trùng
      với bất kỳ khoá validator/consensus nào của chính Root Anchor hay chain nào khác (tập
      trung quyền lực thật — phát hiện + vá 2026-09-04, xem
      `note/eurozone_unified_native_coin_plan.md`). RecoveryCommittee là bên duy nhất có thể
      `declareChainDeadWithCert`/`unregisterChainWithCert`/`updateCommitteeWithRecoveryCert`
      cho MỌI chain đã đăng ký — mất khoá này hoặc để nó trùng khoá consensus là mất toàn bộ
      ý nghĩa "cứu hộ tách biệt quyền lực".
- [ ] Mọi `config.json` khối `cross_chain` đã xác nhận field đúng snake_case bằng cách xem
      log thật (mục 5.1), không chỉ tin node khởi động không lỗi.
- [ ] Đã đọc và xử lý toàn bộ mục còn mở trong `note/cross_chain_production_readiness_plan.md`
      (roadmap P0-P8: P1-P4 đã xong — bao gồm relay tự động P4 và bug treo chain nghiêm trọng
      nhất từng tìm được — P5-P8 chưa làm; xem `note/cross_chain_root_anchor_architecture.md`
      mục 8).
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
| Vận hành 1 private chain đơn (Ansible), 8 role, đầy đủ flag | `deploy/ansible/README.md` |
| Cụm nhiều node 1 máy (systemd) | `deploy/systemd/docs/systemd-cluster.md` |
| Index script `deploy/systemd/` (script nào dùng/không dùng), devnet 1 máy | `deploy/systemd/README.md` |
| Khôi phục từ snapshot | `deploy/systemd/docs/restore_snapshot_systemd.md` |
| Chết node, phục hồi | `note/runbook_chain_death_recovery.md` |
| Số node tối thiểu cho BFT | `note/bft_fault_tolerance_node_count.md` |
| **Mọi biến bí mật (private key, password): field JSON, override env var, khuyến nghị** | **`note/security_variables_reference.md`** |
| Đánh giá bảo mật đã làm/chưa làm, toàn dự án | `note/security_assessment_status_report.md` |

---

## 9. Bàn giao triển khai (đọc nếu bạn mới tiếp quản việc này)

**Trạng thái hiện tại (2026-08-27):**
- Toàn bộ fix chức năng/bảo mật ở mục 0 đã có trên `dev` — xác nhận bằng checklist mục 7 trước
  khi deploy, đừng chỉ tin dòng chữ này.
- Không có gì đang chạy dở/hỏng — `go build/vet/test` sạch, không có regression nào chưa xử lý.
- **Đã xác nhận sống (không chỉ unit test):** một chu trình chuyển native coin liên chuỗi đầy
  đủ — `outbound()` → `batchOutboundCommit()` → QuorumCert BLS thật → `attestCommit()` →
  `claimMessage()` → số dư thật tăng đúng giá trị ở chain đích — chạy trọn vẹn, hoàn toàn tự
  động qua relayer, trên 1 devnet 9-tiến-trình (4 validator Root Anchor + 4 private chain +
  1 relayer) trên **1 máy** (chưa phải T2 nhiều máy thật).
- **T2 (nhiều máy thật) đang được thử** (chưa xác nhận thành công tại thời điểm viết) — nếu
  gặp `AddrInUse`/consensus panic khi khởi động Root Anchor, kiểm tra checkout có fix
  `peer_rpc_port` chưa (mục 0, dòng "Layer C") trước khi nghi ngờ code; nếu đã có mà vẫn lỗi,
  nhiều khả năng là process cũ từ lần chạy trước chưa dọn hết (`ss -tlnp` để tìm PID giữ cổng).

**Việc tiếp theo, theo đúng thứ tự ưu tiên:**
1. Xác định mục tiêu triển khai: testnet/demo (đã sẵn sàng, đi thẳng vào mục 3-5) hay mainnet
   giá trị thật (dừng lại lên kế hoạch cho 2 điều kiện chặn ở mục 0 — P5 và T2 — trước khi
   chạy thêm bất kỳ script nào).
2. Nếu mainnet: liên hệ đơn vị audit độc lập cho P5 (dùng
   `note/external_security_audit_scope_p5.md` làm tài liệu bàn giao cho họ), song song hoàn
   tất T2 (hạ tầng nhiều máy vật lý/VM độc lập đang được thử, xem trên).
3. Việc code còn lại (không chặn, nhưng nên làm trước khi coi Phase 1 là xong): giao
   `note/all_remaining_fixes_plan.md` cho 1 agent/dev khác — tài liệu đó đã liệt kê đầy đủ
   từng việc kèm file/hàm chính xác.

Toàn bộ các phát hiện governance/allocation (C6/C7/C8) và `pkg/devicekey/DeviceKey.go` (D6,
secret Telegram token hardcode + cơ chế device-activation) đã được xử lý dứt điểm 2026-08-27 —
xem `note/cross_chain_attack_scenario_catalog.md` để có danh sách đầy đủ + trạng thái từng mục.

**Nếu có nghi ngờ về bất cứ điều gì ở tài liệu này:** đọc trực tiếp
`note/cross_chain_production_readiness_plan.md` (log đầy đủ, trung thực, kể cả các lần kết
luận sai rồi tự sửa) thay vì tin lời tóm tắt — đây là quy ước xuyên suốt dự án: mọi khẳng định
"đã vá"/"đã xong" đều phải có bằng chứng thật (test hồi quy, log chạy sống) đi kèm, không phải
chỉ lời khẳng định.
