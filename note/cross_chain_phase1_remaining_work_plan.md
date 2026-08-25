# Kế hoạch xử lý các mục còn mở — Phase 1 Root Anchor Cross-Chain (giao cho agent/dev khác)

Viết 2026-08-25 (đêm), sau khi Phase 0.6–0.9 (`cross_chain_production_readiness_plan.md`)
đóng xong toàn bộ các lỗ hổng CRITICAL đã tìm được (di chuyển giá trị thật, front-run genesis,
timestamp bypass timelock, Root Anchor Layer C livelock). **Không còn lỗi CRITICAL/fund-loss
nào đang mở** — tài liệu này chỉ liệt kê các mục Phase 1 còn lại: một số cần quyết định thiết
kế trước khi code, một số cần review sâu chưa làm, một số cần đo đạc thật trước khi tối ưu.
Không mục nào khẩn cấp bằng các lỗi đã vá đêm nay, nhưng đều cần làm trước khi coi Phase 1 là
xong (theo `cross_chain_root_anchor_architecture.md` mục 8, lộ trình P0–P8).

**Đọc trước khi bắt đầu bất cứ mục nào:**
- `note/cross_chain_production_readiness_plan.md` — toàn bộ lịch sử bug tìm+vá, đọc để hiểu
  "vì sao" trước khi đọc "làm gì".
- `note/cross_chain_root_anchor_architecture.md` — tài liệu thiết kế gốc.
- Phần "How to work" cuối `cross_chain_production_readiness_plan.md` — quy tắc bắt buộc: Zero-
  Fork invariant, verification bar (`go build/vet/test` sạch, không regression), triết lý test
  (dùng crypto/state thật, không mock, hỏi "test này chứng minh được gì thật, hay chỉ tự set
  giá trị nó cần"), branch từ `dev`, PR qua `gh`, **không tự merge**, và điều quan trọng nhất:
  **gặp việc mơ hồ thì dừng lại hỏi, đừng đoán** — 2 trong 6 mục dưới đây thuộc loại này.

---

## Mục 1 — 🟡 Epoch catch-up: chain mất kết nối nhiều epoch không có đường bắt kịp

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

---

## Mục 2 — 🟡 `propose()` không có gate — xác nhận chủ ý hay thiếu sót

**File:** `execution/pkg/cross_chain/governance.go`, hàm `Propose` (dòng ~86);
`execution/pkg/blockchain/tx_processor/gateway_handler.go`, case `"propose"` (dòng ~984).

**Hiện trạng:** bất kỳ địa chỉ nào cũng gọi được `propose()`, chỉ cần trả phí 0.1 native
token chống spam (`gateway_handler.go:987`) — không cần là thành viên `ActiveChains`. Việc
chặn thật chỉ xảy ra ở bước `vote()`/quorum. `Proposals map[common.Hash]*GovernanceProposal`
không có giới hạn số lượng hay dọn dẹp — mỗi content-hash khác nhau tạo 1 entry mới, tồn tại
vĩnh viễn.

**⚠️ Cần xác nhận trước, không tự quyết:** đọc `cross_chain_root_anchor_architecture.md` mục
11.6 xem thiết kế gốc có nói rõ "permissionless propose, gated ở vote" là chủ ý không. Nếu
tài liệu không rõ, hỏi người phụ trách kiến trúc câu hỏi đúng: *"propose() permissionless
hoàn toàn có phải chủ ý? Nếu có, cần giới hạn chống phình bộ nhớ (rate-limit theo địa chỉ,
TTL dọn proposal hết hạn, hay cứ để phí 0.1 token là đủ) trước mainnet không?"*

**Nếu xác nhận cần vá:** đây là vấn đề griefing/chi phí lưu trữ, KHÔNG phải mất giá trị — ưu
tiên thấp hơn mục 1. Test bắt buộc: chứng minh rate-limit/TTL hoạt động đúng mà không chặn
nhầm 1 chain hợp lệ đề xuất nhiều proposal thật trong thời gian ngắn (ví dụ registerAsset
nhiều lần liên tiếp lúc mới tham gia).

---

## Mục 3 — 🟡 `RelayerDaemon` giữ private key thật dạng plain string trong config

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

---

## Mục 4 — Re-review đối kháng `CommitteeAttestationWorker` + `RelayerDaemon` ở độ sâu Phase 0

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

---

## Mục 5 — Đo chi phí thật `accountTreeRootAtBlock` trước khi tối ưu

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

---

## Mục 6 — Review + quyết định số phận của patch Gate 1 thực nghiệm (`cold_start.rs`)

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

---

## Việc dọn dẹp tài liệu (không phải code, nhưng cần làm cùng đợt)

- `note/cross_chain_full_implementation_plan.md` **đang lỗi thời** — vẫn ghi Task 1.2/Task 2
  (custom-asset value wiring, front-run bootstrap) là "chưa xong", nhưng cả 2 đã vá xong đêm
  25/8/2026 (xem `cross_chain_production_readiness_plan.md` Phase 0.7–0.9). Cập nhật lại toàn
  bộ bảng trạng thái + thứ tự Task cho khớp, và thêm 6 mục ở tài liệu này vào đúng vị trí
  trong lộ trình Task 1→6 hiện có (các mục 1–5 ở đây thuộc "Task 3 — Phase 1 còn mở"; mục 6
  là 1 mục mới, không có trong file đó).

---

## Thứ tự khuyến nghị

Không mục nào chặn mục khác (khác với Task 1→6 trong `cross_chain_full_implementation_plan.md`
cũ, vốn có phụ thuộc tuần tự) — có thể làm song song hoặc theo độ ưu tiên rủi ro:

```
Mục 6 (review Gate 1)         — làm trước nếu định chạy T2 nhiều máy sớm, vì ảnh hưởng liveness thật
Mục 4 (re-review F/I)         — cùng nhóm rủi ro với các bug đã tìm thấy, ưu tiên kế tiếp
Mục 1 (epoch catch-up)        — cần quyết định thiết kế trước, bắt đầu bằng việc hỏi
Mục 2 (propose gating)        — cần xác nhận trước, việc nhỏ sau khi có câu trả lời
Mục 5 (đo account-tree-root)  — làm cùng lúc/sau Phase 2 T2 (cần hạ tầng đo)
Mục 3 (relayer key custody)   — quyết định vận hành, không chặn kỹ thuật, làm bất cứ lúc nào
```

**Verification bar giống hệt mọi phase trước:** `go build ./... && go vet ./... && go test
./...` sạch từ `execution/`; với mục 6 (Rust) thêm `cargo build --release && cargo test` sạch
từ `consensus/metanode/`. Mỗi finding/thay đổi phải có test hồi quy thật. Branch từ `dev`, PR
qua `gh`, không tự merge.
