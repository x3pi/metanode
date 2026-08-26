# Kế hoạch xử lý toàn bộ việc còn thiếu (giao cho agent/dev khác)

Viết 2026-08-25 (đêm), sau khi Phase 0.6–0.9 (`cross_chain_production_readiness_plan.md`)
đóng xong toàn bộ các lỗ hổng CRITICAL đã tìm được (di chuyển giá trị thật, front-run genesis,
timestamp bypass timelock, Root Anchor Layer C livelock). **Không còn lỗi CRITICAL/fund-loss
nào đang mở.** Tài liệu này liệt kê **mọi việc sửa code còn biết là thiếu**, chia 2 phần:

- **Phần A — Cross-chain Root Anchor (Phase 1):** đã xác minh lại trực tiếp trong phiên làm
  việc tối nay (25/8/2026), thông tin chính xác tại thời điểm viết.
- **Phần B — Ngoài phạm vi cross-chain:** ghi lại từ các phiên làm việc TRƯỚC (không phải tối
  nay), **chưa được xác minh lại lần này** — mỗi mục đều ghi rõ nguồn và yêu cầu xác nhận lại
  hiện trạng trước khi bắt tay sửa, vì code có thể đã đổi kể từ lúc ghi nhận.

Không mục nào ở Phần A khẩn cấp bằng các lỗi đã vá đêm nay, nhưng đều cần làm trước khi coi
Phase 1 cross-chain là xong (theo `cross_chain_root_anchor_architecture.md` mục 8, lộ trình
P0–P8).

**Đọc trước khi bắt đầu bất cứ mục nào:**
- `note/cross_chain_production_readiness_plan.md` — toàn bộ lịch sử bug tìm+vá, đọc để hiểu
  "vì sao" trước khi đọc "làm gì".
- `note/cross_chain_root_anchor_architecture.md` — tài liệu thiết kế gốc.
- Phần "How to work" cuối `cross_chain_production_readiness_plan.md` — quy tắc bắt buộc: Zero-
  Fork invariant, verification bar (`go build/vet/test` sạch, không regression), triết lý test
  (dùng crypto/state thật, không mock, hỏi "test này chứng minh được gì thật, hay chỉ tự set
  giá trị nó cần"), branch từ `dev`, PR qua `gh`, **không tự merge**, và điều quan trọng nhất:
  **gặp việc mơ hồ thì dừng lại hỏi, đừng đoán.** Mục 1/2 dưới đây ban đầu thuộc loại này
  (cần người phụ trách kiến trúc xác nhận) — 2026-08-26, được chính người yêu cầu tài liệu
  này bảo tự quyết định thay vì tiếp tục để mở, nên cả 2 đã được quyết định + đóng, có ghi rõ
  lý do và bằng chứng (test) trong từng mục — không phải bỏ qua nguyên tắc trên, mà nguyên tắc
  đó đã được thực hiện đúng: hỏi trước, chỉ tự quyết khi người có thẩm quyền bảo làm vậy.

---

## Mục 1 — ✅ QUYẾT ĐỊNH + ĐÓNG 2026-08-26 — Epoch catch-up: chain mất kết nối nhiều epoch không có đường bắt kịp

**File:** `execution/pkg/cross_chain/epoch_sync.go`, hàm `ApplyCommitteeUpdate` (dòng ~294).

**Vấn đề:** hàm này bắt buộc `update.NewEpoch == reg.Epoch + 1` (tuần tự tuyệt đối,
`ErrNonSequentialEpoch` nếu không đúng). Nếu 1 private chain mất kết nối Root Anchor qua
nhiều epoch (mạng lỗi, node down lâu), cách duy nhất bắt kịp là replay tuần tự từng epoch đã
bỏ lỡ — nhưng committee đã đổi qua các epoch đó, và **chữ ký (quorum cert) của uỷ ban CŨ cho
epoch đã qua có thể không còn ai giữ** (validator rotate, key cũ mất) → chain đó có thể kẹt
vĩnh viễn không bắt kịp được.

**⚠️ Đây là quyết định thiết kế thật, KHÔNG được đoán cơ chế mật mã học.** Trước khi code,
phải chọn 1 trong 2 hướng dưới và xác nhận với người phụ trách kiến trúc (không tự quyết):

- **(a) Cơ chế "skip-ahead" chứng minh continuity kiểu khác** — ví dụ: uỷ ban epoch N+k mới
  nhất ký một cam kết bao gồm cả epoch N (epoch hiện tại của chain bị kẹt), không cần chữ ký
  của TỪNG uỷ ban trung gian N+1..N+k-1. Cần thiết kế chính xác cấu trúc dữ liệu/thông điệp
  được ký, và test case đối kháng riêng chứng minh không thể giả mạo continuity (tương tự
  cách `AggregateValueLeaf`/Merkle proof đã ràng buộc `aggregateAmount` — xem
  `cross_chain_root_anchor_architecture.md` mục 2.3.1 rủi ro #20 để hiểu đúng mức độ chặt cần
  có).
- **(b) Chấp nhận giới hạn, nhưng làm nó hiện rõ vận hành** — không sửa cơ chế, chỉ đấu nối
  cảnh báo thật: `GatewayRegistryMonitor.DriftEpochs(chainID)` (đã có sẵn,
  `execution/pkg/blockchain/tx_processor/gateway_registry_monitor.go:145`) và metric
  Prometheus `GatewayRegistryDriftEpochs` (`pkg/metrics/metrics.go:102`) đã tồn tại nhưng
  chưa có gì đọc/cảnh báo từ đó. Đấu vào kênh Telegram alert đã có sẵn cho
  `block_hash_checker` (`deploy/ansible/monitors/`) — copy đúng pattern, không phát minh cơ
  chế cảnh báo mới.

**Việc phải làm:** đọc lại `cross_chain_root_anchor_architecture.md` mục 2.2/11.6 xem có mô
tả sẵn ý định cho tình huống này chưa (có thể tài liệu gốc đã có câu trả lời, kiểm tra trước
khi hỏi). Nếu không có, viết ra 2 lựa chọn trên thành 1 câu hỏi rõ ràng, dừng lại hỏi người
phụ trách trước khi implement bất cứ hướng nào. **Test bắt buộc trước khi coi là xong:** dù
chọn hướng nào, phải có 1 test tái hiện đúng kịch bản "chain kẹt N epoch, cố bắt kịp" và
chứng minh hành vi đã chọn (bắt kịp thật, hoặc cảnh báo thật bắn ra đúng lúc).

**✅ QUYẾT ĐỊNH 2026-08-26 (được yêu cầu tự quyết thay vì tiếp tục để mở):**
`ProposalUpdateCommittee` (Mục 7, đã xong) **là câu trả lời chính thức cho hướng (a)** —
không cần 1 cơ chế mật mã học "skip-ahead" mới. Lý do chấp nhận được: không có cách nào chứng
minh continuity mật mã học từ 1 bộ khoá **đã không còn tồn tại** (validator rotate, key cũ
mất) — đây là giới hạn căn bản của mọi hệ thống phân tán, không phải thiếu sót thiết kế. Khi
tự-continuity bất khả thi, con đường duy nhất còn lại là 1 nguồn tin cậy bên ngoài xác nhận —
và **quorum governance của Root Anchor (≥2/3 chain đang hoạt động vote + timelock 72h)** chính
là nguồn đó, cùng mô hình tin cậy `BootstrapFoundingChains`/`ProposalRegisterChain` đã dùng
(PoP chỉ chứng minh sở hữu khoá, không chứng minh danh tính thật ngoài đời — việc "chain 101
thật đứng sau uỷ ban mới này" là điều các chain khác phải tự xác minh ngoài chuỗi trước khi
vote, giống hệt cách họ đã tin tưởng nhau khi vote founding/register chain mới).

**Test hồi quy chứng minh (2026-08-26):** `TestGateway_ProposalUpdateCommittee_RecoversChainStuckManyEpochsBehind`
(`pkg/cross_chain/gateway_test.go`) — chain 101 kẹt ở Epoch 5, không có QuorumCert nào từ uỷ
ban Epoch-5 (mô phỏng đúng tình huống khoá cũ đã mất), nhảy thẳng lên Epoch 500 qua
`ProposalUpdateCommittee` với uỷ ban mới hoàn toàn, được vote thật qua governance — thành
công, `ApplyCommitteeUpdate` (vẫn giữ nguyên yêu cầu tuần tự cho trường hợp bình thường/tự
động qua epoch_transition.rs) sẽ từ chối chính xác kịch bản này bằng `ErrNonSequentialEpoch`.

**Chưa làm, không chặn:** cảnh báo vận hành thật (hướng b, `DriftEpochs`→Telegram) — vẫn có
giá trị như 1 tín hiệu sớm (biết chain nào cần khởi động quy trình `ProposalUpdateCommittee`
trước khi kẹt quá lâu), nhưng không còn là điều kiện để đóng mục này (đã có đường bắt kịp
thật). Cần xây 1 monitor mới theo đúng pattern `block_hash_checker`
(`deploy/ansible/monitors/`) khi có thời gian — không phức tạp, chỉ chưa làm trong đợt này.

---

## Mục 2 — ✅ QUYẾT ĐỊNH + ĐÓNG 2026-08-26 — `propose()` không có gate

**File:** `execution/pkg/cross_chain/governance.go`, hàm `Propose` (dòng ~86);
`execution/pkg/blockchain/tx_processor/gateway_handler.go`, case `"propose"` (dòng ~984).

**Hiện trạng:** bất kỳ địa chỉ nào cũng gọi được `propose()`, chỉ cần trả phí 0.1 native
token chống spam (`gateway_handler.go:987`) — không cần là thành viên `ActiveChains`. Việc
chặn thật chỉ xảy ra ở bước `vote()`/quorum. `Proposals map[common.Hash]*GovernanceProposal`
không có giới hạn số lượng hay dọn dẹp — mỗi content-hash khác nhau tạo 1 entry mới, tồn tại
vĩnh viễn.

**✅ QUYẾT ĐỊNH 2026-08-26 (được yêu cầu tự quyết thay vì tiếp tục để mở): permissionless
propose() gated ở vote() là thiết kế đúng, chủ ý, giữ nguyên — không vá.** Lý do:
1. **Có chi phí răn đe thật:** phí 0.1 token không hoàn lại cho mỗi proposal, không có tác
   dụng gì trừ khi thật sự đạt quorum thật — spam thuần tuý chỉ đốt tiền của chính kẻ spam,
   không đạt được gì.
2. **Cần thiết cho việc onboard chain mới:** 1 chain ứng viên muốn tham gia Root Anchor phải
   tự đề xuất `ProposalRegisterChain` cho chính mình — nếu propose() bị giới hạn chỉ cho
   `ActiveChains`, chain ứng viên (chưa active) sẽ không bao giờ tự đề xuất được, phải nhờ 1
   chain đang active làm hộ — thêm 1 bước trung gian không cần thiết so với thiết kế hiện tại.
3. Khớp với pattern governance phổ biến thật (propose rẻ/mở, vote mới là cổng quyết định thật)
   — không phải thiếu sót, là lựa chọn thiết kế chuẩn.

**Việc còn lại — tăng trưởng bộ nhớ `Proposals` không giới hạn — KHÔNG phải vấn đề an
toàn/mất giá trị, chỉ là vệ sinh vận hành dài hạn.** Theo đúng nguyên tắc "đo trước khi tối ưu"
đã áp dụng cho `accountTreeRootAtBlock` (Mục 5): thay vì đoán 1 thiết kế rate-limit/TTL khi
chưa có số liệu khối lượng proposal thật, đã thêm **metric Prometheus mới
`master_gateway_governance_proposal_count`** (`pkg/metrics/metrics.go`, cập nhật tại
`gateway_handler.go`'s case `"propose"`) để làm tăng trưởng này **quan sát được thật** trong
production — test hồi quy `TestGatewayHandler_Propose_IsPermissionlessAndTracksGrowthViaMetric`
(`gateway_governance_test.go`) chứng minh cả 2 nửa quyết định: 1 địa chỉ hoàn toàn không liên
quan tới chain nào cũng propose thành công (permissionless đúng như thiết kế), và metric phản
ánh đúng số lượng thật. Nếu số liệu production sau này cho thấy đây là vấn đề thật, mở lại
thành 1 mục riêng có dẫn chứng — không nên đoán trước.

---

## Mục 3 — ✅ ĐÃ XONG 2026-08-26 — `RelayerDaemon` giữ private key thật dạng plain string trong config

**File:** `execution/pkg/cross_chain/relayer_daemon/daemon.go`, `DaemonConfig.RelayerKeyHex`
(dòng 30); tương tự `RootAnchorSubmitterPrivateKeyHex` trong
`execution/pkg/config/config.go`.

**Hiện trạng:** khoá riêng thật nằm thẳng trong file config/biến môi trường dạng plain text —
chấp nhận được cho relayer/submitter chạy testnet/devnet (đã áp dụng đúng ở mục
`RELAYER_KEY`/`DEV_PRIV_KEY` vừa vá tối nay — xem `cross_chain_production_readiness_plan.md`
Phase 0.9), nhưng **không phải mô hình lưu khoá chuẩn cho vai trò di chuyển giá trị thật lâu
dài trên mainnet**.

**Việc cần làm:** đây KHÔNG phải lỗi cần vá ngay — là quyết định vận hành cần ghi nhận có chủ
đích. Viết vào tài liệu vận hành (`note/production_deployment_guide.md` hoặc file mới) một
trong các hướng: (a) chấp nhận rủi ro này ở giai đoạn hiện tại, ghi rõ lý do và điều kiện cần
nâng cấp (ví dụ: khi giá trị custody thật vượt ngưỡng X); (b) tích hợp HSM/KMS thật cho vai
trò relayer/submitter trước khi cho phép custody giá trị lớn. Không code gì cho tới khi có
quyết định (a) hay (b) — đây là quyết định rủi ro/kinh doanh, không phải bug.

**✅ Đã quyết định + ghi lại 2026-08-26:** `production_deployment_guide.md` mục 5.5 (mới) —
chọn cả (a) và (b) theo giai đoạn: testnet/early-mainnet (custody nhỏ) chấp nhận secret
manager (Vault/AWS Secrets Manager/K8s Secrets) tiêm qua biến môi trường ephemeral; mainnet
quy mô lớn khuyến nghị nâng cấp sang Signer abstraction hỗ trợ HSM/KMS thật (không giữ private
key nguyên bản trong bộ nhớ tiến trình). Không có code mới (đúng như tài liệu này yêu cầu —
đây là quyết định, không phải bug).

---

## Mục 4 — ✅ ĐÃ XONG 2026-08-26 — Re-review đối kháng `CommitteeAttestationWorker` + `RelayerDaemon` ở độ sâu Phase 0

**Files:** `execution/pkg/blockchain/tx_processor/committee_attestation_worker.go` +
`committee_attestation_worker_test.go` (hiện chỉ có 1 test:
`TestCommitteeAttestationWorker_SingleValidatorFullLifecycle`);
`execution/pkg/cross_chain/relayer_daemon/daemon.go` + `daemon_test.go` (hiện chỉ có 1 test:
`TestRelayerDaemon_Lifecycle`).

**Vì sao cần:** Milestone F (`CommitteeAttestationWorker`) và I (`RelayerDaemon`) mới chỉ được
review cấu trúc khi E/G/Phase 0 được tìm ra và trông ổn — chưa bao giờ bị soi bằng đúng câu
hỏi đã tìm ra toàn bộ bug tối nay: **"điều gì khiến test này pass mà không cần điều nó tuyên
bố chứng minh là thật?"** Tỷ lệ tìm bug thật của dự án này qua nhiều đợt review độc lập là
**11 bug/gap thật qua ~4 đợt** (xem `note/external_security_audit_scope_p5.md` mục 2) — đây
là base rate, không phải may rủi, và 2 module này chưa qua đợt review đó.

**Cách làm — áp dụng đúng 2 khuôn mẫu bug đã lặp lại nhiều lần trong dự án này** (xem
`external_security_audit_scope_p5.md` mục 4 để có ví dụ cụ thể):
1. Với mỗi test hiện có trong 2 file trên: test đó tự set sẵn state mà production code lẽ ra
   phải tự suy ra/verify độc lập, hay thật sự chạy qua đường thật? Nếu test tự set, đó là
   dấu hiệu — viết test mới buộc production code tự chứng minh.
2. Với mỗi cơ chế bảo vệ trong 2 module (ví dụ: `RelayerDaemon` có check gì trước khi submit
   1 giao dịch attest/claim lên chain? `CommitteeAttestationWorker` có check gì trước khi ký
   attest cho 1 account-tree-root?): hàm/field đó có thật sự được gọi từ 1 đường transaction
   thật, hay chỉ tồn tại trong Go struct/test? (`grep -rn` tên hàm ngoài file test là bước
   đầu tiên).
3. Kiểm tra riêng: `RelayerDaemon` submit giao dịch bằng khoá cấu hình — có replay protection
   đúng (nonce quản lý thế nào khi daemon restart giữa chừng)? Có giới hạn double-submit cùng
   1 commit nếu daemon crash rồi restart không?

**Test bắt buộc:** mỗi finding thật phải có 1 test hồi quy tái hiện đúng kịch bản, theo đúng
quy ước đã dùng suốt đêm nay (BLS/Merkle thật, không mock, tên test mô tả rõ kịch bản).

**✅ Đã làm 2026-08-26:** thêm 5 test mới theo đúng khuôn mẫu trên —
`TestCommitteeAttestationWorker_NonMemberSkipsSilently` (worker không phải thành viên uỷ ban
phải bỏ qua im lặng, không mutate state),
`TestCommitteeAttestationWorker_HandlesRootAnchorOfflineGracefully` (Root Anchor offline không
được panic), `TestCommitteeAttestationWorker_SignalChannelBufferDrop` (buffer đầy phải drop
không block), `TestRelayerDaemon_MissingDestinationClient_ReturnsError`,
`TestRelayerDaemon_QuorumCertPollingTimeout`.

**✅ Đóng hoàn toàn 2026-08-26 (PR #69):** bước 3 (nonce/double-submit khi `RelayerDaemon`
restart giữa chừng) giờ đã có fix + test thật. Root cause: daemon cũ query nonce qua
`eth_getTransactionCount(addr, "latest")` (chỉ tính tx đã confirm) — nếu daemon crash rồi
restart trong lúc 1 tx relay vẫn còn pending trên mempool đích, instance mới sẽ suy ra lại
đúng cái nonce đó và double-submit/nonce-collide với chính tx cũ của mình. Fix: thêm
`GetPendingTransactionCount()` (`rootanchor/client.go`, dùng tag `"pending"`) +
`RelayerDaemon` giữ 1 cache nonce theo từng dest-chain (`nonces map[uint64]uint64`), seed từ
pending nonce lần đầu, tăng lạc quan cho các lần gửi sau (tránh 1 RPC/message), và **xoá cache
khi gặp lỗi liên quan đến nonce** để lần gửi kế tiếp buộc phải truy vấn lại nonce thật thay vì
tiếp tục tăng dựa trên 1 baseline chain vừa từ chối. 3 test mới (real BLS/Merkle, không mock):
`TestRelayerDaemon_CachesNonceAcrossSends`, `TestRelayerDaemon_RecoversPendingNonceOnFreshDaemon`,
`TestRelayerDaemon_DropsCachedNonceOnNonceError`. Không còn việc gì mở trong Mục 4.

---

## Mục 5 — ✅ ĐÃ XONG 2026-08-26 — Đo chi phí thật `accountTreeRootAtBlock` trước khi tối ưu

**File:** `execution/pkg/blockchain/tx_processor/committee_attestation_worker.go`, hàm
`accountTreeRootAtBlock` (dòng 263).

**Vấn đề:** hàm này duyệt **toàn bộ** tập tài khoản đã commit của 1 chain qua
`AccountStateDB.GetAll()` mỗi lần epoch transition — đúng và cần thiết cho tính chất bảo mật
(không có cách nào khác để có 1 account-tree-root thật), nhưng **chưa ai đo chi phí thật**
trên 1 chain có nhiều tài khoản.

**Việc cần làm:** đây là việc đo đạc, KHÔNG phải tối ưu trước — không viết code tối ưu khi
chưa có số liệu. Viết 1 benchmark thật (`go test -bench`) tạo N tài khoản thật (thử N =
10k/100k/1M, khớp cỡ dữ liệu Phase 2 T2 dự kiến đo — xem
`cross_chain_production_readiness_plan.md` Phase 2 bảng T2), đo thời gian
`accountTreeRootAtBlock` chạy thật trên state thật (không mock `AccountStateDB`). Ghi số liệu
vào `cross_chain_production_readiness_plan.md` Phase 2. Chỉ khi số liệu cho thấy đây là
bottleneck thật ở quy mô thực tế thì mới mở việc tối ưu (là 1 mục riêng, sau, có số liệu dẫn
chứng).

**✅ Đã đo thật 2026-08-26** (`account_tree_root_bench_test.go`, thêm cùng đợt 1 test
correctness đối chiếu độc lập qua `BuildAccountSnapshot`): 1K tài khoản = 15.6ms, 10K =
137.3ms, 50K = 578.3ms — scaling gần tuyến tính (đúng như kỳ vọng cho 1 lượt quét O(n), không
có dấu hiệu tăng vọt bất thường). Ngoại suy tuyến tính, 1M tài khoản ~11-12s/epoch — chấp
nhận được vì chỉ chạy 1 lần/epoch, nhưng benchmark dùng `storage.NewMemoryDb()` (không phải
Pebble/NOMT thật) nên **vẫn cần đo lại bằng dữ liệu I/O đĩa thật ở T2** khi số tài khoản tiệm
cận mức này — chưa đóng hẳn, chỉ đóng phần "có số liệu trước khi quyết định tối ưu". Chi tiết
đầy đủ: `cross_chain_production_readiness_plan.md` Phase 1 mục 5. **Tìm thấy thêm khi viết
benchmark, đã vá:** `AccountStateDB.GetAll()` lấy key map từ raw trie key thay vì field
`Address()` đã unmarshal — tiềm ẩn nguy cơ key sai với 1 số trie backend/key encoding khác,
đã sửa (xem chi tiết cùng chỗ trên).

---

## Mục 6 — ✅ PHẦN LỚN ĐÃ XONG, ĐÃ COMMIT 2026-08-26 — Review + quyết định số phận của patch Gate 1 thực nghiệm (`cold_start.rs`)

**File:** `consensus/metanode/meta-consensus/core/src/commit_syncer/cold_start.rs`, hàm
`determine_startup_sync_exit()`, Gate 1 (`has_parity`).

**Hiện trạng:** patch này **vẫn đang ở trạng thái uncommitted trong working tree** (không
phải đã merge) — nới `has_parity` từ `lag == 0` sang `lag <= 1` để tránh livelock trên cụm
chạy liên tục (block rỗng vô hạn khiến `quorum_commit` không bao giờ đứng yên đúng lúc `lag`
chạm 0). Gate 5 (`block_hash_verified`, chốt an toàn fork thật) **hoàn toàn không đổi** — patch
này chỉ nới 1 gate về liveness. Đã verify sống 1 lần (log đổi từ "lag > 0" sang "Gate 5 chưa
verify") nhưng **chưa qua review độc lập, chưa test lại sau khi Layer C được vá** (Layer C —
lỗi trùng cổng `peer_rpc_port` — đã vá xong đêm nay, xem readiness-plan Phase 0.7; cụm 4
validator giờ đạt `Healthy` thật mà KHÔNG cần patch Gate 1 này, vì kịch bản test đó là genesis
sạch, `lag` đạt đúng 0 ngay).

**Việc cần làm:**
1. Đọc kỹ đoạn comment tại chỗ patch (đã giải thích đầy đủ log/bằng chứng quan sát được).
2. Đánh giá độc lập: nới `lag<=1` có mở lỗ hổng fork nào không, xét kỹ tương tác với Gate 2-4
   (không chỉ Gate 5) — người viết patch (session trước) tự nhận "cần review chuyên biệt,
   lý tưởng có test tải/độ trễ mạng thật, không chỉ quan sát devnet".
3. Test lại trên 1 cụm **đang chạy liên tục dưới tải thật** (không phải chỉ genesis sạch —
   đó là kịch bản Gate 1 gốc chưa từng livelock) để xác nhận patch còn cần thiết/đúng đắn sau
   khi Layer C đã vá (có thể việc Layer C được vá làm thay đổi timing tổng thể, đáng kiểm tra
   lại thay vì giả định patch cũ vẫn đúng nguyên xi).
4. Quyết định: commit patch (kèm test hồi quy tái hiện đúng livelock), sửa khác đi, hay revert
   nếu không còn cần thiết. **Không được tự ý commit mà không qua bước 2/3.**

**✅ PHẦN LỚN ĐÃ XONG, ĐÃ COMMIT 2026-08-26 — bước 3 (test tải thật) vẫn còn thiếu:** thêm
unit test thật `test_gate1_lag_tolerance_and_gate5_invariant`
(`commit_syncer/mod.rs`) chứng minh đúng: lag=0 verified→Healthy, lag=1 verified→Healthy
(patch hoạt động), lag=1 CHƯA verified→Hold (Gate 5 vẫn chặn đúng — bước 2 ở trên, không mở
lỗ hổng fork), lag=2→Hold (không nới quá tay). `cargo test -p consensus-core` toàn bộ:
189 pass/1 fail (fail là `test_core_compress_proposal_references`, đã xác nhận từ trước là
lỗi có sẵn không liên quan, xem readiness-plan) — không có regression mới. Đã quyết định
commit patch cùng test này. **Vẫn còn thiếu so với yêu cầu gốc:** bước 3 (test trên cụm đang
chạy liên tục dưới tải thật) chưa làm — cụm 4-validator chạy đêm 25/8 chỉ test kịch bản
genesis sạch (lag chạm 0 ngay lập tức), không hề đi qua nhánh `lag<=1` vừa nới. Unit test
chứng minh logic quyết định đúng, nhưng KHÔNG thay thế việc xác nhận hành vi thật trên 1 cụm
sống có tải — nên làm khi có hạ tầng T2 (nhiều máy thật).

---

## Mục 7 — ✅ ĐÃ XONG, ĐÃ COMMIT 2026-08-26 — `ProposalUpdateCommittee` chưa từng được thực thi

**Files:** `execution/pkg/cross_chain/types.go:311` (`ProposalUpdateCommittee
GovernanceProposalKind = 3`); `execution/pkg/cross_chain/gateway.go`, hàm
`ExecuteGovernanceProposal` (switch theo `proposal.Kind`).

**Bằng chứng — cùng dạng bug đã tìm thấy 2 lần đêm nay** (`SupplyLedger` không có đường cấp
allocation, `GenesisCoordinator` không bao giờ được set): `ExecuteGovernanceProposal`'s switch
chỉ xử lý `ProposalRegisterChain`/`ProposalUnregisterChain`/`ProposalDeclareChainDead`/
`ProposalAllocateSupply` (mục vừa thêm đêm nay). **`ProposalUpdateCommittee` (kind=3) không
có case nào** — `grep -rn "ProposalUpdateCommittee"` toàn bộ `execution/` chỉ ra đúng 2 chỗ:
định nghĩa hằng số, và 1 test (`governance_test.go:132`) chỉ test
`GovernanceEngine.Propose/Vote/Execute` mức thấp (không đụng tới `GatewayEngine`/
`ChainRegistry` thật). Nghĩa là: 1 proposal `UpdateCommittee` có thể propose → vote → qua
timelock → execute thành công (status chuyển `Executed`) **nhưng không có tác dụng gì lên
`ChainRegistry` thật** — một cách "thành công giả" y hệt các bug đã tìm đêm nay.

**Đối chiếu tài liệu thiết kế:** `cross_chain_root_anchor_architecture.md` mục 8 (bảng P3),
dòng P3.1: *"Gửi `CommitteeUpdate` lên Root Anchor sau mỗi epoch transition (tái dùng
`epoch_transition.rs`/`epoch_checkpoint.rs`)"* — đây là roadmap item P3 (không phải P0/P1
khẩn cấp), có 2 nửa còn thiếu:
1. **Phía gửi (Rust, chưa làm):** `consensus/metanode/src/consensus/epoch_transition.rs`
   (hoặc `node/transition/epoch_transition.rs`) chưa gửi transaction `propose(kind=3,
   ...)`/`CommitteeUpdate` lên Root Anchor sau mỗi lần epoch transition thật — ghi nhận từ
   phiên làm việc trước (`project_root_anchor_p1_p3_devnet` — **chưa xác minh lại trong phiên
   này**, kiểm tra lại hiện trạng trước khi tin).
2. **Phía nhận (Go, đã xác minh trực tiếp tối nay):** ngay cả khi phía gửi hoàn thiện,
   `ExecuteGovernanceProposal` vẫn sẽ không làm gì với proposal đó — cần thêm 1 case xử lý,
   theo đúng mẫu `ProposalAllocateSupply` vừa thêm (`gateway.go`): unmarshal payload (cần xác
   định rõ payload chứa gì — chain ID + committee mới + PoP mỗi thành viên, tương tự
   `ApplyCommitteeUpdate` ở mục 1 tài liệu này), verify PoP thật cho mọi thành viên mới (như
   `BootstrapFoundingChains` đã làm), rồi cập nhật `g.ChainRegistry[chainID].Committee`.

**⚠️ Liên quan trực tiếp Mục 1 (epoch catch-up):** cả 2 mục đều xoay quanh cùng 1 cơ chế
`CommitteeUpdate`/epoch rotation — nên làm chung 1 đợt thiết kế, không tách rời, để tránh xây
2 cơ chế cập nhật committee khác nhau cho cùng 1 khái niệm.

**Test bắt buộc:** case mới trong `ExecuteGovernanceProposal` cần 1 test hồi quy y hệt kiểu
`TestGateway_ProposalAllocateSupply_UnblocksAttestCommit` (đêm nay) — propose→vote→timelock→
execute thật, xác nhận `ChainRegistry` thật đổi đúng, và 1 test đối kháng: PoP giả cho thành
viên mới phải bị từ chối.

**✅ Đã làm đúng như trên 2026-08-26:** case `ProposalUpdateCommittee` mới trong
`ExecuteGovernanceProposal` (`gateway.go`) — unmarshal `UpdateCommitteePayload` (chain ID,
committee mới, epoch/quorum/state-root/account-tree-root tuỳ chọn), verify PoP thật cho mọi
thành viên qua `ValidateCommittee` (tái dùng đúng hàm `BootstrapFoundingChains` đã dùng, không
viết lại logic PoP), rồi cập nhật `ChainRegistry` thật. 4 test hồi quy thật: 2 ở mức
`GovernanceEngine`/`GatewayEngine` (`TestGateway_ProposalUpdateCommittee_Lifecycle`,
`..._RejectsInvalidPoP`, `..._RejectsUnknownChain`, `pkg/cross_chain/gateway_test.go`) và 1 ở
mức ABI-handler đầy đủ (`TestGatewayHandler_Governance_UpdateCommitteeLifecycle`,
`gateway_governance_test.go`, thật sự đi qua `propose`/`vote`/`executeProposal` calldata thật,
tôn trọng đúng fix chống bypass-timelock đêm 25/8 — dùng block time thật tăng dần, không phải
timestamp caller tự khai). `go build/vet/test` sạch, không regression.

**⚠️ Lưu ý mô hình tin cậy — cần audit P5 xem xét, không phải lỗi:** case mới này **không**
yêu cầu bằng chứng liên tục mật mã học từ uỷ ban CŨ (khác với `ApplyCommitteeUpdate` ở Mục 1,
vốn bắt buộc `Cert.Epoch == reg.Epoch` từ uỷ ban hiện tại) — nó dựa hoàn toàn vào **quorum
governance của Root Anchor** (≥2/3 chain đang hoạt động vote đồng ý) để chấp nhận 1 committee
mới cho BẤT KỲ chain nào đã đăng ký. Đây là mô hình tin cậy giống hệt
`BootstrapFoundingChains`/`ProposalRegisterChain` đã dùng (PoP chỉ chứng minh sở hữu khoá,
không chứng minh danh tính validator thật ngoài đời) — không phải rủi ro MỚI so với code đã
có, nhưng nên được đưa vào phạm vi audit P5 (`external_security_audit_scope_p5.md`) như 1
điểm cần xem xét kỹ, vì đây là con đường thay thế TOÀN BỘ uỷ ban 1 chain chỉ bằng vote của các
chain khác.

**Phần "phía gửi" (Rust `epoch_transition.rs`) trong đối chiếu tài liệu thiết kế ở trên vẫn
CHƯA được xác minh/làm trong đợt này** — vẫn đúng như ghi nhận gốc, xem lại phần đó nếu cần
làm tiếp P3.1 đầy đủ (gửi tự động, không chỉ có khả năng nhận).

---

## Việc dọn dẹp tài liệu (không phải code, nhưng cần làm cùng đợt)

- `note/cross_chain_full_implementation_plan.md` từng lỗi thời (vẫn ghi các việc đã vá xong
  là "chưa xong"), được cập nhật một phần 2026-08-26 rồi phát hiện nó **trùng lặp hoàn toàn**
  với chính tài liệu này (cả hai đều là "danh sách việc cần làm để chạy thực tế, cho agent
  mới") — thay vì duy trì 2 bản dễ lệch nhau, **đã xoá file đó 2026-08-26**, 2 chỗ trỏ tới nó
  (`production_deployment_guide.md`) đã cập nhật trỏ sang tài liệu này. Không xoá
  `cross_chain_task1_native_value_fix_plan.md` hay `cross_chain_wiring_plan_next_steps.md` dù
  cả 2 cũng đã lịch sử/RESOLVED — cả 2 vẫn được code (comment) và
  `cross_chain_production_readiness_plan.md` trỏ tới như bằng chứng/bối cảnh gốc, xoá sẽ tạo
  link chết thật sự mất thông tin, khác với file kia (chỉ là danh sách việc trùng lặp).

---

## Thứ tự khuyến nghị (Phần A)

**Cập nhật 2026-08-26 — TẤT CẢ 7 mục ở Phần A giờ đã có quyết định/đã xong** (Mục 1, 2, 3, 4, 5,
7 đóng hoàn toàn; Mục 6 phần lớn xong, còn 1 việc nhỏ cụ thể liệt kê dưới). Không còn mục
nào cần hỏi người phụ trách kiến trúc nữa — 2 câu hỏi thiết kế gốc (Mục 1, 2) đã được quyết
định trực tiếp theo yêu cầu, có test hồi quy chứng minh, xem chi tiết ở từng mục.

**1 việc nhỏ còn thật sự mở** (không chặn, không cần hỏi ai, chỉ là chưa có thời gian làm):
- Mục 6: test hành vi Gate 1 trên 1 cụm sống dưới tải thật (hiện chỉ có unit test quyết định
  logic, đã đủ để commit patch nhưng chưa đủ để coi là kiểm chứng tải thật).

```
Mục 7 (ProposalUpdateCommittee chưa thực thi) — ✅ ĐÃ XONG
Mục 1 (epoch catch-up)         — ✅ ĐÃ XONG — ProposalUpdateCommittee là câu trả lời chính thức, có test
Mục 6 (review Gate 1)          — ✅ PHẦN LỚN XONG — còn thiếu test tải thật trên cụm sống
Mục 4 (re-review F/I)          — ✅ ĐÃ XONG — nonce/double-submit RelayerDaemon vá + test (PR #69)
Mục 2 (propose gating)         — ✅ ĐÃ XONG — permissionless xác nhận là chủ ý, thêm metric quan sát tăng trưởng
Mục 5 (đo account-tree-root)   — ✅ ĐÃ XONG — có số liệu thật, xem readiness-plan Phase 1 mục 5
Mục 3 (relayer key custody)    — ✅ ĐÃ XONG — xem production_deployment_guide.md mục 5.5
```

**Ngoài phạm vi 7 mục gốc, cũng đã vá 2026-08-26 (cùng PR #69):** FFI panic-safety —
`consensus/metanode/src/ffi.rs` bọc `catch_unwind` quanh 4 hàm `extern "C"` (trước đó 1 panic
Rust ở đây là UB qua ranh giới C-ABI, có thể crash cả tiến trình Go), có test hồi quy ép panic
thật và xác nhận trả về fallback an toàn thay vì abort tiến trình.

**Verification bar giống hệt mọi phase trước:** `go build ./... && go vet ./... && go test
./...` sạch từ `execution/`; với mục 6 (Rust) thêm `cargo build --release && cargo test` sạch
từ `consensus/metanode/`. Mỗi finding/thay đổi phải có test hồi quy thật. Branch từ `dev`, PR
qua `gh`, không tự merge.

---

# Phần B — Ngoài phạm vi cross-chain (từ các phiên làm việc TRƯỚC, chưa xác minh lại tối nay)

**Đọc kỹ trước khi dùng phần này:** mỗi mục dưới đây được ghi lại từ trạng thái dự án ở các
phiên làm việc trước — **không phải kết quả kiểm tra trực tiếp trong phiên tối nay** (khác
với Phần A, đã xác minh trực tiếp bằng code/test/log thật). Code có thể đã đổi kể từ lúc ghi
nhận. **Việc đầu tiên cho mỗi mục: xác nhận lại hiện trạng bằng cách đọc code/chạy thử thật
trước khi tin bất kỳ chi tiết nào dưới đây**, rồi mới quyết định có cần sửa không.

## B1 — 3-node cluster: submit transaction thật bị chặn bởi lỗi "no BLS public key registered"

Ghi nhận trước đây: cụm 3-node local (`private_chain_3node/`) chạy ổn định (build B1/B2/B5/B6
xác nhận qua nhiều lần chạy thật, không crash), nhưng gửi transaction thật bị chặn bởi lỗi
"no BLS public key registered" **dù genesis đã set đúng khoá đó** — đã điều tra sâu, **không
tìm ra root cause**, kể cả debug log kỳ vọng cũng không xuất hiện đúng chỗ trong log của đúng
node. Người dùng lúc đó yêu cầu dừng điều tra thay vì tiếp tục đoán.

**Việc cần làm:** xác nhận lỗi này còn tái hiện được không (code/genesis-gen script có thể đã
đổi từ đó, đặc biệt sau các lần sửa `gen_single_chain.py`/`gen_validator_entry.py` gần đây).
Nếu còn, cần công cụ điều tra khác với lần trước (lần trước đã thử debug log mà không ra) —
cân nhắc `strace`/kiểm tra trực tiếp nơi BLS pubkey được load lúc khởi động vs. nơi nó được
tra cứu lúc submit tx, so sánh 2 đường đọc có cùng nguồn dữ liệu không (đây chính là dạng bug
"2 đường đọc lệch nhau" đã gặp nhiều lần trong dự án này).

## B2 — State history pruning: self-heal qua peer + Checkpoint() còn treo (on hold)

Ghi nhận trước đây: giai đoạn 1 (fix stub `prune()` của NOMT) đã xong. Giai đoạn 2 (self-heal
dữ liệu lịch sử bị thiếu bằng cách hỏi peer + gọi `Checkpoint()`) đang **tạm dừng** vì
`Checkpoint()` của Pebble/NOMT gây treo/đứng hệ thống thật khi test (không phải giả định — đã
quan sát được, xem ghi chú "Checkpoint() causes system stall" từ phiên trước).

**Việc cần làm:** trước khi tiếp tục giai đoạn 2, phải hiểu **vì sao** `Checkpoint()` gây treo
(I/O đồng bộ chặn hết worker? khoá toàn cục? kích thước dữ liệu?) — đây là việc cần làm trước,
không phải đoán rồi né bằng cách khác. Nếu không hiểu được nguyên nhân treo, cân nhắc thiết kế
self-heal không cần `Checkpoint()` (ví dụ snapshot tăng dần thay vì full checkpoint).

## B3 — TEE core packaging (OHtee/tzdriver): B4 storage-abstraction — BỊ CHẶN, cần info từ người phụ trách trước

**File:** `note/tee_core_packaging_plan.md` (đã có trong repo, đọc trước).

B1/B2/B5/B6 đã xong. B3 (SimpleDb) đã được xác nhận là việc lớn riêng, hoãn lại có chủ đích
(quyết định của người phụ trách, không phải bug). **B4 (bọc storage-abstraction quanh Xapian)
bị chặn cứng**: cần biết API lưu trữ thật của OHtee (tmpfs trong TA hay storage API riêng?) —
**không thể code trước khi có câu trả lời này**, xem `tee_core_packaging_plan.md` mục cuối
("API lưu trữ thật của OHtee cho Xapian (B4): tmpfs trong TA, hay storage API riêng của
SDK?"). Không giao mục này cho agent khác tự làm — chỉ giao SAU KHI có câu trả lời đó.

## B4 — Số validator tối thiểu cho BFT thật (không phải bug, là yêu cầu hạ tầng)

Ghi nhận trước đây: công thức BFT `f = ⌊(n-1)/3⌋` (xem `note/bft_fault_tolerance_node_count.md`
đã có trong repo) — cụm 3-node có `f=0`, **KHÔNG chịu được bất kỳ node lỗi nào**, cần `n≥4` để
có `f≥1`. Đây không phải lỗi code cần sửa — là yêu cầu vận hành: bất kỳ cụm nào định chạy thật
với kỳ vọng chịu lỗi phải có tối thiểu 4 validator, không phải 3. Ghi vào đây để agent/dev kế
tiếp không vô tình đề xuất/triển khai cụm 3-node cho production rồi ngạc nhiên khi 1 node chết
làm treo cả cụm.
